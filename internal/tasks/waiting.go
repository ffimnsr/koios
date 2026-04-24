package tasks

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"
)

type WaitingStatus string

const (
	WaitingStatusOpen     WaitingStatus = "open"
	WaitingStatusSnoozed  WaitingStatus = "snoozed"
	WaitingStatusResolved WaitingStatus = "resolved"
	WaitingStatusAll      WaitingStatus = "all"
)

type WaitingOn struct {
	ID               string        `json:"id"`
	PeerID           string        `json:"peer_id"`
	Title            string        `json:"title"`
	Details          string        `json:"details,omitempty"`
	WaitingFor       string        `json:"waiting_for"`
	Status           WaitingStatus `json:"status"`
	FollowUpAt       int64         `json:"follow_up_at,omitempty"`
	RemindAt         int64         `json:"remind_at,omitempty"`
	SnoozeUntil      int64         `json:"snooze_until,omitempty"`
	CreatedAt        int64         `json:"created_at"`
	UpdatedAt        int64         `json:"updated_at"`
	ResolvedAt       int64         `json:"resolved_at,omitempty"`
	SourceSessionKey string        `json:"source_session_key,omitempty"`
	SourceExcerpt    string        `json:"source_excerpt,omitempty"`
}

type WaitingOnInput struct {
	Title            string
	Details          string
	WaitingFor       string
	FollowUpAt       int64
	RemindAt         int64
	SourceSessionKey string
	SourceExcerpt    string
}

type WaitingOnPatch struct {
	Title            *string
	Details          *string
	WaitingFor       *string
	FollowUpAt       *int64
	RemindAt         *int64
	SnoozeUntil      *int64
	SourceSessionKey *string
	SourceExcerpt    *string
}

