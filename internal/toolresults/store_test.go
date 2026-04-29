package toolresults_test

import (
	"context"
	"os"
	"testing"

	"github.com/ffimnsr/koios/internal/toolresults"
)

func TestStoreCreateAndGet(t *testing.T) {
	f, err := os.CreateTemp("", "toolresults-*.db")
	if err != nil {
		t.Fatal(err)
	}
	f.Close()
	defer os.Remove(f.Name())

	s, err := toolresults.New(f.Name())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	ctx := context.Background()
	r, err := s.Create(ctx, "peer1", toolresults.Input{
		SessionKey: "peer1::session1",
		ToolCallID: "call_abc",
		ToolName:   "memory.search",
		ArgsJSON:   `{"query":"foo"}`,
		ResultJSON: `{"results":[]}`,
		Summary:    "searched memory",
		IsError:    false,
		DurationMS: 42,
		Provenance: toolresults.Provenance{
			ExecutorKind: "builtin",
			ModelProfile: "default",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if r.ID == "" {
		t.Fatal("expected non-empty ID")
	}
	if r.PeerID != "peer1" {
		t.Fatalf("peer_id = %q", r.PeerID)
	}

	got, err := s.Get(ctx, "peer1", r.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.ToolName != "memory.search" {
		t.Fatalf("tool_name = %q", got.ToolName)
	}
	if got.DurationMS != 42 {
		t.Fatalf("duration_ms = %d", got.DurationMS)
	}
	if got.Provenance.ExecutorKind != "builtin" {
		t.Fatalf("provenance.executor_kind = %q", got.Provenance.ExecutorKind)
	}
}

func TestStoreGetWrongPeer(t *testing.T) {
	f, err := os.CreateTemp("", "toolresults-*.db")
	if err != nil {
		t.Fatal(err)
	}
	f.Close()
	defer os.Remove(f.Name())

	s, err := toolresults.New(f.Name())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	ctx := context.Background()
	r, err := s.Create(ctx, "peer1", toolresults.Input{ToolName: "time.now"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.Get(ctx, "peer2", r.ID)
	if err == nil {
		t.Fatal("expected error for wrong peer")
	}
}

func TestStoreList(t *testing.T) {
	f, err := os.CreateTemp("", "toolresults-*.db")
	if err != nil {
		t.Fatal(err)
	}
	f.Close()
	defer os.Remove(f.Name())

	s, err := toolresults.New(f.Name())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	ctx := context.Background()
	tools := []string{"time.now", "memory.search", "time.now"}
	for i, name := range tools {
		isErr := i == 2
		if _, err := s.Create(ctx, "peer1", toolresults.Input{
			ToolName: name,
			IsError:  isErr,
		}); err != nil {
			t.Fatal(err)
		}
	}

	// List all
	all, err := s.List(ctx, "peer1", toolresults.Filter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 3 {
		t.Fatalf("expected 3 records, got %d", len(all))
	}

	// Filter by tool name
	byName, err := s.List(ctx, "peer1", toolresults.Filter{ToolName: "time.now"})
	if err != nil {
		t.Fatal(err)
	}
	if len(byName) != 2 {
		t.Fatalf("expected 2 time.now records, got %d", len(byName))
	}

	// Filter by error state
	errTrue := true
	errs, err := s.List(ctx, "peer1", toolresults.Filter{IsError: &errTrue})
	if err != nil {
		t.Fatal(err)
	}
	if len(errs) != 1 {
		t.Fatalf("expected 1 error record, got %d", len(errs))
	}
}
