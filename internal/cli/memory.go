package cli

import (
	"fmt"
	"time"

	"github.com/spf13/cobra"
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
			client := newDaemonClient(state, timeout)
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
			client := newDaemonClient(state, timeout)
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
			client := newDaemonClient(state, timeout)
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
