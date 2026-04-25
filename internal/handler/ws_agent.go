package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/ffimnsr/koios/internal/agent"
	"github.com/ffimnsr/koios/internal/types"
	"github.com/google/uuid"
)

// ── agent.run ─────────────────────────────────────────────────────────────────

type agentRunParams struct {
	Messages         []types.Message `json:"messages"`
	Scope            string          `json:"scope,omitempty"`
	SenderID         string          `json:"sender_id,omitempty"`
	SessionKey       string          `json:"session_key,omitempty"`
	Stream           bool            `json:"stream,omitempty"`
	BlockStream      *bool           `json:"block_stream,omitempty"`
	StreamChunkChars int             `json:"stream_chunk_chars,omitempty"`
	StreamCoalesceMS int             `json:"stream_coalesce_ms,omitempty"`
	MaxSteps         int             `json:"max_steps,omitempty"`
	Timeout          string          `json:"timeout,omitempty"` // Go duration string, e.g. "30s"
}

func (h *Handler) rpcAgentRun(ctx context.Context, wsc *wsConn, req *rpcRequest) {
	if h.agentRuntime == nil || h.agentCoord == nil {
		wsc.replyErr(req.ID, errCodeServer, "agent runtime is not enabled")
		return
	}
	var p agentRunParams
	if err := decodeParams(req.Params, &p); err != nil {
		wsc.replyErr(req.ID, errCodeInvalidParams, err.Error())
		return
	}
	runReq, err := h.buildAgentRunRequest(wsc, req.ID, p)
	if err != nil {
		return
	}
	streamCfg, err := resolveWSStreamConfig(h.store.Policy(wsc.peerID), p.BlockStream, p.StreamChunkChars, p.StreamCoalesceMS)
	if err != nil {
		wsc.replyErr(req.ID, errCodeInvalidParams, err.Error())
		return
	}
	runReq.BlockStream = streamCfg.BlockMode

	// Register a cancellable context so the caller can interrupt this
	// synchronous run via agent.cancel using the returned run_id.
	runID := uuid.NewString()
	runCtx, runCancel := context.WithCancel(ctx)
	h.syncRunsMu.Lock()
	h.syncRuns[runID] = runCancel
	h.syncRunsMu.Unlock()
	defer func() {
		runCancel()
		h.syncRunsMu.Lock()
		delete(h.syncRuns, runID)
		h.syncRunsMu.Unlock()
	}()

	if p.Stream {
		sw := newWSStreamWriter(wsc, req.ID, streamCfg)
		result, err := h.agentCoord.RunStream(runCtx, runReq, sw)
		sw.Close()
		if err != nil {
			slog.Error("ws: agent run stream", "peer", wsc.peerID, "error", err)
			wsc.replyErr(req.ID, errCodeServer, "agent run failed: "+err.Error())
			return
		}
		visibleText, visibleResp := h.userVisibleReply(wsc.peerID, result)
		if !sw.HasDeltas() && visibleText != "" {
			wsc.notify("stream.delta", map[string]any{
				"req_id":  req.ID,
				"content": visibleText,
			})
		}
		if h.usageStore != nil {
			h.usageStore.Add(wsc.peerID, result.Usage)
		}
		visible := *result
		visible.AssistantText = visibleText
		visible.Response = visibleResp
		wsc.reply(req.ID, visible)
		return
	}

	result, err := h.agentCoord.Run(runCtx, runReq)
	if err != nil {
		slog.Error("ws: agent run", "peer", wsc.peerID, "error", err)
		wsc.replyErr(req.ID, errCodeServer, "agent run failed: "+err.Error())
		return
	}
	if h.usageStore != nil {
		h.usageStore.Add(wsc.peerID, result.Usage)
	}
	visibleText, visibleResp := h.userVisibleReply(wsc.peerID, result)
	visible := *result
	visible.AssistantText = visibleText
	visible.Response = visibleResp
	// Include run_id so the caller can correlate with agent.cancel if needed.
	wsc.reply(req.ID, map[string]any{
		"run_id": runID,
		"result": visible,
	})
}

func (h *Handler) rpcAgentStart(_ context.Context, wsc *wsConn, req *rpcRequest) {
	if h.agentRuntime == nil || h.agentCoord == nil {
		wsc.replyErr(req.ID, errCodeServer, "agent runtime is not enabled")
		return
	}
	var p agentRunParams
	if err := decodeParams(req.Params, &p); err != nil {
		wsc.replyErr(req.ID, errCodeInvalidParams, err.Error())
		return
	}
	runReq, err := h.buildAgentRunRequest(wsc, req.ID, p)
	if err != nil {
		return
	}
	if runReq.Stream {
		wsc.replyErr(req.ID, errCodeInvalidParams, "agent.start does not support stream=true")
		return
	}
	record, err := h.agentCoord.Start(runReq)
	if err != nil {
		wsc.replyErr(req.ID, errCodeServer, err.Error())
		return
	}
	wsc.reply(req.ID, record)
}

func (h *Handler) rpcAgentGet(_ context.Context, wsc *wsConn, req *rpcRequest) {
	if h.agentCoord == nil {
		wsc.replyErr(req.ID, errCodeServer, "agent runtime is not enabled")
		return
	}
	var p struct {
		ID string `json:"id"`
	}
	if err := decodeParams(req.Params, &p); err != nil {
		wsc.replyErr(req.ID, errCodeInvalidParams, err.Error())
		return
	}
	if p.ID == "" {
		wsc.replyErr(req.ID, errCodeInvalidParams, "id is required")
		return
	}
	record, ok := h.agentCoord.Get(p.ID)
	if !ok {
		wsc.replyErr(req.ID, errCodeServer, "run not found")
		return
	}
	wsc.reply(req.ID, record)
}

