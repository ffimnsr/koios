// Package handler provides the WebSocket JSON-RPC control plane for
// koios.
//
// A single endpoint is exposed:
//
//	GET /v1/ws?peer_id=<id>  — upgrade to WebSocket; all operations are
//	                           performed via JSON-RPC frames on the connection.
//
// # Protocol
//
// Every frame is a JSON object.
//
// Client → Server (request):
//
//	{"id":"1","method":"chat","params":{...}}
//
// Server → Client (response — success):
//
//	{"id":"1","result":{...}}
//
// Server → Client (response — error):
//
//	{"id":"1","error":{"code":-32000,"message":"..."}}
//
// Server → Client (streaming notification — no id field):
//
//	{"method":"stream.delta","params":{"req_id":"1","content":"token"}}
//
// Server → Client (session append notification — no id field):
//
//	{"method":"session.message","params":{"peer_id":"alice","source":"cron","message":{"role":"system","content":"..."}}}
//
// When stream:true is set on chat or agent.run, delta notifications arrive
// before the final response.  The response itself signals completion.
//
// # Methods
//
//	ping
//	server.capabilities
//	chat
//	session.history
//	session.reset
//	standing.get / .set / .clear
//	agent.run / .start / .get / .wait / .cancel
//	subagent.list / .spawn / .get / .status / .kill / .steer / .transcript
//	memory.search / .insert / .list / .delete
//	cron.list / .create / .get / .update / .delete / .trigger / .runs
//	heartbeat.get / .set / .wake
package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/ffimnsr/koios/internal/agent"
	"github.com/ffimnsr/koios/internal/eventbus"
	"github.com/ffimnsr/koios/internal/heartbeat"
	"github.com/ffimnsr/koios/internal/mcp"
	"github.com/ffimnsr/koios/internal/memory"
	"github.com/ffimnsr/koios/internal/monitor"
	"github.com/ffimnsr/koios/internal/ops"
	"github.com/ffimnsr/koios/internal/presence"
	"github.com/ffimnsr/koios/internal/scheduler"
	"github.com/ffimnsr/koios/internal/session"
	"github.com/ffimnsr/koios/internal/standing"
	"github.com/ffimnsr/koios/internal/subagent"
	"github.com/ffimnsr/koios/internal/types"
	"github.com/ffimnsr/koios/internal/usage"
	"github.com/ffimnsr/koios/internal/workspace"
	"github.com/google/uuid"
	"github.com/gorilla/websocket"
)

// ── JSON-RPC frame types ──────────────────────────────────────────────────────

// rpcRequest is an inbound JSON-RPC call from the client.
type rpcRequest struct {
	ID     json.RawMessage `json:"id"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params,omitempty"`
}

// rpcResponse is an outbound JSON-RPC result or error frame.
type rpcResponse struct {
	ID     json.RawMessage `json:"id"`
	Result any             `json:"result,omitempty"`
	Error  *rpcError       `json:"error,omitempty"`
}

// rpcNotification is an outbound server-push frame (no id).
type rpcNotification struct {
	Method string `json:"method"`
	Params any    `json:"params"`
}

// rpcError is the error payload in a JSON-RPC error response.
type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// Standard JSON-RPC error codes.
const (
	errCodeParseError     = -32700
	errCodeInvalidRequest = -32600
	errCodeMethodNotFound = -32601
	errCodeInvalidParams  = -32602
	errCodeServer         = -32000
)

// ── WebSocket connection wrapper ──────────────────────────────────────────────

// wsConn wraps a gorilla WebSocket connection with a write mutex so that
// concurrent goroutines (e.g. concurrent streaming requests) can write safely.
type wsConn struct {
	mu     sync.Mutex
	conn   *websocket.Conn
	peerID string
}

func (c *wsConn) send(v any) {
	b, err := json.Marshal(v)
	if err != nil {
		slog.Error("ws: marshal", "peer", c.peerID, "error", err)
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.conn.WriteMessage(websocket.TextMessage, b); err != nil {
		// Connection likely closed; keep at debug level to avoid noise.
		slog.Debug("ws: write", "peer", c.peerID, "error", err)
	}
}

func (c *wsConn) reply(id json.RawMessage, result any) {
	c.send(rpcResponse{ID: id, Result: result})
}

func (c *wsConn) replyErr(id json.RawMessage, code int, msg string) {
	c.send(rpcResponse{ID: id, Error: &rpcError{Code: code, Message: msg}})
}

func (c *wsConn) notify(method string, params any) {
	c.send(rpcNotification{Method: method, Params: params})
}

func (h *Handler) addClient(wsc *wsConn) {
	if h == nil || wsc == nil {
		return
	}
	h.clientsMu.Lock()
	h.clients[wsc] = struct{}{}
	h.clientsMu.Unlock()
}

func (h *Handler) removeClient(wsc *wsConn) {
	if h == nil || wsc == nil {
		return
	}
	h.clientsMu.Lock()
	delete(h.clients, wsc)
	h.clientsMu.Unlock()
}

func (h *Handler) broadcast(method string, params any) {
	if h == nil {
		return
	}
	h.clientsMu.RLock()
	clients := make([]*wsConn, 0, len(h.clients))
	for c := range h.clients {
		clients = append(clients, c)
	}
	h.clientsMu.RUnlock()
	for _, c := range clients {
		c.notify(method, params)
	}
}

// ── Handler ───────────────────────────────────────────────────────────────────

var wsUpgrader = websocket.Upgrader{
	// Origin check is enforced per-request in ServeHTTP using h.allowedOrigins.
	CheckOrigin: func(_ *http.Request) bool { return true },
}

// Handler is the WebSocket JSON-RPC control-plane handler.
// One instance serves all peers; all peer-scoped state lives in the session
// store and the individual sub-system registries.
type Handler struct {
	store           *session.Store
	provider        llmProvider
	timeout         time.Duration
	model           string
	memStore        *memory.Store
	memTopK         int
	memInject       bool
	identityDir     string
	hbRunner        *heartbeat.Runner
	hbConfigStore   *heartbeat.ConfigStore
	hbDefaultEvery  time.Duration
	standingManager *standing.Manager
	agentRuntime    *agent.Runtime
	agentCoord      *agent.Coordinator
	subRuntime      *subagent.Runtime
	jobStore        *scheduler.JobStore
	sched           *scheduler.Scheduler
	workspaceStore  *workspace.Manager
	toolPolicy      ToolPolicy
	execConfig      ExecConfig
	execApprovals   *execApprovalStore
	allowedOrigins  []string // empty = allow all
	hooks           *ops.Manager
	presence        *presence.Manager
	messageBus      *eventbus.Bus
	usageStore      *usage.Store
	monitor         *monitor.Monitor
	logLevel        *slog.LevelVar
	mcpManager      *mcp.Manager

	// fetchClient is the HTTP client used by the web_fetch tool.  When nil,
	// a client backed by ssrfSafeTransport() is used.  Override in tests only.
	fetchClient *http.Client

	// dispatchWG tracks in-flight dispatch goroutines for graceful shutdown.
	dispatchWG sync.WaitGroup

	// syncRunsMu guards syncRuns, which maps a client-visible run ID to a
	// cancel function for in-flight synchronous agent.run calls.  This allows
	// agent.cancel to interrupt a synchronous run that has no async record in
	// the coordinator.
	syncRunsMu sync.Mutex
	syncRuns   map[string]context.CancelFunc
	clientsMu  sync.RWMutex
	clients    map[*wsConn]struct{}
}

// HandlerOptions holds all optional subsystem references.
type HandlerOptions struct {
	Model           string
	Timeout         time.Duration
	MemStore        *memory.Store
	MemTopK         int
	MemInject       bool
	HBRunner        *heartbeat.Runner
	HBConfigStore   *heartbeat.ConfigStore
	HBDefaultEvery  time.Duration
	StandingManager *standing.Manager
	AgentRuntime    *agent.Runtime
	AgentCoord      *agent.Coordinator
	SubRuntime      *subagent.Runtime
	JobStore        *scheduler.JobStore
	Sched           *scheduler.Scheduler
	WorkspaceStore  *workspace.Manager
	ToolPolicy      ToolPolicy
	ExecConfig      ExecConfig
	Hooks           *ops.Manager
	Presence        *presence.Manager
	MessageBus      *eventbus.Bus
	// AllowedOrigins, when non-empty, restricts WebSocket upgrades to requests
	// whose Origin header exactly matches one of the listed values
	// (case-insensitive).  An empty slice permits all origins.
	AllowedOrigins []string
	// WorkspaceRoot is the directory from which identity files (AGENTS.md,
	// SOUL.md, USER.md, IDENTITY.md) are read and injected into every system
	// prompt.
	WorkspaceRoot string
	// UsageStore, when non-nil, accumulates per-peer token usage across turns.
	UsageStore *usage.Store
	// Monitor, when non-nil, is notified of inbound requests so it can track
	// idle / stale state.
	Monitor *monitor.Monitor
	// LogLevel, when non-nil, is updated by the server.set_log_level RPC and
	// by hot-reload events.
	LogLevel *slog.LevelVar
	// MCPManager, when non-nil, provides tools from external MCP servers.
	MCPManager *mcp.Manager
}

// NewHandler creates the WebSocket control-plane handler.
func NewHandler(store *session.Store, prov llmProvider, opts HandlerOptions) *Handler {
	topK := opts.MemTopK
	if topK <= 0 {
		topK = 3
	}
	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = 2 * time.Minute
	}
	execCfg := normalizeExecConfig(opts.ExecConfig)
	h := &Handler{
		store:           store,
		provider:        prov,
		timeout:         timeout,
		model:           opts.Model,
		memStore:        opts.MemStore,
		memTopK:         topK,
		memInject:       opts.MemInject,
		identityDir:     opts.WorkspaceRoot,
		hbRunner:        opts.HBRunner,
		hbConfigStore:   opts.HBConfigStore,
		hbDefaultEvery:  opts.HBDefaultEvery,
		standingManager: opts.StandingManager,
		agentRuntime:    opts.AgentRuntime,
		agentCoord:      opts.AgentCoord,
		subRuntime:      opts.SubRuntime,
		jobStore:        opts.JobStore,
		sched:           opts.Sched,
		workspaceStore:  opts.WorkspaceStore,
		toolPolicy:      opts.ToolPolicy,
		execConfig:      execCfg,
		allowedOrigins:  opts.AllowedOrigins,
		hooks:           opts.Hooks,
		presence:        opts.Presence,
		messageBus:      opts.MessageBus,
		usageStore:      opts.UsageStore,
		monitor:         opts.Monitor,
		logLevel:        opts.LogLevel,
		mcpManager:      opts.MCPManager,
		syncRuns:        make(map[string]context.CancelFunc),
		clients:         make(map[*wsConn]struct{}),
		execApprovals:   newExecApprovalStore(execCfg.ApprovalTTL),
	}
	if h.presence != nil {
		h.presence.Subscribe(func(state presence.State) {
			h.broadcast("presence.update", state)
		})
	}
	return h
}

