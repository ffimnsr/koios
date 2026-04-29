// Package plans provides structured task plans with explicit step status.
// Plans support in-progress agent work without implying the user has created
// persistent personal commitments (use the tasks package for those).
package plans

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

// StepStatus represents the lifecycle of one plan step.
type StepStatus string

const (
	StepPending    StepStatus = "pending"
	StepInProgress StepStatus = "in_progress"
	StepDone       StepStatus = "done"
	StepSkipped    StepStatus = "skipped"
)

// PlanStatus represents the overall plan state.
type PlanStatus string

const (
	PlanOpen       PlanStatus = "open"
	PlanInProgress PlanStatus = "in_progress"
	PlanCompleted  PlanStatus = "completed"
	PlanCancelled  PlanStatus = "cancelled"
)

// Step is one discrete action item within a Plan.
type Step struct {
	ID          string     `json:"id"`
	Title       string     `json:"title"`
	Status      StepStatus `json:"status"`
	Notes       string     `json:"notes,omitempty"`
	CompletedAt int64      `json:"completed_at,omitempty"`
}

// Plan groups a set of steps under a title and tracks overall progress.
type Plan struct {
	ID          string     `json:"id"`
	PeerID      string     `json:"peer_id"`
	Title       string     `json:"title"`
	Description string     `json:"description,omitempty"`
	Status      PlanStatus `json:"status"`
	Steps       []Step     `json:"steps"`
	CreatedAt   int64      `json:"created_at"`
	UpdatedAt   int64      `json:"updated_at"`
}

// StepInput is used when supplying steps at creation time.
type StepInput struct {
	Title string `json:"title"`
	Notes string `json:"notes,omitempty"`
}

// Input holds the required and optional fields for creating a plan.
type Input struct {
	Title       string
	Description string
	Steps       []StepInput
}

// Patch carries optional fields for a partial update.
type Patch struct {
	Title       *string
	Description *string
	Status      *PlanStatus
	Steps       *[]StepInput
}

// Store persists plans in a local SQLite database.
type Store struct {
	db *sql.DB
}

// New opens (or creates) the plans SQLite database at dbPath.
func New(dbPath string) (*Store, error) {
	db, err := sql.Open("sqlite", dbPath+"?_journal_mode=WAL&_foreign_keys=on")
	if err != nil {
		return nil, fmt.Errorf("plans: open db: %w", err)
	}
	db.SetMaxOpenConns(1)
	if err := migrate(db); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("plans: migrate: %w", err)
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

// Create inserts a new plan with the given steps and returns the saved record.
func (s *Store) Create(ctx context.Context, peerID string, input Input) (*Plan, error) {
	plan, err := newPlan(strings.TrimSpace(peerID), input)
	if err != nil {
		return nil, err
	}
	stepsJSON, err := marshalSteps(plan.Steps)
	if err != nil {
		return nil, err
	}
	if _, err := s.db.ExecContext(ctx,
		`INSERT INTO plans(id, peer_id, title, description, status, steps, created_at, updated_at)
		 VALUES (?,?,?,?,?,?,?,?)`,
		plan.ID, plan.PeerID, plan.Title, plan.Description, string(plan.Status),
		stepsJSON, plan.CreatedAt, plan.UpdatedAt); err != nil {
		return nil, fmt.Errorf("plan create: %w", err)
	}
	return plan, nil
}

// Get returns a single plan by ID, verifying peer ownership.
func (s *Store) Get(ctx context.Context, peerID, id string) (*Plan, error) {
	plan, err := scanPlan(s.db.QueryRowContext(ctx,
		`SELECT id, peer_id, title, COALESCE(description,''), status, COALESCE(steps,'[]'), created_at, updated_at
		   FROM plans WHERE id = ?`,
		strings.TrimSpace(id)))
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("plan %s not found", id)
	}
	if err != nil {
		return nil, fmt.Errorf("plan get: %w", err)
	}
	if plan.PeerID != strings.TrimSpace(peerID) {
		return nil, fmt.Errorf("plan %s does not belong to peer", id)
	}
	return &plan, nil
}

