// Package landscape probes a filesystem tree for the user's AI
// landscape: installed agent harnesses & editor integrations, local
// model runtimes, and git projects carrying AI markers.
//
// Detection is deterministic, read-only, and existence-only — no file
// contents are ever read (GOAL.md §3 P2's privacy stance: names and
// paths only). Each probe family is a Scanner with its own typed
// result (Tool, Runtime, Project); Scan composes all three against a
// single root.
package landscape

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/dereksantos/cortex/internal/projectscan"
)

// Tool is a detected AI agent harness or editor integration.
type Tool struct {
	Name string
	Path string
}

// Runtime is a detected local model runtime.
type Runtime struct {
	Name string
	Path string
}

// Project is a detected git repository carrying at least one AI
// marker, found while walking a scan root.
type Project struct {
	Path    string
	Markers []string
}

// Caps bounds a scan: max directory depth, max entries visited, and a
// hard wall-clock timeout. The zero value applies no bound. Enforced by
// ScanProjects, the only Scanner with an unbounded walk surface (its
// filepath.WalkDir over an arbitrary tree) — ScanHarnesses/ScanRuntimes
// each check a small fixed-length list of well-known relative paths
// directly under root with no recursion, so none of the three caps has
// a meaningful bound to apply there (GOAL.md M2.4 Decisions Log).
type Caps struct {
	MaxDepth   int
	MaxEntries int
	Timeout    time.Duration
}

// Result aggregates one scan's findings across every probe family.
// Truncated reports whether any cap (MaxDepth, MaxEntries, or Timeout)
// caused the underlying walk to stop early — only ScanProjects can trip
// it today, per Caps's doc comment.
type Result struct {
	Tools     []Tool
	Runtimes  []Runtime
	Projects  []Project
	Truncated bool
}

// HarnessScanner probes a root for agent-harness / editor-integration
// installations.
type HarnessScanner interface {
	Scan(root string, caps Caps) ([]Tool, error)
}

// RuntimeScanner probes a root for local model runtime installations.
type RuntimeScanner interface {
	Scan(root string, caps Caps) ([]Runtime, error)
}

// ProjectScanner probes a root for git repositories carrying AI
// markers. The bool return reports whether a Caps bound truncated the
// walk (GOAL.md M2.4).
type ProjectScanner interface {
	Scan(root string, caps Caps) ([]Project, bool, error)
}

// knownHarnesses maps a well-known name to the path (relative to a
// home-equivalent root) whose existence indicates it's installed.
var knownHarnesses = []struct {
	name string
	rel  string
}{
	{"claude", ".claude"},
	{"cursor", ".cursor"},
	{"codex", ".codex"},
	{"continue", ".continue"},
	{"github-copilot", filepath.Join(".config", "github-copilot")},
}

// knownRuntimes maps a well-known local model runtime name to its
// well-known relative path.
var knownRuntimes = []struct {
	name string
	rel  string
}{
	{"ollama", ".ollama"},
}

// aiMarkers are the file/dir names that mark a git repo as AI-tooled.
var aiMarkers = []string{"AGENTS.md", "CLAUDE.md", ".cursor", ".cortex"}

// wellKnownHarnessScanner is the default HarnessScanner: existence
// checks against knownHarnesses.
type wellKnownHarnessScanner struct{}

func (wellKnownHarnessScanner) Scan(root string, caps Caps) ([]Tool, error) {
	return ScanHarnesses(root, caps)
}

// wellKnownRuntimeScanner is the default RuntimeScanner: existence
// checks against knownRuntimes.
type wellKnownRuntimeScanner struct{}

func (wellKnownRuntimeScanner) Scan(root string, caps Caps) ([]Runtime, error) {
	return ScanRuntimes(root, caps)
}

// gitMarkerProjectScanner is the default ProjectScanner: walks root
// for ".git" entries carrying an AI marker.
type gitMarkerProjectScanner struct{}

func (gitMarkerProjectScanner) Scan(root string, caps Caps) ([]Project, bool, error) {
	return ScanProjects(root, caps)
}

// ScanHarnesses probes root for well-known agent-harness and editor
// installations. Detection is existence-only via os.Stat, which
// follows symlinks — a broken symlink resolves to a "not found" stat
// error and is treated as absent, never a crash.
func ScanHarnesses(root string, caps Caps) ([]Tool, error) {
	var found []Tool
	for _, h := range knownHarnesses {
		p := filepath.Join(root, h.rel)
		if _, err := os.Stat(p); err == nil {
			found = append(found, Tool{Name: h.name, Path: p})
		}
	}
	return found, nil
}

