// Package orchestrator implements multi-session fan-out orchestration with
// reply aggregation for Koios.
//
// An orchestration run spawns N child subagent sessions (via subagent.Runtime),
// tracks their lifecycle, and aggregates their replies once all (or a policy-
// defined subset) have finished. Three aggregation modes are supported:
//
//   - collect  – raw child outputs returned as a list (no LLM pass)
//   - concat   – labeled child replies joined into a single text block
//   - reducer  – a final LLM agent pass over labeled child results; both the
//     raw child outputs and the synthesised reply are stored
//
// # Concurrency safety
//
// All mutable state is protected by the run's mu mutex. The subagent semaphore
// is the sole concurrency rate-limiter for child execution; the orchestrator
// does not introduce an additional semaphore so the two systems cannot deadlock
// against each other.
//
// # Cancellation and orphan prevention
//
// When a run is cancelled (via Cancel or a parent context deadline), the
// orchestrator calls Kill on every active child before marking itself done.
// Children that are already finished are unaffected.
package orchestrator

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
	"github.com/ffimnsr/koios/internal/redact"
	"github.com/ffimnsr/koios/internal/subagent"
	"github.com/ffimnsr/koios/internal/types"
)

// AggregationMode controls how child replies are combined.
type AggregationMode string

const (
	// AggregateCollect returns child replies as-is in a structured list.
	AggregateCollect AggregationMode = "collect"
	// AggregateConcat concatenates labeled child replies into one text block.
	AggregateConcat AggregationMode = "concat"
	// AggregateReducer runs a final LLM pass over labeled child results.
	AggregateReducer AggregationMode = "reducer"
)

// WaitPolicy controls when the orchestration is considered done.
type WaitPolicy string

const (
	// WaitAll waits for every child to finish (default).
	WaitAll WaitPolicy = "all"
	// WaitFirst finishes as soon as the first child completes successfully.
	WaitFirst WaitPolicy = "first"
	// WaitQuorum finishes when at least half of the children complete.
	WaitQuorum WaitPolicy = "quorum"
)

// RunStatus describes the lifecycle state of an orchestration run.
type RunStatus string

const (
	RunStatusStarting    RunStatus = "starting"
	RunStatusRunning     RunStatus = "running"
	RunStatusAggregating RunStatus = "aggregating"
	RunStatusCompleted   RunStatus = "completed"
	RunStatusErrored     RunStatus = "errored"
	RunStatusCancelled   RunStatus = "cancelled"
)

// ChildTask describes one slot in a fan-out request.
type ChildTask struct {
	// Label is a short human-readable identifier used to tag the child's output.
	// When empty a positional label ("child-0", "child-1", …) is used.
	Label string `json:"label,omitempty"`
	// Task is the prompt delivered to the child agent.
	Task string `json:"task"`
	// PeerID overrides the parent's peer ID for this child. When empty the
	// parent's peer ID is used.
	PeerID string `json:"peer_id,omitempty"`
	// Model overrides the model for this child.
	Model string `json:"model,omitempty"`
	// Timeout overrides the child timeout.
	Timeout time.Duration `json:"timeout,omitempty"`
}

// ChildResult captures the outcome of one child agent run.
type ChildResult struct {
	Label      string          `json:"label"`
	RunID      string          `json:"run_id"`
	SessionKey string          `json:"session_key"`
	Status     subagent.Status `json:"status"`
	FinalReply string          `json:"final_reply,omitempty"`
	Error      string          `json:"error,omitempty"`
	Steps      int             `json:"steps,omitempty"`
	ToolCalls  int             `json:"tool_calls,omitempty"`
	StartedAt  time.Time       `json:"started_at,omitempty"`
	FinishedAt time.Time       `json:"finished_at,omitempty"`
}

