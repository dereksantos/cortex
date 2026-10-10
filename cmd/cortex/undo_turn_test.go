// undo_turn_test.go — issue #111: the cmd/cortex-level ACCEPTANCE tests for
// /undo, driven through the REAL turn path (cs.Turn) with a scripted backend
// (senderOverride) over a real git repo — the
// forced_finalize_turn_test.go / newFixtureRepo pattern: zero network, but the
// REAL coderDispatcher runs, so the scripted write_file / edit_file /
// remove_path calls actually create / modify / delete files on disk, exactly
// as a production turn would. The per-turn checkpoint is recorded at the
// start of each turn (recordCheckpoint) and committed to the undo stack at
// the turn's end iff the turn mutated files (commitCheckpoint) — so /undo
// restores the modified, created, and deleted files back while leaving
// untracked user files and .cortex/ alone (the issue's acceptance criteria).
//
// A snapshot names the tree the turn STARTS from (HEAD's commit on a clean
// tree), so "undo turn N" = restore to the state the Nth-most-recent MUTATING
// turn left behind: /undo 1 (the newest snapshot) reverts the most recent
// mutating turn's edits. The dedicated TestUndoStashAndIndexUntouched pins
// the issue's second acceptance criterion — the user's stash list and index
// are unchanged across a snapshot + restore, even when a tracked deletion is
// involved.

package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// undoTurnScriptedSession builds a scripted session whose coder's round-trip
// is driven by script (the fake Sender) over a real git repo — zero network —
// while the REAL coderDispatcher runs (the write_file / edit_file /
// remove_path calls create, modify, and delete files on disk; the per-turn
// checkpoint records a real, restorable snapshot). The workspace is rooted at
// the git repo in root (t.Chdir) so relative tool paths resolve there and
// cs.root() is the repo the git checkpoint calls run in. allowDelete is on so
// remove_path is permitted (a hand-built session defaults it off; the
// REPL's NewCortexSession sets it from config, which defaults to enabled).
func undoTurnScriptedSession(t *testing.T, root string, script []*AgentResponse) *CortexSession {
	t.Helper()
	t.Chdir(root)
	ws, err := NewWorkspace(root)
	if err != nil {
		t.Fatalf("NewWorkspace: %v", err)
	}
	cs := &CortexSession{
		workspace:   ws,
		Window:      20000,
		SessionID:   "undo-acceptance-test",
		Request:     &AgentRequest{Model: "m", Messages: []Message{{Role: RoleSystem, Content: "sys"}}},
		allowDelete: true,
		deleteRoot:  root,
	}
	cs.ws = cs.newWorkingSet(1)
	cs.StartTranscript()
	t.Cleanup(func() { cs.Close() })

	var i int
	cs.senderOverride = SenderFunc(func(_ context.Context, _ *AgentRequest) (*AgentResponse, bool, error) {
		r := script[i]
		if i < len(script)-1 {
			i++
		}
		return r, false, nil
	})
	return cs
}

// initUndoRepo builds a git repo in root with the base files /undo's
// acceptance criteria assume: three committed tracked files (modify.go will
// be modified, delete.go will be deleted, keep.go is untouched), plus a
// pre-existing untracked user file and a .cortex/ file that a snapshot +
// restore must leave alone.
func initUndoRepo(t *testing.T, root string) {
	t.Helper()
	initGitRepo(t, root)
	mustWrite(t, root, "modify.go", "package modify\n")
	mustWrite(t, root, "delete.go", "package delete\n")
	mustWrite(t, root, "keep.go", "package keep\n")
	gitCmd(t, root, "add", "modify.go", "delete.go", "keep.go")
	gitCmd(t, root, "commit", "-q", "-m", "base")
	// Pre-existing untracked user file — must survive a restore untouched.
	mustWrite(t, root, "user_untracked.txt", "user work\n")
	// The gitignored .cortex/ dir — never in a snapshot, never touched.
	if err := os.MkdirAll(filepath.Join(root, ".cortex"), 0o755); err != nil {
		t.Fatalf("mkdir .cortex: %v", err)
	}
	mustWrite(t, root, filepath.Join(".cortex", "session.json"), "{}\n")
}

