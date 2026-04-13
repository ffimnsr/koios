package requestctx

import (
	"context"
	"fmt"
	"strings"

	"github.com/ffimnsr/koios/internal/memory"
	"github.com/ffimnsr/koios/internal/types"
)

const memoryPrefix = "Relevant context from past conversations:\n\n"
const continuityInstruction = "Conversation continuity note: earlier messages included in this request are prior turns from the same ongoing conversation scope unless explicitly marked otherwise. Use them when answering questions about what was previously said."

// BuildOptions describes how a model request context should be assembled.
type BuildOptions struct {
	Model             string
	Messages          []types.Message
	History           []types.Message
	Stream            bool
	ExtraSystem       []types.Message
	PruneToolMessages int
	MemoryStore       *memory.Store
	MemoryTopK        int
	MemoryInject      bool
	MemoryPeerID      string
}

// BuildResult contains the assembled request plus related metadata.
type BuildResult struct {
	Request        *types.ChatRequest
	MemoryHits     int
	InjectedMemory string
	PrunedMessages int
}

// ValidateMessages rejects messages with roles outside the known set.
func ValidateMessages(msgs []types.Message) error {
	for i, m := range msgs {
		switch m.Role {
		case "system", "user", "assistant", "tool", "function":
		default:
			return fmt.Errorf("message[%d] has unknown role %q", i, m.Role)
		}
	}
	return nil
}

// SplitMessages partitions msgs into system-role messages and all other turns.
func SplitMessages(msgs []types.Message) (sys, turns []types.Message) {
	for _, m := range msgs {
		if m.Role == "system" {
			sys = append(sys, m)
			continue
		}
		turns = append(turns, m)
	}
	return sys, turns
}

// Build assembles a request from system messages, stored history, new turns,
// and optional long-term memory injection.
func Build(ctx context.Context, opts BuildOptions) (*BuildResult, error) {
	if err := ValidateMessages(opts.Messages); err != nil {
		return nil, err
	}
	sysMessages, turnMessages := SplitMessages(opts.Messages)
	if len(opts.ExtraSystem) > 0 {
		if err := ValidateMessages(opts.ExtraSystem); err != nil {
			return nil, err
		}
		sysMessages = append(append([]types.Message(nil), opts.ExtraSystem...), sysMessages...)
	}
	result := &BuildResult{}
	prunedHistory, prunedCount := pruneHistory(opts.History, opts.PruneToolMessages)
	result.PrunedMessages = prunedCount
	if len(prunedHistory) > 0 {
		sysMessages = append([]types.Message{{Role: "system", Content: continuityInstruction}}, sysMessages...)
	}
	if opts.MemoryStore != nil && opts.MemoryInject && len(turnMessages) > 0 {
		query := turnMessages[len(turnMessages)-1].Content
		injected, hits, err := injectMemory(ctx, opts.MemoryStore, opts.MemoryPeerID, query, opts.MemoryTopK)
		if err != nil {
			return nil, err
		}
		if injected != "" {
			sysMessages = append(sysMessages, types.Message{Role: "system", Content: injected})
			result.MemoryHits = hits
			result.InjectedMemory = injected
		}
	}
	result.Request = &types.ChatRequest{
		Model:    opts.Model,
		Messages: buildContext(sysMessages, prunedHistory, turnMessages),
		Stream:   opts.Stream,
	}
	return result, nil
}

func injectMemory(ctx context.Context, store *memory.Store, peerID, query string, topK int) (string, int, error) {
	if strings.TrimSpace(query) == "" {
		return "", 0, nil
	}
	hits, err := store.Search(ctx, peerID, query, topK)
	if err != nil || len(hits) == 0 {
		return "", 0, err
	}
	var sb strings.Builder
	sb.WriteString(memoryPrefix)
	for _, hit := range hits {
		sb.WriteString(hit.Content)
		sb.WriteString("\n\n---\n\n")
	}
	return sb.String(), len(hits), nil
}

// buildContext assembles the full message slice that will be sent to the LLM:
// system messages first, then stored non-system history, then the new turn messages.
func buildContext(sys, history, turns []types.Message) []types.Message {
	out := make([]types.Message, 0, len(sys)+len(history)+len(turns))
	out = append(out, sys...)
	for _, m := range history {
		if m.Role != "system" {
			out = append(out, m)
		}
	}
	out = append(out, turns...)
	return out
}

func pruneHistory(history []types.Message, keepToolMessages int) ([]types.Message, int) {
	if keepToolMessages < 0 {
		keepToolMessages = 0
	}
	blocks := collectToolBlocks(history)
	if len(blocks) <= keepToolMessages {
		cp := append([]types.Message(nil), history...)
		return cp, 0
	}
	keepIndexes := make(map[int]struct{}, len(history))
	for i := len(blocks) - keepToolMessages; i < len(blocks); i++ {
		if i < 0 {
			continue
		}
		for _, idx := range blocks[i] {
			keepIndexes[idx] = struct{}{}
		}
	}
	out := make([]types.Message, 0, len(history))
	pruned := 0
	for i, m := range history {
		if isToolMessage(m) {
			if _, ok := keepIndexes[i]; !ok {
				pruned++
				continue
			}
		}
		out = append(out, m)
	}
	return out, pruned
}

func isToolMessage(m types.Message) bool {
	return m.Role == "tool" || len(m.ToolCalls) > 0
}

func collectToolBlocks(history []types.Message) [][]int {
	type block struct {
		indexes []int
	}
	blocksByID := make(map[string]*block)
	order := make([]string, 0)
	standalone := make([][]int, 0)

	for i, m := range history {
		if len(m.ToolCalls) > 0 {
			ids := make([]string, 0, len(m.ToolCalls))
			for _, tc := range m.ToolCalls {
				id := tc.ID
				if id == "" {
					standalone = append(standalone, []int{i})
					continue
				}
				ids = append(ids, id)
				b, ok := blocksByID[id]
				if !ok {
					b = &block{}
					blocksByID[id] = b
					order = append(order, id)
				}
				b.indexes = appendUniqueIndex(b.indexes, i)
			}
			continue
		}
		if m.Role == "tool" {
			if m.ToolCallID == "" {
				standalone = append(standalone, []int{i})
				continue
			}
			b, ok := blocksByID[m.ToolCallID]
			if !ok {
				b = &block{}
				blocksByID[m.ToolCallID] = b
				order = append(order, m.ToolCallID)
			}
			b.indexes = appendUniqueIndex(b.indexes, i)
		}
	}

	blocks := make([][]int, 0, len(order)+len(standalone))
	for _, id := range order {
		if b := blocksByID[id]; b != nil && len(b.indexes) > 0 {
			blocks = append(blocks, b.indexes)
		}
	}
	blocks = append(blocks, standalone...)
	return blocks
}

func appendUniqueIndex(indexes []int, idx int) []int {
	for _, existing := range indexes {
		if existing == idx {
			return indexes
		}
	}
	return append(indexes, idx)
}
