package extensions

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/ffimnsr/koios/internal/ops"
)

type hookToolCall struct {
	fullName string
	payload  map[string]any
}

type stubHookToolCaller struct {
	calls   []hookToolCall
	results map[string]any
	err     error
}

func (s *stubHookToolCaller) CallTool(_ context.Context, fullName string, rawArgs json.RawMessage) (any, error) {
	if s.err != nil {
		return nil, s.err
	}
	var payload map[string]any
	if err := json.Unmarshal(rawArgs, &payload); err != nil {
		return nil, err
	}
	s.calls = append(s.calls, hookToolCall{fullName: fullName, payload: payload})
	if s.results == nil {
		return "", nil
	}
	return s.results[fullName], nil
}

func TestRegisterHooksUsesNamespacedPluginToolCalls(t *testing.T) {
	manager := ops.NewManager(time.Second, true)
	caller := &stubHookToolCaller{results: map[string]any{
		"mcp_plug_demo_hooks__rewrite_request": `{"data":{"model":"model-b"}}`,
	}}
	manifests := []DiscoveredManifest{{
		Manifest: Manifest{
			ID:           "demo.hooks",
			Name:         "hook runner",
			Capabilities: []string{CapabilityHooks},
			Hooks: []HookBinding{
				{Name: "rewrite", Event: "before_llm", Mode: HookModeIntercept, Tool: "rewrite_request", Priority: 5},
				{Name: "audit", Event: "after_message", Tool: "audit_event", Priority: 10},
			},
		},
		Path: "demo/koios-extension.toml",
	}}

	if err := RegisterHooks(manager, caller, manifests); err != nil {
		t.Fatalf("RegisterHooks: %v", err)
	}

	rewritten, err := manager.Intercept(context.Background(), ops.Event{
		Name:      ops.HookBeforeLLM,
		Timestamp: time.Now().UTC(),
		Data:      map[string]any{"model": "model-a"},
	})
	if err != nil {
		t.Fatalf("Intercept: %v", err)
	}
	if got := rewritten.Data["model"]; got != "model-b" {
		t.Fatalf("expected intercept rewrite, got %#v", rewritten.Data)
	}
	if err := manager.Emit(context.Background(), ops.Event{
		Name:      ops.HookAfterMessage,
		Timestamp: time.Now().UTC(),
		PeerID:    "peer-1",
	}); err != nil {
		t.Fatalf("Emit: %v", err)
	}
	if len(caller.calls) != 2 {
		t.Fatalf("expected 2 hook tool calls, got %#v", caller.calls)
	}
	if caller.calls[0].fullName != "mcp_plug_demo_hooks__rewrite_request" {
		t.Fatalf("unexpected intercept tool name: %#v", caller.calls[0])
	}
	if caller.calls[1].fullName != "mcp_plug_demo_hooks__audit_event" {
		t.Fatalf("unexpected emit tool name: %#v", caller.calls[1])
	}
	hookMeta, _ := caller.calls[0].payload["hook"].(map[string]any)
	if hookMeta["binding"] != "demo.hooks/rewrite" {
		t.Fatalf("expected namespaced binding id, got %#v", hookMeta)
	}
}

func TestHookBindingsSkipsDisabledBindings(t *testing.T) {
	bindings, err := HookBindings([]DiscoveredManifest{{
		Manifest: Manifest{
			ID:           "demo.hooks",
			Capabilities: []string{CapabilityHooks},
			Hooks: []HookBinding{
				{Event: "after_message", Tool: "enabled"},
				{Event: "after_message", Tool: "disabled", Enabled: boolPtr(false)},
			},
		},
	}})
	if err != nil {
		t.Fatalf("HookBindings: %v", err)
	}
	if len(bindings) != 1 || bindings[0].Binding.Tool != "enabled" {
		t.Fatalf("unexpected enabled hook bindings: %#v", bindings)
	}
}
