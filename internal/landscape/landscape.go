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
	"time"
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
// hard wall-clock timeout. The zero value applies no bound beyond
// each Scanner's own existence checks — depth/entry/timeout
// enforcement is a later increment (GOAL.md M2.4) that extends these
// same Scanner implementations without changing this signature.
type Caps struct {
	MaxDepth   int
	MaxEntries int
	Timeout    time.Duration
}

// Result aggregates one scan's findings across every probe family.
type Result struct {
	Tools    []Tool
	Runtimes []Runtime
	Projects []Project
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
// markers.
type ProjectScanner interface {
	Scan(root string, caps Caps) ([]Project, error)
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

func (gitMarkerProjectScanner) Scan(root string, caps Caps) ([]Project, error) {
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
func ScanProjects(root string, caps Caps) ([]Project, error) {
	var found []Project
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if !d.IsDir() {
			return nil
		}
		if _, statErr := os.Stat(filepath.Join(path, ".git")); statErr != nil {
			return nil
		}
		var markers []string
		for _, m := range aiMarkers {
			if _, statErr := os.Stat(filepath.Join(path, m)); statErr == nil {
				markers = append(markers, m)
			}
		}
		if len(markers) == 0 {
			return nil
		}
		found = append(found, Project{Path: path, Markers: markers})
		return fs.SkipDir
	})
	if err != nil {
		return nil, err
	}
	return found, nil
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
	projects, err := ps.Scan(root, caps)
	if err != nil {
		return Result{}, err
	}
	return Result{Tools: tools, Runtimes: runtimes, Projects: projects}, nil
}
