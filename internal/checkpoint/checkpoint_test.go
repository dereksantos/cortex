package checkpoint

import (
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// These tests drive the REAL git binary against a temp repo — the checkpoint
// invariants (index untouched, stash list untouched, .cortex/ untouched,
// restore round-trip) are only meaningful against real git, not a stub.

// initRepo builds a git repo in dir with a committed base (a.txt, b.txt) plus
// a pre-existing untracked user file and a .cortex/ file, the exact starting
// state the issue's acceptance criteria assume: committed work, uncommitted
// untracked user work, and the gitignored .cortex/ dir.
func initRepo(t *testing.T) (dir string) {
	t.Helper()
	dir = t.TempDir()
	git(t, dir, "init", "-q")
	git(t, dir, "config", "user.name", "checkpoint-test")
	git(t, dir, "config", "user.email", "checkpoint-test@example.com")
	// Ensure core.fileMode is on so the exec bit is tracked and restored.
	git(t, dir, "config", "core.fileMode", "true")
	// Committed base.
	mustWrite(t, dir, "a.txt", "v1\n")
	mustWrite(t, dir, "b.txt", "keep\n")
	git(t, dir, "add", "a.txt", "b.txt")
	git(t, dir, "commit", "-q", "-m", "base")
	// Pre-existing untracked user file — must survive a Restore untouched.
	mustWrite(t, dir, "user_untracked.txt", "user work\n")
	// The gitignored .cortex/ dir — never in a snapshot, never touched.
	if err := os.MkdirAll(filepath.Join(dir, ".cortex"), 0o755); err != nil {
		t.Fatalf("mkdir .cortex: %v", err)
	}
	// The self-gitignore file (issue #119): makes .cortex/ ignored, so
	// `git ls-files --others --exclude-standard` never lists it.
	mustWrite(t, dir, ".cortex/.gitignore", "*")
	mustWrite(t, dir, filepath.Join(".cortex", "session.json"), "{}\n")
	return dir
}

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s in %s: %v\n%s", strings.Join(args, " "), dir, err, out)
	}
	return strings.TrimSpace(string(out))
}

func mustWrite(t *testing.T, dir, path, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, filepath.FromSlash(path)), []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func readOr(t *testing.T, dir, path string) (string, bool) {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(path)))
	if err != nil {
		return "", false
	}
	return string(b), true
}

func indexPathsOf(t *testing.T, dir string) []string {
	t.Helper()
	out := git(t, dir, "ls-files", "--stage")
	var paths []string
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimRight(line, "\r")
		if line == "" {
			continue
		}
		_, p, ok := strings.Cut(line, "\t")
		if ok && p != "" {
			paths = append(paths, p)
		}
	}
	sort.Strings(paths)
	return paths
}

func TestAvailable(t *testing.T) {
	dir := t.TempDir() // not a git repo
	if Available(dir) {
		t.Error("Available(true) for a non-git dir, want false")
	}
	repo := initRepo(t)
	if !Available(repo) {
		t.Error("Available(false) for a git repo, want true")
	}
}

func TestRefFor(t *testing.T) {
	tests := []struct {
		name    string
		session string
		turn    string
		want    string
	}{
		{"plain", "sess1", "0001", RefPrefix + "sess1/0001"},
		// `.` and `/` are not in [A-Za-z0-9._-]? `.` IS. So "../evil" keeps the
		// dots but drops the slashes → "..evil", then Trim removes leading dots
		// → "evil". The turn "2" is a digit → "2".
		{"sanitize slashes", "../evil", "2", RefPrefix + "evil/2"},
		// dots are kept; only leading/trailing dots are trimmed.
		{"sanitize dots", "a..b", "3", RefPrefix + "a..b/3"},
		{"empty session", "", "4", RefPrefix + "x/4"},
		// spaces and specials dropped, dots/underscores kept.
		{"special chars", "s p!@#", "t1", RefPrefix + "sp/t1"},
		{"dots only", "...", "5", RefPrefix + "x/5"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := RefFor(tt.session, tt.turn); got != tt.want {
				t.Errorf("RefFor(%q,%q) = %q, want %q", tt.session, tt.turn, got, tt.want)
			}
		})
	}
}