// TestUndoRestoresModifiedCreatedDeleted is the issue #111 acceptance
// headline, driven end to end through the REAL turn path (cs.Turn) and the
// REAL /undo path (cs.undo) over a clean, committed fixture — the case the
// old test hid: a clean tree at the turn's start, then one bad turn. The
// scripted turn modifies a tracked file, CREATES a file (write_file), and
// DELETES a tracked file (remove_path); the turn's start-of-turn snapshot
// (recordCheckpoint, falling back to HEAD on the clean tree) is committed to
// the undo stack at the turn's end (commitCheckpoint, the turn mutated
// files), and cs.undo(1) restores it: the modified file reverts, the created
// file is removed, the deleted file is re-materialised, the pre-existing
// untracked user file and .cortex/ are untouched, the transcript gets the
// undo note, and the consumed ref is gone.
func TestUndoRestoresModifiedCreatedDeleted(t *testing.T) {
	root := t.TempDir()
	initUndoRepo(t, root)
	cs := undoTurnScriptedSession(t, root, nil)

	// The pre-mutation state: three tracked files (modify.go / delete.go /
	// keep.go), committed, clean — plus the pre-existing untracked user file
	// and the gitignored .cortex/ file.
	modifyBefore := readFileContent(t, root, "modify.go")
	deleteBefore := readFileContent(t, root, "delete.go")
	userBefore := readFileContent(t, root, "user_untracked.txt")
	cortexBefore := readFileContent(t, root, filepath.Join(".cortex", "session.json"))
	wantRef := "refs/cortex/checkpoints/" + cs.SessionID + "/0001"

	// The REAL turn path runs the mutations: modify modify.go, create
	// created.txt (untracked), delete delete.go. The turn's start-of-turn
	// snapshot (recordCheckpoint) and turn-end commit (commitCheckpoint) run
	// inside cs.Turn exactly as in production.
	script := []*AgentResponse{
		editFileCallResp("c1", "modify.go", "package modify", "package modify_changed"),
		writeFileCallResp("c2", "created.txt", "created content\n"),
		removePathCallResp("c3", "delete.go"),
		answerResp("done"),
	}
	cs.senderOverride = SenderFunc(multiResp(script))

	if _, err := cs.Turn(context.Background(), "mutate files"); err != nil {
		t.Fatalf("Turn: %v", err)
	}

	// Confirm the turn actually mutated the tree (the real dispatcher ran).
	if got := readFileContent(t, root, "modify.go"); got == modifyBefore {
		t.Fatalf("modify.go unchanged after turn — the real dispatcher did not run")
	}
	if !fileExists(filepath.Join(root, "created.txt")) {
		t.Fatal("created.txt not created by the turn's write_file")
	}
	if fileExists(filepath.Join(root, "delete.go")) {
		t.Fatal("delete.go still present after the turn's remove_path")
	}

	// The turn's checkpoint is on the stack (the turn mutated files) and its
	// ref was recorded at the turn's start.
	if cs.checkpoints == nil || cs.checkpoints.empty() {
		t.Fatal("the mutating turn recorded no checkpoint — /undo would say nothing to undo")
	}
	refs := gitCheckpointRefs(t, root, "refs/cortex/checkpoints/")
	if !refs[wantRef] {
		t.Fatalf("ref %s not recorded; have %v", wantRef, keys(refs))
	}

	// The REAL /undo path: restore the newest snapshot to the working tree.
	cs.undo(1)

	// (1) modified file reverts to its pre-mutation bytes.
	if got := readFileContent(t, root, "modify.go"); got != modifyBefore {
		t.Errorf("modify.go after /undo = %q, want pre-mutation %q", got, modifyBefore)
	}
	// (2) the deleted file is re-materialised from the snapshot's tree (the
	// worktree remove left it tracked in the index, so it stays tracked and
	// its blob is written back).
	if !fileExists(filepath.Join(root, "delete.go")) {
		t.Error("delete.go not restored; the snapshot tree contains it, so /undo must re-materialise it")
	} else if got := readFileContent(t, root, "delete.go"); got != deleteBefore {
		t.Errorf("delete.go after /undo = %q, want pre-mutation %q", got, deleteBefore)
	}
	// (3) the created file (untracked at the turn's start, so absent from the
	// snapshot's untracked baseline) is REMOVED — the issue's acceptance
	// criterion that undo restores created files.
	if fileExists(filepath.Join(root, "created.txt")) {
		t.Error("created.txt (turn-created) still present after /undo; it was not in the snapshot's untracked baseline, so /undo must remove it")
	}
	// (4) untracked user file untouched (it was in the snapshot's baseline).
	if got := readFileContent(t, root, "user_untracked.txt"); got != userBefore {
		t.Errorf("user_untracked.txt after /undo = %q, want untouched %q", got, userBefore)
	}
	// (5) .cortex/ untouched (gitignored, never in a snapshot).
	if got := readFileContent(t, root, filepath.Join(".cortex", "session.json")); got != cortexBefore {
		t.Errorf(".cortex/session.json after /undo = %q, want untouched %q", got, cortexBefore)
	}
	// (6) the transcript has the undo note (the durable half of the undo).
	last := cs.Request.Messages[len(cs.Request.Messages)-1]
	if last.Role != RoleUser || !strings.Contains(last.Content, "reverted") {
		t.Errorf("undo transcript note = %+v, want a RoleUser revert note", last)
	}
	// (7) the consumed ref is gone and the stack is empty.
	refs = gitCheckpointRefs(t, root, "refs/cortex/checkpoints/")
	if refs[wantRef] {
		t.Errorf("consumed ref %s still present after /undo", wantRef)
	}
	if !cs.checkpoints.empty() {
		t.Errorf("stack not empty after /undo: %d entries", cs.checkpoints.len())
	}
}

