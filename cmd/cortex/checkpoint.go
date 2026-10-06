package main

import (
	"fmt"
	"sort"
	"strings"

	"github.com/dereksantos/cortex/internal/checkpoint"
)

// checkpoint.go wires the internal/checkpoint package (issue #111, per-turn
// /undo) into the coder's turn loop: a snapshot stack scoped to cs.root(),
// keyed by session id, and pruned to the newest maxCheckpointRefs on append.
//
// A checkpoint is recorded at the START of EVERY coder turn (turn.go) — the
// snapshot names the tree the turn starts from (on a clean tracked tree that
// is HEAD's commit, because `git stash create` prints nothing there). It is
// COMMITTED to the undo stack only at the turn's END, and only if the turn
// actually changed the working tree — commitCheckpoint re-snapshots the
// tracked tree (again `git stash create`'s tree object, HEAD's tree on a
// clean tree) plus the untracked listing and compares with the pending
// entry: the checkpoint is kept iff the TREE hash differs or the untracked
// set differs. The comparison is on tree objects, never commit hashes:
// `git stash create` embeds author/committer timestamps in its commit, so
// two snapshots of identical content taken in different seconds are
// different commits (different hashes), while their trees are the same
// content-addressed object. That single
// signal catches edits made through any tool surface (write_file, edit_file,
// remove_path, a mutating bash, the `agent` subagent's own dispatcher) and
// drops a turn that only ran read-only bash. The turn-start cleanliness of
// the tree therefore tells us nothing (a clean tree is the normal case before
// a bad turn, and a dirty tree is the normal case for a read-only turn); what
// decides is whether the turn's end tree differs from its start tree. The
// stack's depth N therefore maps to the Nth-most-recent turn that changed
// files — the issue's spec.
//
// The stack is an in-memory slice of (snapshot hash, untracked baseline) pairs
// in TURN order (oldest first). Undo walks it newest-first: the top of the
// stack is the snapshot taken at the start of the most recent turn that
// mutated files — the state to restore to first. A ref
// (refs/cortex/checkpoints/<session>/<turn>) is the durable record; the stack
// is the session's own, pruned view. The untracked baseline (the set of
// untracked, non-ignored files present when the snapshot was taken) rides
// alongside the hash in the stack entry — it is what Restore needs to tell
// the files the turn created apart from pre-existing user files.
//
// The turn never fails because of a checkpoint — every git call here is
// guarded and its errors swallowed (the issue's "guarded and non-fatal"
// requirement). A checkpoint is cleared on /clear and at session end
// (clearCheckpoints).

// maxCheckpointRefs is the per-session ceiling on recorded checkpoint refs.
// A session's stack is pruned to the newest N on every append; N is generous
// enough that a long interactive session keeps its whole recent history
// undoable, and small enough that the hidden refs never bloat the repo.
const maxCheckpointRefs = 50

// checkpointEntry is one recorded turn checkpoint: the snapshot's tree hash
// and the untracked, non-ignored files present when the snapshot was taken.
// The baseline rides in the stack (not in the ref) — it is the small,
// session-scoped fact Restore needs to find the files the turn created.
type checkpointEntry struct {
	snap      string
	untracked []string
	// tree is the snapshot's TREE hash (`git rev-parse <snap>^{tree}`),
	// taken when the snapshot is recorded. commitCheckpoint compares the
	// turn-end tree to THIS — never the commit hash, because `git stash
	// create` embeds author/committer timestamps in its commit object: two
	// snapshots of identical content taken in different seconds are
	// different commits (and thus different hashes), while their trees are
	// byte-for-byte the same object. A read-only turn on a dirty tree that
	// crosses a second boundary would otherwise keep a no-op checkpoint and
	// skew the /undo depth.
	tree string
}

// checkpointStack is the session's in-memory undo history: entries in turn
// order (index 0 = the earliest turn that mutated files). Nil until the first
// mutating turn. Scoped to cs.root() — a /clear or a re-target at a different
// workspace resets it (the new conversation starts with nothing to undo, and
// a different worktree's refs are meaningless here).
type checkpointStack struct {
	entries []checkpointEntry
}

