package orchestrator

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/ffimnsr/koios/internal/redact"
	"github.com/ffimnsr/koios/internal/runledger"
	"github.com/ffimnsr/koios/internal/subagent"
)

// execute is the goroutine that drives the orchestration lifecycle.
func (o *Orchestrator) execute(ctx context.Context, cancel context.CancelFunc, run *Run, req FanOutRequest) {
	defer cancel()

	run.mu.Lock()
	run.Status = RunStatusRunning
	run.StartedAt = time.Now().UTC()
	startedAt := run.StartedAt
	run.mu.Unlock()
	if ledger := o.currentLedger(); ledger != nil {
		ledger.LedgerStarted(run.ID, startedAt)
	}

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

	if len(req.Stages) > 0 || req.VerifierTask != nil || req.ArbiterTask != nil {
		o.executeMultiStage(ctx, cancel, run, req)
		return
	}

	policyEarlyExit, budgetExceeded := o.runDAGWave(ctx, cancel, run, req, req.Tasks, 0)

	if ctx.Err() != nil {
		o.killActiveChildren(run)
	}

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
		o.recordTerminal(run, runledger.StatusErrored, run.Error)
		return
	}

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
		o.recordTerminal(run, runledger.StatusCanceled, "")
		return
	}

	run.mu.Lock()
	children := append([]ChildResult(nil), run.Children...)
	run.mu.Unlock()
	o.finishRun(ctx, cancel, run, req, children)
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
	return childOutcome{status: subagent.StatusErrored, err: "poll loop exited unexpectedly"}
}

// runChild executes one child slot with support for retry, hedging, and fallback.
func (o *Orchestrator) runChild(ctx context.Context, run *Run, req FanOutRequest, idx int, task ChildTask) {
	run.mu.Lock()
	childLabel := run.Children[idx].Label
	run.mu.Unlock()

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
		if attempt > 0 && retry.BackoffBase > 0 {
			backoff := retry.BackoffBase * (1 << uint(attempt-1))
			select {
			case <-ctx.Done():
				goto done
			case <-time.After(backoff):
			}
		}

		if hedge > 1 {
			lastOutcome, succeeded = o.runChildHedged(ctx, run, req, idx, task, hedge)
			attempts += hedge
		} else {
			o.publishChildProgress(run.ID, req.PeerID, req.ParentSessionKey, idx, childLabel, "queued")
			lastOutcome = o.spawnAndPoll(ctx, run, req, idx, task)
			attempts++
			succeeded = lastOutcome.status == subagent.StatusCompleted
		}
	}

	if !succeeded && task.FallbackTask != nil {
		slog.Info("orchestrator: running fallback task", "orchestration", run.ID, "idx", idx)
		o.publishChildProgress(run.ID, req.PeerID, req.ParentSessionKey, idx, childLabel, "fallback")
		lastOutcome = o.spawnAndPoll(ctx, run, req, idx, *task.FallbackTask)
		attempts++
		succeeded = lastOutcome.status == subagent.StatusCompleted
	}

done:
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

	if req.StreamPartials && req.ParentSessionKey != "" && childReply != "" {
		o.publishSession(req.PeerID, req.ParentSessionKey, run.ID,
			fmt.Sprintf("[orchestrator:%s child %s] %s", run.ID, childLabel, childReply))
	}

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
			hedgeCancel()
		}
		if received == count {
			break
		}
	}
	if !winnerFound {
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
		if c.Status == subagent.StatusQueued || c.Status == subagent.StatusRunning || c.Status == "" {
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

	labelToLocal := make(map[string]int, len(tasks))
	for i := range tasks {
		labelToLocal[run.Children[offset+i].Label] = i
	}

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

	go func() { wg.Wait(); close(completionCh) }()

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
		o.recordTerminal(run, runledger.StatusErrored, run.Error)
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
	o.recordTerminal(run, runledger.StatusCompleted, "")
}
