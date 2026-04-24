package cli

import (
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/ffimnsr/koios/internal/tasks"
)

func newWaitingCommand(ctx *commandContext) *cobra.Command {
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "waiting",
		Short: "Waiting-on tracker for delegated asks and unanswered follow-ups",
	}
	cmd.PersistentFlags().BoolVar(&jsonOut, "json", false, "emit JSON output")
	cmd.AddCommand(newWaitingListCommand(ctx, &jsonOut))
	cmd.AddCommand(newWaitingGetCommand(ctx, &jsonOut))
	cmd.AddCommand(newWaitingCreateCommand(ctx, &jsonOut))
	cmd.AddCommand(newWaitingUpdateCommand(ctx, &jsonOut))
	cmd.AddCommand(newWaitingSnoozeCommand(ctx, &jsonOut))
	cmd.AddCommand(newWaitingResolveCommand(ctx, &jsonOut))
	cmd.AddCommand(newWaitingReopenCommand(ctx, &jsonOut))
	return cmd
}

func newWaitingListCommand(ctx *commandContext, jsonOut *bool) *cobra.Command {
	var peer, status string
	var limit int
	var timeout time.Duration
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List waiting-on records",
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
			if err := client.rpc(cmd.Context(), peer, "waiting.list", map[string]any{"limit": limit, "status": status}, &result); err != nil {
				return err
			}
			if *jsonOut {
				emit(cmd, true, result)
			} else {
				emit(cmd, false, formatWaitingList(result))
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&peer, "peer", "", "peer ID")
	cmd.Flags().StringVar(&status, "status", string(tasks.WaitingStatusOpen), "status filter: open, snoozed, resolved, all")
	cmd.Flags().IntVar(&limit, "limit", 20, "max waiting-on records to return")
	cmd.Flags().DurationVar(&timeout, "timeout", 10*time.Second, "request timeout")
	return cmd
}

func newWaitingGetCommand(ctx *commandContext, jsonOut *bool) *cobra.Command {
	var peer, id string
	var timeout time.Duration
	cmd := &cobra.Command{
		Use:   "get",
		Short: "Fetch one waiting-on record",
		RunE: func(cmd *cobra.Command, args []string) error {
			if peer == "" || id == "" {
				return fmt.Errorf("--peer and --id are required")
			}
			return runWaitingRPC(ctx, cmd, *jsonOut, timeout, peer, "waiting.get", map[string]any{"id": id})
		},
	}
	cmd.Flags().StringVar(&peer, "peer", "", "peer ID")
	cmd.Flags().StringVar(&id, "id", "", "waiting-on ID")
	cmd.Flags().DurationVar(&timeout, "timeout", 10*time.Second, "request timeout")
	return cmd
}

func newWaitingCreateCommand(ctx *commandContext, jsonOut *bool) *cobra.Command {
	var peer, title, details, waitingFor, sourceSessionKey, sourceExcerpt string
	var followUpAt, remindAt int64
	var timeout time.Duration
	cmd := &cobra.Command{
		Use:   "create",
		Short: "Create a waiting-on record",
		RunE: func(cmd *cobra.Command, args []string) error {
			if peer == "" || strings.TrimSpace(title) == "" || strings.TrimSpace(waitingFor) == "" {
				return fmt.Errorf("--peer, --title, and --waiting-for are required")
			}
			params := map[string]any{
				"title":       title,
				"details":     details,
				"waiting_for": waitingFor,
			}
			if followUpAt > 0 {
				params["follow_up_at"] = followUpAt
			}
			if remindAt > 0 {
				params["remind_at"] = remindAt
			}
			if sourceSessionKey != "" {
				params["source_session_key"] = sourceSessionKey
			}
			if sourceExcerpt != "" {
				params["source_excerpt"] = sourceExcerpt
			}
			return runWaitingRPC(ctx, cmd, *jsonOut, timeout, peer, "waiting.create", params)
		},
	}
	cmd.Flags().StringVar(&peer, "peer", "", "peer ID")
	cmd.Flags().StringVar(&title, "title", "", "waiting-on title")
	cmd.Flags().StringVar(&details, "details", "", "waiting-on details")
	cmd.Flags().StringVar(&waitingFor, "waiting-for", "", "person or team that owns the next action")
	cmd.Flags().Int64Var(&followUpAt, "follow-up-at", 0, "follow-up timestamp as Unix seconds")
	cmd.Flags().Int64Var(&remindAt, "remind-at", 0, "reminder timestamp as Unix seconds")
	cmd.Flags().StringVar(&sourceSessionKey, "source-session-key", "", "source session key")
	cmd.Flags().StringVar(&sourceExcerpt, "source-excerpt", "", "source excerpt")
	cmd.Flags().DurationVar(&timeout, "timeout", 10*time.Second, "request timeout")
	return cmd
}

