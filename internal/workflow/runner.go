package workflow

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/ffimnsr/koios/internal/agent"
	"github.com/ffimnsr/koios/internal/types"
)

// agentProvider is the minimal interface the Runner requires from an LLM
// agent runtime.
type agentProvider interface {
	Run(ctx context.Context, req agent.RunRequest) (*agent.Result, error)
	Model() string
}

// Runner drives workflow run execution.  Runs are dispatched asynchronously;
// status is persisted to the Store after every step.
type Runner struct {
	store      *Store
	httpClient *http.Client

	mu          sync.Mutex
	agentRT     agentProvider
	toolExec    agent.ToolExecutor
	cancels     map[string]context.CancelFunc
}

// NewRunner creates a Runner backed by store.  Call SetAgentRuntime before
// starting any agent_turn workflows.
func NewRunner(store *Store) *Runner {
	return &Runner{
		store:      store,
		httpClient: &http.Client{Timeout: 30 * time.Second},
		cancels:    make(map[string]context.CancelFunc),
	}
}

// Store returns the backing store for direct definition and run access.
func (r *Runner) Store() *Store {
	return r.store
}

// SetAgentRuntime wires a full agent runtime and optional tool executor into
// the Runner.  Without this, agent_turn steps return an error.  Call this
// after both the Runner and the handler are fully constructed.
func (r *Runner) SetAgentRuntime(rt agentProvider, exec agent.ToolExecutor) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.agentRT = rt
	r.toolExec = exec
}

// Start launches a new run for workflowID owned by peerID asynchronously.
// It returns the initial Run record immediately; callers can poll Status for
// updates.
func (r *Runner) Start(ctx context.Context, workflowID, peerID string) (*Run, error) {
	wf := r.store.Get(workflowID)
	if wf == nil {
		return nil, fmt.Errorf("workflow %s not found", workflowID)
	}
	if wf.PeerID != peerID {
		return nil, fmt.Errorf("workflow %s is not accessible to peer %s", workflowID, peerID)
	}
	if len(wf.Steps) == 0 {
		return nil, fmt.Errorf("workflow %s has no steps", workflowID)
	}
	firstStep := wf.FirstStep
	if firstStep == "" {
		firstStep = wf.Steps[0].ID
	}
	run := &Run{
		ID:          uuid.New().String(),
		WorkflowID:  workflowID,
		PeerID:      peerID,
		Status:      RunStatusPending,
		CurrentStep: firstStep,
		StepResults: make(map[string]StepResult),
		StartedAt:   time.Now().UTC(),
		UpdatedAt:   time.Now().UTC(),
	}
	if err := r.store.SaveRun(run); err != nil {
		return nil, fmt.Errorf("workflow: save initial run: %w", err)
	}

	// Copy the workflow snapshot so the goroutine is not affected by future
	// mutations to the definition.
	wfCopy := *wf

	runCtx, cancel := context.WithCancel(context.Background())
	r.mu.Lock()
	r.cancels[run.ID] = cancel
	r.mu.Unlock()

	go func() {
		defer func() {
			r.mu.Lock()
			delete(r.cancels, run.ID)
			r.mu.Unlock()
			cancel()
		}()
		r.execute(runCtx, &wfCopy, run)
	}()

	// Return a snapshot that reflects the pending status.
	snap := *run
	return &snap, nil
}

// Cancel stops an active run by ID.  Returns an error when the run is not
// currently active.
func (r *Runner) Cancel(runID string) error {
	r.mu.Lock()
	cancel, ok := r.cancels[runID]
	r.mu.Unlock()
	if !ok {
		return fmt.Errorf("run %s is not active", runID)
	}
	cancel()
	return nil
}

// Status loads and returns the current run record from the store.
func (r *Runner) Status(runID string) (*Run, error) {
	return r.store.LoadRun(runID)
}

