package handler

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"time"

	"github.com/ffimnsr/koios/internal/ops"
)

// dispatch routes a parsed RPC request to its handler method.
func (h *Handler) dispatch(ctx context.Context, wsc *wsConn, req *rpcRequest) {
	if h.handleIdempotentRPC(ctx, wsc, req) {
		return
	}
	h.dispatchOnce(ctx, wsc, req)
}

func (h *Handler) handleIdempotentRPC(ctx context.Context, wsc *wsConn, req *rpcRequest) bool {
	if h == nil || h.idempotency == nil || !isIdempotentRPCMethod(req.Method) {
		return false
	}
	key, err := idempotencyKeyFromParams(req.Params)
	if err != nil {
		wsc.replyErr(req.ID, errCodeInvalidParams, err.Error())
		return true
	}
	if key == "" {
		return false
	}
	paramsHash, err := canonicalParamsHash(req.Params)
	if err != nil {
		wsc.replyErr(req.ID, errCodeInvalidParams, err.Error())
		return true
	}
	scope := wsc.peerID + "\x00" + req.Method + "\x00" + key
	reservation := h.idempotency.reserve(scope, paramsHash, time.Now().UTC())
	if reservation.conflict {
		wsc.replyErr(req.ID, errCodeInvalidParams, "idempotency_key cannot be reused with different params")
		return true
	}
	if reservation.wait {
		resp, err := h.idempotency.wait(ctx, reservation.record)
		if err != nil {
			wsc.replyErr(req.ID, errCodeServer, "idempotent wait failed: "+err.Error())
			return true
		}
		resp.ID = req.ID
		wsc.send(resp)
		return true
	}

	var (
		captureMu sync.Mutex
		captured  bool
		finalResp rpcResponse
	)
	capturedConn := *wsc
	capturedConn.onReply = func(resp rpcResponse) {
		captureMu.Lock()
		defer captureMu.Unlock()
		if captured {
			return
		}
		captured = true
		finalResp = resp
	}
	h.dispatchOnce(ctx, &capturedConn, req)

	captureMu.Lock()
	if !captured {
		finalResp = rpcResponse{
			ID:    req.ID,
			Error: &rpcError{Code: errCodeServer, Message: "internal error: missing rpc response"},
		}
	}
	captureMu.Unlock()
	h.idempotency.finish(reservation.record, finalResp)
	return true
}

