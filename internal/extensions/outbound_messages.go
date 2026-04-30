package extensions

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/ffimnsr/koios/internal/channels"
	"github.com/ffimnsr/koios/internal/mcp"
)

type OutboundMessageToolCaller interface {
	CallTool(ctx context.Context, fullName string, rawArgs json.RawMessage) (any, error)
}

type DiscoveredOutboundMessageBinding struct {
	Manifest DiscoveredManifest
	Binding  OutboundMessageBinding
}

func OutboundMessageBindings(manifests []DiscoveredManifest) ([]DiscoveredOutboundMessageBinding, error) {
	bindings := make([]DiscoveredOutboundMessageBinding, 0)
	for _, manifest := range manifests {
		if !manifest.Enabled() || !manifest.HasCapability(CapabilityOutboundMessages) {
			continue
		}
		for _, binding := range manifest.Manifest.OutboundMessages {
			if binding.Enabled != nil && !*binding.Enabled {
				continue
			}
			if err := validateOutboundMessageBinding(binding, manifest.Path); err != nil {
				return nil, err
			}
			bindings = append(bindings, DiscoveredOutboundMessageBinding{
				Manifest: manifest,
				Binding:  binding,
			})
		}
	}
	return bindings, nil
}

func RegisterOutboundSenders(manager *channels.Manager, caller OutboundMessageToolCaller, manifests []DiscoveredManifest) error {
	bindings, err := OutboundMessageBindings(manifests)
	if err != nil {
		return err
	}
	if len(bindings) == 0 {
		return nil
	}
	if manager == nil {
		return fmt.Errorf("extension outbound message bindings require a channel manager")
	}
	if caller == nil {
		return fmt.Errorf("extension outbound message bindings require an MCP tool caller")
	}
	seen := make(map[string]string, len(bindings))
	for _, discovered := range bindings {
		channelID := strings.TrimSpace(discovered.Binding.Channel)
		if owner, exists := seen[channelID]; exists {
			return fmt.Errorf("extension outbound channel %q is declared by both %q and %q", channelID, owner, discovered.Manifest.Manifest.ID)
		}
		if manager.HasChannel(channelID) {
			return fmt.Errorf("extension outbound channel %q conflicts with an existing channel registration", channelID)
		}
		seen[channelID] = discovered.Manifest.Manifest.ID
		manager.Register(&extensionOutboundSender{binding: discovered, caller: caller})
	}
	return nil
}

type extensionOutboundSender struct {
	binding DiscoveredOutboundMessageBinding
	caller  OutboundMessageToolCaller
}

func (s *extensionOutboundSender) ID() string { return s.binding.Binding.Channel }

func (s *extensionOutboundSender) Routes() []channels.Route { return nil }

func (s *extensionOutboundSender) Start(context.Context) error { return nil }

func (s *extensionOutboundSender) Shutdown(context.Context) error { return nil }

func (s *extensionOutboundSender) SendMessage(ctx context.Context, target channels.OutboundTarget, msg channels.OutboundMessage) (*channels.OutboundReceipt, error) {
	result, err := s.caller.CallTool(ctx, s.binding.fullToolName(), s.binding.payload(target, msg))
	if err != nil {
		return nil, err
	}
	return decodeOutboundMessageToolResult(result, &channels.OutboundReceipt{
		Channel:        s.ID(),
		SubjectID:      strings.TrimSpace(target.SubjectID),
		ConversationID: strings.TrimSpace(target.ConversationID),
		ThreadID:       strings.TrimSpace(target.ThreadID),
		ChunkCount:     1,
	})
}

func (d DiscoveredOutboundMessageBinding) fullToolName() string {
	return mcp.PluginToolPrefix(d.Manifest.Manifest.ID) + d.Binding.Tool
}

func (d DiscoveredOutboundMessageBinding) bindingID() string {
	name := strings.TrimSpace(d.Binding.Name)
	if name == "" {
		name = strings.TrimSpace(d.Binding.Channel) + ":" + strings.TrimSpace(d.Binding.Tool)
	}
	return d.Manifest.Manifest.ID + "/" + name
}

func (d DiscoveredOutboundMessageBinding) payload(target channels.OutboundTarget, msg channels.OutboundMessage) json.RawMessage {
	body, _ := json.Marshal(map[string]any{
		"outbound_message": map[string]any{
			"binding":        d.bindingID(),
			"channel":        d.Binding.Channel,
			"extension_id":   d.Manifest.Manifest.ID,
			"extension_name": d.Manifest.Manifest.Name,
		},
		"target":  target,
		"message": msg,
	})
	return body
}

func decodeOutboundMessageToolResult(result any, fallback *channels.OutboundReceipt) (*channels.OutboundReceipt, error) {
	data, err := decodeHookToolResult(result)
	if err != nil {
		if result == nil {
			return fallback, nil
		}
		if value, ok := result.(map[string]any); ok {
			encoded, marshalErr := json.Marshal(value)
			if marshalErr != nil {
				return nil, marshalErr
			}
			data = encoded
		} else {
			return nil, err
		}
	}
	if len(data) == 0 {
		return fallback, nil
	}
	var envelope struct {
		Receipt *channels.OutboundReceipt `json:"receipt"`
	}
	if err := json.Unmarshal(data, &envelope); err == nil && envelope.Receipt != nil {
		return envelope.Receipt, nil
	}
	var receipt channels.OutboundReceipt
	if err := json.Unmarshal(data, &receipt); err != nil {
		return nil, fmt.Errorf("decode outbound message result: %w", err)
	}
	if receipt.Channel == "" && receipt.SubjectID == "" && receipt.ConversationID == "" && receipt.ThreadID == "" && receipt.ChunkCount == 0 && len(receipt.Deliveries) == 0 && len(receipt.Metadata) == 0 {
		return fallback, nil
	}
	return &receipt, nil
}
