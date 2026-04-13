package handler

import "strings"

// ToolPolicy configures which chat tools are exposed to the model.
type ToolPolicy struct {
	Profile string
	Allow   []string
	Deny    []string
}

var toolProfiles = map[string][]string{
	"coding": {
		"group:sessions",
		"group:fs",
		"group:runtime",
		"group:memory",
		"group:web",
		"group:automation",
	},
	"messaging": {
		"group:sessions",
		"group:memory",
	},
	"minimal": {
		"group:sessions",
	},
}

var toolGroups = map[string][]string{
	"group:sessions": {
		"time.now",
		"session.history",
		"session.reset",
	},
	"group:memory": {
		"memory.search",
		"memory.insert",
		"memory.get",
	},
	"group:fs": {
		"read",
		"write",
		"edit",
		"workspace.list",
		"workspace.read",
		"workspace.write",
		"workspace.edit",
		"workspace.mkdir",
		"workspace.delete",
	},
	"group:runtime": {
		"exec",
	},
	"group:web": {
		"web_search",
		"web_fetch",
	},
	"group:automation": {
		"cron.list",
		"cron.create",
		"cron.get",
		"cron.update",
		"cron.delete",
		"cron.trigger",
		"cron.runs",
	},
}

func (p ToolPolicy) Allows(name string) bool {
	denied := expandToolTokens(p.Deny)
	if denied[name] {
		return false
	}
	allowed := expandToolTokens(p.Allow)
	if len(allowed) > 0 {
		return allowed[name]
	}
	profile := strings.ToLower(strings.TrimSpace(p.Profile))
	if profile == "" || profile == "full" {
		return true
	}
	base := expandToolTokens(toolProfiles[profile])
	return base[name]
}

func expandToolTokens(tokens []string) map[string]bool {
	expanded := make(map[string]bool)
	for _, raw := range tokens {
		token := strings.TrimSpace(raw)
		if token == "" {
			continue
		}
		if token == "group:openclaw" {
			for _, d := range toolDefs {
				expanded[d.name] = true
			}
			continue
		}
		if group, ok := toolGroups[token]; ok {
			for _, name := range group {
				expanded[name] = true
			}
			continue
		}
		expanded[token] = true
	}
	return expanded
}
