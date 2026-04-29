package plans

import (
	"context"
	"path/filepath"
	"testing"
)

func TestPlansCRUD(t *testing.T) {
	dir := t.TempDir()
	s, err := New(filepath.Join(dir, "plans.db"))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx := context.Background()
	const peer = "alice"

	// Create
	plan, err := s.Create(ctx, peer, Input{
		Title:       "Ship feature X",
		Description: "Full rollout",
		Steps: []StepInput{
			{Title: "Write tests"},
			{Title: "Deploy"},
		},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if plan.ID == "" {
		t.Fatal("expected non-empty plan ID")
	}
	if len(plan.Steps) != 2 {
		t.Fatalf("expected 2 steps, got %d", len(plan.Steps))
	}
	if plan.Steps[0].Status != StepPending {
		t.Errorf("expected pending step, got %q", plan.Steps[0].Status)
	}

	// Get
	got, err := s.Get(ctx, peer, plan.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Title != "Ship feature X" {
		t.Errorf("title mismatch: %q", got.Title)
	}

	// List
	list, err := s.List(ctx, peer, 20, "")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 1 {
		t.Errorf("expected 1 plan, got %d", len(list))
	}

	// Update title
	newTitle := "Ship feature Y"
	updated, err := s.Update(ctx, peer, plan.ID, Patch{Title: &newTitle})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if updated.Title != "Ship feature Y" {
		t.Errorf("updated title mismatch: %q", updated.Title)
	}

	// CompleteStep — first step
	stepID := plan.Steps[0].ID
	result, err := s.CompleteStep(ctx, peer, plan.ID, stepID, "done with tests")
	if err != nil {
		t.Fatalf("CompleteStep: %v", err)
	}
	if result.Steps[0].Status != StepDone {
		t.Errorf("step status mismatch: %q", result.Steps[0].Status)
	}

	// Complete second step — plan should auto-complete
	step2ID := plan.Steps[1].ID
	final, err := s.CompleteStep(ctx, peer, plan.ID, step2ID, "")
	if err != nil {
		t.Fatalf("CompleteStep second: %v", err)
	}
	if final.Status != PlanCompleted {
		t.Errorf("expected plan completed, got %q", final.Status)
	}

	// Delete
	if err := s.Delete(ctx, peer, plan.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	list2, err := s.List(ctx, peer, 20, "")
	if err != nil {
		t.Fatalf("List after delete: %v", err)
	}
	if len(list2) != 0 {
		t.Errorf("expected 0 plans after delete, got %d", len(list2))
	}
}

func TestPlansCompleteStepNotFound(t *testing.T) {
	dir := t.TempDir()
	s, err := New(filepath.Join(dir, "plans.db"))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx := context.Background()
	_, err = s.CompleteStep(ctx, "alice", "no-such-plan", "no-such-step", "")
	if err == nil {
		t.Error("expected error for missing plan")
	}
}