func (h *Handler) publishSessionMessage(sessionKey, source string, msg types.Message, data map[string]any) {
	if sessionKey == "" {
		return
	}
	if h.messageBus != nil {
		h.messageBus.Publish(eventbus.Event{
			Kind:       "session.message",
			SessionKey: sessionKey,
			PeerID:     sessionKey,
			Source:     source,
			Message:    &msg,
			Data:       data,
		})
		return
	}
	h.store.AppendWithSource(sessionKey, source, msg)
}

// ServeHTTP upgrades the connection to WebSocket and drives the per-peer
// read loop.  The peer ID is read from the peer_id query parameter.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "use GET to upgrade to WebSocket", http.StatusMethodNotAllowed)
		return
	}
	peerID := r.URL.Query().Get("peer_id")
	if peerID == "" {
		http.Error(w, "missing peer_id query parameter", http.StatusBadRequest)
		return
	}
	if !IsValidPeerID(peerID) {
		http.Error(w, "invalid peer_id: only alphanumeric and -_.:@ allowed (max 256)", http.StatusBadRequest)
		return
	}

	// Origin check: when AllowedOrigins is configured, reject any request whose
	// Origin header does not match.  This prevents Cross-Site WebSocket
	// Hijacking (CSWSH) when the gateway is reachable from browsers.
	if len(h.allowedOrigins) > 0 {
		origin := r.Header.Get("Origin")
		allowed := false
		for _, o := range h.allowedOrigins {
			if strings.EqualFold(origin, o) {
				allowed = true
				break
			}
		}
		if !allowed {
			http.Error(w, "origin not allowed", http.StatusForbidden)
			return
		}
	}

	conn, err := wsUpgrader.Upgrade(w, r, nil)
	if err != nil {
		slog.Error("ws: upgrade failed", "peer", peerID, "error", err)
		return
	}
	defer conn.Close()

	if h.monitor != nil {
		h.monitor.TouchActivity()
	}

	// Lazily start the peer's heartbeat goroutine on first connection.
	if h.hbRunner != nil {
		h.hbRunner.EnsureRunning(peerID)
	}

	slog.Info("ws: connected", "peer", peerID)
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	wsc := &wsConn{conn: conn, peerID: peerID}
	h.addClient(wsc)
	defer h.removeClient(wsc)
	if h.presence != nil {
		h.presence.Set(peerID, "online", false, "ws_connect")
		defer h.presence.Set(peerID, "offline", false, "ws_disconnect")
	}

	// Keepalive: set an initial read deadline and reset it on every pong.
	// If no pong arrives within the deadline the next ReadMessage call fails
	// and the read loop exits, cleaning up dead half-open connections.
	const (
		pingInterval = 30 * time.Second
		pongDeadline = 60 * time.Second
	)
	conn.SetReadDeadline(time.Now().Add(pongDeadline)) //nolint:errcheck
	conn.SetPongHandler(func(string) error {
		return conn.SetReadDeadline(time.Now().Add(pongDeadline))
	})
	go func() {
		ticker := time.NewTicker(pingInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				wsc.mu.Lock()
				err := conn.WriteControl(websocket.PingMessage, nil, time.Now().Add(10*time.Second))
				wsc.mu.Unlock()
				if err != nil {
					cancel()
					return
				}
			}
		}
	}()
	unsubscribe := h.store.Subscribe(peerID, func(ev session.AppendEvent) {
		if ev.Source == "" {
			return
		}
		for _, msg := range ev.Messages {
			wsc.notify("session.message", map[string]any{
				"peer_id": peerID,
				"source":  ev.Source,
				"message": msg,
			})
		}
	})
	defer unsubscribe()
	h.readLoop(ctx, wsc)
	slog.Info("ws: disconnected", "peer", peerID)
}

// readLoop reads inbound frames until the connection is closed.
func (h *Handler) readLoop(ctx context.Context, wsc *wsConn) {
	for {
		_, raw, err := wsc.conn.ReadMessage()
		if err != nil {
			if !websocket.IsCloseError(err,
				websocket.CloseNormalClosure,
				websocket.CloseGoingAway,
				websocket.CloseNoStatusReceived,
			) {
				slog.Debug("ws: read error", "peer", wsc.peerID, "error", err)
			}
			return
		}

		var req rpcRequest
		if err := json.Unmarshal(raw, &req); err != nil {
			wsc.replyErr(nil, errCodeParseError, "parse error: "+err.Error())
			continue
		}
		if req.Method == "" {
			wsc.replyErr(req.ID, errCodeInvalidRequest, "method is required")
			continue
		}
		if h.hooks != nil {
			ev, err := h.hooks.Intercept(ctx, ops.Event{
				Name:   ops.HookBeforeMessage,
				PeerID: wsc.peerID,
				Data: map[string]any{
					"method": req.Method,
					"params": json.RawMessage(req.Params),
					"has_id": len(req.ID) > 0,
				},
			})
			if err != nil {
				wsc.replyErr(req.ID, errCodeServer, "hook rejected request: "+err.Error())
				continue
			}
			req.Method = eventString(ev.Data, "method", req.Method)
			if params, ok := eventRawMessage(ev.Data, "params"); ok {
				req.Params = params
			}
		}
		if h.hooks != nil {
			if err := h.hooks.Emit(ctx, ops.Event{
				Name:   ops.HookMessageReceived,
				PeerID: wsc.peerID,
				Data: map[string]any{
					"method": req.Method,
					"has_id": len(req.ID) > 0,
				},
			}); err != nil {
				wsc.replyErr(req.ID, errCodeServer, "hook rejected request: "+err.Error())
				continue
			}
		}
		// Dispatch each call in its own goroutine so that long-running or
		// streaming requests do not stall inbound frame parsing.
		h.dispatchWG.Add(1)
		go func(r *rpcRequest) {
			defer h.dispatchWG.Done()
			h.dispatch(ctx, wsc, r)
		}(&req)
	}
}

// Drain waits for all in-flight dispatch goroutines to finish.  Call this
// after the HTTP server has stopped accepting new connections to ensure a
// clean shutdown without abandoning mid-flight agent runs.
func (h *Handler) Drain() {
	h.dispatchWG.Wait()
}

