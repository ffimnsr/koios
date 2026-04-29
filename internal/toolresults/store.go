package toolresults

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

// Record holds durable provenance for a single tool execution. It captures
// the execution identifiers, result summary, and source metadata so that
// operator and agent-facing surfaces can answer where a result came from,
// which executor produced it, and whether it was redacted or approval-gated.
type Record struct {
	ID         string     `json:"id"`
	PeerID     string     `json:"peer_id"`
	SessionKey string     `json:"session_key,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
	ToolName   string     `json:"tool_name"`
	ArgsJSON   string     `json:"args_json,omitempty"`
	ResultJSON string     `json:"result_json,omitempty"`
	Summary    string     `json:"summary,omitempty"`
	IsError    bool       `json:"is_error,omitempty"`
	DurationMS int64      `json:"duration_ms,omitempty"`
	CreatedAt  int64      `json:"created_at"`
	Provenance Provenance `json:"provenance"`
}

// Provenance carries source metadata embedded in every Record, identifying
// the executor kind, model profile, and originating session or workflow.
type Provenance struct {
	ExecutorKind  string `json:"executor_kind,omitempty"`
	ModelProfile  string `json:"model_profile,omitempty"`
	ApprovalState string `json:"approval_state,omitempty"`
	Redacted      bool   `json:"redacted,omitempty"`
	CaptureReason string `json:"capture_reason,omitempty"`
}

// Input holds the mutable fields used when creating a tool result record.
type Input struct {
	SessionKey string
	ToolCallID string
	ToolName   string
	ArgsJSON   string
	ResultJSON string
	Summary    string
	IsError    bool
	DurationMS int64
	Provenance Provenance
}

// Filter narrows the result set returned by List.
type Filter struct {
	SessionKey string
	ToolName   string
	IsError    *bool
	Limit      int
}

// Store persists tool execution records in a local SQLite database.
type Store struct {
	db *sql.DB
}

// New opens (or creates) the tool results SQLite database at dbPath.
func New(dbPath string) (*Store, error) {
	db, err := sql.Open("sqlite", dbPath+"?_journal_mode=WAL&_foreign_keys=on")
	if err != nil {
		return nil, fmt.Errorf("toolresults: open db: %w", err)
	}
	db.SetMaxOpenConns(1)
	if err := migrate(db); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("toolresults: migrate: %w", err)
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

// Create inserts a new tool result record for peerID and returns the saved record.
func (s *Store) Create(ctx context.Context, peerID string, input Input) (*Record, error) {
	r, err := newRecord(strings.TrimSpace(peerID), input)
	if err != nil {
		return nil, err
	}
	provJSON, err := json.Marshal(r.Provenance)
	if err != nil {
		return nil, fmt.Errorf("toolresults: marshal provenance: %w", err)
	}
	isErrInt := 0
	if r.IsError {
		isErrInt = 1
	}
	_, err = s.db.ExecContext(ctx,
		`INSERT INTO tool_results(id, peer_id, session_key, tool_call_id, tool_name,
		  args_json, result_json, summary, is_error, duration_ms, created_at, provenance)
		 VALUES (?,?,?,?,?,?,?,?,?,?,?,?)`,
		r.ID, r.PeerID, r.SessionKey, r.ToolCallID, r.ToolName,
		r.ArgsJSON, r.ResultJSON, r.Summary,
		isErrInt, r.DurationMS, r.CreatedAt, string(provJSON))
	if err != nil {
		return nil, fmt.Errorf("toolresults create: %w", err)
	}
	return r, nil
}

// Get returns a single record by ID, verifying peer ownership.
func (s *Store) Get(ctx context.Context, peerID, id string) (*Record, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT id, peer_id, session_key, tool_call_id, tool_name,
		        args_json, result_json, summary, is_error, duration_ms, created_at, provenance
		   FROM tool_results WHERE id = ?`,
		strings.TrimSpace(id))
	r, err := scanRecord(row)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("tool result %s not found", id)
	}
	if err != nil {
		return nil, fmt.Errorf("toolresults get: %w", err)
	}
	if r.PeerID != strings.TrimSpace(peerID) {
		return nil, fmt.Errorf("tool result %s does not belong to peer", id)
	}
	return r, nil
}

