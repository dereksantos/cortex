package main

// indemote.go — the session-side wiring for in-turn demotion (issue #171).
//
// The pure selection policy lives in internal/cache (indemote.go):
// cache.InTurnDemotionPolicy + cache.PlanInTurnDemotion decide WHICH tool
// results to stub given a message list and a token estimator;
// cache.InTurnStubContent renders the one-line stub. This file is the THIN
// adapter that binds that policy to a live CortexSession:
//
//   - turnInTurnBudget() resolves the policy for the current window — the
//     SAME high/low watermarks turn demotion uses (cs.Config's
//     tailHighWatermark/tailDrainWatermark), enabled by context.in_turn_demotion
//     (default on) and keepRecent (default 6).
//   - applyInTurnDemotion(req, turnStart) runs the policy over the CURRENT
//     turn's messages and, for each stubbed index, swaps req.Messages[i].Content
//     for the one-line stub in place. Role and ToolCallID are preserved (the
//     tool result still pairs with the assistant's tool call), and the
//     transcript is NEVER touched — writeTranscript already recorded the
//     original content, so recall(@session/<id>#m<i>-<i+1>) resolves the
//     stubbed message to its full original output (see Recall, tool_deps.go).
//
// turn.go wires the hook:
//
//	ts.BeforeSend = func(req *AgentRequest) { cs.applyInTurnDemotion(req, turnStart) }
//
// and the turn-end consumers (the outline entry, captureTurn) read the
// originals back through turnOriginalSpan.
//
// The wire-side swap is the ONLY mutation of cs.Request.Messages — the slice
// runLoop re-sends each round (passed by reference, appended to in place), so
// mutating it here shrinks the NEXT send, exactly what the BeforeSend seam
// (loop.go) asks for, byte-for-byte when under budget. The swap is lossless
// by design: applyInTurnDemotion records each stubbed message's ORIGINAL
// content in cs.inTurnOriginals (keyed by absolute index) before the swap,
// and the turn-end consumers (turn.go's outline entry and captureTurn) read
// the originals back through turnOriginalSpan — the transcript itself is
// never touched (writeTranscript already recorded the original content, so
// recall of the stub's citation returns the full output).

import (
	"fmt"
	"strings"

	"github.com/dereksantos/cortex/internal/cache"
)

// inTurnKeepRecent resolves the count of the turn's most-recent tool results
// that always stay verbatim — even when the turn is over budget, the newest
// few results are the ones the model is actively working from, so they are
// never the first to be stubbed. The value comes from context.in_turn_keep_recent
// (cmd/cortex/config.go's inTurnKeepRecent, default inTurnKeepRecentDefault).
func (cs *CortexSession) inTurnKeepRecent() int {
	return cs.Config.inTurnKeepRecent()
}

// turnInTurnBudget resolves the in-turn demotion policy for the current
// window. It returns nil when in-turn demotion is disabled
// (context.in_turn_demotion: false) — the caller then skips the policy entirely
// (nil = today's byte-for-byte behavior).
//
// HighWM/LowWM are the SAME watermarks turn demotion (turn-end DemoteBatch)
// uses, so in-turn demotion fires and drains on the same thresholds the
// turn-end path already reasons about: the high watermark (W/2) is the trigger,
// the low watermark (W/3) the drain target. SessionID names the current
// session, which the stub's recall citation carries.
func (cs *CortexSession) turnInTurnBudget() *cache.InTurnDemotionPolicy {
	if !cs.Config.inTurnDemotionEnabled() {
		return nil
	}
	w := cs.windowSize()
	return &cache.InTurnDemotionPolicy{
		HighWM:     cs.Config.tailHighWatermark(w),
		LowWM:      cs.Config.tailDrainWatermark(w),
		KeepRecent: cs.inTurnKeepRecent(),
		SessionID:  cs.SessionID,
	}
}

// inTurnMsg maps one of the current turn's Messages to the cache.Msg shape the
// pure policy reasons over. The policy selects on Role/Content/ToolCallID only;
// ToolName is set to the role-appropriate placeholder (it is not the stub's
// label — that comes from activityLabelForCallID, resolved ONCE per stubbed
// index, not per message — so we avoid an O(n) scan over the whole message
// list for every message on every send).
func inTurnMsg(m Message) cache.Msg {
	return cache.Msg{
		Role:       m.Role,
		Content:    m.Content,
		ToolCallID: m.ToolCallID,
		ToolName:   m.Role,
	}
}

// activityLabelForCallID renders the "tool(shortArgs)" label for the tool call
// matching callID — the same label the status row shows while the tool runs
// (ToolCall.ActivityLabel), so the stub reads consistently with the action
// line that announced the call. Empty string when there is no match.
func activityLabelForCallID(msgs []Message, callID string) string {
	if callID == "" {
		return ""
	}
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role != "assistant" {
			continue
		}
		for _, c := range msgs[i].ToolCalls {
			if c.ID == callID {
				return c.ActivityLabel()
			}
		}
	}
	return ""
}

