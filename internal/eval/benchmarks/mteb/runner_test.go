package mteb

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/dereksantos/cortex/internal/eval/benchmarks"
)

// TestRunRejectsBadPayload — passing the wrong Payload type is a Loader
// bug; the runner must refuse rather than panic.
func TestRunRejectsBadPayload(t *testing.T) {
	b := &Benchmark{}
	_, err := b.Run(context.Background(), benchmarks.Instance{ID: "x", Payload: "nope"}, benchmarks.Env{Workdir: t.TempDir()})
	if err == nil {
		t.Fatal("expected error on bad payload")
	}
	if !strings.Contains(err.Error(), "want *Task") {
		t.Errorf("error %q should mention want *Task", err.Error())
	}
}

// TestRunIntegration_TinyCorpus — index a 3-doc corpus and confirm the
// runner returns a fully-validated CellResult with non-zero retrieval
// telemetry. Exercises the cortex CLI end-to-end (bulk-embed + search-
// vector). Skipped when no embedder is reachable from the test env —
// the keyword-stub regression tests are gone now that runner internals
// are subprocess-based.
//
// We don't assert specific NDCG values: scoring is sensitive to the
// embedder's model, and the goal here is wiring confidence (CellResult
// shape, embedder attribution, success threshold logic) — not
// reproducing an MTEB leaderboard number from a tiny corpus.
func TestRunIntegration_TinyCorpus(t *testing.T) {
	if os.Getenv("CORTEX_BINARY") == "" {
		t.Skip("CORTEX_BINARY not set (TestMain builds it; run via `go test`)")
	}
	if !embedderReachable(t) {
		t.Skip("no embedder reachable from test env (Ollama or Hugot required)")
	}

	task := &Task{
		Name: "tiny",
		Corpus: map[string]Doc{
			"d1": {ID: "d1", Title: "fox tale", Text: "The quick brown fox jumps over the lazy dog."},
			"d2": {ID: "d2", Title: "dog tale", Text: "A loyal dog guards the village from wolves."},
			"d3": {ID: "d3", Title: "plain story", Text: "The plains stretched endlessly under a heavy sky."},
		},
		Queries: []Query{
			{ID: "q1", Text: "fox jumping"},
			{ID: "q2", Text: "open plains"},
		},
		Qrels: map[string]map[string]int{
			"q1": {"d1": 2},
			"q2": {"d3": 1},
		},
	}

	b := &Benchmark{}
	cell, err := b.Run(context.Background(), benchmarks.Instance{
		ID:      "mteb/tiny",
		Payload: task,
	}, benchmarks.Env{Workdir: t.TempDir()})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if cell == nil {
		t.Fatal("nil cell")
	}
	if err := cell.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if cell.Benchmark != "mteb" || cell.ScenarioID != "mteb/tiny" {
		t.Errorf("Benchmark/ScenarioID = %q/%q", cell.Benchmark, cell.ScenarioID)
	}
	if cell.Model == "" || cell.Provider == "" {
		t.Errorf("embedder attribution missing: model=%q provider=%q", cell.Model, cell.Provider)
	}
	for _, sub := range []string{"NDCG@10=", "MRR@10=", "Recall@10=", "queries=2", "rerank=false"} {
		if !strings.Contains(cell.Notes, sub) {
			t.Errorf("Notes %q missing %q", cell.Notes, sub)
		}
	}
}

// embedderReachable runs `cortex embed --text foo` and checks for a
// successful exit. Done via the helper rather than a direct call into
// pkg/llm so we test the same code path the benchmark exercises.
func embedderReachable(t *testing.T) bool {
	t.Helper()
	_, err := benchmarks.RunSearchVector(context.Background(), os.Getenv("CORTEX_BINARY"), benchmarks.SearchVectorOpts{
		Workdir: t.TempDir(),
		Text:    "smoke test",
		TopK:    1,
	})
	// "no embedder available" is the signal we want; any other error
	// (network, etc.) probably means an environment problem and we
	// should let the integration test surface it rather than silently
	// skipping. We treat any error mentioning "embedder" as not-
	// reachable; otherwise assume reachable and let the actual test
	// fail loudly.
	if err != nil && strings.Contains(err.Error(), "embedder") {
		return false
	}
	return true
}
