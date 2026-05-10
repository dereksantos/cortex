//go:build !windows

// HarnessResult and ResultfulHarness — additive, non-breaking telemetry
// seam over the existing Harness interface (library_service.go:76).
//
// The existing Harness.RunSession returns only error. That's enough for
// the library-service eval, which derives quality metrics from on-disk
// artifacts (handler files, tests). For the cross-harness × OpenRouter
// grid runner (TODO 8), we need per-cell tokens/cost/turns directly from
// the harness — they aren't recoverable from the workdir afterwards.
//
// Rather than mutate Harness, ResultfulHarness extends it. The grid
// runner type-asserts; harnesses that haven't been upgraded fall back to
// bare RunSession with a synthesized HarnessResult containing only the
// observable fields (LatencyMs, FilesChanged from a workdir diff). This
// keeps ClaudeCLIHarness and the existing Aider behavior compatible with
// no edits.
package eval

import "context"

// HarnessResult carries per-session telemetry that downstream code needs
// to fill in a CellResult. It is a *subset* of CellResult — the grid
// runner adds the grid-dimension fields (scenario_id, harness, provider,
// model, context_strategy, etc.) and computes derived rollups.
//
// Fields are deliberately permissive: most are zero-valued when a
// harness can't observe them. The grid runner treats zero as "unknown"
// rather than "definitely zero".
type HarnessResult struct {
	// Token counts as reported by the underlying CLI / event stream.
	// 0 if the harness can't observe them.
	TokensIn  int
	TokensOut int

	// CostUSD is the per-session cost reported by the harness or the
	// upstream provider. Zero on free models or when the harness
	// can't extract it. Do not confuse with cell-level totals — this
	// is one harness call.
	CostUSD float64

	// AgentTurnsTotal counts every assistant turn the agent took in
	// this session (including tool-call rounds). Useful as a denominator
	// for CorrectionTurns at the CellResult level.
	AgentTurnsTotal int

	// FilesChanged is the set of paths under workdir the agent
	// modified during the session. Best-effort; harnesses that can't
	// distinguish their writes from pre-existing tree state may leave
	// it empty and let the runner derive it from a git diff.
	FilesChanged []string

	// LatencyMs is wall-clock duration of the session, end-to-end,
	// including any retry/backoff. The harness owns this measurement —
	// the runner's outer timing covers a wider scope.
	LatencyMs int64

	// ProviderEcho / ModelEcho are what the harness *actually* called.
	// Useful when a harness translates model names (e.g. Aider's
	// "openrouter/x" → litellm's underlying call) and we want to
	// confirm the trip ID matches the requested grid cell.
	ProviderEcho string
	ModelEcho    string
}

// ResultfulHarness is the optional extension over Harness. The grid
// runner does:
//
//	if rh, ok := h.(ResultfulHarness); ok {
//	    res, err := rh.RunSessionWithResult(ctx, prompt, workdir)
//	    ...
//	} else {
//	    err := h.RunSession(ctx, prompt, workdir)
//	    res := HarnessResult{LatencyMs: time.Since(start).Milliseconds()}
//	    ...
//	}
//
// Implementations should return a HarnessResult on *success*. On error,
// callers should not rely on the result's contents.
type ResultfulHarness interface {
	Harness
	RunSessionWithResult(ctx context.Context, prompt string, workdir string) (HarnessResult, error)
}
