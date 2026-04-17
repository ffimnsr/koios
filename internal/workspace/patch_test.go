package workspace

import "testing"

func TestManagerApplyPatchMultiHunkAndAddFile(t *testing.T) {
	dir := t.TempDir()
	m, err := New(dir, true, 1024)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.Write("alice", "notes/todo.md", "one\ntwo\nthree\n", false); err != nil {
		t.Fatal(err)
	}

	result, err := m.ApplyPatch("alice", `*** Begin Patch
*** Update File: notes/todo.md
@@
 one
-two
+TWO
 three
@@
 three
+four
*** Add File: notes/new.txt
+alpha
+beta
*** End Patch`)
	if err != nil {
		t.Fatal(err)
	}
	if result.Count != 2 {
		t.Fatalf("count=%d", result.Count)
	}

	content, err := m.Read("alice", "notes/todo.md")
	if err != nil {
		t.Fatal(err)
	}
	if content != "one\nTWO\nthree\nfour\n" {
		t.Fatalf("content=%q", content)
	}

	added, err := m.Read("alice", "notes/new.txt")
	if err != nil {
		t.Fatal(err)
	}
	if added != "alpha\nbeta\n" {
		t.Fatalf("added=%q", added)
	}
}

func TestManagerApplyPatchDelete(t *testing.T) {
	dir := t.TempDir()
	m, err := New(dir, true, 1024)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.Write("alice", "notes/todo.md", "one\ntwo\n", false); err != nil {
		t.Fatal(err)
	}

	result, err := m.ApplyPatch("alice", `*** Begin Patch
*** Delete File: notes/todo.md
*** End Patch`)
	if err != nil {
		t.Fatal(err)
	}
	if result.Count != 1 || len(result.Files) != 1 {
		t.Fatalf("unexpected result: %#v", result)
	}
	if _, err := m.Read("alice", "notes/todo.md"); err == nil {
		t.Fatal("expected deleted file to be unreadable")
	}
}
