package main

import (
	"context"
	"errors"
	"strings"

	"github.com/dereksantos/cortex/internal/cache"
)

func (cs *CortexSession) startActivity(label string) {
	if cs.live != nil {
		cs.live.SetActivity(label)
	}
}

func (cs *CortexSession) stopActivity() {
	if cs.live != nil {
		cs.live.SetActivity("")
	}
}

func toolCallSignature(calls []ToolCall) string {
	var b strings.Builder
	for _, c := range calls {
		b.WriteString(c.Function.Name)
		b.WriteByte(0)
		b.WriteString(c.Function.Arguments)
		b.WriteByte('\n')
	}
	return b.String()
}

type TurnResult struct {
	Reply       string
	Interrupted bool
	// StopReason is the engine's raw loopStats.StopReason for this turn
	// (clean-finalize|salvaged-finalize|max-iter|read-budget|token-budget|
	// no-progress|deadline|error) — surfaced so a caller enforcing its own
	// per-run bounds (RunLoopFiring, M6.4) can tell a bound-forced stop from
	// a clean answer without re-deriving it.
	StopReason string
}

// Turn runs one turn with no progress notifications — today's behavior,
// preserved for every existing call site (REPL, discord, greeting, headless
// `turn`). TurnWithProgress is the identical logic with a Progress sink
// attached (M4.2b3's SSE handler is its only caller today); both delegate to
// the unexported turn so there's exactly one implementation.
func (cs *CortexSession) Turn(ctx context.Context, input string) (TurnResult, error) {
	return cs.turn(ctx, input, nil, 0, 0)
}

// TurnWithProgress is Turn with p (may be nil) wired into runLoop's existing
// Progress seam (cmd/cortex/loop.go) — the same breadcrumb sink the REPL's
// live display already drives, just not previously reachable from Turn().
func (cs *CortexSession) TurnWithProgress(ctx context.Context, input string, p Progress) (TurnResult, error) {
	return cs.turn(ctx, input, p, 0, 0)
}

// TurnWithBudget is Turn with per-run bound overrides (D11's loop-firing
// caps, M6.4): maxIter overrides the default maxToolIterations ceiling when
// >0 (loops.Spec.MaxTurns), and tokenBudget caps cumulative input+output
// tokens for the turn when >0 (loops.Spec.MaxTokens, Bounds.TokenBudget).
// Zero means "use the normal default" for either. RunLoopFiring
// (loop_run.go) is its only caller today.
func (cs *CortexSession) TurnWithBudget(ctx context.Context, input string, maxIter, tokenBudget int) (TurnResult, error) {
	return cs.turn(ctx, input, nil, maxIter, tokenBudget)
}

