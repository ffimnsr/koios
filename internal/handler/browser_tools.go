package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"path"
	"strconv"
	"strings"

	"github.com/ffimnsr/koios/internal/agent"
	browsercfg "github.com/ffimnsr/koios/internal/browser"
	"github.com/ffimnsr/koios/internal/config"
	"github.com/ffimnsr/koios/internal/mcp"
)

type browserSnapshotRefState struct {
	NextRef  int
	UIDToRef map[string]string
	RefToUID map[string]string
}

type browserSnapshotLine struct {
	Raw    string
	Indent string
	Depth  int
	UID    string
	Role   string
	Name   string
	Suffix string
	Ref    string
	Attrs  map[string]any
}

type browserSnapshotNode struct {
	Ref        string
	UID        string
	Role       string
	Name       string
	Depth      int
	Attributes map[string]any
	Children   []*browserSnapshotNode
}

func (h *Handler) browserProfilesEnabled() bool {
	return len(h.browserConfig.Profiles) > 0 && h.mcpManager != nil
}

func (h *Handler) browserEnabledProfiles() []config.BrowserProfileConfig {
	cfg := &config.Config{Browser: h.browserConfig}
	return browsercfg.EnabledProfiles(cfg)
}

func (h *Handler) resolveActiveBrowserProfile(sessionKey string) string {
	cfg := &config.Config{Browser: h.browserConfig}
	sessionProfile := ""
	if sessionKey != "" {
		sessionProfile = h.store.Policy(sessionKey).BrowserProfile
	}
	return browsercfg.ResolveActiveProfile(cfg, sessionProfile)
}

func (h *Handler) activeBrowserProfile(sessionKey string) (config.BrowserProfileConfig, bool) {
	activeProfile := h.resolveActiveBrowserProfile(sessionKey)
	if activeProfile == "" {
		return config.BrowserProfileConfig{}, false
	}
	for _, profile := range h.browserEnabledProfiles() {
		if strings.EqualFold(strings.TrimSpace(profile.Name), activeProfile) {
			return profile, true
		}
	}
	return config.BrowserProfileConfig{}, false
}

func (h *Handler) browserSessionKey(ctx context.Context, peerID string) string {
	toolCtx, _ := agent.ToolRunContextFromContext(ctx)
	sessionKey := strings.TrimSpace(toolCtx.SessionKey)
	if sessionKey == "" {
		sessionKey = peerID
	}
	return sessionKey
}

func (h *Handler) browserServerStatus(sessionKey string) (mcp.ServerStatus, bool) {
	if h.mcpManager == nil {
		return mcp.ServerStatus{}, false
	}
	activeProfile := h.resolveActiveBrowserProfile(sessionKey)
	if activeProfile == "" {
		return mcp.ServerStatus{}, false
	}
	return h.mcpManager.ServerStatus("browser", activeProfile)
}

func (h *Handler) ensureBrowserServer(ctx context.Context, sessionKey string) (config.BrowserProfileConfig, mcp.ServerStatus, error) {
	profile, ok := h.activeBrowserProfile(sessionKey)
	if !ok {
		return config.BrowserProfileConfig{}, mcp.ServerStatus{}, fmt.Errorf("no browser profile is active for session %q", sessionKey)
	}
	if h.mcpManager == nil {
		return config.BrowserProfileConfig{}, mcp.ServerStatus{}, fmt.Errorf("browser MCP manager is not configured")
	}
	status, err := h.mcpManager.EnsureServer(ctx, "browser", strings.TrimSpace(profile.Name))
	return profile, status, err
}

func (h *Handler) selectBrowserPage(ctx context.Context, peerID, sessionKey string, pageID int) error {
	if pageID <= 0 {
		return fmt.Errorf("page_id must be greater than zero")
	}
	_, err := h.invokeBrowserAliasTool(ctx, peerID, sessionKey, "browser.select_page", map[string]any{
		"pageId":       pageID,
		"bringToFront": false,
	})
	if errors.Is(err, errUnhandledTool) {
		return fmt.Errorf("active browser profile does not expose browser.select_page")
	}
	return err
}

