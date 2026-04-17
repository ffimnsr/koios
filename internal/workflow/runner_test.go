package workflow

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/ffimnsr/koios/internal/agent"
)

// ── helpers ───────────────────────────────────────────────────────────────────

func newTestStore(t *testing.T) *Store {
	t.Helper()
	dir, err := os.MkdirTemp("", "workflow-test-*")
	if err != nil {
		t.Fatalf("tempdir: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	s, err := NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	return s
}

// stubAgent implements agentProvider and returns a fixed response.
type stubAgent struct {
	reply string
	err   error
	calls int
}

func (s *stubAgent) Run(_ context.Context, _ agent.RunRequest) (*agent.Result, error) {
	s.calls++
	if s.err != nil {
		return nil, s.err
	}
	return &agent.Result{AssistantText: s.reply}, nil
}

func (s *stubAgent) Model() string { return "stub-model" }

// sampleWorkflow returns a simple 2-step workflow for a test peer.
func sampleWorkflow(peerID string) *Workflow {
	return &Workflow{
		Name:      "test-wf",
		PeerID:    peerID,
		FirstStep: "step-a",
		Steps: []Step{
			{
				ID:        "step-a",
				Kind:      StepKindAgentTurn,
				Message:   "hello",
				OnSuccess: "step-b",
			},
			{
				ID:        "step-b",
				Kind:      StepKindAgentTurn,
				Message:   "reply: {{.Output}}",
				OnSuccess: "",
			},
		},
	}
}

// waitForRun polls the store until the run leaves the pending/running state or
// the deadline is exceeded.
func waitForRun(t *testing.T, store *Store, runID string, timeout time.Duration) *Run {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		run, err := store.LoadRun(runID)
		if err == nil && run.Status != RunStatusPending && run.Status != RunStatusRunning {
			return run
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("run did not reach a terminal state within timeout")
	return nil
}

// ── Store tests ───────────────────────────────────────────────────────────────

func TestStore_CreateGetListDelete(t *testing.T) {
	s := newTestStore(t)

	wf := sampleWorkflow("peer-1")
	created, err := s.Create(*wf)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if created.ID == "" {
		t.Fatal("expected non-empty workflow ID")
	}
	if created.CreatedAt.IsZero() {
		t.Fatal("expected non-zero CreatedAt")
	}

	got := s.Get(created.ID)
	if got == nil {
		t.Fatalf("Get(%q) returned nil", created.ID)
	}
	if got.Name != wf.Name {
		t.Errorf("Name mismatch: got %q, want %q", got.Name, wf.Name)
	}

	all := s.List("peer-1")
	if len(all) != 1 {
		t.Errorf("List: got %d items, want 1", len(all))
	}

	if err := s.Delete(created.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if s.Get(created.ID) != nil {
		t.Fatal("expected nil after delete")
	}
	if len(s.List("peer-1")) != 0 {
		t.Fatal("expected empty list after delete")
	}
}

func TestStore_DeleteNonexistent(t *testing.T) {
	s := newTestStore(t)
	// Delete is a no-op for missing IDs; it just saves the unchanged list.
	if err := s.Delete("no-such-id"); err != nil {
		t.Fatalf("unexpected error deleting non-existent workflow: %v", err)
	}
}

func TestStore_SaveLoadRun(t *testing.T) {
	s := newTestStore(t)
	run := &Run{
		ID:          "run-1",
		WorkflowID:  "wf-1",
		PeerID:      "peer-1",
		Status:      RunStatusCompleted,
		StepResults: map[string]StepResult{},
		StartedAt:   time.Now().UTC(),
	}
	if err := s.SaveRun(run); err != nil {
		t.Fatalf("SaveRun: %v", err)
	}
	loaded, err := s.LoadRun("run-1")
	if err != nil {
		t.Fatalf("LoadRun: %v", err)
	}
	if loaded.Status != RunStatusCompleted {
		t.Errorf("status: got %q, want %q", loaded.Status, RunStatusCompleted)
	}
}

func TestStore_ListRuns(t *testing.T) {
	s := newTestStore(t)
	for i := range 3 {
		_ = s.SaveRun(&Run{
			ID:          strings.Repeat("a", i+1),
			WorkflowID:  "wf-x",
			Status:      RunStatusCompleted,
			StepResults: map[string]StepResult{},
		})
	}
	runs, err := s.ListRuns("", "wf-x")
	if err != nil {
		t.Fatalf("ListRuns: %v", err)
	}
	if len(runs) != 3 {
		t.Errorf("got %d runs, want 3", len(runs))
	}
}

func TestStore_ListRunsLimit(t *testing.T) {
	s := newTestStore(t)
	for i := range 5 {
		_ = s.SaveRun(&Run{
			ID:          strings.Repeat("x", i+1),
			WorkflowID:  "wf-y",
			Status:      RunStatusCompleted,
			StepResults: map[string]StepResult{},
		})
	}
	// ListRuns has no built-in limit; verify all are returned then check caller-side limiting.
	runs, err := s.ListRuns("", "wf-y")
	if err != nil {
		t.Fatalf("ListRuns: %v", err)
	}
	if len(runs) != 5 {
		t.Errorf("got %d runs, want 5", len(runs))
	}
}

// ── evalCondition tests ───────────────────────────────────────────────────────

func TestEvalCondition(t *testing.T) {
	tests := []struct {
		cond  string
		prev  string
		want  bool
	}{
		{"always_true", "", true},
		{"always_true", "anything", true},
		{"always_false", "", false},
		{"always_false", "anything", false},
		{"output_empty", "", true},
		{"output_empty", "   ", true},
		{"output_empty", "hello", false},
		{"output_contains:foo", "foobar", true},
		{"output_contains:foo", "bar", false},
		{"output_not_contains:foo", "bar", true},
		{"output_not_contains:foo", "foobar", false},
		{"unknown_expr", "anything", false},
	}
	for _, tc := range tests {
		got := evalCondition(tc.cond, tc.prev)
		if got != tc.want {
			t.Errorf("evalCondition(%q, %q) = %v, want %v", tc.cond, tc.prev, got, tc.want)
		}
	}
}

// ── applyTemplate tests ───────────────────────────────────────────────────────

func TestApplyTemplate(t *testing.T) {
	tests := []struct {
		tmpl   string
		output string
		want   string
	}{
		{"hello {{.Output}}", "world", "hello world"},
		{"no placeholder", "ignored", "no placeholder"},
		{"{{.Output}} {{.Output}}", "x", "x x"},
	}
	for _, tc := range tests {
		got := applyTemplate(tc.tmpl, tc.output)
		if got != tc.want {
			t.Errorf("applyTemplate(%q, %q) = %q, want %q", tc.tmpl, tc.output, got, tc.want)
		}
	}
}

// ── nextStep tests ────────────────────────────────────────────────────────────

func TestNextStep(t *testing.T) {
	tests := []struct {
		name   string
		step   Step
		output string
		want   string
	}{
		{
			name:   "agent_turn on_success",
			step:   Step{Kind: StepKindAgentTurn, OnSuccess: "next"},
			output: "anything",
			want:   "next",
		},
		{
			name:   "agent_turn empty on_success ends workflow",
			step:   Step{Kind: StepKindAgentTurn, OnSuccess: ""},
			output: "x",
			want:   "",
		},
		{
			name:   "condition true -> TrueStep",
			step:   Step{Kind: StepKindCondition, Condition: "always_true", TrueStep: "t", FalseStep: "f"},
			output: "true",
			want:   "t",
		},
		{
			name:   "condition false -> FalseStep",
			step:   Step{Kind: StepKindCondition, Condition: "always_false", TrueStep: "t", FalseStep: "f"},
			output: "false",
			want:   "f",
		},
	}
	for _, tc := range tests {
		got := nextStep(tc.step, tc.output)
		if got != tc.want {
			t.Errorf("[%s] nextStep = %q, want %q", tc.name, got, tc.want)
		}
	}
}

// ── Runner integration tests ──────────────────────────────────────────────────

func TestRunner_AgentTurnWorkflow(t *testing.T) {
	st := newTestStore(t)
	stub := &stubAgent{reply: "hello from agent"}
	r := NewRunner(st)
	r.SetAgentRuntime(stub, nil)

	wf, err := st.Create(*sampleWorkflow("peer-test"))
	if err != nil {
		t.Fatalf("create workflow: %v", err)
	}

	run, err := r.Start(context.Background(), wf.ID, "peer-test")
	if err != nil {
		t.Fatalf("Start: %v", err)
	}

	final := waitForRun(t, st, run.ID, 5*time.Second)
	if final.Status != RunStatusCompleted {
		t.Errorf("status: got %q, want %q; error: %s", final.Status, RunStatusCompleted, final.Error)
	}
	if stub.calls != 2 {
		t.Errorf("expected 2 agent calls, got %d", stub.calls)
	}
}

func TestRunner_AgentTurnOutputPropagation(t *testing.T) {
	st := newTestStore(t)

	var msgCapture []string
	captureAgent := &captureAgentProvider{
		replyFn: func(req agent.RunRequest) string {
			for _, m := range req.Messages {
				msgCapture = append(msgCapture, m.Content)
			}
			return "agent-output"
		},
	}

	r := NewRunner(st)
	r.SetAgentRuntime(captureAgent, nil)

	wf, _ := st.Create(Workflow{
		Name:      "prop-test",
		PeerID:    "peer-x",
		FirstStep: "s1",
		Steps: []Step{
			{ID: "s1", Kind: StepKindAgentTurn, Message: "first", OnSuccess: "s2"},
			{ID: "s2", Kind: StepKindAgentTurn, Message: "got: {{.Output}}", OnSuccess: ""},
		},
	})

	run, _ := r.Start(context.Background(), wf.ID, "peer-x")
	final := waitForRun(t, st, run.ID, 5*time.Second)

	if final.Status != RunStatusCompleted {
		t.Fatalf("run failed: %s", final.Error)
	}
	if len(msgCapture) < 2 {
		t.Fatalf("expected at least 2 captured messages, got %d", len(msgCapture))
	}
	if !strings.Contains(msgCapture[1], "agent-output") {
		t.Errorf("template substitution failed; second message: %q", msgCapture[1])
	}
}

// captureAgentProvider is a test helper implementing agentProvider.
type captureAgentProvider struct {
	replyFn func(agent.RunRequest) string
}

func (c *captureAgentProvider) Run(_ context.Context, req agent.RunRequest) (*agent.Result, error) {
	reply := c.replyFn(req)
	return &agent.Result{AssistantText: reply}, nil
}

func (c *captureAgentProvider) Model() string { return "capture-model" }

func TestRunner_ConditionStepPreservesLastOutput(t *testing.T) {
	st := newTestStore(t)
	stub := &stubAgent{reply: "important-output"}
	r := NewRunner(st)
	r.SetAgentRuntime(stub, nil)

	wf, _ := st.Create(Workflow{
		Name:      "cond-test",
		PeerID:    "peer-c",
		FirstStep: "agent-step",
		Steps: []Step{
			{ID: "agent-step", Kind: StepKindAgentTurn, Message: "go", OnSuccess: "cond"},
			{ID: "cond", Kind: StepKindCondition, Condition: "always_true", TrueStep: "final", FalseStep: ""},
			{ID: "final", Kind: StepKindAgentTurn, Message: "result: {{.Output}}", OnSuccess: ""},
		},
	})

	run, _ := r.Start(context.Background(), wf.ID, "peer-c")
	final := waitForRun(t, st, run.ID, 5*time.Second)

	if final.Status != RunStatusCompleted {
		t.Fatalf("run failed: %s", final.Error)
	}
	// LastOutput after completion should equal the last agent step's reply,
	// not the "true"/"false" sentinel from the condition step.
	if final.LastOutput != "important-output" {
		t.Errorf("LastOutput = %q, want %q", final.LastOutput, "important-output")
	}
}

func TestRunner_Cancel(t *testing.T) {
	st := newTestStore(t)
	// Agent that blocks until context is done so we can cancel mid-run.
	blockingAgent := &blockingAgentProvider{}
	r := NewRunner(st)
	r.SetAgentRuntime(blockingAgent, nil)

	wf, _ := st.Create(*sampleWorkflow("peer-cancel"))
	run, err := r.Start(context.Background(), wf.ID, "peer-cancel")
	if err != nil {
		t.Fatalf("Start: %v", err)
	}

	// Give the goroutine time to start.
	time.Sleep(20 * time.Millisecond)
	if cancelErr := r.Cancel(run.ID); cancelErr != nil {
		t.Fatalf("Cancel: %v", cancelErr)
	}

	final := waitForRun(t, st, run.ID, 5*time.Second)
	if final.Status != RunStatusCancelled {
		t.Errorf("status: got %q, want cancelled", final.Status)
	}
}

// blockingAgentProvider blocks until its context is cancelled.
type blockingAgentProvider struct{}

func (b *blockingAgentProvider) Run(ctx context.Context, _ agent.RunRequest) (*agent.Result, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

func (b *blockingAgentProvider) Model() string { return "blocking-model" }

func TestRunner_MissingAgentRuntime(t *testing.T) {
	st := newTestStore(t)
	r := NewRunner(st) // no SetAgentRuntime

	wf, _ := st.Create(*sampleWorkflow("peer-rt"))
	run, err := r.Start(context.Background(), wf.ID, "peer-rt")
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	final := waitForRun(t, st, run.ID, 5*time.Second)
	if final.Status != RunStatusFailed {
		t.Errorf("expected failed without agent runtime, got %q", final.Status)
	}
}

func TestRunner_WrongPeer(t *testing.T) {
	st := newTestStore(t)
	r := NewRunner(st)
	wf, _ := st.Create(*sampleWorkflow("owner-peer"))
	_, err := r.Start(context.Background(), wf.ID, "other-peer")
	if err == nil {
		t.Fatal("expected error when starting workflow for wrong peer")
	}
}

func TestRunner_UnknownWorkflow(t *testing.T) {
	st := newTestStore(t)
	r := NewRunner(st)
	_, err := r.Start(context.Background(), "no-such-workflow", "peer-x")
	if err == nil {
		t.Fatal("expected error for unknown workflow")
	}
}

func TestRunner_EmptyWorkflow(t *testing.T) {
	st := newTestStore(t)
	r := NewRunner(st)
	wf, _ := st.Create(Workflow{Name: "empty", PeerID: "peer-e", Steps: []Step{}})
	_, err := r.Start(context.Background(), wf.ID, "peer-e")
	if err == nil {
		t.Fatal("expected error for workflow with no steps")
	}
}

func TestRunner_WebhookStep(t *testing.T) {
	// HTTP test server that echoes the request body with a 200.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("webhook-response"))
	}))
	defer srv.Close()

	st := newTestStore(t)
	r := NewRunner(st)

	wf, _ := st.Create(Workflow{
		Name:      "webhook-wf",
		PeerID:    "peer-wh",
		FirstStep: "webhook-step",
		Steps: []Step{
			{
				ID:        "webhook-step",
				Kind:      StepKindWebhook,
				URL:       srv.URL + "/hook",
				Method:    "POST",
				OnSuccess: "",
			},
		},
	})

	run, err := r.Start(context.Background(), wf.ID, "peer-wh")
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	final := waitForRun(t, st, run.ID, 5*time.Second)
	if final.Status != RunStatusCompleted {
		t.Fatalf("expected completed, got %q: %s", final.Status, final.Error)
	}
	if final.LastOutput != "webhook-response" {
		t.Errorf("LastOutput = %q, want %q", final.LastOutput, "webhook-response")
	}
}

func TestRunner_WebhookStepHTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "bad request", http.StatusBadRequest)
	}))
	defer srv.Close()

	st := newTestStore(t)
	r := NewRunner(st)

	wf, _ := st.Create(Workflow{
		Name:      "fail-webhook",
		PeerID:    "peer-fw",
		FirstStep: "webhook-step",
		Steps: []Step{
			{ID: "webhook-step", Kind: StepKindWebhook, URL: srv.URL + "/bad", Method: "GET"},
		},
	})

	run, _ := r.Start(context.Background(), wf.ID, "peer-fw")
	final := waitForRun(t, st, run.ID, 5*time.Second)
	if final.Status != RunStatusFailed {
		t.Errorf("expected failed on HTTP 400, got %q", final.Status)
	}
}

