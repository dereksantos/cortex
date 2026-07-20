// learn_user_eval_test.go is the Δ (deterministic) layer of the user-scope
// cross-project promotion pass (docs/cross-source-learning.md piece 1's
// promotion half, piece 2's link rules, piece 3's scope generalization):
// scripted-sender tests over the REAL registry/memory-store/journal
// machinery, mirroring learning_loop_eval_test.go's style — no live model.
//
//	go test ./cmd/cortex -run LearnUser
package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dereksantos/cortex/internal/journal"
	"github.com/dereksantos/cortex/internal/loops"
	"github.com/dereksantos/cortex/internal/memory"
	"github.com/dereksantos/cortex/internal/registry"
	"github.com/dereksantos/cortex/internal/tools"
	"github.com/dereksantos/cortex/internal/userhome"
)

// writeProjectNote writes a real memory note into root's .cortex/memory
// store — the same on-disk shape collectProjectNotes reads.
func writeProjectNote(t *testing.T, root, name, content string) {
	t.Helper()
	ws, err := NewWorkspace(root)
	if err != nil {
		t.Fatalf("NewWorkspace: %v", err)
	}
	mem, err := memory.New(ws.ContextDir())
	if err != nil {
		t.Fatalf("memory.New: %v", err)
	}
	if _, err := mem.Write(name, content, time.Now()); err != nil {
		t.Fatalf("write note: %v", err)
	}
}

// newLearnUserEvalSession builds a *CortexSession with the user memory tier
// wired (EnableMemory, rooted at $CORTEX_HOME — set this via t.Setenv
// BEFORE calling) and Study pointed at the scripted backend. LearnUser is
// machine-scoped, not project-scoped, so unlike newLearnEvalSession there is
// no project workspace to apply.
func newLearnUserEvalSession(t *testing.T, backendURL string) *CortexSession {
	t.Helper()
	t.Chdir(t.TempDir())
	cs := &CortexSession{quiet: true, Study: ModelSpec{Model: "learn-user-m", Endpoint: backendURL}}
	cs.EnableMemory()
	return cs
}

// --- Δ1: the recurrence detector is deterministic and threshold-bound ----

func TestDetectCandidateGroupsThresholdBoundary(t *testing.T) {
	newNote := func(project, name, body string) candidateNote {
		return candidateNote{Project: project, Name: name, Body: body, terms: significantTerms(name + " " + body)}
	}
	tests := []struct {
		name       string
		notes      []candidateNote
		wantGroups int
	}{
		{
			name: "below threshold (2 shared distinctive terms) — no candidate group",
			notes: []candidateNote{
				newNote("proj-a", "retry-note", "We standardized linear retry logic here."),
				newNote("proj-b", "retry-note-b", "This deployment differs in retry linear timing slightly."),
			},
			wantGroups: 0,
		},
		{
			name: "at threshold (exactly recurrenceMinSharedTerms shared terms) — one candidate group",
			notes: []candidateNote{
				newNote("proj-a", "retry-note", "We standardized linear retry backoff here."),
				newNote("proj-b", "retry-note-b", "This linear retry backoff differs slightly."),
			},
			wantGroups: 1,
		},
		{
			name: "same-project overlap never groups regardless of shared terms",
			notes: []candidateNote{
				newNote("proj-a", "note-1", "We standardized linear retry backoff strategy."),
				newNote("proj-a", "note-2", "Also linear retry backoff strategy, same project."),
			},
			wantGroups: 0,
		},
		{
			name: "three projects, one recurring pair plus one unrelated note",
			notes: []candidateNote{
				newNote("proj-a", "retry-note", "We standardized linear retry backoff here."),
				newNote("proj-b", "retry-note-b", "This linear retry backoff differs slightly."),
				newNote("proj-c", "unrelated", "Completely unrelated database migration notes."),
			},
			wantGroups: 1,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if recurrenceMinSharedTerms != 3 {
				t.Skip("test fixtures are tuned for recurrenceMinSharedTerms == 3; update fixtures if the const changes")
			}
			groups := detectCandidateGroups(tt.notes)
			if len(groups) != tt.wantGroups {
				t.Errorf("detectCandidateGroups() = %d groups, want %d: %+v", len(groups), tt.wantGroups, groups)
			}
		})
	}
}

