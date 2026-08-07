package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// newCorpus builds a miniature polyglot checkout with the exercises named,
// each shaped exactly like the real ones: .docs/ instructions, .meta/ config
// plus the reference solution, a stub, a test, and a go.mod.
func newCorpus(t *testing.T, names ...string) string {
	t.Helper()
	root := t.TempDir()
	for _, name := range names {
		dir := filepath.Join(root, "go", "exercises", "practice", name)
		files := map[string]string{
			filepath.Join(".docs", "instructions.md"):        "# Instructions\n\nSolve " + name + ".\n",
			filepath.Join(".docs", "instructions.append.md"): "# Implementation\n\nfunc Solve()\n",
			filepath.Join(".meta", "config.json"): `{"files":{"solution":["` + name + `.go"],` +
				`"test":["` + name + `_test.go"],"example":[".meta/example.go"]}}`,
			filepath.Join(".meta", "example.go"): "package " + name + "\n\n// THE ANSWER\n",
			name + ".go":                         "package " + name + "\n\nfunc Solve() {}\n",
			name + "_test.go":                    "package " + name + "\n",
			"go.mod":                             "module " + name + "\n\ngo 1.18\n",
		}
		for rel, body := range files {
			p := filepath.Join(dir, rel)
			if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
				t.Fatalf("mkdir %s: %v", p, err)
			}
			if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
				t.Fatalf("write %s: %v", p, err)
			}
		}
	}
	return root
}

func TestDiscoverExercises(t *testing.T) {
	root := newCorpus(t, "wordy", "matrix", "alphametics")
	got, err := DiscoverExercises(root)
	if err != nil {
		t.Fatalf("DiscoverExercises: %v", err)
	}
	want := []string{"alphametics", "matrix", "wordy"}
	if len(got) != len(want) {
		t.Fatalf("discovered %d exercises, want %d", len(got), len(want))
	}
	for i, w := range want {
		if got[i].Name != w {
			t.Errorf("exercise %d = %q, want %q (discovery must be name-sorted so --only N is stable)", i, got[i].Name, w)
		}
	}
	if len(got[0].Solution) != 1 || got[0].Solution[0] != "alphametics.go" {
		t.Errorf("solution files = %v, want [alphametics.go]", got[0].Solution)
	}
	if len(got[0].Test) != 1 || got[0].Test[0] != "alphametics_test.go" {
		t.Errorf("test files = %v, want [alphametics_test.go]", got[0].Test)
	}
}

func TestDiscoverExercisesMissingRoot(t *testing.T) {
	if _, err := DiscoverExercises(t.TempDir()); err == nil {
		t.Error("DiscoverExercises on a checkout with no go/exercises/practice = nil error, want an error")
	}
}

func TestSelectExercises(t *testing.T) {
	all, err := DiscoverExercises(newCorpus(t, "wordy", "matrix", "alphametics"))
	if err != nil {
		t.Fatalf("DiscoverExercises: %v", err)
	}

	tests := []struct {
		name    string
		names   []string
		only    int
		want    []string
		wantErr bool
	}{
		{name: "no selection runs everything", want: []string{"alphametics", "matrix", "wordy"}},
		{name: "only N takes the first N in name order", only: 2, want: []string{"alphametics", "matrix"}},
		{name: "only above the corpus size runs everything", only: 99, want: []string{"alphametics", "matrix", "wordy"}},
		{name: "explicit names keep the order given", names: []string{"wordy", "alphametics"}, want: []string{"wordy", "alphametics"}},
		{name: "explicit names override only", names: []string{"wordy"}, only: 2, want: []string{"wordy"}},
		{name: "an unknown name is an error", names: []string{"nosuch"}, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := SelectExercises(all, tt.names, tt.only)
			if tt.wantErr {
				if err == nil {
					t.Fatal("SelectExercises = nil error, want an error")
				}
				return
			}
			if err != nil {
				t.Fatalf("SelectExercises: %v", err)
			}
			if names := exerciseNames(got); strings.Join(names, ",") != strings.Join(tt.want, ",") {
				t.Errorf("selected %v, want %v", names, tt.want)
			}
		})
	}
}

