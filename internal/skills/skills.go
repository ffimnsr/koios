package skills

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/ffimnsr/koios/internal/config"
	"github.com/ffimnsr/koios/internal/types"
)

type SourceKind string

const (
	SourceBundled   SourceKind = "bundled"
	SourceManaged   SourceKind = "managed"
	SourceExtra     SourceKind = "extra"
	SourcePersonal  SourceKind = "personal"
	SourceProject   SourceKind = "project"
	SourceWorkspace SourceKind = "workspace"
)

type SourceSpec struct {
	Kind SourceKind
	Path string
	Rank int
}

type Requirements struct {
	OS              []string `yaml:"os"`
	Env             []string `yaml:"env"`
	Binaries        []string `yaml:"binaries"`
	Config          []string `yaml:"config"`
	MinKoiosVersion string   `yaml:"min_koios_version"`
}

type Command struct {
	Name          string `yaml:"name"`
	Description   string `yaml:"description"`
	AssistantText string `yaml:"assistant_text"`
}

type Frontmatter struct {
	ID           string       `yaml:"id"`
	Name         string       `yaml:"name"`
	Description  string       `yaml:"description"`
	Enabled      *bool        `yaml:"enabled"`
	Agents       []string     `yaml:"agents"`
	Requirements Requirements `yaml:"requires"`
	Version      string       `yaml:"version"`
	Origin       string       `yaml:"origin"`
	Trust        string       `yaml:"trust"`
	Managed      *bool        `yaml:"managed"`
	Setup        []string     `yaml:"setup"`
	Commands     []Command    `yaml:"commands"`
}

type Skill struct {
	ID          string     `json:"id"`
	Name        string     `json:"name"`
	Description string     `json:"description,omitempty"`
	Content     string     `json:"content,omitempty"`
	Source      SourceKind `json:"source"`
	Path        string     `json:"path"`
	Rank        int        `json:"rank"`
	Agents      []string   `json:"agents,omitempty"`
	Version     string     `json:"version,omitempty"`
	Origin      string     `json:"origin,omitempty"`
	Trust       string     `json:"trust,omitempty"`
	Managed     bool       `json:"managed,omitempty"`
	Setup       []string   `json:"setup,omitempty"`
	Commands    []Command  `json:"commands,omitempty"`
}

type CatalogEntry struct {
	Skill          Skill    `json:"skill"`
	Status         string   `json:"status"`
	Selected       bool     `json:"selected"`
	BlockedReasons []string `json:"blocked_reasons,omitempty"`
	ShadowedBy     string   `json:"shadowed_by,omitempty"`
	Overrides      []string `json:"overrides,omitempty"`
}

type CatalogSnapshot struct {
	Generation     int64          `json:"generation"`
	RefreshedAt    time.Time      `json:"refreshed_at"`
	Fingerprint    string         `json:"fingerprint"`
	AutoRefreshed  bool           `json:"auto_refreshed"`
	CurrentVersion string         `json:"current_version,omitempty"`
	Entries        []CatalogEntry `json:"entries"`
}

type CommandMatch struct {
	Skill   Skill   `json:"skill"`
	Command Command `json:"command"`
}

type ScanFinding struct {
	Severity string `json:"severity"`
	Reason   string `json:"reason"`
	Path     string `json:"path,omitempty"`
}

type ScanResult struct {
	Safe     bool          `json:"safe"`
	Findings []ScanFinding `json:"findings,omitempty"`
}

type InstallResult struct {
	Installed bool       `json:"installed"`
	TargetDir string     `json:"target_dir,omitempty"`
	Skill     *Skill     `json:"skill,omitempty"`
	Scan      ScanResult `json:"scan"`
}

type loadedSkill struct {
	Skill   Skill
	Status  string
	Blocked []string
}

type cacheState struct {
	fingerprint string
	generation  int64
	refreshedAt time.Time
	snapshot    CatalogSnapshot
}

type Manager struct {
	cfg            *config.Config
	projectRoot    string
	currentVersion string

	mu    sync.Mutex
	cache cacheState
}

