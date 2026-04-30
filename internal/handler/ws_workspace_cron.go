package handler

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"time"

	"github.com/ffimnsr/koios/internal/heartbeat"
	"github.com/ffimnsr/koios/internal/scheduler"
)

func (h *Handler) rpcWorkspaceList(_ context.Context, wsc *wsConn, req *rpcRequest) {
	var p struct {
		Path      string `json:"path,omitempty"`
		Recursive bool   `json:"recursive,omitempty"`
		Limit     int    `json:"limit,omitempty"`
	}
	if err := decodeParams(req.Params, &p); err != nil {
		wsc.replyErr(req.ID, errCodeInvalidParams, err.Error())
		return
	}
	if strings.TrimSpace(p.Path) == "" {
		p.Path = "."
	}
	result, err := h.workspaceList(wsc.peerID, p.Path, p.Recursive, p.Limit)
	if err != nil {
		wsc.replyErr(req.ID, errCodeServer, err.Error())
		return
	}
	wsc.reply(req.ID, result)
}

func (h *Handler) rpcWorkspaceRead(_ context.Context, wsc *wsConn, req *rpcRequest) {
	var p struct {
		Path      string `json:"path"`
		StartLine int    `json:"start_line,omitempty"`
		EndLine   int    `json:"end_line,omitempty"`
	}
	if err := decodeParams(req.Params, &p); err != nil {
		wsc.replyErr(req.ID, errCodeInvalidParams, err.Error())
		return
	}
	if strings.TrimSpace(p.Path) == "" {
		wsc.replyErr(req.ID, errCodeInvalidParams, "path is required")
		return
	}
	result, err := h.workspaceRead(wsc.peerID, p.Path, p.StartLine, p.EndLine)
	if err != nil {
		wsc.replyErr(req.ID, errCodeServer, err.Error())
		return
	}
	wsc.reply(req.ID, result)
}

func (h *Handler) rpcWorkspaceHead(_ context.Context, wsc *wsConn, req *rpcRequest) {
	var p struct {
		Path  string `json:"path"`
		Lines int    `json:"lines,omitempty"`
	}
	if err := decodeParams(req.Params, &p); err != nil {
		wsc.replyErr(req.ID, errCodeInvalidParams, err.Error())
		return
	}
	if p.Lines == 0 {
		p.Lines = 10
	}
	result, err := h.workspaceHead(wsc.peerID, p.Path, p.Lines)
	if err != nil {
		wsc.replyErr(req.ID, errCodeServer, err.Error())
		return
	}
	wsc.reply(req.ID, result)
}

func (h *Handler) rpcWorkspaceTail(_ context.Context, wsc *wsConn, req *rpcRequest) {
	var p struct {
		Path  string `json:"path"`
		Lines int    `json:"lines,omitempty"`
	}
	if err := decodeParams(req.Params, &p); err != nil {
		wsc.replyErr(req.ID, errCodeInvalidParams, err.Error())
		return
	}
	if p.Lines == 0 {
		p.Lines = 10
	}
	result, err := h.workspaceTail(wsc.peerID, p.Path, p.Lines)
	if err != nil {
		wsc.replyErr(req.ID, errCodeServer, err.Error())
		return
	}
	wsc.reply(req.ID, result)
}

func (h *Handler) rpcWorkspaceGrep(_ context.Context, wsc *wsConn, req *rpcRequest) {
	var p struct {
		Path          string `json:"path,omitempty"`
		Pattern       string `json:"pattern"`
		Recursive     *bool  `json:"recursive,omitempty"`
		Limit         int    `json:"limit,omitempty"`
		CaseSensitive *bool  `json:"case_sensitive,omitempty"`
		Regexp        bool   `json:"regexp,omitempty"`
	}
	if err := decodeParams(req.Params, &p); err != nil {
		wsc.replyErr(req.ID, errCodeInvalidParams, err.Error())
		return
	}
	recursive := true
	if p.Recursive != nil {
		recursive = *p.Recursive
	}
	caseSensitive := true
	if p.CaseSensitive != nil {
		caseSensitive = *p.CaseSensitive
	}
	result, err := h.workspaceGrep(wsc.peerID, p.Path, p.Pattern, recursive, p.Limit, caseSensitive, p.Regexp)
	if err != nil {
		wsc.replyErr(req.ID, errCodeServer, err.Error())
		return
	}
	wsc.reply(req.ID, result)
}

