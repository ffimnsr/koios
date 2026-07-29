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

// serverEntry holds a connected MCP server client along with its discovered assets.
type serverEntry struct {
	cfg               config.MCPServerConfig
	name              string
	toolPrefix        string
	hideTools         bool
	kind              string
	profile           string
	connected         bool
	lastError         string
	protocolVersion   string
	capabilities      json.RawMessage
	client            Client
	tools             []Tool
	resources         []Resource
	resourceTemplates []ResourceTemplate
	prompts           []Prompt
	resourceReads     map[string]cachedResourceRead
	cacheFresh        bool
	listenCancel      context.CancelFunc
}

type cachedResourceRead struct {
	result    *ResourceReadResult
	expiresAt time.Time
	private   bool
}

// Manager manages connections to multiple MCP servers and exposes their assets
// to the handler layer.
type Manager struct {
	mu            sync.RWMutex
	servers       []*serverEntry
	clientFactory func(config.MCPServerConfig) Client
}

// NewManager constructs a Manager from the given server configs.
func NewManager(cfgs []config.MCPServerConfig) *Manager {
	return NewManagerWithFactory(cfgs, newClient)
}

// NewManagerWithFactory constructs a Manager with a custom client factory.
func NewManagerWithFactory(cfgs []config.MCPServerConfig, factory func(config.MCPServerConfig) Client) *Manager {
	if factory == nil {
		factory = newClient
	}
	m := &Manager{clientFactory: factory}
	for _, c := range cfgs {
		if !c.Enabled {
			continue
		}
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
	default:
		return NewHTTPClient(c.Name, c.URL, c.Headers, timeout)
	}
}

// Start connects to all configured servers and discovers their assets.
func (m *Manager) Start(ctx context.Context) error {
	m.mu.RLock()
	servers := append([]*serverEntry(nil), m.servers...)
	m.mu.RUnlock()

	var wg sync.WaitGroup
	for _, s := range servers {
		wg.Go(func() {
			if err := m.connectServer(ctx, s); err != nil {
				slog.Warn("mcp: server init failed", "server", s.name, "err", err)
			}
		})
	}
	wg.Wait()
	return nil
}

// ServerStatus is runtime state for one configured MCP server.
type ServerStatus struct {
	Name            string
	Kind            string
	ProfileName     string
	Hidden          bool
	Connected       bool
	ProtocolVersion string
	Capabilities    json.RawMessage
	ToolCount       int
	ResourceCount   int
	PromptCount     int
	CacheFresh      bool
	SubscriptionOn  bool
	LastError       string
}

// ServerStatuses returns runtime state for all configured MCP servers.
func (m *Manager) ServerStatuses() []ServerStatus {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]ServerStatus, 0, len(m.servers))
	for _, s := range m.servers {
		out = append(out, serverStatusSnapshot(s))
	}
	return out
}

// ServerStatus returns runtime state for one configured MCP server selected by kind/profile.
func (m *Manager) ServerStatus(kind, profile string) (ServerStatus, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	s, ok := m.findServerLocked(kind, profile)
	if !ok {
		return ServerStatus{}, false
	}
	return serverStatusSnapshot(s), true
}

