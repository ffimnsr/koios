package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	"github.com/ffimnsr/koios/internal/agent"
	"github.com/ffimnsr/koios/internal/ops"
)

// pluginDescriptor identifies one internal runtime plugin.
type pluginDescriptor struct {
	ID          string
	Description string
}

// runtimePlugin registers tools or hooks into the native runtime.
type runtimePlugin interface {
	Descriptor() pluginDescriptor
	Register(*pluginRegistrar) error
}

// pluginToolContext is the invocation context passed to plugin-provided tools.
type pluginToolContext struct {
	Handler     *Handler
	PeerID      string
	Call        agent.ToolCall
	ToolContext agent.ToolRunContext
}

// pluginToolHandler executes one plugin-provided tool.
type pluginToolHandler func(context.Context, pluginToolContext) (any, error)

// pluginTool describes one plugin-provided tool.
type pluginTool struct {
	PluginID    string
	Name        string
	Description string
	Parameters  json.RawMessage
	ArgHint     string
	Available   func(*Handler) bool
	Execute     pluginToolHandler
}

type pluginHookRegistration struct {
	name     ops.HookName
	priority int
	handler  ops.Handler
}

type pluginInterceptorRegistration struct {
	name        ops.HookName
	priority    int
	interceptor ops.Interceptor
}

// pluginRegistrar collects one plugin's registrations before they are
// committed to a registry.
type pluginRegistrar struct {
	descriptor   pluginDescriptor
	tools        []pluginTool
	hooks        []pluginHookRegistration
	interceptors []pluginInterceptorRegistration
}

func newPluginRegistrar(descriptor pluginDescriptor) *pluginRegistrar {
	return &pluginRegistrar{descriptor: descriptor}
}

// RegisterTool adds one plugin-provided tool.
func (r *pluginRegistrar) RegisterTool(tool pluginTool) error {
	if r == nil {
		return fmt.Errorf("plugin registrar is nil")
	}
	tool.Name = strings.TrimSpace(tool.Name)
	if tool.Name == "" {
		return fmt.Errorf("plugin tool name is required")
	}
	if tool.Execute == nil {
		return fmt.Errorf("plugin tool %q is missing an executor", tool.Name)
	}
	if len(tool.Parameters) == 0 {
		tool.Parameters = mustJSONSchema(map[string]any{
			"type":                 "object",
			"properties":           map[string]any{},
			"additionalProperties": false,
		})
	}
	if strings.TrimSpace(tool.ArgHint) == "" {
		tool.ArgHint = `{}`
	}
	tool.PluginID = r.descriptor.ID
	for _, existing := range r.tools {
		if existing.Name == tool.Name {
			return fmt.Errorf("plugin %q registers duplicate tool %q", r.descriptor.ID, tool.Name)
		}
	}
	r.tools = append(r.tools, tool)
	return nil
}

// RegisterHook adds one lifecycle hook handler.
func (r *pluginRegistrar) RegisterHook(name ops.HookName, priority int, handler ops.Handler) error {
	if r == nil {
		return fmt.Errorf("plugin registrar is nil")
	}
	if handler == nil {
		return fmt.Errorf("plugin hook handler is nil")
	}
	r.hooks = append(r.hooks, pluginHookRegistration{name: name, priority: priority, handler: handler})
	return nil
}

// RegisterInterceptor adds one lifecycle hook interceptor.
func (r *pluginRegistrar) RegisterInterceptor(name ops.HookName, priority int, interceptor ops.Interceptor) error {
	if r == nil {
		return fmt.Errorf("plugin registrar is nil")
	}
	if interceptor == nil {
		return fmt.Errorf("plugin interceptor is nil")
	}
	r.interceptors = append(r.interceptors, pluginInterceptorRegistration{name: name, priority: priority, interceptor: interceptor})
	return nil
}

// pluginRegistry stores runtime-registered internal plugins.
type pluginRegistry struct {
	mu           sync.RWMutex
	plugins      map[string]pluginDescriptor
	toolOrder    []string
	tools        map[string]pluginTool
	hooks        []pluginHookRegistration
	interceptors []pluginInterceptorRegistration
}

// newPluginRegistry registers the provided plugins into a new registry.
func newPluginRegistry(plugins ...runtimePlugin) (*pluginRegistry, error) {
	r := &pluginRegistry{
		plugins: make(map[string]pluginDescriptor),
		tools:   make(map[string]pluginTool),
	}
	for _, plugin := range plugins {
		if err := r.Register(plugin); err != nil {
			return nil, err
		}
	}
	return r, nil
}

