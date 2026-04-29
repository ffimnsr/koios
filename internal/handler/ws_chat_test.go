package handler_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ffimnsr/koios/internal/agent"
	"github.com/ffimnsr/koios/internal/handler"
	"github.com/ffimnsr/koios/internal/memory"
	"github.com/ffimnsr/koios/internal/scheduler"
	"github.com/ffimnsr/koios/internal/session"
	"github.com/ffimnsr/koios/internal/subagent"
	"github.com/ffimnsr/koios/internal/types"
	"github.com/gorilla/websocket"
)

func TestWS_Chat_EmptyMessages(t *testing.T) {
	prov := &stubProvider{response: &types.ChatResponse{}}
	srv, _ := newTestServer(t, prov)

	conn := dialWS(t, srv, "alice")
	sendRPC(t, conn, "1", "chat", map[string]any{"messages": []any{}})
	msg := readUntilID(t, conn, "1")
	if msg.Error == nil {
		t.Fatal("expected error for empty messages")
	}
	if msg.Error.Code != -32602 {
		t.Fatalf("expected invalid params code -32602, got %d", msg.Error.Code)
	}
}

func TestWS_Chat_InvalidRole(t *testing.T) {
	prov := &stubProvider{response: &types.ChatResponse{}}
	srv, _ := newTestServer(t, prov)

	conn := dialWS(t, srv, "alice")
	msgs := []types.Message{{Role: "robot", Content: "hi"}}
	sendRPC(t, conn, "2", "chat", map[string]any{"messages": msgs})
	msg := readUntilID(t, conn, "2")
	if msg.Error == nil {
		t.Fatal("expected error for invalid role")
	}
}

func TestWS_Chat_SuccessStoresHistory(t *testing.T) {
	const assistantReply = "hello back"
	prov := &stubProvider{
		response: &types.ChatResponse{
			Choices: []types.ChatChoice{
				{Message: types.Message{Role: "assistant", Content: assistantReply}},
			},
		},
	}
	srv, store := newTestServer(t, prov)

	conn := dialWS(t, srv, "alice")
	msgs := []types.Message{{Role: "user", Content: "hello"}}
	sendRPC(t, conn, "3", "chat", map[string]any{"messages": msgs})
	msg := readUntilID(t, conn, "3")

	if msg.Error != nil {
		t.Fatalf("unexpected error: %v", msg.Error)
	}

	// Give the handler a moment to persist the session asynchronously.
	time.Sleep(50 * time.Millisecond)

	history := store.Get("alice").History()
	if len(history) != 2 {
		t.Fatalf("want 2 history entries (user + assistant), got %d", len(history))
	}
	if history[0].Role != "user" || history[1].Role != "assistant" {
		t.Fatalf("unexpected roles: %v", history)
	}
	if history[1].Content != assistantReply {
		t.Fatalf("unexpected assistant content: %q", history[1].Content)
	}
}

func TestWS_Chat_HistoryPrependedOnSecondRequest(t *testing.T) {
	prov := &stubProvider{
		response: &types.ChatResponse{
			Choices: []types.ChatChoice{
				{Message: types.Message{Role: "assistant", Content: "reply"}},
			},
		},
	}
	srv, _ := newTestServer(t, prov)

	conn := dialWS(t, srv, "bob")

	// First turn.
	sendRPC(t, conn, "t1", "chat", map[string]any{
		"messages": []types.Message{{Role: "user", Content: "first"}},
	})
	readUntilID(t, conn, "t1")
	time.Sleep(50 * time.Millisecond)

	// Second turn – re-use same connection (same peer).
	sendRPC(t, conn, "t2", "chat", map[string]any{
		"messages": []types.Message{{Role: "user", Content: "second"}},
	})
	readUntilID(t, conn, "t2")
}

func TestWS_SessionReset(t *testing.T) {
	prov := &stubProvider{
		response: &types.ChatResponse{
			Choices: []types.ChatChoice{
				{Message: types.Message{Role: "assistant", Content: "hi"}},
			},
		},
	}
	srv, store := newTestServer(t, prov)

	conn := dialWS(t, srv, "carol")

	// Send a chat to populate history.
	sendRPC(t, conn, "c1", "chat", map[string]any{
		"messages": []types.Message{{Role: "user", Content: "hello"}},
	})
	readUntilID(t, conn, "c1")
	time.Sleep(50 * time.Millisecond)

	if len(store.Get("carol").History()) == 0 {
		t.Fatal("expected non-empty history before reset")
	}

	// Reset.
	sendRPC(t, conn, "c2", "session.reset", nil)
	msg := readUntilID(t, conn, "c2")
	if msg.Error != nil {
		t.Fatalf("session reset error: %v", msg.Error)
	}
	time.Sleep(50 * time.Millisecond)

	if len(store.Get("carol").History()) != 0 {
		t.Fatal("expected empty history after reset")
	}
}

