// project_command_hook.go is the post-edit hook for issue #129.
//
// After write_file / edit_file lands, the session runs the project's OWN
// format command (and, if declared, its per-file or per-package lint) on
// the file that was just touched, so an unformatted or broken file never
// reaches review. The hook is a best-effort observation: it appends what
// it ran and what the tool reported to the tool result — including lint
// failures and timeouts — but it NEVER fails the edit. A broken formatter,
// a missing toolchain, or a command that takes too long all degrade to a
// note, never to a refused edit.
//
// Injection safety: only allowlisted commands run
// (shellrisk.AllowlistProjectCommand), and they run as a plain argv — the
// allowlisted template is split with strings.Fields and the {file}/{dir}
// tokens are each replaced by a SINGLE argv element
// (splitProjectCommand), which runHookDirect launches with
// exec.CommandContext directly: no shell anywhere in the pipeline. A path
// is model-controlled input, so this is what keeps a file named
// "x$(touch marker).go" a literal filename — the substituted value can
// never be re-split, re-globbed, or re-interpreted as shell syntax.
// shellrisk.SubstitutedArgs re-scans the substituted values before the
// exec as the belt to that suspenders and pins the argument contract: a
// shell-shaped "path" is declined (noted for format, silently skipped for
// lint) rather than handed to the tool.
//
// Workspace trust (issue #129): the allowlist is two TIERS. TierInert
// commands (gofmt, rustfmt, ruff, black, plain go vet/fmt, cargo fmt) do
// not execute repository code and run on any workspace. TierCode commands
// (cargo clippy — compiles the crate, runs build.rs and proc macros;
// eslint/prettier — load a JS config; npm/npx scripts — run arbitrary node
// code) run ONLY when the session's workspace is trusted
// (WorkspaceTrust; absent capability = untrusted, the safe default). On
// an untrusted workspace a TierCode command is skipped with a note —
// "skipped: <cmd> runs repository code; trust this workspace to enable" —
// so opening an untrusted Rust or JS repo never executes that repo's code
// after an edit.
package tools

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/dereksantos/cortex/internal/projectcmd"
	"github.com/dereksantos/cortex/internal/shellrisk"
)

// ProjectCommands is the OPTIONAL ToolDeps capability that supplies a
// session's resolved project command set (discovered + declared, per
// internal/projectcmd.Resolve). It is asserted dynamically by
// projectCommandsOf, so every existing ToolDeps implementor is untouched; a
// session that doesn't implement it (or returns no usable commands) has no
// post-edit hook.
type ProjectCommands interface {
	ProjectCommands() projectcmd.Commands
}

// WorkspaceTrust is the OPTIONAL ToolDeps capability that reports whether
// THIS session's workspace is trusted (issue #129's trust gate). It is
// asserted dynamically by workspaceTrusted: a session that doesn't
// implement it is UNTRUSTED — the safe default for every existing
// implementor, so the gate is fail-closed by construction. Trust is a
// persisted, per-workspace, USER-level decision (the ~/.cortex user
// config's project.trusted list, set by `cortex project trust`); it can
// never come from the workspace itself — not from the project's
// .cortex/config.json, not from AGENTS.md — because the repository is the
// untrusted party.
type WorkspaceTrust interface {
	WorkspaceTrusted() bool
}

// projectCommandsOf extracts the session's resolved commands, or the zero
// value when the capability is absent (the common REPL/CWD case today).
func projectCommandsOf(deps ToolDeps) projectcmd.Commands {
	pc, ok := deps.(ProjectCommands)
	if !ok {
		return projectcmd.Commands{}
	}
	return pc.ProjectCommands()
}

// workspaceTrusted reports whether the session's workspace is trusted.
// Absent capability → untrusted (false): a code-executing project command
// (TierCode) is skipped on a workspace that never said it was trusted, so
// the default is the safe one.
func workspaceTrusted(deps ToolDeps) bool {
	wt, ok := deps.(WorkspaceTrust)
	return ok && wt.WorkspaceTrusted()
}

