package app

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/ffimnsr/koios/internal/agent"
	"github.com/ffimnsr/koios/internal/config"
	"github.com/ffimnsr/koios/internal/handler"
	"github.com/ffimnsr/koios/internal/heartbeat"
	"github.com/ffimnsr/koios/internal/memory"
	"github.com/ffimnsr/koios/internal/memory/milvus"
	"github.com/ffimnsr/koios/internal/ops"
	"github.com/ffimnsr/koios/internal/presence"
	"github.com/ffimnsr/koios/internal/provider"
	"github.com/ffimnsr/koios/internal/scheduler"
	"github.com/ffimnsr/koios/internal/session"
	"github.com/ffimnsr/koios/internal/standing"
	"github.com/ffimnsr/koios/internal/subagent"
	"github.com/ffimnsr/koios/internal/types"
	"github.com/ffimnsr/koios/internal/workspace"
	"gopkg.in/natefinch/lumberjack.v2"
)

// BuildInfo describes the runtime build metadata reported by the daemon and CLI.
type BuildInfo struct {
	Version   string `json:"version"`
	GitHash   string `json:"git_hash"`
	BuildTime string `json:"build_time"`
}

type compactionMemoryFlusher struct {
	store *memory.Store
}

func (f *compactionMemoryFlusher) FlushCompaction(ctx context.Context, peerID string, _ []types.Message, summary string) error {
	if f == nil || f.store == nil {
		return nil
	}
	summary = strings.TrimSpace(summary)
	if summary == "" {
		return nil
	}
	_, err := f.store.InsertChunk(ctx, peerID, "Session checkpoint before compaction:\n\n"+summary)
	return err
}

