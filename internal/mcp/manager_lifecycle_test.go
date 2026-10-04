package mcp

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/ffimnsr/koios/internal/config"
)

// gatedConnectClient is a Client stub whose Initialize blocks until its gate is
// closed or the context is canceled, letting tests hold a connection attempt in
// flight deterministically. Initialize and Close calls are recorded so tests
// can assert connect serialization and that every created client is eventually
// closed exactly once.
type gatedConnectClient struct {
	*fakeManagerClient
	gate        chan struct{}
	initStart   chan struct{}
	mu          sync.Mutex
	initStarted bool
	initCalls   int
	closeCalls  int
}

func newGatedConnectClient() *gatedConnectClient {
	return &gatedConnectClient{
		fakeManagerClient: &fakeManagerClient{tools: []Tool{{Name: "quote"}}},
		gate:              make(chan struct{}),
		initStart:         make(chan struct{}),
	}
}

func (c *gatedConnectClient) Initialize(ctx context.Context) error {
	c.mu.Lock()
	if !c.initStarted {
		c.initStarted = true
		close(c.initStart)
	}
	c.initCalls++
	c.mu.Unlock()
	select {
	case <-c.gate:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (c *gatedConnectClient) Close() error {
	c.mu.Lock()
	c.closeCalls++
	c.mu.Unlock()
	return nil
}

func (c *gatedConnectClient) releaseGate() { close(c.gate) }

func (c *gatedConnectClient) initCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.initCalls
}

func (c *gatedConnectClient) closeCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.closeCalls
}

// gatedManager builds a Manager for a single enabled server whose factory
// returns gated clients, collected on the returned channel in creation order.
func gatedManager(t *testing.T) (*Manager, <-chan *gatedConnectClient) {
	t.Helper()
	created := make(chan *gatedConnectClient, 16)
	mgr := NewManagerWithFactory([]config.MCPServerConfig{{
		Name: "monaco", Enabled: true, Transport: "stdio", Command: "ignored",
	}}, func(config.MCPServerConfig) Client {
		c := newGatedConnectClient()
		created <- c
		return c
	})
	t.Cleanup(mgr.Close)
	return mgr, created
}

