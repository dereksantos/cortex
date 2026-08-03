// skills_test.go covers the Agent Skills index injection (skills.go): note
// rendering (empty/populated/capped), the coder-only guarantee (subagent
// seeds must never see it), and /context surfacing. internal/skills'
// discovery+parsing behavior itself is covered by internal/skills/skills_test.go;
// this file exercises only the cmd/cortex-side wiring on top of it.
package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dereksantos/cortex/internal/tools"
)

// writeTestSkill creates dir/name/SKILL.md with a minimal valid frontmatter
// block for the given description.
func writeTestSkill(t *testing.T, dir, name, description string) {
	t.Helper()
	skillDir := filepath.Join(dir, name)
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatal(err)
	}
	body := "---\nname: " + name + "\ndescription: " + description + "\n---\n\nBody.\n"
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestSkillsIndexNoteEmptyWhenNoSkills(t *testing.T) {
	dir := t.TempDir()
	cs := &CortexSession{Config: &Config{Skills: SkillsConfig{Dirs: []string{dir}}}}
	if note := cs.skillsIndexNote(); note != "" {
		t.Errorf("skillsIndexNote() = %q, want \"\" (no skills discovered)", note)
	}
}

func TestSkillsIndexNoteDisabledByConfig(t *testing.T) {
	dir := t.TempDir()
	writeTestSkill(t, dir, "a-skill", "Does a thing.")
	no := false
	cs := &CortexSession{Config: &Config{Skills: SkillsConfig{Dirs: []string{dir}, Enabled: &no}}}
	if note := cs.skillsIndexNote(); note != "" {
		t.Errorf("skillsIndexNote() with skills.enabled=false = %q, want \"\"", note)
	}
}

// TestSkillsIndexNoteRendersTwoSkills pins the exact rendered note for a
// 2-skill example.
func TestSkillsIndexNoteRendersTwoSkills(t *testing.T) {
	dir := t.TempDir()
	writeTestSkill(t, dir, "pdf-tools", "Extract text and tables from PDFs.")
	writeTestSkill(t, dir, "web-search", "Search the web for current information.")
	cs := &CortexSession{Config: &Config{Skills: SkillsConfig{Dirs: []string{dir}}}}

	note := cs.skillsIndexNote()
	if note == "" {
		t.Fatal("skillsIndexNote() is empty, want a rendered 2-skill index")
	}
	wantLines := []string{
		"## Skills",
		"read_file",
		"- pdf-tools: Extract text and tables from PDFs. (SKILL.md: " + filepath.Join(dir, "pdf-tools", "SKILL.md") + ")",
		"- web-search: Search the web for current information. (SKILL.md: " + filepath.Join(dir, "web-search", "SKILL.md") + ")",
	}
	for _, want := range wantLines {
		if !strings.Contains(note, want) {
			t.Errorf("skillsIndexNote() = %q, missing expected substring %q", note, want)
		}
	}
	if strings.Contains(note, "omitted") {
		t.Errorf("skillsIndexNote() = %q, should not mention omission under the default cap", note)
	}
}

// TestSkillsIndexNoteCap covers the skills.index_max cap: over-cap discovery
// truncates to the first N (by discovery order) and appends an omission note.
func TestSkillsIndexNoteCap(t *testing.T) {
	dir := t.TempDir()
	writeTestSkill(t, dir, "skill-a", "First.")
	writeTestSkill(t, dir, "skill-b", "Second.")
	writeTestSkill(t, dir, "skill-c", "Third.")
	cs := &CortexSession{Config: &Config{Skills: SkillsConfig{Dirs: []string{dir}, IndexMax: 2}}}

	note := cs.skillsIndexNote()
	if !strings.Contains(note, "skill-a") || !strings.Contains(note, "skill-b") {
		t.Errorf("skillsIndexNote() = %q, want the first 2 (by discovery order) present", note)
	}
	if strings.Contains(note, "skill-c") {
		t.Errorf("skillsIndexNote() = %q, want skill-c omitted past the cap", note)
	}
	if !strings.Contains(note, "1 more skill") {
		t.Errorf("skillsIndexNote() = %q, want a line noting 1 skill was omitted", note)
	}
}

