package mteb

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dereksantos/cortex/internal/eval/benchmarks"
	evalv2 "github.com/dereksantos/cortex/internal/eval/v2"
	"github.com/dereksantos/cortex/pkg/llm"
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

// TestRunEndToEndDeterministicEmbedder — exercise index → retrieve →
// score with a deterministic embedder that puts known docs at the top.
// This is the single test that proves the whole runner stack — fresh
// storage, indexing, vector search, metric aggregation, CellResult
// shape — is wired correctly without needing Hugot/Ollama.
func TestRunEndToEndDeterministicEmbedder(t *testing.T) {
	withFakeEmbedder(t, &keywordEmbedder{
		// Each "keyword" maps to a one-hot vector; queries match docs
		// that share their keyword.
		keywords: []string{"fox", "dog", "plain"},
	})

	task := &Task{
		Name: "NFCorpus",
		Corpus: map[string]Doc{
			"d1": {ID: "d1", Title: "fox tale", Text: "fox fox fox"},
			"d2": {ID: "d2", Title: "dog tale", Text: "dog dog"},
			"d3": {ID: "d3", Title: "plain story", Text: "plain plain plain"},
		},
		Queries: []Query{
			{ID: "q1", Text: "fox"},
			{ID: "q2", Text: "plain"},
		},
		Qrels: map[string]map[string]int{
			"q1": {"d1": 2},
			"q2": {"d3": 1},
		},
	}

	b := &Benchmark{}
	cell, err := b.Run(context.Background(), benchmarks.Instance{
		ID:      "mteb/NFCorpus",
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
	if cell.Benchmark != "mteb" {
		t.Errorf("Benchmark=%q, want mteb", cell.Benchmark)
	}
	if cell.ScenarioID != "mteb/NFCorpus" {
		t.Errorf("ScenarioID=%q, want mteb/NFCorpus", cell.ScenarioID)
	}
	if cell.Harness != evalv2.HarnessCortex {
		t.Errorf("Harness=%q, want %q", cell.Harness, evalv2.HarnessCortex)
	}
	if cell.ContextStrategy != evalv2.StrategyCortex {
		t.Errorf("ContextStrategy=%q, want cortex", cell.ContextStrategy)
	}
	if cell.TaskSuccessCriterion != evalv2.CriterionTestsPassAll {
		t.Errorf("TaskSuccessCriterion=%q, want tests_pass_all", cell.TaskSuccessCriterion)
	}
	if cell.CortexVersion == "" {
		t.Error("CortexVersion empty — Validate rejects this for cortex strategy")
	}
	if !cell.TaskSuccess {
		t.Errorf("expected TaskSuccess=true with perfect-ranking embedder; notes=%q", cell.Notes)
	}
	for _, sub := range []string{"NDCG@10=", "MRR@10=", "Recall@10=", "queries=2", "embedder="} {
		if !strings.Contains(cell.Notes, sub) {
			t.Errorf("Notes %q missing %q", cell.Notes, sub)
		}
	}
}

// TestRunSkipsQueriesWithoutQrels — a query with no judged relevant
// docs is dropped from the aggregate (matches MTEB convention). Notes
// must reflect the lower scored count, not the loaded count.
func TestRunSkipsQueriesWithoutQrels(t *testing.T) {
	withFakeEmbedder(t, &keywordEmbedder{keywords: []string{"a", "b"}})
	task := &Task{
		Name: "NFCorpus",
		Corpus: map[string]Doc{
			"d1": {ID: "d1", Text: "a a a"},
		},
		Queries: []Query{
			{ID: "q1", Text: "a"},
			{ID: "q-empty", Text: "b"}, // no qrels for this query
		},
		Qrels: map[string]map[string]int{
			"q1": {"d1": 1},
		},
	}
	b := &Benchmark{}
	cell, err := b.Run(context.Background(), benchmarks.Instance{Payload: task}, benchmarks.Env{Workdir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(cell.Notes, "queries=1") {
		t.Errorf("Notes %q should report queries=1 (unjudged query skipped)", cell.Notes)
	}
}

// TestRunErrorsWhenNoEmbedder — surface a clear message rather than
// silently returning 0 scores. An MTEB run with no embedder measures
// nothing.
func TestRunErrorsWhenNoEmbedder(t *testing.T) {
	withFakeEmbedder(t, &unavailableEmbedder{})
	task := &Task{Name: "NFCorpus", Corpus: map[string]Doc{}, Queries: nil}
	b := &Benchmark{}
	_, err := b.Run(context.Background(), benchmarks.Instance{Payload: task}, benchmarks.Env{Workdir: t.TempDir()})
	if err == nil || !strings.Contains(err.Error(), "no embedder available") {
		t.Errorf("err=%v, want \"no embedder available\"", err)
	}
}

// TestRunUsesIsolatedStorage — the per-Run storage lives under
// workdir/.cortex/data/, leaving the operator's real ~/.cortex alone.
// Confirm the embeddings file lands in the right place.
func TestRunUsesIsolatedStorage(t *testing.T) {
	withFakeEmbedder(t, &keywordEmbedder{keywords: []string{"a"}})
	wd := t.TempDir()
	task := &Task{
		Name: "NFCorpus",
		Corpus: map[string]Doc{
			"d1": {ID: "d1", Text: "a a a"},
		},
		Queries: []Query{{ID: "q1", Text: "a"}},
		Qrels:   map[string]map[string]int{"q1": {"d1": 1}},
	}
	b := &Benchmark{}
	if _, err := b.Run(context.Background(), benchmarks.Instance{Payload: task}, benchmarks.Env{Workdir: wd}); err != nil {
		t.Fatal(err)
	}
	embPath := filepath.Join(wd, ".cortex", "data", "embeddings.jsonl")
	info, err := os.Stat(embPath)
	if err != nil {
		t.Fatalf("expected embeddings file under workdir: %v", err)
	}
	if info.Size() == 0 {
		t.Error("embeddings file is empty")
	}
}

// --- fake embedders + helpers ---

// keywordEmbedder produces a deterministic one-hot vector indexed by
// the first known keyword present in the input. Docs with keyword K
// retrieve perfectly for queries containing K — letting unit tests
// score perfect NDCG without a real embedding model.
type keywordEmbedder struct {
	keywords []string
}

func (k *keywordEmbedder) Embed(_ context.Context, text string) ([]float32, error) {
	vec := make([]float32, len(k.keywords))
	for i, kw := range k.keywords {
		if strings.Contains(strings.ToLower(text), kw) {
			vec[i] = 1
		}
	}
	if allZero(vec) {
		// Give it some signal so similarity > 0; consistent across docs.
		vec[0] = 0.01
	}
	return vec, nil
}

func (k *keywordEmbedder) IsEmbeddingAvailable() bool { return true }

type unavailableEmbedder struct{}

func (u *unavailableEmbedder) Embed(_ context.Context, _ string) ([]float32, error) {
	return nil, errors.New("not available")
}
func (u *unavailableEmbedder) IsEmbeddingAvailable() bool { return false }

func allZero(v []float32) bool {
	for _, x := range v {
		if x != 0 {
			return false
		}
	}
	return true
}

// withFakeEmbedder swaps embedderFactory for the duration of one test.
func withFakeEmbedder(t *testing.T, e llm.Embedder) {
	t.Helper()
	prev := embedderFactory
	embedderFactory = func() (llm.Embedder, string, string) {
		return e, "fake-keyword-embedder", evalv2.ProviderLocal
	}
	t.Cleanup(func() { embedderFactory = prev })
}

// Compile-time assertion that the fake embedders satisfy the same
// interface the runner depends on — catches signature drift between
// llm.Embedder and the test fakes.
var (
	_ llm.Embedder = (*keywordEmbedder)(nil)
	_ llm.Embedder = (*unavailableEmbedder)(nil)
)
