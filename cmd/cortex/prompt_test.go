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

// TestDefaultPromptEncodesVerifyBeforeFix pins the issue #178 principle in
// the built-in SystemPrompt: the prompt must tell the model to confirm a
// problem exists before fixing it, and that a reported problem that doesn't
// reproduce is finished by reporting so with the evidence — not by a fix for
// a problem that was never observed. The check is on the PRINCIPLE (the
// verify-before-fix idea and the honest no-repro outcome), not exact
// wording, so a future rewrite can rephrase without breaking this test as
// long as the idea survives — the same loose style as
// TestDefaultPromptEncodesTestIntegrity. It checks the built-in prompt
// specifically: every turn sees it (REPL, headless, plan mode, and the
// self-dev loop's ordinary step turns), so the principle lives here rather
// than only in plan-mode prompts.
func TestDefaultPromptEncodesVerifyBeforeFix(t *testing.T) {
	lower := strings.ToLower(SystemPrompt)
	for _, keyword := range []string{
		"confirm",   // verify-before-fix: confirm the problem exists before fixing it
		"reproduce", // the no-repro case: a reported problem that doesn't reproduce
		"evidence",  // the no-repro outcome must carry evidence
		"observed",  // a fix for a problem you haven't observed is not the finished result
	} {
		if !strings.Contains(lower, keyword) {
			t.Errorf("built-in prompt no longer encodes the verify-before-fix principle (missing %q)", keyword)
		}
	}
}

// TestVerifyBeforeFixPrincipleCarriedInEveryTurnPrompt pins issue #178's
// delivery: the built-in SystemPrompt carries verifyBeforeFixPrinciple
// VERBATIM, and the same const is restated in the planning instruction and
// every step prompt (plan_mode.go), so a loop-driven ordinary turn, a plan
// step, and the planning turn all see the identical principle text. The plan
// mode const is checked here (not in plan_mode_test.go) because the point is
// that the BASE prompt — which every turn, loop-driven or not, is seeded
// with — carries the principle, not only the plan-mode prompts.
func TestVerifyBeforeFixPrincipleCarriedInEveryTurnPrompt(t *testing.T) {
	if i := strings.Index(SystemPrompt, verifyBeforeFixPrinciple); i < 0 {
		t.Fatal("the built-in SystemPrompt does not carry the verify-before-fix principle verbatim")
	} else if j := strings.Index(SystemPrompt, "# How you communicate"); j < i {
		t.Error("the verify-before-fix principle must sit in the \"# How you work\" block (inside \"Verify first\"), before \"# How you communicate\"")
	}
	if !strings.Contains(planModeInstruction, verifyBeforeFixPrinciple) {
		t.Error("planModeInstruction must restate the verify-before-fix principle (same const) for the planning turn")
	}
	// The step prompt restates it for each step turn (tools present) too.
	if !strings.Contains(planStepPrompt("t", 1, 2, "s", nil), verifyBeforeFixPrinciple) {
		t.Error("planStepPrompt must restate the verify-before-fix principle (same const) for each step turn")
	}
	// The step prompt restates the issue #200 blocked-check principle the same
	// way: a step turn is where a blocked verification command actually lands,
	// and demotion at the turn boundaries can fold the planning turn out of
	// view — so the step prompt carries the standing principle, not just the
	// base system prompt.
	if !strings.Contains(planStepPrompt("t", 1, 2, "s", nil), blockedCheckPrinciple) {
		t.Error("planStepPrompt must restate the blocked-check principle (same const) for each step turn")
	}
	// The step prompt restates the issue #162 review-feedback principle the
	// same way: a review round's findings are applied on a step turn, and
	// demotion at the turn boundaries can fold the review out of view, so the
	// step prompt carries the standing principle — not only the base prompt.
	if !strings.Contains(planStepPrompt("t", 1, 2, "s", nil), reviewFeedbackPrinciple) {
		t.Error("planStepPrompt must restate the review-feedback principle (same const) for each step turn")
	}
	// The step prompt restates the issue #224 locate-before-writing principle
	// the same way: a step turn is where new code — mostly test files —
	// actually gets written, and demotion at the turn boundaries can fold the
	// earlier grep/outline work out of view, so the step prompt carries the
	// standing principle — not only the base prompt.
	if !strings.Contains(planStepPrompt("t", 1, 2, "s", nil), locateBeforeWritingPrinciple) {
		t.Error("planStepPrompt must restate the locate-before-writing principle (same const) for each step turn")
	}
	// The issue #225 principles ride the same way — in BOTH the planning
	// instruction (where a lower-level helper can be named in place of the
	// user-facing path) and each step prompt (where a failing acceptance
	// test actually gets bent): testTargetPrinciple names the surface a
	// criterion's test must call, and specTestPrinciple makes a failing
	// acceptance test a spec a step must not bend to pass.
	if !strings.Contains(planModeInstruction, testTargetPrinciple) {
		t.Error("planModeInstruction must restate the test-target principle (same const) for the planning turn")
	}
	if !strings.Contains(planStepPrompt("t", 1, 2, "s", nil), testTargetPrinciple) {
		t.Error("planStepPrompt must restate the test-target principle (same const) for each step turn")
	}
	if !strings.Contains(planModeInstruction, specTestPrinciple) {
		t.Error("planModeInstruction must restate the spec-test principle (same const) for the planning turn")
	}
	if !strings.Contains(planStepPrompt("t", 1, 2, "s", nil), specTestPrinciple) {
		t.Error("planStepPrompt must restate the spec-test principle (same const) for each step turn")
	}
	// The issue #231 comment-truth principle rides the same way — in BOTH the
	// planning instruction (where a comment or summary claim can be written
	// ahead of the behavior it describes) and each step prompt (where the
	// behavior actually changes and a stale comment or claim can outlive it):
	// commentsTruthPrinciple makes a comment and a summary/docs claim describe
	// the shipped code, not the intent.
	if !strings.Contains(planModeInstruction, commentsTruthPrinciple) {
		t.Error("planModeInstruction must restate the comment-truth principle (same const) for the planning turn")
	}
	if !strings.Contains(planStepPrompt("t", 1, 2, "s", nil), commentsTruthPrinciple) {
		t.Error("planStepPrompt must restate the comment-truth principle (same const) for each step turn")
	}
}

