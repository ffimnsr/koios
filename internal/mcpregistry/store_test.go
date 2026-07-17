package mcpregistry

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")
	s, err := New(dbPath)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestCreateAndGet(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	rec, err := s.Create(ctx, Input{
		OwnerPeerID: "alice",
		Name:        "filesystem",
		Transport:   "stdio",
		Command:     "npx",
		Args:        []string{"-y", "@modelcontextprotocol/server-filesystem", "/tmp"},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if rec.ID == "" {
		t.Fatal("expected non-empty id")
	}
	if rec.Name != "filesystem" {
		t.Fatalf("unexpected name: %q", rec.Name)
	}
	if rec.Transport != "stdio" {
		t.Fatalf("unexpected transport: %q", rec.Transport)
	}
	if rec.Visibility != "private" {
		t.Fatalf("unexpected visibility: %q", rec.Visibility)
	}
	// ApprovalRequired defaults to false (Go zero-value); SQL default 1 only
	// applies when column is omitted. Input with false is explicit.
	if rec.ApprovalRequired {
		t.Fatal("expected approval_required=false by default")
	}
	if rec.Enabled {
		t.Fatal("expected enabled=false")
	}
	if rec.CreatedAt == 0 || rec.UpdatedAt == 0 {
		t.Fatal("expected timestamps")
	}

	got, err := s.Get(ctx, "alice", rec.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Name != "filesystem" || got.Transport != "stdio" {
		t.Fatalf("Get returned wrong data: %+v", got)
	}
}

func TestCreateRejectsEmptyName(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	_, err := s.Create(ctx, Input{OwnerPeerID: "alice", Name: "", Transport: "stdio"})
	if err == nil || !strings.Contains(err.Error(), "name is required") {
		t.Fatalf("expected name error, got: %v", err)
	}
}

func TestListByOwner(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	for i := 0; i < 3; i++ {
		name := "server_" + string(rune('a'+i))
		_, err := s.Create(ctx, Input{OwnerPeerID: "bob", Name: name, Transport: "stdio", Command: "echo"})
		if err != nil {
			t.Fatalf("Create %d: %v", i, err)
		}
	}
	// Add one for alice
	_, err := s.Create(ctx, Input{OwnerPeerID: "alice", Name: "alice-server", Transport: "http", URL: "http://localhost"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	records, err := s.ListByOwner(ctx, "bob")
	if err != nil {
		t.Fatalf("ListByOwner: %v", err)
	}
	if len(records) != 3 {
		t.Fatalf("expected 3 records for bob, got %d", len(records))
	}

	records, err = s.ListByOwner(ctx, "alice")
	if err != nil {
		t.Fatalf("ListByOwner: %v", err)
	}
	if len(records) != 1 {
		t.Fatalf("expected 1 record for alice, got %d", len(records))
	}
}

func TestListAll(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	s.Create(ctx, Input{OwnerPeerID: "alice", Name: "s1", Transport: "stdio", Command: "echo"})
	s.Create(ctx, Input{OwnerPeerID: "bob", Name: "s2", Transport: "http", URL: "http://localhost"})

	all, err := s.ListAll(ctx)
	if err != nil {
		t.Fatalf("ListAll: %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("expected 2 records, got %d", len(all))
	}
}

func TestUpdate(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	rec, err := s.Create(ctx, Input{
		OwnerPeerID: "alice",
		Name:        "old-name",
		Transport:   "stdio",
		Command:     "echo",
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	enabled := true
	updated, err := s.Update(ctx, rec.ID, "alice", UpdateInput{
		Name:    strPtr("new-name"),
		Enabled: &enabled,
	})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if updated.Name != "new-name" {
		t.Fatalf("expected name new-name, got %q", updated.Name)
	}
	if !updated.Enabled {
		t.Fatal("expected enabled=true")
	}
	if updated.UpdatedAt < rec.UpdatedAt {
		t.Fatal("expected updated_at to advance")
	}
}

func TestUpdateNotOwned(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	rec, err := s.Create(ctx, Input{
		OwnerPeerID: "alice",
		Name:        "my-server",
		Transport:   "stdio",
		Command:     "echo",
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	_, err = s.Update(ctx, rec.ID, "bob", UpdateInput{})
	if err == nil {
		t.Fatal("expected error for non-owner update")
	}
}

func TestDelete(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	rec, err := s.Create(ctx, Input{OwnerPeerID: "alice", Name: "del-me", Transport: "stdio", Command: "echo"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	if err := s.Delete(ctx, "alice", rec.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	_, err = s.Get(ctx, "alice", rec.ID)
	if err == nil {
		t.Fatal("expected error after delete")
	}
}

func TestDeleteNotOwned(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	rec, err := s.Create(ctx, Input{OwnerPeerID: "alice", Name: "mine", Transport: "stdio", Command: "echo"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	err = s.Delete(ctx, "bob", rec.ID)
	if err == nil {
		t.Fatal("expected error for non-owner delete")
	}
}

func TestToMCPServerConfig(t *testing.T) {
	rec := &ServerRecord{
		ID:               "abc123def456",
		OwnerPeerID:      "alice",
		Name:             "filesystem",
		Transport:        "stdio",
		Command:          "npx",
		Args:             []string{"-y", "@modelcontextprotocol/server-filesystem"},
		Env:              map[string]string{"KEY": "val"},
		URL:              "",
		Headers:          nil,
		Timeout:          "30s",
		Enabled:          true,
		Visibility:       "private",
		ApprovalRequired: true,
	}

	cfg := rec.ToMCPServerConfig()
	if cfg.Name != "u_alice_abc123de" {
		t.Fatalf("unexpected runtime name: %q", cfg.Name)
	}
	if cfg.Transport != "stdio" {
		t.Fatalf("unexpected transport: %q", cfg.Transport)
	}
	if cfg.Command != "npx" {
		t.Fatalf("unexpected command: %q", cfg.Command)
	}
	if len(cfg.Args) != 2 {
		t.Fatalf("expected 2 args, got %d", len(cfg.Args))
	}
	if cfg.Env["KEY"] != "val" {
		t.Fatalf("expected env KEY=val, got %v", cfg.Env)
	}
	if cfg.Kind != "user" {
		t.Fatalf("expected kind user, got %q", cfg.Kind)
	}
	if cfg.ProfileName != "alice" {
		t.Fatalf("expected profile alice, got %q", cfg.ProfileName)
	}
	if !strings.HasPrefix(cfg.ToolNamePrefix, "mcp__u_alice_abc123de__") {
		t.Fatalf("unexpected tool prefix: %q", cfg.ToolNamePrefix)
	}
}

func TestRedactedEnv(t *testing.T) {
	env := map[string]string{"API_KEY": "secret123", "TOKEN": "abc"}
	redacted := RedactedEnv(env)
	if redacted["API_KEY"] != "<redacted>" || redacted["TOKEN"] != "<redacted>" {
		t.Fatalf("unexpected redacted: %v", redacted)
	}
	if len(redacted) != 2 {
		t.Fatalf("expected 2 keys, got %d", len(redacted))
	}

	if RedactedEnv(nil) != nil {
		t.Fatal("expected nil for empty env")
	}
}

func TestRedactedHeaders(t *testing.T) {
	headers := map[string]string{"Authorization": "Bearer secret"}
	redacted := RedactedHeaders(headers)
	if redacted["Authorization"] != "<redacted>" {
		t.Fatalf("unexpected redacted: %v", redacted)
	}

	if RedactedHeaders(nil) != nil {
		t.Fatal("expected nil for empty headers")
	}
}

func TestRuntimeName(t *testing.T) {
	tests := []struct {
		owner, id, want string
	}{
		{"alice", "abc123def456", "u_alice_abc123de"},
		{"alice_long_name_here", "abc123def4567890", "u_alice_lo_abc123de"},
		{"bob", "xyz", "u_bob_xyz"},
		{"", "", "u__"},
	}
	for _, tt := range tests {
		got := RuntimeName(tt.owner, tt.id)
		if got != tt.want {
			t.Errorf("RuntimeName(%q, %q) = %q, want %q", tt.owner, tt.id, got, tt.want)
		}
	}
}

func TestRandomID(t *testing.T) {
	id1, err := RandomID()
	if err != nil {
		t.Fatalf("RandomID: %v", err)
	}
	if len(id1) != 16 {
		t.Fatalf("expected 16 chars, got %d", len(id1))
	}
	id2, _ := RandomID()
	if id1 == id2 {
		t.Fatal("expected unique ids")
	}
}

func TestCreateWithSecretsAreRedacted(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	rec, err := s.Create(ctx, Input{
		OwnerPeerID: "alice",
		Name:        "secret-server",
		Transport:   "http",
		URL:         "http://example.com/mcp",
		Headers:     map[string]string{"Authorization": "Bearer my-token"},
		Env:         map[string]string{"API_KEY": "supersecret"},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	// Get returns the raw values for internal use
	got, err := s.Get(ctx, "alice", rec.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Headers["Authorization"] != "Bearer my-token" {
		t.Fatalf("expected raw header value for get, got: %q", got.Headers["Authorization"])
	}
	if got.Env["API_KEY"] != "supersecret" {
		t.Fatalf("expected raw env value for get, got: %q", got.Env["API_KEY"])
	}

	// Redacted functions should replace values
	redactedEnv := RedactedEnv(got.Env)
	if redactedEnv["API_KEY"] != "<redacted>" {
		t.Fatalf("expected redacted env value, got: %q", redactedEnv["API_KEY"])
	}
	redactedHeaders := RedactedHeaders(got.Headers)
	if redactedHeaders["Authorization"] != "<redacted>" {
		t.Fatalf("expected redacted header, got: %q", redactedHeaders["Authorization"])
	}
}

func strPtr(s string) *string { return &s }