// execute drives a workflow run to completion in the calling goroutine.
func (r *Runner) execute(ctx context.Context, wf *Workflow, run *Run) {
	run.Status = RunStatusRunning
	run.UpdatedAt = time.Now().UTC()
	if err := r.store.SaveRun(run); err != nil {
		slog.Error("workflow runner: save run failed", "run_id", run.ID, "err", err)
	}

	stepIdx := buildStepIndex(wf)
	// Cap iterations to prevent infinite loops from circular step graphs.
	maxIter := len(wf.Steps) * 3
	if maxIter < 20 {
		maxIter = 20
	}

	for i := 0; i < maxIter; i++ {
		select {
		case <-ctx.Done():
			run.Status = RunStatusCancelled
			run.FinishedAt = time.Now().UTC()
			run.UpdatedAt = run.FinishedAt
			_ = r.store.SaveRun(run)
			slog.Info("workflow run cancelled", "run_id", run.ID, "workflow_id", run.WorkflowID)
			return
		default:
		}

		if run.CurrentStep == "" {
			run.Status = RunStatusCompleted
			run.FinishedAt = time.Now().UTC()
			run.UpdatedAt = run.FinishedAt
			_ = r.store.SaveRun(run)
			slog.Info("workflow run completed", "run_id", run.ID, "workflow_id", run.WorkflowID)
			return
		}

		step, ok := stepIdx[run.CurrentStep]
		if !ok {
			run.Status = RunStatusFailed
			run.Error = fmt.Sprintf("step %q not found in workflow definition", run.CurrentStep)
			run.FinishedAt = time.Now().UTC()
			run.UpdatedAt = run.FinishedAt
			_ = r.store.SaveRun(run)
			slog.Error("workflow run failed: step not found",
				"run_id", run.ID, "step", run.CurrentStep)
			return
		}

		slog.Info("workflow step starting",
			"run_id", run.ID, "step_id", step.ID, "kind", step.Kind)
		res := r.executeStep(ctx, wf, run, step)
		run.StepResults[step.ID] = res
		// Condition steps use a "true"/"false" sentinel for routing only;
		// preserve the previous step's content so downstream steps still
		// receive meaningful {{.Output}} substitutions.
		if step.Kind != StepKindCondition {
			run.LastOutput = res.Output
		}
		run.UpdatedAt = time.Now().UTC()

		if res.Status == "failed" {
			if step.OnFailure == "" {
				// If the context was cancelled while the step was running, report
				// the run as cancelled rather than failed.
				if ctx.Err() != nil {
					run.Status = RunStatusCancelled
					run.FinishedAt = time.Now().UTC()
					run.UpdatedAt = run.FinishedAt
					_ = r.store.SaveRun(run)
					slog.Info("workflow run cancelled", "run_id", run.ID, "workflow_id", run.WorkflowID)
					return
				}
				run.Status = RunStatusFailed
				run.Error = fmt.Sprintf("step %q failed: %s", step.ID, res.Error)
				run.FinishedAt = time.Now().UTC()
				run.UpdatedAt = run.FinishedAt
				_ = r.store.SaveRun(run)
				slog.Error("workflow run failed", "run_id", run.ID, "step", step.ID,
					"err", res.Error)
				return
			}
			run.CurrentStep = step.OnFailure
		} else {
			run.CurrentStep = nextStep(step, res.Output)
		}
		_ = r.store.SaveRun(run)
	}

	run.Status = RunStatusFailed
	run.Error = "exceeded maximum step iterations; possible cycle in workflow graph"
	run.FinishedAt = time.Now().UTC()
	run.UpdatedAt = run.FinishedAt
	_ = r.store.SaveRun(run)
	slog.Error("workflow run exceeded max iterations", "run_id", run.ID)
}

// executeStep dispatches one step and returns its result.
func (r *Runner) executeStep(ctx context.Context, wf *Workflow, run *Run, step Step) StepResult {
	res := StepResult{
		StepID:    step.ID,
		StartedAt: time.Now().UTC(),
	}
	var output string
	var err error
	switch step.Kind {
	case StepKindAgentTurn:
		output, err = r.runAgentTurn(ctx, wf.PeerID, run, step)
	case StepKindWebhook:
		output, err = r.runWebhook(ctx, run, step)
	case StepKindCondition:
		output, err = r.runCondition(run, step)
	default:
		err = fmt.Errorf("unknown step kind %q", step.Kind)
	}
	res.FinishedAt = time.Now().UTC()
	if err != nil {
		res.Status = "failed"
		res.Error = err.Error()
	} else {
		res.Status = "completed"
		res.Output = output
	}
	return res
}

