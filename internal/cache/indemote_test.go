package cache

import (
	"strconv"
	"strings"
	"testing"
)

// testMsgEstimator is the repo-standard chars/4 token estimator (TokensOf),
// applied to the whole message: content plus the role and tool-call-id
// overhead the wire carries. It mirrors cmd/cortex's estTurnTokens per message
// so the policy's drain math is tested against the same estimate the session
// will use.
func testMsgEstimator(m Msg) int {
	return TokensOf(len(m.Content) + len(m.Role) + len(m.ToolCallID))
}

// mkTurn builds a turn: one user message, then n assistant/tool pairs. Each
// tool result is sizeChars long (the accumulated spill) and tagged with its
// ordinal so tests can name a specific result. Returns the messages and, per
// tool result, its transcript index.
func mkTurn(n, sizeChars int, tool string) ([]Msg, []int) {
	msgs := []Msg{{Role: "user", Content: "do the thing"}}
	var toolIdx []int
	for i := 0; i < n; i++ {
		msgs = append(msgs,
			Msg{Role: "assistant", Content: "calling " + tool, ToolName: tool},
			Msg{Role: "tool", Content: strings.Repeat("x", sizeChars) + "#" + strconv.Itoa(i), ToolCallID: "call_" + strconv.Itoa(i), ToolName: tool},
		)
		toolIdx = append(toolIdx, len(msgs)-1)
	}
	return msgs, toolIdx
}

