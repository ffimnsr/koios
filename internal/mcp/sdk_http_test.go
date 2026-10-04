package mcp

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ffimnsr/koios/internal/config"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

// sdkHTTPRecord captures one request observed by the SDK HTTP test fixture.
type sdkHTTPRecord struct {
	httpMethod string
	path       string
	header     http.Header
	rpcMethod  string
	rpcParams  json.RawMessage
}

// sdkHTTPServer is an in-process MCP server built on the official SDK server
// APIs, wrapped with a middleware that records every request and its headers
// so tests can assert header behavior.
type sdkHTTPServer struct {
	t      *testing.T
	server *sdkmcp.Server
	srv    *httptest.Server

	mu      sync.Mutex
	records []sdkHTTPRecord
}

// newSDKHTTPServer builds the fixture server with tools, a resource, a
// resource template, and a prompt. The httptest server is closed via
// t.Cleanup; client sessions must be closed before it, which LIFO cleanup
// ordering handles when clients are created after the fixture.
func newSDKHTTPServer(t *testing.T) *sdkHTTPServer {
	t.Helper()
	fixture := &sdkHTTPServer{t: t}
	fixture.server = sdkmcp.NewServer(&sdkmcp.Implementation{Name: "sdk-http-fixture", Version: "1.0.0"}, nil)
	addSDKFixtureTools(fixture.server)
	addSDKFixtureAssets(fixture.server)

	handler := sdkmcp.NewStreamableHTTPHandler(func(*http.Request) *sdkmcp.Server {
		return fixture.server
	}, nil)
	fixture.srv = httptest.NewServer(fixture.recordMiddleware(handler))
	t.Cleanup(fixture.srv.Close)
	return fixture
}

// newSDKHTTPServerToolsOnly builds the fixture server without resources,
// resource templates, or prompts so optional-capability behavior can be
// exercised end to end.
func newSDKHTTPServerToolsOnly(t *testing.T) *sdkHTTPServer {
	t.Helper()
	fixture := &sdkHTTPServer{t: t}
	fixture.server = sdkmcp.NewServer(&sdkmcp.Implementation{Name: "sdk-http-fixture", Version: "1.0.0"}, nil)
	addSDKFixtureTools(fixture.server)

	handler := sdkmcp.NewStreamableHTTPHandler(func(*http.Request) *sdkmcp.Server {
		return fixture.server
	}, nil)
	fixture.srv = httptest.NewServer(fixture.recordMiddleware(handler))
	t.Cleanup(fixture.srv.Close)
	return fixture
}

// addSDKFixtureTools registers the tools shared by every fixture variant.
func addSDKFixtureTools(server *sdkmcp.Server) {
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

	// tenant_echo declares an x-mcp-header annotation on its tenant argument,
	// so the client must send it as an Mcp-Param-Tenant request header.
	sdkmcp.AddTool(server, &sdkmcp.Tool{
		Name:        "tenant_echo",
		Description: "echoes the tenant and text",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"tenant": map[string]any{"type": "string", "x-mcp-header": "Tenant"},
				"text":   map[string]any{"type": "string"},
			},
		},
	}, func(_ context.Context, _ *sdkmcp.CallToolRequest, args struct {
		Tenant string `json:"tenant"`
		Text   string `json:"text"`
	}) (*sdkmcp.CallToolResult, any, error) {
		return &sdkmcp.CallToolResult{Content: []sdkmcp.Content{&sdkmcp.TextContent{Text: args.Tenant + ":" + args.Text}}}, nil, nil
	})

	// slow sleeps before responding so timeout tests can observe the
	// configured per-request deadline.
	sdkmcp.AddTool(server, &sdkmcp.Tool{
		Name:        "slow",
		Description: "sleeps before responding",
		InputSchema: map[string]any{"type": "object", "properties": map[string]any{}},
	}, func(_ context.Context, _ *sdkmcp.CallToolRequest, _ struct{}) (*sdkmcp.CallToolResult, any, error) {
		time.Sleep(1500 * time.Millisecond)
		return &sdkmcp.CallToolResult{Content: []sdkmcp.Content{&sdkmcp.TextContent{Text: "finally"}}}, nil, nil
	})
}

