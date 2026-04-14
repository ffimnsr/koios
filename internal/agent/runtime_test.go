package agent_test

import (
	"context"
	"errors"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ffimnsr/koios/internal/agent"
	"github.com/ffimnsr/koios/internal/memory"
	"github.com/ffimnsr/koios/internal/ops"
	"github.com/ffimnsr/koios/internal/session"
	"github.com/ffimnsr/koios/internal/types"
)

type stubProvider struct {
	complete func(ctx context.Context, req *types.ChatRequest) (*types.ChatResponse, error)
	stream   func(ctx context.Context, req *types.ChatRequest, w http.ResponseWriter) (string, error)
}

type stubToolExecutor struct {
	execute func(ctx context.Context, peerID string, call agent.ToolCall) (any, error)
}

func (s stubToolExecutor) ToolPrompt(peerID string) string {
	return "use tools"
}

func (s stubToolExecutor) ToolDefinitions(peerID string) []types.Tool {
	return nil
}

func (s stubToolExecutor) ExecuteTool(ctx context.Context, peerID string, call agent.ToolCall) (any, error) {
	return s.execute(ctx, peerID, call)
}

func (s *stubProvider) Complete(ctx context.Context, req *types.ChatRequest) (*types.ChatResponse, error) {
	_ = req
	return s.complete(ctx, req)
}

func (s *stubProvider) CompleteStream(ctx context.Context, req *types.ChatRequest, w http.ResponseWriter) (string, error) {
	_ = req
	_ = w
	return s.stream(ctx, req, w)
}

