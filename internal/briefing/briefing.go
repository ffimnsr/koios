package briefing

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/ffimnsr/koios/internal/calendar"
	"github.com/ffimnsr/koios/internal/memory"
	"github.com/ffimnsr/koios/internal/tasks"
	"github.com/ffimnsr/koios/internal/types"
)

type Kind string

const (
	KindDaily  Kind = "daily"
	KindWeekly Kind = "weekly"
)

type Options struct {
	Kind           string `json:"kind,omitempty"`
	Timezone       string `json:"timezone,omitempty"`
	Now            int64  `json:"now,omitempty"`
	HistoryLimit   int    `json:"history_limit,omitempty"`
	EventLimit     int    `json:"event_limit,omitempty"`
	WaitingLimit   int    `json:"waiting_limit,omitempty"`
	TaskLimit      int    `json:"task_limit,omitempty"`
	ProjectLimit   int    `json:"project_limit,omitempty"`
	CandidateLimit int    `json:"candidate_limit,omitempty"`
	SessionKey     string `json:"session_key,omitempty"`
}

type Report struct {
	Kind                    string             `json:"kind"`
	PeerID                  string             `json:"peer_id"`
	SessionKey              string             `json:"session_key,omitempty"`
	Timezone                string             `json:"timezone"`
	GeneratedAt             int64              `json:"generated_at"`
	WindowStart             int64              `json:"window_start,omitempty"`
	WindowEnd               int64              `json:"window_end,omitempty"`
	Agenda                  *calendar.Agenda   `json:"agenda,omitempty"`
	DueTasks                []tasks.Task       `json:"due_tasks,omitempty"`
	StaleWaiting            []WaitingDigest    `json:"stale_waiting,omitempty"`
	PendingMemoryCandidates []memory.Candidate `json:"pending_memory_candidates,omitempty"`
	ActiveProjects          []memory.Entity    `json:"active_projects,omitempty"`
	RecentCommitments       []Commitment       `json:"recent_commitments,omitempty"`
	SuggestedActions        []Action           `json:"suggested_actions,omitempty"`
	Notes                   []string           `json:"notes,omitempty"`
	Summary                 Summary            `json:"summary"`
}

type Summary struct {
	EventCount           int `json:"event_count"`
	DueTaskCount         int `json:"due_task_count"`
	StaleWaitingCount    int `json:"stale_waiting_count"`
	PendingMemoryCount   int `json:"pending_memory_count"`
	ActiveProjectCount   int `json:"active_project_count"`
	CommitmentCount      int `json:"commitment_count"`
	SuggestedActionCount int `json:"suggested_action_count"`
}

type WaitingDigest struct {
	Waiting tasks.WaitingOn `json:"waiting"`
	Reason  string          `json:"reason"`
}

type Commitment struct {
	Title         string `json:"title"`
	Details       string `json:"details,omitempty"`
	SourceRole    string `json:"source_role,omitempty"`
	SourceExcerpt string `json:"source_excerpt,omitempty"`
}

type Action struct {
	Kind   string `json:"kind"`
	Title  string `json:"title"`
	Reason string `json:"reason,omitempty"`
}

type Builder struct {
	CalendarStore *calendar.Store
	MemoryStore   *memory.Store
	TaskStore     *tasks.Store
	HTTPClient    *http.Client
}

func NormalizeKind(raw string) (Kind, error) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", string(KindDaily):
		return KindDaily, nil
	case string(KindWeekly):
		return KindWeekly, nil
	default:
		return "", fmt.Errorf("kind must be daily or weekly")
	}
}

