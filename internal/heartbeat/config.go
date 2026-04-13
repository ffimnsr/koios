// Package heartbeat manages per-peer periodic agent health-check turns,
// replicating the OpenClaw heartbeat mechanism.
//
// Each configured peer gets a timer goroutine. Every HeartbeatEvery the runner
// replays that peer's main-session history, injects HEARTBEAT.md when present,
// sends the heartbeat prompt to the LLM, and, if the response is not a
// HEARTBEAT_OK acknowledgement, appends it to the peer's session history as an
// assistant message.
package heartbeat

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"
)

const (
	// DefaultPrompt is the verbatim user message sent on each heartbeat tick.
	DefaultPrompt = "Read HEARTBEAT.md if it exists (workspace context). Follow it strictly. " +
		"Do not infer or repeat old tasks from prior chats. " +
		"If nothing needs attention, reply HEARTBEAT_OK."

	// DefaultAckMaxChars is the maximum number of characters allowed in a
	// heartbeat reply (after stripping HEARTBEAT_OK) before the message is
	// considered a real alert and stored in the session.
	DefaultAckMaxChars = 300
)

// ActiveHours restricts heartbeat runs to a window within the day.
type ActiveHours struct {
	// Start is the window open time in HH:MM (inclusive). Use "00:00" for
	// start-of-day.
	Start string `json:"start"`
	// End is the window close time in HH:MM (exclusive). Use "24:00" for
	// end-of-day.
	End string `json:"end"`
	// Timezone is an IANA timezone name. Empty or "user" falls back to the
	// host system timezone.
	Timezone string `json:"timezone,omitempty"`
}

// Config holds per-peer heartbeat settings.
type Config struct {
	// Enabled, when false, skips all heartbeat runs for this peer.
	Enabled bool `json:"enabled"`
	// Every is the interval between heartbeat runs.
	Every time.Duration `json:"every"`
	// Prompt overrides the default heartbeat prompt for this peer.
	Prompt string `json:"prompt,omitempty"`
	// AckMaxChars is the threshold below which a HEARTBEAT_OK reply is
	// silently discarded.
	AckMaxChars int `json:"ack_max_chars"`
	// ActiveHours, when non-nil, restricts heartbeat runs to a time window.
	ActiveHours *ActiveHours `json:"active_hours,omitempty"`
}

// EffectivePrompt returns the prompt to use, falling back to DefaultPrompt.
func (c *Config) EffectivePrompt() string {
	if c.Prompt != "" {
		return c.Prompt
	}
	return DefaultPrompt
}

// EffectiveAckMaxChars returns the ackMaxChars to use, falling back to the default.
func (c *Config) EffectiveAckMaxChars() int {
	if c.AckMaxChars > 0 {
		return c.AckMaxChars
	}
	return DefaultAckMaxChars
}

// IsInActiveHours reports whether now falls within the configured active window.
// When ActiveHours is nil, the window is always active.
func (c *Config) IsInActiveHours(now time.Time) bool {
	if c.ActiveHours == nil {
		return true
	}
	ah := c.ActiveHours

	// Resolve timezone.
	loc := time.Local
	if ah.Timezone != "" && ah.Timezone != "user" && ah.Timezone != "local" {
		if l, err := time.LoadLocation(ah.Timezone); err == nil {
			loc = l
		}
	}
	t := now.In(loc)

	startH, startM, ok1 := parseHHMM(ah.Start)
	endH, endM, ok2 := parseHHMM(ah.End)
	if !ok1 || !ok2 {
		return true // invalid config → always active
	}

	// A zero-width window (start == end) is always inactive.
	if startH == endH && startM == endM {
		return false
	}

	startMins := startH*60 + startM
	endMins := endH*60 + endM
	nowMins := t.Hour()*60 + t.Minute()

	// "24:00" is represented as 1440.
	if endMins == 24*60 {
		return nowMins >= startMins
	}
	return nowMins >= startMins && nowMins < endMins
}

