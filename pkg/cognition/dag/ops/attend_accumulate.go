// Package ops — attend.accumulate.
//
// LLM micro-node that maintains a bounded WORKING MEMORY across a
// session's other nodes. Each call folds one new observation into
// the previous snapshot, emits a new snapshot that fits the budget,
// and (when wired) writes a journal entry capturing the step.
//
// This is the foundation for the bounded-emergence approach: instead
// of every coding turn rebuilding context from the raw transcript,
// downstream nodes (decide.next, decide.coding_turn, attend.compress
// callers) read the current accumulator snapshot. The snapshot stays
// bounded by max_tokens regardless of how many observations have been
// folded in — that's what keeps individual node prompts from blowing
// past the model's n_ctx.
//
// Fails safe: when the provider is unavailable or the budget can't
// afford the LLM call, the handler falls back to a deterministic
// truncate-and-merge that preserves the most recent observation +
// as much of the prior snapshot as fits. The contract holds even
// when the small model is unreachable.
package ops

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/dereksantos/cortex/pkg/cognition/dag"
	"github.com/dereksantos/cortex/pkg/llm"
)

// AccumulateConfig wires the op to its provider. JournalWrite, when
// non-nil, is invoked once per successful call with the post-update
// snapshot so the caller can persist a think.accumulator_update
// entry. The handler stays journal-agnostic (the journal lives in
// internal/, this package lives in pkg/) — callers wire the bridge.
type AccumulateConfig struct {
	Provider     llm.Provider
	JournalWrite func(snapshot string, snapshotTokens int, sourceObservation string, fallback bool)
}

// accumulateCostHint — one small-LLM call that reads (prev_snapshot,
// observation), emits a compressed snapshot. Sized like attend.compress
// (4s / 300tok); the input is two text blocks and the output fits
// max_tokens. Calibrates once we have a few sessions of data.
var accumulateCostHint = dag.Cost{LatencyMS: 4000, Tokens: 400}

// AccumulateSpec returns the NodeSpec for attend.accumulate.
//
// Inputs:
//   - prev_snapshot (string) — working memory so far; "" on step 0.
//   - observation (string)   — new content to fold in.
//   - max_tokens (int)       — hard cap on the output snapshot.
//   - intent (string)        — what the session is ultimately trying
//     to do; guides the compressor's "what to keep".
//
// Outputs:
//   - snapshot (string)        — post-update working memory.
//   - snapshot_tokens (int)    — 4-char-heuristic estimate.
//   - prev_snapshot_tokens (int)
//   - observation_tokens (int)
//   - fallback (bool)          — true when the deterministic path ran.
func AccumulateSpec(opts ...AccumulateConfig) dag.NodeSpec {
	var cfg AccumulateConfig
	if len(opts) > 0 {
		cfg = opts[0]
	}
	return dag.NodeSpec{
		Function:    dag.FuncAttend,
		Op:          "accumulate",
		Description: "fold a new observation into a bounded working-memory snapshot",
		Inputs: []dag.ParamSpec{
			{Name: "prev_snapshot", Type: "string"},
			{Name: "observation", Type: "string", Required: true},
			{Name: "max_tokens", Type: "int", Required: true},
			{Name: "intent", Type: "string"},
		},
		Outputs: []dag.ParamSpec{
			{Name: "snapshot", Type: "string"},
			{Name: "snapshot_tokens", Type: "int"},
			{Name: "prev_snapshot_tokens", Type: "int"},
			{Name: "observation_tokens", Type: "int"},
			{Name: "fallback", Type: "bool"},
		},
		Cost:      accumulateCostHint,
		Exposable: false,
		Handler:   newAccumulateHandler(cfg),
	}
}

