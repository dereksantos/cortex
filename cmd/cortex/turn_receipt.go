// turn_receipt.go is the measurement-only turn receipt (issue #219,
// step 1): after a turn that ran tools, the harness reads the workspace
// back and measures what the turn actually left behind, in three facts a
// small model is not reliably going to report about its own work:
//
//   - files changed: the workspace's own `git diff --stat` block, when the
//     turn's workspace (cs.Workdir()) is a git repository — the model's
//     claim of what it changed, measured against the repository instead of
//     its words;
//   - verification: the exit codes of the project's OWN test/build commands
//     (cs.projectCommands, discovered per internal/projectcmd), recorded
//     twice — by the per-turn bash recorder for every test/build command
//     the MODEL itself ran in a bash call, and by one final project-command
//     run the harness makes itself through the tools.hookRunner seam (the
//     same stubbed seam the per-edit hook and the turn-end lint pass run
//     through), so the verification fact exists even on a turn that edited
//     files but never ran the checks itself;
//   - unformatted: the files the post-edit format hook knows about — the
//     production hook call sites run through the per-turn wrapper
//     (FormatHook, the tools.FormatHookNoter capability) so its note is
//     recorded on the receipt while the model still sees the identical note.
//
// MEASUREMENT ONLY, by design: the receipt never blocks the turn, never
// fails a tool call, never adds a finalize round, and never changes what
// the model sees. It is computed at the end of turn.go, for every turn
// that ran tools (turnUsedTools), and rides TurnResult.Receipt for a
// caller to surface. Every measurement degrades to silence: a
// non-repository workspace reports no files-changed line, an absent
// test/build command contributes no verification run, a workspace the hook
// never ran in contributes no unformatted files, and a receipt with no
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
// the role (test|build), the exact command line, and the outcome the
// harness observed for it — elapsed (as reported by the runner) and
// timedOut (the run was cut off by its budget). A model-side run records
// whether the bash tool reported an error for the call (failed) instead of
// an exit code — the harness observed the tool's error, not a process exit.
type receiptVerification struct {
	role     string
	command  string
	failed   bool
	elapsed  time.Duration
	timedOut bool
}

// turnReceipt is the measurement-only receipt for one turn (issue #219):
// the three facts above, assembled by receipt() and rendered by render().
// All fields are empty on a turn that ran no tools or measured nothing —
// render() then returns "" and TurnResult.Receipt stays empty.
type turnReceipt struct {
	filesChanged []string // git diff --stat lines (per-file + summary tail)
	verification []receiptVerification
	unformatted  []string // paths the post-edit format hook knows about
}

// receiptBash records one test/build command the model is about to run in
// a bash call, for the receipt's verification fact. Called from
// coderDispatcher (loop.go) BEFORE the bash call runs, so the recorded
// command and its outcome (the bash tool's observed result, resolved by
// receiptBashOutcome) pair on the same call. Only commands that are a
// recognized run of the PROJECT's own test or build command are recorded
// — an exact match of the discovered command, or a prefix of it (the model
// ran the check with fewer flags, or `go test ./...` where discovery
// recorded `go test ./... -v`). Every other command (rm, ls, git, a foreign
// toolchain) is not this project's verification and contributes nothing.
// A session with no discovered test/build command records nothing.
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
// test or build command (exact, or a prefix of it), and which role it is.
func (cs *CortexSession) receiptBashRole(command string) (projectcmd.Role, bool) {
	fields := strings.Fields(command)
	for _, role := range []projectcmd.Role{projectcmd.RoleTest, projectcmd.RoleBuild} {
		cmd, ok := cs.projectCommands.Get(role)
		if !ok {
			continue
		}
		cmds := strings.Fields(strings.TrimSpace(cmd.Cmd))
		if len(cmds) == 0 || len(fields) < len(cmds) {
			continue
		}
		match := true
		for i := range cmds {
			if fields[i] != cmds[i] {
				match = false
				break
			}
		}
		if match {
			return role, true
		}
	}
	return "", false
}

// receiptModelBashRun is one model-side test/build run recorded before the
// bash call: the role it matched and the exact command the model ran. The
// outcome (error, elapsed) is filled in by receiptBashOutcome once the call
// has run.
type receiptModelBashRun struct {
	role     string
	command  string
	hadError bool
	elapsed  time.Duration
}

