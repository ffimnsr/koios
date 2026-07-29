package mcp

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ffimnsr/koios/internal/config"
)

func TestPluginToolPrefixSanitizesManifestID(t *testing.T) {
	got := PluginToolPrefix("Demo.Filesystem v1")
	if got != "mcp_plug_demo_filesystem_v1__" {
		t.Fatalf("unexpected plugin tool prefix: %q", got)
	}
}

func TestParseToolNameSupportsPluginPrefix(t *testing.T) {
	namespace, tool, ok := ParseToolName("mcp_plug_demo_filesystem__read_file")
	if !ok {
		t.Fatal("expected plugin tool name to parse")
	}
	if namespace != "demo_filesystem" || tool != "read_file" {
		t.Fatalf("unexpected parse result: namespace=%q tool=%q", namespace, tool)
	}
}

func TestListToolsSkipsHiddenServers(t *testing.T) {
	mgr := &Manager{servers: []*serverEntry{
		{name: "visible", toolPrefix: ToolPrefix("visible"), tools: []Tool{{Name: "ping", Description: "visible tool"}}},
		{name: "hooks-only", toolPrefix: PluginToolPrefix("demo.hooks"), hideTools: true, tools: []Tool{{Name: "on_event", Description: "internal hook tool"}}},
	}}
	tools := mgr.ListTools()
	if len(tools) != 1 {
		t.Fatalf("expected only visible tools, got %#v", tools)
	}
	if tools[0].FullName != "mcp__visible__ping" {
		t.Fatalf("unexpected visible tool listing: %#v", tools[0])
	}
}

func TestManagerEnsureAndStopServer(t *testing.T) {
	var factoryCalls atomic.Int32
	funcNewClient := func(cfg config.MCPServerConfig) Client {
		factoryCalls.Add(1)
		return &fakeManagerClient{tools: []Tool{{Name: "list_pages", Description: "list pages"}}, callResult: cfg.Name}
	}
	mgr := NewManagerWithFactory([]config.MCPServerConfig{{
		Name:           "browser_work",
		Enabled:        true,
		Transport:      "stdio",
		Command:        "ignored",
		ToolNamePrefix: ToolPrefix("browser_work"),
		HideTools:      true,
		Kind:           "browser",
		ProfileName:    "work",
	}}, funcNewClient)

	status, ok := mgr.ServerStatus("browser", "work")
	if !ok {
		t.Fatal("expected browser server status")
	}
	if status.Connected {
		t.Fatalf("expected disconnected server before ensure, got %#v", status)
	}

	status, err := mgr.EnsureServer(context.Background(), "browser", "work")
	if err != nil {
		t.Fatalf("EnsureServer: %v", err)
	}
	if !status.Connected || status.ToolCount != 1 {
		t.Fatalf("unexpected ensured server status: %#v", status)
	}

	tools := mgr.AllTools()
	if len(tools) != 1 || tools[0].ProfileName != "work" || tools[0].Kind != "browser" {
		t.Fatalf("unexpected tool listing after ensure: %#v", tools)
	}

	status, err = mgr.StopServer("browser", "work")
	if err != nil {
		t.Fatalf("StopServer: %v", err)
	}
	if status.Connected || status.ToolCount != 0 {
		t.Fatalf("unexpected stopped server status: %#v", status)
	}
	if got := factoryCalls.Load(); got < 2 {
		t.Fatalf("expected stop to recreate client, factory calls=%d", got)
	}

	_, err = mgr.CallTool(context.Background(), ToolName("browser_work", "list_pages"), nil)
	if err == nil || !strings.Contains(err.Error(), "not connected") {
		t.Fatalf("expected disconnected server call failure, got %v", err)
	}
}

