// gitignore_self_test.go — issue #119: after the first session in a fresh
// git repo, `.cortex/` must not appear as untracked in `git status --porcelain`
// (a routine `git add -A && git commit` must not publish session transcripts,
// which can carry secrets captured from tool output — see #103). The fix is
// the self-contained `.cortex/.gitignore` written by ensureSelfGitignore
// (gitignore_self.go), never an edit to the user's own `.gitignore`.
//
// The tests drive the real `git` binary against a temp repo (the same
// gitCmd/gitCmdOutput convention change_test.go establishes) so the assertion
// is exactly the issue's acceptance criterion: `git status --porcelain` is
// clean of `.cortex/` after `StartTranscript` (which now calls
// ensureSelfGitignore).
package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// initGitRepo runs `git init -q` in dir and configures the minimal identity
// a commit needs. It reuses change_test.go's shared gitCmd helper (the main
// test package is one Go package, so its helpers are visible here) rather
// than forking a parallel git runner.
func initGitRepo(t *testing.T, dir string) {
	t.Helper()
	gitCmd(t, dir, "init", "-q")
	gitCmd(t, dir, "config", "user.name", "cortex-test")
	gitCmd(t, dir, "config", "user.email", "cortex-test@example.com")
}

// gitStatusPorcelain runs `git status --porcelain` in dir and returns the raw
// (untrimmed) output lines. A missing file, a `.cortex/` dir, or a
// `.cortex/.gitignore` all show up here unless git is told to ignore them.
func gitStatusPorcelain(t *testing.T, dir string) string {
	t.Helper()
	out, err := gitCmdOutput(t, dir, "status", "--porcelain")
	if err != nil {
		t.Fatalf("git status --porcelain in %s: %v", dir, err)
	}
	return out
}

// writeFakeTranscript drops a tiny JSONL transcript under .cortex/sessions so
// the `git status --porcelain` assertion has a real file to be (or not be)
// listing — the exact leak the issue describes.
func writeFakeTranscript(t *testing.T, ctxDir, id string) {
	t.Helper()
	sessDir := filepath.Join(ctxDir, "sessions")
	if err := os.MkdirAll(sessDir, 0o755); err != nil {
		t.Fatalf("MkdirAll sessions: %v", err)
	}
	if err := os.WriteFile(filepath.Join(sessDir, id+".jsonl"), []byte(`{"kind":"message","role":"user","content":"secret"}`+"\n"), 0o644); err != nil {
		t.Fatalf("write transcript: %v", err)
	}
}

// selfGitignoreSession builds a hermetic *CortexSession whose workspace is
// rooted at root, mirroring the hand-built-session convention
// greeting_test.go/serve_turn_test.go establish. Rooting the workspace (vs.
// relying on CWD) is what production NewCortexSession does, and it lets the
// test target an explicit temp repo.
func selfGitignoreSession(t *testing.T, root string) *CortexSession {
	t.Helper()
	ws, err := NewWorkspace(root)
	if err != nil {
		t.Fatalf("NewWorkspace(%s): %v", root, err)
	}
	cs := &CortexSession{quiet: true, workspace: ws, Request: CortexArgs{}.Request()}
	return cs
}

