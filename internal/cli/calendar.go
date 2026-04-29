package cli

import (
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

func newCalendarCommand(ctx *commandContext) *cobra.Command {
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "calendar",
		Short: "Calendar source management and agenda queries",
	}
	enableDerivedPeerDefault(cmd)
	cmd.PersistentFlags().BoolVar(&jsonOut, "json", false, "emit JSON output")
	cmd.AddCommand(newCalendarAddCommand(ctx, &jsonOut))
	cmd.AddCommand(newCalendarListCommand(ctx, &jsonOut))
	cmd.AddCommand(newCalendarRemoveCommand(ctx, &jsonOut))
	cmd.AddCommand(newCalendarAgendaCommand(ctx, &jsonOut))
	return cmd
}

func newCalendarAddCommand(ctx *commandContext, jsonOut *bool) *cobra.Command {
	var peer, name, path, rawURL, timezone string
	var enabled bool
	var timeout time.Duration
	cmd := &cobra.Command{
		Use:   "add",
		Short: "Register a local or remote ICS calendar source",
		RunE: func(cmd *cobra.Command, args []string) error {
			if peer == "" {
				return fmt.Errorf("--peer is required")
			}
			if strings.TrimSpace(path) == "" && strings.TrimSpace(rawURL) == "" {
				return fmt.Errorf("--path or --url is required")
			}
			if strings.TrimSpace(path) != "" && strings.TrimSpace(rawURL) != "" {
				return fmt.Errorf("provide either --path or --url, not both")
			}
			state, err := ctx.state()
			if err != nil {
				return err
			}
			client := newGatewayClient(state, timeout)
			params := map[string]any{
				"name":    name,
				"enabled": enabled,
			}
			if path != "" {
				params["path"] = path
			}
			if rawURL != "" {
				params["url"] = rawURL
			}
			if timezone != "" {
				params["timezone"] = timezone
			}
			var result map[string]any
			if err := client.rpc(cmd.Context(), peer, "calendar.source.create", params, &result); err != nil {
				return err
			}
			emit(cmd, *jsonOut, result)
			return nil
		},
	}
	cmd.Flags().StringVar(&peer, "peer", "", "peer ID")
	cmd.Flags().StringVar(&name, "name", "", "display name for the calendar source")
	cmd.Flags().StringVar(&path, "path", "", "local .ics file path")
	cmd.Flags().StringVar(&rawURL, "url", "", "remote ICS URL")
	cmd.Flags().StringVar(&timezone, "timezone", "", "default timezone for floating events")
	cmd.Flags().BoolVar(&enabled, "enabled", true, "enable the source immediately")
	cmd.Flags().DurationVar(&timeout, "timeout", 10*time.Second, "request timeout")
	return cmd
}

func newCalendarListCommand(ctx *commandContext, jsonOut *bool) *cobra.Command {
	var peer string
	var enabledOnly bool
	var timeout time.Duration
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List registered calendar sources",
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
			if err := client.rpc(cmd.Context(), peer, "calendar.source.list", map[string]any{"enabled_only": enabledOnly}, &result); err != nil {
				return err
			}
			if *jsonOut {
				emit(cmd, true, result)
			} else {
				emit(cmd, false, formatCalendarSources(result))
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&peer, "peer", "", "peer ID")
	cmd.Flags().BoolVar(&enabledOnly, "enabled-only", false, "show only enabled sources")
	cmd.Flags().DurationVar(&timeout, "timeout", 10*time.Second, "request timeout")
	return cmd
}

func newCalendarRemoveCommand(ctx *commandContext, jsonOut *bool) *cobra.Command {
	var peer, id string
	var timeout time.Duration
	cmd := &cobra.Command{
		Use:   "remove",
		Short: "Delete a calendar source",
		RunE: func(cmd *cobra.Command, args []string) error {
			if id == "" {
				return fmt.Errorf("--id is required")
			}
			state, err := ctx.state()
			if err != nil {
				return err
			}
			client := newGatewayClient(state, timeout)
			var result map[string]any
			if err := client.rpc(cmd.Context(), peer, "calendar.source.delete", map[string]any{"id": id}, &result); err != nil {
				return err
			}
			emit(cmd, *jsonOut, result)
			return nil
		},
	}
	cmd.Flags().StringVar(&peer, "peer", "", "peer ID")
	cmd.Flags().StringVar(&id, "id", "", "calendar source ID")
	cmd.Flags().DurationVar(&timeout, "timeout", 10*time.Second, "request timeout")
	return cmd
}

