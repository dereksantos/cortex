// testwatch.go implements the turn-level "tests changed" receipt (issue
// #141): the harness snapshots the before-content of the files a turn's
// write_file / edit_file / remove_path calls touch (bounded, best-effort,
// at turn start) and, after the turn, runs internal/testguard over the
// diffs. A turn that removed test definitions or substantially shrunk a
// test file gets a "tests changed: …" line in its capture summary — the
// model then has to account for the loss. The scan never blocks the turn:
// an unreadable file, a size cap, or a vanished snapshot degrades to
// silence.
package main

import (
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/dereksantos/cortex/internal/testguard"
	"github.com/dereksantos/cortex/internal/tools"
)

// testwatchTouchNames are the tool calls whose success mutates a file the
// scan must see the before-side of: the two writers and the deleter. bash
// is NOT in this set — a command names no file — but a bash call can still
// mutate any file in the workspace (the #127 incident was a test file
// sed'd away), so armTestwatch snapshots the project's test-named files
// whenever a bash call is about to run.
var testwatchTouchNames = map[string]bool{
	tools.FunctionWriteFile: true,
	tools.FunctionEditFile:  true,
	tools.FunctionRemove:    true,
}

// testwatchTouchedPath returns the path a mutating tool call names, or ""
// when the call is not one of the scan's tool names (or its path arg is
// missing) — the hook the coder dispatcher arms the snapshot with.
func testwatchTouchedPath(call ToolCall) string {
	if !testwatchTouchNames[call.Function.Name] {
		return ""
	}
	p, err := call.StringArg("path")
	if err != nil {
		return ""
	}
	return p
}

// armTestwatch arms the before-side of the workspace's test-named files in
// anticipation of a bash call. bash names no file — a command can mutate
// ANY file in the workspace, and the #127 incident this receipt exists
// for was exactly a test file sed'd away — so without a per-call path to
// snapshot, the receipt would be blind to the headline case. armTestwatch
// does a single walk of the workdir (skipping .git, .cortex, vendor,
// node_modules) and stores a compact per-file baseline (testguard.Baseline:
// line count + test-definition lines) for EVERY test-named file it finds —
// not the full content, which a 32-file / 1 MiB budget cannot cover in a
// real repo. The turn-end receipt (testwatchReceipt) then reads the
// file's current content and diffs it against the baseline to recover
// removed definitions and shrink.
//
// The baseline store (cs.testwatchBash) is a SEPARATE budget from the
// full-content snapshots (cs.testwatch) that touchFile enforces, so a turn
// that runs any bash command (e.g. `go test`) before a write_file/edit_file
// on a test file cannot starve the named-tool snapshot — the #127 incident
// still produces a receipt for the write_file arm, and vice versa.
//
// Called from coderDispatcher BEFORE the bash command runs, so the
// before-side is captured first. The walk runs ONCE per turn (the first
// bash call arms it; subsequent bash calls in the same turn are no-ops),
// because a turn's bash calls all see the same pre-turn workspace state —
// re-walking would just re-read the same files.
func (cs *CortexSession) armTestwatch() {
	wd := cs.Workdir()
	if wd == "" {
		return
	}
	// Once per turn: the workspace's test files were already baselined by
	// an earlier bash call in this turn.
	if cs.testwatchBashArmed {
		return
	}
	cs.testwatchBashArmed = true
	cs.testwatchBash = map[string]testguard.Baseline{}

	var walk func(dir, rel string)
	walk = func(dir, rel string) {
		if len(cs.testwatchBash) >= testwatchMaxBashBaselines {
			return
		}
		ents, err := os.ReadDir(dir)
		if err != nil {
			return
		}
		for _, e := range ents {
			name := e.Name()
			if name == ".git" || name == ".cortex" || name == "vendor" || name == "node_modules" {
				continue
			}
			childRel := name
			if rel != "" {
				childRel = rel + "/" + name
			}
			if e.IsDir() {
				walk(filepath.Join(dir, name), childRel)
				if len(cs.testwatchBash) >= testwatchMaxBashBaselines {
					return
				}
				continue
			}
			if !testguard.IsTestPath(childRel) {
				continue
			}
			if _, seen := cs.testwatchBash[childRel]; seen {
				continue
			}
			// A file already snapshotted by a named-tool call this turn
			// has the full content in cs.testwatch — the receipt diffs
			// that against the live read. Baselining it too would double-
			// report, so skip it here.
			if _, already := cs.testwatch[childRel]; already {
				continue
			}
			// The cap is enforced PER-FILE, not just at recursion entry:
			// a flat directory with more test files than the cap must
			// stop at the cap, not keep reading past it.
			if len(cs.testwatchBash) >= testwatchMaxBashBaselines {
				return
			}
			data, err := os.ReadFile(filepath.Join(dir, name))
			if err != nil {
				continue
			}
			cs.testwatchBash[childRel] = testguard.MakeBaseline(childRel, string(data))
		}
	}
	walk(wd, "")
}

