package mcp

import (
	"context"
	"strings"
	"testing"

	"github.com/ffimnsr/koios/internal/config"
)

// startDefaultManager builds a Manager with the default client factory
// (NewManager -> newClient -> NewSDKClient), starts it, and cleans it up.
func startDefaultManager(t *testing.T, cfgs []config.MCPServerConfig) *Manager {
	t.Helper()
	mgr := NewManager(cfgs)
	t.Cleanup(mgr.Close)
	if err := mgr.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	return mgr
}

// enabledSDKFixtureConfig returns the stdio fixture config with the Enabled
// flag set so the manager registers the server.
func enabledSDKFixtureConfig(t *testing.T) config.MCPServerConfig {
	t.Helper()
	cfg := sdkFixtureConfig(t)
	cfg.Enabled = true
	return cfg
}

// TestManagerDefaultFactoryConnectsStdioServer verifies a static stdio server
// configured at startup connects through the default SDK-backed factory.
func TestManagerDefaultFactoryConnectsStdioServer(t *testing.T) {
	mgr := startDefaultManager(t, []config.MCPServerConfig{enabledSDKFixtureConfig(t)})

	status, ok := mgr.ServerStatusByName("sdk-fixture")
	if !ok || !status.Connected {
		t.Fatalf("expected the stdio server to be connected, got %#v ok=%v", status, ok)
	}
	if status.ToolCount == 0 {
		t.Fatal("expected tools from the stdio fixture")
	}
	detail, ok := mgr.ToolDetails(ToolName("sdk-fixture", "echo"))
	if !ok || detail.ToolName != "echo" {
		t.Fatalf("expected the prefixed echo tool, got %#v ok=%v", detail, ok)
	}
}

// TestManagerDefaultFactoryConnectsHTTPServer verifies a static http server
// configured at startup connects through the default SDK-backed factory.
func TestManagerDefaultFactoryConnectsHTTPServer(t *testing.T) {
	fixture := newSDKHTTPServer(t)
	mgr := startDefaultManager(t, []config.MCPServerConfig{fixture.clientConfig(nil, "10s")})

	status, ok := mgr.ServerStatusByName("sdk-http-fixture")
	if !ok || !status.Connected {
		t.Fatalf("expected the http server to be connected, got %#v ok=%v", status, ok)
	}
	if status.ToolCount == 0 || status.ResourceCount != 1 || status.PromptCount != 1 {
		t.Fatalf("unexpected asset counts: %#v", status)
	}
}

// TestManagerDefaultFactoryAddServerConnectsUserManagedServer verifies the
// user-managed runtime path (Manager.AddServer) connects through the default
// SDK-backed factory.
func TestManagerDefaultFactoryAddServerConnectsUserManagedServer(t *testing.T) {
	fixture := newSDKHTTPServer(t)
	mgr := NewManager(nil)
	t.Cleanup(mgr.Close)

	status, err := mgr.AddServer(context.Background(), fixture.clientConfig(nil, "10s"))
	if err != nil {
		t.Fatalf("AddServer: %v", err)
	}
	if !status.Connected || status.ToolCount == 0 {
		t.Fatalf("expected the added server to connect, got %#v", status)
	}
	if !mgr.HasServer("sdk-http-fixture") {
		t.Fatal("expected the added server to be registered")
	}
}

// TestManagerDefaultFactorySkipsDisabledServers verifies disabled servers are
// not registered by the default construction path.
func TestManagerDefaultFactorySkipsDisabledServers(t *testing.T) {
	cfg := config.MCPServerConfig{Name: "offline", Transport: "http", URL: "http://127.0.0.1:1", Enabled: false}
	mgr := NewManager([]config.MCPServerConfig{cfg})
	t.Cleanup(mgr.Close)

	if mgr.HasServer("offline") {
		t.Fatal("disabled servers must not be registered")
	}
	if got := len(mgr.ServerStatuses()); got != 0 {
		t.Fatalf("expected no statuses for disabled servers, got %d", got)
	}
}

