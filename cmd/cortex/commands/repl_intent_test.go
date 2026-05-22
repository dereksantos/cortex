package commands

import (
	"os"
	"path/filepath"
	"testing"
	"time"

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

func TestClassifyIntentForTurn_missingRegistrationFallsBackToCode(t *testing.T) {
	// A registry without sense.classify_intent must yield the safe
	// default — never block the turn on a missing op registration.
	reg := dag.NewRegistry()
	intent, conf := classifyIntentForTurn(reg, "hello")
	if intent != ops.IntentCode {
		t.Errorf("expected fallback intent=%q, got %q", ops.IntentCode, intent)
	}
	if conf != 0 {
		t.Errorf("expected fallback confidence=0, got %v", conf)
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
	intent, conf := classifyIntentForTurn(reg, "hello")
	if intent != ops.IntentCode {
		t.Errorf("nil-provider fallback should yield intent=%q, got %q", ops.IntentCode, intent)
	}
	if conf != 0 {
		t.Errorf("nil-provider fallback should yield confidence=0, got %v", conf)
	}
}
