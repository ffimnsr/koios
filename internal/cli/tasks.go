package cli

import (
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/ffimnsr/koios/internal/tasks"
)

func newTasksCommand(ctx *commandContext) *cobra.Command {
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "tasks",
		Short: "Task inbox and durable task operations",
	}
	enableDerivedPeerDefault(cmd)
	cmd.PersistentFlags().BoolVar(&jsonOut, "json", false, "emit JSON output")
	cmd.AddCommand(newTasksListCommand(ctx, &jsonOut))
	cmd.AddCommand(newTasksGetCommand(ctx, &jsonOut))
	cmd.AddCommand(newTasksUpdateCommand(ctx, &jsonOut))
	cmd.AddCommand(newTasksAssignCommand(ctx, &jsonOut))
	cmd.AddCommand(newTasksSnoozeCommand(ctx, &jsonOut))
	cmd.AddCommand(newTasksCompleteCommand(ctx, &jsonOut))
	cmd.AddCommand(newTasksReopenCommand(ctx, &jsonOut))
	cmd.AddCommand(newTasksQueueCommand(ctx, &jsonOut))
	return cmd
}

func newTasksListCommand(ctx *commandContext, jsonOut *bool) *cobra.Command {
	var (
		peer    string
		status  string
		limit   int
		timeout time.Duration
	)
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List durable tasks",
		RunE: func(cmd *cobra.Command, args []string) error {
			if peer == "" {
				return fmt.Errorf("--peer is required")
			}
			state, err := ctx.state()
			if err != nil {
				return err
			}
			client := newGatewayClient(state, timeout)
			params := map[string]any{"limit": limit}
			if status != "" {
				params["status"] = status
			}
			var result map[string]any
			if err := client.rpc(cmd.Context(), peer, "task.list", params, &result); err != nil {
				return err
			}
			if *jsonOut {
				emit(cmd, true, result)
			} else {
				emit(cmd, false, formatTaskList(result))
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&peer, "peer", "", "peer ID")
	cmd.Flags().StringVar(&status, "status", string(tasks.TaskStatusOpen), "status filter: open, snoozed, completed, all")
	cmd.Flags().IntVar(&limit, "limit", 20, "max tasks to return")
	cmd.Flags().DurationVar(&timeout, "timeout", 10*time.Second, "request timeout")
	return cmd
}

func newTasksGetCommand(ctx *commandContext, jsonOut *bool) *cobra.Command {
	var (
		peer    string
		id      string
		timeout time.Duration
	)
	cmd := &cobra.Command{
		Use:   "get",
		Short: "Fetch one durable task",
		RunE: func(cmd *cobra.Command, args []string) error {
			if peer == "" {
				return fmt.Errorf("--peer is required")
			}
			if id == "" {
				return fmt.Errorf("--id is required")
			}
			state, err := ctx.state()
			if err != nil {
				return err
			}
			client := newGatewayClient(state, timeout)
			var result map[string]any
			if err := client.rpc(cmd.Context(), peer, "task.get", map[string]any{"id": id}, &result); err != nil {
				return err
			}
			emit(cmd, *jsonOut, result)
			return nil
		},
	}
	cmd.Flags().StringVar(&peer, "peer", "", "peer ID")
	cmd.Flags().StringVar(&id, "id", "", "task ID")
	cmd.Flags().DurationVar(&timeout, "timeout", 10*time.Second, "request timeout")
	return cmd
}

func newTasksUpdateCommand(ctx *commandContext, jsonOut *bool) *cobra.Command {
	var (
		peer        string
		id          string
		title       string
		details     string
		owner       string
		dueAt       int64
		snoozeUntil int64
		timeout     time.Duration
	)
	cmd := &cobra.Command{
		Use:   "update",
		Short: "Edit an existing durable task",
		RunE: func(cmd *cobra.Command, args []string) error {
			if peer == "" {
				return fmt.Errorf("--peer is required")
			}
			if id == "" {
				return fmt.Errorf("--id is required")
			}
			state, err := ctx.state()
			if err != nil {
				return err
			}
			client := newGatewayClient(state, timeout)
			params := map[string]any{"id": id}
			if cmd.Flags().Changed("title") {
				params["title"] = title
			}
			if cmd.Flags().Changed("details") {
				params["details"] = details
			}
			if cmd.Flags().Changed("owner") {
				params["owner"] = owner
			}
			if cmd.Flags().Changed("due-at") {
				params["due_at"] = dueAt
			}
			if cmd.Flags().Changed("snooze-until") {
				params["snooze_until"] = snoozeUntil
			}
			var result map[string]any
			if err := client.rpc(cmd.Context(), peer, "task.update", params, &result); err != nil {
				return err
			}
			emit(cmd, *jsonOut, result)
			return nil
		},
	}
	cmd.Flags().StringVar(&peer, "peer", "", "peer ID")
	cmd.Flags().StringVar(&id, "id", "", "task ID")
	cmd.Flags().StringVar(&title, "title", "", "task title")
	cmd.Flags().StringVar(&details, "details", "", "task details")
	cmd.Flags().StringVar(&owner, "owner", "", "task owner")
	cmd.Flags().Int64Var(&dueAt, "due-at", 0, "due timestamp as Unix seconds")
	cmd.Flags().Int64Var(&snoozeUntil, "snooze-until", 0, "snooze-until timestamp as Unix seconds")
	cmd.Flags().DurationVar(&timeout, "timeout", 10*time.Second, "request timeout")
	return cmd
}

func newTasksAssignCommand(ctx *commandContext, jsonOut *bool) *cobra.Command {
	var peer, id, owner string
	var timeout time.Duration
	cmd := &cobra.Command{
		Use:   "assign",
		Short: "Assign a task owner",
		RunE: func(cmd *cobra.Command, args []string) error {
			if id == "" || owner == "" {
				return fmt.Errorf("--id and --owner are required")
			}
			return runTaskRPC(ctx, cmd, *jsonOut, timeout, peer, "task.assign", map[string]any{"id": id, "owner": owner})
		},
	}
	cmd.Flags().StringVar(&peer, "peer", "", "peer ID")
	cmd.Flags().StringVar(&id, "id", "", "task ID")
	cmd.Flags().StringVar(&owner, "owner", "", "task owner")
	cmd.Flags().DurationVar(&timeout, "timeout", 10*time.Second, "request timeout")
	return cmd
}

func newTasksSnoozeCommand(ctx *commandContext, jsonOut *bool) *cobra.Command {
	var peer, id string
	var until int64
	var timeout time.Duration
	cmd := &cobra.Command{
		Use:   "snooze",
		Short: "Snooze a task until a Unix timestamp",
		RunE: func(cmd *cobra.Command, args []string) error {
			if id == "" || until <= 0 {
				return fmt.Errorf("--id and --until are required")
			}
			return runTaskRPC(ctx, cmd, *jsonOut, timeout, peer, "task.snooze", map[string]any{"id": id, "until": until})
		},
	}
	cmd.Flags().StringVar(&peer, "peer", "", "peer ID")
	cmd.Flags().StringVar(&id, "id", "", "task ID")
	cmd.Flags().Int64Var(&until, "until", 0, "snooze-until timestamp as Unix seconds")
	cmd.Flags().DurationVar(&timeout, "timeout", 10*time.Second, "request timeout")
	return cmd
}

func newTasksCompleteCommand(ctx *commandContext, jsonOut *bool) *cobra.Command {
	var peer, id string
	var timeout time.Duration
	cmd := &cobra.Command{
		Use:   "complete",
		Short: "Mark a task complete",
		RunE: func(cmd *cobra.Command, args []string) error {
			if id == "" {
				return fmt.Errorf("--id is required")
			}
			return runTaskRPC(ctx, cmd, *jsonOut, timeout, peer, "task.complete", map[string]any{"id": id})
		},
	}
	cmd.Flags().StringVar(&peer, "peer", "", "peer ID")
	cmd.Flags().StringVar(&id, "id", "", "task ID")
	cmd.Flags().DurationVar(&timeout, "timeout", 10*time.Second, "request timeout")
	return cmd
}

func newTasksReopenCommand(ctx *commandContext, jsonOut *bool) *cobra.Command {
	var peer, id string
	var timeout time.Duration
	cmd := &cobra.Command{
		Use:   "reopen",
		Short: "Reopen a completed or snoozed task",
		RunE: func(cmd *cobra.Command, args []string) error {
			if id == "" {
				return fmt.Errorf("--id is required")
			}
			return runTaskRPC(ctx, cmd, *jsonOut, timeout, peer, "task.reopen", map[string]any{"id": id})
		},
	}
	cmd.Flags().StringVar(&peer, "peer", "", "peer ID")
	cmd.Flags().StringVar(&id, "id", "", "task ID")
	cmd.Flags().DurationVar(&timeout, "timeout", 10*time.Second, "request timeout")
	return cmd
}

func newTasksQueueCommand(ctx *commandContext, jsonOut *bool) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "queue",
		Short: "Review queue for extracted task candidates",
	}
	cmd.AddCommand(newTasksQueueAddCommand(ctx, jsonOut))
	cmd.AddCommand(newTasksQueueExtractCommand(ctx, jsonOut))
	cmd.AddCommand(newTasksQueueListCommand(ctx, jsonOut))
	cmd.AddCommand(newTasksQueueEditCommand(ctx, jsonOut))
	cmd.AddCommand(newTasksQueueApproveCommand(ctx, jsonOut))
	cmd.AddCommand(newTasksQueueRejectCommand(ctx, jsonOut))
	return cmd
}