// List returns recent plans for peerID, optionally filtered by status.
func (s *Store) List(ctx context.Context, peerID string, limit int, status PlanStatus) ([]Plan, error) {
	if limit <= 0 {
		limit = 50
	}
	query := `SELECT id, peer_id, title, COALESCE(description,''), status, COALESCE(steps,'[]'), created_at, updated_at
	            FROM plans WHERE peer_id = ?`
	args := []any{strings.TrimSpace(peerID)}
	if status != "" {
		query += ` AND status = ?`
		args = append(args, string(status))
	}
	query += ` ORDER BY updated_at DESC LIMIT ?`
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("plan list: %w", err)
	}
	defer rows.Close()
	return collectPlans(rows)
}

// Update applies a partial patch to an existing plan. When Patch.Steps is set
// the entire step list is replaced, giving the caller full control over order.
func (s *Store) Update(ctx context.Context, peerID, id string, patch Patch) (*Plan, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("plan update: begin tx: %w", err)
	}
	defer tx.Rollback()

	plan, err := loadPlanTx(ctx, tx, peerID, id)
	if err != nil {
		return nil, err
	}
	updated := applyPatch(*plan, patch)
	updated.UpdatedAt = time.Now().Unix()

	stepsJSON, err := marshalSteps(updated.Steps)
	if err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE plans SET title = ?, description = ?, status = ?, steps = ?, updated_at = ?
		  WHERE id = ? AND peer_id = ?`,
		updated.Title, updated.Description, string(updated.Status), stepsJSON,
		updated.UpdatedAt, updated.ID, updated.PeerID); err != nil {
		return nil, fmt.Errorf("plan update: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("plan update: commit: %w", err)
	}
	return &updated, nil
}

// CompleteStep marks a single step as done and returns the updated plan. If
// all steps are done the plan status is automatically set to completed.
func (s *Store) CompleteStep(ctx context.Context, peerID, planID, stepID, notes string) (*Plan, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("plan complete_step: begin tx: %w", err)
	}
	defer tx.Rollback()

	plan, err := loadPlanTx(ctx, tx, peerID, planID)
	if err != nil {
		return nil, err
	}

	found := false
	for i, step := range plan.Steps {
		if step.ID == strings.TrimSpace(stepID) {
			plan.Steps[i].Status = StepDone
			plan.Steps[i].CompletedAt = time.Now().Unix()
			if notes != "" {
				plan.Steps[i].Notes = notes
			}
			found = true
			break
		}
	}
	if !found {
		return nil, fmt.Errorf("step %s not found in plan %s", stepID, planID)
	}

	// Auto-complete plan when all steps are done.
	allDone := true
	for _, step := range plan.Steps {
		if step.Status != StepDone && step.Status != StepSkipped {
			allDone = false
			break
		}
	}
	if allDone && plan.Status != PlanCompleted {
		plan.Status = PlanCompleted
	}
	plan.UpdatedAt = time.Now().Unix()

	stepsJSON, err := marshalSteps(plan.Steps)
	if err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE plans SET status = ?, steps = ?, updated_at = ? WHERE id = ? AND peer_id = ?`,
		string(plan.Status), stepsJSON, plan.UpdatedAt, plan.ID, plan.PeerID); err != nil {
		return nil, fmt.Errorf("plan complete_step: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("plan complete_step: commit: %w", err)
	}
	return plan, nil
}

// Delete removes a plan by ID, verifying peer ownership.
func (s *Store) Delete(ctx context.Context, peerID, id string) error {
	result, err := s.db.ExecContext(ctx,
		`DELETE FROM plans WHERE id = ? AND peer_id = ?`,
		strings.TrimSpace(id), strings.TrimSpace(peerID))
	if err != nil {
		return fmt.Errorf("plan delete: %w", err)
	}
	n, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("plan delete rows: %w", err)
	}
	if n == 0 {
		return fmt.Errorf("plan %s not found", id)
	}
	return nil
}

// migrate ensures the schema is up to date.
func migrate(db *sql.DB) error {
	_, err := db.Exec(`CREATE TABLE IF NOT EXISTS plans (
	id          TEXT PRIMARY KEY,
	peer_id     TEXT NOT NULL,
	title       TEXT NOT NULL,
	description TEXT NOT NULL DEFAULT '',
	status      TEXT NOT NULL DEFAULT 'open',
	steps       TEXT NOT NULL DEFAULT '[]',
	created_at  INTEGER NOT NULL,
	updated_at  INTEGER NOT NULL
	);
	CREATE INDEX IF NOT EXISTS idx_plans_peer_updated ON plans(peer_id, updated_at DESC);
	CREATE INDEX IF NOT EXISTS idx_plans_peer_status  ON plans(peer_id, status);`)
	return err
}