// RunDaemon loads configuration, starts the Koios daemon, and blocks until shutdown.
func RunDaemon(build BuildInfo) error {
	setupLogger()

	cfg, err := config.Load()
	if err != nil {
		return err
	}
	workspaceDir, err := os.Getwd()
	if err != nil {
		return err
	}

	prov, err := provider.New(cfg)
	if err != nil {
		return err
	}
	wsStore, err := workspace.New(cfg.WorkspaceRoot, cfg.WorkspacePerAgent, cfg.WorkspaceMaxBytes)
	if err != nil {
		return err
	}

	dailyResetMinutes, err := config.ParseDailyResetMinutes(cfg.SessionDailyResetTime)
	if err != nil {
		return err
	}
	storeOpts := session.Options{
		MaxMessages:       cfg.MaxSessionMessages,
		SessionDir:        cfg.SessionDir(),
		SessionRetention:  cfg.SessionRetention,
		SessionMaxEntries: cfg.SessionMaxEntries,
		IdleResetAfter:    cfg.SessionIdleResetAfter,
		DailyResetMinutes: dailyResetMinutes,
		CompactThreshold:  cfg.CompactThreshold,
		CompactReserve:    cfg.CompactReserve,
	}
	hooks := ops.NewManager(readEnvDuration("KOIOS_HOOK_TIMEOUT", 2*time.Second), readEnvBool("KOIOS_HOOK_FAIL_CLOSED", false))
	if hookWebhook := strings.TrimSpace(os.Getenv("KOIOS_HOOK_WEBHOOK_URL")); hookWebhook != "" {
		hooks.Register(ops.HookMessageReceived, 100, ops.HTTPWebhookHandler(hookWebhook, os.Getenv("KOIOS_HOOK_WEBHOOK_SECRET"), nil))
		hooks.Register(ops.HookBeforeToolCall, 100, ops.HTTPWebhookHandler(hookWebhook, os.Getenv("KOIOS_HOOK_WEBHOOK_SECRET"), nil))
		hooks.Register(ops.HookAfterToolCall, 100, ops.HTTPWebhookHandler(hookWebhook, os.Getenv("KOIOS_HOOK_WEBHOOK_SECRET"), nil))
		hooks.Register(ops.HookBeforeCompaction, 100, ops.HTTPWebhookHandler(hookWebhook, os.Getenv("KOIOS_HOOK_WEBHOOK_SECRET"), nil))
		hooks.Register(ops.HookAfterCompaction, 100, ops.HTTPWebhookHandler(hookWebhook, os.Getenv("KOIOS_HOOK_WEBHOOK_SECRET"), nil))
	}
	storeOpts.Hooks = hooks
	if cfg.CompactThreshold > 0 {
		storeOpts.Compactor = session.NewLLMCompactor(prov, cfg.Model)
	}
	var memStore *memory.Store
	{
		var embedder memory.Embedder
		if cfg.EmbedModel != "" {
			embedder = memory.NewOpenAIEmbedder(cfg.APIKey, cfg.BaseURL, cfg.EmbedModel)
		}
		if cfg.MilvusEnabled {
			milvusClient, milvusErr := milvus.New(context.Background(), cfg.MilvusURL, cfg.MilvusCollection, 0)
			if milvusErr != nil {
				slog.Warn("memory: milvus client init failed, falling back", "err", milvusErr)
				memStore, err = memory.New(cfg.MemoryDBPath(), embedder)
			} else {
				memStore, err = memory.NewWithMilvus(cfg.MemoryDBPath(), embedder, milvusClient)
			}
		} else {
			memStore, err = memory.New(cfg.MemoryDBPath(), embedder)
		}
		if err != nil {
			return err
		}
		slog.Info("long-term memory enabled", "db", cfg.MemoryDBPath(), "inject", cfg.MemoryInject, "milvus", cfg.MilvusEnabled)
	}
	if memStore != nil {
		storeOpts.CompactionMemoryFlusher = &compactionMemoryFlusher{store: memStore}
	}
	store := session.NewWithOptions(storeOpts)
	presenceMgr := presence.NewManager(readEnvDuration("KOIOS_PRESENCE_TYPING_TTL", 8*time.Second))

	var (
		jobStore      *scheduler.JobStore
		sched         *scheduler.Scheduler
		hbRunner      *heartbeat.Runner
		hbConfigStore *heartbeat.ConfigStore
		standingStore *standing.Store
		standingMgr   *standing.Manager
		agentRuntime  *agent.Runtime
		agentCoord    *agent.Coordinator
		subRegistry   *subagent.Registry
		subRuntime    *subagent.Runtime
	)
	standingMgr = standing.NewManager(nil, workspaceDir)
	agentRuntime = agent.NewRuntime(store, prov, cfg.Model, cfg.RequestTimeout, agent.RetryPolicy{
		MaxAttempts:    cfg.AgentRetryAttempts,
		InitialBackoff: cfg.AgentRetryInitialBackoff,
		MaxBackoff:     cfg.AgentRetryMaxBackoff,
	})
	agentRuntime.SetHooks(hooks)
	agentRuntime.EnableMemory(memStore, cfg.MemoryInject, cfg.MemoryTopK)
	agentRuntime.SetPruning(cfg.SessionPruneKeepToolMessages)
	agentRuntime.SetStandingOrders(standingMgr)
	agentRuntime.SetIdentityDir(cfg.WorkspaceRoot)
	agentCoord = agent.NewCoordinator(agentRuntime)
	subRegistry, err = subagent.NewRegistry(cfg.AgentDir())
	if err != nil {
		return err
	}
	subRuntime = subagent.NewRuntime(agentRuntime, store, subRegistry, cfg.AgentMaxChildren)
	{
		slog.Info("subagent registry enabled", "dir", cfg.AgentDir(), "max_children", cfg.AgentMaxChildren)
		go func() {
			ticker := time.NewTicker(15 * time.Minute)
			defer ticker.Stop()
			for range ticker.C {
				removed := subRegistry.Sweep(24 * time.Hour)
				if removed > 0 {
					slog.Info("subagent registry sweep", "removed", removed)
				}
			}
		}()
	}
	standingStore, err = standing.NewStore(cfg.CronDir())
	if err != nil {
		return err
	}
	standingMgr = standing.NewManager(standingStore, workspaceDir)
	agentRuntime.SetStandingOrders(standingMgr)
	jobStore, err = scheduler.NewJobStore(cfg.CronDir())
	if err != nil {
		return err
	}
	sched = scheduler.New(jobStore, prov, store, standingMgr, cfg.Model, cfg.CronMaxConcurrentRuns)
	sched.Start(context.Background())
	slog.Info("cron scheduler started", "dir", cfg.CronDir(), "max_concurrent", cfg.CronMaxConcurrentRuns)

	hbConfigStore, err = heartbeat.NewConfigStore(cfg.CronDir())
	if err != nil {
		return err
	}
	if cfg.HeartbeatEnabled {
		hbRunner = heartbeat.New(prov, store, hbConfigStore, cfg.HeartbeatEvery, cfg.RequestTimeout, workspaceDir, standingMgr)
		slog.Info("heartbeat runner started", "default_every", cfg.HeartbeatEvery)
		peerIDs, err := hbConfigStore.ListPeerIDs()
		if err != nil {
			slog.Warn("heartbeat config scan failed", "error", err)
		} else {
			restored := 0
			for _, peerID := range peerIDs {
				cfg, err := hbConfigStore.GetOrDefault(peerID, cfg.HeartbeatEvery)
				if err != nil {
					slog.Warn("heartbeat config load failed during restore", "peer", peerID, "error", err)
					continue
				}
				if cfg.Enabled && cfg.Every > 0 {
					hbRunner.EnsureRunning(peerID)
					restored++
				}
			}
			if restored > 0 {
				slog.Info("heartbeat peers restored", "count", restored)
			}
		}
	}

	wsHandler := handler.NewHandler(store, prov, handler.HandlerOptions{
		Model:           cfg.Model,
		Timeout:         cfg.RequestTimeout,
		MemStore:        memStore,
		MemTopK:         cfg.MemoryTopK,
		MemInject:       cfg.MemoryInject,
		HBRunner:        hbRunner,
		HBConfigStore:   hbConfigStore,
		HBDefaultEvery:  cfg.HeartbeatEvery,
		StandingManager: standingMgr,
		AgentRuntime:    agentRuntime,
		AgentCoord:      agentCoord,
		SubRuntime:      subRuntime,
		JobStore:        jobStore,
		Sched:           sched,
		AllowedOrigins:  cfg.AllowedOrigins,
		ToolPolicy: handler.ToolPolicy{
			Profile: cfg.ToolProfile,
			Allow:   cfg.ToolsAllow,
			Deny:    cfg.ToolsDeny,
		},
		ExecConfig: handler.ExecConfig{
			Enabled:             cfg.ExecEnabled,
			EnableDenyPatterns:  cfg.ExecEnableDenyPatterns,
			CustomDenyPatterns:  compilePatterns(cfg.ExecCustomDenyPatterns),
			CustomAllowPatterns: compilePatterns(cfg.ExecCustomAllowPatterns),
			DefaultTimeout:      cfg.ExecDefaultTimeout,
			MaxTimeout:          cfg.ExecMaxTimeout,
			ApprovalMode:        cfg.ExecApprovalMode,
			ApprovalTTL:         cfg.ExecApprovalTTL,
			IsolationEnabled:    cfg.ExecIsolationEnabled,
			IsolationPaths:      isolationPaths(cfg.ExecIsolationPaths),
		},
		WorkspaceStore: wsStore,
		Hooks:          hooks,
		Presence:       presenceMgr,
		WorkspaceRoot:  cfg.WorkspaceRoot,
	})

	// Wire the full agent loop into heartbeat and cron so the LLM can invoke
	// tools (and spawn subagents) during scheduled and heartbeat turns.
	if hbRunner != nil {
		hbRunner.SetAgentRuntime(agentRuntime, wsHandler)
	}
	sched.SetAgentRuntime(agentRuntime, wsHandler)

	mux := http.NewServeMux()
	mux.Handle("GET /v1/ws", wsHandler)
	mux.Handle("POST /v1/webhooks/events", handler.NewWebhookHTTPHandler(store, hooks, presenceMgr, os.Getenv("KOIOS_WEBHOOK_TOKEN")))
	mux.HandleFunc("GET /", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(build)
	})
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	})

	srv := &http.Server{
		Addr:              cfg.ListenAddr,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      cfg.RequestTimeout + 15*time.Second,
		IdleTimeout:       120 * time.Second,
	}

	go func() {
		slog.Info("koios started",
			"addr", cfg.ListenAddr,
			"provider", cfg.Provider,
			"model", cfg.Model,
			"version", build.Version,
			"git_hash", build.GitHash,
			"build_time", build.BuildTime,
			"max_session_messages", cfg.MaxSessionMessages,
		)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			slog.Error("server error", "error", err)
			os.Exit(1)
		}
	}()
	if store.MaintenanceEnabled() {
		go func() {
			ticker := time.NewTicker(time.Minute)
			defer ticker.Stop()
			for range ticker.C {
				report := store.Maintain(time.Now())
				if report.DeletedExpired > 0 || report.DeletedEvicted > 0 || report.IdleResets > 0 || report.DailyResets > 0 {
					slog.Info("session maintenance",
						"deleted_expired", report.DeletedExpired,
						"deleted_evicted", report.DeletedEvicted,
						"idle_resets", report.IdleResets,
						"daily_resets", report.DailyResets,
					)
				}
			}
		}()
	}
	go func() {
		ticker := time.NewTicker(2 * time.Second)
		defer ticker.Stop()
		for now := range ticker.C {
			presenceMgr.ClearExpiredTyping(now)
		}
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	slog.Info("shutdown signal received, draining connections…")
	shutCtx, shutCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer shutCancel()
	if err := srv.Shutdown(shutCtx); err != nil {
		return err
	}
	wsHandler.Drain()
	if sched != nil {
		sched.Stop()
	}
	if agentCoord != nil {
		agentCoord.Stop()
	}
	if hbRunner != nil {
		hbRunner.Stop()
	}
	if memStore != nil {
		_ = memStore.Close()
	}
	if subRegistry != nil {
		_ = subRegistry.Sweep(0)
	}
	slog.Info("koios stopped")
	return nil
}