// formatBudget caps how long each hook command (format, then lint) may run.
// A formatter that hangs must not stall the turn; on expiry the hook notes
// the timeout and moves on — the edit already succeeded.
var formatBudget = 10 * time.Second

// errHookTimeout is the single sentinel the hook's exec path returns on a
// budget expiry. Callers report it by identity (errors.Is), never by
// string-matching, so a reworded message can't break the timeout note.
var errHookTimeout = errors.New("project command timed out")

// hookRunner runs an allowlisted project command's argv directly (no shell)
// under the format budget, returning its combined output plus any error
// (nil on success, errHookTimeout on deadline, or the exec error
// otherwise). dir is the command's working directory — the project root
// for an anchored session ("" = the process CWD, the CWD-implicit case) —
// which is what makes a "./"-prefixed {dir} substitution resolvable from
// where the manifest's commands are meant to run. It is a var — not an
// inline exec.Command call — so a test can install a command that actually
// HANGS and drive the real timeout path; no allowlisted formatter (gofmt,
// go vet) ever hangs in practice, so the budget can't be reached
// end-to-end without this seam. The default is runHookDirect.
var hookRunner = runHookDirect

// runHookDirect is the production hookRunner: exec argv[0] argv[1:]
// directly — no shell (the injection-safety contract, package comment) —
// under the format budget, returning combined output plus the run error.
func runHookDirect(ctx context.Context, argv []string, dir string) (string, error) {
	cctx, cancel := context.WithTimeout(ctx, formatBudget)
	defer cancel()
	if ref := hookBinaryRefusal(argv, dir); ref != "" {
		return "", fmt.Errorf("project command refused: %s", ref)
	}
	cmd := exec.CommandContext(cctx, argv[0], argv[1:]...)
	cmd.Dir = dir
	raw, runErr := cmd.CombinedOutput()
	if cctx.Err() == context.DeadlineExceeded {
		return strings.TrimSpace(string(raw)), errHookTimeout
	}
	return strings.TrimSpace(string(raw)), runErr
}

// hookBinaryRefusal refuses to exec argv[0] when PATH resolution would land
// inside the WORKSPACE itself — returning the refusal note ("" when the
// binary is fine). This is the argv half of the "no repo-shipped tool"
// contract: the allowlist (shellrisk.AllowlistProjectCommand) already
// refuses PATH-QUALIFIED names (./tools/gofmt, /repo/bin/eslint) AND a bare
// name that would resolve from a repo-controlled PATH entry (the workspace
// bin dir or node_modules/.bin on PATH — absolute or relative; see
// shellrisk's workspaceBinaryRefusal). What passes both is an absolute
// argv[0] whose target is INSIDE the workspace: the allowlist's
// path-qualified refusal is name-based (it catches the literal
// "./tools/…" and "/repo/…" forms, and bare names), but a command template
// built at runtime from an os.Getwd()-derived absolute dir would name the
// workspace's file with a name that no static prefix check sees — a repo
// shipping an executable named like an allowlisted tool would get it
// exec'd with no prompt after every edit. Resolving argv[0] the way exec
// would (LookPath, dir-relative when relative, otherwise $PATH) and
// refusing a hit inside the workspace — symlinks followed, so a
// <workspace>/bin link to /usr/bin/gofmt is still the workspace's file —
// closes that residual. Only argv[0] is checked: allowlisted argument
// positions are paths to FORMAT or files to VET, never programs to exec.
func hookBinaryRefusal(argv []string, dir string) string {
	if len(argv) == 0 {
		return "empty command"
	}
	root := dir
	if root == "" {
		wd, err := os.Getwd()
		if err != nil {
			return "" // no workspace root to test against: nothing to refuse
		}
		root = wd
	}
	if res, err := exec.LookPath(argv[0]); err == nil {
		if real, eerr := filepath.EvalSymlinks(res); eerr == nil {
			res = real
		}
		rootReal, rerr := filepath.EvalSymlinks(root)
		if rerr != nil {
			rootReal = root
		}
		if res == rootReal || strings.HasPrefix(res, rootReal+string(filepath.Separator)) {
			return fmt.Sprintf("%s resolves inside the workspace (%s) — repo-shipped tools do not run in the post-edit hook", argv[0], root)
		}
	}
	return ""
}

