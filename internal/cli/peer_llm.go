package cli

import (
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"github.com/ffimnsr/koios/internal/config"
)

func newPeerLLMCommand(ctx *commandContext) *cobra.Command {
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "peer-llm",
		Short: "Manage BYOK LLM provider profiles for the active peer",
		Long:  "Manage peer-scoped BYOK LLM provider profiles, including legacy single-key api_key credentials and multi-key api_keys rings for concurrent peer traffic.",
		RunE: func(cmd *cobra.Command, args []string) error {
			return cmd.Help()
		},
	}
	cmd.PersistentFlags().BoolVar(&jsonOut, "json", false, "emit JSON")

	cmd.AddCommand(newPeerLLMSetCommand(ctx, &jsonOut))
	cmd.AddCommand(newPeerLLMGetCommand(ctx, &jsonOut))
	cmd.AddCommand(newPeerLLMListCommand(ctx, &jsonOut))
	cmd.AddCommand(newPeerLLMDeleteCommand(ctx, &jsonOut))
	cmd.AddCommand(newPeerLLMTestCommand(ctx, &jsonOut))
	cmd.AddCommand(newPeerLLMActivateCommand(ctx, &jsonOut))
	cmd.AddCommand(newPeerLLMKeyAddCommand(ctx, &jsonOut))
	cmd.AddCommand(newPeerLLMKeyRemoveCommand(ctx, &jsonOut))
	cmd.AddCommand(newPeerLLMKeyReplaceCommand(ctx, &jsonOut))
	cmd.AddCommand(newPeerLLMKeyRotateCommand(ctx, &jsonOut))
	cmd.AddCommand(newPeerLLMKeyListCommand(ctx, &jsonOut))
	cmd.AddCommand(newPeerLLMKeyOpsCommand(ctx, &jsonOut))
	cmd.AddCommand(newPeerLLMKeysCommand(ctx, &jsonOut))
	return cmd
}

func newPeerLLMSetCommand(ctx *commandContext, jsonOut *bool) *cobra.Command {
	var (
		peer         string
		name         string
		providerName string
		apiKey       string
		apiKeys      []string
		baseURL      string
		defaultModel string
		enabled      bool
		disable      bool
		timeout      time.Duration
	)
	cmd := &cobra.Command{
		Use:   "set",
		Short: "Create or update a BYOK LLM provider profile",
		Long:  "Create or update a peer-scoped BYOK LLM profile. Use either --api-key for the legacy single-key form or --api-keys for a multi-key ring, but never both on the same request.",
		RunE: func(cmd *cobra.Command, args []string) error {
			if peer == "" {
				return fmt.Errorf("--peer is required")
			}
			if name == "" {
				return fmt.Errorf("--name is required")
			}
			if providerName == "" {
				return fmt.Errorf("--provider is required")
			}
			state, err := ctx.state()
			if err != nil {
				return err
			}
			client := newGatewayClient(state, timeout)
			if apiKey != "" && len(apiKeys) > 0 {
				return fmt.Errorf("--api-key and --api-keys are mutually exclusive")
			}
			params := map[string]any{
				"name":     name,
				"provider": providerName,
			}
			if apiKey != "" {
				params["api_key"] = apiKey
			}
			if len(apiKeys) > 0 {
				params["api_keys"] = apiKeys
			}
			if baseURL != "" {
				params["base_url"] = baseURL
			}
			if defaultModel != "" {
				params["default_model"] = defaultModel
			}
			if cmd.Flags().Changed("enabled") || cmd.Flags().Changed("disable") {
				params["enabled"] = enabled && !disable
			}
			var result map[string]any
			if err := client.rpc(cmd.Context(), peer, "peer.llm_provider.set", params, &result); err != nil {
				return err
			}
			emit(cmd, *jsonOut, result)
			return nil
		},
	}
	cmd.Flags().StringVar(&peer, "peer", "", "peer ID")
	cmd.Flags().StringVar(&name, "name", "", "profile name (stable alias)")
	cmd.Flags().StringVar(&providerName, "provider", "", "provider: "+config.SupportedLLMProvidersHint())
	cmd.Flags().StringVar(&apiKey, "api-key", "", "API key (legacy single-key form; omit for local providers; mutually exclusive with --api-keys)")
	cmd.Flags().StringSliceVar(&apiKeys, "api-keys", nil, "API keys (multi-key form; repeat or use comma-separated values; mutually exclusive with --api-key)")
	cmd.Flags().StringVar(&baseURL, "base-url", "", "base URL override")
	cmd.Flags().StringVar(&defaultModel, "default-model", "", "default model ID")
	cmd.Flags().BoolVar(&enabled, "enabled", true, "enable this profile")
	cmd.Flags().BoolVar(&disable, "disable", false, "disable this profile")
	cmd.Flags().DurationVar(&timeout, "timeout", 10*time.Second, "RPC timeout")
	return cmd
}

