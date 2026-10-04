package mcp

import (
	"context"
	"sort"
	"testing"

	"github.com/ffimnsr/koios/internal/config"
	"github.com/ffimnsr/koios/internal/mcpregistry"
)

func testUserRecord(owner, id, name, transport, target string, enabled bool) mcpregistry.ServerRecord {
	return mcpregistry.ServerRecord{
		OwnerPeerID: owner,
		ID:          id,
		Name:        name,
		Transport:   transport,
		URL:         target,
		Command:     "echo",
		Args:        []string{"-n"},
		Env:         map[string]string{"A": "b"},
		Headers:     map[string]string{"X-Test": "1"},
		Timeout:     "10s",
		Enabled:     enabled,
	}
}

// TestRegistryRecordConvertsToAcceptedServerConfig verifies user-managed
// records convert into MCPServerConfig values that the merge path accepts,
// including the derived u_ runtime names that own the user namespace.
func TestRegistryRecordConvertsToAcceptedServerConfig(t *testing.T) {
	for _, rec := range []mcpregistry.ServerRecord{
		testUserRecord("mach1:alice", "ab12cd34ef", "monaco", "http", "http://127.0.0.1:9/mcp", true),
		testUserRecord("mach1:bob", "2233", "files", "stdio", "", false),
	} {
		cfg := rec.ToMCPServerConfig()
		if cfg.Name != mcpregistry.RuntimeName(rec.OwnerPeerID, rec.ID) {
			t.Fatalf("unexpected runtime name %q", cfg.Name)
		}
		if err := validateUserServerConfig(cfg); err != nil {
			t.Fatalf("user config %q rejected: %v", cfg.Name, err)
		}
		if cfg.Timeout != "10s" || cfg.Enabled != rec.Enabled {
			t.Fatalf("record fields not preserved: %#v", cfg)
		}
	}
	// The u_ namespace stays reserved for static and extension sources.
	rec := testUserRecord("mach1:alice", "ab12cd34ef", "monaco", "http", "http://x", true)
	cfg := rec.ToMCPServerConfig()
	if err := ValidateServerConfig(cfg); err == nil {
		t.Fatal("ValidateServerConfig must keep rejecting u_ names from non-user sources")
	}
}

// TestMergeServerConfigsPreservesSourcesAndOrdering verifies MergeServerConfigs
// keeps static, extension, and user-managed entries with deterministic output.
func TestMergeServerConfigsPreservesSourcesAndOrdering(t *testing.T) {
	static := []config.MCPServerConfig{{Name: "static_srv", Transport: "http", URL: "http://s/mcp", Enabled: true}}
	extension := []config.MCPServerConfig{{Name: "ext_srv", Transport: "stdio", Command: "ext", Enabled: true}}
	user := []mcpregistry.ServerRecord{testUserRecord("mach1:alice", "ab12cd34ef", "user_srv", "http", "http://u/mcp", true)}

	merged, err := MergeServerConfigs(static, extension, user)
	if err != nil {
		t.Fatalf("MergeServerConfigs: %v", err)
	}
	if len(merged) != 3 {
		t.Fatalf("expected three merged entries, got %#v", merged)
	}
	if !sort.SliceIsSorted(merged, func(i, j int) bool { return merged[i].Name < merged[j].Name }) {
		t.Fatalf("merged output must be sorted by name: %#v", merged)
	}
	names := map[string]config.MCPServerConfig{}
	for _, cfg := range merged {
		names[cfg.Name] = cfg
	}
	if _, ok := names["static_srv"]; !ok {
		t.Fatal("static source missing from merge")
	}
	if _, ok := names["ext_srv"]; !ok {
		t.Fatal("extension source missing from merge")
	}
	userCfg, ok := names[mcpregistry.RuntimeName("mach1:alice", "ab12cd34ef")]
	if !ok {
		t.Fatal("user-managed source missing from merge")
	}
	if userCfg.Transport != "http" || userCfg.URL != "http://u/mcp" || userCfg.Headers["X-Test"] != "1" {
		t.Fatalf("user fields not preserved: %#v", userCfg)
	}
}

// TestMergeServerConfigsRejectsDuplicateRuntimeNames verifies runtime-name
// collisions across sources are rejected.
func TestMergeServerConfigsRejectsDuplicateRuntimeNames(t *testing.T) {
	if _, err := MergeServerConfigs(
		[]config.MCPServerConfig{{Name: "dupe", Transport: "stdio", Command: "a"}},
		[]config.MCPServerConfig{{Name: "dupe", Transport: "stdio", Command: "b"}},
		nil,
	); err == nil {
		t.Fatal("expected a static/extension name collision to fail")
	}

	// Two distinct user records whose owner and id tokens truncate to the same
	// runtime name must collide.
	dupe1 := testUserRecord("mach1:alice", "ab12cd34xx", "one", "http", "http://a/mcp", true)
	dupe2 := testUserRecord("mach1:alice2", "ab12cd34yy", "two", "http", "http://b/mcp", true)
	if mcpregistry.RuntimeName(dupe1.OwnerPeerID, dupe1.ID) != mcpregistry.RuntimeName(dupe2.OwnerPeerID, dupe2.ID) {
		t.Fatal("fixture records must share a runtime name")
	}
	if _, err := MergeServerConfigs(nil, nil, []mcpregistry.ServerRecord{dupe1, dupe2}); err == nil {
		t.Fatal("expected a user runtime-name collision to fail")
	}
}

// TestMergeDisabledUserServerNotConnectedUntilEnabled verifies a merged
// disabled user-managed server does not connect until its record is enabled.
func TestMergeDisabledUserServerNotConnectedUntilEnabled(t *testing.T) {
	fixture := newSDKHTTPServer(t)
	rec := testUserRecord("mach1:alice", "ab12cd34ef", "monaco", "http", fixture.srv.URL, false)

	merged, err := MergeServerConfigs(nil, nil, []mcpregistry.ServerRecord{rec})
	if err != nil {
		t.Fatalf("MergeServerConfigs: %v", err)
	}
	if len(merged) != 1 || merged[0].Enabled {
		t.Fatalf("expected the disabled record to stay disabled, got %#v", merged)
	}

	disabled := NewManager(merged)
	t.Cleanup(disabled.Close)
	if disabled.HasServer(merged[0].Name) {
		t.Fatal("disabled user servers must not be registered")
	}

	rec.Enabled = true
	merged, err = MergeServerConfigs(nil, nil, []mcpregistry.ServerRecord{rec})
	if err != nil {
		t.Fatalf("re-merge: %v", err)
	}
	enabled := NewManager(merged)
	t.Cleanup(enabled.Close)
	if err := enabled.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	status, ok := enabled.ServerStatusByName(merged[0].Name)
	if !ok || !status.Connected {
		t.Fatalf("expected the enabled user server to connect, got %#v ok=%v", status, ok)
	}
}
