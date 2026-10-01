// Project-command allowlist for issue #129.
//
// A project's format/lint command can come from a manifest (package.json
// script) or a declaration (AGENTS.md / config) that the operator did not
// hand-verify. Discovered test/build commands are NOT auto-approved here —
// they are gated by Classify's normal tiers. This allowlist is the narrow
// escape hatch that lets a *recognized, single-invocation* format or lint
// tool run without a model call, mirroring matchSafePath, while refusing
// anything that could chain or substitute.
//
// Workspace trust (issue #129): the allowlist splits into two tiers —
// TierInert (does not execute repository code: gofmt, rustfmt, ruff, black,
// plain go vet/fmt, cargo fmt) and TierCode (executes repository code by
// design: cargo clippy compiles the crate and runs build.rs and proc
// macros; eslint/prettier load a JS config from the repo; npm scripts run
// arbitrary node code). TierInert may auto-run on an untrusted workspace;
// TierCode runs only on a trusted one — the post-edit hook enforces that
// split, so opening an untrusted Rust or JS repo no longer executes that
// repo's code after every edit. The tier is DATA on the verdict
// (Verdict.ProjectCommand), never implied by a name list.
package shellrisk

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// filePlaceholder is the per-file marker a project command may contain
// (e.g. "gofmt -w {file}") — the same contract as
// internal/projectcmd.FilePlaceholder. It is a projectcmd template token,
// NOT shell syntax: the caller substitutes the target path before running,
// so the literal "{file}" token is stripped before the shell-control scan
// below. Anything else with braces (a real "{...}" group, a glob) stays and
// is caught by hasShellControl.
const filePlaceholder = "{file}"

// dirPlaceholder is the per-PACKAGE marker (e.g. "go vet {dir}") — the same
// contract as internal/projectcmd.DirPlaceholder. The caller substitutes it
// with the touched file's {"./"}-prefixed package directory and runs the
// resulting argv directly (no shell); the substituted values are re-scanned
// (SubstitutedArgs, below) — a template token is safe only because what
// replaces it is.
const dirPlaceholder = "{dir}"

// projectCommandTools are the single-binary format/lint tools that the
// recognized manifests (go.mod, package.json, pyproject.toml, Cargo.toml)
// emit, keyed by the binary's last path element. These are local,
// in-tree code tools: they read and (for formatters) rewrite source
// files, but do not reach the network, install anything, or spawn a
// shell. The set is deliberately small and matches what
// internal/projectcmd.Discover produces plus the obvious hand-declared
// variants. Each entry records its TIER explicitly (runsCode: false =
// TierInert, true = TierCode): an inert tool runs untrusted, a
// code-executing one needs trust.
//
// Multi-subcommand tools (go, cargo, npx, npm) are handled by their own
// switch arms below — their safety depends on the subcommand, not the
// binary — so they are NOT in this map.
//
//   - gofmt           (go.mod format)            inert: reads and rewrites one source file
//   - prettier, eslint (package.json format/lint) CODE: load JS config from the repo
//   - ruff, black     (pyproject.toml format/lint) inert: read/rewrite Python sources
//   - rustfmt         (Cargo.toml format)        inert: rewrite one Rust file; no build.rs, no proc macros
var projectCommandTools = map[string]struct {
	runsCode bool
}{
	"gofmt":    {runsCode: false},
	"prettier": {runsCode: true}, "eslint": {runsCode: true},
	"ruff": {runsCode: false}, "black": {runsCode: false},
	"rustfmt": {runsCode: false},
}

// projectCommandGoSub are the `go` subcommands that are format/lint (not
// build/test/run — those are out of scope for this allowlist). Plain `go
// vet`/`go fmt` are INERT: with no tool-loading flag (goCommandUnsafe
// refuses them), go vet compiles the package to its export form — Go
// source, never a repo-controlled binary — and go fmt rewrites sources.
// Each entry records whether the subcommand executes repository code.
var projectCommandGoSub = map[string]struct {
	runsCode bool
}{
	"vet": {runsCode: false}, "fmt": {runsCode: false},
}

