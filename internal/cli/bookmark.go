package cli

import (
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

func newBookmarkCommand(ctx *commandContext) *cobra.Command {
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "bookmark",
		Short: "Conversation bookmarks and save-for-later recall",
	}
	cmd.PersistentFlags().BoolVar(&jsonOut, "json", false, "emit JSON output")
	cmd.AddCommand(newBookmarkListCommand(ctx, &jsonOut))
	cmd.AddCommand(newBookmarkGetCommand(ctx, &jsonOut))
	cmd.AddCommand(newBookmarkAddCommand(ctx, &jsonOut))
	cmd.AddCommand(newBookmarkClipCommand(ctx, &jsonOut))
	cmd.AddCommand(newBookmarkSearchCommand(ctx, &jsonOut))
	cmd.AddCommand(newBookmarkUpdateCommand(ctx, &jsonOut))
	cmd.AddCommand(newBookmarkDeleteCommand(ctx, &jsonOut))
	return cmd
}

func newBookmarkListCommand(ctx *commandContext, jsonOut *bool) *cobra.Command {
	var peer, label string
	var limit int
	var upcomingOnly bool
	var timeout time.Duration
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List bookmarks",
		RunE: func(cmd *cobra.Command, args []string) error {
			if peer == "" {
				return fmt.Errorf("--peer is required")
			}
			state, err := ctx.state()
			if err != nil {
				return err
			}
			client := newGatewayClient(state, timeout)
			params := map[string]any{"limit": limit, "upcoming_only": upcomingOnly}
			if strings.TrimSpace(label) != "" {
				params["label"] = label
			}
			var result map[string]any
			if err := client.rpc(cmd.Context(), peer, "bookmark.list", params, &result); err != nil {
				return err
			}
			if *jsonOut {
				emit(cmd, true, result)
			} else {
				emit(cmd, false, formatBookmarkList(result))
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&peer, "peer", "", "peer ID")
	cmd.Flags().StringVar(&label, "label", "", "optional label filter")
	cmd.Flags().BoolVar(&upcomingOnly, "upcoming", false, "only show bookmarks with reminders")
	cmd.Flags().IntVar(&limit, "limit", 20, "max bookmarks to return")
	cmd.Flags().DurationVar(&timeout, "timeout", 10*time.Second, "request timeout")
	return cmd
}

func newBookmarkGetCommand(ctx *commandContext, jsonOut *bool) *cobra.Command {
	var peer, id string
	var timeout time.Duration
	cmd := &cobra.Command{
		Use:   "get",
		Short: "Fetch one bookmark",
		RunE: func(cmd *cobra.Command, args []string) error {
			if peer == "" || id == "" {
				return fmt.Errorf("--peer and --id are required")
			}
			return runBookmarkRPC(ctx, cmd, *jsonOut, timeout, peer, "bookmark.get", map[string]any{"id": id})
		},
	}
	cmd.Flags().StringVar(&peer, "peer", "", "peer ID")
	cmd.Flags().StringVar(&id, "id", "", "bookmark ID")
	cmd.Flags().DurationVar(&timeout, "timeout", 10*time.Second, "request timeout")
	return cmd
}

func newBookmarkAddCommand(ctx *commandContext, jsonOut *bool) *cobra.Command {
	var peer, title, content, labels, sourceKind, sourceSessionKey, sourceRunID, sourceExcerpt string
	var reminderAt int64
	var sourceStartIndex, sourceEndIndex int
	var timeout time.Duration
	cmd := &cobra.Command{
		Use:   "add",
		Short: "Create a manual bookmark",
		RunE: func(cmd *cobra.Command, args []string) error {
			if peer == "" || strings.TrimSpace(title) == "" || strings.TrimSpace(content) == "" {
				return fmt.Errorf("--peer, --title, and --content are required")
			}
			params := map[string]any{"title": title, "content": content}
			if strings.TrimSpace(labels) != "" {
				params["labels"] = splitCSV(labels)
			}
			if reminderAt > 0 {
				params["reminder_at"] = reminderAt
			}
			if strings.TrimSpace(sourceKind) != "" {
				params["source_kind"] = sourceKind
			}
			if strings.TrimSpace(sourceSessionKey) != "" {
				params["source_session_key"] = sourceSessionKey
			}
			if strings.TrimSpace(sourceRunID) != "" {
				params["source_run_id"] = sourceRunID
			}
			if sourceStartIndex > 0 {
				params["source_start_index"] = sourceStartIndex
			}
			if sourceEndIndex > 0 {
				params["source_end_index"] = sourceEndIndex
			}
			if strings.TrimSpace(sourceExcerpt) != "" {
				params["source_excerpt"] = sourceExcerpt
			}
			return runBookmarkRPC(ctx, cmd, *jsonOut, timeout, peer, "bookmark.create", params)
		},
	}
	cmd.Flags().StringVar(&peer, "peer", "", "peer ID")
	cmd.Flags().StringVar(&title, "title", "", "bookmark title")
	cmd.Flags().StringVar(&content, "content", "", "bookmark content")
	cmd.Flags().StringVar(&labels, "labels", "", "comma-separated labels")
	cmd.Flags().Int64Var(&reminderAt, "reminder-at", 0, "reminder timestamp as Unix seconds")
	cmd.Flags().StringVar(&sourceKind, "source-kind", "manual", "source kind")
	cmd.Flags().StringVar(&sourceSessionKey, "source-session-key", "", "source session key")
	cmd.Flags().StringVar(&sourceRunID, "source-run-id", "", "source run ID")
	cmd.Flags().IntVar(&sourceStartIndex, "source-start-index", 0, "optional source start index")
	cmd.Flags().IntVar(&sourceEndIndex, "source-end-index", 0, "optional source end index")
	cmd.Flags().StringVar(&sourceExcerpt, "source-excerpt", "", "optional source excerpt")
	cmd.Flags().DurationVar(&timeout, "timeout", 10*time.Second, "request timeout")
	return cmd
}

func newBookmarkClipCommand(ctx *commandContext, jsonOut *bool) *cobra.Command {
	var peer, sessionKey, runID, title, labels string
	var startIndex, endIndex int
	var reminderAt int64
	var timeout time.Duration
	cmd := &cobra.Command{
		Use:   "clip",
		Short: "Capture a session-history range into a bookmark",
		RunE: func(cmd *cobra.Command, args []string) error {
			if peer == "" || startIndex <= 0 {
				return fmt.Errorf("--peer and --start are required")
			}
			params := map[string]any{"start_index": startIndex, "title": title}
			if endIndex > 0 {
				params["end_index"] = endIndex
			}
			if strings.TrimSpace(sessionKey) != "" {
				params["session_key"] = sessionKey
			}
			if strings.TrimSpace(runID) != "" {
				params["run_id"] = runID
			}
			if strings.TrimSpace(labels) != "" {
				params["labels"] = splitCSV(labels)
			}
			if reminderAt > 0 {
				params["reminder_at"] = reminderAt
			}
			return runBookmarkRPC(ctx, cmd, *jsonOut, timeout, peer, "bookmark.capture_session", params)
		},
	}
	cmd.Flags().StringVar(&peer, "peer", "", "peer ID")
	cmd.Flags().StringVar(&sessionKey, "session-key", "", "optional target session key")
	cmd.Flags().StringVar(&runID, "run-id", "", "optional target sub-session run ID")
	cmd.Flags().StringVar(&title, "title", "", "optional bookmark title")
	cmd.Flags().IntVar(&startIndex, "start", 0, "1-based start message index")
	cmd.Flags().IntVar(&endIndex, "end", 0, "1-based end message index")
	cmd.Flags().StringVar(&labels, "labels", "", "comma-separated labels")
	cmd.Flags().Int64Var(&reminderAt, "reminder-at", 0, "reminder timestamp as Unix seconds")
	cmd.Flags().DurationVar(&timeout, "timeout", 10*time.Second, "request timeout")
	return cmd
}

func newBookmarkSearchCommand(ctx *commandContext, jsonOut *bool) *cobra.Command {
	var peer, query string
	var limit int
	var timeout time.Duration
	cmd := &cobra.Command{
		Use:   "search",
		Short: "Search bookmarks",
		RunE: func(cmd *cobra.Command, args []string) error {
			if peer == "" || strings.TrimSpace(query) == "" {
				return fmt.Errorf("--peer and --query are required")
			}
			return runBookmarkRPC(ctx, cmd, *jsonOut, timeout, peer, "bookmark.search", map[string]any{"query": query, "limit": limit})
		},
	}
	cmd.Flags().StringVar(&peer, "peer", "", "peer ID")
	cmd.Flags().StringVar(&query, "query", "", "search query")
	cmd.Flags().IntVar(&limit, "limit", 20, "max bookmarks to return")
	cmd.Flags().DurationVar(&timeout, "timeout", 10*time.Second, "request timeout")
	return cmd
}

func newBookmarkUpdateCommand(ctx *commandContext, jsonOut *bool) *cobra.Command {
	var peer, id, title, content, labels, sourceKind, sourceSessionKey, sourceRunID, sourceExcerpt string
	var reminderAt int64
	var sourceStartIndex, sourceEndIndex int
	var timeout time.Duration
	cmd := &cobra.Command{
		Use:   "update",
		Short: "Update a bookmark",
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
			if cmd.Flags().Changed("content") {
				params["content"] = content
			}
			if cmd.Flags().Changed("labels") {
				params["labels"] = splitCSV(labels)
			}
			if cmd.Flags().Changed("reminder-at") {
				params["reminder_at"] = reminderAt
			}
			if cmd.Flags().Changed("source-kind") {
				params["source_kind"] = sourceKind
			}
			if cmd.Flags().Changed("source-session-key") {
				params["source_session_key"] = sourceSessionKey
			}
			if cmd.Flags().Changed("source-run-id") {
				params["source_run_id"] = sourceRunID
			}
			if cmd.Flags().Changed("source-start-index") {
				params["source_start_index"] = sourceStartIndex
			}
			if cmd.Flags().Changed("source-end-index") {
				params["source_end_index"] = sourceEndIndex
			}
			if cmd.Flags().Changed("source-excerpt") {
				params["source_excerpt"] = sourceExcerpt
			}
			var result map[string]any
			if err := client.rpc(cmd.Context(), peer, "bookmark.update", params, &result); err != nil {
				return err
			}
			if *jsonOut {
				emit(cmd, true, result)
			} else {
				emit(cmd, false, formatBookmarkGet(result))
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&peer, "peer", "", "peer ID")
	cmd.Flags().StringVar(&id, "id", "", "bookmark ID")
	cmd.Flags().StringVar(&title, "title", "", "bookmark title")
	cmd.Flags().StringVar(&content, "content", "", "bookmark content")
	cmd.Flags().StringVar(&labels, "labels", "", "comma-separated labels")
	cmd.Flags().Int64Var(&reminderAt, "reminder-at", 0, "reminder timestamp as Unix seconds")
	cmd.Flags().StringVar(&sourceKind, "source-kind", "", "source kind")
	cmd.Flags().StringVar(&sourceSessionKey, "source-session-key", "", "source session key")
	cmd.Flags().StringVar(&sourceRunID, "source-run-id", "", "source run ID")
	cmd.Flags().IntVar(&sourceStartIndex, "source-start-index", 0, "source start index")
	cmd.Flags().IntVar(&sourceEndIndex, "source-end-index", 0, "source end index")
	cmd.Flags().StringVar(&sourceExcerpt, "source-excerpt", "", "source excerpt")
	cmd.Flags().DurationVar(&timeout, "timeout", 10*time.Second, "request timeout")
	return cmd
}

func newBookmarkDeleteCommand(ctx *commandContext, jsonOut *bool) *cobra.Command {
	var peer, id string
	var timeout time.Duration
	cmd := &cobra.Command{
		Use:   "delete",
		Short: "Delete a bookmark",
		RunE: func(cmd *cobra.Command, args []string) error {
			if peer == "" || id == "" {
				return fmt.Errorf("--peer and --id are required")
			}
			return runBookmarkRPC(ctx, cmd, *jsonOut, timeout, peer, "bookmark.delete", map[string]any{"id": id})
		},
	}
	cmd.Flags().StringVar(&peer, "peer", "", "peer ID")
	cmd.Flags().StringVar(&id, "id", "", "bookmark ID")
	cmd.Flags().DurationVar(&timeout, "timeout", 10*time.Second, "request timeout")
	return cmd
}

func runBookmarkRPC(ctx *commandContext, cmd *cobra.Command, jsonOut bool, timeout time.Duration, peer, method string, params map[string]any) error {
	state, err := ctx.state()
	if err != nil {
		return err
	}
	client := newGatewayClient(state, timeout)
	var result map[string]any
	if err := client.rpc(cmd.Context(), peer, method, params, &result); err != nil {
		return err
	}
	if jsonOut {
		emit(cmd, true, result)
		return nil
	}
	switch method {
	case "bookmark.list", "bookmark.search":
		emit(cmd, false, formatBookmarkList(result))
	case "bookmark.get", "bookmark.update":
		emit(cmd, false, formatBookmarkGet(result))
	default:
		emit(cmd, false, result)
	}
	return nil
}

func formatBookmarkList(result map[string]any) string {
	count := intFromAny(result["count"])
	var sb strings.Builder
	fmt.Fprintf(&sb, "Bookmarks: %d total", count)
	entries, _ := result["bookmarks"].([]any)
	for _, entry := range entries {
		bookmark, ok := entry.(map[string]any)
		if !ok {
			continue
		}
		fmt.Fprintf(&sb, "\n- %s %s", stringFromAny(bookmark["id"]), stringFromAny(bookmark["title"]))
		if meta := formatBookmarkMeta(bookmark); meta != "" {
			fmt.Fprintf(&sb, "\n  %s", meta)
		}
	}
	return sb.String()
}

func formatBookmarkGet(result map[string]any) string {
	bookmark, _ := result["bookmark"].(map[string]any)
	if len(bookmark) == 0 {
		return "Bookmark not found."
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "%s\n%s", stringFromAny(bookmark["id"]), stringFromAny(bookmark["title"]))
	if meta := formatBookmarkMeta(bookmark); meta != "" {
		fmt.Fprintf(&sb, "\n%s", meta)
	}
	if content := stringFromAny(bookmark["content"]); content != "" {
		fmt.Fprintf(&sb, "\n\n%s", content)
	}
	return sb.String()
}

func formatBookmarkMeta(bookmark map[string]any) string {
	parts := []string{}
	if labels, ok := bookmark["labels"].([]any); ok && len(labels) > 0 {
		vals := make([]string, 0, len(labels))
		for _, label := range labels {
			vals = append(vals, stringFromAny(label))
		}
		parts = append(parts, "labels="+strings.Join(vals, ","))
	}
	if reminderAt := intFromAny(bookmark["reminder_at"]); reminderAt > 0 {
		parts = append(parts, "reminder="+time.Unix(int64(reminderAt), 0).UTC().Format(time.RFC3339))
	}
	if sourceKind := stringFromAny(bookmark["source_kind"]); sourceKind != "" {
		parts = append(parts, "source="+sourceKind)
	}
	start := intFromAny(bookmark["source_start_index"])
	end := intFromAny(bookmark["source_end_index"])
	if start > 0 {
		parts = append(parts, fmt.Sprintf("messages=%d-%d", start, max(start, end)))
	}
	return strings.Join(parts, " | ")
}

func splitCSV(value string) []string {
	parts := strings.Split(value, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		trimmed := strings.TrimSpace(part)
		if trimmed == "" {
			continue
		}
		out = append(out, trimmed)
	}
	return out
}
