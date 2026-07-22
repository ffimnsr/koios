package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/ffimnsr/koios/internal/agent"
	"github.com/ffimnsr/koios/internal/artifacts"
	"github.com/ffimnsr/koios/internal/ops"
	"github.com/ffimnsr/koios/internal/session"
	"github.com/ffimnsr/koios/internal/toolresults"
	"github.com/ffimnsr/koios/internal/types"
)

func TestRecordToolResultStoresFullPayloadInArtifactWhenTruncated(t *testing.T) {
	store := session.New(10)
	artifactStore, err := artifacts.New(t.TempDir() + "/artifacts.db")
	if err != nil {
		t.Fatalf("artifacts.New: %v", err)
	}
	defer artifactStore.Close()
	toolResultStore, err := toolresults.New(t.TempDir() + "/tool_results.db")
	if err != nil {
		t.Fatalf("toolresults.New: %v", err)
	}
	defer toolResultStore.Close()
	h := NewHandler(store, noopProvider{}, HandlerOptions{
		Model:           "test-model",
		ArtifactStore:   artifactStore,
		ToolResultStore: toolResultStore,
	})

	result := map[string]any{"payload": strings.Repeat("A", 12000)}
	call := agent.ToolCall{ID: "tc-1", Name: "workspace.read", Arguments: json.RawMessage(`{"path":"README.md"}`)}
	h.recordToolResult(context.Background(), "alice", call, agent.ToolRunContext{SessionKey: "alice", ActiveProfile: "default"}, result, nil, 12, agent.ToolResultMetadata{})

	records, err := toolResultStore.List(context.Background(), "alice", toolresults.Filter{Limit: 10})
	if err != nil {
		t.Fatalf("toolResultStore.List: %v", err)
	}
	if len(records) != 1 {
		t.Fatalf("expected one tool result record, got %d", len(records))
	}
	rec := records[0]
	if !rec.Provenance.ResultTruncated {
		t.Fatalf("expected truncated provenance, got %#v", rec.Provenance)
	}
	if rec.Provenance.FullResultArtifact == "" {
		t.Fatalf("expected full result artifact pointer, got %#v", rec.Provenance)
	}
	if len(rec.ResultJSON) >= len(strings.Repeat("A", 12000)) {
		t.Fatalf("expected inline result json to be truncated, got %d chars", len(rec.ResultJSON))
	}
	artifact, err := artifactStore.Get(context.Background(), "alice", rec.Provenance.FullResultArtifact)
	if err != nil {
		t.Fatalf("artifactStore.Get: %v", err)
	}
	if artifact.Kind != "tool_result" {
		t.Fatalf("unexpected artifact kind: %#v", artifact)
	}
	if !strings.Contains(artifact.Content, strings.Repeat("A", 4000)) {
		t.Fatalf("expected artifact content to include full payload, got %q", artifact.Content)
	}
}

type runtimeToolProvider struct {
	complete func(context.Context, *types.ChatRequest) (*types.ChatResponse, error)
}

func (p runtimeToolProvider) Complete(ctx context.Context, req *types.ChatRequest) (*types.ChatResponse, error) {
	return p.complete(ctx, req)
}

func (runtimeToolProvider) CompleteStream(context.Context, *types.ChatRequest, http.ResponseWriter) (string, error) {
	return "", nil
}

func TestRecordToolResultKeepsFullPayloadInlineWithoutArtifactStore(t *testing.T) {
	store := session.New(10)
	toolResultStore, err := toolresults.New(t.TempDir() + "/tool_results.db")
	if err != nil {
		t.Fatalf("toolresults.New: %v", err)
	}
	defer toolResultStore.Close()
	h := NewHandler(store, noopProvider{}, HandlerOptions{
		Model:           "test-model",
		ToolResultStore: toolResultStore,
	})

	result := map[string]any{"payload": strings.Repeat("B", 12000)}
	call := agent.ToolCall{ID: "tc-2", Name: "workspace.read", Arguments: json.RawMessage(`{"path":"README.md"}`)}
	h.recordToolResult(context.Background(), "alice", call, agent.ToolRunContext{SessionKey: "alice"}, result, nil, 8, agent.ToolResultMetadata{})

	records, err := toolResultStore.List(context.Background(), "alice", toolresults.Filter{Limit: 10})
	if err != nil {
		t.Fatalf("toolResultStore.List: %v", err)
	}
	if len(records) != 1 {
		t.Fatalf("expected one tool result record, got %d", len(records))
	}
	rec := records[0]
	if rec.Provenance.ResultTruncated {
		t.Fatalf("did not expect truncation without artifact store, got %#v", rec.Provenance)
	}
	if rec.Provenance.FullResultArtifact != "" {
		t.Fatalf("did not expect artifact pointer without artifact store, got %#v", rec.Provenance)
	}
	if !strings.Contains(rec.ResultJSON, strings.Repeat("B", 4000)) {
		t.Fatalf("expected full inline payload, got %q", rec.ResultJSON)
	}
}

