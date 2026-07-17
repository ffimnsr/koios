package mcp

import (
	"fmt"
	"sort"
	"strings"

	"github.com/ffimnsr/koios/internal/config"
	"github.com/ffimnsr/koios/internal/mcpregistry"
)

// MergeServerConfigs combines MCP server configs from the three sources and
// returns a unified, validated, deduplicated list. Source-layer ordering is
// preserved: static < extension < user-managed. Collisions on runtime name are
// detected and reported as errors.
func MergeServerConfigs(
	static []config.MCPServerConfig,
	extension []config.MCPServerConfig,
	userRecords []mcpregistry.ServerRecord,
) ([]config.MCPServerConfig, error) {
	seen := make(map[string]string) // runtime name → source name for error messages
	var out []config.MCPServerConfig

	// 1. Static config entries.
	for _, s := range static {
		name := strings.TrimSpace(s.Name)
		if name == "" {
			continue
		}
		if err := ValidateServerConfig(s); err != nil {
			return nil, fmt.Errorf("static MCP server %q: %w", name, err)
		}
		if prev, conflict := seen[name]; conflict {
			return nil, fmt.Errorf("static MCP server %q conflicts with %q", name, prev)
		}
		seen[name] = "static config"
		out = append(out, s)
	}

	// 2. Extension-manifest entries.
	for _, s := range extension {
		name := strings.TrimSpace(s.Name)
		if name == "" {
			continue
		}
		if err := ValidateServerConfig(s); err != nil {
			return nil, fmt.Errorf("extension MCP server %q: %w", name, err)
		}
		if prev, conflict := seen[name]; conflict {
			return nil, fmt.Errorf("extension MCP server %q conflicts with %q", name, prev)
		}
		seen[name] = "extension: " + name
		out = append(out, s)
	}

	// 3. User-managed entries.  Each record already has a derived runtime name.
	for _, rec := range userRecords {
		cfg := rec.ToMCPServerConfig()
		runtimeName := cfg.Name
		if err := ValidateServerConfig(cfg); err != nil {
			return nil, fmt.Errorf("user MCP server %q (owner %q): %w", rec.Name, rec.OwnerPeerID, err)
		}
		if prev, conflict := seen[runtimeName]; conflict {
			return nil, fmt.Errorf("user MCP server %q (owner %q) runtime name %q conflicts with %q",
				rec.Name, rec.OwnerPeerID, runtimeName, prev)
		}
		seen[runtimeName] = fmt.Sprintf("user: %s/%s", rec.OwnerPeerID, rec.Name)
		out = append(out, cfg)
	}

	// Sort by name for deterministic output.
	sort.Slice(out, func(i, j int) bool {
		return out[i].Name < out[j].Name
	})

	return out, nil
}
