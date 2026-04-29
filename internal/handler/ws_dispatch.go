package handler

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/ffimnsr/koios/internal/ops"
)

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
	case "approval.pending":
		h.rpcApprovalPending(ctx, wsc, req)
	case "approval.approve":
		h.rpcApprovalApprove(ctx, wsc, req)
	case "approval.reject":
		h.rpcApprovalReject(ctx, wsc, req)
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

func (h *Handler) serverCapabilities(peerID string) map[string]any {
	caps := map[string]bool{
		"agent_runtime": h.agentRuntime != nil && h.agentCoord != nil,
		"memory":        h.memStore != nil,
		"tasks":         h.taskStore != nil,
		"calendar":      h.calendarStore != nil,
		"briefing":      true,
		"cron":          h.jobStore != nil && h.sched != nil,
		"heartbeat":     h.hbRunner != nil && h.hbConfigStore != nil,
		"standing":      h.standingManager != nil,
		"subagents":     h.subRuntime != nil,
		"workspace":     h.workspaceStore != nil,
		"exec":          h.workspaceStore != nil,
		"presence":      h.presence != nil,
		"web":           true,
	}

	methods := []string{
		"ping",
		"server.capabilities",
		"chat",
		"brief.generate",
		"session.history",
		"session.reset",
	}
	if caps["presence"] {
		methods = append(methods, "presence.get", "presence.set")
	}
	if caps["standing"] {
		methods = append(methods, "standing.get", "standing.set", "standing.clear", "standing.profile.set", "standing.profile.delete", "standing.profile.activate")
	}
	if caps["agent_runtime"] {
		methods = append(methods,
			"agent.run",
			"agent.start",
			"agent.get",
			"agent.wait",
			"agent.cancel",
			"agent.steer",
		)
	}
	if caps["subagents"] {
		methods = append(methods,
			"subagent.list",
			"subagent.spawn",
			"subagent.get",
			"subagent.status",
			"subagent.kill",
			"subagent.steer",
			"subagent.transcript",
		)
	}
	if caps["memory"] {
		methods = append(methods,
			"memory.search",
			"memory.insert",
			"memory.get",
			"memory.list",
			"memory.delete",
			"memory.tag",
			"memory.preference.create",
			"memory.preference.get",
			"memory.preference.list",
			"memory.preference.update",
			"memory.preference.confirm",
			"memory.preference.delete",
			"memory.entity.create",
			"memory.entity.update",
			"memory.entity.get",
			"memory.entity.list",
			"memory.entity.search",
			"memory.entity.link_chunk",
			"memory.entity.relate",
			"memory.entity.touch",
			"memory.entity.delete",
			"memory.entity.unlink_chunk",
			"memory.entity.unrelate",
			"memory.candidate.create",
			"memory.candidate.list",
			"memory.candidate.edit",
			"memory.candidate.approve",
			"memory.candidate.merge",
			"memory.candidate.reject",
		)
	}
	if h.bookmarkStore != nil {
		methods = append(methods,
			"bookmark.create",
			"bookmark.capture_session",
			"bookmark.get",
			"bookmark.list",
			"bookmark.search",
			"bookmark.update",
			"bookmark.delete",
		)
	}
	if caps["tasks"] {
		methods = append(methods,
			"task.candidate.create",
			"task.candidate.extract",
			"task.candidate.list",
			"task.candidate.edit",
			"task.candidate.approve",
			"task.candidate.reject",
			"task.list",
			"task.get",
			"task.update",
			"task.assign",
			"task.snooze",
			"task.complete",
			"task.reopen",
			"waiting.create",
			"waiting.list",
			"waiting.get",
			"waiting.update",
			"waiting.snooze",
			"waiting.resolve",
			"waiting.reopen",
		)
	}
	if caps["calendar"] {
		methods = append(methods,
			"calendar.source.create",
			"calendar.source.list",
			"calendar.source.delete",
			"calendar.agenda",
		)
	}
	if caps["workspace"] {
		methods = append(methods, "workspace.list", "workspace.read", "workspace.head", "workspace.tail", "workspace.grep", "workspace.sort", "workspace.uniq", "workspace.diff", "workspace.write", "workspace.edit", "workspace.mkdir", "workspace.delete")
	}
	methods = append(methods, "approval.pending", "approval.approve", "approval.reject")
	if caps["exec"] {
		methods = append(methods, "exec", "exec.pending", "exec.approve", "exec.reject")
	}
	if caps["web"] {
		methods = append(methods, "web_search", "web_fetch")
	}
	if caps["cron"] {
		methods = append(methods,
			"cron.list",
			"cron.create",
			"cron.get",
			"cron.update",
			"cron.delete",
			"cron.trigger",
			"cron.runs",
		)
	}
	if caps["heartbeat"] {
		methods = append(methods, "heartbeat.get", "heartbeat.set", "heartbeat.wake")
	}
	methods = append(methods, "usage.get", "usage.list", "usage.totals", "server.set_log_level")
	if h.runLedger != nil {
		methods = append(methods, "runs.list", "runs.get")
	}

	tools := make([]string, 0, len(h.ToolDefinitions(peerID)))
	for _, tool := range h.ToolDefinitions(peerID) {
		if tool.Type == "function" && tool.Function.Name != "" {
			tools = append(tools, tool.Function.Name)
		}
	}

	streamNotifications := []string{}
	if caps["agent_runtime"] {
		streamNotifications = append(streamNotifications, "stream.delta", "stream.event")
	}
	if caps["presence"] {
		streamNotifications = append(streamNotifications, "presence.update")
	}

	return map[string]any{
		"peer_id":      peerID,
		"capabilities": caps,
		"methods":      methods,
		"chat_tools":   tools,
		"idempotency": map[string]any{
			"params_field": "idempotency_key",
			"methods":      idempotentRPCMethods(),
		},
		"stream_notifications": append(streamNotifications, "session.message"),
	}
}

type presenceGetParams struct {
	PeerID string `json:"peer_id,omitempty"`
}

func (h *Handler) rpcPresenceGet(wsc *wsConn, req *rpcRequest) {
	var p presenceGetParams
	if err := decodeParams(req.Params, &p); err != nil {
		wsc.replyErr(req.ID, errCodeInvalidParams, err.Error())
		return
	}
	if p.PeerID != "" {
		state, ok := h.presence.Get(p.PeerID)
		wsc.reply(req.ID, map[string]any{
			"peer_id": p.PeerID,
			"found":   ok,
			"state":   state,
		})
		return
	}
	wsc.reply(req.ID, map[string]any{"states": h.presence.Snapshot()})
}

type presenceSetParams struct {
	Status string `json:"status,omitempty"`
	Typing *bool  `json:"typing,omitempty"`
}

func (h *Handler) rpcPresenceSet(wsc *wsConn, req *rpcRequest) {
	var p presenceSetParams
	if err := decodeParams(req.Params, &p); err != nil {
		wsc.replyErr(req.ID, errCodeInvalidParams, err.Error())
		return
	}
	typing := false
	if p.Typing != nil {
		typing = *p.Typing
	}
	state := h.presence.Set(wsc.peerID, p.Status, typing, "rpc")
	wsc.reply(req.ID, map[string]any{"state": state})
}
