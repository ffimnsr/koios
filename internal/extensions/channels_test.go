package extensions

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/ffimnsr/koios/internal/channels"
)

type stubChannelDispatcher struct {
	dispatch func(context.Context, channels.InboundMessage) (*channels.OutboundMessage, error)
}

func (s stubChannelDispatcher) Dispatch(ctx context.Context, msg channels.InboundMessage) (*channels.OutboundMessage, error) {
	if s.dispatch == nil {
		return nil, nil
	}
	return s.dispatch(ctx, msg)
}

type channelCallerStub struct {
	call     func(context.Context, string, json.RawMessage) (any, error)
	lastName string
	lastArgs map[string]any
}

func (s *channelCallerStub) CallTool(ctx context.Context, fullName string, rawArgs json.RawMessage) (any, error) {
	s.lastName = fullName
	s.lastArgs = nil
	if len(rawArgs) > 0 {
		_ = json.Unmarshal(rawArgs, &s.lastArgs)
	}
	if s.call == nil {
		return nil, nil
	}
	return s.call(ctx, fullName, rawArgs)
}

func TestRegisterChannelsDispatchesInboundAndUsesOutboundReply(t *testing.T) {
	caller := &routeCallerStub{result: `{"http_response":{"status":202,"json":{"accepted":true}},"message":{"conversation_id":"conv-7","sender_id":"user-7","text":"hello","reply_to_message_id":"m-1"}}`}
	manager := channels.NewManager()
	var received channels.InboundMessage
	err := registerChannels(manager, caller, []DiscoveredManifest{{
		Manifest: Manifest{
			ID:           "demo.chat",
			Name:         "chat",
			Capabilities: []string{CapabilityChannels, CapabilityOutboundMessages},
			Channels: []ChannelBinding{{
				Channel: "demochat",
				Method:  "POST",
				Path:    "/v1/channels/demochat/webhook",
				Tool:    "receive_demochat",
			}},
			OutboundMessages: []OutboundMessageBinding{{
				Channel: "demochat",
				Tool:    "send_demochat",
			}},
		},
	}}, nil)
	if err == nil || !strings.Contains(err.Error(), "dispatcher") {
		t.Fatalf("expected nil-dispatcher registration error, got %v", err)
	}

	replyingDispatcher := stubChannelDispatcher{dispatch: func(_ context.Context, msg channels.InboundMessage) (*channels.OutboundMessage, error) {
		received = msg
		return &channels.OutboundMessage{Text: "hi back"}, nil
	}}

	manager = channels.NewManager()
	err = registerChannels(manager, caller, []DiscoveredManifest{{
		Manifest: Manifest{
			ID:           "demo.chat",
			Name:         "chat",
			Capabilities: []string{CapabilityChannels, CapabilityOutboundMessages},
			Channels: []ChannelBinding{{
				Channel: "demochat",
				Method:  "POST",
				Path:    "/v1/channels/demochat/webhook",
				Tool:    "receive_demochat",
			}},
			OutboundMessages: []OutboundMessageBinding{{
				Channel: "demochat",
				Tool:    "send_demochat",
			}},
		},
	}}, replyingDispatcher)
	if err != nil {
		t.Fatalf("RegisterChannels: %v", err)
	}
	mux := http.NewServeMux()
	if err := manager.RegisterRoutes(mux); err != nil {
		t.Fatalf("RegisterRoutes: %v", err)
	}
	caller.result = `{"http_response":{"status":202,"json":{"accepted":true}},"message":{"conversation_id":"conv-7","sender_id":"user-7","text":"hello","reply_to_message_id":"m-1"}}`
	req := httptest.NewRequest(http.MethodPost, "/v1/channels/demochat/webhook", strings.NewReader("{}"))
	res := httptest.NewRecorder()
	mux.ServeHTTP(res, req)
	if res.Code != http.StatusAccepted {
		t.Fatalf("status = %d body=%s", res.Code, res.Body.String())
	}
	if received.Channel != "demochat" || received.PeerID != "demochat:conv-7" || received.Text != "hello" {
		t.Fatalf("unexpected inbound dispatch: %#v", received)
	}
	if caller.lastName != "mcp_plug_demo_chat__send_demochat" {
		t.Fatalf("expected reply send tool call, got %q", caller.lastName)
	}
	target, _ := caller.lastArgs["target"].(map[string]any)
	message, _ := caller.lastArgs["message"].(map[string]any)
	if target["conversation_id"] != "conv-7" || message["text"] != "hi back" || message["reply_to_message_id"] != "m-1" {
		t.Fatalf("unexpected outbound reply payload: %#v", caller.lastArgs)
	}
	if !strings.Contains(res.Body.String(), `"accepted":true`) {
		t.Fatalf("unexpected response body: %s", res.Body.String())
	}
}

