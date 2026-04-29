// Package provider defines the Provider interface and its factory.
//
// Only one provider is active per gateway instance; the factory selects the
// correct implementation based on Config.Provider.
package provider

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/ffimnsr/koios/internal/config"
	"github.com/ffimnsr/koios/internal/types"
)

var errStreamIdleTimeout = errors.New("llm stream idle timeout")

type streamIdleTimeoutError struct {
	timeout time.Duration
}

func (e *streamIdleTimeoutError) Error() string {
	return fmt.Sprintf("llm stream idle timeout after %s", e.timeout)
}

func (e *streamIdleTimeoutError) Is(target error) bool {
	return target == errStreamIdleTimeout
}

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
			client:      client,
			apiKey:      cfg.APIKey,
			baseURL:     stripV1(base),
			model:       cfg.Model,
			idleTimeout: cfg.LLMIdleTimeout,
			hooks:       openAICompatibleHooks("openai"),
		}, nil

	case "openrouter":
		base := cfg.BaseURL
		if base == "" {
			base = "https://openrouter.ai/api"
		}
		// OpenRouter exposes an OpenAI-compatible endpoint, so re-use that impl.
		return &openAIProvider{
			client:      client,
			apiKey:      cfg.APIKey,
			baseURL:     stripV1(base),
			model:       cfg.Model,
			idleTimeout: cfg.LLMIdleTimeout,
			hooks:       openAICompatibleHooks("openrouter"),
		}, nil

	case "anthropic":
		base := cfg.BaseURL
		if base == "" {
			base = "https://api.anthropic.com"
		}
		return &anthropicProvider{
			client:      client,
			apiKey:      cfg.APIKey,
			baseURL:     stripV1(base),
			model:       cfg.Model,
			idleTimeout: cfg.LLMIdleTimeout,
			hooks:       anthropicHooks(),
		}, nil

	case "nvidia":
		// NVIDIA NIM exposes an OpenAI-compatible endpoint.
		base := cfg.BaseURL
		if base == "" {
			base = "https://integrate.api.nvidia.com/v1"
		}
		return &openAIProvider{
			client:      client,
			apiKey:      cfg.APIKey,
			baseURL:     stripV1(base),
			model:       cfg.Model,
			idleTimeout: cfg.LLMIdleTimeout,
			hooks:       openAICompatibleHooks("nvidia"),
		}, nil

	case "ollama":
		// Ollama exposes an OpenAI-compatible endpoint. No API key is required
		// for local deployments; an empty string is accepted.
		base := cfg.BaseURL
		if base == "" {
			base = "http://localhost:11434"
		}
		return &openAIProvider{
			client:      client,
			apiKey:      cfg.APIKey,
			baseURL:     stripV1(base),
			model:       cfg.Model,
			idleTimeout: cfg.LLMIdleTimeout,
			hooks:       openAICompatibleHooks("ollama"),
		}, nil

	case "vllm":
		// vLLM serves an OpenAI-compatible API. The default port matches vLLM's
		// out-of-the-box server; set base_url in config to override.
		base := cfg.BaseURL
		if base == "" {
			base = "http://localhost:8000"
		}
		return &openAIProvider{
			client:      client,
			apiKey:      cfg.APIKey,
			baseURL:     stripV1(base),
			model:       cfg.Model,
			idleTimeout: cfg.LLMIdleTimeout,
			hooks:       openAICompatibleHooks("vllm"),
		}, nil

	case "litellm":
		// LiteLLM proxy exposes an OpenAI-compatible surface over many backends.
		// Point base_url at your LiteLLM proxy instance.
		base := cfg.BaseURL
		if base == "" {
			base = "http://localhost:4000"
		}
		return &openAIProvider{
			client:      client,
			apiKey:      cfg.APIKey,
			baseURL:     stripV1(base),
			model:       cfg.Model,
			idleTimeout: cfg.LLMIdleTimeout,
			hooks:       openAICompatibleHooks("litellm"),
		}, nil

	case "gemini":
		// Google Gemini exposes an OpenAI-compatible endpoint under its
		// generativelanguage.googleapis.com domain. Use a Gemini API key.
		base := cfg.BaseURL
		if base == "" {
			base = "https://generativelanguage.googleapis.com/v1beta/openai"
		}
		return &openAIProvider{
			client:      client,
			apiKey:      cfg.APIKey,
			baseURL:     stripV1(base),
			model:       cfg.Model,
			idleTimeout: cfg.LLMIdleTimeout,
			hooks:       openAICompatibleHooks("gemini"),
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

func newStreamContext(parent context.Context) (context.Context, context.CancelCauseFunc) {
	return context.WithCancelCause(parent)
}

func startStreamIdleWatchdog(ctx context.Context, timeout time.Duration, cancel context.CancelCauseFunc) (func(), func()) {
	if timeout <= 0 {
		return func() {}, func() {}
	}
	resetCh := make(chan struct{}, 1)
	stopCh := make(chan struct{})
	var once sync.Once
	go func() {
		timer := time.NewTimer(timeout)
		defer timer.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-stopCh:
				return
			case <-resetCh:
				if !timer.Stop() {
					select {
					case <-timer.C:
					default:
					}
				}
				timer.Reset(timeout)
			case <-timer.C:
				cancel(&streamIdleTimeoutError{timeout: timeout})
				return
			}
		}
	}()
	return func() {
			select {
			case resetCh <- struct{}{}:
			default:
			}
		}, func() {
			once.Do(func() { close(stopCh) })
		}
}

func wrapStreamReadError(ctx context.Context, err error) error {
	if err == nil {
		return nil
	}
	if cause := context.Cause(ctx); cause != nil {
		return fmt.Errorf("reading stream: %w", cause)
	}
	return fmt.Errorf("reading stream: %w", err)
}
