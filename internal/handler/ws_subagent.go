package handler

import (
	"context"
	"log/slog"

	"github.com/ffimnsr/koios/internal/subagent"
)

// ── subagents ─────────────────────────────────────────────────────────────────

func (h *Handler) rpcSubagentList(_ context.Context, wsc *wsConn, req *rpcRequest) {
	all := h.subRuntime.List()
	out := make([]*subagent.RunRecord, 0, len(all))
	for _, rec := range all {
		if rec.PeerID == wsc.peerID {
			out = append(out, rec)
		}
	}
	wsc.reply(req.ID, out)
}

func (h *Handler) rpcSubagentSpawn(ctx context.Context, wsc *wsConn, req *rpcRequest) {
	var spawnReq subagent.SpawnRequest
	if err := decodeParams(req.Params, &spawnReq); err != nil {
		wsc.replyErr(req.ID, errCodeInvalidParams, err.Error())
		return
	}
	spawnReq.PeerID = wsc.peerID
	if spawnReq.ParentSessionKey == "" {
		spawnReq.ParentSessionKey = wsc.peerID
	}
	if spawnReq.SourceSessionKey == "" {
		spawnReq.SourceSessionKey = spawnReq.ParentSessionKey
	}
	rec, err := h.subRuntime.Spawn(ctx, spawnReq)
	if err != nil {
		slog.Error("ws: subagent spawn", "peer", wsc.peerID, "error", err)
		wsc.replyErr(req.ID, errCodeServer, err.Error())
		return
	}
	wsc.reply(req.ID, rec)
}

func (h *Handler) rpcSubagentGet(_ context.Context, wsc *wsConn, req *rpcRequest) {
	var p struct {
		ID string `json:"id"`
	}
	if err := decodeParams(req.Params, &p); err != nil {
		wsc.replyErr(req.ID, errCodeInvalidParams, err.Error())
		return
	}
	rec, ok := h.subRuntime.Get(p.ID)
	if !ok || rec.PeerID != wsc.peerID {
		wsc.replyErr(req.ID, errCodeServer, "run not found")
		return
	}
	wsc.reply(req.ID, rec)
}

func (h *Handler) rpcSubagentKill(_ context.Context, wsc *wsConn, req *rpcRequest) {
	var p struct {
		ID string `json:"id"`
	}
	if err := decodeParams(req.Params, &p); err != nil {
		wsc.replyErr(req.ID, errCodeInvalidParams, err.Error())
		return
	}
	rec, ok := h.subRuntime.Get(p.ID)
	if !ok || rec.PeerID != wsc.peerID {
		wsc.replyErr(req.ID, errCodeServer, "run not found")
		return
	}
	if err := h.subRuntime.Kill(p.ID); err != nil {
		wsc.replyErr(req.ID, errCodeServer, err.Error())
		return
	}
	wsc.reply(req.ID, map[string]bool{"ok": true})
}

func (h *Handler) rpcSubagentSteer(_ context.Context, wsc *wsConn, req *rpcRequest) {
	var p struct {
		ID   string `json:"id"`
		Note string `json:"note"`
	}
	if err := decodeParams(req.Params, &p); err != nil {
		wsc.replyErr(req.ID, errCodeInvalidParams, err.Error())
		return
	}
	rec, ok := h.subRuntime.Get(p.ID)
	if !ok || rec.PeerID != wsc.peerID {
		wsc.replyErr(req.ID, errCodeServer, "run not found")
		return
	}
	updated, err := h.subRuntime.Steer(p.ID, p.Note)
	if err != nil {
		wsc.replyErr(req.ID, errCodeServer, err.Error())
		return
	}
	wsc.reply(req.ID, updated)
}

func (h *Handler) rpcSubagentTranscript(_ context.Context, wsc *wsConn, req *rpcRequest) {
	var p struct {
		ID string `json:"id"`
	}
	if err := decodeParams(req.Params, &p); err != nil {
		wsc.replyErr(req.ID, errCodeInvalidParams, err.Error())
		return
	}
	rec, ok := h.subRuntime.Get(p.ID)
	if !ok || rec.PeerID != wsc.peerID {
		wsc.replyErr(req.ID, errCodeServer, "run not found")
		return
	}
	msgs, err := h.subRuntime.Transcript(p.ID)
	if err != nil {
		wsc.replyErr(req.ID, errCodeServer, err.Error())
		return
	}
	wsc.reply(req.ID, map[string]any{"messages": msgs, "count": len(msgs)})
}