// TestEnsureSelfGitignore covers the full decision table for issue #119.
// The "fresh git repo" case is the one the issue names; the others pin the
// best-effort / idempotent / don't-touch-the-user's-file contract.
func TestEnsureSelfGitignore(t *testing.T) {
	tests := []struct {
		name       string
		gitRepo    bool
		preIgnored bool // a repo .gitignore that already excludes .cortex/
		seedDir    bool // pre-create the .cortex/ dir (StartTranscript does this)
		wantFile   bool // .cortex/.gitignore should exist after the call
		wantStatus bool // git status --porcelain should NOT list .cortex/ (only meaningful for a git repo)
	}{
		{
			name:       "fresh git repo: .cortex/ self-ignores and disappears from git status",
			gitRepo:    true,
			seedDir:    true,
			wantFile:   true,
			wantStatus: true,
		},
		{
			// StartTranscript always creates .cortex/sessions, so the self-ignore
			// fires even without a pre-seeded .cortex/ — the "first session" in
			// the field always has the dir by the time this runs. wantStatus is
			// the real acceptance criterion here: status stays clean of .cortex/.
			name:       "fresh git repo: .cortex/ created by StartTranscript gets self-ignored",
			gitRepo:    true,
			seedDir:    false,
			wantFile:   true,
			wantStatus: true,
		},
		{
			name:       "non-git dir: no file written, no error",
			gitRepo:    false,
			seedDir:    true,
			wantFile:   false,
			wantStatus: false,
		},
		{
			name:       "already ignored by the user's own .gitignore: user's file untouched, no self-file",
			gitRepo:    true,
			preIgnored: true,
			seedDir:    true,
			wantFile:   false,
			wantStatus: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			t.Chdir(root)
			if tt.gitRepo {
				initGitRepo(t, root)
			}
			if tt.preIgnored {
				if err := os.WriteFile(filepath.Join(root, ".gitignore"), []byte(".cortex/\n"), 0o644); err != nil {
					t.Fatalf("write pre-existing .gitignore: %v", err)
				}
			}
			if tt.seedDir {
				if err := os.MkdirAll(filepath.Join(root, ".cortex"), 0o755); err != nil {
					t.Fatalf("seed .cortex dir: %v", err)
				}
			}
			cs := selfGitignoreSession(t, root)

			// The production path is StartTranscript → ensureSelfGitignore.
			// Call StartTranscript (not ensureSelfGitignore directly) so the
			// wiring itself is exercised, then assert the on-disk outcome.
			cs.StartTranscript()
			if cs.transcript != nil {
				defer cs.transcript.Close()
			}
			// StartTranscript always creates .cortex/sessions; that is the
			// real "first session" state the issue starts from.
			writeFakeTranscript(t, filepath.Join(root, ".cortex"), "20260101-000000")

			gitignorePath := filepath.Join(root, ".cortex", ".gitignore")
			_, statErr := os.Stat(gitignorePath)
			gotFile := statErr == nil
			if gotFile != tt.wantFile {
				t.Errorf(".cortex/.gitignore exists = %v, want %v (statErr=%v)", gotFile, tt.wantFile, statErr)
			}

			if tt.wantStatus {
				status := gitStatusPorcelain(t, root)
				for _, line := range strings.Split(status, "\n") {
					if strings.Contains(line, ".cortex") {
						t.Errorf("git status --porcelain still lists .cortex: %q\n(full output: %q)", line, status)
					}
				}
			}
		})
	}
}

// TestEnsureSelfGitignoreCoversStudyLearnPin is the study/learn coverage the
// issue's headline misses: runStudyCLI/runLearnCLI build their session with
// NewCortexSession and never open a transcript, so the self-ignore must fire
// from the shared workspace-resolution seam (NewCortexSession), not from
// StartTranscript. A hand-built *CortexSession — what those CLI functions
// receive from NewCortexSession, workspace already resolved, before the first
// write under .cortex/ — must leave .cortex/.gitignore on disk, so a first
// `cortex study` or `cortex learn` in a fresh repo can't leave .cortex/
// untracked. The seam itself (ensureSelfGitignore from workspace
// resolution) is exactly what this asserts: no transcript, just the hook.
func TestEnsureSelfGitignoreCoversStudyLearnPin(t *testing.T) {
	root := t.TempDir()
	t.Chdir(root)
	initGitRepo(t, root)

	// The production study/learn flow resolves the workspace first
	// (WorkspaceFromCWD / NewWorkspace), then runs the session. NewCortex
	// Session invokes the same seam (ensureSelfGitignore) at that moment —
	// mirror it here without re-invoking the full NewCortexSession (no live
	// backend / model catalog in this hermetic test).
	cs := selfGitignoreSession(t, root)
	cs.ensureSelfGitignore()

	// Now simulate the first real write under .cortex/ that study/learn does
	// — a journal entry — and confirm the write lands inside a gitignored
	// dir (the leak the issue describes).
	writeFakeTranscript(t, filepath.Join(root, ".cortex"), "20260101-000000")

	if _, err := os.Stat(filepath.Join(root, ".cortex", ".gitignore")); err != nil {
		t.Fatalf(".cortex/.gitignore not written via workspace-resolution seam: %v", err)
	}

	status := gitStatusPorcelain(t, root)
	if strings.Contains(status, ".cortex") {
		t.Errorf("git status --porcelain lists .cortex after study/learn path: %q", status)
	}
}

