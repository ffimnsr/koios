package types

import (
	"encoding/json"
	"testing"
)

func TestMessagePreservesRawProviderContent(t *testing.T) {
	input := []byte(`{
		"role": "assistant",
		"content": [
			{
				"type": "functionCall",
				"id": "call_123",
				"name": "default_api:mcp__monaco__get_balance_by_asset",
				"args": {"asset":"USDC"},
				"thought_signature": "sig_abc"
			}
		],
		"tool_calls": [
			{
				"id": "call_123",
				"type": "function",
				"function": {
					"name": "default_api:mcp__monaco__get_balance_by_asset",
					"arguments": "{\"asset\":\"USDC\"}"
				}
			}
		]
	}`)

	var msg Message
	if err := json.Unmarshal(input, &msg); err != nil {
		t.Fatalf("unmarshal message: %v", err)
	}
	if len(msg.RawContent) == 0 {
		t.Fatal("expected raw content to be preserved")
	}
	if len(msg.ToolCalls) != 1 {
		t.Fatalf("expected one tool call, got %d", len(msg.ToolCalls))
	}

	output, err := json.Marshal(msg)
	if err != nil {
		t.Fatalf("marshal message: %v", err)
	}

	var roundTrip struct {
		Content []map[string]any `json:"content"`
	}
	if err := json.Unmarshal(output, &roundTrip); err != nil {
		t.Fatalf("unmarshal round-trip output: %v", err)
	}
	if len(roundTrip.Content) != 1 {
		t.Fatalf("expected one content part, got %d", len(roundTrip.Content))
	}
	if got := roundTrip.Content[0]["thought_signature"]; got != "sig_abc" {
		t.Fatalf("expected thought_signature sig_abc, got %#v", got)
	}
	if got := roundTrip.Content[0]["name"]; got != "default_api:mcp__monaco__get_balance_by_asset" {
		t.Fatalf("expected function name to survive round-trip, got %#v", got)
	}
}
