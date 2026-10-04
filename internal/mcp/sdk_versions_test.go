package mcp

import (
	"context"
	"testing"

	"github.com/ffimnsr/koios/internal/config"
)

// TestSDKClientNegotiatesLegacyProtocolVersions verifies the multi-version
// story for servers that only speak protocol revisions older than 2026-07-28:
// server/discover advertises the older versions, the SDK client falls back to
// the legacy initialize handshake, and the session works end to end (negotiated
// version reported, tools listed and callable) at every revision the SDK
// supports down to the 2024-11-05 floor.
func TestSDKClientNegotiatesLegacyProtocolVersions(t *testing.T) {
	for _, tc := range []struct {
		name     string
		versions []string
	}{
		{name: "2025-11-25", versions: []string{"2025-11-25"}},
		{name: "2025-06-18", versions: []string{"2025-06-18"}},
		{name: "2025-03-26", versions: []string{"2025-03-26"}},
		{name: "2024-11-05", versions: []string{"2024-11-05"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := newMinimalMCPServerWithVersions(t, tc.versions)
			client := NewSDKClient(configForMinimalServer(server))
			t.Cleanup(func() { _ = client.Close() })
			initializeFixture(t, client)

			discover, err := client.Discover(context.Background())
			if err != nil {
				t.Fatalf("Discover: %v", err)
			}
			if discover.ProtocolVersion != tc.versions[0] {
				t.Fatalf("negotiated protocol version = %q, want %q", discover.ProtocolVersion, tc.versions[0])
			}

			tools, err := client.ListTools(context.Background())
			if err != nil {
				t.Fatalf("ListTools: %v", err)
			}
			if len(tools) != 2 || tools[0].Name != "quote" || tools[1].Name != "approve_flow" {
				t.Fatalf("unexpected tools: %#v", tools)
			}
			result, err := client.CallTool(context.Background(), "quote", map[string]any{})
			if err != nil {
				t.Fatalf("CallTool: %v", err)
			}
			if result == nil || len(result.Content) != 1 || result.Content[0].Text != "ok" {
				t.Fatalf("unexpected call result: %#v", result)
			}

			// Every handshake stage must have been exercised: the modern
			// discover probe first, then the legacy initialize + initialized
			// notification, then the request paths.
			for _, want := range []string{"server/discover", "initialize", "notifications/initialized", "tools/list", "tools/call"} {
				if len(server.recordsForRPC(want)) == 0 {
					t.Fatalf("no %s request recorded", want)
				}
			}
		})
	}
}

// TestSDKClientNegotiatesNewestOverlapOnDiscover verifies that a server
// offering both the modern and an older revision negotiates the newest
// mutually supported version over server/discover without the legacy handshake.
func TestSDKClientNegotiatesNewestOverlapOnDiscover(t *testing.T) {
	server := newMinimalMCPServerWithVersions(t, []string{ProtocolVersion2026, "2025-06-18"})
	client := NewSDKClient(configForMinimalServer(server))
	t.Cleanup(func() { _ = client.Close() })
	initializeFixture(t, client)

	discover, err := client.Discover(context.Background())
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if discover.ProtocolVersion != ProtocolVersion2026 {
		t.Fatalf("negotiated protocol version = %q, want %q", discover.ProtocolVersion, ProtocolVersion2026)
	}
	if got := server.recordsForRPC("initialize"); len(got) != 0 {
		t.Fatalf("expected no legacy initialize handshake for a modern server, got %d request(s)", len(got))
	}
}

// TestSDKClientFallsBackWhenDiscoverUnsupported verifies the negotiation path
// for servers that predate the stateless server/discover RPC: the client's
// discover probe is answered with method not found, and the session is
// established through the legacy initialize handshake at the server's newest
// supported revision.
func TestSDKClientFallsBackWhenDiscoverUnsupported(t *testing.T) {
	const negotiated = "2024-11-05"
	server := newMinimalMCPServerLegacyOnly(t, []string{negotiated})
	client := NewSDKClient(configForMinimalServer(server))
	t.Cleanup(func() { _ = client.Close() })
	initializeFixture(t, client)

	discover, err := client.Discover(context.Background())
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if discover.ProtocolVersion != negotiated {
		t.Fatalf("negotiated protocol version = %q, want %q", discover.ProtocolVersion, negotiated)
	}
	if tools, err := client.ListTools(context.Background()); err != nil || len(tools) != 2 {
		t.Fatalf("ListTools = %v, %v; want two tools", tools, err)
	}
	for _, want := range []string{"server/discover", "initialize", "notifications/initialized", "tools/list"} {
		if len(server.recordsForRPC(want)) == 0 {
			t.Fatalf("no %s request recorded", want)
		}
	}
}

// TestManagerStatusReflectsNegotiatedVersion verifies the negotiated protocol
// version of an older-revision server surfaces in the manager status.
func TestManagerStatusReflectsNegotiatedVersion(t *testing.T) {
	const negotiated = "2025-06-18"
	server := newMinimalMCPServerWithVersions(t, []string{negotiated})
	cfg := configForMinimalServer(server)
	mgr := NewManagerWithFactory([]config.MCPServerConfig{cfg}, func(c config.MCPServerConfig) Client {
		return NewSDKClient(c)
	})
	t.Cleanup(mgr.Close)
	if err := mgr.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	status, ok := mgr.ServerStatusByName(cfg.Name)
	if !ok || !status.Connected {
		t.Fatalf("expected the older-version server to connect, got %#v ok=%v", status, ok)
	}
	if status.ProtocolVersion != negotiated {
		t.Fatalf("status protocol version = %q, want %q", status.ProtocolVersion, negotiated)
	}
	if status.ToolCount != 2 {
		t.Fatalf("expected two tools from the older-version server, got %d", status.ToolCount)
	}
}
