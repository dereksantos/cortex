package codebase

import (
	"path/filepath"
	"runtime"
	"testing"
)

// TestFixturesLoad confirms every shipped fixture under
// test/evals/scenarios/codebase-reading parses without validation
// errors. This is the "fixtures are well-formed" smoke check we run on
// every CI tick — it's cheap and catches yaml typos before an LLM-bound
// run wastes time and tokens.
//
// Slice 2 covers cortex (Go) × leanjs (JS) × 11 evals = 22 fixtures.
// Slice 5 will add Python + Rust to the matrix — this test will pick
// them up automatically.
func TestFixturesLoad(t *testing.T) {
	_, thisFile, _, _ := runtime.Caller(0)
	repoRoot := filepath.Join(filepath.Dir(thisFile), "..", "..", "..")
	dir := filepath.Join(repoRoot, "test", "evals", "scenarios", "codebase-reading")
	fxs, err := LoadDir(dir)
	if err != nil {
		t.Fatalf("LoadDir %s: %v", dir, err)
	}
	if len(fxs) < 44 {
		t.Fatalf("LoadDir returned %d fixtures; expected at least 44 (4 projects × 11 evals)", len(fxs))
	}

	// Per-project coverage: every project should hit all 11 evals
	// (R1-R3, Q1-Q5, B1-B3). Drift here surfaces "we forgot to
	// author the leanjs Q4" kind of bugs.
	wantEvals := []string{"R1", "R2", "R3", "Q1", "Q2", "Q3", "Q4", "Q5", "B1", "B2", "B3"}
	perProject := map[string]map[string]bool{}
	for _, fx := range fxs {
		if _, ok := perProject[fx.Project]; !ok {
			perProject[fx.Project] = map[string]bool{}
		}
		perProject[fx.Project][fx.Eval] = true
	}
	for _, proj := range []string{"cortex", "leanjs", "python-todo", "rust-weather"} {
		evals := perProject[proj]
		for _, e := range wantEvals {
			if !evals[e] {
				t.Errorf("project=%s missing eval %s", proj, e)
			}
		}
	}

	// Language axis coverage: slice-5 introduces python + rust to the
	// matrix. Test that we have at least one fixture per language.
	byLang := map[string]int{}
	for _, fx := range fxs {
		byLang[fx.Language]++
	}
	for _, lang := range []string{"go", "js", "python", "rust"} {
		if byLang[lang] == 0 {
			t.Errorf("language=%s has zero fixtures", lang)
		}
	}
}
