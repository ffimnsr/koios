package preferences

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

// Preference stores an explicit user preference with provenance, scope,
// confidence, and last-confirmed timestamps. This gives the runtime a more
// reliable behavior-shaping source than generic memory retrieval.
type Preference struct {
	ID               string  `json:"id"`
	PeerID           string  `json:"peer_id"`
	Key              string  `json:"key"`
	Value            string  `json:"value"`
	Scope            string  `json:"scope,omitempty"`
	Provenance       string  `json:"provenance,omitempty"`
	Confidence       float64 `json:"confidence"`
	LastConfirmedAt  int64   `json:"last_confirmed_at,omitempty"`
	CreatedAt        int64   `json:"created_at"`
	UpdatedAt        int64   `json:"updated_at"`
}

// Input holds the fields for setting a preference.
type Input struct {
	Key             string
	Value           string
	Scope           string
	Provenance      string
	Confidence      float64
	LastConfirmedAt int64
}

// Store persists preferences in a local SQLite database.
type Store struct {
	db *sql.DB
}

// New opens (or creates) the preferences SQLite database at dbPath.
func New(dbPath string) (*Store, error) {
	db, err := sql.Open("sqlite", dbPath+"?_journal_mode=WAL&_foreign_keys=on")
	if err != nil {
		return nil, fmt.Errorf("preferences: open db: %w", err)
	}
	db.SetMaxOpenConns(1)
	if err := migrate(db); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("preferences: migrate: %w", err)
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

// Set upserts a preference by (peer_id, key, scope). If a matching preference
// already exists it is updated in place; otherwise a new record is inserted.
func (s *Store) Set(ctx context.Context, peerID string, input Input) (*Preference, error) {
	key := strings.TrimSpace(input.Key)
	if key == "" {
		return nil, fmt.Errorf("key is required")
	}
	value := strings.TrimSpace(input.Value)
	if value == "" {
		return nil, fmt.Errorf("value is required")
	}
	peerID = strings.TrimSpace(peerID)
	scope := strings.TrimSpace(input.Scope)
	if scope == "" {
		scope = "global"
	}
	confidence := input.Confidence
	if confidence == 0 {
		confidence = 1.0
	}
	now := time.Now().Unix()
	lastConfirmed := input.LastConfirmedAt
	if lastConfirmed == 0 {
		lastConfirmed = now
	}

	// Check for existing preference with same key+scope.
	var existingID string
	err := s.db.QueryRowContext(ctx,
		`SELECT id FROM preferences WHERE peer_id = ? AND key = ? AND scope = ?`,
		peerID, key, scope).Scan(&existingID)
	if err != nil && err != sql.ErrNoRows {
		return nil, fmt.Errorf("preference set lookup: %w", err)
	}

	if existingID != "" {
		// Update existing.
		if _, err := s.db.ExecContext(ctx,
			`UPDATE preferences SET value = ?, provenance = ?, confidence = ?, last_confirmed_at = ?, updated_at = ?
			  WHERE id = ? AND peer_id = ?`,
			value, strings.TrimSpace(input.Provenance), confidence, lastConfirmed, now,
			existingID, peerID); err != nil {
			return nil, fmt.Errorf("preference update: %w", err)
		}
		return s.getByID(ctx, peerID, existingID)
	}

	// Insert new.
	id, err := randomID()
	if err != nil {
		return nil, fmt.Errorf("preference id: %w", err)
	}
	prov := strings.TrimSpace(input.Provenance)
	if _, err := s.db.ExecContext(ctx,
		`INSERT INTO preferences(id, peer_id, key, value, scope, provenance, confidence, last_confirmed_at, created_at, updated_at)
		 VALUES (?,?,?,?,?,?,?,?,?,?)`,
		id, peerID, key, value, scope, prov, confidence, lastConfirmed, now, now); err != nil {
		return nil, fmt.Errorf("preference insert: %w", err)
	}
	return &Preference{
		ID: id, PeerID: peerID, Key: key, Value: value,
		Scope: scope, Provenance: prov, Confidence: confidence,
		LastConfirmedAt: lastConfirmed, CreatedAt: now, UpdatedAt: now,
	}, nil
}

// Get returns a single preference by key and scope for peerID.
func (s *Store) Get(ctx context.Context, peerID, key, scope string) (*Preference, error) {
	peerID = strings.TrimSpace(peerID)
	key = strings.TrimSpace(key)
	scope = strings.TrimSpace(scope)
	if scope == "" {
		scope = "global"
	}
	p, err := scanPreference(s.db.QueryRowContext(ctx,
		`SELECT id, peer_id, key, value, scope, provenance, confidence, last_confirmed_at, created_at, updated_at
		   FROM preferences WHERE peer_id = ? AND key = ? AND scope = ?`,
		peerID, key, scope))
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("preference %q (scope=%s) not found", key, scope)
	}
	if err != nil {
		return nil, fmt.Errorf("preference get: %w", err)
	}
	return &p, nil
}

