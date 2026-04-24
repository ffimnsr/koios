package handler_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ffimnsr/koios/internal/agent"
	"github.com/ffimnsr/koios/internal/calendar"
	"github.com/ffimnsr/koios/internal/handler"
	"github.com/ffimnsr/koios/internal/memory"
	"github.com/ffimnsr/koios/internal/runledger"
	"github.com/ffimnsr/koios/internal/scheduler"
	"github.com/ffimnsr/koios/internal/session"
	"github.com/ffimnsr/koios/internal/subagent"
	"github.com/ffimnsr/koios/internal/tasks"
	"github.com/ffimnsr/koios/internal/types"
	"github.com/ffimnsr/koios/internal/workspace"
	"github.com/gorilla/websocket"
)

// ── stub provider ─────────────────────────────────────────────────────────────

type stubProvider struct {
	response *types.ChatResponse
	err      error
	seenReqs []*types.ChatRequest
	complete func(context.Context, *types.ChatRequest) (*types.ChatResponse, error)
	stream   func(context.Context, *types.ChatRequest, http.ResponseWriter) (string, error)
	caps     types.ProviderCapabilities
}

func (s *stubProvider) Complete(ctx context.Context, req *types.ChatRequest) (*types.ChatResponse, error) {
	s.seenReqs = append(s.seenReqs, req)
	if s.complete != nil {
		return s.complete(ctx, req)
	}
	return s.response, s.err
}

func (s *stubProvider) CompleteStream(ctx context.Context, req *types.ChatRequest, w http.ResponseWriter) (string, error) {
	s.seenReqs = append(s.seenReqs, req)
	if s.stream != nil {
		return s.stream(ctx, req, w)
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.WriteHeader(http.StatusOK)
	return "streamed reply", s.err
}

func (s *stubProvider) Capabilities(string) types.ProviderCapabilities {
	return s.caps
}

func sseChunk(content string) string {
	return fmt.Sprintf("data: {\"id\":\"1\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"test-model\",\"choices\":[{\"index\":0,\"delta\":{\"content\":%q}}]}\n\n", content)
}

// ── test helpers ──────────────────────────────────────────────────────────────

func newTestServer(t *testing.T, prov *stubProvider) (*httptest.Server, *session.Store) {
	t.Helper()
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
	return srv, store
}

func newTestServerWithTasks(t *testing.T, prov *stubProvider) (*httptest.Server, *session.Store, *tasks.Store) {
	t.Helper()
	store := session.New(10)
	rt := agent.NewRuntime(store, prov, "test-model", 5*time.Second, agent.RetryPolicy{MaxAttempts: 1})
	coord := agent.NewCoordinator(rt)
	taskStore, err := tasks.New(filepath.Join(t.TempDir(), "tasks.db"))
	if err != nil {
		t.Fatalf("tasks.New: %v", err)
	}
	rt.EnableTasks(taskStore)
	t.Cleanup(func() { _ = taskStore.Close() })
	h := handler.NewHandler(store, prov, handler.HandlerOptions{
		Model:        "test-model",
		Timeout:      5 * time.Second,
		AgentRuntime: rt,
		AgentCoord:   coord,
		TaskStore:    taskStore,
	})
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return srv, store, taskStore
}

func newTestServerWithCalendar(t *testing.T, prov *stubProvider) (*httptest.Server, *session.Store, *calendar.Store) {
	t.Helper()
	store := session.New(10)
	rt := agent.NewRuntime(store, prov, "test-model", 5*time.Second, agent.RetryPolicy{MaxAttempts: 1})
	coord := agent.NewCoordinator(rt)
	calendarStore, err := calendar.New(filepath.Join(t.TempDir(), "calendar.db"))
	if err != nil {
		t.Fatalf("calendar.New: %v", err)
	}
	t.Cleanup(func() { _ = calendarStore.Close() })
	h := handler.NewHandler(store, prov, handler.HandlerOptions{
		Model:         "test-model",
		Timeout:       5 * time.Second,
		AgentRuntime:  rt,
		AgentCoord:    coord,
		CalendarStore: calendarStore,
	})
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return srv, store, calendarStore
}

func dialWS(t *testing.T, srv *httptest.Server, peerID string) *websocket.Conn {
	t.Helper()
	u := "ws" + strings.TrimPrefix(srv.URL, "http") + "/v1/ws?peer_id=" + peerID
	conn, _, err := websocket.DefaultDialer.Dial(u, nil)
	if err != nil {
		t.Fatalf("dial websocket: %v", err)
	}
	t.Cleanup(func() { conn.Close() })
	return conn
}

func mustSubagentRegistry(t *testing.T) *subagent.Registry {
	t.Helper()
	reg, err := subagent.NewRegistry("")
	if err != nil {
		t.Fatalf("subagent.NewRegistry: %v", err)
	}
	return reg
}

type rpcMsg struct {
	ID     json.RawMessage `json:"id"`
	Method string          `json:"method"`
	Result json.RawMessage `json:"result"`
	Error  *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
	Params json.RawMessage `json:"params"`
}

func sendRPC(t *testing.T, conn *websocket.Conn, id, method string, params any) {
	t.Helper()
	raw, err := json.Marshal(params)
	if err != nil {
		t.Fatalf("marshal params: %v", err)
	}
	idRaw, _ := json.Marshal(id)
	frame := map[string]json.RawMessage{
		"id":     idRaw,
		"method": json.RawMessage(`"` + method + `"`),
		"params": raw,
	}
	if err := conn.WriteJSON(frame); err != nil {
		t.Fatalf("write rpc: %v", err)
	}
}

func readMsg(t *testing.T, conn *websocket.Conn) rpcMsg {
	t.Helper()
	conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	_, b, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("read message: %v", err)
	}
	var msg rpcMsg
	if err := json.Unmarshal(b, &msg); err != nil {
		t.Fatalf("unmarshal message: %v", err)
	}
	return msg
}

// readUntilID discards streaming notifications and returns the first frame
// whose id matches the given request id.
func readUntilID(t *testing.T, conn *websocket.Conn, id string) rpcMsg {
	t.Helper()
	wantID, _ := json.Marshal(id)
	for i := 0; i < 100; i++ {
		msg := readMsg(t, conn)
		if string(msg.ID) == string(wantID) {
			return msg
		}
	}
	t.Fatalf("did not receive response for id %q", id)
	return rpcMsg{}
}

func readFramesUntilID(t *testing.T, conn *websocket.Conn, id string) []rpcMsg {
	t.Helper()
	wantID, _ := json.Marshal(id)
	var msgs []rpcMsg
	for i := 0; i < 100; i++ {
		msg := readMsg(t, conn)
		msgs = append(msgs, msg)
		if string(msg.ID) == string(wantID) {
			return msgs
		}
	}
	t.Fatalf("did not receive response for id %q", id)
	return nil
}

func readResultFromFrames(t *testing.T, frames []rpcMsg, id string) rpcMsg {
	t.Helper()
	wantID, _ := json.Marshal(id)
	for _, msg := range frames {
		if string(msg.ID) == string(wantID) {
			return msg
		}
	}
	t.Fatalf("did not receive response for id %q", id)
	return rpcMsg{}
}

// ── tests ─────────────────────────────────────────────────────────────────────

func TestWS_MissingPeerID(t *testing.T) {
	prov := &stubProvider{response: &types.ChatResponse{}}
	h := handler.NewHandler(session.New(10), prov, handler.HandlerOptions{Model: "m", Timeout: 5 * time.Second})
	srv := httptest.NewServer(h)
	defer srv.Close()

	u := "ws" + strings.TrimPrefix(srv.URL, "http") + "/v1/ws"
	_, resp, err := websocket.DefaultDialer.Dial(u, nil)
	if err == nil {
		t.Fatal("expected dial to fail without peer_id")
	}
	if resp != nil && resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", resp.StatusCode)
	}
}

func TestWS_InvalidPeerID(t *testing.T) {
	prov := &stubProvider{response: &types.ChatResponse{}}
	h := handler.NewHandler(session.New(10), prov, handler.HandlerOptions{Model: "m", Timeout: 5 * time.Second})
	srv := httptest.NewServer(h)
	defer srv.Close()

	u := "ws" + strings.TrimPrefix(srv.URL, "http") + "/v1/ws?peer_id=bad%20peer%3Cinjection%3E"
	_, resp, err := websocket.DefaultDialer.Dial(u, nil)
	if err == nil {
		t.Fatal("expected dial to fail with invalid peer_id")
	}
	if resp != nil && resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", resp.StatusCode)
	}
}

func TestWS_Ping(t *testing.T) {
	prov := &stubProvider{response: &types.ChatResponse{}}
	srv, _ := newTestServer(t, prov)

	conn := dialWS(t, srv, "alice")
	sendRPC(t, conn, "1", "ping", nil)
	msg := readUntilID(t, conn, "1")
	if msg.Error != nil {
		t.Fatalf("unexpected error: %v", msg.Error)
	}
	var result map[string]bool
	if err := json.Unmarshal(msg.Result, &result); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	if !result["pong"] {
		t.Fatal("expected pong:true")
	}
}