// TestDetectCandidateGroupsDeterministic proves Δ1's "same fixtures, same
// groups every run, same order" invariant — a set/map-backed union-find has
// no natural iteration order, so this specifically guards the sortKey
// ordering.
func TestDetectCandidateGroupsDeterministic(t *testing.T) {
	mk := func(p, n, b string) candidateNote {
		return candidateNote{Project: p, Name: n, Body: b, terms: significantTerms(n + " " + b)}
	}
	notes := []candidateNote{
		mk("proj-a", "n1", "linear retry backoff strategy across services"),
		mk("proj-b", "n2", "linear retry backoff strategy across deploys"),
		mk("proj-c", "n3", "completely unrelated database migration topic"),
		mk("proj-d", "n4", "completely unrelated database migration effort"),
	}
	g1 := detectCandidateGroups(notes)
	g2 := detectCandidateGroups(notes)
	if len(g1) != len(g2) {
		t.Fatalf("nondeterministic group count: %d vs %d", len(g1), len(g2))
	}
	for i := range g1 {
		if g1[i].sortKey() != g2[i].sortKey() {
			t.Errorf("group %d order differs across runs: %q vs %q", i, g1[i].sortKey(), g2[i].sortKey())
		}
	}
}

// --- Δ: zero candidates short-circuits with NO model call ----------------

func TestRunLearnUserPassNoCandidatesMakesNoModelCall(t *testing.T) {
	t.Setenv("CORTEX_HOME", t.TempDir())
	backend := newLearnScriptedBackend(t)
	cs := newLearnUserEvalSession(t, backend.url())

	rootA, rootB := t.TempDir(), t.TempDir()
	writeProjectNote(t, rootA, "unrelated-a", "Completely unrelated database migration notes for project A.")
	writeProjectNote(t, rootB, "unrelated-b", "A totally different topic about frontend styling choices.")

	reg := &fakeRegistry{projects: map[string]registry.Project{
		"a": {Name: "a", Root: rootA},
		"b": {Name: "b", Root: rootB},
	}}

	result, err := RunLearnUserPass(context.Background(), cs, reg)
	if err != nil {
		t.Fatalf("RunLearnUserPass: %v", err)
	}
	if result.CandidateGroups != 0 {
		t.Errorf("CandidateGroups = %d, want 0", result.CandidateGroups)
	}
	if got := result.Report(); got != "nothing recurring across projects" {
		t.Errorf("Report() = %q, want %q", got, "nothing recurring across projects")
	}
	if backend.callCount() != 0 {
		t.Errorf("backend received %d requests, want 0 — zero candidates must never call the model", backend.callCount())
	}
}

// TestRunLearnUserPassEmptyRegistryMakesNoModelCall is the degenerate case:
// no registered projects at all.
func TestRunLearnUserPassEmptyRegistryMakesNoModelCall(t *testing.T) {
	t.Setenv("CORTEX_HOME", t.TempDir())
	backend := newLearnScriptedBackend(t)
	cs := newLearnUserEvalSession(t, backend.url())

	result, err := RunLearnUserPass(context.Background(), cs, &fakeRegistry{})
	if err != nil {
		t.Fatalf("RunLearnUserPass: %v", err)
	}
	if result.CandidateGroups != 0 {
		t.Errorf("CandidateGroups = %d, want 0", result.CandidateGroups)
	}
	if backend.callCount() != 0 {
		t.Errorf("backend received %d requests, want 0", backend.callCount())
	}
}

// --- Δ2: promotion write lands user-side with the exact provenance line --

