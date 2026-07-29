package main

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/dereksantos/cortex/internal/agent"
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
type Toolset struct {
	Tools           []Tool
	Dispatch        AgentDispatcher
	BeforeBatch     func()
	AfterToolResult func()
	// Finalize selects the forced-finalize closing (see FinalizeStyle). Zero
	// value = FinalizeSubagent, so subagent callers need no change.
	Finalize FinalizeStyle
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
	LastPromptTokens int    // most recent prompt_tokens (the live context gauge)
	LastCachedTokens int    // most recent provider-reported cached prompt tokens (0 if unreported)
	PeakOutputTokens int    // max completion tokens on any single request
	MaxTokensClamped bool   // any request hit Bounds.MaxTokens (runaway tripwire)
	Salvaged         bool   // an empty clamped finish was recovered by one terse re-ask
	Iterations       int    // model rounds consumed
	StopReason       string // clean-finalize|salvaged-finalize|max-iter|read-budget|no-progress|deadline|error
	FinalizeForced   bool   // answered because a bound dragged finalize out

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

// maxRepeatedToolCalls bounds byte-identical consecutive tool-call batches before
// the no-progress guard intervenes: a weak model can re-issue the same call until
// it burns the turn (observed: 68 identical greps, 2026-06-14). On the penultimate
// repeat the engine nudges; on the next it finalizes. Defined here with its sole
// user (the guard in runLoop).
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
	default:
		return "Harness note: the same tool error keeps recurring and you are not making progress. Stop and report what you did and what is blocking you."
	}
}

// noProgressNudge is injected one repeat short of the cap so a stuck model can
// change course before the guard breaks the loop. Engine-level (every caller),
// not a main-loop special case.
const noProgressNudge = "Harness note: that tool call was byte-identical to the previous one and produced the same result. Repeating it will not yield new information — try a different command or approach, or stop and report what you've found."

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

// reFinalizePrompt is the salvage ask when the first finalize came back EMPTY — a
// reasoning model that spent its whole completion budget deliberating and emitted
// no answer (the max-tokens-clamp signature). It re-asks with a hard brevity floor
// so the model spends its budget on the answer, not the deliberation.
const reFinalizePrompt = "Your previous reply was empty — you spent the whole budget thinking and never answered. You already have everything you need. Do NOT deliberate further: state the answer NOW, directly, in at most five sentences."

