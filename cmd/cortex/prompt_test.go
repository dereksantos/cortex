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
	base, appendix := promptBase, promptAppend
	t.Cleanup(func() { promptBase, promptAppend = base, appendix })
}

func TestSystemPromptContentDefault(t *testing.T) {
	resetPrompt(t)
	configurePrompt(nil)

	if got := systemPromptContent(""); got != SystemPrompt {
		t.Errorf("nil config must yield the built-in prompt verbatim; got %d bytes, want %d", len(got), len(SystemPrompt))
	}
	got := systemPromptContent("do the thing")
	if !strings.HasPrefix(got, SystemPrompt) {
		t.Error("instructions must ride after the base prompt, not replace it")
	}
	if !strings.Contains(got, agentsMarker+"do the thing") {
		t.Error("AGENTS.md body must follow the agentsMarker separator")
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
	got := systemPromptContent("")
	if got != "You are a custom agent." {
		t.Errorf("prompt.file must replace the built-in base (trimmed); got %q", got)
	}
}

func TestConfigurePromptFileMissingFallsBack(t *testing.T) {
	resetPrompt(t)
	configurePrompt(&Config{Prompt: PromptConfig{File: filepath.Join(t.TempDir(), "nope.md")}})
	if got := systemPromptContent(""); got != SystemPrompt {
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
	if got := systemPromptContent(""); got != SystemPrompt {
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
	if got := systemPromptContent(""); got != "from the repo root" {
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
	got := systemPromptContent("")
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

	got := systemPromptContent("agents body")
	base := strings.SplitN(got, agentsMarker, 2)[0]
	if !strings.HasPrefix(base, SystemPrompt) {
		t.Error("append must extend the base prompt, not replace it")
	}
	if !strings.Contains(base, "Always answer in haiku.") {
		t.Error("prompt.append must appear in the system section")
	}
	if strings.Index(got, "Always answer in haiku.") > strings.Index(got, agentsMarker) {
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
	got := systemPromptContent("")
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

// The built-in prompt must encode the four working-style preferences
// (2026-07-20): verify-first, clarify ambiguity with the user, delegation to
// subagents, simple communication, and honest scoping. Keyword checks are
// deliberately loose — they pin that a principle survives future rewrites,
// not its exact wording.
func TestDefaultPromptEncodesWorkingStyle(t *testing.T) {
	lower := strings.ToLower(SystemPrompt)
	for _, principle := range []string{
		"test",     // verify-first: tests before the change
		"delegate", // reasoning model handing bounded work to subagents
		"ask",      // clarify with the user when ambiguous
		"simple",   // simplicity in code and in replies
		"scope",    // scope + feasibility, optimistic but realistic
	} {
		if !strings.Contains(lower, principle) {
			t.Errorf("built-in prompt no longer mentions %q — a core working-style principle was dropped", principle)
		}
	}
}