// TestDefaultPromptEncodesBlockedCheckGuidance pins issue #200's content:
// the built-in prompt must tell the model what to do the moment a check is
// blocked, refused, or declined — its result stays unknown (no guessing, a
// check of something else doesn't stand in for it), and failing a safe way to
// observe the same thing, the claim is marked unverified wherever it is
// stated. The 2026-10-04 PR #196/#197 reviews are the case this exists for:
// a blocked rune-count bash command was answered with hand-counted numbers
// written into comments and goldens, and a refused mutation run was replaced
// by probes of a different property — both stated as fact in commit
// summaries.
//
// The keyword assertions run against blockedCheckPrinciple ITSELF, and each
// keyword is additionally asserted ABSENT from the full prompt with the
// principle stripped — so every subtest actually rides on the new principle.
// (Keywords that occur elsewhere in the prompt can't be used for the
// absence check: the pre-#200 prompt already contained "guess" in "a wrong
// guess costs more", so asserting it against the full prompt alone would
// pass with the principle removed and pin nothing — "guess" is therefore
// checked in the const only, and the absence-checked keywords below are
// ones that occur ONLY in the principle.) The same loose, rewrite-tolerant
// style as TestDefaultPromptEncodesFailingTestGuidance.
func TestDefaultPromptEncodesBlockedCheckGuidance(t *testing.T) {
	// The full prompt with the principle removed: the splice site in
	// SystemPrompt is verifyBeforeFixPrinciple + " " + blockedCheckPrinciple
	// + " " — strip exactly the principle and one surrounding separator,
	// leaving the rest of the prompt intact.
	withoutPrinciple := strings.Replace(SystemPrompt,
		verifyBeforeFixPrinciple+" "+blockedCheckPrinciple,
		verifyBeforeFixPrinciple, 1)
	if withoutPrinciple == SystemPrompt {
		t.Fatal("could not locate the blocked-check principle's splice site in SystemPrompt — the removal below would be a no-op")
	}
	tests := []struct {
		keyword      string
		intent       string
		checkAbsence bool // false when the keyword also occurs elsewhere in the prompt
	}{
		{"blocked", "the trigger: a check that was blocked, refused, or declined", true},
		{"unknown", "the state: the result is still unknown", true},
		{"guess", "the ban: don't guess the unmeasured result", false}, // "a wrong guess costs more" is elsewhere in the prompt
		{"stand in", "the ban: a check of something else doesn't stand in for it", true},
		{"unverified", "the obligation: mark the unchecked claim unverified wherever it is stated", true},
	}
	for _, tt := range tests {
		t.Run(tt.keyword, func(t *testing.T) {
			if !strings.Contains(strings.ToLower(blockedCheckPrinciple), tt.keyword) {
				t.Errorf("blockedCheckPrinciple no longer encodes the blocked-check guidance (%s): missing %q", tt.intent, tt.keyword)
			}
			if tt.checkAbsence && strings.Contains(strings.ToLower(withoutPrinciple), tt.keyword) {
				t.Errorf("keyword %q survives with the principle removed — it does not ride on the new principle: %s", tt.keyword, tt.intent)
			}
		})
	}
}

// TestBlockedCheckPrinciplePosition pins where the issue #200 principle sits:
// in the "# How you work" block, inside the "Verify first" line right after
// verifyBeforeFixPrinciple (issue #178's) and before debugWorkingStylePrinciple
// (issue #154's) — a position check, not a content check, mirroring
// TestFailingTestPrinciplePosition. The placement matters: the model meets it
// at the exact moment a bash refusal could land, next to the verify-first
// principle it extends.
func TestBlockedCheckPrinciplePosition(t *testing.T) {
	i := strings.Index(SystemPrompt, blockedCheckPrinciple)
	if i < 0 {
		t.Fatal("the blocked-check principle is not in the built-in prompt")
	}
	if v := strings.Index(SystemPrompt, verifyBeforeFixPrinciple); v < 0 || i < v {
		t.Error("the blocked-check principle must sit after the verify-before-fix principle, in the same Verify-first line")
	}
	if d := strings.Index(SystemPrompt, debugWorkingStylePrinciple); d < i {
		t.Error("the blocked-check principle must sit before the debugging principle")
	}
	if j := strings.Index(SystemPrompt, "# How you communicate"); j < i {
		t.Error("the blocked-check principle must sit in the \"# How you work\" block, before \"# How you communicate\"")
	}
}

// TestBlockedCheckPrincipleMirroredInClaudeMD is the issue #200 consistency
// tripwire, mirroring TestFailingTestPrincipleMirroredInClaudeMD: CLAUDE.md's
// "Constraints → Testing" section must carry the EXACT text the model
// receives (blockedCheckPrinciple) so docs and prompt can't drift apart.
func TestBlockedCheckPrincipleMirroredInClaudeMD(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "CLAUDE.md"))
	if err != nil {
		t.Fatalf("cannot read CLAUDE.md (the mirrored guidance can't be verified): %v", err)
	}
	if !strings.Contains(string(data), blockedCheckPrinciple) {
		t.Error("CLAUDE.md's \"Constraints → Testing\" section no longer mirrors the built-in prompt's blocked-check principle verbatim (blockedCheckPrinciple) — the docs and the prompt have drifted apart")
	}
}

// TestDefaultPromptEncodesReviewFeedbackGuidance pins issue #162's content:
// the built-in prompt must tell the model how to account for review findings,
// and specifically the four ways the self-dev loop failed to (tick
// 20261001T013107Z: PR #158's docs placeholder surviving rounds 1–2 while
// every review named it, that PR's deferral note landing in
// docs/memory-tools.md instead of the commit message/summary asked for, PR
// #145 following BOTH options offered as alternatives so a complying model
// printed its summary twice, and PR #143 patching one more flag spelling each
// round instead of closing the class).
//
// The keyword assertions run against reviewFeedbackPrinciple ITSELF, and each
// absence-checked keyword is additionally asserted ABSENT from the full prompt
// with the principle stripped — so every subtest actually rides on the new
// principle rather than on text that was already there (the same
// rewrite-tolerant, absence-verified style as
// TestDefaultPromptEncodesBlockedCheckGuidance). Keywords that occur
// elsewhere in the prompt are checked in the const only, with checkAbsence
// false: "dropped" appears in tools' test-loss wording and "review" in "Test
// integrity", so pinning them against the whole prompt would pass with the
// principle removed and pin nothing.
func TestDefaultPromptEncodesReviewFeedbackGuidance(t *testing.T) {
	// The full prompt with the principle removed: its splice site is its own
	// paragraph, so strip exactly the paragraph plus one surrounding separator,
	// leaving the rest of the prompt intact.
	withoutPrinciple := strings.Replace(SystemPrompt,
		"\n\n"+reviewFeedbackPrinciple, "", 1)
	if withoutPrinciple == SystemPrompt {
		t.Fatal("could not locate the review-feedback principle's splice site in SystemPrompt — the removal below would be a no-op")
	}
	tests := []struct {
		keyword      string
		intent       string
		checkAbsence bool // false when the keyword also occurs elsewhere in the prompt
	}{
		// 1. Per-finding accounting: the whole point of the principle — a
		// finding with no disposition was dropped, invisibly to the next round.
		{"owed an explicit disposition", "every finding is owed an explicit disposition", true},
		{"addressed", "the first disposition", true},
		{"deferred with a reason", "the second disposition: deferral must be stated, not silent", true},
		{"disputed", "the third disposition: disagreeing openly beats ignoring", true},
		{"dropped", "no disposition means the finding was dropped", false}, // also in tools' test-loss wording
		{"invisible to the next round", "the cost is that the drop cannot be seen", false},
		// 2. Alternatives are a choice, not a list to satisfy in full (PR #145).
		{"alternatives", "the trigger: several options offered as alternatives", true},
		{"pick one and say which", "the ask: choose exactly one and name it", false},
		// 3. Example instances stand for a class (PR #143's one-spelling-per-round).
		{"underlying class", "the ask: fix the class, not the listed instances", true},
		{"instances", "the trigger: a reviewer listing example instances", false},
		// 4. Placement the reviewer named (PR #158's deferral note in a doc).
		{"put it there", "the ask: something goes where the reviewer said", true},
	}
	for _, tt := range tests {
		t.Run(tt.keyword, func(t *testing.T) {
			if !strings.Contains(strings.ToLower(reviewFeedbackPrinciple), tt.keyword) {
				t.Errorf("reviewFeedbackPrinciple no longer encodes the review-feedback guidance (%s): missing %q", tt.intent, tt.keyword)
			}
			if tt.checkAbsence && strings.Contains(strings.ToLower(withoutPrinciple), tt.keyword) {
				t.Errorf("keyword %q survives with the principle removed — it does not ride on the new principle: %s", tt.keyword, tt.intent)
			}
		})
	}
}

