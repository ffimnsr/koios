// Package workflow provides a multi-step workflow engine comparable to
// Lobster-style routine orchestration.
//
// A Workflow is a named, peer-owned sequence of Steps.  Each Step can execute
// an LLM agent turn, call an external webhook, or evaluate a condition to
// choose the next step.  Workflow definitions and run history are persisted to
// disk under the configured workflow directory.
package workflow

import "time"

// StepKind names the kinds of work a step performs.
type StepKind string

const (
	// StepKindAgentTurn runs an LLM agent turn and stores the assistant reply.
	StepKindAgentTurn StepKind = "agent_turn"
	// StepKindWebhook posts to an HTTP endpoint and stores the response body.
	StepKindWebhook StepKind = "webhook"
	// StepKindCondition evaluates a simple expression and routes to TrueStep
	// or FalseStep based on the result.
	StepKindCondition StepKind = "condition"
)

// Step is one node in a workflow graph.
type Step struct {
	// ID uniquely identifies this step within the workflow.
	ID   string   `json:"id"`
	Name string   `json:"name,omitempty"`
	Kind StepKind `json:"kind"`

	// ── agent_turn fields ────────────────────────────────────────────────────

	// Message is the prompt sent to the LLM.  "{{.Output}}" is replaced with
	// the previous step's output text.
	Message string `json:"message,omitempty"`
	// Model, when non-empty, overrides the default model for this step.
	Model string `json:"model,omitempty"`
	// MaxSteps caps the LLM tool-call loop for this step (default 10).
	MaxSteps int `json:"max_steps,omitempty"`
	// TimeoutSeconds is the per-step agent timeout (default 120).
	TimeoutSeconds int `json:"timeout_seconds,omitempty"`
	// IncludeHistory, when true, prepends the peer's current main session
	// history so the LLM has full conversational context.
	IncludeHistory bool `json:"include_history,omitempty"`
	// Profile, when non-empty, activates a named standing/persona profile for
	// this agent_turn execution.
	Profile string `json:"profile,omitempty"`

	// ── webhook fields ───────────────────────────────────────────────────────

	// URL is the HTTP endpoint for webhook steps.
	URL string `json:"url,omitempty"`
	// Method defaults to POST.
	Method string `json:"method,omitempty"`
	// Headers are extra HTTP headers added to the request.
	Headers map[string]string `json:"headers,omitempty"`
	// BodyTemplate is the request body; "{{.Output}}" is replaced with the
	// previous step's output.
	BodyTemplate string `json:"body_template,omitempty"`
	// WebhookTimeoutSeconds is the HTTP request timeout (default 30).
	WebhookTimeoutSeconds int `json:"webhook_timeout_seconds,omitempty"`

	// ── condition fields ─────────────────────────────────────────────────────

	// Condition is a simple predicate evaluated against the previous step's
	// output.  Supported expressions:
	//   always_true                  — always picks TrueStep
	//   always_false                 — always picks FalseStep
	//   output_empty                 — true when previous output is blank
	//   output_contains:<text>       — true when output contains <text>
	//   output_not_contains:<text>   — true when output does not contain <text>
	Condition string `json:"condition,omitempty"`
	// TrueStep is the next step ID when Condition evaluates to true.
	TrueStep string `json:"true_step,omitempty"`
	// FalseStep is the next step ID when Condition evaluates to false.
	FalseStep string `json:"false_step,omitempty"`

	// ── routing (non-condition steps) ────────────────────────────────────────

	// OnSuccess is the step ID to advance to on success.
	// An empty string ends the workflow successfully.
	OnSuccess string `json:"on_success,omitempty"`
	// OnFailure is the step ID to advance to on failure.
	// An empty string marks the entire run as failed.
	OnFailure string `json:"on_failure,omitempty"`
}

// Workflow is a reusable automation definition owned by a single peer.
type Workflow struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	PeerID      string `json:"peer_id"`
	// FirstStep is the ID of the entry-point step.
	// Defaults to Steps[0].ID when empty.
	FirstStep string    `json:"first_step,omitempty"`
	Steps     []Step    `json:"steps"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// RunStatus is the lifecycle state of a workflow run.
type RunStatus string

const (
	RunStatusPending   RunStatus = "pending"
	RunStatusRunning   RunStatus = "running"
	RunStatusCompleted RunStatus = "completed"
	RunStatusFailed    RunStatus = "failed"
	RunStatusCancelled RunStatus = "cancelled"
)

// StepResult records the outcome of one executed step.
type StepResult struct {
	StepID     string    `json:"step_id"`
	Status     string    `json:"status"` // "completed" or "failed"
	Output     string    `json:"output,omitempty"`
	Error      string    `json:"error,omitempty"`
	StartedAt  time.Time `json:"started_at"`
	FinishedAt time.Time `json:"finished_at"`
}

// Run tracks the execution state of a single workflow invocation.
type Run struct {
	ID         string    `json:"id"`
	WorkflowID string    `json:"workflow_id"`
	PeerID     string    `json:"peer_id"`
	Status     RunStatus `json:"status"`
	// CurrentStep is the ID of the step being executed or to be executed next.
	CurrentStep string                `json:"current_step,omitempty"`
	StepResults map[string]StepResult `json:"step_results"`
	// LastOutput is the most recent completed step's output, available to
	// subsequent steps via the "{{.Output}}" template placeholder.
	LastOutput string    `json:"last_output,omitempty"`
	Error      string    `json:"error,omitempty"`
	StartedAt  time.Time `json:"started_at"`
	FinishedAt time.Time `json:"finished_at,omitempty"`
	UpdatedAt  time.Time `json:"updated_at"`
}
