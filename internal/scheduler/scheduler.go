package scheduler

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/robfig/cron/v3"

	"github.com/ffimnsr/koios/internal/agent"
	"github.com/ffimnsr/koios/internal/ops"
	"github.com/ffimnsr/koios/internal/presence"
	"github.com/ffimnsr/koios/internal/runledger"
	"github.com/ffimnsr/koios/internal/session"
	"github.com/ffimnsr/koios/internal/standing"
	"github.com/ffimnsr/koios/internal/types"
)

// transientErrors lists error substrings that indicate a retryable failure.
// All other errors are treated as permanent and disable the job.
var transientErrors = []string{
	"429",
	"too many requests",
	"rate limit",
	"overloaded",
	"network",
	"timeout",
	"connection",
	"server_error",
	"503",
	"502",
	"500",
}

// recurringBackoff is the sequence of wait durations applied between retry
// attempts for recurring jobs (indexed by consec_errors - 1, clamped at max).
var recurringBackoff = []time.Duration{
	30 * time.Second,
	1 * time.Minute,
	5 * time.Minute,
	15 * time.Minute,
	60 * time.Minute,
}

// oneshotBackoff is the sequence for one-shot ("at") jobs (max 3 attempts).
var oneshotBackoff = []time.Duration{
	30 * time.Second,
	1 * time.Minute,
	5 * time.Minute,
}

// provider is the minimal interface the Scheduler needs from an LLM backend.
type provider interface {
	Complete(ctx context.Context, req *types.ChatRequest) (*types.ChatResponse, error)
}

// Scheduler drives periodic job execution.  It ticks every tickInterval to
// find due jobs and dispatches them.  A wake channel allows external callers
// (e.g. the REST handler for immediate runs) to bypass the tick.
type Scheduler struct {
	store         *JobStore
	prov          provider
	sessionStore  *session.Store
	standingMgr   *standing.Manager
	model         string
	maxConcurrent int

	agentRuntime *agent.Runtime
	toolExec     agent.ToolExecutor
	hooks        *ops.Manager
	presence     *presence.Manager
	runLedger    *runledger.Store
	httpClient   *http.Client

	wakeCh  chan struct{}
	cancel  context.CancelFunc
	wg      sync.WaitGroup
	sem     chan struct{} // semaphore limited to maxConcurrent
	cronLib cron.Parser
}

const tickInterval = 5 * time.Second

// New creates a Scheduler. Call Start to begin background scheduling.
func New(store *JobStore, prov provider, sessionStore *session.Store, standingMgr *standing.Manager, model string, maxConcurrent int) *Scheduler {
	if maxConcurrent < 1 {
		maxConcurrent = 1
	}
	return &Scheduler{
		store:         store,
		prov:          prov,
		sessionStore:  sessionStore,
		standingMgr:   standingMgr,
		model:         model,
		maxConcurrent: maxConcurrent,
		wakeCh:        make(chan struct{}, 1),
		sem:           make(chan struct{}, maxConcurrent),
		cronLib:       cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow),
		httpClient:    &http.Client{Timeout: 10 * time.Second},
	}
}

// SetAgentRuntime wires a full agent runtime and tool executor into the Scheduler.
// When set, agentTurn jobs are executed through the agent loop so that the
// LLM can invoke tools (including spawning subagents for long-running work).
// Call this after both the Scheduler and the handler are fully constructed.
func (s *Scheduler) SetAgentRuntime(rt *agent.Runtime, exec agent.ToolExecutor) {
	s.agentRuntime = rt
	s.toolExec = exec
}

// SetHooks configures optional lifecycle hooks used by scheduled runs.
func (s *Scheduler) SetHooks(hooks *ops.Manager) {
	s.hooks = hooks
}

// SetPresence configures optional presence-aware scheduling.
func (s *Scheduler) SetPresence(p *presence.Manager) {
	s.presence = p
}

// Start launches the background scheduling loop. It returns immediately; the
// loop runs until the provided context is cancelled.
func (s *Scheduler) Start(ctx context.Context) {
	ctx, s.cancel = context.WithCancel(ctx)
	s.wg.Add(1)
	go s.loop(ctx)
}

