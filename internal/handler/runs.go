package handler

import (
	"encoding/json"
	"time"

	"github.com/ffimnsr/koios/internal/runledger"
)

// ── runs.list ─────────────────────────────────────────────────────────────────

type runsListParams struct {
	PeerID string `json:"peer_id"`
	Kind   string `json:"kind"`
	Status string `json:"status"`
	Limit  int    `json:"limit"`
}

func (h *Handler) rpcRunsList(wsc *wsConn, req *rpcRequest) {
	if h.runLedger == nil {
		wsc.replyErr(req.ID, errCodeServer, "run ledger is not enabled")
		return
	}
	var p runsListParams
	if len(req.Params) > 0 {
		if err := json.Unmarshal(req.Params, &p); err != nil {
			wsc.replyErr(req.ID, errCodeInvalidParams, "invalid params: "+err.Error())
			return
		}
	}
	if p.Limit <= 0 {
		p.Limit = 100
	}
	f := runledger.Filter{
		PeerID: p.PeerID,
		Kind:   runledger.RunKind(p.Kind),
		Status: runledger.RunStatus(p.Status),
		Limit:  p.Limit,
	}
	records := h.runLedger.List(f, 7*24*time.Hour)
	for i := range records {
		records[i].Result = nil
	}
	wsc.reply(req.ID, map[string]any{"records": records})
}

// ── runs.get ──────────────────────────────────────────────────────────────────

type runsGetParams struct {
	ID string `json:"id"`
}

func (h *Handler) rpcRunsGet(wsc *wsConn, req *rpcRequest) {
	if h.runLedger == nil {
		wsc.replyErr(req.ID, errCodeServer, "run ledger is not enabled")
		return
	}
	var p runsGetParams
	if err := json.Unmarshal(req.Params, &p); err != nil {
		wsc.replyErr(req.ID, errCodeInvalidParams, "invalid params: "+err.Error())
		return
	}
	if p.ID == "" {
		wsc.replyErr(req.ID, errCodeInvalidParams, "id is required")
		return
	}
	rec, ok := h.runLedger.Get(p.ID)
	if !ok {
		wsc.replyErr(req.ID, errCodeServer, "run not found: "+p.ID)
		return
	}
	wsc.reply(req.ID, rec)
}
