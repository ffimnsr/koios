package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/ffimnsr/koios/internal/memory"
	"github.com/ffimnsr/koios/internal/ops"
	"github.com/ffimnsr/koios/internal/redact"
	"github.com/ffimnsr/koios/internal/requestctx"
	"github.com/ffimnsr/koios/internal/session"
	"github.com/ffimnsr/koios/internal/standing"
	"github.com/ffimnsr/koios/internal/tasks"
	"github.com/ffimnsr/koios/internal/types"
)

// Provider is the minimal model interface used by the agent runtime.
type Provider interface {
	Complete(ctx context.Context, req *types.ChatRequest) (*types.ChatResponse, error)
	CompleteStream(ctx context.Context, req *types.ChatRequest, w http.ResponseWriter) (string, error)
}

// SessionScope describes how a run maps onto session storage.
type SessionScope string

const (
	ScopeMain     SessionScope = "main"
	ScopeDirect   SessionScope = "direct"
	ScopeIsolated SessionScope = "isolated"
	ScopeGlobal   SessionScope = "global"

	QueueModeSteer    = "steer"
	QueueModeFollowup = "followup"
	QueueModeCollect  = "collect"

	SilentReplyToken = "NO_REPLY"
)

var silentReplyTokens = []string{SilentReplyToken, "SILENT_REPLY", "SILENT_REPLY_TOKEN"}

// RetryPolicy controls retry behavior for provider failures.
type RetryPolicy struct {
	MaxAttempts    int
	InitialBackoff time.Duration
	MaxBackoff     time.Duration
	StatusCodes    []int
}

// RunRequest describes one agent turn.
type RunRequest struct {
	PeerID       string
	SenderID     string
	Scope        SessionScope
	SessionKey   string
	Messages     []types.Message
	Model        string
	Stream       bool
	BlockStream  bool
	MaxSteps     int
	MaxTokens    int
	Temperature  *float64
	TopP         *float64
	ToolExecutor ToolExecutor
	EventSink    func(Event)
	Timeout      time.Duration
}

// EventKind identifies a lifecycle or streaming event emitted by the runtime.
type EventKind string

const (
	EventRunStart   EventKind = "run_start"
	EventStepStart  EventKind = "step_start"
	EventContext    EventKind = "context_built"
	EventMemory     EventKind = "memory_injected"
	EventPrune      EventKind = "session_pruned"
	EventToolCall   EventKind = "tool_call"
	EventToolResult EventKind = "tool_result"
	EventRunBlock   EventKind = "run_block"
	EventRunFinish  EventKind = "run_finish"
	EventRunError   EventKind = "run_error"
	EventRetry      EventKind = "run_retry"
	EventSessionKey EventKind = "session_key"
)

// Event is a structured runtime event.
type Event struct {
	Kind       EventKind `json:"kind"`
	SessionKey string    `json:"session_key,omitempty"`
	Message    string    `json:"message,omitempty"`
	Attempt    int       `json:"attempt,omitempty"`
	Step       int       `json:"step,omitempty"`
	Count      int       `json:"count,omitempty"`
	Error      string    `json:"error,omitempty"`
	ToolName   string    `json:"tool_name,omitempty"`
	Summary    string    `json:"summary,omitempty"`
	OK         *bool     `json:"ok,omitempty"`
}

// Result contains the outcome of a run.
type Result struct {
	SessionKey       string              `json:"session_key"`
	Attempts         int                 `json:"attempts"`
	AssistantText    string              `json:"assistant_text,omitempty"`
	Response         *types.ChatResponse `json:"response,omitempty"`
	Usage            types.Usage         `json:"usage,omitempty"`
	Steps            int                 `json:"steps"`
	Events           []Event             `json:"events,omitempty"`
	SuppressedReply  bool                `json:"suppressed_reply,omitempty"`
	SuppressionToken string              `json:"suppression_token,omitempty"`
}

type memoryCandidateExtraction struct {
	Candidates []memoryCandidateSuggestion `json:"candidates"`
}

type memoryCandidateSuggestion struct {
	Content        string   `json:"content"`
	Category       string   `json:"category,omitempty"`
	Tags           []string `json:"tags,omitempty"`
	RetentionClass string   `json:"retention_class,omitempty"`
}

type entityExtraction struct {
	Entities []entitySuggestion `json:"entities"`
}

type entitySuggestion struct {
	Kind    string   `json:"kind"`
	Name    string   `json:"name"`
	Aliases []string `json:"aliases,omitempty"`
	Notes   string   `json:"notes,omitempty"`
}

type providerCapabilities interface {
	Capabilities(model string) types.ProviderCapabilities
}

func DetectSilentReply(text string) (bool, string) {
	trimmed := strings.TrimSpace(text)
	for _, token := range silentReplyTokens {
		if trimmed == token {
			return true, token
		}
	}
	return false, ""
}

func SilentReplyMayContinue(text string) bool {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return true
	}
	for _, token := range silentReplyTokens {
		if trimmed == token || strings.HasPrefix(token, trimmed) {
			return true
		}
	}
	return false
}

// ToolCall is a structured request emitted by the model when it wants the
// runtime to execute a server-side tool.
type ToolCall struct {
	ID        string          `json:"id,omitempty"`
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

// ToolExecutor provides the runtime's tool prompt and executes tool calls.
type ToolExecutor interface {
	ToolPrompt(peerID string) string
	ToolDefinitions(peerID string) []types.Tool
	ExecuteTool(ctx context.Context, peerID string, call ToolCall) (any, error)
}

type toolNameNormalizer interface {
	NormalizeToolName(peerID, name string) string
}

// Runtime executes agent turns against a provider while managing session
// scoping, persistence, compaction, and retries.
type Runtime struct {
	store             *session.Store
	prov              Provider
	model             string
	retry             RetryPolicy
	defaultTimeout    time.Duration
	globalKey         string
	memStore          *memory.Store
	taskStore         *tasks.Store
	memTopK           int
	memInject         bool
	memLCMWindow      int
	memNamespaces     []string
	pruneToolMessages int
	standingManager   *standing.Manager
	hooks             *ops.Manager
	identityDir       string

	steeringMu     sync.Mutex
	steeringQueues map[string][]string // sessionKey → pending user messages
	activeStreams  map[string]*activeStreamState
}

type activeStreamState struct {
	cancel      context.CancelFunc
	interrupted bool
}

func NormalizeQueueMode(mode string) string {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case QueueModeSteer:
		return QueueModeSteer
	case QueueModeCollect:
		return QueueModeCollect
	case "", QueueModeFollowup:
		return QueueModeFollowup
	default:
		return QueueModeFollowup
	}
}

type contextSessionKey struct{}

// CurrentSessionKey returns the runtime session key associated with ctx.
func CurrentSessionKey(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	sessionKey, _ := ctx.Value(contextSessionKey{}).(string)
	return sessionKey
}

