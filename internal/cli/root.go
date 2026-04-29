package cli

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/ffimnsr/koios/internal/app"
	"github.com/ffimnsr/koios/internal/config"
	"github.com/ffimnsr/koios/internal/scheduler"
)

type runGatewayFunc func(app.BuildInfo) error

type commandContext struct {
	build      app.BuildInfo
	runGateway runGatewayFunc
	cwd        string
}

func NewRootCommand(build app.BuildInfo, runGateway runGatewayFunc) *cobra.Command {
	ctx := &commandContext{build: build, runGateway: runGateway}
	root := &cobra.Command{
		Use:           "koios",
		Short:         "Koios gateway and operator CLI",
		SilenceErrors: true,
		SilenceUsage:  true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return cmd.Help()
		},
	}
	root.SetVersionTemplate("{{printf \"%s\\n\" .Version}}")
	root.Version = fmt.Sprintf("%s (%s) %s", build.Version, build.GitHash, build.BuildTime)
	root.PersistentFlags().BoolP("version", "v", false, "print version")
	root.PersistentPreRunE = func(cmd *cobra.Command, args []string) error {
		show, _ := cmd.Flags().GetBool("version")
		if show && cmd.Name() == "koios" {
			fmt.Fprintln(cmd.OutOrStdout(), root.Version)
			return flagOnlyVersionError{}
		}
		return nil
	}

	root.AddCommand(newServeCommand(ctx))
	root.AddCommand(newVersionCommand(ctx))
	root.AddCommand(newHealthCommand(ctx))
	root.AddCommand(newStatusCommand(ctx))
	root.AddCommand(newInitCommand(ctx))
	root.AddCommand(newSetupCommand(ctx))
	root.AddCommand(newDoctorCommand(ctx))
	root.AddCommand(newSessionsCommand(ctx))
	root.AddCommand(newBackupCommand(ctx))
	root.AddCommand(newResetCommand(ctx))
	root.AddCommand(newUpdateCommand(ctx))
	root.AddCommand(newAgentCommand(ctx))
	root.AddCommand(newCronCommand(ctx))
	root.AddCommand(newWorkflowCommand(ctx))
	root.AddCommand(newMigrateCommand(ctx))
	root.AddCommand(newUsageCommand(ctx))
	root.AddCommand(newRunsCommand(ctx))
	root.AddCommand(newBookmarkCommand(ctx))
	root.AddCommand(newMemoryCommand(ctx))
	root.AddCommand(newTasksCommand(ctx))
	root.AddCommand(newCalendarCommand(ctx))
	root.AddCommand(newWaitingCommand(ctx))
	root.AddCommand(newBriefCommand(ctx))
	root.AddCommand(newDashboardCommand(ctx))
	root.AddCommand(newModelCommand(ctx))
	root.AddCommand(newHostCommand(ctx))
	return root
}

type flagOnlyVersionError struct{}

func (flagOnlyVersionError) Error() string { return "" }

func newServeCommand(ctx *commandContext) *cobra.Command {
	return &cobra.Command{
		Use:   "serve",
		Short: "Start the Koios gateway",
		Long:  "Load koios.config.toml and start the WebSocket control-plane gateway.",
		RunE: func(cmd *cobra.Command, args []string) error {
			return ctx.runGateway(ctx.build)
		},
	}
}

func newVersionCommand(ctx *commandContext) *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print build metadata",
		RunE: func(cmd *cobra.Command, args []string) error {
			emit(cmd, false, ctx.build)
			return nil
		},
	}
}

func newHealthCommand(ctx *commandContext) *cobra.Command {
	var verbose bool
	var jsonOut bool
	var timeout time.Duration
	cmd := &cobra.Command{
		Use:   "health",
		Short: "Probe the running gateway health endpoint",
		RunE: func(cmd *cobra.Command, args []string) error {
			state, err := ctx.state()
			if err != nil {
				return err
			}
			client := newGatewayClient(state, timeout)
			reqCtx, cancel := context.WithTimeout(cmd.Context(), timeout)
			defer cancel()
			health, status, err := client.health(reqCtx)
			if err != nil {
				return err
			}
			payload := map[string]any{
				"url":         state.baseHTTPURL() + "/healthz",
				"http_status": status,
				"health":      health,
			}
			if verbose {
				version, _, err := client.version(reqCtx)
				if err == nil {
					payload["version"] = version
				}
				payload["listen_addr"] = state.ListenAddr
			}
			emit(cmd, jsonOut, payload)
			return nil
		},
	}
	cmd.Flags().BoolVar(&verbose, "verbose", false, "include version and connection details")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "emit JSON")
	cmd.Flags().DurationVar(&timeout, "timeout", 5*time.Second, "request timeout")
	return cmd
}

