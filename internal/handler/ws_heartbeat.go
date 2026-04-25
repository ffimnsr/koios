package handler

import (
	"context"
	"log/slog"
	"time"

	"github.com/ffimnsr/koios/internal/heartbeat"
)

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