func TestWS_SessionHistory(t *testing.T) {
	prov := &stubProvider{
		response: &types.ChatResponse{
			Choices: []types.ChatChoice{
				{Message: types.Message{Role: "assistant", Content: "hi"}},
			},
		},
	}
	srv, _ := newTestServer(t, prov)
	conn := dialWS(t, srv, "carol")

	sendRPC(t, conn, "h1", "chat", map[string]any{
		"messages": []types.Message{{Role: "user", Content: "hello"}},
	})
	readUntilID(t, conn, "h1")
	time.Sleep(50 * time.Millisecond)

	sendRPC(t, conn, "h2", "session.history", map[string]any{"limit": 10})
	msg := readUntilID(t, conn, "h2")
	if msg.Error != nil {
		t.Fatalf("unexpected error: %v", msg.Error)
	}
	var result struct {
		PeerID     string          `json:"peer_id"`
		SessionKey string          `json:"session_key"`
		Count      int             `json:"count"`
		Messages   []types.Message `json:"messages"`
	}
	if err := json.Unmarshal(msg.Result, &result); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	if result.PeerID != "carol" || result.SessionKey != "carol" {
		t.Fatalf("unexpected history scope: %#v", result)
	}
	if result.Count != 2 || len(result.Messages) != 2 {
		t.Fatalf("expected 2 messages, got %#v", result)
	}
	if result.Messages[0].Content != "hello" || result.Messages[1].Content != "hi" {
		t.Fatalf("unexpected history: %#v", result.Messages)
	}
}

func TestWS_UnknownMethod(t *testing.T) {
	prov := &stubProvider{response: &types.ChatResponse{}}
	srv, _ := newTestServer(t, prov)

	conn := dialWS(t, srv, "dave")
	sendRPC(t, conn, "u1", "does.not.exist", nil)
	msg := readUntilID(t, conn, "u1")
	if msg.Error == nil {
		t.Fatal("expected error for unknown method")
	}
	if msg.Error.Code != -32601 {
		t.Fatalf("expected method not found code -32601, got %d", msg.Error.Code)
	}
}

func TestWS_AgentRun_InvalidRole(t *testing.T) {
	prov := &stubProvider{response: &types.ChatResponse{}}
	store := session.New(10)
	rt := agent.NewRuntime(store, prov, "test-model", 5*time.Second, agent.RetryPolicy{MaxAttempts: 1})
	coord := agent.NewCoordinator(rt)
	h := handler.NewHandler(store, prov, handler.HandlerOptions{
		Model:        "test-model",
		Timeout:      5 * time.Second,
		AgentRuntime: rt,
		AgentCoord:   coord,
	})
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)

	conn := dialWS(t, srv, "agent-role")
	sendRPC(t, conn, "ar1", "agent.run", map[string]any{
		"messages": []types.Message{{Role: "robot", Content: "hi"}},
	})
	msg := readUntilID(t, conn, "ar1")
	if msg.Error == nil {
		t.Fatal("expected error for invalid role")
	}
	if msg.Error.Code != -32000 {
		t.Fatalf("expected server error code -32000, got %d", msg.Error.Code)
	}
	if !strings.Contains(msg.Error.Message, "unknown role") {
		t.Fatalf("unexpected error message: %q", msg.Error.Message)
	}
}

func TestWS_AgentRun_InjectsMemory(t *testing.T) {
	prov := &stubProvider{
		response: &types.ChatResponse{
			Choices: []types.ChatChoice{{Message: types.Message{Role: "assistant", Content: "reply"}}},
		},
	}
	store := session.New(10)
	memStore, err := memory.New(filepath.Join(t.TempDir(), "memory.db"), nil)
	if err != nil {
		t.Fatalf("memory.New: %v", err)
	}
	t.Cleanup(func() { _ = memStore.Close() })
	if err := memStore.Insert(context.Background(), "agent-memory", "Known fact: deployment constraints mean deployment windows are narrow"); err != nil {
		t.Fatalf("memory.Insert: %v", err)
	}
	rt := agent.NewRuntime(store, prov, "test-model", 5*time.Second, agent.RetryPolicy{MaxAttempts: 1})
	rt.EnableMemory(memStore, true, 3)
	coord := agent.NewCoordinator(rt)

	h := handler.NewHandler(store, prov, handler.HandlerOptions{
		Model:        "test-model",
		Timeout:      5 * time.Second,
		MemStore:     memStore,
		MemTopK:      3,
		MemInject:    true,
		AgentRuntime: rt,
		AgentCoord:   coord,
	})
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)

	conn := dialWS(t, srv, "agent-memory")
	sendRPC(t, conn, "am1", "agent.run", map[string]any{
		"messages": []types.Message{{Role: "user", Content: "deployment constraints"}},
	})
	msg := readUntilID(t, conn, "am1")
	if msg.Error != nil {
		t.Fatalf("unexpected error: %v", msg.Error)
	}
	if len(prov.seenReqs) == 0 {
		t.Fatal("expected provider to receive at least one request")
	}
	last := prov.seenReqs[len(prov.seenReqs)-1]
	found := false
	for _, reqMsg := range last.Messages {
		if reqMsg.Role == "system" && strings.Contains(reqMsg.Content, "deployment windows are narrow") {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected memory-injected system message, got %#v", last.Messages)
	}
}

