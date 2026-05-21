//go:build !windows

package paired

import (
	"context"
	"fmt"

	eval "github.com/dereksantos/cortex/internal/eval/v2"
)

// DefaultHarness wraps eval.CortexHarness so callers can run the
// paired comparison against real LLM endpoints. Tests use a fake
// implementation of Harness instead — this type only exists to spare
// CLI callers from re-wiring the same plumbing.
//
// Construct via NewDefaultHarness; the zero value is not usable
// because the model is per-condition and the harness is rebuilt every
// call to keep cross-cell state from leaking.
type DefaultHarness struct {
	// Verbose forwards to the per-condition harness so frame-diff
	// errors and tool-call traces surface to stdout. Off by default.
	Verbose bool
}

// NewDefaultHarness returns a runner that builds a fresh
// eval.CortexHarness per condition and scores with ScoreGoLFrames.
func NewDefaultHarness() *DefaultHarness { return &DefaultHarness{} }

// Run implements Harness. The harness is rebuilt per call so model
// swaps and endpoint swaps don't carry over from a prior condition's
// LastCostUSD / LastProvider / shared cortex_search store.
func (d *DefaultHarness) Run(ctx context.Context, c Condition, scenario *eval.CodingScenario, workdir string) (Outcome, error) {
	h, err := eval.NewCortexHarness(c.Model)
	if err != nil {
		return Outcome{}, fmt.Errorf("new cortex harness: %w", err)
	}
	if c.Endpoint != nil {
		h.SetEndpoint(c.Endpoint)
	}
	h.SetCortexSearchEnabled(c.UseCortex)

	hr, runErr := h.RunSessionWithResult(ctx, scenario.Prompt, workdir)
	// Score regardless of runErr: a session that errored after writing
	// partial code can still produce a non-zero frame-diff score, and
	// the JSONL row should reflect that. We propagate runErr verbatim
	// so the caller's Result.Err carries the failure.

	frames, frameErr := eval.ScoreGoLFrames(ctx, workdir, scenario.FixturesDir, scenario.Generations)
	if frameErr != nil && d.Verbose {
		fmt.Printf("[paired] %s: frame scorer error: %v\n", c.Name, frameErr)
	}

	out := Outcome{
		TokensIn:   hr.TokensIn,
		TokensOut:  hr.TokensOut,
		CostUSD:    hr.CostUSD,
		LatencyMs:  hr.LatencyMs,
		AgentTurns: hr.AgentTurnsTotal,
		Frames:     frames,
		// No judge here: Tier 2c paired comparisons run faster and
		// cheaper without an LLM judge call per row. Quality is read
		// off the frame diff; callers who need a judge layer should
		// add it as a wrapping Harness implementation.
		JudgePass: frames.BuildOK && frames.AllPassed,
	}
	return out, runErr
}
