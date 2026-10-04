package handler

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/ffimnsr/koios/internal/agent"
	"github.com/ffimnsr/koios/internal/config"
	"github.com/ffimnsr/koios/internal/mcp"
	"github.com/ffimnsr/koios/internal/session"
)

func TestMCPServerListIncludesConfigServers(t *testing.T) {
	client := &captureMCPClient{tools: []mcp.Tool{{Name: "get_balance_by_asset", Description: "balance"}}}
	server := config.MCPServerConfig{
		Name:      "monaco",
		Transport: "stdio",
		Command:   "/app/bin/monaco-mcp",
		Env:       map[string]string{"MONACO_CLIENT_ID": "secret"},
		Timeout:   "30s",
		Enabled:   true,
	}
	mgr := mcp.NewManagerWithFactory([]config.MCPServerConfig{server}, func(config.MCPServerConfig) mcp.Client {
		return client
	})
	if err := mgr.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}

	h := NewHandler(session.New(10), noopProvider{}, HandlerOptions{
		Model:            "test-model",
		MCPManager:       mgr,
		ConfigMCPServers: []config.MCPServerConfig{server},
	})

	result, err := h.executeMCPServerList(context.Background(), "mach1:alice")
	if err != nil {
		t.Fatalf("executeMCPServerList: %v", err)
	}
	payload, ok := result.(map[string]any)
	if !ok {
		t.Fatalf("expected map payload, got %#v", result)
	}
	servers, ok := payload["servers"].([]map[string]any)
	if !ok {
		t.Fatalf("expected server entries, got %#v", payload["servers"])
	}
	if len(servers) != 1 {
		t.Fatalf("expected one config server, got %#v", servers)
	}
	entry := servers[0]
	if entry["name"] != "monaco" {
		t.Fatalf("unexpected server name: %#v", entry)
	}
	if entry["source"] != "config" {
		t.Fatalf("expected config source, got %#v", entry)
	}
	if entry["user_managed"] != false {
		t.Fatalf("expected non-user-managed config server, got %#v", entry)
	}
	if entry["visibility"] != "shared" {
		t.Fatalf("expected shared visibility, got %#v", entry)
	}
	if entry["connected"] != true || entry["tool_count"] != 1 {
		t.Fatalf("expected runtime status, got %#v", entry)
	}
	if entry["runtime_name"] != "monaco" {
		t.Fatalf("expected runtime_name, got %#v", entry)
	}
	env, ok := entry["env"].(map[string]string)
	if !ok || env["MONACO_CLIENT_ID"] != "<redacted>" {
		t.Fatalf("expected redacted env, got %#v", entry["env"])
	}
}

func TestMCPResourceContextToolsAvailableInCodingProfile(t *testing.T) {
	client := &captureMCPClient{tools: []mcp.Tool{{Name: "get_balance_by_asset", Description: "balance"}}}
	server := config.MCPServerConfig{Name: "monaco", Transport: "stdio", Command: "/app/bin/monaco-mcp", Enabled: true}
	mgr := mcp.NewManagerWithFactory([]config.MCPServerConfig{server}, func(config.MCPServerConfig) mcp.Client { return client })
	if err := mgr.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	h := NewHandler(session.New(10), noopProvider{}, HandlerOptions{Model: "test-model", MCPManager: mgr, ConfigMCPServers: []config.MCPServerConfig{server}, ToolPolicy: ToolPolicy{Profile: "coding"}})
	defs := h.ToolDefinitionsForRun("mach1:alice", "mach1:alice", "")
	seen := map[string]bool{}
	for _, def := range defs {
		seen[def.Function.Name] = true
	}
	for _, want := range []string{"mcp.server.list", "mcp.search", "mcp.tool.details", "mcp.tool.call", "mcp.resource.list", "mcp.resource.read", "mcp.prompt.list", "mcp.prompt.get"} {
		if !seen[want] {
			t.Fatalf("expected coding profile to expose %s", want)
		}
	}
	if seen["mcp.server.add"] {
		t.Fatal("expected coding profile to hide MCP server administration")
	}
}