// HookWouldRun reports whether the post-edit hook would auto-run cmd in
// role with the given trust state — the hook's own applicability rules
// (roleApplicable: format needs {file}, lint needs {file}/{dir}, test and
// build are never hook-run) ANDed with the allowlist verdict (tier +
// trust gate — the SAME shellrisk.AllowlistProjectCommand call the hook's
// hookGate runs). The report `cortex project commands` renders uses this
// so runs_now can never drift from what the hook actually runs: a
// whole-project format command is never per-edit work, a lint without
// {file}/{dir} is never per-edit work, and test/build NEVER run — no
// matter how allowlisted or how trusted. (The hook's argument contract —
// SubstitutedArgs on the touched path — is per-file, not per-template, so
// it is not modeled here; the PATH/workspace-binary refusal the hook also
// applies is environment-dependent and the report shows the command's
// tier/trust verdict, which is the stable, workspace-meaningful half.)
func HookWouldRun(role projectcmd.Role, cmd projectcmd.Command, trusted bool) bool {
	if !roleApplicable(role, cmd) {
		return false
	}
	v := shellrisk.AllowlistProjectCommand(cmd.Cmd)
	if v.Level != shellrisk.Safe {
		return false
	}
	// The trust gate: an inert command runs on any workspace, a
	// code-executing one only on a trusted one (the hook's hookGate does
	// the same after the argument check). The argument contract is
	// satisfied by a probe plain path for every real template, so it is
	// not re-checked here — a template failing it on a plain path would do
	// so on every file, and "never runs" is the honest answer either way.
	return v.ProjectCommand == shellrisk.TierInert || trusted
}

// roleApplicable is the role half of the hook's applicability, mirroring
// the hook's own inline checks: the format role runs only a PER-FILE
// command (a whole-project format has no argument to substitute for the
// one file just touched), and the lint role only a command carrying the
// {file} or {dir} token (a whole-project lint is not the hook's job —
// exactly the hook's `lintApplies`-style inline test, without the
// Extends gate the hook applies per touched file). The test and build
// roles are never hook-run at all.
func roleApplicable(role projectcmd.Role, cmd projectcmd.Command) bool {
	switch role {
	case projectcmd.RoleFormat:
		return cmd.PerFile
	case projectcmd.RoleLint:
		return cmd.PerFile || strings.Contains(cmd.Cmd, projectcmd.DirPlaceholder)
	default:
		return false
	}
}

// hookGate decides whether an allowlisted command may RUN in this hook:
// the allowlist (a recognized single-invocation tool) plus the trust gate.
// It returns (argv, ok, note) — ok=false means the command must not run,
// and note (possibly "") is the exact refusal text to fold into the tool
// result. The trust gate (issue #129): a TierCode command — one that
// executes repository code by design (cargo clippy compiles the crate and
// runs build.rs and proc macros; eslint/prettier load a JS config; npm
// scripts run arbitrary node code) — runs ONLY on a trusted workspace; on
// an untrusted one the hook skips it and says so. TierInert (gofmt,
// rustfmt, ruff, black, plain go vet/fmt) runs on any workspace: it reads
// and rewrites source files but never executes repo code. The npx
// --no-install pin is applied here, on the template, before the argv is
// built — the allowlist sees the plain template the declaration carries.
func hookGate(template, root, fsPath string, trusted bool) (argv []string, ok bool, note string) {
	v := shellrisk.AllowlistProjectCommand(template)
	if v.Level != shellrisk.Safe {
		return nil, false, v.Reason
	}
	if v.ProjectCommand == shellrisk.TierCode && !trusted {
		return nil, false, "runs repository code; trust this workspace to enable"
	}
	if argv, ok = splitAndCheckArgs(template, root, fsPath); !ok {
		return nil, false, "the written path was rejected by the argument check (not a plain file path)"
	}
	return argv, true, ""
}

