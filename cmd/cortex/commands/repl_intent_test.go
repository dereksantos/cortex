package commands

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dereksantos/cortex/internal/journal"
	"github.com/dereksantos/cortex/internal/storage"
	"github.com/dereksantos/cortex/pkg/cognition/dag"
	"github.com/dereksantos/cortex/pkg/cognition/dag/ops"
	"github.com/dereksantos/cortex/pkg/config"
	"github.com/dereksantos/cortex/pkg/events"
)

func TestSeedForIntent_greetingAboveThresholdRoutesToPassthrough(t *testing.T) {
	seed := seedForIntent("greeting", 0.9, "hello")
	if len(seed) != 1 {
		t.Fatalf("expected 1 seed node, got %d", len(seed))
	}
	if seed[0].QualifiedName() != "act.passthrough" {
		t.Errorf("greeting (high conf) must route to act.passthrough, got %q", seed[0].QualifiedName())
	}
	if p, _ := seed[0].Attrs["prompt"].(string); p != "hello" {
		t.Errorf("seed must carry the original prompt, got %q", p)
	}
}

func TestSeedForIntent_greetingBelowThresholdFallsThrough(t *testing.T) {
	// Below the confidence threshold the trivial path is unsafe — a
	// canned "Hi" to someone who actually wanted help is worse than
	// paying the coding-turn cost.
	seed := seedForIntent("greeting", 0.5, "hello")
	if seed[0].QualifiedName() != "sense.prompt" {
		t.Errorf("low-confidence greeting must fall through to sense.prompt, got %q", seed[0].QualifiedName())
	}
}

func TestSeedForIntent_clarifyAboveThresholdRoutesToDecideClarify(t *testing.T) {
	seed := seedForIntent("clarify", 0.9, "do the thing")
	if seed[0].QualifiedName() != "decide.clarify" {
		t.Errorf("clarify (high conf) must route to decide.clarify, got %q", seed[0].QualifiedName())
	}
}

func TestSeedForIntent_recallAboveThresholdRoutesToRecallSummary(t *testing.T) {
	seed := seedForIntent("recall", 0.9, "what did we decide about postgres?")
	if seed[0].QualifiedName() != "decide.recall_summary" {
		t.Errorf("recall (high conf) must route to decide.recall_summary, got %q", seed[0].QualifiedName())
	}
}

func TestSeedForIntent_lowConfidenceAlwaysFallsThrough(t *testing.T) {
	// Below the confidence threshold every intent falls through to
	// sense.prompt — the trivial-intent short-circuits would do the
	// wrong thing without confidence backing them.
	for _, intent := range []string{"greeting", "clarify", "recall", "code", "review", "meta"} {
		t.Run(intent, func(t *testing.T) {
			seed := seedForIntent(intent, 0.5, "do the thing")
			if seed[0].QualifiedName() != "sense.prompt" {
				t.Errorf("intent=%q at low confidence must seed sense.prompt, got %q", intent, seed[0].QualifiedName())
			}
		})
	}
}

func TestSeedForIntent_nonShortCircuitIntentsAlwaysFallThrough(t *testing.T) {
	// code / review / meta / unknown have no dedicated terminal node —
	// they always seed sense.prompt regardless of confidence.
	for _, intent := range []string{"code", "review", "meta", "unknown"} {
		t.Run(intent, func(t *testing.T) {
			seed := seedForIntent(intent, 0.95, "do the thing")
			if seed[0].QualifiedName() != "sense.prompt" {
				t.Errorf("intent=%q has no dedicated terminal — must seed sense.prompt, got %q",
					intent, seed[0].QualifiedName())
			}
		})
	}
}

func TestAggregateByIntent_empty(t *testing.T) {
	if got := aggregateByIntent(nil); got != nil {
		t.Errorf("empty input → nil output, got %+v", got)
	}
}

