package handler

import (
	"context"
	"log/slog"

	"github.com/ffimnsr/koios/internal/memory"
)

// ── memory ────────────────────────────────────────────────────────────────────

func (h *Handler) rpcMemorySearch(ctx context.Context, wsc *wsConn, req *rpcRequest) {
	var p struct {
		Q     string `json:"q"`
		Limit int    `json:"limit,omitempty"`
	}
	if err := decodeParams(req.Params, &p); err != nil {
		wsc.replyErr(req.ID, errCodeInvalidParams, err.Error())
		return
	}
	if p.Q == "" {
		wsc.replyErr(req.ID, errCodeInvalidParams, "q is required")
		return
	}
	result, err := h.memorySearch(wsc.peerID, p.Q, p.Limit, ctx)
	if err != nil {
		slog.Error("ws: memory search", "peer", wsc.peerID, "error", err)
		wsc.replyErr(req.ID, errCodeServer, "search error: "+err.Error())
		return
	}
	wsc.reply(req.ID, result)
}

func (h *Handler) rpcMemoryInsert(ctx context.Context, wsc *wsConn, req *rpcRequest) {
	var p struct {
		Content           string   `json:"content"`
		Tags              []string `json:"tags"`
		Category          string   `json:"category"`
		RetentionClass    string   `json:"retention_class"`
		ExposurePolicy    string   `json:"exposure_policy"`
		ExpiresAt         int64    `json:"expires_at"`
		CaptureKind       *string  `json:"capture_kind"`
		CaptureReason     *string  `json:"capture_reason"`
		Confidence        *float64 `json:"confidence"`
		SourceSessionKey  *string  `json:"source_session_key"`
		SourceMessageID   *string  `json:"source_message_id"`
		SourceRunID       *string  `json:"source_run_id"`
		SourceHook        *string  `json:"source_hook"`
		SourceCandidateID *string  `json:"source_candidate_id"`
		SourceExcerpt     *string  `json:"source_excerpt"`
	}
	if err := decodeParams(req.Params, &p); err != nil {
		wsc.replyErr(req.ID, errCodeInvalidParams, err.Error())
		return
	}
	result, err := h.memoryInsertWithOptions(wsc.peerID, p.Content, p.Tags, p.Category, p.RetentionClass, p.ExposurePolicy, p.ExpiresAt, chunkProvenance(p.CaptureKind, p.CaptureReason, p.Confidence, p.SourceSessionKey, p.SourceMessageID, p.SourceRunID, p.SourceHook, p.SourceCandidateID, p.SourceExcerpt), ctx)
	if err != nil {
		slog.Error("ws: memory insert", "peer", wsc.peerID, "error", err)
		wsc.replyErr(req.ID, errCodeServer, "insert error: "+err.Error())
		return
	}
	wsc.reply(req.ID, result)
}

func (h *Handler) rpcMemoryGet(ctx context.Context, wsc *wsConn, req *rpcRequest) {
	var p struct {
		ID string `json:"id"`
	}
	if err := decodeParams(req.Params, &p); err != nil {
		wsc.replyErr(req.ID, errCodeInvalidParams, err.Error())
		return
	}
	result, err := h.memoryGet(wsc.peerID, p.ID, ctx)
	if err != nil {
		slog.Error("ws: memory get", "peer", wsc.peerID, "error", err)
		wsc.replyErr(req.ID, errCodeServer, "get error: "+err.Error())
		return
	}
	wsc.reply(req.ID, result)
}

func (h *Handler) rpcMemoryTag(ctx context.Context, wsc *wsConn, req *rpcRequest) {
	var p struct {
		ID             string   `json:"id"`
		Tags           []string `json:"tags"`
		Category       string   `json:"category"`
		RetentionClass string   `json:"retention_class"`
		ExposurePolicy string   `json:"exposure_policy"`
		ExpiresAt      int64    `json:"expires_at"`
	}
	if err := decodeParams(req.Params, &p); err != nil {
		wsc.replyErr(req.ID, errCodeInvalidParams, err.Error())
		return
	}
	result, err := h.memoryTag(wsc.peerID, p.ID, p.Tags, p.Category, p.RetentionClass, p.ExposurePolicy, p.ExpiresAt, ctx)
	if err != nil {
		slog.Error("ws: memory tag", "peer", wsc.peerID, "error", err)
		wsc.replyErr(req.ID, errCodeServer, "tag error: "+err.Error())
		return
	}
	wsc.reply(req.ID, result)
}

