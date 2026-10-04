package mcp

import (
	"context"
	"testing"

	"github.com/ffimnsr/koios/internal/config"
)

// newSDKManager builds a Manager whose client factory creates SDK-backed
// clients for the given server config. Cleanup closes the manager before the
// fixture server (LIFO), so SSE streams are gone before httptest teardown.
func newSDKManager(t *testing.T, cfg config.MCPServerConfig) *Manager {
	t.Helper()
	mgr := NewManagerWithFactory([]config.MCPServerConfig{cfg}, func(c config.MCPServerConfig) Client {
		return NewSDKClient(c)
	})
	t.Cleanup(mgr.Close)
	return mgr
}

// startSDKManager connects the manager to its configured server and asserts
// the server reached the connected state.
func startSDKManager(t *testing.T, mgr *Manager, name string) {
	t.Helper()
	if err := mgr.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	status, ok := mgr.ServerStatusByName(name)
	if !ok || !status.Connected {
		t.Fatalf("server not connected: %#v", status)
	}
}

func TestManagerSDKAssetsCachedAfterStartup(t *testing.T) {
	fixture := newSDKHTTPServer(t)
	mgr := newSDKManager(t, fixture.clientConfig(nil, "10s"))
	startSDKManager(t, mgr, "sdk-http-fixture")

	resources, err := mgr.ListResources(context.Background(), "sdk-http-fixture")
	if err != nil {
		t.Fatalf("ListResources: %v", err)
	}
	if len(resources) != 1 || resources[0].URI != "mach1://strategy-spec/schema.json" {
		t.Fatalf("unexpected resources: %#v", resources)
	}

	templates, err := mgr.ListResourceTemplates(context.Background(), "sdk-http-fixture")
	if err != nil {
		t.Fatalf("ListResourceTemplates: %v", err)
	}
	if len(templates) != 1 || templates[0].URITemplate != "mach1://specs/{id}/schema.json" {
		t.Fatalf("unexpected templates: %#v", templates)
	}

	prompts, err := mgr.ListPrompts(context.Background(), "sdk-http-fixture")
	if err != nil {
		t.Fatalf("ListPrompts: %v", err)
	}
	if len(prompts) != 1 || prompts[0].Name != "greet" {
		t.Fatalf("unexpected prompts: %#v", prompts)
	}
}

// TestManagerSDKReadResourceCachesTTLBackedRead verifies the manager serves a
// second read from its TTL cache instead of the server.
func TestManagerSDKReadResourceCachesTTLBackedRead(t *testing.T) {
	fixture := newSDKHTTPServer(t)
	mgr := newSDKManager(t, fixture.clientConfig(nil, "10s"))
	startSDKManager(t, mgr, "sdk-http-fixture")

	const uri = "mach1://strategy-spec/schema.json"
	first, err := mgr.ReadResource(context.Background(), "sdk-http-fixture", uri)
	if err != nil {
		t.Fatalf("first ReadResource: %v", err)
	}
	if first.TTLMs != 5000 {
		t.Fatalf("TTL metadata not preserved through the manager: %#v", first)
	}
	second, err := mgr.ReadResource(context.Background(), "sdk-http-fixture", uri)
	if err != nil {
		t.Fatalf("second ReadResource: %v", err)
	}
	if len(second.Contents) != 1 || second.Contents[0].Text != "{}" {
		t.Fatalf("unexpected cached result: %#v", second)
	}
	if got := len(fixture.recordsForRPC("resources/read")); got != 1 {
		t.Fatalf("expected the second read to come from the manager cache, got %d server requests", got)
	}
}

func TestManagerSDKGetPromptResolves(t *testing.T) {
	fixture := newSDKHTTPServer(t)
	mgr := newSDKManager(t, fixture.clientConfig(nil, "10s"))
	startSDKManager(t, mgr, "sdk-http-fixture")

	result, err := mgr.GetPrompt(context.Background(), "sdk-http-fixture", "greet", nil)
	if err != nil {
		t.Fatalf("GetPrompt: %v", err)
	}
	if len(result.Messages) != 1 || result.Messages[0].Content.Text != "hello" {
		t.Fatalf("unexpected prompt result: %#v", result)
	}
}

func TestManagerSDKServerWithoutResourcesStillConnects(t *testing.T) {
	fixture := newSDKHTTPServerToolsOnly(t)
	mgr := newSDKManager(t, fixture.clientConfig(nil, "10s"))
	startSDKManager(t, mgr, "sdk-http-fixture")

	if got := len(mgr.ListTools()); got == 0 {
		t.Fatal("expected tools from a tools-only server")
	}
	resources, err := mgr.ListResources(context.Background(), "sdk-http-fixture")
	if err != nil {
		t.Fatalf("ListResources: %v", err)
	}
	if len(resources) != 0 {
		t.Fatalf("expected no resources, got %#v", resources)
	}
	templates, err := mgr.ListResourceTemplates(context.Background(), "sdk-http-fixture")
	if err != nil {
		t.Fatalf("ListResourceTemplates: %v", err)
	}
	if len(templates) != 0 {
		t.Fatalf("expected no templates, got %#v", templates)
	}
}

func TestManagerSDKServerWithoutPromptsStillConnects(t *testing.T) {
	fixture := newSDKHTTPServerToolsOnly(t)
	mgr := newSDKManager(t, fixture.clientConfig(nil, "10s"))
	startSDKManager(t, mgr, "sdk-http-fixture")

	if got := len(mgr.ListTools()); got == 0 {
		t.Fatal("expected tools from a tools-only server")
	}
	prompts, err := mgr.ListPrompts(context.Background(), "sdk-http-fixture")
	if err != nil {
		t.Fatalf("ListPrompts: %v", err)
	}
	if len(prompts) != 0 {
		t.Fatalf("expected no prompts, got %#v", prompts)
	}
}

// TestManagerSDKOptionalCapabilityErrorsTolerated verifies that a minimal
// server rejecting resource and prompt methods with -32601 method not found is
// treated as lacking the optional capabilities: the server still connects and
// its tools remain usable.
func TestManagerSDKOptionalCapabilityErrorsTolerated(t *testing.T) {
	server := newMinimalMCPServer(t)
	mgr := newSDKManager(t, configForMinimalServer(server))
	startSDKManager(t, mgr, "sdk-minimal")

	if got := len(mgr.ListTools()); got == 0 {
		t.Fatal("expected tools to be available despite missing optional capabilities")
	}
	for _, check := range []struct {
		name string
		call func() (int, error)
	}{
		{"resources", func() (int, error) {
			items, err := mgr.ListResources(context.Background(), "sdk-minimal")
			return len(items), err
		}},
		{"resource templates", func() (int, error) {
			items, err := mgr.ListResourceTemplates(context.Background(), "sdk-minimal")
			return len(items), err
		}},
		{"prompts", func() (int, error) {
			items, err := mgr.ListPrompts(context.Background(), "sdk-minimal")
			return len(items), err
		}},
	} {
		count, err := check.call()
		if err != nil {
			t.Fatalf("%s should be treated as absent, not as an error: %v", check.name, err)
		}
		if count != 0 {
			t.Fatalf("expected no %s, got %d", check.name, count)
		}
	}
}