func TestWS_TaskCandidateLifecycle(t *testing.T) {
	prov := &stubProvider{response: &types.ChatResponse{}}
	srv, _, taskStore := newTestServerWithTasks(t, prov)
	conn := dialWS(t, srv, "tasks-quebec")

	sendRPC(t, conn, "1", "task.candidate.extract", map[string]any{
		"text": "Remember to send the board deck and please book travel.",
	})
	msg := readUntilID(t, conn, "1")
	if msg.Error != nil {
		t.Fatalf("extract rpc error: %+v", msg.Error)
	}
	var extracted struct {
		Count      int              `json:"count"`
		Candidates []map[string]any `json:"candidates"`
	}
	if err := json.Unmarshal(msg.Result, &extracted); err != nil {
		t.Fatalf("unmarshal extract result: %v", err)
	}
	if extracted.Count != 2 {
		t.Fatalf("extract count = %d, want 2", extracted.Count)
	}
	if len(extracted.Candidates) != 2 {
		t.Fatalf("extracted candidates = %d, want 2", len(extracted.Candidates))
	}

	candidates, err := taskStore.ListCandidates(context.Background(), "tasks-quebec", 10, tasks.CandidateStatusPending)
	if err != nil {
		t.Fatalf("ListCandidates: %v", err)
	}
	if len(candidates) != 2 {
		t.Fatalf("stored candidates = %d, want 2", len(candidates))
	}

	sendRPC(t, conn, "2", "task.candidate.approve", map[string]any{"id": candidates[0].ID})
	msg = readUntilID(t, conn, "2")
	if msg.Error != nil {
		t.Fatalf("approve rpc error: %+v", msg.Error)
	}

	tasksList, err := taskStore.ListTasks(context.Background(), "tasks-quebec", 10, tasks.TaskStatusOpen)
	if err != nil {
		t.Fatalf("ListTasks: %v", err)
	}
	if len(tasksList) != 1 {
		t.Fatalf("tasks = %d, want 1", len(tasksList))
	}

	sendRPC(t, conn, "3", "task.complete", map[string]any{"id": tasksList[0].ID})
	msg = readUntilID(t, conn, "3")
	if msg.Error != nil {
		t.Fatalf("complete rpc error: %+v", msg.Error)
	}
	loaded, err := taskStore.GetTask(context.Background(), "tasks-quebec", tasksList[0].ID)
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if loaded.Status != tasks.TaskStatusCompleted {
		t.Fatalf("status = %q, want completed", loaded.Status)
	}
}

func TestWS_WaitingLifecycle(t *testing.T) {
	prov := &stubProvider{response: &types.ChatResponse{}}
	srv, _, taskStore := newTestServerWithTasks(t, prov)
	conn := dialWS(t, srv, "waiting-sierra")

	sendRPC(t, conn, "1", "waiting.create", map[string]any{
		"title":        "Await recruiter confirmation",
		"waiting_for":  "recruiter",
		"follow_up_at": time.Now().Add(24 * time.Hour).Unix(),
	})
	msg := readUntilID(t, conn, "1")
	if msg.Error != nil {
		t.Fatalf("waiting.create error: %+v", msg.Error)
	}

	items, err := taskStore.ListWaitingOns(context.Background(), "waiting-sierra", 10, tasks.WaitingStatusOpen)
	if err != nil {
		t.Fatalf("ListWaitingOns: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("waiting records = %d, want 1", len(items))
	}

	sendRPC(t, conn, "2", "waiting.resolve", map[string]any{"id": items[0].ID})
	msg = readUntilID(t, conn, "2")
	if msg.Error != nil {
		t.Fatalf("waiting.resolve error: %+v", msg.Error)
	}

	loaded, err := taskStore.GetWaitingOn(context.Background(), "waiting-sierra", items[0].ID)
	if err != nil {
		t.Fatalf("GetWaitingOn: %v", err)
	}
	if loaded.Status != tasks.WaitingStatusResolved {
		t.Fatalf("status = %q, want resolved", loaded.Status)
	}
}

func TestWS_CalendarAgendaLifecycle(t *testing.T) {
	prov := &stubProvider{response: &types.ChatResponse{}}
	srv, _, calendarStore := newTestServerWithCalendar(t, prov)
	conn := dialWS(t, srv, "calendar-zulu")

	icsPath := filepath.Join(t.TempDir(), "agenda.ics")
	ics := "BEGIN:VCALENDAR\nVERSION:2.0\nBEGIN:VEVENT\nUID:evt-1\nSUMMARY:Design review\nDTSTART:20260424T090000Z\nDTEND:20260424T100000Z\nEND:VEVENT\nEND:VCALENDAR\n"
	if err := os.WriteFile(icsPath, []byte(ics), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	sendRPC(t, conn, "1", "calendar.source.create", map[string]any{"name": "Work", "path": icsPath})
	msg := readUntilID(t, conn, "1")
	if msg.Error != nil {
		t.Fatalf("calendar.source.create error: %+v", msg.Error)
	}

	sources, err := calendarStore.ListSources(context.Background(), "calendar-zulu", false)
	if err != nil {
		t.Fatalf("ListSources: %v", err)
	}
	if len(sources) != 1 {
		t.Fatalf("sources = %d, want 1", len(sources))
	}

	sendRPC(t, conn, "2", "calendar.agenda", map[string]any{"scope": "today", "timezone": "UTC", "now": time.Date(2026, 4, 24, 8, 0, 0, 0, time.UTC).Unix()})
	msg = readUntilID(t, conn, "2")
	if msg.Error != nil {
		t.Fatalf("calendar.agenda error: %+v", msg.Error)
	}
	var result struct {
		Agenda struct {
			Count  int `json:"count"`
			Events []struct {
				Summary string `json:"summary"`
			} `json:"events"`
		} `json:"agenda"`
	}
	if err := json.Unmarshal(msg.Result, &result); err != nil {
		t.Fatalf("unmarshal calendar.agenda result: %v", err)
	}
	if result.Agenda.Count != 1 {
		t.Fatalf("agenda count = %d, want 1", result.Agenda.Count)
	}
	if len(result.Agenda.Events) != 1 || result.Agenda.Events[0].Summary != "Design review" {
		t.Fatalf("unexpected agenda events: %#v", result.Agenda.Events)
	}
}

func TestWS_ServerCapabilities(t *testing.T) {
	store := session.New(10)
	prov := &stubProvider{response: &types.ChatResponse{}}
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

	conn := dialWS(t, srv, "alice")
	sendRPC(t, conn, "caps1", "server.capabilities", nil)
	msg := readUntilID(t, conn, "caps1")
	if msg.Error != nil {
		t.Fatalf("unexpected error: %v", msg.Error)
	}

	var result struct {
		PeerID       string          `json:"peer_id"`
		Capabilities map[string]bool `json:"capabilities"`
		Methods      []string        `json:"methods"`
		ChatTools    []string        `json:"chat_tools"`
		Idempotency  struct {
			ParamsField string   `json:"params_field"`
			Methods     []string `json:"methods"`
		} `json:"idempotency"`
		StreamEvents []string `json:"stream_notifications"`
	}
	if err := json.Unmarshal(msg.Result, &result); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	if result.PeerID != "alice" {
		t.Fatalf("unexpected peer_id %q", result.PeerID)
	}
	if !result.Capabilities["agent_runtime"] || !result.Capabilities["cron"] {
		t.Fatalf("expected agent_runtime and cron to be enabled, got %#v", result.Capabilities)
	}
	if !containsString(result.Methods, "server.capabilities") || !containsString(result.Methods, "cron.create") {
		t.Fatalf("expected methods to include server.capabilities and cron.create, got %#v", result.Methods)
	}
	if !containsString(result.Methods, "session.history") {
		t.Fatalf("expected methods to include session.history, got %#v", result.Methods)
	}
	if !containsString(result.ChatTools, "cron.create") || !containsString(result.ChatTools, "time.now") {
		t.Fatalf("expected chat tools to include cron.create and time.now, got %#v", result.ChatTools)
	}
	if !containsString(result.ChatTools, "session.history") {
		t.Fatalf("expected chat tools to include session.history, got %#v", result.ChatTools)
	}
	if result.Idempotency.ParamsField != "idempotency_key" {
		t.Fatalf("expected idempotency params_field, got %#v", result.Idempotency)
	}
	if !containsString(result.Idempotency.Methods, "cron.create") || !containsString(result.Idempotency.Methods, "workspace.write") {
		t.Fatalf("expected idempotent methods in capabilities, got %#v", result.Idempotency.Methods)
	}
	if !containsString(result.StreamEvents, "stream.delta") || !containsString(result.StreamEvents, "stream.event") {
		t.Fatalf("expected stream notifications, got %#v", result.StreamEvents)
	}
}

func TestWS_ServerCapabilities_WorkspaceMethods(t *testing.T) {
	store := session.New(10)
	prov := &stubProvider{response: &types.ChatResponse{}}
	wsStore, err := workspace.New(t.TempDir(), true, 1024)
	if err != nil {
		t.Fatalf("workspace.New: %v", err)
	}
	h := handler.NewHandler(store, prov, handler.HandlerOptions{
		Model:          "test-model",
		Timeout:        5 * time.Second,
		WorkspaceStore: wsStore,
	})
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)

	conn := dialWS(t, srv, "alice")
	sendRPC(t, conn, "caps-workspace", "server.capabilities", nil)
	msg := readUntilID(t, conn, "caps-workspace")
	if msg.Error != nil {
		t.Fatalf("unexpected error: %v", msg.Error)
	}
	var result struct {
		Capabilities map[string]bool `json:"capabilities"`
		Methods      []string        `json:"methods"`
	}
	if err := json.Unmarshal(msg.Result, &result); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	if !result.Capabilities["workspace"] {
		t.Fatalf("expected workspace capability, got %#v", result.Capabilities)
	}
	if !containsString(result.Methods, "workspace.list") || !containsString(result.Methods, "workspace.head") || !containsString(result.Methods, "workspace.tail") || !containsString(result.Methods, "workspace.grep") || !containsString(result.Methods, "workspace.sort") || !containsString(result.Methods, "workspace.uniq") || !containsString(result.Methods, "workspace.diff") || !containsString(result.Methods, "workspace.write") {
		t.Fatalf("expected workspace methods in capabilities, got %#v", result.Methods)
	}
}

func TestWS_WorkspaceRPC(t *testing.T) {
	store := session.New(10)
	prov := &stubProvider{response: &types.ChatResponse{}}
	wsStore, err := workspace.New(t.TempDir(), true, 1024)
	if err != nil {
		t.Fatalf("workspace.New: %v", err)
	}
	h := handler.NewHandler(store, prov, handler.HandlerOptions{
		Model:          "test-model",
		Timeout:        5 * time.Second,
		WorkspaceStore: wsStore,
	})
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)

	conn := dialWS(t, srv, "alice")

	sendRPC(t, conn, "w1", "workspace.mkdir", map[string]any{"path": "project"})
	msg := readUntilID(t, conn, "w1")
	if msg.Error != nil {
		t.Fatalf("workspace.mkdir error: %#v", msg.Error)
	}

	sendRPC(t, conn, "w2", "workspace.write", map[string]any{"path": "project/readme.txt", "content": "hello"})
	msg = readUntilID(t, conn, "w2")
	if msg.Error != nil {
		t.Fatalf("workspace.write error: %#v", msg.Error)
	}

	sendRPC(t, conn, "w3", "workspace.read", map[string]any{"path": "project/readme.txt"})
	msg = readUntilID(t, conn, "w3")
	if msg.Error != nil {
		t.Fatalf("workspace.read error: %#v", msg.Error)
	}
	var readRes struct {
		Path    string `json:"path"`
		Content string `json:"content"`
	}
	if err := json.Unmarshal(msg.Result, &readRes); err != nil {
		t.Fatalf("unmarshal workspace.read result: %v", err)
	}
	if readRes.Content != "hello" {
		t.Fatalf("unexpected content: %q", readRes.Content)
	}

	sendRPC(t, conn, "w3b", "workspace.write", map[string]any{"path": "project/range.txt", "content": "one\ntwo\nthree\nfour\n"})
	msg = readUntilID(t, conn, "w3b")
	if msg.Error != nil {
		t.Fatalf("workspace.write range file error: %#v", msg.Error)
	}

	sendRPC(t, conn, "w3d", "workspace.head", map[string]any{"path": "project/range.txt", "lines": 2})
	msg = readUntilID(t, conn, "w3d")
	if msg.Error != nil {
		t.Fatalf("workspace.head error: %#v", msg.Error)
	}
	var headRes struct {
		Content   string `json:"content"`
		StartLine int    `json:"start_line"`
		EndLine   int    `json:"end_line"`
	}
	if err := json.Unmarshal(msg.Result, &headRes); err != nil {
		t.Fatalf("unmarshal workspace.head result: %v", err)
	}
	if headRes.Content != "one\ntwo\n" || headRes.StartLine != 1 || headRes.EndLine != 2 {
		t.Fatalf("unexpected head result: %#v", headRes)
	}

	sendRPC(t, conn, "w3e", "workspace.tail", map[string]any{"path": "project/range.txt", "lines": 2})
	msg = readUntilID(t, conn, "w3e")
	if msg.Error != nil {
		t.Fatalf("workspace.tail error: %#v", msg.Error)
	}
	var tailRes struct {
		Content   string `json:"content"`
		StartLine int    `json:"start_line"`
		EndLine   int    `json:"end_line"`
	}
	if err := json.Unmarshal(msg.Result, &tailRes); err != nil {
		t.Fatalf("unmarshal workspace.tail result: %v", err)
	}
	if tailRes.Content != "three\nfour\n" || tailRes.StartLine != 3 || tailRes.EndLine != 4 {
		t.Fatalf("unexpected tail result: %#v", tailRes)
	}

	sendRPC(t, conn, "w3c", "workspace.read", map[string]any{"path": "project/range.txt", "start_line": 2, "end_line": 3})
	msg = readUntilID(t, conn, "w3c")
	if msg.Error != nil {
		t.Fatalf("workspace.read range error: %#v", msg.Error)
	}
	var rangeRes struct {
		Content    string `json:"content"`
		StartLine  int    `json:"start_line"`
		EndLine    int    `json:"end_line"`
		TotalLines int    `json:"total_lines"`
	}
	if err := json.Unmarshal(msg.Result, &rangeRes); err != nil {
		t.Fatalf("unmarshal workspace.read range result: %v", err)
	}
	if rangeRes.Content != "two\nthree\n" {
		t.Fatalf("unexpected range content: %q", rangeRes.Content)
	}
	if rangeRes.StartLine != 2 || rangeRes.EndLine != 3 || rangeRes.TotalLines != 4 {
		t.Fatalf("unexpected range metadata: %#v", rangeRes)
	}

	sendRPC(t, conn, "w4", "workspace.list", map[string]any{"path": "project"})
	msg = readUntilID(t, conn, "w4")
	if msg.Error != nil {
		t.Fatalf("workspace.list error: %#v", msg.Error)
	}
	var listRes struct {
		Count   int `json:"count"`
		Entries []struct {
			Path string `json:"path"`
		} `json:"entries"`
	}
	if err := json.Unmarshal(msg.Result, &listRes); err != nil {
		t.Fatalf("unmarshal workspace.list result: %v", err)
	}
	if listRes.Count == 0 {
		t.Fatalf("expected at least one workspace entry")
	}

	sendRPC(t, conn, "w4b", "workspace.grep", map[string]any{"path": "project", "pattern": "three", "recursive": true})
	msg = readUntilID(t, conn, "w4b")
	if msg.Error != nil {
		t.Fatalf("workspace.grep error: %#v", msg.Error)
	}
	var grepRes struct {
		Count   int `json:"count"`
		Matches []struct {
			Path   string `json:"path"`
			Line   int    `json:"line"`
			Column int    `json:"column"`
			Text   string `json:"text"`
		} `json:"matches"`
	}
	if err := json.Unmarshal(msg.Result, &grepRes); err != nil {
		t.Fatalf("unmarshal workspace.grep result: %v", err)
	}
	if grepRes.Count != 1 || len(grepRes.Matches) != 1 {
		t.Fatalf("unexpected grep result: %#v", grepRes)
	}
	if grepRes.Matches[0].Path != "project/range.txt" || grepRes.Matches[0].Line != 3 {
		t.Fatalf("unexpected grep match: %#v", grepRes.Matches[0])
	}

	sendRPC(t, conn, "w4d", "workspace.sort", map[string]any{"path": "project/range.txt"})
	msg = readUntilID(t, conn, "w4d")
	if msg.Error != nil {
		t.Fatalf("workspace.sort error: %#v", msg.Error)
	}
	var sortRes struct {
		Content string `json:"content"`
	}
	if err := json.Unmarshal(msg.Result, &sortRes); err != nil {
		t.Fatalf("unmarshal workspace.sort result: %v", err)
	}
	if sortRes.Content != "four\none\nthree\ntwo\n" {
		t.Fatalf("unexpected sort result: %#v", sortRes)
	}

	sendRPC(t, conn, "w4e0", "workspace.write", map[string]any{"path": "project/uniq.txt", "content": "a\na\nb\nb\nb\n"})
	msg = readUntilID(t, conn, "w4e0")
	if msg.Error != nil {
		t.Fatalf("workspace.write uniq file error: %#v", msg.Error)
	}

	sendRPC(t, conn, "w4e", "workspace.uniq", map[string]any{"path": "project/uniq.txt", "count": true})
	msg = readUntilID(t, conn, "w4e")
	if msg.Error != nil {
		t.Fatalf("workspace.uniq error: %#v", msg.Error)
	}
	var uniqRes struct {
		Content string `json:"content"`
	}
	if err := json.Unmarshal(msg.Result, &uniqRes); err != nil {
		t.Fatalf("unmarshal workspace.uniq result: %v", err)
	}
	if uniqRes.Content != "2 a\n3 b\n" {
		t.Fatalf("unexpected uniq result: %#v", uniqRes)
	}

	sendRPC(t, conn, "w4c", "workspace.diff", map[string]any{"path": "project/range.txt", "content": "one\ntwo\nTHREE\nfour\n"})
	msg = readUntilID(t, conn, "w4c")
	if msg.Error != nil {
		t.Fatalf("workspace.diff error: %#v", msg.Error)
	}
	var diffRes struct {
		HasDiff bool   `json:"has_diff"`
		Diff    string `json:"diff"`
	}
	if err := json.Unmarshal(msg.Result, &diffRes); err != nil {
		t.Fatalf("unmarshal workspace.diff result: %v", err)
	}
	if !diffRes.HasDiff || !strings.Contains(diffRes.Diff, "-three") || !strings.Contains(diffRes.Diff, "+THREE") {
		t.Fatalf("unexpected diff result: %#v", diffRes)
	}

	sendRPC(t, conn, "w5", "workspace.delete", map[string]any{"path": "project/readme.txt"})
	msg = readUntilID(t, conn, "w5")
	if msg.Error != nil {
		t.Fatalf("workspace.delete error: %#v", msg.Error)
	}
}