func TestRegisterChannelsLifecycleHooks(t *testing.T) {
	caller := &routeCallerStub{}
	manager := channels.NewManager()
	dispatcher := stubChannelDispatcher{dispatch: func(context.Context, channels.InboundMessage) (*channels.OutboundMessage, error) { return nil, nil }}
	err := registerChannels(manager, caller, []DiscoveredManifest{{
		Manifest: Manifest{
			ID:           "demo.lifecycle",
			Capabilities: []string{CapabilityChannels},
			Channels: []ChannelBinding{{
				Channel:      "webhook-demo",
				Method:       "POST",
				Path:         "/hook/demo",
				Tool:         "receive_demo",
				StartTool:    "start_demo",
				ShutdownTool: "stop_demo",
			}},
		},
	}}, dispatcher)
	if err != nil {
		t.Fatalf("RegisterChannels: %v", err)
	}
	if err := manager.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if caller.lastName != "mcp_plug_demo_lifecycle__start_demo" {
		t.Fatalf("unexpected start tool call: %q", caller.lastName)
	}
	if err := manager.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	if caller.lastName != "mcp_plug_demo_lifecycle__stop_demo" {
		t.Fatalf("unexpected shutdown tool call: %q", caller.lastName)
	}
	channelArgs, _ := caller.lastArgs["channel"].(map[string]any)
	lifecycleArgs, _ := channelArgs["lifecycle"].(map[string]any)
	if lifecycleArgs["phase"] != "shutdown" {
		t.Fatalf("unexpected lifecycle payload: %#v", caller.lastArgs)
	}
}

