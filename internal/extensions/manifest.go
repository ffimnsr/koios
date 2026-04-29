package extensions

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/ffimnsr/koios/internal/config"
	"github.com/ffimnsr/koios/internal/mcp"
	"github.com/ffimnsr/koios/internal/ops"
	"github.com/pelletier/go-toml/v2"
)

const (
	ManifestFileName   = "koios-extension.toml"
	ManifestFileSuffix = ".koios-extension.toml"
	APIVersionV1       = "koios.extension/v1"
	KindMCPServer      = "mcp_server"
	CapabilityTools    = "tools"
	CapabilityHooks    = "hooks"
	HookModeEmit       = "emit"
	HookModeIntercept  = "intercept"
)

type HookBinding struct {
	Name     string `toml:"name"`
	Event    string `toml:"event"`
	Tool     string `toml:"tool"`
	Priority int    `toml:"priority"`
	Mode     string `toml:"mode"`
	Enabled  *bool  `toml:"enabled"`
}

type Manifest struct {
	APIVersion   string        `toml:"api_version"`
	Kind         string        `toml:"kind"`
	ID           string        `toml:"id"`
	Name         string        `toml:"name"`
	Description  string        `toml:"description"`
	Enabled      *bool         `toml:"enabled"`
	Capabilities []string      `toml:"capabilities"`
	Hooks        []HookBinding `toml:"hooks"`
	MCP          struct {
		Transport string            `toml:"transport"`
		Command   string            `toml:"command"`
		Args      []string          `toml:"args"`
		Env       map[string]string `toml:"env"`
		URL       string            `toml:"url"`
		Headers   map[string]string `toml:"headers"`
		Timeout   string            `toml:"timeout"`
	} `toml:"mcp"`
}

type DiscoveredManifest struct {
	Manifest Manifest
	Path     string
}

type FilterPolicy struct {
	Allow []string
	Deny  []string
}

func Discover(paths []string) ([]DiscoveredManifest, error) {
	files, err := discoverManifestFiles(paths)
	if err != nil {
		return nil, err
	}
	manifests := make([]DiscoveredManifest, 0, len(files))
	ids := make(map[string]string)
	names := make(map[string]string)
	for _, path := range files {
		manifest, err := LoadManifest(path)
		if err != nil {
			return nil, err
		}
		if prev, ok := ids[manifest.ID]; ok {
			return nil, fmt.Errorf("extension id %q is declared by both %s and %s", manifest.ID, prev, path)
		}
		if prev, ok := names[manifest.Name]; ok {
			return nil, fmt.Errorf("extension name %q is declared by both %s and %s", manifest.Name, prev, path)
		}
		ids[manifest.ID] = path
		names[manifest.Name] = path
		manifests = append(manifests, DiscoveredManifest{Manifest: manifest, Path: path})
	}
	return manifests, nil
}

func LoadManifest(path string) (Manifest, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Manifest{}, fmt.Errorf("read extension manifest %s: %w", path, err)
	}
	var manifest Manifest
	if err := toml.Unmarshal(data, &manifest); err != nil {
		return Manifest{}, fmt.Errorf("parse extension manifest %s: %w", path, err)
	}
	if err := validateManifest(&manifest, path); err != nil {
		return Manifest{}, err
	}
	return manifest, nil
}

func MCPServers(manifests []DiscoveredManifest) ([]config.MCPServerConfig, error) {
	servers := make([]config.MCPServerConfig, 0, len(manifests))
	names := make(map[string]string)
	for _, manifest := range manifests {
		if !manifest.Enabled() {
			continue
		}
		if !manifest.HasCapability(CapabilityTools) && !manifest.HasCapability(CapabilityHooks) {
			continue
		}
		server, ok, err := manifest.MCPServerConfig()
		if err != nil {
			return nil, err
		}
		if !ok {
			continue
		}
		if prev, exists := names[server.Name]; exists {
			return nil, fmt.Errorf("mcp server name %q is declared by both %s and %s", server.Name, prev, manifest.Path)
		}
		names[server.Name] = manifest.Path
		servers = append(servers, server)
	}
	return servers, nil
}

func Filter(manifests []DiscoveredManifest, policy FilterPolicy) []DiscoveredManifest {
	allow := normalizeTokens(policy.Allow)
	deny := normalizeTokens(policy.Deny)
	if len(allow) == 0 && len(deny) == 0 {
		return append([]DiscoveredManifest(nil), manifests...)
	}
	filtered := make([]DiscoveredManifest, 0, len(manifests))
	for _, manifest := range manifests {
		if matchesPolicyToken(deny, manifest) {
			continue
		}
		if len(allow) > 0 && !matchesPolicyToken(allow, manifest) {
			continue
		}
		filtered = append(filtered, manifest)
	}
	return filtered
}

