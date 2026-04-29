package handler

import (
	"context"
	"fmt"
	"strings"

	"github.com/ffimnsr/koios/internal/calendar"
)

func (h *Handler) calendarSourceCreate(peerID string, input calendar.SourceInput, ctx context.Context) (map[string]any, error) {
	if h.calendarStore == nil {
		return nil, fmt.Errorf("calendar is not enabled")
	}
	source, err := h.calendarStore.CreateSource(ctx, peerID, input)
	if err != nil {
		return nil, err
	}
	return map[string]any{"ok": true, "source": source}, nil
}

func (h *Handler) calendarSourceList(peerID string, enabledOnly bool, ctx context.Context) (map[string]any, error) {
	if h.calendarStore == nil {
		return nil, fmt.Errorf("calendar is not enabled")
	}
	sources, err := h.calendarStore.ListSources(ctx, peerID, enabledOnly)
	if err != nil {
		return nil, err
	}
	return map[string]any{"count": len(sources), "sources": sources}, nil
}

func (h *Handler) calendarSourceDelete(peerID, id string, ctx context.Context) (map[string]any, error) {
	if h.calendarStore == nil {
		return nil, fmt.Errorf("calendar is not enabled")
	}
	if strings.TrimSpace(id) == "" {
		return nil, fmt.Errorf("id is required")
	}
	if err := h.calendarStore.DeleteSource(ctx, peerID, id); err != nil {
		return nil, err
	}
	return map[string]any{"ok": true, "deleted_id": id}, nil
}

func (h *Handler) calendarAgenda(peerID string, query calendar.AgendaQuery, ctx context.Context) (map[string]any, error) {
	if h.calendarStore == nil {
		return nil, fmt.Errorf("calendar is not enabled")
	}
	agenda, err := h.calendarStore.Agenda(ctx, peerID, query, h.fetchClient)
	if err != nil {
		return nil, err
	}
	return map[string]any{"agenda": agenda}, nil
}

func (h *Handler) calendarCreate(peerID string, input calendar.LocalEventInput, ctx context.Context) (map[string]any, error) {
	if h.calendarStore == nil {
		return nil, fmt.Errorf("calendar is not enabled")
	}
	event, err := h.calendarStore.CreateLocalEvent(ctx, peerID, input)
	if err != nil {
		return nil, err
	}
	return map[string]any{"ok": true, "event": event}, nil
}

func (h *Handler) calendarUpdate(peerID, id string, patch calendar.LocalEventPatch, ctx context.Context) (map[string]any, error) {
	if h.calendarStore == nil {
		return nil, fmt.Errorf("calendar is not enabled")
	}
	event, err := h.calendarStore.UpdateLocalEvent(ctx, peerID, id, patch)
	if err != nil {
		return nil, err
	}
	return map[string]any{"ok": true, "event": event}, nil
}

func (h *Handler) calendarCancel(peerID, id string, ctx context.Context) (map[string]any, error) {
	if h.calendarStore == nil {
		return nil, fmt.Errorf("calendar is not enabled")
	}
	if strings.TrimSpace(id) == "" {
		return nil, fmt.Errorf("id is required")
	}
	event, err := h.calendarStore.CancelLocalEvent(ctx, peerID, id)
	if err != nil {
		return nil, err
	}
	return map[string]any{"ok": true, "event": event}, nil
}
