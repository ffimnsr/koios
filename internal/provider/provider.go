// Package provider defines the Provider interface and its factory.
//
// Only one provider is active per gateway instance; the factory selects the
// correct implementation based on Config.Provider.
package provider

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/ffimnsr/koios/internal/config"
	"github.com/ffimnsr/koios/internal/types"
)

type transportHooks struct {
	name         string
	capabilities types.ProviderCapabilities
	applyHeaders func(r *http.Request, apiKey string)
}

func openAICompatibleHooks(name string) transportHooks {
	return transportHooks{
		name: name,
		capabilities: types.ProviderCapabilities{
			Name:                 name,
			SupportsStreaming:    true,
			SupportsNativeTools:  true,
			OpenAICompatibleWire: true,
		},
		applyHeaders: func(r *http.Request, apiKey string) {
			r.Header.Set("Content-Type", "application/json")
			r.Header.Set("Authorization", "Bearer "+apiKey)
		},
	}
}

func anthropicHooks() transportHooks {
	return transportHooks{
		name: "anthropic",
		capabilities: types.ProviderCapabilities{
			Name:                "anthropic",
			SupportsStreaming:   true,
			SupportsNativeTools: true,
			RequiresMaxTokens:   true,
		},
		applyHeaders: func(r *http.Request, apiKey string) {
			r.Header.Set("Content-Type", "application/json")
			r.Header.Set("x-api-key", apiKey)
			r.Header.Set("anthropic-version", anthropicVersion)
		},
	}
}

// Provider sends chat completion requests to an LLM backend.
type Provider interface {
	// Complete sends a non-streaming request and returns the full response.
	Complete(ctx context.Context, req *types.ChatRequest) (*types.ChatResponse, error)

	// CompleteStream writes an OpenAI-compatible SSE stream to w.
	// It returns the complete assistant text once streaming finishes so the
	// caller can persist the turn in the session store.
	CompleteStream(ctx context.Context, req *types.ChatRequest, w http.ResponseWriter) (string, error)
}

// New creates the Provider described by cfg.
func New(cfg *config.Config) (Provider, error) {
	client := &http.Client{Timeout: cfg.RequestTimeout}

	switch cfg.Provider {
	case "openai":
		base := cfg.BaseURL
		if base == "" {
			base = "https://api.openai.com"
		}
		return &openAIProvider{
			client:  client,
			apiKey:  cfg.APIKey,
			baseURL: stripV1(base),
			model:   cfg.Model,
			hooks:   openAICompatibleHooks("openai"),
		}, nil

	case "openrouter":
		base := cfg.BaseURL
		if base == "" {
			base = "https://openrouter.ai/api"
		}
		// OpenRouter exposes an OpenAI-compatible endpoint, so re-use that impl.
		return &openAIProvider{
			client:  client,
			apiKey:  cfg.APIKey,
			baseURL: stripV1(base),
			model:   cfg.Model,
			hooks:   openAICompatibleHooks("openrouter"),
		}, nil

	case "anthropic":
		base := cfg.BaseURL
		if base == "" {
			base = "https://api.anthropic.com"
		}
		return &anthropicProvider{
			client:  client,
			apiKey:  cfg.APIKey,
			baseURL: stripV1(base),
			model:   cfg.Model,
			hooks:   anthropicHooks(),
		}, nil

	case "nvidia":
		// NVIDIA NIM exposes an OpenAI-compatible endpoint.
		base := cfg.BaseURL
		if base == "" {
			base = "https://integrate.api.nvidia.com/v1"
		}
		return &openAIProvider{
			client:  client,
			apiKey:  cfg.APIKey,
			baseURL: stripV1(base),
			model:   cfg.Model,
			hooks:   openAICompatibleHooks("nvidia"),
		}, nil

	default:
		return nil, fmt.Errorf("unknown provider: %s", cfg.Provider)
	}
}

// stripV1 removes a trailing "/v1" from base so that all providers store the
// root URL and openAIProvider can always append "/v1/chat/completions" safely.
func stripV1(base string) string {
	return strings.TrimSuffix(strings.TrimRight(base, "/"), "/v1")
}
