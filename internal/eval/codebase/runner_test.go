package codebase

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// TestRunWithStubBinary validates the runner end-to-end (the
// fixture → cortex-binary → session.jsonl → dag_traces.jsonl →
// metric-extraction pipeline) without actually invoking an LLM.
//
// We point CortexBinary at a tiny shell script that:
//
//   - mkdirs <workdir>/.cortex/sessions/<ts>/
//   - writes session.jsonl with a synthetic FinalText we control
//   - appends rows to <workdir>/.cortex/db/dag_traces.jsonl
//   - prints the ok-envelope JSON the runner parses
//
// The runner should pick up the answer text, tail-read the trace rows,
// extract metrics, and evaluate the fixture's bounds. This is exactly
// the path slice-1 needs validated before authoring more fixtures.
func TestRunWithStubBinary(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("stub uses /bin/sh")
	}

	workdir := t.TempDir()
	// Pre-seed dag_traces.jsonl with a "historical" row so we can
	// confirm the turn_id-delta logic ignores it. Real cortex runs
	// accumulate trace rows over time; the runner must only see the
	// rows whose turn_id this invocation introduced.
	dbDir := filepath.Join(workdir, ".cortex", "db")
	if err := os.MkdirAll(dbDir, 0o755); err != nil {
		t.Fatal(err)
	}
	historical := `{"turn_id":"old-turn","qualified_name":"decide.next","ok":true}` + "\n"
	if err := os.WriteFile(filepath.Join(dbDir, "dag_traces.jsonl"), []byte(historical), 0o644); err != nil {
		t.Fatal(err)
	}

	// Stub cortex: writes session.jsonl + appends three trace rows +
	// prints an ok JSON envelope. Mirrors the shape the real one-shot
	// path produces.
	sessionDir := filepath.Join(workdir, ".cortex", "sessions", "20260101T000000Z")
	if err := os.MkdirAll(sessionDir, 0o755); err != nil {
		t.Fatal(err)
	}
	sessionPath := filepath.Join(sessionDir, "session.jsonl")
	stub := filepath.Join(t.TempDir(), "cortex")
	stubBody := `#!/bin/sh
cat > ` + sessionPath + ` <<'EOF'
{"turn":1,"final_text":"The handler lives in pkg/cognition/dag/ops/decide_next.go and registers via NewDecideNextHandler.\n\n- pkg/cognition/dag/ops/decide_next.go is the seed file\n- cmd/cortex/main.go wires the registry","accepted":true}
EOF
cat >> ` + dbDir + `/dag_traces.jsonl <<'EOF'
{"turn_id":"repl-stub","qualified_name":"sense.estimate_scope","ok":true,"out":{"budget_tokens":8000}}
{"turn_id":"repl-stub","qualified_name":"decide.next","ok":true}
{"turn_id":"repl-stub","qualified_name":"act.read_file","ok":true}
{"turn_id":"repl-stub","qualified_name":"decide.coding_turn","ok":true,"out":{"response":"The handler lives in pkg/cognition/dag/ops/decide_next.go"}}
EOF
echo '{"ok":true,"data":{"session_id":"20260101T000000Z","session_path":"` + sessionPath + `","workdir":"` + workdir + `","accepted":true}}'
`
	if err := os.WriteFile(stub, []byte(stubBody), 0o755); err != nil {
		t.Fatal(err)
	}

	fx := &Fixture{
		ID:      "q1-pinpoint-stub",
		Group:   GroupQuestion,
		Eval:    "Q1",
		Project: "cortex",
		Prompt:  "Where is decide.next registered?",
		Expected: Expectation{
			HopCountMin:     1,
			HopCountMax:     2,
			ReadCountMin:    1,
			ReadCountMax:    3,
			CitationRateMin: 0.5,
			HedgeCountMax:   -1,
			MustCitePaths:   []string{"pkg/cognition/dag/ops/decide_next.go"},
			MustNotInvent:   []string{"NewDecideNextNode"}, // real is NewDecideNextHandler
			BudgetTokenMin:  1000,
			BudgetTokenMax:  20000,
		},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	res, m, bounds, err := Run(ctx, fx, RunOptions{
		CortexBinary: stub,
		Workdir:      workdir,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.CortexExitErr != nil {
		t.Fatalf("CortexExitErr: %v", res.CortexExitErr)
	}
	if res.AnswerText == "" {
		t.Fatal("AnswerText is empty (session.jsonl parse failed)")
	}
	if len(res.TraceRows) != 4 {
		t.Errorf("TraceRows = %d, want 4 (historical row should be filtered out by turn_id delta)", len(res.TraceRows))
	}

	if m.HopCount != 1 || m.ReadCount != 1 {
		t.Errorf("hop=%d read=%d, want hop=1 read=1", m.HopCount, m.ReadCount)
	}
	if m.BudgetTokens != 8000 {
		t.Errorf("BudgetTokens = %d, want 8000", m.BudgetTokens)
	}
	if !m.MustCitePathsSatisfied {
		t.Error("must_cite_paths_satisfied = false")
	}
	if !m.MustNotInventClean {
		t.Errorf("must_not_invent hits = %v", m.MustNotInventHits)
	}

	if !AllPass(bounds) {
		for _, b := range bounds {
			if !b.Pass {
				t.Errorf("bound %s failed: want %s got %s", b.Name, b.Want, b.Got)
			}
		}
	}
}

func TestParseSessionPath(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"ok envelope", `{"ok":true,"data":{"session_path":"/tmp/foo/session.jsonl","model":"x"}}`, "/tmp/foo/session.jsonl"},
		{"top-level", `{"session_path":"/var/session.jsonl"}`, "/var/session.jsonl"},
		{"mixed lines", "noise line\n{\"data\":{\"session_path\":\"/p\"}}\nmore noise", "/p"},
		{"empty", "", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := parseSessionPath(c.in); got != c.want {
				t.Errorf("parseSessionPath = %q, want %q", got, c.want)
			}
		})
	}
}

