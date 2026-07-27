// Package peerllm provides a per-peer encrypted store for BYOK LLM provider profiles.
//
// Each profile stores the provider type, base URL, default model, and one or
// more encrypted API keys so that providers can be linked to individual peers
// and resolved at request time rather than using a single gateway-global provider.
package peerllm

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

// ProviderProfile is one BYOK LLM provider profile linked to a peer.
// API keys are encrypted at rest and never serialised to JSON directly.
type ProviderProfile struct {
	ID           string   `json:"id"`
	PeerID       string   `json:"peer_id"`
	Name         string   `json:"name"`
	Provider     string   `json:"provider"`
	APIKeyEnc    string   `json:"-"` // legacy compatibility: first plaintext key after Get
	APIKeys      []string `json:"-"`
	BaseURL      string   `json:"base_url,omitempty"`
	DefaultModel string   `json:"default_model"`
	Enabled      bool     `json:"enabled"`
	TestedAt     int64    `json:"tested_at,omitempty"`
	CreatedAt    int64    `json:"created_at"`
	UpdatedAt    int64    `json:"updated_at"`
	apiKeysJSON  string
}

// ProfileResult is the public-safe representation of a provider profile
// returned by List. Plaintext API keys are never exposed.
type ProfileResult struct {
	ID           string `json:"id"`
	PeerID       string `json:"peer_id"`
	Name         string `json:"name"`
	Provider     string `json:"provider"`
	HasAPIKey    bool   `json:"has_api_key"`
	APIKeyCount  int    `json:"api_key_count"`
	APIKeyMasked string `json:"api_key_masked,omitempty"`
	BaseURL      string `json:"base_url,omitempty"`
	DefaultModel string `json:"default_model"`
	Enabled      bool   `json:"enabled"`
	TestedAt     int64  `json:"tested_at,omitempty"`
	CreatedAt    int64  `json:"created_at"`
	UpdatedAt    int64  `json:"updated_at"`
}

