package main

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/dereksantos/cortex/internal/agent"
	"github.com/dereksantos/cortex/internal/journal"
	"github.com/dereksantos/cortex/internal/tools"
	"github.com/dereksantos/cortex/pkg/llm"
)

// loop.go is THE agent engine: one tool-iteration loop (`runLoop`) plus the two
// small seams every caller injects — a `Sender` (one model round-trip) and an
// `AgentDispatcher` (one tool call → observation). The coder turn and every
// subagent (study, …) run on this one function; the only variation is the seams
// they pass. See docs/engine-unification.md.
//
// Both callers run on this one engine: the coder turn (`Turn`) and the study
// subagent (`RunSubagent`, study.go). The old second loop (the navigator) is
// gone. The tool-call vocabulary lives in internal/agent; the engine itself
// stays in package main (it composes the session-built seams).

// Sender performs one model round-trip — the seam that makes runLoop testable
// and lets the coder stream while a subagent blocks. streamed reports whether
// the sender already echoed the assistant prose to the terminal (the streaming
// REPL path), so a blocking/quiet sender and the tests return false. A
// SenderFunc fake drives the real loop with zero network.
type Sender interface {
	Send(ctx context.Context, req *AgentRequest) (res *AgentResponse, streamed bool, err error)
}

// SenderFunc adapts a function to the Sender interface (the func-satisfies-one-
// method idiom on the injected seam, not on the engine).
type SenderFunc func(context.Context, *AgentRequest) (*AgentResponse, bool, error)

// Send implements Sender.
func (f SenderFunc) Send(ctx context.Context, req *AgentRequest) (*AgentResponse, bool, error) {
	return f(ctx, req)
}

// AgentDispatcher executes one tool call → observation (result or brief error).
// The impl bakes in the allowlist + any per-agent transforms (e.g. study's
// targeted read), so the engine never branches on "am I a subagent."
type AgentDispatcher interface {
	Dispatch(ctx context.Context, call ToolCall) string
}

// DispatchFunc adapts a function to the AgentDispatcher interface.
type DispatchFunc func(context.Context, ToolCall) string

// Dispatch implements AgentDispatcher.
func (f DispatchFunc) Dispatch(ctx context.Context, call ToolCall) string { return f(ctx, call) }

// Toolset is what the engine advertises to the model plus how it runs the calls.
// BeforeBatch is an optional per-batch display hook (the coder prints a blank
// line separating prose from its tool actions); nil for subagents and tests.
// AfterToolResult is an optional callback invoked after each tool result is
// appended, allowing the caller to update display with current context state.
// OnReasoningFallback is an optional receipt hook: when a natural-finish
// empty reply is recovered by the issue #149 one-shot reasoning-off retry
// (salvageEmptyReasoningRetry), the engine calls it (if non-nil) with the
// recovered run's stats so the caller can persist the fallback's journal
// receipt (cmd/cortex/recovery_journal.go). nil for subagents and tests that
// don't care about the receipt.
type Toolset struct {
	Tools           []Tool
	Dispatch        AgentDispatcher
	BeforeBatch     func()
	AfterToolResult func()
	// BeforeSend, when non-nil, is called once per iteration of the main
	// tool-call loop, immediately before send.Send — the seam where the
	// in-turn demotion policy (issue #171) shrinks this turn's accumulated
	// tool results before the next request is built. The hook receives the
	// request about to be sent and may mutate it (the coder wires it to swap
	// over-budget tool-result messages for recall-citable stubs); it must NOT
	// send or otherwise perform a model round-trip. nil = today's behavior,
	// byte for byte. The finalize and salvage sends (finalizeLoop and the
	// salvage/reasoning-fallback re-asks) are deliberately NOT wired — the
	// turn has already answered or is being recovered, and there the wire
	// already carries the demoted stubs from the main loop's hook.
	BeforeSend func(*AgentRequest)
	// SpliceImages, when non-nil, is called on every tool-result message
	// just before it is appended (issue #217): an image observation's
	// pending attachment (tools.TakeImageObservation) moves onto the
	// message as wire Parts and to the session's side-car, but only while
	// the vision verdict (the second argument) holds. nil = image parts
	// never splice (subagents, tests) — the tool result stays the plain
	// marker/observation string.
	SpliceImages func(msg *Message)
	// WriteImageSideCar is the append-time half of SpliceImages (#217):
	// called right after it with the index the tool-result message is
	// about to be appended at, so the image's on-disk side-car lands under
	// the same citation-space index recall resolves. nil skips the write.
	WriteImageSideCar func(msg *Message, abs int)
	// Finalize selects the forced-finalize closing (see FinalizeStyle). Zero
	// value = FinalizeSubagent, so subagent callers need no change.
	Finalize FinalizeStyle
	// OnReasoningFallback receives the recovered run's stats when the
	// issue #149 natural-finish off-retry recovers an empty reply; nil
	// skips the receipt (see the type doc).
	OnReasoningFallback func(stats loopStats)
	// FinalizeHook, when non-nil, is consulted exactly once — at the moment
	// the model answers with NO tool calls (the clean-finalize point, or the
	// issue #149 off-retry recovering that answer as prose), after every tool
	// call this turn has already run. It returns a harness note to hand the
	// model in one more, tools-withheld finalize round so the model's FINAL
	// answer addresses it (issue #141's "tests changed" receipt, via
	// turn.go; see finalizeHookRound). An empty return (or a nil hook) leaves
	// the answer untouched. Only the coder turn wires it; subagents and tests
	// leave it nil.
	FinalizeHook func() string
	// OnForcedFinalize, when non-nil, is consulted exactly once — at the
	// forced-finalize exit (the run stopped on a bound: max-iter,
	// token-budget, read-budget, no-progress, stuck, or an error it
	// recovered from) — AFTER finalizeLoop has produced the turn's answer
	// from the honesty-core prompt. It receives the run's stats as they
	// stand at that moment and returns a harness note; when the note is
	// non-empty the engine runs one more, tools-withheld finalize round
	// (forcedFinalizeHookRound) whose reply is APPENDED to the answer, so
	// the model's final words account for facts the turn never reached the
	// clean-finalize hook for: a turn cut off at the tool-call cap (issue
	// #161) never answers with no tool calls, so FinalizeHook (the
	// testwatch #141/#154 and turn-end lint #129 receipts) never fires for
	// it, and the forced answer otherwise hands in unfinished work —
	// leftover debug prints, scratch files, unfixed lint findings —
	// unreported. An empty return (or a nil hook) leaves the answer
	// untouched, byte for byte. Only the coder turn wires it; subagents
	// and tests leave it nil.
	OnForcedFinalize func(stats loopStats) string
}

// Bounds are the independent ceilings; whichever trips first forces finalize.
// Defined in internal/agent so a Subagent profile (internal/tools) can carry one;
// aliased here so the engine reads unchanged.
type Bounds = agent.Bounds

// Progress is an optional per-tool-call breadcrumb sink. The REPL wires it so a
// blocking subagent (study) still shows what it's doing; headless and tests
// pass nil — which is today's behavior.
type Progress func(line string)

// loopStats is the engine's always-on usage + per-tool accounting for one run:
// the caller folds the token sums into session totals, and the study eval reads
// the same shape (docs/study-subagent.md §5). StopReason says WHICH bound bound.
type loopStats struct {
	InputTokens      int
	OutputTokens     int
	Cost             float64
	LastPromptTokens int  // most recent prompt_tokens (the live context gauge)
	LastOutputTokens int  // most recent completion_tokens (the live context gauge's "out" side)
	LastCachedTokens int  // most recent provider-reported cached prompt tokens (0 if unreported)
	PeakOutputTokens int  // max completion tokens on any single request
	MaxTokensClamped bool // any request hit Bounds.MaxTokens (runaway tripwire)
	Salvaged         bool // an empty clamped finish was recovered by one terse re-ask
	// SalvagedUnclamped: recovered from empty WITHOUT MaxTokensClamped — the
	// model just stopped with nothing, not a budget-burn spiral. Set by either
	// salvage (the re-ask or the observation fallback). Both share StopReason
	// "salvaged-finalize"; this is the field that tells them apart.
	SalvagedUnclamped bool
	// ReasoningFallback is set when an empty finish was recovered by the
	// one-shot reasoning-off retry (issue #149: a reasoning model that spent
	// its whole turn deliberating and came back with no content and no tool
	// calls). Distinguished from Salvaged so a model that keeps needing it
	// shows up in telemetry separately from the prompt-based salvages.
	// Run-scoped: set only when a recovery actually recovers a round, never
	// cleared once set — a failed retry in a later round does not undo an
	// earlier genuine recovery of the same run (the per-round receipt is
	// what stays round-accurate).
	ReasoningFallback bool
	// ReasoningFallbackOutcome names HOW the #149 off-retry recovered the
	// round: journal.OutcomeAnswer (it answered with prose, returned as the
	// turn's answer) or journal.OutcomeToolCalls (it answered with tool
	// calls, dispatched like a normal round). Only meaningful when
	// ReasoningFallback is set; the journal receipt and telemetry both read
	// it so the two recoveries can be told apart.
	ReasoningFallbackOutcome string
	Iterations               int    // model rounds consumed
	StopReason               string // clean-finalize|salvaged-finalize|empty-finalize|max-iter|read-budget|no-progress|deadline|error
	FinalizeForced           bool   // answered because a bound dragged finalize out

	// LastError is the provider error the run recovered from when
	// StopReason == "error-recovered" (a mid-loop send failed after progress,
	// and the run finalized from what it had). nil on every other outcome —
	// the unrecovered "error" stop returns the error itself to the caller.
	// Carried so the caller (turn.go) can record it instead of the failure
	// vanishing behind the finalize answer (issue #117): turn() journals it
	// (a model.recovered_error entry — the recovered kind, distinct from the
	// unrecovered model.failure the healing ladder journals) + logs it
	// (cortex.log), and TurnResult.LastError lets the CLI/REPL print the
	// one-line "backend error: <status> <message>" notice.
	LastError error

	Outlines  int
	Greps     int
	Reads     int
	ToolErrs  int // observations that came back as a brief "Error: …"
	ReadBytes int // accumulated tool output (the bounded-ness axis)

	// ReasoningTokens sums completion_tokens_details.reasoning_tokens across
	// every request in the run (0 when the backend never reports it).
	ReasoningTokens int
	// DeliberationClamped is set when any request in the run hit the
	// max-tokens-clamp deliberation signature (docs/thinking-models.md §4:
	// finish_reason "length" and either the reasoning/completion split shows
	// reasoning dominated, or — unreported — empty content with a non-empty
	// reasoning trace). Drives the salvage-effort-off mutation
	// (salvageEmptyFinalize/salvageClampedFinalize) and is kept for eval
	// attribution.
	DeliberationClamped bool
}

