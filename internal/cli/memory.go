package cli

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/ffimnsr/koios/internal/memory"
)

func newMemoryCommand(ctx *commandContext) *cobra.Command {
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "memory",
		Short: "Long-term memory operations",
	}
	cmd.PersistentFlags().BoolVar(&jsonOut, "json", false, "emit JSON output")
	cmd.AddCommand(newMemoryStatsCommand(ctx, &jsonOut))
	cmd.AddCommand(newMemoryListCommand(ctx, &jsonOut))
	cmd.AddCommand(newMemorySearchCommand(ctx, &jsonOut))
	cmd.AddCommand(newMemoryEntityCommand(ctx, &jsonOut))
	cmd.AddCommand(newMemoryQueueCommand(ctx, &jsonOut))
	return cmd
}

func newMemoryStatsCommand(ctx *commandContext, jsonOut *bool) *cobra.Command {
	var (
		peer    string
		timeout time.Duration
	)
	cmd := &cobra.Command{
		Use:   "stats",
		Short: "Show memory store statistics for a peer",
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
			if err := client.rpc(cmd.Context(), peer, "memory.stats", map[string]any{}, &result); err != nil {
				return err
			}
			emit(cmd, *jsonOut, result)
			return nil
		},
	}
	cmd.Flags().StringVar(&peer, "peer", "", "peer ID")
	cmd.Flags().DurationVar(&timeout, "timeout", 10*time.Second, "request timeout")
	return cmd
}

func newMemoryListCommand(ctx *commandContext, jsonOut *bool) *cobra.Command {
	var (
		peer    string
		limit   int
		timeout time.Duration
	)
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List memory chunks for a peer",
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
			params := map[string]any{"limit": limit}
			if err := client.rpc(cmd.Context(), peer, "memory.list", params, &result); err != nil {
				return err
			}
			emit(cmd, *jsonOut, result)
			return nil
		},
	}
	cmd.Flags().StringVar(&peer, "peer", "", "peer ID")
	cmd.Flags().IntVar(&limit, "limit", 50, "max chunks to return")
	cmd.Flags().DurationVar(&timeout, "timeout", 10*time.Second, "request timeout")
	return cmd
}

func newMemorySearchCommand(ctx *commandContext, jsonOut *bool) *cobra.Command {
	var (
		peer    string
		query   string
		limit   int
		timeout time.Duration
	)
	cmd := &cobra.Command{
		Use:   "search",
		Short: "Search memory chunks for a peer",
		RunE: func(cmd *cobra.Command, args []string) error {
			if peer == "" {
				return fmt.Errorf("--peer is required")
			}
			if query == "" {
				return fmt.Errorf("--query is required")
			}
			state, err := ctx.state()
			if err != nil {
				return err
			}
			client := newGatewayClient(state, timeout)
			var result map[string]any
			params := map[string]any{"q": query, "limit": limit}
			if err := client.rpc(cmd.Context(), peer, "memory.search", params, &result); err != nil {
				return err
			}
			emit(cmd, *jsonOut, result)
			return nil
		},
	}
	cmd.Flags().StringVar(&peer, "peer", "", "peer ID")
	cmd.Flags().StringVarP(&query, "query", "q", "", "search query")
	cmd.Flags().IntVar(&limit, "limit", 5, "max results to return")
	cmd.Flags().DurationVar(&timeout, "timeout", 10*time.Second, "request timeout")
	return cmd
}

func newMemoryEntityCommand(ctx *commandContext, jsonOut *bool) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "entity",
		Short: "Entity graph operations for durable people, projects, places, and topics",
	}
	cmd.AddCommand(newMemoryEntityCreateCommand(ctx, jsonOut))
	cmd.AddCommand(newMemoryEntityUpdateCommand(ctx, jsonOut))
	cmd.AddCommand(newMemoryEntityGetCommand(ctx, jsonOut))
	cmd.AddCommand(newMemoryEntityListCommand(ctx, jsonOut))
	cmd.AddCommand(newMemoryEntitySearchCommand(ctx, jsonOut))
	cmd.AddCommand(newMemoryEntityLinkChunkCommand(ctx, jsonOut))
	cmd.AddCommand(newMemoryEntityRelateCommand(ctx, jsonOut))
	cmd.AddCommand(newMemoryEntityTouchCommand(ctx, jsonOut))
	cmd.AddCommand(newMemoryEntityDeleteCommand(ctx, jsonOut))
	cmd.AddCommand(newMemoryEntityUnlinkChunkCommand(ctx, jsonOut))
	cmd.AddCommand(newMemoryEntityUnrelateCommand(ctx, jsonOut))
	return cmd
}

