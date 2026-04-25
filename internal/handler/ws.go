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
//	brief.generate
//	session.history
//	session.reset
//	bookmark.create / .capture_session / .list / .get / .search / .update / .delete
//	standing.get / .set / .clear / .profile.set / .profile.delete / .profile.activate
//	agent.run / .start / .get / .wait / .cancel
//	subagent.list / .spawn / .get / .status / .kill / .steer / .transcript
//	memory.search / .insert / .get / .list / .delete / .tag / .batch_get / .timeline / .stats
//	task.candidate.create / .extract / .list / .edit / .approve / .reject
//	task.list / .get / .update / .assign / .snooze / .complete / .reopen
//	waiting.create / .list / .get / .update / .snooze / .resolve / .reopen
//	cron.list / .create / .get / .update / .delete / .trigger / .runs
//	runs.list / .get
//	heartbeat.get / .set / .wake
package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/ffimnsr/koios/internal/agent"
	"github.com/ffimnsr/koios/internal/bookmarks"
	"github.com/ffimnsr/koios/internal/calendar"
	"github.com/ffimnsr/koios/internal/eventbus"
	"github.com/ffimnsr/koios/internal/heartbeat"
	"github.com/ffimnsr/koios/internal/mcp"
	"github.com/ffimnsr/koios/internal/memory"
	"github.com/ffimnsr/koios/internal/monitor"
	"github.com/ffimnsr/koios/internal/ops"
	"github.com/ffimnsr/koios/internal/orchestrator"
	"github.com/ffimnsr/koios/internal/presence"
	"github.com/ffimnsr/koios/internal/runledger"
	"github.com/ffimnsr/koios/internal/scheduler"
	"github.com/ffimnsr/koios/internal/session"
	"github.com/ffimnsr/koios/internal/standing"
	"github.com/ffimnsr/koios/internal/subagent"
	"github.com/ffimnsr/koios/internal/tasks"
	"github.com/ffimnsr/koios/internal/types"
	"github.com/ffimnsr/koios/internal/usage"
	"github.com/ffimnsr/koios/internal/workflow"
	"github.com/ffimnsr/koios/internal/workspace"
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
	mu      *sync.Mutex
	conn    *websocket.Conn
	peerID  string
	onReply func(rpcResponse)
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
	resp := rpcResponse{ID: id, Result: result}
	if c.onReply != nil {
		c.onReply(resp)
	}
	c.send(resp)
}

func (c *wsConn) replyErr(id json.RawMessage, code int, msg string) {
	resp := rpcResponse{ID: id, Error: &rpcError{Code: code, Message: msg}}
	if c.onReply != nil {
		c.onReply(resp)
	}
	c.send(resp)
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
	store                   *session.Store
	provider                llmProvider
	timeout                 time.Duration
	model                   string
	memStore                *memory.Store
	taskStore               *tasks.Store
	bookmarkStore           *bookmarks.Store
	calendarStore           *calendar.Store
	memTopK                 int
	memInject               bool
	identityDir             string
	hbRunner                *heartbeat.Runner
	hbConfigStore           *heartbeat.ConfigStore
	hbDefaultEvery          time.Duration
	standingManager         *standing.Manager
	agentRuntime            *agent.Runtime
	agentCoord              *agent.Coordinator
	subRuntime              *subagent.Runtime
	jobStore                *scheduler.JobStore
	sched                   *scheduler.Scheduler
	workspaceStore          *workspace.Manager
	toolPolicy              ToolPolicy
	execConfig              ExecConfig
	codeExecutionConfig     CodeExecutionConfig
	backgroundProcessConfig BackgroundProcessConfig
	execApprovals           *execApprovalStore
	allowedOrigins          []string // empty = allow all
	hooks                   *ops.Manager
	presence                *presence.Manager
	messageBus              *eventbus.Bus
	usageStore              *usage.Store
	monitor                 *monitor.Monitor
	logLevel                *slog.LevelVar
	mcpManager              *mcp.Manager
	workflowRunner          *workflow.Runner
	orchestrator            *orchestrator.Orchestrator
	idempotency             *idempotencyStore
	runLedger               *runledger.Store

	// fetchClient is the HTTP client used by the web_fetch tool.  When nil,
	// a client backed by ssrfSafeTransport() is used.  Override in tests only.
	fetchClient *http.Client

	// ownerPeerIDs, when non-empty, restricts owner-only commands (e.g. /restart)
	// to the listed peer IDs.
	ownerPeerIDs []string

	// dispatchWG tracks in-flight dispatch goroutines for graceful shutdown.
	dispatchWG sync.WaitGroup

	// syncRunsMu guards syncRuns, which maps a client-visible run ID to a
	// cancel function for in-flight synchronous agent.run calls.  This allows
	// agent.cancel to interrupt a synchronous run that has no async record in
	// the coordinator.
	syncRunsMu sync.Mutex
	syncRuns   map[string]context.CancelFunc
	// codeExecutionRunsMu guards codeExecutionRuns, which maps an async
	// code_execution run ID to its active cancel function.
	codeExecutionRunsMu   sync.Mutex
	codeExecutionRuns     map[string]context.CancelFunc
	backgroundProcessesMu sync.Mutex
	backgroundProcesses   map[string]*managedBackgroundProcess
	clientsMu             sync.RWMutex
	clients               map[*wsConn]struct{}
}

