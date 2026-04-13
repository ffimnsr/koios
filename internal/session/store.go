// Package session provides a thread-safe, per-peer conversation history store
// with optional JSONL persistence (Phase 1) and LLM-based context compaction
// (Phase 2).
//
// Each peer is identified by an opaque string ID. Sessions are created lazily
// on first access.  When a SessionDir is configured, history survives daemon
// restarts via a per-session JSONL append-log.  When a Compactor is configured,
// sessions are summarised by the LLM instead of naively pruned once they reach
// CompactThreshold messages — mirroring how OpenClaw manages per-agent context.
package session

import (
	"bufio"
	"context"
	"encoding/json"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
	"sync"

	"github.com/ffimnsr/koios/internal/types"
)

// journalEntry is one line in a .jsonl session file.
// type "msg" wraps a single Message; type "compaction" wraps a summary string.
type journalEntry struct {
	Type    string         `json:"type"`
	Message *types.Message `json:"message,omitempty"`
	Summary string         `json:"summary,omitempty"`
}

// Session holds the conversation history for a single peer.
type Session struct {
	mu       sync.Mutex
	Messages []types.Message
	filePath string // empty when JSONL persistence is disabled
}

// AppendEvent describes a session append that observers may want to consume.
type AppendEvent struct {
	PeerID   string          `json:"peer_id"`
	Source   string          `json:"source,omitempty"`
	Messages []types.Message `json:"messages"`
}

// History returns a snapshot copy of the session's message slice.
// The copy is safe to read without holding the lock.
func (s *Session) History() []types.Message {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.Messages) == 0 {
		return nil
	}
	cp := make([]types.Message, len(s.Messages))
	copy(cp, s.Messages)
	return cp
}

// appendMsgs adds msgs, then compacts or prunes, and persists to disk.
// Must be called with s.mu held.
func (s *Session) appendMsgs(ctx context.Context, maxMsgs, compactThreshold, compactReserve int, compactor Compactor, msgs []types.Message) {
	s.Messages = append(s.Messages, msgs...)

	// Compaction path: LLM summarises old turns when threshold is reached.
	if compactor != nil && compactThreshold > 0 && len(s.Messages) >= compactThreshold {
		if ok := s.compact(ctx, compactor, compactReserve); ok {
			return // compact called rewriteFile; nothing more to do
		}
		// Compaction failed — fall through to naive pruning + file append.
	}

	// Naive pruning: drop oldest non-system messages to stay within cap.
	if maxMsgs > 0 && len(s.Messages) > maxMsgs {
		sysEnd := 0
		for sysEnd < len(s.Messages) && s.Messages[sysEnd].Role == "system" {
			sysEnd++
		}
		excess := len(s.Messages) - maxMsgs
		cutEnd := sysEnd + excess
		if cutEnd > len(s.Messages) {
			cutEnd = len(s.Messages)
		}
		s.Messages = append(s.Messages[:sysEnd], s.Messages[cutEnd:]...)
	}

	// Append only the new messages to the journal file.
	entries := make([]journalEntry, len(msgs))
	for i := range msgs {
		m := msgs[i]
		entries[i] = journalEntry{Type: "msg", Message: &m}
	}
	s.writeEntries(entries)
}

// compact calls the Compactor to summarise old messages, updates s.Messages,
// and rewrites the session file. Returns true on success.
// Must be called with s.mu held.
func (s *Session) compact(ctx context.Context, compactor Compactor, reserve int) bool {
	if reserve <= 0 || reserve >= len(s.Messages) {
		reserve = len(s.Messages) / 2
	}
	splitIdx := len(s.Messages) - reserve
	toCompact := make([]types.Message, splitIdx)
	copy(toCompact, s.Messages[:splitIdx])
	kept := s.Messages[splitIdx:]

	summary, err := compactor.Compact(ctx, toCompact)
	if err != nil {
		slog.Warn("session compaction failed, keeping full history", "err", err)
		return false
	}

	checkpoint := types.Message{
		Role:    "system",
		Content: "<summary>" + summary + "</summary>",
	}
	s.Messages = append([]types.Message{checkpoint}, kept...)
	s.rewriteFile()
	return true
}

// Reset clears all stored messages and truncates the journal file.
func (s *Session) Reset() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Messages = nil
	s.truncateFile()
}

// — JSONL file helpers ————————————————————————————————————————————————————————