var errNoChoices = errors.New("no choices in model response")

// maxRepeatedToolCalls bounds how long the no-progress guard (issue #132) lets
// a model re-issue the same tool-call batch before intervening. A weak model
// can re-issue the same call until it burns the turn (observed: 68 identical
// greps, 2026-06-14). A batch is "no progress" when it is byte-identical to
// the previous one AND the previous batch's observation carried no new
// information for that call: an error (an identical failing call is no
// progress from the first repeat) or unchanged from the batch before that
// (the re-read yields the same thing). The guard compares one observation per
// BATCH — the round's dispatch observations joined with \x00 — collected
// inside the dispatch loop (the transcript cannot supply it: a repeated batch
// reuses the same tool-call IDs, and the stuck hint / cap warning appended
// after the tool results would shift a transcript tail-slice); the
// observation belongs to the same batch the signature describes, never to a
// sibling call or another round.
// On the penultimate repeat the engine nudges; on the next it finalizes.
const maxRepeatedToolCalls = 3

const (
	stuckThreshold  = 2   // same error-class seen this many times → inject a redirect
	stuckJitterTemp = 0.5 // small perturbation applied only to the post-redirect re-sample
)

var digitRe = regexp.MustCompile(`\d+`)
var mostFrequentCandidateRe = regexp.MustCompile(`(?m)^most_frequent_candidate:\s*(\S+)`)

// errorClass reduces a tool error observation to a stable class — the leading
// "Error: …" line with numbers neutralized — so the SAME failure recurring is
// detectable even when read_file calls interleave (which reset the byte-identical
// no-progress guard). Returns "" for a non-error observation.
func errorClass(obs string) string {
	if !strings.HasPrefix(obs, "Error:") {
		return ""
	}
	line := obs
	if nl := strings.IndexByte(line, '\n'); nl >= 0 {
		line = line[:nl]
	}
	return digitRe.ReplaceAllString(line, "#")
}

// stuckHint maps a recurring tool-error class to an ACTIONABLE redirect — the
// off-ramp a weak model can't infer from the raw error — with a generic wrap-up
// fallback. The known cases are the ones observed thrashing the live loop.
func stuckHint(class string) string {
	switch {
	case strings.Contains(class, "identical; nothing to change"):
		return "Harness note: that edit is a no-op — old_string already equals new_string, so the change is most likely ALREADY APPLIED. Stop editing this region: read_file to confirm, then report what you changed. Do not re-issue the edit."
	case strings.Contains(class, "old_string") && strings.Contains(class, "not found"):
		return "Harness note: edit_file can't find your old_string — it must match the file EXACTLY. Re-read the span and copy the literal current text, or use a larger unique snippet."
	case strings.Contains(class, "does not exist"):
		// Issue #142: the model keeps hitting paths that don't exist — the
		// classic path-guessing loop (foreign absolute paths, made-up names).
		// Each read_file/grep/outline already returns an oriented not-found
		// error pointing at outline/grep, but a model repeating it is the
		// exact thrash this detector exists for; the redirect names the off-
		// ramp: stop guessing, orient from the workspace root, then target
		// real paths.
		return "Harness note: that path does not exist — do not guess paths. Outline or grep the workspace root (or an existing parent) to see what is actually there, then work only from paths those tools returned. Do not re-issue the same path."
	default:
		return "Harness note: the same tool error keeps recurring and you are not making progress. Stop and report what you did and what is blocking you."
	}
}

// noProgressNudge is injected one repeat short of the cap so a stuck model can
// change course before the guard breaks the loop. Engine-level (every caller),
// not a main-loop special case.
const noProgressNudge = "Harness note: that tool call repeated the previous one without new information — the same result or the same error keeps coming back. Repeating it will not yield new information — re-read the span or state, try a different command or approach, or stop and report what you've found."

// toolCapWarningRounds is the distance-to-the-cap at which runLoop starts
// warning the model (issue #161): on the first tool round where the number of
// rounds the model can still spend on tool calls — the in-flight batch
// (round i of the loop, the (i+1)-th round) plus the ones after it
// (remaining = MaxIter - i) — falls to this threshold, one "Harness note"
// tells the turn how many tool-call rounds it still gets and asks it to wrap up
// or clean up (remove debug prints, delete scratch files, leave tests passing
// or failing-honest) before the cap forces a tools-withheld finalize. Without
// the warning, the cap arrives silently mid-exploration and the forced answer
// hands in unfinished work — the incident #161 names. On a 100-round cap the
// loop runs i = 0..99, so the warning lands at i = 90 (remaining = 100-90 = 10)
// saying "10 remaining", and the rounds left (90..99) are exactly ten — a model
// that keeps calling tools gets precisely the rounds the count promised, never
// cut off before. A run with MaxIter at or below the threshold gets no warning
// (it would be every round); a turn that ends with a clean finalize before the
// threshold never crosses it; it fires at most once per turn.
const toolCapWarningRounds = 10

// toolCapWarning is the cap-approaching note itself. The remaining count is
// the tool-call rounds still available INCLUDING the one whose tool calls are
// being dispatched when the warning fires (remaining = MaxIter - i: round i is
// the (i+1)-th round of a loop that runs i = 0 .. MaxIter-1, so it plus the
// rounds after it are MaxIter - i). On a 100-round cap the warning names
// "10 remaining" at i=90 — this batch plus the next nine — and a model that
// keeps calling tools gets exactly those ten rounds: the cap, if it doesn't
// wrap up, lands at round 100 exactly as the count promised.
func toolCapWarning(remaining int) string {
	return "Harness note: you have " + strconv.Itoa(remaining) + " tool-call round(s) left before the per-turn tool-call limit forces a final answer. Wrap up: finish the work you are on, or clean up what you leave behind — remove debug prints you added, delete scratch files you created, and make sure the project's tests pass (or state plainly which ones fail and why) — so your final answer is not handed in mid-exploration with unfinished work behind it."
}

// FinalizeStyle selects how a forced finalize should END. The honesty core is
// shared; only the closing differs, because the callers differ in who is
// listening: an interactive turn has a next turn (the user can say "continue"),
// a subagent has no interlocutor and must hand its caller a complete digest.
type FinalizeStyle int

const (
	// FinalizeSubagent (the zero value — every existing Toolset literal) ends
	// with an open-items list, never a question: nobody is there to answer it.
	FinalizeSubagent FinalizeStyle = iota
	// FinalizeInteractive ends by offering to continue on the open items —
	// the session persists, so the offer is actionable.
	FinalizeInteractive
)

// finalizeCause maps a stop reason to the phrase the finalize prompt leads
// with. One generic "you've reached the limit" hid five different causes
// (the 2026-07-27 launch-assessment transcript read a mid-run backend
// failure as a budget problem); naming the cause keeps the transcript — and
// the model's framing of its own answer — diagnosable.
func finalizeCause(stop string) string {
	switch stop {
	case "max-iter":
		return "the tool-call limit for this turn"
	case "token-budget":
		return "the token budget for this run"
	case "read-budget":
		return "the read budget for this run"
	case "no-progress", "stuck":
		return "repeated tool calls that were not making progress"
	case "error-recovered":
		return "a backend error that interrupted the run"
	default:
		return "a limit"
	}
}

// finalizePromptFor asks for the answer with tools withheld when a bound forced
// the loop to stop, so a budget-exhausted run still produces a grounded digest
// rather than nothing. The old single prompt's "do not say you need more
// exploration" was engineered against hedge-refusal (a weak model emitting "I'd
// need to look further" as the entire answer) but overcorrected into forced
// confidence: an under-explored run invented findings to fill the gaps (the
// 2026-07-27 launch-assessment confabulation). This version holds both failure
// modes apart: report what was verified, name what wasn't — never guess.
func finalizePromptFor(stop string, style FinalizeStyle) string {
	p := "Your exploration was stopped by " + finalizeCause(stop) + ". Stop calling tools and answer now. " +
		"Report only what you verified in the tool output above, naming the concrete files, symbols, and relationships you actually saw. " +
		"Anything the goal needs that you did not verify, state plainly as unverified — do not fill gaps with plausible guesses, and do not present an unverified claim as a finding."
	switch style {
	case FinalizeInteractive:
		return p + " Close by listing the open items and asking whether to continue investigating them. Be concise."
	default:
		return p + " Close with a short list of the open items still unverified. Be concise."
	}
}

