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
// or, in Go, a bare debug-shaped print call (fmt.Println / fmt.Fprintf /
// log.Print... — any log call is a deliberate write, so flagging it is
// acceptable). A scratch file is one whose name carries a throwaway
// convention (IsScratchPath) that exists after the turn (or existed at all).
//
// Both signals are scoped to NON-TEST files: a debug print inside a test is
// legitimate (t.Log, t.Logf, a fmt check in a _test.go body), and the
// scratch-file name check only fires for files that are not themselves
// test-named — a zz_dbg_test.go is both scratch- and test-named, and the
// receipt the caller composes reports it through whichever signal is
// stronger. The caller (cmd/cortex's testwatch) supplies FilePairs for the
// files it snapshotted before the turn; an empty Before means the file did
// not exist before the turn.

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
// function call — in ANY language, or, in Go, when it is a bare print/log
// call (any fmt.Print... / log.Print... / println is a deliberate write a
// model leaves behind, so flag it). Python's print() and JS/TS's console.log
// are routine code, so they only match through the explicit marker — a bare
// print(x) is not a debug print.
var (
	// Any language: an explicit debug marker — "DEBUG" as a token (the
	// #152 "DEBUG: ..." shape) or a dbg_/debug_ call (debug_print, dbg(),
	// _debug()). Anchored to the marker so "debug" inside an identifier
	// (debugger, debugging) does not match on its own.
	debugMarkerRe = regexp.MustCompile(`\bDEBUG\b|dbg_[a-z0-9_]*\s*\(|debug[a-z0-9_]*\s*\(`)
	// Go: a bare print/log call — fmt.Println / fmt.Printf / fmt.Fprintf /
	// fmt.Fprintln / log.Print... / println. Any of these in a non-test file
	// is a deliberate write the model left in; the explicit marker above is
	// a superset, but a plain fmt.Println("x") with no "DEBUG" token is
	// still a leftover print, so Go matches the bare call too. The ln
	// variants are listed first so Println is not swallowed by Print.
	goPrintRe = regexp.MustCompile(`\b(fmt\.(Println|Printf|Print|Fprintln|Fprintf|Fprint|Sprintln|Sprintf|Sprint)|log\.(Println|Printf|Print|Fprintln|Fprintf|Fprint)|println)\s*\(`)
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
	// Scratch reports the file is a scratch-named (IsScratchPath) file that
	// exists after the turn (or existed at all) — a throwaway the model left
	// in the workspace.
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
// scratch-named files that exist after the turn. Test files are skipped
// entirely (a debug print inside a test is legitimate; test loss is
// Scan's job, not this one's). rep.Files is sorted by Path — map iteration
// is random, but the receipt must be deterministic for a deterministic turn.
// It never blocks the turn.
func ScanDebug(files map[string]FilePair) DebugReport {
	var rep DebugReport
	for path, pair := range files {
		if IsTestPath(path) {
			continue
		}
		if frep, ok := scanDebugFile(path, pair.Before, pair.After); ok {
			rep.Files = append(rep.Files, frep)
		}
	}
	sort.Slice(rep.Files, func(i, j int) bool { return rep.Files[i].Path < rep.Files[j].Path })
	return rep
}

// scanDebugFile reports one non-test file. ok=false when the turn added no
// debug print and the file is not a leftover scratch file.
func scanDebugFile(path, before, after string) (DebugFileReport, bool) {
	scratch := IsScratchPath(path) && (after != "" || before != "")
	if before == after {
		// Unchanged: only a pre-existing scratch file (present before AND
		// after) is still a leftover; a freshly-created scratch file has
		// before=="" so it never reaches here.
		if scratch {
			return DebugFileReport{Path: path, Scratch: true}, true
		}
		return DebugFileReport{}, false
	}
	beforeSet := make(map[string]int)
	for _, l := range splitLines(before) {
		beforeSet[l]++
	}
	// Added lines: present more often in after than in before — the mirror
	// of removedLines, which a reviewer would eyeball as "what the turn put
	// in." A line the turn merely moved (present in both) is not added.
	var added []string
	for _, l := range splitLines(after) {
		if beforeSet[l] > 0 {
			beforeSet[l]--
			continue
		}
		added = append(added, l)
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