func TestExecuteTool_ApplyPatch(t *testing.T) {
	store := session.New(10)
	prov := &stubProvider{response: &types.ChatResponse{}}
	wsStore, err := workspace.New(t.TempDir(), true, 1024)
	if err != nil {
		t.Fatalf("workspace.New: %v", err)
	}
	if _, err := wsStore.Write("alice", "notes/todo.md", "one\ntwo\n", false); err != nil {
		t.Fatalf("workspace write setup: %v", err)
	}
	h := handler.NewHandler(store, prov, handler.HandlerOptions{
		Model:          "test-model",
		Timeout:        5 * time.Second,
		WorkspaceStore: wsStore,
	})

	result, err := h.ExecuteTool(context.Background(), "alice", agent.ToolCall{
		Name:      "apply_patch",
		Arguments: json.RawMessage(`{"patch":"*** Begin Patch\n*** Update File: notes/todo.md\n@@\n one\n-two\n+TWO\n*** End Patch"}`),
	})
	if err != nil {
		t.Fatalf("ExecuteTool(apply_patch): %v", err)
	}
	var got struct {
		OK     bool `json:"ok"`
		Result struct {
			Count int `json:"count"`
			Files []struct {
				Path   string `json:"path"`
				Action string `json:"action"`
			} `json:"files"`
		} `json:"result"`
	}
	raw, _ := json.Marshal(result)
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal apply_patch result: %v", err)
	}
	if !got.OK || got.Result.Count != 1 || len(got.Result.Files) != 1 {
		t.Fatalf("unexpected apply_patch result: %#v", got)
	}
	if got.Result.Files[0].Action != "update" || got.Result.Files[0].Path != "notes/todo.md" {
		t.Fatalf("unexpected file result: %#v", got.Result.Files[0])
	}
	content, err := wsStore.Read("alice", "notes/todo.md")
	if err != nil {
		t.Fatalf("read patched content: %v", err)
	}
	if content != "one\nTWO\n" {
		t.Fatalf("unexpected patched content: %q", content)
	}
}