func newStatusCommand(ctx *commandContext) *cobra.Command {
	var deep bool
	var jsonOut bool
	var peer string
	cmd := &cobra.Command{
		Use:   "status",
		Short: "Show local configuration and gateway status",
		RunE: func(cmd *cobra.Command, args []string) error {
			state, err := ctx.state()
			if err != nil {
				return err
			}
			defaultPeer, err := defaultCLIPeerID()
			if err != nil {
				return err
			}
			payload := map[string]any{
				"build":           ctx.build,
				"config_exists":   state.ConfigExists,
				"listen_addr":     state.ListenAddr,
				"provider":        state.Provider,
				"model":           state.Model,
				"base_url":        state.BaseURL,
				"default_peer_id": defaultPeer,
				"paths":           statePaths(state),
				"enabled_systems": map[string]bool{
					"session_persistence": state.WorkspaceRoot != "",
					"cron":                state.WorkspaceRoot != "",
					"heartbeat":           state.WorkspaceRoot != "" && state.HeartbeatEnabled,
					"subagents":           state.WorkspaceRoot != "",
					"memory":              state.WorkspaceRoot != "",
					"bookmarks":           state.WorkspaceRoot != "",
					"tasks":               state.WorkspaceRoot != "",
					"calendar":            state.WorkspaceRoot != "",
					"workspace":           state.WorkspaceRoot != "",
				},
			}
			client := newGatewayClient(state, 3*time.Second)
			reqCtx, cancel := context.WithTimeout(cmd.Context(), 3*time.Second)
			defer cancel()
			if health, status, err := client.health(reqCtx); err == nil {
				payload["gateway"] = map[string]any{
					"reachable":   true,
					"http_status": status,
					"health":      health,
				}
				if version, _, err := client.version(reqCtx); err == nil {
					payload["gateway_version"] = version
				}
				if peer != "" {
					var caps map[string]any
					if err := client.rpc(reqCtx, peer, "server.capabilities", nil, &caps); err == nil {
						payload["peer_capabilities"] = caps
					}
				}
			} else {
				payload["gateway"] = map[string]any{
					"reachable": false,
					"error":     err.Error(),
				}
			}
			if deep {
				payload["doctor_findings"] = state.validate()
			}
			emit(cmd, jsonOut, payload)
			return nil
		},
	}
	cmd.Flags().BoolVar(&deep, "deep", false, "include extended diagnostics")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "emit JSON")
	cmd.Flags().StringVar(&peer, "peer", "", "peer to inspect with server.capabilities")
	return cmd
}

func newInitCommand(ctx *commandContext) *cobra.Command {
	var wizard bool
	var nonInteractive bool
	var setup bool
	cmd := &cobra.Command{
		Use:   "init",
		Short: "Generate koios.config.toml and state directories",
		RunE: func(cmd *cobra.Command, args []string) error {
			state, err := ctx.state()
			if err != nil {
				return err
			}
			if !state.ConfigExists {
				if wizard {
					content, err := initWizard(cmd, state)
					if err != nil {
						return err
					}
					if err := os.WriteFile(state.ConfigPath, []byte(content), 0o600); err != nil {
						return err
					}
				} else {
					if err := os.WriteFile(state.ConfigPath, []byte(config.DefaultTOML()), 0o600); err != nil {
						return err
					}
				}
			}
			state, err = ctx.state()
			if err != nil {
				return err
			}
			created := state.createStateDirs()
			if setup {
				if err := scaffoldWorkspace(state.Config.WorkspaceRoot, false); err != nil {
					return err
				}
			}
			result := map[string]any{
				"config_created": fileExists(state.ConfigPath),
				"config_path":    state.ConfigPath,
				"dirs_created":   created,
				"next_steps": []string{
					"edit koios.config.toml with your provider credentials",
					"run koios doctor to validate the setup",
					"run koios serve to start the gateway",
				},
			}
			if wizard && nonInteractive {
				result["mode"] = "wizard-non-interactive"
			}
			emit(cmd, false, result)
			return nil
		},
	}
	cmd.Flags().BoolVar(&wizard, "wizard", false, "run interactive init")
	cmd.Flags().BoolVar(&setup, "setup", false, "scaffold workspace identity files after config is written")
	cmd.Flags().BoolVar(&nonInteractive, "non-interactive", false, "disable prompts and use defaults")
	return cmd
}

func newDoctorCommand(ctx *commandContext) *cobra.Command {
	var repair bool
	var force bool
	var nonInteractive bool
	var deep bool
	var jsonOut bool
	cmd := &cobra.Command{
		Use:     "doctor",
		Aliases: []string{"fix"},
		Short:   "Run diagnostics and repair guidance for the local Koios setup",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runDoctorCommand(ctx, cmd, repair, force, nonInteractive, deep, jsonOut)
		},
	}
	cmd.Flags().BoolVar(&repair, "repair", false, "perform safe repairs")
	cmd.Flags().BoolVar(&repair, "fix", false, "perform safe repairs")
	cmd.Flags().BoolVar(&force, "force", false, "allow overwriting generated local artifacts")
	cmd.Flags().BoolVar(&nonInteractive, "non-interactive", false, "disable prompts")
	cmd.Flags().BoolVar(&deep, "deep", false, "include gateway checks")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "emit JSON")
	return cmd
}

func newSessionsCommand(ctx *commandContext) *cobra.Command {
	var peer string
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "sessions",
		Short: "List persisted sessions from session.dir",
		RunE: func(cmd *cobra.Command, args []string) error {
			state, err := ctx.state()
			if err != nil {
				return err
			}
			if state.WorkspaceRoot == "" {
				return errors.New("workspace.root is not configured; sessions are in-memory only")
			}
			sessions, err := listSessions(state.sessionDir(), peer)
			if err != nil {
				return err
			}
			emit(cmd, jsonOut, map[string]any{
				"session_dir": state.sessionDir(),
				"count":       len(sessions),
				"sessions":    sessions,
			})
			return nil
		},
	}
	cmd.Flags().StringVar(&peer, "peer", "", "filter by peer/session key")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "emit JSON")
	cmd.AddCommand(newSessionsResetCommand(ctx))
	return cmd
}

