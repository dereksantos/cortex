package codebase

import (
	"reflect"
	"testing"
)

func TestExtractTraceCounters(t *testing.T) {
	rows := []TraceRow{
		{QualifiedName: "sense.classify_intent"},
		{QualifiedName: "sense.estimate_scope", Out: map[string]any{"budget_tokens": float64(12000)}},
		{QualifiedName: "decide.next"},
		{QualifiedName: "decide.coding_turn", Out: map[string]any{"response": "Here is the answer about repl.go"}},
		{QualifiedName: "act.read_file"},
		{QualifiedName: "act.read_file"},
		{QualifiedName: "act.run_shell"},
		{QualifiedName: "decide.next"},
		{QualifiedName: "decide.coding_turn", Out: map[string]any{"response": "NEED_MORE: more context needed for repl.go"}},
	}
	m := Extract("dummy", rows, nil)
	if m.HopCount != 2 {
		t.Errorf("HopCount = %d, want 2", m.HopCount)
	}
	if m.ReadCount != 2 {
		t.Errorf("ReadCount = %d, want 2", m.ReadCount)
	}
	if m.ShellCount != 1 {
		t.Errorf("ShellCount = %d, want 1", m.ShellCount)
	}
	if m.NeedMore != 1 {
		t.Errorf("NeedMore = %d, want 1", m.NeedMore)
	}
	if m.BudgetTokens != 12000 {
		t.Errorf("BudgetTokens = %d, want 12000", m.BudgetTokens)
	}
}

func TestCitationRegex(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{"see pkg/cognition/dag/registry.go:205 for the lookup", []string{"pkg/cognition/dag/registry.go:205"}},
		{"file cmd/cortex/main.go and docs/repl.md", []string{"cmd/cortex/main.go", "docs/repl.md"}},
		{"internal/eval/v2/cellresult.go:74-83 is the struct", []string{"internal/eval/v2/cellresult.go:74-83"}},
		{"no file here, just prose", nil},
	}
	for _, c := range cases {
		got := CitationRegex.FindAllString(c.in, -1)
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("FindAllString(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

func TestHedgeRegex(t *testing.T) {
	cases := []struct {
		in   string
		want int
	}{
		{"the function may be referenced elsewhere", 1},
		{"this appears to be the entry point", 1},
		{"not directly confirmed in the trace; this seems to be wrong", 2},
		{"plain prose with no hedges", 0},
		{"maybeline is a project name and shouldn't match", 0},
	}
	for _, c := range cases {
		got := len(HedgeRegex.FindAllStringIndex(c.in, -1))
		if got != c.want {
			t.Errorf("hedge count of %q = %d, want %d", c.in, got, c.want)
		}
	}
}

func TestCountUnverifiedTail(t *testing.T) {
	in := "Here's the answer.\n\n## Unverified\n\n- claim a\n- claim b\n- claim c\n\n## References\n\n- foo\n"
	got := countUnverifiedTail(in)
	if got != 3 {
		t.Errorf("countUnverifiedTail = %d, want 3", got)
	}

	clean := "All findings have file citations.\n\n## Summary\n\n- thing\n"
	if got := countUnverifiedTail(clean); got != 0 {
		t.Errorf("countUnverifiedTail clean = %d, want 0", got)
	}
}

func TestCountClaims(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want int
	}{
		{"bullets only", "- one\n- two\n- three", 3},
		{"paragraphs only", "First paragraph.\n\nSecond paragraph.", 2},
		{"heading then bullets", "## Findings\n\n- a\n- b", 2},
		{"empty", "", 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := countClaims(c.in); got != c.want {
				t.Errorf("countClaims(%q) = %d, want %d", c.in, got, c.want)
			}
		})
	}
}

func TestMustCiteMustNotInvent(t *testing.T) {
	fx := &Fixture{
		Expected: Expectation{
			MustCitePaths: []string{"pkg/cognition/dag/", "cmd/cortex/main.go"},
			MustNotInvent: []string{"cortex-data-warehouse", "NewCodingTurnNode"},
		},
	}
	answerOK := "The handler is in pkg/cognition/dag/ops/decide_next.go and registers via NewCodingTurnHandler in coding_turn.go"
	m := Extract(answerOK, nil, fx)
	if !m.MustCitePathsSatisfied {
		t.Error("must_cite_paths_satisfied = false; expected true (path prefix match)")
	}
	if !m.MustNotInventClean {
		t.Errorf("must_not_invent hits = %v; expected clean", m.MustNotInventHits)
	}

	answerBad := "see cortex-data-warehouse for the canonical handler"
	m = Extract(answerBad, nil, fx)
	if m.MustCitePathsSatisfied {
		t.Error("must_cite_paths_satisfied = true; expected false (no path cited)")
	}
	if m.MustNotInventClean {
		t.Error("must_not_invent_clean = true; expected false (invention present)")
	}
}

func TestEvaluateAndAllPass(t *testing.T) {
	exp := Expectation{
		HopCountMin:     2,
		HopCountMax:     5,
		CitationRateMin: 0.5,
		HedgeCountMax:   -1,
		BudgetTokenMin:  50000,
		BudgetTokenMax:  150000,
	}
	good := Metrics{
		HopCount:           3,
		ClaimCount:         4,
		CitationCount:      3,
		CitationRate:       0.75,
		HedgeCount:         0,
		BudgetTokens:       80000,
		BudgetTokenInRange: true,
	}
	bounds := Evaluate(good, exp)
	if !AllPass(bounds) {
		for _, b := range bounds {
			if !b.Pass {
				t.Errorf("bound %s failed: want %s got %s", b.Name, b.Want, b.Got)
			}
		}
	}

	bad := good
	bad.HopCount = 1
	bad.HedgeCount = 4
	bad.BudgetTokens = 5000
	bad.BudgetTokenInRange = false
	bounds = Evaluate(bad, exp)
	if AllPass(bounds) {
		t.Error("AllPass(bad) = true; expected at least one failed bound")
	}
}

func TestBudgetInRangeBoundsOnlyOneSide(t *testing.T) {
	if !inBudgetRange(8000, 5000, 0) {
		t.Error("inBudgetRange 8000 in [5000, inf) should be true")
	}
	if inBudgetRange(3000, 5000, 0) {
		t.Error("inBudgetRange 3000 in [5000, inf) should be false")
	}
	if !inBudgetRange(80000, 0, 100000) {
		t.Error("inBudgetRange 80000 in (-inf, 100000] should be true")
	}
	if inBudgetRange(120000, 0, 100000) {
		t.Error("inBudgetRange 120000 in (-inf, 100000] should be false")
	}
}