func NewManager(cfg *config.Config, projectRoot string) *Manager {
	return &Manager{
		cfg:            cfg,
		projectRoot:    strings.TrimSpace(projectRoot),
		currentVersion: loadKoiosVersion(strings.TrimSpace(projectRoot)),
	}
}

func (m *Manager) Sources() []SourceSpec {
	if m == nil || m.cfg == nil {
		return nil
	}
	raw := m.cfg.SkillSearchPaths(m.projectRoot)
	out := make([]SourceSpec, 0, len(raw))
	for _, path := range raw {
		out = append(out, SourceSpec{Kind: SourceKind(path.Kind), Path: path.Path, Rank: path.Rank})
	}
	return out
}

func (m *Manager) Refresh() (CatalogSnapshot, error) {
	return m.snapshot(true)
}

func (m *Manager) Catalog() ([]Skill, error) {
	snapshot, err := m.snapshot(false)
	if err != nil {
		return nil, err
	}
	catalog := make([]Skill, 0, len(snapshot.Entries))
	for _, entry := range snapshot.Entries {
		if entry.Selected && entry.Status == "active" {
			catalog = append(catalog, entry.Skill)
		}
	}
	return catalog, nil
}

func (m *Manager) CatalogDetails() (CatalogSnapshot, error) {
	return m.snapshot(false)
}

func (m *Manager) SystemMessages(activeProfile string, allowlist []string) ([]types.Message, error) {
	catalog, err := m.Catalog()
	if err != nil {
		return nil, err
	}
	allowed := make(map[string]struct{}, len(allowlist))
	for _, raw := range allowlist {
		value := strings.ToLower(strings.TrimSpace(raw))
		if value == "" {
			continue
		}
		allowed[value] = struct{}{}
	}
	activeProfile = strings.TrimSpace(activeProfile)
	var msgs []types.Message
	for _, skill := range catalog {
		if !skillAppliesToProfile(skill, activeProfile) {
			continue
		}
		if len(allowed) > 0 {
			if _, ok := allowed[strings.ToLower(skill.ID)]; !ok {
				if _, ok := allowed[strings.ToLower(skill.Name)]; !ok {
					continue
				}
			}
		}
		content := strings.TrimSpace(skill.Content)
		if content == "" {
			continue
		}
		if skill.Name != "" {
			content = fmt.Sprintf("Skill: %s\n\n%s", skill.Name, content)
		}
		msgs = append(msgs, types.Message{Role: "system", Content: content})
	}
	return msgs, nil
}

func (m *Manager) Command(name string) (*CommandMatch, error) {
	name = normalizeCommandName(name)
	if name == "" {
		return nil, nil
	}
	catalog, err := m.Catalog()
	if err != nil {
		return nil, err
	}
	for _, skill := range catalog {
		for _, cmd := range skill.Commands {
			if normalizeCommandName(cmd.Name) == name {
				copy := cmd
				return &CommandMatch{Skill: skill, Command: copy}, nil
			}
		}
	}
	return nil, nil
}

func (m *Manager) ScanInstallSource(path string) (ScanResult, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return ScanResult{Safe: false, Findings: []ScanFinding{{Severity: "error", Reason: "path is required"}}}, nil
	}
	info, err := os.Stat(path)
	if err != nil {
		return ScanResult{}, fmt.Errorf("stat install source: %w", err)
	}
	var findings []ScanFinding
	visit := func(file string) error {
		data, err := os.ReadFile(file)
		if err != nil {
			return err
		}
		text := string(data)
		patterns := []struct {
			needle   string
			severity string
			reason   string
		}{
			{"rm -rf", "error", "contains destructive shell pattern rm -rf"},
			{"curl ", "warn", "contains curl command"},
			{"wget ", "warn", "contains wget command"},
			{"sudo ", "warn", "contains sudo command"},
			{"| sh", "error", "contains pipe-to-shell pattern"},
			{"| bash", "error", "contains pipe-to-shell pattern"},
			{"eval ", "warn", "contains eval command"},
		}
		for _, pattern := range patterns {
			if strings.Contains(strings.ToLower(text), strings.ToLower(pattern.needle)) {
				findings = append(findings, ScanFinding{Severity: pattern.severity, Reason: pattern.reason, Path: file})
			}
		}
		return nil
	}
	if info.IsDir() {
		err = filepath.WalkDir(path, func(current string, d os.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if d.IsDir() {
				return nil
			}
			if strings.EqualFold(d.Name(), "SKILL.md") || strings.HasSuffix(strings.ToLower(d.Name()), ".md") {
				return visit(current)
			}
			return nil
		})
	} else {
		err = visit(path)
	}
	if err != nil {
		return ScanResult{}, fmt.Errorf("scan install source: %w", err)
	}
	safe := true
	for _, finding := range findings {
		if finding.Severity == "error" {
			safe = false
			break
		}
	}
	return ScanResult{Safe: safe, Findings: findings}, nil
}

