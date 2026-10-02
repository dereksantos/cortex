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
	// Explicit records that Root was given outright (NewWorkspace — the
	// --project / serve / loop-firing leg) rather than derived from the
	// working directory. Only explicit workspaces anchor tool execution
	// (CortexSession.Workdir): a CWD-derived workspace must keep today's
	// CWD-relative tool behavior byte-identical.
	Explicit bool
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
	return &Workspace{Root: abs, Explicit: true}, nil
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

// Instructions resolves and returns the workspace's project instructions:
// the first entry of agentInstructionFiles present at w.Root (AGENTS.md
// first, then CLAUDE.md, then .github/copilot-instructions.md — no
// concatenation), trimmed and truncated at the instruction cap with a
// marker naming the file. It returns the resolved file's path ("" when none
// exists) alongside its body — the identical contract to
// projectInstructions(), which resolves the same file at the CWD-implicit
// root — so the caller can name the loaded file (systemPromptContent's
// label, #147).
func (w *Workspace) Instructions() (path, instructions string) {
	return workspaceInstructions(w.Root)
}

// workspaceInstructions resolves an explicit root's instruction file the
// same way #152's instruction-file resolution does: the FIRST entry of
// agentInstructionFiles present at root (AGENTS.md, then CLAUDE.md, then
// .github/copilot-instructions.md — no concatenation), read with the same
// instructionBytesCap (readInstructions) the system prompt's seed uses. It
// returns the resolved file's path ("") and its body, so both legs —
// Workspace.Instructions (explicit root) and the root-direct reads the
// `## Commands` declaration takes (session_core.go's
// resolveProjectCommands) — label and parse the SAME file a system prompt
// would load from that root.
func workspaceInstructions(root string) (path, instructions string) {
	p := resolveInstructionFile(root)
	if p == "" {
		return "", ""
	}
	return p, readInstructions(p)
}

// ConfinePath vets a tool call's path argument against this workspace's
// root via internal/tools.ConfinePath — the door guard study's dispatcher
// applies to every path-taking tool call.
func (w *Workspace) ConfinePath(call ToolCall) (ToolCall, error) {
	return tools.ConfinePath(call, w.Root)
}
