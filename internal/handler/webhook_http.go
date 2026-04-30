package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/ffimnsr/koios/internal/agent"
	"github.com/ffimnsr/koios/internal/config"
	"github.com/ffimnsr/koios/internal/ops"
	"github.com/ffimnsr/koios/internal/presence"
	"github.com/ffimnsr/koios/internal/scheduler"
	"github.com/ffimnsr/koios/internal/session"
	"github.com/ffimnsr/koios/internal/types"
)

var hookTemplatePattern = regexp.MustCompile(`\{\{\s*([^{}]+?)\s*\}\}`)

// WebhookHTTPHandler accepts authenticated external trigger events.
type WebhookHTTPHandler struct {
	store      *session.Store
	hooks      *ops.Manager
	presence   *presence.Manager
	jobStore   *scheduler.JobStore
	sched      *scheduler.Scheduler
	token      string
	mappings   []config.HookMapping
	agentCoord *agent.Coordinator
	toolExec   agent.ToolExecutor
}

func NewWebhookHTTPHandler(store *session.Store, hooks *ops.Manager, p *presence.Manager, jobStore *scheduler.JobStore, sched *scheduler.Scheduler, token string, mappings []config.HookMapping) *WebhookHTTPHandler {
	return &WebhookHTTPHandler{
		store:    store,
		hooks:    hooks,
		presence: p,
		jobStore: jobStore,
		sched:    sched,
		token:    strings.TrimSpace(token),
		mappings: prepareHookMappings(mappings),
	}
}

// SetAgentCoordinator wires in the agent coordinator and tool executor so that
// the "agent.run" webhook event type can dispatch isolated agent runs directly.
func (h *WebhookHTTPHandler) SetAgentCoordinator(coord *agent.Coordinator, exec agent.ToolExecutor) {
	h.agentCoord = coord
	h.toolExec = exec
}

type webhookEventRequest struct {
	Type           string              `json:"type"`
	PeerID         string              `json:"peer_id"`
	Source         string              `json:"source,omitempty"`
	Message        *types.Message      `json:"message,omitempty"`
	Status         string              `json:"status,omitempty"`
	Typing         *bool               `json:"typing,omitempty"`
	JobID          string              `json:"job_id,omitempty"`
	Name           string              `json:"name,omitempty"`
	Schedule       *scheduler.Schedule `json:"schedule,omitempty"`
	Payload        *scheduler.Payload  `json:"payload,omitempty"`
	Enabled        *bool               `json:"enabled,omitempty"`
	DeleteAfterRun *bool               `json:"delete_after_run,omitempty"`
	Description    string              `json:"description,omitempty"`
	// agent.run fields
	Prompt         string `json:"prompt,omitempty"`
	Model          string `json:"model,omitempty"`
	Profile        string `json:"profile,omitempty"`
	TimeoutSeconds int    `json:"timeout_seconds,omitempty"`
}

type hookTransformContext struct {
	Mapping     config.HookMapping
	Method      string
	RequestPath string
	Headers     map[string]any
	Query       map[string]any
	Body        any
	BodyRaw     string
}

func (h *WebhookHTTPHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "use POST", http.StatusMethodNotAllowed)
		return
	}
	if h == nil || h.store == nil {
		http.Error(w, "webhooks unavailable", http.StatusServiceUnavailable)
		return
	}
	if h.token == "" {
		http.Error(w, "webhooks disabled", http.StatusNotFound)
		return
	}
	if !webhookAuthorized(r, h.token) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if hookName := strings.TrimSpace(r.PathValue("name")); hookName != "" {
		h.serveMappedHook(w, r, hookName)
		return
	}
	h.serveEventHook(w, r)
}