// TestPrepareWorkdirExcludesReferenceSolution is the benchmark's integrity
// test: .meta/ holds example.go, the answer. If it ever reaches the agent's
// workspace the score measures copying, not coding.
func TestPrepareWorkdirExcludesReferenceSolution(t *testing.T) {
	all, err := DiscoverExercises(newCorpus(t, "wordy"))
	if err != nil {
		t.Fatalf("DiscoverExercises: %v", err)
	}
	work := filepath.Join(t.TempDir(), "work")
	before, err := PrepareWorkdir(all[0], work)
	if err != nil {
		t.Fatalf("PrepareWorkdir: %v", err)
	}

	for _, banned := range []string{".meta", ".docs"} {
		if _, err := os.Stat(filepath.Join(work, banned)); !os.IsNotExist(err) {
			t.Errorf("%s/ was staged into the agent's workspace; it must be excluded", banned)
		}
	}
	for _, want := range []string{"wordy.go", "wordy_test.go", "go.mod"} {
		if _, err := os.Stat(filepath.Join(work, want)); err != nil {
			t.Errorf("%s missing from the staged workspace: %v", want, err)
		}
	}
	if len(before) != 1 || before["wordy.go"] == "" {
		t.Errorf("pristine digest = %v, want one non-empty entry for wordy.go", before)
	}
}

func TestCountChanged(t *testing.T) {
	all, err := DiscoverExercises(newCorpus(t, "wordy"))
	if err != nil {
		t.Fatalf("DiscoverExercises: %v", err)
	}
	ex := all[0]
	work := filepath.Join(t.TempDir(), "work")
	before, err := PrepareWorkdir(ex, work)
	if err != nil {
		t.Fatalf("PrepareWorkdir: %v", err)
	}

	t.Run("an untouched workspace reports zero changes", func(t *testing.T) {
		after, err := HashSolutionFiles(ex, work)
		if err != nil {
			t.Fatalf("HashSolutionFiles: %v", err)
		}
		if n := CountChanged(before, after); n != 0 {
			t.Errorf("CountChanged = %d, want 0", n)
		}
	})

	t.Run("rewriting the stub byte-for-byte still reports zero changes", func(t *testing.T) {
		p := filepath.Join(work, "wordy.go")
		body, err := os.ReadFile(p)
		if err != nil {
			t.Fatalf("read stub: %v", err)
		}
		if err := os.WriteFile(p, body, 0o644); err != nil {
			t.Fatalf("rewrite stub: %v", err)
		}
		after, err := HashSolutionFiles(ex, work)
		if err != nil {
			t.Fatalf("HashSolutionFiles: %v", err)
		}
		if n := CountChanged(before, after); n != 0 {
			t.Errorf("CountChanged = %d, want 0 — an identical rewrite is not work on disk", n)
		}
	})

	t.Run("an implemented stub reports one change", func(t *testing.T) {
		body := "package wordy\n\nfunc Solve() { println(1) }\n"
		if err := os.WriteFile(filepath.Join(work, "wordy.go"), []byte(body), 0o644); err != nil {
			t.Fatalf("write solution: %v", err)
		}
		after, err := HashSolutionFiles(ex, work)
		if err != nil {
			t.Fatalf("HashSolutionFiles: %v", err)
		}
		if n := CountChanged(before, after); n != 1 {
			t.Errorf("CountChanged = %d, want 1", n)
		}
	})

	t.Run("a deleted stub reports one change", func(t *testing.T) {
		if err := os.Remove(filepath.Join(work, "wordy.go")); err != nil {
			t.Fatalf("remove solution: %v", err)
		}
		after, err := HashSolutionFiles(ex, work)
		if err != nil {
			t.Fatalf("HashSolutionFiles: %v", err)
		}
		if n := CountChanged(before, after); n != 1 {
			t.Errorf("CountChanged = %d, want 1", n)
		}
	})
}

func TestBuildPrompt(t *testing.T) {
	all, err := DiscoverExercises(newCorpus(t, "wordy"))
	if err != nil {
		t.Fatalf("DiscoverExercises: %v", err)
	}
	prompt, err := BuildPrompt(all[0])
	if err != nil {
		t.Fatalf("BuildPrompt: %v", err)
	}
	for _, want := range []string{
		"Solve wordy.",           // instructions.md
		"func Solve()",           // instructions.append.md
		"- Implement: wordy.go",  // the task frame
		"wordy_test.go",          // the do-not-modify list
		"go test ./...",          // how correctness is decided
		"Standard library only:", // the dependency constraint
	} {
		if !strings.Contains(prompt, want) {
			t.Errorf("prompt is missing %q\n---\n%s", want, prompt)
		}
	}
	if strings.Contains(prompt, "THE ANSWER") {
		t.Error("prompt leaked the reference solution from .meta/example.go")
	}
}
