// Package runledger provides a unified, persistent ledger of all background
// runs across the agent coordinator, subagent registry, and scheduler layers.
//
// Records are appended to a JSONL file at <dir>/ledger.jsonl.  Each state
// transition appends a new line; on load the latest entry per ID wins so the
// file acts as a compact event log.  The in-memory index is always current.
package runledger

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

// RunKind identifies the subsystem that created a run.
type RunKind string

const (
	KindAgent        RunKind = "agent"
	KindSubagent     RunKind = "subagent"
	KindOrchestrator RunKind = "orchestrator"
	KindCron         RunKind = "cron"
)

// RunStatus mirrors the status values used across the various runtime layers.
type RunStatus string

const (
	StatusQueued    RunStatus = "queued"
	StatusRunning   RunStatus = "running"
	StatusCompleted RunStatus = "completed"
	StatusErrored   RunStatus = "errored"
	StatusCanceled  RunStatus = "canceled"
	StatusSkipped   RunStatus = "skipped"
)

// Record is a unified ledger entry that captures a run regardless of which
// subsystem produced it.
type Record struct {
	ID         string    `json:"id"`
	Kind       RunKind   `json:"kind"`
	PeerID     string    `json:"peer_id,omitempty"`
	SessionKey string    `json:"session_key,omitempty"`
	Model      string    `json:"model,omitempty"`
	Status     RunStatus `json:"status"`
	Error      string    `json:"error,omitempty"`
	Steps      int       `json:"steps,omitempty"`
	ToolCalls  int       `json:"tool_calls,omitempty"`
	// ParentID links subagent/orchestrator children to their parent run.
	ParentID         string     `json:"parent_id,omitempty"`
	PromptTokens     int        `json:"prompt_tokens,omitempty"`
	CompletionTokens int        `json:"completion_tokens,omitempty"`
	QueuedAt         time.Time  `json:"queued_at"`
	StartedAt        *time.Time `json:"started_at,omitempty"`
	FinishedAt       *time.Time `json:"finished_at,omitempty"`
}

// Filter restricts which records are returned by List.
type Filter struct {
	// PeerID, when non-empty, restricts results to the given peer.
	PeerID string
	// Kind, when non-empty, restricts results to a specific run kind.
	Kind RunKind
	// Status, when non-empty, restricts results to a specific status.
	Status RunStatus
	// Limit caps the number of records returned (0 = no limit).
	Limit int
}

// Store is a persistent run ledger backed by a JSONL append log.
// All public methods are safe for concurrent use.
type Store struct {
	mu    sync.Mutex
	path  string
	f     *os.File
	enc   *json.Encoder
	index map[string]*Record // id → latest record
	order []string           // insertion order of IDs for stable List output
}

// New opens (or creates) the ledger at dir/ledger.jsonl, replaying any
// existing records into the in-memory index.
func New(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("runledger: create dir: %w", err)
	}
	path := filepath.Join(dir, "ledger.jsonl")
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_APPEND, 0o600)
	if err != nil {
		return nil, fmt.Errorf("runledger: open ledger: %w", err)
	}
	s := &Store{
		path:  path,
		f:     f,
		index: make(map[string]*Record),
	}
	s.enc = json.NewEncoder(f)
	if err := s.replay(path); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("runledger: replay: %w", err)
	}
	return s, nil
}

// replay reads existing ledger lines into the in-memory index.  The last
// entry per ID wins, reflecting the latest known state.
func (s *Store) replay(path string) error {
	r, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	defer r.Close()
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var rec Record
		if err := json.Unmarshal(line, &rec); err != nil {
			continue // skip malformed lines
		}
		if rec.ID == "" {
			continue
		}
		if _, exists := s.index[rec.ID]; !exists {
			s.order = append(s.order, rec.ID)
		}
		cp := rec
		s.index[rec.ID] = &cp
	}
	return sc.Err()
}

// Add records a new run entry.  If a record with the same ID already exists
// in the in-memory index the call is a no-op; use Update to change state.
func (s *Store) Add(rec Record) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.index[rec.ID]; exists {
		return nil
	}
	return s.writeLocked(rec)
}

// Update merges changes into the record identified by id by calling fn with
// a pointer to a copy of the current record.  The modified copy is persisted
// and the index is updated.  Returns an error if the ID is not found.
func (s *Store) Update(id string, fn func(*Record)) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	cur, ok := s.index[id]
	if !ok {
		return fmt.Errorf("runledger: record %s not found", id)
	}
	updated := *cur
	fn(&updated)
	return s.writeLocked(updated)
}

// Get returns the record for the given ID, or false if not found.
func (s *Store) Get(id string) (Record, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rec, ok := s.index[id]
	if !ok {
		return Record{}, false
	}
	return *rec, true
}

// List returns records matching the filter, ordered from newest to oldest.
// Terminal records older than retainFor are omitted when retainFor > 0.
func (s *Store) List(f Filter, retainFor time.Duration) []Record {
	s.mu.Lock()
	defer s.mu.Unlock()
	cutoff := time.Time{}
	if retainFor > 0 {
		cutoff = time.Now().Add(-retainFor)
	}
	out := make([]Record, 0, len(s.order))
	for i := len(s.order) - 1; i >= 0; i-- {
		id := s.order[i]
		rec, ok := s.index[id]
		if !ok {
			continue
		}
		// Age filter: drop old terminal records.
		if !cutoff.IsZero() && isTerminal(rec.Status) {
			ts := rec.QueuedAt
			if rec.FinishedAt != nil {
				ts = *rec.FinishedAt
			}
			if ts.Before(cutoff) {
				continue
			}
		}
		if f.PeerID != "" && rec.PeerID != f.PeerID {
			continue
		}
		if f.Kind != "" && rec.Kind != f.Kind {
			continue
		}
		if f.Status != "" && rec.Status != f.Status {
			continue
		}
		out = append(out, *rec)
	}
	sort.SliceStable(out, func(i, j int) bool {
		ti := latestActivityAt(out[i])
		tj := latestActivityAt(out[j])
		if !ti.Equal(tj) {
			return ti.After(tj)
		}
		return out[i].QueuedAt.After(out[j].QueuedAt)
	})
	if f.Limit > 0 && len(out) > f.Limit {
		out = out[:f.Limit]
	}
	return out
}

// Close flushes and closes the underlying file.
func (s *Store) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.f.Close()
}

// writeLocked appends rec to the JSONL file and updates the index.
// Caller must hold s.mu.
func (s *Store) writeLocked(rec Record) error {
	if err := s.enc.Encode(rec); err != nil {
		return fmt.Errorf("runledger: encode: %w", err)
	}
	if _, exists := s.index[rec.ID]; !exists {
		s.order = append(s.order, rec.ID)
	}
	cp := rec
	s.index[rec.ID] = &cp
	return nil
}

func isTerminal(status RunStatus) bool {
	return status == StatusCompleted || status == StatusErrored || status == StatusCanceled || status == StatusSkipped
}

func latestActivityAt(rec Record) time.Time {
	if rec.FinishedAt != nil {
		return *rec.FinishedAt
	}
	if rec.StartedAt != nil {
		return *rec.StartedAt
	}
	return rec.QueuedAt
}
