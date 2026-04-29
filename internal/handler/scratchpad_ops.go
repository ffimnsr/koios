package handler

import (
	"context"
	"fmt"

	"github.com/ffimnsr/koios/internal/agent"
)

func sessionKeyFromCtx(peerID string, ctx context.Context) string {
	key := agent.CurrentSessionKey(ctx)
	if key == "" {
		return peerID
	}
	return key
}

func (h *Handler) scratchpadCreate(peerID, sessionKey, content string) (map[string]any, error) {
	if h.scratchpadStore == nil {
		return nil, fmt.Errorf("scratchpad is not enabled")
	}
	pad := h.scratchpadStore.Create(peerID, sessionKey, content)
	return map[string]any{"ok": true, "scratchpad": pad}, nil
}

func (h *Handler) scratchpadUpdate(peerID, sessionKey, content string) (map[string]any, error) {
	if h.scratchpadStore == nil {
		return nil, fmt.Errorf("scratchpad is not enabled")
	}
	pad := h.scratchpadStore.Update(peerID, sessionKey, content)
	return map[string]any{"ok": true, "scratchpad": pad}, nil
}

func (h *Handler) scratchpadGet(peerID, sessionKey string) (map[string]any, error) {
	if h.scratchpadStore == nil {
		return nil, fmt.Errorf("scratchpad is not enabled")
	}
	pad, err := h.scratchpadStore.Get(peerID, sessionKey)
	if err != nil {
		return nil, err
	}
	return map[string]any{"ok": true, "scratchpad": pad}, nil
}

func (h *Handler) scratchpadClear(peerID, sessionKey string) (map[string]any, error) {
	if h.scratchpadStore == nil {
		return nil, fmt.Errorf("scratchpad is not enabled")
	}
	h.scratchpadStore.Clear(peerID, sessionKey)
	return map[string]any{"ok": true}, nil
}
