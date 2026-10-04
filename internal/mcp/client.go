// Package mcp provides a Model Context Protocol (MCP) integration layer that
// connects to external MCP servers and exposes their tools, resources, and
// prompts to the Koios agent runtime.
//
// Client transport and protocol handling are delegated to the official MCP Go
// SDK (github.com/modelcontextprotocol/go-sdk): the SDK owns protocol-version
// negotiation, the server/discover and initialize handshakes, transport
// lifecycle, and JSON-RPC framing. Koios targets the MCP 2026-07-28 protocol
// revision and relies on the SDK's own negotiation for servers that only speak
// older revisions.
//
// This package owns the pieces the SDK does not: Koios-facing DTOs, the
// Manager's server lifecycle, runtime names and cache state, config validation
// and merging, tool-header annotation filtering, and the conversion helpers
// between SDK values and Koios types.
package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"regexp"
	"strings"
)

const (
	// ProtocolVersion2026 is the MCP protocol revision Koios targets on the
	// wire, matching the official Go SDK's latest supported version (v1.8).
	// Version negotiation itself is SDK-owned: newer-revision servers negotiate
	// down over server/discover, and servers that only speak older revisions
	// fall back to the legacy initialize handshake at their newest supported
	// version. Koios pins the revision deliberately so a future SDK release
	// adding newer revisions does not silently change the negotiated surface.
	ProtocolVersion2026 = "2026-07-28"
	clientName          = "koios"
	clientVersion       = "1.0"
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

// ─── Helpers ─────────────────────────────────────────────────────────────────

// normalizeResultType maps an empty SDK result type to the complete marker.
func normalizeResultType(resultType string) string {
	resultType = strings.TrimSpace(resultType)
	if resultType == "" {
		return "complete"
	}
	return resultType
}

// isOptionalCapabilityError reports whether err indicates a server that does
// not implement an optional MCP capability. The SDK surfaces method-not-found
// and unsupported-capability errors from the server's JSON-RPC layer; Koios
// treats them as absent capabilities rather than connection failures.
func isOptionalCapabilityError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "method not found") || strings.Contains(msg, "unsupported") || strings.Contains(msg, "not supported")
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
