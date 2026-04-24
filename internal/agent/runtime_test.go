package agent_test

import (
	"context"
	"errors"
	"net/http"
	"path/filepath"
	"sort"
	"strings"
	"sync"
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
	caps     types.ProviderCapabilities
}

type stubToolExecutor struct {
	execute func(ctx context.Context, peerID string, call agent.ToolCall) (any, error)
}

func (s stubToolExecutor) ToolPromptForRun(peerID, sessionKey, activeProfile string) string {
	_, _, _ = peerID, sessionKey, activeProfile
	return "use tools"
}

func (s stubToolExecutor) ToolDefinitionsForRun(peerID, sessionKey, activeProfile string) []types.Tool {
	_, _, _ = peerID, sessionKey, activeProfile
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

func (s *stubProvider) Capabilities(string) types.ProviderCapabilities {
	return s.caps
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