// push records a turn's checkpoint at the top of the stack. An entry whose
// snapshot is empty (no git, no repo, or a turn with nothing to snapshot) is
// a no-op.
func (s *checkpointStack) push(e checkpointEntry) {
	if e.snap == "" {
		return
	}
	s.entries = append(s.entries, e)
}

// newest returns the top-of-stack entry (the state to restore to first on
// /undo), or (zero, false) when there is nothing to undo to.
func (s *checkpointStack) newest() (checkpointEntry, bool) {
	if len(s.entries) == 0 {
		return checkpointEntry{}, false
	}
	return s.entries[len(s.entries)-1], true
}

// nth returns the Nth-most-recent entry, 1-based (1 = newest). It returns
// (zero, false) when N is out of range — no snapshot at that depth. /undo [N]
// looks up the Nth-most-recent turn that mutated files and restores it.
func (s *checkpointStack) nth(n int) (checkpointEntry, bool) {
	if n < 1 || n > len(s.entries) {
		return checkpointEntry{}, false
	}
	return s.entries[len(s.entries)-n], true
}

// truncateTo keeps only the first `keep` entries (the oldest), dropping the
// newest len(s)-keep entries. /undo(n) consumes the Nth-most-recent snapshot
// and the N-1 turns newer than it — `n` entries from the top — so it truncates
// the stack to len(s)-n, keeping the stack and the (deleted) refs in
// agreement: a consumed snapshot is never left in the stack pointing at a ref
// that no longer exists.
func (s *checkpointStack) truncateTo(keep int) {
	if keep < 0 {
		keep = 0
	}
	if keep < len(s.entries) {
		s.entries = s.entries[:keep]
	}
}

func (s *checkpointStack) empty() bool { return len(s.entries) == 0 }
func (s *checkpointStack) len() int    { return len(s.entries) }

// truncateToNewest keeps only the LAST `keep` entries (the NEWEST), dropping
// the oldest len(s)-keep. commitCheckpoint uses it after a push, mirroring
// checkpoint.Prune (which keeps the session's newest refs): after enough
// mutating turns the OLDEST snapshots age out and the most recent turn stays
// undoable. This is the inverse of truncateTo — /undo drops from the top
// (newest consumed first), pruning drops from the bottom (oldest consumed
// first) — so they are separate methods and neither is reused for the other.
func (s *checkpointStack) truncateToNewest(keep int) {
	if keep < 0 {
		keep = 0
	}
	if keep < len(s.entries) {
		s.entries = s.entries[len(s.entries)-keep:]
	}
}

func (s *checkpointStack) clear() { s.entries = nil }

// recordCheckpoint takes the turn's snapshot at the START of the turn
// (turn.go): it names the tree the turn starts from (HEAD's commit on a clean
// tree) and records the hidden ref for the turn's ordinal. The entry is NOT
// yet on the undo stack — commitCheckpoint at turn end decides whether the
// turn mutated files and pushes (or drops the ref) accordingly. It is GUARDED
// and NON-FATAL: a missing git, a non-repo root, or any git failure is
// swallowed — a checkpoint must never break a turn.
func (cs *CortexSession) recordCheckpoint() {
	if cs.SessionID == "" {
		return
	}
	dir := cs.root()
	if dir == "" || !checkpoint.Available(dir) {
		return
	}
	ref := checkpoint.RefFor(cs.SessionID, cs.underscoredTurn())
	snap, untracked, err := checkpoint.Snapshot(dir, ref)
	if err != nil {
		// Best-effort: a git failure is not a turn failure. Swallow it.
		return
	}
	// Record the snapshot's TREE hash alongside the commit: it is the
	// content-stable identity commitCheckpoint's keep-or-drop diff needs
	// (a stash commit's hash also carries its timestamps — see the field's
	// comment).
	tree := ""
	if out, err := gitCmdIn(dir, "rev-parse", snap+"^{tree}"); err == nil {
		tree = strings.TrimSpace(out)
	}
	cs.pending = checkpointEntry{snap: snap, untracked: untracked, tree: tree}
}