func TestWS_Chat_AutoQueuesMemoryCandidates(t *testing.T) {
	store := session.New(10)
	memStore, err := memory.New(filepath.Join(t.TempDir(), "memory.db"), nil)
	if err != nil {
		t.Fatalf("memory.New: %v", err)
	}
	t.Cleanup(func() { _ = memStore.Close() })
	prov := &stubProvider{
		complete: func(_ context.Context, req *types.ChatRequest) (*types.ChatResponse, error) {
			if len(req.Messages) > 0 && req.Messages[0].Role == "system" && strings.Contains(req.Messages[0].Content, "reviewed memory candidates") {
				return &types.ChatResponse{
					Choices: []types.ChatChoice{{Message: types.Message{Role: "assistant", Content: `{"candidates":[{"content":"User prefers concise release summaries.","category":"preferences","tags":["style"],"retention_class":"pinned"}]}`}}},
				}, nil
			}
			return &types.ChatResponse{
				Choices: []types.ChatChoice{{Message: types.Message{Role: "assistant", Content: "Understood."}}},
			}, nil
		},
	}
	rt := agent.NewRuntime(store, prov, "test-model", 5*time.Second, agent.RetryPolicy{MaxAttempts: 1})
	rt.EnableMemory(memStore, false, 3)
	coord := agent.NewCoordinator(rt)

	h := handler.NewHandler(store, prov, handler.HandlerOptions{
		Model:        "test-model",
		Timeout:      5 * time.Second,
		MemStore:     memStore,
		MemTopK:      3,
		AgentRuntime: rt,
		AgentCoord:   coord,
	})
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)

	conn := dialWS(t, srv, "memory-chat")
	sendRPC(t, conn, "mc1", "chat", map[string]any{
		"messages": []types.Message{{Role: "user", Content: "Remember that I prefer concise release summaries in status updates."}},
	})
	msg := readUntilID(t, conn, "mc1")
	if msg.Error != nil {
		t.Fatalf("unexpected error: %#v", msg.Error)
	}

	candidates, err := memStore.ListCandidates(context.Background(), "memory-chat", 10, memory.CandidateStatusPending)
	if err != nil {
		t.Fatalf("ListCandidates: %v", err)
	}
	if len(candidates) != 1 {
		t.Fatalf("pending candidates = %d, want 1", len(candidates))
	}
	if candidates[0].Content != "User prefers concise release summaries." {
		t.Fatalf("candidate content = %q", candidates[0].Content)
	}
	if candidates[0].CaptureKind != memory.CandidateCaptureAutoTurnExtract {
		t.Fatalf("candidate capture kind = %q, want %q", candidates[0].CaptureKind, memory.CandidateCaptureAutoTurnExtract)
	}
	if candidates[0].SourceSessionKey != "memory-chat::main" {
		t.Fatalf("candidate source session = %q, want memory-chat::main", candidates[0].SourceSessionKey)
	}
	if !strings.Contains(candidates[0].SourceExcerpt, "concise release summaries") {
		t.Fatalf("candidate source excerpt = %q, want release summary excerpt", candidates[0].SourceExcerpt)
	}

	chunks, err := memStore.List(context.Background(), "memory-chat", 10)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(chunks) != 1 {
		t.Fatalf("chunks = %d, want 1 archived summary", len(chunks))
	}
	if chunks[0].RetentionClass != memory.RetentionClassArchive {
		t.Fatalf("summary retention = %q, want archive", chunks[0].RetentionClass)
	}
}

func TestWS_Chat_ToolCreatesCronJob(t *testing.T) {
	store := session.New(10)
	callCount := 0
	prov := &stubProvider{
		complete: func(_ context.Context, req *types.ChatRequest) (*types.ChatResponse, error) {
			callCount++
			if callCount == 1 {
				return &types.ChatResponse{
					Choices: []types.ChatChoice{{
						Message: types.Message{Role: "assistant", Content: `<tool_call>{"name":"cron.create","arguments":{"name":"discord-reminder","schedule":{"kind":"at","at":"2026-04-01T14:00:00Z"},"payload":{"kind":"systemEvent","text":"Reminder: check Discord"},"delete_after_run":true}}</tool_call>`},
					}},
				}, nil
			}
			last := req.Messages[len(req.Messages)-1].Content
			if !strings.Contains(last, "[tool_result cron.create]") {
				t.Fatalf("expected tool result in second request, got %q", last)
			}
			return &types.ChatResponse{
				Choices: []types.ChatChoice{{
					Message: types.Message{Role: "assistant", Content: "I set a reminder for 10:00 PM Philippine Time to check Discord."},
				}},
			}, nil
		},
	}
	jobStore, err := scheduler.NewJobStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewJobStore: %v", err)
	}
	sched := scheduler.New(jobStore, prov, store, nil, "test-model", 1)
	rt := agent.NewRuntime(store, prov, "test-model", 5*time.Second, agent.RetryPolicy{MaxAttempts: 1})
	coord := agent.NewCoordinator(rt)
	h := handler.NewHandler(store, prov, handler.HandlerOptions{
		Model:        "test-model",
		Timeout:      5 * time.Second,
		AgentRuntime: rt,
		AgentCoord:   coord,
		JobStore:     jobStore,
		Sched:        sched,
	})
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)

	conn := dialWS(t, srv, "cron-peer")
	sendRPC(t, conn, "tool1", "chat", map[string]any{
		"messages": []types.Message{{Role: "user", Content: "Remind me at 10PM Philippine Time to check Discord"}},
	})
	msg := readUntilID(t, conn, "tool1")
	if msg.Error != nil {
		t.Fatalf("unexpected error: %v", msg.Error)
	}
	var resp types.ChatResponse
	if err := json.Unmarshal(msg.Result, &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if len(resp.Choices) == 0 || !strings.Contains(resp.Choices[0].Message.Content, "set a reminder") {
		t.Fatalf("unexpected final response: %#v", resp)
	}
	jobs := jobStore.List("cron-peer")
	if len(jobs) != 1 {
		t.Fatalf("expected one cron job, got %d", len(jobs))
	}
	if jobs[0].Name != "discord-reminder" {
		t.Fatalf("unexpected job name: %q", jobs[0].Name)
	}
	if jobs[0].Payload.Text != "Reminder: check Discord" {
		t.Fatalf("unexpected payload: %#v", jobs[0].Payload)
	}
}

