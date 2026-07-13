// loop_run_test.go — M6.3 (GOAL.md §6): each due loop firing runs a fresh
// headless session in the target project and produces exactly one
// `loop.run` journal event carrying an outcome and (when the turn actually
// changed files) a change ref. Mirrors the scripted-Sender-via-httptest
// pattern serve_stream_test.go's streamTurnTestSessionFactory established,
// and reuses change_status_test.go's initGitFixture for a real git-backed
// fixture project so the change-lifecycle leg (startChangeIn/
// commitChangeIn, change.go) is exercised against real git, not a fake.
package main

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/dereksantos/cortex/internal/journal"
	"github.com/dereksantos/cortex/internal/loops"
	"github.com/dereksantos/cortex/internal/registry"
	"github.com/dereksantos/cortex/internal/userhome"
)

// writeFileTurnTestSessionFactory scripts a two-round turn: round 1 issues a
// write_file tool call (so the firing actually dirties the target project's
// worktree), round 2 answers with a final "done" reply — the same
// round-then-round-2-final shape streamTurnTestSessionFactory
// (serve_stream_test.go) uses for its bash-tool-call scenario.
func writeFileTurnTestSessionFactory(t *testing.T) sessionFactory {
	t.Helper()
	var round int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		round++
		if round == 1 {
			_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","tool_calls":[{"id":"c1","type":"function","function":{"name":"write_file","arguments":"{\"path\":\"notes.md\",\"content\":\"loop notes\"}"}}]}}],"usage":{"prompt_tokens":5,"completion_tokens":2}}`))
			return
		}
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"done"},"finish_reason":"stop"}],"usage":{"prompt_tokens":6,"completion_tokens":3}}`))
	}))
	t.Cleanup(srv.Close)
	return func() *CortexSession {
		cs := &CortexSession{quiet: true, Request: CortexArgs{}.Request()}
		cs.Request.BaseURL = srv.URL
		return cs
	}
}