// commitCheckpoint runs at the END of the turn (turn.go's deferred cleanup):
// it commits the turn's pending snapshot to the undo stack — and drops the
// recorded ref — iff the turn actually changed the working tree. The decision
// is made here, at the turn's end, by RE-SNAPSHOTTING: the current tracked
// state (`git stash create`, falling back to HEAD when that prints nothing —
// the same fallback recordCheckpoint uses) and the current untracked listing
// are compared with the pending entry, and the checkpoint is kept when the
// tree hash differs or the untracked set differs. That is the ONE signal
// every mutation path is caught by — write_file, edit_file, remove_path, a
// mutating bash, and the `agent` subagent's own dispatcher all of them just
// change the tree — and a turn that only ran read-only bash (`go test`,
// `git status`) ends with an identical tree and an identical untracked set,
// so its ref is dropped and the stack is untouched. A read-only turn leaves
// the stack untouched, so the stack's depth always maps to the
// Nth-most-recent turn that changed files (the issue's spec). Guarded and
// non-fatal: a git failure swallows, the pending entry is always consumed.
func (cs *CortexSession) commitCheckpoint() {
	if cs.pending.snap == "" {
		return // no snapshot was recorded (no repo, no git, no session id)
	}
	// Consume the pending entry whether or not we keep it — a turn never
	// commits a snapshot it did not record.
	entry := cs.pending
	cs.pending = checkpointEntry{}
	dir := cs.root()
	if dir != "" && checkpoint.Available(dir) {
		// The turn-end re-snapshot: the TRACKED TREE the working tree holds
		// NOW (after the turn ran). Capture it as `git stash create` on a
		// dirty tree (the throwaway commit's tree object — the same tree
		// recordCheckpoint's snapshot named) or HEAD's tree on a clean one,
		// and compare against the turn-start TREE hash: `git stash create`
		// commits embed author/committer timestamps, so the commit hashes of
		// two identical trees taken in different seconds differ, but the
		// TREE objects are content-addressed and identical.
		tree := ""
		if out, err := gitCmdIn(dir, "stash", "create"); err == nil {
			if stash := strings.TrimSpace(out); stash != "" {
				// Dirty tree: the throwaway commit's tree object is the
				// turn-end tracked state.
				if t, err := gitCmdIn(dir, "rev-parse", stash+"^{tree}"); err == nil {
					tree = strings.TrimSpace(t)
				}
			}
		}
		if tree == "" {
			if out, err := gitCmdIn(dir, "rev-parse", "HEAD^{tree}"); err == nil {
				tree = strings.TrimSpace(out)
			}
		}
		if tree == "" || entry.tree == "" {
			// Git failed: we cannot tell whether the turn changed files. The
			// turn-start ref already exists; keep it (conservative — an
			// unexplained drop would lose a real undo).
			cs.keepCheckpointEntry(entry)
			return
		}
		if untracked, err := checkpoint.Untracked(dir); err == nil &&
			tree == entry.tree && equalUntracked(untracked, entry.untracked) {
			// The turn's end tree matches its start tree: drop the ref the
			// start of the turn recorded so the on-disk state never
			// outlives a turn that changed nothing.
			_, _ = gitCmdIn(dir, "update-ref", "-d", checkpoint.RefFor(cs.SessionID, cs.underscoredTurn()))
			return
		}
		cs.keepCheckpointEntry(entry)
		return
	}
	cs.keepCheckpointEntry(entry)
}

// keepCheckpointEntry pushes a kept entry onto the stack and prunes both the
// stack and the session's hidden refs to the newest maxCheckpointRefs — the
// shared tail of commitCheckpoint's keep paths.
func (cs *CortexSession) keepCheckpointEntry(entry checkpointEntry) {
	if cs.checkpoints == nil {
		cs.checkpoints = &checkpointStack{}
	}
	cs.checkpoints.push(entry)
	if dir := cs.root(); dir != "" && checkpoint.Available(dir) {
		checkpoint.Prune(dir, cs.SessionID, maxCheckpointRefs)
		cs.pruneStackTo(maxCheckpointRefs)
	}
}

// equalUntracked reports whether two untracked listings hold the same paths
// (order-independent; both come from git's sorted ls-files, but the set
// comparison keeps the check honest against ordering drift).
func equalUntracked(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	set := make(map[string]bool, len(a))
	for _, p := range a {
		set[p] = true
	}
	for _, p := range b {
		if !set[p] {
			return false
		}
	}
	return true
}

