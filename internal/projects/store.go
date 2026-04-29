// Package projects provides durable project tracking, separate from tasks.
// Projects group related tasks, decisions, artifacts, and sessions under a
// higher-level context with an explicit lifecycle.
package projects

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

// ProjectStatus represents the lifecycle of a project.
type ProjectStatus string

const (
	ProjectActive   ProjectStatus = "active"
	ProjectArchived ProjectStatus = "archived"
)

// Project tracks a higher-level context for related tasks, decisions,
// artifacts, and sessions.
type Project struct {
	ID          string        `json:"id"`
	PeerID      string        `json:"peer_id"`
	Title       string        `json:"title"`
	Description string        `json:"description,omitempty"`
	Status      ProjectStatus `json:"status"`
	Labels      []string      `json:"labels,omitempty"`
	LinkedTasks []string      `json:"linked_tasks,omitempty"`
	CreatedAt   int64         `json:"created_at"`
	UpdatedAt   int64         `json:"updated_at"`
}

// Input holds the mutable fields used when creating a project.
type Input struct {
	Title       string
	Description string
	Labels      []string
}

// Patch carries optional fields for a partial update.
type Patch struct {
	Title       *string
	Description *string
	Labels      *[]string
}

// Store persists projects in a local SQLite database.
type Store struct {
	db *sql.DB
}

// New opens (or creates) the projects SQLite database at dbPath.
func New(dbPath string) (*Store, error) {
	db, err := sql.Open("sqlite", dbPath+"?_journal_mode=WAL&_foreign_keys=on")
	if err != nil {
		return nil, fmt.Errorf("projects: open db: %w", err)
	}
	db.SetMaxOpenConns(1)
	if err := migrate(db); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("projects: migrate: %w", err)
	}
	return &Store{db: db}, nil
}

// Close releases the database connection.
func (s *Store) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

// Create inserts a new project and returns the saved record.
func (s *Store) Create(ctx context.Context, peerID string, input Input) (*Project, error) {
	p, err := newProject(strings.TrimSpace(peerID), input)
	if err != nil {
		return nil, err
	}
	if _, err := s.db.ExecContext(ctx,
		`INSERT INTO projects(id, peer_id, title, description, status, labels, created_at, updated_at)
		 VALUES (?,?,?,?,?,?,?,?)`,
		p.ID, p.PeerID, p.Title, p.Description, string(p.Status),
		strings.Join(p.Labels, ","), p.CreatedAt, p.UpdatedAt); err != nil {
		return nil, fmt.Errorf("project create: %w", err)
	}
	return p, nil
}

// Get returns a single project by ID, verifying peer ownership, with linked tasks.
func (s *Store) Get(ctx context.Context, peerID, id string) (*Project, error) {
	p, err := scanProject(s.db.QueryRowContext(ctx,
		`SELECT id, peer_id, title, COALESCE(description,''), status, COALESCE(labels,''), created_at, updated_at
		   FROM projects WHERE id = ?`,
		strings.TrimSpace(id)))
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("project %s not found", id)
	}
	if err != nil {
		return nil, fmt.Errorf("project get: %w", err)
	}
	if p.PeerID != strings.TrimSpace(peerID) {
		return nil, fmt.Errorf("project %s does not belong to peer", id)
	}
	tasks, err := s.linkedTasks(ctx, p.ID)
	if err != nil {
		return nil, err
	}
	p.LinkedTasks = tasks
	return &p, nil
}

// List returns recent projects for peerID, optionally filtered by status.
func (s *Store) List(ctx context.Context, peerID string, limit int, status ProjectStatus) ([]Project, error) {
	if limit <= 0 {
		limit = 50
	}
	var rows *sql.Rows
	var err error
	if status != "" {
		rows, err = s.db.QueryContext(ctx,
			`SELECT id, peer_id, title, COALESCE(description,''), status, COALESCE(labels,''), created_at, updated_at
			   FROM projects
			  WHERE peer_id = ? AND status = ?
			  ORDER BY updated_at DESC
			  LIMIT ?`,
			strings.TrimSpace(peerID), string(status), limit)
	} else {
		rows, err = s.db.QueryContext(ctx,
			`SELECT id, peer_id, title, COALESCE(description,''), status, COALESCE(labels,''), created_at, updated_at
			   FROM projects
			  WHERE peer_id = ?
			  ORDER BY updated_at DESC
			  LIMIT ?`,
			strings.TrimSpace(peerID), limit)
	}
	if err != nil {
		return nil, fmt.Errorf("project list: %w", err)
	}
	defer rows.Close()
	projects, err := collectProjects(rows)
	if err != nil {
		return nil, err
	}
	// Attach linked task IDs to each project.
	for i := range projects {
		tasks, lerr := s.linkedTasks(ctx, projects[i].ID)
		if lerr != nil {
			return nil, lerr
		}
		projects[i].LinkedTasks = tasks
	}
	return projects, nil
}

