// Package ops — attend.compact.
//
// Rolling re-summarization for the accumulator. While attend.accumulate
// folds ONE new observation into a snapshot per call, attend.compact
// re-summarizes K previous snapshots into one tighter snapshot under
// a budget. Triggered when the accumulator approaches its size cap
// (e.g., after enough observations have been folded that newer
// updates can't fit).
//
// The triggering decision lives with callers: the chain builder may
// invoke compact after every N accumulate steps, or the decide.next
// LLM may emit it directly in a plan when it sees the working
// memory growing unwieldy. This op is the worker, not the policy.
//
// Fails safe: when the provider is unavailable OR the budget can't
// afford the LLM call, the handler falls back to a deterministic
// concatenate-and-truncate that preserves the NEWEST snapshot (the
// most relevant) and drops oldest first.
package ops

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/dereksantos/cortex/pkg/cognition/dag"
	"github.com/dereksantos/cortex/pkg/llm"
)

// CompactConfig wires the op to its provider. JournalWrite, when
// non-nil, is invoked once per successful call so the caller can
// persist a think.accumulator_compact entry.
type CompactConfig struct {
	Provider     llm.Provider
	JournalWrite func(snapshot string, snapshotTokens, compactedCount int, fallback bool)
}

// compactCostHint — one small-LLM merge call. Sized like
// attend.accumulate plus a bit (the prompt carries K snapshots,
// which is larger than a single observation). Calibrates with use.
var compactCostHint = dag.Cost{LatencyMS: 6000, Tokens: 600}

// CompactSpec returns the NodeSpec for attend.compact.
//
// Inputs:
//   - snapshots ([]string) — chronological list of recent snapshots
//     to merge; oldest first.
//   - max_tokens (int)     — hard cap on the merged output.
//   - intent (string)      — what the session is trying to do.
//
// Outputs:
//   - snapshot (string)           — the merged tighter snapshot.
//   - snapshot_tokens (int)
//   - compacted_snapshots (int)   — len(input snapshots).
//   - fallback (bool)
func CompactSpec(opts ...CompactConfig) dag.NodeSpec {
	var cfg CompactConfig
	if len(opts) > 0 {
		cfg = opts[0]
	}
	return dag.NodeSpec{
		Function:    dag.FuncAttend,
		Op:          "compact",
		Description: "re-summarize K recent accumulator snapshots into a single tighter snapshot under a budget",
		Inputs: []dag.ParamSpec{
			{Name: "snapshots", Type: "[]string", Required: true},
			{Name: "max_tokens", Type: "int", Required: true},
			{Name: "intent", Type: "string"},
		},
		Outputs: []dag.ParamSpec{
			{Name: "snapshot", Type: "string"},
			{Name: "snapshot_tokens", Type: "int"},
			{Name: "compacted_snapshots", Type: "int"},
			{Name: "fallback", Type: "bool"},
		},
		Cost: compactCostHint,
		// Exposable: decide.next can emit attend.compact when it sees
		// the working memory growing. The trigger is emergent.
		Exposable: true,
		Handler:   newCompactHandler(cfg),
	}
}

