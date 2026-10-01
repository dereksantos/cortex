// project_commands_render.go — step 5 (issue #129): `cortex project
// commands [--json] [--project <name>]`, the CLI half of the project-command
// surface. It renders the RESOLVED command set (discovery + declarations)
// for a project so that what the post-edit hook runs is never a mystery —
// and so external consumers (the self-development loop's run.sh, other
// tooling) can pull the same format/lint/test/build commands cortex itself
// resolves, instead of re-deriving them.
//
// The render functions here are pure and golden/JSON-tested; resolution
// reuses projectcmd.Discover + projectcmd.Resolve (the same projectcmd.Resolve
// the hook in step 4 runs, and the one future completion receipts will reuse
// for fact-based reports).
package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/dereksantos/cortex/internal/projectcmd"
	"github.com/dereksantos/cortex/internal/registry"
	"github.com/dereksantos/cortex/internal/tools"
)

// ErrProjectCommandsUsage is returned (and printed) when `cortex project
// commands` gets an unrecognized argument.
var ErrProjectCommandsUsage = errors.New("usage: cortex project commands [--json] [--project <name>]")

// ProjectCommandInfo is one resolved command's display/JSON shape. Source is
// projectcmd's canonical provenance label: a manifest name ("go.mod",
// "package.json", ...) when discovered, "config.json" or "AGENTS.md" when
// declared. PerFile reports whether the command carries the {file}
// placeholder. RunsNow says whether the post-edit hook would auto-run it IN
// THIS WORKSPACE: the workspace must be trusted AND the role must be
// per-edit work (format needs {file}, lint needs {file}/{dir}; test and
// build are never hook-run).
type ProjectCommandInfo struct {
	Role    string `json:"role"`
	Command string `json:"command"`
	Source  string `json:"source"`
	PerFile bool   `json:"per_file,omitempty"`
	RunsNow bool   `json:"runs_now"`
}

// ProjectCommandsReport is the `cortex project commands` payload: which root
// the commands were resolved for, and the resolved commands in canonical
// role order. An empty Commands slice means the root had no recognized
// manifest and no declarations.
type ProjectCommandsReport struct {
	Root     string               `json:"root"`
	Commands []ProjectCommandInfo `json:"commands"`
}

// commandRolesInOrder is the canonical display order, matching
// projectcmd.Commands' own role fields (format, lint, test, build).
var commandRolesInOrder = []projectcmd.Role{
	projectcmd.RoleFormat,
	projectcmd.RoleLint,
	projectcmd.RoleTest,
	projectcmd.RoleBuild,
}

// commandsReportInfo projects a resolved Commands value onto its display/JSON
// shape in canonical role order, skipping roles no source supplied. Pure: a
// Commands in, a slice out — no filesystem, so it is golden-testable in
// isolation. trusted is the report's workspace trust state (the CLI passes
// the user config's verdict for the root it is resolving — the same state
// the post-edit hook in that workspace would see): on an untrusted
// workspace runs_now is false for every command.
func commandsReportInfo(cmds projectcmd.Commands, trusted bool) []ProjectCommandInfo {
	// A non-nil empty slice, so the JSON always carries "commands":[] (never
	// null) — the shape a run.sh consumer can rely on when a root resolves to
	// no commands.
	out := make([]ProjectCommandInfo, 0)
	for _, role := range commandRolesInOrder {
		c, ok := cmds.Get(role)
		if !ok {
			continue
		}
		out = append(out, ProjectCommandInfo{
			Role:    string(role),
			Command: c.Cmd,
			Source:  c.Source,
			PerFile: c.PerFile,
			RunsNow: tools.HookWouldRun(role, c, trusted),
		})
	}
	return out
}

// buildProjectCommandsReport resolves a root's commands (the same
// projectcmd.Resolve the post-edit hook runs) into a display report. It is
// the seam tests exercise end-to-end against a fixture tree. trusted is the
// report's workspace trust state — the CLI computes it from the user config
// (the report shows the same runs_now the hook would apply in that
// workspace); tests pass it directly.
func buildProjectCommandsReport(root string, cfg *Config, trusted bool) ProjectCommandsReport {
	return ProjectCommandsReport{
		Root:     root,
		Commands: commandsReportInfo(resolveProjectCommands(root, cfg), trusted),
	}
}

