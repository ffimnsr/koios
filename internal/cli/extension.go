package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/ffimnsr/koios/internal/extensions"
	"github.com/ffimnsr/koios/internal/mcp"
)

type extensionListEntry struct {
	ID             string   `json:"id"`
	Name           string   `json:"name"`
	Description    string   `json:"description,omitempty"`
	Path           string   `json:"path"`
	Enabled        bool     `json:"enabled"`
	Capabilities   []string `json:"capabilities,omitempty"`
	InvocationRoot string   `json:"invocation_root"`
	HTTPRoot       string   `json:"http_root,omitempty"`
}

type extensionToolEntry struct {
	ExtensionID    string `json:"extension_id"`
	Tool           string `json:"tool"`
	Description    string `json:"description,omitempty"`
	FullName       string `json:"full_name"`
	InvocationRoot string `json:"invocation_root"`
}

type extensionRouteEntry struct {
	ExtensionID string `json:"extension_id"`
	Method      string `json:"method"`
	Path        string `json:"path"`
	Tool        string `json:"tool"`
	Name        string `json:"name,omitempty"`
	URL         string `json:"url"`
}

func newExtensionCommand(ctx *commandContext) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "extension",
		Aliases: []string{"extensions", "ext"},
		Short:   "Inspect and invoke manifest-backed extensions",
	}
	cmd.AddCommand(newExtensionListCommand(ctx))
	cmd.AddCommand(newExtensionToolsCommand(ctx))
	cmd.AddCommand(newExtensionRoutesCommand(ctx))
	cmd.AddCommand(newExtensionRunCommand(ctx))
	return cmd
}

func newExtensionListCommand(ctx *commandContext) *cobra.Command {
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List discovered extensions and their CLI namespace",
		RunE: func(cmd *cobra.Command, args []string) error {
			state, err := ctx.state()
			if err != nil {
				return err
			}
			manifests, err := discoverCLIExtensions(state)
			if err != nil {
				return err
			}
			entries := make([]extensionListEntry, 0, len(manifests))
			for _, manifest := range manifests {
				entries = append(entries, extensionListEntry{
					ID:             manifest.Manifest.ID,
					Name:           manifest.Manifest.Name,
					Description:    manifest.Manifest.Description,
					Path:           manifest.Path,
					Enabled:        manifest.Enabled(),
					Capabilities:   append([]string(nil), manifest.Manifest.Capabilities...),
					InvocationRoot: extensionInvocationRoot(manifest),
					HTTPRoot:       extensionHTTPRoot(manifest),
				})
			}
			emit(cmd, jsonOut, map[string]any{"count": len(entries), "extensions": entries})
			return nil
		},
	}
	cmd.Flags().BoolVar(&jsonOut, "json", false, "emit JSON")
	return cmd
}

func newExtensionRoutesCommand(ctx *commandContext) *cobra.Command {
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "routes <extension>",
		Short: "List scoped HTTP routes for one extension",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			state, err := ctx.state()
			if err != nil {
				return err
			}
			manifest, err := resolveCLIExtension(state, args[0])
			if err != nil {
				return err
			}
			if !manifest.Enabled() {
				return fmt.Errorf("extension %q is disabled", manifest.Manifest.ID)
			}
			if !manifest.HasCapability(extensions.CapabilityHTTPRoutes) {
				return fmt.Errorf("extension %q does not expose HTTP routes", manifest.Manifest.ID)
			}
			entries := make([]extensionRouteEntry, 0, len(manifest.Manifest.Routes))
			for _, route := range manifest.Manifest.Routes {
				if route.Enabled != nil && !*route.Enabled {
					continue
				}
				entries = append(entries, extensionRouteEntry{
					ExtensionID: manifest.Manifest.ID,
					Method:      route.Method,
					Path:        route.Path,
					Tool:        route.Tool,
					Name:        route.Name,
					URL:         extensionHTTPRoot(manifest) + route.Path,
				})
			}
			sort.Slice(entries, func(i, j int) bool {
				if entries[i].Path == entries[j].Path {
					return entries[i].Method < entries[j].Method
				}
				return entries[i].Path < entries[j].Path
			})
			emit(cmd, jsonOut, map[string]any{
				"extension": manifest.Manifest.ID,
				"count":     len(entries),
				"routes":    entries,
			})
			return nil
		},
	}
	cmd.Flags().BoolVar(&jsonOut, "json", false, "emit JSON")
	return cmd
}

