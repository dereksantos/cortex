// Package projectcmd discovers a project's own format/lint/test/build
// commands from its manifest files — go.mod, package.json, pyproject.toml,
// Cargo.toml, Makefile — so the harness runs the project's own checks
// instead of assuming a toolchain (issue #129: the local model kept
// forgetting gofmt, so unformatted files reached review).
//
// Discovery is pure reading: Discover(root) inspects the manifest files
// at root's top level and returns the first command declared per role.
// Nothing here executes a command; execution and its shellrisk gating
// live with the caller. Per-file commands carry the {file} placeholder,
// which the caller substitutes with the file it just touched; per-package
// commands carry the {dir} placeholder, which the caller substitutes with
// the file's "./"-prefixed package directory relative to the project root;
// commands without either apply to the whole project.
package projectcmd

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// Role is one of the four command categories a project declares.
type Role string

// The command roles, in canonical render order.
const (
	RoleFormat Role = "format"
	RoleLint   Role = "lint"
	RoleTest   Role = "test"
	RoleBuild  Role = "build"
)

// FilePlaceholder is the marker in per-file commands for the argument
// the caller substitutes with the file it just touched
// (e.g. "gofmt -w {file}"). Commands without the placeholder apply to
// the whole project (e.g. "cargo fmt", "go test ./...").
const FilePlaceholder = "{file}"

// DirPlaceholder marks a per-PACKAGE target: the caller substitutes it
// with the {"./"}-prefixed directory of the touched file, relative to the
// project root ({"./"} for a root-level file) — e.g. "go vet {dir}" type-checks
// the touched file's whole package, which is the correct unit for
// cross-file tools (a per-FILE "go vet a.go" type-checks that one file as
// its own package and reports spurious undefined: errors in any
// multi-file package).
const DirPlaceholder = "{dir}"

// Command is one discovered command and where it came from.
type Command struct {
	// Cmd is the shell command line.
	Cmd string
	// PerFile reports whether Cmd carries the {file} placeholder — a
	// command the post-edit hook may run against the single file just
	// touched. ({dir} is NOT per-file: it names the file's package, and the
	// hook runs it with the package dir substituted so cross-file tools like
	// go vet type-check the whole package.)
	PerFile bool
	// Extends lists the source-file extensions this command applies to
	// (".go", ".py"), lowercase. Non-empty, the post-edit hook skips files
	// whose extension is not listed — gofmt on a README.md is not a
	// formatting run, it is a spurious error note on every non-source
	// edit. EMPTY means "no recognized toolchain": declared commands and
	// manifest scripts the discovery can't map to a source set (make
	// targets, package.json scripts, cargo) run on every written file.
	Extends []string
	// Source names the manifest that supplied the command ("go.mod",
	// "package.json", ...), so a rendered report can show where each
	// command was found.
	Source string
}

// Commands is a project's discovered command set. Any role may be
// absent (zero Command) when no manifest supplies it.
type Commands struct {
	Format Command
	Lint   Command
	Test   Command
	Build  Command
	// Roots lists the manifests that contributed at least one command,
	// in discovery order.
	Roots []string
}

// Get returns the command for role and whether one was discovered.
func (c Commands) Get(role Role) (Command, bool) {
	var cmd Command
	switch role {
	case RoleFormat:
		cmd = c.Format
	case RoleLint:
		cmd = c.Lint
	case RoleTest:
		cmd = c.Test
	case RoleBuild:
		cmd = c.Build
	default:
		return Command{}, false
	}
	return cmd, cmd.Cmd != ""
}

// Render is a human-readable report of the discovered commands — one
// line per role in canonical order, "(none discovered)" for a role no
// manifest supplied.
func (c Commands) Render() string {
	lines := make([]string, 0, 4)
	for _, role := range []Role{RoleFormat, RoleLint, RoleTest, RoleBuild} {
		cmd, ok := c.Get(role)
		if !ok {
			lines = append(lines, fmt.Sprintf("%-6s (none discovered)", string(role)))
			continue
		}
		lines = append(lines, fmt.Sprintf("%-6s %s (%s)", string(role), cmd.Cmd, cmd.Source))
	}
	return strings.Join(lines, "\n")
}

// sourceOrder is the discovery order: the first source to supply a role
// wins, and later sources only fill roles no earlier source covered.
// Recognized manifests are strongest (they name the toolchain); a
// bare Makefile is the weakest signal and mostly fills gaps.
var sourceOrder = []string{"go.mod", "package.json", "pyproject.toml", "Cargo.toml", "Makefile"}

