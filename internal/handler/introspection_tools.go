package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
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

func (h *Handler) toolList(peerID, sessionKey, activeProfile, domain, query string, includeArgumentHints bool) map[string]any {
	defs := h.activeDefs(peerID, sessionKey, activeProfile)
	domain = strings.ToLower(strings.TrimSpace(domain))
	query = strings.ToLower(strings.TrimSpace(query))
	groups := make(map[string][]map[string]any)
	for _, def := range defs {
		group := toolDomain(def.name)
		if domain != "" && group != domain {
			continue
		}
		tags := toolDiscoveryTags(def)
		if query != "" {
			haystack := strings.ToLower(def.name + "\n" + def.description + "\n" + def.argHint + "\n" + strings.Join(tags, " "))
			if !strings.Contains(haystack, query) {
				continue
			}
		}
		groups[group] = append(groups[group], toolDiscoveryItem(def, includeArgumentHints, nil))
	}
	orderedDomains := make([]string, 0, len(groups))
	for domainName := range groups {
		orderedDomains = append(orderedDomains, domainName)
	}
	sort.Strings(orderedDomains)
	outGroups := make([]map[string]any, 0, len(orderedDomains))
	count := 0
	for _, domainName := range orderedDomains {
		items := groups[domainName]
		sort.Slice(items, func(i, j int) bool {
			return fmt.Sprint(items[i]["name"]) < fmt.Sprint(items[j]["name"])
		})
		count += len(items)
		outGroups = append(outGroups, map[string]any{
			"domain": domainName,
			"tools":  items,
			"count":  len(items),
		})
	}
	return map[string]any{
		"peer_id":                peerID,
		"session_key":            sessionKey,
		"active_profile":         activeProfile,
		"domain_filter":          strings.TrimSpace(domain),
		"query_filter":           strings.TrimSpace(query),
		"include_argument_hints": includeArgumentHints,
		"domains":                outGroups,
		"count":                  count,
	}
}

func (h *Handler) toolSearch(peerID, sessionKey, activeProfile, query, domain string, limit int, includeArgumentHints bool) (map[string]any, error) {
	query = strings.ToLower(strings.TrimSpace(query))
	if query == "" {
		return nil, fmt.Errorf("query is required")
	}
	domain = strings.ToLower(strings.TrimSpace(domain))
	defs := h.activeDefs(peerID, sessionKey, activeProfile)
	matches := make([]map[string]any, 0)
	for _, def := range defs {
		group := toolDomain(def.name)
		if domain != "" && group != domain {
			continue
		}
		eval := toolSearchEvaluate(def, query)
		if eval.Score <= 0 {
			continue
		}
		matches = append(matches, toolDiscoveryItem(def, includeArgumentHints, &eval))
	}
	if isStructuredToolQuery(query) {
		bestScore := 0
		for _, item := range matches {
			if score := matchScore(item); score > bestScore {
				bestScore = score
			}
		}
		if bestScore >= 900 {
			filtered := matches[:0]
			for _, item := range matches {
				if matchScore(item) >= 900 {
					filtered = append(filtered, item)
				}
			}
			matches = filtered
		}
	}
	sort.Slice(matches, func(i, j int) bool {
		left := matchScore(matches[i])
		right := matchScore(matches[j])
		if left != right {
			return left > right
		}
		return fmt.Sprint(matches[i]["name"]) < fmt.Sprint(matches[j]["name"])
	})
	totalMatches := len(matches)
	topConfidence := "none"
	if totalMatches > 0 {
		topConfidence = matchConfidence(matches[0])
	}
	suggestions := toolSearchSuggestions(defs, query, domain, matches)
	truncated := false
	if limit > 0 && len(matches) > limit {
		matches = matches[:limit]
		truncated = true
	}
	return map[string]any{
		"peer_id":                peerID,
		"session_key":            sessionKey,
		"active_profile":         activeProfile,
		"query":                  strings.TrimSpace(query),
		"domain_filter":          strings.TrimSpace(domain),
		"include_argument_hints": includeArgumentHints,
		"matches":                matches,
		"count":                  totalMatches,
		"truncated":              truncated,
		"confidence":             topConfidence,
		"suggestions":            suggestions,
	}, nil
}

type toolSearchEvaluation struct {
	Score      int
	Reasons    []string
	TopReason  string
	Tags       []string
	Confidence string
}

