package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/ffimnsr/koios/internal/agent"
	"github.com/ffimnsr/koios/internal/briefing"
	"github.com/ffimnsr/koios/internal/session"
	"github.com/ffimnsr/koios/internal/standing"
	"github.com/ffimnsr/koios/internal/types"
	"github.com/google/uuid"
)

// ── chat ──────────────────────────────────────────────────────────────────────

// FileAttachment is an image or file sent inline with a chat message.
// Data must be a base64-encoded payload; MimeType must be a valid MIME type.
type FileAttachment struct {
	Data     string `json:"data"`             // base64-encoded content
	MimeType string `json:"mime_type"`        // e.g. "image/jpeg"
	Name     string `json:"name,omitempty"`   // optional filename hint
	Detail   string `json:"detail,omitempty"` // OpenAI detail level: auto/low/high
}

type chatParams struct {
	Messages         []types.Message  `json:"messages"`
	Files            []FileAttachment `json:"files,omitempty"` // vision: attach images to the last user message
	Stream           bool             `json:"stream,omitempty"`
	BlockStream      *bool            `json:"block_stream,omitempty"`
	StreamChunkChars int              `json:"stream_chunk_chars,omitempty"`
	StreamCoalesceMS int              `json:"stream_coalesce_ms,omitempty"`
	MaxTokens        int              `json:"max_tokens,omitempty"`
	Temperature      *float64         `json:"temperature,omitempty"`
	TopP             *float64         `json:"top_p,omitempty"`
}

type wsStreamConfig struct {
	BlockMode      bool
	ChunkChars     int
	CoalesceWindow time.Duration
}

func normalizeWSStreamConfig(cfg wsStreamConfig) wsStreamConfig {
	if cfg.BlockMode {
		if cfg.ChunkChars <= 0 {
			cfg.ChunkChars = 160
		}
		if cfg.CoalesceWindow <= 0 {
			cfg.CoalesceWindow = 75 * time.Millisecond
		}
	}
	if cfg.ChunkChars < 0 {
		cfg.ChunkChars = 0
	}
	if cfg.CoalesceWindow < 0 {
		cfg.CoalesceWindow = 0
	}
	return cfg
}

func resolveWSStreamConfig(policy session.SessionPolicy, blockOverride *bool, chunkChars, coalesceMS int) (wsStreamConfig, error) {
	if chunkChars < 0 {
		return wsStreamConfig{}, fmt.Errorf("stream_chunk_chars must be >= 0")
	}
	if coalesceMS < 0 {
		return wsStreamConfig{}, fmt.Errorf("stream_coalesce_ms must be >= 0")
	}
	cfg := wsStreamConfig{
		BlockMode:      policy.BlockStream,
		ChunkChars:     policy.StreamChunkChars,
		CoalesceWindow: time.Duration(policy.StreamCoalesceMS) * time.Millisecond,
	}
	if blockOverride != nil {
		cfg.BlockMode = *blockOverride
	}
	if chunkChars > 0 {
		cfg.ChunkChars = chunkChars
	}
	if coalesceMS > 0 {
		cfg.CoalesceWindow = time.Duration(coalesceMS) * time.Millisecond
	}
	return normalizeWSStreamConfig(cfg), nil
}