func TestReadFinalTextPrefersRetry(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "session.jsonl")
	rows := []map[string]any{
		{"turn": 1, "final_text": "initial", "retry_final_text": "after-retry"},
	}
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range rows {
		b, _ := json.Marshal(r)
		f.Write(append(b, '\n'))
	}
	f.Close()
	txt, err := readFinalText(path)
	if err != nil {
		t.Fatal(err)
	}
	if txt != "after-retry" {
		t.Errorf("readFinalText = %q, want after-retry (retry should win)", txt)
	}
}

// TestReadNewTurnRowsByTurnID locks the property the old byte-offset
// tail lacked: a cell's rows are associated by turn_id, NOT file
// position. The previous offset window could land mid-turn (after the
// early sense.estimate_scope row but before the agent-loop rows),
// silently dropping budget_tokens while hop/read still populated. Here
// the new turn's estimate_scope row is INTERLEAVED among prior-turn
// rows; readNewTurnRows must still capture it, and Extract must read its
// budget. Rows with no turn_id are dropped (unattributable).
func TestReadNewTurnRowsByTurnID(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "dag_traces.jsonl")

	// Phase 1: prior content (two historical turns).
	prior := `{"turn_id":"old-1","qualified_name":"decide.next","ok":true}
{"turn_id":"old-2","qualified_name":"sense.estimate_scope","ok":true,"out":{"budget_tokens":99000}}
`
	if err := os.WriteFile(path, []byte(prior), 0o644); err != nil {
		t.Fatal(err)
	}
	priorTurns := snapshotTurnIDs(path)
	if len(priorTurns) != 2 {
		t.Fatalf("snapshotTurnIDs = %d turns, want 2", len(priorTurns))
	}

	// Phase 2: append this run's rows, deliberately interleaved with a
	// late-arriving prior-turn row and a turn_id-less row. The new turn's
	// estimate_scope sits BEFORE its own agent-loop rows — exactly the
	// ordering the offset tail mishandled.
	appended := `{"turn_id":"repl-new","qualified_name":"sense.estimate_scope","ok":true,"out":{"budget_tokens":7000}}
{"turn_id":"old-2","qualified_name":"act.read_file","ok":true}
{"qualified_name":"act.list_dir","ok":true}
{"turn_id":"repl-new","qualified_name":"decide.next","ok":true}
{"turn_id":"repl-new","qualified_name":"act.read_file","ok":true}
`
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	f.WriteString(appended)
	f.Close()

	rows, err := readNewTurnRows(path, priorTurns)
	if err != nil {
		t.Fatal(err)
	}
	// Expect exactly the three repl-new rows; old-2's late row and the
	// turn_id-less row are excluded.
	if len(rows) != 3 {
		t.Fatalf("readNewTurnRows returned %d rows, want 3 (the repl-new rows only)", len(rows))
	}
	for _, r := range rows {
		if r.TurnID != "repl-new" {
			t.Errorf("captured foreign turn_id %q", r.TurnID)
		}
	}

	// The interleaved estimate_scope must survive into the metric.
	m := Extract("", rows, nil)
	if m.BudgetTokens != 7000 {
		t.Errorf("BudgetTokens = %d, want 7000 (the new turn's scope budget, not old-2's 99000)", m.BudgetTokens)
	}
	if m.HopCount != 1 {
		t.Errorf("HopCount = %d, want 1 (one repl-new decide.next)", m.HopCount)
	}
	if m.ReadCount != 1 {
		t.Errorf("ReadCount = %d, want 1 (one repl-new act.read_file; old-2's excluded)", m.ReadCount)
	}
}

// TestClassifyInvalid locks the harness-failure-vs-quality boundary: a
// killed/timed-out subprocess or an empty answer is INVALID (quarantine,
// don't score); a present-but-wrong answer, a non-converged NEED_MORE,
// and an honest "I don't know" are REAL quality outcomes and stay
// scoreable.
func TestClassifyInvalid(t *testing.T) {
	cases := []struct {
		name    string
		res     *RunResult
		want    bool
		reasonH string // substring the reason should contain when invalid
	}{
		{"killed", &RunResult{AnswerText: "partial", CortexExitErr: errors.New("cortex exit: signal: killed (stderr=)")}, true, "killed"},
		{"deadline", &RunResult{AnswerText: "x", CortexExitErr: errors.New("context deadline exceeded")}, true, "timed out"},
		{"empty-answer", &RunResult{AnswerText: "   "}, true, "no answer"},
		{"wrong-but-present", &RunResult{AnswerText: "MAX_TODOS is 5"}, false, ""},
		{"need-more", &RunResult{AnswerText: "NEED_MORE: read app/storage.py"}, false, ""},
		{"honest-unknown", &RunResult{AnswerText: "I couldn't find that in the codebase."}, false, ""},
		{"clean-nonzero-exit-with-answer", &RunResult{AnswerText: "answer", CortexExitErr: errors.New("read dag_traces.jsonl: parse error")}, false, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, reason := classifyInvalid(tc.res)
			if got != tc.want {
				t.Fatalf("classifyInvalid = %v (reason=%q), want %v", got, reason, tc.want)
			}
			if got && tc.reasonH != "" && !strings.Contains(reason, tc.reasonH) {
				t.Errorf("reason %q missing %q", reason, tc.reasonH)
			}
		})
	}
}