func newSessionsResetCommand(ctx *commandContext) *cobra.Command {
	var peer string
	var all bool
	var yes bool
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "reset",
		Short: "Reset one live session or remove all persisted sessions",
		RunE: func(cmd *cobra.Command, args []string) error {
			if peer == "" && !all {
				return errors.New("set --peer or --all")
			}
			if peer != "" && all {
				return errors.New("use either --peer or --all")
			}
			state, err := ctx.state()
			if err != nil {
				return err
			}
			if all {
				if !yes {
					return errors.New("sessions reset --all is destructive; rerun with --yes")
				}
				if state.WorkspaceRoot == "" {
					return errors.New("workspace.root is not configured")
				}
				sessions, err := listSessions(state.sessionDir(), "")
				if err != nil {
					return err
				}
				removed := make([]string, 0, len(sessions))
				for _, entry := range sessions {
					path := filepath.Join(state.sessionDir(), url.PathEscape(entry.PeerID)+".jsonl")
					if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
						return err
					}
					removed = append(removed, entry.PeerID)
				}
				emit(cmd, jsonOut, map[string]any{"removed": removed, "count": len(removed)})
				return nil
			}
			client := newGatewayClient(state, 5*time.Second)
			reqCtx, cancel := context.WithTimeout(cmd.Context(), 5*time.Second)
			defer cancel()
			var out map[string]any
			if err := client.rpc(reqCtx, peer, "session.reset", nil, &out); err != nil {
				return err
			}
			emit(cmd, jsonOut, map[string]any{"peer": peer, "result": out})
			return nil
		},
	}
	cmd.Flags().StringVar(&peer, "peer", "", "peer/session key to reset through the gateway")
	cmd.Flags().BoolVar(&all, "all", false, "delete all persisted sessions from session.dir")
	cmd.Flags().BoolVar(&yes, "yes", false, "confirm destructive action when using --all")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "emit JSON")
	return cmd
}

func newBackupCommand(ctx *commandContext) *cobra.Command {
	cmd := &cobra.Command{Use: "backup", Short: "Create and verify local backups"}
	var (
		output     string
		dryRun     bool
		verifyFlag bool
		jsonOut    bool
		onlyConfig bool
	)
	createCmd := &cobra.Command{
		Use:   "create",
		Short: "Create a backup archive",
		RunE: func(cmd *cobra.Command, args []string) error {
			state, err := ctx.state()
			if err != nil {
				return err
			}
			items := collectBackupItems(state, onlyConfig)
			if output == "" {
				output = filepath.Join(state.Root, "backups", "koios-backup-"+time.Now().UTC().Format("20060102-150405")+".tar.gz")
			}
			manifest := map[string]any{
				"created_at": time.Now().UTC().Format(time.RFC3339),
				"root":       state.Root,
				"items":      items,
			}
			if dryRun {
				emit(cmd, jsonOut, map[string]any{"output": output, "manifest": manifest})
				return nil
			}
			if err := os.MkdirAll(filepath.Dir(output), 0o755); err != nil {
				return err
			}
			if err := writeBackupArchive(output, manifest, items); err != nil {
				return err
			}
			payload := map[string]any{"output": output, "manifest": manifest}
			emit(cmd, jsonOut, payload)
			if verifyFlag {
				return verifyBackupArchive(output)
			}
			return nil
		},
	}
	createCmd.Flags().StringVar(&output, "output", "", "output archive path")
	createCmd.Flags().BoolVar(&dryRun, "dry-run", false, "preview archive contents")
	createCmd.Flags().BoolVar(&verifyFlag, "verify", false, "verify the archive after creation")
	createCmd.Flags().BoolVar(&jsonOut, "json", false, "emit JSON")
	createCmd.Flags().BoolVar(&onlyConfig, "only-config", false, "include config files only")

	var verifyJSON bool
	verifyCmd := &cobra.Command{
		Use:   "verify <archive>",
		Short: "Verify a backup archive",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			err := verifyBackupArchive(args[0])
			payload := map[string]any{"archive": args[0], "ok": err == nil}
			if err != nil {
				payload["error"] = err.Error()
			}
			emit(cmd, verifyJSON, payload)
			return err
		},
	}
	verifyCmd.Flags().BoolVar(&verifyJSON, "json", false, "emit JSON")
	cmd.AddCommand(createCmd, verifyCmd)
	return cmd
}

func newResetCommand(ctx *commandContext) *cobra.Command {
	var scope string
	var yes bool
	var nonInteractive bool
	var dryRun bool
	cmd := &cobra.Command{
		Use:   "reset",
		Short: "Remove local Koios config and state",
		RunE: func(cmd *cobra.Command, args []string) error {
			if nonInteractive && scope == "" {
				return errors.New("--scope is required with --non-interactive")
			}
			state, err := ctx.state()
			if err != nil {
				return err
			}
			paths, err := resetTargets(state, scope)
			if err != nil {
				return err
			}
			if dryRun {
				emit(cmd, false, map[string]any{"scope": scope, "paths": paths, "dry_run": true})
				return nil
			}
			if !yes && !nonInteractive {
				return errors.New("reset is destructive; rerun with --yes or --non-interactive")
			}
			for _, path := range paths {
				if err := os.RemoveAll(path); err != nil && !os.IsNotExist(err) {
					return err
				}
			}
			emit(cmd, false, map[string]any{"scope": scope, "removed": paths})
			return nil
		},
	}
	cmd.Flags().StringVar(&scope, "scope", "", "one of: config, config+creds+sessions, full")
	cmd.Flags().BoolVar(&yes, "yes", false, "confirm destructive action")
	cmd.Flags().BoolVar(&nonInteractive, "non-interactive", false, "disable prompts")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "preview removals")
	return cmd
}