// Model returns the configured model name.
func (rt *Runtime) Model() string {
	return rt.model
}

// EnableMemory configures long-term memory augmentation for agent runs.
func (rt *Runtime) EnableMemory(store *memory.Store, inject bool, topK int) {
	rt.memStore = store
	rt.memInject = inject
	rt.memTopK = topK
	if rt.memTopK <= 0 {
		rt.memTopK = 3
	}
}

// EnableTasks configures the durable task store used for extraction and review.
func (rt *Runtime) EnableTasks(store *tasks.Store) {
	rt.taskStore = store
}

// SetMemoryLCM configures the Latent Context Memory sliding-window window size
// and optional additional namespaces (peer IDs) to inject from.
func (rt *Runtime) SetMemoryLCM(window int, namespaces []string) {
	rt.memLCMWindow = window
	rt.memNamespaces = namespaces
}

// SetPruning configures how many recent tool-related messages remain visible
// to the model during context assembly. Older tool chatter stays in the
// transcript but is pruned from the active request context.
func (rt *Runtime) SetPruning(keepToolMessages int) {
	if keepToolMessages < 0 {
		keepToolMessages = 0
	}
	rt.pruneToolMessages = keepToolMessages
}

func (rt *Runtime) SetStandingOrders(manager *standing.Manager) {
	rt.standingManager = manager
}

// SetHooks configures lifecycle hooks for runtime events.
func (rt *Runtime) SetHooks(hooks *ops.Manager) {
	rt.hooks = hooks
}

// SetIdentityDir configures the workspace root directory from which identity
// files (AGENTS.md, SOUL.md, USER.md, IDENTITY.md) are injected into every
// system prompt.
func (rt *Runtime) SetIdentityDir(dir string) {
	rt.identityDir = dir
}

// Steer enqueues a user message to be injected into the next loop iteration
// for the given session. A maximum of 10 messages may be queued at once.
func (rt *Runtime) Steer(sessionKey, message string) error {
	if strings.TrimSpace(sessionKey) == "" {
		return fmt.Errorf("session_key is required")
	}
	if strings.TrimSpace(message) == "" {
		return fmt.Errorf("message is required")
	}
	sessionKey = rt.normalizeSessionKey(sessionKey)
	rt.steeringMu.Lock()
	defer rt.steeringMu.Unlock()
	if rt.steeringQueues == nil {
		rt.steeringQueues = make(map[string][]string)
	}
	q := rt.steeringQueues[sessionKey]
	if len(q) >= 10 {
		return fmt.Errorf("steering queue full for session %s", sessionKey)
	}
	rt.steeringQueues[sessionKey] = append(q, message)
	if NormalizeQueueMode(rt.sessionPolicy(sessionKey).QueueMode) == QueueModeSteer {
		if active := rt.activeStreams[sessionKey]; active != nil && active.cancel != nil {
			active.interrupted = true
			active.cancel()
		}
	}
	return nil
}

// drainSteering returns and removes any queued steering messages for sessionKey.
func (rt *Runtime) drainSteering(sessionKey string) []string {
	sessionKey = rt.normalizeSessionKey(sessionKey)
	rt.steeringMu.Lock()
	defer rt.steeringMu.Unlock()
	msgs := rt.steeringQueues[sessionKey]
	if len(msgs) == 0 {
		return nil
	}
	delete(rt.steeringQueues, sessionKey)
	if NormalizeQueueMode(rt.sessionPolicy(sessionKey).QueueMode) == QueueModeCollect && len(msgs) > 1 {
		return []string{formatCollectedSteering(msgs)}
	}
	return msgs
}

func formatCollectedSteering(msgs []string) string {
	if len(msgs) == 0 {
		return ""
	}
	if len(msgs) == 1 {
		return msgs[0]
	}
	var sb strings.Builder
	sb.WriteString("Collected steering updates:\n")
	for _, msg := range msgs {
		trimmed := strings.TrimSpace(msg)
		if trimmed == "" {
			continue
		}
		sb.WriteString("- ")
		sb.WriteString(trimmed)
		sb.WriteString("\n")
	}
	return strings.TrimRight(sb.String(), "\n")
}

func (rt *Runtime) registerActiveStream(sessionKey string, cancel context.CancelFunc) *activeStreamState {
	sessionKey = rt.normalizeSessionKey(sessionKey)
	rt.steeringMu.Lock()
	defer rt.steeringMu.Unlock()
	if rt.activeStreams == nil {
		rt.activeStreams = make(map[string]*activeStreamState)
	}
	state := &activeStreamState{cancel: cancel}
	rt.activeStreams[sessionKey] = state
	return state
}

func (rt *Runtime) unregisterActiveStream(sessionKey string, state *activeStreamState) {
	sessionKey = rt.normalizeSessionKey(sessionKey)
	rt.steeringMu.Lock()
	defer rt.steeringMu.Unlock()
	if rt.activeStreams[sessionKey] == state {
		delete(rt.activeStreams, sessionKey)
	}
}

// NewRuntime creates a runtime.
func NewRuntime(store *session.Store, prov Provider, model string, timeout time.Duration, retry RetryPolicy) *Runtime {
	if retry.MaxAttempts <= 0 {
		retry.MaxAttempts = 3
	}
	if retry.InitialBackoff <= 0 {
		retry.InitialBackoff = 500 * time.Millisecond
	}
	if retry.MaxBackoff <= 0 {
		retry.MaxBackoff = 5 * time.Second
	}
	if len(retry.StatusCodes) == 0 {
		retry.StatusCodes = []int{429, 500, 502, 503, 504}
	}
	if timeout <= 0 {
		timeout = 2 * time.Minute
	}
	return &Runtime{
		store:          store,
		prov:           prov,
		model:          model,
		retry:          retry,
		defaultTimeout: timeout,
		globalKey:      "__global__",
	}
}

// Run executes the request without streaming.
func (rt *Runtime) Run(ctx context.Context, req RunRequest) (*Result, error) {
	return rt.run(ctx, req, nil)
}

// RunStream executes the request and streams the provider's SSE output to w.
func (rt *Runtime) RunStream(ctx context.Context, req RunRequest, w http.ResponseWriter) (*Result, error) {
	buf := &captureResponseWriter{header: make(http.Header), downstream: w}
	return rt.run(ctx, req, buf)
}

