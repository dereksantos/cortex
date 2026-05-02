package eval

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// TestLibraryServiceScore_FixtureThresholds runs Score against the hand-crafted
// cohesive and diverged reference repos in test/evals/library-service/fixtures/.
// These thresholds come straight from plans/01-scorer.md "Definition of done"
// — they're the contract the scorer has to hit before the rest of the eval
// can trust its output.
func TestLibraryServiceScore_FixtureThresholds(t *testing.T) {
	root, err := findRepoRoot()
	if err != nil {
		t.Fatalf("find repo root: %v", err)
	}
	cohesiveDir := filepath.Join(root, "test", "evals", "library-service", "fixtures", "cohesive")
	divergedDir := filepath.Join(root, "test", "evals", "library-service", "fixtures", "diverged")
	for _, d := range []string{cohesiveDir, divergedDir} {
		if _, err := os.Stat(d); err != nil {
			t.Fatalf("fixture dir missing: %s", d)
		}
	}

	ev := NewLibraryServiceEvaluator("", "")
	ctx := context.Background()

	cohesive, err := ev.Score(ctx, cohesiveDir)
	if err != nil {
		t.Fatalf("score cohesive: %v", err)
	}
	diverged, err := ev.Score(ctx, divergedDir)
	if err != nil {
		t.Fatalf("score diverged: %v", err)
	}

	t.Logf("cohesive: shape=%.3f naming=%.3f smell=%.3f testParity=%.3f",
		cohesive.ShapeSimilarity, cohesive.NamingAdherence,
		cohesive.SmellDensity, cohesive.TestParity)
	t.Logf("diverged: shape=%.3f naming=%.3f smell=%.3f testParity=%.3f",
		diverged.ShapeSimilarity, diverged.NamingAdherence,
		diverged.SmellDensity, diverged.TestParity)

	if cohesive.ShapeSimilarity < 0.85 {
		t.Errorf("cohesive shape similarity = %.3f, want ≥ 0.85", cohesive.ShapeSimilarity)
	}
	if diverged.ShapeSimilarity > 0.5 {
		t.Errorf("diverged shape similarity = %.3f, want ≤ 0.5", diverged.ShapeSimilarity)
	}
	if cohesive.NamingAdherence < 0.85 {
		t.Errorf("cohesive naming adherence = %.3f, want ≥ 0.85", cohesive.NamingAdherence)
	}
	if diverged.NamingAdherence > 0.6 {
		t.Errorf("diverged naming adherence = %.3f, want ≤ 0.6", diverged.NamingAdherence)
	}
	if diverged.SmellDensity == 0 {
		t.Errorf("diverged smell density = 0, expected > 0")
	}
	if diverged.SmellDensity <= cohesive.SmellDensity {
		t.Errorf("diverged smell density (%.3f) should exceed cohesive (%.3f)",
			diverged.SmellDensity, cohesive.SmellDensity)
	}
	if cohesive.TestParity < 0.95 {
		t.Errorf("cohesive test parity = %.3f, want ≥ 0.95", cohesive.TestParity)
	}
	if diverged.TestParity > 0.5 {
		t.Errorf("diverged test parity = %.3f, want ≤ 0.5", diverged.TestParity)
	}
	if cohesive.RefactorDeltaPct != -1 {
		t.Errorf("RefactorDeltaPct = %.3f, want -1 (deferred)", cohesive.RefactorDeltaPct)
	}
	if cohesive.EndToEndPassRate != 0 {
		t.Errorf("EndToEndPassRate = %.3f, want 0 (deferred to Plan 04)", cohesive.EndToEndPassRate)
	}
}

// findRepoRoot walks up from the current working directory until it finds a
// go.mod, so the test can locate the fixtures regardless of where `go test`
// is invoked from.
func findRepoRoot() (string, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	dir := cwd
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("no go.mod found above %s", cwd)
		}
		dir = parent
	}
}
