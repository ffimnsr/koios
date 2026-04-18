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
	"encoding/json"
	"fmt"
	"log/slog"
	"math"
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
	// AggregateVote clusters child replies by semantic similarity and elects a
	// winner via heuristic cosine voting; an LLM tie-breaker is invoked only
	// when no cluster achieves a strict majority.
	AggregateVote AggregationMode = "vote"
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

// TimelineEvent is one structured event recorded during an orchestration run.
type TimelineEvent struct {
	At       time.Time      `json:"at"`
	Kind     string         `json:"kind"`
	Label    string         `json:"label,omitempty"`
	ChildIdx int            `json:"child_idx,omitempty"`
	Data     map[string]any `json:"data,omitempty"`
}

// AggregationProvenance captures metadata about a completed aggregation pass.
type AggregationProvenance struct {
	Mode              AggregationMode `json:"mode"`
	ReducerSessionKey string          `json:"reducer_session_key,omitempty"`
	InputLength       int             `json:"input_length"`
	OutputLength      int             `json:"output_length"`
	DurationMs        int64           `json:"duration_ms"`
}

// Budget constrains resource usage of an orchestration run.
// Fields with zero values impose no limit.
type Budget struct {
	// MaxSteps is the maximum cumulative LLM steps across all children.
	MaxSteps int `json:"max_steps,omitempty"`
	// MaxChildWallClock is the per-child execution timeout. When set and
	// neither ChildTask.Timeout nor FanOutRequest.ChildTimeout is set, this
	// applies to every child.
	MaxChildWallClock time.Duration `json:"max_child_wall_clock,omitempty"`
}

// VoteConfig controls heuristic clustering and LLM tie-breaking for
// AggregateVote mode. Zero values use safe defaults.
type VoteConfig struct {
	// AgreementThreshold is the minimum cosine-similarity score (0–1) for two
	// replies to be considered in the same cluster. Default 0.5.
	AgreementThreshold float64 `json:"agreement_threshold,omitempty"`
	// DisagreementReport, when true, appends a summary of dissenting clusters
	// to the aggregated reply.
	DisagreementReport bool `json:"disagreement_report,omitempty"`
	// TiebreakerModel overrides the model used for the LLM tie-breaker call.
	// Falls back to FanOutRequest.Model when empty.
	TiebreakerModel string `json:"tiebreaker_model,omitempty"`
}

// SchemaValidationMode controls how output schema violations are handled.
type SchemaValidationMode string

const (
	// SchemaWarn records violations but does not mark the child as failed.
	SchemaWarn SchemaValidationMode = "warn"
	// SchemaStrict marks the child as errored when its reply violates the schema.
	SchemaStrict SchemaValidationMode = "strict"
)

// ChildRole classifies a child's purpose within the orchestration.
type ChildRole string

const (
	RoleWorker   ChildRole = "worker"
	RoleVerifier ChildRole = "verifier"
	RoleArbiter  ChildRole = "arbiter"
)

// StageSpec describes one stage in a multi-stage orchestration.
type StageSpec struct {
	// Tasks are the child tasks for this stage.
	Tasks []ChildTask `json:"tasks"`
	// Aggregation controls how child replies are combined at the end of the stage.
	Aggregation AggregationMode `json:"aggregation,omitempty"`
	// ReducerPrompt is used when Aggregation is AggregateReducer.
	ReducerPrompt string `json:"reducer_prompt,omitempty"`
	// WaitPolicy controls the completion criterion for this stage.
	WaitPolicy WaitPolicy `json:"wait_policy,omitempty"`
}

// StageResult holds the outcome of one completed stage.
type StageResult struct {
	StageIndex      int           `json:"stage_index"`
	Children        []ChildResult `json:"children"`
	AggregatedReply string        `json:"aggregated_reply"`
}

// RetryPolicy controls re-execution of a failed child.
type RetryPolicy struct {
	// MaxAttempts is the total number of tries (1 = no retry). Default 1.
	MaxAttempts int `json:"max_attempts,omitempty"`
	// BackoffBase is the sleep duration before the first retry. Doubles on
	// each subsequent attempt. Default 0 (no sleep).
	BackoffBase time.Duration `json:"backoff_base,omitempty"`
}

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
	// OutputSchema is an optional JSON object schema (as a JSON string).
	// When set, the child's FinalReply is parsed as JSON and validated against
	// the required keys declared in the schema's "required" array.
	OutputSchema string `json:"output_schema,omitempty"`
	// DependsOn lists labels of tasks that must complete before this task starts.
	// An empty list means the task can start immediately (default fan-out).
	DependsOn []string `json:"depends_on,omitempty"`
	// Role classifies the task's purpose (worker, verifier, arbiter).
	Role ChildRole `json:"role,omitempty"`
	// Retry controls re-execution when the child fails. Zero value = no retry.
	Retry RetryPolicy `json:"retry,omitempty"`
	// HedgeCount, when > 1, launches that many parallel copies of this task
	// and keeps the first successful reply, cancelling the rest.
	HedgeCount int `json:"hedge_count,omitempty"`
	// FallbackTask is executed if all primary attempts (including retries) fail.
	FallbackTask *ChildTask `json:"fallback_task,omitempty"`
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
	// Attempts records how many times this child was tried (including hedges).
	Attempts   int       `json:"attempts,omitempty"`
	StartedAt  time.Time `json:"started_at,omitempty"`
	FinishedAt time.Time `json:"finished_at,omitempty"`
	// SchemaViolations lists fields that were missing or invalid when
	// OutputSchema was set on the task.
	SchemaViolations []string `json:"schema_violations,omitempty"`
	// OutputJSON is the parsed JSON from FinalReply when OutputSchema was set.
	OutputJSON []byte `json:"output_json,omitempty"`
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
	// VerifierTask, when set, automatically creates a second stage where one
	// verifier instance per worker reviews each worker's output. The verifier
	// task prompt receives the prior worker's FinalReply appended.
	VerifierTask *ChildTask `json:"verifier_task,omitempty"`
	// ArbiterTask, when set, adds a third stage with a single arbiter agent
	// that reviews all worker + verifier results.
	ArbiterTask *ChildTask `json:"arbiter_task,omitempty"`
	// Stages, when set, overrides Tasks and runs a multi-stage orchestration.
	// Each stage is a full fan-out with its own tasks, aggregation, and wait
	// policy. Prior stage output is injected into each next-stage task prompt.
	Stages []StageSpec `json:"stages,omitempty"`
	// StreamPartials, when true, sends each child's final reply to ParentSessionKey
	// as soon as it finishes, before the full aggregation is delivered.
	StreamPartials bool `json:"stream_partials,omitempty"`
	// Retry controls re-execution when children fail. Task-level Retry
	// overrides this.
	Retry RetryPolicy `json:"retry,omitempty"`
	// Budget constrains resource usage. Zero values impose no limit.
	Budget Budget `json:"budget,omitempty"`
	// BarrierGroups maps group names to child labels. The Barrier() method
	// blocks until all children in the named group have reached a terminal state.
	BarrierGroups map[string][]string `json:"barrier_groups,omitempty"`
	// VoteConfig controls the AggregateVote aggregation mode.
	VoteConfig VoteConfig `json:"vote_config,omitempty"`
	// SchemaValidationMode controls how output-schema violations are handled.
	// Defaults to SchemaWarn.
	SchemaValidationMode SchemaValidationMode `json:"schema_validation_mode,omitempty"`
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
	// BarrierGroups mirrors FanOutRequest.BarrierGroups for use by Barrier().
	BarrierGroups map[string][]string `json:"barrier_groups,omitempty"`

	AggregatedReply string `json:"aggregated_reply,omitempty"`
	Error           string `json:"error,omitempty"`

	CreatedAt  time.Time `json:"created_at"`
	StartedAt  time.Time `json:"started_at,omitempty"`
	FinishedAt time.Time `json:"finished_at,omitempty"`

	// Timeline records significant events during the orchestration run.
	Timeline []TimelineEvent `json:"timeline,omitempty"`
	// Provenance captures metadata about the aggregation pass.
	Provenance *AggregationProvenance `json:"provenance,omitempty"`
	// StageResults captures per-stage outcomes for multi-stage runs.
	StageResults []StageResult `json:"stage_results,omitempty"`
}

