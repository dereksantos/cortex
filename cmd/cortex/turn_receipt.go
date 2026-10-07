// turn_receipt.go is the measurement-only turn receipt (issue #219): after a
// turn that ran tools, the harness reads the workspace back and measures what
// the turn actually left behind, in three facts a small model is not reliably
// going to report about its own work:
//
//   - files changed: the workspace's own `git diff --stat` block PLUS the
//     UNTRACKED files present in the turn's workspace (a diff alone misses a
//     file the turn only wrote — it is not tracked yet; the harness cannot
//     attribute which untracked file this exact turn created, so the fact is
//     scoped to what is on disk at turn end), measured in the session's
//     workspace (both git commands run with cmd.Dir = cs.Workdir()), so an
//     explicit-root workspace (serve, --project, tests) measures ITS repo,
//     not wherever the process started;
//   - verification: the exit codes of the project's OWN test/build commands
//     that the model ran THIS turn — its own runs, recorded by the per-turn
//     bash recorder (receiptBash/receiptBashOutcome, paired on the same
//     call in coderDispatcher). A run records the exit code from the
//     tools.BashOutcome that tools.Execute returned for THAT call (never
//     parsed from the result text); a call the tool disabled, the shell
//     gate REFUSED, or the user DECLINED never ran and records (not run: …)
//     — a blocked check's result is unknown, and it must never render as an
//     exit code, least of all exit 0. The harness does NOT run the
//     project's test/build commands itself: running project-declared
//     commands is a trust-gated decision (issue #129's documented rule —
//     trust is the only gate), and a measurement path that ran them on
//     every turn, untrusted workspaces included, broke that rule and added
//     up to 20s of latency to every chat turn. A turn that ran no
//     verification has no verification fact;
//   - unformatted: the files the post-edit format hook could not verify —
//     the production hook call sites run through the per-turn wrapper
//     (FormatHook, the tools.FormatHookNoter capability) so the files the
//     hook FAILED to format (could not run, failed, or timed out — the
//     file is left as written, its cleanliness unverified) are recorded on
//     the receipt while the model still sees the identical note. A clean
//     run and a run the formatter rewrote (the file is clean NOW — the
//     hook fixed it) do NOT record a file: the label says the formatter
//     failed, and a file it formatted is not one.
//
// MEASUREMENT ONLY, by design: the receipt never blocks the turn, never
// fails a tool call, never adds a finalize round, and never changes what
// the model sees. It is computed at the end of turn.go, for a turn only
// when it ran tools (turnUsedTools — a planning turn or a tools-less turn
// has nothing to measure and computes nothing), and rides TurnResult.Receipt
// for a caller to surface. Every measurement degrades to silence: a
// non-repository workspace reports no files-changed line, a turn that ran
// no verification contributes no verification run, and a receipt with no
// facts renders "".
//
// The bash recorder is the measurement's model-side arm: coderDispatcher
// (loop.go) calls receiptBash before every bash call, pairing the model's
// own test/build commands with their outcomes. The recorder is cleared at
// the START of every turn (turn.go's receiptDrop), mirroring
// testwatchDrop's lifecycle, so a command from an earlier turn that never
// reached turn end (error/interrupt) can't leak into this turn's receipt.
package main

import (
	"context"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/dereksantos/cortex/internal/projectcmd"
	"github.com/dereksantos/cortex/internal/tools"
)

// receiptMaxStatLines bounds the files-changed fact: the per-file
// diff --stat lines are kept up to this many; the block's summary tail
// (the "N files changed, … insertions(+), … deletions(-)" line) is ALWAYS
// kept, it is the fact's headline.
const receiptMaxStatLines = 8

// receiptMaxVerificationLines bounds the verification fact the same way:
// the first few runs are shown and the rest summarized, so a turn that
// ran a build per package still gets a bounded receipt.
const receiptMaxVerificationLines = 6

// receiptVerification is one verification run recorded on the receipt:
// the role (test|build), the exact command line, the exit code the process
// returned (0 only when it actually exited 0), and the elapsed time of the
// run. When notRun is set the command NEVER RAN — the shell gate refused
// it or the user declined it — and the line renders (not run: …) instead of
// an exit code: a blocked check's result is unknown, and it must never
// render as a pass. The exit code and elapsed time are then unmeasured.
type receiptVerification struct {
	role     string
	command  string
	exitCode int
	elapsed  time.Duration
	notRun   string // "" = it ran; otherwise the refusal the line renders
}

