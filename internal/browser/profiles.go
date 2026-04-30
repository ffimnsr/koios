package browser

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/ffimnsr/koios/internal/config"
)

const (
	serverKindBrowser      = "browser"
	defaultMCPCommand      = "npx"
	defaultMCPPackage      = "chrome-devtools-mcp@latest"
	defaultManagedMode     = "managed"
	defaultExistingMode    = "existing_session"
	defaultBrowserToolStem = "browser_"
)

func EnabledProfiles(cfg *config.Config) []config.BrowserProfileConfig {
	if cfg == nil {
		return nil
	}
	profiles := make([]config.BrowserProfileConfig, 0, len(cfg.Browser.Profiles))
	for _, profile := range cfg.Browser.Profiles {
		if !profile.Enabled {
			continue
		}
		profiles = append(profiles, profile)
	}
	return profiles
}

func ResolveActiveProfile(cfg *config.Config, sessionProfile string) string {
	sessionProfile = strings.TrimSpace(sessionProfile)
	if sessionProfile != "" {
		for _, profile := range EnabledProfiles(cfg) {
			if strings.EqualFold(strings.TrimSpace(profile.Name), sessionProfile) {
				return strings.TrimSpace(profile.Name)
			}
		}
	}
	defaultProfile := strings.TrimSpace(cfg.Browser.DefaultProfile)
	if defaultProfile != "" {
		for _, profile := range EnabledProfiles(cfg) {
			if strings.EqualFold(strings.TrimSpace(profile.Name), defaultProfile) {
				return strings.TrimSpace(profile.Name)
			}
		}
	}
	profiles := EnabledProfiles(cfg)
	if len(profiles) == 1 {
		return strings.TrimSpace(profiles[0].Name)
	}
	return ""
}

func MCPServers(cfg *config.Config) ([]config.MCPServerConfig, error) {
	profiles := EnabledProfiles(cfg)
	if len(profiles) == 0 {
		return nil, nil
	}
	servers := make([]config.MCPServerConfig, 0, len(profiles))
	seen := make(map[string]struct{}, len(profiles))
	for _, profile := range profiles {
		name := strings.TrimSpace(profile.Name)
		token := sanitizeToken(name)
		if token == "" {
			return nil, fmt.Errorf("browser profile %q does not produce a usable token", name)
		}
		serverName := defaultBrowserToolStem + token
		if _, exists := seen[serverName]; exists {
			return nil, fmt.Errorf("browser profile %q collides with another browser profile token", name)
		}
		seen[serverName] = struct{}{}
		args, err := mcpArgs(cfg, profile)
		if err != nil {
			return nil, err
		}
		command := strings.TrimSpace(profile.MCPCommand)
		if command == "" {
			command = defaultMCPCommand
		}
		servers = append(servers, config.MCPServerConfig{
			Name:           serverName,
			Transport:      "stdio",
			Command:        command,
			Args:           args,
			Enabled:        true,
			HideTools:      true,
			Kind:           serverKindBrowser,
			ProfileName:    name,
			ToolNamePrefix: "mcp__" + serverName + "__",
		})
	}
	return servers, nil
}

func mcpArgs(cfg *config.Config, profile config.BrowserProfileConfig) ([]string, error) {
	mode := normalizeMode(profile.Mode)
	command := strings.TrimSpace(profile.MCPCommand)
	packageName := strings.TrimSpace(profile.MCPPackage)
	if packageName == "" {
		packageName = defaultMCPPackage
	}
	args := make([]string, 0, 16)
	if command == "" || filepath.Base(command) == defaultMCPCommand {
		args = append(args, "-y", packageName)
	}
	if profile.Slim {
		args = append(args, "--slim=true")
	}
	if profile.Headless {
		args = append(args, "--headless=true")
	}
	if profile.Isolated {
		args = append(args, "--isolated=true")
	}
	if channel := strings.TrimSpace(profile.Channel); channel != "" {
		args = append(args, "--channel="+channel)
	}
	if path := strings.TrimSpace(profile.ExecutablePath); path != "" {
		args = append(args, "--executable-path="+path)
	}
	if viewport := strings.TrimSpace(profile.Viewport); viewport != "" {
		args = append(args, "--viewport="+viewport)
	}
	if profile.AcceptInsecureCerts {
		args = append(args, "--accept-insecure-certs=true")
	}
	if profile.ExperimentalVision {
		args = append(args, "--experimental-vision=true")
	}
	if profile.ExperimentalScreencast {
		args = append(args, "--experimental-screencast=true")
	}
	if path := strings.TrimSpace(profile.ExperimentalFfmpegPath); path != "" {
		args = append(args, "--experimental-ffmpeg-path="+path)
	}
	if profile.PerformanceCrux != nil {
		args = append(args, fmt.Sprintf("--performance-crux=%t", *profile.PerformanceCrux))
	}
	if profile.UsageStatistics != nil {
		args = append(args, fmt.Sprintf("--usage-statistics=%t", *profile.UsageStatistics))
	}
	if profile.RedactNetworkHeaders != nil {
		args = append(args, fmt.Sprintf("--redact-network-headers=%t", *profile.RedactNetworkHeaders))
	}
	for _, chromeArg := range profile.ChromeArgs {
		chromeArg = strings.TrimSpace(chromeArg)
		if chromeArg != "" {
			args = append(args, "--chrome-arg="+chromeArg)
		}
	}
	if wsHeaders := profile.WSHeaders; len(wsHeaders) > 0 {
		encoded, err := json.Marshal(wsHeaders)
		if err != nil {
			return nil, fmt.Errorf("marshal browser profile %q ws_headers: %w", profile.Name, err)
		}
		args = append(args, "--ws-headers="+string(encoded))
	}
	userDataDir := strings.TrimSpace(profile.UserDataDir)
	if userDataDir == "" && mode == defaultManagedMode {
		userDataDir = filepath.Join(cfg.BrowserDir(), sanitizeToken(profile.Name))
	}
	if userDataDir != "" {
		args = append(args, "--user-data-dir="+userDataDir)
	}
	switch mode {
	case defaultExistingMode, "auto_connect":
		args = append(args, "--auto-connect=true")
	case "browser_url":
		args = append(args, "--browser-url="+strings.TrimSpace(profile.BrowserURL))
	case "ws_endpoint":
		args = append(args, "--ws-endpoint="+strings.TrimSpace(profile.WSEndpoint))
	case defaultManagedMode:
		// No extra flags required; chrome-devtools-mcp launches a dedicated browser.
	default:
		return nil, fmt.Errorf("unsupported browser profile mode %q", profile.Mode)
	}
	return args, nil
}

func normalizeMode(mode string) string {
	mode = strings.ToLower(strings.TrimSpace(mode))
	switch mode {
	case "", defaultManagedMode:
		return defaultManagedMode
	case "auto_connect", defaultExistingMode:
		return defaultExistingMode
	}
	return mode
}

func sanitizeToken(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" {
		return ""
	}
	var b strings.Builder
	lastUnderscore := false
	for _, r := range value {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			lastUnderscore = false
			continue
		}
		if !lastUnderscore {
			b.WriteByte('_')
			lastUnderscore = true
		}
	}
	return strings.Trim(b.String(), "_")
}