func newTasksQueueAddCommand(ctx *commandContext, jsonOut *bool) *cobra.Command {
	var peer, title, details, owner string
	var dueAt int64
	var timeout time.Duration
	cmd := &cobra.Command{
		Use:   "add",
		Short: "Queue a task candidate manually",
		RunE: func(cmd *cobra.Command, args []string) error {
			if strings.TrimSpace(title) == "" {
				return fmt.Errorf("--title is required")
			}
			return runTaskRPC(ctx, cmd, *jsonOut, timeout, peer, "task.candidate.create", map[string]any{"title": title, "details": details, "owner": owner, "due_at": dueAt})
		},
	}
	cmd.Flags().StringVar(&peer, "peer", "", "peer ID")
	cmd.Flags().StringVar(&title, "title", "", "candidate title")
	cmd.Flags().StringVar(&details, "details", "", "candidate details")
	cmd.Flags().StringVar(&owner, "owner", "", "candidate owner")
	cmd.Flags().Int64Var(&dueAt, "due-at", 0, "due timestamp as Unix seconds")
	cmd.Flags().DurationVar(&timeout, "timeout", 10*time.Second, "request timeout")
	return cmd
}

func newTasksQueueExtractCommand(ctx *commandContext, jsonOut *bool) *cobra.Command {
	var peer, text, captureKind, sourceSessionKey, sourceExcerpt string
	var timeout time.Duration
	cmd := &cobra.Command{
		Use:   "extract",
		Short: "Extract task candidates from freeform text",
		RunE: func(cmd *cobra.Command, args []string) error {
			if strings.TrimSpace(text) == "" {
				return fmt.Errorf("--text is required")
			}
			params := map[string]any{"text": text}
			if captureKind != "" {
				params["capture_kind"] = captureKind
			}
			if sourceSessionKey != "" {
				params["source_session_key"] = sourceSessionKey
			}
			if sourceExcerpt != "" {
				params["source_excerpt"] = sourceExcerpt
			}
			return runTaskRPC(ctx, cmd, *jsonOut, timeout, peer, "task.candidate.extract", params)
		},
	}
	cmd.Flags().StringVar(&peer, "peer", "", "peer ID")
	cmd.Flags().StringVar(&text, "text", "", "source text to extract from")
	cmd.Flags().StringVar(&captureKind, "capture-kind", "", "capture kind label")
	cmd.Flags().StringVar(&sourceSessionKey, "source-session-key", "", "source session key")
	cmd.Flags().StringVar(&sourceExcerpt, "source-excerpt", "", "source excerpt override")
	cmd.Flags().DurationVar(&timeout, "timeout", 10*time.Second, "request timeout")
	return cmd
}

