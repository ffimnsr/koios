package agent_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ffimnsr/koios/internal/agent"
	"github.com/ffimnsr/koios/internal/config"
	"github.com/ffimnsr/koios/internal/memory"
	"github.com/ffimnsr/koios/internal/ops"
	"github.com/ffimnsr/koios/internal/session"
	"github.com/ffimnsr/koios/internal/skills"
	"github.com/ffimnsr/koios/internal/standing"
	"github.com/ffimnsr/koios/internal/types"
)

type stubProvider struct {
	complete func(ctx context.Context, req *types.ChatRequest) (*types.ChatResponse, error)
	stream   func(ctx context.Context, req *types.ChatRequest, w http.ResponseWriter) (string, error)
	caps     types.ProviderCapabilities
}

type stubToolExecutor struct {
	execute       func(ctx context.Context, peerID string, call agent.ToolCall) (any, error)
	defs          []types.Tool
	contextPrompt func(peerID, sessionKey, activeProfile string, messages []types.Message, maxDefinitions int) string
	contextDefs   func(peerID, sessionKey, activeProfile string, messages []types.Message, maxDefinitions int) []types.Tool
	mutates       func(peerID, name string) bool
}

type stubProviderResolver struct {
	resolve func(ctx context.Context, peerID, sessionKey, modelOverride string) (agent.Provider, string, error)
}

func (s stubToolExecutor) ToolPromptForRun(peerID, sessionKey, activeProfile string) string {
	_, _, _ = peerID, sessionKey, activeProfile
	return "use tools"
}

func (s stubToolExecutor) ToolDefinitionsForRun(peerID, sessionKey, activeProfile string) []types.Tool {
	_, _, _ = peerID, sessionKey, activeProfile
	return append([]types.Tool(nil), s.defs...)
}

func (s stubToolExecutor) ToolPromptForRunWithContext(peerID, sessionKey, activeProfile string, messages []types.Message, maxDefinitions int) string {
	if s.contextPrompt != nil {
		return s.contextPrompt(peerID, sessionKey, activeProfile, messages, maxDefinitions)
	}
	return s.ToolPromptForRun(peerID, sessionKey, activeProfile)
}

func (s stubToolExecutor) ToolDefinitionsForRunWithContext(peerID, sessionKey, activeProfile string, messages []types.Message, maxDefinitions int) []types.Tool {
	if s.contextDefs != nil {
		return s.contextDefs(peerID, sessionKey, activeProfile, messages, maxDefinitions)
	}
	return s.ToolDefinitionsForRun(peerID, sessionKey, activeProfile)
}

func (s stubToolExecutor) ExecuteTool(ctx context.Context, peerID string, call agent.ToolCall) (any, error) {
	return s.execute(ctx, peerID, call)
}

func (s stubToolExecutor) ToolMutatesState(peerID, name string) bool {
	if s.mutates != nil {
		return s.mutates(peerID, name)
	}
	return false
}

func (s stubProviderResolver) ResolveProvider(ctx context.Context, peerID, sessionKey, modelOverride string) (agent.Provider, string, error) {
	return s.resolve(ctx, peerID, sessionKey, modelOverride)
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

func (s *stubProvider) Capabilities(string) types.ProviderCapabilities {
	return s.caps
}

func mustRawJSON(t *testing.T, raw string) json.RawMessage {
	t.Helper()
	return json.RawMessage(raw)
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
		t.Fatalf("expected 2 sessions, got %d", got)
	}
}

