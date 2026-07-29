// Package mcp provides a Model Context Protocol (MCP) client that connects to
// external MCP servers and exposes their tools, resources, and prompts to the
// Koios agent runtime.
//
// Koios targets the MCP 2026-07-28 protocol revision. It prefers the modern
// `server/discover` handshake and falls back to the legacy `initialize`
// handshake only for compatibility with older servers.
package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"strings"
)

const (
	JSONRPCVersion        = "2.0"
	ProtocolVersion2026   = "2026-07-28"
	legacyProtocolVersion = "2024-11-05"
	clientName            = "koios"
	clientVersion         = "1.0"
)

var httpHeaderNameRE = regexp.MustCompile(`^[A-Za-z0-9!#$%&'*+.^_` + "`" + `|~-]+$`)

// Implementation identifies one MCP client or server implementation.
type Implementation struct {
	Name    string `json:"name"`
	Version string `json:"version,omitempty"`
}

// Tool describes a tool exposed by an MCP server.
type Tool struct {
	Name         string          `json:"name"`
	Title        string          `json:"title,omitempty"`
	Description  string          `json:"description,omitempty"`
	InputSchema  json.RawMessage `json:"inputSchema,omitempty"`
	OutputSchema json.RawMessage `json:"outputSchema,omitempty"`
	Annotations  json.RawMessage `json:"annotations,omitempty"`
}

// Content is one element of tool or prompt content.
type Content struct {
	Type        string          `json:"type"`
	Text        string          `json:"text,omitempty"`
	Data        string          `json:"data,omitempty"`
	MimeType    string          `json:"mimeType,omitempty"`
	URI         string          `json:"uri,omitempty"`
	Blob        string          `json:"blob,omitempty"`
	Resource    json.RawMessage `json:"resource,omitempty"`
	Annotations json.RawMessage `json:"annotations,omitempty"`
}

// ToolResult is the value returned by a tools/call response.
type ToolResult struct {
	ResultType        string          `json:"resultType,omitempty"`
	Content           []Content       `json:"content,omitempty"`
	StructuredContent json.RawMessage `json:"structuredContent,omitempty"`
	IsError           bool            `json:"isError,omitempty"`
	TTLMs             int64           `json:"ttlMs,omitempty"`
	CacheScope        string          `json:"cacheScope,omitempty"`
	RequestID         string          `json:"requestId,omitempty"`
	RequestState      json.RawMessage `json:"requestState,omitempty"`
	InputResponses    json.RawMessage `json:"inputResponses,omitempty"`
	Elicitation       json.RawMessage `json:"elicitation,omitempty"`
}

// InputRequiredResult represents a modern MRTR follow-up response.
type InputRequiredResult struct {
	ResultType   string          `json:"resultType,omitempty"`
	RequestID    string          `json:"requestId,omitempty"`
	RequestState json.RawMessage `json:"requestState,omitempty"`
	Elicitation  json.RawMessage `json:"elicitation,omitempty"`
	TTLMs        int64           `json:"ttlMs,omitempty"`
	CacheScope   string          `json:"cacheScope,omitempty"`
}

// Resource describes one server resource.
type Resource struct {
	Name        string          `json:"name,omitempty"`
	Title       string          `json:"title,omitempty"`
	Description string          `json:"description,omitempty"`
	URI         string          `json:"uri"`
	MimeType    string          `json:"mimeType,omitempty"`
	Size        int64           `json:"size,omitempty"`
	Annotations json.RawMessage `json:"annotations,omitempty"`
}

// ResourceTemplate describes a parameterized resource template.
type ResourceTemplate struct {
	Name        string          `json:"name,omitempty"`
	Title       string          `json:"title,omitempty"`
	Description string          `json:"description,omitempty"`
	URITemplate string          `json:"uriTemplate"`
	MimeType    string          `json:"mimeType,omitempty"`
	Annotations json.RawMessage `json:"annotations,omitempty"`
}

// ResourceContent is one resource payload entry.
type ResourceContent struct {
	URI         string          `json:"uri,omitempty"`
	Name        string          `json:"name,omitempty"`
	MimeType    string          `json:"mimeType,omitempty"`
	Text        string          `json:"text,omitempty"`
	Blob        string          `json:"blob,omitempty"`
	Annotations json.RawMessage `json:"annotations,omitempty"`
}

// ResourceReadResult is the result of resources/read.
type ResourceReadResult struct {
	ResultType string            `json:"resultType,omitempty"`
	Contents   []ResourceContent `json:"contents,omitempty"`
	TTLMs      int64             `json:"ttlMs,omitempty"`
	CacheScope string            `json:"cacheScope,omitempty"`
}

// Prompt describes one server prompt.
type Prompt struct {
	Name        string           `json:"name"`
	Title       string           `json:"title,omitempty"`
	Description string           `json:"description,omitempty"`
	Arguments   []PromptArgument `json:"arguments,omitempty"`
}

// PromptArgument describes one prompt argument.
type PromptArgument struct {
	Name        string `json:"name"`
	Title       string `json:"title,omitempty"`
	Description string `json:"description,omitempty"`
	Required    bool   `json:"required,omitempty"`
}

