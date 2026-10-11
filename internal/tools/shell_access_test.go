package tools

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// shell_access_test.go pins the default stance on shell file access
// (tools.steer_shell_reads absent/false): shell reads carry no note, a
// shell-created SCRATCH file (outside the workspace, or not tracked by git)
// carries no note, and a shell rewrite of a TRACKED file — plus every
// in-place form — keeps the edit_file/write_file steering. The
// steer_shell_reads=true behavior (the previous notes, verbatim) is pinned
// by TestInPlaceRewriteNote, TestBashReadNoteEndToEnd, and
// TestBashTeeNoteEndToEnd.

const (
	rewriteNoteMarker = "rewrites file(s) in place"
	readNoteMarker    = "reads file(s) via the shell"
)

// newTrackedRepo makes a git work tree with two tracked files (tracked.py and
// sub/tracked.txt, added to the index) and returns its root.
func newTrackedRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
		}
	}
	run("init", "-q")
	if err := os.MkdirAll(filepath.Join(dir, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{
		"tracked.py":      "x = 1\nprint(x)\n",
		"sub/tracked.txt": "x\n",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	run("add", "tracked.py", "sub/tracked.txt")
	return dir
}

// TestInPlaceRewriteNote_Default pins the note inPlaceRewriteNote renders by
// default, in a git work tree (and, for the conservative fallback, a plain
// directory), plus the steer=true contrast for the same commands. The
// commands are parsed, never executed.
func TestInPlaceRewriteNote_Default(t *testing.T) {
	repo := newTrackedRepo(t)
	plain := t.TempDir()
	cases := []struct {
		name     string
		wd       string
		steer    bool
		cmd      string
		wantNote bool
		wantSubs []string // substrings the note must contain (when wantNote)
	}{
		// Shell reads: no note at all.
		{"cat is fine", repo, false, `cat tracked.py`, false, nil},
		{"sed -n range is fine", repo, false, `sed -n '1,20p' tracked.py`, false, nil},
		{"grep -n is fine", repo, false, `grep -n x tracked.py`, false, nil},
		{"head | tail is fine", repo, false, `head tracked.py | tail -n 5`, false, nil},
		{"tac / cat -A are fine", repo, false, `tac tracked.py && cat -A sub/tracked.txt`, false, nil},
		// Scratch creates: no note.
		{"heredoc to /tmp is scratch", repo, false, "cat > /tmp/repro.py <<'EOF'\nprint(1)\nEOF", false, nil},
		{"tee to /tmp is scratch", repo, false, `echo hi | tee /tmp/out.txt`, false, nil},
		{"heredoc to an untracked workspace file is scratch", repo, false, "cat > new_repro.py <<'EOF'\nprint(1)\nEOF", false, nil},
		{"copy into an untracked workspace file is scratch", repo, false, `cat tracked.py > copy.py`, false, nil},
		{"heredoc outside a git repo to /tmp is scratch", plain, false, "cat > /tmp/repro.py <<'EOF'\nprint(1)\nEOF", false, nil},
		// Tracked targets: steered, exactly as before.
		{"heredoc onto a tracked file is steered", repo, false, "cat > tracked.py <<'EOF'\nprint(2)\nEOF", true,
			[]string{rewriteNoteMarker, "tracked.py", "edit_file", "write_file"}},
		{"absolute heredoc onto a tracked file is steered", repo, false, "cat > " + filepath.Join(repo, "tracked.py") + " <<'EOF'\nprint(2)\nEOF", true,
			[]string{rewriteNoteMarker, "tracked.py"}},
		{"append onto a tracked file is steered", repo, false, `echo y >> sub/tracked.txt`, true,
			[]string{rewriteNoteMarker, "sub/tracked.txt"}},
		{"tee onto a tracked file is steered", repo, false, `echo y | tee tracked.py`, true,
			[]string{rewriteNoteMarker, "tracked.py"}},
		{"sed -i on a tracked file is steered", repo, false, `sed -i 's/x/y/' tracked.py`, true,
			[]string{rewriteNoteMarker, "tracked.py", "edit_file"}},
		{"in-place forms stay steered even off-workspace", repo, false, `sed -i 's/x/y/' /tmp/scratch.py`, true,
			[]string{rewriteNoteMarker, "/tmp/scratch.py"}},
		{"scratch create + tracked rewrite names only the tracked file", repo, false, `echo a > /tmp/a.txt && sed -i 's/x/y/' tracked.py`, true,
			[]string{rewriteNoteMarker, "tracked.py"}},
		// Not a git repo: in-workspace counts as tracked (conservative).
		{"outside a git repo an in-workspace create is steered", plain, false, "cat > new.py <<'EOF'\nprint(1)\nEOF", true,
			[]string{rewriteNoteMarker, "new.py"}},
		// steer_shell_reads=true: the previous notes return.
		{"steer: cat gets the read note", repo, true, `cat tracked.py`, true,
			[]string{readNoteMarker, "tracked.py", "read_file", "outline/grep"}},
		{"steer: sed -n gets the read note", repo, true, `sed -n '1,20p' tracked.py`, true,
			[]string{readNoteMarker, "read_file"}},
		{"steer: heredoc to /tmp gets the create note", repo, true, "cat > /tmp/repro.py <<'EOF'\nprint(1)\nEOF", true,
			[]string{rewriteNoteMarker, "/tmp/repro.py", "write_file"}},
		{"steer: untracked workspace create gets the create note", repo, true, "cat > new_repro.py <<'EOF'\nprint(1)\nEOF", true,
			[]string{rewriteNoteMarker, "new_repro.py"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.steer {
				withSteerShellReads(t)
			}
			got := inPlaceRewriteNote(wdDeps{wd: tc.wd}, tc.cmd)
			if !tc.wantNote {
				if got != "" {
					t.Fatalf("note = %q, want none (cmd %q)", got, tc.cmd)
				}
				return
			}
			if got == "" {
				t.Fatalf("note empty, want a note (cmd %q)", tc.cmd)
			}
			for _, sub := range tc.wantSubs {
				if !strings.Contains(got, sub) {
					t.Errorf("note %q should contain %q (cmd %q)", got, sub, tc.cmd)
				}
			}
			if tc.name == "scratch create + tracked rewrite names only the tracked file" && strings.Contains(got, "/tmp/a.txt") {
				t.Errorf("scratch target must not be named, got %q", got)
			}
			if !tc.steer && strings.Contains(got, readNoteMarker) {
				t.Errorf("default mode must never carry the read note, got %q", got)
			}
		})
	}
}

// TestBashShellAccessDefaultEndToEnd pins the same stance through the bash
// tool itself (Execute): the command really runs in a git work tree and the
// result carries a steering note only for a tracked-file rewrite. Executed
// forms stay GNU/BSD portable (sed -i.bak; see shell_portability_test.go).
func TestBashShellAccessDefaultEndToEnd(t *testing.T) {
	scratchDir := t.TempDir() // outside every per-case workspace
	scratch := filepath.Join(scratchDir, "repro.py")
	cases := []struct {
		name     string
		steer    bool
		cmd      string
		wantSubs []string // nil: no note of either kind
	}{
		{"cat: no note", false, `cat tracked.py`, nil},
		{"sed -n: no note", false, `sed -n '1,2p' tracked.py`, nil},
		{"grep -n: no note", false, `grep -n x tracked.py`, nil},
		{"head | tail: no note", false, `head tracked.py | tail -n 1`, nil},
		{"heredoc to a scratch dir: no note", false, "cat > " + scratch + " <<'EOF'\nprint(1)\nEOF", nil},
		{"heredoc to an untracked workspace file: no note", false, "cat > new_repro.py <<'EOF'\nprint(1)\nEOF", nil},
		{"heredoc onto a tracked file: steered", false, "cat > tracked.py <<'EOF'\nprint(2)\nEOF",
			[]string{rewriteNoteMarker, "tracked.py", "edit_file"}},
		{"sed -i on a tracked file: steered", false, `sed -i.bak 's/x/y/' tracked.py`,
			[]string{rewriteNoteMarker, "tracked.py", "edit_file"}},
		{"steer: cat gets the read note", true, `cat tracked.py`,
			[]string{readNoteMarker, "tracked.py", "read_file"}},
		{"steer: heredoc to a scratch dir gets the create note", true, "cat > " + scratch + " <<'EOF'\nprint(1)\nEOF",
			[]string{rewriteNoteMarker, scratch, "write_file"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.steer {
				withSteerShellReads(t)
			}
			wd := newTrackedRepo(t)
			got, _, err := Execute(context.Background(), bashCall(t, tc.cmd), wdNoteDeps{wdDeps{wd: wd}})
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if tc.wantSubs == nil {
				if strings.Contains(got, rewriteNoteMarker) || strings.Contains(got, readNoteMarker) {
					t.Errorf("command %q should carry no note; got %q", tc.cmd, got)
				}
				return
			}
			for _, sub := range tc.wantSubs {
				if !strings.Contains(got, sub) {
					t.Errorf("result %q should contain %q", got, sub)
				}
			}
		})
	}
}
