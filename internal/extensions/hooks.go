package extensions

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/ffimnsr/koios/internal/mcp"
	"github.com/ffimnsr/koios/internal/ops"
)

type HookToolCaller interface {
	CallTool(ctx context.Context, fullName string, rawArgs json.RawMessage) (any, error)
}

type DiscoveredHookBinding struct {
	Manifest DiscoveredManifest
	Binding  HookBinding
	Event    ops.HookName
}

func HookBindings(manifests []DiscoveredManifest) ([]DiscoveredHookBinding, error) {
	bindings := make([]DiscoveredHookBinding, 0)
	for _, manifest := range manifests {
		if !manifest.Enabled() || !manifest.HasCapability(CapabilityHooks) {
			continue
		}
		for _, binding := range manifest.Manifest.Hooks {
			if binding.Enabled != nil && !*binding.Enabled {
				continue
			}
			event, ok := ops.ParseHookName(binding.Event)
			if !ok {
				return nil, fmt.Errorf("extension %q: unsupported hook event %q", manifest.Manifest.ID, binding.Event)
			}
			bindings = append(bindings, DiscoveredHookBinding{
				Manifest: manifest,
				Binding:  binding,
				Event:    event,
			})
		}
	}
	return bindings, nil
}

func RegisterHooks(manager *ops.Manager, caller HookToolCaller, manifests []DiscoveredManifest) error {
	bindings, err := HookBindings(manifests)
	if err != nil {
		return err
	}
	if len(bindings) == 0 {
		return nil
	}
	if manager == nil {
		return fmt.Errorf("extension hooks require a hook manager")
	}
	if caller == nil {
		return fmt.Errorf("extension hooks require an MCP tool caller")
	}
	for _, discovered := range bindings {
		discovered := discovered
		if discovered.Binding.Mode == HookModeIntercept {
			manager.RegisterInterceptor(discovered.Event, discovered.Binding.Priority, func(ctx context.Context, ev ops.Event) (ops.Event, error) {
				result, err := caller.CallTool(ctx, discovered.fullToolName(), discovered.payload(ev))
				if err != nil {
					return ev, err
				}
				return decodeInterceptEvent(result, ev)
			})
			continue
		}
		manager.Register(discovered.Event, discovered.Binding.Priority, func(ctx context.Context, ev ops.Event) error {
			_, err := caller.CallTool(ctx, discovered.fullToolName(), discovered.payload(ev))
			return err
		})
	}
	return nil
}

func (d DiscoveredHookBinding) fullToolName() string {
	return mcp.PluginToolPrefix(d.Manifest.Manifest.ID) + d.Binding.Tool
}

func (d DiscoveredHookBinding) bindingID() string {
	name := strings.TrimSpace(d.Binding.Name)
	if name == "" {
		name = strings.TrimSpace(d.Binding.Event) + ":" + strings.TrimSpace(d.Binding.Tool)
	}
	return d.Manifest.Manifest.ID + "/" + name
}

func (d DiscoveredHookBinding) payload(ev ops.Event) json.RawMessage {
	body, _ := json.Marshal(map[string]any{
		"hook": map[string]any{
			"binding":        d.bindingID(),
			"event":          string(d.Event),
			"mode":           d.Binding.Mode,
			"extension_id":   d.Manifest.Manifest.ID,
			"extension_name": d.Manifest.Manifest.Name,
		},
		"event": ev,
	})
	return body
}

func decodeInterceptEvent(result any, original ops.Event) (ops.Event, error) {
	data, err := decodeHookToolResult(result)
	if err != nil {
		return original, err
	}
	if len(data) == 0 {
		return original, nil
	}
	var out ops.Event
	if err := json.Unmarshal(data, &out); err != nil {
		return original, fmt.Errorf("decode hook interceptor result: %w", err)
	}
	if out.Name == "" {
		out.Name = original.Name
	}
	if out.Timestamp.IsZero() {
		out.Timestamp = original.Timestamp
	}
	if out.PeerID == "" {
		out.PeerID = original.PeerID
	}
	if out.SessionKey == "" {
		out.SessionKey = original.SessionKey
	}
	if out.Data == nil {
		out.Data = original.Data
	}
	return out, nil
}

func decodeHookToolResult(result any) ([]byte, error) {
	switch value := result.(type) {
	case nil:
		return nil, nil
	case string:
		trimmed := strings.TrimSpace(value)
		if trimmed == "" {
			return nil, nil
		}
		return []byte(trimmed), nil
	case []byte:
		return value, nil
	case json.RawMessage:
		return value, nil
	default:
		return nil, fmt.Errorf("unsupported hook tool result type %T", result)
	}
}
