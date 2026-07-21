package skills

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/ffimnsr/koios/internal/config"
)

func writeSkill(t *testing.T, root, dir, content string) string {
	t.Helper()
	path := filepath.Join(root, dir, "SKILL.md")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir skill dir: %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write skill: %v", err)
	}
	return path
}

func TestManagerCatalogRespectsPrecedence(t *testing.T) {
	projectRoot := t.TempDir()
	workspaceRoot := t.TempDir()
	home := t.TempDir()
	personalRoot := filepath.Join(home, ".koios", "workspace", "skills")
	cfg := config.Default()
	cfg.WorkspaceRoot = workspaceRoot
	cfg.SkillDirs = []string{filepath.Join(t.TempDir(), "extra")}
	if err := os.MkdirAll(personalRoot, 0o755); err != nil {
		t.Fatalf("mkdir personal root: %v", err)
	}

	writeSkill(t, filepath.Join(projectRoot, "internal", "skills", "bundled"), "review", `---
id: review
name: Review
---
Bundled review instructions.
`)
	writeSkill(t, filepath.Join(workspaceRoot, "managed", "skills"), "review", `---
id: review
name: Review
---
Managed review instructions.
`)
	writeSkill(t, cfg.SkillDirs[0], "review", `---
id: review
name: Review
---
Extra review instructions.
`)
	writeSkill(t, personalRoot, "review", `---
id: review
name: Review
---
Personal review instructions.
`)
	writeSkill(t, filepath.Join(projectRoot, "skills"), "review", `---
id: review
name: Review
---
Project review instructions.
`)
	writeSkill(t, filepath.Join(workspaceRoot, "skills"), "review", `---
id: review
name: Review
---
Workspace review instructions.
`)

	t.Setenv("HOME", home)
	if got := cfg.PersonalSkillsDir(); got != personalRoot {
		t.Fatalf("PersonalSkillsDir() = %q want %q", got, personalRoot)
	}

	mgr := NewManager(cfg, projectRoot)
	catalog, err := mgr.Catalog()
	if err != nil {
		t.Fatalf("Catalog: %v", err)
	}
	if len(catalog) != 1 {
		t.Fatalf("expected one merged skill, got %#v", catalog)
	}
	if !strings.Contains(catalog[0].Content, "Workspace review instructions") {
		t.Fatalf("expected workspace override to win, got %#v", catalog[0])
	}
	if catalog[0].Source != SourceWorkspace {
		t.Fatalf("expected workspace source to win, got %s", catalog[0].Source)
	}
}

func TestManagerSystemMessagesAppliesAgentAllowlist(t *testing.T) {
	projectRoot := t.TempDir()
	workspaceRoot := t.TempDir()
	cfg := config.Default()
	cfg.WorkspaceRoot = workspaceRoot

	writeSkill(t, filepath.Join(workspaceRoot, "skills"), "deploy-review", `---
id: deploy-review
name: Deploy Review
---
Check migrations and rollout safety.
`)
	writeSkill(t, filepath.Join(workspaceRoot, "skills"), "security-review", `---
id: security-review
name: Security Review
---
Check auth, secrets, and input validation.
`)

	mgr := NewManager(cfg, projectRoot)
	msgs, err := mgr.SystemMessages("", []string{"security-review"})
	if err != nil {
		t.Fatalf("SystemMessages: %v", err)
	}
	if len(msgs) != 1 {
		t.Fatalf("expected one skill message, got %#v", msgs)
	}
	if !strings.Contains(msgs[0].Content, "Security Review") {
		t.Fatalf("expected security-review content, got %#v", msgs[0])
	}
}

func TestManagerSystemMessagesHonorsSkillAgentFrontmatter(t *testing.T) {
	projectRoot := t.TempDir()
	workspaceRoot := t.TempDir()
	cfg := config.Default()
	cfg.WorkspaceRoot = workspaceRoot

	writeSkill(t, filepath.Join(workspaceRoot, "skills"), "ops", `---
id: ops
name: Ops
agents: ["ops"]
---
Ops-specific instructions.
`)

	mgr := NewManager(cfg, projectRoot)
	msgs, err := mgr.SystemMessages("focus", nil)
	if err != nil {
		t.Fatalf("SystemMessages focus: %v", err)
	}
	if len(msgs) != 0 {
		t.Fatalf("expected no messages for non-matching profile, got %#v", msgs)
	}
	msgs, err = mgr.SystemMessages("ops", nil)
	if err != nil {
		t.Fatalf("SystemMessages ops: %v", err)
	}
	if len(msgs) != 1 || !strings.Contains(msgs[0].Content, "Ops-specific instructions") {
		t.Fatalf("expected ops skill message, got %#v", msgs)
	}
}

func TestManagerCatalogAppliesRequirements(t *testing.T) {
	projectRoot := t.TempDir()
	workspaceRoot := t.TempDir()
	cfg := config.Default()
	cfg.WorkspaceRoot = workspaceRoot
	cfg.ExecEnabled = true
	cfg.ProcessEnabled = false
	t.Setenv("KOIOS_SKILL_TEST", "1")

	writeSkill(t, filepath.Join(workspaceRoot, "skills"), "allowed", `---
id: allowed
name: Allowed
requires:
  os: ["`+runtime.GOOS+`"]
  env: ["KOIOS_SKILL_TEST"]
  binaries: ["sh"]
  config: ["tools.exec.enabled"]
---
Allowed skill.
`)
	writeSkill(t, filepath.Join(workspaceRoot, "skills"), "blocked", `---
id: blocked
name: Blocked
requires:
  config: ["tools.process.enabled"]
---
Blocked skill.
`)

	mgr := NewManager(cfg, projectRoot)
	catalog, err := mgr.Catalog()
	if err != nil {
		t.Fatalf("Catalog: %v", err)
	}
	if len(catalog) != 1 || catalog[0].ID != "allowed" {
		t.Fatalf("expected only allowed skill, got %#v", catalog)
	}
}

