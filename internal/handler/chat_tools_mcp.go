package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/ffimnsr/koios/internal/agent"
	"github.com/ffimnsr/koios/internal/config"
	"github.com/ffimnsr/koios/internal/mcp"
	"github.com/ffimnsr/koios/internal/mcpregistry"
)

// executeMCPServerList returns all user-managed MCP servers visible to peerID.
func (h *Handler) executeMCPServerList(ctx context.Context, peerID string) (any, error) {
	if h.mcpRegistry == nil {
		return nil, fmt.Errorf("MCP registry is not configured")
	}
	records, err := h.mcpRegistry.ListByOwner(ctx, peerID)
	if err != nil {
		return nil, fmt.Errorf("list: %w", err)
	}
	entries := make([]map[string]any, 0, len(records))
	for _, rec := range records {
		entry := mcpServerEntryPayload(&rec)
		entries = append(entries, entry)
	}
	return map[string]any{
		"ok":      true,
		"servers": entries,
	}, nil
}

// executeMCPServerAdd registers a new MCP server for peerID.
func (h *Handler) executeMCPServerAdd(ctx context.Context, peerID string, call agent.ToolCall) (any, error) {
	if h.mcpRegistry == nil {
		return nil, fmt.Errorf("MCP registry is not configured")
	}
	var args struct {
		Name             string            `json:"name"`
		Transport        string            `json:"transport"`
		Command          string            `json:"command,omitempty"`
		Args             []string          `json:"args,omitempty"`
		Env              map[string]string `json:"env,omitempty"`
		URL              string            `json:"url,omitempty"`
		Headers          map[string]string `json:"headers,omitempty"`
		Timeout          string            `json:"timeout,omitempty"`
		Visibility       string            `json:"visibility,omitempty"`
		ApprovalRequired *bool             `json:"approval_required,omitempty"`
	}
	if err := json.Unmarshal(call.Arguments, &args); err != nil {
		return nil, fmt.Errorf("invalid arguments: %w", err)
	}
	if args.Timeout == "" {
		args.Timeout = "30s"
	}
	approvalRequired := true
	if args.ApprovalRequired != nil {
		approvalRequired = *args.ApprovalRequired
	}

	// Validate.
	cfg := config.MCPServerConfig{
		Name:      args.Name,
		Transport: args.Transport,
		Command:   args.Command,
		Args:      args.Args,
		Env:       args.Env,
		URL:       args.URL,
		Headers:   args.Headers,
		Timeout:   args.Timeout,
	}
	if err := mcp.ValidateServerConfig(cfg); err != nil {
		return nil, fmt.Errorf("validation: %w", err)
	}
	if args.Visibility != "" {
		if err := mcp.ValidateUserVisibility(args.Visibility); err != nil {
			return nil, err
		}
	}

	input := mcpregistry.Input{
		OwnerPeerID:      peerID,
		Name:             args.Name,
		Transport:        args.Transport,
		Command:          args.Command,
		Args:             args.Args,
		Env:              args.Env,
		URL:              args.URL,
		Headers:          args.Headers,
		Timeout:          args.Timeout,
		Enabled:          false,
		Visibility:       args.Visibility,
		ApprovalRequired: approvalRequired,
	}
	rec, err := h.mcpRegistry.Create(ctx, input)
	if err != nil {
		return nil, fmt.Errorf("create: %w", err)
	}
	result := mcpServerEntryPayload(rec)
	result["ok"] = true
	return result, nil
}

// executeMCPServerRemove deletes a user-managed MCP server for peerID.
func (h *Handler) executeMCPServerRemove(ctx context.Context, peerID string, call agent.ToolCall) (any, error) {
	if h.mcpRegistry == nil {
		return nil, fmt.Errorf("MCP registry is not configured")
	}
	var args struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(call.Arguments, &args); err != nil {
		return nil, fmt.Errorf("invalid arguments: %w", err)
	}
	if args.ID == "" {
		return nil, fmt.Errorf("id is required")
	}

	// Remove from runtime if connected.
	if h.mcpManager != nil {
		rec, err := h.mcpRegistry.Get(ctx, peerID, args.ID)
		if err == nil {
			runtimeName := mcpregistry.RuntimeName(rec.OwnerPeerID, rec.ID)
			_ = h.mcpManager.RemoveServer(runtimeName)
		}
	}

	if err := h.mcpRegistry.Delete(ctx, peerID, args.ID); err != nil {
		return nil, fmt.Errorf("remove: %w", err)
	}
	return map[string]any{"ok": true, "id": args.ID, "deleted": true}, nil
}

