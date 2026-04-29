package notes

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

// Note is a lightweight knowledge or draft entry, distinct from bookmarks,
// long-term memory, and artifacts. Notes are easy for agents to update
// incrementally without implying a finalised artifact.
type Note struct {
	ID        string   `json:"id"`
	PeerID    string   `json:"peer_id"`
	Title     string   `json:"title"`
	Content   string   `json:"content"`
	Labels    []string `json:"labels,omitempty"`
	CreatedAt int64    `json:"created_at"`
	UpdatedAt int64    `json:"updated_at"`
}

// Input holds the mutable fields used when creating a note.
type Input struct {
	Title   string
	Content string
	Labels  []string
}

// Patch carries optional fields for a partial update.
type Patch struct {
	Title   *string
	Content *string
	Labels  *[]string
}

// Store persists notes in a local SQLite database.
type Store struct {
	db *sql.DB
}

// New opens (or creates) the notes SQLite database at dbPath.
func New(dbPath string) (*Store, error) {
	db, err := sql.Open("sqlite", dbPath+"?_journal_mode=WAL&_foreign_keys=on")
	if err != nil {
		return nil, fmt.Errorf("notes: open db: %w", err)
	}
	db.SetMaxOpenConns(1)
	if err := migrate(db); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("notes: migrate: %w", err)
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

// Create inserts a new note for peerID and returns the saved record.
func (s *Store) Create(ctx context.Context, peerID string, input Input) (*Note, error) {
	note, err := newNote(strings.TrimSpace(peerID), input)
	if err != nil {
		return nil, err
	}
	if _, err := s.db.ExecContext(ctx,
		`INSERT INTO notes(id, peer_id, title, content, labels, created_at, updated_at)
		 VALUES (?,?,?,?,?,?,?)`,
		note.ID, note.PeerID, note.Title, note.Content,
		strings.Join(note.Labels, ","), note.CreatedAt, note.UpdatedAt); err != nil {
		return nil, fmt.Errorf("note create: %w", err)
	}
	return note, nil
}

// Get returns a single note by ID, verifying peer ownership.
func (s *Store) Get(ctx context.Context, peerID, id string) (*Note, error) {
	note, err := scanNote(s.db.QueryRowContext(ctx,
		`SELECT id, peer_id, title, content, COALESCE(labels,''), created_at, updated_at
		   FROM notes WHERE id = ?`,
		strings.TrimSpace(id)))
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("note %s not found", id)
	}
	if err != nil {
		return nil, fmt.Errorf("note get: %w", err)
	}
	if note.PeerID != strings.TrimSpace(peerID) {
		return nil, fmt.Errorf("note %s does not belong to peer", id)
	}
	return &note, nil
}

