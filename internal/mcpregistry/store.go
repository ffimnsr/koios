// Package mcpregistry provides a persistent SQLite-backed store for
// user-managed MCP server definitions, separate from the operator's
// koios.config.toml static config.
package mcpregistry

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/ffimnsr/koios/internal/config"
	_ "modernc.org/sqlite"
)

// toolPrefix returns the MCP tool prefix for a runtime server name.
func toolPrefix(serverName string) string {
	if serverName == "" {
		return "mcp__unknown__"
	}
	return "mcp__" + serverName + "__"
}

// ServerRecord is one user-managed MCP server definition stored persistently.
type ServerRecord struct {
	ID               string            `json:"id"`
	OwnerPeerID      string            `json:"owner_peer_id"`
	Name             string            `json:"name"`
	Transport        string            `json:"transport"`
	Command          string            `json:"command,omitempty"`
	Args             []string          `json:"args,omitempty"`
	Env              map[string]string `json:"env,omitempty"`
	URL              string            `json:"url,omitempty"`
	Headers          map[string]string `json:"headers,omitempty"`
	Timeout          string            `json:"timeout,omitempty"`
	Enabled          bool              `json:"enabled"`
	Visibility       string            `json:"visibility"`
	AllowedPeerIDs   []string          `json:"allowed_peer_ids,omitempty"`
	ApprovalRequired bool              `json:"approval_required"`
	CreatedAt        int64             `json:"created_at"`
	UpdatedAt        int64             `json:"updated_at"`
}

// Input is used when creating a new MCP server record.
type Input struct {
	OwnerPeerID      string
	Name             string
	Transport        string
	Command          string
	Args             []string
	Env              map[string]string
	URL              string
	Headers          map[string]string
	Timeout          string
	Enabled          bool
	Visibility       string
	AllowedPeerIDs   []string
	ApprovalRequired bool
}

// UpdateInput is used when patching an existing record.
// Only non-nil fields are applied; pointer fields indicate a change.
type UpdateInput struct {
	Name             *string
	Transport        *string
	Command          *string
	Args             *[]string
	Env              *map[string]string
	URL              *string
	Headers          *map[string]string
	Timeout          *string
	Enabled          *bool
	Visibility       *string
	AllowedPeerIDs   *[]string
	ApprovalRequired *bool
}

// Store persists user-managed MCP server definitions in a local SQLite database.
type Store struct {
	db *sql.DB
}

// New opens (or creates) the MCP registry SQLite database at dbPath.
func New(dbPath string) (*Store, error) {
	return NewWithContext(context.Background(), dbPath)
}

