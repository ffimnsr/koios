package usage_test

import (
	"testing"

	"github.com/ffimnsr/koios/internal/types"
	"github.com/ffimnsr/koios/internal/usage"
)

func TestStoreAdd(t *testing.T) {
	s := usage.New()

	s.Add("alice", types.Usage{PromptTokens: 10, CompletionTokens: 5, TotalTokens: 15})
	s.Add("alice", types.Usage{PromptTokens: 20, CompletionTokens: 10, TotalTokens: 30})
	s.Add("bob", types.Usage{PromptTokens: 5, CompletionTokens: 3, TotalTokens: 8})

	alice, ok := s.Get("alice")
	if !ok {
		t.Fatal("expected alice usage to exist")
	}
	if alice.PromptTokens != 30 {
		t.Errorf("alice prompt tokens: got %d, want 30", alice.PromptTokens)
	}
	if alice.CompletionTokens != 15 {
		t.Errorf("alice completion tokens: got %d, want 15", alice.CompletionTokens)
	}
	if alice.TotalTokens != 45 {
		t.Errorf("alice total tokens: got %d, want 45", alice.TotalTokens)
	}
	if alice.Turns != 2 {
		t.Errorf("alice turns: got %d, want 2", alice.Turns)
	}
}

func TestStoreTotals(t *testing.T) {
	s := usage.New()
	s.Add("a", types.Usage{PromptTokens: 10, CompletionTokens: 5, TotalTokens: 15})
	s.Add("b", types.Usage{PromptTokens: 20, CompletionTokens: 10, TotalTokens: 30})

	totals := s.Totals()
	if totals.PromptTokens != 30 {
		t.Errorf("totals prompt tokens: got %d, want 30", totals.PromptTokens)
	}
	if totals.TotalTokens != 45 {
		t.Errorf("totals total tokens: got %d, want 45", totals.TotalTokens)
	}
	if totals.Turns != 2 {
		t.Errorf("totals turns: got %d, want 2", totals.Turns)
	}
}

func TestStoreAll(t *testing.T) {
	s := usage.New()
	s.Add("low", types.Usage{PromptTokens: 1, CompletionTokens: 1, TotalTokens: 2})
	s.Add("high", types.Usage{PromptTokens: 50, CompletionTokens: 50, TotalTokens: 100})

	all := s.All()
	if len(all) != 2 {
		t.Fatalf("expected 2 sessions, got %d", len(all))
	}
	if all[0].PeerID != "high" {
		t.Errorf("expected highest-usage first; got %s", all[0].PeerID)
	}
}

func TestStoreNoop(t *testing.T) {
	s := usage.New()
	// empty peerID should be silently ignored
	s.Add("", types.Usage{PromptTokens: 10, CompletionTokens: 5, TotalTokens: 15})
	// zero tokens should be silently ignored
	s.Add("noone", types.Usage{})
	if len(s.All()) != 0 {
		t.Error("expected no entries for no-op adds")
	}
}

func TestStoreGetMissing(t *testing.T) {
	s := usage.New()
	_, ok := s.Get("missing")
	if ok {
		t.Error("expected false for missing peer")
	}
}