// projectCommandGoFlags are `go` flags that change WHICH code or tooling
// runs, not what it runs: they are refused for a go format/lint command
// because each can load arbitrary in-repo code. -vettool/-toolexec exec a
// vet tool binary or wrap each tool in an arbitrary command, -exec wraps
// the go run binary in an arbitrary command, -overlay remaps the source
// files (gofmt -overlay can load a fabricated tree), -modfile swaps the
// module definition, and -C changes directory (and is a firstSubcommand
// skip, so it could steer the subcommand past any check).
var projectCommandGoFlags = map[string]string{
	"-vettool":  "a go -vettool binary",
	"-toolexec": "a go -toolexec command",
	"-exec":     "a go -exec command",
	"-overlay":  "a go -overlay file",
	"-modfile":  "a go -modfile alternative module",
	"-C":        "a go -C directory change",
}

// goCommandGoFlag canonicalizes one arg to the single-dash key of a
// tool-loading flag: cmd/go's flag parser (cmdflag.ParseOne) accepts a
// double-dash prefix too, so `--toolexec=x` is the same flag as
// `-toolexec=x` — strip the leading dash so the double-dash spelling is
// compared against the single-dash keys of projectCommandGoFlags.
func goCommandGoFlag(a string) (string, bool) {
	if a == "" {
		return "", false
	}
	if strings.HasPrefix(a, "--") {
		a = a[1:]
	}
	if !strings.HasPrefix(a, "-") {
		return "", false
	}
	if i := strings.IndexByte(a, '='); i > 0 {
		a = a[:i]
	}
	if projectCommandGoFlags[a] != "" {
		return a, true
	}
	return "", false
}

// projectCommandCargoSub are the `cargo` subcommands that are format/lint.
// `cargo test`/`build` are NOT included: they are test/build roles, gated by
// Classify's normal tiers, not this allowlist. Tiers: `cargo fmt` is INERT —
// rustfmt rewrites Rust sources and never compiles the crate (no build.rs,
// no proc macros); `cargo clippy` is CODE — it compiles the crate to run
// the lint pass, which executes the crate's build.rs and procedural
// macros (round 7: the whole class of repo-code execution, not just an
// instance).
var projectCommandCargoSub = map[string]struct {
	runsCode bool
}{
	"fmt":    {runsCode: false},
	"clippy": {runsCode: true},
}

// projectCommandNPXTools are the tool names that `npx <tool>` is allowed to
// invoke. A bare `npx` with no argument, or `npx` with an unrecognized
// tool, is refused — npx can download and run arbitrary packages, so the
// tool name must be pinned. Both pinned tools are CODE tier: eslint loads
// the repo's eslint.config.js (a JavaScript config) and prettier loads the
// repo's .prettierrc.js et al — the hook's --no pin keeps them to the
// locally installed binary, but the repo's CONFIG is still executed, so
// they run only on a trusted workspace.
var projectCommandNPXTools = map[string]struct {
	runsCode bool
}{
	"prettier": {runsCode: true}, "eslint": {runsCode: true},
}

// The npx no-install pin spellings that may appear BEFORE the tool name.
// npx is "install and run" by default, so even a pinned tool is fetched from
// the registry unless the install is disabled. --no is the supported spelling
// on npm ≥ 7 (--no-install is a deprecated alias, and --yes=false is the
// explicit long form); AppendNPXNoInstall inserts one of these right after
// `npx`, before the tool name, so an allowlisted npx command can only run a
// tool already present in node_modules. Anything else before the tool name
// is refused (npxCommandUnsafe): npm's flag parser (nopt) expands
// unambiguous abbreviations, so a denylist of flags is too narrow to catch
// every flag that could install or call.

