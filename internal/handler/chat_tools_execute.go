package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/ffimnsr/koios/internal/agent"
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
	for _, dispatch := range []func(context.Context, string, agent.ToolCall) (any, error){
		h.executeDataTool,
		h.executeSessionWorkspaceTool,
		h.executeRuntimeTool,
	} {
		result, err := dispatch(ctx, peerID, call)
		if err == nil {
			return result, nil
		}
		if !errors.Is(err, errUnhandledTool) {
			return nil, err
		}
	}
	if h.mcpManager != nil {
		if _, _, ok := mcp.ParseToolName(call.Name); ok {
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
func (h *Handler) suggestTools(peerID, name string) []string {
	if name == "" {
		return nil
	}
	suffix := "." + name
	var matches []string
	for _, tool := range h.ToolDefinitions(peerID) {
		if tool.Type == "function" && strings.HasSuffix(tool.Function.Name, suffix) {
			matches = append(matches, tool.Function.Name)
		}
	}
	return matches
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
	if _, _, ok := mcp.ParseToolName(call.Name); ok {
		executorKind = "mcp"
	}

	var resultJSON string
	if result != nil {
		if b, err := json.Marshal(result); err == nil {
			resultJSON = string(b)
			if len(resultJSON) > 8192 {
				resultJSON = resultJSON[:8192]
			}
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

	input := toolresults.Input{
		SessionKey: toolCtx.SessionKey,
		ToolCallID: call.ID,
		ToolName:   call.Name,
		ArgsJSON:   string(call.Arguments),
		ResultJSON: resultJSON,
		Summary:    summary,
		IsError:    execErr != nil,
		DurationMS: durationMS,
		Provenance: toolresults.Provenance{
			ExecutorKind:  executorKind,
			ModelProfile:  toolCtx.ActiveProfile,
			CaptureReason: "tool_execution",
		},
	}

	if _, err := h.toolResultStore.Create(ctx, peerID, input); err != nil {
		// Non-fatal: provenance capture should not break tool execution.
		_ = err
	}
}
