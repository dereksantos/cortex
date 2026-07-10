package main

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"github.com/dereksantos/cortex/internal/cache"
)

// replayWorkingSet rebuilds the demotion state from a resumed transcript.
// turns[i] is the 1-based ordinal stamped on msgs[i] when it was written (0 =
// seed/system). A well-formed history is an unstamped prefix followed by
// consecutive ordinals K,K+1,… — replayed into exact spans (K need not be 1:
// a transcript Compact starts continues the session's numbering). Anything
// else (a legacy unstamped file, a gap) falls back to keeping the whole
// history hydrated — base = len(msgs), so wireMessages sends it all as
// pre-turn prefix and only new turns demote.
// Returns the working set and the highest ordinal seen, so the session can
// continue numbering (cs.turns) without colliding with existing stamps.
func (cs *CortexSession) replayWorkingSet(msgs []Message, turns []int) (*cache.WorkingSet, int) {
	if len(msgs) == 0 {
		return cs.newWorkingSet(0), 0
	}

	// Find base = index of the first non-zero stamp
	base := 0
	for base = range turns {
		if turns[base] != 0 {
			break
		}
	}
	if turns[base] == 0 {
		// All zeros (no stamps at all)
		return cs.newWorkingSet(len(msgs)), 0
	}

	// Validate in ONE pass before building anything: from base on, stamps must
	// be non-decreasing, increase only by exactly 1, and never return to zero.
	valid := true
	for i := base + 1; i < len(turns); i++ {
		if turns[i] == 0 {
			// Zero stamp after base is invalid
			valid = false
			break
		}
		if turns[i] < turns[i-1] {
			// Decreasing stamp is invalid
			valid = false
			break
		}
		if turns[i] > turns[i-1]+1 {
			// Gap (increase by more than 1) is invalid
			valid = false
			break
		}
	}

	if !valid {
		// Invalid sequence, fallback to keeping everything hydrated
		maxStamp := 0
		for _, t := range turns {
			if t > maxStamp {
				maxStamp = t
			}
		}
		return cs.newWorkingSet(len(msgs)), maxStamp
	}

	// Valid sequence: build the working set by grouping consecutive equal ordinals
	ws := cs.newWorkingSet(base)
	var lastOrdinal int

	// Walk the stamped region grouping consecutive equal ordinals
	start := base
	for i := base; i < len(turns); i++ {
		lastOrdinal = turns[i]
		// Check if next element is different or we're at the end
		if i+1 < len(turns) && turns[i+1] != turns[i] {
			// End of current group
			ws.AddTurn(cache.TurnSpan{
				Start:  start,
				End:    i + 1,
				Tokens: estTurnTokens(msgs[start : i+1]),
			})
			start = i + 1
		}
	}

	// Add the last group if we haven't added it yet
	if start < len(turns) {
		ws.AddTurn(cache.TurnSpan{
			Start:  start,
			End:    len(turns),
			Tokens: estTurnTokens(msgs[start:]),
		})
	}

	return ws, lastOrdinal
}

// outlineHeader labels zone A for the model: demoted turns it can no longer
// see verbatim, with citations into the session transcript.
const outlineHeader = "Session so far — older turns, demoted to this outline. Their raw messages are retrievable: call recall with a turn's @session/… citation. If something you need is not in the outline text, recall the likeliest turn before concluding it is unavailable."

// outlineFoldGoal directs the summarizer when the outline zone outgrows its
// budget. Citations MUST survive the fold — they are what keeps demotion
// lossless (recall resolves them).
const outlineFoldGoal = "Fold these demoted-turn outline entries into one compact digest. For every turn keep a short clause saying what it was about (its topic and any fact it recorded — enough that a reader knows which turn to recall), followed by its [@session/…#m…-…] citation verbatim — never drop, merge, or rewrite the coordinates. Compress by shortening clauses, not by omitting turns."

// foldSummarize is the seam tests stub (mirrors compactSummarize in session.go).
var foldSummarize = func(ctx context.Context, cs *CortexSession, content string, window int) (string, bool, error) {
	return cs.SummarizeText(ctx, content, outlineFoldGoal, window)
}

// renderOutlineBlock assembles zone A: header, any folded digest, then the
// live entries.
func (cs *CortexSession) renderOutlineBlock() string {
	parts := []string{outlineHeader}
	if cs.outlineFolded != "" {
		parts = append(parts, cs.outlineFolded)
	}
	if len(cs.outline) > 0 {
		parts = append(parts, cache.RenderOutline(cs.outline))
	}
	return strings.Join(parts, "\n\n")
}

