package handler

import (
	"context"
	"sort"
	"strings"

	"github.com/ffimnsr/koios/internal/agent"
)

type builtInToolExecutor func(*Handler, context.Context, string, agent.ToolCall) (any, error)

type builtInToolRegistry struct {
	defs          []toolDef
	defsByName    map[string]toolDef
	aliases       map[string]string
	hiddenAliases map[string]bool
	groups        map[string][]string
	profiles      map[string][]string
}

var builtInTools = newBuiltInToolRegistry()

func newBuiltInToolRegistry() *builtInToolRegistry {
	defs := append([]toolDef(nil), toolDefs...)
	defsByName := make(map[string]toolDef, len(defs))
	for i := range defs {
		defs[i].mutatesState = defs[i].mutatesState || inferToolMutation(defs[i].name)
		defsByName[defs[i].name] = defs[i]
	}
	return &builtInToolRegistry{
		defs:       defs,
		defsByName: defsByName,
		aliases: map[string]string{
			"shell":        "exec",
			"list":         "cron.list",
			"spawn.status": "subagent.status",
			"read":         "workspace.read",
			"head":         "workspace.head",
			"tail":         "workspace.tail",
			"grep":         "workspace.grep",
			"find":         "workspace.find",
			"sort":         "workspace.sort",
			"uniq":         "workspace.uniq",
			"diff":         "workspace.diff",
			"write":        "workspace.write",
			"edit":         "workspace.edit",
			"message":      "message.send",
			"exec":         "system.run",
		},
		hiddenAliases: map[string]bool{
			"read":    true,
			"head":    true,
			"tail":    true,
			"grep":    true,
			"find":    true,
			"sort":    true,
			"uniq":    true,
			"diff":    true,
			"write":   true,
			"edit":    true,
			"message": true,
			"exec":    true,
		},
		groups:   toolGroups,
		profiles: toolProfiles,
	}
}

func (r *builtInToolRegistry) Has(name string) bool {
	if r == nil {
		return false
	}
	_, ok := r.defsByName[strings.TrimSpace(name)]
	return ok
}

func (r *builtInToolRegistry) CanonicalAlias(name string) string {
	if r == nil {
		return strings.TrimSpace(name)
	}
	name = strings.TrimSpace(name)
	seen := map[string]struct{}{}
	for {
		mapped, ok := r.aliases[name]
		if !ok || mapped == name {
			return name
		}
		if _, dup := seen[name]; dup {
			return name
		}
		seen[name] = struct{}{}
		name = strings.TrimSpace(mapped)
	}
}

func (r *builtInToolRegistry) ActiveDefs(h *Handler, policy ToolPolicy) []toolDef {
	if r == nil {
		return nil
	}
	active := make([]toolDef, 0, len(r.defs))
	for _, d := range r.defs {
		if d.available != nil && !d.available(h) {
			continue
		}
		if !policy.Allows(d.name) {
			continue
		}
		if r.hiddenAliases[d.name] {
			continue
		}
		active = append(active, d)
	}
	return active
}

func (r *builtInToolRegistry) ExpandTokens(tokens []string) map[string]bool {
	expanded := make(map[string]bool)
	if r == nil {
		return expanded
	}
	for _, raw := range tokens {
		token := strings.TrimSpace(raw)
		if token == "" {
			continue
		}
		if token == "group:openclaw" {
			for _, d := range r.defs {
				if r.hiddenAliases[d.name] {
					continue
				}
				expanded[d.name] = true
			}
			for _, name := range defaultPluginToolNames() {
				expanded[name] = true
			}
			continue
		}
		if group, ok := r.groups[token]; ok {
			for _, name := range group {
				canonical := r.CanonicalAlias(name)
				expanded[canonical] = true
			}
			continue
		}
		expanded[r.CanonicalAlias(token)] = true
	}
	return expanded
}

func (r *builtInToolRegistry) ProfileTokens(profile string) []string {
	if r == nil {
		return nil
	}
	return r.profiles[strings.ToLower(strings.TrimSpace(profile))]
}

func (r *builtInToolRegistry) VisibleCanonicalName(name string) string {
	name = r.CanonicalAlias(name)
	if r.hiddenAliases[name] {
		if canonical, ok := r.aliases[name]; ok {
			return canonical
		}
	}
	return name
}

func (r *builtInToolRegistry) MutatesState(name string) bool {
	if r == nil {
		return inferToolMutation(name)
	}
	canonical := r.CanonicalAlias(name)
	if def, ok := r.defsByName[canonical]; ok {
		return def.mutatesState
	}
	return inferToolMutation(canonical)
}

func (r *builtInToolRegistry) AliasesFor(name string) []string {
	canonical := r.CanonicalAlias(name)
	aliases := make([]string, 0)
	for alias, target := range r.aliases {
		if target == canonical {
			aliases = append(aliases, alias)
		}
	}
	sort.Strings(aliases)
	return aliases
}

func (r *builtInToolRegistry) Dispatch(h *Handler, ctx context.Context, peerID string, call agent.ToolCall) (any, error) {
	if r == nil {
		return nil, errUnhandledTool
	}
	for _, dispatch := range r.executors() {
		result, err := dispatch(h, ctx, peerID, call)
		if err == nil {
			return result, nil
		}
		if err != errUnhandledTool {
			return nil, err
		}
	}
	return nil, errUnhandledTool
}

func (r *builtInToolRegistry) executors() []builtInToolExecutor {
	return []builtInToolExecutor{(*Handler).executeDataTool, (*Handler).executeSessionWorkspaceTool, (*Handler).executeRuntimeTool}
}

func (r *builtInToolRegistry) Suggest(names []string, name string) []string {
	if strings.TrimSpace(name) == "" {
		return nil
	}
	suffix := "." + strings.TrimSpace(name)
	matches := make([]string, 0)
	for _, toolName := range names {
		if strings.HasSuffix(toolName, suffix) {
			matches = append(matches, toolName)
		}
	}
	sort.Strings(matches)
	return matches
}

func inferToolMutation(name string) bool {
	name = strings.ToLower(strings.TrimSpace(name))
	if name == "" {
		return false
	}
	mutatingFragments := []string{
		"create", "update", "delete", "remove", "write", "edit", "patch", "apply", "commit",
		"send", "insert", "set", "start", "stop", "cancel", "approve", "reject", "archive",
		"clear", "save", "schedule", "link", "move", "rename", "upload", "open",
	}
	for _, fragment := range mutatingFragments {
		if strings.Contains(name, fragment) {
			return true
		}
	}
	return false
}