// List returns records for peerID matching the given filter, ordered by recency.
func (s *Store) List(ctx context.Context, peerID string, f Filter) ([]Record, error) {
	limit := f.Limit
	if limit <= 0 {
		limit = 50
	}
	peerID = strings.TrimSpace(peerID)
	conds := []string{"peer_id = ?"}
	args := []any{peerID}
	if sk := strings.TrimSpace(f.SessionKey); sk != "" {
		conds = append(conds, "session_key = ?")
		args = append(args, sk)
	}
	if tn := strings.TrimSpace(f.ToolName); tn != "" {
		conds = append(conds, "tool_name = ?")
		args = append(args, tn)
	}
	if f.IsError != nil {
		errVal := 0
		if *f.IsError {
			errVal = 1
		}
		conds = append(conds, "is_error = ?")
		args = append(args, errVal)
	}
	args = append(args, limit)
	query := `SELECT id, peer_id, session_key, tool_call_id, tool_name,
		         args_json, result_json, summary, is_error, duration_ms, created_at, provenance
		    FROM tool_results
		   WHERE ` + strings.Join(conds, " AND ") + `
		   ORDER BY created_at DESC
		   LIMIT ?`
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("toolresults list: %w", err)
	}
	defer rows.Close()
	return collectRecords(rows)
}

// migrate ensures the schema is up to date.
func migrate(db *sql.DB) error {
	_, err := db.Exec(`
CREATE TABLE IF NOT EXISTS tool_results (
  id           TEXT    PRIMARY KEY,
  peer_id      TEXT    NOT NULL,
  session_key  TEXT    NOT NULL DEFAULT '',
  tool_call_id TEXT    NOT NULL DEFAULT '',
  tool_name    TEXT    NOT NULL,
  args_json    TEXT    NOT NULL DEFAULT '',
  result_json  TEXT    NOT NULL DEFAULT '',
  summary      TEXT    NOT NULL DEFAULT '',
  is_error     INTEGER NOT NULL DEFAULT 0,
  duration_ms  INTEGER NOT NULL DEFAULT 0,
  created_at   INTEGER NOT NULL,
  provenance   TEXT    NOT NULL DEFAULT '{}'
);
CREATE INDEX IF NOT EXISTS idx_tr_peer_created  ON tool_results(peer_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_tr_session       ON tool_results(session_key);
CREATE INDEX IF NOT EXISTS idx_tr_tool_name     ON tool_results(tool_name);
`)
	return err
}

// helpers -----------------------------------------------------------------

func newRecord(peerID string, input Input) (*Record, error) {
	id, err := randomID()
	if err != nil {
		return nil, fmt.Errorf("toolresults id: %w", err)
	}
	return &Record{
		ID:         id,
		PeerID:     peerID,
		SessionKey: strings.TrimSpace(input.SessionKey),
		ToolCallID: strings.TrimSpace(input.ToolCallID),
		ToolName:   strings.TrimSpace(input.ToolName),
		ArgsJSON:   input.ArgsJSON,
		ResultJSON: input.ResultJSON,
		Summary:    strings.TrimSpace(input.Summary),
		IsError:    input.IsError,
		DurationMS: input.DurationMS,
		CreatedAt:  time.Now().Unix(),
		Provenance: input.Provenance,
	}, nil
}

func scanRecord(row *sql.Row) (*Record, error) {
	var r Record
	var isErrInt int
	var provJSON string
	err := row.Scan(
		&r.ID, &r.PeerID, &r.SessionKey, &r.ToolCallID, &r.ToolName,
		&r.ArgsJSON, &r.ResultJSON, &r.Summary, &isErrInt, &r.DurationMS,
		&r.CreatedAt, &provJSON)
	if err != nil {
		return nil, err
	}
	r.IsError = isErrInt != 0
	_ = json.Unmarshal([]byte(provJSON), &r.Provenance)
	return &r, nil
}

func collectRecords(rows *sql.Rows) ([]Record, error) {
	var out []Record
	for rows.Next() {
		var r Record
		var isErrInt int
		var provJSON string
		if err := rows.Scan(
			&r.ID, &r.PeerID, &r.SessionKey, &r.ToolCallID, &r.ToolName,
			&r.ArgsJSON, &r.ResultJSON, &r.Summary, &isErrInt, &r.DurationMS,
			&r.CreatedAt, &provJSON); err != nil {
			return nil, fmt.Errorf("toolresults scan: %w", err)
		}
		r.IsError = isErrInt != 0
		_ = json.Unmarshal([]byte(provJSON), &r.Provenance)
		out = append(out, r)
	}
	return out, rows.Err()
}

func randomID() (string, error) {
	b := make([]byte, 12)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return "tr_" + hex.EncodeToString(b), nil
}
