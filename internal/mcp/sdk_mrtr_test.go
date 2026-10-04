package mcp

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

// TestSDKFromToolResultPreservesInputRequiredMetadata verifies that an
// input_required result decodes into a Koios ToolResult carrying resultType,
// the opaque requestState, and the inputRequests (under Elicitation), and that
// the fields the SDK v1.8 drops during decode stay unset.
func TestSDKFromToolResultPreservesInputRequiredMetadata(t *testing.T) {
	var res sdkmcp.CallToolResult
	if err := json.Unmarshal([]byte(`{
		"resultType": "input_required",
		"requestId": "req-approve-1",
		"requestState": "rs-approve-1",
		"ttlMs": 60000,
		"cacheScope": "public",
		"inputRequests": {
			"q1": {
				"method": "elicitation/create",
				"params": {"mode": "form", "message": "Approve the transfer?"}
			}
		}
	}`), &res); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !res.NeedsInput() {
		t.Fatal("expected NeedsInput to be true")
	}

	result := fromSDKToolResult(&res)
	if result == nil {
		t.Fatal("nil result")
	}
	if result.ResultType != "input_required" {
		t.Fatalf("expected input_required result type, got %q", result.ResultType)
	}
	// requestState is the opaque JSON string echo; it must be preserved byte
	// for byte so it can be sent back on the retry.
	if string(result.RequestState) != `"rs-approve-1"` {
		t.Fatalf("requestState not preserved: %s", result.RequestState)
	}
	var elicitation map[string]any
	if err := json.Unmarshal(result.Elicitation, &elicitation); err != nil {
		t.Fatalf("elicitation is not valid JSON: %v (%s)", err, result.Elicitation)
	}
	q1, ok := elicitation["q1"].(map[string]any)
	if !ok || q1["method"] != "elicitation/create" {
		t.Fatalf("inputRequests not preserved under elicitation: %#v", elicitation)
	}
	// The SDK v1.8 CallToolResult drops requestId, ttlMs, and cacheScope on
	// tool results during decode; Koios cannot recover them.
	if result.RequestID != "" || result.TTLMs != 0 || result.CacheScope != "" {
		t.Fatalf("expected SDK-dropped fields to stay unset, got %#v", result)
	}
}

// TestSDKClientListToolsPaginatesAcrossPages verifies the SDK cursor loop
// replaces the legacy client's hand-rolled pagination path: both pages are
// fetched and merged into one tool list.
func TestSDKClientListToolsPaginatesAcrossPages(t *testing.T) {
	server := newMinimalMCPServerPaged(t)
	client := NewSDKClient(configForMinimalServer(server))
	t.Cleanup(func() { _ = client.Close() })
	initializeFixture(t, client)

	tools, err := client.ListTools(context.Background())
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	if len(tools) != 2 || tools[0].Name != "quote" || tools[1].Name != "approve_flow" {
		t.Fatalf("expected both paged tools, got %#v", tools)
	}
	if got := len(server.recordsForRPC("tools/list")); got != 2 {
		t.Fatalf("expected two tools/list requests, got %d", got)
	}
}