// dispatch routes a parsed RPC request to its handler method.
func (h *Handler) dispatch(ctx context.Context, wsc *wsConn, req *rpcRequest) {
	if h.hooks != nil {
		if err := h.hooks.Emit(ctx, ops.Event{
			Name:   ops.HookBeforeMessage,
			PeerID: wsc.peerID,
			Data: map[string]any{
				"method": req.Method,
				"has_id": len(req.ID) > 0,
			},
		}); err != nil {
			wsc.replyErr(req.ID, errCodeServer, "hook rejected request: "+err.Error())
			return
		}
		defer func() {
			_ = h.hooks.Emit(ctx, ops.Event{
				Name:   ops.HookAfterMessage,
				PeerID: wsc.peerID,
				Data: map[string]any{
					"method": req.Method,
					"has_id": len(req.ID) > 0,
				},
			})
		}()
	}
	switch req.Method {
	case "ping":
		wsc.reply(req.ID, map[string]bool{"pong": true})
	case "server.capabilities":
		wsc.reply(req.ID, h.serverCapabilities(wsc.peerID))

	// ── Chat ──────────────────────────────────────────────────────────────
	case "chat":
		h.rpcChat(ctx, wsc, req)
	case "session.history":
		h.rpcSessionHistory(ctx, wsc, req)
	case "session.reset":
		h.rpcSessionReset(ctx, wsc, req)
	case "presence.get":
		if h.presence == nil {
			wsc.replyErr(req.ID, errCodeServer, "presence is not enabled")
			return
		}
		h.rpcPresenceGet(wsc, req)
	case "presence.set":
		if h.presence == nil {
			wsc.replyErr(req.ID, errCodeServer, "presence is not enabled")
			return
		}
		h.rpcPresenceSet(wsc, req)
	case "standing.get":
		if h.standingManager == nil {
			wsc.replyErr(req.ID, errCodeServer, "standing orders are not enabled")
			return
		}
		h.rpcStandingGet(ctx, wsc, req)
	case "standing.set":
		if h.standingManager == nil {
			wsc.replyErr(req.ID, errCodeServer, "standing orders are not enabled")
			return
		}
		h.rpcStandingSet(ctx, wsc, req)
	case "standing.clear":
		if h.standingManager == nil {
			wsc.replyErr(req.ID, errCodeServer, "standing orders are not enabled")
			return
		}
		h.rpcStandingClear(ctx, wsc, req)

	// ── Agent run ─────────────────────────────────────────────────────────
	case "agent.run":
		h.rpcAgentRun(ctx, wsc, req)
	case "agent.start":
		h.rpcAgentStart(ctx, wsc, req)
	case "agent.get":
		h.rpcAgentGet(ctx, wsc, req)
	case "agent.wait":
		h.rpcAgentWait(ctx, wsc, req)
	case "agent.cancel":
		h.rpcAgentCancel(ctx, wsc, req)
	case "agent.steer":
		h.rpcAgentSteer(ctx, wsc, req)

	// ── Subagents ─────────────────────────────────────────────────────────
	case "subagent.list":
		h.rpcSubagentList(ctx, wsc, req)
	case "subagent.spawn":
		h.rpcSubagentSpawn(ctx, wsc, req)
	case "subagent.get":
		h.rpcSubagentGet(ctx, wsc, req)
	case "subagent.status":
		h.rpcSubagentGet(ctx, wsc, req)
	case "spawn.status":
		h.rpcSubagentGet(ctx, wsc, req)
	case "subagent.kill":
		h.rpcSubagentKill(ctx, wsc, req)
	case "subagent.steer":
		h.rpcSubagentSteer(ctx, wsc, req)
	case "subagent.transcript":
		h.rpcSubagentTranscript(ctx, wsc, req)

	// ── Memory ────────────────────────────────────────────────────────────
	case "memory.search":
		if h.memStore == nil {
			wsc.replyErr(req.ID, errCodeServer, "memory is not enabled")
			return
		}
		h.rpcMemorySearch(ctx, wsc, req)
	case "memory.insert":
		if h.memStore == nil {
			wsc.replyErr(req.ID, errCodeServer, "memory is not enabled")
			return
		}
		h.rpcMemoryInsert(ctx, wsc, req)
	case "memory.get":
		if h.memStore == nil {
			wsc.replyErr(req.ID, errCodeServer, "memory is not enabled")
			return
		}
		h.rpcMemoryGet(ctx, wsc, req)
	case "memory.list":
		if h.memStore == nil {
			wsc.replyErr(req.ID, errCodeServer, "memory is not enabled")
			return
		}
		h.rpcMemoryList(ctx, wsc, req)
	case "memory.delete":
		if h.memStore == nil {
			wsc.replyErr(req.ID, errCodeServer, "memory is not enabled")
			return
		}
		h.rpcMemoryDelete(ctx, wsc, req)

	// ── Workspace ─────────────────────────────────────────────────────────
	case "workspace.list":
		if h.workspaceStore == nil {
			wsc.replyErr(req.ID, errCodeServer, "workspace is not enabled")
			return
		}
		h.rpcWorkspaceList(ctx, wsc, req)
	case "workspace.read":
		if h.workspaceStore == nil {
			wsc.replyErr(req.ID, errCodeServer, "workspace is not enabled")
			return
		}
		h.rpcWorkspaceRead(ctx, wsc, req)
	case "workspace.write":
		if h.workspaceStore == nil {
			wsc.replyErr(req.ID, errCodeServer, "workspace is not enabled")
			return
		}
		h.rpcWorkspaceWrite(ctx, wsc, req)
	case "workspace.edit":
		if h.workspaceStore == nil {
			wsc.replyErr(req.ID, errCodeServer, "workspace is not enabled")
			return
		}
		h.rpcWorkspaceEdit(ctx, wsc, req)
	case "workspace.mkdir":
		if h.workspaceStore == nil {
			wsc.replyErr(req.ID, errCodeServer, "workspace is not enabled")
			return
		}
		h.rpcWorkspaceMkdir(ctx, wsc, req)
	case "workspace.delete":
		if h.workspaceStore == nil {
			wsc.replyErr(req.ID, errCodeServer, "workspace is not enabled")
			return
		}
		h.rpcWorkspaceDelete(ctx, wsc, req)
	case "exec":
		if h.workspaceStore == nil {
			wsc.replyErr(req.ID, errCodeServer, "exec is not enabled")
			return
		}
		h.rpcExec(ctx, wsc, req)
	case "exec.pending":
		h.rpcExecPending(ctx, wsc, req)
	case "exec.approve":
		h.rpcExecApprove(ctx, wsc, req)
	case "exec.reject":
		h.rpcExecReject(ctx, wsc, req)
	case "web_search":
		h.rpcWebSearch(ctx, wsc, req)
	case "web_fetch":
		h.rpcWebFetch(ctx, wsc, req)

	// ── Cron ──────────────────────────────────────────────────────────────
	case "cron.list":
		if h.jobStore == nil {
			wsc.replyErr(req.ID, errCodeServer, "cron is not enabled")
			return
		}
		h.rpcCronList(ctx, wsc, req)
	case "cron.create":
		if h.jobStore == nil || h.sched == nil {
			wsc.replyErr(req.ID, errCodeServer, "cron is not enabled")
			return
		}
		h.rpcCronCreate(ctx, wsc, req)
	case "cron.get":
		if h.jobStore == nil {
			wsc.replyErr(req.ID, errCodeServer, "cron is not enabled")
			return
		}
		h.rpcCronGet(ctx, wsc, req)
	case "cron.update":
		if h.jobStore == nil || h.sched == nil {
			wsc.replyErr(req.ID, errCodeServer, "cron is not enabled")
			return
		}
		h.rpcCronUpdate(ctx, wsc, req)
	case "cron.delete":
		if h.jobStore == nil {
			wsc.replyErr(req.ID, errCodeServer, "cron is not enabled")
			return
		}
		h.rpcCronDelete(ctx, wsc, req)
	case "cron.trigger":
		if h.jobStore == nil || h.sched == nil {
			wsc.replyErr(req.ID, errCodeServer, "cron is not enabled")
			return
		}
		h.rpcCronTrigger(ctx, wsc, req)
	case "cron.runs":
		if h.jobStore == nil {
			wsc.replyErr(req.ID, errCodeServer, "cron is not enabled")
			return
		}
		h.rpcCronRuns(ctx, wsc, req)

	// ── Heartbeat ─────────────────────────────────────────────────────────
	case "heartbeat.get":
		if h.hbRunner == nil || h.hbConfigStore == nil {
			wsc.replyErr(req.ID, errCodeServer, "heartbeat is not enabled")
			return
		}
		h.rpcHeartbeatGet(ctx, wsc, req)
	case "heartbeat.set":
		if h.hbRunner == nil || h.hbConfigStore == nil {
			wsc.replyErr(req.ID, errCodeServer, "heartbeat is not enabled")
			return
		}
		h.rpcHeartbeatSet(ctx, wsc, req)
	case "heartbeat.wake":
		if h.hbRunner == nil {
			wsc.replyErr(req.ID, errCodeServer, "heartbeat is not enabled")
			return
		}
		h.rpcHeartbeatWake(ctx, wsc, req)

	// ── Usage ─────────────────────────────────────────────────────────────
	case "usage.get":
		h.rpcUsageGet(wsc, req)
	case "usage.list":
		h.rpcUsageList(wsc, req)
	case "usage.totals":
		h.rpcUsageTotals(wsc, req)

	// ── Admin / hot-reload ────────────────────────────────────────────────
	case "server.set_log_level":
		h.rpcSetLogLevel(wsc, req)

	default:
		wsc.replyErr(req.ID, errCodeMethodNotFound, "unknown method: "+req.Method)
	}
}

func eventString(data map[string]any, key, fallback string) string {
	if data == nil {
		return fallback
	}
	value, ok := data[key]
	if !ok {
		return fallback
	}
	s, ok := value.(string)
	if !ok || strings.TrimSpace(s) == "" {
		return fallback
	}
	return s
}

func eventRawMessage(data map[string]any, key string) (json.RawMessage, bool) {
	if data == nil {
		return nil, false
	}
	value, ok := data[key]
	if !ok || value == nil {
		return nil, false
	}
	switch v := value.(type) {
	case json.RawMessage:
		return v, true
	case []byte:
		return json.RawMessage(v), true
	case string:
		return json.RawMessage(v), true
	default:
		buf, err := json.Marshal(v)
		if err != nil {
			return nil, false
		}
		return json.RawMessage(buf), true
	}
}

func (h *Handler) serverCapabilities(peerID string) map[string]any {
	caps := map[string]bool{
		"agent_runtime": h.agentRuntime != nil && h.agentCoord != nil,
		"memory":        h.memStore != nil,
		"cron":          h.jobStore != nil && h.sched != nil,
		"heartbeat":     h.hbRunner != nil && h.hbConfigStore != nil,
		"standing":      h.standingManager != nil,
		"subagents":     h.subRuntime != nil,
		"workspace":     h.workspaceStore != nil,
		"exec":          h.workspaceStore != nil,
		"presence":      h.presence != nil,
		"web":           true,
	}

	methods := []string{
		"ping",
		"server.capabilities",
		"chat",
		"session.history",
		"session.reset",
	}
	if caps["presence"] {
		methods = append(methods, "presence.get", "presence.set")
	}
	if caps["standing"] {
		methods = append(methods, "standing.get", "standing.set", "standing.clear")
	}
	if caps["agent_runtime"] {
		methods = append(methods,
			"agent.run",
			"agent.start",
			"agent.get",
			"agent.wait",
			"agent.cancel",
			"agent.steer",
		)
	}
	if caps["subagents"] {
		methods = append(methods,
			"subagent.list",
			"subagent.spawn",
			"subagent.get",
			"subagent.status",
			"subagent.kill",
			"subagent.steer",
			"subagent.transcript",
		)
	}
	if caps["memory"] {
		methods = append(methods, "memory.search", "memory.insert", "memory.get", "memory.list", "memory.delete")
	}
	if caps["workspace"] {
		methods = append(methods, "workspace.list", "workspace.read", "workspace.write", "workspace.edit", "workspace.mkdir", "workspace.delete")
	}
	if caps["exec"] {
		methods = append(methods, "exec", "exec.pending", "exec.approve", "exec.reject")
	}
	if caps["web"] {
		methods = append(methods, "web_search", "web_fetch")
	}
	if caps["cron"] {
		methods = append(methods,
			"cron.list",
			"cron.create",
			"cron.get",
			"cron.update",
			"cron.delete",
			"cron.trigger",
			"cron.runs",
		)
	}
	if caps["heartbeat"] {
		methods = append(methods, "heartbeat.get", "heartbeat.set", "heartbeat.wake")
	}
	methods = append(methods, "usage.get", "usage.list", "usage.totals", "server.set_log_level")

	tools := make([]string, 0, len(h.ToolDefinitions(peerID)))
	for _, tool := range h.ToolDefinitions(peerID) {
		if tool.Type == "function" && tool.Function.Name != "" {
			tools = append(tools, tool.Function.Name)
		}
	}

	streamNotifications := []string{}
	if caps["agent_runtime"] {
		streamNotifications = append(streamNotifications, "stream.delta", "stream.event")
	}
	if caps["presence"] {
		streamNotifications = append(streamNotifications, "presence.update")
	}

	return map[string]any{
		"peer_id":              peerID,
		"capabilities":         caps,
		"methods":              methods,
		"chat_tools":           tools,
		"stream_notifications": append(streamNotifications, "session.message"),
	}
}

type presenceGetParams struct {
	PeerID string `json:"peer_id,omitempty"`
}

func (h *Handler) rpcPresenceGet(wsc *wsConn, req *rpcRequest) {
	var p presenceGetParams
	if err := decodeParams(req.Params, &p); err != nil {
		wsc.replyErr(req.ID, errCodeInvalidParams, err.Error())
		return
	}
	if p.PeerID != "" {
		state, ok := h.presence.Get(p.PeerID)
		wsc.reply(req.ID, map[string]any{
			"peer_id": p.PeerID,
			"found":   ok,
			"state":   state,
		})
		return
	}
	wsc.reply(req.ID, map[string]any{"states": h.presence.Snapshot()})
}

