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
