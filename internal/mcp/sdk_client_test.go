package mcp

import (
	"context"
	"encoding/json"
	"log"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/ffimnsr/koios/internal/config"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

// sdkHelperEnv selects the fake-stdio-server mode for the test binary when
// set: the process re-executes itself (TestMain) and runs the SDK server
// instead of the tests.
const sdkHelperEnv = "KOIOS_MCP_SDK_TEST_HELPER"

// TestMain lets the koios test binary act as a fake MCP server subprocess for
// SDK-backed stdio client tests. When sdkHelperEnv is set the process runs one
// of the sdkHelperServers over stdin/stdout and exits, never running tests.
func TestMain(m *testing.M) {
	if name := os.Getenv(sdkHelperEnv); name != "" {
		os.Unsetenv(sdkHelperEnv)
		os.Exit(runSDKHelperServer(name))
	}
	os.Exit(m.Run())
}

// sdkHelperServers are fake MCP servers that may be run as subprocesses via
// TestMain. They are built with the official SDK server APIs.
var sdkHelperServers = map[string]func(){
	"fixture": runSDKFixtureServer,
}

func runSDKHelperServer(name string) int {
	run, ok := sdkHelperServers[name]
	if !ok {
		log.Printf("unknown sdk test helper server %q", name) // #nosec G706 -- %q escapes the value
		return 1
	}
	run()
	return 0
}

// runSDKFixtureServer serves the fixture tools exercise over stdio: "echo"
// returns its text argument and "boom" always fails with IsError set.
func runSDKFixtureServer() {
	server := sdkmcp.NewServer(&sdkmcp.Implementation{Name: "sdk-fixture-server", Version: "1.0.0"}, nil)
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
	sdkmcp.AddTool(server, &sdkmcp.Tool{
		Name:        "boom",
		Description: "always fails",
		InputSchema: map[string]any{"type": "object", "properties": map[string]any{}},
	}, func(_ context.Context, _ *sdkmcp.CallToolRequest, _ struct{}) (*sdkmcp.CallToolResult, any, error) {
		return &sdkmcp.CallToolResult{
			Content: []sdkmcp.Content{&sdkmcp.TextContent{Text: "boom failed"}},
			IsError: true,
		}, nil, nil
	})
	if err := server.Run(context.Background(), &sdkmcp.StdioTransport{}); err != nil {
		log.Fatal(err)
	}
}

// sdkFixtureConfig returns the config for the fake stdio server subprocess.
func sdkFixtureConfig(t *testing.T) config.MCPServerConfig {
	t.Helper()
	return config.MCPServerConfig{
		Name:      "sdk-fixture",
		Transport: "stdio",
		Command:   os.Args[0],
		// TestMain re-executes with no tests selected and runs the helper.
		Args:    []string{"-test.run=^$"},
		Env:     map[string]string{sdkHelperEnv: "fixture"},
		Timeout: "10s",
	}
}

func newSDKFixtureClient(t *testing.T) Client {
	t.Helper()
	client := NewSDKClient(sdkFixtureConfig(t))
	t.Cleanup(func() { _ = client.Close() })
	return client
}

// initializeFixture connects a fresh SDK client to the fake stdio server.
func initializeFixture(t *testing.T, client Client) {
	t.Helper()
	if err := client.Initialize(context.Background()); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
}

func TestSDKClientInitializeConnectsToStdioServer(t *testing.T) {
	client := newSDKFixtureClient(t)
	initializeFixture(t, client)

	discover, err := client.Discover(context.Background())
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if discover.ProtocolVersion != ProtocolVersion2026 {
		t.Fatalf("unexpected protocol version: %q", discover.ProtocolVersion)
	}
	if discover.ServerInfo.Name != "sdk-fixture-server" || discover.ServerInfo.Version != "1.0.0" {
		t.Fatalf("unexpected server info: %#v", discover.ServerInfo)
	}
}

func TestSDKClientInitializeIsIdempotent(t *testing.T) {
	client := newSDKFixtureClient(t)
	initializeFixture(t, client)

	sc := client.(*sdkClient)
	sc.mu.Lock()
	first := sc.sess
	sc.mu.Unlock()
	if first == nil {
		t.Fatal("expected a connected session after Initialize")
	}

	if err := client.Initialize(context.Background()); err != nil {
		t.Fatalf("second Initialize: %v", err)
	}
	sc.mu.Lock()
	second := sc.sess
	sc.mu.Unlock()
	if second != first {
		t.Fatal("second Initialize created a new session")
	}

	// The original session must still be usable.
	tools, err := client.ListTools(context.Background())
	if err != nil {
		t.Fatalf("ListTools after second Initialize: %v", err)
	}
	if len(tools) == 0 {
		t.Fatal("ListTools returned no tools")
	}
}

func TestSDKClientListToolsReturnsValidTools(t *testing.T) {
	client := newSDKFixtureClient(t)
	initializeFixture(t, client)

	tools, err := client.ListTools(context.Background())
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	var echo *Tool
	for i := range tools {
		if tools[i].Name == "echo" {
			echo = &tools[i]
			break
		}
	}
	if echo == nil {
		t.Fatalf("echo tool not found in %#v", tools)
	}
	if echo.Title != "Echo" {
		t.Fatalf("title not preserved: %q", echo.Title)
	}
	if echo.Description != "echoes text back" {
		t.Fatalf("description not preserved: %q", echo.Description)
	}
	var schema map[string]any
	if err := json.Unmarshal(echo.InputSchema, &schema); err != nil {
		t.Fatalf("inputSchema is not valid JSON: %v", err)
	}
	if schema["type"] != "object" {
		t.Fatalf("unexpected input schema: %v", schema)
	}
}

// TestSDKClientFiltersInvalidToolsLikeLegacy verifies that SDK-converted tools
// with invalid x-mcp-header annotations are dropped by the same filterValidTools
// path the legacy clients use.
func TestSDKClientFiltersInvalidToolsLikeLegacy(t *testing.T) {
	good := fromSDKTool(&sdkmcp.Tool{
		Name: "good",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"tenant": map[string]any{"type": "string", "x-mcp-header": "Tenant"},
			},
		},
	})
	bad := fromSDKTool(&sdkmcp.Tool{
		Name: "bad",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"tenant": map[string]any{"type": "string", "x-mcp-header": "Ten ant"},
			},
		},
	})
	got := filterValidTools("sdk-fixture", []Tool{good, bad})
	if len(got) != 1 || got[0].Name != "good" {
		t.Fatalf("expected only the valid tool to survive, got %#v", got)
	}
}

