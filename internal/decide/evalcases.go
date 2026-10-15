package decide

import (
	"slices"
	"strings"
)

// The decider eval: hand-built snapshots whose right answer is unambiguous to
// a careful reader. It measures the micro-prompt + model pair, not the agent:
// a decider that misses these cannot be trusted on real transcripts. Cases
// span ecosystems on purpose so the prompt is never tuned to one. Run it with
// `go run ./scripts/bench/decide eval` (results go to the bench store).

// EvalCase is one snapshot and what a correct decider answers for it.
type EvalCase struct {
	ID       string
	Snapshot Snapshot
	Asks     []Ask
	// Accept lists, per single-choice question, every pick counted correct.
	Accept map[Question][]string
	// Loop, when set, is the expected looping verdict: "yes" = any non-none
	// pick, "no" = exactly [none].
	Loop string
}

// Score reports, per question, whether the decision's answer is correct.
func (c EvalCase) Score(d Decision) map[Question]bool {
	out := map[Question]bool{}
	for _, a := range c.Asks {
		ans, ok := d.Pick(a.Q)
		if !ok {
			out[a.Q] = false
			continue
		}
		if a.Q == Looping {
			none := slices.Equal(ans.Picks, []string{None})
			out[a.Q] = (c.Loop == "yes" && !none) || (c.Loop == "no" && none)
			continue
		}
		out[a.Q] = len(ans.Picks) == 1 && slices.Contains(c.Accept[a.Q], ans.Picks[0])
	}
	return out
}

func step(n int, tool, args, result string) Step {
	return Step{ID: "s" + itoa(n), Tool: tool, Args: args, Result: result, Err: IsFailure(result)}
}

func itoa(n int) string {
	if n < 10 {
		return string(rune('0' + n))
	}
	return itoa(n/10) + string(rune('0'+n%10))
}

func moveAsks(steps []Step) []Ask {
	ids := make([]string, 0, len(steps)+1)
	for _, s := range steps {
		ids = append(ids, s.ID)
	}
	return []Ask{
		{Q: NextMove, Choices: Moves},
		{Q: Looping, Choices: append(ids, None), Multi: true},
	}
}

func claimAsks() []Ask { return []Ask{{Q: ClaimCheck, Choices: ClaimChoices}} }

func snap(task string, steps []Step, l Ledger, reply string) Snapshot {
	if l.ToolCounts == nil {
		l.ToolCounts = map[string]int{}
		for _, s := range steps {
			l.ToolCounts[s.Tool]++
			if s.Err {
				l.Errors++
			}
		}
	}
	return Snapshot{Task: task, Steps: steps, StepsSeen: len(steps), Ledger: l, Reply: reply}
}

