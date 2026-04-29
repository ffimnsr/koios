package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/ffimnsr/koios/internal/agent"
	"github.com/ffimnsr/koios/internal/orchestrator"
	"github.com/ffimnsr/koios/internal/scheduler"
)

func normalizeSchedule(s scheduler.Schedule) scheduler.Schedule {
	if s.Kind != scheduler.KindCron {
		return s
	}
	fields := strings.Fields(s.Expr)
	if len(fields) == 6 {
		s.Expr = strings.Join(fields[1:], " ")
	}
	return s
}

func (h *Handler) createCronJob(peerID string, p cronCreateParams) (*scheduler.Job, error) {
	if h.jobStore == nil || h.sched == nil {
		return nil, fmt.Errorf("cron is not enabled")
	}
	if strings.TrimSpace(p.Name) == "" {
		return nil, fmt.Errorf("name is required")
	}
	p.Schedule = normalizeSchedule(p.Schedule)
	if err := validateSchedule(p.Schedule); err != nil {
		return nil, fmt.Errorf("invalid schedule: %w", err)
	}
	if err := validatePayload(p.Payload); err != nil {
		return nil, fmt.Errorf("invalid payload: %w", err)
	}
	enabled := true
	if p.Enabled != nil {
		enabled = *p.Enabled
	}
	deleteAfterRun := p.Schedule.Kind == scheduler.KindAt
	if p.DeleteAfterRun != nil {
		deleteAfterRun = *p.DeleteAfterRun
	}
	job := &scheduler.Job{
		PeerID:         peerID,
		Name:           p.Name,
		Description:    p.Description,
		Schedule:       p.Schedule,
		Payload:        p.Payload,
		Dispatch:       p.Dispatch,
		Enabled:        enabled,
		DeleteAfterRun: deleteAfterRun,
	}
	nextRun, err := scheduler.CalcInitialNextRun(job, h.sched.CronParser())
	if err != nil {
		return nil, fmt.Errorf("schedule cannot compute next run: %w", err)
	}
	job.NextRunAt = nextRun
	if err := h.jobStore.Add(job); err != nil {
		return nil, err
	}
	return job, nil
}

func (h *Handler) updateCronJob(peerID string, p cronUpdateParams) (*scheduler.Job, error) {
	if h.jobStore == nil || h.sched == nil {
		return nil, fmt.Errorf("cron is not enabled")
	}
	job, err := h.ownedJobForPeer(peerID, p.ID)
	if err != nil {
		return nil, err
	}
	if p.Name != nil {
		if strings.TrimSpace(*p.Name) == "" {
			return nil, fmt.Errorf("name must not be empty")
		}
		job.Name = *p.Name
	}
	if p.Description != nil {
		job.Description = *p.Description
	}
	if p.Enabled != nil {
		job.Enabled = *p.Enabled
	}
	if p.DeleteAfterRun != nil {
		job.DeleteAfterRun = *p.DeleteAfterRun
	}
	if p.Schedule != nil {
		schedule := normalizeSchedule(*p.Schedule)
		if err := validateSchedule(schedule); err != nil {
			return nil, fmt.Errorf("invalid schedule: %w", err)
		}
		job.Schedule = schedule
		next, err := scheduler.CalcInitialNextRun(job, h.sched.CronParser())
		if err != nil {
			return nil, fmt.Errorf("schedule cannot compute next run: %w", err)
		}
		job.NextRunAt = next
		job.ConsecErrors = 0
	}
	if p.Payload != nil {
		if err := validatePayload(*p.Payload); err != nil {
			return nil, fmt.Errorf("invalid payload: %w", err)
		}
		job.Payload = *p.Payload
	}
	if p.Dispatch != nil {
		job.Dispatch = *p.Dispatch
	}
	if err := h.jobStore.Update(job); err != nil {
		return nil, err
	}
	return job, nil
}

func (h *Handler) ownedJobForPeer(peerID, jobID string) (*scheduler.Job, error) {
	if h.jobStore == nil {
		return nil, fmt.Errorf("cron is not enabled")
	}
	if jobID == "" {
		return nil, fmt.Errorf("id is required")
	}
	job := h.jobStore.Get(jobID)
	if job == nil || job.PeerID != peerID {
		return nil, fmt.Errorf("job not found")
	}
	return job, nil
}

// ── orchestrator tool helpers ─────────────────────────────────────────────────

