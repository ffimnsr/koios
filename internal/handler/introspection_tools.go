package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/ffimnsr/koios/internal/runledger"
	"github.com/ffimnsr/koios/internal/types"
)

type capabilityInspector interface {
	Capabilities(model string) types.ProviderCapabilities
}

func sessionKeyOwnedByPeer(peerID, sessionKey string) bool {
	sessionKey = strings.TrimSpace(sessionKey)
	if sessionKey == "" {
		return false
	}
	return sessionKey == peerID || strings.HasPrefix(sessionKey, peerID+"::")
}

func approximateTokens(payload any) int {
	buf, err := json.Marshal(payload)
	if err != nil {
		return 0
	}
	return (len(buf) + 3) / 4
}

func (h *Handler) modelCapabilitiesFor(model string) types.ProviderCapabilities {
	if caps, ok := h.provider.(capabilityInspector); ok {
		return caps.Capabilities(model)
	}
	return types.ProviderCapabilities{}
}

func (h *Handler) resolveModelInfo(name string) (string, ModelProfileInfo, bool) {
	trimmed := strings.TrimSpace(name)
	for _, profile := range h.modelCatalog.Profiles {
		if profile.Name == trimmed || profile.Model == trimmed {
			return profile.Model, profile, true
		}
	}
	if trimmed == "" {
		return h.model, ModelProfileInfo{
			Name:     h.modelCatalog.DefaultProfile,
			Provider: h.modelCatalog.Provider,
			BaseURL:  h.modelCatalog.BaseURL,
			Model:    h.model,
		}, false
	}
	return trimmed, ModelProfileInfo{
		Provider: h.modelCatalog.Provider,
		BaseURL:  h.modelCatalog.BaseURL,
		Model:    trimmed,
	}, false
}

func (h *Handler) runRecord(peerID, id string) (runledger.Record, error) {
	if h.runLedger == nil {
		return runledger.Record{}, fmt.Errorf("run ledger is not enabled")
	}
	id = strings.TrimSpace(id)
	if id == "" {
		return runledger.Record{}, fmt.Errorf("id is required")
	}
	rec, ok := h.runLedger.Get(id)
	if !ok {
		return runledger.Record{}, fmt.Errorf("run not found")
	}
	if rec.PeerID != "" && rec.PeerID != peerID {
		return runledger.Record{}, fmt.Errorf("run not found")
	}
	return rec, nil
}

func (h *Handler) runSummary(rec runledger.Record, includePayloads bool) map[string]any {
	item := map[string]any{
		"id":                rec.ID,
		"kind":              string(rec.Kind),
		"peer_id":           rec.PeerID,
		"session_key":       rec.SessionKey,
		"model":             rec.Model,
		"status":            string(rec.Status),
		"error":             rec.Error,
		"steps":             rec.Steps,
		"tool_calls":        rec.ToolCalls,
		"parent_id":         rec.ParentID,
		"prompt_tokens":     rec.PromptTokens,
		"completion_tokens": rec.CompletionTokens,
		"queued_at":         rec.QueuedAt,
		"started_at":        rec.StartedAt,
		"finished_at":       rec.FinishedAt,
	}
	if includePayloads {
		if decoded, ok := decodeRawJSON(rec.Request); ok {
			item["request"] = decoded
		}
		if decoded, ok := decodeRawJSON(rec.Result); ok {
			item["result"] = decoded
		}
	}
	switch rec.Kind {
	case runledger.KindCodeExecution:
		item["active"] = h.codeExecutionRunActive(rec.ID)
		item["cancel_supported"] = true
		item["logs_supported"] = true
	case runledger.KindProcess:
		item["active"] = h.backgroundProcessActive(rec.ID)
		item["cancel_supported"] = true
		item["logs_supported"] = true
	case runledger.KindAgent, runledger.KindSubagent, runledger.KindOrchestrator, runledger.KindWorkflow:
		item["active"] = rec.Status == runledger.StatusQueued || rec.Status == runledger.StatusRunning
		item["cancel_supported"] = true
		item["logs_supported"] = true
	case runledger.KindCron:
		item["active"] = false
		item["cancel_supported"] = false
		item["logs_supported"] = false
	default:
		item["active"] = false
	}
	return item
}