// testwatchBounds sizes the per-turn before-side the receipt diffs
// against. Two SEPARATE budgets cover the two arms:
//
//   - touchFile (named-tool arm: write_file / edit_file / remove_path)
//     stores the full content of the files those calls name, within
//     testwatchMaxFiles / testwatchMaxBytes. These calls are few and
//     name their targets, so a small content budget is enough — but it
//     MUST be enough for every named call, which is why the bash arm
//     (below) gets its own store and cannot starve it.
//
//   - armTestwatch (bash arm) stores a compact per-file baseline
//     (testguard.Baseline: line count + definition lines, a few hundred
//     bytes each) for every test-named file in the workspace, up to
//     testwatchMaxBashBaselines. A real repo has more test files than a
//     32-file / 1 MiB content budget can hold, but a {lines, defs} pair
//     per file fits hundreds of them.
//
// testwatchSampleCap bounds how many removed-definition lines the
// receipt shows per file.
const (
	testwatchMaxBytes         = 1 << 20 // 1 MiB total across all named-tool snapshots
	testwatchMaxFiles         = 32      // files in the named-tool snapshot
	testwatchMaxBashBaselines = 512     // baselined files in the bash arm
	testwatchSampleCap        = 3       // sample test-definition lines shown per file in the receipt
)

// testwatchSnapshot is the per-turn before-side of the scan: the path a
// mutating tool call named (as the model sees it) plus the file's content
// read at turn start.
type testwatchSnapshot struct {
	display string
	abs     string
	before  string
}

// touchFile records that a turn's tooling will mutate path. Called from
// the coder dispatcher (before each write_file/edit_file/remove_path), so
// the before-side is captured BEFORE the tool runs. Missing files,
// non-files, reads over the budget, and budget exhaustion all degrade to
// "no snapshot" — the receipt simply cannot report on them.
//
// An absolute path INSIDE the session's workdir is normalized to that
// workdir-relative form before snapshotting, so a model that edits
// /repo/foo_test.go by absolute path is covered just like /repo/foo_test.go
// via "foo_test.go"; only paths OUTSIDE the workspace (or with no workdir
// anchor) are skipped — those the tool call itself would already refuse or
// act on in a way the receipt is not meant to chase.
func (cs *CortexSession) touchFile(path string) {
	if strings.TrimSpace(path) == "" {
		return
	}
	abs := path
	if wd := cs.Workdir(); wd != "" {
		if filepath.IsAbs(path) {
			// Absolute path inside the workdir: make it relative so the
			// snapshot keys and displays consistently with the relative
			// form the model would more often use. Outside the workdir,
			// skip — the receipt is scoped to the workspace.
			rel, relErr := filepath.Rel(wd, path)
			if relErr != nil || rel == "" || strings.HasPrefix(rel, "..") {
				return
			}
			path = rel
		} else {
			abs = filepath.Join(wd, path)
		}
	} else if filepath.IsAbs(path) {
		// No workdir anchor (CWD session): an absolute path is out of the
		// workspace's reach, so there's no before-side to snapshot.
		return
	}
	// Normalize the key with filepath.Clean (+ ToSlash): a model that
	// names "./pkg/x_test.go" gets the same key as the armTestwatch
	// walk's "pkg/x_test.go", so the same file is not recorded under both
	// spellings (and double-reported in the receipt) when a turn mixes a
	// ./-prefixed named-tool edit with a bash call, or the reverse. An
	// in-workdir ABSOLUTE path is already workdir-relative (the branch
	// above made it so), so it keys identically to the relative spelling
	// — /root/pkg/x_test.go and "pkg/x_test.go" are one snapshot.
	relKey := filepath.ToSlash(filepath.Clean(path))
	if cs.testwatch == nil {
		cs.testwatch = map[string]*testwatchSnapshot{}
	}
	if _, seen := cs.testwatch[relKey]; seen {
		return
	}
	if len(cs.testwatch) >= testwatchMaxFiles {
		return
	}
	info, err := os.Stat(abs)
	if err != nil || info.IsDir() {
		return
	}
	if cs.testwatchBytes()+int(info.Size()) > testwatchMaxBytes {
		return
	}
	data, err := os.ReadFile(abs)
	if err != nil {
		return
	}
	cs.testwatch[relKey] = &testwatchSnapshot{display: relKey, abs: abs, before: string(data)}
}