type presenceSetParams struct {
	Status string `json:"status,omitempty"`
	Typing *bool  `json:"typing,omitempty"`
}

func (h *Handler) rpcPresenceSet(wsc *wsConn, req *rpcRequest) {
	var p presenceSetParams
	if err := decodeParams(req.Params, &p); err != nil {
		wsc.replyErr(req.ID, errCodeInvalidParams, err.Error())
		return
	}
	typing := false
	if p.Typing != nil {
		typing = *p.Typing
	}
	state := h.presence.Set(wsc.peerID, p.Status, typing, "rpc")
	wsc.reply(req.ID, map[string]any{"state": state})
}

// ── chat ──────────────────────────────────────────────────────────────────────

// FileAttachment is an image or file sent inline with a chat message.
// Data must be a base64-encoded payload; MimeType must be a valid MIME type.
type FileAttachment struct {
	Data     string `json:"data"`             // base64-encoded content
	MimeType string `json:"mime_type"`        // e.g. "image/jpeg"
	Name     string `json:"name,omitempty"`   // optional filename hint
	Detail   string `json:"detail,omitempty"` // OpenAI detail level: auto/low/high
}

type chatParams struct {
	Messages    []types.Message  `json:"messages"`
	Files       []FileAttachment `json:"files,omitempty"` // vision: attach images to the last user message
	Stream      bool             `json:"stream,omitempty"`
	MaxTokens   int              `json:"max_tokens,omitempty"`
	Temperature *float64         `json:"temperature,omitempty"`
	TopP        *float64         `json:"top_p,omitempty"`
}

func (h *Handler) rpcChat(ctx context.Context, wsc *wsConn, req *rpcRequest) {
	var p chatParams
	if err := decodeParams(req.Params, &p); err != nil {
		wsc.replyErr(req.ID, errCodeInvalidParams, err.Error())
		return
	}
	if len(p.Messages) == 0 {
		wsc.replyErr(req.ID, errCodeInvalidParams, "messages must not be empty")
		return
	}
	// Vision pipeline: attach files to the last user message as multipart content.
	if len(p.Files) > 0 {
		p.Messages = attachFilesToLastUserMessage(p.Messages, p.Files)
	}
	if h.agentRuntime == nil || h.agentCoord == nil {
		wsc.replyErr(req.ID, errCodeServer, "agent runtime is not enabled")
		return
	}
	if h.presence != nil {
		h.presence.Set(wsc.peerID, "online", false, "chat")
	}
	_, turnMessages := splitByRole(p.Messages)
	history := h.store.Get(wsc.peerID).History()
	reqCtx, cancel := context.WithTimeout(ctx, h.timeout)
	defer cancel()

	slog.Info("ws: chat", "peer", wsc.peerID, "turns", len(turnMessages), "history", len(history), "stream", p.Stream)
	var eventSink func(agent.Event)
	if p.Stream {
		eventSink = func(ev agent.Event) {
			wsc.notify("stream.event", map[string]any{
				"req_id": req.ID,
				"event":  ev,
			})
		}
	}
	runReq := agent.RunRequest{
		PeerID:       wsc.peerID,
		SessionKey:   wsc.peerID,
		Messages:     p.Messages,
		Stream:       p.Stream,
		MaxSteps:     toolLoopMaxSteps,
		MaxTokens:    p.MaxTokens,
		Temperature:  p.Temperature,
		TopP:         p.TopP,
		ToolExecutor: h,
		EventSink:    eventSink,
		Timeout:      h.timeout,
	}

	if p.Stream {
		sw := newWSStreamWriter(wsc, req.ID)
		result, err := h.agentCoord.RunStream(reqCtx, runReq, sw)
		if err != nil {
			slog.Error("ws: chat stream", "peer", wsc.peerID, "error", err)
			wsc.replyErr(req.ID, errCodeServer, "stream error: "+err.Error())
			return
		}
		if !sw.HasDeltas() && result.AssistantText != "" {
			wsc.notify("stream.delta", map[string]any{
				"req_id":  req.ID,
				"content": result.AssistantText,
			})
		}
		h.indexMemory(ctx, wsc.peerID, append(turnMessages, types.Message{Role: "assistant", Content: result.AssistantText}))
		if h.usageStore != nil {
			h.usageStore.Add(wsc.peerID, result.Usage)
		}
		wsc.reply(req.ID, map[string]any{"assistant_text": result.AssistantText, "usage": result.Usage, "done": true})
		return
	}

	result, err := h.agentCoord.Run(reqCtx, runReq)
	if err != nil {
		slog.Error("ws: chat", "peer", wsc.peerID, "error", err)
		wsc.replyErr(req.ID, errCodeServer, "upstream error: "+err.Error())
		return
	}
	h.indexMemory(ctx, wsc.peerID, append(turnMessages, types.Message{Role: "assistant", Content: result.AssistantText}))
	if h.usageStore != nil {
		if result.Response != nil {
			h.usageStore.Add(wsc.peerID, result.Response.Usage)
		}
	}
	wsc.reply(req.ID, result.Response)
}

// indexMemory inserts new conversation turns into the long-term memory store.
// Runs best-effort: errors are logged and discarded.
func (h *Handler) indexMemory(ctx context.Context, peerID string, msgs []types.Message) {
	if h.memStore == nil {
		return
	}
	for _, m := range msgs {
		if m.Content == "" {
			continue
		}
		if err := h.memStore.Insert(ctx, peerID, m.Content); err != nil {
			slog.Warn("ws: memory index", "peer", peerID, "err", err)
		}
	}
}

// attachFilesToLastUserMessage builds multipart content for the last user
// message in msgs by appending image_url parts for each FileAttachment. A copy
// of the slice is returned; the original is not mutated.
func attachFilesToLastUserMessage(msgs []types.Message, files []FileAttachment) []types.Message {
	if len(files) == 0 {
		return msgs
	}
	// Find the last user message.
	lastIdx := -1
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role == "user" {
			lastIdx = i
			break
		}
	}
	if lastIdx < 0 {
		return msgs
	}
	// Build content parts: text first, then images.
	out := make([]types.Message, len(msgs))
	copy(out, msgs)
	m := out[lastIdx]
	parts := make([]types.ContentPart, 0, 1+len(files))
	if m.Content != "" {
		parts = append(parts, types.ContentPart{Type: "text", Text: m.Content})
	}
	for _, f := range files {
		if f.Data == "" || f.MimeType == "" {
			continue
		}
		dataURI := "data:" + f.MimeType + ";base64," + f.Data
		detail := f.Detail
		if detail == "" {
			detail = "auto"
		}
		parts = append(parts, types.ContentPart{
			Type: "image_url",
			ImageURL: &types.ImageURLPart{
				URL:    dataURI,
				Detail: detail,
			},
		})
	}
	m.Parts = parts
	out[lastIdx] = m
	return out
}

func (h *Handler) rpcSessionReset(_ context.Context, wsc *wsConn, req *rpcRequest) {
	h.store.Reset(wsc.peerID)
	slog.Info("ws: session reset", "peer", wsc.peerID)
	wsc.reply(req.ID, map[string]bool{"ok": true})
}

func (h *Handler) rpcSessionHistory(_ context.Context, wsc *wsConn, req *rpcRequest) {
	var p struct {
		Limit      int    `json:"limit,omitempty"`
		SessionKey string `json:"session_key,omitempty"`
	}
	if err := decodeParams(req.Params, &p); err != nil {
		wsc.replyErr(req.ID, errCodeInvalidParams, err.Error())
		return
	}
	sessionKey := wsc.peerID
	if strings.TrimSpace(p.SessionKey) != "" {
		sessionKey = strings.TrimSpace(p.SessionKey)
	}
	history := h.store.Get(sessionKey).History()
	if p.Limit > 0 && len(history) > p.Limit {
		history = history[len(history)-p.Limit:]
	}
	wsc.reply(req.ID, map[string]any{
		"peer_id":     wsc.peerID,
		"session_key": sessionKey,
		"count":       len(history),
		"messages":    history,
	})
}

func (h *Handler) rpcStandingGet(_ context.Context, wsc *wsConn, req *rpcRequest) {
	workspace := ""
	peer := ""
	effective := ""
	if h.standingManager != nil {
		workspace = h.standingManager.WorkspaceContent()
		var err error
		peer, err = h.standingManager.PeerContent(wsc.peerID)
		if err != nil {
			slog.Error("ws: standing get", "peer", wsc.peerID, "error", err)
			wsc.replyErr(req.ID, errCodeServer, "error loading standing orders")
			return
		}
		effective, err = h.standingManager.EffectiveContent(wsc.peerID)
		if err != nil {
			slog.Error("ws: standing effective", "peer", wsc.peerID, "error", err)
			wsc.replyErr(req.ID, errCodeServer, "error loading standing orders")
			return
		}
	}
	wsc.reply(req.ID, map[string]any{
		"peer_id":           wsc.peerID,
		"workspace_content": workspace,
		"peer_content":      peer,
		"effective_content": effective,
		"writable":          h.standingManager != nil && h.standingManager.Writable(),
	})
}

func (h *Handler) rpcStandingSet(_ context.Context, wsc *wsConn, req *rpcRequest) {
	if h.standingManager == nil || !h.standingManager.Writable() {
		wsc.replyErr(req.ID, errCodeServer, "standing order persistence is not enabled")
		return
	}
	var p struct {
		Content string `json:"content"`
	}
	if err := decodeParams(req.Params, &p); err != nil {
		wsc.replyErr(req.ID, errCodeInvalidParams, err.Error())
		return
	}
	doc, err := h.standingManager.Store().Save(wsc.peerID, p.Content)
	if err != nil {
		slog.Error("ws: standing set", "peer", wsc.peerID, "error", err)
		wsc.replyErr(req.ID, errCodeServer, "error saving standing orders")
		return
	}
	wsc.reply(req.ID, doc)
}

func (h *Handler) rpcStandingClear(_ context.Context, wsc *wsConn, req *rpcRequest) {
	if h.standingManager == nil || !h.standingManager.Writable() {
		wsc.replyErr(req.ID, errCodeServer, "standing order persistence is not enabled")
		return
	}
	if err := h.standingManager.Store().Delete(wsc.peerID); err != nil {
		slog.Error("ws: standing clear", "peer", wsc.peerID, "error", err)
		wsc.replyErr(req.ID, errCodeServer, "error clearing standing orders")
		return
	}
	wsc.reply(req.ID, map[string]bool{"ok": true})
}

