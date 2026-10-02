package main

import (
	"context"
	"errors"
	"strings"

	"github.com/dereksantos/cortex/internal/cache"
	"github.com/dereksantos/cortex/internal/journal"
)

func (cs *CortexSession) startActivity(label string) {
	cs.setPhase(phaseThinking) // a running tool is busy time, same light as reasoning
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
	// TestReceipt is the harness's own "tests changed: …" line for this turn
	// (issue #141, testwatch.go): non-empty when the turn removed or
	// substantially shrank one of the project's test files. It rides the
	// result so the CALLER of Turn surfaces it to a human (and a headless
	// driver) — the journal capture alone records it but reaches no one
	// reading the turn, which is exactly the unreported-loss gap #141 names.
	// Callers that want to also push it into the model must inject it into
	// the request before the final answer (see turn's finalize hook); this
	// field is the durable, always-present half of that.
	TestReceipt string
	// DebugReceipt is the harness's own "leftover debug: …" line for this
	// turn (issue #154, testwatch.go's ScanDebug): non-empty when the turn
	// added debug prints to non-test production files or left scratch-named
	// files in the workspace. It rides the result the same way TestReceipt
	// does, so the CALLER of Turn can surface it to a human (and a headless
	// driver) — the journal capture records it but reaches no one reading
	// the turn. It is surfaced to the model in the same finalize note as
	// TestReceipt (turn's finalize hook), so the model's final answer
	// accounts for the leftover debug too.
	DebugReceipt string
	// LintReceipt is the harness's own "lint: …" receipt for this turn
	// (issue #129 piece 3, turn_lint.go): non-empty when the turn-end lint
	// pass (the project's lint, run ONCE over the turn's distinct touched
	// files, "all" mode + trusted workspace only) found problems or hit
	// the turn's lint budget. It rides the result the same way
	// TestReceipt does, so the CALLER of Turn can surface it to a human
	// (REPL, headless `cortex turn`) — the journal capture records it but
	// reaches no one reading the turn. It is surfaced to the model in the
	// same finalize round as TestReceipt (turn's finalize hook), so the
	// model can fix findings while it still can.
	LintReceipt string
}

// Turn runs one turn with no progress notifications — today's behavior,
// preserved for every existing call site (REPL, discord, greeting, headless
// `turn`). TurnWithProgress is the identical logic with a Progress sink
// attached (M4.2b3's SSE handler is its only caller today); both delegate to
// the unexported turn so there's exactly one implementation.
func (cs *CortexSession) Turn(ctx context.Context, input string) (TurnResult, error) {
	return cs.turn(ctx, input, nil, 0, 0, FinalizeInteractive)
}

// TurnWithProgress is Turn with p (may be nil) wired into runLoop's existing
// Progress seam (cmd/cortex/loop.go) — the same breadcrumb sink the REPL's
// live display already drives, just not previously reachable from Turn().
func (cs *CortexSession) TurnWithProgress(ctx context.Context, input string, p Progress) (TurnResult, error) {
	return cs.turn(ctx, input, p, 0, 0, FinalizeInteractive)
}

// TurnWithBudget is Turn with per-run bound overrides (D11's loop-firing
// caps, M6.4): maxIter overrides the default maxToolIterations ceiling when
// >0 (loops.Spec.MaxTurns), and tokenBudget caps cumulative input+output
// tokens for the turn when >0 (loops.Spec.MaxTokens, Bounds.TokenBudget).
// Zero means "use the normal default" for either. RunLoopFiring
// (loop_run.go) is its only caller today.
func (cs *CortexSession) TurnWithBudget(ctx context.Context, input string, maxIter, tokenBudget int) (TurnResult, error) {
	// A loop firing has no interlocutor: a forced finalize must not end by
	// asking whether to continue — nobody is there to answer.
	return cs.turn(ctx, input, nil, maxIter, tokenBudget, FinalizeSubagent)
}

