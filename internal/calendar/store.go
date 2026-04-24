package calendar

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	_ "modernc.org/sqlite"

	rrule "github.com/teambition/rrule-go"
)

type SourceKind string

const (
	SourceKindLocalICS  SourceKind = "local_ics"
	SourceKindRemoteICS SourceKind = "remote_ics"
)

type AgendaScope string

const (
	AgendaScopeToday        AgendaScope = "today"
	AgendaScopeThisWeek     AgendaScope = "this_week"
	AgendaScopeNextConflict AgendaScope = "next_conflict"
)

type Source struct {
	ID            string     `json:"id"`
	PeerID        string     `json:"peer_id"`
	Name          string     `json:"name"`
	Kind          SourceKind `json:"kind"`
	Path          string     `json:"path,omitempty"`
	URL           string     `json:"url,omitempty"`
	Timezone      string     `json:"timezone,omitempty"`
	Enabled       bool       `json:"enabled"`
	CreatedAt     int64      `json:"created_at"`
	UpdatedAt     int64      `json:"updated_at"`
	LastSuccessAt int64      `json:"last_success_at,omitempty"`
	LastError     string     `json:"last_error,omitempty"`
}

type SourceInput struct {
	Name     string
	Path     string
	URL      string
	Timezone string
	Enabled  *bool
}

type Event struct {
	SourceID    string `json:"source_id"`
	SourceName  string `json:"source_name"`
	UID         string `json:"uid,omitempty"`
	Summary     string `json:"summary"`
	Description string `json:"description,omitempty"`
	Location    string `json:"location,omitempty"`
	Status      string `json:"status,omitempty"`
	StartAt     int64  `json:"start_at"`
	EndAt       int64  `json:"end_at"`
	AllDay      bool   `json:"all_day,omitempty"`
}

type Conflict struct {
	StartAt int64   `json:"start_at"`
	EndAt   int64   `json:"end_at"`
	Events  []Event `json:"events"`
}

type AgendaQuery struct {
	Scope    string `json:"scope"`
	Timezone string `json:"timezone,omitempty"`
	Now      int64  `json:"now,omitempty"`
	Limit    int    `json:"limit,omitempty"`
}

type Agenda struct {
	Scope       string    `json:"scope"`
	Timezone    string    `json:"timezone"`
	RangeStart  int64     `json:"range_start,omitempty"`
	RangeEnd    int64     `json:"range_end,omitempty"`
	Count       int       `json:"count"`
	Events      []Event   `json:"events,omitempty"`
	Conflict    *Conflict `json:"conflict,omitempty"`
	Warnings    []string  `json:"warnings,omitempty"`
	Sources     []Source  `json:"sources,omitempty"`
	GeneratedAt int64     `json:"generated_at"`
}

type Store struct {
	db *sql.DB
}

type icsEvent struct {
	UID          string
	Summary      string
	Description  string
	Location     string
	Status       string
	Start        time.Time
	End          time.Time
	AllDay       bool
	RRule        string
	RDates       []time.Time
	ExDates      []time.Time
	RecurrenceID *time.Time
}

type icsProperty struct {
	name   string
	params map[string]string
	value  string
}

func New(dbPath string) (*Store, error) {
	db, err := sql.Open("sqlite", dbPath+"?_journal_mode=WAL&_foreign_keys=on")
	if err != nil {
		return nil, fmt.Errorf("calendar: open db: %w", err)
	}
	db.SetMaxOpenConns(1)
	if err := migrate(db); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("calendar: migrate: %w", err)
	}
	return &Store{db: db}, nil
}

func (s *Store) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

