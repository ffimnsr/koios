package preferences_test

import (
	"context"
	"os"
	"testing"

	"github.com/ffimnsr/koios/internal/preferences"
)

func newTestStore(t *testing.T) *preferences.Store {
	t.Helper()
	f, err := os.CreateTemp(t.TempDir(), "preferences-*.db")
	if err != nil {
		t.Fatalf("temp file: %v", err)
	}
	f.Close()
	s, err := preferences.New(f.Name())
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestPreferenceSetGet(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	p, err := s.Set(ctx, "peer1", preferences.Input{
		Key:        "theme",
		Value:      "dark",
		Provenance: "user-stated",
	})
	if err != nil {
		t.Fatalf("set: %v", err)
	}
	if p.ID == "" {
		t.Fatal("expected non-empty ID")
	}
	if p.Scope != "global" {
		t.Errorf("scope = %q, want global", p.Scope)
	}
	if p.Confidence != 1.0 {
		t.Errorf("confidence = %v, want 1.0", p.Confidence)
	}

	got, err := s.Get(ctx, "peer1", "theme", "")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Value != "dark" {
		t.Errorf("value = %q, want dark", got.Value)
	}
}

func TestPreferenceUpsert(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	p1, err := s.Set(ctx, "peer1", preferences.Input{Key: "lang", Value: "en"})
	if err != nil {
		t.Fatalf("set 1: %v", err)
	}
	p2, err := s.Set(ctx, "peer1", preferences.Input{Key: "lang", Value: "fr"})
	if err != nil {
		t.Fatalf("set 2: %v", err)
	}
	// Should be the same record updated in place.
	if p1.ID != p2.ID {
		t.Errorf("upsert should preserve ID: got %s vs %s", p1.ID, p2.ID)
	}
	if p2.Value != "fr" {
		t.Errorf("value = %q, want fr", p2.Value)
	}
}

func TestPreferenceScopeIsolation(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	if _, err := s.Set(ctx, "peer1", preferences.Input{Key: "mode", Value: "v1", Scope: "global"}); err != nil {
		t.Fatalf("set global: %v", err)
	}
	if _, err := s.Set(ctx, "peer1", preferences.Input{Key: "mode", Value: "v2", Scope: "session"}); err != nil {
		t.Fatalf("set session: %v", err)
	}

	g, err := s.Get(ctx, "peer1", "mode", "global")
	if err != nil {
		t.Fatalf("get global: %v", err)
	}
	if g.Value != "v1" {
		t.Errorf("global value = %q, want v1", g.Value)
	}

	sv, err := s.Get(ctx, "peer1", "mode", "session")
	if err != nil {
		t.Fatalf("get session: %v", err)
	}
	if sv.Value != "v2" {
		t.Errorf("session value = %q, want v2", sv.Value)
	}
}

func TestPreferenceList(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	for _, key := range []string{"a", "b", "c"} {
		if _, err := s.Set(ctx, "peer1", preferences.Input{Key: key, Value: "1"}); err != nil {
			t.Fatalf("set %s: %v", key, err)
		}
	}
	if _, err := s.Set(ctx, "peer1", preferences.Input{Key: "d", Value: "1", Scope: "session"}); err != nil {
		t.Fatalf("set d: %v", err)
	}

	all, err := s.List(ctx, "peer1", "", 50)
	if err != nil {
		t.Fatalf("list all: %v", err)
	}
	if len(all) != 4 {
		t.Errorf("list len = %d, want 4", len(all))
	}

	global, err := s.List(ctx, "peer1", "global", 50)
	if err != nil {
		t.Fatalf("list global: %v", err)
	}
	if len(global) != 3 {
		t.Errorf("global list len = %d, want 3", len(global))
	}
}

func TestPreferenceValidation(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	if _, err := s.Set(ctx, "peer1", preferences.Input{Value: "no key"}); err == nil {
		t.Fatal("expected error for empty key")
	}
	if _, err := s.Set(ctx, "peer1", preferences.Input{Key: "no-value"}); err == nil {
		t.Fatal("expected error for empty value")
	}
}
