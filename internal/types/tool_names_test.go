package types

import "testing"

func TestProviderToolNameCodec(t *testing.T) {
	tests := []struct {
		name string
		wire string
	}{
		{name: "tool.list", wire: "tool_x2e_list"},
		{name: "workspace.read", wire: "workspace_x2e_read"},
		{name: "tool_x2e_list", wire: "tool_x5f_x2e_list"},
		{name: "mcp__monaco__tools/list", wire: "mcp__monaco__tools_x2f_list"},
		{name: "already_safe-1", wire: "already_safe-1"},
	}
	for _, tt := range tests {
		if got := EncodeProviderToolName(tt.name); got != tt.wire {
			t.Fatalf("EncodeProviderToolName(%q) = %q, want %q", tt.name, got, tt.wire)
		}
		if got := DecodeProviderToolName(tt.wire); got != tt.name {
			t.Fatalf("DecodeProviderToolName(%q) = %q, want %q", tt.wire, got, tt.name)
		}
	}
}