func guardBrowserURL(rawURL string) error {
	target := strings.TrimSpace(rawURL)
	if target == "" {
		return fmt.Errorf("browser navigation is limited to http and https URLs")
	}
	parsed, err := url.Parse(target)
	if err != nil {
		return fmt.Errorf("invalid browser url: %w", err)
	}
	if !strings.EqualFold(parsed.Scheme, "http") && !strings.EqualFold(parsed.Scheme, "https") {
		return fmt.Errorf("browser navigation is limited to http and https URLs")
	}
	if err := blockPrivateURL(parsed); err != nil {
		return fmt.Errorf("browser navigation blocked: %w", err)
	}
	return nil
}

func guardBrowserURLForProfile(rawURL string, profile config.BrowserProfileConfig) error {
	if err := guardBrowserURL(rawURL); err != nil {
		return err
	}
	parsed, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil {
		return fmt.Errorf("invalid browser url: %w", err)
	}
	host := strings.ToLower(strings.TrimSpace(parsed.Hostname()))
	if host == "" {
		return fmt.Errorf("browser navigation requires a hostname")
	}
	for _, pattern := range profile.HostDenylist {
		if browserHostPatternMatches(pattern, host) {
			return fmt.Errorf("browser navigation host %q is blocked by browser profile denylist", host)
		}
	}
	if len(profile.HostAllowlist) == 0 {
		return nil
	}
	for _, pattern := range profile.HostAllowlist {
		if browserHostPatternMatches(pattern, host) {
			return nil
		}
	}
	return fmt.Errorf("browser navigation host %q is not allowed by the active browser profile", host)
}

func browserHostPatternMatches(pattern, host string) bool {
	pattern = strings.ToLower(strings.TrimSpace(pattern))
	host = strings.ToLower(strings.TrimSpace(host))
	if pattern == "" || host == "" {
		return false
	}
	matched, err := path.Match(pattern, host)
	if err == nil && matched {
		return true
	}
	return pattern == host
}

type browserPageMetadata struct {
	PageID int    `json:"pageId"`
	URL    string `json:"url"`
}

func decodeBrowserPageMetadata(result any) ([]browserPageMetadata, error) {
	normalized := normalizeBrowserToolResult(result)
	raw, err := json.Marshal(normalized)
	if err != nil {
		return nil, err
	}
	var pages []browserPageMetadata
	if err := json.Unmarshal(raw, &pages); err != nil {
		return nil, err
	}
	return pages, nil
}

func (h *Handler) guardBrowserPageTarget(ctx context.Context, peerID, sessionKey string, pageID int) error {
	profile, ok := h.activeBrowserProfile(sessionKey)
	if !ok {
		return fmt.Errorf("no browser profile is active for session %q", sessionKey)
	}
	pages, err := h.invokeBrowserAliasTool(ctx, peerID, sessionKey, "browser.list_pages", map[string]any{})
	if err != nil {
		return err
	}
	decoded, err := decodeBrowserPageMetadata(pages)
	if err != nil {
		return fmt.Errorf("could not inspect browser page target for page_id %d", pageID)
	}
	for _, page := range decoded {
		if page.PageID != pageID {
			continue
		}
		return guardBrowserURLForProfile(page.URL, profile)
	}
	return fmt.Errorf("page_id %d was not found in the active browser profile", pageID)
}

func (h *Handler) invokeBrowserPageTool(ctx context.Context, peerID, sessionKey string, pageID int, alias string, args map[string]any) (any, error) {
	if err := h.guardBrowserPageTarget(ctx, peerID, sessionKey, pageID); err != nil {
		return nil, err
	}
	if err := h.selectBrowserPage(ctx, peerID, sessionKey, pageID); err != nil {
		return nil, err
	}
	return h.invokeBrowserAliasTool(ctx, peerID, sessionKey, alias, args)
}

func (h *Handler) invokeBrowserPageToolResult(ctx context.Context, peerID, sessionKey string, pageID int, alias string, args map[string]any) (*mcp.ToolResult, error) {
	if err := h.guardBrowserPageTarget(ctx, peerID, sessionKey, pageID); err != nil {
		return nil, err
	}
	if err := h.selectBrowserPage(ctx, peerID, sessionKey, pageID); err != nil {
		return nil, err
	}
	return h.invokeBrowserAliasToolResult(ctx, peerID, sessionKey, alias, args)
}

