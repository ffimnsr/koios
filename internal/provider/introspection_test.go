package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/ffimnsr/koios/internal/config"
)

func TestOpenAIListModels(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			t.Fatalf("request path = %q, want /v1/models", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]any{
				{"id": "z-model", "owned_by": "vendor-z"},
				{"id": "a-model", "owned_by": "vendor-a"},
			},
		})
	}))
	defer server.Close()

	p := &openAIProvider{
		client:      server.Client(),
		apiKey:      "test-key",
		baseURL:     server.URL,
		model:       "gpt-4o",
		idleTimeout: time.Second,
		hooks:       openAICompatibleHooks("openai"),
	}

	catalog, err := p.ListModels(context.Background())
	if err != nil {
		t.Fatalf("ListModels: %v", err)
	}
	if catalog.Count != 2 {
		t.Fatalf("catalog.Count = %d, want 2", catalog.Count)
	}
	if got := catalog.Models[0].ID; got != "a-model" {
		t.Fatalf("first model = %q, want a-model", got)
	}
	if catalog.Endpoint != "/v1/models" {
		t.Fatalf("catalog.Endpoint = %q, want /v1/models", catalog.Endpoint)
	}
	if catalog.Inspection.Interface != "native_openai" {
		t.Fatalf("catalog.Inspection.Interface = %q, want native_openai", catalog.Inspection.Interface)
	}
}

func TestProviderNewOpenAICompatibleIntrospection(t *testing.T) {
	providers := []string{"openai", "openrouter", "openai-compatible", "opencode-go", "nvidia", "ollama", "vllm", "litellm", "gemini"}

	for _, providerName := range providers {
		providerName := providerName
		t.Run(providerName, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/v1/models", "/models":
					if providerName == "gemini" && r.URL.Path != "/models" {
						t.Fatalf("request path = %q, want /models for gemini compatibility surface", r.URL.Path)
					}
					if providerName != "gemini" && r.URL.Path != "/v1/models" {
						t.Fatalf("request path = %q, want /v1/models", r.URL.Path)
					}
					w.Header().Set("Content-Type", "application/json")
					_ = json.NewEncoder(w).Encode(map[string]any{
						"data": []map[string]any{
							{"id": providerName + "-b", "owned_by": providerName},
							{"id": providerName + "-a", "owned_by": providerName},
						},
					})
				case "/v1/auth/key":
					if providerName != "openrouter" {
						t.Fatalf("unexpected usage request for provider %q", providerName)
					}
					w.Header().Set("Content-Type", "application/json")
					_ = json.NewEncoder(w).Encode(map[string]any{
						"data": map[string]any{
							"label":           "team-key",
							"usage":           1.5,
							"limit":           5.0,
							"limit_remaining": 3.5,
						},
					})
				default:
					t.Fatalf("unexpected request path %q", r.URL.Path)
				}
			}))
			defer server.Close()

			p, err := New(&config.Config{
				Provider:       providerName,
				APIKey:         "test-key",
				BaseURL:        server.URL,
				Model:          providerName + "-default",
				RequestTimeout: time.Second,
				LLMIdleTimeout: time.Second,
			})
			if err != nil {
				t.Fatalf("New: %v", err)
			}

			catalogProvider, ok := p.(ModelCatalogProvider)
			if !ok {
				t.Fatalf("provider %q does not implement ModelCatalogProvider", providerName)
			}
			catalog, err := catalogProvider.ListModels(context.Background())
			if err != nil {
				t.Fatalf("ListModels: %v", err)
			}
			if catalog.Provider != providerName {
				t.Fatalf("catalog.Provider = %q, want %q", catalog.Provider, providerName)
			}
			if catalog.Count != 2 {
				t.Fatalf("catalog.Count = %d, want 2", catalog.Count)
			}
			if got := catalog.Models[0].ID; got != providerName+"-a" {
				t.Fatalf("first model = %q, want %q", got, providerName+"-a")
			}
			switch providerName {
			case "openai":
				if catalog.Inspection.Interface != "native_openai" || catalog.Endpoint != "/v1/models" {
					t.Fatalf("unexpected openai catalog inspection: %#v", catalog)
				}
			case "openrouter":
				if catalog.Inspection.CatalogMode != "normalized_openai_compatible_catalog" {
					t.Fatalf("unexpected openrouter catalog inspection: %#v", catalog.Inspection)
				}
			case "openai-compatible":
				if catalog.Inspection.Family != "openai_compatible_generic" || catalog.Inspection.Interface != "openai_compatible" || catalog.Endpoint != "/v1/models" {
					t.Fatalf("unexpected generic openai-compatible catalog inspection: %#v", catalog)
				}
			case "opencode-go":
				if catalog.Inspection.Family != "opencode_go" || catalog.Inspection.Interface != "openai_compatible" || catalog.Endpoint != "/v1/models" {
					t.Fatalf("unexpected opencode-go catalog inspection: %#v", catalog)
				}
			case "nvidia", "ollama", "vllm", "litellm":
				if catalog.Inspection.Interface != "openai_compatible" || catalog.Endpoint != "/v1/models" {
					t.Fatalf("unexpected openai-compatible catalog inspection for %q: %#v", providerName, catalog)
				}
			case "gemini":
				if catalog.Endpoint != "/models" || catalog.Inspection.CatalogEndpoint != "/models" {
					t.Fatalf("unexpected gemini catalog inspection: %#v", catalog)
				}
			}

			usageProvider, ok := p.(UsageStatusProvider)
			if !ok {
				t.Fatalf("provider %q does not implement UsageStatusProvider", providerName)
			}
			status, err := usageProvider.UsageStatus(context.Background())
			if err != nil {
				t.Fatalf("UsageStatus: %v", err)
			}
			if providerName == "openrouter" {
				if status.Status != "ok" || status.Remaining != 3.5 {
					t.Fatalf("openrouter usage status = %#v, want ok response", status)
				}
				if status.Endpoint != "/v1/auth/key" || status.Inspection.UsageEndpoint != "/v1/auth/key" {
					t.Fatalf("unexpected openrouter usage inspection: %#v", status)
				}
				return
			}
			if status.Status != "unsupported" {
				t.Fatalf("status.Status = %q, want unsupported", status.Status)
			}
			if status.Inspection.SupportsUsage {
				t.Fatalf("status.Inspection.SupportsUsage = true for unsupported provider %q", providerName)
			}
		})
	}
}

