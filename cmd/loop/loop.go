package main

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/dereksantos/cortex/internal/tools"
)

// loop.go is THE agent engine: one tool-iteration loop (`runLoop`) plus the two
// small seams every caller injects — a `Sender` (one model round-trip) and an
// `AgentDispatcher` (one tool call → observation). The coder turn and every
// subagent (study, …) run on this one function; the only variation is the seams
// they pass. See docs/engine-unification.md.
//
// The coder turn (`Turn`) runs on this engine; `runNavigator` is the last
// remaining second loop and is deleted (not refactored) when the study subagent
// lands (study-subagent.md phase 4). Phase 3 moves the engine + shared data
// types into internal/agent.

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
type Toolset struct {
	Tools       []Tool
	Dispatch    AgentDispatcher
	BeforeBatch func()
}

// Bounds are the independent ceilings; whichever trips first forces finalize.
// THREE mandatory caps — no request is EVER unbounded:
//   - MaxTokens: a finite per-request completion cap, stamped onto the request
//     (the single source of truth, subsuming the old per-payload output-cap
//     helper that this refactor deleted). The
//     primary runaway backstop — the one thing that holds when cancellation
//     fails (see the 2026-06-28 north runaway in docs/engine-unification.md).
//   - MaxIter: caps the rounds.
//   - ReadBudgetBytes: caps accumulated tool output (0 = unbounded, the coder).
type Bounds struct {
	MaxTokens       int
	MaxIter         int
	ReadBudgetBytes int
}

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
	PeakOutputTokens int    // max completion tokens on any single request
	MaxTokensClamped bool   // any request hit Bounds.MaxTokens (runaway tripwire)
	Iterations       int    // model rounds consumed
	StopReason       string // clean-finalize|max-iter|read-budget|no-progress|deadline|error
	FinalizeForced   bool   // answered because a bound dragged finalize out

	Outlines  int
	Greps     int
	Reads     int
	ToolErrs  int // observations that came back as a brief "Error: …"
	ReadBytes int // accumulated tool output (the bounded-ness axis)
}

var errNoChoices = errors.New("no choices in model response")

// noProgressNudge is injected one repeat short of the cap so a stuck model can
// change course before the guard breaks the loop. Engine-level (every caller),
// not a main-loop special case.
const noProgressNudge = "Harness note: that tool call was byte-identical to the previous one and produced the same result. Repeating it will not yield new information — try a different command or approach, or stop and report what you've found."

// finalizePrompt asks for the answer with tools withheld when a bound forced the
// loop to stop, so a budget-exhausted run still produces a grounded digest
// rather than nothing.
const finalizePrompt = "You've reached the limit for this turn. Stop calling tools and answer now from what you've already gathered; be concise."

