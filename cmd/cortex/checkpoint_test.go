package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dereksantos/cortex/internal/checkpoint"
)

// checkpoint_test.go covers the REPL wiring of internal/checkpoint (issue
// #111): the snapshot taken at the start of a turn, the in-memory stack, and
// the /clear + Close cleanup. It drives the REAL git binary in a temp repo (the
// same gitCmd/gitCmdOutput + initGitRepo convention change_test.go establishes)
// so the assertions are on real refs and real git state, not a stub.

// gitCheckpointRefs returns the set of hidden checkpoint refs currently in dir.
func gitCheckpointRefs(t *testing.T, dir, prefix string) map[string]bool {
	t.Helper()
	out, err := gitCmdOutput(t, dir, "for-each-ref", "--format=%(refname)", prefix)
	if err != nil {
		t.Fatalf("for-each-ref: %v", err)
	}
	refs := make(map[string]bool)
	for _, line := range strings.Split(out, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			refs[line] = true
		}
	}
	return refs
}

func checkpointRefCount(t *testing.T, dir string) int {
	t.Helper()
	return len(gitCheckpointRefs(t, dir, checkpoint.RefPrefix))
}

// testCheckpointSession builds a hand-built session whose workspace is rooted
// at the git repo in dir (the gitCmdIn/ensureSelfGitignore seam's explicit-root
// leg), so cs.root() resolves to a real repo the git calls can run in.
func testCheckpointSession(t *testing.T, dir string) *CortexSession {
	t.Helper()
	t.Chdir(dir)
	ws, err := NewWorkspace(dir)
	if err != nil {
		t.Fatalf("NewWorkspace(%s): %v", dir, err)
	}
	cs := &CortexSession{quiet: true, workspace: ws, Request: CortexArgs{}.Request()}
	cs.SessionID = "s-test-0001"
	return cs
}

func TestRecordCheckpointRecordsRefOnMutatedTree(t *testing.T) {
	dir := t.TempDir()
	initGitRepo(t, dir)
	// A committed file the turn will mutate.
	mustWrite(t, dir, "a.txt", "v1\n")
	gitCmd(t, dir, "add", "a.txt")
	gitCmd(t, dir, "commit", "-q", "-m", "base")

	cs := testCheckpointSession(t, dir)
	cs.turnNo = 1 // turn() stamps this before recordCheckpoint runs

	// Mutate a tracked file so git stash create is non-empty.
	mustWrite(t, dir, "a.txt", "v2\n")

	cs.recordCheckpoint()

	if cs.checkpoints == nil || cs.checkpoints.empty() {
		t.Fatal("recordCheckpoint left the stack empty on a mutated tree")
	}
	got := cs.checkpoints.newest()
	if got == "" {
		t.Fatal("stack top is empty")
	}
	// A ref must have been recorded for this session, at this turn's ref.
	wantRef := checkpoint.RefFor(cs.SessionID, "0001")
	refs := gitCheckpointRefs(t, dir, checkpoint.RefPrefix)
	if !refs[wantRef] {
		t.Errorf("ref %s not recorded; have %v", wantRef, keys(refs))
	}
	// The ref names the snapshot the stack holds.
	if head, err := gitCmdOutput(t, dir, "rev-parse", wantRef); err != nil || head != got {
		t.Errorf("ref %s resolves to %q (err=%v), want %q", wantRef, head, err, got)
	}
}

func TestRecordCheckpointNoopOnCleanTree(t *testing.T) {
	dir := t.TempDir()
	initGitRepo(t, dir)
	mustWrite(t, dir, "a.txt", "v1\n")
	gitCmd(t, dir, "add", "a.txt")
	gitCmd(t, dir, "commit", "-q", "-m", "base")

	cs := testCheckpointSession(t, dir)
	cs.turnNo = 1

	// No tracked mutation: git stash create is empty → no ref, empty stack.
	cs.recordCheckpoint()

	if cs.checkpoints != nil && !cs.checkpoints.empty() {
		t.Errorf("clean tree recorded a checkpoint; stack=%v", cs.checkpoints.snapshots)
	}
	if n := checkpointRefCount(t, dir); n != 0 {
		t.Errorf("clean tree recorded %d checkpoint refs, want 0", n)
	}
}