// TestReviewFeedbackPrinciplePosition pins where the issue #162 principle
// sits: in the "# How you work" block, as its own paragraph after
// failingTestPrinciple (issue #177's) and before
// debugWorkingStylePrinciple (issue #154's) — a position check, not a content
// check, mirroring TestBlockedCheckPrinciplePosition. The placement matters:
// the principle belongs with the accountability principles (test integrity,
// tests-as-evidence) that say state what you took away, and ahead of the
// working-style principles, so a model reading the block meets it with the
// review round in view.
func TestReviewFeedbackPrinciplePosition(t *testing.T) {
	i := strings.Index(SystemPrompt, reviewFeedbackPrinciple)
	if i < 0 {
		t.Fatal("the review-feedback principle is not in the built-in prompt")
	}
	if f := strings.Index(SystemPrompt, failingTestPrinciple); f < 0 || i < f {
		t.Error("the review-feedback principle must sit after the failing-test principle")
	}
	if d := strings.Index(SystemPrompt, debugWorkingStylePrinciple); d < i {
		t.Error("the review-feedback principle must sit before the debugging principle")
	}
	if j := strings.Index(SystemPrompt, "# How you communicate"); j < i {
		t.Error("the review-feedback principle must sit in the \"# How you work\" block, before \"# How you communicate\"")
	}
	// Its own paragraph, not spliced mid-line into another principle: the
	// strip in TestDefaultPromptEncodesReviewFeedbackGuidance assumes it.
	if n := strings.Count(SystemPrompt, "\n\n"+reviewFeedbackPrinciple+"\n\n"); n != 1 {
		t.Errorf("the review-feedback principle must be exactly one own paragraph in the built-in prompt, found %d", n)
	}
}

// TestReviewFeedbackPrincipleMirroredInClaudeMD is the issue #162 consistency
// tripwire, mirroring TestBlockedCheckPrincipleMirroredInClaudeMD: CLAUDE.md's
// "Constraints" section must carry the EXACT text the model receives
// (reviewFeedbackPrinciple) so docs and prompt can't drift apart. Beyond the
// contains check it pins the mirror as the doc's ONLY copy and as its own
// paragraph: a mirror embedded in another paragraph, or duplicated, reads as
// guidance the doc never intended to give twice.
func TestReviewFeedbackPrincipleMirroredInClaudeMD(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "CLAUDE.md"))
	if err != nil {
		t.Fatalf("cannot read CLAUDE.md (the mirrored guidance can't be verified): %v", err)
	}
	doc := string(data)
	if !strings.Contains(doc, reviewFeedbackPrinciple) {
		t.Error("CLAUDE.md's \"Constraints\" section no longer mirrors the built-in prompt's review-feedback principle verbatim (reviewFeedbackPrinciple) — the docs and the prompt have drifted apart")
	}
	if n := strings.Count(doc, reviewFeedbackPrinciple); n != 1 {
		t.Errorf("CLAUDE.md mirrors the review-feedback principle %d times, want exactly 1 (a duplicated mirror drifts one copy at a time)", n)
	}
	if !strings.Contains(doc, "\n\n"+reviewFeedbackPrinciple+"\n\n") {
		t.Error("CLAUDE.md's mirrored review-feedback principle must be its own paragraph (blank-line separated), not spliced into another")
	}
}

// TestSpecTestPrincipleMirroredInClaudeMD is the issue #225 consistency
// tripwire, mirroring TestReviewFeedbackPrincipleMirroredInClaudeMD: CLAUDE.md's
// "Constraints" section must carry the EXACT text the model receives
// (specTestPrinciple) so docs and prompt can't drift apart. Beyond the
// contains check it pins the mirror as the doc's ONLY copy and as its own
// paragraph: a mirror embedded in another paragraph, or duplicated, reads as
// guidance the doc never intended to give twice.
func TestSpecTestPrincipleMirroredInClaudeMD(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "CLAUDE.md"))
	if err != nil {
		t.Fatalf("cannot read CLAUDE.md (the mirrored guidance can't be verified): %v", err)
	}
	doc := string(data)
	if !strings.Contains(doc, specTestPrinciple) {
		t.Error("CLAUDE.md's \"Constraints\" section no longer mirrors the built-in prompt's spec-test principle verbatim (specTestPrinciple) — the docs and the prompt have drifted apart")
	}
	if n := strings.Count(doc, specTestPrinciple); n != 1 {
		t.Errorf("CLAUDE.md mirrors the spec-test principle %d times, want exactly 1 (a duplicated mirror drifts one copy at a time)", n)
	}
	if !strings.Contains(doc, "\n\n"+specTestPrinciple+"\n\n") {
		t.Error("CLAUDE.md's mirrored spec-test principle must be its own paragraph (blank-line separated), not spliced into another")
	}
}