func newUpdateCommand(ctx *commandContext) *cobra.Command {
	var dryRun bool
	var jsonOut bool
	root := &cobra.Command{
		Use:   "update",
		Short: "Show source-checkout update status",
		RunE: func(cmd *cobra.Command, args []string) error {
			status, err := gitStatus(ctx.cwdOrDefault())
			if err != nil {
				return err
			}
			payload := map[string]any{
				"status":   status,
				"dry_run":  dryRun,
				"strategy": "source checkout; fetch and fast-forward manually if desired",
			}
			if status["upstream"] != "" {
				payload["intended_flow"] = []string{"git fetch --all --tags", "git pull --ff-only"}
			}
			emit(cmd, jsonOut, payload)
			return nil
		},
	}
	root.Flags().BoolVar(&dryRun, "dry-run", false, "show intended update flow")
	root.Flags().BoolVar(&jsonOut, "json", false, "emit JSON")
	var statusJSON bool
	root.AddCommand(&cobra.Command{
		Use:   "status",
		Short: "Show git checkout update status",
		RunE: func(cmd *cobra.Command, args []string) error {
			status, err := gitStatus(ctx.cwdOrDefault())
			if err != nil {
				return err
			}
			emit(cmd, statusJSON, status)
			return nil
		},
	})
	root.Commands()[0].Flags().BoolVar(&statusJSON, "json", false, "emit JSON")
	return root
}

func newCronCommand(ctx *commandContext) *cobra.Command {
	var jsonOut bool
	root := &cobra.Command{Use: "cron", Short: "Manage cron jobs over the Koios WebSocket API"}
	root.PersistentFlags().BoolVar(&jsonOut, "json", false, "emit JSON")
	root.AddCommand(newCronStatusCommand(ctx, &jsonOut))
	root.AddCommand(newCronListCommand(ctx, &jsonOut))
	root.AddCommand(newCronAddCommand(ctx, &jsonOut))
	root.AddCommand(newCronEditCommand(ctx, &jsonOut))
	root.AddCommand(newCronDeleteCommand(ctx, &jsonOut))
	root.AddCommand(newCronEnableDisableCommand(ctx, &jsonOut, true))
	root.AddCommand(newCronEnableDisableCommand(ctx, &jsonOut, false))
	root.AddCommand(newCronRunsCommand(ctx, &jsonOut))
	root.AddCommand(newCronRunCommand(ctx, &jsonOut))
	return root
}

func newCronStatusCommand(ctx *commandContext, jsonOut *bool) *cobra.Command {
	var peer string
	cmd := &cobra.Command{
		Use:   "status",
		Short: "Show cron availability",
		RunE: func(cmd *cobra.Command, args []string) error {
			state, err := ctx.state()
			if err != nil {
				return err
			}
			payload := map[string]any{
				"cron_dir":        state.cronDir(),
				"locally_enabled": state.WorkspaceRoot != "",
			}
			if peer != "" {
				client := newGatewayClient(state, 5*time.Second)
				reqCtx, cancel := context.WithTimeout(cmd.Context(), 5*time.Second)
				defer cancel()
				var caps map[string]any
				if err := client.rpc(reqCtx, peer, "server.capabilities", nil, &caps); err == nil {
					payload["peer_capabilities"] = caps
				} else {
					payload["probe_error"] = err.Error()
				}
			}
			emit(cmd, *jsonOut, payload)
			return nil
		},
	}
	cmd.Flags().StringVar(&peer, "peer", "", "peer to probe")
	return cmd
}

func newCronListCommand(ctx *commandContext, jsonOut *bool) *cobra.Command {
	var peer string
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List cron jobs for a peer",
		RunE: func(cmd *cobra.Command, args []string) error {
			if peer == "" {
				return errors.New("--peer is required")
			}
			var jobs []scheduler.Job
			if err := ctx.cronRPC(cmd, peer, "cron.list", nil, &jobs); err != nil {
				return err
			}
			emit(cmd, *jsonOut, jobs)
			return nil
		},
	}
	enableDerivedPeerDefault(cmd)
	cmd.Flags().StringVar(&peer, "peer", "", "peer that owns the jobs")
	return cmd
}

func newCronAddCommand(ctx *commandContext, jsonOut *bool) *cobra.Command {
	var flags cronFlags
	cmd := &cobra.Command{
		Use:     "add",
		Aliases: []string{"create"},
		Short:   "Create a cron job",
		RunE: func(cmd *cobra.Command, args []string) error {
			if flags.Peer == "" {
				return errors.New("--peer is required")
			}
			params, err := flags.createParams()
			if err != nil {
				return err
			}
			var out map[string]any
			if err := ctx.cronRPC(cmd, flags.Peer, "cron.create", params, &out); err != nil {
				return err
			}
			emit(cmd, *jsonOut, out)
			return nil
		},
	}
	enableDerivedPeerDefault(cmd)
	flags.bind(cmd, false)
	return cmd
}

