package benchstore

import (
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
)

// evidenceDirs are subdirectories runners fill with per-task evidence. A
// run.json found under one belongs to a tool the runner invoked (the official
// SWE-bench harness writes its own), not to a run of ours.
var evidenceDirs = map[string]bool{
	"eval": true, "logs": true, "work": true, "cortex-home": true,
	"instances": true, "trajs": true, "transcripts": true,
}

// Discover walks <root>/runs and returns one entry per run directory (a dir
// holding run.json), summarized from its results.jsonl. It reads the common
// run.json keys every runner here writes: run_id, started_at, ended_at,
// model, cortex_commit, cortex_dirty, harness.
func Discover(root string) ([]Entry, error) {
	runs := filepath.Join(root, "runs")
	var out []Entry
	err := filepath.WalkDir(runs, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			if os.IsNotExist(err) {
				return filepath.SkipAll
			}
			return err
		}
		if d.IsDir() && evidenceDirs[d.Name()] {
			return filepath.SkipDir
		}
		if d.IsDir() || d.Name() != "run.json" {
			return nil
		}
		dir := filepath.Dir(p)
		rel, err := filepath.Rel(runs, dir)
		if err != nil {
			return nil
		}
		bench, runID := splitFirst(rel)
		if runID == "" {
			return nil
		}
		var meta struct {
			StartedAt string `json:"started_at"`
			EndedAt   string `json:"ended_at"`
			Model     string `json:"model"`
			Commit    string `json:"cortex_commit"`
			Dirty     bool   `json:"cortex_dirty"`
			Harness   string `json:"harness"`
		}
		if b, err := os.ReadFile(p); err == nil {
			_ = json.Unmarshal(b, &meta)
		}
		harness := meta.Harness
		if harness == "" {
			harness = "cortex"
		}
		e := Entry{
			Event:     "reindex",
			Bench:     bench,
			RunID:     runID,
			Dir:       dir,
			StartedAt: meta.StartedAt,
			EndedAt:   meta.EndedAt,
			Commit:    meta.Commit,
			Dirty:     meta.Dirty,
			Model:     meta.Model,
			Labels:    map[string]string{"harness": harness},
		}
		if s, err := SummarizeResults(filepath.Join(dir, "results.jsonl")); err == nil && s.N > 0 {
			e.Summary = &s
		}
		out = append(out, e)
		return filepath.SkipDir // a run's own subdirs are its evidence
	})
	sort.Slice(out, func(i, j int) bool { return out[i].Dir < out[j].Dir })
	return out, err
}

// Reindex appends a "reindex" event for every run on disk whose latest index
// event is not "finish" — runs copied in from elsewhere, or runs that crashed
// before finishing. Append-only: it never rewrites or drops index lines.
// Returns the entries it appended.
func Reindex(root string) ([]Entry, error) {
	found, err := Discover(root)
	if err != nil {
		return nil, err
	}
	existing, err := ReadIndex(root)
	if err != nil {
		return nil, err
	}
	finished := map[string]bool{}
	for _, e := range Latest(existing) {
		if e.Event == "finish" || e.Event == "reindex" {
			finished[e.Bench+"\x00"+e.RunID] = true
		}
	}
	var added []Entry
	for _, e := range found {
		if finished[e.Bench+"\x00"+e.RunID] {
			continue
		}
		if err := AppendIndex(root, e); err != nil {
			return added, err
		}
		added = append(added, e)
	}
	return added, nil
}

func splitFirst(rel string) (first, rest string) {
	rel = filepath.ToSlash(rel)
	for i := 0; i < len(rel); i++ {
		if rel[i] == '/' {
			return rel[:i], rel[i+1:]
		}
	}
	return rel, ""
}
