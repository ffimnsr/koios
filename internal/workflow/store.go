package workflow

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/google/uuid"
)

// Store persists workflow definitions and run records to disk.
//
// Definitions are kept in a single JSON array at <dir>/workflows.json.
// Per-run records are written to <dir>/runs/<run-id>.json.
type Store struct {
	dir string
	mu  sync.Mutex
	wf  map[string]*Workflow // keyed by ID
}

// NewStore opens (or creates) a workflow store rooted at dir.
func NewStore(dir string) (*Store, error) {
	if err := os.MkdirAll(filepath.Join(dir, "runs"), 0o700); err != nil {
		return nil, fmt.Errorf("workflow store: mkdir: %w", err)
	}
	s := &Store{dir: dir, wf: make(map[string]*Workflow)}
	if err := s.load(); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *Store) defPath() string             { return filepath.Join(s.dir, "workflows.json") }
func (s *Store) runPath(runID string) string  { return filepath.Join(s.dir, "runs", runID+".json") }

// load reads all workflow definitions from disk into the in-memory map.
func (s *Store) load() error {
	data, err := os.ReadFile(s.defPath())
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("workflow store: load definitions: %w", err)
	}
	var list []*Workflow
	if err := json.Unmarshal(data, &list); err != nil {
		return fmt.Errorf("workflow store: decode definitions: %w", err)
	}
	for _, wf := range list {
		s.wf[wf.ID] = wf
	}
	return nil
}

// save flushes all in-memory definitions to disk atomically.
// Must be called with s.mu held.
func (s *Store) save() error {
	list := make([]*Workflow, 0, len(s.wf))
	for _, wf := range s.wf {
		list = append(list, wf)
	}
	sort.Slice(list, func(i, j int) bool {
		return list[i].CreatedAt.Before(list[j].CreatedAt)
	})
	data, err := json.MarshalIndent(list, "", "  ")
	if err != nil {
		return fmt.Errorf("workflow store: marshal definitions: %w", err)
	}
	tmp := s.defPath() + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return fmt.Errorf("workflow store: write definitions: %w", err)
	}
	return os.Rename(tmp, s.defPath())
}

// Create stores a new workflow definition, assigning a fresh ID, and returns
// the persisted copy.
func (s *Store) Create(wf Workflow) (*Workflow, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now().UTC()
	wf.ID = uuid.New().String()
	wf.CreatedAt = now
	wf.UpdatedAt = now
	s.wf[wf.ID] = &wf
	if err := s.save(); err != nil {
		delete(s.wf, wf.ID)
		return nil, fmt.Errorf("workflow store: create: %w", err)
	}
	cp := wf
	return &cp, nil
}

// Get returns a copy of one workflow definition, or nil when not found.
func (s *Store) Get(id string) *Workflow {
	s.mu.Lock()
	defer s.mu.Unlock()
	wf := s.wf[id]
	if wf == nil {
		return nil
	}
	cp := *wf
	return &cp
}

// List returns copies of all workflow definitions that belong to peerID.
// When peerID is empty all definitions are returned.
func (s *Store) List(peerID string) []*Workflow {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []*Workflow
	for _, wf := range s.wf {
		if peerID == "" || wf.PeerID == peerID {
			cp := *wf
			out = append(out, &cp)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].CreatedAt.Before(out[j].CreatedAt)
	})
	return out
}

// Delete removes a workflow definition by ID.  No-op when not found.
func (s *Store) Delete(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.wf, id)
	return s.save()
}

// SaveRun atomically persists a run record to disk.
func (s *Store) SaveRun(run *Run) error {
	data, err := json.MarshalIndent(run, "", "  ")
	if err != nil {
		return fmt.Errorf("workflow store: marshal run: %w", err)
	}
	path := s.runPath(run.ID)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return fmt.Errorf("workflow store: write run: %w", err)
	}
	return os.Rename(tmp, path)
}

// LoadRun reads one run record from disk.  Returns nil without error when the
// record does not exist.
func (s *Store) LoadRun(runID string) (*Run, error) {
	data, err := os.ReadFile(s.runPath(runID))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("workflow store: read run: %w", err)
	}
	var run Run
	if err := json.Unmarshal(data, &run); err != nil {
		return nil, fmt.Errorf("workflow store: decode run: %w", err)
	}
	return &run, nil
}

// ListRuns returns all run records, newest-first, optionally filtered by
// peerID and/or workflowID.  Empty filter values are treated as wildcards.
func (s *Store) ListRuns(peerID, workflowID string) ([]*Run, error) {
	entries, err := os.ReadDir(filepath.Join(s.dir, "runs"))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("workflow store: list runs: %w", err)
	}
	var runs []*Run
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".json" {
			continue
		}
		runID := e.Name()[:len(e.Name())-5]
		run, err := s.LoadRun(runID)
		if err != nil || run == nil {
			continue
		}
		if peerID != "" && run.PeerID != peerID {
			continue
		}
		if workflowID != "" && run.WorkflowID != workflowID {
			continue
		}
		runs = append(runs, run)
	}
	sort.Slice(runs, func(i, j int) bool {
		return runs[i].StartedAt.After(runs[j].StartedAt)
	})
	return runs, nil
}