func newPeerLLMGetCommand(ctx *commandContext, jsonOut *bool) *cobra.Command {
	var (
		peer    string
		name    string
		timeout time.Duration
	)
	cmd := &cobra.Command{
		Use:   "get",
		Short: "Get details for one BYOK LLM provider profile",
		RunE: func(cmd *cobra.Command, args []string) error {
			if peer == "" {
				return fmt.Errorf("--peer is required")
			}
			if name == "" && len(args) > 0 {
				name = args[0]
			}
			if name == "" {
				return fmt.Errorf("profile name is required")
			}
			state, err := ctx.state()
			if err != nil {
				return err
			}
			client := newGatewayClient(state, timeout)
			var result map[string]any
			if err := client.rpc(cmd.Context(), peer, "peer.llm_provider.get", map[string]any{"name": name}, &result); err != nil {
				return err
			}
			emit(cmd, *jsonOut, result)
			return nil
		},
	}
	cmd.Flags().StringVar(&peer, "peer", "", "peer ID")
	cmd.Flags().StringVar(&name, "name", "", "profile name")
	cmd.Flags().DurationVar(&timeout, "timeout", 10*time.Second, "RPC timeout")
	return cmd
}

func newPeerLLMListCommand(ctx *commandContext, jsonOut *bool) *cobra.Command {
	var (
		peer    string
		filter  string
		timeout time.Duration
	)
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List BYOK LLM provider profiles for a peer",
		RunE: func(cmd *cobra.Command, args []string) error {
			if peer == "" {
				return fmt.Errorf("--peer is required")
			}
			state, err := ctx.state()
			if err != nil {
				return err
			}
			client := newGatewayClient(state, timeout)
			params := map[string]any{}
			if filter != "" {
				params["provider"] = filter
			}
			var result map[string]any
			if err := client.rpc(cmd.Context(), peer, "peer.llm_provider.list", params, &result); err != nil {
				return err
			}
			emit(cmd, *jsonOut, result)
			return nil
		},
	}
	cmd.Flags().StringVar(&peer, "peer", "", "peer ID")
	cmd.Flags().StringVar(&filter, "provider", "", "filter by provider name")
	cmd.Flags().DurationVar(&timeout, "timeout", 10*time.Second, "RPC timeout")
	return cmd
}

func newPeerLLMDeleteCommand(ctx *commandContext, jsonOut *bool) *cobra.Command {
	var (
		peer    string
		name    string
		timeout time.Duration
	)
	cmd := &cobra.Command{
		Use:   "delete",
		Short: "Delete a BYOK LLM provider profile",
		RunE: func(cmd *cobra.Command, args []string) error {
			if peer == "" {
				return fmt.Errorf("--peer is required")
			}
			if name == "" && len(args) > 0 {
				name = args[0]
			}
			if name == "" {
				return fmt.Errorf("profile name is required")
			}
			state, err := ctx.state()
			if err != nil {
				return err
			}
			client := newGatewayClient(state, timeout)
			var result map[string]any
			if err := client.rpc(cmd.Context(), peer, "peer.llm_provider.delete", map[string]any{"name": name}, &result); err != nil {
				return err
			}
			emit(cmd, *jsonOut, result)
			return nil
		},
	}
	cmd.Flags().StringVar(&peer, "peer", "", "peer ID")
	cmd.Flags().StringVar(&name, "name", "", "profile name")
	cmd.Flags().DurationVar(&timeout, "timeout", 10*time.Second, "RPC timeout")
	return cmd
}

func newPeerLLMTestCommand(ctx *commandContext, jsonOut *bool) *cobra.Command {
	var (
		peer    string
		name    string
		timeout time.Duration
	)
	cmd := &cobra.Command{
		Use:   "test",
		Short: "Test connectivity for a stored BYOK LLM provider profile",
		Long:  "Sends a minimal completion to the provider to verify the endpoint and credentials are working.",
		RunE: func(cmd *cobra.Command, args []string) error {
			if peer == "" {
				return fmt.Errorf("--peer is required")
			}
			if name == "" && len(args) > 0 {
				name = args[0]
			}
			if name == "" {
				return fmt.Errorf("profile name is required")
			}
			state, err := ctx.state()
			if err != nil {
				return err
			}
			client := newGatewayClient(state, timeout)
			var result map[string]any
			if err := client.rpc(cmd.Context(), peer, "peer.llm_provider.test", map[string]any{"name": name}, &result); err != nil {
				return err
			}
			emit(cmd, *jsonOut, result)
			return nil
		},
	}
	cmd.Flags().StringVar(&peer, "peer", "", "peer ID")
	cmd.Flags().StringVar(&name, "name", "", "profile name")
	cmd.Flags().DurationVar(&timeout, "timeout", 30*time.Second, "RPC timeout")
	return cmd
}