func (b Builder) Generate(ctx context.Context, peerID string, history []types.Message, opts Options) (*Report, error) {
	kind, err := NormalizeKind(opts.Kind)
	if err != nil {
		return nil, err
	}
	peerID = strings.TrimSpace(peerID)
	if peerID == "" {
		return nil, fmt.Errorf("peer_id is required")
	}
	loc, err := loadLocation(opts.Timezone)
	if err != nil {
		return nil, err
	}
	now := time.Now().In(loc)
	if opts.Now > 0 {
		now = time.Unix(opts.Now, 0).In(loc)
	}
	report := &Report{
		Kind:        string(kind),
		PeerID:      peerID,
		SessionKey:  strings.TrimSpace(opts.SessionKey),
		Timezone:    loc.String(),
		GeneratedAt: now.Unix(),
		Notes:       []string{},
	}

	eventLimit := positiveOrDefault(opts.EventLimit, 8)
	waitingLimit := positiveOrDefault(opts.WaitingLimit, 5)
	taskLimit := positiveOrDefault(opts.TaskLimit, 5)
	projectLimit := positiveOrDefault(opts.ProjectLimit, 5)
	candidateLimit := positiveOrDefault(opts.CandidateLimit, 5)
	historyLimit := positiveOrDefault(opts.HistoryLimit, defaultHistoryLimit(kind))

	if b.CalendarStore != nil {
		scope := string(calendar.AgendaScopeToday)
		if kind == KindWeekly {
			scope = string(calendar.AgendaScopeThisWeek)
		}
		agenda, err := b.CalendarStore.Agenda(ctx, peerID, calendar.AgendaQuery{
			Scope:    scope,
			Timezone: loc.String(),
			Now:      now.Unix(),
			Limit:    eventLimit,
		}, b.HTTPClient)
		if err != nil {
			report.Notes = append(report.Notes, "calendar agenda unavailable: "+err.Error())
		} else {
			report.Agenda = agenda
			report.WindowStart = agenda.RangeStart
			report.WindowEnd = agenda.RangeEnd
		}
	} else {
		report.Notes = append(report.Notes, "calendar is not configured")
	}

	existingKeys := make(map[string]struct{})
	if b.TaskStore != nil {
		allTasks, err := b.TaskStore.ListTasks(ctx, peerID, 100, tasks.TaskStatusAll)
		if err != nil {
			report.Notes = append(report.Notes, "tasks unavailable: "+err.Error())
		} else {
			report.DueTasks = selectDueTasks(allTasks, now.Unix(), report.WindowEnd, taskLimit)
			for _, task := range allTasks {
				existingKeys[titleKey(task.Title)] = struct{}{}
			}
		}

		allWaiting, err := b.TaskStore.ListWaitingOns(ctx, peerID, 100, tasks.WaitingStatusAll)
		if err != nil {
			report.Notes = append(report.Notes, "waiting tracker unavailable: "+err.Error())
		} else {
			report.StaleWaiting = selectStaleWaiting(allWaiting, kind, now.Unix(), waitingLimit)
			for _, item := range allWaiting {
				existingKeys[titleKey(item.Title)] = struct{}{}
			}
		}
	} else {
		report.Notes = append(report.Notes, "tasks and waiting tracker are not configured")
	}

	if b.MemoryStore != nil {
		candidates, err := b.MemoryStore.ListCandidates(ctx, peerID, candidateLimit, memory.CandidateStatusPending)
		if err != nil {
			report.Notes = append(report.Notes, "memory candidates unavailable: "+err.Error())
		} else {
			report.PendingMemoryCandidates = candidates
		}
		projects, err := b.MemoryStore.ListEntities(ctx, peerID, memory.EntityKindProject, projectLimit)
		if err != nil {
			report.Notes = append(report.Notes, "project memory unavailable: "+err.Error())
		} else {
			report.ActiveProjects = projects
		}
	} else {
		report.Notes = append(report.Notes, "memory is not configured")
	}

	report.RecentCommitments = extractRecentCommitments(history, historyLimit, taskLimit, existingKeys)
	report.SuggestedActions = buildSuggestedActions(report, loc)
	report.Summary = Summary{
		EventCount:           agendaCount(report.Agenda),
		DueTaskCount:         len(report.DueTasks),
		StaleWaitingCount:    len(report.StaleWaiting),
		PendingMemoryCount:   len(report.PendingMemoryCandidates),
		ActiveProjectCount:   len(report.ActiveProjects),
		CommitmentCount:      len(report.RecentCommitments),
		SuggestedActionCount: len(report.SuggestedActions),
	}
	return report, nil
}