// FanOutRequest describes a fan-out orchestration run.
type FanOutRequest struct {
	// PeerID is the parent peer that owns this run.
	PeerID string `json:"peer_id"`
	// ParentSessionKey is the session that receives announcements and the final
	// aggregated reply.
	ParentSessionKey string `json:"parent_session_key,omitempty"`
	// Tasks is the list of child tasks to spawn.
	Tasks []ChildTask `json:"tasks"`
	// MaxConcurrency limits how many children run simultaneously (≤0 = unlimited).
	MaxConcurrency int `json:"max_concurrency,omitempty"`
	// Timeout is the wall-clock deadline for the entire orchestration.
	Timeout time.Duration `json:"timeout,omitempty"`
	// ChildTimeout is the per-child execution timeout when not specified inline.
	ChildTimeout time.Duration `json:"child_timeout,omitempty"`
	// WaitPolicy controls the completion criterion.
	WaitPolicy WaitPolicy `json:"wait_policy,omitempty"`
	// Aggregation controls how child results are combined.
	Aggregation AggregationMode `json:"aggregation,omitempty"`
	// ReducerPrompt is the prompt used by the reducer agent when
	// Aggregation == AggregateReducer.
	ReducerPrompt string `json:"reducer_prompt,omitempty"`
	// Model is the model used for all children (overridden per-task by ChildTask.Model).
	Model string `json:"model,omitempty"`
	// AnnounceStart, when true, sends an announcement to ParentSessionKey when
	// the orchestration begins.
	AnnounceStart bool `json:"announce_start,omitempty"`
	// ParentRunID links this orchestration to an existing parent subagent run.
	ParentRunID string `json:"parent_run_id,omitempty"`
}

// Run is the persisted orchestration run record.
type Run struct {
	mu sync.Mutex

	ID               string          `json:"id"`
	PeerID           string          `json:"peer_id"`
	ParentSessionKey string          `json:"parent_session_key,omitempty"`
	ParentRunID      string          `json:"parent_run_id,omitempty"`
	Status           RunStatus       `json:"status"`
	WaitPolicy       WaitPolicy      `json:"wait_policy"`
	Aggregation      AggregationMode `json:"aggregation"`
	ReducerPrompt    string          `json:"reducer_prompt,omitempty"`
	MaxConcurrency   int             `json:"max_concurrency,omitempty"`

	Children []ChildResult `json:"children"`
	// childRunIDs maps subagent run ID → child index for fast lookup.
	childRunIDs map[string]int `json:"-"`

	AggregatedReply string `json:"aggregated_reply,omitempty"`
	Error           string `json:"error,omitempty"`

	CreatedAt  time.Time `json:"created_at"`
	StartedAt  time.Time `json:"started_at,omitempty"`
	FinishedAt time.Time `json:"finished_at,omitempty"`
}

// snapshot returns a copy of the run safe for external use.
func (r *Run) snapshot() Run {
	r.mu.Lock()
	defer r.mu.Unlock()
	cp := *r
	cp.Children = append([]ChildResult(nil), r.Children...)
	cp.childRunIDs = nil
	return cp
}

// Orchestrator manages fan-out orchestration runs.
type Orchestrator struct {
	subRuntime *subagent.Runtime
	agentRT    *agent.Runtime
	bus        *eventbus.Bus

	mu   sync.Mutex
	runs map[string]*Run
	// cancels maps orchestration run ID → cancel function.
	cancels map[string]context.CancelFunc
}

// New creates an Orchestrator. agentRT is only required when reducer aggregation
// is used; it may be nil if only collect/concat modes are needed.
func New(subRuntime *subagent.Runtime, agentRT *agent.Runtime, bus *eventbus.Bus) *Orchestrator {
	return &Orchestrator{
		subRuntime: subRuntime,
		agentRT:    agentRT,
		bus:        bus,
		runs:       make(map[string]*Run),
		cancels:    make(map[string]context.CancelFunc),
	}
}

