package standing

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/ffimnsr/koios/internal/types"
)

const WorkspaceFilename = "STANDING_ORDERS.md"

// standingCacheTTL is how long a loaded document (or an absent-file result)
// is served from the in-memory cache before the next os.ReadFile is issued.
const standingCacheTTL = 5 * time.Second

type docCacheEntry struct {
	doc       *Document
	expiresAt time.Time
}

type workspaceCacheEntry struct {
	content   string
	expiresAt time.Time
}

type Profile struct {
	Content       string   `json:"content,omitempty"`
	ToolProfile   string   `json:"tool_profile,omitempty"`
	ToolsAllow    []string `json:"tools_allow,omitempty"`
	ToolsDeny     []string `json:"tools_deny,omitempty"`
	ResponseStyle string   `json:"response_style,omitempty"`
	ThinkLevel    string   `json:"think_level,omitempty"`
	VerboseMode   *bool    `json:"verbose_mode,omitempty"`
	TraceMode     *bool    `json:"trace_mode,omitempty"`
}

type ResolvedProfile struct {
	Name    string  `json:"name"`
	Profile Profile `json:"profile"`
}

type Document struct {
	PeerID         string             `json:"peer_id,omitempty"`
	Content        string             `json:"content"`
	DefaultProfile string             `json:"default_profile,omitempty"`
	Profiles       map[string]Profile `json:"profiles,omitempty"`
	UpdatedAt      time.Time          `json:"updated_at,omitempty"`
}

type Store struct {
	dir     string
	cacheMu sync.Mutex
	cache   map[string]docCacheEntry
}

func NewStore(baseDir string) (*Store, error) {
	dir := filepath.Join(baseDir, "standing")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("create standing dir: %w", err)
	}
	return &Store{dir: dir, cache: make(map[string]docCacheEntry)}, nil
}

func (s *Store) Load(peerID string) (*Document, error) {
	if s == nil {
		return nil, nil
	}
	s.cacheMu.Lock()
	if e, ok := s.cache[peerID]; ok && time.Now().Before(e.expiresAt) {
		doc := e.doc
		s.cacheMu.Unlock()
		return doc, nil
	}
	s.cacheMu.Unlock()
	data, err := os.ReadFile(s.path(peerID))
	if os.IsNotExist(err) {
		s.cacheMu.Lock()
		s.cache[peerID] = docCacheEntry{doc: nil, expiresAt: time.Now().Add(standingCacheTTL)}
		s.cacheMu.Unlock()
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read standing orders for %s: %w", peerID, err)
	}
	var doc Document
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("parse standing orders for %s: %w", peerID, err)
	}
	if doc.PeerID == "" {
		doc.PeerID = peerID
	}
	s.cacheMu.Lock()
	s.cache[peerID] = docCacheEntry{doc: &doc, expiresAt: time.Now().Add(standingCacheTTL)}
	s.cacheMu.Unlock()
	return &doc, nil
}

func (s *Store) Save(peerID, content string) (*Document, error) {
	if s == nil {
		return nil, fmt.Errorf("standing store is not enabled")
	}
	doc := &Document{PeerID: peerID, Content: content}
	return s.SaveDocument(doc)
}

func (s *Store) SaveDocument(doc *Document) (*Document, error) {
	if s == nil {
		return nil, fmt.Errorf("standing store is not enabled")
	}
	if doc == nil {
		return nil, fmt.Errorf("standing document is required")
	}
	doc = normalizeDocument(doc.PeerID, doc)
	data, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("marshal standing orders: %w", err)
	}
	tmp := s.path(doc.PeerID) + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return nil, fmt.Errorf("write standing orders: %w", err)
	}
	if err := os.Rename(tmp, s.path(doc.PeerID)); err != nil {
		return nil, fmt.Errorf("rename standing orders: %w", err)
	}
	s.cacheMu.Lock()
	delete(s.cache, doc.PeerID)
	s.cacheMu.Unlock()
	return doc, nil
}

func (s *Store) Delete(peerID string) error {
	if s == nil {
		return fmt.Errorf("standing store is not enabled")
	}
	if err := os.Remove(s.path(peerID)); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("delete standing orders: %w", err)
	}
	s.cacheMu.Lock()
	delete(s.cache, peerID)
	s.cacheMu.Unlock()
	return nil
}

func (s *Store) path(peerID string) string {
	return filepath.Join(s.dir, safeFilename(peerID)+".json")
}

type Manager struct {
	store        *Store
	workspaceDir string
	wcMu         sync.Mutex
	wcEntry      *workspaceCacheEntry
}

func NewManager(store *Store, workspaceDir string) *Manager {
	return &Manager{store: store, workspaceDir: workspaceDir}
}

func (m *Manager) Writable() bool {
	return m != nil && m.store != nil
}

func (m *Manager) Store() *Store {
	if m == nil {
		return nil
	}
	return m.store
}

func (m *Manager) WorkspaceContent() string {
	if m == nil || strings.TrimSpace(m.workspaceDir) == "" {
		return ""
	}
	m.wcMu.Lock()
	if m.wcEntry != nil && time.Now().Before(m.wcEntry.expiresAt) {
		content := m.wcEntry.content
		m.wcMu.Unlock()
		return content
	}
	m.wcMu.Unlock()
	data, err := os.ReadFile(filepath.Join(m.workspaceDir, WorkspaceFilename))
	content := ""
	if err == nil {
		content = strings.TrimSpace(string(data))
	}
	m.wcMu.Lock()
	m.wcEntry = &workspaceCacheEntry{content: content, expiresAt: time.Now().Add(standingCacheTTL)}
	m.wcMu.Unlock()
	return content
}