func newMemoryEntityCreateCommand(ctx *commandContext, jsonOut *bool) *cobra.Command {
	var (
		peer       string
		kind       string
		name       string
		aliases    []string
		notes      string
		lastSeenAt int64
		timeout    time.Duration
	)
	cmd := &cobra.Command{
		Use:   "create",
		Short: "Create a durable memory entity",
		RunE: func(cmd *cobra.Command, args []string) error {
			if peer == "" {
				return fmt.Errorf("--peer is required")
			}
			if strings.TrimSpace(kind) == "" {
				return fmt.Errorf("--kind is required")
			}
			if strings.TrimSpace(name) == "" {
				return fmt.Errorf("--name is required")
			}
			state, err := ctx.state()
			if err != nil {
				return err
			}
			client := newGatewayClient(state, timeout)
			params := map[string]any{"kind": kind, "name": name}
			if len(aliases) > 0 {
				params["aliases"] = aliases
			}
			if notes != "" {
				params["notes"] = notes
			}
			if lastSeenAt > 0 {
				params["last_seen_at"] = lastSeenAt
			}
			var result map[string]any
			if err := client.rpc(cmd.Context(), peer, "memory.entity.create", params, &result); err != nil {
				return err
			}
			emit(cmd, *jsonOut, result)
			return nil
		},
	}
	cmd.Flags().StringVar(&peer, "peer", "", "peer ID")
	cmd.Flags().StringVar(&kind, "kind", "", "entity kind: person, project, place, topic")
	cmd.Flags().StringVar(&name, "name", "", "entity name")
	cmd.Flags().StringSliceVar(&aliases, "aliases", nil, "entity aliases")
	cmd.Flags().StringVar(&notes, "notes", "", "entity notes")
	cmd.Flags().Int64Var(&lastSeenAt, "last-seen-at", 0, "last seen timestamp as Unix seconds")
	cmd.Flags().DurationVar(&timeout, "timeout", 10*time.Second, "request timeout")
	return cmd
}

func newMemoryEntityUpdateCommand(ctx *commandContext, jsonOut *bool) *cobra.Command {
	var (
		peer       string
		id         string
		kind       string
		name       string
		aliases    []string
		notes      string
		lastSeenAt int64
		timeout    time.Duration
	)
	cmd := &cobra.Command{
		Use:   "update",
		Short: "Update a memory entity",
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
			if cmd.Flags().Changed("kind") {
				params["kind"] = kind
			}
			if cmd.Flags().Changed("name") {
				params["name"] = name
			}
			if cmd.Flags().Changed("aliases") {
				params["aliases"] = aliases
			}
			if cmd.Flags().Changed("notes") {
				params["notes"] = notes
			}
			if cmd.Flags().Changed("last-seen-at") {
				params["last_seen_at"] = lastSeenAt
			}
			var result map[string]any
			if err := client.rpc(cmd.Context(), peer, "memory.entity.update", params, &result); err != nil {
				return err
			}
			emit(cmd, *jsonOut, result)
			return nil
		},
	}
	cmd.Flags().StringVar(&peer, "peer", "", "peer ID")
	cmd.Flags().StringVar(&id, "id", "", "entity ID")
	cmd.Flags().StringVar(&kind, "kind", "", "entity kind: person, project, place, topic")
	cmd.Flags().StringVar(&name, "name", "", "entity name")
	cmd.Flags().StringSliceVar(&aliases, "aliases", nil, "entity aliases")
	cmd.Flags().StringVar(&notes, "notes", "", "entity notes")
	cmd.Flags().Int64Var(&lastSeenAt, "last-seen-at", 0, "last seen timestamp as Unix seconds")
	cmd.Flags().DurationVar(&timeout, "timeout", 10*time.Second, "request timeout")
	return cmd
}

