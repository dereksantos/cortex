package cognition

import (
	"testing"
	"time"

	"github.com/dereksantos/cortex/pkg/cognition"
)

// TestWeightByProvenance_VerifiedOutranksUnverified checks that a tool-checked
// capture is ranked above an unverified one even when the unverified one has a
// slightly higher raw textual score — so a confabulated assertion can't be
// served as fact over reality.
func TestWeightByProvenance_VerifiedOutranksUnverified(t *testing.T) {
	now := time.Now()
	cands := []cognition.Result{
		{ID: "unverified", Score: 0.80, Timestamp: now, Metadata: map[string]any{"verified": false}},
		{ID: "verified", Score: 0.70, Timestamp: now, Metadata: map[string]any{"verified": true}},
	}
	got := weightByProvenance(cands)
	if got[0].ID != "verified" {
		t.Errorf("ranking = %s first, want verified first (scores: %v)", got[0].ID, got)
	}
}

// TestWeightByProvenance_RecencyBreaksTies checks that among equally-verified
// content, the more recent one ranks higher.
func TestWeightByProvenance_RecencyBreaksTies(t *testing.T) {
	now := time.Now()
	cands := []cognition.Result{
		{ID: "old", Score: 0.75, Timestamp: now.Add(-60 * 24 * time.Hour), Metadata: map[string]any{"verified": true}},
		{ID: "new", Score: 0.75, Timestamp: now, Metadata: map[string]any{"verified": true}},
	}
	got := weightByProvenance(cands)
	if got[0].ID != "new" {
		t.Errorf("ranking = %s first, want new first", got[0].ID)
	}
}

func TestRecencyFactor(t *testing.T) {
	now := time.Now()
	if f := recencyFactor(time.Time{}, now); f != 1.0 {
		t.Errorf("zero timestamp factor = %v, want 1.0 (neutral)", f)
	}
	if f := recencyFactor(now, now); f != 1.0 {
		t.Errorf("now factor = %v, want 1.0", f)
	}
	if f := recencyFactor(now.Add(-2*recencyHorizon), now); f != recencyFloor {
		t.Errorf("ancient factor = %v, want floor %v", f, recencyFloor)
	}
}