// testwatchBytes is the cumulative size of the current snapshots — the
// turn-level budget touchFile enforces.
func (cs *CortexSession) testwatchBytes() int {
	n := 0
	for _, s := range cs.testwatch {
		n += len(s.before)
	}
	return n
}

// reconstructBefore turns a bash-arm Baseline back into a Before string
// for the turn-end Scan. The receipt needs a Before whose line count and
// definition lines match what the file actually had, so Scan's removed-
// lines and shrink math come out right when it diffs against the live
// read. We don't know the non-definition lines, so we emit one blank
// line per them — Scan ignores their content, counting only line count
// (for shrink) and the definition lines (for removed-tests). The result
// is NOT byte-identical to the real before-side, but it is scan-
// equivalent: same line count, same test definitions.
func reconstructBefore(b testguard.Baseline) string {
	pad := b.Lines - len(b.Defs)
	if pad < 0 {
		pad = 0
	}
	lines := make([]string, 0, b.Lines)
	lines = append(lines, b.Defs...)
	for i := 0; i < pad; i++ {
		lines = append(lines, "")
	}
	return strings.Join(lines, "\n")
}

// testwatchReceipt renders the "tests changed: …" receipt line for this
// turn, or "" when the scan found nothing to report. Computed at the end of
// the turn (before the snapshot is dropped) and surfaced two ways: on the
// TurnResult (so the caller prints it to a human/driver) and in the capture
// summary (so the journal record shows it). testwatchFinalizeNote wraps it
// for the model: the harness hands the model this same fact at finalize time
// (loop.go's FinalizeHook) so its final answer accounts for the loss — the
// issue behind #141 is that a removed test went UNREPORTED, to no one.
func (cs *CortexSession) testwatchReceipt() string {
	if len(cs.testwatch) == 0 && len(cs.testwatchBash) == 0 {
		return ""
	}
	// Deterministic order for a deterministic receipt (map iteration is
	// random).
	paths := make([]string, 0, len(cs.testwatch))
	for p := range cs.testwatch {
		paths = append(paths, p)
	}
	sort.Strings(paths)

	// The named-tool arm (cs.testwatch) holds full before-content. The
	// bash arm (cs.testwatchBash) holds compact baselines; reconstruct
	// each baseline's Before side by splicing its definition lines back
	// into a skeleton of the right length. The receipt only needs to
	// diff against something that, when compared to the live read,
	// surfaces removed definitions and shrink — the exact shape a real
	// removal produces. (A full-content snapshot is more precise for the
	// named-tool arm, which is why it keeps it.)
	files := make(map[string]testguard.FilePair, len(paths)+len(cs.testwatchBash))
	for _, p := range paths {
		s := cs.testwatch[p]
		after, _ := os.ReadFile(s.abs)
		files[s.display] = testguard.FilePair{Before: s.before, After: string(after)}
	}
	for rel, base := range cs.testwatchBash {
		after, err := os.ReadFile(filepath.Join(cs.Workdir(), rel))
		if err != nil {
			after = nil // file was deleted by the turn
		}
		files[rel] = testguard.FilePair{Before: reconstructBefore(base), After: string(after)}
	}
	report := testguard.Scan(files)
	if report.IsEmpty() {
		return ""
	}
	var b strings.Builder
	b.WriteString("tests changed: ")
	for i, f := range report.Files {
		if i > 0 {
			b.WriteString("; ")
		}
		b.WriteString(f.Path)
		switch {
		case f.RemovedTests > 0 && f.Shrank:
			b.WriteString(" — ")
			b.WriteString(pluralize(f.RemovedTests, "test definition", "test definitions"))
			b.WriteString(" removed, file shrank ")
			b.WriteString(strconv.Itoa(f.BeforeLines))
			b.WriteString("→")
			b.WriteString(strconv.Itoa(f.AfterLines))
			b.WriteString(" lines")
		case f.RemovedTests > 0:
			b.WriteString(" — ")
			b.WriteString(pluralize(f.RemovedTests, "test definition", "test definitions"))
			b.WriteString(" removed")
		default:
			b.WriteString(" — shrank ")
			b.WriteString(strconv.Itoa(f.BeforeLines))
			b.WriteString("→")
			b.WriteString(strconv.Itoa(f.AfterLines))
			b.WriteString(" lines")
		}
		if len(f.RemovedSample) > 0 {
			b.WriteString(" (e.g. ")
			for j, l := range f.RemovedSample {
				if j >= testwatchSampleCap {
					break
				}
				if j > 0 {
					b.WriteString(", ")
				}
				b.WriteString(strings.TrimSpace(l))
			}
			if len(f.RemovedSample) > testwatchSampleCap {
				b.WriteString(", …")
			}
			b.WriteString(")")
		}
	}
	return b.String()
}

