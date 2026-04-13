package subagent

import (
	"testing"
	"time"
)

func TestRegistry_PersistAndReload(t *testing.T) {
	dir := t.TempDir()
	reg, err := NewRegistry(dir)
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	rec := reg.Spawn(SpawnRequest{PeerID: "peer", Task: "do work", Model: "model"}, "peer::child")
	if rec.ID == "" {
		t.Fatal("spawn did not assign an id")
	}
	if _, ok := reg.Update(rec.ID, func(r *RunRecord) {
		r.Status = StatusCompleted
		r.FinishedAt = time.Now().UTC()
	}); !ok {
		t.Fatal("update failed")
	}

	reloaded, err := NewRegistry(dir)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	got, ok := reloaded.Get(rec.ID)
	if !ok {
		t.Fatal("expected record after reload")
	}
	if got.Task != "do work" || got.SessionKey != "peer::child" {
		t.Fatalf("unexpected reloaded record: %+v", got)
	}
	if got.Status != StatusCompleted {
		t.Fatalf("unexpected status after reload: %s", got.Status)
	}
}
