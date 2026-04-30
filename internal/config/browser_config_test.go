package config

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestEncodeTOMLIncludesBrowserProfiles(t *testing.T) {
	cfg := Default()
	cfg.WorkspaceRoot = t.TempDir()
	cfg.Browser = BrowserConfig{
		DefaultProfile: "work",
		Profiles: []BrowserProfileConfig{{
			Name:          "work",
			Enabled:       true,
			Mode:          "managed",
			HostAllowlist: []string{"example.com"},
			HostDenylist:  []string{"admin.example.com"},
			Headless:      true,
			UserDataDir:   filepath.Join(cfg.WorkspaceRoot, "browser", "work"),
		}},
	}
	encoded := EncodeTOML(cfg, false)
	for _, want := range []string{
		"[browser]",
		"default_profile = \"work\"",
		"[[browser.profiles]]",
		"name = \"work\"",
		"mode = \"managed\"",
		"host_allowlist = [\"example.com\"]",
		"host_denylist = [\"admin.example.com\"]",
		"headless = true",
	} {
		if !strings.Contains(encoded, want) {
			t.Fatalf("expected encoded config to contain %q, got:\n%s", want, encoded)
		}
	}
}

func TestValidateRejectsInvalidBrowserDefaults(t *testing.T) {
	cfg := Default()
	cfg.Browser.DefaultProfile = "missing"
	cfg.Browser.Profiles = []BrowserProfileConfig{{Name: "work", Enabled: true, Mode: "managed"}}
	err := validate(cfg)
	if err == nil || !strings.Contains(err.Error(), "browser.default_profile") {
		t.Fatalf("expected browser default profile validation error, got %v", err)
	}
}

func TestValidateAcceptsExistingSessionBrowserProfile(t *testing.T) {
	cfg := Default()
	cfg.Browser.DefaultProfile = "personal"
	cfg.Browser.Profiles = []BrowserProfileConfig{{
		Name:        "personal",
		Enabled:     true,
		Mode:        "existing_session",
		UserDataDir: filepath.Join(t.TempDir(), "chrome-personal"),
	}}
	if err := validate(cfg); err != nil {
		t.Fatalf("expected existing_session profile to validate, got %v", err)
	}
}

func TestValidateRejectsBlankBrowserHostPolicyEntry(t *testing.T) {
	cfg := Default()
	cfg.Browser.DefaultProfile = "work"
	cfg.Browser.Profiles = []BrowserProfileConfig{{
		Name:          "work",
		Enabled:       true,
		Mode:          "managed",
		HostAllowlist: []string{""},
	}}
	if err := validate(cfg); err == nil || !strings.Contains(err.Error(), "host_allowlist") {
		t.Fatalf("expected browser host allowlist validation error, got %v", err)
	}
}