func (h *Handler) rpcWorkspaceFind(_ context.Context, wsc *wsConn, req *rpcRequest) {
	var p struct {
		Path          string `json:"path,omitempty"`
		Pattern       string `json:"pattern"`
		Recursive     *bool  `json:"recursive,omitempty"`
		Limit         int    `json:"limit,omitempty"`
		CaseSensitive *bool  `json:"case_sensitive,omitempty"`
		Regexp        bool   `json:"regexp,omitempty"`
	}
	if err := decodeParams(req.Params, &p); err != nil {
		wsc.replyErr(req.ID, errCodeInvalidParams, err.Error())
		return
	}
	recursive := true
	if p.Recursive != nil {
		recursive = *p.Recursive
	}
	caseSensitive := true
	if p.CaseSensitive != nil {
		caseSensitive = *p.CaseSensitive
	}
	result, err := h.workspaceFind(wsc.peerID, p.Path, p.Pattern, recursive, p.Limit, caseSensitive, p.Regexp)
	if err != nil {
		wsc.replyErr(req.ID, errCodeServer, err.Error())
		return
	}
	wsc.reply(req.ID, result)
}

func (h *Handler) rpcWorkspaceSort(_ context.Context, wsc *wsConn, req *rpcRequest) {
	var p struct {
		Path          string `json:"path"`
		Reverse       bool   `json:"reverse,omitempty"`
		CaseSensitive *bool  `json:"case_sensitive,omitempty"`
	}
	if err := decodeParams(req.Params, &p); err != nil {
		wsc.replyErr(req.ID, errCodeInvalidParams, err.Error())
		return
	}
	caseSensitive := true
	if p.CaseSensitive != nil {
		caseSensitive = *p.CaseSensitive
	}
	result, err := h.workspaceSort(wsc.peerID, p.Path, p.Reverse, caseSensitive)
	if err != nil {
		wsc.replyErr(req.ID, errCodeServer, err.Error())
		return
	}
	wsc.reply(req.ID, result)
}

func (h *Handler) rpcWorkspaceUniq(_ context.Context, wsc *wsConn, req *rpcRequest) {
	var p struct {
		Path          string `json:"path"`
		Count         bool   `json:"count,omitempty"`
		CaseSensitive *bool  `json:"case_sensitive,omitempty"`
	}
	if err := decodeParams(req.Params, &p); err != nil {
		wsc.replyErr(req.ID, errCodeInvalidParams, err.Error())
		return
	}
	caseSensitive := true
	if p.CaseSensitive != nil {
		caseSensitive = *p.CaseSensitive
	}
	result, err := h.workspaceUniq(wsc.peerID, p.Path, p.Count, caseSensitive)
	if err != nil {
		wsc.replyErr(req.ID, errCodeServer, err.Error())
		return
	}
	wsc.reply(req.ID, result)
}

func (h *Handler) rpcWorkspaceDiff(_ context.Context, wsc *wsConn, req *rpcRequest) {
	var p struct {
		Path      string `json:"path"`
		OtherPath string `json:"other_path,omitempty"`
		Content   string `json:"content,omitempty"`
		Context   int    `json:"context,omitempty"`
	}
	if err := decodeParams(req.Params, &p); err != nil {
		wsc.replyErr(req.ID, errCodeInvalidParams, err.Error())
		return
	}
	if p.Context == 0 {
		p.Context = 3
	}
	result, err := h.workspaceDiff(wsc.peerID, p.Path, p.OtherPath, p.Content, p.Context)
	if err != nil {
		wsc.replyErr(req.ID, errCodeServer, err.Error())
		return
	}
	wsc.reply(req.ID, result)
}

func (h *Handler) rpcWorkspaceWrite(_ context.Context, wsc *wsConn, req *rpcRequest) {
	var p struct {
		Path    string `json:"path"`
		Content string `json:"content"`
		Append  bool   `json:"append,omitempty"`
	}
	if err := decodeParams(req.Params, &p); err != nil {
		wsc.replyErr(req.ID, errCodeInvalidParams, err.Error())
		return
	}
	if strings.TrimSpace(p.Path) == "" {
		wsc.replyErr(req.ID, errCodeInvalidParams, "path is required")
		return
	}
	result, err := h.workspaceWrite(wsc.peerID, p.Path, p.Content, p.Append)
	if err != nil {
		wsc.replyErr(req.ID, errCodeServer, err.Error())
		return
	}
	wsc.reply(req.ID, result)
}

func (h *Handler) rpcWorkspaceEdit(_ context.Context, wsc *wsConn, req *rpcRequest) {
	var p struct {
		Path       string `json:"path"`
		OldText    string `json:"old_text"`
		NewText    string `json:"new_text"`
		ReplaceAll bool   `json:"replace_all,omitempty"`
	}
	if err := decodeParams(req.Params, &p); err != nil {
		wsc.replyErr(req.ID, errCodeInvalidParams, err.Error())
		return
	}
	result, err := h.workspaceEdit(wsc.peerID, p.Path, p.OldText, p.NewText, p.ReplaceAll)
	if err != nil {
		wsc.replyErr(req.ID, errCodeServer, err.Error())
		return
	}
	wsc.reply(req.ID, result)
}

