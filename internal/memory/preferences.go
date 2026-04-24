package memory

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/ffimnsr/koios/internal/redact"
)

const preferenceOrderClause = `
CASE COALESCE(scope,'global')
	WHEN 'global' THEN 0
	WHEN 'workspace' THEN 1
	WHEN 'profile' THEN 2
	WHEN 'project' THEN 3
	WHEN 'topic' THEN 4
	ELSE 5
END,
CASE COALESCE(kind,'preference')
	WHEN 'decision' THEN 0
	WHEN 'preference' THEN 1
	ELSE 2
END,
confidence DESC,
last_confirmed_at DESC,
updated_at DESC,
name ASC`

func (s *Store) CreatePreference(ctx context.Context, peerID string, kind PreferenceKind, name string, value string, category string, scope PreferenceScope, scopeRef string, confidence float64, lastConfirmedAt int64, sourceSessionKey string, sourceExcerpt string) (*PreferenceRecord, error) {
	record, err := newPreferenceRecord(peerID, kind, name, value, category, scope, scopeRef, confidence, lastConfirmedAt, sourceSessionKey, sourceExcerpt)
	if err != nil {
		return nil, err
	}
	if _, err := s.db.ExecContext(ctx,
		`INSERT INTO memory_preferences(id, peer_id, kind, name, value, category, scope, scope_ref, confidence, last_confirmed_at, created_at, updated_at, source_session_key, source_excerpt) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		record.ID, record.PeerID, record.Kind, record.Name, record.Value, record.Category, record.Scope, record.ScopeRef, record.Confidence, record.LastConfirmedAt, record.CreatedAt, record.UpdatedAt, record.SourceSessionKey, record.SourceExcerpt); err != nil {
		return nil, fmt.Errorf("memory preference create: %w", err)
	}
	return record, nil
}

func (s *Store) GetPreference(ctx context.Context, peerID, preferenceID string) (*PreferenceRecord, error) {
	record, err := scanPreference(s.db.QueryRowContext(ctx,
		`SELECT id, peer_id, COALESCE(kind,'preference'), name, value, COALESCE(category,''), COALESCE(scope,'global'), COALESCE(scope_ref,''), COALESCE(confidence,1.0), COALESCE(last_confirmed_at,0), created_at, updated_at, COALESCE(source_session_key,''), COALESCE(source_excerpt,'') FROM memory_preferences WHERE id = ?`,
		preferenceID))
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("preference %s not found", preferenceID)
	}
	if err != nil {
		return nil, fmt.Errorf("memory preference get: %w", err)
	}
	if record.PeerID != peerID {
		return nil, fmt.Errorf("preference %s does not belong to peer", preferenceID)
	}
	return &record, nil
}

func (s *Store) ListPreferences(ctx context.Context, peerID string, filter PreferenceFilter) ([]PreferenceRecord, error) {
	limit := filter.Limit
	if limit <= 0 {
		limit = 50
	}
	kind, err := normalizePreferenceKind(filter.Kind, true)
	if err != nil {
		return nil, err
	}
	scope, err := normalizePreferenceScope(filter.Scope, true)
	if err != nil {
		return nil, err
	}
	baseQuery := `SELECT id, peer_id, COALESCE(kind,'preference'), name, value, COALESCE(category,''), COALESCE(scope,'global'), COALESCE(scope_ref,''), COALESCE(confidence,1.0), COALESCE(last_confirmed_at,0), created_at, updated_at, COALESCE(source_session_key,''), COALESCE(source_excerpt,'') FROM memory_preferences WHERE peer_id = ?`
	args := []any{peerID}
	if kind != "" {
		baseQuery += ` AND COALESCE(kind,'preference') = ?`
		args = append(args, kind)
	}
	if scope != "" {
		baseQuery += ` AND COALESCE(scope,'global') = ?`
		args = append(args, scope)
	}
	baseQuery += ` ORDER BY ` + preferenceOrderClause + ` LIMIT ?`
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx, baseQuery, args...)
	if err != nil {
		return nil, fmt.Errorf("memory preference list: %w", err)
	}
	defer rows.Close()
	var records []PreferenceRecord
	for rows.Next() {
		record, err := scanPreference(rows)
		if err != nil {
			continue
		}
		records = append(records, record)
	}
	return records, rows.Err()
}

func (s *Store) PreferencesForInjection(ctx context.Context, peerID string, limit int) ([]PreferenceRecord, error) {
	if limit <= 0 {
		limit = 8
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, peer_id, COALESCE(kind,'preference'), name, value, COALESCE(category,''), COALESCE(scope,'global'), COALESCE(scope_ref,''), COALESCE(confidence,1.0), COALESCE(last_confirmed_at,0), created_at, updated_at, COALESCE(source_session_key,''), COALESCE(source_excerpt,'')
		   FROM memory_preferences
		  WHERE peer_id = ? AND COALESCE(confidence,1.0) >= 0.5
		  ORDER BY `+preferenceOrderClause+`
		  LIMIT ?`,
		peerID, limit)
	if err != nil {
		return nil, fmt.Errorf("memory preference injection list: %w", err)
	}
	defer rows.Close()
	var records []PreferenceRecord
	for rows.Next() {
		record, err := scanPreference(rows)
		if err != nil {
			continue
		}
		records = append(records, record)
	}
	return records, rows.Err()
}

