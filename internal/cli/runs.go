package cli

import (
	"context"
	"errors"
	"time"

	"github.com/spf13/cobra"
)

func newRunsCommand(ctx *commandContext) *cobra.Command {
	var jsonOut bool
	root := &cobra.Command{
		Use:   "runs",
		Short: "Query the unified background run ledger",
	}
	root.PersistentFlags().BoolVar(&jsonOut, "json", false, "emit JSON")
	root.AddCommand(newRunsListCommand(ctx, &jsonOut))
	root.AddCommand(newRunsGetCommand(ctx, &jsonOut))
	return root
}

func newRunsListCommand(ctx *commandContext, jsonOut *bool) *cobra.Command {
	var (
		peer   string
		conn   string
		kind   string
		status string
		limit  int
	)
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List recent background runs",
		RunE: func(cmd *cobra.Command, args []string) error {
			state, err := ctx.state()
			if err != nil {
				return err
			}
			connPeer := conn
			if connPeer == "" {
				connPeer = "operator"
			}
			client := newGatewayClient(state, 5*time.Second)
			reqCtx, cancel := context.WithTimeout(cmd.Context(), 5*time.Second)
			defer cancel()
			params := map[string]any{"limit": limit}
			if peer != "" {
				params["peer_id"] = peer
			}
			if kind != "" {
				params["kind"] = kind
			}
			if status != "" {
				params["status"] = status
			}
			var out map[string]any
			if err := client.rpc(reqCtx, connPeer, "runs.list", params, &out); err != nil {
				return err
			}
			emit(cmd, *jsonOut, out)
			return nil
		},
	}
	cmd.Flags().StringVar(&peer, "peer", "", "filter to a peer ID")
	cmd.Flags().StringVar(&conn, "conn-peer", "", "peer ID used to open the ws control connection")
	cmd.Flags().StringVar(&kind, "kind", "", "filter by kind: agent|subagent|orchestrator|cron")
	cmd.Flags().StringVar(&status, "status", "", "filter by status")
	cmd.Flags().IntVar(&limit, "limit", 100, "maximum records to return")
	return cmd
}

func newRunsGetCommand(ctx *commandContext, jsonOut *bool) *cobra.Command {
	var conn string
	cmd := &cobra.Command{
		Use:   "get <run-id>",
		Short: "Fetch one run ledger record",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if args[0] == "" {
				return errors.New("run id is required")
			}
			state, err := ctx.state()
			if err != nil {
				return err
			}
			connPeer := conn
			if connPeer == "" {
				connPeer = "operator"
			}
			client := newGatewayClient(state, 5*time.Second)
			reqCtx, cancel := context.WithTimeout(cmd.Context(), 5*time.Second)
			defer cancel()
			var out map[string]any
			if err := client.rpc(reqCtx, connPeer, "runs.get", map[string]any{"id": args[0]}, &out); err != nil {
				return err
			}
			emit(cmd, *jsonOut, out)
			return nil
		},
	}
	cmd.Flags().StringVar(&conn, "conn-peer", "", "peer ID used to open the ws control connection")
	return cmd
}
