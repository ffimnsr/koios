package scratchpad

import "testing"

func TestScratchpadCreateGetUpdateClear(t *testing.T) {
	s := New()
	const peer, session = "alice", "sess1"

	// Create
	pad := s.Create(peer, session, "initial content")
	if pad.Content != "initial content" {
		t.Errorf("content mismatch: %q", pad.Content)
	}

	// Get
	got, err := s.Get(peer, session)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Content != "initial content" {
		t.Errorf("Get content mismatch: %q", got.Content)
	}

	// Create is idempotent
	same := s.Create(peer, session, "different content")
	if same.Content != "initial content" {
		t.Errorf("Create idempotency broken: %q", same.Content)
	}

	// Update
	updated := s.Update(peer, session, "updated content")
	if updated.Content != "updated content" {
		t.Errorf("Update content mismatch: %q", updated.Content)
	}

	// Update upserts when session doesn't exist yet
	_ = s.Update(peer, "new-session", "brand new")

	// Clear
	s.Clear(peer, session)
	if _, err := s.Get(peer, session); err == nil {
		t.Error("expected error after clear")
	}
}

func TestScratchpadGetMissing(t *testing.T) {
	s := New()
	_, err := s.Get("nobody", "ghost")
	if err == nil {
		t.Error("expected error for missing session")
	}
}