// NewWithContext opens (or creates) the MCP registry SQLite database at dbPath
// and uses ctx for startup migration work.
func NewWithContext(ctx context.Context, dbPath string) (*Store, error) {
	db, err := sql.Open("sqlite", dbPath+"?_journal_mode=WAL&_foreign_keys=on")
	if err != nil {
		return nil, fmt.Errorf("mcpregistry: open db: %w", err)
	}
	db.SetMaxOpenConns(1)
	if err := migrate(ctx, db); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("mcpregistry: migrate: %w", err)
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

// Create inserts a new MCP server record for the given owner and returns the
// saved record with a generated ID and timestamps.
func (s *Store) Create(ctx context.Context, input Input) (*ServerRecord, error) {
	rec, err := newRecord(strings.TrimSpace(input.OwnerPeerID), input)
	if err != nil {
		return nil, err
	}
	argsJSON, err := marshalStringSlice(rec.Args)
	if err != nil {
		return nil, fmt.Errorf("mcpregistry: marshal args: %w", err)
	}
	envJSON, err := marshalStringMap(rec.Env)
	if err != nil {
		return nil, fmt.Errorf("mcpregistry: marshal env: %w", err)
	}
	headersJSON, err := marshalStringMap(rec.Headers)
	if err != nil {
		return nil, fmt.Errorf("mcpregistry: marshal headers: %w", err)
	}
	peerIDsJSON, err := marshalStringSlice(rec.AllowedPeerIDs)
	if err != nil {
		return nil, fmt.Errorf("mcpregistry: marshal allowed_peer_ids: %w", err)
	}
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO mcp_servers(id, owner_peer_id, name, transport, command, args, env, url, headers, timeout, enabled, visibility, allowed_peer_ids, approval_required, created_at, updated_at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		rec.ID, rec.OwnerPeerID, rec.Name, rec.Transport, rec.Command,
		argsJSON, envJSON, rec.URL, headersJSON, rec.Timeout,
		boolToInt(rec.Enabled), rec.Visibility, peerIDsJSON,
		boolToInt(rec.ApprovalRequired), rec.CreatedAt, rec.UpdatedAt)
	if err != nil {
		return nil, fmt.Errorf("mcpregistry: create: %w", err)
	}
	return rec, nil
}

// Update applies a partial patch to an existing record identified by id and
// ownerPeerID. Returns the updated record.
func (s *Store) Update(ctx context.Context, id, ownerPeerID string, patch UpdateInput) (*ServerRecord, error) {
	existing, err := s.Get(ctx, ownerPeerID, id)
	if err != nil {
		return nil, err
	}
	applyPatch(existing, patch)
	existing.UpdatedAt = time.Now().Unix()

	argsJSON, err := marshalStringSlice(existing.Args)
	if err != nil {
		return nil, fmt.Errorf("mcpregistry: marshal args: %w", err)
	}
	envJSON, err := marshalStringMap(existing.Env)
	if err != nil {
		return nil, fmt.Errorf("mcpregistry: marshal env: %w", err)
	}
	headersJSON, err := marshalStringMap(existing.Headers)
	if err != nil {
		return nil, fmt.Errorf("mcpregistry: marshal headers: %w", err)
	}
	peerIDsJSON, err := marshalStringSlice(existing.AllowedPeerIDs)
	if err != nil {
		return nil, fmt.Errorf("mcpregistry: marshal allowed_peer_ids: %w", err)
	}
	_, err = s.db.ExecContext(ctx, `
		UPDATE mcp_servers
		SET name=?, transport=?, command=?, args=?, env=?, url=?, headers=?,
		    timeout=?, enabled=?, visibility=?, allowed_peer_ids=?,
		    approval_required=?, updated_at=?
		WHERE id=? AND owner_peer_id=?`,
		existing.Name, existing.Transport, existing.Command,
		argsJSON, envJSON, existing.URL, headersJSON,
		existing.Timeout, boolToInt(existing.Enabled), existing.Visibility,
		peerIDsJSON, boolToInt(existing.ApprovalRequired),
		existing.UpdatedAt, id, ownerPeerID)
	if err != nil {
		return nil, fmt.Errorf("mcpregistry: update: %w", err)
	}
	return existing, nil
}

// Delete removes a record identified by id and ownerPeerID.
func (s *Store) Delete(ctx context.Context, ownerPeerID, id string) error {
	res, err := s.db.ExecContext(ctx,
		`DELETE FROM mcp_servers WHERE id=? AND owner_peer_id=?`,
		id, ownerPeerID)
	if err != nil {
		return fmt.Errorf("mcpregistry: delete: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("mcpregistry: record %q not found or not owned by %q", id, ownerPeerID)
	}
	return nil
}

// Get returns one record by id and ownerPeerID.
func (s *Store) Get(ctx context.Context, ownerPeerID, id string) (*ServerRecord, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT id, owner_peer_id, name, transport, command, args, env, url, headers,
		       timeout, enabled, visibility, allowed_peer_ids, approval_required,
		       created_at, updated_at
		FROM mcp_servers
		WHERE id=? AND owner_peer_id=?`, id, ownerPeerID)
	rec, err := scanRecord(row)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, fmt.Errorf("mcpregistry: record %q not found", id)
		}
		return nil, fmt.Errorf("mcpregistry: get: %w", err)
	}
	return rec, nil
}

// ListByOwner returns all records owned by the given peer, ordered by recency.
func (s *Store) ListByOwner(ctx context.Context, ownerPeerID string) ([]ServerRecord, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, owner_peer_id, name, transport, command, args, env, url, headers,
		       timeout, enabled, visibility, allowed_peer_ids, approval_required,
		       created_at, updated_at
		FROM mcp_servers
		WHERE owner_peer_id=?
		ORDER BY created_at DESC`, ownerPeerID)
	if err != nil {
		return nil, fmt.Errorf("mcpregistry: list by owner: %w", err)
	}
	defer rows.Close()
	return collectRecords(rows)
}

// ListAll returns all records across all owners, ordered by recency.
func (s *Store) ListAll(ctx context.Context) ([]ServerRecord, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, owner_peer_id, name, transport, command, args, env, url, headers,
		       timeout, enabled, visibility, allowed_peer_ids, approval_required,
		       created_at, updated_at
		FROM mcp_servers
		ORDER BY created_at DESC`)
	if err != nil {
		return nil, fmt.Errorf("mcpregistry: list all: %w", err)
	}
	defer rows.Close()
	return collectRecords(rows)
}

// ToMCPServerConfig converts a ServerRecord to a config.MCPServerConfig
// suitable for ingestion into the MCP manager. The runtime name is derived
// from owner and id to prevent cross-user namespace collisions.
func (r *ServerRecord) ToMCPServerConfig() config.MCPServerConfig {
	runtimeName := RuntimeName(r.OwnerPeerID, r.ID)
	return config.MCPServerConfig{
		Name:           runtimeName,
		Transport:      r.Transport,
		Command:        r.Command,
		Args:           copyStringSlice(r.Args),
		Env:            copyStringMap(r.Env),
		URL:            r.URL,
		Headers:        copyStringMap(r.Headers),
		Timeout:        r.Timeout,
		Enabled:        r.Enabled,
		ToolNamePrefix: toolPrefix(runtimeName),
		Kind:           "user",
		ProfileName:    r.OwnerPeerID,
	}
}

// RuntimeName produces a stable, collision-free internal name for a
// user-managed MCP server by combining the owner and record id.
func RuntimeName(ownerPeerID, recordID string) string {
	owner := sanitizePrefixToken(ownerPeerID)
	id := sanitizePrefixToken(recordID)
	if len(owner) > 8 {
		owner = owner[:8]
	}
	if len(id) > 8 {
		id = id[:8]
	}
	return "u_" + owner + "_" + id
}

// RandomID generates a 16-character hex string identifier.
func RandomID() (string, error) {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("random id: %w", err)
	}
	return hex.EncodeToString(b), nil
}

// RedactedEnv returns a copy of the env map with all values replaced by
// "<redacted>", or nil when the map is empty.
func RedactedEnv(env map[string]string) map[string]string {
	if len(env) == 0 {
		return nil
	}
	out := make(map[string]string, len(env))
	for k := range env {
		out[k] = "<redacted>"
	}
	return out
}

// RedactedHeaders returns a copy of the headers map with all values replaced
// by "<redacted>", or nil when the map is empty.
func RedactedHeaders(headers map[string]string) map[string]string {
	if len(headers) == 0 {
		return nil
	}
	out := make(map[string]string, len(headers))
	for k := range headers {
		out[k] = "<redacted>"
	}
	return out
}

// ─── schema ─────────────────────────────────────────────────────────────────────

func migrate(ctx context.Context, db *sql.DB) error {
	_, err := db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS mcp_servers (
		id                TEXT PRIMARY KEY,
		owner_peer_id     TEXT NOT NULL,
		name              TEXT NOT NULL,
		transport         TEXT NOT NULL,
		command           TEXT NOT NULL DEFAULT '',
		args              TEXT NOT NULL DEFAULT '[]',
		env               TEXT NOT NULL DEFAULT '{}',
		url               TEXT NOT NULL DEFAULT '',
		headers           TEXT NOT NULL DEFAULT '{}',
		timeout           TEXT NOT NULL DEFAULT '',
		enabled           INTEGER NOT NULL DEFAULT 0,
		visibility        TEXT NOT NULL DEFAULT 'private',
		allowed_peer_ids  TEXT NOT NULL DEFAULT '[]',
		approval_required INTEGER NOT NULL DEFAULT 1,
		created_at        INTEGER NOT NULL,
		updated_at        INTEGER NOT NULL
	);
	CREATE INDEX IF NOT EXISTS idx_mcp_servers_owner_created
		ON mcp_servers(owner_peer_id, created_at DESC);`)
	return err
}