// snapshot returns a copy of the run safe for external use.
func (r *Run) snapshot() Run {
	r.mu.Lock()
	defer r.mu.Unlock()
	cp := *r
	cp.Children = append([]ChildResult(nil), r.Children...)
	cp.Timeline = append([]TimelineEvent(nil), r.Timeline...)
	cp.childRunIDs = nil
	return cp
}

// appendTimeline appends a timeline event. Safe to call concurrently.
func (r *Run) appendTimeline(kind, label string, idx int, data map[string]any) {
	r.mu.Lock()
	r.Timeline = append(r.Timeline, TimelineEvent{
		At:       time.Now().UTC(),
		Kind:     kind,
		Label:    label,
		ChildIdx: idx,
		Data:     data,
	})
	r.mu.Unlock()
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

	isMultiStage := len(req.Stages) > 0 || req.VerifierTask != nil || req.ArbiterTask != nil
	if len(req.Tasks) == 0 && !isMultiStage {
		return nil, fmt.Errorf("tasks must not be empty")
	}
	if len(req.Stages) > 0 {
		for i, s := range req.Stages {
			if len(s.Tasks) == 0 {
				return nil, fmt.Errorf("stage %d has no tasks", i)
			}
		}
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

	// Validate DependsOn labels and detect cycles in single-stage tasks.
	if len(req.Tasks) > 0 {
		labelSet := make(map[string]bool, len(req.Tasks))
		for i, t := range req.Tasks {
			lbl := t.Label
			if lbl == "" {
				lbl = fmt.Sprintf("child-%d", i)
			}
			if labelSet[lbl] {
				return nil, fmt.Errorf("duplicate task label %q", lbl)
			}
			labelSet[lbl] = true
		}
		labelToIdx := make(map[string]int, len(req.Tasks))
		for i, t := range req.Tasks {
			lbl := t.Label
			if lbl == "" {
				lbl = fmt.Sprintf("child-%d", i)
			}
			labelToIdx[lbl] = i
		}
		for _, t := range req.Tasks {
			for _, dep := range t.DependsOn {
				if !labelSet[dep] {
					return nil, fmt.Errorf("task %q depends on unknown label %q", t.Label, dep)
				}
			}
		}
		// Build adjacency (i depends on j → adj[i] contains j) and check for cycles.
		adj := make([][]int, len(req.Tasks))
		for i, t := range req.Tasks {
			for _, dep := range t.DependsOn {
				if j, ok := labelToIdx[dep]; ok {
					adj[i] = append(adj[i], j)
				}
			}
		}
		if detectCycle(len(req.Tasks), adj) {
			return nil, fmt.Errorf("dependency cycle detected in tasks")
		}
	}

	runID := uuid.NewString()
	childRunIDs := make(map[string]int, len(req.Tasks))

	// Multi-stage runs (Stages, VerifierTask, ArbiterTask) allocate children
	// dynamically in executeMultiStage. Single-stage runs pre-allocate here.
	var children []ChildResult
	if !isMultiStage {
		children = make([]ChildResult, len(req.Tasks))
		for i, t := range req.Tasks {
			label := t.Label
			if label == "" {
				label = fmt.Sprintf("child-%d", i)
			}
			children[i] = ChildResult{Label: label}
		}
	}

	// Store BarrierGroups on the run for use by Barrier().
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
		BarrierGroups:    req.BarrierGroups,
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
		taskCount := len(req.Tasks)
		if taskCount == 0 {
			for _, s := range req.Stages {
				taskCount += len(s.Tasks)
			}
		}
		o.publishSession(req.PeerID, req.ParentSessionKey, runID, fmt.Sprintf(
			"[orchestrator:%s] starting fan-out: %d tasks, aggregation=%s",
			runID, taskCount, req.Aggregation,
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

	taskCount := len(req.Tasks)
	if taskCount == 0 {
		for _, s := range req.Stages {
			taskCount += len(s.Tasks)
		}
	}
	run.appendTimeline("orchestration.started", "", 0, map[string]any{"tasks": taskCount})
	o.publishLifecycle(run.ID, req.PeerID, req.ParentSessionKey, "orchestrator.started", map[string]any{
		"tasks": taskCount,
	})

	// Multi-stage or verifier/arbiter mode: delegate to executeMultiStage.
	if len(req.Stages) > 0 || req.VerifierTask != nil || req.ArbiterTask != nil {
		o.executeMultiStage(ctx, cancel, run, req)
		return
	}

	// Single-stage DAG fan-out.
	policyEarlyExit, budgetExceeded := o.runDAGWave(ctx, cancel, run, req, req.Tasks, 0)

	// Kill any still-active children when the context was cancelled.
	if ctx.Err() != nil {
		o.killActiveChildren(run)
	}

	// Budget exceeded: mark as errored.
	if budgetExceeded {
		now := time.Now().UTC()
		run.mu.Lock()
		run.Status = RunStatusErrored
		run.FinishedAt = now
		run.Error = "budget exceeded: max_steps reached"
		run.mu.Unlock()
		o.mu.Lock()
		delete(o.cancels, run.ID)
		o.mu.Unlock()
		run.appendTimeline("orchestration.errored", "", 0, map[string]any{"reason": "budget_exceeded"})
		slog.Warn("orchestration budget exceeded", "id", run.ID)
		o.publishLifecycle(run.ID, req.PeerID, req.ParentSessionKey, "orchestrator.errored", map[string]any{
			"error": "budget exceeded: max_steps reached",
		})
		return
	}

	// Externally cancelled and policy was not satisfied by child completion.
	if ctx.Err() != nil && !policyEarlyExit {
		o.markCancelled(run)
		o.mu.Lock()
		delete(o.cancels, run.ID)
		o.mu.Unlock()
		run.appendTimeline("orchestration.cancelled", "", 0, nil)
		slog.Info("orchestration cancelled", "id", run.ID)
		o.publishLifecycle(run.ID, req.PeerID, req.ParentSessionKey, "orchestrator.cancelled", nil)
		if req.ParentSessionKey != "" {
			o.publishSession(req.PeerID, req.ParentSessionKey, run.ID,
				fmt.Sprintf("[orchestrator:%s] cancelled", run.ID))
		}
		return
	}

	// Aggregate and finalise.
	run.mu.Lock()
	children := append([]ChildResult(nil), run.Children...)
	run.mu.Unlock()
	o.finishRun(ctx, cancel, run, req, children)
}

// runChild spawns one child and waits for it to finish (blocking within the
// calling goroutine).
// childOutcome is the result returned by spawnAndPoll for a single attempt.
type childOutcome struct {
	runID      string
	sessionKey string
	status     subagent.Status
	finalReply string
	err        string
	steps      int
	toolCalls  int
	startedAt  time.Time
	finishedAt time.Time
}

// spawnAndPoll launches one subagent run and blocks until it reaches a
// terminal state or ctx is cancelled. It registers the spawn run ID in
// run.childRunIDs so killActiveChildren can reach it during cancellation.
func (o *Orchestrator) spawnAndPoll(ctx context.Context, run *Run, req FanOutRequest, idx int, task ChildTask) childOutcome {
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
	if timeout <= 0 {
		timeout = req.Budget.MaxChildWallClock
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
		AnnounceSkip:     true,
		ReplyBack:        false,
		ReplySkip:        true,
	}

	rec, err := o.subRuntime.Spawn(ctx, spawnReq)
	if err != nil {
		slog.Warn("orchestrator: child spawn failed", "orchestration", run.ID, "idx", idx, "error", err)
		return childOutcome{status: subagent.StatusErrored, err: redact.Error(err)}
	}

	// Register for killActiveChildren reachability.
	run.mu.Lock()
	run.childRunIDs[rec.ID] = idx
	run.mu.Unlock()

	run.appendTimeline("child.queued", run.Children[idx].Label, idx, map[string]any{"run_id": rec.ID})

	for {
		select {
		case <-ctx.Done():
			return childOutcome{
				runID:      rec.ID,
				sessionKey: rec.SessionKey,
				startedAt:  rec.CreatedAt,
				status:     subagent.StatusKilled,
				err:        "context cancelled",
			}
		case <-time.After(500 * time.Millisecond):
		}
		current, ok := o.subRuntime.Get(rec.ID)
		if !ok {
			break
		}
		switch current.Status {
		case subagent.StatusCompleted, subagent.StatusErrored, subagent.StatusKilled:
			return childOutcome{
				runID:      rec.ID,
				sessionKey: rec.SessionKey,
				startedAt:  rec.CreatedAt,
				status:     current.Status,
				finalReply: redact.String(current.FinalReply),
				err:        current.Error,
				steps:      current.SubTurn.Steps,
				toolCalls:  current.SubTurn.ToolCalls,
				finishedAt: current.FinishedAt,
			}
		}
	}
	// Unreachable but satisfies compiler.
	return childOutcome{status: subagent.StatusErrored, err: "poll loop exited unexpectedly"}
}

// runChild executes one child slot with support for retry, hedging, and fallback.
func (o *Orchestrator) runChild(ctx context.Context, run *Run, req FanOutRequest, idx int, task ChildTask) {
	run.mu.Lock()
	childLabel := run.Children[idx].Label
	run.mu.Unlock()

	// Resolve retry policy: task overrides run-level.
	retry := task.Retry
	if retry.MaxAttempts <= 0 {
		retry = req.Retry
	}
	if retry.MaxAttempts <= 0 {
		retry.MaxAttempts = 1
	}

	hedge := task.HedgeCount
	if hedge < 1 {
		hedge = 1
	}

	var (
		lastOutcome childOutcome
		succeeded   bool
		attempts    int
	)

	for attempt := 0; attempt < retry.MaxAttempts && !succeeded; attempt++ {
		// Exponential backoff between retries.
		if attempt > 0 && retry.BackoffBase > 0 {
			backoff := retry.BackoffBase * (1 << uint(attempt-1))
			select {
			case <-ctx.Done():
				goto done
			case <-time.After(backoff):
			}
		}

		if hedge > 1 {
			// Hedging: launch HedgeCount parallel copies, keep first success.
			lastOutcome, succeeded = o.runChildHedged(ctx, run, req, idx, task, hedge)
			attempts += hedge
		} else {
			o.publishChildProgress(run.ID, req.PeerID, req.ParentSessionKey, idx, childLabel, "queued")
			lastOutcome = o.spawnAndPoll(ctx, run, req, idx, task)
			attempts++
			succeeded = lastOutcome.status == subagent.StatusCompleted
		}
	}

	// Fallback: run alternative task if all primary attempts failed.
	if !succeeded && task.FallbackTask != nil {
		slog.Info("orchestrator: running fallback task", "orchestration", run.ID, "idx", idx)
		o.publishChildProgress(run.ID, req.PeerID, req.ParentSessionKey, idx, childLabel, "fallback")
		lastOutcome = o.spawnAndPoll(ctx, run, req, idx, *task.FallbackTask)
		attempts++
		succeeded = lastOutcome.status == subagent.StatusCompleted
	}

done:
	// Commit outcome to the child slot.
	run.mu.Lock()
	run.Children[idx].RunID = lastOutcome.runID
	run.Children[idx].SessionKey = lastOutcome.sessionKey
	run.Children[idx].Status = lastOutcome.status
	run.Children[idx].FinalReply = lastOutcome.finalReply
	run.Children[idx].Error = lastOutcome.err
	run.Children[idx].Steps = lastOutcome.steps
	run.Children[idx].ToolCalls = lastOutcome.toolCalls
	run.Children[idx].Attempts = attempts
	if !lastOutcome.startedAt.IsZero() {
		run.Children[idx].StartedAt = lastOutcome.startedAt
	}
	if !lastOutcome.finishedAt.IsZero() {
		run.Children[idx].FinishedAt = lastOutcome.finishedAt
	}
	childReply := run.Children[idx].FinalReply
	run.mu.Unlock()

	o.publishChildProgress(run.ID, req.PeerID, req.ParentSessionKey, idx, childLabel, string(lastOutcome.status))

	// Stream partial reply to parent session if requested.
	if req.StreamPartials && req.ParentSessionKey != "" && childReply != "" {
		o.publishSession(req.PeerID, req.ParentSessionKey, run.ID,
			fmt.Sprintf("[orchestrator:%s child %s] %s", run.ID, childLabel, childReply))
	}

	// Validate output schema if requested.
	if task.OutputSchema != "" && childReply != "" {
		violations := validateOutputSchema(childReply, task.OutputSchema)
		if len(violations) > 0 {
			run.mu.Lock()
			run.Children[idx].SchemaViolations = violations
			if req.SchemaValidationMode == SchemaStrict {
				run.Children[idx].Status = subagent.StatusErrored
				run.Children[idx].Error = fmt.Sprintf("schema violation: %s", strings.Join(violations, "; "))
			}
			run.mu.Unlock()
		} else {
			run.mu.Lock()
			run.Children[idx].OutputJSON = []byte(childReply)
			run.mu.Unlock()
		}
	}

	run.appendTimeline("child.finished", childLabel, idx, map[string]any{
		"status":   string(lastOutcome.status),
		"steps":    lastOutcome.steps,
		"attempts": attempts,
	})
}

// runChildHedged launches count parallel copies of task and returns the first
// successful outcome, cancelling all other copies. Returns (outcome, success).
func (o *Orchestrator) runChildHedged(ctx context.Context, run *Run, req FanOutRequest, idx int, task ChildTask, count int) (childOutcome, bool) {
	hedgeCtx, hedgeCancel := context.WithCancel(ctx)
	defer hedgeCancel()

	type result struct {
		outcome childOutcome
		ok      bool
	}
	ch := make(chan result, count)

	for i := 0; i < count; i++ {
		go func() {
			out := o.spawnAndPoll(hedgeCtx, run, req, idx, task)
			ch <- result{out, out.status == subagent.StatusCompleted}
		}()
	}

	var winner childOutcome
	var winnerFound bool
	received := 0
	for r := range ch {
		received++
		if r.ok && !winnerFound {
			winner = r.outcome
			winnerFound = true
			hedgeCancel() // stop remaining hedges
		}
		if received == count {
			break
		}
	}
	if !winnerFound {
		// All failed — return last outcome.
		winner, _ = func() (childOutcome, bool) {
			for r := range ch {
				return r.outcome, false
			}
			return childOutcome{status: subagent.StatusErrored, err: "all hedges failed"}, false
		}()
	}
	return winner, winnerFound
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

// detectCycle returns true when the directed graph adj contains a cycle.
// adj[i] is the list of node indices that node i has edges to.
func detectCycle(n int, adj [][]int) bool {
	const (
		white = 0
		gray  = 1
		black = 2
	)
	color := make([]int, n)
	var dfs func(v int) bool
	dfs = func(v int) bool {
		color[v] = gray
		for _, w := range adj[v] {
			if color[w] == gray {
				return true
			}
			if color[w] == white && dfs(w) {
				return true
			}
		}
		color[v] = black
		return false
	}
	for i := 0; i < n; i++ {
		if color[i] == white && dfs(i) {
			return true
		}
	}
	return false
}

// stageWaitSatisfied returns true when the configured wait policy threshold
// has been met for the given slice of children.
func stageWaitSatisfied(children []ChildResult, policy WaitPolicy) bool {
	total := len(children)
	if total == 0 {
		return true
	}
	completed := 0
	finished := 0
	for _, c := range children {
		if c.Status == subagent.StatusCompleted {
			completed++
		}
		switch c.Status {
		case subagent.StatusCompleted, subagent.StatusErrored, subagent.StatusKilled:
			finished++
		}
	}
	switch policy {
	case WaitFirst:
		return completed >= 1
	case WaitQuorum:
		return completed >= (total/2 + 1)
	default: // WaitAll
		return finished >= total
	}
}

// runDAGWave fans out tasks[0:len(tasks)] starting at run.Children[offset],
// respecting DependsOn ordering within the wave. It blocks until the wave
// completes or ctx is cancelled. Returns (policyEarlyExit, budgetExceeded).
func (o *Orchestrator) runDAGWave(
	ctx context.Context, cancel context.CancelFunc,
	run *Run, req FanOutRequest,
	tasks []ChildTask, offset int,
) (policyEarlyExit, budgetExceeded bool) {
	if len(tasks) == 0 {
		return false, false
	}

	// Build label→local-index map for this wave.
	labelToLocal := make(map[string]int, len(tasks))
	for i := range tasks {
		labelToLocal[run.Children[offset+i].Label] = i
	}

	// Build reverse-dependency graph and initial unmet-dep counters.
	depOf := make([][]int, len(tasks))
	unmetDeps := make([]int, len(tasks))
	for i, t := range tasks {
		for _, dep := range t.DependsOn {
			if j, ok := labelToLocal[dep]; ok {
				depOf[j] = append(depOf[j], i)
				unmetDeps[i]++
			}
		}
	}

	// Seed ready channel with tasks that have no dependencies.
	readyCh := make(chan int, len(tasks)+1)
	for i := range tasks {
		if unmetDeps[i] == 0 {
			readyCh <- i
		}
	}

	sem := make(chan struct{}, maxConcurrency(req.MaxConcurrency, len(tasks)))
	completionCh := make(chan int, len(tasks))
	var wg sync.WaitGroup
	var depMu sync.Mutex

	// Dispatch loop: pull ready indices and launch goroutines.
	launched := 0
	aborted := false
	for launched < len(tasks) && !aborted {
		var local int
		select {
		case local = <-readyCh:
		case <-ctx.Done():
			aborted = true
			continue
		}
		select {
		case sem <- struct{}{}:
		case <-ctx.Done():
			aborted = true
			continue
		}

		// Snapshot current stage children for policy check.
		run.mu.Lock()
		stageSlice := append([]ChildResult(nil), run.Children[offset:offset+len(tasks)]...)
		run.mu.Unlock()
		if stageWaitSatisfied(stageSlice, req.WaitPolicy) {
			<-sem
			cancel()
			aborted = true
			continue
		}

		idx := offset + local
		localIdx := local
		task := tasks[local]
		launched++
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			defer func() { completionCh <- localIdx }()
			defer func() {
				// Unblock dependents when this task finishes.
				depMu.Lock()
				for _, dep := range depOf[localIdx] {
					unmetDeps[dep]--
					if unmetDeps[dep] == 0 {
						readyCh <- dep
					}
				}
				depMu.Unlock()
			}()
			o.runChild(ctx, run, req, idx, task)
		}()
	}

	// Start the closer after all wg.Add calls are done.
	go func() { wg.Wait(); close(completionCh) }()

	// Drain completions; check budget and wait policy per completion.
	for range completionCh {
		if req.Budget.MaxSteps > 0 && !budgetExceeded {
			run.mu.Lock()
			totalSteps := 0
			for _, c := range run.Children {
				totalSteps += c.Steps
			}
			run.mu.Unlock()
			if totalSteps >= req.Budget.MaxSteps {
				budgetExceeded = true
				run.appendTimeline("budget.exceeded", "", 0, map[string]any{
					"reason": "max_steps", "total_steps": totalSteps,
				})
				cancel()
			}
		}
		if !budgetExceeded && req.WaitPolicy != WaitAll {
			run.mu.Lock()
			stageSlice := append([]ChildResult(nil), run.Children[offset:offset+len(tasks)]...)
			run.mu.Unlock()
			if stageWaitSatisfied(stageSlice, req.WaitPolicy) {
				policyEarlyExit = true
				cancel()
			}
		}
	}

	// Final policy check after all completions are drained.
	// Only set policyEarlyExit here when the context was NOT externally
	// cancelled; otherwise killed children would look like a satisfied WaitAll.
	if ctx.Err() == nil {
		run.mu.Lock()
		stageSlice := append([]ChildResult(nil), run.Children[offset:offset+len(tasks)]...)
		run.mu.Unlock()
		if stageWaitSatisfied(stageSlice, req.WaitPolicy) {
			policyEarlyExit = true
		}
	}

	return policyEarlyExit, budgetExceeded
}

// finishRun performs the aggregation pass and records terminal run state.
func (o *Orchestrator) finishRun(
	ctx context.Context, cancel context.CancelFunc,
	run *Run, req FanOutRequest, children []ChildResult,
) {
	run.mu.Lock()
	run.Status = RunStatusAggregating
	run.mu.Unlock()

	run.appendTimeline("aggregation.started", "", 0, map[string]any{"mode": string(req.Aggregation)})

	// Use a fresh context for aggregation so a policy-cancel on the
	// orchestration context does not abort the reducer LLM pass.
	aggCtx := context.Background()
	if req.Timeout > 0 {
		var aggCancel context.CancelFunc
		aggCtx, aggCancel = context.WithTimeout(aggCtx, req.Timeout/4+5*time.Second)
		defer aggCancel()
	}
	aggregated, aggErr := o.aggregateWith(aggCtx, run, req, children)

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
		run.appendTimeline("orchestration.errored", "", 0, map[string]any{"reason": "aggregation_failed"})
		slog.Warn("orchestration aggregation failed", "id", run.ID, "error", aggErr)
		o.publishLifecycle(run.ID, req.PeerID, req.ParentSessionKey, "orchestrator.errored", map[string]any{
			"error": redact.Error(aggErr),
		})
		return
	}

	run.appendTimeline("orchestration.completed", "", 0, map[string]any{"children": len(run.Children)})
	slog.Info("orchestration completed", "id", run.ID, "children", len(run.Children))
	o.publishLifecycle(run.ID, req.PeerID, req.ParentSessionKey, "orchestrator.completed", map[string]any{
		"children": len(run.Children),
	})
	if req.ParentSessionKey != "" && aggregated != "" {
		o.publishSession(req.PeerID, req.ParentSessionKey, run.ID,
			fmt.Sprintf("[orchestrator:%s] %s", run.ID, aggregated))
	}
}

// makeStageTasks returns a copy of tasks with the prior stage output prepended
// to each task prompt. Returns the original slice unchanged when prior is empty.
func makeStageTasks(base []ChildTask, prior string) []ChildTask {
	if prior == "" {
		return base
	}
	out := make([]ChildTask, len(base))
	for i, t := range base {
		cp := t
		cp.Task = "Prior stage output:\n" + prior + "\n\nYour task:\n" + t.Task
		out[i] = cp
	}
	return out
}

// buildVerifierTasks creates one verifier task per completed worker result.
func buildVerifierTasks(template *ChildTask, workers []ChildResult) []ChildTask {
	out := make([]ChildTask, 0, len(workers))
	for _, w := range workers {
		if w.FinalReply == "" {
			continue
		}
		t := *template
		t.Task = template.Task + "\n\nWork to review:\n" + w.FinalReply
		t.Label = "verifier-for-" + w.Label
		out = append(out, t)
	}
	return out
}

// buildArbiterTask creates a single arbiter task summarising all prior context.
func buildArbiterTask(template *ChildTask, priorChildren []ChildResult, priorSummary string) []ChildTask {
	var sb strings.Builder
	for _, c := range priorChildren {
		if c.FinalReply != "" {
			fmt.Fprintf(&sb, "## %s\n%s\n\n", c.Label, c.FinalReply)
		}
	}
	t := *template
	t.Task = template.Task + "\n\nAll results:\n" + sb.String()
	if priorSummary != "" {
		t.Task += "\nSummary of prior stage:\n" + priorSummary
	}
	if t.Label == "" {
		t.Label = "arbiter"
	}
	return []ChildTask{t}
}

// executeMultiStage drives a multi-stage orchestration. Stages are executed
// sequentially; prior-stage output is injected into each subsequent stage's
// task prompts. VerifierTask and ArbiterTask convenience fields are expanded
// into additional stages dynamically after the worker stage completes.
func (o *Orchestrator) executeMultiStage(
	ctx context.Context, cancel context.CancelFunc,
	run *Run, req FanOutRequest,
) {
	// Define a stage runner: a spec + a function that produces tasks given
	// the prior stage's children and aggregated output.
	type stageRunner struct {
		spec       StageSpec
		buildTasks func(prior []ChildResult, priorAgg string) []ChildTask
	}

	var runners []stageRunner
	if len(req.Stages) > 0 {
		for _, s := range req.Stages {
			spec := s
			runners = append(runners, stageRunner{
				spec:       spec,
				buildTasks: func(_ []ChildResult, priorAgg string) []ChildTask { return makeStageTasks(spec.Tasks, priorAgg) },
			})
		}
	} else {
		// Worker stage.
		runners = append(runners, stageRunner{
			spec:       StageSpec{Tasks: req.Tasks, Aggregation: req.Aggregation, WaitPolicy: req.WaitPolicy, ReducerPrompt: req.ReducerPrompt},
			buildTasks: func(_ []ChildResult, _ string) []ChildTask { return req.Tasks },
		})
		if req.VerifierTask != nil {
			vt := req.VerifierTask
			runners = append(runners, stageRunner{
				spec:       StageSpec{Aggregation: AggregateConcat},
				buildTasks: func(prior []ChildResult, _ string) []ChildTask { return buildVerifierTasks(vt, prior) },
			})
		}
		if req.ArbiterTask != nil {
			at := req.ArbiterTask
			runners = append(runners, stageRunner{
				spec:       StageSpec{Aggregation: req.Aggregation, ReducerPrompt: req.ReducerPrompt},
				buildTasks: func(prior []ChildResult, priorAgg string) []ChildTask { return buildArbiterTask(at, prior, priorAgg) },
			})
		}
	}

	priorAgg := ""
	var priorChildren []ChildResult
	stopEarly := false

	for stageIdx, runner := range runners {
		if ctx.Err() != nil || stopEarly {
			break
		}

		tasks := runner.buildTasks(priorChildren, priorAgg)
		if len(tasks) == 0 {
			continue
		}

		// Allocate child slots for this stage.
		offset := len(run.Children)
		newSlots := make([]ChildResult, len(tasks))
		for i, t := range tasks {
			lbl := t.Label
			if lbl == "" {
				lbl = fmt.Sprintf("s%d-child-%d", stageIdx, i)
				tasks[i].Label = lbl
			}
			newSlots[i] = ChildResult{Label: lbl}
		}
		run.mu.Lock()
		run.Children = append(run.Children, newSlots...)
		run.mu.Unlock()

		// Build a stage-scoped request.
		stageReq := req
		stageReq.Tasks = tasks
		if runner.spec.Aggregation != "" {
			stageReq.Aggregation = runner.spec.Aggregation
		}
		if runner.spec.WaitPolicy != "" {
			stageReq.WaitPolicy = runner.spec.WaitPolicy
		} else {
			stageReq.WaitPolicy = WaitAll
		}
		if runner.spec.ReducerPrompt != "" {
			stageReq.ReducerPrompt = runner.spec.ReducerPrompt
		}

		run.appendTimeline("stage.started", "", stageIdx, map[string]any{"stage": stageIdx, "tasks": len(tasks)})

		_, budgetExceeded := o.runDAGWave(ctx, cancel, run, stageReq, tasks, offset)

		if ctx.Err() != nil {
			o.killActiveChildren(run)
			stopEarly = true
			break
		}
		if budgetExceeded {
			stopEarly = true
			break
		}

		// Aggregate this stage.
		run.mu.Lock()
		stageChildren := append([]ChildResult(nil), run.Children[offset:offset+len(tasks)]...)
		run.mu.Unlock()

		aggCtx := context.Background()
		stageAgg, aggErr := o.aggregateWith(aggCtx, run, stageReq, stageChildren)
		if aggErr != nil {
			slog.Warn("stage aggregation failed", "id", run.ID, "stage", stageIdx, "error", aggErr)
			stopEarly = true
			break
		}

		sr := StageResult{
			StageIndex:      stageIdx,
			Children:        stageChildren,
			AggregatedReply: stageAgg,
		}
		run.mu.Lock()
		run.StageResults = append(run.StageResults, sr)
		run.mu.Unlock()

		run.appendTimeline("stage.completed", "", stageIdx, map[string]any{"stage": stageIdx, "len": len(stageAgg)})

		priorAgg = stageAgg
		priorChildren = stageChildren
	}

	// Finalize the run.
	now := time.Now().UTC()
	run.mu.Lock()
	run.FinishedAt = now

	if len(run.StageResults) > 0 {
		run.Status = RunStatusCompleted
		run.AggregatedReply = run.StageResults[len(run.StageResults)-1].AggregatedReply
	} else {
		run.Status = RunStatusCancelled
	}
	run.mu.Unlock()

	o.mu.Lock()
	delete(o.cancels, run.ID)
	o.mu.Unlock()

	if len(run.StageResults) > 0 {
		run.appendTimeline("orchestration.completed", "", 0, map[string]any{"stages": len(run.StageResults)})
		slog.Info("orchestration multi-stage completed", "id", run.ID, "stages", len(run.StageResults))
		o.publishLifecycle(run.ID, req.PeerID, req.ParentSessionKey, "orchestrator.completed", map[string]any{
			"stages": len(run.StageResults),
		})
		if req.ParentSessionKey != "" && run.AggregatedReply != "" {
			o.publishSession(req.PeerID, req.ParentSessionKey, run.ID,
				fmt.Sprintf("[orchestrator:%s] %s", run.ID, run.AggregatedReply))
		}
	} else {
		run.appendTimeline("orchestration.cancelled", "", 0, nil)
		slog.Info("orchestration cancelled (multi-stage)", "id", run.ID)
		o.publishLifecycle(run.ID, req.PeerID, req.ParentSessionKey, "orchestrator.cancelled", nil)
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

// aggregate assembles the final reply from all run.Children and records provenance.
func (o *Orchestrator) aggregate(ctx context.Context, run *Run, req FanOutRequest) (string, error) {
	run.mu.Lock()
	children := append([]ChildResult(nil), run.Children...)
	run.mu.Unlock()
	return o.aggregateWith(ctx, run, req, children)
}

// aggregateWith aggregates a caller-supplied children slice and records provenance.
// It is the canonical implementation; aggregate() is a thin wrapper over it.
func (o *Orchestrator) aggregateWith(ctx context.Context, run *Run, req FanOutRequest, children []ChildResult) (string, error) {
	start := time.Now()

	// Measure combined child reply length for provenance.
	inputLength := 0
	for _, c := range children {
		inputLength += len(c.FinalReply)
	}

	var result string
	var err error
	var reducerSessionKey string

	switch req.Aggregation {
	case AggregateCollect:
		result = aggregateCollect(children)
	case AggregateConcat:
		result = aggregateConcat(children)
	case AggregateReducer:
		reducerSessionKey = fmt.Sprintf("%s::orchestrator-reducer::%s", req.PeerID, run.ID)
		result, err = o.aggregateReducer(ctx, run, req, children)
	case AggregateVote:
		result, err = o.aggregateVote(ctx, run, req, children)
	default:
		result = aggregateCollect(children)
	}

	prov := &AggregationProvenance{
		Mode:              req.Aggregation,
		ReducerSessionKey: reducerSessionKey,
		InputLength:       inputLength,
		OutputLength:      len(result),
		DurationMs:        time.Since(start).Milliseconds(),
	}
	run.mu.Lock()
	run.Provenance = prov
	run.mu.Unlock()

	return result, err
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

// Barrier blocks until all children belonging to the named group reach a
// terminal state, or until ctx expires. Groups are declared via
// FanOutRequest.BarrierGroups. If the group name is empty, all children are
// waited on (equivalent to Wait).
func (o *Orchestrator) Barrier(ctx context.Context, orchID, group string) (*Run, error) {
	for {
		o.mu.Lock()
		r, ok := o.runs[orchID]
		o.mu.Unlock()
		if !ok {
			return nil, fmt.Errorf("orchestration %s not found", orchID)
		}

		r.mu.Lock()
		st := r.Status
		var labels []string
		if group != "" {
			labels = r.BarrierGroups[group]
		}
		r.mu.Unlock()

		switch st {
		case RunStatusCompleted, RunStatusErrored, RunStatusCancelled:
			snap := r.snapshot()
			return &snap, nil
		}

		if group == "" {
			// Wait on all children.
			r.mu.Lock()
			all := true
			for _, c := range r.Children {
				switch c.Status {
				case subagent.StatusCompleted, subagent.StatusErrored, subagent.StatusKilled:
				default:
					all = false
				}
			}
			r.mu.Unlock()
			if all {
				snap := r.snapshot()
				return &snap, nil
			}
		} else if len(labels) > 0 {
			// Wait on named group.
			labelSet := make(map[string]bool, len(labels))
			for _, l := range labels {
				labelSet[l] = true
			}
			r.mu.Lock()
			groupDone := true
			for _, c := range r.Children {
				if !labelSet[c.Label] {
					continue
				}
				switch c.Status {
				case subagent.StatusCompleted, subagent.StatusErrored, subagent.StatusKilled:
				default:
					groupDone = false
				}
			}
			r.mu.Unlock()
			if groupDone {
				snap := r.snapshot()
				return &snap, nil
			}
		}

		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(300 * time.Millisecond):
		}
	}
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

// ── Phase 2: vote aggregation ─────────────────────────────────────────────────

// aggregateVote clusters child replies by cosine similarity and elects a
// winner. If no cluster achieves a strict majority, a single LLM tie-breaker
// call is made using the top-2 cluster summaries.
func (o *Orchestrator) aggregateVote(ctx context.Context, run *Run, req FanOutRequest, children []ChildResult) (string, error) {
	// Collect non-empty replies.
	type entry struct {
		label string
		reply string
	}
	var entries []entry
	for _, c := range children {
		if c.FinalReply != "" {
			entries = append(entries, entry{c.Label, c.FinalReply})
		}
	}
	if len(entries) == 0 {
		return aggregateCollect(children), nil
	}

	threshold := req.VoteConfig.AgreementThreshold
	if threshold <= 0 {
		threshold = 0.5
	}

	// Build word-frequency vectors.
	vec := make([]map[string]float64, len(entries))
	for i, e := range entries {
		vec[i] = wordFreq(e.reply)
	}

	// Build clusters: greedy single-link — each entry joins the first cluster
	// whose centroid is within the threshold similarity.
	type cluster struct {
		indices  []int
		centroid map[string]float64
	}
	var clusters []cluster
	for i, v := range vec {
		joined := false
		for ci := range clusters {
			if cosineSim(v, clusters[ci].centroid) >= threshold {
				clusters[ci].indices = append(clusters[ci].indices, i)
				// Update centroid as element-wise average.
				clusters[ci].centroid = centroidAdd(clusters[ci].centroid, v, len(clusters[ci].indices))
				joined = true
				break
			}
		}
		if !joined {
			clusterCentroid := make(map[string]float64, len(v))
			for k, val := range v {
				clusterCentroid[k] = val
			}
			clusters = append(clusters, cluster{indices: []int{i}, centroid: clusterCentroid})
		}
	}

	// Find largest cluster.
	best := 0
	for ci, cl := range clusters {
		if len(cl.indices) > len(clusters[best].indices) {
			best = ci
		}
	}

	total := len(entries)
	winnerCluster := clusters[best]

	// Sort clusters by size descending for tie-breaker prompt.
	for i := 1; i < len(clusters); i++ {
		for j := i; j > 0 && len(clusters[j].indices) > len(clusters[j-1].indices); j-- {
			clusters[j], clusters[j-1] = clusters[j-1], clusters[j]
		}
	}

	var winner string
	var usedLLM bool

	if len(winnerCluster.indices)*2 > total {
		// Strict majority: use the first reply in the winning cluster as representative.
		winner = entries[winnerCluster.indices[0]].reply
	} else if o.agentRT != nil && len(clusters) >= 2 {
		// No majority: LLM tie-breaker between top-2 clusters.
		top1 := entries[clusters[0].indices[0]].reply
		top2 := entries[clusters[1].indices[0]].reply
		model := req.VoteConfig.TiebreakerModel
		if model == "" {
			model = req.Model
		}
		task := fmt.Sprintf(
			"Two groups of agents produced these responses. Decide which is more correct and return it verbatim (no extra text).\n\nGroup A:\n%s\n\nGroup B:\n%s",
			top1, top2,
		)
		sessionKey := fmt.Sprintf("%s::orchestrator-tiebreaker::%s", req.PeerID, run.ID)
		result, err := o.agentRT.Run(ctx, agent.RunRequest{
			PeerID:     req.PeerID,
			Scope:      agent.ScopeIsolated,
			SessionKey: sessionKey,
			Messages:   []types.Message{{Role: "user", Content: task}},
			Model:      model,
		})
		if err != nil {
			slog.Warn("orchestrator vote tiebreaker failed, using largest cluster", "id", run.ID, "error", err)
			winner = entries[clusters[0].indices[0]].reply
		} else {
			winner = redact.String(result.AssistantText)
			usedLLM = true
		}
	} else {
		// Fallback: largest cluster representative.
		winner = entries[winnerCluster.indices[0]].reply
	}

	_ = usedLLM

	if !req.VoteConfig.DisagreementReport || len(clusters) <= 1 {
		return winner, nil
	}

	// Append dissenting cluster summaries.
	var sb strings.Builder
	sb.WriteString(winner)
	sb.WriteString("\n\n--- Dissenting views ---\n")
	for ci, cl := range clusters {
		if ci == 0 {
			continue // skip winning cluster
		}
		sb.WriteString(fmt.Sprintf("Minority cluster (%d/%d replies):\n", len(cl.indices), total))
		for _, idx := range cl.indices {
			sb.WriteString(fmt.Sprintf("  [%s]: %s\n", entries[idx].label, entries[idx].reply))
		}
	}
	return sb.String(), nil
}

// ── text similarity helpers ───────────────────────────────────────────────────

// wordFreq returns a normalised term-frequency map for a text string.
func wordFreq(s string) map[string]float64 {
	freq := make(map[string]float64)
	fields := strings.Fields(strings.ToLower(s))
	for _, w := range fields {
		// Strip simple punctuation.
		w = strings.Trim(w, ".,!?;:\"'()[]{}")
		if w != "" {
			freq[w]++
		}
	}
	if len(fields) > 0 {
		n := float64(len(fields))
		for k := range freq {
			freq[k] /= n
		}
	}
	return freq
}

// cosineSim returns the cosine similarity between two word-frequency maps.
func cosineSim(a, b map[string]float64) float64 {
	var dot, normA, normB float64
	for k, va := range a {
		normA += va * va
		if vb, ok := b[k]; ok {
			dot += va * vb
		}
	}
	for _, vb := range b {
		normB += vb * vb
	}
	if normA == 0 || normB == 0 {
		return 0
	}
	return dot / (math.Sqrt(normA) * math.Sqrt(normB))
}

// centroidAdd returns the running average centroid after adding the new vector
// as the n-th member.
func centroidAdd(centroid, new map[string]float64, n int) map[string]float64 {
	result := make(map[string]float64)
	// Merge all keys from both maps.
	keys := make(map[string]struct{}, len(centroid)+len(new))
	for k := range centroid {
		keys[k] = struct{}{}
	}
	for k := range new {
		keys[k] = struct{}{}
	}
	for k := range keys {
		result[k] = (centroid[k]*float64(n-1) + new[k]) / float64(n)
	}
	return result
}

// ── Phase 2: structured output schema validation ─────────────────────────────

// validateOutputSchema parses reply as JSON and checks that all keys declared
// in the schema's "required" array are present in the top-level object.
// Returns a list of violation strings (empty means valid).
func validateOutputSchema(reply, schema string) []string {
	// Parse schema to extract "required" keys.
	var schemaDoc struct {
		Required []string `json:"required"`
	}
	if err := json.Unmarshal([]byte(schema), &schemaDoc); err != nil || len(schemaDoc.Required) == 0 {
		// Validate that reply is at least valid JSON when schema has no required keys.
		var out any
		if err2 := json.Unmarshal([]byte(reply), &out); err2 != nil {
			return []string{"reply is not valid JSON: " + err2.Error()}
		}
		return nil
	}

	var obj map[string]any
	if err := json.Unmarshal([]byte(reply), &obj); err != nil {
		return []string{"reply is not valid JSON object: " + err.Error()}
	}

	var violations []string
	for _, key := range schemaDoc.Required {
		if _, ok := obj[key]; !ok {
			violations = append(violations, fmt.Sprintf("missing required key %q", key))
		}
	}
	return violations
}