func (d DiscoveredManifest) Enabled() bool {
	if d.Manifest.Enabled == nil {
		return true
	}
	return *d.Manifest.Enabled
}

func (d DiscoveredManifest) HasCapability(name string) bool {
	want := strings.ToLower(strings.TrimSpace(name))
	for _, capability := range d.Manifest.Capabilities {
		if capability == want {
			return true
		}
	}
	return false
}

func (d DiscoveredManifest) MCPServerConfig() (config.MCPServerConfig, bool, error) {
	if d.Manifest.Kind != KindMCPServer {
		return config.MCPServerConfig{}, false, nil
	}
	server := config.MCPServerConfig{
		Name:           d.Manifest.Name,
		Transport:      d.Manifest.MCP.Transport,
		Command:        d.Manifest.MCP.Command,
		Args:           append([]string(nil), d.Manifest.MCP.Args...),
		Env:            cloneStringMap(d.Manifest.MCP.Env),
		URL:            d.Manifest.MCP.URL,
		Headers:        cloneStringMap(d.Manifest.MCP.Headers),
		Timeout:        d.Manifest.MCP.Timeout,
		Enabled:        true,
		ToolNamePrefix: mcp.PluginToolPrefix(d.Manifest.ID),
		HideTools:      !d.HasCapability(CapabilityTools),
	}
	if err := validateMCPServer(server, d.Path); err != nil {
		return config.MCPServerConfig{}, false, err
	}
	return server, true, nil
}

func discoverManifestFiles(paths []string) ([]string, error) {
	seen := make(map[string]struct{})
	var files []string
	for _, raw := range paths {
		path := strings.TrimSpace(raw)
		if path == "" {
			continue
		}
		info, err := os.Stat(path)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, fmt.Errorf("stat extension path %s: %w", path, err)
		}
		if info.IsDir() {
			err = filepath.WalkDir(path, func(candidate string, entry os.DirEntry, walkErr error) error {
				if walkErr != nil {
					return walkErr
				}
				if entry.IsDir() || !isManifestFile(entry.Name()) {
					return nil
				}
				if _, exists := seen[candidate]; exists {
					return nil
				}
				seen[candidate] = struct{}{}
				files = append(files, candidate)
				return nil
			})
			if err != nil {
				return nil, fmt.Errorf("walk extension path %s: %w", path, err)
			}
			continue
		}
		if !isManifestFile(filepath.Base(path)) {
			return nil, fmt.Errorf("extension path %s is not a recognized manifest file", path)
		}
		if _, exists := seen[path]; exists {
			continue
		}
		seen[path] = struct{}{}
		files = append(files, path)
	}
	sort.Strings(files)
	return files, nil
}

func isManifestFile(name string) bool {
	trimmed := strings.TrimSpace(name)
	return trimmed == ManifestFileName || strings.HasSuffix(trimmed, ManifestFileSuffix)
}

func validateManifest(manifest *Manifest, path string) error {
	manifest.APIVersion = strings.TrimSpace(manifest.APIVersion)
	manifest.Kind = strings.TrimSpace(manifest.Kind)
	manifest.ID = strings.TrimSpace(manifest.ID)
	manifest.Name = strings.TrimSpace(manifest.Name)
	manifest.Description = strings.TrimSpace(manifest.Description)
	manifest.Capabilities = normalizeCapabilities(manifest.Kind, manifest.Capabilities)
	manifest.Hooks = normalizeHookBindings(manifest.Hooks)
	manifest.MCP.Transport = strings.TrimSpace(manifest.MCP.Transport)
	manifest.MCP.Command = strings.TrimSpace(manifest.MCP.Command)
	manifest.MCP.URL = strings.TrimSpace(manifest.MCP.URL)
	manifest.MCP.Timeout = strings.TrimSpace(manifest.MCP.Timeout)

	if manifest.APIVersion == "" {
		return fmt.Errorf("extension manifest %s: api_version is required", path)
	}
	if manifest.APIVersion != APIVersionV1 {
		return fmt.Errorf("extension manifest %s: unsupported api_version %q", path, manifest.APIVersion)
	}
	if manifest.Kind == "" {
		return fmt.Errorf("extension manifest %s: kind is required", path)
	}
	if manifest.ID == "" {
		return fmt.Errorf("extension manifest %s: id is required", path)
	}
	if manifest.Name == "" {
		return fmt.Errorf("extension manifest %s: name is required", path)
	}
	if manifest.Kind == KindMCPServer {
		for _, capability := range manifest.Capabilities {
			if capability != CapabilityTools && capability != CapabilityHooks {
				return fmt.Errorf("extension manifest %s: mcp_server only supports capabilities %q and %q, got %q", path, CapabilityTools, CapabilityHooks, capability)
			}
		}
		if len(manifest.Hooks) > 0 && !hasCapability(manifest.Capabilities, CapabilityHooks) {
			return fmt.Errorf("extension manifest %s: hook bindings require capability %q", path, CapabilityHooks)
		}
		for _, binding := range manifest.Hooks {
			if err := validateHookBinding(binding, path); err != nil {
				return err
			}
		}
		return validateMCPServer(config.MCPServerConfig{
			Name:      manifest.Name,
			Transport: manifest.MCP.Transport,
			Command:   manifest.MCP.Command,
			URL:       manifest.MCP.URL,
			Enabled:   true,
		}, path)
	}
	return nil
}

