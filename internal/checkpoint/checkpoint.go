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
//     and prints its hash (or an EMPTY line when the tracked tree is clean);
//     it neither writes to `refs/stash` nor rewrites the index. `git
//     update-ref` records that commit under a hidden
//     `refs/cortex/checkpoints/…` ref. A file the turn deleted and staged
//     stays staged in the index throughout — the ref only names a tree.
//
//  2. On a clean tracked tree `git stash create` prints an empty line, so
//     `git rev-parse HEAD` is used as the snapshot commit instead — the
//     committed base IS the tree. Only whether the turn MUTATES files
//     decides whether a checkpoint is recorded, and that is decided at TURN
//     END (the caller keeps the checkpoint only if the turn actually ran a
//     file-mutating tool), never by the cleanliness of the tree at turn
//     start: a clean tree at the start tells you nothing about whether the
//     turn will mutate, and a dirty tree at the start is common for
//     read-only turns.
//
// A snapshot therefore carries two facts: the tracked tree (the commit hash)
// and the set of UNTRACKED, non-ignored files present at snapshot time (the
// `git ls-files --others --exclude-standard` listing). Restore uses the tree
// to write tracked files back, and the untracked set to remove the files the
// turn CREATED — an untracked file present now that was NOT in the set is a
// turn creation and is deleted, so pre-existing user untracked files and
// `.cortex/` (ignored) stay untouched.
//
// Restore is worktree-only: it never runs `git read-tree` / `checkout` —
// both of those rewrite the index, which would violate invariant 1 the
// moment the user had staged anything. Writing a blob to a path that the
// turn deleted-and-staged re-materialises the file as UNTRACKED (the staged
// deletion stays put), so the index is left byte-for-byte.
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
// per-session ref prefix RefFor does (e.g. to enumerate, prune, or delete a
// session's checkpoint refs on /clear) without re-deriving the
// sanitisation — one place owns the rule.
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
// commit via `git stash create` (falling back to HEAD's commit when the
// tracked tree is clean — `git stash create` prints nothing there) and
// records it under ref, returning the commit hash. It also returns the set
// of untracked, non-ignored paths present at snapshot time (the `git ls-files
// --others --exclude-standard` listing) — the baseline Restore needs to tell
// the files THIS turn created apart from pre-existing user files. It never
// touches the index or the stash list.
func Snapshot(dir, ref string) (snap string, untracked []string, err error) {
	out, err := gitOut(dir, "stash", "create")
	if err != nil {
		return "", nil, fmt.Errorf("git stash create: %w", err)
	}
	if strings.TrimSpace(out) == "" {
		// Clean tracked tree: the committed base IS the tree. Record HEAD's
		// commit — the caller still decides whether the turn mutated files
		// (at turn end) and drops the checkpoint if it did not.
		out, err = gitOut(dir, "rev-parse", "HEAD")
		if err != nil {
			return "", nil, fmt.Errorf("git rev-parse HEAD: %w", err)
		}
	}
	if _, err := gitOut(dir, "update-ref", ref, out); err != nil {
		return "", nil, fmt.Errorf("git update-ref %s: %w", ref, err)
	}
	untracked, err = untrackedPaths(dir)
	if err != nil {
		return "", nil, fmt.Errorf("git ls-files --others: %w", err)
	}
	return out, untracked, nil
}

// Restore writes the snapshot back to dir's working tree and returns the list
// of files it changed (sorted, relative to dir). It never touches the index
// or the stash list:
//
//   - every BLOB in the snapshot tree whose on-disk content differs from the
//     snapshot's is rewritten (honouring the tree entry's mode — 100755 keeps
//     its exec bit, 120000 is re-linked as a symlink); a file the turn
//     deleted-and-staged is re-materialised as UNTRACKED;
//   - every working-tree file that the index tracks but the snapshot tree
//     does not is removed (a tracked file the turn deleted without staging);
//   - every untracked file present now that was NOT in `untracked` (the set
//     the snapshot recorded) is removed — the files the turn created.
//
// Files already matching the snapshot are not rewritten and do not appear in
// the changed list; pre-existing untracked user files (they were in
// `untracked`) and `.cortex/` (gitignored, never listed) are left alone.
func Restore(dir string, snap string, untracked []string) ([]string, error) {
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
		switch e.Type {
		case "blob":
			rewrote, err := restoreBlob(dir, e)
			if err != nil {
				return changed, fmt.Errorf("restore %s: %w", e.Path, err)
			}
			if rewrote {
				changed = append(changed, e.Path)
			}
		case "commit":
			// gitlink (submodule): a file checkpoint is per-blob; skip.
		}
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

	// Remove the files THIS turn created: untracked now, but absent from the
	// snapshot's untracked baseline. Pre-existing user untracked files were
	// in the baseline and survive; `.cortex/` is gitignored and was never
	// listed.
	baseline := make(map[string]bool, len(untracked))
	for _, p := range untracked {
		baseline[p] = true
	}
	if others, err := untrackedPaths(dir); err == nil {
		for _, p := range others {
			if baseline[p] || snapPaths[p] {
				continue
			}
			if err := removePathRecursive(dir, p); err != nil {
				return changed, fmt.Errorf("remove created %s: %w", p, err)
			}
			changed = append(changed, p)
		}
	}

	sort.Strings(changed)
	return changed, nil
}