// Input holds the fields for setting a provider profile.
// APIKey/APIKeys are plaintext input; they are encrypted before storage.
type Input struct {
	Name         string
	Provider     string
	APIKey       string
	APIKeys      []string
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
// API keys are encrypted at rest using the local Koios hidden-secret key.
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

	normalizedKeys, err := config.NormalizeLLMAPIKeys(input.APIKey, input.APIKeys)
	if err != nil {
		return nil, err
	}

	baseURL := strings.TrimSpace(input.BaseURL)
	defaultModel := strings.TrimSpace(input.DefaultModel)
	now := time.Now().Unix()

	var existingID, existingEncKey, existingKeysJSON string
	err = s.db.QueryRowContext(ctx,
		`SELECT id, COALESCE(api_key_enc, ''), COALESCE(api_keys_json, '') FROM peer_llm_profiles WHERE peer_id = ? AND name = ?`,
		peerID, name).Scan(&existingID, &existingEncKey, &existingKeysJSON)
	if err != nil && err != sql.ErrNoRows {
		return nil, fmt.Errorf("lookup existing profile: %w", err)
	}

	var apiKeysJSON, apiKeyEnc string
	if existingID != "" {
		if len(normalizedKeys) == 0 {
			apiKeyEnc = existingEncKey
			apiKeysJSON = existingKeysJSON
		} else {
			if !config.IsLocalLLMProvider(provider) && len(normalizedKeys) == 0 {
				return nil, fmt.Errorf("api key is required for provider %q", provider)
			}
			apiKeysJSON, apiKeyEnc, err = encryptAPIKeys(normalizedKeys)
			if err != nil {
				return nil, err
			}
		}
		enabled := 1
		if input.Enabled != nil {
			enabled = boolToInt(*input.Enabled)
		} else {
			var existingEnabled int
			_ = s.db.QueryRowContext(ctx, `SELECT enabled FROM peer_llm_profiles WHERE id = ?`, existingID).Scan(&existingEnabled)
			enabled = existingEnabled
		}
		if _, err := s.db.ExecContext(ctx,
			`UPDATE peer_llm_profiles SET provider = ?, api_key_enc = ?, api_keys_json = ?, base_url = ?, default_model = ?, enabled = ?, updated_at = ? WHERE id = ? AND peer_id = ?`,
			provider, apiKeyEnc, apiKeysJSON, baseURL, defaultModel, enabled, now, existingID, peerID); err != nil {
			return nil, fmt.Errorf("update profile: %w", err)
		}
		return s.getByID(ctx, peerID, existingID)
	}

	if len(normalizedKeys) == 0 && !config.IsLocalLLMProvider(provider) {
		return nil, fmt.Errorf("api key is required for provider %q", provider)
	}
	apiKeysJSON, apiKeyEnc, err = encryptAPIKeys(normalizedKeys)
	if err != nil {
		return nil, err
	}

	id, err := randomID()
	if err != nil {
		return nil, fmt.Errorf("generate id: %w", err)
	}
	enabled := 1
	if input.Enabled != nil {
		enabled = boolToInt(*input.Enabled)
	}
	if _, err := s.db.ExecContext(ctx,
		`INSERT INTO peer_llm_profiles(id, peer_id, name, provider, api_key_enc, api_keys_json, base_url, default_model, enabled, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		id, peerID, name, provider, apiKeyEnc, apiKeysJSON, baseURL, defaultModel, enabled, now, now); err != nil {
		return nil, fmt.Errorf("insert profile: %w", err)
	}
	return s.getByID(ctx, peerID, id)
}

// Get returns a single provider profile by name for peerID.
// API keys are decrypted and available in the returned profile.
func (s *Store) Get(ctx context.Context, peerID, name string) (*ProviderProfile, error) {
	peerID = strings.TrimSpace(peerID)
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, fmt.Errorf("name is required")
	}
	profile, err := scanProfile(s.db.QueryRowContext(ctx,
		`SELECT id, peer_id, name, provider, COALESCE(api_key_enc,''), COALESCE(api_keys_json,''), COALESCE(base_url,''), COALESCE(default_model,''), enabled, tested_at, created_at, updated_at
		   FROM peer_llm_profiles WHERE peer_id = ? AND name = ?`,
		peerID, name))
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("provider profile %q not found", name)
	}
	if err != nil {
		return nil, fmt.Errorf("get profile: %w", err)
	}
	if err := hydrateProfileSecrets(profile); err != nil {
		return nil, err
	}
	return profile, nil
}

// List returns all provider profiles for peerID with masked API key previews.
// Plaintext API keys are never returned.
func (s *Store) List(ctx context.Context, peerID string) ([]ProfileResult, error) {
	peerID = strings.TrimSpace(peerID)
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, peer_id, name, provider, COALESCE(api_key_enc,''), COALESCE(api_keys_json,''), COALESCE(base_url,''), COALESCE(default_model,''), enabled, tested_at, created_at, updated_at
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
		if err := hydrateProfileSecrets(p); err != nil {
			return nil, err
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
func (s *Store) AddKey(ctx context.Context, peerID, name, apiKey string) (*ProviderProfile, error) {
	return s.mutateKeys(ctx, peerID, name, func(keys []string) ([]string, error) {
		trimmed := strings.TrimSpace(apiKey)
		if trimmed == "" {
			return nil, fmt.Errorf("api_key is required")
		}
		return append(append([]string(nil), keys...), trimmed), nil
	})
}

func (s *Store) RemoveKey(ctx context.Context, peerID, name string, index int) (*ProviderProfile, error) {
	return s.mutateKeys(ctx, peerID, name, func(keys []string) ([]string, error) {
		if index < 0 || index >= len(keys) {
			return nil, fmt.Errorf("index %d out of range", index)
		}
		out := append([]string(nil), keys[:index]...)
		out = append(out, keys[index+1:]...)
		return out, nil
	})
}

func (s *Store) ReplaceKey(ctx context.Context, peerID, name string, index int, apiKey string) (*ProviderProfile, error) {
	return s.mutateKeys(ctx, peerID, name, func(keys []string) ([]string, error) {
		if index < 0 || index >= len(keys) {
			return nil, fmt.Errorf("index %d out of range", index)
		}
		trimmed := strings.TrimSpace(apiKey)
		if trimmed == "" {
			return nil, fmt.Errorf("api_key is required")
		}
		out := append([]string(nil), keys...)
		out[index] = trimmed
		return out, nil
	})
}

func (s *Store) RotateKey(ctx context.Context, peerID, name string, index int, apiKey string) (*ProviderProfile, error) {
	return s.ReplaceKey(ctx, peerID, name, index, apiKey)
}

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

func (s *Store) mutateKeys(ctx context.Context, peerID, name string, mutate func([]string) ([]string, error)) (*ProviderProfile, error) {
	profile, err := s.Get(ctx, peerID, name)
	if err != nil {
		return nil, err
	}
	updatedKeys, err := mutate(profile.APIKeys)
	if err != nil {
		return nil, err
	}
	if len(updatedKeys) == 0 && !config.IsLocalLLMProvider(profile.Provider) {
		return nil, fmt.Errorf("cannot remove the last api key for provider %q", profile.Provider)
	}
	setInput := Input{
		Name:         profile.Name,
		Provider:     profile.Provider,
		APIKeys:      updatedKeys,
		BaseURL:      profile.BaseURL,
		DefaultModel: profile.DefaultModel,
		Enabled:      &profile.Enabled,
	}
	return s.Set(ctx, peerID, setInput)
}

func (s *Store) getByID(ctx context.Context, peerID, id string) (*ProviderProfile, error) {
	profile, err := scanProfile(s.db.QueryRowContext(ctx,
		`SELECT id, peer_id, name, provider, COALESCE(api_key_enc,''), COALESCE(api_keys_json,''), COALESCE(base_url,''), COALESCE(default_model,''), enabled, tested_at, created_at, updated_at
		   FROM peer_llm_profiles WHERE id = ? AND peer_id = ?`,
		id, peerID))
	if err != nil {
		return nil, fmt.Errorf("get profile by id: %w", err)
	}
	if err := hydrateProfileSecrets(profile); err != nil {
		return nil, err
	}
	return profile, nil
}

func (p *ProviderProfile) toResult() ProfileResult {
	masked := maskedAPIKey(firstAPIKey(p.APIKeys))
	return ProfileResult{
		ID:           p.ID,
		PeerID:       p.PeerID,
		Name:         p.Name,
		Provider:     p.Provider,
		HasAPIKey:    len(p.APIKeys) > 0,
		APIKeyCount:  len(p.APIKeys),
		APIKeyMasked: masked,
		BaseURL:      p.BaseURL,
		DefaultModel: p.DefaultModel,
		Enabled:      p.Enabled,
		TestedAt:     p.TestedAt,
		CreatedAt:    p.CreatedAt,
		UpdatedAt:    p.UpdatedAt,
	}
}

func firstAPIKey(keys []string) string {
	for _, key := range keys {
		if trimmed := strings.TrimSpace(key); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func encryptAPIKeys(keys []string) (apiKeysJSON string, apiKeyEnc string, err error) {
	if len(keys) == 0 {
		return "", "", nil
	}
	encrypted := make([]string, 0, len(keys))
	for _, key := range keys {
		hidden, hideErr := config.HideSecret(key)
		if hideErr != nil {
			return "", "", fmt.Errorf("encrypt api key: %w", hideErr)
		}
		encrypted = append(encrypted, hidden)
	}
	blob, err := json.Marshal(encrypted)
	if err != nil {
		return "", "", fmt.Errorf("marshal encrypted api keys: %w", err)
	}
	return string(blob), encrypted[0], nil
}

func hydrateProfileSecrets(profile *ProviderProfile) error {
	if profile == nil {
		return nil
	}
	keys, err := decryptAPIKeys(profile.APIKeyEnc, profile.apiKeysJSON)
	if err != nil {
		return err
	}
	profile.APIKeys = keys
	profile.APIKeyEnc = firstAPIKey(keys)
	return nil
}

func decryptAPIKeys(legacyEnc, apiKeysJSON string) ([]string, error) {
	encrypted, err := decodeEncryptedKeyList(legacyEnc, apiKeysJSON)
	if err != nil {
		return nil, err
	}
	keys := make([]string, 0, len(encrypted))
	for _, hidden := range encrypted {
		plaintext, err := config.RevealSecret(hidden)
		if err != nil {
			return nil, fmt.Errorf("decrypt api key: %w", err)
		}
		if trimmed := strings.TrimSpace(plaintext); trimmed != "" {
			keys = append(keys, trimmed)
		}
	}
	return keys, nil
}

func decodeEncryptedKeyList(legacyEnc, apiKeysJSON string) ([]string, error) {
	if strings.TrimSpace(apiKeysJSON) != "" {
		var encrypted []string
		if err := json.Unmarshal([]byte(apiKeysJSON), &encrypted); err != nil {
			return nil, fmt.Errorf("decode api keys json: %w", err)
		}
		if len(encrypted) > 0 {
			return encrypted, nil
		}
	}
	if strings.TrimSpace(legacyEnc) == "" {
		return nil, nil
	}
	return []string{legacyEnc}, nil
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
	if _, err := db.ExecContext(ctx, `
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
	`); err != nil {
		return err
	}
	if _, err := db.ExecContext(ctx, `ALTER TABLE peer_llm_profiles ADD COLUMN api_keys_json TEXT NOT NULL DEFAULT ''`); err != nil && !strings.Contains(strings.ToLower(err.Error()), "duplicate column") {
		return err
	}
	_, err := db.ExecContext(ctx, `
		UPDATE peer_llm_profiles
		   SET api_keys_json = json_array(api_key_enc)
		 WHERE COALESCE(api_keys_json, '') = ''
		   AND COALESCE(api_key_enc, '') <> ''
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
		&p.APIKeyEnc, &p.apiKeysJSON, &p.BaseURL, &p.DefaultModel,
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
