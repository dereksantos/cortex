package benchstore

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func writeFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestCheckDurable(t *testing.T) {
	tmp := t.TempDir()
	repo := filepath.Join(tmp, "repo")
	if err := os.MkdirAll(filepath.Join(repo, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	worktree := filepath.Join(tmp, "wt")
	writeFile(t, filepath.Join(worktree, ".git"), "gitdir: elsewhere\n") // a linked worktree's .git is a file

	tests := []struct {
		name      string
		dir       string
		ephemeral bool
	}{
		{"outside any repo", filepath.Join(tmp, "bench", "runs", "x"), false},
		{"gitignored dir inside a repo", filepath.Join(repo, ".cortex", "bench", "run1"), true},
		{"inside a linked worktree", filepath.Join(worktree, ".cortex", "bench"), true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := CheckDurable(tt.dir)
			if got := errors.Is(err, ErrEphemeral); got != tt.ephemeral {
				t.Errorf("CheckDurable(%s) = %v, want ephemeral=%v", tt.dir, err, tt.ephemeral)
			}
		})
	}
}

func TestBeginFinish(t *testing.T) {
	root := t.TempDir()
	t.Setenv(EnvHome, root)

	run, err := Begin(BeginOptions{Bench: "polyglot", RunID: "r1", Model: "m", Labels: map[string]string{"arm": "a"}})
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	if want := filepath.Join(root, "runs", "polyglot", "r1"); run.Dir != want {
		t.Errorf("run dir = %s, want %s", run.Dir, want)
	}
	writeFile(t, filepath.Join(run.Dir, "results.jsonl"),
		`{"exercise":"a","pass":true,"tokens_in":10,"tokens_out":2,"wall_ms":1500}`+"\n"+
			`{"exercise":"b","pass":false,"failure_class":"early_finalize","tokens_in":5,"wall_ms":500}`+"\n")
	s, err := run.Finish("")
	if err != nil {
		t.Fatalf("Finish: %v", err)
	}
	if s.N != 2 || s.Pass != 1 || s.Fail != 1 || s.TokensIn != 15 || s.WallSec != 2 || s.Classes["early_finalize"] != 1 {
		t.Errorf("summary = %+v", s)
	}

	t.Run("never overwrites an existing run", func(t *testing.T) {
		if _, err := Begin(BeginOptions{Bench: "polyglot", RunID: "r1"}); err == nil || !strings.Contains(err.Error(), "already exists") {
			t.Errorf("second Begin with the same id: err = %v, want already exists", err)
		}
	})

	t.Run("index carries start then finish", func(t *testing.T) {
		entries, err := ReadIndex(root)
		if err != nil {
			t.Fatal(err)
		}
		if len(entries) != 2 || entries[0].Event != "start" || entries[1].Event != "finish" {
			t.Fatalf("index = %+v", entries)
		}
		latest := Latest(entries)
		if len(latest) != 1 || latest[0].Summary == nil || latest[0].Labels["arm"] != "a" {
			t.Errorf("latest = %+v", latest)
		}
	})
}

func TestBeginRefusesWorkingTree(t *testing.T) {
	t.Setenv(EnvHome, t.TempDir())
	repo := t.TempDir()
	if err := os.Mkdir(filepath.Join(repo, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(repo, ".cortex", "bench")
	if _, err := Begin(BeginOptions{Bench: "x", RunID: "r", OutRoot: out}); !errors.Is(err, ErrEphemeral) {
		t.Errorf("Begin into a repo: err = %v, want ErrEphemeral", err)
	}
	if _, err := Begin(BeginOptions{Bench: "x", RunID: "r", OutRoot: out, Ephemeral: true}); err != nil {
		t.Errorf("Begin --ephemeral into a repo: %v", err)
	}
}

func TestBeginSavesSourcePatchWhenDirty(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	t.Setenv(EnvHome, t.TempDir())
	repo := t.TempDir()
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", repo, "-c", "user.name=t", "-c", "user.email=t@t", "-c", "commit.gpgsign=false"}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	git("init", "-q")
	writeFile(t, filepath.Join(repo, "f.txt"), "one\n")
	git("add", ".")
	git("commit", "-qm", "init")
	writeFile(t, filepath.Join(repo, "f.txt"), "two\n")

	run, err := Begin(BeginOptions{Bench: "b", RunID: "r", SourceRepo: repo})
	if err != nil {
		t.Fatal(err)
	}
	patch, err := os.ReadFile(filepath.Join(run.Dir, "source.patch"))
	if err != nil {
		t.Fatalf("source.patch missing: %v", err)
	}
	if !strings.Contains(string(patch), "+two") {
		t.Errorf("source.patch does not carry the edit:\n%s", patch)
	}
	if !run.start.Dirty || run.start.Commit == "" {
		t.Errorf("start entry = %+v, want dirty with a commit", run.start)
	}
}

func TestSummarizeResultsShapes(t *testing.T) {
	tests := []struct {
		name                string
		rows                string
		pass, fail, skip, n int
	}{
		{"polyglot rows", `{"pass":true}` + "\n" + `{"pass":false}`, 1, 1, 0, 2},
		{"swebench rows", `{"resolved":true,"eval_status":"resolved","cost_usd":0.5}` + "\n" +
			`{"resolved":false,"eval_status":"unresolved"}` + "\n" +
			`{"resolved":false,"eval_status":"skipped_empty_patch"}`, 1, 1, 1, 3},
		{"bad line is skipped", `{"pass":true}` + "\nnot json\n", 1, 0, 0, 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := filepath.Join(t.TempDir(), "results.jsonl")
			writeFile(t, p, tt.rows+"\n")
			s, err := SummarizeResults(p)
			if err != nil {
				t.Fatal(err)
			}
			if s.N != tt.n || s.Pass != tt.pass || s.Fail != tt.fail || s.Skipped != tt.skip {
				t.Errorf("summary = %+v, want n=%d pass=%d fail=%d skip=%d", s, tt.n, tt.pass, tt.fail, tt.skip)
			}
		})
	}
}

func TestReindex(t *testing.T) {
	root := t.TempDir()
	runs := filepath.Join(root, "runs")
	writeFile(t, filepath.Join(runs, "swebench", "pair", "cortex", "run.json"), `{"model":"m","cortex_commit":"abc","started_at":"2026-10-08T00:00:00Z"}`)
	writeFile(t, filepath.Join(runs, "swebench", "pair", "cortex", "results.jsonl"), `{"resolved":true}`+"\n")
	writeFile(t, filepath.Join(runs, "swebench", "pair", "mini-x", "mini", "run.json"), `{"harness":"mini-swe-agent"}`)
	// The official harness's own run.json under an evidence dir is not a run.
	writeFile(t, filepath.Join(runs, "swebench", "pair", "cortex", "eval", "logs", "run.json"), `{}`)
	writeFile(t, filepath.Join(runs, "polyglot", "20260807", "run.json"), `{"model":"q"}`)

	added, err := Reindex(root)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]Entry{}
	for _, e := range added {
		got[e.Bench+"/"+e.RunID] = e
	}
	if len(got) != 3 {
		t.Fatalf("reindexed %d runs, want 3: %v", len(got), got)
	}
	if e := got["swebench/pair/cortex"]; e.Summary == nil || e.Summary.Pass != 1 || e.Labels["harness"] != "cortex" {
		t.Errorf("pair/cortex = %+v", e)
	}
	if e := got["swebench/pair/mini-x/mini"]; e.Labels["harness"] != "mini-swe-agent" {
		t.Errorf("mini harness label = %+v", e.Labels)
	}

	again, err := Reindex(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(again) != 0 {
		t.Errorf("second Reindex appended %d entries, want 0 (idempotent)", len(again))
	}
}

// TestEveryRunnerUsesTheStore is the tripwire for the durability rule: every
// benchmark runner under scripts/bench must open its run through Begin, so a
// new runner cannot quietly write results into a repo or worktree again.
func TestEveryRunnerUsesTheStore(t *testing.T) {
	mains, err := filepath.Glob(filepath.Join("..", "..", "scripts", "bench", "*", "main.go"))
	if err != nil {
		t.Fatal(err)
	}
	if len(mains) == 0 {
		t.Fatal("no runners found under scripts/bench — did the layout move?")
	}
	for _, m := range mains {
		if filepath.Base(filepath.Dir(m)) == "benchstore" {
			continue // the store's own CLI
		}
		b, err := os.ReadFile(m)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(b), "benchstore.Begin(") {
			t.Errorf("%s does not open its run with benchstore.Begin — results must go to the durable store (docs/benchmarks.md)", m)
		}
	}
}