func TestPlanInTurnDemotion(t *testing.T) {
	const sessionID = "20260701-143210"

	t.Run("under the high watermark: nothing changes", func(t *testing.T) {
		// 4 results of 1000 chars = 1000 tokens + overhead; high watermark is
		// 5000 tokens, so the turn is well under budget.
		msgs, _ := mkTurn(4, 1000, "read_file")
		policy := InTurnDemotionPolicy{HighWM: 5000, LowWM: 3000, KeepRecent: 2, SessionID: sessionID}
		stubbed := PlanInTurnDemotion(msgs, testMsgEstimator, policy)
		if len(stubbed) != 0 {
			t.Errorf("stubbed %d indices, want 0 (under budget)", len(stubbed))
		}
	})

	t.Run("exactly at the high watermark: nothing changes", func(t *testing.T) {
		// The invariant is "> high watermark fires"; at exactly highWM the turn
		// is still within budget. Size the content so the estimate lands just
		// under highWM.
		msgs, _ := mkTurn(4, 1000, "read_file")
		// highWM chosen ABOVE the actual estimate, lowWM far below.
		policy := InTurnDemotionPolicy{HighWM: 10_000, LowWM: 100, KeepRecent: 1, SessionID: sessionID}
		total := 0
		for _, m := range msgs {
			total += testMsgEstimator(m)
		}
		if total > 10_000 {
			t.Fatalf("fixture estimate %d exceeds the chosen highWM 10000; pick a smaller fixture", total)
		}
		stubbed := PlanInTurnDemotion(msgs, testMsgEstimator, policy)
		if len(stubbed) != 0 {
			t.Errorf("stubbed %d indices, want 0 (under budget, est=%d)", len(stubbed), total)
		}
	})

	t.Run("over the high watermark: oldest stubbed first, keepRecent stay verbatim, drains to lowWM", func(t *testing.T) {
		// 8 results of 4000 chars ≈ 8000 tokens of spill; highWM 5000 fires,
		// lowWM 2000 is the drain target, keepRecent 2 keeps the newest two.
		msgs, toolIdx := mkTurn(8, 4000, "bash")
		policy := InTurnDemotionPolicy{HighWM: 5000, LowWM: 2000, KeepRecent: 2, SessionID: sessionID}
		stubbed := PlanInTurnDemotion(msgs, testMsgEstimator, policy)

		// The oldest results must be stubbed first: stubbed must be a
		// strictly-increasing prefix of the tool-result indices.
		if len(stubbed) == 0 {
			t.Fatal("no results stubbed, want some (turn is over the high watermark)")
		}
		for i, idx := range stubbed {
			if idx != toolIdx[i] {
				t.Errorf("stubbed[%d] = %d, want the %d-th oldest tool result at index %d", i, idx, i, toolIdx[i])
			}
		}

		// The most recent keepRecent tool results must NOT be stubbed.
		stubSet := map[int]bool{}
		for _, idx := range stubbed {
			stubSet[idx] = true
		}
		for k := 0; k < 2; k++ {
			idx := toolIdx[len(toolIdx)-1-k]
			if stubSet[idx] {
				t.Errorf("recent tool result at index %d was stubbed, but the last keepRecent must stay verbatim", idx)
			}
		}

		// The drain stops at whichever comes first: reaching the low watermark,
		// or exhausting every stubbable (older-than-keepRecent) result. Assert
		// that: the post-stub estimate is at or under lowWM, OR the stubbed set
		// is exactly every stubbable candidate (nothing left to stub).
		apply := applyStubs(msgs, stubbed, testMsgEstimator, policy)
		total := 0
		for _, m := range apply {
			total += testMsgEstimator(m)
		}
		stubbable := len(toolIdx) - 2 // older than keepRecent
		if total > policy.LowWM && len(stubbed) != stubbable {
			t.Errorf("post-stub estimate %d exceeds lowWM %d and only %d of %d stubbable results were used; the drain should reach lowWM when enough results exist", total, policy.LowWM, len(stubbed), stubbable)
		}
	})

	t.Run("enough large results: drain reaches the low watermark", func(t *testing.T) {
		// 12 results of 16000 chars = 4000 tokens each, ~48000 total — far over
		// the high watermark. keepRecent 2 keeps the two newest verbatim, so the
		// irreducible floor (user + 12 assistants + 10 one-line stubs + 2
		// verbatim 4000-token results) is ~8.3k tokens. lowWM is set ABOVE that
		// floor (9000) so the drain provably lands at or under it: with 10
		// candidates each shrinking ~4000 tokens, the estimate crosses 9000 well
		// before all candidates are used — the clean "reached the low watermark"
		// case.
		msgs, _ := mkTurn(12, 16000, "bash")
		policy := InTurnDemotionPolicy{HighWM: 20000, LowWM: 9000, KeepRecent: 2, SessionID: sessionID}
		stubbed := PlanInTurnDemotion(msgs, testMsgEstimator, policy)
		if len(stubbed) == 0 {
			t.Fatal("nothing stubbed; the turn is far over the high watermark")
		}
		// Oldest-first prefix of the tool indices.
		toolIdx := make([]int, 0, 12)
		for i, m := range msgs {
			if m.Role == "tool" {
				toolIdx = append(toolIdx, i)
			}
		}
		for i, idx := range stubbed {
			if idx != toolIdx[i] {
				t.Fatalf("stubbed[%d]=%d, want %d (oldest first)", i, idx, toolIdx[i])
			}
		}
		apply := applyStubs(msgs, stubbed, testMsgEstimator, policy)
		total := 0
		for _, m := range apply {
			total += testMsgEstimator(m)
		}
		if total > policy.LowWM {
			t.Errorf("post-stub estimate %d exceeds lowWM %d; with this many large results the drain must reach the low watermark", total, policy.LowWM)
		}
		// The keepRecent region must not have been touched.
		stubSet := map[int]bool{}
		for _, idx := range stubbed {
			stubSet[idx] = true
		}
		for k := 0; k < 2; k++ {
			if stubSet[toolIdx[len(toolIdx)-1-k]] {
				t.Errorf("a keepRecent result was stubbed")
			}
		}
	})

	t.Run("user and assistant messages are never touched", func(t *testing.T) {
		msgs, _ := mkTurn(10, 4000, "bash")
		policy := InTurnDemotionPolicy{HighWM: 5000, LowWM: 1000, KeepRecent: 3, SessionID: sessionID}
		stubbed := PlanInTurnDemotion(msgs, testMsgEstimator, policy)
		for _, idx := range stubbed {
			if msgs[idx].Role != "tool" {
				t.Errorf("stubbed index %d is role %q, want only role:tool", idx, msgs[idx].Role)
			}
		}
	})

	t.Run("not enough results to stub: returns nil even when over budget", func(t *testing.T) {
		// Only 2 tool results and keepRecent 2 → no candidate is old enough,
		// so nothing can be stubbed even though the turn is over budget.
		msgs, _ := mkTurn(2, 100000, "bash")
		policy := InTurnDemotionPolicy{HighWM: 5000, LowWM: 1000, KeepRecent: 2, SessionID: sessionID}
		stubbed := PlanInTurnDemotion(msgs, testMsgEstimator, policy)
		if len(stubbed) != 0 {
			t.Errorf("stubbed %d indices, want 0 (nothing older than keepRecent)", len(stubbed))
		}
	})
}