// pruneStackTo keeps only the newest keep entries of the stack (mirroring the
// ref Prune, so the stack and the hidden refs agree on what is undoable).
func (cs *CortexSession) pruneStackTo(keep int) {
	if cs.checkpoints == nil || keep <= 0 {
		return
	}
	if cs.checkpoints.len() > keep {
		cs.checkpoints.truncateToNewest(keep)
	}
}

// underscoredTurn returns the turn ordinal for this turn's checkpoint ref,
// zero-padded to four digits so lexicographic ref order == creation order
// (checkpoint.Prune relies on it). cs.turnNo is the 1-based ordinal stamped
// at the top of turn() before recordCheckpoint runs; 0 (a turn that stamped
// none — defensive) falls back to cs.turns+1.
func (cs *CortexSession) underscoredTurn() string {
	n := cs.turnNo
	if n <= 0 {
		n = cs.turns + 1
	}
	return fmt.Sprintf("%04d", n)
}

// clearCheckpoints empties the session's snapshot stack and (best-effort)
// deletes the session's hidden checkpoint refs from the git object store —
// the /clear and session-end cleanup the issue names. After a /clear the
// conversation is fresh, so the old conversation's undo history is gone; the
// refs are dropped too so the on-disk state never outlives the session it
// recorded. Non-fatal: a non-repo root or git failure is swallowed.
func (cs *CortexSession) clearCheckpoints() {
	if cs.checkpoints != nil {
		cs.checkpoints.clear()
		cs.checkpoints = nil
	}
	cs.pending = checkpointEntry{}
	if cs.SessionID == "" {
		return
	}
	dir := cs.root()
	if dir == "" || !checkpoint.Available(dir) {
		return
	}
	// Drop every ref under this session's prefix (the whole session, not just
	// what the in-memory stack still holds — the stack is pruned, the refs
	// were pruned to a larger ceiling).
	for ref := range cs.checkpointRefSet(dir) {
		_, _ = gitCmdIn(dir, "update-ref", "-d", ref)
	}
}

// checkpointRefSet returns the set of this session's hidden checkpoint refs
// still present in the git object store. Empty (nil) on any git failure.
func (cs *CortexSession) checkpointRefSet(dir string) map[string]bool {
	prefix := checkpoint.RefPrefix + checkpoint.Sanitize(cs.SessionID) + "/"
	out, err := gitCmdIn(dir, "for-each-ref", "--format=%(refname)", prefix)
	if err != nil {
		return nil
	}
	refs := make(map[string]bool)
	for _, line := range strings.Split(out, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			refs[line] = true
		}
	}
	return refs
}

// recordUndo appends a short note to the transcript recording that the user's
// /undo reverted a turn's edits — the durable, replayable half of the undo
// (the REPL's one-line console message is the transient half). It rides the
// SAME cs.Append + writeTranscript path every other message does, so it is
// redacted (issue #103), replayed on resume, and visible to the model in the
// next turn's context: the model learns its edits were reverted and must not
// act as though they still stand.
//
// Role is RoleUser, not RoleSystem: the note is harness-injected, turn-scoped
// content (cs.turnNo is 0 outside a turn, so writeTranscript stamps it
// unstamped — correct: it is a post-turn event, not part of a turn's span),
// and RoleSystem messages are reserved for the byte-stable system prompt
// whose position the prompt cache depends on. It is called ONLY after a
// successful /undo, so a failed or declined undo records nothing.
func (cs *CortexSession) recordUndo(what string) {
	cs.Append(Message{Role: RoleUser, Content: undoTranscriptNote(what)})
}

// undoTranscriptNote renders the short transcript note recordUndo appends.
// What is a one-line description of what was reverted (e.g. "the edits from
// your last turn" or a file list); "" (nothing described) still yields a
// complete sentence.
func undoTranscriptNote(what string) string {
	base := "Your /undo reverted your most recent turn's file changes."
	if what != "" {
		base = "Your /undo reverted " + what + "."
	}
	return "Harness note: " + base + " The files are back to the state before that turn; do not act as though those changes still stand."
}