// Start spawns a new fan-out orchestration and returns the run ID immediately.
// The orchestration executes asynchronously. The supplied ctx is used only as
// the parent for the orchestration's own context; individual child contexts are
// derived from it.
func (o *Orchestrator) Start(ctx context.Context, req FanOutRequest) (*Run, error) {
	if req.PeerID == "" {
		return nil, fmt.Errorf("peer_id is required")
	}
	if len(req.Tasks) == 0 {
		return nil, fmt.Errorf("tasks must not be empty")
	}
	if req.WaitPolicy == "" {
		req.WaitPolicy = WaitAll
	}
	if req.Aggregation == "" {
		req.Aggregation = AggregateCollect
	}
	if req.Aggregation == AggregateReducer && o.agentRT == nil {
		return nil, fmt.Errorf("reducer aggregation requires an agent runtime")
	}

	runID := uuid.NewString()
	children := make([]ChildResult, len(req.Tasks))
	childRunIDs := make(map[string]int, len(req.Tasks))
	for i, t := range req.Tasks {
		label := t.Label
		if label == "" {
			label = fmt.Sprintf("child-%d", i)
		}
		children[i] = ChildResult{Label: label}
	}

	run := &Run{
		ID:               runID,
		PeerID:           req.PeerID,
		ParentSessionKey: req.ParentSessionKey,
		ParentRunID:      req.ParentRunID,
		Status:           RunStatusStarting,
		WaitPolicy:       req.WaitPolicy,
		Aggregation:      req.Aggregation,
		ReducerPrompt:    req.ReducerPrompt,
		MaxConcurrency:   req.MaxConcurrency,
		Children:         children,
		childRunIDs:      childRunIDs,
		CreatedAt:        time.Now().UTC(),
	}

	o.mu.Lock()
	o.runs[runID] = run
	o.mu.Unlock()

	var orchCtx context.Context
	var cancel context.CancelFunc
	if req.Timeout > 0 {
		orchCtx, cancel = context.WithTimeout(ctx, req.Timeout)
	} else {
		orchCtx, cancel = context.WithCancel(ctx)
	}
	o.mu.Lock()
	o.cancels[runID] = cancel
	o.mu.Unlock()

	if req.AnnounceStart && req.ParentSessionKey != "" {
		o.publishSession(req.PeerID, req.ParentSessionKey, runID, fmt.Sprintf(
			"[orchestrator:%s] starting fan-out: %d tasks, aggregation=%s",
			runID, len(req.Tasks), req.Aggregation,
		))
	}

	go o.execute(orchCtx, cancel, run, req)
	return run, nil
}

// Get returns a snapshot of a run by ID.
func (o *Orchestrator) Get(id string) (*Run, bool) {
	o.mu.Lock()
	r, ok := o.runs[id]
	o.mu.Unlock()
	if !ok {
		return nil, false
	}
	snap := r.snapshot()
	return &snap, true
}

// List returns snapshots of all runs ordered by creation time.
func (o *Orchestrator) List() []*Run {
	o.mu.Lock()
	all := make([]*Run, 0, len(o.runs))
	for _, r := range o.runs {
		all = append(all, r)
	}
	o.mu.Unlock()

	out := make([]*Run, 0, len(all))
	for _, r := range all {
		snap := r.snapshot()
		out = append(out, &snap)
	}
	// stable sort by creation time
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j].CreatedAt.Before(out[j-1].CreatedAt); j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}

// Cancel stops a running orchestration and kills all active children.
func (o *Orchestrator) Cancel(id string) error {
	o.mu.Lock()
	cancel, ok := o.cancels[id]
	o.mu.Unlock()
	if !ok {
		// Check if run exists but is already done.
		o.mu.Lock()
		_, exists := o.runs[id]
		o.mu.Unlock()
		if !exists {
			return fmt.Errorf("orchestration %s not found", id)
		}
		return fmt.Errorf("orchestration %s is not running", id)
	}
	cancel()
	return nil
}

// Wait blocks until the orchestration with the given ID finishes or ctx expires.
// Returns the final run snapshot.
func (o *Orchestrator) Wait(ctx context.Context, id string) (*Run, error) {
	for {
		o.mu.Lock()
		r, ok := o.runs[id]
		o.mu.Unlock()
		if !ok {
			return nil, fmt.Errorf("orchestration %s not found", id)
		}
		r.mu.Lock()
		st := r.Status
		r.mu.Unlock()
		switch st {
		case RunStatusCompleted, RunStatusErrored, RunStatusCancelled:
			snap := r.snapshot()
			return &snap, nil
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(300 * time.Millisecond):
			// poll
		}
	}
}

// execute is the goroutine that drives the orchestration lifecycle.
func (o *Orchestrator) execute(ctx context.Context, cancel context.CancelFunc, run *Run, req FanOutRequest) {
	defer cancel()

	run.mu.Lock()
	run.Status = RunStatusRunning
	run.StartedAt = time.Now().UTC()
	run.mu.Unlock()

	o.publishLifecycle(run.ID, req.PeerID, req.ParentSessionKey, "orchestrator.started", map[string]any{
		"tasks": len(req.Tasks),
	})

	// Semaphore for max_concurrency (0 = unlimited).
	sem := make(chan struct{}, maxConcurrency(req.MaxConcurrency, len(req.Tasks)))

	// completionCh receives child indices as they finish.
	completionCh := make(chan int, len(req.Tasks))
	var wg sync.WaitGroup

	for i, task := range req.Tasks {
		i, task := i, task
		// Acquire semaphore slot before checking wait policy so we don't
		// launch a child that will immediately be abandoned.
		select {
		case sem <- struct{}{}:
		case <-ctx.Done():
			goto drain
		}

		// For early-exit policies, stop launching once the threshold is met.
		if o.waitPolicySatisfied(run, req.WaitPolicy, len(req.Tasks)) {
			<-sem // return the slot we just acquired
			cancel()
			goto drain
		}

		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			defer func() { completionCh <- i }()

			o.runChild(ctx, run, req, i, task)
		}()
	}

	// Wait for all launched goroutines to finish.
	go func() {
		wg.Wait()
		close(completionCh)
	}()