func (h *Handler) rpcChat(ctx context.Context, wsc *wsConn, req *rpcRequest) {
	var p chatParams
	if err := decodeParams(req.Params, &p); err != nil {
		wsc.replyErr(req.ID, errCodeInvalidParams, err.Error())
		return
	}
	if len(p.Messages) == 0 {
		wsc.replyErr(req.ID, errCodeInvalidParams, "messages must not be empty")
		return
	}
	// Check if the last user message is a slash command; handle it without
	// forwarding to the LLM.
	if h.handleSlashCommand(ctx, wsc, req, p.Messages) {
		return
	}
	// Vision pipeline: attach files to the last user message as multipart content.
	if len(p.Files) > 0 {
		p.Messages = attachFilesToLastUserMessage(p.Messages, p.Files)
	}
	if h.agentRuntime == nil || h.agentCoord == nil {
		wsc.replyErr(req.ID, errCodeServer, "agent runtime is not enabled")
		return
	}
	if h.presence != nil {
		h.presence.Set(wsc.peerID, "online", false, "chat")
	}
	streamCfg, err := resolveWSStreamConfig(h.store.Policy(wsc.peerID), p.BlockStream, p.StreamChunkChars, p.StreamCoalesceMS)
	if err != nil {
		wsc.replyErr(req.ID, errCodeInvalidParams, err.Error())
		return
	}
	_, turnMessages := splitByRole(p.Messages)
	history := h.store.Get(wsc.peerID).History()
	reqCtx, cancel := context.WithTimeout(ctx, h.timeout)
	defer cancel()

	slog.Info("ws: chat", "peer", wsc.peerID, "turns", len(turnMessages), "history", len(history), "stream", p.Stream)
	var eventSink func(agent.Event)
	if p.Stream {
		eventSink = func(ev agent.Event) {
			wsc.notify("stream.event", map[string]any{
				"req_id": req.ID,
				"event":  ev,
			})
		}
	}
	runReq := agent.RunRequest{
		PeerID:        wsc.peerID,
		SessionKey:    wsc.peerID,
		Messages:      p.Messages,
		Stream:        p.Stream,
		BlockStream:   streamCfg.BlockMode,
		MaxSteps:      toolLoopMaxSteps,
		MaxTokens:     p.MaxTokens,
		Temperature:   p.Temperature,
		TopP:          p.TopP,
		ToolExecutor:  h,
		EventSink:     eventSink,
		Timeout:       h.timeout,
		ActiveProfile: h.store.Policy(wsc.peerID).ActiveProfile,
	}

	if p.Stream {
		sw := newWSStreamWriter(wsc, req.ID, streamCfg)
		result, err := h.agentCoord.RunStream(reqCtx, runReq, sw)
		sw.Close()
		if err != nil {
			slog.Error("ws: chat stream", "peer", wsc.peerID, "error", err)
			wsc.replyErr(req.ID, errCodeServer, "stream error: "+err.Error())
			return
		}
		visibleText, _ := h.userVisibleReply(wsc.peerID, result)
		if !sw.HasDeltas() && visibleText != "" {
			wsc.notify("stream.delta", map[string]any{
				"req_id":  req.ID,
				"content": visibleText,
			})
		}
		if h.usageStore != nil {
			h.usageStore.Add(wsc.peerID, result.Usage)
		}
		wsc.reply(req.ID, map[string]any{"assistant_text": visibleText, "usage": result.Usage, "done": true, "suppressed_reply": result.SuppressedReply})
		return
	}

	result, err := h.agentCoord.Run(reqCtx, runReq)
	if err != nil {
		slog.Error("ws: chat", "peer", wsc.peerID, "error", err)
		wsc.replyErr(req.ID, errCodeServer, "upstream error: "+err.Error())
		return
	}
	if h.usageStore != nil {
		if result.Response != nil {
			h.usageStore.Add(wsc.peerID, result.Response.Usage)
		}
	}
	_, visibleResp := h.userVisibleReply(wsc.peerID, result)
	wsc.reply(req.ID, visibleResp)
}

// attachFilesToLastUserMessage builds multipart content for the last user
// message in msgs by appending image_url parts for each FileAttachment. A copy
// of the slice is returned; the original is not mutated.
func attachFilesToLastUserMessage(msgs []types.Message, files []FileAttachment) []types.Message {
	if len(files) == 0 {
		return msgs
	}
	// Find the last user message.
	lastIdx := -1
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role == "user" {
			lastIdx = i
			break
		}
	}
	if lastIdx < 0 {
		return msgs
	}
	// Build content parts: text first, then images.
	out := make([]types.Message, len(msgs))
	copy(out, msgs)
	m := out[lastIdx]
	parts := make([]types.ContentPart, 0, 1+len(files))
	if m.Content != "" {
		parts = append(parts, types.ContentPart{Type: "text", Text: m.Content})
	}
	for _, f := range files {
		if f.Data == "" || f.MimeType == "" {
			continue
		}
		dataURI := "data:" + f.MimeType + ";base64," + f.Data
		detail := f.Detail
		if detail == "" {
			detail = "auto"
		}
		parts = append(parts, types.ContentPart{
			Type: "image_url",
			ImageURL: &types.ImageURLPart{
				URL:    dataURI,
				Detail: detail,
			},
		})
	}
	m.Parts = parts
	out[lastIdx] = m
	return out
}