// foldOutlineIfNeeded folds the oldest half of the outline entries into the
// digest when the zone exceeds its budget (window/8). Rare by construction; a
// summarizer failure just skips the fold — the outline stays big but correct.
func (cs *CortexSession) foldOutlineIfNeeded(ctx context.Context) {
	if len(cs.outline) < 2 {
		return
	}
	if cache.TokensOf(len(cs.renderOutlineBlock())) <= cs.windowSize()/8 {
		return
	}
	n := len(cs.outline) / 2
	content := cs.outlineFolded
	if content != "" {
		content += "\n\n"
	}
	content += cache.RenderOutline(cs.outline[:n])
	digest, _, err := foldSummarize(ctx, cs, content, cs.windowSize()/8)
	if err != nil || strings.TrimSpace(digest) == "" {
		return
	}
	cs.outlineFolded = restoreCitations(content, strings.TrimSpace(digest))
	cs.outline = append([]cache.OutlineEntry(nil), cs.outline[n:]...)
}

// foldCitationRe matches the transcript coordinates outline entries carry.
var foldCitationRe = regexp.MustCompile(`@session/[A-Za-z0-9-]+#m\d+-\d+`)

// restoreCitations enforces the fold's losslessness invariant mechanically:
// any citation present in the folded material but missing from the digest is
// appended, so demoted turns stay reachable via recall no matter what the
// summarizer did (the fold goal asks it to keep them; this guarantees it).
func restoreCitations(content, digest string) string {
	var missing []string
	seen := map[string]bool{}
	for _, c := range foldCitationRe.FindAllString(content, -1) {
		if !seen[c] && !strings.Contains(digest, c) {
			missing = append(missing, "["+c+"]")
		}
		seen[c] = true
	}
	if len(missing) == 0 {
		return digest
	}
	return digest + "\nOther folded turns (recall for detail): " + strings.Join(missing, " ")
}

// turnOutlineEntry converts a completed turn (slice of messages) into a
// deterministic outline entry for demotion.
func turnOutlineEntry(turn int, span cache.TurnSpan, msgs []Message, sessionID string) cache.OutlineEntry {
	// User: msgs[0] is the real user input; later RoleUser messages are harness injections
	userContent := ""
	if len(msgs) > 0 && msgs[0].Role == "user" {
		userContent = msgs[0].Content
	}
	if r := []rune(userContent); len(r) > outlineUserCap {
		userContent = string(r[:outlineUserCap]) + "… (truncated; recall the citation below for the rest)"
	}

	// Build map from ToolCallID to ok/err by scanning RoleTool messages
	resultMap := make(map[string]bool) // true = ok, false = err
	for _, msg := range msgs {
		if msg.Role == "tool" {
			resultMap[msg.ToolCallID] = !strings.HasPrefix(msg.Content, "Error:")
		}
	}

	// Actions: walk assistant messages in order, collect tool call labels
	var actions []string
	for _, msg := range msgs {
		if msg.Role == "assistant" {
			for _, call := range msg.ToolCalls {
				label := call.ActivityLabel()
				ok, exists := resultMap[call.ID]
				if !exists {
					ok = true // default to ok when no result message found
				}
				if ok {
					label += " [ok]"
				} else {
					label += " [err]"
				}
				actions = append(actions, label)
			}
		}
	}

	// ReplyHead: first line of the LAST assistant message with no tool calls and non-empty content
	var replyHead string
	for i := len(msgs) - 1; i >= 0; i-- {
		msg := msgs[i]
		if msg.Role == "assistant" && len(msg.ToolCalls) == 0 && strings.TrimSpace(msg.Content) != "" {
			replyHead = strings.TrimSpace(msg.Content)
			if nl := strings.IndexByte(replyHead, '\n'); nl >= 0 {
				replyHead = replyHead[:nl]
			}
			break
		}
	}

	// Citation: deterministic transcript coordinate
	citation := ""
	if sessionID != "" {
		citation = fmt.Sprintf("@session/%s#m%d-%d", sessionID, span.Start, span.End)
	}

	return cache.OutlineEntry{
		Turn:      turn,
		User:      userContent,
		Actions:   actions,
		ReplyHead: replyHead,
		Citation:  citation,
	}
}

// estTurnTokens estimates the token size of a turn's messages.
// It sums len(Content) for each message plus len(Function.Name)+len(Function.Arguments)
// for each ToolCall, then converts to tokens using cache.TokensOf.
func estTurnTokens(msgs []Message) int {
	sum := 0
	for _, msg := range msgs {
		sum += len(msg.Content)
		for _, call := range msg.ToolCalls {
			sum += len(call.Function.Name) + len(call.Function.Arguments)
		}
	}
	return cache.TokensOf(sum)
}

// outlineUserCap is the maximum runes for a demoted user message to stay verbatim.
// Beyond this, the outline keeps the head and the citation covers the rest.
const outlineUserCap = 500
