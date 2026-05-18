// Package dagnode bridges the DAG executor (pkg/cognition/dag) to
// the existing Cortex coding harness (internal/eval/v2 + internal/
// harness). It provides handlers for cortex-function-typed DAG nodes
// that need to invoke real LLM-backed work — primarily
// `decide.coding_turn`.
//
// Per ADR-001 (docs/adrs/0001-coding-turn-structure.md), V0
// `decide.coding_turn` runs the existing agent loop INLINE within a
// single handler call. The handler returns the final response in
// Out + total CostConsumed; it does NOT spawn act.* children in V0
// (that's Stage 3 per the same ADR).
package dagnode

import (
	"context"
	"fmt"
	"time"

	evalv2 "github.com/dereksantos/cortex/internal/eval/v2"
	"github.com/dereksantos/cortex/pkg/cognition/dag"
)

// CodingTurnConfig wires the handler to a model + workdir at
// registration time. Both fields can be empty: if Model is empty the
// handler returns a stub-mode NodeResult; if Workdir is empty it
// defaults to ".".
type CodingTurnConfig struct {
	Model   string // OpenRouter-qualified model id, e.g. "anthropic/claude-haiku-4.5"
	Workdir string // workdir the harness operates on; defaults to "." (cwd)
}

// NewCodingTurnHandler returns a dag.Handler for decide.coding_turn.
//
// V0 behavior (per ADR-001):
//   - Reads `prompt` from in (string)
//   - Reads `model` from in.attrs override if present, else cfg.Model
//   - If no model configured: returns stub NodeResult with a clear
//     error_message indicating provider-not-configured; this lets
//     `cortex run --type=turn` exercise the rest of the DAG without
//     requiring API keys for every developer iteration
//   - If model configured: constructs evalv2.CortexHarness, runs the
//     session, returns the final response + actual cost
//   - Does NOT spawn act.* children (Stage 3)
//
// Errors from the harness are returned as the second return value;
// the executor will log them as handler_error TraceEntry rows.
func NewCodingTurnHandler(cfg CodingTurnConfig) dag.Handler {
	return func(ctx context.Context, in map[string]any, budget dag.Budget) (dag.NodeResult, error) {
		prompt, _ := in["prompt"].(string)
		if prompt == "" {
			return dag.NodeResult{}, fmt.Errorf("decide.coding_turn: prompt input is required")
		}

		// Per-call model override (attrs) wins over registration default.
		model := cfg.Model
		if m, ok := in["model"].(string); ok && m != "" {
			model = m
		}

		// Stub-mode path: no model configured. Return success with a
		// clear stub indicator. This is the path cortex run --type=turn
		// takes by default (so developers can exercise the protocol
		// without API keys).
		if model == "" {
			return dag.NodeResult{
				Out: map[string]any{
					"response": fmt.Sprintf("[stub coding_turn] prompt was: %q (no provider configured; set --model to invoke real LLM)", prompt),
					"stub":     true,
				},
				CostConsumed: dag.Cost{LatencyMS: 10, Tokens: 0},
			}, nil
		}

		// Real path: construct the existing CortexHarness and run.
		// This is the V0 inline form per ADR-001 — wraps the LLM agent
		// loop as one opaque node. Stage 3 will refactor to spawn
		// act.* children for each tool call.
		started := time.Now()
		h, herr := evalv2.NewCortexHarness(model)
		if herr != nil {
			return dag.NodeResult{
				Out:          map[string]any{"error": herr.Error()},
				CostConsumed: dag.Cost{LatencyMS: int(time.Since(started).Milliseconds())},
			}, fmt.Errorf("decide.coding_turn init: %w", herr)
		}

		workdir := cfg.Workdir
		if workdir == "" {
			workdir = "."
		}

		hr, runErr := h.RunSessionWithResult(ctx, prompt, workdir)
		latency := int(time.Since(started).Milliseconds())
		if runErr != nil {
			return dag.NodeResult{
				Out: map[string]any{
					"error": runErr.Error(),
				},
				CostConsumed: dag.Cost{LatencyMS: latency, Tokens: hr.TokensIn + hr.TokensOut},
			}, fmt.Errorf("decide.coding_turn run: %w", runErr)
		}

		lr := h.LastLoopResult()
		return dag.NodeResult{
			Out: map[string]any{
				"response":      lr.Final,
				"turns":         hr.AgentTurnsTotal,
				"tokens_in":     hr.TokensIn,
				"tokens_out":    hr.TokensOut,
				"cost_usd":      hr.CostUSD,
				"files_changed": hr.FilesChanged,
			},
			CostConsumed: dag.Cost{
				LatencyMS: latency,
				Tokens:    hr.TokensIn + hr.TokensOut,
			},
		}, nil
	}
}