func TestManagerAddServer(t *testing.T) {
	mgr := NewManagerWithFactory(nil, func(cfg config.MCPServerConfig) Client {
		return &fakeManagerClient{tools: []Tool{{Name: "ping"}}, callResult: "pong"}
	})
	status, err := mgr.AddServer(context.Background(), config.MCPServerConfig{Name: "u_alice_test", Kind: "user", Enabled: false})
	if err != nil {
		t.Fatalf("AddServer: %v", err)
	}
	if status.Connected {
		t.Fatal("expected disabled server to not be connected")
	}
	if !mgr.HasServer("u_alice_test") {
		t.Fatal("expected HasServer to be true after AddServer")
	}

	status, err = mgr.AddServer(context.Background(), config.MCPServerConfig{Name: "u_bob_other", Kind: "user", Enabled: true, Transport: "stdio", Command: "echo"})
	if err != nil {
		t.Fatalf("AddServer enabled: %v", err)
	}
	if !status.Connected {
		t.Fatal("expected enabled server to be connected")
	}

	_, err = mgr.AddServer(context.Background(), config.MCPServerConfig{Name: "u_alice_test", Kind: "user"})
	if err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("expected duplicate error, got %v", err)
	}

	tools := mgr.AllTools()
	if len(tools) != 1 {
		t.Fatalf("expected 1 tool from enabled server, got %d: %#v", len(tools), tools)
	}
}

func TestManagerRemoveServer(t *testing.T) {
	mgr := NewManagerWithFactory([]config.MCPServerConfig{{Name: "alice_fs", Enabled: true, Transport: "stdio", Command: "echo"}}, func(cfg config.MCPServerConfig) Client {
		return &fakeManagerClient{tools: []Tool{{Name: "read"}}, callResult: "ok"}
	})
	if !mgr.HasServer("alice_fs") {
		t.Fatal("expected server from config")
	}
	if err := mgr.RemoveServer("alice_fs"); err != nil {
		t.Fatalf("RemoveServer: %v", err)
	}
	if mgr.HasServer("alice_fs") {
		t.Fatal("expected HasServer to be false after RemoveServer")
	}
	if err := mgr.RemoveServer("nonexistent"); err == nil {
		t.Fatal("expected error for non-existent server")
	}
	if tools := mgr.AllTools(); len(tools) != 0 {
		t.Fatalf("expected 0 tools after removal, got %d", len(tools))
	}
}

func TestManagerUpdateServer(t *testing.T) {
	callCount := 0
	mgr := NewManagerWithFactory(nil, func(cfg config.MCPServerConfig) Client {
		callCount++
		return &fakeManagerClient{tools: []Tool{{Name: "tool_" + cfg.Name}}, callResult: cfg.Name}
	})
	_, err := mgr.AddServer(context.Background(), config.MCPServerConfig{Name: "u_alice_s1", Enabled: true, Transport: "stdio", Command: "echo"})
	if err != nil {
		t.Fatalf("AddServer: %v", err)
	}
	status, err := mgr.UpdateServer(context.Background(), config.MCPServerConfig{Name: "u_alice_s1", Enabled: true, Transport: "stdio", Command: "new-command"})
	if err != nil {
		t.Fatalf("UpdateServer: %v", err)
	}
	if !status.Connected {
		t.Fatal("expected connected after update")
	}
	_, err = mgr.UpdateServer(context.Background(), config.MCPServerConfig{Name: "nonexistent"})
	if err == nil {
		t.Fatal("expected error for non-existent server update")
	}
}

func TestManagerHasServer(t *testing.T) {
	mgr := NewManagerWithFactory([]config.MCPServerConfig{{Name: "existing", Enabled: true}}, nil)
	if !mgr.HasServer("existing") {
		t.Fatal("expected HasServer to be true for existing server")
	}
	if mgr.HasServer("nonexistent") {
		t.Fatal("expected HasServer to be false for non-existent server")
	}
}

