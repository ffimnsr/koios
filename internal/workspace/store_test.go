package workspace

import (
	"os"
	"path/filepath"
	"testing"
)

func TestManagerReadWriteListDelete(t *testing.T) {
	dir := t.TempDir()
	m, err := New(dir, true, 1024)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.Write("alice", "notes/todo.md", "hello", false); err != nil {
		t.Fatal(err)
	}
	content, err := m.Read("alice", "notes/todo.md")
	if err != nil {
		t.Fatal(err)
	}
	if content != "hello" {
		t.Fatalf("content=%q", content)
	}
	entries, err := m.List("alice", "notes", false, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Path != "notes/todo.md" {
		t.Fatalf("entries=%+v", entries)
	}
	if err := m.Delete("alice", "notes/todo.md", false); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(m.PeerRoot("alice"), "notes", "todo.md")); !os.IsNotExist(err) {
		t.Fatalf("expected deleted, err=%v", err)
	}
}

func TestManagerBlocksTraversal(t *testing.T) {
	dir := t.TempDir()
	m, err := New(dir, true, 1024)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.Write("alice", "../escape.txt", "x", false); err == nil {
		t.Fatal("expected traversal error")
	}
}

func TestManagerEdit(t *testing.T) {
	dir := t.TempDir()
	m, err := New(dir, true, 1024)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.Write("alice", "notes/todo.md", "hello world\nhello world\n", false); err != nil {
		t.Fatal(err)
	}
	result, err := m.Edit("alice", "notes/todo.md", "hello", "goodbye", false)
	if err != nil {
		t.Fatal(err)
	}
	if result.Replacements != 1 {
		t.Fatalf("replacements=%d", result.Replacements)
	}
	content, err := m.Read("alice", "notes/todo.md")
	if err != nil {
		t.Fatal(err)
	}
	if content != "goodbye world\nhello world\n" {
		t.Fatalf("content=%q", content)
	}
}