func (m *Manager) PeerContent(peerID string) (string, error) {
	if m == nil || m.store == nil {
		return "", nil
	}
	doc, err := m.store.Load(peerID)
	if err != nil || doc == nil {
		return "", err
	}
	return strings.TrimSpace(doc.Content), nil
}

func (m *Manager) EffectiveContent(peerID string) (string, error) {
	return m.EffectiveContentForProfile(peerID, "")
}

func (m *Manager) EffectiveContentForProfile(peerID, activeProfile string) (string, error) {
	if m == nil {
		return "", nil
	}
	parts := make([]string, 0, 4)
	if workspace := m.WorkspaceContent(); workspace != "" {
		parts = append(parts, workspace)
	}
	peer, err := m.PeerContent(peerID)
	if err != nil {
		return "", err
	}
	if peer != "" {
		parts = append(parts, peer)
	}
	resolved, err := m.ResolveProfile(peerID, activeProfile)
	if err != nil {
		return "", err
	}
	if resolved != nil {
		if content := strings.TrimSpace(resolved.Profile.Content); content != "" {
			parts = append(parts, content)
		}
		if style := strings.TrimSpace(resolved.Profile.ResponseStyle); style != "" {
			parts = append(parts, "Active profile response style: "+style)
		}
	}
	return strings.TrimSpace(strings.Join(parts, "\n\n")), nil
}

func (m *Manager) SystemMessage(peerID string) (*types.Message, error) {
	return m.SystemMessageForProfile(peerID, "")
}

func (m *Manager) SystemMessageForProfile(peerID, activeProfile string) (*types.Message, error) {
	content, err := m.EffectiveContentForProfile(peerID, activeProfile)
	if err != nil || content == "" {
		return nil, err
	}
	msg := &types.Message{
		Role:    "system",
		Content: "Standing orders:\n\n" + content,
	}
	return msg, nil
}

func (m *Manager) Document(peerID string) (*Document, error) {
	if m == nil || m.store == nil {
		return nil, nil
	}
	return m.store.Load(peerID)
}

func (m *Manager) ProfileNames(peerID string) ([]string, error) {
	doc, err := m.Document(peerID)
	if err != nil || doc == nil || len(doc.Profiles) == 0 {
		return nil, err
	}
	names := make([]string, 0, len(doc.Profiles))
	for name := range doc.Profiles {
		names = append(names, name)
	}
	sort.Strings(names)
	return names, nil
}

func (m *Manager) ResolveProfile(peerID, activeProfile string) (*ResolvedProfile, error) {
	if m == nil || m.store == nil {
		return nil, nil
	}
	doc, err := m.store.Load(peerID)
	if err != nil || doc == nil {
		return nil, err
	}
	name := strings.TrimSpace(activeProfile)
	if name == "" {
		name = strings.TrimSpace(doc.DefaultProfile)
	}
	if name == "" {
		return nil, nil
	}
	profile, ok := doc.Profiles[name]
	if !ok {
		return nil, fmt.Errorf("standing profile %q not found", name)
	}
	return &ResolvedProfile{Name: name, Profile: normalizeProfile(profile)}, nil
}

func normalizeDocument(peerID string, doc *Document) *Document {
	normalized := &Document{
		PeerID:         strings.TrimSpace(peerID),
		Content:        strings.TrimSpace(doc.Content),
		DefaultProfile: strings.TrimSpace(doc.DefaultProfile),
		UpdatedAt:      time.Now().UTC(),
	}
	if normalized.PeerID == "" {
		normalized.PeerID = strings.TrimSpace(doc.PeerID)
	}
	if len(doc.Profiles) > 0 {
		normalized.Profiles = make(map[string]Profile, len(doc.Profiles))
		for rawName, profile := range doc.Profiles {
			name := strings.TrimSpace(rawName)
			if name == "" {
				continue
			}
			normalizedProfile := normalizeProfile(profile)
			if normalizedProfile.isZero() {
				continue
			}
			normalized.Profiles[name] = normalizedProfile
		}
		if len(normalized.Profiles) == 0 {
			normalized.Profiles = nil
		}
	}
	if normalized.DefaultProfile != "" {
		if normalized.Profiles == nil {
			normalized.DefaultProfile = ""
		} else if _, ok := normalized.Profiles[normalized.DefaultProfile]; !ok {
			normalized.DefaultProfile = ""
		}
	}
	return normalized
}

func normalizeProfile(profile Profile) Profile {
	profile.Content = strings.TrimSpace(profile.Content)
	profile.ToolProfile = strings.TrimSpace(profile.ToolProfile)
	profile.ResponseStyle = strings.TrimSpace(profile.ResponseStyle)
	profile.ThinkLevel = strings.TrimSpace(profile.ThinkLevel)
	profile.ToolsAllow = trimStringSlice(profile.ToolsAllow)
	profile.ToolsDeny = trimStringSlice(profile.ToolsDeny)
	return profile
}

func (p Profile) isZero() bool {
	return p.Content == "" &&
		p.ToolProfile == "" &&
		len(p.ToolsAllow) == 0 &&
		len(p.ToolsDeny) == 0 &&
		p.ResponseStyle == "" &&
		p.ThinkLevel == "" &&
		p.VerboseMode == nil &&
		p.TraceMode == nil
}

func trimStringSlice(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	trimmed := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, raw := range values {
		value := strings.TrimSpace(raw)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		trimmed = append(trimmed, value)
	}
	if len(trimmed) == 0 {
		return nil
	}
	return trimmed
}

func safeFilename(peerID string) string {
	// Percent-encode the peer ID so every distinct peer ID maps to a distinct
	// filename. Using url.PathEscape keeps alphanumeric and '-', '_', '.', '~'
	// as-is and encodes everything else (including '@', ':', '/', etc.).
	return url.PathEscape(peerID)
}