func (rt *Runtime) run(ctx context.Context, req RunRequest, sink *captureResponseWriter) (*Result, error) {
	if len(req.Messages) == 0 {
		return nil, fmt.Errorf("messages must not be empty")
	}
	if req.Model == "" {
		req.Model = rt.model
	}
	if req.Model == "" {
		return nil, fmt.Errorf("model must not be empty")
	}
	sessionKey := rt.sessionKey(req)

	// Apply session-persisted model override; it takes precedence over the
	// runtime default but yields to an explicit per-request Model value.
	if req.Model == rt.model {
		if policy := rt.sessionPolicy(sessionKey); policy.ModelOverride != "" {
			req.Model = policy.ModelOverride
		}
	}

	result := &Result{SessionKey: sessionKey}
	reqCopy := req
	reqCopy.SessionKey = sessionKey
	reqCopy.Messages = append([]types.Message(nil), req.Messages...)
	rt.emitEvent(result, reqCopy.EventSink, Event{Kind: EventSessionKey, SessionKey: sessionKey})
	rt.emitEvent(result, reqCopy.EventSink, Event{Kind: EventRunStart, SessionKey: sessionKey})
	if reqCopy.Timeout <= 0 {
		reqCopy.Timeout = rt.defaultTimeout
	}
	if reqCopy.MaxSteps <= 0 {
		if reqCopy.ToolExecutor != nil || reqCopy.Stream {
			reqCopy.MaxSteps = 4
		} else {
			reqCopy.MaxSteps = 1
		}
	}
	callCtx, cancel := context.WithTimeout(ctx, reqCopy.Timeout)
	defer cancel()
	workingMessages := append([]types.Message(nil), reqCopy.Messages...)
	persistedMessages := append([]types.Message(nil), reqCopy.Messages...)

	var lastErr error
	for step := 1; step <= reqCopy.MaxSteps; step++ {
		result.Steps = step
		rt.emitEvent(result, reqCopy.EventSink, Event{Kind: EventStepStart, SessionKey: sessionKey, Step: step})

		// Poll for pending steering messages before building the next request context.
		if steered := rt.drainSteering(sessionKey); len(steered) > 0 {
			for _, msg := range steered {
				workingMessages = append(workingMessages, types.Message{Role: "user", Content: msg})
			}
		}

		history := rt.store.Get(sessionKey).History()
		stepMessages := append([]types.Message(nil), workingMessages...)
		if reqCopy.ToolExecutor != nil {
			if toolPrompt := strings.TrimSpace(reqCopy.ToolExecutor.ToolPrompt(reqCopy.PeerID)); toolPrompt != "" {
				stepMessages = append([]types.Message{{Role: "system", Content: toolPrompt}}, stepMessages...)
			}
		}
		extraSystem, err := rt.standingSystemMessages(reqCopy)
		if err != nil {
			rt.emitEvent(result, reqCopy.EventSink, Event{Kind: EventRunError, SessionKey: sessionKey, Step: step, Error: err.Error()})
			return result, err
		}
		built, err := requestctx.Build(callCtx, requestctx.BuildOptions{
			Model:             reqCopy.Model,
			Messages:          stepMessages,
			History:           history,
			Stream:            reqCopy.Stream,
			ExtraSystem:       extraSystem,
			PruneToolMessages: rt.pruneToolMessages,
			MemoryStore:       rt.memStore,
			MemoryTopK:        rt.memTopK,
			MemoryInject:      rt.memInject,
			MemoryPeerID:      reqCopy.PeerID,
			MemoryLCMWindow:   rt.memLCMWindow,
			MemoryNamespaces:  rt.memNamespaces,
			IdentityDir:       rt.identityDir,
		})
		if err != nil {
			rt.emitEvent(result, reqCopy.EventSink, Event{Kind: EventRunError, SessionKey: sessionKey, Step: step, Error: err.Error()})
			return result, err
		}
		rt.emitEvent(result, reqCopy.EventSink, Event{
			Kind:       EventContext,
			SessionKey: sessionKey,
			Step:       step,
			Count:      len(built.Request.Messages),
		})
		if built.MemoryHits > 0 {
			rt.emitEvent(result, reqCopy.EventSink, Event{
				Kind:       EventMemory,
				SessionKey: sessionKey,
				Step:       step,
				Count:      built.MemoryHits,
			})
		}
		if built.PrunedMessages > 0 {
			rt.emitEvent(result, reqCopy.EventSink, Event{
				Kind:       EventPrune,
				SessionKey: sessionKey,
				Step:       step,
				Count:      built.PrunedMessages,
			})
		}
		if reqCopy.ToolExecutor != nil {
			caps := providerCapabilitiesFor(rt.prov, built.Request.Model)
			if caps.SupportsNativeTools || caps.Name == "" {
				built.Request.Tools = reqCopy.ToolExecutor.ToolDefinitions(reqCopy.PeerID)
			}
			if len(built.Request.Tools) > 0 {
				built.Request.ToolChoice = "auto"
			}
			// Only force non-streaming for non-stream requests. Streamed websocket
			// turns still need the provider's streaming path when no tool call is made.
			if !reqCopy.Stream {
				built.Request.Stream = false
			}
		}
		built.Request.MaxTokens = reqCopy.MaxTokens
		built.Request.Temperature = reqCopy.Temperature
		built.Request.TopP = reqCopy.TopP
		advanceStep := false
		for attempt := 1; attempt <= rt.retry.MaxAttempts; attempt++ {
			result.Attempts = attempt
			attemptCtx, attemptCancel := context.WithCancel(callCtx)
			invokeReq := *built.Request
			invokeStream := invokeReq.Stream
			invokeSink := sink
			toolProbe := reqCopy.Stream && reqCopy.ToolExecutor != nil && len(invokeReq.Tools) > 0
			if toolProbe {
				invokeReq.Stream = false
				invokeStream = false
				invokeSink = nil
			}
			var activeStream *activeStreamState
			if invokeStream {
				activeStream = rt.registerActiveStream(sessionKey, attemptCancel)
			}
			if sink != nil {
				sink.reset()
			}
			attemptCtx = context.WithValue(attemptCtx, contextSessionKey{}, sessionKey)
			assistantText, resp, err := rt.invoke(attemptCtx, &invokeReq, invokeStream, invokeSink)
			if err != nil && toolProbe && err.Error() == "nil response from provider" {
				invokeReq.Stream = built.Request.Stream
				invokeStream = invokeReq.Stream
				invokeSink = sink
				if invokeStream {
					activeStream = rt.registerActiveStream(sessionKey, attemptCancel)
				}
				assistantText, resp, err = rt.invoke(attemptCtx, &invokeReq, invokeStream, invokeSink)
			}
			if activeStream != nil {
				rt.unregisterActiveStream(sessionKey, activeStream)
			}
			attemptCancel()
			if err != nil && activeStream != nil && activeStream.interrupted {
				if steered := rt.drainSteering(sessionKey); len(steered) > 0 {
					for _, msg := range steered {
						workingMessages = append(workingMessages, types.Message{Role: "user", Content: msg})
						persistedMessages = append(persistedMessages, types.Message{Role: "user", Content: msg})
					}
					advanceStep = true
					lastErr = nil
					break
				}
			}
			if err == nil {
				assistantText, resp, suppressed, token := suppressSilentReply(assistantText, resp)
				if suppressed {
					result.SuppressedReply = true
					result.SuppressionToken = token
				}
				if reqCopy.ToolExecutor != nil && resp != nil {
					if toolCalls := nativeToolCalls(resp); len(toolCalls) > 0 {
						toolLoopAborted := false
						for _, tc := range toolCalls {
							tc.Name = normalizeToolName(reqCopy.ToolExecutor, reqCopy.PeerID, tc.Name)
							if err := rt.emitHook(callCtx, ops.Event{
								Name:       ops.HookBeforeToolCall,
								PeerID:     reqCopy.PeerID,
								SessionKey: sessionKey,
								Data: map[string]any{
									"tool": tc.Name,
									"step": step,
								},
							}); err != nil {
								lastErr = err
								rt.emitEvent(result, reqCopy.EventSink, Event{Kind: EventRunError, SessionKey: sessionKey, Attempt: attempt, Step: step, Error: err.Error()})
								toolLoopAborted = true
								break
							}
							argsSummary := summarizeToolJSON(tc.Arguments)
							rt.emitEvent(result, reqCopy.EventSink, Event{Kind: EventToolCall, SessionKey: sessionKey, Step: step, Message: tc.Name, ToolName: tc.Name, Summary: argsSummary})
							toolResult, execErr := reqCopy.ToolExecutor.ExecuteTool(attemptCtx, reqCopy.PeerID, tc)
							ok := execErr == nil
							rt.emitEvent(result, reqCopy.EventSink, Event{Kind: EventToolResult, SessionKey: sessionKey, Step: step, Message: tc.Name, ToolName: tc.Name, Summary: summarizeToolResult(toolResult, execErr), OK: &ok})
							if hookErr := rt.emitHook(callCtx, ops.Event{
								Name:       ops.HookAfterToolCall,
								PeerID:     reqCopy.PeerID,
								SessionKey: sessionKey,
								Data: map[string]any{
									"tool":  tc.Name,
									"step":  step,
									"ok":    execErr == nil,
									"error": errString(execErr),
								},
							}); hookErr != nil {
								lastErr = hookErr
								rt.emitEvent(result, reqCopy.EventSink, Event{Kind: EventRunError, SessionKey: sessionKey, Attempt: attempt, Step: step, Error: hookErr.Error()})
								toolLoopAborted = true
								break
							}
							workingMessages = append(workingMessages,
								types.Message{Role: "assistant", Content: assistantText, ToolCalls: []types.ToolCall{{
									ID:   tc.ID,
									Type: "function",
									Function: types.ToolCallFunctionRef{
										Name:      tc.Name,
										Arguments: string(tc.Arguments),
									},
								}}},
								types.Message{Role: "tool", ToolCallID: tc.ID, Content: formatToolResult(tc.Name, toolResult, execErr)},
							)
							if execErr != nil {
								lastErr = execErr
							}
							// Check for incoming steering messages after each tool;
							// if present, skip remaining tool calls in this batch.
							if steered := rt.drainSteering(sessionKey); len(steered) > 0 {
								for _, msg := range steered {
									workingMessages = append(workingMessages, types.Message{Role: "user", Content: msg})
								}
								break
							}
						}
						if toolLoopAborted {
							break
						}
						advanceStep = true
						break
					}
					call, ok, parseErr := parseToolCall(assistantText)
					if parseErr != nil {
						lastErr = parseErr
						rt.emitEvent(result, reqCopy.EventSink, Event{Kind: EventRunError, SessionKey: sessionKey, Attempt: attempt, Step: step, Error: parseErr.Error()})
						break
					}
					if ok {
						call.Name = normalizeToolName(reqCopy.ToolExecutor, reqCopy.PeerID, call.Name)
						if err := rt.emitHook(callCtx, ops.Event{
							Name:       ops.HookBeforeToolCall,
							PeerID:     reqCopy.PeerID,
							SessionKey: sessionKey,
							Data: map[string]any{
								"tool": call.Name,
								"step": step,
							},
						}); err != nil {
							lastErr = err
							rt.emitEvent(result, reqCopy.EventSink, Event{Kind: EventRunError, SessionKey: sessionKey, Attempt: attempt, Step: step, Error: err.Error()})
							break
						}
						argsSummary := summarizeToolJSON(call.Arguments)
						rt.emitEvent(result, reqCopy.EventSink, Event{Kind: EventToolCall, SessionKey: sessionKey, Step: step, Message: call.Name, ToolName: call.Name, Summary: argsSummary})
						toolResult, execErr := reqCopy.ToolExecutor.ExecuteTool(attemptCtx, reqCopy.PeerID, *call)
						ok := execErr == nil
						rt.emitEvent(result, reqCopy.EventSink, Event{Kind: EventToolResult, SessionKey: sessionKey, Step: step, Message: call.Name, ToolName: call.Name, Summary: summarizeToolResult(toolResult, execErr), OK: &ok})
						if hookErr := rt.emitHook(callCtx, ops.Event{
							Name:       ops.HookAfterToolCall,
							PeerID:     reqCopy.PeerID,
							SessionKey: sessionKey,
							Data: map[string]any{
								"tool":  call.Name,
								"step":  step,
								"ok":    execErr == nil,
								"error": errString(execErr),
							},
						}); hookErr != nil {
							lastErr = hookErr
							rt.emitEvent(result, reqCopy.EventSink, Event{Kind: EventRunError, SessionKey: sessionKey, Attempt: attempt, Step: step, Error: hookErr.Error()})
							break
						}
						workingMessages = append(workingMessages,
							types.Message{Role: "assistant", Content: assistantText},
							types.Message{Role: "tool", ToolCallID: call.ID, Content: formatToolResult(call.Name, toolResult, execErr)},
						)
						if execErr != nil {
							lastErr = execErr
						}
						advanceStep = true
						break
					}
				}
				result.AssistantText = assistantText
				result.Response = resp
				if resp != nil {
					result.Usage = resp.Usage
				}
				if steered := rt.drainSteering(sessionKey); len(steered) > 0 && step < reqCopy.MaxSteps {
					if assistantMsg, ok := assistantMessage(assistantText, resp); ok {
						workingMessages = append(workingMessages, assistantMsg)
						persistedMessages = append(persistedMessages, assistantMsg)
					}
					for _, msg := range steered {
						workingMessages = append(workingMessages, types.Message{Role: "user", Content: msg})
						persistedMessages = append(persistedMessages, types.Message{Role: "user", Content: msg})
					}
					advanceStep = true
					break
				}
				if reqCopy.Stream && sink != nil {
					rt.emitEvent(result, reqCopy.EventSink, Event{Kind: EventRunBlock, SessionKey: sessionKey, Step: step, Message: assistantText})
				}
				if assistantMsg, ok := assistantMessage(assistantText, resp); ok {
					persistedMessages = append(persistedMessages, assistantMsg)
				}
				rt.persistTurn(reqCopy.PeerID, sessionKey, reqCopy.Model, persistedMessages, workingMessages, assistantText, result.SuppressedReply)
				rt.emitEvent(result, reqCopy.EventSink, Event{Kind: EventRunFinish, SessionKey: sessionKey, Step: step, Message: assistantText})
				return result, nil
			}
			lastErr = err
			rt.emitEvent(result, reqCopy.EventSink, Event{Kind: EventRunError, SessionKey: sessionKey, Attempt: attempt, Step: step, Error: err.Error()})
			if invokeStream && sink != nil && sink.liveWritten() {
				break
			}
			if attempt < rt.retry.MaxAttempts && rt.shouldRetry(err) {
				rt.emitEvent(result, reqCopy.EventSink, Event{Kind: EventRetry, SessionKey: sessionKey, Attempt: attempt + 1, Step: step})
				select {
				case <-ctx.Done():
					return result, ctx.Err()
				case <-time.After(rt.backoff(attempt)):
				}
				continue
			}
			break
		}
		if advanceStep {
			continue
		}
		break
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("agent run failed")
	}
	return result, lastErr
}

