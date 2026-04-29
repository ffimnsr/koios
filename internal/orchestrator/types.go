package orchestrator

import (
	"sync"
	"time"

	"github.com/ffimnsr/koios/internal/subagent"
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
	// AgreementThreshold is the minimum cosine-similarity score (0-1) for two
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
	// When empty a positional label ("child-0", "child-1", ...) is used.
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
	// MaxConcurrency limits how many children run simultaneously (<=0 = unlimited).
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
	// childRunIDs maps subagent run ID -> child index for fast lookup.
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
