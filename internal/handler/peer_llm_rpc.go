package handler

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/ffimnsr/koios/internal/config"
	"github.com/ffimnsr/koios/internal/peerllm"
	"github.com/ffimnsr/koios/internal/preferences"
	"github.com/ffimnsr/koios/internal/provider"
	"github.com/ffimnsr/koios/internal/types"
)

// —── peer.llm_provider.set ────────────────────────────────────────────────────

type peerLLMSetParams struct {
	Name         string `json:"name"`
	Provider     string `json:"provider"`
	APIKey       string `json:"api_key"`
	BaseURL      string `json:"base_url"`
	DefaultModel string `json:"default_model"`
	Enabled      *bool  `json:"enabled"`
}

func (h *Handler) rpcPeerLLMSet(ctx context.Context, wsc *wsConn, req *rpcRequest) {
	var p peerLLMSetParams
	if err := decodeParams(req.Params, &p); err != nil {
		wsc.replyErr(req.ID, errCodeInvalidParams, err.Error())
		return
	}
	profile, err := h.peerLLMStore.Set(ctx, wsc.peerID, peerllm.Input{
		Name:         p.Name,
		Provider:     p.Provider,
		APIKey:       p.APIKey,
		BaseURL:      p.BaseURL,
		DefaultModel: p.DefaultModel,
		Enabled:      p.Enabled,
	})
	if err != nil {
		wsc.replyErr(req.ID, errCodeServer, fmt.Sprintf("set provider profile: %s", err))
		return
	}
	// Invalidate provider cache so the next request rebuilds from updated profile.
	if h.agentRuntime != nil {
		h.agentRuntime.InvalidateProviderCache(wsc.peerID, profile.Name)
	}
	wsc.reply(req.ID, map[string]any{
		"ok":            true,
		"id":            profile.ID,
		"name":          profile.Name,
		"provider":      profile.Provider,
		"has_api_key":   profile.APIKeyEnc != "",
		"base_url":      profile.BaseURL,
		"default_model": profile.DefaultModel,
		"enabled":       profile.Enabled,
	})
}

// —── peer.llm_provider.get ────────────────────────────────────────────────────

type peerLLMGetParams struct {
	Name string `json:"name"`
}

func (h *Handler) rpcPeerLLMGet(ctx context.Context, wsc *wsConn, req *rpcRequest) {
	var p peerLLMGetParams
	if err := decodeParams(req.Params, &p); err != nil {
		wsc.replyErr(req.ID, errCodeInvalidParams, err.Error())
		return
	}
	profile, err := h.peerLLMStore.Get(ctx, wsc.peerID, p.Name)
	if err != nil {
		wsc.replyErr(req.ID, errCodeServer, err.Error())
		return
	}
	wsc.reply(req.ID, map[string]any{
		"id":            profile.ID,
		"peer_id":       profile.PeerID,
		"name":          profile.Name,
		"provider":      profile.Provider,
		"has_api_key":   profile.APIKeyEnc != "",
		"base_url":      profile.BaseURL,
		"default_model": profile.DefaultModel,
		"enabled":       profile.Enabled,
		"tested_at":     profile.TestedAt,
		"created_at":    profile.CreatedAt,
		"updated_at":    profile.UpdatedAt,
	})
}

// —── peer.llm_provider.list ──────────────────────────────────────────────────

type peerLLMListParams struct {
	Provider string `json:"provider,omitempty"`
}

func (h *Handler) rpcPeerLLMList(ctx context.Context, wsc *wsConn, req *rpcRequest) {
	var p peerLLMListParams
	if err := decodeParams(req.Params, &p); err != nil {
		wsc.replyErr(req.ID, errCodeInvalidParams, err.Error())
		return
	}
	results, err := h.peerLLMStore.List(ctx, wsc.peerID)
	if err != nil {
		wsc.replyErr(req.ID, errCodeServer, err.Error())
		return
	}
	// Optional client-side filter by provider.
	if prov := strings.TrimSpace(p.Provider); prov != "" {
		filtered := make([]peerllm.ProfileResult, 0, len(results))
		for _, r := range results {
			if strings.EqualFold(r.Provider, prov) {
				filtered = append(filtered, r)
			}
		}
		results = filtered
	}
	wsc.reply(req.ID, map[string]any{
		"profiles": results,
		"count":    len(results),
	})
}