func (h *Handler) rpcAgentWait(ctx context.Context, wsc *wsConn, req *rpcRequest) {
	if h.agentCoord == nil {
		wsc.replyErr(req.ID, errCodeServer, "agent runtime is not enabled")
		return
	}
	var p struct {
		ID      string `json:"id"`
		Timeout string `json:"timeout,omitempty"`
	}
	if err := decodeParams(req.Params, &p); err != nil {
		wsc.replyErr(req.ID, errCodeInvalidParams, err.Error())
		return
	}
	if p.ID == "" {
		wsc.replyErr(req.ID, errCodeInvalidParams, "id is required")
		return
	}
	waitCtx := ctx
	if p.Timeout != "" {
		d, err := time.ParseDuration(p.Timeout)
		if err != nil {
			wsc.replyErr(req.ID, errCodeInvalidParams, "invalid timeout: "+err.Error())
			return
		}
		var cancel context.CancelFunc
		waitCtx, cancel = context.WithTimeout(ctx, d)
		defer cancel()
	}
	record, err := h.agentCoord.Wait(waitCtx, p.ID)
	if err != nil {
		wsc.replyErr(req.ID, errCodeServer, err.Error())
		return
	}
	wsc.reply(req.ID, record)
}

func (h *Handler) rpcAgentCancel(_ context.Context, wsc *wsConn, req *rpcRequest) {
	if h.agentCoord == nil {
		wsc.replyErr(req.ID, errCodeServer, "agent runtime is not enabled")
		return
	}
	var p struct {
		ID string `json:"id"`
	}
	if err := decodeParams(req.Params, &p); err != nil {
		wsc.replyErr(req.ID, errCodeInvalidParams, err.Error())
		return
	}
	if p.ID == "" {
		wsc.replyErr(req.ID, errCodeInvalidParams, "id is required")
		return
	}

	// Check synchronous runs first (registered by agent.run).
	h.syncRunsMu.Lock()
	syncCancel, isSyncRun := h.syncRuns[p.ID]
	if isSyncRun {
		delete(h.syncRuns, p.ID)
	}
	h.syncRunsMu.Unlock()
	if isSyncRun {
		syncCancel()
		wsc.reply(req.ID, map[string]any{"id": p.ID, "status": "canceled"})
		return
	}

	// Fall through to async runs managed by the coordinator.
	record, err := h.agentCoord.Cancel(p.ID)
	if err != nil {
		wsc.replyErr(req.ID, errCodeServer, err.Error())
		return
	}
	wsc.reply(req.ID, record)
}

func (h *Handler) rpcAgentSteer(_ context.Context, wsc *wsConn, req *rpcRequest) {
	if h.agentRuntime == nil {
		wsc.replyErr(req.ID, errCodeServer, "agent runtime is not enabled")
		return
	}
	var p struct {
		SessionKey string `json:"session_key"`
		Note       string `json:"note"`
	}
	if err := decodeParams(req.Params, &p); err != nil {
		wsc.replyErr(req.ID, errCodeInvalidParams, err.Error())
		return
	}
	if p.SessionKey == "" {
		wsc.replyErr(req.ID, errCodeInvalidParams, "session_key is required")
		return
	}
	if p.Note == "" {
		wsc.replyErr(req.ID, errCodeInvalidParams, "note is required")
		return
	}
	if err := h.agentRuntime.Steer(p.SessionKey, p.Note); err != nil {
		wsc.replyErr(req.ID, errCodeServer, err.Error())
		return
	}
	wsc.reply(req.ID, map[string]any{"session_key": p.SessionKey, "status": "queued"})
}

func (h *Handler) buildAgentRunRequest(wsc *wsConn, reqID json.RawMessage, p agentRunParams) (agent.RunRequest, error) {
	var timeout time.Duration
	policyKey := strings.TrimSpace(p.SessionKey)
	if policyKey == "" {
		policyKey = wsc.peerID
	}
	if p.Timeout != "" {
		d, err := time.ParseDuration(p.Timeout)
		if err != nil {
			wsc.replyErr(reqID, errCodeInvalidParams, "invalid timeout: "+err.Error())
			return agent.RunRequest{}, err
		}
		timeout = d
	}

	scope := agent.SessionScope(p.Scope)
	switch scope {
	case agent.ScopeMain, agent.ScopeDirect, agent.ScopeIsolated, agent.ScopeGlobal, "":
	default:
		wsc.replyErr(reqID, errCodeInvalidParams, "invalid scope: must be main, direct, isolated, or global")
		return agent.RunRequest{}, fmt.Errorf("invalid scope")
	}

	return agent.RunRequest{
		PeerID:        wsc.peerID,
		SenderID:      p.SenderID,
		Scope:         scope,
		SessionKey:    p.SessionKey,
		Messages:      p.Messages,
		Stream:        p.Stream,
		BlockStream:   p.BlockStream != nil && *p.BlockStream,
		MaxSteps:      p.MaxSteps,
		ToolExecutor:  h,
		ActiveProfile: h.store.Policy(policyKey).ActiveProfile,
		EventSink: func() func(agent.Event) {
			if !p.Stream {
				return nil
			}
			return func(ev agent.Event) {
				wsc.notify("stream.event", map[string]any{
					"req_id": reqID,
					"event":  ev,
				})
			}
		}(),
		Timeout: timeout,
	}, nil
}