func setupLogger() {
	level := new(slog.LevelVar)
	level.Set(parseLogLevel(os.Getenv("KOIOS_LOG_LEVEL")))
	var output io.Writer = os.Stdout
	logFile := strings.TrimSpace(os.Getenv("KOIOS_LOG_FILE"))
	if logFile != "" {
		if err := os.MkdirAll(filepath.Dir(logFile), 0o700); err != nil {
			slog.Warn("logger: cannot create log dir", "file", logFile, "err", err)
		} else {
			fileWriter := &lumberjack.Logger{
				Filename:   logFile,
				MaxSize:    readEnvInt("KOIOS_LOG_MAX_SIZE_MB", 20),
				MaxBackups: readEnvInt("KOIOS_LOG_MAX_BACKUPS", 5),
				MaxAge:     readEnvInt("KOIOS_LOG_MAX_AGE_DAYS", 14),
				Compress:   readEnvBool("KOIOS_LOG_COMPRESS", true),
			}
			output = io.MultiWriter(os.Stdout, fileWriter)
		}
	}
	logger := slog.New(slog.NewJSONHandler(output, &slog.HandlerOptions{Level: level}))
	slog.SetDefault(logger)
}

func parseLogLevel(raw string) slog.Level {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "debug":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

func readEnvInt(key string, fallback int) int {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return fallback
	}
	v, err := strconv.Atoi(raw)
	if err != nil || v <= 0 {
		return fallback
	}
	return v
}