// runProjectCommandHook runs the post-edit format/lint hook for the file at
// fsPath (the workdir-resolved path) and returns the note to append to the
// tool result. root is the project root the commands are meant to run from
// (the session's workdir, "" for a CWD-implicit session). It returns "" when
// there is nothing to report — no command set, no command that applies to
// the file's extension, an already-clean file with no applicable lint — so
// the tool result is byte-identical to the pre-hook behavior in the common
// case.
//
// It never returns an error: the hook observes, it doesn't veto. Every
// failure (allowlist refusal, tier refusal, argument refusal, spawn error,
// timeout, lint failure) is folded into the note string, because the
// contract is that the hook never blocks the edit.
func runProjectCommandHook(ctx context.Context, cmds projectcmd.Commands, root, fsPath string, trusted bool) string {
	var b strings.Builder

	// --- Format -----------------------------------------------------------
	// Only a per-file format command (carrying {file}) is safe to run
	// against the one file just touched; a whole-project format command has
	// no argument to substitute and is left to the model/bash. The
	// command's declared extension set gates which files it applies to at
	// all (appliesTo) — gofmt on a README.md in a Go project is not a
	// formatting run, it is a spurious error note on every non-source edit.
	if cmds.Format.Cmd != "" && cmds.Format.PerFile && appliesTo(cmds.Format, fsPath) {
		argv, ok, refusal := hookGate(cmds.Format.Cmd, root, fsPath, trusted)
		switch {
		case ok:
			changed, out, err := runAndWriteBack(ctx, argv, root, fsPath)
			switch {
			case errors.Is(err, errHookTimeout):
				b.WriteString("note: project format command timed out after " + formatBudget.String() + "; it was NOT run to completion")
			case err != nil:
				// A run error (spawn failure, gofmt on an unparseable file,
				// etc.). The tool's output, if any, rides along as an
				// observation — the file is left as written.
				if s := strings.TrimSpace(out); s != "" {
					fmt.Fprintf(&b, "note: project format command ran with an error (%v): %s", err, clipNote(s))
				} else {
					fmt.Fprintf(&b, "note: project format command could not run: %v", err)
				}
			case changed:
				b.WriteString("note: formatted " + fsPath + " with the project format command (" + cmds.Format.Cmd + ")")
			default:
				// Clean run, no change. Surface any tool output as an
				// observation; otherwise the file was already clean.
				if s := strings.TrimSpace(out); s != "" {
					b.WriteString("note: project format command reported: " + clipNote(s))
				}
			}
		case strings.HasPrefix(refusal, "runs repository code"):
			// Trust gate: the command is allowlisted but executes the
			// repository's own code (cargo clippy's build.rs, a JS config,
			// an npm script) and this workspace is untrusted. The file is
			// left exactly as written; the refusal names what would unlock
			// it — the operator's decision, never the repo's.
			b.WriteString("skipped: " + cmds.Format.Cmd + " " + refusal)
		case refusal == "the written path was rejected by the argument check (not a plain file path)":
			// The written path is shell-shaped (SubstitutedArgs): not a
			// plain file, so the hook declines to run and leaves the file
			// exactly as written.
			b.WriteString("note: project format command not run: " + refusal + "; the file was left as written")
		default:
			// Refused by the allowlist (step 3): the command is not a
			// recognized single-invocation formatter free of shell control,
			// so a declared/malicious format script does not get free
			// execution here. We do NOT fall through to the shell gate —
			// that would route an arbitrary script through the classifier
			// for a free run, exactly the gap the allowlist closes.
			b.WriteString("note: project format command not allowlisted for auto-run (" + refusal + "); it was NOT run")
		}
	}

	// --- Per-file / per-package lint (if declared) -------------------------
	// A lint carrying {file} reports problems for the file just touched; one
	// carrying {dir} type-checks the file's WHOLE PACKAGE (the correct unit
	// for cross-file tools — see projectcmd.DirPlaceholder). A whole-project
	// lint (neither token) is skipped — re-linting the world on every edit
	// is not what the hook is for. The command's extension set gates it the
	// same way it gates format (appliesTo). Output is folded into the note,
	// success or failure.
	if lintApplies(cmds.Lint, fsPath) {
		argv, ok, refusal := hookGate(cmds.Lint.Cmd, root, fsPath, trusted)
		switch {
		case ok:
			out, err := hookRunner(ctx, argv, root)
			switch {
			case errors.Is(err, errHookTimeout):
				b.WriteString("note: project lint command timed out after " + formatBudget.String())
			case err != nil && strings.TrimSpace(out) == "":
				b.WriteString("note: project lint command could not run: " + err.Error())
			case strings.TrimSpace(out) != "":
				b.WriteString("note: project lint for " + fsPath + ":\n" + clipNote(strings.TrimSpace(out)))
			}
			// A clean lint (exit 0, no output) is silent.
		case strings.HasPrefix(refusal, "runs repository code"):
			// Trust gate: the lint is allowlisted but executes the
			// repository's own code (e.g. cargo clippy's build.rs, a JS
			// eslint config) and this workspace is untrusted — skip it and
			// say so. A lint finding is advisory (folded into the result),
			// so a skipped lint costs nothing the operator can't recover by
			// trusting the workspace or running the command through bash.
			b.WriteString("skipped: " + cmds.Lint.Cmd + " " + refusal)
		}
		// A non-allowlisted lint command, a path rejected by the argument
		// check, or an untrusted-workspace inert skip is silent for the
		// lint role: the hook only runs recognized tools on plain paths of
		// workspaces that consent to repo-code execution, and a skipped
		// finding is nothing to surface.
	}

	return b.String()
}

