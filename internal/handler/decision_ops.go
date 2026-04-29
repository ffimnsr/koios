package handler

import (
	"context"
	"fmt"

	"github.com/ffimnsr/koios/internal/decisions"
)

func (h *Handler) decisionRecord(peerID string, input decisions.Input, ctx context.Context) (map[string]any, error) {
	if h.decisionStore == nil {
		return nil, fmt.Errorf("decisions are not enabled")
	}
	d, err := h.decisionStore.Record(ctx, peerID, input)
	if err != nil {
		return nil, err
	}
	return map[string]any{"ok": true, "decision": d}, nil
}

func (h *Handler) decisionList(peerID string, limit int, ctx context.Context) (map[string]any, error) {
	if h.decisionStore == nil {
		return nil, fmt.Errorf("decisions are not enabled")
	}
	results, err := h.decisionStore.List(ctx, peerID, limit)
	if err != nil {
		return nil, err
	}
	if limit <= 0 {
		limit = 20
	}
	return map[string]any{"ok": true, "count": len(results), "decisions": results}, nil
}

func (h *Handler) decisionSearch(peerID, query string, limit int, ctx context.Context) (map[string]any, error) {
	if h.decisionStore == nil {
		return nil, fmt.Errorf("decisions are not enabled")
	}
	results, err := h.decisionStore.Search(ctx, peerID, query, limit)
	if err != nil {
		return nil, err
	}
	if limit <= 0 {
		limit = 20
	}
	return map[string]any{"ok": true, "count": len(results), "decisions": results}, nil
}
