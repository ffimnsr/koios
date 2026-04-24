package handler

import (
	"context"
	"log/slog"

	"github.com/ffimnsr/koios/internal/bookmarks"
)

func bookmarkPatch(title, content *string, labels *[]string, reminderAt *int64, sourceKind, sourceSessionKey, sourceRunID *string, sourceStartIndex, sourceEndIndex *int, sourceExcerpt *string) bookmarks.Patch {
	return bookmarks.Patch{
		Title:            title,
		Content:          content,
		Labels:           labels,
		ReminderAt:       reminderAt,
		SourceKind:       sourceKind,
		SourceSessionKey: sourceSessionKey,
		SourceRunID:      sourceRunID,
		SourceStartIndex: sourceStartIndex,
		SourceEndIndex:   sourceEndIndex,
		SourceExcerpt:    sourceExcerpt,
	}
}

func (h *Handler) rpcBookmarkCreate(ctx context.Context, wsc *wsConn, req *rpcRequest) {
	var p struct {
		Title            string   `json:"title"`
		Content          string   `json:"content"`
		Labels           []string `json:"labels"`
		ReminderAt       int64    `json:"reminder_at"`
		SourceKind       string   `json:"source_kind"`
		SourceSessionKey string   `json:"source_session_key"`
		SourceRunID      string   `json:"source_run_id"`
		SourceStartIndex int      `json:"source_start_index"`
		SourceEndIndex   int      `json:"source_end_index"`
		SourceExcerpt    string   `json:"source_excerpt"`
	}
	if err := decodeParams(req.Params, &p); err != nil {
		wsc.replyErr(req.ID, errCodeInvalidParams, err.Error())
		return
	}
	result, err := h.bookmarkCreate(wsc.peerID, bookmarks.Input{
		Title:            p.Title,
		Content:          p.Content,
		Labels:           p.Labels,
		ReminderAt:       p.ReminderAt,
		SourceKind:       p.SourceKind,
		SourceSessionKey: p.SourceSessionKey,
		SourceRunID:      p.SourceRunID,
		SourceStartIndex: p.SourceStartIndex,
		SourceEndIndex:   p.SourceEndIndex,
		SourceExcerpt:    p.SourceExcerpt,
	}, ctx)
	if err != nil {
		slog.Error("ws: bookmark create", "peer", wsc.peerID, "error", err)
		wsc.replyErr(req.ID, errCodeServer, "bookmark create error: "+err.Error())
		return
	}
	wsc.reply(req.ID, result)
}

func (h *Handler) rpcBookmarkCaptureSession(ctx context.Context, wsc *wsConn, req *rpcRequest) {
	var p struct {
		SessionKey string   `json:"session_key"`
		RunID      string   `json:"run_id"`
		Title      string   `json:"title"`
		StartIndex int      `json:"start_index"`
		EndIndex   int      `json:"end_index"`
		Labels     []string `json:"labels"`
		ReminderAt int64    `json:"reminder_at"`
	}
	if err := decodeParams(req.Params, &p); err != nil {
		wsc.replyErr(req.ID, errCodeInvalidParams, err.Error())
		return
	}
	result, err := h.bookmarkCaptureSession(wsc.peerID, p.SessionKey, p.RunID, p.Title, p.StartIndex, p.EndIndex, p.Labels, p.ReminderAt, ctx)
	if err != nil {
		slog.Error("ws: bookmark capture session", "peer", wsc.peerID, "error", err)
		wsc.replyErr(req.ID, errCodeServer, "bookmark capture error: "+err.Error())
		return
	}
	wsc.reply(req.ID, result)
}

func (h *Handler) rpcBookmarkGet(ctx context.Context, wsc *wsConn, req *rpcRequest) {
	var p struct {
		ID string `json:"id"`
	}
	if err := decodeParams(req.Params, &p); err != nil {
		wsc.replyErr(req.ID, errCodeInvalidParams, err.Error())
		return
	}
	result, err := h.bookmarkGet(wsc.peerID, p.ID, ctx)
	if err != nil {
		slog.Error("ws: bookmark get", "peer", wsc.peerID, "error", err)
		wsc.replyErr(req.ID, errCodeServer, "bookmark get error: "+err.Error())
		return
	}
	wsc.reply(req.ID, result)
}

