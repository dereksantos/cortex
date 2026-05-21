// Package ops — decide.recall_summary.
//
// Terminal node for the `recall` intent. The user is asking about
// something we've already done or decided ("what did we settle on for
// X", "remind me about Y"). This op:
//
//  1. Searches storage for events matching the prompt (text-based,
//     no embedder required — text search hits the same SQLite + JSONL
//     backing the eventsByTime index).
//  2. Builds a short context block from the top-N hits.
//  3. Asks a small LLM to synthesize a concise prose answer.
//  4. Forwards the answer to cfg.OnResponse so the REPL captures it
//     into LoopResult.Final.
//
// No tools, no verifier, no agent loop. Whole turn settles in a few
// seconds and a few hundred tokens.
//
// Fails safe: missing storage → synthesize from the prompt alone (no
// context block), labeled `grounded=false`. Missing provider → return
// a mechanical "no prior context indexed yet" reply rather than
// blocking the turn.
package ops

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/dereksantos/cortex/internal/storage"
	"github.com/dereksantos/cortex/pkg/cognition/dag"
	"github.com/dereksantos/cortex/pkg/events"
	"github.com/dereksantos/cortex/pkg/llm"
)

// RecallSummaryConfig wires the handler to the provider, storage, and
// the REPL's response sink. Storage may be nil — the handler then
// synthesizes from the prompt alone and labels the answer ungrounded.
type RecallSummaryConfig struct {
	Provider   llm.Provider
	Storage    *storage.Storage
	OnResponse func(reply string)

	// MaxResults caps the number of stored events folded into the
	// context block. Default 5 — enough to give the model a handful of
	// candidate decisions / commits / tool outputs without ballooning
	// the prompt past the small-model amplifier ceiling.
	MaxResults int
}

// recallSummaryResponse is the parser target.
type recallSummaryResponse struct {
	Answer   string `json:"answer"`
	Grounded bool   `json:"grounded"`
	Why      string `json:"why"`
}

// RecallSummarySpec returns the NodeSpec for decide.recall_summary.
// Marked Exposable so decide.next can spawn it directly when its plan
// notices a recall-shaped sub-question.
func RecallSummarySpec(cfg RecallSummaryConfig) dag.NodeSpec {
	return dag.NodeSpec{
		Function:    dag.FuncDecide,
		Op:          "recall_summary",
		Description: "answer a recall question using prior project context from storage; no tools, no verifier",
		Inputs: []dag.ParamSpec{
			{Name: "prompt", Type: "string", Required: true},
		},
		Outputs: []dag.ParamSpec{
			{Name: "answer", Type: "string"},
			{Name: "grounded", Type: "bool"},
			{Name: "results_count", Type: "int"},
			{Name: "why", Type: "string"},
			{Name: "fallback", Type: "bool"},
		},
		Cost:      recallSummaryCostHint,
		Exposable: true,
		Handler:   NewRecallSummaryHandler(cfg),
	}
}

// recallSummaryCostHint — one text-search (cheap) + one small-LLM
// synthesis call. Sized to fit under BudgetForIntent("recall") (20s
// / 3k tok) with room for the classifier that ran first and a
// possible follow-up.
var recallSummaryCostHint = dag.Cost{LatencyMS: 12000, Tokens: 1500}

// mechanicalRecallReply is the fallback when there's no provider —
// we can't synthesize, so we surface the situation honestly.
const mechanicalRecallReply = "I don't have a model wired up for this recall right now. Try re-asking once the provider is available, or rephrase as a direct question."

