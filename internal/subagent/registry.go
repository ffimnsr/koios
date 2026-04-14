package subagent

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/ffimnsr/koios/internal/types"
)

// Role describes a subagent's depth-based capability tier.
type Role string

const (
	RoleMain         Role = "main"
	RoleOrchestrator Role = "orchestrator"
	RoleLeaf         Role = "leaf"
)

// ControlScope describes whether a run may create children.
type ControlScope string

const (
	ControlChildren ControlScope = "children"
	ControlNone     ControlScope = "none"
)

// Status describes the current lifecycle state of a subagent run.
type Status string

const (
	StatusQueued    Status = "queued"
	StatusRunning   Status = "running"
	StatusCompleted Status = "completed"
	StatusErrored   Status = "errored"
	StatusKilled    Status = "killed"
	StatusReset     Status = "session-reset"
	StatusDeleted   Status = "session-delete"
)

// LifecycleEvent records one state transition or orchestration step.
type LifecycleEvent struct {
	At      time.Time `json:"at"`
	Type    string    `json:"type"`
	Message string    `json:"message,omitempty"`
}

// SubTurnState describes the high-level orchestration phase of a child turn.
type SubTurnState string

const (
	SubTurnQueued    SubTurnState = "queued"
	SubTurnStarting  SubTurnState = "starting"
	SubTurnRunning   SubTurnState = "running"
	SubTurnCompleted SubTurnState = "completed"
	SubTurnErrored   SubTurnState = "errored"
	SubTurnKilled    SubTurnState = "killed"
)

// SubTurn captures structured parent/child coordination metadata for one
// spawned subagent turn.
type SubTurn struct {
	ID               string       `json:"id"`
	State            SubTurnState `json:"state"`
	ParentSessionKey string       `json:"parent_session_key,omitempty"`
	ParentRunID      string       `json:"parent_run_id,omitempty"`
	SourceSessionKey string       `json:"source_session_key,omitempty"`
	ChildSessionKey  string       `json:"child_session_key,omitempty"`
	ReservedSlot     bool         `json:"reserved_slot"`
	ConcurrencyKey   string       `json:"concurrency_key,omitempty"`
	ConcurrencyLimit int          `json:"concurrency_limit,omitempty"`
	ActiveChildren   int          `json:"active_children,omitempty"`
	Steps            int          `json:"steps,omitempty"`
	ToolCalls        int          `json:"tool_calls,omitempty"`
	LastEvent        string       `json:"last_event,omitempty"`
	LastEventAt      time.Time    `json:"last_event_at,omitempty"`
	QueuedAt         time.Time    `json:"queued_at,omitempty"`
	StartedAt        time.Time    `json:"started_at,omitempty"`
	FinishedAt       time.Time    `json:"finished_at,omitempty"`
}

// Attachment is carried with a subagent spawn request.
type Attachment struct {
	Name     string `json:"name"`
	MimeType string `json:"mime_type,omitempty"`
	Content  string `json:"content,omitempty"`
}

// SpawnRequest describes a child agent to start.
type SpawnRequest struct {
	PeerID           string        `json:"peer_id"`
	ParentID         string        `json:"parent_id,omitempty"`
	ParentRunID      string        `json:"parent_run_id,omitempty"`
	ParentSessionKey string        `json:"parent_session_key,omitempty"`
	SourceSessionKey string        `json:"source_session_key,omitempty"`
	Task             string        `json:"task"`
	Model            string        `json:"model,omitempty"`
	Timeout          time.Duration `json:"timeout,omitempty"`
	SandboxMode      string        `json:"sandbox_mode,omitempty"`
	Attachments      []Attachment  `json:"attachments,omitempty"`
	Role             Role          `json:"role,omitempty"`
	Control          ControlScope  `json:"control_scope,omitempty"`
	MaxChildren      int           `json:"max_children,omitempty"`
	SessionKey       string        `json:"session_key,omitempty"`
	Stream           bool          `json:"stream,omitempty"`
	// PushToParent, when true, automatically appends the child's final reply
	// as an assistant message to the parent peer's main session when the run
	// completes successfully.
	PushToParent bool `json:"push_to_parent,omitempty"`
	ReplyBack    bool `json:"reply_back,omitempty"`
	AnnounceSkip bool `json:"announce_skip,omitempty"`
	ReplySkip    bool `json:"reply_skip,omitempty"`
}

