// project_command_hook.go is the post-edit hook and the turn-end lint pass
// for issue #129.
//
// After write_file / edit_file lands, the session runs the project's OWN
// format command on the file that was just touched, so an unformatted file
// never reaches review. Lint is NOT per-edit (it is slow and noisy —
// clippy, eslint): RunTurnEndLint runs it ONCE at the end of the turn, over
// the distinct files the turn touched, under the turn's total lint budget
// (RunTurnEndLint's doc). The hook is a best-effort observation: it appends
// what it ran and what the tool reported to the tool result — including
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
// intent is unexpressible, not dangerous. The per-command budget (default
// 10s, configurable via project.command_timeout_sec) and the 2000-byte
// output cap apply to every run; every hook note carries the elapsed time
// ("gofmt 0.2s", "eslint timed out after 10s") so a slow tool is obvious in
// the result.
//
// Two gates stand between a write/edit and a command run: workspace trust
// and the mode switch. Trust is a persisted, per-workspace, USER-level
// decision (the ~/.cortex user config's project.trusted list, set by
// `cortex project trust`); it can never come from the workspace itself —
// not from the project's .cortex/config.json, not from AGENTS.md — because
// the repository is the untrusted party. The mode (off | format | all,
// default all — config tools.post_edit_hook, env CORTEX_POST_EDIT_HOOK, REPL
// /hook) turns the hook down or off when formatters are slow or the run
// wants it quieter: an operator can lower it (the REPL's /hook, the
// per-call `hook: "skip"` argument) but nothing RAISES it above the
// configured ceiling, and trust is never affected by either. In "all" mode
// the turn-end lint pass (RunTurnEndLint) runs the project's lint once over
// the turn's touched files; "format" and "off" skip it.

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

// hookCommandBudget caps how long each hook command (format, then lint) may
// run. It reads from the Limits package var (HookCommandBudgetSec), which
// defaults to 10s and is set once at session construction from
// `project.command_timeout_sec`. A formatter that hangs must not stall the
// turn; on expiry the hook notes the timeout and moves on — the edit
// already succeeded.
func hookCommandBudget() time.Duration {
	return time.Duration(active.HookCommandBudgetSec) * time.Second
}

// errHookTimeout is the single sentinel the hook's exec path returns on a
// budget expiry. Callers report it by identity (errors.Is), never by
// string-matching, so a reworded message can't break the timeout note.
var errHookTimeout = errors.New("project command timed out")

// hookRunner runs a project command's argv directly (no shell) under the
// per-command budget, returning the elapsed time, the combined output, and
// any error (nil on success, errHookTimeout on deadline, or the exec error
// otherwise). dir is the command's working directory — the project root for
// an anchored session ("" = the process CWD, the CWD-implicit case) — which
// is what makes a "./"-prefixed {dir} substitution resolvable from where the
// manifest's commands are meant to run. It is a var — not an inline exec
// command call — so a test can install a command that actually HANGS and
// drive the real timeout path; no formatter ever hangs in practice, so the
// budget can't be reached end-to-end without this seam. The elapsed time is
// what the hook notes fold in ("gofmt 0.2s", "eslint timed out after 10s")
// so a slow tool is obvious. The default is runHookDirect.
var hookRunner = runHookDirect

// SetHookRunner swaps the package-wide hookRunner (the exec seam the hook
// and the turn-end lint pass run every command through) and returns the
// previous value so the caller can restore it. It is the EXPORTED seam:
// the internal tests touch hookRunner directly (same package), while a
// cmd/cortex session test that drives a REAL turn must stand in for the
// linter through it (the pass would otherwise exec the project's actual
// lint command), and the test package can't reach the unexported var.
// Production code never calls it.
func SetHookRunner(r func(ctx context.Context, argv []string, dir string) (elapsed time.Duration, out string, err error)) (prev func(ctx context.Context, argv []string, dir string) (time.Duration, string, error)) {
	prev = hookRunner
	hookRunner = r
	return prev
}

// HookRunner returns the current hookRunner (the value SetHookRunner
// swapped in, or runHookDirect). cmd/cortex's session tests pair it with
// SetHookRunner to save/restore across a turn.
func HookRunner() func(ctx context.Context, argv []string, dir string) (time.Duration, string, error) {
	return hookRunner
}