func (rt *Runtime) standingSystemMessages(req RunRequest) ([]types.Message, error) {
	if rt.standingManager == nil || req.Scope == ScopeGlobal {
		return nil, nil
	}
	msg, err := rt.standingManager.SystemMessage(req.PeerID)
	if err != nil || msg == nil {
		return nil, err
	}
	return []types.Message{*msg}, nil
}

func (rt *Runtime) invoke(ctx context.Context, req *types.ChatRequest, stream bool, sink *captureResponseWriter) (string, *types.ChatResponse, error) {
	if rt.hooks != nil {
		ev, err := rt.hooks.Intercept(ctx, ops.Event{
			Name: ops.HookBeforeLLM,
			Data: map[string]any{
				"model":          req.Model,
				"messages":       req.Messages,
				"messages_count": len(req.Messages),
				"stream":         stream,
			},
		})
		if err != nil {
			return "", nil, err
		}
		req.Model = eventString(ev.Data, "model", req.Model)
		if msgs, ok := eventMessages(ev.Data, "messages"); ok {
			req.Messages = msgs
		}
	}
	if err := rt.emitHook(ctx, ops.Event{
		Name: ops.HookBeforeLLM,
		Data: map[string]any{
			"model":          req.Model,
			"messages_count": len(req.Messages),
			"stream":         stream,
		},
	}); err != nil {
		return "", nil, err
	}
	var text string
	var resp *types.ChatResponse
	var callErr error
	if stream {
		if sink == nil {
			return "", nil, fmt.Errorf("stream sink is required")
		}
		var streamText string
		streamText, callErr = rt.prov.CompleteStream(ctx, req, sink)
		if callErr == nil {
			text = streamText
			resp = &types.ChatResponse{
				Choices: []types.ChatChoice{{Message: types.Message{Role: "assistant", Content: streamText}}},
			}
		}
	} else {
		resp, callErr = rt.prov.Complete(ctx, req)
		if callErr == nil {
			if resp == nil {
				callErr = fmt.Errorf("nil response from provider")
			} else if len(resp.Choices) == 0 {
				callErr = fmt.Errorf("empty response from provider")
			} else {
				text = resp.Choices[0].Message.Content
			}
		}
	}
	if callErr != nil {
		return text, nil, callErr
	}
	_ = rt.emitHook(ctx, ops.Event{
		Name: ops.HookAfterLLM,
		Data: map[string]any{
			"model":  req.Model,
			"stream": stream,
		},
	})
	return text, resp, nil
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

func eventMessages(data map[string]any, key string) ([]types.Message, bool) {
	if data == nil {
		return nil, false
	}
	value, ok := data[key]
	if !ok || value == nil {
		return nil, false
	}
	buf, err := json.Marshal(value)
	if err != nil {
		return nil, false
	}
	var msgs []types.Message
	if err := json.Unmarshal(buf, &msgs); err != nil {
		return nil, false
	}
	return msgs, true
}

func assistantMessage(assistantText string, resp *types.ChatResponse) (types.Message, bool) {
	if resp != nil && len(resp.Choices) > 0 {
		msg := resp.Choices[0].Message
		if msg.Role == "" {
			msg.Role = "assistant"
		}
		if msg.Content != "" || len(msg.ToolCalls) > 0 {
			return msg, true
		}
	}
	if strings.TrimSpace(assistantText) == "" {
		return types.Message{}, false
	}
	return types.Message{Role: "assistant", Content: assistantText}, true
}

func (rt *Runtime) persistTurn(peerID, sessionKey, model string, transcript, workingMessages []types.Message, assistantText string, suppressed bool) {
	// Session persistence — unchanged behavior.
	if len(transcript) > 0 {
		rt.store.AppendCtx(context.Background(), sessionKey, transcript...)
	}

	// Long-term memory persistence stores a compact archived turn summary for
	// operator recall while candidate extraction proposes higher-signal facts
	// for explicit approval before they become auto-injectable memory.
	if suppressed || rt.memStore == nil || peerID == "" {
		return
	}
	chunk := buildTurnMemoryChunk(transcript, workingMessages, assistantText)
	if chunk == "" {
		return
	}
	memCtx, cancel := context.WithTimeout(context.Background(), minDuration(rt.defaultTimeout/4, 8*time.Second))
	defer cancel()
	archivedChunk, err := rt.memStore.InsertChunkWithOptions(memCtx, peerID, redact.String(chunk), memory.ChunkOptions{
		Category:       "conversation",
		RetentionClass: memory.RetentionClassArchive,
		ExposurePolicy: memory.ExposurePolicySearchOnly,
	})
	if err != nil {
		slog.Warn("agent: archived turn memory insert failed", "peer", peerID, "err", err)
		return
	}
	if err := rt.queueTurnMemoryCandidates(memCtx, peerID, sessionKey, model, transcript, chunk); err != nil {
		slog.Warn("agent: memory candidate extraction failed", "peer", peerID, "err", err)
	}
	if err := rt.queueTurnTaskCandidates(memCtx, peerID, sessionKey, transcript); err != nil {
		slog.Warn("agent: task candidate extraction failed", "peer", peerID, "err", err)
	}
	if err := rt.upsertTurnEntities(memCtx, peerID, sessionKey, model, transcript, chunk, archivedChunk.ID); err != nil {
		slog.Warn("agent: entity extraction failed", "peer", peerID, "err", err)
	}
}

func (rt *Runtime) normalizeSessionKey(sessionKey string) string {
	sessionKey = strings.TrimSpace(sessionKey)
	if sessionKey == "" {
		return ""
	}
	if strings.Contains(sessionKey, "::") {
		return sessionKey
	}
	return sessionKey + "::main"
}

func (rt *Runtime) sessionPolicy(sessionKey string) session.SessionPolicy {
	policy := rt.store.Policy(sessionKey)
	if policy != (session.SessionPolicy{}) {
		return policy
	}
	normalized := rt.normalizeSessionKey(sessionKey)
	if normalized != sessionKey {
		return rt.store.Policy(normalized)
	}
	if idx := strings.Index(sessionKey, "::"); idx > 0 {
		return rt.store.Policy(sessionKey[:idx])
	}
	return policy
}

// buildTurnMemoryChunk formats a completed agent turn as a compact memory
// entry: the last user message, any tool names invoked (not their payloads),
// and the final assistant response.
func buildTurnMemoryChunk(userMsgs, workingMsgs []types.Message, assistantText string) string {
	// Use the last non-empty user message as the anchor.
	userContent := lastUserMessageContent(userMsgs)
	if userContent == "" {
		return ""
	}

	// Collect unique tool names from assistant messages that include tool calls.
	var toolNames []string
	seen := make(map[string]bool)
	for _, m := range workingMsgs {
		for _, tc := range m.ToolCalls {
			if tc.Function.Name != "" && !seen[tc.Function.Name] {
				toolNames = append(toolNames, tc.Function.Name)
				seen[tc.Function.Name] = true
			}
		}
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "User: %s\n", userContent)
	if len(toolNames) > 0 {
		fmt.Fprintf(&sb, "Tools: %s\n", strings.Join(toolNames, ", "))
	}
	if a := strings.TrimSpace(assistantText); a != "" {
		fmt.Fprintf(&sb, "Assistant: %s", a)
	}
	return strings.TrimSpace(sb.String())
}

func lastUserMessageContent(msgs []types.Message) string {
	var userContent string
	for _, m := range msgs {
		if m.Role == "user" && strings.TrimSpace(m.Content) != "" {
			userContent = strings.TrimSpace(m.Content)
		}
	}
	return userContent
}

func (rt *Runtime) queueTurnMemoryCandidates(ctx context.Context, peerID, sessionKey, model string, transcript []types.Message, summary string) error {
	if rt == nil || rt.prov == nil || rt.memStore == nil {
		return nil
	}
	userContent := lastUserMessageContent(transcript)
	if !shouldExtractMemoryCandidates(userContent) {
		return nil
	}
	suggestions, err := rt.extractMemoryCandidates(ctx, model, summary)
	if err != nil {
		return err
	}
	if len(suggestions) == 0 {
		return nil
	}
	seen, err := rt.existingMemoryContentSet(ctx, peerID)
	if err != nil {
		return err
	}
	for _, suggestion := range suggestions {
		normalized := normalizeMemoryCandidateContent(suggestion.Content)
		if normalized == "" {
			continue
		}
		if _, ok := seen[normalized]; ok {
			continue
		}
		if _, err := rt.memStore.QueueCandidateWithProvenance(ctx, peerID, suggestion.Content, memory.ChunkOptions{
			Tags:           suggestion.Tags,
			Category:       strings.TrimSpace(suggestion.Category),
			RetentionClass: normalizeSuggestionRetention(suggestion.RetentionClass),
		}, memory.CandidateProvenance{
			CaptureKind:      memory.CandidateCaptureAutoTurnExtract,
			SourceSessionKey: rt.normalizeSessionKey(sessionKey),
			SourceExcerpt:    userContent,
		}); err != nil {
			slog.Warn("agent: memory candidate queue failed", "peer", peerID, "err", err)
			continue
		}
		seen[normalized] = struct{}{}
	}
	return nil
}

func (rt *Runtime) queueTurnTaskCandidates(ctx context.Context, peerID, sessionKey string, transcript []types.Message) error {
	if rt == nil || rt.taskStore == nil {
		return nil
	}
	userContent := lastUserMessageContent(transcript)
	if !shouldExtractTaskCandidates(userContent) {
		return nil
	}
	_, err := rt.taskStore.ExtractAndQueue(ctx, peerID, userContent, tasks.CandidateProvenance{
		CaptureKind:      tasks.CandidateCaptureAutoTurnExtract,
		SourceSessionKey: rt.normalizeSessionKey(sessionKey),
		SourceExcerpt:    userContent,
	})
	return err
}

func (rt *Runtime) extractMemoryCandidates(ctx context.Context, model, summary string) ([]memoryCandidateSuggestion, error) {
	if strings.TrimSpace(summary) == "" {
		return nil, nil
	}
	temperature := 0.0
	resp, err := rt.prov.Complete(ctx, &types.ChatRequest{
		Model: model,
		Messages: []types.Message{
			{Role: "system", Content: memoryCandidateExtractorPrompt},
			{Role: "user", Content: "Completed conversation turn:\n\n" + summary},
		},
		Stream:      false,
		MaxTokens:   300,
		Temperature: &temperature,
	})
	if err != nil {
		return nil, err
	}
	if resp == nil || len(resp.Choices) == 0 {
		return nil, nil
	}
	return parseMemoryCandidateExtraction(resp.Choices[0].Message.Content)
}

func (rt *Runtime) upsertTurnEntities(ctx context.Context, peerID, sessionKey, model string, transcript []types.Message, summary string, chunkID string) error {
	if rt == nil || rt.prov == nil || rt.memStore == nil {
		return nil
	}
	userContent := lastUserMessageContent(transcript)
	if !shouldExtractMemoryCandidates(userContent) {
		return nil
	}
	entities, err := rt.extractEntityCandidates(ctx, model, summary)
	if err != nil {
		return err
	}
	for _, suggestion := range entities {
		entity, err := rt.memStore.UpsertEntity(ctx, peerID, memory.EntityKind(suggestion.Kind), suggestion.Name, suggestion.Aliases, suggestion.Notes, time.Now().Unix())
		if err != nil {
			slog.Warn("agent: entity upsert failed", "peer", peerID, "name", suggestion.Name, "err", err)
			continue
		}
		if strings.TrimSpace(chunkID) != "" {
			if err := rt.memStore.LinkChunkToEntity(ctx, peerID, entity.ID, chunkID); err != nil {
				slog.Warn("agent: entity chunk link failed", "peer", peerID, "entity", entity.ID, "chunk", chunkID, "err", err)
			}
		}
	}
	return nil
}

func (rt *Runtime) extractEntityCandidates(ctx context.Context, model, summary string) ([]entitySuggestion, error) {
	if strings.TrimSpace(summary) == "" {
		return nil, nil
	}
	temperature := 0.0
	resp, err := rt.prov.Complete(ctx, &types.ChatRequest{
		Model: model,
		Messages: []types.Message{
			{Role: "system", Content: entityExtractorPrompt},
			{Role: "user", Content: "Completed conversation turn:\n\n" + summary},
		},
		Stream:      false,
		MaxTokens:   300,
		Temperature: &temperature,
	})
	if err != nil {
		return nil, err
	}
	if resp == nil || len(resp.Choices) == 0 {
		return nil, nil
	}
	entities, err := parseEntityExtraction(resp.Choices[0].Message.Content)
	if err != nil {
		return nil, nil
	}
	return entities, nil
}

func parseMemoryCandidateExtraction(raw string) ([]memoryCandidateSuggestion, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	if strings.HasPrefix(raw, "```") {
		raw = strings.TrimPrefix(raw, "```json")
		raw = strings.TrimPrefix(raw, "```")
		raw = strings.TrimSuffix(strings.TrimSpace(raw), "```")
	}
	start := strings.Index(raw, "{")
	end := strings.LastIndex(raw, "}")
	if start >= 0 && end > start {
		raw = raw[start : end+1]
	}
	var payload memoryCandidateExtraction
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		return nil, err
	}
	if len(payload.Candidates) > 2 {
		payload.Candidates = payload.Candidates[:2]
	}
	return payload.Candidates, nil
}

