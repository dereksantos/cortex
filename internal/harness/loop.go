package harness

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
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
	ReasonNoProgress  LoopReason = "no_progress"  // sliding window of recent turns showed no side-effecting work
)

// noProgressWindow is the count of consecutive recent turns the
// loop watches for "no productive work" before stopping with
// ReasonNoProgress. The two conditions that count as no progress in
// a window of this size are:
//
//  1. Zero write_file / run_shell calls — pure exploration that
//     never lands a change is the dominant agent-loop pathology on
//     small models.
//  2. Identical read_file / list_dir targets across every turn —
//     the model re-reading the same files in a circle.
//
// 5 turns is generous enough that legitimate exploration (search →
// read → search → think → write) clears the bar, but tight enough
// that the binding constraint replaces the old 8-turn hard cap
// without making sessions unbounded.
const noProgressWindow = 5

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

// ToolDispatcher, when set on a Loop, replaces the inline
// Registry.Dispatch call for each tool the model emits. Used by
// Stage 3 to route tool calls through the DAG executor as act.*
// nodes — the dispatcher implementation can record per-tool trace
// rows, honor AxisContract gates, and surface tool latency / cost
// as separate child nodes. Returns the tool output string (same
// shape Registry.Dispatch returns) — the loop forwards it back to
// the model unchanged.
//
// The Loop passes its own *ToolRegistry as reg so the dispatcher
// can delegate to the harness's actual tool instances via
// reg.Dispatch — preserving FilesWritten / ShellNonZeroExits /
// InjectedContextTokens accounting. A dispatcher that constructs
// its own parallel tools breaks those fields silently; principle 5
// (Reproducible) + 7 (Structured) forbid that.
//
// V0 (no dispatcher set) preserves the inline-dispatch behavior the
// loop has always had.
type ToolDispatcher func(ctx context.Context, reg *ToolRegistry, call llm.ToolCall) (string, error)

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

	// Dispatcher, if set, replaces the inline Registry.Dispatch call
	// per tool. nil → use Registry.Dispatch (V0 behavior). See
	// ToolDispatcher doc for the Stage-3 contract.
	Dispatcher ToolDispatcher

	// PriorMessages, if non-empty, are inserted between the system
	// prompt and the current user message. Used by the REPL to give
	// the model conversation history from prior accepted turns
	// (alternating user/assistant pairs, no tool-call traces).
	//
	// The Loop doesn't validate the role sequence — callers are
	// expected to pass a well-formed alternation. Token-budget
	// trimming is the caller's job too; the Loop forwards as-is.
	PriorMessages []llm.ChatMessage

	// ContextWindowTokens is the model server's n_ctx, when known.
	// Used for two things:
	//   1. Proactive budgeting — before each call, the loop
	//      estimates msgs token count; if over a safety threshold
	//      it trims the oldest PriorMessages.
	//   2. Setting an upper bound for the catch-and-retry path
	//      below (the safe-retry target is ~70% of this value).
	//
	// 0 disables proactive budgeting; the retry path still works
	// because it learns n_ctx from the server's error response and
	// updates this field for the rest of the session.
	ContextWindowTokens int

	// AccumulatorSnapshot, when non-nil, is invoked before each
	// provider call after the first turn. The returned string (if
	// non-empty) is the bounded working memory that subsumes prior
	// tool outputs. The loop injects it as a synthetic user message
	// right after the original user prompt (refreshed each turn) and
	// stubs the Content of tool-role messages outside the
	// KeepRecentTurns window — the snapshot is the new source of
	// truth for what those tools produced.
	//
	// Wired by callers (decide.coding_turn dispatcher) that fold
	// every tool output through attend.accumulate and deposit
	// snapshots into the DAG executor's turn state. The loop's
	// per-turn input then stays bounded by
	// snapshot_max_tokens + KeepRecentTurns × max_tool_output rather
	// than growing linearly with turn count. See
	// internal/eval/accumulator/eval.go for the bounded-emergence
	// invariant this implements at the inner-loop layer.
	//
	// nil → behavior unchanged (history grows linearly with tool
	// outputs, the pre-bounded-context default).
	AccumulatorSnapshot func(context.Context) string

	// KeepRecentTurns is how many recent (assistant tool_call,
	// tool_result(s)) pairs the rewrite keeps verbatim. Older tool
	// results get their Content replaced with a stub; the assistant
	// tool_call messages are always preserved so tool_call_id
	// matching stays intact. Defaults to 1 when AccumulatorSnapshot
	// is set — the immediately prior turn stays full-fidelity, older
	// turns live in the snapshot.
	KeepRecentTurns int

	// Stats accumulated as the loop runs.
	tokensIn  int
	tokensOut int
	costUSD   float64
}

