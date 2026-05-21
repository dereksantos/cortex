package llm

import "testing"

// TestSalienceCapForClass_OverrideWins exercises the Phase 3 Slice 5
// override path: SetSalienceCapOverride pins per-class values, and
// SalienceCapForClass returns them instead of the static defaults.
// Falls back to the static 200/500/1500 when the override is cleared.
func TestSalienceCapForClass_OverrideWins(t *testing.T) {
	// Save and restore so test order doesn't leak state.
	defer SetSalienceCapOverride(nil)

	SetSalienceCapOverride(map[ContextClass]int{
		ContextSmall:  333,
		ContextMedium: 777,
		ContextLarge:  1111,
	})
	if got := SalienceCapForClass(ContextSmall); got != 333 {
		t.Errorf("ContextSmall override: got %d, want 333", got)
	}
	if got := SalienceCapForClass(ContextMedium); got != 777 {
		t.Errorf("ContextMedium override: got %d, want 777", got)
	}
	if got := SalienceCapForClass(ContextLarge); got != 1111 {
		t.Errorf("ContextLarge override: got %d, want 1111", got)
	}

	// Clearing reverts to the static defaults.
	SetSalienceCapOverride(nil)
	if got := SalienceCapForClass(ContextSmall); got != 200 {
		t.Errorf("after clear, ContextSmall: got %d, want 200 (static default)", got)
	}
	if got := SalienceCapForClass(ContextMedium); got != 500 {
		t.Errorf("after clear, ContextMedium: got %d, want 500", got)
	}
	if got := SalienceCapForClass(ContextLarge); got != 1500 {
		t.Errorf("after clear, ContextLarge: got %d, want 1500", got)
	}
}

func TestSalienceCapForClass_PartialOverride_StaticFallback(t *testing.T) {
	defer SetSalienceCapOverride(nil)

	SetSalienceCapOverride(map[ContextClass]int{
		ContextSmall: 250,
	})
	if got := SalienceCapForClass(ContextSmall); got != 250 {
		t.Errorf("ContextSmall override: got %d, want 250", got)
	}
	// Unset classes fall through to the static default — important
	// when calibration data only covers part of the matrix.
	if got := SalienceCapForClass(ContextMedium); got != 500 {
		t.Errorf("ContextMedium (unset): got %d, want 500 (fallback)", got)
	}
	if got := SalienceCapForClass(ContextLarge); got != 1500 {
		t.Errorf("ContextLarge (unset): got %d, want 1500 (fallback)", got)
	}
}

func TestSalienceCapForClass_NonPositiveOverridesIgnored(t *testing.T) {
	defer SetSalienceCapOverride(nil)

	SetSalienceCapOverride(map[ContextClass]int{
		ContextSmall:  0,
		ContextMedium: -5,
		ContextLarge:  1200,
	})
	if got := SalienceCapForClass(ContextSmall); got != 200 {
		t.Errorf("zero override should be ignored: got %d, want 200", got)
	}
	if got := SalienceCapForClass(ContextMedium); got != 500 {
		t.Errorf("negative override should be ignored: got %d, want 500", got)
	}
	if got := SalienceCapForClass(ContextLarge); got != 1200 {
		t.Errorf("ContextLarge: got %d, want 1200", got)
	}
}