func newCronEditCommand(ctx *commandContext, jsonOut *bool) *cobra.Command {
	var flags cronFlags
	cmd := &cobra.Command{
		Use:   "edit <id>",
		Short: "Patch a cron job",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if flags.Peer == "" {
				return errors.New("--peer is required")
			}
			flags.EnabledChanged = cmd.Flags().Changed("enabled")
			flags.DeleteChanged = cmd.Flags().Changed("delete-after-run")
			params, err := flags.updateParams(args[0])
			if err != nil {
				return err
			}
			var out map[string]any
			if err := ctx.cronRPC(cmd, flags.Peer, "cron.update", params, &out); err != nil {
				return err
			}
			emit(cmd, *jsonOut, out)
			return nil
		},
	}
	enableDerivedPeerDefault(cmd)
	flags.bind(cmd, true)
	return cmd
}

func newCronDeleteCommand(ctx *commandContext, jsonOut *bool) *cobra.Command {
	var peer string
	cmd := &cobra.Command{
		Use:     "rm <id>",
		Aliases: []string{"remove", "delete"},
		Short:   "Delete a cron job",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if peer == "" {
				return errors.New("--peer is required")
			}
			var out map[string]any
			if err := ctx.cronRPC(cmd, peer, "cron.delete", map[string]any{"id": args[0]}, &out); err != nil {
				return err
			}
			emit(cmd, *jsonOut, out)
			return nil
		},
	}
	enableDerivedPeerDefault(cmd)
	cmd.Flags().StringVar(&peer, "peer", "", "peer that owns the job")
	return cmd
}

func newCronEnableDisableCommand(ctx *commandContext, jsonOut *bool, enabled bool) *cobra.Command {
	name := "disable"
	short := "Disable a cron job"
	if enabled {
		name = "enable"
		short = "Enable a cron job"
	}
	var peer string
	cmd := &cobra.Command{
		Use:   name + " <id>",
		Short: short,
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if peer == "" {
				return errors.New("--peer is required")
			}
			var out map[string]any
			params := map[string]any{"id": args[0], "enabled": enabled}
			if err := ctx.cronRPC(cmd, peer, "cron.update", params, &out); err != nil {
				return err
			}
			emit(cmd, *jsonOut, out)
			return nil
		},
	}
	enableDerivedPeerDefault(cmd)
	cmd.Flags().StringVar(&peer, "peer", "", "peer that owns the job")
	return cmd
}

func newCronRunsCommand(ctx *commandContext, jsonOut *bool) *cobra.Command {
	var peer, id string
	var limit int
	cmd := &cobra.Command{
		Use:   "runs",
		Short: "List run history for a cron job",
		RunE: func(cmd *cobra.Command, args []string) error {
			if id == "" {
				return errors.New("--id is required")
			}
			var out []scheduler.RunRecord
			if err := ctx.cronRPC(cmd, peer, "cron.runs", map[string]any{"id": id, "limit": limit}, &out); err != nil {
				return err
			}
			emit(cmd, *jsonOut, out)
			return nil
		},
	}
	enableDerivedPeerDefault(cmd)
	cmd.Flags().StringVar(&peer, "peer", "", "peer that owns the job")
	cmd.Flags().StringVar(&id, "id", "", "job id")
	cmd.Flags().IntVar(&limit, "limit", 50, "max run records")
	return cmd
}

func newCronRunCommand(ctx *commandContext, jsonOut *bool) *cobra.Command {
	var peer string
	cmd := &cobra.Command{
		Use:   "run <id>",
		Short: "Trigger an immediate cron run",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if peer == "" {
				return errors.New("--peer is required")
			}
			var out map[string]any
			if err := ctx.cronRPC(cmd, peer, "cron.trigger", map[string]any{"id": args[0]}, &out); err != nil {
				return err
			}
			emit(cmd, *jsonOut, out)
			return nil
		},
	}
	enableDerivedPeerDefault(cmd)
	cmd.Flags().StringVar(&peer, "peer", "", "peer that owns the job")
	return cmd
}

type cronFlags struct {
	Peer           string
	Name           string
	Description    string
	At             string
	Every          string
	CronExpr       string
	TZ             string
	SystemEvent    string
	Message        string
	Enabled        bool
	EnabledChanged bool
	DeleteAfterRun bool
	DeleteChanged  bool
	IncludeHistory bool
}

func (f *cronFlags) bind(cmd *cobra.Command, edit bool) {
	cmd.Flags().StringVar(&f.Peer, "peer", "", "peer that owns the job")
	cmd.Flags().StringVar(&f.Name, "name", "", "job name")
	cmd.Flags().StringVar(&f.Description, "description", "", "job description")
	cmd.Flags().StringVar(&f.At, "at", "", "one-shot run time in RFC3339")
	cmd.Flags().StringVar(&f.Every, "every", "", "fixed interval (Go duration)")
	cmd.Flags().StringVar(&f.CronExpr, "cron", "", "5-field cron expression")
	cmd.Flags().StringVar(&f.TZ, "tz", "", "IANA timezone for cron schedules")
	cmd.Flags().StringVar(&f.SystemEvent, "system-event", "", "system event payload text")
	cmd.Flags().StringVar(&f.Message, "message", "", "agentTurn payload message")
	cmd.Flags().BoolVar(&f.Enabled, "enabled", true, "enable the job")
	cmd.Flags().BoolVar(&f.DeleteAfterRun, "delete-after-run", false, "delete after a one-shot run")
	cmd.Flags().BoolVar(&f.IncludeHistory, "include-history", false, "include session history for agentTurn payloads")
	if edit {
		cmd.Flags().Lookup("enabled").NoOptDefVal = "true"
		cmd.Flags().Lookup("delete-after-run").NoOptDefVal = "true"
	}
}

