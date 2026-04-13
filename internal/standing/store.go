package standing

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
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

type Document struct {
	PeerID    string    `json:"peer_id,omitempty"`
	Content   string    `json:"content"`
	UpdatedAt time.Time `json:"updated_at,omitempty"`
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
	doc := &Document{
		PeerID:    peerID,
		Content:   strings.TrimSpace(content),
		UpdatedAt: time.Now().UTC(),
	}
	data, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("marshal standing orders: %w", err)
	}
	tmp := s.path(peerID) + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return nil, fmt.Errorf("write standing orders: %w", err)
	}
	if err := os.Rename(tmp, s.path(peerID)); err != nil {
		return nil, fmt.Errorf("rename standing orders: %w", err)
	}
	s.cacheMu.Lock()
	delete(s.cache, peerID)
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
	if m == nil {
		return "", nil
	}
	parts := make([]string, 0, 2)
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
	return strings.TrimSpace(strings.Join(parts, "\n\n")), nil
}

func (m *Manager) SystemMessage(peerID string) (*types.Message, error) {
	content, err := m.EffectiveContent(peerID)
	if err != nil || content == "" {
		return nil, err
	}
	msg := &types.Message{
		Role:    "system",
		Content: "Standing orders:\n\n" + content,
	}
	return msg, nil
}

func safeFilename(peerID string) string {
	// Percent-encode the peer ID so every distinct peer ID maps to a distinct
	// filename. Using url.PathEscape keeps alphanumeric and '-', '_', '.', '~'
	// as-is and encodes everything else (including '@', ':', '/', etc.).
	return url.PathEscape(peerID)
}
