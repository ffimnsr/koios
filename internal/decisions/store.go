package decisions

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

// Decision is a structured registry entry for a durable decision such as
// "we chose X because Y". It includes provenance, timestamp, alternatives,
// and owner/context for high-signal knowledge tracking.
type Decision struct {
	ID           string `json:"id"`
	PeerID       string `json:"peer_id"`
	Title        string `json:"title"`
	Decision     string `json:"decision"`
	Rationale    string `json:"rationale,omitempty"`
	Alternatives string `json:"alternatives,omitempty"`
	Owner        string `json:"owner,omitempty"`
	Context      string `json:"context,omitempty"`
	CreatedAt    int64  `json:"created_at"`
	UpdatedAt    int64  `json:"updated_at"`
}

// Input holds the fields used when recording a decision.
type Input struct {
	Title        string
	Decision     string
	Rationale    string
	Alternatives string
	Owner        string
	Context      string
}

// Store persists decisions in a local SQLite database.
type Store struct {
	db *sql.DB
}

// New opens (or creates) the decisions SQLite database at dbPath.
func New(dbPath string) (*Store, error) {
	db, err := sql.Open("sqlite", dbPath+"?_journal_mode=WAL&_foreign_keys=on")
	if err != nil {
		return nil, fmt.Errorf("decisions: open db: %w", err)
	}
	db.SetMaxOpenConns(1)
	if err := migrate(db); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("decisions: migrate: %w", err)
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

// Record inserts a new decision for peerID and returns the saved record.
func (s *Store) Record(ctx context.Context, peerID string, input Input) (*Decision, error) {
	d, err := newDecision(strings.TrimSpace(peerID), input)
	if err != nil {
		return nil, err
	}
	_, err = s.db.ExecContext(ctx,
		`INSERT INTO decisions(id, peer_id, title, decision, rationale, alternatives, owner, context, created_at, updated_at)
		 VALUES (?,?,?,?,?,?,?,?,?,?)`,
		d.ID, d.PeerID, d.Title, d.Decision,
		d.Rationale, d.Alternatives, d.Owner, d.Context,
		d.CreatedAt, d.UpdatedAt)
	if err != nil {
		return nil, fmt.Errorf("decision record: %w", err)
	}
	return d, nil
}

// List returns decisions for peerID ordered by recency.
func (s *Store) List(ctx context.Context, peerID string, limit int) ([]Decision, error) {
	if limit <= 0 {
		limit = 20
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, peer_id, title, decision, rationale, alternatives, owner, context, created_at, updated_at
		   FROM decisions
		  WHERE peer_id = ?
		  ORDER BY created_at DESC
		  LIMIT ?`,
		strings.TrimSpace(peerID), limit)
	if err != nil {
		return nil, fmt.Errorf("decision list: %w", err)
	}
	defer rows.Close()
	return collectDecisions(rows)
}

// Search returns decisions matching query in title, decision text, rationale,
// owner, or context, ordered by recency.
func (s *Store) Search(ctx context.Context, peerID, query string, limit int) ([]Decision, error) {
	q := strings.ToLower(strings.TrimSpace(query))
	if q == "" {
		return nil, fmt.Errorf("query is required")
	}
	if limit <= 0 {
		limit = 20
	}
	like := "%" + q + "%"
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, peer_id, title, decision, rationale, alternatives, owner, context, created_at, updated_at
		   FROM decisions
		  WHERE peer_id = ?
		    AND (LOWER(title) LIKE ?
		      OR LOWER(decision) LIKE ?
		      OR LOWER(rationale) LIKE ?
		      OR LOWER(owner) LIKE ?
		      OR LOWER(context) LIKE ?)
		  ORDER BY created_at DESC
		  LIMIT ?`,
		strings.TrimSpace(peerID), like, like, like, like, like, limit)
	if err != nil {
		return nil, fmt.Errorf("decision search: %w", err)
	}
	defer rows.Close()
	return collectDecisions(rows)
}

// migrate ensures the schema is up to date.
func migrate(db *sql.DB) error {
	_, err := db.Exec(`CREATE TABLE IF NOT EXISTS decisions (
	id           TEXT PRIMARY KEY,
	peer_id      TEXT NOT NULL,
	title        TEXT NOT NULL,
	decision     TEXT NOT NULL,
	rationale    TEXT NOT NULL DEFAULT '',
	alternatives TEXT NOT NULL DEFAULT '',
	owner        TEXT NOT NULL DEFAULT '',
	context      TEXT NOT NULL DEFAULT '',
	created_at   INTEGER NOT NULL,
	updated_at   INTEGER NOT NULL
	);
	CREATE INDEX IF NOT EXISTS idx_decisions_peer_created ON decisions(peer_id, created_at DESC);`)
	return err
}

// helpers -----------------------------------------------------------------

func newDecision(peerID string, input Input) (*Decision, error) {
	id, err := randomID()
	if err != nil {
		return nil, fmt.Errorf("decision id: %w", err)
	}
	title := strings.TrimSpace(input.Title)
	if title == "" {
		return nil, fmt.Errorf("title is required")
	}
	decision := strings.TrimSpace(input.Decision)
	if decision == "" {
		return nil, fmt.Errorf("decision is required")
	}
	now := time.Now().Unix()
	return &Decision{
		ID:           id,
		PeerID:       peerID,
		Title:        title,
		Decision:     decision,
		Rationale:    strings.TrimSpace(input.Rationale),
		Alternatives: strings.TrimSpace(input.Alternatives),
		Owner:        strings.TrimSpace(input.Owner),
		Context:      strings.TrimSpace(input.Context),
		CreatedAt:    now,
		UpdatedAt:    now,
	}, nil
}

type rowScanner interface {
	Scan(dest ...any) error
}

func scanDecision(scanner rowScanner) (Decision, error) {
	var d Decision
	err := scanner.Scan(
		&d.ID, &d.PeerID, &d.Title, &d.Decision,
		&d.Rationale, &d.Alternatives, &d.Owner, &d.Context,
		&d.CreatedAt, &d.UpdatedAt)
	if err != nil {
		return Decision{}, err
	}
	return d, nil
}

func collectDecisions(rows *sql.Rows) ([]Decision, error) {
	var out []Decision
	for rows.Next() {
		d, err := scanDecision(rows)
		if err != nil {
			return nil, fmt.Errorf("decision scan: %w", err)
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

func randomID() (string, error) {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}