func TestWS_Chat_ToolAliasListResolvesToCronList(t *testing.T) {
	store := session.New(10)
	prov := &stubProvider{}
	jobStore, err := scheduler.NewJobStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewJobStore: %v", err)
	}
	sched := scheduler.New(jobStore, prov, store, nil, "test-model", 1)
	job := &scheduler.Job{
		PeerID:         "cron-peer",
		Name:           "existing-reminder",
		Schedule:       scheduler.Schedule{Kind: scheduler.KindEvery, EveryMs: 60_000},
		Payload:        scheduler.Payload{Kind: scheduler.PayloadSystemEvent, Text: "check discord"},
		Enabled:        true,
		DeleteAfterRun: false,
		NextRunAt:      time.Now().UTC().Add(time.Minute),
	}
	if err := jobStore.Add(job); err != nil {
		t.Fatalf("jobStore.Add: %v", err)
	}
	callCount := 0
	prov.complete = func(_ context.Context, req *types.ChatRequest) (*types.ChatResponse, error) {
		callCount++
		if callCount == 1 {
			return &types.ChatResponse{
				Choices: []types.ChatChoice{{
					Message: types.Message{Role: "assistant", Content: `<tool_call>{"name":"list","arguments":{}}</tool_call>`},
				}},
			}, nil
		}
		last := req.Messages[len(req.Messages)-1].Content
		if !strings.Contains(last, "[tool_result cron.list]") {
			t.Fatalf("expected cron.list tool result in second request, got %q", last)
		}
		return &types.ChatResponse{
			Choices: []types.ChatChoice{{
				Message: types.Message{Role: "assistant", Content: "You have 1 scheduled job: existing-reminder."},
			}},
		}, nil
	}
	rt := agent.NewRuntime(store, prov, "test-model", 5*time.Second, agent.RetryPolicy{MaxAttempts: 1})
	coord := agent.NewCoordinator(rt)
	h := handler.NewHandler(store, prov, handler.HandlerOptions{
		Model:        "test-model",
		Timeout:      5 * time.Second,
		AgentRuntime: rt,
		AgentCoord:   coord,
		JobStore:     jobStore,
		Sched:        sched,
	})
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)

	conn := dialWS(t, srv, "cron-peer")
	sendRPC(t, conn, "list1", "chat", map[string]any{
		"messages": []types.Message{{Role: "user", Content: "List my scheduled jobs"}},
		"stream":   true,
	})
	msg := readUntilID(t, conn, "list1")
	if msg.Error != nil {
		t.Fatalf("unexpected error: %v", msg.Error)
	}
}

func TestWS_Chat_CronCreateNormalizesSixFieldExpr(t *testing.T) {
	store := session.New(10)
	callCount := 0
	prov := &stubProvider{
		complete: func(_ context.Context, req *types.ChatRequest) (*types.ChatResponse, error) {
			callCount++
			if callCount == 1 {
				return &types.ChatResponse{
					Choices: []types.ChatChoice{{
						Message: types.Message{Role: "assistant", Content: `<tool_call>{"name":"cron.create","arguments":{"name":"nightly-reminder","schedule":{"kind":"cron","expr":"0 15 22 * * *","tz":"Asia/Manila"},"payload":{"kind":"systemEvent","text":"Reminder: check Discord"}}}</tool_call>`},
					}},
				}, nil
			}
			last := req.Messages[len(req.Messages)-1].Content
			if !strings.Contains(last, "[tool_result cron.create]") {
				t.Fatalf("expected tool result in second request, got %q", last)
			}
			return &types.ChatResponse{
				Choices: []types.ChatChoice{{
					Message: types.Message{Role: "assistant", Content: "I created the recurring reminder."},
				}},
			}, nil
		},
	}
	jobStore, err := scheduler.NewJobStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewJobStore: %v", err)
	}
	sched := scheduler.New(jobStore, prov, store, nil, "test-model", 1)
	rt := agent.NewRuntime(store, prov, "test-model", 5*time.Second, agent.RetryPolicy{MaxAttempts: 1})
	coord := agent.NewCoordinator(rt)
	h := handler.NewHandler(store, prov, handler.HandlerOptions{
		Model:        "test-model",
		Timeout:      5 * time.Second,
		AgentRuntime: rt,
		AgentCoord:   coord,
		JobStore:     jobStore,
		Sched:        sched,
	})
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)

	conn := dialWS(t, srv, "cron-peer")
	sendRPC(t, conn, "cron6", "chat", map[string]any{
		"messages": []types.Message{{Role: "user", Content: "Create a job for 10:15 PM daily"}},
	})
	msg := readUntilID(t, conn, "cron6")
	if msg.Error != nil {
		t.Fatalf("unexpected error: %v", msg.Error)
	}
	jobs := jobStore.List("cron-peer")
	if len(jobs) != 1 {
		t.Fatalf("expected one cron job, got %d", len(jobs))
	}
	if jobs[0].Schedule.Expr != "15 22 * * *" {
		t.Fatalf("expected normalized 5-field cron expr, got %q", jobs[0].Schedule.Expr)
	}
}