func (h *Handler) rpcWorkspaceMkdir(_ context.Context, wsc *wsConn, req *rpcRequest) {
	var p struct {
		Path string `json:"path"`
	}
	if err := decodeParams(req.Params, &p); err != nil {
		wsc.replyErr(req.ID, errCodeInvalidParams, err.Error())
		return
	}
	if strings.TrimSpace(p.Path) == "" {
		wsc.replyErr(req.ID, errCodeInvalidParams, "path is required")
		return
	}
	result, err := h.workspaceMkdir(wsc.peerID, p.Path)
	if err != nil {
		wsc.replyErr(req.ID, errCodeServer, err.Error())
		return
	}
	wsc.reply(req.ID, result)
}

func (h *Handler) rpcWorkspaceDelete(_ context.Context, wsc *wsConn, req *rpcRequest) {
	var p struct {
		Path      string `json:"path"`
		Recursive bool   `json:"recursive,omitempty"`
	}
	if err := decodeParams(req.Params, &p); err != nil {
		wsc.replyErr(req.ID, errCodeInvalidParams, err.Error())
		return
	}
	if strings.TrimSpace(p.Path) == "" {
		wsc.replyErr(req.ID, errCodeInvalidParams, "path is required")
		return
	}
	result, err := h.workspaceDelete(wsc.peerID, p.Path, p.Recursive)
	if err != nil {
		wsc.replyErr(req.ID, errCodeServer, err.Error())
		return
	}
	wsc.reply(req.ID, result)
}

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

// ── heartbeat ─────────────────────────────────────────────────────────────────

func (h *Handler) rpcHeartbeatGet(_ context.Context, wsc *wsConn, req *rpcRequest) {
	cfg, err := h.hbConfigStore.GetOrDefault(wsc.peerID, h.hbDefaultEvery)
	if err != nil {
		slog.Error("ws: heartbeat get", "peer", wsc.peerID, "error", err)
		wsc.replyErr(req.ID, errCodeServer, "error loading config")
		return
	}
	wsc.reply(req.ID, cfg)
}

type heartbeatSetParams struct {
	Enabled     *bool                  `json:"enabled,omitempty"`
	Every       *string                `json:"every,omitempty"` // Go duration string, e.g. "30m"
	Prompt      *string                `json:"prompt,omitempty"`
	AckMaxChars *int                   `json:"ack_max_chars,omitempty"`
	ActiveHours *heartbeat.ActiveHours `json:"active_hours,omitempty"`
}

func (h *Handler) rpcHeartbeatSet(_ context.Context, wsc *wsConn, req *rpcRequest) {
	current, err := h.hbConfigStore.GetOrDefault(wsc.peerID, h.hbDefaultEvery)
	if err != nil {
		slog.Error("ws: heartbeat set load", "peer", wsc.peerID, "error", err)
		wsc.replyErr(req.ID, errCodeServer, "error loading config")
		return
	}
	var p heartbeatSetParams
	if err := decodeParams(req.Params, &p); err != nil {
		wsc.replyErr(req.ID, errCodeInvalidParams, err.Error())
		return
	}
	if p.Enabled != nil {
		current.Enabled = *p.Enabled
	}
	if p.Every != nil {
		d, err := time.ParseDuration(*p.Every)
		if err != nil {
			wsc.replyErr(req.ID, errCodeInvalidParams, "invalid every: must be a Go duration (e.g. 30m, 1h)")
			return
		}
		if d < 0 {
			wsc.replyErr(req.ID, errCodeInvalidParams, "every must be non-negative")
			return
		}
		current.Every = d
	}
	if p.Prompt != nil {
		current.Prompt = *p.Prompt
	}
	if p.AckMaxChars != nil {
		if *p.AckMaxChars < 0 {
			wsc.replyErr(req.ID, errCodeInvalidParams, "ack_max_chars must be >= 0")
			return
		}
		current.AckMaxChars = *p.AckMaxChars
	}
	if p.ActiveHours != nil {
		current.ActiveHours = p.ActiveHours
	}
	if err := h.hbRunner.SetConfig(wsc.peerID, current); err != nil {
		slog.Error("ws: heartbeat set save", "peer", wsc.peerID, "error", err)
		wsc.replyErr(req.ID, errCodeServer, "error saving config")
		return
	}
	slog.Info("ws: heartbeat config updated", "peer", wsc.peerID, "enabled", current.Enabled, "every", current.Every)
	wsc.reply(req.ID, current)
}

func (h *Handler) rpcHeartbeatWake(_ context.Context, wsc *wsConn, req *rpcRequest) {
	h.hbRunner.WakePeer(wsc.peerID)
	slog.Info("ws: heartbeat wake", "peer", wsc.peerID)
	wsc.reply(req.ID, map[string]bool{"ok": true})
}

// ── wsStreamWriter ────────────────────────────────────────────────────────────

// wsStreamWriter implements http.ResponseWriter and http.Flusher so that the
// provider's CompleteStream can write SSE bytes to it.  Each complete SSE data
// line is forwarded as a stream.delta WebSocket notification.