func TestRunLearnUserPassPromotesWithProvenanceLine(t *testing.T) {
	t.Setenv("CORTEX_HOME", t.TempDir())
	backend := newLearnScriptedBackend(t)
	cs := newLearnUserEvalSession(t, backend.url())

	rootA, rootB := t.TempDir(), t.TempDir()
	writeProjectNote(t, rootA, "retry-standard", "We standardized linear retry backoff across every service in this project.")
	writeProjectNote(t, rootB, "retry-standard-b", "This project also settled on linear retry backoff across every deploy.")

	reg := &fakeRegistry{projects: map[string]registry.Project{
		"proj-a": {Name: "proj-a", Root: rootA},
		"proj-b": {Name: "proj-b", Root: rootB},
	}}

	// The scripted model deliberately does NOT write its own provenance
	// line — learnUserSystem tells it not to; preparePromotionWrite must
	// supply one mechanically.
	backend.toolCallRound(1, memoryWriteCall("c1", "retry-backoff-everywhere", "Every project standardizes on linear retry backoff."))
	backend.finalRound(2, "promoted the retry-backoff-everywhere note")

	result, err := RunLearnUserPass(context.Background(), cs, reg)
	if err != nil {
		t.Fatalf("RunLearnUserPass: %v", err)
	}
	if result.CandidateGroups != 1 {
		t.Fatalf("CandidateGroups = %d, want 1", result.CandidateGroups)
	}
	if result.NotesWritten != 1 {
		t.Fatalf("NotesWritten = %d, want 1", result.NotesWritten)
	}

	body, err := cs.userMemory.Read("retry-backoff-everywhere")
	if err != nil {
		t.Fatalf("read promoted note: %v", err)
	}
	if !strings.Contains(body, "linear retry backoff") {
		t.Errorf("promoted body = %q, want the promoted fact", body)
	}
	want := "Promoted from: proj-a, proj-b"
	if !strings.Contains(body, want) {
		t.Errorf("promoted body = %q, want it to contain %q", body, want)
	}
	if n := strings.Count(body, "Promoted from:"); n != 1 {
		t.Errorf("promoted body has %d provenance lines, want exactly 1 (no more, no fewer)", n)
	}

	// The note is real: readable off disk via a SEPARATE Store handle opened
	// the same way EnableMemory opens the user tier — not just cs's own
	// in-process handle.
	userDir, err := userhome.Path("memory")
	if err != nil {
		t.Fatalf("userhome.Path: %v", err)
	}
	freshUserMem, err := memory.New(userDir)
	if err != nil {
		t.Fatalf("memory.New: %v", err)
	}
	if _, err := freshUserMem.Read("retry-backoff-everywhere"); err != nil {
		t.Errorf("note not written to disk: %v", err)
	}
}

// TestRunLearnUserPassForcesUserScopeRegardlessOfModelArg proves scope is
// mechanically forced: even if a scripted model named a different scope,
// the note still lands on the user tier (LearnUser has no meaningful
// project tier of its own to write to in the first place).
func TestRunLearnUserPassForcesUserScopeRegardlessOfModelArg(t *testing.T) {
	t.Setenv("CORTEX_HOME", t.TempDir())
	backend := newLearnScriptedBackend(t)
	cs := newLearnUserEvalSession(t, backend.url())

	rootA, rootB := t.TempDir(), t.TempDir()
	writeProjectNote(t, rootA, "retry-standard", "We standardized linear retry backoff across every service in this project.")
	writeProjectNote(t, rootB, "retry-standard-b", "This project also settled on linear retry backoff across every deploy.")
	reg := &fakeRegistry{projects: map[string]registry.Project{
		"proj-a": {Name: "proj-a", Root: rootA},
		"proj-b": {Name: "proj-b", Root: rootB},
	}}

	backend.toolCallRound(1, memoryWriteCall("c1", "retry-backoff-everywhere", "Every project standardizes on linear retry backoff."))
	backend.finalRound(2, "promoted")

	if _, err := RunLearnUserPass(context.Background(), cs, reg); err != nil {
		t.Fatalf("RunLearnUserPass: %v", err)
	}
	if _, err := cs.userMemory.Read("retry-backoff-everywhere"); err != nil {
		t.Errorf("promoted note not found on the user tier: %v", err)
	}
}

// --- dedup: re-running the pass promotes nothing new ----------------------

