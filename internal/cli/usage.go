package cli

import (
	"context"
	"time"

	"github.com/spf13/cobra"
)

func newUsageCommand(ctx *commandContext) *cobra.Command {
	var jsonOut bool
	root := &cobra.Command{
		Use:   "usage",
		Short: "Query per-session token usage from the running gateway",
	}
	root.PersistentFlags().BoolVar(&jsonOut, "json", false, "emit JSON")

	root.AddCommand(newUsageListCommand(ctx, &jsonOut))
	root.AddCommand(newUsageTotalsCommand(ctx, &jsonOut))
	root.AddCommand(newUsageGetCommand(ctx, &jsonOut))
	return root
}

func newUsageListCommand(ctx *commandContext, jsonOut *bool) *cobra.Command {
	var peer string
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List token usage for all sessions",
		RunE: func(cmd *cobra.Command, args []string) error {
			state, err := ctx.state()
			if err != nil {
				return err
			}
			connPeer := peer
			if connPeer == "" {
				connPeer = "operator"
			}
			client := newGatewayClient(state, 5*time.Second)
			reqCtx, cancel := context.WithTimeout(cmd.Context(), 5*time.Second)
			defer cancel()
			var out map[string]any
			if err := client.rpc(reqCtx, connPeer, "usage.list", nil, &out); err != nil {
				return err
			}
			emit(cmd, *jsonOut, out)
			return nil
		},
	}
	cmd.Flags().StringVar(&peer, "peer", "", "peer to connect as (default: operator)")
	return cmd
}

func newUsageTotalsCommand(ctx *commandContext, jsonOut *bool) *cobra.Command {
	var peer string
	cmd := &cobra.Command{
		Use:   "totals",
		Short: "Show aggregate token usage across all sessions",
		RunE: func(cmd *cobra.Command, args []string) error {
			state, err := ctx.state()
			if err != nil {
				return err
			}
			connPeer := peer
			if connPeer == "" {
				connPeer = "operator"
			}
			client := newGatewayClient(state, 5*time.Second)
			reqCtx, cancel := context.WithTimeout(cmd.Context(), 5*time.Second)
			defer cancel()
			var out map[string]any
			if err := client.rpc(reqCtx, connPeer, "usage.totals", nil, &out); err != nil {
				return err
			}
			emit(cmd, *jsonOut, out)
			return nil
		},
	}
	cmd.Flags().StringVar(&peer, "peer", "", "peer to connect as (default: operator)")
	return cmd
}

func newUsageGetCommand(ctx *commandContext, jsonOut *bool) *cobra.Command {
	var peer string
	var targetPeer string
	cmd := &cobra.Command{
		Use:   "get",
		Short: "Get token usage for a specific peer/session",
		RunE: func(cmd *cobra.Command, args []string) error {
			state, err := ctx.state()
			if err != nil {
				return err
			}
			connPeer := peer
			if connPeer == "" {
				connPeer = "operator"
			}
			client := newGatewayClient(state, 5*time.Second)
			reqCtx, cancel := context.WithTimeout(cmd.Context(), 5*time.Second)
			defer cancel()
			params := map[string]any{}
			if targetPeer != "" {
				params["peer_id"] = targetPeer
			}
			var out map[string]any
			if err := client.rpc(reqCtx, connPeer, "usage.get", params, &out); err != nil {
				return err
			}
			emit(cmd, *jsonOut, out)
			return nil
		},
	}
	cmd.Flags().StringVar(&peer, "peer", "", "peer to connect as (default: operator)")
	cmd.Flags().StringVar(&targetPeer, "target", "", "peer ID to query usage for (default: connecting peer)")
	return cmd
}
