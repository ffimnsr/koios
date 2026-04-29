package handler

import (
	"context"
	"fmt"

	"github.com/ffimnsr/koios/internal/plans"
)

func planPatch(title, description *string, status *plans.PlanStatus, steps *[]plans.StepInput) plans.Patch {
	return plans.Patch{Title: title, Description: description, Status: status, Steps: steps}
}

func (h *Handler) planCreate(peerID string, input plans.Input, ctx context.Context) (map[string]any, error) {
	if h.planStore == nil {
		return nil, fmt.Errorf("plans are not enabled")
	}
	plan, err := h.planStore.Create(ctx, peerID, input)
	if err != nil {
		return nil, err
	}
	return map[string]any{"ok": true, "plan": plan}, nil
}

func (h *Handler) planUpdate(peerID, id string, patch plans.Patch, ctx context.Context) (map[string]any, error) {
	if h.planStore == nil {
		return nil, fmt.Errorf("plans are not enabled")
	}
	plan, err := h.planStore.Update(ctx, peerID, id, patch)
	if err != nil {
		return nil, err
	}
	return map[string]any{"ok": true, "plan": plan}, nil
}

func (h *Handler) planStatus(peerID, id string, ctx context.Context) (map[string]any, error) {
	if h.planStore == nil {
		return nil, fmt.Errorf("plans are not enabled")
	}
	plan, err := h.planStore.Get(ctx, peerID, id)
	if err != nil {
		return nil, err
	}
	return map[string]any{"ok": true, "plan": plan}, nil
}

func (h *Handler) planCompleteStep(peerID, planID, stepID, notes string, ctx context.Context) (map[string]any, error) {
	if h.planStore == nil {
		return nil, fmt.Errorf("plans are not enabled")
	}
	plan, err := h.planStore.CompleteStep(ctx, peerID, planID, stepID, notes)
	if err != nil {
		return nil, err
	}
	return map[string]any{"ok": true, "plan": plan}, nil
}