// ── agent.run ─────────────────────────────────────────────────────────────────

type agentRunParams struct {
	Messages   []types.Message `json:"messages"`
	Scope      string          `json:"scope,omitempty"`
	SenderID   string          `json:"sender_id,omitempty"`
	SessionKey string          `json:"session_key,omitempty"`
	Stream     bool            `json:"stream,omitempty"`
	MaxSteps   int             `json:"max_steps,omitempty"`
	Timeout    string          `json:"timeout,omitempty"` // Go duration string, e.g. "30s"
}

func (h *Handler) rpcAgentRun(ctx context.Context, wsc *wsConn, req *rpcRequest) {
	if h.agentRuntime == nil || h.agentCoord == nil {
		wsc.replyErr(req.ID, errCodeServer, "agent runtime is not enabled")
		return
	}
	var p agentRunParams
	if err := decodeParams(req.Params, &p); err != nil {
		wsc.replyErr(req.ID, errCodeInvalidParams, err.Error())
		return
	}
	runReq, err := h.buildAgentRunRequest(wsc, req.ID, p)
	if err != nil {
		return
	}

	// Register a cancellable context so the caller can interrupt this
	// synchronous run via agent.cancel using the returned run_id.
	runID := uuid.NewString()
	runCtx, runCancel := context.WithCancel(ctx)
	h.syncRunsMu.Lock()
	h.syncRuns[runID] = runCancel
	h.syncRunsMu.Unlock()
	defer func() {
		runCancel()
		h.syncRunsMu.Lock()
		delete(h.syncRuns, runID)
		h.syncRunsMu.Unlock()
	}()

	if p.Stream {
		sw := newWSStreamWriter(wsc, req.ID)
		result, err := h.agentCoord.RunStream(runCtx, runReq, sw)
		if err != nil {
			slog.Error("ws: agent run stream", "peer", wsc.peerID, "error", err)
			wsc.replyErr(req.ID, errCodeServer, "agent run failed: "+err.Error())
			return
		}
		if !sw.HasDeltas() && result.AssistantText != "" {
			wsc.notify("stream.delta", map[string]any{
				"req_id":  req.ID,
				"content": result.AssistantText,
			})
		}
		if h.usageStore != nil {
			h.usageStore.Add(wsc.peerID, result.Usage)
		}
		wsc.reply(req.ID, result)
		return
	}

	result, err := h.agentCoord.Run(runCtx, runReq)
	if err != nil {
		slog.Error("ws: agent run", "peer", wsc.peerID, "error", err)
		wsc.replyErr(req.ID, errCodeServer, "agent run failed: "+err.Error())
		return
	}
	if h.usageStore != nil {
		h.usageStore.Add(wsc.peerID, result.Usage)
	}
	// Include run_id so the caller can correlate with agent.cancel if needed.
	wsc.reply(req.ID, map[string]any{
		"run_id": runID,
		"result": result,
	})
}

func (h *Handler) rpcAgentStart(_ context.Context, wsc *wsConn, req *rpcRequest) {
	if h.agentRuntime == nil || h.agentCoord == nil {
		wsc.replyErr(req.ID, errCodeServer, "agent runtime is not enabled")
		return
	}
	var p agentRunParams
	if err := decodeParams(req.Params, &p); err != nil {
		wsc.replyErr(req.ID, errCodeInvalidParams, err.Error())
		return
	}
	runReq, err := h.buildAgentRunRequest(wsc, req.ID, p)
	if err != nil {
		return
	}
	if runReq.Stream {
		wsc.replyErr(req.ID, errCodeInvalidParams, "agent.start does not support stream=true")
		return
	}
	record, err := h.agentCoord.Start(runReq)
	if err != nil {
		wsc.replyErr(req.ID, errCodeServer, err.Error())
		return
	}
	wsc.reply(req.ID, record)
}

func (h *Handler) rpcAgentGet(_ context.Context, wsc *wsConn, req *rpcRequest) {
	if h.agentCoord == nil {
		wsc.replyErr(req.ID, errCodeServer, "agent runtime is not enabled")
		return
	}
	var p struct {
		ID string `json:"id"`
	}
	if err := decodeParams(req.Params, &p); err != nil {
		wsc.replyErr(req.ID, errCodeInvalidParams, err.Error())
		return
	}
	if p.ID == "" {
		wsc.replyErr(req.ID, errCodeInvalidParams, "id is required")
		return
	}
	record, ok := h.agentCoord.Get(p.ID)
	if !ok {
		wsc.replyErr(req.ID, errCodeServer, "run not found")
		return
	}
	wsc.reply(req.ID, record)
}

func (h *Handler) rpcAgentWait(ctx context.Context, wsc *wsConn, req *rpcRequest) {
	if h.agentCoord == nil {
		wsc.replyErr(req.ID, errCodeServer, "agent runtime is not enabled")
		return
	}
	var p struct {
		ID      string `json:"id"`
		Timeout string `json:"timeout,omitempty"`
	}
	if err := decodeParams(req.Params, &p); err != nil {
		wsc.replyErr(req.ID, errCodeInvalidParams, err.Error())
		return
	}
	if p.ID == "" {
		wsc.replyErr(req.ID, errCodeInvalidParams, "id is required")
		return
	}
	waitCtx := ctx
	if p.Timeout != "" {
		d, err := time.ParseDuration(p.Timeout)
		if err != nil {
			wsc.replyErr(req.ID, errCodeInvalidParams, "invalid timeout: "+err.Error())
			return
		}
		var cancel context.CancelFunc
		waitCtx, cancel = context.WithTimeout(ctx, d)
		defer cancel()
	}
	record, err := h.agentCoord.Wait(waitCtx, p.ID)
	if err != nil {
		wsc.replyErr(req.ID, errCodeServer, err.Error())
		return
	}
	wsc.reply(req.ID, record)
}

func (h *Handler) rpcAgentCancel(_ context.Context, wsc *wsConn, req *rpcRequest) {
	if h.agentCoord == nil {
		wsc.replyErr(req.ID, errCodeServer, "agent runtime is not enabled")
		return
	}
	var p struct {
		ID string `json:"id"`
	}
	if err := decodeParams(req.Params, &p); err != nil {
		wsc.replyErr(req.ID, errCodeInvalidParams, err.Error())
		return
	}
	if p.ID == "" {
		wsc.replyErr(req.ID, errCodeInvalidParams, "id is required")
		return
	}

	// Check synchronous runs first (registered by agent.run).
	h.syncRunsMu.Lock()
	syncCancel, isSyncRun := h.syncRuns[p.ID]
	if isSyncRun {
		delete(h.syncRuns, p.ID)
	}
	h.syncRunsMu.Unlock()
	if isSyncRun {
		syncCancel()
		wsc.reply(req.ID, map[string]any{"id": p.ID, "status": "canceled"})
		return
	}

	// Fall through to async runs managed by the coordinator.
	record, err := h.agentCoord.Cancel(p.ID)
	if err != nil {
		wsc.replyErr(req.ID, errCodeServer, err.Error())
		return
	}
	wsc.reply(req.ID, record)
}

func (h *Handler) rpcAgentSteer(_ context.Context, wsc *wsConn, req *rpcRequest) {
	if h.agentRuntime == nil {
		wsc.replyErr(req.ID, errCodeServer, "agent runtime is not enabled")
		return
	}
	var p struct {
		SessionKey string `json:"session_key"`
		Note       string `json:"note"`
	}
	if err := decodeParams(req.Params, &p); err != nil {
		wsc.replyErr(req.ID, errCodeInvalidParams, err.Error())
		return
	}
	if p.SessionKey == "" {
		wsc.replyErr(req.ID, errCodeInvalidParams, "session_key is required")
		return
	}
	if p.Note == "" {
		wsc.replyErr(req.ID, errCodeInvalidParams, "note is required")
		return
	}
	if err := h.agentRuntime.Steer(p.SessionKey, p.Note); err != nil {
		wsc.replyErr(req.ID, errCodeServer, err.Error())
		return
	}
	wsc.reply(req.ID, map[string]any{"session_key": p.SessionKey, "status": "queued"})
}

func (h *Handler) buildAgentRunRequest(wsc *wsConn, reqID json.RawMessage, p agentRunParams) (agent.RunRequest, error) {
	var timeout time.Duration
	if p.Timeout != "" {
		d, err := time.ParseDuration(p.Timeout)
		if err != nil {
			wsc.replyErr(reqID, errCodeInvalidParams, "invalid timeout: "+err.Error())
			return agent.RunRequest{}, err
		}
		timeout = d
	}

	scope := agent.SessionScope(p.Scope)
	switch scope {
	case agent.ScopeMain, agent.ScopeDirect, agent.ScopeIsolated, agent.ScopeGlobal, "":
	default:
		wsc.replyErr(reqID, errCodeInvalidParams, "invalid scope: must be main, direct, isolated, or global")
		return agent.RunRequest{}, fmt.Errorf("invalid scope")
	}

	return agent.RunRequest{
		PeerID:       wsc.peerID,
		SenderID:     p.SenderID,
		Scope:        scope,
		SessionKey:   p.SessionKey,
		Messages:     p.Messages,
		Stream:       p.Stream,
		MaxSteps:     p.MaxSteps,
		ToolExecutor: h,
		EventSink: func() func(agent.Event) {
			if !p.Stream {
				return nil
			}
			return func(ev agent.Event) {
				wsc.notify("stream.event", map[string]any{
					"req_id": reqID,
					"event":  ev,
				})
			}
		}(),
		Timeout: timeout,
	}, nil
}

// ── subagents ─────────────────────────────────────────────────────────────────

func (h *Handler) rpcSubagentList(_ context.Context, wsc *wsConn, req *rpcRequest) {
	all := h.subRuntime.List()
	out := make([]*subagent.RunRecord, 0, len(all))
	for _, rec := range all {
		if rec.PeerID == wsc.peerID {
			out = append(out, rec)
		}
	}
	wsc.reply(req.ID, out)
}

