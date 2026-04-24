package requestctx

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ffimnsr/koios/internal/memory"
	"github.com/ffimnsr/koios/internal/types"
)

func TestBuild_PrunesOldToolMessagesFromContext(t *testing.T) {
	history := []types.Message{
		{Role: "user", Content: "u1"},
		{Role: "assistant", Content: "", ToolCalls: []types.ToolCall{{ID: "tc1", Function: types.ToolCallFunctionRef{Name: "time.now", Arguments: `{}`}}}},
		{Role: "tool", ToolCallID: "tc1", Content: "old result"},
		{Role: "assistant", Content: "after old tool"},
		{Role: "assistant", Content: "", ToolCalls: []types.ToolCall{{ID: "tc2", Function: types.ToolCallFunctionRef{Name: "time.now", Arguments: `{}`}}}},
		{Role: "tool", ToolCallID: "tc2", Content: "recent result"},
	}

	built, err := Build(context.Background(), BuildOptions{
		Model:             "m",
		Messages:          []types.Message{{Role: "user", Content: "next"}},
		History:           history,
		PruneToolMessages: 2,
	})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if built.PrunedMessages != 0 {
		t.Fatalf("expected 0 pruned messages when keeping both tool blocks, got %d", built.PrunedMessages)
	}
	got := built.Request.Messages
	if len(got) < 2 {
		t.Fatalf("expected trust-boundary and continuity system messages, got %#v", got)
	}
	if got[0].Role != "system" || got[0].Content != trustBoundaryInstruction {
		t.Fatalf("expected trust-boundary instruction first, got %#v", got[0])
	}
	if got[1].Role != "system" || got[1].Content != continuityInstruction {
		t.Fatalf("expected continuity instruction second when history exists, got %#v", got)
	}
	foundRecentTool := false
	foundOlderTool := false
	for _, msg := range got {
		if msg.ToolCallID == "tc1" {
			foundOlderTool = true
		}
		if msg.ToolCallID == "tc2" {
			foundRecentTool = true
		}
	}
	if !foundOlderTool {
		t.Fatalf("expected older tool block to remain when keep=2, got %#v", got)
	}
	if !foundRecentTool {
		t.Fatalf("expected recent tool result to remain, got %#v", got)
	}
}

func TestBuild_DoesNotInjectContinuityInstructionWithoutHistory(t *testing.T) {
	built, err := Build(context.Background(), BuildOptions{
		Model:    "m",
		Messages: []types.Message{{Role: "user", Content: "hello"}},
	})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	for _, msg := range built.Request.Messages {
		if msg.Content == continuityInstruction {
			t.Fatalf("did not expect continuity instruction without history, got %#v", built.Request.Messages)
		}
	}
}

func TestBuild_PrependsExtraSystemMessages(t *testing.T) {
	built, err := Build(context.Background(), BuildOptions{
		Model:       "m",
		Messages:    []types.Message{{Role: "user", Content: "hello"}},
		ExtraSystem: []types.Message{{Role: "system", Content: "Standing orders go here."}},
	})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if len(built.Request.Messages) < 2 {
		t.Fatalf("expected extra system message and user turn, got %#v", built.Request.Messages)
	}
	if built.Request.Messages[0].Role != "system" || built.Request.Messages[0].Content != trustBoundaryInstruction {
		t.Fatalf("unexpected first message: %#v", built.Request.Messages[0])
	}
	if built.Request.Messages[1].Role != "system" || built.Request.Messages[1].Content != "Standing orders go here." {
		t.Fatalf("unexpected second message: %#v", built.Request.Messages[1])
	}
}

func TestBuild_AlwaysPrependsTrustBoundaryInstruction(t *testing.T) {
	built, err := Build(context.Background(), BuildOptions{
		Model:    "m",
		Messages: []types.Message{{Role: "user", Content: "hello"}},
	})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if len(built.Request.Messages) == 0 {
		t.Fatal("expected built messages")
	}
	if built.Request.Messages[0].Role != "system" || built.Request.Messages[0].Content != trustBoundaryInstruction {
		t.Fatalf("expected trust-boundary instruction first, got %#v", built.Request.Messages[0])
	}
}

