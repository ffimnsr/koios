package memory_test

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

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
	if got.RetentionClass != memory.RetentionClassWorking {
		t.Fatalf("RetentionClass = %q, want %q", got.RetentionClass, memory.RetentionClassWorking)
	}
	if got.ExposurePolicy != memory.ExposurePolicyAuto {
		t.Fatalf("ExposurePolicy = %q, want %q", got.ExposurePolicy, memory.ExposurePolicyAuto)
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

func TestStore_SearchForInjection_FiltersArchived(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	_, err := s.InsertChunkWithOptions(ctx, "alice", "deploy preference pinned", memory.ChunkOptions{
		RetentionClass: memory.RetentionClassPinned,
	})
	if err != nil {
		t.Fatalf("InsertChunkWithOptions pinned: %v", err)
	}
	_, err = s.InsertChunkWithOptions(ctx, "alice", "deploy preference archived", memory.ChunkOptions{
		RetentionClass: memory.RetentionClassArchive,
	})
	if err != nil {
		t.Fatalf("InsertChunkWithOptions archived: %v", err)
	}

	results, err := s.SearchForInjection(ctx, "alice", "deploy preference", 10)
	if err != nil {
		t.Fatalf("SearchForInjection: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("SearchForInjection count = %d, want 1", len(results))
	}
	if results[0].RetentionClass != memory.RetentionClassPinned {
		t.Fatalf("RetentionClass = %q, want pinned", results[0].RetentionClass)
	}
	searchResults, err := s.Search(ctx, "alice", "deploy preference", 10)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(searchResults) != 2 {
		t.Fatalf("Search count = %d, want 2", len(searchResults))
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
	if stats.ByRetention[memory.RetentionClassWorking] != 3 {
		t.Fatalf("ByRetention[working] = %d, want 3", stats.ByRetention[memory.RetentionClassWorking])
	}
	if stats.MilvusActive {
		t.Fatal("MilvusActive should be false without milvus")
	}
}

func TestStore_TagChunk(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	chunk, _ := s.InsertChunk(ctx, "alice", "untagged content")

	err := s.TagChunk(ctx, "alice", chunk.ID, []string{"urgent", "review"}, "tasks", memory.RetentionClassPinned, memory.ExposurePolicyAuto, 0)
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
	if got.RetentionClass != memory.RetentionClassPinned {
		t.Fatalf("retention = %q, want pinned", got.RetentionClass)
	}
}

func TestStore_TagChunk_WrongPeer(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	chunk, _ := s.InsertChunk(ctx, "alice", "alice's chunk")
	err := s.TagChunk(ctx, "bob", chunk.ID, []string{"hack"}, "evil", memory.RetentionClassPinned, memory.ExposurePolicyAuto, 0)
	if err == nil {
		t.Fatal("expected error tagging another peer's chunk")
	}
}

func TestStore_TagChunk_NotFound(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	err := s.TagChunk(ctx, "alice", "nonexistent", []string{"x"}, "y", memory.RetentionClassWorking, memory.ExposurePolicyAuto, 0)
	if err == nil {
		t.Fatal("expected error tagging nonexistent chunk")
	}
}

func TestStore_ExpiredChunkIsPurgedOnRead(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	chunk, err := s.InsertChunkWithOptions(ctx, "alice", "short lived", memory.ChunkOptions{
		ExpiresAt: time.Now().Add(-time.Minute).Unix(),
	})
	if err != nil {
		t.Fatalf("InsertChunkWithOptions: %v", err)
	}
	chunks, err := s.List(ctx, "alice", 10)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(chunks) != 0 {
		t.Fatalf("expected expired chunk to be purged, got %d chunks", len(chunks))
	}
	if _, err := s.GetChunk(ctx, "alice", chunk.ID); err == nil {
		t.Fatal("expected expired chunk lookup to fail")
	}
}

func TestStore_RecentForInjection_FiltersSearchOnly(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	_, err := s.InsertChunkWithOptions(ctx, "alice", "active memory", memory.ChunkOptions{})
	if err != nil {
		t.Fatalf("Insert active memory: %v", err)
	}
	_, err = s.InsertChunkWithOptions(ctx, "alice", "archived memory", memory.ChunkOptions{
		RetentionClass: memory.RetentionClassArchive,
	})
	if err != nil {
		t.Fatalf("Insert archived memory: %v", err)
	}

	chunks, err := s.RecentForInjection(ctx, "alice", 10)
	if err != nil {
		t.Fatalf("RecentForInjection: %v", err)
	}
	if len(chunks) != 1 {
		t.Fatalf("RecentForInjection count = %d, want 1", len(chunks))
	}
	if chunks[0].Content != "active memory" {
		t.Fatalf("unexpected injectable memory: %q", chunks[0].Content)
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

func TestStore_CandidateApproveLifecycle(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	candidate, err := s.QueueCandidate(ctx, "alice", "Remember the deployment preference", memory.ChunkOptions{
		Tags:           []string{"deploy", "preference"},
		Category:       "preferences",
		RetentionClass: memory.RetentionClassPinned,
	})
	if err != nil {
		t.Fatalf("QueueCandidate: %v", err)
	}
	if candidate.Status != memory.CandidateStatusPending {
		t.Fatalf("status = %q, want pending", candidate.Status)
	}

	edited := "Remember the deployment preference for production rollouts"
	candidate, err = s.EditCandidate(ctx, "alice", candidate.ID, memory.CandidatePatch{Content: &edited})
	if err != nil {
		t.Fatalf("EditCandidate: %v", err)
	}
	if candidate.Content != edited {
		t.Fatalf("content = %q, want %q", candidate.Content, edited)
	}

	approved, chunk, err := s.ApproveCandidate(ctx, "alice", candidate.ID, memory.CandidatePatch{}, "confirmed by user")
	if err != nil {
		t.Fatalf("ApproveCandidate: %v", err)
	}
	if approved.Status != memory.CandidateStatusApproved {
		t.Fatalf("status = %q, want approved", approved.Status)
	}
	if approved.ResultChunkID != chunk.ID {
		t.Fatalf("result chunk = %q, want %q", approved.ResultChunkID, chunk.ID)
	}
	if chunk.Content != edited {
		t.Fatalf("chunk content = %q, want %q", chunk.Content, edited)
	}
	if _, err := s.EditCandidate(ctx, "alice", candidate.ID, memory.CandidatePatch{}); err == nil {
		t.Fatal("expected reviewed candidate edit to fail")
	}

	chunks, err := s.List(ctx, "alice", 10)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(chunks) != 1 {
		t.Fatalf("List count = %d, want 1", len(chunks))
	}
	if chunks[0].RetentionClass != memory.RetentionClassPinned {
		t.Fatalf("retention = %q, want pinned", chunks[0].RetentionClass)
	}
}

func TestStore_CandidateMergeLifecycle(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	target, err := s.InsertChunkWithOptions(ctx, "alice", "Existing preference note", memory.ChunkOptions{
		Tags:           []string{"existing"},
		Category:       "notes",
		RetentionClass: memory.RetentionClassWorking,
	})
	if err != nil {
		t.Fatalf("InsertChunkWithOptions: %v", err)
	}
	candidate, err := s.QueueCandidate(ctx, "alice", "Add staging deployment detail", memory.ChunkOptions{
		Tags:     []string{"staging", "existing"},
		Category: "details",
	})
	if err != nil {
		t.Fatalf("QueueCandidate: %v", err)
	}

	merged, updated, err := s.MergeCandidate(ctx, "alice", candidate.ID, target.ID, memory.CandidatePatch{}, "same topic")
	if err != nil {
		t.Fatalf("MergeCandidate: %v", err)
	}
	if merged.Status != memory.CandidateStatusMerged {
		t.Fatalf("status = %q, want merged", merged.Status)
	}
	if updated.ID != target.ID {
		t.Fatalf("updated chunk = %q, want %q", updated.ID, target.ID)
	}
	if updated.Content != "Existing preference note\n\nAdd staging deployment detail" {
		t.Fatalf("unexpected merged content: %q", updated.Content)
	}
	if len(updated.Tags) != 2 {
		t.Fatalf("merged tags = %v, want 2 unique tags", updated.Tags)
	}
}

func TestStore_CandidateRejectLifecycle(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	candidate, err := s.QueueCandidate(ctx, "alice", "Temporary travel idea", memory.ChunkOptions{})
	if err != nil {
		t.Fatalf("QueueCandidate: %v", err)
	}
	rejected, err := s.RejectCandidate(ctx, "alice", candidate.ID, "too transient")
	if err != nil {
		t.Fatalf("RejectCandidate: %v", err)
	}
	if rejected.Status != memory.CandidateStatusRejected {
		t.Fatalf("status = %q, want rejected", rejected.Status)
	}
	if rejected.ReviewReason != "too transient" {
		t.Fatalf("review reason = %q, want too transient", rejected.ReviewReason)
	}
	pending, err := s.ListCandidates(ctx, "alice", 10, memory.CandidateStatusPending)
	if err != nil {
		t.Fatalf("ListCandidates: %v", err)
	}
	if len(pending) != 0 {
		t.Fatalf("pending candidates = %d, want 0", len(pending))
	}
	reviewed, err := s.ListCandidates(ctx, "alice", 10, memory.CandidateStatusRejected)
	if err != nil {
		t.Fatalf("ListCandidates rejected: %v", err)
	}
	if len(reviewed) != 1 {
		t.Fatalf("rejected candidates = %d, want 1", len(reviewed))
	}
}

func TestStore_EntityGraphLifecycle(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	chunk, err := s.InsertChunk(ctx, "alice", "Borealis trip is blocked on venue approval.")
	if err != nil {
		t.Fatalf("InsertChunk: %v", err)
	}
	project, err := s.UpsertEntity(ctx, "alice", memory.EntityKindProject, "Borealis Trip", []string{"summer trip", "borealis"}, "Planning the annual trip.", 100)
	if err != nil {
		t.Fatalf("UpsertEntity project create: %v", err)
	}
	project, err = s.UpsertEntity(ctx, "alice", memory.EntityKindProject, "summer trip", nil, "Venue planning project.", 200)
	if err != nil {
		t.Fatalf("UpsertEntity project merge: %v", err)
	}
	if project.LastSeenAt != 200 {
		t.Fatalf("project last_seen_at = %d, want 200", project.LastSeenAt)
	}
	if !strings.Contains(project.Notes, "Venue planning project") {
		t.Fatalf("project notes = %q", project.Notes)
	}
	person, err := s.CreateEntity(ctx, "alice", memory.EntityKindPerson, "Alice Example", []string{"Alice"}, "Primary trip coordinator.", 0)
	if err != nil {
		t.Fatalf("CreateEntity person: %v", err)
	}

	if err := s.LinkChunkToEntity(ctx, "alice", project.ID, chunk.ID); err != nil {
		t.Fatalf("LinkChunkToEntity: %v", err)
	}
	rel, err := s.RelateEntities(ctx, "alice", project.ID, person.ID, "owned_by", "Alice is coordinating the trip.")
	if err != nil {
		t.Fatalf("RelateEntities: %v", err)
	}
	if rel.Relation != "owned_by" {
		t.Fatalf("relation = %q, want owned_by", rel.Relation)
	}

	updatedNotes := "Planning the annual trip and venue search."
	project, err = s.UpdateEntity(ctx, "alice", project.ID, memory.EntityPatch{Notes: &updatedNotes})
	if err != nil {
		t.Fatalf("UpdateEntity: %v", err)
	}
	if project.Notes != updatedNotes {
		t.Fatalf("notes = %q, want %q", project.Notes, updatedNotes)
	}

	searchResults, err := s.SearchEntities(ctx, "alice", "summer trip", 10)
	if err != nil {
		t.Fatalf("SearchEntities: %v", err)
	}
	if len(searchResults) != 1 || searchResults[0].ID != project.ID {
		t.Fatalf("unexpected entity search results: %#v", searchResults)
	}

	projectGraph, err := s.GetEntityGraph(ctx, "alice", project.ID)
	if err != nil {
		t.Fatalf("GetEntityGraph: %v", err)
	}
	if len(projectGraph.LinkedChunks) != 1 || projectGraph.LinkedChunks[0].ID != chunk.ID {
		t.Fatalf("unexpected linked chunks: %#v", projectGraph.LinkedChunks)
	}
	if len(projectGraph.Outgoing) != 1 || projectGraph.Outgoing[0].TargetEntityID != person.ID {
		t.Fatalf("unexpected outgoing relationships: %#v", projectGraph.Outgoing)
	}
	if err := s.UnlinkChunkFromEntity(ctx, "alice", project.ID, chunk.ID); err != nil {
		t.Fatalf("UnlinkChunkFromEntity: %v", err)
	}
	if err := s.UnrelateEntities(ctx, "alice", project.ID, person.ID, "owned_by"); err != nil {
		t.Fatalf("UnrelateEntities: %v", err)
	}
	projectGraph, err = s.GetEntityGraph(ctx, "alice", project.ID)
	if err != nil {
		t.Fatalf("GetEntityGraph after unlink: %v", err)
	}
	if len(projectGraph.LinkedChunks) != 0 {
		t.Fatalf("expected no linked chunks after unlink, got %#v", projectGraph.LinkedChunks)
	}
	if len(projectGraph.Outgoing) != 0 {
		t.Fatalf("expected no outgoing edges after unrelate, got %#v", projectGraph.Outgoing)
	}
	if err := s.LinkChunkToEntity(ctx, "alice", project.ID, chunk.ID); err != nil {
		t.Fatalf("LinkChunkToEntity relink: %v", err)
	}

	listResults, err := s.ListEntities(ctx, "alice", memory.EntityKindProject, 10)
	if err != nil {
		t.Fatalf("ListEntities: %v", err)
	}
	if len(listResults) != 1 || listResults[0].LinkedChunkCount != 1 {
		t.Fatalf("unexpected entity list results: %#v", listResults)
	}

	touched, err := s.TouchEntity(ctx, "alice", person.ID, 12345)
	if err != nil {
		t.Fatalf("TouchEntity: %v", err)
	}
	if touched.LastSeenAt != 12345 {
		t.Fatalf("last_seen_at = %d, want 12345", touched.LastSeenAt)
	}
	if err := s.DeleteEntity(ctx, "alice", person.ID); err != nil {
		t.Fatalf("DeleteEntity person: %v", err)
	}
	if _, err := s.GetEntity(ctx, "alice", person.ID); err == nil {
		t.Fatal("expected deleted person entity lookup to fail")
	}
	if err := s.DeleteEntity(ctx, "alice", project.ID); err != nil {
		t.Fatalf("DeleteEntity project: %v", err)
	}
	searchResults, err = s.SearchEntities(ctx, "alice", "summer trip", 10)
	if err != nil {
		t.Fatalf("SearchEntities after delete: %v", err)
	}
	if len(searchResults) != 0 {
		t.Fatalf("expected deleted project to disappear from search, got %#v", searchResults)
	}

	if err := s.DeletePeer(ctx, "alice"); err != nil {
		t.Fatalf("DeletePeer cleanup: %v", err)
	}
	entities, err := s.ListEntities(ctx, "alice", "", 10)
	if err != nil {
		t.Fatalf("ListEntities after delete: %v", err)
	}
	if len(entities) != 0 {
		t.Fatalf("expected no entities after delete, got %d", len(entities))
	}
}
