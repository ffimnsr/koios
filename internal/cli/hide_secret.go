package cli

import (
	"fmt"
	"io"
	"strings"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/ffimnsr/koios/internal/config"
)

func newHideSecretCommand(ctx *commandContext) *cobra.Command {
	var jsonOut bool
	var stdin bool
	var setField string
	cmd := &cobra.Command{
		Use:   "hide-secret [secret]",
		Short: "Encrypt a config secret for this host and user",
		Long:  "Encrypt a secret into a machine-bound blob that Koios can decrypt later from koios.config.toml. The blob is bound to the current host/user fingerprint and a local master key stored under the user config directory.\n\nWith --set <field>, the named config field is encrypted in-place. Supported fields: llm.<profile>.api_key, channels.telegram.bot_token, channels.telegram.webhook_secret, hooks.webhook_secret, hooks.webhook_token.",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if setField != "" {
				state, err := ctx.state()
				if err != nil {
					return err
				}
				// Optional: caller may supply the new secret value; if not, encrypt the existing field value.
				var newSecret string
				if len(args) > 0 || stdin {
					newSecret, err = readHideSecretInput(cmd, args, stdin)
					if err != nil {
						return err
					}
				}
				hidden, updated, err := hideSecretInConfigField(state, setField, newSecret)
				if err != nil {
					return err
				}
				if !updated {
					return fmt.Errorf("field %q has an empty value; provide the secret as an argument or via --stdin", setField)
				}
				payload := map[string]any{
					"field":         setField,
					"hidden_secret": hidden,
				}
				if jsonOut {
					emit(cmd, true, payload)
					return nil
				}
				fmt.Fprintf(cmd.OutOrStdout(), "Updated %s in %s\n", setField, state.ConfigPath)
				return nil
			}

			secret, err := readHideSecretInput(cmd, args, stdin)
			if err != nil {
				return err
			}
			hidden, err := config.HideSecret(secret)
			if err != nil {
				return err
			}
			payload := map[string]any{
				"hidden_secret": hidden,
				"config_usage":  `api_key = "` + hidden + `"`,
			}
			if jsonOut {
				emit(cmd, true, payload)
				return nil
			}
			fmt.Fprintln(cmd.OutOrStdout(), hidden)
			fmt.Fprintln(cmd.OutOrStdout(), "Paste it into koios.config.toml as a quoted value, for example:")
			fmt.Fprintf(cmd.OutOrStdout(), "api_key = %q\n", hidden)
			return nil
		},
	}
	cmd.Flags().BoolVar(&stdin, "stdin", false, "read the secret from stdin")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "emit JSON")
	cmd.Flags().StringVar(&setField, "set", "", "encrypt and rewrite the named config field in-place (e.g. llm.default.api_key)")
	return cmd
}

// hideSecretInConfigField loads the config at state.ConfigPath, encrypts the
// value of the named field (or newSecret if non-empty), writes the config back,
// and returns the hidden blob plus whether a field was actually updated.
// Supported field paths:
//
//	llm.<profile-name>.api_key
//	channels.telegram.bot_token
//	channels.telegram.webhook_secret
//	hooks.webhook_secret
//	hooks.webhook_token
func hideSecretInConfigField(state *repoState, field, newSecret string) (hidden string, updated bool, err error) {
	cfg, err := config.LoadFromPath(state.ConfigPath)
	if err != nil {
		return "", false, fmt.Errorf("load config: %w", err)
	}

	// Resolve the plaintext to encrypt and the internal hidden-secrets path.
	plaintext, internalPath, err := resolveHideSecretFieldPlaintext(cfg, field)
	if err != nil {
		return "", false, err
	}
	if newSecret != "" {
		plaintext = newSecret
	}
	if plaintext == "" {
		return "", false, nil
	}

	hidden, err = config.HideSecret(plaintext)
	if err != nil {
		return "", false, err
	}
	// Update the hidden-secrets cache so EncodeTOML writes the new blob.
	config.SetHiddenSecret(cfg, internalPath, hidden)

	if err := writeConfigWithValidation(state.ConfigPath, cfg); err != nil {
		return "", false, err
	}
	return hidden, true, nil
}