func (h *WebhookHTTPHandler) serveEventHook(w http.ResponseWriter, r *http.Request) {
	var req webhookEventRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON body", http.StatusBadRequest)
		return
	}
	if !IsValidPeerID(req.PeerID) {
		http.Error(w, "invalid peer_id", http.StatusBadRequest)
		return
	}
	runID, err := h.apply(req)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	resp := map[string]any{"ok": true}
	if runID != "" {
		resp["run_id"] = runID
	}
	_ = json.NewEncoder(w).Encode(resp)
}

func (h *WebhookHTTPHandler) serveMappedHook(w http.ResponseWriter, r *http.Request, hookPath string) {
	mapping, ok := h.findHookMapping(hookPath)
	if !ok {
		http.Error(w, "hook not found", http.StatusNotFound)
		return
	}
	bodyRaw, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "failed to read request body", http.StatusBadRequest)
		return
	}
	var body any
	if trimmed := bytes.TrimSpace(bodyRaw); len(trimmed) > 0 {
		_ = json.Unmarshal(trimmed, &body)
	}
	req, err := h.buildMappedRequest(mapping, r, body, string(bodyRaw))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if !IsValidPeerID(req.PeerID) {
		http.Error(w, "invalid peer_id", http.StatusBadRequest)
		return
	}
	runID, err := h.apply(req)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	resp := map[string]any{"ok": true, "hook": mapping.Name}
	if runID != "" {
		resp["run_id"] = runID
	}
	_ = json.NewEncoder(w).Encode(resp)
}

func prepareHookMappings(mappings []config.HookMapping) []config.HookMapping {
	if len(mappings) == 0 {
		return nil
	}
	prepared := make([]config.HookMapping, 0, len(mappings))
	for _, mapping := range mappings {
		if mapping.Enabled != nil && !*mapping.Enabled {
			continue
		}
		mapping.Name = strings.TrimSpace(mapping.Name)
		mapping.Path = strings.ToLower(strings.Trim(strings.TrimSpace(mapping.Path), "/"))
		if mapping.Path == "" {
			mapping.Path = strings.ToLower(strings.Trim(strings.TrimSpace(mapping.Name), "/"))
		}
		mapping.Type = strings.TrimSpace(mapping.Type)
		prepared = append(prepared, mapping)
	}
	return prepared
}

func (h *WebhookHTTPHandler) findHookMapping(path string) (config.HookMapping, bool) {
	needle := strings.ToLower(strings.Trim(strings.TrimSpace(path), "/"))
	for _, mapping := range h.mappings {
		if strings.EqualFold(mapping.Path, needle) {
			return mapping, true
		}
	}
	return config.HookMapping{}, false
}

func (h *WebhookHTTPHandler) buildMappedRequest(mapping config.HookMapping, r *http.Request, body any, bodyRaw string) (webhookEventRequest, error) {
	req := webhookEventRequest{
		Type:   strings.TrimSpace(mapping.Type),
		Source: "hook:" + hookFirstNonEmpty(mapping.Path, mapping.Name),
	}
	ctx := hookTransformContext{
		Mapping:     mapping,
		Method:      r.Method,
		RequestPath: r.URL.Path,
		Headers:     requestHeadersAsMap(r),
		Query:       requestQueryAsMap(r),
		Body:        body,
		BodyRaw:     bodyRaw,
	}
	for _, field := range mapping.Fields {
		value, ok, err := resolveHookFieldValue(ctx, field)
		if err != nil {
			return req, fmt.Errorf("hook %q field %q: %w", mapping.Name, field.To, err)
		}
		if !ok {
			continue
		}
		if err := applyHookFieldValue(&req, field.To, value); err != nil {
			return req, fmt.Errorf("hook %q field %q: %w", mapping.Name, field.To, err)
		}
	}
	if strings.TrimSpace(req.Source) == "" {
		req.Source = "hook:" + hookFirstNonEmpty(mapping.Path, mapping.Name)
	}
	return req, nil
}