func (s *Store) CreateSource(ctx context.Context, peerID string, input SourceInput) (*Source, error) {
	input, err := normalizeSourceInput(input)
	if err != nil {
		return nil, err
	}
	peerID = strings.TrimSpace(peerID)
	if peerID == "" {
		return nil, fmt.Errorf("peer_id is required")
	}
	now := time.Now().Unix()
	source := &Source{
		ID:        randomID(),
		PeerID:    peerID,
		Name:      input.Name,
		Kind:      sourceKindFromInput(input),
		Path:      input.Path,
		URL:       input.URL,
		Timezone:  input.Timezone,
		Enabled:   sourceEnabled(input),
		CreatedAt: now,
		UpdatedAt: now,
	}
	if _, err := s.db.ExecContext(ctx, `INSERT INTO calendar_sources(id, peer_id, name, kind, path, url, timezone, enabled, created_at, updated_at, last_success_at, last_error) VALUES (?,?,?,?,?,?,?,?,?,?,?,?)`,
		source.ID, source.PeerID, source.Name, source.Kind, source.Path, source.URL, source.Timezone, boolToInt(source.Enabled), source.CreatedAt, source.UpdatedAt, 0, ""); err != nil {
		return nil, fmt.Errorf("calendar source create: %w", err)
	}
	return source, nil
}

func (s *Store) ListSources(ctx context.Context, peerID string, enabledOnly bool) ([]Source, error) {
	peerID = strings.TrimSpace(peerID)
	var (
		rows *sql.Rows
		err  error
	)
	if enabledOnly {
		rows, err = s.db.QueryContext(ctx, `SELECT id, peer_id, name, kind, path, url, timezone, enabled, created_at, updated_at, last_success_at, last_error FROM calendar_sources WHERE peer_id = ? AND enabled = 1 ORDER BY created_at ASC`, peerID)
	} else {
		rows, err = s.db.QueryContext(ctx, `SELECT id, peer_id, name, kind, path, url, timezone, enabled, created_at, updated_at, last_success_at, last_error FROM calendar_sources WHERE peer_id = ? ORDER BY created_at ASC`, peerID)
	}
	if err != nil {
		return nil, fmt.Errorf("calendar source list: %w", err)
	}
	defer rows.Close()
	var out []Source
	for rows.Next() {
		source, err := scanSource(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, source)
	}
	return out, rows.Err()
}

