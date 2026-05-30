package codebase

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadFixture(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "ok.yaml")
	if err := os.WriteFile(path, []byte(`
id: q3-audit-cortex
group: Q
eval: Q3
project: cortex
prompt: "Does the README match the actual project implementation?"
expected:
  hop_count_min: 2
  hop_count_max: 5
  citation_rate_min: 0.7
  hedge_count_max: -1
  must_cite_paths:
    - pkg/cognition/dag/
    - cmd/cortex/main.go
  must_not_invent:
    - cortex-data-warehouse
  budget_token_min: 50000
  budget_token_max: 150000
`), 0o644); err != nil {
		t.Fatal(err)
	}
	fx, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if fx.ID != "q3-audit-cortex" {
		t.Errorf("ID = %q", fx.ID)
	}
	if fx.Group != GroupQuestion {
		t.Errorf("Group = %q", fx.Group)
	}
	if fx.Eval != "Q3" {
		t.Errorf("Eval = %q", fx.Eval)
	}
	if fx.Expected.CitationRateMin != 0.7 {
		t.Errorf("CitationRateMin = %v", fx.Expected.CitationRateMin)
	}
	if len(fx.Expected.MustCitePaths) != 2 {
		t.Errorf("MustCitePaths = %v", fx.Expected.MustCitePaths)
	}
}

func TestLoadFixtureRejectsBadGroup(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bad.yaml")
	if err := os.WriteFile(path, []byte(`
id: x
group: Z
eval: X1
project: cortex
prompt: "x"
`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("Load: want error for invalid group, got nil")
	}
}

func TestResolveFixturePath(t *testing.T) {
	repo := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repo, "test", "evals", "fixtures", "leanjs"), 0o755); err != nil {
		t.Fatal(err)
	}

	// cortex == repoRoot
	got := ResolveFixturePath("", repo, "cortex")
	if got != repo {
		t.Errorf("ResolveFixturePath(cortex) = %q, want %q", got, repo)
	}
	// fixtures/<name>
	got = ResolveFixturePath("", repo, "leanjs")
	want := filepath.Join(repo, "test", "evals", "fixtures", "leanjs")
	if got != want {
		t.Errorf("ResolveFixturePath(leanjs) = %q, want %q", got, want)
	}
	// fixtureRoot override
	override := t.TempDir()
	got = ResolveFixturePath(override, repo, "cortex")
	if got != filepath.Join(override, "cortex") && got != repo {
		// The override path doesn't exist; the resolver should fall
		// through to repoRoot in that case (cortex special-case).
		t.Errorf("ResolveFixturePath with non-existing override = %q", got)
	}
}

func TestLoadDir(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"r1.yaml", "q1.yaml", "q3.yaml"} {
		body := "id: " + name + "\ngroup: Q\neval: Q1\nproject: cortex\nprompt: \"x\"\n"
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// Drop a non-YAML file in there — should be ignored.
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("ignored"), 0o644); err != nil {
		t.Fatal(err)
	}
	fxs, err := LoadDir(dir)
	if err != nil {
		t.Fatalf("LoadDir: %v", err)
	}
	if len(fxs) != 3 {
		t.Errorf("LoadDir returned %d fixtures, want 3", len(fxs))
	}
}
