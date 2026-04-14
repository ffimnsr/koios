package cli

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/ffimnsr/koios/internal/config"
)

// configMigration describes one schema migration step: a key that used to
// appear in older config files and the plain-English action taken.
type configMigration struct {
	OldKey  string `json:"old_key"`
	NewKey  string `json:"new_key"`
	Action  string `json:"action"`
	Applied bool   `json:"applied"`
}

// detectMigrations scans raw TOML bytes for deprecated keys and returns a list
// of migration notes. Migrations are non-destructive; the canonical rewrite via
// config.EncodeTOML handles the actual transform.
func detectMigrations(raw []byte) []configMigration {
	content := string(raw)
	var migrations []configMigration

	// v0 → v1: top-level `listen_addr` was moved under [server].
	if hasTopLevelKey(content, "listen_addr") && !strings.Contains(content, "[server]") {
		migrations = append(migrations, configMigration{
			OldKey: "listen_addr",
			NewKey: "server.listen_addr",
			Action: "moved under [server] section",
		})
	}
	// v0 → v1: top-level `api_key` was moved under [llm].
	if hasTopLevelKey(content, "api_key") && !strings.Contains(content, "[llm]") {
		migrations = append(migrations, configMigration{
			OldKey: "api_key",
			NewKey: "llm.api_key",
			Action: "moved under [llm] section",
		})
	}
	// v0 → v1: top-level `model` field moved under [llm].
	if hasTopLevelKey(content, "model") && !strings.Contains(content, "[llm]") {
		migrations = append(migrations, configMigration{
			OldKey: "model",
			NewKey: "llm.model",
			Action: "moved under [llm] section",
		})
	}
	// New in this version: [log] and [monitor] sections.
	if !strings.Contains(content, "[log]") {
		migrations = append(migrations, configMigration{
			OldKey: "",
			NewKey: "log.level",
			Action: "new section [log] added (defaults to level = \"info\")",
		})
	}
	if !strings.Contains(content, "[monitor]") {
		migrations = append(migrations, configMigration{
			OldKey: "",
			NewKey: "monitor.*",
			Action: "new section [monitor] added (stale_threshold and max_restarts)",
		})
	}
	return migrations
}

// hasTopLevelKey returns true when s contains a bare `key =` assignment that
// does NOT appear inside a TOML section header (i.e. not under [something]).
func hasTopLevelKey(s, key string) bool {
	inSection := false
	for _, line := range strings.Split(s, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "[") {
			inSection = true
		}
		if !inSection && strings.HasPrefix(trimmed, key+" ") {
			return true
		}
	}
	return false
}

func newMigrateCommand(ctx *commandContext) *cobra.Command {
	var dryRun bool
	var backup bool
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "migrate",
		Short: "Migrate koios.config.toml to the current schema",
		Long:  migrateLongHelp,
		RunE: func(cmd *cobra.Command, args []string) error {
			state, err := ctx.state()
			if err != nil {
				return err
			}
			if !state.ConfigExists {
				return errors.New("koios.config.toml not found; run `koios init` first")
			}

			raw, err := os.ReadFile(state.ConfigPath)
			if err != nil {
				return fmt.Errorf("read config: %w", err)
			}

			detected := detectMigrations(raw)

			if dryRun {
				emit(cmd, jsonOut, map[string]any{
					"config_path":     state.ConfigPath,
					"dry_run":         true,
					"migrations":      detected,
					"migration_count": len(detected),
				})
				return nil
			}

			// Re-parse via canonical loader (already applies defaults).
			cfg, _, err := config.LoadOptionalFromPath(state.ConfigPath)
			if err != nil {
				return fmt.Errorf("parse config for migration: %w", err)
			}

			// Create backup before overwriting.
			if backup {
				backupPath := state.ConfigPath + ".bak." + time.Now().UTC().Format("20060102-150405")
				if err := os.WriteFile(backupPath, raw, 0o600); err != nil {
					return fmt.Errorf("create backup: %w", err)
				}
				emit(cmd, jsonOut, map[string]any{"backup": backupPath})
			}

			// Rewrite in canonical form (preserves API key).
			canonical := config.EncodeTOML(cfg, true)
			if err := os.WriteFile(state.ConfigPath, []byte(canonical), 0o600); err != nil {
				return fmt.Errorf("write migrated config: %w", err)
			}

			for i := range detected {
				detected[i].Applied = true
			}
			emit(cmd, jsonOut, map[string]any{
				"config_path":     state.ConfigPath,
				"migrations":      detected,
				"migration_count": len(detected),
				"rewritten":       true,
				"backup":          backup,
				"backup_suffix": func() string {
					if backup {
						return filepath.Base(state.ConfigPath) + ".bak.<timestamp>"
					}
					return ""
				}(),
			})
			return nil
		},
	}
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "detect migrations without writing any files")
	cmd.Flags().BoolVar(&backup, "backup", true, "create a .bak copy before rewriting (use --backup=false to skip)")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "emit JSON output")
	return cmd
}