func TestAggregateByIntent_groupsCountsAndSums(t *testing.T) {
	rows := []turnRow{
		{Intent: "code", LatencyMs: 100, TokensIn: 50, TokensOut: 30, CostUSD: 0.01},
		{Intent: "code", LatencyMs: 200, TokensIn: 60, TokensOut: 40, CostUSD: 0.02},
		{Intent: "code", LatencyMs: 300, TokensIn: 70, TokensOut: 50, CostUSD: 0.03},
		{Intent: "greeting", LatencyMs: 10, TokensIn: 0, TokensOut: 5, CostUSD: 0},
		{Intent: "recall", LatencyMs: 5000, TokensIn: 200, TokensOut: 100, CostUSD: 0.05},
	}
	out := aggregateByIntent(rows)
	if len(out) != 3 {
		t.Fatalf("expected 3 intents, got %d", len(out))
	}
	// Sorted by Count desc: code(3), greeting(1), recall(1) — secondary
	// alphabetical for ties.
	if out[0].Intent != "code" || out[0].Count != 3 {
		t.Errorf("first bucket = (%q, %d), want (code, 3)", out[0].Intent, out[0].Count)
	}
	if out[0].TotalTokensIn != 180 || out[0].TotalTokensOut != 120 {
		t.Errorf("code tokens not summed: in=%d out=%d", out[0].TotalTokensIn, out[0].TotalTokensOut)
	}
	if out[0].P50LatencyMs != 200 {
		t.Errorf("code P50 = %d, want 200", out[0].P50LatencyMs)
	}
	if out[0].P95LatencyMs != 300 {
		t.Errorf("code P95 = %d, want 300", out[0].P95LatencyMs)
	}
	// Ties: greeting and recall both Count=1; alphabetical secondary
	// puts greeting first.
	if out[1].Intent != "greeting" {
		t.Errorf("tie-break order wrong: out[1]=%q (expected greeting)", out[1].Intent)
	}
}

func TestAggregateByIntent_foldsEmptyIntentIntoNoneBucket(t *testing.T) {
	// Legacy turnRows (written before Slice 3) have empty Intent. They
	// must show up in a "(none)" bucket so analysis sees them rather
	// than silently dropping them.
	rows := []turnRow{
		{Intent: "", LatencyMs: 100},
		{Intent: "", LatencyMs: 200},
		{Intent: "code", LatencyMs: 50},
	}
	out := aggregateByIntent(rows)
	if len(out) != 2 {
		t.Fatalf("expected 2 buckets, got %d", len(out))
	}
	found := false
	for _, r := range out {
		if r.Intent == "(none)" && r.Count == 2 {
			found = true
		}
	}
	if !found {
		t.Errorf("expected (none) bucket with Count=2, got %+v", out)
	}
}

func TestAggregateByIntent_singleRowP50EqualsP95(t *testing.T) {
	rows := []turnRow{{Intent: "code", LatencyMs: 777}}
	out := aggregateByIntent(rows)
	if len(out) != 1 {
		t.Fatalf("expected 1 bucket, got %d", len(out))
	}
	if out[0].P50LatencyMs != 777 || out[0].P95LatencyMs != 777 {
		t.Errorf("single-row p50/p95 must both equal the value, got p50=%d p95=%d",
			out[0].P50LatencyMs, out[0].P95LatencyMs)
	}
}

func TestPercentile_edgeCases(t *testing.T) {
	if percentile(nil, 0.5) != 0 {
		t.Error("empty slice must return 0")
	}
	if percentile([]int64{42}, 0.99) != 42 {
		t.Error("single element must return that element")
	}
	// p=0 → smallest, p=1 → largest.
	sorted := []int64{1, 2, 3, 4, 5}
	if percentile(sorted, 0) != 1 {
		t.Errorf("p=0 should be smallest, got %d", percentile(sorted, 0))
	}
	if percentile(sorted, 1.0) != 5 {
		t.Errorf("p=1.0 should be largest, got %d", percentile(sorted, 1.0))
	}
}

func TestDetectFeedbackCue_correction(t *testing.T) {
	corrections := []string{
		"no, do it differently",
		"wrong file, try main.go",
		"actually use pgx not database/sql",
		"that's not what I meant",
		"don't use redis",
		"NOPE. start over",
		"Use foo instead",
	}
	for _, p := range corrections {
		t.Run(p, func(t *testing.T) {
			if got := detectFeedbackCue(p); got != "correction" {
				t.Errorf("expected correction for %q, got %q", p, got)
			}
		})
	}
}