// rewriteClampedPrompt is the salvage ask when a model DID answer, but only by
// running into the completion ceiling. That answer is usually verbose and fails
// the eval's runaway tripwire; ask for a compact rewrite, then mark the run
// salvaged if the rewrite succeeds.
const rewriteClampedPrompt = "Your previous reply hit the completion limit. Rewrite it now as a concise final answer in at most five sentences. Keep only the facts needed to answer the goal; do not add tool calls or extra reasoning."

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
func runLoop(ctx context.Context, send Sender, req *AgentRequest, ts Toolset, b Bounds, p Progress, appendMsg func(Message), onStatusUpdate func(lastPromptTokens int, maxTokens int)) (string, loopStats, error) {
	var stats loopStats
	req.Tools = ts.Tools
	if b.MaxTokens > 0 {
		req.MaxTokens = b.MaxTokens
	}

	var lastSig string
	var repeats int             // consecutive batches identical to lastSig, including current
	errSeen := map[string]int{} // error-class → times seen this turn (survives interleaved reads)
	jitter := false             // perturb temperature on the next send (set when stuck)
	baseTemp := req.Temperature // restore after a one-shot jitter
	stop := ""
	lastObservation := ""
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
			onStatusUpdate(stats.LastPromptTokens, req.MaxTokens)
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
		// Recover Qwen-native XML tool calls the proxy didn't normalize, so a
		// call isn't silently lost (empty tool_calls reads as a final answer).
		if len(msg.ToolCalls) == 0 {
			if calls := parseXMLToolCalls(msg.Content); len(calls) > 0 {
				msg.ToolCalls = calls
				msg.Content = stripToolMarkup(msg.Content)
			}
		}
		// Append the assistant message BEFORE any tool results: the API requires
		// assistant(tool_calls) → tool(result) ordering.
		appendMsg(msg)

		// No tool calls → the model answered. That prose IS the result — unless it's
		// EMPTY because the model spiraled to the token clamp on this turn, which we
		// salvage with one terse re-ask rather than returning nothing.
		if len(msg.ToolCalls) == 0 {
			answer := strings.TrimSpace(msg.Content)
			if answer == "" && stats.MaxTokensClamped {
				if a2 := salvageEmptyFinalize(ctx, send, req, &stats, appendMsg); a2 != "" {
					req.Tools = ts.Tools // salvage withheld them; restore for the caller's reuse
					return a2, stats, nil
				}
				if a2 := salvageObservationFinalize(lastObservation, &stats); a2 != "" {
					req.Tools = ts.Tools
					return a2, stats, nil
				}
			}
			if answer != "" && stats.MaxTokensClamped {
				if a2 := salvageClampedFinalize(ctx, send, req, &stats, appendMsg); a2 != "" {
					req.Tools = ts.Tools
					return a2, stats, nil
				}
			}
			stats.StopReason = "clean-finalize"
			return answer, stats, nil
		}

		// No-progress guard: a weak model can re-issue the identical batch
		// forever. Track consecutive repeats and break before it burns the run.
		if sig := toolCallSignature(msg.ToolCalls); sig == lastSig {
			repeats++
		} else {
			lastSig, repeats = sig, 1
		}

		if ts.BeforeBatch != nil {
			ts.BeforeBatch()
		}
		// hintClass carries a just-crossed stuck threshold so the off-ramp is injected
		// AFTER all tool results (the API requires tool results to follow the
		// assistant message before any user turn).
		hintClass := ""
		for _, call := range msg.ToolCalls {
			countTool(&stats, call.Function.Name)
			obs := ts.Dispatch.Dispatch(ctx, call)
			lastObservation = obs
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
			appendMsg(Message{Role: RoleTool, ToolCallID: call.ID, Content: obs})
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
		// The redirect didn't take and the same error keeps recurring — stop thrashing
		// and finalize from what's gathered.
		if stop == "stuck" {
			break
		}

		// The same batch repeated past the cap: the model won't recover on its
		// own (the nudge below already gave it a chance). Finalize.
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
	answer := strings.TrimSpace(msg.Content)
	if answer == "" && stats.MaxTokensClamped {
		if a2 := salvageEmptyFinalize(ctx, send, req, stats, appendMsg); a2 != "" {
			answer = a2
		}
		if answer == "" {
			answer = salvageObservationFinalize(lastObservation, stats)
		}
	}
	if answer != "" && stats.MaxTokensClamped && !stats.Salvaged {
		if a2 := salvageClampedFinalize(ctx, send, req, stats, appendMsg); a2 != "" {
			answer = a2
		}
	}
	return answer, *stats, nil
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

// salvageEmptyFinalize re-asks ONCE (tools withheld) with a hard brevity floor when
// a finish came back EMPTY because a reasoning model burned its whole budget
// deliberating (the max-tokens clamp → no prose). Returns the salvaged answer (and
// stamps stop_reason + Salvaged) or "". Gated by the empty-clamp signature at both
// call sites, so a healthy run (always prose) never triggers it. Like every
// finalize send (docs/thinking-models.md §5a), this re-ask always goes out
// with effort OFF — P4 introduced this gated on stats.DeliberationClamped;
// P5a generalizes it to every finalize/salvage send unconditionally, since
// tools are withheld here too and pleading for brevity while still letting
// the model reason was the exact failure this turns into an actual control.
// stats.DeliberationClamped is still recorded (accountUsage) for eval
// attribution even though it no longer gates this mutation.
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
	}
	applyEffort(req, dialect, spec.Thinking)
	return req
}

// blockingSender is the subagent / non-streaming round-trip: one plain blocking
// Send, no terminal echo (Progress shows the tool calls). The per-request
// deadline + ctx-cancel ride on req.Send → sendOnce (http.NewRequestWithContext),
// so a cancelled ctx closes the socket.
func (cs *CortexSession) blockingSender() Sender {
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
		if calls := parseXMLToolCalls(content); len(calls) > 0 {
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
// old runToolCalls per-call behavior.
func (cs *CortexSession) coderDispatcher() AgentDispatcher {
	return DispatchFunc(func(ctx context.Context, call ToolCall) string {
		if ctx.Err() != nil {
			return "Error: interrupted by user before this tool ran"
		}
		cs.startActivity(call.ActivityLabel())
		out, err := tools.Execute(ctx, call, cs)
		cs.stopActivity()
		if err != nil {
			return "Error: " + err.Error()
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