func (cs *CortexSession) turn(ctx context.Context, input string, progress Progress, maxIterOverride, tokenBudget int, finalize FinalizeStyle) (TurnResult, error) {
	// Stamp transcript entries with this turn's ordinal (resume replays them
	// into spans); cleared on exit so seed/compaction writes stay unstamped.
	cs.turnNo = cs.turns + 1
	defer func() { cs.turnNo = 0 }()

	cs.setPhase(phaseThinking)
	defer cs.setPhase(phaseIdle)

	turnStart := len(cs.Request.Messages)
	// Issue #141: clear any stale test-file before-snapshot carried over from
	// an earlier turn that never reached captureTurn (error or interrupt
	// paths return before it — turn.go's early returns). Dropping it here, at
	// the START of every turn, guarantees a turn's receipt only ever diffs
	// against that turn's own before-side; the 32-file cap can't fill up
	// across turns either. Issue #129 piece 3: the same drop clears the
	// turn-end lint pass's touched-file list and re-arms its total budget
	// for the new turn (lintTouchedFiles never carry over — a turn lints
	// exactly the files IT touched).
	cs.testwatchDrop()
	// Arm the turn-end lint pass's budget for this turn (piece 3): 0 means
	// "not configured by NewCortexSession" — runTurnLint falls back to the
	// default 60s, so hand-built test sessions stay functional.
	cs.turnLinter.budgetSec = cs.Config.toolLimits().TurnLintBudgetSec
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

	// Put the memory index (and, adjacent to it, the skills index) in the
	// fixed wire slot. Never mutate the stored system message: that would
	// invalidate the prompt cache from byte zero and append duplicate
	// indexes on every turn. Both notes are coder-only — subagentRequest
	// (study.go) builds a subagent's opening request from its own static
	// System + seed and never touches this slot, so neither index reaches
	// Study/Learn/Agent.
	//
	// The full memory section (memoryPromptSection) rides in this same
	// ephemeral slot when there's something to use it on (notes or demoted
	// turns) — again never the stored system message, which must stay
	// byte-stable for the whole session so the prompt cache survives.
	// memorySectionFor decides per turn from the memory index alone: the
	// skills index below must not count as a reason to include it. The
	// outline-present condition mirrors the outline-block one above (a
	// folded digest with live @session citations counts too, even when
	// context_evict has emptied the live entries). The section is built-in
	// guidance: it rides only when the base prompt is the built-in one
	// (prompt.file replaces the base and owns its own memory guidance).
	memNote := cs.memoryIndexNote()
	outlinePresent := len(cs.outline) > 0 || cs.outlineFolded != ""
	builtinBase := promptBase == SystemPrompt
	if section := memorySectionFor(memNote, outlinePresent, builtinBase); section != "" {
		memNote = section + "\n\n" + memNote
	}
	note := memNote
	if skillsNote := cs.skillsIndexNote(); skillsNote != "" {
		if note != "" {
			note += "\n\n"
		}
		note += skillsNote
	}
	cs.Request.EphemeralSystem = note
	if note != "" {
		cs.injections++
		cs.injectedChars += len(note)
	}

	maxTok := cs.Request.MaxTokens
	if maxTok <= 0 {
		maxTok = codeMaxOutputTokens
	}
	maxIter := cs.Config.maxToolIterations()
	if maxIterOverride > 0 {
		maxIter = maxIterOverride
	}
	ts := Toolset{Tools: cs.Request.Tools, Dispatch: cs.coderDispatcher(), BeforeBatch: cs.coderBeforeBatch, Finalize: finalize}
	// Issue #141: the model must account for test removals it made. The
	// receipt is computed at the clean-finalize point (runLoop calls
	// ts.FinalizeHook exactly when the model answers with no tool calls,
	// after every tool call has run) — at that moment the test-file
	// before-side is already settled, so the note names precisely what the
	// turn removed and the model's final answer addresses it. An empty
	// receipt (no test file touched, or nothing test-relevant lost) returns
	// "" and the answer is left untouched.
	// Issue #129 piece 3: the SAME finalize hook is the turn-end lint pass's
	// delivery (turn_lint.go): the pass runs the project's lint once over
	// the turn's distinct touched files ("all" mode + trusted only), and a
	// non-empty receipt returns the note the model's final answer accounts
	// for — the findings ride the engine's tools-withheld round, appended to
	// the turn's answer, exactly like the test-loss receipt. The two facts
	// join when both are present; either alone gets its own framing; both
	// empty (the common case) leave the answer untouched with no extra
	// round.
	ts.FinalizeHook = func() string {
		testNote := cs.testwatchFinalizeNote()
		lintNote := cs.turnLintAtFinalize(ctx)
		switch {
		case testNote == "":
			return lintNote
		case lintNote == "":
			return testNote
		default:
			return testNote + "\n\n" + lintNote
		}
	}
	bounds := Bounds{MaxTokens: maxTok, MaxIter: maxIter, TokenBudget: tokenBudget, EscalateEffort: cs.Config.effortEscalationEnabled()}

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
	// Issue #149 receipt: when the engine's natural-finish off-retry recovers an
	// empty reply, persist the fallback (model + this role) to the project-scope
	// recovery journal — the role code is fixed here (the coder turn), so the
	// closure is the composition root's, not the engine's.
	ts.OnReasoningFallback = func(stats loopStats) {
		cs.appendReasoningFallback(roleCode, journal.ReasoningFallbackPathNatural, cs.Request.Model, stats.StopReason, stats.ReasoningFallbackOutcome, stats.MaxTokensClamped, stats.SalvagedUnclamped)
		cs.transcriptNote(reasoningFallbackNote())
	}

	content, stats, err := runLoop(ctx, cs.healingSender(roleCode, cs.coderSender()), cs.Request, ts, bounds, progress, cs.Append, onStatusUpdate)
	cs.Request.EphemeralSystem = ""
	cs.turns++
	cs.tokensIn += stats.InputTokens
	cs.tokensOut += stats.OutputTokens
	cs.reasoningTokens += stats.ReasoningTokens
	cs.costUSD += stats.Cost
	cs.LastPromptTokens = stats.LastPromptTokens
	cs.LastCachedTokens = stats.LastCachedTokens

	// Issue #141: the "tests changed" receipt is surfaced on the RESULT
	// (not just the journal) so a caller — REPL, headless `cortex turn`, a
	// self-dev driver — can print it to a human. Compute it here, after
	// runLoop has settled every tool call, so the before/after is final;
	// captureTurn re-derives it (cheap, idempotent) for the journal record.
	// Issue #154: the "leftover debug" receipt is computed the same way and
	// surfaced the same way — TurnResult.DebugReceipt.
	// Issue #129 piece 3: the "lint: …" receipt was already computed at the
	// clean-finalize point (turnLintAtFinalize stored it on the session —
	// the finalize hook is where the pass runs, the moment every touched
	// file is settled); on the error/interrupt path (no finalize happened)
	// cs.lintReceipt is empty and the pass is simply skipped. Turn it into
	// the result the same way as the other receipts.
	lintReceipt := cs.lintReceipt
	testReceipt := cs.testwatchTestsReceipt()
	debugReceipt := cs.testwatchDebugReceipt()

	if err != nil {
		return TurnResult{Interrupted: errors.Is(err, context.Canceled), StopReason: stats.StopReason, TestReceipt: testReceipt, DebugReceipt: debugReceipt, LintReceipt: lintReceipt}, err
	}

	turnMsgs := cs.Request.Messages[turnStart:]
	cs.captureTurn(input, turnMsgs)

	return TurnResult{Reply: content, StopReason: stats.StopReason, TestReceipt: testReceipt, DebugReceipt: debugReceipt, LintReceipt: lintReceipt}, nil
}
