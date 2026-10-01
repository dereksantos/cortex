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
// Workspace trust is the ONLY gate. Trust is a persisted, per-workspace,
// USER-level decision (the ~/.cortex user config's project.trusted list,
// set by `cortex project trust`); it can never come from the workspace
// itself — not from the project's .cortex/config.json, not from AGENTS.md —
// because the repository is the untrusted party. On an UNTRUSTED workspace
// (the default) the hook runs nothing at all: a trusted repo may use
// repo-local binaries (./node_modules/.bin/eslint, ./bin/fmt) of ANY
// language, so a per-tool allowlist would only couple the hook to specific
// tools and keep producing bypasses. The first write/edit of a session
// gets a one-line note saying the hook is inactive and how to enable it;
// later edits stay silent.
//
// Injection safety on the trusted path: commands run as a plain argv —
// the template is split into argv BEFORE {file}/{dir} are substituted,
// each as a SINGLE argv element (splitProjectCommand), and
// runHookDirect launches the argv with exec.CommandContext directly: no
// shell anywhere in the pipeline. A template containing shell-control
// characters (pipe, chain, redirect, command substitution, subshell,
// newline) can't run without a shell, so it is skipped with a note — its
// intent is unexpressible, not dangerous. The 10s budget and the 2000-byte
// output cap apply to every run.

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
// Absent capability → untrusted (false): the hook runs nothing on a
// workspace that never said it was trusted, so the default is the safe one.
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

// hookRunner runs a project command's argv directly (no shell) under the
// format budget, returning its combined output plus any error (nil on
// success, errHookTimeout on deadline, or the exec error otherwise). dir is
// the command's working directory — the project root for an anchored
// session ("" = the process CWD, the CWD-implicit case) — which is what
// makes a "./"-prefixed {dir} substitution resolvable from where the
// manifest's commands are meant to run. It is a var — not an inline exec
// command call — so a test can install a command that actually HANGS and
// drive the real timeout path; no formatter ever hangs in practice, so the
// budget can't be reached end-to-end without this seam. The default is
// runHookDirect.
var hookRunner = runHookDirect

// runHookDirect is the production hookRunner: exec argv[0] argv[1:]
// directly — no shell (the injection-safety contract, package comment) —
// under the format budget, returning combined output plus the run error.
// A trusted workspace may name repo-local binaries (./node_modules/.bin/…,
// ./bin/…), so no refusal is applied here: the trust decision already
// authorized running what this repo configures, and the argv contract
// (single substitution, no shell) keeps the file path one inert argument.
func runHookDirect(ctx context.Context, argv []string, dir string) (string, error) {
	cctx, cancel := context.WithTimeout(ctx, formatBudget)
	defer cancel()
	if len(argv) == 0 {
		return "", errors.New("empty command")
	}
	cmd := exec.CommandContext(cctx, argv[0], argv[1:]...)
	cmd.Dir = dir
	raw, runErr := cmd.CombinedOutput()
	if cctx.Err() == context.DeadlineExceeded {
		return strings.TrimSpace(string(raw)), errHookTimeout
	}
	return strings.TrimSpace(string(raw)), runErr
}

// HookWouldRun reports whether the post-edit hook would auto-run cmd in role
// with the given trust state — the hook's own applicability rules
// (roleApplicable: format needs {file}, lint needs {file}/{dir}, test and
// build are never hook-run) ANDed with the trust gate (untrusted → never).
// The report `cortex project commands` renders uses this so runs_now can
// never drift from what the hook actually runs: a whole-project format
// command is never per-edit work, a lint without {file}/{dir} is never
// per-edit work, test/build NEVER run, and none of that changes on an
// untrusted workspace — the hook runs nothing there.
func HookWouldRun(role projectcmd.Role, cmd projectcmd.Command, trusted bool) bool {
	return trusted && roleApplicable(role, cmd)
}