// turnReceipt is the measurement-only receipt for one turn (issue #219 and
// the #220 step-2 checklist fact): the three #219 facts above plus the
// checklist fact, assembled by computeReceipt and rendered by render(). All
// fields are empty on a turn that ran no tools or measured nothing —
// render() then returns "" and TurnResult.Receipt stays empty.
type turnReceipt struct {
	filesChanged       []string // git diff --stat lines + the untracked files present in the turn's workspace
	gitWorkspace       bool     // true when the turn's workspace is a git repository (a clean tree still renders the files-changed section: "nothing changed")
	verification       []receiptVerification
	unformatted        []string // files the post-edit format hook failed to verify (could not run, failed, timed out)
	hadReceiptBashRuns bool     // true when the bash recorder saw a verification run this turn
	// checklistMissing is the #220 step-2 fact: the task's checklist items
	// the reply does NOT account for (checklistMissingItems — per item, every
	// significant word, word-prefix match; see checklistItemPresent). nil
	// when the task has no checklist or the reply met every item — a turn
	// with no missing-item fact renders no "checklist:" section.
	checklistMissing []string
}

// receiptBash records one test/build command the model is about to run in
// a bash call, for the receipt's verification fact. Called from
// coderDispatcher (loop.go) BEFORE the bash call runs, so the recorded
// command and its outcome (resolved by receiptBashOutcome from the
// dispatcher's own knowledge of the call's fate) pair on the same call.
// Only commands that are a recognized run of the PROJECT's own test or
// build command are recorded (receiptBashRole: a two-field-or-longer
// discovered command must be shared field-for-field up to the shorter
// side — the model ran the check, possibly with extra flags — and no
// field past the first may carry shell-control syntax; a discovered
// command of ONE field, a bare `make` or a single test binary, matches
// ONLY the exact command). Every other command (rm, ls, git, a foreign
// toolchain, even `go vet` when the project declared `go test ./...`) is
// not this project's verification and contributes nothing. A session with
// no discovered test/build command records nothing.
func (cs *CortexSession) receiptBash(command string) {
	if strings.TrimSpace(command) == "" {
		return
	}
	role, ok := cs.receiptBashRole(command)
	if !ok {
		return
	}
	cs.receiptModelBash = append(cs.receiptModelBash, receiptModelBashRun{
		role:    string(role),
		command: command,
	})
}

// receiptBashRole reports whether command is a run of the project's own
// test or build command, and which role it is. Two shapes of discovery:
//
//   - a discovered command of ONE field (a bare `make`, a single test
//     binary like `failcheck`): it has no prefix to share — only the EXACT
//     command is a run of it (`failcheck -v` is a different command line,
//     `make` alone is the build);
//   - a discovered command of TWO or more fields: the model must have
//     shared at least the first TWO fields of the DISCOVERED command (a
//     bare `go` is a toolchain, not a run of `go test ./...`) and the line
//     must carry NO SHELL-CONTROL field ANYWHERE past its first field
//     (`|`, `||`, `&&`, `;`, `&`, `>`, `<`, …): `go test ./... | tail` is a
//     pipeline whose exit status is tail's, `go test ./... -v || true`
//     never fails, and `go test -v ./... | tail -5` is a pipeline with an
//     extra flag BEFORE the operator — none of them is a run of the check,
//     so none is recorded as one. The model may run the check with EXTRA
//     FIELDS after the shared prefix (`go test ./... -v` vs discovery
//     `go test ./...`); running a PREFIX of the discovered command (fewer
//     fields than discovery) is a different, broader command and is not
//     accepted — the recorded exit would be a compound or broader
//     command's, not the check's.
//
// Only the PROJECT's own commands count — `go vet` (same toolchain, a
// DIFFERENT check the project never declared) is not this project's
// verification. A session with no discovered test/build command records
// nothing.
func (cs *CortexSession) receiptBashRole(command string) (projectcmd.Role, bool) {
	fields := strings.Fields(command)
	if len(fields) == 0 {
		return "", false
	}
	for _, role := range []projectcmd.Role{projectcmd.RoleTest, projectcmd.RoleBuild} {
		cmd, ok := cs.projectCommands.Get(role)
		if !ok {
			continue
		}
		cmds := strings.Fields(strings.TrimSpace(cmd.Cmd))
		if len(cmds) == 0 {
			continue
		}
		if len(cmds) == 1 {
			// A one-field discovery is a bare command: only the exact
			// command is a run of it (any extra field makes it a different
			// command line).
			if len(fields) == 1 && fields[0] == cmds[0] {
				return role, true
			}
			continue
		}
		if len(fields) < 2 {
			continue
		}
		shared := 0
		for shared < len(cmds) && shared < len(fields) && cmds[shared] == fields[shared] {
			shared++
		}
		if shared < 2 {
			continue
		}
		// The model must have shared at least the first two fields of the
		// DISCOVERED command. If the model has FEWER fields than discovery,
		// the model ran a prefix of the check (e.g. "go test" vs "go test
		// ./...") — that is not a run of the check, it is a different,
		// broader command. Reject it.
		if len(fields) < len(cmds) {
			continue
		}
		// The model may have EXTRA FIELDS after the shared prefix (e.g.
		// "go test ./... -v") — that is a run of the check with extra
		// flags. But if ANY field past the first carries shell-control
		// syntax, the line is a compound command, not a bare run of the
		// check — a flag before the operator ("go test -v ./... | tail")
		// does not hide it: the exit status would be the pipeline's or
		// chain's, not the check's.
		compound := false
		for _, f := range fields[1:] {
			if isShellControl(f) {
				compound = true
				break
			}
		}
		if compound {
			continue
		}
		return role, true
	}
	return "", false
}

