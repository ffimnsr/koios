package reminder

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

// Reminder is a lightweight, user-facing nudge separate from cron jobs and
// tasks. It captures what to do, when to surface it, and whether it has been
// acted on.
type Reminder struct {
	ID          string `json:"id"`
	PeerID      string `json:"peer_id"`
	Title       string `json:"title"`
	Body        string `json:"body,omitempty"`
	DueAt       int64  `json:"due_at,omitempty"`
	CompletedAt int64  `json:"completed_at,omitempty"`
	CreatedAt   int64  `json:"created_at"`
	UpdatedAt   int64  `json:"updated_at"`
}

// Input holds the fields used when creating a reminder.
type Input struct {
	Title string
	Body  string
	DueAt int64
}

// Store persists reminders in a local SQLite database.
type Store struct {
	db *sql.DB
}

// New opens (or creates) the reminders SQLite database at dbPath.
func New(dbPath string) (*Store, error) {
	db, err := sql.Open("sqlite", dbPath+"?_journal_mode=WAL&_foreign_keys=on")
	if err != nil {
		return nil, fmt.Errorf("reminder: open db: %w", err)
	}
	db.SetMaxOpenConns(1)
	if err := migrate(db); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("reminder: migrate: %w", err)
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

// Create inserts a new reminder for peerID and returns the saved record.
func (s *Store) Create(ctx context.Context, peerID string, input Input) (*Reminder, error) {
	peerID = strings.TrimSpace(peerID)
	if peerID == "" {
		return nil, fmt.Errorf("peer_id is required")
	}
	title := strings.TrimSpace(input.Title)
	if title == "" {
		return nil, fmt.Errorf("title is required")
	}
	now := time.Now().Unix()
	r := &Reminder{
		ID:        randomID(),
		PeerID:    peerID,
		Title:     title,
		Body:      strings.TrimSpace(input.Body),
		DueAt:     input.DueAt,
		CreatedAt: now,
		UpdatedAt: now,
	}
	if _, err := s.db.ExecContext(ctx,
		`INSERT INTO reminders(id, peer_id, title, body, due_at, completed_at, created_at, updated_at)
		 VALUES (?,?,?,?,?,?,?,?)`,
		r.ID, r.PeerID, r.Title, r.Body, r.DueAt, 0, r.CreatedAt, r.UpdatedAt); err != nil {
		return nil, fmt.Errorf("reminder create: %w", err)
	}
	return r, nil
}

// List returns reminders for peerID.  When pendingOnly is true only
// non-completed reminders are returned; otherwise all are returned.
// Results are ordered by due_at ascending (zeroes last), then created_at.
func (s *Store) List(ctx context.Context, peerID string, pendingOnly bool, limit int) ([]Reminder, error) {
	peerID = strings.TrimSpace(peerID)
	if limit <= 0 {
		limit = 50
	}
	var (
		rows *sql.Rows
		err  error
	)
	if pendingOnly {
		rows, err = s.db.QueryContext(ctx,
			`SELECT id, peer_id, title, body, due_at, completed_at, created_at, updated_at
			   FROM reminders
			  WHERE peer_id = ? AND completed_at = 0
			  ORDER BY CASE WHEN due_at = 0 THEN 1 ELSE 0 END, due_at ASC, created_at ASC
			  LIMIT ?`,
			peerID, limit)
	} else {
		rows, err = s.db.QueryContext(ctx,
			`SELECT id, peer_id, title, body, due_at, completed_at, created_at, updated_at
			   FROM reminders
			  WHERE peer_id = ?
			  ORDER BY CASE WHEN due_at = 0 THEN 1 ELSE 0 END, due_at ASC, created_at ASC
			  LIMIT ?`,
			peerID, limit)
	}
	if err != nil {
		return nil, fmt.Errorf("reminder list: %w", err)
	}
	defer rows.Close()
	var out []Reminder
	for rows.Next() {
		r, err := scanReminder(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// Complete marks a reminder as done. It is idempotent – a second call is a
// no-op.
func (s *Store) Complete(ctx context.Context, peerID, id string) (*Reminder, error) {
	peerID = strings.TrimSpace(peerID)
	id = strings.TrimSpace(id)
	if id == "" {
		return nil, fmt.Errorf("id is required")
	}
	now := time.Now().Unix()
	res, err := s.db.ExecContext(ctx,
		`UPDATE reminders SET completed_at = ?, updated_at = ?
		  WHERE peer_id = ? AND id = ? AND completed_at = 0`,
		now, now, peerID, id)
	if err != nil {
		return nil, fmt.Errorf("reminder complete: %w", err)
	}
	// Allow the update to be a no-op (already completed) but still return the
	// current row so the caller can inspect its state.
	_ = res
	return s.get(ctx, peerID, id)
}

func (s *Store) get(ctx context.Context, peerID, id string) (*Reminder, error) {
	r, err := scanReminder(s.db.QueryRowContext(ctx,
		`SELECT id, peer_id, title, body, due_at, completed_at, created_at, updated_at
		   FROM reminders WHERE id = ?`, id))
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("reminder %s not found", id)
	}
	if err != nil {
		return nil, fmt.Errorf("reminder get: %w", err)
	}
	if r.PeerID != peerID {
		return nil, fmt.Errorf("reminder %s does not belong to peer", id)
	}
	return &r, nil
}

// scanner abstracts *sql.Row and *sql.Rows for scanReminder.
type scanner interface {
	Scan(dest ...any) error
}

func scanReminder(s scanner) (Reminder, error) {
	var r Reminder
	if err := s.Scan(&r.ID, &r.PeerID, &r.Title, &r.Body, &r.DueAt, &r.CompletedAt, &r.CreatedAt, &r.UpdatedAt); err != nil {
		return Reminder{}, err
	}
	return r, nil
}

func migrate(db *sql.DB) error {
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS reminders (
			id           TEXT    PRIMARY KEY,
			peer_id      TEXT    NOT NULL,
			title        TEXT    NOT NULL,
			body         TEXT    NOT NULL DEFAULT '',
			due_at       INTEGER NOT NULL DEFAULT 0,
			completed_at INTEGER NOT NULL DEFAULT 0,
			created_at   INTEGER NOT NULL,
			updated_at   INTEGER NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS idx_reminders_peer_due ON reminders(peer_id, due_at ASC)`,
	}
	for _, stmt := range stmts {
		if _, err := db.Exec(stmt); err != nil {
			return err
		}
	}
	return nil
}

func randomID() string {
	buf := make([]byte, 8)
	_, _ = rand.Read(buf)
	return hex.EncodeToString(buf)
}
