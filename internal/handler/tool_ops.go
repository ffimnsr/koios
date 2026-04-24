package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/ffimnsr/koios/internal/memory"
	"github.com/ffimnsr/koios/internal/workflow"
)

func candidatePatch(content *string, tags *[]string, category *string, retentionClass *string, exposurePolicy *string, expiresAt *int64) memory.CandidatePatch {
	patch := memory.CandidatePatch{
		Content:   content,
		Tags:      tags,
		Category:  category,
		ExpiresAt: expiresAt,
	}
	if retentionClass != nil {
		value := memory.RetentionClass(strings.TrimSpace(*retentionClass))
		patch.RetentionClass = &value
	}
	if exposurePolicy != nil {
		value := memory.ExposurePolicy(strings.TrimSpace(*exposurePolicy))
		patch.ExposurePolicy = &value
	}
	return patch
}

func entityPatch(kind *string, name *string, aliases *[]string, notes *string, lastSeenAt *int64) memory.EntityPatch {
	patch := memory.EntityPatch{
		Name:       name,
		Aliases:    aliases,
		Notes:      notes,
		LastSeenAt: lastSeenAt,
	}
	if kind != nil {
		value := memory.EntityKind(strings.TrimSpace(*kind))
		patch.Kind = &value
	}
	return patch
}

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
	return h.memoryInsertWithOptions(peerID, content, nil, "", "", "", 0, ctx)
}