// TestTestTargetPrincipleMirroredInClaudeMD is the issue #225 consistency
// tripwire, mirroring TestSpecTestPrincipleMirroredInClaudeMD: CLAUDE.md's
// "Constraints" section must carry the EXACT text the model receives
// (testTargetPrinciple) so docs and prompt can't drift apart. Beyond the
// contains check it pins the mirror as the doc's ONLY copy and as its own
// paragraph: a mirror embedded in another paragraph, or duplicated, reads as
// guidance the doc never intended to give twice.
func TestTestTargetPrincipleMirroredInClaudeMD(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "CLAUDE.md"))
	if err != nil {
		t.Fatalf("cannot read CLAUDE.md (the mirrored guidance can't be verified): %v", err)
	}
	doc := string(data)
	if !strings.Contains(doc, testTargetPrinciple) {
		t.Error("CLAUDE.md's \"Constraints\" section no longer mirrors the built-in prompt's test-target principle verbatim (testTargetPrinciple) — the docs and the prompt have drifted apart")
	}
	if n := strings.Count(doc, testTargetPrinciple); n != 1 {
		t.Errorf("CLAUDE.md mirrors the test-target principle %d times, want exactly 1 (a duplicated mirror drifts one copy at a time)", n)
	}
	if !strings.Contains(doc, "\n\n"+testTargetPrinciple+"\n\n") {
		t.Error("CLAUDE.md's mirrored test-target principle must be its own paragraph (blank-line separated), not spliced into another")
	}
}

// TestDefaultPromptEncodesSpecTestGuidance pins issue #225's content in the
// built-in prompt: a failing test written from the issue's acceptance criteria
// is a spec, so making it green by editing the test, its setup, or its
// expectation — instead of fixing the behavior the criterion names — is a
// deviation owed a report, not a fix (a green suite never counts as coverage
// of an unmet criterion; the defect must be fixed in production, never
// patched around in the test; a wrong criterion is disputed with evidence,
// not silently rewritten). The 2026-10-06 PR #223 reviews (issue #111) are the
// case this exists for: an acceptance test patched around a production defect
// (the stash fallback), the "undo restores created files" criterion rewritten
// to assert the opposite, and a prune length test "fixed" by making the code
// keep the wrong entries.
//
// The keyword assertions run against specTestPrinciple ITSELF, and each
// absence-checked keyword is additionally asserted ABSENT from the full prompt
// with the principle stripped — so every subtest actually rides on the new
// principle rather than on text that was already there (the same
// rewrite-tolerant, absence-verified style as
// TestDefaultPromptEncodesReviewFeedbackGuidance). Keywords that occur
// elsewhere in the prompt are checked in the const only, with checkAbsence
// false: "test", "summary", and "evidence" already appear in other
// principles.
func TestDefaultPromptEncodesSpecTestGuidance(t *testing.T) {
	// The full prompt with the principle removed: its splice site is its own
	// paragraph, so strip exactly the paragraph plus one surrounding
	// separator, leaving the rest of the prompt intact.
	withoutPrinciple := strings.Replace(SystemPrompt,
		"\n\n"+specTestPrinciple, "", 1)
	if withoutPrinciple == SystemPrompt {
		t.Fatal("could not locate the spec-test principle's splice site in SystemPrompt — the removal below would be a no-op")
	}
	tests := []struct {
		keyword      string
		intent       string
		checkAbsence bool // false when the keyword also occurs elsewhere in the prompt
	}{
		// The trigger: a test written from the issue's acceptance criteria.
		{"acceptance criteria", "the spec a new test may be written from", true},
		// The rule: a failing test written from the criterion is a spec.
		{"spec", "the failing acceptance test is a specification", false}, // "specification" is in failingTestPrinciple
		// The act: editing the test, its setup, or its expectation.
		{"setup", "editing the test's setup counts as bending it", false},             // "fixture setup" is in debugWorkingStylePrinciple
		{"expectation", "editing the test's expectation counts as bending it", false}, // "Rewriting an expectation" is in failingTestPrinciple
		// The trigger state: making it green.
		{"green", "making the failing test green", false}, // "green build" is in the Test integrity paragraph
		// The verdict: a deviation to report, never a fix.
		{"deviation you must report", "bending the test is a deviation you must report", true},
		// The obligation: say it in the summary.
		{"summary", "the deviation must be reported in the summary", false}, // "summary" is in Test integrity
		// The unmet criterion: green does not cover.
		{"coverage", "a green suite is not coverage of an unmet criterion", true},
		// The production side: the defect is fixed in production, not the test.
		{"production", "the defect must be fixed in production", false}, // "production code" is in debugWorkingStylePrinciple
		{"patched around", "never patch a production defect around in the test", true},
		// A wrong criterion is disputed with evidence, not rewritten.
		{"silently rewriting", "a wrong criterion is not silently rewritten", true},
	}
	for _, tt := range tests {
		t.Run(tt.keyword, func(t *testing.T) {
			if !strings.Contains(strings.ToLower(specTestPrinciple), tt.keyword) {
				t.Errorf("specTestPrinciple no longer encodes the spec-test guidance (%s): missing %q", tt.intent, tt.keyword)
			}
			if tt.checkAbsence && strings.Contains(strings.ToLower(withoutPrinciple), tt.keyword) {
				t.Errorf("keyword %q survives with the principle removed — it does not ride on the new principle: %s", tt.keyword, tt.intent)
			}
		})
	}
}

// TestDefaultPromptEncodesTestTargetGuidance pins issue #225's test-target
// content in the built-in prompt: the model must test the user-facing surface
// the requirement names — the command, the API, the entry point the user
// invokes — never a lower-level helper that surface routes through, because a
// test that never calls the surface proves nothing about it. The 2026-10-06
// PR #223 reviews (issue #111) are the case this exists for: a step that
// tested checkpoint.Restore directly instead of cs.undo, so the wiring bugs
// went unnoticed — the tested unit passed while the user-facing path it
// serves stayed broken.
//
// The keyword assertions run against testTargetPrinciple ITSELF, and each
// absence-checked keyword is additionally asserted ABSENT from the full prompt
// with the principle stripped — so every subtest actually rides on the new
// principle rather than on text that was already there (the same
// rewrite-tolerant, absence-verified style as
// TestDefaultPromptEncodesSpecTestGuidance). Keywords that occur
// elsewhere in the prompt are checked in the const only, with checkAbsence
// false: "surface" and "green" already appear in other principles, and
// "requirement" in "When a requirement is ambiguous".
func TestDefaultPromptEncodesTestTargetGuidance(t *testing.T) {
	// The full prompt with the principle removed: its splice site is inside
	// the "Test integrity" paragraph, so strip exactly the principle and one
	// surrounding separator, leaving the rest of the prompt intact.
	withoutPrinciple := strings.Replace(SystemPrompt,
		"Test integrity. "+testTargetPrinciple+" ",
		"Test integrity. ", 1)
	if withoutPrinciple == SystemPrompt {
		t.Fatal("could not locate the test-target principle's splice site in SystemPrompt — the removal below would be a no-op")
	}
	tests := []struct {
		keyword      string
		intent       string
		checkAbsence bool // false when the keyword also occurs elsewhere in the prompt
	}{
		// The subject: the path the requirement names.
		{"requirement names", "the test target is named by the requirement", true},
		// The target: the user-facing surface the criterion describes.
		{"user-facing", "the criterion's surface is the one the user faces", true},
		{"surface", "the user-facing surface a test must call", false}, // also in other principles
		// The ban: a lower-level helper the surface routes through.
		{"lower-level helper", "a test going through a lower-level helper", true},
		// The consequence: a test that never calls the surface proves nothing.
		{"never calls the surface", "a test that never calls the surface proves nothing about it", true},
	}
	for _, tt := range tests {
		t.Run(tt.keyword, func(t *testing.T) {
			if !strings.Contains(strings.ToLower(testTargetPrinciple), tt.keyword) {
				t.Errorf("testTargetPrinciple no longer encodes the test-target guidance (%s): missing %q", tt.intent, tt.keyword)
			}
			if tt.checkAbsence && strings.Contains(strings.ToLower(withoutPrinciple), tt.keyword) {
				t.Errorf("keyword %q survives with the principle removed — it does not ride on the new principle: %s", tt.keyword, tt.intent)
			}
		})
	}
}

