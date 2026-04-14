package cli

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/ffimnsr/koios/internal/config"
)

func newModelCommand(ctx *commandContext) *cobra.Command {
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "model",
		Short: "View and manage configured LLM models",
		RunE: func(cmd *cobra.Command, args []string) error {
			return cmd.Help()
		},
	}
	cmd.PersistentFlags().BoolVar(&jsonOut, "json", false, "emit JSON")

	cmd.AddCommand(newModelListCommand(ctx, &jsonOut))
	cmd.AddCommand(newModelGetCommand(ctx, &jsonOut))
	cmd.AddCommand(newModelSetCommand(ctx))
	return cmd
}

// newModelListCommand lists all model profiles plus the default and lightweight models.
func newModelListCommand(ctx *commandContext, jsonOut *bool) *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List configured model profiles",
		RunE: func(cmd *cobra.Command, args []string) error {
			state, err := ctx.state()
			if err != nil {
				return err
			}
			cfg := state.Config

			type modelInfo struct {
				Name      string `json:"name"`
				Provider  string `json:"provider"`
				Model     string `json:"model"`
				BaseURL   string `json:"base_url,omitempty"`
				IsDefault bool   `json:"is_default,omitempty"`
				Role      string `json:"role,omitempty"`
			}

			var models []modelInfo
			// Primary / default.
			defaultRole := "default"
			if cfg.DefaultProfile != "" {
				defaultRole = "default (" + cfg.DefaultProfile + ")"
			}
			models = append(models, modelInfo{
				Name:      "default",
				Provider:  cfg.Provider,
				Model:     cfg.Model,
				BaseURL:   cfg.BaseURL,
				IsDefault: true,
				Role:      defaultRole,
			})
			// Lightweight model.
			if cfg.LightweightModel != "" {
				models = append(models, modelInfo{
					Name:     "lightweight",
					Provider: cfg.Provider,
					Model:    cfg.LightweightModel,
					BaseURL:  cfg.BaseURL,
					Role:     "lightweight",
				})
			}
			// Named profiles.
			for _, p := range cfg.ModelProfiles {
				models = append(models, modelInfo{
					Name:     p.Name,
					Provider: p.Provider,
					Model:    p.Model,
					BaseURL:  p.BaseURL,
					Role:     "profile",
				})
			}

			emit(cmd, *jsonOut, map[string]any{
				"count":           len(models),
				"default_model":   cfg.Model,
				"fallback_models": cfg.FallbackModels,
				"models":          models,
			})
			return nil
		},
	}
}

// newModelGetCommand shows the current default model configuration.
func newModelGetCommand(ctx *commandContext, jsonOut *bool) *cobra.Command {
	return &cobra.Command{
		Use:   "get",
		Short: "Show the current default model configuration",
		RunE: func(cmd *cobra.Command, args []string) error {
			state, err := ctx.state()
			if err != nil {
				return err
			}
			cfg := state.Config

			info := map[string]any{
				"provider":          cfg.Provider,
				"model":             cfg.Model,
				"base_url":          cfg.BaseURL,
				"default_profile":   cfg.DefaultProfile,
				"lightweight_model": cfg.LightweightModel,
				"fallback_models":   cfg.FallbackModels,
				"profile_count":     len(cfg.ModelProfiles),
			}
			emit(cmd, *jsonOut, info)
			return nil
		},
	}
}

// newModelSetCommand updates the default model in koios.config.toml.
func newModelSetCommand(ctx *commandContext) *cobra.Command {
	var provider string
	var baseURL string
	cmd := &cobra.Command{
		Use:   "set <model>",
		Short: "Set the default model in koios.config.toml",
		Long:  modelSetLongHelp,
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			state, err := ctx.state()
			if err != nil {
				return err
			}
			if !state.ConfigExists {
				return fmt.Errorf("config file not found; run `koios init` first")
			}

			newModel := args[0]
			cfg := state.Config

			// Check if it's a named profile — if so, set default_profile.
			for _, p := range cfg.ModelProfiles {
				if p.Name == newModel {
					cfg.DefaultProfile = p.Name
					cfg.Model = p.Model
					if p.Provider != "" {
						cfg.Provider = p.Provider
					}
					if p.APIKey != "" {
						cfg.APIKey = p.APIKey
					}
					if p.BaseURL != "" {
						cfg.BaseURL = p.BaseURL
					}
					goto write
				}
			}
			// Not a profile — treat as a raw model ID with optional overrides.
			// Clear default_profile since we're specifying a model directly.
			cfg.DefaultProfile = ""
			cfg.Model = newModel
			if provider != "" {
				cfg.Provider = provider
			}
			if baseURL != "" {
				cfg.BaseURL = baseURL
			}

		write:
			content := config.EncodeTOML(cfg, false)
			if err := os.WriteFile(state.ConfigPath, []byte(content), 0o600); err != nil {
				return fmt.Errorf("write config: %w", err)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Default model updated to %q (provider: %s)\n", cfg.Model, cfg.Provider)
			return nil
		},
	}
	cmd.Flags().StringVar(&provider, "provider", "", "provider to use with the new model")
	cmd.Flags().StringVar(&baseURL, "base-url", "", "base URL for the provider")
	return cmd
}
