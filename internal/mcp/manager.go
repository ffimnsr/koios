package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/ffimnsr/koios/internal/config"
)

// serverEntry holds a connected MCP server client along with its discovered tools.
type serverEntry struct {
	name   string
	client Client
	tools  []Tool // cached after Initialize
}

// Manager manages connections to multiple MCP servers and exposes their tools
// to the handler layer.
type Manager struct {
	mu      sync.RWMutex
	servers []*serverEntry
}

// NewManager constructs a Manager from the given server configs. Servers with
// Enabled=false are skipped. Call Start to connect to the servers.
func NewManager(cfgs []config.MCPServerConfig) *Manager {
	m := &Manager{}
	for _, c := range cfgs {
		if !c.Enabled {
			continue
		}
		c := c // capture loop variable
		m.servers = append(m.servers, &serverEntry{name: c.Name, client: newClient(c)})
	}
	return m
}

// newClient builds the appropriate transport client from the server config.
func newClient(c config.MCPServerConfig) Client {
	timeout, _ := time.ParseDuration(c.Timeout)
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	switch strings.ToLower(strings.TrimSpace(c.Transport)) {
	case "stdio":
		return NewStdioClient(c.Name, c.Command, c.Args, c.Env)
	case "sse":
		return NewSSEClient(c.Name, c.URL, c.Headers, timeout)
	default: // "http" and anything else
		return NewHTTPClient(c.Name, c.URL, c.Headers, timeout)
	}
}

// Start connects to all configured servers, performs the MCP handshake, and
// discovers their tools. Errors for individual servers are logged but do not
// prevent other servers from starting.
func (m *Manager) Start(ctx context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	var wg sync.WaitGroup
	for _, s := range m.servers {
		s := s
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := s.client.Initialize(ctx); err != nil {
				slog.Warn("mcp: server init failed", "server", s.name, "err", err)
				return
			}
			tools, err := s.client.ListTools(ctx)
			if err != nil {
				slog.Warn("mcp: tools/list failed", "server", s.name, "err", err)
				return
			}
			s.tools = tools
			slog.Info("mcp: server connected", "server", s.name, "tools", len(tools))
		}()
	}
	wg.Wait()
	return nil
}

// ToolName returns the prefixed tool name used in the handler registry.
// Format: mcp__{serverName}__{toolName}
func ToolName(serverName, toolName string) string {
	return "mcp__" + serverName + "__" + toolName
}

// ParseToolName splits a prefixed tool name back into (serverName, toolName).
// Returns ok=false if the name is not in MCP format.
func ParseToolName(name string) (serverName, toolName string, ok bool) {
	const prefix = "mcp__"
	if !strings.HasPrefix(name, prefix) {
		return "", "", false
	}
	rest := name[len(prefix):]
	idx := strings.Index(rest, "__")
	if idx < 0 {
		return "", "", false
	}
	return rest[:idx], rest[idx+2:], true
}

// RegisteredTool is one tool offered by an MCP server, ready for integration
// into handler.toolDef.
type RegisteredTool struct {
	FullName    string // mcp__{server}__{tool}
	ServerName  string
	ToolName    string
	Description string
	InputSchema json.RawMessage
}

// ListTools returns all tools from all connected servers with their prefixed names.
func (m *Manager) ListTools() []RegisteredTool {
	m.mu.RLock()
	defer m.mu.RUnlock()

	var out []RegisteredTool
	for _, s := range m.servers {
		for _, t := range s.tools {
			out = append(out, RegisteredTool{
				FullName:    ToolName(s.name, t.Name),
				ServerName:  s.name,
				ToolName:    t.Name,
				Description: t.Description,
				InputSchema: t.InputSchema,
			})
		}
	}
	return out
}

// CallTool invokes a tool on the appropriate MCP server. fullName must be in
// the mcp__{server}__{tool} format returned by ToolName.
func (m *Manager) CallTool(ctx context.Context, fullName string, rawArgs json.RawMessage) (any, error) {
	serverName, toolName, ok := ParseToolName(fullName)
	if !ok {
		return nil, fmt.Errorf("mcp: invalid tool name %q", fullName)
	}

	var args map[string]any
	if len(rawArgs) > 0 {
		if err := json.Unmarshal(rawArgs, &args); err != nil {
			return nil, fmt.Errorf("mcp: unmarshal args for %s: %w", fullName, err)
		}
	}
	if args == nil {
		args = map[string]any{}
	}

	m.mu.RLock()
	var target *serverEntry
	for _, s := range m.servers {
		if s.name == serverName {
			target = s
			break
		}
	}
	m.mu.RUnlock()

	if target == nil {
		return nil, fmt.Errorf("mcp: server %q not found", serverName)
	}

	result, err := target.client.CallTool(ctx, toolName, args)
	if err != nil {
		return nil, err
	}

	if result.IsError {
		text := extractText(result)
		return nil, fmt.Errorf("mcp tool error: %s", text)
	}
	return extractText(result), nil
}

// Close shuts down all MCP server connections.
func (m *Manager) Close() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, s := range m.servers {
		if err := s.client.Close(); err != nil {
			slog.Warn("mcp: close error", "server", s.name, "err", err)
		}
	}
}

// extractText concatenates all text content items from a tool result.
func extractText(r *ToolResult) string {
	var parts []string
	for _, c := range r.Content {
		if c.Type == "text" && c.Text != "" {
			parts = append(parts, c.Text)
		}
	}
	return strings.Join(parts, "\n")
}
