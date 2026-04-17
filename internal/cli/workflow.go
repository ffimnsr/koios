package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/ffimnsr/koios/internal/workflow"
)

func newWorkflowCommand(ctx *commandContext) *cobra.Command {
	var jsonOut bool
	root := &cobra.Command{
		Use:   "workflow",
		Short: "Manage and run multi-step workflows",
	}
	root.PersistentFlags().BoolVar(&jsonOut, "json", false, "emit JSON")
	root.AddCommand(newWorkflowListCommand(ctx, &jsonOut))
	root.AddCommand(newWorkflowCreateCommand(ctx, &jsonOut))
	root.AddCommand(newWorkflowGetCommand(ctx, &jsonOut))
	root.AddCommand(newWorkflowDeleteCommand(ctx, &jsonOut))
	root.AddCommand(newWorkflowRunCommand(ctx, &jsonOut))
	root.AddCommand(newWorkflowStatusCommand(ctx, &jsonOut))
	root.AddCommand(newWorkflowCancelCommand(ctx, &jsonOut))
	root.AddCommand(newWorkflowRunsCommand(ctx, &jsonOut))
	return root
}

// workflowRPC invokes a workflow.* JSON-RPC method on the gateway.
func (ctx *commandContext) workflowRPC(cmd *cobra.Command, peer, method string, params any, out any) error {
	state, err := ctx.state()
	if err != nil {
		return err
	}
	client := newGatewayClient(state, 10*time.Second)
	reqCtx, cancel := context.WithTimeout(cmd.Context(), 10*time.Second)
	defer cancel()
	return client.rpc(reqCtx, peer, method, params, out)
}

func newWorkflowListCommand(ctx *commandContext, jsonOut *bool) *cobra.Command {
	var peer string
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List workflow definitions for a peer",
		RunE: func(cmd *cobra.Command, args []string) error {
			if peer == "" {
				return errors.New("--peer is required")
			}
			var out map[string]any
			if err := ctx.workflowRPC(cmd, peer, "workflow.list", nil, &out); err != nil {
				return err
			}
			emit(cmd, *jsonOut, out)
			return nil
		},
	}
	cmd.Flags().StringVar(&peer, "peer", "", "peer that owns the workflows")
	return cmd
}

func newWorkflowCreateCommand(ctx *commandContext, jsonOut *bool) *cobra.Command {
	var (
		peer      string
		name      string
		desc      string
		firstStep string
		stepsJSON string
	)
	cmd := &cobra.Command{
		Use:   "create",
		Short: "Create a new workflow definition",
		RunE: func(cmd *cobra.Command, args []string) error {
			if peer == "" {
				return errors.New("--peer is required")
			}
			if name == "" {
				return errors.New("--name is required")
			}
			if stepsJSON == "" {
				return errors.New("--steps is required (JSON array of step objects)")
			}
			var steps []workflow.Step
			if err := json.Unmarshal([]byte(stepsJSON), &steps); err != nil {
				return fmt.Errorf("invalid --steps JSON: %w", err)
			}
			params := map[string]any{
				"name":        name,
				"description": desc,
				"first_step":  firstStep,
				"steps":       steps,
			}
			var out map[string]any
			if err := ctx.workflowRPC(cmd, peer, "workflow.create", params, &out); err != nil {
				return err
			}
			emit(cmd, *jsonOut, out)
			return nil
		},
	}
	cmd.Flags().StringVar(&peer, "peer", "", "peer that will own the workflow")
	cmd.Flags().StringVar(&name, "name", "", "workflow name")
	cmd.Flags().StringVar(&desc, "description", "", "optional description")
	cmd.Flags().StringVar(&firstStep, "first-step", "", "ID of the entry-point step (default: first step in list)")
	cmd.Flags().StringVar(&stepsJSON, "steps", "", "JSON array of step objects")
	return cmd
}

func newWorkflowGetCommand(ctx *commandContext, jsonOut *bool) *cobra.Command {
	var peer string
	cmd := &cobra.Command{
		Use:   "get <id>",
		Short: "Fetch one workflow definition",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if peer == "" {
				return errors.New("--peer is required")
			}
			var out map[string]any
			if err := ctx.workflowRPC(cmd, peer, "workflow.get", map[string]any{"id": args[0]}, &out); err != nil {
				return err
			}
			emit(cmd, *jsonOut, out)
			return nil
		},
	}
	cmd.Flags().StringVar(&peer, "peer", "", "peer that owns the workflow")
	return cmd
}

