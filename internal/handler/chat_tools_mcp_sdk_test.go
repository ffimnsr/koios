package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/ffimnsr/koios/internal/agent"
	"github.com/ffimnsr/koios/internal/config"
	"github.com/ffimnsr/koios/internal/mcp"
	"github.com/ffimnsr/koios/internal/mcpregistry"
	"github.com/ffimnsr/koios/internal/session"
	"github.com/ffimnsr/koios/internal/toolresults"
	"github.com/ffimnsr/koios/internal/types"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

// newSDKFixtureMCPHTTPServer builds an in-process MCP server with the official
// SDK so handler tests can exercise the SDK-backed default client path end to
// end. It serves one tool, one resource, one resource template, and one
// prompt.
func newSDKFixtureMCPHTTPServer(t *testing.T) *httptest.Server {
	t.Helper()
	server := sdkmcp.NewServer(&sdkmcp.Implementation{Name: "h-sdk-fixture", Version: "1.0.0"}, nil)
	sdkmcp.AddTool(server, &sdkmcp.Tool{
		Name:        "echo",
		Title:       "Echo",
		Description: "echoes text back",
		InputSchema: map[string]any{
			"type":       "object",
			"properties": map[string]any{"text": map[string]any{"type": "string"}},
		},
	}, func(_ context.Context, _ *sdkmcp.CallToolRequest, args struct {
		Text string `json:"text"`
	}) (*sdkmcp.CallToolResult, any, error) {
		return &sdkmcp.CallToolResult{Content: []sdkmcp.Content{&sdkmcp.TextContent{Text: args.Text}}}, nil, nil
	})
	server.AddResource(&sdkmcp.Resource{
		URI:      "mach1://strategy-spec/schema.json",
		Name:     "strategy-spec",
		MIMEType: "application/json",
	}, func(_ context.Context, _ *sdkmcp.ReadResourceRequest) (*sdkmcp.ReadResourceResult, error) {
		return &sdkmcp.ReadResourceResult{Contents: []*sdkmcp.ResourceContents{{
			URI:      "mach1://strategy-spec/schema.json",
			MIMEType: "application/json",
			Text:     "{}",
		}}}, nil
	})
	server.AddResourceTemplate(&sdkmcp.ResourceTemplate{
		Name:        "spec-template",
		URITemplate: "mach1://specs/{id}/schema.json",
		MIMEType:    "application/json",
	}, func(_ context.Context, _ *sdkmcp.ReadResourceRequest) (*sdkmcp.ReadResourceResult, error) {
		return &sdkmcp.ReadResourceResult{Contents: []*sdkmcp.ResourceContents{{Text: "{}"}}}, nil
	})
	server.AddPrompt(&sdkmcp.Prompt{
		Name:        "greet",
		Description: "greets the caller",
	}, func(_ context.Context, _ *sdkmcp.GetPromptRequest) (*sdkmcp.GetPromptResult, error) {
		return &sdkmcp.GetPromptResult{Messages: []*sdkmcp.PromptMessage{{
			Role:    sdkmcp.Role("user"),
			Content: &sdkmcp.TextContent{Text: "hello"},
		}}}, nil
	})
	handler := sdkmcp.NewStreamableHTTPHandler(func(*http.Request) *sdkmcp.Server { return server }, nil)
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return srv
}

// sdkFixtureConfig returns the operator-config entry for the SDK fixture.
func sdkFixtureConfig(serverURL, name string) config.MCPServerConfig {
	return config.MCPServerConfig{
		Name:      name,
		Transport: "http",
		URL:       serverURL,
		Timeout:   "10s",
		Enabled:   true,
	}
}

// startSDKDefaultManager connects a default-factory manager to the given
// config entries, mirroring how the gateway merges static, extension, and
// user-managed server configs before Manager.Start.
func startSDKDefaultManager(t *testing.T, cfgs []config.MCPServerConfig) *mcp.Manager {
	t.Helper()
	mgr := mcp.NewManager(cfgs)
	t.Cleanup(mgr.Close)
	if err := mgr.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	return mgr
}

