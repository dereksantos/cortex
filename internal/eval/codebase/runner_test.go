package codebase

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
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
	// confirm the tail-by-offset logic ignores it. Real cortex runs
	// will accumulate trace rows over time; the runner must only see
	// the rows this invocation appends.
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
		t.Errorf("TraceRows = %d, want 4 (historical row should be filtered out by tail-offset)", len(res.TraceRows))
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
