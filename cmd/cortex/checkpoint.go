package main

import (
	"fmt"
	"sort"
	"strings"

	"github.com/dereksantos/cortex/internal/checkpoint"
)

// checkpoint.go wires the internal/checkpoint package (issue #111, per-turn
// /undo) into the coder's turn loop: a snapshot stack scoped to cs.root(),
// keyed by session id, taken at the START of every turn, pruned on append,
// and cleared on /clear and session end. The turn never fails because of a
// checkpoint — every git call here is guarded and its errors swallowed — and
// only a turn that actually changed the tracked tree records a ref
// (checkpoint.Snapshot returns "" on a clean tree, which the stack drops).
//
// The stack is an in-memory slice of the snapshot hashes in TURN order
// (oldest first). Undo walks it newest-first: the top of the stack is the
// snapshot taken at the start of the most recent turn that mutated files —
// the state to restore to first. A ref (refs/cortex/checkpoints/<session>/
// <turn>) is the durable record; the stack is the session's own, pruned view.

// maxCheckpointRefs is the per-session ceiling on recorded checkpoint refs.
// A session's stack is pruned to the newest N on every append; N is generous
// enough that a long interactive session keeps its whole recent history
// undoable, and small enough that the hidden refs never bloat the repo.
const maxCheckpointRefs = 50

// checkpointStack is the session's in-memory undo history: snapshot hashes in
// turn order (index 0 = the earliest turn that mutated files). Nil until the
// first turn that mutated the tracked tree. Scoped to cs.root() — a /clear or
// a re-target at a different workspace resets it (the new conversation starts
// with nothing to undo, and a different worktree's refs are meaningless here).
type checkpointStack struct {
	snapshots []string
}

// push records a turn's snapshot (a non-empty hash) at the top of the stack.
// An empty hash (a clean tracked tree — the turn recorded nothing) is a
// no-op, which is the "only turns that mutate record checkpoints" clause.
func (s *checkpointStack) push(snap string) {
	if snap == "" {
		return
	}
	s.snapshots = append(s.snapshots, snap)
}

// newest returns the top-of-stack snapshot (the state to restore to first on
// /undo), or "" when there is nothing to undo to.
func (s *checkpointStack) newest() string {
	if len(s.snapshots) == 0 {
		return ""
	}
	return s.snapshots[len(s.snapshots)-1]
}

// nth returns the Nth-most-recent snapshot, 1-based (1 = newest). It returns
// ("", false) when N is out of range — no snapshot at that depth. /undo [N]
// looks up the Nth-most-recent turn that mutated files and restores it.
func (s *checkpointStack) nth(n int) (string, bool) {
	if n < 1 || n > len(s.snapshots) {
		return "", false
	}
	return s.snapshots[len(s.snapshots)-n], true
}

// truncateTo keeps only the first `keep` entries (the oldest), dropping the
// newest len(s)-keep entries. /undo(n) consumes the Nth-most-recent snapshot
// and the N-1 turns newer than it — `n` entries from the top — so it truncates
// the stack to len(s)-n, keeping the stack and the (deleted) refs in agreement:
// a consumed snapshot is never left in the stack pointing at a ref that no
// longer exists.
func (s *checkpointStack) truncateTo(keep int) {
	if keep < 0 {
		keep = 0
	}
	if keep < len(s.snapshots) {
		s.snapshots = s.snapshots[:keep]
	}
}

func (s *checkpointStack) empty() bool { return len(s.snapshots) == 0 }

func (s *checkpointStack) clear() { s.snapshots = nil }

// recordCheckpoint takes the turn's snapshot at cs.root() and, if it records
// a non-empty ref, pushes it onto the session's stack and prunes the on-disk
// refs to maxCheckpointRefs. It is GUARDED and NON-FATAL: a missing git, a
// non-repo root, or any git failure is swallowed — a checkpoint must never
// break a turn (the issue's "guarded and non-fatal" requirement). It also
// records nothing when the tracked tree is clean (Snapshot returns ""), so a
// read-only turn leaves the stack and the ref space untouched.
//
// Called at the START of every coder turn (turn.go), before the turn's user
// message is appended, so the stack's top always names the state the previous
// mutating turn left behind — the correct target for the first /undo.
func (cs *CortexSession) recordCheckpoint() {
	if cs.SessionID == "" {
		return
	}
	dir := cs.root()
	if dir == "" || !checkpoint.Available(dir) {
		return
	}
	snap, err := checkpoint.Snapshot(dir, checkpoint.RefFor(cs.SessionID, cs.underscoredTurn()))
	if err != nil {
		// Best-effort: a git failure is not a turn failure. Swallow it.
		return
	}
	if snap == "" {
		return // clean tracked tree: nothing to record (no-op turn)
	}
	if cs.checkpoints == nil {
		cs.checkpoints = &checkpointStack{}
	}
	cs.checkpoints.push(snap)
	// Prune the on-disk refs to the newest maxCheckpointRefs. The in-memory
	// stack is pruned to match so a /undo never walks a ref we just dropped.
	checkpoint.Prune(dir, maxCheckpointRefs)
	cs.pruneStackTo(maxCheckpointRefs)
}