func TestRunLearnUserPassDedupSkipsAlreadyPromotedGroup(t *testing.T) {
	t.Setenv("CORTEX_HOME", t.TempDir())
	backend := newLearnScriptedBackend(t)
	cs := newLearnUserEvalSession(t, backend.url())

	rootA, rootB := t.TempDir(), t.TempDir()
	writeProjectNote(t, rootA, "retry-standard", "We standardized linear retry backoff across every service in this project.")
	writeProjectNote(t, rootB, "retry-standard-b", "This project also settled on linear retry backoff across every deploy.")
	reg := &fakeRegistry{projects: map[string]registry.Project{
		"proj-a": {Name: "proj-a", Root: rootA},
		"proj-b": {Name: "proj-b", Root: rootB},
	}}

	backend.toolCallRound(1, memoryWriteCall("c1", "retry-backoff-everywhere", "Every project standardizes on linear retry backoff."))
	backend.finalRound(2, "promoted the retry-backoff-everywhere note")

	if _, err := RunLearnUserPass(context.Background(), cs, reg); err != nil {
		t.Fatalf("first RunLearnUserPass: %v", err)
	}
	callsAfterFirst := backend.callCount()

	// Re-run: same two projects, same notes still on disk (LearnUser has no
	// cursor — it re-scans every time), but the group's exact source-project
	// set is already promoted — groupAlreadyPromoted must short-circuit it
	// without a model call.
	result2, err := RunLearnUserPass(context.Background(), cs, reg)
	if err != nil {
		t.Fatalf("second RunLearnUserPass: %v", err)
	}
	if result2.CandidateGroups != 1 {
		t.Errorf("second pass CandidateGroups = %d, want 1 (still detected)", result2.CandidateGroups)
	}
	if result2.GroupsSkipped != 1 {
		t.Errorf("second pass GroupsSkipped = %d, want 1 (already promoted)", result2.GroupsSkipped)
	}
	if result2.NotesWritten != 0 {
		t.Errorf("second pass NotesWritten = %d, want 0", result2.NotesWritten)
	}
	if backend.callCount() != callsAfterFirst {
		t.Errorf("second pass made %d more model round-trips, want 0 (an already-promoted group must not call the model)", backend.callCount()-callsAfterFirst)
	}

	metas, err := cs.userMemory.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(metas) != 1 {
		t.Errorf("user memory has %d notes after re-run, want 1 (no duplicate promotion)", len(metas))
	}
}

// --- Δ4: the promotion un-link rule ---------------------------------------

func TestUnlinkAbsentTargetsStripsOnlyUnresolvableLinks(t *testing.T) {
	t.Setenv("CORTEX_HOME", t.TempDir())
	userMem, err := memory.New(t.TempDir())
	if err != nil {
		t.Fatalf("memory.New: %v", err)
	}
	if _, err := userMem.Write("already-promoted", "an existing promoted fact.", time.Now()); err != nil {
		t.Fatalf("seed note: %v", err)
	}

	content := "See [[already-promoted]] for background; this also relates to [[local-only]] which was never promoted."
	got, err := unlinkAbsentTargets(content, userMem)
	if err != nil {
		t.Fatalf("unlinkAbsentTargets: %v", err)
	}
	if !strings.Contains(got, "[[already-promoted]]") {
		t.Errorf("got = %q, want the resolvable link to survive in bracket form", got)
	}
	if strings.Contains(got, "[[local-only]]") {
		t.Errorf("got = %q, want the unresolvable link's brackets stripped", got)
	}
	if !strings.Contains(got, "local-only") {
		t.Errorf("got = %q, want the unresolvable link's TEXT to survive (just not the brackets)", got)
	}
}

// TestRunLearnUserPassPromotionUnlinksAbsentTarget is Δ4 exercised through
// the full promotion write path (preparePromotionWrite, via the dispatcher
// study.go wires for LearnUser) — not just the helper in isolation.
func TestRunLearnUserPassPromotionUnlinksAbsentTarget(t *testing.T) {
	t.Setenv("CORTEX_HOME", t.TempDir())
	backend := newLearnScriptedBackend(t)
	cs := newLearnUserEvalSession(t, backend.url())

	if _, err := cs.userMemory.Write("already-promoted", "an existing promoted fact.", time.Now()); err != nil {
		t.Fatalf("seed user note: %v", err)
	}

	rootA, rootB := t.TempDir(), t.TempDir()
	writeProjectNote(t, rootA, "retry-standard", "We standardized linear retry backoff across every service in this project.")
	writeProjectNote(t, rootB, "retry-standard-b", "This project also settled on linear retry backoff across every deploy.")
	reg := &fakeRegistry{projects: map[string]registry.Project{
		"proj-a": {Name: "proj-a", Root: rootA},
		"proj-b": {Name: "proj-b", Root: rootB},
	}}

	body := "Every project standardizes on linear retry backoff — see [[already-promoted]] and [[local-only]]."
	backend.toolCallRound(1, memoryWriteCall("c1", "retry-backoff-everywhere", body))
	backend.finalRound(2, "promoted")

	if _, err := RunLearnUserPass(context.Background(), cs, reg); err != nil {
		t.Fatalf("RunLearnUserPass: %v", err)
	}

	got, err := cs.userMemory.Read("retry-backoff-everywhere")
	if err != nil {
		t.Fatalf("read promoted note: %v", err)
	}
	if !strings.Contains(got, "[[already-promoted]]") {
		t.Errorf("promoted body = %q, want the resolvable link to survive in bracket form", got)
	}
	if strings.Contains(got, "[[local-only]]") {
		t.Errorf("promoted body = %q, want the unresolvable link's brackets stripped", got)
	}
	if !strings.Contains(got, "local-only") {
		t.Errorf("promoted body = %q, want the unresolvable link's TEXT to survive", got)
	}
}