func (s *Store) UpdatePreference(ctx context.Context, peerID, preferenceID string, patch PreferencePatch) (*PreferenceRecord, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("memory preference update: begin tx: %w", err)
	}
	defer tx.Rollback()
	record, err := loadPreferenceTx(ctx, tx, peerID, preferenceID)
	if err != nil {
		return nil, err
	}
	updated, err := applyPreferencePatch(*record, patch)
	if err != nil {
		return nil, err
	}
	if err := updatePreferenceTx(ctx, tx, updated); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("memory preference update: commit: %w", err)
	}
	return &updated, nil
}

func (s *Store) ConfirmPreference(ctx context.Context, peerID, preferenceID string, lastConfirmedAt int64, confidence *float64) (*PreferenceRecord, error) {
	patch := PreferencePatch{LastConfirmedAt: &lastConfirmedAt}
	if confidence != nil {
		patch.Confidence = confidence
	}
	return s.UpdatePreference(ctx, peerID, preferenceID, patch)
}

func (s *Store) DeletePreference(ctx context.Context, peerID, preferenceID string) error {
	result, err := s.db.ExecContext(ctx, `DELETE FROM memory_preferences WHERE id = ? AND peer_id = ?`, preferenceID, peerID)
	if err != nil {
		return fmt.Errorf("memory preference delete: %w", err)
	}
	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("memory preference delete rows: %w", err)
	}
	if rowsAffected == 0 {
		return fmt.Errorf("preference %s not found", preferenceID)
	}
	return nil
}

func newPreferenceRecord(peerID string, kind PreferenceKind, name string, value string, category string, scope PreferenceScope, scopeRef string, confidence float64, lastConfirmedAt int64, sourceSessionKey string, sourceExcerpt string) (*PreferenceRecord, error) {
	normalizedKind, normalizedName, normalizedValue, normalizedCategory, normalizedScope, normalizedScopeRef, normalizedConfidence, normalizedLastConfirmedAt, normalizedSourceSessionKey, normalizedSourceExcerpt, err := normalizePreferenceFields(kind, name, value, category, scope, scopeRef, confidence, lastConfirmedAt, sourceSessionKey, sourceExcerpt, true)
	if err != nil {
		return nil, err
	}
	now := time.Now().Unix()
	if normalizedLastConfirmedAt == 0 {
		normalizedLastConfirmedAt = now
	}
	return &PreferenceRecord{
		ID:               newID(),
		PeerID:           peerID,
		Kind:             normalizedKind,
		Name:             normalizedName,
		Value:            normalizedValue,
		Category:         normalizedCategory,
		Scope:            normalizedScope,
		ScopeRef:         normalizedScopeRef,
		Confidence:       normalizedConfidence,
		LastConfirmedAt:  normalizedLastConfirmedAt,
		CreatedAt:        now,
		UpdatedAt:        now,
		SourceSessionKey: normalizedSourceSessionKey,
		SourceExcerpt:    normalizedSourceExcerpt,
	}, nil
}

func normalizePreferenceFields(kind PreferenceKind, name string, value string, category string, scope PreferenceScope, scopeRef string, confidence float64, lastConfirmedAt int64, sourceSessionKey string, sourceExcerpt string, defaultConfidence bool) (PreferenceKind, string, string, string, PreferenceScope, string, float64, int64, string, string, error) {
	normalizedKind, err := normalizePreferenceKind(kind, false)
	if err != nil {
		return "", "", "", "", "", "", 0, 0, "", "", err
	}
	normalizedScope, err := normalizePreferenceScope(scope, false)
	if err != nil {
		return "", "", "", "", "", "", 0, 0, "", "", err
	}
	name = strings.TrimSpace(redact.String(name))
	if name == "" {
		return "", "", "", "", "", "", 0, 0, "", "", fmt.Errorf("name is required")
	}
	value = strings.TrimSpace(redact.String(value))
	if value == "" {
		return "", "", "", "", "", "", 0, 0, "", "", fmt.Errorf("value is required")
	}
	category = strings.TrimSpace(redact.String(category))
	scopeRef = strings.TrimSpace(redact.String(scopeRef))
	defaultValue := 1.0
	if !defaultConfidence {
		defaultValue = confidence
	}
	normalizedConfidence, err := normalizePreferenceConfidence(confidence, defaultValue)
	if err != nil {
		return "", "", "", "", "", "", 0, 0, "", "", err
	}
	if lastConfirmedAt < 0 {
		return "", "", "", "", "", "", 0, 0, "", "", fmt.Errorf("last_confirmed_at must be >= 0")
	}
	sourceSessionKey = strings.TrimSpace(sourceSessionKey)
	sourceExcerpt = truncateExcerpt(strings.TrimSpace(redact.String(sourceExcerpt)), 160)
	return normalizedKind, name, value, category, normalizedScope, scopeRef, normalizedConfidence, lastConfirmedAt, sourceSessionKey, sourceExcerpt, nil
}