// appliesTo reports whether cmd's declared source-extension set (Extends)
// covers path's extension. An empty set means "no recognized toolchain" — a
// declaration or manifest script discovery couldn't map to a source set
// (a make target, a package.json script, cargo) — and applies to every
// file; a non-empty set gates the hook to those source files, so gofmt on a
// README.md in a Go project is not a formatting run.
func appliesTo(cmd projectcmd.Command, path string) bool {
	if len(cmd.Extends) == 0 {
		return true
	}
	ext := strings.ToLower(filepath.Ext(path))
	for _, e := range cmd.Extends {
		if ext == e {
			return true
		}
	}
	return false
}

// lintApplies reports whether the lint role has a command the hook runs for
// path: non-empty, carrying the {file} or {dir} token (per-file or
// per-package — a whole-project lint is not the hook's job), and applicable
// to the file's extension. (roleApplicable — used by HookWouldRun for the
// report's runs_now — uses the same {file}/{dir} test WITHOUT the Extends
// gate: applicability to a SPECIFIC file is the hook's per-edit call, while
// the report asks whether the command is per-edit work AT ALL.)
func lintApplies(cmd projectcmd.Command, path string) bool {
	return cmd.Cmd != "" &&
		(cmd.PerFile || strings.Contains(cmd.Cmd, projectcmd.DirPlaceholder)) &&
		appliesTo(cmd, path)
}

