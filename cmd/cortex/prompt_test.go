package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dereksantos/cortex/internal/tools"
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

// defaultComposedSystemPrompt is the system text a normal session sends with
// default config: the built-in base prompt plus the default attribution line
// (the stable system message, systemPromptContent) plus the full memory
// section that rides in the ephemeral slot once notes or an outline exist
// (memorySectionFor). A long model name stands in for the real one the
// attribution trailer names.
func defaultComposedSystemPrompt(t *testing.T) string {
	t.Helper()
	resetPrompt(t)
	cfg := &Config{}
	configurePrompt(cfg)
	configureAttributionPrompt(cfg, "qwen/qwen3-coder-30b-a3b-instruct-q4_k_m")
	stable := systemPromptContent("", "")
	return stable + "\n\n" + memorySectionFor("## Project memory\n- a-note — a hook", true, promptBase == SystemPrompt)
}

// TestSystemPromptSizeBudget caps the default composed system prompt at
// 4,000 characters. The system prompt is paid on every model call, and small
// local models attend poorly to long ones: each added rule dilutes the rest.
// When a failure tempts you to add a rule here, put the guidance in the
// repo's AGENTS.md or an Agent Skill instead — the harness prompt states
// general principles only.
func TestSystemPromptSizeBudget(t *testing.T) {
	const budget = 4000
	got := defaultComposedSystemPrompt(t)
	if !strings.Contains(got, memoryPromptSection) {
		t.Fatal("the composed prompt must include the memory section, as a normal session with notes sends it")
	}
	if n := len(got); n > budget {
		t.Errorf("default composed system prompt is %d chars, over the %d-char budget; move situation- or repo-specific guidance to AGENTS.md or a skill instead of growing the harness prompt", n, budget)
	}
}

// TestDefaultPromptCarriesCoreIntents pins the few intents the built-in
// prompt exists to carry, by key phrase rather than whole sentences, so the
// wording can be revised without breaking the test while a revert that drops
// an idea fails loudly.
func TestDefaultPromptCarriesCoreIntents(t *testing.T) {
	tests := []struct {
		intent string
		phrase string
	}{
		{"reproduce a reported problem before fixing it", "reproduce a reported problem before fixing"},
		{"a problem that doesn't reproduce is reported with the evidence", "report that with the evidence"},
		{"don't bend tests to fit a change", "suspect your change first"},
		{"report changed tests and unmet requirements", "say so plainly in your summary"},
		{"verify through the real entry point", "entry point that needs it actually reaches it"},
		{"a check that couldn't run is unknown, not inferred", "its result is unknown"},
		{"locate before acting; don't guess names", "instead of guessing"},
	}
	lower := strings.ToLower(SystemPrompt)
	for _, tt := range tests {
		t.Run(tt.intent, func(t *testing.T) {
			if !strings.Contains(lower, strings.ToLower(tt.phrase)) {
				t.Errorf("SystemPrompt no longer carries the intent %q (missing phrase %q)", tt.intent, tt.phrase)
			}
		})
	}
}

// languageSpecificTerms are terms that tie harness text to one language or
// toolchain. Cortex works in any repository; guidance that names a specific
// language's tools belongs in that repo's AGENTS.md or an Agent Skill. Matching
// is case-sensitive. Extend the list freely.
var languageSpecificTerms = []string{
	"go test", "go build", "go vet", "gofmt", "golang", "_test.go",
	"t.Fatal", "t.Logf", "t.Errorf", "package-level", "goroutine",
	"pytest", "npm ", "cargo ", ".go", ".py",
}

// harnessSurface is one piece of harness-owned text the model sees.
type harnessSurface struct{ name, text string }

// harnessTextSurfaces lists every piece of harness-owned text the model sees:
// the base, memory, and checklist prompts, the plan-mode prompts, each
// subagent profile's system prompt and seed, and every coder tool's name,
// description, and parameter descriptions. Add a surface here when you add
// one to the harness.
func harnessTextSurfaces() []harnessSurface {
	surfaces := []harnessSurface{
		{"SystemPrompt", SystemPrompt},
		{"memoryPromptSection", memoryPromptSection},
		{"checklistAccountingPrinciple", checklistAccountingPrinciple},
		{"planModeInstruction", planModeInstruction},
		{"planStepPrompt", planStepPrompt("TASK", 1, 2, "STEP", nil)},
		{"StudySeed", tools.StudySeed("GOAL", "PATH", "")},
	}
	for _, sa := range []tools.Subagent{tools.Study, tools.Agent, tools.Learn, tools.LearnUser} {
		surfaces = append(surfaces, harnessSurface{"subagent " + sa.Role + " system", sa.System})
	}
	for _, tool := range tools.All {
		fn := tool.Function
		surfaces = append(surfaces,
			harnessSurface{"tool " + fn.Name + " name", fn.Name},
			harnessSurface{"tool " + fn.Name + " description", fn.Description},
		)
		for _, d := range paramDescriptions(fn.Parameters) {
			surfaces = append(surfaces, harnessSurface{"tool " + fn.Name + " parameter", d})
		}
	}
	return surfaces
}

// paramDescriptions walks a JSON-Schema parameters object and returns every
// "description" string in it, nested properties and array items included.
func paramDescriptions(v any) []string {
	var out []string
	switch x := v.(type) {
	case map[string]any:
		for k, child := range x {
			if s, ok := child.(string); ok && k == "description" {
				out = append(out, s)
				continue
			}
			out = append(out, paramDescriptions(child)...)
		}
	case []any:
		for _, child := range x {
			out = append(out, paramDescriptions(child)...)
		}
	}
	return out
}

// TestHarnessTextIsLanguageNeutral keeps every harness-owned surface the model
// sees free of language- and toolchain-specific terms: Cortex is a general
// coding agent, and a Go (or Python, or Rust) idiom in the harness text
// misleads it in every other kind of repository.
func TestHarnessTextIsLanguageNeutral(t *testing.T) {
	for _, s := range harnessTextSurfaces() {
		t.Run(s.name, func(t *testing.T) {
			if strings.TrimSpace(s.text) == "" {
				t.Fatalf("surface %q is empty — the scan would check nothing", s.name)
			}
			for _, term := range languageSpecificTerms {
				if i := strings.Index(s.text, term); i >= 0 {
					lo, hi := max(0, i-60), min(len(s.text), i+len(term)+60)
					t.Errorf("%s contains language-specific term %q: …%s…\nMove language- or repo-specific guidance to the repo's AGENTS.md or an Agent Skill; harness text must stay language-neutral.", s.name, term, s.text[lo:hi])
				}
			}
		})
	}
}