func (m *Manager) InstallManagedSkill(sourcePath string) (InstallResult, error) {
	if m == nil || m.cfg == nil {
		return InstallResult{}, fmt.Errorf("skill manager is not configured")
	}
	sourcePath = strings.TrimSpace(sourcePath)
	if sourcePath == "" {
		return InstallResult{}, fmt.Errorf("source path is required")
	}
	scan, err := m.ScanInstallSource(sourcePath)
	if err != nil {
		return InstallResult{}, err
	}
	skillFile, rootDir, err := resolveInstallSource(sourcePath)
	if err != nil {
		return InstallResult{Scan: scan}, err
	}
	parsed, ok, err := loadSkillFile(skillFile, SourceSpec{Kind: SourceManaged, Path: m.cfg.ManagedSkillsDir(), Rank: 20}, m.cfg, m.currentVersion)
	if err != nil {
		return InstallResult{Scan: scan}, err
	}
	if !ok {
		return InstallResult{Scan: scan}, fmt.Errorf("source skill is not loadable")
	}
	targetDir := filepath.Join(m.cfg.ManagedSkillsDir(), parsed.ID)
	if err := copyTree(rootDir, targetDir); err != nil {
		return InstallResult{Scan: scan}, fmt.Errorf("install managed skill: %w", err)
	}
	if _, err := m.Refresh(); err != nil {
		return InstallResult{Installed: true, TargetDir: targetDir, Skill: &parsed, Scan: scan}, err
	}
	return InstallResult{Installed: true, TargetDir: targetDir, Skill: &parsed, Scan: scan}, nil
}

func (m *Manager) snapshot(force bool) (CatalogSnapshot, error) {
	if m == nil || m.cfg == nil {
		return CatalogSnapshot{}, nil
	}
	fingerprint := computeFingerprint(m.Sources())
	m.mu.Lock()
	cached := m.cache
	m.mu.Unlock()
	if !force && cached.snapshot.Generation > 0 && cached.fingerprint == fingerprint {
		return cached.snapshot, nil
	}
	snapshot, err := m.buildSnapshot(fingerprint)
	if err != nil {
		return CatalogSnapshot{}, err
	}
	snapshot.AutoRefreshed = !force && cached.snapshot.Generation > 0 && cached.fingerprint != fingerprint
	m.mu.Lock()
	m.cache = cacheState{fingerprint: fingerprint, generation: snapshot.Generation, refreshedAt: snapshot.RefreshedAt, snapshot: snapshot}
	m.mu.Unlock()
	return snapshot, nil
}