drain:
	// policyEarlyExit is set when the orchestration ends due to its wait policy
	// being satisfied rather than external cancellation. We still call cancel()
	// to stop any lingering children, but we proceed to aggregation rather than
	// treating the run as cancelled.
	policyEarlyExit := false

	for range completionCh {
		// For non-all policies, signal that we're done once the threshold is met.
		if req.WaitPolicy != WaitAll && o.waitPolicySatisfied(run, req.WaitPolicy, len(req.Tasks)) {
			policyEarlyExit = true
			cancel() // stop lingering children; remaining goroutines will drain
		}
	}

	// If all children finished naturally the policy is also satisfied.
	if o.waitPolicySatisfied(run, req.WaitPolicy, len(req.Tasks)) {
		policyEarlyExit = true
	}

	// Collect context cancellation: kill any still-active children.
	if ctx.Err() != nil {
		o.killActiveChildren(run)
	}

	// Only treat the run as externally cancelled when the context was cancelled
	// and the wait policy was NOT satisfied by normal child completion.
	if ctx.Err() != nil && !policyEarlyExit {
		o.markCancelled(run)
		o.mu.Lock()
		delete(o.cancels, run.ID)
		o.mu.Unlock()
		slog.Info("orchestration cancelled", "id", run.ID)
		o.publishLifecycle(run.ID, req.PeerID, req.ParentSessionKey, "orchestrator.cancelled", nil)
		if req.ParentSessionKey != "" {
			o.publishSession(req.PeerID, req.ParentSessionKey, run.ID,
				fmt.Sprintf("[orchestrator:%s] cancelled", run.ID))
		}
		return
	}

	// Aggregate results. Use a fresh context so a policy-triggered cancel on the
	// orchestration context does not abort the aggregation LLM pass.
	run.mu.Lock()
	run.Status = RunStatusAggregating
	run.mu.Unlock()

	aggCtx := context.Background()
	if req.Timeout > 0 {
		var aggCancel context.CancelFunc
		aggCtx, aggCancel = context.WithTimeout(aggCtx, req.Timeout/4+5*time.Second)
		defer aggCancel()
	}
	aggregated, aggErr := o.aggregate(aggCtx, run, req)

	now := time.Now().UTC()
	run.mu.Lock()
	run.FinishedAt = now
	if aggErr != nil {
		run.Status = RunStatusErrored
		run.Error = redact.Error(aggErr)
	} else {
		run.Status = RunStatusCompleted
		run.AggregatedReply = aggregated
	}
	run.mu.Unlock()

	o.mu.Lock()
	delete(o.cancels, run.ID)
	o.mu.Unlock()

	if aggErr != nil {
		slog.Warn("orchestration aggregation failed", "id", run.ID, "error", aggErr)
		o.publishLifecycle(run.ID, req.PeerID, req.ParentSessionKey, "orchestrator.errored", map[string]any{
			"error": redact.Error(aggErr),
		})
		return
	}

	slog.Info("orchestration completed", "id", run.ID, "children", len(run.Children))
	o.publishLifecycle(run.ID, req.PeerID, req.ParentSessionKey, "orchestrator.completed", map[string]any{
		"children": len(run.Children),
	})
	if req.ParentSessionKey != "" && aggregated != "" {
		o.publishSession(req.PeerID, req.ParentSessionKey, run.ID,
			fmt.Sprintf("[orchestrator:%s] %s", run.ID, aggregated))
	}
}

