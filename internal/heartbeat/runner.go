package heartbeat

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/ffimnsr/koios/internal/session"
	"github.com/ffimnsr/koios/internal/standing"
	"github.com/ffimnsr/koios/internal/types"
)

// provider is the minimal interface the Runner needs from an LLM backend.
type provider interface {
	Complete(ctx context.Context, req *types.ChatRequest) (*types.ChatResponse, error)
}

// peerEntry tracks the running state for one peer.
type peerEntry struct {
	cancel context.CancelFunc
	// wakeCh is a buffered channel (size 1) that signals the peer loop to run
	// an immediate out-of-schedule heartbeat.  Non-blocking sends ensure that
	// multiple rapid WakePeer calls collapse into a single extra run.
	wakeCh chan struct{}
}

// Runner manages per-peer heartbeat goroutines.  It is created once at startup
// and is safe for concurrent use.
type Runner struct {
	prov         provider
	sessionStore *session.Store
	configStore  *ConfigStore
	defaultEvery time.Duration
	timeout      time.Duration
	workspaceDir string
	standingMgr  *standing.Manager

	mu    sync.RWMutex
	peers map[string]*peerEntry

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

// New creates a Runner.  Call Start before using EnsureRunning or WakePeer.
func New(
	prov provider,
	sessionStore *session.Store,
	configStore *ConfigStore,
	defaultEvery time.Duration,
	requestTimeout time.Duration,
	workspaceDir string,
	standingMgr *standing.Manager,
) *Runner {
	ctx, cancel := context.WithCancel(context.Background())
	return &Runner{
		prov:         prov,
		sessionStore: sessionStore,
		configStore:  configStore,
		defaultEvery: defaultEvery,
		timeout:      requestTimeout,
		workspaceDir: workspaceDir,
		standingMgr:  standingMgr,
		peers:        make(map[string]*peerEntry),
		ctx:          ctx,
		cancel:       cancel,
	}
}

// Stop shuts down all peer goroutines and waits for them to exit.
func (r *Runner) Stop() {
	r.cancel()
	r.wg.Wait()
}

// EnsureRunning starts a heartbeat goroutine for peerID if one is not already
// running.  It is safe to call on every incoming request.
func (r *Runner) EnsureRunning(peerID string) {
	r.mu.RLock()
	_, exists := r.peers[peerID]
	r.mu.RUnlock()
	if exists {
		return
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	// Double-check under write lock.
	if _, exists := r.peers[peerID]; exists {
		return
	}

	ctx, cancel := context.WithCancel(r.ctx)
	entry := &peerEntry{cancel: cancel, wakeCh: make(chan struct{}, 1)}
	r.peers[peerID] = entry
	r.wg.Add(1)
	go r.peerLoop(ctx, peerID, entry)
}

// WakePeer triggers an immediate out-of-schedule heartbeat run for peerID.
// If the peer has no goroutine yet, one is started first.
// Multiple rapid calls collapse into a single extra run (non-blocking send).
func (r *Runner) WakePeer(peerID string) {
	r.EnsureRunning(peerID)
	r.mu.RLock()
	entry := r.peers[peerID]
	r.mu.RUnlock()
	if entry == nil {
		return
	}
	select {
	case entry.wakeCh <- struct{}{}:
	default: // a wake is already pending; nothing more to do
	}
}

// SetConfig persists a new heartbeat config for peerID and restarts the peer's
// timer goroutine so the new Every interval takes effect immediately.
func (r *Runner) SetConfig(peerID string, cfg *Config) error {
	if err := r.configStore.Save(peerID, cfg); err != nil {
		return fmt.Errorf("save heartbeat config: %w", err)
	}
	// Restart the goroutine so it picks up the new interval.
	r.mu.Lock()
	if entry, exists := r.peers[peerID]; exists {
		entry.cancel()
		delete(r.peers, peerID)
	}
	r.mu.Unlock()

	if cfg.Enabled {
		r.EnsureRunning(peerID)
	}
	return nil
}

// peerLoop runs the heartbeat timer for a single peer until ctx is cancelled.
func (r *Runner) peerLoop(ctx context.Context, peerID string, entry *peerEntry) {
	defer r.wg.Done()
	defer func() {
		r.mu.Lock()
		delete(r.peers, peerID)
		r.mu.Unlock()
	}()

	cfg, err := r.configStore.GetOrDefault(peerID, r.defaultEvery)
	if err != nil {
		slog.Warn("heartbeat: failed to load config, using defaults",
			"peer", peerID, "error", err)
		cfg = &Config{Enabled: true, Every: r.defaultEvery, AckMaxChars: DefaultAckMaxChars}
	}
	if !cfg.Enabled || cfg.Every <= 0 {
		return
	}

	ticker := time.NewTicker(cfg.Every)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			r.runHeartbeat(ctx, peerID)
		case <-entry.wakeCh:
			r.runHeartbeat(ctx, peerID)
		}
	}
}

