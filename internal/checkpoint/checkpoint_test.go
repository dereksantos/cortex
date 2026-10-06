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

// TestSnapshotNoopOnCleanTree is the "only turns that mutate record
// checkpoints" clause: a clean tracked tree yields an empty hash and records
// no ref.
func TestSnapshotNoopOnCleanTree(t *testing.T) {
	dir := initRepo(t)
	ref := RefFor("s1", "0001")
	got, err := Snapshot(dir, ref)
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if got != "" {
		t.Errorf("Snapshot on clean tree = %q, want empty (no-op)", got)
	}
	// No ref created.
	refs := git(t, dir, "for-each-ref", "--format=%(refname)", RefPrefix)
	if refs != "" {
		t.Errorf("a checkpoint ref was created on a clean tree: %q", refs)
	}
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
	s1, err := Snapshot(dir, RefFor("s1", "0001"))
	if err != nil {
		t.Fatalf("snapshot 1: %v", err)
	}
	if s1 == "" {
		t.Fatal("snapshot 1 empty though a tracked file changed")
	}

	// Turn 2: modify a again, delete b (tracked deletion via plain rm).
	// Snapshot turn 2's end state.
	mustWrite(t, dir, "a.txt", "v1\nmod1\nmod2\n")
	if err := os.Remove(filepath.Join(dir, "b.txt")); err != nil {
		t.Fatalf("remove b.txt: %v", err)
	}
	s2, err := Snapshot(dir, RefFor("s1", "0002"))
	if err != nil {
		t.Fatalf("snapshot 2: %v", err)
	}
	if s2 == "" {
		t.Fatal("snapshot 2 empty though tracked files changed")
	}

	// /undo 1 → restore to s2 (turn 2's end): a back to v1+mod1+mod2, b absent.
	changed, err := Restore(dir, s2)
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
	// Untracked files the turn created (new.txt) and the pre-existing user
	// file are not in the snapshot tree and must remain untouched.
	if got, ok := readOr(t, dir, "new.txt"); !ok || got != "brand\n" {
		t.Errorf("new.txt (untracked, turn 1) = %q (ok=%v), want it untouched %q", got, ok, "brand\n")
	}
	if got, ok := readOr(t, dir, "user_untracked.txt"); !ok || got != "user work\n" {
		t.Errorf("user_untracked.txt = %q (ok=%v), want it untouched %q", got, ok, "user work\n")
	}
	if got, ok := readOr(t, dir, filepath.Join(".cortex", "session.json")); !ok || got != "{}\n" {
		t.Errorf(".cortex/session.json = %q (ok=%v), want it untouched %q", got, ok, "{}\n")
	}
	// The changed list names exactly the snapshot's blobs — a.txt only (b.txt
	// is absent from the turn-2 snapshot and was removed as tracked-deleted).
	// It must NOT include the untracked new.txt or the user file.
	want := []string{"a.txt", "b.txt"}
	if !sameStrings(changed, want) {
		t.Errorf("changed = %v, want %v", changed, want)
	}

	// /undo 2 → restore to s1 (turn 1's end): a back to v1+mod1, b re-created.
	if _, err := Restore(dir, s1); err != nil {
		t.Fatalf("restore to s1: %v", err)
	}
	if got, ok := readOr(t, dir, "a.txt"); !ok || got != "v1\nmod1\n" {
		t.Errorf("a.txt after second undo = %q (ok=%v), want turn-1 end %q", got, ok, "v1\nmod1\n")
	}
	if got, ok := readOr(t, dir, "b.txt"); !ok || got != "keep\n" {
		t.Errorf("b.txt after second undo = %q (ok=%v), want it re-materialised as %q", got, ok, "keep\n")
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

	s, err := Snapshot(dir, RefFor("s1", "0001"))
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

	// More mutation, then restore.
	mustWrite(t, dir, "a.txt", "v1\nmod\nmore\n")
	if _, err := Restore(dir, s); err != nil {
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

// TestPrune keeps the newest `keep` checkpoint refs and drops the rest.
func TestPrune(t *testing.T) {
	dir := initRepo(t)
	// Four checkpoints for one session, zero-padded ordinals (the REPL's
	// format), so lexicographic order == creation order.
	for turn := "0001"; turn <= "0004"; turn = nextTurn(turn) {
		mustWrite(t, dir, "a.txt", "v\n"+turn+"\n")
		if s, err := Snapshot(dir, RefFor("s1", turn)); err != nil {
			t.Fatalf("snapshot %s: %v", turn, err)
		} else if s == "" {
			t.Fatalf("snapshot %s empty", turn)
		}
	}
	refsBefore := git(t, dir, "for-each-ref", "--format=%(refname)", RefPrefix)
	if n := strings.Count(refsBefore, "\n") + 1; n != 4 {
		t.Fatalf("want 4 checkpoint refs, got %d (%q)", n, refsBefore)
	}

	Prune(dir, 2)

	refs := git(t, dir, "for-each-ref", "--format=%(refname)", RefPrefix)
	var lines []string
	for _, l := range strings.Split(refs, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			lines = append(lines, l)
		}
	}
	if len(lines) != 2 {
		t.Fatalf("Prune(2) left %d refs, want 2: %v", len(lines), lines)
	}
	// The two survivors are the newest: 0003 and 0004. The assertion reads the
	// refs straight off `for-each-ref`, which lists lexicographically, so the
	// order here is [0003, 0004].
	if !sameStrings(lines, []string{RefPrefix + "s1/0003", RefPrefix + "s1/0004"}) {
		t.Errorf("survivors = %v, want the two newest [s1/0003 s1/0004]", lines)
	}
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
