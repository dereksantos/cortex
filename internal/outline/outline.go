// Package outline is the structural primitive study leads with: `ls` generalized
// to file interiors, filled breadth-first to a token budget. A directory lists
// its files/subdirs; a file lists its top-level units; every entry carries a
// locator — a child path to outline deeper, or a line span to read_file.
// Deterministic, no model; a leaf primitive (stdlib + projectscan only, never
// cmd/cortex). See docs/study-subagent.md §2.
package outline

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/dereksantos/cortex/internal/projectscan"
)

// Entry is one structural unit: a directory child to outline deeper (Path set,
// Span zero), or an in-file unit to read (Span set, Path ""). Name is the label
// the model reads ("tools.go", "sub/", "func Resolve", "# Setup").
type Entry struct {
	Name string
	Path string // a dir/file to outline deeper; "" for an in-file unit
	Span [2]int // 1-indexed [start,end] for an in-file unit; {0,0} otherwise
}

// bytesPerToken is the rough tokens≈bytes/4 conversion the budget uses.
const bytesPerToken = 4

// maxReadLines mirrors the read tool's small-file floor: a file at or under this
// many lines has no useful interior outline (the model just reads it whole), so
// listing its units buys nothing.
const smallFileLines = 60

// Outline returns the structural map of path, breadth-first to budget tokens. A
// file yields its in-file units; a directory yields its children (and, within
// budget, the units of the files it contains). The truncation note lines Render
// adds are not Entries — Outline returns only real units/children.
func Outline(path string, budget int) ([]Entry, error) {
	entries, _, err := build(path, budget)
	return entries, err
}

// Render is the text the model sees: one line per entry, with a "… outline(…) to
// expand" note wherever budget deferred a deeper expansion.
func Render(path string, budget int) (string, error) {
	_, text, err := build(path, budget)
	return text, err
}

// unit is one in-file declaration/section with its 1-indexed line span.
type unit struct {
	label string // "func Resolve", "type T struct", "# Heading"
	start int
	end   int
}

// build is the shared walker: it produces the entry list and the rendered text
// in one pass so the two never drift.
func build(path string, budget int) ([]Entry, string, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, "", err
	}
	if budget <= 0 {
		budget = 4000
	}
	var (
		entries []Entry
		b       strings.Builder
		spent   int
	)
	emit := func(line string) {
		b.WriteString(line)
		b.WriteByte('\n')
		spent += len(line)/bytesPerToken + 1
	}

	if !info.IsDir() {
		buildFile(path, budget, emit, &entries, &spent)
		return entries, b.String(), nil
	}
	buildDir(path, budget, emit, &entries, &spent)
	return entries, b.String(), nil
}

// buildFile renders a single file's units, truncating to budget with a note.
func buildFile(path string, budget int, emit func(string), entries *[]Entry, spent *int) {
	units := listUnits(path)
	shown := 0
	for _, u := range units {
		line := fmt.Sprintf("%-46s L%d-%d", u.label, u.start, u.end)
		if shown > 0 && *spent+len(line)/bytesPerToken+1 > budget {
			break
		}
		emit(line)
		*entries = append(*entries, Entry{Name: u.label, Span: [2]int{u.start, u.end}})
		shown++
	}
	if shown < len(units) {
		emit(fmt.Sprintf("… +%d more — outline(%q, %d) to expand", len(units)-shown, path, budget*2))
	}
	if len(units) == 0 {
		emit(fmt.Sprintf("(%s has no outlineable structure — read_file(%q) to read it)", filepath.Base(path), path))
	}
}

// buildDir lists a directory's contents breadth-first. Every direct child's NAME
// is always listed (cheap, load-bearing — a relevant file can never silently
// vanish); budget is spent only on going deeper: expanding a Go file's symbols
// and descending into subdirectories. When budget runs out, deeper levels are
// deferred to a one-line "outline(child) to expand" note.
func buildDir(root string, budget int, emit func(string), entries *[]Entry, spent *int) {
	absRoot, err := filepath.Abs(root)
	if err != nil {
		absRoot = root
	}
	ignore := projectscan.LoadIgnoreSet(absRoot)

	type item struct {
		abs, rel string
		depth    int
	}
	queue := []item{{absRoot, "", 0}}
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		kids, derr := children(cur.abs, ignore)
		if derr != nil {
			continue
		}
		indent := strings.Repeat("  ", cur.depth)
		// A new BFS level beyond the root is budget-gated: if we've spent the
		// budget, list this subtree's name as deferred rather than walking it.
		descend := cur.depth == 0 || *spent < budget
		for _, k := range kids {
			rel := k.name
			if cur.rel != "" {
				rel = cur.rel + "/" + k.name
			}
			full := filepath.Join(root, rel)
			if k.isDir {
				emit(indent + k.name + "/")
				*entries = append(*entries, Entry{Name: k.name + "/", Path: rel})
				if descend {
					queue = append(queue, item{k.abs, rel, cur.depth + 1})
				} else {
					emit(indent + "  … outline(" + fmt.Sprintf("%q", rel) + ") to expand")
				}
				continue
			}
			emit(indent + k.name)
			*entries = append(*entries, Entry{Name: k.name, Path: rel})
			// Expand a file's units inline only while budget remains.
			if descend && *spent < budget {
				for _, u := range listUnits(full) {
					line := fmt.Sprintf("%s  %-42s L%d-%d", indent, u.label, u.start, u.end)
					if *spent+len(line)/bytesPerToken+1 > budget {
						emit(indent + "  … outline(" + fmt.Sprintf("%q", rel) + ") to expand")
						break
					}
					emit(line)
					*entries = append(*entries, Entry{Name: u.label, Path: rel, Span: [2]int{u.start, u.end}})
				}
			}
		}
	}
}

// child is one directory entry.
type child struct {
	name  string
	abs   string
	isDir bool
}

// children lists a directory's non-ignored entries, directories first then
// files, each alphabetical — a stable, ls-like order.
func children(dir string, ignore *projectscan.IgnoreSet) ([]child, error) {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var out []child
	for _, e := range ents {
		abs := filepath.Join(dir, e.Name())
		if e.IsDir() {
			if ignore.IsDirExcluded(abs, e.Name()) {
				continue
			}
			out = append(out, child{name: e.Name(), abs: abs, isDir: true})
			continue
		}
		if ignore.IsFileExcluded(abs) {
			continue
		}
		out = append(out, child{name: e.Name(), abs: abs})
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].isDir != out[j].isDir {
			return out[i].isDir // dirs first
		}
		return out[i].name < out[j].name
	})
	return out, nil
}

// listUnits returns a file's in-file units, dispatching by kind: go/ast for Go,
// a declaration/heading regex for other code and prose, and nothing for a small
// or unparseable file (read it whole instead).
func listUnits(path string) []unit {
	data, err := os.ReadFile(path)
	if err != nil || len(data) == 0 {
		return nil
	}
	if strings.HasSuffix(path, ".go") {
		if u := goUnits(data); len(u) > 0 {
			return u
		}
		return nil
	}
	lines := splitLines(data)
	if len(lines) <= smallFileLines {
		return nil // small file: read it whole, an outline buys nothing
	}
	return regexUnits(path, lines)
}
