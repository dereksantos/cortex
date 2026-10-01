package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// resetPrompt restores the package-level prompt state configurePrompt mutates,
// so tests can't leak a configured prompt into each other.
func resetPrompt(t *testing.T) {
	t.Helper()
	base, appendix, attribution := promptBase, promptAppend, promptAttribution
	t.Cleanup(func() {
		promptBase, promptAppend, promptAttribution = base, appendix, attribution
	})
}

func TestSystemPromptContentDefault(t *testing.T) {
	resetPrompt(t)
	configurePrompt(nil)

	if got := systemPromptContent("", ""); got != SystemPrompt {
		t.Errorf("nil config must yield the built-in prompt verbatim; got %d bytes, want %d", len(got), len(SystemPrompt))
	}
	got := systemPromptContent("AGENTS.md", "do the thing")
	if !strings.HasPrefix(got, SystemPrompt) {
		t.Error("instructions must ride after the base prompt, not replace it")
	}
	if !strings.Contains(got, agentsMarkerPrefix+"AGENTS.md)\n\ndo the thing") {
		t.Error("the instructions body must follow the agentsMarker separator naming the loaded file")
	}
}

func TestConfigurePromptFileReplacesBase(t *testing.T) {
	resetPrompt(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "prompt.md")
	if err := os.WriteFile(path, []byte("You are a custom agent.\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	configurePrompt(&Config{Prompt: PromptConfig{File: path}})
	got := systemPromptContent("", "")
	if got != "You are a custom agent." {
		t.Errorf("prompt.file must replace the built-in base (trimmed); got %q", got)
	}
}

func TestConfigurePromptFileMissingFallsBack(t *testing.T) {
	resetPrompt(t)
	configurePrompt(&Config{Prompt: PromptConfig{File: filepath.Join(t.TempDir(), "nope.md")}})
	if got := systemPromptContent("", ""); got != SystemPrompt {
		t.Error("a missing prompt.file must fall back to the built-in prompt")
	}
}

func TestConfigurePromptFileEmptyFallsBack(t *testing.T) {
	resetPrompt(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "empty.md")
	if err := os.WriteFile(path, []byte("  \n\t\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	configurePrompt(&Config{Prompt: PromptConfig{File: path}})
	if got := systemPromptContent("", ""); got != SystemPrompt {
		t.Error("a whitespace-only prompt.file must fall back to the built-in prompt")
	}
}

func TestConfigurePromptFileRelativeFindsUp(t *testing.T) {
	resetPrompt(t)
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".cortex"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".cortex", "prompt.md"), []byte("from the repo root"), 0o644); err != nil {
		t.Fatal(err)
	}
	nested := filepath.Join(root, "a", "b")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(nested)

	configurePrompt(&Config{Prompt: PromptConfig{File: filepath.Join(".cortex", "prompt.md")}})
	if got := systemPromptContent("", ""); got != "from the repo root" {
		t.Errorf("a relative prompt.file must resolve upward like AGENTS.md/config.json; got %q", got)
	}
}

func TestConfigurePromptFileTruncatesAtCap(t *testing.T) {
	resetPrompt(t)
	oldCap := instructionBytesCap
	instructionBytesCap = 32
	t.Cleanup(func() { instructionBytesCap = oldCap })

	dir := t.TempDir()
	path := filepath.Join(dir, "big.md")
	if err := os.WriteFile(path, []byte(strings.Repeat("x", 100)), 0o644); err != nil {
		t.Fatal(err)
	}
	configurePrompt(&Config{Prompt: PromptConfig{File: path}})
	got := systemPromptContent("", "")
	if !strings.HasSuffix(got, "[prompt truncated]") {
		t.Errorf("an over-cap prompt.file must be truncated with a marker; got %q", got)
	}
	if len(got) > 32+len("\n...[prompt truncated]") {
		t.Errorf("truncated prompt exceeds the cap: %d bytes", len(got))
	}
}

func TestConfigurePromptAppend(t *testing.T) {
	resetPrompt(t)
	configurePrompt(&Config{Prompt: PromptConfig{Append: "Always answer in haiku."}})

	got := systemPromptContent("AGENTS.md", "agents body")
	base := strings.SplitN(got, agentsMarkerPrefix, 2)[0]
	if !strings.HasPrefix(base, SystemPrompt) {
		t.Error("append must extend the base prompt, not replace it")
	}
	if !strings.Contains(base, "Always answer in haiku.") {
		t.Error("prompt.append must appear in the system section")
	}
	if strings.Index(got, "Always answer in haiku.") > strings.Index(got, agentsMarkerPrefix) {
		t.Error("prompt.append must ride BEFORE the AGENTS.md section")
	}
}

func TestConfigurePromptFileAndAppendCompose(t *testing.T) {
	resetPrompt(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "prompt.md")
	if err := os.WriteFile(path, []byte("Custom base."), 0o644); err != nil {
		t.Fatal(err)
	}
	configurePrompt(&Config{Prompt: PromptConfig{File: path, Append: "Extra rule."}})
	got := systemPromptContent("", "")
	if !strings.HasPrefix(got, "Custom base.") || !strings.Contains(got, "Extra rule.") {
		t.Errorf("file+append must compose (file replaces base, append follows); got %q", got)
	}
}

func TestMergePromptConfig(t *testing.T) {
	tests := []struct {
		name       string
		user, proj PromptConfig
		want       PromptConfig
	}{
		{"project overrides user", PromptConfig{File: "u.md", Append: "user"}, PromptConfig{File: "p.md", Append: "proj"}, PromptConfig{File: "p.md", Append: "proj"}},
		{"unset project field keeps user", PromptConfig{File: "u.md", Append: "user"}, PromptConfig{}, PromptConfig{File: "u.md", Append: "user"}},
		{"fields merge independently", PromptConfig{File: "u.md"}, PromptConfig{Append: "proj"}, PromptConfig{File: "u.md", Append: "proj"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := mergeConfig(&Config{Prompt: tt.user}, &Config{Prompt: tt.proj}).Prompt
			if got != tt.want {
				t.Errorf("got %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestMemorySectionSplit(t *testing.T) {
	// The base prompt keeps one short memory line but not the full section.
	if !strings.Contains(SystemPrompt, "You have a persistent memory") {
		t.Error("base prompt must keep the short memory line")
	}
	if strings.Contains(SystemPrompt, "memory_search") {
		t.Error("base prompt must NOT contain the full memory section (should be in memoryPromptSection)")
	}
	// memoryPromptSection carries the guidance the base prompt drops.
	if !strings.Contains(memoryPromptSection, "memory_search") {
		t.Error("memoryPromptSection must carry the memory guidance")
	}
	if !strings.Contains(memoryPromptSection, "outline") {
		t.Error("memoryPromptSection must carry the outline/recall paragraph")
	}
	// The two together should cover everything the old SystemPrompt had.
	combined := SystemPrompt + memoryPromptSection
	for _, probe := range []string{"# Memory", "memory_read", "memory_write", "memory_forget", "@session/"} {
		if !strings.Contains(combined, probe) {
			t.Errorf("SystemPrompt + memoryPromptSection missing %q", probe)
		}
	}
}

func TestMemorySectionComposition(t *testing.T) {
	resetPrompt(t)
	configurePrompt(nil)

	got := systemPromptContent("", "")
	if got != SystemPrompt {
		t.Errorf("unconfigured prompt must yield the base prompt verbatim; got %d bytes, want %d", len(got), len(SystemPrompt))
	}
	if strings.Contains(got, "memory_search") {
		t.Error("the composed system prompt must not contain the full memory section — it is delivered per turn through the ephemeral slot")
	}
}

// TestMemorySectionForState table-tests the per-turn decision helper turn.go
// calls — not an inlined copy of it, so a change to turn.go's decision fails
// here. The outline arrives as the same present/absent condition turn.go
// uses for the outline block (live entries OR folded digest), the skills
// index is deliberately absent from the inputs (it must not switch the
// section on — its real-turn coverage is
// TestTurnMemorySectionSkillsOnlyDoesNotTrigger), and builtinBase says
// whether the session's base prompt is the built-in one: a prompt.file
// replacement suppresses the section even when notes or an outline exist.
func TestMemorySectionForState(t *testing.T) {
	tests := []struct {
		name           string
		outlinePresent bool
		builtinBase    bool
		note           string // the memory index, before the skills note is merged in
		want           bool
	}{
		{"notes absent, no outline", false, true, "", false},
		{"notes present, no outline", false, true, "## Project memory\n- my-note — a hook", true},
		{"no notes, outline present", true, true, "", true},
		{"notes present, outline present", true, true, "## Project memory\n- my-note — a hook", true},
		{"no notes, only the folded digest remains (live entries evicted)", true, true, "", true},
		{"notes present but the base prompt is a prompt.file replacement", false, false, "## Project memory\n- my-note — a hook", false},
		{"outline present but the base prompt is a prompt.file replacement", true, false, "", false},
		{"notes and outline but the base prompt is a prompt.file replacement", true, false, "## Project memory\n- my-note — a hook", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := memorySectionFor(tt.note, tt.outlinePresent, tt.builtinBase)
			if (got != "") != tt.want {
				t.Errorf("memorySectionFor = %q (present=%v), want present=%v", got, got != "", tt.want)
			}
			if got != "" && got != memoryPromptSection {
				t.Error("when present, the section must be the full memoryPromptSection verbatim")
			}
		})
	}
}

// The built-in prompt must encode the working-style preferences
// (2026-07-20, extended 2026-09-28 by issue #148): verify-first, clarify
// ambiguity with the user, delegation to subagents, simple communication,
// honest scoping, connect-it-through (done only when the path that needs the
// change reaches it — traced from the entry point), and docs updated with
// behavior changes. Keyword checks are deliberately loose — they pin that a
// principle survives future rewrites, not its exact wording.
func TestDefaultPromptEncodesWorkingStyle(t *testing.T) {
	lower := strings.ToLower(SystemPrompt)
	for _, principle := range []string{
		"test",        // verify-first: tests before the change
		"delegate",    // reasoning model handing bounded work to subagents
		"ask",         // clarify with the user when ambiguous
		"simple",      // simplicity in code and in replies
		"scope",       // scope + feasibility, optimistic but realistic
		"entry point", // connect it all the way through: trace from the entry point, not just the new unit
		"document",    // update the docs describing changed behavior in the same change
	} {
		if !strings.Contains(lower, principle) {
			t.Errorf("built-in prompt no longer mentions %q — a core working-style principle was dropped", principle)
		}
	}
}

// TestDefaultPromptEncodesTestIntegrity pins issue #141's principle: the
// built-in prompt must tell the model that removing or changing an existing
// test to make a build pass is a decision that has to be stated in the
// summary, not an implementation detail. The check is on the principle,
// not the exact wording, so a future rewrite can rephrase without breaking
// this test as long as the idea survives.
func TestDefaultPromptEncodesTestIntegrity(t *testing.T) {
	lower := strings.ToLower(SystemPrompt)
	for _, keyword := range []string{
		"test",    // the subject: tests
		"remov",   // the act: removing (covers "removing")
		"chang",   // the act: changing (covers "changing")
		"summary", // where it must be stated
		"state",   // the obligation: must be stated
	} {
		if !strings.Contains(lower, keyword) {
			t.Errorf("built-in prompt no longer encodes test-integrity principle (missing %q)", keyword)
		}
	}
}

// TestDefaultPromptEncodesDebugGuidance pins issue #154's debugging guidance
// in the built-in SystemPrompt: the prompt must tell the model to check every
// error in test/fixture setup with t.Fatal, confirm the fixture exists before
// suspecting the code under test, and debug with a focused test + t.Logf in
// the real package — never by copying production code into scratch modules or
// leaving DEBUG prints in shipped code. The check is on loose keywords (the
// principle, not the exact wording), so a future rewrite can rephrase without
// breaking this test as long as the idea survives — the same loose style as
// TestDefaultPromptEncodesTestIntegrity.
func TestDefaultPromptEncodesDebugGuidance(t *testing.T) {
	lower := strings.ToLower(SystemPrompt)
	for _, keyword := range []string{
		"t.Fatal", // check every error in test/fixture setup
		"fixture", // confirm the fixture exists first
		"t.Logf",  // debug with t.Logf in the real package
		"scratch", // never copy production code into scratch modules
		"debug",   // never leave DEBUG prints in shipped code
	} {
		if !strings.Contains(lower, strings.ToLower(keyword)) {
			t.Errorf("built-in prompt no longer encodes the debugging guidance (missing %q)", keyword)
		}
	}
}

// TestDefaultPromptEncodesDebugWorkingStyle pins the issue #154 principle's
// POSITION in the built-in prompt: the debugging working-style principle
// (debugWorkingStylePrinciple, spliced into SystemPrompt after "Test
// integrity") must sit in the "# How you work" block, before "# How you
// communicate". This is a position check, not a content check — the content
// is pinned by TestDefaultPromptEncodesDebugGuidance (loose keywords) and
// TestDebugPrincipleMirroredInClaudeMD (verbatim mirror in CLAUDE.md). A
// rewrite that moves the principle into a different section (or a different
// prompt slot) fails here, even if the wording survives.
func TestDefaultPromptEncodesDebugWorkingStyle(t *testing.T) {
	lower := strings.ToLower(SystemPrompt)
	for _, phrase := range []string{
		"t.Fatal", // check every error in test/fixture setup
		"fixture", // confirm the fixture exists first
		"t.Logf",  // debug with t.Logf in the real package
		"scratch", // never copy production code into scratch modules
		"debug",   // never leave DEBUG prints in shipped code
	} {
		if !strings.Contains(lower, strings.ToLower(phrase)) {
			t.Errorf("built-in prompt no longer encodes the debugging working-style principle (missing %q)", phrase)
		}
	}
	// The principle must live in the "# How you work" block (before "# How
	// you communicate"), matching its position after "Test integrity".
	if i := strings.Index(SystemPrompt, debugWorkingStylePrinciple); i < 0 {
		t.Fatal("the debugging working-style principle is not in the built-in prompt")
	} else if j := strings.Index(SystemPrompt, "# How you communicate"); j < i {
		t.Error("the debugging working-style principle must sit in the \"# How you work\" block, before \"# How you communicate\"")
	}
}

// TestDebugPrincipleMirroredInClaudeMD is the issue #154 consistency tripwire:
// CLAUDE.md's "Constraints → Testing" section must mirror the EXACT same
// debugging guidance the model actually receives in the built-in prompt
// (debugWorkingStylePrinciple) — the docs describe the guidance, so the two
// can't drift apart. It reads the file from the module root (the test's
// working directory is the package dir, cmd/cortex), so it passes wherever
// the checkout lives.
func TestDebugPrincipleMirroredInClaudeMD(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "CLAUDE.md"))
	if err != nil {
		t.Fatalf("cannot read CLAUDE.md (the mirrored guidance can't be verified): %v", err)
	}
	if !strings.Contains(string(data), debugWorkingStylePrinciple) {
		t.Error("CLAUDE.md's \"Constraints → Testing\" section no longer mirrors the built-in prompt's debugging working-style principle verbatim (debugWorkingStylePrinciple) — the docs and the prompt have drifted apart")
	}
}
