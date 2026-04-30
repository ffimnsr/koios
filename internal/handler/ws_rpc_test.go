package handler_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ffimnsr/koios/internal/agent"
	"github.com/ffimnsr/koios/internal/channels"
	"github.com/ffimnsr/koios/internal/handler"
	"github.com/ffimnsr/koios/internal/memory"
	"github.com/ffimnsr/koios/internal/scheduler"
	"github.com/ffimnsr/koios/internal/session"
	"github.com/ffimnsr/koios/internal/tasks"
	"github.com/ffimnsr/koios/internal/types"
	"github.com/ffimnsr/koios/internal/workspace"
	"github.com/gorilla/websocket"
)

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
		"content":            "remember this",
		"retention_class":    "pinned",
		"exposure_policy":    "auto",
		"capture_reason":     "saved during testing",
		"source_session_key": "alice::main",
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
			CaptureReason  string `json:"capture_reason"`
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
	if insertRes.Chunk.CaptureReason != "saved during testing" {
		t.Fatalf("unexpected capture reason: %q", insertRes.Chunk.CaptureReason)
	}

	sendRPC(t, conn, "m2", "memory.get", map[string]any{"id": insertRes.Chunk.ID})
	msg = readUntilID(t, conn, "m2")
	if msg.Error != nil {
		t.Fatalf("memory.get error: %#v", msg.Error)
	}
	var getRes struct {
		Chunk struct {
			ID               string `json:"id"`
			Content          string `json:"content"`
			RetentionClass   string `json:"retention_class"`
			CaptureReason    string `json:"capture_reason"`
			SourceSessionKey string `json:"source_session_key"`
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
	if getRes.Chunk.CaptureReason != "saved during testing" {
		t.Fatalf("unexpected fetched capture reason: %q", getRes.Chunk.CaptureReason)
	}
	if getRes.Chunk.SourceSessionKey != "alice::main" {
		t.Fatalf("unexpected source session key: %q", getRes.Chunk.SourceSessionKey)
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

func TestWS_ContactToolsRPC(t *testing.T) {
	store := session.New(10)
	prov := &stubProvider{response: &types.ChatResponse{}}
	memStore, err := memory.New(filepath.Join(t.TempDir(), "memory.db"), nil)
	if err != nil {
		t.Fatalf("memory.New: %v", err)
	}
	t.Cleanup(func() { _ = memStore.Close() })
	bindingStore := channels.NewBindingStore(filepath.Join(t.TempDir(), "bindings.json"))
	pending, err := bindingStore.EnsurePending(channels.BindingRequest{Channel: "telegram", SubjectID: "7", ConversationID: "123", DisplayName: "Alice Sender"})
	if err != nil {
		t.Fatalf("EnsurePending: %v", err)
	}
	if _, err := bindingStore.ApproveCodeWithRoute(pending.Code, "operator", channels.BindingRoute{PeerID: "alice"}); err != nil {
		t.Fatalf("ApproveCodeWithRoute: %v", err)
	}
	h := handler.NewHandler(store, prov, handler.HandlerOptions{
		Model:               "test-model",
		Timeout:             5 * time.Second,
		MemStore:            memStore,
		ChannelBindingStore: bindingStore,
	})
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)

	conn := dialWS(t, srv, "alice")
	sendRPC(t, conn, "c1", "memory.entity.create", map[string]any{"kind": "person", "name": "Alice Example"})
	msg := readUntilID(t, conn, "c1")
	if msg.Error != nil {
		t.Fatalf("memory.entity.create error: %#v", msg.Error)
	}
	var createRes struct {
		Entity struct {
			ID string `json:"id"`
		} `json:"entity"`
	}
	if err := json.Unmarshal(msg.Result, &createRes); err != nil {
		t.Fatalf("unmarshal create result: %v", err)
	}

	sendRPC(t, conn, "c2", "contact.alias", map[string]any{"id": createRes.Entity.ID, "aliases": []string{"Ali"}})
	msg = readUntilID(t, conn, "c2")
	if msg.Error != nil {
		t.Fatalf("contact.alias error: %#v", msg.Error)
	}

	sendRPC(t, conn, "c3", "contact.link_channel_identity", map[string]any{"id": createRes.Entity.ID, "channel": "telegram", "subject_id": "7"})
	msg = readUntilID(t, conn, "c3")
	if msg.Error != nil {
		t.Fatalf("contact.link_channel_identity error: %#v", msg.Error)
	}

	sendRPC(t, conn, "c4", "contact.resolve", map[string]any{"channel": "telegram", "subject_id": "7"})
	msg = readUntilID(t, conn, "c4")
	if msg.Error != nil {
		t.Fatalf("contact.resolve error: %#v", msg.Error)
	}
	if !strings.Contains(string(msg.Result), `"resolved":true`) || !strings.Contains(string(msg.Result), createRes.Entity.ID) {
		t.Fatalf("unexpected contact.resolve result: %s", msg.Result)
	}

	sendRPC(t, conn, "c5", "contact.list", map[string]any{"q": "Ali"})
	msg = readUntilID(t, conn, "c5")
	if msg.Error != nil {
		t.Fatalf("contact.list error: %#v", msg.Error)
	}
	if !strings.Contains(string(msg.Result), `"Alice Example"`) {
		t.Fatalf("unexpected contact.list result: %s", msg.Result)
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