// isShellControl reports whether a command field carries shell-control
// syntax (pipes, chains, redirects, substitutions, command separators,
// comments) — the same character set the shell-approval prefix matcher
// refuses to widen across (containsShellControl). Any field like this past
// the command's first field means the line is a compound command, not a
// bare run of the check: the recorded exit would be the pipeline's or
// chain's, not the check's.
func isShellControl(field string) bool {
	return strings.ContainsAny(field, "|;&<>`$\n")
}

// receiptModelBashRun is one model-side test/build run recorded before the
// bash call: the role it matched and the exact command the model ran. The
// outcome (exit code, not-run reason, elapsed) is filled in by
// receiptBashOutcome once the call has run.
type receiptModelBashRun struct {
	role     string
	command  string
	exitCode int
	elapsed  time.Duration
	notRun   string // "" = it ran; otherwise the refusal rendered instead of an exit code
}

// receiptBashOutcome fills in the outcome of the last recorded model bash
// run for command: how the bash call fared, from the structured
// tools.BashOutcome that tools.Execute returned for THIS call (issue #219).
// It is NEVER inferred from the message text: the bash tool reports a
// non-zero exit in its result text (the "[exit error: …]" marker), and a
// CLEAN (msg, nil) for a command the tool disabled, a validation rejected,
// or the shell gate refused or the user declined — the absence of a marker
// is not an exit code. outcome.Ran is a POSITIVE signal, set only on the
// path that spawned the process, and carries the process's exit code;
// Ran=false records (not run: …), never an exit. A tool-level error
// (callErr, e.g. a rejected tool call) means the command never ran too.
// Called from coderDispatcher (loop.go) AFTER the bash call has run. A
// command receiptBash did not record (a non-verification command) is a
// no-op.
func (cs *CortexSession) receiptBashOutcome(command string, outcome tools.BashOutcome, callErr error, elapsed time.Duration) {
	if len(cs.receiptModelBash) == 0 {
		return
	}
	last := &cs.receiptModelBash[len(cs.receiptModelBash)-1]
	if last.command != command {
		return
	}
	last.elapsed = elapsed
	switch {
	case callErr != nil:
		// A tool-level error: the command never ran.
		last.notRun = "not run: " + callErr.Error()
	case outcome.Ran:
		last.exitCode = outcome.ExitCode
	default:
		// The tool disabled the call, a validation rejected it, or the
		// shell gate refused it / the user declined: it never ran.
		last.notRun = "not run: refused, declined, or disabled"
	}
}