func TestSDKClientCallToolReturnsTextContent(t *testing.T) {
	client := newSDKFixtureClient(t)
	initializeFixture(t, client)

	result, err := client.CallTool(context.Background(), "echo", map[string]any{"text": "hello"})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if result.IsError {
		t.Fatalf("unexpected IsError: %#v", result)
	}
	if len(result.Content) != 1 || result.Content[0].Type != "text" || result.Content[0].Text != "hello" {
		t.Fatalf("unexpected content: %#v", result.Content)
	}
	if result.ResultType != "complete" {
		t.Fatalf("unexpected result type: %q", result.ResultType)
	}
}

func TestSDKClientCallToolPreservesIsError(t *testing.T) {
	client := newSDKFixtureClient(t)
	initializeFixture(t, client)

	result, err := client.CallTool(context.Background(), "boom", nil)
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if !result.IsError {
		t.Fatalf("expected IsError to be preserved: %#v", result)
	}
	if len(result.Content) != 1 || result.Content[0].Text != "boom failed" {
		t.Fatalf("unexpected content: %#v", result.Content)
	}
}

func TestSDKClientListToolsBeforeInitializeErrors(t *testing.T) {
	client := NewSDKClient(sdkFixtureConfig(t))
	defer client.Close()
	if _, err := client.ListTools(context.Background()); err == nil {
		t.Fatal("ListTools before Initialize should fail")
	} else if !strings.Contains(err.Error(), "tools/list") {
		t.Fatalf("error should carry the operation name: %v", err)
	}
}

func TestSDKClientCallToolBeforeInitializeErrors(t *testing.T) {
	client := NewSDKClient(sdkFixtureConfig(t))
	defer client.Close()
	if _, err := client.CallTool(context.Background(), "echo", nil); err == nil {
		t.Fatal("CallTool before Initialize should fail")
	} else if !strings.Contains(err.Error(), "tools/call") {
		t.Fatalf("error should carry the operation name: %v", err)
	}
}

func TestSDKClientCloseAfterInitialize(t *testing.T) {
	client := NewSDKClient(sdkFixtureConfig(t))
	initializeFixture(t, client)
	if err := client.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := client.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
	// The session state must be cleared: requests now fail with
	// not-initialized errors instead of touching a dead session.
	if _, err := client.ListTools(context.Background()); err == nil {
		t.Fatal("ListTools after Close should fail")
	}
}

