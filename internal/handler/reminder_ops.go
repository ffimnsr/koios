package handler

import (
	"context"
	"fmt"

	"github.com/ffimnsr/koios/internal/reminder"
)

func (h *Handler) reminderCreate(peerID string, input reminder.Input, ctx context.Context) (map[string]any, error) {
	if h.reminderStore == nil {
		return nil, fmt.Errorf("reminder store is not enabled")
	}
	r, err := h.reminderStore.Create(ctx, peerID, input)
	if err != nil {
		return nil, err
	}
	return map[string]any{"ok": true, "reminder": r}, nil
}

func (h *Handler) reminderList(peerID string, pendingOnly bool, limit int, ctx context.Context) (map[string]any, error) {
	if h.reminderStore == nil {
		return nil, fmt.Errorf("reminder store is not enabled")
	}
	items, err := h.reminderStore.List(ctx, peerID, pendingOnly, limit)
	if err != nil {
		return nil, err
	}
	if items == nil {
		items = []reminder.Reminder{}
	}
	return map[string]any{"count": len(items), "reminders": items}, nil
}

func (h *Handler) reminderComplete(peerID, id string, ctx context.Context) (map[string]any, error) {
	if h.reminderStore == nil {
		return nil, fmt.Errorf("reminder store is not enabled")
	}
	r, err := h.reminderStore.Complete(ctx, peerID, id)
	if err != nil {
		return nil, err
	}
	return map[string]any{"ok": true, "reminder": r}, nil
}