func TestNewGenericOpenAICompatibleRequiresBaseURL(t *testing.T) {
	_, err := New(&config.Config{
		Provider:       "openai-compatible",
		APIKey:         "test-key",
		Model:          "compat-model",
		RequestTimeout: time.Second,
		LLMIdleTimeout: time.Second,
	})
	if err == nil || err.Error() != `provider "openai-compatible" requires base_url` {
		t.Fatalf("expected base_url requirement error, got %v", err)
	}
}

func TestAnthropicListModels(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			t.Fatalf("request path = %q, want /v1/models", r.URL.Path)
		}
		if got := r.Header.Get("anthropic-version"); got != anthropicVersion {
			t.Fatalf("anthropic-version = %q, want %q", got, anthropicVersion)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]any{{"id": "claude-sonnet-4-0", "owned_by": "anthropic"}},
		})
	}))
	defer server.Close()

	p := &anthropicProvider{
		client:      server.Client(),
		apiKey:      "test-key",
		baseURL:     server.URL,
		model:       "claude-sonnet-4-0",
		idleTimeout: time.Second,
		hooks:       anthropicHooks(),
	}

	catalog, err := p.ListModels(context.Background())
	if err != nil {
		t.Fatalf("ListModels: %v", err)
	}
	if catalog.Count != 1 || catalog.Models[0].ID != "claude-sonnet-4-0" {
		t.Fatalf("unexpected catalog: %#v", catalog)
	}
	if catalog.Inspection.Interface != "native_anthropic" || catalog.Endpoint != "/v1/models" {
		t.Fatalf("unexpected anthropic inspection: %#v", catalog)
	}
}

func TestOpenRouterUsageStatus(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/auth/key" {
			t.Fatalf("request path = %q, want /v1/auth/key", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": map[string]any{
				"label":           "team-key",
				"usage":           10.5,
				"limit":           25.0,
				"limit_remaining": 14.5,
				"is_free_tier":    false,
				"rate_limit": map[string]any{
					"requests": 60,
					"interval": "1m",
				},
			},
		})
	}))
	defer server.Close()

	p := &openAIProvider{
		client:      server.Client(),
		apiKey:      "test-key",
		baseURL:     server.URL,
		model:       "openrouter/auto",
		idleTimeout: time.Second,
		hooks:       openAICompatibleHooks("openrouter"),
	}

	status, err := p.UsageStatus(context.Background())
	if err != nil {
		t.Fatalf("UsageStatus: %v", err)
	}
	if status.Status != "ok" || status.Remaining != 14.5 || status.Requests != 60 {
		t.Fatalf("unexpected usage status: %#v", status)
	}
	if status.Inspection.UsageMode != "provider_key_metadata" || status.Inspection.UsageEndpoint != "/v1/auth/key" {
		t.Fatalf("unexpected openrouter usage inspection: %#v", status)
	}
}

func TestOpenAIUsageStatusUnsupported(t *testing.T) {
	p := &openAIProvider{baseURL: "https://api.openai.com", hooks: openAICompatibleHooks("openai")}
	status, err := p.UsageStatus(context.Background())
	if err != nil {
		t.Fatalf("UsageStatus: %v", err)
	}
	if status.Status != "unsupported" {
		t.Fatalf("status = %q, want unsupported", status.Status)
	}
	if status.Inspection.Interface != "native_openai" || status.Inspection.SupportsUsage {
		t.Fatalf("unexpected openai unsupported inspection: %#v", status)
	}
}