// PromptMessage is one message returned by prompts/get.
type PromptMessage struct {
	Role    string  `json:"role"`
	Content Content `json:"content"`
}

// PromptGetResult is the result of prompts/get.
type PromptGetResult struct {
	ResultType  string          `json:"resultType,omitempty"`
	Description string          `json:"description,omitempty"`
	Messages    []PromptMessage `json:"messages,omitempty"`
	TTLMs       int64           `json:"ttlMs,omitempty"`
	CacheScope  string          `json:"cacheScope,omitempty"`
}

// DiscoverResult is the modern server/discover handshake response.
type DiscoverResult struct {
	ProtocolVersion string          `json:"protocolVersion,omitempty"`
	Capabilities    json.RawMessage `json:"capabilities,omitempty"`
	ServerInfo      Implementation  `json:"serverInfo,omitempty"`
	Instructions    string          `json:"instructions,omitempty"`
	ResultType      string          `json:"resultType,omitempty"`
	TTLMs           int64           `json:"ttlMs,omitempty"`
	CacheScope      string          `json:"cacheScope,omitempty"`
}

// Notification is one server-to-client MCP notification.
type Notification struct {
	Method string          `json:"method"`
	Params json.RawMessage `json:"params,omitempty"`
}

// Client is one transport-bound MCP client.
type Client interface {
	Discover(ctx context.Context) (*DiscoverResult, error)
	Initialize(ctx context.Context) error
	ListTools(ctx context.Context) ([]Tool, error)
	CallTool(ctx context.Context, name string, args map[string]any) (*ToolResult, error)
	CallToolWithInput(ctx context.Context, name string, args map[string]any, inputResponses, requestState json.RawMessage) (*ToolResult, error)
	ListResources(ctx context.Context) ([]Resource, error)
	ListResourceTemplates(ctx context.Context) ([]ResourceTemplate, error)
	ReadResource(ctx context.Context, uri string) (*ResourceReadResult, error)
	ListPrompts(ctx context.Context) ([]Prompt, error)
	GetPrompt(ctx context.Context, name string, args map[string]any) (*PromptGetResult, error)
	Listen(ctx context.Context) (<-chan Notification, error)
	Cancel(ctx context.Context, requestID any, reason string) error
	Close() error
}

// ─── JSON-RPC 2.0 helpers ────────────────────────────────────────────────────

type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      any             `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      any             `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (e *rpcError) Error() string {
	return fmt.Sprintf("MCP RPC error %d: %s", e.Code, e.Message)
}

// ─── Protocol types ──────────────────────────────────────────────────────────

type requestMeta struct {
	ProtocolVersion    string          `json:"io.modelcontextprotocol/protocolVersion,omitempty"`
	ClientCapabilities map[string]any  `json:"io.modelcontextprotocol/clientCapabilities,omitempty"`
	ClientInfo         *Implementation `json:"io.modelcontextprotocol/clientInfo,omitempty"`
}

type discoverParams struct {
	Meta requestMeta `json:"_meta,omitempty"`
}

type initializeParams struct {
	ProtocolVersion string            `json:"protocolVersion"`
	Capabilities    map[string]any    `json:"capabilities"`
	ClientInfo      map[string]string `json:"clientInfo"`
}

type initializeResult struct {
	ProtocolVersion string            `json:"protocolVersion"`
	Capabilities    map[string]any    `json:"capabilities"`
	ServerInfo      map[string]string `json:"serverInfo"`
}

type listParams struct {
	Cursor *string     `json:"cursor,omitempty"`
	Meta   requestMeta `json:"_meta,omitempty"`
}

type toolsListResult struct {
	Tools      []Tool  `json:"tools"`
	NextCursor *string `json:"nextCursor,omitempty"`
	ResultType string  `json:"resultType,omitempty"`
	TTLMs      int64   `json:"ttlMs,omitempty"`
	CacheScope string  `json:"cacheScope,omitempty"`
}

type toolsCallParams struct {
	Name           string          `json:"name"`
	Arguments      map[string]any  `json:"arguments,omitempty"`
	InputResponses json.RawMessage `json:"inputResponses,omitempty"`
	RequestState   json.RawMessage `json:"requestState,omitempty"`
	Meta           requestMeta     `json:"_meta,omitempty"`
}

type resourcesListResult struct {
	Resources  []Resource `json:"resources"`
	NextCursor *string    `json:"nextCursor,omitempty"`
	ResultType string     `json:"resultType,omitempty"`
	TTLMs      int64      `json:"ttlMs,omitempty"`
	CacheScope string     `json:"cacheScope,omitempty"`
}

type resourceTemplatesListResult struct {
	ResourceTemplates []ResourceTemplate `json:"resourceTemplates"`
	NextCursor        *string            `json:"nextCursor,omitempty"`
	ResultType        string             `json:"resultType,omitempty"`
	TTLMs             int64              `json:"ttlMs,omitempty"`
	CacheScope        string             `json:"cacheScope,omitempty"`
}

