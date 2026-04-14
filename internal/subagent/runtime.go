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
	"github.com/ffimnsr/koios/internal/eventbus"
	"github.com/ffimnsr/koios/internal/session"
	"github.com/ffimnsr/koios/internal/types"
)

type parentSemaphore struct {
	ch chan struct{}
}

// Runtime orchestrates child agent runs.
type Runtime struct {
	agent       *agent.Runtime
	store       *session.Store
	reg         *Registry
	bus         *eventbus.Bus
	mu          sync.Mutex
	cancels     map[string]context.CancelFunc
	semaphores  map[string]*parentSemaphore
	maxChildren int
	defaultRole Role
}

// NewRuntime creates a subagent runtime.
func NewRuntime(agentRuntime *agent.Runtime, store *session.Store, reg *Registry, bus *eventbus.Bus, maxChildren int) *Runtime {
	if maxChildren <= 0 {
		maxChildren = 4
	}
	return &Runtime{
		agent:       agentRuntime,
		store:       store,
		reg:         reg,
		bus:         bus,
		cancels:     make(map[string]context.CancelFunc),
		semaphores:  make(map[string]*parentSemaphore),
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
	if req.ParentSessionKey == "" {
		req.ParentSessionKey = agent.CurrentSessionKey(ctx)
	}
	if req.ParentSessionKey == "" {
		req.ParentSessionKey = req.PeerID
	}
	if req.SourceSessionKey == "" {
		req.SourceSessionKey = req.ParentSessionKey
	}
	req.Task, req.AnnounceSkip, req.ReplySkip = normalizeSpawnFlags(req.Task, req.AnnounceSkip, req.ReplySkip)
	parentKey := req.ParentSessionKey
	if parentKey == "" {
		parentKey = req.ParentID
	}
	if req.MaxChildren <= 0 {
		req.MaxChildren = r.maxChildren
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
	_, _ = r.reg.Update(rec.ID, func(rec *RunRecord) {
		rec.SubTurn.ConcurrencyKey = parentKey
		rec.SubTurn.ConcurrencyLimit = req.MaxChildren
		rec.SubTurn.ReservedSlot = false
	})
	if req.ParentRunID != "" {
		r.reg.LinkChild(req.ParentRunID, rec.ID)
	}
	_, _ = r.reg.AppendEvent(rec.ID, "queue", "child enqueued")
	r.publishLifecycle(rec.ID, rec.PeerID, rec.SessionKey, "subagent.queued", map[string]any{
		"parent_session_key": req.ParentSessionKey,
		"source_session_key": req.SourceSessionKey,
		"announce_skip":      req.AnnounceSkip,
		"reply_skip":         req.ReplySkip,
	})
	r.announce(rec, req)

	childCtx, cancel := context.WithCancel(ctx)
	r.mu.Lock()
	r.cancels[rec.ID] = cancel
	r.mu.Unlock()

	go r.execute(childCtx, rec.ID, req, parentKey)
	return rec, nil
}

func (r *Runtime) execute(ctx context.Context, id string, req SpawnRequest, parentKey string) {
	if err := r.acquireParentSlot(ctx, id, parentKey, req.MaxChildren); err != nil {
		now := time.Now().UTC()
		_, _ = r.reg.Update(id, func(rec *RunRecord) {
			rec.Status = StatusKilled
			rec.Error = err.Error()
			rec.FinishedAt = now
			rec.SubTurn.State = SubTurnKilled
			rec.SubTurn.FinishedAt = now
			rec.Events = append(rec.Events, LifecycleEvent{At: now, Type: "cancelled", Message: err.Error()})
		})
		r.publishLifecycle(id, req.PeerID, req.SessionKey, "subagent.cancelled", map[string]any{"error": err.Error()})
		return
	}
	defer r.releaseParent(parentKey, req.ParentID)

	_, _ = r.reg.Update(id, func(rec *RunRecord) {
		rec.Status = StatusRunning
		rec.StartedAt = time.Now().UTC()
		rec.SubTurn.State = SubTurnStarting
		rec.SubTurn.StartedAt = rec.StartedAt
		rec.SubTurn.ReservedSlot = parentKey != ""
		rec.Events = append(rec.Events, LifecycleEvent{At: rec.StartedAt, Type: "start", Message: "child started"})
	})
	r.publishLifecycle(id, req.PeerID, req.SessionKey, "subagent.started", map[string]any{
		"parent_session_key": req.ParentSessionKey,
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
		EventSink: func(ev agent.Event) {
			r.onAgentEvent(id, ev)
		},
	}
	result, err := r.agent.Run(ctx, runtimeReq)
	now := time.Now().UTC()
	r.mu.Lock()
	delete(r.cancels, id)
	r.mu.Unlock()
	if err != nil {
		_, _ = r.reg.Update(id, func(rec *RunRecord) {
			rec.Status = StatusErrored
			rec.Error = err.Error()
			rec.FinishedAt = now
			rec.SubTurn.State = SubTurnErrored
			rec.SubTurn.FinishedAt = now
			rec.Events = append(rec.Events, LifecycleEvent{At: now, Type: "error", Message: err.Error()})
		})
		r.publishLifecycle(id, req.PeerID, req.SessionKey, "subagent.errored", map[string]any{"error": err.Error()})
		slog.Warn("subagent run failed", "run", id, "error", err)
		return
	}
	_, _ = r.reg.Update(id, func(rec *RunRecord) {
		rec.Status = StatusCompleted
		rec.FinishedAt = now
		rec.FinalReply = result.AssistantText
		rec.Transcript = append([]types.Message(nil), r.store.Get(result.SessionKey).History()...)
		rec.SubTurn.State = SubTurnCompleted
		rec.SubTurn.FinishedAt = now
		rec.SubTurn.Steps = result.Steps
		rec.Events = append(rec.Events, LifecycleEvent{At: now, Type: "complete", Message: result.AssistantText})
	})
	r.publishLifecycle(id, req.PeerID, req.SessionKey, "subagent.completed", map[string]any{
		"steps":      result.Steps,
		"tool_calls": currentToolCalls(r.reg, id),
	})

	// If requested, push the child's reply to the parent peer's main session so
	// the parent is automatically aware of the result without polling.
	replyBack := req.PushToParent || req.ReplyBack
	if current, ok := r.reg.Get(id); ok {
		replyBack = current.ReplyBack && !current.ReplySkip
	}
	if replyBack && req.PeerID != "" && result.AssistantText != "" {
		pushMsg := fmt.Sprintf("[subagent:%s] %s", id, result.AssistantText)
		msg := types.Message{
			Role:    "assistant",
			Content: pushMsg,
		}
		r.publishSessionMessage(eventbus.Event{
			Kind:       "session.message",
			PeerID:     req.PeerID,
			SessionKey: req.SourceSessionKeyOrPeer(),
			Source:     "subagent",
			RunID:      id,
			Message:    &msg,
			Data:       map[string]any{"kind": "reply"},
		}, msg)
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
		r.releaseParent(rec.SubTurn.ParentSessionKey, rec.ParentID)
	}
	_, _ = r.reg.Update(id, func(rec *RunRecord) {
		rec.Status = StatusKilled
		rec.FinishedAt = time.Now().UTC()
		rec.SubTurn.State = SubTurnKilled
		rec.SubTurn.FinishedAt = rec.FinishedAt
		rec.Events = append(rec.Events, LifecycleEvent{At: rec.FinishedAt, Type: "killed", Message: "run cancelled"})
	})
	r.publishLifecycle(id, "", "", "subagent.killed", nil)
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

// SetReplyBack updates the persisted reply-back policy for a run.
func (r *Runtime) SetReplyBack(id string, enabled bool) (*RunRecord, error) {
	updated, ok := r.reg.Update(id, func(record *RunRecord) {
		record.ReplyBack = enabled
		record.Events = append(record.Events, LifecycleEvent{
			At:      time.Now().UTC(),
			Type:    "reply_back",
			Message: fmt.Sprintf("reply_back=%t", enabled),
		})
	})
	if !ok {
		return nil, fmt.Errorf("run %s not found", id)
	}
	return updated, nil
}

func (r *Runtime) onAgentEvent(id string, ev agent.Event) {
	_, _ = r.reg.Update(id, func(rec *RunRecord) {
		now := time.Now().UTC()
		rec.SubTurn.LastEvent = string(ev.Kind)
		rec.SubTurn.LastEventAt = now
		switch ev.Kind {
		case agent.EventRunStart:
			rec.SubTurn.State = SubTurnRunning
			if rec.SubTurn.StartedAt.IsZero() {
				rec.SubTurn.StartedAt = now
			}
		case agent.EventStepStart:
			if ev.Step > rec.SubTurn.Steps {
				rec.SubTurn.Steps = ev.Step
			}
		case agent.EventToolCall:
			rec.SubTurn.ToolCalls++
		case agent.EventRunFinish:
			rec.SubTurn.State = SubTurnCompleted
		case agent.EventRunError:
			rec.SubTurn.State = SubTurnErrored
		}
		rec.Events = append(rec.Events, LifecycleEvent{
			At:      now,
			Type:    "agent." + string(ev.Kind),
			Message: formatAgentEventMessage(ev),
		})
		if len(rec.Events) > 100 {
			rec.Events = rec.Events[len(rec.Events)-100:]
		}
	})
	r.publishLifecycle(id, "", ev.SessionKey, "subagent.lifecycle", map[string]any{
		"event":   ev.Kind,
		"step":    ev.Step,
		"attempt": ev.Attempt,
		"count":   ev.Count,
		"message": ev.Message,
		"error":   ev.Error,
	})
}

func formatAgentEventMessage(ev agent.Event) string {
	parts := make([]string, 0, 4)
	if ev.Step > 0 {
		parts = append(parts, fmt.Sprintf("step=%d", ev.Step))
	}
	if ev.Attempt > 0 {
		parts = append(parts, fmt.Sprintf("attempt=%d", ev.Attempt))
	}
	if ev.Count > 0 {
		parts = append(parts, fmt.Sprintf("count=%d", ev.Count))
	}
	if ev.Message != "" {
		parts = append(parts, ev.Message)
	}
	if ev.Error != "" {
		parts = append(parts, "error="+ev.Error)
	}
	return strings.Join(parts, " ")
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

func (r *Runtime) releaseParent(parentSessionKey, parentID string) {
	parentKey := parentSessionKey
	if parentKey == "" {
		parentKey = parentID
	}
	if parentKey == "" {
		return
	}
	r.mu.Lock()
	sem, ok := r.semaphores[parentKey]
	r.mu.Unlock()
	if !ok {
		return
	}
	select {
	case <-sem.ch:
	default:
	}
}

func (r *Runtime) acquireParentSlot(ctx context.Context, id, parentKey string, limit int) error {
	if parentKey == "" {
		return nil
	}
	if limit <= 0 {
		limit = r.maxChildren
	}
	sem := r.parentSemaphore(parentKey, limit)
	_, _ = r.reg.AppendEvent(id, "semaphore.wait", fmt.Sprintf("waiting for parent session slot %s", parentKey))
	r.publishLifecycle(id, "", "", "subagent.waiting", map[string]any{
		"parent_session_key": parentKey,
		"limit":              limit,
	})
	select {
	case sem.ch <- struct{}{}:
	case <-ctx.Done():
		return ctx.Err()
	}
	activeChildren := len(sem.ch)
	_, _ = r.reg.Update(id, func(rec *RunRecord) {
		rec.SubTurn.ReservedSlot = true
		rec.SubTurn.ActiveChildren = activeChildren
		rec.SubTurn.ConcurrencyKey = parentKey
		rec.SubTurn.ConcurrencyLimit = limit
	})
	_, _ = r.reg.AppendEvent(id, "semaphore.acquire", fmt.Sprintf("acquired parent session slot %s", parentKey))
	r.publishLifecycle(id, "", "", "subagent.acquired", map[string]any{
		"parent_session_key": parentKey,
		"active_children":    activeChildren,
		"limit":              limit,
	})
	return nil
}

func (r *Runtime) parentSemaphore(parentKey string, limit int) *parentSemaphore {
	r.mu.Lock()
	defer r.mu.Unlock()
	sem, ok := r.semaphores[parentKey]
	if ok && cap(sem.ch) == limit {
		return sem
	}
	if ok && cap(sem.ch) != limit {
		sem = &parentSemaphore{ch: make(chan struct{}, limit)}
		r.semaphores[parentKey] = sem
		return sem
	}
	sem = &parentSemaphore{ch: make(chan struct{}, limit)}
	r.semaphores[parentKey] = sem
	return sem
}

func (r *Runtime) announce(rec *RunRecord, req SpawnRequest) {
	if rec == nil {
		return
	}
	if rec.AnnounceSkip {
		_, _ = r.reg.AppendEvent(rec.ID, "announce_skip", "announcement suppressed")
		return
	}
	target := req.SourceSessionKeyOrPeer()
	if target == "" {
		return
	}
	msg := types.Message{
		Role:    "assistant",
		Content: fmt.Sprintf("[subagent:%s] queued: %s", rec.ID, rec.Task),
	}
	r.publishSessionMessage(eventbus.Event{
		Kind:       "session.message",
		PeerID:     rec.PeerID,
		SessionKey: target,
		Source:     "subagent",
		RunID:      rec.ID,
		Message:    &msg,
		Data:       map[string]any{"kind": "announcement"},
	}, msg)
	_, _ = r.reg.AppendEvent(rec.ID, "announce", msg.Content)
}

func (r *Runtime) publishSessionMessage(ev eventbus.Event, fallback types.Message) {
	if ev.Message == nil {
		ev.Message = &fallback
	}
	if ev.Kind == "" {
		ev.Kind = "session.message"
	}
	if ev.SessionKey == "" {
		ev.SessionKey = ev.PeerID
	}
	if r.bus != nil {
		r.bus.Publish(ev)
		return
	}
	r.store.AppendWithSource(ev.SessionKey, ev.Source, *ev.Message)
}

func (r *Runtime) publishLifecycle(id, peerID, sessionKey, kind string, data map[string]any) {
	if r.bus == nil {
		return
	}
	r.bus.Publish(eventbus.Event{
		Kind:       kind,
		PeerID:     peerID,
		SessionKey: sessionKey,
		Source:     "subagent",
		RunID:      id,
		Data:       data,
	})
}

func normalizeSpawnFlags(task string, announceSkip, replySkip bool) (string, bool, bool) {
	fields := strings.Fields(task)
	if len(fields) == 0 {
		return strings.TrimSpace(task), announceSkip, replySkip
	}
	filtered := make([]string, 0, len(fields))
	for _, field := range fields {
		switch field {
		case "ANNOUNCE_SKIP":
			announceSkip = true
		case "REPLY_SKIP":
			replySkip = true
		default:
			filtered = append(filtered, field)
		}
	}
	return strings.TrimSpace(strings.Join(filtered, " ")), announceSkip, replySkip
}

func currentToolCalls(reg *Registry, id string) int {
	if reg == nil {
		return 0
	}
	rec, ok := reg.Get(id)
	if !ok {
		return 0
	}
	return rec.SubTurn.ToolCalls
}

func (req SpawnRequest) SourceSessionKeyOrPeer() string {
	if req.SourceSessionKey != "" {
		return req.SourceSessionKey
	}
	if req.ParentSessionKey != "" {
		return req.ParentSessionKey
	}
	return req.PeerID
}
