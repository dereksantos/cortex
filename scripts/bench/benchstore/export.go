package main

import (
	"bufio"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/dereksantos/cortex/internal/benchstore"
)

// export writes the trend view's three tables from the store's v1 files:
//
//	runs.csv      one row per logical run (bench, harness, model, commit, kind, counts, rate, cost)
//	failures.csv  run × failure_class → count
//	tasks.csv     one row per task attempt (status, failure class, tokens, wall)
//
// A "logical run" folds the 2026-10-08 pairing layout, where the overnight
// script gave each mini-SWE-agent instance its own run dir
// (<batch>/mini-<instance>/mini), back into one run per harness. Paths and
// host names are left out: the tables are for graphing, not provenance (the
// store keeps that).
func export(root, outDir string) error {
	entries, err := benchstore.ReadIndex(root)
	if err != nil {
		return err
	}
	type agg struct {
		id, batch, arm, bench, harness, model, commit, kind, started, labels string
		dirty                                                                bool
		s                                                                    benchstore.Summary
	}
	groups := map[string]*agg{}
	var tasks [][]string
	for _, e := range benchstore.Latest(entries) {
		harness := e.Labels["harness"]
		if harness == "" {
			harness = "cortex"
		}
		rows, run := readRun(e.Dir)
		ids := make([]string, 0, len(rows))
		for _, r := range rows {
			ids = append(ids, r.TaskID)
		}
		gid, batch, arm := logicalRun(e.RunID, ids, harness)
		key := e.Bench + "\x00" + gid
		g := groups[key]
		if g == nil {
			g = &agg{id: gid, batch: batch, arm: arm, bench: e.Bench, harness: harness, started: e.StartedAt,
				commit: short(e.Commit), dirty: e.Dirty, kind: kindOf(e.RunID), labels: labelString(e.Labels)}
			groups[key] = g
		}
		if e.StartedAt != "" && (g.started == "" || e.StartedAt < g.started) {
			g.started = e.StartedAt
		}
		if g.model == "" {
			g.model = run.Models["code"]
			if g.model == "" {
				g.model = e.Model
			}
		}
		for _, r := range rows {
			tasks = append(tasks, []string{g.id, g.batch, e.Bench, harness, r.TaskID, r.Status, r.FailureClass,
				strconv.FormatInt(r.TokensIn, 10), fmt.Sprintf("%.1f", float64(r.WallMs)/60000), g.arm, key})
			g.s.N++
			switch r.Status {
			case benchstore.StatusPass:
				g.s.Pass++
			case benchstore.StatusError:
				g.s.Skipped++
			default:
				g.s.Fail++
			}
			if r.FailureClass != "" {
				if g.s.Classes == nil {
					g.s.Classes = map[string]int{}
				}
				g.s.Classes[r.FailureClass]++
			}
			g.s.TokensIn += r.TokensIn
			g.s.TokensOut += r.TokensOut
			g.s.CostUSD += r.CostUSD
			g.s.WallSec += float64(r.WallMs) / 1000
		}
	}

	keys := make([]string, 0, len(groups))
	for k := range groups {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		a, b := groups[keys[i]], groups[keys[j]]
		if a.started != b.started {
			return a.started < b.started
		}
		return a.id < b.id
	})
	// The last two cells of a task row were the arm and its group key: swap
	// the key for the group's model, now that every member has been read.
	for _, t := range tasks {
		t[len(t)-1] = groups[t[len(t)-1]].model
	}
	var runs, failures [][]string
	for _, k := range keys {
		g := groups[k]
		rate := ""
		if g.s.Pass+g.s.Fail > 0 {
			rate = strconv.FormatFloat(g.s.PassRate(), 'f', 4, 64)
		}
		commit := g.commit
		if g.dirty {
			commit += "+"
		}
		runs = append(runs, []string{g.id, g.batch, g.bench, g.harness, g.model, g.started, commit, g.kind,
			strconv.Itoa(g.s.N), strconv.Itoa(g.s.Pass), strconv.Itoa(g.s.Fail), strconv.Itoa(g.s.Skipped), rate,
			strconv.FormatInt(g.s.TokensIn, 10), strconv.FormatInt(g.s.TokensOut, 10),
			strconv.FormatFloat(g.s.CostUSD, 'f', 4, 64), fmt.Sprintf("%.1f", g.s.WallSec/60), g.labels, g.arm})
		classes := make([]string, 0, len(g.s.Classes))
		for c := range g.s.Classes {
			classes = append(classes, c)
		}
		sort.Strings(classes)
		for _, c := range classes {
			failures = append(failures, []string{g.id, g.bench, g.harness, c, strconv.Itoa(g.s.Classes[c]), g.batch, g.arm})
		}
	}

	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return err
	}
	for _, f := range []struct {
		name   string
		header []string
		rows   [][]string
	}{
		{"runs.csv", []string{"run", "batch", "bench", "harness", "model", "started_at", "commit", "kind",
			"tasks", "pass", "fail", "error", "pass_rate", "tokens_in", "tokens_out", "cost_usd", "wall_min", "labels", "arm"}, runs},
		{"failures.csv", []string{"run", "bench", "harness", "failure_class", "count", "batch", "arm"}, failures},
		{"tasks.csv", []string{"run", "batch", "bench", "harness", "task", "status", "failure_class", "tokens_in", "wall_min", "arm", "model"}, tasks},
	} {
		if err := writeCSVFile(filepath.Join(outDir, f.name), f.header, f.rows); err != nil {
			return err
		}
		fmt.Printf("wrote %s (%d rows)\n", filepath.Join(outDir, f.name), len(f.rows))
	}
	return nil
}