// receiptBashOutcome fills in the outcome of the last recorded model
// bash run for command: the bash tool's error (the call failed) and the
// wall time of the call. Called from coderDispatcher (loop.go) AFTER the
// bash call has run. A command receiptBash did not record (a
// non-verification command) is a no-op.
func (cs *CortexSession) receiptBashOutcome(command string, err error, elapsed time.Duration) {
	if len(cs.receiptModelBash) == 0 {
		return
	}
	last := &cs.receiptModelBash[len(cs.receiptModelBash)-1]
	if last.command != command {
		return
	}
	last.elapsed = elapsed
	last.hadError = err != nil
}

// receiptFinalVerification runs the project's OWN test/build commands
// ONCE, through the tools.hookRunner seam (the same stubbed seam the
// per-edit hook and the turn-end lint pass run through — tools.HookRunner
// is the runner the seam currently holds), and records their exit codes on
// the receipt. The harness makes this run itself — independent of whether
// the MODEL ran the checks — so a turn that edited files but never ran the
// tests still gets a verification fact: the exit code of the project's
// checks on the settled workspace. It is invoked at the end of turn.go
// (the clean-finalize point has already happened — every tool call, and
// any model fix-up round, has run), and NEVER fails the turn: every
// degradation (no command, a run error, a timeout) is folded into the
// recorded lines or silently skipped, the per-edit hook's contract.
//
// The run's budget is the hook's per-command budget (the same one the
// post-edit hook applies — a hung test suite must not stall the turn); an
// expired run is recorded as a timeout, not a pass.
func (cs *CortexSession) receiptFinalVerification(ctx context.Context) {
	wd := cs.Workdir()
	if wd == "" {
		return
	}
	run := tools.HookRunner()
	for _, role := range []projectcmd.Role{projectcmd.RoleTest, projectcmd.RoleBuild} {
		cmd, ok := cs.projectCommands.Get(role)
		if !ok {
			continue
		}
		cmdStr := strings.TrimSpace(cmd.Cmd)
		if cmdStr == "" || strings.ContainsAny(cmdStr, "|&;$<>\n") {
			// Shell-control characters need a shell; the hook seam runs
			// argv, not a shell — such a command is not expressible through
			// it, so the receipt simply has no run for that role.
			continue
		}
		argv := strings.Fields(cmdStr)
		runCtx, cancel := context.WithTimeout(ctx, cs.hookCommandBudget())
		timedOut := runVerification(run, runCtx, argv, wd)
		cancel()
		cs.receiptFinal = append(cs.receiptFinal, receiptVerification{
			role:     string(role),
			command:  cmdStr,
			timedOut: timedOut,
		})
	}
}

// hookCommandBudget is the per-command hook budget this session's hook
// runs under (project.command_timeout_sec via Config.toolLimits, 0 = the
// 10s default) — the receipt's final run is cut off at the same wall the
// post-edit hook is.
func (cs *CortexSession) hookCommandBudget() time.Duration {
	sec := cs.Config.toolLimits().HookCommandBudgetSec
	if sec <= 0 {
		sec = 10
	}
	return time.Duration(sec) * time.Second
}

// runVerification runs one final project-command verification through the
// hookRunner seam under the hook's per-command budget (the ctx carries the
// deadline) and reports whether the run was cut off by its budget's deadline
// (a timeout is recorded, not a pass). The elapsed time and the runner's
// output are the observation the receipt could fold in later — today the
// receipt records only the timeout mark. It never fails: a missing binary
// or a spawn error is simply an empty observation — the receipt is a
// measurement, not a retry mechanism.
func runVerification(run func(ctx context.Context, argv []string, dir string) (time.Duration, string, error), ctx context.Context, argv []string, dir string) bool {
	if _, _, runErr := run(ctx, argv, dir); runErr != nil {
		return ctx.Err() == context.DeadlineExceeded
	}
	return false
}

// receiptFormatHook is the session's per-turn wrapper around the post-edit
// format hook: it runs the hook exactly as the production write_file/
// edit_file path does (the same trust/mode/extension gates —
// runProjectCommandHook carries them) and records the hook's note on the
// receipt, so the receipt's unformatted fact is precisely "the files the
// post-edit hook knows about". The note is returned UNCHANGED, so the model
// sees exactly what the pre-receipt hook printed; a clean run's note still
// records the file (the hook knows about it — and reports it as clean —
// which is what the fact is, the hook's own knowledge, not the receipt's
// judgment). Measurement only: this wrapper never fails the edit.
//
// It is the tools.FormatHookNoter capability: the production hook call sites
// (internal/tools' write_file/edit_file and the in-place-rewrite hook) run
// through it when the session implements it, and run the hook directly
// otherwise (the model-facing note is byte-identical either way).
func (cs *CortexSession) FormatHook(ctx context.Context, fsPath string) string {
	note := tools.RunProjectCommandHook(ctx, cs.ProjectCommands(), cs.Workdir(), fsPath, cs.WorkspaceTrusted(), cs.HookState(), tools.EffectiveHookMode(cs.HookState()))
	if note != "" && fsPath != "" {
		cs.receiptUnformatted = append(cs.receiptUnformatted, fsPath)
	}
	return note
}