// reFinalizePrompt is the salvage ask when a finish came back EMPTY. That can
// be a reasoning model that spent its whole completion budget deliberating
// (the max-tokens clamp), or a model that simply stopped with nothing (a
// hybrid-reasoning answer landing in a channel the blocking path never
// parses). The wording is cause-neutral because either can be true, and it
// carries the same honesty clause as rewriteClampedPrompt: an empty finish on
// a coding turn often means the work is unfinished, and "state the answer
// now" without that clause invites the model to narrate the goal as done (the
// 2026-08-07 polyglot confabulation).
const reFinalizePrompt = "Your previous reply was empty. Without calling tools, give your final answer now in at most five sentences. Describe only what you actually completed — if any of the work is unfinished, say so plainly rather than presenting it as done."

// rewriteClampedPrompt is the salvage ask when a model DID answer, but only by
// running into the completion ceiling. That answer is usually verbose and fails
// the eval's runaway tripwire; ask for a compact rewrite, then mark the run
// salvaged if the rewrite succeeds.
//
// The honesty clause is not decoration. This prompt was written for the study
// path, where the clamped answer is a digest and brevity is the only goal. On a
// coding turn the same ask is actively dangerous: a model cut off while writing
// an implementation into chat satisfies "give a concise final answer to the
// goal" by narrating the goal as accomplished. The 2026-08-07 polyglot run
// caught exactly that — two exercises with zero write calls whose salvaged
// answer opened "I have implemented the Song, Verses, and Verse functions",
// scoring as a clean finalize over an untouched file. finalizePromptFor already
// holds this line ("never guess"); the clamped branch simply never inherited it.
//
// Deliberately intent-NEUTRAL: it constrains what may be claimed, never what
// must be done. An instruction to go write the file would fire on discussion
// turns too, where producing no edit is the correct outcome, and push unwanted
// changes into the tree — a worse failure than the one being fixed. Telling the
// truth about what happened is safe on every turn; telling the model to edit is
// not.
const rewriteClampedPrompt = "Your previous reply hit the completion limit. Rewrite it now as a concise final answer in at most five sentences. Describe only what you actually completed — never present work you did not finish as done. Keep only the facts needed to answer the goal; do not add tool calls or extra reasoning."

