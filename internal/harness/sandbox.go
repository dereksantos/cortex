// Package harness implements Cortex's own LLM-driven coding-agent loop.
//
// Until this package landed, Cortex *observed* external coding harnesses
// (Aider, OpenCode, pi.dev, Claude CLI) and measured whether injected
// context improved their outputs. Now Cortex can host the loop itself,
// driving a small model via OpenRouter through a tool surface that
// includes file ops, a sandboxed shell, and `cortex_search` (which
// reads from a workdir-local Cortex store, never the user's personal
// one). The coding eval framework (internal/eval/v2/coding_runner.go)
// uses this package to implement scenarios like Conway's Game of Life.
//
// Sandboxing: file-write tools scope every path to the workdir and
// refuse writes under .git/.cortex. run_shell intentionally runs the
// model's command via `bash -c` — the trust model is "the user
// running cortex has opted into the agent running shell commands."
// User-controlled allow/deny shell policies are layered on top
// separately (see internal/harness/shell_policy.go when present).
package harness

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
)

var (
	errPathEscapesWorkdir = errors.New("path escapes workdir")
	errPathIsReservedDir  = errors.New("path is under reserved directory (.git or .cortex)")
	errPathIsSymlink      = errors.New("path is a symlink; refused for safety")
	errWorkdirNotAbsolute = errors.New("workdir must be an absolute path")
	errEmptyPath          = errors.New("path must not be empty")
	errArgIsAbsolutePath  = errors.New("path is absolute; use a workdir-relative path")
)

// reservedDirs are directories under workdir that tool writes must
// never touch. The agent owns the project tree, not Cortex's own
// state or git plumbing.
var reservedDirs = []string{".git", ".cortex"}

// containPath validates that rel resolves to a path inside workdir
// and not under any reserved directory. Returns the absolute path on
// success. The workdir must itself be absolute (defense against
// relative-workdir attacks where a caller's cwd could shift the
// containment check).
//
// rel may include ".." segments — filepath.Clean normalizes them
// before the prefix check. Symlinks at the final path component are
// rejected (we Lstat the target file when it exists).
func containPath(workdir, rel string) (string, error) {
	if !filepath.IsAbs(workdir) {
		return "", fmt.Errorf("%w: %q", errWorkdirNotAbsolute, workdir)
	}
	if rel == "" {
		return "", errEmptyPath
	}
	if filepath.IsAbs(rel) {
		return "", fmt.Errorf("%w: %q", errArgIsAbsolutePath, rel)
	}

	abs := filepath.Clean(filepath.Join(workdir, rel))

	// Ensure containment. We require either equality with workdir
	// (the dir itself) or a prefix that ends at a separator (so
	// /workdir-evil doesn't pass when workdir is /workdir).
	if abs != workdir && !strings.HasPrefix(abs, workdir+string(filepath.Separator)) {
		return "", fmt.Errorf("%w: %q resolves to %q", errPathEscapesWorkdir, rel, abs)
	}

	// Reserved-dir check: walk segments relative to workdir.
	relFromWorkdir, err := filepath.Rel(workdir, abs)
	if err != nil {
		return "", fmt.Errorf("rel: %w", err)
	}
	if relFromWorkdir != "." {
		parts := strings.Split(relFromWorkdir, string(filepath.Separator))
		for _, reserved := range reservedDirs {
			if len(parts) > 0 && parts[0] == reserved {
				return "", fmt.Errorf("%w: %q", errPathIsReservedDir, rel)
			}
		}
	}
	return abs, nil
}