func TestManagerCachesResourcesAndPrompts(t *testing.T) {
	mgr := NewManagerWithFactory([]config.MCPServerConfig{{Name: "monaco", Enabled: true, Transport: "stdio", Command: "ignored"}}, func(cfg config.MCPServerConfig) Client {
		return &fakeManagerClient{
			tools:             []Tool{{Name: "ping"}},
			resources:         []Resource{{URI: "mach1://strategy-spec/schema.json", Name: "schema"}},
			resourceTemplates: []ResourceTemplate{{URITemplate: "mach1://strategy-spec/{name}"}},
			prompts:           []Prompt{{Name: "build_strategy"}},
			resourceRead:      &ResourceReadResult{Contents: []ResourceContent{{URI: "mach1://strategy-spec/schema.json", Text: "{}", MimeType: "application/json"}}},
			promptGet:         &PromptGetResult{Messages: []PromptMessage{{Role: "user", Content: Content{Type: "text", Text: "hi"}}}},
		}
	})
	if err := mgr.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	resources, err := mgr.ListResources(context.Background(), "monaco")
	if err != nil || len(resources) != 1 || resources[0].URI != "mach1://strategy-spec/schema.json" {
		t.Fatalf("unexpected resources: %#v err=%v", resources, err)
	}
	templates, err := mgr.ListResourceTemplates(context.Background(), "monaco")
	if err != nil || len(templates) != 1 {
		t.Fatalf("unexpected resource templates: %#v err=%v", templates, err)
	}
	readResult, err := mgr.ReadResource(context.Background(), "monaco", "mach1://strategy-spec/schema.json")
	if err != nil || len(readResult.Contents) != 1 || readResult.Contents[0].Text != "{}" {
		t.Fatalf("unexpected read result: %#v err=%v", readResult, err)
	}
	prompts, err := mgr.ListPrompts(context.Background(), "monaco")
	if err != nil || len(prompts) != 1 || prompts[0].Name != "build_strategy" {
		t.Fatalf("unexpected prompts: %#v err=%v", prompts, err)
	}
	promptResult, err := mgr.GetPrompt(context.Background(), "monaco", "build_strategy", nil)
	if err != nil || len(promptResult.Messages) != 1 {
		t.Fatalf("unexpected prompt result: %#v err=%v", promptResult, err)
	}
	status, ok := mgr.ServerStatusByName("monaco")
	if !ok || status.ResourceCount != 1 || status.PromptCount != 1 {
		t.Fatalf("unexpected status: %#v ok=%v", status, ok)
	}
}

func TestCallToolResultPreservesToolErrors(t *testing.T) {
	mgr := NewManagerWithFactory([]config.MCPServerConfig{{Name: "monaco", Enabled: true, Transport: "stdio", Command: "ignored"}}, func(cfg config.MCPServerConfig) Client {
		return &fakeManagerClient{tools: []Tool{{Name: "ping"}}, toolResult: &ToolResult{IsError: true, Content: []Content{{Type: "text", Text: "bad request"}}}}
	})
	if err := mgr.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	result, err := mgr.CallToolResult(context.Background(), ToolName("monaco", "ping"), nil)
	if err != nil {
		t.Fatalf("expected MCP tool error payload, got err=%v", err)
	}
	if !result.IsError || extractText(result) != "bad request" {
		t.Fatalf("unexpected tool result: %#v", result)
	}
}

func TestManagerSearchDetailsAndResourceCacheInvalidation(t *testing.T) {
	client := &fakeManagerClient{
		tools:         []Tool{{Name: "quote", Description: "quote asset"}},
		resources:     []Resource{{URI: "mach1://strategy-spec/schema.json", Name: "schema"}},
		prompts:       []Prompt{{Name: "build_strategy"}},
		resourceRead:  &ResourceReadResult{TTLMs: 60_000, Contents: []ResourceContent{{URI: "mach1://strategy-spec/schema.json", Text: "v1"}}},
		notifications: make(chan Notification, 2),
	}
	mgr := NewManagerWithFactory([]config.MCPServerConfig{{Name: "monaco", Enabled: true, Transport: "stdio", Command: "ignored"}}, func(config.MCPServerConfig) Client {
		return client
	})
	if err := mgr.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	matches := mgr.Search("strategy", 10)
	if len(matches) == 0 {
		t.Fatal("expected strategy match")
	}
	if detail, ok := mgr.ToolDetails(ToolName("monaco", "quote")); !ok || detail.ToolName != "quote" {
		t.Fatalf("unexpected details: %#v ok=%v", detail, ok)
	}
	got, err := mgr.ReadResource(context.Background(), "monaco", "mach1://strategy-spec/schema.json")
	if err != nil || got.Contents[0].Text != "v1" {
		t.Fatalf("read cache seed: %#v err=%v", got, err)
	}
	client.resourceRead = &ResourceReadResult{TTLMs: 60_000, Contents: []ResourceContent{{URI: "mach1://strategy-spec/schema.json", Text: "v2"}}}
	got, err = mgr.ReadResource(context.Background(), "monaco", "mach1://strategy-spec/schema.json")
	if err != nil || got.Contents[0].Text != "v1" {
		t.Fatalf("expected cached v1 before invalidation, got %#v err=%v", got, err)
	}
	client.notifications <- Notification{Method: "notifications/resources/updated", Params: json.RawMessage(`{"uri":"mach1://strategy-spec/schema.json"}`)}
	time.Sleep(20 * time.Millisecond)
	got, err = mgr.ReadResource(context.Background(), "monaco", "mach1://strategy-spec/schema.json")
	if err != nil || got.Contents[0].Text != "v2" {
		t.Fatalf("expected v2 after invalidation, got %#v err=%v", got, err)
	}
}