func TestRecordCheckpointNonRepoIsNoop(t *testing.T) {
	dir := t.TempDir() // not a git repo
	// A workspace rooted at the non-repo dir: root() resolves to it, and
	// checkpoint.Available is false there, so recordCheckpoint is a no-op.
	ws, err := NewWorkspace(dir)
	if err != nil {
		t.Fatalf("NewWorkspace: %v", err)
	}
	cs := &CortexSession{quiet: true, workspace: ws, Request: CortexArgs{}.Request()}
	cs.SessionID = "s-norepo"
	cs.turnNo = 1

	cs.recordCheckpoint() // must not panic, must not record anything

	if cs.checkpoints != nil && !cs.checkpoints.empty() {
		t.Errorf("non-repo recorded a checkpoint; stack=%v", cs.checkpoints.snapshots)
	}
}

func TestRecordCheckpointPrunes(t *testing.T) {
	dir := t.TempDir()
	initGitRepo(t, dir)
	mustWrite(t, dir, "a.txt", "v0\n")
	gitCmd(t, dir, "add", "a.txt")
	gitCmd(t, dir, "commit", "-q", "-m", "base")

	cs := testCheckpointSession(t, dir)
	// Record more than maxCheckpointRefs checkpoints, each on a mutated tree.
	for turn := 1; turn <= maxCheckpointRefs+10; turn++ {
		cs.turnNo = turn
		mustWrite(t, dir, "a.txt", "v"+itoa(turn)+"\n")
		cs.recordCheckpoint()
	}
	// The on-disk refs are pruned to maxCheckpointRefs.
	if n := checkpointRefCount(t, dir); n != maxCheckpointRefs {
		t.Errorf("after %d turns, %d checkpoint refs, want %d (pruned)", maxCheckpointRefs+10, n, maxCheckpointRefs)
	}
	// The in-memory stack is pruned to the same ceiling.
	if cs.checkpoints == nil || len(cs.checkpoints.snapshots) != maxCheckpointRefs {
		t.Errorf("stack length = %v, want %d", lenOf(cs.checkpoints), maxCheckpointRefs)
	}
}

func lenOf(s *checkpointStack) int {
	if s == nil {
		return -1
	}
	return len(s.snapshots)
}

func TestClearCheckpointsClearsStackAndRefs(t *testing.T) {
	dir := t.TempDir()
	initGitRepo(t, dir)
	mustWrite(t, dir, "a.txt", "v1\n")
	gitCmd(t, dir, "add", "a.txt")
	gitCmd(t, dir, "commit", "-q", "-m", "base")

	cs := testCheckpointSession(t, dir)
	cs.turnNo = 1
	mustWrite(t, dir, "a.txt", "v2\n")
	cs.recordCheckpoint()
	if n := checkpointRefCount(t, dir); n == 0 {
		t.Fatal("no checkpoint ref recorded before clear")
	}

	cs.clearCheckpoints()

	if cs.checkpoints != nil && !cs.checkpoints.empty() {
		t.Errorf("clearCheckpoints left the stack: %v", cs.checkpoints.snapshots)
	}
	if n := checkpointRefCount(t, dir); n != 0 {
		t.Errorf("clearCheckpoints left %d checkpoint refs, want 0", n)
	}
}

func TestClearClearsCheckpoints(t *testing.T) {
	cs := newTestSession(t)
	// Simulate a recorded checkpoint on this session's stack (the git repo
	// cleanup in clearCheckpoints is best-effort and a no-op here — the temp
	// CWD is not a git repo — so only the in-memory stack matters for this
	// assertion).
	cs.SessionID = "s-clear-0001"
	cs.checkpoints = &checkpointStack{snapshots: []string{"deadbeef"}}

	cs.Clear()
	defer cs.transcript.Close()

	if cs.checkpoints != nil && !cs.checkpoints.empty() {
		t.Errorf("Clear left the checkpoint stack: %v", cs.checkpoints.snapshots)
	}
}

func TestCloseClearsCheckpoints(t *testing.T) {
	cs := newTestSession(t)
	cs.SessionID = "s-close-0001"
	cs.checkpoints = &checkpointStack{snapshots: []string{"deadbeef"}}

	cs.Close()

	if cs.checkpoints != nil && !cs.checkpoints.empty() {
		t.Errorf("Close left the checkpoint stack: %v", cs.checkpoints.snapshots)
	}
}

func keys(m map[string]bool) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	return out
}

