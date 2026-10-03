package cache

import (
	"strconv"
	"strings"
)

// indemote.go — in-turn demotion, the message-grained sibling of turn demotion
// (WorkingSet.DemoteBatch). Within one turn nothing demotes until the turn
// ends, so a long turn's accumulated tool results can overflow the window
// before the next send (issue #171: one turn grew to ~131k prompt tokens
// against a 131072 window, and the next request failed). In-turn demotion
// fixes the SAME problem at message granularity: before each send, if the
// current turn's messages are over the in-turn budget, the OLDEST tool results
// are replaced by one-line stubs that carry a citation into the session
// transcript, so recall(citation) re-fetches the original verbatim.
//
// This file is deliberately pure: a policy value plus two functions over a
// minimal Msg shape (Role, Content, ToolCallID, ToolName), so the selection
// logic is testable without a model and without importing the session's
// Message type. The caller (cmd/cortex) supplies the token estimator (the repo
// already estimates chars/4, TokensOf) and builds the actual stub content with
// InTurnStubContent. Only the wire-side message list is ever mutated; the
// transcript (the lossless record) is never touched, so recall and resume keep
// seeing the original content.

// InTurnStubPrefix is the fixed prefix every in-turn demotion stub's content
// carries. PlanInTurnDemotion skips tool results whose Content already starts
// with it, so a message an earlier send stubbed is never stubbed again (a
// re-stub of the stub text would read it as a fresh one-line result and lose
// the original's line count and error status — wrong information to the model
// for almost no token savings).
const InTurnStubPrefix = "[demoted:"

// InTurnDemotionPolicy is the in-turn demotion budget. HighWM/LowWM are the
// hydrated-tail watermarks (the same values turn demotion uses — the high
// watermark fires, the low watermark is the drain target), KeepRecent is how
// many of the turn's most-recent tool results stay verbatim (default 6), and
// SessionID is the session id the stub's recall citation names.
type InTurnDemotionPolicy struct {
	HighWM     int
	LowWM      int
	KeepRecent int
	SessionID  string
}

// Msg is the minimal message shape the in-turn policy reasons over. The caller
// maps its own Message type into this (Role/Content/ToolCallID verbatim,
// ToolName from the tool call the result answers) and back. Keeping it in this
// package — rather than importing cmd/cortex's Message — is what lets the
// selection logic stay a pure, stdlib-only function here.
type Msg struct {
	Role       string
	Content    string
	ToolCallID string
	ToolName   string
}

// PlanInTurnDemotion returns the indices (into msgs) of the tool results that
// should be stubbed, in oldest-first order. Empty (nil) means the turn is
// under the high watermark and nothing changes — byte for byte, as required.
//
// It never stubs:
//   - any message that is not role:"tool" (user messages — the first real
//     input and any harness-injected nudge — and assistant messages, whose
//     text and tool calls are the model's own reasoning), or
//   - the most recent policy.KeepRecent tool results, or
//   - a tool result that an earlier send already stubbed (Content already
//     carries InTurnStubPrefix) — re-stubbing the stub text would read it as
//     a fresh one-line result (wrong line count and error status) while
//     saving almost no tokens. Stubs still count toward the turn's total,
//     so they contribute to the over-budget test.
//
// It stubs tool results oldest-first until the remaining token estimate (after
// each stub, whose content shrinks to the short stub line) is at or under
// LowWM — or until no more stubbable tool results remain. The estimate is
// recomputed each step, so the drain is measured against the real post-stub
// size, not the pre-stub size.
func PlanInTurnDemotion(msgs []Msg, estimator func(Msg) int, policy InTurnDemotionPolicy) []int {
	if policy.HighWM <= 0 || policy.LowWM <= 0 {
		return nil
	}
	// Collect the stubbable tool-result indices, oldest-first. The last
	// KeepRecent are excluded (they stay verbatim); a message an earlier send
	// already stubbed is excluded too (see InTurnStubPrefix). Everything else
	// is a candidate.
	var candidates []int
	for i, m := range msgs {
		if m.Role != "tool" {
			continue
		}
		if strings.HasPrefix(m.Content, InTurnStubPrefix) {
			continue // already stubbed — never re-stub the stub text
		}
		candidates = append(candidates, i)
	}
	if len(candidates) <= policy.KeepRecent {
		return nil // nothing old enough to stub
	}
	candidates = candidates[:len(candidates)-policy.KeepRecent]
	if len(candidates) == 0 {
		return nil
	}

	total := 0
	for _, m := range msgs {
		total += estimator(m)
	}
	if total <= policy.HighWM {
		return nil // under budget: byte-for-byte unchanged
	}

	stubbed := []int{}
	// Stub oldest-first, re-measuring after each, until at or under LowWM or
	// no candidates remain. A stub shrinks its message to the short stub line,
	// so we measure the post-stub size, not the pre-stub size.
	for _, i := range candidates {
		if total <= policy.LowWM {
			break
		}
		// Estimate the stub size: the same role/toolcallid overhead plus the
		// short stub content in place of the original.
		stubContent := InTurnStubContent(msgs[i].ToolName, 0, false, "", citationFor(msgs, i, policy.SessionID))
		total += estimator(Msg{Role: "tool", Content: stubContent, ToolCallID: msgs[i].ToolCallID, ToolName: msgs[i].ToolName})
		total -= estimator(msgs[i])
		stubbed = append(stubbed, i)
	}
	return stubbed
}