func newMemoryEntityGetCommand(ctx *commandContext, jsonOut *bool) *cobra.Command {
	var (
		peer    string
		id      string
		timeout time.Duration
	)
	cmd := &cobra.Command{
		Use:   "get",
		Short: "Fetch one memory entity with linked chunks and relationships",
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
			if err := client.rpc(cmd.Context(), peer, "memory.entity.get", map[string]any{"id": id}, &result); err != nil {
				return err
			}
			emit(cmd, *jsonOut, result)
			return nil
		},
	}
	cmd.Flags().StringVar(&peer, "peer", "", "peer ID")
	cmd.Flags().StringVar(&id, "id", "", "entity ID")
	cmd.Flags().DurationVar(&timeout, "timeout", 10*time.Second, "request timeout")
	return cmd
}

func newMemoryEntityListCommand(ctx *commandContext, jsonOut *bool) *cobra.Command {
	var (
		peer    string
		kind    string
		limit   int
		timeout time.Duration
	)
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List memory entities",
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
			if kind != "" {
				params["kind"] = kind
			}
			var result map[string]any
			if err := client.rpc(cmd.Context(), peer, "memory.entity.list", params, &result); err != nil {
				return err
			}
			emit(cmd, *jsonOut, result)
			return nil
		},
	}
	cmd.Flags().StringVar(&peer, "peer", "", "peer ID")
	cmd.Flags().StringVar(&kind, "kind", "", "entity kind filter")
	cmd.Flags().IntVar(&limit, "limit", 50, "max entities to return")
	cmd.Flags().DurationVar(&timeout, "timeout", 10*time.Second, "request timeout")
	return cmd
}

func newMemoryEntitySearchCommand(ctx *commandContext, jsonOut *bool) *cobra.Command {
	var (
		peer    string
		query   string
		limit   int
		timeout time.Duration
	)
	cmd := &cobra.Command{
		Use:   "search",
		Short: "Search memory entities",
		RunE: func(cmd *cobra.Command, args []string) error {
			if peer == "" {
				return fmt.Errorf("--peer is required")
			}
			if query == "" {
				return fmt.Errorf("--query is required")
			}
			state, err := ctx.state()
			if err != nil {
				return err
			}
			client := newGatewayClient(state, timeout)
			var result map[string]any
			if err := client.rpc(cmd.Context(), peer, "memory.entity.search", map[string]any{"q": query, "limit": limit}, &result); err != nil {
				return err
			}
			emit(cmd, *jsonOut, result)
			return nil
		},
	}
	cmd.Flags().StringVar(&peer, "peer", "", "peer ID")
	cmd.Flags().StringVarP(&query, "query", "q", "", "search query")
	cmd.Flags().IntVar(&limit, "limit", 10, "max entities to return")
	cmd.Flags().DurationVar(&timeout, "timeout", 10*time.Second, "request timeout")
	return cmd
}

func newMemoryEntityLinkChunkCommand(ctx *commandContext, jsonOut *bool) *cobra.Command {
	var (
		peer    string
		id      string
		chunkID string
		timeout time.Duration
	)
	cmd := &cobra.Command{
		Use:   "link-chunk",
		Short: "Link a memory chunk to an entity",
		RunE: func(cmd *cobra.Command, args []string) error {
			if peer == "" {
				return fmt.Errorf("--peer is required")
			}
			if id == "" {
				return fmt.Errorf("--id is required")
			}
			if chunkID == "" {
				return fmt.Errorf("--chunk-id is required")
			}
			state, err := ctx.state()
			if err != nil {
				return err
			}
			client := newGatewayClient(state, timeout)
			var result map[string]any
			if err := client.rpc(cmd.Context(), peer, "memory.entity.link_chunk", map[string]any{"id": id, "chunk_id": chunkID}, &result); err != nil {
				return err
			}
			emit(cmd, *jsonOut, result)
			return nil
		},
	}
	cmd.Flags().StringVar(&peer, "peer", "", "peer ID")
	cmd.Flags().StringVar(&id, "id", "", "entity ID")
	cmd.Flags().StringVar(&chunkID, "chunk-id", "", "memory chunk ID")
	cmd.Flags().DurationVar(&timeout, "timeout", 10*time.Second, "request timeout")
	return cmd
}