// renderProjectCommandsText renders the default (non-JSON) `cortex project
// commands` output — one line per resolved command: its source, and the
// post-edit hook's call for it in THIS workspace (runs now / not run).
// trusted is the report's workspace trust state (the same value that
// produced the runs_now fields) — the label logic is pure over
// (command, trusted). A provenance legend closes the block so discovered
// vs declared reads at a glance.
func renderProjectCommandsText(r ProjectCommandsReport, trusted bool) string {
	if len(r.Commands) == 0 {
		return fmt.Sprintf("No project commands discovered for %s\n", r.Root)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Project commands (%s):\n", r.Root)
	for _, c := range r.Commands {
		perFile := ""
		if c.PerFile {
			perFile = " [per-file]"
		}
		// The runs label must match runs_now: "trusted-workspace required"
		// means trusting the workspace WOULD make the hook run it (a
		// per-edit command in a role the hook runs — HookWouldRun(cmd, false)
		// false but HookWouldRun(cmd, true) true). Everything else that
		// doesn't run is "not run by the post-edit hook" — a test/build role
		// (never hook-run) or a whole-project format/lint — and on an
		// untrusted workspace nothing runs at all.
		runs := "not run by the post-edit hook"
		if c.RunsNow {
			runs = "runs now"
		} else if trusted &&
			!tools.HookWouldRun(projectcmd.Role(c.Role), projectcmd.Command{Cmd: c.Command, PerFile: c.PerFile}, false) &&
			tools.HookWouldRun(projectcmd.Role(c.Role), projectcmd.Command{Cmd: c.Command, PerFile: c.PerFile}, true) {
			runs = "trusted-workspace required"
		}
		fmt.Fprintf(&b, "  %-8s%s (%s)%s — %s\n", c.Role, c.Command, c.Source, perFile, runs)
	}
	b.WriteString("  source: manifest name = discovered; config.json / AGENTS.md = declared\n")
	b.WriteString("  runs now / trusted-workspace required: the post-edit hook's call for this workspace (cortex project trust); the hook runs only in a trusted workspace, and never runs test/build or whole-project commands per edit\n")
	return b.String()
}

// runProjectCommandsCLI is the `cortex project commands [--json] [--project
// <name>]` entry point (dispatched from runProjectCLI). Default target is
// the CWD-derived workspace root (the project the session would anchor to);
// --project <name> targets a registered project by name. Always exits 0 on
// a resolvable root — a root with no commands is a report, not a failure.
func runProjectCommandsCLI(args []string) {
	asJSON := false
	projectName := ""
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--json":
			asJSON = true
		case "--project":
			if i+1 >= len(args) {
				fmt.Fprintln(os.Stderr, "project commands:", ErrProjectCommandsUsage)
				os.Exit(1)
			}
			i++
			projectName = args[i]
		default:
			fmt.Fprintln(os.Stderr, "project commands:", ErrProjectCommandsUsage)
			os.Exit(1)
		}
	}

	root, cfg, err := projectCommandsTarget(projectName)
	if err != nil {
		fmt.Fprintln(os.Stderr, "project commands:", err)
		os.Exit(1)
	}
	// The report's runs_now must match the post-edit hook in THIS workspace:
	// the hook consults the merged config's USER-level trust list
	// (CortexSession.WorkspaceTrusted → Config.WorkspaceTrusted, which reads
	// Project.Trusted — user-level only, mergeProject drops the project copy).
	trusted := cfg != nil && cfg.WorkspaceTrusted(root)
	report := buildProjectCommandsReport(root, cfg, trusted)

	if asJSON {
		b, err := json.MarshalIndent(report, "", "  ")
		if err != nil {
			fmt.Fprintln(os.Stderr, "project commands:", err)
			os.Exit(1)
		}
		fmt.Println(string(b))
		return
	}
	fmt.Print(renderProjectCommandsText(report, trusted))
}

// projectCommandsTarget resolves the (root, config) the report is built from:
// --project <name> → the registry-resolved root plus that project's own
// config (not the CWD's); otherwise the CWD-derived workspace root plus the
// merged CWD config — the exact pair the post-edit hook uses in a REPL
// session, so the CLI shows the same commands the hook runs.
func projectCommandsTarget(projectName string) (root string, cfg *Config, err error) {
	if projectName != "" {
		reg, regErr := registry.New()
		if regErr != nil {
			return "", nil, fmt.Errorf("failed to open project registry: %w", regErr)
		}
		p, err := reg.Lookup(projectName)
		if err != nil {
			return "", nil, fmt.Errorf("project %q: %w", projectName, err)
		}
		return p.Root, configForRoot(p.Root), nil
	}
	ws := WorkspaceFromCWD()
	return ws.Root, LoadConfig(), nil
}

// configForRoot loads the merged config for an explicit root: the user config
// under that root's .cortex/config.json. A missing project config is an
// absent layer (loadMergedConfig's documented semantics), and a root with
// neither yields a nil config — resolveProjectCommands treats that as
// "no declarations, discovery only".
func configForRoot(root string) *Config {
	projPath := filepath.Join(root, ".cortex", "config.json")
	if _, statErr := os.Stat(projPath); statErr != nil {
		projPath = ""
	}
	return loadMergedConfig(userConfigPath(), projPath)
}
