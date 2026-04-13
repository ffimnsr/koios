package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/ffimnsr/koios/internal/agent"
	"github.com/ffimnsr/koios/internal/scheduler"
	"github.com/ffimnsr/koios/internal/types"
)

const toolLoopMaxSteps = 4

func mustJSONSchema(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return b
}

// toolDef registers a server-side tool in one place.
// ToolPrompt, ToolDefinitions, and the ExecuteTool switch are all derived from
// this table; adding a new tool only requires a new entry here plus a case in
// ExecuteTool — no other functions need to change.
type toolDef struct {
	name        string
	description string
	parameters  json.RawMessage
	// argHint is the compact argument example shown in ToolPrompt.
	argHint string
	// available returns false when the tool's backing subsystem is not
	// configured.  nil means always available.
	available func(*Handler) bool
}

// toolDefs is the single source of truth for every registered tool.
var toolDefs = []toolDef{
	{
		name:        "time.now",
		description: "Get the current UTC time from the server.",
		parameters: mustJSONSchema(map[string]any{
			"type":                 "object",
			"properties":           map[string]any{},
			"additionalProperties": false,
		}),
		argHint: `{}`,
	},
	{
		name:        "session.history",
		description: "Read stored session history for the current peer. Optional session_key must belong to this peer (bare peer ID or a key prefixed with '<peer_id>::').",
		parameters: mustJSONSchema(map[string]any{
			"type": "object",
			"properties": map[string]any{
				"limit":       map[string]any{"type": "integer"},
				"session_key": map[string]any{"type": "string"},
			},
			"additionalProperties": false,
		}),
		argHint: `{"limit":50,"session_key":"optional — must be your own peer ID or start with '<your-peer-id>::'"}`,
	},
	{
		name:        "memory.search",
		description: "Search long-term memory for this peer.",
		parameters: mustJSONSchema(map[string]any{
			"type": "object",
			"properties": map[string]any{
				"q":     map[string]any{"type": "string"},
				"limit": map[string]any{"type": "integer"},
			},
			"required":             []string{"q"},
			"additionalProperties": false,
		}),
		argHint:   `{"q":"string","limit":5}`,
		available: func(h *Handler) bool { return h.memStore != nil },
	},
	{
		name:        "cron.list",
		description: "List cron jobs for this peer.",
		parameters:  mustJSONSchema(map[string]any{"type": "object", "properties": map[string]any{}, "additionalProperties": false}),
		argHint:     `{}`,
		available:   func(h *Handler) bool { return h.jobStore != nil && h.sched != nil },
	},
	{
		name:        "cron.create",
		description: "Create a scheduled job for this peer.",
		parameters: mustJSONSchema(map[string]any{
			"type": "object",
			"properties": map[string]any{
				"name":             map[string]any{"type": "string"},
				"description":      map[string]any{"type": "string"},
				"schedule":         map[string]any{"type": "object"},
				"payload":          map[string]any{"type": "object"},
				"enabled":          map[string]any{"type": "boolean"},
				"delete_after_run": map[string]any{"type": "boolean"},
			},
			"required": []string{"name", "schedule", "payload"},
		}),
		argHint:   `{"name":"string","description":"optional","schedule":{"kind":"at|every|cron","at":"RFC3339 for at","every_ms":60000,"expr":"5-field cron","tz":"Asia/Manila"},"payload":{"kind":"systemEvent|agentTurn","text":"for systemEvent","message":"for agentTurn"},"enabled":true,"delete_after_run":true}`,
		available: func(h *Handler) bool { return h.jobStore != nil && h.sched != nil },
	},
	{
		name:        "cron.get",
		description: "Fetch one cron job by id.",
		parameters:  mustJSONSchema(map[string]any{"type": "object", "properties": map[string]any{"id": map[string]any{"type": "string"}}, "required": []string{"id"}, "additionalProperties": false}),
		argHint:     `{"id":"job-id"}`,
		available:   func(h *Handler) bool { return h.jobStore != nil && h.sched != nil },
	},
	{
		name:        "cron.update",
		description: "Update an existing scheduled job for this peer.",
		parameters: mustJSONSchema(map[string]any{
			"type": "object",
			"properties": map[string]any{
				"id":               map[string]any{"type": "string"},
				"name":             map[string]any{"type": "string"},
				"description":      map[string]any{"type": "string"},
				"schedule":         map[string]any{"type": "object"},
				"payload":          map[string]any{"type": "object"},
				"enabled":          map[string]any{"type": "boolean"},
				"delete_after_run": map[string]any{"type": "boolean"},
			},
			"required": []string{"id"},
		}),
		argHint:   `{"id":"job-id","name":"optional","description":"optional","enabled":true,"schedule":{...},"payload":{...},"delete_after_run":true}`,
		available: func(h *Handler) bool { return h.jobStore != nil && h.sched != nil },
	},
	{
		name:        "cron.delete",
		description: "Delete one cron job by id.",
		parameters:  mustJSONSchema(map[string]any{"type": "object", "properties": map[string]any{"id": map[string]any{"type": "string"}}, "required": []string{"id"}, "additionalProperties": false}),
		argHint:     `{"id":"job-id"}`,
		available:   func(h *Handler) bool { return h.jobStore != nil && h.sched != nil },
	},
	{
		name:        "cron.trigger",
		description: "Trigger a cron job immediately.",
		parameters:  mustJSONSchema(map[string]any{"type": "object", "properties": map[string]any{"id": map[string]any{"type": "string"}}, "required": []string{"id"}, "additionalProperties": false}),
		argHint:     `{"id":"job-id"}`,
		available:   func(h *Handler) bool { return h.jobStore != nil && h.sched != nil },
	},
	{
		name:        "cron.runs",
		description: "Read recent run records for a cron job.",
		parameters:  mustJSONSchema(map[string]any{"type": "object", "properties": map[string]any{"id": map[string]any{"type": "string"}, "limit": map[string]any{"type": "integer"}}, "required": []string{"id"}, "additionalProperties": false}),
		argHint:     `{"id":"job-id","limit":50}`,
		available:   func(h *Handler) bool { return h.jobStore != nil && h.sched != nil },
	},
	{
		name:        "session.reset",
		description: "Clear the current peer session history.",
		parameters: mustJSONSchema(map[string]any{
			"type":                 "object",
			"properties":           map[string]any{},
			"additionalProperties": false,
		}),
		argHint: `{}`,
	},
}

