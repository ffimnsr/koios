package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/ffimnsr/koios/internal/agent"
	"github.com/ffimnsr/koios/internal/artifacts"
	"github.com/ffimnsr/koios/internal/mcp"
	"github.com/ffimnsr/koios/internal/toolresults"
)

var errUnhandledTool = errors.New("unhandled tool")

func (h *Handler) ExecuteTool(ctx context.Context, peerID string, call agent.ToolCall) (any, error) {
	toolCtx, _ := agent.ToolRunContextFromContext(ctx)
	call.Name = h.NormalizeToolName(peerID, call.Name)
	if !h.effectiveToolPolicy(peerID, toolCtx.SessionKey, toolCtx.ActiveProfile).Allows(call.Name) {
		return nil, fmt.Errorf("tool %q is not allowed", call.Name)
	}

	start := time.Now()
	result, execErr := h.dispatchTool(ctx, peerID, call)
	durationMS := time.Since(start).Milliseconds()

	h.recordToolResult(ctx, peerID, call, toolCtx, result, execErr, durationMS)

	return result, execErr
}

// dispatchTool routes the call to the appropriate executor without any
// provenance concerns.
func (h *Handler) dispatchTool(ctx context.Context, peerID string, call agent.ToolCall) (any, error) {
	if result, err := builtInTools.Dispatch(h, ctx, peerID, call); err == nil || !errors.Is(err, errUnhandledTool) {
		return result, err
	}
	if h.pluginRegistry != nil {
		toolCtx, _ := agent.ToolRunContextFromContext(ctx)
		if tool, ok := h.pluginRegistry.Tool(call.Name); ok {
			if tool.Available != nil && !tool.Available(h) {
				return nil, fmt.Errorf("tool %q is not available", call.Name)
			}
			return tool.Execute(ctx, pluginToolContext{
				Handler:     h,
				PeerID:      peerID,
				Call:        call,
				ToolContext: toolCtx,
			})
		}
	}
	if result, err := h.browserAliasTool(ctx, peerID, call); err == nil || !errors.Is(err, errUnhandledTool) {
		return result, err
	}
	if h.mcpManager != nil {
		if _, _, ok := mcp.ParseToolName(call.Name); ok {
			// Enforce ownership: user-managed tools can only be called by
			// their owning peer. The listing in activeDefs already filters
			// by visibility, but this is a defense-in-depth check.
			for _, t := range h.mcpManager.AllTools() {
				if t.FullName == call.Name && t.Kind == "user" {
					if t.ProfileName != "" && t.ProfileName != peerID {
						return nil, fmt.Errorf("tool %q is owned by %q, not %q", call.Name, t.ProfileName, peerID)
					}
					break
				}
			}
			call.Arguments = injectMCPPeerID(call.Name, peerID, call.Arguments)
			return h.mcpManager.CallTool(ctx, call.Name, call.Arguments)
		}
	}
	if suggestions := h.suggestTools(peerID, call.Name); len(suggestions) > 0 {
		return nil, fmt.Errorf("unknown tool %q; did you mean: %s", call.Name, strings.Join(suggestions, ", "))
	}
	return nil, fmt.Errorf("unknown tool %q", call.Name)
}

// suggestTools returns tool names that share the same suffix as name (e.g.
// "create" matches "task.create", "bookmark.create"). It is used to produce
// actionable error messages when an agent calls a bare name without its domain
// prefix.
func injectMCPPeerID(toolName, peerID string, rawArgs json.RawMessage) json.RawMessage {
	if peerID == "" {
		return rawArgs
	}
	namespace, _, ok := mcp.ParseToolName(toolName)
	if !ok || namespace != "monaco" {
		return rawArgs
	}

	args := map[string]any{}
	if len(rawArgs) > 0 {
		if err := json.Unmarshal(rawArgs, &args); err != nil {
			return rawArgs
		}
	}
	if args == nil {
		args = map[string]any{}
	}
	if _, exists := args["peer_id"]; exists {
		return rawArgs
	}
	if _, exists := args["privy_user_id"]; exists {
		return rawArgs
	}
	args["peer_id"] = peerID
	encoded, err := json.Marshal(args)
	if err != nil {
		return rawArgs
	}
	return encoded
}