// TestSDKClientMRTRInputRequiredAndRetryOverHTTP drives the full MRTR flow
// against the minimal server: the first call returns input_required, the
// follow-up echoes inputResponses and requestState and completes.
func TestSDKClientMRTRInputRequiredAndRetryOverHTTP(t *testing.T) {
	server := newMinimalMCPServer(t)
	client := NewSDKClient(configForMinimalServer(server))
	t.Cleanup(func() { _ = client.Close() })
	initializeFixture(t, client)

	first, err := client.CallTool(context.Background(), "approve_flow", map[string]any{"amount": 100})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if first == nil || first.ResultType != "input_required" {
		t.Fatalf("expected an input_required result, got %#v", first)
	}
	if string(first.RequestState) != `"rs-approve-1"` {
		t.Fatalf("requestState not preserved: %s", first.RequestState)
	}
	if !json.Valid(first.Elicitation) {
		t.Fatalf("elicitation is not valid JSON: %s", first.Elicitation)
	}
	// The SDK v1.8 drops these fields from tools/call results on decode.
	if first.RequestID != "" || first.TTLMs != 0 || first.CacheScope != "" {
		t.Fatalf("expected SDK-dropped fields to stay unset, got %#v", first)
	}

	second, err := client.CallToolWithInput(
		context.Background(),
		"approve_flow",
		map[string]any{"amount": 100},
		json.RawMessage(`{"q1":{"action":"accept"}}`),
		json.RawMessage(`"rs-approve-1"`),
	)
	if err != nil {
		t.Fatalf("follow-up CallToolWithInput: %v", err)
	}
	if second == nil || second.ResultType != "complete" || len(second.Content) != 1 || second.Content[0].Text != "accepted" {
		t.Fatalf("unexpected follow-up result: %#v", second)
	}

	// The follow-up request must have echoed inputResponses and requestState
	// to the server verbatim.
	records := server.recordsForRPC("tools/call")
	if len(records) != 2 {
		t.Fatalf("expected two tools/call requests, got %d", len(records))
	}
	var params struct {
		Name           string          `json:"name"`
		InputResponses json.RawMessage `json:"inputResponses"`
		RequestState   string          `json:"requestState"`
	}
	if err := json.Unmarshal(records[1].rpcParams, &params); err != nil {
		t.Fatalf("decode recorded params: %v (%s)", err, records[1].rpcParams)
	}
	if params.Name != "approve_flow" || params.RequestState != "rs-approve-1" {
		t.Fatalf("requestState not echoed: %#v", params)
	}
	var responses map[string]any
	if err := json.Unmarshal(params.InputResponses, &responses); err != nil {
		t.Fatalf("inputResponses not echoed: %v (%s)", err, params.InputResponses)
	}
	if len(responses) != 1 {
		t.Fatalf("expected one echoed input response, got %#v", responses)
	}
}

// TestSDKClientMalformedInputResponsesRejectedWithoutToolCall verifies that
// malformed inputResponses fail contextually before any request is sent.
func TestSDKClientMalformedInputResponsesRejectedWithoutToolCall(t *testing.T) {
	server := newMinimalMCPServer(t)
	client := NewSDKClient(configForMinimalServer(server))
	t.Cleanup(func() { _ = client.Close() })
	initializeFixture(t, client)

	_, err := client.CallToolWithInput(context.Background(), "approve_flow", nil, json.RawMessage(`{`), nil)
	if err == nil {
		t.Fatal("malformed inputResponses should fail")
	}
	if !strings.Contains(err.Error(), "inputResponses") || !strings.Contains(err.Error(), "approve_flow") {
		t.Fatalf("error should carry field and tool context: %v", err)
	}
	if got := len(server.recordsForRPC("tools/call")); got != 0 {
		t.Fatalf("no tools/call request should be sent for malformed inputResponses, got %d", got)
	}
}

// TestSDKClientMalformedRequestStateRejectedWithoutToolCall verifies that a
// requestState that is not the opaque JSON string fails contextually before
// any request is sent.
func TestSDKClientMalformedRequestStateRejectedWithoutToolCall(t *testing.T) {
	server := newMinimalMCPServer(t)
	client := NewSDKClient(configForMinimalServer(server))
	t.Cleanup(func() { _ = client.Close() })
	initializeFixture(t, client)

	_, err := client.CallToolWithInput(context.Background(), "approve_flow", nil, nil, json.RawMessage(`{"not":"a string"}`))
	if err == nil {
		t.Fatal("malformed requestState should fail")
	}
	if !strings.Contains(err.Error(), "requestState") || !strings.Contains(err.Error(), "approve_flow") {
		t.Fatalf("error should carry field and tool context: %v", err)
	}
	if got := len(server.recordsForRPC("tools/call")); got != 0 {
		t.Fatalf("no tools/call request should be sent for malformed requestState, got %d", got)
	}
}
