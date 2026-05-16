package harness

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/dereksantos/cortex/pkg/llm"
)

// LoopProvider is the subset of *llm.OpenRouterClient the loop needs.
// Modeled as an interface so tests can substitute a scripted fake; in
// production the only implementation is *llm.OpenRouterClient.
type LoopProvider interface {
	GenerateWithTools(ctx context.Context, msgs []llm.ChatMessage, tools []llm.ToolSpec, toolChoice any) (llm.ChatResult, llm.GenerationStats, error)
	LastCostUSD() float64
	Model() string
}

// Budget caps cumulative spend across the entire loop. Either limit
// hitting zero stops the loop with reason=budget.
//
// Per-turn caps live elsewhere:
//   - max output tokens per assistant turn is the provider's
//     SetMaxTokens (see CortexHarness which sets it from the model's
//     known max-output via ModelMaxOutputTokens).
//   - context-window enforcement is the provider's job (Anthropic
//     auto-trims oldest messages; OpenRouter passes through).
//
// MaxCumulativeTokens is the sum of input+output tokens across ALL
// turns — input grows on every turn because the conversation
// history is replayed. With Haiku-class models a 12-turn coding
// session typically consumes 100K-200K cumulative tokens; the 300K
// default leaves headroom for retries before the cap bites.
type Budget struct {
	MaxCumulativeTokens int     // sum of input + output across all turns
	MaxCostUSD          float64 // sum of LastCostUSD() across turns
}

// LoopReason is the structured cause for a loop terminating.
type LoopReason string

const (
	ReasonModelDone   LoopReason = "model_done"   // model emitted a final assistant message with no tool calls
	ReasonTurnLimit   LoopReason = "turn_limit"   // hit MaxTurns before the model said it was done
	ReasonBudget      LoopReason = "budget"       // tokens or cost budget exhausted
	ReasonContextDone LoopReason = "context_done" // ctx cancelled or timed out
	ReasonError       LoopReason = "error"        // provider call failed; check Err
)

// LoopResult is returned after Run() completes.
type LoopResult struct {
	Reason    LoopReason
	Final     string // the final assistant content (may be empty on turn_limit/budget)
	Turns     int    // number of assistant turns taken
	TokensIn  int
	TokensOut int
	CostUSD   float64
	StartedAt time.Time
	EndedAt   time.Time
	Err       error // non-nil only when Reason == ReasonError or ReasonContextDone

	// Tool accounting, surfaced for the HarnessResult mapping.
	InjectedContextTokens int
	ShellNonZeroExits     int
	FilesWritten          []string
}

// Loop holds the per-session state needed to run one harness session.
// Construct once per session — the registry's accounting (shell exits,
// files written) is session-scoped.
type Loop struct {
	Provider   LoopProvider
	Registry   *ToolRegistry
	System     string      // system prompt (constant for the session)
	MaxTurns   int         // hard cap; 0 -> defaultMaxTurns
	Budget     Budget      // 0 fields -> defaultBudget
	Transcript *Transcript // optional; nil disables JSONL transcript

	// Notify, if set, is invoked synchronously on every loop event
	// before the Transcript is written. Used by interactive
	// front-ends (e.g. `cortex code`) that want human-readable
	// progress to stdout — the JSONL transcript stays the canonical
	// machine-readable log. Payload shape mirrors what Transcript
	// receives.
	Notify func(kind string, payload any)

	// Stats accumulated as the loop runs.
	tokensIn  int
	tokensOut int
	costUSD   float64
}

// Loop defaults. Conservative for iteration 1: a small model that
// can't drive 25 productive turns will hit the budget cap instead of
// the turn cap, which is fine — both signal "the harness held the
// rails closed, the model couldn't finish".
const (
	defaultMaxTurns = 25
)

// defaultBudget bounds cumulative spend for a typical coding
// session. Set generously enough that the COST cap is the brake
// you actually hit. Haiku's LRU run consumed ~138K cumulative
// tokens across 12 turns; 300K leaves room for self-correction
// iterations on harder tasks before the cumulative cap bites.
var defaultBudget = Budget{
	MaxCumulativeTokens: 300_000,
	MaxCostUSD:          0.20,
}