func toolSearchEvaluate(def toolDef, query string) toolSearchEvaluation {
	name := strings.ToLower(strings.TrimSpace(def.name))
	desc := strings.ToLower(strings.TrimSpace(def.description))
	hint := strings.ToLower(strings.TrimSpace(def.argHint))
	domain := toolDomain(def.name)
	op := name
	if idx := strings.LastIndex(name, "."); idx >= 0 && idx+1 < len(name) {
		op = name[idx+1:]
	}
	tags := toolDiscoveryTags(def)
	normalizedQuery := normalizeToolSearchTerm(query)
	normalizedName := normalizeToolSearchTerm(name)
	normalizedOp := normalizeToolSearchTerm(op)
	structuredQuery := isStructuredToolQuery(query)
	queryTerms := toolSearchTerms(query)
	eval := toolSearchEvaluation{Tags: tags}
	reasons := make(map[string]struct{})
	add := func(reason string, delta int) {
		if delta <= 0 {
			return
		}
		eval.Score += delta
		if _, ok := reasons[reason]; ok {
			return
		}
		reasons[reason] = struct{}{}
		eval.Reasons = append(eval.Reasons, reason)
	}
	switch {
	case name == query:
		add("exact_name", 1000)
	case normalizedName != "" && normalizedName == normalizedQuery:
		add("normalized_name", 950)
	case strings.HasPrefix(name, query):
		add("prefix_name", 800)
	case op == query:
		add("exact_operation", 780)
	case strings.Contains(name, "."+query):
		add("qualified_suffix", 700)
	case strings.Contains(name, query):
		add("name_contains", 600)
	}
	if domain == query {
		add("exact_domain", 450)
	}
	if normalizedOp != "" && normalizedOp == normalizedQuery {
		add("normalized_operation", 400)
	}
	if normalizedName != "" && normalizedQuery != "" && strings.HasPrefix(normalizedName, normalizedQuery) {
		add("normalized_prefix", 250)
	}
	for _, tag := range tags {
		switch {
		case tag == query:
			add("exact_tag", 320)
		case normalizedQuery != "" && normalizeToolSearchTerm(tag) == normalizedQuery:
			add("normalized_tag", 300)
		case strings.Contains(tag, query):
			add("tag_contains", 160)
		}
	}
	if len(queryTerms) > 1 {
		if allTermsPresent(name, queryTerms) {
			add("all_name_terms", 300)
		}
		if allTermsPresent(strings.Join(tags, " "), queryTerms) {
			add("all_tag_terms", 220)
		}
		if !structuredQuery && allTermsPresent(desc, queryTerms) {
			add("all_description_terms", 120)
		}
		if !structuredQuery && allTermsPresent(hint, queryTerms) {
			add("all_arg_hint_terms", 60)
		}
	}
	if !structuredQuery && strings.Contains(desc, query) {
		add("description_contains", 250)
	}
	if !structuredQuery && strings.Contains(hint, query) {
		add("arg_hint_contains", 100)
	}
	if structuredQuery {
		if fuzzyToolSearchMatch(normalizedName, normalizedQuery) {
			add("fuzzy_name", 800)
		}
	} else {
		if fuzzyToolSearchMatch(normalizedName, normalizedQuery) {
			add("fuzzy_name", 220)
		}
		if fuzzyToolSearchMatch(normalizedOp, normalizedQuery) {
			add("fuzzy_operation", 180)
		}
	}
	for _, alias := range builtInTools.AliasesFor(def.name) {
		alias = strings.ToLower(strings.TrimSpace(alias))
		normalizedAlias := normalizeToolSearchTerm(alias)
		switch {
		case alias == query:
			add("exact_alias", 500)
		case normalizedAlias != "" && normalizedAlias == normalizedQuery:
			add("normalized_alias", 450)
		case strings.HasPrefix(alias, query):
			add("prefix_alias", 350)
		case strings.Contains(alias, query):
			add("alias_contains", 250)
		}
		if fuzzyToolSearchMatch(normalizedAlias, normalizedQuery) {
			add("fuzzy_alias", 150)
		}
	}
	eval.Confidence = toolSearchConfidence(eval)
	if len(eval.Reasons) > 0 {
		eval.TopReason = eval.Reasons[0]
	}
	return eval
}

func toolDiscoveryItem(def toolDef, includeArgumentHints bool, eval *toolSearchEvaluation) map[string]any {
	item := map[string]any{
		"name":        def.name,
		"description": def.description,
		"domain":      toolDomain(def.name),
		"tags":        toolDiscoveryTags(def),
	}
	if aliases := builtInTools.AliasesFor(def.name); len(aliases) > 0 {
		item["aliases"] = aliases
	}
	if includeArgumentHints && strings.TrimSpace(def.argHint) != "" {
		item["arg_hint"] = def.argHint
	}
	if eval != nil {
		item["score"] = eval.Score
		item["reasons"] = eval.Reasons
		item["top_reason"] = eval.TopReason
		item["confidence"] = eval.Confidence
	}
	return item
}

