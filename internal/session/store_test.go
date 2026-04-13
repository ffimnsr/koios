package session_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ffimnsr/koios/internal/session"
	"github.com/ffimnsr/koios/internal/types"
)

func msgs(roles ...string) []types.Message {
	out := make([]types.Message, len(roles))
	for i, r := range roles {
		out[i] = types.Message{Role: r, Content: r + "_content"}
	}
	return out
}

func TestStore_GetCreatesOnce(t *testing.T) {
	st := session.New(10)
	s1 := st.Get("peer1")
	s2 := st.Get("peer1")
	if s1 != s2 {
		t.Fatal("expected same session pointer for same peer")
	}
}

func TestStore_PeerIsolation(t *testing.T) {
	st := session.New(10)
	st.Append("alice", msgs("user")...)
	st.Append("bob", msgs("user", "user")...)

	if h := st.Get("alice").History(); len(h) != 1 {
		t.Fatalf("alice: want 1 message, got %d", len(h))
	}
	if h := st.Get("bob").History(); len(h) != 2 {
		t.Fatalf("bob: want 2 messages, got %d", len(h))
	}
}

func TestStore_PruneKeepsSystemMessages(t *testing.T) {
	const max = 5
	st := session.New(max)
	peer := "p"

	st.Append(peer, types.Message{Role: "system", Content: "sys"})
	for i := 0; i < 10; i++ {
		st.Append(peer,
			types.Message{Role: "user", Content: "u"},
			types.Message{Role: "assistant", Content: "a"},
		)
	}

	h := st.Get(peer).History()
	if len(h) > max {
		t.Fatalf("history length %d exceeds max %d", len(h), max)
	}
	if h[0].Role != "system" {
		t.Fatalf("expected system message at index 0, got role %q", h[0].Role)
	}
}

func TestStore_Reset(t *testing.T) {
	st := session.New(10)
	st.Append("p", msgs("user")...)
	st.Reset("p")
	if h := st.Get("p").History(); len(h) != 0 {
		t.Fatalf("expected empty history after reset, got %d messages", len(h))
	}
}

func TestStore_ResetUnknownPeer(t *testing.T) {
	st := session.New(10)
	// Should not panic for a peer that has never been seen.
	st.Reset("ghost")
}

func TestStore_ConcurrentAccess(t *testing.T) {
	st := session.New(50)
	const peers = 20
	const ops = 100

	var wg sync.WaitGroup
	for i := 0; i < peers; i++ {
		peerID := string(rune('a'+i)) + "_peer"
		wg.Add(1)
		go func(id string) {
			defer wg.Done()
			for j := 0; j < ops; j++ {
				st.Append(id, types.Message{Role: "user", Content: "msg"})
				_ = st.Get(id).History()
			}
		}(peerID)
	}
	wg.Wait()

	if st.Len() != peers {
		t.Fatalf("expected %d peers, got %d", peers, st.Len())
	}
}

func TestSession_HistoryReturnsCopy(t *testing.T) {
	st := session.New(10)
	st.Append("p", types.Message{Role: "user", Content: "original"})

	h := st.Get("p").History()
	h[0].Content = "mutated"

	h2 := st.Get("p").History()
	if h2[0].Content != "original" {
		t.Fatal("History() must return a copy, not a reference to internal state")
	}
}

// — Phase 1: JSONL persistence ————————————————————————————————————————————————

func TestStore_PersistAndReload(t *testing.T) {
	dir := t.TempDir()
	st := session.NewWithOptions(session.Options{MaxMessages: 10, SessionDir: dir})
	st.Append("alice", types.Message{Role: "user", Content: "hello"})
	st.Append("alice", types.Message{Role: "assistant", Content: "hi"})

	// Create a new Store pointing at the same dir — should reload the history.
	st2 := session.NewWithOptions(session.Options{MaxMessages: 10, SessionDir: dir})
	h := st2.Get("alice").History()
	if len(h) != 2 {
		t.Fatalf("want 2 reloaded messages, got %d", len(h))
	}
	if h[0].Content != "hello" || h[1].Content != "hi" {
		t.Fatalf("unexpected reloaded messages: %v", h)
	}
}

func TestStore_PersistReset(t *testing.T) {
	dir := t.TempDir()
	st := session.NewWithOptions(session.Options{MaxMessages: 10, SessionDir: dir})
	st.Append("bob", types.Message{Role: "user", Content: "something"})
	st.Reset("bob")

	st2 := session.NewWithOptions(session.Options{MaxMessages: 10, SessionDir: dir})
	if h := st2.Get("bob").History(); len(h) != 0 {
		t.Fatalf("expected empty history after reset+reload, got %d messages", len(h))
	}
}

// — Phase 2: LLM-based compaction —————————————————————————————————————————————

// stubCompactor is a Compactor that returns a fixed summary for testing.
type stubCompactor struct {
	summary string
	err     error
	calls   int
}

func (s *stubCompactor) Compact(_ context.Context, _ []types.Message) (string, error) {
	s.calls++
	return s.summary, s.err
}

type stubMemoryFlusher struct {
	calls    int
	peerID   string
	summary  string
	messages []types.Message
}

func (s *stubMemoryFlusher) FlushCompaction(_ context.Context, peerID string, messages []types.Message, summary string) error {
	s.calls++
	s.peerID = peerID
	s.summary = summary
	s.messages = append([]types.Message(nil), messages...)
	return nil
}