// newCompactHandler builds the attend.compact handler.
//
// Path selection per call:
//   - snapshots empty → error (nothing to compact).
//   - single snapshot already under budget → passthrough.
//   - provider available + budget OK → LLM merge.
//   - otherwise → deterministic concatenate + drop-oldest fallback.
func newCompactHandler(cfg CompactConfig) dag.Handler {
	return func(ctx context.Context, in map[string]any, budget dag.Budget) (dag.NodeResult, error) {
		started := time.Now()
		snapshots, _ := readStringSlice(in, "snapshots")
		maxTokens := readInt(in, "max_tokens", 0)
		intent := readString(in, "intent")

		if len(snapshots) == 0 {
			return dag.NodeResult{
				CostConsumed: dag.Cost{LatencyMS: int(time.Since(started).Milliseconds())},
			}, fmt.Errorf("attend.compact: 'snapshots' (non-empty []string) is required")
		}
		if maxTokens <= 0 {
			return dag.NodeResult{
				CostConsumed: dag.Cost{LatencyMS: int(time.Since(started).Milliseconds())},
			}, fmt.Errorf("attend.compact: 'max_tokens' must be >= 1")
		}

		// Passthrough — single input that already fits. No LLM.
		if len(snapshots) == 1 && approxTokens(snapshots[0]) <= maxTokens {
			snap := snapshots[0]
			snapTok := approxTokens(snap)
			emitCompactJournal(cfg, snap, snapTok, 1, false)
			return dag.NodeResult{
				Out: map[string]any{
					"snapshot":            snap,
					"snapshot_tokens":     snapTok,
					"compacted_snapshots": 1,
					"fallback":            false,
				},
				CostConsumed: dag.Cost{LatencyMS: int(time.Since(started).Milliseconds()), OutputTokens: snapTok},
			}, nil
		}

		// LLM path.
		if cfg.Provider != nil && cfg.Provider.IsAvailable() && budget.CanAfford(compactCostHint) {
			if out, ok := llmCompact(ctx, cfg.Provider, snapshots, intent, maxTokens, started); ok {
				snap, _ := out.Out["snapshot"].(string)
				snapTok, _ := out.Out["snapshot_tokens"].(int)
				emitCompactJournal(cfg, snap, snapTok, len(snapshots), false)
				out.Out["compacted_snapshots"] = len(snapshots)
				return out, nil
			}
		}

		// Deterministic fallback: keep newest snapshot in full,
		// truncate to budget; drop older snapshots. Worst case
		// (newest alone exceeds budget): truncate newest.
		newest := snapshots[len(snapshots)-1]
		snap := truncateToTokens(newest, maxTokens)
		// If room remains, prepend older snapshots in reverse-chrono
		// order until full. This keeps the most recent context dense
		// and lets older context survive in summary form when budget
		// allows. Simpler than an LLM merge but better than dropping
		// everything but the newest.
		used := approxTokens(snap)
		for i := len(snapshots) - 2; i >= 0 && used < maxTokens; i-- {
			room := maxTokens - used
			if room <= 8 {
				break
			}
			kept := truncateToTokens(snapshots[i], room-4) // room - join overhead
			if kept == "" {
				break
			}
			snap = kept + "\n— next —\n" + snap
			used = approxTokens(snap)
		}
		if approxTokens(snap) > maxTokens {
			snap = truncateToTokens(snap, maxTokens)
		}
		snapTok := approxTokens(snap)
		emitCompactJournal(cfg, snap, snapTok, len(snapshots), true)
		return dag.NodeResult{
			Out: map[string]any{
				"snapshot":            snap,
				"snapshot_tokens":     snapTok,
				"compacted_snapshots": len(snapshots),
				"fallback":            true,
			},
			CostConsumed: dag.Cost{LatencyMS: int(time.Since(started).Milliseconds()), OutputTokens: snapTok},
		}, nil
	}
}

// llmCompact runs the small-LLM merge via the attend_compact
// template. Returns (result, true) on success; (zero, false) on any
// error so the caller can fall through to the fallback.
func llmCompact(ctx context.Context, p llm.Provider, snapshots []string, intent string, maxTokens int, started time.Time) (dag.NodeResult, bool) {
	pt, terr := LoadTemplate("attend_compact")
	if terr != nil {
		return dag.NodeResult{}, false
	}
	var b strings.Builder
	for i, s := range snapshots {
		fmt.Fprintf(&b, "[snapshot %d/%d]\n%s\n\n", i+1, len(snapshots), strings.TrimSpace(s))
	}
	prompt, rerr := pt.Render(map[string]any{
		"snapshots":  b.String(),
		"intent":     intent,
		"max_tokens": maxTokens,
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

func emitCompactJournal(cfg CompactConfig, snapshot string, snapshotTokens, compactedCount int, fallback bool) {
	if cfg.JournalWrite == nil {
		return
	}
	cfg.JournalWrite(snapshot, snapshotTokens, compactedCount, fallback)
}
