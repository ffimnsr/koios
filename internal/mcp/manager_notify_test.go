package mcp

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/ffimnsr/koios/internal/config"
)

// waitForCondition polls cond until it reports true or the timeout passes.
// Manager invalidation runs asynchronously through consumeNotifications, so
// assertions on its effects must poll.
func waitForCondition(t *testing.T, timeout time.Duration, desc string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		if cond() {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", desc)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// monacoManager builds a manager whose factory always returns the given fake
// client, primed with a cacheable resource read helper.
func monacoManager(t *testing.T, client *fakeManagerClient) *Manager {
	t.Helper()
	mgr := NewManagerWithFactory([]config.MCPServerConfig{{Name: "monaco", Enabled: true, Transport: "stdio", Command: "ignored"}}, func(config.MCPServerConfig) Client {
		return client
	})
	t.Cleanup(mgr.Close)
	if err := mgr.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	return mgr
}

func TestManagerNotificationToolsListChangedClearsTools(t *testing.T) {
	client := &fakeManagerClient{
		tools:         []Tool{{Name: "quote", Description: "quotes"}},
		notifications: make(chan Notification, 2),
	}
	mgr := monacoManager(t, client)
	if _, ok := mgr.ToolDetails(ToolName("monaco", "quote")); !ok {
		t.Fatal("expected the initial tool to be cached")
	}

	client.notifications <- Notification{Method: "notifications/tools/list_changed"}
	waitForCondition(t, 2*time.Second, "tools cache to be marked stale", func() bool {
		status, ok := mgr.ServerStatusByName("monaco")
		return ok && !status.CacheFresh
	})
	if got := mgr.ListTools(); len(got) != 0 {
		t.Fatalf("expected cleared tools, got %#v", got)
	}
	if _, ok := mgr.ToolDetails(ToolName("monaco", "quote")); ok {
		t.Fatal("expected tool details to be cleared")
	}
}

func TestManagerNotificationResourcesListChangedClearsAssets(t *testing.T) {
	const uri = "mach1://strategy-spec/schema.json"
	client := &fakeManagerClient{
		resources:         []Resource{{URI: uri, Name: "schema"}},
		resourceTemplates: []ResourceTemplate{{URITemplate: "mach1://specs/{id}/schema.json"}},
		resourceRead:      &ResourceReadResult{TTLMs: 60_000, Contents: []ResourceContent{{URI: uri, Text: "v1"}}},
		notifications:     make(chan Notification, 2),
	}
	mgr := monacoManager(t, client)

	got, err := mgr.ReadResource(context.Background(), "monaco", uri)
	if err != nil || len(got.Contents) != 1 || got.Contents[0].Text != "v1" {
		t.Fatalf("read cache seed: %#v err=%v", got, err)
	}
	client.resourceRead = &ResourceReadResult{TTLMs: 60_000, Contents: []ResourceContent{{URI: uri, Text: "v2"}}}

	client.notifications <- Notification{Method: "notifications/resources/list_changed"}
	waitForCondition(t, 2*time.Second, "resource caches to clear", func() bool {
		resources, _ := mgr.ListResources(context.Background(), "monaco")
		templates, _ := mgr.ListResourceTemplates(context.Background(), "monaco")
		return len(resources) == 0 && len(templates) == 0
	})
	// The resource read cache must be gone: the next read reaches the client.
	got, err = mgr.ReadResource(context.Background(), "monaco", uri)
	if err != nil || len(got.Contents) != 1 || got.Contents[0].Text != "v2" {
		t.Fatalf("expected a refetch after invalidation, got %#v err=%v", got, err)
	}
}

func TestManagerNotificationResourceUpdatedClearsOnlyMatchingRead(t *testing.T) {
	const uriA = "mach1://specs/a.json"
	const uriB = "mach1://specs/b.json"
	client := &fakeManagerClient{
		resourceRead:  &ResourceReadResult{TTLMs: 60_000, Contents: []ResourceContent{{URI: uriA, Text: "v1"}}},
		notifications: make(chan Notification, 2),
	}
	mgr := monacoManager(t, client)

	for _, uri := range []string{uriA, uriB} {
		got, err := mgr.ReadResource(context.Background(), "monaco", uri)
		if err != nil || got.Contents[0].Text != "v1" {
			t.Fatalf("seed read %s: %#v err=%v", uri, got, err)
		}
	}
	client.resourceRead = &ResourceReadResult{TTLMs: 60_000, Contents: []ResourceContent{{URI: uriA, Text: "v2"}}}

	client.notifications <- Notification{Method: "notifications/resources/updated", Params: json.RawMessage(`{"uri":"mach1://specs/a.json"}`)}
	waitForCondition(t, 2*time.Second, "matching read to be invalidated", func() bool {
		got, err := mgr.ReadResource(context.Background(), "monaco", uriA)
		return err == nil && len(got.Contents) == 1 && got.Contents[0].Text == "v2"
	})
	// The unrelated cached read must survive the notification.
	got, err := mgr.ReadResource(context.Background(), "monaco", uriB)
	if err != nil || len(got.Contents) != 1 || got.Contents[0].Text != "v1" {
		t.Fatalf("expected uriB to stay cached, got %#v err=%v", got, err)
	}
}

func TestManagerNotificationResourceUpdatedWithoutURIClearsAllReads(t *testing.T) {
	for name, params := range map[string]json.RawMessage{
		"empty URI":     json.RawMessage(`{"uri":""}`),
		"invalid JSON":  json.RawMessage(`{`),
		"missing field": json.RawMessage(`{"other":true}`),
	} {
		t.Run(name, func(t *testing.T) {
			const uriA = "mach1://specs/a.json"
			const uriB = "mach1://specs/b.json"
			client := &fakeManagerClient{
				resourceRead:  &ResourceReadResult{TTLMs: 60_000, Contents: []ResourceContent{{URI: uriA, Text: "v1"}}},
				notifications: make(chan Notification, 2),
			}
			mgr := monacoManager(t, client)
			for _, uri := range []string{uriA, uriB} {
				if _, err := mgr.ReadResource(context.Background(), "monaco", uri); err != nil {
					t.Fatalf("seed read %s: %v", uri, err)
				}
			}
			client.resourceRead = &ResourceReadResult{TTLMs: 60_000, Contents: []ResourceContent{{URI: uriA, Text: "v2"}}}

			client.notifications <- Notification{Method: "notifications/resources/updated", Params: params}
			waitForCondition(t, 2*time.Second, "all reads to be invalidated", func() bool {
				got, err := mgr.ReadResource(context.Background(), "monaco", uriB)
				return err == nil && len(got.Contents) == 1 && got.Contents[0].Text == "v2"
			})
		})
	}
}

func TestManagerNotificationPromptsListChangedClearsPrompts(t *testing.T) {
	client := &fakeManagerClient{
		prompts:       []Prompt{{Name: "build_strategy"}},
		notifications: make(chan Notification, 2),
	}
	mgr := monacoManager(t, client)
	if prompts, err := mgr.ListPrompts(context.Background(), "monaco"); err != nil || len(prompts) != 1 {
		t.Fatalf("expected the initial prompt cache, got %#v err=%v", prompts, err)
	}

	client.notifications <- Notification{Method: "notifications/prompts/list_changed"}
	waitForCondition(t, 2*time.Second, "prompts cache to clear", func() bool {
		prompts, err := mgr.ListPrompts(context.Background(), "monaco")
		return err == nil && len(prompts) == 0
	})
}

// ─── listener lifecycle ───────────────────────────────────────────────────────

// listenRecordingClient wraps fakeManagerClient and records every listener
// context handed to Listen so tests can observe listener cancellation.
type listenRecordingClient struct {
	*fakeManagerClient
	mu   sync.Mutex
	ctxs []context.Context
}

func (c *listenRecordingClient) Listen(ctx context.Context) (<-chan Notification, error) {
	c.mu.Lock()
	c.ctxs = append(c.ctxs, ctx)
	c.mu.Unlock()
	return c.fakeManagerClient.Listen(ctx)
}

func (c *listenRecordingClient) listenerContexts() []context.Context {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]context.Context(nil), c.ctxs...)
}

