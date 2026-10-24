package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dereksantos/cortex/internal/style"
)

// This file tests the startup banner's skill summary + one-shot project
// summary (the "precedence + show on startup" feature).
//
// The design (agreed with the user) is:
//
//	cortex v0.4.2  ·  ~/eng/projects/cortex  ·  claude-sonnet-4-6
//	Project: cortex (Go, 12 packages, 847 commits)
//	  "A general coding agent with a two-zone context window, memory
//	   tools, subagents, and a durable benchmark store."
//
//	Skills (3)
//	  system  writing-config        built-in, config playbook
//	  project my-test-playbook     .cortex/skills/
//	  user    code-review          ~/.cortex/skills/
//
// Precedence is system → project → user. The "Project:" line is a one-shot
// LLM summary; when the model is unavailable or the call fails, the banner
// degrades to the static header line (the existing behavior).
//
// This test is written FIRST and must fail before the production code exists.
// It exercises the user-facing surface (renderHeader + startupFacts) and the
// skill-tier classification, not a lower-level helper.

// The types (skillTier, skillEntry, startupSkillFacts) and the Name() method
// are declared in startup_skills.go (production). This test file uses them.

// TestSkillTierClassification pins the precedence order: a built-in skill
// is system, a skill from .cortex/skills (or .claude/skills, .agents/skills)
// is project, and a skill from ~/.cortex/skills is user.
func TestSkillTierClassification(t *testing.T) {
	tmp := t.TempDir()
	projectRoot := tmp
	userHome := filepath.Join(tmp, "home")
	_ = os.MkdirAll(filepath.Join(projectRoot, ".cortex", "skills", "my-test-playbook"), 0o755)
	_ = os.MkdirAll(filepath.Join(userHome, ".cortex", "skills", "code-review"), 0o755)

	// Write a minimal valid SKILL.md for each.
	writeSkillMD(t, filepath.Join(projectRoot, ".cortex", "skills", "my-test-playbook"), "my-test-playbook", "Test playbook")
	writeSkillMD(t, filepath.Join(userHome, ".cortex", "skills", "code-review"), "code-review", "Review code")

	tests := []struct {
		name string
		path string
		want skillTier
	}{
		{"built-in is system", defaultConfigSkillPath, tierSystem},
		{"project .cortex/skills", filepath.Join(projectRoot, ".cortex", "skills", "my-test-playbook"), tierProject},
		{"user ~/.cortex/skills", filepath.Join(userHome, ".cortex", "skills", "code-review"), tierUser},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := skillTierForPath(tt.path, projectRoot, userHome)
			if got != tt.want {
				t.Errorf("skillTierForPath(%q) = %v, want %v", tt.path, got, tt.want)
			}
		})
	}
}

// TestRenderHeaderWithSkills pins the exact banner output when skills and a
// project summary are present. The expected output matches the mockup the
// user approved.
func TestRenderHeaderWithSkills(t *testing.T) {
	f := headerFacts{
		Version: "0.4.2",
		Project: "~/eng/projects/cortex",
		Model:   "claude-sonnet-4-6",
	}
	f.Skills = startupSkillFacts{
		ProjectSummary: "A general coding agent with a two-zone context window, memory tools, subagents, and a durable benchmark store.",
		Skills: []skillEntry{
			{Name: "writing-config", Tier: tierSystem, Source: ""},
			{Name: "my-test-playbook", Tier: tierProject, Source: ".cortex/skills/"},
			{Name: "code-review", Tier: tierUser, Source: "~/.cortex/skills/"},
		},
	}

	defer style.ForceColor(false)()
	got := strings.Join(renderHeader(f), "\n")
	want := "cortex 0.4.2 · ~/eng/projects/cortex · claude-sonnet-4-6\n" +
		"Project: A general coding agent with a two-zone context window, memory tools, subagents, and a durable benchmark store.\n\n" +
		"Skills (3)\n" +
		"  system   writing-config\n" +
		"  project  my-test-playbook  .cortex/skills/\n" +
		"  user     code-review  ~/.cortex/skills/"

	if got != want {
		t.Errorf("renderHeader with skills changed.\n got: %q\nwant: %q", got, want)
	}
}

