package cli

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/ffimnsr/koios/internal/briefing"
	"github.com/ffimnsr/koios/internal/runledger"
	"github.com/ffimnsr/koios/internal/tasks"
)

type dashboardBriefResult struct {
	OK     bool             `json:"ok"`
	Kind   string           `json:"kind"`
	Text   string           `json:"text,omitempty"`
	Report *briefing.Report `json:"report,omitempty"`
}

type dashboardTaskList struct {
	Tasks []tasks.Task `json:"tasks"`
}

type dashboardWaitingList struct {
	Waiting []tasks.WaitingOn `json:"waiting"`
}

type dashboardRunsList struct {
	Records []runledger.Record `json:"records"`
}

type dashboardProjectThread struct {
	ID               string `json:"id"`
	Name             string `json:"name"`
	Notes            string `json:"notes,omitempty"`
	LinkedChunkCount int    `json:"linked_chunk_count,omitempty"`
	LastSeenAt       int64  `json:"last_seen_at,omitempty"`
	DaysStale        int    `json:"days_stale"`
}

type dashboardGateway struct {
	Reachable  bool   `json:"reachable"`
	HTTPStatus int    `json:"http_status,omitempty"`
	Version    string `json:"version,omitempty"`
	Error      string `json:"error,omitempty"`
}

type dashboardRuntime struct {
	ActiveRuns   int                `json:"active_runs"`
	QueuedRuns   int                `json:"queued_runs"`
	RunningRuns  int                `json:"running_runs"`
	RecentRuns   []runledger.Record `json:"recent_runs,omitempty"`
	LatestRunAt  int64              `json:"latest_run_at,omitempty"`
	LatestStatus string             `json:"latest_status,omitempty"`
}

type dashboardView struct {
	PeerID        string                   `json:"peer_id"`
	Kind          string                   `json:"kind"`
	GeneratedAt   int64                    `json:"generated_at"`
	Timezone      string                   `json:"timezone,omitempty"`
	Report        *briefing.Report         `json:"report,omitempty"`
	OpenTasks     []tasks.Task             `json:"open_tasks,omitempty"`
	OpenWaiting   []tasks.WaitingOn        `json:"open_waiting,omitempty"`
	StaleProjects []dashboardProjectThread `json:"stale_projects,omitempty"`
	Gateway       dashboardGateway         `json:"gateway"`
	Runtime       dashboardRuntime         `json:"runtime"`
	Notes         []string                 `json:"notes,omitempty"`
}

