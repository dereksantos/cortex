package codebase

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// BaselineRow is one row in a baseline snapshot — the persisted shape
// of a single fixture run. Designed so slice-4's --compare can diff two
// runs by FixtureID without reaching back into dag_traces.jsonl.
type BaselineRow struct {
	FixtureID    string    `json:"fixture_id"`
	Group        string    `json:"group"`
	Eval         string    `json:"eval"`
	Project      string    `json:"project"`
	Language     string    `json:"language,omitempty"`
	Timestamp    string    `json:"timestamp"`
	GitCommitSHA string    `json:"git_commit_sha,omitempty"`
	Model        string    `json:"model,omitempty"`
	JudgeModel   string    `json:"judge_model,omitempty"`
	WallTimeMs   int64     `json:"wall_time_ms"`
	Metrics      Metrics   `json:"metrics"`
	Bounds       []Bound   `json:"bounds"`
	Pass         bool      `json:"pass"`
	// Invalid marks a harness failure (killed/timed-out subprocess or
	// empty answer) rather than a quality failure. Invalid rows are
	// excluded from pass-rate denominators and never count as a
	// regression — an infra stall isn't a model regression.
	Invalid       bool   `json:"invalid,omitempty"`
	InvalidReason string `json:"invalid_reason,omitempty"`
	JudgePass     *bool  `json:"judge_pass,omitempty"`
	JudgeReason  string    `json:"judge_reason,omitempty"`
	CortexExit   string    `json:"cortex_exit,omitempty"`
	AnswerSample string    `json:"answer_sample,omitempty"` // first 400 chars; full text stays in session.jsonl
	Captured     time.Time `json:"-"`                       // populated on load
}

// BaselineDir returns the canonical directory baselines write to under
// the project's .cortex/db root. One sub-directory per git commit so
// the slice-4 dashboard's "compare to ref" can resolve a ref to a
// directory in one stat.
func BaselineDir(workdir string) string {
	return filepath.Join(workdir, ".cortex", "db", "eval_baselines")
}

// WriteBaseline writes one JSONL snapshot for the given commit (or a
// timestamp when commit is empty). Returns the path written. Each row
// is one fixture's outcome.
//
// Append-only: a re-run with the same commit doesn't overwrite — it
// appends a new file with a timestamp suffix. The "latest" baseline for
// a commit is the lexically-last entry, which matches mtime ordering
// because the suffix is RFC3339Nano.
func WriteBaseline(workdir, commit string, rows []BaselineRow) (string, error) {
	if commit == "" {
		commit = "uncommitted"
	}
	dir := filepath.Join(BaselineDir(workdir), commit)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("mkdir baseline dir: %w", err)
	}
	stamp := time.Now().UTC().Format("20060102T150405.000000000Z")
	path := filepath.Join(dir, stamp+".jsonl")
	f, err := os.Create(path)
	if err != nil {
		return "", fmt.Errorf("create %s: %w", path, err)
	}
	defer f.Close()
	w := bufio.NewWriter(f)
	for _, r := range rows {
		b, err := json.Marshal(r)
		if err != nil {
			return path, fmt.Errorf("marshal %s: %w", r.FixtureID, err)
		}
		if _, err := w.Write(append(b, '\n')); err != nil {
			return path, fmt.Errorf("write: %w", err)
		}
	}
	if err := w.Flush(); err != nil {
		return path, fmt.Errorf("flush: %w", err)
	}
	if err := f.Sync(); err != nil {
		return path, fmt.Errorf("sync: %w", err)
	}
	return path, nil
}

// LoadBaseline reads the most recent baseline JSONL under
// <workdir>/.cortex/db/eval_baselines/<ref>/. The ref may be a git
// commit SHA (full or short), the literal "HEAD" (resolved via git), or
// "latest" (newest baseline across all commits).
//
// Returns (rows, "" or path, nil) on success. Empty rows + nil error
// means no baseline exists for that ref yet — caller decides whether
// that's an error.
func LoadBaseline(workdir, ref string) ([]BaselineRow, string, error) {
	root := BaselineDir(workdir)
	if _, err := os.Stat(root); err != nil {
		if os.IsNotExist(err) {
			return nil, "", nil
		}
		return nil, "", err
	}
	var subdir string
	switch ref {
	case "", "latest":
		// newest across all commits
		entries, err := os.ReadDir(root)
		if err != nil {
			return nil, "", err
		}
		newest := ""
		var newestTime time.Time
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			files, _ := filepath.Glob(filepath.Join(root, e.Name(), "*.jsonl"))
			for _, f := range files {
				if st, err := os.Stat(f); err == nil && st.ModTime().After(newestTime) {
					newest = f
					newestTime = st.ModTime()
				}
			}
		}
		if newest == "" {
			return nil, "", nil
		}
		rows, err := readBaselineFile(newest)
		return rows, newest, err
	case "HEAD":
		sha, err := resolveGitRef("HEAD")
		if err != nil {
			return nil, "", err
		}
		subdir = filepath.Join(root, sha)
	default:
		// Try as-is first; if not present, attempt to resolve via git.
		if st, err := os.Stat(filepath.Join(root, ref)); err == nil && st.IsDir() {
			subdir = filepath.Join(root, ref)
		} else {
			sha, err := resolveGitRef(ref)
			if err != nil {
				return nil, "", fmt.Errorf("ref %q not a baseline dir and not a git ref: %w", ref, err)
			}
			subdir = filepath.Join(root, sha)
		}
	}
	files, err := filepath.Glob(filepath.Join(subdir, "*.jsonl"))
	if err != nil {
		return nil, "", err
	}
	if len(files) == 0 {
		return nil, "", nil
	}
	sort.Strings(files)
	path := files[len(files)-1]
	rows, err := readBaselineFile(path)
	return rows, path, err
}

