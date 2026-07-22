package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ffimnsr/koios/internal/types"
)

func TestOpenAICompleteStreamIdleTimeout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"hello\"}}]}\n\n")
		flusher.Flush()
		<-r.Context().Done()
	}))
	defer server.Close()

	p := &openAIProvider{
		client:      server.Client(),
		apiKey:      "test-key",
		baseURL:     server.URL,
		model:       "gpt-4o",
		idleTimeout: 50 * time.Millisecond,
		hooks:       openAICompatibleHooks("openai"),
	}

	rec := httptest.NewRecorder()
	text, err := p.CompleteStream(context.Background(), &types.ChatRequest{}, rec)
	if text != "hello" {
		t.Fatalf("text = %q", text)
	}
	if err == nil || !errors.Is(err, errStreamIdleTimeout) {
		t.Fatalf("expected idle-timeout error, got %v", err)
	}
	if !strings.Contains(err.Error(), "50ms") {
		t.Fatalf("expected timeout duration in error, got %v", err)
	}
	if !strings.Contains(rec.Body.String(), "hello") {
		t.Fatalf("expected streamed content to be forwarded, got %q", rec.Body.String())
	}
}

func TestOpenAICompleteEmitsReasoningSummary(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"id":"resp_1","object":"chat.completion","created":1,"choices":[{"index":0,"message":{"role":"assistant","content":"done","reasoning_content":"inspect markets before answer"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`)
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

	var events []types.ReasoningEvent
	ctx := types.WithReasoningSink(context.Background(), func(ev types.ReasoningEvent) {
		events = append(events, ev)
	})
	resp, err := p.Complete(ctx, &types.ChatRequest{ReasoningVisibility: "summary"})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if len(resp.Reasoning) != 1 || resp.Reasoning[0].Text != "inspect markets before answer" {
		t.Fatalf("unexpected reasoning blocks: %#v", resp.Reasoning)
	}
	if len(events) != 1 || events[0].Kind != types.ReasoningEventSummary {
		t.Fatalf("unexpected reasoning events: %#v", events)
	}
}

func TestOpenAICompleteStreamEmitsReasoningDeltaAndSummary(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"reasoning_content\":\"think 1\"}}]}\n\n")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"hello\"}}]}\n\n")
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

	var events []types.ReasoningEvent
	ctx := types.WithReasoningSink(context.Background(), func(ev types.ReasoningEvent) {
		events = append(events, ev)
	})
	rec := httptest.NewRecorder()
	text, err := p.CompleteStream(ctx, &types.ChatRequest{ReasoningVisibility: "full"}, rec)
	if err != nil {
		t.Fatalf("CompleteStream: %v", err)
	}
	if text != "hello" {
		t.Fatalf("text = %q, want hello", text)
	}
	if len(events) != 2 || events[0].Kind != types.ReasoningEventDelta || events[1].Kind != types.ReasoningEventSummary {
		t.Fatalf("unexpected reasoning events: %#v", events)
	}
}

func TestMarshalOpenAIWireRequestGeminiSanitizesToolTranscriptWithoutRawContent(t *testing.T) {
	messages := []types.Message{
		{
			Role:       "assistant",
			Content:    "Checking balance.",
			RawContent: json.RawMessage(`""`),
			ToolCalls: []types.ToolCall{{
				ID:   "call_123",
				Type: "function",
				Function: types.ToolCallFunctionRef{
					Name:      "monaco.get_balance",
					Arguments: `{"asset":"USDC"}`,
				},
			}},
		},
		{
			Role:       "tool",
			ToolCallID: "call_123",
			Content:    `{"ok":true,"balance":"12.34"}`,
		},
		{
			Role:    "assistant",
			Content: "Fresh Gemini tool call",
			RawContent: json.RawMessage(`[
				{"type":"functionCall","id":"call_live","name":"monaco.get_balance","args":{"asset":"SOL"},"thought_signature":"sig_live"}
			]`),
			ToolCalls: []types.ToolCall{{
				ID:   "call_live",
				Type: "function",
				Function: types.ToolCallFunctionRef{
					Name:      "monaco.get_balance",
					Arguments: `{"asset":"SOL"}`,
				},
			}},
		},
	}

	body, err := marshalOpenAIWireRequest("gemini", &types.ChatRequest{Model: "gemini-2.5-flash", Messages: messages})
	if err != nil {
		t.Fatalf("marshalOpenAIWireRequest: %v", err)
	}

	var wire struct {
		Messages []struct {
			Role      string           `json:"role"`
			Content   any              `json:"content"`
			ToolCalls []types.ToolCall `json:"tool_calls,omitempty"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(body, &wire); err != nil {
		t.Fatalf("decode wire request: %v", err)
	}
	if len(wire.Messages) != 2 {
		t.Fatalf("wire message count = %d, want 2", len(wire.Messages))
	}
	if wire.Messages[0].Role != "assistant" {
		t.Fatalf("first role = %q, want assistant", wire.Messages[0].Role)
	}
	if _, ok := wire.Messages[0].Content.(string); !ok {
		t.Fatalf("expected sanitized Gemini replay content to become plain text, got %#v", wire.Messages[0].Content)
	}
	if len(wire.Messages[0].ToolCalls) != 0 {
		t.Fatalf("expected sanitized Gemini replay to drop tool_calls, got %#v", wire.Messages[0].ToolCalls)
	}
	if !strings.Contains(wire.Messages[0].Content.(string), "previous tool call replay omitted") {
		t.Fatalf("expected compatibility note in sanitized content, got %q", wire.Messages[0].Content.(string))
	}
	if !strings.Contains(wire.Messages[0].Content.(string), "tool result monaco.get_balance") {
		t.Fatalf("expected tool result summary in sanitized content, got %q", wire.Messages[0].Content.(string))
	}
	contentParts, ok := wire.Messages[1].Content.([]any)
	if !ok {
		t.Fatalf("expected live Gemini tool call to keep raw multipart content, got %#v", wire.Messages[1].Content)
	}
	if len(contentParts) != 1 {
		t.Fatalf("live Gemini content parts = %d, want 1", len(contentParts))
	}
	part, ok := contentParts[0].(map[string]any)
	if !ok {
		t.Fatalf("expected Gemini content part object, got %#v", contentParts[0])
	}
	if part["thought_signature"] != "sig_live" {
		t.Fatalf("expected live Gemini thought_signature to survive, got %#v", part["thought_signature"])
	}
}

func TestGeminiToolReplayHasThoughtSignature(t *testing.T) {
	if geminiToolReplayHasThoughtSignature(json.RawMessage(`""`)) {
		t.Fatal("empty string raw content should not count as Gemini functionCall replay")
	}
	if geminiToolReplayHasThoughtSignature(json.RawMessage(`[{"type":"functionCall","id":"call_1","name":"peer.llm_provider.list","args":{},"thought_signature":""}]`)) {
		t.Fatal("blank thought_signature should not count as valid Gemini replay")
	}
	if !geminiToolReplayHasThoughtSignature(json.RawMessage(`[{"type":"functionCall","id":"call_1","name":"peer.llm_provider.list","args":{},"thought_signature":"sig_1"}]`)) {
		t.Fatal("expected Gemini functionCall replay with thought_signature to be preserved")
	}
}

func TestAnthropicCompleteStreamIdleTimeout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)
		fmt.Fprint(w, "event: message_start\n")
		fmt.Fprint(w, "data: {\"message\":{\"id\":\"msg_1\"}}\n\n")
		fmt.Fprint(w, "event: content_block_delta\n")
		fmt.Fprint(w, "data: {\"delta\":{\"type\":\"text_delta\",\"text\":\"hello\"}}\n\n")
		flusher.Flush()
		<-r.Context().Done()
	}))
	defer server.Close()

	p := &anthropicProvider{
		client:      server.Client(),
		apiKey:      "test-key",
		baseURL:     server.URL,
		model:       "claude-3-7-sonnet",
		idleTimeout: 50 * time.Millisecond,
		hooks:       anthropicHooks(),
	}

	rec := httptest.NewRecorder()
	text, err := p.CompleteStream(context.Background(), &types.ChatRequest{}, rec)
	if text != "hello" {
		t.Fatalf("text = %q", text)
	}
	if err == nil || !errors.Is(err, errStreamIdleTimeout) {
		t.Fatalf("expected idle-timeout error, got %v", err)
	}
	if !strings.Contains(rec.Body.String(), "hello") {
		t.Fatalf("expected streamed content to be forwarded, got %q", rec.Body.String())
	}
}

