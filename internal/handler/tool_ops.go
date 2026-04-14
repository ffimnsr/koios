package handler

import (
	"context"
	"fmt"
	"strings"
)

func (h *Handler) memorySearch(peerID, query string, limit int, ctx context.Context) (map[string]any, error) {
	if h.memStore == nil {
		return nil, fmt.Errorf("memory is not enabled")
	}
	if strings.TrimSpace(query) == "" {
		return nil, fmt.Errorf("q is required")
	}
	if limit <= 0 {
		limit = 5
	}
	results, err := h.memStore.Search(ctx, peerID, query, limit)
	if err != nil {
		return nil, err
	}
	return map[string]any{"results": results}, nil
}

func (h *Handler) memoryInsert(peerID, content string, ctx context.Context) (map[string]any, error) {
	if h.memStore == nil {
		return nil, fmt.Errorf("memory is not enabled")
	}
	content = strings.TrimSpace(content)
	if content == "" {
		return nil, fmt.Errorf("content is required")
	}
	const maxContentBytes = 8 * 1024
	if len(content) > maxContentBytes {
		return nil, fmt.Errorf("content exceeds %d byte limit", maxContentBytes)
	}
	chunk, err := h.memStore.InsertChunk(ctx, peerID, content)
	if err != nil {
		return nil, err
	}
	return map[string]any{"ok": true, "chunk": chunk}, nil
}

func (h *Handler) memoryGet(peerID, id string, ctx context.Context) (map[string]any, error) {
	if h.memStore == nil {
		return nil, fmt.Errorf("memory is not enabled")
	}
	if strings.TrimSpace(id) == "" {
		return nil, fmt.Errorf("id is required")
	}
	chunk, err := h.memStore.GetChunk(ctx, peerID, id)
	if err != nil {
		return nil, err
	}
	return map[string]any{"chunk": chunk}, nil
}

func (h *Handler) memoryDelete(peerID, id string, ctx context.Context) (map[string]any, error) {
	if h.memStore == nil {
		return nil, fmt.Errorf("memory is not enabled")
	}
	if strings.TrimSpace(id) == "" {
		return nil, fmt.Errorf("id is required")
	}
	if err := h.memStore.DeleteChunk(ctx, peerID, id); err != nil {
		return nil, err
	}
	return map[string]any{"ok": true, "id": id}, nil
}

func (h *Handler) memoryList(peerID string, limit int, ctx context.Context) (map[string]any, error) {
	if h.memStore == nil {
		return nil, fmt.Errorf("memory is not enabled")
	}
	if limit <= 0 {
		limit = 50
	}
	chunks, err := h.memStore.List(ctx, peerID, limit)
	if err != nil {
		return nil, err
	}
	return map[string]any{"count": len(chunks), "chunks": chunks}, nil
}

func (h *Handler) memoryTimeline(peerID, anchorID string, depthBefore, depthAfter int, ctx context.Context) (map[string]any, error) {
	if h.memStore == nil {
		return nil, fmt.Errorf("memory is not enabled")
	}
	if strings.TrimSpace(anchorID) == "" {
		return nil, fmt.Errorf("anchor_id is required")
	}
	chunks, err := h.memStore.Timeline(ctx, peerID, anchorID, depthBefore, depthAfter)
	if err != nil {
		return nil, err
	}
	return map[string]any{"anchor_id": anchorID, "count": len(chunks), "chunks": chunks}, nil
}

func (h *Handler) memoryBatchGet(peerID string, ids []string, ctx context.Context) (map[string]any, error) {
	if h.memStore == nil {
		return nil, fmt.Errorf("memory is not enabled")
	}
	if len(ids) == 0 {
		return nil, fmt.Errorf("ids is required")
	}
	const maxBatch = 100
	if len(ids) > maxBatch {
		return nil, fmt.Errorf("batch limited to %d ids", maxBatch)
	}
	chunks, err := h.memStore.BatchGet(ctx, peerID, ids)
	if err != nil {
		return nil, err
	}
	return map[string]any{"count": len(chunks), "chunks": chunks}, nil
}

