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

	"github.com/dereksantos/cortex/internal/registry"
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

// writeFakeTranscript appends one line to the transcript file the test
// session's StartTranscript already opened, so the
// `git status --porcelain` assertion has a real file to be (or not be)
// listing — the exact leak the issue describes. (The file is the session's
// own transcript, id == cs.SessionID — this is NOT a journal write.)
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
// greeting_test.go/serve_turn_test.go establish. It assigns cs.workspace
// DIRECTLY (not via SetWorkspace): these tests exercise the
// StartTranscript/ResumeTranscript/ensureSelfGitignore seams themselves, and
// they need a workspace without the re-target guard running first. Rooting
// the workspace (vs. relying on CWD) is what production NewCortexSession
// does, and it lets the test target an explicit temp repo.
func selfGitignoreSession(t *testing.T, root string) *CortexSession {
	t.Helper()
	ws, err := NewWorkspace(root)
	if err != nil {
		t.Fatalf("NewWorkspace(%s): %v", root, err)
	}
	cs := &CortexSession{quiet: true, workspace: ws, Request: CortexArgs{}.Request()}
	return cs
}

// freshGitSession is a session built the way runStudyCLI/runTurnCLI/
// runLearnCLI build theirs — via NewCortexSession, not a hand-built
// struct — with the environment isolated so construction stays hermetic:
// CORTEX_BACKEND pins the endpoint (no model discovery in a temp dir) and
// CORTEX_HOME points user config / the project registry / the user journal
// at a throwaway home. No network call is made during NewCortexSession
// (config + binding resolution only) — but the test must still exercise the
// REAL constructor's SetWorkspace hook, so this goes through the full
// constructor, not a shortcut around it.
func freshGitSession(t *testing.T) *CortexSession {
	t.Helper()
	t.Setenv("CORTEX_BACKEND", "http://localhost:0")
	t.Setenv("CORTEX_HOME", t.TempDir())
	// NewCortexSession mutates package-level prompt/cap state (instructionBytesCap,
	// promptBase/Append/Attribution via configurePrompt/configureAttributionPrompt).
	// The seam under test is only the SetWorkspace/ensureSelfGitignore hook, not
	// that state — snapshot it and restore on cleanup so it doesn't leak into the
	// later prompt/instruction tests in this package (they assert on SystemPrompt
	// verbatim and a 16 KiB cap). resetPrompt (prompt_test.go) already guards the
	// prompt vars; instructionBytesCap has no equivalent, so snapshot it here too.
	resetPrompt(t)
	oldCap := instructionBytesCap
	t.Cleanup(func() { instructionBytesCap = oldCap })
	cs := NewCortexSession()
	cs.quiet = true
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
			// No pre-seeded .cortex/ at all: ensureSelfGitignore's own
			// MkdirAll (run only once the git checks have said the write is
			// wanted — see the ordering comment in gitignore_self.go) must
			// create the dir, write the self-ignore into it, and leave git
			// status clean of .cortex/. wantStatus is the real acceptance
			// criterion here: status stays clean of .cortex/.
			name:       "fresh git repo: .cortex/ created by ensureSelfGitignore's own MkdirAll gets self-ignored",
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
			// Non-git CWD with NO pre-existing .cortex/: the rev-parse check
			// must run before the MkdirAll, so this hook leaves NO self-ignore
			// file (and no hook-created .cortex/) behind — the dir here is
			// created by StartTranscript's own MkdirAll, not by this hook
			// (NewCortexSession does no MkdirAll of its own).
			name:       "non-git dir, no pre-seeded .cortex/: hook writes no self-ignore file",
			gitRepo:    false,
			seedDir:    false,
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
			// StartTranscript always creates .cortex/sessions with the
			// session's own transcript inside; that is the real "first
			// session" state the issue starts from.
			writeFakeTranscript(t, filepath.Join(root, ".cortex"), cs.SessionID)

			gitignorePath := filepath.Join(root, ".cortex", ".gitignore")
			_, statErr := os.Stat(gitignorePath)
			gotFile := statErr == nil
			if gotFile != tt.wantFile {
				t.Errorf(".cortex/.gitignore exists = %v, want %v (statErr=%v)", gotFile, tt.wantFile, statErr)
			}

			// When the case expects no self-file (non-git, or the user already
			// ignores .cortex/), the hook must write no .cortex/.gitignore. The
			// .cortex/ dir itself may legitimately exist — StartTranscript
			// always creates .cortex/sessions (the real "first session" state) —
			// but the hook's own MkdirAll must not have run for a no-write case.
			// (A pre-seeded dir may of course still be there.)
			if !tt.wantFile {
				if _, err := os.Stat(gitignorePath); err == nil {
					t.Errorf(".cortex/.gitignore was written in a no-write case")
				}
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
// from the shared workspace-resolution seam (CortexSession.SetWorkspace,
// called by NewCortexSession right after the workspace resolves —
// workspace.go), not from StartTranscript. Delete that hook and this test
// fails: a real NewCortexSession in a fresh git repo must leave
// .cortex/.gitignore on disk before the session's first write, so a first
// `cortex study` or `cortex learn` can't leave .cortex/ untracked. No
// transcript is ever opened — that is the point.
func TestEnsureSelfGitignoreCoversStudyLearnPin(t *testing.T) {
	root := t.TempDir()
	t.Chdir(root)
	initGitRepo(t, root)

	// The production study/learn flow is exactly NewCortexSession() followed
	// by writes under the CWD workspace's .cortex/ (journal, memory, the
	// learn cursor) — no StartTranscript in between. Drive the real
	// constructor (freshGitSession isolates CORTEX_BACKEND/CORTEX_HOME so it
	// stays hermetic; NewCortexSession itself makes no network calls) and
	// assert on the on-disk outcome it must produce at construction time.
	cs := freshGitSession(t)
	if cs.workspace == nil || cs.workspace.Root == "" {
		t.Fatal("NewCortexSession did not resolve a workspace")
	}
	if resolved := resolvedPath(t, cs.workspace.Root); resolved != resolvedPath(t, root) {
		t.Fatalf("workspace root = %q, want CWD %q (the study/learn CWD-implicit leg)", resolved, resolvedPath(t, root))
	}

	if _, err := os.Stat(filepath.Join(root, ".cortex", ".gitignore")); err != nil {
		t.Fatalf(".cortex/.gitignore not written by the NewCortexSession/SetWorkspace seam: %v", err)
	}

	// Now the first real write under .cortex/ that study/learn do — a
	// transcript line, standing in for a journal entry — and confirm it
	// lands inside a gitignored dir (the leak the issue describes).
	if err := os.MkdirAll(filepath.Join(root, ".cortex", "sessions"), 0o755); err != nil {
		t.Fatalf("MkdirAll sessions: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, ".cortex", "sessions", "20260101-000000.jsonl"),
		[]byte(`{"kind":"message","role":"user","content":"secret"}`+"\n"), 0o644); err != nil {
		t.Fatalf("write session transcript: %v", err)
	}

	status := gitStatusPorcelain(t, root)
	if strings.Contains(status, ".cortex") {
		t.Errorf("git status --porcelain lists .cortex after the study/learn path: %q", status)
	}
}

// TestApplyProjectByNameCoversTargetRootSelfIgnore is the --project leg of
// issue #119: applyProjectByName re-targets the session at a DIFFERENT root
// than NewCortexSession resolved (the CWD), and its SetWorkspace call must
// re-run the guard for that target's .cortex/. Without it, `cortex
// study|learn|turn --project X` writes under X/.cortex/ while only the CWD
// workspace is protected — and X may be a git repo whose .cortex/ would be
// swept up by a routine `git add -A`.
func TestApplyProjectByNameCoversTargetRootSelfIgnore(t *testing.T) {
	target := t.TempDir()
	initGitRepo(t, target)

	// Build the session the way the CLI entry points do: NewCortexSession
	// from an UNRELATED CWD (a git repo too, so the CWD-side guard has
	// something to protect — the assertion below is about the target, not
	// the CWD).
	cwd := t.TempDir()
	t.Chdir(cwd)
	initGitRepo(t, cwd)
	cs := freshGitSession(t)

	regPath := filepath.Join(t.TempDir(), "projects.json")
	reg := registry.NewAt(regPath)
	if err := reg.Save(registry.Project{Name: "target", Root: target}); err != nil {
		t.Fatalf("Save: %v", err)
	}

	if err := applyProjectByName(cs, reg, "target"); err != nil {
		t.Fatalf("applyProjectByName: %v", err)
	}
	if resolved := resolvedPath(t, cs.workspace.Root); resolved != resolvedPath(t, target) {
		t.Fatalf("workspace root after re-target = %q, want %q", resolved, resolvedPath(t, target))
	}

	// The target's .cortex/ must now be self-ignored — written by the
	// re-target's SetWorkspace, not by the CWD-side guard.
	if _, err := os.Stat(filepath.Join(target, ".cortex", ".gitignore")); err != nil {
		t.Fatalf("target .cortex/.gitignore not written by applyProjectByName: %v", err)
	}

	// And the target repo's status is clean: a real file written under the
	// target's .cortex/ (what learn --project does with its journal/cursor)
	// must not appear as untracked.
	if err := os.MkdirAll(filepath.Join(target, ".cortex", "journal"), 0o755); err != nil {
		t.Fatalf("MkdirAll target journal: %v", err)
	}
	if err := os.WriteFile(filepath.Join(target, ".cortex", "journal", "probe"), []byte("{}\n"), 0o644); err != nil {
		t.Fatalf("write target journal probe: %v", err)
	}
	status := gitStatusPorcelain(t, target)
	for _, line := range strings.Split(status, "\n") {
		if strings.Contains(line, ".cortex") {
			t.Errorf("target git status --porcelain still lists .cortex: %q\n(full output: %q)", line, status)
		}
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
