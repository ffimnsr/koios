package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ffimnsr/koios/internal/agent"
	"github.com/ffimnsr/koios/internal/session"
	"github.com/ffimnsr/koios/internal/toolresults"
)

func TestProviderModelsPreviewUsesTemporaryProfileAndRedactsAPIKey(t *testing.T) {
	const apiKey = "preview-secret-key"

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			t.Fatalf("request path = %q, want /v1/models", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer "+apiKey {
			t.Fatalf("Authorization = %q, want temporary preview credential", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]any{{"id": "preview-model", "owned_by": "test"}},
		})
	}))
	defer server.Close()

	resultStore, err := toolresults.New(t.TempDir() + "/tool_results.db")
	if err != nil {
		t.Fatalf("toolresults.New: %v", err)
	}
	defer resultStore.Close()

	h := NewHandler(session.New(10), noopProvider{}, HandlerOptions{
		Model:           "test-model",
		Timeout:         time.Second,
		ToolPolicy:      ToolPolicy{Profile: "coding"},
		ToolResultStore: resultStore,
	})

	result, err := h.ExecuteTool(context.Background(), "alice", agent.ToolCall{
		Name:      "provider.models_preview",
		Arguments: json.RawMessage(`{"provider":"openai","api_key":"preview-secret-key","base_url":"` + server.URL + `"}`),
	})
	if err != nil {
		t.Fatalf("ExecuteTool(provider.models_preview): %v", err)
	}

	raw, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("marshal preview result: %v", err)
	}
	if strings.Contains(string(raw), apiKey) {
		t.Fatalf("preview response exposed API key: %s", raw)
	}
	for _, expected := range []string{
		`"provider":"openai"`,
		`"scope":"preview"`,
		`"default_model":"catalog-preview"`,
		`"id":"preview-model"`,
		`"catalog":"provider_owned"`,
	} {
		if !strings.Contains(string(raw), expected) {
			t.Fatalf("preview result missing %s: %s", expected, raw)
		}
	}

	records, err := resultStore.List(context.Background(), "alice", toolresults.Filter{Limit: 10})
	if err != nil {
		t.Fatalf("tool result list: %v", err)
	}
	if len(records) != 1 {
		t.Fatalf("tool result records = %d, want 1", len(records))
	}
	if strings.Contains(records[0].ArgsJSON, apiKey) || strings.Contains(records[0].ResultJSON, apiKey) {
		t.Fatalf("persisted preview tool result exposed API key: %#v", records[0])
	}
}

func TestProviderModelsPreviewRejectsInvalidArguments(t *testing.T) {
	h := NewHandler(session.New(10), noopProvider{}, HandlerOptions{
		Model:      "test-model",
		ToolPolicy: ToolPolicy{Profile: "coding"},
	})

	for _, arguments := range []json.RawMessage{
		json.RawMessage(`{}`),
		json.RawMessage(`{"provider":"openai","unexpected":true}`),
		json.RawMessage(`{"provider":"openai","api_key":null}`),
	} {
		_, err := h.ExecuteTool(context.Background(), "alice", agent.ToolCall{
			Name:      "provider.models_preview",
			Arguments: arguments,
		})
		if err == nil {
			t.Fatalf("ExecuteTool(provider.models_preview, %s) succeeded, want validation error", arguments)
		}
		if !strings.Contains(err.Error(), "invalid arguments") {
			t.Fatalf("error = %q, want invalid arguments", err)
		}
	}
}
