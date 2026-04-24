package tasks

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"fmt"
	"regexp"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

type CandidateStatus string

const (
	CandidateStatusPending  CandidateStatus = "pending"
	CandidateStatusApproved CandidateStatus = "approved"
	CandidateStatusRejected CandidateStatus = "rejected"
	CandidateStatusAll      CandidateStatus = "all"
)

type TaskStatus string

const (
	TaskStatusOpen      TaskStatus = "open"
	TaskStatusSnoozed   TaskStatus = "snoozed"
	TaskStatusCompleted TaskStatus = "completed"
	TaskStatusAll       TaskStatus = "all"
)

const (
	CandidateCaptureManual          = "manual"
	CandidateCaptureAutoTurnExtract = "auto_turn_extract"
	CandidateCaptureExternalExtract = "external_extract"
)

type CandidateProvenance struct {
	CaptureKind      string
	SourceSessionKey string
	SourceExcerpt    string
}

type CandidateInput struct {
	Title   string
	Details string
	Owner   string
	DueAt   int64
}

type Candidate struct {
	ID               string          `json:"id"`
	PeerID           string          `json:"peer_id"`
	Title            string          `json:"title"`
	Details          string          `json:"details,omitempty"`
	Owner            string          `json:"owner,omitempty"`
	DueAt            int64           `json:"due_at,omitempty"`
	CreatedAt        int64           `json:"created_at"`
	UpdatedAt        int64           `json:"updated_at"`
	Status           CandidateStatus `json:"status"`
	ReviewReason     string          `json:"review_reason,omitempty"`
	ReviewedAt       int64           `json:"reviewed_at,omitempty"`
	ResultTaskID     string          `json:"result_task_id,omitempty"`
	CaptureKind      string          `json:"capture_kind,omitempty"`
	SourceSessionKey string          `json:"source_session_key,omitempty"`
	SourceExcerpt    string          `json:"source_excerpt,omitempty"`
}

type CandidatePatch struct {
	Title   *string
	Details *string
	Owner   *string
	DueAt   *int64
}

type Task struct {
	ID                string     `json:"id"`
	PeerID            string     `json:"peer_id"`
	Title             string     `json:"title"`
	Details           string     `json:"details,omitempty"`
	Owner             string     `json:"owner,omitempty"`
	Status            TaskStatus `json:"status"`
	DueAt             int64      `json:"due_at,omitempty"`
	SnoozeUntil       int64      `json:"snooze_until,omitempty"`
	CreatedAt         int64      `json:"created_at"`
	UpdatedAt         int64      `json:"updated_at"`
	CompletedAt       int64      `json:"completed_at,omitempty"`
	SourceCandidateID string     `json:"source_candidate_id,omitempty"`
	SourceSessionKey  string     `json:"source_session_key,omitempty"`
	SourceExcerpt     string     `json:"source_excerpt,omitempty"`
}

type TaskPatch struct {
	Title       *string
	Details     *string
	Owner       *string
	DueAt       *int64
	SnoozeUntil *int64
}

type Store struct {
	db *sql.DB
}

var (
	boxTaskRE          = regexp.MustCompile(`(?i)^\s*(?:[-*+]\s*)?\[ \]\s*(.+)$`)
	prefixedTaskRE     = regexp.MustCompile(`(?i)^\s*(?:[-*+]\s*)?(?:todo|task|action item|follow up|follow-up)\s*[:\-]\s*(.+)$`)
	needToRE           = regexp.MustCompile(`(?i)\b(?:i|we|you)\s+(?:need|should|must|have)\s+to\s+(.+)`)
	rememberToRE       = regexp.MustCompile(`(?i)\bremember\s+to\s+(.+)`)
	pleaseRE           = regexp.MustCompile(`(?i)^\s*please\s+(.+)`)
	followUpWithRE     = regexp.MustCompile(`(?i)\bfollow[- ]up\s+with\s+(.+)`)
	sentenceSplitterRE = regexp.MustCompile(`[\n.!?]+`)
)

func New(dbPath string) (*Store, error) {
	db, err := sql.Open("sqlite", dbPath+"?_journal_mode=WAL&_foreign_keys=on")
	if err != nil {
		return nil, fmt.Errorf("tasks: open db: %w", err)
	}
	db.SetMaxOpenConns(1)
	if err := migrate(db); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("tasks: migrate: %w", err)
	}
	return &Store{db: db}, nil
}