// undo is the /undo [N] command body (issue #111): restore the Nth-most-recent
// snapshot for the current session (1 = newest) to the working tree, report
// the files changed, record the transcript note (recordUndo), and drop the
// consumed checkpoint refs. N < 1 defaults to 1; N out of range prints a one-
// line message and changes nothing.
//
// It is guarded end to end: not in a git worktree (or no git) prints the
// disabled one-liner; an empty stack (no mutating turn yet) prints "nothing to
// undo"; a restore failure is reported without corrupting state. The index
// and the stash list are never touched (checkpoint.Restore's invariant) — a
// deleted-and-staged file comes back as untracked, `.cortex/` and the user's
// pre-existing untracked files are left alone, and the files the turn CREATED
// are removed (they are the untracked paths absent from the snapshot's
// baseline).
func (cs *CortexSession) undo(n int) {
	if n < 1 {
		n = 1
	}
	dir := cs.root()
	if dir == "" || !checkpoint.Available(dir) {
		fmt.Println(withColor("undo unavailable: not in a git repository", gray))
		return
	}
	if cs.checkpoints == nil || cs.checkpoints.empty() {
		fmt.Println(withColor("nothing to undo (no file changes yet)", gray))
		return
	}
	entry, ok := cs.checkpoints.nth(n)
	if !ok {
		fmt.Printf("nothing to undo at depth %d (only %d checkpoint%s recorded)\n",
			n, cs.checkpoints.len(), plural(cs.checkpoints.len(), "", "s"))
		return
	}
	// Restore the Nth-most-recent snapshot to the working tree: write the
	// snapshot's tracked files back (honouring modes), re-materialise a
	// turn-deleted file, and remove the files the turn created (the untracked
	// paths absent from the snapshot's baseline).
	changed, err := checkpoint.Restore(dir, entry.snap, entry.untracked)
	if err != nil {
		fmt.Println("undo failed: " + err.Error())
		return
	}
	// Drop the consumed refs FIRST, then record the transcript note. A
	// consumed snapshot is never left in the stack pointing at a ref that no
	// longer exists: the truncate below matches the deleted ref set exactly.
	for ref := range cs.consumedCheckpointRefs(n) {
		_, _ = gitCmdIn(dir, "update-ref", "-d", ref)
	}
	cs.checkpoints.truncateTo(cs.checkpoints.len() - n)
	cs.recordUndo(fmt.Sprintf("the changes from %d turn(s) ago (%d file%s)", n, len(changed), plural(len(changed), "", "s")))
	// Print the files the restore changed, one per line, plain text (the
	// REPL's plain style; no icons, color only).
	if len(changed) == 0 {
		fmt.Println("undone: working tree already matched the restored snapshot")
		return
	}
	fmt.Printf("undone %d file%s (restored to %d turn%s ago):\n", len(changed), plural(len(changed), "", "s"), n, plural(n, "", "s"))
	for _, f := range changed {
		fmt.Println("  " + f)
	}
}

// consumedCheckpointRefs returns the set of hidden checkpoint refs under this
// session's prefix that /undo (at depth n) just consumed: the Nth-most-recent
// snapshot's ref plus the refs of the N-1 turns newer than it (the restore
// subsumed them). Each entry is the full refname. A snapshot at depth k maps
// to the ref recorded by the turn that took it, so the consumed set is the
// newest n entries of the session's ref order.
func (cs *CortexSession) consumedCheckpointRefs(n int) map[string]bool {
	// Enumerate the session's refs that still exist and keep the newest n of
	// them (lexicographic order == creation order within a session, the same
	// ordering checkpoint.listCheckpointRefs relies on).
	all := cs.checkpointRefSet(cs.root())
	var refs []string
	for r := range all {
		refs = append(refs, r)
	}
	sort.Strings(refs) // oldest-first (zero-padded ordinals)
	if len(refs) > n {
		refs = refs[len(refs)-n:] // keep the newest n
	}
	out := make(map[string]bool, len(refs))
	for _, r := range refs {
		out[r] = true
	}
	return out
}

// plural returns "" for 1 and suffix for everything else (the tiny
// "%d file%s" pluraliser undo uses, avoiding fmt plurals).
func plural(count int, _, suffix string) string {
	if count == 1 {
		return ""
	}
	return suffix
}