func (h *Handler) memoryTag(peerID, id string, tags []string, category string, ctx context.Context) (map[string]any, error) {
	if h.memStore == nil {
		return nil, fmt.Errorf("memory is not enabled")
	}
	if strings.TrimSpace(id) == "" {
		return nil, fmt.Errorf("id is required")
	}
	if err := h.memStore.TagChunk(ctx, peerID, id, tags, category); err != nil {
		return nil, err
	}
	return map[string]any{"ok": true, "id": id, "tags": tags, "category": category}, nil
}

func (h *Handler) memoryStats(peerID string, ctx context.Context) (map[string]any, error) {
	if h.memStore == nil {
		return nil, fmt.Errorf("memory is not enabled")
	}
	stats, err := h.memStore.Stats(ctx, peerID)
	if err != nil {
		return nil, err
	}
	return map[string]any{"stats": stats}, nil
}

func (h *Handler) workspaceList(peerID, path string, recursive bool, limit int) (map[string]any, error) {
	if h.workspaceStore == nil {
		return nil, fmt.Errorf("workspace is not enabled")
	}
	if strings.TrimSpace(path) == "" {
		path = "."
	}
	entries, err := h.workspaceStore.List(peerID, path, recursive, limit)
	if err != nil {
		return nil, err
	}
	return map[string]any{"path": path, "entries": entries, "count": len(entries)}, nil
}

func (h *Handler) workspaceRead(peerID, path string, startLine, endLine int) (map[string]any, error) {
	if h.workspaceStore == nil {
		return nil, fmt.Errorf("workspace is not enabled")
	}
	if strings.TrimSpace(path) == "" {
		return nil, fmt.Errorf("path is required")
	}
	result, err := h.workspaceStore.ReadRange(peerID, path, startLine, endLine)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"path":        path,
		"content":     result.Content,
		"start_line":  result.StartLine,
		"end_line":    result.EndLine,
		"total_lines": result.TotalLines,
	}, nil
}

func (h *Handler) workspaceWrite(peerID, path, content string, appendMode bool) (map[string]any, error) {
	if h.workspaceStore == nil {
		return nil, fmt.Errorf("workspace is not enabled")
	}
	if strings.TrimSpace(path) == "" {
		return nil, fmt.Errorf("path is required")
	}
	if _, err := h.workspaceStore.Write(peerID, path, content, appendMode); err != nil {
		return nil, err
	}
	return map[string]any{"ok": true, "path": path, "bytes": len(content), "append": appendMode}, nil
}

func (h *Handler) workspaceEdit(peerID, path, oldText, newText string, replaceAll bool) (map[string]any, error) {
	if h.workspaceStore == nil {
		return nil, fmt.Errorf("workspace is not enabled")
	}
	if strings.TrimSpace(path) == "" {
		return nil, fmt.Errorf("path is required")
	}
	result, err := h.workspaceStore.Edit(peerID, path, oldText, newText, replaceAll)
	if err != nil {
		return nil, err
	}
	return map[string]any{"ok": true, "result": result}, nil
}

func (h *Handler) workspaceMkdir(peerID, path string) (map[string]any, error) {
	if h.workspaceStore == nil {
		return nil, fmt.Errorf("workspace is not enabled")
	}
	if strings.TrimSpace(path) == "" {
		return nil, fmt.Errorf("path is required")
	}
	if _, err := h.workspaceStore.Mkdir(peerID, path); err != nil {
		return nil, err
	}
	return map[string]any{"ok": true, "path": path}, nil
}

func (h *Handler) workspaceDelete(peerID, path string, recursive bool) (map[string]any, error) {
	if h.workspaceStore == nil {
		return nil, fmt.Errorf("workspace is not enabled")
	}
	if strings.TrimSpace(path) == "" {
		return nil, fmt.Errorf("path is required")
	}
	if err := h.workspaceStore.Delete(peerID, path, recursive); err != nil {
		return nil, err
	}
	return map[string]any{"ok": true, "path": path, "recursive": recursive}, nil
}