func TestHTTPClientPaginatesAndMirrorsToolHeaderAnnotations(t *testing.T) {
	var cursors []string
	var gotParam string
	inputSchema := json.RawMessage(`{"type":"object","properties":{"tenant":{"type":"string","x-mcp-header":"Tenant"}}}`)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req rpcRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		_ = r.Body.Close()
		switch req.Method {
		case "tools/list":
			var params listParams
			_ = json.Unmarshal(req.Params, &params)
			if params.Cursor == nil {
				cursors = append(cursors, "<nil>")
				result, _ := json.Marshal(toolsListResult{Tools: []Tool{{Name: "first"}}, NextCursor: ptrString("")})
				_ = json.NewEncoder(w).Encode(rpcResponse{JSONRPC: JSONRPCVersion, ID: req.ID, Result: result})
				return
			}
			cursors = append(cursors, *params.Cursor)
			result, _ := json.Marshal(toolsListResult{Tools: []Tool{{Name: "quote", InputSchema: inputSchema}}})
			_ = json.NewEncoder(w).Encode(rpcResponse{JSONRPC: JSONRPCVersion, ID: req.ID, Result: result})
		case "tools/call":
			gotParam = r.Header.Get("Mcp-Param-Tenant")
			result, _ := json.Marshal(ToolResult{Content: []Content{{Type: "text", Text: "ok"}}})
			_ = json.NewEncoder(w).Encode(rpcResponse{JSONRPC: JSONRPCVersion, ID: req.ID, Result: result})
		default:
			t.Fatalf("unexpected method %s", req.Method)
		}
	}))
	defer srv.Close()
	client := NewHTTPClient("monaco", srv.URL, nil, 5*time.Second)
	tools, err := client.ListTools(context.Background())
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	if len(tools) != 2 || cursors[0] != "<nil>" || cursors[1] != "" {
		t.Fatalf("pagination failed tools=%#v cursors=%#v", tools, cursors)
	}
	if _, err := client.CallTool(context.Background(), "quote", map[string]any{"tenant": "alpha"}); err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if gotParam != "alpha" {
		t.Fatalf("expected mirrored header alpha, got %q", gotParam)
	}
}

func ptrString(v string) *string { return &v }

func TestHTTPClientSetsModernHeadersAndReadsResource(t *testing.T) {
	var gotAccept, gotVersion, gotMethod, gotName string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAccept = r.Header.Get("Accept")
		gotVersion = r.Header.Get("MCP-Protocol-Version")
		gotMethod = r.Header.Get("Mcp-Method")
		gotName = r.Header.Get("Mcp-Name")
		var req rpcRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		_ = r.Body.Close()
		result, _ := json.Marshal(ResourceReadResult{Contents: []ResourceContent{{URI: "mach1://strategy-spec/schema.json", Text: "{}"}}})
		_ = json.NewEncoder(w).Encode(rpcResponse{JSONRPC: JSONRPCVersion, ID: req.ID, Result: result})
	}))
	defer srv.Close()
	client := NewHTTPClient("monaco", srv.URL, nil, 5*time.Second)
	readResult, err := client.ReadResource(context.Background(), "mach1://strategy-spec/schema.json")
	if err != nil {
		t.Fatalf("ReadResource: %v", err)
	}
	if len(readResult.Contents) != 1 || readResult.Contents[0].Text != "{}" {
		t.Fatalf("unexpected read result: %#v", readResult)
	}
	if !strings.Contains(gotAccept, "text/event-stream") || !strings.Contains(gotAccept, "application/json") {
		t.Fatalf("unexpected Accept header: %q", gotAccept)
	}
	if gotVersion != ProtocolVersion2026 {
		t.Fatalf("unexpected protocol version header: %q", gotVersion)
	}
	if gotMethod != "resources/read" {
		t.Fatalf("unexpected method header: %q", gotMethod)
	}
	if gotName != "base64:bWFjaDE6Ly9zdHJhdGVneS1zcGVjL3NjaGVtYS5qc29u" {
		t.Fatalf("unexpected name header: %q", gotName)
	}
}