// NewRecallSummaryHandler returns the dag.Handler for
// decide.recall_summary.
//
// Inputs:
//   - prompt (string) — required; the user's recall question.
//
// Outputs:
//   - answer (string)       — prose synthesis (also forwarded to OnResponse).
//   - grounded (bool)       — true when answer is supported by retrieved events.
//   - results_count (int)   — number of events that made it into the context block.
//   - why (string)          — short rationale from the model.
//   - fallback (bool)       — true when the mechanical fallback fired.
func NewRecallSummaryHandler(cfg RecallSummaryConfig) dag.Handler {
	maxResults := cfg.MaxResults
	if maxResults <= 0 {
		maxResults = 5
	}
	return func(ctx context.Context, in map[string]any, budget dag.Budget) (dag.NodeResult, error) {
		started := time.Now()
		prompt := readString(in, "prompt")
		if prompt == "" {
			return dag.NodeResult{
				CostConsumed: dag.Cost{LatencyMS: int(time.Since(started).Milliseconds())},
			}, fmt.Errorf("decide.recall_summary: 'prompt' (string) is required")
		}

		if cfg.Provider == nil || !cfg.Provider.IsAvailable() || !budget.CanAfford(recallSummaryCostHint) {
			return recallMechanicalFallback(cfg, started, "provider unavailable or budget exhausted"), nil
		}

		// Text-search the journal-backed storage. Missing storage is
		// handled gracefully — synthesize from the prompt alone, label
		// the answer ungrounded so the user knows it isn't drawing on
		// history.
		var (
			matches []*events.Event
			ctxBlock string
		)
		if cfg.Storage != nil {
			results, serr := cfg.Storage.SearchEvents(prompt, maxResults)
			if serr == nil {
				matches = results
			}
		}
		ctxBlock = formatRecallContext(matches)

		pt, terr := LoadTemplate("decide_recall_summary")
		if terr != nil {
			return recallMechanicalFallback(cfg, started, fmt.Sprintf("template load: %v", terr)), nil
		}
		rendered, rerr := pt.Render(map[string]any{
			"prompt":  prompt,
			"context": ctxBlock,
		})
		if rerr != nil {
			return dag.NodeResult{
				CostConsumed: dag.Cost{LatencyMS: int(time.Since(started).Milliseconds())},
			}, fmt.Errorf("decide.recall_summary: render: %w", rerr)
		}

		resp, stats, gerr := cfg.Provider.GenerateWithStats(ctx, rendered)
		latency := int(time.Since(started).Milliseconds())
		if gerr != nil {
			return recallMechanicalFallback(cfg, started, fmt.Sprintf("llm error: %v", gerr)), nil
		}

		parsed, perr := parseRecallSummaryResponse(resp)
		if perr != nil || strings.TrimSpace(parsed.Answer) == "" {
			out := recallMechanicalFallback(cfg, started, fmt.Sprintf("parse error or empty answer: %v", perr))
			out.CostConsumed.Tokens = stats.TotalTokens()
			return out, nil
		}

		// When the user has no indexed history yet, force grounded=false
		// even if the model claims otherwise — the model can't have
		// grounded an answer in zero context.
		grounded := parsed.Grounded
		if len(matches) == 0 {
			grounded = false
		}

		answer := strings.TrimSpace(parsed.Answer)
		if cfg.OnResponse != nil {
			cfg.OnResponse(answer)
		}
		return dag.NodeResult{
			Out: map[string]any{
				"answer":        answer,
				"grounded":      grounded,
				"results_count": len(matches),
				"why":           parsed.Why,
				"fallback":      false,
			},
			CostConsumed: dag.Cost{LatencyMS: latency, Tokens: stats.TotalTokens()},
		}, nil
	}
}

// formatRecallContext renders matched events into a compact text block
// the synthesis prompt can ground in. Returns "(no prior context
// indexed for this query yet)" when there are no matches, so the
// model has a clear signal rather than an empty section it might
// pretend to read.
func formatRecallContext(matches []*events.Event) string {
	if len(matches) == 0 {
		return "(no prior context indexed for this query yet)"
	}
	var b strings.Builder
	for i, ev := range matches {
		fmt.Fprintf(&b, "[%d] %s — %s\n", i+1, ev.Timestamp.Format("2006-01-02 15:04"), string(ev.EventType))
		switch {
		case ev.Prompt != "":
			fmt.Fprintf(&b, "    user: %s\n", truncate(ev.Prompt, 240))
		case ev.ToolName != "":
			fmt.Fprintf(&b, "    tool: %s\n", ev.ToolName)
			if ev.ToolResult != "" {
				fmt.Fprintf(&b, "    out: %s\n", truncate(ev.ToolResult, 240))
			}
		}
	}
	return b.String()
}

// truncate caps s at max runes, suffixing "…" when trimmed. Keeps
// the context block under a predictable size regardless of how chatty
// any single event was.
func truncate(s string, max int) string {
	if max <= 0 || len(s) <= max {
		return s
	}
	return s[:max] + "…"
}

func parseRecallSummaryResponse(resp string) (recallSummaryResponse, error) {
	start := strings.Index(resp, "{")
	end := strings.LastIndex(resp, "}")
	if start < 0 || end <= start {
		return recallSummaryResponse{}, fmt.Errorf("no JSON object")
	}
	var parsed recallSummaryResponse
	if err := json.Unmarshal([]byte(resp[start:end+1]), &parsed); err != nil {
		return recallSummaryResponse{}, fmt.Errorf("unmarshal: %w", err)
	}
	return parsed, nil
}

// recallMechanicalFallback is the fail-safe reply. Forwards through
// OnResponse and packages a NodeResult with fallback=true.
func recallMechanicalFallback(cfg RecallSummaryConfig, started time.Time, why string) dag.NodeResult {
	if cfg.OnResponse != nil {
		cfg.OnResponse(mechanicalRecallReply)
	}
	return dag.NodeResult{
		Out: map[string]any{
			"answer":        mechanicalRecallReply,
			"grounded":      false,
			"results_count": 0,
			"why":           why,
			"fallback":      true,
		},
		CostConsumed: dag.Cost{LatencyMS: int(time.Since(started).Milliseconds())},
	}
}