func resolveHookFieldValue(ctx hookTransformContext, field config.HookFieldTransform) (any, bool, error) {
	required := field.Required != nil && *field.Required
	if strings.TrimSpace(field.From) != "" {
		value, ok := lookupHookSource(ctx, field.From)
		if !ok {
			if required {
				return nil, false, fmt.Errorf("required source %q not found", field.From)
			}
			return nil, false, nil
		}
		return value, true, nil
	}
	if strings.TrimSpace(field.Template) != "" {
		value := renderHookTemplate(ctx, field.Template)
		if required && strings.TrimSpace(value) == "" {
			return nil, false, fmt.Errorf("required template %q rendered empty", field.Template)
		}
		return value, true, nil
	}
	if field.Value != "" {
		return field.Value, true, nil
	}
	if required {
		return nil, false, fmt.Errorf("required field has no source")
	}
	return nil, false, nil
}

func requestHeadersAsMap(r *http.Request) map[string]any {
	values := make(map[string]any, len(r.Header))
	for key, entries := range r.Header {
		normalized := strings.ToLower(strings.TrimSpace(key))
		if len(entries) == 1 {
			values[normalized] = entries[0]
			continue
		}
		copied := make([]string, 0, len(entries))
		copied = append(copied, entries...)
		values[normalized] = copied
	}
	return values
}

func requestQueryAsMap(r *http.Request) map[string]any {
	query := r.URL.Query()
	values := make(map[string]any, len(query))
	for key, entries := range query {
		normalized := strings.ToLower(strings.TrimSpace(key))
		if len(entries) == 1 {
			values[normalized] = entries[0]
			continue
		}
		copied := make([]string, 0, len(entries))
		copied = append(copied, entries...)
		values[normalized] = copied
	}
	return values
}

func lookupHookSource(ctx hookTransformContext, path string) (any, bool) {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil, false
	}
	parts := strings.Split(path, ".")
	root := strings.ToLower(strings.TrimSpace(parts[0]))
	var current any
	switch root {
	case "body":
		current = ctx.Body
	case "headers":
		current = ctx.Headers
	case "query":
		current = ctx.Query
	case "hook":
		current = map[string]any{
			"name": ctx.Mapping.Name,
			"path": ctx.Mapping.Path,
			"type": ctx.Mapping.Type,
		}
	case "request":
		current = map[string]any{
			"method":   ctx.Method,
			"path":     ctx.RequestPath,
			"body_raw": ctx.BodyRaw,
		}
	default:
		return nil, false
	}
	if len(parts) == 1 {
		return current, current != nil
	}
	return lookupNestedValue(current, parts[1:])
}

func lookupNestedValue(current any, parts []string) (any, bool) {
	value := current
	for _, rawPart := range parts {
		part := strings.TrimSpace(rawPart)
		switch typed := value.(type) {
		case map[string]any:
			next, ok := typed[part]
			if !ok {
				next, ok = typed[strings.ToLower(part)]
				if !ok {
					return nil, false
				}
			}
			value = next
		case []any:
			idx, err := strconv.Atoi(part)
			if err != nil || idx < 0 || idx >= len(typed) {
				return nil, false
			}
			value = typed[idx]
		case []string:
			idx, err := strconv.Atoi(part)
			if err != nil || idx < 0 || idx >= len(typed) {
				return nil, false
			}
			value = typed[idx]
		default:
			return nil, false
		}
	}
	return value, true
}

func renderHookTemplate(ctx hookTransformContext, template string) string {
	return hookTemplatePattern.ReplaceAllStringFunc(template, func(match string) string {
		groups := hookTemplatePattern.FindStringSubmatch(match)
		if len(groups) < 2 {
			return ""
		}
		value, ok := lookupHookSource(ctx, groups[1])
		if !ok {
			return ""
		}
		return hookValueToString(value)
	})
}

