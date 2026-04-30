package browser

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/ffimnsr/koios/internal/config"
)

func TestResolveActiveProfilePrefersSessionThenDefault(t *testing.T) {
	cfg := &config.Config{Browser: config.BrowserConfig{
		DefaultProfile: "work",
		Profiles: []config.BrowserProfileConfig{
			{Name: "work", Enabled: true},
			{Name: "review", Enabled: true},
		},
	}}
	if got := ResolveActiveProfile(cfg, "review"); got != "review" {
		t.Fatalf("expected session override, got %q", got)
	}
	if got := ResolveActiveProfile(cfg, "missing"); got != "work" {
		t.Fatalf("expected default profile fallback, got %q", got)
	}
}

func TestMCPServersManagedProfileUsesWorkspaceBrowserDir(t *testing.T) {
	root := t.TempDir()
	cfg := config.Default()
	cfg.WorkspaceRoot = root
	cfg.Browser = config.BrowserConfig{
		DefaultProfile: "work",
		Profiles: []config.BrowserProfileConfig{{
			Name:     "work",
			Enabled:  true,
			Mode:     "managed",
			Headless: true,
		}},
	}
	servers, err := MCPServers(cfg)
	if err != nil {
		t.Fatalf("MCPServers: %v", err)
	}
	if len(servers) != 1 {
		t.Fatalf("expected 1 server, got %d", len(servers))
	}
	server := servers[0]
	if !server.HideTools || server.Kind != "browser" || server.ProfileName != "work" {
		t.Fatalf("unexpected server metadata: %#v", server)
	}
	args := strings.Join(server.Args, " ")
	if !strings.Contains(args, "chrome-devtools-mcp@latest") {
		t.Fatalf("expected chrome-devtools-mcp package in args, got %q", args)
	}
	wantUserDataDir := filepath.Join(cfg.BrowserDir(), "work")
	if !strings.Contains(args, "--user-data-dir="+wantUserDataDir) {
		t.Fatalf("expected managed user-data-dir %q in args %q", wantUserDataDir, args)
	}
	if !strings.Contains(args, "--headless=true") {
		t.Fatalf("expected headless flag in args %q", args)
	}
	if server.Name != "browser_work" {
		t.Fatalf("unexpected server name %q", server.Name)
	}
}

func TestMCPServersExistingSessionProfileUsesAutoConnect(t *testing.T) {
	root := t.TempDir()
	cfg := config.Default()
	cfg.WorkspaceRoot = root
	cfg.Browser = config.BrowserConfig{
		DefaultProfile: "personal",
		Profiles: []config.BrowserProfileConfig{{
			Name:        "personal",
			Enabled:     true,
			Mode:        "existing_session",
			UserDataDir: filepath.Join(root, "chrome-personal"),
		}},
	}
	servers, err := MCPServers(cfg)
	if err != nil {
		t.Fatalf("MCPServers: %v", err)
	}
	if len(servers) != 1 {
		t.Fatalf("expected 1 server, got %d", len(servers))
	}
	args := strings.Join(servers[0].Args, " ")
	if !strings.Contains(args, "--auto-connect=true") {
		t.Fatalf("expected existing_session mode to enable auto-connect, got %q", args)
	}
	if !strings.Contains(args, "--user-data-dir="+filepath.Join(root, "chrome-personal")) {
		t.Fatalf("expected existing_session mode to keep explicit user_data_dir, got %q", args)
	}
}

func TestMCPServersBrowserURLProfileUsesRemoteEndpoint(t *testing.T) {
	root := t.TempDir()
	cfg := config.Default()
	cfg.WorkspaceRoot = root
	cfg.Browser = config.BrowserConfig{
		DefaultProfile: "remote",
		Profiles: []config.BrowserProfileConfig{{
			Name:       "remote",
			Enabled:    true,
			Mode:       "browser_url",
			BrowserURL: "http://127.0.0.1:9222",
		}},
	}
	servers, err := MCPServers(cfg)
	if err != nil {
		t.Fatalf("MCPServers: %v", err)
	}
	if len(servers) != 1 {
		t.Fatalf("expected 1 server, got %d", len(servers))
	}
	args := strings.Join(servers[0].Args, " ")
	if !strings.Contains(args, "--browser-url=http://127.0.0.1:9222") {
		t.Fatalf("expected browser_url mode to pass --browser-url, got %q", args)
	}
	if strings.Contains(args, "--user-data-dir=") {
		t.Fatalf("expected remote browser_url profile to skip user-data-dir, got %q", args)
	}
}

func TestMCPServersWSEndpointProfileIncludesHeaders(t *testing.T) {
	root := t.TempDir()
	cfg := config.Default()
	cfg.WorkspaceRoot = root
	cfg.Browser = config.BrowserConfig{
		DefaultProfile: "ws",
		Profiles: []config.BrowserProfileConfig{{
			Name:       "ws",
			Enabled:    true,
			Mode:       "ws_endpoint",
			WSEndpoint: "ws://127.0.0.1:9223/devtools/browser/abc",
			WSHeaders: map[string]string{
				"Authorization": "Bearer token",
				"X-Trace":       "enabled",
			},
		}},
	}
	servers, err := MCPServers(cfg)
	if err != nil {
		t.Fatalf("MCPServers: %v", err)
	}
	if len(servers) != 1 {
		t.Fatalf("expected 1 server, got %d", len(servers))
	}
	args := strings.Join(servers[0].Args, " ")
	if !strings.Contains(args, "--ws-endpoint=ws://127.0.0.1:9223/devtools/browser/abc") {
		t.Fatalf("expected ws_endpoint mode to pass --ws-endpoint, got %q", args)
	}
	if !strings.Contains(args, `--ws-headers={"Authorization":"Bearer token","X-Trace":"enabled"}`) &&
		!strings.Contains(args, `--ws-headers={"X-Trace":"enabled","Authorization":"Bearer token"}`) {
		t.Fatalf("expected ws headers payload in args, got %q", args)
	}
	if strings.Contains(args, "--user-data-dir=") {
		t.Fatalf("expected websocket remote profile to skip user-data-dir, got %q", args)
	}
}