func toolDiscoveryTags(def toolDef) []string {
	seen := make(map[string]struct{})
	add := func(values ...string) {
		for _, value := range values {
			value = strings.ToLower(strings.TrimSpace(value))
			if value == "" {
				continue
			}
			if _, ok := seen[value]; ok {
				continue
			}
			seen[value] = struct{}{}
		}
	}
	add(toolDomain(def.name))
	add(def.tags...)
	add(toolSearchTerms(def.name)...)
	for _, alias := range builtInTools.AliasesFor(def.name) {
		add(toolSearchTerms(alias)...)
	}
	out := make([]string, 0, len(seen))
	for value := range seen {
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}

func toolSearchConfidence(eval toolSearchEvaluation) string {
	if eval.Score <= 0 {
		return "none"
	}
	for _, reason := range eval.Reasons {
		switch reason {
		case "exact_name", "normalized_name", "prefix_name", "exact_operation", "qualified_suffix", "name_contains", "exact_alias", "normalized_alias", "prefix_alias", "normalized_operation":
			return "high"
		}
	}
	for _, reason := range eval.Reasons {
		switch reason {
		case "exact_domain", "normalized_prefix", "exact_tag", "normalized_tag", "tag_contains", "all_name_terms", "all_tag_terms", "all_description_terms", "all_arg_hint_terms", "description_contains", "arg_hint_contains", "alias_contains":
			return "medium"
		}
	}
	return "low"
}

func toolSearchSuggestions(defs []toolDef, query, domain string, matches []map[string]any) []map[string]any {
	suggestions := make([]map[string]any, 0, 4)
	if len(matches) == 0 {
		if domain != "" {
			suggestions = append(suggestions, map[string]any{
				"kind":    "hint",
				"message": fmt.Sprintf("No tools matched in the %q domain. Try removing the domain filter or browse that domain with tool.list.", domain),
			})
		}
		suggestions = append(suggestions, map[string]any{
			"kind":    "hint",
			"message": "Try tool.list to browse by domain, or tool.help if you know the canonical tool name.",
		})
		closest := toolSearchClosestCandidates(defs, query, domain, 3)
		for _, candidate := range closest {
			suggestions = append(suggestions, map[string]any{
				"kind":    "tool",
				"name":    candidate["name"],
				"domain":  candidate["domain"],
				"message": "Closest available canonical tool.",
			})
		}
		return suggestions
	}
	if matchConfidence(matches[0]) == "low" {
		suggestions = append(suggestions, map[string]any{
			"kind":    "hint",
			"message": "Top result is a low-confidence fuzzy or metadata match. Confirm it with tool.help before calling it.",
		})
		if domain == "" {
			suggestions = append(suggestions, map[string]any{
				"kind":    "hint",
				"message": "If you know the domain, rerun tool.search with a domain filter to narrow the result set.",
			})
		}
	}
	return suggestions
}

func toolSearchClosestCandidates(defs []toolDef, query, domain string, limit int) []map[string]any {
	normalizedQuery := normalizeToolSearchTerm(query)
	queryTerms := toolSearchTerms(query)
	candidates := make([]map[string]any, 0, len(defs))
	for _, def := range defs {
		group := toolDomain(def.name)
		if domain != "" && group != domain {
			continue
		}
		name := strings.ToLower(strings.TrimSpace(def.name))
		normalizedName := normalizeToolSearchTerm(name)
		if normalizedName == "" || normalizedQuery == "" {
			continue
		}
		score := 0
		distance := levenshteinDistance(normalizedName, normalizedQuery)
		if distance <= 6 {
			score += maxIntValue(0, 120-distance*15)
		}
		if allTermsPresent(name, queryTerms) {
			score += 60
		}
		if strings.Contains(name, query) {
			score += 40
		}
		if score <= 0 {
			continue
		}
		candidates = append(candidates, map[string]any{
			"name":   def.name,
			"domain": group,
			"score":  score,
		})
	}
	sort.Slice(candidates, func(i, j int) bool {
		left := matchScore(candidates[i])
		right := matchScore(candidates[j])
		if left != right {
			return left > right
		}
		return fmt.Sprint(candidates[i]["name"]) < fmt.Sprint(candidates[j]["name"])
	})
	if limit > 0 && len(candidates) > limit {
		candidates = candidates[:limit]
	}
	for _, candidate := range candidates {
		delete(candidate, "score")
	}
	return candidates
}

func toolSearchTerms(value string) []string {
	return strings.FieldsFunc(strings.ToLower(strings.TrimSpace(value)), func(r rune) bool {
		return (r < 'a' || r > 'z') && (r < '0' || r > '9')
	})
}

func isStructuredToolQuery(value string) bool {
	value = strings.TrimSpace(value)
	return strings.ContainsAny(value, ".-_/") || strings.ContainsRune(value, ' ')
}

func normalizeToolSearchTerm(value string) string {
	terms := toolSearchTerms(value)
	if len(terms) == 0 {
		return ""
	}
	return strings.Join(terms, "")
}

func allTermsPresent(haystack string, terms []string) bool {
	if len(terms) == 0 {
		return false
	}
	for _, term := range terms {
		if !strings.Contains(haystack, term) {
			return false
		}
	}
	return true
}

func fuzzyToolSearchMatch(candidate, query string) bool {
	if candidate == "" || query == "" || candidate == query {
		return false
	}
	if len(query) < 4 || absInt(len(candidate)-len(query)) > 1 {
		return false
	}
	return levenshteinDistance(candidate, query) == 1
}

func levenshteinDistance(a, b string) int {
	if a == b {
		return 0
	}
	if a == "" {
		return len(b)
	}
	if b == "" {
		return len(a)
	}
	prev := make([]int, len(b)+1)
	curr := make([]int, len(b)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(a); i++ {
		curr[0] = i
		for j := 1; j <= len(b); j++ {
			cost := 0
			if a[i-1] != b[j-1] {
				cost = 1
			}
			curr[j] = minInt(
				curr[j-1]+1,
				prev[j]+1,
				prev[j-1]+cost,
			)
		}
		prev, curr = curr, prev
	}
	return prev[len(b)]
}

func minInt(values ...int) int {
	best := values[0]
	for _, value := range values[1:] {
		if value < best {
			best = value
		}
	}
	return best
}

func maxIntValue(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func absInt(value int) int {
	if value < 0 {
		return -value
	}
	return value
}

func matchScore(item map[string]any) int {
	value, ok := item["score"]
	if !ok {
		return 0
	}
	score, ok := value.(int)
	if ok {
		return score
	}
	return 0
}

func matchConfidence(item map[string]any) string {
	value, ok := item["confidence"]
	if !ok {
		return "none"
	}
	confidence, ok := value.(string)
	if ok {
		return confidence
	}
	return "none"
}

func (h *Handler) toolHelp(peerID, sessionKey, activeProfile, name string) (map[string]any, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, fmt.Errorf("name is required")
	}
	canonical := h.NormalizeToolName(peerID, name)
	for _, def := range h.activeDefs(peerID, sessionKey, activeProfile) {
		if def.name != canonical {
			continue
		}
		item := toolDiscoveryItem(def, true, nil)
		item["requested_name"] = name
		item["canonical_name"] = canonical
		item["parameters"] = json.RawMessage(def.parameters)
		return item, nil
	}
	return nil, fmt.Errorf("tool %q is not available", name)
}

func toolDomain(name string) string {
	name = strings.TrimSpace(name)
	if idx := strings.Index(name, "."); idx > 0 {
		return name[:idx]
	}
	return "core"
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

func (h *Handler) listModels(peerID string) map[string]any {
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
	// Append peer-linked BYOK provider profiles if the store is available.
	if h.peerLLMStore != nil && strings.TrimSpace(peerID) != "" {
		peerProfiles, err := h.peerLLMStore.List(context.Background(), peerID)
		if err == nil && len(peerProfiles) > 0 {
			for _, pp := range peerProfiles {
				if !pp.Enabled {
					continue
				}
				models = append(models, map[string]any{
					"name":           pp.Name,
					"model":          pp.DefaultModel,
					"provider":       pp.Provider,
					"base_url":       pp.BaseURL,
					"role":           "peer_profile",
					"has_api_key":    pp.HasAPIKey,
					"api_key_masked": pp.APIKeyMasked,
				})
			}
		}
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
		// If no model override, check provider_profile for a BYOK default.
		if requested == "" {
			requested = strings.TrimSpace(policy.ProviderProfile)
		}
	}
	// Try config-level model resolution first.
	resolvedModel, info, viaProfile := h.resolveModelInfo(requested)
	providerName := info.Provider
	if providerName == "" {
		providerName = h.modelCatalog.Provider
	}
	matchedProfile := ""
	if viaProfile {
		matchedProfile = info.Name
	}

	// If not found in config profiles, check peer BYOK provider profiles.
	if !viaProfile && h.peerLLMStore != nil && strings.TrimSpace(peerID) != "" && requested != "" {
		if p, err := h.peerLLMStore.Get(context.Background(), peerID, requested); err == nil && p != nil {
			resolvedModel = p.DefaultModel
			if resolvedModel == "" {
				resolvedModel = requested
			}
			providerName = p.Provider
			matchedProfile = requested
			viaProfile = true
		}
	}

	return map[string]any{
		"requested_model": requested,
		"resolved_model":  resolvedModel,
		"matched_profile": matchedProfile,
		"capabilities":    h.modelCapabilitiesPayload(resolvedModel, providerName),
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