// runHookDirect is the production hookRunner: exec argv[0] argv[1:]
// directly — no shell (the injection-safety contract, package comment) —
// under the per-command budget, returning the elapsed time, combined output,
// and the run error. A trusted workspace may name repo-local binaries
// (./node_modules/.bin/…, ./bin/…), so no refusal is applied here: the trust
// decision already authorized running what this repo configures, and the
// argv contract (single substitution, no shell) keeps the file path one
// inert argument.
func runHookDirect(ctx context.Context, argv []string, dir string) (elapsed time.Duration, out string, err error) {
	cctx, cancel := context.WithTimeout(ctx, hookCommandBudget())
	defer cancel()
	if len(argv) == 0 {
		return 0, "", errors.New("empty command")
	}
	cmd := exec.CommandContext(cctx, argv[0], argv[1:]...)
	cmd.Dir = dir
	start := time.Now()
	raw, runErr := cmd.CombinedOutput()
	elapsed = time.Since(start)
	if cctx.Err() == context.DeadlineExceeded {
		return elapsed, strings.TrimSpace(string(raw)), errHookTimeout
	}
	return elapsed, strings.TrimSpace(string(raw)), runErr
}

// HookWouldRun reports whether the post-edit hook would auto-run cmd in role
// with the given trust state — the hook's own applicability rules
// (roleApplicable: format needs {file}; lint runs at the turn END via
// RunTurnEndLint and needs {file}/{dir} there) ANDed with the trust gate
// (untrusted → never). The report `cortex project commands` renders uses
// this so When can never drift from what the hook actually runs: a
// whole-project format command is never per-edit work, a lint without
// {file}/{dir} is never auto-run work (the turn-end pass needs a target),
// test/build NEVER run, and none of that changes on an untrusted workspace
// — the hook runs nothing there.
func HookWouldRun(role projectcmd.Role, cmd projectcmd.Command, trusted bool) bool {
	return trusted && roleApplicable(role, cmd)
}

// roleApplicable is the role half of the hook's applicability, mirroring
// the hook's own inline checks: the format role runs only a PER-FILE
// command (a whole-project format has no argument to substitute for the
// one file just touched), and the lint role only a command carrying the
// {file} or {dir} token (a whole-project lint has no per-touched-file
// target — the turn-end pass, like the old per-edit lint, is not its job).
// The test and build roles are never hook-run at all.
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

// runProjectCommandHook runs the post-edit FORMAT hook for the file at
// fsPath (the workdir-resolved path) and returns the note to append to the
// tool result. root is the project root the commands are meant to run from
// (the session's workdir, "" for a CWD-implicit session). mode is the
// resolved hook mode for this call (effectiveHookMode: the configured
// ceiling, the session-mode a /hook command lowered, and the per-call
// `hook: "skip"` already folded in): off runs nothing and is silent; format
// and all both run the per-file format command only — lint moved to the
// turn end (RunTurnEndLint) because per-edit lint is slow and noisy
// (clippy, eslint). It returns "" when there is nothing to report — no
// command set, no command that applies to the file's extension — so the
// tool result is byte-identical to the pre-hook behavior in the common
// case.
//
// On an untrusted workspace it runs NOTHING (trust is the hard gate, ahead
// of the mode): the one-line "hook inactive" note is emitted exactly once
// per SESSION (state is the session's per-run state, nil when the caller
// has none — such a caller has no session to announce for, so the note is
// never surfaced; that is also why every command-less or headless edit
// stays byte-identical to the pre-hook result), and the slot is marked
// consumed only when the note is actually emitted — a trusted workspace
// never touches the state.
//
// It never returns an error: the hook observes, it doesn't veto. Every
// failure (template refusal, spawn error, timeout, lint failure) is folded
// into the note string, because the contract is that the hook never blocks
// the edit — and every note carries the elapsed time ("gofmt 0.2s", "eslint
// timed out after 10s") so a slow tool is obvious in the result.
func runProjectCommandHook(ctx context.Context, cmds projectcmd.Commands, root, fsPath string, trusted bool, state *PostEditHookState, mode HookMode) string {
	if !trusted {
		if state != nil && state.inactiveNoteDue() {
			state.announceInactive()
			return "post-edit hook inactive: this workspace isn't trusted (`cortex project trust add <root>` to enable)"
		}
		return ""
	}
	if mode == HookModeOff {
		return "" // off: nothing runs, and it's the operator's choice, so silent
	}

	// Note: in "all" mode lint is NOT run per edit (clippy/eslint are slow
	// and noisy); RunTurnEndLint runs it once at the turn end over the
	// turn's touched files. The per-edit hook is format-only.

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
			elapsed, changed, out, err := runAndWriteBack(ctx, argv, root, fsPath)
			switch {
			case errors.Is(err, errHookTimeout):
				b.WriteString("note: " + cmdName(cmds.Format.Cmd) + " timed out after " + fmtSeconds(elapsed) + "; it was NOT run to completion")
			case err != nil:
				// A run error (spawn failure, gofmt on an unparseable file,
				// etc.). The tool's output, if any, rides along as an
				// observation — the file is left as written.
				if s := strings.TrimSpace(out); s != "" {
					fmt.Fprintf(&b, "note: %s ran with an error in %s (%v): %s", cmdName(cmds.Format.Cmd), fmtSeconds(elapsed), err, clipNote(s))
				} else {
					fmt.Fprintf(&b, "note: %s could not run: %v", cmdName(cmds.Format.Cmd), err)
				}
			case changed:
				b.WriteString("note: formatted " + fsPath + " with the project format command (" + cmds.Format.Cmd + ", " + fmtSeconds(elapsed) + ")")
			default:
				// Clean run, no change. Surface any tool output as an
				// observation; otherwise the file was already clean.
				if s := strings.TrimSpace(out); s != "" {
					b.WriteString("note: project format command reported (" + fmtSeconds(elapsed) + "): " + clipNote(s))
				}
			}
		default:
			// The template can't run as a plain argv (shell syntax) — or a
			// trusted check refused it. The file is left exactly as written.
			b.WriteString("note: project format command not run: " + refusal)
		}
	}

	return b.String()
}

