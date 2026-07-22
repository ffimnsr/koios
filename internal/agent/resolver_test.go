package agent

import (
	"context"
	"net/http"
	"path/filepath"
	"testing"

	"github.com/ffimnsr/koios/internal/peerllm"
	"github.com/ffimnsr/koios/internal/preferences"
	"github.com/ffimnsr/koios/internal/session"
	"github.com/ffimnsr/koios/internal/types"
)

// simpleProvider is a minimal Provider for resolver tests.
type simpleProvider struct{}

func (s *simpleProvider) Complete(_ context.Context, _ *types.ChatRequest) (*types.ChatResponse, error) {
	return &types.ChatResponse{Choices: []types.ChatChoice{{Message: types.Message{Role: "assistant", Content: "ok"}}}}, nil
}

func (s *simpleProvider) CompleteStream(_ context.Context, _ *types.ChatRequest, _ http.ResponseWriter) (string, error) {
	return "", nil
}

func TestPeerAwareResolverFallsBackToGlobal(t *testing.T) {
	global := &simpleProvider{}
	store := session.New(10)
	prefStore, err := preferences.New(filepath.Join(t.TempDir(), "pref.db"))
	if err != nil {
		t.Fatalf("preferences.New: %v", err)
	}
	defer prefStore.Close()

	resolver := NewPeerAwareResolver(global, "gpt-4", 0, 0, nil, prefStore, store)

	prov, model, err := resolver.ResolveProvider(context.Background(), "alice", "", "")
	if err != nil {
		t.Fatalf("ResolveProvider: %v", err)
	}
	if prov != global {
		t.Fatal("expected global provider fallback")
	}
	if model != "gpt-4" {
		t.Fatalf("model = %q, want gpt-4", model)
	}
}

func TestPeerAwareResolverUsesModelOverride(t *testing.T) {
	global := &simpleProvider{}
	store := session.New(10)
	resolver := NewPeerAwareResolver(global, "gpt-4", 0, 0, nil, nil, store)

	prov, model, err := resolver.ResolveProvider(context.Background(), "alice", "", "claude-3")
	if err != nil {
		t.Fatalf("ResolveProvider: %v", err)
	}
	if prov != global {
		t.Fatal("expected global provider")
	}
	if model != "claude-3" {
		t.Fatalf("model = %q, want claude-3", model)
	}
}

func TestPeerAwareResolverSessionProfile(t *testing.T) {
	global := &simpleProvider{}
	store := session.New(10)
	if err := store.SetPolicy("alice", session.SessionPolicy{ProviderProfile: "custom"}); err != nil {
		t.Fatalf("SetPolicy: %v", err)
	}
	resolver := NewPeerAwareResolver(global, "gpt-4", 0, 0, nil, nil, store)

	prov, model, err := resolver.ResolveProvider(context.Background(), "alice", "alice", "")
	if err != nil {
		t.Fatalf("ResolveProvider: %v", err)
	}
	if prov != global {
		t.Fatal("expected global fallback when profile not in store")
	}
	if model != "gpt-4" {
		t.Fatalf("model = %q", model)
	}
}

func TestPeerAwareResolverPeerDefault(t *testing.T) {
	global := &simpleProvider{}
	dir := t.TempDir()
	prefStore, err := preferences.New(filepath.Join(dir, "pref.db"))
	if err != nil {
		t.Fatalf("preferences.New: %v", err)
	}
	defer prefStore.Close()

	if _, err = prefStore.Set(context.Background(), "alice", preferences.Input{
		Key:   "peer.llm.default_provider_profile",
		Value: "my-profile",
	}); err != nil {
		t.Fatalf("prefStore.Set: %v", err)
	}

	store := session.New(10)
	resolver := NewPeerAwareResolver(global, "gpt-4", 0, 0, nil, prefStore, store)

	prov, model, err := resolver.ResolveProvider(context.Background(), "alice", "", "")
	if err != nil {
		t.Fatalf("ResolveProvider: %v", err)
	}
	if prov != global {
		t.Fatal("expected global fallback when peerLLMStore is nil")
	}
	if model != "gpt-4" {
		t.Fatalf("model = %q", model)
	}
}

func TestPeerAwareResolverEmptySessionKey(t *testing.T) {
	global := &simpleProvider{}
	store := session.New(10)
	resolver := NewPeerAwareResolver(global, "gpt-4", 0, 0, nil, nil, store)

	prov, model, err := resolver.ResolveProvider(context.Background(), "alice", "", "")
	if err != nil {
		t.Fatalf("ResolveProvider: %v", err)
	}
	if prov != global {
		t.Fatal("expected global provider")
	}
	if model != "gpt-4" {
		t.Fatalf("model = %q", model)
	}
}