// ServerStatusByName returns runtime state for one configured MCP server selected by runtime name.
func (m *Manager) ServerStatusByName(name string) (ServerStatus, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, s := range m.servers {
		if s.name == name {
			return serverStatusSnapshot(s), true
		}
	}
	return ServerStatus{}, false
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

// StopServer disconnects one configured server and resets it so it can be started again later.
func (m *Manager) StopServer(kind, profile string) (ServerStatus, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.findServerLocked(kind, profile)
	if !ok {
		return ServerStatus{}, fmt.Errorf("mcp: server kind=%q profile=%q not found", kind, profile)
	}
	if s.listenCancel != nil {
		s.listenCancel()
		s.listenCancel = nil
	}
	if s.client != nil {
		if err := s.client.Close(); err != nil {
			s.lastError = err.Error()
			s.connected = false
			clearServerCaches(s)
			s.client = m.clientFactory(s.cfg)
			return serverStatusSnapshot(s), err
		}
	}
	s.connected = false
	s.lastError = ""
	s.protocolVersion = ""
	clearServerCaches(s)
	s.client = m.clientFactory(s.cfg)
	return serverStatusSnapshot(s), nil
}

// ToolPrefix returns default runtime tool prefix.
func ToolPrefix(serverName string) string { return "mcp__" + serverName + "__" }

// ToolName returns default prefixed tool name used in the handler registry.
func ToolName(serverName, toolName string) string { return ToolPrefix(serverName) + toolName }

// PluginToolPrefix returns the runtime tool prefix for a manifest-backed extension server.
func PluginToolPrefix(manifestID string) string {
	namespace := sanitizePrefixToken(manifestID)
	if namespace == "" {
		namespace = "plugin"
	}
	return "mcp_plug_" + namespace + "__"
}

// ParseToolName splits a prefixed MCP tool name into a namespace token and tool name.
func ParseToolName(name string) (namespace, toolName string, ok bool) {
	for _, prefix := range []string{"mcp__", "mcp_plug_"} {
		if !strings.HasPrefix(name, prefix) {
			continue
		}
		rest := name[len(prefix):]
		idx := strings.Index(rest, "__")
		if idx < 0 || idx == 0 || idx+2 >= len(rest) {
			return "", "", false
		}
		return rest[:idx], rest[idx+2:], true
	}
	return "", "", false
}

// RegisteredTool is one tool offered by an MCP server, ready for integration into handler.toolDef.
type RegisteredTool struct {
	FullName     string
	ServerName   string
	ToolName     string
	Description  string
	InputSchema  json.RawMessage
	OutputSchema json.RawMessage
	Annotations  json.RawMessage
	Kind         string
	ProfileName  string
	Hidden       bool
}

// ListTools returns all tools from all connected servers with their prefixed names.
func (m *Manager) ListTools() []RegisteredTool { return m.listTools(false) }

// AllTools returns all tools from all connected servers, including hidden tools.
func (m *Manager) AllTools() []RegisteredTool { return m.listTools(true) }

func (m *Manager) listTools(includeHidden bool) []RegisteredTool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var out []RegisteredTool
	for _, s := range m.servers {
		if s.hideTools && !includeHidden {
			continue
		}
		for _, t := range s.tools {
			out = append(out, registeredToolFromEntry(s, t))
		}
	}
	return out
}

func registeredToolFromEntry(s *serverEntry, t Tool) RegisteredTool {
	return RegisteredTool{
		FullName:     s.toolPrefix + t.Name,
		ServerName:   s.name,
		ToolName:     t.Name,
		Description:  t.Description,
		InputSchema:  t.InputSchema,
		OutputSchema: t.OutputSchema,
		Annotations:  t.Annotations,
		Kind:         s.kind,
		ProfileName:  s.profile,
		Hidden:       s.hideTools,
	}
}

// Search returns MCP assets matching query across connected servers.
func (m *Manager) Search(query string, limit int) []map[string]any {
	query = strings.ToLower(strings.TrimSpace(query))
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	var out []map[string]any
	add := func(item map[string]any, haystack ...string) {
		if len(out) >= limit {
			return
		}
		if query != "" {
			matched := false
			for _, value := range haystack {
				if strings.Contains(strings.ToLower(value), query) {
					matched = true
					break
				}
			}
			if !matched {
				return
			}
		}
		out = append(out, item)
	}
	for _, s := range m.servers {
		if !s.connected {
			continue
		}
		for _, tool := range s.tools {
			add(map[string]any{"type": "tool", "server": s.name, "name": tool.Name, "full_name": s.toolPrefix + tool.Name, "description": tool.Description}, tool.Name, tool.Description)
		}
		for _, resource := range s.resources {
			add(map[string]any{"type": "resource", "server": s.name, "uri": resource.URI, "name": resource.Name, "description": resource.Description}, resource.URI, resource.Name, resource.Description)
		}
		for _, tmpl := range s.resourceTemplates {
			add(map[string]any{"type": "resource_template", "server": s.name, "uri_template": tmpl.URITemplate, "name": tmpl.Name, "description": tmpl.Description}, tmpl.URITemplate, tmpl.Name, tmpl.Description)
		}
		for _, prompt := range s.prompts {
			add(map[string]any{"type": "prompt", "server": s.name, "name": prompt.Name, "description": prompt.Description}, prompt.Name, prompt.Description)
		}
	}
	return out
}

// ToolDetails returns full metadata for one runtime tool name.
func (m *Manager) ToolDetails(fullName string) (RegisteredTool, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, s := range m.servers {
		if !strings.HasPrefix(fullName, s.toolPrefix) {
			continue
		}
		name := strings.TrimPrefix(fullName, s.toolPrefix)
		for _, tool := range s.tools {
			if tool.Name == name {
				return registeredToolFromEntry(s, tool), true
			}
		}
	}
	return RegisteredTool{}, false
}