// Prune deletes the oldest checkpoint refs for ONE session, keeping the last
// `keep` (newest first). It is scoped to the session's ref prefix — another
// session's refs are never touched, so a concurrent (or lower-sorting)
// session's history survives. It is best-effort: a missing git or a missing
// ref neither errors nor panics — checkpoint bookkeeping must never break a
// turn.
func Prune(dir, session string, keep int) {
	if keep < 0 {
		keep = 0
	}
	prefix := RefPrefix + Sanitize(session) + "/"
	refs, err := listCheckpointRefs(dir, prefix)
	if err != nil || len(refs) <= keep {
		return
	}
	// refs is newest-first; the tail is the oldest and what we drop.
	for _, ref := range refs[keep:] {
		_, _ = gitOut(dir, "update-ref", "-d", ref)
	}
}

// listCheckpointRefs returns the checkpoint refs under prefix (normally one
// session's RefPrefix+Sanitize(id)+"/"), newest first. `git for-each-ref`
// lists names lexicographically; a session's turns are named
// <session>/<turn> with zero-padded ordinals, so within a session the
// lexicographic order equals creation order. Reversed, the largest name
// (newest) leads.
func listCheckpointRefs(dir, prefix string) ([]string, error) {
	out, err := gitOut(dir, "for-each-ref", "--format=%(refname)", prefix)
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

// restoreBlob rewrites one snapshot tree entry to its working-tree path,
// honouring the entry's mode, and reports whether the working tree actually
// changed. A 100755 blob is written 0o755; a 120000 entry is replaced with a
// real symlink whose target is the blob's content; anything else is a
// 0o644 file. The current file's content is compared to the snapshot's blob
// first — an unchanged file is left byte-for-byte (and not reported as
// changed).
func restoreBlob(dir string, e treeEntry) (bool, error) {
	contents, err := gitBlob(dir, e.Blob)
	if err != nil {
		return false, fmt.Errorf("git cat-file blob %s: %w", e.Blob, err)
	}
	dst := filepath.Join(dir, filepath.FromSlash(e.Path))
	if d := filepath.Dir(dst); d != "" && d != dir {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return false, fmt.Errorf("mkdir %s: %w", d, err)
		}
	}
	if e.Mode == "120000" {
		// A symlink in the snapshot tree: re-link it. The blob's bytes are
		// the link target.
		if target, ok := readLink(dst); ok && target == string(contents) {
			return false, nil // already the right symlink
		}
		if err := os.Remove(dst); err != nil && !os.IsNotExist(err) {
			return false, fmt.Errorf("remove %s: %w", dst, err)
		}
		if err := os.Symlink(string(contents), dst); err != nil {
			return false, fmt.Errorf("symlink %s: %w", dst, err)
		}
		return true, nil
	}
	if existing, err := os.ReadFile(dst); err == nil && string(existing) == string(contents) {
		// Content matches the snapshot; keep the file as-is (mode and all).
		return false, nil
	}
	mode := os.FileMode(0o644)
	if e.Mode == "100755" {
		mode = 0o755
	}
	if err := os.WriteFile(dst, contents, mode); err != nil {
		return false, fmt.Errorf("write %s: %w", dst, err)
	}
	// os.WriteFile preserves the existing file's mode when overwriting, so
	// explicitly chmod to the snapshot's mode — a turn that stripped the
	// exec bit (or a non-exec file that gained one) is restored to the
	// snapshot's permission.
	if err := os.Chmod(dst, mode); err != nil {
		return false, fmt.Errorf("chmod %s: %w", dst, err)
	}
	return true, nil
}

// readLink returns the symlink's target (and whether dst is a symlink). It is
// separate from restoreBlob's error path so a missing file reports "not a
// symlink" rather than a read error.
func readLink(dst string) (string, bool) {
	target, err := os.Readlink(dst)
	if err != nil {
		return "", false
	}
	return target, true
}

// removePathRecursive removes dir/path from the working tree — a file, or a
// directory tree (a turn can create whole untracked directories). It never
// touches the index. A missing path is not an error.
func removePathRecursive(dir, path string) error {
	dst := filepath.Join(dir, filepath.FromSlash(path))
	err := os.RemoveAll(dst)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove %s: %w", path, err)
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

// untrackedPaths returns the working tree's untracked, non-ignored paths —
// the exact `git ls-files --others --exclude-standard` listing, sorted. This
// is the baseline a snapshot records and Restore diffs against to find the
// files the turn created. It reads git's output RAW (no trimming): a path
// that is a single space is a legal file name and must survive.
func untrackedPaths(dir string) ([]string, error) {
	out, err := gitOutRaw(dir, "ls-files", "--others", "--exclude-standard")
	if err != nil {
		return nil, err
	}
	var paths []string
	for _, line := range strings.Split(out, "\n") {
		if line = strings.TrimRight(line, "\r"); line != "" {
			paths = append(paths, line)
		}
	}
	sort.Strings(paths)
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
	out, err := gitOutRaw(dir, args...)
	return strings.TrimSpace(out), err
}

// gitOutRaw runs `git <args>` in dir and returns the UNTRIMMED stdout, or an
// error carrying the raw error plus whatever stdout held on non-zero exit.
// gitOut is the trimmed convenience for single-value outputs (a hash, a ref
// list); gitOutRaw is for content-accurate outputs where a leading or
// trailing space in a line is data (the untracked-path listing).
func gitOutRaw(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return string(out), fmt.Errorf("git %s: %w", strings.Join(args, " "), err)
	}
	return string(out), nil
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