func (h *Handler) listRuns(peerID string, kind, status string, limit int) (map[string]any, error) {
	if h.runLedger == nil {
		return nil, fmt.Errorf("run ledger is not enabled")
	}
	if limit <= 0 {
		limit = 20
	}
	records := h.runLedger.List(runledger.Filter{
		PeerID: peerID,
		Kind:   runledger.RunKind(strings.TrimSpace(kind)),
		Status: runledger.RunStatus(strings.TrimSpace(status)),
		Limit:  limit,
	}, 30*24*time.Hour)
	runs := make([]map[string]any, 0, len(records))
	for _, rec := range records {
		runs = append(runs, h.runSummary(rec, false))
	}
	return map[string]any{"runs": runs, "count": len(runs)}, nil
}

func (h *Handler) runStatus(peerID, id string) (map[string]any, error) {
	rec, err := h.runRecord(peerID, id)
	if err != nil {
		return nil, err
	}
	return h.runSummary(rec, true), nil
}

func (h *Handler) cancelRun(peerID, id string) (map[string]any, error) {
	rec, err := h.runRecord(peerID, id)
	if err != nil {
		return nil, err
	}
	switch rec.Kind {
	case runledger.KindAgent:
		if h.agentCoord == nil {
			return nil, fmt.Errorf("agent runtime is not enabled")
		}
		record, err := h.agentCoord.Cancel(id)
		if err != nil {
			return nil, err
		}
		return map[string]any{"id": record.ID, "kind": string(rec.Kind), "status": record.Status}, nil
	case runledger.KindSubagent:
		if h.subRuntime == nil {
			return nil, fmt.Errorf("subagents are not enabled")
		}
		if err := h.subRuntime.Kill(id); err != nil {
			return nil, err
		}
		return map[string]any{"id": id, "kind": string(rec.Kind), "status": "canceling"}, nil
	case runledger.KindOrchestrator:
		if h.orchestrator == nil {
			return nil, fmt.Errorf("orchestrator is not enabled")
		}
		if err := h.orchestrator.Cancel(id); err != nil {
			return nil, err
		}
		return map[string]any{"id": id, "kind": string(rec.Kind), "status": "canceling"}, nil
	case runledger.KindWorkflow:
		if h.workflowRunner == nil {
			return nil, fmt.Errorf("workflow engine is not enabled")
		}
		if err := h.workflowRunner.Cancel(id); err != nil {
			return nil, err
		}
		return map[string]any{"id": id, "kind": string(rec.Kind), "status": "canceling"}, nil
	case runledger.KindCodeExecution:
		return h.codeExecutionCancel(peerID, id)
	case runledger.KindProcess:
		return h.stopBackgroundProcess(peerID, id)
	case runledger.KindCron:
		return nil, fmt.Errorf("cron runs cannot be canceled once dispatched")
	default:
		return nil, fmt.Errorf("run kind %q cannot be canceled", rec.Kind)
	}
}

func (h *Handler) runLogs(peerID, id string, maxBytes, maxMessages int) (map[string]any, error) {
	rec, err := h.runRecord(peerID, id)
	if err != nil {
		return nil, err
	}
	switch rec.Kind {
	case runledger.KindProcess:
		return h.backgroundProcessLogs(peerID, id, maxBytes)
	case runledger.KindCodeExecution:
		result, _ := decodeRawJSON(rec.Result)
		request, _ := decodeRawJSON(rec.Request)
		payload := h.runSummary(rec, false)
		payload["request"] = request
		payload["result"] = result
		if resultMap, ok := result.(map[string]any); ok {
			payload["stdout"] = resultMap["stdout"]
			payload["stderr"] = resultMap["stderr"]
			payload["artifacts"] = resultMap["artifacts"]
		}
		return payload, nil
	case runledger.KindWorkflow:
		if h.workflowRunner != nil {
			run, err := h.workflowRunner.Status(id)
			if err == nil && run != nil && run.PeerID == peerID {
				return map[string]any{
					"id":     id,
					"kind":   string(rec.Kind),
					"status": string(rec.Status),
					"run":    run,
				}, nil
			}
		}
	}
	payload := h.runSummary(rec, true)
	if rec.SessionKey != "" && sessionKeyOwnedByPeer(peerID, rec.SessionKey) {
		history := h.store.Get(rec.SessionKey).History()
		if maxMessages <= 0 {
			maxMessages = 50
		}
		if len(history) > maxMessages {
			history = history[len(history)-maxMessages:]
		}
		payload["messages"] = history
		payload["message_count"] = len(history)
	}
	return payload, nil
}