// TestManagerRemoveServerDuringConnectCancelsAndWaits verifies that removing a
// server while its connection attempt is still in flight cancels the attempt,
// waits for it to unwind, and closes every client involved without leaking or
// double-closing any of them.
func TestManagerRemoveServerDuringConnectCancelsAndWaits(t *testing.T) {
	mgr, created := gatedManager(t)

	connectResult := make(chan error, 1)
	go func() { connectResult <- connectResultErr(mgr.EnsureServer(context.Background(), "", "")) }()
	first := <-created
	<-first.initStart // the connection attempt is now in flight

	removeResult := make(chan error, 1)
	go func() { removeResult <- mgr.RemoveServer("monaco") }()

	// RemoveServer must cancel the in-flight attempt rather than race it.
	select {
	case err := <-removeResult:
		if err != nil {
			t.Fatalf("RemoveServer during connect: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("RemoveServer blocked on the in-flight connect")
	}
	if err := <-connectResult; err == nil {
		t.Fatal("expected the in-flight connect to fail after cancellation")
	}
	if mgr.HasServer("monaco") {
		t.Fatal("server should be removed")
	}

	// The canceled client is closed by the connect failure path; the
	// replacement client is closed by RemoveServer.
	second := <-created
	if got := first.closeCount(); got != 1 {
		t.Fatalf("connecting client closed %d times, want 1", got)
	}
	if got := second.closeCount(); got != 1 {
		t.Fatalf("replacement client closed %d times, want 1", got)
	}
}

// TestManagerStopServerDuringConnectCancelsAndWaits verifies that stopping a
// server mid-connect cancels the attempt, keeps the entry stop-able, and that
// a later EnsureServer reconnects against a fresh client.
func TestManagerStopServerDuringConnectCancelsAndWaits(t *testing.T) {
	created := make(chan *gatedConnectClient, 16)
	mgr := NewManagerWithFactory([]config.MCPServerConfig{{
		Name: "monaco", Enabled: true, Transport: "stdio", Command: "ignored",
	}}, func(config.MCPServerConfig) Client {
		c := newGatedConnectClient()
		created <- c
		return c
	})

	connectResult := make(chan error, 1)
	go func() { connectResult <- connectResultErr(mgr.EnsureServer(context.Background(), "", "")) }()
	first := <-created
	<-first.initStart

	stopResult := make(chan error, 1)
	go func() {
		_, err := mgr.StopServer("", "")
		stopResult <- err
	}()
	select {
	case err := <-stopResult:
		if err != nil {
			t.Fatalf("StopServer during connect: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("StopServer blocked on the in-flight connect")
	}
	if err := <-connectResult; err == nil {
		t.Fatal("expected the in-flight connect to fail after cancellation")
	}

	// first is closed by the connect failure path, the replacement client is
	// closed by StopServer, and StopServer installs a fresh client that stays
	// open until the next connect.
	second := <-created
	third := <-created
	if got := first.closeCount(); got != 1 {
		t.Fatalf("connecting client closed %d times, want 1", got)
	}
	if got := second.closeCount(); got != 1 {
		t.Fatalf("replacement client closed %d times, want 1", got)
	}
	if got := third.closeCount(); got != 0 {
		t.Fatalf("fresh client must be open until reconnect, closed %d times", got)
	}
	status, ok := mgr.ServerStatusByName("monaco")
	if !ok || status.Connected {
		t.Fatalf("expected stopped server to stay disconnected, got %#v ok=%v", status, ok)
	}

	// The entry must reconnect on the next EnsureServer; release the fresh
	// client's gate so its initialize completes.
	third.releaseGate()
	if _, err := mgr.EnsureServer(context.Background(), "", ""); err != nil {
		t.Fatalf("reconnect after stop: %v", err)
	}
	status, ok = mgr.ServerStatusByName("monaco")
	if !ok || !status.Connected || status.ToolCount != 1 {
		t.Fatalf("expected reconnected server with tools, got %#v ok=%v", status, ok)
	}

	mgr.Close()
	if got := third.closeCount(); got != 1 {
		t.Fatalf("reconnected client closed %d times, want 1", got)
	}
}

// TestManagerConcurrentEnsuresSerializeToOneConnect verifies that concurrent
// connect attempts for the same entry serialize: only one client is created
// and initialized, and the loser reports the connected status.
func TestManagerConcurrentEnsuresSerializeToOneConnect(t *testing.T) {
	mgr, created := gatedManager(t)

	results := make(chan error, 2)
	for range 2 {
		go func() { results <- connectResultErr(mgr.EnsureServer(context.Background(), "", "")) }()
	}

	first := <-created
	<-first.initStart

	// While the first attempt is in flight, no second client may be created.
	select {
	case extra := <-created:
		t.Fatalf("second connect started while the first was in flight: %v", extra)
	case <-time.After(50 * time.Millisecond):
	}

	first.releaseGate()
	for i := 0; i < 2; i++ {
		if err := <-results; err != nil {
			t.Fatalf("EnsureServer %d: %v", i, err)
		}
	}
	if got := first.initCount(); got != 1 {
		t.Fatalf("expected a single initialize, got %d", got)
	}
	select {
	case extra := <-created:
		t.Fatalf("unexpected extra client after connect: %v", extra)
	default:
	}
	status, ok := mgr.ServerStatusByName("monaco")
	if !ok || !status.Connected {
		t.Fatalf("expected connected server, got %#v ok=%v", status, ok)
	}
}

// TestManagerCloseDuringConnectCancelsAndClosesAll verifies that Manager.Close
// aborts an in-flight connection attempt, waits for it to unwind, and closes
// every client the entry touched.
func TestManagerCloseDuringConnectCancelsAndClosesAll(t *testing.T) {
	mgr, created := gatedManager(t)

	connectResult := make(chan error, 1)
	go func() { connectResult <- connectResultErr(mgr.EnsureServer(context.Background(), "", "")) }()
	first := <-created
	<-first.initStart

	closed := make(chan struct{})
	go func() { mgr.Close(); close(closed) }()
	select {
	case <-closed:
	case <-time.After(5 * time.Second):
		t.Fatal("Close blocked on the in-flight connect")
	}
	if err := <-connectResult; err == nil {
		t.Fatal("expected the in-flight connect to fail after Close")
	}

	second := <-created // replacement created by the connect failure path
	if got := first.closeCount(); got != 1 {
		t.Fatalf("connecting client closed %d times, want 1", got)
	}
	if got := second.closeCount(); got != 1 {
		t.Fatalf("replacement client closed %d times, want 1", got)
	}
	status, ok := mgr.ServerStatusByName("monaco")
	if !ok || status.Connected {
		t.Fatalf("expected closed server to stay disconnected, got %#v ok=%v", status, ok)
	}
}

// connectResultErr flattens EnsureServer's (ServerStatus, error) return into
// just the error for channel plumbing in lifecycle tests.
func connectResultErr(_ ServerStatus, err error) error { return err }