func RenderText(report *Report) string {
	if report == nil {
		return "No brief available."
	}
	loc, _ := loadLocation(report.Timezone)
	generated := time.Unix(report.GeneratedAt, 0).In(loc)
	title := "Daily brief"
	if report.Kind == string(KindWeekly) {
		title = "Weekly review"
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "%s\n", title)
	fmt.Fprintf(&sb, "Generated: %s\n", generated.Format(time.RFC3339))
	fmt.Fprintf(&sb, "Scope: %s\n", report.Timezone)
	fmt.Fprintf(&sb, "Summary: %d events, %d due tasks, %d stale waiting-ons, %d pending memory candidates, %d active projects, %d recent commitments\n",
		report.Summary.EventCount,
		report.Summary.DueTaskCount,
		report.Summary.StaleWaitingCount,
		report.Summary.PendingMemoryCount,
		report.Summary.ActiveProjectCount,
		report.Summary.CommitmentCount,
	)

	writeAgendaSection(&sb, report, loc)
	writeDueTasksSection(&sb, report, loc)
	writeWaitingSection(&sb, report)
	writeCandidateSection(&sb, report)
	writeProjectSection(&sb, report, loc)
	writeCommitmentSection(&sb, report)
	writeActionsSection(&sb, report)
	if len(report.Notes) > 0 {
		sb.WriteString("\nNotes:\n")
		for _, note := range report.Notes {
			fmt.Fprintf(&sb, "- %s\n", note)
		}
	}
	return strings.TrimRight(sb.String(), "\n")
}

func writeAgendaSection(sb *strings.Builder, report *Report, loc *time.Location) {
	sb.WriteString("\nUpcoming events:\n")
	if report.Agenda == nil || len(report.Agenda.Events) == 0 {
		sb.WriteString("- none\n")
		return
	}
	for _, event := range report.Agenda.Events {
		fmt.Fprintf(sb, "- %s (%s)\n", firstNonEmpty(event.Summary, "untitled event"), formatEvent(event, loc))
	}
}

func writeDueTasksSection(sb *strings.Builder, report *Report, loc *time.Location) {
	sb.WriteString("\nDue tasks:\n")
	if len(report.DueTasks) == 0 {
		sb.WriteString("- none\n")
		return
	}
	for _, task := range report.DueTasks {
		fmt.Fprintf(sb, "- %s (%s)\n", task.Title, dueLabel(task.DueAt, loc))
	}
}

func writeWaitingSection(sb *strings.Builder, report *Report) {
	sb.WriteString("\nStale waiting-ons:\n")
	if len(report.StaleWaiting) == 0 {
		sb.WriteString("- none\n")
		return
	}
	for _, item := range report.StaleWaiting {
		fmt.Fprintf(sb, "- %s [%s]\n", item.Waiting.Title, item.Reason)
	}
}

func writeCandidateSection(sb *strings.Builder, report *Report) {
	sb.WriteString("\nPending memory candidates:\n")
	if len(report.PendingMemoryCandidates) == 0 {
		sb.WriteString("- none\n")
		return
	}
	for _, candidate := range report.PendingMemoryCandidates {
		fmt.Fprintf(sb, "- %s\n", clip(candidate.Content, 120))
	}
}

func writeProjectSection(sb *strings.Builder, report *Report, loc *time.Location) {
	sb.WriteString("\nActive projects:\n")
	if len(report.ActiveProjects) == 0 {
		sb.WriteString("- none\n")
		return
	}
	for _, project := range report.ActiveProjects {
		extra := ""
		if project.LastSeenAt > 0 {
			extra = "last seen " + time.Unix(project.LastSeenAt, 0).In(loc).Format("2006-01-02")
		}
		if extra == "" && strings.TrimSpace(project.Notes) != "" {
			extra = clip(project.Notes, 80)
		}
		if extra != "" {
			fmt.Fprintf(sb, "- %s (%s)\n", project.Name, extra)
		} else {
			fmt.Fprintf(sb, "- %s\n", project.Name)
		}
	}
}