func newListenRecordingClient() *listenRecordingClient {
	return &listenRecordingClient{fakeManagerClient: &fakeManagerClient{notifications: make(chan Notification, 4)}}
}

func TestManagerStopServerCancelsListener(t *testing.T) {
	client := newListenRecordingClient()
	mgr := NewManagerWithFactory([]config.MCPServerConfig{{Name: "monaco", Enabled: true, Transport: "stdio", Command: "ignored"}}, func(config.MCPServerConfig) Client {
		return client
	})
	t.Cleanup(mgr.Close)
	if err := mgr.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}

	ctxs := client.listenerContexts()
	if len(ctxs) != 1 || ctxs[0].Err() != nil {
		t.Fatalf("expected one live listener after Start, got %#v", ctxs)
	}
	if _, err := mgr.StopServer("", ""); err != nil {
		t.Fatalf("StopServer: %v", err)
	}
	ctxs = client.listenerContexts()
	if len(ctxs) != 1 {
		t.Fatalf("StopServer must not start a new listener, got %d", len(ctxs))
	}
	if ctxs[0].Err() == nil {
		t.Fatal("expected the listener context to be canceled by StopServer")
	}
	if status, ok := mgr.ServerStatusByName("monaco"); !ok || status.SubscriptionOn {
		t.Fatalf("expected SubscriptionOn to be false after StopServer, got %#v ok=%v", status, ok)
	}
}

