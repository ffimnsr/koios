package tasks_test

import (
	"context"
	"testing"
	"time"

	"github.com/ffimnsr/koios/internal/tasks"
)

func TestWaitingOnLifecycle(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	followUpAt := time.Now().Add(48 * time.Hour).Unix()
	remindAt := time.Now().Add(24 * time.Hour).Unix()

	item, err := store.CreateWaitingOn(ctx, "alice", tasks.WaitingOnInput{
		Title:            "Vendor reply on security questionnaire",
		Details:          "Asked for the updated answers on Friday.",
		WaitingFor:       "vendor",
		FollowUpAt:       followUpAt,
		RemindAt:         remindAt,
		SourceSessionKey: "alice::main",
		SourceExcerpt:    "Waiting on the vendor to send the updated questionnaire.",
	})
	if err != nil {
		t.Fatalf("CreateWaitingOn: %v", err)
	}
	if item.Status != tasks.WaitingStatusOpen {
		t.Fatalf("status = %q, want open", item.Status)
	}

	loaded, err := store.GetWaitingOn(ctx, "alice", item.ID)
	if err != nil {
		t.Fatalf("GetWaitingOn: %v", err)
	}
	if loaded.WaitingFor != "vendor" {
		t.Fatalf("waiting_for = %q, want vendor", loaded.WaitingFor)
	}

	updatedDetails := "No response yet after the initial ask."
	updatedWaiter := "primary vendor contact"
	updated, err := store.UpdateWaitingOn(ctx, "alice", item.ID, tasks.WaitingOnPatch{
		Details:    &updatedDetails,
		WaitingFor: &updatedWaiter,
	})
	if err != nil {
		t.Fatalf("UpdateWaitingOn: %v", err)
	}
	if updated.WaitingFor != updatedWaiter {
		t.Fatalf("updated waiting_for = %q, want %q", updated.WaitingFor, updatedWaiter)
	}

	snoozeUntil := time.Now().Add(12 * time.Hour).Unix()
	snoozed, err := store.SnoozeWaitingOn(ctx, "alice", item.ID, snoozeUntil)
	if err != nil {
		t.Fatalf("SnoozeWaitingOn: %v", err)
	}
	if snoozed.Status != tasks.WaitingStatusSnoozed {
		t.Fatalf("snoozed status = %q, want snoozed", snoozed.Status)
	}

	resolved, err := store.ResolveWaitingOn(ctx, "alice", item.ID)
	if err != nil {
		t.Fatalf("ResolveWaitingOn: %v", err)
	}
	if resolved.Status != tasks.WaitingStatusResolved {
		t.Fatalf("resolved status = %q, want resolved", resolved.Status)
	}
	if resolved.ResolvedAt == 0 {
		t.Fatal("expected resolved_at to be set")
	}

	reopened, err := store.ReopenWaitingOn(ctx, "alice", item.ID)
	if err != nil {
		t.Fatalf("ReopenWaitingOn: %v", err)
	}
	if reopened.Status != tasks.WaitingStatusOpen {
		t.Fatalf("reopened status = %q, want open", reopened.Status)
	}
	if reopened.ResolvedAt != 0 {
		t.Fatalf("reopened resolved_at = %d, want 0", reopened.ResolvedAt)
	}

	items, err := store.ListWaitingOns(ctx, "alice", 10, tasks.WaitingStatusOpen)
	if err != nil {
		t.Fatalf("ListWaitingOns: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("list count = %d, want 1", len(items))
	}
	if items[0].SourceSessionKey != "alice::main" {
		t.Fatalf("source session key = %q, want alice::main", items[0].SourceSessionKey)
	}
}

func TestWaitingOnRequiresWaitingFor(t *testing.T) {
	store := newTestStore(t)
	_, err := store.CreateWaitingOn(context.Background(), "alice", tasks.WaitingOnInput{Title: "Missing owner"})
	if err == nil {
		t.Fatal("expected missing waiting_for validation error")
	}
}
