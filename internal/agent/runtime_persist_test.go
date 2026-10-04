package agent_test

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ffimnsr/koios/internal/agent"
	"github.com/ffimnsr/koios/internal/session"
	"github.com/ffimnsr/koios/internal/types"
)

// partialPersistProvider returns a tool call on the first invocation and fails
// on subsequent ones so a run can make progress and then be interrupted.
type partialPersistProvider struct {
	calls atomic.Int32
}

func (p *partialPersistProvider) Complete(context.Context, *types.ChatRequest) (*types.ChatResponse, error) {
	if p.calls.Add(1) == 1 {
		return &types.ChatResponse{Choices: []types.ChatChoice{{
			Message: types.Message{Role: "assistant", Content: `<tool_call>{"name":"time.now","arguments":{}}</tool_call>`},
		}}}, nil
	}
	return nil, errors.New("provider exploded")
}

func (p *partialPersistProvider) CompleteStream(context.Context, *types.ChatRequest, http.ResponseWriter) (string, error) {
	return "", nil
}

// partialPersistToolExecutor records tool calls and returns a simple result.
type partialPersistToolExecutor struct{}

func (partialPersistToolExecutor) ToolPromptForRun(_, _, _ string) string { return "use tools" }
func (partialPersistToolExecutor) ToolPromptForRunWithContext(_, _, _ string, _ []types.Message, _ int) string {
	return "use tools"
}
func (partialPersistToolExecutor) ToolDefinitionsForRun(_, _, _ string) []types.Tool { return nil }
func (partialPersistToolExecutor) ToolDefinitionsForRunWithContext(_, _, _ string, _ []types.Message, _ int) []types.Tool {
	return nil
}
func (partialPersistToolExecutor) ExecuteTool(_ context.Context, _ string, call agent.ToolCall) (any, error) {
	return map[string]string{"utc": "2026-04-29T00:00:00Z"}, nil
}
func (partialPersistToolExecutor) ToolMutatesState(_, _ string) bool { return false }

// TestRuntimePersistsPartialTranscriptOnInterrupt verifies that a run which
// fails after executing tools still appends its partial transcript (assistant
// tool call + tool result) to session history, so interrupted turns do not
// vanish from the conversation record.
func TestRuntimePersistsPartialTranscriptOnInterrupt(t *testing.T) {
	store := session.New(40)
	rt := agent.NewRuntime(store, &partialPersistProvider{}, "model", 5*time.Second, agent.RetryPolicy{MaxAttempts: 1})

	result, err := rt.Run(context.Background(), agent.RunRequest{
		PeerID:       "peer",
		Scope:        agent.ScopeMain,
		Messages:     []types.Message{{Role: "user", Content: "what time is it?"}},
		ToolExecutor: partialPersistToolExecutor{},
	})
	if err == nil {
		t.Fatalf("expected the run to fail, got %#v", result)
	}
	history := store.Get("peer::main").History()
	// The partial transcript must persist: user message, assistant tool call,
	// and the executed tool result.
	if len(history) != 3 {
		t.Fatalf("expected 3 persisted messages, got %d: %#v", len(history), history)
	}
	if history[1].Role != "assistant" || !strings.Contains(history[1].Content, "tool_call") {
		t.Fatalf("expected assistant tool-call message, got %#v", history[1])
	}
	if history[2].Role != "tool" || !strings.Contains(history[2].Content, "utc") {
		t.Fatalf("expected tool result message, got %#v", history[2])
	}
}

// TestRuntimeSurfacesOrchestrationDepthInToolContext verifies the depth
// plumbing for the orchestration recursion bound: RunRequest.
// OrchestrationDepth is exposed to tool execution through ToolRunContext so a
// nested orchestrator.start call can compute its own depth.
func TestRuntimeSurfacesOrchestrationDepthInToolContext(t *testing.T) {
	store := session.New(40)
	var calls atomic.Int32
	prov := &stubProvider{
		complete: func(_ context.Context, _ *types.ChatRequest) (*types.ChatResponse, error) {
			if calls.Add(1) == 1 {
				return &types.ChatResponse{Choices: []types.ChatChoice{{
					Message: types.Message{Role: "assistant", Content: `<tool_call>{"name":"time.now","arguments":{}}</tool_call>`},
				}}}, nil
			}
			return &types.ChatResponse{Choices: []types.ChatChoice{{
				Message: types.Message{Role: "assistant", Content: "done"},
			}}}, nil
		},
	}
	rt := agent.NewRuntime(store, prov, "model", 5*time.Second, agent.RetryPolicy{MaxAttempts: 1})

	var observedDepth int
	exec := stubToolExecutor{
		defs: []types.Tool{{Function: types.ToolFunction{Name: "time.now"}}},
		execute: func(ctx context.Context, _ string, _ agent.ToolCall) (any, error) {
			if toolCtx, ok := agent.ToolRunContextFromContext(ctx); ok {
				observedDepth = toolCtx.OrchestrationDepth
			}
			return map[string]string{"utc": "x"}, nil
		},
	}

	_, err := rt.Run(context.Background(), agent.RunRequest{
		PeerID:             "peer",
		Scope:              agent.ScopeMain,
		Messages:           []types.Message{{Role: "user", Content: "time please"}},
		ToolExecutor:       exec,
		MaxSteps:           2,
		OrchestrationDepth: 2,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if observedDepth != 2 {
		t.Fatalf("tool run context depth = %d, want 2", observedDepth)
	}
}

// TestRuntimeDoesNotPersistFreshRequestOnInterrupt verifies that a run failing
// before producing any turn content (no tools executed, no assistant output)
// persists only the request messages — the same transcript base a completed
// turn would persist — and nothing more.
func TestRuntimeDoesNotPersistFreshRequestOnInterrupt(t *testing.T) {
	store := session.New(40)
	prov := &partialPersistProvider{}
	// Fail immediately on the first call.
	prov.calls.Add(1)
	rt := agent.NewRuntime(store, prov, "model", 5*time.Second, agent.RetryPolicy{MaxAttempts: 1})

	_, err := rt.Run(context.Background(), agent.RunRequest{
		PeerID:   "peer",
		Scope:    agent.ScopeMain,
		Messages: []types.Message{{Role: "user", Content: "hello"}},
	})
	if err == nil {
		t.Fatal("expected the run to fail")
	}
	history := store.Get("peer::main").History()
	if len(history) != 1 || history[0].Role != "user" || history[0].Content != "hello" {
		t.Fatalf("expected only the request message to persist, got %#v", history)
	}
}