func normalizeCapabilities(kind string, capabilities []string) []string {
	seen := make(map[string]struct{}, len(capabilities))
	normalized := make([]string, 0, len(capabilities))
	for _, capability := range capabilities {
		trimmed := strings.ToLower(strings.TrimSpace(capability))
		if trimmed == "" {
			continue
		}
		if _, exists := seen[trimmed]; exists {
			continue
		}
		seen[trimmed] = struct{}{}
		normalized = append(normalized, trimmed)
	}
	if len(normalized) == 0 && strings.TrimSpace(kind) == KindMCPServer {
		return []string{CapabilityTools}
	}
	return normalized
}

func normalizeHookBindings(bindings []HookBinding) []HookBinding {
	if len(bindings) == 0 {
		return nil
	}
	normalized := make([]HookBinding, 0, len(bindings))
	for _, binding := range bindings {
		binding.Name = strings.TrimSpace(binding.Name)
		binding.Event = strings.ToLower(strings.TrimSpace(binding.Event))
		binding.Tool = strings.TrimSpace(binding.Tool)
		binding.Mode = strings.ToLower(strings.TrimSpace(binding.Mode))
		if binding.Mode == "" {
			binding.Mode = HookModeEmit
		}
		normalized = append(normalized, binding)
	}
	return normalized
}

func normalizeTokens(values []string) map[string]struct{} {
	if len(values) == 0 {
		return nil
	}
	normalized := make(map[string]struct{}, len(values))
	for _, value := range values {
		trimmed := strings.ToLower(strings.TrimSpace(value))
		if trimmed == "" {
			continue
		}
		normalized[trimmed] = struct{}{}
	}
	return normalized
}

func matchesPolicyToken(tokens map[string]struct{}, manifest DiscoveredManifest) bool {
	if len(tokens) == 0 {
		return false
	}
	_, idMatch := tokens[strings.ToLower(strings.TrimSpace(manifest.Manifest.ID))]
	_, nameMatch := tokens[strings.ToLower(strings.TrimSpace(manifest.Manifest.Name))]
	return idMatch || nameMatch
}

func validateHookBinding(binding HookBinding, path string) error {
	if binding.Event == "" {
		return fmt.Errorf("extension manifest %s: hooks.event is required", path)
	}
	if _, ok := ops.ParseHookName(binding.Event); !ok {
		return fmt.Errorf("extension manifest %s: unsupported hooks.event %q", path, binding.Event)
	}
	if binding.Tool == "" {
		return fmt.Errorf("extension manifest %s: hooks.tool is required", path)
	}
	if binding.Mode != HookModeEmit && binding.Mode != HookModeIntercept {
		return fmt.Errorf("extension manifest %s: hooks.mode must be %q or %q", path, HookModeEmit, HookModeIntercept)
	}
	if binding.Mode == HookModeIntercept && !strings.HasPrefix(binding.Event, "before_") {
		return fmt.Errorf("extension manifest %s: hooks.mode %q requires a before_* event, got %q", path, HookModeIntercept, binding.Event)
	}
	return nil
}

func hasCapability(capabilities []string, target string) bool {
	for _, capability := range capabilities {
		if capability == target {
			return true
		}
	}
	return false
}

func validateMCPServer(server config.MCPServerConfig, path string) error {
	transport := strings.ToLower(strings.TrimSpace(server.Transport))
	if strings.TrimSpace(server.Name) == "" {
		return fmt.Errorf("extension manifest %s: mcp server name is required", path)
	}
	if transport != "stdio" && transport != "http" && transport != "sse" {
		return fmt.Errorf("extension manifest %s: unsupported mcp transport %q", path, server.Transport)
	}
	if transport == "stdio" && strings.TrimSpace(server.Command) == "" {
		return fmt.Errorf("extension manifest %s: mcp.command is required for stdio transport", path)
	}
	if (transport == "http" || transport == "sse") && strings.TrimSpace(server.URL) == "" {
		return fmt.Errorf("extension manifest %s: mcp.url is required for %s transport", path, transport)
	}
	return nil
}

func cloneStringMap(src map[string]string) map[string]string {
	if len(src) == 0 {
		return nil
	}
	dst := make(map[string]string, len(src))
	for key, value := range src {
		dst[key] = value
	}
	return dst
}