func (s *Store) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

func (s *Store) QueueCandidate(ctx context.Context, peerID string, input CandidateInput) (*Candidate, error) {
	return s.QueueCandidateWithProvenance(ctx, peerID, input, CandidateProvenance{})
}

func (s *Store) QueueCandidateWithProvenance(ctx context.Context, peerID string, input CandidateInput, provenance CandidateProvenance) (*Candidate, error) {
	input, err := normalizeCandidateInput(input)
	if err != nil {
		return nil, err
	}
	provenance = normalizeProvenance(provenance)
	now := time.Now().Unix()
	candidate := &Candidate{
		ID:               randomID(),
		PeerID:           strings.TrimSpace(peerID),
		Title:            input.Title,
		Details:          input.Details,
		Owner:            input.Owner,
		DueAt:            input.DueAt,
		CreatedAt:        now,
		UpdatedAt:        now,
		Status:           CandidateStatusPending,
		CaptureKind:      provenance.CaptureKind,
		SourceSessionKey: provenance.SourceSessionKey,
		SourceExcerpt:    provenance.SourceExcerpt,
	}
	if candidate.PeerID == "" {
		return nil, fmt.Errorf("peer_id is required")
	}
	if _, err := s.db.ExecContext(ctx, `INSERT INTO task_candidates(id, peer_id, title, details, owner, due_at, created_at, updated_at, status, review_reason, reviewed_at, result_task_id, capture_kind, source_session_key, source_excerpt) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		candidate.ID, candidate.PeerID, candidate.Title, candidate.Details, candidate.Owner, candidate.DueAt, candidate.CreatedAt, candidate.UpdatedAt, candidate.Status, "", 0, "", candidate.CaptureKind, candidate.SourceSessionKey, candidate.SourceExcerpt); err != nil {
		return nil, fmt.Errorf("task candidate create: %w", err)
	}
	return candidate, nil
}

func (s *Store) ExtractAndQueue(ctx context.Context, peerID, text string, provenance CandidateProvenance) ([]Candidate, error) {
	inputs := ExtractCandidateInputs(text)
	if len(inputs) == 0 {
		return nil, nil
	}
	seen, err := s.existingTaskKeys(ctx, peerID)
	if err != nil {
		return nil, err
	}
	out := make([]Candidate, 0, len(inputs))
	for _, input := range inputs {
		key := normalizeTaskKey(input.Title, input.Details)
		if key == "" {
			continue
		}
		if _, ok := seen[key]; ok {
			continue
		}
		candidate, err := s.QueueCandidateWithProvenance(ctx, peerID, input, provenance)
		if err != nil {
			return nil, err
		}
		out = append(out, *candidate)
		seen[key] = struct{}{}
	}
	return out, nil
}

func (s *Store) ListCandidates(ctx context.Context, peerID string, limit int, status CandidateStatus) ([]Candidate, error) {
	status, err := normalizeCandidateStatus(status)
	if err != nil {
		return nil, err
	}
	if limit <= 0 {
		limit = 50
	}
	var rows *sql.Rows
	if status == CandidateStatusAll {
		rows, err = s.db.QueryContext(ctx, `SELECT id, peer_id, title, details, owner, due_at, created_at, updated_at, status, review_reason, reviewed_at, result_task_id, capture_kind, source_session_key, source_excerpt FROM task_candidates WHERE peer_id = ? ORDER BY created_at DESC LIMIT ?`, strings.TrimSpace(peerID), limit)
	} else {
		rows, err = s.db.QueryContext(ctx, `SELECT id, peer_id, title, details, owner, due_at, created_at, updated_at, status, review_reason, reviewed_at, result_task_id, capture_kind, source_session_key, source_excerpt FROM task_candidates WHERE peer_id = ? AND status = ? ORDER BY created_at DESC LIMIT ?`, strings.TrimSpace(peerID), status, limit)
	}
	if err != nil {
		return nil, fmt.Errorf("task candidate list: %w", err)
	}
	defer rows.Close()
	var out []Candidate
	for rows.Next() {
		candidate, err := scanCandidate(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, candidate)
	}
	return out, rows.Err()
}

func (s *Store) EditCandidate(ctx context.Context, peerID, candidateID string, patch CandidatePatch) (*Candidate, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("task candidate edit: begin tx: %w", err)
	}
	defer tx.Rollback()
	candidate, err := loadCandidateTx(ctx, tx, peerID, candidateID)
	if err != nil {
		return nil, err
	}
	if candidate.Status != CandidateStatusPending {
		return nil, fmt.Errorf("candidate %s is already reviewed", candidateID)
	}
	applyCandidatePatch(candidate, patch)
	if err := validateCandidateRecord(*candidate); err != nil {
		return nil, err
	}
	candidate.UpdatedAt = time.Now().Unix()
	if err := updateCandidateTx(ctx, tx, *candidate); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("task candidate edit: commit: %w", err)
	}
	return candidate, nil
}

func (s *Store) ApproveCandidate(ctx context.Context, peerID, candidateID string, patch CandidatePatch, reason string) (*Candidate, *Task, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, nil, fmt.Errorf("task candidate approve: begin tx: %w", err)
	}
	defer tx.Rollback()
	candidate, err := loadCandidateTx(ctx, tx, peerID, candidateID)
	if err != nil {
		return nil, nil, err
	}
	if candidate.Status != CandidateStatusPending {
		return nil, nil, fmt.Errorf("candidate %s is already reviewed", candidateID)
	}
	applyCandidatePatch(candidate, patch)
	if err := validateCandidateRecord(*candidate); err != nil {
		return nil, nil, err
	}
	now := time.Now().Unix()
	task := &Task{
		ID:                randomID(),
		PeerID:            candidate.PeerID,
		Title:             candidate.Title,
		Details:           candidate.Details,
		Owner:             candidate.Owner,
		Status:            TaskStatusOpen,
		DueAt:             candidate.DueAt,
		CreatedAt:         now,
		UpdatedAt:         now,
		SourceCandidateID: candidate.ID,
		SourceSessionKey:  candidate.SourceSessionKey,
		SourceExcerpt:     candidate.SourceExcerpt,
	}
	if err := insertTaskTx(ctx, tx, *task); err != nil {
		return nil, nil, err
	}
	candidate.Status = CandidateStatusApproved
	candidate.ReviewReason = strings.TrimSpace(reason)
	candidate.ReviewedAt = now
	candidate.ResultTaskID = task.ID
	candidate.UpdatedAt = now
	if err := updateCandidateTx(ctx, tx, *candidate); err != nil {
		return nil, nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, nil, fmt.Errorf("task candidate approve: commit: %w", err)
	}
	return candidate, task, nil
}

func (s *Store) RejectCandidate(ctx context.Context, peerID, candidateID, reason string) (*Candidate, error) {
	if strings.TrimSpace(reason) == "" {
		return nil, fmt.Errorf("reason is required")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("task candidate reject: begin tx: %w", err)
	}
	defer tx.Rollback()
	candidate, err := loadCandidateTx(ctx, tx, peerID, candidateID)
	if err != nil {
		return nil, err
	}
	if candidate.Status != CandidateStatusPending {
		return nil, fmt.Errorf("candidate %s is already reviewed", candidateID)
	}
	now := time.Now().Unix()
	candidate.Status = CandidateStatusRejected
	candidate.ReviewReason = strings.TrimSpace(reason)
	candidate.ReviewedAt = now
	candidate.UpdatedAt = now
	if err := updateCandidateTx(ctx, tx, *candidate); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("task candidate reject: commit: %w", err)
	}
	return candidate, nil
}

func (s *Store) ListTasks(ctx context.Context, peerID string, limit int, status TaskStatus) ([]Task, error) {
	status, err := normalizeTaskStatus(status)
	if err != nil {
		return nil, err
	}
	if limit <= 0 {
		limit = 50
	}
	var rows *sql.Rows
	if status == TaskStatusAll {
		rows, err = s.db.QueryContext(ctx, `SELECT id, peer_id, title, details, owner, status, due_at, snooze_until, created_at, updated_at, completed_at, source_candidate_id, source_session_key, source_excerpt FROM tasks WHERE peer_id = ? ORDER BY updated_at DESC LIMIT ?`, strings.TrimSpace(peerID), limit)
	} else {
		rows, err = s.db.QueryContext(ctx, `SELECT id, peer_id, title, details, owner, status, due_at, snooze_until, created_at, updated_at, completed_at, source_candidate_id, source_session_key, source_excerpt FROM tasks WHERE peer_id = ? AND status = ? ORDER BY updated_at DESC LIMIT ?`, strings.TrimSpace(peerID), status, limit)
	}
	if err != nil {
		return nil, fmt.Errorf("task list: %w", err)
	}
	defer rows.Close()
	var out []Task
	for rows.Next() {
		task, err := scanTask(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, task)
	}
	return out, rows.Err()
}

func (s *Store) GetTask(ctx context.Context, peerID, taskID string) (*Task, error) {
	row := s.db.QueryRowContext(ctx, `SELECT id, peer_id, title, details, owner, status, due_at, snooze_until, created_at, updated_at, completed_at, source_candidate_id, source_session_key, source_excerpt FROM tasks WHERE peer_id = ? AND id = ?`, strings.TrimSpace(peerID), strings.TrimSpace(taskID))
	task, err := scanTaskRow(row)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, fmt.Errorf("task %s not found", taskID)
		}
		return nil, fmt.Errorf("task get: %w", err)
	}
	return task, nil
}

func (s *Store) UpdateTask(ctx context.Context, peerID, taskID string, patch TaskPatch) (*Task, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("task update: begin tx: %w", err)
	}
	defer tx.Rollback()
	task, err := loadTaskTx(ctx, tx, peerID, taskID)
	if err != nil {
		return nil, err
	}
	applyTaskPatch(task, patch)
	if err := validateTaskRecord(*task); err != nil {
		return nil, err
	}
	task.UpdatedAt = time.Now().Unix()
	if err := updateTaskTx(ctx, tx, *task); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("task update: commit: %w", err)
	}
	return task, nil
}

func (s *Store) AssignTask(ctx context.Context, peerID, taskID, owner string) (*Task, error) {
	owner = strings.TrimSpace(owner)
	if owner == "" {
		return nil, fmt.Errorf("owner is required")
	}
	return s.UpdateTask(ctx, peerID, taskID, TaskPatch{Owner: &owner})
}

func (s *Store) SnoozeTask(ctx context.Context, peerID, taskID string, until int64) (*Task, error) {
	if until <= 0 {
		return nil, fmt.Errorf("until is required")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("task snooze: begin tx: %w", err)
	}
	defer tx.Rollback()
	task, err := loadTaskTx(ctx, tx, peerID, taskID)
	if err != nil {
		return nil, err
	}
	task.Status = TaskStatusSnoozed
	task.SnoozeUntil = until
	task.UpdatedAt = time.Now().Unix()
	if err := validateTaskRecord(*task); err != nil {
		return nil, err
	}
	if err := updateTaskTx(ctx, tx, *task); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("task snooze: commit: %w", err)
	}
	return task, nil
}

func (s *Store) CompleteTask(ctx context.Context, peerID, taskID string) (*Task, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("task complete: begin tx: %w", err)
	}
	defer tx.Rollback()
	task, err := loadTaskTx(ctx, tx, peerID, taskID)
	if err != nil {
		return nil, err
	}
	now := time.Now().Unix()
	task.Status = TaskStatusCompleted
	task.CompletedAt = now
	task.SnoozeUntil = 0
	task.UpdatedAt = now
	if err := updateTaskTx(ctx, tx, *task); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("task complete: commit: %w", err)
	}
	return task, nil
}

func (s *Store) ReopenTask(ctx context.Context, peerID, taskID string) (*Task, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("task reopen: begin tx: %w", err)
	}
	defer tx.Rollback()
	task, err := loadTaskTx(ctx, tx, peerID, taskID)
	if err != nil {
		return nil, err
	}
	task.Status = TaskStatusOpen
	task.CompletedAt = 0
	task.SnoozeUntil = 0
	task.UpdatedAt = time.Now().Unix()
	if err := updateTaskTx(ctx, tx, *task); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("task reopen: commit: %w", err)
	}
	return task, nil
}

func ExtractCandidateInputs(text string) []CandidateInput {
	text = strings.TrimSpace(strings.ReplaceAll(text, "\r\n", "\n"))
	if text == "" {
		return nil
	}
	units := make([]string, 0)
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			units = append(units, line)
		}
	}
	for _, part := range sentenceSplitterRE.Split(text, -1) {
		part = strings.TrimSpace(part)
		if part != "" {
			units = append(units, part)
		}
	}
	seenUnits := make(map[string]struct{}, len(units))
	seenTasks := make(map[string]struct{})
	out := make([]CandidateInput, 0, 5)
	for _, unit := range units {
		unit = normalizeWhitespace(unit)
		if unit == "" {
			continue
		}
		for _, part := range splitCompoundUnit(unit) {
			part = normalizeWhitespace(part)
			if part == "" {
				continue
			}
			unitKey := strings.ToLower(trimTaskPunctuation(part))
			if _, ok := seenUnits[unitKey]; ok {
				continue
			}
			seenUnits[unitKey] = struct{}{}
			input, ok := extractCandidateInput(part)
			if !ok {
				continue
			}
			key := normalizeTaskKey(input.Title, input.Details)
			if key == "" {
				continue
			}
			if _, ok := seenTasks[key]; ok {
				continue
			}
			seenTasks[key] = struct{}{}
			out = append(out, input)
			if len(out) == 5 {
				break
			}
		}
		if len(out) == 5 {
			break
		}
	}
	return out
}

func splitCompoundUnit(unit string) []string {
	parts := []string{unit}
	for _, marker := range []string{" and please ", " and remember to ", " and follow up with "} {
		next := make([]string, 0, len(parts)+1)
		for _, part := range parts {
			idx := strings.Index(strings.ToLower(part), marker)
			if idx <= 0 {
				next = append(next, part)
				continue
			}
			next = append(next, strings.TrimSpace(part[:idx]))
			next = append(next, strings.TrimSpace(part[idx+5:]))
		}
		parts = next
	}
	return parts
}

func extractCandidateInput(unit string) (CandidateInput, bool) {
	cleaned := strings.TrimSpace(unit)
	var action string
	switch {
	case boxTaskRE.MatchString(cleaned):
		action = boxTaskRE.FindStringSubmatch(cleaned)[1]
	case prefixedTaskRE.MatchString(cleaned):
		action = prefixedTaskRE.FindStringSubmatch(cleaned)[1]
	case needToRE.MatchString(cleaned):
		action = needToRE.FindStringSubmatch(cleaned)[1]
	case rememberToRE.MatchString(cleaned):
		action = rememberToRE.FindStringSubmatch(cleaned)[1]
	case pleaseRE.MatchString(cleaned):
		action = pleaseRE.FindStringSubmatch(cleaned)[1]
	case followUpWithRE.MatchString(cleaned):
		action = "follow up with " + followUpWithRE.FindStringSubmatch(cleaned)[1]
	default:
		return CandidateInput{}, false
	}
	action = normalizeWhitespace(trimTaskPunctuation(action))
	if action == "" || len(action) < 4 {
		return CandidateInput{}, false
	}
	title := action
	if len(title) > 120 {
		title = strings.TrimSpace(title[:120])
	}
	title = strings.ToUpper(title[:1]) + title[1:]
	details := cleaned
	if strings.EqualFold(details, title) {
		details = ""
	}
	return CandidateInput{Title: title, Details: details}, true
}

func migrate(db *sql.DB) error {
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS task_candidates (
			id TEXT PRIMARY KEY,
			peer_id TEXT NOT NULL,
			title TEXT NOT NULL,
			details TEXT NOT NULL DEFAULT '',
			owner TEXT NOT NULL DEFAULT '',
			due_at INTEGER NOT NULL DEFAULT 0,
			created_at INTEGER NOT NULL,
			updated_at INTEGER NOT NULL,
			status TEXT NOT NULL,
			review_reason TEXT NOT NULL DEFAULT '',
			reviewed_at INTEGER NOT NULL DEFAULT 0,
			result_task_id TEXT NOT NULL DEFAULT '',
			capture_kind TEXT NOT NULL DEFAULT '',
			source_session_key TEXT NOT NULL DEFAULT '',
			source_excerpt TEXT NOT NULL DEFAULT ''
		)`,
		`CREATE INDEX IF NOT EXISTS idx_task_candidates_peer_status_created ON task_candidates(peer_id, status, created_at DESC)`,
		`CREATE TABLE IF NOT EXISTS tasks (
			id TEXT PRIMARY KEY,
			peer_id TEXT NOT NULL,
			title TEXT NOT NULL,
			details TEXT NOT NULL DEFAULT '',
			owner TEXT NOT NULL DEFAULT '',
			status TEXT NOT NULL,
			due_at INTEGER NOT NULL DEFAULT 0,
			snooze_until INTEGER NOT NULL DEFAULT 0,
			created_at INTEGER NOT NULL,
			updated_at INTEGER NOT NULL,
			completed_at INTEGER NOT NULL DEFAULT 0,
			source_candidate_id TEXT NOT NULL DEFAULT '',
			source_session_key TEXT NOT NULL DEFAULT '',
			source_excerpt TEXT NOT NULL DEFAULT ''
		)`,
		`CREATE INDEX IF NOT EXISTS idx_tasks_peer_status_updated ON tasks(peer_id, status, updated_at DESC)`,
		`CREATE TABLE IF NOT EXISTS waiting_on (
			id TEXT PRIMARY KEY,
			peer_id TEXT NOT NULL,
			title TEXT NOT NULL,
			details TEXT NOT NULL DEFAULT '',
			waiting_for TEXT NOT NULL DEFAULT '',
			status TEXT NOT NULL,
			follow_up_at INTEGER NOT NULL DEFAULT 0,
			remind_at INTEGER NOT NULL DEFAULT 0,
			snooze_until INTEGER NOT NULL DEFAULT 0,
			created_at INTEGER NOT NULL,
			updated_at INTEGER NOT NULL,
			resolved_at INTEGER NOT NULL DEFAULT 0,
			source_session_key TEXT NOT NULL DEFAULT '',
			source_excerpt TEXT NOT NULL DEFAULT ''
		)`,
		`CREATE INDEX IF NOT EXISTS idx_waiting_on_peer_status_updated ON waiting_on(peer_id, status, updated_at DESC)`,
	}
	for _, stmt := range stmts {
		if _, err := db.Exec(stmt); err != nil {
			return err
		}
	}
	return nil
}