// activeDefs returns the subset of toolDefs whose backing subsystem is
// available on this handler instance.
func (h *Handler) activeDefs() []toolDef {
	var active []toolDef
	for _, d := range toolDefs {
		if d.available == nil || d.available(h) {
			active = append(active, d)
		}
	}
	return active
}

func (h *Handler) ToolPrompt(peerID string) string {
	defs := h.activeDefs()
	names := make([]string, len(defs))
	hints := make([]string, len(defs))
	for i, d := range defs {
		names[i] = d.name
		hints[i] = "- " + d.name + ": " + d.argHint
	}
	return "You can use server-side tools to take actions for the current peer.\n" +
		"Current peer_id: " + peerID + "\n" +
		"Current UTC time: " + time.Now().UTC().Format(time.RFC3339) + "\n" +
		"If a tool is needed, respond with ONLY a single XML-wrapped JSON object in this exact format:\n" +
		"<tool_call>{\"name\":\"tool.name\",\"arguments\":{}}</tool_call>\n" +
		"Do not include any extra text before or after the tool call.\n" +
		"After you receive a tool result message from the user, either make another tool call or answer normally.\n" +
		"Available tools: " + strings.Join(names, ", ") + ".\n" +
		"Tool argument shapes:\n" +
		strings.Join(hints, "\n") + "\n" +
		"When the user asks what was said earlier, asks you to count prior words/messages, or asks what you should remember, use session.history instead of guessing or claiming you cannot inspect prior turns.\n" +
		"Only call tools that are available. Use tools instead of claiming you cannot perform actions when the tool can satisfy the request."
}

func (h *Handler) ToolDefinitions(peerID string) []types.Tool {
	_ = peerID
	defs := h.activeDefs()
	tools := make([]types.Tool, len(defs))
	for i, d := range defs {
		tools[i] = types.Tool{
			Type: "function",
			Function: types.ToolFunction{
				Name:        d.name,
				Description: d.description,
				Parameters:  d.parameters,
			},
		}
	}
	return tools
}