// TestManagerDefaultFactoryFailedServerDoesNotBlockOthers verifies a server
// that fails to connect records LastError without preventing other servers
// from connecting.
func TestManagerDefaultFactoryFailedServerDoesNotBlockOthers(t *testing.T) {
	bad := config.MCPServerConfig{Name: "downstream", Transport: "http", URL: "http://127.0.0.1:1", Enabled: true, Timeout: "3s"}
	mgr := startDefaultManager(t, []config.MCPServerConfig{bad, enabledSDKFixtureConfig(t)})

	down, ok := mgr.ServerStatusByName("downstream")
	if !ok || down.Connected {
		t.Fatalf("expected the failing server to stay disconnected, got %#v ok=%v", down, ok)
	}
	if down.LastError == "" {
		t.Fatal("expected LastError to record the connect failure")
	}

	good, ok := mgr.ServerStatusByName("sdk-fixture")
	if !ok || !good.Connected {
		t.Fatalf("expected the healthy server to connect despite the failure, got %#v ok=%v", good, ok)
	}
}

// TestManagerDefaultRuntimeToolNamesBackwardCompatible verifies runtime tool
// names keep the mcp__<server>__<tool> shape for SDK-connected servers and
// that calls through the prefixed name work.
func TestManagerDefaultRuntimeToolNamesBackwardCompatible(t *testing.T) {
	fixture := newSDKHTTPServer(t)
	mgr := startDefaultManager(t, []config.MCPServerConfig{fixture.clientConfig(nil, "10s")})

	if got := ToolPrefix("sdk-http-fixture"); got != "mcp__sdk-http-fixture__" {
		t.Fatalf("unexpected ToolPrefix: %q", got)
	}
	fullName := ToolName("sdk-http-fixture", "echo")
	if fullName != "mcp__sdk-http-fixture__echo" {
		t.Fatalf("unexpected ToolName: %q", fullName)
	}
	namespace, tool, ok := ParseToolName(fullName)
	if !ok || namespace != "sdk-http-fixture" || tool != "echo" {
		t.Fatalf("ParseToolName failed: %q %q %v", namespace, tool, ok)
	}
	if got := PluginToolPrefix("Demo.Filesystem v1"); got != "mcp_plug_demo_filesystem_v1__" {
		t.Fatalf("unexpected PluginToolPrefix: %q", got)
	}

	result, err := mgr.CallTool(context.Background(), fullName, []byte(`{"text":"hello default"}`))
	if err != nil {
		t.Fatalf("CallTool via prefixed name: %v", err)
	}
	if result != "hello default" {
		t.Fatalf("unexpected call result: %q", result)
	}
	if _, ok := mgr.ToolDetails(strings.ToUpper(fullName)); ok {
		t.Fatal("expected exact match tool lookup")
	}
}

// TestManagerDefaultFactoryConnectsMultipleServersConcurrently verifies
// Manager.Start connects several SDK-backed servers of both transports.
func TestManagerDefaultFactoryConnectsMultipleServersConcurrently(t *testing.T) {
	httpA := newSDKHTTPServer(t)
	httpB := newSDKHTTPServer(t)
	cfgs := []config.MCPServerConfig{
		enabledSDKFixtureConfig(t),
		{Name: "alpha", Transport: "http", URL: httpA.srv.URL, Timeout: "10s", Enabled: true},
		{Name: "beta", Transport: "http", URL: httpB.srv.URL, Timeout: "10s", Enabled: true},
	}
	mgr := startDefaultManager(t, cfgs)

	for _, name := range []string{"sdk-fixture", "alpha", "beta"} {
		status, ok := mgr.ServerStatusByName(name)
		if !ok || !status.Connected || status.ToolCount == 0 {
			t.Fatalf("expected %s to be connected with tools, got %#v ok=%v", name, status, ok)
		}
	}
}

// TestManagerEnsureServerReconnectsAfterStop verifies EnsureServer brings an
// initially disconnected configured server back online through the SDK path.
func TestManagerEnsureServerReconnectsAfterStop(t *testing.T) {
	mgr := startDefaultManager(t, []config.MCPServerConfig{enabledSDKFixtureConfig(t)})

	if _, err := mgr.StopServer("", ""); err != nil {
		t.Fatalf("StopServer: %v", err)
	}
	status, ok := mgr.ServerStatusByName("sdk-fixture")
	if !ok || status.Connected {
		t.Fatalf("expected the server to be disconnected, got %#v ok=%v", status, ok)
	}

	status, err := mgr.EnsureServer(context.Background(), "", "")
	if err != nil {
		t.Fatalf("EnsureServer: %v", err)
	}
	if !status.Connected || status.ToolCount == 0 {
		t.Fatalf("expected the server to reconnect, got %#v", status)
	}
}

