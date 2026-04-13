package subagent

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/ffimnsr/koios/internal/agent"
	"github.com/ffimnsr/koios/internal/session"
	"github.com/ffimnsr/koios/internal/types"
)

// Runtime orchestrates child agent runs.
type Runtime struct {
	agent       *agent.Runtime
	store       *session.Store
	reg         *Registry
	mu          sync.Mutex
	cancels     map[string]context.CancelFunc
	children    map[string]int
	maxChildren int
	defaultRole Role
}

// NewRuntime creates a subagent runtime.
func NewRuntime(agentRuntime *agent.Runtime, store *session.Store, reg *Registry, maxChildren int) *Runtime {
	if maxChildren <= 0 {
		maxChildren = 4
	}
	return &Runtime{
		agent:       agentRuntime,
		store:       store,
		reg:         reg,
		cancels:     make(map[string]context.CancelFunc),
		children:    make(map[string]int),
		maxChildren: maxChildren,
		defaultRole: RoleLeaf,
	}
}

// Spawn starts a child run asynchronously.
func (r *Runtime) Spawn(ctx context.Context, req SpawnRequest) (*RunRecord, error) {
	if r.agent == nil {
		return nil, fmt.Errorf("agent runtime is nil")
	}
	if req.PeerID == "" {
		return nil, fmt.Errorf("peer_id is required")
	}
	if req.Task == "" {
		return nil, fmt.Errorf("task is required")
	}
	parent := req.ParentID
	if parent != "" {
		r.mu.Lock()
		count := r.children[parent]
		if req.MaxChildren <= 0 {
			req.MaxChildren = r.maxChildren
		}
		if count >= req.MaxChildren {
			r.mu.Unlock()
			return nil, fmt.Errorf("max children reached for parent %s", parent)
		}
		r.children[parent] = count + 1
		r.mu.Unlock()
	}
	if req.Role == "" {
		req.Role = r.defaultRole
	}
	if req.Control == "" {
		req.Control = ControlChildren
	}
	if req.Model == "" {
		req.Model = r.agent.Model()
	}
	if req.SessionKey == "" {
		req.SessionKey = fmt.Sprintf("%s::subagent::%s", req.PeerID, uuid.NewString())
	}
	rec := r.reg.Spawn(req, req.SessionKey)
	_, _ = r.reg.AppendEvent(rec.ID, "queue", "child enqueued")

	childCtx, cancel := context.WithCancel(ctx)
	r.mu.Lock()
	r.cancels[rec.ID] = cancel
	r.mu.Unlock()

	go r.execute(childCtx, rec.ID, req)
	return rec, nil
}

func (r *Runtime) execute(ctx context.Context, id string, req SpawnRequest) {
	_, _ = r.reg.Update(id, func(rec *RunRecord) {
		rec.Status = StatusRunning
		rec.StartedAt = time.Now().UTC()
		rec.Events = append(rec.Events, LifecycleEvent{At: rec.StartedAt, Type: "start", Message: "child started"})
	})

	// Build the task message, injecting any attachment content.
	taskContent := req.Task
	if len(req.Attachments) > 0 {
		var sb strings.Builder
		sb.WriteString(req.Task)
		for _, a := range req.Attachments {
			sb.WriteString("\n\n--- Attachment: ")
			sb.WriteString(a.Name)
			if a.MimeType != "" {
				sb.WriteString(" (")
				sb.WriteString(a.MimeType)
				sb.WriteString(")")
			}
			sb.WriteString(" ---\n")
			sb.WriteString(a.Content)
		}
		taskContent = sb.String()
	}
	runtimeReq := agent.RunRequest{
		PeerID:     req.PeerID,
		SenderID:   req.ParentID,
		Scope:      agent.ScopeIsolated,
		SessionKey: req.SessionKey,
		Messages: []types.Message{
			{Role: "system", Content: fmt.Sprintf("You are a child agent. Task: %s", req.Task)},
			{Role: "user", Content: taskContent},
		},
		Model:   req.Model,
		Stream:  req.Stream,
		Timeout: req.Timeout,
	}
	result, err := r.agent.Run(ctx, runtimeReq)
	now := time.Now().UTC()
	r.mu.Lock()
	delete(r.cancels, id)
	r.mu.Unlock()
	r.releaseParent(req.ParentID)
	if err != nil {
		_, _ = r.reg.Update(id, func(rec *RunRecord) {
			rec.Status = StatusErrored
			rec.Error = err.Error()
			rec.FinishedAt = now
			rec.Events = append(rec.Events, LifecycleEvent{At: now, Type: "error", Message: err.Error()})
		})
		slog.Warn("subagent run failed", "run", id, "error", err)
		return
	}
	_, _ = r.reg.Update(id, func(rec *RunRecord) {
		rec.Status = StatusCompleted
		rec.FinishedAt = now
		rec.FinalReply = result.AssistantText
		rec.Transcript = append([]types.Message(nil), r.store.Get(result.SessionKey).History()...)
		rec.Events = append(rec.Events, LifecycleEvent{At: now, Type: "complete", Message: result.AssistantText})
	})

	// If requested, push the child's reply to the parent peer's main session so
	// the parent is automatically aware of the result without polling.
	if req.PushToParent && req.PeerID != "" && result.AssistantText != "" {
		pushMsg := fmt.Sprintf("[subagent:%s] %s", id, result.AssistantText)
		r.store.AppendWithSource(req.PeerID, "subagent", types.Message{
			Role:    "assistant",
			Content: pushMsg,
		})
	}
}

