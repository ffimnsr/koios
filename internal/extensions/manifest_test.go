package extensions

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/ffimnsr/koios/internal/config"
)

func TestDiscoverLoadsWorkspaceStyleManifest(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "workspace", "extensions", "demo")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	manifestPath := filepath.Join(dir, ManifestFileName)
	content := []byte(`api_version = "koios.extension/v1"
kind = "mcp_server"
id = "demo.filesystem"
name = "filesystem"
description = "Filesystem MCP extension"
capabilities = ["tools"]

[mcp]
transport = "stdio"
command = "npx"
args = ["-y", "@modelcontextprotocol/server-filesystem", "/tmp"]
`)
	if err := os.WriteFile(manifestPath, content, 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	manifests, err := Discover([]string{filepath.Join(root, "workspace", "extensions")})
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(manifests) != 1 {
		t.Fatalf("expected 1 manifest, got %d", len(manifests))
	}
	if manifests[0].Manifest.ID != "demo.filesystem" || manifests[0].Manifest.Name != "filesystem" {
		t.Fatalf("unexpected manifest: %#v", manifests[0])
	}

	servers, err := MCPServers(manifests)
	if err != nil {
		t.Fatalf("MCPServers: %v", err)
	}
	if len(servers) != 1 {
		t.Fatalf("expected 1 mcp server, got %d", len(servers))
	}
	if servers[0].Transport != "stdio" || servers[0].Command != "npx" {
		t.Fatalf("unexpected server config: %#v", servers[0])
	}
	if servers[0].ToolNamePrefix != "mcp_plug_demo_filesystem__" {
		t.Fatalf("unexpected extension tool prefix: %#v", servers[0].ToolNamePrefix)
	}
	if servers[0].HideTools {
		t.Fatalf("tools-capable extension should stay visible to the agent catalog: %#v", servers[0])
	}
}

func TestDiscoverRejectsDuplicateExtensionIDs(t *testing.T) {
	root := t.TempDir()
	for _, dir := range []string{"one", "two"} {
		path := filepath.Join(root, dir)
		if err := os.MkdirAll(path, 0o755); err != nil {
			t.Fatalf("MkdirAll(%s): %v", dir, err)
		}
		content := `api_version = "koios.extension/v1"
kind = "mcp_server"
id = "duplicate"
name = "` + dir + `"

[mcp]
transport = "stdio"
command = "echo"
`
		if err := os.WriteFile(filepath.Join(path, ManifestFileName), []byte(content), 0o644); err != nil {
			t.Fatalf("WriteFile(%s): %v", dir, err)
		}
	}

	if _, err := Discover([]string{root}); err == nil {
		t.Fatal("expected duplicate extension id error, got nil")
	}
}

func TestMCPServersSkipsDisabledManifest(t *testing.T) {
	manifests := []DiscoveredManifest{{
		Manifest: Manifest{
			APIVersion:   APIVersionV1,
			Kind:         KindMCPServer,
			ID:           "demo.disabled",
			Name:         "disabled",
			Enabled:      boolPtr(false),
			Capabilities: []string{CapabilityTools},
			MCP: struct {
				Transport string            `toml:"transport"`
				Command   string            `toml:"command"`
				Args      []string          `toml:"args"`
				Env       map[string]string `toml:"env"`
				URL       string            `toml:"url"`
				Headers   map[string]string `toml:"headers"`
				Timeout   string            `toml:"timeout"`
			}{Transport: "stdio", Command: "echo"},
		},
		Path: "disabled",
	}}

	servers, err := MCPServers(manifests)
	if err != nil {
		t.Fatalf("MCPServers: %v", err)
	}
	if len(servers) != 0 {
		t.Fatalf("expected disabled extension to be skipped, got %#v", servers)
	}
}

