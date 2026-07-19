package main

import "testing"

// TestCuratedFreeModelsSanity pins the shape docs/completion-roadmap.md
// Track E2 asks for: 3-5 entries, every entry fully filled in, no duplicate
// ids, and a stable, non-empty top pick used for both live roles.
func TestCuratedFreeModelsSanity(t *testing.T) {
	n := len(curatedFreeModels)
	if n < 3 || n > 5 {
		t.Fatalf("len(curatedFreeModels) = %d, want 3-5", n)
	}

	seen := make(map[string]bool, n)
	for _, m := range curatedFreeModels {
		if m.ID == "" {
			t.Errorf("curated model has empty ID: %+v", m)
		}
		if m.Window <= 0 {
			t.Errorf("curated model %q has non-positive Window %d", m.ID, m.Window)
		}
		if m.Why == "" {
			t.Errorf("curated model %q has empty Why", m.ID)
		}
		if seen[m.ID] {
			t.Errorf("duplicate curated model id %q", m.ID)
		}
		seen[m.ID] = true
	}
}

// TestCuratedTopPick pins that curatedTopPick returns the first table entry
// (bootstrap's default for both code and study — E1's "same model by
// default") and that it's recognized by isCuratedModel.
func TestCuratedTopPick(t *testing.T) {
	top := curatedTopPick()
	if top.ID != curatedFreeModels[0].ID {
		t.Errorf("curatedTopPick().ID = %q, want %q (curatedFreeModels[0])", top.ID, curatedFreeModels[0].ID)
	}
	if !isCuratedModel(top.ID) {
		t.Errorf("isCuratedModel(%q) = false, want true", top.ID)
	}
}

// TestIsCuratedModel checks both membership directions.
func TestIsCuratedModel(t *testing.T) {
	for _, m := range curatedFreeModels {
		if !isCuratedModel(m.ID) {
			t.Errorf("isCuratedModel(%q) = false, want true", m.ID)
		}
	}
	for _, id := range []string{"", "not/a-curated-model:free", "anthropic/claude-haiku-4.5"} {
		if isCuratedModel(id) {
			t.Errorf("isCuratedModel(%q) = true, want false", id)
		}
	}
}