// runChild spawns one child and waits for it to finish (blocking within the
// calling goroutine).
func (o *Orchestrator) runChild(ctx context.Context, run *Run, req FanOutRequest, idx int, task ChildTask) {
	peerID := task.PeerID
	if peerID == "" {
		peerID = req.PeerID
	}
	model := task.Model
	if model == "" {
		model = req.Model
	}
	timeout := task.Timeout
	if timeout <= 0 {
		timeout = req.ChildTimeout
	}

	spawnReq := subagent.SpawnRequest{
		PeerID:           peerID,
		ParentID:         req.PeerID,
		ParentRunID:      req.ParentRunID,
		ParentSessionKey: req.ParentSessionKey,
		SourceSessionKey: req.ParentSessionKey,
		Task:             task.Task,
		Model:            model,
		Timeout:          timeout,
		Role:             subagent.RoleLeaf,
		Control:          subagent.ControlNone,
		// Suppress per-child announcements; the orchestrator owns messaging.
		AnnounceSkip: true,
		// We deliver the aggregate result ourselves, not per-child reply-backs.
		ReplyBack: false,
		ReplySkip: true,
	}

	rec, err := o.subRuntime.Spawn(ctx, spawnReq)
	if err != nil {
		run.mu.Lock()
		run.Children[idx].Status = subagent.StatusErrored
		run.Children[idx].Error = redact.Error(err)
		run.mu.Unlock()
		o.publishChildProgress(run.ID, req.PeerID, req.ParentSessionKey, idx, run.Children[idx].Label, "errored")
		slog.Warn("orchestrator: child spawn failed", "orchestration", run.ID, "idx", idx, "error", err)
		return
	}

	// Register run ID for fast lookup.
	run.mu.Lock()
	run.Children[idx].RunID = rec.ID
	run.Children[idx].SessionKey = rec.SessionKey
	run.Children[idx].StartedAt = rec.CreatedAt
	run.childRunIDs[rec.ID] = idx
	run.mu.Unlock()

	o.publishChildProgress(run.ID, req.PeerID, req.ParentSessionKey, idx, run.Children[idx].Label, "queued")

	// Poll until the child finishes or our context is cancelled.
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(500 * time.Millisecond):
		}

		current, ok := o.subRuntime.Get(rec.ID)
		if !ok {
			break
		}
		switch current.Status {
		case subagent.StatusCompleted, subagent.StatusErrored, subagent.StatusKilled:
			run.mu.Lock()
			run.Children[idx].Status = current.Status
			run.Children[idx].FinalReply = redact.String(current.FinalReply)
			run.Children[idx].Error = current.Error
			run.Children[idx].Steps = current.SubTurn.Steps
			run.Children[idx].ToolCalls = current.SubTurn.ToolCalls
			run.Children[idx].FinishedAt = current.FinishedAt
			run.mu.Unlock()
			o.publishChildProgress(run.ID, req.PeerID, req.ParentSessionKey, idx, run.Children[idx].Label, string(current.Status))
			return
		}
	}
}

// killActiveChildren cancels any child run that is still queued or running.
func (o *Orchestrator) killActiveChildren(run *Run) {
	run.mu.Lock()
	active := make([]string, 0, len(run.Children))
	for _, c := range run.Children {
		if c.RunID == "" {
			continue
		}
		if c.Status == subagent.StatusQueued || c.Status == subagent.StatusRunning ||
			c.Status == "" /* not yet set */ {
			active = append(active, c.RunID)
		}
	}
	run.mu.Unlock()

	for _, id := range active {
		if err := o.subRuntime.Kill(id); err != nil {
			slog.Debug("orchestrator: kill child", "run", id, "err", err)
		}
	}
}

func (o *Orchestrator) markCancelled(run *Run) {
	now := time.Now().UTC()
	run.mu.Lock()
	defer run.mu.Unlock()
	run.Status = RunStatusCancelled
	run.FinishedAt = now
	// Mark any children that never finished as killed.
	for i := range run.Children {
		if run.Children[i].Status == "" ||
			run.Children[i].Status == subagent.StatusQueued ||
			run.Children[i].Status == subagent.StatusRunning {
			run.Children[i].Status = subagent.StatusKilled
		}
	}
}

// waitPolicySatisfied returns true when the configured wait policy threshold
// has been met.
func (o *Orchestrator) waitPolicySatisfied(run *Run, policy WaitPolicy, total int) bool {
	if total == 0 {
		return true
	}
	run.mu.Lock()
	defer run.mu.Unlock()

	completed := 0
	for _, c := range run.Children {
		if c.Status == subagent.StatusCompleted {
			completed++
		}
	}
	switch policy {
	case WaitFirst:
		return completed >= 1
	case WaitQuorum:
		return completed >= (total/2 + 1)
	default: // WaitAll
		finished := 0
		for _, c := range run.Children {
			switch c.Status {
			case subagent.StatusCompleted, subagent.StatusErrored, subagent.StatusKilled:
				finished++
			}
		}
		return finished >= total
	}
}