// defaultMaxTurns is a safety ceiling, not the binding constraint.
// After Phase 3 the no-progress signal stops the loop when the model
// spins on exploration; before that, Budget catches runaway cost.
// 50 turns is enough headroom that productive long sessions don't
// trip it, and is high enough that hitting it indicates a bug in
// the agent's tool discipline rather than legitimate work.
const (
	defaultMaxTurns = 50
)

// progressTracker tracks a sliding window of recent turns' tool
// shapes so the loop can stop early when the model spins without
// producing side-effecting work. See noProgressWindow doc for the
// two conditions that fire ReasonNoProgress.
//
// turnShapes records one entry per turn that DID issue tool calls.
// A turn the model spent only writing assistant text (no tool calls)
// is the model-done case and is handled separately — it does not
// enter the tracker. This keeps the tracker focused on the
// "tool-calling-but-not-progressing" pathology.
type progressTracker struct {
	turnShapes []turnShape
}

type turnShape struct {
	hadWriteOrShell bool
	readTargets     string // sorted-joined read_file/list_dir args; empty if none
}

func (p *progressTracker) recordTurn(calls []llm.ToolCall) {
	var reads []string
	hadWrite := false
	for _, c := range calls {
		switch c.Function.Name {
		case "write_file", "run_shell":
			hadWrite = true
		case "read_file", "list_dir":
			reads = append(reads, c.Function.Arguments)
		}
	}
	sort.Strings(reads)
	p.turnShapes = append(p.turnShapes, turnShape{
		hadWriteOrShell: hadWrite,
		readTargets:     strings.Join(reads, "|"),
	})
	if len(p.turnShapes) > noProgressWindow {
		p.turnShapes = p.turnShapes[len(p.turnShapes)-noProgressWindow:]
	}
}

