package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/ffimnsr/koios/internal/agent"
	"github.com/ffimnsr/koios/internal/session"
	"github.com/ffimnsr/koios/internal/subagent"
	"github.com/ffimnsr/koios/internal/types"
)

func (h *Handler) executeSessionWorkspaceTool(ctx context.Context, peerID string, call agent.ToolCall) (any, error) {
	switch call.Name {
	case "session.reset":
		h.store.Reset(peerID)
		return map[string]bool{"ok": true}, nil
	case "workspace.list":
		var args struct {
			Path      string `json:"path"`
			Recursive bool   `json:"recursive"`
			Limit     int    `json:"limit"`
		}
		if err := json.Unmarshal(call.Arguments, &args); err != nil {
			return nil, fmt.Errorf("invalid arguments: %w", err)
		}
		return h.workspaceList(peerID, args.Path, args.Recursive, args.Limit)
	case "read", "workspace.read":
		var args struct {
			Path      string `json:"path"`
			StartLine int    `json:"start_line"`
			EndLine   int    `json:"end_line"`
		}
		if err := json.Unmarshal(call.Arguments, &args); err != nil {
			return nil, fmt.Errorf("invalid arguments: %w", err)
		}
		return h.workspaceRead(peerID, args.Path, args.StartLine, args.EndLine)
	case "head", "workspace.head":
		var args struct {
			Path  string `json:"path"`
			Lines int    `json:"lines"`
		}
		if err := json.Unmarshal(call.Arguments, &args); err != nil {
			return nil, fmt.Errorf("invalid arguments: %w", err)
		}
		if args.Lines == 0 {
			args.Lines = 10
		}
		return h.workspaceHead(peerID, args.Path, args.Lines)
	case "tail", "workspace.tail":
		var args struct {
			Path  string `json:"path"`
			Lines int    `json:"lines"`
		}
		if err := json.Unmarshal(call.Arguments, &args); err != nil {
			return nil, fmt.Errorf("invalid arguments: %w", err)
		}
		if args.Lines == 0 {
			args.Lines = 10
		}
		return h.workspaceTail(peerID, args.Path, args.Lines)
	case "grep", "workspace.grep":
		var args struct {
			Path          string `json:"path"`
			Pattern       string `json:"pattern"`
			Recursive     *bool  `json:"recursive"`
			Limit         int    `json:"limit"`
			CaseSensitive *bool  `json:"case_sensitive"`
			Regexp        bool   `json:"regexp"`
		}
		if err := json.Unmarshal(call.Arguments, &args); err != nil {
			return nil, fmt.Errorf("invalid arguments: %w", err)
		}
		recursive := true
		if args.Recursive != nil {
			recursive = *args.Recursive
		}
		caseSensitive := true
		if args.CaseSensitive != nil {
			caseSensitive = *args.CaseSensitive
		}
		return h.workspaceGrep(peerID, args.Path, args.Pattern, recursive, args.Limit, caseSensitive, args.Regexp)
	case "sort", "workspace.sort":
		var args struct {
			Path          string `json:"path"`
			Reverse       bool   `json:"reverse"`
			CaseSensitive *bool  `json:"case_sensitive"`
		}
		if err := json.Unmarshal(call.Arguments, &args); err != nil {
			return nil, fmt.Errorf("invalid arguments: %w", err)
		}
		caseSensitive := true
		if args.CaseSensitive != nil {
			caseSensitive = *args.CaseSensitive
		}
		return h.workspaceSort(peerID, args.Path, args.Reverse, caseSensitive)
	case "uniq", "workspace.uniq":
		var args struct {
			Path          string `json:"path"`
			Count         bool   `json:"count"`
			CaseSensitive *bool  `json:"case_sensitive"`
		}
		if err := json.Unmarshal(call.Arguments, &args); err != nil {
			return nil, fmt.Errorf("invalid arguments: %w", err)
		}
		caseSensitive := true
		if args.CaseSensitive != nil {
			caseSensitive = *args.CaseSensitive
		}
		return h.workspaceUniq(peerID, args.Path, args.Count, caseSensitive)
	case "diff", "workspace.diff":
		var args struct {
			Path      string `json:"path"`
			OtherPath string `json:"other_path"`
			Content   string `json:"content"`
			Context   int    `json:"context"`
		}
		if err := json.Unmarshal(call.Arguments, &args); err != nil {
			return nil, fmt.Errorf("invalid arguments: %w", err)
		}
		if args.Context == 0 {
			args.Context = 3
		}
		return h.workspaceDiff(peerID, args.Path, args.OtherPath, args.Content, args.Context)
	case "session.list":
		type sessionEntry struct {
			kind         string
			runID        string
			sessionKey   string
			status       any
			task         string
			createdAt    time.Time
			messageCount int
			replyBack    bool
		}
		entries := map[string]sessionEntry{
			peerID: {
				kind:         "main",
				sessionKey:   peerID,
				status:       "active",
				messageCount: len(h.store.Get(peerID).History()),
				replyBack:    h.store.Policy(peerID).ReplyBack,
			},
		}
		for _, key := range h.store.SessionKeys(peerID) {
			if _, ok := entries[key]; !ok {
				entries[key] = sessionEntry{
					kind:         "session",
					sessionKey:   key,
					status:       "available",
					messageCount: len(h.store.Get(key).History()),
					replyBack:    h.store.Policy(key).ReplyBack,
				}
			}
		}
		if h.subRuntime != nil {
			for _, rec := range h.subRuntime.List() {
				if rec.PeerID != peerID {
					continue
				}
				count := len(rec.Transcript)
				if count == 0 {
					count = len(h.store.Get(rec.SessionKey).History())
				}
				entries[rec.SessionKey] = sessionEntry{
					kind:         "subagent",
					runID:        rec.ID,
					sessionKey:   rec.SessionKey,
					status:       rec.Status,
					task:         rec.Task,
					createdAt:    rec.CreatedAt,
					messageCount: count,
					replyBack:    rec.ReplyBack || h.store.Policy(rec.SessionKey).ReplyBack,
				}
			}
		}
		sessions := make([]map[string]any, 0, len(entries))
		for _, entry := range entries {
			item := map[string]any{
				"kind":          entry.kind,
				"session_key":   entry.sessionKey,
				"status":        entry.status,
				"message_count": entry.messageCount,
				"reply_back":    entry.replyBack,
			}
			if entry.runID != "" {
				item["run_id"] = entry.runID
			}
			if entry.task != "" {
				item["task"] = entry.task
			}
			if !entry.createdAt.IsZero() {
				item["created_at"] = entry.createdAt
			}
			sessions = append(sessions, item)
		}
		sort.Slice(sessions, func(i, j int) bool {
			return fmt.Sprint(sessions[i]["session_key"]) < fmt.Sprint(sessions[j]["session_key"])
		})
		return map[string]any{"peer_id": peerID, "count": len(sessions), "sessions": sessions}, nil
	case "session.spawn":
		var args struct {
			Task               string `json:"task"`
			Wait               bool   `json:"wait"`
			WaitTimeoutSeconds int    `json:"wait_timeout_seconds"`
			ReplyBack          bool   `json:"reply_back"`
			AnnounceSkip       bool   `json:"announce_skip"`
			ReplySkip          bool   `json:"reply_skip"`
		}
		if err := json.Unmarshal(call.Arguments, &args); err != nil {
			return nil, fmt.Errorf("invalid arguments: %w", err)
		}
		sourceSessionKey := agent.CurrentSessionKey(ctx)
		if sourceSessionKey == "" {
			sourceSessionKey = peerID
		}
		rec, err := h.subRuntime.Spawn(ctx, subagent.SpawnRequest{
			PeerID:           peerID,
			ParentSessionKey: sourceSessionKey,
			SourceSessionKey: sourceSessionKey,
			Task:             args.Task,
			ReplyBack:        args.ReplyBack,
			PushToParent:     args.ReplyBack,
			AnnounceSkip:     args.AnnounceSkip,
			ReplySkip:        args.ReplySkip,
		})
		if err != nil {
			return nil, err
		}
		if err := h.store.SetPolicy(rec.SessionKey, session.SessionPolicy{ReplyBack: args.ReplyBack}); err != nil {
			return nil, err
		}
		if !args.Wait {
			return rec, nil
		}
		waitTimeout := 30 * time.Second
		if args.WaitTimeoutSeconds > 0 {
			waitTimeout = time.Duration(args.WaitTimeoutSeconds) * time.Second
		}
		waitCtx, cancel := context.WithTimeout(ctx, waitTimeout)
		defer cancel()
		for {
			current, ok := h.subRuntime.Get(rec.ID)
			if !ok {
				return nil, fmt.Errorf("run %s not found", rec.ID)
			}
			switch current.Status {
			case subagent.StatusCompleted, subagent.StatusErrored, subagent.StatusKilled:
				msgs, err := h.subRuntime.Transcript(rec.ID)
				if err != nil {
					return nil, err
				}
				return map[string]any{
					"run":         current,
					"count":       len(msgs),
					"messages":    msgs,
					"completed":   current.Status == subagent.StatusCompleted,
					"session_key": current.SessionKey,
				}, nil
			}
			select {
			case <-waitCtx.Done():
				return map[string]any{
					"status":  "timeout",
					"run":     current,
					"message": "sub-session is still running",
				}, nil
			case <-time.After(100 * time.Millisecond):
			}
		}
	case "session.send":
		var args struct {
			SessionKey         string `json:"session_key"`
			RunID              string `json:"run_id"`
			Message            string `json:"message"`
			ReplyBack          *bool  `json:"reply_back"`
			WaitTimeoutSeconds int    `json:"wait_timeout_seconds"`
		}
		if err := json.Unmarshal(call.Arguments, &args); err != nil {
			return nil, fmt.Errorf("invalid arguments: %w", err)
		}
		sourceSessionKey := agent.CurrentSessionKey(ctx)
		if sourceSessionKey == "" {
			sourceSessionKey = peerID
		}
		targetSessionKey := strings.TrimSpace(args.SessionKey)
		var rec *subagent.RunRecord
		if strings.TrimSpace(args.RunID) != "" {
			var ok bool
			rec, ok = h.subRuntime.Get(args.RunID)
			if !ok || rec.PeerID != peerID {
				return nil, fmt.Errorf("run %s not found", args.RunID)
			}
			targetSessionKey = rec.SessionKey
		}
		if targetSessionKey == "" {
			return nil, fmt.Errorf("session_key or run_id is required")
		}
		if targetSessionKey != peerID && !strings.HasPrefix(targetSessionKey, peerID+"::") {
			return nil, fmt.Errorf("session_key %q is not accessible to peer %q", targetSessionKey, peerID)
		}
		if targetSessionKey == sourceSessionKey {
			return nil, fmt.Errorf("session.send target must differ from source session")
		}
		var replyBack bool
		if args.ReplyBack != nil {
			replyBack = *args.ReplyBack
			if err := h.store.SetPolicy(targetSessionKey, session.SessionPolicy{ReplyBack: replyBack}); err != nil {
				return nil, err
			}
			if rec != nil {
				updated, err := h.subRuntime.SetReplyBack(args.RunID, *args.ReplyBack)
				if err != nil {
					return nil, err
				}
				rec = updated
			}
		} else {
			replyBack = h.store.Policy(targetSessionKey).ReplyBack
			if rec != nil && rec.ReplyBack {
				replyBack = true
			}
		}
		if rec != nil && strings.TrimSpace(args.Message) != "" &&
			(rec.Status == subagent.StatusQueued || rec.Status == subagent.StatusRunning) {
			updated, err := h.subRuntime.Steer(args.RunID, args.Message)
			if err != nil {
				return nil, err
			}
			return updated, nil
		}
		if strings.TrimSpace(args.Message) == "" {
			return map[string]any{
				"ok":          true,
				"session_key": targetSessionKey,
				"reply_back":  replyBack,
			}, nil
		}
		timeout := h.timeout
		if args.WaitTimeoutSeconds > 0 {
			timeout = time.Duration(args.WaitTimeoutSeconds) * time.Second
		}
		sendCtx, cancel := context.WithTimeout(ctx, timeout)
		defer cancel()
		result, err := h.agentCoord.Run(sendCtx, agent.RunRequest{
			PeerID:       peerID,
			SessionKey:   targetSessionKey,
			Messages:     []types.Message{{Role: "user", Content: args.Message}},
			MaxSteps:     toolLoopMaxSteps,
			ToolExecutor: h,
			Timeout:      timeout,
		})
		if err != nil {
			return nil, err
		}
		if replyBack && strings.TrimSpace(result.AssistantText) != "" {
			h.publishSessionMessage(sourceSessionKey, "session.send", types.Message{
				Role:    "assistant",
				Content: fmt.Sprintf("[reply:%s] %s", targetSessionKey, result.AssistantText),
			}, map[string]any{"target_session_key": targetSessionKey, "kind": "reply_back"})
		}
		return map[string]any{
			"ok":             true,
			"source_session": sourceSessionKey,
			"session_key":    targetSessionKey,
			"reply_back":     replyBack,
			"result":         result,
		}, nil
	case "session.patch":
		var args struct {
			SessionKey       string  `json:"session_key"`
			RunID            string  `json:"run_id"`
			ReplyBack        *bool   `json:"reply_back"`
			UsageMode        *string `json:"usage_mode"`
			ModelOverride    *string `json:"model_override"`
			ActiveProfile    *string `json:"active_profile"`
			QueueMode        *string `json:"queue_mode"`
			BlockStream      *bool   `json:"block_stream"`
			StreamChunkChars *int    `json:"stream_chunk_chars"`
			StreamCoalesceMS *int    `json:"stream_coalesce_ms"`
		}
		if err := json.Unmarshal(call.Arguments, &args); err != nil {
			return nil, fmt.Errorf("invalid arguments: %w", err)
		}
		if args.ReplyBack == nil && args.UsageMode == nil && args.ModelOverride == nil && args.ActiveProfile == nil && args.QueueMode == nil && args.BlockStream == nil && args.StreamChunkChars == nil && args.StreamCoalesceMS == nil {
			return nil, fmt.Errorf("at least one policy field is required")
		}
		targetSessionKey := strings.TrimSpace(args.SessionKey)
		var rec *subagent.RunRecord
		if strings.TrimSpace(args.RunID) != "" {
			var ok bool
			rec, ok = h.subRuntime.Get(args.RunID)
			if !ok || rec.PeerID != peerID {
				return nil, fmt.Errorf("run %s not found", args.RunID)
			}
			targetSessionKey = rec.SessionKey
		}
		if targetSessionKey == "" {
			return nil, fmt.Errorf("session_key or run_id is required")
		}
		if targetSessionKey != peerID && !strings.HasPrefix(targetSessionKey, peerID+"::") {
			return nil, fmt.Errorf("session_key %q is not accessible to peer %q", targetSessionKey, peerID)
		}
		// Read the existing policy so we patch rather than overwrite.
		policy := h.store.Policy(targetSessionKey)
		if args.ReplyBack != nil {
			policy.ReplyBack = *args.ReplyBack
		}
		if args.UsageMode != nil {
			mode, ok := normalizeUsageMode(*args.UsageMode)
			if !ok {
				return nil, fmt.Errorf("usage_mode must be one of: off, tokens, full")
			}
			policy.UsageMode = mode
		}
		if args.ModelOverride != nil {
			policy.ModelOverride = strings.TrimSpace(*args.ModelOverride)
		}
		if args.ActiveProfile != nil {
			name := strings.TrimSpace(*args.ActiveProfile)
			if name != "" {
				if h.standingManager == nil {
					return nil, fmt.Errorf("standing profiles are not enabled")
				}
				if _, err := h.standingManager.ResolveProfile(peerID, name); err != nil {
					return nil, err
				}
			}
			policy.ActiveProfile = name
		}
		if args.QueueMode != nil {
			policy.QueueMode = agent.NormalizeQueueMode(*args.QueueMode)
		}
		if args.BlockStream != nil {
			policy.BlockStream = *args.BlockStream
		}
		if args.StreamChunkChars != nil {
			if *args.StreamChunkChars < 0 {
				return nil, fmt.Errorf("stream_chunk_chars must be >= 0")
			}
			policy.StreamChunkChars = *args.StreamChunkChars
		}
		if args.StreamCoalesceMS != nil {
			if *args.StreamCoalesceMS < 0 {
				return nil, fmt.Errorf("stream_coalesce_ms must be >= 0")
			}
			policy.StreamCoalesceMS = *args.StreamCoalesceMS
		}
		if err := h.store.SetPolicy(targetSessionKey, policy); err != nil {
			return nil, err
		}
		if rec != nil && args.ReplyBack != nil {
			if _, err := h.subRuntime.SetReplyBack(rec.ID, *args.ReplyBack); err != nil {
				return nil, err
			}
		}
		result := map[string]any{
			"ok":          true,
			"session_key": targetSessionKey,
		}
		if args.ReplyBack != nil {
			result["reply_back"] = *args.ReplyBack
		}
		if args.UsageMode != nil {
			result["usage_mode"] = usageModeLabel(policy.UsageMode)
		}
		if args.ModelOverride != nil {
			result["model_override"] = policy.ModelOverride
		}
		if args.ActiveProfile != nil {
			result["active_profile"] = policy.ActiveProfile
		}
		if args.QueueMode != nil {
			result["queue_mode"] = policy.QueueMode
		}
		if args.BlockStream != nil {
			result["block_stream"] = policy.BlockStream
		}
		if args.StreamChunkChars != nil {
			result["stream_chunk_chars"] = policy.StreamChunkChars
		}
		if args.StreamCoalesceMS != nil {
			result["stream_coalesce_ms"] = policy.StreamCoalesceMS
		}
		return result, nil
	default:
		return nil, errUnhandledTool
	}
}