func readEnvDuration(key string, fallback time.Duration) time.Duration {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return fallback
	}
	v, err := time.ParseDuration(raw)
	if err != nil || v <= 0 {
		return fallback
	}
	return v
}

func readEnvBool(key string, fallback bool) bool {
	raw := strings.TrimSpace(strings.ToLower(os.Getenv(key)))
	if raw == "" {
		return fallback
	}
	switch raw {
	case "1", "true", "yes", "on":
		return true
	case "0", "false", "no", "off":
		return false
	default:
		return fallback
	}
}

func isolationPaths(in []config.ExecIsolationPath) []handler.ExecIsolationPath {
	if len(in) == 0 {
		return nil
	}
	out := make([]handler.ExecIsolationPath, len(in))
	for i, p := range in {
		out[i] = handler.ExecIsolationPath{
			Source: p.Source,
			Target: p.Target,
			Mode:   p.Mode,
		}
	}
	return out
}

// compilePatterns compiles a list of regex strings into []*regexp.Regexp,
// silently skipping any patterns that fail to compile and logging a warning.
func compilePatterns(patterns []string) []*regexp.Regexp {
	if len(patterns) == 0 {
		return nil
	}
	out := make([]*regexp.Regexp, 0, len(patterns))
	for _, p := range patterns {
		re, err := regexp.Compile(p)
		if err != nil {
			slog.Warn("exec: skipping invalid pattern", "pattern", p, "err", err)
			continue
		}
		out = append(out, re)
	}
	return out
}