func TestWS_MemoryInsertAndGetRPC(t *testing.T) {
	store := session.New(10)
	prov := &stubProvider{response: &types.ChatResponse{}}
	memStore, err := memory.New(filepath.Join(t.TempDir(), "memory.db"), nil)
	if err != nil {
		t.Fatalf("memory.New: %v", err)
	}
	t.Cleanup(func() { _ = memStore.Close() })
	h := handler.NewHandler(store, prov, handler.HandlerOptions{
		Model:    "test-model",
		Timeout:  5 * time.Second,
		MemStore: memStore,
	})
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)

	conn := dialWS(t, srv, "alice")
	sendRPC(t, conn, "m1", "memory.insert", map[string]any{
		"content":         "remember this",
		"retention_class": "pinned",
		"exposure_policy": "auto",
	})
	msg := readUntilID(t, conn, "m1")
	if msg.Error != nil {
		t.Fatalf("memory.insert error: %#v", msg.Error)
	}
	var insertRes struct {
		Chunk struct {
			ID             string `json:"id"`
			Content        string `json:"content"`
			RetentionClass string `json:"retention_class"`
		} `json:"chunk"`
	}
	if err := json.Unmarshal(msg.Result, &insertRes); err != nil {
		t.Fatalf("unmarshal memory.insert result: %v", err)
	}
	if insertRes.Chunk.ID == "" {
		t.Fatal("expected inserted chunk id")
	}
	if insertRes.Chunk.RetentionClass != "pinned" {
		t.Fatalf("unexpected retention class: %q", insertRes.Chunk.RetentionClass)
	}

	sendRPC(t, conn, "m2", "memory.get", map[string]any{"id": insertRes.Chunk.ID})
	msg = readUntilID(t, conn, "m2")
	if msg.Error != nil {
		t.Fatalf("memory.get error: %#v", msg.Error)
	}
	var getRes struct {
		Chunk struct {
			ID             string `json:"id"`
			Content        string `json:"content"`
			RetentionClass string `json:"retention_class"`
		} `json:"chunk"`
	}
	if err := json.Unmarshal(msg.Result, &getRes); err != nil {
		t.Fatalf("unmarshal memory.get result: %v", err)
	}
	if getRes.Chunk.Content != "remember this" {
		t.Fatalf("unexpected memory chunk content: %q", getRes.Chunk.Content)
	}
	if getRes.Chunk.RetentionClass != "pinned" {
		t.Fatalf("unexpected fetched retention class: %q", getRes.Chunk.RetentionClass)
	}
}

func TestWS_MemoryCandidateLifecycleRPC(t *testing.T) {
	store := session.New(10)
	prov := &stubProvider{response: &types.ChatResponse{}}
	memStore, err := memory.New(filepath.Join(t.TempDir(), "memory.db"), nil)
	if err != nil {
		t.Fatalf("memory.New: %v", err)
	}
	t.Cleanup(func() { _ = memStore.Close() })
	h := handler.NewHandler(store, prov, handler.HandlerOptions{
		Model:    "test-model",
		Timeout:  5 * time.Second,
		MemStore: memStore,
	})
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)

	conn := dialWS(t, srv, "alice")
	sendRPC(t, conn, "c1", "memory.candidate.create", map[string]any{"content": "review this fact"})
	msg := readUntilID(t, conn, "c1")
	if msg.Error != nil {
		t.Fatalf("memory.candidate.create error: %#v", msg.Error)
	}
	var createRes struct {
		Candidate struct {
			ID     string `json:"id"`
			Status string `json:"status"`
		} `json:"candidate"`
	}
	if err := json.Unmarshal(msg.Result, &createRes); err != nil {
		t.Fatalf("unmarshal memory.candidate.create: %v", err)
	}
	if createRes.Candidate.Status != "pending" {
		t.Fatalf("status = %q, want pending", createRes.Candidate.Status)
	}

	sendRPC(t, conn, "c2", "memory.candidate.approve", map[string]any{"id": createRes.Candidate.ID})
	msg = readUntilID(t, conn, "c2")
	if msg.Error != nil {
		t.Fatalf("memory.candidate.approve error: %#v", msg.Error)
	}
	var approveRes struct {
		Candidate struct {
			Status string `json:"status"`
		} `json:"candidate"`
		Chunk struct {
			ID      string `json:"id"`
			Content string `json:"content"`
		} `json:"chunk"`
	}
	if err := json.Unmarshal(msg.Result, &approveRes); err != nil {
		t.Fatalf("unmarshal memory.candidate.approve: %v", err)
	}
	if approveRes.Candidate.Status != "approved" {
		t.Fatalf("candidate status = %q, want approved", approveRes.Candidate.Status)
	}
	if approveRes.Chunk.Content != "review this fact" {
		t.Fatalf("chunk content = %q, want review this fact", approveRes.Chunk.Content)
	}
}

func TestWS_MemoryEntityGraphRPC(t *testing.T) {
	store := session.New(10)
	prov := &stubProvider{response: &types.ChatResponse{}}
	memStore, err := memory.New(filepath.Join(t.TempDir(), "memory.db"), nil)
	if err != nil {
		t.Fatalf("memory.New: %v", err)
	}
	t.Cleanup(func() { _ = memStore.Close() })
	h := handler.NewHandler(store, prov, handler.HandlerOptions{
		Model:    "test-model",
		Timeout:  5 * time.Second,
		MemStore: memStore,
	})
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)

	conn := dialWS(t, srv, "alice")
	sendRPC(t, conn, "e-chunk", "memory.insert", map[string]any{"content": "Borealis trip is blocked on venue approval."})
	msg := readUntilID(t, conn, "e-chunk")
	if msg.Error != nil {
		t.Fatalf("memory.insert error: %#v", msg.Error)
	}
	var chunkRes struct {
		Chunk struct {
			ID string `json:"id"`
		} `json:"chunk"`
	}
	if err := json.Unmarshal(msg.Result, &chunkRes); err != nil {
		t.Fatalf("unmarshal chunk result: %v", err)
	}

	sendRPC(t, conn, "e1", "memory.entity.create", map[string]any{"kind": "project", "name": "Borealis Trip", "aliases": []string{"summer trip"}})
	msg = readUntilID(t, conn, "e1")
	if msg.Error != nil {
		t.Fatalf("memory.entity.create project error: %#v", msg.Error)
	}
	var projectRes struct {
		Entity struct {
			ID string `json:"id"`
		} `json:"entity"`
	}
	if err := json.Unmarshal(msg.Result, &projectRes); err != nil {
		t.Fatalf("unmarshal project entity: %v", err)
	}

	sendRPC(t, conn, "e2", "memory.entity.create", map[string]any{"kind": "person", "name": "Alice Example"})
	msg = readUntilID(t, conn, "e2")
	if msg.Error != nil {
		t.Fatalf("memory.entity.create person error: %#v", msg.Error)
	}
	var personRes struct {
		Entity struct {
			ID string `json:"id"`
		} `json:"entity"`
	}
	if err := json.Unmarshal(msg.Result, &personRes); err != nil {
		t.Fatalf("unmarshal person entity: %v", err)
	}

	sendRPC(t, conn, "e3", "memory.entity.link_chunk", map[string]any{"id": projectRes.Entity.ID, "chunk_id": chunkRes.Chunk.ID})
	msg = readUntilID(t, conn, "e3")
	if msg.Error != nil {
		t.Fatalf("memory.entity.link_chunk error: %#v", msg.Error)
	}

	sendRPC(t, conn, "e4", "memory.entity.relate", map[string]any{"source_id": projectRes.Entity.ID, "target_id": personRes.Entity.ID, "relation": "owned_by", "notes": "Alice is coordinating the trip."})
	msg = readUntilID(t, conn, "e4")
	if msg.Error != nil {
		t.Fatalf("memory.entity.relate error: %#v", msg.Error)
	}

	sendRPC(t, conn, "e5", "memory.entity.get", map[string]any{"id": projectRes.Entity.ID})
	msg = readUntilID(t, conn, "e5")
	if msg.Error != nil {
		t.Fatalf("memory.entity.get error: %#v", msg.Error)
	}
	var graphRes struct {
		EntityGraph struct {
			Entity struct {
				ID string `json:"id"`
			} `json:"entity"`
			LinkedChunks []struct {
				ID string `json:"id"`
			} `json:"linked_chunks"`
			Outgoing []struct {
				Relation       string `json:"relation"`
				TargetEntityID string `json:"target_entity_id"`
			} `json:"outgoing_relationships"`
		} `json:"entity_graph"`
	}
	if err := json.Unmarshal(msg.Result, &graphRes); err != nil {
		t.Fatalf("unmarshal entity graph result: %v", err)
	}
	if graphRes.EntityGraph.Entity.ID != projectRes.Entity.ID {
		t.Fatalf("unexpected entity id: %#v", graphRes.EntityGraph.Entity)
	}
	if len(graphRes.EntityGraph.LinkedChunks) != 1 || graphRes.EntityGraph.LinkedChunks[0].ID != chunkRes.Chunk.ID {
		t.Fatalf("unexpected linked chunks: %#v", graphRes.EntityGraph.LinkedChunks)
	}
	if len(graphRes.EntityGraph.Outgoing) != 1 || graphRes.EntityGraph.Outgoing[0].Relation != "owned_by" || graphRes.EntityGraph.Outgoing[0].TargetEntityID != personRes.Entity.ID {
		t.Fatalf("unexpected outgoing relationships: %#v", graphRes.EntityGraph.Outgoing)
	}

	sendRPC(t, conn, "e6", "memory.entity.unlink_chunk", map[string]any{"id": projectRes.Entity.ID, "chunk_id": chunkRes.Chunk.ID})
	msg = readUntilID(t, conn, "e6")
	if msg.Error != nil {
		t.Fatalf("memory.entity.unlink_chunk error: %#v", msg.Error)
	}

	sendRPC(t, conn, "e7", "memory.entity.unrelate", map[string]any{"source_id": projectRes.Entity.ID, "target_id": personRes.Entity.ID, "relation": "owned_by"})
	msg = readUntilID(t, conn, "e7")
	if msg.Error != nil {
		t.Fatalf("memory.entity.unrelate error: %#v", msg.Error)
	}

	sendRPC(t, conn, "e8", "memory.entity.delete", map[string]any{"id": personRes.Entity.ID})
	msg = readUntilID(t, conn, "e8")
	if msg.Error != nil {
		t.Fatalf("memory.entity.delete error: %#v", msg.Error)
	}

	sendRPC(t, conn, "e9", "memory.entity.get", map[string]any{"id": projectRes.Entity.ID})
	msg = readUntilID(t, conn, "e9")
	if msg.Error != nil {
		t.Fatalf("memory.entity.get after unlink error: %#v", msg.Error)
	}
	var updatedGraphRes struct {
		EntityGraph struct {
			Entity struct {
				ID string `json:"id"`
			} `json:"entity"`
			LinkedChunks []struct {
				ID string `json:"id"`
			} `json:"linked_chunks"`
			Outgoing []struct {
				Relation       string `json:"relation"`
				TargetEntityID string `json:"target_entity_id"`
			} `json:"outgoing_relationships"`
		} `json:"entity_graph"`
	}
	if err := json.Unmarshal(msg.Result, &updatedGraphRes); err != nil {
		t.Fatalf("unmarshal entity graph result after unlink: %v", err)
	}
	if len(updatedGraphRes.EntityGraph.LinkedChunks) != 0 {
		t.Fatalf("unexpected linked chunks after unlink: %#v", updatedGraphRes.EntityGraph.LinkedChunks)
	}
	if len(updatedGraphRes.EntityGraph.Outgoing) != 0 {
		t.Fatalf("unexpected outgoing relationships after unrelate: %#v", updatedGraphRes.EntityGraph.Outgoing)
	}
}

