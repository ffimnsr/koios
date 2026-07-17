package cli

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/ffimnsr/koios/internal/config"
	"github.com/ffimnsr/koios/internal/mcp"
	"github.com/ffimnsr/koios/internal/mcpregistry"
)

func newMCPCommand(ctx *commandContext) *cobra.Command {
	var jsonOut bool
	root := &cobra.Command{
		Use:   "mcp",
		Short: "Manage user-installed MCP servers",
		Long: `Manage user-installed MCP servers that are persisted in the local
MCP registry. Commands operate on the local SQLite store; changes are
picked up at gateway startup or can be pushed to a running gateway
via the enable/disable/remove commands.`,
	}
	enableDerivedPeerDefault(root)
	root.PersistentFlags().BoolVar(&jsonOut, "json", false, "emit JSON output")

	root.AddCommand(newMCPListCommand(ctx, &jsonOut))
	root.AddCommand(newMCPAddCommand(ctx, &jsonOut))
	root.AddCommand(newMCPTestCommand(ctx, &jsonOut))
	root.AddCommand(newMCPEnableCommand(ctx, &jsonOut))
	root.AddCommand(newMCPDisableCommand(ctx, &jsonOut))
	root.AddCommand(newMCPRemoveCommand(ctx, &jsonOut))
	root.AddCommand(newMCPInspectCommand(ctx, &jsonOut))
	return root
}

func newMCPListCommand(ctx *commandContext, jsonOut *bool) *cobra.Command {
	var peer string
	cmd := &cobra.Command{
		Use:     "list",
		Aliases: []string{"ls"},
		Short:   "List user-managed MCP servers",
		RunE: func(cmd *cobra.Command, args []string) error {
			store, err := openMCPRegistry(ctx)
			if err != nil {
				return err
			}
			defer store.Close()

			var records []mcpregistry.ServerRecord
			if peer != "" {
				records, err = store.ListByOwner(cmd.Context(), peer)
			} else {
				records, err = store.ListAll(cmd.Context())
			}
			if err != nil {
				return fmt.Errorf("list: %w", err)
			}
			if len(records) == 0 {
				emit(cmd, *jsonOut, map[string]any{"ok": true, "servers": []any{}})
				return nil
			}

			sort.Slice(records, func(i, j int) bool {
				return records[i].OwnerPeerID+"/"+records[i].Name < records[j].OwnerPeerID+"/"+records[j].Name
			})

			type entry struct {
				ID               string `json:"id"`
				Owner            string `json:"owner"`
				Name             string `json:"name"`
				Transport        string `json:"transport"`
				CommandOrURL     string `json:"command_or_url"`
				Enabled          bool   `json:"enabled"`
				Visibility       string `json:"visibility"`
				ApprovalRequired bool   `json:"approval_required"`
				ToolCount        int    `json:"tool_count,omitempty"`
				Connected        bool   `json:"connected,omitempty"`
				LastError        string `json:"last_error,omitempty"`
			}

			entries := make([]entry, 0, len(records))
			for _, rec := range records {
				e := entry{
					ID:               rec.ID,
					Owner:            rec.OwnerPeerID,
					Name:             rec.Name,
					Transport:        rec.Transport,
					Enabled:          rec.Enabled,
					Visibility:       rec.Visibility,
					ApprovalRequired: rec.ApprovalRequired,
				}
				if rec.Transport == "stdio" {
					e.CommandOrURL = rec.Command
				} else {
					e.CommandOrURL = rec.URL
				}
				entries = append(entries, e)
			}

			emit(cmd, *jsonOut, map[string]any{"ok": true, "servers": entries})
			return nil
		},
	}
	cmd.Flags().StringVar(&peer, "peer", "", "list only for this peer")
	return cmd
}