func (h *Handler) rpcMemoryPreferenceCreate(ctx context.Context, wsc *wsConn, req *rpcRequest) {
	var p struct {
		Kind             string  `json:"kind"`
		Name             string  `json:"name"`
		Value            string  `json:"value"`
		Category         string  `json:"category"`
		Scope            string  `json:"scope"`
		ScopeRef         string  `json:"scope_ref"`
		Confidence       float64 `json:"confidence"`
		LastConfirmedAt  int64   `json:"last_confirmed_at"`
		SourceSessionKey string  `json:"source_session_key"`
		SourceExcerpt    string  `json:"source_excerpt"`
	}
	if err := decodeParams(req.Params, &p); err != nil {
		wsc.replyErr(req.ID, errCodeInvalidParams, err.Error())
		return
	}
	result, err := h.memoryPreferenceCreate(wsc.peerID, p.Kind, p.Name, p.Value, p.Category, p.Scope, p.ScopeRef, p.Confidence, p.LastConfirmedAt, p.SourceSessionKey, p.SourceExcerpt, ctx)
	if err != nil {
		slog.Error("ws: memory preference create", "peer", wsc.peerID, "error", err)
		wsc.replyErr(req.ID, errCodeServer, "preference create error: "+err.Error())
		return
	}
	wsc.reply(req.ID, result)
}

func (h *Handler) rpcMemoryPreferenceGet(ctx context.Context, wsc *wsConn, req *rpcRequest) {
	var p struct {
		ID string `json:"id"`
	}
	if err := decodeParams(req.Params, &p); err != nil {
		wsc.replyErr(req.ID, errCodeInvalidParams, err.Error())
		return
	}
	result, err := h.memoryPreferenceGet(wsc.peerID, p.ID, ctx)
	if err != nil {
		slog.Error("ws: memory preference get", "peer", wsc.peerID, "error", err)
		wsc.replyErr(req.ID, errCodeServer, "preference get error: "+err.Error())
		return
	}
	wsc.reply(req.ID, result)
}

func (h *Handler) rpcMemoryPreferenceList(ctx context.Context, wsc *wsConn, req *rpcRequest) {
	var p struct {
		Kind  string `json:"kind"`
		Scope string `json:"scope"`
		Limit int    `json:"limit"`
	}
	if err := decodeParams(req.Params, &p); err != nil {
		wsc.replyErr(req.ID, errCodeInvalidParams, err.Error())
		return
	}
	result, err := h.memoryPreferenceList(wsc.peerID, p.Kind, p.Scope, p.Limit, ctx)
	if err != nil {
		slog.Error("ws: memory preference list", "peer", wsc.peerID, "error", err)
		wsc.replyErr(req.ID, errCodeServer, "preference list error: "+err.Error())
		return
	}
	wsc.reply(req.ID, result)
}

func (h *Handler) rpcMemoryPreferenceUpdate(ctx context.Context, wsc *wsConn, req *rpcRequest) {
	var p struct {
		ID               string   `json:"id"`
		Kind             *string  `json:"kind"`
		Name             *string  `json:"name"`
		Value            *string  `json:"value"`
		Category         *string  `json:"category"`
		Scope            *string  `json:"scope"`
		ScopeRef         *string  `json:"scope_ref"`
		Confidence       *float64 `json:"confidence"`
		LastConfirmedAt  *int64   `json:"last_confirmed_at"`
		SourceSessionKey *string  `json:"source_session_key"`
		SourceExcerpt    *string  `json:"source_excerpt"`
	}
	if err := decodeParams(req.Params, &p); err != nil {
		wsc.replyErr(req.ID, errCodeInvalidParams, err.Error())
		return
	}
	result, err := h.memoryPreferenceUpdate(wsc.peerID, p.ID, preferencePatch(p.Kind, p.Name, p.Value, p.Category, p.Scope, p.ScopeRef, p.Confidence, p.LastConfirmedAt, p.SourceSessionKey, p.SourceExcerpt), ctx)
	if err != nil {
		slog.Error("ws: memory preference update", "peer", wsc.peerID, "error", err)
		wsc.replyErr(req.ID, errCodeServer, "preference update error: "+err.Error())
		return
	}
	wsc.reply(req.ID, result)
}