func (h *Handler) orchestratorStart(ctx context.Context, peerID string, raw json.RawMessage) (*orchestrator.Run, error) {
	if h.orchestrator == nil {
		return nil, fmt.Errorf("orchestrator is not enabled")
	}
	var p struct {
		Tasks []struct {
			Label   string `json:"label"`
			Task    string `json:"task"`
			Model   string `json:"model"`
			Timeout int    `json:"timeout"`
		} `json:"tasks"`
		Aggregation    string `json:"aggregation"`
		ReducerPrompt  string `json:"reducer_prompt"`
		WaitPolicy     string `json:"wait_policy"`
		MaxConcurrency int    `json:"max_concurrency"`
		Timeout        int    `json:"timeout"`
		ChildTimeout   int    `json:"child_timeout"`
		AnnounceStart  bool   `json:"announce_start"`
	}
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, fmt.Errorf("invalid arguments: %w", err)
	}
	if len(p.Tasks) == 0 {
		return nil, fmt.Errorf("tasks must not be empty")
	}
	tasks := make([]orchestrator.ChildTask, len(p.Tasks))
	for i, t := range p.Tasks {
		if strings.TrimSpace(t.Task) == "" {
			return nil, fmt.Errorf("task[%d].task must not be empty", i)
		}
		var to time.Duration
		if t.Timeout > 0 {
			to = time.Duration(t.Timeout) * time.Second
		}
		tasks[i] = orchestrator.ChildTask{
			Label:   t.Label,
			Task:    t.Task,
			Model:   t.Model,
			Timeout: to,
		}
	}
	sessionKey := agent.CurrentSessionKey(ctx)
	if sessionKey == "" {
		sessionKey = peerID
	}
	req := orchestrator.FanOutRequest{
		PeerID:           peerID,
		ParentSessionKey: sessionKey,
		Tasks:            tasks,
		MaxConcurrency:   p.MaxConcurrency,
		WaitPolicy:       orchestrator.WaitPolicy(p.WaitPolicy),
		Aggregation:      orchestrator.AggregationMode(p.Aggregation),
		ReducerPrompt:    p.ReducerPrompt,
		AnnounceStart:    p.AnnounceStart,
	}
	if p.Timeout > 0 {
		req.Timeout = time.Duration(p.Timeout) * time.Second
	}
	if p.ChildTimeout > 0 {
		req.ChildTimeout = time.Duration(p.ChildTimeout) * time.Second
	}
	return h.orchestrator.Start(ctx, req)
}

func (h *Handler) orchestratorStatus(peerID, id string) (*orchestrator.Run, error) {
	if h.orchestrator == nil {
		return nil, fmt.Errorf("orchestrator is not enabled")
	}
	id = strings.TrimSpace(id)
	if id == "" {
		return nil, fmt.Errorf("id is required")
	}
	run, ok := h.orchestrator.Get(id)
	if !ok || run.PeerID != peerID {
		return nil, fmt.Errorf("orchestration %s not found", id)
	}
	return run, nil
}

func (h *Handler) orchestratorWait(ctx context.Context, peerID, id string, timeoutSecs int) (*orchestrator.Run, error) {
	if h.orchestrator == nil {
		return nil, fmt.Errorf("orchestrator is not enabled")
	}
	id = strings.TrimSpace(id)
	if id == "" {
		return nil, fmt.Errorf("id is required")
	}
	// Ownership check before waiting to fail fast.
	run, ok := h.orchestrator.Get(id)
	if !ok || run.PeerID != peerID {
		return nil, fmt.Errorf("orchestration %s not found", id)
	}
	waitCtx := ctx
	if timeoutSecs > 0 {
		var cancel context.CancelFunc
		waitCtx, cancel = context.WithTimeout(ctx, time.Duration(timeoutSecs)*time.Second)
		defer cancel()
	}
	return h.orchestrator.Wait(waitCtx, id)
}

func (h *Handler) orchestratorCancel(peerID, id string) (map[string]string, error) {
	if h.orchestrator == nil {
		return nil, fmt.Errorf("orchestrator is not enabled")
	}
	id = strings.TrimSpace(id)
	if id == "" {
		return nil, fmt.Errorf("id is required")
	}
	// Ownership check.
	run, ok := h.orchestrator.Get(id)
	if !ok || run.PeerID != peerID {
		return nil, fmt.Errorf("orchestration %s not found", id)
	}
	if err := h.orchestrator.Cancel(id); err != nil {
		return nil, err
	}
	return map[string]string{"id": id, "status": "cancelling"}, nil
}

func (h *Handler) orchestratorBarrier(ctx context.Context, peerID, id, group string, waitTimeoutSeconds int) (map[string]any, error) {
	if h.orchestrator == nil {
		return nil, fmt.Errorf("orchestrator is not enabled")
	}
	id = strings.TrimSpace(id)
	if id == "" {
		return nil, fmt.Errorf("id is required")
	}
	// Ownership check.
	run, ok := h.orchestrator.Get(id)
	if !ok || run.PeerID != peerID {
		return nil, fmt.Errorf("orchestration %s not found", id)
	}
	barrierCtx := ctx
	if waitTimeoutSeconds > 0 {
		var cancel context.CancelFunc
		barrierCtx, cancel = context.WithTimeout(ctx, time.Duration(waitTimeoutSeconds)*time.Second)
		defer cancel()
	}
	final, err := h.orchestrator.Barrier(barrierCtx, id, group)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"id":       final.ID,
		"status":   string(final.Status),
		"children": final.Children,
	}, nil
}

func (h *Handler) orchestratorRuns(peerID string) (map[string]any, error) {
	if h.orchestrator == nil {
		return nil, fmt.Errorf("orchestrator is not enabled")
	}
	all := h.orchestrator.List()
	var own []*orchestrator.Run
	for _, r := range all {
		if r.PeerID == peerID {
			own = append(own, r)
		}
	}
	if own == nil {
		own = []*orchestrator.Run{}
	}
	return map[string]any{"runs": own, "count": len(own)}, nil
}
