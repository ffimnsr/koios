package handler_test

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ffimnsr/koios/internal/agent"
	"github.com/ffimnsr/koios/internal/handler"
	"github.com/ffimnsr/koios/internal/session"
	"github.com/ffimnsr/koios/internal/types"
	"github.com/gorilla/websocket"
)

// ── slash command test helpers ────────────────────────────────────────────────

// newSlashChatResponse builds a minimal ChatResponse whose first choice
// carries the given assistant text.
func newSlashChatResponse(text string) *types.ChatResponse {
	return &types.ChatResponse{
		Choices: []types.ChatChoice{
			{Message: types.Message{Role: "assistant", Content: text}},
		},
	}
}

// dialSlashServer constructs a full Handler+httptest.Server and dials a WebSocket
// for peerID.  It returns the connection, the session store, and the provider so
// callers can inspect or customise behaviour.
func dialSlashServer(t *testing.T, ownerPeerIDs []string, peerID string) (*websocket.Conn, *session.Store, *stubProvider) {
	t.Helper()
	store := session.NewWithOptions(session.Options{MaxMessages: 50})
	return dialSlashServerWithStore(t, store, ownerPeerIDs, peerID)
}

func dialSlashServerWithStore(t *testing.T, store *session.Store, ownerPeerIDs []string, peerID string) (*websocket.Conn, *session.Store, *stubProvider) {
	t.Helper()
	prov := &stubProvider{
		response: newSlashChatResponse("LLM reply"),
	}
	rt := agent.NewRuntime(store, prov, "test-model", 5*time.Second, agent.RetryPolicy{MaxAttempts: 1})
	coord := agent.NewCoordinator(rt)
	t.Cleanup(func() { coord.Stop() })
	h := handler.NewHandler(store, prov, handler.HandlerOptions{
		Model:        "test-model",
		Timeout:      5 * time.Second,
		AgentRuntime: rt,
		AgentCoord:   coord,
		OwnerPeerIDs: ownerPeerIDs,
	})
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	conn := dialWS(t, srv, peerID)
	return conn, store, prov
}

type slashMemoryFlusher struct {
	calls int
}

func (f *slashMemoryFlusher) FlushCompaction(_ context.Context, _ string, _ []types.Message) error {
	f.calls++
	return nil
}

type slashStubCompactor struct {
	summary string
}

func (c *slashStubCompactor) Compact(_ context.Context, _ []types.Message) (string, error) {
	return c.summary, nil
}

// sendSlashChat sends a single-user-message chat RPC and returns the response
// frame after skipping any streaming notifications.
func sendSlashChat(t *testing.T, conn *websocket.Conn, reqID, text string) rpcMsg {
	t.Helper()
	params := map[string]any{
		"messages": []map[string]any{
			{"role": "user", "content": text},
		},
	}
	sendRPC(t, conn, reqID, "chat", params)
	return readUntilID(t, conn, reqID)
}

// slashAssistantText extracts assistant_text from the result of a slash RPC
// response, failing the test on any RPC-level error.
func slashAssistantText(t *testing.T, msg rpcMsg) string {
	t.Helper()
	if msg.Error != nil {
		t.Fatalf("RPC error %d: %s", msg.Error.Code, msg.Error.Message)
	}
	var result struct {
		AssistantText string          `json:"assistant_text"`
		Status        json.RawMessage `json:"status"`
	}
	if err := json.Unmarshal(msg.Result, &result); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	return result.AssistantText
}

// ── Tests ─────────────────────────────────────────────────────────────────────

func TestSlashThink_SetAndQuery(t *testing.T) {
	conn, _, _ := dialSlashServer(t, nil, "think-alice")

	// Set think level.
	msg := sendSlashChat(t, conn, "1", "/think high")
	got := slashAssistantText(t, msg)
	if !strings.Contains(got, "high") {
		t.Errorf("expected 'high' in response, got: %s", got)
	}

	// Query current level.
	msg2 := sendSlashChat(t, conn, "2", "/think")
	got2 := slashAssistantText(t, msg2)
	if !strings.Contains(got2, "high") {
		t.Errorf("expected persisted 'high', got: %s", got2)
	}
}

func TestSlashThink_Invalid(t *testing.T) {
	conn, _, _ := dialSlashServer(t, nil, "think-bob")
	msg := sendSlashChat(t, conn, "1", "/think ultramax")
	got := slashAssistantText(t, msg)
	if !strings.Contains(got, "Invalid") {
		t.Errorf("expected invalid message, got: %s", got)
	}
}

func TestSlashThink_Off(t *testing.T) {
	conn, _, _ := dialSlashServer(t, nil, "think-charlie")
	sendSlashChat(t, conn, "1", "/think medium")
	msg := sendSlashChat(t, conn, "2", "/think off")
	got := slashAssistantText(t, msg)
	if !strings.Contains(got, "disabled") {
		t.Errorf("expected disabled, got: %s", got)
	}
}

func TestSlashVerbose(t *testing.T) {
	conn, _, _ := dialSlashServer(t, nil, "verbose-dave")

	msg := sendSlashChat(t, conn, "1", "/verbose on")
	if !strings.Contains(slashAssistantText(t, msg), "on") {
		t.Errorf("expected on")
	}
	msg2 := sendSlashChat(t, conn, "2", "/verbose off")
	if !strings.Contains(slashAssistantText(t, msg2), "off") {
		t.Errorf("expected off")
	}
}

func TestSlashTrace(t *testing.T) {
	conn, _, _ := dialSlashServer(t, nil, "trace-eve")

	msg := sendSlashChat(t, conn, "1", "/trace on")
	if !strings.Contains(slashAssistantText(t, msg), "on") {
		t.Errorf("expected on")
	}
}