func TestDetectFeedbackCue_confirmation(t *testing.T) {
	confirms := []string{
		"perfect, thanks",
		"thanks!",
		"yes that worked",
		"exactly what I wanted",
		"got it, moving on",
		"that works",
		"nice",
	}
	for _, p := range confirms {
		t.Run(p, func(t *testing.T) {
			if got := detectFeedbackCue(p); got != "confirmation" {
				t.Errorf("expected confirmation for %q, got %q", p, got)
			}
		})
	}
}

func TestDetectFeedbackCue_neither(t *testing.T) {
	noCue := []string{
		"add a print to main.go",
		"how does auth work?",
		"what files are in this directory?",
		"hi",
		"", // empty
	}
	for _, p := range noCue {
		t.Run(p, func(t *testing.T) {
			if got := detectFeedbackCue(p); got != "" {
				t.Errorf("expected no cue for %q, got %q", p, got)
			}
		})
	}
}

func TestDetectFeedbackCue_falsePositiveGuards(t *testing.T) {
	// "no problem" and friends must NOT trip the correction marker.
	// The space-padded marker "no, " requires a comma; bare "no
	// problem" should fall through to no cue (or confirmation, but
	// not correction).
	cases := map[string]string{
		"no problem":                 "",
		"no idea":                    "",
		"no worries":                 "",
		"no, this is wrong actually": "correction", // both fire; correction wins
	}
	for prompt, want := range cases {
		t.Run(prompt, func(t *testing.T) {
			if got := detectFeedbackCue(prompt); got != want {
				t.Errorf("prompt=%q: got %q, want %q", prompt, got, want)
			}
		})
	}
}

func TestEmitFeedbackEntry_skipsOnTurnZero(t *testing.T) {
	// Turn 1 has no prior turn to grade. Auto-emit must skip silently
	// without erroring or writing a bogus entry.
	tempDir, err := os.MkdirTemp("", "cortex-feedback-test-*")
	if err != nil {
		t.Fatalf("mktemp: %v", err)
	}
	defer os.RemoveAll(tempDir)
	s := &replState{workdir: tempDir, sessionID: "test", turns: 0}
	if err := s.emitFeedbackEntry("correction", "no, do it differently"); err != nil {
		t.Fatalf("emitFeedbackEntry on turn 0 must not error: %v", err)
	}
	// No feedback dir should have been created.
	if _, err := os.Stat(filepath.Join(tempDir, ".cortex", "journal", "feedback")); !os.IsNotExist(err) {
		t.Errorf("turn-0 emit must not create feedback dir: stat err = %v", err)
	}
}

func TestEmitFeedbackEntry_writesCorrectionToJournal(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "cortex-feedback-test-*")
	if err != nil {
		t.Fatalf("mktemp: %v", err)
	}
	defer os.RemoveAll(tempDir)
	s := &replState{workdir: tempDir, sessionID: "session-abc", turns: 3}

	if err := s.emitFeedbackEntry("correction", "no, use pgx instead"); err != nil {
		t.Fatalf("emitFeedbackEntry: %v", err)
	}
	classDir := filepath.Join(tempDir, ".cortex", "journal", "feedback")
	r, err := journal.NewReader(classDir)
	if err != nil {
		t.Fatalf("open reader: %v", err)
	}
	defer r.Close()
	e, err := r.Next()
	if err != nil {
		t.Fatalf("read entry: %v", err)
	}
	if e.Type != journal.TypeFeedbackCorrection {
		t.Errorf("type = %q, want %q", e.Type, journal.TypeFeedbackCorrection)
	}
	p, err := journal.ParseFeedback(e)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if p.GradedID != "repl-session-abc-turn-3" {
		t.Errorf("GradedID = %q, want repl-session-abc-turn-3", p.GradedID)
	}
	if !strings.Contains(p.Note, "pgx") {
		t.Errorf("Note should preserve user prompt content, got %q", p.Note)
	}
	if p.SessionID != "session-abc" {
		t.Errorf("SessionID = %q, want session-abc", p.SessionID)
	}
}