func (s *Store) DeleteSource(ctx context.Context, peerID, sourceID string) error {
	peerID = strings.TrimSpace(peerID)
	sourceID = strings.TrimSpace(sourceID)
	if sourceID == "" {
		return fmt.Errorf("id is required")
	}
	res, err := s.db.ExecContext(ctx, `DELETE FROM calendar_sources WHERE peer_id = ? AND id = ?`, peerID, sourceID)
	if err != nil {
		return fmt.Errorf("calendar source delete: %w", err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("calendar source delete: rows affected: %w", err)
	}
	if affected == 0 {
		return fmt.Errorf("calendar source %s not found", sourceID)
	}
	return nil
}

func (s *Store) Agenda(ctx context.Context, peerID string, query AgendaQuery, client *http.Client) (*Agenda, error) {
	sources, err := s.ListSources(ctx, peerID, true)
	if err != nil {
		return nil, err
	}
	loc, err := loadLocation(query.Timezone)
	if err != nil {
		return nil, err
	}
	now := queryNow(query.Now, loc)
	scope, err := normalizeAgendaScope(query.Scope)
	if err != nil {
		return nil, err
	}
	limit := query.Limit
	if limit <= 0 {
		limit = 20
	}
	windowStart, windowEnd := agendaWindow(scope, now)
	lookaheadEnd := windowEnd
	if scope == AgendaScopeNextConflict {
		lookaheadEnd = now.Add(30 * 24 * time.Hour)
	}
	out := &Agenda{
		Scope:       string(scope),
		Timezone:    loc.String(),
		RangeStart:  windowStart.Unix(),
		RangeEnd:    windowEnd.Unix(),
		Warnings:    []string{},
		Sources:     append([]Source(nil), sources...),
		GeneratedAt: time.Now().Unix(),
	}
	if len(sources) == 0 {
		return out, nil
	}
	allEvents := make([]Event, 0, 32)
	for _, source := range sources {
		events, loadErr := loadSourceEvents(ctx, source, client, loc, windowStart, lookaheadEnd)
		if loadErr != nil {
			out.Warnings = append(out.Warnings, fmt.Sprintf("%s: %v", source.Name, loadErr))
			_ = s.recordFetchResult(ctx, source.ID, 0, loadErr.Error())
			continue
		}
		_ = s.recordFetchResult(ctx, source.ID, time.Now().Unix(), "")
		allEvents = append(allEvents, events...)
	}
	sort.Slice(allEvents, func(i, j int) bool {
		if allEvents[i].StartAt != allEvents[j].StartAt {
			return allEvents[i].StartAt < allEvents[j].StartAt
		}
		if allEvents[i].EndAt != allEvents[j].EndAt {
			return allEvents[i].EndAt < allEvents[j].EndAt
		}
		if allEvents[i].SourceName != allEvents[j].SourceName {
			return allEvents[i].SourceName < allEvents[j].SourceName
		}
		return allEvents[i].Summary < allEvents[j].Summary
	})
	if scope == AgendaScopeNextConflict {
		conflict := findNextConflict(allEvents, now.Unix())
		out.Conflict = conflict
		if conflict != nil {
			out.Events = append(out.Events, conflict.Events...)
			out.Count = len(conflict.Events)
		}
		return out, nil
	}
	filtered := filterEventsInRange(allEvents, windowStart.Unix(), windowEnd.Unix())
	if len(filtered) > limit {
		filtered = filtered[:limit]
	}
	out.Events = filtered
	out.Count = len(filtered)
	return out, nil
}

func loadSourceEvents(ctx context.Context, source Source, client *http.Client, queryLoc *time.Location, windowStart, windowEnd time.Time) ([]Event, error) {
	defaultLoc := queryLoc
	if tz := strings.TrimSpace(source.Timezone); tz != "" {
		loc, err := time.LoadLocation(tz)
		if err != nil {
			return nil, fmt.Errorf("invalid source timezone %q: %w", tz, err)
		}
		defaultLoc = loc
	}
	content, err := loadICSContent(ctx, source, client)
	if err != nil {
		return nil, err
	}
	parsed, err := parseICS(content, defaultLoc)
	if err != nil {
		return nil, err
	}
	return expandEvents(source, parsed, windowStart, windowEnd)
}

func expandEvents(source Source, parsed []icsEvent, windowStart, windowEnd time.Time) ([]Event, error) {
	overrides := make(map[string]icsEvent)
	masters := make([]icsEvent, 0, len(parsed))
	for _, item := range parsed {
		if item.RecurrenceID != nil {
			overrides[overrideKey(item.UID, *item.RecurrenceID)] = item
			continue
		}
		masters = append(masters, item)
	}
	out := make([]Event, 0, len(masters))
	for _, item := range masters {
		duration := item.End.Sub(item.Start)
		if duration < 0 {
			duration = 0
		}
		if item.RRule == "" && len(item.RDates) == 0 {
			if eventIntersects(item.Start.Unix(), item.End.Unix(), windowStart.Unix(), windowEnd.Unix()) {
				out = append(out, buildEvent(source, item, item.Start, item.End))
			}
			continue
		}
		set := &rrule.Set{}
		set.DTStart(item.Start)
		if strings.TrimSpace(item.RRule) != "" {
			opt, err := rrule.StrToROption(item.RRule)
			if err != nil {
				return nil, fmt.Errorf("event %s: parse rrule: %w", item.UID, err)
			}
			opt.Dtstart = item.Start
			rr, err := rrule.NewRRule(*opt)
			if err != nil {
				return nil, fmt.Errorf("event %s: build rrule: %w", item.UID, err)
			}
			set.RRule(rr)
		}
		for _, rdate := range item.RDates {
			set.RDate(rdate)
		}
		for _, exdate := range item.ExDates {
			set.ExDate(exdate)
		}
		occurrences := set.Between(windowStart, windowEnd, true)
		for _, occ := range occurrences {
			start := occ
			end := occ.Add(duration)
			occurrence := item
			if override, ok := overrides[overrideKey(item.UID, occ)]; ok {
				occurrence = override
				start = override.Start
				end = override.End
			}
			if !eventIntersects(start.Unix(), end.Unix(), windowStart.Unix(), windowEnd.Unix()) {
				continue
			}
			out = append(out, buildEvent(source, occurrence, start, end))
		}
	}
	return out, nil
}

func buildEvent(source Source, item icsEvent, start, end time.Time) Event {
	if end.Before(start) {
		end = start
	}
	return Event{
		SourceID:    source.ID,
		SourceName:  source.Name,
		UID:         item.UID,
		Summary:     firstNonEmpty(item.Summary, "Untitled event"),
		Description: item.Description,
		Location:    item.Location,
		Status:      strings.ToLower(strings.TrimSpace(item.Status)),
		StartAt:     start.Unix(),
		EndAt:       end.Unix(),
		AllDay:      item.AllDay,
	}
}

func loadICSContent(ctx context.Context, source Source, client *http.Client) ([]byte, error) {
	switch source.Kind {
	case SourceKindLocalICS:
		content, err := os.ReadFile(source.Path)
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", source.Path, err)
		}
		return content, nil
	case SourceKindRemoteICS:
		if client == nil {
			client = &http.Client{Timeout: 15 * time.Second}
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, source.URL, nil)
		if err != nil {
			return nil, fmt.Errorf("build request: %w", err)
		}
		resp, err := client.Do(req)
		if err != nil {
			return nil, fmt.Errorf("fetch %s: %w", source.URL, err)
		}
		defer resp.Body.Close()
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			return nil, fmt.Errorf("fetch %s: unexpected status %d", source.URL, resp.StatusCode)
		}
		content, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", source.URL, err)
		}
		return content, nil
	default:
		return nil, fmt.Errorf("unsupported source kind %q", source.Kind)
	}
}

