package provider

import (
	"context"
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