func (h *Handler) rpcSessionReset(_ context.Context, wsc *wsConn, req *rpcRequest) {
	h.store.Reset(wsc.peerID)
	slog.Info("ws: session reset", "peer", wsc.peerID)
	wsc.reply(req.ID, map[string]bool{"ok": true})
}

func (h *Handler) rpcBriefGenerate(ctx context.Context, wsc *wsConn, req *rpcRequest) {
	var opts briefing.Options
	if err := decodeParams(req.Params, &opts); err != nil {
		wsc.replyErr(req.ID, errCodeInvalidParams, err.Error())
		return
	}
	result, err := h.briefGenerate(wsc.peerID, opts, ctx)
	if err != nil {
		wsc.replyErr(req.ID, errCodeServer, err.Error())
		return
	}
	wsc.reply(req.ID, result)
}

func (h *Handler) rpcSessionHistory(_ context.Context, wsc *wsConn, req *rpcRequest) {
	var p struct {
		Limit      int    `json:"limit,omitempty"`
		SessionKey string `json:"session_key,omitempty"`
	}
	if err := decodeParams(req.Params, &p); err != nil {
		wsc.replyErr(req.ID, errCodeInvalidParams, err.Error())
		return
	}
	sessionKey := wsc.peerID
	if strings.TrimSpace(p.SessionKey) != "" {
		sessionKey = strings.TrimSpace(p.SessionKey)
	}
	history := h.store.Get(sessionKey).History()
	if p.Limit > 0 && len(history) > p.Limit {
		history = history[len(history)-p.Limit:]
	}
	wsc.reply(req.ID, map[string]any{
		"peer_id":     wsc.peerID,
		"session_key": sessionKey,
		"count":       len(history),
		"messages":    history,
	})
}

func (h *Handler) rpcStandingGet(_ context.Context, wsc *wsConn, req *rpcRequest) {
	workspace := ""
	peer := ""
	effective := ""
	defaultProfile := ""
	activeProfile := strings.TrimSpace(h.store.Policy(wsc.peerID).ActiveProfile)
	profileError := ""
	profiles := map[string]standing.Profile{}
	var resolvedProfile *standing.ResolvedProfile
	if h.standingManager != nil {
		workspace = h.standingManager.WorkspaceContent()
		var err error
		peer, err = h.standingManager.PeerContent(wsc.peerID)
		if err != nil {
			slog.Error("ws: standing get", "peer", wsc.peerID, "error", err)
			wsc.replyErr(req.ID, errCodeServer, "error loading standing orders")
			return
		}
		doc, err := h.standingManager.Document(wsc.peerID)
		if err != nil {
			slog.Error("ws: standing document", "peer", wsc.peerID, "error", err)
			wsc.replyErr(req.ID, errCodeServer, "error loading standing orders")
			return
		}
		if doc != nil {
			defaultProfile = doc.DefaultProfile
			profiles = doc.Profiles
		}
		effective, err = h.standingManager.EffectiveContentForProfile(wsc.peerID, activeProfile)
		if err != nil {
			profileError = err.Error()
			parts := make([]string, 0, 2)
			if workspace != "" {
				parts = append(parts, workspace)
			}
			if peer != "" {
				parts = append(parts, peer)
			}
			effective = strings.TrimSpace(strings.Join(parts, "\n\n"))
		} else {
			resolvedProfile, err = h.standingManager.ResolveProfile(wsc.peerID, activeProfile)
			if err != nil {
				profileError = err.Error()
			}
		}
	}
	wsc.reply(req.ID, map[string]any{
		"peer_id":           wsc.peerID,
		"workspace_content": workspace,
		"peer_content":      peer,
		"effective_content": effective,
		"default_profile":   defaultProfile,
		"active_profile":    activeProfile,
		"resolved_profile":  resolvedProfile,
		"profiles":          profiles,
		"profile_error":     profileError,
		"writable":          h.standingManager != nil && h.standingManager.Writable(),
	})
}

