package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/ffimnsr/koios/internal/scheduler"
)

// ── cron ──────────────────────────────────────────────────────────────────────

func (h *Handler) rpcCronList(_ context.Context, wsc *wsConn, req *rpcRequest) {
	jobs := h.jobStore.List(wsc.peerID)
	if jobs == nil {
		jobs = []*scheduler.Job{}
	}
	wsc.reply(req.ID, jobs)
}

type cronCreateParams struct {
	Name           string                   `json:"name"`
	Description    string                   `json:"description,omitempty"`
	Schedule       scheduler.Schedule       `json:"schedule"`
	Payload        scheduler.Payload        `json:"payload"`
	Dispatch       scheduler.DispatchPolicy `json:"dispatch,omitempty"`
	Enabled        *bool                    `json:"enabled,omitempty"`
	DeleteAfterRun *bool                    `json:"delete_after_run,omitempty"`
}

func (h *Handler) rpcCronCreate(_ context.Context, wsc *wsConn, req *rpcRequest) {
	var p cronCreateParams
	if err := decodeParams(req.Params, &p); err != nil {
		wsc.replyErr(req.ID, errCodeInvalidParams, err.Error())
		return
	}
	if strings.TrimSpace(p.Name) == "" {
		wsc.replyErr(req.ID, errCodeInvalidParams, "name is required")
		return
	}
	if err := validateSchedule(p.Schedule); err != nil {
		wsc.replyErr(req.ID, errCodeInvalidParams, "invalid schedule: "+err.Error())
		return
	}
	if err := validatePayload(p.Payload); err != nil {
		wsc.replyErr(req.ID, errCodeInvalidParams, "invalid payload: "+err.Error())
		return
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
		PeerID:         wsc.peerID,
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
		wsc.replyErr(req.ID, errCodeInvalidParams, "schedule cannot compute next run: "+err.Error())
		return
	}
	job.NextRunAt = nextRun
	if err := h.jobStore.Add(job); err != nil {
		slog.Error("ws: cron add", "peer", wsc.peerID, "error", err)
		wsc.replyErr(req.ID, errCodeServer, "error saving job")
		return
	}
	slog.Info("ws: cron created", "peer", wsc.peerID, "job", job.JobID)
	wsc.reply(req.ID, job)
}

func (h *Handler) rpcCronGet(_ context.Context, wsc *wsConn, req *rpcRequest) {
	var p struct {
		ID string `json:"id"`
	}
	if err := decodeParams(req.Params, &p); err != nil {
		wsc.replyErr(req.ID, errCodeInvalidParams, err.Error())
		return
	}
	job := h.ownedJob(wsc, req.ID, p.ID)
	if job == nil {
		return
	}
	wsc.reply(req.ID, job)
}

type cronUpdateParams struct {
	ID             string                    `json:"id"`
	Name           *string                   `json:"name,omitempty"`
	Description    *string                   `json:"description,omitempty"`
	Enabled        *bool                     `json:"enabled,omitempty"`
	Schedule       *scheduler.Schedule       `json:"schedule,omitempty"`
	Payload        *scheduler.Payload        `json:"payload,omitempty"`
	Dispatch       *scheduler.DispatchPolicy `json:"dispatch,omitempty"`
	DeleteAfterRun *bool                     `json:"delete_after_run,omitempty"`
}

