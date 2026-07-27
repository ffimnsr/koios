package session

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/ffimnsr/koios/internal/types"
)

type captureCompleter struct {
	req  *types.ChatRequest
	resp *types.ChatResponse
}

func (c *captureCompleter) Complete(_ context.Context, req *types.ChatRequest) (*types.ChatResponse, error) {
	c.req = req
	if c.resp != nil {
		return c.resp, nil
	}
	return &types.ChatResponse{Choices: []types.ChatChoice{{Message: types.Message{Content: "summary"}}}}, nil
}

func TestLLMCompactorIncludesToolAndRawContentContext(t *testing.T) {
	completer := &captureCompleter{}
	comp := NewLLMCompactor(completer, "test-model")

	_, err := comp.Compact(context.Background(), []types.Message{
		{
			Role:    "assistant",
			Content: "Checking balance.",
			RawContent: json.RawMessage(`[
				{"type":"thinking","text":"internal"},
				{"type":"tool_use","id":"toolu_1","name":"monaco.get_balance","input":{"asset":"SOL"}}
			]`),
			ToolCalls: []types.ToolCall{{
				ID:   "toolu_1",
				Type: "function",
				Function: types.ToolCallFunctionRef{
					Name:      "monaco.get_balance",
					Arguments: `{"asset":"SOL"}`,
				},
			}},
		},
		{
			Role:       "tool",
			ToolCallID: "toolu_1",
			Content:    `{"asset":"SOL","balance":42}`,
		},
	})
	if err != nil {
		t.Fatalf("Compact: %v", err)
	}
	if completer.req == nil || len(completer.req.Messages) != 2 {
		t.Fatalf("expected compactor request with 2 messages, got %#v", completer.req)
	}
	transcript := completer.req.Messages[1].Content
	for _, want := range []string{
		"role: assistant",
		"tool_calls_json:",
		"monaco.get_balance",
		"raw_content_json:",
		"tool_call_id: toolu_1",
		`{"asset":"SOL","balance":42}`,
	} {
		if !strings.Contains(transcript, want) {
			t.Fatalf("transcript missing %q\n%s", want, transcript)
		}
	}
}

func TestCompactRawContentSkipsDuplicatePlainString(t *testing.T) {
	msg := types.Message{
		Role:       "user",
		Content:    "hello",
		RawContent: json.RawMessage(`"hello"`),
	}
	if got := compactRawContent(msg); got != "" {
		t.Fatalf("expected duplicate plain string raw content to be skipped, got %q", got)
	}
}