// Discover inspects the project manifests at root's top level and
// returns the commands they declare. A directory with no recognized
// manifest yields an empty Commands and a nil error; a missing root, or
// a root that is not a directory, is an error.
func Discover(root string) (Commands, error) {
	info, err := os.Stat(root)
	if err != nil {
		return Commands{}, fmt.Errorf("discover project commands: %w", err)
	}
	if !info.IsDir() {
		return Commands{}, fmt.Errorf("discover project commands: %s is not a directory", root)
	}

	var out Commands
	rootSeen := map[string]bool{}
	for _, name := range sourceOrder {
		data, err := os.ReadFile(filepath.Join(root, name))
		if err != nil {
			continue // no such manifest
		}
		var cmds map[Role]Command
		switch name {
		case "go.mod":
			cmds = goCommands()
		case "package.json":
			cmds = packageJSONCommands(data)
		case "pyproject.toml":
			cmds = pyprojectCommands(data)
		case "Cargo.toml":
			cmds = cargoCommands()
		case "Makefile":
			cmds = makefileCommands(data)
		}
		for _, role := range []Role{RoleFormat, RoleLint, RoleTest, RoleBuild} {
			c, ok := cmds[role]
			if !ok {
				continue
			}
			if _, have := out.Get(role); have {
				continue
			}
			c.PerFile = strings.Contains(c.Cmd, FilePlaceholder)
			c.Source = name
			switch role {
			case RoleFormat:
				out.Format = c
			case RoleLint:
				out.Lint = c
			case RoleTest:
				out.Test = c
			case RoleBuild:
				out.Build = c
			}
			if !rootSeen[name] {
				out.Roots = append(out.Roots, name)
				rootSeen[name] = true
			}
		}
	}
	return out, nil
}

// goCommands is the Go convention: go.mod is present, so the toolchain
// is the go tool itself. gofmt rewrites in place (-w), so format is
// per-file; lint is PER-PACKAGE ({dir}): `go vet <one file>` type-checks
// that file as its own package, so any symbol defined in a sibling file
// reports a spurious undefined: — nearly every real multi-file package
// would get a false lint failure on every edit. go vet on the package
// (go vet ./<dir>) type-checks all of the package's files together and
// reports only what is genuinely wrong.
func goCommands() map[Role]Command {
	return map[Role]Command{
		RoleFormat: {Cmd: "gofmt -w {file}", Extends: []string{".go"}},
		RoleLint:   {Cmd: "go vet {dir}", Extends: []string{".go"}},
		RoleTest:   {Cmd: "go test ./..."},
		RoleBuild:  {Cmd: "go build ./..."},
	}
}

// npmScriptKeys, in check order, are the script names projects use for
// each role in package.json.
var npmScriptKeys = map[Role][]string{
	RoleFormat: {"format", "fmt", "format:all", "format:write", "format:fix"},
	RoleLint:   {"lint", "lint:js", "lint:all", "lint:fix", "check:lint", "check"},
	RoleTest:   {"test", "test:all"},
	RoleBuild:  {"build", "build:all"},
}

// npmTestAlias reports whether key is an npm alias for the test role: npm
// treats "run test" and "test" as the same invocation (both run the test
// script plus its pre/post hooks), so the test script is reported as the
// bare "npm test".
func npmTestAlias(key string) bool { return key == "test" }

// npmDepFallback names the dependency that implies a command when no
// script declares one: a project that pins prettier/eslint in its
// dependencies almost always runs it through npx. The fallback commands
// are per-file and carry the JS/TS extension set — prettier/eslint run
// on JavaScript/TypeScript sources, not on a README.md.
//
// Scripts DECLARED in package.json (whatever their text) carry NO
// extends: discovery can't infer the source set from an arbitrary script
// line, so such commands apply to all files — the same convention as a
// hand declaration.
var npmDepFallback = map[Role]struct {
	dep     string
	cmd     string
	extends []string
}{
	RoleFormat: {"prettier", "npx prettier --write {file}", jsTSExtends},
	RoleLint:   {"eslint", "npx eslint {file}", jsTSExtends},
}

// jsTSExtends is the source-extension set prettier/eslint apply to — the
// usual JS/TS set (plus the common non-code assets prettier formats), so
// the post-edit hook only fires on those, not on a .md or .go in a Node
// repo. (prettier happily formats .md/.json/.css too, so they're in the
// set — "the usual JS/TS set" plus what prettier is known for.)
var jsTSExtends = []string{".js", ".jsx", ".ts", ".tsx", ".mjs", ".cjs", ".json", ".css", ".scss", ".md", ".html"}

// packageJSONCommands reads the "scripts" object of a package.json. A
// script is reported as "npm run <script>" (or the bare "npm test" for the
// test script): the script body alone (e.g. "tsc -p tsconfig.json") only
// runs inside npm, which puts node_modules/.bin on PATH and runs the
// script's pre/post hooks — run from a shell, the body fails with
// "tsc: command not found". A malformed manifest contributes nothing
// (discovery must never fail the caller over a broken package.json).
func packageJSONCommands(data []byte) map[Role]Command {
	var pkg struct {
		Scripts         map[string]string `json:"scripts"`
		Dependencies    map[string]string `json:"dependencies"`
		DevDependencies map[string]string `json:"devDependencies"`
	}
	if err := json.Unmarshal(data, &pkg); err != nil {
		return nil
	}
	dep := func(name string) bool {
		_, inDev := pkg.DevDependencies[name]
		_, inProd := pkg.Dependencies[name]
		return inDev || inProd
	}
	cmds := map[Role]Command{}
	for _, role := range []Role{RoleFormat, RoleLint, RoleTest, RoleBuild} {
		for _, key := range npmScriptKeys[role] {
			if s, ok := pkg.Scripts[key]; ok && strings.TrimSpace(s) != "" {
				cmds[role] = Command{Cmd: npmScriptCommand(key)}
				break
			}
		}
		if _, ok := cmds[role]; !ok {
			if fb, ok := npmDepFallback[role]; ok && dep(fb.dep) {
				cmds[role] = Command{Cmd: fb.cmd, Extends: fb.extends}
			}
		}
	}
	return cmds
}

