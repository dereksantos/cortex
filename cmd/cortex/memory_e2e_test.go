package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dereksantos/cortex/internal/tools"
)

// memory_e2e_test.go is the FAST, model-free end-to-end check of the
// model-driven memory seam: that a note written in one session is recallable —
// via the injected index and memory_read — by a *fresh* session over the same
// .cortex directory. It exercises the real on-disk store and the real tool
// dispatch; only the model is absent (its tool calls are made directly). The
// behavioral half — that the model actually chooses to write and read notes — is
// the gated live eval (memory_e2e_live_test.go). See docs/memory-tools.md.

// newMemSession builds a session with memory enabled in the current dir, as the
// REPL/headless drivers do via EnableMemory.
func newMemSession(t *testing.T) *CortexSession {
	t.Helper()
	cs := &CortexSession{Request: CortexArgs{}.Request()}
	cs.EnableMemory()
	if cs.memory == nil {
		t.Fatal("EnableMemory should wire a memory store in a writable dir")
	}
	return cs
}

// TestMemoryRecallAcrossSessions is scenario (a): write a non-re-derivable fact
// in session 1, then open a brand-new session over the same .cortex and confirm
// the note is both surfaced by the injected index and readable in full. This is
// the cross-session learning contract the whole pivot exists to deliver.
func TestMemoryRecallAcrossSessions(t *testing.T) {
	t.Chdir(t.TempDir())
	ctx := context.Background()

	// --- session 1: the agent saves a durable, non-re-derivable fact ----------
	s1 := newMemSession(t)
	const fact = "We deploy only on Tuesdays — never Fridays (change-freeze policy)."
	if _, err := tools.Execute(ctx, memCall(tools.FunctionMemoryWrite, map[string]any{
		"name": "deploy-policy", "content": fact,
	}), s1); err != nil {
		t.Fatalf("session 1 memory_write: %v", err)
	}

	// --- session 2: a fresh process/session over the same .cortex -------------
	s2 := newMemSession(t)

	// The index injection — the one mechanical seam — must tell session 2 the
	// note exists, so a tool-only model knows it can recall it.
	idx := s2.memoryIndexNote()
	if !strings.Contains(idx, "deploy-policy") {
		t.Fatalf("fresh session's injected index must list the prior note; got:\n%s", idx)
	}

	// And reading it back returns the full fact — the recall path end to end.
	body, err := tools.Execute(ctx, memCall(tools.FunctionMemoryRead, map[string]any{"name": "deploy-policy"}), s2)
	if err != nil {
		t.Fatalf("session 2 memory_read: %v", err)
	}
	if !strings.Contains(body, "Tuesdays") || !strings.Contains(body, "Fridays") {
		t.Errorf("session 2 did not recall the fact in full, got %q", body)
	}
}

// TestMemoryStaleNoteUpdate is scenario (b): a saved fact goes stale, the agent
// corrects it with a write to the SAME name, and a later session sees only the
// corrected value — no duplicate, no stale leftover. Timestamps advance so the
// model could tell the note had changed.
func TestMemoryStaleNoteUpdate(t *testing.T) {
	t.Chdir(t.TempDir())
	ctx := context.Background()

	s1 := newMemSession(t)
	if _, err := tools.Execute(ctx, memCall(tools.FunctionMemoryWrite, map[string]any{
		"name": "staging-reset", "content": "Staging DB resets nightly at 2am UTC.",
	}), s1); err != nil {
		t.Fatal(err)
	}

	// reality changes; the agent updates the existing note rather than adding one
	s2 := newMemSession(t)
	if _, err := tools.Execute(ctx, memCall(tools.FunctionMemoryWrite, map[string]any{
		"name": "staging-reset", "content": "Staging DB resets nightly at 4am UTC (moved from 2am).",
	}), s2); err != nil {
		t.Fatal(err)
	}

	// a fresh session recalls only the corrected value
	s3 := newMemSession(t)
	body, err := tools.Execute(ctx, memCall(tools.FunctionMemoryRead, map[string]any{"name": "staging-reset"}), s3)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(body, "4am") {
		t.Errorf("updated note should hold the corrected time, got %q", body)
	}
	// the index must carry exactly one staging-reset entry (no duplicate)
	idx := s3.memoryIndexNote()
	if n := strings.Count(idx, "staging-reset"); n != 1 {
		t.Errorf("expected exactly one staging-reset note in the index, got %d:\n%s", n, idx)
	}
}

