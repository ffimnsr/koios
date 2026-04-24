package handler

import (
	"context"
	"log/slog"

	"github.com/ffimnsr/koios/internal/tasks"
)

func (h *Handler) rpcTaskCandidateCreate(ctx context.Context, wsc *wsConn, req *rpcRequest) {
	var p struct {
		Title   string `json:"title"`
		Details string `json:"details"`
		Owner   string `json:"owner"`
		DueAt   int64  `json:"due_at"`
	}
	if err := decodeParams(req.Params, &p); err != nil {
		wsc.replyErr(req.ID, errCodeInvalidParams, err.Error())
		return
	}
	result, err := h.taskCandidateCreate(wsc.peerID, tasks.CandidateInput{Title: p.Title, Details: p.Details, Owner: p.Owner, DueAt: p.DueAt}, ctx)
	if err != nil {
		slog.Error("ws: task candidate create", "peer", wsc.peerID, "error", err)
		wsc.replyErr(req.ID, errCodeServer, "task candidate create error: "+err.Error())
		return
	}
	wsc.reply(req.ID, result)
}

func (h *Handler) rpcTaskCandidateExtract(ctx context.Context, wsc *wsConn, req *rpcRequest) {
	var p struct {
		Text             string `json:"text"`
		CaptureKind      string `json:"capture_kind"`
		SourceSessionKey string `json:"source_session_key"`
		SourceExcerpt    string `json:"source_excerpt"`
	}
	if err := decodeParams(req.Params, &p); err != nil {
		wsc.replyErr(req.ID, errCodeInvalidParams, err.Error())
		return
	}
	result, err := h.taskCandidateExtract(wsc.peerID, p.Text, tasks.CandidateProvenance{
		CaptureKind:      p.CaptureKind,
		SourceSessionKey: p.SourceSessionKey,
		SourceExcerpt:    p.SourceExcerpt,
	}, ctx)
	if err != nil {
		slog.Error("ws: task candidate extract", "peer", wsc.peerID, "error", err)
		wsc.replyErr(req.ID, errCodeServer, "task candidate extract error: "+err.Error())
		return
	}
	wsc.reply(req.ID, result)
}

func (h *Handler) rpcTaskCandidateList(ctx context.Context, wsc *wsConn, req *rpcRequest) {
	var p struct {
		Limit  int    `json:"limit,omitempty"`
		Status string `json:"status,omitempty"`
	}
	if err := decodeParams(req.Params, &p); err != nil {
		wsc.replyErr(req.ID, errCodeInvalidParams, err.Error())
		return
	}
	result, err := h.taskCandidateList(wsc.peerID, p.Limit, p.Status, ctx)
	if err != nil {
		slog.Error("ws: task candidate list", "peer", wsc.peerID, "error", err)
		wsc.replyErr(req.ID, errCodeServer, "task candidate list error: "+err.Error())
		return
	}
	wsc.reply(req.ID, result)
}

func (h *Handler) rpcTaskCandidateEdit(ctx context.Context, wsc *wsConn, req *rpcRequest) {
	var p struct {
		ID      string  `json:"id"`
		Title   *string `json:"title"`
		Details *string `json:"details"`
		Owner   *string `json:"owner"`
		DueAt   *int64  `json:"due_at"`
	}
	if err := decodeParams(req.Params, &p); err != nil {
		wsc.replyErr(req.ID, errCodeInvalidParams, err.Error())
		return
	}
	result, err := h.taskCandidateEdit(wsc.peerID, p.ID, taskCandidatePatch(p.Title, p.Details, p.Owner, p.DueAt), ctx)
	if err != nil {
		slog.Error("ws: task candidate edit", "peer", wsc.peerID, "error", err)
		wsc.replyErr(req.ID, errCodeServer, "task candidate edit error: "+err.Error())
		return
	}
	wsc.reply(req.ID, result)
}

func (h *Handler) rpcTaskCandidateApprove(ctx context.Context, wsc *wsConn, req *rpcRequest) {
	var p struct {
		ID      string  `json:"id"`
		Reason  string  `json:"reason"`
		Title   *string `json:"title"`
		Details *string `json:"details"`
		Owner   *string `json:"owner"`
		DueAt   *int64  `json:"due_at"`
	}
	if err := decodeParams(req.Params, &p); err != nil {
		wsc.replyErr(req.ID, errCodeInvalidParams, err.Error())
		return
	}
	result, err := h.taskCandidateApprove(wsc.peerID, p.ID, taskCandidatePatch(p.Title, p.Details, p.Owner, p.DueAt), p.Reason, ctx)
	if err != nil {
		slog.Error("ws: task candidate approve", "peer", wsc.peerID, "error", err)
		wsc.replyErr(req.ID, errCodeServer, "task candidate approve error: "+err.Error())
		return
	}
	wsc.reply(req.ID, result)
}

func (h *Handler) rpcTaskCandidateReject(ctx context.Context, wsc *wsConn, req *rpcRequest) {
	var p struct {
		ID     string `json:"id"`
		Reason string `json:"reason"`
	}
	if err := decodeParams(req.Params, &p); err != nil {
		wsc.replyErr(req.ID, errCodeInvalidParams, err.Error())
		return
	}
	result, err := h.taskCandidateReject(wsc.peerID, p.ID, p.Reason, ctx)
	if err != nil {
		slog.Error("ws: task candidate reject", "peer", wsc.peerID, "error", err)
		wsc.replyErr(req.ID, errCodeServer, "task candidate reject error: "+err.Error())
		return
	}
	wsc.reply(req.ID, result)
}