func normalizeCandidateInput(input CandidateInput) (CandidateInput, error) {
	input.Title = normalizeWhitespace(input.Title)
	input.Details = normalizeWhitespace(input.Details)
	input.Owner = normalizeWhitespace(input.Owner)
	if input.Title == "" {
		return input, fmt.Errorf("title is required")
	}
	if len(input.Title) > 256 {
		return input, fmt.Errorf("title exceeds 256 characters")
	}
	if len(input.Details) > 4096 {
		return input, fmt.Errorf("details exceeds 4096 characters")
	}
	if input.DueAt < 0 {
		return input, fmt.Errorf("due_at must be >= 0")
	}
	return input, nil
}

func normalizeProvenance(provenance CandidateProvenance) CandidateProvenance {
	provenance.CaptureKind = strings.TrimSpace(provenance.CaptureKind)
	if provenance.CaptureKind == "" {
		provenance.CaptureKind = CandidateCaptureManual
	}
	provenance.SourceSessionKey = normalizeWhitespace(provenance.SourceSessionKey)
	provenance.SourceExcerpt = normalizeWhitespace(provenance.SourceExcerpt)
	if len(provenance.SourceExcerpt) > 512 {
		provenance.SourceExcerpt = strings.TrimSpace(provenance.SourceExcerpt[:512])
	}
	return provenance
}