func (h *Handler) invokeBrowserPageToolResultAny(ctx context.Context, peerID, sessionKey string, pageID int, aliases []string, args map[string]any) (*mcp.ToolResult, string, error) {
	if err := h.guardBrowserPageTarget(ctx, peerID, sessionKey, pageID); err != nil {
		return nil, "", err
	}
	if err := h.selectBrowserPage(ctx, peerID, sessionKey, pageID); err != nil {
		return nil, "", err
	}
	return h.invokeBrowserAliasToolResultAny(ctx, peerID, sessionKey, aliases, args)
}

func (h *Handler) browserAliasDefs(sessionKey string) []toolDef {
	if !h.browserProfilesEnabled() {
		return nil
	}
	activeProfile := h.resolveActiveBrowserProfile(sessionKey)
	if activeProfile == "" {
		return nil
	}
	defs := make([]toolDef, 0)
	seen := make(map[string]struct{})
	for _, tool := range h.mcpManager.AllTools() {
		if tool.Kind != "browser" || !strings.EqualFold(tool.ProfileName, activeProfile) {
			continue
		}
		alias := browserAliasName(tool.ToolName)
		if isBuiltInToolName(alias) {
			continue
		}
		if _, exists := seen[alias]; exists {
			continue
		}
		seen[alias] = struct{}{}
		schema := tool.InputSchema
		if len(schema) == 0 {
			schema = mustJSONSchema(map[string]any{"type": "object", "properties": map[string]any{}})
		}
		defs = append(defs, toolDef{
			name:        alias,
			description: tool.Description,
			parameters:  schema,
			argHint:     `{}`,
		})
	}
	return defs
}

func (h *Handler) browserAliasTool(ctx context.Context, peerID string, call agent.ToolCall) (any, error) {
	if !strings.HasPrefix(strings.TrimSpace(call.Name), "browser.") || !h.browserProfilesEnabled() {
		return nil, errUnhandledTool
	}
	toolCtx, _ := agent.ToolRunContextFromContext(ctx)
	sessionKey := strings.TrimSpace(toolCtx.SessionKey)
	if sessionKey == "" {
		sessionKey = peerID
	}
	activeProfile := h.resolveActiveBrowserProfile(sessionKey)
	if activeProfile == "" {
		return nil, fmt.Errorf("no browser profile is active for session %q", sessionKey)
	}
	if _, err := h.mcpManager.EnsureServer(ctx, "browser", activeProfile); err != nil {
		return nil, err
	}
	target, ok := h.browserToolByAlias(activeProfile, call.Name)
	if !ok {
		return nil, errUnhandledTool
	}
	return h.mcpManager.CallTool(ctx, target.FullName, call.Arguments)
}

func (h *Handler) browserToolByAlias(activeProfile, alias string) (mcp.RegisteredTool, bool) {
	for _, tool := range h.mcpManager.AllTools() {
		if tool.Kind != "browser" || !strings.EqualFold(tool.ProfileName, activeProfile) {
			continue
		}
		if browserAliasName(tool.ToolName) == alias {
			return tool, true
		}
	}
	return mcp.RegisteredTool{}, false
}

func browserAliasName(toolName string) string {
	return "browser." + strings.TrimSpace(toolName)
}

func isBuiltInToolName(name string) bool {
	for _, def := range toolDefs {
		if def.name == name {
			return true
		}
	}
	return false
}

func (h *Handler) invokeBrowserAliasTool(ctx context.Context, peerID, sessionKey, alias string, args map[string]any) (any, error) {
	raw, err := json.Marshal(args)
	if err != nil {
		return nil, err
	}
	return h.browserAliasTool(agent.WithToolRunContext(ctx, sessionKey, ""), peerID, agent.ToolCall{
		Name:      alias,
		Arguments: raw,
	})
}

func (h *Handler) invokeBrowserAliasToolResult(ctx context.Context, peerID, sessionKey, alias string, args map[string]any) (*mcp.ToolResult, error) {
	raw, err := json.Marshal(args)
	if err != nil {
		return nil, err
	}
	return h.browserAliasToolResult(agent.WithToolRunContext(ctx, sessionKey, ""), peerID, agent.ToolCall{
		Name:      alias,
		Arguments: raw,
	})
}