func TestRegisterChannelsSuppressesReplyWhenEnvelopeRequestsNoReply(t *testing.T) {
	caller := &routeCallerStub{result: `{"http_response":{"status":204},"message":{"conversation_id":"conv-7","sender_id":"user-7","text":"hello","no_reply":true}}`}
	manager := channels.NewManager()
	replies := 0
	err := registerChannels(manager, caller, []DiscoveredManifest{{
		Manifest: Manifest{
			ID:           "demo.chat",
			Name:         "chat",
			Capabilities: []string{CapabilityChannels, CapabilityOutboundMessages},
			Channels: []ChannelBinding{{
				Channel: "demochat",
				Method:  "POST",
				Path:    "/v1/channels/demochat/webhook",
				Tool:    "receive_demochat",
			}},
			OutboundMessages: []OutboundMessageBinding{{
				Channel: "demochat",
				Tool:    "send_demochat",
			}},
		},
	}}, stubChannelDispatcher{dispatch: func(_ context.Context, msg channels.InboundMessage) (*channels.OutboundMessage, error) {
		return &channels.OutboundMessage{Text: "reply that should be suppressed"}, nil
	}})
	if err != nil {
		t.Fatalf("registerChannels: %v", err)
	}
	mux := http.NewServeMux()
	if err := manager.RegisterRoutes(mux); err != nil {
		t.Fatalf("RegisterRoutes: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/v1/channels/demochat/webhook", strings.NewReader("{}"))
	res := httptest.NewRecorder()
	mux.ServeHTTP(res, req)
	if res.Code != http.StatusNoContent {
		t.Fatalf("status = %d body=%s", res.Code, res.Body.String())
	}
	if caller.lastName == "mcp_plug_demo_chat__send_demochat" {
		t.Fatalf("expected no outbound reply tool call, got %#v", caller.lastArgs)
	}
	_ = replies
}

func TestRegisterChannelsStartsPollingLoopAndPersistsPollState(t *testing.T) {
	caller := &channelCallerStub{}
	manager := channels.NewManager()
	var received []channels.InboundMessage
	pollCalls := 0
	caller.call = func(_ context.Context, fullName string, rawArgs json.RawMessage) (any, error) {
		if strings.HasSuffix(fullName, "poll_demochat") {
			pollCalls++
			var args map[string]any
			_ = json.Unmarshal(rawArgs, &args)
			channelArgs, _ := args["channel"].(map[string]any)
			pollArgs, _ := channelArgs["poll"].(map[string]any)
			if pollCalls == 1 {
				if pollArgs["state"] != nil {
					t.Fatalf("expected first poll to have nil state, got %#v", pollArgs)
				}
				return `{"message":{"conversation_id":"conv-1","sender_id":"user-1","text":"hello from poll","no_reply":true},"poll_state":{"cursor":"next-1"}}`, nil
			}
			if pollCalls == 2 {
				state, _ := pollArgs["state"].(map[string]any)
				if state["cursor"] != "next-1" {
					t.Fatalf("expected second poll to receive prior state, got %#v", pollArgs)
				}
			}
			return `{"poll_state":{"cursor":"next-2"}}`, nil
		}
		return nil, nil
	}
	err := registerChannels(manager, caller, []DiscoveredManifest{{
		Manifest: Manifest{
			ID:           "demo.poll",
			Name:         "poll",
			Capabilities: []string{CapabilityChannels},
			Channels: []ChannelBinding{{
				Channel:      "pollchat",
				PollTool:     "poll_demochat",
				PollInterval: "10ms",
			}},
		},
	}}, stubChannelDispatcher{dispatch: func(_ context.Context, msg channels.InboundMessage) (*channels.OutboundMessage, error) {
		received = append(received, msg)
		return &channels.OutboundMessage{Text: "suppressed"}, nil
	}})
	if err != nil {
		t.Fatalf("registerChannels: %v", err)
	}
	start := time.Now()
	if err := manager.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() {
		_ = manager.Shutdown(context.Background())
	})
	for time.Since(start) < 300*time.Millisecond {
		if pollCalls >= 2 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if pollCalls < 2 {
		t.Fatalf("expected polling loop to run at least twice, got %d", pollCalls)
	}
	if len(received) != 1 || received[0].Channel != "pollchat" || received[0].Text != "hello from poll" {
		t.Fatalf("unexpected polled dispatches: %#v", received)
	}
	if caller.lastName != "mcp_plug_demo_poll__poll_demochat" {
		t.Fatalf("unexpected last poll tool name: %q", caller.lastName)
	}
}

func TestLoadManifestAcceptsChannelBindings(t *testing.T) {
	root := t.TempDir()
	path := root + "/" + ManifestFileName
	content := []byte(`api_version = "koios.extension/v1"
kind = "mcp_server"
id = "demo.channels"
name = "channels"
capabilities = ["channels"]

[mcp]
transport = "http"
url = "https://example.test/mcp"

[[channels]]
channel = "demochat"
method = "post"
path = "/v1/channels/demochat/webhook"
tool = "receive_demochat"
start_tool = "start_demochat"
shutdown_tool = "stop_demochat"
`)
	if err := os.WriteFile(path, content, 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	manifest, err := LoadManifest(path)
	if err != nil {
		t.Fatalf("LoadManifest: %v", err)
	}
	if len(manifest.Channels) != 1 {
		t.Fatalf("expected 1 channel binding, got %#v", manifest.Channels)
	}
	if manifest.Channels[0].Channel != "demochat" || manifest.Channels[0].Method != "POST" || manifest.Channels[0].Path != "/v1/channels/demochat/webhook" {
		t.Fatalf("unexpected channel binding normalization: %#v", manifest.Channels[0])
	}
	servers, err := MCPServers([]DiscoveredManifest{{Manifest: manifest, Path: path}})
	if err != nil {
		t.Fatalf("MCPServers: %v", err)
	}
	if len(servers) != 1 || !servers[0].HideTools {
		t.Fatalf("expected channels-only extension server to stay internal, got %#v", servers)
	}
}

func TestLoadManifestAcceptsPollingChannelBindings(t *testing.T) {
	root := t.TempDir()
	path := root + "/" + ManifestFileName
	content := []byte(`api_version = "koios.extension/v1"
kind = "mcp_server"
id = "demo.poll"
name = "poll"
capabilities = ["channels"]

[mcp]
transport = "http"
url = "https://example.test/mcp"

[[channels]]
channel = "demochat"
poll_tool = "poll_demochat"
poll_interval = "5s"
`)
	if err := os.WriteFile(path, content, 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	manifest, err := LoadManifest(path)
	if err != nil {
		t.Fatalf("LoadManifest: %v", err)
	}
	if len(manifest.Channels) != 1 || manifest.Channels[0].PollTool != "poll_demochat" || manifest.Channels[0].PollInterval != "5s" {
		t.Fatalf("unexpected poll channel binding: %#v", manifest.Channels)
	}
}

func TestLoadManifestRejectsChannelsWithoutCapability(t *testing.T) {
	root := t.TempDir()
	path := root + "/" + ManifestFileName
	content := []byte(`api_version = "koios.extension/v1"
kind = "mcp_server"
id = "demo.invalid-channels"
name = "invalid-channels"
capabilities = ["tools"]

[mcp]
transport = "http"
url = "https://example.test/mcp"

[[channels]]
channel = "demochat"
method = "POST"
path = "/v1/channels/demochat/webhook"
tool = "receive_demochat"
`)
	if err := os.WriteFile(path, content, 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if _, err := LoadManifest(path); err == nil {
		t.Fatal("expected channel capability validation error, got nil")
	}
}

func TestDecodeChannelToolEnvelopeAcceptsMapResults(t *testing.T) {
	envelope, response, plainText, err := decodeChannelToolEnvelope(map[string]any{
		"message": map[string]any{"conversation_id": "conv-1", "text": "hello"},
	})
	if err != nil {
		t.Fatalf("decodeChannelToolEnvelope: %v", err)
	}
	if response != nil || plainText != "" || envelope.Message == nil || envelope.Message.ConversationID != "conv-1" {
		t.Fatalf("unexpected decoded envelope: %#v %#v %q", envelope, response, plainText)
	}
	raw, _ := json.Marshal(envelope.Message)
	if !strings.Contains(string(raw), "conv-1") {
		t.Fatalf("expected decoded message to round-trip, got %s", raw)
	}
}
