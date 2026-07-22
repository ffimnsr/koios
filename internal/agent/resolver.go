package agent

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/ffimnsr/koios/internal/config"
	"github.com/ffimnsr/koios/internal/peerllm"
	"github.com/ffimnsr/koios/internal/preferences"
	"github.com/ffimnsr/koios/internal/provider"
	"github.com/ffimnsr/koios/internal/session"
)

// PeerAwareResolver resolves per-request providers from peer-stored profiles.
// It caches constructed providers and falls back to the global provider when
// no peer or session override is configured.
type PeerAwareResolver struct {
	globalProv     Provider
	globalModel    string
	requestTimeout time.Duration
	idleTimeout    time.Duration

	peerStore    *peerllm.Store
	prefStore    *preferences.Store
	sessionStore *session.Store

	mu    sync.RWMutex
	cache map[string]cachedEntry
}

type cachedEntry struct {
	prov  Provider
	model string
}

// NewPeerAwareResolver creates a resolver that looks up BYOK profiles from
// the peer store and session policy, falling back to the global provider.
func NewPeerAwareResolver(
	globalProv Provider,
	globalModel string,
	requestTimeout time.Duration,
	idleTimeout time.Duration,
	peerStore *peerllm.Store,
	prefStore *preferences.Store,
	sessionStore *session.Store,
) *PeerAwareResolver {
	return &PeerAwareResolver{
		globalProv:     globalProv,
		globalModel:    globalModel,
		requestTimeout: requestTimeout,
		idleTimeout:    idleTimeout,
		peerStore:      peerStore,
		prefStore:      prefStore,
		sessionStore:   sessionStore,
		cache:          make(map[string]cachedEntry),
	}
}

// ResolveProvider implements ProviderResolver.
func (r *PeerAwareResolver) ResolveProvider(ctx context.Context, peerID, sessionKey, modelOverride string) (Provider, string, error) {
	// 1. Check session policy for explicit provider_profile override.
	profileName := r.resolveSessionProfile(sessionKey)
	fromSession := profileName != ""
	if profileName == "" {
		// 2. Fall back to peer default provider profile from preferences.
		profileName = r.resolvePeerDefault(ctx, peerID)
	}
	if profileName == "" {
		// 3. No override — use global provider.
		if modelOverride != "" {
			return r.globalProv, modelOverride, nil
		}
		return r.globalProv, r.globalModel, nil
	}

	// Resolve the profile and build or cache the provider.
	if r.peerStore == nil {
		// No peerStore configured — fall back to global.
		if modelOverride != "" {
			return r.globalProv, modelOverride, nil
		}
		return r.globalProv, r.globalModel, nil
	}
	return r.resolveNamedProfile(ctx, peerID, sessionKey, profileName, modelOverride, fromSession)
}

// resolveSessionProfile checks the session policy for a provider_profile override.
func (r *PeerAwareResolver) resolveSessionProfile(sessionKey string) string {
	if r.sessionStore == nil || strings.TrimSpace(sessionKey) == "" {
		return ""
	}
	policy := r.sessionStore.Policy(sessionKey)
	return strings.TrimSpace(policy.ProviderProfile)
}

// resolvePeerDefault checks the peer's stored default provider profile preference.
func (r *PeerAwareResolver) resolvePeerDefault(ctx context.Context, peerID string) string {
	if r.prefStore == nil || strings.TrimSpace(peerID) == "" {
		return ""
	}
	pref, err := r.prefStore.Get(ctx, peerID, "peer.llm.default_provider_profile", "global")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(pref.Value)
}

// resolveNamedProfile returns a cached or freshly built provider for the named profile.
func (r *PeerAwareResolver) resolveNamedProfile(ctx context.Context, peerID, sessionKey, profileName, modelOverride string, fromSession bool) (Provider, string, error) {
	cacheKey := peerID + "::" + profileName

	r.mu.RLock()
	entry, ok := r.cache[cacheKey]
	r.mu.RUnlock()

	if ok {
		// Prefer profile default over global default; honour explicit overrides.
		model := entry.model
		if modelOverride != "" && modelOverride != r.globalModel {
			model = modelOverride
		}
		return entry.prov, model, nil
	}

	// Fetch profile from store and build provider.
	profile, err := r.peerStore.Get(ctx, peerID, profileName)
	if err != nil {
		r.clearMissingProfileReference(ctx, peerID, sessionKey, profileName, fromSession)
		slog.Warn("resolver: profile not found, falling back to global",
			"peer", peerID, "profile", profileName, "err", err)
		if modelOverride != "" {
			return r.globalProv, modelOverride, nil
		}
		return r.globalProv, r.globalModel, nil
	}

	if !profile.Enabled {
		slog.Warn("resolver: profile is disabled, falling back to global",
			"peer", peerID, "profile", profileName)
		if modelOverride != "" {
			return r.globalProv, modelOverride, nil
		}
		return r.globalProv, r.globalModel, nil
	}

	// Build provider from profile, seeding its default model.
	defaultModel := profile.DefaultModel
	if defaultModel == "" {
		defaultModel = r.globalModel
	}
	cfg := &config.Config{
		Provider:       profile.Provider,
		APIKey:         profile.APIKeyEnc,
		BaseURL:        profile.BaseURL,
		Model:          defaultModel,
		RequestTimeout: r.requestTimeout,
		LLMIdleTimeout: r.idleTimeout,
	}
	p, err := provider.New(cfg)
	if err != nil {
		return nil, "", fmt.Errorf("build provider from profile %q: %w", profileName, err)
	}
	prov := p.(Provider)

	r.mu.Lock()
	r.cache[cacheKey] = cachedEntry{prov: prov, model: defaultModel}
	r.mu.Unlock()

	// Prefer profile default model over the global default; only use modelOverride
	// when it differs from the runtime default, preserving explicit user overrides.
	model := defaultModel
	if modelOverride != "" && modelOverride != r.globalModel {
		model = modelOverride
	}
	return prov, model, nil
}

// InvalidateCache clears the cache for a specific peer+profile key.
func (r *PeerAwareResolver) clearMissingProfileReference(ctx context.Context, peerID, sessionKey, profileName string, fromSession bool) {
	profileName = strings.TrimSpace(profileName)
	if profileName == "" {
		return
	}
	if fromSession {
		if r.sessionStore == nil || strings.TrimSpace(sessionKey) == "" {
			return
		}
		if err := r.sessionStore.PatchPolicy(sessionKey, func(policy *session.SessionPolicy) {
			if strings.TrimSpace(policy.ProviderProfile) == profileName {
				policy.ProviderProfile = ""
			}
		}); err != nil {
			slog.Warn("resolver: failed to clear stale session provider profile",
				"peer", peerID, "session_key", sessionKey, "profile", profileName, "err", err)
		}
		return
	}
	if r.prefStore == nil || strings.TrimSpace(peerID) == "" {
		return
	}
	if err := r.prefStore.Delete(ctx, peerID, "peer.llm.default_provider_profile", "global"); err != nil {
		slog.Warn("resolver: failed to clear stale peer default provider profile",
			"peer", peerID, "profile", profileName, "err", err)
	}
}

func (r *PeerAwareResolver) InvalidateCache(peerID, profileName string) {
	cacheKey := peerID + "::" + profileName
	r.mu.Lock()
	delete(r.cache, cacheKey)
	r.mu.Unlock()
}