// runLoop is THE engine. It iterates send → dispatch → re-send until the model
// answers with no tool calls (clean finalize) or a bound trips, then finalizes
// with tools withheld. The variation between callers is the Sender + Toolset
// (+ Progress); the loop core — XML recovery, no-progress guard, usage
// accounting, ctx-cancel, finalize — is identical for everyone.
//
// appendMsg records each message: the coder passes cs.Append (grows the live
// history and writes the transcript); a subagent passes a plain slice append on
// its own request. req is the caller's request, re-sent (grown) each round.
// onStatusUpdate is an optional callback invoked after each iteration to update
// the display with the current context usage (for interactive REPL).
func runLoop(ctx context.Context, send Sender, req *AgentRequest, ts Toolset, b Bounds, p Progress, appendMsg func(Message), onStatusUpdate func(lastPromptTokens, lastOutputTokens, maxTokens int)) (string, loopStats, error) {
	var stats loopStats
	req.Tools = ts.Tools
	if b.MaxTokens > 0 {
		req.MaxTokens = b.MaxTokens
	}

	var repeats int             // consecutive batches identical to prevBatchSig, including current
	errSeen := map[string]int{} // error-class → times seen this turn (survives interleaved reads)
	jitter := false             // perturb temperature on the next send (set when stuck)
	baseTemp := req.Temperature // restore after a one-shot jitter
	stop := ""
	var prevBatchSig, prevBatchObs string // the previous round's batch: signature + joined observations (no-progress guard)
	lastObservation := ""                 // the last round's joined batch observation (also feeds salvageObservationFinalize)
	capWarned := false                    // the cap-approaching note fired (at most once per turn, issue #161)
	for i := 0; i < b.MaxIter; i++ {
		stats.Iterations = i + 1
		var restoreEffort func()
		if jitter {
			req.Temperature = stuckJitterTemp // perturb only the post-redirect re-sample
			if b.EscalateEffort {
				// P5c: escalate effort one tier alongside the temperature
				// jitter for this single post-redirect re-sample, opt-in via
				// tools.enable_effort_escalation (docs/thinking-models.md §5c).
				restoreEffort = escalateEffortOnce(req)
			}
			jitter = false
		}
		// In-turn demotion seam (issue #171): before the request is sent this
		// round, give the harness a chance to shrink the turn's accumulated tool
		// results. nil (subagents, tests, every non-coder caller) skips this and
		// the request goes out byte-for-byte as today. The finalize and salvage
		// sends are deliberately not wired — see the BeforeSend field doc.
		if ts.BeforeSend != nil {
			ts.BeforeSend(req)
		}
		res, _, err := send.Send(ctx, req)
		req.Temperature = baseTemp // one-shot: restore so the rest of the turn stays deterministic
		if restoreEffort != nil {
			restoreEffort()
		}
		if err != nil {
			// A mid-loop model-call failure (transient backend error, or a proxy
			// rejecting a tool-call round's grammar) shouldn't lose a run that has
			// already gathered context: if we made progress and the ctx is still
			// live, finalize from what we have (tools withheld, so the failing round
			// is sidestepped). Abort only on the first round or a real cancellation.
			if i > 0 && ctx.Err() == nil {
				stop = "error-recovered"
				// Carry the failing send's error to the caller: the run still
				// finalizes, so the error otherwise vanishes behind the
				// finalize answer — the caller logs it and prints it (issue #117).
				stats.LastError = err
				break
			}
			stats.StopReason = "error"
			return "", stats, err
		}
		if res == nil || len(res.Choices) == 0 {
			stats.StopReason = "error"
			return "", stats, errNoChoices
		}
		accountUsage(&stats, res, req.MaxTokens)

		// Update display with current context usage (for interactive REPL)
		if onStatusUpdate != nil {
			onStatusUpdate(stats.LastPromptTokens, stats.LastOutputTokens, req.MaxTokens)
		}

		// D11's per-loop-firing token budget (0 = unbounded for every other
		// caller): stop the instant cumulative spend crosses it, before this
		// round's response is even added to the transcript or its tool calls
		// dispatched — a token-hungry runaway is capped by SPEND, not just
		// round count (see Bounds.TokenBudget).
		if b.TokenBudget > 0 && stats.InputTokens+stats.OutputTokens >= b.TokenBudget {
			stop = "token-budget"
			break
		}

		msg := res.Choices[0].Message
		// Recover tool calls the model wrote into its text instead of
		// emitting as structured tool_calls (Qwen XML or Hermes
		// <function_calls> tags), so a call isn't silently lost (empty
		// tool_calls reads as a final answer). Issue #230: a recovered
		// call that is BYTE-IDENTICAL to the batch already in flight is
		// not a new action — it's the model re-emitting the call it
		// already asked for as text (the recorded 082119.jsonl:281 case:
		// a <tool_call><function=grep> block in the
		// assistant content of the round that was already executing grep).
		// Re-dispatching it would re-run the same tool; the correct
		// treatment is to treat the round as a natural finish with the
		// markup stripped, so the sanitizer (below) and the no-progress
		// guard decide what happens next — the call is neither dropped
		// nor re-run. A genuinely NEW call (different signature) is
		// recovered and dispatched as a tool round (the pre-#230
		// behavior).
		if calls := parseToolCallsFromContent(msg.Content); len(calls) > 0 && len(msg.ToolCalls) == 0 {
			if toolCallSignature(calls) != prevBatchSig {
				msg.ToolCalls = calls
				msg.Content = stripToolMarkup(msg.Content)
			}
			// else: byte-identical to the in-flight batch — leave the
			// message as a natural finish (no tool calls); the sanitizer
			// below strips the markup and the clean-finalize path
			// returns the prose.
		}
		// This round's assistant message is appended at the dispatch point
		// below (before any tool results) — UNLESS it's an empty natural finish
		// that the issue #149 off-retry may replace: in that case only the
		// message that survives the retry decision is appended, so the wire and
		// the resumable transcript (cs.Append writes both) never carry the
		// dropped empty turn (which would read as "assistant(empty),
		// assistant(…)", a shape providers reject when the retry returns tool
		// calls). The retry's prose result is appended right before it returns.

		// No tool calls → the model answered. That prose IS the result — unless
		// it's EMPTY, salvaged with one terse re-ask instead of returning
		// nothing. Not gated on MaxTokensClamped: a hybrid-reasoning model can
		// also land here empty without hitting the clamp (docs/thinking-models.md's
		// blocking-path gap). (The issue #149 off-retry may REPLACE msg with a
		// tool-carrying message below; the empty-finish gate is checked BEFORE
		// that happens, so the fall-through dispatch runs in the same round.)
		finishedNaturally := len(msg.ToolCalls) == 0
		// The issue #149 off-retry fires only on a NATURAL empty finish — no
		// tool calls AND no content — from a role whose reasoning is ON
		// (req.Effort.Level != EffortOff; EffortUnset is treated as on). It
		// must NOT fire on a tool round that happens to return empty content
		// with tool calls (a common Qwen shape): that is an ordinary tool round
		// that the empty-finish recovery was never written for, and retrying it
		// would drop the tool call. finishedNaturally (len(ToolCalls)==0) is
		// checked alongside the empty content so only a bare empty answer
		// qualifies.
		retryableEmptyFinish := finishedNaturally && strings.TrimSpace(msg.Content) == "" && req.Effort.Level != llm.EffortOff
		if finishedNaturally {
			// Issue #230: sanitize the natural finish at the SOURCE — strip
			// leaked tool-call markup before the salvage chains and the
			// receipt/clamped salvage decide what to do with the answer.
			// A markup-only finish is treated as empty (the empty-salvage
			// chain below is its single bounded repair); a truncated finish
			// is rewritten by the clamped salvage below.
			answer := sanitizeFinalAnswer(msg.Content)
			if retryableEmptyFinish {
				// Issue #149: an empty finish from a role whose reasoning is on
				// (a Qwen-style model that spent its whole turn deliberating) is
				// recovered by re-sending the SAME request once with reasoning
				// pinned off — before treating the round as an empty finish and
				// falling through to the prompt-based salvages. Recovery, not
				// policy: the configured effort default is never changed, and
				// roles that already run with reasoning off never trigger it.
				// The empty assistant message is NOT appended yet: the retry
				// re-sends the request it came back empty WITHOUT the empty turn
				// (a trailing empty turn would not be the same request, and
				// providers reject the transcript shape it would produce when the
				// retry returns tool calls), so only the message that survives the
				// decision below is appended — the retry's (replacing the empty
				// one) or the original empty one on fall-through.
				// The receipt is round-local: it fires inside the branch that
				// actually recovered THIS round (see below), never at a gate that
				// reads run-scoped stats — a recovery in a later round must not
				// produce a second, false receipt, and a failed retry records
				// nothing. stats.ReasoningFallback itself is run-scoped and is
				// never cleared once set, so an earlier genuine recovery still
				// stands for the run's stats no matter how later rounds go.
				retry := salvageEmptyReasoningRetry(ctx, send, req, &stats)
				if retry != nil && len(retry.ToolCalls) > 0 {
					// The retry returned TOOL CALLS, not an answer: it is an
					// ordinary tool round. It replaces the dropped empty message;
					// the append happens at the round's dispatch point below (the
					// tool-round appendMsg), so the model's requested actions are
					// neither dropped nor left dangling without results. The
					// fallback fired and recovered the round with tool calls, so it
					// is recorded (receipt + flag) just like the prose recovery.
					// The receipt's StopReason is attributed "tool-round" (the
					// recovery's own attribution, per internal/journal/recovery.go)
					// — the run's StopReason is left to the round the loop actually
					// ends in, because the calls are dispatched, not answered.
					msg = *retry
					finishedNaturally = false
					stats.ReasoningFallback = true
					stats.ReasoningFallbackOutcome = journal.OutcomeToolCalls
					salvagedStop := stats.StopReason // receipt attribution: the recovery's own label
					stats.StopReason = "tool-round"
					if ts.OnReasoningFallback != nil {
						ts.OnReasoningFallback(stats)
					}
					stats.StopReason = salvagedStop // the run's stop reason stays with the round it ends in
				} else if a2 := retryText(retry); a2 != "" {
					stats.Salvaged = true
					stats.ReasoningFallback = true
					stats.ReasoningFallbackOutcome = journal.OutcomeAnswer
					stats.SalvagedUnclamped = !stats.MaxTokensClamped
					stats.StopReason = "salvaged-finalize"
					appendMsg(*retry)
					if ts.OnReasoningFallback != nil {
						ts.OnReasoningFallback(stats)
					}
					// Issue #141: the recovered prose IS the model's natural
					// answer (just produced with reasoning off), so it reaches
					// the same "tests changed" finalize round a clean finish
					// does — otherwise a reasoning-on model that needed the
					// #149 retry would never be told what its turn did to the
					// tests. Run AFTER the fallback receipt so that receipt's
					// stats describe the recovery alone. The round's reply is
					// appended to a2, never substituted for it.
					a2 = finalizeHookRound(ctx, send, req, ts, &stats, appendMsg, a2)
					req.Tools = ts.Tools
					return a2, stats, nil
				}
				// else: the retry also came back empty (or errored): fall through
				// to the prompt-based salvages with the ORIGINAL empty message
				// standing in for the round (appended once, below), so the
				// transcript mirrors the wire conversation — the empty message is
				// written once, not twice. No receipt: the retry recovered nothing.
			}
			if finishedNaturally {
				// Natural finish: append the original message so the transcript
				// mirrors the wire conversation (one empty message, not two),
				// then re-run the prompt-based salvage chain from it.
				appendMsg(msg)
				if answer == "" {
					wasClamped := stats.MaxTokensClamped
					if a2 := salvageEmptyFinalize(ctx, send, req, &stats, appendMsg); a2 != "" {
						stats.SalvagedUnclamped = !wasClamped
						req.Tools = ts.Tools // salvage withheld them; restore for the caller's reuse
						return a2, stats, nil
					}
					if a2 := salvageObservationFinalize(lastObservation, &stats); a2 != "" {
						stats.SalvagedUnclamped = !wasClamped
						req.Tools = ts.Tools
						return a2, stats, nil
					}
				}
				// Issue #141: hand the model the turn's "tests changed" receipt
				// in one more tools-withheld finalize round (finalizeHookRound);
				// its reply is APPENDED to the answer, never substituted. It
				// sits after the empty-finish salvages (which return their own
				// terse answers) and before the clamped salvage — the position
				// #145 gave it — so, as there, an empty answer nothing could
				// salvage still gets the receipt round, and only a turn whose
				// receipt round ALSO yields nothing ends as empty-finalize.
				answer = finalizeHookRound(ctx, send, req, ts, &stats, appendMsg, answer)
				if answer == "" {
					// Nothing salvageable: say so rather than passing an empty turn
					// off as a clean finish.
					stats.StopReason = "empty-finalize"
					req.Tools = ts.Tools
					return "", stats, nil
				}
				// The clamped salvage runs when the (sanitized) answer hit the
				// token ceiling OR looks cut off mid-sentence (issue #230: a
				// truncation that arrived unclamped by token count, e.g. the
				// recorded "Let me restate my complete final" case).
				if (stats.MaxTokensClamped || looksTruncated(answer)) && !stats.Salvaged {
					if a2 := salvageClampedFinalize(ctx, send, req, &stats, appendMsg); a2 != "" {
						req.Tools = ts.Tools
						return a2, stats, nil
					}
				}
				// A failed (clamped) salvage leaves the tools withheld; restore
				// them so the caller's long-lived request (cs.Request) keeps its
				// tools next turn.
				req.Tools = ts.Tools
				stats.StopReason = "clean-finalize"
				return answer, stats, nil
			}
		}

		// A natural finish always returns from inside the block above (clean,
		// salvaged, or empty-finalize). Reaching here means the model asked for
		// tool calls — either a normal tool round, or one the issue #149
		// off-retry recovered above (replacing the dropped empty message) — and
		// the assistant message must be on the wire before any tool results (the
		// API ordering invariant).
		appendMsg(msg)

		sig := toolCallSignature(msg.ToolCalls)
		// The guard compares BATCHES, not individual calls: the previous
		// round's signature (prevBatchSig) and the previous round's joined
		// observation (prevBatchObs) both belong to that round's batch, so the
		// observation judged is always the same call's — never a sibling call's
		// or another round's. (The transcript cannot supply the previous
		// observation: a repeated batch reuses the same tool-call IDs, so the
		// prior round's tool-result message — already appended by its dispatch
		// — would masquerade as this round's.)
		//
		// A batch that is NOT byte-identical to the previous one starts its own
		// streak fresh. Whether an identical batch is "no progress" is decided
		// at the END of the round, after dispatch, when this round's own
		// observation is known: an erroring or unchanged observation is no
		// progress (re-reading yields nothing new); a changed observation is
		// progress (the re-read yielded new information) and resets the streak.
		// Deciding post-dispatch is what lets a changed observation count:
		// pre-dispatch the guard only sees the previous observation and cannot
		// tell whether this round will re-yield it.
		if sig != prevBatchSig || len(msg.ToolCalls) == 0 {
			repeats = 1 // a new batch starts its own streak
		}
		if ts.BeforeBatch != nil {
			ts.BeforeBatch()
		}
		// hintClass carries a just-crossed stuck threshold so the off-ramp is injected
		// AFTER all tool results (the API requires tool results to follow the
		// assistant message before any user turn).
		hintClass := ""
		// One batch observation: this round's dispatch observations joined, compared
		// as a unit against the previous round's at the end of the round (the
		// no-progress guard, issue #132). Collected INSIDE the dispatch loop —
		// in the same order and with the same pairing as the tool-result
		// messages it appends — because the transcript cannot supply it: a
		// repeated batch reuses the same tool-call IDs, and the stuck hint or
		// the cap-approaching warning appended after the tool results (the API
		// requires them to follow) would shift any transcript tail-slice away
		// from this round's own results.
		batchObs := make([]string, 0, len(msg.ToolCalls))
		for _, call := range msg.ToolCalls {
			countTool(&stats, call.Function.Name)
			obs := ts.Dispatch.Dispatch(ctx, call)
			batchObs = append(batchObs, obs)
			// Stuck detector: count by ERROR CLASS, not byte-identical call, so a
			// recurring failure is caught even when the model interleaves read_file
			// calls (which slip past the no-progress guard). First crossing → an
			// actionable redirect; persisting → finalize instead of thrashing.
			if cls := errorClass(obs); cls != "" {
				stats.ToolErrs++
				errSeen[cls]++
				switch errSeen[cls] {
				case stuckThreshold:
					hintClass = cls
				case stuckThreshold + 2:
					stop = "stuck"
				}
			}
			stats.ReadBytes += len(obs)
			toolMsg := Message{Role: RoleTool, ToolCallID: call.ID, Content: obs}
			// Image input (#217): an image observation's attachment (the
			// data URI read_file recorded for this dispatch) rides the
			// tool-result message as wire Parts, and its bytes go to the
			// per-session side-car so recall can name them after the live
			// Parts are gone. The coder wires this; a text-only model never
			// got an attachment (the tool refused it) and the wire gate
			// backstops anything else.
			if ts.SpliceImages != nil {
				ts.SpliceImages(&toolMsg)
				// The side-car key is the index appendMsg is about to give
				// this message — for the coder (cs.Append) the request's
				// length BEFORE the append, which equals its transcript
				// position 1:1; measuring after would be one past it and
				// recall would look up an index no side-car was written
				// under (#217).
				sideCarIdx := len(req.Messages)
				appendMsg(toolMsg)
				// The append-time half of the side-car write (#217): with
				// appendMsg done, write under the index the message
				// actually landed at — the key recall's citations resolve.
				if ts.WriteImageSideCar != nil {
					ts.WriteImageSideCar(&toolMsg, sideCarIdx)
				}
			} else {
				appendMsg(toolMsg)
			}
			if ts.AfterToolResult != nil {
				ts.AfterToolResult()
			}
			if p != nil {
				p(progressLine(call))
			}
		}
		// Stop at the iteration boundary if the user interrupted — history is
		// valid and the model can pick up next turn.
		if err := ctx.Err(); err != nil {
			stats.StopReason = "deadline"
			return "", stats, err
		}
		// A recurring error just crossed the threshold: inject the off-ramp (after the
		// tool results) and perturb the next sample so the model doesn't snap back to
		// the same losing token sequence.
		if hintClass != "" {
			appendMsg(Message{Role: RoleUser, Content: stuckHint(hintClass)})
			jitter = true
		}
		// Cap-approaching warning (issue #161): the model asked for tool calls
		// with at most toolCapWarningRounds left INCLUDING the in-flight batch:
		// round i is the (i+1)-th round of the i = 0 .. MaxIter-1 loop, so the
		// rounds the model can still spend on tool calls — this batch plus the
		// ones after it — are remaining = MaxIter - i. Tell it the count and ask
		// it to wrap up or clean up (remove debug prints, delete scratch
		// files, leave tests honest) BEFORE the cap decides for it, so the
		// rounds it is told it still has are exactly the rounds it actually
		// gets (on a 100-round cap: at i=90 the model hears "10 remaining",
		// rounds 90..99 are exactly ten, and if it keeps asking the cap lands
		// on round 99, the last of them). Injected after the tool results, like
		// the stuck hint and the no-progress nudge: the API requires tool
		// results to follow the assistant message before any user turn, and the
		// model still gets this batch's output before hearing the warning. Once
		// per turn (capWarned); a MaxIter at or below the threshold never fires
		// it (it would be every round); a clean finalize before the threshold
		// never reaches this point.
		if !capWarned && b.MaxIter > toolCapWarningRounds {
			if remaining := b.MaxIter - i; remaining <= toolCapWarningRounds {
				appendMsg(Message{Role: RoleUser, Content: toolCapWarning(remaining)})
				capWarned = true
			}
		}
		lastObservation = strings.Join(batchObs, "\x00")
		// No-progress decision (issue #132), made HERE — after dispatch — where
		// this round's own observation (lastObservation) and the previous
		// round's (prevBatchObs) are both fresh. An identical batch is no
		// progress when its observation carried no new information: an error
		// (an identical failing call re-yields the same failure) or unchanged
		// from the previous round (the re-read yields the same thing). A
		// changed observation is progress: the re-read yielded new information,
		// so the streak starts over. prevBatchObs keeps its own copy of the
		// join: lastObservation is also read on the natural-finish path (the
		// empty-answer observation salvage) and the finalize, where it must be
		// the LAST round's value — so the decision reads prevBatchObs (the
		// previous round) against lastObservation (this round), then the trail
		// advances below.
		if sig == prevBatchSig && len(msg.ToolCalls) > 0 {
			switch {
			case errorClass(lastObservation) != "":
				repeats++ // an identical failing call is no progress from the first repeat
			case lastObservation == prevBatchObs:
				repeats++ // the observation didn't change (re-read the same thing)
			default:
				repeats = 1 // an identical batch whose observation changed is progress
			}
		}
		// Advance the guard's batch trail at the end of the round, once the
		// whole batch has been dispatched: the next round's batch-identity
		// check compares its signature against this round's.
		prevBatchSig = sig
		prevBatchObs = lastObservation

		// The redirect didn't take and the same error keeps recurring — stop thrashing
		// and finalize from what's gathered.
		if stop == "stuck" {
			break
		}

		// The same batch repeated past the cap without new information: the
		// model won't recover on its own (the nudge below already gave it a
		// chance). Finalize.
		if repeats >= maxRepeatedToolCalls {
			stop = "no-progress"
			break
		}
		// One repeat short of the cap, inject a nudge so it can change course.
		if repeats == maxRepeatedToolCalls-1 {
			appendMsg(Message{Role: RoleUser, Content: noProgressNudge})
		}
		// Context budget spent: stop reading and answer from what's gathered, so
		// a bulk-reading model can't grow context past the ceiling.
		if b.ReadBudgetBytes > 0 && stats.ReadBytes >= b.ReadBudgetBytes {
			stop = "read-budget"
			break
		}
	}
	if stop == "" {
		stop = "max-iter"
	}
	stats.StopReason = stop
	stats.FinalizeForced = true
	content, finalStats, err := finalizeLoop(ctx, send, req, finalizePromptFor(stop, ts.Finalize), &stats, appendMsg, lastObservation)
	// Issue #161: a bound-forced finish skips the clean-finalize path where
	// FinalizeHook runs (the turn never answered with no tool calls), so the
	// testwatch / turn-end-lint receipts the hook delivers would vanish
	// exactly when the work is most likely unfinished. Consult the session's
	// forced-finalize counterpart for one more tools-withheld round; an
	// empty note leaves the answer untouched.
	content = forcedFinalizeHookRound(ctx, send, req, ts, &finalStats, appendMsg, content)
	// Restore the advertised tools: finalize withheld them, but the caller's
	// request (cs.Request for the coder) is long-lived and reused next turn.
	req.Tools = ts.Tools
	return content, finalStats, err
}