func loadPreferenceTx(ctx context.Context, tx *sql.Tx, peerID, preferenceID string) (*PreferenceRecord, error) {
	record, err := scanPreference(tx.QueryRowContext(ctx,
		`SELECT id, peer_id, COALESCE(kind,'preference'), name, value, COALESCE(category,''), COALESCE(scope,'global'), COALESCE(scope_ref,''), COALESCE(confidence,1.0), COALESCE(last_confirmed_at,0), created_at, updated_at, COALESCE(source_session_key,''), COALESCE(source_excerpt,'') FROM memory_preferences WHERE id = ?`,
		preferenceID))
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("preference %s not found", preferenceID)
	}
	if err != nil {
		return nil, fmt.Errorf("memory preference get: %w", err)
	}
	if record.PeerID != peerID {
		return nil, fmt.Errorf("preference %s does not belong to peer", preferenceID)
	}
	return &record, nil
}

func applyPreferencePatch(record PreferenceRecord, patch PreferencePatch) (PreferenceRecord, error) {
	kind := record.Kind
	if patch.Kind != nil {
		kind = *patch.Kind
	}
	name := record.Name
	if patch.Name != nil {
		name = *patch.Name
	}
	value := record.Value
	if patch.Value != nil {
		value = *patch.Value
	}
	category := record.Category
	if patch.Category != nil {
		category = *patch.Category
	}
	scope := record.Scope
	if patch.Scope != nil {
		scope = *patch.Scope
	}
	scopeRef := record.ScopeRef
	if patch.ScopeRef != nil {
		scopeRef = *patch.ScopeRef
	}
	confidence := record.Confidence
	if patch.Confidence != nil {
		confidence = *patch.Confidence
	}
	lastConfirmedAt := record.LastConfirmedAt
	if patch.LastConfirmedAt != nil {
		lastConfirmedAt = *patch.LastConfirmedAt
	}
	sourceSessionKey := record.SourceSessionKey
	if patch.SourceSessionKey != nil {
		sourceSessionKey = *patch.SourceSessionKey
	}
	sourceExcerpt := record.SourceExcerpt
	if patch.SourceExcerpt != nil {
		sourceExcerpt = *patch.SourceExcerpt
	}
	normalizedKind, normalizedName, normalizedValue, normalizedCategory, normalizedScope, normalizedScopeRef, normalizedConfidence, normalizedLastConfirmedAt, normalizedSourceSessionKey, normalizedSourceExcerpt, err := normalizePreferenceFields(kind, name, value, category, scope, scopeRef, confidence, lastConfirmedAt, sourceSessionKey, sourceExcerpt, false)
	if err != nil {
		return PreferenceRecord{}, err
	}
	record.Kind = normalizedKind
	record.Name = normalizedName
	record.Value = normalizedValue
	record.Category = normalizedCategory
	record.Scope = normalizedScope
	record.ScopeRef = normalizedScopeRef
	record.Confidence = normalizedConfidence
	record.LastConfirmedAt = normalizedLastConfirmedAt
	record.SourceSessionKey = normalizedSourceSessionKey
	record.SourceExcerpt = normalizedSourceExcerpt
	record.UpdatedAt = time.Now().Unix()
	return record, nil
}

func updatePreferenceTx(ctx context.Context, tx *sql.Tx, record PreferenceRecord) error {
	_, err := tx.ExecContext(ctx,
		`UPDATE memory_preferences SET kind = ?, name = ?, value = ?, category = ?, scope = ?, scope_ref = ?, confidence = ?, last_confirmed_at = ?, updated_at = ?, source_session_key = ?, source_excerpt = ? WHERE id = ? AND peer_id = ?`,
		record.Kind, record.Name, record.Value, record.Category, record.Scope, record.ScopeRef, record.Confidence, record.LastConfirmedAt, record.UpdatedAt, record.SourceSessionKey, record.SourceExcerpt, record.ID, record.PeerID)
	if err != nil {
		return fmt.Errorf("memory preference update: %w", err)
	}
	return nil
}