func newDashboardCommand(ctx *commandContext) *cobra.Command {
	var (
		peer             string
		kind             string
		timezone         string
		now              int64
		historyLimit     int
		eventLimit       int
		waitingLimit     int
		taskLimit        int
		projectLimit     int
		candidateLimit   int
		runsLimit        int
		staleProjectDays int
		timeout          time.Duration
		jsonOut          bool
	)
	cmd := &cobra.Command{
		Use:     "dashboard",
		Aliases: []string{"commitments"},
		Short:   "Show a commitments-first personal dashboard",
		RunE: func(cmd *cobra.Command, args []string) error {
			if peer == "" {
				return fmt.Errorf("--peer is required")
			}
			state, err := ctx.state()
			if err != nil {
				return err
			}
			client := newGatewayClient(state, timeout)

			briefResult, tasksResult, waitingResult, runsResult, notes, err := loadDashboardData(cmd.Context(), client, peer, dashboardQueryOptions{
				kind:           kind,
				timezone:       timezone,
				now:            now,
				historyLimit:   historyLimit,
				eventLimit:     eventLimit,
				waitingLimit:   waitingLimit,
				taskLimit:      taskLimit,
				projectLimit:   projectLimit,
				candidateLimit: candidateLimit,
				runsLimit:      runsLimit,
			})
			if err != nil {
				return err
			}

			gateway := dashboardGateway{Reachable: true}
			if health, status, err := client.health(cmd.Context()); err == nil {
				gateway.HTTPStatus = status
				if stateText, _ := health["status"].(string); strings.TrimSpace(stateText) != "" {
					gateway.Reachable = strings.EqualFold(stateText, "ok")
				}
			} else {
				gateway.Reachable = false
				gateway.Error = err.Error()
				notes = append(notes, "gateway health unavailable: "+err.Error())
			}
			if version, _, err := client.version(cmd.Context()); err == nil {
				gateway.Version, _ = version["version"].(string)
			}

			view := buildDashboardView(peer, briefResult, tasksResult.Tasks, waitingResult.Waiting, runsResult.Records, gateway, staleProjectDays, notes)
			if jsonOut {
				emit(cmd, true, view)
				return nil
			}
			emit(cmd, false, renderDashboard(view))
			return nil
		},
	}
	enableDerivedPeerDefault(cmd)
	cmd.Flags().StringVar(&peer, "peer", "", "peer ID")
	cmd.Flags().StringVar(&kind, "kind", "daily", "dashboard scope: daily or weekly")
	cmd.Flags().StringVar(&timezone, "timezone", "", "display timezone")
	cmd.Flags().Int64Var(&now, "now", 0, "override current time as Unix seconds")
	cmd.Flags().IntVar(&historyLimit, "history-limit", 0, "max recent session messages to inspect for commitments")
	cmd.Flags().IntVar(&eventLimit, "event-limit", 6, "max upcoming events to include")
	cmd.Flags().IntVar(&waitingLimit, "waiting-limit", 6, "max waiting-on records to include")
	cmd.Flags().IntVar(&taskLimit, "task-limit", 6, "max open tasks to include")
	cmd.Flags().IntVar(&projectLimit, "project-limit", 6, "max active projects to inspect")
	cmd.Flags().IntVar(&candidateLimit, "candidate-limit", 5, "max pending memory candidates to inspect in the brief")
	cmd.Flags().IntVar(&runsLimit, "runs-limit", 5, "max recent runs to show in runtime diagnostics")
	cmd.Flags().IntVar(&staleProjectDays, "stale-project-days", 14, "days without activity before a project is marked stale")
	cmd.Flags().DurationVar(&timeout, "timeout", 10*time.Second, "request timeout")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "emit JSON output")
	return cmd
}

type dashboardQueryOptions struct {
	kind           string
	timezone       string
	now            int64
	historyLimit   int
	eventLimit     int
	waitingLimit   int
	taskLimit      int
	projectLimit   int
	candidateLimit int
	runsLimit      int
}

func loadDashboardData(ctx context.Context, client *gatewayClient, peer string, opts dashboardQueryOptions) (dashboardBriefResult, dashboardTaskList, dashboardWaitingList, dashboardRunsList, []string, error) {
	briefParams := map[string]any{"kind": opts.kind, "task_limit": opts.taskLimit, "waiting_limit": opts.waitingLimit, "event_limit": opts.eventLimit, "project_limit": opts.projectLimit, "candidate_limit": opts.candidateLimit}
	if opts.timezone != "" {
		briefParams["timezone"] = opts.timezone
	}
	if opts.now > 0 {
		briefParams["now"] = opts.now
	}
	if opts.historyLimit > 0 {
		briefParams["history_limit"] = opts.historyLimit
	}

	var briefResult dashboardBriefResult
	if err := client.rpc(ctx, peer, "brief.generate", briefParams, &briefResult); err != nil {
		return dashboardBriefResult{}, dashboardTaskList{}, dashboardWaitingList{}, dashboardRunsList{}, nil, err
	}

	notes := []string{}
	if briefResult.Report != nil {
		notes = append(notes, briefResult.Report.Notes...)
	}

	var tasksResult dashboardTaskList
	if err := client.rpc(ctx, peer, "task.list", map[string]any{"status": string(tasks.TaskStatusOpen), "limit": opts.taskLimit}, &tasksResult); err != nil {
		notes = append(notes, "open tasks unavailable: "+err.Error())
	}

	var waitingResult dashboardWaitingList
	if err := client.rpc(ctx, peer, "waiting.list", map[string]any{"status": string(tasks.WaitingStatusOpen), "limit": opts.waitingLimit}, &waitingResult); err != nil {
		notes = append(notes, "waiting tracker unavailable: "+err.Error())
	}

	var runsResult dashboardRunsList
	if err := client.rpc(ctx, peer, "runs.list", map[string]any{"peer_id": peer, "limit": opts.runsLimit}, &runsResult); err != nil {
		notes = append(notes, "run ledger unavailable: "+err.Error())
	}

	return briefResult, tasksResult, waitingResult, runsResult, uniqueStrings(notes), nil
}

