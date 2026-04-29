package provider

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/ffimnsr/koios/internal/config"
	"github.com/ffimnsr/koios/internal/types"
)

type capabilityProvider interface {
	Capabilities(model string) types.ProviderCapabilities
}

// modelEntry is one entry in the routing provider's registry.
type modelEntry struct {
	model string
	prov  Provider
}

// RoutingProvider dispatches requests to the appropriate backend based on
// routing rules:
//   - If the request's Model field matches a registered profile (by model ID
//     or profile name), that profile's provider is used.
//   - If a lightweight model is configured and the request looks simple, it
//     routes to the lightweight model on the primary provider.
//   - On failure, the fallback chain is tried in order.
//   - Unknown model names fall back to the primary provider.
type RoutingProvider struct {
	primary          modelEntry            // default/primary
	lightweight      *modelEntry           // optional lightweight model (same provider as primary)
	fallbacks        []modelEntry          // ordered fallback chain (same provider as primary unless overridden via profile)
	profiles         map[string]modelEntry // named profile overrides
	lightweightWords int                   // word threshold for lightweight routing (0 = disabled)
}

// RoutingConfig describes how to build a RoutingProvider.
type RoutingConfig struct {
	// Primary is the default model entry.
	Primary modelEntry
	// Lightweight is the optional lightweight model entry.
	Lightweight *modelEntry
	// Fallbacks is the ordered list of fallback models.
	Fallbacks []modelEntry
	// Profiles maps profile/model names to entries for per-session overrides.
	Profiles map[string]modelEntry
	// LightweightWords is the word count threshold below which the lightweight
	// model is selected (0 disables lightweight routing).
	LightweightWords int
}

// NewRoutingProvider creates a RoutingProvider from the given config.
func NewRoutingProvider(rc RoutingConfig) *RoutingProvider {
	rp := &RoutingProvider{
		primary:          rc.Primary,
		lightweight:      rc.Lightweight,
		fallbacks:        rc.Fallbacks,
		profiles:         rc.Profiles,
		lightweightWords: rc.LightweightWords,
	}
	if rp.profiles == nil {
		rp.profiles = make(map[string]modelEntry)
	}
	return rp
}

// ForModel returns the provider and model string to use for the given model
// name. If name is empty or not found in profiles, the primary is returned.
func (rp *RoutingProvider) ForModel(name string) (Provider, string) {
	name = strings.TrimSpace(name)
	if name == "" {
		return rp.primary.prov, rp.primary.model
	}
	// Exact profile lookup (by profile name or model string).
	if e, ok := rp.profiles[name]; ok {
		return e.prov, e.model
	}
	// Fall back to primary using the explicitly requested model name, which
	// lets per-session overrides specify a raw model ID understood by the
	// primary provider without needing a full profile entry.
	return rp.primary.prov, name
}

// PrimaryModel returns the primary model name.
func (rp *RoutingProvider) PrimaryModel() string { return rp.primary.model }

func (rp *RoutingProvider) Capabilities(model string) types.ProviderCapabilities {
	prov, resolvedModel := rp.ForModel(model)
	if caps, ok := prov.(capabilityProvider); ok {
		return caps.Capabilities(resolvedModel)
	}
	return types.ProviderCapabilities{}
}

// ProfileNames returns the list of registered profile names.
func (rp *RoutingProvider) ProfileNames() []string {
	names := make([]string, 0, len(rp.profiles))
	for n := range rp.profiles {
		names = append(names, n)
	}
	return names
}

// Complete dispatches the request, applying routing and failover logic.
func (rp *RoutingProvider) Complete(ctx context.Context, req *types.ChatRequest) (*types.ChatResponse, error) {
	chain := rp.buildChain(req)
	var lastErr error
	for _, entry := range chain {
		r := cloneRequest(req)
		r.Model = entry.model
		resp, err := entry.prov.Complete(ctx, r)
		if err == nil {
			return resp, nil
		}
		lastErr = err
		if !isRetryableProviderErr(err) {
			break
		}
	}
	return nil, lastErr
}

// CompleteStream dispatches the streaming request, applying routing and failover.
// Streaming fallback tries non-streaming on subsequent attempts to avoid partial
// output being emitted before the failure is detected.
func (rp *RoutingProvider) CompleteStream(ctx context.Context, req *types.ChatRequest, w http.ResponseWriter) (string, error) {
	chain := rp.buildChain(req)
	if len(chain) == 0 {
		return "", fmt.Errorf("no model available")
	}

	// First attempt: stream with the chosen model.
	first := chain[0]
	if !providerSupportsStreaming(first.prov, first.model) {
		resp, err := first.prov.Complete(ctx, cloneRequestWithModel(req, first.model))
		if err != nil {
			return "", err
		}
		if len(resp.Choices) == 0 {
			return "", fmt.Errorf("empty response from provider")
		}
		text := resp.Choices[0].Message.Content
		setSSEHeaders(w)
		flusher, _ := w.(http.Flusher)
		fmt.Fprintf(w, "data: [DONE]\n\n")
		if flusher != nil {
			flusher.Flush()
		}
		return text, nil
	}
	r := cloneRequest(req)
	r.Model = first.model
	text, err := first.prov.CompleteStream(ctx, r, w)
	if err == nil {
		return text, nil
	}
	if !isRetryableProviderErr(err) || len(chain) == 1 {
		return "", err
	}

	// Fallback attempts: non-streaming to avoid writing partial SSE output.
	for _, entry := range chain[1:] {
		r := cloneRequest(req)
		r.Model = entry.model
		resp, ferr := entry.prov.Complete(ctx, r)
		if ferr == nil {
			if len(resp.Choices) == 0 {
				continue
			}
			text = resp.Choices[0].Message.Content
			// Emit a minimal SSE response so the caller receives the text.
			setSSEHeaders(w)
			flusher, _ := w.(http.Flusher)
			fmt.Fprintf(w, "data: [DONE]\n\n")
			if flusher != nil {
				flusher.Flush()
			}
			return text, nil
		}
		if !isRetryableProviderErr(ferr) {
			break
		}
	}
	return "", err // return original streaming error
}

