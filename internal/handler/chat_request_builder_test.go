package handler

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ffimnsr/koios/internal/memory"
	"github.com/ffimnsr/koios/internal/session"
	"github.com/ffimnsr/koios/internal/types"
)

func TestRequestBuilder_UsesExtendedContextOptions(t *testing.T) {
	memStore, err := memory.New(filepath.Join(t.TempDir(), "memory.db"), nil)
	if err != nil {
		t.Fatalf("memory.New: %v", err)
	}
	t.Cleanup(func() { _ = memStore.Close() })

	ctx := context.Background()
	if _, err := memStore.InsertChunkWithOptions(ctx, "alice", "Alice recent note", memory.ChunkOptions{
		RetentionClass: memory.RetentionClassPinned,
	}); err != nil {
		t.Fatalf("insert alice chunk: %v", err)
	}
	if _, err := memStore.InsertChunkWithOptions(ctx, "shared", "Shared deployment preference", memory.ChunkOptions{
		RetentionClass: memory.RetentionClassPinned,
	}); err != nil {
		t.Fatalf("insert shared chunk: %v", err)
	}

	h := NewHandler(session.New(10), noopProvider{}, HandlerOptions{
		Model:               "test-model",
		MemStore:            memStore,
		MemTopK:             3,
		MemInject:           true,
		MemoryLCMWindow:     1,
		MemoryNamespaces:    []string{"shared"},
		PruneToolMessages:   0,
		MaxContextTokens:    4096,
		PromptReserveTokens: 256,
	})

	history := []types.Message{
		{Role: "assistant", ToolCalls: []types.ToolCall{{
			ID:       "old-tool",
			Type:     "function",
			Function: types.ToolCallFunctionRef{Name: "time.now", Arguments: `{}`},
		}}},
		{Role: "tool", ToolCallID: "old-tool", Content: "old tool result"},
	}

	req, err := requestBuilder(ctx, h, "alice", []types.Message{{Role: "user", Content: "deployment preference"}}, history, false)
	if err != nil {
		t.Fatalf("requestBuilder: %v", err)
	}

	var foundRecent, foundNamespace bool
	for _, msg := range req.Messages {
		if msg.ToolCallID == "old-tool" {
			t.Fatalf("expected pruned tool result to be omitted, got %#v", req.Messages)
		}
		for _, tc := range msg.ToolCalls {
			if tc.ID == "old-tool" {
				t.Fatalf("expected pruned tool call to be omitted, got %#v", req.Messages)
			}
		}
		if strings.Contains(msg.Content, "Alice recent note") {
			foundRecent = true
		}
		if strings.Contains(msg.Content, "Shared deployment preference") {
			foundNamespace = true
		}
	}
	if !foundRecent {
		t.Fatalf("expected LCM recent memory in request, got %#v", req.Messages)
	}
	if !foundNamespace {
		t.Fatalf("expected namespace memory in request, got %#v", req.Messages)
	}
}