func TestEmitFeedbackEntry_writesConfirmation(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "cortex-feedback-test-*")
	if err != nil {
		t.Fatalf("mktemp: %v", err)
	}
	defer os.RemoveAll(tempDir)
	s := &replState{workdir: tempDir, sessionID: "s1", turns: 5}

	if err := s.emitFeedbackEntry("confirmation", "perfect, thanks"); err != nil {
		t.Fatalf("emitFeedbackEntry: %v", err)
	}
	r, _ := journal.NewReader(filepath.Join(tempDir, ".cortex", "journal", "feedback"))
	defer r.Close()
	e, _ := r.Next()
	if e.Type != journal.TypeFeedbackConfirmation {
		t.Errorf("type = %q, want %q", e.Type, journal.TypeFeedbackConfirmation)
	}
}

func TestEmitFeedbackEntry_unknownCueIsNoOp(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "cortex-feedback-test-*")
	if err != nil {
		t.Fatalf("mktemp: %v", err)
	}
	defer os.RemoveAll(tempDir)
	s := &replState{workdir: tempDir, sessionID: "s", turns: 1}
	if err := s.emitFeedbackEntry("", "anything"); err != nil {
		t.Errorf("empty cue must not error: %v", err)
	}
	if err := s.emitFeedbackEntry("nonsense", "anything"); err != nil {
		t.Errorf("unknown cue must not error: %v", err)
	}
	if _, err := os.Stat(filepath.Join(tempDir, ".cortex", "journal", "feedback")); !os.IsNotExist(err) {
		t.Errorf("unknown/empty cue must not create feedback dir")
	}
}

func TestStitchClarifyFollowUp_noPriorClarifyIsPassThrough(t *testing.T) {
	s := &replState{}
	got := s.stitchClarifyFollowUp("fix the bug")
	if got != "fix the bug" {
		t.Errorf("expected passthrough when lastClarifyPrompt empty, got %q", got)
	}
}

func TestStitchClarifyFollowUp_combinesAndResets(t *testing.T) {
	s := &replState{lastClarifyPrompt: "delete it"}
	got := s.stitchClarifyFollowUp("the migrations table")
	if !strings.Contains(got, "delete it") {
		t.Errorf("stitched prompt missing original: %q", got)
	}
	if !strings.Contains(got, "the migrations table") {
		t.Errorf("stitched prompt missing user answer: %q", got)
	}
	// One-shot: next call must NOT stitch again.
	if s.lastClarifyPrompt != "" {
		t.Errorf("lastClarifyPrompt must reset after consumption, got %q", s.lastClarifyPrompt)
	}
	second := s.stitchClarifyFollowUp("another prompt")
	if second != "another prompt" {
		t.Errorf("second call should be passthrough, got %q", second)
	}
}

func TestClassifyIntentForTurn_missingRegistrationFallsBackToCode(t *testing.T) {
	// A registry without sense.classify_intent must yield the safe
	// default — never block the turn on a missing op registration.
	reg := dag.NewRegistry()
	intent, conf := classifyIntentForTurn(reg, nil, "hello", nil)
	if intent != ops.IntentCode {
		t.Errorf("expected fallback intent=%q, got %q", ops.IntentCode, intent)
	}
	if conf != 0 {
		t.Errorf("expected fallback confidence=0, got %v", conf)
	}
}

