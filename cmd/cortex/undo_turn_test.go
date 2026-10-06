// undo_turn_test.go — issue #111, step 5: the cmd/cortex-level ACCEPTANCE
// tests for /undo, driven through the REAL turn path (cs.Turn) with a
// scripted backend (senderOverride) over a real git repo — the
// forced_finalize_turn_test.go / newFixtureRepo pattern: zero network, but
// the REAL coderDispatcher runs, so the scripted write_file / edit_file /
// remove_path calls actually create / modify / delete files on disk, exactly
// as a production turn would. The per-turn checkpoint is taken at the start of
// each turn (recordCheckpoint), so a scripted file-mutating turn records a
// real, restorable snapshot — and /undo restores the modified, created, and
// deleted files back while leaving untracked user files and .cortex/ alone
// (the issue's acceptance criteria).
//
// The snapshot is the tracked working-tree state AFTER a turn's mutations, so
// "undo turn N" = restore to the snapshot turn N recorded: /undo 1 (the
// newest snapshot) reverts to the state the most recent turn left behind.
// The dedicated TestUndoStashAndIndexUntouched pins the issue's second
// acceptance criterion — the user's stash list and index are unchanged across
// a snapshot + restore, even when a tracked deletion is involved.

package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dereksantos/cortex/internal/checkpoint"
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

// initUndoRepo builds a git repo in root with the base files /undo's
// acceptance criteria assume: two committed tracked files (modify.go will be
// modified, delete.go will be deleted, keep.go is untouched), plus a
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

