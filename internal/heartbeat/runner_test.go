package heartbeat

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ffimnsr/koios/internal/session"
	"github.com/ffimnsr/koios/internal/types"
)

// ── isHeartbeatOK ────────────────────────────────────────────────────────────

func TestIsHeartbeatOK(t *testing.T) {
	cases := []struct {
		text        string
		ackMaxChars int
		want        bool
	}{
		{"HEARTBEAT_OK", 300, true},
		{" HEARTBEAT_OK ", 300, true},
		{"HEARTBEAT_OK All is well.", 300, true},
		{"HEARTBEAT_OK " + rpt("x", 299), 300, true},
		{"HEARTBEAT_OK " + rpt("x", 301), 300, false},
		{"All is well. HEARTBEAT_OK", 300, true},
		{rpt("x", 299) + " HEARTBEAT_OK", 300, true},
		{rpt("x", 301) + " HEARTBEAT_OK", 300, false},
		{"Something needs attention.", 300, false},
		{"", 300, false},
		{"Before HEARTBEAT_OK After", 300, false},
		{"HEARTBEAT_OK", 0, true},
		{"HEARTBEAT_OK x", 0, false},
	}
	for _, tc := range cases {
		if got := isHeartbeatOK(tc.text, tc.ackMaxChars); got != tc.want {
			t.Errorf("isHeartbeatOK(%q, %d) = %v, want %v", tc.text, tc.ackMaxChars, got, tc.want)
		}
	}
}

func rpt(s string, n int) string {
	b := make([]byte, n)
	for i := range b {
		b[i] = s[0]
	}
	return string(b)
}

// ── Config.IsInActiveHours ───────────────────────────────────────────────────

func TestIsInActiveHours_NilActiveHours(t *testing.T) {
	cfg := &Config{ActiveHours: nil}
	if !cfg.IsInActiveHours(time.Now()) {
		t.Error("expected active when ActiveHours is nil")
	}
}

func TestIsInActiveHours_ZeroWidthWindow(t *testing.T) {
	cfg := &Config{ActiveHours: &ActiveHours{Start: "09:00", End: "09:00"}}
	for _, h := range []int{0, 9, 12, 23} {
		if cfg.IsInActiveHours(utcAt(h, 0)) {
			t.Errorf("expected inactive at %02d:00 with zero-width window", h)
		}
	}
}

func TestIsInActiveHours_FullDay(t *testing.T) {
	cfg := &Config{ActiveHours: &ActiveHours{Start: "00:00", End: "24:00", Timezone: "UTC"}}
	for _, h := range []int{0, 6, 12, 23} {
		if !cfg.IsInActiveHours(utcAt(h, 0)) {
			t.Errorf("expected active at %02d:00 with full-day window", h)
		}
	}
}

func TestIsInActiveHours_Boundary(t *testing.T) {
	cfg := &Config{ActiveHours: &ActiveHours{Start: "09:00", End: "17:00", Timezone: "UTC"}}
	cases := []struct {
		h, m int
		want bool
	}{
		{8, 59, false},
		{9, 0, true},
		{12, 30, true},
		{16, 59, true},
		{17, 0, false},
		{17, 1, false},
	}
	for _, tc := range cases {
		if got := cfg.IsInActiveHours(utcAt(tc.h, tc.m)); got != tc.want {
			t.Errorf("at %02d:%02d: got %v, want %v", tc.h, tc.m, got, tc.want)
		}
	}
}

func TestIsInActiveHours_End24(t *testing.T) {
	cfg := &Config{ActiveHours: &ActiveHours{Start: "18:00", End: "24:00", Timezone: "UTC"}}
	if cfg.IsInActiveHours(utcAt(17, 59)) {
		t.Error("expected inactive at 17:59")
	}
	if !cfg.IsInActiveHours(utcAt(18, 0)) {
		t.Error("expected active at 18:00")
	}
	if !cfg.IsInActiveHours(utcAt(23, 59)) {
		t.Error("expected active at 23:59")
	}
}

func TestIsInActiveHours_InvalidConfig(t *testing.T) {
	cfg := &Config{ActiveHours: &ActiveHours{Start: "bad", End: "worse"}}
	if !cfg.IsInActiveHours(time.Now()) {
		t.Error("expected active when config is invalid (safe default)")
	}
}

// ── Config helpers ────────────────────────────────────────────────────────────

func TestEffectivePrompt(t *testing.T) {
	if cfg := (&Config{}); cfg.EffectivePrompt() != DefaultPrompt {
		t.Errorf("expected DefaultPrompt, got %q", cfg.EffectivePrompt())
	}
	if cfg := (&Config{Prompt: "custom"}); cfg.EffectivePrompt() != "custom" {
		t.Errorf("expected custom, got %q", cfg.EffectivePrompt())
	}
}