// Stop signals the scheduler to stop and waits for all in-flight runs to finish.
func (s *Scheduler) Stop() {
	if s.cancel != nil {
		s.cancel()
	}
	s.wg.Wait()
}

// TriggerRun enqueues an immediate out-of-schedule run for jobID.  It does not
// wait for the run to complete.  Returns the new run ID.
func (s *Scheduler) TriggerRun(jobID string) (string, error) {
	job := s.store.Get(jobID)
	if job == nil {
		return "", fmt.Errorf("job %s not found", jobID)
	}
	runID := uuid.New().String()
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		s.executeJob(context.Background(), job, runID)
	}()
	return runID, nil
}

// SetRunLedger configures an optional unified run ledger sink.
func (s *Scheduler) SetRunLedger(ledger *runledger.Store) {
	if s == nil {
		return
	}
	s.runLedger = ledger
}

func (s *Scheduler) loop(ctx context.Context) {
	defer s.wg.Done()
	ticker := time.NewTicker(tickInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.dispatch(ctx)
		case <-s.wakeCh:
			s.dispatch(ctx)
		}
	}
}

func (s *Scheduler) dispatch(ctx context.Context) {
	now := time.Now().UTC()
	jobs := s.store.All()
	for _, job := range jobs {
		if !job.Enabled {
			continue
		}
		if job.NextRunAt.IsZero() || job.NextRunAt.After(now) {
			continue
		}
		if reason, deferred := s.shouldDefer(job, now); deferred {
			s.recordSkip(job, reason)
			continue
		}
		// Acquire semaphore without blocking (skip if all slots are busy).
		select {
		case s.sem <- struct{}{}:
		default:
			slog.Warn("cron: max concurrent runs reached, skipping job",
				"job", job.JobID, "name", job.Name)
			// Reschedule one-shot jobs so they are not permanently dropped.
			// Recurring jobs will naturally re-fire on the next tick, so they
			// do not need special treatment here.
			if job.Schedule.Kind == KindAt {
				jobCopy := *job
				jobCopy.NextRunAt = now.Add(tickInterval)
				if err := s.store.Update(&jobCopy); err != nil {
					slog.Warn("cron: failed to reschedule one-shot job after skip",
						"job", job.JobID, "error", err)
				}
			}
			continue
		}
		runID := uuid.New().String()
		jobCopy := *job
		s.wg.Add(1)
		go func(j Job, rid string) {
			defer s.wg.Done()
			defer func() { <-s.sem }()
			s.executeJob(ctx, &j, rid)
		}(jobCopy, runID)
	}
}

func (s *Scheduler) shouldDefer(job *Job, now time.Time) (string, bool) {
	if job.Dispatch.RequireApproval && s.hooks != nil {
		if err := s.hooks.Emit(context.Background(), ops.Event{
			Name:   ops.HookCronApproval,
			PeerID: job.PeerID,
			Data: map[string]any{
				"job_id":      job.JobID,
				"name":        job.Name,
				"description": job.Description,
				"payload":     job.Payload,
				"schedule":    job.Schedule,
			},
		}); err != nil {
			jobCopy := *job
			jobCopy.NextRunAt = now.Add(tickInterval)
			if updateErr := s.store.Update(&jobCopy); updateErr != nil {
				slog.Warn("cron: failed to defer unapproved job", "job", job.JobID, "error", updateErr)
			}
			return "approval required", true
		}
	}
	if !job.Dispatch.DeferIfActive || s.presence == nil {
		return "", false
	}
	state, ok := s.presence.Get(job.PeerID)
	if !ok {
		return "", false
	}
	if state.Status != "online" && !state.Typing {
		return "", false
	}
	jobCopy := *job
	jobCopy.NextRunAt = now.Add(tickInterval)
	if updateErr := s.store.Update(&jobCopy); updateErr != nil {
		slog.Warn("cron: failed to defer active-peer job", "job", job.JobID, "error", updateErr)
	}
	return "peer is active", true
}

