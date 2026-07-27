package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/ffimnsr/koios/internal/app"
	"github.com/ffimnsr/koios/internal/config"
)

func TestHideSecretCommandJSON(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	cmd := NewRootCommand(app.BuildInfo{Version: "test"}, nil)
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"hide-secret", "test-api-key", "--json"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	var payload map[string]any
	if err := json.Unmarshal(out.Bytes(), &payload); err != nil {
		t.Fatalf("json.Unmarshal: %v\n%s", err, out.String())
	}
	hidden, _ := payload["hidden_secret"].(string)
	if !config.IsHiddenSecret(hidden) {
		t.Fatalf("expected hidden secret, got %#v", payload)
	}
	configUsage, _ := payload["config_usage"].(string)
	if !strings.Contains(configUsage, hidden) {
		t.Fatalf("expected config usage to contain hidden secret, got %q", configUsage)
	}
	plaintext, err := config.RevealSecret(hidden)
	if err != nil {
		t.Fatalf("RevealSecret: %v", err)
	}
	if plaintext != "test-api-key" {
		t.Fatalf("plaintext = %q", plaintext)
	}
}

func TestHideSecretCommandReadsStdin(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	cmd := NewRootCommand(app.BuildInfo{Version: "test"}, nil)
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetIn(strings.NewReader("stdin-secret\n"))
	cmd.SetArgs([]string{"hide-secret", "--stdin"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !config.IsHiddenSecret(strings.Split(strings.TrimSpace(out.String()), "\n")[0]) {
		t.Fatalf("expected first output line to be a hidden secret, got %q", out.String())
	}
}

// writeMinimalConfig writes a minimal koios.config.toml with one LLM profile.
func writeMinimalConfig(t *testing.T, dir, apiKey string) {
	t.Helper()
	toml := strings.Join([]string{
		"[server]",
		`listen_addr = ":9090"`,
		"",
		"[llm]",
		`default_profile = "default"`,
		"",
		"[[llm.profiles]]",
		`name = "default"`,
		`provider = "openai"`,
		`model = "gpt-4o"`,
		`api_key = "` + apiKey + `"`,
		"",
		"[workspace]",
		`root = "./workspace"`,
		"",
	}, "\n")
	if err := os.WriteFile(filepath.Join(dir, config.DefaultConfigFile), []byte(toml), 0o600); err != nil {
		t.Fatal(err)
	}
}

func writeMinimalMultiKeyProfileConfig(t *testing.T, dir string, apiKeys []string) {
	t.Helper()
	quoted := make([]string, 0, len(apiKeys))
	for _, apiKey := range apiKeys {
		quoted = append(quoted, strconv.Quote(apiKey))
	}
	toml := strings.Join([]string{
		"[server]",
		`listen_addr = ":9090"`,
		"",
		"[llm]",
		`default_profile = "default"`,
		"",
		"[[llm.profiles]]",
		`name = "default"`,
		`provider = "openai"`,
		`model = "gpt-4o"`,
		`api_keys = [` + strings.Join(quoted, ", ") + `]`,
		"",
		"[workspace]",
		`root = "./workspace"`,
		"",
	}, "\n")
	if err := os.WriteFile(filepath.Join(dir, config.DefaultConfigFile), []byte(toml), 0o600); err != nil {
		t.Fatal(err)
	}
}

func writeMinimalMultiKeyRootConfig(t *testing.T, dir string, apiKeys []string) {
	t.Helper()
	quoted := make([]string, 0, len(apiKeys))
	for _, apiKey := range apiKeys {
		quoted = append(quoted, strconv.Quote(apiKey))
	}
	toml := strings.Join([]string{
		"[server]",
		`listen_addr = ":9090"`,
		"",
		"[llm]",
		`provider = "openai"`,
		`model = "gpt-4o"`,
		`api_keys = [` + strings.Join(quoted, ", ") + `]`,
		"",
		"[workspace]",
		`root = "./workspace"`,
		"",
	}, "\n")
	if err := os.WriteFile(filepath.Join(dir, config.DefaultConfigFile), []byte(toml), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestHideSecretSetLLMProfileAPIKey(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	dir := t.TempDir()
	writeMinimalConfig(t, dir, "plaintext-key")

	cmdCtx := &commandContext{build: app.BuildInfo{Version: "test"}, cwd: dir}
	cmd := NewRootCommand(app.BuildInfo{Version: "test"}, nil)
	cmd.SetArgs([]string{"hide-secret", "--set", "llm.default.api_key", "my-new-secret"})
	// Route the command through a context with the right cwd.
	_ = cmdCtx // we use NewRootCommand directly, so set cwd via PersistentPreRun
	rootCmd := &commandContext{build: app.BuildInfo{Version: "test"}, cwd: dir}
	rootCmd2 := NewRootCommand(rootCmd.build, nil)
	var out bytes.Buffer
	rootCmd2.SetOut(&out)
	rootCmd2.SetErr(&out)
	rootCmd2.SetArgs([]string{"hide-secret", "--set", "llm.default.api_key", "my-new-secret"})
	// Inject cwd by temporarily changing working directory.
	orig, _ := os.Getwd()
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(orig) })

	if err := rootCmd2.Execute(); err != nil {
		t.Fatalf("Execute: %v", err)
	}

	// Reload the config and verify the api_key is now hidden and decryptable.
	cfgPath := filepath.Join(dir, config.DefaultConfigFile)
	cfg, err := config.LoadFromPath(cfgPath)
	if err != nil {
		t.Fatalf("LoadFromPath: %v", err)
	}
	found := false
	for _, p := range cfg.ModelProfiles {
		if p.Name == "default" {
			found = true
			if p.APIKey != "my-new-secret" {
				t.Fatalf("api_key decoded = %q, want %q", p.APIKey, "my-new-secret")
			}
		}
	}
	if !found {
		t.Fatal("default profile not found after rewrite")
	}

	// Verify the raw TOML contains a hidden blob, not the plaintext.
	raw, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "my-new-secret") {
		t.Fatal("plaintext secret found in rewritten config; expected hidden blob")
	}
	if strings.Contains(string(raw), "plaintext-key") {
		t.Fatal("old plaintext key found in rewritten config")
	}
}

func TestHideSecretSetLLMProfileAPIKeysEntry(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	dir := t.TempDir()
	writeMinimalMultiKeyProfileConfig(t, dir, []string{"plain-a", "plain-b"})
	orig, _ := os.Getwd()
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(orig) })

	cmd := NewRootCommand(app.BuildInfo{Version: "test"}, nil)
	cmd.SetArgs([]string{"hide-secret", "--set", "llm.default.api_keys.1", "profile-rotated-secret"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute: %v", err)
	}

	cfgPath := filepath.Join(dir, config.DefaultConfigFile)
	cfg, err := config.LoadFromPath(cfgPath)
	if err != nil {
		t.Fatalf("LoadFromPath: %v", err)
	}
	if len(cfg.ModelProfiles) != 1 {
		t.Fatalf("expected 1 profile, got %d", len(cfg.ModelProfiles))
	}
	if got := cfg.ModelProfiles[0].APIKeys; len(got) != 2 || got[1] != "profile-rotated-secret" {
		t.Fatalf("profile api_keys = %#v", got)
	}

	raw, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "profile-rotated-secret") || strings.Contains(string(raw), "plain-b") {
		t.Fatal("expected rewritten profile api_keys entry to stay hidden")
	}
}