type resourceReadParams struct {
	URI  string      `json:"uri"`
	Meta requestMeta `json:"_meta,omitempty"`
}

type promptsListResult struct {
	Prompts    []Prompt `json:"prompts"`
	NextCursor *string  `json:"nextCursor,omitempty"`
	ResultType string   `json:"resultType,omitempty"`
	TTLMs      int64    `json:"ttlMs,omitempty"`
	CacheScope string   `json:"cacheScope,omitempty"`
}

type promptGetParams struct {
	Name      string         `json:"name"`
	Arguments map[string]any `json:"arguments,omitempty"`
	Meta      requestMeta    `json:"_meta,omitempty"`
}

type cancelledNotificationParams struct {
	RequestID any         `json:"requestId"`
	Reason    string      `json:"reason,omitempty"`
	Meta      requestMeta `json:"_meta,omitempty"`
}

func defaultClientInfo() *Implementation {
	return &Implementation{Name: clientName, Version: clientVersion}
}

func defaultClientCapabilities() map[string]any {
	return map[string]any{
		"tools":         map[string]any{},
		"resources":     map[string]any{"read": true, "templates": true, "subscribe": true},
		"prompts":       map[string]any{},
		"elicitation":   map[string]any{"form": true, "url": true},
		"subscriptions": map[string]any{},
	}
}

func defaultRequestMeta() requestMeta {
	return requestMeta{
		ProtocolVersion:    ProtocolVersion2026,
		ClientCapabilities: defaultClientCapabilities(),
		ClientInfo:         defaultClientInfo(),
	}
}

func defaultLegacyInitializeParams() initializeParams {
	return initializeParams{
		ProtocolVersion: legacyProtocolVersion,
		Capabilities:    defaultClientCapabilities(),
		ClientInfo: map[string]string{
			"name":    clientName,
			"version": clientVersion,
		},
	}
}

func isMethodNotFoundError(err error) bool {
	if err == nil {
		return false
	}
	var rpcErr *rpcError
	if AsRPCError(err, &rpcErr) {
		return rpcErr.Code == -32601 || strings.Contains(strings.ToLower(rpcErr.Message), "method not found")
	}
	return strings.Contains(strings.ToLower(err.Error()), "method not found")
}

func isOptionalCapabilityError(err error) bool {
	if err == nil {
		return false
	}
	if isMethodNotFoundError(err) {
		return true
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "unsupported") || strings.Contains(msg, "not supported")
}

func AsRPCError(err error, target **rpcError) bool {
	if err == nil || target == nil {
		return false
	}
	var rpcErr *rpcError
	if errors.As(err, &rpcErr) {
		*target = rpcErr
		return true
	}
	return false
}

// encodeParams marshals v to json.RawMessage. All callers pass JSON-derived
// or static struct values so marshaling should never fail. If it does, an
// error is logged and an empty object is returned so the server keeps running.
func encodeParams(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		slog.Error("mcp: encodeParams: cannot marshal params", "err", err)
		return json.RawMessage("{}")
	}
	return b
}

func normalizeResultType(resultType string) string {
	resultType = strings.TrimSpace(resultType)
	if resultType == "" {
		return "complete"
	}
	return resultType
}

func validateToolHeaderAnnotations(tool Tool) error {
	for _, header := range toolHeaderMappings(tool) {
		if !httpHeaderNameRE.MatchString(header.header) {
			return fmt.Errorf("tool %q has invalid x-mcp-header %q", tool.Name, header.header)
		}
	}
	return nil
}

type toolHeaderMapping struct {
	arg    string
	header string
}

func toolHeaderMappings(tool Tool) []toolHeaderMapping {
	if len(tool.InputSchema) == 0 {
		return nil
	}
	var schema struct {
		Properties map[string]json.RawMessage `json:"properties"`
	}
	if err := json.Unmarshal(tool.InputSchema, &schema); err != nil {
		return nil
	}
	var out []toolHeaderMapping
	for arg, raw := range schema.Properties {
		var prop map[string]json.RawMessage
		if err := json.Unmarshal(raw, &prop); err != nil {
			continue
		}
		rawHeader, ok := prop["x-mcp-header"]
		if !ok {
			continue
		}
		var header string
		if err := json.Unmarshal(rawHeader, &header); err == nil {
			out = append(out, toolHeaderMapping{arg: arg, header: strings.TrimSpace(header)})
			continue
		}
		var obj struct {
			Name string `json:"name"`
		}
		if err := json.Unmarshal(rawHeader, &obj); err == nil && strings.TrimSpace(obj.Name) != "" {
			out = append(out, toolHeaderMapping{arg: arg, header: strings.TrimSpace(obj.Name)})
		}
	}
	return out
}

func filterValidTools(server string, tools []Tool) []Tool {
	out := make([]Tool, 0, len(tools))
	for _, tool := range tools {
		if err := validateToolHeaderAnnotations(tool); err != nil {
			slog.Warn("mcp: dropping invalid tool header annotation", "server", server, "tool", tool.Name, "err", err)
			continue
		}
		out = append(out, tool)
	}
	return out
}
