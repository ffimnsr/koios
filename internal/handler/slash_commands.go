package handler

// slash_commands.go implements the in-chat slash-command surface.
//
// Supported commands (parsed from the last user message, case-insensitive):
//
//	/think [off|minimal|low|medium|high|xhigh]
//	/usage   [off|tokens|full]
//	/verbose [on|off]
//	/trace   [on|off]
//	/profile [list|use <name>|clear]
//	/status
//	/new | /reset
//	/compact [status|now]
//	/bookmark [list [label]|get <id>|search <query>|add <title> | <content> [| labels] [| reminder]|clip <start>[-<end>] [| <title>] [| labels] [| reminder]|delete <id>]
//	/memory queue [list [status]|add <text>|edit <id> <text>|approve <id>|merge <id> <chunk-id>|reject <id> <reason>]
//	/tasks [list [status]|queue list [status]|queue add <title>|extract <text>|queue approve <id>|queue reject <id> <reason>|assign <id> <owner>|snooze <id> <unix>|complete <id>|reopen <id>]
//	/waiting [list [status]|add <waiting-for> | <title>|resolve <id>|reopen <id>|snooze <id> <unix>]
//	/calendar [list|add [<name> |] <path-or-url> [| <timezone>]|remove <id>|agenda [today|this_week|next_conflict] [timezone]]
//	/brief [daily|weekly] [timezone]
//	/review [weekly|daily] [timezone]
//	/restart  (owner-only)
//
// When a recognised command is found, handleSlashCommand processes it and
// returns true so that rpcChat skips the usual LLM call.

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/ffimnsr/koios/internal/bookmarks"
	"github.com/ffimnsr/koios/internal/briefing"
	"github.com/ffimnsr/koios/internal/calendar"
	"github.com/ffimnsr/koios/internal/memory"
	"github.com/ffimnsr/koios/internal/session"
	"github.com/ffimnsr/koios/internal/tasks"
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
		arg = strings.TrimSpace(parts[1])
	}
	return name, arg, name != ""
}

func trimLeadingFields(rest string, count int) string {
	rest = strings.TrimSpace(rest)
	for index := 0; index < count && rest != ""; index++ {
		fields := strings.Fields(rest)
		if len(fields) == 0 {
			return ""
		}
		rest = strings.TrimSpace(rest[len(fields[0]):])
	}
	return rest
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
	case "usage":
		return h.slashUsage(wsc, req, arg)
	case "verbose":
		return h.slashBoolToggle(wsc, req, arg, "verbose", func(p *session.SessionPolicy, v bool) {
			p.VerboseMode = v
		})
	case "trace":
		return h.slashBoolToggle(wsc, req, arg, "trace", func(p *session.SessionPolicy, v bool) {
			p.TraceMode = v
		})
	case "profile", "mode":
		h.slashProfile(wsc, req, arg)
		return true
	case "status":
		h.slashStatus(wsc, req)
		return true
	case "new", "reset":
		h.slashReset(wsc, req)
		return true
	case "compact":
		h.slashCompact(ctx, wsc, req, arg)
		return true
	case "bookmark", "bookmarks":
		h.slashBookmarks(ctx, wsc, req, arg)
		return true
	case "memory":
		h.slashMemory(ctx, wsc, req, arg)
		return true
	case "tasks", "task":
		h.slashTasks(ctx, wsc, req, arg)
		return true
	case "waiting":
		h.slashWaiting(ctx, wsc, req, arg)
		return true
	case "calendar":
		h.slashCalendar(ctx, wsc, req, arg)
		return true
	case "brief":
		h.slashBrief(ctx, wsc, req, arg, briefing.KindDaily)
		return true
	case "review":
		h.slashBrief(ctx, wsc, req, arg, briefing.KindWeekly)
		return true
	case "restart":
		h.slashRestart(wsc, req)
		return true
	}
	return false
}

func (h *Handler) slashBrief(ctx context.Context, wsc *wsConn, req *rpcRequest, arg string, defaultKind briefing.Kind) {
	fields := strings.Fields(strings.TrimSpace(arg))
	kind := string(defaultKind)
	timezone := ""
	if len(fields) > 0 && (fields[0] == string(briefing.KindDaily) || fields[0] == string(briefing.KindWeekly)) {
		kind = fields[0]
		fields = fields[1:]
	}
	if len(fields) > 0 {
		timezone = fields[0]
	}
	result, err := h.briefGenerate(wsc.peerID, briefing.Options{Kind: kind, Timezone: timezone}, ctx)
	if err != nil {
		slashReply(wsc, req, "Brief generation failed: "+err.Error())
		return
	}
	text, _ := result["text"].(string)
	wsc.reply(req.ID, map[string]any{"assistant_text": text, "brief": result["report"], "done": true})
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

// ── /usage ───────────────────────────────────────────────────────────────────

func normalizeUsageMode(arg string) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(arg)) {
	case "", "off":
		return "", true
	case "tokens", "full":
		return "tokens", true
	default:
		return "", false
	}
}

func usageModeLabel(mode string) string {
	if strings.TrimSpace(mode) == "" {
		return "off"
	}
	return mode
}