func newMemoryEntityRelateCommand(ctx *commandContext, jsonOut *bool) *cobra.Command {
	var (
		peer     string
		sourceID string
		targetID string
		relation string
		notes    string
		timeout  time.Duration
	)
	cmd := &cobra.Command{
		Use:   "relate",
		Short: "Create or refresh a relationship edge between two entities",
		RunE: func(cmd *cobra.Command, args []string) error {
			if peer == "" {
				return fmt.Errorf("--peer is required")
			}
			if sourceID == "" {
				return fmt.Errorf("--source is required")
			}
			if targetID == "" {
				return fmt.Errorf("--target is required")
			}
			if relation == "" {
				return fmt.Errorf("--relation is required")
			}
			state, err := ctx.state()
			if err != nil {
				return err
			}
			client := newGatewayClient(state, timeout)
			params := map[string]any{"source_id": sourceID, "target_id": targetID, "relation": relation}
			if notes != "" {
				params["notes"] = notes
			}
			var result map[string]any
			if err := client.rpc(cmd.Context(), peer, "memory.entity.relate", params, &result); err != nil {
				return err
			}
			emit(cmd, *jsonOut, result)
			return nil
		},
	}
	cmd.Flags().StringVar(&peer, "peer", "", "peer ID")
	cmd.Flags().StringVar(&sourceID, "source", "", "source entity ID")
	cmd.Flags().StringVar(&targetID, "target", "", "target entity ID")
	cmd.Flags().StringVar(&relation, "relation", "", "relationship label, for example blocked_by or belongs_to")
	cmd.Flags().StringVar(&notes, "notes", "", "relationship notes")
	cmd.Flags().DurationVar(&timeout, "timeout", 10*time.Second, "request timeout")
	return cmd
}

func newMemoryEntityTouchCommand(ctx *commandContext, jsonOut *bool) *cobra.Command {
	var (
		peer       string
		id         string
		lastSeenAt int64
		timeout    time.Duration
	)
	cmd := &cobra.Command{
		Use:   "touch",
		Short: "Update the last-seen timestamp for an entity",
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
			if lastSeenAt > 0 {
				params["last_seen_at"] = lastSeenAt
			}
			var result map[string]any
			if err := client.rpc(cmd.Context(), peer, "memory.entity.touch", params, &result); err != nil {
				return err
			}
			emit(cmd, *jsonOut, result)
			return nil
		},
	}
	cmd.Flags().StringVar(&peer, "peer", "", "peer ID")
	cmd.Flags().StringVar(&id, "id", "", "entity ID")
	cmd.Flags().Int64Var(&lastSeenAt, "last-seen-at", 0, "last seen timestamp as Unix seconds")
	cmd.Flags().DurationVar(&timeout, "timeout", 10*time.Second, "request timeout")
	return cmd
}

func newMemoryEntityDeleteCommand(ctx *commandContext, jsonOut *bool) *cobra.Command {
	var (
		peer    string
		id      string
		timeout time.Duration
	)
	cmd := &cobra.Command{
		Use:   "delete",
		Short: "Delete an entity and remove its links and relationships",
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
			if err := client.rpc(cmd.Context(), peer, "memory.entity.delete", map[string]any{"id": id}, &result); err != nil {
				return err
			}
			emit(cmd, *jsonOut, result)
			return nil
		},
	}
	cmd.Flags().StringVar(&peer, "peer", "", "peer ID")
	cmd.Flags().StringVar(&id, "id", "", "entity ID")
	cmd.Flags().DurationVar(&timeout, "timeout", 10*time.Second, "request timeout")
	return cmd
}

func newMemoryEntityUnlinkChunkCommand(ctx *commandContext, jsonOut *bool) *cobra.Command {
	var (
		peer    string
		id      string
		chunkID string
		timeout time.Duration
	)
	cmd := &cobra.Command{
		Use:   "unlink-chunk",
		Short: "Remove a chunk link from an entity",
		RunE: func(cmd *cobra.Command, args []string) error {
			if peer == "" {
				return fmt.Errorf("--peer is required")
			}
			if id == "" {
				return fmt.Errorf("--id is required")
			}
			if chunkID == "" {
				return fmt.Errorf("--chunk-id is required")
			}
			state, err := ctx.state()
			if err != nil {
				return err
			}
			client := newGatewayClient(state, timeout)
			var result map[string]any
			if err := client.rpc(cmd.Context(), peer, "memory.entity.unlink_chunk", map[string]any{"id": id, "chunk_id": chunkID}, &result); err != nil {
				return err
			}
			emit(cmd, *jsonOut, result)
			return nil
		},
	}
	cmd.Flags().StringVar(&peer, "peer", "", "peer ID")
	cmd.Flags().StringVar(&id, "id", "", "entity ID")
	cmd.Flags().StringVar(&chunkID, "chunk-id", "", "memory chunk ID")
	cmd.Flags().DurationVar(&timeout, "timeout", 10*time.Second, "request timeout")
	return cmd
}