// citationFor builds the transcript coordinate a stub's recall citation uses:
// the message's index i in the session transcript, as a single-message
// HALF-OPEN range #m<i>-<i+1> — the same coordinate space turn-outline
// citations use (cmd/cortex/demote.go: a turn span is [Start, End)) and the
// same shape Recall resolves (loadTranscript → msgs[start:end]). A bare
// #m<i>-<i> would be an empty range Recall rejects as out-of-range, so the
// single message at index i must be the half-open [i, i+1).
func citationFor(msgs []Msg, i int, sessionID string) string {
	if sessionID == "" {
		return ""
	}
	return "@session/" + sessionID + "#m" + strconv.Itoa(i) + "-" + strconv.Itoa(i+1)
}

// InTurnStubContent renders the one-line stub that replaces a demoted tool
// result's content. tool is the tool name, sizeLines the original result's
// line count, ok whether it succeeded, firstErrLine the result's first line
// (present only when !ok), and citation the @session/<id>#m<i>-<i+1>
// coordinate (the half-open single-message range Recall resolves) that
// resolves to the original. The shape is fixed by the issue:
//
//	[demoted: <tool>(<short args>) → <N> lines, <ok|error><: first error line, if any>. recall @session/<id>#m<i>-<i+1> for the full output]
//
// "<short args>" is the caller's job (it has the full ToolCall to label); this
// helper takes the label (or "" for a bare name) and the rest of the line.
func InTurnStubContent(toolLabel string, sizeLines int, ok bool, firstErrLine, citation string) string {
	status := "ok"
	if !ok {
		status = "error"
	}
	b := InTurnStubPrefix + " " + toolLabel + " → " + strconv.Itoa(sizeLines) + " lines, " + status
	if !ok && firstErrLine != "" {
		b += ": " + firstErrLine
	}
	if citation != "" {
		b += ". recall " + citation + " for the full output"
	}
	b += "]"
	return b
}

// WholePromptBudget is the whole-prompt in-turn demotion budget (issue #180).
// HighWM/LowWM are the same hydrated-tail watermarks as InTurnDemotionPolicy
// (the high watermark fires, the low watermark is the drain target); the
// difference is what is measured against them: the ENTIRE prompt (prefix +
// hydrated tail + the current turn), not the current turn alone.
type WholePromptBudget struct {
	HighWM int
	LowWM  int
}

