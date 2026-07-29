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

// executeMCPServerList returns operator-configured shared MCP servers plus user-managed MCP servers visible to peerID.
func (h *Handler) executeMCPServerList(ctx context.Context, peerID string) (any, error) {
	statusByName := h.mcpStatusByName()
	entries := make([]map[string]any, 0, len(h.configMCPServers))
	for _, server := range h.configMCPServers {
		name := strings.TrimSpace(server.Name)
		if name == "" {
			continue
		}
		entry := configMCPServerEntryPayload(server)
		if status, ok := statusByName[name]; ok {
			applyMCPRuntimeStatus(entry, status)
		}
		entries = append(entries, entry)
	}

	if h.mcpRegistry != nil {
		records, err := h.mcpRegistry.ListByOwner(ctx, peerID)
		if err != nil {
			return nil, fmt.Errorf("list: %w", err)
		}
		for _, rec := range records {
			entry := mcpServerEntryPayload(&rec)
			if status, ok := statusByName[mcpregistry.RuntimeName(rec.OwnerPeerID, rec.ID)]; ok {
				applyMCPRuntimeStatus(entry, status)
			}
			entries = append(entries, entry)
		}
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

func (h *Handler) executeMCPSearch(_ context.Context, _ string, call agent.ToolCall) (any, error) {
	if h.mcpManager == nil {
		return nil, fmt.Errorf("MCP manager is not configured")
	}
	var args struct {
		Query string `json:"query"`
		Limit int    `json:"limit"`
	}
	if err := json.Unmarshal(call.Arguments, &args); err != nil {
		return nil, fmt.Errorf("invalid arguments: %w", err)
	}
	matches := h.mcpManager.Search(args.Query, args.Limit)
	return map[string]any{"ok": true, "query": args.Query, "count": len(matches), "matches": matches}, nil
}

func (h *Handler) executeMCPToolDetails(_ context.Context, peerID string, call agent.ToolCall) (any, error) {
	if h.mcpManager == nil {
		return nil, fmt.Errorf("MCP manager is not configured")
	}
	var args struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(call.Arguments, &args); err != nil {
		return nil, fmt.Errorf("invalid arguments: %w", err)
	}
	if strings.TrimSpace(args.Name) == "" {
		return nil, fmt.Errorf("name is required")
	}
	detail, ok := h.mcpManager.ToolDetails(args.Name)
	if !ok {
		return nil, fmt.Errorf("mcp: tool %q not found", args.Name)
	}
	if _, err := h.authorizedMCPServerName(peerID, detail.ServerName); err != nil {
		return nil, err
	}
	return map[string]any{"ok": true, "tool": detail}, nil
}

func (h *Handler) executeMCPToolCall(ctx context.Context, peerID string, call agent.ToolCall) (any, error) {
	if h.mcpManager == nil {
		return nil, fmt.Errorf("MCP manager is not configured")
	}
	var args struct {
		Name           string          `json:"name"`
		Arguments      json.RawMessage `json:"arguments"`
		InputResponses json.RawMessage `json:"input_responses"`
		RequestState   json.RawMessage `json:"request_state"`
	}
	if err := json.Unmarshal(call.Arguments, &args); err != nil {
		return nil, fmt.Errorf("invalid arguments: %w", err)
	}
	if strings.TrimSpace(args.Name) == "" {
		return nil, fmt.Errorf("name is required")
	}
	detail, ok := h.mcpManager.ToolDetails(args.Name)
	if !ok {
		return nil, fmt.Errorf("mcp: tool %q not found", args.Name)
	}
	if _, err := h.authorizedMCPServerName(peerID, detail.ServerName); err != nil {
		return nil, err
	}
	result, err := h.mcpManager.CallToolResultWithInput(ctx, args.Name, args.Arguments, args.InputResponses, args.RequestState)
	if err != nil {
		return nil, err
	}
	return map[string]any{"ok": true, "tool": args.Name, "result": result}, nil
}

func (h *Handler) executeMCPResourceList(ctx context.Context, peerID string, call agent.ToolCall) (any, error) {
	if h.mcpManager == nil {
		return nil, fmt.Errorf("MCP manager is not configured")
	}
	var args struct {
		Server string `json:"server"`
	}
	if err := json.Unmarshal(call.Arguments, &args); err != nil {
		return nil, fmt.Errorf("invalid arguments: %w", err)
	}
	serverName, err := h.authorizedMCPServerName(peerID, args.Server)
	if err != nil {
		return nil, err
	}
	resources, err := h.mcpManager.ListResources(ctx, serverName)
	if err != nil {
		return nil, err
	}
	templates, err := h.mcpManager.ListResourceTemplates(ctx, serverName)
	if err != nil {
		return nil, err
	}
	return map[string]any{"ok": true, "server": serverName, "resources": resources, "resource_templates": templates}, nil
}

func (h *Handler) executeMCPResourceRead(ctx context.Context, peerID string, call agent.ToolCall) (any, error) {
	if h.mcpManager == nil {
		return nil, fmt.Errorf("MCP manager is not configured")
	}
	var args struct {
		Server string `json:"server"`
		URI    string `json:"uri"`
	}
	if err := json.Unmarshal(call.Arguments, &args); err != nil {
		return nil, fmt.Errorf("invalid arguments: %w", err)
	}
	if strings.TrimSpace(args.URI) == "" {
		return nil, fmt.Errorf("uri is required")
	}
	serverName, err := h.authorizedMCPServerName(peerID, args.Server)
	if err != nil {
		return nil, err
	}
	result, err := h.mcpManager.ReadResource(ctx, serverName, args.URI)
	if err != nil {
		return nil, err
	}
	return map[string]any{"ok": true, "server": serverName, "uri": args.URI, "result": result}, nil
}

func (h *Handler) executeMCPPromptList(ctx context.Context, peerID string, call agent.ToolCall) (any, error) {
	if h.mcpManager == nil {
		return nil, fmt.Errorf("MCP manager is not configured")
	}
	var args struct {
		Server string `json:"server"`
	}
	if err := json.Unmarshal(call.Arguments, &args); err != nil {
		return nil, fmt.Errorf("invalid arguments: %w", err)
	}
	serverName, err := h.authorizedMCPServerName(peerID, args.Server)
	if err != nil {
		return nil, err
	}
	prompts, err := h.mcpManager.ListPrompts(ctx, serverName)
	if err != nil {
		return nil, err
	}
	return map[string]any{"ok": true, "server": serverName, "prompts": prompts}, nil
}

func (h *Handler) executeMCPPromptGet(ctx context.Context, peerID string, call agent.ToolCall) (any, error) {
	if h.mcpManager == nil {
		return nil, fmt.Errorf("MCP manager is not configured")
	}
	var args struct {
		Server    string         `json:"server"`
		Name      string         `json:"name"`
		Arguments map[string]any `json:"arguments"`
	}
	if err := json.Unmarshal(call.Arguments, &args); err != nil {
		return nil, fmt.Errorf("invalid arguments: %w", err)
	}
	if strings.TrimSpace(args.Name) == "" {
		return nil, fmt.Errorf("name is required")
	}
	serverName, err := h.authorizedMCPServerName(peerID, args.Server)
	if err != nil {
		return nil, err
	}
	result, err := h.mcpManager.GetPrompt(ctx, serverName, args.Name, args.Arguments)
	if err != nil {
		return nil, err
	}
	return map[string]any{"ok": true, "server": serverName, "name": args.Name, "result": result}, nil
}

func (h *Handler) authorizedMCPServerName(peerID, requested string) (string, error) {
	serverName := strings.TrimSpace(requested)
	if serverName == "" {
		return "", fmt.Errorf("server is required")
	}
	if h.mcpManager == nil {
		return "", fmt.Errorf("MCP manager is not configured")
	}
	status, ok := h.mcpManager.ServerStatusByName(serverName)
	if !ok {
		return "", fmt.Errorf("mcp: server %q not found", serverName)
	}
	if status.Kind == "user" && status.ProfileName != "" && status.ProfileName != peerID {
		return "", fmt.Errorf("mcp: server %q is owned by %q, not %q", serverName, status.ProfileName, peerID)
	}
	return serverName, nil
}

// ── helpers ─────────────────────────────────────────────────────────────────────

func mcpServerEntryPayload(rec *mcpregistry.ServerRecord) map[string]any {
	m := map[string]any{
		"id":                rec.ID,
		"name":              rec.Name,
		"runtime_name":      mcpregistry.RuntimeName(rec.OwnerPeerID, rec.ID),
		"source":            "user",
		"user_managed":      true,
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

func configMCPServerEntryPayload(server config.MCPServerConfig) map[string]any {
	m := map[string]any{
		"name":         strings.TrimSpace(server.Name),
		"runtime_name": strings.TrimSpace(server.Name),
		"source":       "config",
		"user_managed": false,
		"transport":    server.Transport,
		"enabled":      server.Enabled,
		"visibility":   "shared",
	}
	if server.Transport == "stdio" {
		m["command"] = server.Command
		m["args"] = append([]string(nil), server.Args...)
	} else {
		m["url"] = server.URL
	}
	if len(server.Env) > 0 {
		m["env"] = mcpregistry.RedactedEnv(server.Env)
	}
	if len(server.Headers) > 0 {
		m["headers"] = mcpregistry.RedactedHeaders(server.Headers)
	}
	if server.Timeout != "" {
		m["timeout"] = server.Timeout
	}
	return m
}

func (h *Handler) mcpStatusByName() map[string]mcp.ServerStatus {
	if h.mcpManager == nil {
		return nil
	}
	statuses := h.mcpManager.ServerStatuses()
	out := make(map[string]mcp.ServerStatus, len(statuses))
	for _, status := range statuses {
		out[status.Name] = status
	}
	return out
}

func applyMCPRuntimeStatus(entry map[string]any, status mcp.ServerStatus) {
	entry["connected"] = status.Connected
	entry["tool_count"] = status.ToolCount
	entry["resource_count"] = status.ResourceCount
	entry["prompt_count"] = status.PromptCount
	if strings.TrimSpace(status.ProtocolVersion) != "" {
		entry["protocol_version"] = status.ProtocolVersion
	}
	if len(status.Capabilities) > 0 {
		entry["capabilities"] = json.RawMessage(status.Capabilities)
	}
	entry["cache_fresh"] = status.CacheFresh
	entry["subscription_state"] = map[string]any{"listening": status.SubscriptionOn}
	if status.LastError != "" {
		entry["last_error"] = status.LastError
	}
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
		client = mcp.NewStdioClientWithContext(ctx, cfg.Name, cfg.Command, cfg.Args, cfg.Env)
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
