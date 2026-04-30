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
	cfg        config.MCPServerConfig
	name       string
	toolPrefix string
	hideTools  bool
	kind       string
	profile    string
	connected  bool
	lastError  string
	client     Client
	tools      []Tool // cached after Initialize
}

// Manager manages connections to multiple MCP servers and exposes their tools
// to the handler layer.
type Manager struct {
	mu            sync.RWMutex
	servers       []*serverEntry
	clientFactory func(config.MCPServerConfig) Client
}

// NewManager constructs a Manager from the given server configs. Servers with
// Enabled=false are skipped. Call Start to connect to the servers.
func NewManager(cfgs []config.MCPServerConfig) *Manager {
	return NewManagerWithFactory(cfgs, newClient)
}

// NewManagerWithFactory constructs a Manager with a custom client factory.
// It is primarily useful for tests and alternate client implementations.
func NewManagerWithFactory(cfgs []config.MCPServerConfig, factory func(config.MCPServerConfig) Client) *Manager {
	if factory == nil {
		factory = newClient
	}
	m := &Manager{clientFactory: factory}
	for _, c := range cfgs {
		if !c.Enabled {
			continue
		}
		c := c // capture loop variable
		prefix := strings.TrimSpace(c.ToolNamePrefix)
		if prefix == "" {
			prefix = ToolPrefix(c.Name)
		}
		m.servers = append(m.servers, &serverEntry{
			cfg:        c,
			name:       c.Name,
			toolPrefix: prefix,
			hideTools:  c.HideTools,
			kind:       strings.TrimSpace(c.Kind),
			profile:    strings.TrimSpace(c.ProfileName),
			client:     factory(c),
		})
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
	m.mu.RLock()
	servers := append([]*serverEntry(nil), m.servers...)
	m.mu.RUnlock()

	var wg sync.WaitGroup
	for _, s := range servers {
		s := s
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := m.connectServer(ctx, s); err != nil {
				slog.Warn("mcp: server init failed", "server", s.name, "err", err)
			}
		}()
	}
	wg.Wait()
	return nil
}

// ServerStatus is the runtime state of one configured MCP server.
type ServerStatus struct {
	Name        string
	Kind        string
	ProfileName string
	Hidden      bool
	Connected   bool
	ToolCount   int
	LastError   string
}

// ServerStatuses returns the runtime state for all configured MCP servers.
func (m *Manager) ServerStatuses() []ServerStatus {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]ServerStatus, 0, len(m.servers))
	for _, s := range m.servers {
		out = append(out, serverStatusSnapshot(s))
	}
	return out
}

// ServerStatus returns the runtime state for one configured MCP server.
func (m *Manager) ServerStatus(kind, profile string) (ServerStatus, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	s, ok := m.findServerLocked(kind, profile)
	if !ok {
		return ServerStatus{}, false
	}
	return serverStatusSnapshot(s), true
}

// EnsureServer initializes a configured server when it is not connected yet.
func (m *Manager) EnsureServer(ctx context.Context, kind, profile string) (ServerStatus, error) {
	m.mu.RLock()
	s, ok := m.findServerLocked(kind, profile)
	if !ok {
		m.mu.RUnlock()
		return ServerStatus{}, fmt.Errorf("mcp: server kind=%q profile=%q not found", kind, profile)
	}
	if s.connected {
		status := serverStatusSnapshot(s)
		m.mu.RUnlock()
		return status, nil
	}
	m.mu.RUnlock()
	if err := m.connectServer(ctx, s); err != nil {
		m.mu.RLock()
		status := serverStatusSnapshot(s)
		m.mu.RUnlock()
		return status, err
	}
	m.mu.RLock()
	status := serverStatusSnapshot(s)
	m.mu.RUnlock()
	return status, nil
}

// StopServer disconnects one configured server and resets it so it can be
// started again later via EnsureServer or Start.
func (m *Manager) StopServer(kind, profile string) (ServerStatus, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.findServerLocked(kind, profile)
	if !ok {
		return ServerStatus{}, fmt.Errorf("mcp: server kind=%q profile=%q not found", kind, profile)
	}
	if s.client != nil {
		if err := s.client.Close(); err != nil {
			s.lastError = err.Error()
			s.connected = false
			s.tools = nil
			s.client = m.clientFactory(s.cfg)
			return serverStatusSnapshot(s), err
		}
	}
	s.connected = false
	s.lastError = ""
	s.tools = nil
	s.client = m.clientFactory(s.cfg)
	return serverStatusSnapshot(s), nil
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
	Kind        string
	ProfileName string
	Hidden      bool
}

