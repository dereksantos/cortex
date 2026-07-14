// project.go — M3.4: `cortex project add/list/remove` wired to
// internal/registry (GOAL.md §6 M3.4, docs/cortex-web.md Phase 3). CLI
// argument parsing mirrors scan.go's manual-switch style; the pure
// functions below (addProject, removeProject, renderProjectList,
// registerDiscoveredProjects) are what's tested — runProjectCLI itself is
// the thin os.Exit-driving wrapper dispatched from main.go, same
// convention as runScanCLI (see scan.go's Decisions Log entries).
package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/dereksantos/cortex/internal/landscape"
	"github.com/dereksantos/cortex/internal/registry"
)

// ErrProjectUsage is returned (and printed) when `cortex project` is
// called with a missing or malformed subcommand/argument list.
var ErrProjectUsage = errors.New("usage: cortex project <add <name> <root>|list|remove <name>>")

// addProject resolves root to an absolute path (relative to the current
// working directory, matching how a user would type it at a shell) and
// upserts name -> root into reg via Registry.Save's documented
// update-in-place semantics.
func addProject(reg registry.Registry, name, root string) error {
	if name == "" || root == "" {
		return fmt.Errorf("%w: add <name> <root>", ErrProjectUsage)
	}
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return fmt.Errorf("failed to resolve root %s: %w", root, err)
	}
	return reg.Save(registry.Project{Name: name, Root: absRoot})
}

// removeProject is a thin pass-through to Registry.Remove after
// validating the CLI-level argument shape — errors (including
// registry.ErrProjectNotFound) surface unwrapped.
func removeProject(reg registry.Registry, name string) error {
	if name == "" {
		return fmt.Errorf("%w: remove <name>", ErrProjectUsage)
	}
	return reg.Remove(name)
}

// renderProjectList renders the registered project list as `cortex
// project list`'s default text output (golden-pinned, same convention as
// renderScanReport/greetingPrompt — any intentional layout change needs a
// matching Decisions Log entry).
func renderProjectList(projects []registry.Project) string {
	if len(projects) == 0 {
		return "No projects registered. Add one: cortex project add <name> <root>\n"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Registered projects (%d):\n", len(projects))
	for _, p := range projects {
		fmt.Fprintf(&b, "  - %s -> %s\n", p.Name, p.Root)
	}
	return b.String()
}

// registerDiscoveredProjects feeds landscape-discovered projects (M2.1's
// ScanProjects results) into reg, keyed by directory basename — `cortex
// scan --register`'s half of M3.4. Existing entries with the same name
// are upserted (Registry.Save's documented semantics), so re-running
// --register over an unchanged tree does not duplicate entries.
func registerDiscoveredProjects(reg registry.Registry, projects []landscape.Project) error {
	for _, p := range projects {
		name := filepath.Base(p.Path)
		if err := reg.Save(registry.Project{Name: name, Root: p.Path}); err != nil {
			return fmt.Errorf("failed to register project %s: %w", name, err)
		}
	}
	return nil
}

// runProjectCLI is the `cortex project add|list|remove` entry point
// (dispatched from main.go). Not directly unit-tested — matches
// runScanCLI's established convention of leaving the os.Exit-driving
// wrapper untested while its composed pure functions carry the coverage.
func runProjectCLI(args []string) {
	reg, err := registry.New()
	if err != nil {
		fmt.Fprintln(os.Stderr, "project:", err)
		os.Exit(1)
	}
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "project:", ErrProjectUsage)
		os.Exit(1)
	}

	switch args[0] {
	case "add":
		if len(args) < 3 {
			fmt.Fprintln(os.Stderr, "project:", ErrProjectUsage)
			os.Exit(1)
		}
		if err := addProject(reg, args[1], args[2]); err != nil {
			fmt.Fprintln(os.Stderr, "project:", err)
			os.Exit(1)
		}
		fmt.Printf("registered %s\n", args[1])
	case "list":
		projects, err := reg.List()
		if err != nil {
			fmt.Fprintln(os.Stderr, "project:", err)
			os.Exit(1)
		}
		fmt.Print(renderProjectList(projects))
	case "remove":
		if len(args) < 2 {
			fmt.Fprintln(os.Stderr, "project:", ErrProjectUsage)
			os.Exit(1)
		}
		if err := removeProject(reg, args[1]); err != nil {
			fmt.Fprintln(os.Stderr, "project:", err)
			os.Exit(1)
		}
		fmt.Printf("removed %s\n", args[1])
	default:
		fmt.Fprintln(os.Stderr, "project:", ErrProjectUsage)
		os.Exit(1)
	}
}
