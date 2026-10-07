package tools

import (
	"os"
	"path/filepath"
	"strings"
)

// workdir.go anchors relative tool paths to a session's workspace root.
//
// A long-lived process (cortex serve, a loop firing) hosts sessions for many
// registered projects, so the process CWD belongs to none of them: a tool
// call's relative path must resolve against the SESSION's workspace root,
// not wherever the process happens to have been started. Confinement
// (ConfinePath) already checks against the root; this closes the matching
// execution gap.
//
// Resolution happens at the filesystem boundary only — display strings, tool
// results, and error messages keep the model-visible relative path, so
// CWD-rooted sessions (the REPL) stay byte-identical.

// Workdirer is an OPTIONAL ToolDeps capability, asserted dynamically so
// existing ToolDeps implementations are untouched. A non-empty Workdir
// anchors relative paths (and bash's working directory); empty means
// CWD-relative, today's behavior.
type Workdirer interface {
	Workdir() string
}

// workdirOf extracts the optional workdir from deps ("" when absent).
func workdirOf(deps ToolDeps) string {
	w, ok := deps.(Workdirer)
	if !ok {
		return ""
	}
	return strings.TrimSpace(w.Workdir())
}

// workdirRootForErrors reports the workspace root a not-found error states
// (issue #142): deps' anchored workdir when present, else the process CWD —
// the workspace every CWD-rooted session (the default REPL, cortex turn)
// resolves relative to. "" only when neither is resolvable. Kept separate
// from resolveWorkdir on purpose: this one falls back to the CWD (error
// MESSAGES name the root), while filesystem resolution must stay CWD-relative
// when no workdir is set.
func workdirRootForErrors(deps ToolDeps) string {
	if wd := workdirOf(deps); wd != "" {
		return wd
	}
	if wd, err := os.Getwd(); err == nil {
		return wd
	}
	return ""
}

// resolveWorkdir returns path anchored to deps' workdir when path is
// relative and a workdir is present; otherwise path unchanged. Callers use
// the return value for filesystem access ONLY, keeping the original path in
// anything the model or user reads.
func resolveWorkdir(deps ToolDeps, path string) string {
	if path == "" || filepath.IsAbs(path) {
		return path
	}
	wd := workdirOf(deps)
	if wd == "" {
		return path
	}
	return filepath.Join(wd, path)
}