func (h *Handler) invokeBrowserAliasToolResultAny(ctx context.Context, peerID, sessionKey string, aliases []string, args map[string]any) (*mcp.ToolResult, string, error) {
	var lastErr error
	for _, alias := range aliases {
		result, err := h.invokeBrowserAliasToolResult(ctx, peerID, sessionKey, alias, args)
		if errors.Is(err, errUnhandledTool) {
			lastErr = err
			continue
		}
		return result, alias, err
	}
	if lastErr != nil {
		return nil, "", lastErr
	}
	return nil, "", errUnhandledTool
}

func (h *Handler) browserAliasToolResult(ctx context.Context, peerID string, call agent.ToolCall) (*mcp.ToolResult, error) {
	if !strings.HasPrefix(strings.TrimSpace(call.Name), "browser.") || !h.browserProfilesEnabled() {
		return nil, errUnhandledTool
	}
	toolCtx, _ := agent.ToolRunContextFromContext(ctx)
	sessionKey := strings.TrimSpace(toolCtx.SessionKey)
	if sessionKey == "" {
		sessionKey = peerID
	}
	activeProfile := h.resolveActiveBrowserProfile(sessionKey)
	if activeProfile == "" {
		return nil, fmt.Errorf("no browser profile is active for session %q", sessionKey)
	}
	if _, err := h.mcpManager.EnsureServer(ctx, "browser", activeProfile); err != nil {
		return nil, err
	}
	target, ok := h.browserToolByAlias(activeProfile, call.Name)
	if !ok {
		return nil, errUnhandledTool
	}
	return h.mcpManager.CallToolResult(ctx, target.FullName, call.Arguments)
}

func normalizeBrowserToolResult(result any) any {
	text, ok := result.(string)
	if !ok {
		return result
	}
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return text
	}
	if strings.HasPrefix(trimmed, "{") || strings.HasPrefix(trimmed, "[") {
		var decoded any
		if err := json.Unmarshal([]byte(trimmed), &decoded); err == nil {
			return decoded
		}
	}
	return text
}

func browserStatusPayload(activeProfile string, status mcp.ServerStatus) map[string]any {
	return map[string]any{
		"name":       status.Name,
		"kind":       status.Kind,
		"profile":    browserFirstNonEmpty(strings.TrimSpace(status.ProfileName), activeProfile),
		"hidden":     status.Hidden,
		"connected":  status.Connected,
		"tool_count": status.ToolCount,
		"last_error": status.LastError,
	}
}

func browserMode(mode string) string {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "", "managed":
		return "managed"
	case "auto_connect", "existing_session":
		return "existing_session"
	case "browser_url":
		return "browser_url"
	case "ws_endpoint":
		return "ws_endpoint"
	default:
		return strings.ToLower(strings.TrimSpace(mode))
	}
}

func browserProfilePayload(profile config.BrowserProfileConfig, activeProfile string, server mcp.ServerStatus) map[string]any {
	name := strings.TrimSpace(profile.Name)
	mode := browserMode(profile.Mode)
	payload := map[string]any{
		"name":                      name,
		"mode":                      mode,
		"connection_mode":           mode,
		"headless":                  profile.Headless,
		"isolated":                  profile.Isolated,
		"active":                    strings.EqualFold(name, activeProfile),
		"attached_existing_browser": mode == "existing_session",
		"remote":                    mode == "browser_url" || mode == "ws_endpoint",
		"managed":                   mode == "managed",
		"server":                    browserStatusPayload(name, server),
	}
	if len(profile.HostAllowlist) > 0 {
		payload["host_allowlist"] = append([]string(nil), profile.HostAllowlist...)
		payload["host_allowlist_count"] = len(profile.HostAllowlist)
	}
	if len(profile.HostDenylist) > 0 {
		payload["host_denylist"] = append([]string(nil), profile.HostDenylist...)
		payload["host_denylist_count"] = len(profile.HostDenylist)
	}
	if target := browserFirstNonEmpty(strings.TrimSpace(profile.BrowserURL), strings.TrimSpace(profile.WSEndpoint)); target != "" {
		payload["connection_target"] = target
	}
	if dir := strings.TrimSpace(profile.UserDataDir); dir != "" {
		payload["user_data_dir"] = dir
	}
	if len(profile.WSHeaders) > 0 {
		payload["ws_headers_configured"] = true
		payload["ws_header_count"] = len(profile.WSHeaders)
	}
	return payload
}

