// change_status_test.go — M5.2a: tests for changeStatusFor (change.go), the
// dir-scoped git-status seam the dashboard view-model (webui_dashboard.go)
// reuses. Deliberately a NEW file rather than an addition to the
// genesis-predating change_test.go: GOAL.md §2's standing regression guard
// bars modifying any pre-existing test file (mechanically checked via `git
// diff --name-only <genesis>..HEAD -- '*_test.go'`), so new coverage for
// change.go additions lands in its own file instead.
package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// initGitFixture creates a real git repo under a temp dir with one commit
// (a committed .gitignore, mirroring CLAUDE.md's ".cortex/ in .gitignore"
// invariant so fixture repos that also get session files written under
// .cortex/ don't register those as dirty untracked files), then checks out
// branch (deterministic regardless of the git installation's
// default-branch config, since we only branch off if not already there).
// When dirty is true, an untracked file is left behind so `git status
// --porcelain` reports something.
func initGitFixture(t *testing.T, branch string, dirty bool) string {
	t.Helper()
	dir := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	run("init", "-q")
	run("config", "user.email", "loop@example.com")
	run("config", "user.name", "loop")
	if err := os.WriteFile(filepath.Join(dir, ".gitignore"), []byte(".cortex/\n"), 0644); err != nil {
		t.Fatalf("write .gitignore: %v", err)
	}
	run("add", ".gitignore")
	run("commit", "-q", "-m", "init")
	// The machine's git may already default the initial branch to the
	// requested name (e.g. init.defaultBranch=main); only branch off if
	// we're not there already, so "-b <existing>" doesn't fail.
	cur := exec.Command("git", "rev-parse", "--abbrev-ref", "HEAD")
	cur.Dir = dir
	if out, err := cur.Output(); err != nil || strings.TrimSpace(string(out)) != branch {
		run("checkout", "-q", "-b", branch)
	}
	if dirty {
		if err := os.WriteFile(filepath.Join(dir, "untracked.txt"), []byte("x"), 0644); err != nil {
			t.Fatalf("write untracked file: %v", err)
		}
	}
	return dir
}

// changeStatusFor is the seam the M5.2 dashboard view-model reuses to show
// per-project change status without re-deriving it — it must report the
// exact same branch/active/clean facts `cortex change status` does, but
// scoped to an arbitrary directory instead of the process CWD.
func TestChangeStatusForReportsBranchActiveAndClean(t *testing.T) {
	tests := []struct {
		name       string
		branch     string
		dirty      bool
		wantActive bool
		wantClean  bool
	}{
		{"clean change branch", "cortex/fix-login", false, true, true},
		{"dirty change branch", "cortex/fix-login", true, true, false},
		{"clean non-change branch", "main", false, false, true},
		{"dirty non-change branch", "main", true, false, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := initGitFixture(t, tt.branch, tt.dirty)
			branch, active, clean, err := changeStatusFor(dir)
			if err != nil {
				t.Fatalf("changeStatusFor(%q): %v", dir, err)
			}
			if branch != tt.branch {
				t.Errorf("branch = %q, want %q", branch, tt.branch)
			}
			if active != tt.wantActive {
				t.Errorf("activeChange = %v, want %v", active, tt.wantActive)
			}
			if clean != tt.wantClean {
				t.Errorf("clean = %v, want %v", clean, tt.wantClean)
			}
		})
	}
}

// TestChangeStatusForNotAGitRepoReturnsError proves a non-repo directory
// (e.g. a project registered before it was ever git-initialized) surfaces a
// real error rather than a false "clean" verdict.
func TestChangeStatusForNotAGitRepoReturnsError(t *testing.T) {
	dir := t.TempDir()
	if _, _, _, err := changeStatusFor(dir); err == nil {
		t.Fatal("changeStatusFor on a non-git directory: got nil error, want one")
	}
}