func (h *Handler) suggestTools(peerID, name string) []string {
	if name == "" {
		return nil
	}
	toolNames := make([]string, 0)
	for _, tool := range h.ToolDefinitions(peerID) {
		if tool.Type == "function" {
			toolNames = append(toolNames, tool.Function.Name)
		}
	}
	return builtInTools.Suggest(toolNames, name)
}

// recordToolResult persists a provenance record for a completed tool execution.
// Errors during persistence are logged but do not affect the tool result.
func (h *Handler) recordToolResult(
	ctx context.Context,
	peerID string,
	call agent.ToolCall,
	toolCtx agent.ToolRunContext,
	result any,
	execErr error,
	durationMS int64,
) {
	if h.toolResultStore == nil {
		return
	}

	executorKind := "builtin"
	if h.pluginRegistry != nil {
		if _, ok := h.pluginRegistry.Tool(call.Name); ok {
			executorKind = "plugin"
		}
	}
	if executorKind == "builtin" {
		if _, _, ok := mcp.ParseToolName(call.Name); ok {
			executorKind = "mcp"
		}
	}

	var (
		resultJSON      string
		fullResultJSON  string
		fullResultBytes int
	)
	if result != nil {
		if b, err := json.Marshal(result); err == nil {
			fullResultJSON = string(b)
			fullResultBytes = len(fullResultJSON)
			resultJSON = fullResultJSON
		}
	}

	var errMsg string
	if execErr != nil {
		errMsg = execErr.Error()
	}
	summary := call.Name
	if errMsg != "" {
		summary = call.Name + ": " + errMsg
	}
	prov := toolresults.Provenance{
		ExecutorKind:  executorKind,
		ModelProfile:  toolCtx.ActiveProfile,
		CaptureReason: "tool_execution",
		ResultBytes:   fullResultBytes,
	}
	if fullResultJSON != "" && len(fullResultJSON) > 8192 {
		if artifactID, err := h.persistFullToolResultArtifact(ctx, peerID, call, toolCtx, fullResultJSON, durationMS); err == nil && artifactID != "" {
			prov.ResultTruncated = true
			prov.FullResultArtifact = artifactID
			resultJSON = fullResultJSON[:8192]
			if errMsg != "" {
				summary = call.Name + ": " + errMsg + " (full result artifact " + artifactID + ")"
			} else {
				summary = call.Name + ": full result stored in artifact " + artifactID
			}
		}
	}

	input := toolresults.Input{
		SessionKey: toolCtx.SessionKey,
		ToolCallID: call.ID,
		ToolName:   call.Name,
		ArgsJSON:   string(call.Arguments),
		ResultJSON: resultJSON,
		Summary:    summary,
		IsError:    execErr != nil,
		DurationMS: durationMS,
		Provenance: prov,
	}

	if _, err := h.toolResultStore.Create(ctx, peerID, input); err != nil {
		// Non-fatal: provenance capture should not break tool execution.
		_ = err
	}
}

func (h *Handler) persistFullToolResultArtifact(ctx context.Context, peerID string, call agent.ToolCall, toolCtx agent.ToolRunContext, fullResultJSON string, durationMS int64) (string, error) {
	if h == nil || h.artifactStore == nil || strings.TrimSpace(fullResultJSON) == "" {
		return "", nil
	}
	title := fmt.Sprintf("Tool result %s", call.Name)
	if call.ID != "" {
		title = fmt.Sprintf("Tool result %s (%s)", call.Name, call.ID)
	}
	content, err := json.Marshal(map[string]any{
		"tool_name":    call.Name,
		"tool_call_id": call.ID,
		"session_key":  toolCtx.SessionKey,
		"args_json":    json.RawMessage(call.Arguments),
		"result_json":  json.RawMessage(fullResultJSON),
		"duration_ms":  durationMS,
		"captured_at":  time.Now().UTC().Format(time.RFC3339),
	})
	if err != nil {
		return "", err
	}
	artifact, err := h.artifactStore.Create(ctx, peerID, artifacts.Input{
		Kind:    "tool_result",
		Title:   title,
		Content: string(content),
		Labels:  []string{"tool_result", call.Name},
	})
	if err != nil {
		return "", err
	}
	return artifact.ID, nil
}
