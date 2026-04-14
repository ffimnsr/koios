// Jobs are scoped per-peer (X-Peer-ID) and support three schedule kinds:
//   - "at"    — one-shot ISO 8601 timestamp
//   - "every" — fixed interval in milliseconds
//   - "cron"  — 5-field cron expression (with optional IANA timezone)
//
// Job definitions are persisted as JSON at <CronDir>/jobs.json.
// Per-job run history is appended to <CronDir>/runs/<jobID>.jsonl.
package scheduler

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/google/uuid"
)

// ScheduleKind names the three supported schedule variants.
type ScheduleKind string

const (
	KindAt    ScheduleKind = "at"
	KindEvery ScheduleKind = "every"
	KindCron  ScheduleKind = "cron"
)

// PayloadKind names what the job does when it fires.
type PayloadKind string

const (
	PayloadSystemEvent PayloadKind = "systemEvent"
	PayloadAgentTurn   PayloadKind = "agentTurn"
)

// Schedule describes when a job runs.
type Schedule struct {
	Kind ScheduleKind `json:"kind"`
	// At is an ISO 8601 timestamp; used when Kind == "at".
	At string `json:"at,omitempty"`
	// EveryMs is a fixed interval in milliseconds; used when Kind == "every".
	EveryMs int64 `json:"every_ms,omitempty"`
	// Expr is a 5-field cron expression; used when Kind == "cron".
	Expr string `json:"expr,omitempty"`
	// Tz is an IANA timezone name for cron expressions.
	// Defaults to UTC when empty.
	Tz string `json:"tz,omitempty"`
	// StaggerMs, when > 0, spreads the job execution by up to this many
	// milliseconds using a deterministic hash of the job ID.
	StaggerMs int64 `json:"stagger_ms,omitempty"`
}

// Payload describes what happens when a job fires.
type Payload struct {
	Kind PayloadKind `json:"kind"`
	// Text is used for systemEvent jobs.
	Text string `json:"text,omitempty"`
	// Message is used for agentTurn jobs.
	Message string `json:"message,omitempty"`
	// IncludeHistory, when true, prepends the peer's current session history
	// to the agentTurn request so the LLM has conversational context.
	// When false (the default) the turn is sent with a fresh context.
	IncludeHistory bool `json:"include_history,omitempty"`
	// PreloadURLs are fetched just before an agentTurn executes and injected as
	// extra context blocks. This is a best-effort lazy-load step so scheduled
	// turns can work against fresh external content.
	PreloadURLs []string `json:"preload_urls,omitempty"`
}

// DispatchPolicy controls how scheduled runs behave when the user is active or
// when an external approval hook should gate execution.
type DispatchPolicy struct {
	// When true, scheduled runs are deferred while the peer is active.
	DeferIfActive bool `json:"defer_if_active,omitempty"`
	// When true, a cron approval hook must allow the run before execution.
	RequireApproval bool `json:"require_approval,omitempty"`
}

// Job is a single persisted cron job entry.
type Job struct {
	JobID          string         `json:"job_id"`
	PeerID         string         `json:"peer_id"`
	Name           string         `json:"name"`
	Description    string         `json:"description,omitempty"`
	Schedule       Schedule       `json:"schedule"`
	Payload        Payload        `json:"payload"`
	Dispatch       DispatchPolicy `json:"dispatch,omitempty"`
	Enabled        bool           `json:"enabled"`
	DeleteAfterRun bool           `json:"delete_after_run"`
	NextRunAt      time.Time      `json:"next_run_at"`
	LastRunAt      time.Time      `json:"last_run_at,omitempty"`
	ConsecErrors   int            `json:"consec_errors,omitempty"`
	CreatedAt      time.Time      `json:"created_at"`
}

// RunStatus represents the outcome of a single job execution.
type RunStatus string

const (
	RunOK    RunStatus = "ok"
	RunError RunStatus = "error"
	RunSkip  RunStatus = "skip"
)

// RunRecord is one entry appended to the per-job JSONL run log.
type RunRecord struct {
	RunID     string    `json:"run_id"`
	JobID     string    `json:"job_id"`
	StartedAt time.Time `json:"started_at"`
	EndedAt   time.Time `json:"ended_at"`
	Status    RunStatus `json:"status"`
	Error     string    `json:"error,omitempty"`
	Output    string    `json:"output,omitempty"`
}

// runLogMaxBytes is the maximum size of a run log file before pruning.
const runLogMaxBytes = 2_000_000

// runLogKeepLines is the number of lines retained when the log is pruned.
const runLogKeepLines = 2000

// jobStoreFile is the name of the JSON file that holds all job definitions.
const jobStoreFile = "jobs.json"

// JobStore persists job definitions to <cronDir>/jobs.json and run records to
// <cronDir>/runs/<jobID>.jsonl. All methods are safe for concurrent use.
type JobStore struct {
	mu      sync.RWMutex
	cronDir string
	jobs    map[string]*Job // keyed by JobID
}

// NewJobStore loads (or creates) the job store rooted at cronDir.
func NewJobStore(cronDir string) (*JobStore, error) {
	if err := os.MkdirAll(cronDir, 0o755); err != nil {
		return nil, fmt.Errorf("create cron dir: %w", err)
	}
	runsDir := filepath.Join(cronDir, "runs")
	if err := os.MkdirAll(runsDir, 0o755); err != nil {
		return nil, fmt.Errorf("create runs dir: %w", err)
	}

	js := &JobStore{cronDir: cronDir, jobs: make(map[string]*Job)}
	if err := js.load(); err != nil {
		return nil, err
	}
	return js, nil
}

