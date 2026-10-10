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

// ProjectCommandInfo is one resolved command's display/JSON shape. Role is
// the command's role, Source its provenance: a manifest name ("go.mod",
// "package.json", ...) when discovered, "config.json" when declared in
// project.commands, or the resolved instruction file's name ("AGENTS.md",
// "CLAUDE.md", ".github/copilot-instructions.md") when declared in the
// `## Commands` section. When is when the post-edit hook runs it NOW, for
// THIS workspace and session: "per-edit" (a per-file format command, in a
// trusted workspace with mode >= format), "turn-end" (a lint with
// {file}/{dir}, in a trusted workspace in mode all), "never" (the test and
// build roles, or a whole-project format/lint — the hook never auto-runs
// those), "inactive: workspace untrusted" (nothing runs on an untrusted
// workspace), or "inactive: hook mode <mode>" (a trusted workspace whose
// hook mode would not run this role now).
type ProjectCommandInfo struct {
	Role    string `json:"role"`
	Command string `json:"command"`
	Source  string `json:"source"`
	When    string `json:"when"`
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
// the post-edit hook in that workspace would see); mode is the session's
// CURRENT effective hook mode (off/format/all). The two together decide each
// command's When: an untrusted workspace is "inactive: workspace untrusted"
// for every command, and a trusted workspace's per-edit / turn-end roles are
// further gated by mode (off runs nothing; format skips turn-end lint).
func commandsReportInfo(cmds projectcmd.Commands, trusted bool, mode tools.HookMode) []ProjectCommandInfo {
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
			When:    commandWhen(role, c, trusted, mode),
		})
	}
	return out
}

// commandWhen is the report's "when does this command run" verdict for one
// resolved command, in the exact order the hook's own gates apply:
//
//  1. untrusted workspace — NOTHING runs (trust is the hard gate, ahead of
//     the mode): "inactive: workspace untrusted";
//  2. the test and build roles — never auto-run by the hook, trusted or
//     not: "never";
//  3. the role's applicability — a whole-project format (no {file}) and a
//     whole-project lint (no {file}/{dir}) have no per-edit / per-turn
//     target, so the hook never auto-runs them: "never";
//  4. the mode — "off" runs nothing; "format" runs the per-edit format but
//     skips the turn-end lint; "all" runs both. A role the current mode
//     would not run is "inactive: hook mode <mode>" (named so an operator
//     sees exactly which setting to change);
//  5. otherwise the role's cadence: "per-edit" for the format role,
//     "turn-end" for the lint role.
//
// It mirrors tools.HookWouldRun / tools.roleApplicability one-for-one, so
// the report's When can never drift from what the hook actually runs.
func commandWhen(role projectcmd.Role, cmd projectcmd.Command, trusted bool, mode tools.HookMode) string {
	if !trusted {
		return "inactive: workspace untrusted"
	}
	switch role {
	case projectcmd.RoleTest, projectcmd.RoleBuild:
		return "never"
	}
	if !roleRunnable(role, cmd) {
		return "never"
	}
	switch mode {
	case tools.HookModeOff:
		return "inactive: hook mode off"
	case tools.HookModeFormat:
		if role == projectcmd.RoleFormat {
			return "per-edit"
		}
		return "inactive: hook mode format"
	default: // tools.HookModeAll
		if role == projectcmd.RoleFormat {
			return "per-edit"
		}
		return "turn-end"
	}
}

// roleRunnable is the role half of the hook's applicability (mirrors
// tools.roleApplicability, which the report must stay in lockstep with):
// the format role runs only a PER-FILE command (a whole-project format has
// no argument to substitute for the one file just touched), and the lint
// role only a command carrying the {file} or {dir} token (a whole-project
// lint has no per-touched-file target). The test and build roles are never
// hook-run at all (handled before this is called).
func roleRunnable(role projectcmd.Role, cmd projectcmd.Command) bool {
	switch role {
	case projectcmd.RoleFormat:
		return cmd.PerFile
	case projectcmd.RoleLint:
		return cmd.PerFile || strings.Contains(cmd.Cmd, projectcmd.DirPlaceholder)
	default:
		return false
	}
}

// buildProjectCommandsReport resolves a root's commands (the same
// projectcmd.Resolve the post-edit hook runs) into a display report. It is
// the seam tests exercise end-to-end against a fixture tree. trusted is the
// report's workspace trust state and mode the session's current effective
// hook mode — the CLI computes both the same way a session in that
// workspace would (trust from the user config, mode from
// Config.postEditHookMode), so the report's When fields show the same
// verdict the hook would apply there; tests pass them directly.
func buildProjectCommandsReport(root string, cfg *Config, trusted bool, mode tools.HookMode) ProjectCommandsReport {
	return ProjectCommandsReport{
		Root:     root,
		Commands: commandsReportInfo(resolveProjectCommands(root, cfg), trusted, mode),
	}
}

// renderProjectCommandsText renders the default (non-JSON) `cortex project
// commands` output — one line per resolved command: role, command, source,
// and when it runs now (the When verdict, already baked into the report).
// A provenance legend closes the block so discovered vs declared reads at a
// glance, and a "when" legend names the hook's own vocabulary so the label
// on each line is self-explanatory.
func renderProjectCommandsText(r ProjectCommandsReport) string {
	if len(r.Commands) == 0 {
		return fmt.Sprintf("No project commands discovered for %s\n", r.Root)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Project commands (%s):\n", r.Root)
	for _, c := range r.Commands {
		fmt.Fprintf(&b, "  %-6s %s (%s) — %s\n", c.Role, c.Command, c.Source, c.When)
	}
	b.WriteString("  source: manifest name = discovered; config.json / instruction file (AGENTS.md, CLAUDE.md, …) = declared\n")
	b.WriteString("  when: per-edit (the post-edit hook, after each write/edit), turn-end (the turn-end lint pass), never (the hook never auto-runs it), or inactive (this workspace or hook mode does not run it)\n")
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
	// The report's When fields must match the post-edit hook in THIS
	// workspace: the hook consults the merged config's USER-level trust list
	// (CortexSession.WorkspaceTrusted → Config.WorkspaceTrusted, which reads
	// Project.Trusted — user-level only, mergeProject drops the project copy)
	// and the resolved hook mode (Config.postEditHookMode: the
	// CORTEX_POST_EDIT_HOOK env var, then the merged tools.post_edit_hook,
	// then the default all — the same value NewCortexSession installs as the
	// process ceiling). A nil config means "default mode, untrusted" —
	// exactly the session's default for a root with no config.
	trusted := cfg != nil && cfg.WorkspaceTrusted(root)
	var mode tools.HookMode
	if cfg != nil {
		mode = cfg.postEditHookMode()
	} else {
		mode = tools.HookModeAll
	}
	report := buildProjectCommandsReport(root, cfg, trusted, mode)

	if asJSON {
		b, err := json.MarshalIndent(report, "", "  ")
		if err != nil {
			fmt.Fprintln(os.Stderr, "project commands:", err)
			os.Exit(1)
		}
		fmt.Println(string(b))
		return
	}
	fmt.Print(renderProjectCommandsText(report))
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