func TestWS_Chat_SessionHistoryToolReadsStoredTranscript(t *testing.T) {
	store := session.New(20)
	store.Append("history-peer",
		types.Message{Role: "user", Content: "hello"},
		types.Message{Role: "assistant", Content: "hi"},
	)
	callCount := 0
	prov := &stubProvider{
		complete: func(_ context.Context, req *types.ChatRequest) (*types.ChatResponse, error) {
			callCount++
			if callCount == 1 {
				return &types.ChatResponse{
					Choices: []types.ChatChoice{{
						Message: types.Message{Role: "assistant", Content: `<tool_call>{"name":"session.history","arguments":{"limit":10}}</tool_call>`},
					}},
				}, nil
			}
			last := req.Messages[len(req.Messages)-1].Content
			if !strings.Contains(last, "[tool_result session.history]") || !strings.Contains(last, "\"hello\"") {
				t.Fatalf("expected session.history tool result in second request, got %q", last)
			}
			return &types.ChatResponse{
				Choices: []types.ChatChoice{{
					Message: types.Message{Role: "assistant", Content: "You previously said hello once."},
				}},
			}, nil
		},
	}
	rt := agent.NewRuntime(store, prov, "test-model", 5*time.Second, agent.RetryPolicy{MaxAttempts: 1})
	coord := agent.NewCoordinator(rt)
	h := handler.NewHandler(store, prov, handler.HandlerOptions{
		Model:        "test-model",
		Timeout:      5 * time.Second,
		AgentRuntime: rt,
		AgentCoord:   coord,
	})
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)

	conn := dialWS(t, srv, "history-peer")
	sendRPC(t, conn, "hist-tool", "chat", map[string]any{
		"messages": []types.Message{{Role: "user", Content: "how many times did I say hello earlier?"}},
	})
	msg := readUntilID(t, conn, "hist-tool")
	if msg.Error != nil {
		t.Fatalf("unexpected error: %v", msg.Error)
	}
}

func TestWS_Chat_StreamEmitsToolEvents(t *testing.T) {
	store := session.New(10)
	callCount := 0
	prov := &stubProvider{
		complete: func(_ context.Context, req *types.ChatRequest) (*types.ChatResponse, error) {
			callCount++
			if callCount == 1 {
				return &types.ChatResponse{
					Choices: []types.ChatChoice{{
						Message: types.Message{Role: "assistant", ToolCalls: []types.ToolCall{{
							ID:   "tool-1",
							Type: "function",
							Function: types.ToolCallFunctionRef{
								Name:      "time.now",
								Arguments: `{}`,
							},
						}}},
					}},
				}, nil
			}
			return &types.ChatResponse{
				Choices: []types.ChatChoice{{
					Message: types.Message{Role: "assistant", Content: "Done."},
				}},
			}, nil
		},
	}
	rt := agent.NewRuntime(store, prov, "test-model", 5*time.Second, agent.RetryPolicy{MaxAttempts: 1})
	coord := agent.NewCoordinator(rt)
	h := handler.NewHandler(store, prov, handler.HandlerOptions{
		Model:        "test-model",
		Timeout:      5 * time.Second,
		AgentRuntime: rt,
		AgentCoord:   coord,
	})
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)

	conn := dialWS(t, srv, "stream-peer")
	sendRPC(t, conn, "stream1", "chat", map[string]any{
		"messages": []types.Message{{Role: "user", Content: "What time is it?"}},
		"stream":   true,
	})
	frames := readFramesUntilID(t, conn, "stream1")
	var sawToolEvent, sawDelta bool
	for _, frame := range frames {
		if frame.Method == "stream.event" && strings.Contains(string(frame.Params), "tool_call") {
			sawToolEvent = true
		}
		if frame.Method == "stream.delta" && strings.Contains(string(frame.Params), "Done.") {
			sawDelta = true
		}
	}
	if !sawToolEvent {
		t.Fatal("expected stream.event tool_call notification")
	}
	if !sawDelta {
		t.Fatal("expected final stream.delta notification")
	}
}