func hookValueToString(value any) string {
	switch typed := value.(type) {
	case nil:
		return ""
	case string:
		return typed
	case bool:
		return strconv.FormatBool(typed)
	case int:
		return strconv.Itoa(typed)
	case int64:
		return strconv.FormatInt(typed, 10)
	case float64:
		return strconv.FormatFloat(typed, 'f', -1, 64)
	case []string:
		return strings.Join(typed, ",")
	default:
		data, err := json.Marshal(typed)
		if err != nil {
			return fmt.Sprint(typed)
		}
		return string(data)
	}
}

func applyHookFieldValue(req *webhookEventRequest, target string, value any) error {
	switch strings.ToLower(strings.TrimSpace(target)) {
	case "peer_id":
		req.PeerID = hookValueToString(value)
	case "source":
		req.Source = hookValueToString(value)
	case "prompt":
		req.Prompt = hookValueToString(value)
	case "model":
		req.Model = hookValueToString(value)
	case "profile":
		req.Profile = hookValueToString(value)
	case "timeout_seconds":
		n, err := hookValueToInt(value)
		if err != nil {
			return err
		}
		req.TimeoutSeconds = int(n)
	case "status":
		req.Status = hookValueToString(value)
	case "typing":
		flag, err := hookValueToBool(value)
		if err != nil {
			return err
		}
		req.Typing = &flag
	case "job_id":
		req.JobID = hookValueToString(value)
	case "name":
		req.Name = hookValueToString(value)
	case "description":
		req.Description = hookValueToString(value)
	case "enabled":
		flag, err := hookValueToBool(value)
		if err != nil {
			return err
		}
		req.Enabled = &flag
	case "delete_after_run":
		flag, err := hookValueToBool(value)
		if err != nil {
			return err
		}
		req.DeleteAfterRun = &flag
	case "message.role":
		if req.Message == nil {
			req.Message = &types.Message{}
		}
		req.Message.Role = hookValueToString(value)
	case "message.content":
		if req.Message == nil {
			req.Message = &types.Message{}
		}
		req.Message.Content = hookValueToString(value)
	case "schedule.kind":
		if req.Schedule == nil {
			req.Schedule = &scheduler.Schedule{}
		}
		req.Schedule.Kind = scheduler.ScheduleKind(hookValueToString(value))
	case "schedule.at":
		if req.Schedule == nil {
			req.Schedule = &scheduler.Schedule{}
		}
		req.Schedule.At = hookValueToString(value)
	case "schedule.every_ms":
		if req.Schedule == nil {
			req.Schedule = &scheduler.Schedule{}
		}
		n, err := hookValueToInt(value)
		if err != nil {
			return err
		}
		req.Schedule.EveryMs = n
	case "schedule.expr":
		if req.Schedule == nil {
			req.Schedule = &scheduler.Schedule{}
		}
		req.Schedule.Expr = hookValueToString(value)
	case "schedule.tz":
		if req.Schedule == nil {
			req.Schedule = &scheduler.Schedule{}
		}
		req.Schedule.Tz = hookValueToString(value)
	case "schedule.stagger_ms":
		if req.Schedule == nil {
			req.Schedule = &scheduler.Schedule{}
		}
		n, err := hookValueToInt(value)
		if err != nil {
			return err
		}
		req.Schedule.StaggerMs = n
	case "payload.kind":
		if req.Payload == nil {
			req.Payload = &scheduler.Payload{}
		}
		req.Payload.Kind = scheduler.PayloadKind(hookValueToString(value))
	case "payload.text":
		if req.Payload == nil {
			req.Payload = &scheduler.Payload{}
		}
		req.Payload.Text = hookValueToString(value)
	case "payload.message":
		if req.Payload == nil {
			req.Payload = &scheduler.Payload{}
		}
		req.Payload.Message = hookValueToString(value)
	case "payload.include_history":
		if req.Payload == nil {
			req.Payload = &scheduler.Payload{}
		}
		flag, err := hookValueToBool(value)
		if err != nil {
			return err
		}
		req.Payload.IncludeHistory = flag
	case "payload.profile":
		if req.Payload == nil {
			req.Payload = &scheduler.Payload{}
		}
		req.Payload.Profile = hookValueToString(value)
	case "payload.preload_urls":
		if req.Payload == nil {
			req.Payload = &scheduler.Payload{}
		}
		urls, err := hookValueToStringSlice(value)
		if err != nil {
			return err
		}
		req.Payload.PreloadURLs = urls
	default:
		return fmt.Errorf("unsupported target %q", target)
	}
	return nil
}

