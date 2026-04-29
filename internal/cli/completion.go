package cli

import (
	"context"
	"os"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/ffimnsr/koios/internal/calendar"
	"github.com/ffimnsr/koios/internal/scheduler"
	"github.com/ffimnsr/koios/internal/tasks"
	"github.com/ffimnsr/koios/internal/workflow"
)

type completionItem struct {
	value       string
	description string
}

func configureCLICompletions(root *cobra.Command, ctx *commandContext) {
	registerPeerFlagCompletions(root, ctx)

	registerFlagCompletions(findSubcommand(root, "reset"), "scope",
		completionItem{value: "config", description: "remove only koios.config.toml"},
		completionItem{value: "config+creds+sessions", description: "remove config and local state"},
		completionItem{value: "full", description: "remove config, local state, and databases"},
	)
	registerFlagCompletions(findSubcommand(root, "tasks", "list"), "status",
		completionItem{value: string(tasks.TaskStatusOpen), description: "open tasks"},
		completionItem{value: string(tasks.TaskStatusSnoozed), description: "snoozed tasks"},
		completionItem{value: string(tasks.TaskStatusCompleted), description: "completed tasks"},
		completionItem{value: string(tasks.TaskStatusAll), description: "all tasks"},
	)
	registerFlagCompletions(findSubcommand(root, "tasks", "queue", "list"), "status",
		completionItem{value: string(tasks.CandidateStatusPending), description: "pending candidates"},
		completionItem{value: string(tasks.CandidateStatusApproved), description: "approved candidates"},
		completionItem{value: string(tasks.CandidateStatusRejected), description: "rejected candidates"},
		completionItem{value: string(tasks.CandidateStatusAll), description: "all candidates"},
	)
	registerFlagCompletions(findSubcommand(root, "calendar", "agenda"), "scope",
		completionItem{value: string(calendar.AgendaScopeToday), description: "today's agenda"},
		completionItem{value: string(calendar.AgendaScopeThisWeek), description: "this week's agenda"},
		completionItem{value: string(calendar.AgendaScopeNextConflict), description: "next calendar conflict"},
	)
	registerFlagCompletions(findSubcommand(root, "model", "set"), "provider",
		completionItem{value: "openai", description: "OpenAI-compatible provider"},
		completionItem{value: "anthropic", description: "Anthropic provider"},
		completionItem{value: "openrouter", description: "OpenRouter provider"},
		completionItem{value: "nvidia", description: "NVIDIA NIM provider"},
	)

	registerIDFlagCompletions(findSubcommand(root, "tasks", "get"), ctx, false)
	registerIDFlagCompletions(findSubcommand(root, "tasks", "update"), ctx, false)
	registerIDFlagCompletions(findSubcommand(root, "tasks", "assign"), ctx, false)
	registerIDFlagCompletions(findSubcommand(root, "tasks", "snooze"), ctx, false)
	registerIDFlagCompletions(findSubcommand(root, "tasks", "complete"), ctx, false)
	registerIDFlagCompletions(findSubcommand(root, "tasks", "reopen"), ctx, false)
	registerIDFlagCompletions(findSubcommand(root, "tasks", "queue", "edit"), ctx, true)
	registerIDFlagCompletions(findSubcommand(root, "tasks", "queue", "approve"), ctx, true)
	registerIDFlagCompletions(findSubcommand(root, "tasks", "queue", "reject"), ctx, true)
	registerCalendarIDFlagCompletion(findSubcommand(root, "calendar", "remove"), ctx)
	registerCronIDFlagCompletion(findSubcommand(root, "cron", "runs"), ctx)

	registerCronArgCompletion(findSubcommand(root, "cron", "edit"), ctx)
	registerCronArgCompletion(findSubcommand(root, "cron", "rm"), ctx)
	registerCronArgCompletion(findSubcommand(root, "cron", "enable"), ctx)
	registerCronArgCompletion(findSubcommand(root, "cron", "disable"), ctx)
	registerCronArgCompletion(findSubcommand(root, "cron", "run"), ctx)

	registerWorkflowArgCompletion(findSubcommand(root, "workflow", "get"), ctx)
	registerWorkflowArgCompletion(findSubcommand(root, "workflow", "delete"), ctx)
	registerWorkflowArgCompletion(findSubcommand(root, "workflow", "run"), ctx)
	registerWorkflowArgCompletion(findSubcommand(root, "workflow", "runs"), ctx)
	registerWorkflowRunArgCompletion(findSubcommand(root, "workflow", "status"), ctx)
	registerWorkflowRunArgCompletion(findSubcommand(root, "workflow", "cancel"), ctx)
	registerModelArgCompletion(findSubcommand(root, "model", "set"), ctx)

	markFlagFilename(findSubcommand(root, "calendar", "add"), "path", "ics")
	markFlagFilename(findSubcommand(root, "backup", "create"), "output", "tar.gz", "tgz")
	registerArchiveArgCompletion(findSubcommand(root, "backup", "verify"))
}

