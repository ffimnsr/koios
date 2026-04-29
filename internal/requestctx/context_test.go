package requestctx

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ffimnsr/koios/internal/memory"
	"github.com/ffimnsr/koios/internal/types"
	"github.com/ffimnsr/koios/internal/workspace"
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

func TestBuild_InjectsStructuredPreferencesAheadOfGenericMemory(t *testing.T) {
	store, err := memory.New(filepath.Join(t.TempDir(), "memory.db"), nil)
	if err != nil {
		t.Fatalf("memory.New: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	ctx := context.Background()

	_, err = store.CreatePreference(ctx, "alice", memory.PreferenceKindDecision, "deployment windows", "Prefer Tuesday deployments after 18:00 UTC.", "ops", memory.PreferenceScopeWorkspace, "koios", 0.95, 0, "alice::main", "We deploy on Tuesdays.")
	if err != nil {
		t.Fatalf("CreatePreference: %v", err)
	}
	_, err = store.InsertChunkWithOptions(ctx, "alice", "When should I deploy? Deploy after 18:00 UTC and follow the rollout checklist.", memory.ChunkOptions{
		RetentionClass: memory.RetentionClassPinned,
	})
	if err != nil {
		t.Fatalf("InsertChunkWithOptions: %v", err)
	}

	built, err := Build(ctx, BuildOptions{
		Model:        "m",
		Messages:     []types.Message{{Role: "user", Content: "When should I deploy?"}},
		MemoryStore:  store,
		MemoryInject: true,
		MemoryTopK:   5,
		MemoryPeerID: "alice",
	})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if !containsSubstring(built.InjectedMemory, "Stable preferences and durable decisions") {
		t.Fatalf("expected structured preference block, got %q", built.InjectedMemory)
	}
	if !containsSubstring(built.InjectedMemory, "deployment windows: Prefer Tuesday deployments after 18:00 UTC.") {
		t.Fatalf("expected structured preference content, got %q", built.InjectedMemory)
	}
	if !containsSubstring(built.InjectedMemory, "When should I deploy? Deploy after 18:00 UTC and follow the rollout checklist.") {
		t.Fatalf("expected generic memory block, got %q", built.InjectedMemory)
	}
	if strings.Index(built.InjectedMemory, "Stable preferences and durable decisions") > strings.Index(built.InjectedMemory, "Relevant context from past conversations") {
		t.Fatalf("expected preference block before generic memory, got %q", built.InjectedMemory)
	}
	if built.MemoryHits != 2 {
		t.Fatalf("MemoryHits = %d, want 2", built.MemoryHits)
	}
}

func containsSubstring(s, want string) bool {
	return strings.Contains(s, want)
}

func TestLoadIdentityMessages_IncludesTOOLSmd(t *testing.T) {
	dir := t.TempDir()
	defaultPeerDir := filepath.Join(dir, "peers", workspace.DefaultPeerID)
	if err := os.MkdirAll(defaultPeerDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	content := "## Core Tooling\n- Build: go build ./..."
	if err := os.WriteFile(filepath.Join(defaultPeerDir, "TOOLS.md"), []byte(content), 0o644); err != nil {
		t.Fatalf("write TOOLS.md: %v", err)
	}

	msgs := LoadIdentityMessages(dir, "")

	var found bool
	for _, m := range msgs {
		if m.Role == "system" && strings.Contains(m.Content, "go build ./...") {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected TOOLS.md content in identity messages, got %#v", msgs)
	}
}

func TestBuild_InjectsBootstrapOnFreshSession(t *testing.T) {
	dir := t.TempDir()
	defaultPeerDir := filepath.Join(dir, "peers", workspace.DefaultPeerID)
	if err := os.MkdirAll(defaultPeerDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	bootstrapContent := "# BOOTSTRAP.md\n\nStartup checklist goes here."
	if err := os.WriteFile(filepath.Join(defaultPeerDir, "BOOTSTRAP.md"), []byte(bootstrapContent), 0o644); err != nil {
		t.Fatalf("write BOOTSTRAP.md: %v", err)
	}

	built, err := Build(context.Background(), BuildOptions{
		Model:       "m",
		Messages:    []types.Message{{Role: "user", Content: "hello"}},
		IdentityDir: dir,
		PeerID:      "",
		// No History — fresh session.
	})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	var found bool
	for _, m := range built.Request.Messages {
		if m.Role == "system" && strings.Contains(m.Content, "Startup checklist goes here.") {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected BOOTSTRAP.md in messages for fresh session, got %#v", built.Request.Messages)
	}
}

func TestBuild_SkipsBootstrapWhenHistoryPresent(t *testing.T) {
	dir := t.TempDir()
	defaultPeerDir := filepath.Join(dir, "peers", workspace.DefaultPeerID)
	if err := os.MkdirAll(defaultPeerDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(defaultPeerDir, "BOOTSTRAP.md"), []byte("# Startup content"), 0o644); err != nil {
		t.Fatalf("write BOOTSTRAP.md: %v", err)
	}

	built, err := Build(context.Background(), BuildOptions{
		Model:    "m",
		Messages: []types.Message{{Role: "user", Content: "hello"}},
		IdentityDir: dir,
		PeerID:   "",
		History:  []types.Message{
			{Role: "user", Content: "prior turn"},
			{Role: "assistant", Content: "prior reply"},
		},
	})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	for _, m := range built.Request.Messages {
		if m.Role == "system" && strings.Contains(m.Content, "Startup content") {
			t.Fatalf("did not expect BOOTSTRAP.md when history is present, got %#v", built.Request.Messages)
		}
	}
}