func (s *Store) CreateWaitingOn(ctx context.Context, peerID string, input WaitingOnInput) (*WaitingOn, error) {
	input, err := normalizeWaitingOnInput(input)
	if err != nil {
		return nil, err
	}
	peerID = strings.TrimSpace(peerID)
	if peerID == "" {
		return nil, fmt.Errorf("peer_id is required")
	}
	now := time.Now().Unix()
	item := &WaitingOn{
		ID:               randomID(),
		PeerID:           peerID,
		Title:            input.Title,
		Details:          input.Details,
		WaitingFor:       input.WaitingFor,
		Status:           WaitingStatusOpen,
		FollowUpAt:       input.FollowUpAt,
		RemindAt:         input.RemindAt,
		CreatedAt:        now,
		UpdatedAt:        now,
		SourceSessionKey: input.SourceSessionKey,
		SourceExcerpt:    input.SourceExcerpt,
	}
	if _, err := s.db.ExecContext(ctx, `INSERT INTO waiting_on(id, peer_id, title, details, waiting_for, status, follow_up_at, remind_at, snooze_until, created_at, updated_at, resolved_at, source_session_key, source_excerpt) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		item.ID, item.PeerID, item.Title, item.Details, item.WaitingFor, item.Status, item.FollowUpAt, item.RemindAt, item.SnoozeUntil, item.CreatedAt, item.UpdatedAt, item.ResolvedAt, item.SourceSessionKey, item.SourceExcerpt); err != nil {
		return nil, fmt.Errorf("waiting create: %w", err)
	}
	return item, nil
}

func (s *Store) ListWaitingOns(ctx context.Context, peerID string, limit int, status WaitingStatus) ([]WaitingOn, error) {
	status, err := normalizeWaitingStatus(status)
	if err != nil {
		return nil, err
	}
	if limit <= 0 {
		limit = 50
	}
	peerID = strings.TrimSpace(peerID)
	var rows *sql.Rows
	if status == WaitingStatusAll {
		rows, err = s.db.QueryContext(ctx, `SELECT id, peer_id, title, details, waiting_for, status, follow_up_at, remind_at, snooze_until, created_at, updated_at, resolved_at, source_session_key, source_excerpt FROM waiting_on WHERE peer_id = ? ORDER BY updated_at DESC LIMIT ?`, peerID, limit)
	} else {
		rows, err = s.db.QueryContext(ctx, `SELECT id, peer_id, title, details, waiting_for, status, follow_up_at, remind_at, snooze_until, created_at, updated_at, resolved_at, source_session_key, source_excerpt FROM waiting_on WHERE peer_id = ? AND status = ? ORDER BY updated_at DESC LIMIT ?`, peerID, status, limit)
	}
	if err != nil {
		return nil, fmt.Errorf("waiting list: %w", err)
	}
	defer rows.Close()
	var out []WaitingOn
	for rows.Next() {
		item, err := scanWaitingOn(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func (s *Store) GetWaitingOn(ctx context.Context, peerID, waitingID string) (*WaitingOn, error) {
	row := s.db.QueryRowContext(ctx, `SELECT id, peer_id, title, details, waiting_for, status, follow_up_at, remind_at, snooze_until, created_at, updated_at, resolved_at, source_session_key, source_excerpt FROM waiting_on WHERE peer_id = ? AND id = ?`, strings.TrimSpace(peerID), strings.TrimSpace(waitingID))
	item, err := scanWaitingOnRow(row)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, fmt.Errorf("waiting-on %s not found", waitingID)
		}
		return nil, fmt.Errorf("waiting get: %w", err)
	}
	return item, nil
}

func (s *Store) UpdateWaitingOn(ctx context.Context, peerID, waitingID string, patch WaitingOnPatch) (*WaitingOn, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("waiting update: begin tx: %w", err)
	}
	defer tx.Rollback()
	item, err := loadWaitingOnTx(ctx, tx, peerID, waitingID)
	if err != nil {
		return nil, err
	}
	applyWaitingOnPatch(item, patch)
	if err := validateWaitingOnRecord(*item); err != nil {
		return nil, err
	}
	item.UpdatedAt = time.Now().Unix()
	if item.Status == WaitingStatusResolved {
		item.ResolvedAt = item.UpdatedAt
	}
	if err := updateWaitingOnTx(ctx, tx, *item); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("waiting update: commit: %w", err)
	}
	return item, nil
}

func (s *Store) SnoozeWaitingOn(ctx context.Context, peerID, waitingID string, until int64) (*WaitingOn, error) {
	if until <= 0 {
		return nil, fmt.Errorf("until is required")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("waiting snooze: begin tx: %w", err)
	}
	defer tx.Rollback()
	item, err := loadWaitingOnTx(ctx, tx, peerID, waitingID)
	if err != nil {
		return nil, err
	}
	item.Status = WaitingStatusSnoozed
	item.SnoozeUntil = until
	item.UpdatedAt = time.Now().Unix()
	item.ResolvedAt = 0
	if err := validateWaitingOnRecord(*item); err != nil {
		return nil, err
	}
	if err := updateWaitingOnTx(ctx, tx, *item); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("waiting snooze: commit: %w", err)
	}
	return item, nil
}

func (s *Store) ResolveWaitingOn(ctx context.Context, peerID, waitingID string) (*WaitingOn, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("waiting resolve: begin tx: %w", err)
	}
	defer tx.Rollback()
	item, err := loadWaitingOnTx(ctx, tx, peerID, waitingID)
	if err != nil {
		return nil, err
	}
	now := time.Now().Unix()
	item.Status = WaitingStatusResolved
	item.SnoozeUntil = 0
	item.ResolvedAt = now
	item.UpdatedAt = now
	if err := updateWaitingOnTx(ctx, tx, *item); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("waiting resolve: commit: %w", err)
	}
	return item, nil
}

func (s *Store) ReopenWaitingOn(ctx context.Context, peerID, waitingID string) (*WaitingOn, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("waiting reopen: begin tx: %w", err)
	}
	defer tx.Rollback()
	item, err := loadWaitingOnTx(ctx, tx, peerID, waitingID)
	if err != nil {
		return nil, err
	}
	item.Status = WaitingStatusOpen
	item.SnoozeUntil = 0
	item.ResolvedAt = 0
	item.UpdatedAt = time.Now().Unix()
	if err := validateWaitingOnRecord(*item); err != nil {
		return nil, err
	}
	if err := updateWaitingOnTx(ctx, tx, *item); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("waiting reopen: commit: %w", err)
	}
	return item, nil
}

func normalizeWaitingOnInput(input WaitingOnInput) (WaitingOnInput, error) {
	input.Title = normalizeWhitespace(input.Title)
	input.Details = normalizeWhitespace(input.Details)
	input.WaitingFor = normalizeWhitespace(input.WaitingFor)
	input.SourceSessionKey = normalizeWhitespace(input.SourceSessionKey)
	input.SourceExcerpt = normalizeWhitespace(input.SourceExcerpt)
	if input.Title == "" {
		return input, fmt.Errorf("title is required")
	}
	if input.WaitingFor == "" {
		return input, fmt.Errorf("waiting_for is required")
	}
	if len(input.Title) > 256 {
		return input, fmt.Errorf("title exceeds 256 characters")
	}
	if len(input.Details) > 4096 {
		return input, fmt.Errorf("details exceeds 4096 characters")
	}
	if len(input.SourceExcerpt) > 512 {
		input.SourceExcerpt = strings.TrimSpace(input.SourceExcerpt[:512])
	}
	if input.FollowUpAt < 0 {
		return input, fmt.Errorf("follow_up_at must be >= 0")
	}
	if input.RemindAt < 0 {
		return input, fmt.Errorf("remind_at must be >= 0")
	}
	return input, nil
}

func normalizeWaitingStatus(status WaitingStatus) (WaitingStatus, error) {
	status = WaitingStatus(strings.ToLower(strings.TrimSpace(string(status))))
	switch status {
	case "", WaitingStatusOpen:
		return WaitingStatusOpen, nil
	case WaitingStatusSnoozed, WaitingStatusResolved, WaitingStatusAll:
		return status, nil
	default:
		return "", fmt.Errorf("invalid waiting status %q", status)
	}
}

func validateWaitingOnRecord(item WaitingOn) error {
	_, err := normalizeWaitingOnInput(WaitingOnInput{
		Title:            item.Title,
		Details:          item.Details,
		WaitingFor:       item.WaitingFor,
		FollowUpAt:       item.FollowUpAt,
		RemindAt:         item.RemindAt,
		SourceSessionKey: item.SourceSessionKey,
		SourceExcerpt:    item.SourceExcerpt,
	})
	if err != nil {
		return err
	}
	if item.SnoozeUntil < 0 {
		return fmt.Errorf("snooze_until must be >= 0")
	}
	if item.ResolvedAt < 0 {
		return fmt.Errorf("resolved_at must be >= 0")
	}
	if _, err := normalizeWaitingStatus(item.Status); err != nil {
		return err
	}
	return nil
}

func applyWaitingOnPatch(item *WaitingOn, patch WaitingOnPatch) {
	if patch.Title != nil {
		item.Title = normalizeWhitespace(*patch.Title)
	}
	if patch.Details != nil {
		item.Details = normalizeWhitespace(*patch.Details)
	}
	if patch.WaitingFor != nil {
		item.WaitingFor = normalizeWhitespace(*patch.WaitingFor)
	}
	if patch.FollowUpAt != nil {
		item.FollowUpAt = *patch.FollowUpAt
	}
	if patch.RemindAt != nil {
		item.RemindAt = *patch.RemindAt
	}
	if patch.SnoozeUntil != nil {
		item.SnoozeUntil = *patch.SnoozeUntil
		if item.SnoozeUntil > 0 && item.Status != WaitingStatusResolved {
			item.Status = WaitingStatusSnoozed
		}
		if item.SnoozeUntil == 0 && item.Status == WaitingStatusSnoozed {
			item.Status = WaitingStatusOpen
		}
	}
	if patch.SourceSessionKey != nil {
		item.SourceSessionKey = normalizeWhitespace(*patch.SourceSessionKey)
	}
	if patch.SourceExcerpt != nil {
		item.SourceExcerpt = normalizeWhitespace(*patch.SourceExcerpt)
		if len(item.SourceExcerpt) > 512 {
			item.SourceExcerpt = strings.TrimSpace(item.SourceExcerpt[:512])
		}
	}
}

func loadWaitingOnTx(ctx context.Context, tx *sql.Tx, peerID, waitingID string) (*WaitingOn, error) {
	row := tx.QueryRowContext(ctx, `SELECT id, peer_id, title, details, waiting_for, status, follow_up_at, remind_at, snooze_until, created_at, updated_at, resolved_at, source_session_key, source_excerpt FROM waiting_on WHERE peer_id = ? AND id = ?`, strings.TrimSpace(peerID), strings.TrimSpace(waitingID))
	item, err := scanWaitingOnRow(row)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, fmt.Errorf("waiting-on %s not found", waitingID)
		}
		return nil, fmt.Errorf("waiting load: %w", err)
	}
	return item, nil
}

func updateWaitingOnTx(ctx context.Context, tx *sql.Tx, item WaitingOn) error {
	_, err := tx.ExecContext(ctx, `UPDATE waiting_on SET title = ?, details = ?, waiting_for = ?, status = ?, follow_up_at = ?, remind_at = ?, snooze_until = ?, updated_at = ?, resolved_at = ?, source_session_key = ?, source_excerpt = ? WHERE id = ? AND peer_id = ?`,
		item.Title, item.Details, item.WaitingFor, item.Status, item.FollowUpAt, item.RemindAt, item.SnoozeUntil, item.UpdatedAt, item.ResolvedAt, item.SourceSessionKey, item.SourceExcerpt, item.ID, item.PeerID)
	if err != nil {
		return fmt.Errorf("waiting update: %w", err)
	}
	return nil
}

func scanWaitingOn(rows interface{ Scan(dest ...any) error }) (WaitingOn, error) {
	var item WaitingOn
	err := rows.Scan(&item.ID, &item.PeerID, &item.Title, &item.Details, &item.WaitingFor, &item.Status, &item.FollowUpAt, &item.RemindAt, &item.SnoozeUntil, &item.CreatedAt, &item.UpdatedAt, &item.ResolvedAt, &item.SourceSessionKey, &item.SourceExcerpt)
	return item, err
}

func scanWaitingOnRow(row *sql.Row) (*WaitingOn, error) {
	item, err := scanWaitingOn(row)
	if err != nil {
		return nil, err
	}
	return &item, nil
}