// testwatchFinalizeNote is the harness note loop.go's FinalizeHook hands the
// model at the clean-finalize point: when the turn removed or substantially
// shrank one of the project's test files, the model is told — in its own
// words from the scan — exactly what disappeared, and asked to restate its
// COMPLETE final answer with the loss accounted for (summary first,
// test-loss accounting second). The note is additive by design: runLoop
// APPENDS the model's reply to the original answer rather than replacing
// it, so the ask must be for the full restatement — a note that only asks
// about the tests would leave a narrow model answering just the note, and
// appending that to the summary would read like a doubled-up account. An
// empty restatement (or a failed send) leaves the original answer
// untouched. "" (no test loss, or nothing to report) means the answer is
// left untouched. It reads the SAME receipt the journal carries, so the
// model, the human, and the journal all agree on what the turn did.
func (cs *CortexSession) testwatchFinalizeNote() string {
	receipt := cs.testwatchReceipt()
	if receipt == "" {
		return ""
	}
	return "Before you finish: the harness detected that this turn removed or " +
		"substantially shrank one or more of the project's test files — " +
		receipt +
		". Restate your complete final answer — first your summary of what you " +
		"changed and why, then plainly which test(s) you removed or shrank and " +
		"why that loss is acceptable. A green build that quietly removed a " +
		"failing test is not a fix."
}

// testwatchDrop releases this turn's snapshot and baselines; called
// unconditionally at the START of every turn (turn.go) so a stale before-
// side from an earlier turn that never reached captureTurn (an error or
// interrupt path returns before it) can't leak into the next turn's
// receipt — and neither budget can fill up across turns.
func (cs *CortexSession) testwatchDrop() {
	cs.testwatch = nil
	cs.testwatchBash = nil
	cs.testwatchBashArmed = false
}

// pluralize renders "1 test definition" / "3 test definitions".
func pluralize(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return strconv.Itoa(n) + " " + many
}
