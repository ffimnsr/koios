package peerllm

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

func TestStoreSetAndGet(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	defer store.Close()

	_, err := store.Set(ctx, "alice", Input{
		Name:         "openai-work",
		Provider:     "openai",
		APIKey:       "sk-test-key-1234567890",
		BaseURL:      "https://api.openai.com",
		DefaultModel: "gpt-4.1",
	})
	if err != nil {
		t.Fatalf("Set: %v", err)
	}

	prof, err := store.Get(ctx, "alice", "openai-work")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if prof.Name != "openai-work" {
		t.Fatalf("Name = %q", prof.Name)
	}
	if prof.Provider != "openai" {
		t.Fatalf("Provider = %q", prof.Provider)
	}
	if prof.APIKeyEnc != "sk-test-key-1234567890" {
		t.Fatalf("APIKeyEnc = %q, want decrypted key", prof.APIKeyEnc)
	}
	if prof.BaseURL != "https://api.openai.com" {
		t.Fatalf("BaseURL = %q", prof.BaseURL)
	}
	if prof.DefaultModel != "gpt-4.1" {
		t.Fatalf("DefaultModel = %q", prof.DefaultModel)
	}
	if !prof.Enabled {
		t.Fatal("expected enabled")
	}
	if prof.ID == "" {
		t.Fatal("expected non-empty ID")
	}
	if prof.PeerID != "alice" {
		t.Fatalf("PeerID = %q", prof.PeerID)
	}
}

func TestStoreGetMissing(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	defer store.Close()

	_, err := store.Get(ctx, "alice", "nonexistent")
	if err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("expected not found error, got %v", err)
	}
}

func TestStoreSetUpdatesExisting(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	defer store.Close()

	_, err := store.Set(ctx, "alice", Input{
		Name:         "work",
		Provider:     "openai",
		APIKey:       "sk-original",
		DefaultModel: "gpt-4",
	})
	if err != nil {
		t.Fatalf("first Set: %v", err)
	}

	// Update without providing a new API key — existing key preserved.
	_, err = store.Set(ctx, "alice", Input{
		Name:         "work",
		Provider:     "openai",
		DefaultModel: "gpt-4.1",
	})
	if err != nil {
		t.Fatalf("update Set: %v", err)
	}

	prof, err := store.Get(ctx, "alice", "work")
	if err != nil {
		t.Fatalf("Get after update: %v", err)
	}
	if prof.DefaultModel != "gpt-4.1" {
		t.Fatalf("DefaultModel = %q, want gpt-4.1", prof.DefaultModel)
	}
	if prof.APIKeyEnc != "sk-original" {
		t.Fatalf("APIKeyEnc = %q, want preserved key", prof.APIKeyEnc)
	}
}

func TestStoreSetReplacesAPIKey(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	defer store.Close()

	_, err := store.Set(ctx, "alice", Input{
		Name:     "work",
		Provider: "anthropic",
		APIKey:   "sk-anthropic-v1", // #nosec G101
	})
	if err != nil {
		t.Fatalf("first Set: %v", err)
	}

	// Update with new API key — key replaced.
	_, err = store.Set(ctx, "alice", Input{
		Name:     "work",
		Provider: "anthropic",
		APIKey:   "sk-anthropic-v2", // #nosec G101
	})
	if err != nil {
		t.Fatalf("second Set: %v", err)
	}

	prof, err := store.Get(ctx, "alice", "work")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if prof.APIKeyEnc != "sk-anthropic-v2" {
		t.Fatalf("APIKeyEnc = %q, want v2", prof.APIKeyEnc)
	}
}

func TestStoreListReturnsMaskedKeys(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	defer store.Close()

	_, err := store.Set(ctx, "alice", Input{
		Name:     "a-openai",
		Provider: "openai",
		APIKey:   "sk-abcdefghijklmnop", // #nosec G101
	})
	if err != nil {
		t.Fatalf("Set a-openai: %v", err)
	}
	_, err = store.Set(ctx, "alice", Input{
		Name:     "b-anthropic",
		Provider: "anthropic",
		APIKey:   "sk-anthropic-11223344", // #nosec G101
	})
	if err != nil {
		t.Fatalf("Set b-anthropic: %v", err)
	}

	results, err := store.List(ctx, "alice")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("len(results) = %d, want 2", len(results))
	}
	if results[0].Name != "a-openai" {
		t.Fatalf("results[0].Name = %q", results[0].Name)
	}
	if !results[0].HasAPIKey {
		t.Fatal("expected HasAPIKey")
	}
	if strings.Contains(results[0].APIKeyMasked, "sk-abcdefghijklmnop") {
		t.Fatal("API key not masked in list result")
	}
	if results[0].APIKeyMasked == "" {
		t.Fatal("expected non-empty masked key")
	}
}