// mustWrite writes a file under dir (a temp fixture dir). It is separate from
// the checkpoint package's own mustWrite (different package) so the main test
// package keeps its own helper without an import.
func mustWrite(t *testing.T, dir, path, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, path), []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func TestUndoTranscriptNote(t *testing.T) {
	// The default (no "what") still yields a complete sentence.
	if got := undoTranscriptNote(""); !strings.Contains(got, "reverted your most recent turn's file changes") || !strings.Contains(got, "do not act as though those changes still stand") {
		t.Errorf("default note = %q", got)
	}
	// A described "what" is spliced in.
	if got := undoTranscriptNote("the edits to foo.go and bar.go"); !strings.Contains(got, "reverted the edits to foo.go and bar.go") {
		t.Errorf("note with what = %q", got)
	}
	// Both spell the harness provenance so a resumed model knows it is not a
	// user utterance.
	if !strings.HasPrefix(undoTranscriptNote(""), "Harness note: ") {
		t.Errorf("note must be prefixed with the harness marker: %q", undoTranscriptNote(""))
	}
}

func TestRecordUndoAppendsToTranscriptAndReplaysOnResume(t *testing.T) {
	cs := newTestSession(t)
	cs.SessionID = "s-undo-0001"
	// A prior turn's user + assistant messages, so the undo note lands after
	// real history (mirrors the post-turn state /undo runs from).
	cs.Append(Message{Role: RoleUser, Content: "edit the file"})
	cs.Append(Message{Role: "assistant", Content: "done"})

	note := "your most recent turn's file changes"
	cs.recordUndo(note)

	// The note is the last in-memory message, RoleUser, and names the revert.
	got := cs.Request.Messages[len(cs.Request.Messages)-1]
	if got.Role != RoleUser {
		t.Errorf("undo note role = %q, want %q", got.Role, RoleUser)
	}
	if !strings.Contains(got.Content, "reverted") || !strings.Contains(got.Content, "do not act as though those changes still stand") {
		t.Errorf("undo note content = %q", got.Content)
	}

	// Resume replays the note from the transcript (the durable half of the
	// undo: a fresh session that resumes this one still knows the edits were
	// reverted).
	cs.Close()
	resumed := &CortexSession{Request: CortexArgs{}.Request()}
	if err := resumed.ResumeTranscript(""); err != nil {
		t.Fatalf("resume: %v", err)
	}
	defer resumed.transcript.Close()
	r := resumed.Request.Messages[len(resumed.Request.Messages)-1]
	if !strings.Contains(r.Content, "reverted") {
		t.Errorf("resumed undo note = %q, want the revert note replayed", r.Content)
	}
}

// TestUndoStackNth covers the 1-based lookup /undo [N] uses: 1 is the newest,
// N the Nth-from-newest, and out-of-range depths report absent.
func TestUndoStackNth(t *testing.T) {
	s := &checkpointStack{snapshots: []string{"old", "mid", "new"}}
	if v, ok := s.nth(1); !ok || v != "new" {
		t.Errorf("nth(1) = %q, %v; want new", v, ok)
	}
	if v, ok := s.nth(2); !ok || v != "mid" {
		t.Errorf("nth(2) = %q, %v; want mid", v, ok)
	}
	if v, ok := s.nth(3); !ok || v != "old" {
		t.Errorf("nth(3) = %q, %v; want old", v, ok)
	}
	if _, ok := s.nth(0); ok {
		t.Error("nth(0) should be out of range")
	}
	if _, ok := s.nth(4); ok {
		t.Error("nth(4) should be out of range for a 3-deep stack")
	}
}

// TestUndoCommandNotInRepo is the one-line disabled path: not a git worktree.
func TestUndoCommandNotInRepo(t *testing.T) {
	dir := t.TempDir() // no git repo
	cs := testCheckpointSession(t, dir)
	cs.checkpoints = &checkpointStack{snapshots: []string{"deadbeef"}}
	// Must not panic; prints the disabled one-liner and changes nothing.
	cs.undo(1)
	if cs.checkpoints == nil || cs.checkpoints.empty() {
		t.Error("undo outside a repo must leave the stack untouched")
	}
}

// TestUndoCommandNothingToUndo is the empty-stack one-liner.
func TestUndoCommandNothingToUndo(t *testing.T) {
	dir := t.TempDir()
	initGitRepo(t, dir)
	cs := testCheckpointSession(t, dir)
	cs.undo(1) // no snapshots recorded
	if cs.checkpoints != nil && !cs.checkpoints.empty() {
		t.Error("undo with no snapshots must be a no-op")
	}
}