func (h *Handler) slashUsage(wsc *wsConn, req *rpcRequest, arg string) bool {
	if strings.TrimSpace(arg) == "" {
		policy := h.store.Policy(wsc.peerID)
		slashReply(wsc, req, fmt.Sprintf("Usage footer: %s", usageModeLabel(policy.UsageMode)))
		return true
	}
	mode, ok := normalizeUsageMode(arg)
	if !ok {
		slashReply(wsc, req, "Usage: /usage [off|tokens|full]")
		return true
	}
	if err := h.store.PatchPolicy(wsc.peerID, func(p *session.SessionPolicy) {
		p.UsageMode = mode
	}); err != nil {
		wsc.replyErr(req.ID, errCodeServer, "could not persist policy: "+err.Error())
		return true
	}
	slashReply(wsc, req, fmt.Sprintf("Usage footer: %s", usageModeLabel(mode)))
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
	resolvedProfileName := ""
	if policy.ModelOverride != "" {
		model = policy.ModelOverride
	}
	if h.standingManager != nil {
		resolved, err := h.standingManager.ResolveProfile(wsc.peerID, policy.ActiveProfile)
		if err == nil && resolved != nil {
			resolvedProfileName = resolved.Name
		}
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
	if policy.UsageMode != "" {
		fmt.Fprintf(&sb, "Usage footer: %s\n", policy.UsageMode)
	}
	if policy.ActiveProfile != "" {
		fmt.Fprintf(&sb, "Active profile: %s\n", policy.ActiveProfile)
	} else if resolvedProfileName != "" {
		fmt.Fprintf(&sb, "Active profile: %s (default)\n", resolvedProfileName)
	}
	if policy.QueueMode != "" {
		fmt.Fprintf(&sb, "Queue mode: %s\n", policy.QueueMode)
	}
	if policy.BlockStream {
		sb.WriteString("Block streaming: on\n")
	}
	if policy.StreamChunkChars > 0 {
		fmt.Fprintf(&sb, "Stream chunk chars: %d\n", policy.StreamChunkChars)
	}
	if policy.StreamCoalesceMS > 0 {
		fmt.Fprintf(&sb, "Stream coalesce ms: %d\n", policy.StreamCoalesceMS)
	}

	if h.usageStore != nil {
		if u, ok := h.usageStore.Get(wsc.peerID); ok {
			fmt.Fprintf(&sb, "Tokens (prompt/completion/total): %d / %d / %d\n",
				u.PromptTokens, u.CompletionTokens, u.TotalTokens)
			fmt.Fprintf(&sb, "Turns: %d\n", u.Turns)
		}
	}

	b, _ := json.Marshal(map[string]any{
		"model":              model,
		"session_messages":   histLen,
		"active_profile":     policy.ActiveProfile,
		"resolved_profile":   resolvedProfileName,
		"queue_mode":         policy.QueueMode,
		"think_level":        policy.ThinkLevel,
		"usage_mode":         usageModeLabel(policy.UsageMode),
		"verbose_mode":       policy.VerboseMode,
		"trace_mode":         policy.TraceMode,
		"block_stream":       policy.BlockStream,
		"stream_chunk_chars": policy.StreamChunkChars,
		"stream_coalesce_ms": policy.StreamCoalesceMS,
	})
	wsc.reply(req.ID, map[string]any{
		"assistant_text": strings.TrimRight(sb.String(), "\n"),
		"status":         json.RawMessage(b),
		"done":           true,
	})
}

func (h *Handler) slashProfile(wsc *wsConn, req *rpcRequest, arg string) {
	if h.standingManager == nil {
		slashReply(wsc, req, "Standing profiles are not enabled.")
		return
	}
	fields := strings.Fields(strings.TrimSpace(arg))
	policy := h.store.Policy(wsc.peerID)
	current := strings.TrimSpace(policy.ActiveProfile)
	resolvedName := ""
	if resolved, err := h.standingManager.ResolveProfile(wsc.peerID, current); err == nil && resolved != nil {
		resolvedName = resolved.Name
	}
	if len(fields) == 0 {
		label := "none"
		if current != "" {
			label = current
		} else if resolvedName != "" {
			label = resolvedName + " (default)"
		}
		slashReply(wsc, req, "Active profile: "+label)
		return
	}
	switch strings.ToLower(fields[0]) {
	case "list":
		names, err := h.standingManager.ProfileNames(wsc.peerID)
		if err != nil {
			slashReply(wsc, req, "Could not load profiles: "+err.Error())
			return
		}
		if len(names) == 0 {
			slashReply(wsc, req, "No standing profiles are defined for this peer.")
			return
		}
		var lines []string
		for _, name := range names {
			line := name
			if current != "" && name == current {
				line += " (active)"
			} else if current == "" && resolvedName != "" && name == resolvedName {
				line += " (default)"
			}
			lines = append(lines, line)
		}
		slashReply(wsc, req, "Profiles:\n- "+strings.Join(lines, "\n- "))
		return
	case "clear", "off", "none":
		if err := h.store.PatchPolicy(wsc.peerID, func(p *session.SessionPolicy) {
			p.ActiveProfile = ""
		}); err != nil {
			wsc.replyErr(req.ID, errCodeServer, "could not persist policy: "+err.Error())
			return
		}
		slashReply(wsc, req, "Session profile override cleared.")
		return
	case "use", "activate":
		if len(fields) < 2 {
			slashReply(wsc, req, "Usage: /profile use <name>")
			return
		}
		arg = fields[1]
	default:
		arg = fields[0]
	}
	name := strings.TrimSpace(arg)
	if _, err := h.standingManager.ResolveProfile(wsc.peerID, name); err != nil {
		slashReply(wsc, req, err.Error())
		return
	}
	if err := h.store.PatchPolicy(wsc.peerID, func(p *session.SessionPolicy) {
		p.ActiveProfile = name
	}); err != nil {
		wsc.replyErr(req.ID, errCodeServer, "could not persist policy: "+err.Error())
		return
	}
	slashReply(wsc, req, "Active profile set to: "+name)
}

// ── /new | /reset ─────────────────────────────────────────────────────────────

func (h *Handler) slashReset(wsc *wsConn, req *rpcRequest) {
	h.store.Reset(wsc.peerID)
	slashReply(wsc, req, "Session reset. Starting fresh.")
}

// ── /compact ──────────────────────────────────────────────────────────────────

func (h *Handler) slashCompact(ctx context.Context, wsc *wsConn, req *rpcRequest, arg string) {
	switch arg {
	case "", "now":
		report := h.store.CompactNow(ctx, wsc.peerID)
		wsc.reply(req.ID, map[string]any{
			"assistant_text": renderCompactionReport(report),
			"compaction":     report,
			"done":           true,
		})
	case "status":
		status := h.store.CompactionStatus(wsc.peerID)
		wsc.reply(req.ID, map[string]any{
			"assistant_text": renderCompactionStatus(status),
			"compaction":     status,
			"done":           true,
		})
	default:
		slashReply(wsc, req, "Usage: /compact [status|now]")
	}
}

func renderCompactionStatus(status session.CompactionStatus) string {
	var sb strings.Builder
	state := "disabled"
	if status.Enabled {
		state = "enabled"
	}
	eligible := "no"
	if status.Eligible {
		eligible = "yes"
	}
	memFlush := "off"
	if status.MemoryFlushEnabled {
		memFlush = "on"
	}
	fmt.Fprintf(&sb, "Compaction: %s\n", state)
	fmt.Fprintf(&sb, "Session messages: %d\n", status.SessionMessages)
	fmt.Fprintf(&sb, "Eligible now: %s\n", eligible)
	fmt.Fprintf(&sb, "Would compact: %d\n", status.CompactedMessages)
	fmt.Fprintf(&sb, "Would keep: %d\n", status.KeptMessages)
	fmt.Fprintf(&sb, "Memory flush before compaction: %s", memFlush)
	if status.Reason != "" {
		fmt.Fprintf(&sb, "\nReason: %s", status.Reason)
	}
	return sb.String()
}

func renderCompactionReport(report session.CompactionReport) string {
	status := report.Status
	if !status.Enabled || !status.Eligible {
		return "Compaction not available: " + status.Reason + "."
	}
	var lines []string
	lines = append(lines, "Compaction started.")
	if status.MemoryFlushEnabled {
		switch {
		case report.MemoryFlushed:
			lines = append(lines, fmt.Sprintf("Memory flush completed for %d messages before compaction.", status.CompactedMessages))
		case report.MemoryFlushError != "":
			lines = append(lines, "Memory flush failed: "+report.MemoryFlushError)
		default:
			lines = append(lines, "Memory flush skipped.")
		}
	} else {
		lines = append(lines, "Memory flush not configured.")
	}
	if report.Compacted {
		lines = append(lines, fmt.Sprintf("Compaction finished: summarized %d messages and kept %d recent messages.", status.CompactedMessages, status.KeptMessages))
		if report.SummaryChars > 0 {
			lines = append(lines, fmt.Sprintf("Summary size: %d chars.", report.SummaryChars))
		}
	} else if report.Error != "" {
		lines = append(lines, "Compaction failed: "+report.Error)
	} else {
		lines = append(lines, "Compaction did not run.")
	}
	return strings.Join(lines, "\n")
}

// ── /memory queue ────────────────────────────────────────────────────────────

func (h *Handler) slashMemory(ctx context.Context, wsc *wsConn, req *rpcRequest, arg string) {
	if h.memStore == nil {
		slashReply(wsc, req, "Memory is not enabled.")
		return
	}
	rest := strings.TrimSpace(arg)
	if strings.HasPrefix(rest, "queue") {
		rest = trimLeadingFields(rest, 1)
	}
	if rest == "" {
		h.slashMemoryList(ctx, wsc, req, "pending")
		return
	}
	parts := strings.Fields(rest)
	if len(parts) == 0 {
		h.slashMemoryList(ctx, wsc, req, "pending")
		return
	}
	switch strings.ToLower(parts[0]) {
	case "list":
		status := "pending"
		if len(parts) > 1 {
			status = strings.ToLower(parts[1])
		}
		h.slashMemoryList(ctx, wsc, req, status)
	case "add":
		content := trimLeadingFields(rest, 1)
		if content == "" {
			slashReply(wsc, req, "Usage: /memory queue add <text>")
			return
		}
		candidate, err := h.memStore.QueueCandidate(ctx, wsc.peerID, content, memory.ChunkOptions{})
		if err != nil {
			wsc.replyErr(req.ID, errCodeServer, "candidate create error: "+err.Error())
			return
		}
		slashReply(wsc, req, fmt.Sprintf("Queued memory candidate %s.", candidate.ID))
	case "edit":
		if len(parts) < 3 {
			slashReply(wsc, req, "Usage: /memory queue edit <candidate-id> <text>")
			return
		}
		id := parts[1]
		content := trimLeadingFields(rest, 2)
		candidate, err := h.memStore.EditCandidate(ctx, wsc.peerID, id, memory.CandidatePatch{Content: &content})
		if err != nil {
			wsc.replyErr(req.ID, errCodeServer, "candidate edit error: "+err.Error())
			return
		}
		slashReply(wsc, req, fmt.Sprintf("Updated memory candidate %s.", candidate.ID))
	case "approve":
		if len(parts) != 2 {
			slashReply(wsc, req, "Usage: /memory queue approve <candidate-id>")
			return
		}
		candidate, chunk, err := h.memStore.ApproveCandidate(ctx, wsc.peerID, parts[1], memory.CandidatePatch{}, "approved from chat")
		if err != nil {
			wsc.replyErr(req.ID, errCodeServer, "candidate approve error: "+err.Error())
			return
		}
		slashReply(wsc, req, fmt.Sprintf("Approved candidate %s into memory chunk %s.", candidate.ID, chunk.ID))
	case "merge":
		if len(parts) != 3 {
			slashReply(wsc, req, "Usage: /memory queue merge <candidate-id> <chunk-id>")
			return
		}
		candidate, chunk, err := h.memStore.MergeCandidate(ctx, wsc.peerID, parts[1], parts[2], memory.CandidatePatch{}, "merged from chat")
		if err != nil {
			wsc.replyErr(req.ID, errCodeServer, "candidate merge error: "+err.Error())
			return
		}
		slashReply(wsc, req, fmt.Sprintf("Merged candidate %s into memory chunk %s.", candidate.ID, chunk.ID))
	case "reject":
		if len(parts) < 3 {
			slashReply(wsc, req, "Usage: /memory queue reject <candidate-id> <reason>")
			return
		}
		id := parts[1]
		reason := trimLeadingFields(rest, 2)
		candidate, err := h.memStore.RejectCandidate(ctx, wsc.peerID, id, reason)
		if err != nil {
			wsc.replyErr(req.ID, errCodeServer, "candidate reject error: "+err.Error())
			return
		}
		slashReply(wsc, req, fmt.Sprintf("Rejected memory candidate %s.", candidate.ID))
	default:
		slashReply(wsc, req, "Usage: /memory queue [list [status]|add <text>|edit <id> <text>|approve <id>|merge <id> <chunk-id>|reject <id> <reason>]")
	}
}

func (h *Handler) slashMemoryList(ctx context.Context, wsc *wsConn, req *rpcRequest, status string) {
	candidates, err := h.memStore.ListCandidates(ctx, wsc.peerID, 10, memory.CandidateStatus(status))
	if err != nil {
		wsc.replyErr(req.ID, errCodeServer, "candidate list error: "+err.Error())
		return
	}
	if len(candidates) == 0 {
		slashReply(wsc, req, fmt.Sprintf("No %s memory candidates.", strings.TrimSpace(status)))
		return
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "Memory candidates (%s):", strings.TrimSpace(status))
	for _, candidate := range candidates {
		preview := candidate.Content
		if len(preview) > 72 {
			preview = preview[:72] + "..."
		}
		fmt.Fprintf(&sb, "\n- %s [%s] %s", candidate.ID, candidate.Status, preview)
		if origin := formatCandidateOrigin(candidate); origin != "" {
			fmt.Fprintf(&sb, "\n  source: %s", origin)
		}
	}
	slashReply(wsc, req, sb.String())
}

func formatCandidateOrigin(candidate memory.Candidate) string {
	parts := make([]string, 0, 3)
	if candidate.CaptureKind != "" {
		parts = append(parts, candidate.CaptureKind)
	}
	if candidate.SourceSessionKey != "" {
		parts = append(parts, candidate.SourceSessionKey)
	}
	if candidate.SourceExcerpt != "" {
		parts = append(parts, fmt.Sprintf("%q", candidate.SourceExcerpt))
	}
	if len(parts) == 0 {
		return ""
	}
	return strings.Join(parts, " | ")
}

// ── /tasks ───────────────────────────────────────────────────────────────────

func (h *Handler) slashTasks(ctx context.Context, wsc *wsConn, req *rpcRequest, arg string) {
	if h.taskStore == nil {
		slashReply(wsc, req, "Tasks are not enabled.")
		return
	}
	rest := strings.TrimSpace(arg)
	if rest == "" {
		h.slashTaskList(ctx, wsc, req, "open")
		return
	}
	parts := strings.Fields(rest)
	if len(parts) == 0 {
		h.slashTaskList(ctx, wsc, req, "open")
		return
	}
	switch strings.ToLower(parts[0]) {
	case "list":
		status := "open"
		if len(parts) > 1 {
			status = strings.ToLower(parts[1])
		}
		h.slashTaskList(ctx, wsc, req, status)
	case "queue":
		h.slashTaskQueue(ctx, wsc, req, trimLeadingFields(rest, 1))
	case "extract":
		text := trimLeadingFields(rest, 1)
		if text == "" {
			slashReply(wsc, req, "Usage: /tasks extract <text>")
			return
		}
		candidates, err := h.taskStore.ExtractAndQueue(ctx, wsc.peerID, text, tasks.CandidateProvenance{
			CaptureKind:      tasks.CandidateCaptureExternalExtract,
			SourceSessionKey: wsc.peerID + "::main",
			SourceExcerpt:    text,
		})
		if err != nil {
			wsc.replyErr(req.ID, errCodeServer, "task extract error: "+err.Error())
			return
		}
		if len(candidates) == 0 {
			slashReply(wsc, req, "No task candidates extracted.")
			return
		}
		slashReply(wsc, req, fmt.Sprintf("Queued %d task candidates.", len(candidates)))
	case "assign":
		if len(parts) < 3 {
			slashReply(wsc, req, "Usage: /tasks assign <task-id> <owner>")
			return
		}
		id := parts[1]
		owner := trimLeadingFields(rest, 2)
		task, err := h.taskStore.AssignTask(ctx, wsc.peerID, id, owner)
		if err != nil {
			wsc.replyErr(req.ID, errCodeServer, "task assign error: "+err.Error())
			return
		}
		slashReply(wsc, req, fmt.Sprintf("Assigned task %s to %s.", task.ID, task.Owner))
	case "snooze":
		if len(parts) != 3 {
			slashReply(wsc, req, "Usage: /tasks snooze <task-id> <unix>")
			return
		}
		until, err := parseInt64Arg(parts[2])
		if err != nil {
			slashReply(wsc, req, "Usage: /tasks snooze <task-id> <unix>")
			return
		}
		task, err := h.taskStore.SnoozeTask(ctx, wsc.peerID, parts[1], until)
		if err != nil {
			wsc.replyErr(req.ID, errCodeServer, "task snooze error: "+err.Error())
			return
		}
		slashReply(wsc, req, fmt.Sprintf("Snoozed task %s until %d.", task.ID, task.SnoozeUntil))
	case "complete":
		if len(parts) != 2 {
			slashReply(wsc, req, "Usage: /tasks complete <task-id>")
			return
		}
		task, err := h.taskStore.CompleteTask(ctx, wsc.peerID, parts[1])
		if err != nil {
			wsc.replyErr(req.ID, errCodeServer, "task complete error: "+err.Error())
			return
		}
		slashReply(wsc, req, fmt.Sprintf("Completed task %s.", task.ID))
	case "reopen":
		if len(parts) != 2 {
			slashReply(wsc, req, "Usage: /tasks reopen <task-id>")
			return
		}
		task, err := h.taskStore.ReopenTask(ctx, wsc.peerID, parts[1])
		if err != nil {
			wsc.replyErr(req.ID, errCodeServer, "task reopen error: "+err.Error())
			return
		}
		slashReply(wsc, req, fmt.Sprintf("Reopened task %s.", task.ID))
	default:
		slashReply(wsc, req, "Usage: /tasks [list [status]|queue list [status]|queue add <title>|extract <text>|queue approve <id>|queue reject <id> <reason>|assign <id> <owner>|snooze <id> <unix>|complete <id>|reopen <id>]")
	}
}

func (h *Handler) slashTaskQueue(ctx context.Context, wsc *wsConn, req *rpcRequest, rest string) {
	parts := strings.Fields(rest)
	if len(parts) == 0 {
		h.slashTaskCandidateList(ctx, wsc, req, "pending")
		return
	}
	switch strings.ToLower(parts[0]) {
	case "list":
		status := "pending"
		if len(parts) > 1 {
			status = strings.ToLower(parts[1])
		}
		h.slashTaskCandidateList(ctx, wsc, req, status)
	case "add":
		title := trimLeadingFields(rest, 1)
		if title == "" {
			slashReply(wsc, req, "Usage: /tasks queue add <title>")
			return
		}
		candidate, err := h.taskStore.QueueCandidate(ctx, wsc.peerID, tasks.CandidateInput{Title: title})
		if err != nil {
			wsc.replyErr(req.ID, errCodeServer, "task candidate create error: "+err.Error())
			return
		}
		slashReply(wsc, req, fmt.Sprintf("Queued task candidate %s.", candidate.ID))
	case "approve":
		if len(parts) != 2 {
			slashReply(wsc, req, "Usage: /tasks queue approve <candidate-id>")
			return
		}
		candidate, task, err := h.taskStore.ApproveCandidate(ctx, wsc.peerID, parts[1], tasks.CandidatePatch{}, "approved from chat")
		if err != nil {
			wsc.replyErr(req.ID, errCodeServer, "task candidate approve error: "+err.Error())
			return
		}
		slashReply(wsc, req, fmt.Sprintf("Approved task candidate %s into task %s.", candidate.ID, task.ID))
	case "reject":
		if len(parts) < 3 {
			slashReply(wsc, req, "Usage: /tasks queue reject <candidate-id> <reason>")
			return
		}
		id := parts[1]
		reason := trimLeadingFields(rest, 2)
		candidate, err := h.taskStore.RejectCandidate(ctx, wsc.peerID, id, reason)
		if err != nil {
			wsc.replyErr(req.ID, errCodeServer, "task candidate reject error: "+err.Error())
			return
		}
		slashReply(wsc, req, fmt.Sprintf("Rejected task candidate %s.", candidate.ID))
	default:
		slashReply(wsc, req, "Usage: /tasks queue [list [status]|add <title>|approve <id>|reject <id> <reason>]")
	}
}

func (h *Handler) slashTaskCandidateList(ctx context.Context, wsc *wsConn, req *rpcRequest, status string) {
	candidates, err := h.taskStore.ListCandidates(ctx, wsc.peerID, 10, tasks.CandidateStatus(status))
	if err != nil {
		wsc.replyErr(req.ID, errCodeServer, "task candidate list error: "+err.Error())
		return
	}
	if len(candidates) == 0 {
		slashReply(wsc, req, fmt.Sprintf("No %s task candidates.", strings.TrimSpace(status)))
		return
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "Task candidates (%s):", strings.TrimSpace(status))
	for _, candidate := range candidates {
		preview := candidate.Title
		if len(preview) > 72 {
			preview = preview[:72] + "..."
		}
		fmt.Fprintf(&sb, "\n- %s [%s] %s", candidate.ID, candidate.Status, preview)
		if origin := formatTaskCandidateOrigin(candidate); origin != "" {
			fmt.Fprintf(&sb, "\n  source: %s", origin)
		}
	}
	slashReply(wsc, req, sb.String())
}

func (h *Handler) slashTaskList(ctx context.Context, wsc *wsConn, req *rpcRequest, status string) {
	tasksList, err := h.taskStore.ListTasks(ctx, wsc.peerID, 10, tasks.TaskStatus(status))
	if err != nil {
		wsc.replyErr(req.ID, errCodeServer, "task list error: "+err.Error())
		return
	}
	if len(tasksList) == 0 {
		slashReply(wsc, req, fmt.Sprintf("No %s tasks.", strings.TrimSpace(status)))
		return
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "Tasks (%s):", strings.TrimSpace(status))
	for _, task := range tasksList {
		preview := task.Title
		if len(preview) > 72 {
			preview = preview[:72] + "..."
		}
		fmt.Fprintf(&sb, "\n- %s [%s] %s", task.ID, task.Status, preview)
		if task.Owner != "" {
			fmt.Fprintf(&sb, " (owner: %s)", task.Owner)
		}
	}
	slashReply(wsc, req, sb.String())
}

func formatTaskCandidateOrigin(candidate tasks.Candidate) string {
	parts := make([]string, 0, 3)
	if candidate.CaptureKind != "" {
		parts = append(parts, candidate.CaptureKind)
	}
	if candidate.SourceSessionKey != "" {
		parts = append(parts, candidate.SourceSessionKey)
	}
	if candidate.SourceExcerpt != "" {
		parts = append(parts, fmt.Sprintf("%q", candidate.SourceExcerpt))
	}
	return strings.Join(parts, " | ")
}

// ── /bookmark ──────────────────────────────────────────────────────────────

func (h *Handler) slashBookmarks(ctx context.Context, wsc *wsConn, req *rpcRequest, arg string) {
	if h.bookmarkStore == nil {
		slashReply(wsc, req, "Bookmarks are not enabled.")
		return
	}
	rest := strings.TrimSpace(arg)
	if rest == "" {
		h.slashBookmarkList(ctx, wsc, req, "", false)
		return
	}
	parts := strings.Fields(rest)
	if len(parts) == 0 {
		h.slashBookmarkList(ctx, wsc, req, "", false)
		return
	}
	switch strings.ToLower(parts[0]) {
	case "list":
		label := ""
		upcomingOnly := false
		if len(parts) > 1 {
			if strings.EqualFold(parts[1], "upcoming") {
				upcomingOnly = true
			} else {
				label = parts[1]
			}
		}
		h.slashBookmarkList(ctx, wsc, req, label, upcomingOnly)
	case "get":
		if len(parts) != 2 {
			slashReply(wsc, req, "Usage: /bookmark get <bookmark-id>")
			return
		}
		result, err := h.bookmarkGet(wsc.peerID, parts[1], ctx)
		if err != nil {
			wsc.replyErr(req.ID, errCodeServer, "bookmark get error: "+err.Error())
			return
		}
		bookmark, _ := result["bookmark"].(*bookmarks.Bookmark)
		if bookmark == nil {
			slashReply(wsc, req, "Bookmark not found.")
			return
		}
		slashReply(wsc, req, formatSlashBookmark(*bookmark, true))
	case "search":
		query := trimLeadingFields(rest, 1)
		if query == "" {
			slashReply(wsc, req, "Usage: /bookmark search <query>")
			return
		}
		result, err := h.bookmarkSearch(wsc.peerID, query, 10, ctx)
		if err != nil {
			wsc.replyErr(req.ID, errCodeServer, "bookmark search error: "+err.Error())
			return
		}
		items, _ := result["bookmarks"].([]bookmarks.Bookmark)
		if len(items) == 0 {
			slashReply(wsc, req, "No matching bookmarks.")
			return
		}
		slashReply(wsc, req, formatSlashBookmarkList("Search results", items))
	case "add":
		fields := splitSlashPipeFields(trimLeadingFields(rest, 1))
		if len(fields) < 2 {
			slashReply(wsc, req, "Usage: /bookmark add <title> | <content> [| labels] [| reminder]")
			return
		}
		labels := parseSlashLabels(fieldsAt(fields, 2))
		reminderAt, err := parseOptionalSlashReminder(fieldsAt(fields, 3))
		if err != nil {
			slashReply(wsc, req, "Usage: /bookmark add <title> | <content> [| labels] [| reminder]")
			return
		}
		result, err := h.bookmarkCreate(wsc.peerID, bookmarks.Input{
			Title:            fields[0],
			Content:          fields[1],
			Labels:           labels,
			ReminderAt:       reminderAt,
			SourceKind:       bookmarks.SourceKindManual,
			SourceSessionKey: wsc.peerID,
		}, ctx)
		if err != nil {
			wsc.replyErr(req.ID, errCodeServer, "bookmark create error: "+err.Error())
			return
		}
		bookmark, _ := result["bookmark"].(*bookmarks.Bookmark)
		if bookmark == nil {
			slashReply(wsc, req, "Bookmark saved.")
			return
		}
		slashReply(wsc, req, fmt.Sprintf("Saved bookmark %s.", bookmark.ID))
	case "clip":
		fields := splitSlashPipeFields(trimLeadingFields(rest, 1))
		if len(fields) == 0 {
			slashReply(wsc, req, "Usage: /bookmark clip <start>[-<end>] [| <title>] [| labels] [| reminder]")
			return
		}
		startIndex, endIndex, ok := parseSlashMessageRange(fields[0])
		if !ok {
			slashReply(wsc, req, "Usage: /bookmark clip <start>[-<end>] [| <title>] [| labels] [| reminder]")
			return
		}
		labels := parseSlashLabels(fieldsAt(fields, 2))
		reminderAt, err := parseOptionalSlashReminder(fieldsAt(fields, 3))
		if err != nil {
			slashReply(wsc, req, "Usage: /bookmark clip <start>[-<end>] [| <title>] [| labels] [| reminder]")
			return
		}
		result, err := h.bookmarkCaptureSession(wsc.peerID, "", "", fieldsAt(fields, 1), startIndex, endIndex, labels, reminderAt, ctx)
		if err != nil {
			wsc.replyErr(req.ID, errCodeServer, "bookmark capture error: "+err.Error())
			return
		}
		bookmark, _ := result["bookmark"].(*bookmarks.Bookmark)
		if bookmark == nil {
			slashReply(wsc, req, "Session bookmark saved.")
			return
		}
		slashReply(wsc, req, fmt.Sprintf("Saved session bookmark %s from messages %d-%d.", bookmark.ID, bookmark.SourceStartIndex, bookmark.SourceEndIndex))
	case "delete", "remove":
		if len(parts) != 2 {
			slashReply(wsc, req, "Usage: /bookmark delete <bookmark-id>")
			return
		}
		if _, err := h.bookmarkDelete(wsc.peerID, parts[1], ctx); err != nil {
			wsc.replyErr(req.ID, errCodeServer, "bookmark delete error: "+err.Error())
			return
		}
		slashReply(wsc, req, fmt.Sprintf("Deleted bookmark %s.", parts[1]))
	default:
		slashReply(wsc, req, "Usage: /bookmark [list [label]|get <id>|search <query>|add <title> | <content> [| labels] [| reminder]|clip <start>[-<end>] [| <title>] [| labels] [| reminder]|delete <id>]")
	}
}

func (h *Handler) slashBookmarkList(ctx context.Context, wsc *wsConn, req *rpcRequest, label string, upcomingOnly bool) {
	result, err := h.bookmarkList(wsc.peerID, 10, label, upcomingOnly, ctx)
	if err != nil {
		wsc.replyErr(req.ID, errCodeServer, "bookmark list error: "+err.Error())
		return
	}
	items, _ := result["bookmarks"].([]bookmarks.Bookmark)
	if len(items) == 0 {
		if upcomingOnly {
			slashReply(wsc, req, "No upcoming bookmarks.")
			return
		}
		if strings.TrimSpace(label) != "" {
			slashReply(wsc, req, fmt.Sprintf("No bookmarks with label %q.", strings.TrimSpace(label)))
			return
		}
		slashReply(wsc, req, "No bookmarks saved.")
		return
	}
	labelText := "Bookmarks"
	if strings.TrimSpace(label) != "" {
		labelText = fmt.Sprintf("Bookmarks [%s]", strings.TrimSpace(label))
	}
	if upcomingOnly {
		labelText = "Upcoming bookmarks"
	}
	slashReply(wsc, req, formatSlashBookmarkList(labelText, items))
}

func formatSlashBookmarkList(title string, items []bookmarks.Bookmark) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "%s:", title)
	for _, item := range items {
		fmt.Fprintf(&sb, "\n- %s %s", item.ID, item.Title)
		meta := slashBookmarkMeta(item)
		if meta != "" {
			fmt.Fprintf(&sb, "\n  %s", meta)
		}
	}
	return sb.String()
}

