package mcp

import (
	"encoding/json"
	"testing"
)

// TestSDKClientDoesNotOverdeclareCapabilities is a strict-MCP conformance
// check: the client must only declare capabilities it can actually serve.
// Koios fulfills MRTR input-required results itself via CallToolWithInput and
// has no elicitation/request handler, so it must not advertise the elicitation
// capability (a server honoring it would send elicitation/request the client
// cannot answer; servers fall back to input_required instead, which Koios
// implements). Roots support is likewise not provided and must not be
// advertised. Under the 2026-07-28 revision the handshake is the
// server/discover request, which carries the client declaration in its _meta.
func TestSDKClientDoesNotOverdeclareCapabilities(t *testing.T) {
	server := newMinimalMCPServer(t)
	client := NewSDKClient(configForMinimalServer(server))
	t.Cleanup(func() { _ = client.Close() })
	initializeFixture(t, client)

	records := server.recordsForRPC("server/discover")
	if len(records) == 0 {
		t.Fatal("no server/discover request recorded")
	}
	var params struct {
		Meta map[string]json.RawMessage `json:"_meta"`
	}
	if err := json.Unmarshal(records[0].rpcParams, &params); err != nil {
		t.Fatalf("decode discover params: %v (%s)", err, records[0].rpcParams)
	}
	var protocolVersion string
	if err := json.Unmarshal(params.Meta["io.modelcontextprotocol/protocolVersion"], &protocolVersion); err != nil || protocolVersion != ProtocolVersion2026 {
		t.Fatalf("discover must target %s, got %q (%s)", ProtocolVersion2026, protocolVersion, records[0].rpcParams)
	}
	var caps map[string]json.RawMessage
	if err := json.Unmarshal(params.Meta["io.modelcontextprotocol/clientCapabilities"], &caps); err != nil {
		t.Fatalf("decode client capabilities: %v (%s)", err, records[0].rpcParams)
	}
	for _, key := range []string{"elicitation", "roots"} {
		if _, ok := caps[key]; ok {
			t.Fatalf("client over-declares %q capability: %s", key, records[0].rpcParams)
		}
	}
}