func newMemoryEntityUnrelateCommand(ctx *commandContext, jsonOut *bool) *cobra.Command {
	var (
		peer     string
		sourceID string
		targetID string
		relation string
		timeout  time.Duration
	)
	cmd := &cobra.Command{
		Use:   "unrelate",
		Short: "Remove a relationship edge between two entities",
		RunE: func(cmd *cobra.Command, args []string) error {
			if peer == "" {
				return fmt.Errorf("--peer is required")
			}
			if sourceID == "" {
				return fmt.Errorf("--source is required")
			}
			if targetID == "" {
				return fmt.Errorf("--target is required")
			}
			if relation == "" {
				return fmt.Errorf("--relation is required")
			}
			state, err := ctx.state()
			if err != nil {
				return err
			}
			client := newGatewayClient(state, timeout)
			var result map[string]any
			params := map[string]any{"source_id": sourceID, "target_id": targetID, "relation": relation}
			if err := client.rpc(cmd.Context(), peer, "memory.entity.unrelate", params, &result); err != nil {
				return err
			}
			emit(cmd, *jsonOut, result)
			return nil
		},
	}
	cmd.Flags().StringVar(&peer, "peer", "", "peer ID")
	cmd.Flags().StringVar(&sourceID, "source", "", "source entity ID")
	cmd.Flags().StringVar(&targetID, "target", "", "target entity ID")
	cmd.Flags().StringVar(&relation, "relation", "", "relationship label")
	cmd.Flags().DurationVar(&timeout, "timeout", 10*time.Second, "request timeout")
	return cmd
}

func newMemoryQueueCommand(ctx *commandContext, jsonOut *bool) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "queue",
		Short: "Review queue for candidate memories",
	}
	cmd.AddCommand(newMemoryQueueCreateCommand(ctx, jsonOut))
	cmd.AddCommand(newMemoryQueueListCommand(ctx, jsonOut))
	cmd.AddCommand(newMemoryQueueEditCommand(ctx, jsonOut))
	cmd.AddCommand(newMemoryQueueApproveCommand(ctx, jsonOut))
	cmd.AddCommand(newMemoryQueueMergeCommand(ctx, jsonOut))
	cmd.AddCommand(newMemoryQueueRejectCommand(ctx, jsonOut))
	return cmd
}

func newMemoryQueueCreateCommand(ctx *commandContext, jsonOut *bool) *cobra.Command {
	var (
		peer           string
		content        string
		tags           []string
		category       string
		retentionClass string
		exposurePolicy string
		expiresAt      int64
		timeout        time.Duration
	)
	cmd := &cobra.Command{
		Use:   "create",
		Short: "Queue a new candidate memory for review",
		RunE: func(cmd *cobra.Command, args []string) error {
			if peer == "" {
				return fmt.Errorf("--peer is required")
			}
			if strings.TrimSpace(content) == "" {
				return fmt.Errorf("--content is required")
			}
			state, err := ctx.state()
			if err != nil {
				return err
			}
			client := newGatewayClient(state, timeout)
			params := map[string]any{"content": content}
			if len(tags) > 0 {
				params["tags"] = tags
			}
			if category != "" {
				params["category"] = category
			}
			if retentionClass != "" {
				params["retention_class"] = retentionClass
			}
			if exposurePolicy != "" {
				params["exposure_policy"] = exposurePolicy
			}
			if expiresAt > 0 {
				params["expires_at"] = expiresAt
			}
			var result map[string]any
			if err := client.rpc(cmd.Context(), peer, "memory.candidate.create", params, &result); err != nil {
				return err
			}
			emit(cmd, *jsonOut, result)
			return nil
		},
	}
	cmd.Flags().StringVar(&peer, "peer", "", "peer ID")
	cmd.Flags().StringVar(&content, "content", "", "candidate memory content")
	cmd.Flags().StringSliceVar(&tags, "tags", nil, "candidate tags")
	cmd.Flags().StringVar(&category, "category", "", "candidate category")
	cmd.Flags().StringVar(&retentionClass, "retention-class", "", "retention class: working, pinned, archive")
	cmd.Flags().StringVar(&exposurePolicy, "exposure-policy", "", "exposure policy: auto, search_only")
	cmd.Flags().Int64Var(&expiresAt, "expires-at", 0, "expiry timestamp as Unix seconds")
	cmd.Flags().DurationVar(&timeout, "timeout", 10*time.Second, "request timeout")
	return cmd
}

