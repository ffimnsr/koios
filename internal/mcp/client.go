// Package mcp provides a Model Context Protocol (MCP) client that connects to
// external tool servers and exposes their tools to the Koios agent runtime.
//
// MCP uses JSON-RPC 2.0 over pluggable transports:
//   - stdio  — spawns a subprocess and communicates over stdin/stdout
//   - http   — sends JSON-RPC requests to an HTTP endpoint (Streamable HTTP)
//   - sse    — connects via Server-Sent Events (GET /sse + POST /message)
//
// The protocol handshake is:
//  1. Client → initialize (with clientInfo and capabilities)
//  2. Server ← result with protocolVersion and serverInfo
//  3. Client → notifications/initialized
//  4. Client → tools/list to discover available tools
//  5. Client → tools/call to invoke a tool
//
// Tool names are prefixed with the server name so that tools from different
// servers do not conflict: mcp__{server}__{tool}.
package mcp

import (
	"context"
	"encoding/json"
	"fmt"
)

// Tool describes a tool exposed by an MCP server.
type Tool struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"inputSchema"`
}

// Content is one element of a tool result.
type Content struct {
	Type string `json:"type"` // "text", "image", "resource"
	Text string `json:"text,omitempty"`
	// image and resource fields are not expanded further here.
	Data     string `json:"data,omitempty"`
	MimeType string `json:"mimeType,omitempty"`
}

// ToolResult is the value returned by a tools/call response.
type ToolResult struct {
	Content []Content `json:"content"`
	IsError bool      `json:"isError,omitempty"`
}

// Client is the minimal interface a transport must implement to communicate
// with a single MCP server.
type Client interface {
	// Initialize performs the MCP handshake. Must be called before ListTools
	// or CallTool.
	Initialize(ctx context.Context) error
	// ListTools returns all tools advertised by the server.
	ListTools(ctx context.Context) ([]Tool, error)
	// CallTool invokes the named tool with the provided arguments and returns
	// the result content.
	CallTool(ctx context.Context, name string, args map[string]any) (*ToolResult, error)
	// Close releases any resources held by the client (subprocess, connections).
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

type toolsListResult struct {
	Tools []Tool `json:"tools"`
}

type toolsCallParams struct {
	Name      string         `json:"name"`
	Arguments map[string]any `json:"arguments"`
}

// encodeParams marshals v to json.RawMessage, panicking on error (only called
// with static, known-safe values).
func encodeParams(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		panic(fmt.Sprintf("mcp: marshal params: %v", err))
	}
	return b
}