// newAccumulateHandler builds the attend.accumulate handler.
//
// Path selection per call:
//   - max_tokens <= 0           → error (the budget is what makes this useful).
//   - prev_snapshot + observation already fits → passthrough merge
//     (concatenate prior + observation under budget); no LLM call.
//   - provider available + budget OK → LLM compression path.
//   - otherwise → deterministic fallback (truncate prior + keep new).
func newAccumulateHandler(cfg AccumulateConfig) dag.Handler {
	return func(ctx context.Context, in map[string]any, budget dag.Budget) (dag.NodeResult, error) {
		started := time.Now()
		prev := readString(in, "prev_snapshot")
		obs := readString(in, "observation")
		maxTokens := readInt(in, "max_tokens", 0)
		intent := readString(in, "intent")

		if obs == "" {
			return dag.NodeResult{
				CostConsumed: dag.Cost{LatencyMS: int(time.Since(started).Milliseconds())},
			}, fmt.Errorf("attend.accumulate: 'observation' is required")
		}
		if maxTokens <= 0 {
			return dag.NodeResult{
				CostConsumed: dag.Cost{LatencyMS: int(time.Since(started).Milliseconds())},
			}, fmt.Errorf("attend.accumulate: 'max_tokens' must be >= 1")
		}

		prevTok := approxTokens(prev)
		obsTok := approxTokens(obs)

		// Passthrough merge — both fit; no LLM needed. Keep prior
		// then append a delimited observation so callers reading the
		// snapshot can still see the trajectory.
		merged := joinSnapshot(prev, obs)
		mergedTok := approxTokens(merged)
		if mergedTok <= maxTokens {
			emitJournal(cfg, merged, mergedTok, obs, false)
			return dag.NodeResult{
				Out: map[string]any{
					"snapshot":             merged,
					"snapshot_tokens":      mergedTok,
					"prev_snapshot_tokens": prevTok,
					"observation_tokens":   obsTok,
					"fallback":             false,
				},
				CostConsumed: dag.Cost{LatencyMS: int(time.Since(started).Milliseconds()), OutputTokens: mergedTok},
			}, nil
		}

		// LLM path — provider available + budget headroom.
		if cfg.Provider != nil && cfg.Provider.IsAvailable() && budget.CanAfford(accumulateCostHint) {
			if out, ok := llmAccumulate(ctx, cfg.Provider, prev, obs, intent, maxTokens, started); ok {
				snap, _ := out.Out["snapshot"].(string)
				snapTok, _ := out.Out["snapshot_tokens"].(int)
				emitJournal(cfg, snap, snapTok, obs, false)
				// Patch the prev/obs token counts in — the LLM path
				// doesn't compute them.
				out.Out["prev_snapshot_tokens"] = prevTok
				out.Out["observation_tokens"] = obsTok
				return out, nil
			}
			// LLM path errored — fall through to deterministic merge.
		}

		// Deterministic fallback — truncate prior to fit budget after
		// reserving room for the new observation. Worst case (new
		// observation alone exceeds budget): truncate the observation.
		obsRoom := maxTokens
		if obsRoom > obsTok {
			obsRoom = obsTok
		}
		keptObs := truncateToTokens(obs, obsRoom)
		priorRoom := maxTokens - approxTokens(keptObs)
		if priorRoom < 0 {
			priorRoom = 0
		}
		keptPrev := truncateToTokens(prev, priorRoom)
		snap := joinSnapshot(keptPrev, keptObs)
		if approxTokens(snap) > maxTokens {
			snap = truncateToTokens(snap, maxTokens)
		}
		snapTok := approxTokens(snap)
		emitJournal(cfg, snap, snapTok, obs, true)
		return dag.NodeResult{
			Out: map[string]any{
				"snapshot":             snap,
				"snapshot_tokens":      snapTok,
				"prev_snapshot_tokens": prevTok,
				"observation_tokens":   obsTok,
				"fallback":             true,
			},
			CostConsumed: dag.Cost{LatencyMS: int(time.Since(started).Milliseconds()), OutputTokens: snapTok},
		}, nil
	}
}

// llmAccumulate runs the small-LLM compression via the
// attend_accumulate template. Returns (result, true) on success;
// (zero, false) on any error so the caller can fall through to the
// deterministic merge.
func llmAccumulate(ctx context.Context, p llm.Provider, prev, obs, intent string, maxTokens int, started time.Time) (dag.NodeResult, bool) {
	pt, terr := LoadTemplate("attend_accumulate")
	if terr != nil {
		return dag.NodeResult{}, false
	}
	prompt, rerr := pt.Render(map[string]any{
		"prev_snapshot": prev,
		"observation":   obs,
		"intent":        intent,
		"max_tokens":    maxTokens,
	})
	if rerr != nil {
		return dag.NodeResult{}, false
	}
	resp, stats, gerr := p.GenerateWithStats(ctx, prompt)
	if gerr != nil {
		return dag.NodeResult{}, false
	}
	snap := strings.TrimSpace(resp)
	if snap == "" {
		return dag.NodeResult{}, false
	}
	// Defensive clamp: a model that ignored the budget gets truncated
	// here.
	if approxTokens(snap) > maxTokens {
		snap = truncateToTokens(snap, maxTokens)
	}
	snapTok := approxTokens(snap)
	latency := int(time.Since(started).Milliseconds())
	return dag.NodeResult{
		Out: map[string]any{
			"snapshot":        snap,
			"snapshot_tokens": snapTok,
			"fallback":        false,
		},
		CostConsumed: dag.Cost{LatencyMS: latency, Tokens: stats.TotalTokens(), OutputTokens: snapTok},
	}, true
}

// joinSnapshot concatenates the prior snapshot and a new observation
// with a delimiter that distinguishes the two without adding heavy
// markup. Returns just the observation when prev is empty (step 0).
func joinSnapshot(prev, obs string) string {
	prev = strings.TrimSpace(prev)
	obs = strings.TrimSpace(obs)
	if prev == "" {
		return obs
	}
	if obs == "" {
		return prev
	}
	return prev + "\n— next —\n" + obs
}

// emitJournal calls the configured journal-write hook (if any). The
// op stays free of internal/journal imports; the bridge is wired by
// the caller (the chain that builds the DAG).
func emitJournal(cfg AccumulateConfig, snapshot string, snapshotTokens int, observation string, fallback bool) {
	if cfg.JournalWrite == nil {
		return
	}
	cfg.JournalWrite(snapshot, snapshotTokens, observation, fallback)
}
