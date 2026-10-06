// turn_receipt.go is the measurement-only turn receipt (issue #219): after a
// turn that ran tools, the harness reads the workspace back and measures what
// the turn actually left behind, in three facts a small model is not reliably
// going to report about its own work:
//
//   - files changed: the workspace's own `git diff --stat` block PLUS the
//     untracked files the turn created (a diff alone misses a file a turn
//     only wrote — it is not tracked yet), measured in the session's
//     workspace (both git commands run with cmd.Dir = cs.Workdir()), so an
//     explicit-root workspace (serve, --project, tests) measures ITS repo,
//     not wherever the process started;
//   - verification: the exit codes of the project's OWN test/build commands
//     that RAN this turn — the model's own runs, recorded by the per-turn
//     bash recorder (the bash tool reports a non-zero exit in its result
//     text, so the exit code is parsed from the result, not from a shadowed
//     tool error). The harness does NOT run the project's test/build
//     commands itself: running project-declared commands is a trust-gated
//     decision (issue #129's documented rule — trust is the only gate), and
//     a measurement path that ran them on every turn, untrusted workspaces
//     included, broke that rule and added up to 20s of latency to every
//     chat turn. A turn that ran no verification has no verification fact;
//   - unformatted: the files the post-edit format hook knows about — the
//     production hook call sites run through the per-turn wrapper
//     (FormatHook, the tools.FormatHookNoter capability) so the files the
//     hook reported a problem with are recorded on the receipt while the
//     model still sees the identical note. A clean run (the hook formatted
//     the file without a problem note) and the one-time "hook inactive:
//     workspace not trusted" note do NOT record a file — the label says the
//     formatter failed or left changes, so a clean file is not one.
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
// returned (0 on success; 1 when it was cut off by a deadline or failed to
// run), and the elapsed time of the run. Both facts are always rendered —
// an unmeasured exit code or elapsed time would be a measurement gap, not
// an absence of the fact.
type receiptVerification struct {
	role     string
	command  string
	exitCode int
	elapsed  time.Duration
}

// turnReceipt is the measurement-only receipt for one turn (issue #219):
// the three facts above, assembled by computeReceipt and rendered by
// render(). All fields are empty on a turn that ran no tools or measured
// nothing — render() then returns "" and TurnResult.Receipt stays empty.
type turnReceipt struct {
	filesChanged       []string // git diff --stat lines + the untracked files the turn created
	gitWorkspace       bool     // true when the turn's workspace is a git repository (a clean tree still renders the files-changed section: "nothing changed")
	verification       []receiptVerification
	unformatted        []string // files the post-edit format hook reported a problem with
	hadReceiptBashRuns bool     // true when the bash recorder saw a verification run this turn
}

// receiptBash records one test/build command the model is about to run in
// a bash call, for the receipt's verification fact. Called from
// coderDispatcher (loop.go) BEFORE the bash call runs, so the recorded
// command and its outcome (the bash tool's observed result, resolved by
// receiptBashOutcome) pair on the same call. Only commands that are a
// recognized run of the PROJECT's own test or build command are recorded
// (receiptBashRole: an exact match of the discovered command, or a prefix
// of it in either direction — the model ran the check with fewer flags, or
// bare). Every other command (rm, ls, git, a foreign toolchain, even `go
// vet` when the project declared `go test ./...`) is not this project's
// verification and contributes nothing. A session with no discovered
// test/build command records nothing.
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
// test or build command (an exact match or a prefix of it in EITHER DIRECTION),
// and which role it is. The match is prefix-of in either direction because
// neither side is a reliable superset: discovery's recorded command may carry
// flags the model omitted (discovery `go test ./... -v`, model ran `go test
// ./...` — the model's fields prefix discovery's), or the model may run the
// check BARE (discovery `go test ./...`, model ran `go test` — discovery's
// fields prefix the model's); a prefix in the other direction still names the
// same project check. Only the PROJECT's own commands count — an exact match
// of the first field is NOT enough, so `go vet` (same toolchain, a DIFFERENT
// check the project never declared) is not this project's verification.
// A session with no discovered test/build command records nothing.
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
		if sharedPrefix(cmds, fields) || sharedPrefix(fields, cmds) {
			return role, true
		}
	}
	return "", false
}

