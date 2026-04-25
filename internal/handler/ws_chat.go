package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/ffimnsr/koios/internal/agent"
	"github.com/ffimnsr/koios/internal/briefing"
	"github.com/ffimnsr/koios/internal/session"
	"github.com/ffimnsr/koios/internal/types"
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

// ── wsStreamWriter ────────────────────────────────────────────────────────────

// wsStreamWriter implements http.ResponseWriter and http.Flusher so that the
// provider's CompleteStream can write SSE bytes to it.  Each complete SSE data
// line is forwarded as a stream.delta WebSocket notification.
type wsStreamWriter struct {
	wsc      *wsConn
	reqID    json.RawMessage
	config   wsStreamConfig
	mu       sync.Mutex
	buf      bytes.Buffer
	full     strings.Builder
	hidden   strings.Builder
	pending  strings.Builder
	timer    *time.Timer
	closed   bool
	deltas   int
	hdr      http.Header
	resolved bool
}

func newWSStreamWriter(wsc *wsConn, reqID json.RawMessage, config wsStreamConfig) *wsStreamWriter {
	return &wsStreamWriter{wsc: wsc, reqID: reqID, hdr: make(http.Header), config: normalizeWSStreamConfig(config)}
}

func (w *wsStreamWriter) Header() http.Header { return w.hdr }
func (w *wsStreamWriter) WriteHeader(_ int)   {}
func (w *wsStreamWriter) Flush()              { w.processLines() }

func (w *wsStreamWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	w.buf.Write(p)
	w.mu.Unlock()
	w.processLines()
	return len(p), nil
}

func (w *wsStreamWriter) processLines() {
	w.mu.Lock()
	defer w.mu.Unlock()
	for {
		data := w.buf.Bytes()
		idx := bytes.IndexByte(data, '\n')
		if idx < 0 {
			return
		}
		line := string(bytes.TrimRight(data[:idx], "\r"))
		w.buf.Next(idx + 1)

		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		payload := line[6:]
		if payload == "[DONE]" {
			continue
		}
		var chunk types.StreamChunk
		if err := json.Unmarshal([]byte(payload), &chunk); err != nil {
			continue
		}
		if len(chunk.Choices) == 0 {
			continue
		}
		content := chunk.Choices[0].Delta.Content
		if content == "" {
			continue
		}
		w.full.WriteString(content)
		if !w.resolved {
			w.hidden.WriteString(content)
			if agent.SilentReplyMayContinue(w.hidden.String()) {
				continue
			}
			w.resolved = true
			content = w.hidden.String()
			w.hidden.Reset()
		}
		if !w.shouldBufferLocked() {
			w.emitDeltaLocked(content)
			continue
		}
		w.pending.WriteString(content)
		if w.config.ChunkChars > 0 && w.pending.Len() >= w.config.ChunkChars {
			w.flushPendingLocked()
			continue
		}
		w.scheduleFlushLocked()
	}
}

func (w *wsStreamWriter) shouldBufferLocked() bool {
	return w.config.BlockMode || w.config.ChunkChars > 0 || w.config.CoalesceWindow > 0
}

func (w *wsStreamWriter) scheduleFlushLocked() {
	if w.closed || w.config.CoalesceWindow <= 0 || w.pending.Len() == 0 || w.timer != nil {
		return
	}
	w.timer = time.AfterFunc(w.config.CoalesceWindow, func() {
		w.mu.Lock()
		defer w.mu.Unlock()
		w.flushPendingLocked()
	})
}

func (w *wsStreamWriter) emitDeltaLocked(content string) {
	if content == "" {
		return
	}
	w.deltas++
	w.wsc.notify("stream.delta", map[string]any{
		"req_id":  w.reqID,
		"content": content,
	})
}

func (w *wsStreamWriter) flushPendingLocked() {
	if w.timer != nil {
		w.timer.Stop()
		w.timer = nil
	}
	content := w.pending.String()
	w.pending.Reset()
	w.emitDeltaLocked(content)
}

func (w *wsStreamWriter) Close() {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.closed = true
	if !w.resolved {
		buffered := w.hidden.String()
		w.hidden.Reset()
		w.resolved = true
		if suppressed, _ := agent.DetectSilentReply(buffered); !suppressed {
			if !w.shouldBufferLocked() {
				w.emitDeltaLocked(buffered)
			} else {
				w.pending.WriteString(buffered)
			}
		}
	}
	w.flushPendingLocked()
}

// AssistantText returns the full assistant text accumulated from stream deltas.
func (w *wsStreamWriter) AssistantText() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.full.String()
}

func (w *wsStreamWriter) HasDeltas() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.deltas > 0
}

func (h *Handler) userVisibleReply(peerID string, result *agent.Result) (string, *types.ChatResponse) {
	if result == nil {
		return "", nil
	}
	visibleText := result.AssistantText
	visibleResp := cloneChatResponse(result.Response)
	if result.SuppressedReply {
		visibleText = ""
		if visibleResp != nil && len(visibleResp.Choices) > 0 {
			visibleResp.Choices[0].Message.Content = ""
		}
		return visibleText, visibleResp
	}
	policy := h.store.Policy(peerID)
	if policy.VerboseMode {
		if summary := h.renderVerboseToolSummary(peerID, result.Events); summary != "" {
			if strings.TrimSpace(visibleText) == "" {
				visibleText = summary
			} else {
				visibleText = summary + "\n\nReply:\n" + visibleText
			}
		}
	}
	if visibleResp != nil && len(visibleResp.Choices) > 0 {
		visibleResp.Choices[0].Message.Content = visibleText
	}
	return visibleText, visibleResp
}

func (h *Handler) renderVerboseToolSummary(peerID string, events []agent.Event) string {
	if len(events) == 0 {
		return ""
	}
	descriptions := make(map[string]string)
	for _, tool := range h.ToolDefinitions(peerID) {
		descriptions[tool.Function.Name] = strings.TrimSpace(tool.Function.Description)
	}
	var sb strings.Builder
	count := 0
	for i := range events {
		ev := events[i]
		if ev.Kind != agent.EventToolCall {
			continue
		}
		count++
		if count == 1 {
			sb.WriteString("Tool summary:")
		}
		name := ev.ToolName
		if name == "" {
			name = ev.Message
		}
		fmt.Fprintf(&sb, "\n[%d] %s", count, name)
		if desc := descriptions[name]; desc != "" {
			fmt.Fprintf(&sb, "\nDescription: %s", desc)
		}
		if strings.TrimSpace(ev.Summary) != "" {
			fmt.Fprintf(&sb, "\nArguments: %s", ev.Summary)
		}
		if result := matchingToolResult(events, i); result != nil && strings.TrimSpace(result.Summary) != "" {
			fmt.Fprintf(&sb, "\nResult: %s", result.Summary)
		}
	}
	return strings.TrimSpace(sb.String())
}

func matchingToolResult(events []agent.Event, callIdx int) *agent.Event {
	call := events[callIdx]
	for i := callIdx + 1; i < len(events); i++ {
		ev := events[i]
		if ev.Step != call.Step {
			continue
		}
		if ev.Kind != agent.EventToolResult {
			continue
		}
		if ev.ToolName == call.ToolName || ev.Message == call.Message {
			return &events[i]
		}
	}
	return nil
}

func cloneChatResponse(resp *types.ChatResponse) *types.ChatResponse {
	if resp == nil {
		return nil
	}
	cp := *resp
	if len(resp.Choices) > 0 {
		cp.Choices = append([]types.ChatChoice(nil), resp.Choices...)
	}
	return &cp
}