func normalizeCandidateStatus(status CandidateStatus) (CandidateStatus, error) {
	status = CandidateStatus(strings.ToLower(strings.TrimSpace(string(status))))
	switch status {
	case "", CandidateStatusPending:
		return CandidateStatusPending, nil
	case CandidateStatusApproved, CandidateStatusRejected, CandidateStatusAll:
		return status, nil
	default:
		return "", fmt.Errorf("invalid candidate status %q", status)
	}
}

func normalizeTaskStatus(status TaskStatus) (TaskStatus, error) {
	status = TaskStatus(strings.ToLower(strings.TrimSpace(string(status))))
	switch status {
	case "", TaskStatusOpen:
		return TaskStatusOpen, nil
	case TaskStatusSnoozed, TaskStatusCompleted, TaskStatusAll:
		return status, nil
	default:
		return "", fmt.Errorf("invalid task status %q", status)
	}
}

func validateCandidateRecord(candidate Candidate) error {
	_, err := normalizeCandidateInput(CandidateInput{Title: candidate.Title, Details: candidate.Details, Owner: candidate.Owner, DueAt: candidate.DueAt})
	return err
}

func validateTaskRecord(task Task) error {
	if _, err := normalizeCandidateInput(CandidateInput{Title: task.Title, Details: task.Details, Owner: task.Owner, DueAt: task.DueAt}); err != nil {
		return err
	}
	if _, err := normalizeTaskStatus(task.Status); err != nil {
		return err
	}
	if task.SnoozeUntil < 0 {
		return fmt.Errorf("snooze_until must be >= 0")
	}
	if task.CompletedAt < 0 {
		return fmt.Errorf("completed_at must be >= 0")
	}
	return nil
}

