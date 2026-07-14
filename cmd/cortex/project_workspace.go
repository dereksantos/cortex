// project_workspace.go — M3.5: `--project <name>` on turn, resume, and
// study (GOAL.md §6 M3.5, docs/cortex-web.md Phase 3). Resolves a
// registered project name (internal/registry, M3.3/M3.4) to its root,
// builds an explicit Workspace (NewWorkspace, M3.1) for it, and re-targets
// an already-constructed *CortexSession at that root instead of the
// CWD-implicit default NewCortexSession() gave it — so ContextDir/
// SessionsDir, the system prompt's project instructions, and tool-path
// confinement (cs.root(), study.go's ConfinePath) all resolve against the
// project, matching the CWD-vs-explicit-root equivalence M3.1 already
// proved for Workspace itself.
package main

import (
	"fmt"

	"github.com/dereksantos/cortex/internal/registry"
)

// parseProjectFlag extracts a "--project <name>" pair from args (any
// position), returning the resolved name (empty if absent) and the
// remaining args with the flag and its value removed. Shared by the
// turn/resume/study CLI entry points so each composes --project with its
// own flags/positional arguments without hand-parsing it individually. A
// dangling "--project" with no following value is left in rest untouched
// (not treated as the flag) rather than silently swallowed.
func parseProjectFlag(args []string) (name string, rest []string) {
	rest = make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		if args[i] == "--project" && i+1 < len(args) {
			name = args[i+1]
			i++
			continue
		}
		rest = append(rest, args[i])
	}
	return name, rest
}

// applyProjectByName resolves name via reg and re-targets cs at that
// project's Workspace: cs.workspace and cs.deleteRoot (the confinement root
// cs.root() prefers, study.go) both become the project's root, and the
// system prompt's project-instructions section is rebuilt from the new
// root's AGENTS.md (systemPromptContent, session_core.go) in place of the
// CWD-implicit projectInstructions() CortexArgs.Request() used at
// construction.
func applyProjectByName(cs *CortexSession, reg registry.Registry, name string) error {
	p, err := reg.Lookup(name)
	if err != nil {
		return fmt.Errorf("failed to resolve project %q: %w", name, err)
	}
	ws, err := NewWorkspace(p.Root)
	if err != nil {
		return fmt.Errorf("failed to build workspace for project %q: %w", name, err)
	}
	cs.workspace = ws
	cs.deleteRoot = ws.Root
	if len(cs.Request.Messages) > 0 && cs.Request.Messages[0].Role == RoleSystem {
		cs.Request.Messages[0].Content = systemPromptContent(ws.Instructions())
	}
	return nil
}

// applyProjectFlag is the end-to-end helper the turn/resume/study CLI entry
// points call after parsing --project: a no-op when name is empty (the
// CWD-implicit default every command already had before M3.5), otherwise
// opens the registry and applies it.
func applyProjectFlag(cs *CortexSession, name string) error {
	if name == "" {
		return nil
	}
	reg, err := registry.New()
	if err != nil {
		return fmt.Errorf("failed to open project registry: %w", err)
	}
	return applyProjectByName(cs, reg, name)
}
