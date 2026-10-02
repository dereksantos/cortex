// gitignore_self.go — the self-contained `.cortex/` gitignore (issue #119).
//
// Running Cortex in a fresh git repo creates `.cortex/sessions/`,
// `.cortex/journal/`, `.cortex/history` and `.cortex/cortex.log`, but nothing
// told git to ignore them, so a routine `git add -A && git commit` published
// full session transcripts — including any secrets a tool's output captured
// (#103). The fix is deliberately self-contained rather than editing the
// user's own `.gitignore`: when the workspace lives inside a git repository
// and `.cortex/` is not already ignored, cortex writes a `.cortex/.gitignore`
// containing only `*`, so the directory excludes itself from git without
// touching a single file the user owns.
//
// Everything here is best-effort: a missing git binary, a worktree that is
// not a git repository, or an unwritable `.cortex/` dir must each degrade
// silently to "nothing written", never to a startup failure or a printed
// error. The one exception is the success case, which prints a single
// one-line notice so the user sees that their transcripts are now
// gitignored instead of wondering where the file came from.
package main

import (
	"os"
	"path/filepath"
)

// selfGitignoreBody is the exact content of the `.cortex/.gitignore` this
// file writes: a lone `*` that ignores every entry directly under `.cortex/`.
// A nested `.gitignore` only governs its own directory and its children, so
// `*` covers sessions/, journal/, history, cortex.log, memory/, and anything
// else `.cortex/` grows — without matching `.gitignore` itself, which stays
// trackable so the ignore rule survives a fresh clone.
const selfGitignoreBody = "*\n"

// ensureSelfGitignore makes the workspace's `.cortex/` dir self-ignoring in
// git (issue #119). It writes `.cortex/.gitignore` containing `*` — and
// prints a one-line notice — iff: the workspace root resolves, that root is
// inside a git repository (or is one), `.cortex/` is not already ignored
// (`git check-ignore -q .cortex`), and the self-ignore file is not already
// present. Every other outcome is a silent no-op: there is no resolved
// workspace, no `.cortex/` dir to ignore yet, the root is not in a git repo,
// git is unavailable, or `.cortex/` is already ignored.
//
// Idempotent: once `.cortex/.gitignore` exists (written by a prior run) the
// file's presence short-circuits the check, so repeat sessions and repeat
// invocations of `StartTranscript` (compaction, /clear) add nothing.
//
// Called from StartTranscript (session.go) right after the sessions dir is
// created, so every entry point that opens a transcript (the REPL, `turn`,
// `study`, `learn`, `serve`, `discord`, and the loop scheduler) covers the
// workspace's `.cortex/` at the first moment it could leak.
func (cs *CortexSession) ensureSelfGitignore() {
	if cs == nil || cs.workspace == nil || cs.workspace.Root == "" {
		return
	}
	root := cs.workspace.Root
	ctxDir := filepath.Join(root, ".cortex")
	gitignorePath := filepath.Join(ctxDir, ".gitignore")

	// Already self-ignored: nothing to do (idempotency + "user already set
	// this up" both land here). Stat'ing first means a repeated session never
	// spawns a git process at all.
	if _, err := os.Stat(gitignorePath); err == nil {
		return
	}

	// The `.cortex/` dir is not present yet: StartTranscript's MkdirAll runs
	// immediately after this call, so there is nothing to protect this instant.
	if _, err := os.Stat(ctxDir); err != nil {
		return
	}

	// Not inside a git repository (or git is unavailable): there is nothing to
	// ignore and no user to tell. exec.LookPath's error (no git on PATH) and
	// check-ignore's non-zero exit (no repo / not ignored) are handled below.
	repoRoot, err := gitCmdIn(root, "rev-parse", "--show-toplevel")
	if err != nil || repoRoot == "" {
		return
	}

	// `.cortex/` is already ignored by some rule the user (or a tool) wrote —
	// an entry in the repo's .gitignore, a global excludes file, a nested
	// pattern. Respect it: never overwrite a user's choice with our own.
	//
	// `git check-ignore -q .cortex` exits 0 when the path is ignored and 1
	// when it is not (a non-existent path is still checked: git resolves the
	// .gitignore patterns textually, so the file need not exist for the
	// query to work). With -q there is no output, so the exit status is the
	// only signal: err == nil means "ignored", and we must NOT treat that as
	// a failure. The earlier form (`err == nil && out == ""`) inverted this
	// and skipped the write exactly when it was needed.
	if _, err := gitCmdIn(root, "check-ignore", "-q", ".cortex"); err == nil {
		return
	}

	if err := os.WriteFile(gitignorePath, []byte(selfGitignoreBody), 0o644); err != nil {
		// Best-effort: a read-only `.cortex/` dir or a permission error must
		// not break transcript startup. Swallowed on purpose.
		return
	}
	// One-line notice on the success case, in the same shape as the other
	// startup preflight diagnostics (printStartupWarning: stderr, yellow) so
	// the user sees their transcripts are now gitignored instead of finding
	// the file later and wondering where it came from. Stderr keeps stdout
	// machine-clean for headless `turn --json` consumers (issue #118).
	printStartupWarning(os.Stderr, "note: wrote .cortex/.gitignore — .cortex/ (sessions, journal, memory) is now gitignored and won't be swept up by `git add -A`")
}