func (h *Handler) rpcMemoryPreferenceConfirm(ctx context.Context, wsc *wsConn, req *rpcRequest) {
	var p struct {
		ID              string   `json:"id"`
		LastConfirmedAt int64    `json:"last_confirmed_at"`
		Confidence      *float64 `json:"confidence"`
	}
	if err := decodeParams(req.Params, &p); err != nil {
		wsc.replyErr(req.ID, errCodeInvalidParams, err.Error())
		return
	}
	result, err := h.memoryPreferenceConfirm(wsc.peerID, p.ID, p.LastConfirmedAt, p.Confidence, ctx)
	if err != nil {
		slog.Error("ws: memory preference confirm", "peer", wsc.peerID, "error", err)
		wsc.replyErr(req.ID, errCodeServer, "preference confirm error: "+err.Error())
		return
	}
	wsc.reply(req.ID, result)
}

func (h *Handler) rpcMemoryPreferenceDelete(ctx context.Context, wsc *wsConn, req *rpcRequest) {
	var p struct {
		ID string `json:"id"`
	}
	if err := decodeParams(req.Params, &p); err != nil {
		wsc.replyErr(req.ID, errCodeInvalidParams, err.Error())
		return
	}
	result, err := h.memoryPreferenceDelete(wsc.peerID, p.ID, ctx)
	if err != nil {
		slog.Error("ws: memory preference delete", "peer", wsc.peerID, "error", err)
		wsc.replyErr(req.ID, errCodeServer, "preference delete error: "+err.Error())
		return
	}
	wsc.reply(req.ID, result)
}

func (h *Handler) rpcMemoryEntityCreate(ctx context.Context, wsc *wsConn, req *rpcRequest) {
	var p struct {
		Kind       string   `json:"kind"`
		Name       string   `json:"name"`
		Aliases    []string `json:"aliases"`
		Notes      string   `json:"notes"`
		LastSeenAt int64    `json:"last_seen_at"`
	}
	if err := decodeParams(req.Params, &p); err != nil {
		wsc.replyErr(req.ID, errCodeInvalidParams, err.Error())
		return
	}
	result, err := h.memoryEntityCreate(wsc.peerID, p.Kind, p.Name, p.Aliases, p.Notes, p.LastSeenAt, ctx)
	if err != nil {
		slog.Error("ws: memory entity create", "peer", wsc.peerID, "error", err)
		wsc.replyErr(req.ID, errCodeServer, "entity create error: "+err.Error())
		return
	}
	wsc.reply(req.ID, result)
}

func (h *Handler) rpcMemoryEntityUpdate(ctx context.Context, wsc *wsConn, req *rpcRequest) {
	var p struct {
		ID         string    `json:"id"`
		Kind       *string   `json:"kind"`
		Name       *string   `json:"name"`
		Aliases    *[]string `json:"aliases"`
		Notes      *string   `json:"notes"`
		LastSeenAt *int64    `json:"last_seen_at"`
	}
	if err := decodeParams(req.Params, &p); err != nil {
		wsc.replyErr(req.ID, errCodeInvalidParams, err.Error())
		return
	}
	result, err := h.memoryEntityUpdate(wsc.peerID, p.ID, entityPatch(p.Kind, p.Name, p.Aliases, p.Notes, p.LastSeenAt), ctx)
	if err != nil {
		slog.Error("ws: memory entity update", "peer", wsc.peerID, "error", err)
		wsc.replyErr(req.ID, errCodeServer, "entity update error: "+err.Error())
		return
	}
	wsc.reply(req.ID, result)
}

func (h *Handler) rpcMemoryEntityGet(ctx context.Context, wsc *wsConn, req *rpcRequest) {
	var p struct {
		ID string `json:"id"`
	}
	if err := decodeParams(req.Params, &p); err != nil {
		wsc.replyErr(req.ID, errCodeInvalidParams, err.Error())
		return
	}
	result, err := h.memoryEntityGet(wsc.peerID, p.ID, ctx)
	if err != nil {
		slog.Error("ws: memory entity get", "peer", wsc.peerID, "error", err)
		wsc.replyErr(req.ID, errCodeServer, "entity get error: "+err.Error())
		return
	}
	wsc.reply(req.ID, result)
}

func (h *Handler) rpcMemoryEntityList(ctx context.Context, wsc *wsConn, req *rpcRequest) {
	var p struct {
		Kind  string `json:"kind"`
		Limit int    `json:"limit"`
	}
	if err := decodeParams(req.Params, &p); err != nil {
		wsc.replyErr(req.ID, errCodeInvalidParams, err.Error())
		return
	}
	result, err := h.memoryEntityList(wsc.peerID, p.Kind, p.Limit, ctx)
	if err != nil {
		slog.Error("ws: memory entity list", "peer", wsc.peerID, "error", err)
		wsc.replyErr(req.ID, errCodeServer, "entity list error: "+err.Error())
		return
	}
	wsc.reply(req.ID, result)
}