func applyCandidatePatch(candidate *Candidate, patch CandidatePatch) {
	if patch.Title != nil {
		candidate.Title = normalizeWhitespace(*patch.Title)
	}
	if patch.Details != nil {
		candidate.Details = normalizeWhitespace(*patch.Details)
	}
	if patch.Owner != nil {
		candidate.Owner = normalizeWhitespace(*patch.Owner)
	}
	if patch.DueAt != nil {
		candidate.DueAt = *patch.DueAt
	}
}

func applyTaskPatch(task *Task, patch TaskPatch) {
	if patch.Title != nil {
		task.Title = normalizeWhitespace(*patch.Title)
	}
	if patch.Details != nil {
		task.Details = normalizeWhitespace(*patch.Details)
	}
	if patch.Owner != nil {
		task.Owner = normalizeWhitespace(*patch.Owner)
	}
	if patch.DueAt != nil {
		task.DueAt = *patch.DueAt
	}
	if patch.SnoozeUntil != nil {
		task.SnoozeUntil = *patch.SnoozeUntil
		if task.SnoozeUntil > 0 && task.Status != TaskStatusCompleted {
			task.Status = TaskStatusSnoozed
		}
	}
	if task.Status == TaskStatusSnoozed && task.SnoozeUntil == 0 {
		task.Status = TaskStatusOpen
	}
}

