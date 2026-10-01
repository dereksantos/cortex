// testwatch.go implements the turn-level "tests changed" and "leftover
// debug" receipts (issues #141, #154): the harness snapshots the
// before-content of the files a turn's write_file / edit_file / remove_path
// calls touch (bounded, best-effort, at turn start) and, after the turn,
// runs internal/testguard over the diffs. A turn that removed test
// definitions or substantially shrunk a test file gets a "tests changed: …"
// line in its capture summary; a turn that added debug prints to production
// files or left scratch files behind gets a "leftover debug: …" line (issue
// #154). The model then has to account for either. The scans never block the
// turn: an unreadable file, a size cap, or a vanished snapshot degrades to
// silence.
package main

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/dereksantos/cortex/internal/testguard"
	"github.com/dereksantos/cortex/internal/tools"
)

// testwatchTouchNames are the tool calls whose success mutates a file the
// scan must see the before-side of: the two writers and the deleter. These
// arm the snapshot for EVERY path they name (test or production) — the
// leftover-debug scan (issue #154) needs the before-side of production
// files to see the debug prints a turn adds to them, just as the tests-
// changed scan (issue #141) needs the before-side of test files. bash is
// NOT in this set — a command names no file — but a bash call can still
// mutate any file in the workspace (the #127 incident was a test file
// sed'd away), so armTestwatch baselines the project's test-named files
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
// The walk enforces the baseline cap (testwatchMaxBashBaselines) on the
// BASELINES ONLY: once it is reached, the walk keeps descending and
// RECORDING scratch-named paths (cs.testwatchScratchBefore) — the sweep
// (sweepScratchFiles) has no cap of its own, so a scratch file missed here
// (a pre-existing file in a directory past the cap) would be treated as NEW
// and flagged on every bash turn. Only the file READS stop.
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
	cs.testwatchScratchBefore = map[string]bool{}

	var walk func(dir, rel string)
	walk = func(dir, rel string) {
		// The cap stops the BASELINE READS only, never the recursion:
		// scratch-named paths must still be recorded into
		// cs.testwatchScratchBefore past the cap — sweepScratchFiles has no
		// cap of its own, so a pre-existing scratch file missed here (in a
		// directory visited after the cap) would look NEW to the sweep and
		// get a false "scratch file left behind" on every bash turn.
		baselined := len(cs.testwatchBash) < testwatchMaxBashBaselines
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
				continue
			}
			childKey := filepath.ToSlash(filepath.Clean(childRel))
			// The scratch baseline records EVERY scratch-named path that
			// exists before the command — test-named or not — so the post-
			// command sweep can tell a file the bash call CREATED from one
			// that pre-existed (testdata/foo.bak, scripts/tmp_setup.sh, a
			// committed scratchpad.go) and never re-flags the latter on
			// every bash turn. It applies PAST the baseline cap, because
			// the sweep walks uncapped (see above).
			if testguard.IsScratchPath(childKey) {
				cs.testwatchScratchBefore[childKey] = true
			}
			if !baselined {
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
			// stop READING at the cap, not keep reading past it. continue
			// (not break) so the loop still records scratch names and
			// still recurses into LATER siblings (ents is name-sorted, so
			// a break here would silently skip every entry after the
			// capping file — pre-existing scratch files in later subdirs
			// would never hit testwatchScratchBefore and the uncapped
			// sweep would flag them as new on every bash turn).
			if len(cs.testwatchBash) >= testwatchMaxBashBaselines {
				continue
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
// read at turn start. created marks a file the turn CREATED (it did not
// exist at snapshot time) — the leftover-debug scan's scratch signal fires
// only on those (a scratch file the turn merely edited or deleted is not
// "left behind", and a scratch file created and then removed was cleaned
// up).
type testwatchSnapshot struct {
	display string
	abs     string
	before  string
	created bool
}

// touchFile records that a turn's tooling will mutate path. Called from
// the coder dispatcher (before each write_file/edit_file/remove_path), so
// the before-side is captured BEFORE the tool runs. It snapshots EVERY path
// the mutating calls name — test files (issue #141's tests-changed scan) AND
// production files (issue #154's leftover-debug scan needs the production
// before-side to see the debug prints a turn adds). Missing files,
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
	// A MISSING scratch-named path (a write_file/edit_file target the turn is
	// about to create) is recorded with an EMPTY before-side and created
	// set: the model's usual way of creating a file is write_file, and the
	// sweep only runs after bash calls — without this, a turn that does
	// write_file(zz_dbg.go) and never runs bash would leave the scratch
	// file invisible to the turn-end ScanDebug (Before="", After=content,
	// Created=true → Scratch, "scratch file left behind"). created marks
	// the file as the turn's own, so a turn that creates it and then removes
	// it is not flagged (the scan requires the file to exist after the
	// turn). A missing NON-scratch path is not recorded: a new production
	// file is not a leftover, and the debug-print scan on a created
	// non-scratch file has no meaningful before-side. Any other stat
	// failure (non-file, unreadable, outside the workdir) degrades to "no
	// snapshot".
	info, err := os.Stat(abs)
	if err != nil {
		if os.IsNotExist(err) && testguard.IsScratchPath(relKey) {
			cs.testwatch[relKey] = &testwatchSnapshot{display: relKey, abs: abs, before: "", created: true}
			return
		}
		return
	}
	if info.IsDir() {
		return
	}
	if cs.testwatchBytes()+int(info.Size()) > testwatchMaxBytes {
		return
	}
	data, err := os.ReadFile(abs)
	if err != nil {
		// (A missing scratch path was already recorded in the os.Stat branch
		// above; every other read error degrades to "no snapshot".)
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

// sweepScratchFiles arms the leftover-debug scan (issue #154) for scratch-
// named files a bash call just CREATED in the workspace. bash names no file,
// so the touch hook can't snapshot a bash-created file the way it snapshots
// a write_file/edit_file path — the bash arm's baseline (armTestwatch) only
// covers test-named files, so without this sweep a scratch file left behind
// would be invisible to the turn-end ScanDebug. The sweep does a bounded
// best-effort walk of the workdir (skipping .git/.cortex/vendor/node_modules,
// like armTestwatch) and records the full content of every scratch-named file
// that is NEW since the pre-bash baseline (cs.testwatchScratchBefore) —
// test-named or not: zz_dbg_test.go, the issue's cited incident, is scratch-
// AND test-named, and only this arm can surface it — into the named-tool
// budget (cs.testwatch) under the file's current (post-bash) content. A
// scratch file that EXISTED before the bash call is in the baseline, so it
// is skipped: flagging pre-existing scratch-named files (testdata/foo.bak,
// scripts/tmp_setup.sh, a committed scratchpad.go) would fire a false
// "scratch file left behind" receipt on every turn that runs bash. A scratch
// file the bash call CREATED is not in the baseline, so it lands here with
// its post-bash content as the before-side (before==after — the file was
// just created) and created=true; the turn-end ScanDebug then sees
// Before==After with Created=true and Scratch=true and flags it as "scratch
// file left behind".
//
// A scratch file that pre-existed and was MUTATED by bash is NOT snapshotted
// (it is in the baseline): the leftover-debug scan's Scratch signal only
// cares that a scratch file the turn CREATED exists after the turn — a
// mutation of an old one is not "left behind". The byte budget is shared
// with touchFile's named-tool snapshots (cs.testwatch), so a turn that runs
// many bash commands creating many scratch files can exhaust the 32-file /
// 1 MiB budget — the same bounded, best-effort degradation as touchFile.
//
// Called from coderDispatcher AFTER the bash command runs (the dispatcher
// already armed the baseline BEFORE the command; the sweep runs after, when
// the command's mutations are visible on disk). A no-op when there is no
// workdir, no bash call ran yet this turn (no baseline), or the sweep finds
// no new scratch files.
func (cs *CortexSession) sweepScratchFiles() {
	wd := cs.Workdir()
	if wd == "" {
		return
	}
	// No pre-bash baseline means no bash call ran yet this turn (the
	// baseline is armed BEFORE the first command), so there is nothing to
	// sweep against — never flag pre-existing scratch files.
	if cs.testwatchScratchBefore == nil {
		return
	}
	if cs.testwatch == nil {
		cs.testwatch = map[string]*testwatchSnapshot{}
	}
	var sweep func(dir, rel string)
	sweep = func(dir, rel string) {
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
				sweep(filepath.Join(dir, name), childRel)
				continue
			}
			// Only scratch-named files are of interest, and only ones NEW
			// since the pre-bash baseline: a pre-existing scratch file is
			// already in the baseline (or was snapshotted by a named-tool
			// call, cs.testwatch) and must not get a false "left behind"
			// receipt on every bash turn. Test-named scratch files are NO
			// LONGER skipped — zz_dbg_test.go (the issue's cited incident) is
			// scratch AND test-named, and only this arm snapshots it: the
			// bash arm's baseline feeds the TESTS scan (removed definitions),
			// not the leftover-debug scan.
			childKey := filepath.ToSlash(filepath.Clean(childRel))
			if !testguard.IsScratchPath(childKey) {
				continue
			}
			if cs.testwatchScratchBefore[childKey] {
				continue
			}
			// Skip a file already snapshotted by a named-tool call this turn
			// (cs.testwatch) — the receipt would double-report it.
			if _, already := cs.testwatch[childKey]; already {
				continue
			}
			if len(cs.testwatch) >= testwatchMaxFiles {
				return
			}
			abs := filepath.Join(dir, name)
			info, err := os.Stat(abs)
			if err != nil || info.IsDir() {
				continue
			}
			if cs.testwatchBytes()+int(info.Size()) > testwatchMaxBytes {
				return
			}
			data, err := os.ReadFile(abs)
			if err != nil {
				continue
			}
			relKey := filepath.ToSlash(filepath.Clean(childRel))
			cs.testwatch[relKey] = &testwatchSnapshot{display: relKey, abs: abs, before: string(data), created: true}
		}
	}
	sweep(wd, "")
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

// testwatchReceipt renders this turn's receipt: a "tests changed: …" line
// (issue #141, from testguard.Scan) and/or a "leftover debug: …" line (issue
// #154, from testguard.ScanDebug), joined by a newline — "" when both scans
// found nothing to report. One FilePair map feeds both scans (see
// testwatchFilePairs); the two reports are independent, so a turn that
// removed a test but left no debug gets only the tests line, and a turn that
// added a debug print to a production file but touched no test gets only the
// debug line. Computed at the end of the turn (before the snapshot is
// dropped) and surfaced two ways: on the TurnResult (so the caller prints it
// to a human/driver) and in the capture summary (so the journal record shows
// it). testwatchFinalizeNote wraps it for the model: the harness hands the
// model this same fact at finalize time (loop.go's FinalizeHook) so its
// final answer accounts for it — the issues behind #141 and #154 are that a
// removed test / a leftover debug print went UNREPORTED, to no one.
func (cs *CortexSession) testwatchReceipt() string {
	files := cs.testwatchFilePairs()
	if len(files) == 0 {
		return ""
	}
	testLine := renderTestReceipt(testguard.Scan(files))
	debugLine := renderDebugReceipt(testguard.ScanDebug(files))
	switch {
	case testLine != "" && debugLine != "":
		return testLine + "\n" + debugLine
	default:
		return testLine + debugLine
	}
}

// testwatchTestsReceipt returns just the "tests changed: …" line (issue
// #141) for this turn, or "" when the tests scan found nothing to report.
// captureTurn uses this to add the tests fact to the capture summary
// separately from the leftover-debug fact, so the journal record shows each
// on its own — a turn that lost a test but left no debug gets only the tests
// line, and vice versa.
func (cs *CortexSession) testwatchTestsReceipt() string {
	files := cs.testwatchFilePairs()
	if len(files) == 0 {
		return ""
	}
	return renderTestReceipt(testguard.Scan(files))
}

// testwatchDebugReceipt returns just the "leftover debug: …" line (issue
// #154) for this turn, or "" when the leftover-debug scan found nothing to
// report. captureTurn uses this to add the leftover-debug fact to the capture
// summary separately from the tests fact, and turn.go copies it into
// TurnResult.DebugReceipt so a caller can surface it to a human.
func (cs *CortexSession) testwatchDebugReceipt() string {
	files := cs.testwatchFilePairs()
	if len(files) == 0 {
		return ""
	}
	return renderDebugReceipt(testguard.ScanDebug(files))
}

// testwatchFilePairs builds the single FilePair map both scans diff against:
// the named-tool arm (cs.testwatch) holds full before-content, the bash arm
// (cs.testwatchBash) holds compact baselines reconstructed into a scan-
// equivalent Before (see reconstructBefore). Both arms' files land in ONE
// map so a file recorded by both is diffed once — the tests scan and the
// leftover-debug scan see the same before/after and can't double-report the
// same file. Keys are workdir-relative and cleaned, so a "./"-spelled
// named-tool path and the bash arm's walk key are the same entry. Created
// carries the snapshot's created flag (a bash-reconstructed baseline is
// never created — it is a pre-turn state by construction).
func (cs *CortexSession) testwatchFilePairs() map[string]testguard.FilePair {
	if len(cs.testwatch) == 0 && len(cs.testwatchBash) == 0 {
		return nil
	}
	files := make(map[string]testguard.FilePair, len(cs.testwatch)+len(cs.testwatchBash))
	for _, s := range cs.testwatch {
		after, _ := os.ReadFile(s.abs)
		files[s.display] = testguard.FilePair{Before: s.before, After: string(after), Created: s.created}
	}
	for rel, base := range cs.testwatchBash {
		after, err := os.ReadFile(filepath.Join(cs.Workdir(), rel))
		if err != nil {
			after = nil // file was deleted by the turn
		}
		files[rel] = testguard.FilePair{Before: reconstructBefore(base), After: string(after)}
	}
	return files
}

// renderTestReceipt renders the "tests changed: …" line from a testguard
// Report, or "" when the report is empty (a turn that lost no tests).
func renderTestReceipt(report testguard.Report) string {
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

// renderDebugReceipt renders the "leftover debug: …" line from a
// testguard.DebugReport, or "" when the report is empty (a turn that left no
// debug print or scratch file behind — the silence case). Each entry names
// the file and what the turn left in it: debug prints added to a production
// file (with a bounded sample) and/or the fact that it is a leftover scratch
// file. Entries are separated by "; " and sorted by path (the scan already
// sorted them).
func renderDebugReceipt(report testguard.DebugReport) string {
	if report.IsEmpty() {
		return ""
	}
	var b strings.Builder
	b.WriteString("leftover debug: ")
	for i, f := range report.Files {
		if i > 0 {
			b.WriteString("; ")
		}
		b.WriteString(f.Path)
		switch {
		case f.DebugPrints > 0 && f.Scratch:
			b.WriteString(" — ")
			b.WriteString(pluralize(f.DebugPrints, "debug print", "debug prints"))
			b.WriteString(" added, scratch file left behind")
		case f.DebugPrints > 0:
			b.WriteString(" — ")
			b.WriteString(pluralize(f.DebugPrints, "debug print", "debug prints"))
			b.WriteString(" added")
		default:
			b.WriteString(" — scratch file left behind")
		}
		if len(f.DebugSample) > 0 {
			b.WriteString(" (e.g. ")
			for j, l := range f.DebugSample {
				if j >= testwatchSampleCap {
					break
				}
				if j > 0 {
					b.WriteString(", ")
				}
				b.WriteString(strings.TrimSpace(l))
			}
			if len(f.DebugSample) > testwatchSampleCap {
				b.WriteString(", …")
			}
			b.WriteString(")")
		}
	}
	return b.String()
}

// testwatchFinalizeNote is the harness note loop.go's FinalizeHook hands the
// model at the clean-finalize point: when the turn removed or substantially
// shrank one of the project's test files (issue #141) OR added debug prints
// to production files / left scratch files behind (issue #154), the model is
// told — in its own words from the scans — exactly what happened, and asked
// to restate its COMPLETE final answer with the loss / leftover accounted
// for (summary first, accounting second). The note is additive by design:
// runLoop APPENDS the model's reply to the original answer rather than
// replacing it, so the ask must be for the full restatement — a note that
// only asks about one fact would leave a narrow model answering just the
// note, and appending that to the summary would read like a doubled-up
// account. An empty restatement (or a failed send) leaves the original
// answer untouched. "" (nothing to report) means the answer is left
// untouched. It reads the SAME receipt the journal carries, so the model,
// the human, and the journal all agree on what the turn did.
func (cs *CortexSession) testwatchFinalizeNote() string {
	receipt := cs.testwatchReceipt()
	if receipt == "" {
		return ""
	}
	// The note covers whichever fact(s) the turn produced. A turn that lost
	// a test but left no debug gets the test-loss framing; a turn that left
	// a debug print but touched no test gets the leftover-debug framing; a
	// turn that did both gets both.
	var framing strings.Builder
	framing.WriteString("Before you finish: the harness detected that this turn ")
	testLine := cs.testwatchTestsReceipt()
	debugLine := cs.testwatchDebugReceipt()
	switch {
	case testLine != "" && debugLine != "":
		framing.WriteString("removed or substantially shrank one or more of the project's test files AND left leftover debug prints / scratch files behind — ")
	case testLine != "":
		framing.WriteString("removed or substantially shrank one or more of the project's test files — ")
	default:
		framing.WriteString("left leftover debug prints in production files and/or scratch files in the workspace — ")
	}
	framing.WriteString(receipt)
	framing.WriteString(". Restate your complete final answer — first your summary of what you changed and why, then plainly account for what the harness flagged: ")
	switch {
	case testLine != "" && debugLine != "":
		framing.WriteString("which test(s) you removed or shrank and why that loss is acceptable, AND which debug print(s) you added to production code / which scratch file(s) you left behind and why they should be removed (or why keeping them is intentional). A green build that quietly removed a failing test or left a fmt.Fprintf(os.Stderr, \"DEBUG: …\") in shipped code is not a fix.")
	case testLine != "":
		framing.WriteString("which test(s) you removed or shrank and why that loss is acceptable. A green build that quietly removed a failing test is not a fix.")
	default:
		framing.WriteString("which debug print(s) you added to production code / which scratch file(s) you left behind and why they should be removed (or why keeping them is intentional). A green build that quietly left a fmt.Fprintf(os.Stderr, \"DEBUG: …\") in shipped code is not a fix.")
	}
	return framing.String()
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
	cs.testwatchScratchBefore = nil
}

// pluralize renders "1 test definition" / "3 test definitions".
func pluralize(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return strconv.Itoa(n) + " " + many
}