func TestManagerCatalogAppliesOverrides(t *testing.T) {
	projectRoot := t.TempDir()
	workspaceRoot := t.TempDir()
	cfg := config.Default()
	cfg.WorkspaceRoot = workspaceRoot
	cfg.SkillOverrides = map[string]config.SkillOverride{
		"ops": {
			Name:        "Operations",
			Description: "Override description",
			Agents:      []string{"ops"},
			Trust:       "trusted",
			Commands: []config.SkillCommandOverride{{
				Name:          "ops-check",
				AssistantText: "Run ops checks: {{args}}",
			}},
		},
	}
	writeSkill(t, filepath.Join(workspaceRoot, "skills"), "ops", `---
id: ops
name: Ops
---
Ops body.
`)
	mgr := NewManager(cfg, projectRoot)
	catalog, err := mgr.Catalog()
	if err != nil {
		t.Fatalf("Catalog: %v", err)
	}
	if len(catalog) != 1 {
		t.Fatalf("expected one skill, got %#v", catalog)
	}
	if catalog[0].Name != "Operations" || catalog[0].Description != "Override description" || catalog[0].Trust != "trusted" {
		t.Fatalf("expected overrides to apply, got %#v", catalog[0])
	}
	if len(catalog[0].Commands) != 1 || catalog[0].Commands[0].Name != "ops-check" {
		t.Fatalf("expected override commands, got %#v", catalog[0].Commands)
	}
}

func TestManagerRefreshAutoRefreshesAfterChange(t *testing.T) {
	projectRoot := t.TempDir()
	workspaceRoot := t.TempDir()
	cfg := config.Default()
	cfg.WorkspaceRoot = workspaceRoot
	path := writeSkill(t, filepath.Join(workspaceRoot, "skills"), "review", `---
id: review
name: Review
---
First body.
`)
	mgr := NewManager(cfg, projectRoot)
	first, err := mgr.CatalogDetails()
	if err != nil {
		t.Fatalf("CatalogDetails first: %v", err)
	}
	if err := os.WriteFile(path, []byte(`---
id: review
name: Review
---
Second body.
`), 0o600); err != nil {
		t.Fatalf("rewrite skill: %v", err)
	}
	second, err := mgr.CatalogDetails()
	if err != nil {
		t.Fatalf("CatalogDetails second: %v", err)
	}
	if second.Generation == first.Generation {
		t.Fatalf("expected generation change after update, got %d == %d", second.Generation, first.Generation)
	}
	if !second.AutoRefreshed {
		t.Fatalf("expected auto refresh after file change")
	}
}

func TestManagerCommandFindsSelectedSkillCommand(t *testing.T) {
	projectRoot := t.TempDir()
	workspaceRoot := t.TempDir()
	cfg := config.Default()
	cfg.WorkspaceRoot = workspaceRoot
	writeSkill(t, filepath.Join(workspaceRoot, "skills"), "ops", `---
id: ops
name: Ops
commands:
  - name: ops-check
    assistant_text: |
      Run ops checks with: {{args}}
---
Ops body.
`)
	mgr := NewManager(cfg, projectRoot)
	match, err := mgr.Command("ops-check")
	if err != nil {
		t.Fatalf("Command: %v", err)
	}
	if match == nil || match.Command.Name != "ops-check" {
		t.Fatalf("expected command match, got %#v", match)
	}
}

func TestManagerInstallManagedSkillScansAndCopies(t *testing.T) {
	projectRoot := t.TempDir()
	workspaceRoot := t.TempDir()
	cfg := config.Default()
	cfg.WorkspaceRoot = workspaceRoot
	sourceRoot := filepath.Join(t.TempDir(), "incoming-skill")
	writeSkill(t, sourceRoot, ".", `---
id: imported
name: Imported
managed: true
---
Imported skill body.
`)
	mgr := NewManager(cfg, projectRoot)
	result, err := mgr.InstallManagedSkill(sourceRoot)
	if err != nil {
		t.Fatalf("InstallManagedSkill: %v", err)
	}
	if !result.Installed || result.Skill == nil || result.Skill.ID != "imported" {
		t.Fatalf("unexpected install result: %#v", result)
	}
	if _, err := os.Stat(filepath.Join(cfg.ManagedSkillsDir(), "imported", "SKILL.md")); err != nil {
		t.Fatalf("expected managed skill to be copied: %v", err)
	}
}

func TestParseSkillMarkdownRequiresFrontmatter(t *testing.T) {
	_, _, err := parseSkillMarkdown("/tmp/SKILL.md", "# missing frontmatter")
	if err == nil || !strings.Contains(err.Error(), "missing YAML frontmatter") {
		t.Fatalf("expected frontmatter error, got %v", err)
	}
}
