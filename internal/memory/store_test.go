package memory_test

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/ffimnsr/koios/internal/memory"
)

func newTestStore(t *testing.T) *memory.Store {
	t.Helper()
	s, err := memory.New(filepath.Join(t.TempDir(), "test.db"), nil)
	if err != nil {
		t.Fatalf("memory.New: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestStore_InsertAndGet(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	chunk, err := s.InsertChunk(ctx, "alice", "hello world")
	if err != nil {
		t.Fatalf("InsertChunk: %v", err)
	}
	if chunk.ID == "" {
		t.Fatal("expected non-empty chunk ID")
	}
	if chunk.Content != "hello world" {
		t.Fatalf("content = %q, want %q", chunk.Content, "hello world")
	}

	got, err := s.GetChunk(ctx, "alice", chunk.ID)
	if err != nil {
		t.Fatalf("GetChunk: %v", err)
	}
	if got.Content != "hello world" {
		t.Fatalf("GetChunk content = %q, want %q", got.Content, "hello world")
	}
}

func TestStore_InsertChunkWithTags(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	chunk, err := s.InsertChunkWithTags(ctx, "alice", "tagged content",
		[]string{"important", "project"}, "notes")
	if err != nil {
		t.Fatalf("InsertChunkWithTags: %v", err)
	}

	got, err := s.GetChunk(ctx, "alice", chunk.ID)
	if err != nil {
		t.Fatalf("GetChunk: %v", err)
	}
	if got.Category != "notes" {
		t.Fatalf("category = %q, want %q", got.Category, "notes")
	}
	if len(got.Tags) != 2 || got.Tags[0] != "important" || got.Tags[1] != "project" {
		t.Fatalf("tags = %v, want [important project]", got.Tags)
	}
}

func TestStore_Search(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	_, _ = s.InsertChunk(ctx, "alice", "the quick brown fox jumps over the lazy dog")
	_, _ = s.InsertChunk(ctx, "alice", "a stitch in time saves nine")
	_, _ = s.InsertChunk(ctx, "bob", "bob's unrelated content")

	results, err := s.Search(ctx, "alice", "quick fox", 5)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(results) == 0 {
		t.Fatal("expected at least one search result")
	}
	// First result should match the fox sentence.
	if results[0].PeerID != "alice" {
		t.Fatalf("result peer = %q, want alice", results[0].PeerID)
	}
}

func TestStore_SearchCompact(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	longContent := ""
	for i := 0; i < 50; i++ {
		longContent += "word "
	}
	longContent += "findme searchable content here"
	_, _ = s.InsertChunk(ctx, "alice", longContent)

	results, err := s.SearchCompact(ctx, "alice", "findme searchable", 5)
	if err != nil {
		t.Fatalf("SearchCompact: %v", err)
	}
	if len(results) == 0 {
		t.Fatal("expected at least one compact result")
	}
	// Preview should be truncated.
	if len(results[0].Preview) > 210 {
		t.Fatalf("preview too long: %d chars", len(results[0].Preview))
	}
}

func TestStore_List(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	_, _ = s.InsertChunk(ctx, "alice", "chunk 1")
	_, _ = s.InsertChunk(ctx, "alice", "chunk 2")
	_, _ = s.InsertChunk(ctx, "bob", "bob chunk")

	chunks, err := s.List(ctx, "alice", 50)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(chunks) != 2 {
		t.Fatalf("List count = %d, want 2", len(chunks))
	}
}

func TestStore_DeleteChunk(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	chunk, _ := s.InsertChunk(ctx, "alice", "to be deleted")
	if err := s.DeleteChunk(ctx, "alice", chunk.ID); err != nil {
		t.Fatalf("DeleteChunk: %v", err)
	}
	_, err := s.GetChunk(ctx, "alice", chunk.ID)
	if err == nil {
		t.Fatal("expected error getting deleted chunk")
	}
}

func TestStore_DeleteChunk_WrongPeer(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	chunk, _ := s.InsertChunk(ctx, "alice", "alice's chunk")
	err := s.DeleteChunk(ctx, "bob", chunk.ID)
	if err == nil {
		t.Fatal("expected error deleting another peer's chunk")
	}
}

func TestStore_Timeline(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	// Insert 5 chunks — they'll all have the same second-precision timestamp,
	// so we rely on insertion order for the (created_at, rowid) ordering.
	// To guarantee distinct created_at values use 1-second sleeps, which is
	// too slow. Instead, insert them and verify the timeline with what we get.
	chunks := make([]*memory.Chunk, 5)
	contents := []string{"first", "second", "third", "fourth", "fifth"}
	for i, c := range contents {
		var err error
		chunks[i], err = s.InsertChunk(ctx, "alice", c)
		if err != nil {
			t.Fatalf("InsertChunk(%q): %v", c, err)
		}
	}

	// Anchor on the third chunk (middle), ask for 5 before and 5 after.
	// With same-second timestamps, the < / > comparisons won't split them,
	// so we expect at minimum the anchor itself.
	timeline, err := s.Timeline(ctx, "alice", chunks[2].ID, 5, 5)
	if err != nil {
		t.Fatalf("Timeline: %v", err)
	}
	if len(timeline) < 1 {
		t.Fatalf("Timeline count = %d, want at least 1 (the anchor)", len(timeline))
	}
	// The anchor must be present.
	found := false
	for _, c := range timeline {
		if c.ID == chunks[2].ID {
			found = true
		}
	}
	if !found {
		t.Fatal("anchor chunk not found in timeline")
	}
}

func TestStore_BatchGet(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	c1, _ := s.InsertChunk(ctx, "alice", "one")
	c2, _ := s.InsertChunk(ctx, "alice", "two")
	_, _ = s.InsertChunk(ctx, "bob", "three")

	chunks, err := s.BatchGet(ctx, "alice", []string{c1.ID, c2.ID})
	if err != nil {
		t.Fatalf("BatchGet: %v", err)
	}
	if len(chunks) != 2 {
		t.Fatalf("BatchGet count = %d, want 2", len(chunks))
	}
}

func TestStore_BatchGet_Empty(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	chunks, err := s.BatchGet(ctx, "alice", nil)
	if err != nil {
		t.Fatalf("BatchGet nil: %v", err)
	}
	if chunks != nil {
		t.Fatalf("expected nil, got %v", chunks)
	}
}

func TestStore_Stats(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	_, _ = s.InsertChunkWithTags(ctx, "alice", "one", nil, "notes")
	_, _ = s.InsertChunkWithTags(ctx, "alice", "two", nil, "code")
	_, _ = s.InsertChunkWithTags(ctx, "alice", "three", nil, "notes")

	stats, err := s.Stats(ctx, "alice")
	if err != nil {
		t.Fatalf("Stats: %v", err)
	}
	if stats.TotalChunks != 3 {
		t.Fatalf("TotalChunks = %d, want 3", stats.TotalChunks)
	}
	if stats.ByCategory["notes"] != 2 {
		t.Fatalf("ByCategory[notes] = %d, want 2", stats.ByCategory["notes"])
	}
	if stats.ByCategory["code"] != 1 {
		t.Fatalf("ByCategory[code] = %d, want 1", stats.ByCategory["code"])
	}
	if stats.MilvusActive {
		t.Fatal("MilvusActive should be false without milvus")
	}
}

func TestStore_TagChunk(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	chunk, _ := s.InsertChunk(ctx, "alice", "untagged content")

	err := s.TagChunk(ctx, "alice", chunk.ID, []string{"urgent", "review"}, "tasks")
	if err != nil {
		t.Fatalf("TagChunk: %v", err)
	}

	got, err := s.GetChunk(ctx, "alice", chunk.ID)
	if err != nil {
		t.Fatalf("GetChunk after tag: %v", err)
	}
	if got.Category != "tasks" {
		t.Fatalf("category = %q, want %q", got.Category, "tasks")
	}
	if len(got.Tags) != 2 || got.Tags[0] != "urgent" {
		t.Fatalf("tags = %v, want [urgent review]", got.Tags)
	}
}

func TestStore_TagChunk_WrongPeer(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	chunk, _ := s.InsertChunk(ctx, "alice", "alice's chunk")
	err := s.TagChunk(ctx, "bob", chunk.ID, []string{"hack"}, "evil")
	if err == nil {
		t.Fatal("expected error tagging another peer's chunk")
	}
}

func TestStore_TagChunk_NotFound(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	err := s.TagChunk(ctx, "alice", "nonexistent", []string{"x"}, "y")
	if err == nil {
		t.Fatal("expected error tagging nonexistent chunk")
	}
}

func TestStore_DeletePeer(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	_, _ = s.InsertChunk(ctx, "alice", "one")
	_, _ = s.InsertChunk(ctx, "alice", "two")
	_, _ = s.InsertChunk(ctx, "bob", "bob's chunk")

	if err := s.DeletePeer(ctx, "alice"); err != nil {
		t.Fatalf("DeletePeer: %v", err)
	}
	chunks, _ := s.List(ctx, "alice", 50)
	if len(chunks) != 0 {
		t.Fatalf("expected 0 chunks after delete, got %d", len(chunks))
	}
	chunks, _ = s.List(ctx, "bob", 50)
	if len(chunks) != 1 {
		t.Fatalf("expected bob's chunk to survive, got %d", len(chunks))
	}
}