func (h *Handler) usageCurrent(peerID string) map[string]any {
	if h.usageStore == nil {
		return map[string]any{"peer_id": peerID, "found": false}
	}
	u, ok := h.usageStore.Get(peerID)
	return map[string]any{"peer_id": peerID, "found": ok, "usage": u}
}

func (h *Handler) usageHistory(limit int) map[string]any {
	if h.usageStore == nil {
		return map[string]any{"sessions": []any{}, "count": 0, "totals": nil}
	}
	sessions := h.usageStore.All()
	if limit > 0 && len(sessions) > limit {
		sessions = sessions[:limit]
	}
	return map[string]any{"sessions": sessions, "count": len(sessions), "totals": h.usageStore.Totals()}
}

func (h *Handler) usageEstimate(ctx context.Context, peerID string, messages []types.Message, text, sessionKey, model string, includeHistory, includeTools bool, expectedCompletionTokens int) (map[string]any, error) {
	if strings.TrimSpace(sessionKey) == "" {
		sessionKey = peerID
	}
	if !sessionKeyOwnedByPeer(peerID, sessionKey) {
		return nil, fmt.Errorf("session_key must belong to this peer")
	}
	if len(messages) == 0 && strings.TrimSpace(text) != "" {
		messages = []types.Message{{Role: "user", Content: text}}
	}
	history := []types.Message(nil)
	if includeHistory {
		history = h.store.Get(sessionKey).History()
	}
	req, err := requestBuilder(ctx, h, peerID, messages, history, false)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(model) != "" {
		req.Model = strings.TrimSpace(model)
	}
	toolCount := 0
	if includeTools {
		tools := h.ToolDefinitions(peerID)
		req.Tools = tools
		toolCount = len(tools)
	}
	promptEstimate := approximateTokens(req)
	if expectedCompletionTokens < 0 {
		expectedCompletionTokens = 0
	}
	return map[string]any{
		"model":                       req.Model,
		"session_key":                 sessionKey,
		"includes_history":            includeHistory,
		"history_messages":            len(history),
		"tool_count":                  toolCount,
		"estimated_prompt_tokens":     promptEstimate,
		"estimated_completion_tokens": expectedCompletionTokens,
		"estimated_total_tokens":      promptEstimate + expectedCompletionTokens,
		"heuristic":                   "approximate_tokens = ceil(json_request_bytes / 4)",
		"request":                     req,
	}, nil
}

func (h *Handler) listModels() map[string]any {
	models := []map[string]any{{
		"name":         "default",
		"model":        h.model,
		"provider":     h.modelCatalog.Provider,
		"base_url":     h.modelCatalog.BaseURL,
		"role":         "default",
		"capabilities": h.modelCapabilitiesPayload(h.model, h.modelCatalog.Provider),
	}}
	if h.modelCatalog.LightweightModel != "" {
		models = append(models, map[string]any{
			"name":         "lightweight",
			"model":        h.modelCatalog.LightweightModel,
			"provider":     h.modelCatalog.Provider,
			"base_url":     h.modelCatalog.BaseURL,
			"role":         "lightweight",
			"capabilities": h.modelCapabilitiesPayload(h.modelCatalog.LightweightModel, h.modelCatalog.Provider),
		})
	}
	for _, profile := range h.modelCatalog.Profiles {
		providerName := profile.Provider
		if providerName == "" {
			providerName = h.modelCatalog.Provider
		}
		models = append(models, map[string]any{
			"name":         profile.Name,
			"model":        profile.Model,
			"provider":     providerName,
			"base_url":     firstNonEmpty(profile.BaseURL, h.modelCatalog.BaseURL),
			"role":         "profile",
			"capabilities": h.modelCapabilitiesPayload(profile.Model, providerName),
		})
	}
	return map[string]any{
		"default_model":   h.model,
		"default_profile": h.modelCatalog.DefaultProfile,
		"fallback_models": append([]string(nil), h.modelCatalog.FallbackModels...),
		"routing_policy":  map[string]any{"lightweight_word_threshold": h.lightweightWordThreshold()},
		"models":          models,
		"count":           len(models),
	}
}