func TestEffectiveAckMaxChars(t *testing.T) {
	if cfg := (&Config{}); cfg.EffectiveAckMaxChars() != DefaultAckMaxChars {
		t.Errorf("expected %d, got %d", DefaultAckMaxChars, cfg.EffectiveAckMaxChars())
	}
	if cfg := (&Config{AckMaxChars: 50}); cfg.EffectiveAckMaxChars() != 50 {
		t.Errorf("expected 50, got %d", cfg.EffectiveAckMaxChars())
	}
}

// ── ConfigStore ───────────────────────────────────────────────────────────────

func TestConfigStore_SaveLoad(t *testing.T) {
	store, _ := NewConfigStore(t.TempDir())
	cfg := &Config{Enabled: true, Every: 15 * time.Minute, Prompt: "Do the thing.", AckMaxChars: 100}
	if err := store.Save("peer-1", cfg); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := store.Load("peer-1")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got == nil {
		t.Fatal("Load returned nil")
	}
	if got.Every != cfg.Every || got.Prompt != cfg.Prompt {
		t.Errorf("mismatch: %+v", got)
	}
}

func TestConfigStore_Load_Missing(t *testing.T) {
	store, _ := NewConfigStore(t.TempDir())
	cfg, err := store.Load("no-such-peer")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg != nil {
		t.Errorf("expected nil, got %+v", cfg)
	}
}

func TestConfigStore_GetOrDefault(t *testing.T) {
	store, _ := NewConfigStore(t.TempDir())
	defaultEvery := 30 * time.Minute

	// No config saved → returns defaults.
	cfg, err := store.GetOrDefault("new-peer", defaultEvery)
	if err != nil {
		t.Fatalf("GetOrDefault: %v", err)
	}
	if cfg.Every != defaultEvery || !cfg.Enabled {
		t.Errorf("unexpected default: %+v", cfg)
	}

	// After save, returns saved config.
	_ = store.Save("new-peer", &Config{Enabled: false, Every: 5 * time.Minute})
	cfg2, _ := store.GetOrDefault("new-peer", defaultEvery)
	if cfg2.Every != 5*time.Minute || cfg2.Enabled {
		t.Errorf("unexpected saved config: %+v", cfg2)
	}
}

func TestConfigStore_ListPeerIDs(t *testing.T) {
	store, _ := NewConfigStore(t.TempDir())
	if err := store.Save("peer:one", &Config{Enabled: true, Every: 15 * time.Minute}); err != nil {
		t.Fatalf("Save peer:one: %v", err)
	}
	if err := store.Save("peer.two", &Config{Enabled: true, Every: 20 * time.Minute}); err != nil {
		t.Fatalf("Save peer.two: %v", err)
	}

	ids, err := store.ListPeerIDs()
	if err != nil {
		t.Fatalf("ListPeerIDs: %v", err)
	}
	if len(ids) != 2 || ids[0] != "peer.two" || ids[1] != "peer:one" {
		t.Fatalf("unexpected peer ids: %#v", ids)
	}
}

func TestBuildHeartbeatMessages_UsesSessionHistoryAndHeartbeatFile(t *testing.T) {
	dir := t.TempDir()
	peerDir := filepath.Join(dir, "peers", "default")
	if err := os.MkdirAll(peerDir, 0o755); err != nil {
		t.Fatalf("mkdir default peer dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(peerDir, "HEARTBEAT.md"), []byte("Check inbox\nReview calendar\n"), 0o644); err != nil {
		t.Fatalf("write HEARTBEAT.md: %v", err)
	}

	store := session.New(10)
	store.Append("peer-1",
		types.Message{Role: "user", Content: "Earlier task"},
		types.Message{Role: "assistant", Content: "Earlier answer"},
	)

	r := New(noopProvider{}, store, nil, 30*time.Minute, time.Minute, dir, nil)
	msgs := r.buildHeartbeatMessages("peer-1", "Check if anything needs attention.")

	if len(msgs) != 4 {
		t.Fatalf("expected 4 messages, got %d", len(msgs))
	}
	if msgs[0].Role != "system" || msgs[0].Content != "HEARTBEAT.md\n\nCheck inbox\nReview calendar" {
		t.Fatalf("unexpected heartbeat instructions: %#v", msgs[0])
	}
	if msgs[1].Content != "Earlier task" || msgs[2].Content != "Earlier answer" {
		t.Fatalf("expected history in heartbeat request, got %#v", msgs)
	}
	if msgs[3].Role != "user" || msgs[3].Content != "Check if anything needs attention." {
		t.Fatalf("unexpected prompt message: %#v", msgs[3])
	}
}

type noopProvider struct{}

func (noopProvider) Complete(context.Context, *types.ChatRequest) (*types.ChatResponse, error) {
	return &types.ChatResponse{}, nil
}

func utcAt(hour, minute int) time.Time {
	return time.Date(2025, 6, 1, hour, minute, 0, 0, time.UTC)
}