// RunRecord is the persisted representation of a spawned subagent.
type RunRecord struct {
	ID           string           `json:"id"`
	PeerID       string           `json:"peer_id"`
	ParentID     string           `json:"parent_id,omitempty"`
	SessionKey   string           `json:"session_key"`
	Task         string           `json:"task"`
	Model        string           `json:"model"`
	Timeout      time.Duration    `json:"timeout,omitempty"`
	SandboxMode  string           `json:"sandbox_mode,omitempty"`
	Attachments  []Attachment     `json:"attachments,omitempty"`
	Role         Role             `json:"role,omitempty"`
	Control      ControlScope     `json:"control_scope,omitempty"`
	MaxChildren  int              `json:"max_children,omitempty"`
	Status       Status           `json:"status"`
	CreatedAt    time.Time        `json:"created_at"`
	StartedAt    time.Time        `json:"started_at,omitempty"`
	FinishedAt   time.Time        `json:"finished_at,omitempty"`
	FinalReply   string           `json:"final_reply,omitempty"`
	Error        string           `json:"error,omitempty"`
	Events       []LifecycleEvent `json:"events,omitempty"`
	Transcript   []types.Message  `json:"transcript,omitempty"`
	Children     []string         `json:"children,omitempty"`
	Steering     []string         `json:"steering,omitempty"`
	ReplyBack    bool             `json:"reply_back,omitempty"`
	AnnounceSkip bool             `json:"announce_skip,omitempty"`
	ReplySkip    bool             `json:"reply_skip,omitempty"`
	SubTurn      SubTurn          `json:"subturn"`
}

// Registry persists run records to disk and restores them on startup.
type Registry struct {
	mu        sync.RWMutex
	dir       string
	records   map[string]*RunRecord
	path      string
	maxAge    time.Duration
	maxEvents int
}

// NewRegistry creates a registry rooted at dir. When dir is empty the registry
// is in-memory only.
func NewRegistry(dir string) (*Registry, error) {
	r := &Registry{dir: dir, records: make(map[string]*RunRecord), maxAge: 24 * time.Hour, maxEvents: 100}
	if dir == "" {
		return r, nil
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("create subagent dir: %w", err)
	}
	r.path = filepath.Join(dir, "subagents.json")
	if err := r.load(); err != nil {
		return nil, err
	}
	return r, nil
}

func (r *Registry) load() error {
	if r.path == "" {
		return nil
	}
	data, err := os.ReadFile(r.path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read subagent registry: %w", err)
	}
	var records []*RunRecord
	if err := json.Unmarshal(data, &records); err != nil {
		return fmt.Errorf("parse subagent registry: %w", err)
	}
	for _, rec := range records {
		r.records[rec.ID] = rec
	}
	return nil
}

func (r *Registry) saveLocked() error {
	if r.path == "" {
		return nil
	}
	list := make([]*RunRecord, 0, len(r.records))
	for _, rec := range r.records {
		copyRec := *rec
		list = append(list, &copyRec)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].CreatedAt.Before(list[j].CreatedAt) })
	data, err := json.MarshalIndent(list, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal subagent registry: %w", err)
	}
	tmp := r.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return fmt.Errorf("write subagent registry: %w", err)
	}
	if err := os.Rename(tmp, r.path); err != nil {
		return fmt.Errorf("rename subagent registry: %w", err)
	}
	return nil
}

