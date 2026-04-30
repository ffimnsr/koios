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
		"group:browser",
		"group:automation",
	},
	"messaging": {
		"group:sessions",
		"group:memory",
		"group:messaging",
	},
	"minimal": {
		"group:sessions",
	},
	// sandbox is the least-privilege profile for sandboxed sessions.
	// It allows read-only file access and safe web tools, but no writes,
	// no exec, no process management, and no browser automation.
	"sandbox": {
		"time.now",
		"subagent.status",
		"session.history",
		"session.list",
		"read",
		"head",
		"tail",
		"grep",
		"find",
		"sort",
		"uniq",
		"diff",
		"workspace.list",
		"workspace.read",
		"workspace.head",
		"workspace.tail",
		"workspace.grep",
		"workspace.find",
		"workspace.sort",
		"workspace.uniq",
		"workspace.diff",
		"web_search",
		"web_fetch",
		"memory.search",
		"memory.get",
		"bookmark.get",
		"bookmark.list",
		"bookmark.search",
		"note.search",
	},
}

var toolGroups = map[string][]string{
	"group:sessions": {
		"time.now",
		"subagent.status",
		"session.history",
		"session.reset",
		"session.list",
		"session.spawn",
		"session.send",
		"session.patch",
	},
	"group:memory": {
		"bookmark.create",
		"bookmark.capture_session",
		"bookmark.get",
		"bookmark.list",
		"bookmark.search",
		"bookmark.update",
		"bookmark.delete",
		"memory.search",
		"memory.insert",
		"memory.get",
		"brief.generate",
		"waiting.create",
		"waiting.list",
		"waiting.get",
		"waiting.update",
		"waiting.snooze",
		"waiting.resolve",
		"waiting.reopen",
		"calendar.source.create",
		"calendar.source.list",
		"calendar.source.delete",
		"calendar.agenda",
		"memory.preference.create",
		"memory.preference.get",
		"memory.preference.list",
		"memory.preference.update",
		"memory.preference.confirm",
		"memory.preference.delete",
		"memory.entity.create",
		"memory.entity.update",
		"memory.entity.get",
		"memory.entity.list",
		"memory.entity.search",
		"memory.entity.link_chunk",
		"memory.entity.relate",
		"memory.entity.touch",
		"memory.entity.delete",
		"memory.entity.unlink_chunk",
		"memory.entity.unrelate",
		"contact.list",
		"contact.resolve",
		"contact.alias",
		"contact.link_channel_identity",
		"memory.candidate.create",
		"memory.candidate.list",
		"memory.candidate.edit",
		"memory.candidate.approve",
		"memory.candidate.merge",
		"memory.candidate.reject",
		"task.create",
		"task.extract",
		"task.list",
		"task.get",
		"task.update",
		"task.assign",
		"task.snooze",
		"task.complete",
		"task.reopen",
		"note.create",
		"note.search",
		"note.update",
		"scratchpad.create",
		"scratchpad.update",
		"scratchpad.get",
		"scratchpad.clear",
		"plan.create",
		"plan.update",
		"plan.status",
		"plan.complete_step",
		"artifact.create",
		"artifact.get",
		"artifact.list",
		"artifact.update",
		"decision.record",
		"decision.list",
		"decision.search",
		"preference.set",
		"preference.get",
		"preference.list",
	},
	"group:fs": {
		"read",
		"head",
		"tail",
		"grep",
		"find",
		"sort",
		"uniq",
		"diff",
		"git.status",
		"git.diff",
		"git.log",
		"git.branch",
		"git.commit",
		"git.apply_patch",
		"write",
		"edit",
		"apply_patch",
		"workspace.list",
		"workspace.read",
		"workspace.head",
		"workspace.tail",
		"workspace.grep",
		"workspace.find",
		"workspace.sort",
		"workspace.uniq",
		"workspace.diff",
		"workspace.write",
		"workspace.edit",
		"workspace.mkdir",
		"workspace.delete",
	},
	"group:runtime": {
		"approval.request",
		"exec",
		"run.list",
		"run.status",
		"run.cancel",
		"run.logs",
		"usage.current",
		"usage.history",
		"usage.estimate",
		"model.list",
		"model.capabilities",
		"model.route",
		"code_execution",
		"code_execution.status",
		"code_execution.cancel",
		"process.start",
		"process.status",
		"process.stop",
		"process.list",
		"process.logs",
		"system.run",
		"system.notify",
		"notification.send",
	},
	"group:messaging": {
		"message",
		"message.send",
		"inbox.list",
		"inbox.read",
		"inbox.mark_read",
		"inbox.route",
		"inbox.summarize",
	},
	"group:web": {
		"web_search",
		"web_fetch",
	},
	"group:browser": {
		"browser.profile.list",
		"browser.profile.use",
		"browser.status",
		"browser.*",
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
	if matchesToolToken(denied, name) {
		return false
	}
	allowed := expandToolTokens(p.Allow)
	if len(allowed) > 0 {
		return matchesToolToken(allowed, name)
	}
	profile := strings.ToLower(strings.TrimSpace(p.Profile))
	if profile == "" || profile == "full" {
		return true
	}
	base := expandToolTokens(toolProfiles[profile])
	return matchesToolToken(base, name)
}

func matchesToolToken(tokens map[string]bool, name string) bool {
	if tokens[name] {
		return true
	}
	for token := range tokens {
		if strings.HasSuffix(token, ".*") && strings.HasPrefix(name, strings.TrimSuffix(token, "*")) {
			return true
		}
	}
	return false
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
			for _, name := range defaultPluginToolNames() {
				expanded[name] = true
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