// applyStubs produces the post-stub message list the way cmd will: swap the
// Content of each stubbed index for a short stub line, leaving Role/ToolCallID
// intact. It shares the policy so the stub content (and thus the estimate)
// matches what PlanInTurnDemotion assumed when it drained.
func applyStubs(msgs []Msg, stubbed []int, estimator func(Msg) int, policy InTurnDemotionPolicy) []Msg {
	out := make([]Msg, len(msgs))
	copy(out, msgs)
	for _, i := range stubbed {
		out[i].Content = InTurnStubContent(msgs[i].ToolName, 0, false, "", citationFor(msgs, i, policy.SessionID))
	}
	return out
}

func TestInTurnStubContent(t *testing.T) {
	citation := "@session/20260701-143210#m5-6" // single message at index 5, half-open (the real stub shape)

	t.Run("ok result with citation", func(t *testing.T) {
		got := InTurnStubContent("bash(go test ./...)", 42, true, "", citation)
		want := "[demoted: bash(go test ./...) → 42 lines, ok. recall " + citation + " for the full output]"
		if got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})

	t.Run("error result carries the first error line", func(t *testing.T) {
		got := InTurnStubContent("read_file(go.mod)", 7, false, "Error: file not found", citation)
		want := "[demoted: read_file(go.mod) → 7 lines, error: Error: file not found. recall " + citation + " for the full output]"
		if got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})

	t.Run("no citation (no session id) omits the recall clause", func(t *testing.T) {
		got := InTurnStubContent("bash", 1, true, "", "")
		want := "[demoted: bash → 1 lines, ok]"
		if got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})

	t.Run("error with no first line still reads as error", func(t *testing.T) {
		got := InTurnStubContent("bash", 3, false, "", citation)
		want := "[demoted: bash → 3 lines, error. recall " + citation + " for the full output]"
		if got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})
}

// citationFor is tested directly: the stub's citation must name the message's
// transcript index (0-based) as a single-message HALF-OPEN range
// #m<i>-<i+1> so recall resolves it to that one message (a bare #m<i>-<i> is
// an empty range Recall rejects as out-of-range).
func TestCitationFor(t *testing.T) {
	msgs := []Msg{
		{Role: "user", Content: "q"},
		{Role: "tool", Content: "r0", ToolCallID: "c0"},
		{Role: "tool", Content: "r1", ToolCallID: "c1"},
	}
	if got := citationFor(msgs, 1, "abc"); got != "@session/abc#m1-2" {
		t.Errorf("citationFor(msgs,1,\"abc\") = %q, want @session/abc#m1-2 (single message index 1, half-open)", got)
	}
	if got := citationFor(msgs, 2, "abc"); got != "@session/abc#m2-3" {
		t.Errorf("citationFor(msgs,2,\"abc\") = %q, want @session/abc#m2-3 (single message index 2, half-open)", got)
	}
	if got := citationFor(msgs, 2, ""); got != "" {
		t.Errorf("citationFor with empty session id = %q, want empty", got)
	}
}
