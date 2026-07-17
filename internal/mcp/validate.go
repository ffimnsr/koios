package mcp

import (
	"fmt"
	"strings"
	"time"

	"github.com/ffimnsr/koios/internal/config"
)

// ValidateServerConfig checks that a MCPServerConfig has valid fields for
// runtime use. It is shared by the static config loader, extension manifests,
// and the user-managed MCP registry.
func ValidateServerConfig(server config.MCPServerConfig) error {
	name := strings.TrimSpace(server.Name)
	if name == "" {
		return fmt.Errorf("mcp: server name is required")
	}
	if sanitizePrefixToken(name) == "" {
		return fmt.Errorf("mcp: server name %q does not produce a usable token", name)
	}

	transport := strings.ToLower(strings.TrimSpace(server.Transport))
	switch transport {
	case "stdio", "http", "sse":
	default:
		return fmt.Errorf("mcp: unsupported transport %q (expected stdio, http, or sse)", server.Transport)
	}

	if transport == "stdio" && strings.TrimSpace(server.Command) == "" {
		return fmt.Errorf("mcp: command is required for stdio transport")
	}
	if (transport == "http" || transport == "sse") && strings.TrimSpace(server.URL) == "" {
		return fmt.Errorf("mcp: url is required for %s transport", transport)
	}

	if t := strings.TrimSpace(server.Timeout); t != "" {
		if _, err := time.ParseDuration(t); err != nil {
			return fmt.Errorf("mcp: invalid timeout %q: %w", t, err)
		}
	}

	// Reject reserved name prefixes used by the runtime.
	if strings.HasPrefix(strings.ToLower(name), "mcp__") {
		return fmt.Errorf("mcp: server name %q uses reserved prefix \"mcp__\"", name)
	}
	if strings.HasPrefix(strings.ToLower(name), "mcp_plug_") {
		return fmt.Errorf("mcp: server name %q uses reserved prefix \"mcp_plug_\"", name)
	}
	if strings.HasPrefix(strings.ToLower(name), "u_") {
		return fmt.Errorf("mcp: server name %q uses reserved prefix \"u_\"", name)
	}
	if strings.HasPrefix(strings.ToLower(name), "browser_") {
		return fmt.Errorf("mcp: server name %q uses reserved prefix \"browser_\"", name)
	}

	return nil
}

// ValidateUserVisibility checks that the visibility string is one of the
// allowed values for user-managed MCP servers.
func ValidateUserVisibility(v string) error {
	v = strings.ToLower(strings.TrimSpace(v))
	switch v {
	case "private", "shared", "owner_only", "":
		return nil
	default:
		return fmt.Errorf("mcp: invalid visibility %q (expected private, shared, or owner_only)", v)
	}
}

// sanitizePrefixToken is defined in manager.go — it lowercases and collapses
// non-alphanumeric characters into single underscores.