// roleApplicable is the role half of the hook's applicability, mirroring
// the hook's own inline checks: the format role runs only a PER-FILE
// command (a whole-project format has no argument to substitute for the
// one file just touched), and the lint role only a command carrying the
// {file} or {dir} token (a whole-project lint is not the hook's job —
// exactly the hook's `lintApplies`-style inline test, without the Extends
// gate the hook applies per touched file). The test and build roles are
// never hook-run at all.
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

// hookTemplateRejected reports why a trusted template cannot run as a plain
// argv ("") or a note to fold into the tool result: a template with
// shell-control characters (pipe, chain, redirect, command substitution,
// subshell, newline) is unexpressible without a shell — the hook has no
// shell, so the command is skipped rather than guessed at. The tokens
// {file}/{dir} are the template's own argument slots and are stripped
// before the scan, so their braces are not mistaken for shell syntax.
func hookTemplateRejected(template string) string {
	checked := strings.ReplaceAll(template, projectcmd.FilePlaceholder, "")
	checked = strings.ReplaceAll(checked, projectcmd.DirPlaceholder, "")
	if strings.ContainsAny(checked, "|&;<>`$()\n") {
		return "the template uses shell syntax (pipe, chain, redirect, or substitution), which the hook cannot run without a shell; run it through bash instead"
	}
	return ""
}

// hookGate decides whether a resolved command may RUN in this hook: role
// applicability (format needs {file}, lint needs {file}/{dir}), trust, and
// a template with no shell-control characters. It returns (argv, ok, note)
// — ok=false means the command must not run, and note (possibly "") is the
// exact refusal text to fold into the tool result. The template is split
// into argv BEFORE {file}/{dir} are substituted, each token as ONE argv
// element (splitProjectCommand): the file path is model-controlled input,
// and the argv exec passes it as a single opaque argument that no shell
// ever re-parses.
func hookGate(template, root, fsPath string, trusted bool) (argv []string, ok bool, note string) {
	if !trusted {
		return nil, false, "the workspace is not trusted"
	}
	if note := hookTemplateRejected(template); note != "" {
		return nil, false, note
	}
	return splitProjectCommand(template, root, fsPath), true, ""
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
// On an untrusted workspace it runs NOTHING. The one-line "hook inactive"
// note is emitted exactly once per SESSION: state is the session's per-run
// state (nil when the caller has none — such a caller has no session to
// announce for, so the note is never surfaced; that is also why every
// command-less or headless edit stays byte-identical to the pre-hook
// result), and the slot is marked consumed only when the note is actually
// emitted — a trusted workspace never touches the state.
//
// It never returns an error: the hook observes, it doesn't veto. Every
// failure (template refusal, spawn error, timeout, lint failure) is folded
// into the note string, because the contract is that the hook never blocks
// the edit.
func runProjectCommandHook(ctx context.Context, cmds projectcmd.Commands, root, fsPath string, trusted bool, state *PostEditHookState) string {
	if !trusted {
		if state != nil && state.inactiveNoteDue() {
			state.announceInactive()
			return "post-edit hook inactive: this workspace isn't trusted (`cortex project trust add <root>` to enable)"
		}
		return ""
	}

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
		default:
			// The template can't run as a plain argv (shell syntax) — or a
			// trusted check refused it. The file is left exactly as written.
			b.WriteString("note: project format command not run: " + refusal)
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
		default:
			// A skipped lint is advisory (folded into the result), so the
			// refusal is noted for the same reason the format refusal is.
			b.WriteString("note: project lint command not run: " + refusal)
		}
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

// splitProjectCommand turns a template into the argv the hook execs
// directly: strings.Fields on the template, with the {file} token replaced
// by the touched file's path as ONE argv element and {dir} by its package
// dir (as one argv element). Splitting happens BEFORE substitution, so a
// path with spaces or shell metacharacters can never be re-split — it
// travels to the tool as a single opaque argument.
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
