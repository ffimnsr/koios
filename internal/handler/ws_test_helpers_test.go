package handler_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ffimnsr/koios/internal/agent"
	"github.com/ffimnsr/koios/internal/calendar"
	"github.com/ffimnsr/koios/internal/handler"
	"github.com/ffimnsr/koios/internal/session"
	"github.com/ffimnsr/koios/internal/subagent"
	"github.com/ffimnsr/koios/internal/tasks"
	"github.com/ffimnsr/koios/internal/types"
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

func contains(items []string, want string) bool {
	for _, item := range items {
		if item == want {
			return true
		}
	}
	return false
}