func (s *Scheduler) recordSkip(job *Job, reason string) {
	now := time.Now().UTC()
	rec := RunRecord{
		RunID:     uuid.New().String(),
		JobID:     job.JobID,
		StartedAt: now,
		EndedAt:   now,
		Status:    RunSkip,
		Error:     reason,
	}
	if err := s.store.AppendRunRecord(rec); err != nil {
		slog.Warn("cron: failed to write skipped run record", "job", job.JobID, "error", err)
	}
	if s.runLedger != nil {
		_ = s.runLedger.Add(runledger.Record{
			ID:         rec.RunID,
			Kind:       runledger.KindCron,
			PeerID:     job.PeerID,
			SessionKey: job.PeerID,
			Status:     runledger.StatusSkipped,
			Error:      reason,
			QueuedAt:   rec.StartedAt,
			StartedAt:  &rec.StartedAt,
			FinishedAt: &rec.EndedAt,
		})
	}
}

// executeJob runs a single job, updates its state in the store, and persists
// the run record.
func (s *Scheduler) executeJob(ctx context.Context, job *Job, runID string) {
	startedAt := time.Now().UTC()
	slog.Info("cron: starting job", "job", job.JobID, "name", job.Name, "peer", job.PeerID, "run", runID)

	out, execErr := s.runPayload(ctx, job)

	endedAt := time.Now().UTC()

	rec := RunRecord{
		RunID:     runID,
		JobID:     job.JobID,
		StartedAt: startedAt,
		EndedAt:   endedAt,
		Output:    out,
	}
	if execErr != nil {
		rec.Status = RunError
		rec.Error = execErr.Error()
		slog.Warn("cron: job failed", "job", job.JobID, "name", job.Name, "error", execErr)
	} else {
		rec.Status = RunOK
		slog.Info("cron: job succeeded", "job", job.JobID, "name", job.Name, "run", runID)
	}
	if err := s.store.AppendRunRecord(rec); err != nil {
		slog.Warn("cron: failed to write run record", "job", job.JobID, "error", err)
	}
	if s.runLedger != nil {
		status := runledger.StatusCompleted
		if rec.Status == RunError {
			status = runledger.StatusErrored
		}
		_ = s.runLedger.Add(runledger.Record{
			ID:         rec.RunID,
			Kind:       runledger.KindCron,
			PeerID:     job.PeerID,
			SessionKey: job.PeerID,
			Status:     status,
			Error:      rec.Error,
			QueuedAt:   rec.StartedAt,
			StartedAt:  &rec.StartedAt,
			FinishedAt: &rec.EndedAt,
		})
	}

	// Reload the current version of the job (may have changed while we ran).
	current := s.store.Get(job.JobID)
	if current == nil {
		return // job was deleted while running
	}

	now := time.Now().UTC()
	current.LastRunAt = now

	if execErr != nil {
		current.ConsecErrors++
		if isPermanentError(execErr) || (current.Schedule.Kind == KindAt && current.ConsecErrors > len(oneshotBackoff)) {
			current.Enabled = false
			slog.Info("cron: disabling job after permanent/max-retry failure", "job", job.JobID)
		} else {
			backoff := backoffDuration(current.ConsecErrors, current.Schedule.Kind)
			current.NextRunAt = now.Add(backoff)
		}
	} else {
		current.ConsecErrors = 0
		if current.DeleteAfterRun && current.Schedule.Kind == KindAt {
			_ = s.store.Remove(current.JobID)
			return
		}
		if current.Schedule.Kind == KindAt {
			current.Enabled = false
		} else {
			next, err := calcNextRun(current, now, &s.cronLib)
			if err != nil {
				slog.Warn("cron: could not compute next run", "job", job.JobID, "error", err)
				current.Enabled = false
			} else {
				current.NextRunAt = next
			}
		}
	}

	if err := s.store.Update(current); err != nil {
		slog.Warn("cron: failed to update job after run", "job", job.JobID, "error", err)
	}
}

// runPayload dispatches the job payload to the appropriate execution path.
func (s *Scheduler) runPayload(ctx context.Context, job *Job) (string, error) {
	switch job.Payload.Kind {
	case PayloadSystemEvent:
		return s.runSystemEvent(job)
	case PayloadAgentTurn:
		return s.runAgentTurn(ctx, job)
	default:
		return "", fmt.Errorf("unknown payload kind %q", job.Payload.Kind)
	}
}