func (h *Handler) memoryInsertWithOptions(peerID, content string, tags []string, category string, retentionClass string, exposurePolicy string, expiresAt int64, ctx context.Context) (map[string]any, error) {
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
	chunk, err := h.memStore.InsertChunkWithOptions(ctx, peerID, content, memory.ChunkOptions{
		Tags:           tags,
		Category:       category,
		RetentionClass: memory.RetentionClass(retentionClass),
		ExposurePolicy: memory.ExposurePolicy(exposurePolicy),
		ExpiresAt:      expiresAt,
	})
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

func (h *Handler) memoryTag(peerID, id string, tags []string, category string, retentionClass string, exposurePolicy string, expiresAt int64, ctx context.Context) (map[string]any, error) {
	if h.memStore == nil {
		return nil, fmt.Errorf("memory is not enabled")
	}
	if strings.TrimSpace(id) == "" {
		return nil, fmt.Errorf("id is required")
	}
	if err := h.memStore.TagChunk(ctx, peerID, id, tags, category, memory.RetentionClass(retentionClass), memory.ExposurePolicy(exposurePolicy), expiresAt); err != nil {
		return nil, err
	}
	return map[string]any{"ok": true, "id": id, "tags": tags, "category": category, "retention_class": retentionClass, "exposure_policy": exposurePolicy, "expires_at": expiresAt}, nil
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

func (h *Handler) memoryCandidateCreate(peerID, content string, tags []string, category string, retentionClass string, exposurePolicy string, expiresAt int64, ctx context.Context) (map[string]any, error) {
	if h.memStore == nil {
		return nil, fmt.Errorf("memory is not enabled")
	}
	content = strings.TrimSpace(content)
	if content == "" {
		return nil, fmt.Errorf("content is required")
	}
	candidate, err := h.memStore.QueueCandidate(ctx, peerID, content, memory.ChunkOptions{
		Tags:           tags,
		Category:       category,
		RetentionClass: memory.RetentionClass(retentionClass),
		ExposurePolicy: memory.ExposurePolicy(exposurePolicy),
		ExpiresAt:      expiresAt,
	})
	if err != nil {
		return nil, err
	}
	return map[string]any{"ok": true, "candidate": candidate}, nil
}

func (h *Handler) memoryCandidateList(peerID string, limit int, status string, ctx context.Context) (map[string]any, error) {
	if h.memStore == nil {
		return nil, fmt.Errorf("memory is not enabled")
	}
	if limit <= 0 {
		limit = 50
	}
	candidates, err := h.memStore.ListCandidates(ctx, peerID, limit, memory.CandidateStatus(status))
	if err != nil {
		return nil, err
	}
	manualCount := 0
	autoGeneratedCount := 0
	byCaptureKind := make(map[string]int)
	for _, candidate := range candidates {
		kind := strings.TrimSpace(candidate.CaptureKind)
		if kind == "" {
			kind = memory.CandidateCaptureManual
		}
		byCaptureKind[kind]++
		switch kind {
		case memory.CandidateCaptureManual:
			manualCount++
		case memory.CandidateCaptureAutoTurnExtract:
			autoGeneratedCount++
		}
	}
	return map[string]any{
		"count":                len(candidates),
		"candidates":           candidates,
		"status":               strings.TrimSpace(status),
		"manual_count":         manualCount,
		"auto_generated_count": autoGeneratedCount,
		"capture_kinds":        byCaptureKind,
	}, nil
}

func (h *Handler) memoryCandidateEdit(peerID, id string, patch memory.CandidatePatch, ctx context.Context) (map[string]any, error) {
	if h.memStore == nil {
		return nil, fmt.Errorf("memory is not enabled")
	}
	if strings.TrimSpace(id) == "" {
		return nil, fmt.Errorf("id is required")
	}
	candidate, err := h.memStore.EditCandidate(ctx, peerID, id, patch)
	if err != nil {
		return nil, err
	}
	return map[string]any{"ok": true, "candidate": candidate}, nil
}

func (h *Handler) memoryCandidateApprove(peerID, id string, patch memory.CandidatePatch, reason string, ctx context.Context) (map[string]any, error) {
	if h.memStore == nil {
		return nil, fmt.Errorf("memory is not enabled")
	}
	if strings.TrimSpace(id) == "" {
		return nil, fmt.Errorf("id is required")
	}
	candidate, chunk, err := h.memStore.ApproveCandidate(ctx, peerID, id, patch, reason)
	if err != nil {
		return nil, err
	}
	return map[string]any{"ok": true, "candidate": candidate, "chunk": chunk}, nil
}

func (h *Handler) memoryCandidateMerge(peerID, id, mergeIntoID string, patch memory.CandidatePatch, reason string, ctx context.Context) (map[string]any, error) {
	if h.memStore == nil {
		return nil, fmt.Errorf("memory is not enabled")
	}
	if strings.TrimSpace(id) == "" {
		return nil, fmt.Errorf("id is required")
	}
	if strings.TrimSpace(mergeIntoID) == "" {
		return nil, fmt.Errorf("merge_into_id is required")
	}
	candidate, chunk, err := h.memStore.MergeCandidate(ctx, peerID, id, mergeIntoID, patch, reason)
	if err != nil {
		return nil, err
	}
	return map[string]any{"ok": true, "candidate": candidate, "chunk": chunk}, nil
}

func (h *Handler) memoryCandidateReject(peerID, id, reason string, ctx context.Context) (map[string]any, error) {
	if h.memStore == nil {
		return nil, fmt.Errorf("memory is not enabled")
	}
	if strings.TrimSpace(id) == "" {
		return nil, fmt.Errorf("id is required")
	}
	if strings.TrimSpace(reason) == "" {
		return nil, fmt.Errorf("reason is required")
	}
	candidate, err := h.memStore.RejectCandidate(ctx, peerID, id, reason)
	if err != nil {
		return nil, err
	}
	return map[string]any{"ok": true, "candidate": candidate}, nil
}

func (h *Handler) memoryEntityCreate(peerID string, kind string, name string, aliases []string, notes string, lastSeenAt int64, ctx context.Context) (map[string]any, error) {
	if h.memStore == nil {
		return nil, fmt.Errorf("memory is not enabled")
	}
	entity, err := h.memStore.CreateEntity(ctx, peerID, memory.EntityKind(kind), name, aliases, notes, lastSeenAt)
	if err != nil {
		return nil, err
	}
	return map[string]any{"ok": true, "entity": entity}, nil
}

func (h *Handler) memoryEntityUpdate(peerID, id string, patch memory.EntityPatch, ctx context.Context) (map[string]any, error) {
	if h.memStore == nil {
		return nil, fmt.Errorf("memory is not enabled")
	}
	if strings.TrimSpace(id) == "" {
		return nil, fmt.Errorf("id is required")
	}
	entity, err := h.memStore.UpdateEntity(ctx, peerID, id, patch)
	if err != nil {
		return nil, err
	}
	return map[string]any{"ok": true, "entity": entity}, nil
}

func (h *Handler) memoryEntityGet(peerID, id string, ctx context.Context) (map[string]any, error) {
	if h.memStore == nil {
		return nil, fmt.Errorf("memory is not enabled")
	}
	if strings.TrimSpace(id) == "" {
		return nil, fmt.Errorf("id is required")
	}
	graph, err := h.memStore.GetEntityGraph(ctx, peerID, id)
	if err != nil {
		return nil, err
	}
	return map[string]any{"entity_graph": graph}, nil
}

func (h *Handler) memoryEntityList(peerID, kind string, limit int, ctx context.Context) (map[string]any, error) {
	if h.memStore == nil {
		return nil, fmt.Errorf("memory is not enabled")
	}
	entities, err := h.memStore.ListEntities(ctx, peerID, memory.EntityKind(kind), limit)
	if err != nil {
		return nil, err
	}
	return map[string]any{"count": len(entities), "entities": entities, "kind": strings.TrimSpace(kind)}, nil
}

func (h *Handler) memoryEntitySearch(peerID, query string, limit int, ctx context.Context) (map[string]any, error) {
	if h.memStore == nil {
		return nil, fmt.Errorf("memory is not enabled")
	}
	if strings.TrimSpace(query) == "" {
		return nil, fmt.Errorf("q is required")
	}
	entities, err := h.memStore.SearchEntities(ctx, peerID, query, limit)
	if err != nil {
		return nil, err
	}
	return map[string]any{"count": len(entities), "entities": entities}, nil
}

func (h *Handler) memoryEntityLinkChunk(peerID, id, chunkID string, ctx context.Context) (map[string]any, error) {
	if h.memStore == nil {
		return nil, fmt.Errorf("memory is not enabled")
	}
	if strings.TrimSpace(id) == "" {
		return nil, fmt.Errorf("id is required")
	}
	if strings.TrimSpace(chunkID) == "" {
		return nil, fmt.Errorf("chunk_id is required")
	}
	if err := h.memStore.LinkChunkToEntity(ctx, peerID, id, chunkID); err != nil {
		return nil, err
	}
	graph, err := h.memStore.GetEntityGraph(ctx, peerID, id)
	if err != nil {
		return nil, err
	}
	return map[string]any{"ok": true, "entity_graph": graph}, nil
}

func (h *Handler) memoryEntityRelate(peerID, sourceID, targetID, relation, notes string, ctx context.Context) (map[string]any, error) {
	if h.memStore == nil {
		return nil, fmt.Errorf("memory is not enabled")
	}
	relationship, err := h.memStore.RelateEntities(ctx, peerID, sourceID, targetID, relation, notes)
	if err != nil {
		return nil, err
	}
	return map[string]any{"ok": true, "relationship": relationship}, nil
}

func (h *Handler) memoryEntityTouch(peerID, id string, lastSeenAt int64, ctx context.Context) (map[string]any, error) {
	if h.memStore == nil {
		return nil, fmt.Errorf("memory is not enabled")
	}
	if strings.TrimSpace(id) == "" {
		return nil, fmt.Errorf("id is required")
	}
	entity, err := h.memStore.TouchEntity(ctx, peerID, id, lastSeenAt)
	if err != nil {
		return nil, err
	}
	return map[string]any{"ok": true, "entity": entity}, nil
}

func (h *Handler) memoryEntityDelete(peerID, id string, ctx context.Context) (map[string]any, error) {
	if h.memStore == nil {
		return nil, fmt.Errorf("memory is not enabled")
	}
	if strings.TrimSpace(id) == "" {
		return nil, fmt.Errorf("id is required")
	}
	if err := h.memStore.DeleteEntity(ctx, peerID, id); err != nil {
		return nil, err
	}
	return map[string]any{"ok": true, "id": id}, nil
}

func (h *Handler) memoryEntityUnlinkChunk(peerID, id, chunkID string, ctx context.Context) (map[string]any, error) {
	if h.memStore == nil {
		return nil, fmt.Errorf("memory is not enabled")
	}
	if strings.TrimSpace(id) == "" {
		return nil, fmt.Errorf("id is required")
	}
	if strings.TrimSpace(chunkID) == "" {
		return nil, fmt.Errorf("chunk_id is required")
	}
	if err := h.memStore.UnlinkChunkFromEntity(ctx, peerID, id, chunkID); err != nil {
		return nil, err
	}
	return map[string]any{"ok": true, "id": id, "chunk_id": chunkID}, nil
}

func (h *Handler) memoryEntityUnrelate(peerID, sourceID, targetID, relation string, ctx context.Context) (map[string]any, error) {
	if h.memStore == nil {
		return nil, fmt.Errorf("memory is not enabled")
	}
	if strings.TrimSpace(sourceID) == "" {
		return nil, fmt.Errorf("source_id is required")
	}
	if strings.TrimSpace(targetID) == "" {
		return nil, fmt.Errorf("target_id is required")
	}
	if strings.TrimSpace(relation) == "" {
		return nil, fmt.Errorf("relation is required")
	}
	if err := h.memStore.UnrelateEntities(ctx, peerID, sourceID, targetID, relation); err != nil {
		return nil, err
	}
	return map[string]any{"ok": true, "source_id": sourceID, "target_id": targetID, "relation": relation}, nil
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

func (h *Handler) workspaceHead(peerID, path string, lines int) (map[string]any, error) {
	if h.workspaceStore == nil {
		return nil, fmt.Errorf("workspace is not enabled")
	}
	if strings.TrimSpace(path) == "" {
		return nil, fmt.Errorf("path is required")
	}
	result, err := h.workspaceStore.Head(peerID, path, lines)
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

func (h *Handler) workspaceTail(peerID, path string, lines int) (map[string]any, error) {
	if h.workspaceStore == nil {
		return nil, fmt.Errorf("workspace is not enabled")
	}
	if strings.TrimSpace(path) == "" {
		return nil, fmt.Errorf("path is required")
	}
	result, err := h.workspaceStore.Tail(peerID, path, lines)
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

func (h *Handler) workspaceGrep(peerID, path, pattern string, recursive bool, limit int, caseSensitive, useRegexp bool) (map[string]any, error) {
	if h.workspaceStore == nil {
		return nil, fmt.Errorf("workspace is not enabled")
	}
	if strings.TrimSpace(path) == "" {
		path = "."
	}
	if strings.TrimSpace(pattern) == "" {
		return nil, fmt.Errorf("pattern is required")
	}
	matches, err := h.workspaceStore.Grep(peerID, path, pattern, recursive, limit, caseSensitive, useRegexp)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"path":           path,
		"pattern":        pattern,
		"recursive":      recursive,
		"case_sensitive": caseSensitive,
		"regexp":         useRegexp,
		"matches":        matches,
		"count":          len(matches),
	}, nil
}

func (h *Handler) workspaceSort(peerID, path string, reverse, caseSensitive bool) (map[string]any, error) {
	if h.workspaceStore == nil {
		return nil, fmt.Errorf("workspace is not enabled")
	}
	if strings.TrimSpace(path) == "" {
		return nil, fmt.Errorf("path is required")
	}
	result, err := h.workspaceStore.SortLines(peerID, path, reverse, caseSensitive)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"path":        path,
		"content":     result.Content,
		"line_count":  result.LineCount,
		"total_lines": result.TotalLines,
		"reverse":     reverse,
	}, nil
}

func (h *Handler) workspaceUniq(peerID, path string, count, caseSensitive bool) (map[string]any, error) {
	if h.workspaceStore == nil {
		return nil, fmt.Errorf("workspace is not enabled")
	}
	if strings.TrimSpace(path) == "" {
		return nil, fmt.Errorf("path is required")
	}
	result, err := h.workspaceStore.UniqLines(peerID, path, count, caseSensitive)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"path":           path,
		"content":        result.Content,
		"line_count":     result.LineCount,
		"total_lines":    result.TotalLines,
		"count":          count,
		"case_sensitive": caseSensitive,
	}, nil
}

func (h *Handler) workspaceDiff(peerID, path, otherPath, content string, contextLines int) (map[string]any, error) {
	if h.workspaceStore == nil {
		return nil, fmt.Errorf("workspace is not enabled")
	}
	result, err := h.workspaceStore.Diff(peerID, path, otherPath, content, contextLines)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"path":       result.Path,
		"other_path": result.OtherPath,
		"has_diff":   result.HasDiff,
		"diff":       result.Diff,
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

func (h *Handler) workspaceApplyPatch(peerID, patch string) (map[string]any, error) {
	if h.workspaceStore == nil {
		return nil, fmt.Errorf("workspace is not enabled")
	}
	patch = strings.TrimSpace(patch)
	if patch == "" {
		return nil, fmt.Errorf("patch is required")
	}
	result, err := h.workspaceStore.ApplyPatch(peerID, patch)
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

// ── workflow tool helpers ─────────────────────────────────────────────────────

type workflowCreateParams struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	FirstStep   string          `json:"first_step"`
	Steps       json.RawMessage `json:"steps"`
}

func (h *Handler) workflowList(peerID string) (map[string]any, error) {
	if h.workflowRunner == nil {
		return nil, fmt.Errorf("workflow engine is not enabled")
	}
	wfs := h.workflowRunner.Store().List(peerID)
	return map[string]any{"count": len(wfs), "workflows": wfs}, nil
}

func (h *Handler) workflowCreate(peerID string, p workflowCreateParams) (map[string]any, error) {
	if h.workflowRunner == nil {
		return nil, fmt.Errorf("workflow engine is not enabled")
	}
	name := strings.TrimSpace(p.Name)
	if name == "" {
		return nil, fmt.Errorf("name is required")
	}
	var steps []workflow.Step
	if len(p.Steps) > 0 && string(p.Steps) != "null" {
		if err := json.Unmarshal(p.Steps, &steps); err != nil {
			return nil, fmt.Errorf("invalid steps: %w", err)
		}
	}
	if len(steps) == 0 {
		return nil, fmt.Errorf("at least one step is required")
	}
	for i, s := range steps {
		if strings.TrimSpace(s.ID) == "" {
			steps[i].ID = fmt.Sprintf("step-%d", i+1)
		}
		if s.Kind == "" {
			return nil, fmt.Errorf("step %d: kind is required", i+1)
		}
	}
	wf, err := h.workflowRunner.Store().Create(workflow.Workflow{
		Name:        name,
		Description: strings.TrimSpace(p.Description),
		PeerID:      peerID,
		FirstStep:   strings.TrimSpace(p.FirstStep),
		Steps:       steps,
	})
	if err != nil {
		return nil, err
	}
	return map[string]any{"ok": true, "workflow": wf}, nil
}

func (h *Handler) workflowGet(peerID, id string) (map[string]any, error) {
	if h.workflowRunner == nil {
		return nil, fmt.Errorf("workflow engine is not enabled")
	}
	id = strings.TrimSpace(id)
	if id == "" {
		return nil, fmt.Errorf("id is required")
	}
	wf := h.workflowRunner.Store().Get(id)
	if wf == nil || wf.PeerID != peerID {
		return nil, fmt.Errorf("workflow %s not found", id)
	}
	return map[string]any{"workflow": wf}, nil
}

func (h *Handler) workflowDelete(peerID, id string) (map[string]any, error) {
	if h.workflowRunner == nil {
		return nil, fmt.Errorf("workflow engine is not enabled")
	}
	id = strings.TrimSpace(id)
	if id == "" {
		return nil, fmt.Errorf("id is required")
	}
	wf := h.workflowRunner.Store().Get(id)
	if wf == nil || wf.PeerID != peerID {
		return nil, fmt.Errorf("workflow %s not found", id)
	}
	if err := h.workflowRunner.Store().Delete(id); err != nil {
		return nil, err
	}
	return map[string]any{"ok": true, "id": id}, nil
}

func (h *Handler) workflowStartRun(ctx context.Context, peerID, workflowID string) (map[string]any, error) {
	if h.workflowRunner == nil {
		return nil, fmt.Errorf("workflow engine is not enabled")
	}
	workflowID = strings.TrimSpace(workflowID)
	if workflowID == "" {
		return nil, fmt.Errorf("id is required")
	}
	run, err := h.workflowRunner.Start(ctx, workflowID, peerID)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"ok":          true,
		"run_id":      run.ID,
		"workflow_id": run.WorkflowID,
		"status":      run.Status,
	}, nil
}

func (h *Handler) workflowStatus(peerID, runID string) (map[string]any, error) {
	if h.workflowRunner == nil {
		return nil, fmt.Errorf("workflow engine is not enabled")
	}
	runID = strings.TrimSpace(runID)
	if runID == "" {
		return nil, fmt.Errorf("run_id is required")
	}
	run, err := h.workflowRunner.Status(runID)
	if err != nil {
		return nil, err
	}
	if run == nil || run.PeerID != peerID {
		return nil, fmt.Errorf("run %s not found", runID)
	}
	return map[string]any{"run": run}, nil
}

func (h *Handler) workflowCancel(peerID, runID string) (map[string]any, error) {
	if h.workflowRunner == nil {
		return nil, fmt.Errorf("workflow engine is not enabled")
	}
	runID = strings.TrimSpace(runID)
	if runID == "" {
		return nil, fmt.Errorf("run_id is required")
	}
	// Verify ownership before cancelling.
	run, err := h.workflowRunner.Status(runID)
	if err != nil {
		return nil, err
	}
	if run == nil || run.PeerID != peerID {
		return nil, fmt.Errorf("run %s not found", runID)
	}
	if err := h.workflowRunner.Cancel(runID); err != nil {
		return nil, err
	}
	return map[string]any{"ok": true, "run_id": runID}, nil
}

func (h *Handler) workflowRuns(peerID, workflowID string, limit int) (map[string]any, error) {
	if h.workflowRunner == nil {
		return nil, fmt.Errorf("workflow engine is not enabled")
	}
	workflowID = strings.TrimSpace(workflowID)
	if workflowID == "" {
		return nil, fmt.Errorf("id is required")
	}
	// Verify ownership.
	wf := h.workflowRunner.Store().Get(workflowID)
	if wf == nil || wf.PeerID != peerID {
		return nil, fmt.Errorf("workflow %s not found", workflowID)
	}
	runs, err := h.workflowRunner.Store().ListRuns(peerID, workflowID)
	if err != nil {
		return nil, err
	}
	if limit > 0 && len(runs) > limit {
		runs = runs[:limit]
	}
	return map[string]any{"workflow_id": workflowID, "count": len(runs), "runs": runs}, nil
}