func TestWS_Chat_SilentReplySuppressesOutputAndHistory(t *testing.T) {
	store := session.New(10)
	prov := &stubProvider{
		stream: func(_ context.Context, req *types.ChatRequest, w http.ResponseWriter) (string, error) {
			if _, err := w.Write([]byte(sseChunk(agent.SilentReplyToken))); err != nil {
				return "", err
			}
			if flusher, ok := w.(http.Flusher); ok {
				flusher.Flush()
			}
			if _, err := w.Write([]byte("data: [DONE]\n\n")); err != nil {
				return "", err
			}
			if flusher, ok := w.(http.Flusher); ok {
				flusher.Flush()
			}
			return agent.SilentReplyToken, nil
		},
	}
	rt := agent.NewRuntime(store, prov, "test-model", 5*time.Second, agent.RetryPolicy{MaxAttempts: 1})
	coord := agent.NewCoordinator(rt)
	h := handler.NewHandler(store, prov, handler.HandlerOptions{
		Model:        "test-model",
		Timeout:      5 * time.Second,
		AgentRuntime: rt,
		AgentCoord:   coord,
	})
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)

	conn := dialWS(t, srv, "silent-peer")
	sendRPC(t, conn, "silent1", "chat", map[string]any{
		"messages": []types.Message{{Role: "user", Content: "stay quiet"}},
		"stream":   true,
	})
	frames := readFramesUntilID(t, conn, "silent1")
	for _, frame := range frames {
		if frame.Method != "stream.delta" {
			continue
		}
		var payload struct {
			Content string `json:"content"`
		}
		if err := json.Unmarshal(frame.Params, &payload); err != nil {
			t.Fatalf("unmarshal delta: %v", err)
		}
		if payload.Content != "" {
			t.Fatalf("expected no streamed content, got %q", payload.Content)
		}
	}
	msg := readResultFromFrames(t, frames, "silent1")
	var result struct {
		AssistantText   string `json:"assistant_text"`
		SuppressedReply bool   `json:"suppressed_reply"`
	}
	if err := json.Unmarshal(msg.Result, &result); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	if result.AssistantText != "" || !result.SuppressedReply {
		t.Fatalf("unexpected silent result: %#v", result)
	}
	history := store.Get("silent-peer").History()
	if len(history) != 1 || history[0].Role != "user" {
		t.Fatalf("expected only the user message in history, got %#v", history)
	}
}

func TestWS_Chat_VerboseModeIncludesInlineToolSummary(t *testing.T) {
	store := session.New(10)
	if err := store.SetPolicy("verbose-peer", session.SessionPolicy{VerboseMode: true}); err != nil {
		t.Fatalf("SetPolicy: %v", err)
	}
	callCount := 0
	prov := &stubProvider{
		complete: func(_ context.Context, req *types.ChatRequest) (*types.ChatResponse, error) {
			callCount++
			if callCount == 1 {
				return &types.ChatResponse{
					Choices: []types.ChatChoice{{
						Message: types.Message{Role: "assistant", ToolCalls: []types.ToolCall{{
							ID:       "tool-1",
							Type:     "function",
							Function: types.ToolCallFunctionRef{Name: "time.now", Arguments: `{}`},
						}}},
					}},
				}, nil
			}
			return &types.ChatResponse{
				Choices: []types.ChatChoice{{Message: types.Message{Role: "assistant", Content: "It is done."}}},
			}, nil
		},
	}
	rt := agent.NewRuntime(store, prov, "test-model", 5*time.Second, agent.RetryPolicy{MaxAttempts: 1})
	coord := agent.NewCoordinator(rt)
	h := handler.NewHandler(store, prov, handler.HandlerOptions{
		Model:        "test-model",
		Timeout:      5 * time.Second,
		AgentRuntime: rt,
		AgentCoord:   coord,
	})
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)

	conn := dialWS(t, srv, "verbose-peer")
	sendRPC(t, conn, "verbose1", "chat", map[string]any{
		"messages": []types.Message{{Role: "user", Content: "What time is it?"}},
	})
	msg := readUntilID(t, conn, "verbose1")
	if msg.Error != nil {
		t.Fatalf("unexpected error: %v", msg.Error)
	}
	var resp types.ChatResponse
	if err := json.Unmarshal(msg.Result, &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	text := resp.Choices[0].Message.Content
	if !strings.Contains(text, "Tool summary:") || !strings.Contains(text, "time.now") || !strings.Contains(text, "Arguments: {}") || !strings.Contains(text, "Reply:\nIt is done.") {
		t.Fatalf("expected inline verbose tool summary, got %q", text)
	}
	history := store.Get("verbose-peer").History()
	if len(history) != 2 || history[1].Content != "It is done." {
		t.Fatalf("expected persisted history to keep the plain assistant reply, got %#v", history)
	}
}

