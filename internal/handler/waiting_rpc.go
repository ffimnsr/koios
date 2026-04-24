package handler

import (
	"context"
	"log/slog"

	"github.com/ffimnsr/koios/internal/tasks"
)

func (h *Handler) rpcWaitingCreate(ctx context.Context, wsc *wsConn, req *rpcRequest) {
	var p struct {
		Title            string `json:"title"`
		Details          string `json:"details"`
		WaitingFor       string `json:"waiting_for"`
		FollowUpAt       int64  `json:"follow_up_at"`
		RemindAt         int64  `json:"remind_at"`
		SourceSessionKey string `json:"source_session_key"`
		SourceExcerpt    string `json:"source_excerpt"`
	}
	if err := decodeParams(req.Params, &p); err != nil {
		wsc.replyErr(req.ID, errCodeInvalidParams, err.Error())
		return
	}
	result, err := h.waitingCreate(wsc.peerID, tasks.WaitingOnInput{
		Title:            p.Title,
		Details:          p.Details,
		WaitingFor:       p.WaitingFor,
		FollowUpAt:       p.FollowUpAt,
		RemindAt:         p.RemindAt,
		SourceSessionKey: p.SourceSessionKey,
		SourceExcerpt:    p.SourceExcerpt,
	}, ctx)
	if err != nil {
		slog.Error("ws: waiting create", "peer", wsc.peerID, "error", err)
		wsc.replyErr(req.ID, errCodeServer, "waiting create error: "+err.Error())
		return
	}
	wsc.reply(req.ID, result)
}

func (h *Handler) rpcWaitingList(ctx context.Context, wsc *wsConn, req *rpcRequest) {
	var p struct {
		Limit  int    `json:"limit,omitempty"`
		Status string `json:"status,omitempty"`
	}
	if err := decodeParams(req.Params, &p); err != nil {
		wsc.replyErr(req.ID, errCodeInvalidParams, err.Error())
		return
	}
	result, err := h.waitingList(wsc.peerID, p.Limit, p.Status, ctx)
	if err != nil {
		slog.Error("ws: waiting list", "peer", wsc.peerID, "error", err)
		wsc.replyErr(req.ID, errCodeServer, "waiting list error: "+err.Error())
		return
	}
	wsc.reply(req.ID, result)
}

func (h *Handler) rpcWaitingGet(ctx context.Context, wsc *wsConn, req *rpcRequest) {
	var p struct {
		ID string `json:"id"`
	}
	if err := decodeParams(req.Params, &p); err != nil {
		wsc.replyErr(req.ID, errCodeInvalidParams, err.Error())
		return
	}
	result, err := h.waitingGet(wsc.peerID, p.ID, ctx)
	if err != nil {
		slog.Error("ws: waiting get", "peer", wsc.peerID, "error", err)
		wsc.replyErr(req.ID, errCodeServer, "waiting get error: "+err.Error())
		return
	}
	wsc.reply(req.ID, result)
}

func (h *Handler) rpcWaitingUpdate(ctx context.Context, wsc *wsConn, req *rpcRequest) {
	var p struct {
		ID               string  `json:"id"`
		Title            *string `json:"title"`
		Details          *string `json:"details"`
		WaitingFor       *string `json:"waiting_for"`
		FollowUpAt       *int64  `json:"follow_up_at"`
		RemindAt         *int64  `json:"remind_at"`
		SnoozeUntil      *int64  `json:"snooze_until"`
		SourceSessionKey *string `json:"source_session_key"`
		SourceExcerpt    *string `json:"source_excerpt"`
	}
	if err := decodeParams(req.Params, &p); err != nil {
		wsc.replyErr(req.ID, errCodeInvalidParams, err.Error())
		return
	}
	result, err := h.waitingUpdate(wsc.peerID, p.ID, waitingPatch(p.Title, p.Details, p.WaitingFor, p.FollowUpAt, p.RemindAt, p.SnoozeUntil, p.SourceSessionKey, p.SourceExcerpt), ctx)
	if err != nil {
		slog.Error("ws: waiting update", "peer", wsc.peerID, "error", err)
		wsc.replyErr(req.ID, errCodeServer, "waiting update error: "+err.Error())
		return
	}
	wsc.reply(req.ID, result)
}

func (h *Handler) rpcWaitingSnooze(ctx context.Context, wsc *wsConn, req *rpcRequest) {
	var p struct {
		ID    string `json:"id"`
		Until int64  `json:"until"`
	}
	if err := decodeParams(req.Params, &p); err != nil {
		wsc.replyErr(req.ID, errCodeInvalidParams, err.Error())
		return
	}
	result, err := h.waitingSnooze(wsc.peerID, p.ID, p.Until, ctx)
	if err != nil {
		slog.Error("ws: waiting snooze", "peer", wsc.peerID, "error", err)
		wsc.replyErr(req.ID, errCodeServer, "waiting snooze error: "+err.Error())
		return
	}
	wsc.reply(req.ID, result)
}

func (h *Handler) rpcWaitingResolve(ctx context.Context, wsc *wsConn, req *rpcRequest) {
	var p struct {
		ID string `json:"id"`
	}
	if err := decodeParams(req.Params, &p); err != nil {
		wsc.replyErr(req.ID, errCodeInvalidParams, err.Error())
		return
	}
	result, err := h.waitingResolve(wsc.peerID, p.ID, ctx)
	if err != nil {
		slog.Error("ws: waiting resolve", "peer", wsc.peerID, "error", err)
		wsc.replyErr(req.ID, errCodeServer, "waiting resolve error: "+err.Error())
		return
	}
	wsc.reply(req.ID, result)
}

func (h *Handler) rpcWaitingReopen(ctx context.Context, wsc *wsConn, req *rpcRequest) {
	var p struct {
		ID string `json:"id"`
	}
	if err := decodeParams(req.Params, &p); err != nil {
		wsc.replyErr(req.ID, errCodeInvalidParams, err.Error())
		return
	}
	result, err := h.waitingReopen(wsc.peerID, p.ID, ctx)
	if err != nil {
		slog.Error("ws: waiting reopen", "peer", wsc.peerID, "error", err)
		wsc.replyErr(req.ID, errCodeServer, "waiting reopen error: "+err.Error())
		return
	}
	wsc.reply(req.ID, result)
}
