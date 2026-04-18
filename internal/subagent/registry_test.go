package subagent

import (
	"testing"
	"time"

	"github.com/ffimnsr/koios/internal/runledger"
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

func TestRegistry_UpdatesUnifiedLedgerMetadata(t *testing.T) {
	dir := t.TempDir()
	reg, err := NewRegistry(dir)
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	ledger, err := runledger.New(t.TempDir())
	if err != nil {
		t.Fatalf("runledger.New: %v", err)
	}
	defer ledger.Close()

	reg.SetLedger(runledger.NewSubagentAdapter(ledger))
	rec := reg.Spawn(SpawnRequest{
		PeerID:      "peer",
		ParentRunID: "parent-run",
		Task:        "do work",
		Model:       "model",
	}, "peer::child")

	finishedAt := time.Now().UTC()
	if _, ok := reg.Update(rec.ID, func(r *RunRecord) {
		r.SubTurn.ToolCalls = 4
		r.SubTurn.Steps = 2
		r.Status = StatusCompleted
		r.FinishedAt = finishedAt
	}); !ok {
		t.Fatal("update failed")
	}

	got, ok := ledger.Get(rec.ID)
	if !ok {
		t.Fatal("expected ledger record")
	}
	if got.ParentID != "parent-run" {
		t.Fatalf("expected parent-run, got %q", got.ParentID)
	}
	if got.ToolCalls != 4 {
		t.Fatalf("expected 4 tool calls, got %d", got.ToolCalls)
	}
	if got.Status != runledger.StatusCompleted {
		t.Fatalf("expected completed status, got %q", got.Status)
	}
}