func (h *Handler) rpcStandingSet(_ context.Context, wsc *wsConn, req *rpcRequest) {
	if h.standingManager == nil || !h.standingManager.Writable() {
		wsc.replyErr(req.ID, errCodeServer, "standing order persistence is not enabled")
		return
	}
	var p struct {
		Content string `json:"content"`
	}
	if err := decodeParams(req.Params, &p); err != nil {
		wsc.replyErr(req.ID, errCodeInvalidParams, err.Error())
		return
	}
	doc, err := h.standingManager.Store().Save(wsc.peerID, p.Content)
	if err != nil {
		slog.Error("ws: standing set", "peer", wsc.peerID, "error", err)
		wsc.replyErr(req.ID, errCodeServer, "error saving standing orders")
		return
	}
	wsc.reply(req.ID, doc)
}

func (h *Handler) rpcStandingClear(_ context.Context, wsc *wsConn, req *rpcRequest) {
	if h.standingManager == nil || !h.standingManager.Writable() {
		wsc.replyErr(req.ID, errCodeServer, "standing order persistence is not enabled")
		return
	}
	if err := h.standingManager.Store().Delete(wsc.peerID); err != nil {
		slog.Error("ws: standing clear", "peer", wsc.peerID, "error", err)
		wsc.replyErr(req.ID, errCodeServer, "error clearing standing orders")
		return
	}
	wsc.reply(req.ID, map[string]bool{"ok": true})
}

func (h *Handler) rpcStandingProfileSet(_ context.Context, wsc *wsConn, req *rpcRequest) {
	if h.standingManager == nil || !h.standingManager.Writable() {
		wsc.replyErr(req.ID, errCodeServer, "standing order persistence is not enabled")
		return
	}
	var p struct {
		Name          string   `json:"name"`
		Content       string   `json:"content"`
		ToolProfile   string   `json:"tool_profile"`
		ToolsAllow    []string `json:"tools_allow"`
		ToolsDeny     []string `json:"tools_deny"`
		ResponseStyle string   `json:"response_style"`
		ThinkLevel    string   `json:"think_level"`
		VerboseMode   *bool    `json:"verbose_mode"`
		TraceMode     *bool    `json:"trace_mode"`
		MakeDefault   bool     `json:"make_default"`
	}
	if err := decodeParams(req.Params, &p); err != nil {
		wsc.replyErr(req.ID, errCodeInvalidParams, err.Error())
		return
	}
	name := strings.TrimSpace(p.Name)
	if name == "" {
		wsc.replyErr(req.ID, errCodeInvalidParams, "name is required")
		return
	}
	if strings.TrimSpace(p.ThinkLevel) != "" && !validThinkLevels[strings.TrimSpace(p.ThinkLevel)] {
		wsc.replyErr(req.ID, errCodeInvalidParams, "invalid think level")
		return
	}
	doc, err := h.standingManager.Document(wsc.peerID)
	if err != nil {
		wsc.replyErr(req.ID, errCodeServer, "error loading standing orders")
		return
	}
	if doc == nil {
		doc = &standing.Document{PeerID: wsc.peerID}
	}
	if doc.Profiles == nil {
		doc.Profiles = make(map[string]standing.Profile)
	}
	profile := standing.Profile{
		Content:       p.Content,
		ToolProfile:   p.ToolProfile,
		ToolsAllow:    p.ToolsAllow,
		ToolsDeny:     p.ToolsDeny,
		ResponseStyle: p.ResponseStyle,
		ThinkLevel:    p.ThinkLevel,
		VerboseMode:   cloneOptionalBool(p.VerboseMode),
		TraceMode:     cloneOptionalBool(p.TraceMode),
	}
	doc.Profiles[name] = profile
	if p.MakeDefault {
		doc.DefaultProfile = name
	}
	saved, err := h.standingManager.Store().SaveDocument(doc)
	if err != nil {
		slog.Error("ws: standing profile set", "peer", wsc.peerID, "profile", name, "error", err)
		wsc.replyErr(req.ID, errCodeServer, "error saving standing profile")
		return
	}
	wsc.reply(req.ID, map[string]any{
		"ok":              true,
		"profile_name":    name,
		"profile":         saved.Profiles[name],
		"default_profile": saved.DefaultProfile,
	})
}