func TestAnthropicCompleteStreamEmitsReasoning(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)
		fmt.Fprint(w, "event: message_start\n")
		fmt.Fprint(w, "data: {\"message\":{\"id\":\"msg_1\"}}\n\n")
		fmt.Fprint(w, "event: content_block_delta\n")
		fmt.Fprint(w, "data: {\"delta\":{\"type\":\"thinking_delta\",\"thinking\":\"plan step\"}}\n\n")
		fmt.Fprint(w, "event: content_block_delta\n")
		fmt.Fprint(w, "data: {\"delta\":{\"type\":\"text_delta\",\"text\":\"hello\"}}\n\n")
		fmt.Fprint(w, "event: message_stop\n")
		fmt.Fprint(w, "data: {}\n\n")
		flusher.Flush()
	}))
	defer server.Close()

	p := &anthropicProvider{
		client:      server.Client(),
		apiKey:      "test-key",
		baseURL:     server.URL,
		model:       "claude-3-7-sonnet",
		idleTimeout: time.Second,
		hooks:       anthropicHooks(),
	}

	var events []types.ReasoningEvent
	ctx := types.WithReasoningSink(context.Background(), func(ev types.ReasoningEvent) {
		events = append(events, ev)
	})
	rec := httptest.NewRecorder()
	text, err := p.CompleteStream(ctx, &types.ChatRequest{ReasoningBudget: 2048, ReasoningVisibility: "full"}, rec)
	if err != nil {
		t.Fatalf("CompleteStream: %v", err)
	}
	if text != "hello" {
		t.Fatalf("text = %q, want hello", text)
	}
	if len(events) != 2 || events[0].Kind != types.ReasoningEventDelta || events[1].Kind != types.ReasoningEventSummary {
		t.Fatalf("unexpected reasoning events: %#v", events)
	}
}