func TestWS_Chat_UsageFooterEnabled(t *testing.T) {
	prov := &stubProvider{
		response: &types.ChatResponse{
			Choices: []types.ChatChoice{{Message: types.Message{Role: "assistant", Content: "Done."}}},
			Usage:   types.Usage{PromptTokens: 4, CompletionTokens: 6, TotalTokens: 10},
		},
	}
	srv, store := newTestServer(t, prov)
	if err := store.SetPolicy("usage-footer-peer", session.SessionPolicy{UsageMode: "tokens"}); err != nil {
		t.Fatalf("SetPolicy: %v", err)
	}
	conn := dialWS(t, srv, "usage-footer-peer")

	sendRPC(t, conn, "usage1", "chat", map[string]any{
		"messages": []types.Message{{Role: "user", Content: "hello"}},
	})
	msg := readUntilID(t, conn, "usage1")
	var result types.ChatResponse
	if err := json.Unmarshal(msg.Result, &result); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	if len(result.Choices) == 0 {
		t.Fatalf("expected at least one chat choice, got %#v", result)
	}
	assistantText := result.Choices[0].Message.Content
	if !strings.Contains(assistantText, "Done.") {
		t.Fatalf("expected assistant text in reply, got %q", assistantText)
	}
	if !strings.Contains(assistantText, "Usage: 4 prompt, 6 completion, 10 total tokens") {
		t.Fatalf("expected usage footer in reply, got %q", assistantText)
	}
	if result.Usage.TotalTokens != 10 {
		t.Fatalf("unexpected usage payload: %#v", result.Usage)
	}
	if history := store.Get("usage-footer-peer").History(); len(history) != 2 || history[1].Content != "Done." {
		t.Fatalf("expected persisted assistant message to remain footer-free, got %#v", history)
	}
}

func TestExecuteTool_SessionPatchUsageMode(t *testing.T) {
	store := session.New(20)
	prov := &stubProvider{response: &types.ChatResponse{Choices: []types.ChatChoice{{Message: types.Message{Role: "assistant", Content: "ok"}}}}}
	rt := agent.NewRuntime(store, prov, "test-model", 5*time.Second, agent.RetryPolicy{MaxAttempts: 1})
	reg, err := subagent.NewRegistry("")
	if err != nil {
		t.Fatalf("subagent.NewRegistry: %v", err)
	}
	subrt := subagent.NewRuntime(rt, store, reg, nil, 4)
	h := handler.NewHandler(store, prov, handler.HandlerOptions{
		Model:        "test-model",
		Timeout:      5 * time.Second,
		AgentRuntime: rt,
		AgentCoord:   agent.NewCoordinator(rt),
		SubRuntime:   subrt,
	})

	result, err := h.ExecuteTool(context.Background(), "alice", agent.ToolCall{
		Name:      "session.patch",
		Arguments: json.RawMessage(`{"session_key":"alice::sender::bob","usage_mode":"full"}`),
	})
	if err != nil {
		t.Fatalf("ExecuteTool(session.patch usage_mode): %v", err)
	}
	var patchRes struct {
		OK         bool   `json:"ok"`
		SessionKey string `json:"session_key"`
		UsageMode  string `json:"usage_mode"`
	}
	raw, _ := json.Marshal(result)
	if err := json.Unmarshal(raw, &patchRes); err != nil {
		t.Fatalf("unmarshal session.patch result: %v", err)
	}
	if !patchRes.OK || patchRes.SessionKey != "alice::sender::bob" || patchRes.UsageMode != "tokens" {
		t.Fatalf("unexpected session.patch result: %#v", patchRes)
	}
	if store.Policy("alice::sender::bob").UsageMode != "tokens" {
		t.Fatal("expected patched usage_mode policy on target session")
	}
}

func TestWS_Chat_BlockStreamingCoalescesChunks(t *testing.T) {
	store := session.New(10)
	if err := store.SetPolicy("block-peer", session.SessionPolicy{BlockStream: true, StreamChunkChars: 5}); err != nil {
		t.Fatalf("SetPolicy: %v", err)
	}
	prov := &stubProvider{
		stream: func(_ context.Context, req *types.ChatRequest, w http.ResponseWriter) (string, error) {
			for _, part := range []string{"H", "e", "l", "l", "o", " ", "w", "o", "r", "l", "d"} {
				if _, err := w.Write([]byte(sseChunk(part))); err != nil {
					return "", err
				}
				if flusher, ok := w.(http.Flusher); ok {
					flusher.Flush()
				}
			}
			if _, err := w.Write([]byte("data: [DONE]\n\n")); err != nil {
				return "", err
			}
			if flusher, ok := w.(http.Flusher); ok {
				flusher.Flush()
			}
			return "Hello world", nil
		},
	}
	rt := agent.NewRuntime(store, prov, "test-model", 5*time.Second, agent.RetryPolicy{MaxAttempts: 1})
	coord := agent.NewCoordinator(rt)
	h := handler.NewHandler(store, prov, handler.HandlerOptions{
		Model:        "test-model",
		Timeout:      5 * time.Second,
		AgentRuntime: rt,
		AgentCoord:   coord,
	})
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)

	conn := dialWS(t, srv, "block-peer")
	sendRPC(t, conn, "block1", "chat", map[string]any{
		"messages": []types.Message{{Role: "user", Content: "hello"}},
		"stream":   true,
	})
	frames := readFramesUntilID(t, conn, "block1")
	var deltas []string
	for _, frame := range frames {
		if frame.Method != "stream.delta" {
			continue
		}
		var payload struct {
			Content string `json:"content"`
		}
		if err := json.Unmarshal(frame.Params, &payload); err != nil {
			t.Fatalf("unmarshal delta: %v", err)
		}
		deltas = append(deltas, payload.Content)
	}
	joined := strings.Join(deltas, "")
	if joined != "Hello world" {
		t.Fatalf("joined deltas = %q", joined)
	}
	if len(deltas) >= 11 {
		t.Fatalf("expected coalesced block streaming, got %d deltas", len(deltas))
	}
}