// TestUndoSkipsReadonlyTurns is the issue's depth spec: the undo stack's
// depth N maps to the Nth-most-recent turn that MUTATED files. One mutating
// turn, then a read-only turn on the now-dirty tree — the read-only turn
// must push no no-op snapshot — and /undo must revert the mutating turn
// (not the read-only one).
func TestUndoSkipsReadonlyTurns(t *testing.T) {
	root := t.TempDir()
	initUndoRepo(t, root)
	cs := undoTurnScriptedSession(t, root, nil)

	modifyBefore := readFileContent(t, root, "modify.go")

	// Turn 1: a mutating turn — the stack's depth-1 entry must be this
	// turn's pre-mutation snapshot.
	mutateScript := []*AgentResponse{
		editFileCallResp("c1", "modify.go", "package modify", "package modify_changed"),
		answerResp("done"),
	}
	cs.senderOverride = SenderFunc(multiResp(mutateScript))
	if _, err := cs.Turn(context.Background(), "mutate files"); err != nil {
		t.Fatalf("turn 1: %v", err)
	}
	if cs.checkpoints == nil || cs.checkpoints.len() != 1 {
		t.Fatalf("after the mutating turn the stack has %d entries, want 1", cs.checkpoints.len())
	}

	// Turn 2: a read-only turn on the now-dirty tree. It must record nothing:
	// its start-of-turn snapshot is dropped at the turn's end (no mutation),
	// so the stack depth stays 1 and /undo still targets turn 1.
	readScript := []*AgentResponse{
		readCallResp("c1", "modify.go"),
		answerResp("read it"),
	}
	cs.senderOverride = SenderFunc(multiResp(readScript))
	if _, err := cs.Turn(context.Background(), "read the file"); err != nil {
		t.Fatalf("turn 2: %v", err)
	}
	if cs.checkpoints.len() != 1 {
		t.Fatalf("the read-only turn pushed a snapshot: stack depth = %d, want 1 (only mutating turns count)", cs.checkpoints.len())
	}
	// The read-only turn's ref was dropped — only turn 1's ref remains.
	refs := gitCheckpointRefs(t, root, "refs/cortex/checkpoints/")
	if n := len(refs); n != 1 {
		t.Fatalf("after the read-only turn %d checkpoint refs remain, want 1 (turn 1's)", n)
	}

	// /undo reverts the MUTATING turn (depth 1), not the read-only one.
	cs.undo(1)
	if got := readFileContent(t, root, "modify.go"); got != modifyBefore {
		t.Errorf("modify.go after /undo = %q, want the mutating turn's pre-mutation state %q", got, modifyBefore)
	}
	if !cs.checkpoints.empty() {
		t.Errorf("stack not empty after /undo: %d entries", cs.checkpoints.len())
	}
}