func (h *Handler) rpcSubagentSpawn(ctx context.Context, wsc *wsConn, req *rpcRequest) {
	var spawnReq subagent.SpawnRequest
	if err := decodeParams(req.Params, &spawnReq); err != nil {
		wsc.replyErr(req.ID, errCodeInvalidParams, err.Error())
		return
	}
	spawnReq.PeerID = wsc.peerID
	if spawnReq.ParentSessionKey == "" {
		spawnReq.ParentSessionKey = wsc.peerID
	}
	if spawnReq.SourceSessionKey == "" {
		spawnReq.SourceSessionKey = spawnReq.ParentSessionKey
	}
	rec, err := h.subRuntime.Spawn(ctx, spawnReq)
	if err != nil {
		slog.Error("ws: subagent spawn", "peer", wsc.peerID, "error", err)
		wsc.replyErr(req.ID, errCodeServer, err.Error())
		return
	}
	wsc.reply(req.ID, rec)
}

func (h *Handler) rpcSubagentGet(_ context.Context, wsc *wsConn, req *rpcRequest) {
	var p struct {
		ID string `json:"id"`
	}
	if err := decodeParams(req.Params, &p); err != nil {
		wsc.replyErr(req.ID, errCodeInvalidParams, err.Error())
		return
	}
	rec, ok := h.subRuntime.Get(p.ID)
	if !ok || rec.PeerID != wsc.peerID {
		wsc.replyErr(req.ID, errCodeServer, "run not found")
		return
	}
	wsc.reply(req.ID, rec)
}

func (h *Handler) rpcSubagentKill(_ context.Context, wsc *wsConn, req *rpcRequest) {
	var p struct {
		ID string `json:"id"`
	}
	if err := decodeParams(req.Params, &p); err != nil {
		wsc.replyErr(req.ID, errCodeInvalidParams, err.Error())
		return
	}
	rec, ok := h.subRuntime.Get(p.ID)
	if !ok || rec.PeerID != wsc.peerID {
		wsc.replyErr(req.ID, errCodeServer, "run not found")
		return
	}
	if err := h.subRuntime.Kill(p.ID); err != nil {
		wsc.replyErr(req.ID, errCodeServer, err.Error())
		return
	}
	wsc.reply(req.ID, map[string]bool{"ok": true})
}

func (h *Handler) rpcSubagentSteer(_ context.Context, wsc *wsConn, req *rpcRequest) {
	var p struct {
		ID   string `json:"id"`
		Note string `json:"note"`
	}
	if err := decodeParams(req.Params, &p); err != nil {
		wsc.replyErr(req.ID, errCodeInvalidParams, err.Error())
		return
	}
	rec, ok := h.subRuntime.Get(p.ID)
	if !ok || rec.PeerID != wsc.peerID {
		wsc.replyErr(req.ID, errCodeServer, "run not found")
		return
	}
	updated, err := h.subRuntime.Steer(p.ID, p.Note)
	if err != nil {
		wsc.replyErr(req.ID, errCodeServer, err.Error())
		return
	}
	wsc.reply(req.ID, updated)
}

func (h *Handler) rpcSubagentTranscript(_ context.Context, wsc *wsConn, req *rpcRequest) {
	var p struct {
		ID string `json:"id"`
	}
	if err := decodeParams(req.Params, &p); err != nil {
		wsc.replyErr(req.ID, errCodeInvalidParams, err.Error())
		return
	}
	rec, ok := h.subRuntime.Get(p.ID)
	if !ok || rec.PeerID != wsc.peerID {
		wsc.replyErr(req.ID, errCodeServer, "run not found")
		return
	}
	msgs, err := h.subRuntime.Transcript(p.ID)
	if err != nil {
		wsc.replyErr(req.ID, errCodeServer, err.Error())
		return
	}
	wsc.reply(req.ID, map[string]any{"messages": msgs, "count": len(msgs)})
}

// ── memory ────────────────────────────────────────────────────────────────────

func (h *Handler) rpcMemorySearch(ctx context.Context, wsc *wsConn, req *rpcRequest) {
	var p struct {
		Q     string `json:"q"`
		Limit int    `json:"limit,omitempty"`
	}
	if err := decodeParams(req.Params, &p); err != nil {
		wsc.replyErr(req.ID, errCodeInvalidParams, err.Error())
		return
	}
	if p.Q == "" {
		wsc.replyErr(req.ID, errCodeInvalidParams, "q is required")
		return
	}
	result, err := h.memorySearch(wsc.peerID, p.Q, p.Limit, ctx)
	if err != nil {
		slog.Error("ws: memory search", "peer", wsc.peerID, "error", err)
		wsc.replyErr(req.ID, errCodeServer, "search error: "+err.Error())
		return
	}
	wsc.reply(req.ID, result)
}

func (h *Handler) rpcMemoryInsert(ctx context.Context, wsc *wsConn, req *rpcRequest) {
	var p struct {
		Content string `json:"content"`
	}
	if err := decodeParams(req.Params, &p); err != nil {
		wsc.replyErr(req.ID, errCodeInvalidParams, err.Error())
		return
	}
	result, err := h.memoryInsert(wsc.peerID, p.Content, ctx)
	if err != nil {
		slog.Error("ws: memory insert", "peer", wsc.peerID, "error", err)
		wsc.replyErr(req.ID, errCodeServer, "insert error: "+err.Error())
		return
	}
	wsc.reply(req.ID, result)
}

func (h *Handler) rpcMemoryGet(ctx context.Context, wsc *wsConn, req *rpcRequest) {
	var p struct {
		ID string `json:"id"`
	}
	if err := decodeParams(req.Params, &p); err != nil {
		wsc.replyErr(req.ID, errCodeInvalidParams, err.Error())
		return
	}
	result, err := h.memoryGet(wsc.peerID, p.ID, ctx)
	if err != nil {
		slog.Error("ws: memory get", "peer", wsc.peerID, "error", err)
		wsc.replyErr(req.ID, errCodeServer, "get error: "+err.Error())
		return
	}
	wsc.reply(req.ID, result)
}

func (h *Handler) rpcMemoryList(ctx context.Context, wsc *wsConn, req *rpcRequest) {
	var p struct {
		Limit int `json:"limit,omitempty"`
	}
	if err := decodeParams(req.Params, &p); err != nil {
		wsc.replyErr(req.ID, errCodeInvalidParams, err.Error())
		return
	}
	if p.Limit <= 0 {
		p.Limit = 100
	}
	if p.Limit > 500 {
		p.Limit = 500
	}
	chunks, err := h.memStore.List(ctx, wsc.peerID, p.Limit)
	if err != nil {
		slog.Error("ws: memory list", "peer", wsc.peerID, "error", err)
		wsc.replyErr(req.ID, errCodeServer, "list error: "+err.Error())
		return
	}
	if chunks == nil {
		chunks = []memory.Chunk{}
	}
	wsc.reply(req.ID, map[string]any{"chunks": chunks})
}

func (h *Handler) rpcMemoryDelete(ctx context.Context, wsc *wsConn, req *rpcRequest) {
	var p struct {
		ID string `json:"id"`
	}
	if err := decodeParams(req.Params, &p); err != nil {
		wsc.replyErr(req.ID, errCodeInvalidParams, err.Error())
		return
	}
	if p.ID == "" {
		wsc.replyErr(req.ID, errCodeInvalidParams, "id is required")
		return
	}
	if err := h.memStore.DeleteChunk(ctx, wsc.peerID, p.ID); err != nil {
		slog.Error("ws: memory delete", "peer", wsc.peerID, "error", err)
		wsc.replyErr(req.ID, errCodeServer, "delete error: "+err.Error())
		return
	}
	wsc.reply(req.ID, map[string]bool{"ok": true})
}

// ── workspace ────────────────────────────────────────────────────────────────

func (h *Handler) rpcWorkspaceList(_ context.Context, wsc *wsConn, req *rpcRequest) {
	var p struct {
		Path      string `json:"path,omitempty"`
		Recursive bool   `json:"recursive,omitempty"`
		Limit     int    `json:"limit,omitempty"`
	}
	if err := decodeParams(req.Params, &p); err != nil {
		wsc.replyErr(req.ID, errCodeInvalidParams, err.Error())
		return
	}
	if strings.TrimSpace(p.Path) == "" {
		p.Path = "."
	}
	result, err := h.workspaceList(wsc.peerID, p.Path, p.Recursive, p.Limit)
	if err != nil {
		wsc.replyErr(req.ID, errCodeServer, err.Error())
		return
	}
	wsc.reply(req.ID, result)
}

func (h *Handler) rpcWorkspaceRead(_ context.Context, wsc *wsConn, req *rpcRequest) {
	var p struct {
		Path      string `json:"path"`
		StartLine int    `json:"start_line,omitempty"`
		EndLine   int    `json:"end_line,omitempty"`
	}
	if err := decodeParams(req.Params, &p); err != nil {
		wsc.replyErr(req.ID, errCodeInvalidParams, err.Error())
		return
	}
	if strings.TrimSpace(p.Path) == "" {
		wsc.replyErr(req.ID, errCodeInvalidParams, "path is required")
		return
	}
	result, err := h.workspaceRead(wsc.peerID, p.Path, p.StartLine, p.EndLine)
	if err != nil {
		wsc.replyErr(req.ID, errCodeServer, err.Error())
		return
	}
	wsc.reply(req.ID, result)
}

func (h *Handler) rpcWorkspaceWrite(_ context.Context, wsc *wsConn, req *rpcRequest) {
	var p struct {
		Path    string `json:"path"`
		Content string `json:"content"`
		Append  bool   `json:"append,omitempty"`
	}
	if err := decodeParams(req.Params, &p); err != nil {
		wsc.replyErr(req.ID, errCodeInvalidParams, err.Error())
		return
	}
	if strings.TrimSpace(p.Path) == "" {
		wsc.replyErr(req.ID, errCodeInvalidParams, "path is required")
		return
	}
	result, err := h.workspaceWrite(wsc.peerID, p.Path, p.Content, p.Append)
	if err != nil {
		wsc.replyErr(req.ID, errCodeServer, err.Error())
		return
	}
	wsc.reply(req.ID, result)
}

func (h *Handler) rpcWorkspaceEdit(_ context.Context, wsc *wsConn, req *rpcRequest) {
	var p struct {
		Path       string `json:"path"`
		OldText    string `json:"old_text"`
		NewText    string `json:"new_text"`
		ReplaceAll bool   `json:"replace_all,omitempty"`
	}
	if err := decodeParams(req.Params, &p); err != nil {
		wsc.replyErr(req.ID, errCodeInvalidParams, err.Error())
		return
	}
	result, err := h.workspaceEdit(wsc.peerID, p.Path, p.OldText, p.NewText, p.ReplaceAll)
	if err != nil {
		wsc.replyErr(req.ID, errCodeServer, err.Error())
		return
	}
	wsc.reply(req.ID, result)
}

