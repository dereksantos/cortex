package testguard

// leftover-debug detector (issue #154). Sibling to Scan, which watches test
// files for LOST definitions: this one watches what a turn ADDS to the rest
// of the project — throwaway debug prints left in non-test (production)
// files, and scratch-named files left behind in the workspace after the turn
// (zz_dbg* / tmp_* / *_tmp / *.bak / ...). The #152 incident: a model added
// fmt.Fprintf(os.Stderr, "DEBUG: ...") into shipped production code and
// dropped a zz_dbg test file into the package; both were later removed, but
// nothing enforced it. A report here is a warning for the turn's receipt,
// exactly like Scan's: it never blocks the turn.
//
// Detection is a shape match, not a parse — the same standing as Scan. A
// debug print is a line the turn added (not present in Before) that carries
// an explicit debug marker (a "DEBUG" token or a dbg_/debug_ function call)
// or, in Go, a bare debug-shaped print call (fmt.Print* / fmt.Fprintf or
// fmt.Fprintln to os.Stderr / os.Stdout / println / print). Routine formatting
// — fmt.Sprintf / fmt.Sprint*, or fmt.Fprintf(w, …) to an arbitrary writer —
// is ordinary production code and never matches on its own. A scratch file is
// one whose name carries a throwaway convention (IsScratchPath) that the turn
// created and that still exists after the turn — a pre-existing scratch file
// the turn only edited or deleted, or a scratch file the turn created and
// then removed, is not "left behind".
//
// The debug-print signal is scoped to NON-TEST files — a debug print inside
// a test is legitimate (t.Log, t.Logf, a fmt check in a _test.go body) — but
// the scratch signal applies to every path the turn CREATED, test-named or
// not: the #152 incident's zz_dbg_test.go is BOTH scratch- and test-named,
// and only the scratch signal surfaces it. Created carries that created fact
// (FilePair.Created); a turn that merely edited or deleted a pre-existing
// scratch file is not reported as leaving it behind. ScanDebug applies the
// non-test gate to the debug-print signal alone, so a test-named scratch
// file is reported with Scratch=true and zero debug prints. The caller
// (cmd/cortex's testwatch) supplies FilePairs for the files it snapshotted
// before the turn, setting Created when the turn made the file.