// computeReceipt computes this turn's receipt from the turn's
// measurements and stores it on the session (cs.receipt) so the turn
// boundary can surface it (turn.go). It assembles the files-changed fact
// (a fresh git diff --stat read), the verification fact (the model's
// recorded test/build runs plus the final project-command runs), and the
// unformatted fact (deduped, sorted). The final verification runs HERE,
// once, through the hookRunner seam — the receipt is computed at the end of
// turn.go, after the clean-finalize point, so the run sees the settled
// workspace.
func (cs *CortexSession) computeReceipt(ctx context.Context) turnReceipt {
	cs.receiptFinalVerification(ctx)
	r := turnReceipt{
		filesChanged: cs.receiptFilesChanged(),
		verification: append(append([]receiptVerification{}, cs.receiptModelVerifications()...), cs.receiptFinal...),
		unformatted:  cs.receiptUnformattedPaths(),
	}
	cs.receipt = r
	return r
}

// receiptModelVerifications turns the recorded model-side bash runs into
// the receipt's verification entries, in run order.
func (cs *CortexSession) receiptModelVerifications() []receiptVerification {
	var out []receiptVerification
	for _, r := range cs.receiptModelBash {
		out = append(out, receiptVerification{
			role:    r.role,
			command: r.command,
			failed:  r.hadError,
			elapsed: r.elapsed,
		})
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

// receiptFilesChanged reads the workspace's `git diff --stat` when the
// turn's workspace (cs.Workdir()) is a git repository. A non-repository,
// a missing git binary, or a failed invocation degrades to an empty fact —
// the receipt simply has no files-changed line. The per-file stat lines
// are bounded to receiptMaxStatLines; the block's summary tail is always
// kept (boundStatLines).
func (cs *CortexSession) receiptFilesChanged() []string {
	wd := cs.Workdir()
	if wd == "" {
		return nil
	}
	// The cheap, side-effect-free repository check: `git rev-parse
	// --is-inside-work-tree` answers true/false and does not spawn the diff.
	out, err := exec.Command("git", "rev-parse", "--is-inside-work-tree").Output()
	if err != nil || strings.TrimSpace(string(out)) != "true" {
		return nil
	}
	out, err = exec.Command("git", "diff", "--stat").Output()
	if err != nil {
		return nil
	}
	return boundStatLines(strings.Split(strings.TrimSpace(string(out)), "\n"))
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
	return append(nonEmpty[:receiptMaxStatLines-1], nonEmpty[len(nonEmpty)-1])
}

// render emits the receipt's fixed plain-text form: one "files changed:"
// section (the git diff --stat block, two-space indented), one
// "verification:" section (one line per verification run: "<role>:
// <command> (exit N, Ss)"), and one "unformatted:" line (the files the
// format hook knows about, "; "-joined). The order is fixed; a fact that
// measured nothing is omitted, and a receipt with no facts renders "" (a
// turn with nothing to measure has no receipt).
func (r turnReceipt) render() string {
	var b strings.Builder
	if len(r.filesChanged) > 0 {
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
// "test: go test ./... (exit 0, 0.4s)" — the role, the exact command, the
// outcome, and the elapsed time the runner reported. A model-side run
// records the bash tool's error as "exit 1" (a failed run) or "exit 0";
// a timed-out run is marked, not passed.
func renderVerificationLine(v receiptVerification) string {
	exit := 0
	if v.failed || v.timedOut {
		exit = 1
	}
	s := strconv.FormatFloat(v.elapsed.Seconds(), 'f', -1, 64)
	line := v.role + ": " + v.command + " (exit " + strconv.Itoa(exit) + ", " + s + "s)"
	if v.timedOut {
		line += " (timed out)"
	}
	return line
}

// receiptDrop clears this turn's receipt state at the START of every turn
// (turn.go): the model-side bash recorder, the final-verification list, the
// unformatted-file list, and the computed receipt itself all belong to
// exactly one turn — an error or interrupt path returns before the receipt
// is computed, and a stale recorder from that turn must not leak into the
// next one (testwatchDrop's lifecycle).
func (cs *CortexSession) receiptDrop() {
	cs.receiptModelBash = nil
	cs.receiptFinal = nil
	cs.receiptUnformatted = nil
	cs.receipt = turnReceipt{}
}