// TestUndoStashAndIndexUntouched is the issue #111 second acceptance
// criterion, driven through the REAL turn path (cs.Turn) and the REAL /undo
// path (cs.undo): the user's stash list and index are unchanged across a
// snapshot + restore — even when a tracked file is deleted (a plain
// worktree removal that leaves the index entry intact). This is the
// invariant no `git stash push`/`checkout` combination satisfies; it is
// exactly why the mechanism is `git stash create` + worktree-only Restore.
func TestUndoStashAndIndexUntouched(t *testing.T) {
	root := t.TempDir()
	initUndoRepo(t, root)
	cs := undoTurnScriptedSession(t, root, nil)

	// Baseline index and stash list BEFORE any snapshot/restore.
	idxBefore := gitIndexSorted(t, root)
	stashBefore := gitStashList(t, root)
	if stashBefore != "" {
		t.Fatalf("pre-existing stash entries in fixture: %q", stashBefore)
	}
	// The baseline index is exactly the committed tracked set (three files).
	if want := []string{"delete.go", "keep.go", "modify.go"}; !sameStringsSlice(idxBefore, want) {
		t.Fatalf("fixture index = %v, want %v", idxBefore, want)
	}

	// The REAL turn path mutates: modify modify.go, delete delete.go (a plain
	// worktree removal — the index entry stays). The index is unchanged by
	// this mutation (edit_file / remove_path write to the worktree only).
	script := []*AgentResponse{
		editFileCallResp("c1", "modify.go", "package modify", "package modify_changed"),
		removePathCallResp("c2", "delete.go"),
		answerResp("done"),
	}
	cs.senderOverride = SenderFunc(multiResp(script))
	if _, err := cs.Turn(context.Background(), "mutate files"); err != nil {
		t.Fatalf("Turn: %v", err)
	}

	// (a) The mutation did not touch the index: delete.go is still tracked,
	// and modify.go's index entry is intact.
	if got := gitIndexSorted(t, root); !sameStringsSlice(got, idxBefore) {
		t.Fatalf("index changed by the turn's mutation: before=%v after=%v", idxBefore, got)
	}
	// (b) The stash list is unchanged by the turn — no stash entry created
	// (git stash create never writes refs/stash).
	if got := gitStashList(t, root); got != stashBefore {
		t.Errorf("git stash list changed across the turn: before=%q after=%q", stashBefore, got)
	}

	// The REAL /undo path: restore the turn's snapshot.
	cs.undo(1)

	// (1) The stash list is unchanged across snapshot + restore — no stash
	// entry was created.
	if got := gitStashList(t, root); got != stashBefore {
		t.Errorf("git stash list changed across snapshot+restore: before=%q after=%q", stashBefore, got)
	}
	// (2) The index is unchanged across the restore — Restore never runs
	// `git read-tree` / `checkout`. The deleted file (delete.go) was
	// re-materialised as TRACKED (the worktree remove left it in the index,
	// so it stays tracked; Restore writes its blob back without `git add`).
	if got := gitIndexSorted(t, root); !sameStringsSlice(got, idxBefore) {
		t.Errorf("index changed across snapshot+restore: before=%v after=%v", idxBefore, got)
	}
	// (3) The stash list stayed empty throughout (no stray stash entry).
	if s := gitStashList(t, root); s != "" {
		t.Errorf("a stash entry was created; got %q", s)
	}
}

// readFileContent returns the contents of root/path, failing the test on read
// error.
func readFileContent(t *testing.T, root, path string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(root, path))
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(b)
}

// gitStashList returns `git stash list` in root.
func gitStashList(t *testing.T, root string) string {
	t.Helper()
	out, err := gitCmdOutput(t, root, "stash", "list")
	if err != nil {
		t.Fatalf("git stash list in %s: %v", root, err)
	}
	return out
}

// gitIndexSorted returns the deduplicated, sorted index paths in root.
func gitIndexSorted(t *testing.T, root string) []string {
	t.Helper()
	out, err := gitCmdOutput(t, root, "ls-files", "--stage")
	if err != nil {
		t.Fatalf("git ls-files in %s: %v", root, err)
	}
	seen := map[string]bool{}
	var paths []string
	for _, line := range strings.Split(out, "\n") {
		if line = strings.TrimRight(line, "\r"); line == "" {
			continue
		}
		_, p, ok := strings.Cut(line, "\t")
		if ok && p != "" && !seen[p] {
			seen[p] = true
			paths = append(paths, p)
		}
	}
	// sort without importing "sort" (change_test.go's gitCmd helpers already
	// keep imports lean) — a tiny insertion sort is enough for a handful of
	// paths.
	for i := 1; i < len(paths); i++ {
		for j := i; j > 0 && paths[j] < paths[j-1]; j-- {
			paths[j-1], paths[j] = paths[j], paths[j-1]
		}
	}
	return paths
}

// multiResp returns a SenderFunc that plays each scripted response in order
// (repeating the last) so a single cs.Turn runs a multi-round script: the
// file-tool rounds run, then the answer round finalises.
func multiResp(script []*AgentResponse) SenderFunc {
	var i int
	return SenderFunc(func(_ context.Context, _ *AgentRequest) (*AgentResponse, bool, error) {
		r := script[i]
		if i < len(script)-1 {
			i++
		}
		return r, false, nil
	})
}

// sameStringsSlice reports whether two string slices are equal element-wise.
func sameStringsSlice(a, b []string) bool {
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

// editFileCallResp returns an AgentResponse carrying one edit_file tool call.
func editFileCallResp(id, path, old, new string) *AgentResponse {
	args, _ := json.Marshal(map[string]any{"path": path, "old_string": old, "new_string": new})
	return &AgentResponse{
		Choices: []Choice{{
			Message: Message{
				Role: "assistant",
				ToolCalls: []ToolCall{{
					ID:       id,
					Function: FunctionCall{Name: "edit_file", Arguments: string(args)},
				}},
			},
		}},
	}
}

// removePathCallResp returns an AgentResponse carrying one remove_path tool
// call.
func removePathCallResp(id, path string) *AgentResponse {
	args, _ := json.Marshal(map[string]any{"path": path})
	return &AgentResponse{
		Choices: []Choice{{
			Message: Message{
				Role: "assistant",
				ToolCalls: []ToolCall{{
					ID:       id,
					Function: FunctionCall{Name: "remove_path", Arguments: string(args)},
				}},
			},
		}},
	}
}
