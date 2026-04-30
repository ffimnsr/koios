package channels

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/ffimnsr/koios/internal/session"
)

type stubChannel struct {
	id          string
	routes      []Route
	startErr    error
	shutdownErr error
	started     bool
	stopped     bool
}

func (s *stubChannel) ID() string      { return s.id }
func (s *stubChannel) Routes() []Route { return append([]Route(nil), s.routes...) }
func (s *stubChannel) Start(context.Context) error {
	s.started = true
	return s.startErr
}
func (s *stubChannel) Shutdown(context.Context) error {
	s.stopped = true
	return s.shutdownErr
}

func TestManagerRegisterRoutesRejectsDuplicates(t *testing.T) {
	mgr := NewManager(
		&stubChannel{id: "a", routes: []Route{{Pattern: "POST /same", Handler: http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})}}},
		&stubChannel{id: "b", routes: []Route{{Pattern: "POST /same", Handler: http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})}}},
	)
	err := mgr.RegisterRoutes(http.NewServeMux())
	if err == nil {
		t.Fatal("expected duplicate route error")
	}
}

func TestManagerStartRollsBackStartedChannels(t *testing.T) {
	first := &stubChannel{id: "a"}
	second := &stubChannel{id: "b", startErr: errors.New("boom")}
	mgr := NewManager(first, second)
	err := mgr.Start(context.Background())
	if err == nil {
		t.Fatal("expected start error")
	}
	if !first.started {
		t.Fatal("expected first channel to start")
	}
	if !first.stopped {
		t.Fatal("expected first channel to be stopped after rollback")
	}
}

func TestManagerRegisterRoutesMountsHandlers(t *testing.T) {
	mgr := NewManager(&stubChannel{id: "telegram", routes: []Route{{
		Pattern: "POST /v1/channels/test",
		Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusNoContent)
		}),
	}}})
	mux := http.NewServeMux()
	if err := mgr.RegisterRoutes(mux); err != nil {
		t.Fatalf("RegisterRoutes: %v", err)
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/channels/test", nil)
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusNoContent)
	}
}

func TestChannelInboxSessionKey(t *testing.T) {
	if got, want := ChannelInboxSessionKey("default", "telegram:123:9"), "default::channel::telegram:123:9"; got != want {
		t.Fatalf("ChannelInboxSessionKey() = %q, want %q", got, want)
	}
	if got := ChannelInboxSessionKey("", "telegram:123"); got != "" {
		t.Fatalf("empty peer id should return empty key, got %q", got)
	}
}

func TestSessionKeyOwnerPeer(t *testing.T) {
	if got, want := SessionKeyOwnerPeer("default::channel::telegram:123"), "default"; got != want {
		t.Fatalf("SessionKeyOwnerPeer() = %q, want %q", got, want)
	}
	if got := SessionKeyOwnerPeer("telegram:123"); got != "" {
		t.Fatalf("expected empty owner for unscoped key, got %q", got)
	}
}

func TestDispatcherActivationCommandUsesSessionPolicy(t *testing.T) {
	store := session.NewWithOptions(session.Options{MaxMessages: 10})
	d := &Dispatcher{
		SessionPolicy: store.Policy,
		PatchPolicy:   store.PatchPolicy,
	}
	reply, err := d.handleActivationCommand(InboundMessage{
		PeerID:     "owner",
		SessionKey: "owner::channel::telegram:123",
		Text:       "/activation mention",
		Metadata: map[string]any{
			"conversation_peer_id": "telegram:123",
		},
	}, "mention")
	if err != nil {
		t.Fatalf("handleActivationCommand: %v", err)
	}
	if reply == nil || reply.Text != "Session activation: mention" {
		t.Fatalf("unexpected reply: %#v", reply)
	}
	if got := store.Policy("owner::channel::telegram:123").ActivationMode; got != "mention" {
		t.Fatalf("activation mode = %q, want mention", got)
	}

	reply, err = d.handleActivationCommand(InboundMessage{PeerID: "owner"}, "group always")
	if err != nil {
		t.Fatalf("handleActivationCommand group: %v", err)
	}
	if reply == nil || reply.Text != "Group activation default: always" {
		t.Fatalf("unexpected group reply: %#v", reply)
	}
	if got := store.Policy("owner").GroupActivationMode; got != "always" {
		t.Fatalf("group activation = %q, want always", got)
	}
}

func TestNormalizeStreamQueueMode(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{name: "default", in: "", want: StreamQueueModeBurst},
		{name: "burst", in: "burst", want: StreamQueueModeBurst},
		{name: "throttle", in: "THROTTLE", want: StreamQueueModeThrottle},
		{name: "invalid passes through", in: "later", want: "later"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := NormalizeStreamQueueMode(tc.in); got != tc.want {
				t.Fatalf("NormalizeStreamQueueMode(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestSendChunkedThrottleSkipsInitialDelay(t *testing.T) {
	ctx := context.Background()
	started := time.Now()
	var offsets []time.Duration
	err := SendChunked(ctx, []string{"one", "two", "three"}, StreamQueuePolicy{
		Mode:     StreamQueueModeThrottle,
		Throttle: 20 * time.Millisecond,
	}, func(_ int, _ string) error {
		offsets = append(offsets, time.Since(started))
		return nil
	})
	if err != nil {
		t.Fatalf("SendChunked: %v", err)
	}
	if len(offsets) != 3 {
		t.Fatalf("send count = %d, want 3", len(offsets))
	}
	if offsets[0] >= 15*time.Millisecond {
		t.Fatalf("expected first chunk without throttle delay, got %s", offsets[0])
	}
	if offsets[1] < 15*time.Millisecond {
		t.Fatalf("expected second chunk to be delayed, got %s", offsets[1])
	}
	if offsets[2] < 35*time.Millisecond {
		t.Fatalf("expected third chunk to include two intervals, got %s", offsets[2])
	}
}
