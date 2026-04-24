package bookmarks

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	"github.com/ffimnsr/koios/internal/redact"
	_ "modernc.org/sqlite"
)

const (
	SourceKindManual       = "manual"
	SourceKindSessionRange = "session_range"
	SourceKindWorkflowRun  = "workflow_run"
)

type Bookmark struct {
	ID               string   `json:"id"`
	PeerID           string   `json:"peer_id"`
	Title            string   `json:"title"`
	Content          string   `json:"content"`
	Labels           []string `json:"labels,omitempty"`
	ReminderAt       int64    `json:"reminder_at,omitempty"`
	CreatedAt        int64    `json:"created_at"`
	UpdatedAt        int64    `json:"updated_at"`
	SourceKind       string   `json:"source_kind,omitempty"`
	SourceSessionKey string   `json:"source_session_key,omitempty"`
	SourceRunID      string   `json:"source_run_id,omitempty"`
	SourceStartIndex int      `json:"source_start_index,omitempty"`
	SourceEndIndex   int      `json:"source_end_index,omitempty"`
	SourceExcerpt    string   `json:"source_excerpt,omitempty"`
}

type Input struct {
	Title            string
	Content          string
	Labels           []string
	ReminderAt       int64
	SourceKind       string
	SourceSessionKey string
	SourceRunID      string
	SourceStartIndex int
	SourceEndIndex   int
	SourceExcerpt    string
}

type Patch struct {
	Title            *string
	Content          *string
	Labels           *[]string
	ReminderAt       *int64
	SourceKind       *string
	SourceSessionKey *string
	SourceRunID      *string
	SourceStartIndex *int
	SourceEndIndex   *int
	SourceExcerpt    *string
}

type Filter struct {
	Limit        int
	Label        string
	UpcomingOnly bool
}

type Store struct {
	db *sql.DB
}

func New(dbPath string) (*Store, error) {
	db, err := sql.Open("sqlite", dbPath+"?_journal_mode=WAL&_foreign_keys=on")
	if err != nil {
		return nil, fmt.Errorf("bookmarks: open db: %w", err)
	}
	db.SetMaxOpenConns(1)
	if err := migrate(db); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("bookmarks: migrate: %w", err)
	}
	return &Store{db: db}, nil
}

func (s *Store) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

func (s *Store) Create(ctx context.Context, peerID string, input Input) (*Bookmark, error) {
	bookmark, err := newBookmark(strings.TrimSpace(peerID), input)
	if err != nil {
		return nil, err
	}
	if _, err := s.db.ExecContext(ctx,
		`INSERT INTO bookmarks(id, peer_id, title, content, labels, reminder_at, created_at, updated_at, source_kind, source_session_key, source_run_id, source_start_index, source_end_index, source_excerpt) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		bookmark.ID, bookmark.PeerID, bookmark.Title, bookmark.Content, strings.Join(bookmark.Labels, ","), bookmark.ReminderAt, bookmark.CreatedAt, bookmark.UpdatedAt, bookmark.SourceKind, bookmark.SourceSessionKey, bookmark.SourceRunID, bookmark.SourceStartIndex, bookmark.SourceEndIndex, bookmark.SourceExcerpt); err != nil {
		return nil, fmt.Errorf("bookmark create: %w", err)
	}
	return bookmark, nil
}

func (s *Store) Get(ctx context.Context, peerID, id string) (*Bookmark, error) {
	bookmark, err := scanBookmark(s.db.QueryRowContext(ctx,
		`SELECT id, peer_id, title, content, COALESCE(labels,''), COALESCE(reminder_at,0), created_at, updated_at, COALESCE(source_kind,''), COALESCE(source_session_key,''), COALESCE(source_run_id,''), COALESCE(source_start_index,0), COALESCE(source_end_index,0), COALESCE(source_excerpt,'') FROM bookmarks WHERE id = ?`,
		strings.TrimSpace(id)))
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("bookmark %s not found", id)
	}
	if err != nil {
		return nil, fmt.Errorf("bookmark get: %w", err)
	}
	if bookmark.PeerID != strings.TrimSpace(peerID) {
		return nil, fmt.Errorf("bookmark %s does not belong to peer", id)
	}
	return &bookmark, nil
}

func (s *Store) List(ctx context.Context, peerID string, filter Filter) ([]Bookmark, error) {
	limit := filter.Limit
	if limit <= 0 {
		limit = 50
	}
	query := `SELECT id, peer_id, title, content, COALESCE(labels,''), COALESCE(reminder_at,0), created_at, updated_at, COALESCE(source_kind,''), COALESCE(source_session_key,''), COALESCE(source_run_id,''), COALESCE(source_start_index,0), COALESCE(source_end_index,0), COALESCE(source_excerpt,'') FROM bookmarks WHERE peer_id = ?`
	args := []any{strings.TrimSpace(peerID)}
	if label := normalizeLabel(filter.Label); label != "" {
		query += ` AND (',' || COALESCE(labels,'') || ',') LIKE ?`
		args = append(args, "%,"+label+",%")
	}
	if filter.UpcomingOnly {
		query += ` AND COALESCE(reminder_at,0) > 0`
	}
	query += ` ORDER BY CASE WHEN COALESCE(reminder_at,0) > 0 THEN 0 ELSE 1 END, CASE WHEN COALESCE(reminder_at,0) > 0 THEN reminder_at ELSE updated_at END DESC, updated_at DESC LIMIT ?`
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("bookmark list: %w", err)
	}
	defer rows.Close()
	return collectBookmarks(rows)
}

func (s *Store) Search(ctx context.Context, peerID, query string, limit int) ([]Bookmark, error) {
	query = strings.ToLower(strings.TrimSpace(query))
	if query == "" {
		return nil, fmt.Errorf("query is required")
	}
	if limit <= 0 {
		limit = 20
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, peer_id, title, content, COALESCE(labels,''), COALESCE(reminder_at,0), created_at, updated_at, COALESCE(source_kind,''), COALESCE(source_session_key,''), COALESCE(source_run_id,''), COALESCE(source_start_index,0), COALESCE(source_end_index,0), COALESCE(source_excerpt,'')
		   FROM bookmarks
		  WHERE peer_id = ?
		    AND (LOWER(title) LIKE ? OR LOWER(content) LIKE ? OR LOWER(COALESCE(labels,'')) LIKE ?)
		  ORDER BY updated_at DESC
		  LIMIT ?`,
		strings.TrimSpace(peerID), "%"+query+"%", "%"+query+"%", "%"+query+"%", limit)
	if err != nil {
		return nil, fmt.Errorf("bookmark search: %w", err)
	}
	defer rows.Close()
	return collectBookmarks(rows)
}