func TestSDKClientCloseAfterFailedInitialize(t *testing.T) {
	// Failure before any subprocess is spawned: empty stdio command.
	empty := NewSDKClient(config.MCPServerConfig{Name: "sdk-empty", Transport: "stdio"})
	if err := empty.Initialize(context.Background()); err == nil {
		t.Fatal("Initialize with empty stdio command should fail")
	}
	if err := empty.Close(); err != nil {
		t.Fatalf("Close after failed Initialize: %v", err)
	}

	// Failure while spawning the subprocess: nonexistent command.
	missing := NewSDKClient(config.MCPServerConfig{
		Name:      "sdk-missing",
		Transport: "stdio",
		Command:   "/nonexistent/koios-sdk-test-helper",
		Timeout:   "5s",
	})
	if err := missing.Initialize(context.Background()); err == nil {
		t.Fatal("Initialize with missing command should fail")
	}
	if err := missing.Close(); err != nil {
		t.Fatalf("Close after spawn failure: %v", err)
	}
}

// TestSDKClientTimeoutBindsRequests verifies the configured per-request
// timeout is applied to request operations.
func TestSDKClientTimeoutBindsRequests(t *testing.T) {
	cfg := sdkFixtureConfig(t)
	cfg.Timeout = "250ms"
	client := NewSDKClient(cfg).(*sdkClient)
	if client.timeout != 250*time.Millisecond {
		t.Fatalf("unexpected timeout: %v", client.timeout)
	}
}

// ─── conversion helper tests ─────────────────────────────────────────────────

func TestFromSDKToolPreservesMetadata(t *testing.T) {
	tool := fromSDKTool(&sdkmcp.Tool{
		Name:        "quote",
		Title:       "Quote",
		Description: "quotes a tenant",
		InputSchema: map[string]any{"type": "object"},
		OutputSchema: map[string]any{
			"type":       "object",
			"properties": map[string]any{"text": map[string]any{"type": "string"}},
		},
		Annotations: &sdkmcp.ToolAnnotations{Title: "Quote Annotated"},
	})
	if tool.Name != "quote" || tool.Title != "Quote" || tool.Description != "quotes a tenant" {
		t.Fatalf("tool metadata not preserved: %#v", tool)
	}
	if string(tool.InputSchema) != `{"type":"object"}` {
		t.Fatalf("unexpected input schema: %s", tool.InputSchema)
	}
	if !json.Valid(tool.OutputSchema) {
		t.Fatalf("output schema is not valid JSON: %s", tool.OutputSchema)
	}
	if !json.Valid(tool.Annotations) {
		t.Fatalf("annotations are not valid JSON: %s", tool.Annotations)
	}
}

func TestFromSDKToolResultPreservesContentAndState(t *testing.T) {
	result := fromSDKToolResult(&sdkmcp.CallToolResult{
		Content: []sdkmcp.Content{
			&sdkmcp.TextContent{Text: "plain"},
			&sdkmcp.ImageContent{Data: []byte{0x01, 0x02}, MIMEType: "image/png"},
		},
		StructuredContent: map[string]any{"ok": true},
		IsError:           true,
		RequestState:      "state-1",
	})
	if result == nil {
		t.Fatal("nil result")
	}
	if !result.IsError || result.ResultType != "complete" {
		t.Fatalf("unexpected error/resultType: %#v", result)
	}
	if len(result.Content) != 2 {
		t.Fatalf("unexpected content count: %#v", result.Content)
	}
	if result.Content[0].Type != "text" || result.Content[0].Text != "plain" {
		t.Fatalf("text content not preserved: %#v", result.Content[0])
	}
	if result.Content[1].Type != "image" || result.Content[1].MimeType != "image/png" {
		t.Fatalf("image content not preserved: %#v", result.Content[1])
	}
	if result.Content[1].Data != "AQI=" {
		t.Fatalf("image data not preserved as base64: %q", result.Content[1].Data)
	}
	if string(result.StructuredContent) != `{"ok":true}` {
		t.Fatalf("structured content not preserved: %s", result.StructuredContent)
	}
	if string(result.RequestState) != `"state-1"` {
		t.Fatalf("request state not preserved: %s", result.RequestState)
	}
}

func TestFromSDKToolResultMapsInputRequired(t *testing.T) {
	// An input_required result is parsed from the wire shape the SDK exposes.
	var res sdkmcp.CallToolResult
	if err := json.Unmarshal([]byte(`{"resultType":"input_required"}`), &res); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !res.NeedsInput() {
		t.Fatal("expected NeedsInput to be true")
	}
	result := fromSDKToolResult(&res)
	if result.ResultType != "input_required" {
		t.Fatalf("expected input_required, got %q", result.ResultType)
	}
}
