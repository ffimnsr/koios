package handler

import (
	"context"
	"fmt"
	"strings"

	"github.com/ffimnsr/koios/internal/tasks"
)

func waitingPatch(title *string, details *string, waitingFor *string, followUpAt *int64, remindAt *int64, snoozeUntil *int64, sourceSessionKey *string, sourceExcerpt *string) tasks.WaitingOnPatch {
	return tasks.WaitingOnPatch{
		Title:            title,
		Details:          details,
		WaitingFor:       waitingFor,
		FollowUpAt:       followUpAt,
		RemindAt:         remindAt,
		SnoozeUntil:      snoozeUntil,
		SourceSessionKey: sourceSessionKey,
		SourceExcerpt:    sourceExcerpt,
	}
}

func (h *Handler) waitingCreate(peerID string, input tasks.WaitingOnInput, ctx context.Context) (map[string]any, error) {
	if h.taskStore == nil {
		return nil, fmt.Errorf("tasks are not enabled")
	}
	item, err := h.taskStore.CreateWaitingOn(ctx, peerID, input)
	if err != nil {
		return nil, err
	}
	return map[string]any{"ok": true, "waiting": item}, nil
}

func (h *Handler) waitingList(peerID string, limit int, status string, ctx context.Context) (map[string]any, error) {
	if h.taskStore == nil {
		return nil, fmt.Errorf("tasks are not enabled")
	}
	if limit <= 0 {
		limit = 50
	}
	items, err := h.taskStore.ListWaitingOns(ctx, peerID, limit, tasks.WaitingStatus(status))
	if err != nil {
		return nil, err
	}
	return map[string]any{"count": len(items), "waiting": items, "status": strings.TrimSpace(status)}, nil
}

func (h *Handler) waitingGet(peerID, id string, ctx context.Context) (map[string]any, error) {
	if h.taskStore == nil {
		return nil, fmt.Errorf("tasks are not enabled")
	}
	if strings.TrimSpace(id) == "" {
		return nil, fmt.Errorf("id is required")
	}
	item, err := h.taskStore.GetWaitingOn(ctx, peerID, id)
	if err != nil {
		return nil, err
	}
	return map[string]any{"waiting": item}, nil
}

func (h *Handler) waitingUpdate(peerID, id string, patch tasks.WaitingOnPatch, ctx context.Context) (map[string]any, error) {
	if h.taskStore == nil {
		return nil, fmt.Errorf("tasks are not enabled")
	}
	if strings.TrimSpace(id) == "" {
		return nil, fmt.Errorf("id is required")
	}
	item, err := h.taskStore.UpdateWaitingOn(ctx, peerID, id, patch)
	if err != nil {
		return nil, err
	}
	return map[string]any{"ok": true, "waiting": item}, nil
}

func (h *Handler) waitingSnooze(peerID, id string, until int64, ctx context.Context) (map[string]any, error) {
	if h.taskStore == nil {
		return nil, fmt.Errorf("tasks are not enabled")
	}
	item, err := h.taskStore.SnoozeWaitingOn(ctx, peerID, id, until)
	if err != nil {
		return nil, err
	}
	return map[string]any{"ok": true, "waiting": item}, nil
}

func (h *Handler) waitingResolve(peerID, id string, ctx context.Context) (map[string]any, error) {
	if h.taskStore == nil {
		return nil, fmt.Errorf("tasks are not enabled")
	}
	item, err := h.taskStore.ResolveWaitingOn(ctx, peerID, id)
	if err != nil {
		return nil, err
	}
	return map[string]any{"ok": true, "waiting": item}, nil
}

func (h *Handler) waitingReopen(peerID, id string, ctx context.Context) (map[string]any, error) {
	if h.taskStore == nil {
		return nil, fmt.Errorf("tasks are not enabled")
	}
	item, err := h.taskStore.ReopenWaitingOn(ctx, peerID, id)
	if err != nil {
		return nil, err
	}
	return map[string]any{"ok": true, "waiting": item}, nil
}