// finalizeLoop requests a final answer with the tool set withheld, so a model
// that ran out of budget (iterations, bytes, or stuck repeating) still produces
// an answer grounded in what it read rather than returning nothing. Every
// finalize send (including this one) always goes out with effort OFF
// (docs/thinking-models.md §5a): tools are withheld, so this is a pure
// formatting ask — generalizes P4's clamp-gated salvage-only fix to every
// finalize send, unconditionally.
func finalizeLoop(ctx context.Context, send Sender, req *AgentRequest, prompt string, stats *loopStats, appendMsg func(Message), lastObservation string) (string, loopStats, error) {
	req.Tools = nil
	defer disableEffortForSend(req)()
	appendMsg(Message{Role: RoleUser, Content: prompt})
	res, _, err := send.Send(ctx, req)
	if err != nil {
		stats.StopReason = "error"
		return "", *stats, err
	}
	if res == nil || len(res.Choices) == 0 {
		stats.StopReason = "error"
		return "", *stats, errNoChoices
	}
	accountUsage(stats, res, req.MaxTokens)
	msg := res.Choices[0].Message
	appendMsg(msg)
	// Issue #230: sanitize the reply at the SOURCE — strip leaked tool-call
	// markup (a "clean" reply that is really raw <tool_call>
	// or <function_calls> markup) before the salvage chains decide what to
	// repair. Without this a markup-only reply passes every gate (non-empty,
	// not clamped) and reportWithheldToolCalls would convert it into prose,
	// shipping the markup into the commit message. Sanitizing here makes the
	// empty-salvage chain (below) the SINGLE bounded repair for a
	// markup-only or truncated reply — no second, redundant re-ask.
	answer := sanitizeFinalAnswer(msg.Content)
	// Not gated on MaxTokensClamped — see runLoop's empty-answer branch above.
	if answer == "" {
		wasClamped := stats.MaxTokensClamped
		if a2 := salvageEmptyFinalize(ctx, send, req, stats, appendMsg); a2 != "" {
			answer = a2
			stats.SalvagedUnclamped = !wasClamped
		}
		if answer == "" {
			if a2 := salvageObservationFinalize(lastObservation, stats); a2 != "" {
				answer = a2
				stats.SalvagedUnclamped = !wasClamped
			}
		}
	}
	// The clamped salvage runs when the (sanitized) answer looks cut off
	// mid-sentence OR hit the token ceiling. The recorded "Let me restate my
	// complete final" truncation (issue #230) often arrives unclamped by
	// token count, so MaxTokensClamped alone misses it. !stats.Salvaged keeps
	// the existing "salvage once" bound: a recovery by the empty salvage
	// above already set Salvaged, so this won't re-ask.
	if answer != "" && (stats.MaxTokensClamped || looksTruncated(answer)) && !stats.Salvaged {
		if a2 := salvageClampedFinalize(ctx, send, req, stats, appendMsg); a2 != "" {
			answer = a2
		}
	}
	return reportWithheldToolCalls(answer), *stats, nil
}

