//go:build !windows

package eval

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// TestLibraryServiceScore_NearCohesiveCalibration is a calibration probe, not
// a pass/fail gate. It scores a fixture where each non-S1 resource has exactly
// one realistic small-model drift (dropped fmt.Errorf wrap, json.Marshal+Write
// instead of NewEncoder, len()==0 instead of ==\"\", sequential tests instead
// of table-driven) and reports the result.
//
// Use this output to read the scorer's resolution: the gap between perfect
// cohesive (1.0) and near-cohesive should be visible (so a Cortex lift is
// detectable) but not so wide that ordinary variation looks catastrophic.
//
// Soft envelopes are checked, not strict thresholds — adjust them as the
// scorer is tuned.
func TestLibraryServiceScore_NearCohesiveCalibration(t *testing.T) {
	root, err := findRepoRoot()
	if err != nil {
		t.Fatalf("find repo root: %v", err)
	}
	near := filepath.Join(root, "test", "evals", "library-service", "fixtures", "near_cohesive")
	cohesive := filepath.Join(root, "test", "evals", "library-service", "fixtures", "cohesive")
	diverged := filepath.Join(root, "test", "evals", "library-service", "fixtures", "diverged")
	for _, d := range []string{near, cohesive, diverged} {
		if _, err := os.Stat(d); err != nil {
			t.Fatalf("fixture missing: %s", d)
		}
	}

	ev := NewLibraryServiceEvaluator("", "")
	ctx := context.Background()
	cs, err := ev.Score(ctx, cohesive)
	if err != nil {
		t.Fatal(err)
	}
	ns, err := ev.Score(ctx, near)
	if err != nil {
		t.Fatal(err)
	}
	ds, err := ev.Score(ctx, diverged)
	if err != nil {
		t.Fatal(err)
	}

	t.Logf("       │ cohesive │ near    │ diverged")
	t.Logf("shape  │ %.3f    │ %.3f   │ %.3f", cs.ShapeSimilarity, ns.ShapeSimilarity, ds.ShapeSimilarity)
	t.Logf("naming │ %.3f    │ %.3f   │ %.3f", cs.NamingAdherence, ns.NamingAdherence, ds.NamingAdherence)
	t.Logf("smell  │ %.3f    │ %.3f   │ %.3f", cs.SmellDensity, ns.SmellDensity, ds.SmellDensity)
	t.Logf("test   │ %.3f    │ %.3f   │ %.3f", cs.TestParity, ns.TestParity, ds.TestParity)

	// Minimum resolution checks — near must register a drop from cohesive on
	// shape and test parity (the two axes the drift was designed to perturb),
	// and must still sit well above diverged on every axis.
	if ns.ShapeSimilarity >= cs.ShapeSimilarity {
		t.Errorf("shape: near (%.3f) should be < cohesive (%.3f) — metric saturating",
			ns.ShapeSimilarity, cs.ShapeSimilarity)
	}
	if ns.ShapeSimilarity <= ds.ShapeSimilarity {
		t.Errorf("shape: near (%.3f) should be > diverged (%.3f)",
			ns.ShapeSimilarity, ds.ShapeSimilarity)
	}
	if ns.TestParity >= cs.TestParity {
		t.Errorf("test parity: near (%.3f) should be < cohesive (%.3f) — metric saturating",
			ns.TestParity, cs.TestParity)
	}
	if ns.TestParity <= ds.TestParity {
		t.Errorf("test parity: near (%.3f) should be > diverged (%.3f)",
			ns.TestParity, ds.TestParity)
	}
	// Soft envelopes for the headline metric. If shape lands outside this
	// band, the scorer is mis-calibrated for the discrimination Cortex needs:
	//   < 0.55 → too sensitive (one drift looks catastrophic)
	//   > 0.95 → too coarse (drifts vanish into noise)
	if ns.ShapeSimilarity < 0.55 || ns.ShapeSimilarity > 0.95 {
		t.Errorf("shape: near (%.3f) outside calibration envelope [0.55, 0.95]",
			ns.ShapeSimilarity)
	}
}
