// pathnav.go — shared helpers for the not-found errors read_file, grep, and
// outline all return when a path doesn't exist (issue #142). Instead of a bare
// "no such file or directory" that dead-ends the model (the same
// path-guessing family as the workspace note and the directory listing), a
// missing path hands back orientation: which tools locate a file, the actual
// workspace root when the given path is absolute or outside it (so the model
// sees where it really stands instead of guessing a foreign absolute path), and
// nearby existing candidates when they can be found — so the model corrects the
// path from real state rather than blindly retrying a guess.
//
// Everything here is best-effort: an empty result just means "no candidates
// found" and the caller still returns an oriented error.

package tools

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// maxPathCandidates caps how many nearby candidates a not-found error lists, so
// a listing of a huge directory can't flood the error (and the context).
const maxPathCandidates = 10

// nearCandidates finds existing files near fsPath: files in fsPath's parent
// directory sharing its base name by extension (any base), and, if the parent
// doesn't exist, the same-dir same-basename candidates of the deepest existing
// ancestor (the model likely mistyped one path component). "" when none are
// found or the parent can't be read — the caller then returns an oriented error
// with no candidate list.
//
// A "same extension" match needs the base to carry one (no dot) or an extension
// at all; a dotless base (e.g. "Makefile") matches every file in the parent,
// which is still useful orientation, so a dotless base is treated as matching
// everything.
func nearCandidates(fsPath string) string {
	dir := filepath.Dir(fsPath)
	if info, err := os.Stat(dir); err != nil || !info.IsDir() {
		return ""
	}
	base := filepath.Base(fsPath)
	ext := filepath.Ext(base) // ".go", ".txt", "" …
	var matches []string
	ents, err := os.ReadDir(dir)
	if err != nil {
		return ""
	}
	for _, e := range ents {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if ext != "" && filepath.Ext(name) != ext {
			continue
		}
		matches = append(matches, filepath.Join(dir, name))
	}
	if len(matches) == 0 {
		return ""
	}
	sort.Strings(matches)
	if len(matches) > maxPathCandidates {
		matches = matches[:maxPathCandidates]
	}
	var b strings.Builder
	b.WriteString("Nearby existing files (same directory, same extension):\n")
	for _, m := range matches {
		b.WriteString("  - ")
		b.WriteString(m)
		b.WriteByte('\n')
	}
	return strings.TrimRight(b.String(), "\n")
}

// parentCandidates is like nearCandidates but for a path whose PARENT directory
// doesn't exist: it walks up to the deepest existing ancestor and lists files
// there that share the path's base name exactly (same basename) — the most
// likely correction when one path component was mistyped. "" when the parent
// chain is unresolvable or no same-basename file exists.
func parentCandidates(fsPath string) string {
	dir := filepath.Dir(fsPath)
	base := filepath.Base(fsPath)
	// Walk up to the deepest existing ancestor directory.
	d := dir
	for d != "." && d != string(filepath.Separator) {
		if info, err := os.Stat(d); err == nil && info.IsDir() {
			break
		}
		parent := filepath.Dir(d)
		if parent == d {
			return ""
		}
		d = parent
	}
	if info, err := os.Stat(d); err != nil || !info.IsDir() {
		return ""
	}
	ents, err := os.ReadDir(d)
	if err != nil {
		return ""
	}
	var matches []string
	for _, e := range ents {
		if e.IsDir() {
			continue
		}
		if e.Name() == base {
			matches = append(matches, filepath.Join(d, e.Name()))
		}
	}
	if len(matches) == 0 {
		return ""
	}
	sort.Strings(matches)
	if len(matches) > maxPathCandidates {
		matches = matches[:maxPathCandidates]
	}
	var b strings.Builder
	b.WriteString("Files with that name in the nearest existing directory:\n")
	for _, m := range matches {
		b.WriteString("  - ")
		b.WriteString(m)
		b.WriteByte('\n')
	}
	return strings.TrimRight(b.String(), "\n")
}

// pathNotFoundError builds the shared not-found error message: it names the
// missing path, points at outline/grep for orientation, includes the actual
// workspace root when the given path is absolute or outside it (so the model
// stops guessing foreign absolute paths), and appends nearby candidates when
// found. display is the model-visible path, fsPath the resolved one, root the
// workspace root ("" when none is set).
func pathNotFoundError(display, fsPath, root string) error {
	var b strings.Builder
	fmt.Fprintf(&b, "%s does not exist.", display)
	b.WriteString(" To locate the right path, use outline (list a directory) or grep (search contents) instead of guessing.")
	// The root line only earns its keep when it adds information: an absolute
	// or out-of-workspace path is exactly where the model is guessing a foreign
	// root, so tell it where the workspace actually is. A relative path that
	// simply isn't there yet doesn't need the root repeated back.
	if root != "" && (filepath.IsAbs(display) || !isUnderRoot(fsPath, root)) {
		b.WriteString(" The workspace root is ")
		b.WriteString(root)
		b.WriteString("; all tool paths are relative to it.")
	}
	// Nearby candidates: prefer the parent-listing form when the parent exists
	// (same-dir same-extension), else the walk-up same-basename form.
	if cand := nearCandidates(fsPath); cand != "" {
		b.WriteString("\n\n")
		b.WriteString(cand)
	} else if cand := parentCandidates(fsPath); cand != "" {
		b.WriteString("\n\n")
		b.WriteString(cand)
	}
	return fmt.Errorf("%s", b.String())
}

// isUnderRoot reports whether fsPath is under (or equal to) root, both
// lexically (no symlink resolution — the goal is display guidance, not
// confinement, which ConfinePath already does with real-path checks).
func isUnderRoot(fsPath, root string) bool {
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return false
	}
	fsAbs, err := filepath.Abs(fsPath)
	if err != nil {
		return false
	}
	rel, err := filepath.Rel(rootAbs, fsAbs)
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))
}
