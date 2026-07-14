package main

import (
	"fmt"
	"os/exec"
	"strings"
)

// One change at a time. The persistent Cortex process works on a single branch dedicated
// to the active change, so edits stay isolated and reviewable and the "one
// PR/change at a time" invariant holds. These are the harness-level git
// primitives an external driver runs around an agent turn; the agent still edits
// files through its own tools.
//
// Local only by design: nothing here pushes or opens a PR. Publishing a branch
// is outward-facing and needs explicit human consent, so it stays out of the
// automated path — a driver surfaces the branch name and a human (or a
// separately-consented step) does the push.

const changeBranchPrefix = "cortex/"

// gitCmdIn runs git in the given directory (an empty dir defaults to the
// current process's working directory) and returns trimmed combined
// output. On failure the error carries the command and git's own message,
// so a caller (or the model reading a tool result) can see exactly what
// went wrong. Every change-lifecycle helper below (gitCleanIn,
// currentBranchIn, startChangeIn, commitChangeIn) goes through this one
// entry point — a caller with a project root that isn't the process CWD
// (M5.2's dashboard view-model, M6.3's per-project loop firing) reuses the
// exact same git plumbing instead of a parallel implementation; the
// zero-arg CWD-implicit wrappers (gitClean, currentBranch, startChange,
// commitChange — `cortex change`'s own CLI) pass "" through.
func gitCmdIn(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	trimmed := strings.TrimSpace(string(out))
	if err != nil {
		return trimmed, fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, trimmed)
	}
	return trimmed, nil
}

// gitCleanIn reports whether dir's worktree has no staged or unstaged
// changes. An empty dir defaults to the process's working directory
// (matching gitCmdIn's own convention).
func gitCleanIn(dir string) (bool, error) {
	out, err := gitCmdIn(dir, "status", "--porcelain")
	if err != nil {
		return false, err
	}
	return out == "", nil
}

// gitClean reports whether the worktree has no staged or unstaged changes.
func gitClean() (bool, error) {
	return gitCleanIn("")
}

// currentBranchIn returns dir's checked-out branch name (or "HEAD" when
// detached). An empty dir defaults to the process's working directory.
func currentBranchIn(dir string) (string, error) {
	return gitCmdIn(dir, "rev-parse", "--abbrev-ref", "HEAD")
}

// currentBranch returns the checked-out branch name (or "HEAD" when detached).
func currentBranch() (string, error) {
	return currentBranchIn("")
}

// onChangeBranch reports whether HEAD is one of the Cortex's change branches.
func onChangeBranch(branch string) bool {
	return strings.HasPrefix(branch, changeBranchPrefix)
}

// changeStatusFor reports the same three facts `cortex change status`
// prints (branch, active-change, clean), but scoped to an arbitrary
// project root rather than the process's CWD — the seam the M5.2 dashboard
// view-model reuses (GOAL.md §3 pillar 3: reuse the seam) instead of
// re-deriving change status through a parallel mechanism. Returns whatever
// partial results were obtained alongside the first error encountered, so a
// caller can still render a branch name even if the porcelain-status call
// fails.
func changeStatusFor(dir string) (branch string, activeChange bool, clean bool, err error) {
	branch, err = gitCmdIn(dir, "rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		return "", false, false, err
	}
	activeChange = onChangeBranch(branch)
	out, err := gitCmdIn(dir, "status", "--porcelain")
	if err != nil {
		return branch, activeChange, false, err
	}
	return branch, activeChange, out == "", nil
}

// startChangeIn creates and checks out cortex/<slug> off dir's current HEAD.
// It refuses when dir's worktree is dirty: one change at a time means the
// previous change must be committed (or discarded) before the next one
// begins, so a half-finished change never bleeds into the new branch. An
// empty dir defaults to the process's working directory — the seam M6.3's
// per-project loop firing (loop_run.go) reuses to scope change creation to
// the target project's root instead of the process CWD (GOAL.md §3 pillar
// 3: reuse the seam over inventing a parallel per-project git mechanism).
func startChangeIn(dir, name string) (string, error) {
	clean, err := gitCleanIn(dir)
	if err != nil {
		return "", err
	}
	if !clean {
		return "", fmt.Errorf("worktree has uncommitted changes — commit or discard the current change before starting a new one")
	}
	branch := changeBranchPrefix + slugifyChange(name)
	if _, err := gitCmdIn(dir, "checkout", "-b", branch); err != nil {
		return "", err
	}
	return branch, nil
}