func TestClassifyIntentForTurn_emitsTraceEntryWhenCallbackProvided(t *testing.T) {
	// Slice 6: the classifier runs outside the DAG executor, so we
	// synthesize a TraceEntry and invoke the same callback the
	// executor uses. The entry must show up with the right
	// QualifiedName, spawn the seed node, and carry the classification
	// result in Out so dag_traces.jsonl / dag-color visualizers can
	// surface it like any other DAG node.
	reg := dag.NewRegistry()
	if err := reg.Register(ops.ClassifyIntentSpec(ops.ClassifyIntentConfig{Provider: nil})); err != nil {
		t.Fatalf("register: %v", err)
	}
	var emitted []dag.TraceEntry
	cb := func(e dag.TraceEntry) { emitted = append(emitted, e) }

	classifyIntentForTurn(reg, nil, "hello", cb)

	if len(emitted) != 1 {
		t.Fatalf("expected 1 trace entry, got %d", len(emitted))
	}
	e := emitted[0]
	if e.QualifiedName != "sense.classify_intent" {
		t.Errorf("qualified name = %q, want sense.classify_intent", e.QualifiedName)
	}
	if e.NodeID == "" {
		t.Error("NodeID must be set")
	}
	if e.ParentID != "" {
		t.Errorf("ParentID must be empty (classifier is entry-point), got %q", e.ParentID)
	}
	if len(e.SpawnedChildren) != 1 {
		t.Errorf("expected 1 spawned child (the seed node), got %d", len(e.SpawnedChildren))
	}
	if intent, _ := e.Out["intent"].(string); intent != ops.IntentCode {
		t.Errorf("Out[intent] = %v, want %q", e.Out["intent"], ops.IntentCode)
	}
	if fb, _ := e.Out["fallback"].(bool); !fb {
		t.Error("Out[fallback] must be true for nil-provider path")
	}
	if e.WallEnd.Before(e.WallStart) {
		t.Error("WallEnd must be ≥ WallStart")
	}
}

func TestClassifyIntentForTurn_nilCallbackIsNoOp(t *testing.T) {
	// Production safety: passing nil traceCB must not panic and must
	// not affect the returned classification result.
	reg := dag.NewRegistry()
	intent, conf := classifyIntentForTurn(reg, nil, "hello", nil)
	if intent != ops.IntentCode || conf != 0 {
		t.Errorf("nil callback must still return safe defaults, got (%q, %v)", intent, conf)
	}
}

func TestClassifyIntentForTurn_registryMissEmitsErrorTrace(t *testing.T) {
	// When sense.classify_intent isn't registered, the classifier still
	// emits a trace entry with the failure recorded — that's the
	// observability win. Without the entry, "why did we route to the
	// fallback?" would be invisible to debuggers.
	reg := dag.NewRegistry() // empty — no ops registered
	var emitted []dag.TraceEntry
	cb := func(e dag.TraceEntry) { emitted = append(emitted, e) }

	intent, _ := classifyIntentForTurn(reg, nil, "hello", cb)

	if intent != ops.IntentCode {
		t.Errorf("registry miss must still return safe default")
	}
	if len(emitted) != 1 {
		t.Fatalf("expected 1 trace entry on registry miss, got %d", len(emitted))
	}
	e := emitted[0]
	if e.OK {
		t.Error("trace entry must report OK=false on registry miss")
	}
	if e.ErrorCode == "" {
		t.Error("ErrorCode must be populated on failure")
	}
}

func TestDowngradeRecallIfNoContext_nonRecallIsNoOp(t *testing.T) {
	// Downgrade only applies to intent=recall. Every other intent —
	// including the safe-default "code" — must pass through unchanged
	// regardless of storage state.
	for _, intent := range []string{"greeting", "clarify", "code", "review", "meta", "unknown"} {
		t.Run(intent, func(t *testing.T) {
			got := downgradeRecallIfNoContext(intent, "anything", nil)
			if got != intent {
				t.Errorf("intent=%q: got %q, want unchanged %q", intent, got, intent)
			}
		})
	}
}

func TestDowngradeRecallIfNoContext_nilStorageDowngrades(t *testing.T) {
	// A recall question we can't answer from storage at all (storage
	// not wired) is better served by the agent than by an apology.
	got := downgradeRecallIfNoContext("recall", "what did we decide?", nil)
	if got != ops.IntentCode {
		t.Errorf("nil storage must downgrade recall to %q, got %q", ops.IntentCode, got)
	}
}

func TestDowngradeRecallIfNoContext_emptyStorageDowngrades(t *testing.T) {
	// A real storage with zero events still can't answer recall — the
	// downgrade kicks in here too. This is the cold-journal case that
	// motivated the fix: first-turn user with no captured history asks
	// "where are we" and should get investigation, not "no context".
	store := newTestStorage(t)
	defer store.Close()
	got := downgradeRecallIfNoContext("recall", "what did we decide about postgres?", store)
	if got != ops.IntentCode {
		t.Errorf("empty storage must downgrade recall to %q, got %q", ops.IntentCode, got)
	}
}