func (h *Handler) rpcBookmarkList(ctx context.Context, wsc *wsConn, req *rpcRequest) {
	var p struct {
		Limit        int    `json:"limit"`
		Label        string `json:"label"`
		UpcomingOnly bool   `json:"upcoming_only"`
	}
	if err := decodeParams(req.Params, &p); err != nil {
		wsc.replyErr(req.ID, errCodeInvalidParams, err.Error())
		return
	}
	result, err := h.bookmarkList(wsc.peerID, p.Limit, p.Label, p.UpcomingOnly, ctx)
	if err != nil {
		slog.Error("ws: bookmark list", "peer", wsc.peerID, "error", err)
		wsc.replyErr(req.ID, errCodeServer, "bookmark list error: "+err.Error())
		return
	}
	wsc.reply(req.ID, result)
}

func (h *Handler) rpcBookmarkSearch(ctx context.Context, wsc *wsConn, req *rpcRequest) {
	var p struct {
		Query string `json:"query"`
		Limit int    `json:"limit"`
	}
	if err := decodeParams(req.Params, &p); err != nil {
		wsc.replyErr(req.ID, errCodeInvalidParams, err.Error())
		return
	}
	result, err := h.bookmarkSearch(wsc.peerID, p.Query, p.Limit, ctx)
	if err != nil {
		slog.Error("ws: bookmark search", "peer", wsc.peerID, "error", err)
		wsc.replyErr(req.ID, errCodeServer, "bookmark search error: "+err.Error())
		return
	}
	wsc.reply(req.ID, result)
}

func (h *Handler) rpcBookmarkUpdate(ctx context.Context, wsc *wsConn, req *rpcRequest) {
	var p struct {
		ID               string    `json:"id"`
		Title            *string   `json:"title"`
		Content          *string   `json:"content"`
		Labels           *[]string `json:"labels"`
		ReminderAt       *int64    `json:"reminder_at"`
		SourceKind       *string   `json:"source_kind"`
		SourceSessionKey *string   `json:"source_session_key"`
		SourceRunID      *string   `json:"source_run_id"`
		SourceStartIndex *int      `json:"source_start_index"`
		SourceEndIndex   *int      `json:"source_end_index"`
		SourceExcerpt    *string   `json:"source_excerpt"`
	}
	if err := decodeParams(req.Params, &p); err != nil {
		wsc.replyErr(req.ID, errCodeInvalidParams, err.Error())
		return
	}
	result, err := h.bookmarkUpdate(wsc.peerID, p.ID, bookmarkPatch(p.Title, p.Content, p.Labels, p.ReminderAt, p.SourceKind, p.SourceSessionKey, p.SourceRunID, p.SourceStartIndex, p.SourceEndIndex, p.SourceExcerpt), ctx)
	if err != nil {
		slog.Error("ws: bookmark update", "peer", wsc.peerID, "error", err)
		wsc.replyErr(req.ID, errCodeServer, "bookmark update error: "+err.Error())
		return
	}
	wsc.reply(req.ID, result)
}

func (h *Handler) rpcBookmarkDelete(ctx context.Context, wsc *wsConn, req *rpcRequest) {
	var p struct {
		ID string `json:"id"`
	}
	if err := decodeParams(req.Params, &p); err != nil {
		wsc.replyErr(req.ID, errCodeInvalidParams, err.Error())
		return
	}
	result, err := h.bookmarkDelete(wsc.peerID, p.ID, ctx)
	if err != nil {
		slog.Error("ws: bookmark delete", "peer", wsc.peerID, "error", err)
		wsc.replyErr(req.ID, errCodeServer, "bookmark delete error: "+err.Error())
		return
	}
	wsc.reply(req.ID, result)
}