// noProgress reports whether the recent window suggests the loop
// is spinning. Returns false until the window is full so early
// exploration isn't punished.
func (p *progressTracker) noProgress() bool {
	if len(p.turnShapes) < noProgressWindow {
		return false
	}
	// Condition 1: zero write_file/run_shell calls in the entire window.
	anyWrite := false
	for _, s := range p.turnShapes {
		if s.hadWriteOrShell {
			anyWrite = true
			break
		}
	}
	if !anyWrite {
		return true
	}
	// Condition 2: every turn in the window re-reads the same set of
	// targets (and that set is non-empty). The model is reading in a
	// circle without writing.
	first := p.turnShapes[0].readTargets
	if first == "" {
		return false
	}
	for _, s := range p.turnShapes[1:] {
		if s.readTargets != first {
			return false
		}
	}
	return true
}

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
	if len(l.PriorMessages) > 0 {
		msgs = append(msgs, l.PriorMessages...)
	}
	msgs = append(msgs, llm.ChatMessage{Role: "user", Content: userPrompt})

	// keepHead protects the system prompt from being trimmed; the
	// loop's TrimChatHistory targets only the PriorMessages /
	// mid-session sections between head and tail.
	keepHead := 0
	if l.System != "" {
		keepHead = 1
	}

	specs := l.Registry.Specs()
	l.note("coding.session_start", map[string]any{
		"model":                 l.Provider.Model(),
		"max_turns":             maxTurns,
		"max_cumulative_tokens": budget.MaxCumulativeTokens,
		"max_cost":              budget.MaxCostUSD,
		"num_tools":             len(specs),
		"user_prompt":           userPrompt,
		"no_progress_window":    noProgressWindow,
	})

	progress := &progressTracker{}

	for turn := 0; turn < maxTurns; turn++ {
		if err := ctx.Err(); err != nil {
			res.Reason = ReasonContextDone
			res.Err = err
			break
		}

		// Bounded-context rewrite: when the caller wired an accumulator
		// (decide.coding_turn's dispatcher folds every tool output
		// through attend.accumulate and deposits a snapshot), drink
		// from the latest snapshot before each provider call after
		// turn 0. The snapshot subsumes older tool outputs; the rewrite
		// stubs their Content so the per-turn input plateaus instead of
		// growing linearly. Turn 0 has no prior outputs to fold.
		if turn > 0 && l.AccumulatorSnapshot != nil {
			snap := l.AccumulatorSnapshot(ctx)
			if snap != "" {
				keep := l.KeepRecentTurns
				if keep < 1 {
					keep = 1
				}
				before := llm.EstimateChatTokens(msgs)
				if rewrote, _ := rewriteHistoryWithSnapshot(&msgs, snap, keep); rewrote {
					l.note("coding.context_rewrite", map[string]any{
						"turn":              turn,
						"snapshot_tokens":   len(snap) / 4,
						"keep_recent_turns": keep,
						"estimated_before":  before,
						"estimated_after":   llm.EstimateChatTokens(msgs),
					})
				}
			}
		}

		// Proactive budget — if we know n_ctx, trim PriorMessages
		// when the estimated prompt size approaches the cap. The 85%
		// threshold leaves headroom for response tokens + estimator
		// drift. keepTail=1 protects the current user message.
		// trimAndNotify is a no-op when nothing exceeds the threshold,
		// so the loop's local msgs stays the source of truth either way.
		if l.ContextWindowTokens > 0 {
			threshold := (l.ContextWindowTokens * 85) / 100
			trimAndNotify(l, &msgs, threshold, keepHead, 1, "proactive")
		}

		callRes, stats, err := l.Provider.GenerateWithTools(ctx, msgs, specs, "auto")
		// Catch-and-retry: when the server reports an overflow, learn
		// n_ctx from the error, trim aggressively to ~70% of n_ctx,
		// and retry exactly once. Subsequent turns in this session
		// benefit from the learned ContextWindowTokens via the
		// proactive branch above.
		if overflow, ok := llm.AsContextOverflow(err); ok {
			if overflow.AvailableTokens > 0 {
				l.ContextWindowTokens = overflow.AvailableTokens
			}
			target := l.ContextWindowTokens
			if target > 0 {
				target = (target * 70) / 100
			}
			dropped := trimAndNotify(l, &msgs, target, keepHead, 1, "overflow_retry")
			l.note("coding.context_overflow_retry", map[string]any{
				"turn":             turn,
				"available_tokens": overflow.AvailableTokens,
				"requested_tokens": overflow.RequestedTokens,
				"dropped_messages": dropped,
			})
			callRes, stats, err = l.Provider.GenerateWithTools(ctx, msgs, specs, "auto")
		}
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
			var out string
			if l.Dispatcher != nil {
				out, _ = l.Dispatcher(ctx, l.Registry, call)
			} else {
				out, _ = l.Registry.Dispatch(ctx, call)
			}
			msgs = append(msgs, llm.ToolResultMessage(call.ID, call.Function.Name, out))
			l.note("coding.tool_result", map[string]any{
				"turn":         turn,
				"id":           call.ID,
				"name":         call.Function.Name,
				"output_chars": len(out),
			})
		}

		// Record the turn's tool shape and stop if the recent window
		// shows no productive work. This is the binding constraint
		// post-Phase-3 — before it, the integer MaxTurns was what
		// stopped exploratory sessions too early. Budget still wins
		// when cost or tokens cross their caps.
		progress.recordTurn(callRes.ToolCalls)
		if progress.noProgress() {
			res.Reason = ReasonNoProgress
			l.note("coding.no_progress", map[string]any{
				"turn":   turn,
				"window": noProgressWindow,
			})
			break
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

// trimAndNotify trims msgs in-place to fit maxTokens, emitting a
// coding.history_trimmed event when any messages were dropped.
// reason is "proactive" (pre-call budget) or "overflow_retry"
// (server reported overflow); used in the event payload so the
// transcript distinguishes the two paths.
//
// Returns the number of messages dropped. 0 when no trim was needed
// or when the budget already fits.
func trimAndNotify(l *Loop, msgs *[]llm.ChatMessage, maxTokens, keepHead, keepTail int, reason string) int {
	if maxTokens <= 0 {
		return 0
	}
	before := llm.EstimateChatTokens(*msgs)
	trimmed, dropped := llm.TrimChatHistory(*msgs, maxTokens, keepHead, keepTail)
	if dropped == 0 {
		return 0
	}
	*msgs = trimmed
	l.note("coding.history_trimmed", map[string]any{
		"reason":           reason,
		"dropped_messages": dropped,
		"estimated_before": before,
		"estimated_after":  llm.EstimateChatTokens(trimmed),
		"max_tokens":       maxTokens,
	})
	return dropped
}

// Sentinels used by the accumulator-snapshot rewrite path. The
// working-memory message is identified by its Content prefix so the
// loop can find and refresh it across turns without tracking an
// index (TrimChatHistory may shift positions). The tool-result stub
// replaces the Content of older tool messages once their information
// is folded into the snapshot.
const (
	workingMemorySentinel = "[CORTEX_WORKING_MEMORY]"
	toolResultStub        = "[folded into working memory above]"
)

// rewriteHistoryWithSnapshot injects/refreshes a synthetic
// working-memory user message right after the original user prompt
// and replaces the Content of tool-role messages outside the
// last `keep` (assistant_tool_call, tool_result*) pairs with a stub.
//
// Bounded-context invariant: each call's input becomes
// (system + priorMessages + user + working_memory + last_K_turns)
// rather than (system + priorMessages + user + all_tool_outputs).
// Per-turn input size plateaus instead of growing with turn count.
// See internal/eval/accumulator/eval.go for the proof-of-concept
// pattern this implements at the inner-loop layer.
//
// In-place mutation. Returns (true, newLen) if the rewrite ran
// (snapshot non-empty + at least the original user prompt present),
// (false, len(msgs)) when there was nothing to do.
//
// keep < 1 is normalized to 1: the immediately prior turn is always
// kept verbatim so the model has direct context for what it just did.
func rewriteHistoryWithSnapshot(msgs *[]llm.ChatMessage, snapshot string, keep int) (bool, int) {
	if snapshot == "" {
		return false, len(*msgs)
	}
	if keep < 1 {
		keep = 1
	}

	// Locate the original user prompt: the LAST role=user message
	// whose Content does NOT start with the working-memory sentinel.
	// "Last" so REPL PriorMessages (alternating user/assistant) before
	// the current prompt aren't mistaken for it.
	ms := *msgs
	userIdx := -1
	for i := len(ms) - 1; i >= 0; i-- {
		if ms[i].Role == "user" && !strings.HasPrefix(ms[i].Content, workingMemorySentinel) {
			userIdx = i
			break
		}
	}
	if userIdx < 0 {
		return false, len(ms)
	}

	wmContent := workingMemorySentinel + "\n\nWorking memory (synthesized from prior tool outputs in this turn — use as ground truth; do not re-fetch what's here):\n\n" + snapshot

	// Find or insert the working-memory message right after userIdx.
	var wmIdx int
	if userIdx+1 < len(ms) && ms[userIdx+1].Role == "user" && strings.HasPrefix(ms[userIdx+1].Content, workingMemorySentinel) {
		wmIdx = userIdx + 1
		ms[wmIdx].Content = wmContent
	} else {
		insertIdx := userIdx + 1
		ms = append(ms, llm.ChatMessage{})
		copy(ms[insertIdx+1:], ms[insertIdx:])
		ms[insertIdx] = llm.ChatMessage{Role: "user", Content: wmContent}
		wmIdx = insertIdx
	}

	// Walk back from end, count assistant tool-call turns until we
	// hit `keep` of them. The boundary is the index of the oldest
	// kept assistant turn; tool messages between wmIdx+1 and
	// boundary-1 get stubbed.
	boundary := -1
	seen := 0
	for i := len(ms) - 1; i > wmIdx; i-- {
		if ms[i].Role == "assistant" && len(ms[i].ToolCalls) > 0 {
			seen++
			if seen == keep {
				boundary = i
				break
			}
		}
	}
	if boundary > wmIdx+1 {
		for i := wmIdx + 1; i < boundary; i++ {
			if ms[i].Role == "tool" && ms[i].Content != toolResultStub {
				ms[i].Content = toolResultStub
			}
		}
	}

	*msgs = ms
	return true, len(ms)
}
