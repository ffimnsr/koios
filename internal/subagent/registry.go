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

// Attachment is carried with a subagent spawn request.
type Attachment struct {
	Name     string `json:"name"`
	MimeType string `json:"mime_type,omitempty"`
	Content  string `json:"content,omitempty"`
}

// SpawnRequest describes a child agent to start.
type SpawnRequest struct {
	PeerID      string        `json:"peer_id"`
	ParentID    string        `json:"parent_id,omitempty"`
	Task        string        `json:"task"`
	Model       string        `json:"model,omitempty"`
	Timeout     time.Duration `json:"timeout,omitempty"`
	SandboxMode string        `json:"sandbox_mode,omitempty"`
	Attachments []Attachment  `json:"attachments,omitempty"`
	Role        Role          `json:"role,omitempty"`
	Control     ControlScope  `json:"control_scope,omitempty"`
	MaxChildren int           `json:"max_children,omitempty"`
	SessionKey  string        `json:"session_key,omitempty"`
	Stream      bool          `json:"stream,omitempty"`
	// PushToParent, when true, automatically appends the child's final reply
	// as an assistant message to the parent peer's main session when the run
	// completes successfully.
	PushToParent bool `json:"push_to_parent,omitempty"`
}

// RunRecord is the persisted representation of a spawned subagent.
type RunRecord struct {
	ID          string           `json:"id"`
	PeerID      string           `json:"peer_id"`
	ParentID    string           `json:"parent_id,omitempty"`
	SessionKey  string           `json:"session_key"`
	Task        string           `json:"task"`
	Model       string           `json:"model"`
	Timeout     time.Duration    `json:"timeout,omitempty"`
	SandboxMode string           `json:"sandbox_mode,omitempty"`
	Attachments []Attachment     `json:"attachments,omitempty"`
	Role        Role             `json:"role,omitempty"`
	Control     ControlScope     `json:"control_scope,omitempty"`
	MaxChildren int              `json:"max_children,omitempty"`
	Status      Status           `json:"status"`
	CreatedAt   time.Time        `json:"created_at"`
	StartedAt   time.Time        `json:"started_at,omitempty"`
	FinishedAt  time.Time        `json:"finished_at,omitempty"`
	FinalReply  string           `json:"final_reply,omitempty"`
	Error       string           `json:"error,omitempty"`
	Events      []LifecycleEvent `json:"events,omitempty"`
	Transcript  []types.Message  `json:"transcript,omitempty"`
	Children    []string         `json:"children,omitempty"`
	Steering    []string         `json:"steering,omitempty"`
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
		ID:          uuid.NewString(),
		PeerID:      req.PeerID,
		ParentID:    req.ParentID,
		SessionKey:  sessionKey,
		Task:        req.Task,
		Model:       req.Model,
		Timeout:     req.Timeout,
		SandboxMode: req.SandboxMode,
		Attachments: append([]Attachment(nil), req.Attachments...),
		Role:        req.Role,
		Control:     req.Control,
		MaxChildren: req.MaxChildren,
		Status:      StatusQueued,
		CreatedAt:   time.Now().UTC(),
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
		rec.Events = append(rec.Events, LifecycleEvent{At: time.Now().UTC(), Type: typ, Message: message})
		if len(rec.Events) > r.maxEvents {
			rec.Events = rec.Events[len(rec.Events)-r.maxEvents:]
		}
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
