// Package benchstore is the durable home for every benchmark and eval run.
//
// Benchmark results used to land in gitignored directories inside the repo
// or inside throwaway agent worktrees, and a lot of them were lost when those
// directories went away. The rule this package enforces: a run's record lives
// OUTSIDE any git working tree, under one root, and is never overwritten.
//
// Layout (root = $CORTEX_BENCH_HOME, else ~/.cortex/benchmarks):
//
//	<root>/runs/<bench>/<run-id>/   one directory per run, created exclusively;
//	                                the runner writes run.json + results.jsonl
//	                                (+ whatever evidence it keeps) here
//	<root>/runs/.../source.patch    `git diff HEAD` when the tree under test was dirty
//	<root>/index.jsonl              append-only start/finish events, one per line —
//	                                the time series the trend views read; derivable
//	                                from runs/ via Reindex, so losing it loses nothing
//	<root>/archive/                 raw copies of older formats (kept, not indexed)
//
// The per-run directory is the source of truth; index.jsonl is a convenience
// projection of it (the same CQRS shape as internal/journal).
package benchstore

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/dereksantos/cortex/internal/fslock"
)

// EnvHome overrides the root directory.
const EnvHome = "CORTEX_BENCH_HOME"

// Root returns the benchmark root: $CORTEX_BENCH_HOME, else ~/.cortex/benchmarks.
func Root() (string, error) {
	if v := strings.TrimSpace(os.Getenv(EnvHome)); v != "" {
		return filepath.Abs(v)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("failed to resolve home dir: %w", err)
	}
	return filepath.Join(home, ".cortex", "benchmarks"), nil
}

// RunsDir is where a benchmark's run directories live under root.
func RunsDir(root, bench string) string {
	return filepath.Join(root, "runs", bench)
}

// ErrEphemeral is returned by CheckDurable for a directory inside a git
// working tree.
var ErrEphemeral = errors.New("output dir is inside a git working tree")

// CheckDurable refuses a run directory inside a git working tree. Gitignored
// dirs in a repo or an agent worktree are exactly where results were lost:
// a `git clean`, a worktree removal, or a fresh clone deletes them silently.
// The store's own git repo (Sync makes the store root one) is the exception:
// it only ever tracks records and is never cleaned.
func CheckDurable(dir string) error {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return err
	}
	storeRoot, _ := Root()
	for d := abs; ; d = filepath.Dir(d) {
		if d == storeRoot {
			// The store root's .git is the store's own; anything above it
			// still counts (a store placed inside a repo is not durable).
		} else if _, err := os.Stat(filepath.Join(d, ".git")); err == nil {
			return fmt.Errorf("%w: %s is under %s — results there are lost with a worktree removal or git clean; write under %s instead (or pass --ephemeral for a throwaway run)",
				ErrEphemeral, abs, d, "$"+EnvHome+" / ~/.cortex/benchmarks")
		}
		if parent := filepath.Dir(d); parent == d {
			return nil
		}
	}
}

// Summary is the benchmark-neutral roll-up of one run's results.v1.jsonl —
// the numbers the trend views graph. Skipped counts infra errors (status
// "error"), which PassRate excludes.
type Summary struct {
	N         int            `json:"n"`
	Pass      int            `json:"pass"`
	Fail      int            `json:"fail"`
	Skipped   int            `json:"skipped,omitempty"`
	TokensIn  int64          `json:"tokens_in,omitempty"`
	TokensOut int64          `json:"tokens_out,omitempty"`
	CostUSD   float64        `json:"cost_usd,omitempty"`
	WallSec   float64        `json:"wall_sec,omitempty"`
	Classes   map[string]int `json:"failure_classes,omitempty"`
}

// PassRate is Pass/(Pass+Fail); skipped rows are excluded. 0 for no scored rows.
func (s Summary) PassRate() float64 {
	if d := s.Pass + s.Fail; d > 0 {
		return float64(s.Pass) / float64(d)
	}
	return 0
}

// Entry is one index.jsonl line.
type Entry struct {
	Event     string            `json:"event"` // start | finish | reindex
	At        string            `json:"at"`    // when this line was written
	Bench     string            `json:"bench"`
	RunID     string            `json:"run_id"`
	Dir       string            `json:"dir"`
	StartedAt string            `json:"started_at,omitempty"`
	EndedAt   string            `json:"ended_at,omitempty"`
	Commit    string            `json:"commit,omitempty"`
	Dirty     bool              `json:"dirty,omitempty"`
	Model     string            `json:"model,omitempty"`
	Labels    map[string]string `json:"labels,omitempty"` // arm, prompt, config switches…
	Summary   *Summary          `json:"summary,omitempty"`
	Note      string            `json:"note,omitempty"`
}

// Run is an open run directory.
type Run struct {
	Root  string
	Dir   string
	start Entry
}