func hookValueToInt(value any) (int64, error) {
	switch typed := value.(type) {
	case int:
		return int64(typed), nil
	case int64:
		return typed, nil
	case float64:
		return int64(typed), nil
	case json.Number:
		return typed.Int64()
	case string:
		trimmed := strings.TrimSpace(typed)
		if trimmed == "" {
			return 0, nil
		}
		return strconv.ParseInt(trimmed, 10, 64)
	default:
		return 0, fmt.Errorf("expected integer, got %T", value)
	}
}

func hookValueToBool(value any) (bool, error) {
	switch typed := value.(type) {
	case bool:
		return typed, nil
	case string:
		trimmed := strings.TrimSpace(typed)
		if trimmed == "" {
			return false, nil
		}
		return strconv.ParseBool(trimmed)
	default:
		return false, fmt.Errorf("expected bool, got %T", value)
	}
}

func hookValueToStringSlice(value any) ([]string, error) {
	switch typed := value.(type) {
	case nil:
		return nil, nil
	case string:
		trimmed := strings.TrimSpace(typed)
		if trimmed == "" {
			return nil, nil
		}
		return []string{trimmed}, nil
	case []string:
		copied := make([]string, 0, len(typed))
		copied = append(copied, typed...)
		return copied, nil
	case []any:
		out := make([]string, 0, len(typed))
		for _, item := range typed {
			out = append(out, hookValueToString(item))
		}
		return out, nil
	default:
		return nil, fmt.Errorf("expected string array, got %T", value)
	}
}

