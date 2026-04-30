package cli

import (
	"context"
	"fmt"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/ffimnsr/koios/internal/config"
)

type execApprovalView struct {
	ID        string         `json:"id"`
	PeerID    string         `json:"peer_id"`
	Kind      string         `json:"kind"`
	Action    string         `json:"action"`
	Summary   string         `json:"summary,omitempty"`
	Reason    string         `json:"reason,omitempty"`
	Command   string         `json:"command,omitempty"`
	Workdir   string         `json:"workdir,omitempty"`
	Timeout   int            `json:"timeout_seconds,omitempty"`
	Metadata  map[string]any `json:"metadata,omitempty"`
	CreatedAt time.Time      `json:"created_at"`
	ExpiresAt time.Time      `json:"expires_at"`
}

func newExecCommand(ctx *commandContext) *cobra.Command {
	var peer string
	cmd := &cobra.Command{
		Use:   "exec",
		Short: "Manage exec approvals and exec policy allowlists",
		RunE: func(cmd *cobra.Command, args []string) error {
			return cmd.Help()
		},
	}
	cmd.PersistentFlags().StringVar(&peer, "peer", "", "peer that owns the exec approval requests")
	enableDerivedPeerDefault(cmd)
	cmd.AddCommand(newExecPendingCommand(ctx, &peer))
	cmd.AddCommand(newExecApproveCommand(ctx, &peer))
	cmd.AddCommand(newExecRejectCommand(ctx, &peer))
	cmd.AddCommand(newExecAllowlistCommand(ctx))
	return cmd
}