func formatSlashBookmark(item bookmarks.Bookmark, includeContent bool) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "%s\n%s", item.ID, item.Title)
	if meta := slashBookmarkMeta(item); meta != "" {
		fmt.Fprintf(&sb, "\n%s", meta)
	}
	if includeContent && strings.TrimSpace(item.Content) != "" {
		fmt.Fprintf(&sb, "\n\n%s", item.Content)
	}
	return sb.String()
}

func slashBookmarkMeta(item bookmarks.Bookmark) string {
	parts := make([]string, 0, 4)
	if len(item.Labels) > 0 {
		parts = append(parts, "labels="+strings.Join(item.Labels, ","))
	}
	if item.ReminderAt > 0 {
		parts = append(parts, "reminder="+time.Unix(item.ReminderAt, 0).UTC().Format(time.RFC3339))
	}
	if item.SourceKind != "" {
		parts = append(parts, "source="+item.SourceKind)
	}
	if item.SourceStartIndex > 0 {
		parts = append(parts, fmt.Sprintf("messages=%d-%d", item.SourceStartIndex, item.SourceEndIndex))
	}
	return strings.Join(parts, " | ")
}

func parseInt64Arg(value string) (int64, error) {
	return strconv.ParseInt(strings.TrimSpace(value), 10, 64)
}

