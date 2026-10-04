package agent_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ffimnsr/koios/internal/agent"
	"github.com/ffimnsr/koios/internal/session"
	"github.com/ffimnsr/koios/internal/types"
)

// TestRuntimeStreamsLiveAndExecutesNativeToolCalls verifies the #1 streaming
// fix: with a native-tool provider, a streamed tool-call turn is live (content
// deltas reach the client) while the tool call captured from the chunk stream
// is executed by the tool loop, and the run completes with the final answer.
func TestRuntimeStreamsLiveAndExecutesNativeToolCalls(t *testing.T) {
	store := session.New(40)
	var toolExecuted atomic.Int32
	prov := &stubProvider{
		caps: types.ProviderCapabilities{SupportsNativeTools: true},
		stream: func(_ context.Context, _ *types.ChatRequest, w http.ResponseWriter) (string, error) {
			if toolExecuted.Load() == 0 {
				// Step 1: stream content, then a native tool call.
				fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"planning \"}}]}\n\n")
				fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"call_1\",\"type\":\"function\",\"function\":{\"name\":\"time.now\",\"arguments\":\"{}\"}}]}}]}\n\n")
				fmt.Fprint(w, "data: [DONE]\n\n")
				return "planning ", nil
			}
			// Step 2: final text answer.
			fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"final answer\"}}]}\n\n")
			fmt.Fprint(w, "data: [DONE]\n\n")
			return "final answer", nil
		},
	}
	rt := agent.NewRuntime(store, prov, "model", 5*time.Second, agent.RetryPolicy{MaxAttempts: 1})

	exec := stubToolExecutor{
		defs: []types.Tool{{Function: types.ToolFunction{Name: "time.now", Description: "gets the time"}}},
		execute: func(_ context.Context, _ string, call agent.ToolCall) (any, error) {
			if call.Name != "time.now" {
				t.Fatalf("unexpected tool name %q", call.Name)
			}
			toolExecuted.Add(1)
			return map[string]string{"utc": "2026-04-29T00:00:00Z"}, nil
		},
	}

	rec := httptest.NewRecorder()
	result, err := rt.RunStream(context.Background(), agent.RunRequest{
		PeerID:       "peer",
		Scope:        agent.ScopeMain,
		Stream:       true,
		Messages:     []types.Message{{Role: "user", Content: "what time is it?"}},
		ToolExecutor: exec,
	}, rec)
	if err != nil {
		t.Fatalf("RunStream: %v", err)
	}
	if result.AssistantText != "final answer" {
		t.Fatalf("assistant text = %q, want %q", result.AssistantText, "final answer")
	}
	if toolExecuted.Load() != 1 {
		t.Fatalf("expected the streamed tool call to be executed once, got %d", toolExecuted.Load())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "planning ") {
		t.Fatalf("expected step 1 content to be streamed live, got %q", body)
	}
	if !strings.Contains(body, "final answer") {
		t.Fatalf("expected step 2 content to be streamed live, got %q", body)
	}
}

// TestRuntimeProbesEnvelopeProviderToolTurns verifies the envelope-provider
// path is preserved: text-protocol (XML tool call) providers still probe tool
// turns with a non-streaming call so the tool-call envelope is never streamed
// to the client — no bytes leak into the stream, and the tool still executes.
func TestRuntimeProbesEnvelopeProviderToolTurns(t *testing.T) {
	store := session.New(40)
	var (
		streamCalls   atomic.Int32
		completeCalls atomic.Int32
		toolExecuted  atomic.Int32
	)
	prov := &stubProvider{
		stream: func(_ context.Context, _ *types.ChatRequest, w http.ResponseWriter) (string, error) {
			streamCalls.Add(1)
			fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"LIVE\"}}]}\n\n")
			fmt.Fprint(w, "data: [DONE]\n\n")
			return "LIVE", nil
		},
		complete: func(_ context.Context, _ *types.ChatRequest) (*types.ChatResponse, error) {
			if completeCalls.Add(1) == 1 {
				return &types.ChatResponse{Choices: []types.ChatChoice{{
					Message: types.Message{Role: "assistant", Content: `<tool_call>{"name":"time.now","arguments":{}}</tool_call>`},
				}}}, nil
			}
			return &types.ChatResponse{Choices: []types.ChatChoice{{
				Message: types.Message{Role: "assistant", Content: "final answer"},
			}}}, nil
		},
	}
	rt := agent.NewRuntime(store, prov, "model", 5*time.Second, agent.RetryPolicy{MaxAttempts: 1})

	exec := stubToolExecutor{
		defs: []types.Tool{{Function: types.ToolFunction{Name: "time.now", Description: "gets the time"}}},
		execute: func(_ context.Context, _ string, call agent.ToolCall) (any, error) {
			toolExecuted.Add(1)
			return map[string]string{"utc": "2026-04-29T00:00:00Z"}, nil
		},
	}

	rec := httptest.NewRecorder()
	result, err := rt.RunStream(context.Background(), agent.RunRequest{
		PeerID:       "peer",
		Scope:        agent.ScopeMain,
		Stream:       true,
		Messages:     []types.Message{{Role: "user", Content: "what time is it?"}},
		ToolExecutor: exec,
	}, rec)
	if err != nil {
		t.Fatalf("RunStream: %v", err)
	}
	if result.AssistantText != "final answer" {
		t.Fatalf("assistant text = %q, want %q", result.AssistantText, "final answer")
	}
	if toolExecuted.Load() != 1 {
		t.Fatalf("expected the envelope tool call to be executed, got %d", toolExecuted.Load())
	}
	if got := streamCalls.Load(); got != 0 {
		t.Fatalf("envelope provider tool turns must not stream, got %d streamed call(s)", got)
	}
	if got := completeCalls.Load(); got != 2 {
		t.Fatalf("expected two non-streaming probes, got %d", got)
	}
	if body := rec.Body.String(); body != "" {
		t.Fatalf("expected no streamed bytes from envelope-provider tool turns, got %q", body)
	}
}