func loadCandidateTx(ctx context.Context, tx *sql.Tx, peerID, candidateID string) (*Candidate, error) {
	row := tx.QueryRowContext(ctx, `SELECT id, peer_id, title, details, owner, due_at, created_at, updated_at, status, review_reason, reviewed_at, result_task_id, capture_kind, source_session_key, source_excerpt FROM task_candidates WHERE peer_id = ? AND id = ?`, strings.TrimSpace(peerID), strings.TrimSpace(candidateID))
	candidate, err := scanCandidateRow(row)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, fmt.Errorf("candidate %s not found", candidateID)
		}
		return nil, fmt.Errorf("task candidate load: %w", err)
	}
	return candidate, nil
}

func updateCandidateTx(ctx context.Context, tx *sql.Tx, candidate Candidate) error {
	_, err := tx.ExecContext(ctx, `UPDATE task_candidates SET title = ?, details = ?, owner = ?, due_at = ?, updated_at = ?, status = ?, review_reason = ?, reviewed_at = ?, result_task_id = ?, capture_kind = ?, source_session_key = ?, source_excerpt = ? WHERE id = ? AND peer_id = ?`,
		candidate.Title, candidate.Details, candidate.Owner, candidate.DueAt, candidate.UpdatedAt, candidate.Status, candidate.ReviewReason, candidate.ReviewedAt, candidate.ResultTaskID, candidate.CaptureKind, candidate.SourceSessionKey, candidate.SourceExcerpt, candidate.ID, candidate.PeerID)
	if err != nil {
		return fmt.Errorf("task candidate update: %w", err)
	}
	return nil
}