func (m *Manager) buildSnapshot(fingerprint string) (CatalogSnapshot, error) {
	entriesByID := make(map[string][]CatalogEntry)
	for _, source := range m.Sources() {
		loaded, err := loadSource(source, m.cfg, m.currentVersion)
		if err != nil {
			return CatalogSnapshot{}, err
		}
		for _, item := range loaded {
			entry := CatalogEntry{
				Skill:          item.Skill,
				Status:         item.Status,
				BlockedReasons: append([]string(nil), item.Blocked...),
				Overrides:      appliedOverrideFields(item.Skill.ID, m.cfg),
				Selected:       false,
			}
			entriesByID[strings.ToLower(item.Skill.ID)] = append(entriesByID[strings.ToLower(item.Skill.ID)], entry)
		}
	}
	var entries []CatalogEntry
	keys := make([]string, 0, len(entriesByID))
	for key := range entriesByID {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		group := entriesByID[key]
		sort.Slice(group, func(i, j int) bool {
			if group[i].Skill.Rank != group[j].Skill.Rank {
				return group[i].Skill.Rank < group[j].Skill.Rank
			}
			return group[i].Skill.Path < group[j].Skill.Path
		})
		selected := -1
		for i, g := range slices.Backward(group) {
			if g.Status == "active" {
				selected = i
				break
			}
		}
		for i := range group {
			if selected >= 0 && i == selected {
				group[i].Selected = true
			} else if group[i].Status == "active" && selected >= 0 {
				group[i].Status = "shadowed"
				group[i].ShadowedBy = group[selected].Skill.Path
			}
			entries = append(entries, group[i])
		}
	}
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].Skill.Rank != entries[j].Skill.Rank {
			return entries[i].Skill.Rank < entries[j].Skill.Rank
		}
		if entries[i].Skill.ID != entries[j].Skill.ID {
			return entries[i].Skill.ID < entries[j].Skill.ID
		}
		return entries[i].Skill.Path < entries[j].Skill.Path
	})
	generation := time.Now().UTC().UnixNano()
	return CatalogSnapshot{Generation: generation, RefreshedAt: time.Now().UTC(), Fingerprint: fingerprint, CurrentVersion: m.currentVersion, Entries: entries}, nil
}

func skillAppliesToProfile(skill Skill, activeProfile string) bool {
	if len(skill.Agents) == 0 {
		return true
	}
	activeProfile = strings.TrimSpace(activeProfile)
	if activeProfile == "" {
		return false
	}
	for _, raw := range skill.Agents {
		if strings.EqualFold(strings.TrimSpace(raw), activeProfile) {
			return true
		}
	}
	return false
}

func loadSource(source SourceSpec, cfg *config.Config, currentVersion string) ([]loadedSkill, error) {
	root := strings.TrimSpace(source.Path)
	if root == "" {
		return nil, nil
	}
	info, err := os.Stat(root)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("stat skills source %s (%s): %w", source.Kind, root, err)
	}
	if !info.IsDir() {
		return nil, nil
	}
	var paths []string
	if err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		if strings.EqualFold(d.Name(), "SKILL.md") {
			paths = append(paths, path)
		}
		return nil
	}); err != nil {
		return nil, fmt.Errorf("walk skills source %s (%s): %w", source.Kind, root, err)
	}
	sort.Strings(paths)
	loaded := make([]loadedSkill, 0, len(paths))
	for _, path := range paths {
		skill, ok, err := loadSkillFile(path, source, cfg, currentVersion)
		if err != nil {
			return nil, err
		}
		if ok {
			loaded = append(loaded, loadedSkill{Skill: skill, Status: "active"})
			continue
		}
		blocked, err := inspectSkillFile(path, source, cfg, currentVersion)
		if err != nil {
			return nil, err
		}
		loaded = append(loaded, blocked)
	}
	return loaded, nil
}

func inspectSkillFile(path string, source SourceSpec, cfg *config.Config, currentVersion string) (loadedSkill, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return loadedSkill{}, fmt.Errorf("read skill %s: %w", path, err)
	}
	fm, body, err := parseSkillMarkdown(path, string(data))
	if err != nil {
		return loadedSkill{}, err
	}
	skill := buildSkill(path, source, fm, body, cfg)
	blocked := blockedReasons(fm, cfg, currentVersion, strings.TrimSpace(skill.ID))
	if strings.TrimSpace(skill.Content) == "" {
		blocked = append(blocked, "empty skill body")
	}
	if len(blocked) == 0 {
		blocked = append(blocked, "not selected")
	}
	return loadedSkill{Skill: skill, Status: "blocked", Blocked: blocked}, nil
}

func loadSkillFile(path string, source SourceSpec, cfg *config.Config, currentVersion string) (Skill, bool, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Skill{}, false, fmt.Errorf("read skill %s: %w", path, err)
	}
	fm, body, err := parseSkillMarkdown(path, string(data))
	if err != nil {
		return Skill{}, false, err
	}
	blocked := blockedReasons(fm, cfg, currentVersion, strings.TrimSpace(fm.ID))
	body = strings.TrimSpace(body)
	if len(blocked) > 0 || body == "" {
		return Skill{}, false, nil
	}
	return buildSkill(path, source, fm, body, cfg), true, nil
}