func newExtensionToolsCommand(ctx *commandContext) *cobra.Command {
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "tools <extension>",
		Short: "List tool-backed CLI commands for one extension",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			state, err := ctx.state()
			if err != nil {
				return err
			}
			manifest, err := resolveCLIExtension(state, args[0])
			if err != nil {
				return err
			}
			if !manifest.Enabled() {
				return fmt.Errorf("extension %q is disabled", manifest.Manifest.ID)
			}
			if !manifest.HasCapability(extensions.CapabilityTools) {
				return fmt.Errorf("extension %q does not expose tool-backed CLI commands", manifest.Manifest.ID)
			}
			manager, err := startExtensionCLIManager(cmd.Context(), manifest)
			if err != nil {
				return err
			}
			defer manager.Close()

			registered := extensionRegisteredTools(manager, manifest)
			entries := make([]extensionToolEntry, 0, len(registered))
			for _, tool := range registered {
				entries = append(entries, extensionToolEntry{
					ExtensionID:    manifest.Manifest.ID,
					Tool:           tool.ToolName,
					Description:    tool.Description,
					FullName:       tool.FullName,
					InvocationRoot: extensionInvocationRoot(manifest),
				})
			}
			emit(cmd, jsonOut, map[string]any{
				"extension": manifest.Manifest.ID,
				"count":     len(entries),
				"tools":     entries,
			})
			return nil
		},
	}
	cmd.Flags().BoolVar(&jsonOut, "json", false, "emit JSON")
	return cmd
}

func newExtensionRunCommand(ctx *commandContext) *cobra.Command {
	var (
		jsonOut  bool
		argsJSON string
		argsKV   []string
		stdinKey string
	)
	cmd := &cobra.Command{
		Use:   "run <extension> <tool>",
		Short: "Run one extension tool through the scoped CLI namespace",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			state, err := ctx.state()
			if err != nil {
				return err
			}
			manifest, err := resolveCLIExtension(state, args[0])
			if err != nil {
				return err
			}
			if !manifest.Enabled() {
				return fmt.Errorf("extension %q is disabled", manifest.Manifest.ID)
			}
			if !manifest.HasCapability(extensions.CapabilityTools) {
				return fmt.Errorf("extension %q does not expose tool-backed CLI commands", manifest.Manifest.ID)
			}
			manager, err := startExtensionCLIManager(cmd.Context(), manifest)
			if err != nil {
				return err
			}
			defer manager.Close()

			toolName := strings.TrimSpace(args[1])
			registered := extensionRegisteredTools(manager, manifest)
			if !hasExtensionTool(registered, toolName) {
				return fmt.Errorf("extension %q does not expose tool %q", manifest.Manifest.ID, toolName)
			}

			rawArgs, err := buildExtensionToolArgs(argsJSON, argsKV, stdinKey, cmd.InOrStdin())
			if err != nil {
				return err
			}
			result, err := manager.CallTool(cmd.Context(), mcp.PluginToolPrefix(manifest.Manifest.ID)+toolName, rawArgs)
			if err != nil {
				return err
			}
			if jsonOut {
				emit(cmd, true, map[string]any{
					"extension": manifest.Manifest.ID,
					"tool":      toolName,
					"result":    result,
				})
				return nil
			}
			emit(cmd, false, result)
			return nil
		},
	}
	cmd.Flags().BoolVar(&jsonOut, "json", false, "emit JSON")
	cmd.Flags().StringVar(&argsJSON, "args-json", "", "JSON object passed as tool arguments")
	cmd.Flags().StringArrayVar(&argsKV, "arg", nil, "tool argument in key=value form; may be repeated")
	cmd.Flags().StringVar(&stdinKey, "stdin", "", "argument key that should receive stdin as a string")
	return cmd
}