// RunProjectCommandHook is the EXPORTED post-edit format hook: the same run
// as runProjectCommandHook (the identical trust/mode/extension gates, the
// identical note), for a session that wants to wrap the hook per turn —
// cmd/cortex's FormatHook (the FormatHookNoter capability, issue #219) runs
// through it so the turn's measurement-only receipt records the hook's note
// while the model sees the identical note. The production write_file/
// edit_file path calls the unexported runProjectCommandHook directly (same
// package); the exported form exists only for the session's wrapper, which
// sits in cmd/cortex.
func RunProjectCommandHook(ctx context.Context, cmds projectcmd.Commands, root, fsPath string, trusted bool, state *PostEditHookState, mode HookMode) string {
	return runProjectCommandHook(ctx, cmds, root, fsPath, trusted, state, mode)
}

// RunTurnEndLint is the turn-end lint pass (issue #129, piece 3): lint is
// slow and noisy per edit (clippy, eslint), so it runs ONCE per turn, here,
// instead of inside runProjectCommandHook. The session calls it at the
// clean-finalize point (the turn's FinalizeHook, alongside the #141
// test-loss receipt) with the DISTINCT files the turn touched (workdir-
// resolved paths, in first-touch order), so the model sees lint problems
// while it can still fix them (the finalize round appends the model's
// answer to the turn's answer) and a human sees them in the REPL and in
// `cortex turn` output (the session's lint receipt) and in the journal
// (the session folds the receipt into the capture summary).
//
// Gates, in order — all fail closed:
//   - untrusted workspace: nothing runs (trust is the hard gate, ahead of
//     the mode, exactly like the per-edit hook);
//   - no lint command, or a lint command without {file}/{dir}: nothing to
//     run (a whole-project lint is reported but never auto-run — the same
//     rule roleApplicable applies);
//   - no touched files (or none the command's extension set applies to): nothing
//     to lint. A touched file that no longer exists on disk is skipped too:
//     the turn may have DELETED one of the files it wrote (write_file then
//     remove_path), and linting a missing path would report a spurious
//     "could not run" finding for a file that is gone on purpose.
//
// The caller gates the MODE (only "all" runs lint — "format" and "off" skip
// it) and passes the turn's total lint budget (budget, seconds; <=0 means
// "use the default", currently 60s — project.turn_lint_budget_sec): the
// deadline is start + budget, the moment the budget runs out. It binds TWO
// ways: a run that has not started when the deadline passes is not run, and
// a run in progress is cut off at the deadline — the deadline travels into
// each hookRunner call as the run's context, which caps the per-command
// budget (hookCommandBudget, project.command_timeout_sec) at the turn
// deadline. So the pass can never overrun the total budget even when
// command_timeout_sec > turn_lint_budget_sec, and a deadline-cancelled run
// is reported as a budget hit ("turn lint budget (Ns) exhausted — M of R
// runs not run"), not as "timed out after 10s". The per-command budget
// still applies on its own (a broken linter in a long, cheap pass must not
// hang the turn).
//
// For {file} commands each touched file is linted once, in the order the
// turn touched them. For {dir} commands (per-package tools like
// "go vet {dir}") the files are deduplicated by their package dir — one
// run per distinct dir, in first-touch dir order — so several edits in one
// turn produce exactly one lint run per touched dir. A clean run (exit 0,
// no output) is silent; a finding is folded into the receipt as one line
// per run, clipped to 2000 bytes like every other hook note. It never
// returns an error: like the per-edit hook, the pass observes, it doesn't
// veto.
func RunTurnEndLint(ctx context.Context, cmds projectcmd.Commands, root string, files []string, trusted bool, budget time.Duration) string {
	if !trusted {
		return "" // untrusted: the hard gate — nothing runs (the per-edit hook already announced the inactive state)
	}
	if !lintApplies(cmds.Lint, "") {
		return "" // no lint command, or a whole-project lint: nothing to auto-run
	}
	// Zero means "the caller didn't arm a budget" (a hand-built test
	// session): the default total budget applies. A NEGATIVE budget is a
	// deadline already in the past (the test seam for "the budget is spent
	// before the pass starts"): the budget line reports the configured
	// duration, and nothing runs.
	negative := budget < 0
	if negative {
		return "lint: turn lint budget (" + fmtSeconds(-budget) + ") exhausted — no runs were made"
	}
	if budget <= 0 {
		budget = time.Duration(DefaultLimits().TurnLintBudgetSec) * time.Second // caller not armed: the default total budget
	}
	deadline := time.Now().Add(budget)
	if hookTemplateRejected(cmds.Lint.Cmd) != "" {
		return "" // shell-syntax template: unexpressible without a shell — nothing runs (the per-edit hook notes this for format; the turn-end pass has no tool result to fold a refusal into, and the session gates the mode anyway)
	}

	// Build the argv list: per-file is one run per touched file (the
	// command's Extends gate applies per file); per-package dedups by the
	// file's package dir, in first-touch dir order.
	type lintRun struct {
		argv    []string
		display string // what the receipt shows for this run (the file or the dir)
	}
	var runs []lintRun
	seenDir := map[string]bool{}
	for _, f := range files {
		if !appliesTo(cmds.Lint, f) {
			continue
		}
		// Skip files the turn deleted since writing them (write_file then
		// remove_path): linting a missing path reports a spurious finding
		// for a file that is gone on purpose. The workdir anchor is root —
		// the directory the pass runs the commands in (same as the {dir}
		// substitution); "" (a CWD-implicit session) means the process CWD.
		if _, err := os.Stat(filepath.Join(root, f)); err != nil {
			continue
		}
		argv, ok, _ := hookGate(cmds.Lint.Cmd, root, f, true) // trust already checked above
		if !ok {
			continue
		}
		if strings.Contains(cmds.Lint.Cmd, projectcmd.DirPlaceholder) {
			dir := packageDirArg(root, f)
			if seenDir[dir] {
				continue // one lint run per touched dir
			}
			seenDir[dir] = true
		}
		runs = append(runs, lintRun{argv: argv, display: f})
	}
	if len(runs) == 0 {
		return ""
	}

	name := cmdName(cmds.Lint.Cmd)
	// The deadline binds EVERY run, not just the gaps between them: it
	// travels into each hookRunner call as the run's context, so a run in
	// progress is cut off at the turn deadline (and so is the per-command
	// budget, which WithTimeout caps at the remaining time) — the pass can
	// never overrun its total budget even when command_timeout_sec >
	// turn_lint_budget_sec.
	lctx, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()
	var findings []string
	budgetHit := false
	completed := 0
	for _, r := range runs {
		if time.Now().After(deadline) {
			budgetHit = true
			break
		}
		elapsed, out, err := hookRunner(lctx, r.argv, root)
		// A run is "completed" when it ran to its own outcome (a finding or
		// a clean run), whether or not the pass then stops on the budget. A
		// run cut off at the turn deadline (the cases below) is NOT
		// completed — it counts as not run, so the receipt's "N of M runs
		// not run" includes the cut-off run itself.
		switch {
		case lctx.Err() == context.DeadlineExceeded:
			// Checked FIRST, ahead of the timeout sentinel: a run cut off at
			// the TURN deadline is a budget hit, not a finding about the
			// file. The order matters — the production runner (runHookDirect)
			// maps any deadline expiry to errHookTimeout, including the
			// parent's (the turn budget's), so when the turn deadline fires
			// mid-run this case and the next are BOTH true; only this one is
			// correct there. This is a budget hit, not a per-file timeout —
			// the run's half-output (if any) is discarded with the cut-off.
			budgetHit = true
		case errors.Is(err, errHookTimeout):
			// The per-command budget (hookCommandBudget) is capped at the
			// deadline by lctx, so after the case above this sentinel only
			// fires when the per-command budget is SHORTER than the time
			// left in the turn budget — a genuine per-command timeout, not a
			// budget hit.
			findings = append(findings, name+" for "+r.display+" timed out after "+fmtSeconds(elapsed))
			completed++
		case err != nil && strings.TrimSpace(out) == "":
			findings = append(findings, name+" for "+r.display+" could not run: "+err.Error())
			completed++
		case strings.TrimSpace(out) != "":
			findings = append(findings, name+" for "+r.display+" ("+fmtSeconds(elapsed)+"): "+clipNote(strings.TrimSpace(out)))
			completed++
		default:
			completed++ // a clean run (exit 0, no output) is silent
		}
		// The budget is spent: stop (the remaining runs are not run).
		if budgetHit {
			break
		}
	}

	if len(findings) == 0 && !budgetHit {
		return "" // clean: no extra round, no receipt
	}
	var b strings.Builder
	b.WriteString("lint: ")
	for i, f := range findings {
		if i > 0 {
			b.WriteString("\n")
		}
		b.WriteString(f)
	}
	if budgetHit {
		// len(runs)-completed, not len(runs)-runNo: a run cut off in
		// progress counts as not run, so the message reads "M of R runs not
		// run" with M including the cut-off run.
		fmt.Fprintf(&b, "\nturn lint budget (%s) exhausted — %d of %d runs not run", fmtSeconds(budget), len(runs)-completed, len(runs))
	}
	return b.String()
}