// Search returns notes matching query in title or content, ordered by recency.
func (s *Store) Search(ctx context.Context, peerID, query string, limit int) ([]Note, error) {
	q := strings.ToLower(strings.TrimSpace(query))
	if q == "" {
		return nil, fmt.Errorf("query is required")
	}
	if limit <= 0 {
		limit = 20
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, peer_id, title, content, COALESCE(labels,''), created_at, updated_at
		   FROM notes
		  WHERE peer_id = ?
		    AND (LOWER(title) LIKE ? OR LOWER(content) LIKE ? OR LOWER(COALESCE(labels,'')) LIKE ?)
		  ORDER BY updated_at DESC
		  LIMIT ?`,
		strings.TrimSpace(peerID), "%"+q+"%", "%"+q+"%", "%"+q+"%", limit)
	if err != nil {
		return nil, fmt.Errorf("note search: %w", err)
	}
	defer rows.Close()
	return collectNotes(rows)
}

// Update applies a partial patch to an existing note and returns the updated record.
func (s *Store) Update(ctx context.Context, peerID, id string, patch Patch) (*Note, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("note update: begin tx: %w", err)
	}
	defer tx.Rollback()

	note, err := loadNoteTx(ctx, tx, peerID, id)
	if err != nil {
		return nil, err
	}
	updated := applyPatch(*note, patch)
	updated.UpdatedAt = time.Now().Unix()

	if _, err := tx.ExecContext(ctx,
		`UPDATE notes SET title = ?, content = ?, labels = ?, updated_at = ?
		  WHERE id = ? AND peer_id = ?`,
		updated.Title, updated.Content, strings.Join(updated.Labels, ","), updated.UpdatedAt,
		updated.ID, updated.PeerID); err != nil {
		return nil, fmt.Errorf("note update: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("note update: commit: %w", err)
	}
	return &updated, nil
}

// Delete removes a note by ID, verifying peer ownership.
func (s *Store) Delete(ctx context.Context, peerID, id string) error {
	result, err := s.db.ExecContext(ctx,
		`DELETE FROM notes WHERE id = ? AND peer_id = ?`,
		strings.TrimSpace(id), strings.TrimSpace(peerID))
	if err != nil {
		return fmt.Errorf("note delete: %w", err)
	}
	n, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("note delete rows: %w", err)
	}
	if n == 0 {
		return fmt.Errorf("note %s not found", id)
	}
	return nil
}

// migrate ensures the schema is up to date.
func migrate(db *sql.DB) error {
	_, err := db.Exec(`CREATE TABLE IF NOT EXISTS notes (
	id         TEXT PRIMARY KEY,
	peer_id    TEXT NOT NULL,
	title      TEXT NOT NULL,
	content    TEXT NOT NULL,
	labels     TEXT NOT NULL DEFAULT '',
	created_at INTEGER NOT NULL,
	updated_at INTEGER NOT NULL
	);
	CREATE INDEX IF NOT EXISTS idx_notes_peer_updated ON notes(peer_id, updated_at DESC);`)
	return err
}

// helpers -----------------------------------------------------------------

func newNote(peerID string, input Input) (*Note, error) {
	id, err := randomID()
	if err != nil {
		return nil, fmt.Errorf("note id: %w", err)
	}
	title := strings.TrimSpace(input.Title)
	if title == "" {
		return nil, fmt.Errorf("title is required")
	}
	now := time.Now().Unix()
	return &Note{
		ID:        id,
		PeerID:    peerID,
		Title:     title,
		Content:   strings.TrimSpace(input.Content),
		Labels:    normaliseLabels(input.Labels),
		CreatedAt: now,
		UpdatedAt: now,
	}, nil
}

func applyPatch(note Note, patch Patch) Note {
	if patch.Title != nil {
		note.Title = strings.TrimSpace(*patch.Title)
	}
	if patch.Content != nil {
		note.Content = strings.TrimSpace(*patch.Content)
	}
	if patch.Labels != nil {
		note.Labels = normaliseLabels(*patch.Labels)
	}
	return note
}

type rowScanner interface {
	Scan(dest ...any) error
}

func scanNote(scanner rowScanner) (Note, error) {
	var note Note
	var labels string
	err := scanner.Scan(
		&note.ID, &note.PeerID, &note.Title, &note.Content,
		&labels, &note.CreatedAt, &note.UpdatedAt)
	if err != nil {
		return Note{}, err
	}
	if labels != "" {
		note.Labels = strings.Split(labels, ",")
	}
	return note, nil
}

func collectNotes(rows *sql.Rows) ([]Note, error) {
	var out []Note
	for rows.Next() {
		note, err := scanNote(rows)
		if err != nil {
			return nil, fmt.Errorf("note scan: %w", err)
		}
		out = append(out, note)
	}
	return out, rows.Err()
}

func loadNoteTx(ctx context.Context, tx *sql.Tx, peerID, id string) (*Note, error) {
	note, err := scanNote(tx.QueryRowContext(ctx,
		`SELECT id, peer_id, title, content, COALESCE(labels,''), created_at, updated_at
		   FROM notes WHERE id = ? AND peer_id = ?`,
		strings.TrimSpace(id), strings.TrimSpace(peerID)))
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("note %s not found", id)
	}
	if err != nil {
		return nil, fmt.Errorf("note load: %w", err)
	}
	return &note, nil
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