func findSubcommand(root *cobra.Command, names ...string) *cobra.Command {
	current := root
	for _, name := range names {
		if current == nil {
			return nil
		}
		var next *cobra.Command
		for _, child := range current.Commands() {
			if child.Name() == name {
				next = child
				break
			}
		}
		current = next
	}
	return current
}

func registerPeerFlagCompletions(cmd *cobra.Command, ctx *commandContext) {
	if cmd == nil {
		return
	}
	if cmd.Flags().Lookup("peer") != nil {
		_ = cmd.RegisterFlagCompletionFunc("peer", func(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
			return formatCompletionItems(listKnownPeerCompletions(ctx), toComplete), cobra.ShellCompDirectiveNoFileComp
		})
	}
	for _, child := range cmd.Commands() {
		registerPeerFlagCompletions(child, ctx)
	}
}

func registerFlagCompletions(cmd *cobra.Command, flag string, items ...completionItem) {
	if cmd == nil || cmd.Flags().Lookup(flag) == nil {
		return
	}
	_ = cmd.RegisterFlagCompletionFunc(flag, func(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
		return formatCompletionItems(items, toComplete), cobra.ShellCompDirectiveNoFileComp
	})
}

func registerIDFlagCompletions(cmd *cobra.Command, ctx *commandContext, candidates bool) {
	if cmd == nil || cmd.Flags().Lookup("id") == nil {
		return
	}
	_ = cmd.RegisterFlagCompletionFunc("id", func(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
		return formatCompletionItems(listTaskIDCompletions(ctx, cmd, candidates), toComplete), cobra.ShellCompDirectiveNoFileComp
	})
}

func registerCalendarIDFlagCompletion(cmd *cobra.Command, ctx *commandContext) {
	if cmd == nil || cmd.Flags().Lookup("id") == nil {
		return
	}
	_ = cmd.RegisterFlagCompletionFunc("id", func(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
		return formatCompletionItems(listCalendarSourceCompletions(ctx, cmd), toComplete), cobra.ShellCompDirectiveNoFileComp
	})
}

func registerCronIDFlagCompletion(cmd *cobra.Command, ctx *commandContext) {
	if cmd == nil || cmd.Flags().Lookup("id") == nil {
		return
	}
	_ = cmd.RegisterFlagCompletionFunc("id", func(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
		return formatCompletionItems(listCronJobCompletions(ctx, cmd), toComplete), cobra.ShellCompDirectiveNoFileComp
	})
}

func registerCronArgCompletion(cmd *cobra.Command, ctx *commandContext) {
	if cmd == nil {
		return
	}
	cmd.ValidArgsFunction = func(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
		return formatCompletionItems(listCronJobCompletions(ctx, cmd), toComplete), cobra.ShellCompDirectiveNoFileComp
	}
}

func registerWorkflowArgCompletion(cmd *cobra.Command, ctx *commandContext) {
	if cmd == nil {
		return
	}
	cmd.ValidArgsFunction = func(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
		return formatCompletionItems(listWorkflowCompletions(ctx, cmd), toComplete), cobra.ShellCompDirectiveNoFileComp
	}
}

func registerWorkflowRunArgCompletion(cmd *cobra.Command, ctx *commandContext) {
	if cmd == nil {
		return
	}
	cmd.ValidArgsFunction = func(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
		return formatCompletionItems(listWorkflowRunCompletions(ctx, cmd), toComplete), cobra.ShellCompDirectiveNoFileComp
	}
}

