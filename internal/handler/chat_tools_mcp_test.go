package handler

import (
	"context"
	"testing"

	"github.com/ffimnsr/koios/internal/config"
	"github.com/ffimnsr/koios/internal/mcp"
	"github.com/ffimnsr/koios/internal/session"
)

func TestMCPServerListIncludesConfigServers(t *testing.T) {
	client := &captureMCPClient{tools: []mcp.Tool{{Name: "get_balance_by_asset", Description: "balance"}}}
	server := config.MCPServerConfig{
		Name:      "monaco",
		Transport: "stdio",
		Command:   "/app/bin/monaco-mcp",
		Env:       map[string]string{"MONACO_CLIENT_ID": "secret"},
		Timeout:   "30s",
		Enabled:   true,
	}
	mgr := mcp.NewManagerWithFactory([]config.MCPServerConfig{server}, func(config.MCPServerConfig) mcp.Client {
		return client
	})
	if err := mgr.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}

	h := NewHandler(session.New(10), noopProvider{}, HandlerOptions{
		Model:            "test-model",
		MCPManager:       mgr,
		ConfigMCPServers: []config.MCPServerConfig{server},
	})

	result, err := h.executeMCPServerList(context.Background(), "mach1:alice")
	if err != nil {
		t.Fatalf("executeMCPServerList: %v", err)
	}
	payload, ok := result.(map[string]any)
	if !ok {
		t.Fatalf("expected map payload, got %#v", result)
	}
	servers, ok := payload["servers"].([]map[string]any)
	if !ok {
		t.Fatalf("expected server entries, got %#v", payload["servers"])
	}
	if len(servers) != 1 {
		t.Fatalf("expected one config server, got %#v", servers)
	}
	entry := servers[0]
	if entry["name"] != "monaco" {
		t.Fatalf("unexpected server name: %#v", entry)
	}
	if entry["source"] != "config" {
		t.Fatalf("expected config source, got %#v", entry)
	}
	if entry["user_managed"] != false {
		t.Fatalf("expected non-user-managed config server, got %#v", entry)
	}
	if entry["visibility"] != "shared" {
		t.Fatalf("expected shared visibility, got %#v", entry)
	}
	if entry["connected"] != true || entry["tool_count"] != 1 {
		t.Fatalf("expected runtime status, got %#v", entry)
	}
	env, ok := entry["env"].(map[string]string)
	if !ok || env["MONACO_CLIENT_ID"] != "<redacted>" {
		t.Fatalf("expected redacted env, got %#v", entry["env"])
	}
}