// TestSnapshotCleanTreeFallsBackToHEAD is the "clean tracked tree" clause:
// `git stash create` prints nothing there, so Snapshot records HEAD's commit
// (the committed base IS the tree) and still returns the untracked baseline.
// The CALLER decides whether the turn mutated files (at turn end) — that is
// no longer the snapshot's job.
func TestSnapshotCleanTreeFallsBackToHEAD(t *testing.T) {
	dir := initRepo(t)
	ref := RefFor("s1", "0001")
	snap, untracked, err := Snapshot(dir, ref)
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if snap == "" {
		t.Fatal("Snapshot on clean tree is empty; want HEAD's commit (the tree to restore to)")
	}
	head, err := gitOut2(t, dir, "rev-parse", "HEAD")
	if err != nil {
		t.Fatalf("rev-parse HEAD: %v", err)
	}
	if snap != head {
		t.Errorf("Snapshot on clean tree = %q, want HEAD %q", snap, head)
	}
	// A ref must have been recorded.
	refs := git(t, dir, "for-each-ref", "--format=%(refname)", RefPrefix)
	if refs != ref {
		t.Errorf("ref not recorded on clean tree: %q", refs)
	}
	// The untracked baseline is the pre-existing user file — .cortex/ is
	// gitignored and never listed.
	wantUntracked := []string{"user_untracked.txt"}
	if !sameStrings(untracked, wantUntracked) {
		t.Errorf("untracked = %v, want %v", untracked, wantUntracked)
	}
}

// gitOut2 runs `git <args>` in dir returning stdout (unlike git, which
// t.Fatals; this lets the test compare against a second git call).
func gitOut2(t *testing.T, dir string, args ...string) (string, error) {
	t.Helper()
	out, err := gitOut(dir, args...)
	return out, err
}