// load reads jobs from disk into memory. Missing file is not an error.
func (js *JobStore) load() error {
	path := filepath.Join(js.cronDir, jobStoreFile)
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read job store: %w", err)
	}
	var jobs []*Job
	if err := json.Unmarshal(data, &jobs); err != nil {
		return fmt.Errorf("parse job store: %w", err)
	}
	for _, j := range jobs {
		js.jobs[j.JobID] = j
	}
	return nil
}

// save persists the current in-memory job set atomically (write → rename).
// mu must be held by the caller.
func (js *JobStore) save() error {
	list := make([]*Job, 0, len(js.jobs))
	for _, j := range js.jobs {
		list = append(list, j)
	}
	data, err := json.MarshalIndent(list, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal jobs: %w", err)
	}
	path := filepath.Join(js.cronDir, jobStoreFile)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return fmt.Errorf("write jobs tmp: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("rename jobs: %w", err)
	}
	return nil
}

// List returns all jobs owned by peerID.
func (js *JobStore) List(peerID string) []*Job {
	js.mu.RLock()
	defer js.mu.RUnlock()
	var out []*Job
	for _, j := range js.jobs {
		if j.PeerID == peerID {
			cp := *j
			out = append(out, &cp)
		}
	}
	return out
}

// Get returns a copy of the job by ID, or nil if it does not exist.
func (js *JobStore) Get(jobID string) *Job {
	js.mu.RLock()
	defer js.mu.RUnlock()
	j, ok := js.jobs[jobID]
	if !ok {
		return nil
	}
	cp := *j
	return &cp
}

// Add inserts a new job and persists the store.
func (js *JobStore) Add(j *Job) error {
	js.mu.Lock()
	defer js.mu.Unlock()
	if j.JobID == "" {
		j.JobID = uuid.New().String()
	}
	if j.CreatedAt.IsZero() {
		j.CreatedAt = time.Now().UTC()
	}
	js.jobs[j.JobID] = j
	return js.save()
}

// Update replaces an existing job and persists.
func (js *JobStore) Update(j *Job) error {
	js.mu.Lock()
	defer js.mu.Unlock()
	if _, ok := js.jobs[j.JobID]; !ok {
		return fmt.Errorf("job %s not found", j.JobID)
	}
	js.jobs[j.JobID] = j
	return js.save()
}

// Remove deletes a job by ID and persists. Returns nil if not found.
func (js *JobStore) Remove(jobID string) error {
	js.mu.Lock()
	defer js.mu.Unlock()
	delete(js.jobs, jobID)
	return js.save()
}

// All returns a snapshot of every job regardless of peer.
func (js *JobStore) All() []*Job {
	js.mu.RLock()
	defer js.mu.RUnlock()
	out := make([]*Job, 0, len(js.jobs))
	for _, j := range js.jobs {
		cp := *j
		out = append(out, &cp)
	}
	return out
}

// AppendRunRecord writes a RunRecord to the per-job JSONL log and prunes
// the file if it exceeds runLogMaxBytes.
func (js *JobStore) AppendRunRecord(rec RunRecord) error {
	path := filepath.Join(js.cronDir, "runs", rec.JobID+".jsonl")
	line, err := json.Marshal(rec)
	if err != nil {
		return fmt.Errorf("marshal run record: %w", err)
	}

	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("open run log: %w", err)
	}
	_, writeErr := fmt.Fprintf(f, "%s\n", line)
	closeErr := f.Close()
	if writeErr != nil {
		return fmt.Errorf("write run record: %w", writeErr)
	}
	if closeErr != nil {
		return fmt.Errorf("close run log: %w", closeErr)
	}

	// Prune if the file grew beyond the size limit (best-effort).
	if info, err := os.Stat(path); err == nil && info.Size() > runLogMaxBytes {
		_ = pruneRunLog(path, runLogKeepLines)
	}
	return nil
}

// ReadRunRecords returns the last limit entries from the per-job run log.
func (js *JobStore) ReadRunRecords(jobID string, limit int) ([]RunRecord, error) {
	path := filepath.Join(js.cronDir, "runs", jobID+".jsonl")
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("open run log: %w", err)
	}

	sc := bufio.NewScanner(bytes.NewReader(data))
	var lines []string
	for sc.Scan() {
		if t := sc.Text(); t != "" {
			lines = append(lines, t)
		}
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("scan run log: %w", err)
	}

	start := 0
	if len(lines) > limit {
		start = len(lines) - limit
	}
	lines = lines[start:]

	records := make([]RunRecord, 0, len(lines))
	for _, l := range lines {
		var rec RunRecord
		if err := json.Unmarshal([]byte(l), &rec); err != nil {
			continue // skip malformed lines
		}
		records = append(records, rec)
	}
	return records, nil
}

// pruneRunLog keeps only the newest keepLines lines of path by rewriting it.
func pruneRunLog(path string, keepLines int) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	sc := bufio.NewScanner(bytes.NewReader(data))
	var lines []string
	for sc.Scan() {
		lines = append(lines, sc.Text())
	}
	if len(lines) <= keepLines {
		return nil
	}
	lines = lines[len(lines)-keepLines:]

	var buf bytes.Buffer
	for _, l := range lines {
		buf.WriteString(l)
		buf.WriteByte('\n')
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, buf.Bytes(), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// staggerOffset returns a deterministic delay for jobID within [0, staggerMs).
func staggerOffset(jobID string, staggerMs int64) time.Duration {
	if staggerMs <= 0 {
		return 0
	}
	h := fnv.New32a()
	_, _ = h.Write([]byte(jobID))
	return time.Duration(int64(h.Sum32())%staggerMs) * time.Millisecond
}
