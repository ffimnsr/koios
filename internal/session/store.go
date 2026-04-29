// Package session provides a thread-safe, per-peer conversation history store
// with optional JSONL persistence (Phase 1) and LLM-based context compaction
// (Phase 2).
//
// Each peer is identified by an opaque string ID. Sessions are created lazily
// on first access.  When a SessionDir is configured, history survives gateway
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
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/ffimnsr/koios/internal/ops"
	"github.com/ffimnsr/koios/internal/redact"
	"github.com/ffimnsr/koios/internal/types"
)

// journalEntry is one line in a .jsonl session file.
// type "msg" wraps a single Message; type "compaction" wraps a summary string.
type journalEntry struct {
	Type    string         `json:"type"`
	Message *types.Message `json:"message,omitempty"`
	Summary string         `json:"summary,omitempty"`
}

// SessionPolicy captures persisted per-session behavior toggles.
type SessionPolicy struct {
	ReplyBack bool `json:"reply_back,omitempty"`
	// UsageMode controls whether visible replies append a usage footer.
	// Valid values: off | tokens
	UsageMode string `json:"usage_mode,omitempty"`
	// ModelOverride, when non-empty, pins this session to a specific model
	// (profile name or raw model ID). The agent runtime applies it before
	// each turn.
	ModelOverride string `json:"model_override,omitempty"`
	// ActiveProfile selects a named standing/persona profile for this session.
	// Empty falls back to the peer document's default profile.
	ActiveProfile string `json:"active_profile,omitempty"`
	// QueueMode controls how mid-run steering notes are applied.
	// Valid values: steer | followup | collect
	QueueMode string `json:"queue_mode,omitempty"`
	// ThinkLevel controls the reasoning budget sent to the model.
	// Valid values: off | minimal | low | medium | high | xhigh
	ThinkLevel string `json:"think_level,omitempty"`
	// VerboseMode, when true, enables verbose tool summaries in responses.
	VerboseMode bool `json:"verbose_mode,omitempty"`
	// TraceMode, when true, emits per-step debug trace events.
	TraceMode bool `json:"trace_mode,omitempty"`
	// BlockStream switches streamed delivery from token-like deltas to larger
	// coalesced blocks.
	BlockStream bool `json:"block_stream,omitempty"`
	// StreamChunkChars controls the preferred emitted chunk size for streamed
	// output. Zero selects the transport default.
	StreamChunkChars int `json:"stream_chunk_chars,omitempty"`
	// StreamCoalesceMS controls how long streamed output may be buffered before
	// it is flushed to the client as a coalesced chunk.
	StreamCoalesceMS int `json:"stream_coalesce_ms,omitempty"`
}

// Session holds the conversation history for a single peer.
type Session struct {
	mu           sync.Mutex
	Messages     []types.Message
	filePath     string // empty when JSONL persistence is disabled
	lastActivity time.Time
}

// AppendEvent describes a session append that observers may want to consume.
type AppendEvent struct {
	PeerID   string          `json:"peer_id"`
	Source   string          `json:"source,omitempty"`
	Messages []types.Message `json:"messages"`
}

// CompactionStatus describes whether a session can currently be compacted and
// how many messages would be summarized vs retained.
type CompactionStatus struct {
	Enabled            bool   `json:"enabled"`
	MemoryFlushEnabled bool   `json:"memory_flush_enabled"`
	SessionMessages    int    `json:"session_messages"`
	Reserve            int    `json:"reserve"`
	CompactedMessages  int    `json:"compacted_messages"`
	KeptMessages       int    `json:"kept_messages"`
	Eligible           bool   `json:"eligible"`
	Reason             string `json:"reason,omitempty"`
}