// ─── helpers ────────────────────────────────────────────────────────────────────

func newRecord(ownerPeerID string, input Input) (*ServerRecord, error) {
	id, err := RandomID()
	if err != nil {
		return nil, fmt.Errorf("mcpregistry: id: %w", err)
	}
	name := strings.TrimSpace(input.Name)
	if name == "" {
		return nil, fmt.Errorf("mcpregistry: name is required")
	}
	transport := strings.TrimSpace(input.Transport)
	if transport == "" {
		return nil, fmt.Errorf("mcpregistry: transport is required")
	}
	visibility := strings.TrimSpace(input.Visibility)
	if visibility == "" {
		visibility = "private"
	}
	now := time.Now().Unix()
	return &ServerRecord{
		ID:               id,
		OwnerPeerID:      ownerPeerID,
		Name:             name,
		Transport:        transport,
		Command:          strings.TrimSpace(input.Command),
		Args:             input.Args,
		Env:              input.Env,
		URL:              strings.TrimSpace(input.URL),
		Headers:          input.Headers,
		Timeout:          strings.TrimSpace(input.Timeout),
		Enabled:          input.Enabled,
		Visibility:       visibility,
		AllowedPeerIDs:   input.AllowedPeerIDs,
		ApprovalRequired: input.ApprovalRequired,
		CreatedAt:        now,
		UpdatedAt:        now,
	}, nil
}