// BeginOptions describe a new run.
type BeginOptions struct {
	Bench string
	RunID string
	// OutRoot overrides <root>/runs/<bench> as the parent of the run dir.
	OutRoot string
	// Ephemeral skips the git-working-tree check (throwaway runs only).
	Ephemeral bool
	// SourceRepo, when set, is the repo whose tree is under test: its commit
	// and dirty state are recorded, and a dirty tree's `git diff HEAD` is
	// saved as source.patch so the exact code that ran can be rebuilt.
	SourceRepo string
	Model      string
	Labels     map[string]string
}

// Begin creates the run directory exclusively (an existing run is never
// overwritten), records the source patch, and appends a start event.
func Begin(o BeginOptions) (*Run, error) {
	if o.Bench == "" || o.RunID == "" {
		return nil, errors.New("benchstore: bench and run id are required")
	}
	root, err := Root()
	if err != nil {
		return nil, err
	}
	if err := validRunID(o.RunID); err != nil {
		return nil, err
	}
	parent := o.OutRoot
	if parent == "" {
		parent = RunsDir(root, o.Bench)
	}
	dir, err := filepath.Abs(filepath.Join(parent, o.RunID))
	if err != nil {
		return nil, err
	}
	if !o.Ephemeral {
		if err := CheckDurable(dir); err != nil {
			return nil, err
		}
	}
	if err := os.MkdirAll(filepath.Dir(dir), 0o755); err != nil {
		return nil, fmt.Errorf("failed to create %s: %w", filepath.Dir(dir), err)
	}
	if err := os.Mkdir(dir, 0o755); err != nil {
		if errors.Is(err, os.ErrExist) {
			return nil, fmt.Errorf("run dir %s already exists — runs are never overwritten; pick another --run-id", dir)
		}
		return nil, fmt.Errorf("failed to create run dir %s: %w", dir, err)
	}
	e := Entry{
		Event:     "start",
		Bench:     o.Bench,
		RunID:     o.RunID,
		Dir:       dir,
		StartedAt: now(),
		Model:     o.Model,
		Labels:    o.Labels,
	}
	if o.SourceRepo != "" {
		e.Commit, e.Dirty = gitState(o.SourceRepo)
		if e.Dirty {
			if err := saveSourcePatch(o.SourceRepo, dir); err != nil {
				// Never fail the run over it, but never drop it silently:
				// the run's record says the patch is incomplete and why.
				e.Note = "source.patch incomplete: " + err.Error()
				_ = os.WriteFile(filepath.Join(dir, "source.patch.err"), []byte(err.Error()+"\n"), 0o644)
			}
		}
	}
	r := &Run{Root: root, Dir: dir, start: e}
	if err := AppendIndex(root, e); err != nil {
		// Nothing has run yet: remove the dir so a retry with this id works.
		_ = os.RemoveAll(dir)
		return nil, err
	}
	return r, nil
}

// validRunID refuses ids that would escape runs/<bench>: absolute paths and
// any "." / ".." / empty segment. Nested ids (night-1/w32768) are fine.
func validRunID(id string) error {
	if filepath.IsAbs(id) || strings.HasPrefix(id, "/") {
		return fmt.Errorf("run id %q must be relative", id)
	}
	for _, seg := range strings.Split(filepath.ToSlash(id), "/") {
		if seg == "" || seg == "." || seg == ".." {
			return fmt.Errorf("run id %q has an empty, . or .. segment", id)
		}
	}
	return nil
}

// saveSourcePatch writes source.patch: the tracked changes against HEAD plus
// every untracked, non-ignored file as an addition, so `git apply` on the
// recorded commit rebuilds the tree that ran.
func saveSourcePatch(repo, dir string) error {
	var buf bytes.Buffer
	out, err := exec.Command("git", "-C", repo, "diff", "HEAD", "--binary").Output()
	if err != nil {
		return fmt.Errorf("git diff HEAD: %w", err)
	}
	buf.Write(out)
	list, err := exec.Command("git", "-C", repo, "ls-files", "--others", "--exclude-standard", "-z").Output()
	if err != nil {
		return fmt.Errorf("git ls-files --others: %w", err)
	}
	for _, f := range strings.Split(string(list), "\x00") {
		if f == "" {
			continue
		}
		// --no-index exits 1 when the files differ, which they always do here.
		d, err := exec.Command("git", "-C", repo, "diff", "--binary", "--no-index", "--", "/dev/null", f).Output()
		var ee *exec.ExitError
		if err != nil && (!errors.As(err, &ee) || ee.ExitCode() != 1) {
			return fmt.Errorf("git diff --no-index %s: %w", f, err)
		}
		buf.Write(d)
	}
	return os.WriteFile(filepath.Join(dir, "source.patch"), buf.Bytes(), 0o644)
}

