package benchstore

import (
	"encoding/json"
	"io/fs"
	"maps"
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
		// A run copied in from another host carries the labels it was begun
		// with in its own run.v1.json; this host's index has never seen them.
		var v1 struct {
			Labels map[string]string `json:"labels"`
		}
		if b, err := os.ReadFile(filepath.Join(dir, RunFileV1)); err == nil &&
			json.Unmarshal(b, &v1) == nil && len(v1.Labels) > 0 {
			e.Labels = v1.Labels
		}
		if s, err := Summarize(dir); err == nil && s.N > 0 {
			e.Summary = &s
		}
		out = append(out, e)
		return filepath.SkipDir // a run's own subdirs are its evidence
	})
	sort.Slice(out, func(i, j int) bool { return out[i].Dir < out[j].Dir })
	return out, err
}

// Reindex brings every run on disk up to date: it (re)writes each run's v1
// files from its native ones, then appends a "reindex" event for any run
// whose latest index entry is missing, unfinished, or carries a different
// summary — runs copied in from elsewhere, runs that crashed before
// finishing, runs rescored after the fact. The index stays append-only (no
// line is rewritten or dropped); the v1 files are rewritten, being a pure
// function of each run's native files. A field the run's files don't carry
// (an older run.json without started_at, say) is carried forward from the
// run's previous index entry rather than blanked. Returns the entries it
// appended.
func Reindex(root string) ([]Entry, error) {
	found, err := Discover(root)
	if err != nil {
		return nil, err
	}
	existing, err := ReadIndex(root)
	if err != nil {
		return nil, err
	}
	latest := map[string]Entry{}
	for _, e := range Latest(existing) {
		latest[e.Bench+"\x00"+e.RunID] = e
	}
	var added []Entry
	for _, e := range found {
		prev, seen := latest[e.Bench+"\x00"+e.RunID]
		if seen {
			// Labels given at Begin outrank the discovered ones, key by key;
			// a key only the run's own run.v1.json carries is kept.
			merged := map[string]string{}
			for k, v := range e.Labels {
				merged[k] = v
			}
			for k, v := range prev.Labels {
				merged[k] = v
			}
			e.Labels = merged
			carryForward(&e, prev)
		}
		if err := Normalize(e.Bench, e.RunID, e.Dir, e.Labels); err != nil {
			return added, err
		}
		if s, err := Summarize(e.Dir); err == nil && s.N > 0 {
			e.Summary = &s
		}
		if seen && prev.Event != "start" && sameSummary(prev.Summary, e.Summary) && maps.Equal(prev.Labels, e.Labels) {
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

func sameSummary(a, b *Summary) bool {
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	return string(x) == string(y)
}

// carryForward fills e's empty metadata from prev, the run's earlier entry.
func carryForward(e *Entry, prev Entry) {
	if e.StartedAt == "" {
		e.StartedAt = prev.StartedAt
	}
	if e.EndedAt == "" {
		e.EndedAt = prev.EndedAt
	}
	if e.Commit == "" {
		e.Commit, e.Dirty = prev.Commit, prev.Dirty
	}
	if e.Model == "" {
		e.Model = prev.Model
	}
}
