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

// first80 truncates s to 80 runes for test diagnostics.
func first80(s string) string {
	r := []rune(s)
	if len(r) <= 80 {
		return s
	}
	return string(r[:80]) + "…"
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

// sumEstimates totals the estimator over msgs — the whole-prompt estimate the
// whole-prompt plan budgets against.
func sumEstimates(msgs []Msg) int {
	total := 0
	for _, m := range msgs {
		total += testMsgEstimator(m)
	}
	return total
}

// planWholePrompt is PlanWholePromptStubbing with the previous span empty
// (previousStart == previousEnd == 0), so the current turn is the ENTIRE
// message list. This is the "no previous turn" (fresh session) case: the
// plan degrades to a turn-only plan over msgs.
func planWholePrompt(msgs []Msg, budget WholePromptBudget, keepRecent int) []int {
	return PlanWholePromptStubbing(msgs, testMsgEstimator, budget, keepRecent, 0, 0, 0)
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

	t.Run("already-stubbed results are never re-stubbed, and still count toward the total", func(t *testing.T) {
		// Simulates a later send in the same turn: an earlier send already
		// stubbed the two oldest tool results (their Content is the stub an
		// earlier applyInTurnDemotion wrote). They must NOT be stubbed again —
		// re-stubbing the stub text would read it as a fresh one-line result
		// (countLines=1, isErr=false) and silently lose the original's line
		// count and "error: …" status — but they STILL count toward the total,
		// so the over-budget test fires and the next oldest VERBATIM results
		// are the ones stubbed.
		msgs, toolIdx := mkTurn(8, 4000, "bash")
		// The two oldest tool results are ALREADY stubs — exactly as a later
		// send in the same turn would see them after an earlier
		// applyInTurnDemotion. One carries the error status (a failed
		// "Error: …" result) so the "1 lines, ok" regression is detectable.
		msgs[toolIdx[0]].Content = InTurnStubContent("bash(go test)", 240, false, "Error: exit 1", citationFor(msgs, toolIdx[0], sessionID))
		msgs[toolIdx[1]].Content = InTurnStubContent("bash(go vet)", 12, true, "", citationFor(msgs, toolIdx[1], sessionID))
		policy := InTurnDemotionPolicy{HighWM: 5000, LowWM: 1000, KeepRecent: 2, SessionID: sessionID}
		stubbed := PlanInTurnDemotion(msgs, testMsgEstimator, policy)
		if len(stubbed) == 0 {
			t.Fatal("nothing stubbed; the turn is still over the high watermark (the stubs count toward the total)")
		}
		for _, idx := range stubbed {
			if idx == toolIdx[0] || idx == toolIdx[1] {
				t.Errorf("already-stubbed tool result at index %d was stubbed again; a stub must never be re-stubbed", idx)
			}
		}
		// The new stubs must be the next-oldest VERBATIM results (toolIdx[2],
		// toolIdx[3], … oldest-first, keepRecent 2 untouched).
		wantFrom := 2
		for i, idx := range stubbed {
			if idx != toolIdx[wantFrom+i] {
				t.Fatalf("stubbed[%d] = %d, want %d (the %d-th oldest VERBATIM result, oldest-first)", i, idx, toolIdx[wantFrom+i], wantFrom+i)
			}
		}
		// The first pass's stubs must be byte-for-byte unchanged — the model
		// keeps seeing "240 lines, error: …" (the original's count and status),
		// not a re-stubbed "1 lines, ok".
		firstPassStubs := []string{
			InTurnStubContent("bash(go test)", 240, false, "Error: exit 1", citationFor(msgs, toolIdx[0], sessionID)),
			InTurnStubContent("bash(go vet)", 12, true, "", citationFor(msgs, toolIdx[1], sessionID)),
		}
		for k, i := range []int{toolIdx[0], toolIdx[1]} {
			if msgs[i].Content != firstPassStubs[k] {
				t.Errorf("pre-existing stub at index %d was rewritten (re-stubbed from its own stub text): got %q, want %q", i, first80(msgs[i].Content), first80(firstPassStubs[k]))
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

// TestPlanWholePromptStubbing covers the issue #180 variant: the budget is the
// WHOLE prompt (fixed part + hydrated tail + current turn), and the hydrated
// tail's tool results become candidates once the current turn's are exhausted.
// Layout everywhere: [system | previous hydrated tail | current turn], where
// the previous hydrated tail is the newest demotion-immune spans (possibly
// several turns) and the current turn ends at len(msgs).
func TestPlanWholePromptStubbing(t *testing.T) {
	const (
		sessionID     = "20260701-143210"
		highWM, lowWM = 65536, 43690
		keepRecent    = 2
	)
	budget := WholePromptBudget{HighWM: highWM, LowWM: lowWM}
	system := Msg{Role: "system", Content: strings.Repeat("y", 4000)}

	t.Run("small session: under the high watermark, nothing stubbed", func(t *testing.T) {
		// The system message (index 0) is not part of the stubbable region:
		// in the session adapter it rides in the wire prefix (the fixed part).
		// The previous hydrated tail is [1, prevEnd); the current turn is
		// [prevEnd, len). fixed = 0 here — the wire prefix's tokens are the
		// caller's precomputed fixed, not a member of msgs.
		prev, _ := mkTurn(2, 1000, "read_file")
		cur, _ := mkTurn(2, 1000, "bash")
		msgs := append(append([]Msg{system}, prev...), cur...)
		prevEnd := 1 + len(prev)
		stubbed := PlanWholePromptStubbing(msgs, testMsgEstimator, budget, keepRecent, 1, prevEnd, 0)
		if len(stubbed) != 0 {
			t.Errorf("stubbed %d indices, want 0 (whole prompt under budget)", len(stubbed))
		}
	})

	t.Run("exactly at the high watermark: nothing stubbed", func(t *testing.T) {
		build := func(size int) []Msg {
			c, _ := mkTurn(1, size, "bash")
			return append([]Msg{system}, c...)
		}
		base := sumEstimates(build(0))
		// one result: content = size + "#0" = size+2 chars, est = (size+2)/4
		// total = base + (size+2)/4 = highWM → size = 4*(highWM-base) - 2
		// Adjust for floor division: use binary search for exactness.
		size := 4*(highWM-base) - 2
		for delta := 0; delta < 4; delta++ {
			if sumEstimates(build(size+delta)) == highWM {
				size += delta
				break
			}
		}
		if sumEstimates(build(size)) != highWM {
			t.Fatalf("fixture: cannot land exactly on highWM (base %d)", base)
		}
		msgs := build(size)
		if total := sumEstimates(msgs); total != highWM {
			t.Fatalf("fixture estimate %d != highWM %d (base %d size %d)", total, highWM, base, size)
		}
		stubbed := planWholePrompt(msgs, budget, keepRecent)
		if len(stubbed) != 0 {
			t.Errorf("stubbed %d at exactly highWM; want 0 (fires only when strictly over)", len(stubbed))
		}
	})

	t.Run("large tail: stubs earlier than turn-alone", func(t *testing.T) {
		// prev: 16×8000 (~32k tok), cur: 8×18000 (~36k tok), sys ~1k.
		// Whole: ~69k > highWM. Cur alone: ~36k < highWM. ✓
		// Drain: 6 cur stubs (~18k saved) + 12 prev stubs (~24k saved) =
		// ~42k saved → post ~27k < lowWM. ✓
		prev, prevIdx := mkTurn(16, 8000, "read_file")
		cur, curIdx := mkTurn(8, 18000, "bash")
		msgs := append(append([]Msg{system}, prev...), cur...)
		prevStart, prevEnd := 1, 1+len(prev)

		if n := len(PlanInTurnDemotion(cur, testMsgEstimator, InTurnDemotionPolicy{HighWM: highWM, LowWM: lowWM, KeepRecent: keepRecent, SessionID: sessionID})); n != 0 {
			t.Fatalf("fixture: cur alone stubbed %d; must be under highWM", n)
		}
		stubbed := PlanWholePromptStubbing(msgs, testMsgEstimator, budget, keepRecent, prevStart, prevEnd, 0)
		if len(stubbed) == 0 {
			t.Fatal("nothing stubbed; whole prompt over highWM, turn alone under")
		}
		// Order: cur oldest-first (minus keepRecent), then prev oldest-first.
		// curIdx is local to cur; offset by prevEnd for global index.
		curCands := make([]int, len(curIdx)-keepRecent)
		for i := range curCands {
			curCands[i] = prevEnd + curIdx[i]
		}
		prevCands := make([]int, len(prevIdx)-keepRecent)
		for i := range prevCands {
			prevCands[i] = prevStart + prevIdx[i]
		}
		wantOrder := append(append([]int{}, curCands...), prevCands...)
		for i := range stubbed {
			if i >= len(wantOrder) {
				t.Fatalf("stubbed %d, want ≤ %d", len(stubbed), len(wantOrder))
			}
			if stubbed[i] != wantOrder[i] {
				t.Fatalf("stubbed[%d]=%d want %d", i, stubbed[i], wantOrder[i])
			}
		}
		// keepRecent newest of cur must not be stubbed.
		stubSet := map[int]bool{}
		for _, idx := range stubbed {
			stubSet[idx] = true
		}
		for k := 1; k <= keepRecent; k++ {
			if stubSet[curIdx[len(curIdx)-k]] {
				t.Errorf("cur's %d-th newest stubbed; keepRecent must stay", k)
			}
		}
		apply := applyStubs(msgs, stubbed, testMsgEstimator, InTurnDemotionPolicy{SessionID: sessionID})
		if total := sumEstimates(apply); total > lowWM {
			t.Errorf("post-stub %d > lowWM %d", total, lowWM)
		}
	})

	t.Run("previous turn stubbed only after current turn's candidates", func(t *testing.T) {
		// prev: 16×20000 (~64k tok), cur: 3×20000 (~12k tok), sys ~1k.
		// Whole: ~77k > highWM. Cur candidates: 2 (keepRecent 1).
		// Drain: 2 cur stubs (~8k saved) + ~6 prev stubs (~24k saved) =
		// ~32k saved → post ~45k. Slightly over lowWM. Use 20000 chars.
		// Actually: 77k - 32k = 45k > 43690. Need more stubs.
		// 2 cur + 8 prev = ~40k saved → post ~37k < lowWM. ✓
		prev, prevIdx := mkTurn(16, 20000, "read_file")
		cur, curIdx := mkTurn(3, 20000, "bash")
		msgs := append(append([]Msg{{Role: "system", Content: strings.Repeat("y", 4000)}}, prev...), cur...)
		prevStart, prevEnd := 1, 1+len(prev)
		stubbed := PlanWholePromptStubbing(msgs, testMsgEstimator, budget, 1, prevStart, prevEnd, 0)
		if len(stubbed) == 0 {
			t.Fatal("nothing stubbed; whole prompt far over highWM")
		}
		// Order: cur's oldest stubbable first (keepRecent 1 excludes the
		// newest), then prev's oldest stubbable (keepRecent 1 excludes the
		// newest). curIdx is local to cur; offset by prevEnd for global.
		curCands := make([]int, len(curIdx)-1)
		for i := range curCands {
			curCands[i] = prevEnd + curIdx[i]
		}
		prevCands := make([]int, len(prevIdx)-1)
		for i := range prevCands {
			prevCands[i] = prevStart + prevIdx[i]
		}
		want := append(append([]int{}, curCands...), prevCands...)
		if len(stubbed) > len(want) {
			t.Fatalf("stubbed %d, want ≤ %d (keepRecent 1 per span)", len(stubbed), len(want))
		}
		for i, idx := range stubbed {
			if idx != want[i] {
				t.Fatalf("stubbed[%d]=%d want %d (cur first, then prev oldest-first)", i, idx, want[i])
			}
		}
		if len(stubbed) <= 2 {
			t.Fatalf("only %d stubbed (all cur); prev must follow", len(stubbed))
		}
		stubSet := map[int]bool{}
		for _, idx := range stubbed {
			stubSet[idx] = true
		}
		if stubSet[prevIdx[len(prevIdx)-1]] {
			t.Errorf("prev's newest (keepRecent) stubbed")
		}
		if stubSet[curIdx[2]] {
			t.Errorf("cur's newest (keepRecent) stubbed")
		}
	})

	t.Run("drain reaches the low watermark across both spans", func(t *testing.T) {
		prev, _ := mkTurn(16, 20000, "read_file")
		cur, _ := mkTurn(16, 20000, "bash")
		msgs := append(append([]Msg{{Role: "system", Content: "sys"}}, prev...), cur...)
		prevStart, prevEnd := 1, 1+len(prev)
		stubbed := PlanWholePromptStubbing(msgs, testMsgEstimator, budget, keepRecent, prevStart, prevEnd, 0)
		if len(stubbed) == 0 {
			t.Fatal("nothing stubbed; whole prompt far over highWM")
		}
		apply := applyStubs(msgs, stubbed, testMsgEstimator, InTurnDemotionPolicy{SessionID: sessionID})
		if total := sumEstimates(apply); total > lowWM {
			t.Errorf("post-stub %d > lowWM %d; drain must reach lowWM", total, lowWM)
		}
	})

	t.Run("already-stubbed results excluded but count toward total", func(t *testing.T) {
		// prev: 20×20000 (~80k tok), cur: 2×20000 (~8k tok), sys ~1k.
		// Whole: ~89k > highWM. The two oldest prev results are pre-stubbed.
		// Cur candidates: 1 (keepRecent 1). Prev candidates: 18 (20 - 2 stubbed - 1 keepRecent).
		// Drain: 1 cur stub (~4k saved) + ~6 prev stubs (~24k saved) = ~28k saved.
		// Post: ~89k - 28k = ~61k. Still over lowWM. Need more stubs.
		// 1 cur + 12 prev = ~52k saved → post ~37k < lowWM. ✓
		prev, prevIdx := mkTurn(20, 20000, "read_file")
		cur, _ := mkTurn(2, 20000, "bash")
		msgs := append(append([]Msg{{Role: "system", Content: strings.Repeat("y", 4000)}}, prev...), cur...)
		prevStart, prevEnd := 1, 1+len(prev)
		// Stub the two oldest prev results (simulating a prior process).
		// prevIdx is local to prev; offset by prevStart for global index.
		gPrev0 := prevStart + prevIdx[0]
		gPrev1 := prevStart + prevIdx[1]
		msgs[gPrev0].Content = InTurnStubContent("read_file(a.go)", 240, false, "Error: exit 1", citationFor(msgs, gPrev0, sessionID))
		msgs[gPrev1].Content = InTurnStubContent("read_file(b.go)", 12, true, "", citationFor(msgs, gPrev1, sessionID))
		stubbed := PlanWholePromptStubbing(msgs, testMsgEstimator, budget, 1, prevStart, prevEnd, 0)
		if len(stubbed) == 0 {
			t.Fatal("nothing stubbed; whole prompt over highWM (existing stubs count)")
		}
		for _, idx := range stubbed {
			if idx == gPrev0 || idx == gPrev1 {
				t.Errorf("already-stubbed result %d re-stubbed", idx)
			}
		}
		// After cur's 1 candidate, next must be the next verbatim prev result.
		gPrev2 := prevStart + prevIdx[2]
		if len(stubbed) > 1 && stubbed[1] != gPrev2 {
			t.Errorf("stubbed[1]=%d want %d (next verbatim prev)", stubbed[1], gPrev2)
		}
	})

	t.Run("no previous span: degrades to turn-only plan", func(t *testing.T) {
		// 30×20000 (~120k tok > highWM). Empty prev span (0,0).
		msgs, toolIdx := mkTurn(30, 20000, "bash")
		if n := len(PlanWholePromptStubbing(msgs, testMsgEstimator, WholePromptBudget{}, keepRecent, 0, 0, 0)); n != 0 {
			t.Fatalf("zero budget stubbed %d; must be 0", n)
		}
		stubbed := planWholePrompt(msgs, budget, keepRecent)
		turnOnly := PlanInTurnDemotion(msgs, testMsgEstimator, InTurnDemotionPolicy{HighWM: highWM, LowWM: lowWM, KeepRecent: keepRecent, SessionID: sessionID})
		if len(stubbed) != len(turnOnly) {
			t.Fatalf("empty prev span stubbed %d, turn-only %d; must match", len(stubbed), len(turnOnly))
		}
		for i, idx := range stubbed {
			if idx != turnOnly[i] {
				t.Errorf("stubbed[%d]=%d want %d", i, idx, turnOnly[i])
			}
			if idx != toolIdx[i] {
				t.Errorf("stubbed[%d]=%d want %d (oldest first)", i, idx, toolIdx[i])
			}
		}
	})

	t.Run("no stubbable results: nil even over budget", func(t *testing.T) {
		prev, _ := mkTurn(2, 100000, "read_file")
		cur, _ := mkTurn(2, 100000, "bash")
		msgs := append(append([]Msg{{Role: "system", Content: "sys"}}, prev...), cur...)
		prevStart, prevEnd := 1, 1+len(prev)
		stubbed := PlanWholePromptStubbing(msgs, testMsgEstimator, budget, keepRecent, prevStart, prevEnd, 0)
		if len(stubbed) != 0 {
			t.Errorf("stubbed %d, want 0 (nothing older than keepRecent)", len(stubbed))
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