// reportWithheldToolCalls is the forced-wrap-up half of issue #132's text
// tool-call recovery. When the tools-withheld finalize reply carries tool-call
// markup in its text — the model "reaching for" the edit it would have made
// (the recorded qwen3-coder <tool_call> shape) — the intended action is reported in the
// completion receipt instead of being dropped: the transcript keeps the model's
// prose (markup stripped) and the receipt leads with what it intended to do.
// Prose-only replies pass through untouched.
func reportWithheldToolCalls(answer string) string {
	calls := parseToolCallsFromContent(answer)
	if len(calls) == 0 {
		return answer
	}
	prose := strings.TrimSpace(stripToolMarkup(answer))
	var b strings.Builder
	for _, c := range calls {
		b.WriteString("Intended action not executed (tools are withheld during wrap-up): " + c.ActivityLabel() + "\n")
	}
	if prose != "" {
		b.WriteString(prose)
	}
	return strings.TrimSpace(b.String())
}

func salvageObservationFinalize(obs string, stats *loopStats) string {
	m := mostFrequentCandidateRe.FindStringSubmatch(obs)
	if m == nil {
		return ""
	}
	stats.StopReason = "salvaged-finalize"
	stats.Salvaged = true
	return "The bounded tool evidence identifies " + m[1] + " as the most frequent candidate."
}

// salvageEmptyReasoningRetry is issue #149's recovery: an empty finish (no
// content, no tool calls) from a role whose reasoning is ON — a Qwen-style
// model that spent its whole turn deliberating and came back with nothing —
// is recovered by re-sending the SAME request once with reasoning pinned off.
// The role's configured effort is untouched (recovery, not policy): the
// one-shot mutation rides the same pattern as disableEffortForSend's existing
// uses and is restored before return. The caller must NOT have appended the
// empty assistant message yet (it appends only the message that survives the
// retry decision), so the retry re-sends the request that came back empty
// byte-identically — a trailing empty turn would not be the same request.
//
// The result is the whole retry message, not just its text, with the SAME
// text-form tool-call recovery the main round applies (recoverTextToolCalls):
// for exactly the Qwen models this issue targets, a reasoning-off retry can
// answer with native <tool_call> markup rather than structured
// tool_calls, and without the recovery that markup would be returned as the
// turn's final prose instead of dispatched. A retry that also comes back
// empty (or errors) returns nil. Exactly one retry per empty finish, enforced
// by construction: the caller invokes this at most once per empty branch.
func salvageEmptyReasoningRetry(ctx context.Context, send Sender, req *AgentRequest, stats *loopStats) *Message {
	restore := disableEffortForSend(req)
	defer restore()
	res, _, err := send.Send(ctx, req)
	if err != nil || res == nil || len(res.Choices) == 0 {
		return nil
	}
	accountUsage(stats, res, req.MaxTokens)
	msg := res.Choices[0].Message
	recoverTextToolCalls(&msg)
	return &msg
}

// finalizeHookRound is issue #141's finalize round: if the turn mutated a
// test file and the harness has a "tests changed" receipt, hand the model
// that fact in one more finalize round so its FINAL answer accounts for the
// loss — the complaint behind #141 is that the loss goes unreported, and a
// receipt that only lands in the journal reaches neither the model nor a
// human. The caller's FinalizeHook (turn.go) supplies the note; runLoop calls
// this ONLY at the point the model has answered with no tool calls — after
// every tool call has run — so the test-file before-side is already settled.
// A nil hook (every subagent, every test) or an empty note means "nothing to
// report" and returns answer untouched without a send. The note is a real
// transcript message (the model's answer to it is the turn's record) and this
// round WITHHOLDS tools (finalize-style): the ask is to address what was
// already done, not to do more work. req.Tools is restored before return.
//
// APPEND, never replace: a small model answers the note narrowly (just what
// happened to the tests), so replacing the turn's real answer with that reply
// would drop the actual work summary from TurnResult.Reply (and the headless
// `cortex turn` driver with it). The note's ask is to restate the FULL answer
// — summary first, then the test-loss accounting — so appending yields a
// complete record in either model behavior: a model that does restate the
// summary reads naturally (summary → its test note), and one that doesn't
// still keeps the original summary plus the note's answer. Empty replies and
// send failures keep the original answer untouched.
func finalizeHookRound(ctx context.Context, send Sender, req *AgentRequest, ts Toolset, stats *loopStats, appendMsg func(Message), answer string) string {
	if ts.FinalizeHook == nil {
		return answer
	}
	note := ts.FinalizeHook()
	if note == "" {
		return answer
	}
	savedTools := req.Tools
	req.Tools = nil
	defer func() { req.Tools = savedTools }()
	appendMsg(Message{Role: RoleUser, Content: note})
	r2, _, err := send.Send(ctx, req)
	if err != nil || r2 == nil || len(r2.Choices) == 0 {
		return answer
	}
	appendMsg(r2.Choices[0].Message)
	accountUsage(stats, r2, req.MaxTokens)
	if a2 := strings.TrimSpace(r2.Choices[0].Message.Content); a2 != "" {
		if answer != "" {
			answer += "\n\n"
		}
		answer += a2
	}
	return answer
}

// forcedFinalizeHookRound is issue #161's counterpart of finalizeHookRound:
// when a bound dragged the run to its forced finalize (max-iter,
// token-budget, …), the turn never took the clean-finalize path, so
// Toolset.FinalizeHook (the testwatch #141/#154 and turn-end lint #129
// receipts, via turn.go) never ran and the forced answer would hand in
// unfinished work — leftover debug prints, scratch files, unfixed lint
// findings — without ever naming it. runLoop calls this exactly once, on
// the forced-finalize exit, AFTER finalizeLoop has produced the answer: it
// consults ts.OnForcedFinalize (nil for every subagent and test) for a
// harness note and, when the note is non-empty, hands it to the model in
// one more, tools-withheld finalize round — the same shape as
// finalizeHookRound, whose discipline this round follows exactly:
//
//   - APPEND, never replace: a small model answers the note narrowly
//     (just the leftover facts), so the turn's forced answer — the honesty-
//     core digest the bound-forced run earned — is kept and the reply to
//     the note is appended to it (turn.go's forced-framing asks the model
//     to restate the full picture, the same way the clean-finalize framing
//     does, so the append is a complete record either way);
//   - tools withheld and effort off (docs/thinking-models.md §5a): this is
//     a formatting/accounting ask on a run whose tool budget is spent, not
//     more work;
//   - empty note, empty reply, or a failed send leaves the answer
//     untouched (the common case — a bound-forced turn with nothing the
//     scans found to report — is byte-identical to before this seam).
//
// The round's usage is folded into the run's stats (accountUsage), so the
// extra model round-trip counts toward the caller's token and cost totals
// like every other engine send.
func forcedFinalizeHookRound(ctx context.Context, send Sender, req *AgentRequest, ts Toolset, stats *loopStats, appendMsg func(Message), answer string) string {
	if ts.OnForcedFinalize == nil {
		return answer
	}
	note := ts.OnForcedFinalize(*stats)
	if note == "" {
		return answer
	}
	savedTools := req.Tools
	req.Tools = nil
	defer func() { req.Tools = savedTools }()
	appendMsg(Message{Role: RoleUser, Content: note})
	res, _, err := send.Send(ctx, req)
	if err != nil || res == nil || len(res.Choices) == 0 {
		return answer
	}
	appendMsg(res.Choices[0].Message)
	accountUsage(stats, res, req.MaxTokens)
	if a2 := strings.TrimSpace(res.Choices[0].Message.Content); a2 != "" {
		if answer != "" {
			answer += "\n\n"
		}
		answer += a2
	}
	return answer
}

// recoverTextToolCalls recovers tool calls the model wrote into its reply
// text instead of emitting as structured tool_calls, in place: when the
// message carries no tool calls but its content parses as tool-call markup —
// the Qwen-native <tool_call>…</tool_call> shape or Hermes-style
// <function_calls>JSON</function_calls> tags (parseToolCallsFromContent) — the
// parsed calls become the message's tool calls and the raw markup is stripped
// from the content. Shared by the main round (runLoop) and the issue #149
// off-retry (salvageEmptyReasoningRetry) so a model that answers either with
// text-form calls gets them dispatched rather than returned as prose. A no-op
// when the message already has tool calls or the content has no parseable
// calls.
func recoverTextToolCalls(msg *Message) {
	if msg == nil || len(msg.ToolCalls) > 0 {
		return
	}
	if calls := parseToolCallsFromContent(msg.Content); len(calls) > 0 {
		msg.ToolCalls = calls
		msg.Content = stripToolMarkup(msg.Content)
	}
}