// ScanRuntimes probes root for well-known local model runtime
// installations, same existence-only detection as ScanHarnesses.
func ScanRuntimes(root string, caps Caps) ([]Runtime, error) {
	var found []Runtime
	for _, r := range knownRuntimes {
		p := filepath.Join(root, r.rel)
		if _, err := os.Stat(p); err == nil {
			found = append(found, Runtime{Name: r.name, Path: p})
		}
	}
	return found, nil
}

// ScanProjects walks root for git repositories — a ".git" entry that
// resolves via os.Stat (tolerant of submodule-style ".git" files;
// broken-symlink ".git" resolves to absent, not an error) — carrying
// at least one AI marker. Unreadable entries are skipped rather than
// aborting the whole walk: a partial landscape beats a crashed one.
// A detected repo is not descended into further — nested
// repos-within-repos are out of scope for this scanner.
//
// Every directory visited is filtered through a projectscan.IgnoreSet
// rooted at root: hard-excluded names (node_modules, vendor, .venv,
// build output, …) and .gitignore matches prune the walk before it
// descends, so a project nested under an excluded/ignored path is
// never discovered or reported. Each candidate marker path is
// filtered too (IsDirExcluded for directory markers like .cursor/
// .cortex, IsFileExcluded for file markers like AGENTS.md), so a
// marker that itself lives under excluded content is never counted.
//
// Caps bounds the walk (GOAL.md M2.4): MaxDepth prunes any directory
// deeper than the bound before descending into it; MaxEntries counts
// every entry (file or directory) the walk visits and stops the whole
// walk once the bound is exceeded; Timeout is a hard wall-clock
// deadline checked on every visit, also stopping the whole walk once
// passed. Any bound tripping is a clean stop (WalkDir's SkipDir/SkipAll
// sentinels, never a returned error) with the second return value set
// true — truncation is always reported, never silent. A zero Caps
// value applies no bound, matching every pre-M2.4 caller.
func ScanProjects(root string, caps Caps) ([]Project, bool, error) {
	ignores := projectscan.LoadIgnoreSet(root)
	var found []Project
	truncated := false
	entriesVisited := 0
	var deadline time.Time
	if caps.Timeout > 0 {
		deadline = time.Now().Add(caps.Timeout)
	}
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if !deadline.IsZero() && time.Now().After(deadline) {
			truncated = true
			return filepath.SkipAll
		}
		entriesVisited++
		if caps.MaxEntries > 0 && entriesVisited > caps.MaxEntries {
			truncated = true
			return filepath.SkipAll
		}
		if !d.IsDir() {
			return nil
		}
		if caps.MaxDepth > 0 && walkDepth(root, path) > caps.MaxDepth {
			truncated = true
			return fs.SkipDir
		}
		if path != root && ignores.IsDirExcluded(path, d.Name()) {
			return fs.SkipDir
		}
		if _, statErr := os.Stat(filepath.Join(path, ".git")); statErr != nil {
			return nil
		}
		var markers []string
		for _, m := range aiMarkers {
			markerPath := filepath.Join(path, m)
			info, statErr := os.Stat(markerPath)
			if statErr != nil {
				continue
			}
			if info.IsDir() {
				if ignores.IsDirExcluded(markerPath, info.Name()) {
					continue
				}
			} else if ignores.IsFileExcluded(markerPath) {
				continue
			}
			markers = append(markers, m)
		}
		if len(markers) == 0 {
			return nil
		}
		found = append(found, Project{Path: path, Markers: markers})
		return fs.SkipDir
	})
	if err != nil {
		return nil, truncated, err
	}
	return found, truncated, nil
}

// walkDepth reports path's directory depth relative to root: root
// itself is depth 0, a direct child is depth 1, and so on.
func walkDepth(root, path string) int {
	if path == root {
		return 0
	}
	rel, err := filepath.Rel(root, path)
	if err != nil || rel == "." {
		return 0
	}
	return len(strings.Split(rel, string(filepath.Separator)))
}

// Scan composes the three default Scanner implementations
// (HarnessScanner, RuntimeScanner, ProjectScanner) against the same
// root and aggregates their typed findings into a Result.
func Scan(root string, caps Caps) (Result, error) {
	var hs HarnessScanner = wellKnownHarnessScanner{}
	var rs RuntimeScanner = wellKnownRuntimeScanner{}
	var ps ProjectScanner = gitMarkerProjectScanner{}

	tools, err := hs.Scan(root, caps)
	if err != nil {
		return Result{}, err
	}
	runtimes, err := rs.Scan(root, caps)
	if err != nil {
		return Result{}, err
	}
	projects, truncated, err := ps.Scan(root, caps)
	if err != nil {
		return Result{}, err
	}
	return Result{Tools: tools, Runtimes: runtimes, Projects: projects, Truncated: truncated}, nil
}