func (h *Handler) ExecuteTool(ctx context.Context, peerID string, call agent.ToolCall) (any, error) {
	call.Name = h.NormalizeToolName(peerID, call.Name)
	switch call.Name {
	case "time.now":
		return map[string]string{"utc": time.Now().UTC().Format(time.RFC3339)}, nil
	case "session.history":
		var args struct {
			Limit      int    `json:"limit"`
			SessionKey string `json:"session_key"`
		}
		if err := json.Unmarshal(call.Arguments, &args); err != nil {
			return nil, fmt.Errorf("invalid arguments: %w", err)
		}
		sessionKey := peerID
		if k := strings.TrimSpace(args.SessionKey); k != "" {
			// Only allow keys that belong to this peer: either the bare peer ID
			// or any namespaced key with the "<peerID>::" prefix.
			if k != peerID && !strings.HasPrefix(k, peerID+"::") {
				return nil, fmt.Errorf("session_key %q is not accessible to peer %q", k, peerID)
			}
			sessionKey = k
		}
		history := h.store.Get(sessionKey).History()
		if args.Limit > 0 && len(history) > args.Limit {
			history = history[len(history)-args.Limit:]
		}
		return map[string]any{
			"peer_id":     peerID,
			"session_key": sessionKey,
			"count":       len(history),
			"messages":    history,
		}, nil
	case "memory.search":
		if h.memStore == nil {
			return nil, fmt.Errorf("memory is not enabled")
		}
		var args struct {
			Q     string `json:"q"`
			Limit int    `json:"limit"`
		}
		if err := json.Unmarshal(call.Arguments, &args); err != nil {
			return nil, fmt.Errorf("invalid arguments: %w", err)
		}
		if strings.TrimSpace(args.Q) == "" {
			return nil, fmt.Errorf("q is required")
		}
		if args.Limit <= 0 {
			args.Limit = 5
		}
		results, err := h.memStore.Search(ctx, peerID, args.Q, args.Limit)
		if err != nil {
			return nil, err
		}
		return map[string]any{"results": results}, nil
	case "session.reset":
		h.store.Reset(peerID)
		return map[string]bool{"ok": true}, nil
	case "cron.list":
		if h.jobStore == nil || h.sched == nil {
			return nil, fmt.Errorf("cron is not enabled")
		}
		return h.jobStore.List(peerID), nil
	case "cron.get":
		var args struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(call.Arguments, &args); err != nil {
			return nil, fmt.Errorf("invalid arguments: %w", err)
		}
		return h.ownedJobForPeer(peerID, args.ID)
	case "cron.delete":
		var args struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(call.Arguments, &args); err != nil {
			return nil, fmt.Errorf("invalid arguments: %w", err)
		}
		if _, err := h.ownedJobForPeer(peerID, args.ID); err != nil {
			return nil, err
		}
		if err := h.jobStore.Remove(args.ID); err != nil {
			return nil, err
		}
		return map[string]bool{"ok": true}, nil
	case "cron.trigger":
		var args struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(call.Arguments, &args); err != nil {
			return nil, fmt.Errorf("invalid arguments: %w", err)
		}
		if _, err := h.ownedJobForPeer(peerID, args.ID); err != nil {
			return nil, err
		}
		runID, err := h.sched.TriggerRun(args.ID)
		if err != nil {
			return nil, err
		}
		return map[string]any{"ok": true, "run_id": runID}, nil
	case "cron.runs":
		var args struct {
			ID    string `json:"id"`
			Limit int    `json:"limit"`
		}
		if err := json.Unmarshal(call.Arguments, &args); err != nil {
			return nil, fmt.Errorf("invalid arguments: %w", err)
		}
		if _, err := h.ownedJobForPeer(peerID, args.ID); err != nil {
			return nil, err
		}
		if args.Limit <= 0 {
			args.Limit = 50
		}
		return h.jobStore.ReadRunRecords(args.ID, args.Limit)
	case "cron.create":
		var args cronCreateParams
		if err := json.Unmarshal(call.Arguments, &args); err != nil {
			return nil, fmt.Errorf("invalid arguments: %w", err)
		}
		return h.createCronJob(peerID, args)
	case "cron.update":
		var args cronUpdateParams
		if err := json.Unmarshal(call.Arguments, &args); err != nil {
			return nil, fmt.Errorf("invalid arguments: %w", err)
		}
		return h.updateCronJob(peerID, args)
	default:
		return nil, fmt.Errorf("unknown tool %q", call.Name)
	}
}