func newMemoryQueueListCommand(ctx *commandContext, jsonOut *bool) *cobra.Command {
	var (
		peer    string
		status  string
		limit   int
		timeout time.Duration
	)
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List candidate memories awaiting or after review",
		RunE: func(cmd *cobra.Command, args []string) error {
			if peer == "" {
				return fmt.Errorf("--peer is required")
			}
			state, err := ctx.state()
			if err != nil {
				return err
			}
			client := newGatewayClient(state, timeout)
			params := map[string]any{"limit": limit, "status": status}
			var result map[string]any
			if err := client.rpc(cmd.Context(), peer, "memory.candidate.list", params, &result); err != nil {
				return err
			}
			if !*jsonOut {
				emit(cmd, false, formatMemoryQueueList(result))
				return nil
			}
			emit(cmd, true, result)
			return nil
		},
	}
	cmd.Flags().StringVar(&peer, "peer", "", "peer ID")
	cmd.Flags().StringVar(&status, "status", "pending", "candidate status filter: pending, approved, merged, rejected, all")
	cmd.Flags().IntVar(&limit, "limit", 50, "max candidates to return")
	cmd.Flags().DurationVar(&timeout, "timeout", 10*time.Second, "request timeout")
	return cmd
}

func addCandidatePatchFlags(cmd *cobra.Command, content *string, tags *[]string, category *string, retentionClass *string, exposurePolicy *string, expiresAt *int64) {
	cmd.Flags().StringVar(content, "content", "", "replacement content")
	cmd.Flags().StringSliceVar(tags, "tags", nil, "replacement tags")
	cmd.Flags().StringVar(category, "category", "", "replacement category")
	cmd.Flags().StringVar(retentionClass, "retention-class", "", "replacement retention class")
	cmd.Flags().StringVar(exposurePolicy, "exposure-policy", "", "replacement exposure policy")
	cmd.Flags().Int64Var(expiresAt, "expires-at", 0, "replacement expiry as Unix seconds")
}

func candidatePatchParams(cmd *cobra.Command, content string, tags []string, category string, retentionClass string, exposurePolicy string, expiresAt int64) map[string]any {
	params := map[string]any{}
	if cmd.Flags().Changed("content") {
		params["content"] = content
	}
	if cmd.Flags().Changed("tags") {
		params["tags"] = tags
	}
	if cmd.Flags().Changed("category") {
		params["category"] = category
	}
	if cmd.Flags().Changed("retention-class") {
		params["retention_class"] = retentionClass
	}
	if cmd.Flags().Changed("exposure-policy") {
		params["exposure_policy"] = exposurePolicy
	}
	if cmd.Flags().Changed("expires-at") {
		params["expires_at"] = expiresAt
	}
	return params
}

func formatMemoryQueueList(result map[string]any) string {
	count := intFromAny(result["count"])
	status := strings.TrimSpace(stringFromAny(result["status"]))
	if status == "" {
		status = string(memory.CandidateStatusPending)
	}
	manualCount := intFromAny(result["manual_count"])
	autoGeneratedCount := intFromAny(result["auto_generated_count"])

	var sb strings.Builder
	fmt.Fprintf(&sb, "Memory candidates (%s): %d total", status, count)
	if count > 0 {
		fmt.Fprintf(&sb, " | %d auto-generated | %d manual", autoGeneratedCount, manualCount)
	}

	candidates, _ := result["candidates"].([]any)
	for _, entry := range candidates {
		candidate, ok := entry.(map[string]any)
		if !ok {
			continue
		}
		preview := stringFromAny(candidate["content"])
		if len(preview) > 72 {
			preview = preview[:72] + "..."
		}
		fmt.Fprintf(&sb, "\n- %s [%s] %s", stringFromAny(candidate["id"]), stringFromAny(candidate["status"]), preview)
		if origin := formatMemoryQueueOrigin(candidate); origin != "" {
			fmt.Fprintf(&sb, "\n  source: %s", origin)
		}
	}

	if extra := formatCaptureKinds(result["capture_kinds"]); extra != "" {
		fmt.Fprintf(&sb, "\nCapture kinds: %s", extra)
	}
	return sb.String()
}