// addSDKFixtureAssets registers the resource, resource template, and prompt
// shared by the full fixture variant.
func addSDKFixtureAssets(server *sdkmcp.Server) {
	const resourceURI = "mach1://strategy-spec/schema.json"
	server.AddResource(&sdkmcp.Resource{
		URI:      resourceURI,
		Name:     "strategy-spec",
		MIMEType: "application/json",
	}, func(_ context.Context, _ *sdkmcp.ReadResourceRequest) (*sdkmcp.ReadResourceResult, error) {
		return &sdkmcp.ReadResourceResult{
			Contents: []*sdkmcp.ResourceContents{{
				URI:      resourceURI,
				MIMEType: "application/json",
				Text:     "{}",
			}},
			// A TTL makes the result cacheable by the manager.
			Cacheable: sdkmcp.Cacheable{TTLMs: 5000, CacheScope: "public"},
		}, nil
	})

	const templateURI = "mach1://specs/{id}/schema.json"
	server.AddResourceTemplate(&sdkmcp.ResourceTemplate{
		Name:        "spec-template",
		URITemplate: templateURI,
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
}

// recordMiddleware records every request (method, path, headers, and the
// JSON-RPC method for POST bodies) before passing it to the SDK handler.
func (f *sdkHTTPServer) recordMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		record := sdkHTTPRecord{httpMethod: r.Method, path: r.URL.Path, header: r.Header.Clone()}
		if r.Method == http.MethodPost {
			body, err := io.ReadAll(r.Body)
			if err != nil {
				http.Error(w, "read body", http.StatusBadRequest)
				return
			}
			_ = r.Body.Close()
			r.Body = io.NopCloser(bytes.NewReader(body))
			record.rpcMethod, record.rpcParams = parseRecordedRPC(body)
		}
		f.mu.Lock()
		f.records = append(f.records, record)
		f.mu.Unlock()
		next.ServeHTTP(w, r)
	})
}

func parseRecordedRPC(body []byte) (string, json.RawMessage) {
	var msg struct {
		Method string          `json:"method"`
		Params json.RawMessage `json:"params"`
	}
	if err := json.Unmarshal(body, &msg); err != nil {
		return "", nil
	}
	return msg.Method, msg.Params
}

// recordsForRPC returns the recorded requests whose JSON-RPC method matches.
func (f *sdkHTTPServer) recordsForRPC(method string) []sdkHTTPRecord {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []sdkHTTPRecord
	for _, r := range f.records {
		if r.rpcMethod == method {
			out = append(out, r)
		}
	}
	return out
}

// clientConfig returns the config for a client targeting this fixture.
func (f *sdkHTTPServer) clientConfig(headers map[string]string, timeout string) config.MCPServerConfig {
	return config.MCPServerConfig{
		Name:      "sdk-http-fixture",
		Transport: "http",
		URL:       f.srv.URL,
		Headers:   headers,
		Timeout:   timeout,
		Enabled:   true,
	}
}

// newClient builds a client for the fixture with the given static headers and
// timeout. Cleanup closes the client before the fixture server, so the SSE
// stream is gone by the time httptest.Server.Close waits for handlers.
func (f *sdkHTTPServer) newClient(t *testing.T, headers map[string]string, timeout string) Client {
	t.Helper()
	client := NewSDKClient(f.clientConfig(headers, timeout))
	t.Cleanup(func() { _ = client.Close() })
	return client
}

