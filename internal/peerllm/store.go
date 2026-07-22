// Package peerllm provides a per-peer encrypted store for BYOK LLM provider profiles.
//
// Each profile stores the provider type, base URL, default model, and an
// encrypted API key so that providers can be linked to individual peers and
// resolved at request time rather than using a single gateway-global provider.
package peerllm

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	"github.com/ffimnsr/koios/internal/config"
	_ "modernc.org/sqlite"
)

// ProviderProfile is one BYOK LLM provider profile linked to a peer.
// The API key is encrypted at rest and never serialised to JSON directly.
type ProviderProfile struct {
	ID           string `json:"id"`
	PeerID       string `json:"peer_id"`
	Name         string `json:"name"`
	Provider     string `json:"provider"`
	APIKeyEnc    string `json:"-"` // encrypted at rest, never in JSON output
	BaseURL      string `json:"base_url,omitempty"`
	DefaultModel string `json:"default_model"`
	Enabled      bool   `json:"enabled"`
	TestedAt     int64  `json:"tested_at,omitempty"`
	CreatedAt    int64  `json:"created_at"`
	UpdatedAt    int64  `json:"updated_at"`
}

// ProfileResult is the public-safe representation of a provider profile
// returned by List. The API key is masked and never revealed in full.
type ProfileResult struct {
	ID           string `json:"id"`
	PeerID       string `json:"peer_id"`
	Name         string `json:"name"`
	Provider     string `json:"provider"`
	HasAPIKey    bool   `json:"has_api_key"`
	APIKeyMasked string `json:"api_key_masked,omitempty"`
	BaseURL      string `json:"base_url,omitempty"`
	DefaultModel string `json:"default_model"`
	Enabled      bool   `json:"enabled"`
	TestedAt     int64  `json:"tested_at,omitempty"`
	CreatedAt    int64  `json:"created_at"`
	UpdatedAt    int64  `json:"updated_at"`
}

// Input holds the fields for setting a provider profile.
// APIKey is plaintext input; it is encrypted before storage.
type Input struct {
	Name         string
	Provider     string
	APIKey       string // plaintext; encrypted at rest
	BaseURL      string
	DefaultModel string
	// Enabled, when nil, defaults to true on create and is left unchanged on update.
	Enabled *bool
}

// Store persists per-peer LLM provider profiles in a local SQLite database.
// API keys are encrypted at rest using the same local Koios hidden-secret
// mechanism as the gateway config file.
type Store struct {
	db *sql.DB
}