func buildDashboardView(peer string, briefResult dashboardBriefResult, openTasks []tasks.Task, openWaiting []tasks.WaitingOn, runs []runledger.Record, gateway dashboardGateway, staleProjectDays int, notes []string) dashboardView {
	view := dashboardView{
		PeerID:      peer,
		Kind:        briefResult.Kind,
		Gateway:     gateway,
		OpenTasks:   openTasks,
		OpenWaiting: openWaiting,
		Runtime:     summarizeRuntime(runs),
		Notes:       uniqueStrings(notes),
	}
	if briefResult.Report != nil {
		view.Report = briefResult.Report
		view.GeneratedAt = briefResult.Report.GeneratedAt
		view.Timezone = briefResult.Report.Timezone
		view.StaleProjects = collectStaleProjects(briefResult.Report, staleProjectDays)
		if view.Kind == "" {
			view.Kind = briefResult.Report.Kind
		}
	}
	if view.GeneratedAt == 0 {
		view.GeneratedAt = time.Now().Unix()
	}
	return view
}

func summarizeRuntime(records []runledger.Record) dashboardRuntime {
	runtime := dashboardRuntime{RecentRuns: records}
	for _, record := range records {
		switch record.Status {
		case runledger.StatusQueued:
			runtime.QueuedRuns++
			runtime.ActiveRuns++
		case runledger.StatusRunning:
			runtime.RunningRuns++
			runtime.ActiveRuns++
		}
		activityAt := record.QueuedAt.Unix()
		if record.FinishedAt != nil && record.FinishedAt.Unix() > activityAt {
			activityAt = record.FinishedAt.Unix()
		}
		if record.StartedAt != nil && record.StartedAt.Unix() > activityAt {
			activityAt = record.StartedAt.Unix()
		}
		if activityAt > runtime.LatestRunAt {
			runtime.LatestRunAt = activityAt
			runtime.LatestStatus = string(record.Status)
		}
	}
	return runtime
}

func collectStaleProjects(report *briefing.Report, staleProjectDays int) []dashboardProjectThread {
	if report == nil || len(report.ActiveProjects) == 0 {
		return nil
	}
	if staleProjectDays <= 0 {
		staleProjectDays = 14
	}
	cutoff := time.Unix(report.GeneratedAt, 0).Add(-time.Duration(staleProjectDays) * 24 * time.Hour)
	out := make([]dashboardProjectThread, 0, len(report.ActiveProjects))
	for _, project := range report.ActiveProjects {
		referenceAt := project.LastSeenAt
		if referenceAt == 0 {
			referenceAt = project.UpdatedAt
		}
		if referenceAt == 0 {
			continue
		}
		referenceTime := time.Unix(referenceAt, 0)
		if referenceTime.After(cutoff) {
			continue
		}
		daysStale := int(time.Unix(report.GeneratedAt, 0).Sub(referenceTime).Hours() / 24)
		out = append(out, dashboardProjectThread{
			ID:               project.ID,
			Name:             project.Name,
			Notes:            project.Notes,
			LinkedChunkCount: project.LinkedChunkCount,
			LastSeenAt:       project.LastSeenAt,
			DaysStale:        daysStale,
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].DaysStale == out[j].DaysStale {
			return out[i].Name < out[j].Name
		}
		return out[i].DaysStale > out[j].DaysStale
	})
	return out
}