// —── peer.llm_provider.delete ────────────────────────────────────────────────

type peerLLMDeleteParams struct {
	Name string `json:"name"`
}

func (h *Handler) rpcPeerLLMDelete(ctx context.Context, wsc *wsConn, req *rpcRequest) {
	var p peerLLMDeleteParams
	if err := decodeParams(req.Params, &p); err != nil {
		wsc.replyErr(req.ID, errCodeInvalidParams, err.Error())
		return
	}
	if err := h.peerLLMStore.Delete(ctx, wsc.peerID, p.Name); err != nil {
		wsc.replyErr(req.ID, errCodeServer, err.Error())
		return
	}
	if h.preferenceStore != nil {
		pref, err := h.preferenceStore.Get(ctx, wsc.peerID, "peer.llm.default_provider_profile", "global")
		if err == nil && strings.TrimSpace(pref.Value) == strings.TrimSpace(p.Name) {
			if err := h.preferenceStore.Delete(ctx, wsc.peerID, "peer.llm.default_provider_profile", "global"); err != nil {
				slog.Warn("peer.llm_provider.delete: failed to clear deleted default profile preference",
					"peer", wsc.peerID, "profile", p.Name, "err", err)
			}
		}
	}
	if h.store != nil {
		if cleared, err := h.store.ClearProviderProfileReferences(wsc.peerID, p.Name); err != nil {
			slog.Warn("peer.llm_provider.delete: failed to clear session provider profile references",
				"peer", wsc.peerID, "profile", p.Name, "err", err)
		} else if cleared > 0 {
			slog.Info("peer.llm_provider.delete: cleared stale session provider profile references",
				"peer", wsc.peerID, "profile", p.Name, "count", cleared)
		}
	}
	// Invalidate provider cache so stale connections are not reused.
	if h.agentRuntime != nil {
		h.agentRuntime.InvalidateProviderCache(wsc.peerID, p.Name)
	}
	wsc.reply(req.ID, map[string]bool{"ok": true})
}

// —── peer.llm_provider.test ──────────────────────────────────────────────────

type peerLLMTestParams struct {
	Name string `json:"name"`
}

func (h *Handler) rpcPeerLLMTest(ctx context.Context, wsc *wsConn, req *rpcRequest) {
	var p peerLLMTestParams
	if err := decodeParams(req.Params, &p); err != nil {
		wsc.replyErr(req.ID, errCodeInvalidParams, err.Error())
		return
	}
	profile, err := h.peerLLMStore.Get(ctx, wsc.peerID, p.Name)
	if err != nil {
		wsc.replyErr(req.ID, errCodeServer, err.Error())
		return
	}
	result := h.testProviderConnectivity(ctx, profile)
	wsc.reply(req.ID, result)
}