func registerModelArgCompletion(cmd *cobra.Command, ctx *commandContext) {
	if cmd == nil {
		return
	}
	cmd.ValidArgsFunction = func(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
		return formatCompletionItems(listModelCompletions(ctx), toComplete), cobra.ShellCompDirectiveNoFileComp
	}
}

func registerArchiveArgCompletion(cmd *cobra.Command) {
	if cmd == nil {
		return
	}
	cmd.ValidArgsFunction = func(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
		return []string{"tar.gz", "tgz"}, cobra.ShellCompDirectiveFilterFileExt
	}
}

func markFlagFilename(cmd *cobra.Command, flag string, extensions ...string) {
	if cmd == nil || cmd.Flags().Lookup(flag) == nil {
		return
	}
	_ = cmd.MarkFlagFilename(flag, extensions...)
}

func listKnownPeerCompletions(ctx *commandContext) []completionItem {
	items := map[string]string{}
	if peer, err := defaultCLIPeerID(); err == nil && strings.TrimSpace(peer) != "" {
		items[peer] = "derived default peer"
	}
	state, err := ctx.state()
	if err != nil || state == nil {
		return mapCompletionItems(items)
	}
	if state.Config != nil {
		for _, peer := range state.Config.OwnerPeerIDs {
			peer = strings.TrimSpace(peer)
			if peer != "" {
				items[peer] = "configured owner peer"
			}
		}
	}
	if dir := state.peersDir(); dir != "" && dirExists(dir) {
		entries, err := os.ReadDir(dir)
		if err == nil {
			for _, entry := range entries {
				if entry.IsDir() {
					items[entry.Name()] = "workspace peer"
				}
			}
		}
	}
	if dir := state.sessionDir(); dir != "" && dirExists(dir) {
		sessions, err := listSessions(dir, "")
		if err == nil {
			for _, session := range sessions {
				peer := strings.TrimSpace(session.PeerID)
				if peer != "" {
					items[peer] = "session peer"
				}
			}
		}
	}
	return mapCompletionItems(items)
}

func listCronJobCompletions(ctx *commandContext, cmd *cobra.Command) []completionItem {
	state, err := ctx.state()
	if err != nil || state == nil || !dirExists(state.cronDir()) {
		return nil
	}
	store, err := scheduler.NewJobStore(state.cronDir())
	if err != nil {
		return nil
	}
	peer := peerForCompletion(ctx, cmd)
	jobs := store.List(peer)
	items := make([]completionItem, 0, len(jobs))
	for _, job := range jobs {
		description := strings.TrimSpace(job.Name)
		if description == "" {
			description = string(job.Schedule.Kind)
		}
		items = append(items, completionItem{value: job.JobID, description: description})
	}
	return items
}

func listWorkflowCompletions(ctx *commandContext, cmd *cobra.Command) []completionItem {
	state, err := ctx.state()
	if err != nil || state == nil || !dirExists(state.workflowDir()) {
		return nil
	}
	store, err := workflow.NewStore(state.workflowDir())
	if err != nil {
		return nil
	}
	peer := peerForCompletion(ctx, cmd)
	definitions := store.List(peer)
	items := make([]completionItem, 0, len(definitions))
	for _, definition := range definitions {
		items = append(items, completionItem{value: definition.ID, description: definition.Name})
	}
	return items
}

func listWorkflowRunCompletions(ctx *commandContext, cmd *cobra.Command) []completionItem {
	state, err := ctx.state()
	if err != nil || state == nil || !dirExists(state.workflowDir()) {
		return nil
	}
	store, err := workflow.NewStore(state.workflowDir())
	if err != nil {
		return nil
	}
	peer := peerForCompletion(ctx, cmd)
	runs, err := store.ListRuns(peer, "")
	if err != nil {
		return nil
	}
	items := make([]completionItem, 0, len(runs))
	for _, run := range runs {
		items = append(items, completionItem{value: run.ID, description: string(run.Status)})
	}
	return items
}