func TestPeerAwareResolverInvalidateCache(t *testing.T) {
	global := &simpleProvider{}
	store := session.New(10)

	peerStore, err := peerllm.New(filepath.Join(t.TempDir(), "peer_llm.db"))
	if err != nil {
		t.Fatalf("peerllm.New: %v", err)
	}
	defer peerStore.Close()

	if _, err = peerStore.Set(context.Background(), "alice", peerllm.Input{
		Name:     "test-prov",
		Provider: "openai",
		APIKey:   "sk-test-123",
	}); err != nil {
		t.Fatalf("peerStore.Set: %v", err)
	}

	// Activate via session policy so resolver uses it.
	if err := store.SetPolicy("alice", session.SessionPolicy{ProviderProfile: "test-prov"}); err != nil {
		t.Fatalf("SetPolicy: %v", err)
	}

	resolver := NewPeerAwareResolver(global, "gpt-4", 0, 0, peerStore, nil, store)

	prov1, _, err := resolver.ResolveProvider(context.Background(), "alice", "alice", "")
	if err != nil {
		t.Fatalf("first ResolveProvider: %v", err)
	}
	if prov1 == global {
		t.Fatal("expected resolved provider, not global")
	}

	resolver.InvalidateCache("alice", "test-prov")

	prov2, _, err := resolver.ResolveProvider(context.Background(), "alice", "alice", "")
	if err != nil {
		t.Fatalf("second ResolveProvider: %v", err)
	}
	if prov2 == global {
		t.Fatal("expected resolved provider after cache invalidation")
	}
}

func TestPeerAwareResolverDisabledProfile(t *testing.T) {
	global := &simpleProvider{}
	store := session.New(10)

	peerStore, err := peerllm.New(filepath.Join(t.TempDir(), "peer_llm.db"))
	if err != nil {
		t.Fatalf("peerllm.New: %v", err)
	}
	defer peerStore.Close()

	disabled := false
	if _, err = peerStore.Set(context.Background(), "alice", peerllm.Input{
		Name:     "disabled-prov",
		Provider: "openai",
		APIKey:   "sk-test",
		Enabled:  &disabled,
	}); err != nil {
		t.Fatalf("peerStore.Set: %v", err)
	}

	// Activate disabled profile via session.
	if err := store.SetPolicy("alice", session.SessionPolicy{ProviderProfile: "disabled-prov"}); err != nil {
		t.Fatalf("SetPolicy: %v", err)
	}

	resolver := NewPeerAwareResolver(global, "gpt-4", 0, 0, peerStore, nil, store)

	prov, model, err := resolver.ResolveProvider(context.Background(), "alice", "alice", "")
	if err != nil {
		t.Fatalf("ResolveProvider: %v", err)
	}
	if prov != global {
		t.Fatal("expected global fallback for disabled profile")
	}
	if model != "gpt-4" {
		t.Fatalf("model = %q", model)
	}
}

func TestPeerAwareResolverClearsStalePeerDefaultProfile(t *testing.T) {
	global := &simpleProvider{}
	dir := t.TempDir()
	prefStore, err := preferences.New(filepath.Join(dir, "pref.db"))
	if err != nil {
		t.Fatalf("preferences.New: %v", err)
	}
	defer prefStore.Close()
	peerStore, err := peerllm.New(filepath.Join(dir, "peer_llm.db"))
	if err != nil {
		t.Fatalf("peerllm.New: %v", err)
	}
	defer peerStore.Close()
	if _, err := prefStore.Set(context.Background(), "alice", preferences.Input{
		Key:   "peer.llm.default_provider_profile",
		Value: "missing-profile",
	}); err != nil {
		t.Fatalf("prefStore.Set: %v", err)
	}
	resolver := NewPeerAwareResolver(global, "gpt-4", 0, 0, peerStore, prefStore, session.New(10))

	prov, model, err := resolver.ResolveProvider(context.Background(), "alice", "", "")
	if err != nil {
		t.Fatalf("ResolveProvider: %v", err)
	}
	if prov != global {
		t.Fatal("expected global provider fallback")
	}
	if model != "gpt-4" {
		t.Fatalf("model = %q, want gpt-4", model)
	}
	if _, err := prefStore.Get(context.Background(), "alice", "peer.llm.default_provider_profile", "global"); err == nil {
		t.Fatal("expected stale peer default provider profile to be cleared")
	}
}

func TestPeerAwareResolverClearsStaleSessionProfile(t *testing.T) {
	global := &simpleProvider{}
	store := session.New(10)
	if err := store.SetPolicy("alice::chat", session.SessionPolicy{ProviderProfile: "missing-profile", ThinkLevel: "medium"}); err != nil {
		t.Fatalf("SetPolicy: %v", err)
	}
	peerStore, err := peerllm.New(filepath.Join(t.TempDir(), "peer_llm.db"))
	if err != nil {
		t.Fatalf("peerllm.New: %v", err)
	}
	defer peerStore.Close()
	resolver := NewPeerAwareResolver(global, "gpt-4", 0, 0, peerStore, nil, store)

	prov, model, err := resolver.ResolveProvider(context.Background(), "alice", "alice::chat", "")
	if err != nil {
		t.Fatalf("ResolveProvider: %v", err)
	}
	if prov != global {
		t.Fatal("expected global provider fallback")
	}
	if model != "gpt-4" {
		t.Fatalf("model = %q, want gpt-4", model)
	}
	policy := store.Policy("alice::chat")
	if policy.ProviderProfile != "" {
		t.Fatalf("provider profile = %q, want empty", policy.ProviderProfile)
	}
	if policy.ThinkLevel != "medium" {
		t.Fatalf("think level = %q, want medium", policy.ThinkLevel)
	}
}

