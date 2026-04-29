package decisions_test

import (
	"context"
	"os"
	"testing"

	"github.com/ffimnsr/koios/internal/decisions"
)

func newTestStore(t *testing.T) *decisions.Store {
	t.Helper()
	f, err := os.CreateTemp(t.TempDir(), "decisions-*.db")
	if err != nil {
		t.Fatalf("temp file: %v", err)
	}
	f.Close()
	s, err := decisions.New(f.Name())
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestDecisionRecord(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	d, err := s.Record(ctx, "peer1", decisions.Input{
		Title:        "Auth library choice",
		Decision:     "Use JWT",
		Rationale:    "Stateless, easy to validate",
		Alternatives: "Sessions, API keys",
		Owner:        "alice",
	})
	if err != nil {
		t.Fatalf("record: %v", err)
	}
	if d.ID == "" {
		t.Fatal("expected non-empty ID")
	}
	if d.Decision != "Use JWT" {
		t.Errorf("decision = %q, want 'Use JWT'", d.Decision)
	}
}

func TestDecisionList(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	for i := 0; i < 3; i++ {
		if _, err := s.Record(ctx, "peer1", decisions.Input{
			Title:    "Decision",
			Decision: "Choice",
		}); err != nil {
			t.Fatalf("record: %v", err)
		}
	}
	// Another peer's decision.
	if _, err := s.Record(ctx, "peer2", decisions.Input{Title: "Other", Decision: "X"}); err != nil {
		t.Fatalf("record: %v", err)
	}

	list, err := s.List(ctx, "peer1", 10)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list) != 3 {
		t.Errorf("list len = %d, want 3", len(list))
	}
}

func TestDecisionSearch(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	if _, err := s.Record(ctx, "peer1", decisions.Input{
		Title:    "Database engine",
		Decision: "SQLite for embedded",
		Owner:    "bob",
	}); err != nil {
		t.Fatalf("record: %v", err)
	}
	if _, err := s.Record(ctx, "peer1", decisions.Input{
		Title:    "Cache layer",
		Decision: "Redis",
	}); err != nil {
		t.Fatalf("record: %v", err)
	}

	results, err := s.Search(ctx, "peer1", "sqlite", 10)
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(results) != 1 {
		t.Errorf("results len = %d, want 1", len(results))
	}
}

func TestDecisionValidation(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	if _, err := s.Record(ctx, "peer1", decisions.Input{Decision: "no title"}); err == nil {
		t.Fatal("expected error for empty title")
	}
	if _, err := s.Record(ctx, "peer1", decisions.Input{Title: "no decision"}); err == nil {
		t.Fatal("expected error for empty decision")
	}
}