// TestRenderHeaderWithSkillsNoSummary pins the degraded output when the
// one-shot project summary is unavailable (model down, call failed, etc.).
// The "Project:" line is omitted; the skill list still renders.
func TestRenderHeaderWithSkillsNoSummary(t *testing.T) {
	f := headerFacts{
		Version: "0.4.2",
		Project: "~/eng/projects/cortex",
		Model:   "claude-sonnet-4-6",
	}
	f.Skills = startupSkillFacts{
		// ProjectSummary intentionally empty — the one-shot call failed.
		Skills: []skillEntry{
			{Name: "writing-config", Tier: tierSystem, Source: ""},
		},
	}

	defer style.ForceColor(false)()
	got := strings.Join(renderHeader(f), "\n")
	// The first line must still be the static header.
	wantFirst := "cortex 0.4.2 · ~/eng/projects/cortex · claude-sonnet-4-6"
	if !strings.HasPrefix(got, wantFirst) {
		t.Errorf("first line changed; got %q, want prefix %q", got, wantFirst)
	}
	// The "Project:" line must NOT appear when the summary is empty.
	if strings.Contains(got, "Project:") {
		t.Errorf("banner must not render a Project line when the summary is empty; got %q", got)
	}
	// The skill list must still render.
	if !strings.Contains(got, "Skills (1)") {
		t.Errorf("banner must render the skill list even without a project summary; got %q", got)
	}
	if !strings.Contains(got, "system   writing-config") {
		t.Errorf("banner must list the built-in skill; got %q", got)
	}
}

// TestRenderHeaderWithNoSkills pins the original behavior: no skills block,
// just the static header line. This is the backward-compat case.
func TestRenderHeaderWithNoSkills(t *testing.T) {
	f := headerFacts{
		Version: "0.3.0",
		Project: "~/eng/projects/cortex",
		Model:   "qwen3-coder-q3",
	}
	defer style.ForceColor(false)()
	got := strings.Join(renderHeader(f), "\n")
	want := "cortex 0.3.0 · ~/eng/projects/cortex · qwen3-coder-q3"
	if got != want {
		t.Errorf("no-skills header changed.\n got: %q\nwant: %q", got, want)
	}
}

// TestStartupSkillFactsFromSession pins that startupFacts gathers the skill
// list with correct tiers and order (system first, then project, then user).
// This is the session-level integration: it uses a real CortexSession with
// filesystem skills in a temp dir.
func TestStartupSkillFactsFromSession(t *testing.T) {
	tmp := t.TempDir()
	projectRoot := tmp
	userHome := filepath.Join(tmp, "home")
	_ = os.MkdirAll(filepath.Join(projectRoot, ".cortex", "skills", "proj-skill"), 0o755)
	_ = os.MkdirAll(filepath.Join(userHome, ".cortex", "skills", "user-skill"), 0o755)
	writeSkillMD(t, filepath.Join(projectRoot, ".cortex", "skills", "proj-skill"), "proj-skill", "Project skill")
	writeSkillMD(t, filepath.Join(userHome, ".cortex", "skills", "user-skill"), "user-skill", "User skill")

	// Point the session at the temp project root and user home.
	cs := newTestCortexSession(t, projectRoot)
	cs.userHomeOverride = userHome // test seam: override the user home dir

	f := cs.startupSkillFacts()
	if len(f.Skills) != 3 {
		t.Fatalf("expected 3 skills (1 built-in + 2 filesystem), got %d: %+v", len(f.Skills), f.Skills)
	}
	// Order must be: system, project, user.
	if f.Skills[0].Tier != tierSystem || f.Skills[0].Name != defaultConfigSkillName {
		t.Errorf("first skill must be the built-in (system tier), got %+v", f.Skills[0])
	}
	if f.Skills[1].Tier != tierProject || f.Skills[1].Name != "proj-skill" {
		t.Errorf("second skill must be the project skill, got %+v", f.Skills[1])
	}
	if f.Skills[2].Tier != tierUser || f.Skills[2].Name != "user-skill" {
		t.Errorf("third skill must be the user skill, got %+v", f.Skills[2])
	}
}