func (h *Handler) rpcStandingProfileDelete(_ context.Context, wsc *wsConn, req *rpcRequest) {
	if h.standingManager == nil || !h.standingManager.Writable() {
		wsc.replyErr(req.ID, errCodeServer, "standing order persistence is not enabled")
		return
	}
	var p struct {
		Name string `json:"name"`
	}
	if err := decodeParams(req.Params, &p); err != nil {
		wsc.replyErr(req.ID, errCodeInvalidParams, err.Error())
		return
	}
	name := strings.TrimSpace(p.Name)
	if name == "" {
		wsc.replyErr(req.ID, errCodeInvalidParams, "name is required")
		return
	}
	doc, err := h.standingManager.Document(wsc.peerID)
	if err != nil {
		wsc.replyErr(req.ID, errCodeServer, "error loading standing orders")
		return
	}
	if doc == nil || doc.Profiles == nil {
		wsc.replyErr(req.ID, errCodeInvalidParams, "standing profile not found")
		return
	}
	if _, ok := doc.Profiles[name]; !ok {
		wsc.replyErr(req.ID, errCodeInvalidParams, "standing profile not found")
		return
	}
	delete(doc.Profiles, name)
	if doc.DefaultProfile == name {
		doc.DefaultProfile = ""
	}
	if _, err := h.standingManager.Store().SaveDocument(doc); err != nil {
		slog.Error("ws: standing profile delete", "peer", wsc.peerID, "profile", name, "error", err)
		wsc.replyErr(req.ID, errCodeServer, "error deleting standing profile")
		return
	}
	if h.store.Policy(wsc.peerID).ActiveProfile == name {
		_ = h.store.PatchPolicy(wsc.peerID, func(policy *session.SessionPolicy) {
			policy.ActiveProfile = ""
		})
	}
	wsc.reply(req.ID, map[string]any{"ok": true, "profile_name": name})
}

func (h *Handler) rpcStandingProfileActivate(_ context.Context, wsc *wsConn, req *rpcRequest) {
	var p struct {
		Name       string `json:"name"`
		SessionKey string `json:"session_key,omitempty"`
	}
	if err := decodeParams(req.Params, &p); err != nil {
		wsc.replyErr(req.ID, errCodeInvalidParams, err.Error())
		return
	}
	sessionKey := strings.TrimSpace(p.SessionKey)
	if sessionKey == "" {
		sessionKey = wsc.peerID
	}
	if sessionKey != wsc.peerID && !strings.HasPrefix(sessionKey, wsc.peerID+"::") {
		wsc.replyErr(req.ID, errCodeInvalidParams, "session_key is not accessible to this peer")
		return
	}
	name := strings.TrimSpace(p.Name)
	if name != "" {
		if _, err := h.standingManager.ResolveProfile(wsc.peerID, name); err != nil {
			wsc.replyErr(req.ID, errCodeInvalidParams, err.Error())
			return
		}
	}
	if err := h.store.PatchPolicy(sessionKey, func(policy *session.SessionPolicy) {
		policy.ActiveProfile = name
	}); err != nil {
		wsc.replyErr(req.ID, errCodeServer, "could not persist policy: "+err.Error())
		return
	}
	wsc.reply(req.ID, map[string]any{
		"ok":             true,
		"session_key":    sessionKey,
		"active_profile": name,
	})
}

func cloneOptionalBool(value *bool) *bool {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}

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

// ── subagents ─────────────────────────────────────────────────────────────────