// --- Forbidden tools are refused (mirrors Learn's own allowlist test) ----

func TestLearnUserForbiddenToolsAreRefused(t *testing.T) {
	backend := newLearnScriptedBackend(t)
	cs := newLearnUserEvalSession(t, backend.url())

	group := candidateGroup{Notes: []candidateNote{
		{Project: "proj-a", Name: "n1", Body: "linear retry backoff strategy"},
		{Project: "proj-b", Name: "n2", Body: "linear retry backoff strategy"},
	}}

	backend.toolCallRound(1, bashCall("c1", "rm -rf /"), writeFileCall("c2", "notes.md", "hi"))
	backend.finalRound(2, "NO_PROMOTION")

	seed := learnUserSeed(group, "")
	if _, _, err := cs.runSubagentStats(context.Background(), tools.LearnUser, seed); err != nil {
		t.Fatalf("runSubagentStats: %v", err)
	}

	wire := backend.wireAt(2)
	if wire == nil {
		t.Fatal("no wire captured for round 2")
	}
	var sawBashRefusal, sawWriteRefusal bool
	for _, m := range wire {
		if m.Role != RoleTool {
			continue
		}
		lower := strings.ToLower(m.Content)
		if m.ToolCallID == "c1" && strings.Contains(lower, "not available here") {
			sawBashRefusal = true
		}
		if m.ToolCallID == "c2" && strings.Contains(lower, "not available here") {
			sawWriteRefusal = true
		}
	}
	if !sawBashRefusal {
		t.Errorf("bash call was not refused as unavailable; wire = %+v", wire)
	}
	if !sawWriteRefusal {
		t.Errorf("write_file call was not refused as unavailable; wire = %+v", wire)
	}
}

// --- scope routing through the loop firing path ---------------------------

func TestRunLoopFiringLearnUserScopeRunsPromotionPassAndJournals(t *testing.T) {
	t.Setenv("CORTEX_HOME", t.TempDir())

	rootA, rootB := t.TempDir(), t.TempDir()
	writeProjectNote(t, rootA, "retry-standard", "We standardized linear retry backoff across every service in this project.")
	writeProjectNote(t, rootB, "retry-standard-b", "This project also settled on linear retry backoff across every deploy.")

	reg := &fakeRegistry{projects: map[string]registry.Project{
		"proj-a": {Name: "proj-a", Root: rootA},
		"proj-b": {Name: "proj-b", Root: rootB},
	}}

	backend := newLearnScriptedBackend(t)
	backend.toolCallRound(1, memoryWriteCall("c1", "retry-backoff-everywhere", "Every project standardizes on linear retry backoff."))
	backend.finalRound(2, "promoted")

	spec := loops.Spec{Name: "nightly-promote", Kind: loops.KindLearn, Scope: loops.ScopeUser}

	if err := RunLoopFiring(context.Background(), spec, reg, nil, learnKindTestSessionFactory(t, backend.url())); err != nil {
		t.Fatalf("RunLoopFiring: %v", err)
	}

	entries := readLoopRunEntries(t)
	if len(entries) != 1 {
		t.Fatalf("loop.run entries = %d, want 1: %+v", len(entries), entries)
	}
	got := entries[0]
	if got.Outcome != journal.LoopOutcomeSuccess {
		t.Errorf("Outcome = %q, want %q", got.Outcome, journal.LoopOutcomeSuccess)
	}
	if !strings.Contains(got.Reason, "candidate group") {
		t.Errorf("Reason = %q, want it to summarize the promotion pass", got.Reason)
	}
	if got.ChangeRef != "" {
		t.Errorf("ChangeRef = %q, want empty — a promotion firing writes memory notes, not source files", got.ChangeRef)
	}

	// loop.run is the user-level journal (internal/journal/loop.go) —
	// confirmed already true before this change; this assertion pins that a
	// scope:"user" learn firing goes through the exact same choke point as
	// every other loop firing, not a separate path.
	userJournalDir, err := userhome.Path("journal", "loop")
	if err != nil {
		t.Fatalf("userhome.Path: %v", err)
	}
	if _, err := os.Stat(userJournalDir); err != nil {
		t.Errorf("user-level loop journal dir not written: %v", err)
	}

	// The note landed in the USER tier — via a fresh Store handle opened the
	// same way EnableMemory opens it, since RunLoopFiring built its own
	// session internally (learnKindTestSessionFactory) and this test has no
	// handle to it.
	userMemDir, err := userhome.Path("memory")
	if err != nil {
		t.Fatalf("userhome.Path: %v", err)
	}
	freshUserMem, err := memory.New(userMemDir)
	if err != nil {
		t.Fatalf("memory.New: %v", err)
	}
	body, err := freshUserMem.Read("retry-backoff-everywhere")
	if err != nil {
		t.Fatalf("read promoted note: %v", err)
	}
	if !strings.Contains(body, "Promoted from: proj-a, proj-b") {
		t.Errorf("note body = %q, want the provenance line", body)
	}
}