func TestRuntime_UsesConfiguredDefaultMaxStepsWhenOmitted(t *testing.T) {
	store := session.New(20)
	callCount := 0
	prov := &stubProvider{
		complete: func(_ context.Context, req *types.ChatRequest) (*types.ChatResponse, error) {
			callCount++
			if callCount == 1 {
				return &types.ChatResponse{Choices: []types.ChatChoice{{Message: types.Message{Role: "assistant", Content: `<tool_call>{"name":"time.now","arguments":{}}</tool_call>`}}}}, nil
			}
			return &types.ChatResponse{Choices: []types.ChatChoice{{Message: types.Message{Role: "assistant", Content: "done"}}}}, nil
		},
	}
	rt := agent.NewRuntime(store, prov, "model", time.Second, agent.RetryPolicy{MaxAttempts: 1})
	rt.SetDefaultMaxSteps(2)

	res, err := rt.Run(context.Background(), agent.RunRequest{
		PeerID:   "peer",
		Scope:    agent.ScopeMain,
		Messages: []types.Message{{Role: "user", Content: "what time is it?"}},
		ToolExecutor: stubToolExecutor{execute: func(_ context.Context, _ string, call agent.ToolCall) (any, error) {
			if call.Name != "time.now" {
				t.Fatalf("unexpected tool name %q", call.Name)
			}
			return map[string]string{"utc": "2026-04-29T00:00:00Z"}, nil
		}},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Steps != 2 {
		t.Fatalf("expected configured default max_steps to allow second step, got %d", res.Steps)
	}
	if callCount != 2 {
		t.Fatalf("expected 2 provider calls, got %d", callCount)
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

func TestRuntime_UsesResolvedProviderCapabilitiesForToolSupport(t *testing.T) {
	store := session.New(20)
	globalProv := &stubProvider{
		complete: func(_ context.Context, req *types.ChatRequest) (*types.ChatResponse, error) {
			return &types.ChatResponse{Choices: []types.ChatChoice{{Message: types.Message{Role: "assistant", Content: "global"}}}}, nil
		},
		caps: types.ProviderCapabilities{Name: "global", SupportsNativeTools: false},
	}
	var captured *types.ChatRequest
	resolvedProv := &stubProvider{
		complete: func(_ context.Context, req *types.ChatRequest) (*types.ChatResponse, error) {
			captured = req
			return &types.ChatResponse{Choices: []types.ChatChoice{{Message: types.Message{Role: "assistant", Content: "resolved"}}}}, nil
		},
		caps: types.ProviderCapabilities{Name: "resolved", SupportsNativeTools: true},
	}
	rt := agent.NewRuntime(store, globalProv, "model", time.Second, agent.RetryPolicy{MaxAttempts: 1})
	rt.SetProviderResolver(stubProviderResolver{
		resolve: func(_ context.Context, peerID, sessionKey, modelOverride string) (agent.Provider, string, error) {
			_, _, _ = peerID, sessionKey, modelOverride
			return resolvedProv, "resolved-model", nil
		},
	})

	toolExec := stubToolExecutor{
		execute: func(context.Context, string, agent.ToolCall) (any, error) { return nil, nil },
		defs: []types.Tool{{
			Type: "function",
			Function: types.ToolFunction{
				Name:        "task.create",
				Description: "create a task",
				Parameters:  mustRawJSON(t, `{"type":"object"}`),
			},
		}},
	}

	if _, err := rt.Run(context.Background(), agent.RunRequest{
		PeerID:       "peer",
		Scope:        agent.ScopeMain,
		Messages:     []types.Message{{Role: "user", Content: "hello"}},
		ToolExecutor: toolExec,
	}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if captured == nil {
		t.Fatal("expected resolved provider to capture request")
	}
	if captured.Model != "resolved-model" {
		t.Fatalf("expected resolved model, got %q", captured.Model)
	}
	if len(captured.Tools) != 1 || captured.Tools[0].Function.Name != "task.create" {
		t.Fatalf("expected resolved provider capabilities to keep native tools, got %#v", captured.Tools)
	}
}

func TestRuntime_UsesContextualToolSelectionLimits(t *testing.T) {
	store := session.New(20)
	var captured *types.ChatRequest
	prov := &stubProvider{
		complete: func(_ context.Context, req *types.ChatRequest) (*types.ChatResponse, error) {
			captured = req
			return &types.ChatResponse{Choices: []types.ChatChoice{{Message: types.Message{Role: "assistant", Content: "ok"}}}}, nil
		},
		caps: types.ProviderCapabilities{Name: "stub", SupportsNativeTools: true},
	}
	rt := agent.NewRuntime(store, prov, "model", time.Second, agent.RetryPolicy{MaxAttempts: 1})
	rt.SetContextBudget(32000, 4000, 2, 4000)
	observedMaxDefs := 0
	toolExec := stubToolExecutor{
		execute: func(context.Context, string, agent.ToolCall) (any, error) { return nil, nil },
		contextPrompt: func(_, _, _ string, messages []types.Message, maxDefinitions int) string {
			observedMaxDefs = maxDefinitions
			if len(messages) == 0 || messages[len(messages)-1].Content != "show my schedule" {
				t.Fatalf("unexpected context messages: %#v", messages)
			}
			return "use limited tools"
		},
		contextDefs: func(_, _, _ string, _ []types.Message, maxDefinitions int) []types.Tool {
			all := []types.Tool{
				{Type: "function", Function: types.ToolFunction{Name: "tool.search", Parameters: mustRawJSON(t, `{"type":"object"}`)}},
				{Type: "function", Function: types.ToolFunction{Name: "calendar.today", Parameters: mustRawJSON(t, `{"type":"object"}`)}},
				{Type: "function", Function: types.ToolFunction{Name: "task.list", Parameters: mustRawJSON(t, `{"type":"object"}`)}},
			}
			return append([]types.Tool(nil), all[:maxDefinitions]...)
		},
	}
	_, err := rt.Run(context.Background(), agent.RunRequest{
		PeerID:       "peer",
		Scope:        agent.ScopeMain,
		Messages:     []types.Message{{Role: "user", Content: "show my schedule"}},
		ToolExecutor: toolExec,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if observedMaxDefs != 2 {
		t.Fatalf("expected contextual maxDefinitions=2, got %d", observedMaxDefs)
	}
	if captured == nil {
		t.Fatal("expected captured request")
	}
	if len(captured.Tools) != 2 {
		t.Fatalf("expected two contextual tools, got %#v", captured.Tools)
	}
	if captured.Messages[1].Content != "use limited tools" {
		t.Fatalf("expected contextual prompt in request, got %#v", captured.Messages)
	}
}

func TestRuntime_TruncatesLargeToolResultsInActiveContext(t *testing.T) {
	store := session.New(20)
	var seenToolMsg string
	callCount := 0
	prov := &stubProvider{
		complete: func(_ context.Context, req *types.ChatRequest) (*types.ChatResponse, error) {
			callCount++
			if callCount == 1 {
				return &types.ChatResponse{Choices: []types.ChatChoice{{Message: types.Message{Role: "assistant", Content: `<tool_call>{"name":"workspace.read","arguments":{}}</tool_call>`}}}}, nil
			}
			for _, msg := range req.Messages {
				if msg.Role == "tool" {
					seenToolMsg = msg.Content
				}
			}
			return &types.ChatResponse{Choices: []types.ChatChoice{{Message: types.Message{Role: "assistant", Content: "done"}}}}, nil
		},
	}
	rt := agent.NewRuntime(store, prov, "model", time.Second, agent.RetryPolicy{MaxAttempts: 1})
	rt.SetContextBudget(32000, 4000, 24, 120)
	toolExec := stubToolExecutor{
		execute: func(context.Context, string, agent.ToolCall) (any, error) {
			return strings.Repeat("A", 2000), nil
		},
		defs: []types.Tool{{
			Type:     "function",
			Function: types.ToolFunction{Name: "workspace.read", Parameters: mustRawJSON(t, `{"type":"object"}`)},
		}},
	}
	_, err := rt.Run(context.Background(), agent.RunRequest{
		PeerID:       "peer",
		Scope:        agent.ScopeMain,
		Messages:     []types.Message{{Role: "user", Content: "read file"}},
		ToolExecutor: toolExec,
		MaxSteps:     2,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !strings.Contains(seenToolMsg, `"truncated":true`) {
		t.Fatalf("expected truncated tool payload, got %q", seenToolMsg)
	}
	if len(seenToolMsg) > 300 {
		t.Fatalf("expected compact tool payload, got %d chars", len(seenToolMsg))
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

func TestRuntime_InjectsSkillMessagesIntoAgentRuns(t *testing.T) {
	store := session.New(20)
	workspaceRoot := t.TempDir()
	projectRoot := t.TempDir()
	cfg := config.Default()
	cfg.WorkspaceRoot = workspaceRoot
	if err := os.MkdirAll(filepath.Join(workspaceRoot, "skills", "security-review"), 0o755); err != nil {
		t.Fatalf("mkdir skill dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(workspaceRoot, "skills", "security-review", "SKILL.md"), []byte(`---
id: security-review
name: Security Review
---
Check auth, secrets, and input validation.`), 0o600); err != nil {
		t.Fatalf("write skill: %v", err)
	}
	standingStore, err := standing.NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("standing.NewStore: %v", err)
	}
	standingMgr := standing.NewManager(standingStore, workspaceRoot)
	if _, err := standingStore.SaveDocument(&standing.Document{PeerID: "peer", DefaultProfile: "ops", Profiles: map[string]standing.Profile{"ops": {SkillsAllow: []string{"security-review"}}}}); err != nil {
		t.Fatalf("SaveDocument: %v", err)
	}
	var captured *types.ChatRequest
	prov := &stubProvider{
		complete: func(_ context.Context, req *types.ChatRequest) (*types.ChatResponse, error) {
			captured = req
			return &types.ChatResponse{Choices: []types.ChatChoice{{Message: types.Message{Role: "assistant", Content: "ok"}}}}, nil
		},
	}
	rt := agent.NewRuntime(store, prov, "model", time.Second, agent.RetryPolicy{MaxAttempts: 1})
	rt.SetStandingOrders(standingMgr)
	rt.SetSkillManager(skills.NewManager(cfg, projectRoot))
	_, err = rt.Run(context.Background(), agent.RunRequest{
		PeerID:        "peer",
		Scope:         agent.ScopeMain,
		ActiveProfile: "ops",
		Messages:      []types.Message{{Role: "user", Content: "review this auth change"}},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if captured == nil {
		t.Fatal("expected captured request")
	}
	found := false
	for _, msg := range captured.Messages {
		if msg.Role == "system" && strings.Contains(msg.Content, "Security Review") && strings.Contains(msg.Content, "input validation") {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected injected skill message, got %#v", captured.Messages)
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

func TestRuntime_QueuesMemoryCandidatesAndArchivesTurnSummaries(t *testing.T) {
	store := session.New(20)
	memStore, err := memory.New(filepath.Join(t.TempDir(), "memory.db"), nil)
	if err != nil {
		t.Fatalf("memory.New: %v", err)
	}
	t.Cleanup(func() { _ = memStore.Close() })

	prov := &stubProvider{
		complete: func(_ context.Context, req *types.ChatRequest) (*types.ChatResponse, error) {
			if len(req.Messages) > 0 && req.Messages[0].Role == "system" && strings.Contains(req.Messages[0].Content, "reviewed memory candidates") {
				return &types.ChatResponse{
					Choices: []types.ChatChoice{{Message: types.Message{Role: "assistant", Content: `{"candidates":[{"content":"User prefers Tuesday deployment windows.","category":"preferences","tags":["deploy"],"retention_class":"pinned"}]}`}}},
				}, nil
			}
			return &types.ChatResponse{
				Choices: []types.ChatChoice{{Message: types.Message{Role: "assistant", Content: "I'll keep that in mind."}}},
			}, nil
		},
	}
	rt := agent.NewRuntime(store, prov, "model", time.Second, agent.RetryPolicy{MaxAttempts: 1})
	rt.EnableMemory(memStore, true, 3)

	_, err = rt.Run(context.Background(), agent.RunRequest{
		PeerID:   "peer",
		Scope:    agent.ScopeMain,
		Messages: []types.Message{{Role: "user", Content: "Remember that I prefer Tuesday deployment windows for production changes."}},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	candidates, err := memStore.ListCandidates(context.Background(), "peer", 10, memory.CandidateStatusPending)
	if err != nil {
		t.Fatalf("ListCandidates: %v", err)
	}
	if len(candidates) != 1 {
		t.Fatalf("pending candidates = %d, want 1", len(candidates))
	}
	if candidates[0].Content != "User prefers Tuesday deployment windows." {
		t.Fatalf("candidate content = %q", candidates[0].Content)
	}
	if candidates[0].RetentionClass != memory.RetentionClassPinned {
		t.Fatalf("candidate retention = %q, want pinned", candidates[0].RetentionClass)
	}
	if candidates[0].CaptureKind != memory.CandidateCaptureAutoTurnExtract {
		t.Fatalf("candidate capture kind = %q, want %q", candidates[0].CaptureKind, memory.CandidateCaptureAutoTurnExtract)
	}
	if candidates[0].SourceSessionKey != "peer::main" {
		t.Fatalf("candidate source session = %q, want peer::main", candidates[0].SourceSessionKey)
	}
	if !strings.Contains(candidates[0].SourceExcerpt, "Tuesday deployment windows") {
		t.Fatalf("candidate source excerpt = %q, want deployment excerpt", candidates[0].SourceExcerpt)
	}

	chunks, err := memStore.List(context.Background(), "peer", 10)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(chunks) != 1 {
		t.Fatalf("chunks = %d, want 1 archived summary", len(chunks))
	}
	if chunks[0].RetentionClass != memory.RetentionClassArchive {
		t.Fatalf("summary retention = %q, want archive", chunks[0].RetentionClass)
	}
	if chunks[0].ExposurePolicy != memory.ExposurePolicySearchOnly {
		t.Fatalf("summary exposure = %q, want search_only", chunks[0].ExposurePolicy)
	}
	injected, err := memStore.SearchForInjection(context.Background(), "peer", "Tuesday deployment windows", 10)
	if err != nil {
		t.Fatalf("SearchForInjection: %v", err)
	}
	if len(injected) != 0 {
		t.Fatalf("unexpected injectable memory hits: %d", len(injected))
	}
}

func TestRuntime_ExtractsEntitiesFromTurnSummaries(t *testing.T) {
	store := session.New(20)
	memStore, err := memory.New(filepath.Join(t.TempDir(), "memory.db"), nil)
	if err != nil {
		t.Fatalf("memory.New: %v", err)
	}
	t.Cleanup(func() { _ = memStore.Close() })

	prov := &stubProvider{
		complete: func(_ context.Context, req *types.ChatRequest) (*types.ChatResponse, error) {
			if len(req.Messages) > 0 && req.Messages[0].Role == "system" && strings.Contains(req.Messages[0].Content, "reviewed memory candidates") {
				return &types.ChatResponse{
					Choices: []types.ChatChoice{{Message: types.Message{Role: "assistant", Content: `{"candidates":[]}`}}},
				}, nil
			}
			if len(req.Messages) > 0 && req.Messages[0].Role == "system" && strings.Contains(req.Messages[0].Content, "durable entities") {
				return &types.ChatResponse{
					Choices: []types.ChatChoice{{Message: types.Message{Role: "assistant", Content: `{"entities":[{"kind":"project","name":"Borealis Trip","aliases":["summer trip"],"notes":"Ongoing travel planning project"},{"kind":"person","name":"Alice Example","notes":"Coordinates the trip"}]}`}}},
				}, nil
			}
			return &types.ChatResponse{
				Choices: []types.ChatChoice{{Message: types.Message{Role: "assistant", Content: "I'll keep that in mind."}}},
			}, nil
		},
	}
	rt := agent.NewRuntime(store, prov, "model", time.Second, agent.RetryPolicy{MaxAttempts: 1})
	rt.EnableMemory(memStore, true, 3)

	_, err = rt.Run(context.Background(), agent.RunRequest{
		PeerID:   "peer",
		Scope:    agent.ScopeMain,
		Messages: []types.Message{{Role: "user", Content: "Remember that Alice Example is coordinating the Borealis Trip this quarter."}},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	entities, err := memStore.ListEntities(context.Background(), "peer", "", 10)
	if err != nil {
		t.Fatalf("ListEntities: %v", err)
	}
	if len(entities) != 2 {
		t.Fatalf("entities = %d, want 2", len(entities))
	}
	names := []string{entities[0].Name, entities[1].Name}
	sort.Strings(names)
	if names[0] != "Alice Example" || names[1] != "Borealis Trip" {
		t.Fatalf("entity names = %#v", names)
	}
	projectResults, err := memStore.SearchEntities(context.Background(), "peer", "Borealis", 10)
	if err != nil {
		t.Fatalf("SearchEntities: %v", err)
	}
	if len(projectResults) != 1 {
		t.Fatalf("project search results = %#v", projectResults)
	}
	graph, err := memStore.GetEntityGraph(context.Background(), "peer", projectResults[0].ID)
	if err != nil {
		t.Fatalf("GetEntityGraph: %v", err)
	}
	if len(graph.LinkedChunks) != 1 {
		t.Fatalf("linked chunks = %#v, want archived summary link", graph.LinkedChunks)
	}
	if graph.LinkedChunks[0].Category != "conversation" {
		t.Fatalf("linked chunk category = %q, want conversation", graph.LinkedChunks[0].Category)
	}
	if graph.LinkedChunks[0].RetentionClass != memory.RetentionClassArchive {
		t.Fatalf("linked chunk retention = %q, want archive", graph.LinkedChunks[0].RetentionClass)
	}
	if !strings.Contains(graph.Entity.Notes, "Ongoing travel planning project") {
		t.Fatalf("project notes = %q", graph.Entity.Notes)
	}
	if len(graph.Entity.Aliases) != 1 || graph.Entity.Aliases[0] != "summer trip" {
		t.Fatalf("project aliases = %#v", graph.Entity.Aliases)
	}
	personResults, err := memStore.SearchEntities(context.Background(), "peer", "Alice", 10)
	if err != nil {
		t.Fatalf("SearchEntities person: %v", err)
	}
	if len(personResults) != 1 {
		t.Fatalf("person search results = %#v", personResults)
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

func TestRuntime_RepairsLegacyXMLToolHistoryForReplay(t *testing.T) {
	store := session.New(20)
	sessionKey := "peer::main"
	store.Append(sessionKey,
		types.Message{Role: "user", Content: "fetch it"},
		types.Message{Role: "assistant", Content: `<tool_call>{"name":"web_fetch","arguments":{"url":"https://example.com"}}</tool_call>`},
		types.Message{Role: "tool", ToolCallID: "legacy-call", Content: `[tool_result web_fetch]\n{"ok":true,"result":{"status":200}}`},
	)
	var captured *types.ChatRequest
	prov := &stubProvider{complete: func(_ context.Context, req *types.ChatRequest) (*types.ChatResponse, error) {
		captured = req
		return &types.ChatResponse{Choices: []types.ChatChoice{{Message: types.Message{Role: "assistant", Content: "done"}}}}, nil
	}}
	rt := agent.NewRuntime(store, prov, "model", time.Second, agent.RetryPolicy{MaxAttempts: 1})
	_, err := rt.Run(context.Background(), agent.RunRequest{
		PeerID:   "peer",
		Scope:    agent.ScopeMain,
		Messages: []types.Message{{Role: "user", Content: "continue"}},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if captured == nil {
		t.Fatal("expected captured request")
	}
	foundAssistant := false
	foundTool := false
	for i := range captured.Messages {
		msg := captured.Messages[i]
		if msg.Role == "assistant" && len(msg.ToolCalls) == 1 && msg.ToolCalls[0].Function.Name == "web_fetch" {
			foundAssistant = true
			if msg.ToolCalls[0].ID != "legacy-call" {
				t.Fatalf("repaired tool call id = %q, want legacy-call", msg.ToolCalls[0].ID)
			}
			if i+1 >= len(captured.Messages) || captured.Messages[i+1].Role != "tool" || captured.Messages[i+1].ToolCallID != "legacy-call" {
				t.Fatalf("expected repaired tool message immediately after assistant, got %#v", captured.Messages)
			}
			foundTool = true
			break
		}
	}
	if !foundAssistant || !foundTool {
		t.Fatalf("expected repaired assistant/tool pair in replay, got %#v", captured.Messages)
	}
}

func TestRuntime_RepairsMissingToolResultWithSyntheticError(t *testing.T) {
	store := session.New(20)
	sessionKey := "peer::main"
	store.Append(sessionKey,
		types.Message{Role: "user", Content: "write it"},
		types.Message{Role: "assistant", ToolCalls: []types.ToolCall{{
			ID:       "call-1",
			Type:     "function",
			Function: types.ToolCallFunctionRef{Name: "workspace.write", Arguments: `{}`},
		}}},
	)
	var captured *types.ChatRequest
	prov := &stubProvider{complete: func(_ context.Context, req *types.ChatRequest) (*types.ChatResponse, error) {
		captured = req
		return &types.ChatResponse{Choices: []types.ChatChoice{{Message: types.Message{Role: "assistant", Content: "done"}}}}, nil
	}}
	rt := agent.NewRuntime(store, prov, "model", time.Second, agent.RetryPolicy{MaxAttempts: 1})
	_, err := rt.Run(context.Background(), agent.RunRequest{
		PeerID:   "peer",
		Scope:    agent.ScopeMain,
		Messages: []types.Message{{Role: "user", Content: "continue"}},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if captured == nil {
		t.Fatal("expected captured request")
	}
	foundSynthetic := false
	for i := 0; i < len(captured.Messages)-1; i++ {
		if captured.Messages[i].Role == "assistant" && len(captured.Messages[i].ToolCalls) == 1 && captured.Messages[i].ToolCalls[0].ID == "call-1" {
			toolMsg := captured.Messages[i+1]
			if toolMsg.Role != "tool" || toolMsg.ToolCallID != "call-1" {
				t.Fatalf("expected synthetic tool result after dangling call, got %#v", captured.Messages)
			}
			if !strings.Contains(toolMsg.Content, `"ok":false`) || !strings.Contains(toolMsg.Content, "synthetic repair inserted") {
				t.Fatalf("expected synthetic error tool result, got %q", toolMsg.Content)
			}
			foundSynthetic = true
			break
		}
	}
	if !foundSynthetic {
		t.Fatalf("expected synthetic repaired tool result, got %#v", captured.Messages)
	}
}

func TestRuntime_DropsUnrecoverableOrphanToolResultFromReplay(t *testing.T) {
	store := session.New(20)
	sessionKey := "peer::main"
	store.Append(sessionKey,
		types.Message{Role: "user", Content: "hello"},
		types.Message{Role: "tool", ToolCallID: "orphan-1", Content: `[tool_result mystery]\n{"ok":true}`},
	)
	var captured *types.ChatRequest
	prov := &stubProvider{complete: func(_ context.Context, req *types.ChatRequest) (*types.ChatResponse, error) {
		captured = req
		return &types.ChatResponse{Choices: []types.ChatChoice{{Message: types.Message{Role: "assistant", Content: "done"}}}}, nil
	}}
	rt := agent.NewRuntime(store, prov, "model", time.Second, agent.RetryPolicy{MaxAttempts: 1})
	_, err := rt.Run(context.Background(), agent.RunRequest{
		PeerID:   "peer",
		Scope:    agent.ScopeMain,
		Messages: []types.Message{{Role: "user", Content: "continue"}},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if captured == nil {
		t.Fatal("expected captured request")
	}
	for _, msg := range captured.Messages {
		if msg.Role == "tool" && msg.ToolCallID == "orphan-1" {
			t.Fatalf("expected orphan tool result to be dropped from replay, got %#v", captured.Messages)
		}
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
	assistant := seen[1].Messages[len(seen[1].Messages)-2]
	last := seen[1].Messages[len(seen[1].Messages)-1]
	if assistant.Role != "assistant" {
		t.Fatalf("expected assistant tool-call message, got %#v", assistant)
	}
	if len(assistant.ToolCalls) != 1 {
		t.Fatalf("expected one assistant tool call, got %#v", assistant.ToolCalls)
	}
	if assistant.ToolCalls[0].ID == "" {
		t.Fatal("expected synthesized tool call id")
	}
	if last.Role != "tool" {
		t.Fatalf("expected tool-role message for tool result, got %#v", last)
	}
	if last.ToolCallID != assistant.ToolCalls[0].ID {
		t.Fatalf("expected tool result to reference assistant tool call id, got assistant=%q tool=%q", assistant.ToolCalls[0].ID, last.ToolCallID)
	}
	if !strings.Contains(last.Content, "[REDACTED]") {
		t.Fatalf("expected redacted tool result, got %q", last.Content)
	}
}

func TestRuntime_SuppressesSilentReplyToken(t *testing.T) {
	store := session.New(20)
	prov := &stubProvider{
		complete: func(_ context.Context, req *types.ChatRequest) (*types.ChatResponse, error) {
			return &types.ChatResponse{Choices: []types.ChatChoice{{Message: types.Message{Role: "assistant", Content: agent.SilentReplyToken}}}}, nil
		},
	}
	rt := agent.NewRuntime(store, prov, "model", time.Second, agent.RetryPolicy{MaxAttempts: 1})
	res, err := rt.Run(context.Background(), agent.RunRequest{
		PeerID:   "peer",
		Scope:    agent.ScopeMain,
		Messages: []types.Message{{Role: "user", Content: "ping"}},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !res.SuppressedReply {
		t.Fatal("expected suppressed reply")
	}
	if res.AssistantText != "" {
		t.Fatalf("expected empty assistant text, got %q", res.AssistantText)
	}
	if res.Response == nil || len(res.Response.Choices) == 0 || res.Response.Choices[0].Message.Content != "" {
		t.Fatalf("expected blank response content, got %#v", res.Response)
	}
	history := store.Get("peer::main").History()
	if len(history) != 1 || history[0].Role != "user" {
		t.Fatalf("expected only the user message to persist, got %#v", history)
	}
}

func TestRuntime_DisablesNativeToolsWhenProviderDoesNotSupportThem(t *testing.T) {
	store := session.New(20)
	var seen []*types.ChatRequest
	callCount := 0
	prov := &stubProvider{
		caps: types.ProviderCapabilities{Name: "stub", SupportsStreaming: true, SupportsNativeTools: false},
		complete: func(_ context.Context, req *types.ChatRequest) (*types.ChatResponse, error) {
			seen = append(seen, req)
			callCount++
			if callCount == 1 {
				if len(req.Tools) != 0 {
					t.Fatalf("expected no native tools in request, got %#v", req.Tools)
				}
				return &types.ChatResponse{Choices: []types.ChatChoice{{Message: types.Message{Role: "assistant", Content: `<tool_call>{"name":"time.now","arguments":{}}</tool_call>`}}}}, nil
			}
			return &types.ChatResponse{Choices: []types.ChatChoice{{Message: types.Message{Role: "assistant", Content: "done"}}}}, nil
		},
	}
	rt := agent.NewRuntime(store, prov, "model", time.Second, agent.RetryPolicy{MaxAttempts: 1})
	_, err := rt.Run(context.Background(), agent.RunRequest{
		PeerID:   "peer",
		Scope:    agent.ScopeMain,
		MaxSteps: 2,
		Messages: []types.Message{{Role: "user", Content: "what time is it?"}},
		ToolExecutor: stubToolExecutor{execute: func(_ context.Context, _ string, call agent.ToolCall) (any, error) {
			return map[string]string{"utc": "2026-04-24T00:00:00Z"}, nil
		}},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(seen) != 2 {
		t.Fatalf("expected 2 provider calls, got %d", len(seen))
	}
}

func TestRuntime_XMLToolExecutionUsesLiveRunContext(t *testing.T) {
	store := session.New(20)
	callCount := 0
	prov := &stubProvider{
		complete: func(_ context.Context, req *types.ChatRequest) (*types.ChatResponse, error) {
			callCount++
			if callCount == 1 {
				return &types.ChatResponse{Choices: []types.ChatChoice{{Message: types.Message{Role: "assistant", Content: `<tool_call>{"name":"time.now","arguments":{}}</tool_call>`}}}}, nil
			}
			return &types.ChatResponse{Choices: []types.ChatChoice{{Message: types.Message{Role: "assistant", Content: "done"}}}}, nil
		},
	}
	rt := agent.NewRuntime(store, prov, "model", time.Second, agent.RetryPolicy{MaxAttempts: 1})
	_, err := rt.Run(context.Background(), agent.RunRequest{
		PeerID:   "peer",
		Scope:    agent.ScopeMain,
		MaxSteps: 2,
		Messages: []types.Message{{Role: "user", Content: "what time is it?"}},
		ToolExecutor: stubToolExecutor{execute: func(ctx context.Context, _ string, call agent.ToolCall) (any, error) {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			toolCtx, ok := agent.ToolRunContextFromContext(ctx)
			if !ok {
				t.Fatal("expected tool run context on XML tool execution")
			}
			if toolCtx.SessionKey == "" {
				t.Fatal("expected non-empty session key on XML tool execution")
			}
			if call.Name != "time.now" {
				t.Fatalf("tool name = %q", call.Name)
			}
			return map[string]string{"utc": "2026-04-29T00:00:00Z"}, nil
		}},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if callCount != 2 {
		t.Fatalf("expected 2 provider calls, got %d", callCount)
	}
}

func TestRuntime_NativeToolExecutionNormalizesMissingArgsAndIDs(t *testing.T) {
	store := session.New(20)
	var seen []*types.ChatRequest
	callCount := 0
	prov := &stubProvider{
		complete: func(_ context.Context, req *types.ChatRequest) (*types.ChatResponse, error) {
			seen = append(seen, req)
			callCount++
			if callCount == 1 {
				return &types.ChatResponse{Choices: []types.ChatChoice{{Message: types.Message{Role: "assistant", ToolCalls: []types.ToolCall{{
					Type: "function",
					Function: types.ToolCallFunctionRef{
						Name: "time.now",
					},
				}}}}}}, nil
			}
			return &types.ChatResponse{Choices: []types.ChatChoice{{Message: types.Message{Role: "assistant", Content: "done"}}}}, nil
		},
	}
	rt := agent.NewRuntime(store, prov, "model", time.Second, agent.RetryPolicy{MaxAttempts: 1})
	_, err := rt.Run(context.Background(), agent.RunRequest{
		PeerID:   "peer",
		Scope:    agent.ScopeMain,
		MaxSteps: 2,
		Messages: []types.Message{{Role: "user", Content: "what time is it?"}},
		ToolExecutor: stubToolExecutor{execute: func(ctx context.Context, _ string, call agent.ToolCall) (any, error) {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			if call.ID == "" {
				t.Fatal("expected synthesized tool call id")
			}
			if string(call.Arguments) != `{}` {
				t.Fatalf("tool arguments = %s, want {}", string(call.Arguments))
			}
			return map[string]string{"utc": "2026-04-29T00:00:00Z"}, nil
		}},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(seen) != 2 {
		t.Fatalf("expected 2 provider calls, got %d", len(seen))
	}
	assistant := seen[1].Messages[len(seen[1].Messages)-2]
	tool := seen[1].Messages[len(seen[1].Messages)-1]
	if len(assistant.ToolCalls) != 1 || assistant.ToolCalls[0].ID == "" {
		t.Fatalf("expected assistant tool call with synthesized id, got %#v", assistant)
	}
	if tool.ToolCallID != assistant.ToolCalls[0].ID {
		t.Fatalf("tool result id = %q, want %q", tool.ToolCallID, assistant.ToolCalls[0].ID)
	}
	if !strings.Contains(tool.Content, `"ok":true`) {
		t.Fatalf("expected successful tool result, got %q", tool.Content)
	}
}

func TestRuntime_NativeToolHookFailureReturnsToolErrorToModel(t *testing.T) {
	store := session.New(20)
	var seen []*types.ChatRequest
	callCount := 0
	prov := &stubProvider{
		complete: func(_ context.Context, req *types.ChatRequest) (*types.ChatResponse, error) {
			seen = append(seen, req)
			callCount++
			if callCount == 1 {
				return &types.ChatResponse{Choices: []types.ChatChoice{{Message: types.Message{Role: "assistant", ToolCalls: []types.ToolCall{{
					ID:   "call-1",
					Type: "function",
					Function: types.ToolCallFunctionRef{
						Name:      "time.now",
						Arguments: `{}`,
					},
				}}}}}}, nil
			}
			return &types.ChatResponse{Choices: []types.ChatChoice{{Message: types.Message{Role: "assistant", Content: "done"}}}}, nil
		},
	}
	rt := agent.NewRuntime(store, prov, "model", time.Second, agent.RetryPolicy{MaxAttempts: 1})
	hooks := ops.NewManager(time.Second, true)
	hooks.Register(ops.HookBeforeToolCall, 100, func(_ context.Context, ev ops.Event) error {
		return errors.New("approval denied")
	})
	rt.SetHooks(hooks)
	_, err := rt.Run(context.Background(), agent.RunRequest{
		PeerID:   "peer",
		Scope:    agent.ScopeMain,
		MaxSteps: 2,
		Messages: []types.Message{{Role: "user", Content: "what time is it?"}},
		ToolExecutor: stubToolExecutor{execute: func(context.Context, string, agent.ToolCall) (any, error) {
			t.Fatal("tool should not execute when before-tool hook rejects it")
			return nil, nil
		}},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(seen) != 2 {
		t.Fatalf("expected 2 provider calls, got %d", len(seen))
	}
	assistant := seen[1].Messages[len(seen[1].Messages)-2]
	tool := seen[1].Messages[len(seen[1].Messages)-1]
	if len(assistant.ToolCalls) != 1 || assistant.ToolCalls[0].ID != "call-1" {
		t.Fatalf("expected assistant tool call to survive hook failure, got %#v", assistant)
	}
	if tool.ToolCallID != "call-1" {
		t.Fatalf("tool result id = %q, want call-1", tool.ToolCallID)
	}
	if !strings.Contains(tool.Content, `"ok":false`) || !strings.Contains(tool.Content, "approval denied") {
		t.Fatalf("expected tool error to flow back to model, got %q", tool.Content)
	}
	if len(tool.Content) == 0 {
		t.Fatal("expected non-empty synthetic tool result")
	}
}

func TestRuntime_PendingMutatingToolFailurePersistsUntilMatchingSuccess(t *testing.T) {
	store := session.New(20)
	callCount := 0
	toolCalls := 0
	prov := &stubProvider{
		complete: func(_ context.Context, req *types.ChatRequest) (*types.ChatResponse, error) {
			callCount++
			switch callCount {
			case 1, 2:
				return &types.ChatResponse{Choices: []types.ChatChoice{{Message: types.Message{Role: "assistant", ToolCalls: []types.ToolCall{{
					ID:       "call-1",
					Type:     "function",
					Function: types.ToolCallFunctionRef{Name: "workspace.write", Arguments: `{}`},
				}}}}}}, nil
			default:
				return &types.ChatResponse{Choices: []types.ChatChoice{{Message: types.Message{Role: "assistant", Content: "done"}}}}, nil
			}
		},
	}
	rt := agent.NewRuntime(store, prov, "model", time.Second, agent.RetryPolicy{MaxAttempts: 1})
	res, err := rt.Run(context.Background(), agent.RunRequest{
		PeerID:   "peer",
		Scope:    agent.ScopeMain,
		MaxSteps: 3,
		Messages: []types.Message{{Role: "user", Content: "update file"}},
		ToolExecutor: stubToolExecutor{
			mutates: func(_ string, name string) bool { return name == "workspace.write" },
			execute: func(ctx context.Context, _ string, call agent.ToolCall) (any, error) {
				toolCalls++
				if toolCalls == 1 {
					return nil, errors.New("disk full")
				}
				return map[string]any{"ok": true}, nil
			},
		},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.PendingMutatingToolFailure != nil {
		t.Fatalf("expected matching mutating success to clear pending failure, got %#v", res.PendingMutatingToolFailure)
	}
}

func TestRuntime_PendingMutatingToolFailureSurvivesUnrelatedSuccess(t *testing.T) {
	store := session.New(20)
	callCount := 0
	toolCalls := 0
	prov := &stubProvider{
		complete: func(_ context.Context, req *types.ChatRequest) (*types.ChatResponse, error) {
			callCount++
			switch callCount {
			case 1:
				return &types.ChatResponse{Choices: []types.ChatChoice{{Message: types.Message{Role: "assistant", ToolCalls: []types.ToolCall{{
					ID:       "call-1",
					Type:     "function",
					Function: types.ToolCallFunctionRef{Name: "workspace.write", Arguments: `{}`},
				}}}}}}, nil
			case 2:
				return &types.ChatResponse{Choices: []types.ChatChoice{{Message: types.Message{Role: "assistant", ToolCalls: []types.ToolCall{{
					ID:       "call-2",
					Type:     "function",
					Function: types.ToolCallFunctionRef{Name: "time.now", Arguments: `{}`},
				}}}}}}, nil
			default:
				return &types.ChatResponse{Choices: []types.ChatChoice{{Message: types.Message{Role: "assistant", Content: "done"}}}}, nil
			}
		},
	}
	rt := agent.NewRuntime(store, prov, "model", time.Second, agent.RetryPolicy{MaxAttempts: 1})
	res, err := rt.Run(context.Background(), agent.RunRequest{
		PeerID:   "peer",
		Scope:    agent.ScopeMain,
		MaxSteps: 3,
		Messages: []types.Message{{Role: "user", Content: "update file then check time"}},
		ToolExecutor: stubToolExecutor{
			mutates: func(_ string, name string) bool { return name == "workspace.write" },
			execute: func(ctx context.Context, _ string, call agent.ToolCall) (any, error) {
				toolCalls++
				if toolCalls == 1 {
					return nil, errors.New("disk full")
				}
				return map[string]any{"utc": "2026-04-29T00:00:00Z"}, nil
			},
		},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.PendingMutatingToolFailure == nil {
		t.Fatal("expected unresolved mutating failure to survive unrelated success")
	}
	if res.PendingMutatingToolFailure.ToolName != "workspace.write" {
		t.Fatalf("pending tool name = %q", res.PendingMutatingToolFailure.ToolName)
	}
	if !res.PendingMutatingToolFailure.SideEffectUnknown {
		t.Fatalf("expected side effect unknown to stay true, got %#v", res.PendingMutatingToolFailure)
	}
}

func TestRuntime_NativeToolExecutionUsesLiveRunContext(t *testing.T) {
	store := session.New(20)
	callCount := 0
	prov := &stubProvider{
		complete: func(_ context.Context, req *types.ChatRequest) (*types.ChatResponse, error) {
			callCount++
			if callCount == 1 {
				return &types.ChatResponse{Choices: []types.ChatChoice{{Message: types.Message{Role: "assistant", ToolCalls: []types.ToolCall{{
					ID:   "call-1",
					Type: "function",
					Function: types.ToolCallFunctionRef{
						Name:      "time.now",
						Arguments: `{}`,
					},
				}}}}}}, nil
			}
			return &types.ChatResponse{Choices: []types.ChatChoice{{Message: types.Message{Role: "assistant", Content: "done"}}}}, nil
		},
	}
	rt := agent.NewRuntime(store, prov, "model", time.Second, agent.RetryPolicy{MaxAttempts: 1})
	_, err := rt.Run(context.Background(), agent.RunRequest{
		PeerID:   "peer",
		Scope:    agent.ScopeMain,
		MaxSteps: 2,
		Messages: []types.Message{{Role: "user", Content: "what time is it?"}},
		ToolExecutor: stubToolExecutor{execute: func(ctx context.Context, _ string, call agent.ToolCall) (any, error) {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			toolCtx, ok := agent.ToolRunContextFromContext(ctx)
			if !ok {
				t.Fatal("expected tool run context on native tool execution")
			}
			if toolCtx.SessionKey == "" {
				t.Fatal("expected non-empty session key on native tool execution")
			}
			if call.Name != "time.now" {
				t.Fatalf("tool name = %q", call.Name)
			}
			return map[string]string{"utc": "2026-04-29T00:00:00Z"}, nil
		}},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if callCount != 2 {
		t.Fatalf("expected 2 provider calls, got %d", callCount)
	}
}

func TestRuntime_EventsIncludeTimestampsAndDurations(t *testing.T) {
	store := session.New(20)
	callCount := 0
	prov := &stubProvider{
		complete: func(_ context.Context, req *types.ChatRequest) (*types.ChatResponse, error) {
			callCount++
			if callCount == 1 {
				return &types.ChatResponse{Choices: []types.ChatChoice{{Message: types.Message{Role: "assistant", ToolCalls: []types.ToolCall{{
					ID:   "call-1",
					Type: "function",
					Function: types.ToolCallFunctionRef{
						Name:      "time.now",
						Arguments: `{}`,
					},
				}}}}}}, nil
			}
			return &types.ChatResponse{Choices: []types.ChatChoice{{Message: types.Message{Role: "assistant", Content: "done"}}}}, nil
		},
	}
	rt := agent.NewRuntime(store, prov, "model", time.Second, agent.RetryPolicy{MaxAttempts: 1})
	res, err := rt.Run(context.Background(), agent.RunRequest{
		PeerID:   "peer",
		Scope:    agent.ScopeMain,
		MaxSteps: 2,
		Messages: []types.Message{{Role: "user", Content: "what time is it?"}},
		ToolExecutor: stubToolExecutor{execute: func(ctx context.Context, _ string, call agent.ToolCall) (any, error) {
			if call.Name != "time.now" {
				t.Fatalf("tool name = %q", call.Name)
			}
			time.Sleep(5 * time.Millisecond)
			return map[string]string{"utc": "2026-04-29T00:00:00Z"}, nil
		}},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(res.Events) == 0 {
		t.Fatal("expected runtime events")
	}
	var sawStepFinish bool
	var sawToolResult bool
	var sawRunFinish bool
	for _, event := range res.Events {
		if event.At.IsZero() {
			t.Fatalf("expected timestamp on event %#v", event)
		}
		switch event.Kind {
		case agent.EventStepFinish:
			sawStepFinish = true
			if event.DurationMs <= 0 {
				t.Fatalf("expected positive step duration, got %d", event.DurationMs)
			}
		case agent.EventToolResult:
			sawToolResult = true
			if event.DurationMs <= 0 {
				t.Fatalf("expected positive tool duration, got %d", event.DurationMs)
			}
		case agent.EventRunFinish:
			sawRunFinish = true
			if event.DurationMs <= 0 {
				t.Fatalf("expected positive run duration, got %d", event.DurationMs)
			}
		}
	}
	if !sawStepFinish {
		t.Fatal("expected step finish event")
	}
	if !sawToolResult {
		t.Fatal("expected tool result event")
	}
	if !sawRunFinish {
		t.Fatal("expected run finish event")
	}
}

func TestRuntime_EmitsReasoningEvents(t *testing.T) {
	store := session.New(20)
	if err := store.SetPolicy("peer", session.SessionPolicy{ThinkLevel: "medium", ReasoningVisibility: "full"}); err != nil {
		t.Fatalf("SetPolicy: %v", err)
	}
	prov := &stubProvider{
		complete: func(ctx context.Context, req *types.ChatRequest) (*types.ChatResponse, error) {
			if req.ReasoningEffort != "medium" {
				t.Fatalf("ReasoningEffort = %q, want medium", req.ReasoningEffort)
			}
			if req.ReasoningBudget <= 0 {
				t.Fatalf("expected positive ReasoningBudget, got %d", req.ReasoningBudget)
			}
			if req.ReasoningVisibility != "full" {
				t.Fatalf("ReasoningVisibility = %q, want full", req.ReasoningVisibility)
			}
			types.EmitReasoningEvent(ctx, types.ReasoningEvent{Kind: types.ReasoningEventDelta, Provider: "stub", Text: "first thought"})
			types.EmitReasoningEvent(ctx, types.ReasoningEvent{Kind: types.ReasoningEventSummary, Provider: "stub", Text: "final thought"})
			return &types.ChatResponse{Choices: []types.ChatChoice{{Message: types.Message{Role: "assistant", Content: "done"}}}}, nil
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
	var sawDelta bool
	var sawSummary bool
	for _, event := range res.Events {
		switch event.Kind {
		case agent.EventReasoningDelta:
			sawDelta = true
			if event.Content != "first thought" || event.Provider != "stub" {
				t.Fatalf("unexpected delta event: %#v", event)
			}
		case agent.EventReasoningSummary:
			sawSummary = true
			if event.Content != "final thought" || event.Provider != "stub" {
				t.Fatalf("unexpected summary event: %#v", event)
			}
		}
	}
	if !sawDelta {
		t.Fatal("expected reasoning delta event")
	}
	if !sawSummary {
		t.Fatal("expected reasoning summary event")
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

type probeStreamWriter struct {
	mu       sync.Mutex
	header   http.Header
	payloads []string
	seen     chan struct{}
	once     sync.Once
}

func newProbeStreamWriter() *probeStreamWriter {
	return &probeStreamWriter{header: make(http.Header), seen: make(chan struct{})}
}

func (w *probeStreamWriter) Header() http.Header { return w.header }
func (w *probeStreamWriter) WriteHeader(int)     {}
func (w *probeStreamWriter) Flush()              {}

func (w *probeStreamWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	w.payloads = append(w.payloads, string(p))
	w.mu.Unlock()
	w.once.Do(func() { close(w.seen) })
	return len(p), nil
}

func TestRuntime_QueueModesFollowupAndCollect(t *testing.T) {
	tests := []struct {
		name        string
		mode        string
		inject      []string
		wantLastMsg string
	}{
		{name: "followup", mode: agent.QueueModeFollowup, inject: []string{"follow up on the failure path"}, wantLastMsg: "follow up on the failure path"},
		{name: "collect", mode: agent.QueueModeCollect, inject: []string{"focus on auth", "also cover retries"}, wantLastMsg: "Collected steering updates:\n- focus on auth\n- also cover retries"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			store := session.New(20)
			if err := store.SetPolicy("peer", session.SessionPolicy{QueueMode: tc.mode}); err != nil {
				t.Fatalf("SetPolicy: %v", err)
			}

			calls := 0
			var seen []*types.ChatRequest
			var rt *agent.Runtime
			prov := &stubProvider{
				complete: func(_ context.Context, req *types.ChatRequest) (*types.ChatResponse, error) {
					seen = append(seen, req)
					calls++
					if calls == 1 {
						for _, note := range tc.inject {
							if err := rt.Steer("peer", note); err != nil {
								t.Fatalf("Steer: %v", err)
							}
						}
						return &types.ChatResponse{Choices: []types.ChatChoice{{Message: types.Message{Role: "assistant", Content: "first answer"}}}}, nil
					}
					last := req.Messages[len(req.Messages)-1].Content
					if last != tc.wantLastMsg {
						t.Fatalf("last message = %q, want %q", last, tc.wantLastMsg)
					}
					return &types.ChatResponse{Choices: []types.ChatChoice{{Message: types.Message{Role: "assistant", Content: "adjusted answer"}}}}, nil
				},
			}
			rt = agent.NewRuntime(store, prov, "model", time.Second, agent.RetryPolicy{MaxAttempts: 1})

			res, err := rt.Run(context.Background(), agent.RunRequest{
				PeerID:   "peer",
				Scope:    agent.ScopeMain,
				MaxSteps: 3,
				Messages: []types.Message{{Role: "user", Content: "hello"}},
			})
			if err != nil {
				t.Fatalf("Run: %v", err)
			}
			if res.AssistantText != "adjusted answer" {
				t.Fatalf("AssistantText = %q", res.AssistantText)
			}
			if len(seen) != 2 {
				t.Fatalf("expected 2 provider calls, got %d", len(seen))
			}
		})
	}
}

func TestRuntime_SteerInterruptsStreamingRun(t *testing.T) {
	store := session.New(20)
	if err := store.SetPolicy("peer", session.SessionPolicy{QueueMode: agent.QueueModeSteer}); err != nil {
		t.Fatalf("SetPolicy: %v", err)
	}

	var (
		rt   *agent.Runtime
		mu   sync.Mutex
		seen []*types.ChatRequest
	)
	prov := &stubProvider{
		stream: func(ctx context.Context, req *types.ChatRequest, w http.ResponseWriter) (string, error) {
			mu.Lock()
			seen = append(seen, req)
			callNum := len(seen)
			mu.Unlock()

			if callNum == 1 {
				_, _ = w.Write([]byte("data: {\"id\":\"1\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"model\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"partial \"}}]}\n\n"))
				if flusher, ok := w.(http.Flusher); ok {
					flusher.Flush()
				}
				<-ctx.Done()
				return "", ctx.Err()
			}
			last := req.Messages[len(req.Messages)-1].Content
			if last != "take a different angle" {
				t.Fatalf("last streamed steer message = %q", last)
			}
			_, _ = w.Write([]byte("data: {\"id\":\"2\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"model\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"redirected answer\"}}]}\n\n"))
			_, _ = w.Write([]byte("data: [DONE]\n\n"))
			if flusher, ok := w.(http.Flusher); ok {
				flusher.Flush()
			}
			return "redirected answer", nil
		},
	}
	rt = agent.NewRuntime(store, prov, "model", time.Second, agent.RetryPolicy{MaxAttempts: 1})

	writer := newProbeStreamWriter()
	type runResult struct {
		res *agent.Result
		err error
	}
	done := make(chan runResult, 1)
	go func() {
		res, err := rt.RunStream(context.Background(), agent.RunRequest{
			PeerID:   "peer",
			Scope:    agent.ScopeMain,
			Messages: []types.Message{{Role: "user", Content: "hello"}},
			Stream:   true,
		}, writer)
		done <- runResult{res: res, err: err}
	}()

	select {
	case <-writer.seen:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for first streamed chunk")
	}
	if err := rt.Steer("peer", "take a different angle"); err != nil {
		t.Fatalf("Steer: %v", err)
	}

	select {
	case out := <-done:
		if out.err != nil {
			t.Fatalf("RunStream: %v", out.err)
		}
		if out.res == nil || out.res.AssistantText != "redirected answer" {
			t.Fatalf("unexpected result: %#v", out.res)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for streaming run")
	}

	mu.Lock()
	defer mu.Unlock()
	if len(seen) != 2 {
		t.Fatalf("expected 2 streaming attempts, got %d", len(seen))
	}
}