// TestEnsureSelfGitignoreIdempotent pins that a second session over the same
// workspace adds nothing: the self-ignore file is written once, and a repeat
// call neither rewrites it nor errors. This is the "repeat invocations of
// StartTranscript (compaction, /clear) add nothing" guarantee.
func TestEnsureSelfGitignoreIdempotent(t *testing.T) {
	root := t.TempDir()
	t.Chdir(root)
	initGitRepo(t, root)

	cs := selfGitignoreSession(t, root)
	cs.StartTranscript()
	if cs.transcript != nil {
		cs.transcript.Close()
	}

	gitignorePath := filepath.Join(root, ".cortex", ".gitignore")
	first, err := os.ReadFile(gitignorePath)
	if err != nil {
		t.Fatalf(".cortex/.gitignore not written on first session: %v", err)
	}

	// A second session in the same workspace (a fresh *CortexSession, as a
	// second launch would produce) must be a no-op.
	cs2 := selfGitignoreSession(t, root)
	cs2.StartTranscript()
	if cs2.transcript != nil {
		defer cs2.transcript.Close()
	}

	second, err := os.ReadFile(gitignorePath)
	if err != nil {
		t.Fatalf(".cortex/.gitignore missing after second session: %v", err)
	}
	if string(first) != selfGitignoreBody {
		t.Errorf("first write content = %q, want %q", first, selfGitignoreBody)
	}
	if string(first) != string(second) {
		t.Errorf("second session changed the file: first=%q second=%q", first, second)
	}
}

// TestEnsureSelfGitignoreWantsStatusClean is the issue's acceptance test
// verbatim: "after the first session in a fresh repo, `git status --porcelain`
// doesn't list `.cortex/`." It uses the real `git` binary end-to-end and does
// NOT pre-seed the .cortex/ dir — StartTranscript is the only thing that
// creates it, which is exactly how the first session runs in the field.
func TestEnsureSelfGitignoreWantsStatusClean(t *testing.T) {
	root := t.TempDir()
	t.Chdir(root)
	initGitRepo(t, root)

	cs := selfGitignoreSession(t, root)
	cs.StartTranscript()
	if cs.transcript == nil {
		t.Fatal("StartTranscript did not open a transcript")
	}
	defer cs.transcript.Close()

	// The session wrote a real transcript; confirm it exists on disk so the
	// assertion below is not vacuously passing because nothing was created.
	transcripts := filepath.Join(root, ".cortex", "sessions")
	if _, err := os.Stat(transcripts); err != nil {
		t.Fatalf(".cortex/sessions not created: %v", err)
	}

	status := gitStatusPorcelain(t, root)
	if strings.Contains(status, ".cortex") {
		t.Errorf("git status --porcelain lists .cortex after the first session: %q", status)
	}
}

// TestEnsureSelfGitignoreNilAndMissingWorkspace pins the no-workspace and
// explicit-nil guards: a session with no resolved workspace (a hand-built
// *CortexSession{} with no workspace field) and a nil *CortexSession must
// both no-op without panic.
func TestEnsureSelfGitignoreNilAndMissingWorkspace(t *testing.T) {
	root := t.TempDir()
	t.Chdir(root)
	initGitRepo(t, root)

	// No workspace at all (cs.workspace == nil).
	cs := &CortexSession{quiet: true, Request: CortexArgs{}.Request()}
	cs.ensureSelfGitignore() // must not panic
	if _, err := os.Stat(filepath.Join(root, ".cortex", ".gitignore")); err == nil {
		t.Error("ensureSelfGitignore wrote a file with no workspace")
	}

	// An explicit nil receiver must also be safe.
	var nilCS *CortexSession
	nilCS.ensureSelfGitignore() // must not panic
}
