package session

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/ffimnsr/koios/internal/types"
)

// Compactor condenses a slice of messages into a single summary string.
// The summary is stored as a system-role checkpoint that replaces the
// compacted messages, reducing context length while preserving key information.
type Compactor interface {
	Compact(ctx context.Context, messages []types.Message) (string, error)
}

// LLMCompleter is the subset of provider.Provider required by NewLLMCompactor.
// Any provider.Provider value satisfies this interface automatically.
type LLMCompleter interface {
	Complete(ctx context.Context, req *types.ChatRequest) (*types.ChatResponse, error)
}

// llmCompactor uses the configured LLM to summarize old conversation turns.
type llmCompactor struct {
	completer LLMCompleter
	model     string
}

// NewLLMCompactor returns a Compactor that calls the given LLM to produce
// conversation summaries. model is passed verbatim in the ChatRequest.
func NewLLMCompactor(completer LLMCompleter, model string) Compactor {
	return &llmCompactor{completer: completer, model: model}
}

func (c *llmCompactor) Compact(ctx context.Context, messages []types.Message) (string, error) {
	var sb strings.Builder
	for i, m := range messages {
		writeCompactionMessage(&sb, i, m)
	}

	req := &types.ChatRequest{
		Model: c.model,
		Messages: []types.Message{
			{Role: "system", Content: compactionSystemPrompt},
			{Role: "user", Content: sb.String()},
		},
	}

	resp, err := c.completer.Complete(ctx, req)
	if err != nil {
		return "", fmt.Errorf("compaction LLM call: %w", err)
	}
	if len(resp.Choices) == 0 {
		return "", fmt.Errorf("compaction returned no choices")
	}
	return resp.Choices[0].Message.Content, nil
}

func writeCompactionMessage(sb *strings.Builder, idx int, msg types.Message) {
	fmt.Fprintf(sb, "message[%d]\n", idx)
	fmt.Fprintf(sb, "role: %s\n", strings.TrimSpace(msg.Role))
	if toolCallID := strings.TrimSpace(msg.ToolCallID); toolCallID != "" {
		fmt.Fprintf(sb, "tool_call_id: %s\n", toolCallID)
	}
	if content := strings.TrimSpace(msg.Content); content != "" {
		fmt.Fprintf(sb, "content:\n%s\n", content)
	}
	if len(msg.Parts) > 0 {
		if data, err := json.Marshal(msg.Parts); err == nil {
			fmt.Fprintf(sb, "parts_json: %s\n", data)
		}
	}
	if raw := compactRawContent(msg); raw != "" {
		fmt.Fprintf(sb, "raw_content_json: %s\n", raw)
	}
	if len(msg.ToolCalls) > 0 {
		if data, err := json.Marshal(msg.ToolCalls); err == nil {
			fmt.Fprintf(sb, "tool_calls_json: %s\n", data)
		}
	}
	sb.WriteString("\n")
}

func compactRawContent(msg types.Message) string {
	raw := strings.TrimSpace(string(msg.RawContent))
	if raw == "" || raw == "null" {
		return ""
	}
	var s string
	if err := json.Unmarshal(msg.RawContent, &s); err == nil {
		if strings.TrimSpace(s) == strings.TrimSpace(msg.Content) {
			return ""
		}
	}
	return raw
}