// loadFromFile reads the session's JSONL file and populates s.Messages.
// A missing file is treated as an empty session.
// Must be called before the Session is accessible to other goroutines.
func (s *Session) loadFromFile() {
	if s.filePath == "" {
		return
	}
	f, err := os.Open(s.filePath)
	if os.IsNotExist(err) {
		return
	}
	if err != nil {
		slog.Warn("session load: open", "path", s.filePath, "err", err)
		return
	}
	defer f.Close()

	var messages []types.Message
	scanner := bufio.NewScanner(f)
	// Default 64 KB buffer is too small for large tool results or long messages.
	// Use 4 MB to avoid silently dropping lines that exceed the default limit.
	scanner.Buffer(make([]byte, 4<<20), 4<<20)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var e journalEntry
		if err := json.Unmarshal(line, &e); err != nil {
			slog.Warn("session load: skip malformed line", "err", err)
			continue
		}
		switch e.Type {
		case "msg":
			if e.Message != nil {
				messages = append(messages, *e.Message)
			}
		case "compaction":
			// A compaction checkpoint resets the message list; subsequent
			// "msg" entries extend it from there.
			messages = []types.Message{
				{Role: "system", Content: "<summary>" + e.Summary + "</summary>"},
			}
		}
	}
	if err := scanner.Err(); err != nil {
		slog.Warn("session load: scanner error, history may be incomplete", "path", s.filePath, "err", err)
	}
	s.Messages = messages
}

// writeEntries O(1)-appends journal entries to the session file.
func (s *Session) writeEntries(entries []journalEntry) {
	if s.filePath == "" {
		return
	}
	f, err := os.OpenFile(s.filePath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		slog.Warn("session write: open", "path", s.filePath, "err", err)
		return
	}
	defer f.Close()
	enc := json.NewEncoder(f)
	for i := range entries {
		if e := enc.Encode(&entries[i]); e != nil {
			slog.Warn("session write: encode", "err", e)
		}
	}
}

// rewriteFile atomically replaces the session file with the current in-memory
// state using a write-then-rename strategy.
// Must be called with s.mu held.
func (s *Session) rewriteFile() {
	if s.filePath == "" {
		return
	}
	tmpPath := s.filePath + ".tmp"
	f, err := os.OpenFile(tmpPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		slog.Warn("session rewrite: create tmp", "err", err)
		return
	}
	enc := json.NewEncoder(f)
	for i := range s.Messages {
		m := s.Messages[i]
		if err := enc.Encode(journalEntry{Type: "msg", Message: &m}); err != nil {
			slog.Warn("session rewrite: encode", "err", err)
		}
	}
	f.Close()
	if err := os.Rename(tmpPath, s.filePath); err != nil {
		slog.Warn("session rewrite: rename", "err", err)
		os.Remove(tmpPath)
	}
}

// truncateFile empties the session file.
func (s *Session) truncateFile() {
	if s.filePath == "" {
		return
	}
	if err := os.WriteFile(s.filePath, nil, 0o600); err != nil && !os.IsNotExist(err) {
		slog.Warn("session truncate", "path", s.filePath, "err", err)
	}
}

// — Store ——————————————————————————————————————————————————————————————————————

// Options configures a Store.
type Options struct {
	// MaxMessages is the per-session message cap (≤0 = unlimited).
	MaxMessages int
	// SessionDir, if non-empty, enables JSONL persistence at the given directory.
	// Each peer's session is stored as <SessionDir>/<url-escaped-peerID>.jsonl.
	SessionDir string
	// Compactor, if non-nil, replaces naive pruning with LLM summarisation once
	// CompactThreshold messages accumulate.
	Compactor Compactor
	// CompactThreshold is the number of stored messages that triggers compaction.
	// 0 disables compaction.
	CompactThreshold int
	// CompactReserve is the number of most-recent messages kept verbatim after
	// compaction. Defaults to 20 when a Compactor is set.
	CompactReserve int
}

// Store maps peer IDs to their isolated sessions.
type Store struct {
	mu               sync.RWMutex
	peers            map[string]*Session
	maxMsgs          int
	sessionDir       string
	compactor        Compactor
	compactThreshold int
	compactReserve   int
	subscribers      map[string]map[uint64]func(AppendEvent)
	nextSubID        uint64
}

// New creates an in-memory-only Store. It is a convenience wrapper around
// NewWithOptions and is backward-compatible with existing call sites.
func New(maxMsgs int) *Store {
	return NewWithOptions(Options{MaxMessages: maxMsgs})
}