// npmScriptCommand is the runnable form of a package.json script: "npm run
// <script>", except the test script, which npm aliases to the bare "npm
// test" (identical semantics — npm treats "run test" and "test" as the
// same invocation). The script body is never reported directly: it only
// runs inside npm, which puts node_modules/.bin on PATH and runs pre/post
// hooks (a "build" of "tsc -p tsconfig.json" fails with "tsc: command not
// found" from a shell, but "npm run build" works).
func npmScriptCommand(key string) string {
	if npmTestAlias(key) {
		return "npm test"
	}
	return "npm run " + key
}

// tomlHeaderRe matches a top-level [table] header line (already
// trimmed). Only table names are extracted — the stdlib has no TOML
// parser, and header presence is all discovery needs.
var tomlHeaderRe = regexp.MustCompile(`^\[([A-Za-z0-9_.\-]+)\]$`)

// pythonExtends is the source set ruff/black apply to — Python sources.
var pythonExtends = []string{".py", ".pyi"}

// pyprojectCommands reads pyproject.toml's table headers: [tool.ruff]
// wins over [tool.black] for format (and only ruff supplies lint),
// pytest is the Python test convention, and a [project]/[build-system]
// table means "python -m build" can package it.
func pyprojectCommands(data []byte) map[Role]Command {
	hasRuff, hasBlack, hasBuild := false, false, false
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		m := tomlHeaderRe.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		h := strings.ToLower(m[1])
		switch {
		case h == "tool.ruff" || strings.HasPrefix(h, "tool.ruff."):
			hasRuff = true
		case h == "tool.black" || strings.HasPrefix(h, "tool.black."):
			hasBlack = true
		case h == "build-system" || h == "project" || strings.HasPrefix(h, "project."):
			hasBuild = true
		}
	}
	cmds := map[Role]Command{
		RoleTest: {Cmd: "pytest"},
	}
	if hasRuff {
		cmds[RoleFormat] = Command{Cmd: "ruff format {file}", Extends: pythonExtends}
		cmds[RoleLint] = Command{Cmd: "ruff check {file}", Extends: pythonExtends}
	} else if hasBlack {
		cmds[RoleFormat] = Command{Cmd: "black {file}", Extends: pythonExtends}
	}
	if hasBuild {
		cmds[RoleBuild] = Command{Cmd: "python -m build"}
	}
	return cmds
}

// cargoCommands is the Rust convention: Cargo.toml is present, so the
// toolchain is cargo. cargo fmt/clippy run per crate, not per file.
func cargoCommands() map[Role]Command {
	return map[Role]Command{
		RoleFormat: {Cmd: "cargo fmt"},
		RoleLint:   {Cmd: "cargo clippy --all-targets"},
		RoleTest:   {Cmd: "cargo test"},
		RoleBuild:  {Cmd: "cargo build"},
	}
}

// makeTargetRe matches a Makefile target line: one or more target names
// followed by a colon and the rest of the line (prerequisites or blank).
// Callers reject a rest that begins with "=" — that is a variable
// assignment ("CC := cc"), not a target.
var makeTargetRe = regexp.MustCompile(`^([A-Za-z0-9_.\-]+(?:[ \t]+[A-Za-z0-9_.\-]+)*)\s*:(.*)$`)

// makefileCommands finds make targets named exactly format/lint/test/
// build — a project's own convention, invoked as "make <role>". Recipe
// lines (tab-indented) and comments are skipped; the first line that
// declares a role wins. Make targets carry NO extends: a recipe can
// touch any file type, so such commands apply to all files.
func makefileCommands(data []byte) map[Role]Command {
	roleByName := map[string]Role{
		"format": RoleFormat,
		"lint":   RoleLint,
		"test":   RoleTest,
		"build":  RoleBuild,
	}
	cmds := map[Role]Command{}
	for _, line := range strings.Split(string(data), "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(line, "\t") || trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		m := makeTargetRe.FindStringSubmatch(trimmed)
		if m == nil || strings.HasPrefix(strings.TrimLeft(m[2], " \t"), "=") {
			continue // variable assignment, not a target
		}
		for _, name := range strings.Fields(m[1]) {
			role, ok := roleByName[name]
			if !ok {
				continue
			}
			if _, have := cmds[role]; have {
				continue
			}
			cmds[role] = Command{Cmd: "make " + name}
		}
	}
	return cmds
}
