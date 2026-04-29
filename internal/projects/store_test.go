package projects

import (
	"context"
	"path/filepath"
	"testing"
)

func TestProjectCRUD(t *testing.T) {
	dir := t.TempDir()
	s, err := New(filepath.Join(dir, "projects.db"))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer s.Close()
	ctx := context.Background()
	const peer = "alice"

	// Create
	p, err := s.Create(ctx, peer, Input{
		Title:       "Website Redesign",
		Description: "Full overhaul of the marketing site",
		Labels:      []string{"marketing", "design"},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if p.ID == "" {
		t.Fatal("expected non-empty project ID")
	}
	if p.Status != ProjectActive {
		t.Errorf("expected active status, got %q", p.Status)
	}
	if len(p.Labels) != 2 {
		t.Errorf("expected 2 labels, got %d", len(p.Labels))
	}

	// Get
	got, err := s.Get(ctx, peer, p.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Title != "Website Redesign" {
		t.Errorf("title mismatch: %q", got.Title)
	}
	if len(got.LinkedTasks) != 0 {
		t.Errorf("expected no linked tasks, got %d", len(got.LinkedTasks))
	}

	// Wrong peer is rejected
	_, err = s.Get(ctx, "bob", p.ID)
	if err == nil {
		t.Error("expected error for wrong peer")
	}

	// List
	list, err := s.List(ctx, peer, 20, "")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 1 {
		t.Errorf("expected 1 project, got %d", len(list))
	}

	// List with status filter
	active, err := s.List(ctx, peer, 20, ProjectActive)
	if err != nil {
		t.Fatalf("List active: %v", err)
	}
	if len(active) != 1 {
		t.Errorf("expected 1 active project, got %d", len(active))
	}
	archived, err := s.List(ctx, peer, 20, ProjectArchived)
	if err != nil {
		t.Fatalf("List archived: %v", err)
	}
	if len(archived) != 0 {
		t.Errorf("expected 0 archived projects, got %d", len(archived))
	}

	// Update
	newTitle := "Marketing Site Overhaul"
	updated, err := s.Update(ctx, peer, p.ID, Patch{Title: &newTitle})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if updated.Title != newTitle {
		t.Errorf("updated title mismatch: %q", updated.Title)
	}

	// LinkTask
	linked, err := s.LinkTask(ctx, peer, p.ID, "task-abc")
	if err != nil {
		t.Fatalf("LinkTask: %v", err)
	}
	if len(linked.LinkedTasks) != 1 || linked.LinkedTasks[0] != "task-abc" {
		t.Errorf("linked task mismatch: %v", linked.LinkedTasks)
	}

	// LinkTask is idempotent
	linked2, err := s.LinkTask(ctx, peer, p.ID, "task-abc")
	if err != nil {
		t.Fatalf("LinkTask idempotent: %v", err)
	}
	if len(linked2.LinkedTasks) != 1 {
		t.Errorf("expected 1 linked task after duplicate link, got %d", len(linked2.LinkedTasks))
	}

	// Link a second task
	linked3, err := s.LinkTask(ctx, peer, p.ID, "task-def")
	if err != nil {
		t.Fatalf("LinkTask second: %v", err)
	}
	if len(linked3.LinkedTasks) != 2 {
		t.Errorf("expected 2 linked tasks, got %d", len(linked3.LinkedTasks))
	}

	// UnlinkTask
	unlinked, err := s.UnlinkTask(ctx, peer, p.ID, "task-abc")
	if err != nil {
		t.Fatalf("UnlinkTask: %v", err)
	}
	if len(unlinked.LinkedTasks) != 1 || unlinked.LinkedTasks[0] != "task-def" {
		t.Errorf("unlinked tasks mismatch: %v", unlinked.LinkedTasks)
	}

	// Archive
	archived2, err := s.Archive(ctx, peer, p.ID)
	if err != nil {
		t.Fatalf("Archive: %v", err)
	}
	if archived2.Status != ProjectArchived {
		t.Errorf("expected archived, got %q", archived2.Status)
	}

	// Unarchive
	unarchived, err := s.Unarchive(ctx, peer, p.ID)
	if err != nil {
		t.Fatalf("Unarchive: %v", err)
	}
	if unarchived.Status != ProjectActive {
		t.Errorf("expected active, got %q", unarchived.Status)
	}

	// Delete
	if err := s.Delete(ctx, peer, p.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	list2, err := s.List(ctx, peer, 20, "")
	if err != nil {
		t.Fatalf("List after delete: %v", err)
	}
	if len(list2) != 0 {
		t.Errorf("expected 0 projects after delete, got %d", len(list2))
	}
}

func TestProjectCreate_EmptyTitle(t *testing.T) {
	dir := t.TempDir()
	s, err := New(filepath.Join(dir, "projects.db"))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer s.Close()
	_, err = s.Create(context.Background(), "alice", Input{Title: "  "})
	if err == nil {
		t.Error("expected error for empty title")
	}
}
