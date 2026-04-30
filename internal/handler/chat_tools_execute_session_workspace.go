package handler

import (
	"context"
	"encoding/json"
	"errors"
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
	case "find", "workspace.find":
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
		return h.workspaceFind(peerID, args.Path, args.Pattern, recursive, args.Limit, caseSensitive, args.Regexp)
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
			BrowserProfile   *string `json:"browser_profile"`
			QueueMode        *string `json:"queue_mode"`
			BlockStream      *bool   `json:"block_stream"`
			StreamChunkChars *int    `json:"stream_chunk_chars"`
			StreamCoalesceMS *int    `json:"stream_coalesce_ms"`
		}
		if err := json.Unmarshal(call.Arguments, &args); err != nil {
			return nil, fmt.Errorf("invalid arguments: %w", err)
		}
		if args.ReplyBack == nil && args.UsageMode == nil && args.ModelOverride == nil && args.ActiveProfile == nil && args.BrowserProfile == nil && args.QueueMode == nil && args.BlockStream == nil && args.StreamChunkChars == nil && args.StreamCoalesceMS == nil {
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
		if args.BrowserProfile != nil {
			name := strings.TrimSpace(*args.BrowserProfile)
			if name != "" {
				found := false
				for _, profile := range h.browserEnabledProfiles() {
					if strings.EqualFold(strings.TrimSpace(profile.Name), name) {
						name = strings.TrimSpace(profile.Name)
						found = true
						break
					}
				}
				if !found {
					return nil, fmt.Errorf("browser profile %q not found", name)
				}
			}
			policy.BrowserProfile = name
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
		if args.BrowserProfile != nil {
			result["browser_profile"] = policy.BrowserProfile
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
	case "browser.profile.list":
		toolCtx, _ := agent.ToolRunContextFromContext(ctx)
		sessionKey := strings.TrimSpace(toolCtx.SessionKey)
		if sessionKey == "" {
			sessionKey = peerID
		}
		activeProfile := h.resolveActiveBrowserProfile(sessionKey)
		profiles := make([]map[string]any, 0, len(h.browserEnabledProfiles()))
		for _, profile := range h.browserEnabledProfiles() {
			name := strings.TrimSpace(profile.Name)
			server, _ := h.mcpManager.ServerStatus("browser", name)
			entry := browserProfilePayload(profile, activeProfile, server)
			entry["is_default"] = strings.EqualFold(name, strings.TrimSpace(h.browserConfig.DefaultProfile))
			profiles = append(profiles, entry)
		}
		return map[string]any{
			"ok":              true,
			"default_profile": strings.TrimSpace(h.browserConfig.DefaultProfile),
			"active_profile":  activeProfile,
			"profiles":        profiles,
		}, nil
	case "browser.profile.use":
		var args struct {
			SessionKey string `json:"session_key"`
			RunID      string `json:"run_id"`
			Profile    string `json:"profile"`
		}
		if err := json.Unmarshal(call.Arguments, &args); err != nil {
			return nil, fmt.Errorf("invalid arguments: %w", err)
		}
		raw, err := json.Marshal(map[string]any{
			"session_key":     args.SessionKey,
			"run_id":          args.RunID,
			"browser_profile": args.Profile,
		})
		if err != nil {
			return nil, err
		}
		return h.executeSessionWorkspaceTool(ctx, peerID, agent.ToolCall{Name: "session.patch", Arguments: raw})
	case "browser.status":
		sessionKey := h.browserSessionKey(ctx, peerID)
		activeProfile := h.resolveActiveBrowserProfile(sessionKey)
		toolNames := make([]string, 0)
		for _, def := range h.browserAliasDefs(sessionKey) {
			toolNames = append(toolNames, def.name)
		}
		server, _ := h.browserServerStatus(sessionKey)
		profile, ok := h.activeBrowserProfile(sessionKey)
		profilePayload := map[string]any{}
		if ok {
			profilePayload = browserProfilePayload(profile, activeProfile, server)
		}
		return map[string]any{
			"ok":               true,
			"default_profile":  strings.TrimSpace(h.browserConfig.DefaultProfile),
			"active_profile":   activeProfile,
			"tool_count":       len(toolNames),
			"available_tools":  toolNames,
			"configured_count": len(h.browserEnabledProfiles()),
			"server":           browserStatusPayload(activeProfile, server),
			"profile":          profilePayload,
		}, nil
	case "browser.start":
		sessionKey := h.browserSessionKey(ctx, peerID)
		profile, server, err := h.ensureBrowserServer(ctx, sessionKey)
		if err != nil {
			return nil, err
		}
		pages, err := h.invokeBrowserAliasTool(ctx, peerID, sessionKey, "browser.list_pages", map[string]any{})
		if err != nil {
			return nil, err
		}
		return map[string]any{
			"ok":             true,
			"active_profile": strings.TrimSpace(profile.Name),
			"mode":           browserMode(profile.Mode),
			"profile":        browserProfilePayload(profile, strings.TrimSpace(profile.Name), server),
			"server":         browserStatusPayload(profile.Name, server),
			"pages":          normalizeBrowserToolResult(pages),
			"source_tool":    "browser.list_pages",
		}, nil
	case "browser.stop":
		sessionKey := h.browserSessionKey(ctx, peerID)
		profile, ok := h.activeBrowserProfile(sessionKey)
		if !ok {
			return nil, fmt.Errorf("no browser profile is active for session %q", sessionKey)
		}
		if h.mcpManager == nil {
			return nil, fmt.Errorf("browser MCP manager is not configured")
		}
		server, err := h.mcpManager.StopServer("browser", strings.TrimSpace(profile.Name))
		if err != nil {
			return nil, err
		}
		return map[string]any{
			"ok":                          true,
			"active_profile":              strings.TrimSpace(profile.Name),
			"mode":                        browserMode(profile.Mode),
			"profile":                     browserProfilePayload(profile, strings.TrimSpace(profile.Name), server),
			"server":                      browserStatusPayload(profile.Name, server),
			"external_browser_may_remain": browserMode(profile.Mode) != "managed",
		}, nil
	case "browser.tabs":
		sessionKey := h.browserSessionKey(ctx, peerID)
		profile, server, err := h.ensureBrowserServer(ctx, sessionKey)
		if err != nil {
			return nil, err
		}
		pages, err := h.invokeBrowserAliasTool(ctx, peerID, sessionKey, "browser.list_pages", map[string]any{})
		if err != nil {
			return nil, err
		}
		return map[string]any{
			"ok":             true,
			"active_profile": strings.TrimSpace(profile.Name),
			"server":         browserStatusPayload(profile.Name, server),
			"pages":          normalizeBrowserToolResult(pages),
			"source_tool":    "browser.list_pages",
		}, nil
	case "browser.open":
		var args struct {
			URL        string `json:"url"`
			Background bool   `json:"background"`
			TimeoutMS  int    `json:"timeout_ms"`
		}
		if err := json.Unmarshal(call.Arguments, &args); err != nil {
			return nil, fmt.Errorf("invalid arguments: %w", err)
		}
		sessionKey := h.browserSessionKey(ctx, peerID)
		profile, ok := h.activeBrowserProfile(sessionKey)
		if !ok {
			return nil, fmt.Errorf("no browser profile is active for session %q", sessionKey)
		}
		if err := guardBrowserURLForProfile(args.URL, profile); err != nil {
			return nil, err
		}
		profile, server, err := h.ensureBrowserServer(ctx, sessionKey)
		if err != nil {
			return nil, err
		}
		result, err := h.invokeBrowserAliasTool(ctx, peerID, sessionKey, "browser.new_page", map[string]any{
			"url":        args.URL,
			"background": args.Background,
			"timeout":    args.TimeoutMS,
		})
		if err != nil {
			return nil, err
		}
		return map[string]any{
			"ok":             true,
			"active_profile": strings.TrimSpace(profile.Name),
			"server":         browserStatusPayload(profile.Name, server),
			"result":         normalizeBrowserToolResult(result),
			"source_tool":    "browser.new_page",
		}, nil
	case "browser.focus":
		var args struct {
			PageID       int   `json:"page_id"`
			BringToFront *bool `json:"bring_to_front"`
		}
		if err := json.Unmarshal(call.Arguments, &args); err != nil {
			return nil, fmt.Errorf("invalid arguments: %w", err)
		}
		sessionKey := h.browserSessionKey(ctx, peerID)
		profile, server, err := h.ensureBrowserServer(ctx, sessionKey)
		if err != nil {
			return nil, err
		}
		bringToFront := true
		if args.BringToFront != nil {
			bringToFront = *args.BringToFront
		}
		result, err := h.invokeBrowserAliasTool(ctx, peerID, sessionKey, "browser.select_page", map[string]any{
			"pageId":       args.PageID,
			"bringToFront": bringToFront,
		})
		if err != nil {
			return nil, err
		}
		return map[string]any{
			"ok":             true,
			"active_profile": strings.TrimSpace(profile.Name),
			"server":         browserStatusPayload(profile.Name, server),
			"result":         normalizeBrowserToolResult(result),
			"source_tool":    "browser.select_page",
		}, nil
	case "browser.close":
		var args struct {
			PageID int `json:"page_id"`
		}
		if err := json.Unmarshal(call.Arguments, &args); err != nil {
			return nil, fmt.Errorf("invalid arguments: %w", err)
		}
		sessionKey := h.browserSessionKey(ctx, peerID)
		profile, server, err := h.ensureBrowserServer(ctx, sessionKey)
		if err != nil {
			return nil, err
		}
		result, err := h.invokeBrowserAliasTool(ctx, peerID, sessionKey, "browser.close_page", map[string]any{
			"pageId": args.PageID,
		})
		if err != nil {
			return nil, err
		}
		return map[string]any{
			"ok":             true,
			"active_profile": strings.TrimSpace(profile.Name),
			"server":         browserStatusPayload(profile.Name, server),
			"result":         normalizeBrowserToolResult(result),
			"source_tool":    "browser.close_page",
		}, nil
	case "screen.snapshot":
		return h.executeScreenSnapshotTool(ctx, peerID, call)
	case "screen.record":
		return h.executeScreenRecordTool(ctx, peerID, call)
	case "canvas.push":
		return h.executeCanvasPushTool(ctx, peerID, call)
	case "canvas.eval":
		return h.executeCanvasEvalTool(ctx, peerID, call)
	case "canvas.snapshot":
		return h.executeCanvasSnapshotTool(ctx, peerID, call)
	case "canvas.reset":
		return h.executeCanvasResetTool(ctx, peerID, call)
	case "browser.snapshot":
		var args struct {
			PageID  int    `json:"page_id"`
			Format  string `json:"format"`
			Verbose bool   `json:"verbose"`
		}
		if err := json.Unmarshal(call.Arguments, &args); err != nil {
			return nil, fmt.Errorf("invalid arguments: %w", err)
		}
		format := strings.ToLower(strings.TrimSpace(args.Format))
		if format == "" {
			format = "ai"
		}
		if format != "ai" && format != "role" && format != "accessibility" {
			return nil, fmt.Errorf("unsupported browser snapshot format %q", args.Format)
		}
		sessionKey := h.browserSessionKey(ctx, peerID)
		profile, server, err := h.ensureBrowserServer(ctx, sessionKey)
		if err != nil {
			return nil, err
		}
		result, err := h.invokeBrowserPageTool(ctx, peerID, sessionKey, args.PageID, "browser.take_snapshot", map[string]any{
			"verbose": args.Verbose,
		})
		if errors.Is(err, errUnhandledTool) {
			return nil, fmt.Errorf("active browser profile %q does not expose browser.take_snapshot", strings.TrimSpace(profile.Name))
		}
		if err != nil {
			return nil, err
		}
		rawSnapshot := strings.TrimSpace(fmt.Sprint(result))
		lines := parseBrowserSnapshotLines(rawSnapshot)
		nodes, refs := h.storeBrowserSnapshotRefs(sessionKey, strings.TrimSpace(profile.Name), args.PageID, lines)
		response := map[string]any{
			"ok":             true,
			"active_profile": strings.TrimSpace(profile.Name),
			"page_id":        args.PageID,
			"format":         format,
			"server":         browserStatusPayload(profile.Name, server),
			"raw_snapshot":   rawSnapshot,
			"source_tool":    "browser.take_snapshot",
		}
		switch format {
		case "role":
			response["snapshot"] = formatBrowserRoleSnapshot(lines)
			response["nodes"] = nodes
			response["refs"] = refs
			response["ref_count"] = len(refs)
		case "accessibility":
			tree, flatNodes, treeRefs := buildBrowserAccessibilityTree(lines)
			response["snapshot"] = formatBrowserSnapshotWithRefs(lines)
			response["nodes"] = flatNodes
			response["tree"] = snapshotTreeToMaps(tree)
			response["refs"] = treeRefs
			response["ref_count"] = len(treeRefs)
		default:
			response["snapshot"] = formatBrowserSnapshotWithRefs(lines)
			response["nodes"] = nodes
			response["refs"] = refs
			response["ref_count"] = len(refs)
		}
		return response, nil
	case "browser.screenshot":
		var args struct {
			PageID   int    `json:"page_id"`
			UID      string `json:"uid"`
			Ref      string `json:"ref"`
			FullPage bool   `json:"full_page"`
			Format   string `json:"format"`
			Quality  int    `json:"quality"`
		}
		if err := json.Unmarshal(call.Arguments, &args); err != nil {
			return nil, fmt.Errorf("invalid arguments: %w", err)
		}
		if args.FullPage && (strings.TrimSpace(args.UID) != "" || strings.TrimSpace(args.Ref) != "") {
			return nil, fmt.Errorf("browser.screenshot cannot combine full_page with uid or ref targeting")
		}
		format := strings.ToLower(strings.TrimSpace(args.Format))
		if format == "" {
			format = "png"
		}
		if format != "png" && format != "jpeg" && format != "webp" {
			return nil, fmt.Errorf("unsupported browser screenshot format %q", args.Format)
		}
		sessionKey := h.browserSessionKey(ctx, peerID)
		profile, server, err := h.ensureBrowserServer(ctx, sessionKey)
		if err != nil {
			return nil, err
		}
		screenshotArgs := map[string]any{"format": format}
		if args.Quality > 0 {
			screenshotArgs["quality"] = args.Quality
		}
		if args.FullPage {
			screenshotArgs["fullPage"] = true
		} else if strings.TrimSpace(args.UID) != "" || strings.TrimSpace(args.Ref) != "" {
			resolvedUID, err := h.resolveBrowserActionUID(sessionKey, strings.TrimSpace(profile.Name), args.PageID, args.UID, args.Ref)
			if err != nil {
				return nil, err
			}
			screenshotArgs["uid"] = resolvedUID
		}
		result, err := h.invokeBrowserPageToolResult(ctx, peerID, sessionKey, args.PageID, "browser.take_screenshot", screenshotArgs)
		if errors.Is(err, errUnhandledTool) {
			return nil, fmt.Errorf("active browser profile %q does not expose browser.take_screenshot", strings.TrimSpace(profile.Name))
		}
		if err != nil {
			return nil, err
		}
		imageData, mimeType, err := extractBrowserImageContent(result)
		if err != nil {
			return nil, err
		}
		return map[string]any{
			"ok":             true,
			"active_profile": strings.TrimSpace(profile.Name),
			"page_id":        args.PageID,
			"format":         format,
			"mime_type":      mimeType,
			"data_base64":    imageData,
			"server":         browserStatusPayload(profile.Name, server),
			"source_tool":    "browser.take_screenshot",
		}, nil
	case "browser.pdf":
		var args struct {
			PageID            int     `json:"page_id"`
			Landscape         bool    `json:"landscape"`
			PrintBackground   bool    `json:"print_background"`
			Scale             float64 `json:"scale"`
			PaperWidthIn      float64 `json:"paper_width_in"`
			PaperHeightIn     float64 `json:"paper_height_in"`
			MarginTopIn       float64 `json:"margin_top_in"`
			MarginBottomIn    float64 `json:"margin_bottom_in"`
			MarginLeftIn      float64 `json:"margin_left_in"`
			MarginRightIn     float64 `json:"margin_right_in"`
			PreferCSSPageSize bool    `json:"prefer_css_page_size"`
		}
		if err := json.Unmarshal(call.Arguments, &args); err != nil {
			return nil, fmt.Errorf("invalid arguments: %w", err)
		}
		sessionKey := h.browserSessionKey(ctx, peerID)
		profile, server, err := h.ensureBrowserServer(ctx, sessionKey)
		if err != nil {
			return nil, err
		}
		pdfArgs := map[string]any{}
		if args.Landscape {
			pdfArgs["landscape"] = true
		}
		if args.PrintBackground {
			pdfArgs["printBackground"] = true
		}
		if args.Scale > 0 {
			pdfArgs["scale"] = args.Scale
		}
		if args.PaperWidthIn > 0 {
			pdfArgs["paperWidth"] = args.PaperWidthIn
		}
		if args.PaperHeightIn > 0 {
			pdfArgs["paperHeight"] = args.PaperHeightIn
		}
		if args.MarginTopIn > 0 {
			pdfArgs["marginTop"] = args.MarginTopIn
		}
		if args.MarginBottomIn > 0 {
			pdfArgs["marginBottom"] = args.MarginBottomIn
		}
		if args.MarginLeftIn > 0 {
			pdfArgs["marginLeft"] = args.MarginLeftIn
		}
		if args.MarginRightIn > 0 {
			pdfArgs["marginRight"] = args.MarginRightIn
		}
		if args.PreferCSSPageSize {
			pdfArgs["preferCSSPageSize"] = true
		}
		result, sourceTool, err := h.invokeBrowserPageToolResultAny(ctx, peerID, sessionKey, args.PageID, []string{"browser.print_to_pdf", "browser.take_pdf"}, pdfArgs)
		if errors.Is(err, errUnhandledTool) {
			return nil, fmt.Errorf("active browser profile %q does not expose a PDF export tool", strings.TrimSpace(profile.Name))
		}
		if err != nil {
			return nil, err
		}
		pdfData, mimeType, err := extractBrowserPDFContent(result)
		if err != nil {
			return nil, err
		}
		return map[string]any{
			"ok":             true,
			"active_profile": strings.TrimSpace(profile.Name),
			"page_id":        args.PageID,
			"mime_type":      mimeType,
			"data_base64":    pdfData,
			"server":         browserStatusPayload(profile.Name, server),
			"source_tool":    sourceTool,
		}, nil
	case "browser.click":
		var args struct {
			PageID      int    `json:"page_id"`
			UID         string `json:"uid"`
			Ref         string `json:"ref"`
			DoubleClick bool   `json:"double_click"`
		}
		if err := json.Unmarshal(call.Arguments, &args); err != nil {
			return nil, fmt.Errorf("invalid arguments: %w", err)
		}
		sessionKey := h.browserSessionKey(ctx, peerID)
		profile, server, err := h.ensureBrowserServer(ctx, sessionKey)
		if err != nil {
			return nil, err
		}
		resolvedUID, err := h.resolveBrowserActionUID(sessionKey, strings.TrimSpace(profile.Name), args.PageID, args.UID, args.Ref)
		if err != nil {
			return nil, err
		}
		clickArgs := map[string]any{"uid": resolvedUID}
		if args.DoubleClick {
			clickArgs["dblClick"] = true
		}
		result, err := h.invokeBrowserPageTool(ctx, peerID, sessionKey, args.PageID, "browser.click", clickArgs)
		if errors.Is(err, errUnhandledTool) {
			return nil, fmt.Errorf("active browser profile %q does not expose browser.click", strings.TrimSpace(profile.Name))
		}
		if err != nil {
			return nil, err
		}
		return map[string]any{
			"ok":             true,
			"active_profile": strings.TrimSpace(profile.Name),
			"server":         browserStatusPayload(profile.Name, server),
			"result":         normalizeBrowserToolResult(result),
			"source_tool":    "browser.click",
		}, nil
	case "browser.click_coords":
		var args struct {
			PageID      int    `json:"page_id"`
			X           int    `json:"x"`
			Y           int    `json:"y"`
			DoubleClick bool   `json:"double_click"`
			Button      string `json:"button"`
			DelayMS     int    `json:"delay_ms"`
		}
		if err := json.Unmarshal(call.Arguments, &args); err != nil {
			return nil, fmt.Errorf("invalid arguments: %w", err)
		}
		sessionKey := h.browserSessionKey(ctx, peerID)
		profile, server, err := h.ensureBrowserServer(ctx, sessionKey)
		if err != nil {
			return nil, err
		}
		result, err := h.invokeBrowserPageTool(ctx, peerID, sessionKey, args.PageID, "browser.evaluate_script", map[string]any{
			"function": buildBrowserClickCoordsFunction(args.X, args.Y, args.Button, args.DoubleClick, args.DelayMS),
		})
		if errors.Is(err, errUnhandledTool) {
			return nil, fmt.Errorf("active browser profile %q does not expose browser.evaluate_script", strings.TrimSpace(profile.Name))
		}
		if err != nil {
			return nil, err
		}
		return map[string]any{
			"ok":             true,
			"active_profile": strings.TrimSpace(profile.Name),
			"server":         browserStatusPayload(profile.Name, server),
			"result":         normalizeBrowserToolResult(result),
			"source_tool":    "browser.evaluate_script",
		}, nil
	case "browser.type":
		var args struct {
			PageID int    `json:"page_id"`
			UID    string `json:"uid"`
			Ref    string `json:"ref"`
			Text   string `json:"text"`
		}
		if err := json.Unmarshal(call.Arguments, &args); err != nil {
			return nil, fmt.Errorf("invalid arguments: %w", err)
		}
		sessionKey := h.browserSessionKey(ctx, peerID)
		profile, server, err := h.ensureBrowserServer(ctx, sessionKey)
		if err != nil {
			return nil, err
		}
		resolvedUID, err := h.resolveBrowserActionUID(sessionKey, strings.TrimSpace(profile.Name), args.PageID, args.UID, args.Ref)
		if err != nil {
			return nil, err
		}
		result, err := h.invokeBrowserPageTool(ctx, peerID, sessionKey, args.PageID, "browser.fill", map[string]any{
			"uid":   resolvedUID,
			"value": args.Text,
		})
		if errors.Is(err, errUnhandledTool) {
			return nil, fmt.Errorf("active browser profile %q does not expose browser.fill", strings.TrimSpace(profile.Name))
		}
		if err != nil {
			return nil, err
		}
		return map[string]any{
			"ok":             true,
			"active_profile": strings.TrimSpace(profile.Name),
			"server":         browserStatusPayload(profile.Name, server),
			"result":         normalizeBrowserToolResult(result),
			"source_tool":    "browser.fill",
		}, nil
	case "browser.press":
		var args struct {
			PageID int    `json:"page_id"`
			Key    string `json:"key"`
		}
		if err := json.Unmarshal(call.Arguments, &args); err != nil {
			return nil, fmt.Errorf("invalid arguments: %w", err)
		}
		sessionKey := h.browserSessionKey(ctx, peerID)
		profile, server, err := h.ensureBrowserServer(ctx, sessionKey)
		if err != nil {
			return nil, err
		}
		result, err := h.invokeBrowserPageTool(ctx, peerID, sessionKey, args.PageID, "browser.press_key", map[string]any{
			"key": args.Key,
		})
		if errors.Is(err, errUnhandledTool) {
			return nil, fmt.Errorf("active browser profile %q does not expose browser.press_key", strings.TrimSpace(profile.Name))
		}
		if err != nil {
			return nil, err
		}
		return map[string]any{
			"ok":             true,
			"active_profile": strings.TrimSpace(profile.Name),
			"server":         browserStatusPayload(profile.Name, server),
			"result":         normalizeBrowserToolResult(result),
			"source_tool":    "browser.press_key",
		}, nil
	case "browser.submit":
		var args struct {
			PageID int `json:"page_id"`
		}
		if err := json.Unmarshal(call.Arguments, &args); err != nil {
			return nil, fmt.Errorf("invalid arguments: %w", err)
		}
		sessionKey := h.browserSessionKey(ctx, peerID)
		profile, server, err := h.ensureBrowserServer(ctx, sessionKey)
		if err != nil {
			return nil, err
		}
		result, err := h.invokeBrowserPageTool(ctx, peerID, sessionKey, args.PageID, "browser.press_key", map[string]any{
			"key": "Enter",
		})
		if errors.Is(err, errUnhandledTool) {
			return nil, fmt.Errorf("active browser profile %q does not expose browser.press_key", strings.TrimSpace(profile.Name))
		}
		if err != nil {
			return nil, err
		}
		return map[string]any{
			"ok":             true,
			"active_profile": strings.TrimSpace(profile.Name),
			"server":         browserStatusPayload(profile.Name, server),
			"result":         normalizeBrowserToolResult(result),
			"source_tool":    "browser.press_key",
		}, nil
	case "browser.drag":
		var args struct {
			PageID  int    `json:"page_id"`
			FromUID string `json:"from_uid"`
			FromRef string `json:"from_ref"`
			ToUID   string `json:"to_uid"`
			ToRef   string `json:"to_ref"`
		}
		if err := json.Unmarshal(call.Arguments, &args); err != nil {
			return nil, fmt.Errorf("invalid arguments: %w", err)
		}
		sessionKey := h.browserSessionKey(ctx, peerID)
		profile, server, err := h.ensureBrowserServer(ctx, sessionKey)
		if err != nil {
			return nil, err
		}
		fromUID, err := h.resolveBrowserActionUID(sessionKey, strings.TrimSpace(profile.Name), args.PageID, args.FromUID, args.FromRef)
		if err != nil {
			return nil, err
		}
		toUID, err := h.resolveBrowserActionUID(sessionKey, strings.TrimSpace(profile.Name), args.PageID, args.ToUID, args.ToRef)
		if err != nil {
			return nil, err
		}
		result, err := h.invokeBrowserPageTool(ctx, peerID, sessionKey, args.PageID, "browser.drag", map[string]any{
			"from_uid": fromUID,
			"to_uid":   toUID,
		})
		if errors.Is(err, errUnhandledTool) {
			return nil, fmt.Errorf("active browser profile %q does not expose browser.drag", strings.TrimSpace(profile.Name))
		}
		if err != nil {
			return nil, err
		}
		return map[string]any{
			"ok":             true,
			"active_profile": strings.TrimSpace(profile.Name),
			"server":         browserStatusPayload(profile.Name, server),
			"result":         normalizeBrowserToolResult(result),
			"source_tool":    "browser.drag",
		}, nil
	case "browser.select":
		var args struct {
			PageID int    `json:"page_id"`
			UID    string `json:"uid"`
			Ref    string `json:"ref"`
			Value  string `json:"value"`
		}
		if err := json.Unmarshal(call.Arguments, &args); err != nil {
			return nil, fmt.Errorf("invalid arguments: %w", err)
		}
		sessionKey := h.browserSessionKey(ctx, peerID)
		profile, server, err := h.ensureBrowserServer(ctx, sessionKey)
		if err != nil {
			return nil, err
		}
		resolvedUID, err := h.resolveBrowserActionUID(sessionKey, strings.TrimSpace(profile.Name), args.PageID, args.UID, args.Ref)
		if err != nil {
			return nil, err
		}
		result, err := h.invokeBrowserPageTool(ctx, peerID, sessionKey, args.PageID, "browser.fill", map[string]any{
			"uid":   resolvedUID,
			"value": args.Value,
		})
		if errors.Is(err, errUnhandledTool) {
			return nil, fmt.Errorf("active browser profile %q does not expose browser.fill", strings.TrimSpace(profile.Name))
		}
		if err != nil {
			return nil, err
		}
		return map[string]any{
			"ok":             true,
			"active_profile": strings.TrimSpace(profile.Name),
			"server":         browserStatusPayload(profile.Name, server),
			"result":         normalizeBrowserToolResult(result),
			"source_tool":    "browser.fill",
		}, nil
	case "browser.hover":
		var args struct {
			PageID int    `json:"page_id"`
			UID    string `json:"uid"`
			Ref    string `json:"ref"`
		}
		if err := json.Unmarshal(call.Arguments, &args); err != nil {
			return nil, fmt.Errorf("invalid arguments: %w", err)
		}
		sessionKey := h.browserSessionKey(ctx, peerID)
		profile, server, err := h.ensureBrowserServer(ctx, sessionKey)
		if err != nil {
			return nil, err
		}
		resolvedUID, err := h.resolveBrowserActionUID(sessionKey, strings.TrimSpace(profile.Name), args.PageID, args.UID, args.Ref)
		if err != nil {
			return nil, err
		}
		result, err := h.invokeBrowserPageTool(ctx, peerID, sessionKey, args.PageID, "browser.hover", map[string]any{
			"uid": resolvedUID,
		})
		if errors.Is(err, errUnhandledTool) {
			return nil, fmt.Errorf("active browser profile %q does not expose browser.hover", strings.TrimSpace(profile.Name))
		}
		if err != nil {
			return nil, err
		}
		return map[string]any{
			"ok":             true,
			"active_profile": strings.TrimSpace(profile.Name),
			"server":         browserStatusPayload(profile.Name, server),
			"result":         normalizeBrowserToolResult(result),
			"source_tool":    "browser.hover",
		}, nil
	case "browser.scroll":
		var args struct {
			PageID   int    `json:"page_id"`
			DX       int    `json:"dx"`
			DY       int    `json:"dy"`
			Behavior string `json:"behavior"`
		}
		if err := json.Unmarshal(call.Arguments, &args); err != nil {
			return nil, fmt.Errorf("invalid arguments: %w", err)
		}
		sessionKey := h.browserSessionKey(ctx, peerID)
		profile, server, err := h.ensureBrowserServer(ctx, sessionKey)
		if err != nil {
			return nil, err
		}
		fn, err := buildBrowserScrollFunction(args.DX, args.DY, args.Behavior)
		if err != nil {
			return nil, err
		}
		result, err := h.invokeBrowserPageTool(ctx, peerID, sessionKey, args.PageID, "browser.evaluate_script", map[string]any{
			"function": fn,
		})
		if errors.Is(err, errUnhandledTool) {
			return nil, fmt.Errorf("active browser profile %q does not expose browser.evaluate_script", strings.TrimSpace(profile.Name))
		}
		if err != nil {
			return nil, err
		}
		return map[string]any{
			"ok":             true,
			"active_profile": strings.TrimSpace(profile.Name),
			"server":         browserStatusPayload(profile.Name, server),
			"result":         normalizeBrowserToolResult(result),
			"source_tool":    "browser.evaluate_script",
		}, nil
	case "browser.scroll_into_view":
		var args struct {
			PageID   int    `json:"page_id"`
			Selector string `json:"selector"`
			Behavior string `json:"behavior"`
			Block    string `json:"block"`
			Inline   string `json:"inline"`
		}
		if err := json.Unmarshal(call.Arguments, &args); err != nil {
			return nil, fmt.Errorf("invalid arguments: %w", err)
		}
		sessionKey := h.browserSessionKey(ctx, peerID)
		profile, server, err := h.ensureBrowserServer(ctx, sessionKey)
		if err != nil {
			return nil, err
		}
		fn, err := buildBrowserScrollIntoViewFunction(args.Behavior, args.Block, args.Inline)
		if err != nil {
			return nil, err
		}
		result, err := h.invokeBrowserPageTool(ctx, peerID, sessionKey, args.PageID, "browser.evaluate_script", map[string]any{
			"function": fn,
			"args":     []string{args.Selector},
		})
		if errors.Is(err, errUnhandledTool) {
			return nil, fmt.Errorf("active browser profile %q does not expose browser.evaluate_script", strings.TrimSpace(profile.Name))
		}
		if err != nil {
			return nil, err
		}
		return map[string]any{
			"ok":             true,
			"active_profile": strings.TrimSpace(profile.Name),
			"server":         browserStatusPayload(profile.Name, server),
			"result":         normalizeBrowserToolResult(result),
			"source_tool":    "browser.evaluate_script",
		}, nil
	case "browser.fill":
		var args struct {
			PageID int    `json:"page_id"`
			UID    string `json:"uid"`
			Ref    string `json:"ref"`
			Value  string `json:"value"`
		}
		if err := json.Unmarshal(call.Arguments, &args); err != nil {
			return nil, fmt.Errorf("invalid arguments: %w", err)
		}
		sessionKey := h.browserSessionKey(ctx, peerID)
		profile, server, err := h.ensureBrowserServer(ctx, sessionKey)
		if err != nil {
			return nil, err
		}
		resolvedUID, err := h.resolveBrowserActionUID(sessionKey, strings.TrimSpace(profile.Name), args.PageID, args.UID, args.Ref)
		if err != nil {
			return nil, err
		}
		result, err := h.invokeBrowserPageTool(ctx, peerID, sessionKey, args.PageID, "browser.fill", map[string]any{
			"uid":   resolvedUID,
			"value": args.Value,
		})
		if errors.Is(err, errUnhandledTool) {
			return nil, fmt.Errorf("active browser profile %q does not expose browser.fill", strings.TrimSpace(profile.Name))
		}
		if err != nil {
			return nil, err
		}
		return map[string]any{
			"ok":             true,
			"active_profile": strings.TrimSpace(profile.Name),
			"server":         browserStatusPayload(profile.Name, server),
			"result":         normalizeBrowserToolResult(result),
			"source_tool":    "browser.fill",
		}, nil
	case "browser.cookies.list":
		var args struct {
			PageID int `json:"page_id"`
		}
		if err := json.Unmarshal(call.Arguments, &args); err != nil {
			return nil, fmt.Errorf("invalid arguments: %w", err)
		}
		sessionKey := h.browserSessionKey(ctx, peerID)
		profile, server, err := h.ensureBrowserServer(ctx, sessionKey)
		if err != nil {
			return nil, err
		}
		result, err := h.invokeBrowserPageTool(ctx, peerID, sessionKey, args.PageID, "browser.evaluate_script", map[string]any{
			"function": buildBrowserCookieListFunction(),
		})
		if errors.Is(err, errUnhandledTool) {
			return nil, fmt.Errorf("active browser profile %q does not expose browser.evaluate_script", strings.TrimSpace(profile.Name))
		}
		if err != nil {
			return nil, err
		}
		normalized := normalizeBrowserToolResult(result)
		cookies := []any{}
		if payload, ok := normalized.(map[string]any); ok {
			if rawCookies, ok := payload["cookies"].([]any); ok {
				cookies = rawCookies
			}
		}
		return map[string]any{
			"ok":             true,
			"active_profile": strings.TrimSpace(profile.Name),
			"page_id":        args.PageID,
			"server":         browserStatusPayload(profile.Name, server),
			"count":          len(cookies),
			"cookies":        cookies,
			"source_tool":    "browser.evaluate_script",
		}, nil
	case "browser.cookies.set":
		var args struct {
			PageID   int    `json:"page_id"`
			Name     string `json:"name"`
			Value    string `json:"value"`
			Path     string `json:"path"`
			Domain   string `json:"domain"`
			Expires  string `json:"expires"`
			MaxAge   *int   `json:"max_age"`
			SameSite string `json:"same_site"`
			Secure   bool   `json:"secure"`
		}
		if err := json.Unmarshal(call.Arguments, &args); err != nil {
			return nil, fmt.Errorf("invalid arguments: %w", err)
		}
		sessionKey := h.browserSessionKey(ctx, peerID)
		profile, server, err := h.ensureBrowserServer(ctx, sessionKey)
		if err != nil {
			return nil, err
		}
		evalArgs := map[string]any{
			"name":     args.Name,
			"value":    args.Value,
			"path":     args.Path,
			"domain":   args.Domain,
			"expires":  args.Expires,
			"sameSite": args.SameSite,
			"secure":   args.Secure,
		}
		if args.MaxAge != nil {
			evalArgs["maxAge"] = *args.MaxAge
		}
		result, err := h.invokeBrowserPageTool(ctx, peerID, sessionKey, args.PageID, "browser.evaluate_script", map[string]any{
			"function": buildBrowserCookieSetFunction(),
			"args":     []any{evalArgs},
		})
		if errors.Is(err, errUnhandledTool) {
			return nil, fmt.Errorf("active browser profile %q does not expose browser.evaluate_script", strings.TrimSpace(profile.Name))
		}
		if err != nil {
			return nil, err
		}
		return map[string]any{
			"ok":             true,
			"active_profile": strings.TrimSpace(profile.Name),
			"page_id":        args.PageID,
			"server":         browserStatusPayload(profile.Name, server),
			"result":         normalizeBrowserToolResult(result),
			"source_tool":    "browser.evaluate_script",
		}, nil
	case "browser.cookies.delete":
		var args struct {
			PageID int    `json:"page_id"`
			Name   string `json:"name"`
			Path   string `json:"path"`
			Domain string `json:"domain"`
		}
		if err := json.Unmarshal(call.Arguments, &args); err != nil {
			return nil, fmt.Errorf("invalid arguments: %w", err)
		}
		sessionKey := h.browserSessionKey(ctx, peerID)
		profile, server, err := h.ensureBrowserServer(ctx, sessionKey)
		if err != nil {
			return nil, err
		}
		result, err := h.invokeBrowserPageTool(ctx, peerID, sessionKey, args.PageID, "browser.evaluate_script", map[string]any{
			"function": buildBrowserCookieDeleteFunction(),
			"args": []any{map[string]any{
				"name":   args.Name,
				"path":   args.Path,
				"domain": args.Domain,
			}},
		})
		if errors.Is(err, errUnhandledTool) {
			return nil, fmt.Errorf("active browser profile %q does not expose browser.evaluate_script", strings.TrimSpace(profile.Name))
		}
		if err != nil {
			return nil, err
		}
		return map[string]any{
			"ok":             true,
			"active_profile": strings.TrimSpace(profile.Name),
			"page_id":        args.PageID,
			"server":         browserStatusPayload(profile.Name, server),
			"result":         normalizeBrowserToolResult(result),
			"source_tool":    "browser.evaluate_script",
		}, nil
	case "browser.storage.inspect":
		var args struct {
			PageID int      `json:"page_id"`
			Scopes []string `json:"scopes"`
			Keys   []string `json:"keys"`
		}
		if err := json.Unmarshal(call.Arguments, &args); err != nil {
			return nil, fmt.Errorf("invalid arguments: %w", err)
		}
		sessionKey := h.browserSessionKey(ctx, peerID)
		profile, server, err := h.ensureBrowserServer(ctx, sessionKey)
		if err != nil {
			return nil, err
		}
		result, err := h.invokeBrowserPageTool(ctx, peerID, sessionKey, args.PageID, "browser.evaluate_script", map[string]any{
			"function": buildBrowserStorageInspectFunction(),
			"args": []any{map[string]any{
				"scopes": args.Scopes,
				"keys":   args.Keys,
			}},
		})
		if errors.Is(err, errUnhandledTool) {
			return nil, fmt.Errorf("active browser profile %q does not expose browser.evaluate_script", strings.TrimSpace(profile.Name))
		}
		if err != nil {
			return nil, err
		}
		normalized := normalizeBrowserToolResult(result)
		return map[string]any{
			"ok":             true,
			"active_profile": strings.TrimSpace(profile.Name),
			"page_id":        args.PageID,
			"server":         browserStatusPayload(profile.Name, server),
			"storage":        normalized,
			"source_tool":    "browser.evaluate_script",
		}, nil
	case "browser.storage.set":
		var args struct {
			PageID int    `json:"page_id"`
			Scope  string `json:"scope"`
			Key    string `json:"key"`
			Value  string `json:"value"`
		}
		if err := json.Unmarshal(call.Arguments, &args); err != nil {
			return nil, fmt.Errorf("invalid arguments: %w", err)
		}
		sessionKey := h.browserSessionKey(ctx, peerID)
		profile, server, err := h.ensureBrowserServer(ctx, sessionKey)
		if err != nil {
			return nil, err
		}
		result, err := h.invokeBrowserPageTool(ctx, peerID, sessionKey, args.PageID, "browser.evaluate_script", map[string]any{
			"function": buildBrowserStorageSetFunction(),
			"args": []any{map[string]any{
				"scope": args.Scope,
				"key":   args.Key,
				"value": args.Value,
			}},
		})
		if errors.Is(err, errUnhandledTool) {
			return nil, fmt.Errorf("active browser profile %q does not expose browser.evaluate_script", strings.TrimSpace(profile.Name))
		}
		if err != nil {
			return nil, err
		}
		return map[string]any{
			"ok":             true,
			"active_profile": strings.TrimSpace(profile.Name),
			"page_id":        args.PageID,
			"server":         browserStatusPayload(profile.Name, server),
			"result":         normalizeBrowserToolResult(result),
			"source_tool":    "browser.evaluate_script",
		}, nil
	case "browser.storage.delete":
		var args struct {
			PageID int    `json:"page_id"`
			Scope  string `json:"scope"`
			Key    string `json:"key"`
		}
		if err := json.Unmarshal(call.Arguments, &args); err != nil {
			return nil, fmt.Errorf("invalid arguments: %w", err)
		}
		sessionKey := h.browserSessionKey(ctx, peerID)
		profile, server, err := h.ensureBrowserServer(ctx, sessionKey)
		if err != nil {
			return nil, err
		}
		result, err := h.invokeBrowserPageTool(ctx, peerID, sessionKey, args.PageID, "browser.evaluate_script", map[string]any{
			"function": buildBrowserStorageDeleteFunction(),
			"args": []any{map[string]any{
				"scope": args.Scope,
				"key":   args.Key,
			}},
		})
		if errors.Is(err, errUnhandledTool) {
			return nil, fmt.Errorf("active browser profile %q does not expose browser.evaluate_script", strings.TrimSpace(profile.Name))
		}
		if err != nil {
			return nil, err
		}
		return map[string]any{
			"ok":             true,
			"active_profile": strings.TrimSpace(profile.Name),
			"page_id":        args.PageID,
			"server":         browserStatusPayload(profile.Name, server),
			"result":         normalizeBrowserToolResult(result),
			"source_tool":    "browser.evaluate_script",
		}, nil
	case "browser.storage.clear":
		var args struct {
			PageID int      `json:"page_id"`
			Scopes []string `json:"scopes"`
		}
		if err := json.Unmarshal(call.Arguments, &args); err != nil {
			return nil, fmt.Errorf("invalid arguments: %w", err)
		}
		sessionKey := h.browserSessionKey(ctx, peerID)
		profile, server, err := h.ensureBrowserServer(ctx, sessionKey)
		if err != nil {
			return nil, err
		}
		result, err := h.invokeBrowserPageTool(ctx, peerID, sessionKey, args.PageID, "browser.evaluate_script", map[string]any{
			"function": buildBrowserStorageClearFunction(),
			"args": []any{map[string]any{
				"scopes": args.Scopes,
			}},
		})
		if errors.Is(err, errUnhandledTool) {
			return nil, fmt.Errorf("active browser profile %q does not expose browser.evaluate_script", strings.TrimSpace(profile.Name))
		}
		if err != nil {
			return nil, err
		}
		return map[string]any{
			"ok":             true,
			"active_profile": strings.TrimSpace(profile.Name),
			"page_id":        args.PageID,
			"server":         browserStatusPayload(profile.Name, server),
			"result":         normalizeBrowserToolResult(result),
			"source_tool":    "browser.evaluate_script",
		}, nil
	case "browser.emulate":
		var args struct {
			PageID            int               `json:"page_id"`
			Reset             bool              `json:"reset"`
			Offline           *bool             `json:"offline"`
			NetworkProfile    string            `json:"network_profile"`
			ColorScheme       string            `json:"color_scheme"`
			CPUThrottlingRate float64           `json:"cpu_throttling_rate"`
			Timezone          *string           `json:"timezone"`
			UserAgent         *string           `json:"user_agent"`
			ExtraHeaders      map[string]string `json:"extra_headers"`
			Geolocation       *struct {
				Latitude  float64 `json:"latitude"`
				Longitude float64 `json:"longitude"`
			} `json:"geolocation"`
			Viewport *browserViewportSpec `json:"viewport"`
		}
		if err := json.Unmarshal(call.Arguments, &args); err != nil {
			return nil, fmt.Errorf("invalid arguments: %w", err)
		}
		sessionKey := h.browserSessionKey(ctx, peerID)
		profile, server, err := h.ensureBrowserServer(ctx, sessionKey)
		if err != nil {
			return nil, err
		}
		nativeArgs := map[string]any{}
		if colorScheme := strings.TrimSpace(args.ColorScheme); colorScheme != "" {
			nativeArgs["colorScheme"] = colorScheme
		}
		if args.CPUThrottlingRate > 0 {
			nativeArgs["cpuThrottlingRate"] = args.CPUThrottlingRate
		}
		if network := normalizeBrowserNetworkCondition(args.Offline, args.NetworkProfile); network != "" {
			nativeArgs["networkConditions"] = network
		}
		if args.UserAgent != nil {
			nativeArgs["userAgent"] = *args.UserAgent
		}
		if args.Geolocation != nil {
			nativeArgs["geolocation"] = fmt.Sprintf("%gx%g", args.Geolocation.Latitude, args.Geolocation.Longitude)
		}
		if viewport, err := formatBrowserViewport(args.Viewport); err != nil {
			return nil, err
		} else if viewport != "" {
			nativeArgs["viewport"] = viewport
		}
		jsArgs := map[string]any{"reset": args.Reset}
		if args.Offline != nil {
			jsArgs["offline"] = *args.Offline
		}
		if args.Timezone != nil {
			jsArgs["timezone"] = *args.Timezone
		}
		if args.UserAgent != nil {
			jsArgs["userAgent"] = *args.UserAgent
		}
		if len(args.ExtraHeaders) > 0 {
			jsArgs["extraHeaders"] = args.ExtraHeaders
		}
		if args.Geolocation != nil {
			jsArgs["geolocation"] = map[string]any{"latitude": args.Geolocation.Latitude, "longitude": args.Geolocation.Longitude}
		}
		response := map[string]any{
			"ok":             true,
			"active_profile": strings.TrimSpace(profile.Name),
			"page_id":        args.PageID,
			"server":         browserStatusPayload(profile.Name, server),
			"limitations": []string{
				"timezone and extra_headers are applied through a JavaScript override on the current page context",
				"cookie editing uses document.cookie and cannot modify HttpOnly cookies",
			},
		}
		if len(nativeArgs) > 0 {
			result, err := h.invokeBrowserPageTool(ctx, peerID, sessionKey, args.PageID, "browser.emulate", nativeArgs)
			if err != nil && !errors.Is(err, errUnhandledTool) {
				return nil, err
			}
			if errors.Is(err, errUnhandledTool) {
				response["native_supported"] = false
			} else {
				response["native_supported"] = true
				response["native_result"] = normalizeBrowserToolResult(result)
				response["native_source_tool"] = "browser.emulate"
			}
		}
		if len(jsArgs) > 1 || args.Reset {
			result, err := h.invokeBrowserPageTool(ctx, peerID, sessionKey, args.PageID, "browser.evaluate_script", map[string]any{
				"function": buildBrowserApplyEmulationFunction(),
				"args":     []any{jsArgs},
			})
			if errors.Is(err, errUnhandledTool) {
				return nil, fmt.Errorf("active browser profile %q does not expose browser.evaluate_script", strings.TrimSpace(profile.Name))
			}
			if err != nil {
				return nil, err
			}
			response["js_result"] = normalizeBrowserToolResult(result)
			response["js_source_tool"] = "browser.evaluate_script"
		}
		response["requested"] = map[string]any{
			"offline":             args.Offline,
			"network_profile":     args.NetworkProfile,
			"color_scheme":        args.ColorScheme,
			"cpu_throttling_rate": args.CPUThrottlingRate,
			"timezone":            args.Timezone,
			"user_agent":          args.UserAgent,
			"extra_headers":       args.ExtraHeaders,
			"geolocation":         args.Geolocation,
			"viewport":            args.Viewport,
			"reset":               args.Reset,
		}
		return response, nil
	default:
		return nil, errUnhandledTool
	}
}
