package handler

import (
	"context"
	"fmt"

	"github.com/ffimnsr/koios/internal/preferences"
)

func (h *Handler) preferenceSet(peerID string, input preferences.Input, ctx context.Context) (map[string]any, error) {
	if h.preferenceStore == nil {
		return nil, fmt.Errorf("preferences are not enabled")
	}
	p, err := h.preferenceStore.Set(ctx, peerID, input)
	if err != nil {
		return nil, err
	}
	return map[string]any{"ok": true, "preference": p}, nil
}

func (h *Handler) preferenceGet(peerID, key, scope string, ctx context.Context) (map[string]any, error) {
	if h.preferenceStore == nil {
		return nil, fmt.Errorf("preferences are not enabled")
	}
	p, err := h.preferenceStore.Get(ctx, peerID, key, scope)
	if err != nil {
		return nil, err
	}
	return map[string]any{"ok": true, "preference": p}, nil
}

func (h *Handler) preferenceList(peerID, scope string, limit int, ctx context.Context) (map[string]any, error) {
	if h.preferenceStore == nil {
		return nil, fmt.Errorf("preferences are not enabled")
	}
	results, err := h.preferenceStore.List(ctx, peerID, scope, limit)
	if err != nil {
		return nil, err
	}
	if limit <= 0 {
		limit = 50
	}
	return map[string]any{"ok": true, "count": len(results), "preferences": results}, nil
}