// aggregate assembles the final reply from child results.
func (o *Orchestrator) aggregate(ctx context.Context, run *Run, req FanOutRequest) (string, error) {
	run.mu.Lock()
	children := append([]ChildResult(nil), run.Children...)
	run.mu.Unlock()

	switch req.Aggregation {
	case AggregateCollect:
		return aggregateCollect(children), nil
	case AggregateConcat:
		return aggregateConcat(children), nil
	case AggregateReducer:
		return o.aggregateReducer(ctx, run, req, children)
	default:
		return aggregateCollect(children), nil
	}
}

func aggregateCollect(children []ChildResult) string {
	var parts []string
	for _, c := range children {
		if c.FinalReply != "" {
			parts = append(parts, fmt.Sprintf("[%s]: %s", c.Label, c.FinalReply))
		} else if c.Error != "" {
			parts = append(parts, fmt.Sprintf("[%s]: error: %s", c.Label, c.Error))
		}
	}
	return strings.Join(parts, "\n\n")
}

func aggregateConcat(children []ChildResult) string {
	var sb strings.Builder
	for i, c := range children {
		if i > 0 {
			sb.WriteString("\n\n---\n\n")
		}
		sb.WriteString("## ")
		sb.WriteString(c.Label)
		sb.WriteString("\n\n")
		if c.FinalReply != "" {
			sb.WriteString(c.FinalReply)
		} else if c.Error != "" {
			sb.WriteString("Error: ")
			sb.WriteString(c.Error)
		} else {
			sb.WriteString("(no output)")
		}
	}
	return sb.String()
}

// aggregateReducer runs a final LLM agent pass over all child results.
// The raw labeled child outputs are preserved in run.Children; the synthesized
// reply is returned as the aggregated result.
func (o *Orchestrator) aggregateReducer(ctx context.Context, run *Run, req FanOutRequest, children []ChildResult) (string, error) {
	if o.agentRT == nil {
		return aggregateConcat(children), nil
	}

	prompt := req.ReducerPrompt
	if prompt == "" {
		prompt = "Synthesize the following agent results into a single coherent reply. Preserve key findings from each contributor."
	}
	labeled := aggregateConcat(children)
	task := fmt.Sprintf("%s\n\nChild results:\n\n%s", prompt, labeled)

	sessionKey := fmt.Sprintf("%s::orchestrator-reducer::%s", req.PeerID, run.ID)
	result, err := o.agentRT.Run(ctx, agent.RunRequest{
		PeerID:     req.PeerID,
		Scope:      agent.ScopeIsolated,
		SessionKey: sessionKey,
		Messages: []types.Message{
			{Role: "user", Content: task},
		},
		Model: req.Model,
	})
	if err != nil {
		// Fall back to concat on reducer failure so we don't lose child outputs.
		slog.Warn("orchestrator reducer failed, falling back to concat", "id", run.ID, "error", err)
		return aggregateConcat(children), fmt.Errorf("reducer: %w", err)
	}
	return redact.String(result.AssistantText), nil
}

// maxConcurrency returns a channel capacity for the semaphore. When limit is
// 0 or negative, total is used so there is effectively no artificial cap.
func maxConcurrency(limit, total int) int {
	if limit > 0 && limit < total {
		return limit
	}
	if total <= 0 {
		return 1
	}
	return total
}

// ── EventBus helpers ─────────────────────────────────────────────────────────

func (o *Orchestrator) publishLifecycle(id, peerID, sessionKey, kind string, data map[string]any) {
	if o.bus == nil {
		return
	}
	o.bus.Publish(eventbus.Event{
		Kind:       kind,
		PeerID:     peerID,
		SessionKey: sessionKey,
		Source:     "orchestrator",
		RunID:      id,
		Data:       data,
	})
}

func (o *Orchestrator) publishChildProgress(orchID, peerID, sessionKey string, idx int, label, status string) {
	o.publishLifecycle(orchID, peerID, sessionKey, "orchestrator.child.progress", map[string]any{
		"child_index":  idx,
		"child_label":  label,
		"child_status": status,
	})
}

func (o *Orchestrator) publishSession(peerID, sessionKey, runID, content string) {
	if o.bus == nil {
		return
	}
	msg := types.Message{Role: "assistant", Content: content}
	o.bus.Publish(eventbus.Event{
		Kind:       "session.message",
		PeerID:     peerID,
		SessionKey: sessionKey,
		Source:     "orchestrator",
		RunID:      runID,
		Message:    &msg,
	})
}