// Update applies a partial patch to a project and returns the updated record.
func (s *Store) Update(ctx context.Context, peerID, id string, patch Patch) (*Project, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("project update: begin tx: %w", err)
	}
	defer tx.Rollback()

	p, err := loadProjectTx(ctx, tx, peerID, id)
	if err != nil {
		return nil, err
	}
	updated := applyPatch(*p, patch)
	updated.UpdatedAt = time.Now().Unix()

	if _, err := tx.ExecContext(ctx,
		`UPDATE projects SET title = ?, description = ?, labels = ?, updated_at = ?
		  WHERE id = ? AND peer_id = ?`,
		updated.Title, updated.Description, strings.Join(updated.Labels, ","), updated.UpdatedAt,
		updated.ID, updated.PeerID); err != nil {
		return nil, fmt.Errorf("project update: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("project update: commit: %w", err)
	}
	tasks, err := s.linkedTasks(ctx, updated.ID)
	if err != nil {
		return nil, err
	}
	updated.LinkedTasks = tasks
	return &updated, nil
}

// Archive sets the project status to archived.
func (s *Store) Archive(ctx context.Context, peerID, id string) (*Project, error) {
	return s.setStatus(ctx, peerID, id, ProjectArchived)
}

// Unarchive restores the project status to active.
func (s *Store) Unarchive(ctx context.Context, peerID, id string) (*Project, error) {
	return s.setStatus(ctx, peerID, id, ProjectActive)
}

func (s *Store) setStatus(ctx context.Context, peerID, id string, status ProjectStatus) (*Project, error) {
	now := time.Now().Unix()
	result, err := s.db.ExecContext(ctx,
		`UPDATE projects SET status = ?, updated_at = ? WHERE id = ? AND peer_id = ?`,
		string(status), now, strings.TrimSpace(id), strings.TrimSpace(peerID))
	if err != nil {
		return nil, fmt.Errorf("project set status: %w", err)
	}
	n, err := result.RowsAffected()
	if err != nil {
		return nil, fmt.Errorf("project set status rows: %w", err)
	}
	if n == 0 {
		return nil, fmt.Errorf("project %s not found", id)
	}
	return s.Get(ctx, peerID, id)
}

// LinkTask associates a task ID with a project. Idempotent — linking the same
// task twice is not an error.
func (s *Store) LinkTask(ctx context.Context, peerID, id, taskID string) (*Project, error) {
	// Verify ownership.
	if _, err := s.Get(ctx, peerID, id); err != nil {
		return nil, err
	}
	taskID = strings.TrimSpace(taskID)
	if taskID == "" {
		return nil, fmt.Errorf("task_id is required")
	}
	now := time.Now().Unix()
	if _, err := s.db.ExecContext(ctx,
		`INSERT OR IGNORE INTO project_tasks(project_id, task_id, linked_at) VALUES (?,?,?)`,
		strings.TrimSpace(id), taskID, now); err != nil {
		return nil, fmt.Errorf("project link task: %w", err)
	}
	// Bump project updated_at.
	if _, err := s.db.ExecContext(ctx,
		`UPDATE projects SET updated_at = ? WHERE id = ? AND peer_id = ?`,
		now, strings.TrimSpace(id), strings.TrimSpace(peerID)); err != nil {
		return nil, fmt.Errorf("project link task update ts: %w", err)
	}
	return s.Get(ctx, peerID, id)
}

// UnlinkTask removes a task association from a project.
func (s *Store) UnlinkTask(ctx context.Context, peerID, id, taskID string) (*Project, error) {
	// Verify ownership.
	if _, err := s.Get(ctx, peerID, id); err != nil {
		return nil, err
	}
	if _, err := s.db.ExecContext(ctx,
		`DELETE FROM project_tasks WHERE project_id = ? AND task_id = ?`,
		strings.TrimSpace(id), strings.TrimSpace(taskID)); err != nil {
		return nil, fmt.Errorf("project unlink task: %w", err)
	}
	return s.Get(ctx, peerID, id)
}