func parseICS(content []byte, defaultLoc *time.Location) ([]icsEvent, error) {
	lines := unfoldLines(content)
	events := make([]icsEvent, 0, 8)
	insideEvent := false
	props := make([]icsProperty, 0, 16)
	for _, line := range lines {
		switch strings.TrimSpace(line) {
		case "BEGIN:VEVENT":
			insideEvent = true
			props = props[:0]
			continue
		case "END:VEVENT":
			if !insideEvent {
				continue
			}
			event, err := buildICSEvent(props, defaultLoc)
			if err != nil {
				return nil, err
			}
			events = append(events, event)
			insideEvent = false
			continue
		}
		if !insideEvent {
			continue
		}
		prop, ok := parsePropertyLine(line)
		if !ok {
			continue
		}
		props = append(props, prop)
	}
	return events, nil
}

func buildICSEvent(props []icsProperty, defaultLoc *time.Location) (icsEvent, error) {
	var event icsEvent
	for _, prop := range props {
		switch prop.name {
		case "UID":
			event.UID = unescapeICSText(prop.value)
		case "SUMMARY":
			event.Summary = unescapeICSText(prop.value)
		case "DESCRIPTION":
			event.Description = unescapeICSText(prop.value)
		case "LOCATION":
			event.Location = unescapeICSText(prop.value)
		case "STATUS":
			event.Status = unescapeICSText(prop.value)
		case "RRULE":
			event.RRule = strings.TrimSpace(prop.value)
		case "DTSTART":
			start, allDay, err := parseTimeValue(prop.value, prop.params, defaultLoc)
			if err != nil {
				return event, fmt.Errorf("parse DTSTART: %w", err)
			}
			event.Start = start
			event.AllDay = allDay
		case "DTEND":
			end, _, err := parseTimeValue(prop.value, prop.params, defaultLoc)
			if err != nil {
				return event, fmt.Errorf("parse DTEND: %w", err)
			}
			event.End = end
		case "RECURRENCE-ID":
			recurID, _, err := parseTimeValue(prop.value, prop.params, defaultLoc)
			if err != nil {
				return event, fmt.Errorf("parse RECURRENCE-ID: %w", err)
			}
			event.RecurrenceID = &recurID
		case "EXDATE":
			values, err := parseMultiTimes(prop.value, prop.params, defaultLoc)
			if err != nil {
				return event, fmt.Errorf("parse EXDATE: %w", err)
			}
			event.ExDates = append(event.ExDates, values...)
		case "RDATE":
			values, err := parseMultiTimes(prop.value, prop.params, defaultLoc)
			if err != nil {
				return event, fmt.Errorf("parse RDATE: %w", err)
			}
			event.RDates = append(event.RDates, values...)
		}
	}
	if event.Start.IsZero() {
		return event, fmt.Errorf("event missing DTSTART")
	}
	if event.End.IsZero() {
		if event.AllDay {
			event.End = event.Start.Add(24 * time.Hour)
		} else {
			event.End = event.Start
		}
	}
	if event.UID == "" {
		event.UID = randomID()
	}
	return event, nil
}