func hookFirstNonEmpty(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func (h *WebhookHTTPHandler) apply(req webhookEventRequest) (string, error) {
	source := strings.TrimSpace(req.Source)
	if source == "" {
		source = "webhook"
	}
	switch strings.TrimSpace(req.Type) {
	case "message.append":
		if req.Message == nil {
			return "", errors.New("message is required for message.append")
		}
		msg := *req.Message
		if strings.TrimSpace(msg.Role) == "" {
			return "", errors.New("message.role is required")
		}
		if h.hooks != nil {
			if err := h.hooks.Emit(context.Background(), ops.Event{
				Name:   ops.HookBeforeMessage,
				PeerID: req.PeerID,
				Data: map[string]any{
					"method": "webhook.message.append",
					"source": source,
				},
			}); err != nil {
				return "", err
			}
		}
		h.store.AppendWithSource(req.PeerID, source, msg)
		if h.hooks != nil {
			_ = h.hooks.Emit(context.Background(), ops.Event{
				Name:   ops.HookMessageReceived,
				PeerID: req.PeerID,
				Data: map[string]any{
					"method": "webhook.message.append",
					"source": source,
				},
			})
			_ = h.hooks.Emit(context.Background(), ops.Event{
				Name:   ops.HookAfterMessage,
				PeerID: req.PeerID,
				Data: map[string]any{
					"method": "webhook.message.append",
					"source": source,
				},
			})
		}
		return "", nil
	case "presence.set":
		if h.presence == nil {
			return "", errors.New("presence is not enabled")
		}
		typing := false
		if req.Typing != nil {
			typing = *req.Typing
		}
		h.presence.Set(req.PeerID, req.Status, typing, source)
		return "", nil
	case "cron.trigger":
		if h.sched == nil || h.jobStore == nil {
			return "", errors.New("cron is not enabled")
		}
		if strings.TrimSpace(req.JobID) == "" {
			return "", errors.New("job_id is required for cron.trigger")
		}
		job := h.jobStore.Get(req.JobID)
		if job == nil || job.PeerID != req.PeerID {
			return "", errors.New("job not found")
		}
		_, err := h.sched.TriggerRun(req.JobID)
		return "", err
	case "cron.schedule":
		if h.sched == nil || h.jobStore == nil {
			return "", errors.New("cron is not enabled")
		}
		if strings.TrimSpace(req.Name) == "" {
			req.Name = "webhook-scheduled"
		}
		if req.Schedule == nil {
			now := time.Now().UTC().Format(time.RFC3339)
			req.Schedule = &scheduler.Schedule{Kind: scheduler.KindAt, At: now}
		}
		if req.Payload == nil {
			return "", errors.New("payload is required for cron.schedule")
		}
		tmp := &Handler{jobStore: h.jobStore, sched: h.sched}
		_, err := tmp.createCronJob(req.PeerID, cronCreateParams{
			Name:           req.Name,
			Description:    req.Description,
			Schedule:       *req.Schedule,
			Payload:        *req.Payload,
			Enabled:        req.Enabled,
			DeleteAfterRun: req.DeleteAfterRun,
		})
		return "", err
	case "agent.run":
		if h.agentCoord == nil {
			return "", errors.New("agent coordinator is not available for agent.run")
		}
		prompt := strings.TrimSpace(req.Prompt)
		if prompt == "" {
			return "", errors.New("prompt is required for agent.run")
		}
		timeout := time.Duration(req.TimeoutSeconds) * time.Second
		if timeout <= 0 {
			timeout = 5 * time.Minute
		}
		runReq := agent.RunRequest{
			PeerID:        req.PeerID,
			Scope:         agent.ScopeIsolated,
			Messages:      []types.Message{{Role: "user", Content: prompt}},
			ToolExecutor:  h.toolExec,
			Model:         strings.TrimSpace(req.Model),
			ActiveProfile: strings.TrimSpace(req.Profile),
			Timeout:       timeout,
		}
		rec, err := h.agentCoord.Start(runReq)
		if err != nil {
			return "", fmt.Errorf("agent.run: %w", err)
		}
		return rec.ID, nil
	case "session.wake":
		// session.wake dispatches a non-isolated agent run against the main
		// peer session so that external events (cron callbacks, incoming
		// notifications, etc.) can wake the primary context and inject a
		// message that persists in the session history.
		if h.agentCoord == nil {
			return "", errors.New("agent coordinator is not available for session.wake")
		}
		prompt := strings.TrimSpace(req.Prompt)
		if prompt == "" {
			return "", errors.New("prompt is required for session.wake")
		}
		timeout := time.Duration(req.TimeoutSeconds) * time.Second
		if timeout <= 0 {
			timeout = 5 * time.Minute
		}
		runReq := agent.RunRequest{
			PeerID:        req.PeerID,
			Scope:         agent.ScopeMain,
			Messages:      []types.Message{{Role: "user", Content: prompt}},
			ToolExecutor:  h.toolExec,
			Model:         strings.TrimSpace(req.Model),
			ActiveProfile: strings.TrimSpace(req.Profile),
			Timeout:       timeout,
		}
		rec, err := h.agentCoord.Start(runReq)
		if err != nil {
			return "", fmt.Errorf("session.wake: %w", err)
		}
		return rec.ID, nil
	default:
		return "", errors.New("unsupported webhook type")
	}
}

func webhookAuthorized(r *http.Request, token string) bool {
	if strings.TrimSpace(token) == "" {
		return false
	}
	auth := strings.TrimSpace(r.Header.Get("Authorization"))
	if strings.HasPrefix(strings.ToLower(auth), "bearer ") {
		return strings.TrimSpace(auth[len("Bearer "):]) == token
	}
	if v := strings.TrimSpace(r.Header.Get("X-Koios-Webhook-Token")); v != "" {
		return v == token
	}
	return false
}