// Run drives one session. The initial user message is the task the
// model is being asked to perform. Returns when the model declares
// itself done, the loop hits MaxTurns or Budget, or ctx is cancelled.
//
// Run does NOT call Transcript.Close() — the caller (typically the
// CortexHarness adapter) owns transcript lifetime so it can attach
// post-loop diagnostics (e.g. CellResult.RunID) before closing.
func (l *Loop) Run(ctx context.Context, userPrompt string) (LoopResult, error) {
	if l.Provider == nil {
		return LoopResult{Reason: ReasonError}, errors.New("loop: provider is nil")
	}
	if l.Registry == nil {
		return LoopResult{Reason: ReasonError}, errors.New("loop: registry is nil")
	}

	maxTurns := l.MaxTurns
	if maxTurns <= 0 {
		maxTurns = defaultMaxTurns
	}
	budget := l.Budget
	if budget.MaxCumulativeTokens == 0 {
		budget.MaxCumulativeTokens = defaultBudget.MaxCumulativeTokens
	}
	if budget.MaxCostUSD == 0 {
		budget.MaxCostUSD = defaultBudget.MaxCostUSD
	}

	started := time.Now().UTC()
	res := LoopResult{StartedAt: started}

	msgs := []llm.ChatMessage{}
	if l.System != "" {
		msgs = append(msgs, llm.ChatMessage{Role: "system", Content: l.System})
	}
	msgs = append(msgs, llm.ChatMessage{Role: "user", Content: userPrompt})

	specs := l.Registry.Specs()
	l.note("coding.session_start", map[string]any{
		"model":                 l.Provider.Model(),
		"max_turns":             maxTurns,
		"max_cumulative_tokens": budget.MaxCumulativeTokens,
		"max_cost":              budget.MaxCostUSD,
		"num_tools":             len(specs),
		"user_prompt":           userPrompt,
	})

	for turn := 0; turn < maxTurns; turn++ {
		if err := ctx.Err(); err != nil {
			res.Reason = ReasonContextDone
			res.Err = err
			break
		}

		callRes, stats, err := l.Provider.GenerateWithTools(ctx, msgs, specs, "auto")
		if err != nil {
			res.Reason = ReasonError
			res.Err = fmt.Errorf("turn %d: %w", turn, err)
			l.note("coding.error", map[string]any{
				"turn":  turn,
				"error": err.Error(),
			})
			break
		}
		l.tokensIn += stats.InputTokens
		l.tokensOut += stats.OutputTokens
		l.costUSD += l.Provider.LastCostUSD()
		res.Turns = turn + 1

		l.note("coding.turn", map[string]any{
			"turn":           turn,
			"finish_reason":  callRes.FinishReason,
			"content_chars":  len(callRes.Content),
			"tool_calls":     len(callRes.ToolCalls),
			"tokens_in":      stats.InputTokens,
			"tokens_out":     stats.OutputTokens,
			"cumulative_in":  l.tokensIn,
			"cumulative_out": l.tokensOut,
			"cumulative_usd": l.costUSD,
		})

		// Model is done if no tool calls and there's content.
		if len(callRes.ToolCalls) == 0 {
			res.Final = callRes.Content
			res.Reason = ReasonModelDone
			l.note("coding.final", map[string]any{
				"content": callRes.Content,
				"turn":    turn,
			})
			break
		}

		// Append the assistant's tool-calling turn to history, then
		// dispatch each tool and append the results.
		msgs = append(msgs, llm.AssistantMessageFromResult(callRes))

		for _, call := range callRes.ToolCalls {
			l.note("coding.tool_call", map[string]any{
				"turn": turn,
				"id":   call.ID,
				"name": call.Function.Name,
				"args": call.Function.Arguments,
			})
			out, _ := l.Registry.Dispatch(ctx, call)
			msgs = append(msgs, llm.ToolResultMessage(call.ID, call.Function.Name, out))
			l.note("coding.tool_result", map[string]any{
				"turn":         turn,
				"id":           call.ID,
				"name":         call.Function.Name,
				"output_chars": len(out),
			})
		}

		// Check budget after dispatching so the last turn's spending is counted.
		if l.tokensIn+l.tokensOut >= budget.MaxCumulativeTokens || l.costUSD >= budget.MaxCostUSD {
			res.Reason = ReasonBudget
			l.note("coding.budget_exceeded", map[string]any{
				"cumulative_tokens": l.tokensIn + l.tokensOut,
				"cost_usd":          l.costUSD,
				"cap_tokens":        budget.MaxCumulativeTokens,
				"cap_cost":          budget.MaxCostUSD,
			})
			break
		}
	}

	if res.Reason == "" {
		res.Reason = ReasonTurnLimit
		l.note("coding.turn_limit", map[string]any{"turns": res.Turns})
	}

	res.TokensIn = l.tokensIn
	res.TokensOut = l.tokensOut
	res.CostUSD = l.costUSD
	res.EndedAt = time.Now().UTC()
	res.InjectedContextTokens = l.Registry.InjectedContextTokens()
	res.ShellNonZeroExits = l.Registry.ShellNonZeroExits()
	res.FilesWritten = l.Registry.FilesWritten()
	return res, nil
}

// note routes one event to the live Notify hook and the JSONL
// transcript. Failures from either are swallowed — a broken sink
// must not kill the loop mid-session.
func (l *Loop) note(kind string, payload any) {
	if l.Notify != nil {
		l.Notify(kind, payload)
	}
	if l.Transcript == nil {
		return
	}
	_ = l.Transcript.WriteEntry(kind, payload)
}
