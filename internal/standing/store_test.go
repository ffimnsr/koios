package standing

import (
	"os"
	"path/filepath"
	"testing"
)

func TestStoreSaveLoadDelete(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	if _, err := store.Save("peer:1", "Do the thing."); err != nil {
		t.Fatalf("Save: %v", err)
	}
	doc, err := store.Load("peer:1")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if doc == nil || doc.Content != "Do the thing." || doc.PeerID != "peer:1" {
		t.Fatalf("unexpected doc: %#v", doc)
	}
	if err := store.Delete("peer:1"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	doc, err = store.Load("peer:1")
	if err != nil {
		t.Fatalf("Load after delete: %v", err)
	}
	if doc != nil {
		t.Fatalf("expected nil doc after delete, got %#v", doc)
	}
}

func TestManagerEffectiveContentCombinesWorkspaceAndPeer(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, WorkspaceFilename), []byte("Workspace standing order"), 0o644); err != nil {
		t.Fatalf("write workspace standing orders: %v", err)
	}
	store, err := NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	if _, err := store.Save("peer-1", "Peer standing order"); err != nil {
		t.Fatalf("Save: %v", err)
	}
	manager := NewManager(store, dir)
	got, err := manager.EffectiveContent("peer-1")
	if err != nil {
		t.Fatalf("EffectiveContent: %v", err)
	}
	want := "Workspace standing order\n\nPeer standing order"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}