// parseHHMM parses a "HH:MM" or "24:00" string into integer hours and minutes.
func parseHHMM(s string) (h, m int, ok bool) {
	if len(s) != 5 || s[2] != ':' {
		return 0, 0, false
	}
	h = int(s[0]-'0')*10 + int(s[1]-'0')
	m = int(s[3]-'0')*10 + int(s[4]-'0')
	if h < 0 || h > 24 || m < 0 || m > 59 {
		return 0, 0, false
	}
	if h == 24 && m != 0 {
		return 0, 0, false
	}
	return h, m, true
}

// ConfigStore persists per-peer heartbeat configs as individual JSON files
// under <cronDir>/heartbeat/<peerID>.json.
type ConfigStore struct {
	dir string
}

type diskConfig struct {
	PeerID string `json:"peer_id,omitempty"`
	Config
}

// NewConfigStore creates (or opens) the heartbeat config store.
func NewConfigStore(cronDir string) (*ConfigStore, error) {
	dir := filepath.Join(cronDir, "heartbeat")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("create heartbeat config dir: %w", err)
	}
	return &ConfigStore{dir: dir}, nil
}

// Load reads the heartbeat config for peerID from disk.
// Returns nil if no config has been saved yet (caller should use defaults).
func (s *ConfigStore) Load(peerID string) (*Config, error) {
	data, err := os.ReadFile(s.path(peerID))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read heartbeat config for %s: %w", peerID, err)
	}
	var wrapped diskConfig
	if err := json.Unmarshal(data, &wrapped); err == nil {
		return &wrapped.Config, nil
	}

	var legacy Config
	if err := json.Unmarshal(data, &legacy); err != nil {
		return nil, fmt.Errorf("parse heartbeat config for %s: %w", peerID, err)
	}
	return &legacy, nil
}

// Save writes the heartbeat config for peerID to disk atomically.
func (s *ConfigStore) Save(peerID string, cfg *Config) error {
	data, err := json.MarshalIndent(diskConfig{PeerID: peerID, Config: *cfg}, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal heartbeat config: %w", err)
	}
	tmp := s.path(peerID) + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return fmt.Errorf("write heartbeat config: %w", err)
	}
	if err := os.Rename(tmp, s.path(peerID)); err != nil {
		return fmt.Errorf("rename heartbeat config: %w", err)
	}
	return nil
}

// GetOrDefault returns the saved config for peerID, or a default config built
// from the provided defaults when no saved config exists.
func (s *ConfigStore) GetOrDefault(peerID string, defaultEvery time.Duration) (*Config, error) {
	cfg, err := s.Load(peerID)
	if err != nil {
		return nil, err
	}
	if cfg != nil {
		return cfg, nil
	}
	return &Config{
		Enabled:     true,
		Every:       defaultEvery,
		AckMaxChars: DefaultAckMaxChars,
	}, nil
}

// ListPeerIDs returns peer IDs for configs that were saved with embedded
// metadata. Older config files without peer_id are skipped because their
// original peer IDs cannot be reconstructed from the safe filename.
func (s *ConfigStore) ListPeerIDs() ([]string, error) {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return nil, fmt.Errorf("read heartbeat config dir: %w", err)
	}
	ids := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		data, err := os.ReadFile(filepath.Join(s.dir, entry.Name()))
		if err != nil {
			return nil, fmt.Errorf("read heartbeat config entry %s: %w", entry.Name(), err)
		}
		var wrapped diskConfig
		if err := json.Unmarshal(data, &wrapped); err != nil || wrapped.PeerID == "" {
			continue
		}
		ids = append(ids, wrapped.PeerID)
	}
	sort.Strings(ids)
	return ids, nil
}

func (s *ConfigStore) path(peerID string) string {
	return filepath.Join(s.dir, safeFilename(peerID)+".json")
}

// safeFilename converts a peer ID to a safe filename.
func safeFilename(peerID string) string {
	out := make([]byte, 0, len(peerID))
	for i := 0; i < len(peerID); i++ {
		c := peerID[i]
		switch {
		case c >= 'a' && c <= 'z',
			c >= 'A' && c <= 'Z',
			c >= '0' && c <= '9',
			c == '-', c == '_', c == '.':
			out = append(out, c)
		default:
			out = append(out, '_')
		}
	}
	return string(out)
}