// testProviderConnectivity attempts a lightweight upstream validation call.
// It builds a provider from the stored profile, sends a minimal completion,
// and reports whether the endpoint is reachable and the credentials are valid.
func (h *Handler) testProviderConnectivity(ctx context.Context, profile *peerllm.ProviderProfile) map[string]any {
	if profile.APIKeyEnc == "" && !isLocalProvider(profile.Provider) {
		return map[string]any{
			"ok":            false,
			"name":          profile.Name,
			"provider":      profile.Provider,
			"base_url":      profile.BaseURL,
			"default_model": profile.DefaultModel,
			"has_api_key":   false,
			"checked":       false,
			"error":         "API key is required for this provider",
		}
	}

	// Build a provider from the profile.
	var model string
	if profile.DefaultModel != "" {
		model = profile.DefaultModel
	} else {
		model = "default"
	}
	cfg := &config.Config{
		Provider:       profile.Provider,
		APIKey:         profile.APIKeyEnc,
		BaseURL:        profile.BaseURL,
		Model:          model,
		RequestTimeout: 15 * time.Second,
		LLMIdleTimeout: 5 * time.Second,
	}
	prov, err := provider.New(cfg)
	if err != nil {
		return map[string]any{
			"ok":            false,
			"name":          profile.Name,
			"provider":      profile.Provider,
			"base_url":      profile.BaseURL,
			"default_model": model,
			"has_api_key":   profile.APIKeyEnc != "",
			"checked":       false,
			"error":         fmt.Sprintf("build provider: %s", err),
		}
	}

	testCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	start := time.Now()
	resp, err := prov.Complete(testCtx, &types.ChatRequest{
		Model: model,
		Messages: []types.Message{
			{Role: "user", Content: "Say only the word: ok"},
		},
		MaxTokens: 10,
		Stream:    false,
	})
	duration := time.Since(start)

	if err != nil {
		return map[string]any{
			"ok":            false,
			"name":          profile.Name,
			"provider":      profile.Provider,
			"base_url":      profile.BaseURL,
			"default_model": model,
			"has_api_key":   profile.APIKeyEnc != "",
			"checked":       true,
			"error":         fmt.Sprintf("upstream error: %s", err),
			"duration_ms":   duration.Milliseconds(),
		}
	}

	ok := resp != nil && len(resp.Choices) > 0 && strings.TrimSpace(resp.Choices[0].Message.Content) != ""
	return map[string]any{
		"ok":            ok,
		"name":          profile.Name,
		"provider":      profile.Provider,
		"base_url":      profile.BaseURL,
		"default_model": model,
		"has_api_key":   profile.APIKeyEnc != "",
		"checked":       true,
		"duration_ms":   duration.Milliseconds(),
		"response":      firstNonEmpty(resp.Choices[0].Message.Content, ""),
	}
}

func isLocalProvider(name string) bool {
	return config.IsLocalLLMProvider(name)
}

// —── peer.llm_provider.activate ──────────────────────────────────────────────

type peerLLMActivateParams struct {
	Name string `json:"name"`
}

// rpcPeerLLMActivate sets the named provider profile as the default for this peer.
// The activation is stored as a preference so the runtime resolver can read it
// at request time.
func (h *Handler) rpcPeerLLMActivate(ctx context.Context, wsc *wsConn, req *rpcRequest) {
	var p peerLLMActivateParams
	if err := decodeParams(req.Params, &p); err != nil {
		wsc.replyErr(req.ID, errCodeInvalidParams, err.Error())
		return
	}
	name := strings.TrimSpace(p.Name)
	if name == "" {
		wsc.replyErr(req.ID, errCodeInvalidParams, "name is required")
		return
	}
	// Verify the profile exists before activating.
	if _, err := h.peerLLMStore.Get(ctx, wsc.peerID, name); err != nil {
		wsc.replyErr(req.ID, errCodeServer, fmt.Sprintf("provider profile %q not found: %s", name, err))
		return
	}
	// Persist activation via preference store.
	if h.preferenceStore == nil {
		// If preferences not available, activation is still accepted for
		// in-memory use even though it won't survive restart.
		wsc.reply(req.ID, map[string]any{
			"ok":             true,
			"active_profile": name,
			"persisted":      false,
			"message":        "preferences store not available; activation is session-only",
		})
		return
	}
	pref, err := h.preferenceStore.Set(ctx, wsc.peerID, preferencesInput("peer.llm.default_provider_profile", name))
	if err != nil {
		wsc.replyErr(req.ID, errCodeServer, fmt.Sprintf("activate: %s", err))
		return
	}
	_ = pref
	wsc.reply(req.ID, map[string]any{
		"ok":             true,
		"active_profile": name,
		"persisted":      true,
	})
}

// preferencesInput builds a minimal preferences.Input for peer defaults.
func preferencesInput(key, value string) preferences.Input {
	return preferences.Input{
		Key:        key,
		Value:      value,
		Scope:      "global",
		Confidence: 1.0,
		Provenance: "peer.llm_provider.activate",
	}
}