// TestSpecTestPrinciplePosition pins where the issue #225 spec-test principle
// sits: in the "# How you work" block, as its own paragraph after
// reviewFeedbackPrinciple (issue #162's) and before
// debugWorkingStylePrinciple (issue #154's) — a position check, not a content
// check, mirroring TestReviewFeedbackPrinciplePosition. The placement matters:
// the principle belongs with the accountability principles (test integrity,
// tests-as-evidence, review feedback) that say what you owe the spec and the
// reviewer, and ahead of the working-style principles.
func TestSpecTestPrinciplePosition(t *testing.T) {
	i := strings.Index(SystemPrompt, specTestPrinciple)
	if i < 0 {
		t.Fatal("the spec-test principle is not in the built-in prompt")
	}
	if r := strings.Index(SystemPrompt, reviewFeedbackPrinciple); r < 0 || i < r {
		t.Error("the spec-test principle must sit after the review-feedback principle")
	}
	if d := strings.Index(SystemPrompt, debugWorkingStylePrinciple); d < i {
		t.Error("the spec-test principle must sit before the debugging principle")
	}
	if j := strings.Index(SystemPrompt, "# How you communicate"); j < i {
		t.Error("the spec-test principle must sit in the \"# How you work\" block, before \"# How you communicate\"")
	}
	// Its own paragraph, not spliced mid-line into another principle: the
	// strip in TestDefaultPromptEncodesSpecTestGuidance assumes it.
	if n := strings.Count(SystemPrompt, "\n\n"+specTestPrinciple+"\n\n"); n != 1 {
		t.Errorf("the spec-test principle must be exactly one own paragraph in the built-in prompt, found %d", n)
	}
}

// TestDefaultPromptEncodesCommentsTruthGuidance pins issue #231's content in
// the built-in prompt: a comment must say what the code does, not what was
// intended — a comment the code doesn't implement is worse than no comment
// (it misleads the next reader, who is often the agent in a later session);
// after a behavioral change the comments adjacent to the edited span must be
// re-read and fixed or deleted (reordered calls, a new overwrite, a removed
// guard all stale them); and a summary or docs claim describing behavior is
// the same class — it must match the code shipped, and claiming behavior the
// code doesn't implement is a deviation owed the same report as a test bent
// to pass. The 2026-10-06 tick reviews (filed in #231) are the case this
// exists for: PR #222's preflight.go comment claiming a substituted pick's
// verdict comes from the catalog while the call order it chose makes that
// false; PR #226's comments and summary claiming gating the code does not
// implement; PR #229's hand-assembled test commented "Production ordering
// (loop.go)"; PR #227's "removed the guard" claim with the remaining filters
// still in place.
//
// The keyword assertions run against commentsTruthPrinciple ITSELF, and every
// absence-checked keyword is additionally asserted ABSENT from the full prompt
// with the principle stripped — so every subtest actually rides on the new
// principle rather than on text that was already there (the same
// rewrite-tolerant, absence-verified style as
// TestDefaultPromptEncodesSpecTestGuidance). Every keyword below was probed
// against the pre-#231 prompt and occurs ONLY in the principle — "comment" and
// "summary" deliberately aren't: they already appear in reviewFeedbackPrinciple
// and the test-integrity clause, so pinning them against the whole prompt would
// pass with the principle removed and pin nothing.
func TestDefaultPromptEncodesCommentsTruthGuidance(t *testing.T) {
	// The full prompt with the principle removed: its splice site is its own
	// paragraph, so strip exactly the paragraph plus one surrounding
	// separator, leaving the rest of the prompt intact.
	withoutPrinciple := strings.Replace(SystemPrompt,
		"\n\n"+commentsTruthPrinciple, "", 1)
	if withoutPrinciple == SystemPrompt {
		t.Fatal("could not locate the comment-truth principle's splice site in SystemPrompt — the removal below would be a no-op")
	}
	tests := []struct {
		keyword      string
		intent       string
		checkAbsence bool // false when the keyword also occurs elsewhere in the prompt
	}{
		// The rule: the comment states behavior, not intent.
		{"what the code does", "the comment must say what the code does", true},
		{"intended", "the banned content: what was intended", true},
		// The cost: a wrong comment misleads the next reader.
		{"worse than no comment", "a comment the code doesn't implement is worse than none", true},
		{"next reader", "the victim: the next reader of the code", true},
		{"later session", "the next reader is often the agent in a later session", true},
		// The trigger: a behavioral change stales the adjacent comments.
		{"behavioral change", "the trigger: the change that stales comments", true},
		{"re-read the comments", "the ask: re-read the adjacent comments after the change", true},
		{"no longer hold", "the verdict: a comment that no longer holds is fixed or deleted", true},
		// The extension: summaries and docs claims are the same class.
		{"docs claim", "the same class: a docs claim describing behavior", true},
		{"match the code you shipped", "the bar: the claim must match the shipped code", true},
	}
	for _, tt := range tests {
		t.Run(tt.keyword, func(t *testing.T) {
			if !strings.Contains(strings.ToLower(commentsTruthPrinciple), tt.keyword) {
				t.Errorf("commentsTruthPrinciple no longer encodes the comment-truth guidance (%s): missing %q", tt.intent, tt.keyword)
			}
			if tt.checkAbsence && strings.Contains(strings.ToLower(withoutPrinciple), tt.keyword) {
				t.Errorf("keyword %q survives with the principle removed — it does not ride on the new principle: %s", tt.keyword, tt.intent)
			}
		})
	}
}