func (h *Handler) rpcCronUpdate(_ context.Context, wsc *wsConn, req *rpcRequest) {
	var p cronUpdateParams
	if err := decodeParams(req.Params, &p); err != nil {
		wsc.replyErr(req.ID, errCodeInvalidParams, err.Error())
		return
	}
	job := h.ownedJob(wsc, req.ID, p.ID)
	if job == nil {
		return
	}
	if p.Name != nil {
		if strings.TrimSpace(*p.Name) == "" {
			wsc.replyErr(req.ID, errCodeInvalidParams, "name must not be empty")
			return
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
		if err := validateSchedule(*p.Schedule); err != nil {
			wsc.replyErr(req.ID, errCodeInvalidParams, "invalid schedule: "+err.Error())
			return
		}
		job.Schedule = *p.Schedule
		next, err := scheduler.CalcInitialNextRun(job, h.sched.CronParser())
		if err != nil {
			wsc.replyErr(req.ID, errCodeInvalidParams, "schedule cannot compute next run: "+err.Error())
			return
		}
		job.NextRunAt = next
		job.ConsecErrors = 0
	}
	if p.Payload != nil {
		if err := validatePayload(*p.Payload); err != nil {
			wsc.replyErr(req.ID, errCodeInvalidParams, "invalid payload: "+err.Error())
			return
		}
		job.Payload = *p.Payload
	}
	if p.Dispatch != nil {
		job.Dispatch = *p.Dispatch
	}
	if err := h.jobStore.Update(job); err != nil {
		slog.Error("ws: cron update", "job", p.ID, "error", err)
		wsc.replyErr(req.ID, errCodeServer, "error updating job")
		return
	}
	slog.Info("ws: cron updated", "peer", wsc.peerID, "job", p.ID)
	wsc.reply(req.ID, job)
}

func (h *Handler) rpcCronDelete(_ context.Context, wsc *wsConn, req *rpcRequest) {
	var p struct {
		ID string `json:"id"`
	}
	if err := decodeParams(req.Params, &p); err != nil {
		wsc.replyErr(req.ID, errCodeInvalidParams, err.Error())
		return
	}
	if h.ownedJob(wsc, req.ID, p.ID) == nil {
		return
	}
	if err := h.jobStore.Remove(p.ID); err != nil {
		slog.Error("ws: cron delete", "job", p.ID, "error", err)
		wsc.replyErr(req.ID, errCodeServer, "error removing job")
		return
	}
	slog.Info("ws: cron deleted", "peer", wsc.peerID, "job", p.ID)
	wsc.reply(req.ID, map[string]bool{"ok": true})
}

func (h *Handler) rpcCronTrigger(_ context.Context, wsc *wsConn, req *rpcRequest) {
	var p struct {
		ID string `json:"id"`
	}
	if err := decodeParams(req.Params, &p); err != nil {
		wsc.replyErr(req.ID, errCodeInvalidParams, err.Error())
		return
	}
	if h.ownedJob(wsc, req.ID, p.ID) == nil {
		return
	}
	runID, err := h.sched.TriggerRun(p.ID)
	if err != nil {
		slog.Error("ws: cron trigger", "job", p.ID, "error", err)
		wsc.replyErr(req.ID, errCodeServer, "trigger failed: "+err.Error())
		return
	}
	wsc.reply(req.ID, map[string]any{"ok": true, "run_id": runID})
}

func (h *Handler) rpcCronRuns(_ context.Context, wsc *wsConn, req *rpcRequest) {
	var p struct {
		ID    string `json:"id"`
		Limit int    `json:"limit,omitempty"`
	}
	if err := decodeParams(req.Params, &p); err != nil {
		wsc.replyErr(req.ID, errCodeInvalidParams, err.Error())
		return
	}
	if h.ownedJob(wsc, req.ID, p.ID) == nil {
		return
	}
	limit := p.Limit
	if limit <= 0 {
		limit = 50
	}
	if limit > 1000 {
		limit = 1000
	}
	records, err := h.jobStore.ReadRunRecords(p.ID, limit)
	if err != nil {
		slog.Error("ws: cron runs", "job", p.ID, "error", err)
		wsc.replyErr(req.ID, errCodeServer, "error reading run log")
		return
	}
	if records == nil {
		records = []scheduler.RunRecord{}
	}
	wsc.reply(req.ID, records)
}

// ownedJob fetches a job and verifies it belongs to the requesting peer.
// Returns nil and sends an error response when the job is absent or unowned.
// Returns a generic "not found" to avoid leaking other peers' job IDs.
func (h *Handler) ownedJob(wsc *wsConn, reqID json.RawMessage, jobID string) *scheduler.Job {
	if jobID == "" {
		wsc.replyErr(reqID, errCodeInvalidParams, "id is required")
		return nil
	}
	job := h.jobStore.Get(jobID)
	if job == nil || job.PeerID != wsc.peerID {
		wsc.replyErr(reqID, errCodeServer, "job not found")
		return nil
	}
	return job
}

// ── cron validation helpers ───────────────────────────────────────────────────

func validateSchedule(s scheduler.Schedule) error {
	switch s.Kind {
	case scheduler.KindAt:
		if s.At == "" {
			return fmt.Errorf("schedule.at is required for kind=at")
		}
		if _, err := time.Parse(time.RFC3339, s.At); err != nil {
			if _, err2 := time.Parse("2006-01-02T15:04:05", s.At); err2 != nil {
				return fmt.Errorf("schedule.at %q is not a valid ISO 8601 timestamp", s.At)
			}
		}
	case scheduler.KindEvery:
		if s.EveryMs <= 0 {
			return fmt.Errorf("schedule.every_ms must be > 0")
		}
	case scheduler.KindCron:
		if s.Expr == "" {
			return fmt.Errorf("schedule.expr is required for kind=cron")
		}
		if s.Tz != "" {
			if _, err := time.LoadLocation(s.Tz); err != nil {
				return fmt.Errorf("schedule.tz %q is not a valid IANA timezone", s.Tz)
			}
		}
	default:
		return fmt.Errorf("schedule.kind must be one of: at, every, cron")
	}
	return nil
}

func validatePayload(p scheduler.Payload) error {
	switch p.Kind {
	case scheduler.PayloadSystemEvent:
		if strings.TrimSpace(p.Text) == "" {
			return fmt.Errorf("payload.text is required for kind=systemEvent")
		}
	case scheduler.PayloadAgentTurn:
		if strings.TrimSpace(p.Message) == "" {
			return fmt.Errorf("payload.message is required for kind=agentTurn")
		}
	default:
		return fmt.Errorf("payload.kind must be one of: systemEvent, agentTurn")
	}
	return nil
}