// hasToolCallMarkup reports whether the content carries tool-call markup —
// the Qwen-native <tool_call> shape or Hermes-style <function_calls> tags
// (issue #230). Used by the finalize sanitizers to detect a reply that
// leaked raw markup into the final answer, which would end up in a commit
// message or a turn receipt if left unstripped.
func hasToolCallMarkup(content string) bool {
	return len(parseToolCallsFromContent(content)) > 0
}

// looksTruncated reports whether the answer looks cut off mid-sentence
// (issue #230). The check is intentionally conservative — it fires only on
// a clear mid-sentence cutoff pattern, not on every non-sentence-ending
// character, so a reply that ends with a letter (a common, natural ending
// for a terse answer) is not flagged. The patterns:
//
//   - ends with a conjunction or preposition ("and", "or", "but", "to",
//     "in", "on", "at", "by", "for", "with", "from", "that", "which",
//     "who", "whom", "whose") followed by nothing — a sentence that was
//     clearly going to continue;
//   - ends with a dangling "my" or "the" (indefinite article / possessive
//     with no noun after it) — the recorded "Let me restate my complete
//     final" case, where the model was cut off before naming the final
//     answer;
//   - ends with a word of 3+ letters that is NOT a sentence-ending
//     character and the answer is short (under 15 words) — a short reply
//     that ends mid-thought is more likely truncated than a long one.
//
// This is a heuristic, not a proof: a reply that ends mid-sentence but
// doesn't match these patterns (e.g. ends with a 4-letter noun) is not
// flagged. The commit step's SummaryIssue check is the last line of
// defense — a missed truncation here still gets caught there.
func looksTruncated(answer string) bool {
	answer = strings.TrimSpace(answer)
	if answer == "" {
		return false
	}
	last := answer[len(answer)-1]
	// A reply that ends with a sentence-ending character is not truncated.
	switch last {
	case '.', '!', '?', ')', ']', '}', '"', '\'', '`':
		return false
	}
	// A reply that ends with a mid-sentence punctuation mark (comma,
	// semicolon, colon, hyphen) is suspicious but not conclusive — the
	// model may have intended a list or a dash-separated aside.
	switch last {
	case ',', ';', ':', '-':
		return false
	}
	// Extract the last word (the trailing run of letters).
	i := len(answer) - 1
	for i >= 0 && isLetter(answer[i]) {
		i--
	}
	lastWord := strings.ToLower(answer[i+1:])
	if lastWord == "" {
		return false
	}
	// A dangling conjunction, preposition, or relative pronoun: the
	// sentence was clearly going to continue.
	switch lastWord {
	case "and", "or", "but", "to", "in", "on", "at", "by", "for",
		"with", "from", "that", "which", "who", "whom", "whose",
		"the", "my", "a", "an", "of", "as", "if", "then", "so":
		return true
	}
	return false
}

// isLetter reports whether c is an ASCII letter (lowercase or uppercase).
func isLetter(c byte) bool {
	return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}

// sanitizeFinalAnswer is the issue #230 fix for the finalize path: it strips
// leaked tool-call markup (Qwen <tool_call> or
// Hermes <function_calls> tags) from a final answer — the markup is never a
// valid part of a reply, it is a leaked internal representation that would
// otherwise ship into a commit message or turn receipt. The result is the
// trimmed content with every parseable tool-call block removed; "" when the
// answer was markup-only (nothing left after the strip).
//
// It is PURE — no send, no salvage. The repair for what it leaves behind is
// the caller's existing bounded salvage chain, consulted ONCE on the
// sanitized answer:
//   - "" (a truly empty or markup-only answer) → salvageEmptyFinalize /
//     salvageObservationFinalize, exactly as a bare empty finish gets.
//   - a non-empty answer that looksTruncated → the clamped salvage
//     (salvageClampedFinalize), which rewrites it as a concise final answer.
//
// Sanitizing at the SOURCE (the reply just received) — rather than after the
// salvages — is what keeps the repair bounded: the salvage chains see the
// sanitized answer and make their single, usual decision, so a markup-only or
// truncated reply gets exactly one re-ask, never a second one layered on top.
func sanitizeFinalAnswer(answer string) string {
	if answer == "" {
		return ""
	}
	if !hasToolCallMarkup(answer) {
		return strings.TrimSpace(answer)
	}
	return strings.TrimSpace(stripToolMarkup(answer))
}

// retryText reports a retry message's trimmed content ("" when it carries
// tool calls, which the caller dispatches instead of reading as an answer).
func retryText(msg *Message) string {
	if msg == nil || len(msg.ToolCalls) > 0 {
		return ""
	}
	return strings.TrimSpace(msg.Content)
}

// reasoningFallbackNote is the transcript marker for a fired issue #149
// fallback (issue #149: "record the fallback in the transcript"). A short
// harness line written to the session JSONL under a distinct kindNote entry —
// loadSession (cortex resume) skips kindNote, so it is never part of the
// model-visible history or the wire conversation, only of the human-readable
// transcript. Writing it as a plain kindMessage system line would put a
// non-leading system message into the resumed conversation (some chat
// templates reject or mishandle system messages that aren't first). The
// journal receipt (recovery.reasoning_fallback) is the authoritative record;
// the transcript note keeps the fallback visible in the human-readable log.
func reasoningFallbackNote() string {
	return "Harness note: the previous reply came back empty (the model's reasoning consumed the whole completion). The same request was re-sent once with reasoning disabled and recovered an answer."
}

// salvageEmptyFinalize re-asks ONCE (tools withheld) with a hard brevity floor
// when a finish came back EMPTY — clamped or not. Gated only on the empty
// answer, so a healthy run never triggers it. Every finalize send goes out
// with effort OFF (docs/thinking-models.md §5a) — P4 introduced this gated on
// DeliberationClamped; P5a generalized it to every finalize/salvage send.
// DeliberationClamped is still recorded for eval attribution even though it
// no longer gates this.
func salvageEmptyFinalize(ctx context.Context, send Sender, req *AgentRequest, stats *loopStats, appendMsg func(Message)) string {
	req.Tools = nil
	defer disableEffortForSend(req)()
	appendMsg(Message{Role: RoleUser, Content: reFinalizePrompt})
	res, _, err := send.Send(ctx, req)
	if err != nil || res == nil || len(res.Choices) == 0 {
		return ""
	}
	accountUsage(stats, res, req.MaxTokens)
	msg := res.Choices[0].Message
	appendMsg(msg)
	a := strings.TrimSpace(msg.Content)
	if a != "" {
		stats.StopReason = "salvaged-finalize"
		stats.Salvaged = true
	}
	return a
}

// salvageClampedFinalize is salvageEmptyFinalize's counterpart for a
// NON-empty clamped answer (verbose but present prose that ran into the
// completion ceiling): same unconditional effort-off treatment (§5a).
func salvageClampedFinalize(ctx context.Context, send Sender, req *AgentRequest, stats *loopStats, appendMsg func(Message)) string {
	req.Tools = nil
	defer disableEffortForSend(req)()
	appendMsg(Message{Role: RoleUser, Content: rewriteClampedPrompt})
	res, _, err := send.Send(ctx, req)
	if err != nil || res == nil || len(res.Choices) == 0 {
		return ""
	}
	accountUsage(stats, res, req.MaxTokens)
	msg := res.Choices[0].Message
	appendMsg(msg)
	a := strings.TrimSpace(msg.Content)
	if a != "" {
		stats.StopReason = "salvaged-finalize"
		stats.Salvaged = true
	}
	return a
}

// accountUsage folds one response's token usage into the run stats, tracking the
// live prompt fill, the per-request output peak, and whether any request hit the
// completion ceiling (the eval-time runaway tripwire).
func accountUsage(s *loopStats, res *AgentResponse, maxTokens int) {
	s.InputTokens += res.Usage.PromptTokens
	s.OutputTokens += res.Usage.CompletionTokens
	s.Cost += res.Usage.Cost
	s.ReasoningTokens += res.Usage.ReasoningTokens()
	s.LastPromptTokens = res.Usage.PromptTokens
	s.LastOutputTokens = res.Usage.CompletionTokens
	s.LastCachedTokens = res.Usage.CachedPromptTokens()
	if res.Usage.CompletionTokens > s.PeakOutputTokens {
		s.PeakOutputTokens = res.Usage.CompletionTokens
	}
	if maxTokens > 0 && res.Usage.CompletionTokens >= maxTokens {
		s.MaxTokensClamped = true
	}
	if deliberationClamped(res) {
		s.DeliberationClamped = true
	}
}

// countTool bumps the per-tool counter for the locate-then-read discriminator
// (study eval §5). The grep/outline names are forward-compatible with the study
// tools that land in study phases 1–2.
func countTool(s *loopStats, name string) {
	switch name {
	case tools.FunctionReadFile:
		s.Reads++
	case tools.FunctionGrep:
		s.Greps++
	case tools.FunctionOutline:
		s.Outlines++
	}
}