func (h *Handler) rpcMemoryEntitySearch(ctx context.Context, wsc *wsConn, req *rpcRequest) {
	var p struct {
		Q     string `json:"q"`
		Limit int    `json:"limit"`
	}
	if err := decodeParams(req.Params, &p); err != nil {
		wsc.replyErr(req.ID, errCodeInvalidParams, err.Error())
		return
	}
	result, err := h.memoryEntitySearch(wsc.peerID, p.Q, p.Limit, ctx)
	if err != nil {
		slog.Error("ws: memory entity search", "peer", wsc.peerID, "error", err)
		wsc.replyErr(req.ID, errCodeServer, "entity search error: "+err.Error())
		return
	}
	wsc.reply(req.ID, result)
}

func (h *Handler) rpcMemoryEntityLinkChunk(ctx context.Context, wsc *wsConn, req *rpcRequest) {
	var p struct {
		ID      string `json:"id"`
		ChunkID string `json:"chunk_id"`
	}
	if err := decodeParams(req.Params, &p); err != nil {
		wsc.replyErr(req.ID, errCodeInvalidParams, err.Error())
		return
	}
	result, err := h.memoryEntityLinkChunk(wsc.peerID, p.ID, p.ChunkID, ctx)
	if err != nil {
		slog.Error("ws: memory entity link chunk", "peer", wsc.peerID, "error", err)
		wsc.replyErr(req.ID, errCodeServer, "entity link error: "+err.Error())
		return
	}
	wsc.reply(req.ID, result)
}

func (h *Handler) rpcMemoryEntityRelate(ctx context.Context, wsc *wsConn, req *rpcRequest) {
	var p struct {
		SourceID string `json:"source_id"`
		TargetID string `json:"target_id"`
		Relation string `json:"relation"`
		Notes    string `json:"notes"`
	}
	if err := decodeParams(req.Params, &p); err != nil {
		wsc.replyErr(req.ID, errCodeInvalidParams, err.Error())
		return
	}
	result, err := h.memoryEntityRelate(wsc.peerID, p.SourceID, p.TargetID, p.Relation, p.Notes, ctx)
	if err != nil {
		slog.Error("ws: memory entity relate", "peer", wsc.peerID, "error", err)
		wsc.replyErr(req.ID, errCodeServer, "entity relate error: "+err.Error())
		return
	}
	wsc.reply(req.ID, result)
}

func (h *Handler) rpcMemoryEntityTouch(ctx context.Context, wsc *wsConn, req *rpcRequest) {
	var p struct {
		ID         string `json:"id"`
		LastSeenAt int64  `json:"last_seen_at"`
	}
	if err := decodeParams(req.Params, &p); err != nil {
		wsc.replyErr(req.ID, errCodeInvalidParams, err.Error())
		return
	}
	result, err := h.memoryEntityTouch(wsc.peerID, p.ID, p.LastSeenAt, ctx)
	if err != nil {
		slog.Error("ws: memory entity touch", "peer", wsc.peerID, "error", err)
		wsc.replyErr(req.ID, errCodeServer, "entity touch error: "+err.Error())
		return
	}
	wsc.reply(req.ID, result)
}

func (h *Handler) rpcMemoryEntityDelete(ctx context.Context, wsc *wsConn, req *rpcRequest) {
	var p struct {
		ID string `json:"id"`
	}
	if err := decodeParams(req.Params, &p); err != nil {
		wsc.replyErr(req.ID, errCodeInvalidParams, err.Error())
		return
	}
	result, err := h.memoryEntityDelete(wsc.peerID, p.ID, ctx)
	if err != nil {
		slog.Error("ws: memory entity delete", "peer", wsc.peerID, "error", err)
		wsc.replyErr(req.ID, errCodeServer, "entity delete error: "+err.Error())
		return
	}
	wsc.reply(req.ID, result)
}

func (h *Handler) rpcMemoryEntityUnlinkChunk(ctx context.Context, wsc *wsConn, req *rpcRequest) {
	var p struct {
		ID      string `json:"id"`
		ChunkID string `json:"chunk_id"`
	}
	if err := decodeParams(req.Params, &p); err != nil {
		wsc.replyErr(req.ID, errCodeInvalidParams, err.Error())
		return
	}
	result, err := h.memoryEntityUnlinkChunk(wsc.peerID, p.ID, p.ChunkID, ctx)
	if err != nil {
		slog.Error("ws: memory entity unlink chunk", "peer", wsc.peerID, "error", err)
		wsc.replyErr(req.ID, errCodeServer, "entity unlink error: "+err.Error())
		return
	}
	wsc.reply(req.ID, result)
}