// FormatHook is the session's per-turn wrapper around the post-edit
// format hook (the tools.FormatHookNoter capability): it runs the hook
// exactly as the production write_file/edit_file path does (the same
// trust/mode/extension gates — runProjectCommandHook carries them; the
// hookSkip flag is the per-call `hook: "skip"` opt-out, which must lower
// the effective mode the way the direct path does) and records the hook's
// OUTCOME on the receipt. A file is recorded only on HookOutcomeFailed
// (the formatter could not run, failed, or timed out — the file is left as
// written, its cleanliness unverified); a clean run, a run the formatter
// REWROTE (the file is clean now — the hook fixed it), and a run that
// never happened (untrusted workspace, mode off, no applicable command)
// record nothing, so the receipt's "unformatted:" line names exactly the
// files the formatter could NOT verify. The note is returned UNCHANGED, so
// the model sees exactly what the pre-receipt hook printed. Measurement
// only: this wrapper never fails the edit.
//
// It is the tools.FormatHookNoter capability: the production hook call
// sites (internal/tools' write_file/edit_file and the in-place-rewrite
// hook) run through it when the session implements it, and run the hook
// directly otherwise (the model-facing note is byte-identical either
// way).
func (cs *CortexSession) FormatHook(ctx context.Context, fsPath string, hookSkip bool) string {
	// The mode the hook runs in is the session's effective mode (ceiling +
	// session /hook mode) FURTHER lowered to off for a per-call `hook:
	// "skip"` — the same fold the direct path makes via
	// tools.effectiveHookMode(state, skip), which this package can't call
	// (unexported), so it is mirrored here: skip && mode != off → off. With
	// the fold in, a skip runs nothing and notes nothing; without it, the
	// session's effective mode applies. (The mode the caller passes is the
	// pre-fold session effective mode — tools.FormatHookNoter's seam, which
	// hands the flag through, resolves it the same way.)
	mode := tools.EffectiveHookMode(cs.HookState())
	if hookSkip && mode != tools.HookModeOff {
		mode = tools.HookModeOff
	}
	note, outcome := tools.RunProjectCommandHook(ctx, cs.ProjectCommands(), cs.Workdir(), fsPath, cs.WorkspaceTrusted(), cs.HookState(), mode)
	if outcome == tools.HookOutcomeFailed && fsPath != "" {
		// The receipt names the file WORKSPACE-RELATIVE, like the model's
		// own paths: fsPath is the hook's filesystem form (the production
		// call sites resolve it against the workdir — resolveWorkdir), so
		// strip the workdir prefix. A path outside the workdir (or a
		// CWD-implicit session, where fsPath is already CWD-relative) is
		// kept as-is.
		name := fsPath
		if wd := cs.Workdir(); wd != "" && strings.HasPrefix(fsPath, wd+string(filepath.Separator)) {
			name = strings.TrimPrefix(fsPath, wd+string(filepath.Separator))
		}
		cs.receiptUnformatted = append(cs.receiptUnformatted, name)
	}
	return note
}

// computeReceipt computes this turn's receipt from the turn's
// measurements and stores it on the session (cs.receipt) so the turn
// boundary can surface it (turn.go). It assembles the files-changed fact
// (a fresh git read), the verification fact (the model's recorded test/build
// runs), and the unformatted fact (deduped, sorted). The harness runs no
// project commands of its own for the receipt — the verification fact is
// precisely "the verification commands that RAN this turn" (the model's
// bash runs, recorded by the bash recorder), and nothing else. The
// checklistMissing fact (#220 step 2) is NOT set here: it is measured off
// the turn's reply (the model's final answer, content) and the task
// (input), which cs.turn holds in its own scope — it is set by the caller
// (turn.go) right after computeReceipt returns, before the receipt is
// rendered and surfaced on TurnResult.Receipt / the kindNote.
func (cs *CortexSession) computeReceipt(ctx context.Context) turnReceipt {
	fc := cs.receiptFilesChanged()
	r := turnReceipt{
		filesChanged:       fc,
		gitWorkspace:       len(fc) > 0 || cs.gitWorkspace(),
		verification:       cs.receiptModelVerifications(),
		unformatted:        cs.receiptUnformattedPaths(),
		hadReceiptBashRuns: len(cs.receiptModelBash) > 0,
	}
	cs.receipt = r
	return r
}