func parseMultiTimes(raw string, params map[string]string, defaultLoc *time.Location) ([]time.Time, error) {
	parts := strings.Split(strings.TrimSpace(raw), ",")
	out := make([]time.Time, 0, len(parts))
	for _, part := range parts {
		parsed, _, err := parseTimeValue(strings.TrimSpace(part), params, defaultLoc)
		if err != nil {
			return nil, err
		}
		out = append(out, parsed)
	}
	return out, nil
}

func parseTimeValue(raw string, params map[string]string, defaultLoc *time.Location) (time.Time, bool, error) {
	value := strings.TrimSpace(raw)
	loc := defaultLoc
	if tzid := strings.TrimSpace(params["TZID"]); tzid != "" {
		loaded, err := time.LoadLocation(tzid)
		if err != nil {
			return time.Time{}, false, fmt.Errorf("load timezone %q: %w", tzid, err)
		}
		loc = loaded
	}
	if strings.EqualFold(strings.TrimSpace(params["VALUE"]), "DATE") || len(value) == len("20060102") {
		parsed, err := time.ParseInLocation("20060102", value, loc)
		return parsed, true, err
	}
	if strings.HasSuffix(value, "Z") {
		parsed, err := time.Parse("20060102T150405Z", value)
		return parsed, false, err
	}
	parsed, err := time.ParseInLocation("20060102T150405", value, loc)
	return parsed, false, err
}

func unfoldLines(content []byte) []string {
	scanner := bufio.NewScanner(bytes.NewReader(content))
	lines := make([]string, 0, 64)
	for scanner.Scan() {
		line := strings.TrimRight(scanner.Text(), "\r")
		if len(lines) > 0 && len(line) > 0 && (line[0] == ' ' || line[0] == '\t') {
			lines[len(lines)-1] += strings.TrimLeft(line, " \t")
			continue
		}
		lines = append(lines, line)
	}
	return lines
}

func parsePropertyLine(line string) (icsProperty, bool) {
	idx := strings.IndexByte(line, ':')
	if idx <= 0 {
		return icsProperty{}, false
	}
	head := line[:idx]
	value := line[idx+1:]
	parts := strings.Split(head, ";")
	prop := icsProperty{name: strings.ToUpper(strings.TrimSpace(parts[0])), params: map[string]string{}, value: value}
	for _, raw := range parts[1:] {
		kv := strings.SplitN(raw, "=", 2)
		if len(kv) != 2 {
			continue
		}
		prop.params[strings.ToUpper(strings.TrimSpace(kv[0]))] = strings.Trim(strings.TrimSpace(kv[1]), `"`)
	}
	return prop, true
}