func (h *Handler) rpcMemoryEntityUnrelate(ctx context.Context, wsc *wsConn, req *rpcRequest) {
	var p struct {
		SourceID string `json:"source_id"`
		TargetID string `json:"target_id"`
		Relation string `json:"relation"`
	}
	if err := decodeParams(req.Params, &p); err != nil {
		wsc.replyErr(req.ID, errCodeInvalidParams, err.Error())
		return
	}
	result, err := h.memoryEntityUnrelate(wsc.peerID, p.SourceID, p.TargetID, p.Relation, ctx)
	if err != nil {
		slog.Error("ws: memory entity unrelate", "peer", wsc.peerID, "error", err)
		wsc.replyErr(req.ID, errCodeServer, "entity unrelate error: "+err.Error())
		return
	}
	wsc.reply(req.ID, result)
}

func (h *Handler) rpcMemoryList(ctx context.Context, wsc *wsConn, req *rpcRequest) {
	var p struct {
		Limit int `json:"limit,omitempty"`
	}
	if err := decodeParams(req.Params, &p); err != nil {
		wsc.replyErr(req.ID, errCodeInvalidParams, err.Error())
		return
	}
	if p.Limit <= 0 {
		p.Limit = 100
	}
	if p.Limit > 500 {
		p.Limit = 500
	}
	chunks, err := h.memStore.List(ctx, wsc.peerID, p.Limit)
	if err != nil {
		slog.Error("ws: memory list", "peer", wsc.peerID, "error", err)
		wsc.replyErr(req.ID, errCodeServer, "list error: "+err.Error())
		return
	}
	if chunks == nil {
		chunks = []memory.Chunk{}
	}
	wsc.reply(req.ID, map[string]any{"chunks": chunks})
}

func (h *Handler) rpcMemoryDelete(ctx context.Context, wsc *wsConn, req *rpcRequest) {
	var p struct {
		ID string `json:"id"`
	}
	if err := decodeParams(req.Params, &p); err != nil {
		wsc.replyErr(req.ID, errCodeInvalidParams, err.Error())
		return
	}
	if p.ID == "" {
		wsc.replyErr(req.ID, errCodeInvalidParams, "id is required")
		return
	}
	if err := h.memStore.DeleteChunk(ctx, wsc.peerID, p.ID); err != nil {
		slog.Error("ws: memory delete", "peer", wsc.peerID, "error", err)
		wsc.replyErr(req.ID, errCodeServer, "delete error: "+err.Error())
		return
	}
	wsc.reply(req.ID, map[string]bool{"ok": true})
}

func (h *Handler) rpcMemoryCandidateCreate(ctx context.Context, wsc *wsConn, req *rpcRequest) {
	var p struct {
		Content        string   `json:"content"`
		Tags           []string `json:"tags"`
		Category       string   `json:"category"`
		RetentionClass string   `json:"retention_class"`
		ExposurePolicy string   `json:"exposure_policy"`
		ExpiresAt      int64    `json:"expires_at"`
	}
	if err := decodeParams(req.Params, &p); err != nil {
		wsc.replyErr(req.ID, errCodeInvalidParams, err.Error())
		return
	}
	result, err := h.memoryCandidateCreate(wsc.peerID, p.Content, p.Tags, p.Category, p.RetentionClass, p.ExposurePolicy, p.ExpiresAt, ctx)
	if err != nil {
		slog.Error("ws: memory candidate create", "peer", wsc.peerID, "error", err)
		wsc.replyErr(req.ID, errCodeServer, "candidate create error: "+err.Error())
		return
	}
	wsc.reply(req.ID, result)
}

func (h *Handler) rpcMemoryCandidateList(ctx context.Context, wsc *wsConn, req *rpcRequest) {
	var p struct {
		Limit  int    `json:"limit,omitempty"`
		Status string `json:"status,omitempty"`
	}
	if err := decodeParams(req.Params, &p); err != nil {
		wsc.replyErr(req.ID, errCodeInvalidParams, err.Error())
		return
	}
	result, err := h.memoryCandidateList(wsc.peerID, p.Limit, p.Status, ctx)
	if err != nil {
		slog.Error("ws: memory candidate list", "peer", wsc.peerID, "error", err)
		wsc.replyErr(req.ID, errCodeServer, "candidate list error: "+err.Error())
		return
	}
	wsc.reply(req.ID, result)
}

