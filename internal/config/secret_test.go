package config

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"encoding/base64"
	"io"
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
		"[tools.web_search]",
		"enabled = true",
		"providers = [\"brave\", \"exa\", \"tavily\"]",
		"",
		"[tools.web_search.brave]",
		"api_key = \"" + hidden + "\"",
		"base_url = \"https://api.search.brave.com/res/v1/web/search\"",
		"default_timeout = \"15s\"",
		"",
		"[tools.web_search.exa]",
		"api_key = \"" + hidden + "\"",
		"base_url = \"https://api.exa.ai/search\"",
		"default_timeout = \"15s\"",
		"",
		"[tools.web_search.tavily]",
		"api_key = \"" + hidden + "\"",
		"base_url = \"https://api.tavily.com/search\"",
		"default_timeout = \"15s\"",
		"",
		"[tools.browser_run]",
		"enabled = true",
		"account_id = \"account-123\"",
		"api_token = \"" + hidden + "\"",
		"base_url = \"https://api.cloudflare.com/client/v4\"",
		"default_timeout = \"30s\"",
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
	if cfg.WebSearchBrave.APIKey != "api-hidden" {
		t.Fatalf("cfg.WebSearchBrave.APIKey = %q", cfg.WebSearchBrave.APIKey)
	}
	if cfg.WebSearchExa.APIKey != "api-hidden" {
		t.Fatalf("cfg.WebSearchExa.APIKey = %q", cfg.WebSearchExa.APIKey)
	}
	if cfg.WebSearchTavily.APIKey != "api-hidden" {
		t.Fatalf("cfg.WebSearchTavily.APIKey = %q", cfg.WebSearchTavily.APIKey)
	}
	if cfg.BrowserRun.APIToken != "api-hidden" {
		t.Fatalf("cfg.BrowserRun.APIToken = %q", cfg.BrowserRun.APIToken)
	}
	reEncoded := EncodeTOML(cfg, false)
	if !strings.Contains(reEncoded, hidden) {
		t.Fatalf("expected re-encoded config to preserve hidden secret\n%s", reEncoded)
	}
	if strings.Contains(reEncoded, `api_key = "api-hidden"`) {
		t.Fatalf("expected re-encoded config to avoid plaintext secret\n%s", reEncoded)
	}
	if count := strings.Count(reEncoded, hidden); count < 5 {
		t.Fatalf("expected all hidden secrets to be preserved, got count=%d\n%s", count, reEncoded)
	}
}

func TestRevealSecretV2SurvivesHostnameChange(t *testing.T) {
	configureHiddenSecretTestEnv(t)
	hidden, err := HideSecret("test-key")
	if err != nil {
		t.Fatalf("HideSecret: %v", err)
	}
	if !strings.HasPrefix(hidden, hiddenSecretPrefixV2) {
		t.Fatalf("expected v2 hidden secret, got %q", hidden)
	}
	oldHostname := hiddenSecretHostname
	hiddenSecretHostname = func() (string, error) { return "other-host", nil }
	t.Cleanup(func() { hiddenSecretHostname = oldHostname })
	plaintext, err := RevealSecret(hidden)
	if err != nil {
		t.Fatalf("RevealSecret after hostname change: %v", err)
	}
	if plaintext != "test-key" {
		t.Fatalf("plaintext = %q", plaintext)
	}
}

func TestRevealSecretRejectsMismatchedLegacyFingerprint(t *testing.T) {
	configureHiddenSecretTestEnv(t)
	hidden, err := hideSecretV1ForTest("test-key")
	if err != nil {
		t.Fatalf("hideSecretV1ForTest: %v", err)
	}
	oldHostname := hiddenSecretHostname
	hiddenSecretHostname = func() (string, error) { return "other-host", nil }
	t.Cleanup(func() { hiddenSecretHostname = oldHostname })
	if _, err := RevealSecret(hidden); err == nil || !strings.Contains(err.Error(), "different host/user fingerprint") {
		t.Fatalf("expected fingerprint mismatch error, got %v", err)
	}
}

func hideSecretV1ForTest(plaintext string) (string, error) {
	fingerprint, err := currentHiddenSecretFingerprint()
	if err != nil {
		return "", err
	}
	masterKey, err := loadHiddenSecretMasterKey(fingerprint.keyID, true)
	if err != nil {
		return "", err
	}
	salt := make([]byte, hiddenSecretSaltBytes)
	if _, err := io.ReadFull(hiddenSecretRandReader, salt); err != nil {
		return "", err
	}
	nonce := make([]byte, hiddenSecretNonceBytes)
	if _, err := io.ReadFull(hiddenSecretRandReader, nonce); err != nil {
		return "", err
	}
	derivedKey, err := deriveHiddenSecretKeyV1(masterKey, fingerprint.digest, salt)
	if err != nil {
		return "", err
	}
	block, err := aes.NewCipher(derivedKey)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	ciphertext := gcm.Seal(nil, nonce, []byte(plaintext), hiddenSecretAAD(hiddenSecretPrefixV1, fingerprint.keyID))
	return hiddenSecretPrefixV1 + strings.Join([]string{
		fingerprint.keyID,
		base64.RawURLEncoding.EncodeToString(salt),
		base64.RawURLEncoding.EncodeToString(nonce),
		base64.RawURLEncoding.EncodeToString(ciphertext),
	}, "."), nil
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