func TestSlashStatus(t *testing.T) {
	conn, _, _ := dialSlashServer(t, nil, "status-frank")

	msg := sendSlashChat(t, conn, "1", "/status")
	text := slashAssistantText(t, msg)
	if !strings.Contains(text, "Model") {
		t.Errorf("expected 'Model' in /status output, got: %s", text)
	}
	// The result must also carry a machine-readable "status" field.
	var result struct {
		Status json.RawMessage `json:"status"`
	}
	if err := json.Unmarshal(msg.Result, &result); err != nil || len(result.Status) == 0 {
		t.Errorf("expected status field in result, got result: %s", msg.Result)
	}
}

func TestSlashReset(t *testing.T) {
	conn, store, _ := dialSlashServer(t, nil, "reset-grace")

	// Seed the session with a message.
	store.Append("reset-grace", types.Message{Role: "user", Content: "hello"})

	msg := sendSlashChat(t, conn, "1", "/reset")
	if !strings.Contains(strings.ToLower(slashAssistantText(t, msg)), "reset") {
		t.Errorf("expected reset confirmation, got: %s", slashAssistantText(t, msg))
	}
	// History should now be empty.
	if h := store.Get("reset-grace").History(); len(h) != 0 {
		t.Errorf("expected empty history after /reset, got %d messages", len(h))
	}
}

func TestSlashNew(t *testing.T) {
	conn, store, _ := dialSlashServer(t, nil, "new-henry")
	store.Append("new-henry", types.Message{Role: "user", Content: "something"})

	sendSlashChat(t, conn, "1", "/new")
	if h := store.Get("new-henry").History(); len(h) != 0 {
		t.Errorf("expected empty history after /new, got %d", len(h))
	}
}

func TestSlashCompact_NoCompactor(t *testing.T) {
	conn, _, _ := dialSlashServer(t, nil, "compact-iris")
	msg := sendSlashChat(t, conn, "1", "/compact")
	if !strings.Contains(slashAssistantText(t, msg), "not available") {
		t.Errorf("expected 'not available', got: %s", slashAssistantText(t, msg))
	}
}

func TestSlashCompact_Status(t *testing.T) {
	comp := &slashStubCompactor{summary: "checkpoint"}
	flush := &slashMemoryFlusher{}
	store := session.NewWithOptions(session.Options{
		MaxMessages:             50,
		CompactThreshold:        4,
		CompactReserve:          1,
		Compactor:               comp,
		CompactionMemoryFlusher: flush,
	})
	store.Append("compact-status", types.Message{Role: "user", Content: "one"})
	store.Append("compact-status", types.Message{Role: "assistant", Content: "two"})
	store.Append("compact-status", types.Message{Role: "user", Content: "three"})
	conn, _, _ := dialSlashServerWithStore(t, store, nil, "compact-status")

	msg := sendSlashChat(t, conn, "1", "/compact status")
	text := slashAssistantText(t, msg)
	if !strings.Contains(text, "Eligible now: yes") {
		t.Fatalf("expected eligible compaction status, got: %s", text)
	}
	if !strings.Contains(text, "Memory flush before compaction: on") {
		t.Fatalf("expected memory flush status, got: %s", text)
	}
}

func TestSlashCompact_Report(t *testing.T) {
	comp := &slashStubCompactor{summary: "checkpoint"}
	flush := &slashMemoryFlusher{}
	store := session.NewWithOptions(session.Options{
		MaxMessages:             50,
		CompactThreshold:        10,
		CompactReserve:          1,
		Compactor:               comp,
		CompactionMemoryFlusher: flush,
	})
	store.Append("compact-run",
		types.Message{Role: "user", Content: "one"},
		types.Message{Role: "assistant", Content: "two"},
		types.Message{Role: "user", Content: "three"},
	)
	conn, _, _ := dialSlashServerWithStore(t, store, nil, "compact-run")

	msg := sendSlashChat(t, conn, "1", "/compact")
	text := slashAssistantText(t, msg)
	if !strings.Contains(text, "Compaction started") {
		t.Fatalf("expected compaction start notice, got: %s", text)
	}
	if !strings.Contains(text, "Memory flush completed") {
		t.Fatalf("expected memory flush notice, got: %s", text)
	}
	if !strings.Contains(text, "Compaction finished") {
		t.Fatalf("expected completion notice, got: %s", text)
	}
	if flush.calls != 1 {
		t.Fatalf("expected one memory flush, got %d", flush.calls)
	}
}

func TestSlashRestart_NotOwner(t *testing.T) {
	// ownerPeerIDs restricted; "other" must be denied.
	conn, _, _ := dialSlashServer(t, []string{"owner-only"}, "restart-other")
	msg := sendSlashChat(t, conn, "1", "/restart")
	text := slashAssistantText(t, msg)
	if !strings.Contains(strings.ToLower(text), "denied") {
		t.Errorf("expected permission denied, got: %s", text)
	}
}

func TestSlashUnknown_FallsThrough(t *testing.T) {
	// An unknown slash-like word should be forwarded to the LLM.
	conn, _, prov := dialSlashServer(t, nil, "unknown-james")
	prov.response = newSlashChatResponse("LLM handled /custom")
	sendSlashChat(t, conn, "1", "/custom something")
	// Verify the provider was called (i.e. the command was not intercepted).
	if len(prov.seenReqs) == 0 {
		t.Error("expected LLM to be called for unknown slash command")
	}
}

func TestNonSlash_FallsThrough(t *testing.T) {
	conn, _, prov := dialSlashServer(t, nil, "nonslash-kate")
	prov.response = newSlashChatResponse("hello back")
	sendSlashChat(t, conn, "1", "hello")
	if len(prov.seenReqs) == 0 {
		t.Error("expected LLM to be called for non-slash message")
	}
}