func blockedReasons(fm Frontmatter, cfg *config.Config, currentVersion string, rawID string) []string {
	var blocked []string
	if fm.Enabled != nil && !*fm.Enabled {
		blocked = append(blocked, "disabled by frontmatter")
	}
	if reasons := requirementFailures(fm.Requirements, cfg, currentVersion); len(reasons) > 0 {
		blocked = append(blocked, reasons...)
	}
	if cfg != nil && cfg.SkillOverrides != nil {
		id := strings.TrimSpace(rawID)
		if id == "" {
			id = slugify(strings.TrimSpace(fm.Name))
		}
		if override, ok := cfg.SkillOverrides[strings.ToLower(id)]; ok && override.Enabled != nil && !*override.Enabled {
			blocked = append(blocked, "disabled by skills.overrides")
		}
	}
	return blocked
}

func requirementFailures(req Requirements, cfg *config.Config, currentVersion string) []string {
	var blocked []string
	if len(req.OS) > 0 {
		matched := false
		for _, candidate := range req.OS {
			if strings.EqualFold(strings.TrimSpace(candidate), runtime.GOOS) {
				matched = true
				break
			}
		}
		if !matched {
			blocked = append(blocked, "OS does not match requires.os")
		}
	}
	for _, name := range req.Env {
		if _, ok := os.LookupEnv(strings.TrimSpace(name)); !ok {
			blocked = append(blocked, "missing env var "+strings.TrimSpace(name))
		}
	}
	for _, name := range req.Binaries {
		if _, err := exec.LookPath(strings.TrimSpace(name)); err != nil {
			blocked = append(blocked, "missing binary "+strings.TrimSpace(name))
		}
	}
	for _, path := range req.Config {
		if !configPathEnabled(cfg, path) {
			blocked = append(blocked, "config gate disabled: "+strings.TrimSpace(path))
		}
	}
	if minVersion := strings.TrimSpace(req.MinKoiosVersion); minVersion != "" {
		if compareVersions(currentVersion, minVersion) < 0 {
			blocked = append(blocked, "requires Koios >= "+minVersion)
		}
	}
	return blocked
}

func buildSkill(path string, source SourceSpec, fm Frontmatter, body string, cfg *config.Config) Skill {
	id := strings.TrimSpace(fm.ID)
	if id == "" {
		id = slugify(filepath.Base(filepath.Dir(path)))
	}
	name := strings.TrimSpace(fm.Name)
	if name == "" {
		name = id
	}
	skill := Skill{
		ID:          id,
		Name:        name,
		Description: strings.TrimSpace(fm.Description),
		Content:     strings.TrimSpace(body),
		Source:      source.Kind,
		Path:        path,
		Rank:        source.Rank,
		Agents:      trimDedup(fm.Agents),
		Version:     strings.TrimSpace(fm.Version),
		Origin:      strings.TrimSpace(fm.Origin),
		Trust:       normalizeTrust(strings.TrimSpace(fm.Trust)),
		Managed:     fm.Managed != nil && *fm.Managed,
		Setup:       trimDedup(fm.Setup),
		Commands:    normalizeCommands(fm.Commands),
	}
	if skill.Origin == "" {
		skill.Origin = string(source.Kind)
	}
	return applyOverrides(skill, cfg)
}

func parseSkillMarkdown(path, content string) (Frontmatter, string, error) {
	trimmed := strings.TrimSpace(content)
	if !strings.HasPrefix(trimmed, "---\n") && trimmed != "---" {
		return Frontmatter{}, "", fmt.Errorf("skill %s is missing YAML frontmatter", path)
	}
	lines := strings.Split(content, "\n")
	if len(lines) == 0 || strings.TrimSpace(lines[0]) != "---" {
		return Frontmatter{}, "", fmt.Errorf("skill %s is missing YAML frontmatter opener", path)
	}
	end := -1
	for i := 1; i < len(lines); i++ {
		if strings.TrimSpace(lines[i]) == "---" {
			end = i
			break
		}
	}
	if end == -1 {
		return Frontmatter{}, "", fmt.Errorf("skill %s is missing YAML frontmatter closer", path)
	}
	var fm Frontmatter
	frontmatter := strings.Join(lines[1:end], "\n")
	if err := yaml.Unmarshal([]byte(frontmatter), &fm); err != nil {
		return Frontmatter{}, "", fmt.Errorf("parse skill frontmatter %s: %w", path, err)
	}
	body := strings.Join(lines[end+1:], "\n")
	return fm, body, nil
}