func newExecPendingCommand(ctx *commandContext, peer *string) *cobra.Command {
	var jsonOut bool
	var timeout time.Duration
	cmd := &cobra.Command{
		Use:   "pending",
		Short: "List pending exec approval requests",
		RunE: func(cmd *cobra.Command, args []string) error {
			state, err := ctx.state()
			if err != nil {
				return err
			}
			client := newGatewayClient(state, timeout)
			reqCtx, cancel := context.WithTimeout(cmd.Context(), timeout)
			defer cancel()
			var result struct {
				Pending []execApprovalView `json:"pending"`
			}
			if err := client.rpc(reqCtx, strings.TrimSpace(*peer), "exec.pending", nil, &result); err != nil {
				return err
			}
			if jsonOut {
				emit(cmd, true, map[string]any{
					"peer":          strings.TrimSpace(*peer),
					"pending_count": len(result.Pending),
					"pending":       result.Pending,
				})
				return nil
			}
			if len(result.Pending) == 0 {
				fmt.Fprintf(cmd.OutOrStdout(), "No pending exec approvals for peer %q.\n", strings.TrimSpace(*peer))
				return nil
			}
			for _, approval := range result.Pending {
				fmt.Fprintf(cmd.OutOrStdout(), "%s\n", approval.ID)
				fmt.Fprintf(cmd.OutOrStdout(), "  command: %s\n", firstNonEmpty(approval.Command, approval.Summary))
				if approval.Workdir != "" {
					fmt.Fprintf(cmd.OutOrStdout(), "  workdir: %s\n", approval.Workdir)
				}
				if approval.Timeout > 0 {
					fmt.Fprintf(cmd.OutOrStdout(), "  timeout: %ds\n", approval.Timeout)
				}
				if approval.Reason != "" {
					fmt.Fprintf(cmd.OutOrStdout(), "  reason: %s\n", approval.Reason)
				}
				if policy := metadataString(approval.Metadata, "policy"); policy != "" {
					fmt.Fprintf(cmd.OutOrStdout(), "  policy: %s\n", policy)
				}
				if mode := metadataString(approval.Metadata, "approval_mode"); mode != "" {
					fmt.Fprintf(cmd.OutOrStdout(), "  approval mode: %s\n", mode)
				}
				if pattern := metadataString(approval.Metadata, "matched_pattern"); pattern != "" {
					fmt.Fprintf(cmd.OutOrStdout(), "  matched pattern: %s\n", pattern)
				}
				fmt.Fprintf(cmd.OutOrStdout(), "  created: %s\n", approval.CreatedAt.Format(time.RFC3339))
				fmt.Fprintf(cmd.OutOrStdout(), "  expires: %s\n", approval.ExpiresAt.Format(time.RFC3339))
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&jsonOut, "json", false, "emit JSON")
	cmd.Flags().DurationVar(&timeout, "timeout", 5*time.Second, "gateway request timeout")
	return cmd
}

func newExecApproveCommand(ctx *commandContext, peer *string) *cobra.Command {
	var jsonOut bool
	var timeout time.Duration
	cmd := &cobra.Command{
		Use:   "approve <id>",
		Short: "Approve one pending exec request and run it",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			state, err := ctx.state()
			if err != nil {
				return err
			}
			client := newGatewayClient(state, timeout)
			reqCtx, cancel := context.WithTimeout(cmd.Context(), timeout)
			defer cancel()
			result := map[string]any{}
			if err := client.rpc(reqCtx, strings.TrimSpace(*peer), "exec.approve", map[string]any{"id": args[0]}, &result); err != nil {
				return err
			}
			if jsonOut {
				emit(cmd, true, result)
				return nil
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Approved exec request %s for peer %q.\n", args[0], strings.TrimSpace(*peer))
			if status := mapString(result, "status"); status != "" {
				fmt.Fprintf(cmd.OutOrStdout(), "status: %s\n", status)
			}
			if command := mapString(result, "command"); command != "" {
				fmt.Fprintf(cmd.OutOrStdout(), "command: %s\n", command)
			}
			if stdout := mapString(result, "stdout"); strings.TrimSpace(stdout) != "" {
				fmt.Fprintf(cmd.OutOrStdout(), "stdout:\n%s\n", stdout)
			}
			if stderr := mapString(result, "stderr"); strings.TrimSpace(stderr) != "" {
				fmt.Fprintf(cmd.OutOrStdout(), "stderr:\n%s\n", stderr)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&jsonOut, "json", false, "emit JSON")
	cmd.Flags().DurationVar(&timeout, "timeout", 30*time.Second, "gateway request timeout")
	return cmd
}

func newExecRejectCommand(ctx *commandContext, peer *string) *cobra.Command {
	var jsonOut bool
	var timeout time.Duration
	cmd := &cobra.Command{
		Use:   "reject <id>",
		Short: "Reject one pending exec request",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			state, err := ctx.state()
			if err != nil {
				return err
			}
			client := newGatewayClient(state, timeout)
			reqCtx, cancel := context.WithTimeout(cmd.Context(), timeout)
			defer cancel()
			result := map[string]any{}
			if err := client.rpc(reqCtx, strings.TrimSpace(*peer), "exec.reject", map[string]any{"id": args[0]}, &result); err != nil {
				return err
			}
			if jsonOut {
				emit(cmd, true, result)
				return nil
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Rejected exec request %s for peer %q.\n", args[0], strings.TrimSpace(*peer))
			return nil
		},
	}
	cmd.Flags().BoolVar(&jsonOut, "json", false, "emit JSON")
	cmd.Flags().DurationVar(&timeout, "timeout", 5*time.Second, "gateway request timeout")
	return cmd
}

func newExecAllowlistCommand(ctx *commandContext) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "allowlist",
		Short: "Inspect and edit regex patterns that bypass exec danger checks",
		Long:  "Manage tools.exec.custom_allow_patterns in koios.config.toml. Matching commands bypass the built-in dangerous-command checks before custom deny patterns are evaluated.",
	}
	cmd.AddCommand(newExecAllowlistListCommand(ctx))
	cmd.AddCommand(newExecAllowlistAddCommand(ctx))
	cmd.AddCommand(newExecAllowlistRemoveCommand(ctx))
	return cmd
}

func newExecAllowlistListCommand(ctx *commandContext) *cobra.Command {
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List configured exec allowlist regex patterns",
		RunE: func(cmd *cobra.Command, args []string) error {
			state, err := ctx.state()
			if err != nil {
				return err
			}
			patterns := append([]string(nil), state.Config.ExecCustomAllowPatterns...)
			if jsonOut {
				emit(cmd, true, map[string]any{
					"config_path": state.ConfigPath,
					"count":       len(patterns),
					"patterns":    patterns,
				})
				return nil
			}
			if len(patterns) == 0 {
				fmt.Fprintf(cmd.OutOrStdout(), "No exec allowlist patterns configured in %s.\n", state.ConfigPath)
				return nil
			}
			for _, pattern := range patterns {
				fmt.Fprintln(cmd.OutOrStdout(), pattern)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&jsonOut, "json", false, "emit JSON")
	return cmd
}

func newExecAllowlistAddCommand(ctx *commandContext) *cobra.Command {
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "add <pattern>",
		Short: "Add one exec allowlist regex pattern",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			pattern := strings.TrimSpace(args[0])
			if pattern == "" {
				return fmt.Errorf("pattern is required")
			}
			if _, err := regexp.Compile(pattern); err != nil {
				return fmt.Errorf("invalid regex pattern: %w", err)
			}
			state, err := ctx.state()
			if err != nil {
				return err
			}
			if !state.ConfigExists {
				return fmt.Errorf("config file not found; run `koios init` first")
			}
			oldLen := len(state.Config.ExecCustomAllowPatterns)
			cfg := state.Config
			added := appendIfMissing(cfg.ExecCustomAllowPatterns, pattern)
			cfg.ExecCustomAllowPatterns = added
			if err := writeConfigWithValidation(state.ConfigPath, cfg); err != nil {
				return err
			}
			payload := map[string]any{
				"config_path": state.ConfigPath,
				"pattern":     pattern,
				"patterns":    cfg.ExecCustomAllowPatterns,
				"changed":     len(added) != oldLen,
			}
			if jsonOut {
				emit(cmd, true, payload)
				return nil
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Added exec allowlist pattern %q.\n", pattern)
			return nil
		},
	}
	cmd.Flags().BoolVar(&jsonOut, "json", false, "emit JSON")
	return cmd
}

func newExecAllowlistRemoveCommand(ctx *commandContext) *cobra.Command {
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "remove <pattern>",
		Short: "Remove one exec allowlist regex pattern",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			pattern := strings.TrimSpace(args[0])
			if pattern == "" {
				return fmt.Errorf("pattern is required")
			}
			state, err := ctx.state()
			if err != nil {
				return err
			}
			if !state.ConfigExists {
				return fmt.Errorf("config file not found; run `koios init` first")
			}
			cfg := state.Config
			updated, removed := removeExactString(cfg.ExecCustomAllowPatterns, pattern)
			if !removed {
				return fmt.Errorf("exec allowlist pattern %q not found", pattern)
			}
			cfg.ExecCustomAllowPatterns = updated
			if err := writeConfigWithValidation(state.ConfigPath, cfg); err != nil {
				return err
			}
			payload := map[string]any{
				"config_path": state.ConfigPath,
				"pattern":     pattern,
				"patterns":    cfg.ExecCustomAllowPatterns,
				"changed":     true,
			}
			if jsonOut {
				emit(cmd, true, payload)
				return nil
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Removed exec allowlist pattern %q.\n", pattern)
			return nil
		},
	}
	cmd.Flags().BoolVar(&jsonOut, "json", false, "emit JSON")
	return cmd
}

func writeConfigWithValidation(path string, cfg *config.Config) error {
	content := config.EncodeTOML(cfg, false)
	tmpPath := path + ".tmp"
	if err := os.WriteFile(tmpPath, []byte(content), 0o600); err != nil {
		return fmt.Errorf("write config: %w", err)
	}
	if _, err := config.LoadFromPath(tmpPath); err != nil {
		_ = os.Remove(tmpPath)
		return err
	}
	if err := os.Rename(tmpPath, path); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("replace config: %w", err)
	}
	return nil
}

func appendIfMissing(items []string, value string) []string {
	for _, item := range items {
		if item == value {
			return append([]string(nil), items...)
		}
	}
	return append(append([]string(nil), items...), value)
}

func removeExactString(items []string, value string) ([]string, bool) {
	updated := make([]string, 0, len(items))
	removed := false
	for _, item := range items {
		if !removed && item == value {
			removed = true
			continue
		}
		updated = append(updated, item)
	}
	return updated, removed
}

func metadataString(metadata map[string]any, key string) string {
	if metadata == nil {
		return ""
	}
	value, _ := metadata[key].(string)
	return strings.TrimSpace(value)
}

func mapString(payload map[string]any, key string) string {
	value, _ := payload[key].(string)
	return value
}
