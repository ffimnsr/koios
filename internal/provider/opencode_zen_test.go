package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/ffimnsr/koios/internal/config"
	"github.com/ffimnsr/koios/internal/types"
)

func TestOpenCodeZenRoutesModelsByProtocol(t *testing.T) {
	tests := []struct {
		name       string
		model      string
		path       string
		headerName string
		response   string
	}{
		{
			name:       "chat completions",
			model:      "deepseek-v4-flash-free",
			path:       "/v1/chat/completions",
			headerName: "Authorization",
			response:   `{"id":"chat_1","choices":[{"message":{"role":"assistant","content":"chat"}}]}`,
		},
		{
			name:       "responses",
			model:      "gpt-5.6-terra",
			path:       "/v1/responses",
			headerName: "Authorization",
			response:   `{"id":"resp_1","object":"response","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"responses"}]}],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}`,
		},
		{
			name:       "anthropic messages",
			model:      "claude-sonnet-4-5",
			path:       "/v1/messages",
			headerName: "x-api-key",
			response:   `{"id":"msg_1","type":"message","role":"assistant","content":[{"type":"text","text":"messages"}],"usage":{"input_tokens":1,"output_tokens":1}}`,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != tc.path {
					t.Fatalf("request path = %q, want %q", r.URL.Path, tc.path)
				}
				if got := r.Header.Get(tc.headerName); got != "test-key" && got != "Bearer test-key" {
					t.Fatalf("%s = %q, want authenticated request", tc.headerName, got)
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(tc.response))
			}))
			defer server.Close()

			provider, err := New(&config.Config{
				Provider:       "opencode-zen",
				APIKey:         "test-key",
				BaseURL:        server.URL,
				Model:          tc.model,
				RequestTimeout: time.Second,
				LLMIdleTimeout: time.Second,
			})
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			response, err := provider.Complete(context.Background(), &types.ChatRequest{Messages: []types.Message{{Role: "user", Content: "hello"}}})
			if err != nil {
				t.Fatalf("Complete: %v", err)
			}
			if len(response.Choices) != 1 || response.Choices[0].Message.Content == "" {
				t.Fatalf("unexpected response: %#v", response)
			}
		})
	}
}

func TestOpenCodeZenRejectsGoogleAndUnknownModels(t *testing.T) {
	provider, err := New(&config.Config{
		Provider:       "opencode-zen",
		Model:          "gemini-3.5-flash",
		RequestTimeout: time.Second,
		LLMIdleTimeout: time.Second,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := provider.Complete(context.Background(), &types.ChatRequest{}); err == nil {
		t.Fatal("expected Google-native model error")
	}
	if _, err := provider.Complete(context.Background(), &types.ChatRequest{Model: "unknown-model"}); err == nil {
		t.Fatal("expected unknown model error")
	}
}

func TestOpenCodeZenCatalogOmitsGoogleModels(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			t.Fatalf("request path = %q, want /v1/models", r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]string{
				{"id": "gemini-3.5-flash"},
				{"id": "deepseek-v4-flash-free"},
				{"id": "gpt-5.6-terra"},
			},
		})
	}))
	defer server.Close()

	provider, err := New(&config.Config{
		Provider:       "opencode-zen",
		APIKey:         "test-key",
		BaseURL:        server.URL,
		Model:          "deepseek-v4-flash-free",
		RequestTimeout: time.Second,
		LLMIdleTimeout: time.Second,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	catalog, err := provider.(ModelCatalogProvider).ListModels(context.Background())
	if err != nil {
		t.Fatalf("ListModels: %v", err)
	}
	if catalog.Count != 2 || catalog.Models[0].ID != "deepseek-v4-flash-free" || catalog.Models[1].ID != "gpt-5.6-terra" {
		t.Fatalf("unexpected catalog models: %#v", catalog.Models)
	}
	if catalog.Inspection.Interface != "multi_protocol" {
		t.Fatalf("inspection = %#v", catalog.Inspection)
	}
}
