package artifacts

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

// Artifact is a structured store for generated reports, specs, plans, drafts,
// and reusable documents. It provides a durable output surface distinct from
// memory, bookmarks, and workspace files.
type Artifact struct {
	ID        string   `json:"id"`
	PeerID    string   `json:"peer_id"`
	Kind      string   `json:"kind"`
	Title     string   `json:"title"`
	Content   string   `json:"content"`
	Labels    []string `json:"labels,omitempty"`
	CreatedAt int64    `json:"created_at"`
	UpdatedAt int64    `json:"updated_at"`
}

// Input holds the mutable fields used when creating an artifact.
type Input struct {
	Kind    string
	Title   string
	Content string
	Labels  []string
}

// Patch carries optional fields for a partial update.
type Patch struct {
	Kind    *string
	Title   *string
	Content *string
	Labels  *[]string
}

// Store persists artifacts in a local SQLite database.
type Store struct {
	db *sql.DB
}

// New opens (or creates) the artifacts SQLite database at dbPath.
func New(dbPath string) (*Store, error) {
	db, err := sql.Open("sqlite", dbPath+"?_journal_mode=WAL&_foreign_keys=on")
	if err != nil {
		return nil, fmt.Errorf("artifacts: open db: %w", err)
	}
	db.SetMaxOpenConns(1)
	if err := migrate(db); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("artifacts: migrate: %w", err)
	}
	return &Store{db: db}, nil
}

// Close releases the database connection.
func (s *Store) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

// Create inserts a new artifact for peerID and returns the saved record.
func (s *Store) Create(ctx context.Context, peerID string, input Input) (*Artifact, error) {
	a, err := newArtifact(strings.TrimSpace(peerID), input)
	if err != nil {
		return nil, err
	}
	_, err = s.db.ExecContext(ctx,
		`INSERT INTO artifacts(id, peer_id, kind, title, content, labels, created_at, updated_at)
		 VALUES (?,?,?,?,?,?,?,?)`,
		a.ID, a.PeerID, a.Kind, a.Title, a.Content,
		strings.Join(a.Labels, ","), a.CreatedAt, a.UpdatedAt)
	if err != nil {
		return nil, fmt.Errorf("artifact create: %w", err)
	}
	return a, nil
}

// Get returns a single artifact by ID, verifying peer ownership.
func (s *Store) Get(ctx context.Context, peerID, id string) (*Artifact, error) {
	a, err := scanArtifact(s.db.QueryRowContext(ctx,
		`SELECT id, peer_id, kind, title, content, COALESCE(labels,''), created_at, updated_at
		   FROM artifacts WHERE id = ?`,
		strings.TrimSpace(id)))
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("artifact %s not found", id)
	}
	if err != nil {
		return nil, fmt.Errorf("artifact get: %w", err)
	}
	if a.PeerID != strings.TrimSpace(peerID) {
		return nil, fmt.Errorf("artifact %s does not belong to peer", id)
	}
	return &a, nil
}

// List returns artifacts for peerID, optionally filtered by kind, ordered by recency.
func (s *Store) List(ctx context.Context, peerID, kind string, limit int) ([]Artifact, error) {
	if limit <= 0 {
		limit = 20
	}
	peerID = strings.TrimSpace(peerID)
	kind = strings.TrimSpace(kind)
	var rows *sql.Rows
	var err error
	if kind != "" {
		rows, err = s.db.QueryContext(ctx,
			`SELECT id, peer_id, kind, title, content, COALESCE(labels,''), created_at, updated_at
			   FROM artifacts
			  WHERE peer_id = ? AND kind = ?
			  ORDER BY updated_at DESC
			  LIMIT ?`,
			peerID, kind, limit)
	} else {
		rows, err = s.db.QueryContext(ctx,
			`SELECT id, peer_id, kind, title, content, COALESCE(labels,''), created_at, updated_at
			   FROM artifacts
			  WHERE peer_id = ?
			  ORDER BY updated_at DESC
			  LIMIT ?`,
			peerID, limit)
	}
	if err != nil {
		return nil, fmt.Errorf("artifact list: %w", err)
	}
	defer rows.Close()
	return collectArtifacts(rows)
}