// CompactionReport captures the outcome of one compaction attempt.
type CompactionReport struct {
	Status           CompactionStatus `json:"status"`
	Compacted        bool             `json:"compacted"`
	MemoryFlushed    bool             `json:"memory_flushed"`
	MemoryFlushError string           `json:"memory_flush_error,omitempty"`
	SummaryChars     int              `json:"summary_chars"`
	Error            string           `json:"error,omitempty"`
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
func (s *Session) appendMsgs(ctx context.Context, peerID string, maxMsgs, compactThreshold, compactReserve int, compactor Compactor, flusher CompactionMemoryFlusher, hooks *ops.Manager, msgs []types.Message) {
	s.Messages = append(s.Messages, msgs...)
	s.lastActivity = time.Now().UTC()

	// Compaction path: LLM summarises old turns when threshold is reached.
	if compactor != nil && compactThreshold > 0 && len(s.Messages) >= compactThreshold {
		if report := s.compact(ctx, peerID, compactor, flusher, hooks, compactReserve); report.Compacted {
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

// compact flushes the soon-to-be-compacted transcript to memory, calls the
// Compactor to summarise old messages, updates s.Messages, and rewrites the
// session file.
// Must be called with s.mu held.
func (s *Session) compact(ctx context.Context, peerID string, compactor Compactor, flusher CompactionMemoryFlusher, hooks *ops.Manager, reserve int) CompactionReport {
	report := CompactionReport{Status: buildCompactionStatus(len(s.Messages), reserve, compactor != nil, flusher != nil)}
	if !report.Status.Eligible || !report.Status.Enabled {
		return report
	}
	splitIdx := report.Status.CompactedMessages
	toCompact := make([]types.Message, splitIdx)
	copy(toCompact, s.Messages[:splitIdx])
	kept := s.Messages[splitIdx:]
	if hooks != nil {
		if err := hooks.Emit(ctx, ops.Event{
			Name:   ops.HookBeforeCompaction,
			PeerID: peerID,
			Data: map[string]any{
				"messages": len(toCompact),
				"reserve":  report.Status.Reserve,
			},
		}); err != nil {
			slog.Warn("session compaction hook rejected", "peer", peerID, "err", err)
			report.Error = err.Error()
			return report
		}
	}
	if flusher != nil {
		if err := flusher.FlushCompaction(ctx, peerID, toCompact); err != nil {
			report.MemoryFlushError = err.Error()
			slog.Warn("session compaction memory flush failed", "peer", peerID, "err", err)
		} else {
			report.MemoryFlushed = true
		}
	}

	summary, err := compactor.Compact(ctx, toCompact)
	if err != nil {
		slog.Warn("session compaction failed, keeping full history", "err", err)
		report.Error = err.Error()
		return report
	}

	checkpoint := types.Message{
		Role:    "system",
		Content: "<summary>" + summary + "</summary>",
	}
	s.Messages = append([]types.Message{checkpoint}, kept...)
	s.rewriteFile()
	report.Compacted = true
	report.SummaryChars = len(summary)
	if hooks != nil {
		if err := hooks.Emit(ctx, ops.Event{
			Name:   ops.HookAfterCompaction,
			PeerID: peerID,
			Data: map[string]any{
				"summary_chars": len(summary),
				"kept_messages": len(kept),
			},
		}); err != nil {
			slog.Warn("session post-compaction hook failed", "peer", peerID, "err", err)
		}
	}
	return report
}

func buildCompactionStatus(totalMessages, reserve int, enabled, memoryFlushEnabled bool) CompactionStatus {
	status := CompactionStatus{
		Enabled:            enabled,
		MemoryFlushEnabled: memoryFlushEnabled,
		SessionMessages:    totalMessages,
	}
	if reserve <= 0 || reserve >= totalMessages {
		reserve = totalMessages / 2
		if totalMessages > 1 && reserve <= 0 {
			reserve = 1
		}
	}
	if reserve < 0 {
		reserve = 0
	}
	status.Reserve = reserve
	if !enabled {
		status.Reason = "no compactor configured"
		return status
	}
	if totalMessages == 0 {
		status.Reason = "session is empty"
		return status
	}
	status.CompactedMessages = totalMessages - reserve
	if status.CompactedMessages <= 0 {
		status.CompactedMessages = totalMessages
	}
	status.KeptMessages = totalMessages - status.CompactedMessages
	status.Eligible = status.CompactedMessages > 0
	if !status.Eligible {
		status.Reason = "nothing eligible to compact"
	}
	return status
}

// Reset clears all stored messages and truncates the journal file.
func (s *Session) Reset() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Messages = nil
	s.lastActivity = time.Now().UTC()
	s.truncateFile()
}

func (s *Session) LastActivity() time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lastActivity
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
	if info, err := os.Stat(s.filePath); err == nil {
		s.lastActivity = info.ModTime().UTC()
	}
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
	// CompactionMemoryFlusher persists the soon-to-be-compacted transcript before
	// old history is replaced during compaction.
	CompactionMemoryFlusher CompactionMemoryFlusher
	// Hooks emits lifecycle events around compaction.
	Hooks *ops.Manager
	// SessionRetention, when > 0, removes sessions inactive longer than this.
	SessionRetention time.Duration
	// SessionMaxEntries, when > 0, removes the oldest sessions until only this
	// many remain.
	SessionMaxEntries int
	// IdleResetAfter, when > 0, resets a session after this much inactivity.
	IdleResetAfter time.Duration
	// IdlePruneAfter, when > 0, rewrites idle sessions down to IdlePruneKeep
	// messages instead of resetting them entirely.
	IdlePruneAfter time.Duration
	// IdlePruneKeep controls how many most-recent messages remain after an
	// idle-prune cycle. Values <= 0 disable idle pruning.
	IdlePruneKeep int
	// DailyResetMinutes, when >= 0, resets sessions whose latest activity falls
	// before today's local reset cutover and the current time is after it.
	DailyResetMinutes int
	// DailyResetEnabled distinguishes an explicit configured reset time from the
	// zero-value default. Without this, an omitted option would incorrectly
	// behave like a midnight reset.
	DailyResetEnabled bool
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
	memoryFlusher    CompactionMemoryFlusher
	hooks            *ops.Manager
	retention        time.Duration
	maxEntries       int
	idleResetAfter   time.Duration
	idlePruneAfter   time.Duration
	idlePruneKeep    int
	dailyResetMins   int
	policies         map[string]SessionPolicy
	policyPath       string
	subscribers      map[string]map[uint64]func(AppendEvent)
	nextSubID        uint64
}

// CompactionMemoryFlusher persists the to-be-compacted turns before they are
// replaced by a summary checkpoint.
type CompactionMemoryFlusher interface {
	FlushCompaction(ctx context.Context, peerID string, messages []types.Message) error
}

// MaintenanceReport summarizes store maintenance work.
type MaintenanceReport struct {
	DeletedExpired int `json:"deleted_expired"`
	DeletedEvicted int `json:"deleted_evicted"`
	IdleResets     int `json:"idle_resets"`
	IdlePrunes     int `json:"idle_prunes"`
	DailyResets    int `json:"daily_resets"`
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
		memoryFlusher:    opts.CompactionMemoryFlusher,
		hooks:            opts.Hooks,
		retention:        opts.SessionRetention,
		maxEntries:       opts.SessionMaxEntries,
		idleResetAfter:   opts.IdleResetAfter,
		idlePruneAfter:   opts.IdlePruneAfter,
		idlePruneKeep:    opts.IdlePruneKeep,
		dailyResetMins:   -1,
		policies:         make(map[string]SessionPolicy),
		subscribers:      make(map[string]map[uint64]func(AppendEvent)),
	}
	if opts.DailyResetEnabled {
		st.dailyResetMins = opts.DailyResetMinutes
	}
	if opts.SessionDir != "" {
		if err := os.MkdirAll(opts.SessionDir, 0o700); err != nil {
			slog.Warn("session store: cannot create session dir", "dir", opts.SessionDir, "err", err)
		}
		st.policyPath = filepath.Join(opts.SessionDir, "_policies.json")
		st.loadPolicies()
	}
	return st
}

// Get returns the session for peerID, creating and loading it on first access.
func (st *Store) Get(peerID string) *Session {
	sess := st.getOrLoad(peerID)
	st.applyLifecycle(peerID, sess, time.Now())
	return sess
}

func (st *Store) getOrLoad(peerID string) *Session {
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
	msgs = redact.Messages(msgs)
	sess := st.Get(peerID)
	sess.mu.Lock()
	sess.appendMsgs(ctx, peerID, st.maxMsgs, st.compactThreshold, st.compactReserve, st.compactor, st.memoryFlusher, st.hooks, msgs)
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

// Maintain applies reset and cleanup policies across loaded and persisted sessions.
func (st *Store) Maintain(now time.Time) MaintenanceReport {
	report := MaintenanceReport{}
	st.mu.RLock()
	loaded := make(map[string]*Session, len(st.peers))
	for peerID, sess := range st.peers {
		loaded[peerID] = sess
	}
	st.mu.RUnlock()

	if st.idleResetAfter > 0 || (st.idlePruneAfter > 0 && st.idlePruneKeep > 0) || st.dailyResetMins >= 0 {
		for _, rec := range st.sessionRecords(now) {
			if _, ok := loaded[rec.peerID]; ok {
				continue
			}
			loaded[rec.peerID] = st.getOrLoad(rec.peerID)
		}
	}

	for peerID, sess := range loaded {
		switch st.applyLifecycle(peerID, sess, now) {
		case "idle":
			report.IdleResets++
		case "idle-prune":
			report.IdlePrunes++
		case "daily":
			report.DailyResets++
		}
	}

	records := st.sessionRecords(now)
	if st.retention > 0 {
		for _, rec := range records {
			if rec.lastActivity.IsZero() {
				continue
			}
			if now.Sub(rec.lastActivity) >= st.retention {
				if st.deleteSession(rec.peerID) {
					report.DeletedExpired++
				}
			}
		}
		records = st.sessionRecords(now)
	}
	if st.maxEntries > 0 && len(records) > st.maxEntries {
		sort.Slice(records, func(i, j int) bool {
			return records[i].lastActivity.Before(records[j].lastActivity)
		})
		for _, rec := range records[:len(records)-st.maxEntries] {
			if st.deleteSession(rec.peerID) {
				report.DeletedEvicted++
			}
		}
	}
	return report
}

func (st *Store) MaintenanceEnabled() bool {
	return st.retention > 0 || st.maxEntries > 0 || st.idleResetAfter > 0 || (st.idlePruneAfter > 0 && st.idlePruneKeep > 0) || st.dailyResetMins >= 0
}

type sessionRecord struct {
	peerID       string
	lastActivity time.Time
}

func (st *Store) sessionRecords(now time.Time) []sessionRecord {
	st.mu.RLock()
	loaded := make(map[string]*Session, len(st.peers))
	for peerID, sess := range st.peers {
		loaded[peerID] = sess
	}
	sessionDir := st.sessionDir
	st.mu.RUnlock()

	records := make(map[string]sessionRecord, len(loaded))
	for peerID, sess := range loaded {
		last := sess.LastActivity()
		if last.IsZero() {
			last = now
		}
		records[peerID] = sessionRecord{peerID: peerID, lastActivity: last}
	}
	if sessionDir != "" {
		entries, err := os.ReadDir(sessionDir)
		if err == nil {
			for _, entry := range entries {
				if entry.IsDir() || filepath.Ext(entry.Name()) != ".jsonl" {
					continue
				}
				peerID, err := url.PathUnescape(entry.Name()[:len(entry.Name())-len(".jsonl")])
				if err != nil {
					continue
				}
				if _, ok := records[peerID]; ok {
					continue
				}
				info, err := entry.Info()
				if err != nil {
					continue
				}
				records[peerID] = sessionRecord{peerID: peerID, lastActivity: info.ModTime().UTC()}
			}
		}
	}
	out := make([]sessionRecord, 0, len(records))
	for _, rec := range records {
		out = append(out, rec)
	}
	return out
}

func (st *Store) applyLifecycle(peerID string, sess *Session, now time.Time) string {
	if sess == nil {
		return ""
	}
	sess.mu.Lock()
	defer sess.mu.Unlock()
	if len(sess.Messages) == 0 {
		return ""
	}
	if st.idleResetAfter > 0 && !sess.lastActivity.IsZero() && now.Sub(sess.lastActivity) >= st.idleResetAfter {
		sess.Messages = nil
		sess.lastActivity = now.UTC()
		sess.truncateFile()
		return "idle"
	}
	if st.idlePruneAfter > 0 && st.idlePruneKeep > 0 && !sess.lastActivity.IsZero() && now.Sub(sess.lastActivity) >= st.idlePruneAfter {
		if pruneSessionMessages(sess, st.idlePruneKeep) {
			sess.rewriteFile()
			return "idle-prune"
		}
	}
	if st.dailyResetMins >= 0 && !sess.lastActivity.IsZero() {
		if shouldDailyReset(now, sess.lastActivity, st.dailyResetMins) {
			sess.Messages = nil
			sess.lastActivity = now.UTC()
			sess.truncateFile()
			return "daily"
		}
	}
	return ""
}

func pruneSessionMessages(sess *Session, keep int) bool {
	if keep <= 0 || len(sess.Messages) <= keep {
		return false
	}
	sysEnd := 0
	for sysEnd < len(sess.Messages) && sess.Messages[sysEnd].Role == "system" {
		sysEnd++
	}
	if sysEnd >= len(sess.Messages) {
		return false
	}
	nonSystem := len(sess.Messages) - sysEnd
	if nonSystem <= keep {
		return false
	}
	start := len(sess.Messages) - keep
	if start < sysEnd {
		start = sysEnd
	}
	sess.Messages = append(append([]types.Message(nil), sess.Messages[:sysEnd]...), sess.Messages[start:]...)
	return true
}

func (st *Store) SetPolicy(sessionKey string, policy SessionPolicy) error {
	st.mu.Lock()
	defer st.mu.Unlock()
	if policy == (SessionPolicy{}) {
		delete(st.policies, sessionKey)
	} else {
		st.policies[sessionKey] = policy
	}
	return st.savePoliciesLocked()
}

// PatchPolicy reads the current policy for sessionKey, calls patch to mutate
// it in place, then persists the result. A zero-value policy is deleted.
func (st *Store) PatchPolicy(sessionKey string, patch func(*SessionPolicy)) error {
	st.mu.Lock()
	defer st.mu.Unlock()
	p := st.policies[sessionKey]
	patch(&p)
	if p == (SessionPolicy{}) {
		delete(st.policies, sessionKey)
	} else {
		st.policies[sessionKey] = p
	}
	return st.savePoliciesLocked()
}

func (st *Store) Policy(sessionKey string) SessionPolicy {
	st.mu.RLock()
	defer st.mu.RUnlock()
	return st.policies[sessionKey]
}

func (st *Store) SessionKeys(peerID string) []string {
	records := st.sessionRecords(time.Now())
	keys := make([]string, 0, len(records))
	for _, rec := range records {
		if rec.peerID == peerID || strings.HasPrefix(rec.peerID, peerID+"::") {
			keys = append(keys, rec.peerID)
		}
	}
	sort.Strings(keys)
	return keys
}

func (st *Store) loadPolicies() {
	if st.policyPath == "" {
		return
	}
	data, err := os.ReadFile(st.policyPath)
	if os.IsNotExist(err) {
		return
	}
	if err != nil {
		slog.Warn("session policy load", "path", st.policyPath, "err", err)
		return
	}
	var policies map[string]SessionPolicy
	if err := json.Unmarshal(data, &policies); err != nil {
		slog.Warn("session policy parse", "path", st.policyPath, "err", err)
		return
	}
	st.policies = policies
}

func (st *Store) savePoliciesLocked() error {
	if st.policyPath == "" {
		return nil
	}
	data, err := json.MarshalIndent(st.policies, "", "  ")
	if err != nil {
		return err
	}
	tmp := st.policyPath + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, st.policyPath)
}

func shouldDailyReset(now, lastActivity time.Time, resetMinutes int) bool {
	if resetMinutes < 0 {
		return false
	}
	loc := now.Location()
	nowLocal := now.In(loc)
	cutover := time.Date(nowLocal.Year(), nowLocal.Month(), nowLocal.Day(), resetMinutes/60, resetMinutes%60, 0, 0, loc)
	if nowLocal.Before(cutover) {
		return false
	}
	return lastActivity.In(loc).Before(cutover)
}

// CompactionStatus reports whether peerID can currently be compacted and how
// many messages would be summarized vs retained.
func (st *Store) CompactionStatus(peerID string) CompactionStatus {
	st.mu.RLock()
	compactorEnabled := st.compactor != nil
	memoryFlushEnabled := st.memoryFlusher != nil
	reserve := st.compactReserve
	st.mu.RUnlock()
	sess := st.Get(peerID)
	sess.mu.Lock()
	defer sess.mu.Unlock()
	return buildCompactionStatus(len(sess.Messages), reserve, compactorEnabled, memoryFlushEnabled)
}

// CompactNow forces immediate compaction for peerID if a compactor is
// configured and the session has something to compact.
func (st *Store) CompactNow(ctx context.Context, peerID string) CompactionReport {
	st.mu.RLock()
	compactor := st.compactor
	flusher := st.memoryFlusher
	hooks := st.hooks
	reserve := st.compactReserve
	st.mu.RUnlock()
	sess := st.Get(peerID)
	sess.mu.Lock()
	defer sess.mu.Unlock()
	return sess.compact(ctx, peerID, compactor, flusher, hooks, reserve)
}

func (st *Store) deleteSession(peerID string) bool {
	st.mu.Lock()
	sess, ok := st.peers[peerID]
	if ok {
		delete(st.peers, peerID)
	}
	sessionDir := st.sessionDir
	st.mu.Unlock()

	var filePath string
	if ok && sess != nil && sess.filePath != "" {
		filePath = sess.filePath
	} else if sessionDir != "" {
		filePath = filepath.Join(sessionDir, url.PathEscape(peerID)+".jsonl")
	}
	if filePath != "" {
		if err := os.Remove(filePath); err != nil && !os.IsNotExist(err) {
			slog.Warn("session delete: remove", "peer", peerID, "path", filePath, "err", err)
			return false
		}
	}
	return ok || filePath != ""
}