func TestRuntime_DirectScopeUsesSenderSession(t *testing.T) {
	store := session.New(20)
	prov := &stubProvider{
		complete: func(_ context.Context, req *types.ChatRequest) (*types.ChatResponse, error) {
			return &types.ChatResponse{Choices: []types.ChatChoice{{Message: types.Message{Role: "assistant", Content: "ok"}}}}, nil
		},
	}
	rt := agent.NewRuntime(store, prov, "model", time.Second, agent.RetryPolicy{MaxAttempts: 1})

	if _, err := rt.Run(context.Background(), agent.RunRequest{
		PeerID:   "peer",
		SenderID: "alice",
		Scope:    agent.ScopeDirect,
		Messages: []types.Message{{Role: "user", Content: "hello"}},
	}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if _, err := rt.Run(context.Background(), agent.RunRequest{
		PeerID:   "peer",
		SenderID: "bob",
		Scope:    agent.ScopeDirect,
		Messages: []types.Message{{Role: "user", Content: "hello"}},
	}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := store.Len(); got != 2 {
		t.Fatalf("expected two isolated sender sessions, got %d", got)
	}
}

func TestRuntime_RetriesTransientFailures(t *testing.T) {
	store := session.New(20)
	attempts := 0
	prov := &stubProvider{
		complete: func(_ context.Context, req *types.ChatRequest) (*types.ChatResponse, error) {
			attempts++
			if attempts == 1 {
				return nil, errors.New("timeout")
			}
			return &types.ChatResponse{Choices: []types.ChatChoice{{Message: types.Message{Role: "assistant", Content: "retry ok"}}}}, nil
		},
	}
	rt := agent.NewRuntime(store, prov, "model", time.Second, agent.RetryPolicy{MaxAttempts: 2, InitialBackoff: time.Millisecond, MaxBackoff: time.Millisecond})

	res, err := rt.Run(context.Background(), agent.RunRequest{
		PeerID:   "peer",
		Scope:    agent.ScopeMain,
		Messages: []types.Message{{Role: "user", Content: "hello"}},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Attempts != 2 {
		t.Fatalf("expected 2 attempts, got %d", res.Attempts)
	}
	if res.AssistantText != "retry ok" {
		t.Fatalf("unexpected assistant text %q", res.AssistantText)
	}
}

func TestRuntime_RetryStatusCodeFilter(t *testing.T) {
	store := session.New(20)
	attempts := 0
	prov := &stubProvider{
		complete: func(_ context.Context, req *types.ChatRequest) (*types.ChatResponse, error) {
			attempts++
			if attempts == 1 {
				return nil, errors.New("http 418")
			}
			return &types.ChatResponse{Choices: []types.ChatChoice{{Message: types.Message{Role: "assistant", Content: "ok"}}}}, nil
		},
	}
	rt := agent.NewRuntime(store, prov, "model", time.Second, agent.RetryPolicy{
		MaxAttempts:    2,
		InitialBackoff: time.Millisecond,
		MaxBackoff:     time.Millisecond,
		StatusCodes:    []int{429},
	})

	_, err := rt.Run(context.Background(), agent.RunRequest{
		PeerID:   "peer",
		Scope:    agent.ScopeMain,
		Messages: []types.Message{{Role: "user", Content: "hello"}},
	})
	if err == nil {
		t.Fatal("expected non-retryable 418 error")
	}
	if attempts != 1 {
		t.Fatalf("expected one attempt, got %d", attempts)
	}
}

func TestRuntime_ResultIncludesUsage(t *testing.T) {
	store := session.New(20)
	prov := &stubProvider{
		complete: func(_ context.Context, req *types.ChatRequest) (*types.ChatResponse, error) {
			return &types.ChatResponse{
				Choices: []types.ChatChoice{{Message: types.Message{Role: "assistant", Content: "ok"}}},
				Usage:   types.Usage{PromptTokens: 11, CompletionTokens: 7, TotalTokens: 18},
			}, nil
		},
	}
	rt := agent.NewRuntime(store, prov, "model", time.Second, agent.RetryPolicy{MaxAttempts: 1})
	res, err := rt.Run(context.Background(), agent.RunRequest{
		PeerID:   "peer",
		Scope:    agent.ScopeMain,
		Messages: []types.Message{{Role: "user", Content: "hello"}},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Usage.PromptTokens != 11 || res.Usage.CompletionTokens != 7 || res.Usage.TotalTokens != 18 {
		t.Fatalf("unexpected usage: %#v", res.Usage)
	}
}

func TestRuntime_InjectsMemoryIntoAgentRuns(t *testing.T) {
	store := session.New(20)
	memStore, err := memory.New(filepath.Join(t.TempDir(), "memory.db"), nil)
	if err != nil {
		t.Fatalf("memory.New: %v", err)
	}
	t.Cleanup(func() { _ = memStore.Close() })
	if err := memStore.Insert(context.Background(), "peer", "Prior project note about deployment constraints and deployment windows"); err != nil {
		t.Fatalf("memory.Insert: %v", err)
	}

	var captured *types.ChatRequest
	prov := &stubProvider{
		complete: func(_ context.Context, req *types.ChatRequest) (*types.ChatResponse, error) {
			captured = req
			return &types.ChatResponse{Choices: []types.ChatChoice{{Message: types.Message{Role: "assistant", Content: "ok"}}}}, nil
		},
	}
	rt := agent.NewRuntime(store, prov, "model", time.Second, agent.RetryPolicy{MaxAttempts: 1})
	rt.EnableMemory(memStore, true, 3)

	res, err := rt.Run(context.Background(), agent.RunRequest{
		PeerID:   "peer",
		Scope:    agent.ScopeMain,
		MaxSteps: 2,
		Messages: []types.Message{{Role: "user", Content: "deployment constraints"}},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if captured == nil {
		t.Fatal("expected provider request to be captured")
	}
	foundMemory := false
	for _, msg := range captured.Messages {
		if msg.Role == "system" && msg.Content != "" && msg.Content != "What should I know about deployment?" {
			if containsAll(msg.Content, "Relevant context from past conversations:", "deployment windows") {
				foundMemory = true
			}
		}
	}
	if !foundMemory {
		t.Fatalf("expected injected memory in request messages, got %#v", captured.Messages)
	}
	if res.Steps != 1 {
		t.Fatalf("expected runtime to complete in one step, got %d", res.Steps)
	}
}

func TestRuntime_BeforeLLMInterceptorCanRewriteRequest(t *testing.T) {
	store := session.New(20)
	var captured *types.ChatRequest
	prov := &stubProvider{
		complete: func(_ context.Context, req *types.ChatRequest) (*types.ChatResponse, error) {
			captured = req
			return &types.ChatResponse{
				Choices: []types.ChatChoice{{Message: types.Message{Role: "assistant", Content: "ok"}}},
			}, nil
		},
	}
	rt := agent.NewRuntime(store, prov, "model-a", time.Second, agent.RetryPolicy{MaxAttempts: 1})
	hooks := ops.NewManager(time.Second, true)
	hooks.RegisterInterceptor(ops.HookBeforeLLM, 100, func(_ context.Context, ev ops.Event) (ops.Event, error) {
		ev.Data["model"] = "model-b"
		ev.Data["messages"] = []types.Message{{Role: "user", Content: "rewritten prompt"}}
		return ev, nil
	})
	rt.SetHooks(hooks)

	_, err := rt.Run(context.Background(), agent.RunRequest{
		PeerID:   "peer",
		Scope:    agent.ScopeMain,
		Messages: []types.Message{{Role: "user", Content: "original prompt"}},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if captured == nil {
		t.Fatal("expected provider request capture")
	}
	if captured.Model != "model-b" {
		t.Fatalf("expected interceptor model rewrite, got %q", captured.Model)
	}
	if len(captured.Messages) == 0 || captured.Messages[len(captured.Messages)-1].Content != "rewritten prompt" {
		t.Fatalf("expected interceptor message rewrite, got %#v", captured.Messages)
	}
}

func TestRuntime_XMLToolFallbackStoresToolResultAsToolRole(t *testing.T) {
	store := session.New(20)
	var seen []*types.ChatRequest
	callCount := 0
	prov := &stubProvider{
		complete: func(_ context.Context, req *types.ChatRequest) (*types.ChatResponse, error) {
			seen = append(seen, req)
			callCount++
			if callCount == 1 {
				return &types.ChatResponse{Choices: []types.ChatChoice{{Message: types.Message{Role: "assistant", Content: `<tool_call>{"name":"web_fetch","arguments":{"url":"https://example.com?token=sk_secret1234567890"}}</tool_call>`}}}}, nil
			}
			return &types.ChatResponse{Choices: []types.ChatChoice{{Message: types.Message{Role: "assistant", Content: "done"}}}}, nil
		},
	}
	rt := agent.NewRuntime(store, prov, "model", time.Second, agent.RetryPolicy{MaxAttempts: 1})
	_, err := rt.Run(context.Background(), agent.RunRequest{
		PeerID:   "peer",
		Scope:    agent.ScopeMain,
		MaxSteps: 2,
		Messages: []types.Message{{Role: "user", Content: "fetch it"}},
		ToolExecutor: stubToolExecutor{execute: func(_ context.Context, _ string, call agent.ToolCall) (any, error) {
			return map[string]any{"url": "https://example.com?token=sk_secret1234567890"}, nil
		}},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(seen) < 2 {
		t.Fatalf("expected second request after tool call, got %d requests", len(seen))
	}
	last := seen[1].Messages[len(seen[1].Messages)-1]
	if last.Role != "tool" {
		t.Fatalf("expected tool-role message for tool result, got %#v", last)
	}
	if !strings.Contains(last.Content, "[REDACTED]") {
		t.Fatalf("expected redacted tool result, got %q", last.Content)
	}
}

func containsAll(s string, parts ...string) bool {
	for _, part := range parts {
		if !strings.Contains(s, part) {
			return false
		}
	}
	return true
}
