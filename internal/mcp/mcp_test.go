package mcp

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
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
		{
			name:       "visible",
			toolPrefix: ToolPrefix("visible"),
			tools:      []Tool{{Name: "ping", Description: "visible tool"}},
		},
		{
			name:       "hooks-only",
			toolPrefix: PluginToolPrefix("demo.hooks"),
			hideTools:  true,
			tools:      []Tool{{Name: "on_event", Description: "internal hook tool"}},
		},
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
	type fakeClient struct {
		tools  []Tool
		closed bool
	}
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

// ─── encodeParams ─────────────────────────────────────────────────────────────

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

type fakeManagerClient struct {
	tools      []Tool
	callResult string
	closed     bool
}

func (c *fakeManagerClient) Initialize(context.Context) error { return nil }

func (c *fakeManagerClient) ListTools(context.Context) ([]Tool, error) {
	return append([]Tool(nil), c.tools...), nil
}

func (c *fakeManagerClient) CallTool(context.Context, string, map[string]any) (*ToolResult, error) {
	return &ToolResult{Content: []Content{{Type: "text", Text: c.callResult}}}, nil
}

func (c *fakeManagerClient) Close() error {
	c.closed = true
	return nil
}

// TestEncodeParams_NonMarshalable_DoesNotPanic verifies that passing a type
// that cannot be JSON-encoded does not crash the server via panic.
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

// ─── SSE client ───────────────────────────────────────────────────────────────

// TestSSEClient_StreamDoneAlreadyClosed_DoesNotPanic checks that calling send
// after the SSE stream has been closed returns an error rather than panicking.
func TestSSEClient_StreamDoneAlreadyClosed_DoesNotPanic(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("send panicked after stream close: %v", r)
		}
	}()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusAccepted)
	}))
	defer srv.Close()

	c := &sseClient{
		name:       "test",
		postURL:    srv.URL + "/msg",
		timeout:    2 * time.Second,
		http:       &http.Client{Timeout: 0},
		responses:  make(chan *rpcResponse, 256),
		streamDone: make(chan struct{}),
	}
	c.streamOnce.Do(func() { close(c.streamDone) })

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_, err := c.send(ctx, "tools/list", encodeParams(map[string]any{}))
	if err == nil {
		t.Fatal("expected error after stream close, got nil")
	}
	if !strings.Contains(err.Error(), "SSE stream closed") {
		t.Fatalf("unexpected error: %v", err)
	}
}

// TestSSEClient_ContextCancelled_DoesNotPanic checks that cancelling the
// context during send returns ctx.Err() without panicking.
func TestSSEClient_ContextCancelled_DoesNotPanic(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("send panicked on context cancel: %v", r)
		}
	}()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusAccepted)
	}))
	defer srv.Close()

	c := &sseClient{
		name:       "test",
		postURL:    srv.URL + "/msg",
		timeout:    5 * time.Second,
		http:       &http.Client{Timeout: 0},
		responses:  make(chan *rpcResponse, 256),
		streamDone: make(chan struct{}),
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel before send

	_, err := c.send(ctx, "tools/list", encodeParams(map[string]any{}))
	if err == nil {
		t.Fatal("expected error on cancelled context, got nil")
	}
}

// TestSSEClient_CallMu_NoConcurrentPanic runs two goroutines that each call
// send concurrently. callMu serializes them so neither should panic.
func TestSSEClient_CallMu_NoConcurrentPanic(t *testing.T) {
	respCh := make(chan *rpcResponse, 16)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req rpcRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		_ = r.Body.Close()
		w.WriteHeader(http.StatusAccepted)

		result, _ := json.Marshal(toolsListResult{Tools: []Tool{{Name: "ping"}}})
		resp := &rpcResponse{JSONRPC: "2.0", ID: req.ID, Result: result}
		select {
		case respCh <- resp:
		default:
		}
	}))
	defer srv.Close()

	c := &sseClient{
		name:       "test",
		postURL:    srv.URL + "/msg",
		timeout:    3 * time.Second,
		http:       &http.Client{Timeout: 0},
		responses:  make(chan *rpcResponse, 256),
		streamDone: make(chan struct{}),
	}

	stop := make(chan struct{})
	defer close(stop)
	go func() {
		for {
			select {
			case <-stop:
				return
			case resp := <-respCh:
				select {
				case c.responses <- resp:
				case <-stop:
					return
				}
			}
		}
	}()

	var wg sync.WaitGroup
	panicked := false
	var panicMu sync.Mutex
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() {
				if r := recover(); r != nil {
					panicMu.Lock()
					panicked = true
					panicMu.Unlock()
				}
			}()
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			_, _ = c.send(ctx, "tools/list", encodeParams(map[string]any{}))
		}()
	}
	wg.Wait()

	if panicked {
		t.Fatal("concurrent send caused a panic")
	}
}