func parseEntityExtraction(raw string) ([]entitySuggestion, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	if strings.HasPrefix(raw, "```") {
		raw = strings.TrimPrefix(raw, "```json")
		raw = strings.TrimPrefix(raw, "```")
		raw = strings.TrimSuffix(strings.TrimSpace(raw), "```")
	}
	start := strings.Index(raw, "{")
	end := strings.LastIndex(raw, "}")
	if start >= 0 && end > start {
		raw = raw[start : end+1]
	}
	var payload entityExtraction
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		return nil, err
	}
	if len(payload.Entities) > 4 {
		payload.Entities = payload.Entities[:4]
	}
	filtered := make([]entitySuggestion, 0, len(payload.Entities))
	seen := make(map[string]struct{}, len(payload.Entities))
	for _, suggestion := range payload.Entities {
		kind := strings.TrimSpace(suggestion.Kind)
		name := strings.TrimSpace(suggestion.Name)
		if kind == "" || name == "" {
			continue
		}
		key := strings.ToLower(kind + "::" + name)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		filtered = append(filtered, entitySuggestion{
			Kind:    kind,
			Name:    name,
			Aliases: suggestion.Aliases,
			Notes:   suggestion.Notes,
		})
	}
	return filtered, nil
}

func (rt *Runtime) existingMemoryContentSet(ctx context.Context, peerID string) (map[string]struct{}, error) {
	seen := make(map[string]struct{})
	candidates, err := rt.memStore.ListCandidates(ctx, peerID, 100, memory.CandidateStatusAll)
	if err != nil {
		return nil, err
	}
	for _, candidate := range candidates {
		if normalized := normalizeMemoryCandidateContent(candidate.Content); normalized != "" {
			seen[normalized] = struct{}{}
		}
	}
	chunks, err := rt.memStore.List(ctx, peerID, 100)
	if err != nil {
		return nil, err
	}
	for _, chunk := range chunks {
		if normalized := normalizeMemoryCandidateContent(chunk.Content); normalized != "" {
			seen[normalized] = struct{}{}
		}
	}
	return seen, nil
}