func filterEventsInRange(events []Event, startAt, endAt int64) []Event {
	out := make([]Event, 0, len(events))
	for _, event := range events {
		if eventIntersects(event.StartAt, event.EndAt, startAt, endAt) {
			out = append(out, event)
		}
	}
	return out
}

func eventIntersects(startAt, endAt, rangeStart, rangeEnd int64) bool {
	if endAt == 0 {
		endAt = startAt
	}
	return endAt >= rangeStart && startAt < rangeEnd
}

func findNextConflict(events []Event, nowUnix int64) *Conflict {
	for index := range events {
		current := events[index]
		if current.EndAt < nowUnix {
			continue
		}
		for nextIndex := index + 1; nextIndex < len(events); nextIndex++ {
			next := events[nextIndex]
			if next.StartAt >= current.EndAt {
				break
			}
			startAt := maxInt64(current.StartAt, next.StartAt)
			endAt := minInt64(current.EndAt, next.EndAt)
			if endAt <= startAt {
				continue
			}
			return &Conflict{StartAt: startAt, EndAt: endAt, Events: []Event{current, next}}
		}
	}
	return nil
}

func agendaWindow(scope AgendaScope, now time.Time) (time.Time, time.Time) {
	switch scope {
	case AgendaScopeThisWeek:
		start := startOfDay(now)
		weekday := int(now.Weekday())
		if weekday == 0 {
			weekday = 7
		}
		start = start.AddDate(0, 0, -(weekday - 1))
		return start, start.AddDate(0, 0, 7)
	case AgendaScopeNextConflict:
		return now, now.Add(30 * 24 * time.Hour)
	case AgendaScopeToday:
		fallthrough
	default:
		start := startOfDay(now)
		return start, start.AddDate(0, 0, 1)
	}
}

func startOfDay(value time.Time) time.Time {
	return time.Date(value.Year(), value.Month(), value.Day(), 0, 0, 0, 0, value.Location())
}

func normalizeAgendaScope(raw string) (AgendaScope, error) {
	scope := AgendaScope(strings.ToLower(strings.TrimSpace(raw)))
	switch scope {
	case "", AgendaScopeToday:
		return AgendaScopeToday, nil
	case AgendaScopeThisWeek, AgendaScopeNextConflict:
		return scope, nil
	default:
		return "", fmt.Errorf("invalid agenda scope %q", raw)
	}
}

func normalizeSourceInput(input SourceInput) (SourceInput, error) {
	input.Name = strings.TrimSpace(input.Name)
	input.Path = strings.TrimSpace(input.Path)
	input.URL = strings.TrimSpace(input.URL)
	input.Timezone = strings.TrimSpace(input.Timezone)
	if input.Path == "" && input.URL == "" {
		return input, fmt.Errorf("path or url is required")
	}
	if input.Path != "" && input.URL != "" {
		return input, fmt.Errorf("provide either path or url, not both")
	}
	if input.Path != "" {
		resolved, err := filepath.Abs(input.Path)
		if err != nil {
			return input, fmt.Errorf("resolve path: %w", err)
		}
		input.Path = resolved
		if !strings.HasSuffix(strings.ToLower(input.Path), ".ics") {
			return input, fmt.Errorf("path must point to a .ics file")
		}
	}
	if input.URL != "" {
		parsed, err := url.Parse(input.URL)
		if err != nil {
			return input, fmt.Errorf("invalid url: %w", err)
		}
		if parsed.Scheme != "http" && parsed.Scheme != "https" {
			return input, fmt.Errorf("url scheme must be http or https")
		}
	}
	if input.Timezone != "" {
		if _, err := time.LoadLocation(input.Timezone); err != nil {
			return input, fmt.Errorf("invalid timezone %q: %w", input.Timezone, err)
		}
	}
	if input.Name == "" {
		if input.Path != "" {
			input.Name = strings.TrimSuffix(filepath.Base(input.Path), filepath.Ext(input.Path))
		} else if input.URL != "" {
			parsed, _ := url.Parse(input.URL)
			input.Name = parsed.Host
		}
	}
	if input.Name == "" {
		return input, fmt.Errorf("name is required")
	}
	return input, nil
}