func parseOptionalSlashReminder(value string) (int64, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0, nil
	}
	if unix, err := parseInt64Arg(value); err == nil {
		return unix, nil
	}
	if ts, err := time.Parse(time.RFC3339, value); err == nil {
		return ts.Unix(), nil
	}
	if ts, err := time.Parse("2006-01-02", value); err == nil {
		return ts.Unix(), nil
	}
	return 0, fmt.Errorf("invalid reminder")
}

func splitSlashPipeFields(value string) []string {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	parts := strings.Split(value, "|")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		trimmed := strings.TrimSpace(part)
		if trimmed == "" {
			continue
		}
		out = append(out, trimmed)
	}
	return out
}

func fieldsAt(fields []string, index int) string {
	if index < 0 || index >= len(fields) {
		return ""
	}
	return fields[index]
}

func parseSlashLabels(value string) []string {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	return strings.Split(value, ",")
}

func parseSlashMessageRange(value string) (int, int, bool) {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0, 0, false
	}
	if !strings.Contains(value, "-") {
		start, err := strconv.Atoi(value)
		if err != nil || start <= 0 {
			return 0, 0, false
		}
		return start, start, true
	}
	parts := strings.SplitN(value, "-", 2)
	start, err := strconv.Atoi(strings.TrimSpace(parts[0]))
	if err != nil || start <= 0 {
		return 0, 0, false
	}
	end, err := strconv.Atoi(strings.TrimSpace(parts[1]))
	if err != nil || end <= 0 {
		return 0, 0, false
	}
	return start, end, true
}

