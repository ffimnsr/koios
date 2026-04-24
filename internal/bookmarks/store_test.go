package bookmarks_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/ffimnsr/koios/internal/bookmarks"
)

func newTestStore(t *testing.T) *bookmarks.Store {
	t.Helper()
	store, err := bookmarks.New(filepath.Join(t.TempDir(), "bookmarks.db"))
	if err != nil {
		t.Fatalf("bookmarks.New: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func TestStoreBookmarkLifecycle(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	created, err := store.Create(ctx, "alice", bookmarks.Input{
		Title:            "Deployment checklist",
		Content:          "Remember to run smoke tests and verify rollback.",
		Labels:           []string{"Ops", "release", "ops"},
		ReminderAt:       time.Now().Add(time.Hour).Unix(),
		SourceKind:       bookmarks.SourceKindSessionRange,
		SourceSessionKey: "alice::main",
		SourceStartIndex: 2,
		SourceEndIndex:   3,
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if len(created.Labels) != 2 {
		t.Fatalf("labels = %#v, want 2 normalized labels", created.Labels)
	}
	if created.SourceExcerpt == "" {
		t.Fatal("expected source excerpt to be populated")
	}

	got, err := store.Get(ctx, "alice", created.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Title != created.Title {
		t.Fatalf("Title = %q, want %q", got.Title, created.Title)
	}
	if got.SourceStartIndex != 2 || got.SourceEndIndex != 3 {
		t.Fatalf("unexpected source indexes: %#v", got)
	}

	list, err := store.List(ctx, "alice", bookmarks.Filter{Limit: 10, Label: "ops"})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 1 || list[0].ID != created.ID {
		t.Fatalf("unexpected list results: %#v", list)
	}

	search, err := store.Search(ctx, "alice", "smoke tests", 10)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(search) != 1 || search[0].ID != created.ID {
		t.Fatalf("unexpected search results: %#v", search)
	}

	newTitle := "Release checklist"
	newLabels := []string{"release", "urgent"}
	updated, err := store.Update(ctx, "alice", created.ID, bookmarks.Patch{
		Title:  &newTitle,
		Labels: &newLabels,
	})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if updated.Title != newTitle {
		t.Fatalf("updated title = %q, want %q", updated.Title, newTitle)
	}
	if len(updated.Labels) != 2 || updated.Labels[1] != "urgent" {
		t.Fatalf("updated labels = %#v", updated.Labels)
	}

	if err := store.Delete(ctx, "alice", created.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := store.Get(ctx, "alice", created.ID); err == nil {
		t.Fatal("expected deleted bookmark lookup to fail")
	}
}

func TestStoreRejectsInvalidBookmark(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	if _, err := store.Create(ctx, "alice", bookmarks.Input{Title: "", Content: "hello"}); err == nil {
		t.Fatal("expected empty title to fail")
	}
	if _, err := store.Create(ctx, "alice", bookmarks.Input{Title: "hello", Content: "", SourceStartIndex: 3, SourceEndIndex: 1}); err == nil {
		t.Fatal("expected invalid source range to fail")
	}
}