func shouldExtractMemoryCandidates(userContent string) bool {
	trimmed := strings.TrimSpace(userContent)
	if trimmed == "" || len(trimmed) > 800 || strings.Contains(trimmed, "```") {
		return false
	}
	lower := " " + strings.ToLower(trimmed) + " "
	signals := []string{
		" remember ",
		" i ",
		" i'm ",
		" i’m ",
		" my ",
		" me ",
		" we ",
		" we're ",
		" we’re ",
		" our ",
		" us ",
		" prefer ",
		" preference ",
		" call me ",
		" keep in mind ",
		" note that ",
		" deadline ",
		" due ",
	}
	for _, signal := range signals {
		if strings.Contains(lower, signal) {
			return true
		}
	}
	return false
}

func shouldExtractTaskCandidates(userContent string) bool {
	trimmed := strings.TrimSpace(userContent)
	if trimmed == "" || len(trimmed) > 800 || strings.Contains(trimmed, "```") {
		return false
	}
	lower := strings.ToLower(trimmed)
	signals := []string{
		"todo",
		"task",
		"remember to",
		"need to",
		"should ",
		"must ",
		"follow up",
		"please ",
		"[ ]",
	}
	for _, signal := range signals {
		if strings.Contains(lower, signal) {
			return true
		}
	}
	return false
}

