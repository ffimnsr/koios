package notes

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestNotesCRUD(t *testing.T) {
	dir := t.TempDir()
	s, err := New(filepath.Join(dir, "notes.db"))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx := context.Background()
	const peer = "alice"

	// Create
	n, err := s.Create(ctx, peer, Input{Title: "My Note", Content: "hello world", Labels: []string{"work"}})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if n.ID == "" {
		t.Fatal("expected non-empty ID")
	}
	if n.Title != "My Note" {
		t.Errorf("title mismatch: %q", n.Title)
	}

	// Get
	got, err := s.Get(ctx, peer, n.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Content != "hello world" {
		t.Errorf("content mismatch: %q", got.Content)
	}

	// Update
	newTitle := "Updated"
	updated, err := s.Update(ctx, peer, n.ID, Patch{Title: &newTitle})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if updated.Title != "Updated" {
		t.Errorf("updated title mismatch: %q", updated.Title)
	}

	// Search
	results, err := s.Search(ctx, peer, "hello", 10)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(results) != 1 {
		t.Errorf("expected 1 result, got %d", len(results))
	}

	// Delete
	if err := s.Delete(ctx, peer, n.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := s.Get(ctx, peer, n.ID); !os.IsNotExist(err) && err == nil {
		t.Error("expected not-found after delete")
	}
}

func TestNotesSearchNoResults(t *testing.T) {
	dir := t.TempDir()
	s, err := New(filepath.Join(dir, "notes.db"))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx := context.Background()
	results, err := s.Search(ctx, "bob", "anything", 10)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(results) != 0 {
		t.Errorf("expected 0 results, got %d", len(results))
	}
}