func newCalendarAgendaCommand(ctx *commandContext, jsonOut *bool) *cobra.Command {
	var peer, scope, timezone string
	var limit int
	var now int64
	var timeout time.Duration
	cmd := &cobra.Command{
		Use:   "agenda",
		Short: "Query the current agenda or next calendar conflict",
		RunE: func(cmd *cobra.Command, args []string) error {
			if peer == "" {
				return fmt.Errorf("--peer is required")
			}
			state, err := ctx.state()
			if err != nil {
				return err
			}
			client := newGatewayClient(state, timeout)
			params := map[string]any{"scope": scope, "limit": limit}
			if timezone != "" {
				params["timezone"] = timezone
			}
			if now > 0 {
				params["now"] = now
			}
			var result map[string]any
			if err := client.rpc(cmd.Context(), peer, "calendar.agenda", params, &result); err != nil {
				return err
			}
			if *jsonOut {
				emit(cmd, true, result)
			} else {
				emit(cmd, false, formatAgenda(result))
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&peer, "peer", "", "peer ID")
	cmd.Flags().StringVar(&scope, "scope", "today", "agenda scope: today, this_week, next_conflict")
	cmd.Flags().StringVar(&timezone, "timezone", "", "display timezone")
	cmd.Flags().IntVar(&limit, "limit", 20, "max events to return for range views")
	cmd.Flags().Int64Var(&now, "now", 0, "override current time as Unix seconds")
	cmd.Flags().DurationVar(&timeout, "timeout", 10*time.Second, "request timeout")
	return cmd
}

func formatCalendarSources(result map[string]any) string {
	sources, _ := result["sources"].([]any)
	if len(sources) == 0 {
		return "No calendar sources."
	}
	var sb strings.Builder
	sb.WriteString("Calendar sources:")
	for _, raw := range sources {
		source, _ := raw.(map[string]any)
		target := stringValue(source, "path")
		if target == "" {
			target = stringValue(source, "url")
		}
		fmt.Fprintf(&sb, "\n- %s [%s] %s", stringValue(source, "id"), stringValue(source, "kind"), firstCLIValue(stringValue(source, "name"), target))
		if target != "" && target != stringValue(source, "name") {
			fmt.Fprintf(&sb, " -> %s", target)
		}
	}
	return sb.String()
}

func formatAgenda(result map[string]any) string {
	agenda, _ := result["agenda"].(map[string]any)
	if agenda == nil {
		return "No agenda data."
	}
	if conflict, _ := agenda["conflict"].(map[string]any); conflict != nil {
		events, _ := conflict["events"].([]any)
		if len(events) == 0 {
			return "No upcoming conflicts found."
		}
		var sb strings.Builder
		sb.WriteString("Next conflict:")
		for _, raw := range events {
			event, _ := raw.(map[string]any)
			fmt.Fprintf(&sb, "\n- %s (%s)", stringValue(event, "summary"), formatEventSpan(event))
		}
		return sb.String()
	}
	events, _ := agenda["events"].([]any)
	if len(events) == 0 {
		return "No events in the requested agenda window."
	}
	var sb strings.Builder
	sb.WriteString("Agenda:")
	for _, raw := range events {
		event, _ := raw.(map[string]any)
		fmt.Fprintf(&sb, "\n- %s (%s)", stringValue(event, "summary"), formatEventSpan(event))
	}
	return sb.String()
}

func formatEventSpan(event map[string]any) string {
	start := unixToCLITime(event["start_at"])
	end := unixToCLITime(event["end_at"])
	if start.IsZero() {
		return "unscheduled"
	}
	if isTruthy(event["all_day"]) {
		return start.Format("2006-01-02") + " all day"
	}
	if end.IsZero() || end.Equal(start) {
		return start.Format(time.RFC3339)
	}
	return start.Format(time.RFC3339) + " to " + end.Format(time.RFC3339)
}

func stringValue(m map[string]any, key string) string {
	value, _ := m[key].(string)
	return value
}

func firstCLIValue(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func unixToCLITime(value any) time.Time {
	switch typed := value.(type) {
	case float64:
		return time.Unix(int64(typed), 0)
	case int64:
		return time.Unix(typed, 0)
	case int:
		return time.Unix(int64(typed), 0)
	default:
		return time.Time{}
	}
}

func isTruthy(value any) bool {
	switch typed := value.(type) {
	case bool:
		return typed
	default:
		return false
	}
}