func TestStoreListEmpty(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	defer store.Close()

	results, err := store.List(ctx, "alice")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(results) != 0 {
		t.Fatalf("len = %d, want 0", len(results))
	}
}

func TestStoreDelete(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	defer store.Close()

	_, err := store.Set(ctx, "alice", Input{
		Name:     "to-delete",
		Provider: "openai",
		APIKey:   "sk-delete-me",
	})
	if err != nil {
		t.Fatalf("Set: %v", err)
	}

	if err := store.Delete(ctx, "alice", "to-delete"); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	_, err = store.Get(ctx, "alice", "to-delete")
	if err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("expected not found after delete, got %v", err)
	}
}

func TestStoreDeleteMissing(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	defer store.Close()

	err := store.Delete(ctx, "alice", "nonexistent")
	if err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("expected not found error, got %v", err)
	}
}

func TestStoreCrossPeerIsolation(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	defer store.Close()

	_, err := store.Set(ctx, "alice", Input{
		Name:     "shared-name",
		Provider: "openai",
		APIKey:   "sk-alice-key",
	})
	if err != nil {
		t.Fatalf("Set alice: %v", err)
	}
	_, err = store.Set(ctx, "bob", Input{
		Name:     "shared-name",
		Provider: "anthropic",
		APIKey:   "sk-bob-key", // #nosec G101
	})
	if err != nil {
		t.Fatalf("Set bob: %v", err)
	}

	// Alice sees only her profile.
	aliceList, err := store.List(ctx, "alice")
	if err != nil {
		t.Fatalf("List alice: %v", err)
	}
	if len(aliceList) != 1 {
		t.Fatalf("alice list len = %d, want 1", len(aliceList))
	}
	if aliceList[0].Provider != "openai" {
		t.Fatalf("alice provider = %q", aliceList[0].Provider)
	}

	// Bob sees only his profile.
	bobList, err := store.List(ctx, "bob")
	if err != nil {
		t.Fatalf("List bob: %v", err)
	}
	if len(bobList) != 1 {
		t.Fatalf("bob list len = %d, want 1", len(bobList))
	}
	if bobList[0].Provider != "anthropic" {
		t.Fatalf("bob provider = %q", bobList[0].Provider)
	}
}

func TestStoreEmptyAPIKey(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	defer store.Close()

	// Local providers like ollama can have empty API keys.
	_, err := store.Set(ctx, "alice", Input{
		Name:         "local-ollama",
		Provider:     "ollama",
		APIKey:       "",
		BaseURL:      "http://localhost:11434",
		DefaultModel: "llama3",
	})
	if err != nil {
		t.Fatalf("Set: %v", err)
	}

	prof, err := store.Get(ctx, "alice", "local-ollama")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if prof.APIKeyEnc != "" {
		t.Fatalf("expected empty API key, got %q", prof.APIKeyEnc)
	}

	results, err := store.List(ctx, "alice")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("len = %d", len(results))
	}
	if results[0].HasAPIKey {
		t.Fatal("expected HasAPIKey=false for empty key")
	}
	if results[0].APIKeyMasked != "" {
		t.Fatalf("expected empty masked key, got %q", results[0].APIKeyMasked)
	}
}

func TestStoreSetValidatesRequiredFields(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	defer store.Close()

	cases := []struct {
		name string
		in   Input
	}{
		{"empty name", Input{Provider: "openai", APIKey: "sk-test"}},
		{"empty provider", Input{Name: "test", APIKey: "sk-test"}},
	}
	for _, tc := range cases {
		_, err := store.Set(ctx, "alice", tc.in)
		if err == nil {
			t.Fatalf("expected error for %s", tc.name)
		}
	}
}

// --- test helpers ---

func newTestStore(t *testing.T) *Store {
	t.Helper()
	dir := t.TempDir()
	store, err := New(filepath.Join(dir, "peer_llm.db"))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	return store
}