// ── /waiting ────────────────────────────────────────────────────────────────

func (h *Handler) slashWaiting(ctx context.Context, wsc *wsConn, req *rpcRequest, arg string) {
	if h.taskStore == nil {
		slashReply(wsc, req, "Tasks are not enabled.")
		return
	}
	rest := strings.TrimSpace(arg)
	if rest == "" {
		h.slashWaitingList(ctx, wsc, req, string(tasks.WaitingStatusOpen))
		return
	}
	parts := strings.Fields(rest)
	if len(parts) == 0 {
		h.slashWaitingList(ctx, wsc, req, string(tasks.WaitingStatusOpen))
		return
	}
	switch strings.ToLower(parts[0]) {
	case "list":
		status := string(tasks.WaitingStatusOpen)
		if len(parts) > 1 {
			status = strings.ToLower(parts[1])
		}
		h.slashWaitingList(ctx, wsc, req, status)
	case "add":
		payload := trimLeadingFields(rest, 1)
		fields := strings.SplitN(payload, "|", 2)
		if len(fields) != 2 {
			slashReply(wsc, req, "Usage: /waiting add <waiting-for> | <title>")
			return
		}
		waitingFor := strings.TrimSpace(fields[0])
		title := strings.TrimSpace(fields[1])
		item, err := h.taskStore.CreateWaitingOn(ctx, wsc.peerID, tasks.WaitingOnInput{
			Title:            title,
			WaitingFor:       waitingFor,
			SourceSessionKey: wsc.peerID + "::main",
			SourceExcerpt:    payload,
		})
		if err != nil {
			wsc.replyErr(req.ID, errCodeServer, "waiting create error: "+err.Error())
			return
		}
		slashReply(wsc, req, fmt.Sprintf("Created waiting-on %s for %s.", item.ID, item.WaitingFor))
	case "resolve":
		if len(parts) != 2 {
			slashReply(wsc, req, "Usage: /waiting resolve <id>")
			return
		}
		item, err := h.taskStore.ResolveWaitingOn(ctx, wsc.peerID, parts[1])
		if err != nil {
			wsc.replyErr(req.ID, errCodeServer, "waiting resolve error: "+err.Error())
			return
		}
		slashReply(wsc, req, fmt.Sprintf("Resolved waiting-on %s.", item.ID))
	case "reopen":
		if len(parts) != 2 {
			slashReply(wsc, req, "Usage: /waiting reopen <id>")
			return
		}
		item, err := h.taskStore.ReopenWaitingOn(ctx, wsc.peerID, parts[1])
		if err != nil {
			wsc.replyErr(req.ID, errCodeServer, "waiting reopen error: "+err.Error())
			return
		}
		slashReply(wsc, req, fmt.Sprintf("Reopened waiting-on %s.", item.ID))
	case "snooze":
		if len(parts) != 3 {
			slashReply(wsc, req, "Usage: /waiting snooze <id> <unix>")
			return
		}
		until, err := parseInt64Arg(parts[2])
		if err != nil {
			slashReply(wsc, req, "Usage: /waiting snooze <id> <unix>")
			return
		}
		item, err := h.taskStore.SnoozeWaitingOn(ctx, wsc.peerID, parts[1], until)
		if err != nil {
			wsc.replyErr(req.ID, errCodeServer, "waiting snooze error: "+err.Error())
			return
		}
		slashReply(wsc, req, fmt.Sprintf("Snoozed waiting-on %s until %d.", item.ID, item.SnoozeUntil))
	default:
		slashReply(wsc, req, "Usage: /waiting [list [status]|add <waiting-for> | <title>|resolve <id>|reopen <id>|snooze <id> <unix>]")
	}
}