// HandlerOptions holds all optional subsystem references.
type HandlerOptions struct {
	Model                   string
	Timeout                 time.Duration
	MemStore                *memory.Store
	TaskStore               *tasks.Store
	BookmarkStore           *bookmarks.Store
	CalendarStore           *calendar.Store
	MemTopK                 int
	MemInject               bool
	HBRunner                *heartbeat.Runner
	HBConfigStore           *heartbeat.ConfigStore
	HBDefaultEvery          time.Duration
	StandingManager         *standing.Manager
	AgentRuntime            *agent.Runtime
	AgentCoord              *agent.Coordinator
	SubRuntime              *subagent.Runtime
	JobStore                *scheduler.JobStore
	Sched                   *scheduler.Scheduler
	WorkspaceStore          *workspace.Manager
	ToolPolicy              ToolPolicy
	ExecConfig              ExecConfig
	CodeExecutionConfig     CodeExecutionConfig
	BackgroundProcessConfig BackgroundProcessConfig
	Hooks                   *ops.Manager
	Presence                *presence.Manager
	MessageBus              *eventbus.Bus
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
	// WorkflowRunner, when non-nil, enables the workflow.* tool family.
	WorkflowRunner *workflow.Runner
	// Orchestrator, when non-nil, enables the orchestrator.* tool family.
	Orchestrator *orchestrator.Orchestrator
	// OwnerPeerIDs, when non-empty, restricts owner-only slash commands such as
	// /restart to the listed peer IDs. An empty slice grants the commands to all.
	OwnerPeerIDs []string
	// RunLedger, when non-nil, enables the runs.list and runs.get RPC methods.
	RunLedger *runledger.Store
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
	codeExecCfg := normalizeCodeExecutionConfig(opts.CodeExecutionConfig)
	processCfg := normalizeBackgroundProcessConfig(opts.BackgroundProcessConfig)
	h := &Handler{
		store:                   store,
		provider:                prov,
		timeout:                 timeout,
		model:                   opts.Model,
		memStore:                opts.MemStore,
		taskStore:               opts.TaskStore,
		bookmarkStore:           opts.BookmarkStore,
		calendarStore:           opts.CalendarStore,
		memTopK:                 topK,
		memInject:               opts.MemInject,
		identityDir:             opts.WorkspaceRoot,
		hbRunner:                opts.HBRunner,
		hbConfigStore:           opts.HBConfigStore,
		hbDefaultEvery:          opts.HBDefaultEvery,
		standingManager:         opts.StandingManager,
		agentRuntime:            opts.AgentRuntime,
		agentCoord:              opts.AgentCoord,
		subRuntime:              opts.SubRuntime,
		jobStore:                opts.JobStore,
		sched:                   opts.Sched,
		workspaceStore:          opts.WorkspaceStore,
		toolPolicy:              opts.ToolPolicy,
		execConfig:              execCfg,
		codeExecutionConfig:     codeExecCfg,
		backgroundProcessConfig: processCfg,
		allowedOrigins:          opts.AllowedOrigins,
		hooks:                   opts.Hooks,
		presence:                opts.Presence,
		messageBus:              opts.MessageBus,
		usageStore:              opts.UsageStore,
		monitor:                 opts.Monitor,
		logLevel:                opts.LogLevel,
		mcpManager:              opts.MCPManager,
		workflowRunner:          opts.WorkflowRunner,
		orchestrator:            opts.Orchestrator,
		ownerPeerIDs:            opts.OwnerPeerIDs,
		runLedger:               opts.RunLedger,
		syncRuns:                make(map[string]context.CancelFunc),
		codeExecutionRuns:       make(map[string]context.CancelFunc),
		backgroundProcesses:     make(map[string]*managedBackgroundProcess),
		clients:                 make(map[*wsConn]struct{}),
		execApprovals:           newExecApprovalStore(execCfg.ApprovalTTL),
		idempotency:             newIdempotencyStore(idempotencyTTL),
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

	wsc := &wsConn{mu: &sync.Mutex{}, conn: conn, peerID: peerID}
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
	h.stopAllBackgroundProcesses()
	h.dispatchWG.Wait()
}


func (h *Handler) serverCapabilities(peerID string) map[string]any {
	caps := map[string]bool{
		"agent_runtime": h.agentRuntime != nil && h.agentCoord != nil,
		"memory":        h.memStore != nil,
		"tasks":         h.taskStore != nil,
		"calendar":      h.calendarStore != nil,
		"briefing":      true,
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
		"brief.generate",
		"session.history",
		"session.reset",
	}
	if caps["presence"] {
		methods = append(methods, "presence.get", "presence.set")
	}
	if caps["standing"] {
		methods = append(methods, "standing.get", "standing.set", "standing.clear", "standing.profile.set", "standing.profile.delete", "standing.profile.activate")
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
		methods = append(methods,
			"memory.search",
			"memory.insert",
			"memory.get",
			"memory.list",
			"memory.delete",
			"memory.tag",
			"memory.preference.create",
			"memory.preference.get",
			"memory.preference.list",
			"memory.preference.update",
			"memory.preference.confirm",
			"memory.preference.delete",
			"memory.entity.create",
			"memory.entity.update",
			"memory.entity.get",
			"memory.entity.list",
			"memory.entity.search",
			"memory.entity.link_chunk",
			"memory.entity.relate",
			"memory.entity.touch",
			"memory.entity.delete",
			"memory.entity.unlink_chunk",
			"memory.entity.unrelate",
			"memory.candidate.create",
			"memory.candidate.list",
			"memory.candidate.edit",
			"memory.candidate.approve",
			"memory.candidate.merge",
			"memory.candidate.reject",
		)
	}
	if h.bookmarkStore != nil {
		methods = append(methods,
			"bookmark.create",
			"bookmark.capture_session",
			"bookmark.get",
			"bookmark.list",
			"bookmark.search",
			"bookmark.update",
			"bookmark.delete",
		)
	}
	if caps["tasks"] {
		methods = append(methods,
			"task.candidate.create",
			"task.candidate.extract",
			"task.candidate.list",
			"task.candidate.edit",
			"task.candidate.approve",
			"task.candidate.reject",
			"task.list",
			"task.get",
			"task.update",
			"task.assign",
			"task.snooze",
			"task.complete",
			"task.reopen",
			"waiting.create",
			"waiting.list",
			"waiting.get",
			"waiting.update",
			"waiting.snooze",
			"waiting.resolve",
			"waiting.reopen",
		)
	}
	if caps["calendar"] {
		methods = append(methods,
			"calendar.source.create",
			"calendar.source.list",
			"calendar.source.delete",
			"calendar.agenda",
		)
	}
	if caps["workspace"] {
		methods = append(methods, "workspace.list", "workspace.read", "workspace.head", "workspace.tail", "workspace.grep", "workspace.sort", "workspace.uniq", "workspace.diff", "workspace.write", "workspace.edit", "workspace.mkdir", "workspace.delete")
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
	if h.runLedger != nil {
		methods = append(methods, "runs.list", "runs.get")
	}

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
		"peer_id":      peerID,
		"capabilities": caps,
		"methods":      methods,
		"chat_tools":   tools,
		"idempotency": map[string]any{
			"params_field": "idempotency_key",
			"methods":      idempotentRPCMethods(),
		},
		"stream_notifications": append(streamNotifications, "session.message"),
	}
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