func applyPatch(rec *ServerRecord, patch UpdateInput) {
	if patch.Name != nil {
		rec.Name = strings.TrimSpace(*patch.Name)
	}
	if patch.Transport != nil {
		rec.Transport = strings.TrimSpace(*patch.Transport)
	}
	if patch.Command != nil {
		rec.Command = strings.TrimSpace(*patch.Command)
	}
	if patch.Args != nil {
		rec.Args = *patch.Args
	}
	if patch.Env != nil {
		rec.Env = *patch.Env
	}
	if patch.URL != nil {
		rec.URL = strings.TrimSpace(*patch.URL)
	}
	if patch.Headers != nil {
		rec.Headers = *patch.Headers
	}
	if patch.Timeout != nil {
		rec.Timeout = strings.TrimSpace(*patch.Timeout)
	}
	if patch.Enabled != nil {
		rec.Enabled = *patch.Enabled
	}
	if patch.Visibility != nil {
		rec.Visibility = strings.TrimSpace(*patch.Visibility)
	}
	if patch.AllowedPeerIDs != nil {
		rec.AllowedPeerIDs = *patch.AllowedPeerIDs
	}
	if patch.ApprovalRequired != nil {
		rec.ApprovalRequired = *patch.ApprovalRequired
	}
}

type rowScanner interface {
	Scan(dest ...any) error
}

func scanRecord(scanner rowScanner) (*ServerRecord, error) {
	var (
		argsJSON, envJSON, headersJSON, peerIDsJSON                   string
		enabledInt, approvalInt                                       int
		createdAt, updatedAt                                          int64
		id, owner, name, transport, command, url, timeout, visibility string
	)
	err := scanner.Scan(
		&id, &owner, &name, &transport, &command,
		&argsJSON, &envJSON, &url, &headersJSON,
		&timeout, &enabledInt, &visibility, &peerIDsJSON,
		&approvalInt, &createdAt, &updatedAt)
	if err != nil {
		return nil, err
	}
	args, _ := unmarshalStringSlice(argsJSON)
	env, _ := unmarshalStringMap(envJSON)
	headers, _ := unmarshalStringMap(headersJSON)
	peerIDs, _ := unmarshalStringSlice(peerIDsJSON)

	return &ServerRecord{
		ID:               id,
		OwnerPeerID:      owner,
		Name:             name,
		Transport:        transport,
		Command:          command,
		Args:             args,
		Env:              env,
		URL:              url,
		Headers:          headers,
		Timeout:          timeout,
		Enabled:          enabledInt != 0,
		Visibility:       visibility,
		AllowedPeerIDs:   peerIDs,
		ApprovalRequired: approvalInt != 0,
		CreatedAt:        createdAt,
		UpdatedAt:        updatedAt,
	}, nil
}

func collectRecords(rows *sql.Rows) ([]ServerRecord, error) {
	var out []ServerRecord
	for rows.Next() {
		rec, err := scanRecord(rows)
		if err != nil {
			return nil, fmt.Errorf("mcpregistry: scan: %w", err)
		}
		out = append(out, *rec)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("mcpregistry: rows: %w", err)
	}
	return out, nil
}

// sanitizePrefixToken produces a safe, lowercase token by replacing all
// non-alphanumeric characters with a single underscore and collapsing
// consecutive underscores. Matches mcp/manager.go behavior.
func sanitizePrefixToken(s string) string {
	value := strings.ToLower(strings.TrimSpace(s))
	if value == "" {
		return ""
	}
	var b strings.Builder
	b.Grow(len(value))
	lastUnderscore := false
	for _, r := range value {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			lastUnderscore = false
			continue
		}
		if !lastUnderscore {
			b.WriteByte('_')
			lastUnderscore = true
		}
	}
	return strings.Trim(b.String(), "_")
}

func boolToInt(v bool) int {
	if v {
		return 1
	}
	return 0
}

func marshalStringSlice(s []string) (string, error) {
	if s == nil {
		return "[]", nil
	}
	b, err := json.Marshal(s)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func unmarshalStringSlice(s string) ([]string, error) {
	if s == "" || s == "[]" {
		return nil, nil
	}
	var out []string
	if err := json.Unmarshal([]byte(s), &out); err != nil {
		return nil, err
	}
	return out, nil
}

func marshalStringMap(m map[string]string) (string, error) {
	if m == nil {
		return "{}", nil
	}
	b, err := json.Marshal(m)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func unmarshalStringMap(s string) (map[string]string, error) {
	if s == "" || s == "{}" {
		return nil, nil
	}
	out := make(map[string]string)
	if err := json.Unmarshal([]byte(s), &out); err != nil {
		return nil, err
	}
	return out, nil
}

func copyStringSlice(src []string) []string {
	if src == nil {
		return nil
	}
	out := make([]string, len(src))
	copy(out, src)
	return out
}

func copyStringMap(src map[string]string) map[string]string {
	if src == nil {
		return nil
	}
	out := make(map[string]string, len(src))
	for k, v := range src {
		out[k] = v
	}
	return out
}
