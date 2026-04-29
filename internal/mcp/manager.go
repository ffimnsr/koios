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
	name       string
	toolPrefix string
	hideTools  bool
	client     Client
	tools      []Tool // cached after Initialize
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
		prefix := strings.TrimSpace(c.ToolNamePrefix)
		if prefix == "" {
			prefix = ToolPrefix(c.Name)
		}
		m.servers = append(m.servers, &serverEntry{name: c.Name, toolPrefix: prefix, hideTools: c.HideTools, client: newClient(c)})
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

// ToolPrefix returns the default runtime tool prefix for a plain configured
// MCP server.
func ToolPrefix(serverName string) string {
	return "mcp__" + serverName + "__"
}

// ToolName returns the default prefixed tool name used in the handler registry.
// Format: mcp__{serverName}__{toolName}
func ToolName(serverName, toolName string) string {
	return ToolPrefix(serverName) + toolName
}

// PluginToolPrefix returns the runtime tool prefix for a manifest-backed
// extension server. The manifest id is normalized into a stable token.
func PluginToolPrefix(manifestID string) string {
	namespace := sanitizePrefixToken(manifestID)
	if namespace == "" {
		namespace = "plugin"
	}
	return "mcp_plug_" + namespace + "__"
}

// ParseToolName splits a prefixed MCP tool name into a namespace token and the
// tool name. The namespace token is the server name for default mcp__ names and
// the normalized manifest id for mcp_plug_ names.
func ParseToolName(name string) (namespace, toolName string, ok bool) {
	for _, prefix := range []string{"mcp__", "mcp_plug_"} {
		if !strings.HasPrefix(name, prefix) {
			continue
		}
		rest := name[len(prefix):]
		idx := strings.Index(rest, "__")
		if idx < 0 {
			return "", "", false
		}
		if idx == 0 || idx+2 >= len(rest) {
			return "", "", false
		}
		return rest[:idx], rest[idx+2:], true
	}
	return "", "", false
}

// RegisteredTool is one tool offered by an MCP server, ready for integration
// into handler.toolDef.
type RegisteredTool struct {
	FullName    string // mcp__{server}__{tool} or mcp_plug_{manifest}__{tool}
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
		if s.hideTools {
			continue
		}
		for _, t := range s.tools {
			out = append(out, RegisteredTool{
				FullName:    s.toolPrefix + t.Name,
				ServerName:  s.name,
				ToolName:    t.Name,
				Description: t.Description,
				InputSchema: t.InputSchema,
			})
		}
	}
	return out
}

// CallTool invokes a tool on the appropriate MCP server. fullName must match
// one of the prefixes returned by ToolName or PluginToolPrefix.
func (m *Manager) CallTool(ctx context.Context, fullName string, rawArgs json.RawMessage) (any, error) {
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
	var toolName string
	for _, s := range m.servers {
		if strings.HasPrefix(fullName, s.toolPrefix) {
			target = s
			toolName = strings.TrimPrefix(fullName, s.toolPrefix)
			break
		}
	}
	m.mu.RUnlock()

	if target == nil {
		return nil, fmt.Errorf("mcp: invalid tool name %q", fullName)
	}
	if strings.TrimSpace(toolName) == "" {
		return nil, fmt.Errorf("mcp: invalid tool name %q", fullName)
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

func sanitizePrefixToken(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" {
		return ""
	}
	var b strings.Builder
	b.Grow(len(value))
	lastUnderscore := false
	for _, r := range value {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			lastUnderscore = false
			continue
		}
		if !lastUnderscore {
			b.WriteByte('_')
			lastUnderscore = true
		}
	}
	return strings.Trim(b.String(), "_")
}