// Kill cancels a running child run.
func (r *Runtime) Kill(id string) error {
	r.mu.Lock()
	cancel, ok := r.cancels[id]
	if ok {
		delete(r.cancels, id)
	}
	r.mu.Unlock()
	if !ok {
		return fmt.Errorf("run %s not active", id)
	}
	cancel()
	if rec, ok := r.reg.Get(id); ok {
		r.releaseParent(rec.ParentID)
	}
	_, _ = r.reg.Update(id, func(rec *RunRecord) {
		rec.Status = StatusKilled
		rec.FinishedAt = time.Now().UTC()
		rec.Events = append(rec.Events, LifecycleEvent{At: rec.FinishedAt, Type: "killed", Message: "run cancelled"})
	})
	return nil
}

// Steer appends a steering note to an active child session.
// Returns an error if the run is not in a running or queued state.
func (r *Runtime) Steer(id, note string) (*RunRecord, error) {
	rec, ok := r.reg.Get(id)
	if !ok {
		return nil, fmt.Errorf("run %s not found", id)
	}
	if note == "" {
		return nil, fmt.Errorf("note is required")
	}
	if rec.Status != StatusRunning && rec.Status != StatusQueued {
		return nil, fmt.Errorf("run %s is not active (status: %s)", id, rec.Status)
	}
	if err := r.agent.Steer(rec.SessionKey, note); err != nil {
		return nil, err
	}
	updated, _ := r.reg.Update(id, func(record *RunRecord) {
		record.Steering = append(record.Steering, note)
		record.Events = append(record.Events, LifecycleEvent{At: time.Now().UTC(), Type: "steer", Message: note})
	})
	return updated, nil
}

// List returns all known child runs.
func (r *Runtime) List() []*RunRecord {
	return r.reg.List()
}

// Get returns a child run by ID.
func (r *Runtime) Get(id string) (*RunRecord, bool) {
	return r.reg.Get(id)
}

// Transcript returns the message history for a child run.
// For finished runs the snapshot captured at completion time is preferred, as
// the in-memory session may have been evicted or compacted since then.
func (r *Runtime) Transcript(id string) ([]types.Message, error) {
	rec, ok := r.reg.Get(id)
	if !ok {
		return nil, fmt.Errorf("run %s not found", id)
	}
	if rec.Status != StatusRunning && rec.Status != StatusQueued && len(rec.Transcript) > 0 {
		return rec.Transcript, nil
	}
	return r.store.Get(rec.SessionKey).History(), nil
}

func (r *Runtime) releaseParent(parentID string) {
	if parentID == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if count, ok := r.children[parentID]; ok {
		if count <= 1 {
			delete(r.children, parentID)
		} else {
			r.children[parentID] = count - 1
		}
	}
}
