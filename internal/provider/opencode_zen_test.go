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

func TestOpenCodeZenDeepSeekReplaysReasoningContent(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.URL.Path != "/v1/chat/completions" {
			t.Fatalf("request path = %q, want /v1/chat/completions", r.URL.Path)
		}

		if requests == 2 {
			var body struct {
				Messages []struct {
					Role             string `json:"role"`
					ReasoningContent string `json:"reasoning_content"`
				} `json:"messages"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatalf("decode request: %v", err)
			}
			if len(body.Messages) != 3 {
				t.Fatalf("message count = %d, want 3", len(body.Messages))
			}
			if body.Messages[1].Role != "assistant" {
				t.Fatalf("replayed role = %q, want assistant", body.Messages[1].Role)
			}
			if body.Messages[1].ReasoningContent != "inspect current state" {
				t.Fatalf("replayed reasoning_content = %q", body.Messages[1].ReasoningContent)
			}
		}

		w.Header().Set("Content-Type", "application/json")
		if requests == 1 {
			_, _ = w.Write([]byte(`{"id":"chat_1","choices":[{"message":{"role":"assistant","content":"I will inspect it.","reasoning_content":"inspect current state"}}]}`))
			return
		}
		_, _ = w.Write([]byte(`{"id":"chat_2","choices":[{"message":{"role":"assistant","content":"done"}}]}`))
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

	first, err := provider.Complete(context.Background(), &types.ChatRequest{
		Messages: []types.Message{{Role: "user", Content: "inspect it"}},
	})
	if err != nil {
		t.Fatalf("first Complete: %v", err)
	}
	if got := first.Choices[0].Message.ReasoningContent; got != "inspect current state" {
		t.Fatalf("captured reasoning_content = %q", got)
	}

	_, err = provider.Complete(context.Background(), &types.ChatRequest{
		Messages: []types.Message{
			{Role: "user", Content: "inspect it"},
			first.Choices[0].Message,
			{Role: "user", Content: "continue"},
		},
	})
	if err != nil {
		t.Fatalf("second Complete: %v", err)
	}
	if requests != 2 {
		t.Fatalf("request count = %d, want 2", requests)
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