// AllowlistProjectCommand decides whether a project's format or lint
// command may run without a model call. It returns Safe for a recognized,
// single-invocation tool — a bare binary name (resolved via PATH, never a
// path-qualified or relative path), a recognized subcommand, and no
// tool-loading flag — free of shell-control characters (pipe, redirect,
// chaining, command/var substitution, subshell — see hasShellControl) —
// and Risky for everything else.
//
// Scope: this checks the COMMAND, not the role. The caller must only feed
// it format/lint commands (a RoleFormat/RoleLint Command from
// internal/projectcmd); a `make test` script, for example, is refused here
// but would be handled by Classify's normal tiers, not this allowlist.
//
// Workspace trust: a Safe verdict carries the command's tier in
// Verdict.ProjectCommand (projectCommandTier, exported as the tier
// constants): TierInert does not execute repository code and may auto-run
// on an untrusted workspace; TierCode executes repository code by design
// (compiles the crate, loads a JS config, runs a script) and runs only on
// a TRUSTED workspace — the hook (internal/tools) enforces that split.
// Risky verdicts carry NoTier.
//
// This is the project-command analog of matchSafePath and shares its
// conservatism: anything that is more than one inert invocation is
// declined. A declared or malicious command has no free execution through
// this path: a format command that names a path (./tools/gofmt, or a gofmt
// binary that a cloned repo ships under tools/) is refused because the
// hook would otherwise exec whatever the repo put there with no prompt;
// a go vet -vettool=... or -toolexec=... would exec an arbitrary binary;
// an npx -p/--package=... would install and run an arbitrary package —
// all Risky. An allowlisted npx command is additionally pinned to a
// recognized tool name and forced to run without installing (see
// AppendNPXNoInstall), so it executes only a tool already in node_modules.
//
// It returns Risky (not Blocked) for non-matches: the caller's policy then
// decides (prompt, or gate via Classify). Blocked is reserved for the
// deny-floor's catastrophic forms, which Classify consults first.
func AllowlistProjectCommand(cmd string) Verdict {
	cmd = strings.TrimSpace(cmd)
	if cmd == "" {
		return Verdict{Level: Risky, Reason: "empty project command", Tier: "fail-closed"}
	}
	// Strip the per-file and per-package placeholders (projectcmd template
	// tokens the caller substitutes before running) so their braces are not
	// mistaken for shell syntax. A real group/subshell still carries other
	// control characters and is caught below.
	checked := strings.ReplaceAll(cmd, filePlaceholder, "")
	checked = strings.ReplaceAll(checked, dirPlaceholder, "")
	// More than a single inert invocation — chains, redirects, pipes, or
	// command/var substitution. Refuse outright.
	if hasShellControl(checked) {
		return Verdict{Level: Risky, Reason: "project command contains shell-control characters", Tier: "allowlist"}
	}
	cmd = checked
	fields := strings.Fields(cmd)
	if len(fields) == 0 {
		return Verdict{Level: Risky, Reason: "empty project command", Tier: "fail-closed"}
	}
	// A leading VAR=val is an environment-mutated invocation, not a simple
	// tool call.
	if strings.Contains(fields[0], "=") {
		return Verdict{Level: Risky, Reason: "project command sets an environment variable", Tier: "allowlist"}
	}
	// The binary must be a BARE name that exec resolves via PATH. A
	// path-qualified binary (./tools/gofmt, /usr/bin/gofmt) would exec
	// whatever the working directory (typically the repo being edited) put
	// there — a cloned repo shipping an executable named like a format tool
	// would get it run with no prompt after every edit, defeating the
	// allowlist. PATH resolution of a bare name reaches the operator's real
	// toolchain instead.
	if strings.ContainsAny(fields[0], "/\\") {
		return Verdict{Level: Risky, Reason: "project command names a path-qualified binary", Tier: "allowlist"}
	}
	if reason := workspaceBinaryRefusal(fields[0]); reason != "" {
		return Verdict{Level: Risky, Reason: reason, Tier: "allowlist"}
	}
	switch bin := fields[0]; bin {
	case "go":
		if reason, bad := goCommandUnsafe(fields[1:]); bad {
			return Verdict{Level: Risky, Reason: reason, Tier: "allowlist"}
		}
		if sub, ok := firstSubcommand(fields[1:]); ok {
			if entry, known := projectCommandGoSub[sub]; known {
				return tierVerdict("recognized go format/lint command", entry.runsCode)
			}
		}
	case "cargo":
		if sub, ok := firstSubcommand(fields[1:]); ok {
			if entry, known := projectCommandCargoSub[sub]; known {
				return tierVerdict("recognized cargo format/lint command", entry.runsCode)
			}
		}
	case "npx":
		// `npx <tool> ...` — the tool name is the first non-flag token, and
		// the invocation must not install or call anything else.
		if tool, ok := firstSubcommand(fields[1:]); ok {
			if entry, known := projectCommandNPXTools[tool]; known {
				if reason, bad := npxCommandUnsafe(fields[1:]); bad {
					return Verdict{Level: Risky, Reason: reason, Tier: "allowlist"}
				}
				return tierVerdict("recognized npx "+tool+" command (local install only)", entry.runsCode)
			}
		}
	case "npm":
		// `npm run <script>` (or `npm test`) runs a package.json SCRIPT:
		// arbitrary node code, the pre/post hooks, whatever the repo wrote.
		// It executes repository code by definition — the CODE tier — never
		// inert. (npm's own flag parser is unbounded, so the subcommand must
		// be exactly run/test with no other npm-level flag; everything after
		// the script name belongs to the script and is the repo's.)
		if sub, ok := firstSubcommand(fields[1:]); ok {
			if sub == "run" || sub == "test" {
				if reason, bad := npmCommandUnsafe(fields[1:]); bad {
					return Verdict{Level: Risky, Reason: reason, Tier: "allowlist"}
				}
				return tierVerdict("recognized npm script command", true)
			}
		}
	default:
		if entry, known := projectCommandTools[bin]; known {
			return tierVerdict("recognized format/lint tool", entry.runsCode)
		}
	}
	return Verdict{Level: Risky, Reason: "project command is not a recognized format/lint tool", Tier: "allowlist"}
}