func sourceKindFromInput(input SourceInput) SourceKind {
	if strings.TrimSpace(input.URL) != "" {
		return SourceKindRemoteICS
	}
	return SourceKindLocalICS
}

func sourceEnabled(input SourceInput) bool {
	if input.Enabled == nil {
		return true
	}
	return *input.Enabled
}

func loadLocation(name string) (*time.Location, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return time.Local, nil
	}
	loc, err := time.LoadLocation(name)
	if err != nil {
		return nil, fmt.Errorf("load timezone %q: %w", name, err)
	}
	return loc, nil
}

func queryNow(nowUnix int64, loc *time.Location) time.Time {
	if nowUnix > 0 {
		return time.Unix(nowUnix, 0).In(loc)
	}
	return time.Now().In(loc)
}

func overrideKey(uid string, start time.Time) string {
	return strings.TrimSpace(uid) + "|" + start.UTC().Format(time.RFC3339)
}

func unescapeICSText(value string) string {
	value = strings.ReplaceAll(value, `\n`, "\n")
	value = strings.ReplaceAll(value, `\N`, "\n")
	value = strings.ReplaceAll(value, `\,`, ",")
	value = strings.ReplaceAll(value, `\;`, ";")
	value = strings.ReplaceAll(value, `\\`, `\`)
	return strings.TrimSpace(value)
}

func scanSource(scanner interface{ Scan(dest ...any) error }) (Source, error) {
	var (
		source  Source
		enabled int
	)
	if err := scanner.Scan(&source.ID, &source.PeerID, &source.Name, &source.Kind, &source.Path, &source.URL, &source.Timezone, &enabled, &source.CreatedAt, &source.UpdatedAt, &source.LastSuccessAt, &source.LastError); err != nil {
		return Source{}, fmt.Errorf("calendar source scan: %w", err)
	}
	source.Enabled = enabled != 0
	return source, nil
}

func (s *Store) recordFetchResult(ctx context.Context, sourceID string, successAt int64, lastError string) error {
	if s == nil || s.db == nil {
		return nil
	}
	_, err := s.db.ExecContext(ctx, `UPDATE calendar_sources SET last_success_at = ?, last_error = ?, updated_at = ? WHERE id = ?`, successAt, strings.TrimSpace(lastError), time.Now().Unix(), sourceID)
	return err
}

func migrate(db *sql.DB) error {
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS calendar_sources (
			id TEXT PRIMARY KEY,
			peer_id TEXT NOT NULL,
			name TEXT NOT NULL,
			kind TEXT NOT NULL,
			path TEXT NOT NULL DEFAULT '',
			url TEXT NOT NULL DEFAULT '',
			timezone TEXT NOT NULL DEFAULT '',
			enabled INTEGER NOT NULL DEFAULT 1,
			created_at INTEGER NOT NULL,
			updated_at INTEGER NOT NULL,
			last_success_at INTEGER NOT NULL DEFAULT 0,
			last_error TEXT NOT NULL DEFAULT ''
		)`,
		`CREATE INDEX IF NOT EXISTS idx_calendar_sources_peer_created ON calendar_sources(peer_id, created_at ASC)`,
	}
	for _, stmt := range stmts {
		if _, err := db.Exec(stmt); err != nil {
			return err
		}
	}
	return nil
}

func boolToInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		trimmed := strings.TrimSpace(value)
		if trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func randomID() string {
	buf := make([]byte, 8)
	_, _ = rand.Read(buf)
	return hex.EncodeToString(buf)
}

func maxInt64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}

func minInt64(a, b int64) int64 {
	if a < b {
		return a
	}
	return b
}