// PlanWholePromptStubbing returns the indices (into msgs) of the tool results
// that should be stubbed, in oldest-first order. Empty (nil) means the prompt
// is under the high watermark and nothing changes — byte for byte, as
// required.
//
// Issue #180: PlanInTurnDemotion budgets the CURRENT TURN only, which has two
// gaps. (1) On resume, the previous turn comes back verbatim in the hydrated
// tail — DemoteBatch never demotes the most recent turn — and nothing ever
// shrinks it before the next turn's first send. (2) A turn that STARTS with a
// large tail in the prompt can grow by up to another high-watermark's worth
// before the turn-only budget fires, which is past the window. This plan
// closes both: it fires from the WHOLE prompt's estimated size (prefix + tail
// + current turn) against HighWM, and drains oldest-first in two phases — the
// current turn's stubbable tool results FIRST (keeping #172's selection order),
// then the PREVIOUS turn's — until the projected whole-prompt estimate is at
// or under LowWM or no candidates remain.
//
// msgs is the full message list; [previousStart, previousEnd) is the
// previous turn's span (the most recent completed turn, the tail's newest
// span) and [previousEnd, len(msgs)) the current turn; anything before
// previousStart (system messages, outline block, older demoted turns) is
// counted in the whole-prompt total but never stubbed. An empty previous
// span (previousStart == previousEnd, i.e. no previous turn — a fresh
// session) degrades exactly to a turn-only plan over msgs.
//
// It never stubs, per SPAN:
//   - any message that is not role:"tool" (user messages and assistant
//     messages stay verbatim, as in #172), or
//   - the most recent keepRecent tool results of THAT span (the carve-out is
//     per span, not global — the previous turn's newest results are protected
//     independently of the current turn's), or
//   - a tool result that an earlier send already stubbed (Content already
//     carries InTurnStubPrefix). Stubs still count toward the total, so they
//     contribute to the over-budget test.
//
// The estimate is recomputed after each stub (its content shrinks to the
// short stub line), so the drain is measured against the real post-stub
// size, not the pre-stub size.
func PlanWholePromptStubbing(msgs []Msg, estimator func(Msg) int, budget WholePromptBudget, keepRecent, previousStart, previousEnd int) []int {
	if budget.HighWM <= 0 || budget.LowWM <= 0 || len(msgs) == 0 {
		return nil
	}
	// Clamp the spans into the message list.
	if previousEnd > len(msgs) {
		previousEnd = len(msgs)
	}
	if previousStart < 0 {
		previousStart = 0
	}
	if previousStart > previousEnd {
		previousStart, previousEnd = previousEnd, previousStart
	}
	currentStart := previousEnd

	// The whole-prompt total: every message, including the prefix before
	// previousStart (system, outline, older demoted turns).
	total := 0
	for _, m := range msgs {
		total += estimator(m)
	}
	if total <= budget.HighWM {
		return nil // whole prompt under budget: byte-for-byte unchanged
	}

	// Phase 1: the current turn's stubbable tool results, oldest-first, with
	// the current turn's own keepRecent carve-out (its newest results stay
	// verbatim).
	var candidates []int
	for i := currentStart; i < len(msgs); i++ {
		if msgs[i].Role != "tool" {
			continue
		}
		if strings.HasPrefix(msgs[i].Content, InTurnStubPrefix) {
			continue // already stubbed — never re-stub the stub text
		}
		candidates = append(candidates, i)
	}
	if keepRecent < len(candidates) {
		candidates = candidates[:len(candidates)-keepRecent]
	} else {
		candidates = nil
	}

	// Phase 2: the previous turn's, with the previous turn's own carve-out.
	// This keeps #172's selection order: the current turn's oldest results
	// always go first, the previous turn's only after.
	var prevCands []int
	for i := previousStart; i < previousEnd; i++ {
		if msgs[i].Role != "tool" {
			continue
		}
		if strings.HasPrefix(msgs[i].Content, InTurnStubPrefix) {
			continue
		}
		prevCands = append(prevCands, i)
	}
	if keepRecent < len(prevCands) {
		prevCands = prevCands[:len(prevCands)-keepRecent]
	} else {
		prevCands = nil
	}
	candidates = append(candidates, prevCands...)
	if len(candidates) == 0 {
		return nil // nothing old enough to stub (both spans within keepRecent)
	}

	stubbed := []int{}
	// Stub oldest-first, re-measuring after each, until at or under LowWM or
	// no candidates remain.
	for _, i := range candidates {
		if total <= budget.LowWM {
			break
		}
		// Estimate the stub size: the same role/tool-call-id overhead plus the
		// short stub content in place of the original. (SessionID is unknown
		// to the pure policy; the citation the real stub carries is a few
		// extra tokens, absorbed by the low-watermark margin.)
		stubContent := InTurnStubContent(msgs[i].ToolName, 0, false, "", "")
		total += estimator(Msg{Role: "tool", Content: stubContent, ToolCallID: msgs[i].ToolCallID, ToolName: msgs[i].ToolName})
		total -= estimator(msgs[i])
		stubbed = append(stubbed, i)
	}
	return stubbed
}