// gitWorkspace reports whether the turn's workspace (cs.Workdir()) is a git
// repository — the files-changed fact's own repository probe. A clean tree
// (no changes) still makes the workspace a git workspace: the receipt then
// renders the files-changed section with no lines, which says "nothing
// changed", in contrast to a non-repository workspace, which omits the
// section entirely.
func (cs *CortexSession) gitWorkspace() bool {
	wd := cs.Workdir()
	if wd == "" {
		return false
	}
	rev := exec.Command("git", "rev-parse", "--is-inside-work-tree")
	rev.Dir = wd
	out, err := rev.Output()
	return err == nil && strings.TrimSpace(string(out)) == "true"
}

// receiptModelVerifications turns the recorded model-side bash runs into
// the receipt's verification entries, in run order. The two structs share
// the same fields in the same order (receiptModelBashRun is the recorder's
// form, receiptVerification the receipt's rendered form), so a straight
// conversion is the copy.
func (cs *CortexSession) receiptModelVerifications() []receiptVerification {
	out := make([]receiptVerification, 0, len(cs.receiptModelBash))
	for _, r := range cs.receiptModelBash {
		out = append(out, receiptVerification(r))
	}
	return out
}

// receiptUnformattedPaths returns the receipt's unformatted files, deduped
// and sorted for stable rendering.
func (cs *CortexSession) receiptUnformattedPaths() []string {
	if len(cs.receiptUnformatted) == 0 {
		return nil
	}
	seen := map[string]bool{}
	var out []string
	for _, p := range cs.receiptUnformatted {
		if !seen[p] {
			seen[p] = true
			out = append(out, p)
		}
	}
	sort.Strings(out)
	return out
}

// receiptFilesChanged reads the workspace's tracked changes (a `git
// diff --stat` block) and the untracked files the turn created (`git
// status --porcelain` "?? " entries) when the turn's workspace
// (cs.Workdir()) is a git repository. Both commands run in the workspace
// (cmd.Dir = wd) — without it they would measure wherever the PROCESS
// started (the wrong repo, or none, on an explicit-root workspace). A
// non-repository, a missing git binary, or a failed invocation degrades to
// an empty fact — the receipt simply has no files-changed line. The per-file
// stat lines are bounded to receiptMaxStatLines; the block's summary tail
// is always kept (boundStatLines). The untracked entries — already
// .gitignore-filtered by `git status` itself, so cortex's own ignored
// runtime (its `.cortex/`, including the session transcripts) can never
// appear — are bounded to receiptMaxStatLines too; their names are appended
// after the stat block, each on its own line, so the block stays readable
// either way.
func (cs *CortexSession) receiptFilesChanged() []string {
	wd := cs.Workdir()
	if wd == "" {
		return nil
	}
	// The cheap, side-effect-free repository check: `git rev-parse
	// --is-inside-work-tree` answers true/false and does not spawn the diff.
	rev := exec.Command("git", "rev-parse", "--is-inside-work-tree")
	rev.Dir = wd
	out, err := rev.Output()
	if err != nil || strings.TrimSpace(string(out)) != "true" {
		return nil
	}
	stat := exec.Command("git", "diff", "--stat")
	stat.Dir = wd
	statOut, err := stat.Output()
	var lines []string
	if err == nil {
		if s := strings.TrimSpace(string(statOut)); s != "" {
			lines = append(lines, boundStatLines(strings.Split(s, "\n"))...)
		}
	}
	status := exec.Command("git", "status", "--porcelain")
	status.Dir = wd
	statusOut, err := status.Output()
	if err == nil {
		var untracked []string
		for _, l := range strings.Split(strings.TrimSpace(string(statusOut)), "\n") {
			if strings.HasPrefix(l, "?? ") {
				untracked = append(untracked, strings.TrimPrefix(l, "?? "))
			}
		}
		if len(untracked) > receiptMaxStatLines {
			untracked = untracked[:receiptMaxStatLines]
		}
		lines = append(lines, untracked...)
	}
	return lines
}