func discoverCLIExtensions(state *repoState) ([]extensions.DiscoveredManifest, error) {
	if state == nil || state.Config == nil {
		return nil, errors.New("load config before discovering extensions")
	}
	manifests, err := extensions.Discover(state.Config.ExtensionSearchPaths())
	if err != nil {
		return nil, fmt.Errorf("discover extensions: %w", err)
	}
	filtered := extensions.Filter(manifests, extensions.FilterPolicy{
		Allow: state.Config.ExtensionAllow,
		Deny:  state.Config.ExtensionDeny,
	})
	sort.Slice(filtered, func(i, j int) bool {
		return filtered[i].Manifest.ID < filtered[j].Manifest.ID
	})
	return filtered, nil
}

func resolveCLIExtension(state *repoState, target string) (extensions.DiscoveredManifest, error) {
	manifests, err := discoverCLIExtensions(state)
	if err != nil {
		return extensions.DiscoveredManifest{}, err
	}
	target = strings.TrimSpace(target)
	if target == "" {
		return extensions.DiscoveredManifest{}, errors.New("extension id or name is required")
	}
	targetFolded := strings.ToLower(target)
	for _, manifest := range manifests {
		if strings.EqualFold(manifest.Manifest.ID, target) {
			return manifest, nil
		}
	}
	for _, manifest := range manifests {
		if strings.ToLower(strings.TrimSpace(manifest.Manifest.Name)) == targetFolded {
			return manifest, nil
		}
	}
	return extensions.DiscoveredManifest{}, fmt.Errorf("extension %q was not found", target)
}

func startExtensionCLIManager(parent context.Context, manifest extensions.DiscoveredManifest) (*mcp.Manager, error) {
	servers, err := extensions.MCPServers([]extensions.DiscoveredManifest{manifest})
	if err != nil {
		return nil, err
	}
	manager := mcp.NewManager(servers)
	startCtx, cancel := context.WithTimeout(parent, 30*time.Second)
	defer cancel()
	if err := manager.Start(startCtx); err != nil {
		manager.Close()
		return nil, err
	}
	return manager, nil
}

func extensionRegisteredTools(manager *mcp.Manager, manifest extensions.DiscoveredManifest) []mcp.RegisteredTool {
	prefix := mcp.PluginToolPrefix(manifest.Manifest.ID)
	tools := make([]mcp.RegisteredTool, 0)
	for _, tool := range manager.ListTools() {
		if strings.HasPrefix(tool.FullName, prefix) {
			tools = append(tools, tool)
		}
	}
	sort.Slice(tools, func(i, j int) bool {
		return tools[i].ToolName < tools[j].ToolName
	})
	return tools
}

func hasExtensionTool(tools []mcp.RegisteredTool, name string) bool {
	for _, tool := range tools {
		if tool.ToolName == name {
			return true
		}
	}
	return false
}

func buildExtensionToolArgs(argsJSON string, kvPairs []string, stdinKey string, input io.Reader) (json.RawMessage, error) {
	payload := make(map[string]any)
	if strings.TrimSpace(argsJSON) != "" {
		if err := json.Unmarshal([]byte(argsJSON), &payload); err != nil {
			return nil, fmt.Errorf("parse --args-json: %w", err)
		}
		if payload == nil {
			payload = make(map[string]any)
		}
	}
	for _, pair := range kvPairs {
		key, value, ok := strings.Cut(pair, "=")
		if !ok {
			return nil, fmt.Errorf("invalid --arg %q; expected key=value", pair)
		}
		key = strings.TrimSpace(key)
		if key == "" {
			return nil, fmt.Errorf("invalid --arg %q; key must not be empty", pair)
		}
		payload[key] = value
	}
	if trimmedKey := strings.TrimSpace(stdinKey); trimmedKey != "" {
		data, err := io.ReadAll(input)
		if err != nil {
			return nil, fmt.Errorf("read stdin: %w", err)
		}
		payload[trimmedKey] = string(data)
	}
	if len(payload) == 0 {
		return nil, nil
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("marshal tool args: %w", err)
	}
	return body, nil
}

func extensionInvocationRoot(manifest extensions.DiscoveredManifest) string {
	return "koios extension run " + manifest.Manifest.ID
}

func extensionHTTPRoot(manifest extensions.DiscoveredManifest) string {
	return extensions.HTTPRouteNamespacePrefix + manifest.Manifest.ID
}