func normalizeSuggestionRetention(value string) memory.RetentionClass {
	switch strings.TrimSpace(strings.ToLower(value)) {
	case string(memory.RetentionClassPinned):
		return memory.RetentionClassPinned
	default:
		return memory.RetentionClassWorking
	}
}

func normalizeMemoryCandidateContent(content string) string {
	content = strings.TrimSpace(content)
	if content == "" {
		return ""
	}
	return strings.ToLower(strings.Join(strings.Fields(content), " "))
}

func minDuration(a, b time.Duration) time.Duration {
	if a <= 0 || a > b {
		return b
	}
	return a
}

func (rt *Runtime) sessionKey(req RunRequest) string {
	if req.SessionKey != "" {
		return req.SessionKey
	}
	switch req.Scope {
	case ScopeGlobal:
		return rt.globalKey
	case ScopeIsolated:
		return fmt.Sprintf("%s::isolated::%s", req.PeerID, uuid.NewString())
	case ScopeDirect:
		if req.SenderID != "" {
			return fmt.Sprintf("%s::sender::%s", req.PeerID, req.SenderID)
		}
		return req.PeerID + "::direct"
	case ScopeMain, "":
		return req.PeerID + "::main"
	default:
		return req.PeerID + "::" + string(req.Scope)
	}
}

func (rt *Runtime) backoff(attempt int) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	d := rt.retry.InitialBackoff << (attempt - 1)
	if d > rt.retry.MaxBackoff {
		return rt.retry.MaxBackoff
	}
	return d
}

func (rt *Runtime) shouldRetry(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	for _, code := range rt.retry.StatusCodes {
		if containsHTTPStatusCode(msg, code) {
			return true
		}
	}
	retryable := []string{"too many requests", "rate limit", "timeout", "network", "connection", "overloaded", "temporary"}
	for _, part := range retryable {
		if strings.Contains(msg, part) {
			return true
		}
	}
	return false
}