import (
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// scratchPrefixes / scratchSuffixes / scratchExts name a throwaway file by
// convention, whatever the language. Matched against the lowercased basename
// with its final extension stripped, so zz_dbg_test.go is recognized through
// its prefix and foo.tmp through its extension. Deliberately narrow: a file
// called debug.go (a legitimate module named debug) is NOT scratch — only
// the throwaway markers below are.
var (
	scratchPrefixes = []string{
		"zz_dbg", "zzdbg", "dbg_", "debug_", "scratch", "tmp_", "temp_",
	}
	// scratchSuffixes are name-part suffixes (with an underscore or dot
	// separator BEFORE the marker) that mark a file as throwaway. They are
	// matched against the basename with its final extension stripped, so
	// "config.tmp" (from config.tmp.go) is NOT scratch — its final extension
	// is .go, and "config.tmp" carries no underscore-suffixed marker. The
	// dot-suffixed .tmp/.bak/.backup forms are handled by scratchExts, which
	// checks the actual file extension, so they are deliberately NOT here.
	scratchSuffixes = []string{
		"_tmp", "_scratch", "_bak", "_backup",
	}
	// scratchExts are whole-file scratch extensions (tmp files, backups).
	scratchExts = map[string]bool{
		".tmp": true, ".bak": true, ".backup": true,
	}
)

// IsScratchPath reports whether path is a throwaway/scratch file by naming
// convention: the basename (final extension stripped) carries a scratch
// prefix (zz_dbg, zzdbg, dbg_, debug_, scratch, tmp_, temp_), a scratch
// suffix (_tmp, _scratch, _bak, _backup), or a scratch extension
// (.tmp, .bak, .backup). The check is on the name alone — it never looks at
// content — and it is a pure name test: it does NOT exclude test-named files,
// so a zz_dbg_test.go IS a scratch path here. (ScanDebug applies the
// non-test gate separately, and the harness — cmd/cortex's testwatch, issue
// #154 — composes the final signal.) Exported so the harness can recognize a
// scratch file by the same convention the scan flags.
func IsScratchPath(path string) bool {
	base := filepath.Base(path)
	ext := strings.ToLower(filepath.Ext(base))
	if scratchExts[ext] {
		return true
	}
	name := strings.ToLower(strings.TrimSuffix(base, ext))
	for _, p := range scratchPrefixes {
		if strings.HasPrefix(name, p) {
			return true
		}
	}
	for _, s := range scratchSuffixes {
		if strings.HasSuffix(name, s) {
			return true
		}
	}
	return false
}

// The shape of a debug print, per file type. A line is a debug print when it
// carries an explicit debug marker — a "DEBUG" token or a dbg_/debug_
// function call — in ANY language, or, in Go, when it is a bare debug print:
// fmt.Print* (deliberate writes to the program's default output), println /
// print, or fmt.Fprintf / fmt.Fprintln writing to os.Stderr / os.Stdout
// (the stderr/stdout debug shape). fmt.Sprintf / fmt.Sprint* and a bare
// fmt.Fprintf(w, …) to an arbitrary writer are routine production code —
// this repo formats into bytes.Buffer and strings.Builder everywhere — so
// they only match through the explicit marker.
var (
	// Any language: an explicit debug marker — "DEBUG" as a token (the
	// #152 "DEBUG: ..." shape) or a dbg_/debug_ call (debug_print, dbg(),
	// _debug()). Anchored to the marker so "debug" inside an identifier
	// (debugger, debugging) does not match on its own.
	debugMarkerRe = regexp.MustCompile(`\bDEBUG\b|dbg_[a-z0-9_]*\s*\(|debug[a-z0-9_]*\s*\(`)
	// Go: a bare debug print — fmt.Print* (Println / Printf / Print — the ln
	// variants listed first so Println is not swallowed by Print), the
	// builtin println / print, or fmt.Fprintf / fmt.Fprintln whose FIRST
	// argument is os.Stderr or os.Stdout (the stderr/stdout debug shape).
	// fmt.Sprintf / fmt.Sprint* and a write to any other writer are routine
	// code: this repo uses fmt.Sprintf and fmt.Fprintf(&b, …) everywhere, so
	// flagging them would fire on most Go-editing turns.
	goPrintRe = regexp.MustCompile(`\bfmt\.(Println|Printf|Print)\s*\(|\b(?:Fprintln|Fprintf)\s*\(\s*os\.(Stderr|Stdout)\b|\bprintln\s*\(|\bprint\s*\(`)
)

// isDebugPrintLine reports whether a single line of code is a leftover debug
// print. The file's extension picks the per-language bare-call pattern (Go
// only); the explicit debug marker is language-agnostic and always counts.
// A comment line is never a debug print.
func isDebugPrintLine(path, line string) bool {
	trimmed := strings.TrimSpace(line)
	if trimmed == "" || strings.HasPrefix(trimmed, "//") ||
		strings.HasPrefix(trimmed, "#") || strings.HasPrefix(trimmed, "/*") ||
		strings.HasPrefix(trimmed, "*") {
		return false
	}
	if debugMarkerRe.MatchString(line) {
		return true
	}
	if strings.ToLower(filepath.Ext(path)) == ".go" && goPrintRe.MatchString(line) {
		return true
	}
	return false
}

// DebugFileReport is the per-file half of a DebugReport.
type DebugFileReport struct {
	Path string // the file's path as the turn touched it
	// DebugPrints counts ADDED lines (present in After, not in Before) that
	// look like debug prints, in a non-test production file.
	DebugPrints int
	// DebugSample is a bounded sample of those added lines.
	DebugSample []string
	// Scratch reports the file is a scratch-named (IsScratchPath) file the
	// TURN created and that still exists after the turn — a throwaway the
	// model left in the workspace. A pre-existing scratch file (edited,
	// deleted, or merely present) is never "left behind" by this turn, and a
	// scratch file the turn created and then REMOVED was cleaned up, not
	// left behind.
	Scratch bool
}

// DebugReport is what a turn LEFT behind: debug prints in production files
// and scratch files in the workspace.
type DebugReport struct {
	Files []DebugFileReport
}

// IsEmpty reports whether the turn left nothing to flag — the case where the
// receipt should stay silent.
func (r DebugReport) IsEmpty() bool {
	return len(r.Files) == 0
}

// ScanDebug compares each file's before/after content and reports what the
// turn LEFT behind: added debug prints in non-test production files, and
// scratch-named files that exist after the turn. The non-test gate applies
// to the debug-print signal ONLY — a debug print inside a test is
// legitimate (and test loss is Scan's job, not this one's) — while the
// scratch signal fires for EVERY snapshotted path, test-named or not:
// zz_dbg_test.go is both scratch- and test-named, and only the scratch
// signal surfaces it. rep.Files is sorted by Path — map iteration is
// random, but the receipt must be deterministic for a deterministic turn.
// It never blocks the turn.
func ScanDebug(files map[string]FilePair) DebugReport {
	var rep DebugReport
	for path, pair := range files {
		frep, ok := scanDebugFile(path, pair.Before, pair.After, !IsTestPath(path), pair.Created)
		if ok {
			rep.Files = append(rep.Files, frep)
		}
	}
	sort.Slice(rep.Files, func(i, j int) bool { return rep.Files[i].Path < rep.Files[j].Path })
	return rep
}

// scanDebugFile reports one file. nonTest gates the debug-print signal
// (a debug print in a test file is legitimate); created gates the scratch
// signal (only a scratch file the turn created is "left behind" — a
// pre-existing scratch file the turn edited or deleted, or one the turn
// created and then removed, is not). ok=false when nothing is flagged.
func scanDebugFile(path, before, after string, nonTest, created bool) (DebugFileReport, bool) {
	// "Left behind" means: the turn created this scratch-named file AND it
	// still exists after the turn. before=="" is only the absence of a
	// BEFORE-side the harness had — a bash-arm baseline, an unreadable
	// file — not proof the turn created the file; pair.Created carries that
	// fact. A turn that deletes a pre-existing scratch file (before!=
	// "", after=="") cleaned up, not left behind; one that creates then
	// removes (before=="", after=="", created=true) likewise cleaned up.
	scratch := IsScratchPath(path) && created && after != ""
	if before == after {
		// Unchanged: only a scratch file the turn CREATED (before=="",
		// created=true) is a leftover; a pre-existing file the turn never
		// touched (or created=false) is not.
		if scratch {
			return DebugFileReport{Path: path, Scratch: true}, true
		}
		return DebugFileReport{}, false
	}
	var added []string
	if nonTest {
		beforeSet := make(map[string]int)
		for _, l := range splitLines(before) {
			beforeSet[l]++
		}
		// Added lines: present more often in after than in before — the
		// mirror of removedLines, which a reviewer would eyeball as "what
		// the turn put in." A line the turn merely moved (present in both)
		// is not added.
		for _, l := range splitLines(after) {
			if beforeSet[l] > 0 {
				beforeSet[l]--
				continue
			}
			added = append(added, l)
		}
	}
	prints := 0
	sample := make([]string, 0, len(added))
	for _, l := range added {
		if isDebugPrintLine(path, l) {
			prints++
			if len(sample) < maxSample {
				sample = append(sample, truncate(l, maxSampleLen))
			}
		}
	}
	if prints == 0 && !scratch {
		return DebugFileReport{}, false
	}
	return DebugFileReport{
		Path:        path,
		DebugPrints: prints,
		DebugSample: sample,
		Scratch:     scratch,
	}, true
}