// pruneStackTo keeps only the newest keep entries of the stack (mirroring the
// ref Prune, so the stack and the hidden refs agree on what is undoable).
func (cs *CortexSession) pruneStackTo(keep int) {
	if cs.checkpoints == nil || keep <= 0 {
		return
	}
	n := len(cs.checkpoints.snapshots)
	if n > keep {
		cs.checkpoints.snapshots = cs.checkpoints.snapshots[n-keep:]
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
	s := ""
	if n < 10 {
		s = "000" + string(rune('0'+n))
	} else if n < 100 {
		s = "00" + itoa(n)
	} else if n < 1000 {
		s = "0" + itoa(n)
	} else {
		s = itoa(n)
	}
	return s
}

// itoa is a tiny unsigned-int-to-string helper (strconv would import the
// whole package for one call site; this keeps checkpoint.go stdlib-minimal).
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
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
	if cs.SessionID == "" {
		return
	}
	dir := cs.root()
	if dir == "" || !checkpoint.Available(dir) {
		return
	}
	// Drop every ref under this session's prefix (the whole session, not just
	// what the in-memory stack still holds — the stack is pruned, the refs
	// were pruned to a larger ceiling, and a resumed session may have refs
	// its stack does not yet know about).
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
// successful /undo (the /undo command in step 4), so a failed or declined
// undo records nothing.
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
// snapshot for the current session (1 = newest) to the working tree, report the
// files changed, record the transcript note (recordUndo), and drop the
// consumed checkpoint refs. N < 1 defaults to 1; N out of range prints a one-
// line message and changes nothing.
//
// It is guarded end to end: not in a git worktree (or no git) prints the
// disabled one-liner; an empty stack (no mutating turn yet) prints "nothing to
// undo"; a restore failure is reported without corrupting state. The index and
// the stash list are never touched (checkpoint.Restore's invariant) — a
// deleted-and-staged file comes back as untracked, `.cortex/` and untracked
// files are left alone.
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
	snap, ok := cs.checkpoints.nth(n)
	if !ok {
		fmt.Printf("nothing to undo at depth %d (only %d checkpoint%s recorded)\n",
			n, len(cs.checkpoints.snapshots), plural(len(cs.checkpoints.snapshots), "", "s"))
		return
	}
	// Restore the Nth-most-recent snapshot to the working tree. This writes
	// the snapshot's full committed state back: a file the turn modified or
	// deleted is reverted; a file the turn merely created untracked is not in
	// the snapshot tree and is left as-is (see checkpoint.Restore).
	changed, err := checkpoint.Restore(dir, snap)
	if err != nil {
		fmt.Println("undo failed: " + err.Error())
		return
	}
	// Drop the consumed refs FIRST, then record the transcript note. The order
	// matters for step 5's resume: the note is appended to the transcript with
	// Turn: cs.turnNo (the last turn's ordinal, set by turn() and only cleared
	// on its exit), and a resumed session rehydrates the stack from refs in
	// turnNo order. Deleting the consumed refs BEFORE the note is written keeps
	// the transcript's append order and the ref set in agreement — the note's
	// turn ordinal (the last turn) comes after every remaining ref's ordinal,
	// so resume replays it after the restored snapshot rather than attaching it
	// to a ref that no longer exists. Deleting a ref does not delete the commit
	// object, so a misfire is recoverable via the reflog or the older refs.
	for ref := range cs.consumedCheckpointRefs(n) {
		_, _ = gitCmdIn(dir, "update-ref", "-d", ref)
	}
	// Truncate the in-memory stack to match the deleted refs: drop the Nth
	// snapshot and the N-1 turns newer than it (`n` entries from the top), so
	// the stack never keeps a snapshot whose ref is gone.
	cs.checkpoints.truncateTo(len(cs.checkpoints.snapshots) - n)
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
// newest n entries of the stack's ref order.
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
