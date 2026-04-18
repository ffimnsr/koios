package handler

// slash_commands.go implements the in-chat slash-command surface.
//
// Supported commands (parsed from the last user message, case-insensitive):
//
//	/think [off|minimal|low|medium|high|xhigh]
//	/verbose [on|off]
//	/trace   [on|off]
//	/status
//	/new | /reset
//	/compact
//	/restart  (owner-only)
//
// When a recognised command is found, handleSlashCommand processes it and
// returns true so that rpcChat skips the usual LLM call.

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"syscall"

	"github.com/ffimnsr/koios/internal/session"
	"github.com/ffimnsr/koios/internal/types"
)

// validThinkLevels is the ordered set of accepted think-level arguments.
var validThinkLevels = map[string]bool{
	"off":     true,
	"minimal": true,
	"low":     true,
	"medium":  true,
	"high":    true,
	"xhigh":   true,
}

// parseSlashCommand extracts the command name (without leading slash) and any
// trailing arguments from text. It returns ok=false when text does not start
// with '/'.
func parseSlashCommand(text string) (name, arg string, ok bool) {
	text = strings.TrimSpace(text)
	if !strings.HasPrefix(text, "/") {
		return "", "", false
	}
	text = text[1:]
	parts := strings.SplitN(text, " ", 2)
	name = strings.ToLower(strings.TrimSpace(parts[0]))
	if len(parts) == 2 {
		arg = strings.ToLower(strings.TrimSpace(parts[1]))
	}
	return name, arg, name != ""
}

// lastUserText returns the trimmed content of the last user-role message in
// msgs, or "" if no user message is present.
func lastUserText(msgs []types.Message) string {
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role == "user" {
			return strings.TrimSpace(msgs[i].Content)
		}
	}
	return ""
}

// slashReply sends a simple plain-text result back as {assistant_text:...}.
func slashReply(wsc *wsConn, req *rpcRequest, text string) {
	wsc.reply(req.ID, map[string]any{"assistant_text": text, "done": true})
}

// handleSlashCommand checks whether msgs contain a slash command and, if so,
// executes it and returns true. When false is returned the caller should
// continue with the normal LLM flow.
func (h *Handler) handleSlashCommand(ctx context.Context, wsc *wsConn, req *rpcRequest, msgs []types.Message) bool {
	text := lastUserText(msgs)
	name, arg, ok := parseSlashCommand(text)
	if !ok {
		return false
	}
	switch name {
	case "think":
		return h.slashThink(wsc, req, arg)
	case "verbose":
		return h.slashBoolToggle(wsc, req, arg, "verbose", func(p *session.SessionPolicy, v bool) {
			p.VerboseMode = v
		})
	case "trace":
		return h.slashBoolToggle(wsc, req, arg, "trace", func(p *session.SessionPolicy, v bool) {
			p.TraceMode = v
		})
	case "status":
		h.slashStatus(wsc, req)
		return true
	case "new", "reset":
		h.slashReset(wsc, req)
		return true
	case "compact":
		h.slashCompact(ctx, wsc, req)
		return true
	case "restart":
		h.slashRestart(wsc, req)
		return true
	}
	return false
}

// ── /think ────────────────────────────────────────────────────────────────────

func (h *Handler) slashThink(wsc *wsConn, req *rpcRequest, arg string) bool {
	if arg == "" {
		// Report current setting.
		policy := h.store.Policy(wsc.peerID)
		level := policy.ThinkLevel
		if level == "" {
			level = "off"
		}
		slashReply(wsc, req, fmt.Sprintf("Think level: %s", level))
		return true
	}
	if !validThinkLevels[arg] {
		slashReply(wsc, req, "Invalid think level. Choose: off | minimal | low | medium | high | xhigh")
		return true
	}
	if err := h.store.PatchPolicy(wsc.peerID, func(p *session.SessionPolicy) {
		if arg == "off" {
			p.ThinkLevel = ""
		} else {
			p.ThinkLevel = arg
		}
	}); err != nil {
		wsc.replyErr(req.ID, errCodeServer, "could not persist policy: "+err.Error())
		return true
	}
	if arg == "off" {
		slashReply(wsc, req, "Think level disabled.")
	} else {
		slashReply(wsc, req, fmt.Sprintf("Think level set to: %s", arg))
	}
	return true
}

// ── /verbose and /trace (bool toggles) ───────────────────────────────────────