// Update applies a partial patch to an existing artifact and returns the updated record.
func (s *Store) Update(ctx context.Context, peerID, id string, patch Patch) (*Artifact, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("artifact update: begin tx: %w", err)
	}
	defer tx.Rollback()

	a, err := loadArtifactTx(ctx, tx, peerID, id)
	if err != nil {
		return nil, err
	}
	updated := applyPatch(*a, patch)
	updated.UpdatedAt = time.Now().Unix()

	if _, err := tx.ExecContext(ctx,
		`UPDATE artifacts SET kind = ?, title = ?, content = ?, labels = ?, updated_at = ?
		  WHERE id = ? AND peer_id = ?`,
		updated.Kind, updated.Title, updated.Content,
		strings.Join(updated.Labels, ","), updated.UpdatedAt,
		updated.ID, updated.PeerID); err != nil {
		return nil, fmt.Errorf("artifact update: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("artifact update: commit: %w", err)
	}
	return &updated, nil
}

// migrate ensures the schema is up to date.
func migrate(db *sql.DB) error {
	_, err := db.Exec(`CREATE TABLE IF NOT EXISTS artifacts (
	id         TEXT PRIMARY KEY,
	peer_id    TEXT NOT NULL,
	kind       TEXT NOT NULL DEFAULT '',
	title      TEXT NOT NULL,
	content    TEXT NOT NULL,
	labels     TEXT NOT NULL DEFAULT '',
	created_at INTEGER NOT NULL,
	updated_at INTEGER NOT NULL
	);
	CREATE INDEX IF NOT EXISTS idx_artifacts_peer_updated ON artifacts(peer_id, updated_at DESC);
	CREATE INDEX IF NOT EXISTS idx_artifacts_peer_kind ON artifacts(peer_id, kind);`)
	return err
}

// helpers -----------------------------------------------------------------

func newArtifact(peerID string, input Input) (*Artifact, error) {
	id, err := randomID()
	if err != nil {
		return nil, fmt.Errorf("artifact id: %w", err)
	}
	title := strings.TrimSpace(input.Title)
	if title == "" {
		return nil, fmt.Errorf("title is required")
	}
	now := time.Now().Unix()
	return &Artifact{
		ID:        id,
		PeerID:    peerID,
		Kind:      strings.TrimSpace(input.Kind),
		Title:     title,
		Content:   strings.TrimSpace(input.Content),
		Labels:    normaliseLabels(input.Labels),
		CreatedAt: now,
		UpdatedAt: now,
	}, nil
}

func applyPatch(a Artifact, patch Patch) Artifact {
	if patch.Kind != nil {
		a.Kind = strings.TrimSpace(*patch.Kind)
	}
	if patch.Title != nil {
		a.Title = strings.TrimSpace(*patch.Title)
	}
	if patch.Content != nil {
		a.Content = strings.TrimSpace(*patch.Content)
	}
	if patch.Labels != nil {
		a.Labels = normaliseLabels(*patch.Labels)
	}
	return a
}

type rowScanner interface {
	Scan(dest ...any) error
}

func scanArtifact(scanner rowScanner) (Artifact, error) {
	var a Artifact
	var labels string
	err := scanner.Scan(
		&a.ID, &a.PeerID, &a.Kind, &a.Title, &a.Content,
		&labels, &a.CreatedAt, &a.UpdatedAt)
	if err != nil {
		return Artifact{}, err
	}
	if labels != "" {
		a.Labels = strings.Split(labels, ",")
	}
	return a, nil
}

func collectArtifacts(rows *sql.Rows) ([]Artifact, error) {
	var out []Artifact
	for rows.Next() {
		a, err := scanArtifact(rows)
		if err != nil {
			return nil, fmt.Errorf("artifact scan: %w", err)
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

func loadArtifactTx(ctx context.Context, tx *sql.Tx, peerID, id string) (*Artifact, error) {
	a, err := scanArtifact(tx.QueryRowContext(ctx,
		`SELECT id, peer_id, kind, title, content, COALESCE(labels,''), created_at, updated_at
		   FROM artifacts WHERE id = ? AND peer_id = ?`,
		strings.TrimSpace(id), strings.TrimSpace(peerID)))
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("artifact %s not found", id)
	}
	if err != nil {
		return nil, fmt.Errorf("artifact load: %w", err)
	}
	return &a, nil
}

func normaliseLabels(labels []string) []string {
	var out []string
	for _, l := range labels {
		l = strings.TrimSpace(l)
		if l != "" {
			out = append(out, l)
		}
	}
	return out
}

func randomID() (string, error) {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}