func TestPeerAwareResolverMissingProfileFallsBack(t *testing.T) {
	global := &simpleProvider{}
	store := session.New(10)

	peerStore, err := peerllm.New(filepath.Join(t.TempDir(), "peer_llm.db"))
	if err != nil {
		t.Fatalf("peerllm.New: %v", err)
	}
	defer peerStore.Close()

	if err := store.SetPolicy("alice", session.SessionPolicy{ProviderProfile: "nonexistent"}); err != nil {
		t.Fatalf("SetPolicy: %v", err)
	}

	resolver := NewPeerAwareResolver(global, "gpt-4", 0, 0, peerStore, nil, store)

	prov, model, err := resolver.ResolveProvider(context.Background(), "alice", "alice", "")
	if err != nil {
		t.Fatalf("ResolveProvider: %v", err)
	}
	if prov != global {
		t.Fatal("expected global fallback for missing profile")
	}
	if model != "gpt-4" {
		t.Fatalf("model = %q", model)
	}
}

func TestPeerAwareResolverPrefersExplicitModel(t *testing.T) {
	global := &simpleProvider{}
	store := session.New(10)

	peerStore, err := peerllm.New(filepath.Join(t.TempDir(), "peer_llm.db"))
	if err != nil {
		t.Fatalf("peerllm.New: %v", err)
	}
	defer peerStore.Close()

	if _, err = peerStore.Set(context.Background(), "alice", peerllm.Input{
		Name:         "work",
		Provider:     "openai",
		APIKey:       "sk-key",
		DefaultModel: "gpt-4",
	}); err != nil {
		t.Fatalf("peerStore.Set: %v", err)
	}

	// Activate via session.
	if err := store.SetPolicy("alice", session.SessionPolicy{ProviderProfile: "work"}); err != nil {
		t.Fatalf("SetPolicy: %v", err)
	}

	resolver := NewPeerAwareResolver(global, "gpt-4", 0, 0, peerStore, nil, store)

	// With no model override, uses profile's default model.
	prov, model, err := resolver.ResolveProvider(context.Background(), "alice", "alice", "")
	if err != nil {
		t.Fatalf("ResolveProvider: %v", err)
	}
	if prov == global {
		t.Fatal("expected resolved provider")
	}
	if model != "gpt-4" {
		t.Fatalf("model = %q, want gpt-4", model)
	}

	// With explicit model override that differs from global, should use override.
	prov2, model2, err := resolver.ResolveProvider(context.Background(), "alice", "alice", "claude-3")
	if err != nil {
		t.Fatalf("ResolveProvider with override: %v", err)
	}
	if prov2 == global {
		t.Fatal("expected resolved provider")
	}
	if model2 != "claude-3" {
		t.Fatalf("model = %q, want claude-3", model2)
	}
}

func TestPeerAwareResolverPrefersProfileModelOverGlobalDefault(t *testing.T) {
	global := &simpleProvider{}
	store := session.New(10)

	peerStore, err := peerllm.New(filepath.Join(t.TempDir(), "peer_llm.db"))
	if err != nil {
		t.Fatalf("peerllm.New: %v", err)
	}
	defer peerStore.Close()

	// Profile has a different default model than the global.
	if _, err = peerStore.Set(context.Background(), "alice", peerllm.Input{
		Name:         "special",
		Provider:     "openai",
		APIKey:       "sk-key",
		DefaultModel: "gpt-4-special",
	}); err != nil {
		t.Fatalf("peerStore.Set: %v", err)
	}

	if err := store.SetPolicy("alice", session.SessionPolicy{ProviderProfile: "special"}); err != nil {
		t.Fatalf("SetPolicy: %v", err)
	}

	// Global model is "gpt-4", profile has "gpt-4-special".
	resolver := NewPeerAwareResolver(global, "gpt-4", 0, 0, peerStore, nil, store)

	// When modelOverride equals the global model ("gpt-4"), the profile's
	// default ("gpt-4-special") should take precedence.
	prov, model, err := resolver.ResolveProvider(context.Background(), "alice", "alice", "gpt-4")
	if err != nil {
		t.Fatalf("ResolveProvider: %v", err)
	}
	if prov == global {
		t.Fatal("expected resolved provider")
	}
	if model != "gpt-4-special" {
		t.Fatalf("model = %q, want gpt-4-special (profile default over global)", model)
	}
}