func TestMCPServerListIncludesSDKConnectedStatus(t *testing.T) {
	srv := newSDKFixtureMCPHTTPServer(t)
	cfg := sdkFixtureConfig(srv.URL, "monaco")
	mgr := startSDKDefaultManager(t, []config.MCPServerConfig{cfg})
	h := NewHandler(session.New(10), noopProvider{}, HandlerOptions{
		Model:            "test-model",
		MCPManager:       mgr,
		ConfigMCPServers: []config.MCPServerConfig{cfg},
	})

	result, err := h.executeMCPServerList(context.Background(), "mach1:alice")
	if err != nil {
		t.Fatalf("executeMCPServerList: %v", err)
	}
	entries := result.(map[string]any)["servers"].([]map[string]any)
	if len(entries) != 1 {
		t.Fatalf("expected one server entry, got %#v", entries)
	}
	entry := entries[0]
	if entry["name"] != "monaco" || entry["user_managed"] != false {
		t.Fatalf("unexpected entry: %#v", entry)
	}
	if entry["connected"] != true || entry["tool_count"] != 1 || entry["resource_count"] != 1 || entry["prompt_count"] != 1 {
		t.Fatalf("expected SDK-connected runtime status, got %#v", entry)
	}
}

func TestMCPServerTestSucceedsAgainstSDKBackedServer(t *testing.T) {
	srv := newSDKFixtureMCPHTTPServer(t)
	store, err := mcpregistry.New(filepath.Join(t.TempDir(), "mcp.db"))
	if err != nil {
		t.Fatalf("mcpregistry.New: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	rec, err := store.Create(context.Background(), mcpregistry.Input{
		OwnerPeerID: "mach1:alice",
		Name:        "monaco",
		Transport:   "http",
		URL:         srv.URL,
		Timeout:     "10s",
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	h := NewHandler(session.New(10), noopProvider{}, HandlerOptions{Model: "test-model", MCPRegistry: store})

	payload, err := h.executeMCPServerTest(context.Background(), "mach1:alice", agent.ToolCall{
		Name:      "mcp.server.test",
		Arguments: []byte(`{"id":"` + rec.ID + `"}`),
	})
	if err != nil {
		t.Fatalf("executeMCPServerTest: %v", err)
	}
	result := payload.(map[string]any)
	if result["success"] != true {
		t.Fatalf("expected probe success, got %#v", result)
	}
	if result["tool_count"] != 1 {
		t.Fatalf("expected one probed tool, got %#v", result)
	}
}

func TestMCPServerEnableAddsUserManagedServerToRuntime(t *testing.T) {
	srv := newSDKFixtureMCPHTTPServer(t)
	store, err := mcpregistry.New(filepath.Join(t.TempDir(), "mcp.db"))
	if err != nil {
		t.Fatalf("mcpregistry.New: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	rec, err := store.Create(context.Background(), mcpregistry.Input{
		OwnerPeerID: "mach1:alice",
		Name:        "monaco",
		Transport:   "http",
		URL:         srv.URL,
		Timeout:     "10s",
		Enabled:     false,
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	mgr := mcp.NewManager(nil)
	t.Cleanup(mgr.Close)
	h := NewHandler(session.New(10), noopProvider{}, HandlerOptions{Model: "test-model", MCPManager: mgr, MCPRegistry: store})

	runtimeName := mcpregistry.RuntimeName(rec.OwnerPeerID, rec.ID)
	if mgr.HasServer(runtimeName) {
		t.Fatal("expected the disabled server to be absent from the runtime")
	}

	payload, err := h.executeMCPServerEnable(context.Background(), "mach1:alice", agent.ToolCall{
		Name:      "mcp.server.enable",
		Arguments: []byte(`{"id":"` + rec.ID + `"}`),
	})
	if err != nil {
		t.Fatalf("executeMCPServerEnable: %v", err)
	}
	if payload.(map[string]any)["enabled"] != true {
		t.Fatalf("unexpected enable payload: %#v", payload)
	}
	if !mgr.HasServer(runtimeName) {
		t.Fatal("expected the enabled server to be added to the runtime manager")
	}
	status, ok := mgr.ServerStatusByName(runtimeName)
	if !ok || !status.Connected || status.ToolCount != 1 {
		t.Fatalf("expected the SDK-enabled server to connect, got %#v ok=%v", status, ok)
	}
}

func TestMCPServerDisableRemovesUserManagedServerFromRuntime(t *testing.T) {
	srv := newSDKFixtureMCPHTTPServer(t)
	store, err := mcpregistry.New(filepath.Join(t.TempDir(), "mcp.db"))
	if err != nil {
		t.Fatalf("mcpregistry.New: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	rec, err := store.Create(context.Background(), mcpregistry.Input{
		OwnerPeerID: "mach1:alice",
		Name:        "monaco",
		Transport:   "http",
		URL:         srv.URL,
		Timeout:     "10s",
		Enabled:     true,
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	mgr := mcp.NewManager(nil)
	t.Cleanup(mgr.Close)
	runtimeName := mcpregistry.RuntimeName(rec.OwnerPeerID, rec.ID)
	if _, err := mgr.AddServer(context.Background(), rec.ToMCPServerConfig()); err != nil {
		t.Fatalf("AddServer: %v", err)
	}
	if !mgr.HasServer(runtimeName) {
		t.Fatal("expected the enabled server in the runtime before disable")
	}

	h := NewHandler(session.New(10), noopProvider{}, HandlerOptions{Model: "test-model", MCPManager: mgr, MCPRegistry: store})
	payload, err := h.executeMCPServerDisable(context.Background(), "mach1:alice", agent.ToolCall{
		Name:      "mcp.server.disable",
		Arguments: []byte(`{"id":"` + rec.ID + `"}`),
	})
	if err != nil {
		t.Fatalf("executeMCPServerDisable: %v", err)
	}
	if payload.(map[string]any)["enabled"] != false {
		t.Fatalf("unexpected disable payload: %#v", payload)
	}
	if mgr.HasServer(runtimeName) {
		t.Fatal("expected the disabled server to be removed from the runtime manager")
	}
}

func TestMCPSearchReturnsSDKDiscoveredAssets(t *testing.T) {
	srv := newSDKFixtureMCPHTTPServer(t)
	cfg := sdkFixtureConfig(srv.URL, "monaco")
	mgr := startSDKDefaultManager(t, []config.MCPServerConfig{cfg})
	h := NewHandler(session.New(10), noopProvider{}, HandlerOptions{Model: "test-model", MCPManager: mgr})

	payload, err := h.executeMCPSearch(context.Background(), "mach1:alice", agent.ToolCall{
		Name:      "mcp.search",
		Arguments: []byte(`{"query":"","limit":20}`),
	})
	if err != nil {
		t.Fatalf("executeMCPSearch: %v", err)
	}
	result := payload.(map[string]any)
	matches := result["matches"].([]map[string]any)
	seen := map[string]bool{}
	for _, m := range matches {
		if m["server"] == "monaco" {
			seen[m["type"].(string)] = true
		}
	}
	for _, want := range []string{"tool", "resource", "resource_template", "prompt"} {
		if !seen[want] {
			t.Fatalf("expected an SDK-discovered %s in search results, got %#v", want, matches)
		}
	}
	// The tool must be exposed under its prefixed runtime name.
	for _, m := range matches {
		if m["type"] == "tool" && m["name"] == "echo" {
			if m["full_name"] != "mcp__monaco__echo" {
				t.Fatalf("expected the prefixed runtime tool name, got %#v", m)
			}
			return
		}
	}
	t.Fatalf("echo tool missing from %#v", matches)
}

func TestMCPCallInvokesSDKDiscoveredTool(t *testing.T) {
	srv := newSDKFixtureMCPHTTPServer(t)
	cfg := sdkFixtureConfig(srv.URL, "monaco")
	mgr := startSDKDefaultManager(t, []config.MCPServerConfig{cfg})
	h := NewHandler(session.New(10), noopProvider{}, HandlerOptions{
		Model:            "test-model",
		MCPManager:       mgr,
		ConfigMCPServers: []config.MCPServerConfig{cfg},
	})

	fullName := mcp.ToolName("monaco", "echo")
	payload, err := h.executeMCPToolCall(context.Background(), "mach1:alice", agent.ToolCall{
		Name:      "mcp.tool.call",
		Arguments: []byte(`{"name":"` + fullName + `","arguments":{"text":"hello sdk"}}`),
	})
	if err != nil {
		t.Fatalf("executeMCPToolCall: %v", err)
	}
	result := payload.(map[string]any)
	if result["tool"] != fullName || result["ok"] != true {
		t.Fatalf("unexpected call payload: %#v", result)
	}
	raw, err := json.Marshal(result["result"])
	if err != nil {
		t.Fatalf("marshal tool result: %v", err)
	}
	var toolResult struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := json.Unmarshal(raw, &toolResult); err != nil {
		t.Fatalf("decode tool result: %v", err)
	}
	if len(toolResult.Content) != 1 || toolResult.Content[0].Text != "hello sdk" {
		t.Fatalf("unexpected tool result content: %#v", toolResult.Content)
	}
}

// TestMCPSDKToolsAppearInToolDefinitionsForRun verifies SDK-discovered tools
// surface as agent-visible tool definitions under their prefixed runtime
// names, with user-owned servers scoped to their owning peer.
func TestMCPSDKToolsAppearInToolDefinitionsForRun(t *testing.T) {
	srv := newSDKFixtureMCPHTTPServer(t)
	cfg := sdkFixtureConfig(srv.URL, "monaco")
	mgr := startSDKDefaultManager(t, []config.MCPServerConfig{cfg})
	h := NewHandler(session.New(10), noopProvider{}, HandlerOptions{Model: "test-model", MCPManager: mgr})

	defs := h.ToolDefinitionsForRun("mach1:alice", "mach1:alice", "")
	seen := map[string]bool{}
	for _, def := range defs {
		seen[def.Function.Name] = true
	}
	if !seen["mcp__monaco__echo"] {
		t.Fatalf("expected the SDK-discovered tool in definitions, got %#v", seen)
	}

	// User-owned servers are only visible to their owning peer.
	userCfg := sdkFixtureConfig(srv.URL, "monaco_user")
	userCfg.Kind = "user"
	userCfg.ProfileName = "mach1:alice"
	userMgr := startSDKDefaultManager(t, []config.MCPServerConfig{userCfg})
	userH := NewHandler(session.New(10), noopProvider{}, HandlerOptions{Model: "test-model", MCPManager: userMgr})
	ownerDefs := userH.ToolDefinitionsForRun("mach1:alice", "mach1:alice", "")
	if !defsContain(ownerDefs, "mcp__monaco_user__echo") {
		t.Fatal("expected the owning peer to see user-managed tools")
	}
	otherDefs := userH.ToolDefinitionsForRun("mach1:bob", "mach1:bob", "")
	if defsContain(otherDefs, "mcp__monaco_user__echo") {
		t.Fatal("expected user-managed tools to be hidden from other peers")
	}
}

func defsContain(defs []types.Tool, name string) bool {
	for _, def := range defs {
		if def.Function.Name == name {
			return true
		}
	}
	return false
}

// TestMCPHiddenToolsNotAgentVisibleButCallableInternally verifies hidden MCP
// tools stay out of agent-visible definitions while remaining callable by
// their full runtime name for internal callers.
func TestMCPHiddenToolsNotAgentVisibleButCallableInternally(t *testing.T) {
	srv := newSDKFixtureMCPHTTPServer(t)
	cfg := sdkFixtureConfig(srv.URL, "monaco")
	cfg.HideTools = true
	mgr := startSDKDefaultManager(t, []config.MCPServerConfig{cfg})
	h := NewHandler(session.New(10), noopProvider{}, HandlerOptions{Model: "test-model", MCPManager: mgr})

	if got := mgr.ListTools(); len(got) != 0 {
		t.Fatalf("expected hidden tools to be excluded from agent listing, got %#v", got)
	}
	if got := mgr.AllTools(); len(got) == 0 {
		t.Fatal("expected hidden tools in the full runtime listing")
	}
	if defsContain(h.ToolDefinitionsForRun("mach1:alice", "mach1:alice", ""), "mcp__monaco__echo") {
		t.Fatal("expected hidden tools to be absent from agent-visible definitions")
	}

	result, err := h.ExecuteTool(context.Background(), "mach1:alice", agent.ToolCall{
		Name:      "mcp__monaco__echo",
		Arguments: json.RawMessage(`{"text":"internal"}`),
	})
	if err != nil {
		t.Fatalf("internal call to hidden tool: %v", err)
	}
	if result != "internal" {
		t.Fatalf("unexpected hidden tool result: %#v", result)
	}
}

// TestMCPToolMutationClassificationUnchanged pins the mutation classification
// contract for MCP runtime tool names and the built-in mcp.* tools.
func TestMCPToolMutationClassificationUnchanged(t *testing.T) {
	srv := newSDKFixtureMCPHTTPServer(t)
	mgr := startSDKDefaultManager(t, []config.MCPServerConfig{sdkFixtureConfig(srv.URL, "monaco")})
	h := NewHandler(session.New(10), noopProvider{}, HandlerOptions{Model: "test-model", MCPManager: mgr})

	for name, want := range map[string]bool{
		"mcp.tool.call":              false,
		"mcp.server.list":            false,
		"mcp__monaco__echo":          false,
		"mcp__monaco__boom":          false,
		"mcp__monaco__create_widget": true,
		"mcp__monaco__delete_widget": true,
		"mcp__unknown__read":         false,
		"mcp__unknown__write":        true,
	} {
		if got := h.ToolMutatesState("mach1:alice", name); got != want {
			t.Fatalf("ToolMutatesState(%q) = %v, want %v", name, got, want)
		}
	}
}

// TestMCPToolCallRecordsRuntimeToolResult verifies direct MCP tool execution
// persists a runtime-managed tool result record tagged with the mcp executor.
func TestMCPToolCallRecordsRuntimeToolResult(t *testing.T) {
	srv := newSDKFixtureMCPHTTPServer(t)
	mgr := startSDKDefaultManager(t, []config.MCPServerConfig{sdkFixtureConfig(srv.URL, "monaco")})
	toolResultStore, err := toolresults.New(t.TempDir() + "/tool_results.db")
	if err != nil {
		t.Fatalf("toolresults.New: %v", err)
	}
	t.Cleanup(func() { _ = toolResultStore.Close() })
	h := NewHandler(session.New(10), noopProvider{}, HandlerOptions{Model: "test-model", MCPManager: mgr, ToolResultStore: toolResultStore})

	result, err := h.ExecuteTool(context.Background(), "mach1:alice", agent.ToolCall{
		Name:      "mcp__monaco__echo",
		Arguments: json.RawMessage(`{"text":"recorded"}`),
	})
	if err != nil {
		t.Fatalf("ExecuteTool: %v", err)
	}
	if result != "recorded" {
		t.Fatalf("unexpected result: %#v", result)
	}

	records, err := toolResultStore.List(context.Background(), "mach1:alice", toolresults.Filter{Limit: 10})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(records) != 1 {
		t.Fatalf("expected one recorded tool result, got %#v", records)
	}
	record := records[0]
	if record.ToolName != "mcp__monaco__echo" {
		t.Fatalf("unexpected recorded tool name: %q", record.ToolName)
	}
	if record.Provenance.ExecutorKind != "mcp" {
		t.Fatalf("expected mcp executor kind, got %q", record.Provenance.ExecutorKind)
	}
}

// TestMCPSearchFindsAssetsAcrossSurfaces verifies mcp.search resolves tools by
// name and description, resources by URI, templates by URI template, and
// prompts by name.
func TestMCPSearchFindsAssetsAcrossSurfaces(t *testing.T) {
	srv := newSDKFixtureMCPHTTPServer(t)
	mgr := startSDKDefaultManager(t, []config.MCPServerConfig{sdkFixtureConfig(srv.URL, "monaco")})
	h := NewHandler(session.New(10), noopProvider{}, HandlerOptions{Model: "test-model", MCPManager: mgr})

	for _, tt := range []struct {
		query string
		kind  string
	}{
		{"echo", "tool"},        // tool by name
		{"echoes text", "tool"}, // tool by description
		{"mach1://strategy-spec", "resource"},
		{"mach1://specs", "resource_template"},
		{"greet", "prompt"},
	} {
		t.Run(tt.query, func(t *testing.T) {
			payload, err := h.executeMCPSearch(context.Background(), "mach1:alice", agent.ToolCall{
				Name:      "mcp.search",
				Arguments: []byte(`{"query":` + strconv.Quote(tt.query) + `,"limit":20}`),
			})
			if err != nil {
				t.Fatalf("executeMCPSearch: %v", err)
			}
			found := false
			for _, m := range payload.(map[string]any)["matches"].([]map[string]any) {
				if m["server"] == "monaco" && m["type"] == tt.kind {
					found = true
				}
			}
			if !found {
				t.Fatalf("query %q did not match a %s: %#v", tt.query, tt.kind, payload.(map[string]any)["matches"])
			}
		})
	}
}

// TestMCPSearchRespectsLimit verifies the search limit argument caps matches.
func TestMCPSearchRespectsLimit(t *testing.T) {
	srv := newSDKFixtureMCPHTTPServer(t)
	mgr := startSDKDefaultManager(t, []config.MCPServerConfig{sdkFixtureConfig(srv.URL, "monaco")})
	h := NewHandler(session.New(10), noopProvider{}, HandlerOptions{Model: "test-model", MCPManager: mgr})

	payload, err := h.executeMCPSearch(context.Background(), "mach1:alice", agent.ToolCall{
		Name:      "mcp.search",
		Arguments: []byte(`{"query":"","limit":1}`),
	})
	if err != nil {
		t.Fatalf("executeMCPSearch: %v", err)
	}
	if count := payload.(map[string]any)["count"].(int); count != 1 {
		t.Fatalf("expected the limit to cap matches at 1, got %d", count)
	}
}