// noopTurnTestSessionFactory scripts a single-round turn with no tool calls
// at all — the "ran fine, changed nothing" case, proving a firing that
// makes no edits still journals success with an empty ChangeRef rather than
// a fabricated one.
func noopTurnTestSessionFactory(t *testing.T) sessionFactory {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"nothing to do"},"finish_reason":"stop"}],"usage":{"prompt_tokens":4,"completion_tokens":2}}`))
	}))
	t.Cleanup(srv.Close)
	return func() *CortexSession {
		cs := &CortexSession{quiet: true, Request: CortexArgs{}.Request()}
		cs.Request.BaseURL = srv.URL
		return cs
	}
}

// failingTurnTestSessionFactory scripts a backend that always 500s, so
// session.Turn returns an error — the "the run itself failed" case.
func failingTurnTestSessionFactory(t *testing.T) sessionFactory {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)
	return func() *CortexSession {
		cs := &CortexSession{quiet: true, Request: CortexArgs{}.Request()}
		cs.Request.BaseURL = srv.URL
		return cs
	}
}

// readLoopRunEntries drains the user-level loop.run journal (under the
// test's isolated CORTEX_HOME) into a plain slice of payloads, in write
// order.
func readLoopRunEntries(t *testing.T) []journal.LoopRunPayload {
	t.Helper()
	dir, err := userhome.Path("journal", "loop")
	if err != nil {
		t.Fatalf("userhome.Path: %v", err)
	}
	r, err := journal.NewReader(dir)
	if err != nil {
		t.Fatalf("NewReader: %v", err)
	}
	defer r.Close()

	var out []journal.LoopRunPayload
	for {
		e, err := r.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("Next: %v", err)
		}
		p, err := journal.ParseLoopRun(e)
		if err != nil {
			t.Fatalf("ParseLoopRun: %v", err)
		}
		out = append(out, *p)
	}
	return out
}

// TestRunLoopFiringRunsFreshHeadlessSessionAndJournalsSuccessWithChangeRef is
// the M6.3 DoD proper: a due firing whose scripted sender edits a file lands
// that edit as a reviewable `cortex change` (a cortex/<slug> branch plus one
// commit, docs/cortex-web.md Phase 6) scoped to the target project's root —
// not the process CWD — and journals exactly one success loop.run event
// carrying a non-empty ChangeRef.
func TestRunLoopFiringRunsFreshHeadlessSessionAndJournalsSuccessWithChangeRef(t *testing.T) {
	t.Setenv("CORTEX_HOME", t.TempDir())
	root := initGitFixture(t, "main", false)
	// The coder tool dispatcher (internal/tools) resolves write_file's path
	// argument against the process's actual working directory, not any
	// workspace root (a pre-existing gap outside M6.3's scope — only
	// contextDir/instructions/confinement are workspace-threaded today, per
	// project_workspace_test.go's own equivalence proof). Chdir into the
	// fixture so the scripted write_file call lands inside the git repo
	// startChangeIn/commitChangeIn operate on, exactly as it would if a real
	// `cortex serve` process were launched from inside a single project.
	t.Chdir(root)

	reg := &fakeRegistry{projects: map[string]registry.Project{"blog": {Name: "blog", Root: root}}}
	spec := loops.Spec{Name: "nightly", Project: "blog", Prompt: "leave a note"}

	if err := RunLoopFiring(context.Background(), spec, reg, writeFileTurnTestSessionFactory(t)); err != nil {
		t.Fatalf("RunLoopFiring: %v", err)
	}

	entries := readLoopRunEntries(t)
	if len(entries) != 1 {
		t.Fatalf("loop.run entries = %d, want 1: %+v", len(entries), entries)
	}
	got := entries[0]
	if got.Name != spec.Name {
		t.Errorf("Name = %q, want %q", got.Name, spec.Name)
	}
	if got.Project != spec.Project {
		t.Errorf("Project = %q, want %q", got.Project, spec.Project)
	}
	if got.Outcome != journal.LoopOutcomeSuccess {
		t.Errorf("Outcome = %q, want %q", got.Outcome, journal.LoopOutcomeSuccess)
	}
	if got.ChangeRef == "" {
		t.Fatal("ChangeRef is empty, want a branch@hash reference for the committed change")
	}
	if !strings.HasPrefix(got.ChangeRef, "cortex/nightly@") {
		t.Errorf("ChangeRef = %q, want prefix %q", got.ChangeRef, "cortex/nightly@")
	}
}

// TestRunLoopFiringNoOpTurnJournalsSuccessWithEmptyChangeRef proves a
// firing whose turn makes no edits still journals success — it must not
// fabricate a ChangeRef for a worktree that never became dirty.
func TestRunLoopFiringNoOpTurnJournalsSuccessWithEmptyChangeRef(t *testing.T) {
	t.Setenv("CORTEX_HOME", t.TempDir())
	root := initGitFixture(t, "main", false)
	t.Chdir(root)

	reg := &fakeRegistry{projects: map[string]registry.Project{"blog": {Name: "blog", Root: root}}}
	spec := loops.Spec{Name: "nightly", Project: "blog", Prompt: "leave a note"}

	if err := RunLoopFiring(context.Background(), spec, reg, noopTurnTestSessionFactory(t)); err != nil {
		t.Fatalf("RunLoopFiring: %v", err)
	}

	entries := readLoopRunEntries(t)
	if len(entries) != 1 {
		t.Fatalf("loop.run entries = %d, want 1: %+v", len(entries), entries)
	}
	if entries[0].Outcome != journal.LoopOutcomeSuccess {
		t.Errorf("Outcome = %q, want %q", entries[0].Outcome, journal.LoopOutcomeSuccess)
	}
	if entries[0].ChangeRef != "" {
		t.Errorf("ChangeRef = %q, want empty (no-op turn made no edits)", entries[0].ChangeRef)
	}
}

// TestRunLoopFiringTurnErrorJournalsFailedOutcome proves a firing whose
// session.Turn itself errors (e.g. the backend is unreachable) still
// produces exactly one loop.run event, with outcome "failed" and a Reason —
// a firing that errors mid-way must stay visible in run history instead of
// silently vanishing.
func TestRunLoopFiringTurnErrorJournalsFailedOutcome(t *testing.T) {
	t.Setenv("CORTEX_HOME", t.TempDir())
	root := initGitFixture(t, "main", false)
	t.Chdir(root)

	reg := &fakeRegistry{projects: map[string]registry.Project{"blog": {Name: "blog", Root: root}}}
	spec := loops.Spec{Name: "nightly", Project: "blog", Prompt: "leave a note"}

	if err := RunLoopFiring(context.Background(), spec, reg, failingTurnTestSessionFactory(t)); err != nil {
		t.Fatalf("RunLoopFiring: %v", err)
	}

	entries := readLoopRunEntries(t)
	if len(entries) != 1 {
		t.Fatalf("loop.run entries = %d, want 1: %+v", len(entries), entries)
	}
	if entries[0].Outcome != journal.LoopOutcomeFailed {
		t.Errorf("Outcome = %q, want %q", entries[0].Outcome, journal.LoopOutcomeFailed)
	}
	if entries[0].Reason == "" {
		t.Error("Reason is empty, want a short machine string explaining the failure")
	}
	if entries[0].ChangeRef != "" {
		t.Errorf("ChangeRef = %q, want empty on a failed run", entries[0].ChangeRef)
	}
}

// TestRunLoopFiringUnknownProjectJournalsFailedOutcome proves a spec
// naming an unregistered project fails cleanly (no panic, no live session
// construction) and still lands one journaled "failed" event, rather than
// a Go-level error the scheduler's caller must special-case.
func TestRunLoopFiringUnknownProjectJournalsFailedOutcome(t *testing.T) {
	t.Setenv("CORTEX_HOME", t.TempDir())

	reg := &fakeRegistry{}
	spec := loops.Spec{Name: "nightly", Project: "nope", Prompt: "leave a note"}

	if err := RunLoopFiring(context.Background(), spec, reg, writeFileTurnTestSessionFactory(t)); err != nil {
		t.Fatalf("RunLoopFiring: %v", err)
	}

	entries := readLoopRunEntries(t)
	if len(entries) != 1 {
		t.Fatalf("loop.run entries = %d, want 1: %+v", len(entries), entries)
	}
	if entries[0].Outcome != journal.LoopOutcomeFailed {
		t.Errorf("Outcome = %q, want %q", entries[0].Outcome, journal.LoopOutcomeFailed)
	}
	if !strings.Contains(entries[0].Reason, "nope") {
		t.Errorf("Reason = %q, want it to mention the unresolved project name %q", entries[0].Reason, "nope")
	}
}