func newPeerLLMKeyAddCommand(ctx *commandContext, jsonOut *bool) *cobra.Command {
	var (
		peer    string
		name    string
		apiKey  string
		timeout time.Duration
	)
	cmd := &cobra.Command{
		Use:   "key-add",
		Short: "Add one API key to a BYOK LLM provider profile",
		RunE: func(cmd *cobra.Command, args []string) error {
			if peer == "" || name == "" || apiKey == "" {
				return fmt.Errorf("--peer, --name, and --api-key are required")
			}
			state, err := ctx.state()
			if err != nil {
				return err
			}
			client := newGatewayClient(state, timeout)
			var result map[string]any
			if err := client.rpc(cmd.Context(), peer, "peer.llm_provider.key_add", map[string]any{"name": name, "api_key": apiKey}, &result); err != nil {
				return err
			}
			emit(cmd, *jsonOut, result)
			return nil
		},
	}
	cmd.Flags().StringVar(&peer, "peer", "", "peer ID")
	cmd.Flags().StringVar(&name, "name", "", "profile name")
	cmd.Flags().StringVar(&apiKey, "api-key", "", "API key to append")
	cmd.Flags().DurationVar(&timeout, "timeout", 10*time.Second, "RPC timeout")
	return cmd
}

func newPeerLLMKeyRemoveCommand(ctx *commandContext, jsonOut *bool) *cobra.Command {
	var (
		peer    string
		name    string
		index   int
		timeout time.Duration
	)
	cmd := &cobra.Command{
		Use:   "key-remove",
		Short: "Remove one API key from a BYOK LLM provider profile by index",
		RunE: func(cmd *cobra.Command, args []string) error {
			if peer == "" || name == "" {
				return fmt.Errorf("--peer and --name are required")
			}
			state, err := ctx.state()
			if err != nil {
				return err
			}
			client := newGatewayClient(state, timeout)
			var result map[string]any
			if err := client.rpc(cmd.Context(), peer, "peer.llm_provider.key_remove", map[string]any{"name": name, "index": index}, &result); err != nil {
				return err
			}
			emit(cmd, *jsonOut, result)
			return nil
		},
	}
	cmd.Flags().StringVar(&peer, "peer", "", "peer ID")
	cmd.Flags().StringVar(&name, "name", "", "profile name")
	cmd.Flags().IntVar(&index, "index", 0, "zero-based key index")
	cmd.Flags().DurationVar(&timeout, "timeout", 10*time.Second, "RPC timeout")
	return cmd
}

func newPeerLLMKeyReplaceCommand(ctx *commandContext, jsonOut *bool) *cobra.Command {
	var (
		peer    string
		name    string
		index   int
		apiKey  string
		timeout time.Duration
	)
	cmd := &cobra.Command{
		Use:   "key-replace",
		Short: "Replace one API key on a BYOK LLM provider profile by index",
		RunE: func(cmd *cobra.Command, args []string) error {
			if peer == "" || name == "" || apiKey == "" {
				return fmt.Errorf("--peer, --name, and --api-key are required")
			}
			state, err := ctx.state()
			if err != nil {
				return err
			}
			client := newGatewayClient(state, timeout)
			var result map[string]any
			if err := client.rpc(cmd.Context(), peer, "peer.llm_provider.key_replace", map[string]any{"name": name, "index": index, "api_key": apiKey}, &result); err != nil {
				return err
			}
			emit(cmd, *jsonOut, result)
			return nil
		},
	}
	cmd.Flags().StringVar(&peer, "peer", "", "peer ID")
	cmd.Flags().StringVar(&name, "name", "", "profile name")
	cmd.Flags().IntVar(&index, "index", 0, "zero-based key index")
	cmd.Flags().StringVar(&apiKey, "api-key", "", "replacement API key")
	cmd.Flags().DurationVar(&timeout, "timeout", 10*time.Second, "RPC timeout")
	return cmd
}