// Delete removes a project and its task links.
func (s *Store) Delete(ctx context.Context, peerID, id string) error {
	result, err := s.db.ExecContext(ctx,
		`DELETE FROM projects WHERE id = ? AND peer_id = ?`,
		strings.TrimSpace(id), strings.TrimSpace(peerID))
	if err != nil {
		return fmt.Errorf("project delete: %w", err)
	}
	n, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("project delete rows: %w", err)
	}
	if n == 0 {
		return fmt.Errorf("project %s not found", id)
	}
	return nil
}

// migrate ensures the schema is up to date.
func migrate(db *sql.DB) error {
	_, err := db.Exec(`
CREATE TABLE IF NOT EXISTS projects (
	id          TEXT PRIMARY KEY,
	peer_id     TEXT NOT NULL,
	title       TEXT NOT NULL,
	description TEXT NOT NULL DEFAULT '',
	status      TEXT NOT NULL DEFAULT 'active',
	labels      TEXT NOT NULL DEFAULT '',
	created_at  INTEGER NOT NULL,
	updated_at  INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_projects_peer_updated ON projects(peer_id, updated_at DESC);
CREATE TABLE IF NOT EXISTS project_tasks (
	project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
	task_id    TEXT NOT NULL,
	linked_at  INTEGER NOT NULL,
	PRIMARY KEY (project_id, task_id)
);`)
	return err
}

// helpers -----------------------------------------------------------------

func newProject(peerID string, input Input) (*Project, error) {
	id, err := randomID()
	if err != nil {
		return nil, fmt.Errorf("project id: %w", err)
	}
	title := strings.TrimSpace(input.Title)
	if title == "" {
		return nil, fmt.Errorf("title is required")
	}
	now := time.Now().Unix()
	return &Project{
		ID:          id,
		PeerID:      peerID,
		Title:       title,
		Description: strings.TrimSpace(input.Description),
		Status:      ProjectActive,
		Labels:      normaliseLabels(input.Labels),
		CreatedAt:   now,
		UpdatedAt:   now,
	}, nil
}

func applyPatch(p Project, patch Patch) Project {
	if patch.Title != nil {
		if t := strings.TrimSpace(*patch.Title); t != "" {
			p.Title = t
		}
	}
	if patch.Description != nil {
		p.Description = strings.TrimSpace(*patch.Description)
	}
	if patch.Labels != nil {
		p.Labels = normaliseLabels(*patch.Labels)
	}
	return p
}

type rowScanner interface {
	Scan(dest ...any) error
}

func scanProject(scanner rowScanner) (Project, error) {
	var p Project
	var status, labels string
	err := scanner.Scan(
		&p.ID, &p.PeerID, &p.Title, &p.Description,
		&status, &labels, &p.CreatedAt, &p.UpdatedAt)
	if err != nil {
		return Project{}, err
	}
	p.Status = ProjectStatus(status)
	if labels != "" {
		p.Labels = strings.Split(labels, ",")
	}
	return p, nil
}

func collectProjects(rows *sql.Rows) ([]Project, error) {
	var out []Project
	for rows.Next() {
		p, err := scanProject(rows)
		if err != nil {
			return nil, fmt.Errorf("project scan: %w", err)
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func loadProjectTx(ctx context.Context, tx *sql.Tx, peerID, id string) (*Project, error) {
	p, err := scanProject(tx.QueryRowContext(ctx,
		`SELECT id, peer_id, title, COALESCE(description,''), status, COALESCE(labels,''), created_at, updated_at
		   FROM projects WHERE id = ? AND peer_id = ?`,
		strings.TrimSpace(id), strings.TrimSpace(peerID)))
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("project %s not found", id)
	}
	if err != nil {
		return nil, fmt.Errorf("project load: %w", err)
	}
	return &p, nil
}

func (s *Store) linkedTasks(ctx context.Context, projectID string) ([]string, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT task_id FROM project_tasks WHERE project_id = ? ORDER BY linked_at ASC`,
		projectID)
	if err != nil {
		return nil, fmt.Errorf("project linked tasks: %w", err)
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("project task scan: %w", err)
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func normaliseLabels(labels []string) []string {
	var out []string
	for _, l := range labels {
		l = strings.TrimSpace(l)
		if l != "" {
			out = append(out, l)
		}
	}
	return out
}

func randomID() (string, error) {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}
