package main

import "testing"

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
