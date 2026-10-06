// Package checkpoint snapshots the working tree before a turn that mutates
// files and restores it on `/undo` (issue #111).
//
// The mechanism is deliberately the one the issue names — `git stash create`
// + `git update-ref` — because it satisfies the two invariants the issue's
// acceptance criteria demand and no `git stash push`/`checkout` combination
// does:
//
//  1. It never touches the user's stash list or the index. `git stash create`
//     commits the current TRACKED working-tree state to a throwaway commit
//     and prints its hash; it neither writes to `refs/stash` nor rewrites the
//     index. `git update-ref` records that commit under a hidden
//     `refs/cortex/checkpoints/…` ref. A file the turn deleted and staged
//     stays staged in the index throughout — the ref only names a tree.
//
//  2. On a clean tracked tree `git stash create` prints an EMPTY line — it is
//     a no-op. Only a turn that actually changed the tracked tree yields a
//     non-empty hash, so only such turns record a restorable checkpoint. That
//     is exactly the "at the start of each turn that mutates files" clause:
//     the snapshot is taken at turn start and a turn that never mutates
//     records nothing.
//
// Restore is worktree-only: it writes each blob in the snapshot tree back to
// its working-tree path and removes working-tree files that the index still
// tracks but the snapshot tree no longer does (a tracked file deleted since
// the snapshot). It never runs `git read-tree` / `checkout` — both of those
// rewrite the index, which would violate invariant 1 the moment the user had
// staged anything. Writing a blob to a path that the turn deleted-and-staged
// re-materialises the file as UNTRACKED (the staged deletion stays put), so
// the index is left byte-for-byte. Untracked files the turn created, and
// `.cortex/` (gitignored, #119), are never in the snapshot tree and are
// never touched.
package checkpoint

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

// RefPrefix is the hidden ref namespace under which per-turn checkpoints
// live. A checkpoint for session s, turn n is at <RefPrefix><s>/<n>.
const RefPrefix = "refs/cortex/checkpoints/"

// RefFor returns the full hidden ref for a session's turn checkpoint. The
// session id and turn ordinal are sanitised (path separators and `..`
// refused, non-[A-Za-z0-9._-] dropped) so a hostile session id cannot point
// the ref at an arbitrary location under refs/.
func RefFor(session, turn string) string {
	return RefPrefix + Sanitize(session) + "/" + Sanitize(turn)
}

// Sanitize is the ref-safe-token reduction RefFor applies to each of the
// session id and turn ordinal. It is exported so the REPL can build the same
// per-session ref prefix RefFor does (e.g. to enumerate or delete a session's
// checkpoint refs on /clear) without re-deriving the sanitisation — one
// place owns the rule.
func Sanitize(s string) string {
	var b strings.Builder
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '.' || r == '_' || r == '-' {
			b.WriteRune(r)
		}
	}
	out := strings.Trim(b.String(), ".")
	if out == "" {
		return "x"
	}
	return out
}

// Available reports whether a git binary is on PATH and dir is inside a git
// worktree. Outside a repo (or with no git) every other call is a no-op
// guard, and /undo prints its one-line disabled message.
func Available(dir string) bool {
	if _, err := exec.LookPath("git"); err != nil {
		return false
	}
	if err := gitIn(dir, "rev-parse", "--is-inside-work-tree"); err != nil {
		return false
	}
	return true
}

// Snapshot commits dir's current tracked working-tree state to a throwaway
// commit via `git stash create` and records it under ref, returning the
// commit hash. It returns "" (and no error) when the tracked tree is clean —
// `git stash create` printed nothing — which the caller treats as "this turn
// has nothing to undo to yet". It never touches the index or the stash list.
func Snapshot(dir, ref string) (string, error) {
	snap, err := gitOut(dir, "stash", "create")
	if err != nil {
		return "", fmt.Errorf("git stash create: %w", err)
	}
	if strings.TrimSpace(snap) == "" {
		return "", nil // clean tracked tree: nothing to record
	}
	if _, err := gitOut(dir, "update-ref", ref, snap); err != nil {
		return "", fmt.Errorf("git update-ref %s: %w", ref, err)
	}
	return snap, nil
}

// Restore writes the snapshot tree back to dir's working tree and returns the
// list of files it changed (sorted, relative to dir). It never touches the
// index or the stash list:
//
//   - every blob in the snapshot tree is written to its working-tree path
//     (re-materialising a file the turn deleted-and-staged as untracked);
//   - every working-tree file that the index tracks but the snapshot tree
//     does not is removed (a tracked file the turn deleted without staging);
//
// Files the turn created untracked (not in the snapshot tree) and `.cortex/`
// (gitignored) are left alone.
func Restore(dir, snap string) ([]string, error) {
	tree, err := treeEntries(dir, snap)
	if err != nil {
		return nil, fmt.Errorf("read snapshot tree %s: %w", snap, err)
	}
	snapPaths := make(map[string]bool, len(tree))
	for _, e := range tree {
		snapPaths[e.Path] = true
	}

	changed := make([]string, 0, len(tree))
	for _, e := range tree {
		if e.Type != "blob" {
			continue // subtrees / gitlinks: a file checkpoint is per-blob
		}
		if err := writeBlob(dir, e.Path, e.Blob); err != nil {
			return changed, fmt.Errorf("restore %s: %w", e.Path, err)
		}
		changed = append(changed, e.Path)
	}

	// Remove tracked-but-deleted files: present in the index, absent from the
	// snapshot tree. The index is the committed+staged set — a file the turn
	// `git rm`-ed is gone from it, so it is NOT in this set and was restored
	// as untracked above instead (re-materialising it is the correct undo).
	idxPaths, err := indexPaths(dir)
	if err != nil {
		return changed, fmt.Errorf("read index: %w", err)
	}
	for _, p := range idxPaths {
		if snapPaths[p] {
			continue
		}
		if err := os.Remove(filepath.Join(dir, filepath.FromSlash(p))); err != nil && !os.IsNotExist(err) {
			return changed, fmt.Errorf("remove deleted %s: %w", p, err)
		}
		changed = append(changed, p)
	}

	sort.Strings(changed)
	return changed, nil
}

