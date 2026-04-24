package handler

import (
	"context"
	"log/slog"

	"github.com/ffimnsr/koios/internal/calendar"
)

func (h *Handler) rpcCalendarSourceCreate(ctx context.Context, wsc *wsConn, req *rpcRequest) {
	var p struct {
		Name     string `json:"name"`
		Path     string `json:"path"`
		URL      string `json:"url"`
		Timezone string `json:"timezone"`
		Enabled  *bool  `json:"enabled"`
	}
	if err := decodeParams(req.Params, &p); err != nil {
		wsc.replyErr(req.ID, errCodeInvalidParams, err.Error())
		return
	}
	result, err := h.calendarSourceCreate(wsc.peerID, calendar.SourceInput{Name: p.Name, Path: p.Path, URL: p.URL, Timezone: p.Timezone, Enabled: p.Enabled}, ctx)
	if err != nil {
		slog.Error("ws: calendar source create", "peer", wsc.peerID, "error", err)
		wsc.replyErr(req.ID, errCodeServer, "calendar source create error: "+err.Error())
		return
	}
	wsc.reply(req.ID, result)
}

func (h *Handler) rpcCalendarSourceList(ctx context.Context, wsc *wsConn, req *rpcRequest) {
	var p struct {
		EnabledOnly bool `json:"enabled_only"`
	}
	if err := decodeParams(req.Params, &p); err != nil {
		wsc.replyErr(req.ID, errCodeInvalidParams, err.Error())
		return
	}
	result, err := h.calendarSourceList(wsc.peerID, p.EnabledOnly, ctx)
	if err != nil {
		slog.Error("ws: calendar source list", "peer", wsc.peerID, "error", err)
		wsc.replyErr(req.ID, errCodeServer, "calendar source list error: "+err.Error())
		return
	}
	wsc.reply(req.ID, result)
}

func (h *Handler) rpcCalendarSourceDelete(ctx context.Context, wsc *wsConn, req *rpcRequest) {
	var p struct {
		ID string `json:"id"`
	}
	if err := decodeParams(req.Params, &p); err != nil {
		wsc.replyErr(req.ID, errCodeInvalidParams, err.Error())
		return
	}
	result, err := h.calendarSourceDelete(wsc.peerID, p.ID, ctx)
	if err != nil {
		slog.Error("ws: calendar source delete", "peer", wsc.peerID, "error", err)
		wsc.replyErr(req.ID, errCodeServer, "calendar source delete error: "+err.Error())
		return
	}
	wsc.reply(req.ID, result)
}

func (h *Handler) rpcCalendarAgenda(ctx context.Context, wsc *wsConn, req *rpcRequest) {
	var p struct {
		Scope    string `json:"scope"`
		Timezone string `json:"timezone"`
		Now      int64  `json:"now"`
		Limit    int    `json:"limit"`
	}
	if err := decodeParams(req.Params, &p); err != nil {
		wsc.replyErr(req.ID, errCodeInvalidParams, err.Error())
		return
	}
	result, err := h.calendarAgenda(wsc.peerID, calendar.AgendaQuery{Scope: p.Scope, Timezone: p.Timezone, Now: p.Now, Limit: p.Limit}, ctx)
	if err != nil {
		slog.Error("ws: calendar agenda", "peer", wsc.peerID, "error", err)
		wsc.replyErr(req.ID, errCodeServer, "calendar agenda error: "+err.Error())
		return
	}
	wsc.reply(req.ID, result)
}