// runHeartbeat performs a single heartbeat turn for peerID.
func (r *Runner) runHeartbeat(ctx context.Context, peerID string) {
	cfg, err := r.configStore.GetOrDefault(peerID, r.defaultEvery)
	if err != nil {
		slog.Warn("heartbeat: config load error", "peer", peerID, "error", err)
		return
	}
	if !cfg.Enabled {
		return
	}
	if !cfg.IsInActiveHours(time.Now()) {
		slog.Debug("heartbeat: outside active hours, skipping", "peer", peerID)
		return
	}

	prompt := cfg.EffectivePrompt()
	msgs := r.buildHeartbeatMessages(peerID, prompt)
	req := &types.ChatRequest{
		Messages: msgs,
	}

	callCtx, cancel := context.WithTimeout(ctx, r.timeout)
	defer cancel()

	resp, err := r.prov.Complete(callCtx, req)
	if err != nil {
		slog.Warn("heartbeat: LLM call failed", "peer", peerID, "error", err)
		return
	}
	if len(resp.Choices) == 0 {
		return
	}

	text := resp.Choices[0].Message.Content
	if isHeartbeatOK(text, cfg.EffectiveAckMaxChars()) {
		slog.Debug("heartbeat: HEARTBEAT_OK, dropping silently", "peer", peerID)
		return
	}

	// Prepend the heartbeat marker and store as an assistant message.
	stored := "[heartbeat] " + text
	r.sessionStore.AppendWithSource(peerID, "heartbeat", types.Message{Role: "assistant", Content: stored})
	slog.Info("heartbeat: stored alert for peer", "peer", peerID, "chars", len(text))
}

func (r *Runner) buildHeartbeatMessages(peerID, prompt string) []types.Message {
	history := r.sessionStore.Get(peerID).History()
	msgs := make([]types.Message, 0, len(history)+3)
	if r.standingMgr != nil {
		msg, err := r.standingMgr.SystemMessage(peerID)
		if err == nil && msg != nil {
			msgs = append(msgs, *msg)
		}
	}
	if instructions := r.loadHeartbeatInstructions(); instructions != "" {
		msgs = append(msgs, types.Message{
			Role:    "system",
			Content: "HEARTBEAT.md\n\n" + instructions,
		})
	}
	msgs = append(msgs, history...)
	msgs = append(msgs, types.Message{Role: "user", Content: prompt})
	return msgs
}

func (r *Runner) loadHeartbeatInstructions() string {
	if strings.TrimSpace(r.workspaceDir) == "" {
		return ""
	}
	data, err := os.ReadFile(filepath.Join(r.workspaceDir, "HEARTBEAT.md"))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

// isHeartbeatOK returns true when the reply should be silently discarded:
// the text starts or ends with "HEARTBEAT_OK" and the remaining content is
// at most ackMaxChars characters.
func isHeartbeatOK(text string, ackMaxChars int) bool {
	trimmed := strings.TrimSpace(text)
	const token = "HEARTBEAT_OK"

	var rest string
	if strings.HasPrefix(trimmed, token) {
		rest = strings.TrimSpace(trimmed[len(token):])
	} else if strings.HasSuffix(trimmed, token) {
		rest = strings.TrimSpace(trimmed[:len(trimmed)-len(token)])
	} else {
		return false
	}
	return len(rest) <= ackMaxChars
}