// ListResources returns cached resources for one connected server.
func (m *Manager) ListResources(_ context.Context, serverName string) ([]Resource, error) {
	s, err := m.connectedServerByName(serverName)
	if err != nil {
		return nil, err
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	return append([]Resource(nil), s.resources...), nil
}

// ListResourceTemplates returns cached resource templates for one connected server.
func (m *Manager) ListResourceTemplates(_ context.Context, serverName string) ([]ResourceTemplate, error) {
	s, err := m.connectedServerByName(serverName)
	if err != nil {
		return nil, err
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	return append([]ResourceTemplate(nil), s.resourceTemplates...), nil
}

// ReadResource reads one resource from a connected server.
func (m *Manager) ReadResource(ctx context.Context, serverName, uri string) (*ResourceReadResult, error) {
	s, err := m.connectedServerByName(serverName)
	if err != nil {
		return nil, err
	}
	m.mu.RLock()
	if cached, ok := s.resourceReads[uri]; ok && time.Now().Before(cached.expiresAt) {
		result := cloneResourceReadResult(cached.result)
		m.mu.RUnlock()
		return result, nil
	}
	m.mu.RUnlock()
	result, err := s.client.ReadResource(ctx, uri)
	if err != nil {
		return nil, err
	}
	if ttl := result.TTLMs; ttl > 0 {
		m.mu.Lock()
		if s.resourceReads == nil {
			s.resourceReads = map[string]cachedResourceRead{}
		}
		s.resourceReads[uri] = cachedResourceRead{result: cloneResourceReadResult(result), expiresAt: time.Now().Add(time.Duration(ttl) * time.Millisecond), private: strings.EqualFold(result.CacheScope, "private")}
		s.cacheFresh = true
		m.mu.Unlock()
	}
	return result, nil
}

// ListPrompts returns cached prompts for one connected server.
func (m *Manager) ListPrompts(_ context.Context, serverName string) ([]Prompt, error) {
	s, err := m.connectedServerByName(serverName)
	if err != nil {
		return nil, err
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	return append([]Prompt(nil), s.prompts...), nil
}

// GetPrompt resolves one prompt from a connected server.
func (m *Manager) GetPrompt(ctx context.Context, serverName, name string, args map[string]any) (*PromptGetResult, error) {
	s, err := m.connectedServerByName(serverName)
	if err != nil {
		return nil, err
	}
	return s.client.GetPrompt(ctx, name, args)
}

// CallTool invokes a tool on the appropriate MCP server. fullName must match one of the runtime prefixes.
func (m *Manager) CallTool(ctx context.Context, fullName string, rawArgs json.RawMessage) (any, error) {
	result, err := m.CallToolResult(ctx, fullName, rawArgs)
	if err != nil {
		return nil, err
	}
	return extractText(result), nil
}

// CallToolResult invokes a tool on the appropriate MCP server and returns raw MCP tool result content.
func (m *Manager) CallToolResult(ctx context.Context, fullName string, rawArgs json.RawMessage) (*ToolResult, error) {
	return m.CallToolResultWithInput(ctx, fullName, rawArgs, nil, nil)
}

// CallToolResultWithInput retries MRTR input_required requests with explicit user-provided input responses.
func (m *Manager) CallToolResultWithInput(ctx context.Context, fullName string, rawArgs, inputResponses, requestState json.RawMessage) (*ToolResult, error) {
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
	return target.client.CallToolWithInput(ctx, toolName, args, inputResponses, requestState)
}

// AddServer adds a new server entry at runtime.
func (m *Manager) AddServer(ctx context.Context, cfg config.MCPServerConfig) (ServerStatus, error) {
	prefix := strings.TrimSpace(cfg.ToolNamePrefix)
	if prefix == "" {
		prefix = ToolPrefix(cfg.Name)
	}
	entry := &serverEntry{
		cfg:        cfg,
		name:       cfg.Name,
		toolPrefix: prefix,
		hideTools:  cfg.HideTools,
		kind:       strings.TrimSpace(cfg.Kind),
		profile:    strings.TrimSpace(cfg.ProfileName),
		client:     m.clientFactory(cfg),
	}
	m.mu.Lock()
	for _, s := range m.servers {
		if strings.EqualFold(s.name, entry.name) {
			m.mu.Unlock()
			return ServerStatus{}, fmt.Errorf("mcp: server %q already exists", entry.name)
		}
	}
	m.servers = append(m.servers, entry)
	m.mu.Unlock()
	if cfg.Enabled {
		if err := m.connectServer(ctx, entry); err != nil {
			slog.Warn("mcp: add-server connect failed", "server", entry.name, "err", err)
		}
	}
	m.mu.RLock()
	status := serverStatusSnapshot(entry)
	m.mu.RUnlock()
	return status, nil
}

// RemoveServer disconnects and removes a server entry by runtime name.
func (m *Manager) RemoveServer(name string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i, s := range m.servers {
		if s.name == name {
			if s.listenCancel != nil {
				s.listenCancel()
			}
			if s.client != nil {
				_ = s.client.Close()
			}
			m.servers = append(m.servers[:i], m.servers[i+1:]...)
			return nil
		}
	}
	return fmt.Errorf("mcp: server %q not found", name)
}

// UpdateServer replaces configuration for an existing server entry.
func (m *Manager) UpdateServer(ctx context.Context, cfg config.MCPServerConfig) (ServerStatus, error) {
	m.mu.Lock()
	var target *serverEntry
	for _, s := range m.servers {
		if s.name == cfg.Name {
			target = s
			break
		}
	}
	if target == nil {
		m.mu.Unlock()
		return ServerStatus{}, fmt.Errorf("mcp: server %q not found", cfg.Name)
	}
	if target.listenCancel != nil {
		target.listenCancel()
		target.listenCancel = nil
	}
	_ = target.client.Close()
	target.cfg = cfg
	target.name = cfg.Name
	prefix := strings.TrimSpace(cfg.ToolNamePrefix)
	if prefix == "" {
		prefix = ToolPrefix(cfg.Name)
	}
	target.toolPrefix = prefix
	target.hideTools = cfg.HideTools
	target.kind = strings.TrimSpace(cfg.Kind)
	target.profile = strings.TrimSpace(cfg.ProfileName)
	target.connected = false
	target.lastError = ""
	target.protocolVersion = ""
	clearServerCaches(target)
	target.client = m.clientFactory(cfg)
	m.mu.Unlock()
	if cfg.Enabled {
		if err := m.connectServer(ctx, target); err != nil {
			slog.Warn("mcp: update-server connect failed", "server", target.name, "err", err)
		}
	}
	m.mu.RLock()
	status := serverStatusSnapshot(target)
	m.mu.RUnlock()
	return status, nil
}

// HasServer returns true when a server with the given runtime name is registered.
func (m *Manager) HasServer(name string) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, s := range m.servers {
		if s.name == name {
			return true
		}
	}
	return false
}

// Close shuts down all MCP server connections.
func (m *Manager) Close() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, s := range m.servers {
		if s.listenCancel != nil {
			s.listenCancel()
			s.listenCancel = nil
		}
		if err := s.client.Close(); err != nil {
			slog.Warn("mcp: close error", "server", s.name, "err", err)
		}
		s.connected = false
		s.lastError = ""
		s.protocolVersion = ""
		clearServerCaches(s)
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
		s.lastError = err.Error()
		s.protocolVersion = ""
		clearServerCaches(s)
		m.mu.Unlock()
		return err
	}

	discover, err := s.client.Discover(ctx)
	if err == nil && discover != nil {
		if strings.TrimSpace(discover.ProtocolVersion) != "" {
			s.protocolVersion = strings.TrimSpace(discover.ProtocolVersion)
		}
		s.capabilities = append(json.RawMessage(nil), discover.Capabilities...)
	} else if err != nil && !isMethodNotFoundError(err) {
		slog.Debug("mcp: discover after initialize failed", "server", s.name, "err", err)
	}
	if s.protocolVersion == "" {
		s.protocolVersion = ProtocolVersion2026
	}

	tools, err := s.client.ListTools(ctx)
	if err != nil {
		if s.client != nil {
			_ = s.client.Close()
		}
		s.client = m.clientFactory(s.cfg)
		m.mu.Lock()
		s.connected = false
		s.lastError = err.Error()
		s.protocolVersion = ""
		clearServerCaches(s)
		m.mu.Unlock()
		return err
	}

	resources, resourceTemplates, prompts := loadOptionalServerAssets(ctx, s)

	notifications, listenCtx, listenCancel, listenErr := startClientListener(ctx, s.client)
	if listenErr != nil {
		slog.Debug("mcp: listen unavailable", "server", s.name, "err", listenErr)
	}

	m.mu.Lock()
	if s.listenCancel != nil {
		s.listenCancel()
	}
	s.connected = true
	s.lastError = ""
	s.tools = tools
	s.resources = resources
	s.resourceTemplates = resourceTemplates
	s.prompts = prompts
	s.resourceReads = map[string]cachedResourceRead{}
	s.cacheFresh = true
	s.listenCancel = nil
	if listenErr == nil {
		s.listenCancel = listenCancel
	}
	m.mu.Unlock()
	if listenErr == nil {
		go m.consumeNotifications(listenCtx, s.name, notifications)
	}
	slog.Info("mcp: server connected", "server", s.name, "tools", len(tools), "resources", len(resources), "prompts", len(prompts))
	return nil
}

func startClientListener(parent context.Context, client Client) (<-chan Notification, context.Context, context.CancelFunc, error) {
	listenCtx, listenCancel := context.WithCancel(context.WithoutCancel(parent))
	notifications, err := client.Listen(listenCtx)
	if err != nil {
		listenCancel()
		return nil, nil, nil, err
	}
	return notifications, listenCtx, listenCancel, nil
}

func loadOptionalServerAssets(ctx context.Context, s *serverEntry) ([]Resource, []ResourceTemplate, []Prompt) {
	var resources []Resource
	if listed, err := s.client.ListResources(ctx); err == nil {
		resources = listed
	} else if !isOptionalCapabilityError(err) {
		slog.Debug("mcp: resources/list failed", "server", s.name, "err", err)
	}
	var templates []ResourceTemplate
	if listed, err := s.client.ListResourceTemplates(ctx); err == nil {
		templates = listed
	} else if !isOptionalCapabilityError(err) {
		slog.Debug("mcp: resources/templates/list failed", "server", s.name, "err", err)
	}
	var prompts []Prompt
	if listed, err := s.client.ListPrompts(ctx); err == nil {
		prompts = listed
	} else if !isOptionalCapabilityError(err) {
		slog.Debug("mcp: prompts/list failed", "server", s.name, "err", err)
	}
	return resources, templates, prompts
}

func clearServerCaches(s *serverEntry) {
	s.tools = nil
	s.resources = nil
	s.resourceTemplates = nil
	s.prompts = nil
	s.resourceReads = nil
	s.cacheFresh = false
	s.capabilities = nil
}

func (m *Manager) consumeNotifications(ctx context.Context, serverName string, notifications <-chan Notification) {
	for {
		select {
		case <-ctx.Done():
			return
		case n, ok := <-notifications:
			if !ok {
				return
			}
			m.applyNotification(serverName, n)
		}
	}
}

func (m *Manager) applyNotification(serverName string, n Notification) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var target *serverEntry
	for _, s := range m.servers {
		if s.name == serverName {
			target = s
			break
		}
	}
	if target == nil {
		return
	}
	switch n.Method {
	case "notifications/tools/list_changed":
		target.tools = nil
		target.cacheFresh = false
	case "notifications/resources/list_changed":
		target.resources = nil
		target.resourceTemplates = nil
		target.resourceReads = nil
		target.cacheFresh = false
	case "notifications/resources/updated":
		if target.resourceReads == nil {
			return
		}
		var params struct {
			URI string `json:"uri"`
		}
		if err := json.Unmarshal(n.Params, &params); err == nil && strings.TrimSpace(params.URI) != "" {
			delete(target.resourceReads, params.URI)
		} else {
			target.resourceReads = nil
		}
		target.cacheFresh = false
	case "notifications/prompts/list_changed":
		target.prompts = nil
		target.cacheFresh = false
	}
}

func cloneResourceReadResult(result *ResourceReadResult) *ResourceReadResult {
	if result == nil {
		return nil
	}
	clone := *result
	clone.Contents = append([]ResourceContent(nil), result.Contents...)
	return &clone
}

func (m *Manager) connectedServerByName(name string) (*serverEntry, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, s := range m.servers {
		if s.name != name {
			continue
		}
		if !s.connected {
			return nil, fmt.Errorf("mcp: server %q is not connected", name)
		}
		return s, nil
	}
	return nil, fmt.Errorf("mcp: server %q not found", name)
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
		Name:            s.name,
		Kind:            s.kind,
		ProfileName:     s.profile,
		Hidden:          s.hideTools,
		Connected:       s.connected,
		ProtocolVersion: s.protocolVersion,
		Capabilities:    append(json.RawMessage(nil), s.capabilities...),
		ToolCount:       len(s.tools),
		ResourceCount:   len(s.resources),
		PromptCount:     len(s.prompts),
		CacheFresh:      s.cacheFresh,
		SubscriptionOn:  s.listenCancel != nil,
		LastError:       s.lastError,
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
