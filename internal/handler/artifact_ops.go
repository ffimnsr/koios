package handler

import (
	"context"
	"fmt"

	"github.com/ffimnsr/koios/internal/artifacts"
)

func (h *Handler) artifactCreate(peerID string, input artifacts.Input, ctx context.Context) (map[string]any, error) {
	if h.artifactStore == nil {
		return nil, fmt.Errorf("artifacts are not enabled")
	}
	a, err := h.artifactStore.Create(ctx, peerID, input)
	if err != nil {
		return nil, err
	}
	return map[string]any{"ok": true, "artifact": a}, nil
}

func (h *Handler) artifactGet(peerID, id string, ctx context.Context) (map[string]any, error) {
	if h.artifactStore == nil {
		return nil, fmt.Errorf("artifacts are not enabled")
	}
	a, err := h.artifactStore.Get(ctx, peerID, id)
	if err != nil {
		return nil, err
	}
	return map[string]any{"ok": true, "artifact": a}, nil
}

func (h *Handler) artifactList(peerID, kind string, limit int, ctx context.Context) (map[string]any, error) {
	if h.artifactStore == nil {
		return nil, fmt.Errorf("artifacts are not enabled")
	}
	results, err := h.artifactStore.List(ctx, peerID, kind, limit)
	if err != nil {
		return nil, err
	}
	if limit <= 0 {
		limit = 20
	}
	return map[string]any{"ok": true, "count": len(results), "artifacts": results}, nil
}

func (h *Handler) artifactUpdate(peerID, id string, patch artifacts.Patch, ctx context.Context) (map[string]any, error) {
	if h.artifactStore == nil {
		return nil, fmt.Errorf("artifacts are not enabled")
	}
	a, err := h.artifactStore.Update(ctx, peerID, id, patch)
	if err != nil {
		return nil, err
	}
	return map[string]any{"ok": true, "artifact": a}, nil
}