// Spawn inserts a new run record and persists it.
func (r *Registry) Spawn(req SpawnRequest, sessionKey string) *RunRecord {
	r.mu.Lock()
	defer r.mu.Unlock()
	rec := &RunRecord{
		ID:           uuid.NewString(),
		PeerID:       req.PeerID,
		ParentID:     req.ParentID,
		SessionKey:   sessionKey,
		Task:         req.Task,
		Model:        req.Model,
		Timeout:      req.Timeout,
		SandboxMode:  req.SandboxMode,
		Attachments:  append([]Attachment(nil), req.Attachments...),
		Role:         req.Role,
		Control:      req.Control,
		MaxChildren:  req.MaxChildren,
		Status:       StatusQueued,
		CreatedAt:    time.Now().UTC(),
		ReplyBack:    req.ReplyBack || req.PushToParent,
		AnnounceSkip: req.AnnounceSkip,
		ReplySkip:    req.ReplySkip,
	}
	rec.SubTurn = SubTurn{
		ID:               uuid.NewString(),
		State:            SubTurnQueued,
		ParentSessionKey: req.ParentSessionKey,
		ParentRunID:      req.ParentRunID,
		SourceSessionKey: req.SourceSessionKey,
		ChildSessionKey:  sessionKey,
		QueuedAt:         rec.CreatedAt,
	}
	if rec.SubTurn.SourceSessionKey == "" {
		rec.SubTurn.SourceSessionKey = req.ParentSessionKey
	}
	rec.Events = append(rec.Events, LifecycleEvent{At: rec.CreatedAt, Type: "spawn", Message: req.Task})
	r.records[rec.ID] = rec
	_ = r.saveLocked()
	return rec
}

// Update mutates a run record in-place and persists the registry.
func (r *Registry) Update(id string, fn func(*RunRecord)) (*RunRecord, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	rec, ok := r.records[id]
	if !ok {
		return nil, false
	}
	fn(rec)
	_ = r.saveLocked()
	copyRec := *rec
	return &copyRec, true
}

// Get returns a snapshot copy of a run record.
func (r *Registry) Get(id string) (*RunRecord, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	rec, ok := r.records[id]
	if !ok {
		return nil, false
	}
	copyRec := *rec
	return &copyRec, true
}

// List returns all records sorted by creation time.
func (r *Registry) List() []*RunRecord {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]*RunRecord, 0, len(r.records))
	for _, rec := range r.records {
		copyRec := *rec
		out = append(out, &copyRec)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out
}

// AppendEvent adds a lifecycle event to the run.
func (r *Registry) AppendEvent(id, typ, message string) (*RunRecord, bool) {
	return r.Update(id, func(rec *RunRecord) {
		now := time.Now().UTC()
		rec.Events = append(rec.Events, LifecycleEvent{At: now, Type: typ, Message: message})
		rec.SubTurn.LastEvent = typ
		rec.SubTurn.LastEventAt = now
		if len(rec.Events) > r.maxEvents {
			rec.Events = rec.Events[len(rec.Events)-r.maxEvents:]
		}
	})
}

// LinkChild records a parent/child run relationship when both run ids are known.
func (r *Registry) LinkChild(parentID, childID string) {
	if parentID == "" || childID == "" || parentID == childID {
		return
	}
	_, _ = r.Update(parentID, func(rec *RunRecord) {
		for _, existing := range rec.Children {
			if existing == childID {
				return
			}
		}
		rec.Children = append(rec.Children, childID)
	})
}

// Sweep removes completed records older than maxAge.
func (r *Registry) Sweep(maxAge time.Duration) int {
	if maxAge <= 0 {
		maxAge = r.maxAge
	}
	now := time.Now().UTC()
	r.mu.Lock()
	defer r.mu.Unlock()
	removed := 0
	for id, rec := range r.records {
		if rec.Status == StatusRunning || rec.Status == StatusQueued {
			continue
		}
		finished := rec.FinishedAt
		if finished.IsZero() {
			finished = rec.CreatedAt
		}
		if now.Sub(finished) > maxAge {
			delete(r.records, id)
			removed++
		}
	}
	_ = r.saveLocked()
	return removed
}