// executeMCPServerEnable enables a user-managed MCP server for peerID.
func (h *Handler) executeMCPServerEnable(ctx context.Context, peerID string, call agent.ToolCall) (any, error) {
	if h.mcpRegistry == nil {
		return nil, fmt.Errorf("MCP registry is not configured")
	}
	var args struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(call.Arguments, &args); err != nil {
		return nil, fmt.Errorf("invalid arguments: %w", err)
	}
	if args.ID == "" {
		return nil, fmt.Errorf("id is required")
	}

	enabled := true
	rec, err := h.mcpRegistry.Update(ctx, args.ID, peerID, mcpregistry.UpdateInput{Enabled: &enabled})
	if err != nil {
		return nil, fmt.Errorf("enable: %w", err)
	}

	// Push to runtime manager if running.
	if h.mcpManager != nil {
		cfg := rec.ToMCPServerConfig()
		if h.mcpManager.HasServer(cfg.Name) {
			_, _ = h.mcpManager.UpdateServer(ctx, cfg)
		} else {
			_, _ = h.mcpManager.AddServer(ctx, cfg)
		}
	}

	result := mcpServerEntryPayload(rec)
	result["ok"] = true
	return result, nil
}

// executeMCPServerDisable disables a user-managed MCP server for peerID.
func (h *Handler) executeMCPServerDisable(ctx context.Context, peerID string, call agent.ToolCall) (any, error) {
	if h.mcpRegistry == nil {
		return nil, fmt.Errorf("MCP registry is not configured")
	}
	var args struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(call.Arguments, &args); err != nil {
		return nil, fmt.Errorf("invalid arguments: %w", err)
	}
	if args.ID == "" {
		return nil, fmt.Errorf("id is required")
	}

	enabled := false
	rec, err := h.mcpRegistry.Update(ctx, args.ID, peerID, mcpregistry.UpdateInput{Enabled: &enabled})
	if err != nil {
		return nil, fmt.Errorf("disable: %w", err)
	}

	// Remove from runtime manager if running.
	if h.mcpManager != nil {
		runtimeName := mcpregistry.RuntimeName(rec.OwnerPeerID, rec.ID)
		_ = h.mcpManager.RemoveServer(runtimeName)
	}

	result := mcpServerEntryPayload(rec)
	result["ok"] = true
	return result, nil
}

// executeMCPServerInspect returns full details of a user-managed MCP server.
func (h *Handler) executeMCPServerInspect(ctx context.Context, peerID string, call agent.ToolCall) (any, error) {
	if h.mcpRegistry == nil {
		return nil, fmt.Errorf("MCP registry is not configured")
	}
	var args struct {
		ID      string `json:"id"`
		Secrets bool   `json:"secrets"`
	}
	if err := json.Unmarshal(call.Arguments, &args); err != nil {
		return nil, fmt.Errorf("invalid arguments: %w", err)
	}
	if args.ID == "" {
		return nil, fmt.Errorf("id is required")
	}

	rec, err := h.mcpRegistry.Get(ctx, peerID, args.ID)
	if err != nil {
		return nil, fmt.Errorf("get: %w", err)
	}
	result := mcpServerDetailPayload(rec, args.Secrets)
	result["ok"] = true
	return result, nil
}