func TestHideSecretSetRootLLMAPIKeysEntry(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	dir := t.TempDir()
	writeMinimalMultiKeyRootConfig(t, dir, []string{"plain-a", "plain-b"})
	orig, _ := os.Getwd()
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(orig) })

	cmd := NewRootCommand(app.BuildInfo{Version: "test"}, nil)
	cmd.SetArgs([]string{"hide-secret", "--set", "llm.api_keys.0", "root-rotated-secret"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute: %v", err)
	}

	cfgPath := filepath.Join(dir, config.DefaultConfigFile)
	cfg, err := config.LoadFromPath(cfgPath)
	if err != nil {
		t.Fatalf("LoadFromPath: %v", err)
	}
	if got := cfg.APIKeys; len(got) != 2 || got[0] != "root-rotated-secret" {
		t.Fatalf("root api_keys = %#v", got)
	}

	raw, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "root-rotated-secret") || strings.Contains(string(raw), "plain-a") {
		t.Fatal("expected rewritten root api_keys entry to stay hidden")
	}
}

func TestHideSecretSetUnsupportedField(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	dir := t.TempDir()
	writeMinimalConfig(t, dir, "k")
	orig, _ := os.Getwd()
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(orig) })

	cmd := NewRootCommand(app.BuildInfo{Version: "test"}, nil)
	cmd.SetArgs([]string{"hide-secret", "--set", "server.listen_addr", "value"})
	err := cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "unsupported field") {
		t.Fatalf("expected unsupported field error, got %v", err)
	}
}

func TestHideSecretSetMissingProfile(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	dir := t.TempDir()
	writeMinimalConfig(t, dir, "k")
	orig, _ := os.Getwd()
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(orig) })

	cmd := NewRootCommand(app.BuildInfo{Version: "test"}, nil)
	cmd.SetArgs([]string{"hide-secret", "--set", "llm.nonexistent.api_key", "value"})
	err := cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("expected profile not found error, got %v", err)
	}
}

func TestHideSecretSetEmptyFieldWithoutSecret(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	dir := t.TempDir()
	// Config with empty api_key and no secret provided.
	toml := strings.Join([]string{
		"[server]",
		`listen_addr = ":9090"`,
		"",
		"[llm]",
		`default_profile = "default"`,
		"",
		"[[llm.profiles]]",
		`name = "default"`,
		`provider = "openai"`,
		`model = "gpt-4o"`,
		`api_key = ""`,
		"",
		"[workspace]",
		`root = "./workspace"`,
		"",
	}, "\n")
	if err := os.WriteFile(filepath.Join(dir, config.DefaultConfigFile), []byte(toml), 0o600); err != nil {
		t.Fatal(err)
	}
	orig, _ := os.Getwd()
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(orig) })

	cmd := NewRootCommand(app.BuildInfo{Version: "test"}, nil)
	cmd.SetArgs([]string{"hide-secret", "--set", "llm.default.api_key"})
	err := cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "empty value") {
		t.Fatalf("expected empty value error, got %v", err)
	}
}