func (h *Handler) rpcWorkspaceMkdir(_ context.Context, wsc *wsConn, req *rpcRequest) {
	var p struct {
		Path string `json:"path"`
	}
	if err := decodeParams(req.Params, &p); err != nil {
		wsc.replyErr(req.ID, errCodeInvalidParams, err.Error())
		return
	}
	if strings.TrimSpace(p.Path) == "" {
		wsc.replyErr(req.ID, errCodeInvalidParams, "path is required")
		return
	}
	result, err := h.workspaceMkdir(wsc.peerID, p.Path)
	if err != nil {
		wsc.replyErr(req.ID, errCodeServer, err.Error())
		return
	}
	wsc.reply(req.ID, result)
}

func (h *Handler) rpcWorkspaceDelete(_ context.Context, wsc *wsConn, req *rpcRequest) {
	var p struct {
		Path      string `json:"path"`
		Recursive bool   `json:"recursive,omitempty"`
	}
	if err := decodeParams(req.Params, &p); err != nil {
		wsc.replyErr(req.ID, errCodeInvalidParams, err.Error())
		return
	}
	if strings.TrimSpace(p.Path) == "" {
		wsc.replyErr(req.ID, errCodeInvalidParams, "path is required")
		return
	}
	result, err := h.workspaceDelete(wsc.peerID, p.Path, p.Recursive)
	if err != nil {
		wsc.replyErr(req.ID, errCodeServer, err.Error())
		return
	}
	wsc.reply(req.ID, result)
}

// ── cron ──────────────────────────────────────────────────────────────────────

func (h *Handler) rpcCronList(_ context.Context, wsc *wsConn, req *rpcRequest) {
	jobs := h.jobStore.List(wsc.peerID)
	if jobs == nil {
		jobs = []*scheduler.Job{}
	}
	wsc.reply(req.ID, jobs)
}

type cronCreateParams struct {
	Name           string                   `json:"name"`
	Description    string                   `json:"description,omitempty"`
	Schedule       scheduler.Schedule       `json:"schedule"`
	Payload        scheduler.Payload        `json:"payload"`
	Dispatch       scheduler.DispatchPolicy `json:"dispatch,omitempty"`
	Enabled        *bool                    `json:"enabled,omitempty"`
	DeleteAfterRun *bool                    `json:"delete_after_run,omitempty"`
}

func (h *Handler) rpcCronCreate(_ context.Context, wsc *wsConn, req *rpcRequest) {
	var p cronCreateParams
	if err := decodeParams(req.Params, &p); err != nil {
		wsc.replyErr(req.ID, errCodeInvalidParams, err.Error())
		return
	}
	if strings.TrimSpace(p.Name) == "" {
		wsc.replyErr(req.ID, errCodeInvalidParams, "name is required")
		return
	}
	if err := validateSchedule(p.Schedule); err != nil {
		wsc.replyErr(req.ID, errCodeInvalidParams, "invalid schedule: "+err.Error())
		return
	}
	if err := validatePayload(p.Payload); err != nil {
		wsc.replyErr(req.ID, errCodeInvalidParams, "invalid payload: "+err.Error())
		return
	}

	enabled := true
	if p.Enabled != nil {
		enabled = *p.Enabled
	}
	deleteAfterRun := p.Schedule.Kind == scheduler.KindAt
	if p.DeleteAfterRun != nil {
		deleteAfterRun = *p.DeleteAfterRun
	}

	job := &scheduler.Job{
		PeerID:         wsc.peerID,
		Name:           p.Name,
		Description:    p.Description,
		Schedule:       p.Schedule,
		Payload:        p.Payload,
		Dispatch:       p.Dispatch,
		Enabled:        enabled,
		DeleteAfterRun: deleteAfterRun,
	}
	nextRun, err := scheduler.CalcInitialNextRun(job, h.sched.CronParser())
	if err != nil {
		wsc.replyErr(req.ID, errCodeInvalidParams, "schedule cannot compute next run: "+err.Error())
		return
	}
	job.NextRunAt = nextRun
	if err := h.jobStore.Add(job); err != nil {
		slog.Error("ws: cron add", "peer", wsc.peerID, "error", err)
		wsc.replyErr(req.ID, errCodeServer, "error saving job")
		return
	}
	slog.Info("ws: cron created", "peer", wsc.peerID, "job", job.JobID)
	wsc.reply(req.ID, job)
}

func (h *Handler) rpcCronGet(_ context.Context, wsc *wsConn, req *rpcRequest) {
	var p struct {
		ID string `json:"id"`
	}
	if err := decodeParams(req.Params, &p); err != nil {
		wsc.replyErr(req.ID, errCodeInvalidParams, err.Error())
		return
	}
	job := h.ownedJob(wsc, req.ID, p.ID)
	if job == nil {
		return
	}
	wsc.reply(req.ID, job)
}

type cronUpdateParams struct {
	ID             string                    `json:"id"`
	Name           *string                   `json:"name,omitempty"`
	Description    *string                   `json:"description,omitempty"`
	Enabled        *bool                     `json:"enabled,omitempty"`
	Schedule       *scheduler.Schedule       `json:"schedule,omitempty"`
	Payload        *scheduler.Payload        `json:"payload,omitempty"`
	Dispatch       *scheduler.DispatchPolicy `json:"dispatch,omitempty"`
	DeleteAfterRun *bool                     `json:"delete_after_run,omitempty"`
}

func (h *Handler) rpcCronUpdate(_ context.Context, wsc *wsConn, req *rpcRequest) {
	var p cronUpdateParams
	if err := decodeParams(req.Params, &p); err != nil {
		wsc.replyErr(req.ID, errCodeInvalidParams, err.Error())
		return
	}
	job := h.ownedJob(wsc, req.ID, p.ID)
	if job == nil {
		return
	}
	if p.Name != nil {
		if strings.TrimSpace(*p.Name) == "" {
			wsc.replyErr(req.ID, errCodeInvalidParams, "name must not be empty")
			return
		}
		job.Name = *p.Name
	}
	if p.Description != nil {
		job.Description = *p.Description
	}
	if p.Enabled != nil {
		job.Enabled = *p.Enabled
	}
	if p.DeleteAfterRun != nil {
		job.DeleteAfterRun = *p.DeleteAfterRun
	}
	if p.Schedule != nil {
		if err := validateSchedule(*p.Schedule); err != nil {
			wsc.replyErr(req.ID, errCodeInvalidParams, "invalid schedule: "+err.Error())
			return
		}
		job.Schedule = *p.Schedule
		next, err := scheduler.CalcInitialNextRun(job, h.sched.CronParser())
		if err != nil {
			wsc.replyErr(req.ID, errCodeInvalidParams, "schedule cannot compute next run: "+err.Error())
			return
		}
		job.NextRunAt = next
		job.ConsecErrors = 0
	}
	if p.Payload != nil {
		if err := validatePayload(*p.Payload); err != nil {
			wsc.replyErr(req.ID, errCodeInvalidParams, "invalid payload: "+err.Error())
			return
		}
		job.Payload = *p.Payload
	}
	if p.Dispatch != nil {
		job.Dispatch = *p.Dispatch
	}
	if err := h.jobStore.Update(job); err != nil {
		slog.Error("ws: cron update", "job", p.ID, "error", err)
		wsc.replyErr(req.ID, errCodeServer, "error updating job")
		return
	}
	slog.Info("ws: cron updated", "peer", wsc.peerID, "job", p.ID)
	wsc.reply(req.ID, job)
}

func (h *Handler) rpcCronDelete(_ context.Context, wsc *wsConn, req *rpcRequest) {
	var p struct {
		ID string `json:"id"`
	}
	if err := decodeParams(req.Params, &p); err != nil {
		wsc.replyErr(req.ID, errCodeInvalidParams, err.Error())
		return
	}
	if h.ownedJob(wsc, req.ID, p.ID) == nil {
		return
	}
	if err := h.jobStore.Remove(p.ID); err != nil {
		slog.Error("ws: cron delete", "job", p.ID, "error", err)
		wsc.replyErr(req.ID, errCodeServer, "error removing job")
		return
	}
	slog.Info("ws: cron deleted", "peer", wsc.peerID, "job", p.ID)
	wsc.reply(req.ID, map[string]bool{"ok": true})
}

func (h *Handler) rpcCronTrigger(_ context.Context, wsc *wsConn, req *rpcRequest) {
	var p struct {
		ID string `json:"id"`
	}
	if err := decodeParams(req.Params, &p); err != nil {
		wsc.replyErr(req.ID, errCodeInvalidParams, err.Error())
		return
	}
	if h.ownedJob(wsc, req.ID, p.ID) == nil {
		return
	}
	runID, err := h.sched.TriggerRun(p.ID)
	if err != nil {
		slog.Error("ws: cron trigger", "job", p.ID, "error", err)
		wsc.replyErr(req.ID, errCodeServer, "trigger failed: "+err.Error())
		return
	}
	wsc.reply(req.ID, map[string]any{"ok": true, "run_id": runID})
}

func (h *Handler) rpcCronRuns(_ context.Context, wsc *wsConn, req *rpcRequest) {
	var p struct {
		ID    string `json:"id"`
		Limit int    `json:"limit,omitempty"`
	}
	if err := decodeParams(req.Params, &p); err != nil {
		wsc.replyErr(req.ID, errCodeInvalidParams, err.Error())
		return
	}
	if h.ownedJob(wsc, req.ID, p.ID) == nil {
		return
	}
	limit := p.Limit
	if limit <= 0 {
		limit = 50
	}
	if limit > 1000 {
		limit = 1000
	}
	records, err := h.jobStore.ReadRunRecords(p.ID, limit)
	if err != nil {
		slog.Error("ws: cron runs", "job", p.ID, "error", err)
		wsc.replyErr(req.ID, errCodeServer, "error reading run log")
		return
	}
	if records == nil {
		records = []scheduler.RunRecord{}
	}
	wsc.reply(req.ID, records)
}

// ownedJob fetches a job and verifies it belongs to the requesting peer.
// Returns nil and sends an error response when the job is absent or unowned.
// Returns a generic "not found" to avoid leaking other peers' job IDs.
func (h *Handler) ownedJob(wsc *wsConn, reqID json.RawMessage, jobID string) *scheduler.Job {
	if jobID == "" {
		wsc.replyErr(reqID, errCodeInvalidParams, "id is required")
		return nil
	}
	job := h.jobStore.Get(jobID)
	if job == nil || job.PeerID != wsc.peerID {
		wsc.replyErr(reqID, errCodeServer, "job not found")
		return nil
	}
	return job
}

