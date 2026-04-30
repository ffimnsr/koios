package config

import (
	"bytes"
	"os"
	"os/user"
	"path/filepath"
	"strings"
	"testing"
)

func TestHideSecretRoundTrip(t *testing.T) {
	configureHiddenSecretTestEnv(t)
	hidden, err := HideSecret("test-key")
	if err != nil {
		t.Fatalf("HideSecret: %v", err)
	}
	if !IsHiddenSecret(hidden) {
		t.Fatalf("expected hidden secret prefix, got %q", hidden)
	}
	plaintext, err := RevealSecret(hidden)
	if err != nil {
		t.Fatalf("RevealSecret: %v", err)
	}
	if plaintext != "test-key" {
		t.Fatalf("plaintext = %q", plaintext)
	}
	configDir, err := hiddenSecretUserConfigDir()
	if err != nil {
		t.Fatalf("hiddenSecretUserConfigDir: %v", err)
	}
	entries, err := os.ReadDir(filepath.Join(configDir, "koios", "secrets"))
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected one master key file, got %d", len(entries))
	}
}

func TestLoadFromPathDecryptsAndPreservesHiddenSecrets(t *testing.T) {
	configureHiddenSecretTestEnv(t)
	hidden, err := HideSecret("api-hidden")
	if err != nil {
		t.Fatalf("HideSecret: %v", err)
	}
	dir := t.TempDir()
	path := filepath.Join(dir, DefaultConfigFile)
	content := strings.Join([]string{
		"[llm]",
		"default_profile = \"default\"",
		"",
		"[[llm.profiles]]",
		"name = \"default\"",
		"provider = \"openai\"",
		"model = \"gpt-4o\"",
		"api_key = \"" + hidden + "\"",
		"",
		"[workspace]",
		"root = \"./workspace\"",
		"",
	}, "\n")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	cfg, err := LoadFromPath(path)
	if err != nil {
		t.Fatalf("LoadFromPath: %v", err)
	}
	if cfg.APIKey != "api-hidden" {
		t.Fatalf("cfg.APIKey = %q", cfg.APIKey)
	}
	reEncoded := EncodeTOML(cfg, false)
	if !strings.Contains(reEncoded, hidden) {
		t.Fatalf("expected re-encoded config to preserve hidden secret\n%s", reEncoded)
	}
	if strings.Contains(reEncoded, `api_key = "api-hidden"`) {
		t.Fatalf("expected re-encoded config to avoid plaintext secret\n%s", reEncoded)
	}
}

func TestRevealSecretRejectsMismatchedFingerprint(t *testing.T) {
	configureHiddenSecretTestEnv(t)
	hidden, err := HideSecret("test-key")
	if err != nil {
		t.Fatalf("HideSecret: %v", err)
	}
	oldHostname := hiddenSecretHostname
	hiddenSecretHostname = func() (string, error) { return "other-host", nil }
	t.Cleanup(func() { hiddenSecretHostname = oldHostname })
	if _, err := RevealSecret(hidden); err == nil || !strings.Contains(err.Error(), "different host/user fingerprint") {
		t.Fatalf("expected fingerprint mismatch error, got %v", err)
	}
}

func configureHiddenSecretTestEnv(t *testing.T) {
	t.Helper()
	baseDir := t.TempDir()
	oldHostname := hiddenSecretHostname
	oldUserCurrent := hiddenSecretUserCurrent
	oldUserConfigDir := hiddenSecretUserConfigDir
	oldReadFile := hiddenSecretReadFile
	oldRandReader := hiddenSecretRandReader
	hiddenSecretHostname = func() (string, error) { return "workstation-01", nil }
	hiddenSecretUserCurrent = func() (*user.User, error) {
		return &user.User{Username: "pastel", Uid: "1000", Gid: "1000", HomeDir: "/home/pastel"}, nil
	}
	hiddenSecretUserConfigDir = func() (string, error) { return filepath.Join(baseDir, "config"), nil }
	hiddenSecretReadFile = func(path string) ([]byte, error) {
		switch path {
		case "/etc/machine-id", "/var/lib/dbus/machine-id":
			return []byte("machine-id-123\n"), nil
		default:
			return os.ReadFile(path)
		}
	}
	hiddenSecretRandReader = bytes.NewReader(bytes.Repeat([]byte{0x42}, 1024))
	t.Cleanup(func() {
		hiddenSecretHostname = oldHostname
		hiddenSecretUserCurrent = oldUserCurrent
		hiddenSecretUserConfigDir = oldUserConfigDir
		hiddenSecretReadFile = oldReadFile
		hiddenSecretRandReader = oldRandReader
	})
}