// TestCommentsTruthPrinciplePosition pins where the issue #231 comment-truth
// principle sits: in the "# How you work" block, as its own paragraph after
// specTestPrinciple (issue #225's) and before
// debugWorkingStylePrinciple (issue #154's) — a position check, not a content
// check, mirroring TestSpecTestPrinciplePosition. The placement matters: the
// principle belongs with the accountability principles (test integrity,
// tests-as-evidence, review feedback, the spec) that say what a change owes
// its readers, and ahead of the working-style principles.
func TestCommentsTruthPrinciplePosition(t *testing.T) {
	// The splice probe looks for a line-starting copy of the const — the
	// const's own doc comment in prompt.go names the text mid-line, and a
	// line-starting match is the shape only a spliced paragraph has.
	c := strings.Index(SystemPrompt, "\n\n"+commentsTruthPrinciple)
	if c < 0 {
		t.Fatal("the comment-truth principle is not in the built-in prompt")
	}
	if s := strings.Index(SystemPrompt, specTestPrinciple); s < 0 || c < s {
		t.Error("the comment-truth principle must sit after the spec-test principle")
	}
	if d := strings.Index(SystemPrompt, debugWorkingStylePrinciple); d < c {
		t.Error("the comment-truth principle must sit before the debugging principle")
	}
	if j := strings.Index(SystemPrompt, "# How you communicate"); j < c {
		t.Error("the comment-truth principle must sit in the \"# How you work\" block, before \"# How you communicate\"")
	}
	// Its own paragraph, not spliced mid-line into another principle: the
	// strip in TestDefaultPromptEncodesCommentsTruthGuidance assumes it.
	if n := strings.Count(SystemPrompt, "\n\n"+commentsTruthPrinciple+"\n\n"); n != 1 {
		t.Errorf("the comment-truth principle must be exactly one own paragraph in the built-in prompt, found %d", n)
	}
}

// TestCommentsTruthPrincipleRidesInEveryTurnPrompt pins issue #231's
// delivery: the built-in SystemPrompt carries commentsTruthPrinciple VERBATIM
// as its own paragraph, so EVERY turn sees it — REPL turns, headless turns,
// plan-mode turns (the same delivery reasoning as
// reviewFeedbackPrinciple and specTestPrinciple). The plan-mode ride itself is
// asserted by TestVerifyBeforeFixPrincipleCarriedInEveryTurnPrompt (the
// commentsTruthPrinciple assertions there) and
// TestPlanStepPromptCarriesTaskAndPrinciple.
func TestCommentsTruthPrincipleRidesInEveryTurnPrompt(t *testing.T) {
	// Exactly one copy, verbatim, and as its own paragraph (a duplicated or
	// paraphrased ride would pass a bare contains and pin nothing).
	if c := strings.Count(SystemPrompt, commentsTruthPrinciple); c != 1 {
		t.Errorf("the comment-truth principle must appear exactly once verbatim in the built-in prompt, found %d", c)
	}
	if n := strings.Count(SystemPrompt, "\n\n"+commentsTruthPrinciple+"\n\n"); n != 1 {
		t.Errorf("the comment-truth principle must ride as exactly one own paragraph in the built-in prompt, found %d", n)
	}
}

// TestCommentsTruthPrincipleMirroredInClaudeMD is the issue #231 consistency
// tripwire, mirroring TestSpecTestPrincipleMirroredInClaudeMD: CLAUDE.md's
// "Constraints" section must carry the EXACT text the model receives
// (commentsTruthPrinciple) so docs and prompt can't drift apart. Beyond the
// contains check it pins the mirror as the doc's ONLY copy and as its own
// paragraph: a mirror embedded in another paragraph, or duplicated, reads as
// guidance the doc never intended to give twice.
func TestCommentsTruthPrincipleMirroredInClaudeMD(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "CLAUDE.md"))
	if err != nil {
		t.Fatalf("cannot read CLAUDE.md (the mirrored guidance can't be verified): %v", err)
	}
	doc := string(data)
	if !strings.Contains(doc, commentsTruthPrinciple) {
		t.Error("CLAUDE.md's \"Constraints\" section no longer mirrors the built-in prompt's comment-truth principle verbatim (commentsTruthPrinciple) — the docs and the prompt have drifted apart")
	}
	if n := strings.Count(doc, commentsTruthPrinciple); n != 1 {
		t.Errorf("CLAUDE.md mirrors the comment-truth principle %d times, want exactly 1 (a duplicated mirror drifts one copy at a time)", n)
	}
	if !strings.Contains(doc, "\n\n"+commentsTruthPrinciple+"\n\n") {
		t.Error("CLAUDE.md's mirrored comment-truth principle must be its own paragraph (blank-line separated), not spliced into another")
	}
}