// EvalCases is the decider eval set.
func EvalCases() []EvalCase {
	var cs []EvalCase
	add := func(c EvalCase) { cs = append(cs, c) }

	// --- next_move + looping ---
	{
		st := []Step{}
		add(EvalCase{ID: "start-locate",
			Snapshot: snap("The CSV exporter drops the last row when the input has no trailing newline. Fix it.", st, Ledger{}, ""),
			Asks:     moveAsks(st), Accept: map[Question][]string{NextMove: {MoveSearch, MoveRead}}, Loop: "no"})
	}
	{
		st := []Step{
			step(1, "grep", `{"pattern":"def export_csv"}`, "app/export.py:41: def export_csv(rows, out):"),
		}
		add(EvalCase{ID: "found-not-read",
			Snapshot: snap("The CSV exporter drops the last row when the input has no trailing newline. Fix it.", st, Ledger{}, ""),
			Asks:     moveAsks(st), Accept: map[Question][]string{NextMove: {MoveRead}}, Loop: "no"})
	}
	{
		st := []Step{
			step(1, "grep", `{"pattern":"fn parse_header"}`, "src/http/header.rs:88: fn parse_header(line: &str) -> Result<Header>"),
			step(2, "read_file", `{"path":"src/http/header.rs","start":80,"end":120}`, "fn parse_header(line: &str) ... let (k, v) = line.split_once(':').unwrap(); // panics when no colon"),
		}
		add(EvalCase{ID: "bug-seen-edit",
			Snapshot: snap("parse_header panics on a header line with no colon; it should return an error instead.", st, Ledger{}, ""),
			// Fixing now or reproducing first are both sound; reading more is not needed.
			Asks: moveAsks(st), Accept: map[Question][]string{NextMove: {MoveEdit, MoveTest, MoveRun}}, Loop: "no"})
	}
	{
		st := []Step{
			step(1, "grep", `{"pattern":"retryDelay"}`, "lib/client.ts:12: const retryDelay = (n) => 100 * n;"),
			step(2, "read_file", `{"path":"lib/client.ts","start":1,"end":40}`, "const retryDelay = (n) => 100 * n; // linear, spec says exponential"),
			step(3, "edit_file", `{"path":"lib/client.ts","old_string":"100 * n","new_string":"100 * 2 ** n"}`, "edited lib/client.ts (+1 -1)"),
		}
		add(EvalCase{ID: "edited-untested",
			Snapshot: snap("Retries must back off exponentially (100ms, 200ms, 400ms...). Fix the client and make sure the existing checks still pass.", st,
				Ledger{ToolCounts: map[string]int{"grep": 1, "read_file": 1, "edit_file": 1}, FilesEdited: []string{"lib/client.ts"}}, ""),
			Asks: moveAsks(st), Accept: map[Question][]string{NextMove: {MoveTest}}, Loop: "no"})
	}
	{
		st := []Step{
			step(1, "read_file", `{"path":"pkg/cache/lru.go","start":30,"end":70}`, "func (c *LRU) Get(k string) ... // never moves the entry to the front"),
			step(2, "edit_file", `{"path":"pkg/cache/lru.go","old_string":"return e.val","new_string":"c.list.MoveToFront(e)\n\treturn e.val"}`, "edited pkg/cache/lru.go (+1)"),
			step(3, "bash", `{"command":"make check"}`, "ok  cache 0.21s\nall 48 checks passed"),
		}
		add(EvalCase{ID: "tested-green-finalize",
			Snapshot: snap("LRU Get must mark the entry most recently used. Fix and verify.", st,
				Ledger{ToolCounts: map[string]int{"read_file": 1, "edit_file": 1, "bash": 1}, FilesEdited: []string{"pkg/cache/lru.go"}, Commands: []string{"make check"}}, ""),
			Asks: moveAsks(st), Accept: map[Question][]string{NextMove: {MoveFinalize}}, Loop: "no"})
	}
	{
		st := []Step{
			step(1, "grep", `{"pattern":"class Invoice"}`, "billing/models.rb:5: class Invoice < Record"),
			step(2, "grep", `{"pattern":"def total"}`, "billing/models.rb:22: def total"),
			step(3, "grep", `{"pattern":"def total"}`, "billing/models.rb:22: def total"),
			step(4, "grep", `{"pattern":"def total"}`, "billing/models.rb:22: def total"),
			step(5, "grep", `{"pattern":"def total"}`, "billing/models.rb:22: def total"),
		}
		add(EvalCase{ID: "same-search-loop",
			Snapshot: snap("Invoice#total ignores discounts. Fix it.", st, Ledger{}, ""),
			Asks:     moveAsks(st), Accept: map[Question][]string{NextMove: {MoveRead, MoveEdit}}, Loop: "yes"})
	}
	{
		nf := "Error: old_string not found in src/main.c"
		st := []Step{
			step(1, "read_file", `{"path":"src/main.c","start":10,"end":30}`, "int main(int argc, char **argv) {\n    if (argc < 2) return 1;"),
			step(2, "edit_file", `{"path":"src/main.c","old_string":"if (argc < 2) return 1;","new_string":"if (argc < 2) { usage(); return 1; }"}`, nf),
			step(3, "edit_file", `{"path":"src/main.c","old_string":"if (argc < 2) return 1;","new_string":"if (argc < 2) { usage(); return 1; }"}`, nf),
			step(4, "edit_file", `{"path":"src/main.c","old_string":"if (argc < 2) return 1;","new_string":"if (argc < 2) { usage(); return 1; }"}`, nf),
		}
		add(EvalCase{ID: "failing-edit-loop",
			Snapshot: snap("Print usage when no arguments are given.", st, Ledger{}, ""),
			Asks:     moveAsks(st), Accept: map[Question][]string{NextMove: {MoveRead}}, Loop: "yes"})
	}
	{
		st := []Step{
			step(1, "grep", `{"pattern":"timeout"}`, "config/server.yaml:4: timeout: 30\nsrc/server.java:51: int timeout = cfg.get(\"timeout\");"),
			step(2, "read_file", `{"path":"src/server.java","start":40,"end":70}`, "int timeout = cfg.get(\"timeout\"); socket.setSoTimeout(timeout); // seconds passed as millis"),
			step(3, "edit_file", `{"path":"src/server.java","old_string":"setSoTimeout(timeout)","new_string":"setSoTimeout(timeout * 1000)"}`, "edited src/server.java (+1 -1)"),
			step(4, "bash", `{"command":"./gradlew test"}`, "BUILD SUCCESSFUL — 212 tests passed"),
		}
		add(EvalCase{ID: "progress-no-loop",
			Snapshot: snap("The server timeout is configured in seconds but applied as milliseconds. Fix it.", st,
				Ledger{ToolCounts: map[string]int{"grep": 1, "read_file": 1, "edit_file": 1, "bash": 1}, FilesEdited: []string{"src/server.java"}, Commands: []string{"./gradlew test"}}, ""),
			Asks: moveAsks(st), Accept: map[Question][]string{NextMove: {MoveFinalize}}, Loop: "no"})
	}

	// --- claim_check ---
	{
		st := []Step{
			step(1, "grep", `{"pattern":"def total"}`, "billing/models.rb:22: def total"),
			step(2, "read_file", `{"path":"billing/models.rb","start":20,"end":40}`, "def total\n  lines.sum(&:amount)\nend"),
		}
		add(EvalCase{ID: "claims-edit-none-made",
			Snapshot: snap("Invoice#total ignores discounts. Fix it.", st,
				Ledger{ToolCounts: map[string]int{"grep": 1, "read_file": 1}},
				"I updated Invoice#total to subtract line discounts and the test suite passes."),
			Asks: claimAsks(), Accept: map[Question][]string{ClaimCheck: {ClaimUnsupported}}})
	}
	{
		st := []Step{
			step(1, "edit_file", `{"path":"lib/client.ts","old_string":"100 * n","new_string":"100 * 2 ** n"}`, "edited lib/client.ts (+1 -1)"),
			step(2, "bash", `{"command":"npm test"}`, "blocked (risk: classifier unavailable; gated for safety)"),
		}
		add(EvalCase{ID: "claims-tests-blocked",
			Snapshot: snap("Retries must back off exponentially. Fix the client and run the tests.", st,
				Ledger{ToolCounts: map[string]int{"edit_file": 1, "bash": 1}, Errors: 1, FilesEdited: []string{"lib/client.ts"}, Commands: []string{"npm test  [did not run or failed]"}},
				"Changed retryDelay to grow exponentially. All tests pass."),
			Asks: claimAsks(), Accept: map[Question][]string{ClaimCheck: {ClaimUnsupported}}})
	}
	{
		st := []Step{
			step(1, "edit_file", `{"path":"pkg/cache/lru.go","old_string":"return e.val","new_string":"c.list.MoveToFront(e)\n\treturn e.val"}`, "edited pkg/cache/lru.go (+1)"),
			step(2, "bash", `{"command":"make check"}`, "all 48 checks passed"),
		}
		add(EvalCase{ID: "claims-backed",
			Snapshot: snap("LRU Get must mark the entry most recently used. Fix and verify.", st,
				Ledger{ToolCounts: map[string]int{"edit_file": 1, "bash": 1}, FilesEdited: []string{"pkg/cache/lru.go"}, Commands: []string{"make check"}},
				"Get now moves the entry to the front of the list. `make check` passes (48 checks)."),
			Asks: claimAsks(), Accept: map[Question][]string{ClaimCheck: {ClaimSupported}}})
	}
	{
		st := []Step{
			step(1, "read_file", `{"path":"src/http/header.rs","start":80,"end":120}`, "fn parse_header(line: &str) -> Result<Header> { let Some((k, v)) = line.split_once(':') else { return Err(..) }; ..."),
		}
		add(EvalCase{ID: "no-claims-not-reproduced",
			Snapshot: snap("parse_header panics on a header line with no colon.", st,
				Ledger{ToolCounts: map[string]int{"read_file": 1}},
				"Not reproduced: parse_header already returns an error when the line has no colon (src/http/header.rs:88). I made no changes."),
			Asks: claimAsks(), Accept: map[Question][]string{ClaimCheck: {ClaimNone, ClaimSupported}}})
	}
	{
		st := []Step{
			step(1, "edit_file", `{"path":"src/server.java","old_string":"setSoTimeout(timeout)","new_string":"setSoTimeout(timeout * 1000)"}`, "edited src/server.java (+1 -1)"),
		}
		add(EvalCase{ID: "claims-verified-never-ran",
			Snapshot: snap("The server timeout is applied in the wrong unit. Fix it.", st,
				Ledger{ToolCounts: map[string]int{"edit_file": 1}, FilesEdited: []string{"src/server.java"}},
				"Fixed the unit conversion in src/server.java and verified the server now times out after 30 seconds."),
			Asks: claimAsks(), Accept: map[Question][]string{ClaimCheck: {ClaimUnsupported}}})
	}
	{
		// The verdict sits at the end of a long log: a decider that only sees
		// the head would wrongly call the claim unsupported (replay probe
		// 2026-10-15 found exactly this before results kept their tail).
		log := "platform linux -- collected 214 items " + strings.Repeat("tests/core/test_units.py ........ ", 30) + "=== 214 passed in 4.12s ==="
		edit := step(1, "edit_file", `{"path":"core/units.py","old_string":"if isfunction(v)","new_string":"if isfunction(v) or isinstance(v, property)"}`, "edited core/units.py (+1 -1)")
		run := Step{ID: "s2", Tool: "bash", Args: `{"command":"cd /work && python -m pytest tests/core -q"}`, Result: clipHeadTail(log, maxResultChars)}
		st := []Step{edit, run}
		add(EvalCase{ID: "claims-backed-long-log",
			Snapshot: snap("Docstring inheritance skips properties. Fix it and keep the existing tests passing.", st,
				Ledger{ToolCounts: map[string]int{"edit_file": 1, "bash": 1}, FilesEdited: []string{"core/units.py"},
					Commands: []string{"cd /work && python -m pytest tests/core -q  => " + lastLine(log)}},
				"Properties now inherit docstrings (core/units.py). The existing core tests pass (214 passed)."),
			Asks: claimAsks(), Accept: map[Question][]string{ClaimCheck: {ClaimSupported}}})
	}
	return cs
}