func (f *cronFlags) createParams() (map[string]any, error) {
	if strings.TrimSpace(f.Name) == "" {
		return nil, errors.New("--name is required")
	}
	schedule, err := f.schedule(true)
	if err != nil {
		return nil, err
	}
	payload, err := f.payload(true)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"name":             f.Name,
		"description":      f.Description,
		"schedule":         schedule,
		"payload":          payload,
		"enabled":          f.Enabled,
		"delete_after_run": f.DeleteAfterRun,
	}, nil
}

func (f *cronFlags) updateParams(id string) (map[string]any, error) {
	params := map[string]any{"id": id}
	if f.Name != "" {
		params["name"] = f.Name
	}
	if f.Description != "" {
		params["description"] = f.Description
	}
	if f.Enabled || f.EnabledChanged {
		params["enabled"] = f.Enabled
	}
	if f.DeleteAfterRun || f.DeleteChanged {
		params["delete_after_run"] = f.DeleteAfterRun
	}
	if schedule, err := f.schedule(false); err != nil {
		return nil, err
	} else if schedule != nil {
		params["schedule"] = schedule
	}
	if payload, err := f.payload(false); err != nil {
		return nil, err
	} else if payload != nil {
		params["payload"] = payload
	}
	return params, nil
}

func (f *cronFlags) schedule(required bool) (map[string]any, error) {
	count := 0
	if f.At != "" {
		count++
	}
	if f.Every != "" {
		count++
	}
	if f.CronExpr != "" {
		count++
	}
	if required && count != 1 {
		return nil, errors.New("exactly one of --at, --every, or --cron is required")
	}
	if !required && count == 0 {
		return nil, nil
	}
	if count > 1 {
		return nil, errors.New("only one of --at, --every, or --cron may be set")
	}
	switch {
	case f.At != "":
		if _, err := time.Parse(time.RFC3339, f.At); err != nil {
			return nil, fmt.Errorf("invalid --at value: %w", err)
		}
		return map[string]any{"kind": "at", "at": f.At}, nil
	case f.Every != "":
		d, err := time.ParseDuration(f.Every)
		if err != nil {
			return nil, fmt.Errorf("invalid --every value: %w", err)
		}
		return map[string]any{"kind": "every", "every_ms": d.Milliseconds()}, nil
	default:
		out := map[string]any{"kind": "cron", "expr": f.CronExpr}
		if f.TZ != "" {
			out["tz"] = f.TZ
		}
		return out, nil
	}
}

func (f *cronFlags) payload(required bool) (map[string]any, error) {
	count := 0
	if f.SystemEvent != "" {
		count++
	}
	if f.Message != "" {
		count++
	}
	if required && count != 1 {
		return nil, errors.New("exactly one of --system-event or --message is required")
	}
	if !required && count == 0 {
		return nil, nil
	}
	if count > 1 {
		return nil, errors.New("only one of --system-event or --message may be set")
	}
	if f.SystemEvent != "" {
		return map[string]any{"kind": "systemEvent", "text": f.SystemEvent}, nil
	}
	return map[string]any{"kind": "agentTurn", "message": f.Message, "include_history": f.IncludeHistory}, nil
}

func (ctx *commandContext) state() (*repoState, error) {
	return resolveRepoState(ctx.cwdOrDefault())
}

func (ctx *commandContext) cwdOrDefault() string {
	if ctx.cwd != "" {
		return ctx.cwd
	}
	cwd, err := os.Getwd()
	if err != nil {
		return "."
	}
	return cwd
}

func (ctx *commandContext) cronRPC(cmd *cobra.Command, peer, method string, params any, out any) error {
	state, err := ctx.state()
	if err != nil {
		return err
	}
	client := newGatewayClient(state, 10*time.Second)
	reqCtx, cancel := context.WithTimeout(cmd.Context(), 10*time.Second)
	defer cancel()
	return client.rpc(reqCtx, peer, method, params, out)
}

func emit(cmd *cobra.Command, jsonOut bool, v any) {
	if jsonOut {
		data, _ := json.MarshalIndent(v, "", "  ")
		fmt.Fprintln(cmd.OutOrStdout(), string(data))
		return
	}
	switch x := v.(type) {
	case string:
		fmt.Fprintln(cmd.OutOrStdout(), x)
	default:
		data, _ := json.MarshalIndent(v, "", "  ")
		fmt.Fprintln(cmd.OutOrStdout(), string(data))
	}
}