func TestRuntimeManagedToolPersistenceStoresHookDeniedOutcome(t *testing.T) {
	store := session.New(10)
	toolResultStore, err := toolresults.New(t.TempDir() + "/tool_results.db")
	if err != nil {
		t.Fatalf("toolresults.New: %v", err)
	}
	defer toolResultStore.Close()
	h := NewHandler(store, noopProvider{}, HandlerOptions{Model: "test-model", ToolResultStore: toolResultStore})
	callCount := 0
	prov := runtimeToolProvider{complete: func(_ context.Context, req *types.ChatRequest) (*types.ChatResponse, error) {
		callCount++
		if callCount == 1 {
			return &types.ChatResponse{Choices: []types.ChatChoice{{Message: types.Message{Role: "assistant", ToolCalls: []types.ToolCall{{
				ID:       "call-1",
				Type:     "function",
				Function: types.ToolCallFunctionRef{Name: "time.now", Arguments: `{}`},
			}}}}}}, nil
		}
		return &types.ChatResponse{Choices: []types.ChatChoice{{Message: types.Message{Role: "assistant", Content: "done"}}}}, nil
	}}
	rt := agent.NewRuntime(store, prov, "test-model", time.Second, agent.RetryPolicy{MaxAttempts: 1})
	hooks := ops.NewManager(time.Second, true)
	hooks.Register(ops.HookBeforeToolCall, 100, func(context.Context, ops.Event) error {
		return errors.New("approval denied")
	})
	rt.SetHooks(hooks)
	if _, err := rt.Run(context.Background(), agent.RunRequest{
		PeerID:       "alice",
		Scope:        agent.ScopeMain,
		MaxSteps:     2,
		Messages:     []types.Message{{Role: "user", Content: "what time is it?"}},
		ToolExecutor: h,
	}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	records, err := toolResultStore.List(context.Background(), "alice", toolresults.Filter{Limit: 10})
	if err != nil {
		t.Fatalf("toolResultStore.List: %v", err)
	}
	if len(records) != 1 {
		t.Fatalf("expected one tool result record, got %d", len(records))
	}
	rec := records[0]
	if !rec.IsError {
		t.Fatalf("expected hook-denied record to be error, got %#v", rec)
	}
	if rec.ToolCallID != "call-1" {
		t.Fatalf("tool call id = %q, want call-1", rec.ToolCallID)
	}
	if rec.Provenance.Outcome != "failure" {
		t.Fatalf("outcome = %q, want failure", rec.Provenance.Outcome)
	}
	if rec.Provenance.FailureKind != "hook_before" {
		t.Fatalf("failure kind = %q, want hook_before", rec.Provenance.FailureKind)
	}
	if rec.Provenance.ExecutionState != "not_started" {
		t.Fatalf("execution state = %q, want not_started", rec.Provenance.ExecutionState)
	}
	if rec.Provenance.ExecutionStarted {
		t.Fatalf("expected execution_started=false, got %#v", rec.Provenance)
	}
	if rec.Provenance.ApprovalState != "denied" {
		t.Fatalf("approval state = %q, want denied", rec.Provenance.ApprovalState)
	}
	if rec.Provenance.SideEffectUnknown {
		t.Fatalf("expected side_effect_unknown=false, got %#v", rec.Provenance)
	}
	if !rec.Provenance.RuntimeManaged {
		t.Fatalf("expected runtime managed provenance, got %#v", rec.Provenance)
	}
	if !strings.Contains(rec.Summary, "approval denied") {
		t.Fatalf("expected summary to contain hook error, got %q", rec.Summary)
	}
}

func TestExecuteToolPersistsTypedGenericFailureMetadata(t *testing.T) {
	store := session.New(10)
	toolResultStore, err := toolresults.New(t.TempDir() + "/tool_results.db")
	if err != nil {
		t.Fatalf("toolresults.New: %v", err)
	}
	defer toolResultStore.Close()
	h := NewHandler(store, noopProvider{}, HandlerOptions{Model: "test-model", ToolResultStore: toolResultStore})
	if _, err := h.ExecuteTool(context.Background(), "alice", agent.ToolCall{Name: "madeup.send", Arguments: json.RawMessage(`{"text":"hello"}`)}); err == nil {
		t.Fatal("expected unknown tool error")
	}
	records, err := toolResultStore.List(context.Background(), "alice", toolresults.Filter{Limit: 10})
	if err != nil {
		t.Fatalf("toolResultStore.List: %v", err)
	}
	if len(records) != 1 {
		t.Fatalf("expected one tool result record, got %d", len(records))
	}
	rec := records[0]
	if rec.Provenance.Outcome != "failure" {
		t.Fatalf("outcome = %q, want failure", rec.Provenance.Outcome)
	}
	if rec.Provenance.FailureKind != "unknown_tool" {
		t.Fatalf("failure kind = %q, want unknown_tool", rec.Provenance.FailureKind)
	}
	if rec.Provenance.ExecutionState != "not_started" || rec.Provenance.ExecutionStarted {
		t.Fatalf("expected not_started/false execution metadata, got %#v", rec.Provenance)
	}
	if rec.Provenance.ApprovalState != "" {
		t.Fatalf("approval state = %q, want empty", rec.Provenance.ApprovalState)
	}
	if rec.Provenance.RuntimeManaged {
		t.Fatalf("expected runtime_managed=false, got %#v", rec.Provenance)
	}
	if !strings.Contains(rec.Summary, "unknown tool") {
		t.Fatalf("expected summary to contain unknown tool error, got %q", rec.Summary)
	}
}

func TestRuntimeManagedToolPersistenceStoresPostHookFailureOutcome(t *testing.T) {
	store := session.New(10)
	toolResultStore, err := toolresults.New(t.TempDir() + "/tool_results.db")
	if err != nil {
		t.Fatalf("toolresults.New: %v", err)
	}
	defer toolResultStore.Close()
	h := NewHandler(store, noopProvider{}, HandlerOptions{Model: "test-model", ToolResultStore: toolResultStore})
	callCount := 0
	prov := runtimeToolProvider{complete: func(_ context.Context, req *types.ChatRequest) (*types.ChatResponse, error) {
		callCount++
		if callCount == 1 {
			return &types.ChatResponse{Choices: []types.ChatChoice{{Message: types.Message{Role: "assistant", ToolCalls: []types.ToolCall{{
				ID:       "call-2",
				Type:     "function",
				Function: types.ToolCallFunctionRef{Name: "time.now", Arguments: `{}`},
			}}}}}}, nil
		}
		return &types.ChatResponse{Choices: []types.ChatChoice{{Message: types.Message{Role: "assistant", Content: "done"}}}}, nil
	}}
	rt := agent.NewRuntime(store, prov, "test-model", time.Second, agent.RetryPolicy{MaxAttempts: 1})
	hooks := ops.NewManager(time.Second, true)
	hooks.Register(ops.HookAfterToolCall, 100, func(context.Context, ops.Event) error {
		return errors.New("post hook failed")
	})
	rt.SetHooks(hooks)
	if _, err := rt.Run(context.Background(), agent.RunRequest{
		PeerID:       "alice",
		Scope:        agent.ScopeMain,
		MaxSteps:     2,
		Messages:     []types.Message{{Role: "user", Content: "what time is it?"}},
		ToolExecutor: h,
	}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	records, err := toolResultStore.List(context.Background(), "alice", toolresults.Filter{Limit: 10})
	if err != nil {
		t.Fatalf("toolResultStore.List: %v", err)
	}
	if len(records) != 1 {
		t.Fatalf("expected one tool result record, got %d", len(records))
	}
	rec := records[0]
	if !rec.IsError {
		t.Fatalf("expected post-hook record to be error, got %#v", rec)
	}
	if rec.ToolCallID != "call-2" {
		t.Fatalf("tool call id = %q, want call-2", rec.ToolCallID)
	}
	if rec.Provenance.Outcome != "failure" {
		t.Fatalf("outcome = %q, want failure", rec.Provenance.Outcome)
	}
	if rec.Provenance.FailureKind != "hook_after" {
		t.Fatalf("failure kind = %q, want hook_after", rec.Provenance.FailureKind)
	}
	if rec.Provenance.ExecutionState != "started" {
		t.Fatalf("execution state = %q, want started", rec.Provenance.ExecutionState)
	}
	if !rec.Provenance.ExecutionStarted {
		t.Fatalf("expected execution_started=true, got %#v", rec.Provenance)
	}
	if rec.Provenance.SideEffectUnknown {
		t.Fatalf("expected side_effect_unknown=false for non-mutating tool, got %#v", rec.Provenance)
	}
	if rec.Provenance.ApprovalState != "" {
		t.Fatalf("approval state = %q, want empty", rec.Provenance.ApprovalState)
	}
	if !rec.Provenance.RuntimeManaged {
		t.Fatalf("expected runtime managed provenance, got %#v", rec.Provenance)
	}
	if !strings.Contains(rec.Summary, "post hook failed") {
		t.Fatalf("expected summary to contain post-hook error, got %q", rec.Summary)
	}
	if !strings.Contains(rec.ResultJSON, "utc") {
		t.Fatalf("expected successful tool payload to remain stored, got %q", rec.ResultJSON)
	}
}