func TestBuild_PrunesByToolInteractionBlock(t *testing.T) {
	history := []types.Message{
		{Role: "assistant", Content: "", ToolCalls: []types.ToolCall{
			{ID: "tc1", Function: types.ToolCallFunctionRef{Name: "time.now", Arguments: `{}`}},
			{ID: "tc2", Function: types.ToolCallFunctionRef{Name: "memory.search", Arguments: `{"q":"x"}`}},
		}},
		{Role: "tool", ToolCallID: "tc1", Content: "t1"},
		{Role: "tool", ToolCallID: "tc2", Content: "t2"},
		{Role: "assistant", Content: "", ToolCalls: []types.ToolCall{
			{ID: "tc3", Function: types.ToolCallFunctionRef{Name: "cron.list", Arguments: `{}`}},
		}},
		{Role: "tool", ToolCallID: "tc3", Content: "t3"},
	}

	built, err := Build(context.Background(), BuildOptions{
		Model:             "m",
		Messages:          []types.Message{{Role: "user", Content: "next"}},
		History:           history,
		PruneToolMessages: 1,
	})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if built.PrunedMessages != 3 {
		t.Fatalf("expected 3 pruned messages from the older block, got %d", built.PrunedMessages)
	}
	for _, msg := range built.Request.Messages {
		if msg.ToolCallID == "tc1" || msg.ToolCallID == "tc2" {
			t.Fatalf("expected older tool block to be pruned, got %#v", built.Request.Messages)
		}
		if len(msg.ToolCalls) > 0 {
			for _, tc := range msg.ToolCalls {
				if tc.ID == "tc1" || tc.ID == "tc2" {
					t.Fatalf("expected older tool-call carrier to be pruned, got %#v", built.Request.Messages)
				}
			}
		}
	}
	foundCurrentBlock := false
	for _, msg := range built.Request.Messages {
		if msg.ToolCallID == "tc3" {
			foundCurrentBlock = true
		}
	}
	if !foundCurrentBlock {
		t.Fatalf("expected most recent tool block to remain, got %#v", built.Request.Messages)
	}
}

func TestBuild_InjectsOnlyAutoExposureMemory(t *testing.T) {
	store, err := memory.New(filepath.Join(t.TempDir(), "memory.db"), nil)
	if err != nil {
		t.Fatalf("memory.New: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	ctx := context.Background()

	_, err = store.InsertChunkWithOptions(ctx, "alice", "Pinned deployment preference", memory.ChunkOptions{
		RetentionClass: memory.RetentionClassPinned,
	})
	if err != nil {
		t.Fatalf("insert pinned memory: %v", err)
	}
	_, err = store.InsertChunkWithOptions(ctx, "alice", "Archived deployment note", memory.ChunkOptions{
		RetentionClass: memory.RetentionClassArchive,
	})
	if err != nil {
		t.Fatalf("insert archived memory: %v", err)
	}

	built, err := Build(ctx, BuildOptions{
		Model:        "m",
		Messages:     []types.Message{{Role: "user", Content: "deployment preference"}},
		MemoryStore:  store,
		MemoryInject: true,
		MemoryTopK:   5,
		MemoryPeerID: "alice",
	})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if built.MemoryHits != 1 {
		t.Fatalf("MemoryHits = %d, want 1", built.MemoryHits)
	}
	if built.InjectedMemory == "" || !containsSubstring(built.InjectedMemory, "Pinned deployment preference") {
		t.Fatalf("expected pinned memory in injected context, got %q", built.InjectedMemory)
	}
	if containsSubstring(built.InjectedMemory, "Archived deployment note") {
		t.Fatalf("did not expect archived memory in injected context, got %q", built.InjectedMemory)
	}
}

func containsSubstring(s, want string) bool {
	return strings.Contains(s, want)
}