// Finish writes the run's v1 files (run.v1.json, results.v1.jsonl) from its
// native ones and appends a finish event carrying their summary.
func (r *Run) Finish(note string) (Summary, error) {
	if err := Normalize(r.start.Bench, r.start.RunID, r.Dir, r.start.Labels); err != nil {
		return Summary{}, fmt.Errorf("failed to write the v1 result files: %w", err)
	}
	s, err := Summarize(r.Dir)
	if err != nil {
		return s, err
	}
	e := r.start
	e.Event = "finish"
	e.EndedAt = now()
	e.Summary = &s
	e.Note = note
	return s, AppendIndex(r.Root, e)
}

// AppendIndex appends one entry to <root>/index.jsonl under an exclusive
// flock and fsyncs it, so concurrent runners never interleave lines.
func AppendIndex(root string, e Entry) error {
	if e.At == "" {
		e.At = now()
	}
	b, err := json.Marshal(e)
	if err != nil {
		return fmt.Errorf("failed to encode index entry: %w", err)
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		return fmt.Errorf("failed to create bench root: %w", err)
	}
	f, err := os.OpenFile(filepath.Join(root, "index.jsonl"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("failed to open index: %w", err)
	}
	defer f.Close()
	// The lock is released when the deferred Close runs.
	if err := fslock.Lock(int(f.Fd())); err != nil {
		return fmt.Errorf("failed to lock index: %w", err)
	}
	if _, err := f.Write(append(b, '\n')); err != nil {
		return fmt.Errorf("failed to append index: %w", err)
	}
	return f.Sync()
}

// ReadIndex reads every entry of <root>/index.jsonl (missing file = none).
func ReadIndex(root string) ([]Entry, error) {
	f, err := os.Open(filepath.Join(root, "index.jsonl"))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to open index: %w", err)
	}
	defer f.Close()
	var out []Entry
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<24)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var e Entry
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			continue // one bad line never hides the rest
		}
		out = append(out, e)
	}
	return out, sc.Err()
}

// Latest folds the event log to one entry per (bench, run id): the last
// event wins, so a run that started and never finished shows as "start".
// Sorted by start time, oldest first — the order a trend graph wants.
func Latest(entries []Entry) []Entry {
	byKey := map[string]Entry{}
	for _, e := range entries {
		byKey[e.Bench+"\x00"+e.RunID] = e
	}
	out := make([]Entry, 0, len(byKey))
	for _, e := range byKey {
		out = append(out, e)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].StartedAt != out[j].StartedAt {
			return out[i].StartedAt < out[j].StartedAt
		}
		return out[i].Bench+out[i].RunID < out[j].Bench+out[j].RunID
	})
	return out
}

func truthy(v any) bool { b, ok := v.(bool); return ok && b }

func num(v any) float64 {
	f, _ := v.(float64)
	return f
}

func now() string { return time.Now().UTC().Format(time.RFC3339) }

func gitState(repo string) (commit string, dirty bool) {
	out, err := exec.Command("git", "-C", repo, "rev-parse", "HEAD").Output()
	if err != nil {
		return "", false
	}
	commit = strings.TrimSpace(string(out))
	st, err := exec.Command("git", "-C", repo, "status", "--porcelain").Output()
	return commit, err == nil && len(strings.TrimSpace(string(st))) > 0
}

// LabelFlag is a repeatable `--label key=value` flag: free-form tags (arm,
// prompt, config switch…) carried into the index so a trend view can group
// and filter runs without parsing run.json.
type LabelFlag map[string]string

// String implements flag.Value.
func (l LabelFlag) String() string {
	parts := make([]string, 0, len(l))
	for k, v := range l {
		parts = append(parts, k+"="+v)
	}
	sort.Strings(parts)
	return strings.Join(parts, ",")
}

// Set implements flag.Value.
func (l LabelFlag) Set(s string) error {
	k, v, ok := strings.Cut(s, "=")
	if !ok || strings.TrimSpace(k) == "" {
		return fmt.Errorf("label %q: want key=value", s)
	}
	l[strings.TrimSpace(k)] = strings.TrimSpace(v)
	return nil
}

// FinishByID appends a finish event for a run opened by another process (the
// `benchstore begin` / `benchstore finish` pair a shell runner uses): the
// run's latest index entry supplies its metadata, and its results.jsonl the
// summary. A run missing from the index is reported, never invented.
func FinishByID(root, bench, runID, note string) (Summary, error) {
	entries, err := ReadIndex(root)
	if err != nil {
		return Summary{}, err
	}
	var last *Entry
	for i := range entries {
		if entries[i].Bench == bench && entries[i].RunID == runID {
			last = &entries[i]
		}
	}
	if last == nil {
		return Summary{}, fmt.Errorf("run %s/%s is not in the index — was it opened with benchstore begin?", bench, runID)
	}
	r := &Run{Root: root, Dir: last.Dir, start: *last}
	r.start.Summary = nil
	return r.Finish(note)
}
