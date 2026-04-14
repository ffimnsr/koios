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

const compactionSystemPrompt = `Compress the conversation into a structured summary
that preserves all information needed to continue work seamlessly. Optimize for the assistant's
ability to continue working, not human readability.

<analysis-instructions>
Before generating your summary, analyze the transcript in <think>...</think> tags:
1. What did the user originally request? (Exact phrasing)
2. What actions succeeded? What failed and why?
3. Did the user correct or redirect the assistant at any point?
4. What was actively being worked on at the end?
5. What tasks remain incomplete or pending?
6. What specific details (IDs, paths, values, names) must survive compression?
</analysis-instructions>

<summary-format>
## User Intent
The user's original request and any refinements. Use direct quotes for key requirements.
If the user's goal evolved during the conversation, capture that progression.

## Completed Work
Actions successfully performed. Be specific:
- What was created, modified, or deleted
- Exact identifiers (file paths, record IDs, URLs, names)
- Specific values, configurations, or settings applied

## Errors & Corrections
- Problems encountered and how they were resolved
- Approaches that failed (so they aren't retried)
- User corrections: "don't do X", "actually I meant Y", "that's wrong because..."
Capture corrections verbatim—these represent learned preferences.

## Active Work
What was in progress when the session ended. Include:
- The specific task being performed
- Direct quotes showing exactly where work left off
- Any partial results or intermediate state

## Pending Tasks
Remaining items the user requested that haven't been started.
Distinguish between "explicitly requested" and "implied/assumed."

## Key References
Important details needed to continue:
- Identifiers: IDs, paths, URLs, names, keys
- Values: numbers, dates, configurations, credentials (redacted)
- Context: relevant background information, constraints, preferences
- Citations: sources referenced during the conversation
</summary-format>

<preserve-rules>
Always preserve when present:
- Exact identifiers (IDs, paths, URLs, keys, names)
- Error messages verbatim
- User corrections and negative feedback
- Specific values, formulas, or configurations
- Technical constraints or requirements discovered
- The precise state of any in-progress work
</preserve-rules>

<compression-rules>
- Weight recent messages more heavily—the end of the transcript is the active context
- Omit pleasantries, acknowledgments, and filler ("Sure!", "Great question")
- Omit system context that will be re-injected separately
- Keep each section under 500 words; condense older content to make room for recent
- If you must cut details, preserve: user corrections > errors > active work > completed work
</compression-rules>
`

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