func (h *Handler) NormalizeToolName(peerID, name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return name
	}
	for _, tool := range h.ToolDefinitions(peerID) {
		if tool.Type == "function" && tool.Function.Name == name {
			return name
		}
	}
	if strings.Contains(name, ".") {
		return name
	}
	var matched string
	for _, tool := range h.ToolDefinitions(peerID) {
		if tool.Type != "function" || tool.Function.Name == "" {
			continue
		}
		if strings.HasSuffix(tool.Function.Name, "."+name) {
			if matched != "" {
				return name
			}
			matched = tool.Function.Name
		}
	}
	if matched != "" {
		return matched
	}
	return name
}

func normalizeSchedule(s scheduler.Schedule) scheduler.Schedule {
	if s.Kind != scheduler.KindCron {
		return s
	}
	fields := strings.Fields(s.Expr)
	if len(fields) == 6 {
		s.Expr = strings.Join(fields[1:], " ")
	}
	return s
}

func (h *Handler) createCronJob(peerID string, p cronCreateParams) (*scheduler.Job, error) {
	if h.jobStore == nil || h.sched == nil {
		return nil, fmt.Errorf("cron is not enabled")
	}
	if strings.TrimSpace(p.Name) == "" {
		return nil, fmt.Errorf("name is required")
	}
	p.Schedule = normalizeSchedule(p.Schedule)
	if err := validateSchedule(p.Schedule); err != nil {
		return nil, fmt.Errorf("invalid schedule: %w", err)
	}
	if err := validatePayload(p.Payload); err != nil {
		return nil, fmt.Errorf("invalid payload: %w", err)
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
		PeerID:         peerID,
		Name:           p.Name,
		Description:    p.Description,
		Schedule:       p.Schedule,
		Payload:        p.Payload,
		Enabled:        enabled,
		DeleteAfterRun: deleteAfterRun,
	}
	nextRun, err := scheduler.CalcInitialNextRun(job, h.sched.CronParser())
	if err != nil {
		return nil, fmt.Errorf("schedule cannot compute next run: %w", err)
	}
	job.NextRunAt = nextRun
	if err := h.jobStore.Add(job); err != nil {
		return nil, err
	}
	return job, nil
}

func (h *Handler) updateCronJob(peerID string, p cronUpdateParams) (*scheduler.Job, error) {
	if h.jobStore == nil || h.sched == nil {
		return nil, fmt.Errorf("cron is not enabled")
	}
	job, err := h.ownedJobForPeer(peerID, p.ID)
	if err != nil {
		return nil, err
	}
	if p.Name != nil {
		if strings.TrimSpace(*p.Name) == "" {
			return nil, fmt.Errorf("name must not be empty")
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
		schedule := normalizeSchedule(*p.Schedule)
		if err := validateSchedule(schedule); err != nil {
			return nil, fmt.Errorf("invalid schedule: %w", err)
		}
		job.Schedule = schedule
		next, err := scheduler.CalcInitialNextRun(job, h.sched.CronParser())
		if err != nil {
			return nil, fmt.Errorf("schedule cannot compute next run: %w", err)
		}
		job.NextRunAt = next
		job.ConsecErrors = 0
	}
	if p.Payload != nil {
		if err := validatePayload(*p.Payload); err != nil {
			return nil, fmt.Errorf("invalid payload: %w", err)
		}
		job.Payload = *p.Payload
	}
	if err := h.jobStore.Update(job); err != nil {
		return nil, err
	}
	return job, nil
}

func (h *Handler) ownedJobForPeer(peerID, jobID string) (*scheduler.Job, error) {
	if h.jobStore == nil {
		return nil, fmt.Errorf("cron is not enabled")
	}
	if jobID == "" {
		return nil, fmt.Errorf("id is required")
	}
	job := h.jobStore.Get(jobID)
	if job == nil || job.PeerID != peerID {
		return nil, fmt.Errorf("job not found")
	}
	return job, nil
}
