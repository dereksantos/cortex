package commands

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dereksantos/cortex/internal/eval/benchmarks"
	evalv2 "github.com/dereksantos/cortex/internal/eval/v2"
)

// dummyBenchForCLI is a registered Benchmark used only by these tests to
// exercise the runBenchmark dispatch path end-to-end.
type dummyBenchForCLI struct{}

func (d *dummyBenchForCLI) Name() string { return "cli-dummy" }
func (d *dummyBenchForCLI) Load(_ context.Context, opts benchmarks.LoadOpts) ([]benchmarks.Instance, error) {
	out := []benchmarks.Instance{{ID: "a"}, {ID: "b"}, {ID: "c"}}
	if opts.Limit > 0 && opts.Limit < len(out) {
		out = out[:opts.Limit]
	}
	return out, nil
}
func (d *dummyBenchForCLI) Run(_ context.Context, inst benchmarks.Instance, _ benchmarks.Env) (*evalv2.CellResult, error) {
	return &evalv2.CellResult{
		SchemaVersion:        evalv2.CellResultSchemaVersion,
		RunID:                "cli-dummy-" + inst.ID + "-" + time.Now().Format("150405.000000"),
		Timestamp:            time.Now().UTC().Format(time.RFC3339),
		ScenarioID:           "cli-dummy/" + inst.ID,
		Benchmark:            "cli-dummy",
		Harness:              evalv2.HarnessCortex,
		Provider:             evalv2.ProviderOpenRouter,
		Model:                "test-model",
		ContextStrategy:      evalv2.StrategyCortex,
		CortexVersion:        "0.1.0",
		Temperature:          0.0,
		TaskSuccess:          true,
		TaskSuccessCriterion: evalv2.CriterionTestsPassAll,
	}, nil
}

func TestParseBenchmarkArgs(t *testing.T) {
	tests := []struct {
		name       string
		args       []string
		wantSubset string
		wantLimit  int
		wantErr    bool
	}{
		{"empty", nil, "", 0, false},
		{"subset only", []string{"--subset", "oracle"}, "oracle", 0, false},
		{"limit only", []string{"--limit", "5"}, "", 5, false},
		{"both", []string{"--subset", "verified", "--limit", "3"}, "verified", 3, false},
		{"unknown flag tolerated", []string{"--depth", "0.5", "--subset", "x"}, "x", 0, false},
		{"missing subset value", []string{"--subset"}, "", 0, true},
		{"missing limit value", []string{"--limit"}, "", 0, true},
		{"limit not int", []string{"--limit", "abc"}, "", 0, true},
		{"shared flags survive unknown per-benchmark flags", []string{"--subset", "verified", "--limit", "3", "--model", "x", "--strategy", "baseline,cortex"}, "verified", 3, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			opts, err := parseBenchmarkArgs(tc.args)
			if tc.wantErr {
				if err == nil {
					t.Errorf("want error, got nil; opts=%+v", opts)
				}
				return
			}
			if err != nil {
				t.Errorf("unexpected err: %v", err)
			}
			if opts.Subset != tc.wantSubset {
				t.Errorf("Subset=%q want %q", opts.Subset, tc.wantSubset)
			}
			if opts.Limit != tc.wantLimit {
				t.Errorf("Limit=%d want %d", opts.Limit, tc.wantLimit)
			}
		})
	}
}

func TestRunBenchmark_EndToEndPersists(t *testing.T) {
	// Isolate from any real ~/.cortex/ — runBenchmark calls NewPersister
	// which uses cwd-relative paths.
	dir := t.TempDir()
	prev, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	t.Cleanup(func() { os.Chdir(prev) })

	// Reset and register the dummy benchmark for this test only.
	t.Cleanup(func() {
		// resetRegistryForTest is in the benchmarks package; we can't call
		// it from here. Subsequent tests that depend on registry state
		// should rely on Get returning ErrUnknownBenchmark for fresh
		// names rather than a clean slate.
	})
	benchmarks.Register("cli-dummy", func() benchmarks.Benchmark { return &dummyBenchForCLI{} })

	if err := runBenchmark("cli-dummy", []string{"--limit", "2"}, false); err != nil {
		t.Fatalf("runBenchmark: %v", err)
	}

	// SQLite + JSONL + journal all populated for the persisted cells.
	jsonlPath := filepath.Join(dir, ".cortex", "db", "cell_results.jsonl")
	data, err := os.ReadFile(jsonlPath)
	if err != nil {
		t.Fatalf("read jsonl: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != 2 {
		t.Errorf("jsonl line count=%d want 2", len(lines))
	}
	for _, line := range lines {
		if !strings.Contains(line, `"benchmark":"cli-dummy"`) {
			t.Errorf("jsonl line missing benchmark field: %s", line)
		}
		if !strings.Contains(line, `"scenario_id":"cli-dummy/`) {
			t.Errorf("jsonl line missing scenario_id prefix: %s", line)
		}
	}

	// Journal directory has at least one segment file.
	journalDir := filepath.Join(dir, ".cortex", "journal", "eval")
	entries, err := os.ReadDir(journalDir)
	if err != nil {
		t.Fatalf("read journal dir: %v", err)
	}
	if len(entries) == 0 {
		t.Errorf("journal dir %q is empty; expected at least one segment", journalDir)
	}
}

// Per-benchmark flag parsing (--repo, --strategy, --model, etc.) is
// covered in the per-benchmark ApplyArgs tests (see
// internal/eval/benchmarks/swebench/applyargs_test.go and the niah
// equivalent). The CLI dispatch layer only owns --subset / --limit.

func TestRunBenchmark_UnknownReturnsCleanError(t *testing.T) {
	dir := t.TempDir()
	prev, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	t.Cleanup(func() { os.Chdir(prev) })

	err = runBenchmark("does-not-exist-"+t.Name(), nil, false)
	if err == nil {
		t.Fatal("want error on unknown benchmark, got nil")
	}
	if !strings.Contains(err.Error(), "unknown benchmark") {
		t.Errorf("err=%v should mention 'unknown benchmark'", err)
	}
}
