//go:build !windows

package eval

import (
	"os"
	"path/filepath"
	"testing"
)

// codingScenariosDir is relative to the test binary's working
// directory, which `go test` sets to the package dir
// (internal/eval/v2/). Walk up three levels to the repo root.
const codingScenariosDir = "../../../test/evals/coding"

// TestCodingScenariosLoadable validates that every YAML under
// test/evals/coding/ parses cleanly and has the grid-runner extension
// fields populated (seed_dir, verify, cortex_context). This is a
// trip-wire for YAML drift — broken scenarios fail at load time, not
// mid-grid-run.
func TestCodingScenariosLoadable(t *testing.T) {
	scenarios, err := LoadAll(codingScenariosDir)
	if err != nil {
		t.Fatalf("LoadAll(%q): %v", codingScenariosDir, err)
	}
	if len(scenarios) < 5 {
		t.Fatalf("expected ≥ 5 coding scenarios, got %d", len(scenarios))
	}

	expected := map[string]bool{
		"fizzbuzz":        false,
		"rename-json-tag": false,
		"fix-off-by-one":  false,
		"add-table-test":  false,
		"error-wrap":      false,
	}
	for _, s := range scenarios {
		if _, ok := expected[s.ID]; !ok {
			continue
		}
		expected[s.ID] = true

		if s.SeedDir == "" {
			t.Errorf("scenario %q: SeedDir empty", s.ID)
		} else {
			abs := filepath.Join("../../..", s.SeedDir)
			info, statErr := os.Stat(abs)
			if statErr != nil || !info.IsDir() {
				t.Errorf("scenario %q: SeedDir %q does not resolve to a dir (abs=%q, err=%v)",
					s.ID, s.SeedDir, abs, statErr)
			}
		}
		if s.Verify == "" {
			t.Errorf("scenario %q: Verify empty (need a verifier for real signal)", s.ID)
		}
		if len(s.CortexContext) == 0 {
			t.Errorf("scenario %q: CortexContext empty (cortex strategy would no-op)", s.ID)
		}
		if len(s.Tests) == 0 || s.Tests[0].Query == "" {
			t.Errorf("scenario %q: Tests[0].Query empty (no agent prompt)", s.ID)
		}
	}
	for id, found := range expected {
		if !found {
			t.Errorf("expected scenario %q not found in coding dir", id)
		}
	}
}