// Register installs one plugin into the registry.
func (r *pluginRegistry) Register(plugin runtimePlugin) error {
	if r == nil {
		return fmt.Errorf("plugin registry is nil")
	}
	if plugin == nil {
		return fmt.Errorf("plugin is nil")
	}
	descriptor := plugin.Descriptor()
	descriptor.ID = strings.TrimSpace(descriptor.ID)
	if descriptor.ID == "" {
		return fmt.Errorf("plugin id is required")
	}
	registrar := newPluginRegistrar(descriptor)
	if err := plugin.Register(registrar); err != nil {
		return fmt.Errorf("register plugin %q: %w", descriptor.ID, err)
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.plugins[descriptor.ID]; exists {
		return fmt.Errorf("plugin %q already registered", descriptor.ID)
	}
	for _, tool := range registrar.tools {
		if builtinToolDefined(tool.Name) {
			return fmt.Errorf("plugin tool %q conflicts with built-in tool", tool.Name)
		}
		if _, exists := r.tools[tool.Name]; exists {
			return fmt.Errorf("plugin tool %q already registered", tool.Name)
		}
	}
	r.plugins[descriptor.ID] = descriptor
	for _, tool := range registrar.tools {
		r.tools[tool.Name] = tool
		r.toolOrder = append(r.toolOrder, tool.Name)
	}
	r.hooks = append(r.hooks, registrar.hooks...)
	r.interceptors = append(r.interceptors, registrar.interceptors...)
	return nil
}

// Merge copies plugins, tools, and hook registrations from another registry.
func (r *pluginRegistry) Merge(other *pluginRegistry) error {
	if r == nil || other == nil || r == other {
		return nil
	}
	other.mu.RLock()
	plugins := make(map[string]pluginDescriptor, len(other.plugins))
	for id, descriptor := range other.plugins {
		plugins[id] = descriptor
	}
	toolOrder := append([]string(nil), other.toolOrder...)
	tools := make(map[string]pluginTool, len(other.tools))
	for name, tool := range other.tools {
		tools[name] = tool
	}
	hooks := append([]pluginHookRegistration(nil), other.hooks...)
	interceptors := append([]pluginInterceptorRegistration(nil), other.interceptors...)
	other.mu.RUnlock()

	r.mu.Lock()
	defer r.mu.Unlock()
	for id := range plugins {
		if _, exists := r.plugins[id]; exists {
			return fmt.Errorf("plugin %q already registered", id)
		}
	}
	for _, name := range toolOrder {
		if builtinToolDefined(name) {
			return fmt.Errorf("plugin tool %q conflicts with built-in tool", name)
		}
		if _, exists := r.tools[name]; exists {
			return fmt.Errorf("plugin tool %q already registered", name)
		}
	}
	for id, descriptor := range plugins {
		r.plugins[id] = descriptor
	}
	for _, name := range toolOrder {
		r.tools[name] = tools[name]
		r.toolOrder = append(r.toolOrder, name)
	}
	r.hooks = append(r.hooks, hooks...)
	r.interceptors = append(r.interceptors, interceptors...)
	return nil
}

// Tools returns the registered plugin tools in registration order.
func (r *pluginRegistry) Tools() []pluginTool {
	if r == nil {
		return nil
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	tools := make([]pluginTool, 0, len(r.toolOrder))
	for _, name := range r.toolOrder {
		tools = append(tools, r.tools[name])
	}
	return tools
}

// Tool looks up one plugin tool by exact name.
func (r *pluginRegistry) Tool(name string) (pluginTool, bool) {
	if r == nil {
		return pluginTool{}, false
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	tool, ok := r.tools[name]
	return tool, ok
}

// InstallHooks registers plugin-provided hooks into the provided manager.
func (r *pluginRegistry) InstallHooks(manager *ops.Manager) {
	if r == nil || manager == nil {
		return
	}
	r.mu.RLock()
	hooks := append([]pluginHookRegistration(nil), r.hooks...)
	interceptors := append([]pluginInterceptorRegistration(nil), r.interceptors...)
	r.mu.RUnlock()
	for _, hook := range hooks {
		manager.Register(hook.name, hook.priority, hook.handler)
	}
	for _, interceptor := range interceptors {
		manager.RegisterInterceptor(interceptor.name, interceptor.priority, interceptor.interceptor)
	}
}

func builtinToolDefined(name string) bool {
	for _, def := range toolDefs {
		if def.name == name {
			return true
		}
	}
	return false
}

func defaultRuntimePlugins() []runtimePlugin {
	return []runtimePlugin{builtinTimePlugin{}}
}

func mustDefaultPluginRegistry() *pluginRegistry {
	registry, err := newPluginRegistry(defaultRuntimePlugins()...)
	if err != nil {
		panic(fmt.Sprintf("build default plugin registry: %v", err))
	}
	return registry
}

func defaultPluginToolNames() []string {
	registry := mustDefaultPluginRegistry()
	tools := registry.Tools()
	names := make([]string, 0, len(tools))
	for _, tool := range tools {
		names = append(names, tool.Name)
	}
	return names
}