// applyInTurnDemotion runs the in-turn policy over the current turn (the
// messages from turnStart on) and, for each stubbed index, replaces
// req.Messages[idx].Content with the one-line stub in place. It is the body of
// the BeforeSend hook wired by turn(). Under budget (or disabled) it is a no-op
// — the request goes out byte-for-byte as today.
//
// req is the live request slice runLoop re-sends each round; turning it into a
// stubbed copy in place is what shrinks the next send. Only Content changes;
// Role and ToolCallID are preserved so the tool result still pairs with the
// assistant's tool call on the wire. The transcript is never touched, so
// recall of the stub's citation returns the original output.
func (cs *CortexSession) applyInTurnDemotion(req *AgentRequest, turnStart int) {
	policy := cs.turnInTurnBudget()
	if policy == nil {
		return // in-turn demotion disabled → today's behavior, byte for byte
	}
	turn := req.Messages[turnStart:]
	if len(turn) == 0 {
		return
	}
	// Map the turn's messages to the pure-policy shape and estimate each with
	// the SAME chars/4 estimator turn-end demotion uses (estTurnTokens over a
	// single message), so in-turn and turn-end demotion fire on consistent
	// numbers.
	msgs := make([]cache.Msg, len(turn))
	for i, m := range turn {
		msgs[i] = inTurnMsg(m)
	}
	if cs.inTurnOriginals == nil {
		cs.inTurnOriginals = map[int]string{}
	}
	if cs.inTurnOrigTurn != turnStart {
		// A new turn starts: the previous turn's originals are no longer
		// needed (its turn-end consumers — the outline entry and the journal
		// capture — already read them, and its messages are now an
		// outline-eligible past turn). Drop them so a session of long turns
		// doesn't accumulate original content in memory.
		cs.inTurnOriginals = map[int]string{}
		cs.inTurnOrigTurn = turnStart
	}
	estimator := func(m cache.Msg) int {
		// Mirror estTurnTokens: a tool result carries the tool-call-id overhead
		// on the wire; assistant tool calls add name+args, but those are not in
		// Content — keep the estimate to Content + tool-call-id so the drain
		// math tracks what actually shrinks (the Content swap).
		return cache.TokensOf(len(m.Content) + len(m.ToolCallID))
	}
	stubbed := cache.PlanInTurnDemotion(msgs, estimator, *policy)
	for _, idx := range stubbed {
		abs := turnStart + idx
		m := &req.Messages[abs]
		label := activityLabelForCallID(req.Messages, m.ToolCallID)
		if _, seen := cs.inTurnOriginals[abs]; !seen {
			// Record the ORIGINAL content before the swap: turn-end consumers
			// (turn.go's outline entry and captureTurn) read this map instead of
			// the wire copy, so they see the original result, never the stub
			// (issue #171 item 5). The transcript already holds it — this is the
			// in-memory half, for the in-session turn only.
			cs.inTurnOriginals[abs] = m.Content
		}
		m.Content = cache.InTurnStubContent(label, countLines(m.Content),
			!isErrResult(m.Content), firstErrLine(m.Content), fmtCitation(cs.SessionID, abs))
	}
}

// turnOriginalSpan copies req.Messages[turnStart:] with the original content
// restored at every stubbed index (from cs.inTurnOriginals, recorded by
// applyInTurnDemotion before the wire swap). It is the view turn-end consumers
// need — the turn-end outline entry and captureTurn read the original tool
// results (an [err] label, the true content for journal artifacts), not the
// one-line wire stubs (issue #171 item 5). Under budget (or with demotion
// disabled) the copy is identical to the wire copy.
func (cs *CortexSession) turnOriginalSpan(req *AgentRequest, turnStart int) []Message {
	n := len(req.Messages) - turnStart
	if n == 0 {
		return nil
	}
	out := make([]Message, n)
	copy(out, req.Messages[turnStart:])
	for i := range out {
		abs := turnStart + i
		if orig, ok := cs.inTurnOriginals[abs]; ok {
			out[i].Content = orig
		}
	}
	return out
}

// fmtCitation builds the transcript coordinate for message index abs: a
// single-message half-open range #m<abs>-<abs+1>, the same space Recall
// resolves (tool_deps.go's loadTranscript → msgs[start:end]). The stub names
// the exact index the original content sits at in the current session.
func fmtCitation(sessionID string, abs int) string {
	if sessionID == "" {
		return ""
	}
	return fmt.Sprintf("@session/%s#m%d-%d", sessionID, abs, abs+1)
}

// countLines returns the line count of s for the stub's "<N> lines" clause.
func countLines(s string) int {
	if s == "" {
		return 0
	}
	n := 1
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			n++
		}
	}
	return n
}

// isErrResult reports whether s looks like a failed tool result —
// the dispatcher returns errors as "Error: <msg>" (coderDispatcher, loop.go),
// so the leading "Error:" prefix is the signal.
func isErrResult(s string) bool {
	return len(s) >= 7 && s[:6] == "Error:"
}

// firstErrLine returns the first line of a failed tool result, for the stub's
// "error: <first line>" clause. Empty for a non-error result.
func firstErrLine(s string) string {
	if !isErrResult(s) {
		return ""
	}
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	if len(s) > 80 {
		return s[:80] + "…"
	}
	return s
}