// cmdName is the first word of a hook command's template ("gofmt -w
// {file}" → "gofmt", "./bin/my-fmt {file}" → "./bin/my-fmt") — the short
// identity the elapsed-time notes use ("gofmt 0.2s").
func cmdName(template string) string {
	f := strings.Fields(template)
	if len(f) == 0 {
		return "the project command"
	}
	return f[0]
}

// fmtSeconds renders an elapsed duration the way the hook notes read it: a
// sub-second run as fractional seconds ("0.2s"), a whole-second run as an
// integer ("10s"), and the timeout's "timed out after Ns" uses the budget
// (which is always a whole second). One decimal for sub-second keeps a slow
// tool obvious without a wall of digits.
func fmtSeconds(d time.Duration) string {
	if d < time.Second {
		return fmt.Sprintf("%.1fs", d.Seconds())
	}
	return fmt.Sprintf("%ds", int(d.Round(time.Second).Seconds()))
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

// lintApplies reports whether the lint role has a command the harness runs
// automatically for path: non-empty, carrying the {file} or {dir} token
// (per-file or per-package — a whole-project lint is never auto-run), and
// (when path is non-empty) applicable to the file's extension. With an
// empty path the extension gate is skipped — RunTurnEndLint gates each
// touched file through appliesTo itself, and the turn-level question is
// only "is there a lint command with a target".
func lintApplies(cmd projectcmd.Command, path string) bool {
	return cmd.Cmd != "" &&
		(cmd.PerFile || strings.Contains(cmd.Cmd, projectcmd.DirPlaceholder)) &&
		(path == "" || appliesTo(cmd, path))
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
// then reports whether path changed. elapsed is how long the run took (what
// the hook's note folds in), err is the run error (errHookTimeout on a
// deadline, nil on a clean run); a non-zero exit WITH output (gofmt on an
// unparseable file) is captured in out as well, because it is an observation
// — the file is left exactly as written, the hook just reports what it saw.
func runAndWriteBack(ctx context.Context, argv []string, root, path string) (elapsed time.Duration, changed bool, out string, err error) {
	before, rerr := os.ReadFile(path)
	if rerr != nil {
		return 0, false, "", rerr
	}
	dur, runOut, runErr := hookRunner(ctx, argv, root)
	after, werr := os.ReadFile(path)
	if werr != nil {
		return dur, false, runOut, werr
	}
	return dur, !stringEqual(before, after), runOut, runErr
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
