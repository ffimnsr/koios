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
		StreamEvents []string        `json:"stream_notifications"`
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
	if !containsString(result.Methods, "workspace.list") || !containsString(result.Methods, "workspace.write") {
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

	sendRPC(t, conn, "w5", "workspace.delete", map[string]any{"path": "project/readme.txt"})
	msg = readUntilID(t, conn, "w5")
	if msg.Error != nil {
		t.Fatalf("workspace.delete error: %#v", msg.Error)
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
	sendRPC(t, conn, "m1", "memory.insert", map[string]any{"content": "remember this"})
	msg := readUntilID(t, conn, "m1")
	if msg.Error != nil {
		t.Fatalf("memory.insert error: %#v", msg.Error)
	}
	var insertRes struct {
		Chunk struct {
			ID      string `json:"id"`
			Content string `json:"content"`
		} `json:"chunk"`
	}
	if err := json.Unmarshal(msg.Result, &insertRes); err != nil {
		t.Fatalf("unmarshal memory.insert result: %v", err)
	}
	if insertRes.Chunk.ID == "" {
		t.Fatal("expected inserted chunk id")
	}

	sendRPC(t, conn, "m2", "memory.get", map[string]any{"id": insertRes.Chunk.ID})
	msg = readUntilID(t, conn, "m2")
	if msg.Error != nil {
		t.Fatalf("memory.get error: %#v", msg.Error)
	}
	var getRes struct {
		Chunk struct {
			ID      string `json:"id"`
			Content string `json:"content"`
		} `json:"chunk"`
	}
	if err := json.Unmarshal(msg.Result, &getRes); err != nil {
		t.Fatalf("unmarshal memory.get result: %v", err)
	}
	if getRes.Chunk.Content != "remember this" {
		t.Fatalf("unexpected memory chunk content: %q", getRes.Chunk.Content)
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

func TestWS_AgentStartAndWait(t *testing.T) {
	prov := &stubProvider{
		response: &types.ChatResponse{
			Choices: []types.ChatChoice{{Message: types.Message{Role: "assistant", Content: "done"}}},
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
			AssistantText string `json:"assistant_text"`
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
