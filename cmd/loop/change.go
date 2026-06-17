package main

import (
	"fmt"
	"os/exec"
	"strings"
)

// One change at a time. The persistent loop works on a single branch dedicated
// to the active change, so edits stay isolated and reviewable and the "one
// PR/change at a time" invariant holds. These are the harness-level git
// primitives an external driver runs around an agent turn; the agent still edits
// files through its own tools.
//
// Local only by design: nothing here pushes or opens a PR. Publishing a branch
// is outward-facing and needs explicit human consent, so it stays out of the
// automated path — a driver surfaces the branch name and a human (or a
// separately-consented step) does the push.

const changeBranchPrefix = "loop/"

// gitCmd runs git in the current working directory and returns trimmed combined
// output. On failure the error carries the command and git's own message, so a
// caller (or the model reading a tool result) can see exactly what went wrong.
func gitCmd(args ...string) (string, error) {
	out, err := exec.Command("git", args...).CombinedOutput()
	trimmed := strings.TrimSpace(string(out))
	if err != nil {
		return trimmed, fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, trimmed)
	}
	return trimmed, nil
}

// gitClean reports whether the worktree has no staged or unstaged changes.
func gitClean() (bool, error) {
	out, err := gitCmd("status", "--porcelain")
	if err != nil {
		return false, err
	}
	return out == "", nil
}

// currentBranch returns the checked-out branch name (or "HEAD" when detached).
func currentBranch() (string, error) {
	return gitCmd("rev-parse", "--abbrev-ref", "HEAD")
}

// onChangeBranch reports whether HEAD is one of the loop's change branches.
func onChangeBranch(branch string) bool {
	return strings.HasPrefix(branch, changeBranchPrefix)
}

// startChange creates and checks out loop/<slug> off the current HEAD. It
// refuses when the worktree is dirty: one change at a time means the previous
// change must be committed (or discarded) before the next one begins, so a
// half-finished change never bleeds into the new branch.
func startChange(name string) (string, error) {
	clean, err := gitClean()
	if err != nil {
		return "", err
	}
	if !clean {
		return "", fmt.Errorf("worktree has uncommitted changes — commit or discard the current change before starting a new one")
	}
	branch := changeBranchPrefix + slugifyChange(name)
	if _, err := gitCmd("checkout", "-b", branch); err != nil {
		return "", err
	}
	return branch, nil
}

// commitChange stages everything and commits on the active change branch. It
// requires being on a change branch (so an automated commit can't land on main
// or a feature branch by accident) and refuses an empty commit. Local only.
func commitChange(message string) (string, error) {
	branch, err := currentBranch()
	if err != nil {
		return "", err
	}
	if !onChangeBranch(branch) {
		return "", fmt.Errorf("not on a change branch (on %q) — run `loop change start <name>` first", branch)
	}
	clean, err := gitClean()
	if err != nil {
		return "", err
	}
	if clean {
		return "", fmt.Errorf("nothing to commit on %s", branch)
	}
	if _, err := gitCmd("add", "-A"); err != nil {
		return "", err
	}
	if _, err := gitCmd("commit", "-m", message); err != nil {
		return "", err
	}
	head, _ := gitCmd("rev-parse", "--short", "HEAD")
	return head, nil
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

// runChangeCLI implements `loop change <start|commit|status>` — the one-change-
// at-a-time git lifecycle a driver runs around an agent turn. All operations
// are local; the branch is left for a consented push step.
func runChangeCLI(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: loop change <start <name> | commit <message> | status>")
	}
	switch args[0] {
	case "start":
		name := strings.TrimSpace(strings.Join(args[1:], " "))
		if name == "" {
			return fmt.Errorf("usage: loop change start <name>")
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
			return fmt.Errorf("usage: loop change commit <message>")
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