// ── heartbeat ─────────────────────────────────────────────────────────────────

func (h *Handler) rpcHeartbeatGet(_ context.Context, wsc *wsConn, req *rpcRequest) {
	cfg, err := h.hbConfigStore.GetOrDefault(wsc.peerID, h.hbDefaultEvery)
	if err != nil {
		slog.Error("ws: heartbeat get", "peer", wsc.peerID, "error", err)
		wsc.replyErr(req.ID, errCodeServer, "error loading config")
		return
	}
	wsc.reply(req.ID, cfg)
}

type heartbeatSetParams struct {
	Enabled     *bool                  `json:"enabled,omitempty"`
	Every       *string                `json:"every,omitempty"` // Go duration string, e.g. "30m"
	Prompt      *string                `json:"prompt,omitempty"`
	AckMaxChars *int                   `json:"ack_max_chars,omitempty"`
	ActiveHours *heartbeat.ActiveHours `json:"active_hours,omitempty"`
}

func (h *Handler) rpcHeartbeatSet(_ context.Context, wsc *wsConn, req *rpcRequest) {
	current, err := h.hbConfigStore.GetOrDefault(wsc.peerID, h.hbDefaultEvery)
	if err != nil {
		slog.Error("ws: heartbeat set load", "peer", wsc.peerID, "error", err)
		wsc.replyErr(req.ID, errCodeServer, "error loading config")
		return
	}
	var p heartbeatSetParams
	if err := decodeParams(req.Params, &p); err != nil {
		wsc.replyErr(req.ID, errCodeInvalidParams, err.Error())
		return
	}
	if p.Enabled != nil {
		current.Enabled = *p.Enabled
	}
	if p.Every != nil {
		d, err := time.ParseDuration(*p.Every)
		if err != nil {
			wsc.replyErr(req.ID, errCodeInvalidParams, "invalid every: must be a Go duration (e.g. 30m, 1h)")
			return
		}
		if d < 0 {
			wsc.replyErr(req.ID, errCodeInvalidParams, "every must be non-negative")
			return
		}
		current.Every = d
	}
	if p.Prompt != nil {
		current.Prompt = *p.Prompt
	}
	if p.AckMaxChars != nil {
		if *p.AckMaxChars < 0 {
			wsc.replyErr(req.ID, errCodeInvalidParams, "ack_max_chars must be >= 0")
			return
		}
		current.AckMaxChars = *p.AckMaxChars
	}
	if p.ActiveHours != nil {
		current.ActiveHours = p.ActiveHours
	}
	if err := h.hbRunner.SetConfig(wsc.peerID, current); err != nil {
		slog.Error("ws: heartbeat set save", "peer", wsc.peerID, "error", err)
		wsc.replyErr(req.ID, errCodeServer, "error saving config")
		return
	}
	slog.Info("ws: heartbeat config updated", "peer", wsc.peerID, "enabled", current.Enabled, "every", current.Every)
	wsc.reply(req.ID, current)
}

func (h *Handler) rpcHeartbeatWake(_ context.Context, wsc *wsConn, req *rpcRequest) {
	h.hbRunner.WakePeer(wsc.peerID)
	slog.Info("ws: heartbeat wake", "peer", wsc.peerID)
	wsc.reply(req.ID, map[string]bool{"ok": true})
}

// ── wsStreamWriter ────────────────────────────────────────────────────────────

// wsStreamWriter implements http.ResponseWriter and http.Flusher so that the
// provider's CompleteStream can write SSE bytes to it.  Each complete SSE data
// line is forwarded as a stream.delta WebSocket notification.
type wsStreamWriter struct {
	wsc    *wsConn
	reqID  json.RawMessage
	mu     sync.Mutex
	buf    bytes.Buffer
	full   strings.Builder
	deltas int
	hdr    http.Header
}

func newWSStreamWriter(wsc *wsConn, reqID json.RawMessage) *wsStreamWriter {
	return &wsStreamWriter{wsc: wsc, reqID: reqID, hdr: make(http.Header)}
}

func (w *wsStreamWriter) Header() http.Header { return w.hdr }
func (w *wsStreamWriter) WriteHeader(_ int)   {}
func (w *wsStreamWriter) Flush()              { w.processLines() }

func (w *wsStreamWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	w.buf.Write(p)
	w.mu.Unlock()
	w.processLines()
	return len(p), nil
}

func (w *wsStreamWriter) processLines() {
	w.mu.Lock()
	defer w.mu.Unlock()
	for {
		data := w.buf.Bytes()
		idx := bytes.IndexByte(data, '\n')
		if idx < 0 {
			return
		}
		line := string(bytes.TrimRight(data[:idx], "\r"))
		w.buf.Next(idx + 1)

		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		payload := line[6:]
		if payload == "[DONE]" {
			continue
		}
		var chunk types.StreamChunk
		if err := json.Unmarshal([]byte(payload), &chunk); err != nil {
			continue
		}
		if len(chunk.Choices) == 0 {
			continue
		}
		content := chunk.Choices[0].Delta.Content
		if content == "" {
			continue
		}
		w.full.WriteString(content)
		w.deltas++
		w.wsc.notify("stream.delta", map[string]any{
			"req_id":  w.reqID,
			"content": content,
		})
	}
}

// AssistantText returns the full assistant text accumulated from stream deltas.
func (w *wsStreamWriter) AssistantText() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.full.String()
}

func (w *wsStreamWriter) HasDeltas() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.deltas > 0
}

// ── cron validation helpers ───────────────────────────────────────────────────

func validateSchedule(s scheduler.Schedule) error {
	switch s.Kind {
	case scheduler.KindAt:
		if s.At == "" {
			return fmt.Errorf("schedule.at is required for kind=at")
		}
		if _, err := time.Parse(time.RFC3339, s.At); err != nil {
			if _, err2 := time.Parse("2006-01-02T15:04:05", s.At); err2 != nil {
				return fmt.Errorf("schedule.at %q is not a valid ISO 8601 timestamp", s.At)
			}
		}
	case scheduler.KindEvery:
		if s.EveryMs <= 0 {
			return fmt.Errorf("schedule.every_ms must be > 0")
		}
	case scheduler.KindCron:
		if s.Expr == "" {
			return fmt.Errorf("schedule.expr is required for kind=cron")
		}
		if s.Tz != "" {
			if _, err := time.LoadLocation(s.Tz); err != nil {
				return fmt.Errorf("schedule.tz %q is not a valid IANA timezone", s.Tz)
			}
		}
	default:
		return fmt.Errorf("schedule.kind must be one of: at, every, cron")
	}
	return nil
}

func validatePayload(p scheduler.Payload) error {
	switch p.Kind {
	case scheduler.PayloadSystemEvent:
		if strings.TrimSpace(p.Text) == "" {
			return fmt.Errorf("payload.text is required for kind=systemEvent")
		}
	case scheduler.PayloadAgentTurn:
		if strings.TrimSpace(p.Message) == "" {
			return fmt.Errorf("payload.message is required for kind=agentTurn")
		}
	default:
		return fmt.Errorf("payload.kind must be one of: systemEvent, agentTurn")
	}
	return nil
}

// ── misc helpers ──────────────────────────────────────────────────────────────

// decodeParams unmarshals the params JSON into dst. A nil or null params is
// treated as an empty object so methods with all-optional fields work correctly.
func decodeParams(params json.RawMessage, dst any) error {
	if len(params) == 0 || string(params) == "null" {
		return nil
	}
	if err := json.Unmarshal(params, dst); err != nil {
		return fmt.Errorf("invalid params: %w", err)
	}
	return nil
}

// ── usage RPCs ────────────────────────────────────────────────────────────────

func (h *Handler) rpcUsageGet(wsc *wsConn, req *rpcRequest) {
	var p struct {
		PeerID string `json:"peer_id"`
	}
	if err := decodeParams(req.Params, &p); err != nil {
		wsc.replyErr(req.ID, errCodeInvalidParams, err.Error())
		return
	}
	peerID := strings.TrimSpace(p.PeerID)
	if peerID == "" {
		peerID = wsc.peerID
	}
	if h.usageStore == nil {
		wsc.reply(req.ID, map[string]any{"peer_id": peerID, "found": false})
		return
	}
	u, ok := h.usageStore.Get(peerID)
	wsc.reply(req.ID, map[string]any{"peer_id": peerID, "found": ok, "usage": u})
}

func (h *Handler) rpcUsageList(wsc *wsConn, req *rpcRequest) {
	if h.usageStore == nil {
		wsc.reply(req.ID, map[string]any{"sessions": []any{}, "count": 0})
		return
	}
	all := h.usageStore.All()
	wsc.reply(req.ID, map[string]any{"sessions": all, "count": len(all)})
}

func (h *Handler) rpcUsageTotals(wsc *wsConn, req *rpcRequest) {
	if h.usageStore == nil {
		wsc.reply(req.ID, map[string]any{"totals": nil})
		return
	}
	wsc.reply(req.ID, map[string]any{"totals": h.usageStore.Totals()})
}

// ── admin ─────────────────────────────────────────────────────────────────────

// rpcSetLogLevel changes the gateway log level at runtime.
func (h *Handler) rpcSetLogLevel(wsc *wsConn, req *rpcRequest) {
	var p struct {
		Level string `json:"level"`
	}
	if err := decodeParams(req.Params, &p); err != nil {
		wsc.replyErr(req.ID, errCodeInvalidParams, err.Error())
		return
	}
	if strings.TrimSpace(p.Level) == "" {
		wsc.replyErr(req.ID, errCodeInvalidParams, "level is required")
		return
	}
	if h.logLevel == nil {
		wsc.replyErr(req.ID, errCodeServer, "log level control is not enabled on this gateway")
		return
	}
	var level slog.Level
	switch strings.ToLower(strings.TrimSpace(p.Level)) {
	case "debug":
		level = slog.LevelDebug
	case "info":
		level = slog.LevelInfo
	case "warn", "warning":
		level = slog.LevelWarn
	case "error":
		level = slog.LevelError
	default:
		wsc.replyErr(req.ID, errCodeInvalidParams, "invalid level: must be debug, info, warn, or error")
		return
	}
	h.logLevel.Set(level)
	slog.Info("log level changed", "level", level.String(), "peer", wsc.peerID)
	wsc.reply(req.ID, map[string]any{"ok": true, "level": level.String()})
}