// NewWithOptions creates a Store with the given options.
func NewWithOptions(opts Options) *Store {
	if opts.Compactor != nil && opts.CompactReserve <= 0 {
		opts.CompactReserve = 20
	}
	st := &Store{
		peers:            make(map[string]*Session),
		maxMsgs:          opts.MaxMessages,
		sessionDir:       opts.SessionDir,
		compactor:        opts.Compactor,
		compactThreshold: opts.CompactThreshold,
		compactReserve:   opts.CompactReserve,
		subscribers:      make(map[string]map[uint64]func(AppendEvent)),
	}
	if opts.SessionDir != "" {
		if err := os.MkdirAll(opts.SessionDir, 0o700); err != nil {
			slog.Warn("session store: cannot create session dir", "dir", opts.SessionDir, "err", err)
		}
	}
	return st
}

// Get returns the session for peerID, creating and loading it on first access.
func (st *Store) Get(peerID string) *Session {
	st.mu.RLock()
	sess, ok := st.peers[peerID]
	st.mu.RUnlock()
	if ok {
		return sess
	}

	st.mu.Lock()
	defer st.mu.Unlock()
	// Re-check under write lock to avoid a double-create race.
	if sess, ok = st.peers[peerID]; ok {
		return sess
	}
	sess = &Session{}
	if st.sessionDir != "" {
		sess.filePath = filepath.Join(st.sessionDir, url.PathEscape(peerID)+".jsonl")
		sess.loadFromFile()
	}
	st.peers[peerID] = sess
	return sess
}

// Append adds msgs to the peer's session using a background context.
// Use AppendCtx to propagate a request context for compaction cancellation.
func (st *Store) Append(peerID string, msgs ...types.Message) {
	st.AppendCtxWithSource(context.Background(), peerID, "", msgs...)
}

// AppendCtx adds msgs with context support so compaction LLM calls are
// cancelled when the originating request is cancelled.
func (st *Store) AppendCtx(ctx context.Context, peerID string, msgs ...types.Message) {
	st.AppendCtxWithSource(ctx, peerID, "", msgs...)
}

// AppendWithSource adds msgs and tags the append with a source label for
// subscribers. The source does not affect stored session contents.
func (st *Store) AppendWithSource(peerID, source string, msgs ...types.Message) {
	st.AppendCtxWithSource(context.Background(), peerID, source, msgs...)
}

// AppendCtxWithSource adds msgs with context support and a source label that
// subscribers can use to distinguish background events from interactive turns.
func (st *Store) AppendCtxWithSource(ctx context.Context, peerID, source string, msgs ...types.Message) {
	if len(msgs) == 0 {
		return
	}
	sess := st.Get(peerID)
	sess.mu.Lock()
	sess.appendMsgs(ctx, st.maxMsgs, st.compactThreshold, st.compactReserve, st.compactor, msgs)
	sess.mu.Unlock()
	st.notifyAppend(AppendEvent{
		PeerID:   peerID,
		Source:   source,
		Messages: cloneMessages(msgs),
	})
}

// Reset clears the message history for peerID. A no-op if the peer has no session.
func (st *Store) Reset(peerID string) {
	st.mu.RLock()
	sess, ok := st.peers[peerID]
	st.mu.RUnlock()
	if ok {
		sess.Reset()
	}
}

// Len returns the number of active peer sessions.
func (st *Store) Len() int {
	st.mu.RLock()
	defer st.mu.RUnlock()
	return len(st.peers)
}

// Subscribe registers a callback for append events for one peer and returns an
// unsubscribe function.
func (st *Store) Subscribe(peerID string, fn func(AppendEvent)) func() {
	st.mu.Lock()
	defer st.mu.Unlock()
	st.nextSubID++
	id := st.nextSubID
	if st.subscribers[peerID] == nil {
		st.subscribers[peerID] = make(map[uint64]func(AppendEvent))
	}
	st.subscribers[peerID][id] = fn
	return func() {
		st.mu.Lock()
		defer st.mu.Unlock()
		subs := st.subscribers[peerID]
		if subs == nil {
			return
		}
		delete(subs, id)
		if len(subs) == 0 {
			delete(st.subscribers, peerID)
		}
	}
}

func (st *Store) notifyAppend(ev AppendEvent) {
	st.mu.RLock()
	subs := st.subscribers[ev.PeerID]
	callbacks := make([]func(AppendEvent), 0, len(subs))
	for _, fn := range subs {
		callbacks = append(callbacks, fn)
	}
	st.mu.RUnlock()
	for _, fn := range callbacks {
		fn(ev)
	}
}

func cloneMessages(msgs []types.Message) []types.Message {
	cp := make([]types.Message, len(msgs))
	copy(cp, msgs)
	return cp
}
