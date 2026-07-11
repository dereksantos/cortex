package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/dereksantos/cortex/internal/tools"
)

// workspace.go introduces the explicit Workspace GOAL.md §3 P3 calls for: a
// root path plus its derived .cortex dir and AGENTS.md instructions path,
// constructed once and threaded through session construction, contextDir,
// instructions discovery, and ConfinePath. This first pass is deliberately
// behavior-preserving: WorkspaceFromCWD() reproduces findUp(".cortex")'s
// existing upward-search exactly, so every CWD-implicit call site keeps
// today's behavior bit-for-bit. NewWorkspace(root) is the explicit-root leg
// --project (M3.5) builds on; it takes no part in resolution yet.

// Workspace is a resolved project root plus its derived paths. The zero
// value is not valid — construct via NewWorkspace or WorkspaceFromCWD.
type Workspace struct {
	// Root is the absolute workspace root directory.
	Root string
}

// NewWorkspace resolves an explicit workspace root (absolute or relative to
// the current working directory) with NO upward search — the root is taken
// as given. This is the leg --project (M3.5) will use once the registry
// resolves a project name to a root.
func NewWorkspace(root string) (*Workspace, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("resolve workspace root %q: %w", root, err)
	}
	return &Workspace{Root: abs}, nil
}

// WorkspaceFromCWD resolves a Workspace the way every existing CWD-implicit
// call site already does: findUp(".cortex") walks up from the working
// directory looking for an existing .cortex dir; its parent becomes the
// root. When no .cortex dir is found anywhere up the tree (a fresh
// workspace), the root is the working directory itself — matching
// contextDir()'s existing fallback (a relative "./.cortex" is created under
// CWD on first write).
func WorkspaceFromCWD() *Workspace {
	if found := findUp(".cortex"); found != "" {
		if abs, err := filepath.Abs(filepath.Dir(found)); err == nil {
			return &Workspace{Root: abs}
		}
	}
	wd, err := os.Getwd()
	if err != nil {
		// Matches contextDir()'s own last-resort: a relative path is still a
		// usable (if fragile) root when even os.Getwd() fails.
		return &Workspace{Root: "."}
	}
	return &Workspace{Root: wd}
}

// ContextDir is the workspace's .cortex directory (sessions, journal,
// config, history, log — everything session.go's contextDir() callers
// resolve today).
func (w *Workspace) ContextDir() string { return filepath.Join(w.Root, ".cortex") }

// SessionsDir is the workspace's session transcript directory.
func (w *Workspace) SessionsDir() string { return filepath.Join(w.ContextDir(), "sessions") }

// Instructions reads and returns the workspace's AGENTS.md content
// (trimmed, truncated at maxInstructionBytes), or "" if absent/unreadable —
// identical contract to the existing projectInstructions().
func (w *Workspace) Instructions() string {
	return readInstructions(filepath.Join(w.Root, "AGENTS.md"))
}

// ConfinePath vets a tool call's path argument against this workspace's
// root via internal/tools.ConfinePath — the door guard study's dispatcher
// applies to every path-taking tool call.
func (w *Workspace) ConfinePath(call ToolCall) (ToolCall, error) {
	return tools.ConfinePath(call, w.Root)
}