// runSystemEvent appends a system message directly to the peer's session.
func (s *Scheduler) runSystemEvent(job *Job) (string, error) {
	text := fmt.Sprintf("[system-event:%s %s] %s", job.JobID, job.Name, job.Payload.Text)
	s.sessionStore.AppendWithSource(job.PeerID, "cron", types.Message{Role: "system", Content: text})
	return "", nil
}

// runAgentTurn sends an LLM turn for the peer and appends the assistant
// response to the peer's session.  When job.Payload.IncludeHistory is true the
// peer's current session history is prepended so the model has conversational
// context; otherwise a fresh (stateless) context is used.
func (s *Scheduler) runAgentTurn(ctx context.Context, job *Job) (string, error) {
	prompt := fmt.Sprintf("[cron:%s %s] %s", job.JobID, job.Name, job.Payload.Message)
	var messages []types.Message
	if s.standingMgr != nil {
		msg, err := s.standingMgr.SystemMessage(job.PeerID)
		if err != nil {
			return "", err
		}
		if msg != nil {
			messages = append(messages, *msg)
		}
	}
	if job.Payload.IncludeHistory {
		history := s.sessionStore.Get(job.PeerID).History()
		// Append only non-system history to avoid duplicating the standing
		// orders message that was already prepended above.
		for _, m := range history {
			if m.Role != "system" {
				messages = append(messages, m)
			}
		}
	}
	preloaded, err := s.lazyLoadContent(ctx, job.Payload.PreloadURLs)
	if err != nil {
		return "", err
	}
	messages = append(messages, preloaded...)
	messages = append(messages, types.Message{Role: "user", Content: prompt})

	if s.agentRuntime != nil {
		// Full agent loop: the LLM can invoke tools and spawn subagents.
		// Use ScopeIsolated so intermediate messages go to a throwaway session
		// key and do not clutter the peer's session; then append the final
		// assistant text to the peer's session via AppendWithSource as before.
		result, err := s.agentRuntime.Run(ctx, agent.RunRequest{
			PeerID:       job.PeerID,
			Scope:        agent.ScopeIsolated,
			Messages:     messages,
			ToolExecutor: s.toolExec,
			Model:        s.model,
		})
		if err != nil {
			return "", err
		}
		assistantText := result.AssistantText
		if assistantText != "" {
			s.sessionStore.AppendWithSource(job.PeerID, "cron", types.Message{Role: "assistant", Content: assistantText})
		}
		return assistantText, nil
	}

	req := &types.ChatRequest{
		Model:    s.model,
		Messages: messages,
	}

	resp, err := s.prov.Complete(ctx, req)
	if err != nil {
		return "", err
	}
	if len(resp.Choices) == 0 {
		return "", fmt.Errorf("empty response from provider")
	}
	assistantText := resp.Choices[0].Message.Content
	if suppressed, _ := agent.DetectSilentReply(assistantText); suppressed {
		return "", nil
	}
	s.sessionStore.AppendWithSource(job.PeerID, "cron", types.Message{Role: "assistant", Content: assistantText})
	return assistantText, nil
}

func (s *Scheduler) lazyLoadContent(ctx context.Context, rawURLs []string) ([]types.Message, error) {
	if len(rawURLs) == 0 {
		return nil, nil
	}
	out := make([]types.Message, 0, len(rawURLs))
	for _, raw := range rawURLs {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		u, err := url.Parse(raw)
		if err != nil {
			return nil, fmt.Errorf("invalid preload url %q: %w", raw, err)
		}
		if u.Scheme != "http" && u.Scheme != "https" {
			return nil, fmt.Errorf("preload url %q must use http or https", raw)
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, raw, nil)
		if err != nil {
			return nil, fmt.Errorf("preload %q: %w", raw, err)
		}
		resp, err := s.httpClient.Do(req)
		if err != nil {
			return nil, fmt.Errorf("preload %q: %w", raw, err)
		}
		body, readErr := io.ReadAll(io.LimitReader(resp.Body, 32*1024))
		closeErr := resp.Body.Close()
		if readErr != nil {
			return nil, fmt.Errorf("preload %q: %w", raw, readErr)
		}
		if closeErr != nil {
			return nil, fmt.Errorf("preload %q: %w", raw, closeErr)
		}
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			return nil, fmt.Errorf("preload %q returned status %d", raw, resp.StatusCode)
		}
		content := strings.TrimSpace(string(body))
		if content == "" {
			continue
		}
		out = append(out, types.Message{
			Role:    "system",
			Content: fmt.Sprintf("[preloaded:%s]\n%s", raw, content),
		})
	}
	return out, nil
}

