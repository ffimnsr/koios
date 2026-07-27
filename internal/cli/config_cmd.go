package cli

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/ffimnsr/koios/internal/config"
)

func newConfigCommand(ctx *commandContext) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "config",
		Short: "Inspect and validate koios.config.toml",
		RunE: func(cmd *cobra.Command, args []string) error {
			return cmd.Help()
		},
	}
	cmd.AddCommand(newConfigValidateCommand(ctx))
	cmd.AddCommand(newConfigLintCommand(ctx))
	return cmd
}

func newConfigValidateCommand(ctx *commandContext) *cobra.Command {
	var jsonOut bool
	cmd := &cobra.Command{
		Use:     "validate",
		Aliases: []string{"check"},
		Short:   "Strictly validate koios.config.toml",
		Long:    "Parse koios.config.toml using the strict schema loader and fail if unknown keys or invalid values are present.",
		RunE: func(cmd *cobra.Command, args []string) error {
			configPath := filepath.Join(ctx.cwdOrDefault(), config.DefaultConfigFile)
			cfg, err := config.LoadFromPath(configPath)
			if err != nil {
				if jsonOut {
					emit(cmd, true, map[string]any{
						"config_path": configPath,
						"valid":       false,
						"error":       err.Error(),
					})
				}
				return err
			}
			payload := map[string]any{
				"config_path": configPath,
				"valid":       true,
				"workspace":   cfg.WorkspaceRoot,
				"provider":    cfg.Provider,
				"model":       cfg.Model,
			}
			if jsonOut {
				emit(cmd, true, payload)
				return nil
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Config is valid: %s\n", configPath)
			return nil
		},
	}
	cmd.Flags().BoolVar(&jsonOut, "json", false, "emit JSON")
	return cmd
}

func newConfigLintCommand(ctx *commandContext) *cobra.Command {
	var jsonOut bool
	var fix bool
	var force bool
	cmd := &cobra.Command{
		Use:   "lint",
		Short: "Inspect config findings and optionally normalize repairable issues",
		Long:  "Report config-specific findings derived from koios.config.toml. Use --fix to rewrite repairable invalid settings into canonical values. Use --force with --fix to replace unreadable config files with defaults.",
		RunE: func(cmd *cobra.Command, args []string) error {
			state, err := resolveDoctorState(ctx.cwdOrDefault())
			if err != nil {
				return err
			}
			repairs := []string{}
			if fix {
				if state.ConfigLoadError != "" && !state.ConfigParsed {
					repairs, err = applyDoctorBrokenConfigRepair(state, nil, force)
				} else {
					repairs, err = repairDoctorConfig(state)
				}
				if err != nil {
					return err
				}
				state, err = resolveDoctorState(ctx.cwdOrDefault())
				if err != nil {
					return err
				}
			}
			findings := configOnlyFindings(state.validate())
			report := map[string]any{
				"config_path": state.ConfigPath,
				"summary":     summarizeDoctorFindings(findings, repairs),
				"findings":    findings,
				"repairs":     repairs,
			}
			if jsonOut {
				emit(cmd, true, report)
			} else {
				emit(cmd, false, report)
			}
			if len(findings) > 0 && summarizeDoctorFindings(findings, repairs).Errors > 0 {
				return fmt.Errorf("config lint found errors")
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&fix, "fix", false, "rewrite repairable config issues")
	cmd.Flags().BoolVar(&force, "force", false, "allow replacing unreadable config with defaults when used with --fix")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "emit JSON")
	return cmd
}

func configOnlyFindings(findings []doctorFinding) []doctorFinding {
	filtered := make([]doctorFinding, 0, len(findings))
	for _, finding := range findings {
		if strings.HasPrefix(finding.Key, "config") || strings.HasPrefix(finding.Key, "llm.") || strings.HasPrefix(finding.Key, "server.") || strings.HasPrefix(finding.Key, "session.") || strings.HasPrefix(finding.Key, "cron.") || strings.HasPrefix(finding.Key, "agent.") || strings.HasPrefix(finding.Key, "tools.") || strings.HasPrefix(finding.Key, "workspace.") || strings.HasPrefix(finding.Key, "memory.") || strings.HasPrefix(finding.Key, "compaction.") || strings.HasPrefix(finding.Key, "log.") || strings.HasPrefix(finding.Key, "monitor.") || strings.HasPrefix(finding.Key, "channels.") || strings.HasPrefix(finding.Key, "browser.") {
			filtered = append(filtered, finding)
		}
	}
	return filtered
}