// splitAndCheckArgs builds the argv for an allowlisted template and re-scans
// the SUBSTITUTED VALUES (the model-controlled file path and package dir)
// with shellrisk.SubstitutedArgs. ok=false — the command must not run —
// when any substituted value carries a shell-control character: such a
// "path" is model input shaped like a probe of the substitution contract.
// (Defense in depth: the argv exec would pass it as one inert argument
// anyway — but the check pins the argument contract the allowlist assumes,
// and keeps the refusal visible for the format role.) An allowlisted npx
// template also gets the --no no-install pin inserted right after `npx`,
// before the tool name (shellrisk.AppendNPXNoInstall) so the hook never
// turns a declared npx line into a silent registry fetch — a pin placed
// after the tool name would be forwarded to the tool as their own flag.
func splitAndCheckArgs(template, root, fsPath string) ([]string, bool) {
	if strings.HasPrefix(template, "npx ") || template == "npx" {
		template = shellrisk.AppendNPXNoInstall(template)
	}
	args := []string{fsPath}
	if strings.Contains(template, projectcmd.DirPlaceholder) {
		args = append(args, packageDirArg(root, fsPath))
	}
	if v := shellrisk.SubstitutedArgs(args...); v.Level != shellrisk.Safe {
		return nil, false
	}
	return splitProjectCommand(template, root, fsPath), true
}

// splitProjectCommand turns an allowlisted template (already vetted by
// allowlistProjectCommand) into the argv the hook execs directly:
// strings.Fields on the template, with the {file} token replaced by the
// touched file's path as ONE argv element and {dir} by its package dir (as
// one argv element). Substitution happens on the split fields, so a path
// with spaces or shell metacharacters can never be re-split — it travels to
// the tool as a single opaque argument.
func splitProjectCommand(template, root, fsPath string) []string {
	dir := packageDirArg(root, fsPath)
	argv := make([]string, 0, 8)
	for _, f := range strings.Fields(template) {
		switch f {
		case projectcmd.FilePlaceholder:
			argv = append(argv, fsPath)
		case projectcmd.DirPlaceholder:
			argv = append(argv, dir)
		default:
			f = strings.ReplaceAll(f, projectcmd.FilePlaceholder, fsPath)
			f = strings.ReplaceAll(f, projectcmd.DirPlaceholder, dir)
			argv = append(argv, f)
		}
	}
	return argv
}

// packageDirArg is the value substituted for the {dir} placeholder: the
// touched file's directory, "./"-prefixed and relative to root — the project
// root, where the hook runs the command ("" = the process CWD, the
// CWD-implicit session) — so "go vet {dir}" type-checks the file's whole
// package from where the manifest's commands are meant to run ("./" for a
// root-level file). A root-relative fsPath that does not live under root
// (shouldn't happen: fsPath is workdir-resolved) falls back to a CWD
// relative computation, which a CWD-implicit session is anyway.
func packageDirArg(root, fsPath string) string {
	rel := fsPath
	if root != "" {
		if r, err := filepath.Rel(root, fsPath); err == nil {
			rel = r
		}
	}
	dir := filepath.Dir(rel)
	if dir == "." {
		return "./"
	}
	return filepath.ToSlash("./" + dir)
}

// runAndWriteBack runs argv (which may rewrite path on disk, e.g. gofmt -w),
// then reports whether path changed. err is the run error (errHookTimeout on
// a deadline, nil on a clean run); a non-zero exit WITH output (gofmt on an
// unparseable file) is captured in out as well, because it is an observation
// — the file is left exactly as written, the hook just reports what it saw.
func runAndWriteBack(ctx context.Context, argv []string, root, path string) (changed bool, out string, err error) {
	before, rerr := os.ReadFile(path)
	if rerr != nil {
		return false, "", rerr
	}
	out, runErr := hookRunner(ctx, argv, root)
	after, werr := os.ReadFile(path)
	if werr != nil {
		return false, out, werr
	}
	return !stringEqual(before, after), out, runErr
}

// clipNote bounds a tool's output folded into a tool result so a chatty
// formatter or linter can't blow the context. Truncation, not a study — the
// hook is a cheap observation, and the full output is still recoverable by
// running the command through bash.
const noteCap = 2000

func clipNote(s string) string {
	if len(s) <= noteCap {
		return s
	}
	cut := noteCap
	// Don't split a multi-byte rune at the cap (the tool's output may carry
	// non-ASCII — a path, an error message).
	for cut > 0 && !utf8.ValidString(s[:cut]) {
		cut--
	}
	return s[:cut] + "\n...[output truncated]"
}

func stringEqual(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