func listTaskIDCompletions(ctx *commandContext, cmd *cobra.Command, candidates bool) []completionItem {
	state, err := ctx.state()
	if err != nil || state == nil || !fileExists(state.tasksDBPath()) {
		return nil
	}
	store, err := tasks.New(state.tasksDBPath())
	if err != nil {
		return nil
	}
	defer store.Close()
	peer := peerForCompletion(ctx, cmd)
	requestCtx := commandCompletionContext(cmd)
	if candidates {
		queued, err := store.ListCandidates(requestCtx, peer, 100, tasks.CandidateStatusAll)
		if err != nil {
			return nil
		}
		items := make([]completionItem, 0, len(queued))
		for _, candidate := range queued {
			items = append(items, completionItem{value: candidate.ID, description: candidate.Title})
		}
		return items
	}
	listed, err := store.ListTasks(requestCtx, peer, 100, tasks.TaskStatusAll)
	if err != nil {
		return nil
	}
	items := make([]completionItem, 0, len(listed))
	for _, task := range listed {
		items = append(items, completionItem{value: task.ID, description: task.Title})
	}
	return items
}

func listCalendarSourceCompletions(ctx *commandContext, cmd *cobra.Command) []completionItem {
	state, err := ctx.state()
	if err != nil || state == nil || !fileExists(state.calendarDBPath()) {
		return nil
	}
	store, err := calendar.New(state.calendarDBPath())
	if err != nil {
		return nil
	}
	defer store.Close()
	peer := peerForCompletion(ctx, cmd)
	sources, err := store.ListSources(commandCompletionContext(cmd), peer, false)
	if err != nil {
		return nil
	}
	items := make([]completionItem, 0, len(sources))
	for _, source := range sources {
		description := strings.TrimSpace(source.Name)
		if description == "" {
			description = string(source.Kind)
		}
		items = append(items, completionItem{value: source.ID, description: description})
	}
	return items
}

func listModelCompletions(ctx *commandContext) []completionItem {
	state, err := ctx.state()
	if err != nil || state == nil || state.Config == nil {
		return nil
	}
	items := map[string]string{}
	if model := strings.TrimSpace(state.Config.Model); model != "" {
		items[model] = "current default model"
	}
	if model := strings.TrimSpace(state.Config.LightweightModel); model != "" {
		items[model] = "lightweight model"
	}
	for _, model := range state.Config.FallbackModels {
		model = strings.TrimSpace(model)
		if model != "" {
			items[model] = "fallback model"
		}
	}
	for _, profile := range state.Config.ModelProfiles {
		if strings.TrimSpace(profile.Name) == "" {
			continue
		}
		description := strings.TrimSpace(profile.Provider)
		if description != "" && strings.TrimSpace(profile.Model) != "" {
			description += "/"
		}
		description += strings.TrimSpace(profile.Model)
		if description == "" {
			description = "named profile"
		}
		items[profile.Name] = description
	}
	return mapCompletionItems(items)
}

func peerForCompletion(ctx *commandContext, cmd *cobra.Command) string {
	if cmd != nil {
		if flag := cmd.Flags().Lookup("peer"); flag != nil {
			if value := strings.TrimSpace(flag.Value.String()); value != "" {
				return value
			}
		}
	}
	peer, err := defaultCLIPeerID()
	if err != nil {
		return ""
	}
	return peer
}

func commandCompletionContext(cmd *cobra.Command) context.Context {
	if cmd != nil && cmd.Context() != nil {
		return cmd.Context()
	}
	return context.Background()
}

func formatCompletionItems(items []completionItem, toComplete string) []string {
	filtered := filterCompletionItems(items, toComplete)
	out := make([]string, 0, len(filtered))
	for _, item := range filtered {
		if item.description == "" {
			out = append(out, item.value)
			continue
		}
		out = append(out, item.value+"\t"+item.description)
	}
	return out
}

func filterCompletionItems(items []completionItem, toComplete string) []completionItem {
	needle := strings.ToLower(strings.TrimSpace(toComplete))
	filtered := make([]completionItem, 0, len(items))
	for _, item := range items {
		if item.value == "" {
			continue
		}
		if needle == "" || strings.HasPrefix(strings.ToLower(item.value), needle) {
			filtered = append(filtered, item)
		}
	}
	sort.Slice(filtered, func(i, j int) bool {
		return filtered[i].value < filtered[j].value
	})
	return filtered
}

func mapCompletionItems(values map[string]string) []completionItem {
	items := make([]completionItem, 0, len(values))
	for value, description := range values {
		items = append(items, completionItem{value: value, description: description})
	}
	sort.Slice(items, func(i, j int) bool {
		return items[i].value < items[j].value
	})
	return items
}
