package handler

import (
	"context"
	"fmt"
	"strings"

	"github.com/ffimnsr/koios/internal/bookmarks"
	"github.com/ffimnsr/koios/internal/types"
)

func (h *Handler) bookmarkCreate(peerID string, input bookmarks.Input, ctx context.Context) (map[string]any, error) {
	if h.bookmarkStore == nil {
		return nil, fmt.Errorf("bookmarks are not enabled")
	}
	bookmark, err := h.bookmarkStore.Create(ctx, peerID, input)
	if err != nil {
		return nil, err
	}
	return map[string]any{"ok": true, "bookmark": bookmark}, nil
}

func (h *Handler) bookmarkCaptureSession(peerID, sessionKey, runID, title string, startIndex, endIndex int, labels []string, reminderAt int64, ctx context.Context) (map[string]any, error) {
	if h.bookmarkStore == nil {
		return nil, fmt.Errorf("bookmarks are not enabled")
	}
	messages, resolvedSessionKey, resolvedRunID, err := h.resolveBookmarkSource(peerID, sessionKey, runID)
	if err != nil {
		return nil, err
	}
	content, excerpt, resolvedTitle, rangeStart, rangeEnd, err := renderBookmarkCapture(messages, title, startIndex, endIndex)
	if err != nil {
		return nil, err
	}
	bookmark, err := h.bookmarkStore.Create(ctx, peerID, bookmarks.Input{
		Title:            resolvedTitle,
		Content:          content,
		Labels:           labels,
		ReminderAt:       reminderAt,
		SourceKind:       bookmarks.SourceKindSessionRange,
		SourceSessionKey: resolvedSessionKey,
		SourceRunID:      resolvedRunID,
		SourceStartIndex: rangeStart,
		SourceEndIndex:   rangeEnd,
		SourceExcerpt:    excerpt,
	})
	if err != nil {
		return nil, err
	}
	return map[string]any{"ok": true, "bookmark": bookmark}, nil
}

func (h *Handler) bookmarkGet(peerID, id string, ctx context.Context) (map[string]any, error) {
	if h.bookmarkStore == nil {
		return nil, fmt.Errorf("bookmarks are not enabled")
	}
	bookmark, err := h.bookmarkStore.Get(ctx, peerID, id)
	if err != nil {
		return nil, err
	}
	return map[string]any{"bookmark": bookmark}, nil
}

func (h *Handler) bookmarkList(peerID string, limit int, label string, upcomingOnly bool, ctx context.Context) (map[string]any, error) {
	if h.bookmarkStore == nil {
		return nil, fmt.Errorf("bookmarks are not enabled")
	}
	items, err := h.bookmarkStore.List(ctx, peerID, bookmarks.Filter{Limit: limit, Label: label, UpcomingOnly: upcomingOnly})
	if err != nil {
		return nil, err
	}
	return map[string]any{"count": len(items), "bookmarks": items, "label": strings.TrimSpace(label), "upcoming_only": upcomingOnly}, nil
}

func (h *Handler) bookmarkSearch(peerID, query string, limit int, ctx context.Context) (map[string]any, error) {
	if h.bookmarkStore == nil {
		return nil, fmt.Errorf("bookmarks are not enabled")
	}
	items, err := h.bookmarkStore.Search(ctx, peerID, query, limit)
	if err != nil {
		return nil, err
	}
	return map[string]any{"count": len(items), "bookmarks": items, "query": strings.TrimSpace(query)}, nil
}

func (h *Handler) bookmarkUpdate(peerID, id string, patch bookmarks.Patch, ctx context.Context) (map[string]any, error) {
	if h.bookmarkStore == nil {
		return nil, fmt.Errorf("bookmarks are not enabled")
	}
	bookmark, err := h.bookmarkStore.Update(ctx, peerID, id, patch)
	if err != nil {
		return nil, err
	}
	return map[string]any{"ok": true, "bookmark": bookmark}, nil
}

func (h *Handler) bookmarkDelete(peerID, id string, ctx context.Context) (map[string]any, error) {
	if h.bookmarkStore == nil {
		return nil, fmt.Errorf("bookmarks are not enabled")
	}
	if err := h.bookmarkStore.Delete(ctx, peerID, id); err != nil {
		return nil, err
	}
	return map[string]any{"ok": true, "id": strings.TrimSpace(id)}, nil
}

func (h *Handler) resolveBookmarkSource(peerID, sessionKey, runID string) ([]types.Message, string, string, error) {
	if strings.TrimSpace(runID) != "" {
		if h.subRuntime == nil {
			return nil, "", "", fmt.Errorf("sub-sessions are not enabled")
		}
		rec, ok := h.subRuntime.Get(strings.TrimSpace(runID))
		if !ok || rec.PeerID != peerID {
			return nil, "", "", fmt.Errorf("run %s not found", strings.TrimSpace(runID))
		}
		messages, err := h.subRuntime.Transcript(strings.TrimSpace(runID))
		if err != nil {
			return nil, "", "", err
		}
		return messages, rec.SessionKey, rec.ID, nil
	}
	resolvedSessionKey := strings.TrimSpace(sessionKey)
	if resolvedSessionKey == "" {
		resolvedSessionKey = peerID
	}
	if resolvedSessionKey != peerID && !strings.HasPrefix(resolvedSessionKey, peerID+"::") {
		return nil, "", "", fmt.Errorf("session_key %q is not accessible to peer %q", resolvedSessionKey, peerID)
	}
	return h.store.Get(resolvedSessionKey).History(), resolvedSessionKey, "", nil
}

func renderBookmarkCapture(messages []types.Message, title string, startIndex, endIndex int) (string, string, string, int, int, error) {
	if len(messages) == 0 {
		return "", "", "", 0, 0, fmt.Errorf("source session has no messages")
	}
	if startIndex <= 0 {
		startIndex = len(messages)
	}
	if endIndex <= 0 {
		endIndex = startIndex
	}
	if startIndex < 1 || endIndex < 1 || startIndex > len(messages) || endIndex > len(messages) {
		return "", "", "", 0, 0, fmt.Errorf("message range %d-%d is out of bounds for %d stored messages", startIndex, endIndex, len(messages))
	}
	if startIndex > endIndex {
		return "", "", "", 0, 0, fmt.Errorf("start_index must be <= end_index")
	}
	segment := messages[startIndex-1 : endIndex]
	var lines []string
	firstContent := ""
	for offset, msg := range segment {
		content := strings.TrimSpace(msg.Content)
		if content == "" {
			continue
		}
		if firstContent == "" {
			firstContent = content
		}
		lines = append(lines, fmt.Sprintf("[%d] %s: %s", startIndex+offset, strings.ToUpper(msg.Role), content))
	}
	if len(lines) == 0 {
		return "", "", "", 0, 0, fmt.Errorf("selected message range does not contain bookmarkable text")
	}
	resolvedTitle := strings.TrimSpace(title)
	if resolvedTitle == "" {
		resolvedTitle = fmt.Sprintf("Session %d-%d", startIndex, endIndex)
		if firstContent != "" {
			resolvedTitle = truncateBookmarkText(firstContent, 72)
		}
	}
	content := strings.Join(lines, "\n")
	return content, truncateBookmarkText(firstContent, 160), resolvedTitle, startIndex, endIndex, nil
}

func truncateBookmarkText(value string, limit int) string {
	value = strings.TrimSpace(value)
	if limit <= 0 || len(value) <= limit {
		return value
	}
	return strings.TrimSpace(value[:limit])
}