func statePaths(state *repoState) map[string]string {
	return map[string]string{
		"config":      state.ConfigPath,
		"sessionDir":  state.sessionDir(),
		"cronDir":     state.cronDir(),
		"agentDir":    state.agentDir(),
		"workflowDir": state.workflowDir(),
		"runsDir":     state.runsDir(),
		"memoryDB":    state.memoryDBPath(),
		"tasksDB":     state.tasksDBPath(),
		"calendarDB":  state.calendarDBPath(),
		"workspace":   state.WorkspaceRoot,
	}
}

type sessionEntry struct {
	PeerID       string    `json:"peer_id"`
	SessionKey   string    `json:"session_key"`
	FilePath     string    `json:"file_path"`
	MessageCount int       `json:"message_count"`
	ModifiedAt   time.Time `json:"modified_at"`
}

func listSessions(dir, peer string) ([]sessionEntry, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	out := []sessionEntry{}
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".jsonl" {
			continue
		}
		key := decodeSessionName(entry.Name())
		if peer != "" && key != peer {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		count, err := countJSONLLines(path)
		if err != nil {
			return nil, err
		}
		info, err := entry.Info()
		if err != nil {
			return nil, err
		}
		out = append(out, sessionEntry{
			PeerID:       key,
			SessionKey:   key,
			FilePath:     path,
			MessageCount: count,
			ModifiedAt:   info.ModTime(),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].SessionKey < out[j].SessionKey })
	return out, nil
}

func countJSONLLines(path string) (int, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	buf := make([]byte, 32*1024)
	count := 0
	lineSep := []byte{'\n'}
	for {
		n, err := f.Read(buf)
		count += strings.Count(string(buf[:n]), string(lineSep))
		if err == io.EOF {
			return count, nil
		}
		if err != nil {
			return 0, err
		}
	}
}

func collectBackupItems(state *repoState, onlyConfig bool) []string {
	items := []string{}
	for _, candidate := range []string{state.ConfigPath, filepath.Join(state.Root, "VERSION")} {
		if fileExists(candidate) {
			items = append(items, candidate)
		}
	}
	if onlyConfig {
		return items
	}
	for _, candidate := range []string{state.sessionDir(), state.cronDir(), state.agentDir(), state.memoryDBPath(), state.tasksDBPath(), state.calendarDBPath(), state.WorkspaceRoot} {
		if candidate == "" {
			continue
		}
		if fileExists(candidate) || dirExists(candidate) {
			items = append(items, candidate)
		}
	}
	sort.Strings(items)
	return items
}

func writeBackupArchive(output string, manifest map[string]any, items []string) error {
	f, err := os.Create(output)
	if err != nil {
		return err
	}
	defer f.Close()
	gz := gzip.NewWriter(f)
	defer gz.Close()
	tw := tar.NewWriter(gz)
	defer tw.Close()

	manifestBytes, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return err
	}
	if err := writeTarFile(tw, "manifest.json", manifestBytes, 0o644, time.Now()); err != nil {
		return err
	}

	for _, item := range items {
		if err := filepath.Walk(item, func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return err
			}
			rel := strings.TrimPrefix(path, string(filepath.Separator))
			if info.IsDir() {
				hdr, err := tar.FileInfoHeader(info, "")
				if err != nil {
					return err
				}
				hdr.Name = rel + "/"
				return tw.WriteHeader(hdr)
			}
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			return writeTarFile(tw, rel, data, info.Mode(), info.ModTime())
		}); err != nil {
			return err
		}
	}
	return nil
}

func writeTarFile(tw *tar.Writer, name string, data []byte, mode os.FileMode, modTime time.Time) error {
	hdr := &tar.Header{Name: name, Mode: int64(mode.Perm()), Size: int64(len(data)), ModTime: modTime}
	if err := tw.WriteHeader(hdr); err != nil {
		return err
	}
	_, err := tw.Write(data)
	return err
}

func verifyBackupArchive(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return err
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	seenManifest := false
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		if hdr.Name == "manifest.json" {
			seenManifest = true
			var body map[string]any
			if err := json.NewDecoder(tr).Decode(&body); err != nil {
				return fmt.Errorf("invalid manifest: %w", err)
			}
		}
	}
	if !seenManifest {
		return errors.New("manifest.json not found")
	}
	return nil
}

func resetTargets(state *repoState, scope string) ([]string, error) {
	switch scope {
	case "config":
		return []string{state.ConfigPath}, nil
	case "config+creds+sessions":
		out := []string{state.ConfigPath}
		for _, p := range []string{state.sessionDir(), state.cronDir(), state.agentDir(), state.WorkspaceRoot} {
			if p != "" {
				out = append(out, p)
			}
		}
		return out, nil
	case "full":
		out, _ := resetTargets(state, "config+creds+sessions")
		if dbPath := state.memoryDBPath(); dbPath != "" {
			out = append(out, dbPath)
		}
		if dbPath := state.tasksDBPath(); dbPath != "" {
			out = append(out, dbPath)
		}
		if dbPath := state.calendarDBPath(); dbPath != "" {
			out = append(out, dbPath)
		}
		return out, nil
	default:
		return nil, errors.New("invalid --scope; use config, config+creds+sessions, or full")
	}
}