func TestStore_Compaction(t *testing.T) {
	comp := &stubCompactor{summary: "compacted history"}
	st := session.NewWithOptions(session.Options{
		MaxMessages:      100,
		CompactThreshold: 5,
		CompactReserve:   2,
		Compactor:        comp,
	})

	for i := 0; i < 5; i++ {
		st.Append("peer", types.Message{Role: "user", Content: fmt.Sprintf("msg%d", i)})
	}

	h := st.Get("peer").History()
	// After 5 messages we hit the threshold:
	//   splitIdx = 5 - 2 = 3  →  toCompact = msgs[0:3], kept = msgs[3:5]
	//   result   = [summary] + kept = 3 messages
	if len(h) != 3 {
		t.Fatalf("expected 3 messages after compaction (1 summary + 2 reserve), got %d", len(h))
	}
	if h[0].Role != "system" || !strings.Contains(h[0].Content, "compacted history") {
		t.Fatalf("expected summary checkpoint as first message, got: %+v", h[0])
	}
	if comp.calls != 1 {
		t.Fatalf("expected 1 compaction call, got %d", comp.calls)
	}
}

func TestStore_CompactionPersistReload(t *testing.T) {
	dir := t.TempDir()
	comp := &stubCompactor{summary: "persistent summary"}
	st := session.NewWithOptions(session.Options{
		MaxMessages:      100,
		SessionDir:       dir,
		CompactThreshold: 4,
		CompactReserve:   1,
		Compactor:        comp,
	})
	for i := 0; i < 4; i++ {
		st.Append("carol", types.Message{Role: "user", Content: fmt.Sprintf("m%d", i)})
	}

	// Reload without a compactor to verify the compacted file survives.
	st2 := session.NewWithOptions(session.Options{MaxMessages: 100, SessionDir: dir})
	h := st2.Get("carol").History()
	// [summary] + 1 reserve = 2 messages
	if len(h) != 2 {
		t.Fatalf("expected 2 messages on reload, got %d: %v", len(h), h)
	}
	if !strings.Contains(h[0].Content, "persistent summary") {
		t.Fatalf("expected summary in first reloaded message, got: %q", h[0].Content)
	}
}

func TestStore_CompactionFlushesMemory(t *testing.T) {
	comp := &stubCompactor{summary: "checkpoint"}
	flush := &stubMemoryFlusher{}
	st := session.NewWithOptions(session.Options{
		MaxMessages:             100,
		CompactThreshold:        4,
		CompactReserve:          1,
		Compactor:               comp,
		CompactionMemoryFlusher: flush,
	})
	for i := 0; i < 4; i++ {
		st.Append("peer", types.Message{Role: "user", Content: fmt.Sprintf("m%d", i)})
	}
	if flush.calls != 1 {
		t.Fatalf("expected 1 flush call, got %d", flush.calls)
	}
	if flush.peerID != "peer" || flush.summary != "checkpoint" {
		t.Fatalf("unexpected flush payload: %#v", flush)
	}
	if len(flush.messages) != 3 {
		t.Fatalf("expected compacted messages to be flushed, got %d", len(flush.messages))
	}
}

func TestStore_IdleResetOnGet(t *testing.T) {
	dir := t.TempDir()
	st := session.NewWithOptions(session.Options{
		MaxMessages:    10,
		SessionDir:     dir,
		IdleResetAfter: time.Minute,
	})
	st.Append("idle-peer", types.Message{Role: "user", Content: "hello"})
	path := filepath.Join(dir, "idle-peer.jsonl")
	old := time.Now().Add(-2 * time.Minute)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatalf("Chtimes: %v", err)
	}
	st2 := session.NewWithOptions(session.Options{
		MaxMessages:    10,
		SessionDir:     dir,
		IdleResetAfter: time.Minute,
	})
	if got := st2.Get("idle-peer").History(); len(got) != 0 {
		t.Fatalf("expected idle reset to clear history, got %#v", got)
	}
}

func TestStore_DailyResetOnGet(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	resetMinutes := ((now.Hour() + 23) % 24) * 60
	st := session.NewWithOptions(session.Options{
		MaxMessages:       10,
		SessionDir:        dir,
		DailyResetMinutes: resetMinutes,
	})
	st.Append("daily-peer", types.Message{Role: "user", Content: "hello"})
	path := filepath.Join(dir, "daily-peer.jsonl")
	old := now.Add(-2 * time.Hour)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatalf("Chtimes: %v", err)
	}
	st2 := session.NewWithOptions(session.Options{
		MaxMessages:       10,
		SessionDir:        dir,
		DailyResetMinutes: resetMinutes,
	})
	got := st2.Get("daily-peer").History()
	if len(got) != 0 {
		t.Fatalf("expected daily reset after cutoff, got %#v", got)
	}
}

func TestStore_MaintainDeletesExpiredAndEvictsOldest(t *testing.T) {
	dir := t.TempDir()
	base := time.Now().Add(-4 * time.Hour)
	for i, peer := range []string{"a", "b", "c"} {
		path := filepath.Join(dir, peer+".jsonl")
		if err := os.WriteFile(path, []byte(`{"type":"msg","message":{"role":"user","content":"x"}}`+"\n"), 0o600); err != nil {
			t.Fatalf("WriteFile: %v", err)
		}
		ts := base.Add(time.Duration(i) * time.Hour)
		if err := os.Chtimes(path, ts, ts); err != nil {
			t.Fatalf("Chtimes: %v", err)
		}
	}
	st := session.NewWithOptions(session.Options{
		MaxMessages:       10,
		SessionDir:        dir,
		SessionRetention:  90 * time.Minute,
		SessionMaxEntries: 1,
	})
	report := st.Maintain(time.Now())
	if report.DeletedExpired == 0 {
		t.Fatalf("expected expired sessions to be deleted, got %#v", report)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	if len(entries) > 1 {
		t.Fatalf("expected at most one session file after maintenance, got %d", len(entries))
	}
}