// List returns all preferences for peerID, optionally filtered by scope, ordered by key.
func (s *Store) List(ctx context.Context, peerID, scope string, limit int) ([]Preference, error) {
	if limit <= 0 {
		limit = 50
	}
	peerID = strings.TrimSpace(peerID)
	scope = strings.TrimSpace(scope)
	var rows *sql.Rows
	var err error
	if scope != "" {
		rows, err = s.db.QueryContext(ctx,
			`SELECT id, peer_id, key, value, scope, provenance, confidence, last_confirmed_at, created_at, updated_at
			   FROM preferences
			  WHERE peer_id = ? AND scope = ?
			  ORDER BY key ASC
			  LIMIT ?`,
			peerID, scope, limit)
	} else {
		rows, err = s.db.QueryContext(ctx,
			`SELECT id, peer_id, key, value, scope, provenance, confidence, last_confirmed_at, created_at, updated_at
			   FROM preferences
			  WHERE peer_id = ?
			  ORDER BY key ASC
			  LIMIT ?`,
			peerID, limit)
	}
	if err != nil {
		return nil, fmt.Errorf("preference list: %w", err)
	}
	defer rows.Close()
	return collectPreferences(rows)
}

// migrate ensures the schema is up to date.
func migrate(db *sql.DB) error {
	_, err := db.Exec(`CREATE TABLE IF NOT EXISTS preferences (
	id               TEXT PRIMARY KEY,
	peer_id          TEXT NOT NULL,
	key              TEXT NOT NULL,
	value            TEXT NOT NULL,
	scope            TEXT NOT NULL DEFAULT 'global',
	provenance       TEXT NOT NULL DEFAULT '',
	confidence       REAL NOT NULL DEFAULT 1.0,
	last_confirmed_at INTEGER NOT NULL DEFAULT 0,
	created_at       INTEGER NOT NULL,
	updated_at       INTEGER NOT NULL
	);
	CREATE UNIQUE INDEX IF NOT EXISTS idx_preferences_peer_key_scope ON preferences(peer_id, key, scope);
	CREATE INDEX IF NOT EXISTS idx_preferences_peer_scope ON preferences(peer_id, scope);`)
	return err
}

// helpers -----------------------------------------------------------------

func (s *Store) getByID(ctx context.Context, peerID, id string) (*Preference, error) {
	p, err := scanPreference(s.db.QueryRowContext(ctx,
		`SELECT id, peer_id, key, value, scope, provenance, confidence, last_confirmed_at, created_at, updated_at
		   FROM preferences WHERE id = ? AND peer_id = ?`,
		id, peerID))
	if err != nil {
		return nil, fmt.Errorf("preference get by id: %w", err)
	}
	return &p, nil
}

type rowScanner interface {
	Scan(dest ...any) error
}

func scanPreference(scanner rowScanner) (Preference, error) {
	var p Preference
	err := scanner.Scan(
		&p.ID, &p.PeerID, &p.Key, &p.Value, &p.Scope,
		&p.Provenance, &p.Confidence, &p.LastConfirmedAt,
		&p.CreatedAt, &p.UpdatedAt)
	if err != nil {
		return Preference{}, err
	}
	return p, nil
}

func collectPreferences(rows *sql.Rows) ([]Preference, error) {
	var out []Preference
	for rows.Next() {
		p, err := scanPreference(rows)
		if err != nil {
			return nil, fmt.Errorf("preference scan: %w", err)
		}
		out = append(out, p)
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