// buildChain returns the ordered list of (provider, model) to try for req.
func (rp *RoutingProvider) buildChain(req *types.ChatRequest) []modelEntry {
	// If the caller set a specific model, honour it as the primary entry;
	// fallbacks still apply on failure.
	requested := strings.TrimSpace(req.Model)
	var first modelEntry
	if requested != "" {
		p, m := rp.ForModel(requested)
		first = modelEntry{model: m, prov: p}
	} else if rp.lightweight != nil && rp.lightweightWords > 0 && isSimpleRequest(req, rp.lightweightWords) {
		first = *rp.lightweight
	} else {
		first = rp.primary
	}

	chain := []modelEntry{first}
	chain = append(chain, rp.fallbacks...)
	return chain
}

// isSimpleRequest returns true when the last user message is below the word
// threshold, signalling a "lightweight" query that doesn't need a powerful model.
func isSimpleRequest(req *types.ChatRequest, threshold int) bool {
	// Walk backwards to find the last user message.
	for i := len(req.Messages) - 1; i >= 0; i-- {
		m := req.Messages[i]
		if m.Role != "user" {
			continue
		}
		text := strings.TrimSpace(m.Content)
		if text == "" {
			return false
		}
		words := len(strings.Fields(text))
		return words <= threshold
	}
	return false
}

// isRetryableProviderErr returns true for transient upstream errors that may
// succeed with a different model/endpoint.
func isRetryableProviderErr(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	transient := []string{"429", "500", "502", "503", "504", "too many requests", "rate limit", "overloaded", "timeout", "unavailable"}
	for _, t := range transient {
		if strings.Contains(msg, t) {
			return true
		}
	}
	return false
}

// cloneRequest returns a shallow copy of req so that each attempt has its own
// Model field without mutating the caller's request.
func cloneRequest(req *types.ChatRequest) *types.ChatRequest {
	cp := *req
	return &cp
}

func cloneRequestWithModel(req *types.ChatRequest, model string) *types.ChatRequest {
	cp := cloneRequest(req)
	cp.Model = model
	return cp
}

func providerSupportsStreaming(prov Provider, model string) bool {
	if caps, ok := prov.(capabilityProvider); ok {
		pc := caps.Capabilities(model)
		if pc.SupportsStreaming {
			return true
		}
		if pc.Name != "" {
			return false
		}
	}
	return true
}

// BuildRoutingProvider constructs a RoutingProvider from application config.
// It builds the primary provider, optional lightweight and fallback entries
// (reusing the primary provider for same-provider models), and named profiles.
func BuildRoutingProvider(cfg *config.Config) (*RoutingProvider, error) {
	primary, err := New(cfg)
	if err != nil {
		return nil, err
	}
	primaryEntry := modelEntry{model: cfg.Model, prov: primary}

	// Lightweight model reuses the same provider as the primary.
	var lightweight *modelEntry
	if lw := strings.TrimSpace(cfg.LightweightModel); lw != "" && lw != cfg.Model {
		lightweight = &modelEntry{model: lw, prov: primary}
	}

	// Fallback chain: resolved against named profiles first, otherwise use
	// the primary provider with the given model string.
	profileMap := buildProfileMap(cfg)
	var fallbacks []modelEntry
	for _, name := range cfg.FallbackModels {
		name = strings.TrimSpace(name)
		if name == "" || name == cfg.Model {
			continue
		}
		if e, ok := profileMap[name]; ok {
			fallbacks = append(fallbacks, e)
		} else {
			// Same provider, different model string.
			fallbacks = append(fallbacks, modelEntry{model: name, prov: primary})
		}
	}

	return NewRoutingProvider(RoutingConfig{
		Primary:          primaryEntry,
		Lightweight:      lightweight,
		Fallbacks:        fallbacks,
		Profiles:         profileMap,
		LightweightWords: 15, // queries ≤ 15 words route to the lightweight model
	}), nil
}

// buildProfileMap converts ModelProfiles from config into a map keyed by
// profile name and also by the model string for convenience.
func buildProfileMap(cfg *config.Config) map[string]modelEntry {
	m := make(map[string]modelEntry)
	for _, p := range cfg.ModelProfiles {
		profileCfg := &config.Config{
			Provider:       p.Provider,
			APIKey:         p.APIKey,
			BaseURL:        p.BaseURL,
			Model:          p.Model,
			LLMIdleTimeout: cfg.LLMIdleTimeout,
			RequestTimeout: cfg.RequestTimeout,
		}
		if profileCfg.Provider == "" {
			profileCfg.Provider = cfg.Provider
		}
		if profileCfg.APIKey == "" {
			profileCfg.APIKey = cfg.APIKey
		}
		if profileCfg.BaseURL == "" {
			profileCfg.BaseURL = cfg.BaseURL
		}
		prov, err := New(profileCfg)
		if err != nil {
			continue
		}
		entry := modelEntry{model: p.Model, prov: prov}
		if p.Name != "" {
			m[p.Name] = entry
		}
		if p.Model != "" {
			m[p.Model] = entry
		}
	}
	return m
}
