package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/ffimnsr/koios/internal/agent"
	"github.com/ffimnsr/koios/internal/artifacts"
	"github.com/ffimnsr/koios/internal/bookmarks"
	"github.com/ffimnsr/koios/internal/briefing"
	"github.com/ffimnsr/koios/internal/calendar"
	"github.com/ffimnsr/koios/internal/decisions"
	"github.com/ffimnsr/koios/internal/notes"
	"github.com/ffimnsr/koios/internal/plans"
	"github.com/ffimnsr/koios/internal/preferences"
	"github.com/ffimnsr/koios/internal/projects"
	"github.com/ffimnsr/koios/internal/reminder"
	"github.com/ffimnsr/koios/internal/scheduler"
	"github.com/ffimnsr/koios/internal/tasks"
	"github.com/ffimnsr/koios/internal/toolresults"
)

func (h *Handler) executeDataTool(ctx context.Context, peerID string, call agent.ToolCall) (any, error) {
	switch call.Name {
	case "time.now":
		return map[string]string{"utc": time.Now().UTC().Format(time.RFC3339)}, nil
	case "subagent.status":
		var args struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(call.Arguments, &args); err != nil {
			return nil, fmt.Errorf("invalid arguments: %w", err)
		}
		if h.subRuntime == nil {
			return nil, fmt.Errorf("sub-sessions are not enabled")
		}
		rec, ok := h.subRuntime.Get(strings.TrimSpace(args.ID))
		if !ok || rec.PeerID != peerID {
			return nil, fmt.Errorf("run %s not found", args.ID)
		}
		return rec, nil
	case "session.history":
		var args struct {
			Limit      int    `json:"limit"`
			SessionKey string `json:"session_key"`
			RunID      string `json:"run_id"`
		}
		if err := json.Unmarshal(call.Arguments, &args); err != nil {
			return nil, fmt.Errorf("invalid arguments: %w", err)
		}
		if strings.TrimSpace(args.RunID) != "" {
			if h.subRuntime == nil {
				return nil, fmt.Errorf("sub-sessions are not enabled")
			}
			rec, ok := h.subRuntime.Get(args.RunID)
			if !ok || rec.PeerID != peerID {
				return nil, fmt.Errorf("run %s not found", args.RunID)
			}
			msgs, err := h.subRuntime.Transcript(args.RunID)
			if err != nil {
				return nil, err
			}
			if args.Limit > 0 && len(msgs) > args.Limit {
				msgs = msgs[len(msgs)-args.Limit:]
			}
			return map[string]any{
				"peer_id":     peerID,
				"run_id":      rec.ID,
				"session_key": rec.SessionKey,
				"count":       len(msgs),
				"messages":    msgs,
			}, nil
		}
		sessionKey := peerID
		if k := strings.TrimSpace(args.SessionKey); k != "" {
			// Only allow keys that belong to this peer: either the bare peer ID
			// or any namespaced key with the "<peerID>::" prefix.
			if k != peerID && !strings.HasPrefix(k, peerID+"::") {
				return nil, fmt.Errorf("session_key %q is not accessible to peer %q", k, peerID)
			}
			sessionKey = k
		}
		history := h.store.Get(sessionKey).History()
		if args.Limit > 0 && len(history) > args.Limit {
			history = history[len(history)-args.Limit:]
		}
		return map[string]any{
			"peer_id":     peerID,
			"session_key": sessionKey,
			"count":       len(history),
			"messages":    history,
		}, nil
	case "bookmark.create":
		var args struct {
			Title            string   `json:"title"`
			Content          string   `json:"content"`
			Labels           []string `json:"labels"`
			ReminderAt       int64    `json:"reminder_at"`
			SourceKind       string   `json:"source_kind"`
			SourceSessionKey string   `json:"source_session_key"`
			SourceRunID      string   `json:"source_run_id"`
			SourceStartIndex int      `json:"source_start_index"`
			SourceEndIndex   int      `json:"source_end_index"`
			SourceExcerpt    string   `json:"source_excerpt"`
		}
		if err := json.Unmarshal(call.Arguments, &args); err != nil {
			return nil, fmt.Errorf("invalid arguments: %w", err)
		}
		return h.bookmarkCreate(peerID, bookmarks.Input{
			Title:            args.Title,
			Content:          args.Content,
			Labels:           args.Labels,
			ReminderAt:       args.ReminderAt,
			SourceKind:       args.SourceKind,
			SourceSessionKey: args.SourceSessionKey,
			SourceRunID:      args.SourceRunID,
			SourceStartIndex: args.SourceStartIndex,
			SourceEndIndex:   args.SourceEndIndex,
			SourceExcerpt:    args.SourceExcerpt,
		}, ctx)
	case "bookmark.capture_session":
		var args struct {
			SessionKey string   `json:"session_key"`
			RunID      string   `json:"run_id"`
			Title      string   `json:"title"`
			StartIndex int      `json:"start_index"`
			EndIndex   int      `json:"end_index"`
			Labels     []string `json:"labels"`
			ReminderAt int64    `json:"reminder_at"`
		}
		if err := json.Unmarshal(call.Arguments, &args); err != nil {
			return nil, fmt.Errorf("invalid arguments: %w", err)
		}
		return h.bookmarkCaptureSession(peerID, args.SessionKey, args.RunID, args.Title, args.StartIndex, args.EndIndex, args.Labels, args.ReminderAt, ctx)
	case "bookmark.get":
		var args struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(call.Arguments, &args); err != nil {
			return nil, fmt.Errorf("invalid arguments: %w", err)
		}
		return h.bookmarkGet(peerID, args.ID, ctx)
	case "bookmark.list":
		var args struct {
			Limit        int    `json:"limit"`
			Label        string `json:"label"`
			UpcomingOnly bool   `json:"upcoming_only"`
		}
		if err := json.Unmarshal(call.Arguments, &args); err != nil {
			return nil, fmt.Errorf("invalid arguments: %w", err)
		}
		return h.bookmarkList(peerID, args.Limit, args.Label, args.UpcomingOnly, ctx)
	case "bookmark.search":
		var args struct {
			Query string `json:"query"`
			Limit int    `json:"limit"`
		}
		if err := json.Unmarshal(call.Arguments, &args); err != nil {
			return nil, fmt.Errorf("invalid arguments: %w", err)
		}
		return h.bookmarkSearch(peerID, args.Query, args.Limit, ctx)
	case "bookmark.update":
		var args struct {
			ID               string    `json:"id"`
			Title            *string   `json:"title"`
			Content          *string   `json:"content"`
			Labels           *[]string `json:"labels"`
			ReminderAt       *int64    `json:"reminder_at"`
			SourceKind       *string   `json:"source_kind"`
			SourceSessionKey *string   `json:"source_session_key"`
			SourceRunID      *string   `json:"source_run_id"`
			SourceStartIndex *int      `json:"source_start_index"`
			SourceEndIndex   *int      `json:"source_end_index"`
			SourceExcerpt    *string   `json:"source_excerpt"`
		}
		if err := json.Unmarshal(call.Arguments, &args); err != nil {
			return nil, fmt.Errorf("invalid arguments: %w", err)
		}
		return h.bookmarkUpdate(peerID, args.ID, bookmarkPatch(args.Title, args.Content, args.Labels, args.ReminderAt, args.SourceKind, args.SourceSessionKey, args.SourceRunID, args.SourceStartIndex, args.SourceEndIndex, args.SourceExcerpt), ctx)
	case "bookmark.delete":
		var args struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(call.Arguments, &args); err != nil {
			return nil, fmt.Errorf("invalid arguments: %w", err)
		}
		return h.bookmarkDelete(peerID, args.ID, ctx)
	case "memory.search":
		var args struct {
			Q     string `json:"q"`
			Limit int    `json:"limit"`
		}
		if err := json.Unmarshal(call.Arguments, &args); err != nil {
			return nil, fmt.Errorf("invalid arguments: %w", err)
		}
		return h.memorySearch(peerID, args.Q, args.Limit, ctx)
	case "memory.insert":
		var args struct {
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
		if err := json.Unmarshal(call.Arguments, &args); err != nil {
			return nil, fmt.Errorf("invalid arguments: %w", err)
		}
		return h.memoryInsertWithOptions(peerID, args.Content, args.Tags, args.Category, args.RetentionClass, args.ExposurePolicy, args.ExpiresAt, chunkProvenance(args.CaptureKind, args.CaptureReason, args.Confidence, args.SourceSessionKey, args.SourceMessageID, args.SourceRunID, args.SourceHook, args.SourceCandidateID, args.SourceExcerpt), ctx)
	case "memory.get":
		var args struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(call.Arguments, &args); err != nil {
			return nil, fmt.Errorf("invalid arguments: %w", err)
		}
		return h.memoryGet(peerID, args.ID, ctx)
	case "memory.delete":
		var args struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(call.Arguments, &args); err != nil {
			return nil, fmt.Errorf("invalid arguments: %w", err)
		}
		return h.memoryDelete(peerID, args.ID, ctx)
	case "memory.list":
		var args struct {
			Limit int `json:"limit"`
		}
		if err := json.Unmarshal(call.Arguments, &args); err != nil {
			return nil, fmt.Errorf("invalid arguments: %w", err)
		}
		return h.memoryList(peerID, args.Limit, ctx)
	case "memory.timeline":
		var args struct {
			AnchorID    string `json:"anchor_id"`
			DepthBefore int    `json:"depth_before"`
			DepthAfter  int    `json:"depth_after"`
		}
		if err := json.Unmarshal(call.Arguments, &args); err != nil {
			return nil, fmt.Errorf("invalid arguments: %w", err)
		}
		return h.memoryTimeline(peerID, args.AnchorID, args.DepthBefore, args.DepthAfter, ctx)
	case "memory.batch_get":
		var args struct {
			IDs []string `json:"ids"`
		}
		if err := json.Unmarshal(call.Arguments, &args); err != nil {
			return nil, fmt.Errorf("invalid arguments: %w", err)
		}
		return h.memoryBatchGet(peerID, args.IDs, ctx)
	case "memory.tag":
		var args struct {
			ID             string   `json:"id"`
			Tags           []string `json:"tags"`
			Category       string   `json:"category"`
			RetentionClass string   `json:"retention_class"`
			ExposurePolicy string   `json:"exposure_policy"`
			ExpiresAt      int64    `json:"expires_at"`
		}
		if err := json.Unmarshal(call.Arguments, &args); err != nil {
			return nil, fmt.Errorf("invalid arguments: %w", err)
		}
		return h.memoryTag(peerID, args.ID, args.Tags, args.Category, args.RetentionClass, args.ExposurePolicy, args.ExpiresAt, ctx)
	case "memory.stats":
		return h.memoryStats(peerID, ctx)
	case "memory.preference.create":
		var args struct {
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
		if err := json.Unmarshal(call.Arguments, &args); err != nil {
			return nil, fmt.Errorf("invalid arguments: %w", err)
		}
		return h.memoryPreferenceCreate(peerID, args.Kind, args.Name, args.Value, args.Category, args.Scope, args.ScopeRef, args.Confidence, args.LastConfirmedAt, args.SourceSessionKey, args.SourceExcerpt, ctx)
	case "memory.preference.get":
		var args struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(call.Arguments, &args); err != nil {
			return nil, fmt.Errorf("invalid arguments: %w", err)
		}
		return h.memoryPreferenceGet(peerID, args.ID, ctx)
	case "memory.preference.list":
		var args struct {
			Kind  string `json:"kind"`
			Scope string `json:"scope"`
			Limit int    `json:"limit"`
		}
		if err := json.Unmarshal(call.Arguments, &args); err != nil {
			return nil, fmt.Errorf("invalid arguments: %w", err)
		}
		return h.memoryPreferenceList(peerID, args.Kind, args.Scope, args.Limit, ctx)
	case "memory.preference.update":
		var args struct {
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
		if err := json.Unmarshal(call.Arguments, &args); err != nil {
			return nil, fmt.Errorf("invalid arguments: %w", err)
		}
		return h.memoryPreferenceUpdate(peerID, args.ID, preferencePatch(args.Kind, args.Name, args.Value, args.Category, args.Scope, args.ScopeRef, args.Confidence, args.LastConfirmedAt, args.SourceSessionKey, args.SourceExcerpt), ctx)
	case "memory.preference.confirm":
		var args struct {
			ID              string   `json:"id"`
			LastConfirmedAt int64    `json:"last_confirmed_at"`
			Confidence      *float64 `json:"confidence"`
		}
		if err := json.Unmarshal(call.Arguments, &args); err != nil {
			return nil, fmt.Errorf("invalid arguments: %w", err)
		}
		return h.memoryPreferenceConfirm(peerID, args.ID, args.LastConfirmedAt, args.Confidence, ctx)
	case "memory.preference.delete":
		var args struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(call.Arguments, &args); err != nil {
			return nil, fmt.Errorf("invalid arguments: %w", err)
		}
		return h.memoryPreferenceDelete(peerID, args.ID, ctx)
	case "memory.candidate.create":
		var args struct {
			Content        string   `json:"content"`
			Tags           []string `json:"tags"`
			Category       string   `json:"category"`
			RetentionClass string   `json:"retention_class"`
			ExposurePolicy string   `json:"exposure_policy"`
			ExpiresAt      int64    `json:"expires_at"`
		}
		if err := json.Unmarshal(call.Arguments, &args); err != nil {
			return nil, fmt.Errorf("invalid arguments: %w", err)
		}
		return h.memoryCandidateCreate(peerID, args.Content, args.Tags, args.Category, args.RetentionClass, args.ExposurePolicy, args.ExpiresAt, ctx)
	case "memory.candidate.list":
		var args struct {
			Limit  int    `json:"limit"`
			Status string `json:"status"`
		}
		if err := json.Unmarshal(call.Arguments, &args); err != nil {
			return nil, fmt.Errorf("invalid arguments: %w", err)
		}
		return h.memoryCandidateList(peerID, args.Limit, args.Status, ctx)
	case "memory.candidate.edit":
		var args struct {
			ID             string    `json:"id"`
			Content        *string   `json:"content"`
			Tags           *[]string `json:"tags"`
			Category       *string   `json:"category"`
			RetentionClass *string   `json:"retention_class"`
			ExposurePolicy *string   `json:"exposure_policy"`
			ExpiresAt      *int64    `json:"expires_at"`
		}
		if err := json.Unmarshal(call.Arguments, &args); err != nil {
			return nil, fmt.Errorf("invalid arguments: %w", err)
		}
		return h.memoryCandidateEdit(peerID, args.ID, candidatePatch(args.Content, args.Tags, args.Category, args.RetentionClass, args.ExposurePolicy, args.ExpiresAt), ctx)
	case "memory.candidate.approve":
		var args struct {
			ID             string    `json:"id"`
			Reason         string    `json:"reason"`
			Content        *string   `json:"content"`
			Tags           *[]string `json:"tags"`
			Category       *string   `json:"category"`
			RetentionClass *string   `json:"retention_class"`
			ExposurePolicy *string   `json:"exposure_policy"`
			ExpiresAt      *int64    `json:"expires_at"`
		}
		if err := json.Unmarshal(call.Arguments, &args); err != nil {
			return nil, fmt.Errorf("invalid arguments: %w", err)
		}
		return h.memoryCandidateApprove(peerID, args.ID, candidatePatch(args.Content, args.Tags, args.Category, args.RetentionClass, args.ExposurePolicy, args.ExpiresAt), args.Reason, ctx)
	case "memory.candidate.merge":
		var args struct {
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
		if err := json.Unmarshal(call.Arguments, &args); err != nil {
			return nil, fmt.Errorf("invalid arguments: %w", err)
		}
		return h.memoryCandidateMerge(peerID, args.ID, args.MergeIntoID, candidatePatch(args.Content, args.Tags, args.Category, args.RetentionClass, args.ExposurePolicy, args.ExpiresAt), args.Reason, ctx)
	case "memory.candidate.reject":
		var args struct {
			ID     string `json:"id"`
			Reason string `json:"reason"`
		}
		if err := json.Unmarshal(call.Arguments, &args); err != nil {
			return nil, fmt.Errorf("invalid arguments: %w", err)
		}
		return h.memoryCandidateReject(peerID, args.ID, args.Reason, ctx)
	case "memory.entity.create":
		var args struct {
			Kind       string   `json:"kind"`
			Name       string   `json:"name"`
			Aliases    []string `json:"aliases"`
			Notes      string   `json:"notes"`
			LastSeenAt int64    `json:"last_seen_at"`
		}
		if err := json.Unmarshal(call.Arguments, &args); err != nil {
			return nil, fmt.Errorf("invalid arguments: %w", err)
		}
		return h.memoryEntityCreate(peerID, args.Kind, args.Name, args.Aliases, args.Notes, args.LastSeenAt, ctx)
	case "memory.entity.update":
		var args struct {
			ID         string    `json:"id"`
			Kind       *string   `json:"kind"`
			Name       *string   `json:"name"`
			Aliases    *[]string `json:"aliases"`
			Notes      *string   `json:"notes"`
			LastSeenAt *int64    `json:"last_seen_at"`
		}
		if err := json.Unmarshal(call.Arguments, &args); err != nil {
			return nil, fmt.Errorf("invalid arguments: %w", err)
		}
		return h.memoryEntityUpdate(peerID, args.ID, entityPatch(args.Kind, args.Name, args.Aliases, args.Notes, args.LastSeenAt), ctx)
	case "memory.entity.get":
		var args struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(call.Arguments, &args); err != nil {
			return nil, fmt.Errorf("invalid arguments: %w", err)
		}
		return h.memoryEntityGet(peerID, args.ID, ctx)
	case "memory.entity.list":
		var args struct {
			Kind  string `json:"kind"`
			Limit int    `json:"limit"`
		}
		if err := json.Unmarshal(call.Arguments, &args); err != nil {
			return nil, fmt.Errorf("invalid arguments: %w", err)
		}
		return h.memoryEntityList(peerID, args.Kind, args.Limit, ctx)
	case "memory.entity.search":
		var args struct {
			Q     string `json:"q"`
			Limit int    `json:"limit"`
		}
		if err := json.Unmarshal(call.Arguments, &args); err != nil {
			return nil, fmt.Errorf("invalid arguments: %w", err)
		}
		return h.memoryEntitySearch(peerID, args.Q, args.Limit, ctx)
	case "memory.entity.link_chunk":
		var args struct {
			ID      string `json:"id"`
			ChunkID string `json:"chunk_id"`
		}
		if err := json.Unmarshal(call.Arguments, &args); err != nil {
			return nil, fmt.Errorf("invalid arguments: %w", err)
		}
		return h.memoryEntityLinkChunk(peerID, args.ID, args.ChunkID, ctx)
	case "memory.entity.relate":
		var args struct {
			SourceID string `json:"source_id"`
			TargetID string `json:"target_id"`
			Relation string `json:"relation"`
			Notes    string `json:"notes"`
		}
		if err := json.Unmarshal(call.Arguments, &args); err != nil {
			return nil, fmt.Errorf("invalid arguments: %w", err)
		}
		return h.memoryEntityRelate(peerID, args.SourceID, args.TargetID, args.Relation, args.Notes, ctx)
	case "memory.entity.touch":
		var args struct {
			ID         string `json:"id"`
			LastSeenAt int64  `json:"last_seen_at"`
		}
		if err := json.Unmarshal(call.Arguments, &args); err != nil {
			return nil, fmt.Errorf("invalid arguments: %w", err)
		}
		return h.memoryEntityTouch(peerID, args.ID, args.LastSeenAt, ctx)
	case "memory.entity.delete":
		var args struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(call.Arguments, &args); err != nil {
			return nil, fmt.Errorf("invalid arguments: %w", err)
		}
		return h.memoryEntityDelete(peerID, args.ID, ctx)
	case "memory.entity.unlink_chunk":
		var args struct {
			ID      string `json:"id"`
			ChunkID string `json:"chunk_id"`
		}
		if err := json.Unmarshal(call.Arguments, &args); err != nil {
			return nil, fmt.Errorf("invalid arguments: %w", err)
		}
		return h.memoryEntityUnlinkChunk(peerID, args.ID, args.ChunkID, ctx)
	case "memory.entity.unrelate":
		var args struct {
			SourceID string `json:"source_id"`
			TargetID string `json:"target_id"`
			Relation string `json:"relation"`
		}
		if err := json.Unmarshal(call.Arguments, &args); err != nil {
			return nil, fmt.Errorf("invalid arguments: %w", err)
		}
		return h.memoryEntityUnrelate(peerID, args.SourceID, args.TargetID, args.Relation, ctx)
	case "task.create":
		var args struct {
			Title            string `json:"title"`
			Details          string `json:"details"`
			Owner            string `json:"owner"`
			DueAt            int64  `json:"due_at"`
			SourceSessionKey string `json:"source_session_key"`
			SourceExcerpt    string `json:"source_excerpt"`
		}
		if err := json.Unmarshal(call.Arguments, &args); err != nil {
			return nil, fmt.Errorf("invalid arguments: %w", err)
		}
		sourceSessionKey := strings.TrimSpace(args.SourceSessionKey)
		if sourceSessionKey == "" {
			sourceSessionKey = agent.CurrentSessionKey(ctx)
		}
		if sourceSessionKey == "" {
			sourceSessionKey = peerID
		}
		return h.taskCreate(peerID, tasks.CandidateInput{
			Title:   args.Title,
			Details: args.Details,
			Owner:   args.Owner,
			DueAt:   args.DueAt,
		}, tasks.CandidateProvenance{
			CaptureKind:      tasks.CandidateCaptureManual,
			SourceSessionKey: sourceSessionKey,
			SourceExcerpt:    args.SourceExcerpt,
		}, ctx)
	case "task.extract":
		var args struct {
			Text             string `json:"text"`
			CaptureKind      string `json:"capture_kind"`
			SourceSessionKey string `json:"source_session_key"`
			SourceExcerpt    string `json:"source_excerpt"`
		}
		if err := json.Unmarshal(call.Arguments, &args); err != nil {
			return nil, fmt.Errorf("invalid arguments: %w", err)
		}
		captureKind := strings.TrimSpace(args.CaptureKind)
		if captureKind == "" {
			captureKind = tasks.CandidateCaptureExternalExtract
		}
		return h.taskCandidateExtract(peerID, args.Text, tasks.CandidateProvenance{
			CaptureKind:      captureKind,
			SourceSessionKey: strings.TrimSpace(args.SourceSessionKey),
			SourceExcerpt:    args.SourceExcerpt,
		}, ctx)
	case "task.list":
		var args struct {
			Limit  int    `json:"limit"`
			Status string `json:"status"`
		}
		if err := json.Unmarshal(call.Arguments, &args); err != nil {
			return nil, fmt.Errorf("invalid arguments: %w", err)
		}
		return h.taskList(peerID, args.Limit, args.Status, ctx)
	case "task.get":
		var args struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(call.Arguments, &args); err != nil {
			return nil, fmt.Errorf("invalid arguments: %w", err)
		}
		return h.taskGet(peerID, args.ID, ctx)
	case "task.update":
		var args struct {
			ID          string  `json:"id"`
			Title       *string `json:"title"`
			Details     *string `json:"details"`
			Owner       *string `json:"owner"`
			DueAt       *int64  `json:"due_at"`
			SnoozeUntil *int64  `json:"snooze_until"`
		}
		if err := json.Unmarshal(call.Arguments, &args); err != nil {
			return nil, fmt.Errorf("invalid arguments: %w", err)
		}
		return h.taskUpdate(peerID, args.ID, taskPatch(args.Title, args.Details, args.Owner, args.DueAt, args.SnoozeUntil), ctx)
	case "task.assign":
		var args struct {
			ID    string `json:"id"`
			Owner string `json:"owner"`
		}
		if err := json.Unmarshal(call.Arguments, &args); err != nil {
			return nil, fmt.Errorf("invalid arguments: %w", err)
		}
		return h.taskAssign(peerID, args.ID, args.Owner, ctx)
	case "task.snooze":
		var args struct {
			ID    string `json:"id"`
			Until int64  `json:"until"`
		}
		if err := json.Unmarshal(call.Arguments, &args); err != nil {
			return nil, fmt.Errorf("invalid arguments: %w", err)
		}
		return h.taskSnooze(peerID, args.ID, args.Until, ctx)
	case "task.complete":
		var args struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(call.Arguments, &args); err != nil {
			return nil, fmt.Errorf("invalid arguments: %w", err)
		}
		return h.taskComplete(peerID, args.ID, ctx)
	case "task.reopen":
		var args struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(call.Arguments, &args); err != nil {
			return nil, fmt.Errorf("invalid arguments: %w", err)
		}
		return h.taskReopen(peerID, args.ID, ctx)
	case "waiting.create":
		var args struct {
			Title            string `json:"title"`
			Details          string `json:"details"`
			WaitingFor       string `json:"waiting_for"`
			FollowUpAt       int64  `json:"follow_up_at"`
			RemindAt         int64  `json:"remind_at"`
			SourceSessionKey string `json:"source_session_key"`
			SourceExcerpt    string `json:"source_excerpt"`
		}
		if err := json.Unmarshal(call.Arguments, &args); err != nil {
			return nil, fmt.Errorf("invalid arguments: %w", err)
		}
		return h.waitingCreate(peerID, tasks.WaitingOnInput{
			Title:            args.Title,
			Details:          args.Details,
			WaitingFor:       args.WaitingFor,
			FollowUpAt:       args.FollowUpAt,
			RemindAt:         args.RemindAt,
			SourceSessionKey: args.SourceSessionKey,
			SourceExcerpt:    args.SourceExcerpt,
		}, ctx)
	case "waiting.list":
		var args struct {
			Limit  int    `json:"limit"`
			Status string `json:"status"`
		}
		if err := json.Unmarshal(call.Arguments, &args); err != nil {
			return nil, fmt.Errorf("invalid arguments: %w", err)
		}
		return h.waitingList(peerID, args.Limit, args.Status, ctx)
	case "waiting.get":
		var args struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(call.Arguments, &args); err != nil {
			return nil, fmt.Errorf("invalid arguments: %w", err)
		}
		return h.waitingGet(peerID, args.ID, ctx)
	case "waiting.update":
		var args struct {
			ID               string  `json:"id"`
			Title            *string `json:"title"`
			Details          *string `json:"details"`
			WaitingFor       *string `json:"waiting_for"`
			FollowUpAt       *int64  `json:"follow_up_at"`
			RemindAt         *int64  `json:"remind_at"`
			SnoozeUntil      *int64  `json:"snooze_until"`
			SourceSessionKey *string `json:"source_session_key"`
			SourceExcerpt    *string `json:"source_excerpt"`
		}
		if err := json.Unmarshal(call.Arguments, &args); err != nil {
			return nil, fmt.Errorf("invalid arguments: %w", err)
		}
		return h.waitingUpdate(peerID, args.ID, waitingPatch(args.Title, args.Details, args.WaitingFor, args.FollowUpAt, args.RemindAt, args.SnoozeUntil, args.SourceSessionKey, args.SourceExcerpt), ctx)
	case "waiting.snooze":
		var args struct {
			ID    string `json:"id"`
			Until int64  `json:"until"`
		}
		if err := json.Unmarshal(call.Arguments, &args); err != nil {
			return nil, fmt.Errorf("invalid arguments: %w", err)
		}
		return h.waitingSnooze(peerID, args.ID, args.Until, ctx)
	case "waiting.resolve":
		var args struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(call.Arguments, &args); err != nil {
			return nil, fmt.Errorf("invalid arguments: %w", err)
		}
		return h.waitingResolve(peerID, args.ID, ctx)
	case "waiting.reopen":
		var args struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(call.Arguments, &args); err != nil {
			return nil, fmt.Errorf("invalid arguments: %w", err)
		}
		return h.waitingReopen(peerID, args.ID, ctx)
	case "calendar.source.create":
		var args struct {
			Name     string `json:"name"`
			Path     string `json:"path"`
			URL      string `json:"url"`
			Timezone string `json:"timezone"`
			Enabled  *bool  `json:"enabled"`
		}
		if err := json.Unmarshal(call.Arguments, &args); err != nil {
			return nil, fmt.Errorf("invalid arguments: %w", err)
		}
		return h.calendarSourceCreate(peerID, calendar.SourceInput{Name: args.Name, Path: args.Path, URL: args.URL, Timezone: args.Timezone, Enabled: args.Enabled}, ctx)
	case "calendar.source.list":
		var args struct {
			EnabledOnly bool `json:"enabled_only"`
		}
		if err := json.Unmarshal(call.Arguments, &args); err != nil {
			return nil, fmt.Errorf("invalid arguments: %w", err)
		}
		return h.calendarSourceList(peerID, args.EnabledOnly, ctx)
	case "calendar.source.delete":
		var args struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(call.Arguments, &args); err != nil {
			return nil, fmt.Errorf("invalid arguments: %w", err)
		}
		return h.calendarSourceDelete(peerID, args.ID, ctx)
	case "calendar.agenda":
		var args struct {
			Scope    string `json:"scope"`
			Timezone string `json:"timezone"`
			Now      int64  `json:"now"`
			Limit    int    `json:"limit"`
		}
		if err := json.Unmarshal(call.Arguments, &args); err != nil {
			return nil, fmt.Errorf("invalid arguments: %w", err)
		}
		return h.calendarAgenda(peerID, calendar.AgendaQuery{Scope: args.Scope, Timezone: args.Timezone, Now: args.Now, Limit: args.Limit}, ctx)
	case "brief.generate":
		var args briefing.Options
		if err := json.Unmarshal(call.Arguments, &args); err != nil {
			return nil, fmt.Errorf("invalid arguments: %w", err)
		}
		return h.briefGenerate(peerID, args, ctx)
	case "brief.preview":
		var args briefing.Options
		if err := json.Unmarshal(call.Arguments, &args); err != nil {
			return nil, fmt.Errorf("invalid arguments: %w", err)
		}
		return h.briefPreview(peerID, args, ctx)
	case "brief.save":
		var args struct {
			briefing.Options
			Title string `json:"title"`
		}
		if err := json.Unmarshal(call.Arguments, &args); err != nil {
			return nil, fmt.Errorf("invalid arguments: %w", err)
		}
		return h.briefSave(peerID, args.Options, args.Title, ctx)
	case "brief.send":
		var args struct {
			briefing.Options
			Title string `json:"title"`
		}
		if err := json.Unmarshal(call.Arguments, &args); err != nil {
			return nil, fmt.Errorf("invalid arguments: %w", err)
		}
		return h.briefSend(peerID, args.Options, args.Title, ctx)
	case "brief.schedule":
		var args struct {
			Name     string             `json:"name"`
			Kind     string             `json:"kind"`
			Timezone string             `json:"timezone"`
			Schedule scheduler.Schedule `json:"schedule"`
		}
		if err := json.Unmarshal(call.Arguments, &args); err != nil {
			return nil, fmt.Errorf("invalid arguments: %w", err)
		}
		return h.briefSchedule(peerID, args.Name, briefing.Options{Kind: args.Kind, Timezone: args.Timezone}, args.Schedule, ctx)
	case "calendar.create":
		var args struct {
			Summary     string `json:"summary"`
			Description string `json:"description"`
			Location    string `json:"location"`
			StartAt     int64  `json:"start_at"`
			EndAt       int64  `json:"end_at"`
			AllDay      bool   `json:"all_day"`
			Timezone    string `json:"timezone"`
			Status      string `json:"status"`
		}
		if err := json.Unmarshal(call.Arguments, &args); err != nil {
			return nil, fmt.Errorf("invalid arguments: %w", err)
		}
		return h.calendarCreate(peerID, calendar.LocalEventInput{
			Summary:     args.Summary,
			Description: args.Description,
			Location:    args.Location,
			StartAt:     args.StartAt,
			EndAt:       args.EndAt,
			AllDay:      args.AllDay,
			Timezone:    args.Timezone,
			Status:      args.Status,
		}, ctx)
	case "calendar.update":
		var args struct {
			ID          string  `json:"id"`
			Summary     *string `json:"summary"`
			Description *string `json:"description"`
			Location    *string `json:"location"`
			StartAt     *int64  `json:"start_at"`
			EndAt       *int64  `json:"end_at"`
			AllDay      *bool   `json:"all_day"`
			Timezone    *string `json:"timezone"`
			Status      *string `json:"status"`
		}
		if err := json.Unmarshal(call.Arguments, &args); err != nil {
			return nil, fmt.Errorf("invalid arguments: %w", err)
		}
		return h.calendarUpdate(peerID, args.ID, calendar.LocalEventPatch{
			Summary:     args.Summary,
			Description: args.Description,
			Location:    args.Location,
			StartAt:     args.StartAt,
			EndAt:       args.EndAt,
			AllDay:      args.AllDay,
			Timezone:    args.Timezone,
			Status:      args.Status,
		}, ctx)
	case "calendar.cancel":
		var args struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(call.Arguments, &args); err != nil {
			return nil, fmt.Errorf("invalid arguments: %w", err)
		}
		return h.calendarCancel(peerID, args.ID, ctx)
	case "reminder.create":
		var args struct {
			Title string `json:"title"`
			Body  string `json:"body"`
			DueAt int64  `json:"due_at"`
		}
		if err := json.Unmarshal(call.Arguments, &args); err != nil {
			return nil, fmt.Errorf("invalid arguments: %w", err)
		}
		return h.reminderCreate(peerID, reminder.Input{Title: args.Title, Body: args.Body, DueAt: args.DueAt}, ctx)
	case "reminder.list":
		var args struct {
			PendingOnly bool `json:"pending_only"`
			Limit       int  `json:"limit"`
		}
		if err := json.Unmarshal(call.Arguments, &args); err != nil {
			return nil, fmt.Errorf("invalid arguments: %w", err)
		}
		return h.reminderList(peerID, args.PendingOnly, args.Limit, ctx)
	case "reminder.complete":
		var args struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(call.Arguments, &args); err != nil {
			return nil, fmt.Errorf("invalid arguments: %w", err)
		}
		return h.reminderComplete(peerID, args.ID, ctx)
	case "note.create":
		var args struct {
			Title   string   `json:"title"`
			Content string   `json:"content"`
			Labels  []string `json:"labels"`
		}
		if err := json.Unmarshal(call.Arguments, &args); err != nil {
			return nil, fmt.Errorf("invalid arguments: %w", err)
		}
		return h.noteCreate(peerID, notes.Input{Title: args.Title, Content: args.Content, Labels: args.Labels}, ctx)
	case "note.search":
		var args struct {
			Query string `json:"query"`
			Limit int    `json:"limit"`
		}
		if err := json.Unmarshal(call.Arguments, &args); err != nil {
			return nil, fmt.Errorf("invalid arguments: %w", err)
		}
		return h.noteSearch(peerID, args.Query, args.Limit, ctx)
	case "note.update":
		var args struct {
			ID      string    `json:"id"`
			Title   *string   `json:"title"`
			Content *string   `json:"content"`
			Labels  *[]string `json:"labels"`
		}
		if err := json.Unmarshal(call.Arguments, &args); err != nil {
			return nil, fmt.Errorf("invalid arguments: %w", err)
		}
		return h.noteUpdate(peerID, args.ID, notePatch(args.Title, args.Content, args.Labels), ctx)
	case "scratchpad.create":
		var args struct {
			Content string `json:"content"`
		}
		if err := json.Unmarshal(call.Arguments, &args); err != nil {
			return nil, fmt.Errorf("invalid arguments: %w", err)
		}
		return h.scratchpadCreate(peerID, sessionKeyFromCtx(peerID, ctx), args.Content)
	case "scratchpad.update":
		var args struct {
			Content string `json:"content"`
		}
		if err := json.Unmarshal(call.Arguments, &args); err != nil {
			return nil, fmt.Errorf("invalid arguments: %w", err)
		}
		return h.scratchpadUpdate(peerID, sessionKeyFromCtx(peerID, ctx), args.Content)
	case "scratchpad.get":
		return h.scratchpadGet(peerID, sessionKeyFromCtx(peerID, ctx))
	case "scratchpad.clear":
		return h.scratchpadClear(peerID, sessionKeyFromCtx(peerID, ctx))
	case "plan.create":
		var args struct {
			Title       string            `json:"title"`
			Description string            `json:"description"`
			Steps       []plans.StepInput `json:"steps"`
		}
		if err := json.Unmarshal(call.Arguments, &args); err != nil {
			return nil, fmt.Errorf("invalid arguments: %w", err)
		}
		return h.planCreate(peerID, plans.Input{Title: args.Title, Description: args.Description, Steps: args.Steps}, ctx)
	case "plan.update":
		var args struct {
			ID          string             `json:"id"`
			Title       *string            `json:"title"`
			Description *string            `json:"description"`
			Status      *string            `json:"status"`
			Steps       *[]plans.StepInput `json:"steps"`
		}
		if err := json.Unmarshal(call.Arguments, &args); err != nil {
			return nil, fmt.Errorf("invalid arguments: %w", err)
		}
		var status *plans.PlanStatus
		if args.Status != nil {
			s := plans.PlanStatus(*args.Status)
			status = &s
		}
		return h.planUpdate(peerID, args.ID, planPatch(args.Title, args.Description, status, args.Steps), ctx)
	case "plan.status":
		var args struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(call.Arguments, &args); err != nil {
			return nil, fmt.Errorf("invalid arguments: %w", err)
		}
		return h.planStatus(peerID, args.ID, ctx)
	case "plan.complete_step":
		var args struct {
			PlanID string `json:"plan_id"`
			StepID string `json:"step_id"`
			Notes  string `json:"notes"`
		}
		if err := json.Unmarshal(call.Arguments, &args); err != nil {
			return nil, fmt.Errorf("invalid arguments: %w", err)
		}
		return h.planCompleteStep(peerID, args.PlanID, args.StepID, args.Notes, ctx)
	case "project.create":
		var args struct {
			Title       string   `json:"title"`
			Description string   `json:"description"`
			Labels      []string `json:"labels"`
		}
		if err := json.Unmarshal(call.Arguments, &args); err != nil {
			return nil, fmt.Errorf("invalid arguments: %w", err)
		}
		return h.projectCreate(peerID, projects.Input{Title: args.Title, Description: args.Description, Labels: args.Labels}, ctx)
	case "project.list":
		var args struct {
			Status string `json:"status"`
			Limit  int    `json:"limit"`
		}
		if err := json.Unmarshal(call.Arguments, &args); err != nil {
			return nil, fmt.Errorf("invalid arguments: %w", err)
		}
		return h.projectList(peerID, args.Limit, projects.ProjectStatus(args.Status), ctx)
	case "project.status":
		var args struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(call.Arguments, &args); err != nil {
			return nil, fmt.Errorf("invalid arguments: %w", err)
		}
		return h.projectStatus(peerID, args.ID, ctx)
	case "project.update":
		var args struct {
			ID          string    `json:"id"`
			Title       *string   `json:"title"`
			Description *string   `json:"description"`
			Labels      *[]string `json:"labels"`
		}
		if err := json.Unmarshal(call.Arguments, &args); err != nil {
			return nil, fmt.Errorf("invalid arguments: %w", err)
		}
		return h.projectUpdate(peerID, args.ID, projectPatch(args.Title, args.Description, args.Labels), ctx)
	case "project.link_task":
		var args struct {
			ID     string `json:"id"`
			TaskID string `json:"task_id"`
		}
		if err := json.Unmarshal(call.Arguments, &args); err != nil {
			return nil, fmt.Errorf("invalid arguments: %w", err)
		}
		return h.projectLinkTask(peerID, args.ID, args.TaskID, ctx)
	case "project.unlink_task":
		var args struct {
			ID     string `json:"id"`
			TaskID string `json:"task_id"`
		}
		if err := json.Unmarshal(call.Arguments, &args); err != nil {
			return nil, fmt.Errorf("invalid arguments: %w", err)
		}
		return h.projectUnlinkTask(peerID, args.ID, args.TaskID, ctx)
	case "project.archive":
		var args struct {
			ID        string `json:"id"`
			Unarchive bool   `json:"unarchive"`
		}
		if err := json.Unmarshal(call.Arguments, &args); err != nil {
			return nil, fmt.Errorf("invalid arguments: %w", err)
		}
		if args.Unarchive {
			return h.projectUnarchive(peerID, args.ID, ctx)
		}
		return h.projectArchive(peerID, args.ID, ctx)
	case "artifact.create":
		var args struct {
			Kind    string   `json:"kind"`
			Title   string   `json:"title"`
			Content string   `json:"content"`
			Labels  []string `json:"labels"`
		}
		if err := json.Unmarshal(call.Arguments, &args); err != nil {
			return nil, fmt.Errorf("invalid arguments: %w", err)
		}
		return h.artifactCreate(peerID, artifacts.Input{Kind: args.Kind, Title: args.Title, Content: args.Content, Labels: args.Labels}, ctx)
	case "artifact.get":
		var args struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(call.Arguments, &args); err != nil {
			return nil, fmt.Errorf("invalid arguments: %w", err)
		}
		return h.artifactGet(peerID, args.ID, ctx)
	case "artifact.list":
		var args struct {
			Kind  string `json:"kind"`
			Limit int    `json:"limit"`
		}
		if err := json.Unmarshal(call.Arguments, &args); err != nil {
			return nil, fmt.Errorf("invalid arguments: %w", err)
		}
		return h.artifactList(peerID, args.Kind, args.Limit, ctx)
	case "artifact.update":
		var args struct {
			ID      string    `json:"id"`
			Kind    *string   `json:"kind"`
			Title   *string   `json:"title"`
			Content *string   `json:"content"`
			Labels  *[]string `json:"labels"`
		}
		if err := json.Unmarshal(call.Arguments, &args); err != nil {
			return nil, fmt.Errorf("invalid arguments: %w", err)
		}
		return h.artifactUpdate(peerID, args.ID, artifacts.Patch{Kind: args.Kind, Title: args.Title, Content: args.Content, Labels: args.Labels}, ctx)
	case "decision.record":
		var args struct {
			Title        string `json:"title"`
			Decision     string `json:"decision"`
			Rationale    string `json:"rationale"`
			Alternatives string `json:"alternatives"`
			Owner        string `json:"owner"`
			Context      string `json:"context"`
		}
		if err := json.Unmarshal(call.Arguments, &args); err != nil {
			return nil, fmt.Errorf("invalid arguments: %w", err)
		}
		return h.decisionRecord(peerID, decisions.Input{
			Title: args.Title, Decision: args.Decision,
			Rationale: args.Rationale, Alternatives: args.Alternatives,
			Owner: args.Owner, Context: args.Context,
		}, ctx)
	case "decision.list":
		var args struct {
			Limit int `json:"limit"`
		}
		if err := json.Unmarshal(call.Arguments, &args); err != nil {
			return nil, fmt.Errorf("invalid arguments: %w", err)
		}
		return h.decisionList(peerID, args.Limit, ctx)
	case "decision.search":
		var args struct {
			Query string `json:"query"`
			Limit int    `json:"limit"`
		}
		if err := json.Unmarshal(call.Arguments, &args); err != nil {
			return nil, fmt.Errorf("invalid arguments: %w", err)
		}
		return h.decisionSearch(peerID, args.Query, args.Limit, ctx)
	case "preference.set":
		var args struct {
			Key             string  `json:"key"`
			Value           string  `json:"value"`
			Scope           string  `json:"scope"`
			Provenance      string  `json:"provenance"`
			Confidence      float64 `json:"confidence"`
			LastConfirmedAt int64   `json:"last_confirmed_at"`
		}
		if err := json.Unmarshal(call.Arguments, &args); err != nil {
			return nil, fmt.Errorf("invalid arguments: %w", err)
		}
		return h.preferenceSet(peerID, preferences.Input{
			Key: args.Key, Value: args.Value, Scope: args.Scope,
			Provenance: args.Provenance, Confidence: args.Confidence,
			LastConfirmedAt: args.LastConfirmedAt,
		}, ctx)
	case "preference.get":
		var args struct {
			Key   string `json:"key"`
			Scope string `json:"scope"`
		}
		if err := json.Unmarshal(call.Arguments, &args); err != nil {
			return nil, fmt.Errorf("invalid arguments: %w", err)
		}
		return h.preferenceGet(peerID, args.Key, args.Scope, ctx)
	case "preference.list":
		var args struct {
			Scope string `json:"scope"`
			Limit int    `json:"limit"`
		}
		if err := json.Unmarshal(call.Arguments, &args); err != nil {
			return nil, fmt.Errorf("invalid arguments: %w", err)
		}
		return h.preferenceList(peerID, args.Scope, args.Limit, ctx)
	case "tool_result.get":
		var args struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(call.Arguments, &args); err != nil {
			return nil, fmt.Errorf("invalid arguments: %w", err)
		}
		if h.toolResultStore == nil {
			return nil, fmt.Errorf("tool result store is not available")
		}
		return h.toolResultStore.Get(ctx, peerID, args.ID)
	case "tool_result.list":
		var args struct {
			SessionKey string `json:"session_key"`
			ToolName   string `json:"tool_name"`
			IsError    *bool  `json:"is_error"`
			Limit      int    `json:"limit"`
		}
		if err := json.Unmarshal(call.Arguments, &args); err != nil {
			return nil, fmt.Errorf("invalid arguments: %w", err)
		}
		if h.toolResultStore == nil {
			return nil, fmt.Errorf("tool result store is not available")
		}
		return h.toolResultStore.List(ctx, peerID, toolresults.Filter{
			SessionKey: args.SessionKey,
			ToolName:   args.ToolName,
			IsError:    args.IsError,
			Limit:      args.Limit,
		})
	default:
		return nil, errUnhandledTool
	}
}
