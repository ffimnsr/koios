package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/ffimnsr/koios/internal/memory"
	"github.com/ffimnsr/koios/internal/ops"
	"github.com/ffimnsr/koios/internal/requestctx"
	"github.com/ffimnsr/koios/internal/session"
	"github.com/ffimnsr/koios/internal/standing"
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
)

// RetryPolicy controls retry behavior for provider failures.
type RetryPolicy struct {
	MaxAttempts    int
	InitialBackoff time.Duration
	MaxBackoff     time.Duration
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
}

// Result contains the outcome of a run.
type Result struct {
	SessionKey    string              `json:"session_key"`
	Attempts      int                 `json:"attempts"`
	AssistantText string              `json:"assistant_text,omitempty"`
	Response      *types.ChatResponse `json:"response,omitempty"`
	Steps         int                 `json:"steps"`
	Events        []Event             `json:"events,omitempty"`
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
	memTopK           int
	memInject         bool
	pruneToolMessages int
	standingManager   *standing.Manager
	hooks             *ops.Manager
	identityDir       string

	steeringMu     sync.Mutex
	steeringQueues map[string][]string // sessionKey → pending user messages
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
	if sessionKey == "" {
		return fmt.Errorf("session_key is required")
	}
	if message == "" {
		return fmt.Errorf("message is required")
	}
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
	return nil
}

// drainSteering returns and removes any queued steering messages for sessionKey.
func (rt *Runtime) drainSteering(sessionKey string) []string {
	rt.steeringMu.Lock()
	defer rt.steeringMu.Unlock()
	msgs := rt.steeringQueues[sessionKey]
	if len(msgs) == 0 {
		return nil
	}
	delete(rt.steeringQueues, sessionKey)
	return msgs
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
	buf := &captureResponseWriter{header: make(http.Header)}
	res, err := rt.run(ctx, req, buf)
	if err != nil {
		return res, err
	}
	buf.replayTo(w)
	return res, nil
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
		if reqCopy.ToolExecutor != nil {
			reqCopy.MaxSteps = 4
		} else {
			reqCopy.MaxSteps = 1
		}
	}
	callCtx, cancel := context.WithTimeout(ctx, reqCopy.Timeout)
	defer cancel()
	workingMessages := append([]types.Message(nil), reqCopy.Messages...)

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
			built.Request.Tools = reqCopy.ToolExecutor.ToolDefinitions(reqCopy.PeerID)
			if len(built.Request.Tools) > 0 {
				built.Request.ToolChoice = "auto"
			}
			// Tool-driven runs use non-streaming model calls for intermediate steps.
			built.Request.Stream = false
		}
		built.Request.MaxTokens = reqCopy.MaxTokens
		built.Request.Temperature = reqCopy.Temperature
		built.Request.TopP = reqCopy.TopP
		advanceStep := false
		for attempt := 1; attempt <= rt.retry.MaxAttempts; attempt++ {
			result.Attempts = attempt
			attemptCtx, attemptCancel := context.WithCancel(callCtx)
			if sink != nil {
				sink.reset()
			}
			assistantText, resp, err := rt.invoke(attemptCtx, built.Request, built.Request.Stream, sink)
			attemptCancel()
			if err == nil {
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
							rt.emitEvent(result, reqCopy.EventSink, Event{Kind: EventToolCall, SessionKey: sessionKey, Step: step, Message: tc.Name})
							toolResult, execErr := reqCopy.ToolExecutor.ExecuteTool(attemptCtx, reqCopy.PeerID, tc)
							rt.emitEvent(result, reqCopy.EventSink, Event{Kind: EventToolResult, SessionKey: sessionKey, Step: step, Message: tc.Name})
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
						rt.emitEvent(result, reqCopy.EventSink, Event{Kind: EventToolCall, SessionKey: sessionKey, Step: step, Message: call.Name})
						toolResult, execErr := reqCopy.ToolExecutor.ExecuteTool(attemptCtx, reqCopy.PeerID, *call)
						rt.emitEvent(result, reqCopy.EventSink, Event{Kind: EventToolResult, SessionKey: sessionKey, Step: step, Message: call.Name})
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
							types.Message{Role: "user", Content: formatToolResult(call.Name, toolResult, execErr)},
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
				if reqCopy.Stream && sink != nil {
					rt.emitEvent(result, reqCopy.EventSink, Event{Kind: EventRunBlock, SessionKey: sessionKey, Step: step, Message: assistantText})
				}
				rt.persistTurn(sessionKey, reqCopy.Messages, assistantText, resp)
				rt.emitEvent(result, reqCopy.EventSink, Event{Kind: EventRunFinish, SessionKey: sessionKey, Step: step, Message: assistantText})
				return result, nil
			}
			lastErr = err
			rt.emitEvent(result, reqCopy.EventSink, Event{Kind: EventRunError, SessionKey: sessionKey, Attempt: attempt, Step: step, Error: err.Error()})
			if attempt < rt.retry.MaxAttempts && shouldRetry(err) {
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
			if len(resp.Choices) == 0 {
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

func (rt *Runtime) persistTurn(sessionKey string, userMsgs []types.Message, assistantText string, resp *types.ChatResponse) {
	if len(userMsgs) > 0 {
		rt.store.AppendCtx(context.Background(), sessionKey, userMsgs...)
	}
	if assistantText != "" {
		rt.store.Append(sessionKey, types.Message{Role: "assistant", Content: assistantText})
		return
	}
	if resp != nil && len(resp.Choices) > 0 {
		rt.store.Append(sessionKey, resp.Choices[0].Message)
	}
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

func shouldRetry(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	retryable := []string{"429", "too many requests", "rate limit", "timeout", "network", "connection", "503", "502", "500", "overloaded", "temporary"}
	for _, part := range retryable {
		if strings.Contains(msg, part) {
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
	body := map[string]any{"ok": err == nil}
	if err != nil {
		body["error"] = map[string]any{"message": err.Error()}
	} else {
		body["result"] = result
	}
	encoded, marshalErr := json.Marshal(body)
	if marshalErr != nil {
		return fmt.Sprintf("[tool_result %s]\n{\"ok\":false,\"error\":{\"message\":%q}}", name, marshalErr.Error())
	}
	return fmt.Sprintf("[tool_result %s]\n%s", name, encoded)
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
	header http.Header
	status int
	buf    bytes.Buffer
	mu     sync.Mutex
}

func (w *captureResponseWriter) Header() http.Header {
	return w.header
}

func (w *captureResponseWriter) WriteHeader(statusCode int) {
	if w.status == 0 {
		w.status = statusCode
	}
}

func (w *captureResponseWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buf.Write(p)
}

func (w *captureResponseWriter) Flush() {}

func (w *captureResponseWriter) reset() {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.header = make(http.Header)
	w.status = 0
	w.buf.Reset()
}

func (w *captureResponseWriter) replayTo(dst http.ResponseWriter) {
	for k, values := range w.header {
		for _, value := range values {
			dst.Header().Add(k, value)
		}
	}
	if w.status != 0 {
		dst.WriteHeader(w.status)
	}
	_, _ = dst.Write(w.buf.Bytes())
	if flusher, ok := dst.(http.Flusher); ok {
		flusher.Flush()
	}
}