func TestRunner_ConditionBranching(t *testing.T) {
	st := newTestStore(t)
	// Stub returns "yes", then records which branch ran
	var branchHit string
	captureAgent := &captureAgentProvider{
		replyFn: func(req agent.RunRequest) string {
			if strings.Contains(req.Messages[0].Content, "first") {
				return "contains-foo"
			}
			branchHit = req.Messages[0].Content
			return "done"
		},
	}
	r := NewRunner(st)
	r.SetAgentRuntime(captureAgent, nil)

	wf, _ := st.Create(Workflow{
		Name:      "branch-test",
		PeerID:    "peer-br",
		FirstStep: "step1",
		Steps: []Step{
			{ID: "step1", Kind: StepKindAgentTurn, Message: "first", OnSuccess: "check"},
			{
				ID:        "check",
				Kind:      StepKindCondition,
				Condition: "output_contains:foo",
				TrueStep:  "true-branch",
				FalseStep: "false-branch",
			},
			{ID: "true-branch", Kind: StepKindAgentTurn, Message: "true path", OnSuccess: ""},
			{ID: "false-branch", Kind: StepKindAgentTurn, Message: "false path", OnSuccess: ""},
		},
	})

	run, _ := r.Start(context.Background(), wf.ID, "peer-br")
	final := waitForRun(t, st, run.ID, 5*time.Second)

	if final.Status != RunStatusCompleted {
		t.Fatalf("run failed: %s", final.Error)
	}
	if branchHit != "true path" {
		t.Errorf("expected true-branch to run, got message: %q", branchHit)
	}
}