// startChange creates and checks out cortex/<slug> off the current HEAD. It
// refuses when the worktree is dirty: one change at a time means the previous
// change must be committed (or discarded) before the next one begins, so a
// half-finished change never bleeds into the new branch.
func startChange(name string) (string, error) {
	return startChangeIn("", name)
}

// commitChangeIn stages everything and commits on dir's active change
// branch. It requires being on a change branch (so an automated commit
// can't land on main or a feature branch by accident) and refuses an empty
// commit. Local only. An empty dir defaults to the process's working
// directory.
func commitChangeIn(dir, message string) (string, error) {
	branch, err := currentBranchIn(dir)
	if err != nil {
		return "", err
	}
	if !onChangeBranch(branch) {
		return "", fmt.Errorf("not on a change branch (on %q) — run `cortex change start <name>` first", branch)
	}
	clean, err := gitCleanIn(dir)
	if err != nil {
		return "", err
	}
	if clean {
		return "", fmt.Errorf("nothing to commit on %s", branch)
	}
	if _, err := gitCmdIn(dir, "add", "-A"); err != nil {
		return "", err
	}
	if _, err := gitCmdIn(dir, "commit", "-m", message); err != nil {
		return "", err
	}
	head, _ := gitCmdIn(dir, "rev-parse", "--short", "HEAD")
	return head, nil
}

// commitChange stages everything and commits on the active change branch. It
// requires being on a change branch (so an automated commit can't land on main
// or a feature branch by accident) and refuses an empty commit. Local only.
func commitChange(message string) (string, error) {
	return commitChangeIn("", message)
}

// slugifyChange turns a free-text change name into a safe branch suffix:
// lowercase, alphanumerics kept, every other run collapsed to a single dash,
// no leading/trailing dash. Empty input falls back to "change" so a branch name
// always forms.
func slugifyChange(name string) string {
	var b strings.Builder
	prevDash := false
	for _, r := range strings.ToLower(strings.TrimSpace(name)) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			prevDash = false
		default:
			if b.Len() > 0 && !prevDash {
				b.WriteByte('-')
				prevDash = true
			}
		}
	}
	s := strings.Trim(b.String(), "-")
	if s == "" {
		return "change"
	}
	return s
}

// runChangeCLI implements `cortex change <start|commit|status>` — the one-change-
// at-a-time git lifecycle a driver runs around an agent turn. All operations
// are local; the branch is left for a consented push step.
func runChangeCLI(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: cortex change <start <name> | commit <message> | status>")
	}
	switch args[0] {
	case "start":
		name := strings.TrimSpace(strings.Join(args[1:], " "))
		if name == "" {
			return fmt.Errorf("usage: cortex change start <name>")
		}
		branch, err := startChange(name)
		if err != nil {
			return err
		}
		fmt.Printf("started change on %s\n", branch)
		return nil
	case "commit":
		message := strings.TrimSpace(strings.Join(args[1:], " "))
		if message == "" {
			return fmt.Errorf("usage: cortex change commit <message>")
		}
		head, err := commitChange(message)
		if err != nil {
			return err
		}
		branch, _ := currentBranch()
		fmt.Printf("committed %s on %s (not pushed)\n", head, branch)
		return nil
	case "status":
		branch, err := currentBranch()
		if err != nil {
			return err
		}
		clean, err := gitClean()
		if err != nil {
			return err
		}
		state := "uncommitted changes"
		if clean {
			state = "clean"
		}
		active := "no active change"
		if onChangeBranch(branch) {
			active = "active change"
		}
		fmt.Printf("%s — %s — %s\n", branch, active, state)
		return nil
	default:
		return fmt.Errorf("unknown change subcommand %q (want start|commit|status)", args[0])
	}
}
