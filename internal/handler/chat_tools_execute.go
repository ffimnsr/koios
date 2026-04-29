package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/ffimnsr/koios/internal/agent"
	"github.com/ffimnsr/koios/internal/mcp"
)

var errUnhandledTool = errors.New("unhandled tool")

func (h *Handler) ExecuteTool(ctx context.Context, peerID string, call agent.ToolCall) (any, error) {
	toolCtx, _ := agent.ToolRunContextFromContext(ctx)
	call.Name = h.NormalizeToolName(peerID, call.Name)
	call.Name = h.inferToolNameFromArguments(peerID, call.Name, call.Arguments)
	if !h.effectiveToolPolicy(peerID, toolCtx.SessionKey, toolCtx.ActiveProfile).Allows(call.Name) {
		return nil, fmt.Errorf("tool %q is not allowed", call.Name)
	}
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
	return nil, fmt.Errorf("unknown tool %q", call.Name)
}

func (h *Handler) inferToolNameFromArguments(peerID, name string, arguments json.RawMessage) string {
	name = strings.TrimSpace(name)
	if name == "" || strings.Contains(name, ".") {
		return name
	}
	var args map[string]json.RawMessage
	if err := json.Unmarshal(arguments, &args); err != nil {
		return name
	}
	has := func(key string) bool {
		_, ok := args[key]
		return ok
	}
	hasTool := func(toolName string) bool {
		for _, tool := range h.ToolDefinitions(peerID) {
			if tool.Type == "function" && tool.Function.Name == toolName {
				return true
			}
		}
		return false
	}
	switch name {
	case "create":
		if hasTool("task.create") && has("title") && (has("owner") || has("due_at")) && !has("waiting_for") && !has("content") && !has("name") {
			return "task.create"
		}
	case "insert":
		if hasTool("memory.insert") && has("content") && !has("id") && !has("title") && !has("name") {
			return "memory.insert"
		}
	}
	return name
}