func (r *Runner) runAgentTurn(ctx context.Context, peerID string, run *Run, step Step) (string, error) {
	r.mu.Lock()
	rt := r.agentRT
	toolExec := r.toolExec
	r.mu.Unlock()
	if rt == nil {
		return "", fmt.Errorf("agent runtime not available; call SetAgentRuntime first")
	}

	msg := applyTemplate(step.Message, run.LastOutput)
	model := step.Model
	if model == "" {
		model = rt.Model()
	}
	maxSteps := step.MaxSteps
	if maxSteps <= 0 {
		maxSteps = 10
	}
	timeout := time.Duration(step.TimeoutSeconds) * time.Second
	if timeout <= 0 {
		timeout = 2 * time.Minute
	}

	req := agent.RunRequest{
		PeerID:     peerID,
		SessionKey: fmt.Sprintf("%s::workflow::%s::%s", peerID, run.ID, step.ID),
		Scope:      agent.ScopeIsolated,
		Messages: []types.Message{
			{Role: "user", Content: msg},
		},
		Model:        model,
		MaxSteps:     maxSteps,
		Timeout:      timeout,
		ToolExecutor: toolExec,
	}

	result, err := rt.Run(ctx, req)
	if err != nil {
		return "", fmt.Errorf("agent turn: %w", err)
	}
	return strings.TrimSpace(result.AssistantText), nil
}

func (r *Runner) runWebhook(ctx context.Context, run *Run, step Step) (string, error) {
	method := strings.ToUpper(strings.TrimSpace(step.Method))
	if method == "" {
		method = http.MethodPost
	}
	body := applyTemplate(step.BodyTemplate, run.LastOutput)
	timeout := time.Duration(step.WebhookTimeoutSeconds) * time.Second
	if timeout <= 0 {
		timeout = 30 * time.Second
	}

	reqCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, method, step.URL, strings.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("webhook: build request: %w", err)
	}
	for k, v := range step.Headers {
		req.Header.Set(k, v)
	}
	if body != "" {
		if _, ok := step.Headers["Content-Type"]; !ok {
			req.Header.Set("Content-Type", "application/json")
		}
	}

	resp, err := r.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("webhook: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	if err != nil {
		return "", fmt.Errorf("webhook: read response: %w", err)
	}
	if resp.StatusCode >= 400 {
		return "", fmt.Errorf("webhook: HTTP %d: %s", resp.StatusCode,
			bytes.TrimSpace(respBody))
	}
	return strings.TrimSpace(string(respBody)), nil
}

// runCondition evaluates the step condition against the previous step's output
// and returns "true" or "false" as a sentinel for routing in nextStep.
func (r *Runner) runCondition(run *Run, step Step) (string, error) {
	if evalCondition(step.Condition, run.LastOutput) {
		return "true", nil
	}
	return "false", nil
}

// ── helpers ───────────────────────────────────────────────────────────────────

// nextStep returns the step ID to execute after step completes with output.
// For condition steps output is the "true"/"false" sentinel returned by
// runCondition — the expression has already been evaluated.
func nextStep(step Step, output string) string {
	if step.Kind == StepKindCondition {
		if output == "true" {
			return step.TrueStep
		}
		return step.FalseStep
	}
	return step.OnSuccess
}

// evalCondition evaluates a simple predicate expression against prev.
func evalCondition(cond, prev string) bool {
	switch {
	case cond == "always_true":
		return true
	case cond == "always_false":
		return false
	case cond == "output_empty":
		return strings.TrimSpace(prev) == ""
	case strings.HasPrefix(cond, "output_contains:"):
		return strings.Contains(prev, strings.TrimPrefix(cond, "output_contains:"))
	case strings.HasPrefix(cond, "output_not_contains:"):
		return !strings.Contains(prev, strings.TrimPrefix(cond, "output_not_contains:"))
	default:
		return false
	}
}

// applyTemplate replaces "{{.Output}}" with the previous step's output.
func applyTemplate(tmpl, output string) string {
	return strings.ReplaceAll(tmpl, "{{.Output}}", output)
}

// buildStepIndex returns a map from step ID to Step for fast lookup.
func buildStepIndex(wf *Workflow) map[string]Step {
	idx := make(map[string]Step, len(wf.Steps))
	for _, s := range wf.Steps {
		idx[s.ID] = s
	}
	return idx
}