func renderDashboard(view dashboardView) string {
	loc, _ := time.LoadLocation(firstNonEmpty(view.Timezone, "Local"))
	if loc == nil {
		loc = time.Local
	}
	now := time.Unix(view.GeneratedAt, 0).In(loc)
	var sb strings.Builder
	scope := strings.TrimSpace(view.Kind)
	if scope == "" {
		scope = "daily"
	}
	fmt.Fprintf(&sb, "Commitments dashboard\n")
	fmt.Fprintf(&sb, "Peer: %s\n", view.PeerID)
	fmt.Fprintf(&sb, "Generated: %s\n", now.Format(time.RFC3339))
	fmt.Fprintf(&sb, "Scope: %s | Timezone: %s\n", scope, firstNonEmpty(view.Timezone, loc.String()))
	fmt.Fprintf(&sb, "Summary: %d open tasks, %d waiting-ons, %d upcoming events, %d recent promises, %d stale project threads\n",
		len(view.OpenTasks),
		len(view.OpenWaiting),
		dashboardEventCount(view.Report),
		dashboardCommitmentCount(view.Report),
		len(view.StaleProjects),
	)

	writeDashboardTasks(&sb, view.OpenTasks, loc)
	writeDashboardWaiting(&sb, view.OpenWaiting, now)
	writeDashboardEvents(&sb, view.Report, loc)
	writeDashboardCommitments(&sb, view.Report)
	writeDashboardProjects(&sb, view.StaleProjects, loc)
	writeDashboardRuntime(&sb, view.Runtime, view.Gateway, loc)
	if len(view.Notes) > 0 {
		sb.WriteString("\nNotes:\n")
		for _, note := range view.Notes {
			fmt.Fprintf(&sb, "- %s\n", note)
		}
	}
	return strings.TrimRight(sb.String(), "\n")
}

func writeDashboardTasks(sb *strings.Builder, items []tasks.Task, loc *time.Location) {
	sb.WriteString("\nOpen tasks:\n")
	if len(items) == 0 {
		sb.WriteString("- none\n")
		return
	}
	for _, item := range items {
		line := item.Title
		meta := []string{}
		if strings.TrimSpace(item.Owner) != "" {
			meta = append(meta, "owner="+item.Owner)
		}
		if item.DueAt > 0 {
			meta = append(meta, "due "+time.Unix(item.DueAt, 0).In(loc).Format("2006-01-02 15:04"))
		}
		if len(meta) > 0 {
			line += " [" + strings.Join(meta, " | ") + "]"
		}
		fmt.Fprintf(sb, "- %s\n", line)
	}
}

func writeDashboardWaiting(sb *strings.Builder, items []tasks.WaitingOn, now time.Time) {
	sb.WriteString("\nWaiting-ons:\n")
	if len(items) == 0 {
		sb.WriteString("- none\n")
		return
	}
	for _, item := range items {
		meta := []string{item.WaitingFor}
		if label := waitingUrgencyLabel(item, now); label != "" {
			meta = append(meta, label)
		}
		fmt.Fprintf(sb, "- %s [%s]\n", item.Title, strings.Join(meta, " | "))
	}
}

func writeDashboardEvents(sb *strings.Builder, report *briefing.Report, loc *time.Location) {
	sb.WriteString("\nUpcoming events:\n")
	if report == nil || report.Agenda == nil || len(report.Agenda.Events) == 0 {
		sb.WriteString("- none\n")
		return
	}
	for _, event := range report.Agenda.Events {
		fmt.Fprintf(sb, "- %s (%s)\n", firstNonEmpty(event.Summary, "untitled event"), dashboardEventLabel(event.StartAt, event.EndAt, event.AllDay, loc))
	}
}

func writeDashboardCommitments(sb *strings.Builder, report *briefing.Report) {
	sb.WriteString("\nRecent promises:\n")
	if report == nil || len(report.RecentCommitments) == 0 {
		sb.WriteString("- none\n")
		return
	}
	for _, commitment := range report.RecentCommitments {
		line := commitment.Title
		if excerpt := strings.TrimSpace(commitment.SourceExcerpt); excerpt != "" {
			line += " - " + excerpt
		}
		fmt.Fprintf(sb, "- %s\n", line)
	}
}