func TestSDKClientHTTPInitializeConnects(t *testing.T) {
	fixture := newSDKHTTPServer(t)
	client := fixture.newClient(t, nil, "10s")
	initializeFixture(t, client)

	if len(fixture.recordsForRPC("server/discover")) == 0 {
		t.Fatal("no server/discover request was recorded over HTTP")
	}
	discover, err := client.Discover(context.Background())
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	// The official SDK's HTTP server transport filters 2026-07-28 from its
	// supported versions (it only serves it over stdio), so the session is
	// negotiated down. The value must at least be a supported non-empty
	// version negotiated over the wire.
	if discover.ProtocolVersion == "" {
		t.Fatal("expected a negotiated protocol version")
	}
	if discover.ServerInfo.Name != "sdk-http-fixture" || discover.ServerInfo.Version != "1.0.0" {
		t.Fatalf("unexpected server info: %#v", discover.ServerInfo)
	}
}

func TestSDKClientHTTPInitializeIsIdempotent(t *testing.T) {
	fixture := newSDKHTTPServer(t)
	client := fixture.newClient(t, nil, "10s")
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
	if got := len(fixture.recordsForRPC("server/discover")); got != 1 {
		t.Fatalf("expected exactly one server/discover handshake, got %d", got)
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

func TestSDKClientHTTPListTools(t *testing.T) {
	fixture := newSDKHTTPServer(t)
	client := fixture.newClient(t, nil, "10s")
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
	if echo.Title != "Echo" || echo.Description != "echoes text back" {
		t.Fatalf("tool metadata not preserved: %#v", echo)
	}
}

func TestSDKClientHTTPCallTool(t *testing.T) {
	fixture := newSDKHTTPServer(t)
	client := fixture.newClient(t, nil, "10s")
	initializeFixture(t, client)

	result, err := client.CallTool(context.Background(), "echo", map[string]any{"text": "hello http"})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if result.IsError {
		t.Fatalf("unexpected IsError: %#v", result)
	}
	if len(result.Content) != 1 || result.Content[0].Type != "text" || result.Content[0].Text != "hello http" {
		t.Fatalf("unexpected content: %#v", result.Content)
	}

	failed, err := client.CallTool(context.Background(), "boom", nil)
	if err != nil {
		t.Fatalf("CallTool boom: %v", err)
	}
	if !failed.IsError || len(failed.Content) != 1 || failed.Content[0].Text != "boom failed" {
		t.Fatalf("tool-level IsError not preserved: %#v", failed)
	}
}

// TestSDKClientHTTPSendsStaticHeaders verifies that configured headers reach
// the server on the handshake and on every RPC type.
func TestSDKClientHTTPSendsStaticHeaders(t *testing.T) {
	fixture := newSDKHTTPServer(t)
	client := fixture.newClient(t, map[string]string{"X-Koios-Test": "static-value"}, "10s")
	initializeFixture(t, client)

	if _, err := client.ListTools(context.Background()); err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	if _, err := client.CallTool(context.Background(), "echo", map[string]any{"text": "hi"}); err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if _, err := client.ListResources(context.Background()); err != nil {
		t.Fatalf("ListResources: %v", err)
	}
	if _, err := client.ReadResource(context.Background(), "mach1://strategy-spec/schema.json"); err != nil {
		t.Fatalf("ReadResource: %v", err)
	}
	if _, err := client.ListPrompts(context.Background()); err != nil {
		t.Fatalf("ListPrompts: %v", err)
	}
	if _, err := client.GetPrompt(context.Background(), "greet", nil); err != nil {
		t.Fatalf("GetPrompt: %v", err)
	}

	for _, method := range []string{
		"server/discover",
		"tools/list",
		"tools/call",
		"resources/list",
		"resources/read",
		"prompts/list",
		"prompts/get",
	} {
		records := fixture.recordsForRPC(method)
		if len(records) == 0 {
			t.Fatalf("no %s request was recorded", method)
		}
		for _, rec := range records {
			if got := rec.header.Get("X-Koios-Test"); got != "static-value" {
				t.Fatalf("%s request missing static header: got %q", method, got)
			}
		}
	}
}

// TestSDKClientHTTPDynamicParamHeadersOnToolCall verifies that per-tool
// x-mcp-header annotations are converted into Mcp-Param-* headers on
// tools/call while static headers remain present. It runs against the minimal
// 2026-07-28 server: the official SDK's HTTP server transport negotiates down
// to 2025-11-25, where the standard headers (SEP-2243) are not emitted.
func TestSDKClientHTTPDynamicParamHeadersOnToolCall(t *testing.T) {
	server := newMinimalMCPServer(t)
	cfg := configForMinimalServer(server)
	cfg.Headers = map[string]string{"X-Koios-Test": "static-value"}
	client := NewSDKClient(cfg)
	t.Cleanup(func() { _ = client.Close() })
	initializeFixture(t, client)

	discover, err := client.Discover(context.Background())
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if discover.ProtocolVersion != ProtocolVersion2026 {
		t.Fatalf("expected the minimal server to negotiate %s, got %q", ProtocolVersion2026, discover.ProtocolVersion)
	}

	// ListTools first: the SDK derives param headers from the cached tool schema.
	tools, err := client.ListTools(context.Background())
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	var quote *Tool
	for i := range tools {
		if tools[i].Name == "quote" {
			quote = &tools[i]
		}
		if tools[i].Name == "bad_tenant" {
			t.Fatalf("tool with invalid x-mcp-header annotation must be filtered: %#v", tools)
		}
	}
	if quote == nil {
		t.Fatalf("quote tool missing from %#v", tools)
	}

	if _, err := client.CallTool(context.Background(), "quote", map[string]any{"tenant": "acme"}); err != nil {
		t.Fatalf("CallTool: %v", err)
	}

	records := server.recordsForRPC("tools/call")
	if len(records) == 0 {
		t.Fatal("no tools/call request recorded")
	}
	hdr := records[len(records)-1].header
	if got := hdr.Get("Mcp-Param-Tenant"); got != "acme" {
		t.Fatalf("expected Mcp-Param-Tenant header %q, got %q", "acme", got)
	}
	if got := hdr.Get("Mcp-Method"); got != "tools/call" {
		t.Fatalf("unexpected Mcp-Method header %q", got)
	}
	if got := hdr.Get("Mcp-Name"); got != "quote" {
		t.Fatalf("unexpected Mcp-Name header %q", got)
	}
	// Static headers must still be present when dynamic headers are added.
	if got := hdr.Get("X-Koios-Test"); got != "static-value" {
		t.Fatalf("static header missing on tools/call: got %q", got)
	}
}

func TestSDKClientHTTPTimeoutAppliesToDelayedResponse(t *testing.T) {
	fixture := newSDKHTTPServer(t)
	client := fixture.newClient(t, nil, "500ms")
	initializeFixture(t, client)

	start := time.Now()
	_, err := client.CallTool(context.Background(), "slow", nil)
	if err == nil {
		t.Fatal("expected the delayed tool call to fail")
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("timeout did not bound the request (took %v)", elapsed)
	}
	if !strings.Contains(err.Error(), "tools/call") || !strings.Contains(err.Error(), "sdk-http-fixture") {
		t.Fatalf("error should carry operation and server names: %v", err)
	}
	lower := strings.ToLower(err.Error())
	if !strings.Contains(lower, "deadline") && !strings.Contains(lower, "timeout") && !strings.Contains(lower, "canceled") {
		t.Fatalf("expected a deadline-related error, got: %v", err)
	}
}

func TestSDKClientHTTPFailedResponseIsContextual(t *testing.T) {
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = io.WriteString(w, "boom")
	}))
	defer bad.Close()

	client := NewSDKClient(config.MCPServerConfig{
		Name:      "sdk-http-500",
		Transport: "http",
		URL:       bad.URL,
		Timeout:   "5s",
	})
	defer client.Close()

	err := client.Initialize(context.Background())
	if err == nil {
		t.Fatal("Initialize against a failing server should error")
	}
	if !strings.Contains(err.Error(), "mcp sdk sdk-http-500: initialize:") {
		t.Fatalf("error should carry the server and operation names: %v", err)
	}
}