func insertTaskTx(ctx context.Context, tx *sql.Tx, task Task) error {
	if err := validateTaskRecord(task); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO tasks(id, peer_id, title, details, owner, status, due_at, snooze_until, created_at, updated_at, completed_at, source_candidate_id, source_session_key, source_excerpt) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		task.ID, task.PeerID, task.Title, task.Details, task.Owner, task.Status, task.DueAt, task.SnoozeUntil, task.CreatedAt, task.UpdatedAt, task.CompletedAt, task.SourceCandidateID, task.SourceSessionKey, task.SourceExcerpt)
	if err != nil {
		return fmt.Errorf("task insert: %w", err)
	}
	return nil
}

func loadTaskTx(ctx context.Context, tx *sql.Tx, peerID, taskID string) (*Task, error) {
	row := tx.QueryRowContext(ctx, `SELECT id, peer_id, title, details, owner, status, due_at, snooze_until, created_at, updated_at, completed_at, source_candidate_id, source_session_key, source_excerpt FROM tasks WHERE peer_id = ? AND id = ?`, strings.TrimSpace(peerID), strings.TrimSpace(taskID))
	task, err := scanTaskRow(row)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, fmt.Errorf("task %s not found", taskID)
		}
		return nil, fmt.Errorf("task load: %w", err)
	}
	return task, nil
}

