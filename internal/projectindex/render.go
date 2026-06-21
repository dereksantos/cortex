package projectindex

import (
	"fmt"
	"path"
	"sort"
	"strings"
)

// Render returns the index as compact text: files grouped under their directory,
// each Go file followed by its symbols inline as "name:line", "·"-separated.
// The header reports totals so a model can judge whether to scope to a subtree.
//
//	internal/projectindex/
//	  index.go (180)
//	    type Symbol:24 · type File:31 · func Build:43 · func (*Index) Render:…
//	  render.go (60)
//	    func Render:14
func (ix *Index) Render() string {
	var b strings.Builder
	root := path.Base(ix.Root)
	if root == "." || root == "/" || root == "" {
		root = ix.Root
	}
	syms := 0
	for _, f := range ix.Files {
		syms += len(f.Symbols)
	}
	fmt.Fprintf(&b, "%s — %d files, %d symbols\n", root, len(ix.Files), syms)

	// Group files under their directory so each directory header prints once
	// (path-sorting alone interleaves root files with subdirectories).
	byDir := map[string][]File{}
	for _, f := range ix.Files {
		dir := path.Dir(f.Path)
		byDir[dir] = append(byDir[dir], f)
	}
	dirs := make([]string, 0, len(byDir))
	for d := range byDir {
		dirs = append(dirs, d)
	}
	sort.Strings(dirs)

	for _, dir := range dirs {
		header := dir + "/"
		if dir == "." {
			header = "./"
		}
		fmt.Fprintf(&b, "\n%s\n", header)
		files := byDir[dir]
		sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
		for _, f := range files {
			fmt.Fprintf(&b, "  %s (%d)\n", path.Base(f.Path), f.Lines)
			if line := renderSymbols(f.Symbols); line != "" {
				fmt.Fprintf(&b, "    %s\n", line)
			}
		}
	}
	return b.String()
}

// renderSymbols formats one file's symbols as "name:line · name:line · …",
// types first then funcs in declaration order, so the shape of a file reads at
// a glance. Returns "" when there are none.
func renderSymbols(syms []Symbol) string {
	if len(syms) == 0 {
		return ""
	}
	ordered := make([]Symbol, len(syms))
	copy(ordered, syms)
	sort.SliceStable(ordered, func(i, j int) bool {
		if (ordered[i].Kind == "type") != (ordered[j].Kind == "type") {
			return ordered[i].Kind == "type" // types lead
		}
		return false // otherwise keep declaration order
	})
	parts := make([]string, len(ordered))
	for i, s := range ordered {
		parts[i] = fmt.Sprintf("%s:%d", s.Name, s.Line)
	}
	return strings.Join(parts, " · ")
}
