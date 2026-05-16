package commands

import (
	"bytes"
	"encoding/json"
	"reflect"
	"testing"

	evalv2 "github.com/dereksantos/cortex/internal/eval/v2"
	"github.com/dereksantos/cortex/internal/harness"
)

// TestEmitCodeJSON pins the --json schema benchmarks (and other
// downstream consumers) parse. Field names and types are the public
// contract; changing them is a breaking change.
func TestEmitCodeJSON(t *testing.T) {
	hr := evalv2.HarnessResult{
		AgentTurnsTotal: 7,
		TokensIn:        1234,
		TokensOut:       567,
		CostUSD:         0.045,
		LatencyMs:       8200,
		FilesChanged:    []string{"main.go", "main_test.go"},
	}
	loopRes := harness.LoopResult{
		Reason:                harness.LoopReason("budget_exhausted"),
		Final:                 "done; tests pass",
		InjectedContextTokens: 320,
	}

	var buf bytes.Buffer
	if err := emitCodeJSON(&buf, "/tmp/work", "anthropic/claude-haiku-4.5", hr, loopRes); err != nil {
		t.Fatalf("emitCodeJSON: %v", err)
	}

	var got codeJSONOutput
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	want := codeJSONOutput{
		Workdir:         "/tmp/work",
		Model:           "anthropic/claude-haiku-4.5",
		Turns:           7,
		TokensIn:        1234,
		TokensOut:       567,
		CostUSD:         0.045,
		LatencyMs:       8200,
		Reason:          "budget_exhausted",
		FilesChanged:    []string{"main.go", "main_test.go"},
		Final:           "done; tests pass",
		InjectedContext: 320,
	}
	if !reflect.DeepEqual(got, want) {
		gw, _ := json.MarshalIndent(got, "", "  ")
		ww, _ := json.MarshalIndent(want, "", "  ")
		t.Fatalf("emitCodeJSON output mismatch\n got: %s\nwant: %s", gw, ww)
	}
}

// TestEmitCodeJSON_NilFilesChanged ensures the contract emits an empty
// JSON array rather than null when the agent didn't touch any files.
// Benchmark parsers shouldn't have to handle both forms.
func TestEmitCodeJSON_NilFilesChanged(t *testing.T) {
	var buf bytes.Buffer
	if err := emitCodeJSON(&buf, "/w", "m", evalv2.HarnessResult{}, harness.LoopResult{}); err != nil {
		t.Fatalf("emitCodeJSON: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	fc, ok := got["files_changed"]
	if !ok {
		t.Fatal("files_changed key missing")
	}
	arr, ok := fc.([]any)
	if !ok {
		t.Fatalf("files_changed type = %T, want []any (JSON array)", fc)
	}
	if len(arr) != 0 {
		t.Errorf("len(files_changed) = %d, want 0", len(arr))
	}
}