func (h *Handler) slashWaitingList(ctx context.Context, wsc *wsConn, req *rpcRequest, status string) {
	items, err := h.taskStore.ListWaitingOns(ctx, wsc.peerID, 10, tasks.WaitingStatus(status))
	if err != nil {
		wsc.replyErr(req.ID, errCodeServer, "waiting list error: "+err.Error())
		return
	}
	if len(items) == 0 {
		slashReply(wsc, req, fmt.Sprintf("No %s waiting-on records.", strings.TrimSpace(status)))
		return
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "Waiting-on (%s):", strings.TrimSpace(status))
	for _, item := range items {
		preview := item.Title
		if len(preview) > 72 {
			preview = preview[:72] + "..."
		}
		fmt.Fprintf(&sb, "\n- %s [%s] %s (waiting for: %s)", item.ID, item.Status, preview, item.WaitingFor)
	}
	slashReply(wsc, req, sb.String())
}

// ── /calendar ───────────────────────────────────────────────────────────────

func (h *Handler) slashCalendar(ctx context.Context, wsc *wsConn, req *rpcRequest, arg string) {
	if h.calendarStore == nil {
		slashReply(wsc, req, "Calendar is not enabled.")
		return
	}
	rest := strings.TrimSpace(arg)
	if rest == "" {
		h.slashCalendarAgenda(ctx, wsc, req, string(calendar.AgendaScopeToday), "")
		return
	}
	parts := strings.Fields(rest)
	if len(parts) == 0 {
		h.slashCalendarAgenda(ctx, wsc, req, string(calendar.AgendaScopeToday), "")
		return
	}
	switch strings.ToLower(parts[0]) {
	case "list":
		h.slashCalendarList(ctx, wsc, req)
	case "add":
		h.slashCalendarAdd(ctx, wsc, req, trimLeadingFields(rest, 1))
	case "remove", "delete", "rm":
		if len(parts) != 2 {
			slashReply(wsc, req, "Usage: /calendar remove <source-id>")
			return
		}
		if err := h.calendarStore.DeleteSource(ctx, wsc.peerID, parts[1]); err != nil {
			wsc.replyErr(req.ID, errCodeServer, "calendar remove error: "+err.Error())
			return
		}
		slashReply(wsc, req, fmt.Sprintf("Removed calendar source %s.", parts[1]))
	case "agenda":
		scope := string(calendar.AgendaScopeToday)
		timezone := ""
		if len(parts) > 1 {
			scope = normalizeSlashAgendaScope(parts[1])
		}
		if len(parts) > 2 {
			timezone = parts[2]
		}
		h.slashCalendarAgenda(ctx, wsc, req, scope, timezone)
	default:
		h.slashCalendarAgenda(ctx, wsc, req, normalizeSlashAgendaScope(parts[0]), strings.Join(parts[1:], " "))
	}
}