func TestLoadManifestDefaultsMCPServerCapabilityToTools(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, ManifestFileName)
	content := []byte(`api_version = "koios.extension/v1"
kind = "mcp_server"
id = "demo.default-tools"
name = "default-tools"

[mcp]
transport = "stdio"
command = "echo"
`)
	if err := os.WriteFile(path, content, 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	manifest, err := LoadManifest(path)
	if err != nil {
		t.Fatalf("LoadManifest: %v", err)
	}
	if len(manifest.Capabilities) != 1 || manifest.Capabilities[0] != CapabilityTools {
		t.Fatalf("expected default tools capability, got %#v", manifest.Capabilities)
	}
}

func TestLoadManifestAcceptsHookBindings(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, ManifestFileName)
	content := []byte(`api_version = "koios.extension/v1"
kind = "mcp_server"
id = "demo.hooks"
name = "hook-runner"
capabilities = ["hooks"]

[mcp]
transport = "stdio"
command = "echo"

[[hooks]]
name = "rewrite-llm"
event = "before_llm"
mode = "intercept"
tool = "rewrite_request"

[[hooks]]
event = "after_message"
tool = "audit_event"
`)
	if err := os.WriteFile(path, content, 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	manifest, err := LoadManifest(path)
	if err != nil {
		t.Fatalf("LoadManifest: %v", err)
	}
	if len(manifest.Hooks) != 2 {
		t.Fatalf("expected 2 hook bindings, got %#v", manifest.Hooks)
	}
	if manifest.Hooks[0].Mode != HookModeIntercept {
		t.Fatalf("unexpected first hook mode: %#v", manifest.Hooks[0])
	}
	if manifest.Hooks[1].Mode != HookModeEmit {
		t.Fatalf("expected default hook mode %q, got %#v", HookModeEmit, manifest.Hooks[1])
	}

	servers, err := MCPServers([]DiscoveredManifest{{Manifest: manifest, Path: path}})
	if err != nil {
		t.Fatalf("MCPServers: %v", err)
	}
	if len(servers) != 1 || !servers[0].HideTools {
		t.Fatalf("expected hooks-only extension server to stay internal, got %#v", servers)
	}
}

func TestLoadManifestRejectsHooksWithoutCapability(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, ManifestFileName)
	content := []byte(`api_version = "koios.extension/v1"
kind = "mcp_server"
id = "demo.invalid-hooks"
name = "invalid-hooks"
capabilities = ["tools"]

[mcp]
transport = "stdio"
command = "echo"

[[hooks]]
event = "after_message"
tool = "audit_event"
`)
	if err := os.WriteFile(path, content, 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	if _, err := LoadManifest(path); err == nil {
		t.Fatal("expected hook capability validation error, got nil")
	}
}

func TestFilterAppliesAllowAndDenyLists(t *testing.T) {
	manifests := []DiscoveredManifest{
		{Manifest: Manifest{ID: "demo.allowed", Name: "allowed"}},
		{Manifest: Manifest{ID: "demo.denied", Name: "denied"}},
		{Manifest: Manifest{ID: "demo.other", Name: "other"}},
	}
	filtered := Filter(manifests, FilterPolicy{
		Allow: []string{"demo.allowed", "other"},
		Deny:  []string{"demo.denied"},
	})
	if len(filtered) != 2 {
		t.Fatalf("expected 2 manifests after filtering, got %#v", filtered)
	}
	if filtered[0].Manifest.ID != "demo.allowed" || filtered[1].Manifest.ID != "demo.other" {
		t.Fatalf("unexpected filtered manifests: %#v", filtered)
	}
}

func TestConfigExtensionSearchPathsIncludesWorkspaceAndExtras(t *testing.T) {
	cfg := config.Default()
	cfg.WorkspaceRoot = "/tmp/koios-workspace"
	cfg.ExtensionDirs = []string{"/opt/koios/extensions", "/srv/koios/extensions"}

	paths := cfg.ExtensionSearchPaths()
	if len(paths) != 3 {
		t.Fatalf("expected 3 extension search paths, got %#v", paths)
	}
	if paths[0] != "/tmp/koios-workspace/extensions" {
		t.Fatalf("unexpected workspace extension dir: %#v", paths)
	}
}

func boolPtr(v bool) *bool { return &v }
