// skills_test.go covers the Agent Skills index injection (skills.go): note
// rendering (empty/populated/capped), the coder-only guarantee (subagent
// seeds must never see it), and /context surfacing. internal/skills'
// discovery+parsing behavior itself is covered by internal/skills/skills_test.go;
// this file exercises only the cmd/cortex-side wiring on top of it.
package main

import (
	"context"
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

// TestSkillsIndexNoteShowsDefaultSkillWhenNoFilesystemSkills covers the
// built-in default skill: when no filesystem skills are discovered, the
// note is NOT empty — it renders only the default skill's line.
func TestSkillsIndexNoteShowsDefaultSkillWhenNoFilesystemSkills(t *testing.T) {
	dir := t.TempDir()
	cs := &CortexSession{Config: &Config{Skills: SkillsConfig{Dirs: []string{dir}}}}
	note := cs.skillsIndexNote()
	if note == "" {
		t.Fatal("skillsIndexNote() is empty, want the default skill's line")
	}
	if !strings.Contains(note, defaultConfigSkillName) {
		t.Errorf("skillsIndexNote() = %q, want the default skill name %q", note, defaultConfigSkillName)
	}
	if !strings.Contains(note, defaultConfigSkillPath) {
		t.Errorf("skillsIndexNote() = %q, want the default skill path %q", note, defaultConfigSkillPath)
	}
	// Exactly one skill line (the default): no other "- " entries.
	if n := strings.Count(note, "\n- "); n != 1 {
		t.Errorf("skillsIndexNote() has %d skill lines, want 1 (the default only)", n)
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
// 2-skill example: the two filesystem skills PLUS the built-in default
// skill (3 lines total).
func TestSkillsIndexNoteRendersTwoSkills(t *testing.T) {
	dir := t.TempDir()
	writeTestSkill(t, dir, "pdf-tools", "Extract text and tables from PDFs.")
	writeTestSkill(t, dir, "web-search", "Search the web for current information.")
	cs := &CortexSession{Config: &Config{Skills: SkillsConfig{Dirs: []string{dir}}}}

	note := cs.skillsIndexNote()
	if note == "" {
		t.Fatal("skillsIndexNote() is empty, want a rendered 3-skill index")
	}
	wantLines := []string{
		"## Skills",
		"read_file",
		"- pdf-tools: Extract text and tables from PDFs. (SKILL.md: " + filepath.Join(dir, "pdf-tools", "SKILL.md") + ")",
		"- web-search: Search the web for current information. (SKILL.md: " + filepath.Join(dir, "web-search", "SKILL.md") + ")",
		"- " + defaultConfigSkillName + ": " + defaultConfigSkillDescription + " (SKILL.md: " + defaultConfigSkillPath + ")",
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

// TestSkillsIndexNoteDefaultSkillShadowedByFilesystemSkill covers the
// shadow rule: a filesystem skill named "writing-config" replaces the
// built-in rather than co-existing with it.
func TestSkillsIndexNoteDefaultSkillShadowedByFilesystemSkill(t *testing.T) {
	dir := t.TempDir()
	writeTestSkill(t, dir, "writing-config", "User's own config playbook.")
	cs := &CortexSession{Config: &Config{Skills: SkillsConfig{Dirs: []string{dir}}}}

	note := cs.skillsIndexNote()
	if !strings.Contains(note, "User's own config playbook.") {
		t.Errorf("skillsIndexNote() = %q, want the user's writing-config skill", note)
	}
	// The built-in's virtual path must NOT appear — the user's skill
	// shadowed it.
	if strings.Contains(note, defaultConfigSkillPath) {
		t.Errorf("skillsIndexNote() = %q, should not contain the built-in's virtual path (shadowed)", note)
	}
}

// TestSkillsIndexNoteCap covers the skills.index_max cap: over-cap discovery
// truncates to the first N (by discovery order) and appends an omission
// note. The default skill is appended AFTER filesystem skills, so with
// IndexMax=2 and 3 filesystem skills the default is also past the cap.
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
	// 2 filesystem skills shown + 1 filesystem + 1 default = 2 more omitted.
	if !strings.Contains(note, "2 more skill") {
		t.Errorf("skillsIndexNote() = %q, want a line noting 2 skills were omitted (skill-c + default)", note)
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

// TestContextReportShowsSkillsIndexLineAlways covers the /context zone-A
// integration: the skills legend row (glyphSkills, "skills") appears
// whenever the note is non-empty — the default skill makes it non-empty in
// every case — and headTokens() folds its token cost in.
func TestContextReportShowsSkillsIndexLineAlways(t *testing.T) {
	// Default skill only (no filesystem skills): the note is non-empty
	// (the default line), so the legend row appears and the count is 1.
	emptyDir := t.TempDir()
	cs := &CortexSession{Config: &Config{Skills: SkillsConfig{Dirs: []string{emptyDir}}}, Request: CortexArgs{}.Request()}
	if !strings.Contains(cs.contextReport(), "skills") {
		t.Error("contextReport() should mention \"skills\" (the default skill makes the note non-empty)")
	}
	if !strings.Contains(cs.contextReport(), "1 skills") {
		t.Errorf("contextReport() = %q, want the skill count (1 skills) for the default alone", cs.contextReport())
	}
	if got, want := cs.headTokens(), cs.systemPromptTokens()+cs.outlineTokens()+cs.memoryIndexTokens()+cs.skillsIndexTokens()+cs.workspaceTokens(); got != want {
		t.Errorf("headTokens() = %d, want %d (skills index token cost folded in)", got, want)
	}

	// One filesystem skill + the default: count is 2.
	skillDir := t.TempDir()
	writeTestSkill(t, skillDir, "a-skill", "Does a thing.")
	cs2 := &CortexSession{Config: &Config{Skills: SkillsConfig{Dirs: []string{skillDir}}}, Request: CortexArgs{}.Request()}
	report := cs2.contextReport()
	if !strings.Contains(report, "skills") {
		t.Errorf("contextReport() = %q, want a \"skills\" legend row when a skill is discovered", report)
	}
	if !strings.Contains(report, "2 skills") {
		t.Errorf("contextReport() = %q, want the skill count (2 skills: 1 filesystem + 1 default)", report)
	}
	if got, want := cs2.headTokens(), cs2.systemPromptTokens()+cs2.outlineTokens()+cs2.memoryIndexTokens()+cs2.skillsIndexTokens()+cs2.workspaceTokens(); got != want {
		t.Errorf("headTokens() = %d, want %d (skills index token cost folded in)", got, want)
	}
}

// TestDefaultConfigSkillFor returns the embedded SKILL.md text for the
// default skill's virtual path and "" for anything else.
func TestDefaultConfigSkillFor(t *testing.T) {
	got := defaultConfigSkillFor(defaultConfigSkillPath)
	if got == "" {
		t.Fatal("defaultConfigSkillFor(defaultConfigSkillPath) is empty, want the embedded SKILL.md text")
	}
	if !strings.Contains(got, "name: writing-config") {
		t.Errorf("defaultConfigSkillFor = %q, want frontmatter with name: writing-config", got)
	}
	if !strings.Contains(got, "# Writing Cortex config") {
		t.Errorf("defaultConfigSkillFor = %q, want the skill's body heading", got)
	}
	// A non-default path returns "".
	if got := defaultConfigSkillFor("some/other/path"); got != "" {
		t.Errorf("defaultConfigSkillFor(%q) = %q, want \"\"", "some/other/path", got)
	}
}

// TestDefaultConfigSkillServedByReadFile covers the read_file intercept:
// a read_file of the default skill's virtual path returns the embedded
// SKILL.md text (not a not-found error), and a read_file of a different
// path is unaffected.
func TestDefaultConfigSkillServedByReadFile(t *testing.T) {
	cs := &CortexSession{Config: &Config{Skills: SkillsConfig{Dirs: []string{t.TempDir()}}}}

	// The virtual path is served from the binary.
	tc := tools.ToolCall{Function: tools.FunctionCall{
		Name:      tools.FunctionReadFile,
		Arguments: `{"path":"` + defaultConfigSkillPath + `"}`,
	}}
	out, _, err := tools.Execute(context.Background(), tc, cs)
	if err != nil {
		t.Fatalf("Execute(read_file %q) = error %v, want the embedded text", defaultConfigSkillPath, err)
	}
	if !strings.Contains(out, "name: writing-config") {
		t.Errorf("read_file(%q) = %q, want the embedded SKILL.md frontmatter", defaultConfigSkillPath, out)
	}

	// A different path is unaffected (not the default skill).
	tc2 := tools.ToolCall{Function: tools.FunctionCall{
		Name:      tools.FunctionReadFile,
		Arguments: `{"path":"not-the-default-skill.md"}`,
	}}
	_, _, err2 := tools.Execute(context.Background(), tc2, cs)
	// Should be a not-found error (the path doesn't exist on disk).
	if err2 == nil {
		t.Error("read_file(non-default path) should return a not-found error")
	}
}

// TestDefaultConfigSkillRangedRead returns the whole text plus a note
// that line ranges are meaningless over an embedded skill.
func TestDefaultConfigSkillRangedRead(t *testing.T) {
	cs := &CortexSession{Config: &Config{Skills: SkillsConfig{Dirs: []string{t.TempDir()}}}}

	tc := tools.ToolCall{Function: tools.FunctionCall{
		Name:      tools.FunctionReadFile,
		Arguments: `{"path":"` + defaultConfigSkillPath + `","start":5}`,
	}}
	out, _, err := tools.Execute(context.Background(), tc, cs)
	if err != nil {
		t.Fatalf("Execute(ranged read_file %q) = error %v", defaultConfigSkillPath, err)
	}
	if !strings.Contains(out, "name: writing-config") {
		t.Errorf("ranged read_file = %q, want the full embedded text", out)
	}
	if !strings.Contains(out, "line ranges are meaningless") {
		t.Errorf("ranged read_file = %q, want a note that ranges are meaningless", out)
	}
}

// TestDefaultConfigSkillSatisfiedOnCortexSession verifies that
// *CortexSession satisfies the tools.DefaultConfigSkiller interface, so
// readFile (internal/tools) can serve the embedded text via the dynamic
// type assertion.
func TestDefaultConfigSkillSatisfiedOnCortexSession(t *testing.T) {
	var _ tools.DefaultConfigSkiller = (*CortexSession)(nil)
}