func TestWS_AgentStartAndWait(t *testing.T) {
	prov := &stubProvider{
		response: &types.ChatResponse{
			Choices: []types.ChatChoice{{Message: types.Message{Role: "assistant", Content: "done"}}},
			Usage:   types.Usage{PromptTokens: 5, CompletionTokens: 2, TotalTokens: 7},
		},
	}
	srv, _ := newTestServer(t, prov)
	conn := dialWS(t, srv, "agent-async")

	sendRPC(t, conn, "start1", "agent.start", map[string]any{
		"messages": []types.Message{{Role: "user", Content: "hello"}},
	})
	startMsg := readUntilID(t, conn, "start1")
	if startMsg.Error != nil {
		t.Fatalf("unexpected start error: %v", startMsg.Error)
	}
	var startResult struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(startMsg.Result, &startResult); err != nil {
		t.Fatalf("unmarshal start result: %v", err)
	}
	if startResult.ID == "" {
		t.Fatal("expected run id")
	}

	sendRPC(t, conn, "wait1", "agent.wait", map[string]any{
		"id":      startResult.ID,
		"timeout": "2s",
	})
	waitMsg := readUntilID(t, conn, "wait1")
	if waitMsg.Error != nil {
		t.Fatalf("unexpected wait error: %v", waitMsg.Error)
	}
	var waitResult struct {
		Status string `json:"status"`
		Result struct {
			AssistantText string      `json:"assistant_text"`
			Usage         types.Usage `json:"usage"`
		} `json:"result"`
	}
	if err := json.Unmarshal(waitMsg.Result, &waitResult); err != nil {
		t.Fatalf("unmarshal wait result: %v", err)
	}
	if waitResult.Status != "completed" {
		t.Fatalf("expected completed status, got %q", waitResult.Status)
	}
	if waitResult.Result.AssistantText != "done" {
		t.Fatalf("unexpected assistant text %q", waitResult.Result.AssistantText)
	}
	if waitResult.Result.Usage.TotalTokens != 7 {
		t.Fatalf("unexpected usage %#v", waitResult.Result.Usage)
	}
}

func TestWS_AgentCancel(t *testing.T) {
	prov := &stubProvider{
		complete: func(ctx context.Context, req *types.ChatRequest) (*types.ChatResponse, error) {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(2 * time.Second):
				return &types.ChatResponse{
					Choices: []types.ChatChoice{{Message: types.Message{Role: "assistant", Content: "late"}}},
				}, nil
			}
		},
	}
	srv, _ := newTestServer(t, prov)
	conn := dialWS(t, srv, "agent-cancel")

	sendRPC(t, conn, "start-cancel", "agent.start", map[string]any{
		"messages": []types.Message{{Role: "user", Content: "hello"}},
	})
	startMsg := readUntilID(t, conn, "start-cancel")
	if startMsg.Error != nil {
		t.Fatalf("unexpected start error: %v", startMsg.Error)
	}
	var startResult struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(startMsg.Result, &startResult); err != nil {
		t.Fatalf("unmarshal start result: %v", err)
	}

	sendRPC(t, conn, "cancel1", "agent.cancel", map[string]any{"id": startResult.ID})
	cancelMsg := readUntilID(t, conn, "cancel1")
	if cancelMsg.Error != nil {
		t.Fatalf("unexpected cancel error: %v", cancelMsg.Error)
	}
	var cancelResult struct {
		Status string `json:"status"`
	}
	if err := json.Unmarshal(cancelMsg.Result, &cancelResult); err != nil {
		t.Fatalf("unmarshal cancel result: %v", err)
	}
	if cancelResult.Status != "canceled" {
		t.Fatalf("expected canceled status, got %q", cancelResult.Status)
	}
}

func TestWS_ParseError(t *testing.T) {
	prov := &stubProvider{response: &types.ChatResponse{}}
	srv, _ := newTestServer(t, prov)

	conn := dialWS(t, srv, "eve")
	// Send malformed JSON.
	if err := conn.WriteMessage(websocket.TextMessage, []byte("{bad json")); err != nil {
		t.Fatalf("write: %v", err)
	}
	msg := readMsg(t, conn)
	if msg.Error == nil {
		t.Fatal("expected parse error")
	}
	if msg.Error.Code != -32700 {
		t.Fatalf("expected parse error code -32700, got %d", msg.Error.Code)
	}
}

// ── helper function tests (pure, no server) ───────────────────────────────────

func TestIsValidPeerID(t *testing.T) {
	cases := []struct {
		id   string
		want bool
	}{
		{"alice", true},
		{"peer-1", true},
		{"peer_1.local@home:8080", true},
		{"", false},
		{"bad peer", false},
		{"bad<peer>", false},
		{strings.Repeat("a", 257), false},
		{strings.Repeat("a", 256), true},
	}
	for _, tc := range cases {
		got := handler.IsValidPeerID(tc.id)
		if got != tc.want {
			t.Errorf("IsValidPeerID(%q) = %v, want %v", tc.id, got, tc.want)
		}
	}
}

func containsString(items []string, want string) bool {
	for _, item := range items {
		if item == want {
			return true
		}
	}
	return false
}
