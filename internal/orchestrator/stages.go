package orchestrator

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/ffimnsr/koios/internal/runledger"
)

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

		sr := StageResult{StageIndex: stageIdx, Children: stageChildren, AggregatedReply: stageAgg}
		run.mu.Lock()
		run.StageResults = append(run.StageResults, sr)
		run.mu.Unlock()

		run.appendTimeline("stage.completed", "", stageIdx, map[string]any{"stage": stageIdx, "len": len(stageAgg)})

		priorAgg = stageAgg
		priorChildren = stageChildren
	}

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
		o.recordTerminal(run, runledger.StatusCompleted, "")
	} else {
		run.appendTimeline("orchestration.cancelled", "", 0, nil)
		slog.Info("orchestration cancelled (multi-stage)", "id", run.ID)
		o.publishLifecycle(run.ID, req.PeerID, req.ParentSessionKey, "orchestrator.cancelled", nil)
		o.recordTerminal(run, runledger.StatusCanceled, "")
	}
}
