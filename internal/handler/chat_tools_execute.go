package handler

import (
	"context"
	"errors"
	"fmt"

	"github.com/ffimnsr/koios/internal/agent"
	"github.com/ffimnsr/koios/internal/mcp"
)

var errUnhandledTool = errors.New("unhandled tool")

func (h *Handler) ExecuteTool(ctx context.Context, peerID string, call agent.ToolCall) (any, error) {
	toolCtx, _ := agent.ToolRunContextFromContext(ctx)
	call.Name = h.NormalizeToolName(peerID, call.Name)
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
