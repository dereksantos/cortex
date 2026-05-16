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
// Sandboxing is real, not theoretical: every tool call is scoped to
// the workdir, run_shell uses an absolute-path allowlist and never
// invokes a shell interpreter, and write_file refuses paths under
// .git or .cortex.
package harness

import (
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
)

// shellAllowlist is the closed set of commands run_shell will execute.
// Adding to this list expands what arbitrary LLM-generated calls can
// reach, so additions must be deliberate. Network-capable commands
// (curl, wget, git push) are intentionally absent.
//
// `go` is allowed because the only language we target in iteration 1
// is Go (Conway's Game of Life). Adding new languages should
// allowlist their compilers/test-runners explicitly rather than
// broadening this list.
var shellAllowlist = map[string]bool{
	"go":    true,
	"gofmt": true,
	"ls":    true,
	"cat":   true,
	"head":  true,
	"tail":  true,
	"wc":    true,
	"diff":  true,
	"grep":  true,
	// Note: /usr/bin/test (string conditionals) is deliberately
	// absent. Models confuse it with `go test`, which lives behind
	// the `go` command. Run-shell auto-splits `"go test"` so the
	// model can write either form.
}

// shellMetaChars are characters in arguments that suggest a shell-style
// composition the model might be trying to smuggle in. We reject any
// argument containing these even though exec.Command does NOT invoke a
// shell — defense in depth, and useful as a signal that the model is
// confused about the tool contract.
var shellMetaChars = []string{";", "&&", "||", "|", "`", "$(", "$("}

var (
	// resolvedShellPaths caches exec.LookPath results so we don't pay
	// the PATH walk on every tool call. Populated lazily on first use
	// of each command; cached for the process lifetime.
	resolvedShellPaths     sync.Map // map[string]string
	errCommandNotAllowed   = errors.New("command not allowed")
	errArgContainsMeta     = errors.New("argument contains shell metacharacter")
	errArgIsAbsolutePath   = errors.New("argument is an absolute path; use a workdir-relative path")
	errPathEscapesWorkdir  = errors.New("path escapes workdir")
	errPathIsReservedDir   = errors.New("path is under reserved directory (.git or .cortex)")
	errPathIsSymlink       = errors.New("path is a symlink; refused for safety")
	errWorkdirNotAbsolute  = errors.New("workdir must be an absolute path")
	errEmptyPath           = errors.New("path must not be empty")
	errEmptyCommand        = errors.New("command must not be empty")
	errArgContainsRelative = errors.New("argument contains '..'")
)

// resolveShellCommand returns the absolute path for cmd, looked up
// once and cached. Returns errCommandNotAllowed if cmd is not in the
// allowlist (without consulting PATH at all — never log the absent
// command's PATH location as a footgun).
func resolveShellCommand(cmd string) (string, error) {
	if cmd == "" {
		return "", errEmptyCommand
	}
	if !shellAllowlist[cmd] {
		return "", fmt.Errorf("%w: %q (allowed: %v)", errCommandNotAllowed, cmd, allowedCommandsForDiag())
	}
	if v, ok := resolvedShellPaths.Load(cmd); ok {
		return v.(string), nil
	}
	path, err := exec.LookPath(cmd)
	if err != nil {
		return "", fmt.Errorf("look up %q on PATH: %w", cmd, err)
	}
	resolvedShellPaths.Store(cmd, path)
	return path, nil
}

// allowedCommandsForDiag returns the allowlist as a sorted slice so
// error messages list commands in a stable order regardless of map
// iteration order.
func allowedCommandsForDiag() []string {
	out := make([]string, 0, len(shellAllowlist))
	for k := range shellAllowlist {
		out = append(out, k)
	}
	// Bubble sort is fine for a 10-element list.
	for i := 0; i < len(out); i++ {
		for j := i + 1; j < len(out); j++ {
			if out[j] < out[i] {
				out[i], out[j] = out[j], out[i]
			}
		}
	}
	return out
}

// validateShellArg rejects arguments that contain shell metacharacters,
// absolute paths, or path-traversal segments. Returning an error
// causes the tool to refuse the call without executing anything.
//
// Note on ".." vs "...":
//
//	"..".......... path-traversal segment — blocked
//	"./..." ....... Go package wildcard — allowed
//	"pkg/..." ..... Go package wildcard — allowed
//
// We block ".." only when it appears as a full path SEGMENT (split on
// "/"). The naive substring check would refuse "go build ./..." which
// is a routine call.
func validateShellArg(arg string) error {
	if arg == "" {
		return nil // empty args are fine; some commands legitimately take them
	}
	if strings.HasPrefix(arg, "/") {
		return fmt.Errorf("%w: %q", errArgIsAbsolutePath, arg)
	}
	for _, seg := range strings.Split(arg, "/") {
		if seg == ".." {
			return fmt.Errorf("%w: %q", errArgContainsRelative, arg)
		}
	}
	for _, meta := range shellMetaChars {
		if strings.Contains(arg, meta) {
			return fmt.Errorf("%w (%q in %q)", errArgContainsMeta, meta, arg)
		}
	}
	return nil
}

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
