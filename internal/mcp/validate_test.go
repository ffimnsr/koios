package mcp

import (
	"testing"

	"github.com/ffimnsr/koios/internal/config"
)

func TestValidateServerConfig_Ok(t *testing.T) {
	tests := []struct {
		name string
		cfg  config.MCPServerConfig
	}{
		{"stdio minimal", config.MCPServerConfig{Name: "filesystem", Transport: "stdio", Command: "npx"}},
		{"http minimal", config.MCPServerConfig{Name: "myserver", Transport: "http", URL: "http://localhost:3000/mcp"}},
		{"sse minimal", config.MCPServerConfig{Name: "myserver", Transport: "sse", URL: "http://localhost:3000/sse"}},
		{"with timeout", config.MCPServerConfig{Name: "timed", Transport: "http", URL: "http://localhost/mcp", Timeout: "30s"}},
		{"with args", config.MCPServerConfig{Name: "args", Transport: "stdio", Command: "npx", Args: []string{"-y", "package"}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := ValidateServerConfig(tt.cfg); err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

func TestValidateServerConfig_Errors(t *testing.T) {
	tests := []struct {
		name string
		cfg  config.MCPServerConfig
		want string
	}{
		{"empty name", config.MCPServerConfig{Name: "", Transport: "stdio", Command: "npx"}, "name is required"},
		{"invalid transport", config.MCPServerConfig{Name: "x", Transport: "ws"}, "unsupported transport"},
		{"stdio missing command", config.MCPServerConfig{Name: "x", Transport: "stdio", Command: ""}, "command is required"},
		{"http missing url", config.MCPServerConfig{Name: "x", Transport: "http", URL: ""}, "url is required"},
		{"sse missing url", config.MCPServerConfig{Name: "x", Transport: "sse", URL: ""}, "url is required"},
		{"invalid timeout", config.MCPServerConfig{Name: "x", Transport: "http", URL: "http://localhost", Timeout: "not-a-duration"}, "invalid timeout"},
		{"reserved prefix mcp__", config.MCPServerConfig{Name: "mcp__test", Transport: "stdio", Command: "echo"}, "reserved prefix"},
		{"reserved prefix mcp_plug_", config.MCPServerConfig{Name: "mcp_plug_test", Transport: "stdio", Command: "echo"}, "reserved prefix"},
		{"reserved prefix u_", config.MCPServerConfig{Name: "u_test", Transport: "stdio", Command: "echo"}, "reserved prefix"},
		{"reserved prefix browser_", config.MCPServerConfig{Name: "browser_work", Transport: "stdio", Command: "echo"}, "reserved prefix"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateServerConfig(tt.cfg)
			if err == nil {
				t.Fatal("expected error, got nil")
			}
			if !containsIgnoreCase(err.Error(), tt.want) {
				t.Fatalf("expected error containing %q, got %q", tt.want, err.Error())
			}
		})
	}
}

func TestValidateUserVisibility(t *testing.T) {
	tests := []struct {
		v     string
		valid bool
	}{
		{"private", true},
		{"shared", true},
		{"owner_only", true},
		{"", true},
		{"public", false},
		{"invalid", false},
		{"PRIVATE", true},
	}
	for _, tt := range tests {
		t.Run(tt.v, func(t *testing.T) {
			err := ValidateUserVisibility(tt.v)
			if tt.valid && err != nil {
				t.Fatalf("expected valid, got error: %v", err)
			}
			if !tt.valid && err == nil {
				t.Fatal("expected error, got nil")
			}
		})
	}
}

func containsIgnoreCase(s, substr string) bool {
	s, substr = toLower(s), toLower(substr)
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

func toLower(s string) string {
	b := make([]byte, len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= 'A' && c <= 'Z' {
			c += 32
		}
		b[i] = c
	}
	return string(b)
}
