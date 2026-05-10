//go:build !windows

package eval

import (
	"context"
	"testing"
)

// Compile-time guard: existing harnesses still satisfy the bare Harness
// interface. Step 3 must not break ClaudeCLIHarness or AiderHarness.
var (
	_ Harness = (*ClaudeCLIHarness)(nil)
	_ Harness = (*AiderHarness)(nil)
)

// bareHarness implements only Harness.
type bareHarness struct{}

func (bareHarness) RunSession(_ context.Context, _ string, _ string) error {
	return nil
}

// upgradedHarness implements both Harness and ResultfulHarness.
type upgradedHarness struct{}

func (upgradedHarness) RunSession(_ context.Context, _ string, _ string) error {
	return nil
}

func (upgradedHarness) RunSessionWithResult(_ context.Context, _ string, _ string) (HarnessResult, error) {
	return HarnessResult{
		TokensIn:        100,
		TokensOut:       50,
		CostUSD:         0.0042,
		AgentTurnsTotal: 3,
		FilesChanged:    []string{"a.go", "b.go"},
		LatencyMs:       1234,
		ProviderEcho:    "openrouter",
		ModelEcho:       "openai/gpt-oss-20b:free",
	}, nil
}

// TestResultfulHarnessAssertion locks the type-assertion pattern the
// grid runner relies on (see ResultfulHarness doc comment).
func TestResultfulHarnessAssertion(t *testing.T) {
	t.Run("bare harness fails the assertion", func(t *testing.T) {
		var h Harness = bareHarness{}
		if _, ok := h.(ResultfulHarness); ok {
			t.Fatal("bareHarness should NOT satisfy ResultfulHarness")
		}
	})

	t.Run("upgraded harness satisfies both", func(t *testing.T) {
		var h Harness = upgradedHarness{}
		rh, ok := h.(ResultfulHarness)
		if !ok {
			t.Fatal("upgradedHarness should satisfy ResultfulHarness")
		}
		res, err := rh.RunSessionWithResult(context.Background(), "x", t.TempDir())
		if err != nil {
			t.Fatalf("RunSessionWithResult: %v", err)
		}
		if res.TokensIn != 100 || res.TokensOut != 50 {
			t.Errorf("tokens=%+v want 100/50", res)
		}
		if res.CostUSD != 0.0042 {
			t.Errorf("cost=%v want 0.0042", res.CostUSD)
		}
		if len(res.FilesChanged) != 2 {
			t.Errorf("files=%+v want 2", res.FilesChanged)
		}
		if res.ProviderEcho != "openrouter" || res.ModelEcho != "openai/gpt-oss-20b:free" {
			t.Errorf("echo mismatch: provider=%q model=%q", res.ProviderEcho, res.ModelEcho)
		}
	})
}

// TestHarnessResultZeroValueIsValid documents that an unfilled
// HarnessResult is the contract for "harness ran but observed no
// telemetry" — the runner must treat zero fields as unknown, not as
// "the agent definitely used 0 tokens".
func TestHarnessResultZeroValueIsValid(t *testing.T) {
	var r HarnessResult
	if r.TokensIn != 0 || r.CostUSD != 0 || r.LatencyMs != 0 {
		t.Errorf("HarnessResult zero value not zero: %+v", r)
	}
	if len(r.FilesChanged) != 0 {
		t.Errorf("FilesChanged nil-or-empty expected, got %v", r.FilesChanged)
	}
}