// TestManagerStopServerResetsStateAndClearsCaches verifies StopServer drops
// connection state, caches, and the listener for an SDK-connected server.
func TestManagerStopServerResetsStateAndClearsCaches(t *testing.T) {
	fixture := newSDKHTTPServer(t)
	mgr := startDefaultManager(t, []config.MCPServerConfig{fixture.clientConfig(nil, "10s")})

	if _, ok := mgr.ToolDetails(ToolName("sdk-http-fixture", "echo")); !ok {
		t.Fatal("expected cached tools before StopServer")
	}
	if _, err := mgr.StopServer("", ""); err != nil {
		t.Fatalf("StopServer: %v", err)
	}
	status, ok := mgr.ServerStatusByName("sdk-http-fixture")
	if !ok {
		t.Fatal("expected the stopped server entry to remain")
	}
	if status.Connected || status.ToolCount != 0 || status.CacheFresh || status.SubscriptionOn {
		t.Fatalf("expected fully reset state, got %#v", status)
	}
	if _, ok := mgr.ToolDetails(ToolName("sdk-http-fixture", "echo")); ok {
		t.Fatal("expected tool cache to be cleared")
	}
}

// TestManagerUpdateServerReconnectsWithNewConfig verifies UpdateServer
// replaces the server configuration and reconnects the SDK client when
// enabled, pointing subsequent calls at the new endpoint.
func TestManagerUpdateServerReconnectsWithNewConfig(t *testing.T) {
	fixtureA := newSDKHTTPServer(t)
	fixtureB := newSDKHTTPServer(t)
	mgr := startDefaultManager(t, []config.MCPServerConfig{fixtureA.clientConfig(nil, "10s")})

	status, err := mgr.UpdateServer(context.Background(), fixtureB.clientConfig(nil, "10s"))
	if err != nil {
		t.Fatalf("UpdateServer: %v", err)
	}
	if !status.Connected || status.ToolCount == 0 {
		t.Fatalf("expected the updated server to reconnect, got %#v", status)
	}

	// The updated endpoint must now serve the next tool call.
	result, err := mgr.CallTool(context.Background(), ToolName("sdk-http-fixture", "echo"), []byte(`{"text":"after update"}`))
	if err != nil {
		t.Fatalf("CallTool after update: %v", err)
	}
	if result != "after update" {
		t.Fatalf("unexpected call result: %q", result)
	}
	if got := len(fixtureB.recordsForRPC("tools/call")); got != 1 {
		t.Fatalf("expected the updated endpoint to serve the call, got %d", got)
	}
	if got := len(fixtureA.recordsForRPC("tools/call")); got != 0 {
		t.Fatalf("expected the old endpoint to be disconnected, got %d calls", got)
	}
}

// TestManagerRemoveServerClosesClient verifies RemoveServer releases the
// SDK client connection resources.
func TestManagerRemoveServerClosesClient(t *testing.T) {
	var created []*fakeManagerClient
	mgr := NewManagerWithFactory([]config.MCPServerConfig{{Name: "monaco", Enabled: true, Transport: "stdio", Command: "ignored"}}, func(config.MCPServerConfig) Client {
		c := &fakeManagerClient{tools: []Tool{{Name: "quote"}}}
		created = append(created, c)
		return c
	})
	t.Cleanup(mgr.Close)
	if err := mgr.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := mgr.RemoveServer("monaco"); err != nil {
		t.Fatalf("RemoveServer: %v", err)
	}
	if mgr.HasServer("monaco") {
		t.Fatal("expected the runtime entry to be removed")
	}
	if len(created) != 1 || !created[0].closed {
		t.Fatalf("expected the connected client to be closed, got %#v", created)
	}
}