func TestRunLoopFiringLearnUserScopeWithNoCandidatesJournalsSuccess(t *testing.T) {
	t.Setenv("CORTEX_HOME", t.TempDir())

	reg := &fakeRegistry{}
	backend := newLearnScriptedBackend(t)
	spec := loops.Spec{Name: "nightly-promote", Kind: loops.KindLearn, Scope: loops.ScopeUser}

	if err := RunLoopFiring(context.Background(), spec, reg, nil, learnKindTestSessionFactory(t, backend.url())); err != nil {
		t.Fatalf("RunLoopFiring: %v", err)
	}

	entries := readLoopRunEntries(t)
	if len(entries) != 1 {
		t.Fatalf("loop.run entries = %d, want 1: %+v", len(entries), entries)
	}
	if entries[0].Outcome != journal.LoopOutcomeSuccess {
		t.Errorf("Outcome = %q, want %q", entries[0].Outcome, journal.LoopOutcomeSuccess)
	}
	if !strings.Contains(entries[0].Reason, "nothing recurring") {
		t.Errorf("Reason = %q, want it to report nothing recurring", entries[0].Reason)
	}
	if backend.callCount() != 0 {
		t.Errorf("backend received %d requests, want 0 (empty registry)", backend.callCount())
	}
}

// TestRunLoopFiringKindLearnProjectScopeStillUsesLearnEngine is a regression
// guard: a spec with Kind:"learn" and Scope unset (or "project") must still
// route to the EXISTING per-project runLearnFiring, not the new
// runLearnUserFiring — Scope is additive, it must not change today's
// default behavior.
func TestRunLoopFiringKindLearnProjectScopeStillUsesLearnEngine(t *testing.T) {
	t.Setenv("CORTEX_HOME", t.TempDir())
	root := t.TempDir()

	seedCS := &CortexSession{quiet: true}
	ws, err := NewWorkspace(root)
	if err != nil {
		t.Fatalf("NewWorkspace: %v", err)
	}
	seedCS.workspace = ws
	seedCS.EnableMemory()
	seedCaptureTurn(t, seedCS, "we use table-driven tests here by convention", "→ acknowledged, continuing")

	backend := newLearnScriptedBackend(t)
	backend.toolCallRound(1, memoryWriteCall("c1", "test-convention", "The project uses table-driven tests by convention."))
	backend.finalRound(2, "saved the table-driven-tests convention")

	reg := &fakeRegistry{projects: map[string]registry.Project{"blog": {Name: "blog", Root: root}}}
	spec := loops.Spec{Name: "nightly-learn", Project: "blog", Kind: loops.KindLearn, Scope: loops.ScopeProject}

	if err := RunLoopFiring(context.Background(), spec, reg, nil, learnKindTestSessionFactory(t, backend.url())); err != nil {
		t.Fatalf("RunLoopFiring: %v", err)
	}

	// The note landed in the PROJECT's memory store — the per-project Learn
	// engine ran, not the user-scope promotion pass.
	body, err := os.ReadFile(filepath.Join(root, ".cortex", "memory", "test-convention.md"))
	if err != nil {
		t.Fatalf("read saved note: %v", err)
	}
	if !strings.Contains(string(body), "table-driven") {
		t.Errorf("note body = %q, want it to contain the saved fact", string(body))
	}
}