// TestTestTargetPrincipleSpliced pins issue #225's test-target splice: the
// built-in SystemPrompt's "Test integrity" paragraph must carry
// testTargetPrinciple VERBATIM — the test the model writes goes through the
// user-facing path the requirement names, never a lower-level helper — before
// #141's existing-test clause, so #141 and #225 read as one rule. Content is
// pinned by the loose keyword checks in
// TestDefaultPromptEncodesTestTargetGuidance; this is the
// connect-through: a test target that never calls the user-facing surface
// proves nothing about it.
func TestTestTargetPrincipleSpliced(t *testing.T) {
	i := strings.Index(SystemPrompt, testTargetPrinciple)
	if i < 0 {
		t.Fatal("the test-target principle is not in the built-in prompt")
	}
	// It sits in the "Test integrity" paragraph, before #141's clause.
	if !strings.Contains(SystemPrompt[i-20:i+len(testTargetPrinciple)+20], "Test integrity") {
		t.Error("the test-target principle must be spliced into the \"Test integrity\" paragraph")
	}
	if n := strings.Index(SystemPrompt, "Removing or changing an existing test"); n < i {
		t.Error("the test-target principle must precede #141's existing-test clause in the Test integrity paragraph")
	}
	// And it is its OWN principle (one verbatim copy), not a paraphrase:
	// a test written to match the code is the same class of bending as a
	// changed existing test.
	if c := strings.Count(SystemPrompt, testTargetPrinciple); c != 1 {
		t.Errorf("the test-target principle must appear exactly once in the built-in prompt, found %d", c)
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

// TestDefaultPromptEncodesLocateFirst pins issue #142's locate-first
// principle in the built-in SystemPrompt (tightened for issue #209): the
// prompt must tell the model to locate before it reads (outline/grep to find
// where content lives, then read_file only the needed spans), never to invent
// or guess file paths, never to re-read content already present in context,
// and never to use bash to READ files (`cat`/`sed`/`head`) or CREATE them
// (`cat > f`, heredocs, `tee`, `/tmp` scratch) — pointing at the
// dedicated tools: read_file/outline/grep for reads and write_file/edit_file
// for writes. The check is on loose keywords (the principle, not the exact
// wording), so a future rewrite can rephrase without breaking this test as
// long as the idea survives — the same loose style as
// TestDefaultPromptEncodesDebugGuidance.
func TestDefaultPromptEncodesLocateFirst(t *testing.T) {
	lower := strings.ToLower(SystemPrompt)
	for _, keyword := range []string{
		"outline",    // locate with outline/grep before reading
		"grep",       // locate with grep before reading
		"read",       // read only what you need (read_file, re-read)
		"guess",      // never invent or guess file paths
		"bash",       // never use bash to read or create files
		"cat",        // the bash reader to avoid (reads)
		"create",     // the ban covers file CREATION, not just reads (#209)
		"heredoc",    // heredoc file creation (spelled out)
		"tee",        // `tee` file creation
		"/tmp",       // `/tmp` scratch files
		"write_file", // the dedicated writer to use for creation
		"edit_file",  // the dedicated writer to use for edits
		"context",    // never re-read content already in context
	} {
		if !strings.Contains(lower, keyword) {
			t.Errorf("built-in prompt no longer encodes the locate-first principle (missing %q)", keyword)
		}
	}
}

// TestDefaultPromptEncodesLocateFirstPosition pins the issue #142 principle's
// POSITION in the built-in prompt: the locate-first working-style principle
// (locateFirstPrinciple, spliced into SystemPrompt in the "# How you work"
// block) must sit in that block, before "# How you communicate". This is a
// position check, not a content check — the content is pinned by
// TestDefaultPromptEncodesLocateFirst (loose keywords) and
// TestLocateFirstPrincipleMirroredInClaudeMD (verbatim mirror in CLAUDE.md). A
// rewrite that moves the principle into a different section (or a different
// prompt slot) fails here, even if the wording survives.
func TestDefaultPromptEncodesLocateFirstPosition(t *testing.T) {
	if i := strings.Index(SystemPrompt, locateFirstPrinciple); i < 0 {
		t.Fatal("the locate-first working-style principle is not in the built-in prompt")
	} else if j := strings.Index(SystemPrompt, "# How you communicate"); j < i {
		t.Error("the locate-first working-style principle must sit in the \"# How you work\" block, before \"# How you communicate\"")
	}
}

// TestDefaultPromptEncodesLocateFirstCreateBan pins the issue #209 tightening
// of the locate-first principle's POSITION: the new file-creation bash ban
// (`cat > f`/heredocs/`tee`/`/tmp` scratch, steering to write_file/
// edit_file) must sit in the SAME "# How you work" block as the read ban —
// the principle is one working-style line, not split across sections. It is a
// position check on the exact new phrasing, complementing
// TestDefaultPromptEncodesLocateFirst (loose keywords for the content) and
// TestDefaultPromptEncodesLocateFirstPosition (the whole principle's slot).
func TestDefaultPromptEncodesLocateFirstCreateBan(t *testing.T) {
	createBan := "never use bash to read or create files"
	if i := strings.Index(SystemPrompt, createBan); i < 0 {
		t.Fatal("the locate-first principle's file-creation bash ban (issue #209) is not in the built-in prompt")
	} else if j := strings.Index(SystemPrompt, "# How you work"); j < 0 {
		t.Error("the built-in prompt is missing the \"# How you work\" block")
	} else if k := strings.Index(SystemPrompt, "# How you communicate"); k < 0 || i < j || i >= k {
		t.Error("the locate-first principle's file-creation bash ban must sit in the \"# How you work\" block, between \"# How you work\" and \"# How you communicate\"")
	}
}

// TestLocateFirstPrincipleMirroredInClaudeMD is the issue #142 consistency
// tripwire: CLAUDE.md's "The agent's tools" section must mirror the EXACT
// same locate-first guidance the model actually receives in the built-in
// prompt (locateFirstPrinciple) — the docs describe the guidance, so the two
// can't drift apart. It reads the file from the module root (the test's
// working directory is the package dir, cmd/cortex), so it passes wherever
// the checkout lives.
func TestLocateFirstPrincipleMirroredInClaudeMD(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "CLAUDE.md"))
	if err != nil {
		t.Fatalf("cannot read CLAUDE.md (the mirrored guidance can't be verified): %v", err)
	}
	if !strings.Contains(string(data), locateFirstPrinciple) {
		t.Error("CLAUDE.md's \"The agent's tools\" section no longer mirrors the built-in prompt's locate-first working-style principle verbatim (locateFirstPrinciple) — the docs and the prompt have drifted apart")
	}
}

// TestDefaultPromptEncodesFailingTestGuidance pins issue #177's content: the
// built-in prompt must treat an existing test's expected value as evidence —
// the burden of proof lands on a change that disagrees with it, and rewriting
// an expectation to match output you just produced is not a fix. (The loop in
// PR #176 edited a correct assertion to match its own off-by-one output; this
// is the guidance that was missing.) Each keyword was checked against the
// pre-#177 prompt (strings.Contains on SystemPrompt with the principle
// removed) so it rides only on the new principle, and none leans on the
// recipe wording the principle deliberately avoids — the same loose,
// rewrite-tolerant style as TestDefaultPromptEncodesTestIntegrity.
func TestDefaultPromptEncodesFailingTestGuidance(t *testing.T) {
	lower := strings.ToLower(SystemPrompt)
	tests := []struct {
		keyword string
		intent  string
	}{
		{"expected value", "the expectation is the recorded decision the change must answer to"},
		{"burden of proof", "when a test disagrees with your change, the change bears the proof"},
		{"never a fix", "matching your own new output is never a fix"},
		{"specification", "rewritten output becomes the spec — the failure mode to avoid"},
	}
	for _, tt := range tests {
		t.Run(tt.keyword, func(t *testing.T) {
			if !strings.Contains(lower, tt.keyword) {
				t.Errorf("built-in prompt no longer encodes the failing-test guidance (%s): missing %q", tt.intent, tt.keyword)
			}
		})
	}
}

// TestFailingTestPrinciplePosition pins where the issue #177 principle sits:
// in the "# How you work" block, after the "Test integrity" paragraph (issue
// #141's) and before the debugging principle (issue #154's) — a position
// check, not a content check, mirroring TestDefaultPromptEncodesDebugWorkingStyle.
func TestFailingTestPrinciplePosition(t *testing.T) {
	i := strings.Index(SystemPrompt, failingTestPrinciple)
	if i < 0 {
		t.Fatal("the failing-test working-style principle is not in the built-in prompt")
	}
	if ti := strings.Index(SystemPrompt, "Test integrity."); ti < 0 || i < ti {
		t.Error("the failing-test principle must sit after the \"Test integrity\" paragraph")
	}
	if d := strings.Index(SystemPrompt, debugWorkingStylePrinciple); d < i {
		t.Error("the failing-test principle must sit before the debugging principle")
	}
	if j := strings.Index(SystemPrompt, "# How you communicate"); j < i {
		t.Error("the failing-test principle must sit in the \"# How you work\" block, before \"# How you communicate\"")
	}
}