func newMCPAddCommand(ctx *commandContext, jsonOut *bool) *cobra.Command {
	var (
		owner            string
		name             string
		transport        string
		command          string
		args             []string
		env              []string // key=value
		url_             string
		headers          []string // key=value
		timeout          string
		enable           bool
		skipTest         bool
		visibility       string
		approvalRequired bool
	)
	cmd := &cobra.Command{
		Use:   "add",
		Short: "Register a new user-managed MCP server",
		Long: `Register a new MCP server in the local persistent registry.
The server is saved disabled by default; use --enable or
'koios mcp enable' to activate it.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if owner == "" {
				derived, err := defaultCLIPeerID()
				if err != nil {
					return fmt.Errorf("--owner is required and could not be derived: %w", err)
				}
				owner = derived
			}
			if name == "" {
				return fmt.Errorf("--name is required")
			}
			transport = strings.ToLower(strings.TrimSpace(transport))
			if transport == "" {
				return fmt.Errorf("--transport is required (stdio, http, sse)")
			}

			// Parse env flags.
			envMap := make(map[string]string)
			for _, kv := range env {
				parts := strings.SplitN(kv, "=", 2)
				if len(parts) != 2 {
					return fmt.Errorf("invalid --env %q, expected key=value", kv)
				}
				envMap[parts[0]] = parts[1]
			}
			// Parse header flags.
			headerMap := make(map[string]string)
			for _, kv := range headers {
				parts := strings.SplitN(kv, "=", 2)
				if len(parts) != 2 {
					return fmt.Errorf("invalid --header %q, expected key=value", kv)
				}
				headerMap[parts[0]] = parts[1]
			}

			// Validate.
			cfg := config.MCPServerConfig{
				Name:      name,
				Transport: transport,
				Command:   command,
				Args:      args,
				Env:       envMap,
				URL:       url_,
				Headers:   headerMap,
				Timeout:   timeout,
				Enabled:   enable,
			}
			if err := mcp.ValidateServerConfig(cfg); err != nil {
				return fmt.Errorf("invalid server config: %w", err)
			}
			if err := mcp.ValidateUserVisibility(visibility); err != nil {
				return err
			}

			store, err := openMCPRegistry(ctx)
			if err != nil {
				return err
			}
			defer store.Close()

			input := mcpregistry.Input{
				OwnerPeerID:      owner,
				Name:             name,
				Transport:        transport,
				Command:          command,
				Args:             args,
				Env:              envMap,
				URL:              url_,
				Headers:          headerMap,
				Timeout:          timeout,
				Enabled:          enable,
				Visibility:       visibility,
				ApprovalRequired: approvalRequired,
			}
			rec, err := store.Create(context.Background(), input)
			if err != nil {
				return fmt.Errorf("create: %w", err)
			}

			result := map[string]any{
				"ok":                true,
				"id":                rec.ID,
				"name":              rec.Name,
				"transport":         rec.Transport,
				"enabled":           rec.Enabled,
				"visibility":        rec.Visibility,
				"approval_required": rec.ApprovalRequired,
			}
			if rec.Transport == "stdio" {
				result["command"] = rec.Command
				result["args"] = rec.Args
			} else {
				result["url"] = rec.URL
			}
			if len(rec.Env) > 0 {
				result["env"] = mcpregistry.RedactedEnv(rec.Env)
			}
			if len(rec.Headers) > 0 {
				result["headers"] = mcpregistry.RedactedHeaders(rec.Headers)
			}
			if rec.Timeout != "" {
				result["timeout"] = rec.Timeout
			}
			if !skipTest && rec.Enabled {
				testResult := testMCPServer(cmd.Context(), rec)
				result["test"] = testResult
			}
			emit(cmd, *jsonOut, result)
			return nil
		},
	}
	cmd.Flags().StringVar(&owner, "owner", "", "owner peer ID")
	cmd.Flags().StringVar(&name, "name", "", "display name for the server")
	cmd.Flags().StringVar(&transport, "transport", "", "transport: stdio, http, or sse")
	cmd.Flags().StringVar(&command, "command", "", "executable for stdio transport")
	cmd.Flags().StringArrayVar(&args, "arg", nil, "command argument (repeatable)")
	cmd.Flags().StringArrayVar(&env, "env", nil, "environment key=value (repeatable)")
	cmd.Flags().StringVar(&url_, "url", "", "URL for http/sse transport")
	cmd.Flags().StringArrayVar(&headers, "header", nil, "HTTP header key=value (repeatable)")
	cmd.Flags().StringVar(&timeout, "timeout", "30s", "request timeout")
	cmd.Flags().BoolVar(&enable, "enable", false, "enable the server immediately")
	cmd.Flags().BoolVar(&skipTest, "skip-test", false, "skip connectivity test on add")
	cmd.Flags().StringVar(&visibility, "visibility", "private", "visibility: private, shared, owner_only")
	cmd.Flags().BoolVar(&approvalRequired, "approval-required", true, "require approval for tool calls")
	return cmd
}

func newMCPTestCommand(ctx *commandContext, jsonOut *bool) *cobra.Command {
	var owner, id string
	var timeout time.Duration
	cmd := &cobra.Command{
		Use:   "test --owner <peer> --id <record-id>",
		Short: "Probe and test connectivity of a user-managed MCP server",
		RunE: func(cmd *cobra.Command, args []string) error {
			if owner == "" {
				return fmt.Errorf("--owner is required")
			}
			if id == "" {
				return fmt.Errorf("--id is required")
			}
			store, err := openMCPRegistry(ctx)
			if err != nil {
				return err
			}
			defer store.Close()
			rec, err := store.Get(cmd.Context(), owner, id)
			if err != nil {
				return fmt.Errorf("get: %w", err)
			}
			result := testMCPServerWithTimeout(cmd.Context(), rec, timeout)
			emit(cmd, *jsonOut, result)
			return nil
		},
	}
	cmd.Flags().StringVar(&owner, "owner", "", "owner peer ID")
	cmd.Flags().StringVar(&id, "id", "", "record ID")
	cmd.Flags().DurationVar(&timeout, "timeout", 10*time.Second, "probe timeout")
	return cmd
}

func newMCPEnableCommand(ctx *commandContext, jsonOut *bool) *cobra.Command {
	var owner, id string
	cmd := &cobra.Command{
		Use:   "enable --owner <peer> --id <record-id>",
		Short: "Enable a user-managed MCP server so it connects at next gateway start",
		RunE: func(cmd *cobra.Command, args []string) error {
			if owner == "" {
				return fmt.Errorf("--owner is required")
			}
			if id == "" {
				return fmt.Errorf("--id is required")
			}
			store, err := openMCPRegistry(ctx)
			if err != nil {
				return err
			}
			defer store.Close()

			enabled := true
			rec, err := store.Update(cmd.Context(), id, owner, mcpregistry.UpdateInput{
				Enabled: &enabled,
			})
			if err != nil {
				return fmt.Errorf("enable: %w", err)
			}
			emit(cmd, *jsonOut, map[string]any{"ok": true, "id": rec.ID, "name": rec.Name, "enabled": rec.Enabled})
			return nil
		},
	}
	cmd.Flags().StringVar(&owner, "owner", "", "owner peer ID")
	cmd.Flags().StringVar(&id, "id", "", "record ID")
	return cmd
}

func newMCPDisableCommand(ctx *commandContext, jsonOut *bool) *cobra.Command {
	var owner, id string
	cmd := &cobra.Command{
		Use:   "disable --owner <peer> --id <record-id>",
		Short: "Disable a user-managed MCP server without removing its configuration",
		RunE: func(cmd *cobra.Command, args []string) error {
			if owner == "" {
				return fmt.Errorf("--owner is required")
			}
			if id == "" {
				return fmt.Errorf("--id is required")
			}
			store, err := openMCPRegistry(ctx)
			if err != nil {
				return err
			}
			defer store.Close()

			enabled := false
			rec, err := store.Update(cmd.Context(), id, owner, mcpregistry.UpdateInput{
				Enabled: &enabled,
			})
			if err != nil {
				return fmt.Errorf("disable: %w", err)
			}
			emit(cmd, *jsonOut, map[string]any{"ok": true, "id": rec.ID, "name": rec.Name, "enabled": rec.Enabled})
			return nil
		},
	}
	cmd.Flags().StringVar(&owner, "owner", "", "owner peer ID")
	cmd.Flags().StringVar(&id, "id", "", "record ID")
	return cmd
}

func newMCPRemoveCommand(ctx *commandContext, jsonOut *bool) *cobra.Command {
	var owner, id string
	var force bool
	cmd := &cobra.Command{
		Use:     "remove",
		Aliases: []string{"rm", "delete"},
		Short:   "Remove a user-managed MCP server from the registry",
		RunE: func(cmd *cobra.Command, args []string) error {
			if owner == "" {
				return fmt.Errorf("--owner is required")
			}
			if id == "" {
				if len(args) > 0 && !force {
					return fmt.Errorf("use --force to remove by positional ID without --owner")
				}
				if len(args) > 0 {
					id = args[0]
				}
			}
			if id == "" {
				return fmt.Errorf("--id is required")
			}
			store, err := openMCPRegistry(ctx)
			if err != nil {
				return err
			}
			defer store.Close()

			if err := store.Delete(cmd.Context(), owner, id); err != nil {
				return fmt.Errorf("remove: %w", err)
			}
			emit(cmd, *jsonOut, map[string]any{"ok": true, "id": id, "deleted": true})
			return nil
		},
	}
	cmd.Flags().StringVar(&owner, "owner", "", "owner peer ID")
	cmd.Flags().StringVar(&id, "id", "", "record ID")
	cmd.Flags().BoolVar(&force, "force", false, "force remove by positional ID")
	return cmd
}

func newMCPInspectCommand(ctx *commandContext, jsonOut *bool) *cobra.Command {
	var owner, id string
	var showSecrets bool
	cmd := &cobra.Command{
		Use:   "inspect --owner <peer> --id <record-id>",
		Short: "Show full details of a user-managed MCP server",
		RunE: func(cmd *cobra.Command, args []string) error {
			if owner == "" {
				return fmt.Errorf("--owner is required")
			}
			if id == "" {
				return fmt.Errorf("--id is required")
			}
			store, err := openMCPRegistry(ctx)
			if err != nil {
				return err
			}
			defer store.Close()
			rec, err := store.Get(cmd.Context(), owner, id)
			if err != nil {
				return fmt.Errorf("get: %w", err)
			}

			result := map[string]any{
				"id":                rec.ID,
				"owner":             rec.OwnerPeerID,
				"name":              rec.Name,
				"transport":         rec.Transport,
				"enabled":           rec.Enabled,
				"visibility":        rec.Visibility,
				"approval_required": rec.ApprovalRequired,
				"created_at":        rec.CreatedAt,
				"updated_at":        rec.UpdatedAt,
			}
			if rec.Transport == "stdio" {
				result["command"] = rec.Command
				result["args"] = rec.Args
			} else {
				result["url"] = rec.URL
			}
			if rec.Timeout != "" {
				result["timeout"] = rec.Timeout
			}
			if showSecrets {
				result["env"] = rec.Env
				result["headers"] = rec.Headers
			} else {
				result["env"] = mcpregistry.RedactedEnv(rec.Env)
				result["headers"] = mcpregistry.RedactedHeaders(rec.Headers)
			}
			if len(rec.AllowedPeerIDs) > 0 {
				result["allowed_peer_ids"] = rec.AllowedPeerIDs
			}

			// Probe runtime status.
			status := testMCPServer(cmd.Context(), rec)
			result["test"] = status

			emit(cmd, *jsonOut, result)
			return nil
		},
	}
	cmd.Flags().StringVar(&owner, "owner", "", "owner peer ID")
	cmd.Flags().StringVar(&id, "id", "", "record ID")
	cmd.Flags().BoolVar(&showSecrets, "show-secrets", false, "reveal stored secrets in output")
	return cmd
}

// ── helpers ─────────────────────────────────────────────────────────────────────

func openMCPRegistry(ctx *commandContext) (*mcpregistry.Store, error) {
	cfg, err := loadCLIConfig(ctx)
	if err != nil {
		return nil, err
	}
	dbPath := cfg.MCPRegistryDBPath()
	dir := cfg.DBDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("create db dir: %w", err)
	}
	return mcpregistry.New(dbPath)
}

func loadCLIConfig(ctx *commandContext) (*config.Config, error) {
	state, err := ctx.state()
	if err != nil {
		return nil, fmt.Errorf("load config: %w", err)
	}
	if state.Config == nil {
		return nil, fmt.Errorf("no config loaded (run 'koios init' or 'koios doctor --repair')")
	}
	cfg := *state.Config
	return &cfg, nil
}

func testMCPServer(ctx context.Context, rec *mcpregistry.ServerRecord) map[string]any {
	return testMCPServerWithTimeout(ctx, rec, 10*time.Second)
}

func testMCPServerWithTimeout(ctx context.Context, rec *mcpregistry.ServerRecord, timeout time.Duration) map[string]any {
	transport := strings.ToLower(strings.TrimSpace(rec.Transport))
	client := probeMCPClient(config.MCPServerConfig{
		Name:    rec.Name,
		Command: rec.Command,
		Args:    rec.Args,
		Env:     rec.Env,
		URL:     rec.URL,
		Headers: rec.Headers,
		Timeout: rec.Timeout,
	}, transport, timeout)
	if client == nil {
		return map[string]any{"success": false, "error": "could not create client"}
	}
	defer client.Close()

	testCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	if err := client.Initialize(testCtx); err != nil {
		return map[string]any{"success": false, "phase": "initialize", "error": err.Error()}
	}
	tools, err := client.ListTools(testCtx)
	if err != nil {
		return map[string]any{"success": false, "phase": "list_tools", "error": err.Error()}
	}
	toolNames := make([]string, 0, len(tools))
	for _, t := range tools {
		toolNames = append(toolNames, t.Name)
	}
	return map[string]any{
		"success":    true,
		"tools":      toolNames,
		"tool_count": len(tools),
	}
}

// probeMCPClient creates a transport client for probing a user-managed server.
func probeMCPClient(server config.MCPServerConfig, transport string, timeout time.Duration) mcp.Client {
	switch transport {
	case "stdio":
		return mcp.NewStdioClient(server.Name, server.Command, server.Args, server.Env)
	case "sse":
		return mcp.NewSSEClient(server.Name, server.URL, server.Headers, timeout)
	default:
		return mcp.NewHTTPClient(server.Name, server.URL, server.Headers, timeout)
	}
}
