package artifacts_test

import (
	"context"
	"os"
	"testing"

	"github.com/ffimnsr/koios/internal/artifacts"
)

func newTestStore(t *testing.T) *artifacts.Store {
	t.Helper()
	f, err := os.CreateTemp(t.TempDir(), "artifacts-*.db")
	if err != nil {
		t.Fatalf("temp file: %v", err)
	}
	f.Close()
	s, err := artifacts.New(f.Name())
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestArtifactCreateGet(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	a, err := s.Create(ctx, "peer1", artifacts.Input{
		Kind:    "report",
		Title:   "Q1 Report",
		Content: "Revenue grew 10%.",
		Labels:  []string{"finance", "q1"},
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if a.ID == "" {
		t.Fatal("expected non-empty ID")
	}
	if a.Kind != "report" {
		t.Errorf("kind = %q, want report", a.Kind)
	}

	got, err := s.Get(ctx, "peer1", a.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Title != "Q1 Report" {
		t.Errorf("title = %q, want Q1 Report", got.Title)
	}
	if len(got.Labels) != 2 {
		t.Errorf("labels = %v, want 2", got.Labels)
	}
}

func TestArtifactGetWrongPeer(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	a, err := s.Create(ctx, "peer1", artifacts.Input{Title: "doc", Content: "body"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	_, err = s.Get(ctx, "peer2", a.ID)
	if err == nil {
		t.Fatal("expected error for wrong peer")
	}
}

func TestArtifactList(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	for _, kind := range []string{"spec", "spec", "report"} {
		if _, err := s.Create(ctx, "peer1", artifacts.Input{Kind: kind, Title: "t", Content: "c"}); err != nil {
			t.Fatalf("create: %v", err)
		}
	}

	all, err := s.List(ctx, "peer1", "", 10)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(all) != 3 {
		t.Errorf("list len = %d, want 3", len(all))
	}

	specs, err := s.List(ctx, "peer1", "spec", 10)
	if err != nil {
		t.Fatalf("list spec: %v", err)
	}
	if len(specs) != 2 {
		t.Errorf("spec list len = %d, want 2", len(specs))
	}
}

func TestArtifactUpdate(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	a, err := s.Create(ctx, "peer1", artifacts.Input{Title: "draft", Content: "v1"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	newContent := "v2"
	updated, err := s.Update(ctx, "peer1", a.ID, artifacts.Patch{Content: &newContent})
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if updated.Content != "v2" {
		t.Errorf("content = %q, want v2", updated.Content)
	}
	if updated.Title != "draft" {
		t.Errorf("title changed unexpectedly to %q", updated.Title)
	}
}

func TestArtifactTitleRequired(t *testing.T) {
	s := newTestStore(t)
	_, err := s.Create(context.Background(), "peer1", artifacts.Input{Content: "no title"})
	if err == nil {
		t.Fatal("expected error for empty title")
	}
}