// TestFailingTestPrincipleMirroredInClaudeMD is the issue #177 consistency
// tripwire, mirroring TestDebugPrincipleMirroredInClaudeMD: CLAUDE.md's
// "Constraints → Testing" section must carry the EXACT text the model
// receives (failingTestPrinciple) so docs and prompt can't drift apart.
func TestFailingTestPrincipleMirroredInClaudeMD(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "CLAUDE.md"))
	if err != nil {
		t.Fatalf("cannot read CLAUDE.md (the mirrored guidance can't be verified): %v", err)
	}
	if !strings.Contains(string(data), failingTestPrinciple) {
		t.Error("CLAUDE.md's \"Constraints → Testing\" section no longer mirrors the built-in prompt's failing-test working-style principle verbatim (failingTestPrinciple) — the docs and the prompt have drifted apart")
	}
}

// TestDefaultPromptEncodesLocateBeforeWriting pins issue #224's content: the
// built-in prompt must tell the model to locate before WRITING — grep or
// outline for a helper, constant, type or path it has not seen this session
// instead of inventing one, and check a new package-level name against the
// package (including its other _test.go files) so it neither references what
// doesn't exist nor collides with what does. The self-dev loop's tick
// 20261006T074738Z is the case this exists for: sessions wrote test files
// against guessed identifiers (seven undefined at once), carried a type-shape
// compile error from one session into the next, and duplicated a
// package-level const — each guess costing a build-and-fix round.
//
// The keyword assertions run against locateBeforeWritingPrinciple ITSELF,
// and each absence-checked keyword is additionally asserted ABSENT from the
// full prompt with the principle stripped — so every subtest actually rides
// on the new principle (the same style as
// TestDefaultPromptEncodesBlockedCheckGuidance). Keywords that occur
// elsewhere in the prompt ("grep", "outline", "invent" also sit in
// locateFirstPrinciple; "package" also sits in debugWorkingStylePrinciple and
// the Inspect line) are checked in the const only.
func TestDefaultPromptEncodesLocateBeforeWriting(t *testing.T) {
	// The full prompt with the principle removed: its splice site is
	// locateFirstPrinciple + "\n\n" + locateBeforeWritingPrinciple + "\n\n" —
	// strip exactly the principle and one surrounding blank-line separator,
	// leaving the rest of the prompt intact.
	withoutPrinciple := strings.Replace(SystemPrompt,
		"\n\n"+locateBeforeWritingPrinciple, "", 1)
	if withoutPrinciple == SystemPrompt {
		t.Fatal("could not locate the locate-before-writing principle's splice site in SystemPrompt — the removal below would be a no-op")
	}
	tests := []struct {
		keyword      string
		intent       string
		checkAbsence bool // false when the keyword also occurs elsewhere in the prompt
	}{
		{"helper", "the trigger: a helper (or constant/type/path) named by new code", false}, // testTargetPrinciple (#225) also names "a lower-level helper"
		{"collide", "the collision ban: don't redeclare a package-level name that exists", true},
		{"package", "the scope to check: the package the new name lands in", false}, // "package" also occurs in debugWorkingStylePrinciple and the Inspect line
		{"undefined", "the failure class the guess produces (a name that is not there)", true},
		{"grep", "the locate tool to use before writing", false},                           // locateFirstPrinciple also names grep
		{"outline", "the other locate tool to use before writing", false},                  // locateFirstPrinciple also names outline
		{"invent", "the ban: never invent identifiers, type shapes, or file paths", false}, // locateFirstPrinciple also says "never invent or guess file paths"
		{"_test.go", "the test-file scope: check the package's other test files too", true},
	}
	for _, tt := range tests {
		t.Run(tt.keyword, func(t *testing.T) {
			if !strings.Contains(strings.ToLower(locateBeforeWritingPrinciple), tt.keyword) {
				t.Errorf("locateBeforeWritingPrinciple no longer encodes the locate-before-writing guidance (%s): missing %q", tt.intent, tt.keyword)
			}
			if tt.checkAbsence && strings.Contains(strings.ToLower(withoutPrinciple), tt.keyword) {
				t.Errorf("keyword %q survives with the principle removed — it does not ride on the new principle: %s", tt.keyword, tt.intent)
			}
		})
	}
}

// TestLocateBeforeWritingPrinciplePosition pins where the issue #224
// principle sits: right after locateFirstPrinciple (the reading half it
// extends) inside the "# How you work" block, before "# How you
// communicate" — a position check, not a content check, mirroring
// TestDefaultPromptEncodesLocateFirstPosition. The placement matters: the
// writing half must be met immediately after the reading half, as one
// locate-before-you-touch-anything pair.
func TestLocateBeforeWritingPrinciplePosition(t *testing.T) {
	i := strings.Index(SystemPrompt, locateBeforeWritingPrinciple)
	if i < 0 {
		t.Fatal("the locate-before-writing principle is not in the built-in prompt")
	}
	if j := strings.Index(SystemPrompt, locateFirstPrinciple); j < 0 || i < j {
		t.Error("the locate-before-writing principle must sit right after the locate-first principle it extends")
	}
	if j := strings.Index(SystemPrompt, "# How you work"); j < 0 || i < j {
		t.Error("the locate-before-writing principle must sit in the \"# How you work\" block")
	}
	if k := strings.Index(SystemPrompt, "# How you communicate"); k < 0 || i >= k {
		t.Error("the locate-before-writing principle must sit before \"# How you communicate\"")
	}
	// Its own paragraph, not spliced mid-line into another principle.
	if n := strings.Count(SystemPrompt, locateBeforeWritingPrinciple); n != 1 {
		t.Errorf("the locate-before-writing principle must appear exactly once in the built-in prompt, found %d", n)
	}
}

// TestLocateBeforeWritingPrincipleMirroredInClaudeMD is the issue #224
// consistency tripwire, mirroring TestLocateFirstPrincipleMirroredInClaudeMD:
// CLAUDE.md must mirror the EXACT same guidance the model receives
// (locateBeforeWritingPrinciple) so docs and prompt can't drift apart.
func TestLocateBeforeWritingPrincipleMirroredInClaudeMD(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "CLAUDE.md"))
	if err != nil {
		t.Fatalf("cannot read CLAUDE.md (the mirrored guidance can't be verified): %v", err)
	}
	if !strings.Contains(string(data), locateBeforeWritingPrinciple) {
		t.Error("CLAUDE.md no longer mirrors the built-in prompt's locate-before-writing working-style principle verbatim (locateBeforeWritingPrinciple) — the docs and the prompt have drifted apart")
	}
}
