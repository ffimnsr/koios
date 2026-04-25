package handler

import (
	"log/slog"
	"strings"
)

// ── usage RPCs ────────────────────────────────────────────────────────────────

func (h *Handler) rpcUsageGet(wsc *wsConn, req *rpcRequest) {
	var p struct {
		PeerID string `json:"peer_id"`
	}
	if err := decodeParams(req.Params, &p); err != nil {
		wsc.replyErr(req.ID, errCodeInvalidParams, err.Error())
		return
	}
	peerID := strings.TrimSpace(p.PeerID)
	if peerID == "" {
		peerID = wsc.peerID
	}
	if h.usageStore == nil {
		wsc.reply(req.ID, map[string]any{"peer_id": peerID, "found": false})
		return
	}
	u, ok := h.usageStore.Get(peerID)
	wsc.reply(req.ID, map[string]any{"peer_id": peerID, "found": ok, "usage": u})
}

func (h *Handler) rpcUsageList(wsc *wsConn, req *rpcRequest) {
	if h.usageStore == nil {
		wsc.reply(req.ID, map[string]any{"sessions": []any{}, "count": 0})
		return
	}
	all := h.usageStore.All()
	wsc.reply(req.ID, map[string]any{"sessions": all, "count": len(all)})
}

func (h *Handler) rpcUsageTotals(wsc *wsConn, req *rpcRequest) {
	if h.usageStore == nil {
		wsc.reply(req.ID, map[string]any{"totals": nil})
		return
	}
	wsc.reply(req.ID, map[string]any{"totals": h.usageStore.Totals()})
}

// ── admin ─────────────────────────────────────────────────────────────────────

// rpcSetLogLevel changes the gateway log level at runtime.
func (h *Handler) rpcSetLogLevel(wsc *wsConn, req *rpcRequest) {
	var p struct {
		Level string `json:"level"`
	}
	if err := decodeParams(req.Params, &p); err != nil {
		wsc.replyErr(req.ID, errCodeInvalidParams, err.Error())
		return
	}
	if strings.TrimSpace(p.Level) == "" {
		wsc.replyErr(req.ID, errCodeInvalidParams, "level is required")
		return
	}
	if h.logLevel == nil {
		wsc.replyErr(req.ID, errCodeServer, "log level control is not enabled on this gateway")
		return
	}
	var level slog.Level
	switch strings.ToLower(strings.TrimSpace(p.Level)) {
	case "debug":
		level = slog.LevelDebug
	case "info":
		level = slog.LevelInfo
	case "warn", "warning":
		level = slog.LevelWarn
	case "error":
		level = slog.LevelError
	default:
		wsc.replyErr(req.ID, errCodeInvalidParams, "invalid level: must be debug, info, warn, or error")
		return
	}
	h.logLevel.Set(level)
	slog.Info("log level changed", "level", level.String(), "peer", wsc.peerID)
	wsc.reply(req.ID, map[string]any{"ok": true, "level": level.String()})
}