func (h *Handler) rpcTaskList(ctx context.Context, wsc *wsConn, req *rpcRequest) {
	var p struct {
		Limit  int    `json:"limit,omitempty"`
		Status string `json:"status,omitempty"`
	}
	if err := decodeParams(req.Params, &p); err != nil {
		wsc.replyErr(req.ID, errCodeInvalidParams, err.Error())
		return
	}
	result, err := h.taskList(wsc.peerID, p.Limit, p.Status, ctx)
	if err != nil {
		slog.Error("ws: task list", "peer", wsc.peerID, "error", err)
		wsc.replyErr(req.ID, errCodeServer, "task list error: "+err.Error())
		return
	}
	wsc.reply(req.ID, result)
}

func (h *Handler) rpcTaskGet(ctx context.Context, wsc *wsConn, req *rpcRequest) {
	var p struct {
		ID string `json:"id"`
	}
	if err := decodeParams(req.Params, &p); err != nil {
		wsc.replyErr(req.ID, errCodeInvalidParams, err.Error())
		return
	}
	result, err := h.taskGet(wsc.peerID, p.ID, ctx)
	if err != nil {
		slog.Error("ws: task get", "peer", wsc.peerID, "error", err)
		wsc.replyErr(req.ID, errCodeServer, "task get error: "+err.Error())
		return
	}
	wsc.reply(req.ID, result)
}

func (h *Handler) rpcTaskUpdate(ctx context.Context, wsc *wsConn, req *rpcRequest) {
	var p struct {
		ID          string  `json:"id"`
		Title       *string `json:"title"`
		Details     *string `json:"details"`
		Owner       *string `json:"owner"`
		DueAt       *int64  `json:"due_at"`
		SnoozeUntil *int64  `json:"snooze_until"`
	}
	if err := decodeParams(req.Params, &p); err != nil {
		wsc.replyErr(req.ID, errCodeInvalidParams, err.Error())
		return
	}
	result, err := h.taskUpdate(wsc.peerID, p.ID, taskPatch(p.Title, p.Details, p.Owner, p.DueAt, p.SnoozeUntil), ctx)
	if err != nil {
		slog.Error("ws: task update", "peer", wsc.peerID, "error", err)
		wsc.replyErr(req.ID, errCodeServer, "task update error: "+err.Error())
		return
	}
	wsc.reply(req.ID, result)
}

func (h *Handler) rpcTaskAssign(ctx context.Context, wsc *wsConn, req *rpcRequest) {
	var p struct {
		ID    string `json:"id"`
		Owner string `json:"owner"`
	}
	if err := decodeParams(req.Params, &p); err != nil {
		wsc.replyErr(req.ID, errCodeInvalidParams, err.Error())
		return
	}
	result, err := h.taskAssign(wsc.peerID, p.ID, p.Owner, ctx)
	if err != nil {
		slog.Error("ws: task assign", "peer", wsc.peerID, "error", err)
		wsc.replyErr(req.ID, errCodeServer, "task assign error: "+err.Error())
		return
	}
	wsc.reply(req.ID, result)
}

func (h *Handler) rpcTaskSnooze(ctx context.Context, wsc *wsConn, req *rpcRequest) {
	var p struct {
		ID    string `json:"id"`
		Until int64  `json:"until"`
	}
	if err := decodeParams(req.Params, &p); err != nil {
		wsc.replyErr(req.ID, errCodeInvalidParams, err.Error())
		return
	}
	result, err := h.taskSnooze(wsc.peerID, p.ID, p.Until, ctx)
	if err != nil {
		slog.Error("ws: task snooze", "peer", wsc.peerID, "error", err)
		wsc.replyErr(req.ID, errCodeServer, "task snooze error: "+err.Error())
		return
	}
	wsc.reply(req.ID, result)
}

func (h *Handler) rpcTaskComplete(ctx context.Context, wsc *wsConn, req *rpcRequest) {
	var p struct {
		ID string `json:"id"`
	}
	if err := decodeParams(req.Params, &p); err != nil {
		wsc.replyErr(req.ID, errCodeInvalidParams, err.Error())
		return
	}
	result, err := h.taskComplete(wsc.peerID, p.ID, ctx)
	if err != nil {
		slog.Error("ws: task complete", "peer", wsc.peerID, "error", err)
		wsc.replyErr(req.ID, errCodeServer, "task complete error: "+err.Error())
		return
	}
	wsc.reply(req.ID, result)
}

func (h *Handler) rpcTaskReopen(ctx context.Context, wsc *wsConn, req *rpcRequest) {
	var p struct {
		ID string `json:"id"`
	}
	if err := decodeParams(req.Params, &p); err != nil {
		wsc.replyErr(req.ID, errCodeInvalidParams, err.Error())
		return
	}
	result, err := h.taskReopen(wsc.peerID, p.ID, ctx)
	if err != nil {
		slog.Error("ws: task reopen", "peer", wsc.peerID, "error", err)
		wsc.replyErr(req.ID, errCodeServer, "task reopen error: "+err.Error())
		return
	}
	wsc.reply(req.ID, result)
}