func writeDashboardProjects(sb *strings.Builder, items []dashboardProjectThread, loc *time.Location) {
	sb.WriteString("\nStale project threads:\n")
	if len(items) == 0 {
		sb.WriteString("- none\n")
		return
	}
	for _, item := range items {
		meta := []string{fmt.Sprintf("%dd stale", item.DaysStale)}
		if item.LastSeenAt > 0 {
			meta = append(meta, "last seen "+time.Unix(item.LastSeenAt, 0).In(loc).Format("2006-01-02"))
		}
		if item.LinkedChunkCount > 0 {
			meta = append(meta, fmt.Sprintf("%d linked notes", item.LinkedChunkCount))
		}
		line := item.Name + " [" + strings.Join(meta, " | ") + "]"
		if note := strings.TrimSpace(item.Notes); note != "" {
			line += " - " + clipDashboard(note, 96)
		}
		fmt.Fprintf(sb, "- %s\n", line)
	}
}

func writeDashboardRuntime(sb *strings.Builder, runtime dashboardRuntime, gateway dashboardGateway, loc *time.Location) {
	sb.WriteString("\nRuntime:\n")
	if gateway.Reachable {
		line := "Gateway: ok"
		if gateway.Version != "" {
			line += " (" + gateway.Version + ")"
		}
		fmt.Fprintf(sb, "- %s\n", line)
	} else {
		fmt.Fprintf(sb, "- Gateway: unavailable")
		if gateway.Error != "" {
			fmt.Fprintf(sb, " (%s)", gateway.Error)
		}
		sb.WriteString("\n")
	}
	fmt.Fprintf(sb, "- Active runs: %d running, %d queued\n", runtime.RunningRuns, runtime.QueuedRuns)
	if runtime.LatestRunAt > 0 {
		fmt.Fprintf(sb, "- Latest run activity: %s [%s]\n", time.Unix(runtime.LatestRunAt, 0).In(loc).Format(time.RFC3339), runtime.LatestStatus)
	}
	if len(runtime.RecentRuns) == 0 {
		sb.WriteString("- Recent runs: none\n")
		return
	}
	sb.WriteString("- Recent runs:\n")
	for _, record := range runtime.RecentRuns {
		stamp := record.QueuedAt.In(loc).Format("2006-01-02 15:04")
		fmt.Fprintf(sb, "  %s %s [%s/%s] steps=%d tools=%d\n", record.ID, stamp, record.Kind, record.Status, record.Steps, record.ToolCalls)
	}
}

func dashboardEventCount(report *briefing.Report) int {
	if report == nil || report.Agenda == nil {
		return 0
	}
	return len(report.Agenda.Events)
}

func dashboardCommitmentCount(report *briefing.Report) int {
	if report == nil {
		return 0
	}
	return len(report.RecentCommitments)
}

func waitingUrgencyLabel(item tasks.WaitingOn, now time.Time) string {
	if item.FollowUpAt > 0 {
		followUp := time.Unix(item.FollowUpAt, 0)
		if !followUp.After(now) {
			return "follow-up due"
		}
		return "follow up " + followUp.Format("2006-01-02")
	}
	if item.RemindAt > 0 {
		remindAt := time.Unix(item.RemindAt, 0)
		if !remindAt.After(now) {
			return "reminder due"
		}
		return "remind " + remindAt.Format("2006-01-02")
	}
	return ""
}

func dashboardEventLabel(startAt, endAt int64, allDay bool, loc *time.Location) string {
	if startAt <= 0 {
		return "unscheduled"
	}
	start := time.Unix(startAt, 0).In(loc)
	if allDay {
		return start.Format("2006-01-02") + " all day"
	}
	if endAt <= startAt {
		return start.Format(time.RFC3339)
	}
	end := time.Unix(endAt, 0).In(loc)
	return start.Format("2006-01-02 15:04") + " to " + end.Format("15:04")
}

func clipDashboard(text string, limit int) string {
	text = strings.TrimSpace(text)
	if len(text) <= limit || limit <= 3 {
		return text
	}
	return strings.TrimSpace(text[:limit-3]) + "..."
}

func uniqueStrings(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