func (h *Handler) rpcMemoryCandidateEdit(ctx context.Context, wsc *wsConn, req *rpcRequest) {
	var p struct {
		ID             string    `json:"id"`
		Content        *string   `json:"content"`
		Tags           *[]string `json:"tags"`
		Category       *string   `json:"category"`
		RetentionClass *string   `json:"retention_class"`
		ExposurePolicy *string   `json:"exposure_policy"`
		ExpiresAt      *int64    `json:"expires_at"`
	}
	if err := decodeParams(req.Params, &p); err != nil {
		wsc.replyErr(req.ID, errCodeInvalidParams, err.Error())
		return
	}
	result, err := h.memoryCandidateEdit(wsc.peerID, p.ID, candidatePatch(p.Content, p.Tags, p.Category, p.RetentionClass, p.ExposurePolicy, p.ExpiresAt), ctx)
	if err != nil {
		slog.Error("ws: memory candidate edit", "peer", wsc.peerID, "error", err)
		wsc.replyErr(req.ID, errCodeServer, "candidate edit error: "+err.Error())
		return
	}
	wsc.reply(req.ID, result)
}

func (h *Handler) rpcMemoryCandidateApprove(ctx context.Context, wsc *wsConn, req *rpcRequest) {
	var p struct {
		ID             string    `json:"id"`
		Reason         string    `json:"reason"`
		Content        *string   `json:"content"`
		Tags           *[]string `json:"tags"`
		Category       *string   `json:"category"`
		RetentionClass *string   `json:"retention_class"`
		ExposurePolicy *string   `json:"exposure_policy"`
		ExpiresAt      *int64    `json:"expires_at"`
	}
	if err := decodeParams(req.Params, &p); err != nil {
		wsc.replyErr(req.ID, errCodeInvalidParams, err.Error())
		return
	}
	result, err := h.memoryCandidateApprove(wsc.peerID, p.ID, candidatePatch(p.Content, p.Tags, p.Category, p.RetentionClass, p.ExposurePolicy, p.ExpiresAt), p.Reason, ctx)
	if err != nil {
		slog.Error("ws: memory candidate approve", "peer", wsc.peerID, "error", err)
		wsc.replyErr(req.ID, errCodeServer, "candidate approve error: "+err.Error())
		return
	}
	wsc.reply(req.ID, result)
}

func (h *Handler) rpcMemoryCandidateMerge(ctx context.Context, wsc *wsConn, req *rpcRequest) {
	var p struct {
		ID             string    `json:"id"`
		MergeIntoID    string    `json:"merge_into_id"`
		Reason         string    `json:"reason"`
		Content        *string   `json:"content"`
		Tags           *[]string `json:"tags"`
		Category       *string   `json:"category"`
		RetentionClass *string   `json:"retention_class"`
		ExposurePolicy *string   `json:"exposure_policy"`
		ExpiresAt      *int64    `json:"expires_at"`
	}
	if err := decodeParams(req.Params, &p); err != nil {
		wsc.replyErr(req.ID, errCodeInvalidParams, err.Error())
		return
	}
	result, err := h.memoryCandidateMerge(wsc.peerID, p.ID, p.MergeIntoID, candidatePatch(p.Content, p.Tags, p.Category, p.RetentionClass, p.ExposurePolicy, p.ExpiresAt), p.Reason, ctx)
	if err != nil {
		slog.Error("ws: memory candidate merge", "peer", wsc.peerID, "error", err)
		wsc.replyErr(req.ID, errCodeServer, "candidate merge error: "+err.Error())
		return
	}
	wsc.reply(req.ID, result)
}

func (h *Handler) rpcMemoryCandidateReject(ctx context.Context, wsc *wsConn, req *rpcRequest) {
	var p struct {
		ID     string `json:"id"`
		Reason string `json:"reason"`
	}
	if err := decodeParams(req.Params, &p); err != nil {
		wsc.replyErr(req.ID, errCodeInvalidParams, err.Error())
		return
	}
	result, err := h.memoryCandidateReject(wsc.peerID, p.ID, p.Reason, ctx)
	if err != nil {
		slog.Error("ws: memory candidate reject", "peer", wsc.peerID, "error", err)
		wsc.replyErr(req.ID, errCodeServer, "candidate reject error: "+err.Error())
		return
	}
	wsc.reply(req.ID, result)
}