func TestMCPProgressiveToolDiscoveryAndCall(t *testing.T) {
	client := &captureMCPClient{tools: []mcp.Tool{{Name: "quote", Description: "quote asset", InputSchema: json.RawMessage(`{"type":"object"}`)}}}
	server := config.MCPServerConfig{Name: "monaco", Transport: "stdio", Command: "/app/bin/monaco-mcp", Enabled: true}
	mgr := mcp.NewManagerWithFactory([]config.MCPServerConfig{server}, func(config.MCPServerConfig) mcp.Client { return client })
	if err := mgr.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	h := NewHandler(session.New(10), noopProvider{}, HandlerOptions{Model: "test-model", MCPManager: mgr, ConfigMCPServers: []config.MCPServerConfig{server}})
	search, err := h.executeMCPSearch(context.Background(), "mach1:alice", agent.ToolCall{Name: "mcp.search", Arguments: []byte(`{"query":"quote"}`)})
	if err != nil {
		t.Fatalf("executeMCPSearch: %v", err)
	}
	if search.(map[string]any)["count"].(int) == 0 {
		t.Fatalf("expected search result: %#v", search)
	}
	fullName := mcp.ToolName("monaco", "quote")
	if _, err := h.executeMCPToolDetails(context.Background(), "mach1:alice", agent.ToolCall{Name: "mcp.tool.details", Arguments: []byte(`{"name":"` + fullName + `"}`)}); err != nil {
		t.Fatalf("executeMCPToolDetails: %v", err)
	}
	called, err := h.executeMCPToolCall(context.Background(), "mach1:alice", agent.ToolCall{Name: "mcp.tool.call", Arguments: []byte(`{"name":"` + fullName + `","arguments":{"asset":"BTC"}}`)})
	if err != nil {
		t.Fatalf("executeMCPToolCall: %v", err)
	}
	if called.(map[string]any)["tool"] != fullName || client.lastName != "quote" {
		t.Fatalf("unexpected call result=%#v last=%s", called, client.lastName)
	}
}

func TestMCPResourceReadUsesRuntimeServerName(t *testing.T) {
	client := &captureMCPClient{
		tools:      []mcp.Tool{{Name: "get_balance_by_asset", Description: "balance"}},
		resources:  []mcp.Resource{{URI: "mach1://strategy-spec/schema.json", Name: "schema"}},
		readResult: &mcp.ResourceReadResult{Contents: []mcp.ResourceContent{{URI: "mach1://strategy-spec/schema.json", Text: "{}", MimeType: "application/json"}}},
	}
	server := config.MCPServerConfig{Name: "monaco", Transport: "stdio", Command: "/app/bin/monaco-mcp", Enabled: true}
	mgr := mcp.NewManagerWithFactory([]config.MCPServerConfig{server}, func(config.MCPServerConfig) mcp.Client { return client })
	if err := mgr.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	h := NewHandler(session.New(10), noopProvider{}, HandlerOptions{Model: "test-model", MCPManager: mgr, ConfigMCPServers: []config.MCPServerConfig{server}})
	result, err := h.executeMCPResourceRead(context.Background(), "mach1:alice", agent.ToolCall{Name: "mcp.resource.read", Arguments: []byte(`{"server":"monaco","uri":"mach1://strategy-spec/schema.json"}`)})
	if err != nil {
		t.Fatalf("executeMCPResourceRead: %v", err)
	}
	payload, ok := result.(map[string]any)
	if !ok {
		t.Fatalf("expected map payload, got %#v", result)
	}
	if payload["server"] != "monaco" || payload["uri"] != "mach1://strategy-spec/schema.json" {
		t.Fatalf("unexpected payload: %#v", payload)
	}
}