func newTasksQueueListCommand(ctx *commandContext, jsonOut *bool) *cobra.Command {
	var peer, status string
	var limit int
	var timeout time.Duration
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List task candidates in the review inbox",
		RunE: func(cmd *cobra.Command, args []string) error {
			if peer == "" {
				return fmt.Errorf("--peer is required")
			}
			state, err := ctx.state()
			if err != nil {
				return err
			}
			client := newGatewayClient(state, timeout)
			var result map[string]any
			params := map[string]any{"limit": limit, "status": status}
			if err := client.rpc(cmd.Context(), peer, "task.candidate.list", params, &result); err != nil {
				return err
			}
			if *jsonOut {
				emit(cmd, true, result)
			} else {
				emit(cmd, false, formatTaskQueueList(result))
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&peer, "peer", "", "peer ID")
	cmd.Flags().StringVar(&status, "status", string(tasks.CandidateStatusPending), "status filter: pending, approved, rejected, all")
	cmd.Flags().IntVar(&limit, "limit", 20, "max candidates to return")
	cmd.Flags().DurationVar(&timeout, "timeout", 10*time.Second, "request timeout")
	return cmd
}

func newTasksQueueEditCommand(ctx *commandContext, jsonOut *bool) *cobra.Command {
	var peer, id, title, details, owner string
	var dueAt int64
	var timeout time.Duration
	cmd := &cobra.Command{
		Use:   "edit",
		Short: "Edit a pending task candidate",
		RunE: func(cmd *cobra.Command, args []string) error {
			if id == "" {
				return fmt.Errorf("--id is required")
			}
			state, err := ctx.state()
			if err != nil {
				return err
			}
			client := newGatewayClient(state, timeout)
			params := map[string]any{"id": id}
			if cmd.Flags().Changed("title") {
				params["title"] = title
			}
			if cmd.Flags().Changed("details") {
				params["details"] = details
			}
			if cmd.Flags().Changed("owner") {
				params["owner"] = owner
			}
			if cmd.Flags().Changed("due-at") {
				params["due_at"] = dueAt
			}
			var result map[string]any
			if err := client.rpc(cmd.Context(), peer, "task.candidate.edit", params, &result); err != nil {
				return err
			}
			emit(cmd, *jsonOut, result)
			return nil
		},
	}
	cmd.Flags().StringVar(&peer, "peer", "", "peer ID")
	cmd.Flags().StringVar(&id, "id", "", "candidate ID")
	cmd.Flags().StringVar(&title, "title", "", "candidate title")
	cmd.Flags().StringVar(&details, "details", "", "candidate details")
	cmd.Flags().StringVar(&owner, "owner", "", "candidate owner")
	cmd.Flags().Int64Var(&dueAt, "due-at", 0, "due timestamp as Unix seconds")
	cmd.Flags().DurationVar(&timeout, "timeout", 10*time.Second, "request timeout")
	return cmd
}

func newTasksQueueApproveCommand(ctx *commandContext, jsonOut *bool) *cobra.Command {
	var peer, id, reason, title, details, owner string
	var dueAt int64
	var timeout time.Duration
	cmd := &cobra.Command{
		Use:   "approve",
		Short: "Promote a task candidate into the durable task store",
		RunE: func(cmd *cobra.Command, args []string) error {
			if id == "" {
				return fmt.Errorf("--id is required")
			}
			state, err := ctx.state()
			if err != nil {
				return err
			}
			client := newGatewayClient(state, timeout)
			params := map[string]any{"id": id, "reason": reason}
			if cmd.Flags().Changed("title") {
				params["title"] = title
			}
			if cmd.Flags().Changed("details") {
				params["details"] = details
			}
			if cmd.Flags().Changed("owner") {
				params["owner"] = owner
			}
			if cmd.Flags().Changed("due-at") {
				params["due_at"] = dueAt
			}
			var result map[string]any
			if err := client.rpc(cmd.Context(), peer, "task.candidate.approve", params, &result); err != nil {
				return err
			}
			emit(cmd, *jsonOut, result)
			return nil
		},
	}
	cmd.Flags().StringVar(&peer, "peer", "", "peer ID")
	cmd.Flags().StringVar(&id, "id", "", "candidate ID")
	cmd.Flags().StringVar(&reason, "reason", "", "approval reason")
	cmd.Flags().StringVar(&title, "title", "", "task title override")
	cmd.Flags().StringVar(&details, "details", "", "task details override")
	cmd.Flags().StringVar(&owner, "owner", "", "task owner override")
	cmd.Flags().Int64Var(&dueAt, "due-at", 0, "due timestamp as Unix seconds")
	cmd.Flags().DurationVar(&timeout, "timeout", 10*time.Second, "request timeout")
	return cmd
}

func newTasksQueueRejectCommand(ctx *commandContext, jsonOut *bool) *cobra.Command {
	var peer, id, reason string
	var timeout time.Duration
	cmd := &cobra.Command{
		Use:   "reject",
		Short: "Reject a pending task candidate",
		RunE: func(cmd *cobra.Command, args []string) error {
			if id == "" || strings.TrimSpace(reason) == "" {
				return fmt.Errorf("--id and --reason are required")
			}
			return runTaskRPC(ctx, cmd, *jsonOut, timeout, peer, "task.candidate.reject", map[string]any{"id": id, "reason": reason})
		},
	}
	cmd.Flags().StringVar(&peer, "peer", "", "peer ID")
	cmd.Flags().StringVar(&id, "id", "", "candidate ID")
	cmd.Flags().StringVar(&reason, "reason", "", "rejection reason")
	cmd.Flags().DurationVar(&timeout, "timeout", 10*time.Second, "request timeout")
	return cmd
}

func runTaskRPC(ctx *commandContext, cmd *cobra.Command, jsonOut bool, timeout time.Duration, peer string, method string, params map[string]any) error {
	state, err := ctx.state()
	if err != nil {
		return err
	}
	client := newGatewayClient(state, timeout)
	var result map[string]any
	if err := client.rpc(cmd.Context(), peer, method, params, &result); err != nil {
		return err
	}
	emit(cmd, jsonOut, result)
	return nil
}

func formatTaskList(result map[string]any) string {
	tasksValue, _ := result["tasks"].([]any)
	status, _ := result["status"].(string)
	if len(tasksValue) == 0 {
		return fmt.Sprintf("No %s tasks.", status)
	}
	var lines []string
	lines = append(lines, fmt.Sprintf("Tasks (%s):", status))
	for _, raw := range tasksValue {
		task, _ := raw.(map[string]any)
		id, _ := task["id"].(string)
		title, _ := task["title"].(string)
		taskStatus, _ := task["status"].(string)
		owner, _ := task["owner"].(string)
		line := fmt.Sprintf("- %s [%s] %s", id, taskStatus, title)
		if owner != "" {
			line += fmt.Sprintf(" (owner: %s)", owner)
		}
		lines = append(lines, line)
	}
	return strings.Join(lines, "\n")
}

func formatTaskQueueList(result map[string]any) string {
	candidatesValue, _ := result["candidates"].([]any)
	status, _ := result["status"].(string)
	manualCount, _ := result["manual_count"].(float64)
	autoCount, _ := result["auto_generated_count"].(float64)
	if len(candidatesValue) == 0 {
		return fmt.Sprintf("No %s task candidates.", status)
	}
	var lines []string
	lines = append(lines, fmt.Sprintf("Task candidates (%s): %.0f auto-generated | %.0f manual", status, autoCount, manualCount))
	for _, raw := range candidatesValue {
		candidate, _ := raw.(map[string]any)
		id, _ := candidate["id"].(string)
		title, _ := candidate["title"].(string)
		candidateStatus, _ := candidate["status"].(string)
		lines = append(lines, fmt.Sprintf("- %s [%s] %s", id, candidateStatus, title))
		if origin := formatTaskQueueOrigin(candidate); origin != "" {
			lines = append(lines, "  source: "+origin)
		}
	}
	return strings.Join(lines, "\n")
}

func formatTaskQueueOrigin(candidate map[string]any) string {
	parts := make([]string, 0, 3)
	if captureKind, _ := candidate["capture_kind"].(string); captureKind != "" {
		parts = append(parts, captureKind)
	}
	if sourceSessionKey, _ := candidate["source_session_key"].(string); sourceSessionKey != "" {
		parts = append(parts, sourceSessionKey)
	}
	if sourceExcerpt, _ := candidate["source_excerpt"].(string); sourceExcerpt != "" {
		parts = append(parts, fmt.Sprintf("%q", sourceExcerpt))
	}
	return strings.Join(parts, " | ")
}