// TestStartupSkillFactsShadowed pins that when a project skill shadows the
// built-in, the banner shows the project version, not the built-in.
func TestStartupSkillFactsShadowed(t *testing.T) {
	tmp := t.TempDir()
	projectRoot := tmp
	userHome := filepath.Join(tmp, "home")
	// Project ships its own writing-config skill — it shadows the built-in.
	_ = os.MkdirAll(filepath.Join(projectRoot, ".cortex", "skills", "writing-config"), 0o755)
	writeSkillMD(t, filepath.Join(projectRoot, ".cortex", "skills", "writing-config"), "writing-config", "Custom config playbook")

	cs := newTestCortexSession(t, projectRoot)
	cs.userHomeOverride = userHome

	f := cs.startupSkillFacts()
	// Only one "writing-config" entry — the project one, at the project tier.
	var wc []skillEntry
	for _, s := range f.Skills {
		if s.Name == defaultConfigSkillName {
			wc = append(wc, s)
		}
	}
	if len(wc) != 1 {
		t.Fatalf("expected exactly 1 writing-config entry (shadowed), got %d: %+v", len(wc), wc)
	}
	if wc[0].Tier != tierProject {
		t.Errorf("shadowed writing-config must be at the project tier, got %v", wc[0].Tier)
	}
}

// TestProjectSummaryOneShot pins the one-shot LLM call: when the model is
// available, startupFacts produces a non-empty ProjectSummary. When the
// model is unavailable (no backend), it produces "" and the banner degrades.
//
// This test uses a fake LLM provider (the same pattern as greeting_test.go
// and other cmd/cortex tests) to avoid a real network call.
func TestProjectSummaryOneShot(t *testing.T) {
	// Case 1: model available → summary is non-empty.
	{
		cs := newTestCortexSessionWithFakeLLM(t, "A test project for the cortex agent.")
		f := cs.startupSkillFacts()
		if f.ProjectSummary == "" {
			t.Errorf("with a fake LLM, ProjectSummary should be non-empty; got empty")
		}
	}
	// Case 2: no model → summary is empty (graceful degradation).
	{
		cs := newTestCortexSession(t, t.TempDir())
		cs.userHomeOverride = filepath.Join(t.TempDir(), "home")
		f := cs.startupSkillFacts()
		if f.ProjectSummary != "" {
			t.Errorf("with no LLM backend, ProjectSummary should be empty; got %q", f.ProjectSummary)
		}
	}
}

// writeSkillMD writes a minimal valid SKILL.md to the given directory.
// Package-level so all tests in this file can use it.
func writeSkillMD(t *testing.T, dir, name, desc string) {
	t.Helper()
	content := "---\nname: " + name + "\ndescription: " + desc + "\n---\n# " + name + "\n"
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(content), 0o644); err != nil {
		t.Fatalf("failed to write SKILL.md in %s: %v", dir, err)
	}
}

// newTestCortexSession is a test seam: it builds a CortexSession rooted at
// the given project dir, with no real LLM backend. It must exist for the
// tests above to compile. (The production code doesn't need it — it's
// only for this test file.)
func newTestCortexSession(t *testing.T, root string) *CortexSession {
	t.Helper()
	// This is a test helper; the real session is built in session_core.go.
	// We need a minimal session that has cs.root() and cs.userHomeOverride
	// set, plus cs.Config and cs.skillsDirs() working.
	cs := &CortexSession{}
	cs.rootOverride = root
	return cs
}

// newTestCortexSessionWithFakeLLM is like newTestCortexSession but with a
// fake LLM provider that returns a canned one-shot summary.
func newTestCortexSessionWithFakeLLM(t *testing.T, cannedSummary string) *CortexSession {
	t.Helper()
	cs := newTestCortexSession(t, t.TempDir())
	cs.userHomeOverride = filepath.Join(t.TempDir(), "home")
	cs.fakeSummary = cannedSummary
	return cs
}
