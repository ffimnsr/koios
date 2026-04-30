package handler_test

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ffimnsr/koios/internal/agent"
	"github.com/ffimnsr/koios/internal/bookmarks"
	"github.com/ffimnsr/koios/internal/calendar"
	"github.com/ffimnsr/koios/internal/handler"
	"github.com/ffimnsr/koios/internal/memory"
	"github.com/ffimnsr/koios/internal/session"
	"github.com/ffimnsr/koios/internal/standing"
	"github.com/ffimnsr/koios/internal/tasks"
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

func dialSlashServerWithStanding(t *testing.T, peerID string, manager *standing.Manager) (*websocket.Conn, *session.Store, *stubProvider) {
	t.Helper()
	store := session.NewWithOptions(session.Options{MaxMessages: 50})
	prov := &stubProvider{response: newSlashChatResponse("LLM reply")}
	rt := agent.NewRuntime(store, prov, "test-model", 5*time.Second, agent.RetryPolicy{MaxAttempts: 1})
	coord := agent.NewCoordinator(rt)
	t.Cleanup(func() { coord.Stop() })
	h := handler.NewHandler(store, prov, handler.HandlerOptions{
		Model:           "test-model",
		Timeout:         5 * time.Second,
		AgentRuntime:    rt,
		AgentCoord:      coord,
		StandingManager: manager,
	})
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return dialWS(t, srv, peerID), store, prov
}

func dialSlashServerWithMemory(t *testing.T, peerID string) (*websocket.Conn, *memory.Store) {
	t.Helper()
	store := session.NewWithOptions(session.Options{MaxMessages: 50})
	prov := &stubProvider{response: newSlashChatResponse("LLM reply")}
	rt := agent.NewRuntime(store, prov, "test-model", 5*time.Second, agent.RetryPolicy{MaxAttempts: 1})
	coord := agent.NewCoordinator(rt)
	t.Cleanup(func() { coord.Stop() })
	memStore, err := memory.New(filepath.Join(t.TempDir(), "memory.db"), nil)
	if err != nil {
		t.Fatalf("memory.New: %v", err)
	}
	t.Cleanup(func() { _ = memStore.Close() })
	h := handler.NewHandler(store, prov, handler.HandlerOptions{
		Model:        "test-model",
		Timeout:      5 * time.Second,
		AgentRuntime: rt,
		AgentCoord:   coord,
		MemStore:     memStore,
	})
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return dialWS(t, srv, peerID), memStore
}

func dialSlashServerWithTasks(t *testing.T, peerID string) (*websocket.Conn, *tasks.Store) {
	t.Helper()
	store := session.NewWithOptions(session.Options{MaxMessages: 50})
	prov := &stubProvider{response: newSlashChatResponse("LLM reply")}
	rt := agent.NewRuntime(store, prov, "test-model", 5*time.Second, agent.RetryPolicy{MaxAttempts: 1})
	coord := agent.NewCoordinator(rt)
	t.Cleanup(func() { coord.Stop() })
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
	return dialWS(t, srv, peerID), taskStore
}

func dialSlashServerWithBookmarks(t *testing.T, peerID string) (*websocket.Conn, *session.Store, *bookmarks.Store) {
	t.Helper()
	store := session.NewWithOptions(session.Options{MaxMessages: 50})
	prov := &stubProvider{response: newSlashChatResponse("LLM reply")}
	rt := agent.NewRuntime(store, prov, "test-model", 5*time.Second, agent.RetryPolicy{MaxAttempts: 1})
	coord := agent.NewCoordinator(rt)
	t.Cleanup(func() { coord.Stop() })
	bookmarkStore, err := bookmarks.New(filepath.Join(t.TempDir(), "bookmarks.db"))
	if err != nil {
		t.Fatalf("bookmarks.New: %v", err)
	}
	t.Cleanup(func() { _ = bookmarkStore.Close() })
	h := handler.NewHandler(store, prov, handler.HandlerOptions{
		Model:         "test-model",
		Timeout:       5 * time.Second,
		AgentRuntime:  rt,
		AgentCoord:    coord,
		BookmarkStore: bookmarkStore,
	})
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return dialWS(t, srv, peerID), store, bookmarkStore
}

