package handler

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/ffimnsr/koios/internal/agent"
	"github.com/ffimnsr/koios/internal/config"
	"github.com/ffimnsr/koios/internal/mcp"
	"github.com/ffimnsr/koios/internal/session"
)

type captureMCPClient struct {
	tools    []mcp.Tool
	lastName string
	lastArgs map[string]any
}

func (c *captureMCPClient) Initialize(context.Context) error { return nil }

func (c *captureMCPClient) ListTools(context.Context) ([]mcp.Tool, error) {
	return c.tools, nil
}

func (c *captureMCPClient) CallTool(_ context.Context, name string, args map[string]any) (*mcp.ToolResult, error) {
	c.lastName = name
	c.lastArgs = args
	return &mcp.ToolResult{Content: []mcp.Content{{Type: "text", Text: "ok"}}}, nil
}

func (c *captureMCPClient) Close() error { return nil }

func TestExecuteToolInjectsPeerIDForMonacoMCPTools(t *testing.T) {
	client := &captureMCPClient{tools: []mcp.Tool{{Name: "get_profile", Description: "profile"}}}
	mgr := mcp.NewManagerWithFactory([]config.MCPServerConfig{{
		Name:      "monaco",
		Enabled:   true,
		Transport: "stdio",
		Command:   "ignored",
	}}, func(config.MCPServerConfig) mcp.Client {
		return client
	})
	if err := mgr.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}

	h := NewHandler(session.New(10), noopProvider{}, HandlerOptions{
		Model:      "test-model",
		Timeout:    5 * time.Second,
		MCPManager: mgr,
	})

	result, err := h.ExecuteTool(context.Background(), "mach1:did:privy:abc", agent.ToolCall{
		Name:      mcp.ToolName("monaco", "get_profile"),
		Arguments: json.RawMessage(`{"venue_env":"staging"}`),
	})
	if err != nil {
		t.Fatalf("ExecuteTool: %v", err)
	}
	if got, ok := result.(string); !ok || got != "ok" {
		t.Fatalf("unexpected tool result: %#v", result)
	}
	if client.lastName != "get_profile" {
		t.Fatalf("unexpected MCP tool name: %q", client.lastName)
	}
	if got := client.lastArgs["peer_id"]; got != "mach1:did:privy:abc" {
		t.Fatalf("expected injected peer_id, got %#v", client.lastArgs)
	}
}

func TestExecuteToolKeepsExplicitMonacoAuthFields(t *testing.T) {
	client := &captureMCPClient{tools: []mcp.Tool{{Name: "get_profile", Description: "profile"}}}
	mgr := mcp.NewManagerWithFactory([]config.MCPServerConfig{{
		Name:      "monaco",
		Enabled:   true,
		Transport: "stdio",
		Command:   "ignored",
	}}, func(config.MCPServerConfig) mcp.Client {
		return client
	})
	if err := mgr.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}

	h := NewHandler(session.New(10), noopProvider{}, HandlerOptions{
		Model:      "test-model",
		Timeout:    5 * time.Second,
		MCPManager: mgr,
	})

	_, err := h.ExecuteTool(context.Background(), "mach1:did:privy:abc", agent.ToolCall{
		Name: mcp.ToolName("monaco", "get_profile"),
		Arguments: json.RawMessage(`{
			"peer_id":"mach1:override",
			"privy_user_id":"did:privy:explicit",
			"venue_env":"production"
		}`),
	})
	if err != nil {
		t.Fatalf("ExecuteTool: %v", err)
	}
	if got := client.lastArgs["peer_id"]; got != "mach1:override" {
		t.Fatalf("expected explicit peer_id preserved, got %#v", client.lastArgs)
	}
	if got := client.lastArgs["privy_user_id"]; got != "did:privy:explicit" {
		t.Fatalf("expected explicit privy_user_id preserved, got %#v", client.lastArgs)
	}
}

func TestExecuteToolDoesNotInjectPeerIDForOtherMCPServers(t *testing.T) {
	client := &captureMCPClient{tools: []mcp.Tool{{Name: "read_file", Description: "read file"}}}
	mgr := mcp.NewManagerWithFactory([]config.MCPServerConfig{{
		Name:      "filesystem",
		Enabled:   true,
		Transport: "stdio",
		Command:   "ignored",
	}}, func(config.MCPServerConfig) mcp.Client {
		return client
	})
	if err := mgr.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}

	h := NewHandler(session.New(10), noopProvider{}, HandlerOptions{
		Model:      "test-model",
		Timeout:    5 * time.Second,
		MCPManager: mgr,
	})

	_, err := h.ExecuteTool(context.Background(), "mach1:did:privy:abc", agent.ToolCall{
		Name:      mcp.ToolName("filesystem", "read_file"),
		Arguments: json.RawMessage(`{"path":"README.md"}`),
	})
	if err != nil {
		t.Fatalf("ExecuteTool: %v", err)
	}
	if _, ok := client.lastArgs["peer_id"]; ok {
		t.Fatalf("did not expect peer_id injection for non-monaco MCP tool: %#v", client.lastArgs)
	}
}