func (h *Handler) modelCapabilitiesPayload(model, providerName string) map[string]any {
	caps := h.modelCapabilitiesFor(model)
	if providerName == "" {
		providerName = caps.Name
	}
	return map[string]any{
		"provider":               firstNonEmpty(providerName, caps.Name),
		"supports_streaming":     caps.SupportsStreaming,
		"supports_native_tools":  caps.SupportsNativeTools,
		"requires_max_tokens":    caps.RequiresMaxTokens,
		"openai_compatible_wire": caps.OpenAICompatibleWire,
		"context_limits":         map[string]any{"known": false},
		"media_support":          map[string]any{"known": false},
	}
}

func (h *Handler) modelCapabilities(peerID, model, sessionKey string) (map[string]any, error) {
	if strings.TrimSpace(sessionKey) != "" && !sessionKeyOwnedByPeer(peerID, sessionKey) {
		return nil, fmt.Errorf("session_key must belong to this peer")
	}
	requested := strings.TrimSpace(model)
	if requested == "" && strings.TrimSpace(sessionKey) != "" {
		policy := h.store.Policy(sessionKey)
		requested = strings.TrimSpace(policy.ModelOverride)
	}
	resolvedModel, info, viaProfile := h.resolveModelInfo(requested)
	providerName := info.Provider
	if providerName == "" {
		providerName = h.modelCatalog.Provider
	}
	return map[string]any{
		"requested_model": requested,
		"resolved_model":  resolvedModel,
		"matched_profile": func() string {
			if viaProfile {
				return info.Name
			}
			return ""
		}(),
		"capabilities": h.modelCapabilitiesPayload(resolvedModel, providerName),
	}, nil
}

func (h *Handler) lightweightWordThreshold() int {
	if h.modelCatalog.LightweightWordThreshold > 0 {
		return h.modelCatalog.LightweightWordThreshold
	}
	return 15
}

func (h *Handler) routeModel(peerID, text, requestedModel, sessionKey string) (map[string]any, error) {
	if strings.TrimSpace(sessionKey) == "" {
		sessionKey = peerID
	}
	if !sessionKeyOwnedByPeer(peerID, sessionKey) {
		return nil, fmt.Errorf("session_key must belong to this peer")
	}
	policy := h.store.Policy(sessionKey)
	currentOverride := strings.TrimSpace(policy.ModelOverride)
	selected := strings.TrimSpace(requestedModel)
	reason := "default"
	if selected != "" {
		reason = "explicit_model"
	} else if currentOverride != "" {
		selected = currentOverride
		reason = "session_override"
	} else if h.modelCatalog.LightweightModel != "" && len(strings.Fields(strings.TrimSpace(text))) > 0 && len(strings.Fields(strings.TrimSpace(text))) <= h.lightweightWordThreshold() {
		selected = h.modelCatalog.LightweightModel
		reason = "lightweight_query"
	}
	resolvedModel, info, viaProfile := h.resolveModelInfo(selected)
	providerName := info.Provider
	if providerName == "" {
		providerName = h.modelCatalog.Provider
	}
	return map[string]any{
		"requested_model":          strings.TrimSpace(requestedModel),
		"current_session_override": currentOverride,
		"selected_model":           resolvedModel,
		"selected_profile": func() string {
			if viaProfile {
				return info.Name
			}
			return ""
		}(),
		"provider":                   providerName,
		"base_url":                   firstNonEmpty(info.BaseURL, h.modelCatalog.BaseURL),
		"reason":                     reason,
		"can_apply_session_override": true,
		"routing_policy": map[string]any{
			"default_model":              h.model,
			"default_profile":            h.modelCatalog.DefaultProfile,
			"lightweight_model":          h.modelCatalog.LightweightModel,
			"lightweight_word_threshold": h.lightweightWordThreshold(),
			"fallback_models":            append([]string(nil), h.modelCatalog.FallbackModels...),
		},
		"capabilities": h.modelCapabilitiesPayload(resolvedModel, providerName),
	}, nil
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