func gitStatus(cwd string) (map[string]any, error) {
	run := func(args ...string) string {
		cmd := exec.Command("git", args...)
		cmd.Dir = cwd
		out, err := cmd.Output()
		if err != nil {
			return ""
		}
		return strings.TrimSpace(string(out))
	}
	status := map[string]any{
		"branch": run("rev-parse", "--abbrev-ref", "HEAD"),
		"sha":    run("rev-parse", "--short", "HEAD"),
	}
	status["dirty"] = run("status", "--porcelain") != ""
	status["upstream"] = run("rev-parse", "--abbrev-ref", "--symbolic-full-name", "@{u}")
	if up := status["upstream"].(string); up != "" {
		ab := run("rev-list", "--left-right", "--count", "HEAD...@{u}")
		parts := strings.Fields(ab)
		if len(parts) == 2 {
			status["ahead"] = parts[0]
			status["behind"] = parts[1]
		}
	}
	return status, nil
}

func initWizard(cmd *cobra.Command, state *repoState) (string, error) {
	prompt := func(label, fallback string) (string, error) {
		fmt.Fprintf(cmd.OutOrStdout(), "%s", label)
		if fallback != "" {
			fmt.Fprintf(cmd.OutOrStdout(), " [%s]", fallback)
		}
		fmt.Fprint(cmd.OutOrStdout(), ": ")
		var value string
		if _, err := fmt.Fscanln(cmd.InOrStdin(), &value); err != nil {
			if errors.Is(err, io.EOF) {
				return fallback, nil
			}
			return "", err
		}
		if strings.TrimSpace(value) == "" {
			return fallback, nil
		}
		return value, nil
	}
	apiKey, err := prompt("llm.api_key (optional)", state.APIKey)
	if err != nil {
		return "", err
	}
	model, err := prompt("llm.model", state.Model)
	if err != nil {
		return "", err
	}
	provider, err := prompt("llm.provider", "openai")
	if err != nil {
		return "", err
	}
	listenAddr, err := prompt("server.listen_addr", state.ListenAddr)
	if err != nil {
		return "", err
	}
	sessionDir, err := prompt("session.dir", "./data/sessions")
	if err != nil {
		return "", err
	}
	cronDir, err := prompt("cron.dir", "./data/cron")
	if err != nil {
		return "", err
	}
	agentDir, err := prompt("agent.dir", "./data/agents")
	if err != nil {
		return "", err
	}
	workspaceRoot, err := prompt("workspace.root", "./workspace")
	if err != nil {
		return "", err
	}
	apiKeyLine := "# api_key = \"\""
	if strings.TrimSpace(apiKey) != "" {
		apiKeyLine = "api_key = " + strconv.Quote(apiKey)
	}
	return fmt.Sprintf(initWizardConfigTemplate,
		strconv.Quote(listenAddr),
		strconv.Quote(provider),
		strconv.Quote(model),
		apiKeyLine,
		strconv.Quote(sessionDir),
		strconv.Quote(cronDir),
		strconv.Quote(agentDir),
		strconv.Quote(workspaceRoot),
	), nil
}

func mapPaths(paths []string, prefix string) []string {
	out := make([]string, 0, len(paths))
	for _, path := range paths {
		out = append(out, prefix+path)
	}
	return out
}

// scaffoldWorkspace creates workspace subdirectories and starter workspace docs.
// If force is true, existing files are overwritten.
func scaffoldWorkspace(root string, force bool) error {
	if root == "" {
		return fmt.Errorf("workspace.root is not configured")
	}
	subdirs := []string{"sessions", "cron", "agents", "memory"}
	for _, sub := range subdirs {
		if err := os.MkdirAll(filepath.Join(root, sub), 0o755); err != nil {
			return fmt.Errorf("creating %s/: %w", sub, err)
		}
	}
	templates := []struct {
		name    string
		content string
	}{
		{name: "AGENTS.md", content: scaffoldAgentsTemplate},
		{name: "SOUL.md", content: scaffoldSoulTemplate},
		{name: "USER.md", content: scaffoldUserTemplate},
		{name: "IDENTITY.md", content: scaffoldIdentityTemplate},
		{name: "BOOTSTRAP.md", content: scaffoldBootstrapTemplate},
		{name: "TOOLS.md", content: scaffoldToolsTemplate},
		{name: "HEARTBEAT.md", content: scaffoldHeartbeatTemplate},
	}
	for _, tpl := range templates {
		path := filepath.Join(root, tpl.name)
		if !force {
			if _, err := os.Stat(path); err == nil {
				continue // already exists, skip
			}
		}
		if err := os.WriteFile(path, []byte(tpl.content), 0o644); err != nil {
			return fmt.Errorf("writing %s: %w", tpl.name, err)
		}
	}
	return nil
}

func newSetupCommand(ctx *commandContext) *cobra.Command {
	var force bool
	cmd := &cobra.Command{
		Use:   "setup",
		Short: "Scaffold workspace directories and starter workspace docs",
		RunE: func(cmd *cobra.Command, args []string) error {
			state, err := ctx.state()
			if err != nil {
				return err
			}
			if !state.ConfigExists {
				return fmt.Errorf("config not found at %s; run `koios init` first", state.ConfigPath)
			}
			if err := scaffoldWorkspace(state.Config.WorkspaceRoot, force); err != nil {
				return err
			}
			emit(cmd, false, map[string]any{
				"workspace_root": state.Config.WorkspaceRoot,
				"status":         "ok",
			})
			return nil
		},
		Args: cobra.NoArgs,
	}
	cmd.Flags().BoolVar(&force, "force", false, "overwrite existing starter workspace files")
	return cmd
}