func (h *Handler) slashCalendarAdd(ctx context.Context, wsc *wsConn, req *rpcRequest, payload string) {
	fields := strings.SplitN(payload, "|", 3)
	values := make([]string, 0, len(fields))
	for _, field := range fields {
		values = append(values, strings.TrimSpace(field))
	}
	var name, target, timezone string
	switch len(values) {
	case 1:
		target = values[0]
	case 2:
		name = values[0]
		target = values[1]
	case 3:
		name = values[0]
		target = values[1]
		timezone = values[2]
	default:
		slashReply(wsc, req, "Usage: /calendar add [<name> |] <path-or-url> [| <timezone>]")
		return
	}
	if target == "" {
		slashReply(wsc, req, "Usage: /calendar add [<name> |] <path-or-url> [| <timezone>]")
		return
	}
	input := calendar.SourceInput{Name: name, Timezone: timezone}
	if strings.HasPrefix(strings.ToLower(target), "http://") || strings.HasPrefix(strings.ToLower(target), "https://") {
		input.URL = target
	} else {
		input.Path = target
	}
	source, err := h.calendarStore.CreateSource(ctx, wsc.peerID, input)
	if err != nil {
		wsc.replyErr(req.ID, errCodeServer, "calendar add error: "+err.Error())
		return
	}
	slashReply(wsc, req, fmt.Sprintf("Created calendar source %s (%s).", source.ID, source.Name))
}