// ListTools returns all tools from all connected servers with their prefixed names.
func (m *Manager) ListTools() []RegisteredTool {
	return m.listTools(false)
}

// AllTools returns all tools from all connected servers, including hidden tools.
func (m *Manager) AllTools() []RegisteredTool {
	return m.listTools(true)
}

func (m *Manager) listTools(includeHidden bool) []RegisteredTool {
	m.mu.RLock()
	defer m.mu.RUnlock()

	var out []RegisteredTool
	for _, s := range m.servers {
		if s.hideTools && !includeHidden {
			continue
		}
		for _, t := range s.tools {
			out = append(out, RegisteredTool{
				FullName:    s.toolPrefix + t.Name,
				ServerName:  s.name,
				ToolName:    t.Name,
				Description: t.Description,
				InputSchema: t.InputSchema,
				Kind:        s.kind,
				ProfileName: s.profile,
				Hidden:      s.hideTools,
			})
		}
	}
	return out
}

// CallTool invokes a tool on the appropriate MCP server. fullName must match
// one of the prefixes returned by ToolName or PluginToolPrefix.
func (m *Manager) CallTool(ctx context.Context, fullName string, rawArgs json.RawMessage) (any, error) {
	result, err := m.CallToolResult(ctx, fullName, rawArgs)
	if err != nil {
		return nil, err
	}
	return extractText(result), nil
}

// CallToolResult invokes a tool on the appropriate MCP server and returns the
// raw MCP tool result content.
func (m *Manager) CallToolResult(ctx context.Context, fullName string, rawArgs json.RawMessage) (*ToolResult, error) {
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
	if !target.connected {
		return nil, fmt.Errorf("mcp: server %q is not connected", target.name)
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
	return result, nil
}

// Close shuts down all MCP server connections.
func (m *Manager) Close() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, s := range m.servers {
		if err := s.client.Close(); err != nil {
			slog.Warn("mcp: close error", "server", s.name, "err", err)
		}
		s.connected = false
		s.tools = nil
		s.lastError = ""
	}
}

func (m *Manager) connectServer(ctx context.Context, s *serverEntry) error {
	if s == nil {
		return fmt.Errorf("mcp: nil server entry")
	}
	if s.client == nil {
		s.client = m.clientFactory(s.cfg)
	}
	if err := s.client.Initialize(ctx); err != nil {
		if s.client != nil {
			_ = s.client.Close()
		}
		s.client = m.clientFactory(s.cfg)
		m.mu.Lock()
		s.connected = false
		s.tools = nil
		s.lastError = err.Error()
		m.mu.Unlock()
		return err
	}
	tools, err := s.client.ListTools(ctx)
	if err != nil {
		if s.client != nil {
			_ = s.client.Close()
		}
		s.client = m.clientFactory(s.cfg)
		m.mu.Lock()
		s.connected = false
		s.tools = nil
		s.lastError = err.Error()
		m.mu.Unlock()
		return err
	}
	m.mu.Lock()
	s.connected = true
	s.lastError = ""
	s.tools = tools
	m.mu.Unlock()
	slog.Info("mcp: server connected", "server", s.name, "tools", len(tools))
	return nil
}

func (m *Manager) findServerLocked(kind, profile string) (*serverEntry, bool) {
	kind = strings.TrimSpace(kind)
	profile = strings.TrimSpace(profile)
	for _, s := range m.servers {
		if kind != "" && !strings.EqualFold(s.kind, kind) {
			continue
		}
		if profile != "" && !strings.EqualFold(s.profile, profile) {
			continue
		}
		return s, true
	}
	return nil, false
}

func serverStatusSnapshot(s *serverEntry) ServerStatus {
	if s == nil {
		return ServerStatus{}
	}
	return ServerStatus{
		Name:        s.name,
		Kind:        s.kind,
		ProfileName: s.profile,
		Hidden:      s.hideTools,
		Connected:   s.connected,
		ToolCount:   len(s.tools),
		LastError:   s.lastError,
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