// TestMCPToolCallPassesInputResponsesAndRequestState verifies mcp.call
// forwards input_responses and request_state into
// Manager.CallToolResultWithInput unchanged.
func TestMCPToolCallPassesInputResponsesAndRequestState(t *testing.T) {
	client := &captureMCPClient{tools: []mcp.Tool{{Name: "approve_transfer", Description: "approve a transfer"}}}
	server := config.MCPServerConfig{Name: "monaco", Transport: "stdio", Command: "/app/bin/monaco-mcp", Enabled: true}
	mgr := mcp.NewManagerWithFactory([]config.MCPServerConfig{server}, func(config.MCPServerConfig) mcp.Client { return client })
	if err := mgr.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	h := NewHandler(session.New(10), noopProvider{}, HandlerOptions{Model: "test-model", MCPManager: mgr, ConfigMCPServers: []config.MCPServerConfig{server}})
	fullName := mcp.ToolName("monaco", "approve_transfer")

	result, err := h.executeMCPToolCall(context.Background(), "mach1:alice", agent.ToolCall{Name: "mcp.tool.call", Arguments: []byte(`{
		"name": "` + fullName + `",
		"arguments": {"amount": 100},
		"input_responses": {"q1":{"action":"accept"}},
		"request_state": "rs-1"
	}`)})
	if err != nil {
		t.Fatalf("executeMCPToolCall: %v", err)
	}
	if client.lastName != "approve_transfer" {
		t.Fatalf("unexpected tool called: %q", client.lastName)
	}
	if string(client.lastInputResponses) != `{"q1":{"action":"accept"}}` {
		t.Fatalf("input_responses not forwarded: %s", client.lastInputResponses)
	}
	if string(client.lastRequestState) != `"rs-1"` {
		t.Fatalf("request_state not forwarded: %s", client.lastRequestState)
	}
	if result.(map[string]any)["ok"] != true {
		t.Fatalf("unexpected result: %#v", result)
	}
}

// TestMCPToolCallSurfacesInputRequiredMetadata verifies that an
// input-required result returned by the manager is visible in the mcp.call
// handler response with its resultType, requestState, and elicitation fields.
func TestMCPToolCallSurfacesInputRequiredMetadata(t *testing.T) {
	client := &captureMCPClient{
		tools: []mcp.Tool{{Name: "approve_transfer", Description: "approve a transfer"}},
		toolResult: &mcp.ToolResult{
			ResultType:   "input_required",
			RequestState: json.RawMessage(`"rs-1"`),
			Elicitation:  json.RawMessage(`{"q1":{"method":"elicitation/create","params":{"mode":"form","message":"Approve?"}}}`),
		},
	}
	server := config.MCPServerConfig{Name: "monaco", Transport: "stdio", Command: "/app/bin/monaco-mcp", Enabled: true}
	mgr := mcp.NewManagerWithFactory([]config.MCPServerConfig{server}, func(config.MCPServerConfig) mcp.Client { return client })
	if err := mgr.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	h := NewHandler(session.New(10), noopProvider{}, HandlerOptions{Model: "test-model", MCPManager: mgr, ConfigMCPServers: []config.MCPServerConfig{server}})
	fullName := mcp.ToolName("monaco", "approve_transfer")

	result, err := h.executeMCPToolCall(context.Background(), "mach1:alice", agent.ToolCall{Name: "mcp.tool.call", Arguments: []byte(`{"name":"` + fullName + `","arguments":{"amount":100}}`)})
	if err != nil {
		t.Fatalf("executeMCPToolCall: %v", err)
	}
	payload := result.(map[string]any)
	raw, err := json.Marshal(payload["result"])
	if err != nil {
		t.Fatalf("marshal handler result: %v", err)
	}
	var toolResult struct {
		ResultType   string          `json:"resultType"`
		RequestState json.RawMessage `json:"requestState"`
		Elicitation  json.RawMessage `json:"elicitation"`
	}
	if err := json.Unmarshal(raw, &toolResult); err != nil {
		t.Fatalf("decode handler result: %v", err)
	}
	if toolResult.ResultType != "input_required" {
		t.Fatalf("resultType not visible: %#v", toolResult)
	}
	if string(toolResult.RequestState) != `"rs-1"` {
		t.Fatalf("requestState not visible: %s", toolResult.RequestState)
	}
	if !json.Valid(toolResult.Elicitation) || !strings.Contains(string(toolResult.Elicitation), "elicitation/create") {
		t.Fatalf("elicitation not visible: %s", toolResult.Elicitation)
	}
}