func (s *Store) Update(ctx context.Context, peerID, id string, patch Patch) (*Bookmark, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("bookmark update: begin tx: %w", err)
	}
	defer tx.Rollback()
	bookmark, err := loadBookmarkTx(ctx, tx, peerID, id)
	if err != nil {
		return nil, err
	}
	updated, err := applyPatch(*bookmark, patch)
	if err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE bookmarks SET title = ?, content = ?, labels = ?, reminder_at = ?, updated_at = ?, source_kind = ?, source_session_key = ?, source_run_id = ?, source_start_index = ?, source_end_index = ?, source_excerpt = ? WHERE id = ? AND peer_id = ?`,
		updated.Title, updated.Content, strings.Join(updated.Labels, ","), updated.ReminderAt, updated.UpdatedAt, updated.SourceKind, updated.SourceSessionKey, updated.SourceRunID, updated.SourceStartIndex, updated.SourceEndIndex, updated.SourceExcerpt, updated.ID, updated.PeerID); err != nil {
		return nil, fmt.Errorf("bookmark update: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("bookmark update: commit: %w", err)
	}
	return &updated, nil
}

func (s *Store) Delete(ctx context.Context, peerID, id string) error {
	result, err := s.db.ExecContext(ctx, `DELETE FROM bookmarks WHERE id = ? AND peer_id = ?`, strings.TrimSpace(id), strings.TrimSpace(peerID))
	if err != nil {
		return fmt.Errorf("bookmark delete: %w", err)
	}
	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("bookmark delete rows: %w", err)
	}
	if rowsAffected == 0 {
		return fmt.Errorf("bookmark %s not found", id)
	}
	return nil
}

func migrate(db *sql.DB) error {
	_, err := db.Exec(`CREATE TABLE IF NOT EXISTS bookmarks (
	id TEXT PRIMARY KEY,
	peer_id TEXT NOT NULL,
	title TEXT NOT NULL,
	content TEXT NOT NULL,
	labels TEXT NOT NULL DEFAULT '',
	reminder_at INTEGER NOT NULL DEFAULT 0,
	created_at INTEGER NOT NULL,
	updated_at INTEGER NOT NULL,
	source_kind TEXT NOT NULL DEFAULT 'manual',
	source_session_key TEXT NOT NULL DEFAULT '',
	source_run_id TEXT NOT NULL DEFAULT '',
	source_start_index INTEGER NOT NULL DEFAULT 0,
	source_end_index INTEGER NOT NULL DEFAULT 0,
	source_excerpt TEXT NOT NULL DEFAULT ''
	);
	CREATE INDEX IF NOT EXISTS idx_bookmarks_peer_updated_at ON bookmarks(peer_id, updated_at DESC);
	CREATE INDEX IF NOT EXISTS idx_bookmarks_peer_reminder_at ON bookmarks(peer_id, reminder_at ASC);
	CREATE INDEX IF NOT EXISTS idx_bookmarks_peer_labels ON bookmarks(peer_id, labels);`)
	if err != nil {
		return err
	}
	return nil
}

type rowScanner interface {
	Scan(dest ...any) error
}

func scanBookmark(scanner rowScanner) (Bookmark, error) {
	var bookmark Bookmark
	var labels string
	err := scanner.Scan(
		&bookmark.ID,
		&bookmark.PeerID,
		&bookmark.Title,
		&bookmark.Content,
		&labels,
		&bookmark.ReminderAt,
		&bookmark.CreatedAt,
		&bookmark.UpdatedAt,
		&bookmark.SourceKind,
		&bookmark.SourceSessionKey,
		&bookmark.SourceRunID,
		&bookmark.SourceStartIndex,
		&bookmark.SourceEndIndex,
		&bookmark.SourceExcerpt,
	)
	if err != nil {
		return Bookmark{}, err
	}
	bookmark.Labels = splitLabels(labels)
	return bookmark, nil
}

func collectBookmarks(rows *sql.Rows) ([]Bookmark, error) {
	var out []Bookmark
	for rows.Next() {
		bookmark, err := scanBookmark(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, bookmark)
	}
	return out, rows.Err()
}

func loadBookmarkTx(ctx context.Context, tx *sql.Tx, peerID, id string) (*Bookmark, error) {
	bookmark, err := scanBookmark(tx.QueryRowContext(ctx,
		`SELECT id, peer_id, title, content, COALESCE(labels,''), COALESCE(reminder_at,0), created_at, updated_at, COALESCE(source_kind,''), COALESCE(source_session_key,''), COALESCE(source_run_id,''), COALESCE(source_start_index,0), COALESCE(source_end_index,0), COALESCE(source_excerpt,'') FROM bookmarks WHERE id = ?`,
		strings.TrimSpace(id)))
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("bookmark %s not found", id)
	}
	if err != nil {
		return nil, fmt.Errorf("bookmark get: %w", err)
	}
	if bookmark.PeerID != strings.TrimSpace(peerID) {
		return nil, fmt.Errorf("bookmark %s does not belong to peer", id)
	}
	return &bookmark, nil
}

func newBookmark(peerID string, input Input) (*Bookmark, error) {
	input, err := normalizeInput(input)
	if err != nil {
		return nil, err
	}
	if peerID == "" {
		return nil, fmt.Errorf("peer_id is required")
	}
	now := time.Now().Unix()
	return &Bookmark{
		ID:               randomID(),
		PeerID:           peerID,
		Title:            input.Title,
		Content:          input.Content,
		Labels:           input.Labels,
		ReminderAt:       input.ReminderAt,
		CreatedAt:        now,
		UpdatedAt:        now,
		SourceKind:       input.SourceKind,
		SourceSessionKey: input.SourceSessionKey,
		SourceRunID:      input.SourceRunID,
		SourceStartIndex: input.SourceStartIndex,
		SourceEndIndex:   input.SourceEndIndex,
		SourceExcerpt:    input.SourceExcerpt,
	}, nil
}

func applyPatch(bookmark Bookmark, patch Patch) (Bookmark, error) {
	if patch.Title != nil {
		bookmark.Title = *patch.Title
	}
	if patch.Content != nil {
		bookmark.Content = *patch.Content
	}
	if patch.Labels != nil {
		bookmark.Labels = *patch.Labels
	}
	if patch.ReminderAt != nil {
		bookmark.ReminderAt = *patch.ReminderAt
	}
	if patch.SourceKind != nil {
		bookmark.SourceKind = *patch.SourceKind
	}
	if patch.SourceSessionKey != nil {
		bookmark.SourceSessionKey = *patch.SourceSessionKey
	}
	if patch.SourceRunID != nil {
		bookmark.SourceRunID = *patch.SourceRunID
	}
	if patch.SourceStartIndex != nil {
		bookmark.SourceStartIndex = *patch.SourceStartIndex
	}
	if patch.SourceEndIndex != nil {
		bookmark.SourceEndIndex = *patch.SourceEndIndex
	}
	if patch.SourceExcerpt != nil {
		bookmark.SourceExcerpt = *patch.SourceExcerpt
	}
	input, err := normalizeInput(Input{
		Title:            bookmark.Title,
		Content:          bookmark.Content,
		Labels:           bookmark.Labels,
		ReminderAt:       bookmark.ReminderAt,
		SourceKind:       bookmark.SourceKind,
		SourceSessionKey: bookmark.SourceSessionKey,
		SourceRunID:      bookmark.SourceRunID,
		SourceStartIndex: bookmark.SourceStartIndex,
		SourceEndIndex:   bookmark.SourceEndIndex,
		SourceExcerpt:    bookmark.SourceExcerpt,
	})
	if err != nil {
		return Bookmark{}, err
	}
	bookmark.Title = input.Title
	bookmark.Content = input.Content
	bookmark.Labels = input.Labels
	bookmark.ReminderAt = input.ReminderAt
	bookmark.SourceKind = input.SourceKind
	bookmark.SourceSessionKey = input.SourceSessionKey
	bookmark.SourceRunID = input.SourceRunID
	bookmark.SourceStartIndex = input.SourceStartIndex
	bookmark.SourceEndIndex = input.SourceEndIndex
	bookmark.SourceExcerpt = input.SourceExcerpt
	bookmark.UpdatedAt = time.Now().Unix()
	return bookmark, nil
}

func normalizeInput(input Input) (Input, error) {
	input.Title = strings.TrimSpace(redact.String(input.Title))
	input.Content = strings.TrimSpace(redact.String(input.Content))
	input.SourceKind = normalizeSourceKind(input.SourceKind)
	input.SourceSessionKey = strings.TrimSpace(input.SourceSessionKey)
	input.SourceRunID = strings.TrimSpace(input.SourceRunID)
	input.SourceExcerpt = truncate(strings.TrimSpace(redact.String(input.SourceExcerpt)), 512)
	input.Labels = normalizeLabels(input.Labels)
	if input.Title == "" {
		return Input{}, fmt.Errorf("title is required")
	}
	if input.Content == "" {
		return Input{}, fmt.Errorf("content is required")
	}
	if len(input.Title) > 256 {
		return Input{}, fmt.Errorf("title exceeds 256 characters")
	}
	if len(input.Content) > 64*1024 {
		return Input{}, fmt.Errorf("content exceeds 65536 characters")
	}
	if input.ReminderAt < 0 {
		return Input{}, fmt.Errorf("reminder_at must be >= 0")
	}
	if input.SourceStartIndex < 0 || input.SourceEndIndex < 0 {
		return Input{}, fmt.Errorf("source indexes must be >= 0")
	}
	if input.SourceEndIndex > 0 && input.SourceStartIndex == 0 {
		input.SourceStartIndex = input.SourceEndIndex
	}
	if input.SourceStartIndex > 0 && input.SourceEndIndex == 0 {
		input.SourceEndIndex = input.SourceStartIndex
	}
	if input.SourceStartIndex > 0 && input.SourceEndIndex > 0 && input.SourceStartIndex > input.SourceEndIndex {
		return Input{}, fmt.Errorf("source_start_index must be <= source_end_index")
	}
	if input.SourceExcerpt == "" {
		input.SourceExcerpt = truncate(input.Content, 512)
	}
	return input, nil
}

func normalizeSourceKind(kind string) string {
	kind = strings.ToLower(strings.TrimSpace(kind))
	if kind == "" {
		return SourceKindManual
	}
	switch kind {
	case SourceKindManual, SourceKindSessionRange, SourceKindWorkflowRun:
		return kind
	default:
		return kind
	}
}

func normalizeLabels(labels []string) []string {
	seen := make(map[string]struct{}, len(labels))
	out := make([]string, 0, len(labels))
	for _, label := range labels {
		label = normalizeLabel(label)
		if label == "" {
			continue
		}
		if _, ok := seen[label]; ok {
			continue
		}
		seen[label] = struct{}{}
		out = append(out, label)
	}
	return out
}

func normalizeLabel(label string) string {
	label = strings.ToLower(strings.TrimSpace(redact.String(label)))
	label = strings.ReplaceAll(label, ",", "-")
	label = strings.Join(strings.Fields(label), "-")
	if len(label) > 64 {
		label = label[:64]
	}
	return label
}

func splitLabels(labels string) []string {
	if strings.TrimSpace(labels) == "" {
		return nil
	}
	return normalizeLabels(strings.Split(labels, ","))
}

func truncate(value string, limit int) string {
	if limit <= 0 || len(value) <= limit {
		return value
	}
	return strings.TrimSpace(value[:limit])
}

func randomID() string {
	buf := make([]byte, 8)
	if _, err := rand.Read(buf); err != nil {
		return fmt.Sprintf("bm-%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(buf)
}
