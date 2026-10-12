package benchstore

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

type fakeUploader struct {
	keys []string
	fail map[string]bool // unit prefixes whose upload fails
}

func (f *fakeUploader) Put(key, file string) error {
	if _, err := os.Stat(file); err != nil {
		return err
	}
	for p := range f.fail {
		if strings.HasPrefix(key, p) {
			return errors.New("simulated upload failure")
		}
	}
	f.keys = append(f.keys, key)
	return nil
}

func (f *fakeUploader) Name() string { return "fake" }

func TestSync(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "runs", "swebench", "pair", "cortex", "run.json"), `{"model":"m"}`)
	writeFile(t, filepath.Join(root, "runs", "swebench", "pair", "cortex", "results.jsonl"), `{"resolved":true}`+"\n")
	writeFile(t, filepath.Join(root, "runs", "swebench", "pair", "cortex", "trajs", "big.traj"), strings.Repeat("x", 4096))
	writeFile(t, filepath.Join(root, "runs", "swebench", "pair", "driver.log"), "log\n")
	writeFile(t, filepath.Join(root, "archive", "old", "evals.db"), "db")
	if err := AppendIndex(root, Entry{Event: "start", Bench: "swebench", RunID: "pair/cortex"}); err != nil {
		t.Fatal(err)
	}

	up := &fakeUploader{}
	rep, err := Sync(root, up)
	if err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if !rep.Committed {
		t.Error("first sync committed nothing")
	}
	if len(rep.Uploaded) != 2 {
		t.Fatalf("uploaded %v, want one bundle per unit (runs/swebench/pair, archive/old)", rep.Uploaded)
	}

	t.Run("git tracks records, not evidence", func(t *testing.T) {
		out, err := exec.Command("git", "-C", root, "ls-files").Output()
		if err != nil {
			t.Fatal(err)
		}
		files := string(out)
		for _, want := range []string{"index.jsonl", "runs/swebench/pair/cortex/run.json", "runs/swebench/pair/cortex/results.jsonl", ".sync/r2.jsonl", "archive/old/evals.db"} {
			if !strings.Contains(files, want) {
				t.Errorf("git does not track %s:\n%s", want, files)
			}
		}
		if strings.Contains(files, "big.traj") || strings.Contains(files, "driver.log") {
			t.Errorf("git tracks evidence:\n%s", files)
		}
	})

	t.Run("unchanged units are not re-uploaded", func(t *testing.T) {
		rep, err := Sync(root, up)
		if err != nil {
			t.Fatal(err)
		}
		if len(rep.Uploaded) != 0 || rep.Unchanged != 2 {
			t.Errorf("second sync: uploaded %v, unchanged %d", rep.Uploaded, rep.Unchanged)
		}
	})

	t.Run("a changed unit gets a new key, never overwriting", func(t *testing.T) {
		writeFile(t, filepath.Join(root, "runs", "swebench", "pair", "driver.log"), "log\nmore\n")
		rep, err := Sync(root, up)
		if err != nil {
			t.Fatal(err)
		}
		if len(rep.Uploaded) != 1 {
			t.Fatalf("uploaded %v, want 1", rep.Uploaded)
		}
		for _, k := range up.keys[:len(up.keys)-len(rep.Uploaded)-1] {
			if k == rep.Uploaded[0] {
				t.Errorf("changed unit reused key %s", k)
			}
		}
	})

	t.Run("no uploader still commits", func(t *testing.T) {
		writeFile(t, filepath.Join(root, "runs", "polyglot", "r2", "run.json"), `{}`)
		rep, err := Sync(root, nil)
		if err != nil || !rep.Committed {
			t.Errorf("Sync(nil) = %+v, %v; want a commit", rep, err)
		}
	})
}

func TestDefaultUploaderNeedsLocalConfig(t *testing.T) {
	root := t.TempDir()
	t.Setenv(EnvR2Bucket, "")
	if up, why := DefaultUploader(root); up != nil || !strings.Contains(why, "config.json") {
		t.Errorf("unconfigured: up=%v why=%q, want none naming config.json", up, why)
	}
	t.Setenv(EnvR2Bucket, "off")
	if up, _ := DefaultUploader(root); up != nil {
		t.Errorf("off: up=%v, want nil", up)
	}
}

func TestBeginAfterSync(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	root := t.TempDir()
	t.Setenv(EnvHome, root)
	if _, err := Begin(BeginOptions{Bench: "b", RunID: "first"}); err != nil {
		t.Fatal(err)
	}
	if _, err := Sync(root, nil); err != nil { // makes the store root a git repo
		t.Fatal(err)
	}
	if _, err := Begin(BeginOptions{Bench: "b", RunID: "second"}); err != nil {
		t.Errorf("Begin after Sync refused — the store's own .git must not count: %v", err)
	}
}

func TestSyncSurvivesUploadFailures(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "runs", "b", "bad", "run.json"), `{}`)
	writeFile(t, filepath.Join(root, "runs", "b", "good", "run.json"), `{}`)
	up := &fakeUploader{fail: map[string]bool{"runs/b/bad/": true}}
	rep, err := Sync(root, up)
	if err == nil || !strings.Contains(err.Error(), "runs/b/bad") {
		t.Errorf("Sync err = %v, want the failed unit named", err)
	}
	if len(rep.Uploaded) != 1 || !strings.HasPrefix(rep.Uploaded[0], "runs/b/good/") {
		t.Errorf("uploaded %v, want the good unit despite the bad one", rep.Uploaded)
	}
	if !rep.Committed {
		t.Error("records were not committed when an upload failed")
	}
}

func TestSyncRefusesSecrets(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "runs", "b", "leaky", "logs", "agent.log"), "env: OPENROUTER_API_KEY=sk-or-v1-abcdef\n")
	up := &fakeUploader{}
	_, err := Sync(root, up)
	if err == nil || !strings.Contains(err.Error(), "API key") {
		t.Errorf("Sync err = %v, want a refusal naming the API key", err)
	}
	if len(up.keys) > 1 { // index.jsonl may go; the unit must not
		for _, k := range up.keys {
			if strings.HasPrefix(k, "runs/b/leaky/") {
				t.Errorf("uploaded a unit holding a secret: %s", k)
			}
		}
	}
}

func TestScanWriterAcrossBoundaries(t *testing.T) {
	w := &scanWriter{}
	_, _ = w.Write([]byte("prefix sk-o"))
	_, _ = w.Write([]byte("r-v1-xyz"))
	if w.found == "" {
		t.Error("a key split across two writes was not found")
	}
}