// calcNextRun computes the next fire time for a job starting from from.
// It applies the job's stagger offset deterministically.
func calcNextRun(job *Job, from time.Time, parser *cron.Parser) (time.Time, error) {
	stagger := staggerOffset(job.JobID, job.Schedule.StaggerMs)

	switch job.Schedule.Kind {
	case KindAt:
		t, err := time.Parse(time.RFC3339, job.Schedule.At)
		if err != nil {
			// Try without timezone (treat as UTC).
			t, err = time.Parse("2006-01-02T15:04:05", job.Schedule.At)
			if err != nil {
				return time.Time{}, fmt.Errorf("parse at time %q: %w", job.Schedule.At, err)
			}
			t = t.UTC()
		}
		return t.Add(stagger), nil

	case KindEvery:
		if job.Schedule.EveryMs <= 0 {
			return time.Time{}, fmt.Errorf("every_ms must be > 0")
		}
		return from.Add(time.Duration(job.Schedule.EveryMs)*time.Millisecond + stagger), nil

	case KindCron:
		loc := time.UTC
		if job.Schedule.Tz != "" {
			l, err := time.LoadLocation(job.Schedule.Tz)
			if err != nil {
				return time.Time{}, fmt.Errorf("invalid timezone %q: %w", job.Schedule.Tz, err)
			}
			loc = l
		}
		sched, err := parser.Parse(job.Schedule.Expr)
		if err != nil {
			return time.Time{}, fmt.Errorf("parse cron %q: %w", job.Schedule.Expr, err)
		}
		next := sched.Next(from.In(loc))
		return next.UTC().Add(stagger), nil

	default:
		return time.Time{}, fmt.Errorf("unknown schedule kind %q", job.Schedule.Kind)
	}
}

// CalcInitialNextRun computes the first fire time for a newly added job.
// Exported so callers (e.g. REST handlers) can populate NextRunAt before Add.
func CalcInitialNextRun(job *Job, parser *cron.Parser) (time.Time, error) {
	return calcNextRun(job, time.Now().UTC(), parser)
}

// CronParser returns a pointer to the Scheduler's underlying cron parser,
// allowing external callers to validate expressions or compute next-run times.
func (s *Scheduler) CronParser() *cron.Parser {
	return &s.cronLib
}

// isPermanentError returns true for errors that should never be retried
// (e.g. authentication failures).
func isPermanentError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	permanent := []string{"401", "403", "unauthorized", "forbidden", "invalid api key", "auth"}
	for _, p := range permanent {
		if strings.Contains(msg, p) {
			return true
		}
	}
	// Check that it is NOT a known transient error.
	for _, t := range transientErrors {
		if strings.Contains(msg, t) {
			return false // transient → not permanent
		}
	}
	// Unknown → treat as permanent to avoid infinite retries.
	return true
}

// backoffDuration returns the wait duration before the next retry attempt.
func backoffDuration(consecErrors int, kind ScheduleKind) time.Duration {
	if kind == KindAt {
		idx := consecErrors - 1
		if idx < 0 {
			idx = 0
		}
		if idx >= len(oneshotBackoff) {
			idx = len(oneshotBackoff) - 1
		}
		return oneshotBackoff[idx]
	}
	idx := consecErrors - 1
	if idx < 0 {
		idx = 0
	}
	if idx >= len(recurringBackoff) {
		idx = len(recurringBackoff) - 1
	}
	return recurringBackoff[idx]
}
