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
	if ix.SingleFile && len(ix.Files) == 1 {
		return renderSkeleton(ix.Files[0])
	}
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

// renderSymbols formats a file's navigable symbols (funcs and types only — the
// compact directory view) as "name:line · …", types first then funcs in
// declaration order. const/var are omitted here; the single-file skeleton shows
// them. Returns "" when there are none.
func renderSymbols(syms []Symbol) string {
	var nav []Symbol
	for _, s := range syms {
		if s.Kind == "func" || s.Kind == "type" {
			nav = append(nav, s)
		}
	}
	if len(nav) == 0 {
		return ""
	}
	sort.SliceStable(nav, func(i, j int) bool {
		if (nav[i].Kind == "type") != (nav[j].Kind == "type") {
			return nav[i].Kind == "type" // types lead
		}
		return false // otherwise keep declaration order
	})
	parts := make([]string, len(nav))
	for i, s := range nav {
		parts[i] = fmt.Sprintf("%s:%d", s.Name, s.Line)
	}
	return strings.Join(parts, " · ")
}

// renderSkeleton is the single-file view the "what are the seams" use case wants:
// every top-level declaration in file order, one per line, with its line number
// and kind — a cleaner native form of `grep -nE '^(func|type|const|var)'`.
func renderSkeleton(f File) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s — %d lines, %d declarations\n\n", f.Path, f.Lines, len(f.Symbols))
	for _, s := range f.Symbols {
		fmt.Fprintf(&b, "  %-6s %-5s %s\n", fmt.Sprintf("L%d", s.Line), s.Kind, s.Name)
	}
	return b.String()
}