// helpers -----------------------------------------------------------------

func newPlan(peerID string, input Input) (*Plan, error) {
	id, err := randomID()
	if err != nil {
		return nil, fmt.Errorf("plan id: %w", err)
	}
	title := strings.TrimSpace(input.Title)
	if title == "" {
		return nil, fmt.Errorf("title is required")
	}
	steps := make([]Step, 0, len(input.Steps))
	for _, si := range input.Steps {
		si.Title = strings.TrimSpace(si.Title)
		if si.Title == "" {
			continue
		}
		sid, err := randomID()
		if err != nil {
			return nil, fmt.Errorf("step id: %w", err)
		}
		steps = append(steps, Step{
			ID:     sid,
			Title:  si.Title,
			Status: StepPending,
			Notes:  strings.TrimSpace(si.Notes),
		})
	}
	now := time.Now().Unix()
	return &Plan{
		ID:          id,
		PeerID:      peerID,
		Title:       title,
		Description: strings.TrimSpace(input.Description),
		Status:      PlanOpen,
		Steps:       steps,
		CreatedAt:   now,
		UpdatedAt:   now,
	}, nil
}

func applyPatch(plan Plan, patch Patch) Plan {
	if patch.Title != nil {
		plan.Title = strings.TrimSpace(*patch.Title)
	}
	if patch.Description != nil {
		plan.Description = strings.TrimSpace(*patch.Description)
	}
	if patch.Status != nil {
		plan.Status = *patch.Status
	}
	if patch.Steps != nil {
		steps := make([]Step, 0, len(*patch.Steps))
		for _, si := range *patch.Steps {
			si.Title = strings.TrimSpace(si.Title)
			if si.Title == "" {
				continue
			}
			sid, _ := randomID()
			steps = append(steps, Step{
				ID:     sid,
				Title:  si.Title,
				Status: StepPending,
				Notes:  strings.TrimSpace(si.Notes),
			})
		}
		plan.Steps = steps
	}
	return plan
}

type rowScanner interface {
	Scan(dest ...any) error
}

func scanPlan(scanner rowScanner) (Plan, error) {
	var plan Plan
	var stepsJSON string
	var status string
	err := scanner.Scan(
		&plan.ID, &plan.PeerID, &plan.Title, &plan.Description,
		&status, &stepsJSON, &plan.CreatedAt, &plan.UpdatedAt)
	if err != nil {
		return Plan{}, err
	}
	plan.Status = PlanStatus(status)
	if err := json.Unmarshal([]byte(stepsJSON), &plan.Steps); err != nil {
		plan.Steps = []Step{}
	}
	return plan, nil
}

func collectPlans(rows *sql.Rows) ([]Plan, error) {
	var out []Plan
	for rows.Next() {
		plan, err := scanPlan(rows)
		if err != nil {
			return nil, fmt.Errorf("plan scan: %w", err)
		}
		out = append(out, plan)
	}
	return out, rows.Err()
}

func loadPlanTx(ctx context.Context, tx *sql.Tx, peerID, id string) (*Plan, error) {
	plan, err := scanPlan(tx.QueryRowContext(ctx,
		`SELECT id, peer_id, title, COALESCE(description,''), status, COALESCE(steps,'[]'), created_at, updated_at
		   FROM plans WHERE id = ? AND peer_id = ?`,
		strings.TrimSpace(id), strings.TrimSpace(peerID)))
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("plan %s not found", id)
	}
	if err != nil {
		return nil, fmt.Errorf("plan load: %w", err)
	}
	return &plan, nil
}

func marshalSteps(steps []Step) (string, error) {
	if steps == nil {
		steps = []Step{}
	}
	b, err := json.Marshal(steps)
	if err != nil {
		return "", fmt.Errorf("plan steps marshal: %w", err)
	}
	return string(b), nil
}

func randomID() (string, error) {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}
