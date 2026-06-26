package cognition

import (
	"testing"
	"time"

	"github.com/dereksantos/cortex/pkg/cognition"
)

type fakeRetractor struct{ retracted []string }

func (f *fakeRetractor) Retract(id, _ string) error {
	f.retracted = append(f.retracted, id)
	return nil
}

// TestApplyRetractions_RetractsOlder verifies a detected contradiction retracts
// the older candidate (recency wins) and keeps the newer one.
func TestApplyRetractions_RetractsOlder(t *testing.T) {
	now := time.Now()
	fr := &fakeRetractor{}
	r := &Reflect{}
	r.SetRetractor(fr)

	candidates := []cognition.Result{
		{ID: "event-old", Timestamp: now.Add(-2 * time.Hour)},
		{ID: "event-new", Timestamp: now},
	}
	r.applyRetractions(candidates, []contradiction{{IDs: []string{"event-old", "event-new"}, Reason: "936 vs 64 docs"}})

	if len(fr.retracted) != 1 || fr.retracted[0] != "event-old" {
		t.Fatalf("retracted = %v, want [event-old]", fr.retracted)
	}
}

// TestApplyRetractions_SkipsTiesAndUnknownAge ensures we never retract when
// recency is ambiguous (equal or zero timestamps), to avoid wrong prunes.
func TestApplyRetractions_SkipsTiesAndUnknownAge(t *testing.T) {
	fr := &fakeRetractor{}
	r := &Reflect{}
	r.SetRetractor(fr)

	// Equal timestamps → tie → no retraction.
	now := time.Now()
	r.applyRetractions(
		[]cognition.Result{{ID: "a", Timestamp: now}, {ID: "b", Timestamp: now}},
		[]contradiction{{IDs: []string{"a", "b"}}},
	)
	// Zero timestamps → unknown age → no retraction.
	r.applyRetractions(
		[]cognition.Result{{ID: "c"}, {ID: "d"}},
		[]contradiction{{IDs: []string{"c", "d"}}},
	)
	if len(fr.retracted) != 0 {
		t.Errorf("retracted %v on ambiguous recency, want none", fr.retracted)
	}
}

// TestApplyRetractions_NilRetractorNoPanic confirms detect-only mode is safe.
func TestApplyRetractions_NilRetractorNoPanic(t *testing.T) {
	r := &Reflect{} // no retractor
	r.applyRetractions(
		[]cognition.Result{{ID: "a", Timestamp: time.Now()}},
		[]contradiction{{IDs: []string{"a"}}},
	)
}
