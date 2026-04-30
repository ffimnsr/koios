package workspace

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestManagerPeerRootUsesPeersSubdir(t *testing.T) {
	dir := t.TempDir()
	m, err := New(dir, true, 1024)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := m.PeerRoot("alice"), filepath.Join(dir, "peers", "alice"); got != want {
		t.Fatalf("PeerRoot() = %q want %q", got, want)
	}
	if info, err := os.Stat(filepath.Join(dir, "peers")); err != nil || !info.IsDir() {
		t.Fatalf("expected peers root directory to exist, err=%v", err)
	}
}

func TestPeerDocumentLookupPathsPreferPeerThenDefaultThenRoot(t *testing.T) {
	root := "/workspace"
	got := PeerDocumentLookupPaths(root, "alice", "AGENTS.md")
	want := []string{
		filepath.Join(root, "peers", "alice", "AGENTS.md"),
		filepath.Join(root, "peers", DefaultPeerID, "AGENTS.md"),
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("PeerDocumentLookupPaths() = %#v want %#v", got, want)
	}
}

func TestPeerDocumentLookupPathsSanitizesPeerAndDedupesDefault(t *testing.T) {
	root := "/workspace"
	got := PeerDocumentLookupPaths(root, "", "USER.md")
	want := []string{
		filepath.Join(root, "peers", DefaultPeerID, "USER.md"),
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("PeerDocumentLookupPaths() = %#v want %#v", got, want)
	}

	got = PeerDocumentLookupPaths(root, DefaultPeerID, "USER.md")
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("PeerDocumentLookupPaths(default) = %#v want %#v", got, want)
	}
}

func TestManagerEnsurePeerCreatesPeerDir(t *testing.T) {
	dir := t.TempDir()
	m, err := New(dir, true, 1024)
	if err != nil {
		t.Fatal(err)
	}
	peerDir, err := m.EnsurePeer("alice")
	if err != nil {
		t.Fatal(err)
	}
	if got, want := peerDir, filepath.Join(dir, "peers", "alice"); got != want {
		t.Fatalf("EnsurePeer() = %q want %q", got, want)
	}
	if info, err := os.Stat(peerDir); err != nil || !info.IsDir() {
		t.Fatalf("expected peer directory to exist, err=%v", err)
	}
}

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

func TestManagerReadRange(t *testing.T) {
	dir := t.TempDir()
	m, err := New(dir, true, 1024)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.Write("alice", "notes/todo.md", "one\ntwo\nthree\nfour\n", false); err != nil {
		t.Fatal(err)
	}
	result, err := m.ReadRange("alice", "notes/todo.md", 2, 3)
	if err != nil {
		t.Fatal(err)
	}
	if result.Content != "two\nthree\n" {
		t.Fatalf("content=%q", result.Content)
	}
	if result.StartLine != 2 || result.EndLine != 3 || result.TotalLines != 4 {
		t.Fatalf("unexpected range metadata: %+v", result)
	}
}

func TestManagerGrep(t *testing.T) {
	dir := t.TempDir()
	m, err := New(dir, true, 1024)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.Write("alice", "notes/todo.md", "TODO first\nskip\nTODO second\n", false); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Write("alice", "notes/nested/ideas.md", "todo lowercase\n", false); err != nil {
		t.Fatal(err)
	}

	matches, err := m.Grep("alice", "notes", "TODO", true, 10, true, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 2 {
		t.Fatalf("expected 2 matches, got %#v", matches)
	}
	if matches[0].Path != "notes/todo.md" || matches[0].Line != 1 || matches[0].Column != 1 {
		t.Fatalf("unexpected first match: %#v", matches[0])
	}

	regexMatches, err := m.Grep("alice", "notes", "^todo", true, 10, false, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(regexMatches) != 3 || regexMatches[0].Path != "notes/nested/ideas.md" {
		t.Fatalf("unexpected regex matches: %#v", regexMatches)
	}
}

func TestManagerDiff(t *testing.T) {
	dir := t.TempDir()
	m, err := New(dir, true, 1024)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.Write("alice", "notes/todo.md", "one\ntwo\nthree\n", false); err != nil {
		t.Fatal(err)
	}

	result, err := m.Diff("alice", "notes/todo.md", "", "one\nTWO\nthree\n", 3)
	if err != nil {
		t.Fatal(err)
	}
	if !result.HasDiff {
		t.Fatal("expected diff")
	}
	if !strings.Contains(result.Diff, "--- notes/todo.md") || !strings.Contains(result.Diff, "+++ proposed") {
		t.Fatalf("unexpected diff header: %q", result.Diff)
	}
	if !strings.Contains(result.Diff, "-two") || !strings.Contains(result.Diff, "+TWO") {
		t.Fatalf("unexpected diff body: %q", result.Diff)
	}
}

func TestManagerTextOps(t *testing.T) {
	dir := t.TempDir()
	m, err := New(dir, true, 1024)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.Write("alice", "notes/list.txt", "delta\nbeta\nbeta\nAlpha\n", false); err != nil {
		t.Fatal(err)
	}

	head, err := m.Head("alice", "notes/list.txt", 2)
	if err != nil {
		t.Fatal(err)
	}
	if head.Content != "delta\nbeta\n" || head.StartLine != 1 || head.EndLine != 2 {
		t.Fatalf("unexpected head: %#v", head)
	}

	tail, err := m.Tail("alice", "notes/list.txt", 2)
	if err != nil {
		t.Fatal(err)
	}
	if tail.Content != "beta\nAlpha\n" || tail.StartLine != 3 || tail.EndLine != 4 {
		t.Fatalf("unexpected tail: %#v", tail)
	}

	sorted, err := m.SortLines("alice", "notes/list.txt", false, false)
	if err != nil {
		t.Fatal(err)
	}
	if sorted.Content != "Alpha\nbeta\nbeta\ndelta\n" {
		t.Fatalf("unexpected sorted content: %q", sorted.Content)
	}

	uniq, err := m.UniqLines("alice", "notes/list.txt", true, true)
	if err != nil {
		t.Fatal(err)
	}
	if uniq.Content != "1 delta\n2 beta\n1 Alpha\n" {
		t.Fatalf("unexpected uniq content: %q", uniq.Content)
	}
}