func formatMemoryQueueOrigin(candidate map[string]any) string {
	parts := make([]string, 0, 3)
	if label := captureKindLabel(stringFromAny(candidate["capture_kind"])); label != "" {
		parts = append(parts, label)
	}
	if sessionKey := stringFromAny(candidate["source_session_key"]); sessionKey != "" {
		parts = append(parts, sessionKey)
	}
	if excerpt := stringFromAny(candidate["source_excerpt"]); excerpt != "" {
		parts = append(parts, fmt.Sprintf("%q", excerpt))
	}
	return strings.Join(parts, " | ")
}

func formatCaptureKinds(value any) string {
	kinds, ok := value.(map[string]any)
	if !ok || len(kinds) == 0 {
		return ""
	}
	keys := make([]string, 0, len(kinds))
	for key := range kinds {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		parts = append(parts, fmt.Sprintf("%s=%d", captureKindLabel(key), intFromAny(kinds[key])))
	}
	return strings.Join(parts, ", ")
}

func captureKindLabel(kind string) string {
	switch strings.TrimSpace(kind) {
	case "", memory.CandidateCaptureManual:
		return "manual"
	case memory.CandidateCaptureAutoTurnExtract:
		return "auto-generated"
	default:
		return kind
	}
}

func stringFromAny(value any) string {
	text, _ := value.(string)
	return text
}

func intFromAny(value any) int {
	switch x := value.(type) {
	case int:
		return x
	case int32:
		return int(x)
	case int64:
		return int(x)
	case float64:
		return int(x)
	default:
		return 0
	}
}

func newMemoryQueueEditCommand(ctx *commandContext, jsonOut *bool) *cobra.Command {
	var (
		peer           string
		id             string
		content        string
		tags           []string
		category       string
		retentionClass string
		exposurePolicy string
		expiresAt      int64
		timeout        time.Duration
	)
	cmd := &cobra.Command{
		Use:   "edit",
		Short: "Edit a pending candidate memory",
		RunE: func(cmd *cobra.Command, args []string) error {
			if peer == "" {
				return fmt.Errorf("--peer is required")
			}
			if id == "" {
				return fmt.Errorf("--id is required")
			}
			params := candidatePatchParams(cmd, content, tags, category, retentionClass, exposurePolicy, expiresAt)
			params["id"] = id
			state, err := ctx.state()
			if err != nil {
				return err
			}
			client := newGatewayClient(state, timeout)
			var result map[string]any
			if err := client.rpc(cmd.Context(), peer, "memory.candidate.edit", params, &result); err != nil {
				return err
			}
			emit(cmd, *jsonOut, result)
			return nil
		},
	}
	cmd.Flags().StringVar(&peer, "peer", "", "peer ID")
	cmd.Flags().StringVar(&id, "id", "", "candidate ID")
	addCandidatePatchFlags(cmd, &content, &tags, &category, &retentionClass, &exposurePolicy, &expiresAt)
	cmd.Flags().DurationVar(&timeout, "timeout", 10*time.Second, "request timeout")
	return cmd
}

