// koios is a daemon that exposes a single WebSocket JSON-RPC
// control-plane endpoint for all peer operations: chat, agent runs, subagent
// orchestration, long-term memory, cron scheduling, and heartbeat.
//
// Each connecting peer is identified by the peer_id query parameter on the
// WebSocket upgrade request.  The daemon maintains a completely isolated
// conversation history per peer.
//
// Configuration is entirely environment-driven — see internal/config for the
// full list of variables.
package main

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/ffimnsr/koios/internal/agent"
	"github.com/ffimnsr/koios/internal/config"
	"github.com/ffimnsr/koios/internal/handler"
	"github.com/ffimnsr/koios/internal/heartbeat"
	"github.com/ffimnsr/koios/internal/memory"
	"github.com/ffimnsr/koios/internal/provider"
	"github.com/ffimnsr/koios/internal/scheduler"
	"github.com/ffimnsr/koios/internal/session"
	"github.com/ffimnsr/koios/internal/standing"
	"github.com/ffimnsr/koios/internal/subagent"
)

var (
	version   = "dev"
	gitHash   = "unknown"
	buildTime = "unknown"
)

// init resolves version metadata at runtime when the binary was not built with
// ldflags (e.g. during `go run .`). Values injected by -ldflags take precedence.
func init() {
	if version == "dev" {
		if b, err := os.ReadFile("VERSION"); err == nil {
			if v := strings.TrimSpace(string(b)); v != "" {
				version = v
			}
		}
	}
	if gitHash == "unknown" {
		if out, err := exec.Command("git", "rev-parse", "--short", "HEAD").Output(); err == nil {
			if h := strings.TrimSpace(string(out)); h != "" {
				gitHash = h
			}
		}
	}
	if buildTime == "unknown" {
		buildTime = time.Now().UTC().Format(time.RFC3339)
	}
}

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(logger)

	cfg, err := config.Load()
	if err != nil {
		slog.Error("configuration error", "error", err)
		os.Exit(1)
	}
	workspaceDir, err := os.Getwd()
	if err != nil {
		slog.Error("resolve workspace dir", "error", err)
		os.Exit(1)
	}

	prov, err := provider.New(cfg)
	if err != nil {
		slog.Error("provider initialisation error", "error", err)
		os.Exit(1)
	}

	// ── Phase 1 + 2: session store with optional JSONL persistence and compaction ──
	storeOpts := session.Options{
		MaxMessages:      cfg.MaxSessionMessages,
		SessionDir:       cfg.SessionDir,
		CompactThreshold: cfg.CompactThreshold,
		CompactReserve:   cfg.CompactReserve,
	}
	if cfg.CompactThreshold > 0 {
		storeOpts.Compactor = session.NewLLMCompactor(prov, cfg.Model)
	}
	store := session.NewWithOptions(storeOpts)

	// ── Phase 3: long-term SQLite memory store ─────────────────────────────────
	var memStore *memory.Store
	if cfg.MemoryDBPath != "" {
		var embedder memory.Embedder
		if cfg.EmbedModel != "" {
			embedder = memory.NewOpenAIEmbedder(cfg.APIKey, cfg.BaseURL, cfg.EmbedModel)
		}
		memStore, err = memory.New(cfg.MemoryDBPath, embedder)
		if err != nil {
			slog.Error("memory store init failed", "error", err)
			os.Exit(1)
		}
		slog.Info("long-term memory enabled", "db", cfg.MemoryDBPath, "inject", cfg.MemoryInject)
	}

	// ── Phase 4: cron scheduler + heartbeat runner ────────────────────────────
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
	agentRuntime.EnableMemory(memStore, cfg.MemoryInject, cfg.MemoryTopK)
	agentRuntime.SetPruning(cfg.SessionPruneKeepToolMessages)
	agentRuntime.SetStandingOrders(standingMgr)
	agentCoord = agent.NewCoordinator(agentRuntime)
	subRegistry, err = subagent.NewRegistry(cfg.AgentDir)
	if err != nil {
		slog.Error("subagent registry init failed", "error", err)
		os.Exit(1)
	}
	subRuntime = subagent.NewRuntime(agentRuntime, store, subRegistry, cfg.AgentMaxChildren)
	if cfg.AgentDir != "" {
		slog.Info("subagent registry enabled", "dir", cfg.AgentDir, "max_children", cfg.AgentMaxChildren)
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
	if cfg.CronDir != "" {
		standingStore, err = standing.NewStore(cfg.CronDir)
		if err != nil {
			slog.Error("standing order store init failed", "error", err)
			os.Exit(1)
		}
		standingMgr = standing.NewManager(standingStore, workspaceDir)
		agentRuntime.SetStandingOrders(standingMgr)
		jobStore, err = scheduler.NewJobStore(cfg.CronDir)
		if err != nil {
			slog.Error("cron job store init failed", "error", err)
			os.Exit(1)
		}
		sched = scheduler.New(jobStore, prov, store, standingMgr, cfg.Model, cfg.CronMaxConcurrentRuns)
		sched.Start(context.Background())
		slog.Info("cron scheduler started", "dir", cfg.CronDir, "max_concurrent", cfg.CronMaxConcurrentRuns)

		hbConfigStore, err = heartbeat.NewConfigStore(cfg.CronDir)
		if err != nil {
			slog.Error("heartbeat config store init failed", "error", err)
			os.Exit(1)
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
	})

	mux := http.NewServeMux()
	// Single WebSocket control-plane entry point.
	mux.Handle("GET /v1/ws", wsHandler)
	mux.HandleFunc("GET /", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]string{
			"version":    version,
			"git_hash":   gitHash,
			"build_time": buildTime,
		})
	})
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	})

	srv := &http.Server{
		Addr:    cfg.ListenAddr,
		Handler: mux,
		// Separate timeouts: read headers quickly; allow long LLM streams on write.
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
			"version", version,
			"git_hash", gitHash,
			"build_time", buildTime,
			"max_session_messages", cfg.MaxSessionMessages,
		)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			slog.Error("server error", "error", err)
			os.Exit(1)
		}
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	slog.Info("shutdown signal received, draining connections…")
	shutCtx, shutCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer shutCancel()
	if err := srv.Shutdown(shutCtx); err != nil {
		slog.Error("graceful shutdown error", "error", err)
		os.Exit(1)
	}
	// Wait for any in-flight WebSocket RPC goroutines to finish.
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
}