// resolveHideSecretFieldPlaintext maps a dot-separated field path (as exposed
// to the user via --set) to the current decoded plaintext and the internal
// hidden-secrets cache key used by EncodeTOML.
func resolveHideSecretFieldPlaintext(cfg *config.Config, field string) (plaintext, internalPath string, err error) {
	switch {
	case strings.HasPrefix(field, "llm.") && strings.HasSuffix(field, ".api_key"):
		profileName := strings.TrimSuffix(strings.TrimPrefix(field, "llm."), ".api_key")
		for i := range cfg.ModelProfiles {
			if cfg.ModelProfiles[i].Name == profileName {
				return cfg.ModelProfiles[i].APIKey,
					config.HiddenSecretFieldPathModelProfileAPIKey(profileName),
					nil
			}
		}
		return "", "", fmt.Errorf("LLM profile %q not found in config", profileName)

	case field == config.HiddenSecretPathTelegramBotToken:
		return cfg.Telegram.BotToken, field, nil

	case field == config.HiddenSecretPathTelegramWebhookSecret:
		return cfg.Telegram.WebhookSecret, field, nil

	case field == config.HiddenSecretPathHooksWebhookSecret:
		return cfg.HookWebhookSecret, field, nil

	case field == config.HiddenSecretPathHooksWebhookToken:
		return cfg.WebhookToken, field, nil

	default:
		return "", "", fmt.Errorf("unsupported field %q; supported: llm.<profile>.api_key, %s, %s, %s, %s",
			field,
			config.HiddenSecretPathTelegramBotToken,
			config.HiddenSecretPathTelegramWebhookSecret,
			config.HiddenSecretPathHooksWebhookSecret,
			config.HiddenSecretPathHooksWebhookToken,
		)
	}
}

// isPipedStdin reports whether stdin is a pipe (not a terminal), meaning the
// caller can safely read from it without prompting.
func isPipedStdin(cmd *cobra.Command) bool {
	f, ok := cmd.InOrStdin().(interface{ Fd() uintptr })
	if !ok {
		return true // conservative: treat unknown readers as piped
	}
	return !term.IsTerminal(int(f.Fd()))
}

func readHideSecretInput(cmd *cobra.Command, args []string, stdin bool) (string, error) {
	if len(args) > 0 {
		secret := strings.TrimSpace(args[0])
		if secret == "" {
			return "", fmt.Errorf("secret must not be empty")
		}
		return secret, nil
	}
	if stdin {
		data, err := io.ReadAll(cmd.InOrStdin())
		if err != nil {
			return "", fmt.Errorf("read secret from stdin: %w", err)
		}
		secret := strings.TrimSpace(string(data))
		if secret == "" {
			return "", fmt.Errorf("secret must not be empty")
		}
		return secret, nil
	}
	if file, ok := cmd.InOrStdin().(interface{ Fd() uintptr }); ok && term.IsTerminal(int(file.Fd())) {
		fmt.Fprint(cmd.ErrOrStderr(), "Enter secret: ")
		secret, err := term.ReadPassword(int(file.Fd()))
		fmt.Fprintln(cmd.ErrOrStderr())
		if err != nil {
			return "", fmt.Errorf("read secret from terminal: %w", err)
		}
		trimmed := strings.TrimSpace(string(secret))
		if trimmed == "" {
			return "", fmt.Errorf("secret must not be empty")
		}
		return trimmed, nil
	}
	data, err := io.ReadAll(cmd.InOrStdin())
	if err != nil {
		return "", fmt.Errorf("read secret from stdin: %w", err)
	}
	secret := strings.TrimSpace(string(data))
	if secret == "" {
		return "", fmt.Errorf("provide a secret argument, pass --stdin, or pipe the secret into koios hide-secret")
	}
	return secret, nil
}