// progressLine renders a one-line breadcrumb for a tool call, shown live via the
// Progress sink so a blocking subagent isn't silent.
//
// NOTE: left glyphed (▸), unlike internal/tools.printToolAction's REPL
// "tool: " rework (2026-07-19 de-glyph decision) — this string is also the
// SSE progress payload's `line` field (serve_stream.go, asserted verbatim by
// a golden test in the serve surface, which this change does not touch).
func progressLine(call ToolCall) string {
	return "  ▸ " + call.ActivityLabel()
}

// requestFor assembles a model request from a spec — the single build site where
// model/base/key/effort wire fields are set, and the single place a finite
// max_tokens is stamped (subsuming the deleted per-payload output-cap helper).
// maxTokens must
// be >0; it falls back to the role/default cap only when a caller passes 0, so no
// request path is ever unbounded. dialect selects the effort translation
// (docs/thinking-models.md §2 — llm.DialectTemplateKwargs or
// llm.DialectOpenRouter, the caller's session.isOpenRouter()). Used by every
// subagent caller (the coder reuses its long-lived cs.Request instead, which
// carries the same stamp from init).
func requestFor(spec ModelSpec, system, seed string, toolset []Tool, maxTokens int, dialect llm.Dialect) *AgentRequest {
	if maxTokens <= 0 {
		maxTokens = spec.maxOut(defaultAgentMaxTokens)
	}
	req := &AgentRequest{
		Model:       spec.Model,
		BaseURL:     spec.Endpoint,
		APIKey:      resolveKey(spec),
		Temperature: spec.temperature(defaultTemperature),
		MaxTokens:   maxTokens,
		Tools:       toolset,
		Messages: []Message{
			{Role: RoleSystem, Content: system},
			{Role: RoleUser, Content: seed},
		},
		Timeout:     spec.timeout(requestTimeout),
		MaxAttempts: spec.maxAttempts(maxSendAttempts),
		Backoff:     spec.backoff(retryBackoff),
		Vision:      spec.VisionEnabled(),
	}
	applyEffort(req, dialect, spec.Thinking)
	return req
}

// blockingSender is the subagent / non-streaming round-trip: one plain blocking
// Send, no terminal echo (Progress shows the tool calls). The per-request
// deadline + ctx-cancel ride on req.Send → sendOnce (http.NewRequestWithContext),
// so a cancelled ctx closes the socket. A TEST-ONLY subagentSenderOverride (see
// CortexSession) replaces the round-trip for a named subagent role, so a test
// can script the subagent's model replies with zero network while the REAL
// runLoop + dispatcher still run — nil in every production session.
func (cs *CortexSession) blockingSender() Sender {
	if cs.subagentSenderOverride != nil {
		// Only the `agent` subagent is file-mutating (study/learn are
		// read-only), so only its role is ever scripted; the literal is the
		// same string internal/tools.Agent.Role carries.
		if send, ok := cs.subagentSenderOverride["agent"]; ok {
			return send
		}
	}
	return SenderFunc(func(ctx context.Context, req *AgentRequest) (*AgentResponse, bool, error) {
		res, err := req.Send(ctx)
		return res, false, err
	})
}

// coderSender is the main coder turn's round-trip: it delegates to the existing
// send() (streaming echo + breadcrumb in the REPL, blocking spinner otherwise),
// which sends cs.Request (== the engine's req for the coder). On the
// non-streamed path it prints the assistant prose itself — the streaming path
// already echoed it live, so the engine never prints.
func (cs *CortexSession) coderSender() Sender {
	return SenderFunc(func(ctx context.Context, _ *AgentRequest) (*AgentResponse, bool, error) {
		res, streamed, err := cs.send(ctx)
		if err == nil && !streamed && !cs.quiet && res != nil && len(res.Choices) > 0 {
			printCoderProse(res.Choices[0].Message)
		}
		return res, streamed, err
	})
}

// printCoderProse prints the model's prose on the blocking (non-streamed) path,
// mirroring the old Resolve step: strip any unnormalized Qwen tool markup first
// (the live stream suppresses it at the marker) and print only if prose remains.
func printCoderProse(msg Message) {
	content := msg.Content
	if len(msg.ToolCalls) == 0 {
		if calls := parseToolCallsFromContent(content); len(calls) > 0 {
			content = stripToolMarkup(content)
		}
	}
	if strings.TrimSpace(content) != "" {
		Message{Role: "assistant", Content: content}.Print()
	}
}

// coderDispatcher executes one coder tool call: the activity spinner + Execute
// against the full session, refusing nothing (the coder is granted every tool).
// A canceled ctx short-circuits with an interrupted observation, matching the
// old runToolCalls per-call behavior. testerDispatcherOverride (CortexSession's
// test-only seam, session_core.go) can replace it entirely for tests that drive
// the REAL turn path with scripted tool results instead of real file access.
func (cs *CortexSession) coderDispatcher() AgentDispatcher {
	if cs.coderDispatcherOverride != nil {
		return cs.coderDispatcherOverride()
	}
	return DispatchFunc(func(ctx context.Context, call ToolCall) string {
		if ctx.Err() != nil {
			return "Error: interrupted by user before this tool ran"
		}
		// Issue #141: snapshot the before-side of what this call can
		// mutate, BEFORE the tool runs — the turn-end testguard scan
		// (testwatch.go) needs the pre-turn content to see what the turn
		// removed. Named paths (write_file / edit_file / remove_path)
		// snapshot exactly that file; a bash call names no file but can
		// mutate any file in the workspace, so it arms the whole
		// workspace's test-named files instead (armTestwatch). A no-op
		// for every other tool and for a missing file.
		isBash := call.Function.Name == tools.FunctionBash
		bashStart := time.Time{}
		if isBash {
			cs.armTestwatch()
			bashStart = time.Now()
			// Issue #219: record a bash command that is the project's OWN
			// test/build run BEFORE it runs (receiptBash, turn_receipt.go) —
			// the receipt's verification fact pairs the model's own
			// verification runs with their outcomes; a non-verification
			// command (rm, ls, git, …) records nothing.
			if cmd, err := call.StringArg("command"); err == nil {
				cs.receiptBash(cmd)
			}
		}
		if p := testwatchTouchedPath(call); p != "" {
			cs.touchFile(p)
			// Issue #129 piece 3: record the touched file for the turn-end
			// lint pass (the per-edit hook is format-only; lint runs once at
			// finalize over the turn's distinct touched files, turn_lint.go).
			// Only write_file / edit_file feed the lint list — remove_path
			// deletes, and linting a file the turn just removed would report a
			// spurious "could not run" finding for a file that is gone on
			// purpose (RunTurnEndLint also skips missing paths, belt and
			// suspenders: a write the turn then deleted via bash is covered
			// there, this covers the remove_path leg).
			if call.Function.Name == tools.FunctionWriteFile || call.Function.Name == tools.FunctionEditFile {
				cs.lintTouchedPath(p)
			}
		}
		cs.startActivity(call.ActivityLabel())
		out, outcome, err := tools.Execute(ctx, call, cs)
		cs.stopActivity()
		// Issue #154: after a bash command, sweep the workspace for
		// scratch-named non-test files the command created — the touch hook
		// can't snapshot a bash-created file (bash names no file), and the
		// bash arm's baseline only covers test-named files, so without this
		// sweep a scratch file left behind would be invisible to the
		// leftover-debug scan. Best-effort: a no-op when there is no workdir.
		if isBash {
			cs.sweepScratchFiles()
		}
		// Issue #219: record the outcome of a verification bash run plus the
		// wall time of the call. The outcome is Execute's own return value
		// for THIS call — Ran=true with the process's exit code only when the
		// bash tool spawned the process; a disabled tool, a validation
		// rejection, a gate refusal, or a declined prompt all return
		// Ran=false, and a callErr ran nothing — so receiptBashOutcome
		// renders those as not-run, never as an exit code.
		if isBash {
			if cmd, argErr := call.StringArg("command"); argErr == nil {
				cs.receiptBashOutcome(cmd, outcome, err, time.Since(bashStart))
			}
		}
		if err != nil {
			return "Error: " + err.Error()
		}
		// Issue #102: untrusted-content taint detection. Any observation
		// carrying the framing wrapper (tools.ObservationIsUntrustedContent
		// matches the banner this package's wrapper stamps — fetch_url and
		// web_search frame every result, "no results" included) taints the
		// turn: a later bash GateShell call consults the taint and raises
		// approval for Risky commands (tool_deps.go), and the event is
		// journalled best-effort inside the record call. Detection is on the
		// OBSERVATION, not on the tool name: the framing is the signal, so a
		// tool outside the web pair that somehow emits the banner taints too
		// (fail-closed), and a page cannot forge the banner at the
		// observation's start or strip it from itself. Skipped when the
		// observation is an error — the tool returned no content to trust or
		// distrust.
		if tools.ObservationIsUntrustedContent(out) {
			cs.recordUntrustedContent(call.Function.Name)
		}
		return out
	})
}

// coderBeforeBatch prints the blank line that separates the model's prose from
// its tool actions (the old runToolCalls leading Println), suppressed in quiet
// headless mode.
func (cs *CortexSession) coderBeforeBatch() {
	if !cs.quiet {
		fmt.Println()
	}
}