// --- config/model-binding wiring spot-checks (mirror the Learn tests in --
// --- config_full_configurability_test.go) ---------------------------------

func TestSubagentRequestLearnUserProfileStaysOnStudyBinding(t *testing.T) {
	study := ModelSpec{Model: "study-m", Endpoint: "http://study.example", RequestTimeoutSec: 111}
	coder := &AgentRequest{
		Model: "coder-live", BaseURL: "http://coder.example", APIKey: "sk-live",
		Timeout: 222 * time.Second, MaxAttempts: 5, Backoff: 3 * time.Second,
	}

	withCoder := &CortexSession{Study: study, Request: coder}
	req := withCoder.subagentRequest(tools.Subagent{Role: roleLearnUser}, "seed")
	if req.Model != study.Model {
		t.Errorf("learn_user profile Model = %q, want the study binding's %q (not the coder's live %q)", req.Model, study.Model, coder.Model)
	}
	if req.Timeout != 111*time.Second {
		t.Errorf("learn_user profile Timeout = %v, want study role's 111s", req.Timeout)
	}

	withoutCoder := &CortexSession{Study: study}
	req2 := withoutCoder.subagentRequest(tools.Subagent{Role: roleLearnUser}, "seed")
	if req2.Model != study.Model {
		t.Errorf("learn_user profile Model with no live coder request = %q, want the study binding's %q", req2.Model, study.Model)
	}
}

func TestConfigSubagentLearnUserBoundsOverrideReachesProfile(t *testing.T) {
	originalBounds := tools.LearnUser.Bounds
	t.Cleanup(func() {
		if tools.LearnUser.Bounds != originalBounds {
			t.Errorf("tools.LearnUser.Bounds was mutated: got %+v, want unchanged %+v", tools.LearnUser.Bounds, originalBounds)
		}
	})

	cfg := &Config{Subagents: SubagentsConfig{
		Learn:     SubagentProfileConfig{MaxTokens: 999, MaxIter: 4, ReadBudgetBytes: 6000},
		LearnUser: SubagentProfileConfig{MaxTokens: 111, MaxIter: 2, ReadBudgetBytes: 3000},
	}}

	sa := tools.LearnUser
	sa.Bounds = cfg.subagentBounds(sa.Role, sa.Bounds)
	if sa.Bounds.MaxTokens != 111 {
		t.Errorf("overridden LearnUser Bounds.MaxTokens = %d, want 111 (not Learn's 999 — the sections must not alias)", sa.Bounds.MaxTokens)
	}
	if sa.Bounds.MaxIter != 2 {
		t.Errorf("overridden LearnUser Bounds.MaxIter = %d, want 2", sa.Bounds.MaxIter)
	}
	if sa.Bounds.ReadBudgetBytes != 3000 {
		t.Errorf("overridden LearnUser Bounds.ReadBudgetBytes = %d, want 3000", sa.Bounds.ReadBudgetBytes)
	}
}

// --- `cortex learn --user` CLI routing (canned) ---------------------------

// TestUserFlagDetection covers the `cortex learn --user` CLI routing
// decision (learn_user.go's userFlag, dispatched from runLearnCLI): the
// engine itself (RunLearnUserPass) is covered exhaustively above, mirroring
// how the codebase already tests `cortex learn`/`cortex turn`'s ENGINE
// (RunLearningPass/RunLoopFiring) directly rather than the os.Args-level
// CLI wrapper, which does no logic of its own beyond routing +
// config/session construction.
func TestUserFlagDetection(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want bool
	}{
		{"no flags", []string{}, false},
		{"project flag only", []string{"--project", "blog"}, false},
		{"user flag alone", []string{"--user"}, true},
		{"user flag among others", []string{"--project", "blog", "--user"}, true},
		{"unrelated flag", []string{"--focus", "something"}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := userFlag(tt.args); got != tt.want {
				t.Errorf("userFlag(%v) = %v, want %v", tt.args, got, tt.want)
			}
		})
	}
}