func readBaselineFile(path string) ([]BaselineRow, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	st, _ := f.Stat()
	captured := time.Now()
	if st != nil {
		captured = st.ModTime()
	}
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)
	var rows []BaselineRow
	for scanner.Scan() {
		var r BaselineRow
		if err := json.Unmarshal(scanner.Bytes(), &r); err != nil {
			continue
		}
		r.Captured = captured
		rows = append(rows, r)
	}
	return rows, scanner.Err()
}

// Diff is one row of the "compare" report — a side-by-side of a single
// fixture's prev vs current outcome. The dashboard flags Regressed
// (prev=pass, curr=fail) and Improved (the reverse), and surfaces
// per-metric % changes that exceed RegressionThreshold.
type Diff struct {
	FixtureID  string
	Prev       *BaselineRow
	Curr       *BaselineRow
	Regressed  bool
	Improved   bool
	Invalid    bool // curr or prev was a harness failure — not a model delta
	BigChanges []MetricChange
}

// MetricChange is a single per-metric delta. Pct is signed: positive
// means curr > prev. Magnitudes ≥ RegressionThreshold (0.10 → 10%) are
// flagged as "big".
type MetricChange struct {
	Name    string
	Prev    float64
	Curr    float64
	Delta   float64
	Pct     float64
	IsBig   bool
	Concern string // "regression" | "improvement" | ""
}

// RegressionThreshold is the |Pct| above which a metric change is
// flagged as a big change in --compare output. The doc names 10%; we
// surface anything ≥ that as a row in the diff report.
const RegressionThreshold = 0.10

// Compare diffs two baseline snapshots by fixture id. The returned
// slice is sorted by id for stable presentation.
func Compare(prev, curr []BaselineRow) []Diff {
	prevByID := map[string]*BaselineRow{}
	for i := range prev {
		prevByID[prev[i].FixtureID] = &prev[i]
	}
	currByID := map[string]*BaselineRow{}
	for i := range curr {
		currByID[curr[i].FixtureID] = &curr[i]
	}
	ids := map[string]struct{}{}
	for id := range prevByID {
		ids[id] = struct{}{}
	}
	for id := range currByID {
		ids[id] = struct{}{}
	}
	idList := make([]string, 0, len(ids))
	for id := range ids {
		idList = append(idList, id)
	}
	sort.Strings(idList)

	var diffs []Diff
	for _, id := range idList {
		d := Diff{FixtureID: id, Prev: prevByID[id], Curr: currByID[id]}
		if d.Prev != nil && d.Curr != nil {
			// A cell that went INVALID this run is a harness failure, not
			// a model regression — don't flag it as regressed/improved.
			// Same if the PREVIOUS baseline was invalid (no trustworthy
			// floor to compare against).
			d.Invalid = d.Curr.Invalid || d.Prev.Invalid
			if !d.Invalid {
				d.Regressed = d.Prev.Pass && !d.Curr.Pass
				d.Improved = !d.Prev.Pass && d.Curr.Pass
			}
			d.BigChanges = bigMetricChanges(d.Prev.Metrics, d.Curr.Metrics)
		}
		diffs = append(diffs, d)
	}
	return diffs
}

func bigMetricChanges(prev, curr Metrics) []MetricChange {
	tracked := []struct {
		name string
		p, c float64
	}{
		{"hop_count", float64(prev.HopCount), float64(curr.HopCount)},
		{"read_count", float64(prev.ReadCount), float64(curr.ReadCount)},
		{"need_more", float64(prev.NeedMore), float64(curr.NeedMore)},
		{"citation_rate", prev.CitationRate, curr.CitationRate},
		{"hedge_count", float64(prev.HedgeCount), float64(curr.HedgeCount)},
		{"budget_tokens", float64(prev.BudgetTokens), float64(curr.BudgetTokens)},
	}
	var out []MetricChange
	for _, t := range tracked {
		mc := MetricChange{Name: t.name, Prev: t.p, Curr: t.c, Delta: t.c - t.p}
		if t.p != 0 {
			mc.Pct = (t.c - t.p) / t.p
		} else if t.c != 0 {
			mc.Pct = 1.0
		}
		if absf(mc.Pct) >= RegressionThreshold {
			mc.IsBig = true
			switch {
			case t.name == "hedge_count" || t.name == "need_more":
				// higher = worse
				if mc.Delta > 0 {
					mc.Concern = "regression"
				} else {
					mc.Concern = "improvement"
				}
			case t.name == "citation_rate":
				// higher = better
				if mc.Delta > 0 {
					mc.Concern = "improvement"
				} else {
					mc.Concern = "regression"
				}
			default:
				// neutral metrics (hop_count, budget_tokens) — flag
				// the change without a polarity label.
				mc.Concern = ""
			}
			out = append(out, mc)
		}
	}
	return out
}