func (h *Handler) slashBoolToggle(wsc *wsConn, req *rpcRequest, arg, name string, apply func(*session.SessionPolicy, bool)) bool {
	var desired bool
	switch arg {
	case "on", "true", "1", "yes":
		desired = true
	case "off", "false", "0", "no":
		desired = false
	case "":
		// Report current setting.
		policy := h.store.Policy(wsc.peerID)
		var current bool
		tmp := policy
		apply(&tmp, !current) // flip to detect default
		// Re-read the actual toggle.
		switch name {
		case "verbose":
			current = policy.VerboseMode
		case "trace":
			current = policy.TraceMode
		}
		state := "off"
		if current {
			state = "on"
		}
		slashReply(wsc, req, fmt.Sprintf("%s mode: %s", strings.Title(name), state)) //nolint:staticcheck
		return true
	default:
		slashReply(wsc, req, fmt.Sprintf("Usage: /%s [on|off]", name))
		return true
	}
	if err := h.store.PatchPolicy(wsc.peerID, func(p *session.SessionPolicy) {
		apply(p, desired)
	}); err != nil {
		wsc.replyErr(req.ID, errCodeServer, "could not persist policy: "+err.Error())
		return true
	}
	state := "on"
	if !desired {
		state = "off"
	}
	slashReply(wsc, req, fmt.Sprintf("%s mode: %s", strings.Title(name), state)) //nolint:staticcheck
	return true
}

// ── /status ───────────────────────────────────────────────────────────────────

func (h *Handler) slashStatus(wsc *wsConn, req *rpcRequest) {
	model := h.model
	policy := h.store.Policy(wsc.peerID)
	if policy.ModelOverride != "" {
		model = policy.ModelOverride
	}

	histLen := 0
	if sess := h.store.Get(wsc.peerID); sess != nil {
		histLen = len(sess.History())
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "Model: %s\n", model)
	fmt.Fprintf(&sb, "Session messages: %d\n", histLen)

	if policy.ThinkLevel != "" {
		fmt.Fprintf(&sb, "Think level: %s\n", policy.ThinkLevel)
	}
	if policy.VerboseMode {
		sb.WriteString("Verbose mode: on\n")
	}
	if policy.TraceMode {
		sb.WriteString("Trace mode: on\n")
	}

	if h.usageStore != nil {
		if u, ok := h.usageStore.Get(wsc.peerID); ok {
			fmt.Fprintf(&sb, "Tokens (prompt/completion/total): %d / %d / %d\n",
				u.PromptTokens, u.CompletionTokens, u.TotalTokens)
			fmt.Fprintf(&sb, "Turns: %d\n", u.Turns)
		}
	}

	b, _ := json.Marshal(map[string]any{
		"model":             model,
		"session_messages":  histLen,
		"think_level":       policy.ThinkLevel,
		"verbose_mode":      policy.VerboseMode,
		"trace_mode":        policy.TraceMode,
	})
	wsc.reply(req.ID, map[string]any{
		"assistant_text": strings.TrimRight(sb.String(), "\n"),
		"status":         json.RawMessage(b),
		"done":           true,
	})
}

// ── /new | /reset ─────────────────────────────────────────────────────────────

func (h *Handler) slashReset(wsc *wsConn, req *rpcRequest) {
	h.store.Reset(wsc.peerID)
	slashReply(wsc, req, "Session reset. Starting fresh.")
}

// ── /compact ──────────────────────────────────────────────────────────────────

func (h *Handler) slashCompact(ctx context.Context, wsc *wsConn, req *rpcRequest) {
	if ok := h.store.CompactNow(ctx, wsc.peerID); ok {
		slashReply(wsc, req, "Session compacted.")
	} else {
		slashReply(wsc, req, "Compaction not available (no compactor configured or session is empty).")
	}
}

// ── /restart ──────────────────────────────────────────────────────────────────

// isOwner returns true when h.ownerPeerIDs is empty (no restriction) or when
// peerID is listed in ownerPeerIDs.
func (h *Handler) isOwner(peerID string) bool {
	if len(h.ownerPeerIDs) == 0 {
		return true
	}
	for _, id := range h.ownerPeerIDs {
		if id == peerID {
			return true
		}
	}
	return false
}

func (h *Handler) slashRestart(wsc *wsConn, req *rpcRequest) {
	if !h.isOwner(wsc.peerID) {
		slashReply(wsc, req, "Permission denied: /restart is restricted to owner peers.")
		return
	}
	slashReply(wsc, req, "Restarting gateway…")
	// Send SIGTERM to the current process so that graceful shutdown runs.
	go func() {
		if err := syscall.Kill(os.Getpid(), syscall.SIGTERM); err != nil {
			// Best-effort; nothing more we can do here.
			_ = err
		}
	}()
}