func (h *Handler) dispatchOnce(ctx context.Context, wsc *wsConn, req *rpcRequest) {
	if h.hooks != nil {
		if err := h.hooks.Emit(ctx, ops.Event{
			Name:   ops.HookBeforeMessage,
			PeerID: wsc.peerID,
			Data: map[string]any{
				"method": req.Method,
				"has_id": len(req.ID) > 0,
			},
		}); err != nil {
			wsc.replyErr(req.ID, errCodeServer, "hook rejected request: "+err.Error())
			return
		}
		defer func() {
			_ = h.hooks.Emit(ctx, ops.Event{
				Name:   ops.HookAfterMessage,
				PeerID: wsc.peerID,
				Data: map[string]any{
					"method": req.Method,
					"has_id": len(req.ID) > 0,
				},
			})
		}()
	}
	switch req.Method {
	case "ping":
		wsc.reply(req.ID, map[string]bool{"pong": true})
	case "server.capabilities":
		wsc.reply(req.ID, h.serverCapabilities(wsc.peerID))

	// ── Chat ──────────────────────────────────────────────────────────────
	case "chat":
		h.rpcChat(ctx, wsc, req)
	case "brief.generate":
		h.rpcBriefGenerate(ctx, wsc, req)
	case "session.history":
		h.rpcSessionHistory(ctx, wsc, req)
	case "session.reset":
		h.rpcSessionReset(ctx, wsc, req)
	case "presence.get":
		if h.presence == nil {
			wsc.replyErr(req.ID, errCodeServer, "presence is not enabled")
			return
		}
		h.rpcPresenceGet(wsc, req)
	case "presence.set":
		if h.presence == nil {
			wsc.replyErr(req.ID, errCodeServer, "presence is not enabled")
			return
		}
		h.rpcPresenceSet(wsc, req)
	case "standing.get":
		if h.standingManager == nil {
			wsc.replyErr(req.ID, errCodeServer, "standing orders are not enabled")
			return
		}
		h.rpcStandingGet(ctx, wsc, req)
	case "standing.set":
		if h.standingManager == nil {
			wsc.replyErr(req.ID, errCodeServer, "standing orders are not enabled")
			return
		}
		h.rpcStandingSet(ctx, wsc, req)
	case "standing.clear":
		if h.standingManager == nil {
			wsc.replyErr(req.ID, errCodeServer, "standing orders are not enabled")
			return
		}
		h.rpcStandingClear(ctx, wsc, req)
	case "standing.profile.set":
		if h.standingManager == nil {
			wsc.replyErr(req.ID, errCodeServer, "standing orders are not enabled")
			return
		}
		h.rpcStandingProfileSet(ctx, wsc, req)
	case "standing.profile.delete":
		if h.standingManager == nil {
			wsc.replyErr(req.ID, errCodeServer, "standing orders are not enabled")
			return
		}
		h.rpcStandingProfileDelete(ctx, wsc, req)
	case "standing.profile.activate":
		if h.standingManager == nil {
			wsc.replyErr(req.ID, errCodeServer, "standing orders are not enabled")
			return
		}
		h.rpcStandingProfileActivate(ctx, wsc, req)

	// ── Agent run ─────────────────────────────────────────────────────────
	case "agent.run":
		h.rpcAgentRun(ctx, wsc, req)
	case "agent.start":
		h.rpcAgentStart(ctx, wsc, req)
	case "agent.get":
		h.rpcAgentGet(ctx, wsc, req)
	case "agent.wait":
		h.rpcAgentWait(ctx, wsc, req)
	case "agent.cancel":
		h.rpcAgentCancel(ctx, wsc, req)
	case "agent.steer":
		h.rpcAgentSteer(ctx, wsc, req)

	// ── Subagents ─────────────────────────────────────────────────────────
	case "subagent.list":
		h.rpcSubagentList(ctx, wsc, req)
	case "subagent.spawn":
		h.rpcSubagentSpawn(ctx, wsc, req)
	case "subagent.get":
		h.rpcSubagentGet(ctx, wsc, req)
	case "subagent.status":
		h.rpcSubagentGet(ctx, wsc, req)
	case "spawn.status":
		h.rpcSubagentGet(ctx, wsc, req)
	case "subagent.kill":
		h.rpcSubagentKill(ctx, wsc, req)
	case "subagent.steer":
		h.rpcSubagentSteer(ctx, wsc, req)
	case "subagent.transcript":
		h.rpcSubagentTranscript(ctx, wsc, req)

	// ── Memory ────────────────────────────────────────────────────────────
	case "memory.search":
		if h.memStore == nil {
			wsc.replyErr(req.ID, errCodeServer, "memory is not enabled")
			return
		}
		h.rpcMemorySearch(ctx, wsc, req)
	case "memory.insert":
		if h.memStore == nil {
			wsc.replyErr(req.ID, errCodeServer, "memory is not enabled")
			return
		}
		h.rpcMemoryInsert(ctx, wsc, req)
	case "memory.get":
		if h.memStore == nil {
			wsc.replyErr(req.ID, errCodeServer, "memory is not enabled")
			return
		}
		h.rpcMemoryGet(ctx, wsc, req)
	case "memory.list":
		if h.memStore == nil {
			wsc.replyErr(req.ID, errCodeServer, "memory is not enabled")
			return
		}
		h.rpcMemoryList(ctx, wsc, req)
	case "memory.delete":
		if h.memStore == nil {
			wsc.replyErr(req.ID, errCodeServer, "memory is not enabled")
			return
		}
		h.rpcMemoryDelete(ctx, wsc, req)
	case "memory.tag":
		if h.memStore == nil {
			wsc.replyErr(req.ID, errCodeServer, "memory is not enabled")
			return
		}
		h.rpcMemoryTag(ctx, wsc, req)
	case "memory.preference.create":
		if h.memStore == nil {
			wsc.replyErr(req.ID, errCodeServer, "memory is not enabled")
			return
		}
		h.rpcMemoryPreferenceCreate(ctx, wsc, req)
	case "memory.preference.get":
		if h.memStore == nil {
			wsc.replyErr(req.ID, errCodeServer, "memory is not enabled")
			return
		}
		h.rpcMemoryPreferenceGet(ctx, wsc, req)
	case "memory.preference.list":
		if h.memStore == nil {
			wsc.replyErr(req.ID, errCodeServer, "memory is not enabled")
			return
		}
		h.rpcMemoryPreferenceList(ctx, wsc, req)
	case "memory.preference.update":
		if h.memStore == nil {
			wsc.replyErr(req.ID, errCodeServer, "memory is not enabled")
			return
		}
		h.rpcMemoryPreferenceUpdate(ctx, wsc, req)
	case "memory.preference.confirm":
		if h.memStore == nil {
			wsc.replyErr(req.ID, errCodeServer, "memory is not enabled")
			return
		}
		h.rpcMemoryPreferenceConfirm(ctx, wsc, req)
	case "memory.preference.delete":
		if h.memStore == nil {
			wsc.replyErr(req.ID, errCodeServer, "memory is not enabled")
			return
		}
		h.rpcMemoryPreferenceDelete(ctx, wsc, req)
	case "memory.entity.create":
		if h.memStore == nil {
			wsc.replyErr(req.ID, errCodeServer, "memory is not enabled")
			return
		}
		h.rpcMemoryEntityCreate(ctx, wsc, req)
	case "memory.entity.update":
		if h.memStore == nil {
			wsc.replyErr(req.ID, errCodeServer, "memory is not enabled")
			return
		}
		h.rpcMemoryEntityUpdate(ctx, wsc, req)
	case "memory.entity.get":
		if h.memStore == nil {
			wsc.replyErr(req.ID, errCodeServer, "memory is not enabled")
			return
		}
		h.rpcMemoryEntityGet(ctx, wsc, req)
	case "memory.entity.list":
		if h.memStore == nil {
			wsc.replyErr(req.ID, errCodeServer, "memory is not enabled")
			return
		}
		h.rpcMemoryEntityList(ctx, wsc, req)
	case "memory.entity.search":
		if h.memStore == nil {
			wsc.replyErr(req.ID, errCodeServer, "memory is not enabled")
			return
		}
		h.rpcMemoryEntitySearch(ctx, wsc, req)
	case "memory.entity.link_chunk":
		if h.memStore == nil {
			wsc.replyErr(req.ID, errCodeServer, "memory is not enabled")
			return
		}
		h.rpcMemoryEntityLinkChunk(ctx, wsc, req)
	case "memory.entity.relate":
		if h.memStore == nil {
			wsc.replyErr(req.ID, errCodeServer, "memory is not enabled")
			return
		}
		h.rpcMemoryEntityRelate(ctx, wsc, req)
	case "memory.entity.touch":
		if h.memStore == nil {
			wsc.replyErr(req.ID, errCodeServer, "memory is not enabled")
			return
		}
		h.rpcMemoryEntityTouch(ctx, wsc, req)
	case "memory.entity.delete":
		if h.memStore == nil {
			wsc.replyErr(req.ID, errCodeServer, "memory is not enabled")
			return
		}
		h.rpcMemoryEntityDelete(ctx, wsc, req)
	case "memory.entity.unlink_chunk":
		if h.memStore == nil {
			wsc.replyErr(req.ID, errCodeServer, "memory is not enabled")
			return
		}
		h.rpcMemoryEntityUnlinkChunk(ctx, wsc, req)
	case "memory.entity.unrelate":
		if h.memStore == nil {
			wsc.replyErr(req.ID, errCodeServer, "memory is not enabled")
			return
		}
		h.rpcMemoryEntityUnrelate(ctx, wsc, req)
	case "memory.candidate.create":
		if h.memStore == nil {
			wsc.replyErr(req.ID, errCodeServer, "memory is not enabled")
			return
		}
		h.rpcMemoryCandidateCreate(ctx, wsc, req)
	case "memory.candidate.list":
		if h.memStore == nil {
			wsc.replyErr(req.ID, errCodeServer, "memory is not enabled")
			return
		}
		h.rpcMemoryCandidateList(ctx, wsc, req)
	case "memory.candidate.edit":
		if h.memStore == nil {
			wsc.replyErr(req.ID, errCodeServer, "memory is not enabled")
			return
		}
		h.rpcMemoryCandidateEdit(ctx, wsc, req)
	case "memory.candidate.approve":
		if h.memStore == nil {
			wsc.replyErr(req.ID, errCodeServer, "memory is not enabled")
			return
		}
		h.rpcMemoryCandidateApprove(ctx, wsc, req)
	case "memory.candidate.merge":
		if h.memStore == nil {
			wsc.replyErr(req.ID, errCodeServer, "memory is not enabled")
			return
		}
		h.rpcMemoryCandidateMerge(ctx, wsc, req)
	case "memory.candidate.reject":
		if h.memStore == nil {
			wsc.replyErr(req.ID, errCodeServer, "memory is not enabled")
			return
		}
		h.rpcMemoryCandidateReject(ctx, wsc, req)

	// ── Bookmarks ─────────────────────────────────────────────────────────
	case "bookmark.create":
		if h.bookmarkStore == nil {
			wsc.replyErr(req.ID, errCodeServer, "bookmarks are not enabled")
			return
		}
		h.rpcBookmarkCreate(ctx, wsc, req)
	case "bookmark.capture_session":
		if h.bookmarkStore == nil {
			wsc.replyErr(req.ID, errCodeServer, "bookmarks are not enabled")
			return
		}
		h.rpcBookmarkCaptureSession(ctx, wsc, req)
	case "bookmark.get":
		if h.bookmarkStore == nil {
			wsc.replyErr(req.ID, errCodeServer, "bookmarks are not enabled")
			return
		}
		h.rpcBookmarkGet(ctx, wsc, req)
	case "bookmark.list":
		if h.bookmarkStore == nil {
			wsc.replyErr(req.ID, errCodeServer, "bookmarks are not enabled")
			return
		}
		h.rpcBookmarkList(ctx, wsc, req)
	case "bookmark.search":
		if h.bookmarkStore == nil {
			wsc.replyErr(req.ID, errCodeServer, "bookmarks are not enabled")
			return
		}
		h.rpcBookmarkSearch(ctx, wsc, req)
	case "bookmark.update":
		if h.bookmarkStore == nil {
			wsc.replyErr(req.ID, errCodeServer, "bookmarks are not enabled")
			return
		}
		h.rpcBookmarkUpdate(ctx, wsc, req)
	case "bookmark.delete":
		if h.bookmarkStore == nil {
			wsc.replyErr(req.ID, errCodeServer, "bookmarks are not enabled")
			return
		}
		h.rpcBookmarkDelete(ctx, wsc, req)

	// ── Tasks ─────────────────────────────────────────────────────────────
	case "task.candidate.create":
		if h.taskStore == nil {
			wsc.replyErr(req.ID, errCodeServer, "tasks are not enabled")
			return
		}
		h.rpcTaskCandidateCreate(ctx, wsc, req)
	case "task.candidate.extract":
		if h.taskStore == nil {
			wsc.replyErr(req.ID, errCodeServer, "tasks are not enabled")
			return
		}
		h.rpcTaskCandidateExtract(ctx, wsc, req)
	case "task.candidate.list":
		if h.taskStore == nil {
			wsc.replyErr(req.ID, errCodeServer, "tasks are not enabled")
			return
		}
		h.rpcTaskCandidateList(ctx, wsc, req)
	case "task.candidate.edit":
		if h.taskStore == nil {
			wsc.replyErr(req.ID, errCodeServer, "tasks are not enabled")
			return
		}
		h.rpcTaskCandidateEdit(ctx, wsc, req)
	case "task.candidate.approve":
		if h.taskStore == nil {
			wsc.replyErr(req.ID, errCodeServer, "tasks are not enabled")
			return
		}
		h.rpcTaskCandidateApprove(ctx, wsc, req)
	case "task.candidate.reject":
		if h.taskStore == nil {
			wsc.replyErr(req.ID, errCodeServer, "tasks are not enabled")
			return
		}
		h.rpcTaskCandidateReject(ctx, wsc, req)
	case "task.list":
		if h.taskStore == nil {
			wsc.replyErr(req.ID, errCodeServer, "tasks are not enabled")
			return
		}
		h.rpcTaskList(ctx, wsc, req)
	case "task.get":
		if h.taskStore == nil {
			wsc.replyErr(req.ID, errCodeServer, "tasks are not enabled")
			return
		}
		h.rpcTaskGet(ctx, wsc, req)
	case "task.update":
		if h.taskStore == nil {
			wsc.replyErr(req.ID, errCodeServer, "tasks are not enabled")
			return
		}
		h.rpcTaskUpdate(ctx, wsc, req)
	case "task.assign":
		if h.taskStore == nil {
			wsc.replyErr(req.ID, errCodeServer, "tasks are not enabled")
			return
		}
		h.rpcTaskAssign(ctx, wsc, req)
	case "task.snooze":
		if h.taskStore == nil {
			wsc.replyErr(req.ID, errCodeServer, "tasks are not enabled")
			return
		}
		h.rpcTaskSnooze(ctx, wsc, req)
	case "task.complete":
		if h.taskStore == nil {
			wsc.replyErr(req.ID, errCodeServer, "tasks are not enabled")
			return
		}
		h.rpcTaskComplete(ctx, wsc, req)
	case "task.reopen":
		if h.taskStore == nil {
			wsc.replyErr(req.ID, errCodeServer, "tasks are not enabled")
			return
		}
		h.rpcTaskReopen(ctx, wsc, req)
	case "waiting.create":
		if h.taskStore == nil {
			wsc.replyErr(req.ID, errCodeServer, "tasks are not enabled")
			return
		}
		h.rpcWaitingCreate(ctx, wsc, req)
	case "waiting.list":
		if h.taskStore == nil {
			wsc.replyErr(req.ID, errCodeServer, "tasks are not enabled")
			return
		}
		h.rpcWaitingList(ctx, wsc, req)
	case "waiting.get":
		if h.taskStore == nil {
			wsc.replyErr(req.ID, errCodeServer, "tasks are not enabled")
			return
		}
		h.rpcWaitingGet(ctx, wsc, req)
	case "waiting.update":
		if h.taskStore == nil {
			wsc.replyErr(req.ID, errCodeServer, "tasks are not enabled")
			return
		}
		h.rpcWaitingUpdate(ctx, wsc, req)
	case "waiting.snooze":
		if h.taskStore == nil {
			wsc.replyErr(req.ID, errCodeServer, "tasks are not enabled")
			return
		}
		h.rpcWaitingSnooze(ctx, wsc, req)
	case "waiting.resolve":
		if h.taskStore == nil {
			wsc.replyErr(req.ID, errCodeServer, "tasks are not enabled")
			return
		}
		h.rpcWaitingResolve(ctx, wsc, req)
	case "waiting.reopen":
		if h.taskStore == nil {
			wsc.replyErr(req.ID, errCodeServer, "tasks are not enabled")
			return
		}
		h.rpcWaitingReopen(ctx, wsc, req)
	case "calendar.source.create":
		if h.calendarStore == nil {
			wsc.replyErr(req.ID, errCodeServer, "calendar is not enabled")
			return
		}
		h.rpcCalendarSourceCreate(ctx, wsc, req)
	case "calendar.source.list":
		if h.calendarStore == nil {
			wsc.replyErr(req.ID, errCodeServer, "calendar is not enabled")
			return
		}
		h.rpcCalendarSourceList(ctx, wsc, req)
	case "calendar.source.delete":
		if h.calendarStore == nil {
			wsc.replyErr(req.ID, errCodeServer, "calendar is not enabled")
			return
		}
		h.rpcCalendarSourceDelete(ctx, wsc, req)
	case "calendar.agenda":
		if h.calendarStore == nil {
			wsc.replyErr(req.ID, errCodeServer, "calendar is not enabled")
			return
		}
		h.rpcCalendarAgenda(ctx, wsc, req)

	// ── Workspace ─────────────────────────────────────────────────────────
	case "workspace.list":
		if h.workspaceStore == nil {
			wsc.replyErr(req.ID, errCodeServer, "workspace is not enabled")
			return
		}
		h.rpcWorkspaceList(ctx, wsc, req)
	case "workspace.read":
		if h.workspaceStore == nil {
			wsc.replyErr(req.ID, errCodeServer, "workspace is not enabled")
			return
		}
		h.rpcWorkspaceRead(ctx, wsc, req)
	case "workspace.head":
		if h.workspaceStore == nil {
			wsc.replyErr(req.ID, errCodeServer, "workspace is not enabled")
			return
		}
		h.rpcWorkspaceHead(ctx, wsc, req)
	case "workspace.tail":
		if h.workspaceStore == nil {
			wsc.replyErr(req.ID, errCodeServer, "workspace is not enabled")
			return
		}
		h.rpcWorkspaceTail(ctx, wsc, req)
	case "workspace.grep":
		if h.workspaceStore == nil {
			wsc.replyErr(req.ID, errCodeServer, "workspace is not enabled")
			return
		}
		h.rpcWorkspaceGrep(ctx, wsc, req)
	case "workspace.sort":
		if h.workspaceStore == nil {
			wsc.replyErr(req.ID, errCodeServer, "workspace is not enabled")
			return
		}
		h.rpcWorkspaceSort(ctx, wsc, req)
	case "workspace.uniq":
		if h.workspaceStore == nil {
			wsc.replyErr(req.ID, errCodeServer, "workspace is not enabled")
			return
		}
		h.rpcWorkspaceUniq(ctx, wsc, req)
	case "workspace.diff":
		if h.workspaceStore == nil {
			wsc.replyErr(req.ID, errCodeServer, "workspace is not enabled")
			return
		}
		h.rpcWorkspaceDiff(ctx, wsc, req)
	case "workspace.write":
		if h.workspaceStore == nil {
			wsc.replyErr(req.ID, errCodeServer, "workspace is not enabled")
			return
		}
		h.rpcWorkspaceWrite(ctx, wsc, req)
	case "workspace.edit":
		if h.workspaceStore == nil {
			wsc.replyErr(req.ID, errCodeServer, "workspace is not enabled")
			return
		}
		h.rpcWorkspaceEdit(ctx, wsc, req)
	case "workspace.mkdir":
		if h.workspaceStore == nil {
			wsc.replyErr(req.ID, errCodeServer, "workspace is not enabled")
			return
		}
		h.rpcWorkspaceMkdir(ctx, wsc, req)
	case "workspace.delete":
		if h.workspaceStore == nil {
			wsc.replyErr(req.ID, errCodeServer, "workspace is not enabled")
			return
		}
		h.rpcWorkspaceDelete(ctx, wsc, req)
	case "exec":
		if h.workspaceStore == nil {
			wsc.replyErr(req.ID, errCodeServer, "exec is not enabled")
			return
		}
		h.rpcExec(ctx, wsc, req)
	case "exec.pending":
		h.rpcExecPending(ctx, wsc, req)
	case "exec.approve":
		h.rpcExecApprove(ctx, wsc, req)
	case "exec.reject":
		h.rpcExecReject(ctx, wsc, req)
	case "web_search":
		h.rpcWebSearch(ctx, wsc, req)
	case "web_fetch":
		h.rpcWebFetch(ctx, wsc, req)

	// ── Cron ──────────────────────────────────────────────────────────────
	case "cron.list":
		if h.jobStore == nil {
			wsc.replyErr(req.ID, errCodeServer, "cron is not enabled")
			return
		}
		h.rpcCronList(ctx, wsc, req)
	case "cron.create":
		if h.jobStore == nil || h.sched == nil {
			wsc.replyErr(req.ID, errCodeServer, "cron is not enabled")
			return
		}
		h.rpcCronCreate(ctx, wsc, req)
	case "cron.get":
		if h.jobStore == nil {
			wsc.replyErr(req.ID, errCodeServer, "cron is not enabled")
			return
		}
		h.rpcCronGet(ctx, wsc, req)
	case "cron.update":
		if h.jobStore == nil || h.sched == nil {
			wsc.replyErr(req.ID, errCodeServer, "cron is not enabled")
			return
		}
		h.rpcCronUpdate(ctx, wsc, req)
	case "cron.delete":
		if h.jobStore == nil {
			wsc.replyErr(req.ID, errCodeServer, "cron is not enabled")
			return
		}
		h.rpcCronDelete(ctx, wsc, req)
	case "cron.trigger":
		if h.jobStore == nil || h.sched == nil {
			wsc.replyErr(req.ID, errCodeServer, "cron is not enabled")
			return
		}
		h.rpcCronTrigger(ctx, wsc, req)
	case "cron.runs":
		if h.jobStore == nil {
			wsc.replyErr(req.ID, errCodeServer, "cron is not enabled")
			return
		}
		h.rpcCronRuns(ctx, wsc, req)

	// ── Heartbeat ─────────────────────────────────────────────────────────
	case "heartbeat.get":
		if h.hbRunner == nil || h.hbConfigStore == nil {
			wsc.replyErr(req.ID, errCodeServer, "heartbeat is not enabled")
			return
		}
		h.rpcHeartbeatGet(ctx, wsc, req)
	case "heartbeat.set":
		if h.hbRunner == nil || h.hbConfigStore == nil {
			wsc.replyErr(req.ID, errCodeServer, "heartbeat is not enabled")
			return
		}
		h.rpcHeartbeatSet(ctx, wsc, req)
	case "heartbeat.wake":
		if h.hbRunner == nil {
			wsc.replyErr(req.ID, errCodeServer, "heartbeat is not enabled")
			return
		}
		h.rpcHeartbeatWake(ctx, wsc, req)

	// ── Usage ─────────────────────────────────────────────────────────────
	case "usage.get":
		h.rpcUsageGet(wsc, req)
	case "usage.list":
		h.rpcUsageList(wsc, req)
	case "usage.totals":
		h.rpcUsageTotals(wsc, req)

	// ── Runs ledger ───────────────────────────────────────────────────────
	case "runs.list":
		h.rpcRunsList(wsc, req)
	case "runs.get":
		h.rpcRunsGet(wsc, req)

	// ── Admin / hot-reload ────────────────────────────────────────────────
	case "server.set_log_level":
		h.rpcSetLogLevel(wsc, req)

	default:
		wsc.replyErr(req.ID, errCodeMethodNotFound, "unknown method: "+req.Method)
	}
}

func eventString(data map[string]any, key, fallback string) string {
	if data == nil {
		return fallback
	}
	value, ok := data[key]
	if !ok {
		return fallback
	}
	s, ok := value.(string)
	if !ok || strings.TrimSpace(s) == "" {
		return fallback
	}
	return s
}

func eventRawMessage(data map[string]any, key string) (json.RawMessage, bool) {
	if data == nil {
		return nil, false
	}
	value, ok := data[key]
	if !ok || value == nil {
		return nil, false
	}
	switch v := value.(type) {
	case json.RawMessage:
		return v, true
	case []byte:
		return json.RawMessage(v), true
	case string:
		return json.RawMessage(v), true
	default:
		buf, err := json.Marshal(v)
		if err != nil {
			return nil, false
		}
		return json.RawMessage(buf), true
	}
}
