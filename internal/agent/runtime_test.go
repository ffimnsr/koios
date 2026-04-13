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
	"github.com/ffimnsr/koios/internal/session"
	"github.com/ffimnsr/koios/internal/types"
)

type stubProvider struct {
	complete func(ctx context.Context, req *types.ChatRequest) (*types.ChatResponse, error)
	stream   func(ctx context.Context, req *types.ChatRequest, w http.ResponseWriter) (string, error)
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

func containsAll(s string, parts ...string) bool {
	for _, part := range parts {
		if !strings.Contains(s, part) {
			return false
		}
	}
	return true
}