// sharedPrefix reports whether a is a PREFIX of b (every field of a equals
// the corresponding field of b; a longer than b is never a prefix of it).
func sharedPrefix(a, b []string) bool {
	if len(a) > len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// receiptModelBashRun is one model-side test/build run recorded before the
// bash call: the role it matched and the exact command the model ran. The
// outcome (exit code, elapsed) is filled in by receiptBashOutcome once the
// call has run.
type receiptModelBashRun struct {
	role     string
	command  string
	exitCode int
	elapsed  time.Duration
}

// receiptBashOutcome fills in the outcome of the last recorded model
// bash run for command: the exit code the bash tool observed and the wall
// time of the call. The bash tool reports a non-zero exit in its RESULT
// TEXT (the "[exit error: exit status N]" marker), not as the call's error
// — a failed test run returns cleanly with the marker in the output — so
// the exit code is parsed from resultText (parseBashExitCode); a tool-level
// error (callErr, e.g. a rejected command) means the command never ran and
// is recorded as a non-zero exit. Called from coderDispatcher (loop.go)
// AFTER the bash call has run. A command receiptBash did not record (a
// non-verification command) is a no-op.
func (cs *CortexSession) receiptBashOutcome(command, resultText string, callErr error, elapsed time.Duration) {
	if len(cs.receiptModelBash) == 0 {
		return
	}
	last := &cs.receiptModelBash[len(cs.receiptModelBash)-1]
	if last.command != command {
		return
	}
	last.elapsed = elapsed
	if callErr != nil {
		last.exitCode = 1
		return
	}
	if code, ok := parseBashExitCode(resultText); ok {
		last.exitCode = code
	}
	// no marker: the run exited 0
}

// parseBashExitCode extracts the exit code the bash tool observed from its
// result text. The tool reports a non-zero exit as a trailing
// "[exit error: <error>]" line, where <error> is the run error's Error()
// text: exec.ExitError renders as "exit status N" (the number is the
// process's real exit code), but a run KILLED BY A SIGNAL renders as
// "signal: killed" (or "signal: SIGKILL") and carries no number. A result
// without the marker ran successfully (exit 0). ok is false when no marker
// is present. A marker that names no exit status — a signal-killed run, or
// a bare marker — is a real failure reported as exit 1: the receipt's job
// is to never report a failed run as exit 0, and a signal-killed check did
// not exit 0.
func parseBashExitCode(resultText string) (code int, ok bool) {
	const marker = "[exit error: "
	i := strings.LastIndex(resultText, marker)
	if i < 0 {
		return 0, false
	}
	rest := resultText[i+len(marker):]
	// A trailing "]" (the marker's close, possibly carrying a trailing
	// newline) is not part of the exit status.
	if j := strings.IndexByte(rest, ']'); j >= 0 {
		rest = rest[:j]
	}
	rest = strings.TrimSpace(rest)
	const status = "exit status "
	if !strings.HasPrefix(rest, status) {
		return 1, true // no number: a signal-killed run or a bare marker — a failure
	}
	n, err := strconv.Atoi(strings.TrimSpace(rest[len(status):]))
	if err != nil {
		return 1, true
	}
	return n, true
}

// receiptFormatHook is the session's per-turn wrapper around the post-edit
// format hook: it runs the hook exactly as the production write_file/
// edit_file path does (the same trust/mode/extension gates —
// runProjectCommandHook carries them; the hookSkip flag is the per-call
// `hook: "skip"` opt-out, which must lower the effective mode the way the
// direct path does) and records the hook's OUTCOME on the receipt. A file
// is recorded only when the hook reported that the formatter FAILED or LEFT
// CHANGES (hookOutcomeProblem, below) — a clean run (the hook formatted the
// file; its note, if any, is informational) and the one-time "hook inactive:
// workspace not trusted" note do not record the file, so the receipt's
// "unformatted:" line names exactly the files the hook says are not clean.
// The note is returned UNCHANGED, so the model sees exactly what the
// pre-receipt hook printed. Measurement only: this wrapper never fails the
// edit.
//
// It is the tools.FormatHookNoter capability: the production hook call sites
// (internal/tools' write_file/edit_file and the in-place-rewrite hook) run
// through it when the session implements it, and run the hook directly
// otherwise (the model-facing note is byte-identical either way).
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
	note := tools.RunProjectCommandHook(ctx, cs.ProjectCommands(), cs.Workdir(), fsPath, cs.WorkspaceTrusted(), cs.HookState(), mode)
	if note != "" && fsPath != "" && hookOutcomeProblem(note) {
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

// hookOutcomeProblem reports whether a format hook's note records a file
// the receipt should name: the formatter FAILED ("note: … ran with an
// error …", "note: … could not run …", "note: … timed out …") or LEFT
// CHANGES ("note: formatted … with the project format command …" — the
// hook rewrote the file, so the content as written was not clean). The
// only note that does NOT record the file is the one-time "post-edit hook
// inactive: …" note — the hook did not run at all, so the file is
// untouched, not unformatted.
func hookOutcomeProblem(note string) bool {
	note = strings.TrimSpace(note)
	return note != tools.HookInactiveNote
}

// computeReceipt computes this turn's receipt from the turn's
// measurements and stores it on the session (cs.receipt) so the turn
// boundary can surface it (turn.go). It assembles the files-changed fact
// (a fresh git read), the verification fact (the model's recorded test/build
// runs), and the unformatted fact (deduped, sorted). The harness runs no
// project commands of its own for the receipt — the verification fact is
// precisely "the verification commands that RAN this turn" (the model's
// bash runs, recorded by the bash recorder), and nothing else.
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
// the receipt's verification entries, in run order. The two structs are
// field-identical (receiptModelBashRun is the recorder's form,
// receiptVerification the receipt's rendered form), so the conversion is
// the same as copying field by field.
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
// is always kept (boundStatLines). The untracked entries are bounded to
// receiptMaxStatLines too; their names are appended after the stat block,
// each on its own line, so the block stays readable either way.
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
// "<role>: <command> (exit N, Ss)"), and one "unformatted:" line (the files
// the format hook reported a problem with, "; "-joined). The order is fixed;
// a fact that measured nothing is omitted, and a receipt with no facts
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
	if len(r.unformatted) > 0 {
		if b.Len() > 0 {
			b.WriteString("\n")
		}
		b.WriteString("unformatted: " + strings.Join(r.unformatted, "; ") + "\n")
	}
	return strings.TrimSuffix(b.String(), "\n")
}

// renderVerificationLine renders one verification run's receipt line:
// "test: go test ./... (exit 1, 0.4s)" — the role, the exact command, the
// REAL exit code the process returned (0 only when it actually exited 0),
// and the elapsed time of the run.
func renderVerificationLine(v receiptVerification) string {
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