// TestSkillsDirsDefaultOrder pins the discovery precedence: project
// cortex-native, project Claude-compat, project Agents-compat, then user —
// project wins over user, cortex-native wins over compat.
func TestSkillsDirsDefaultOrder(t *testing.T) {
	root := t.TempDir()
	home := t.TempDir()
	t.Setenv("CORTEX_HOME", home)
	cs := &CortexSession{Config: &Config{}, deleteRoot: root}

	got := cs.skillsDirs()
	want := []string{
		filepath.Join(root, ".cortex", "skills"),
		filepath.Join(root, ".claude", "skills"),
		filepath.Join(root, ".agents", "skills"),
		filepath.Join(home, "skills"),
	}
	if len(got) != len(want) {
		t.Fatalf("skillsDirs() = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("skillsDirs()[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

// TestSkillsDirsOverrideReplacesDefaultsEntirely covers skills.dirs: when
// set, it REPLACES the four defaults rather than adding to them.
func TestSkillsDirsOverrideReplacesDefaultsEntirely(t *testing.T) {
	override := []string{"/custom/one", "/custom/two"}
	cs := &CortexSession{Config: &Config{Skills: SkillsConfig{Dirs: override}}, deleteRoot: t.TempDir()}
	got := cs.skillsDirs()
	if len(got) != 2 || got[0] != override[0] || got[1] != override[1] {
		t.Errorf("skillsDirs() = %v, want the override verbatim %v", got, override)
	}
}

// TestStudySubagentSeedExcludesSkillsIndex is the coder-only guarantee: the
// skills index must never reach a subagent's opening request, even when it
// WOULD be injected for the coder (a live skill is discoverable and
// skillsIndexNote() is non-empty).
func TestStudySubagentSeedExcludesSkillsIndex(t *testing.T) {
	dir := t.TempDir()
	writeTestSkill(t, dir, "secret-skill", "Should never leak into a subagent seed.")
	study := ModelSpec{Model: "study-m", Endpoint: "http://study.example"}
	cs := &CortexSession{
		Config: &Config{Skills: SkillsConfig{Dirs: []string{dir}}},
		Study:  study,
	}

	// Sanity: the coder-side note DOES see the skill (otherwise this test
	// would pass trivially).
	note := cs.skillsIndexNote()
	if !strings.Contains(note, "secret-skill") {
		t.Fatalf("sanity check failed: skillsIndexNote() = %q, want it to contain secret-skill", note)
	}

	req := cs.subagentRequest(tools.Study, "study seed text")
	if req.EphemeralSystem != "" {
		t.Errorf("subagent request EphemeralSystem = %q, want \"\" (subagents never get the wire injection slot)", req.EphemeralSystem)
	}
	for _, m := range req.Messages {
		if strings.Contains(m.Content, "secret-skill") {
			t.Errorf("subagent request message (role %s) leaked the skills index: %q", m.Role, m.Content)
		}
		if strings.Contains(m.Content, "## Skills") {
			t.Errorf("subagent request message (role %s) leaked the skills index header: %q", m.Role, m.Content)
		}
	}
}

// TestContextReportShowsSkillsIndexLineOnlyWhenNonEmpty covers the /context
// zone-A integration: the skills legend row (glyphSkills, "skills") appears
// only when the note is non-empty, and headTokens() folds its token cost in.
func TestContextReportShowsSkillsIndexLineOnlyWhenNonEmpty(t *testing.T) {
	emptyDir := t.TempDir()
	cs := &CortexSession{Config: &Config{Skills: SkillsConfig{Dirs: []string{emptyDir}}}, Request: CortexArgs{}.Request()}
	if strings.Contains(cs.contextReport(), "skills") {
		t.Error("contextReport() should not mention \"skills\" when no skills are discovered")
	}
	if got := cs.headTokens(); got != cs.systemPromptTokens()+cs.outlineTokens()+cs.memoryIndexTokens() {
		t.Errorf("headTokens() = %d with no skills, want it to equal the sum without a skills contribution", got)
	}

	skillDir := t.TempDir()
	writeTestSkill(t, skillDir, "a-skill", "Does a thing.")
	cs2 := &CortexSession{Config: &Config{Skills: SkillsConfig{Dirs: []string{skillDir}}}, Request: CortexArgs{}.Request()}
	report := cs2.contextReport()
	if !strings.Contains(report, "skills") {
		t.Errorf("contextReport() = %q, want a \"skills\" legend row when a skill is discovered", report)
	}
	if !strings.Contains(report, "1 skills") {
		t.Errorf("contextReport() = %q, want the skill count (1 skills) in the detail column", report)
	}
	if got, want := cs2.headTokens(), cs2.systemPromptTokens()+cs2.outlineTokens()+cs2.memoryIndexTokens()+cs2.skillsIndexTokens(); got != want {
		t.Errorf("headTokens() = %d, want %d (skills index token cost folded in)", got, want)
	}
}