func TestManagerRemoveServerCancelsListener(t *testing.T) {
	client := newListenRecordingClient()
	mgr := NewManagerWithFactory([]config.MCPServerConfig{{Name: "monaco", Enabled: true, Transport: "stdio", Command: "ignored"}}, func(config.MCPServerConfig) Client {
		return client
	})
	t.Cleanup(mgr.Close)
	if err := mgr.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}

	if err := mgr.RemoveServer("monaco"); err != nil {
		t.Fatalf("RemoveServer: %v", err)
	}
	ctxs := client.listenerContexts()
	if len(ctxs) != 1 || ctxs[0].Err() == nil {
		t.Fatalf("expected the listener context to be canceled by RemoveServer, got %#v", ctxs)
	}
}

func TestManagerUpdateServerCancelsPreviousListener(t *testing.T) {
	var clients []*listenRecordingClient
	mgr := NewManagerWithFactory(nil, func(config.MCPServerConfig) Client {
		c := newListenRecordingClient()
		clients = append(clients, c)
		return c
	})
	t.Cleanup(mgr.Close)

	if _, err := mgr.AddServer(context.Background(), config.MCPServerConfig{Name: "monaco", Enabled: true, Transport: "stdio", Command: "echo"}); err != nil {
		t.Fatalf("AddServer: %v", err)
	}
	if len(clients) != 1 {
		t.Fatalf("expected one client after AddServer, got %d", len(clients))
	}

	if _, err := mgr.UpdateServer(context.Background(), config.MCPServerConfig{Name: "monaco", Enabled: true, Transport: "stdio", Command: "new-command"}); err != nil {
		t.Fatalf("UpdateServer: %v", err)
	}
	if len(clients) != 2 {
		t.Fatalf("expected a fresh client after UpdateServer, got %d", len(clients))
	}
	oldCtxs := clients[0].listenerContexts()
	if len(oldCtxs) != 1 || oldCtxs[0].Err() == nil {
		t.Fatalf("expected the previous listener to be canceled before reconnecting, got %#v", oldCtxs)
	}
	newCtxs := clients[1].listenerContexts()
	if len(newCtxs) != 1 || newCtxs[0].Err() != nil {
		t.Fatalf("expected a live listener on the updated session, got %#v", newCtxs)
	}
}

func TestManagerCloseCancelsAllListeners(t *testing.T) {
	first := newListenRecordingClient()
	second := newListenRecordingClient()
	mgr := NewManagerWithFactory(nil, func(cfg config.MCPServerConfig) Client {
		if cfg.Name == "alpha" {
			return first
		}
		return second
	})
	if _, err := mgr.AddServer(context.Background(), config.MCPServerConfig{Name: "alpha", Enabled: true, Transport: "stdio", Command: "echo"}); err != nil {
		t.Fatalf("AddServer alpha: %v", err)
	}
	if _, err := mgr.AddServer(context.Background(), config.MCPServerConfig{Name: "beta", Enabled: true, Transport: "stdio", Command: "echo"}); err != nil {
		t.Fatalf("AddServer beta: %v", err)
	}

	mgr.Close()
	for name, client := range map[string]*listenRecordingClient{"alpha": first, "beta": second} {
		ctxs := client.listenerContexts()
		if len(ctxs) != 1 || ctxs[0].Err() == nil {
			t.Fatalf("expected the %s listener to be canceled by Close, got %#v", name, ctxs)
		}
		if !client.closed {
			t.Fatalf("expected the %s SDK client to be closed by Manager.Close", name)
		}
	}
}