func configPathEnabled(cfg *config.Config, path string) bool {
	if cfg == nil {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(path)) {
	case "tools.exec.enabled":
		return cfg.ExecEnabled
	case "tools.code_execution.enabled":
		return cfg.CodeExecutionEnabled
	case "tools.process.enabled":
		return cfg.ProcessEnabled
	case "memory.inject":
		return cfg.MemoryInject
	case "memory.embed.enabled":
		return cfg.MemoryEmbedEnabled
	case "workspace.per_agent":
		return cfg.WorkspacePerAgent
	case "heartbeat.enabled":
		return cfg.HeartbeatEnabled
	case "browser.enabled":
		return len(cfg.Browser.Profiles) > 0
	default:
		return false
	}
}

func applyOverrides(skill Skill, cfg *config.Config) Skill {
	if cfg == nil || cfg.SkillOverrides == nil {
		return skill
	}
	override, ok := cfg.SkillOverrides[strings.ToLower(strings.TrimSpace(skill.ID))]
	if !ok {
		return skill
	}
	if override.Enabled != nil && !*override.Enabled {
		skill.Content = ""
	}
	if value := strings.TrimSpace(override.Name); value != "" {
		skill.Name = value
	}
	if value := strings.TrimSpace(override.Description); value != "" {
		skill.Description = value
	}
	if len(override.Agents) > 0 {
		skill.Agents = trimDedup(override.Agents)
	}
	if value := strings.TrimSpace(override.Trust); value != "" {
		skill.Trust = normalizeTrust(value)
	}
	if len(override.Commands) > 0 {
		commands := make([]Command, 0, len(override.Commands))
		for _, cmd := range override.Commands {
			commands = append(commands, Command{Name: cmd.Name, Description: cmd.Description, AssistantText: cmd.AssistantText})
		}
		skill.Commands = normalizeCommands(commands)
	}
	return skill
}

func appliedOverrideFields(skillID string, cfg *config.Config) []string {
	if cfg == nil || cfg.SkillOverrides == nil {
		return nil
	}
	override, ok := cfg.SkillOverrides[strings.ToLower(strings.TrimSpace(skillID))]
	if !ok {
		return nil
	}
	var fields []string
	if override.Enabled != nil {
		fields = append(fields, "enabled")
	}
	if strings.TrimSpace(override.Name) != "" {
		fields = append(fields, "name")
	}
	if strings.TrimSpace(override.Description) != "" {
		fields = append(fields, "description")
	}
	if len(override.Agents) > 0 {
		fields = append(fields, "agents")
	}
	if strings.TrimSpace(override.Trust) != "" {
		fields = append(fields, "trust")
	}
	if len(override.Commands) > 0 {
		fields = append(fields, "commands")
	}
	if len(fields) == 0 {
		return nil
	}
	return fields
}

