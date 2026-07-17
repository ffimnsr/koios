package cli

import (
	"fmt"
	"time"

	"github.com/spf13/cobra"
)

func newPeerLLMCommand(ctx *commandContext) *cobra.Command {
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "peer-llm",
		Short: "Manage BYOK LLM provider profiles for the active peer",
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
	return cmd
}

func newPeerLLMSetCommand(ctx *commandContext, jsonOut *bool) *cobra.Command {
	var (
		peer         string
		name         string
		providerName string
		apiKey       string
		baseURL      string
		defaultModel string
		enabled      bool
		disable      bool
		timeout      time.Duration
	)
	cmd := &cobra.Command{
		Use:   "set",
		Short: "Create or update a BYOK LLM provider profile",
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
			params := map[string]any{
				"name":     name,
				"provider": providerName,
				"api_key":  apiKey,
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
	cmd.Flags().StringVar(&providerName, "provider", "", "provider: openai | anthropic | openrouter | gemini | nvidia | ollama | vllm | litellm")
	cmd.Flags().StringVar(&apiKey, "api-key", "", "API key (omit for local providers)")
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