// TestRestoreHonoursModes is the mode table the old writeBlob got wrong:
// a 100755 file keeps its exec bit, a 120000 entry is re-linked as a real
// symlink, and a file whose content already matches the snapshot is NOT
// rewritten and does NOT appear in the changed list.
func TestRestoreHonoursModes(t *testing.T) {
	dir := initRepo(t)

	// Committed base with an executable script and a symlink; a.txt is the
	// unchanged file that must not appear in `changed`.
	mustWrite(t, dir, "a.txt", "v1\n")
	// An executable script: create it WITH the exec bit so the worktree's
	// mode (which `git stash create` commits) matches the index's 100755.
	if err := os.WriteFile(filepath.Join(dir, "run.sh"), []byte("#!/bin/sh\necho run\n"), 0o755); err != nil {
		t.Fatalf("write run.sh: %v", err)
	}
	git(t, dir, "add", "a.txt", "run.sh")
	git(t, dir, "commit", "-q", "-m", "base")
	// A symlink committed to the tree (git symlinks are stored as blobs whose
	// content is the target).
	if err := os.Symlink("a.txt", filepath.Join(dir, "link.txt")); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	git(t, dir, "add", "link.txt")
	git(t, dir, "commit", "-q", "-m", "modes")

	// The committed tree has run.sh as 100755 and link.txt as a 120000
	// symlink. Sanity-check that the committed modes are what we expect.
	treeOut := git(t, dir, "ls-tree", "-r", "HEAD")
	// The committed tree has run.sh as 100755 and link.txt as a 120000
	// symlink. The ls-tree output format is "<mode> <type> <hash>\t<path>",
	// so we check for the mode+path pair (the trim in git() strips the
	// leading space, so we match the substring after the mode).
	if !strings.Contains(treeOut, "100755") || !strings.Contains(treeOut, "run.sh") {
		t.Fatalf("run.sh not committed as 100755: %q", treeOut)
	}
	if !strings.Contains(treeOut, "120000") || !strings.Contains(treeOut, "link.txt") {
		t.Fatalf("link.txt not committed as 120000: %q", treeOut)
	}

	// Snapshot the clean tree (falls back to HEAD) and record the untracked
	// baseline (just the pre-existing user file).
	snap, u, err := Snapshot(dir, RefFor("s1", "0001"))
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if snap == "" {
		t.Fatal("snapshot empty")
	}

	// Mutate: change the script's content AND strip its exec bit (the turn
	// rewrote it without the exec permission), and replace the symlink with a
	// regular file containing the target text.
	if err := os.WriteFile(filepath.Join(dir, "run.sh"), []byte("#!/bin/sh\necho changed\n"), 0o644); err != nil {
		t.Fatalf("write run.sh: %v", err)
	}
	if err := os.Chmod(filepath.Join(dir, "run.sh"), 0o644); err != nil {
		t.Fatalf("chmod run.sh: %v", err)
	}
	if err := os.Remove(filepath.Join(dir, "link.txt")); err != nil {
		t.Fatalf("remove link: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "link.txt"), []byte("a.txt\n"), 0o644); err != nil {
		t.Fatalf("write link.txt: %v", err)
	}

	changed, err := Restore(dir, snap, u)
	if err != nil {
		t.Fatalf("restore: %v", err)
	}

	// (1) The executable file is back: content restored AND exec bit restored.
	info, err := os.Stat(filepath.Join(dir, "run.sh"))
	if err != nil {
		t.Fatalf("stat run.sh: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o755 {
		t.Errorf("run.sh mode after restore = %o, want 0755", got)
	}
	if got, ok := readOr(t, dir, "run.sh"); !ok || got != "#!/bin/sh\necho run\n" {
		t.Errorf("run.sh content after restore = %q (ok=%v), want the committed bytes", got, ok)
	}

	// (2) The symlink is re-linked: os.Readlink resolves it to a.txt.
	target, isLink := readLinkPath(filepath.Join(dir, "link.txt"))
	if !isLink {
		t.Error("link.txt is not a symlink after restore")
	} else if target != "a.txt" {
		t.Errorf("link.txt target after restore = %q, want a.txt", target)
	}

	// (3) a.txt's content already matched the snapshot, so it is NOT in the
	// changed list; run.sh and link.txt (rewritten) are.
	sort.Strings(changed)
	if got := changed; !sameStrings(got, []string{"link.txt", "run.sh"}) {
		t.Errorf("changed = %v, want [link.txt run.sh] (a.txt untouched, not listed)", got)
	}
}

// readLinkPath reports the symlink target at path (and whether it is a
// symlink), for TestRestoreHonoursModes' assertion.
func readLinkPath(path string) (string, bool) {
	target, err := os.Readlink(path)
	if err != nil {
		return "", false
	}
	return target, true
}

// TestRestoreRoundTrip is the issue's headline acceptance: undo restores
// modified, created, and deleted files, and leaves untracked user files it
// didn't touch alone.
//
// A snapshot is the tracked working-tree state AFTER a turn's mutations, so
// "undo turn N" = restore to the snapshot taken at the end of turn N (the
// ref for turn N records what turn N left behind). Each turn below makes
// tracked changes, snapshots its end state, then the NEXT turn mutates again —
// so restoring to a snapshot walks the mutations back in reverse.
func TestRestoreRoundTrip(t *testing.T) {
	dir := initRepo(t)

	// Turn 1: modify a (tracked), create new.txt (untracked — the model's own
	// new file, which a stash-create snapshot does NOT capture, matching real
	// git). Snapshot turn 1's end state.
	mustWrite(t, dir, "a.txt", "v1\nmod1\n")
	mustWrite(t, dir, "new.txt", "brand\n")
	s1, u1, err := Snapshot(dir, RefFor("s1", "0001"))
	if err != nil {
		t.Fatalf("snapshot 1: %v", err)
	}
	if s1 == "" {
		t.Fatal("snapshot 1 empty though a tracked file changed")
	}
	// Turn 1's end state: the pre-existing user file AND turn-1's new.txt are
	// untracked — the baseline Restore diffs against to find turn 2's
	// creations.
	if !sameStrings(u1, []string{"new.txt", "user_untracked.txt"}) {
		t.Fatalf("snapshot 1 untracked = %v, want [new.txt user_untracked.txt]", u1)
	}

	// Turn 2: modify a again, delete b (tracked deletion via plain rm), and
	// create a turn file turn2.txt. Snapshot turn 2's end state.
	mustWrite(t, dir, "a.txt", "v1\nmod1\nmod2\n")
	mustWrite(t, dir, "turn2.txt", "t2\n")
	if err := os.Remove(filepath.Join(dir, "b.txt")); err != nil {
		t.Fatalf("remove b.txt: %v", err)
	}
	s2, u2, err := Snapshot(dir, RefFor("s1", "0002"))
	if err != nil {
		t.Fatalf("snapshot 2: %v", err)
	}
	if s2 == "" {
		t.Fatal("snapshot 2 empty though tracked files changed")
	}
	if !sameStrings(u2, []string{"new.txt", "turn2.txt", "user_untracked.txt"}) {
		t.Fatalf("snapshot 2 untracked = %v, want [new.txt turn2.txt user_untracked.txt]", u2)
	}

	// /undo 1 → restore to s2 (turn 2's end): a back to v1+mod1+mod2, b
	// absent, turn2.txt present (it was untracked at turn 2's end).
	changed, err := Restore(dir, s2, u2)
	if err != nil {
		t.Fatalf("restore to s2: %v", err)
	}
	sort.Strings(changed)
	if got, ok := readOr(t, dir, "a.txt"); !ok || got != "v1\nmod1\nmod2\n" {
		t.Errorf("a.txt after restore = %q (ok=%v), want turn-2 end %q", got, ok, "v1\nmod1\nmod2\n")
	}
	if _, ok := readOr(t, dir, "b.txt"); ok {
		t.Error("b.txt present after restore to turn-2 end, want it absent (turn 2 deleted it)")
	}
	// Untracked files that existed at the snapshot (new.txt from turn 1,
	// turn2.txt from turn 2) and the pre-existing user file are in the
	// baseline and must remain untouched.
	if got, ok := readOr(t, dir, "new.txt"); !ok || got != "brand\n" {
		t.Errorf("new.txt (untracked, turn 1) = %q (ok=%v), want it untouched %q", got, ok, "brand\n")
	}
	if got, ok := readOr(t, dir, "turn2.txt"); !ok || got != "t2\n" {
		t.Errorf("turn2.txt (untracked, turn 2) = %q (ok=%v), want it untouched %q", got, ok, "t2\n")
	}
	if got, ok := readOr(t, dir, "user_untracked.txt"); !ok || got != "user work\n" {
		t.Errorf("user_untracked.txt = %q (ok=%v), want it untouched %q", got, ok, "user work\n")
	}
	if got, ok := readOr(t, dir, filepath.Join(".cortex", "session.json")); !ok || got != "{}\n" {
		t.Errorf(".cortex/session.json = %q (ok=%v), want it untouched %q", got, ok, "{}\n")
	}
	// The changed list names exactly the paths the restore actually rewrote
	// or removed: b.txt (tracked-deleted, removed from the worktree). a.txt
	// already matches the snapshot (the snapshot was taken at the turn's end,
	// so the tree already equals the snapshot) — it is NOT rewritten and does
	// NOT appear in changed. The untracked files (new.txt, turn2.txt,
	// user_untracked.txt) are in the baseline and must NOT appear in changed.
	want := []string{"b.txt"}
	if !sameStrings(changed, want) {
		t.Errorf("changed = %v, want %v", changed, want)
	}

	// /undo 2 → restore to s1 (turn 1's end): a back to v1+mod1, b
	// re-materialised, and turn2.txt REMOVED (created by turn 2, not in
	// s1's untracked baseline). new.txt and user_untracked.txt survive.
	changed, err = Restore(dir, s1, u1)
	if err != nil {
		t.Fatalf("restore to s1: %v", err)
	}
	sort.Strings(changed)
	if got, ok := readOr(t, dir, "a.txt"); !ok || got != "v1\nmod1\n" {
		t.Errorf("a.txt after second undo = %q (ok=%v), want turn-1 end %q", got, ok, "v1\nmod1\n")
	}
	if got, ok := readOr(t, dir, "b.txt"); !ok || got != "keep\n" {
		t.Errorf("b.txt after second undo = %q (ok=%v), want it re-materialised as %q", got, ok, "keep\n")
	}
	if _, ok := readOr(t, dir, "turn2.txt"); ok {
		t.Error("turn2.txt present after undo to turn 1's end; it was created by turn 2 and must be removed")
	}
	if got, ok := readOr(t, dir, "new.txt"); !ok || got != "brand\n" {
		t.Errorf("new.txt after second undo = %q (ok=%v), want it untouched (in s1's baseline)", got, ok)
	}
	if got, ok := readOr(t, dir, "user_untracked.txt"); !ok || got != "user work\n" {
		t.Errorf("user_untracked.txt after second undo = %q (ok=%v), want it untouched", got, ok)
	}
	// turn2.txt is in changed (it was removed); a.txt was rewritten; b.txt was
	// re-materialised. new.txt / user_untracked.txt are not in changed.
	want = []string{"a.txt", "b.txt", "turn2.txt"}
	if !sameStrings(changed, want) {
		t.Errorf("changed (second undo) = %v, want %v", changed, want)
	}
}

// TestRestoreIndexAndStashUntouched is the issue's second acceptance: the
// user's stash list and index are unchanged across snapshot + restore — even
// when a deletion was staged.
func TestRestoreIndexAndStashUntouched(t *testing.T) {
	dir := initRepo(t)

	idxBefore := indexPathsOf(t, dir)
	stashBefore := git(t, dir, "stash", "list")

	// Mutate the tracked tree and stage a deletion so the index is non-trivial.
	mustWrite(t, dir, "a.txt", "v1\nmod\n")
	git(t, dir, "rm", "-q", "b.txt") // staged deletion
	idxAfterMutate := indexPathsOf(t, dir)

	s, u, err := Snapshot(dir, RefFor("s1", "0001"))
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if s == "" {
		t.Fatal("snapshot empty though the tracked tree changed")
	}
	if stashAfter := git(t, dir, "stash", "list"); stashAfter != stashBefore {
		t.Errorf("git stash list changed across Snapshot: before=%q after=%q", stashBefore, stashAfter)
	}
	if idx := indexPathsOf(t, dir); !sameStrings(idx, idxAfterMutate) {
		t.Errorf("Snapshot touched the index: before=%v after=%v", idxAfterMutate, idx)
	}
	// The untracked baseline at snapshot time: only the pre-existing user
	// file (b.txt is staged-deleted, not untracked; a.txt is tracked).
	if !sameStrings(u, []string{"user_untracked.txt"}) {
		t.Fatalf("untracked baseline = %v, want [user_untracked.txt]", u)
	}

	// More mutation, then restore.
	mustWrite(t, dir, "a.txt", "v1\nmod\nmore\n")
	if _, err := Restore(dir, s, u); err != nil {
		t.Fatalf("restore: %v", err)
	}

	if idx := indexPathsOf(t, dir); !sameStrings(idx, idxAfterMutate) {
		t.Errorf("Restore touched the index: before=%v after=%v", idxAfterMutate, idx)
	}
	if stashAfter := git(t, dir, "stash", "list"); stashAfter != stashBefore {
		t.Errorf("git stash list changed across Restore: before=%q after=%q", stashBefore, stashAfter)
	}
	if stashAfter := git(t, dir, "stash", "list"); stashAfter != "" {
		t.Errorf("a stash entry was created; got %q", stashAfter)
	}

	// Sanity: the index still reflects the committed base for a.txt and the
	// staged deletion for b.txt (b.txt not in the index).
	_ = idxBefore // b.txt was committed, a.txt is; idxBefore = [a.txt b.txt]
	if !sameStrings(idxBefore, []string{"a.txt", "b.txt"}) {
		t.Fatalf("fixture index unexpected: %v", idxBefore)
	}
	if !sameStrings(indexPathsOf(t, dir), []string{"a.txt"}) {
		t.Errorf("index after restore = %v, want [a.txt] (b.txt still staged-deleted)", indexPathsOf(t, dir))
	}
}

// TestPrune keeps the newest `keep` checkpoint refs for ONE session and
// drops the rest — never touching another session's refs (a concurrent or
// lower-sorting session's history must survive).
func TestPrune(t *testing.T) {
	dir := initRepo(t)
	// Four checkpoints for one session, zero-padded ordinals (the REPL's
	// format), so lexicographic order == creation order.
	for turn := "0001"; turn <= "0004"; turn = nextTurn(turn) {
		mustWrite(t, dir, "a.txt", "v\n"+turn+"\n")
		if s, _, err := Snapshot(dir, RefFor("s1", turn)); err != nil {
			t.Fatalf("snapshot %s: %v", turn, err)
		} else if s == "" {
			t.Fatalf("snapshot %s empty", turn)
		}
	}
	// A second session's ref (lower-sorting than s1) must survive s1's Prune.
	if s, _, err := Snapshot(dir, RefFor("s0", "0001")); err != nil {
		t.Fatalf("snapshot s0: %v", err)
	} else if s == "" {
		t.Fatalf("snapshot s0 empty")
	}
	refsBefore := git(t, dir, "for-each-ref", "--format=%(refname)", RefPrefix)
	if n := len(nonEmptyLines(refsBefore)); n != 5 {
		t.Fatalf("want 5 checkpoint refs, got %d (%q)", n, refsBefore)
	}

	Prune(dir, "s1", 2)

	refs := git(t, dir, "for-each-ref", "--format=%(refname)", RefPrefix)
	lines := nonEmptyLines(refs)
	sort.Strings(lines)
	// The two s1 survivors are the newest (0003, 0004); the other session's
	// ref is untouched.
	if !sameStrings(lines, []string{RefPrefix + "s0/0001", RefPrefix + "s1/0003", RefPrefix + "s1/0004"}) {
		t.Errorf("survivors = %v, want [s0/0001 s1/0003 s1/0004]", lines)
	}
}

// TestPruneOtherSessionUntouched is the cross-session isolation the old
// global Prune violated: pruning session A's refs must never delete session
// B's, even when B's id sorts lower and B was created first.
func TestPruneOtherSessionUntouched(t *testing.T) {
	dir := initRepo(t)
	// Two checkpoints for the lower-sorting session s0.
	for turn := "0001"; turn <= "0002"; turn = nextTurn(turn) {
		mustWrite(t, dir, "a.txt", "v\n"+turn+"\n")
		if _, _, err := Snapshot(dir, RefFor("s0", turn)); err != nil {
			t.Fatalf("snapshot s0 %s: %v", turn, err)
		}
	}
	// Three checkpoints for s1 (enough to exceed its keep ceiling).
	for turn := "0001"; turn <= "0003"; turn = nextTurn(turn) {
		mustWrite(t, dir, "b.txt", "v\n"+turn+"\n")
		if _, _, err := Snapshot(dir, RefFor("s1", turn)); err != nil {
			t.Fatalf("snapshot s1 %s: %v", turn, err)
		}
	}
	Prune(dir, "s1", 2)

	// s0's refs are all still present.
	s0refs, err := listCheckpointRefs(dir, RefPrefix+"s0/")
	if err != nil {
		t.Fatalf("list s0: %v", err)
	}
	if len(s0refs) != 2 {
		t.Errorf("s0 refs after pruning s1 = %v, want both s0 refs intact", s0refs)
	}
	// s1 is pruned to its two newest.
	s1refs, err := listCheckpointRefs(dir, RefPrefix+"s1/")
	if err != nil {
		t.Fatalf("list s1: %v", err)
	}
	if len(s1refs) != 2 {
		t.Errorf("s1 refs after prune = %v, want 2 (the newest)", s1refs)
	}
}

func nonEmptyLines(s string) []string {
	var lines []string
	for _, l := range strings.Split(s, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			lines = append(lines, l)
		}
	}
	return lines
}

func nextTurn(s string) string {
	n, err := strconv.Atoi(s)
	if err != nil {
		return "0001" // zero-padded ordinals are always numeric; fallback is unreachable
	}
	return pad4(n + 1)
}

func pad4(n int) string {
	if n < 10 {
		return "000" + strconv.Itoa(n)
	}
	if n < 100 {
		return "00" + strconv.Itoa(n)
	}
	if n < 1000 {
		return "0" + strconv.Itoa(n)
	}
	return strconv.Itoa(n)
}

func sameStrings(a, b []string) bool {
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
