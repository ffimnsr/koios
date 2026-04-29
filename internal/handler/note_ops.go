package handler

import (
	"context"
	"fmt"

	"github.com/ffimnsr/koios/internal/notes"
)

func notePatch(title, content *string, labels *[]string) notes.Patch {
	return notes.Patch{Title: title, Content: content, Labels: labels}
}

func (h *Handler) noteCreate(peerID string, input notes.Input, ctx context.Context) (map[string]any, error) {
	if h.noteStore == nil {
		return nil, fmt.Errorf("notes are not enabled")
	}
	note, err := h.noteStore.Create(ctx, peerID, input)
	if err != nil {
		return nil, err
	}
	return map[string]any{"ok": true, "note": note}, nil
}

func (h *Handler) noteSearch(peerID, query string, limit int, ctx context.Context) (map[string]any, error) {
	if h.noteStore == nil {
		return nil, fmt.Errorf("notes are not enabled")
	}
	results, err := h.noteStore.Search(ctx, peerID, query, limit)
	if err != nil {
		return nil, err
	}
	if limit <= 0 {
		limit = 20
	}
	return map[string]any{"ok": true, "count": len(results), "notes": results}, nil
}

func (h *Handler) noteUpdate(peerID, id string, patch notes.Patch, ctx context.Context) (map[string]any, error) {
	if h.noteStore == nil {
		return nil, fmt.Errorf("notes are not enabled")
	}
	note, err := h.noteStore.Update(ctx, peerID, id, patch)
	if err != nil {
		return nil, err
	}
	return map[string]any{"ok": true, "note": note}, nil
}
