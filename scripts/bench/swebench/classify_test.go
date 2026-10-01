package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestClassify(t *testing.T) {
	tests := []struct {
		name string
		s    Signals
		want string
	}{
		{"env failure wins", Signals{EnvFailed: true, EvalStatus: EvalResolved}, ClassEnv},
		{"resolved even after timeout", Signals{TurnEnd: EndTimeout, EvalStatus: EvalResolved, PatchBytes: 10}, ClassNone},
		{"resolved", Signals{TurnEnd: EndCompleted, EvalStatus: EvalResolved, PatchBytes: 10, ToolCalls: 3}, ClassNone},
		{"budget stop", Signals{TurnEnd: EndBudgetStop, EvalStatus: EvalUnresolved, PatchBytes: 10}, ClassBudget},
		{"timeout", Signals{TurnEnd: EndTimeout, EvalStatus: EvalSkippedEmpty}, ClassTimeout},
		{"error", Signals{TurnEnd: EndError, EvalStatus: EvalUnresolved, PatchBytes: 5}, ClassError},
		{"chat mode", Signals{TurnEnd: EndCompleted, EvalStatus: EvalSkippedEmpty}, ClassChatMode},
		{"empty patch", Signals{TurnEnd: EndCompleted, EvalStatus: EvalSkippedEmpty, ToolCalls: 12}, ClassEmptyPatch},
		{"apply failed", Signals{TurnEnd: EndCompleted, EvalStatus: EvalApplyFailed, ToolCalls: 4, PatchBytes: 9}, ClassApplyFailed},
		{"eval error", Signals{TurnEnd: EndCompleted, EvalStatus: EvalError, ToolCalls: 4, PatchBytes: 9}, ClassEvalError},
		{"not scored", Signals{TurnEnd: EndCompleted, EvalStatus: EvalNotRun, ToolCalls: 4, PatchBytes: 9}, ClassEvalError},
		{"wrong patch", Signals{TurnEnd: EndCompleted, EvalStatus: EvalUnresolved, ToolCalls: 4, PatchBytes: 9}, ClassWrongPatch},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Classify(tt.s); got != tt.want {
				t.Errorf("Classify=%q want %q", got, tt.want)
			}
		})
	}
}

func TestPatchFiles(t *testing.T) {
	tests := []struct {
		patch string
		want  int
	}{
		{"", 0},
		{"diff --git a/x b/x\n--- a/x\n+++ b/x\n", 1},
		{"diff --git a/x b/x\n+1\ndiff --git a/y b/y\n+2\n", 2},
	}
	for _, tt := range tests {
		if got := PatchFiles(tt.patch); got != tt.want {
			t.Errorf("PatchFiles(%q)=%d want %d", tt.patch, got, tt.want)
		}
	}
}

func TestScanTranscriptAndMetrics(t *testing.T) {
	dir := t.TempDir()
	tr := filepath.Join(dir, "s.jsonl")
	lines := `{"role":"user","content":"x"}
{"role":"assistant","tool_calls":[{"function":{"name":"grep"}},{"function":{"name":"edit_file"}}]}
{"kind":"snapshot","tool_calls":[{"function":{"name":"bash"}}]}
{"role":"assistant","tool_calls":[{"function":{"name":"bash"}}]}
{"truncated`
	if err := os.WriteFile(tr, []byte(lines), 0o644); err != nil {
		t.Fatal(err)
	}
	st, err := ScanTranscript(tr)
	if err != nil {
		t.Fatal(err)
	}
	if st.ToolCalls != 3 || st.MutatingCalls != 1 || st.ToolCounts["bash"] != 1 {
		t.Errorf("stats=%+v", st)
	}

	ctxDir := filepath.Join(dir, "cortex")
	evalDir := filepath.Join(ctxDir, "journal", "eval")
	if err := os.MkdirAll(evalDir, 0o755); err != nil {
		t.Fatal(err)
	}
	seg := `{"type":"eval.cell_result","payload":{"run_id":"other","tokens_in":1}}
{"type":"eval.cell_result","payload":{"run_id":"sess1","tokens_in":1000,"tokens_out":50,"reasoning_tokens":7,"cost_usd":0.0123,"agent_turns_total":1}}
`
	if err := os.WriteFile(filepath.Join(evalDir, "000001.jsonl"), []byte(seg), 0o644); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name    string
		session string
		want    CellMetrics
	}{
		{"found", "sess1", CellMetrics{TokensIn: 1000, TokensOut: 50, ReasoningTokens: 7, CostUSD: 0.0123, AgentTurns: 1, Found: true}},
		{"missing session", "nope", CellMetrics{}},
		{"empty session", "", CellMetrics{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ReadCellMetrics(ctxDir, tt.session)
			if err != nil {
				t.Fatal(err)
			}
			if got != tt.want {
				t.Errorf("metrics=%+v want %+v", got, tt.want)
			}
		})
	}
}