func updateTaskTx(ctx context.Context, tx *sql.Tx, task Task) error {
	_, err := tx.ExecContext(ctx, `UPDATE tasks SET title = ?, details = ?, owner = ?, status = ?, due_at = ?, snooze_until = ?, updated_at = ?, completed_at = ?, source_candidate_id = ?, source_session_key = ?, source_excerpt = ? WHERE id = ? AND peer_id = ?`,
		task.Title, task.Details, task.Owner, task.Status, task.DueAt, task.SnoozeUntil, task.UpdatedAt, task.CompletedAt, task.SourceCandidateID, task.SourceSessionKey, task.SourceExcerpt, task.ID, task.PeerID)
	if err != nil {
		return fmt.Errorf("task update: %w", err)
	}
	return nil
}

func (s *Store) existingTaskKeys(ctx context.Context, peerID string) (map[string]struct{}, error) {
	seen := make(map[string]struct{})
	candidates, err := s.ListCandidates(ctx, peerID, 100, CandidateStatusAll)
	if err != nil {
		return nil, err
	}
	for _, candidate := range candidates {
		if key := normalizeTaskKey(candidate.Title, candidate.Details); key != "" {
			seen[key] = struct{}{}
		}
	}
	tasks, err := s.ListTasks(ctx, peerID, 100, TaskStatusAll)
	if err != nil {
		return nil, err
	}
	for _, task := range tasks {
		if key := normalizeTaskKey(task.Title, task.Details); key != "" {
			seen[key] = struct{}{}
		}
	}
	return seen, nil
}

func scanCandidate(rows interface{ Scan(dest ...any) error }) (Candidate, error) {
	var candidate Candidate
	err := rows.Scan(&candidate.ID, &candidate.PeerID, &candidate.Title, &candidate.Details, &candidate.Owner, &candidate.DueAt, &candidate.CreatedAt, &candidate.UpdatedAt, &candidate.Status, &candidate.ReviewReason, &candidate.ReviewedAt, &candidate.ResultTaskID, &candidate.CaptureKind, &candidate.SourceSessionKey, &candidate.SourceExcerpt)
	return candidate, err
}

func scanCandidateRow(row *sql.Row) (*Candidate, error) {
	candidate, err := scanCandidate(row)
	if err != nil {
		return nil, err
	}
	return &candidate, nil
}

func scanTask(rows interface{ Scan(dest ...any) error }) (Task, error) {
	var task Task
	err := rows.Scan(&task.ID, &task.PeerID, &task.Title, &task.Details, &task.Owner, &task.Status, &task.DueAt, &task.SnoozeUntil, &task.CreatedAt, &task.UpdatedAt, &task.CompletedAt, &task.SourceCandidateID, &task.SourceSessionKey, &task.SourceExcerpt)
	return task, err
}

func scanTaskRow(row *sql.Row) (*Task, error) {
	task, err := scanTask(row)
	if err != nil {
		return nil, err
	}
	return &task, nil
}

func normalizeWhitespace(value string) string {
	return strings.Join(strings.Fields(strings.TrimSpace(value)), " ")
}

func normalizeTaskKey(title, details string) string {
	key := strings.ToLower(trimTaskPunctuation(title))
	key = strings.Join(strings.Fields(key), " ")
	return key
}

func trimTaskPunctuation(value string) string {
	return strings.Trim(strings.TrimSpace(value), " \t\n\r.,;:!?")
}

func randomID() string {
	var buf [8]byte
	if _, err := rand.Read(buf[:]); err != nil {
		panic(err)
	}
	return hex.EncodeToString(buf[:])
}