// TestUndoRestoresAndDropsRefs drives the real git path end to end: two
// mutating "turns" each record a checkpoint (the snapshot taken at each turn's
// start captures that turn's end state — see the step-1 TestRestoreRoundTrip
// doc comment), /undo restores the newest snapshot to the working tree,
// appends the transcript note, and drops the consumed ref.
func TestUndoRestoresAndDropsRefs(t *testing.T) {
	dir := t.TempDir()
	initGitRepo(t, dir)
	mustWrite(t, dir, "a.txt", "v1\n")
	gitCmd(t, dir, "add", "a.txt")
	gitCmd(t, dir, "commit", "-q", "-m", "base")

	cs := testCheckpointSession(t, dir)

	// Turn 1 mutates the file; at turn 2's START the snapshot captures turn 1's
	// end state (v2). Turn 2 mutates again; at turn 3's START the snapshot
	// captures turn 2's end state (v3, the newest).
	cs.turnNo = 1
	mustWrite(t, dir, "a.txt", "v2\n")
	cs.recordCheckpoint()
	cs.turnNo = 2
	mustWrite(t, dir, "a.txt", "v3\n")
	cs.recordCheckpoint()
	if cs.checkpoints.empty() {
		t.Fatal("no checkpoints recorded")
	}
	if checkpointRefCount(t, dir) != 2 {
		t.Fatalf("want 2 checkpoint refs before undo, got %d", checkpointRefCount(t, dir))
	}

	// /undo (N=1) restores the newest snapshot — the most recent turn's end
	// state (v3). The tree already holds v3, so the restore is idempotent here;
	// what matters is the bookkeeping: the consumed ref is dropped and the
	// transcript note records the revert.
	cs.undo(1)

	if got := read(t, dir, "a.txt"); got != "v3\n" {
		t.Errorf("after /undo, a.txt = %q, want the newest snapshot's end state \"v3\\n\"", got)
	}
	// The consumed ref is dropped; the older (still undoable) one remains.
	if n := checkpointRefCount(t, dir); n != 1 {
		t.Errorf("after /undo, %d checkpoint refs remain, want 1 (newest consumed)", n)
	}
	// The transcript note records the revert (step 3's durable half).
	last := cs.Request.Messages[len(cs.Request.Messages)-1]
	if last.Role != RoleUser || !strings.Contains(last.Content, "reverted") {
		t.Errorf("undo transcript note = %+v, want a RoleUser revert note", last)
	}
	// The restore actually reverts the working tree: the older (now-newest)
	// snapshot is turn 1's end state (v2). /undo 1 at this depth restores it —
	// a.txt back to v2 — and consumes the last ref, leaving an empty stack.
	cs.undo(1)
	if got := read(t, dir, "a.txt"); got != "v2\n" {
		t.Errorf("after second /undo, a.txt = %q, want turn-1 end state \"v2\\n\" (the older snapshot's restore)", got)
	}
	if n := checkpointRefCount(t, dir); n != 0 {
		t.Errorf("after second /undo, %d checkpoint refs remain, want 0", n)
	}
	if !cs.checkpoints.empty() {
		t.Error("stack not empty after consuming both snapshots")
	}
}

// TestUndoCommandOutOfRange is the "only N recorded" one-liner for N beyond
// the recorded history.
func TestUndoCommandOutOfRange(t *testing.T) {
	dir := t.TempDir()
	initGitRepo(t, dir)
	mustWrite(t, dir, "a.txt", "v1\n")
	gitCmd(t, dir, "add", "a.txt")
	gitCmd(t, dir, "commit", "-q", "-m", "base")
	cs := testCheckpointSession(t, dir)
	cs.turnNo = 1
	mustWrite(t, dir, "a.txt", "v2\n")
	cs.recordCheckpoint()

	before := read(t, dir, "a.txt")
	cs.undo(99) // beyond the single recorded snapshot
	if got := read(t, dir, "a.txt"); got != before {
		t.Errorf("out-of-range /undo changed the tree: %q -> %q", before, got)
	}
	if n := checkpointRefCount(t, dir); n != 1 {
		t.Errorf("out-of-range /undo dropped refs: %d remain, want 1", n)
	}
}

// read returns the contents of dir/path, failing the test on read error.
func read(t *testing.T, dir, path string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, path))
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(b)
}