// TestMemoryForgetRemovesFromRecall is scenario complementary to (b): a note the
// agent forgets disappears from the recall surface a later session sees.
func TestMemoryForgetRemovesFromRecall(t *testing.T) {
	t.Chdir(t.TempDir())
	ctx := context.Background()

	s1 := newMemSession(t)
	if _, err := tools.Execute(ctx, memCall(tools.FunctionMemoryWrite, map[string]any{
		"name": "obsolete", "content": "An assumption that later proved wrong.",
	}), s1); err != nil {
		t.Fatal(err)
	}
	if _, err := tools.Execute(ctx, memCall(tools.FunctionMemoryForget, map[string]any{"name": "obsolete"}), s1); err != nil {
		t.Fatal(err)
	}

	s2 := newMemSession(t)
	if idx := s2.memoryIndexNote(); strings.Contains(idx, "obsolete") {
		t.Errorf("forgotten note must not appear in a later session's index:\n%s", idx)
	}
}

// TestMemoryNoteIsRedactedOnDisk (issue #103): a secret the agent (or the
// learn-loop) captures into a note must reach the on-disk .cortex/memory/*.md
// file ONLY as [REDACTED:…], never verbatim, and memory_read of that note must
// return the same redacted form the store holds. MemoryWrite is the single
// choke point for every model- and learn-loop-driven write (both call into
// it), so masking it there covers both; the redaction is recorded on the
// session's per-turn counter (cs.redactions) so it rides TurnResult alongside
// the transcript's and the journal's.
func TestMemoryNoteIsRedactedOnDisk(t *testing.T) {
	t.Chdir(t.TempDir())
	ctx := context.Background()

	const secret = "sk-or-v1-0123456789abcdef0123456789abcdef"
	s1 := newMemSession(t)
	if _, err := s1.MemoryWrite("leaked-key", "the deployment key is "+secret, ""); err != nil {
		t.Fatalf("MemoryWrite: %v", err)
	}
	// The note's redaction count is recorded on the session.
	if s1.redactions == 0 {
		t.Errorf("session redactions = 0, want > 0 (the note's secret must be counted at write time)")
	}

	// The on-disk note file itself must be redacted: no verbatim secret, and at
	// least one [REDACTED:…] marker.
	path := filepath.Join(s1.ContextDir(), "memory", "leaked-key.md")
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading note file: %v", err)
	}
	raw := string(b)
	if strings.Contains(raw, secret) {
		t.Errorf("on-disk note %s still contains the verbatim secret — issue #103 requires it redacted before persisting", path)
	}
	if !strings.Contains(raw, "[REDACTED:") {
		t.Errorf("on-disk note %s has no [REDACTED:…] marker, want at least one (the note carried a secret)", path)
	}

	// memory_read of the note returns the same redacted body the store holds.
	body, err := s1.MemoryRead("leaked-key", "")
	if err != nil {
		t.Fatalf("MemoryRead: %v", err)
	}
	if strings.Contains(body, secret) {
		t.Errorf("memory_read returned the verbatim secret — it must return the redacted form the store holds")
	}
	if !strings.Contains(body, "[REDACTED:") {
		t.Errorf("memory_read body = %q, want the [REDACTED:…] form (the store's redacted body)", body)
	}

	// A fresh session over the same .cortex sees the SAME redacted note via the
	// real tool dispatch (the model-facing read path), not the verbatim secret.
	s2 := newMemSession(t)
	body2, err := tools.Execute(ctx, memCall(tools.FunctionMemoryRead, map[string]any{"name": "leaked-key"}), s2)
	if err != nil {
		t.Fatalf("fresh session memory_read: %v", err)
	}
	if strings.Contains(body2, secret) {
		t.Errorf("fresh session's memory_read returned the verbatim secret — only the redacted form may ever be read back")
	}
	if !strings.Contains(body2, "[REDACTED:") {
		t.Errorf("fresh session's memory_read body = %q, want the [REDACTED:…] form", body2)
	}
}