func newMemoryQueueApproveCommand(ctx *commandContext, jsonOut *bool) *cobra.Command {
	var (
		peer           string
		id             string
		reason         string
		content        string
		tags           []string
		category       string
		retentionClass string
		exposurePolicy string
		expiresAt      int64
		timeout        time.Duration
	)
	cmd := &cobra.Command{
		Use:   "approve",
		Short: "Approve a candidate memory into durable memory",
		RunE: func(cmd *cobra.Command, args []string) error {
			if peer == "" {
				return fmt.Errorf("--peer is required")
			}
			if id == "" {
				return fmt.Errorf("--id is required")
			}
			params := candidatePatchParams(cmd, content, tags, category, retentionClass, exposurePolicy, expiresAt)
			params["id"] = id
			if reason != "" {
				params["reason"] = reason
			}
			state, err := ctx.state()
			if err != nil {
				return err
			}
			client := newGatewayClient(state, timeout)
			var result map[string]any
			if err := client.rpc(cmd.Context(), peer, "memory.candidate.approve", params, &result); err != nil {
				return err
			}
			emit(cmd, *jsonOut, result)
			return nil
		},
	}
	cmd.Flags().StringVar(&peer, "peer", "", "peer ID")
	cmd.Flags().StringVar(&id, "id", "", "candidate ID")
	cmd.Flags().StringVar(&reason, "reason", "", "approval note")
	addCandidatePatchFlags(cmd, &content, &tags, &category, &retentionClass, &exposurePolicy, &expiresAt)
	cmd.Flags().DurationVar(&timeout, "timeout", 10*time.Second, "request timeout")
	return cmd
}

func newMemoryQueueMergeCommand(ctx *commandContext, jsonOut *bool) *cobra.Command {
	var (
		peer           string
		id             string
		mergeIntoID    string
		reason         string
		content        string
		tags           []string
		category       string
		retentionClass string
		exposurePolicy string
		expiresAt      int64
		timeout        time.Duration
	)
	cmd := &cobra.Command{
		Use:   "merge",
		Short: "Merge a candidate memory into an existing durable memory chunk",
		RunE: func(cmd *cobra.Command, args []string) error {
			if peer == "" {
				return fmt.Errorf("--peer is required")
			}
			if id == "" {
				return fmt.Errorf("--id is required")
			}
			if mergeIntoID == "" {
				return fmt.Errorf("--merge-into is required")
			}
			params := candidatePatchParams(cmd, content, tags, category, retentionClass, exposurePolicy, expiresAt)
			params["id"] = id
			params["merge_into_id"] = mergeIntoID
			if reason != "" {
				params["reason"] = reason
			}
			state, err := ctx.state()
			if err != nil {
				return err
			}
			client := newGatewayClient(state, timeout)
			var result map[string]any
			if err := client.rpc(cmd.Context(), peer, "memory.candidate.merge", params, &result); err != nil {
				return err
			}
			emit(cmd, *jsonOut, result)
			return nil
		},
	}
	cmd.Flags().StringVar(&peer, "peer", "", "peer ID")
	cmd.Flags().StringVar(&id, "id", "", "candidate ID")
	cmd.Flags().StringVar(&mergeIntoID, "merge-into", "", "existing chunk ID to merge into")
	cmd.Flags().StringVar(&reason, "reason", "", "merge note")
	addCandidatePatchFlags(cmd, &content, &tags, &category, &retentionClass, &exposurePolicy, &expiresAt)
	cmd.Flags().DurationVar(&timeout, "timeout", 10*time.Second, "request timeout")
	return cmd
}

func newMemoryQueueRejectCommand(ctx *commandContext, jsonOut *bool) *cobra.Command {
	var (
		peer    string
		id      string
		reason  string
		timeout time.Duration
	)
	cmd := &cobra.Command{
		Use:     "reject",
		Aliases: []string{"discard"},
		Short:   "Reject a candidate memory with a reason",
		RunE: func(cmd *cobra.Command, args []string) error {
			if peer == "" {
				return fmt.Errorf("--peer is required")
			}
			if id == "" {
				return fmt.Errorf("--id is required")
			}
			if strings.TrimSpace(reason) == "" {
				return fmt.Errorf("--reason is required")
			}
			state, err := ctx.state()
			if err != nil {
				return err
			}
			client := newGatewayClient(state, timeout)
			var result map[string]any
			if err := client.rpc(cmd.Context(), peer, "memory.candidate.reject", map[string]any{"id": id, "reason": reason}, &result); err != nil {
				return err
			}
			emit(cmd, *jsonOut, result)
			return nil
		},
	}
	cmd.Flags().StringVar(&peer, "peer", "", "peer ID")
	cmd.Flags().StringVar(&id, "id", "", "candidate ID")
	cmd.Flags().StringVar(&reason, "reason", "", "rejection reason")
	cmd.Flags().DurationVar(&timeout, "timeout", 10*time.Second, "request timeout")
	return cmd
}