// New opens or creates the peer LLM provider profile database at dbPath.
func New(dbPath string) (*Store, error) {
	db, err := sql.Open("sqlite", dbPath+"?_journal_mode=WAL&_foreign_keys=on")
	if err != nil {
		return nil, fmt.Errorf("peerllm: open db: %w", err)
	}
	db.SetMaxOpenConns(1)
	if err := migrate(context.Background(), db); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("peerllm: migrate: %w", err)
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

// Set creates or updates a provider profile for the given peer.
// The API key is encrypted at rest using the local Koios hidden-secret key.
func (s *Store) Set(ctx context.Context, peerID string, input Input) (*ProviderProfile, error) {
	name := strings.TrimSpace(input.Name)
	if name == "" {
		return nil, fmt.Errorf("name is required")
	}
	provider := strings.TrimSpace(input.Provider)
	if provider == "" {
		return nil, fmt.Errorf("provider is required")
	}
	peerID = strings.TrimSpace(peerID)
	if peerID == "" {
		return nil, fmt.Errorf("peer_id is required")
	}

	// Encrypt the API key if provided.
	var apiKeyEnc string
	if key := strings.TrimSpace(input.APIKey); key != "" {
		hidden, err := config.HideSecret(key)
		if err != nil {
			return nil, fmt.Errorf("encrypt api key: %w", err)
		}
		apiKeyEnc = hidden
	}

	baseURL := strings.TrimSpace(input.BaseURL)
	defaultModel := strings.TrimSpace(input.DefaultModel)
	now := time.Now().Unix()

	// Check for existing profile with same peer+name.
	var existingID string
	var existingEncKey string
	err := s.db.QueryRowContext(ctx,
		`SELECT id, COALESCE(api_key_enc, '') FROM peer_llm_profiles WHERE peer_id = ? AND name = ?`,
		peerID, name).Scan(&existingID, &existingEncKey)
	if err != nil && err != sql.ErrNoRows {
		return nil, fmt.Errorf("lookup existing profile: %w", err)
	}

	if existingID != "" {
		// Preserve existing encrypted key if no new key provided.
		if apiKeyEnc == "" {
			apiKeyEnc = existingEncKey
		}
		enabled := 1
		if input.Enabled != nil {
			if !*input.Enabled {
				enabled = 0
			}
		} else {
			// Leave existing enabled state unchanged.
			var existingEnabled int
			_ = s.db.QueryRowContext(ctx,
				`SELECT enabled FROM peer_llm_profiles WHERE id = ?`, existingID).Scan(&existingEnabled)
			enabled = existingEnabled
		}
		if _, err := s.db.ExecContext(ctx,
			`UPDATE peer_llm_profiles SET provider = ?, api_key_enc = ?, base_url = ?, default_model = ?, enabled = ?, updated_at = ? WHERE id = ? AND peer_id = ?`,
			provider, apiKeyEnc, baseURL, defaultModel, enabled, now, existingID, peerID); err != nil {
			return nil, fmt.Errorf("update profile: %w", err)
		}
		return s.getByID(ctx, peerID, existingID)
	}

	// Insert new.
	id, err := randomID()
	if err != nil {
		return nil, fmt.Errorf("generate id: %w", err)
	}
	enabled := 1
	if input.Enabled != nil {
		enabled = boolToInt(*input.Enabled)
	}
	if _, err := s.db.ExecContext(ctx,
		`INSERT INTO peer_llm_profiles(id, peer_id, name, provider, api_key_enc, base_url, default_model, enabled, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		id, peerID, name, provider, apiKeyEnc, baseURL, defaultModel, enabled, now, now); err != nil {
		return nil, fmt.Errorf("insert profile: %w", err)
	}
	return s.getByID(ctx, peerID, id)
}

// Get returns a single provider profile by name for peerID.
// The API key is decrypted and available in the returned profile.
func (s *Store) Get(ctx context.Context, peerID, name string) (*ProviderProfile, error) {
	peerID = strings.TrimSpace(peerID)
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, fmt.Errorf("name is required")
	}
	profile, err := scanProfile(s.db.QueryRowContext(ctx,
		`SELECT id, peer_id, name, provider, COALESCE(api_key_enc,''), COALESCE(base_url,''), COALESCE(default_model,''), enabled, tested_at, created_at, updated_at
		   FROM peer_llm_profiles WHERE peer_id = ? AND name = ?`,
		peerID, name))
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("provider profile %q not found", name)
	}
	if err != nil {
		return nil, fmt.Errorf("get profile: %w", err)
	}
	// Decrypt the API key.
	if profile.APIKeyEnc != "" {
		plaintext, err := config.RevealSecret(profile.APIKeyEnc)
		if err != nil {
			return nil, fmt.Errorf("decrypt api key: %w", err)
		}
		profile.APIKeyEnc = plaintext
	}
	return profile, nil
}

// List returns all provider profiles for peerID with masked API key previews.
// The API key is never returned in full.
func (s *Store) List(ctx context.Context, peerID string) ([]ProfileResult, error) {
	peerID = strings.TrimSpace(peerID)
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, peer_id, name, provider, COALESCE(api_key_enc,''), COALESCE(base_url,''), COALESCE(default_model,''), enabled, tested_at, created_at, updated_at
		   FROM peer_llm_profiles WHERE peer_id = ?
		   ORDER BY name ASC`,
		peerID)
	if err != nil {
		return nil, fmt.Errorf("list profiles: %w", err)
	}
	defer rows.Close()

	var results []ProfileResult
	for rows.Next() {
		p, err := scanProfile(rows)
		if err != nil {
			return nil, fmt.Errorf("scan profile: %w", err)
		}
		results = append(results, p.toResult())
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("rows iteration: %w", err)
	}
	if results == nil {
		results = []ProfileResult{}
	}
	return results, nil
}

// Delete removes a provider profile by name for peerID.
func (s *Store) Delete(ctx context.Context, peerID, name string) error {
	peerID = strings.TrimSpace(peerID)
	name = strings.TrimSpace(name)
	if name == "" {
		return fmt.Errorf("name is required")
	}
	res, err := s.db.ExecContext(ctx,
		`DELETE FROM peer_llm_profiles WHERE peer_id = ? AND name = ?`,
		peerID, name)
	if err != nil {
		return fmt.Errorf("delete profile: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("provider profile %q not found", name)
	}
	return nil
}

// --- internal helpers ---

func (s *Store) getByID(ctx context.Context, peerID, id string) (*ProviderProfile, error) {
	profile, err := scanProfile(s.db.QueryRowContext(ctx,
		`SELECT id, peer_id, name, provider, COALESCE(api_key_enc,''), COALESCE(base_url,''), COALESCE(default_model,''), enabled, tested_at, created_at, updated_at
		   FROM peer_llm_profiles WHERE id = ? AND peer_id = ?`,
		id, peerID))
	if err != nil {
		return nil, fmt.Errorf("get profile by id: %w", err)
	}
	if profile.APIKeyEnc != "" {
		plaintext, err := config.RevealSecret(profile.APIKeyEnc)
		if err != nil {
			return nil, fmt.Errorf("decrypt api key: %w", err)
		}
		profile.APIKeyEnc = plaintext
	}
	return profile, nil
}

func (p *ProviderProfile) toResult() ProfileResult {
	r := ProfileResult{
		ID:           p.ID,
		PeerID:       p.PeerID,
		Name:         p.Name,
		Provider:     p.Provider,
		HasAPIKey:    p.APIKeyEnc != "",
		APIKeyMasked: maskedAPIKey(p.APIKeyEnc),
		BaseURL:      p.BaseURL,
		DefaultModel: p.DefaultModel,
		Enabled:      p.Enabled,
		TestedAt:     p.TestedAt,
		CreatedAt:    p.CreatedAt,
		UpdatedAt:    p.UpdatedAt,
	}
	// For result display, mask the encrypted blob — the actual key is never exposed.
	if r.HasAPIKey {
		r.APIKeyMasked = maskedAPIKey(p.APIKeyEnc)
	}
	return r
}

func maskedAPIKey(key string) string {
	if key == "" {
		return ""
	}
	if len(key) <= 8 {
		return strings.Repeat("*", len(key))
	}
	return key[:4] + strings.Repeat("*", len(key)-8) + key[len(key)-4:]
}

func migrate(ctx context.Context, db *sql.DB) error {
	_, err := db.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS peer_llm_profiles (
			id            TEXT PRIMARY KEY,
			peer_id       TEXT NOT NULL,
			name          TEXT NOT NULL,
			provider      TEXT NOT NULL,
			api_key_enc   TEXT NOT NULL DEFAULT '',
			base_url      TEXT NOT NULL DEFAULT '',
			default_model TEXT NOT NULL DEFAULT '',
			enabled       INTEGER NOT NULL DEFAULT 1,
			tested_at     INTEGER NOT NULL DEFAULT 0,
			created_at    INTEGER NOT NULL,
			updated_at    INTEGER NOT NULL,
			UNIQUE(peer_id, name)
		);
		CREATE INDEX IF NOT EXISTS idx_peer_llm_peer_id ON peer_llm_profiles(peer_id);
	`)
	return err
}

type rowScanner interface {
	Scan(dest ...any) error
}

func scanProfile(scanner rowScanner) (*ProviderProfile, error) {
	var p ProviderProfile
	err := scanner.Scan(
		&p.ID, &p.PeerID, &p.Name, &p.Provider,
		&p.APIKeyEnc, &p.BaseURL, &p.DefaultModel,
		&p.Enabled, &p.TestedAt, &p.CreatedAt, &p.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return &p, nil
}

func randomID() (string, error) {
	b := make([]byte, 12)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

func boolToInt(v bool) int {
	if v {
		return 1
	}
	return 0
}
