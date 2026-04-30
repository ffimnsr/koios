package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/ffimnsr/koios/internal/agent"
	"github.com/ffimnsr/koios/internal/types"
)

func (h *Handler) executeRuntimeTool(ctx context.Context, peerID string, call agent.ToolCall) (any, error) {
	switch call.Name {
	case "message", "message.send":
		var args messageToolParams
		if err := json.Unmarshal(call.Arguments, &args); err != nil {
			return nil, fmt.Errorf("invalid arguments: %w", err)
		}
		return h.runMessageTool(ctx, peerID, args)
	case "inbox.list":
		var args struct {
			Channel    string `json:"channel"`
			Limit      int    `json:"limit"`
			UnreadOnly bool   `json:"unread_only"`
		}
		if err := json.Unmarshal(call.Arguments, &args); err != nil {
			return nil, fmt.Errorf("invalid arguments: %w", err)
		}
		return h.runInboxListTool(peerID, args.Channel, args.Limit, args.UnreadOnly)
	case "inbox.read":
		var args struct {
			SessionKey string `json:"session_key"`
			Limit      int    `json:"limit"`
			UnreadOnly bool   `json:"unread_only"`
		}
		if err := json.Unmarshal(call.Arguments, &args); err != nil {
			return nil, fmt.Errorf("invalid arguments: %w", err)
		}
		return h.runInboxReadTool(peerID, args.SessionKey, args.Limit, args.UnreadOnly)
	case "inbox.mark_read":
		var args struct {
			SessionKey string `json:"session_key"`
		}
		if err := json.Unmarshal(call.Arguments, &args); err != nil {
			return nil, fmt.Errorf("invalid arguments: %w", err)
		}
		return h.runInboxMarkReadTool(peerID, args.SessionKey)
	case "inbox.route":
		var args struct {
			Channel    string `json:"channel"`
			SubjectID  string `json:"subject_id"`
			PeerID     string `json:"peer_id"`
			SessionKey string `json:"session_key"`
		}
		if err := json.Unmarshal(call.Arguments, &args); err != nil {
			return nil, fmt.Errorf("invalid arguments: %w", err)
		}
		return h.runInboxRouteTool(peerID, args.Channel, args.SubjectID, args.PeerID, args.SessionKey)
	case "inbox.summarize":
		var args struct {
			SessionKey string `json:"session_key"`
			Limit      int    `json:"limit"`
		}
		if err := json.Unmarshal(call.Arguments, &args); err != nil {
			return nil, fmt.Errorf("invalid arguments: %w", err)
		}
		return h.runInboxSummarizeTool(peerID, args.SessionKey, args.Limit)
	case "system.notify":
		var args struct {
			Title   string `json:"title"`
			Message string `json:"message"`
		}
		if err := json.Unmarshal(call.Arguments, &args); err != nil {
			return nil, fmt.Errorf("invalid arguments: %w", err)
		}
		return h.runSystemNotifyTool(ctx, args.Title, args.Message)
	case "notification.send":
		var args struct {
			Title   string `json:"title"`
			Message string `json:"message"`
			Kind    string `json:"kind"`
			Urgency string `json:"urgency"`
		}
		if err := json.Unmarshal(call.Arguments, &args); err != nil {
			return nil, fmt.Errorf("invalid arguments: %w", err)
		}
		return h.runNotificationSendTool(ctx, args.Title, args.Message, args.Kind, args.Urgency)
	case "approval.request":
		var args struct {
			Kind     string         `json:"kind"`
			Action   string         `json:"action"`
			Summary  string         `json:"summary"`
			Reason   string         `json:"reason"`
			Metadata map[string]any `json:"metadata"`
		}
		if err := json.Unmarshal(call.Arguments, &args); err != nil {
			return nil, fmt.Errorf("invalid arguments: %w", err)
		}
		action := strings.TrimSpace(args.Action)
		if action == "" {
			return nil, fmt.Errorf("action is required")
		}
		summary := strings.TrimSpace(args.Summary)
		if summary == "" {
			summary = action
		}
		return h.requestApproval(peerID, pendingApproval{
			Kind:     strings.TrimSpace(args.Kind),
			Action:   action,
			Summary:  summary,
			Reason:   args.Reason,
			Metadata: args.Metadata,
		}, nil), nil
	case "system.run":
		var args execParams
		if err := json.Unmarshal(call.Arguments, &args); err != nil {
			return nil, fmt.Errorf("invalid arguments: %w", err)
		}
		return h.runExecTool(ctx, peerID, args)
	case "write", "workspace.write":
		var args struct {
			Path    string `json:"path"`
			Content string `json:"content"`
			Append  bool   `json:"append"`
		}
		if err := json.Unmarshal(call.Arguments, &args); err != nil {
			return nil, fmt.Errorf("invalid arguments: %w", err)
		}
		return h.workspaceWrite(peerID, args.Path, args.Content, args.Append)
	case "edit", "workspace.edit":
		var args struct {
			Path       string `json:"path"`
			OldText    string `json:"old_text"`
			NewText    string `json:"new_text"`
			ReplaceAll bool   `json:"replace_all"`
		}
		if err := json.Unmarshal(call.Arguments, &args); err != nil {
			return nil, fmt.Errorf("invalid arguments: %w", err)
		}
		return h.workspaceEdit(peerID, args.Path, args.OldText, args.NewText, args.ReplaceAll)
	case "apply_patch":
		var args struct {
			Patch string `json:"patch"`
		}
		if err := json.Unmarshal(call.Arguments, &args); err != nil {
			return nil, fmt.Errorf("invalid arguments: %w", err)
		}
		return h.workspaceApplyPatch(peerID, args.Patch)
	case "git.status":
		var args struct {
			RepoPath string `json:"repo_path"`
		}
		if err := json.Unmarshal(call.Arguments, &args); err != nil {
			return nil, fmt.Errorf("invalid arguments: %w", err)
		}
		return h.runGitStatusTool(ctx, peerID, args.RepoPath)
	case "git.diff":
		var args struct {
			RepoPath string `json:"repo_path"`
			Path     string `json:"path"`
			Base     string `json:"base"`
			Head     string `json:"head"`
			Staged   bool   `json:"staged"`
			Context  int    `json:"context"`
		}
		if err := json.Unmarshal(call.Arguments, &args); err != nil {
			return nil, fmt.Errorf("invalid arguments: %w", err)
		}
		return h.runGitDiffTool(ctx, peerID, args.RepoPath, args.Path, args.Base, args.Head, args.Staged, args.Context)
	case "git.log":
		var args struct {
			RepoPath string `json:"repo_path"`
			Ref      string `json:"ref"`
			Path     string `json:"path"`
			Limit    int    `json:"limit"`
		}
		if err := json.Unmarshal(call.Arguments, &args); err != nil {
			return nil, fmt.Errorf("invalid arguments: %w", err)
		}
		return h.runGitLogTool(ctx, peerID, args.RepoPath, args.Ref, args.Path, args.Limit)
	case "git.branch":
		var args struct {
			RepoPath   string `json:"repo_path"`
			Action     string `json:"action"`
			Name       string `json:"name"`
			Target     string `json:"target"`
			StartPoint string `json:"start_point"`
			All        bool   `json:"all"`
			Force      bool   `json:"force"`
		}
		if err := json.Unmarshal(call.Arguments, &args); err != nil {
			return nil, fmt.Errorf("invalid arguments: %w", err)
		}
		return h.runGitBranchTool(ctx, peerID, args.RepoPath, args.Action, args.Name, args.Target, args.StartPoint, args.All, args.Force)
	case "git.commit":
		var args struct {
			RepoPath string `json:"repo_path"`
			Message  string `json:"message"`
			All      bool   `json:"all"`
		}
		if err := json.Unmarshal(call.Arguments, &args); err != nil {
			return nil, fmt.Errorf("invalid arguments: %w", err)
		}
		return h.runGitCommitTool(ctx, peerID, args.RepoPath, args.Message, args.All)
	case "git.apply_patch":
		var args struct {
			RepoPath  string `json:"repo_path"`
			Patch     string `json:"patch"`
			CheckOnly bool   `json:"check_only"`
			Index     bool   `json:"index"`
		}
		if err := json.Unmarshal(call.Arguments, &args); err != nil {
			return nil, fmt.Errorf("invalid arguments: %w", err)
		}
		return h.runGitApplyPatchTool(ctx, peerID, args.RepoPath, args.Patch, args.CheckOnly, args.Index)
	case "workspace.mkdir":
		var args struct {
			Path string `json:"path"`
		}
		if err := json.Unmarshal(call.Arguments, &args); err != nil {
			return nil, fmt.Errorf("invalid arguments: %w", err)
		}
		return h.workspaceMkdir(peerID, args.Path)
	case "workspace.delete":
		var args struct {
			Path      string `json:"path"`
			Recursive bool   `json:"recursive"`
		}
		if err := json.Unmarshal(call.Arguments, &args); err != nil {
			return nil, fmt.Errorf("invalid arguments: %w", err)
		}
		return h.workspaceDelete(peerID, args.Path, args.Recursive)
	case "exec":
		var args execParams
		if err := json.Unmarshal(call.Arguments, &args); err != nil {
			return nil, fmt.Errorf("invalid arguments: %w", err)
		}
		return h.runExecTool(ctx, peerID, args)
	case "code_execution":
		var args codeExecutionParams
		if err := json.Unmarshal(call.Arguments, &args); err != nil {
			return nil, fmt.Errorf("invalid arguments: %w", err)
		}
		return h.runCodeExecutionTool(ctx, peerID, args)
	case "web_search":
		var args webSearchParams
		if err := json.Unmarshal(call.Arguments, &args); err != nil {
			return nil, fmt.Errorf("invalid arguments: %w", err)
		}
		return h.runWebSearchTool(ctx, args)
	case "web_fetch":
		var args webFetchParams
		if err := json.Unmarshal(call.Arguments, &args); err != nil {
			return nil, fmt.Errorf("invalid arguments: %w", err)
		}
		return h.runWebFetchTool(ctx, args)
	case "cron.list":
		if h.jobStore == nil || h.sched == nil {
			return nil, fmt.Errorf("cron is not enabled")
		}
		return h.jobStore.List(peerID), nil
	case "cron.get":
		var args struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(call.Arguments, &args); err != nil {
			return nil, fmt.Errorf("invalid arguments: %w", err)
		}
		return h.ownedJobForPeer(peerID, args.ID)
	case "cron.delete":
		var args struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(call.Arguments, &args); err != nil {
			return nil, fmt.Errorf("invalid arguments: %w", err)
		}
		if _, err := h.ownedJobForPeer(peerID, args.ID); err != nil {
			return nil, err
		}
		if err := h.jobStore.Remove(args.ID); err != nil {
			return nil, err
		}
		return map[string]bool{"ok": true}, nil
	case "cron.trigger":
		var args struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(call.Arguments, &args); err != nil {
			return nil, fmt.Errorf("invalid arguments: %w", err)
		}
		if _, err := h.ownedJobForPeer(peerID, args.ID); err != nil {
			return nil, err
		}
		runID, err := h.sched.TriggerRun(args.ID)
		if err != nil {
			return nil, err
		}
		return map[string]any{"ok": true, "run_id": runID}, nil
	case "cron.runs":
		var args struct {
			ID    string `json:"id"`
			Limit int    `json:"limit"`
		}
		if err := json.Unmarshal(call.Arguments, &args); err != nil {
			return nil, fmt.Errorf("invalid arguments: %w", err)
		}
		if _, err := h.ownedJobForPeer(peerID, args.ID); err != nil {
			return nil, err
		}
		if args.Limit <= 0 {
			args.Limit = 50
		}
		return h.jobStore.ReadRunRecords(args.ID, args.Limit)
	case "cron.create":
		var args cronCreateParams
		if err := json.Unmarshal(call.Arguments, &args); err != nil {
			return nil, fmt.Errorf("invalid arguments: %w", err)
		}
		return h.createCronJob(peerID, args)
	case "cron.update":
		var args cronUpdateParams
		if err := json.Unmarshal(call.Arguments, &args); err != nil {
			return nil, fmt.Errorf("invalid arguments: %w", err)
		}
		return h.updateCronJob(peerID, args)
	case "workflow.list":
		return h.workflowList(peerID)
	case "workflow.create":
		var args workflowCreateParams
		if err := json.Unmarshal(call.Arguments, &args); err != nil {
			return nil, fmt.Errorf("invalid arguments: %w", err)
		}
		return h.workflowCreate(peerID, args)
	case "workflow.get":
		var args struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(call.Arguments, &args); err != nil {
			return nil, fmt.Errorf("invalid arguments: %w", err)
		}
		return h.workflowGet(peerID, args.ID)
	case "workflow.delete":
		var args struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(call.Arguments, &args); err != nil {
			return nil, fmt.Errorf("invalid arguments: %w", err)
		}
		return h.workflowDelete(peerID, args.ID)
	case "workflow.run":
		var args struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(call.Arguments, &args); err != nil {
			return nil, fmt.Errorf("invalid arguments: %w", err)
		}
		return h.workflowStartRun(ctx, peerID, args.ID)
	case "workflow.status":
		var args struct {
			RunID string `json:"run_id"`
		}
		if err := json.Unmarshal(call.Arguments, &args); err != nil {
			return nil, fmt.Errorf("invalid arguments: %w", err)
		}
		return h.workflowStatus(peerID, args.RunID)
	case "workflow.cancel":
		var args struct {
			RunID string `json:"run_id"`
		}
		if err := json.Unmarshal(call.Arguments, &args); err != nil {
			return nil, fmt.Errorf("invalid arguments: %w", err)
		}
		return h.workflowCancel(peerID, args.RunID)
	case "workflow.runs":
		var args struct {
			ID    string `json:"id"`
			Limit int    `json:"limit"`
		}
		if err := json.Unmarshal(call.Arguments, &args); err != nil {
			return nil, fmt.Errorf("invalid arguments: %w", err)
		}
		return h.workflowRuns(peerID, args.ID, args.Limit)
	case "orchestrator.start":
		return h.orchestratorStart(ctx, peerID, call.Arguments)
	case "orchestrator.status":
		var args struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(call.Arguments, &args); err != nil {
			return nil, fmt.Errorf("invalid arguments: %w", err)
		}
		return h.orchestratorStatus(peerID, args.ID)
	case "orchestrator.wait":
		var args struct {
			ID                 string `json:"id"`
			WaitTimeoutSeconds int    `json:"wait_timeout_seconds"`
		}
		if err := json.Unmarshal(call.Arguments, &args); err != nil {
			return nil, fmt.Errorf("invalid arguments: %w", err)
		}
		return h.orchestratorWait(ctx, peerID, args.ID, args.WaitTimeoutSeconds)
	case "orchestrator.cancel":
		var args struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(call.Arguments, &args); err != nil {
			return nil, fmt.Errorf("invalid arguments: %w", err)
		}
		return h.orchestratorCancel(peerID, args.ID)
	case "orchestrator.barrier":
		var args struct {
			ID                 string `json:"id"`
			Group              string `json:"group"`
			WaitTimeoutSeconds int    `json:"wait_timeout_seconds"`
		}
		if err := json.Unmarshal(call.Arguments, &args); err != nil {
			return nil, fmt.Errorf("invalid arguments: %w", err)
		}
		return h.orchestratorBarrier(ctx, peerID, args.ID, args.Group, args.WaitTimeoutSeconds)
	case "orchestrator.runs":
		return h.orchestratorRuns(peerID)
	case "code_execution.status":
		var args codeExecutionRunParams
		if err := json.Unmarshal(call.Arguments, &args); err != nil {
			return nil, fmt.Errorf("invalid arguments: %w", err)
		}
		return h.codeExecutionStatus(peerID, args.ID)
	case "code_execution.cancel":
		var args codeExecutionRunParams
		if err := json.Unmarshal(call.Arguments, &args); err != nil {
			return nil, fmt.Errorf("invalid arguments: %w", err)
		}
		return h.codeExecutionCancel(peerID, args.ID)
	case "process.start":
		var args backgroundProcessStartParams
		if err := json.Unmarshal(call.Arguments, &args); err != nil {
			return nil, fmt.Errorf("invalid arguments: %w", err)
		}
		return h.startBackgroundProcess(ctx, peerID, args)
	case "process.status":
		var args backgroundProcessRunParams
		if err := json.Unmarshal(call.Arguments, &args); err != nil {
			return nil, fmt.Errorf("invalid arguments: %w", err)
		}
		return h.backgroundProcessStatus(peerID, args.ID)
	case "process.stop":
		var args backgroundProcessRunParams
		if err := json.Unmarshal(call.Arguments, &args); err != nil {
			return nil, fmt.Errorf("invalid arguments: %w", err)
		}
		return h.stopBackgroundProcess(peerID, args.ID)
	case "process.list":
		var args struct {
			Limit int `json:"limit"`
		}
		if len(call.Arguments) > 0 {
			if err := json.Unmarshal(call.Arguments, &args); err != nil {
				return nil, fmt.Errorf("invalid arguments: %w", err)
			}
		}
		return h.listBackgroundProcesses(peerID, args.Limit)
	case "process.logs":
		var args backgroundProcessLogsParams
		if err := json.Unmarshal(call.Arguments, &args); err != nil {
			return nil, fmt.Errorf("invalid arguments: %w", err)
		}
		return h.backgroundProcessLogs(peerID, args.ID, args.MaxBytes)
	case "run.list":
		var args struct {
			Kind   string `json:"kind"`
			Status string `json:"status"`
			Limit  int    `json:"limit"`
		}
		if len(call.Arguments) > 0 {
			if err := json.Unmarshal(call.Arguments, &args); err != nil {
				return nil, fmt.Errorf("invalid arguments: %w", err)
			}
		}
		return h.listRuns(peerID, args.Kind, args.Status, args.Limit)
	case "run.status":
		var args struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(call.Arguments, &args); err != nil {
			return nil, fmt.Errorf("invalid arguments: %w", err)
		}
		return h.runStatus(peerID, args.ID)
	case "run.cancel":
		var args struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(call.Arguments, &args); err != nil {
			return nil, fmt.Errorf("invalid arguments: %w", err)
		}
		return h.cancelRun(peerID, args.ID)
	case "run.logs":
		var args struct {
			ID          string `json:"id"`
			MaxBytes    int    `json:"max_bytes"`
			MaxMessages int    `json:"max_messages"`
		}
		if err := json.Unmarshal(call.Arguments, &args); err != nil {
			return nil, fmt.Errorf("invalid arguments: %w", err)
		}
		return h.runLogs(peerID, args.ID, args.MaxBytes, args.MaxMessages)
	case "usage.current":
		var args struct {
			PeerID string `json:"peer_id"`
		}
		if len(call.Arguments) > 0 {
			if err := json.Unmarshal(call.Arguments, &args); err != nil {
				return nil, fmt.Errorf("invalid arguments: %w", err)
			}
		}
		if strings.TrimSpace(args.PeerID) == "" {
			args.PeerID = peerID
		}
		return h.usageCurrent(strings.TrimSpace(args.PeerID)), nil
	case "usage.history":
		var args struct {
			Limit int `json:"limit"`
		}
		if len(call.Arguments) > 0 {
			if err := json.Unmarshal(call.Arguments, &args); err != nil {
				return nil, fmt.Errorf("invalid arguments: %w", err)
			}
		}
		return h.usageHistory(args.Limit), nil
	case "usage.estimate":
		var args struct {
			Text                     string          `json:"text"`
			Messages                 []types.Message `json:"messages"`
			SessionKey               string          `json:"session_key"`
			Model                    string          `json:"model"`
			IncludeHistory           *bool           `json:"include_history"`
			IncludeTools             *bool           `json:"include_tools"`
			ExpectedCompletionTokens int             `json:"expected_completion_tokens"`
		}
		if len(call.Arguments) > 0 {
			if err := json.Unmarshal(call.Arguments, &args); err != nil {
				return nil, fmt.Errorf("invalid arguments: %w", err)
			}
		}
		includeHistory := args.IncludeHistory != nil && *args.IncludeHistory
		includeTools := true
		if args.IncludeTools != nil {
			includeTools = *args.IncludeTools
		}
		return h.usageEstimate(ctx, peerID, args.Messages, args.Text, args.SessionKey, args.Model, includeHistory, includeTools, args.ExpectedCompletionTokens)
	case "model.list":
		return h.listModels(), nil
	case "model.capabilities":
		var args struct {
			Model      string `json:"model"`
			SessionKey string `json:"session_key"`
		}
		if len(call.Arguments) > 0 {
			if err := json.Unmarshal(call.Arguments, &args); err != nil {
				return nil, fmt.Errorf("invalid arguments: %w", err)
			}
		}
		if strings.TrimSpace(args.SessionKey) == "" {
			args.SessionKey = peerID
		}
		return h.modelCapabilities(peerID, args.Model, args.SessionKey)
	case "model.route":
		var args struct {
			Text       string `json:"text"`
			Model      string `json:"model"`
			SessionKey string `json:"session_key"`
		}
		if len(call.Arguments) > 0 {
			if err := json.Unmarshal(call.Arguments, &args); err != nil {
				return nil, fmt.Errorf("invalid arguments: %w", err)
			}
		}
		return h.routeModel(peerID, args.Text, args.Model, args.SessionKey)
	default:
		return nil, errUnhandledTool
	}
}