func browserFirstNonEmpty(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func buildBrowserScrollFunction(dx, dy int, behavior string) (string, error) {
	behaviorJSON, err := json.Marshal(browserFirstNonEmpty(behavior, "auto"))
	if err != nil {
		return "", err
	}
	return fmt.Sprintf(`() => {
		window.scrollBy({ left: %d, top: %d, behavior: %s });
		return { x: window.scrollX || 0, y: window.scrollY || 0 };
	}`, dx, dy, string(behaviorJSON)), nil
}

func browserSnapshotStateKey(sessionKey, profile string, pageID int) string {
	return fmt.Sprintf("%s|%s|%d", strings.TrimSpace(sessionKey), strings.TrimSpace(profile), pageID)
}

func parseBrowserSnapshotLines(snapshot string) []browserSnapshotLine {
	rawLines := strings.Split(snapshot, "\n")
	out := make([]browserSnapshotLine, 0, len(rawLines))
	for _, raw := range rawLines {
		trimmedLeft := strings.TrimLeft(raw, " ")
		indent := raw[:len(raw)-len(trimmedLeft)]
		trimmed := strings.TrimSpace(raw)
		line := browserSnapshotLine{Raw: raw, Indent: indent, Depth: len(indent) / 2}
		if !strings.HasPrefix(trimmed, "uid=") {
			out = append(out, line)
			continue
		}
		rest := strings.TrimPrefix(trimmed, "uid=")
		uid := rest
		suffix := ""
		if idx := strings.IndexAny(rest, " \t"); idx >= 0 {
			uid = strings.TrimSpace(rest[:idx])
			suffix = strings.TrimSpace(rest[idx:])
		}
		role, name := parseBrowserSnapshotSuffix(suffix)
		line.UID = uid
		line.Suffix = suffix
		line.Role = role
		line.Name = name
		line.Attrs = parseBrowserSnapshotAttributes(suffix)
		out = append(out, line)
	}
	return out
}

func parseBrowserSnapshotSuffix(suffix string) (string, string) {
	remaining := strings.TrimSpace(suffix)
	if remaining == "" {
		return "", ""
	}
	role := ""
	if !strings.HasPrefix(remaining, "\"") {
		role = remaining
		if idx := strings.IndexAny(remaining, " \t"); idx >= 0 {
			role = remaining[:idx]
			remaining = strings.TrimSpace(remaining[idx:])
		} else {
			remaining = ""
		}
	}
	if !strings.HasPrefix(remaining, "\"") {
		return role, ""
	}
	name, _, ok := parseBrowserSnapshotQuotedString(remaining)
	if !ok {
		return role, ""
	}
	return role, name
}

func parseBrowserSnapshotQuotedString(value string) (string, string, bool) {
	if !strings.HasPrefix(value, "\"") {
		return "", value, false
	}
	escaped := false
	for i := 1; i < len(value); i++ {
		switch value[i] {
		case '\\':
			escaped = !escaped
		case '"':
			if escaped {
				escaped = false
				continue
			}
			decoded, err := strconv.Unquote(value[:i+1])
			if err != nil {
				return "", value, false
			}
			return decoded, strings.TrimSpace(value[i+1:]), true
		default:
			escaped = false
		}
	}
	return "", value, false
}

func parseBrowserSnapshotAttributes(suffix string) map[string]any {
	remaining := strings.TrimSpace(suffix)
	if remaining == "" {
		return nil
	}
	if !strings.HasPrefix(remaining, "\"") {
		if idx := strings.IndexAny(remaining, " \t"); idx >= 0 {
			remaining = strings.TrimSpace(remaining[idx:])
		} else {
			remaining = ""
		}
	}
	if strings.HasPrefix(remaining, "\"") {
		_, tail, ok := parseBrowserSnapshotQuotedString(remaining)
		if ok {
			remaining = tail
		}
	}
	remaining = strings.TrimSpace(remaining)
	if remaining == "" {
		return nil
	}
	tokens := tokenizeBrowserSnapshotAttributes(remaining)
	if len(tokens) == 0 {
		return nil
	}
	attrs := make(map[string]any, len(tokens))
	for _, token := range tokens {
		if token == "" {
			continue
		}
		if idx := strings.IndexByte(token, '='); idx >= 0 {
			key := strings.TrimSpace(token[:idx])
			value := strings.TrimSpace(token[idx+1:])
			if key == "" {
				continue
			}
			if decoded, _, ok := parseBrowserSnapshotQuotedString(value); ok {
				attrs[key] = decoded
				continue
			}
			attrs[key] = value
			continue
		}
		attrs[token] = true
	}
	if len(attrs) == 0 {
		return nil
	}
	return attrs
}

func tokenizeBrowserSnapshotAttributes(value string) []string {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	var (
		tokens   []string
		current  strings.Builder
		inQuotes bool
		escaped  bool
	)
	for _, r := range value {
		switch {
		case escaped:
			current.WriteRune(r)
			escaped = false
		case r == '\\':
			current.WriteRune(r)
			escaped = true
		case r == '"':
			current.WriteRune(r)
			inQuotes = !inQuotes
		case (r == ' ' || r == '\t') && !inQuotes:
			if current.Len() > 0 {
				tokens = append(tokens, current.String())
				current.Reset()
			}
		default:
			current.WriteRune(r)
		}
	}
	if current.Len() > 0 {
		tokens = append(tokens, current.String())
	}
	return tokens
}

func formatBrowserSnapshotWithRefs(lines []browserSnapshotLine) string {
	rendered := make([]string, 0, len(lines))
	for _, line := range lines {
		if line.UID == "" || line.Ref == "" {
			rendered = append(rendered, line.Raw)
			continue
		}
		renderedLine := line.Indent + line.Ref
		if line.Suffix != "" {
			renderedLine += " " + line.Suffix
		}
		rendered = append(rendered, renderedLine)
	}
	return strings.Join(rendered, "\n")
}

func formatBrowserRoleSnapshot(lines []browserSnapshotLine) string {
	rendered := make([]string, 0, len(lines))
	for _, line := range lines {
		if line.UID == "" || line.Ref == "" {
			rendered = append(rendered, line.Raw)
			continue
		}
		renderedLine := line.Indent + line.Ref
		if line.Role != "" {
			renderedLine += " " + line.Role
		}
		if line.Name != "" {
			nameJSON, err := json.Marshal(line.Name)
			if err == nil {
				renderedLine += " " + string(nameJSON)
			}
		}
		rendered = append(rendered, renderedLine)
	}
	return strings.Join(rendered, "\n")
}

func buildBrowserAccessibilityTree(lines []browserSnapshotLine) ([]*browserSnapshotNode, []map[string]any, map[string]any) {
	roots := make([]*browserSnapshotNode, 0)
	flat := make([]map[string]any, 0)
	refs := make(map[string]any)
	stack := make([]*browserSnapshotNode, 0)
	for _, line := range lines {
		if strings.TrimSpace(line.UID) == "" || strings.TrimSpace(line.Ref) == "" {
			continue
		}
		node := &browserSnapshotNode{
			Ref:        line.Ref,
			UID:        line.UID,
			Role:       line.Role,
			Name:       line.Name,
			Depth:      line.Depth,
			Attributes: line.Attrs,
		}
		for len(stack) > line.Depth {
			stack = stack[:len(stack)-1]
		}
		if len(stack) == 0 {
			roots = append(roots, node)
		} else {
			parent := stack[len(stack)-1]
			parent.Children = append(parent.Children, node)
		}
		stack = append(stack, node)
		entry := snapshotNodeToMap(node)
		flat = append(flat, entry)
		refs[line.Ref] = entry
	}
	return roots, flat, refs
}

func snapshotNodeToMap(node *browserSnapshotNode) map[string]any {
	entry := map[string]any{
		"ref":   node.Ref,
		"uid":   node.UID,
		"depth": node.Depth,
	}
	if node.Role != "" {
		entry["role"] = node.Role
	}
	if node.Name != "" {
		entry["name"] = node.Name
	}
	if len(node.Attributes) > 0 {
		entry["attributes"] = node.Attributes
	}
	if len(node.Children) > 0 {
		children := make([]map[string]any, 0, len(node.Children))
		for _, child := range node.Children {
			children = append(children, snapshotNodeToMap(child))
		}
		entry["children"] = children
	}
	return entry
}

func snapshotTreeToMaps(nodes []*browserSnapshotNode) []map[string]any {
	out := make([]map[string]any, 0, len(nodes))
	for _, node := range nodes {
		out = append(out, snapshotNodeToMap(node))
	}
	return out
}

func (h *Handler) storeBrowserSnapshotRefs(sessionKey, profile string, pageID int, lines []browserSnapshotLine) ([]map[string]any, map[string]any) {
	key := browserSnapshotStateKey(sessionKey, profile, pageID)
	h.browserSnapshotsMu.Lock()
	defer h.browserSnapshotsMu.Unlock()

	state := h.browserSnapshots[key]
	if state == nil {
		state = &browserSnapshotRefState{
			NextRef:  1,
			UIDToRef: make(map[string]string),
			RefToUID: make(map[string]string),
		}
		h.browserSnapshots[key] = state
	}

	nodes := make([]map[string]any, 0)
	refs := make(map[string]any)
	currentRefToUID := make(map[string]string)
	for i := range lines {
		uid := strings.TrimSpace(lines[i].UID)
		if uid == "" {
			continue
		}
		ref := state.UIDToRef[uid]
		if ref == "" {
			ref = fmt.Sprintf("@e%d", state.NextRef)
			state.NextRef++
			state.UIDToRef[uid] = ref
		}
		lines[i].Ref = ref
		currentRefToUID[ref] = uid
		entry := map[string]any{
			"ref":   ref,
			"uid":   uid,
			"depth": lines[i].Depth,
		}
		if lines[i].Role != "" {
			entry["role"] = lines[i].Role
		}
		if lines[i].Name != "" {
			entry["name"] = lines[i].Name
		}
		if len(lines[i].Attrs) > 0 {
			entry["attributes"] = lines[i].Attrs
		}
		nodes = append(nodes, entry)
		refs[ref] = entry
	}
	state.RefToUID = currentRefToUID
	return nodes, refs
}

func extractBrowserImageContent(result *mcp.ToolResult) (string, string, error) {
	return extractBrowserBinaryContent(result, []string{"image"}, []string{"image/"}, "browser screenshot")
}

func extractBrowserPDFContent(result *mcp.ToolResult) (string, string, error) {
	return extractBrowserBinaryContent(result, []string{"resource", "text"}, []string{"application/pdf"}, "browser pdf export")
}

func extractBrowserBinaryContent(result *mcp.ToolResult, contentTypes []string, mimePrefixes []string, label string) (string, string, error) {
	if result == nil {
		return "", "", fmt.Errorf("empty %s result", label)
	}
	typeAllowed := make(map[string]struct{}, len(contentTypes))
	for _, contentType := range contentTypes {
		typeAllowed[strings.TrimSpace(contentType)] = struct{}{}
	}
	for _, content := range result.Content {
		if _, ok := typeAllowed[strings.TrimSpace(content.Type)]; !ok {
			matchedMime := false
			for _, prefix := range mimePrefixes {
				if strings.HasPrefix(strings.ToLower(strings.TrimSpace(content.MimeType)), strings.ToLower(strings.TrimSpace(prefix))) {
					matchedMime = true
					break
				}
			}
			if !matchedMime {
				continue
			}
		}
		data := strings.TrimSpace(content.Data)
		if data == "" {
			continue
		}
		mimeType := strings.TrimSpace(content.MimeType)
		if mimeType == "" && len(mimePrefixes) > 0 {
			mimeType = strings.TrimSuffix(strings.TrimSpace(mimePrefixes[0]), "/")
		}
		mimeType = browserFirstNonEmpty(mimeType, "application/octet-stream")
		return data, mimeType, nil
	}
	text := strings.TrimSpace(extractBrowserToolText(result))
	if text != "" {
		return "", "", fmt.Errorf("%s returned text only: %s", label, text)
	}
	return "", "", fmt.Errorf("%s did not return binary content", label)
}

func extractBrowserToolText(result *mcp.ToolResult) string {
	if result == nil {
		return ""
	}
	parts := make([]string, 0, len(result.Content))
	for _, content := range result.Content {
		if content.Type == "text" && strings.TrimSpace(content.Text) != "" {
			parts = append(parts, content.Text)
		}
	}
	return strings.Join(parts, "\n")
}

func (h *Handler) resolveBrowserActionUID(sessionKey, profile string, pageID int, uid, ref string) (string, error) {
	if trimmed := strings.TrimSpace(uid); trimmed != "" {
		return trimmed, nil
	}
	trimmedRef := strings.TrimSpace(ref)
	if trimmedRef == "" {
		return "", fmt.Errorf("either uid or ref is required")
	}
	key := browserSnapshotStateKey(sessionKey, profile, pageID)
	h.browserSnapshotsMu.Lock()
	defer h.browserSnapshotsMu.Unlock()
	state := h.browserSnapshots[key]
	if state == nil {
		return "", fmt.Errorf("no browser snapshot refs are available for page %d on profile %q; take browser.snapshot first", pageID, strings.TrimSpace(profile))
	}
	resolved := strings.TrimSpace(state.RefToUID[trimmedRef])
	if resolved == "" {
		return "", fmt.Errorf("browser ref %q is stale or unknown for page %d on profile %q; take browser.snapshot again", trimmedRef, pageID, strings.TrimSpace(profile))
	}
	return resolved, nil
}

func buildBrowserClickCoordsFunction(x, y int, button string, doubleClick bool, delayMS int) string {
	buttonCode := 0
	pressedButtons := 1
	switch strings.ToLower(strings.TrimSpace(button)) {
	case "middle":
		buttonCode = 1
		pressedButtons = 4
	case "right":
		buttonCode = 2
		pressedButtons = 2
	}
	if delayMS < 0 {
		delayMS = 0
	}
	return fmt.Sprintf(`async () => {
		const x = %d;
		const y = %d;
		const delayMs = %d;
		const doubleClick = %t;
		const target = document.elementFromPoint(x, y) ?? document.body ?? document.documentElement ?? document;
		const base = {
			bubbles: true,
			cancelable: true,
			view: window,
			clientX: x,
			clientY: y,
			screenX: window.screenX + x,
			screenY: window.screenY + y,
			button: %d,
		};
		const pressedButtons = %d;
		const dispatch = (type, buttons, detail) => {
			target.dispatchEvent(new MouseEvent(type, { ...base, buttons, detail }));
		};
		dispatch("mousemove", 0, 0);
		dispatch("mousedown", pressedButtons, 1);
		if (delayMs > 0) {
			await new Promise((resolve) => setTimeout(resolve, delayMs));
		}
		dispatch("mouseup", 0, 1);
		dispatch("click", 0, 1);
		if (doubleClick) {
			dispatch("mousedown", pressedButtons, 2);
			dispatch("mouseup", 0, 2);
			dispatch("click", 0, 2);
			dispatch("dblclick", 0, 2);
		}
		return true;
	}`, x, y, delayMS, doubleClick, buttonCode, pressedButtons)
}

func buildBrowserScrollIntoViewFunction(behavior, block, inline string) (string, error) {
	behaviorJSON, err := json.Marshal(browserFirstNonEmpty(behavior, "auto"))
	if err != nil {
		return "", err
	}
	blockJSON, err := json.Marshal(browserFirstNonEmpty(block, "center"))
	if err != nil {
		return "", err
	}
	inlineJSON, err := json.Marshal(browserFirstNonEmpty(inline, "nearest"))
	if err != nil {
		return "", err
	}
	return fmt.Sprintf(`(selector) => {
		const target = document.querySelector(selector);
		if (!target) {
			throw new Error("selector not found");
		}
		target.scrollIntoView({ behavior: %s, block: %s, inline: %s });
		return true;
	}`, string(behaviorJSON), string(blockJSON), string(inlineJSON)), nil
}
