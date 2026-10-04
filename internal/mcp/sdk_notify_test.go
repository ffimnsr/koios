package mcp

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestSDKClientListenBeforeInitializeErrors(t *testing.T) {
	client := NewSDKClient(sdkFixtureConfig(t))
	defer client.Close()
	notifications, err := client.Listen(context.Background())
	if err == nil {
		t.Fatal("Listen before Initialize should fail")
	}
	if notifications != nil {
		t.Fatal("expected a nil channel when Listen fails")
	}
	if !strings.Contains(err.Error(), "listen") || !strings.Contains(err.Error(), "not initialized") {
		t.Fatalf("unexpected error: %v", err)
	}
}

// TestSDKClientListenRelaysNotifications verifies the relay forwards Koios
// notifications pushed by the SDK notification handlers, preserving the raw
// params.
func TestSDKClientListenRelaysNotifications(t *testing.T) {
	client := newSDKFixtureClient(t)
	initializeFixture(t, client)

	sc := client.(*sdkClient)
	sc.mu.Lock()
	ch := sc.notifications
	sc.mu.Unlock()
	if ch == nil {
		t.Fatal("expected a notification channel after Initialize")
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	notifications, err := client.Listen(ctx)
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}

	want := Notification{
		Method: "notifications/resources/updated",
		Params: json.RawMessage(`{"uri":"mach1://strategy-spec/schema.json"}`),
	}
	ch <- want
	select {
	case got := <-notifications:
		if got.Method != want.Method {
			t.Fatalf("unexpected notification method: %#v", got)
		}
		if string(got.Params) != `{"uri":"mach1://strategy-spec/schema.json"}` {
			t.Fatalf("params not preserved: %s", got.Params)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for the relayed notification")
	}
}

func TestSDKClientListenClosesOnListenerContextCancel(t *testing.T) {
	client := newSDKFixtureClient(t)
	initializeFixture(t, client)

	ctx, cancel := context.WithCancel(context.Background())
	notifications, err := client.Listen(ctx)
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	cancel()
	select {
	case _, ok := <-notifications:
		if ok {
			t.Fatal("expected the notification channel to close on context cancel")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for the channel to close")
	}
}

func TestSDKClientListenClosesWhenSessionCloses(t *testing.T) {
	client := newSDKFixtureClient(t)
	initializeFixture(t, client)

	notifications, err := client.Listen(context.Background())
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	if err := client.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	select {
	case _, ok := <-notifications:
		if ok {
			t.Fatal("expected the notification channel to close when the session closes")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for the channel to close")
	}
}

func TestSDKClientCancelBeforeInitializeErrors(t *testing.T) {
	client := NewSDKClient(sdkFixtureConfig(t))
	defer client.Close()
	err := client.Cancel(context.Background(), 42, "user aborted")
	if err == nil {
		t.Fatal("Cancel before Initialize should fail")
	}
	if !strings.Contains(err.Error(), "notifications/cancelled") || !strings.Contains(err.Error(), "not initialized") {
		t.Fatalf("unexpected error: %v", err)
	}
}

// TestSDKClientCancelAfterInitializeIsContextual verifies Cancel reports the
// SDK limitation with server, request ID, and reason context. The official Go
// SDK v1.8 owns JSON-RPC request IDs internally and exposes no API to send
// notifications/cancelled, so the client must fail loudly instead of silently
// dropping the cancellation.
func TestSDKClientCancelAfterInitializeIsContextual(t *testing.T) {
	client := newSDKFixtureClient(t)
	initializeFixture(t, client)

	err := client.Cancel(context.Background(), 42, "user aborted")
	if err == nil {
		t.Fatal("Cancel should report that it is not supported")
	}
	msg := err.Error()
	for _, want := range []string{"sdk-fixture", "notifications/cancelled", "42", "user aborted", "not supported"} {
		if !strings.Contains(msg, want) {
			t.Fatalf("error missing %q: %v", want, err)
		}
	}
}

// TestSDKClientListenReceivesServerNotifications verifies end to end that a
// notification the SDK server sends after connect reaches the Listen channel
// with its method preserved. The SDK server pushes tools/list_changed through
// its own notification machinery over the standalone SSE stream opened by the
// 2025-11-25 HTTP session.
func TestSDKClientListenReceivesServerNotifications(t *testing.T) {
	fixture := newSDKHTTPServer(t)
	client := fixture.newClient(t, nil, "10s")
	initializeFixture(t, client)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	notifications, err := client.Listen(ctx)
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}

	// Adding a tool after connect makes the SDK server emit
	// notifications/tools/list_changed to the connected session.
	sdkmcp.AddTool(fixture.server, &sdkmcp.Tool{
		Name:        "late_tool",
		Description: "added after connect",
		InputSchema: map[string]any{"type": "object", "properties": map[string]any{}},
	}, func(_ context.Context, _ *sdkmcp.CallToolRequest, _ struct{}) (*sdkmcp.CallToolResult, any, error) {
		return &sdkmcp.CallToolResult{}, nil, nil
	})
	select {
	case got := <-notifications:
		if got.Method != "notifications/tools/list_changed" {
			t.Fatalf("unexpected notification: %#v", got)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for the pushed notification")
	}
}

// TestManagerSDKNotificationInvalidatesToolsCache verifies the full
// server-notification path end to end: the SDK server emits
// notifications/tools/list_changed, the SDK handler relays it through Listen,
// and the manager marks the tools cache stale.
func TestManagerSDKNotificationInvalidatesToolsCache(t *testing.T) {
	fixture := newSDKHTTPServer(t)
	mgr := newSDKManager(t, fixture.clientConfig(nil, "10s"))
	startSDKManager(t, mgr, "sdk-http-fixture")

	if got := len(mgr.ListTools()); got == 0 {
		t.Fatal("expected tools before the notification")
	}

	sdkmcp.AddTool(fixture.server, &sdkmcp.Tool{
		Name:        "late_tool",
		Description: "added after connect",
		InputSchema: map[string]any{"type": "object", "properties": map[string]any{}},
	}, func(_ context.Context, _ *sdkmcp.CallToolRequest, _ struct{}) (*sdkmcp.CallToolResult, any, error) {
		return &sdkmcp.CallToolResult{}, nil, nil
	})
	waitForCondition(t, 3*time.Second, "tools cache to be marked stale", func() bool {
		status, ok := mgr.ServerStatusByName("sdk-http-fixture")
		return ok && !status.CacheFresh
	})
	if got := len(mgr.ListTools()); got != 0 {
		t.Fatalf("expected cleared tools, got %d", got)
	}
}

// TestManagerSDKNotificationResourcesListChangedInvalidatesAssets verifies
// the resources/list_changed path end to end: the SDK server emits the
// notification when a resource is added after connect, and the manager drops
// its cached resource and template lists and marks the server stale.
//
// Note that a refetch assertion for cached reads is intentionally absent: the
// SDK session also honors the read TTL (5000ms) in its own cache, and
// resources/list_changed does not invalidate that layer, so the manager's
// cleared read cache may still be served by the session cache.
func TestManagerSDKNotificationResourcesListChangedInvalidatesAssets(t *testing.T) {
	fixture := newSDKHTTPServer(t)
	mgr := newSDKManager(t, fixture.clientConfig(nil, "10s"))
	startSDKManager(t, mgr, "sdk-http-fixture")

	if resources, err := mgr.ListResources(context.Background(), "sdk-http-fixture"); err != nil || len(resources) != 1 {
		t.Fatalf("expected one cached resource, got %#v err=%v", resources, err)
	}
	if templates, err := mgr.ListResourceTemplates(context.Background(), "sdk-http-fixture"); err != nil || len(templates) != 1 {
		t.Fatalf("expected one cached template, got %#v err=%v", templates, err)
	}

	// Adding a resource after connect makes the SDK server emit
	// notifications/resources/list_changed to the connected session.
	fixture.server.AddResource(&sdkmcp.Resource{
		URI:      "mach1://late-resource.txt",
		Name:     "late-resource",
		MIMEType: "text/plain",
	}, func(_ context.Context, _ *sdkmcp.ReadResourceRequest) (*sdkmcp.ReadResourceResult, error) {
		return &sdkmcp.ReadResourceResult{Contents: []*sdkmcp.ResourceContents{{URI: "mach1://late-resource.txt", Text: "late"}}}, nil
	})
	waitForCondition(t, 3*time.Second, "resource caches to be marked stale", func() bool {
		status, ok := mgr.ServerStatusByName("sdk-http-fixture")
		return ok && !status.CacheFresh
	})
	if resources, _ := mgr.ListResources(context.Background(), "sdk-http-fixture"); len(resources) != 0 {
		t.Fatalf("expected cleared resources, got %#v", resources)
	}
	if templates, _ := mgr.ListResourceTemplates(context.Background(), "sdk-http-fixture"); len(templates) != 0 {
		t.Fatalf("expected cleared templates, got %#v", templates)
	}
}
