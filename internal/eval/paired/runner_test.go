//go:build !windows

package paired

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	eval "github.com/dereksantos/cortex/internal/eval/v2"
)

// fakeHarness deterministically maps Condition → Outcome so the
// runner is testable without LLM access. Each entry in outcomes is
// keyed by Condition.Name; missing entries return a zero outcome and
// the optional err.
type fakeHarness struct {
	outcomes map[string]Outcome
	errs     map[string]error
	calls    []string // Condition.Name in arrival order
}

func (h *fakeHarness) Run(ctx context.Context, c Condition, _ *eval.CodingScenario, workdir string) (Outcome, error) {
	h.calls = append(h.calls, c.Name)
	if err := h.errs[c.Name]; err != nil {
		return h.outcomes[c.Name], err
	}
	return h.outcomes[c.Name], nil
}

// scenarioPath resolves the canonical GoL single-session scenario the
// test exercises. The scenario file ships in the repo, so this test
// stays hermetic — no network, no LLM.
func scenarioPath(t *testing.T) string {
	t.Helper()
	p, err := filepath.Abs("../../../test/evals/v2/coding/conways-game-of-life-single.yaml")
	if err != nil {
		t.Fatalf("abs path: %v", err)
	}
	return p
}

func TestRun_ThreeConditions_WritesJSONL(t *testing.T) {
	dir, err := os.MkdirTemp("", "paired-jsonl-*")
	if err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	defer os.RemoveAll(dir)
	outPath := filepath.Join(dir, "out.jsonl")

	conditions := []Condition{
		{Name: "small_alone", Model: "small-x", UseCortex: false},
		{Name: "small_with_cortex", Model: "small-x", UseCortex: true},
		{Name: "frontier_alone", Model: "frontier-y", UseCortex: false},
	}
	fh := &fakeHarness{
		outcomes: map[string]Outcome{
			"small_alone": {
				TokensIn: 1200, TokensOut: 400, CostUSD: 0.003, LatencyMs: 8000,
				AgentTurns: 6,
				Frames:     eval.FrameDiffResult{BuildOK: true, Passed: 1, Failed: 1, AllPassed: false},
				JudgePass:  false,
			},
			"small_with_cortex": {
				TokensIn: 1800, TokensOut: 500, CostUSD: 0.005, LatencyMs: 11000,
				AgentTurns: 7,
				Frames:     eval.FrameDiffResult{BuildOK: true, Passed: 2, Failed: 0, AllPassed: true},
				JudgePass:  true,
			},
			"frontier_alone": {
				TokensIn: 1100, TokensOut: 600, CostUSD: 0.045, LatencyMs: 6000,
				AgentTurns: 4,
				Frames:     eval.FrameDiffResult{BuildOK: true, Passed: 2, Failed: 0, AllPassed: true},
				JudgePass:  true,
			},
		},
	}

	results, err := Run(context.Background(), scenarioPath(t), conditions, fh, outPath)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got, want := len(results), 3; got != want {
		t.Fatalf("results: got %d, want %d", got, want)
	}
	if got := strings.Join(fh.calls, ","); got != "small_alone,small_with_cortex,frontier_alone" {
		t.Errorf("harness call order: %q", got)
	}

	// One JSONL line per condition, each parseable, in input order.
	f, err := os.Open(outPath)
	if err != nil {
		t.Fatalf("open jsonl: %v", err)
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	var rows []Result
	for scanner.Scan() {
		var r Result
		if err := json.Unmarshal(scanner.Bytes(), &r); err != nil {
			t.Fatalf("unmarshal line: %v (%q)", err, scanner.Text())
		}
		rows = append(rows, r)
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("scanner: %v", err)
	}
	if len(rows) != 3 {
		t.Fatalf("jsonl rows: got %d, want 3", len(rows))
	}

	// Pass semantics: build_ok && all_frames_passed && judge_pass.
	if rows[0].Pass {
		t.Errorf("small_alone should not pass (1/2 frames)")
	}
	if !rows[1].Pass || !rows[2].Pass {
		t.Errorf("small_with_cortex and frontier_alone should pass: %+v %+v", rows[1], rows[2])
	}

	// Cost-quality contrast: cortex row should cost more than small_alone
	// but match frontier on Pass. That is the differential the JSONL is
	// meant to surface.
	if rows[1].CostUSD <= rows[0].CostUSD {
		t.Errorf("small_with_cortex cost (%v) should exceed small_alone (%v)", rows[1].CostUSD, rows[0].CostUSD)
	}
	if rows[1].CostUSD >= rows[2].CostUSD {
		t.Errorf("small_with_cortex cost (%v) should be below frontier_alone (%v)", rows[1].CostUSD, rows[2].CostUSD)
	}
}

func TestRun_HarnessErrorRecordedOnRow_DoesNotAbort(t *testing.T) {
	conditions := []Condition{
		{Name: "ok", Model: "m1"},
		{Name: "boom", Model: "m2"},
		{Name: "after", Model: "m3"},
	}
	fh := &fakeHarness{
		outcomes: map[string]Outcome{
			"ok":    {Frames: eval.FrameDiffResult{BuildOK: true, AllPassed: true}, JudgePass: true},
			"after": {Frames: eval.FrameDiffResult{BuildOK: true, AllPassed: true}, JudgePass: true},
		},
		errs: map[string]error{"boom": errors.New("model unreachable")},
	}
	results, err := Run(context.Background(), scenarioPath(t), conditions, fh, "")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(results) != 3 {
		t.Fatalf("rows: %d", len(results))
	}
	if results[1].Err == "" || !strings.Contains(results[1].Err, "model unreachable") {
		t.Errorf("middle row should carry harness err, got %q", results[1].Err)
	}
	if !results[0].Pass || !results[2].Pass {
		t.Errorf("flanking rows should still pass: %+v %+v", results[0], results[2])
	}
}

func TestRun_RejectsBadInputs(t *testing.T) {
	cases := []struct {
		name       string
		conditions []Condition
		harness    Harness
		wantSubstr string
	}{
		{
			name:       "nil harness",
			conditions: []Condition{{Name: "x", Model: "m"}},
			harness:    nil,
			wantSubstr: "harness is required",
		},
		{
			name:       "empty conditions",
			conditions: nil,
			harness:    &fakeHarness{},
			wantSubstr: "at least one condition",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Run(context.Background(), scenarioPath(t), tc.conditions, tc.harness, "")
			if err == nil || !strings.Contains(err.Error(), tc.wantSubstr) {
				t.Errorf("err=%v, want substring %q", err, tc.wantSubstr)
			}
		})
	}
}

func TestSortByCost_AscendingStable(t *testing.T) {
	rows := []Result{
		{Condition: "b", CostUSD: 0.05},
		{Condition: "a", CostUSD: 0.01},
		{Condition: "c", CostUSD: 0.05}, // same cost as b — order preserved
		{Condition: "d", CostUSD: 0.10},
	}
	sorted := SortByCost(rows)
	if got := []string{sorted[0].Condition, sorted[1].Condition, sorted[2].Condition, sorted[3].Condition}; !equalStrSlice(got, []string{"a", "b", "c", "d"}) {
		t.Errorf("order: %v", got)
	}
}

func equalStrSlice(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