func (h *Handler) slashCalendarList(ctx context.Context, wsc *wsConn, req *rpcRequest) {
	sources, err := h.calendarStore.ListSources(ctx, wsc.peerID, false)
	if err != nil {
		wsc.replyErr(req.ID, errCodeServer, "calendar list error: "+err.Error())
		return
	}
	if len(sources) == 0 {
		slashReply(wsc, req, "No calendar sources.")
		return
	}
	var sb strings.Builder
	sb.WriteString("Calendar sources:")
	for _, source := range sources {
		target := source.Path
		if target == "" {
			target = source.URL
		}
		fmt.Fprintf(&sb, "\n- %s [%s] %s", source.ID, source.Kind, firstNonEmptySlashValue(source.Name, target))
		if target != "" && target != source.Name {
			fmt.Fprintf(&sb, " -> %s", target)
		}
	}
	slashReply(wsc, req, sb.String())
}

func (h *Handler) slashCalendarAgenda(ctx context.Context, wsc *wsConn, req *rpcRequest, scope, timezone string) {
	agenda, err := h.calendarStore.Agenda(ctx, wsc.peerID, calendar.AgendaQuery{Scope: scope, Timezone: timezone, Limit: 10}, h.fetchClient)
	if err != nil {
		wsc.replyErr(req.ID, errCodeServer, "calendar agenda error: "+err.Error())
		return
	}
	loc := time.Local
	if agenda.Timezone != "" {
		if loaded, loadErr := time.LoadLocation(agenda.Timezone); loadErr == nil {
			loc = loaded
		}
	}
	var lines []string
	if agenda.Conflict != nil {
		if len(agenda.Conflict.Events) == 0 {
			lines = append(lines, "No upcoming conflicts found.")
		} else {
			lines = append(lines, "Next conflict:")
			for _, event := range agenda.Conflict.Events {
				lines = append(lines, fmt.Sprintf("- %s (%s)", event.Summary, formatSlashCalendarEvent(event, loc)))
			}
		}
	} else {
		if len(agenda.Events) == 0 {
			lines = append(lines, "No events in the requested agenda window.")
		} else {
			lines = append(lines, fmt.Sprintf("Agenda (%s):", agenda.Scope))
			for _, event := range agenda.Events {
				lines = append(lines, fmt.Sprintf("- %s (%s)", event.Summary, formatSlashCalendarEvent(event, loc)))
			}
		}
	}
	for _, warning := range agenda.Warnings {
		lines = append(lines, "warning: "+warning)
	}
	slashReply(wsc, req, strings.Join(lines, "\n"))
}

func normalizeSlashAgendaScope(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", "today":
		return string(calendar.AgendaScopeToday)
	case "this-week", "this_week", "week":
		return string(calendar.AgendaScopeThisWeek)
	case "next-conflict", "next_conflict", "conflict":
		return string(calendar.AgendaScopeNextConflict)
	default:
		return string(calendar.AgendaScopeToday)
	}
}

func formatSlashCalendarEvent(event calendar.Event, loc *time.Location) string {
	start := time.Unix(event.StartAt, 0).In(loc)
	end := time.Unix(event.EndAt, 0).In(loc)
	if event.AllDay {
		return start.Format("2006-01-02") + " all day"
	}
	span := start.Format(time.RFC3339)
	if event.EndAt > 0 && event.EndAt != event.StartAt {
		span += " to " + end.Format(time.RFC3339)
	}
	if event.SourceName != "" {
		span += " | " + event.SourceName
	}
	return span
}

func firstNonEmptySlashValue(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
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