func normalizeCommands(values []Command) []Command {
	if len(values) == 0 {
		return nil
	}
	out := make([]Command, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, raw := range values {
		cmd := Command{
			Name:          normalizeCommandName(raw.Name),
			Description:   strings.TrimSpace(raw.Description),
			AssistantText: strings.TrimSpace(raw.AssistantText),
		}
		if cmd.Name == "" {
			continue
		}
		if _, ok := seen[cmd.Name]; ok {
			continue
		}
		seen[cmd.Name] = struct{}{}
		out = append(out, cmd)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func normalizeCommandName(name string) string {
	name = strings.TrimSpace(name)
	name = strings.TrimPrefix(name, "/")
	name = strings.ToLower(name)
	return slugify(name)
}

func normalizeTrust(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", "unknown":
		return "unknown"
	case "low", "medium", "high", "trusted":
		return strings.ToLower(strings.TrimSpace(value))
	default:
		return strings.ToLower(strings.TrimSpace(value))
	}
}

func trimDedup(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	out := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, raw := range values {
		value := strings.TrimSpace(raw)
		if value == "" {
			continue
		}
		key := strings.ToLower(value)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, value)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func slugify(raw string) string {
	raw = strings.ToLower(strings.TrimSpace(raw))
	if raw == "" {
		return ""
	}
	var b strings.Builder
	lastDash := false
	for _, r := range raw {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			lastDash = false
		case r == '-', r == '_', r == ' ', r == '.':
			if !lastDash && b.Len() > 0 {
				b.WriteByte('-')
				lastDash = true
			}
		}
	}
	return strings.Trim(b.String(), "-")
}

func loadKoiosVersion(projectRoot string) string {
	if strings.TrimSpace(projectRoot) == "" {
		return ""
	}
	data, err := os.ReadFile(filepath.Join(projectRoot, "VERSION"))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

func compareVersions(current, required string) int {
	currentParts := parseVersionParts(current)
	requiredParts := parseVersionParts(required)
	max := len(currentParts)
	if len(requiredParts) > max {
		max = len(requiredParts)
	}
	for i := 0; i < max; i++ {
		left := 0
		right := 0
		if i < len(currentParts) {
			left = currentParts[i]
		}
		if i < len(requiredParts) {
			right = requiredParts[i]
		}
		if left < right {
			return -1
		}
		if left > right {
			return 1
		}
	}
	return 0
}

func parseVersionParts(raw string) []int {
	fields := strings.Split(strings.TrimSpace(strings.TrimPrefix(raw, "v")), ".")
	out := make([]int, 0, len(fields))
	for _, field := range fields {
		field = strings.TrimSpace(field)
		if field == "" {
			continue
		}
		num, _ := strconv.Atoi(field)
		out = append(out, num)
	}
	return out
}

func computeFingerprint(sources []SourceSpec) string {
	hash := sha256.New()
	for _, source := range sources {
		_, _ = hash.Write([]byte(string(source.Kind)))
		_, _ = hash.Write([]byte{0})
		_, _ = hash.Write([]byte(source.Path))
		_, _ = hash.Write([]byte{0})
		_ = filepath.WalkDir(strings.TrimSpace(source.Path), func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				return nil
			}
			if !strings.EqualFold(d.Name(), "SKILL.md") {
				return nil
			}
			info, statErr := d.Info()
			if statErr != nil {
				return statErr
			}
			_, _ = hash.Write([]byte(path))
			_, _ = hash.Write([]byte(info.ModTime().UTC().Format(time.RFC3339Nano)))
			_, _ = hash.Write([]byte(strconv.FormatInt(info.Size(), 10)))
			_, _ = hash.Write([]byte{0})
			return nil
		})
	}
	return hex.EncodeToString(hash.Sum(nil))
}

func resolveInstallSource(path string) (skillFile string, rootDir string, err error) {
	info, err := os.Stat(path)
	if err != nil {
		return "", "", err
	}
	if info.IsDir() {
		skillFile = filepath.Join(path, "SKILL.md")
		if _, statErr := os.Stat(skillFile); statErr != nil {
			return "", "", fmt.Errorf("directory %s does not contain SKILL.md", path)
		}
		return skillFile, path, nil
	}
	if !strings.EqualFold(filepath.Base(path), "SKILL.md") {
		return "", "", fmt.Errorf("install source must be a SKILL.md file or a directory containing one")
	}
	return path, filepath.Dir(path), nil
}

func copyTree(sourceDir, targetDir string) error {
	if err := os.RemoveAll(targetDir); err != nil {
		return err
	}
	return filepath.Walk(sourceDir, func(path string, info fs.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(sourceDir, path)
		if err != nil {
			return err
		}
		target := filepath.Join(targetDir, rel)
		if info.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		data, err := os.ReadFile(path) // #nosec G122
		if err != nil {
			return err
		}
		return os.WriteFile(target, data, 0o600) // #nosec G703
	})
}