// boundStatLines bounds a git diff --stat block: a clean tree (an empty
// block) yields nil; a block of receiptMaxStatLines or fewer lines is kept
// whole; a larger block keeps its first (receiptMaxStatLines-1) per-file
// lines plus its summary tail (the final "N files changed, …" line — the
// fact's headline).
func boundStatLines(lines []string) []string {
	var nonEmpty []string
	for _, l := range lines {
		if strings.TrimSpace(l) != "" {
			nonEmpty = append(nonEmpty, l)
		}
	}
	if len(nonEmpty) == 0 {
		return nil
	}
	if len(nonEmpty) <= receiptMaxStatLines {
		return nonEmpty
	}
	// A fresh slice, not a sub-slice: the result must keep every kept line
	// — a sub-slice backed by the source (aliasing the per-file lines that
	// fall out of the window) can LOSE the summary tail when the caller's
	// append reuses that backing array.
	out := make([]string, 0, receiptMaxStatLines)
	out = append(out, nonEmpty[:receiptMaxStatLines-1]...)
	return append(out, nonEmpty[len(nonEmpty)-1])
}

// render emits the receipt's fixed plain-text form: one "files changed:"
// section (always rendered on a git workspace — even a clean tree, so the
// reader sees "nothing changed" rather than no section — the git diff
// --stat block plus the turn's created files, two-space indented), one
// "verification:" section (one line per verification run that RAN this turn:
// "<role>: <command> (exit N, Ss)"), one "checklist:" section (the #220
// step-2 fact — the task's checklist items the reply did not account for,
// one per line; rendered after verification and omitted when the task has no
// checklist or the reply met every item), and one "unformatted:" line (the
// files the format hook reported a problem with, "; "-joined). The order is
// fixed; a fact that measured nothing is omitted, and a receipt with no facts
// renders "" (a turn with nothing to measure has no receipt).
func (r turnReceipt) render() string {
	var b strings.Builder
	if r.gitWorkspace {
		b.WriteString("files changed:\n")
		for _, l := range r.filesChanged {
			b.WriteString("  " + l + "\n")
		}
	}
	if len(r.verification) > 0 {
		if b.Len() > 0 {
			b.WriteString("\n")
		}
		b.WriteString("verification:\n")
		shown := r.verification
		if len(shown) > receiptMaxVerificationLines {
			shown = shown[:receiptMaxVerificationLines]
		}
		for _, v := range shown {
			b.WriteString("  " + renderVerificationLine(v) + "\n")
		}
		if len(r.verification) > receiptMaxVerificationLines {
			b.WriteString("  … " + strconv.Itoa(len(r.verification)-receiptMaxVerificationLines) + " more\n")
		}
	}
	if len(r.checklistMissing) > 0 {
		if b.Len() > 0 {
			b.WriteString("\n")
		}
		b.WriteString("checklist (not accounted for in the reply):\n")
		for _, item := range r.checklistMissing {
			b.WriteString("  - " + item + "\n")
		}
	}
	if len(r.unformatted) > 0 {
		if b.Len() > 0 {
			b.WriteString("\n")
		}
		b.WriteString("unformatted: " + strings.Join(r.unformatted, "; ") + "\n")
	}
	return strings.TrimSuffix(b.String(), "\n")
}

// renderVerificationLine renders one verification run's receipt line: a
// run that RAN renders "test: go test ./... (exit 1, 0.4s)" — the role,
// the exact command, the REAL exit code the process returned (0 only when
// it actually exited 0), and the elapsed time of the run. A run the shell
// gate refused or the user declined renders "test: go test ./... (not run:
// …)" — v.notRun: a command that never ran has no exit code, and its
// result is unknown, not a pass.
func renderVerificationLine(v receiptVerification) string {
	if v.notRun != "" {
		return v.role + ": " + v.command + " (" + v.notRun + ")"
	}
	s := strconv.FormatFloat(v.elapsed.Seconds(), 'f', -1, 64)
	return v.role + ": " + v.command + " (exit " + strconv.Itoa(v.exitCode) + ", " + s + "s)"
}

// receiptDrop clears this turn's receipt state at the START of every turn
// (turn.go): the model-side bash recorder, the unformatted-file list, and
// the computed receipt itself all belong to exactly one turn — an error or
// interrupt path returns before the receipt is computed, and a stale
// recorder from that turn must not leak into the next one (testwatchDrop's
// lifecycle).
func (cs *CortexSession) receiptDrop() {
	cs.receiptModelBash = nil
	cs.receiptUnformatted = nil
	cs.receipt = turnReceipt{}
}
