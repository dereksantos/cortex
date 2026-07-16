package main

import (
	"testing"

	"github.com/dereksantos/cortex/pkg/llm"
)

func TestCountGoalHits(t *testing.T) {
	text := "The Resolve loop dispatches tool calls to Execute."
	if got := countGoalHits(text, []string{"Resolve", "Execute", "missing"}); got != 2 {
		t.Errorf("countGoalHits = %d, want 2", got)
	}
	if got := countGoalHits(text, nil); got != 0 {
		t.Errorf("no wants → 0, got %d", got)
	}
	// Acceptance is all-present: a case passes only when every Want appears.
	want := []string{"Resolve", "Execute"}
	if countGoalHits(text, want) != len(want) {
		t.Error("both facts present should be a pass")
	}
}

func TestStudyProbePass(t *testing.T) {
	tests := []struct {
		name   string
		probe  StudyProbe
		digest string
		want   bool
	}{
		{
			name:   "all gold present (MinGold 0 → all)",
			probe:  StudyProbe{Gold: []string{"wrapServerError"}},
			digest: "The wrapServerError function prepends the endpoint name.",
			want:   true,
		},
		{
			name:   "needle missing → fail",
			probe:  StudyProbe{Gold: []string{"wrapServerError"}},
			digest: "Something about server errors, but not the function name.",
			want:   false,
		},
		{
			name:   "MinGold met by a subset",
			probe:  StudyProbe{Gold: []string{"maxRepeatedToolCalls", "nudge", "repeat"}, MinGold: 2},
			digest: "It tracks repeats and aborts; a nudge fires first.",
			want:   true,
		},
		{
			name:   "MinGold not met → fail",
			probe:  StudyProbe{Gold: []string{"Safe", "Risky", "Blocked", "classif"}, MinGold: 2},
			digest: "It classifies commands.", // only "classif" present (1 < 2)
			want:   false,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.probe.pass(tc.digest); got != tc.want {
				t.Errorf("pass(%q) = %v, want %v (need %d of %d gold)",
					tc.digest, got, tc.want, tc.probe.need(), len(tc.probe.Gold))
			}
		})
	}
}

// Every agentic request carries a finite output cap — the runaway backstop —
// stamped at the SINGLE cap site (requestFor) plus session init. The old
// second cap helper was deleted in the engine unification; requestFor's stamp
// is covered by TestRequestForSetsMaxTokens.
func TestAgentRequestOutputCapped(t *testing.T) {
	// The base coder request is bounded by default (before any config override).
	if got := (CortexArgs{}).Request().MaxTokens; got != codeMaxOutputTokens {
		t.Errorf("coder request MaxTokens = %d, want %d", got, codeMaxOutputTokens)
	}
	// requestFor never emits an unbounded request: a 0 falls back to the default.
	if got := requestFor(ModelSpec{}, "s", "g", nil, 0, llm.DialectTemplateKwargs).MaxTokens; got != defaultAgentMaxTokens {
		t.Errorf("unset cap → %d, want backstop %d", got, defaultAgentMaxTokens)
	}
	// ModelSpec.maxOut: a config override wins, else the role default.
	if got := (ModelSpec{MaxTokens: 4096}).maxOut(studyMaxOutputTokens); got != 4096 {
		t.Errorf("maxOut override = %d, want 4096", got)
	}
	if got := (ModelSpec{}).maxOut(studyMaxOutputTokens); got != studyMaxOutputTokens {
		t.Errorf("maxOut default = %d, want %d", got, studyMaxOutputTokens)
	}
}

// need() defaults to all of Gold when MinGold is unset, else MinGold.
func TestStudyProbeNeed(t *testing.T) {
	if n := (StudyProbe{Gold: []string{"a", "b", "c"}}).need(); n != 3 {
		t.Errorf("default need = %d, want 3 (all gold)", n)
	}
	if n := (StudyProbe{Gold: []string{"a", "b", "c"}, MinGold: 2}).need(); n != 2 {
		t.Errorf("MinGold need = %d, want 2", n)
	}
}