func newWaitingUpdateCommand(ctx *commandContext, jsonOut *bool) *cobra.Command {
	var peer, id, title, details, waitingFor, sourceSessionKey, sourceExcerpt string
	var followUpAt, remindAt, snoozeUntil int64
	var timeout time.Duration
	cmd := &cobra.Command{
		Use:   "update",
		Short: "Update a waiting-on record",
		RunE: func(cmd *cobra.Command, args []string) error {
			if peer == "" || id == "" {
				return fmt.Errorf("--peer and --id are required")
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
			if cmd.Flags().Changed("waiting-for") {
				params["waiting_for"] = waitingFor
			}
			if cmd.Flags().Changed("follow-up-at") {
				params["follow_up_at"] = followUpAt
			}
			if cmd.Flags().Changed("remind-at") {
				params["remind_at"] = remindAt
			}
			if cmd.Flags().Changed("snooze-until") {
				params["snooze_until"] = snoozeUntil
			}
			if cmd.Flags().Changed("source-session-key") {
				params["source_session_key"] = sourceSessionKey
			}
			if cmd.Flags().Changed("source-excerpt") {
				params["source_excerpt"] = sourceExcerpt
			}
			var result map[string]any
			if err := client.rpc(cmd.Context(), peer, "waiting.update", params, &result); err != nil {
				return err
			}
			emit(cmd, *jsonOut, result)
			return nil
		},
	}
	cmd.Flags().StringVar(&peer, "peer", "", "peer ID")
	cmd.Flags().StringVar(&id, "id", "", "waiting-on ID")
	cmd.Flags().StringVar(&title, "title", "", "waiting-on title")
	cmd.Flags().StringVar(&details, "details", "", "waiting-on details")
	cmd.Flags().StringVar(&waitingFor, "waiting-for", "", "person or team that owns the next action")
	cmd.Flags().Int64Var(&followUpAt, "follow-up-at", 0, "follow-up timestamp as Unix seconds")
	cmd.Flags().Int64Var(&remindAt, "remind-at", 0, "reminder timestamp as Unix seconds")
	cmd.Flags().Int64Var(&snoozeUntil, "snooze-until", 0, "snooze-until timestamp as Unix seconds")
	cmd.Flags().StringVar(&sourceSessionKey, "source-session-key", "", "source session key")
	cmd.Flags().StringVar(&sourceExcerpt, "source-excerpt", "", "source excerpt")
	cmd.Flags().DurationVar(&timeout, "timeout", 10*time.Second, "request timeout")
	return cmd
}

func newWaitingSnoozeCommand(ctx *commandContext, jsonOut *bool) *cobra.Command {
	var peer, id string
	var until int64
	var timeout time.Duration
	cmd := &cobra.Command{
		Use:   "snooze",
		Short: "Snooze a waiting-on record until a Unix timestamp",
		RunE: func(cmd *cobra.Command, args []string) error {
			if peer == "" || id == "" || until <= 0 {
				return fmt.Errorf("--peer, --id, and --until are required")
			}
			return runWaitingRPC(ctx, cmd, *jsonOut, timeout, peer, "waiting.snooze", map[string]any{"id": id, "until": until})
		},
	}
	cmd.Flags().StringVar(&peer, "peer", "", "peer ID")
	cmd.Flags().StringVar(&id, "id", "", "waiting-on ID")
	cmd.Flags().Int64Var(&until, "until", 0, "snooze-until timestamp as Unix seconds")
	cmd.Flags().DurationVar(&timeout, "timeout", 10*time.Second, "request timeout")
	return cmd
}

func newWaitingResolveCommand(ctx *commandContext, jsonOut *bool) *cobra.Command {
	var peer, id string
	var timeout time.Duration
	cmd := &cobra.Command{
		Use:   "resolve",
		Short: "Resolve a waiting-on record",
		RunE: func(cmd *cobra.Command, args []string) error {
			if peer == "" || id == "" {
				return fmt.Errorf("--peer and --id are required")
			}
			return runWaitingRPC(ctx, cmd, *jsonOut, timeout, peer, "waiting.resolve", map[string]any{"id": id})
		},
	}
	cmd.Flags().StringVar(&peer, "peer", "", "peer ID")
	cmd.Flags().StringVar(&id, "id", "", "waiting-on ID")
	cmd.Flags().DurationVar(&timeout, "timeout", 10*time.Second, "request timeout")
	return cmd
}

func newWaitingReopenCommand(ctx *commandContext, jsonOut *bool) *cobra.Command {
	var peer, id string
	var timeout time.Duration
	cmd := &cobra.Command{
		Use:   "reopen",
		Short: "Reopen a resolved waiting-on record",
		RunE: func(cmd *cobra.Command, args []string) error {
			if peer == "" || id == "" {
				return fmt.Errorf("--peer and --id are required")
			}
			return runWaitingRPC(ctx, cmd, *jsonOut, timeout, peer, "waiting.reopen", map[string]any{"id": id})
		},
	}
	cmd.Flags().StringVar(&peer, "peer", "", "peer ID")
	cmd.Flags().StringVar(&id, "id", "", "waiting-on ID")
	cmd.Flags().DurationVar(&timeout, "timeout", 10*time.Second, "request timeout")
	return cmd
}

func runWaitingRPC(ctx *commandContext, cmd *cobra.Command, jsonOut bool, timeout time.Duration, peer string, method string, params map[string]any) error {
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

func formatWaitingList(result map[string]any) string {
	items, _ := result["waiting"].([]any)
	status, _ := result["status"].(string)
	if len(items) == 0 {
		return fmt.Sprintf("No %s waiting-on records.", status)
	}
	var lines []string
	lines = append(lines, fmt.Sprintf("Waiting-on (%s):", status))
	for _, raw := range items {
		item, _ := raw.(map[string]any)
		id, _ := item["id"].(string)
		title, _ := item["title"].(string)
		waitingFor, _ := item["waiting_for"].(string)
		itemStatus, _ := item["status"].(string)
		line := fmt.Sprintf("- %s [%s] %s", id, itemStatus, title)
		if waitingFor != "" {
			line += fmt.Sprintf(" (waiting for: %s)", waitingFor)
		}
		lines = append(lines, line)
	}
	return strings.Join(lines, "\n")
}