// FormatCompareReport returns the human-readable text rendering of a
// diff report. Used by the CLI's --compare path; tests exercise it via
// the Diff struct directly.
func FormatCompareReport(diffs []Diff) string {
	var b strings.Builder
	regressed, improved, unchanged := 0, 0, 0
	for _, d := range diffs {
		switch {
		case d.Regressed:
			regressed++
		case d.Improved:
			improved++
		default:
			unchanged++
		}
	}
	fmt.Fprintf(&b, "=== codebase eval compare ===\n")
	fmt.Fprintf(&b, "regressed: %d   improved: %d   unchanged: %d   total: %d\n\n",
		regressed, improved, unchanged, len(diffs))
	for _, d := range diffs {
		status := "ok"
		switch {
		case d.Prev == nil:
			status = "NEW"
		case d.Curr == nil:
			status = "DROPPED"
		case d.Regressed:
			status = "REGRESSED"
		case d.Improved:
			status = "IMPROVED"
		}
		fmt.Fprintf(&b, "%-12s %s\n", status, d.FixtureID)
		for _, mc := range d.BigChanges {
			arrow := "→"
			fmt.Fprintf(&b, "             %-15s %v %s %v  (%+.0f%%) [%s]\n",
				mc.Name, mc.Prev, arrow, mc.Curr, mc.Pct*100, mc.Concern)
		}
	}
	return b.String()
}

// CurrentGitSHA returns the short HEAD SHA for the workdir or "" on
// failure (not a git repo, git absent, detached state). Used by the
// CLI to pick the baseline directory.
func CurrentGitSHA() string {
	sha, err := resolveGitRef("HEAD")
	if err != nil {
		return ""
	}
	if len(sha) > 12 {
		return sha[:12]
	}
	return sha
}

func resolveGitRef(ref string) (string, error) {
	if ref == "" {
		return "", errors.New("empty ref")
	}
	out, err := exec.Command("git", "rev-parse", ref).Output()
	if err != nil {
		return "", err
	}
	sha := strings.TrimSpace(string(out))
	if sha == "" {
		return "", fmt.Errorf("git rev-parse %s: empty output", ref)
	}
	if len(sha) > 12 {
		sha = sha[:12]
	}
	return sha, nil
}

func absf(f float64) float64 {
	if f < 0 {
		return -f
	}
	return f
}

// Aggregate is the one-line summary shape `cortex status --eval` emits.
type Aggregate struct {
	Total         int
	Passing       int
	Invalid       int // harness failures, excluded from the pass denominator
	Regressed     int
	Improved      int
	CitationP50   float64
	BudgetTokenP50 int
	Source        string // path to the baseline file the aggregate summarized
	Captured      time.Time
}

// Scoreable returns the pass-rate denominator: total cells minus
// quarantined INVALID ones. A run where INVALID dominates has a small,
// untrustworthy denominator — see Compromised.
func (a Aggregate) Scoreable() int { return a.Total - a.Invalid }

// Compromised reports whether enough cells were quarantined that the
// pass rate is not trustworthy (a fleet stall / mass timeout, not a real
// result). Threshold: more than 15% of cells INVALID.
func (a Aggregate) Compromised() bool {
	return a.Total > 0 && float64(a.Invalid) > 0.15*float64(a.Total)
}

// Summarize builds the dashboard one-line aggregate from a baseline
// row set. Used by --eval surfacing.
func Summarize(rows []BaselineRow) Aggregate {
	a := Aggregate{Total: len(rows)}
	if len(rows) == 0 {
		return a
	}
	rates := make([]float64, 0, len(rows))
	budgets := make([]int, 0, len(rows))
	var newest time.Time
	for _, r := range rows {
		if r.Invalid {
			a.Invalid++
			continue // quarantined: not counted as pass or fail
		}
		if r.Pass {
			a.Passing++
		}
		if r.Metrics.CitationRate > 0 || r.Metrics.ClaimCount > 0 {
			rates = append(rates, r.Metrics.CitationRate)
		}
		if r.Metrics.BudgetTokens > 0 {
			budgets = append(budgets, r.Metrics.BudgetTokens)
		}
		if r.Captured.After(newest) {
			newest = r.Captured
		}
	}
	a.CitationP50 = medianFloat(rates)
	a.BudgetTokenP50 = medianInt(budgets)
	a.Captured = newest
	return a
}

func medianFloat(xs []float64) float64 {
	if len(xs) == 0 {
		return 0
	}
	cp := append([]float64(nil), xs...)
	sort.Float64s(cp)
	return cp[len(cp)/2]
}

func medianInt(xs []int) int {
	if len(xs) == 0 {
		return 0
	}
	cp := append([]int(nil), xs...)
	sort.Ints(cp)
	return cp[len(cp)/2]
}