func writeCommitmentSection(sb *strings.Builder, report *Report) {
	sb.WriteString("\nRecent commitments:\n")
	if len(report.RecentCommitments) == 0 {
		sb.WriteString("- none\n")
		return
	}
	for _, commitment := range report.RecentCommitments {
		line := commitment.Title
		if strings.TrimSpace(commitment.SourceExcerpt) != "" {
			line += " - " + clip(commitment.SourceExcerpt, 100)
		}
		fmt.Fprintf(sb, "- %s\n", line)
	}
}

func writeActionsSection(sb *strings.Builder, report *Report) {
	sb.WriteString("\nSuggested follow-ups:\n")
	if len(report.SuggestedActions) == 0 {
		sb.WriteString("- none\n")
		return
	}
	for index, action := range report.SuggestedActions {
		fmt.Fprintf(sb, "%d. %s", index+1, action.Title)
		if strings.TrimSpace(action.Reason) != "" {
			fmt.Fprintf(sb, " - %s", action.Reason)
		}
		sb.WriteString("\n")
	}
}

func buildSuggestedActions(report *Report, loc *time.Location) []Action {
	actions := make([]Action, 0, 5)
	if len(report.StaleWaiting) > 0 {
		item := report.StaleWaiting[0]
		actions = append(actions, Action{Kind: "waiting_followup", Title: fmt.Sprintf("Follow up on %s", item.Waiting.Title), Reason: item.Reason})
	}
	if len(report.DueTasks) > 0 {
		task := report.DueTasks[0]
		actions = append(actions, Action{Kind: "task_due", Title: fmt.Sprintf("Advance %s", task.Title), Reason: dueLabel(task.DueAt, loc)})
	}
	if report.Agenda != nil && len(report.Agenda.Events) > 0 {
		event := report.Agenda.Events[0]
		actions = append(actions, Action{Kind: "calendar_prep", Title: fmt.Sprintf("Prepare for %s", firstNonEmpty(event.Summary, "next event")), Reason: formatEvent(event, loc)})
	}
	if len(report.PendingMemoryCandidates) > 0 {
		actions = append(actions, Action{Kind: "memory_review", Title: "Review pending memory candidates", Reason: fmt.Sprintf("%d items are waiting for confirmation", len(report.PendingMemoryCandidates))})
	}
	if len(report.RecentCommitments) > 0 {
		actions = append(actions, Action{Kind: "commitment_capture", Title: "Convert recent commitments into tracked tasks", Reason: fmt.Sprintf("%d commitments are visible in recent session history", len(report.RecentCommitments))})
	}
	if len(actions) > 5 {
		return actions[:5]
	}
	return actions
}

func extractRecentCommitments(history []types.Message, historyLimit int, commitmentLimit int, existingKeys map[string]struct{}) []Commitment {
	if historyLimit <= 0 || commitmentLimit <= 0 || len(history) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(existingKeys))
	for key := range existingKeys {
		seen[key] = struct{}{}
	}
	start := 0
	if len(history) > historyLimit {
		start = len(history) - historyLimit
	}
	commitments := make([]Commitment, 0, commitmentLimit)
	for index := len(history) - 1; index >= start; index-- {
		message := history[index]
		if message.Role == "system" || message.Role == "tool" {
			continue
		}
		content := strings.TrimSpace(message.Content)
		if content == "" || strings.HasPrefix(content, "/") {
			continue
		}
		for _, input := range tasks.ExtractCandidateInputs(content) {
			key := titleKey(input.Title)
			if key == "" {
				continue
			}
			if _, ok := seen[key]; ok {
				continue
			}
			seen[key] = struct{}{}
			commitments = append(commitments, Commitment{Title: input.Title, Details: input.Details, SourceRole: message.Role, SourceExcerpt: clip(content, 160)})
			if len(commitments) >= commitmentLimit {
				return commitments
			}
		}
	}
	return commitments
}

func selectDueTasks(items []tasks.Task, now int64, windowEnd int64, limit int) []tasks.Task {
	if limit <= 0 {
		return nil
	}
	horizon := windowEnd
	if horizon <= 0 {
		horizon = now + int64((24 * time.Hour).Seconds())
	}
	out := make([]tasks.Task, 0, limit)
	for _, item := range items {
		if item.Status == tasks.TaskStatusCompleted {
			continue
		}
		if item.Status == tasks.TaskStatusSnoozed && item.SnoozeUntil > now {
			continue
		}
		if item.DueAt <= 0 || item.DueAt > horizon {
			continue
		}
		out = append(out, item)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].DueAt == out[j].DueAt {
			return out[i].UpdatedAt > out[j].UpdatedAt
		}
		return out[i].DueAt < out[j].DueAt
	})
	if len(out) > limit {
		out = out[:limit]
	}
	return out
}