func TestWS_IdempotencyReplayForCronCreate(t *testing.T) {
	store := session.New(10)
	prov := &stubProvider{response: &types.ChatResponse{}}
	jobStore, err := scheduler.NewJobStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewJobStore: %v", err)
	}
	sched := scheduler.New(jobStore, prov, store, nil, "test-model", 1)
	h := handler.NewHandler(store, prov, handler.HandlerOptions{
		Model:    "test-model",
		Timeout:  5 * time.Second,
		JobStore: jobStore,
		Sched:    sched,
	})
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)

	conn := dialWS(t, srv, "alice")
	params := map[string]any{
		"idempotency_key": "cron-create-1",
		"name":            "nightly",
		"schedule": map[string]any{
			"kind":     "every",
			"every_ms": 60000,
		},
		"payload": map[string]any{
			"kind": "systemEvent",
			"text": "tick",
		},
	}

	sendRPC(t, conn, "c1", "cron.create", params)
	first := readUntilID(t, conn, "c1")
	if first.Error != nil {
		t.Fatalf("first cron.create error: %#v", first.Error)
	}

	sendRPC(t, conn, "c2", "cron.create", params)
	second := readUntilID(t, conn, "c2")
	if second.Error != nil {
		t.Fatalf("second cron.create error: %#v", second.Error)
	}

	var firstJob, secondJob struct {
		JobID string `json:"job_id"`
		Name  string `json:"name"`
	}
	if err := json.Unmarshal(first.Result, &firstJob); err != nil {
		t.Fatalf("unmarshal first cron.create: %v", err)
	}
	if err := json.Unmarshal(second.Result, &secondJob); err != nil {
		t.Fatalf("unmarshal second cron.create: %v", err)
	}
	if firstJob.JobID == "" || firstJob.JobID != secondJob.JobID {
		t.Fatalf("expected replayed cron job result, got first=%#v second=%#v", firstJob, secondJob)
	}
	jobs := jobStore.List("alice")
	if len(jobs) != 1 {
		t.Fatalf("expected exactly one job after idempotent replay, got %d", len(jobs))
	}
}

func TestWS_IdempotencyRejectsChangedParams(t *testing.T) {
	store := session.New(10)
	prov := &stubProvider{response: &types.ChatResponse{}}
	wsStore, err := workspace.New(t.TempDir(), true, 1024)
	if err != nil {
		t.Fatalf("workspace.New: %v", err)
	}
	h := handler.NewHandler(store, prov, handler.HandlerOptions{
		Model:          "test-model",
		Timeout:        5 * time.Second,
		WorkspaceStore: wsStore,
	})
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)

	conn := dialWS(t, srv, "alice")
	sendRPC(t, conn, "i1", "workspace.mkdir", map[string]any{
		"idempotency_key": "mkdir-1",
		"path":            "project-a",
	})
	first := readUntilID(t, conn, "i1")
	if first.Error != nil {
		t.Fatalf("first workspace.mkdir error: %#v", first.Error)
	}

	sendRPC(t, conn, "i2", "workspace.mkdir", map[string]any{
		"idempotency_key": "mkdir-1",
		"path":            "project-b",
	})
	second := readUntilID(t, conn, "i2")
	if second.Error == nil {
		t.Fatal("expected idempotency params mismatch error")
	}
	if !strings.Contains(second.Error.Message, "idempotency_key cannot be reused") {
		t.Fatalf("unexpected error message: %#v", second.Error)
	}
}

func TestWS_ExecApprovalFlow(t *testing.T) {
	store := session.New(10)
	prov := &stubProvider{response: &types.ChatResponse{}}
	wsStore, err := workspace.New(t.TempDir(), true, 2048)
	if err != nil {
		t.Fatalf("workspace.New: %v", err)
	}
	if _, err := wsStore.Write("alice", "doomed/file.txt", "bye", false); err != nil {
		t.Fatalf("workspace write setup: %v", err)
	}
	h := handler.NewHandler(store, prov, handler.HandlerOptions{
		Model:          "test-model",
		Timeout:        5 * time.Second,
		WorkspaceStore: wsStore,
		ExecConfig: handler.ExecConfig{
			DefaultTimeout: 5 * time.Second,
			MaxTimeout:     10 * time.Second,
			ApprovalMode:   "dangerous",
			ApprovalTTL:    time.Minute,
		},
	})
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)

	conn := dialWS(t, srv, "alice")
	sendRPC(t, conn, "e1", "exec", map[string]any{"command": "rm -rf doomed"})
	msg := readUntilID(t, conn, "e1")
	if msg.Error != nil {
		t.Fatalf("exec error: %#v", msg.Error)
	}
	var execRes struct {
		Status   string `json:"status"`
		Approval struct {
			ID string `json:"id"`
		} `json:"approval"`
	}
	if err := json.Unmarshal(msg.Result, &execRes); err != nil {
		t.Fatalf("unmarshal exec result: %v", err)
	}
	if execRes.Status != "approval_required" || execRes.Approval.ID == "" {
		t.Fatalf("expected approval_required result, got %#v", execRes)
	}

	sendRPC(t, conn, "e2", "exec.approve", map[string]any{"id": execRes.Approval.ID})
	msg = readUntilID(t, conn, "e2")
	if msg.Error != nil {
		t.Fatalf("exec.approve error: %#v", msg.Error)
	}
	if _, err := wsStore.Read("alice", "doomed/file.txt"); err == nil {
		t.Fatal("expected approved exec to remove workspace file")
	}
}

func TestToolDefinitionsHonorPolicy(t *testing.T) {
	store := session.New(10)
	prov := &stubProvider{response: &types.ChatResponse{}}
	wsStore, err := workspace.New(t.TempDir(), true, 1024)
	if err != nil {
		t.Fatalf("workspace.New: %v", err)
	}
	memStore, err := memory.New(filepath.Join(t.TempDir(), "memory.db"), nil)
	if err != nil {
		t.Fatalf("memory.New: %v", err)
	}
	t.Cleanup(func() { _ = memStore.Close() })
	h := handler.NewHandler(store, prov, handler.HandlerOptions{
		Model:          "test-model",
		Timeout:        5 * time.Second,
		WorkspaceStore: wsStore,
		MemStore:       memStore,
		ToolPolicy: handler.ToolPolicy{
			Allow: []string{"read", "memory.get", "time.now"},
			Deny:  []string{"memory.get"},
		},
	})
	names := []string{}
	for _, tool := range h.ToolDefinitions("alice") {
		names = append(names, tool.Function.Name)
	}
	if !containsString(names, "read") {
		t.Fatalf("expected read tool, got %#v", names)
	}
	if containsString(names, "memory.get") {
		t.Fatalf("expected memory.get to be denied, got %#v", names)
	}
	if containsString(names, "exec") {
		t.Fatalf("expected exec to be excluded by allow list, got %#v", names)
	}
}

func TestToolDefinitionsHonorCodeExecutionPolicy(t *testing.T) {
	store := session.New(10)
	prov := &stubProvider{response: &types.ChatResponse{}}
	wsStore, err := workspace.New(t.TempDir(), true, 1024)
	if err != nil {
		t.Fatalf("workspace.New: %v", err)
	}
	h := handler.NewHandler(store, prov, handler.HandlerOptions{
		Model:          "test-model",
		Timeout:        5 * time.Second,
		WorkspaceStore: wsStore,
		CodeExecutionConfig: handler.CodeExecutionConfig{
			Enabled: true,
		},
		ToolPolicy: handler.ToolPolicy{
			Allow: []string{"code_execution", "read"},
			Deny:  []string{"exec"},
		},
	})
	names := []string{}
	for _, tool := range h.ToolDefinitions("alice") {
		names = append(names, tool.Function.Name)
	}
	if !containsString(names, "code_execution") {
		t.Fatalf("expected code_execution to be exposed, got %#v", names)
	}
	if containsString(names, "exec") {
		t.Fatalf("expected exec to remain denied, got %#v", names)
	}
}

func TestToolDefinitionsRuntimeProfileIncludesCodeExecutionHelpers(t *testing.T) {
	store := session.New(10)
	prov := &stubProvider{response: &types.ChatResponse{}}
	wsStore, err := workspace.New(t.TempDir(), true, 1024)
	if err != nil {
		t.Fatalf("workspace.New: %v", err)
	}
	ledger, err := runledger.New(t.TempDir())
	if err != nil {
		t.Fatalf("runledger.New: %v", err)
	}
	t.Cleanup(func() { _ = ledger.Close() })
	h := handler.NewHandler(store, prov, handler.HandlerOptions{
		Model:          "test-model",
		Timeout:        5 * time.Second,
		WorkspaceStore: wsStore,
		RunLedger:      ledger,
		CodeExecutionConfig: handler.CodeExecutionConfig{
			Enabled: true,
		},
		ToolPolicy: handler.ToolPolicy{Profile: "coding"},
	})
	names := []string{}
	for _, tool := range h.ToolDefinitions("alice") {
		names = append(names, tool.Function.Name)
	}
	if !containsString(names, "code_execution.status") || !containsString(names, "code_execution.cancel") {
		t.Fatalf("expected code_execution helper tools, got %#v", names)
	}
}