// Prune deletes the oldest checkpoint refs under RefPrefix, keeping the last
// `keep` (newest first). It is best-effort: a missing git or a missing ref
// neither errors nor panics — checkpoint bookkeeping must never break a turn.
func Prune(dir string, keep int) {
	if keep < 0 {
		keep = 0
	}
	refs, err := listCheckpointRefs(dir)
	if err != nil || len(refs) <= keep {
		return
	}
	// refs is newest-first; the tail is the oldest and what we drop.
	for _, ref := range refs[keep:] {
		_, _ = gitOut(dir, "update-ref", "-d", ref)
	}
}

// listCheckpointRefs returns the checkpoint refs under RefPrefix, newest
// first. `git for-each-ref` lists names lexicographically; a session's turns
// are named <session>/<turn> with zero-padded ordinals, so within a session
// the lexicographic order equals creation order. Reversed, the largest name
// (newest) leads. Prune drops the tail of this single ordered list; callers
// keep `keep` large enough that cross-session ordering is not the axis.
func listCheckpointRefs(dir string) ([]string, error) {
	out, err := gitOut(dir, "for-each-ref", "--format=%(refname)", RefPrefix)
	if err != nil {
		return nil, err
	}
	var refs []string
	for _, line := range strings.Split(out, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			refs = append(refs, line)
		}
	}
	for i, j := 0, len(refs)-1; i < j; i, j = i+1, j-1 {
		refs[i], refs[j] = refs[j], refs[i]
	}
	return refs, nil
}

type treeEntry struct {
	Mode string
	Type string
	Blob string
	Path string
}

// treeEntries lists the snapshot commit's tree as (mode, type, blob, path)
// rows. Each row is "<mode> <type> <object> TAB <path>"; we split on the FIRST
// tab so a path containing spaces survives.
func treeEntries(dir, snap string) ([]treeEntry, error) {
	out, err := gitOut(dir, "ls-tree", "-r", snap)
	if err != nil {
		return nil, err
	}
	var entries []treeEntry
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimRight(line, "\r")
		if line == "" {
			continue
		}
		meta, path, ok := strings.Cut(line, "\t")
		if !ok {
			continue
		}
		fields := strings.Fields(meta)
		if len(fields) != 3 {
			continue
		}
		entries = append(entries, treeEntry{Mode: fields[0], Type: fields[1], Blob: fields[2], Path: path})
	}
	return entries, nil
}

// writeBlob materialises blob at dir/path (creating parent dirs), overwriting
// any existing file. It is the worktree-only half of Restore: it writes to the
// filesystem and never touches the index. The blob's bytes are fetched raw
// (gitBlob) — NOT via gitOut's TrimSpace — because a file's trailing newline
// is content, not formatting.
func writeBlob(dir, path, blob string) error {
	dst := filepath.Join(dir, filepath.FromSlash(path))
	if d := filepath.Dir(dst); d != "" && d != dir {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return fmt.Errorf("mkdir %s: %w", d, err)
		}
	}
	out, err := gitBlob(dir, blob)
	if err != nil {
		return fmt.Errorf("git cat-file blob %s: %w", blob, err)
	}
	if err := os.WriteFile(dst, out, 0o644); err != nil {
		return fmt.Errorf("write %s: %w", dst, err)
	}
	return nil
}

// indexPaths returns the deduplicated set of paths the index (committed+
// staged) tracks. A file the turn `git rm`-ed is absent here — that is exactly
// why Restore re-materialises it as untracked rather than removing it.
func indexPaths(dir string) ([]string, error) {
	out, err := gitOut(dir, "ls-files", "--stage")
	if err != nil {
		return nil, err
	}
	seen := make(map[string]bool)
	var paths []string
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimRight(line, "\r")
		if line == "" {
			continue
		}
		// "<mode> <object> <stage> TAB <path>"
		_, path, ok := strings.Cut(line, "\t")
		if !ok || path == "" {
			continue
		}
		if !seen[path] {
			seen[path] = true
			paths = append(paths, path)
		}
	}
	return paths, nil
}

// gitIn runs `git <args>` in dir and returns an error on non-zero exit.
// Output is discarded — callers that need it use gitOut.
func gitIn(dir string, args ...string) error {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return nil
}

// gitOut runs `git <args>` in dir and returns the trimmed stdout, or an error
// carrying the raw error plus whatever stdout held on non-zero exit.
func gitOut(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return strings.TrimSpace(string(out)), fmt.Errorf("git %s: %w", strings.Join(args, " "), err)
	}
	return strings.TrimSpace(string(out)), nil
}

// gitBlob runs `git cat-file blob <hash>` in dir and returns the blob's raw
// bytes UNCHANGED — no trimming. A file's trailing newline (or absence of one)
// is content, and Restore must re-materialise the snapshot's bytes exactly.
func gitBlob(dir, hash string) ([]byte, error) {
	cmd := exec.Command("git", "cat-file", "blob", hash)
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("git cat-file blob %s: %w", hash, err)
	}
	return out, nil
}