// executeMCPServerTest probes connectivity of a user-managed MCP server.
func (h *Handler) executeMCPServerTest(ctx context.Context, peerID string, call agent.ToolCall) (any, error) {
	if h.mcpRegistry == nil {
		return nil, fmt.Errorf("MCP registry is not configured")
	}
	var args struct {
		ID      string `json:"id"`
		Timeout int    `json:"timeout"`
	}
	if err := json.Unmarshal(call.Arguments, &args); err != nil {
		return nil, fmt.Errorf("invalid arguments: %w", err)
	}
	if args.ID == "" {
		return nil, fmt.Errorf("id is required")
	}

	rec, err := h.mcpRegistry.Get(ctx, peerID, args.ID)
	if err != nil {
		return nil, fmt.Errorf("get: %w", err)
	}

	timeout := 10 * time.Second
	if args.Timeout > 0 {
		timeout = time.Duration(args.Timeout) * time.Second
	}

	result := probeManagedMCPServer(ctx, rec, timeout)
	result["ok"] = true
	return result, nil
}

// ── helpers ─────────────────────────────────────────────────────────────────────

func mcpServerEntryPayload(rec *mcpregistry.ServerRecord) map[string]any {
	m := map[string]any{
		"id":                rec.ID,
		"name":              rec.Name,
		"transport":         rec.Transport,
		"enabled":           rec.Enabled,
		"visibility":        rec.Visibility,
		"approval_required": rec.ApprovalRequired,
		"created_at":        rec.CreatedAt,
	}
	if rec.Transport == "stdio" {
		m["command"] = rec.Command
		m["args"] = rec.Args
	} else {
		m["url"] = rec.URL
	}
	if len(rec.Env) > 0 {
		m["env"] = mcpregistry.RedactedEnv(rec.Env)
	}
	if len(rec.Headers) > 0 {
		m["headers"] = mcpregistry.RedactedHeaders(rec.Headers)
	}
	if rec.Timeout != "" {
		m["timeout"] = rec.Timeout
	}
	return m
}

func mcpServerDetailPayload(rec *mcpregistry.ServerRecord, showSecrets bool) map[string]any {
	m := mcpServerEntryPayload(rec)
	m["owner"] = rec.OwnerPeerID
	m["updated_at"] = rec.UpdatedAt
	if len(rec.AllowedPeerIDs) > 0 {
		m["allowed_peer_ids"] = rec.AllowedPeerIDs
	}
	if showSecrets && len(rec.Headers) > 0 {
		m["headers"] = rec.Headers
	}
	if showSecrets && len(rec.Env) > 0 {
		m["env"] = rec.Env
	}
	return m
}

func probeManagedMCPServer(ctx context.Context, rec *mcpregistry.ServerRecord, timeout time.Duration) map[string]any {
	transport := strings.ToLower(strings.TrimSpace(rec.Transport))
	cfg := config.MCPServerConfig{
		Name:    rec.Name,
		Command: rec.Command,
		Args:    rec.Args,
		Env:     rec.Env,
		URL:     rec.URL,
		Headers: rec.Headers,
		Timeout: rec.Timeout,
	}

	var client mcp.Client
	switch transport {
	case "stdio":
		client = mcp.NewStdioClient(cfg.Name, cfg.Command, cfg.Args, cfg.Env)
	case "sse":
		client = mcp.NewSSEClient(cfg.Name, cfg.URL, cfg.Headers, timeout)
	default:
		client = mcp.NewHTTPClient(cfg.Name, cfg.URL, cfg.Headers, timeout)
	}
	if client == nil {
		return map[string]any{"success": false, "error": "could not create client"}
	}
	defer client.Close()

	probeCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	if err := client.Initialize(probeCtx); err != nil {
		return map[string]any{
			"success": false,
			"phase":   "initialize",
			"error":   err.Error(),
		}
	}
	tools, err := client.ListTools(probeCtx)
	if err != nil {
		return map[string]any{
			"success": false,
			"phase":   "list_tools",
			"error":   err.Error(),
		}
	}
	toolNames := make([]string, 0, len(tools))
	for _, t := range tools {
		toolNames = append(toolNames, t.Name)
	}
	return map[string]any{
		"success":    true,
		"tools":      toolNames,
		"tool_count": len(tools),
	}
}