func TestToolDefinitionsIncludeApplyPatch(t *testing.T) {
	store := session.New(10)
	prov := &stubProvider{response: &types.ChatResponse{}}
	wsStore, err := workspace.New(t.TempDir(), true, 1024)
	if err != nil {
		t.Fatalf("workspace.New: %v", err)
	}
	h := handler.NewHandler(store, prov, handler.HandlerOptions{
		Model:          "test-model",
		Timeout:        5 * time.Second,
		WorkspaceStore: wsStore,
	})
	names := []string{}
	for _, tool := range h.ToolDefinitions("alice") {
		names = append(names, tool.Function.Name)
	}
	if !containsString(names, "apply_patch") {
		t.Fatalf("expected apply_patch tool, got %#v", names)
	}
	if !containsString(names, "grep") || !containsString(names, "workspace.grep") {
		t.Fatalf("expected grep tools, got %#v", names)
	}
	if !containsString(names, "head") || !containsString(names, "workspace.head") || !containsString(names, "tail") || !containsString(names, "workspace.tail") {
		t.Fatalf("expected head/tail tools, got %#v", names)
	}
	if !containsString(names, "sort") || !containsString(names, "workspace.sort") || !containsString(names, "uniq") || !containsString(names, "workspace.uniq") {
		t.Fatalf("expected sort/uniq tools, got %#v", names)
	}
	if !containsString(names, "diff") || !containsString(names, "workspace.diff") {
		t.Fatalf("expected diff tools, got %#v", names)
	}
}

func TestToolDefinitionsMessagingProfileIncludesWaitingTools(t *testing.T) {
	store := session.New(10)
	prov := &stubProvider{response: &types.ChatResponse{}}
	taskStore, err := tasks.New(filepath.Join(t.TempDir(), "tasks.db"))
	if err != nil {
		t.Fatalf("tasks.New: %v", err)
	}
	t.Cleanup(func() { _ = taskStore.Close() })
	h := handler.NewHandler(store, prov, handler.HandlerOptions{
		Model:     "test-model",
		Timeout:   5 * time.Second,
		TaskStore: taskStore,
		ToolPolicy: handler.ToolPolicy{
			Profile: "messaging",
		},
	})
	names := []string{}
	for _, tool := range h.ToolDefinitions("alice") {
		names = append(names, tool.Function.Name)
	}
	if !containsString(names, "waiting.create") || !containsString(names, "waiting.list") || !containsString(names, "waiting.resolve") {
		t.Fatalf("expected waiting tools in messaging profile, got %#v", names)
	}
	if containsString(names, "exec") {
		t.Fatalf("expected exec to remain excluded from messaging profile, got %#v", names)
	}
}

func TestExecuteToolWaitingLifecycle(t *testing.T) {
	store := session.New(10)
	prov := &stubProvider{response: &types.ChatResponse{}}
	taskStore, err := tasks.New(filepath.Join(t.TempDir(), "tasks.db"))
	if err != nil {
		t.Fatalf("tasks.New: %v", err)
	}
	t.Cleanup(func() { _ = taskStore.Close() })
	h := handler.NewHandler(store, prov, handler.HandlerOptions{
		Model:     "test-model",
		Timeout:   5 * time.Second,
		TaskStore: taskStore,
		ToolPolicy: handler.ToolPolicy{
			Allow: []string{"waiting.create", "waiting.list", "waiting.resolve"},
		},
	})

	createdAny, err := h.ExecuteTool(context.Background(), "alice", agent.ToolCall{
		Name:      "waiting.create",
		Arguments: json.RawMessage(`{"title":"Need vendor answer","waiting_for":"vendor","follow_up_at":1735689600}`),
	})
	if err != nil {
		t.Fatalf("ExecuteTool(waiting.create): %v", err)
	}
	raw, _ := json.Marshal(createdAny)
	var created struct {
		OK      bool            `json:"ok"`
		Waiting tasks.WaitingOn `json:"waiting"`
	}
	if err := json.Unmarshal(raw, &created); err != nil {
		t.Fatalf("unmarshal waiting.create result: %v", err)
	}
	if !created.OK || created.Waiting.ID == "" {
		t.Fatalf("unexpected waiting.create result: %#v", created)
	}

	listAny, err := h.ExecuteTool(context.Background(), "alice", agent.ToolCall{
		Name:      "waiting.list",
		Arguments: json.RawMessage(`{"status":"open","limit":10}`),
	})
	if err != nil {
		t.Fatalf("ExecuteTool(waiting.list): %v", err)
	}
	raw, _ = json.Marshal(listAny)
	var listed struct {
		Count   int               `json:"count"`
		Waiting []tasks.WaitingOn `json:"waiting"`
	}
	if err := json.Unmarshal(raw, &listed); err != nil {
		t.Fatalf("unmarshal waiting.list result: %v", err)
	}
	if listed.Count != 1 || len(listed.Waiting) != 1 {
		t.Fatalf("unexpected waiting.list result: %#v", listed)
	}

	resolvedAny, err := h.ExecuteTool(context.Background(), "alice", agent.ToolCall{
		Name:      "waiting.resolve",
		Arguments: json.RawMessage(fmt.Sprintf(`{"id":%q}`, created.Waiting.ID)),
	})
	if err != nil {
		t.Fatalf("ExecuteTool(waiting.resolve): %v", err)
	}
	raw, _ = json.Marshal(resolvedAny)
	var resolved struct {
		OK      bool            `json:"ok"`
		Waiting tasks.WaitingOn `json:"waiting"`
	}
	if err := json.Unmarshal(raw, &resolved); err != nil {
		t.Fatalf("unmarshal waiting.resolve result: %v", err)
	}
	if !resolved.OK || resolved.Waiting.Status != tasks.WaitingStatusResolved {
		t.Fatalf("unexpected waiting.resolve result: %#v", resolved)
	}
}