func (cs *CortexSession) turn(ctx context.Context, input string, progress Progress, maxIterOverride, tokenBudget int) (TurnResult, error) {
	// Stamp transcript entries with this turn's ordinal (resume replays them
	// into spans); cleared on exit so seed/compaction writes stay unstamped.
	cs.turnNo = cs.turns + 1
	defer func() { cs.turnNo = 0 }()

	turnStart := len(cs.Request.Messages)
	// Lazy init covers sessions built without NewCortexSession (tests, adapters):
	// the working set engages wherever turn content happens to start.
	if cs.ws == nil {
		cs.ws = cs.newWorkingSet(turnStart)
	}
	// Demote-then-send: if the hydrated tail has outgrown its watermark, move
	// the oldest turns into the outline zone (docs/context-architecture.md).
	// Labels count demoted turns monotonically (folds shrink cs.outline, so
	// its length regresses and cannot number entries).
	batch := cs.ws.DemoteBatch()
	for i, span := range batch {
		ordinal := cs.ws.Demoted() - len(batch) + i + 1
		cs.outline = append(cs.outline, turnOutlineEntry(ordinal, span, cs.Request.Messages[span.Start:span.End], cs.SessionID))
	}
	cs.foldOutlineIfNeeded(ctx)
	if len(cs.outline) > 0 || cs.outlineFolded != "" {
		cs.Request.OutlineBlock = cs.renderOutlineBlock()
	}
	cs.Request.PrefixEnd = cs.ws.Base()
	cs.Request.TailFrom = cs.ws.FrontierMsg()

	// Record the turn's span at exit no matter how the turn ends (error,
	// interrupt, panic): AddTurn enforces contiguity, so every appended
	// message must land in a span.
	defer func() {
		if end := len(cs.Request.Messages); end > turnStart {
			cs.ws.AddTurn(cache.TurnSpan{Start: turnStart, End: end, Tokens: estTurnTokens(cs.Request.Messages[turnStart:end])})
			cs.writeSessionState()
		}
	}()

	cs.Append(Message{Role: RoleUser, Content: input})
	cs.turnIntent = input

	// Put the memory index in its fixed wire slot. Never mutate the stored
	// system message: that would invalidate the prompt cache from byte zero and
	// append duplicate indexes on every turn.
	note := cs.memoryIndexNote()
	cs.Request.EphemeralSystem = note
	if note != "" {
		cs.injections++
		cs.injectedChars += len(note)
	}

	maxTok := cs.Request.MaxTokens
	if maxTok <= 0 {
		maxTok = codeMaxOutputTokens
	}
	maxIter := maxToolIterations
	if maxIterOverride > 0 {
		maxIter = maxIterOverride
	}
	ts := Toolset{Tools: cs.Request.Tools, Dispatch: cs.coderDispatcher(), BeforeBatch: cs.coderBeforeBatch}
	bounds := Bounds{MaxTokens: maxTok, MaxIter: maxIter, TokenBudget: tokenBudget}

	// Sample actual-vs-estimated context fill on every model round-trip (not
	// just interactively): the transcript otherwise has no record of how far
	// the char/4 demotion estimate drifts from what the provider actually
	// billed at any given moment mid-turn. See contextSample in session.go.
	iter := 0
	onStatusUpdate := func(lastPromptTokens, maxTokens int) {
		iter++
		// Update the session's token count for display
		cs.LastPromptTokens = lastPromptTokens
		tailEstNow := 0
		if cs.ws != nil {
			tailEstNow = cs.ws.TailTokens() + estTurnTokens(cs.Request.Messages[turnStart:])
		}
		cs.writeContextSample(iter, lastPromptTokens, maxTokens, tailEstNow)
		if cs.live != nil {
			// Force a redraw of the prompt line with updated context gauge
			cs.live.SetPrompt(cs.Prompt())
			cs.live.SetActivity("")
		}
	}
	var onAfterToolResult func()
	if cs.live != nil {
		// After each tool result is appended, force a prompt redraw
		// to update the context gauge with the current context size
		onAfterToolResult = func() {
			cs.live.SetPrompt(cs.Prompt())
			cs.live.SetActivity("")
		}
	}
	ts.AfterToolResult = onAfterToolResult

	_, stats, err := runLoop(ctx, cs.coderSender(), cs.Request, ts, bounds, progress, cs.Append, onStatusUpdate)
	cs.Request.EphemeralSystem = ""
	cs.turns++
	cs.tokensIn += stats.InputTokens
	cs.tokensOut += stats.OutputTokens
	cs.reasoningTokens += stats.ReasoningTokens
	cs.costUSD += stats.Cost
	cs.LastPromptTokens = stats.LastPromptTokens
	cs.LastCachedTokens = stats.LastCachedTokens

	if err != nil {
		return TurnResult{Interrupted: errors.Is(err, context.Canceled), StopReason: stats.StopReason}, err
	}

	turnMsgs := cs.Request.Messages[turnStart:]
	cs.captureTurn(input, turnMsgs)

	return TurnResult{Reply: lastAssistantText(turnMsgs), StopReason: stats.StopReason}, nil
}

func lastAssistantText(turnMsgs []Message) string {
	for i := len(turnMsgs) - 1; i >= 0; i-- {
		m := turnMsgs[i]
		if m.Role == "assistant" && strings.TrimSpace(m.Content) != "" {
			return m.Content
		}
	}
	return ""
}