func dialSlashServerWithCalendar(t *testing.T, peerID string) (*websocket.Conn, *calendar.Store) {
	t.Helper()
	store := session.NewWithOptions(session.Options{MaxMessages: 50})
	prov := &stubProvider{response: newSlashChatResponse("LLM reply")}
	rt := agent.NewRuntime(store, prov, "test-model", 5*time.Second, agent.RetryPolicy{MaxAttempts: 1})
	coord := agent.NewCoordinator(rt)
	t.Cleanup(func() { coord.Stop() })
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
	return dialWS(t, srv, peerID), calendarStore
}

func dialSlashServerWithBriefing(t *testing.T, peerID string) (*websocket.Conn, *memory.Store, *tasks.Store, *calendar.Store) {
	t.Helper()
	store := session.NewWithOptions(session.Options{MaxMessages: 50})
	prov := &stubProvider{response: newSlashChatResponse("LLM reply")}
	rt := agent.NewRuntime(store, prov, "test-model", 5*time.Second, agent.RetryPolicy{MaxAttempts: 1})
	coord := agent.NewCoordinator(rt)
	t.Cleanup(func() { coord.Stop() })
	memStore, err := memory.New(filepath.Join(t.TempDir(), "memory.db"), nil)
	if err != nil {
		t.Fatalf("memory.New: %v", err)
	}
	t.Cleanup(func() { _ = memStore.Close() })
	taskStore, err := tasks.New(filepath.Join(t.TempDir(), "tasks.db"))
	if err != nil {
		t.Fatalf("tasks.New: %v", err)
	}
	t.Cleanup(func() { _ = taskStore.Close() })
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
		MemStore:      memStore,
		TaskStore:     taskStore,
		CalendarStore: calendarStore,
	})
	store.Append(peerID,
		types.Message{Role: "user", Content: "We need to send the vendor follow-up email today."},
		types.Message{Role: "assistant", Content: "I should draft the project summary after the sync."},
	)
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return dialWS(t, srv, peerID), memStore, taskStore, calendarStore
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

func TestSlashBookmarkAddAndList(t *testing.T) {
	conn, _, _ := dialSlashServerWithBookmarks(t, "bookmark-alice")

	msg := sendSlashChat(t, conn, "1", "/bookmark add Sprint recap | Capture the open release work | planning,release | 1735689600")
	got := slashAssistantText(t, msg)
	if !strings.Contains(got, "Saved bookmark") {
		t.Fatalf("expected bookmark save confirmation, got: %s", got)
	}

	msg = sendSlashChat(t, conn, "2", "/bookmark list")
	got = slashAssistantText(t, msg)
	if !strings.Contains(got, "Sprint recap") {
		t.Fatalf("expected bookmark title in list, got: %s", got)
	}
	if !strings.Contains(got, "labels=planning,release") {
		t.Fatalf("expected labels in list, got: %s", got)
	}
}