func TestExecuteToolCalendarAgenda(t *testing.T) {
	store := session.New(10)
	prov := &stubProvider{response: &types.ChatResponse{}}
	calendarStore, err := calendar.New(filepath.Join(t.TempDir(), "calendar.db"))
	if err != nil {
		t.Fatalf("calendar.New: %v", err)
	}
	t.Cleanup(func() { _ = calendarStore.Close() })
	icsPath := filepath.Join(t.TempDir(), "calendar.ics")
	ics := "BEGIN:VCALENDAR\nVERSION:2.0\nBEGIN:VEVENT\nUID:evt-1\nSUMMARY:Focus block\nDTSTART:20260424T130000Z\nDTEND:20260424T140000Z\nEND:VEVENT\nEND:VCALENDAR\n"
	if err := os.WriteFile(icsPath, []byte(ics), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if _, err := calendarStore.CreateSource(context.Background(), "alice", calendar.SourceInput{Name: "Work", Path: icsPath}); err != nil {
		t.Fatalf("CreateSource: %v", err)
	}
	h := handler.NewHandler(store, prov, handler.HandlerOptions{
		Model:         "test-model",
		Timeout:       5 * time.Second,
		CalendarStore: calendarStore,
		ToolPolicy: handler.ToolPolicy{
			Allow: []string{"calendar.agenda"},
		},
	})

	resultAny, err := h.ExecuteTool(context.Background(), "alice", agent.ToolCall{
		Name:      "calendar.agenda",
		Arguments: json.RawMessage(`{"scope":"today","timezone":"UTC","now":1777017600}`),
	})
	if err != nil {
		t.Fatalf("ExecuteTool(calendar.agenda): %v", err)
	}
	raw, _ := json.Marshal(resultAny)
	var result struct {
		Agenda struct {
			Count  int `json:"count"`
			Events []struct {
				Summary string `json:"summary"`
			} `json:"events"`
		} `json:"agenda"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatalf("unmarshal calendar.agenda result: %v", err)
	}
	if result.Agenda.Count != 1 || len(result.Agenda.Events) != 1 || result.Agenda.Events[0].Summary != "Focus block" {
		t.Fatalf("unexpected calendar.agenda result: %#v", result)
	}
}

func TestExecuteTool_TextOpsAliases(t *testing.T) {
	store := session.New(10)
	prov := &stubProvider{response: &types.ChatResponse{}}
	wsStore, err := workspace.New(t.TempDir(), true, 1024)
	if err != nil {
		t.Fatalf("workspace.New: %v", err)
	}
	if _, err := wsStore.Write("alice", "notes/list.txt", "delta\nbeta\nbeta\nAlpha\n", false); err != nil {
		t.Fatalf("workspace write setup: %v", err)
	}
	h := handler.NewHandler(store, prov, handler.HandlerOptions{
		Model:          "test-model",
		Timeout:        5 * time.Second,
		WorkspaceStore: wsStore,
	})

	headAny, err := h.ExecuteTool(context.Background(), "alice", agent.ToolCall{
		Name:      "head",
		Arguments: json.RawMessage(`{"path":"notes/list.txt","lines":2}`),
	})
	if err != nil {
		t.Fatalf("ExecuteTool(head): %v", err)
	}
	sortAny, err := h.ExecuteTool(context.Background(), "alice", agent.ToolCall{
		Name:      "sort",
		Arguments: json.RawMessage(`{"path":"notes/list.txt","case_sensitive":false}`),
	})
	if err != nil {
		t.Fatalf("ExecuteTool(sort): %v", err)
	}
	uniqAny, err := h.ExecuteTool(context.Background(), "alice", agent.ToolCall{
		Name:      "uniq",
		Arguments: json.RawMessage(`{"path":"notes/list.txt","count":true}`),
	})
	if err != nil {
		t.Fatalf("ExecuteTool(uniq): %v", err)
	}
	tailAny, err := h.ExecuteTool(context.Background(), "alice", agent.ToolCall{
		Name:      "tail",
		Arguments: json.RawMessage(`{"path":"notes/list.txt","lines":2}`),
	})
	if err != nil {
		t.Fatalf("ExecuteTool(tail): %v", err)
	}

	var headGot struct {
		Content string `json:"content"`
	}
	var sortGot struct {
		Content string `json:"content"`
	}
	var uniqGot struct {
		Content string `json:"content"`
	}
	var tailGot struct {
		Content string `json:"content"`
	}
	headRaw, _ := json.Marshal(headAny)
	sortRaw, _ := json.Marshal(sortAny)
	uniqRaw, _ := json.Marshal(uniqAny)
	tailRaw, _ := json.Marshal(tailAny)
	if err := json.Unmarshal(headRaw, &headGot); err != nil {
		t.Fatalf("unmarshal head result: %v", err)
	}
	if err := json.Unmarshal(sortRaw, &sortGot); err != nil {
		t.Fatalf("unmarshal sort result: %v", err)
	}
	if err := json.Unmarshal(uniqRaw, &uniqGot); err != nil {
		t.Fatalf("unmarshal uniq result: %v", err)
	}
	if err := json.Unmarshal(tailRaw, &tailGot); err != nil {
		t.Fatalf("unmarshal tail result: %v", err)
	}
	if headGot.Content != "delta\nbeta\n" {
		t.Fatalf("unexpected head result: %#v", headGot)
	}
	if sortGot.Content != "Alpha\nbeta\nbeta\ndelta\n" {
		t.Fatalf("unexpected sort result: %#v", sortGot)
	}
	if uniqGot.Content != "1 delta\n2 beta\n1 Alpha\n" {
		t.Fatalf("unexpected uniq result: %#v", uniqGot)
	}
	if tailGot.Content != "beta\nAlpha\n" {
		t.Fatalf("unexpected tail result: %#v", tailGot)
	}
}

func TestExecuteTool_GrepAlias(t *testing.T) {
	store := session.New(10)
	prov := &stubProvider{response: &types.ChatResponse{}}
	wsStore, err := workspace.New(t.TempDir(), true, 1024)
	if err != nil {
		t.Fatalf("workspace.New: %v", err)
	}
	if _, err := wsStore.Write("alice", "notes/todo.md", "TODO one\nskip\nTODO two\n", false); err != nil {
		t.Fatalf("workspace write setup: %v", err)
	}
	h := handler.NewHandler(store, prov, handler.HandlerOptions{
		Model:          "test-model",
		Timeout:        5 * time.Second,
		WorkspaceStore: wsStore,
	})

	result, err := h.ExecuteTool(context.Background(), "alice", agent.ToolCall{
		Name:      "grep",
		Arguments: json.RawMessage(`{"path":"notes","pattern":"TODO","recursive":true}`),
	})
	if err != nil {
		t.Fatalf("ExecuteTool(grep): %v", err)
	}
	raw, _ := json.Marshal(result)
	var got struct {
		Count   int `json:"count"`
		Matches []struct {
			Path string `json:"path"`
			Line int    `json:"line"`
		} `json:"matches"`
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal grep result: %v", err)
	}
	if got.Count != 2 || len(got.Matches) != 2 {
		t.Fatalf("unexpected grep result: %#v", got)
	}
}

func TestExecuteTool_DiffAlias(t *testing.T) {
	store := session.New(10)
	prov := &stubProvider{response: &types.ChatResponse{}}
	wsStore, err := workspace.New(t.TempDir(), true, 1024)
	if err != nil {
		t.Fatalf("workspace.New: %v", err)
	}
	if _, err := wsStore.Write("alice", "notes/todo.md", "one\ntwo\n", false); err != nil {
		t.Fatalf("workspace write setup: %v", err)
	}
	h := handler.NewHandler(store, prov, handler.HandlerOptions{
		Model:          "test-model",
		Timeout:        5 * time.Second,
		WorkspaceStore: wsStore,
	})

	result, err := h.ExecuteTool(context.Background(), "alice", agent.ToolCall{
		Name:      "diff",
		Arguments: json.RawMessage(`{"path":"notes/todo.md","content":"one\nTWO\n"}`),
	})
	if err != nil {
		t.Fatalf("ExecuteTool(diff): %v", err)
	}
	raw, _ := json.Marshal(result)
	var got struct {
		HasDiff bool   `json:"has_diff"`
		Diff    string `json:"diff"`
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal diff result: %v", err)
	}
	if !got.HasDiff || !strings.Contains(got.Diff, "-two") || !strings.Contains(got.Diff, "+TWO") {
		t.Fatalf("unexpected diff result: %#v", got)
	}
}

func TestExecuteTool_SystemRunUsesApprovalPolicy(t *testing.T) {
	store := session.New(10)
	prov := &stubProvider{response: &types.ChatResponse{}}
	wsStore, err := workspace.New(t.TempDir(), true, 1024)
	if err != nil {
		t.Fatalf("workspace.New: %v", err)
	}
	h := handler.NewHandler(store, prov, handler.HandlerOptions{
		Model:          "test-model",
		Timeout:        5 * time.Second,
		WorkspaceStore: wsStore,
		ExecConfig: handler.ExecConfig{
			Enabled:            true,
			EnableDenyPatterns: true,
			ApprovalMode:       "always",
		},
	})
	result, err := h.ExecuteTool(context.Background(), "alice", agent.ToolCall{
		Name:      "system.run",
		Arguments: json.RawMessage(`{"command":"echo hi"}`),
	})
	if err != nil {
		t.Fatalf("ExecuteTool(system.run): %v", err)
	}
	var got struct {
		Status string `json:"status"`
	}
	raw, _ := json.Marshal(result)
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal system.run result: %v", err)
	}
	if got.Status != "approval_required" {
		t.Fatalf("expected approval_required, got %#v", result)
	}
}

func TestExecuteTool_SessionsTools(t *testing.T) {
	store := session.New(20)
	gate := make(chan struct{})
	prov := &stubProvider{
		complete: func(ctx context.Context, _ *types.ChatRequest) (*types.ChatResponse, error) {
			select {
			case <-gate:
			case <-ctx.Done():
				return nil, ctx.Err()
			}
			return &types.ChatResponse{
				Choices: []types.ChatChoice{{
					Message: types.Message{Role: "assistant", Content: "child finished"},
				}},
			}, nil
		},
	}
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
		SubRuntime:   subrt,
	})

	spawnedAny, err := h.ExecuteTool(context.Background(), "alice", agent.ToolCall{
		Name:      "session.spawn",
		Arguments: json.RawMessage(`{"task":"review parser errors","reply_back":true}`),
	})
	if err != nil {
		t.Fatalf("ExecuteTool(session.spawn): %v", err)
	}
	rec, ok := spawnedAny.(*subagent.RunRecord)
	if !ok {
		t.Fatalf("expected *subagent.RunRecord, got %T", spawnedAny)
	}
	if !rec.ReplyBack {
		t.Fatalf("expected reply_back to be persisted on run record: %#v", rec)
	}
	if rec.SubTurn.ParentSessionKey != "alice" || rec.SubTurn.SourceSessionKey != "alice" {
		t.Fatalf("expected session.spawn to seed SubTurn parent/source session keys, got %#v", rec.SubTurn)
	}

	sentAny, err := h.ExecuteTool(context.Background(), "alice", agent.ToolCall{
		Name:      "session.send",
		Arguments: json.RawMessage(`{"run_id":"` + rec.ID + `","message":"focus on the lexer first","reply_back":false}`),
	})
	if err != nil {
		t.Fatalf("ExecuteTool(session.send): %v", err)
	}
	updated, ok := sentAny.(*subagent.RunRecord)
	if !ok {
		t.Fatalf("expected *subagent.RunRecord from session.send, got %T", sentAny)
	}
	if len(updated.Steering) != 1 || updated.Steering[0] != "focus on the lexer first" {
		t.Fatalf("unexpected steering messages: %#v", updated.Steering)
	}
	if updated.ReplyBack {
		t.Fatalf("expected reply_back to be updated to false: %#v", updated)
	}

	statusAny, err := h.ExecuteTool(context.Background(), "alice", agent.ToolCall{
		Name:      "subagent.status",
		Arguments: json.RawMessage(`{"id":"` + rec.ID + `"}`),
	})
	if err != nil {
		t.Fatalf("ExecuteTool(subagent.status): %v", err)
	}
	statusRec, ok := statusAny.(*subagent.RunRecord)
	if !ok {
		t.Fatalf("expected *subagent.RunRecord from subagent.status, got %T", statusAny)
	}
	if statusRec.ID != rec.ID {
		t.Fatalf("expected subagent.status to return run %q, got %#v", rec.ID, statusRec)
	}

	listAny, err := h.ExecuteTool(context.Background(), "alice", agent.ToolCall{
		Name:      "session.list",
		Arguments: json.RawMessage(`{}`),
	})
	if err != nil {
		t.Fatalf("ExecuteTool(session.list): %v", err)
	}
	var listRes struct {
		Count    int `json:"count"`
		Sessions []struct {
			Kind      string `json:"kind"`
			RunID     string `json:"run_id"`
			ReplyBack bool   `json:"reply_back"`
		} `json:"sessions"`
	}
	raw, _ := json.Marshal(listAny)
	if err := json.Unmarshal(raw, &listRes); err != nil {
		t.Fatalf("unmarshal session.list result: %v", err)
	}
	if listRes.Count < 2 {
		t.Fatalf("expected at least main + sub-session, got %#v", listRes)
	}
	foundSub := false
	for _, sess := range listRes.Sessions {
		if sess.RunID == rec.ID {
			foundSub = true
			if sess.ReplyBack {
				t.Fatalf("expected session.list to show updated reply_back=false: %#v", sess)
			}
		}
	}
	if !foundSub {
		t.Fatalf("expected spawned sub-session in list: %#v", listRes)
	}

	close(gate)
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		current, ok := subrt.Get(rec.ID)
		if ok && current.Status == subagent.StatusCompleted {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	historyAny, err := h.ExecuteTool(context.Background(), "alice", agent.ToolCall{
		Name:      "session.history",
		Arguments: json.RawMessage(`{"run_id":"` + rec.ID + `"}`),
	})
	if err != nil {
		t.Fatalf("ExecuteTool(session.history): %v", err)
	}
	var historyRes struct {
		RunID    string          `json:"run_id"`
		Count    int             `json:"count"`
		Messages []types.Message `json:"messages"`
	}
	raw, _ = json.Marshal(historyAny)
	if err := json.Unmarshal(raw, &historyRes); err != nil {
		t.Fatalf("unmarshal session.history result: %v", err)
	}
	if historyRes.RunID != rec.ID || historyRes.Count == 0 {
		t.Fatalf("unexpected session.history result: %#v", historyRes)
	}
}

func TestExecuteTool_SessionSendCrossSessionReplyBack(t *testing.T) {
	store := session.New(20)
	prov := &stubProvider{
		complete: func(_ context.Context, req *types.ChatRequest) (*types.ChatResponse, error) {
			last := req.Messages[len(req.Messages)-1].Content
			return &types.ChatResponse{
				Choices: []types.ChatChoice{{
					Message: types.Message{Role: "assistant", Content: "reply to: " + last},
				}},
				Usage: types.Usage{PromptTokens: 4, CompletionTokens: 5, TotalTokens: 9},
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

	result, err := h.ExecuteTool(context.Background(), "alice", agent.ToolCall{
		Name:      "session.send",
		Arguments: json.RawMessage(`{"session_key":"alice::sender::bob","message":"hello from main","reply_back":true}`),
	})
	if err != nil {
		t.Fatalf("ExecuteTool(session.send): %v", err)
	}
	var sendRes struct {
		SessionKey string `json:"session_key"`
		ReplyBack  bool   `json:"reply_back"`
		Result     struct {
			AssistantText string      `json:"assistant_text"`
			Usage         types.Usage `json:"usage"`
		} `json:"result"`
	}
	raw, _ := json.Marshal(result)
	if err := json.Unmarshal(raw, &sendRes); err != nil {
		t.Fatalf("unmarshal session.send result: %v", err)
	}
	if sendRes.SessionKey != "alice::sender::bob" || !sendRes.ReplyBack {
		t.Fatalf("unexpected session.send result: %#v", sendRes)
	}
	if sendRes.Result.AssistantText != "reply to: hello from main" || sendRes.Result.Usage.TotalTokens != 9 {
		t.Fatalf("unexpected nested result: %#v", sendRes.Result)
	}
	targetHistory := store.Get("alice::sender::bob").History()
	if len(targetHistory) != 2 {
		t.Fatalf("expected sent session history to contain user+assistant, got %#v", targetHistory)
	}
	sourceHistory := store.Get("alice").History()
	if len(sourceHistory) != 1 || !strings.Contains(sourceHistory[0].Content, "[reply:alice::sender::bob]") {
		t.Fatalf("expected mirrored reply in source history, got %#v", sourceHistory)
	}
	if !store.Policy("alice::sender::bob").ReplyBack {
		t.Fatal("expected reply_back policy to persist on target session")
	}
}

func TestExecuteTool_BriefGenerate(t *testing.T) {
	store := session.New(20)
	prov := &stubProvider{response: &types.ChatResponse{Choices: []types.ChatChoice{{Message: types.Message{Role: "assistant", Content: "ok"}}}}}
	memStore, err := memory.New(filepath.Join(t.TempDir(), "memory.db"), nil)
	if err != nil {
		t.Fatalf("memory.New: %v", err)
	}
	defer memStore.Close()
	taskStore, err := tasks.New(filepath.Join(t.TempDir(), "tasks.db"))
	if err != nil {
		t.Fatalf("tasks.New: %v", err)
	}
	defer taskStore.Close()
	rt := agent.NewRuntime(store, prov, "test-model", 5*time.Second, agent.RetryPolicy{MaxAttempts: 1})
	coord := agent.NewCoordinator(rt)
	defer coord.Stop()
	h := handler.NewHandler(store, prov, handler.HandlerOptions{Model: "test-model", Timeout: 5 * time.Second, AgentRuntime: rt, AgentCoord: coord, MemStore: memStore, TaskStore: taskStore})
	store.Append("alice", types.Message{Role: "user", Content: "We need to close the loop with finance."})
	if _, err := memStore.QueueCandidate(context.Background(), "alice", "Remember to keep summaries concise", memory.ChunkOptions{}); err != nil {
		t.Fatalf("QueueCandidate: %v", err)
	}

	resultAny, err := h.ExecuteTool(context.Background(), "alice", agent.ToolCall{Name: "brief.generate", Arguments: json.RawMessage(`{"kind":"daily","timezone":"UTC"}`)})
	if err != nil {
		t.Fatalf("ExecuteTool(brief.generate): %v", err)
	}
	raw, _ := json.Marshal(resultAny)
	var result struct {
		Text   string `json:"text"`
		Report struct {
			Kind string `json:"kind"`
		} `json:"report"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatalf("unmarshal brief.generate result: %v", err)
	}
	if result.Report.Kind != "daily" {
		t.Fatalf("unexpected brief kind: %#v", result.Report.Kind)
	}
	if !strings.Contains(result.Text, "Daily brief") {
		t.Fatalf("expected rendered brief text, got: %s", result.Text)
	}
}

func TestExecuteTool_SessionPatchUpdatesPolicy(t *testing.T) {
	store := session.New(20)
	prov := &stubProvider{response: &types.ChatResponse{}}
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
		Arguments: json.RawMessage(`{"session_key":"alice::sender::bob","reply_back":true}`),
	})
	if err != nil {
		t.Fatalf("ExecuteTool(session.patch session_key): %v", err)
	}
	var patchRes struct {
		OK         bool   `json:"ok"`
		SessionKey string `json:"session_key"`
		ReplyBack  bool   `json:"reply_back"`
	}
	raw, _ := json.Marshal(result)
	if err := json.Unmarshal(raw, &patchRes); err != nil {
		t.Fatalf("unmarshal session.patch result: %v", err)
	}
	if !patchRes.OK || patchRes.SessionKey != "alice::sender::bob" || !patchRes.ReplyBack {
		t.Fatalf("unexpected session.patch result: %#v", patchRes)
	}
	if !store.Policy("alice::sender::bob").ReplyBack {
		t.Fatal("expected patched reply_back policy on target session")
	}
}