// workspaceBinaryRefusal is the PATH half of the "no repo-shipped tool"
// contract for a BARE binary name (a path-qualified name is already refused
// above). It resolves the name the way exec would (LookPath: the CWD for a
// name containing a dot, otherwise $PATH) and refuses when the resolved
// binary lives INSIDE the workspace — a repo can make that happen with a
// PATH entry pointing at its own bin dir or node_modules/.bin, whether the
// entry is "." (which exec itself rejects — exec.ErrDot, an absolute
// in-workspace PATH entry is NOT rejected by exec and is exactly the case
// this catches), a relative dir, or an absolute dir under the workspace.
// Trust is irrelevant here: a trusted workspace said "run what this repo
// configures" about the repo's SCRIPTS and CONFIGS, not "exec a binary the
// repo happens to put first on PATH" — the operator's real toolchain is the
// point of the allowlist. Symlinks are followed (EvalSymlinks), so a
// <workspace>/bin link to /usr/bin/gofmt is still a file the repo placed.
// Returns "" when the name resolves outside the workspace or resolution is
// inconclusive (the name is unresolvable — the exec will simply fail,
// noted, and the edit stands).
func pathEnvInWorkspaceRefusal(name string) string {
	// Scan PATH entries for a repo-controlled resolution target — the
	// shapes the LookPath result does NOT cover: an ABSOLUTE entry under the
	// workspace (LookPath succeeds, possibly via a later real entry — exec
	// would then run that later binary, but the repo still controls the
	// shadowing), and RELATIVE entries (a dir name) — exec rejects these at
	// spawn time with ErrDot, so the bare-name command could never run in
	// such an environment; refusing keeps a shadowed-by-CWD binary honest.
	// The "." entry is handled separately: exec resolves it to the CWD
	// (the workspace) but LookPath skips it (an earlier real PATH entry
	// wins, or the name is not in the CWD at all), so the in-workspace
	// resolution it implies is invisible to the scan below.
	wd, werr := os.Getwd()
	if werr != nil {
		return ""
	}
	wdReal, rerr := filepath.EvalSymlinks(wd)
	if rerr != nil {
		wdReal = wd
	}
	for _, entry := range filepath.SplitList(os.Getenv("PATH")) {
		if entry == "" {
			continue
		}
		var dir string
		if entry == "." {
			dir = wd // exec's resolution of a "." entry: the current directory
		} else if filepath.IsAbs(entry) {
			dir = entry
		} else {
			dir = filepath.Join(wd, entry)
		}
		if real, eerr := filepath.EvalSymlinks(dir); eerr == nil {
			dir = real
		}
		if dir == wdReal || strings.HasPrefix(dir, wdReal+string(filepath.Separator)) {
			return fmt.Sprintf("the command binary %s can resolve from PATH entry %q inside the workspace, where the repository can place tools", name, entry)
		}
	}
	return ""
}
func workspaceBinaryRefusal(name string) string {
	if reason := pathEnvInWorkspaceRefusal(name); reason != "" {
		return reason
	}
	res, err := exec.LookPath(name)
	if err != nil {
		return "" // nothing to check: the binary is absent (exec will note it)
	}
	if real, eerr := filepath.EvalSymlinks(res); eerr == nil {
		res = real
	}
	wd, werr := os.Getwd()
	if werr != nil {
		return "" // no workspace root to test against
	}
	wdReal, rerr := filepath.EvalSymlinks(wd)
	if rerr != nil {
		wdReal = wd
	}
	if res == wdReal || strings.HasPrefix(res, wdReal+string(filepath.Separator)) {
		return fmt.Sprintf("the command binary %s resolves inside the workspace (%s), where the repository can place tools", name, wd)
	}
	return ""
}

