package handler

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/ffimnsr/koios/internal/agent"
	"github.com/ffimnsr/koios/internal/scheduler"
	"github.com/ffimnsr/koios/internal/types"
)

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
	if footer := renderUsageFooter(policy.UsageMode, result.Usage); footer != "" {
		if strings.TrimSpace(visibleText) == "" {
			visibleText = footer
		} else {
			visibleText += "\n\n" + footer
		}
	}
	if visibleResp != nil && len(visibleResp.Choices) > 0 {
		visibleResp.Choices[0].Message.Content = visibleText
	}
	return visibleText, visibleResp
}

func renderUsageFooter(mode string, usage types.Usage) string {
	if strings.TrimSpace(mode) == "" {
		return ""
	}
	if usage.PromptTokens == 0 && usage.CompletionTokens == 0 && usage.TotalTokens == 0 {
		return ""
	}
	return fmt.Sprintf("Usage: %d prompt, %d completion, %d total tokens",
		usage.PromptTokens,
		usage.CompletionTokens,
		usage.TotalTokens,
	)
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

// ── cron validation helpers ───────────────────────────────────────────────────

func validateSchedule(s scheduler.Schedule) error {
	switch s.Kind {
	case scheduler.KindAt:
		if s.At == "" {
			return fmt.Errorf("schedule.at is required for kind=at")
		}
		if _, err := time.Parse(time.RFC3339, s.At); err != nil {
			if _, err2 := time.Parse("2006-01-02T15:04:05", s.At); err2 != nil {
				return fmt.Errorf("schedule.at %q is not a valid ISO 8601 timestamp", s.At)
			}
		}
	case scheduler.KindEvery:
		if s.EveryMs <= 0 {
			return fmt.Errorf("schedule.every_ms must be > 0")
		}
	case scheduler.KindCron:
		if s.Expr == "" {
			return fmt.Errorf("schedule.expr is required for kind=cron")
		}
		if s.Tz != "" {
			if _, err := time.LoadLocation(s.Tz); err != nil {
				return fmt.Errorf("schedule.tz %q is not a valid IANA timezone", s.Tz)
			}
		}
	default:
		return fmt.Errorf("schedule.kind must be one of: at, every, cron")
	}
	return nil
}

func validatePayload(p scheduler.Payload) error {
	switch p.Kind {
	case scheduler.PayloadSystemEvent:
		if strings.TrimSpace(p.Text) == "" {
			return fmt.Errorf("payload.text is required for kind=systemEvent")
		}
	case scheduler.PayloadAgentTurn:
		if strings.TrimSpace(p.Message) == "" {
			return fmt.Errorf("payload.message is required for kind=agentTurn")
		}
	default:
		return fmt.Errorf("payload.kind must be one of: systemEvent, agentTurn")
	}
	return nil
}

// ── misc helpers ──────────────────────────────────────────────────────────────

// decodeParams unmarshals the params JSON into dst. A nil or null params is
// treated as an empty object so methods with all-optional fields work correctly.
func decodeParams(params json.RawMessage, dst any) error {
	if len(params) == 0 || string(params) == "null" {
		return nil
	}
	if err := json.Unmarshal(params, dst); err != nil {
		return fmt.Errorf("invalid params: %w", err)
	}
	return nil
}

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