func TestSDKClientHTTPInitializeEmptyURLErrors(t *testing.T) {
	client := NewSDKClient(config.MCPServerConfig{Name: "sdk-http-empty", Transport: "http"})
	defer client.Close()
	err := client.Initialize(context.Background())
	if err == nil {
		t.Fatal("Initialize with empty URL should fail")
	}
	if !strings.Contains(err.Error(), "http URL is required") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestSDKClientHTTPCloseAfterInitialize(t *testing.T) {
	fixture := newSDKHTTPServer(t)
	client := fixture.newClient(t, nil, "10s")
	initializeFixture(t, client)

	if err := client.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := client.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
	// Session state must be cleared: requests fail instead of touching a dead
	// session.
	if _, err := client.ListTools(context.Background()); err == nil {
		t.Fatal("ListTools after Close should fail")
	}
}

// ─── minimal in-process MCP server ───────────────────────────────────────────

// minimalMCPRecord captures one request to the minimal server.
type minimalMCPRecord struct {
	rpcMethod string
	rpcParams json.RawMessage
	header    http.Header
}

// minimalMCPServer implements just enough of the MCP protocol to connect the
// SDK client and serve tools/list and tools/call, recording every request
// header. Unlike the full SDK server it performs no standard-header validation,
// which lets it advertise tools with invalid x-mcp-header annotations.
//
// When assets is true it also serves resources, resource templates, and
// prompts; otherwise those methods fail with -32601 method not found, matching
// servers that do not implement the capability.
//
// When versions is set, server/discover advertises exactly those protocol
// versions, and the legacy initialize handshake negotiates against them, so
// multi-version negotiation can be exercised for servers that only speak older
// protocol revisions. When noDiscover is set, server/discover answers with
// method not found, matching pre-2026-07-28 servers that do not know the
// stateless discovery RPC.
type minimalMCPServer struct {
	mu         sync.Mutex
	records    []minimalMCPRecord
	srv        *httptest.Server
	url        string
	assets     bool
	versions   []string
	noDiscover bool
	// paging, when true, makes tools/list serve two pages so client-side
	// cursor pagination is exercised end to end.
	paging bool
}

func newMinimalMCPServer(t *testing.T) *minimalMCPServer {
	return newMinimalMCPServerMode(t, false)
}

func newMinimalMCPServerWithAssets(t *testing.T) *minimalMCPServer {
	return newMinimalMCPServerMode(t, true)
}

func newMinimalMCPServerMode(t *testing.T, assets bool) *minimalMCPServer {
	t.Helper()
	m := &minimalMCPServer{assets: assets, versions: []string{ProtocolVersion2026}}
	m.srv = httptest.NewServer(http.HandlerFunc(m.serveHTTP))
	t.Cleanup(m.srv.Close)
	m.url = m.srv.URL
	return m
}

// newMinimalMCPServerWithVersions builds a server that advertises exactly the
// given protocol versions (newest first) in server/discover and negotiates the
// legacy initialize handshake against them, for multi-version tests.
func newMinimalMCPServerWithVersions(t *testing.T, versions []string) *minimalMCPServer {
	t.Helper()
	m := &minimalMCPServer{versions: versions}
	m.srv = httptest.NewServer(http.HandlerFunc(m.serveHTTP))
	t.Cleanup(m.srv.Close)
	m.url = m.srv.URL
	return m
}

// newMinimalMCPServerLegacyOnly builds a server that does not implement the
// stateless server/discover RPC at all (answering method not found, as real
// pre-2026-07-28 servers do) and negotiates the legacy initialize handshake
// against the given versions.
func newMinimalMCPServerLegacyOnly(t *testing.T, versions []string) *minimalMCPServer {
	t.Helper()
	m := &minimalMCPServer{versions: versions, noDiscover: true}
	m.srv = httptest.NewServer(http.HandlerFunc(m.serveHTTP))
	t.Cleanup(m.srv.Close)
	m.url = m.srv.URL
	return m
}

// newMinimalMCPServerPaged builds a server whose tools/list response is split
// across two pages.
func newMinimalMCPServerPaged(t *testing.T) *minimalMCPServer {
	t.Helper()
	m := &minimalMCPServer{paging: true, versions: []string{ProtocolVersion2026}}
	m.srv = httptest.NewServer(http.HandlerFunc(m.serveHTTP))
	t.Cleanup(m.srv.Close)
	m.url = m.srv.URL
	return m
}

func (m *minimalMCPServer) serveHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		// The client opens a standalone SSE stream after the handshake; hold
		// the connection open until the client hangs up.
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		<-r.Context().Done()
		return
	}
	body, _ := io.ReadAll(r.Body)
	_ = r.Body.Close()
	var req struct {
		ID     json.RawMessage `json:"id"`
		Method string          `json:"method"`
		Params json.RawMessage `json:"params"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	m.mu.Lock()
	m.records = append(m.records, minimalMCPRecord{rpcMethod: req.Method, rpcParams: req.Params, header: r.Header.Clone()})
	m.mu.Unlock()

	if len(req.ID) == 0 || string(req.ID) == "null" {
		// JSON-RPC notifications (e.g. notifications/initialized) carry no id;
		// acknowledge them without a response body.
		w.WriteHeader(http.StatusAccepted)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	if m.noDiscover && req.Method == "server/discover" {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"jsonrpc": "2.0",
			"id":      req.ID,
			"error":   map[string]any{"code": -32601, "message": "method not found"},
		})
		return
	}
	versions := m.versions
	if len(versions) == 0 {
		versions = []string{ProtocolVersion2026}
	}
	switch req.Method {
	case "server/discover":
		_ = json.NewEncoder(w).Encode(map[string]any{
			"jsonrpc": "2.0",
			"id":      req.ID,
			"result": map[string]any{
				"supportedVersions": versions,
				"capabilities":      map[string]any{"tools": map[string]any{"listChanged": false}},
				"_meta": map[string]any{
					"io.modelcontextprotocol/serverInfo": map[string]any{"name": "minimal-fixture", "version": "1.0.0"},
				},
			},
		})
	case "initialize":
		// Legacy handshake: echo the requested version when it is supported,
		// otherwise answer with the newest version the fixture supports.
		var params struct {
			ProtocolVersion string `json:"protocolVersion"`
		}
		_ = json.Unmarshal(req.Params, &params)
		negotiated := params.ProtocolVersion
		if !slices.Contains(versions, negotiated) {
			negotiated = versions[0]
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"jsonrpc": "2.0",
			"id":      req.ID,
			"result": map[string]any{
				"protocolVersion": negotiated,
				"capabilities":    map[string]any{},
				"serverInfo":      map[string]any{"name": "minimal-fixture", "version": "1.0.0"},
			},
		})
	case "tools/list":
		if m.paging {
			var params struct {
				Cursor string `json:"cursor"`
			}
			_ = json.Unmarshal(req.Params, &params)
			page := []any{
				map[string]any{
					"name":        "quote",
					"description": "quotes a tenant",
					"inputSchema": map[string]any{
						"type": "object",
						"properties": map[string]any{
							"tenant": map[string]any{"type": "string", "x-mcp-header": "Tenant"},
						},
					},
				},
			}
			result := map[string]any{"resultType": "complete", "tools": page}
			if params.Cursor == "" {
				result["nextCursor"] = "page-2"
			} else {
				result["tools"] = []any{
					map[string]any{
						"name":        "approve_flow",
						"description": "second page tool",
						"inputSchema": map[string]any{"type": "object", "properties": map[string]any{}},
					},
				}
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": result})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"jsonrpc": "2.0",
			"id":      req.ID,
			"result": map[string]any{
				"resultType": "complete",
				"tools": []any{
					map[string]any{
						"name":        "quote",
						"description": "quotes a tenant",
						"inputSchema": map[string]any{
							"type": "object",
							"properties": map[string]any{
								"tenant": map[string]any{"type": "string", "x-mcp-header": "Tenant"},
							},
						},
					},
					map[string]any{
						"name":        "bad_tenant",
						"description": "has an invalid x-mcp-header annotation",
						"inputSchema": map[string]any{
							"type": "object",
							"properties": map[string]any{
								"tenant": map[string]any{"type": "string", "x-mcp-header": "Ten ant"},
							},
						},
					},
					map[string]any{
						"name":        "approve_flow",
						"description": "requires approval before completing",
						"inputSchema": map[string]any{
							"type":       "object",
							"properties": map[string]any{"amount": map[string]any{"type": "number"}},
						},
					},
				},
			},
		})
	case "tools/call":
		var params struct {
			Name           string          `json:"name"`
			InputResponses json.RawMessage `json:"inputResponses"`
		}
		_ = json.Unmarshal(req.Params, &params)
		if params.Name == "approve_flow" {
			if len(params.InputResponses) == 0 {
				// First call: ask for approval. requestId, ttlMs, and cacheScope
				// are intentional extras: the SDK client drops them on decode,
				// which tests assert as the v1.8 behavior contract.
				_ = json.NewEncoder(w).Encode(map[string]any{
					"jsonrpc": "2.0",
					"id":      req.ID,
					"result": map[string]any{
						"resultType":   "input_required",
						"requestId":    "req-approve-1",
						"requestState": "rs-approve-1",
						"ttlMs":        60000,
						"cacheScope":   "public",
						"inputRequests": map[string]any{
							"q1": map[string]any{
								"method": "elicitation/create",
								"params": map[string]any{"mode": "form", "message": "Approve the transfer?"},
							},
						},
					},
				})
				return
			}
			// Follow-up call carrying inputResponses: the flow completes.
			_ = json.NewEncoder(w).Encode(map[string]any{
				"jsonrpc": "2.0",
				"id":      req.ID,
				"result": map[string]any{
					"resultType": "complete",
					"content":    []any{map[string]any{"type": "text", "text": "accepted"}},
				},
			})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"jsonrpc": "2.0",
			"id":      req.ID,
			"result": map[string]any{
				"resultType": "complete",
				"content":    []any{map[string]any{"type": "text", "text": "ok"}},
			},
		})
	case "resources/list":
		if !m.assets {
			m.writeMethodNotFound(w, req.ID)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"jsonrpc": "2.0",
			"id":      req.ID,
			"result": map[string]any{
				"resultType": "complete",
				"resources": []any{
					map[string]any{
						"uri":         "mach1://strategy-spec/schema.json",
						"name":        "strategy-spec",
						"title":       "Strategy Spec",
						"description": "the strategy specification",
						"mimeType":    "application/json",
						"size":        12345,
						"annotations": map[string]any{"audience": []string{"user"}},
					},
				},
			},
		})
	case "resources/templates/list":
		if !m.assets {
			m.writeMethodNotFound(w, req.ID)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"jsonrpc": "2.0",
			"id":      req.ID,
			"result": map[string]any{
				"resultType": "complete",
				"resourceTemplates": []any{
					map[string]any{
						"uriTemplate": "mach1://specs/{id}/schema.json",
						"name":        "spec-template",
						"title":       "Spec Template",
						"description": "one spec per id",
						"mimeType":    "application/json",
					},
				},
			},
		})
	case "resources/read":
		if !m.assets {
			m.writeMethodNotFound(w, req.ID)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"jsonrpc": "2.0",
			"id":      req.ID,
			"result": map[string]any{
				"resultType": "complete",
				"contents": []any{
					map[string]any{
						"uri":      "mach1://strategy-spec/schema.json",
						"mimeType": "application/json",
						"text":     "{}",
					},
					map[string]any{
						"uri":      "mach1://strategy-spec/logo.png",
						"mimeType": "image/png",
						"blob":     base64.StdEncoding.EncodeToString([]byte("logo")),
					},
				},
				"ttlMs":      5000,
				"cacheScope": "public",
			},
		})
	case "prompts/list":
		if !m.assets {
			m.writeMethodNotFound(w, req.ID)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"jsonrpc": "2.0",
			"id":      req.ID,
			"result": map[string]any{
				"resultType": "complete",
				"prompts": []any{
					map[string]any{
						"name":        "greet",
						"title":       "Greet",
						"description": "greets someone",
						"arguments": []any{
							map[string]any{"name": "name", "description": "who to greet", "required": true},
							map[string]any{"name": "tone", "title": "Tone"},
						},
					},
				},
			},
		})
	case "prompts/get":
		if !m.assets {
			m.writeMethodNotFound(w, req.ID)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"jsonrpc": "2.0",
			"id":      req.ID,
			"result": map[string]any{
				"resultType":  "complete",
				"description": "greets the caller",
				"messages": []any{
					map[string]any{
						"role":    "user",
						"content": map[string]any{"type": "text", "text": "hello acme"},
					},
				},
				"ttlMs":      3000,
				"cacheScope": "public",
			},
		})
	default:
		// Notifications and anything else: accepted with no body.
		w.WriteHeader(http.StatusAccepted)
	}
}

// writeMethodNotFound responds with a JSON-RPC -32601 error, the standard way
// servers signal that an optional capability is not implemented.
func (m *minimalMCPServer) writeMethodNotFound(w http.ResponseWriter, id json.RawMessage) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"error":   map[string]any{"code": -32601, "message": "method not found"},
	})
}

func (m *minimalMCPServer) recordsForRPC(method string) []minimalMCPRecord {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []minimalMCPRecord
	for _, r := range m.records {
		if r.rpcMethod == method {
			out = append(out, r)
		}
	}
	return out
}

// TestSDKClientHTTPInvalidHeaderToolDoesNotProduceUnsafeRequest verifies that a
// tool whose x-mcp-header annotation is not a valid header name is dropped from
// listing and never produces Mcp-Param-* headers, while valid tools still do.
func TestSDKClientHTTPInvalidHeaderToolDoesNotProduceUnsafeRequest(t *testing.T) {
	server := newMinimalMCPServer(t)
	client := NewSDKClient(configForMinimalServer(server))
	t.Cleanup(func() { _ = client.Close() })
	initializeFixture(t, client)

	tools, err := client.ListTools(context.Background())
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	for _, tool := range tools {
		if tool.Name == "bad_tenant" {
			t.Fatal("tool with invalid x-mcp-header annotation must be filtered from listing")
		}
	}

	// Calling the invalid tool works but must not carry any Mcp-Param-* header.
	if _, err := client.CallTool(context.Background(), "bad_tenant", map[string]any{"tenant": "acme"}); err != nil {
		t.Fatalf("CallTool bad_tenant: %v", err)
	}
	records := server.recordsForRPC("tools/call")
	if len(records) != 1 {
		t.Fatalf("expected exactly one tools/call request, got %d", len(records))
	}
	for key := range records[0].header {
		if strings.HasPrefix(key, "Mcp-Param-") {
			t.Fatalf("unsafe Mcp-Param- header %q sent for tool with invalid annotation", key)
		}
	}

	// The valid tool still gets its dynamic header.
	if _, err := client.CallTool(context.Background(), "quote", map[string]any{"tenant": "acme"}); err != nil {
		t.Fatalf("CallTool quote: %v", err)
	}
	records = server.recordsForRPC("tools/call")
	last := records[len(records)-1]
	if got := last.header.Get("Mcp-Param-Tenant"); got != "acme" {
		t.Fatalf("expected Mcp-Param-Tenant header %q, got %q", "acme", got)
	}
}