func newPeerLLMKeyRotateCommand(ctx *commandContext, jsonOut *bool) *cobra.Command {
	var (
		peer    string
		name    string
		index   int
		apiKey  string
		timeout time.Duration
	)
	cmd := &cobra.Command{
		Use:   "key-rotate",
		Short: "Rotate one API key on a BYOK LLM provider profile by index",
		RunE: func(cmd *cobra.Command, args []string) error {
			if peer == "" || name == "" || apiKey == "" {
				return fmt.Errorf("--peer, --name, and --api-key are required")
			}
			state, err := ctx.state()
			if err != nil {
				return err
			}
			client := newGatewayClient(state, timeout)
			var result map[string]any
			if err := client.rpc(cmd.Context(), peer, "peer.llm_provider.key_rotate", map[string]any{"name": name, "index": index, "api_key": apiKey}, &result); err != nil {
				return err
			}
			emit(cmd, *jsonOut, result)
			return nil
		},
	}
	cmd.Flags().StringVar(&peer, "peer", "", "peer ID")
	cmd.Flags().StringVar(&name, "name", "", "profile name")
	cmd.Flags().IntVar(&index, "index", 0, "zero-based key index")
	cmd.Flags().StringVar(&apiKey, "api-key", "", "new API key")
	cmd.Flags().DurationVar(&timeout, "timeout", 10*time.Second, "RPC timeout")
	return cmd
}

func newPeerLLMKeyListCommand(ctx *commandContext, jsonOut *bool) *cobra.Command {
	var (
		peer    string
		name    string
		timeout time.Duration
	)
	cmd := &cobra.Command{
		Use:   "key-list",
		Short: "Show masked key metadata for one BYOK LLM provider profile",
		RunE: func(cmd *cobra.Command, args []string) error {
			if peer == "" || name == "" {
				return fmt.Errorf("--peer and --name are required")
			}
			state, err := ctx.state()
			if err != nil {
				return err
			}
			client := newGatewayClient(state, timeout)
			var result map[string]any
			if err := client.rpc(cmd.Context(), peer, "peer.llm_provider.get", map[string]any{"name": name}, &result); err != nil {
				return err
			}
			emit(cmd, *jsonOut, result)
			return nil
		},
	}
	cmd.Flags().StringVar(&peer, "peer", "", "peer ID")
	cmd.Flags().StringVar(&name, "name", "", "profile name")
	cmd.Flags().DurationVar(&timeout, "timeout", 10*time.Second, "RPC timeout")
	return cmd
}

func newPeerLLMKeyOpsCommand(ctx *commandContext, jsonOut *bool) *cobra.Command {
	cmd := &cobra.Command{Use: "key", Short: "Alias namespace for key operations"}
	cmd.AddCommand(newPeerLLMKeyAddCommand(ctx, jsonOut))
	cmd.AddCommand(newPeerLLMKeyRemoveCommand(ctx, jsonOut))
	cmd.AddCommand(newPeerLLMKeyReplaceCommand(ctx, jsonOut))
	cmd.AddCommand(newPeerLLMKeyRotateCommand(ctx, jsonOut))
	cmd.AddCommand(newPeerLLMKeyListCommand(ctx, jsonOut))
	return cmd
}

func newPeerLLMKeysCommand(ctx *commandContext, jsonOut *bool) *cobra.Command {
	cmd := &cobra.Command{Use: "keys", Short: "Alias namespace for key operations"}
	cmd.AddCommand(newPeerLLMKeyAddCommand(ctx, jsonOut))
	cmd.AddCommand(newPeerLLMKeyRemoveCommand(ctx, jsonOut))
	cmd.AddCommand(newPeerLLMKeyReplaceCommand(ctx, jsonOut))
	cmd.AddCommand(newPeerLLMKeyRotateCommand(ctx, jsonOut))
	cmd.AddCommand(newPeerLLMKeyListCommand(ctx, jsonOut))
	return cmd
}

func newPeerLLMActivateCommand(ctx *commandContext, jsonOut *bool) *cobra.Command {
	var (
		peer    string
		name    string
		timeout time.Duration
	)
	cmd := &cobra.Command{
		Use:   "activate",
		Short: "Set a BYOK provider profile as the default for this peer",
		RunE: func(cmd *cobra.Command, args []string) error {
			if peer == "" {
				return fmt.Errorf("--peer is required")
			}
			if name == "" && len(args) > 0 {
				name = args[0]
			}
			if name == "" {
				return fmt.Errorf("profile name is required")
			}
			state, err := ctx.state()
			if err != nil {
				return err
			}
			client := newGatewayClient(state, timeout)
			var result map[string]any
			if err := client.rpc(cmd.Context(), peer, "peer.llm_provider.activate", map[string]any{"name": name}, &result); err != nil {
				return err
			}
			emit(cmd, *jsonOut, result)
			return nil
		},
	}
	cmd.Flags().StringVar(&peer, "peer", "", "peer ID")
	cmd.Flags().StringVar(&name, "name", "", "profile name to activate")
	cmd.Flags().DurationVar(&timeout, "timeout", 10*time.Second, "RPC timeout")
	return cmd
}