func TestWS_SubagentStatusAlias(t *testing.T) {
	store := session.New(20)
	gate := make(chan struct{})
	prov := &stubProvider{
		complete: func(ctx context.Context, _ *types.ChatRequest) (*types.ChatResponse, error) {
			select {
			case <-gate:
			case <-ctx.Done():
				return nil, ctx.Err()
			}
			return &types.ChatResponse{
				Choices: []types.ChatChoice{{
					Message: types.Message{Role: "assistant", Content: "child finished"},
				}},
			}, nil
		},
	}
	rt := agent.NewRuntime(store, prov, "test-model", 5*time.Second, agent.RetryPolicy{MaxAttempts: 1})
	subrt := subagent.NewRuntime(rt, store, mustSubagentRegistry(t), nil, 4)
	h := handler.NewHandler(store, prov, handler.HandlerOptions{
		Model:        "test-model",
		Timeout:      5 * time.Second,
		AgentRuntime: rt,
		SubRuntime:   subrt,
	})
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)

	conn := dialWS(t, srv, "alice")
	sendRPC(t, conn, "sub-caps", "server.capabilities", nil)
	capsMsg := readUntilID(t, conn, "sub-caps")
	if capsMsg.Error != nil {
		t.Fatalf("unexpected capabilities error: %v", capsMsg.Error)
	}
	var caps struct {
		Methods   []string `json:"methods"`
		ChatTools []string `json:"chat_tools"`
	}
	if err := json.Unmarshal(capsMsg.Result, &caps); err != nil {
		t.Fatalf("unmarshal server.capabilities result: %v", err)
	}
	if !containsString(caps.Methods, "subagent.status") {
		t.Fatalf("expected methods to include subagent.status, got %#v", caps.Methods)
	}
	if !containsString(caps.ChatTools, "subagent.status") {
		t.Fatalf("expected chat tools to include subagent.status, got %#v", caps.ChatTools)
	}

	sendRPC(t, conn, "sub-spawn", "subagent.spawn", map[string]any{"task": "review parser errors"})
	spawnMsg := readUntilID(t, conn, "sub-spawn")
	if spawnMsg.Error != nil {
		t.Fatalf("unexpected spawn error: %v", spawnMsg.Error)
	}
	var rec subagent.RunRecord
	if err := json.Unmarshal(spawnMsg.Result, &rec); err != nil {
		t.Fatalf("unmarshal subagent.spawn result: %v", err)
	}

	sendRPC(t, conn, "sub-status", "subagent.status", map[string]any{"id": rec.ID})
	statusMsg := readUntilID(t, conn, "sub-status")
	if statusMsg.Error != nil {
		t.Fatalf("unexpected subagent.status error: %v", statusMsg.Error)
	}
	var statusRec subagent.RunRecord
	if err := json.Unmarshal(statusMsg.Result, &statusRec); err != nil {
		t.Fatalf("unmarshal subagent.status result: %v", err)
	}
	if statusRec.ID != rec.ID {
		t.Fatalf("expected subagent.status to return run %q, got %#v", rec.ID, statusRec)
	}

	sendRPC(t, conn, "spawn-status", "spawn.status", map[string]any{"id": rec.ID})
	aliasMsg := readUntilID(t, conn, "spawn-status")
	if aliasMsg.Error != nil {
		t.Fatalf("unexpected spawn.status error: %v", aliasMsg.Error)
	}
	var aliasRec subagent.RunRecord
	if err := json.Unmarshal(aliasMsg.Result, &aliasRec); err != nil {
		t.Fatalf("unmarshal spawn.status result: %v", err)
	}
	if aliasRec.ID != rec.ID {
		t.Fatalf("expected spawn.status alias to return run %q, got %#v", rec.ID, aliasRec)
	}

	close(gate)
}

func TestExecuteTool_SystemNotifyErrorsWhenNotifierUnavailable(t *testing.T) {
	t.Setenv("PATH", "")
	h := handler.NewHandler(session.New(10), &stubProvider{response: &types.ChatResponse{}}, handler.HandlerOptions{
		Model:   "test-model",
		Timeout: 5 * time.Second,
	})
	_, err := h.ExecuteTool(context.Background(), "alice", agent.ToolCall{
		Name:      "system.notify",
		Arguments: json.RawMessage(`{"message":"hi"}`),
	})
	if err == nil {
		t.Fatal("expected system.notify to fail without a notifier command")
	}
}

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