func TestDowngradeRecallIfNoContext_storageWithMatchKeepsRecall(t *testing.T) {
	// When storage holds an event whose text matches the prompt, the
	// recall path is the right call — decide.recall_summary can
	// actually ground its answer.
	store := newTestStorage(t)
	defer store.Close()
	// storage.matchesEvent searches ToolName / ToolResult / ToolInput
	// (NOT Event.Prompt — see internal/storage/storage.go:matchesEvent).
	// Use a tool-use event with the keyword in ToolResult so SearchEvents
	// actually hits.
	ev := &events.Event{
		ID:         "test-event-1",
		Source:     events.SourceGeneric,
		EventType:  events.EventToolUse,
		Timestamp:  time.Now(),
		ToolName:   "Edit",
		ToolResult: "decided to use pgx instead of database/sql for postgres",
	}
	if err := store.StoreEvent(ev); err != nil {
		t.Fatalf("StoreEvent: %v", err)
	}
	got := downgradeRecallIfNoContext("recall", "what did we decide about postgres?", store)
	if got != "recall" {
		t.Errorf("matching storage must keep recall, got %q", got)
	}
}

func TestDowngradeRecallIfNoContext_allStopwordsPromptDowngrades(t *testing.T) {
	// "what did we decide?" tokenizes to zero content words (all
	// stopwords / decide-family) — without probe terms we can't ask
	// storage anything useful, so downgrade rather than route to a
	// recall_summary that has nothing to ground on.
	store := newTestStorage(t)
	defer store.Close()
	got := downgradeRecallIfNoContext("recall", "what did we decide?", store)
	if got != ops.IntentCode {
		t.Errorf("zero-term prompt must downgrade, got %q", got)
	}
}

func TestRecallProbeTerms(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want []string // exact, order-sensitive (insertion order)
	}{
		{"empty", "", nil},
		{"stopwords only", "what did we?", nil},
		{"short words filtered", "is a cat", nil}, // all <4 chars
		{"mixed case lowered", "What about POSTGRES?", []string{"postgres"}},
		{"dedupes", "auth auth AUTH", []string{"auth"}},
		{"punctuation strips", "what's the postgres+pgx decision again?", []string{"postgres", "decision"}},
		{"keeps content words", "explain how the cortex DAG seeds work", []string{"explain", "cortex", "seeds", "work"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := recallProbeTerms(tc.in)
			if len(got) != len(tc.want) {
				t.Fatalf("len = %d, want %d (got=%v)", len(got), len(tc.want), got)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Errorf("[%d] %q, want %q (got=%v)", i, got[i], tc.want[i], got)
				}
			}
		})
	}
}

// newTestStorage builds an isolated Storage backed by a temp dir.
// Closes via defer in the test.
func newTestStorage(t *testing.T) *storage.Storage {
	t.Helper()
	tempDir, err := os.MkdirTemp("", "cortex-intent-test-*")
	if err != nil {
		t.Fatalf("mktemp: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(tempDir) })
	if err := os.MkdirAll(filepath.Join(tempDir, "db"), 0o755); err != nil {
		t.Fatalf("mkdir db: %v", err)
	}
	store, err := storage.New(&config.Config{ContextDir: tempDir})
	if err != nil {
		t.Fatalf("storage.New: %v", err)
	}
	return store
}

func TestClassifyIntentForTurn_registeredHandlerReturnsResult(t *testing.T) {
	// Register classify_intent with a nil provider — the handler's
	// internal fallback returns intent=code,confidence=0. This proves
	// the registry → spec → handler invocation path works without
	// requiring a real provider in unit tests.
	reg := dag.NewRegistry()
	if err := reg.Register(ops.ClassifyIntentSpec(ops.ClassifyIntentConfig{Provider: nil})); err != nil {
		t.Fatalf("register: %v", err)
	}
	intent, conf := classifyIntentForTurn(reg, nil, "hello", nil)
	if intent != ops.IntentCode {
		t.Errorf("nil-provider fallback should yield intent=%q, got %q", ops.IntentCode, intent)
	}
	if conf != 0 {
		t.Errorf("nil-provider fallback should yield confidence=0, got %v", conf)
	}
}