func TestSlashBookmarkClipAndGet(t *testing.T) {
	conn, store, _ := dialSlashServerWithBookmarks(t, "bookmark-bob")
	store.Append("bookmark-bob",
		types.Message{Role: "user", Content: "Please save the launch checklist and owner assignments."},
		types.Message{Role: "assistant", Content: "Checklist: release notes, smoke test, deploy window, rollback contact."},
		types.Message{Role: "user", Content: "Also include the reminder to notify support."},
	)

	msg := sendSlashChat(t, conn, "1", "/bookmark clip 2-3 | Launch checklist | ops,launch")
	got := slashAssistantText(t, msg)
	if !strings.Contains(got, "Saved session bookmark") {
		t.Fatalf("expected clip confirmation, got: %s", got)
	}
	parts := strings.Fields(got)
	if len(parts) < 4 {
		t.Fatalf("unexpected clip response: %s", got)
	}
	id := strings.TrimSuffix(parts[3], ".")

	msg = sendSlashChat(t, conn, "2", "/bookmark get "+id)
	got = slashAssistantText(t, msg)
	if !strings.Contains(got, "Launch checklist") {
		t.Fatalf("expected bookmark title in get response, got: %s", got)
	}
	if !strings.Contains(got, "messages=2-3") {
		t.Fatalf("expected source range in get response, got: %s", got)
	}
	if !strings.Contains(got, "Checklist: release notes") {
		t.Fatalf("expected clipped content in get response, got: %s", got)
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

func TestSlashActivation_SetAndQuery(t *testing.T) {
	conn, store, _ := dialSlashServer(t, nil, "activation-alice")

	msg := sendSlashChat(t, conn, "1", "/activation mention")
	if got := slashAssistantText(t, msg); !strings.Contains(got, "mention") {
		t.Fatalf("expected mention confirmation, got: %s", got)
	}
	if got := store.Policy("activation-alice").ActivationMode; got != "mention" {
		t.Fatalf("activation mode = %q, want mention", got)
	}

	msg = sendSlashChat(t, conn, "2", "/activation group always")
	if got := slashAssistantText(t, msg); !strings.Contains(got, "Group activation default: always") {
		t.Fatalf("expected group confirmation, got: %s", got)
	}

	msg = sendSlashChat(t, conn, "3", "/activation chat mention")
	if got := slashAssistantText(t, msg); !strings.Contains(got, "Chat activation default: mention") {
		t.Fatalf("expected chat confirmation, got: %s", got)
	}

	msg = sendSlashChat(t, conn, "4", "/activation")
	got := slashAssistantText(t, msg)
	if !strings.Contains(got, "Session activation: mention") {
		t.Fatalf("expected session activation in query, got: %s", got)
	}
	if !strings.Contains(got, "Group activation default: always") {
		t.Fatalf("expected group activation in query, got: %s", got)
	}
	if !strings.Contains(got, "Chat activation default: mention") {
		t.Fatalf("expected chat activation in query, got: %s", got)
	}
}

func TestSlashUsage_SetAndQuery(t *testing.T) {
	conn, _, _ := dialSlashServer(t, nil, "usage-alice")

	msg := sendSlashChat(t, conn, "1", "/usage tokens")
	if got := slashAssistantText(t, msg); !strings.Contains(got, "tokens") {
		t.Fatalf("expected tokens confirmation, got: %s", got)
	}

	msg = sendSlashChat(t, conn, "2", "/usage")
	if got := slashAssistantText(t, msg); !strings.Contains(got, "tokens") {
		t.Fatalf("expected persisted tokens mode, got: %s", got)
	}

	msg = sendSlashChat(t, conn, "3", "/usage full")
	if got := slashAssistantText(t, msg); !strings.Contains(got, "tokens") {
		t.Fatalf("expected full alias to map to tokens, got: %s", got)
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

func TestSlashProfileActivate(t *testing.T) {
	standingStore, err := standing.NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("standing.NewStore: %v", err)
	}
	if _, err := standingStore.SaveDocument(&standing.Document{
		PeerID: "profile-pam",
		Profiles: map[string]standing.Profile{
			"focus":  {Content: "Focus mode"},
			"travel": {Content: "Travel mode"},
		},
		DefaultProfile: "travel",
	}); err != nil {
		t.Fatalf("SaveDocument: %v", err)
	}
	manager := standing.NewManager(standingStore, "")
	conn, store, _ := dialSlashServerWithStanding(t, "profile-pam", manager)

	msg := sendSlashChat(t, conn, "1", "/profile")
	if got := slashAssistantText(t, msg); !strings.Contains(got, "travel (default)") {
		t.Fatalf("expected default profile in response, got: %s", got)
	}

	msg = sendSlashChat(t, conn, "2", "/profile list")
	if got := slashAssistantText(t, msg); !strings.Contains(got, "focus") || !strings.Contains(got, "travel") {
		t.Fatalf("expected profile list, got: %s", got)
	}

	msg = sendSlashChat(t, conn, "3", "/profile use focus")
	if got := slashAssistantText(t, msg); !strings.Contains(got, "focus") {
		t.Fatalf("expected activation confirmation, got: %s", got)
	}
	if policy := store.Policy("profile-pam"); policy.ActiveProfile != "focus" {
		t.Fatalf("expected active profile focus, got %#v", policy)
	}

	msg = sendSlashChat(t, conn, "4", "/status")
	if got := slashAssistantText(t, msg); !strings.Contains(got, "Active profile: focus") {
		t.Fatalf("expected active profile in status, got: %s", got)
	}

	msg = sendSlashChat(t, conn, "5", "/profile clear")
	if got := slashAssistantText(t, msg); !strings.Contains(strings.ToLower(got), "cleared") {
		t.Fatalf("expected clear confirmation, got: %s", got)
	}
	if policy := store.Policy("profile-pam"); policy.ActiveProfile != "" {
		t.Fatalf("expected cleared active profile, got %#v", policy)
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

func TestSlashMemoryQueueListAndApprove(t *testing.T) {
	conn, memStore := dialSlashServerWithMemory(t, "memory-lima")
	ctx := context.Background()
	candidate, err := memStore.QueueCandidate(ctx, "memory-lima", "Remember the preferred deploy checklist", memory.ChunkOptions{})
	if err != nil {
		t.Fatalf("QueueCandidate: %v", err)
	}

	msg := sendSlashChat(t, conn, "1", "/memory queue list")
	text := slashAssistantText(t, msg)
	if !strings.Contains(text, candidate.ID) {
		t.Fatalf("expected candidate id in list output, got: %s", text)
	}

	msg = sendSlashChat(t, conn, "2", "/memory queue approve "+candidate.ID)
	text = slashAssistantText(t, msg)
	if !strings.Contains(strings.ToLower(text), "approved") {
		t.Fatalf("expected approval confirmation, got: %s", text)
	}

	chunks, err := memStore.List(ctx, "memory-lima", 10)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(chunks) != 1 {
		t.Fatalf("expected one approved memory chunk, got %d", len(chunks))
	}
	if chunks[0].Content != "Remember the preferred deploy checklist" {
		t.Fatalf("unexpected approved chunk content: %q", chunks[0].Content)
	}
}

func TestSlashMemoryQueueRejectRequiresReason(t *testing.T) {
	conn, memStore := dialSlashServerWithMemory(t, "memory-mike")
	ctx := context.Background()
	candidate, err := memStore.QueueCandidate(ctx, "memory-mike", "Temporary draft note", memory.ChunkOptions{})
	if err != nil {
		t.Fatalf("QueueCandidate: %v", err)
	}

	msg := sendSlashChat(t, conn, "1", "/memory queue reject "+candidate.ID)
	text := slashAssistantText(t, msg)
	if !strings.Contains(text, "Usage") {
		t.Fatalf("expected usage guidance, got: %s", text)
	}
	pending, err := memStore.ListCandidates(ctx, "memory-mike", 10, memory.CandidateStatusPending)
	if err != nil {
		t.Fatalf("ListCandidates: %v", err)
	}
	if len(pending) != 1 {
		t.Fatalf("expected candidate to remain pending, got %d", len(pending))
	}
}

func TestSlashMemoryQueueListShowsProvenance(t *testing.T) {
	conn, memStore := dialSlashServerWithMemory(t, "memory-november")
	ctx := context.Background()
	_, err := memStore.QueueCandidateWithProvenance(ctx, "memory-november", "User prefers terse release notes.", memory.ChunkOptions{}, memory.CandidateProvenance{
		CaptureKind:      memory.CandidateCaptureAutoTurnExtract,
		SourceSessionKey: "memory-november::main",
		SourceExcerpt:    "Remember that I prefer terse release notes in updates.",
	})
	if err != nil {
		t.Fatalf("QueueCandidateWithProvenance: %v", err)
	}

	msg := sendSlashChat(t, conn, "1", "/memory queue list")
	text := slashAssistantText(t, msg)
	if !strings.Contains(text, "source:") {
		t.Fatalf("expected provenance in queue list, got: %s", text)
	}
	if !strings.Contains(text, "memory-november::main") {
		t.Fatalf("expected source session in queue list, got: %s", text)
	}
	if !strings.Contains(text, "auto_turn_extract") {
		t.Fatalf("expected capture kind in queue list, got: %s", text)
	}
}

func TestSlashTasksExtractAndApprove(t *testing.T) {
	conn, taskStore := dialSlashServerWithTasks(t, "tasks-oscar")
	ctx := context.Background()

	msg := sendSlashChat(t, conn, "1", "/tasks extract Remember to pay the hosting invoice tomorrow.")
	text := slashAssistantText(t, msg)
	if !strings.Contains(text, "Queued 1 task candidates") {
		t.Fatalf("expected extraction confirmation, got: %s", text)
	}

	candidates, err := taskStore.ListCandidates(ctx, "tasks-oscar", 10, tasks.CandidateStatusPending)
	if err != nil {
		t.Fatalf("ListCandidates: %v", err)
	}
	if len(candidates) != 1 {
		t.Fatalf("pending candidates = %d, want 1", len(candidates))
	}

	msg = sendSlashChat(t, conn, "2", "/tasks queue approve "+candidates[0].ID)
	text = slashAssistantText(t, msg)
	if !strings.Contains(strings.ToLower(text), "approved") {
		t.Fatalf("expected approval confirmation, got: %s", text)
	}

	tasksList, err := taskStore.ListTasks(ctx, "tasks-oscar", 10, tasks.TaskStatusOpen)
	if err != nil {
		t.Fatalf("ListTasks: %v", err)
	}
	if len(tasksList) != 1 {
		t.Fatalf("tasks = %d, want 1", len(tasksList))
	}
	if !strings.Contains(strings.ToLower(tasksList[0].Title), "hosting invoice") {
		t.Fatalf("unexpected task title: %q", tasksList[0].Title)
	}
}

func TestSlashTasksAssignCompleteAndReopen(t *testing.T) {
	conn, taskStore := dialSlashServerWithTasks(t, "tasks-papa")
	ctx := context.Background()
	candidate, err := taskStore.QueueCandidate(ctx, "tasks-papa", tasks.CandidateInput{Title: "Send board update"})
	if err != nil {
		t.Fatalf("QueueCandidate: %v", err)
	}
	_, task, err := taskStore.ApproveCandidate(ctx, "tasks-papa", candidate.ID, tasks.CandidatePatch{}, "confirmed")
	if err != nil {
		t.Fatalf("ApproveCandidate: %v", err)
	}

	msg := sendSlashChat(t, conn, "1", "/tasks assign "+task.ID+" finance")
	text := slashAssistantText(t, msg)
	if !strings.Contains(strings.ToLower(text), "assigned") {
		t.Fatalf("expected assign confirmation, got: %s", text)
	}

	msg = sendSlashChat(t, conn, "2", "/tasks complete "+task.ID)
	text = slashAssistantText(t, msg)
	if !strings.Contains(strings.ToLower(text), "completed") {
		t.Fatalf("expected complete confirmation, got: %s", text)
	}

	msg = sendSlashChat(t, conn, "3", "/tasks reopen "+task.ID)
	text = slashAssistantText(t, msg)
	if !strings.Contains(strings.ToLower(text), "reopened") {
		t.Fatalf("expected reopen confirmation, got: %s", text)
	}

	loaded, err := taskStore.GetTask(ctx, "tasks-papa", task.ID)
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if loaded.Owner != "finance" {
		t.Fatalf("owner = %q, want finance", loaded.Owner)
	}
	if loaded.Status != tasks.TaskStatusOpen {
		t.Fatalf("status = %q, want open", loaded.Status)
	}
}

func TestSlashWaitingAddAndResolve(t *testing.T) {
	conn, taskStore := dialSlashServerWithTasks(t, "waiting-romeo")
	ctx := context.Background()

	msg := sendSlashChat(t, conn, "1", "/waiting add vendor | Vendor reply on contract redlines")
	text := slashAssistantText(t, msg)
	if !strings.Contains(strings.ToLower(text), "created waiting-on") {
		t.Fatalf("expected waiting creation confirmation, got: %s", text)
	}

	items, err := taskStore.ListWaitingOns(ctx, "waiting-romeo", 10, tasks.WaitingStatusOpen)
	if err != nil {
		t.Fatalf("ListWaitingOns: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("waiting records = %d, want 1", len(items))
	}

	msg = sendSlashChat(t, conn, "2", "/waiting resolve "+items[0].ID)
	text = slashAssistantText(t, msg)
	if !strings.Contains(strings.ToLower(text), "resolved") {
		t.Fatalf("expected waiting resolve confirmation, got: %s", text)
	}

	loaded, err := taskStore.GetWaitingOn(ctx, "waiting-romeo", items[0].ID)
	if err != nil {
		t.Fatalf("GetWaitingOn: %v", err)
	}
	if loaded.Status != tasks.WaitingStatusResolved {
		t.Fatalf("status = %q, want resolved", loaded.Status)
	}
}

func TestSlashCalendarAddListAndAgenda(t *testing.T) {
	conn, calendarStore := dialSlashServerWithCalendar(t, "calendar-sierra")
	ctx := context.Background()
	icsPath := filepath.Join(t.TempDir(), "Agenda.ics")
	todayStamp := time.Now().UTC().Format("20060102")
	ics := "BEGIN:VCALENDAR\nVERSION:2.0\nBEGIN:VEVENT\nUID:evt-1\nSUMMARY:Design review\nDTSTART:" + todayStamp + "T090000Z\nDTEND:" + todayStamp + "T100000Z\nEND:VEVENT\nEND:VCALENDAR\n"
	if err := os.WriteFile(icsPath, []byte(ics), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	msg := sendSlashChat(t, conn, "1", "/calendar add Work | "+icsPath+" | UTC")
	text := slashAssistantText(t, msg)
	if !strings.Contains(strings.ToLower(text), "created calendar source") {
		t.Fatalf("expected create confirmation, got: %s", text)
	}

	sources, err := calendarStore.ListSources(ctx, "calendar-sierra", false)
	if err != nil {
		t.Fatalf("ListSources: %v", err)
	}
	if len(sources) != 1 {
		t.Fatalf("sources = %d, want 1", len(sources))
	}
	if sources[0].Path != icsPath {
		t.Fatalf("path = %q, want %q", sources[0].Path, icsPath)
	}

	msg = sendSlashChat(t, conn, "2", "/calendar list")
	text = slashAssistantText(t, msg)
	if !strings.Contains(text, "Work") {
		t.Fatalf("expected source listing, got: %s", text)
	}

	msg = sendSlashChat(t, conn, "3", "/calendar agenda today UTC")
	text = slashAssistantText(t, msg)
	if !strings.Contains(text, "Design review") {
		t.Fatalf("expected agenda event, got: %s", text)
	}
	if !strings.Contains(text, "Work") {
		t.Fatalf("expected source name in agenda, got: %s", text)
	}
}

func TestSlashCalendarRemove(t *testing.T) {
	conn, calendarStore := dialSlashServerWithCalendar(t, "calendar-tango")
	ctx := context.Background()
	icsPath := filepath.Join(t.TempDir(), "team.ics")
	ics := "BEGIN:VCALENDAR\nVERSION:2.0\nBEGIN:VEVENT\nUID:evt-2\nSUMMARY:Standup\nDTSTART:20260424T110000Z\nDTEND:20260424T113000Z\nEND:VEVENT\nEND:VCALENDAR\n"
	if err := os.WriteFile(icsPath, []byte(ics), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	source, err := calendarStore.CreateSource(ctx, "calendar-tango", calendar.SourceInput{Name: "Team", Path: icsPath})
	if err != nil {
		t.Fatalf("CreateSource: %v", err)
	}

	msg := sendSlashChat(t, conn, "1", "/calendar remove "+source.ID)
	text := slashAssistantText(t, msg)
	if !strings.Contains(strings.ToLower(text), "removed calendar source") {
		t.Fatalf("expected remove confirmation, got: %s", text)
	}

	sources, err := calendarStore.ListSources(ctx, "calendar-tango", false)
	if err != nil {
		t.Fatalf("ListSources: %v", err)
	}
	if len(sources) != 0 {
		t.Fatalf("sources = %d, want 0", len(sources))
	}
}

func TestSlashBriefDaily(t *testing.T) {
	conn, memStore, taskStore, calendarStore := dialSlashServerWithBriefing(t, "brief-nina")
	ctx := context.Background()
	if _, err := memStore.QueueCandidate(ctx, "brief-nina", "Remember preferred summary style", memory.ChunkOptions{}); err != nil {
		t.Fatalf("QueueCandidate: %v", err)
	}
	if _, err := taskStore.CreateWaitingOn(ctx, "brief-nina", tasks.WaitingOnInput{Title: "Vendor reply", WaitingFor: "vendor", FollowUpAt: time.Now().Add(-time.Hour).Unix()}); err != nil {
		t.Fatalf("CreateWaitingOn: %v", err)
	}
	if _, err := memStore.CreateEntity(ctx, "brief-nina", memory.EntityKindProject, "Launch", nil, "Kickoff week", time.Now().Unix()); err != nil {
		t.Fatalf("CreateEntity: %v", err)
	}
	icsPath := filepath.Join(t.TempDir(), "brief.ics")
	briefTodayStamp := time.Now().UTC().Format("20060102")
	ics := strings.Join([]string{
		"BEGIN:VCALENDAR",
		"VERSION:2.0",
		"PRODID:-//Koios//EN",
		"BEGIN:VEVENT",
		"UID:brief-event",
		"DTSTAMP:" + briefTodayStamp + "T090000Z",
		"DTSTART:" + briefTodayStamp + "T150000Z",
		"DTEND:" + briefTodayStamp + "T153000Z",
		"SUMMARY:Planning Sync",
		"END:VEVENT",
		"END:VCALENDAR",
	}, "\n")
	if err := os.WriteFile(icsPath, []byte(ics), 0o600); err != nil {
		t.Fatalf("write ics: %v", err)
	}
	enabled := true
	if _, err := calendarStore.CreateSource(ctx, "brief-nina", calendar.SourceInput{Name: "Work", Path: icsPath, Enabled: &enabled}); err != nil {
		t.Fatalf("CreateSource: %v", err)
	}

	msg := sendSlashChat(t, conn, "1", "/brief daily UTC")
	text := slashAssistantText(t, msg)
	if !strings.Contains(text, "Daily brief") {
		t.Fatalf("expected daily brief output, got: %s", text)
	}
	if !strings.Contains(text, "Planning Sync") {
		t.Fatalf("expected agenda event in brief, got: %s", text)
	}
	if !strings.Contains(text, "Vendor reply") {
		t.Fatalf("expected waiting item in brief, got: %s", text)
	}
}
