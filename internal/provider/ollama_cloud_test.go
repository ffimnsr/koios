package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ffimnsr/koios/internal/config"
	"github.com/ffimnsr/koios/internal/types"
)

func TestOllamaCloudCompleteUsesNativeChatAPI(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/chat" {
			t.Fatalf("request path = %q, want /api/chat", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer test-key" {
			t.Fatalf("Authorization = %q, want Bearer token", got)
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if body["model"] != "gpt-oss:120b" || body["stream"] != false {
			t.Fatalf("unexpected request: %#v", body)
		}
		_, _ = w.Write([]byte(`{"model":"gpt-oss:120b","message":{"role":"assistant","content":"cloud answer"},"done":true,"prompt_eval_count":4,"eval_count":3}`))
	}))
	defer server.Close()

	provider, err := New(&config.Config{
		Provider:       "ollama-cloud",
		APIKey:         "test-key",
		BaseURL:        server.URL,
		Model:          "gpt-oss:120b",
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
	if response.Choices[0].Message.Content != "cloud answer" || response.Usage.TotalTokens != 7 {
		t.Fatalf("unexpected response: %#v", response)
	}
}

func TestOllamaCloudCompleteStreamConvertsJSONLToSSE(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/chat" {
			t.Fatalf("request path = %q, want /api/chat", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/x-ndjson")
		_, _ = w.Write([]byte("{\"model\":\"gpt-oss:120b\",\"message\":{\"role\":\"assistant\",\"content\":\"hello \"},\"done\":false}\n"))
		_, _ = w.Write([]byte("{\"model\":\"gpt-oss:120b\",\"message\":{\"role\":\"assistant\",\"content\":\"world\"},\"done\":true}\n"))
	}))
	defer server.Close()

	provider, err := New(&config.Config{
		Provider:       "ollama-cloud",
		APIKey:         "test-key",
		BaseURL:        server.URL,
		Model:          "gpt-oss:120b",
		RequestTimeout: time.Second,
		LLMIdleTimeout: time.Second,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	recorder := httptest.NewRecorder()
	text, err := provider.CompleteStream(context.Background(), &types.ChatRequest{Messages: []types.Message{{Role: "user", Content: "hello"}}}, recorder)
	if err != nil {
		t.Fatalf("CompleteStream: %v", err)
	}
	if text != "hello world" {
		t.Fatalf("stream text = %q, want hello world", text)
	}
	if body := recorder.Body.String(); !strings.Contains(body, "hello ") || !strings.Contains(body, "world") || !strings.Contains(body, "data: [DONE]") {
		t.Fatalf("unexpected SSE body: %q", body)
	}
}

func TestOllamaCloudCatalogUsesTagsEndpoint(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/tags" {
			t.Fatalf("request path = %q, want /api/tags", r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"models": []map[string]string{{"name": "minimax-m3"}, {"name": "gpt-oss:120b"}},
		})
	}))
	defer server.Close()

	provider, err := New(&config.Config{
		Provider:       "ollama-cloud",
		APIKey:         "test-key",
		BaseURL:        server.URL,
		Model:          "gpt-oss:120b",
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
	if catalog.Endpoint != "/api/tags" || catalog.Inspection.Interface != "native_ollama" || catalog.Count != 2 {
		t.Fatalf("unexpected catalog: %#v", catalog)
	}
	if catalog.Models[0].ID != "gpt-oss:120b" {
		t.Fatalf("first model = %q, want sorted catalog", catalog.Models[0].ID)
	}
}
