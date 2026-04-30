package extensions

import (
	"context"
	"testing"

	"github.com/ffimnsr/koios/internal/channels"
	"github.com/ffimnsr/koios/internal/mcp"
)

func TestRegisterOutboundSendersCallsScopedTool(t *testing.T) {
	caller := &routeCallerStub{result: `{"receipt":{"conversation_id":"conv-123","chunk_count":2,"deliveries":[{"message_id":"m1","chunk_index":0},{"message_id":"m2","chunk_index":1}]}}`}
	manager := channels.NewManager()
	err := RegisterOutboundSenders(manager, caller, []DiscoveredManifest{{
		Manifest: Manifest{
			ID:           "demo.signal",
			Name:         "signal-sender",
			Capabilities: []string{CapabilityOutboundMessages},
			OutboundMessages: []OutboundMessageBinding{{
				Channel: "signal",
				Tool:    "send_signal",
			}},
		},
	}})
	if err != nil {
		t.Fatalf("RegisterOutboundSenders: %v", err)
	}
	if !manager.HasChannel("signal") {
		t.Fatal("expected outbound sender to be registered")
	}
	receipt, err := manager.SendMessage(context.Background(), "signal", channels.OutboundTarget{SubjectID: "user-7"}, channels.OutboundMessage{Text: "hello"})
	if err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
	if caller.lastName != mcp.PluginToolPrefix("demo.signal")+"send_signal" {
		t.Fatalf("unexpected tool name: %q", caller.lastName)
	}
	outboundArgs, _ := caller.lastArgs["outbound_message"].(map[string]any)
	if outboundArgs["channel"] != "signal" {
		t.Fatalf("expected channel payload, got %#v", caller.lastArgs)
	}
	if receipt.SubjectID != "user-7" || receipt.ConversationID != "conv-123" || len(receipt.Deliveries) != 2 {
		t.Fatalf("unexpected outbound receipt: %#v", receipt)
	}
	if receipt.Deliveries[0].MessageID != "m1" || receipt.Deliveries[1].MessageID != "m2" {
		t.Fatalf("unexpected delivery data: %#v", receipt.Deliveries)
	}
}

func TestRegisterOutboundSendersRejectsDuplicateChannelIDs(t *testing.T) {
	manager := channels.NewManager()
	err := RegisterOutboundSenders(manager, &routeCallerStub{}, []DiscoveredManifest{
		{Manifest: Manifest{ID: "demo.one", Capabilities: []string{CapabilityOutboundMessages}, OutboundMessages: []OutboundMessageBinding{{Channel: "signal", Tool: "send_one"}}}},
		{Manifest: Manifest{ID: "demo.two", Capabilities: []string{CapabilityOutboundMessages}, OutboundMessages: []OutboundMessageBinding{{Channel: "signal", Tool: "send_two"}}}},
	})
	if err == nil {
		t.Fatal("expected duplicate outbound channel error")
	}
}