func newWorkflowDeleteCommand(ctx *commandContext, jsonOut *bool) *cobra.Command {
	var peer string
	cmd := &cobra.Command{
		Use:   "delete <id>",
		Short: "Delete a workflow definition",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if peer == "" {
				return errors.New("--peer is required")
			}
			var out map[string]any
			if err := ctx.workflowRPC(cmd, peer, "workflow.delete", map[string]any{"id": args[0]}, &out); err != nil {
				return err
			}
			emit(cmd, *jsonOut, out)
			return nil
		},
	}
	cmd.Flags().StringVar(&peer, "peer", "", "peer that owns the workflow")
	return cmd
}

func newWorkflowRunCommand(ctx *commandContext, jsonOut *bool) *cobra.Command {
	var peer string
	cmd := &cobra.Command{
		Use:   "run <workflow-id>",
		Short: "Start a new workflow run",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if peer == "" {
				return errors.New("--peer is required")
			}
			var out map[string]any
			if err := ctx.workflowRPC(cmd, peer, "workflow.run", map[string]any{"id": args[0]}, &out); err != nil {
				return err
			}
			emit(cmd, *jsonOut, out)
			return nil
		},
	}
	cmd.Flags().StringVar(&peer, "peer", "", "peer that owns the workflow")
	return cmd
}

func newWorkflowStatusCommand(ctx *commandContext, jsonOut *bool) *cobra.Command {
	var peer string
	cmd := &cobra.Command{
		Use:   "status <run-id>",
		Short: "Get the current status of a workflow run",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if peer == "" {
				return errors.New("--peer is required")
			}
			var out map[string]any
			if err := ctx.workflowRPC(cmd, peer, "workflow.status", map[string]any{"run_id": args[0]}, &out); err != nil {
				return err
			}
			// Pretty-print run status to stdout even in non-JSON mode.
			if !*jsonOut {
				if run, ok := out["run"].(map[string]any); ok {
					status := fmt.Sprint(run["status"])
					current := fmt.Sprint(run["current_step"])
					errMsg := fmt.Sprint(run["error"])
					var parts []string
					parts = append(parts, "status: "+status)
					if current != "" && current != "<nil>" && !strings.HasPrefix(status, "completed") && !strings.HasPrefix(status, "failed") && !strings.HasPrefix(status, "cancelled") {
						parts = append(parts, "current_step: "+current)
					}
					if errMsg != "" && errMsg != "<nil>" {
						parts = append(parts, "error: "+errMsg)
					}
					fmt.Fprintln(cmd.OutOrStdout(), strings.Join(parts, "\n"))
					return nil
				}
			}
			emit(cmd, *jsonOut, out)
			return nil
		},
	}
	cmd.Flags().StringVar(&peer, "peer", "", "peer that owns the run")
	return cmd
}

func newWorkflowCancelCommand(ctx *commandContext, jsonOut *bool) *cobra.Command {
	var peer string
	cmd := &cobra.Command{
		Use:   "cancel <run-id>",
		Short: "Cancel an active workflow run",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if peer == "" {
				return errors.New("--peer is required")
			}
			var out map[string]any
			if err := ctx.workflowRPC(cmd, peer, "workflow.cancel", map[string]any{"run_id": args[0]}, &out); err != nil {
				return err
			}
			emit(cmd, *jsonOut, out)
			return nil
		},
	}
	cmd.Flags().StringVar(&peer, "peer", "", "peer that owns the run")
	return cmd
}

func newWorkflowRunsCommand(ctx *commandContext, jsonOut *bool) *cobra.Command {
	var (
		peer  string
		limit int
	)
	cmd := &cobra.Command{
		Use:   "runs <workflow-id>",
		Short: "List recent runs for a workflow",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if peer == "" {
				return errors.New("--peer is required")
			}
			var out map[string]any
			if err := ctx.workflowRPC(cmd, peer, "workflow.runs",
				map[string]any{"id": args[0], "limit": limit}, &out); err != nil {
				return err
			}
			emit(cmd, *jsonOut, out)
			return nil
		},
	}
	cmd.Flags().StringVar(&peer, "peer", "", "peer that owns the workflow")
	cmd.Flags().IntVar(&limit, "limit", 10, "maximum number of runs to return")
	return cmd
}