// runLoop is THE engine. It iterates send → dispatch → re-send until the model
// answers with no tool calls (clean finalize) or a bound trips, then finalizes
// with tools withheld. The variation between callers is the Sender + Toolset
// (+ Progress); the loop core — XML recovery, no-progress guard, usage
// accounting, ctx-cancel, finalize — is identical for everyone.
//
// appendMsg records each message: the coder passes cs.Append (grows the live
// history and writes the transcript); a subagent passes a plain slice append on
// its own request. req is the caller's request, re-sent (grown) each round.
func runLoop(ctx context.Context, send Sender, req *AgentRequest, ts Toolset, b Bounds, p Progress, appendMsg func(Message)) (string, loopStats, error) {
	var stats loopStats
	req.Tools = ts.Tools
	if b.MaxTokens > 0 {
		req.MaxTokens = b.MaxTokens
	}

	var lastSig string
	var repeats int // consecutive batches identical to lastSig, including current
	stop := ""
	for i := 0; i < b.MaxIter; i++ {
		stats.Iterations = i + 1
		res, _, err := send.Send(ctx, req)
		if err != nil {
			stats.StopReason = "error"
			return "", stats, err
		}
		if res == nil || len(res.Choices) == 0 {
			stats.StopReason = "error"
			return "", stats, errNoChoices
		}
		accountUsage(&stats, res, req.MaxTokens)

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

		// No tool calls → the model answered. That prose IS the result.
		if len(msg.ToolCalls) == 0 {
			stats.StopReason = "clean-finalize"
			return strings.TrimSpace(msg.Content), stats, nil
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
		for _, call := range msg.ToolCalls {
			countTool(&stats, call.Function.Name)
			obs := ts.Dispatch.Dispatch(ctx, call)
			if strings.HasPrefix(obs, "Error:") {
				stats.ToolErrs++
			}
			stats.ReadBytes += len(obs)
			appendMsg(Message{Role: RoleTool, ToolCallID: call.ID, Content: obs})
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
	content, finalStats, err := finalizeLoop(ctx, send, req, &stats, appendMsg)
	// Restore the advertised tools: finalize withheld them, but the caller's
	// request (cs.Request for the coder) is long-lived and reused next turn.
	req.Tools = ts.Tools
	return content, finalStats, err
}

// finalizeLoop requests a final answer with the tool set withheld, so a model
// that ran out of budget (iterations, bytes, or stuck repeating) still produces
// an answer grounded in what it read rather than returning nothing.
func finalizeLoop(ctx context.Context, send Sender, req *AgentRequest, stats *loopStats, appendMsg func(Message)) (string, loopStats, error) {
	req.Tools = nil
	appendMsg(Message{Role: RoleUser, Content: finalizePrompt})
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
	return strings.TrimSpace(msg.Content), *stats, nil
}

// accountUsage folds one response's token usage into the run stats, tracking the
// live prompt fill, the per-request output peak, and whether any request hit the
// completion ceiling (the eval-time runaway tripwire).
func accountUsage(s *loopStats, res *AgentResponse, maxTokens int) {
	s.InputTokens += res.Usage.PromptTokens
	s.OutputTokens += res.Usage.CompletionTokens
	s.Cost += res.Usage.Cost
	s.LastPromptTokens = res.Usage.PromptTokens
	if res.Usage.CompletionTokens > s.PeakOutputTokens {
		s.PeakOutputTokens = res.Usage.CompletionTokens
	}
	if maxTokens > 0 && res.Usage.CompletionTokens >= maxTokens {
		s.MaxTokensClamped = true
	}
}

// countTool bumps the per-tool counter for the locate-then-read discriminator
// (study eval §5). The grep/outline names are forward-compatible with the study
// tools that land in study phases 1–2.
func countTool(s *loopStats, name string) {
	switch name {
	case tools.FunctionReadFile:
		s.Reads++
	case "grep":
		s.Greps++
	case "outline":
		s.Outlines++
	}
}

// progressLine renders a one-line breadcrumb for a tool call, shown live via the
// Progress sink so a blocking subagent isn't silent.
func progressLine(call ToolCall) string {
	return "  ▸ " + call.ActivityLabel()
}

// requestFor assembles a model request from a spec — the single build site where
// model/base/key/template-kwargs are set, and the single place a finite
// max_tokens is stamped (subsuming the deleted per-payload output-cap helper).
// maxTokens must
// be >0; it falls back to the role/default cap only when a caller passes 0, so no
// request path is ever unbounded. Used by every subagent caller (the coder reuses
// its long-lived cs.Request instead, which carries the same stamp from init).
func requestFor(spec ModelSpec, system, seed string, toolset []Tool, maxTokens int) *AgentRequest {
	if maxTokens <= 0 {
		maxTokens = spec.maxOut(defaultAgentMaxTokens)
	}
	return &AgentRequest{
		Model:              spec.Model,
		BaseURL:            spec.Endpoint,
		APIKey:             resolveKey(spec),
		ChatTemplateKwargs: spec.TemplateKwargs(),
		Temperature:        0,
		MaxTokens:          maxTokens,
		Tools:              toolset,
		Messages: []Message{
			{Role: RoleSystem, Content: system},
			{Role: RoleUser, Content: seed},
		},
	}
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