func TestEncodeParams_StaticValues(t *testing.T) {
	got := encodeParams(map[string]any{"key": "val"})
	var m map[string]any
	if err := json.Unmarshal(got, &m); err != nil {
		t.Fatalf("encodeParams produced invalid JSON: %v", err)
	}
	if m["key"] != "val" {
		t.Fatalf("unexpected result: %v", m)
	}
}

func TestEncodeParams_NonMarshalable_DoesNotPanic(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("encodeParams panicked: %v", r)
		}
	}()
	result := encodeParams(map[string]any{"ch": make(chan int)})
	var m map[string]any
	if err := json.Unmarshal(result, &m); err != nil {
		t.Fatalf("fallback is not valid JSON: %v", err)
	}
}

type fakeManagerClient struct {
	tools             []Tool
	resources         []Resource
	resourceTemplates []ResourceTemplate
	prompts           []Prompt
	callResult        string
	toolResult        *ToolResult
	resourceRead      *ResourceReadResult
	promptGet         *PromptGetResult
	notifications     chan Notification
	closed            bool
}

func (c *fakeManagerClient) Discover(context.Context) (*DiscoverResult, error) {
	return &DiscoverResult{ProtocolVersion: ProtocolVersion2026, ServerInfo: Implementation{Name: "fake", Version: "test"}}, nil
}
func (c *fakeManagerClient) Initialize(context.Context) error { return nil }
func (c *fakeManagerClient) ListTools(context.Context) ([]Tool, error) {
	return append([]Tool(nil), c.tools...), nil
}
func (c *fakeManagerClient) CallTool(ctx context.Context, name string, args map[string]any) (*ToolResult, error) {
	return c.CallToolWithInput(ctx, name, args, nil, nil)
}
func (c *fakeManagerClient) CallToolWithInput(context.Context, string, map[string]any, json.RawMessage, json.RawMessage) (*ToolResult, error) {
	if c.toolResult != nil {
		return c.toolResult, nil
	}
	return &ToolResult{Content: []Content{{Type: "text", Text: c.callResult}}}, nil
}
func (c *fakeManagerClient) ListResources(context.Context) ([]Resource, error) {
	return append([]Resource(nil), c.resources...), nil
}
func (c *fakeManagerClient) ListResourceTemplates(context.Context) ([]ResourceTemplate, error) {
	return append([]ResourceTemplate(nil), c.resourceTemplates...), nil
}
func (c *fakeManagerClient) ReadResource(context.Context, string) (*ResourceReadResult, error) {
	if c.resourceRead == nil {
		return &ResourceReadResult{}, nil
	}
	return c.resourceRead, nil
}
func (c *fakeManagerClient) ListPrompts(context.Context) ([]Prompt, error) {
	return append([]Prompt(nil), c.prompts...), nil
}
func (c *fakeManagerClient) GetPrompt(context.Context, string, map[string]any) (*PromptGetResult, error) {
	if c.promptGet == nil {
		return &PromptGetResult{}, nil
	}
	return c.promptGet, nil
}
func (c *fakeManagerClient) Listen(context.Context) (<-chan Notification, error) {
	if c.notifications != nil {
		return c.notifications, nil
	}
	ch := make(chan Notification)
	close(ch)
	return ch, nil
}
func (c *fakeManagerClient) Cancel(context.Context, any, string) error { return nil }
func (c *fakeManagerClient) Close() error                              { c.closed = true; return nil }
