package provider

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ffimnsr/koios/internal/types"
)

// TestOpenAICompleteStreamForwardsToolCallDeltas verifies the chat-completions
// stream sanitizer preserves delta.tool_calls — including split argument
// fragments — so streamed native tool calls reach the runtime's capture.
func TestOpenAICompleteStreamForwardsToolCallDeltas(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			t.Fatalf("request path = %q, want /v1/chat/completions", r.URL.Path)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"hola \"}}]}\n\n")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"call_1\",\"type\":\"function\",\"function\":{\"name\":\"time.now\",\"arguments\":\"{\\\"format\\\"\"}}]}}]}\n\n")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"function\":{\"arguments\":\":\\\"iso\\\"}\"}}]}}]}\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
		flusher.Flush()
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
	rec := httptest.NewRecorder()
	text, err := p.CompleteStream(context.Background(), &types.ChatRequest{Model: "gpt-4o"}, rec)
	if err != nil {
		t.Fatalf("CompleteStream: %v", err)
	}
	if text != "hola" {
		t.Fatalf("text = %q, want %q", text, "hola")
	}
	body := rec.Body.String()
	if !strings.Contains(body, `"tool_calls"`) {
		t.Fatalf("expected tool_calls to survive sanitization, got %q", body)
	}
	for _, want := range []string{`"call_1"`, "time.now", `{\"format\"`, `:\"iso\"`} {
		if !strings.Contains(body, want) {
			t.Fatalf("expected %q in forwarded chunk, got %q", want, body)
		}
	}
}

// TestAnthropicCompleteStreamForwardsToolUseBlocks verifies the Anthropic
// stream converts tool_use content blocks and input_json_delta fragments into
// OpenAI-style tool_calls chunks so the runtime's capture can execute them.
func TestAnthropicCompleteStreamForwardsToolUseBlocks(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)
		fmt.Fprint(w, "event: message_start\n")
		fmt.Fprint(w, "data: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_tool_1\"}}\n\n")
		fmt.Fprint(w, "event: content_block_start\n")
		fmt.Fprint(w, "data: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"tool_use\",\"id\":\"toolu_1\",\"name\":\"time_x2e_now\"}}\n\n")
		fmt.Fprint(w, "event: content_block_delta\n")
		fmt.Fprint(w, "data: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"input_json_delta\",\"partial_json\":\"{\\\"format\\\":\\\"iso\\\"}\"}}\n\n")
		fmt.Fprint(w, "event: message_delta\n")
		fmt.Fprint(w, "data: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"tool_use\"}}\n\n")
		fmt.Fprint(w, "event: message_stop\n")
		fmt.Fprint(w, "data: {\"type\":\"message_stop\"}\n\n")
		flusher.Flush()
	}))
	defer server.Close()

	p := &anthropicProvider{
		client:      server.Client(),
		apiKey:      "test-key",
		baseURL:     server.URL,
		model:       "claude-sonnet-5",
		idleTimeout: time.Second,
		hooks:       anthropicHooks(),
	}
	rec := httptest.NewRecorder()
	text, err := p.CompleteStream(context.Background(), &types.ChatRequest{Model: "claude-sonnet-5"}, rec)
	if err != nil {
		t.Fatalf("CompleteStream: %v", err)
	}
	if text != "" {
		t.Fatalf("text = %q, want empty (tool-only turn)", text)
	}
	body := rec.Body.String()
	if !strings.Contains(body, `"tool_calls"`) {
		t.Fatalf("expected tool_calls in forwarded Anthropic stream, got %q", body)
	}
	for _, want := range []string{`"toolu_1"`, "time_x2e_now", `{\"format\":\"iso\"}`} {
		if !strings.Contains(body, want) {
			t.Fatalf("expected %q in forwarded chunk, got %q", want, body)
		}
	}
}