func containsHTTPStatusCode(msg string, code int) bool {
	target := strconv.Itoa(code)
	fields := strings.FieldsFunc(msg, func(r rune) bool {
		return r < '0' || r > '9'
	})
	for _, field := range fields {
		if field == target {
			return true
		}
	}
	return false
}

const (
	toolCallStartTag = "<tool_call>"
	toolCallEndTag   = "</tool_call>"
)

func parseToolCall(text string) (*ToolCall, bool, error) {
	start := strings.Index(text, toolCallStartTag)
	if start < 0 {
		return nil, false, nil
	}
	end := strings.Index(text, toolCallEndTag)
	if end < 0 || end < start {
		return nil, false, fmt.Errorf("malformed tool call response")
	}
	payload := strings.TrimSpace(text[start+len(toolCallStartTag) : end])
	var call ToolCall
	if err := json.Unmarshal([]byte(payload), &call); err != nil {
		return nil, false, fmt.Errorf("invalid tool call JSON: %w", err)
	}
	if strings.TrimSpace(call.Name) == "" {
		return nil, false, fmt.Errorf("tool call name is required")
	}
	if len(call.Arguments) == 0 {
		call.Arguments = json.RawMessage(`{}`)
	}
	return &call, true, nil
}

func formatToolResult(name string, result any, err error) string {
	encoded := formatToolResultBody(result, err)
	return fmt.Sprintf("[tool_result %s]\n%s", name, encoded)
}

func formatToolResultBody(result any, err error) string {
	body := map[string]any{"ok": err == nil}
	if err != nil {
		body["error"] = map[string]any{"message": redact.Error(err)}
	} else {
		body["result"] = redact.Value(result)
	}
	encoded, marshalErr := json.Marshal(body)
	if marshalErr != nil {
		return fmt.Sprintf("{\"ok\":false,\"error\":{\"message\":%q}}", redact.String(marshalErr.Error()))
	}
	return string(encoded)
}

func nativeToolCalls(resp *types.ChatResponse) []ToolCall {
	if resp == nil || len(resp.Choices) == 0 {
		return nil
	}
	msg := resp.Choices[0].Message
	if len(msg.ToolCalls) == 0 {
		return nil
	}
	out := make([]ToolCall, 0, len(msg.ToolCalls))
	for _, tc := range msg.ToolCalls {
		out = append(out, ToolCall{
			Name:      tc.Function.Name,
			Arguments: json.RawMessage(tc.Function.Arguments),
			ID:        tc.ID,
		})
	}
	return out
}

func normalizeToolName(exec ToolExecutor, peerID, name string) string {
	if exec == nil {
		return name
	}
	norm, ok := exec.(toolNameNormalizer)
	if !ok {
		return name
	}
	return norm.NormalizeToolName(peerID, name)
}

func providerCapabilitiesFor(prov Provider, model string) types.ProviderCapabilities {
	if caps, ok := prov.(providerCapabilities); ok {
		return caps.Capabilities(model)
	}
	return types.ProviderCapabilities{}
}

func suppressSilentReply(assistantText string, resp *types.ChatResponse) (string, *types.ChatResponse, bool, string) {
	if resp != nil && len(resp.Choices) > 0 && len(resp.Choices[0].Message.ToolCalls) > 0 {
		return assistantText, resp, false, ""
	}
	suppressed, token := DetectSilentReply(assistantText)
	if !suppressed {
		return assistantText, resp, false, ""
	}
	assistantText = ""
	if resp != nil && len(resp.Choices) > 0 {
		resp = cloneResponse(resp)
		resp.Choices[0].Message.Content = ""
	}
	return assistantText, resp, true, token
}

func cloneResponse(resp *types.ChatResponse) *types.ChatResponse {
	if resp == nil {
		return nil
	}
	cp := *resp
	if len(resp.Choices) > 0 {
		cp.Choices = append([]types.ChatChoice(nil), resp.Choices...)
	}
	return &cp
}

func summarizeToolJSON(raw json.RawMessage) string {
	if len(bytes.TrimSpace(raw)) == 0 {
		return "{}"
	}
	var decoded any
	if err := json.Unmarshal(raw, &decoded); err == nil {
		switch value := decoded.(type) {
		case map[string]any:
			if len(value) == 0 {
				return "{}"
			}
		}
		if out, err := json.Marshal(redact.Value(decoded)); err == nil {
			return truncateSummary(string(out), 320)
		}
	}
	return truncateSummary(redact.String(string(raw)), 320)
}

func summarizeToolResult(result any, err error) string {
	return truncateSummary(formatToolResultBody(result, err), 480)
}

func truncateSummary(text string, limit int) string {
	text = strings.TrimSpace(text)
	if limit <= 0 || len(text) <= limit {
		return text
	}
	return text[:limit-3] + "..."
}

func (rt *Runtime) emitEvent(result *Result, sink func(Event), ev Event) {
	result.Events = append(result.Events, ev)
	if sink != nil {
		sink(ev)
	}
}

func (rt *Runtime) emitHook(ctx context.Context, ev ops.Event) error {
	if rt == nil || rt.hooks == nil {
		return nil
	}
	return rt.hooks.Emit(ctx, ev)
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// captureResponseWriter buffers SSE output for retryable streaming calls.
type captureResponseWriter struct {
	header      http.Header
	status      int
	buf         bytes.Buffer
	mu          sync.Mutex
	downstream  http.ResponseWriter
	headersSent bool
	liveEmitted bool
}

func (w *captureResponseWriter) Header() http.Header {
	return w.header
}

func (w *captureResponseWriter) WriteHeader(statusCode int) {
	if w.status == 0 {
		w.status = statusCode
	}
	if w.downstream != nil && !w.headersSent {
		copyHeaders(w.downstream.Header(), w.header)
		w.downstream.WriteHeader(statusCode)
		w.headersSent = true
	}
}

func (w *captureResponseWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if _, err := w.buf.Write(p); err != nil {
		return 0, err
	}
	if w.downstream != nil {
		if !w.headersSent {
			copyHeaders(w.downstream.Header(), w.header)
			w.headersSent = true
		}
		if _, err := w.downstream.Write(p); err != nil {
			return 0, err
		}
		if len(p) > 0 {
			w.liveEmitted = true
		}
	}
	return len(p), nil
}

func (w *captureResponseWriter) Flush() {
	if flusher, ok := w.downstream.(http.Flusher); ok {
		flusher.Flush()
	}
}

func (w *captureResponseWriter) reset() {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.header = make(http.Header)
	w.status = 0
	w.buf.Reset()
	w.headersSent = false
	w.liveEmitted = false
}

func (w *captureResponseWriter) liveWritten() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.liveEmitted
}

func copyHeaders(dst, src http.Header) {
	for k, values := range src {
		dst.Del(k)
		for _, value := range values {
			dst.Add(k, value)
		}
	}
}