// projectCommandTier maps a tool's runsCode flag to the exported tier
// constant recorded on the verdict (Verdict.ProjectCommand).
func projectCommandTier(runsCode bool) ProjectCommandTier {
	if runsCode {
		return TierCode
	}
	return TierInert
}

// tierVerdict is the Safe verdict for an allowlisted command: the tier is
// DATA (runsCode true → TierCode, the workspace must be trusted), never
// implied by the tool's name.
func tierVerdict(reason string, runsCode bool) Verdict {
	return Verdict{Level: Safe, Reason: reason, Tier: "allowlist", ProjectCommand: projectCommandTier(runsCode)}
}

// npmCommandUnsafe scans an npm command's args (the fields after the "npm"
// binary) for an npm-level flag — one before the script name that is not
// part of the plain `npm run <script>` / `npm test` form. npm's flag set is
// wide (registry URLs, cache dirs, scripts that change what runs), so any
// npm-level flag is refused rather than allowlisted: a `--registry
// https://evil` or `--script-shell` would change WHAT npm runs, and that
// decision is not the allowlist's to make. Flags AFTER the script name
// belong to the script (the repo's code anyway) and are not checked here.
func npmCommandUnsafe(args []string) (string, bool) {
	seenSub := false
	for _, a := range args {
		if a == "" {
			continue
		}
		if !strings.HasPrefix(a, "-") {
			seenSub = true
			continue
		}
		if !seenSub {
			return "npm " + a + " is an npm-level flag on a project command", true
		}
	}
	return "", false
}

// goCommandUnsafe scans a go format/lint command's args (the fields after
// the "go" binary) for a tool-loading flag — one that changes WHICH code
// or tooling runs rather than what it runs. It returns a refusal reason
// and bad=true for the first such flag. It does NOT skip flags:
// firstSubcommand skips them to find the subcommand, but a
// -vettool/-toolexec/-C flag is itself the unsafe construct, whatever
// comes after it. cmd/go accepts each flag in a single-dash and a
// double-dash spelling, in a bare and a =value form — goCommandGoFlag
// canonicalizes all four ("-x", "-x=v", "--x", "--x=v").
func goCommandUnsafe(args []string) (string, bool) {
	for _, a := range args {
		if flag, ok := goCommandGoFlag(a); ok {
			return "go " + flag + " can load " + projectCommandGoFlags[flag], true
		}
	}
	return "", false
}

