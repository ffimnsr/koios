package session

import (
	"context"
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

const compactionSystemPrompt = `You are a conversation summarizer. Your task is to produce a concise but complete summary of the conversation history provided.

The summary must capture:
- The main goals or tasks discussed
- Key decisions and conclusions reached
- Important facts, constraints, or context established
- Work in progress and any explicit next steps

Output only the summary text. Do not include any preamble, explanation, or metadata.`

func (c *llmCompactor) Compact(ctx context.Context, messages []types.Message) (string, error) {
	var sb strings.Builder
	for _, m := range messages {
		fmt.Fprintf(&sb, "[%s]: %s\n\n", m.Role, m.Content)
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
