package dag

import "testing"

func TestBudgetForIntent_knownIntents(t *testing.T) {
	tests := []struct {
		intent string
		want   Budget
	}{
		{"greeting", Budget{LatencyMS: 2000, Tokens: 300, Depth: 3, OutputTokens: 500}},
		{"clarify", Budget{LatencyMS: 3000, Tokens: 500, Depth: 3, OutputTokens: 600}},
		{"recall", Budget{LatencyMS: 20000, Tokens: 3000, Depth: 5, OutputTokens: 2000}},
		{"review", Budget{LatencyMS: 60000, Tokens: 5000, Depth: 8, OutputTokens: 4000}},
		{"meta", Budget{LatencyMS: 10000, Tokens: 2000, Depth: 4, OutputTokens: 1500}},
	}
	for _, tc := range tests {
		t.Run(tc.intent, func(t *testing.T) {
			if got := BudgetForIntent(tc.intent); got != tc.want {
				t.Errorf("BudgetForIntent(%q) = %+v, want %+v", tc.intent, got, tc.want)
			}
		})
	}
}

func TestBudgetForIntent_codeIsDefault(t *testing.T) {
	if got, want := BudgetForIntent("code"), DefaultTurnBudget(); got != want {
		t.Errorf("BudgetForIntent(\"code\") = %+v, want DefaultTurnBudget = %+v", got, want)
	}
}

func TestBudgetForIntent_unknownAndEmptyAreDefault(t *testing.T) {
	def := DefaultTurnBudget()
	for _, intent := range []string{"unknown-label", "", "GREETING", "Code"} {
		if got := BudgetForIntent(intent); got != def {
			t.Errorf("BudgetForIntent(%q) = %+v, want DefaultTurnBudget = %+v (case-sensitive: only lowercase known labels match)", intent, got, def)
		}
	}
}

func TestBudgetForIntent_cheapBudgetsCannotAffordCodingTurn(t *testing.T) {
	// Defense-in-depth: greeting / clarify budgets MUST be too tight
	// to spawn decide.coding_turn (Cost{LatencyMS: 15000, Tokens: 2000}).
	// If the classifier mis-routes a code prompt as greeting, the budget
	// gate has to refuse the spawn so the trivial-intent turn can't
	// balloon into a full agent loop.
	codingTurnCost := Cost{LatencyMS: 15000, Tokens: 2000}
	for _, intent := range []string{"greeting", "clarify"} {
		t.Run(intent, func(t *testing.T) {
			b := BudgetForIntent(intent)
			if b.CanAfford(codingTurnCost) {
				t.Errorf("BudgetForIntent(%q) = %+v MUST NOT afford coding_turn cost %+v — defense-in-depth invariant", intent, b, codingTurnCost)
			}
		})
	}
}

func TestBudgetForIntent_recallCanAffordVectorSearch(t *testing.T) {
	// Recall must afford remember.vector_search (Cost{LatencyMS: 0,
	// Tokens: 0}) plus a small synthesis turn. The synthesis is a
	// scaled-down coding_turn; we expect ~10s / 1500 tokens for it.
	synthesis := Cost{LatencyMS: 10000, Tokens: 1500}
	if !BudgetForIntent("recall").CanAfford(synthesis) {
		t.Error("BudgetForIntent(\"recall\") must afford a small synthesis turn")
	}
}

func TestBudget_WithMaxContextTokens(t *testing.T) {
	b := DefaultTurnBudget().WithMaxContextTokens(32768)
	if b.MaxContextTokens != 32768 {
		t.Errorf("MaxContextTokens = %d, want 32768", b.MaxContextTokens)
	}
	// Other axes survive.
	if b.LatencyMS != 150000 || b.Tokens != 10000 {
		t.Errorf("axes corrupted by WithMaxContextTokens: %+v", b)
	}
	// Negative clamps to 0 (unknown).
	if got := b.WithMaxContextTokens(-1).MaxContextTokens; got != 0 {
		t.Errorf("negative should clamp to 0; got %d", got)
	}
}

func TestBudget_PromptBudget_Returns70Percent(t *testing.T) {
	b := Budget{MaxContextTokens: 32768}
	if got, want := b.PromptBudget(), 22937; got != want {
		t.Errorf("PromptBudget = %d, want %d (70%% of 32768)", got, want)
	}
	if got := (Budget{}).PromptBudget(); got != 0 {
		t.Errorf("unknown MaxContextTokens should yield 0; got %d", got)
	}
}

func TestBudget_Consume_LeavesMaxContextTokens(t *testing.T) {
	// MaxContextTokens is a cap, not a consumable — Consume must
	// not touch it even when the cost has no MaxContextTokens-shaped
	// field. Pin this in a test so future contributors don't try.
	b := Budget{LatencyMS: 1000, Tokens: 100, Depth: 5, OutputTokens: 100, MaxContextTokens: 4096}
	b.Consume(Cost{LatencyMS: 100, Tokens: 10, OutputTokens: 10})
	if b.MaxContextTokens != 4096 {
		t.Errorf("Consume must not decay MaxContextTokens; got %d, want 4096", b.MaxContextTokens)
	}
}