func selectStaleWaiting(items []tasks.WaitingOn, kind Kind, now int64, limit int) []WaitingDigest {
	if limit <= 0 {
		return nil
	}
	threshold := int64((72 * time.Hour).Seconds())
	if kind == KindWeekly {
		threshold = int64((7 * 24 * time.Hour).Seconds())
	}
	out := make([]WaitingDigest, 0, limit)
	for _, item := range items {
		reason := staleReason(item, now, threshold)
		if reason == "" {
			continue
		}
		out = append(out, WaitingDigest{Waiting: item, Reason: reason})
	}
	sort.Slice(out, func(i, j int) bool {
		return waitingSortTime(out[i].Waiting) < waitingSortTime(out[j].Waiting)
	})
	if len(out) > limit {
		out = out[:limit]
	}
	return out
}

func staleReason(item tasks.WaitingOn, now int64, ageThreshold int64) string {
	switch item.Status {
	case tasks.WaitingStatusResolved:
		return ""
	case tasks.WaitingStatusSnoozed:
		if item.SnoozeUntil > 0 && item.SnoozeUntil <= now {
			return "snooze expired"
		}
	case tasks.WaitingStatusOpen:
		if item.FollowUpAt > 0 && item.FollowUpAt <= now {
			return "follow-up overdue"
		}
		if item.RemindAt > 0 && item.RemindAt <= now {
			return "reminder due"
		}
	}
	if item.UpdatedAt > 0 && now-item.UpdatedAt >= ageThreshold {
		return fmt.Sprintf("open for %d days", maxInt64(1, (now-item.UpdatedAt)/86400))
	}
	return ""
}

func waitingSortTime(item tasks.WaitingOn) int64 {
	for _, candidate := range []int64{item.SnoozeUntil, item.FollowUpAt, item.RemindAt, item.UpdatedAt, item.CreatedAt} {
		if candidate > 0 {
			return candidate
		}
	}
	return 0
}

func formatEvent(event calendar.Event, loc *time.Location) string {
	start := time.Unix(event.StartAt, 0).In(loc)
	end := time.Unix(event.EndAt, 0).In(loc)
	if event.AllDay {
		return start.Format("2006-01-02") + " all day"
	}
	if event.EndAt <= event.StartAt || event.EndAt == 0 {
		return start.Format(time.RFC3339)
	}
	return start.Format(time.RFC3339) + " to " + end.Format(time.RFC3339)
}

func dueLabel(unix int64, loc *time.Location) string {
	if unix <= 0 {
		return "no due date"
	}
	return "due " + time.Unix(unix, 0).In(loc).Format(time.RFC3339)
}

func agendaCount(agenda *calendar.Agenda) int {
	if agenda == nil {
		return 0
	}
	return len(agenda.Events)
}

func loadLocation(raw string) (*time.Location, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return time.Local, nil
	}
	loc, err := time.LoadLocation(raw)
	if err != nil {
		return nil, fmt.Errorf("invalid timezone %q: %w", raw, err)
	}
	return loc, nil
}

func defaultHistoryLimit(kind Kind) int {
	if kind == KindWeekly {
		return 30
	}
	return 12
}

func positiveOrDefault(value int, fallback int) int {
	if value > 0 {
		return value
	}
	return fallback
}

func titleKey(value string) string {
	return strings.Join(strings.Fields(strings.ToLower(strings.TrimSpace(value))), " ")
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func clip(value string, limit int) string {
	value = strings.TrimSpace(value)
	if limit <= 0 || len(value) <= limit {
		return value
	}
	if limit <= 3 {
		return value[:limit]
	}
	return strings.TrimSpace(value[:limit-3]) + "..."
}

func maxInt64(a int64, b int64) int64 {
	if a > b {
		return a
	}
	return b
}