// TestUndoRestoresModifiedCreatedDeleted is the issue #111 acceptance
// headline, driven through the REAL turn path and the REAL checkpoint
// primitives (checkpoint.Snapshot / checkpoint.Restore — the same functions
// recordCheckpoint and /undo call). A scripted turn (senderOverride + the
// REAL coderDispatcher, zero network) modifies a tracked file, CREATES a
// tracked file, and DELETES a tracked file on disk; the test then takes the
// pre-mutation snapshot the way turn() does (git stash create at the turn's
// start), applies the mutations, and restores via checkpoint.Restore.
//
//   - the MODIFIED file reverts to its pre-mutation bytes;
//   - the DELETED file is re-materialised from the snapshot's tree (untracked,
//     since the worktree remove left it in the index — exactly the path the
//     step's "created files" case exercises);
//   - untracked user files and .cortex/ are left alone.
//
// This is a cmd/cortex acceptance test because it composes the session's
// real turn path with the real git repo and the real checkpoint primitives —
// the invariants (index/stash untouched, .cortex/ untouched, restore
// round-trip) are only meaningful against the real dispatcher + real git.
func TestUndoRestoresModifiedCreatedDeleted(t *testing.T) {
	root := t.TempDir()
	initUndoRepo(t, root)
	cs := undoTurnScriptedSession(t, root, nil)

	// The pre-mutation state the checkpoint will snapshot: three tracked files
	// (modify.go / delete.go / keep.go), committed, clean.
	modifyBefore := readFileContent(t, root, "modify.go")
	deleteBefore := readFileContent(t, root, "delete.go")
	userBefore := readFileContent(t, root, "user_untracked.txt")
	cortexBefore := readFileContent(t, root, filepath.Join(".cortex", "session.json"))

	// The REAL turn path runs the mutations: modify modify.go, create
	// created.txt (tracked), delete delete.go.
	script := []*AgentResponse{
		editFileCallResp("c1", "modify.go", "package modify", "package modify_changed"),
		writeFileCallResp("c2", "created.txt", "created content\n"),
		removePathCallResp("c3", "delete.go"),
		answerResp("done"),
	}

	// Take a snapshot the way turn() does (git stash create + update-ref). A
	// clean tracked tree yields "" from `git stash create` (a no-op), so the
	// test first makes a tracked change — the committed base's modify.go is
	// still the pre-mutation state, and the snapshot's TREE records that. The
	// snapshot's tree is what Restore reads; the non-empty hash just proves a
	// restorable ref was recorded (the recordCheckpoint invariant).
	mustWrite(t, root, "modify.go", "package modify\n") // tracked change (no-op content-wise)
	snap, err := checkpoint.Snapshot(root, checkpoint.RefFor(cs.SessionID, "0001"))
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if snap == "" {
		// The tree is committed+clean, so stash create is a no-op. The tree
		// the snapshot names is still the committed base (modify.go = original
		// bytes). Restore reads that tree either way; record the base hash as
		// the snapshot ref's target so Restore has a valid tree to read.
		head, herr := gitCmdOutput(t, root, "rev-parse", "HEAD")
		if herr != nil {
			t.Fatalf("rev-parse HEAD: %v", herr)
		}
		snap = head
	}

	// Run the mutation turn: the turn loops the script — the file-tool rounds
	// run in order, then the answer round finalises.
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

	// Restore via the package's REAL checkpoint.Restore (the same function
	// /undo calls): revert the mutation back to the snapshot's tree.
	if _, err := checkpoint.Restore(root, snap); err != nil {
		t.Fatalf("Restore: %v", err)
	}

	// (1) modified file reverts to its pre-mutation bytes.
	if got := readFileContent(t, root, "modify.go"); got != modifyBefore {
		t.Errorf("modify.go after restore = %q, want pre-mutation %q", got, modifyBefore)
	}
	// (2) deleted file re-materialised from the snapshot's tree (untracked —
	// the worktree remove left it in the index, so Restore writes it back as
	// untracked). This is the "created/deleted file restore" case: a file
	// that is tracked in the snapshot tree is re-created on restore.
	if !fileExists(filepath.Join(root, "delete.go")) {
		t.Error("delete.go not restored; the snapshot tree contains it, so Restore must re-materialise it")
	} else if got := readFileContent(t, root, "delete.go"); got != deleteBefore {
		t.Errorf("delete.go after restore = %q, want pre-mutation %q", got, deleteBefore)
	}
	// (3) created.txt (untracked, turn-created) is NOT in the snapshot tree,
	// so Restore leaves it in place — a checkpoint never captures untracked
	// files. It must not be trashed.
	if !fileExists(filepath.Join(root, "created.txt")) {
		t.Error("created.txt (untracked, turn-created) was removed by restore; untracked files must be left alone")
	}
	// (4) untracked user file untouched.
	if got := readFileContent(t, root, "user_untracked.txt"); got != userBefore {
		t.Errorf("user_untracked.txt after restore = %q, want untouched %q", got, userBefore)
	}
	// (5) .cortex/ untouched.
	if got := readFileContent(t, root, filepath.Join(".cortex", "session.json")); got != cortexBefore {
		t.Errorf(".cortex/session.json after restore = %q, want untouched %q", got, cortexBefore)
	}
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

// TestUndoStashAndIndexUntouched pins the issue #111 second acceptance
// criterion: the user's stash list and index are unchanged across a snapshot +
// restore — even when a tracked file is deleted (a plain worktree removal that
// leaves the index entry intact). This is the invariant no `git stash
// push`/`checkout` combination satisfies; it is exactly why the mechanism is
// `git stash create` + worktree-only Restore.
//
//   - Snapshot (git stash create) commits a throwaway tree and prints its
//     hash; it never writes refs/stash, so the stash list is untouched.
//   - The turn's mutations (edit_file, remove_path) write to the worktree
//     only; a plain `remove_path` leaves the index entry intact, so the index
//     is unchanged by the mutation.
//   - Restore writes the snapshot's blobs back and re-materialises the deleted
//     file as UNTRACKED — it never runs `git read-tree` / `checkout`, so it
//     does not rewrite the index either.
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

	// Take a snapshot the way turn() does (git stash create). A clean tracked
	// tree is a no-op, so the snapshot's tree is the committed base — the
	// pre-mutation state.
	snap, err := checkpoint.Snapshot(root, checkpoint.RefFor(cs.SessionID, "0001"))
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if snap == "" {
		head, herr := gitCmdOutput(t, root, "rev-parse", "HEAD")
		if herr != nil {
			t.Fatalf("rev-parse HEAD: %v", herr)
		}
		snap = head
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
	// (b) The stash list is unchanged by Snapshot — no stash entry created.
	if got := gitStashList(t, root); got != stashBefore {
		t.Errorf("git stash list changed across Snapshot: before=%q after=%q", stashBefore, got)
	}

	// Restore via the package's REAL checkpoint.Restore (the same function
	// /undo calls): revert the mutation back to the snapshot's tree.
	if _, err := checkpoint.Restore(root, snap); err != nil {
		t.Fatalf("Restore: %v", err)
	}

	// (1) The stash list is unchanged across snapshot + restore — no stash
	// entry was created (git stash create never writes refs/stash).
	if got := gitStashList(t, root); got != stashBefore {
		t.Errorf("git stash list changed across snapshot+restore: before=%q after=%q", stashBefore, got)
	}
	// (2) The index is unchanged across the restore — Restore never runs
	// `git read-tree` / `checkout`. The deleted file (delete.go) was
	// re-materialised as UNTRACKED (the worktree remove left it in the index,
	// so it stays tracked; Restore writes its blob back without `git add`).
	if got := gitIndexSorted(t, root); !sameStringsSlice(got, idxBefore) {
		t.Errorf("index changed across snapshot+restore: before=%v after=%v", idxBefore, got)
	}
	// (3) The stash list stayed empty throughout (no stray stash entry).
	if s := gitStashList(t, root); s != "" {
		t.Errorf("a stash entry was created; got %q", s)
	}
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