// logicalRun names the logical run a stored run belongs to, its batch, and its
// arm. Runners that give every instance its own run dir (the overnight pair's
// <batch>/mini-<instance>/mini, the baseline's <batch>/cortex-<instance>) are
// folded back into one run per arm by dropping the instance id from the path
// (and a segment it leaves repeated: mini/mini → mini). An instance id is one
// of the run's own task ids, or — for a run with no results yet — a segment
// shaped like one (<arm>-owner__repo-N, or the bare id). The batch is the first path segment;
// the arm is the rest of the folded path, or the harness when there is none.
func logicalRun(runID string, taskIDs []string, harness string) (id, batch, arm string) {
	var segs []string
	for _, seg := range strings.Split(runID, "/") {
		if len(taskIDs) == 1 {
			seg = strings.Replace(seg, "-"+taskIDs[0], "", 1)
			seg = strings.Replace(seg, taskIDs[0], "", 1)
		} else if len(taskIDs) == 0 {
			seg = armWithInstance.ReplaceAllString(seg, "$1")
			seg = bareInstance.ReplaceAllString(seg, "")
		}
		if seg == "" || (len(segs) > 0 && segs[len(segs)-1] == seg) {
			continue
		}
		segs = append(segs, seg)
	}
	if len(segs) == 0 {
		return runID, runID, harness
	}
	id, batch, arm = strings.Join(segs, "/"), segs[0], strings.Join(segs[1:], "/")
	if arm == "" {
		arm = harness
	}
	return id, batch, arm
}

// armWithInstance is a segment "<arm>-<owner__repo-N>" (the arm has no dash;
// an owner may: pytest-dev, scikit-learn); bareInstance a segment that is
// only an instance id.
var (
	armWithInstance = regexp.MustCompile(`^([A-Za-z0-9]+)-[A-Za-z0-9_.-]*__[A-Za-z0-9_.-]+$`)
	bareInstance    = regexp.MustCompile(`^[A-Za-z0-9_.-]*__[A-Za-z0-9_.-]+$`)
)

// kindOf tells a measured run from a probe or a run cut short by a harness
// bug, so trend lines don't mix them; it goes by the names runs were given.
func kindOf(runID string) string {
	id := strings.ToLower(runID)
	switch {
	case strings.Contains(id, "aborted"):
		return "aborted"
	case strings.Contains(id, "probe") || strings.Contains(id, "smoke"):
		return "probe"
	}
	return "run"
}

func readRun(dir string) ([]benchstore.ResultV1, benchstore.RunV1) {
	var run benchstore.RunV1
	if b, err := os.ReadFile(filepath.Join(dir, benchstore.RunFileV1)); err == nil {
		_ = json.Unmarshal(b, &run)
	}
	f, err := os.Open(filepath.Join(dir, benchstore.ResultsFileV1))
	if err != nil {
		return nil, run
	}
	defer f.Close()
	var rows []benchstore.ResultV1
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<26)
	for sc.Scan() {
		var r benchstore.ResultV1
		if json.Unmarshal(sc.Bytes(), &r) == nil {
			rows = append(rows, r)
		}
	}
	return rows, run
}

func writeCSVFile(path string, header []string, rows [][]string) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	w := csv.NewWriter(f)
	_ = w.Write(header)
	_ = w.WriteAll(rows)
	w.Flush()
	return w.Error()
}

func short(commit string) string {
	if len(commit) > 7 {
		return commit[:7]
	}
	return commit
}

func labelString(l map[string]string) string {
	var parts []string
	for k, v := range l {
		if k == "harness" {
			continue
		}
		parts = append(parts, k+"="+v)
	}
	sort.Strings(parts)
	return strings.Join(parts, ";")
}