// npxCommandUnsafe scans an npx command's args (the fields after the
// "npx" binary) for any flag BEFORE the tool name (the first positional)
// that is not one of the no-install pin spellings. npx stops parsing its own
// options at the first positional and forwards everything after it to the
// tool, so flags after the tool name are the tool's and are not checked here.
//
// The check is an allowlist, not a denylist: npm's flag parser (nopt) expands
// unambiguous abbreviations, so any flag not in the pin set could be an
// abbreviation for an option that changes what npx runs (install a package,
// call an expression, etc.). A flag before the tool name that is not a
// recognized pin is refused.
func npxCommandUnsafe(args []string) (string, bool) {
	for _, a := range args {
		if a == "" {
			continue
		}
		// The first positional starts the tool's argv — everything after it
		// belongs to the tool, not to npx.
		if !strings.HasPrefix(a, "-") {
			break
		}
		if a != "--no" && a != "--no-install" && a != "--yes=false" {
			return "npx " + a + " is not a recognized no-install pin", true
		}
	}
	return "", false
}

// AppendNPXNoInstall returns an allowlisted npx command with the
// no-install pin: npx is "install and run" by default, so even a pinned
// tool name is fetched from the registry unless the install is disabled.
// The pin forces npx to run the tool from the project's node_modules
// (refusing otherwise), so an allowlisted npx command executes only a
// locally installed tool.
//
// The pin is --no — the supported spelling on npm ≥ 7 (`--no-install` is a
// deprecated alias, and a trailing flag like `npx prettier --no-install`
// would be passed THROUGH to prettier as their own flag, not read by npx)
// — and it is inserted right after `npx`, before the tool name: npx stops
// parsing its own options at the first positional and forwards everything
// after it to the tool.
//
// It is a pure template transform: the returned string re-parses to the
// same argv plus one inert flag at index 1, and it is idempotent for
// templates that already pin it anywhere BEFORE the first positional (a
// trailing pin is not one — npx never sees it there). It is exported so
// the post-edit hook (the argv builder) pins the same flag before it execs
// — see internal/tools.splitAndCheckArgs.
func AppendNPXNoInstall(cmd string) string {
	fields := strings.Fields(cmd)
	if len(fields) == 0 || fields[0] != "npx" {
		return cmd
	}
	for _, f := range fields[1:] {
		if f == "" {
			continue
		}
		// The first positional (the tool name) starts the tool's argv: any
		// pin after it belongs to the tool, not to npx, so it does not count.
		if !strings.HasPrefix(f, "-") {
			break
		}
		if f == "--no" || f == "--no-install" || f == "--yes=false" {
			return cmd
		}
	}
	return fields[0] + " --no " + strings.Join(fields[1:], " ")
}

// SubstitutedArgs scans the ARGUMENTS a caller substitutes into an
// allowlisted project command — the {file} target and the {dir} package
// directory. The allowlist checks the command TEMPLATE, not what is spliced
// into it: a path is model-controlled input (the write_file path argument),
// and an argument carrying a shell-control character is exactly how a path
// like "x$(cmd).go" would inject code if the command were still run through
// a shell. It returns Risky when any argument carries one, Safe when all
// are inert.
//
// It is defense-in-depth: the hook runs allowlisted commands directly with
// exec as an argv (no shell — the {file}/{dir} substitutions land in the
// argv as single opaque arguments that no shell ever re-parses), so a
// malicious path is already inert by construction. SubstitutedArgs is the
// belt to that suspenders: it pins the argument contract the allowlist
// assumes and is the same gate a caller would need if it ever re-routed
// these commands through a shell again.
func SubstitutedArgs(args ...string) Verdict {
	for _, a := range args {
		if a == "" {
			continue
		}
		if hasShellControl(a) {
			return Verdict{Level: Risky, Reason: "project command argument contains shell-control characters", Tier: "allowlist"}
		}
	}
	return Verdict{Level: Safe, Reason: "project command arguments are inert", Tier: "allowlist"}
}
