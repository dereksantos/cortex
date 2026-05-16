package niah

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dereksantos/cortex/internal/eval/benchmarks"
	evalv2 "github.com/dereksantos/cortex/internal/eval/v2"
	"github.com/dereksantos/cortex/pkg/cognition"
)

// TestRegistered — the package registers itself under "niah" via init().
// If this name drifts the CLI dispatch silently breaks; a typo here is
// cheaper to catch than a runtime "unknown benchmark" three layers down.
func TestRegistered(t *testing.T) {
	got, err := benchmarks.Get("niah")
	if err != nil {
		t.Fatalf("benchmarks.Get(\"niah\"): %v", err)
	}
	if got.Name() != "niah" {
		t.Fatalf("Name() = %q, want %q", got.Name(), "niah")
	}
}

// TestLoadCrossProduct — Load returns one Instance per (length × depth)
// combination requested via Filter. This is the load-time contract the
// CLI depends on for cross-product expansion.
func TestLoadCrossProduct(t *testing.T) {
	b, err := benchmarks.Get("niah")
	if err != nil {
		t.Fatal(err)
	}
	opts := benchmarks.LoadOpts{Filter: map[string]string{
		"lengths": "1k,2k",
		"depths":  "0.0,0.5,1.0",
	}}
	insts, err := b.Load(context.Background(), opts)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got, want := len(insts), 2*3; got != want {
		t.Fatalf("len(insts)=%d, want %d", got, want)
	}
	// IDs must be unique and follow the niah/<length>-<depth> convention.
	seen := map[string]bool{}
	for _, in := range insts {
		if seen[in.ID] {
			t.Errorf("duplicate instance ID: %s", in.ID)
		}
		seen[in.ID] = true
		if !strings.HasPrefix(in.ID, "niah/") {
			t.Errorf("ID %q missing niah/ prefix", in.ID)
		}
	}
}

// TestLoadDefaults — with no Filter, Load returns at least the
// default-depth instances at the default length.
func TestLoadDefaults(t *testing.T) {
	b, err := benchmarks.Get("niah")
	if err != nil {
		t.Fatal(err)
	}
	insts, err := b.Load(context.Background(), benchmarks.LoadOpts{})
	if err != nil {
		t.Fatalf("Load defaults: %v", err)
	}
	if len(insts) != len(DefaultDepths) {
		t.Fatalf("default Load returned %d instances, want %d (one per default depth)",
			len(insts), len(DefaultDepths))
	}
}

// TestLoadLimitTrimsAfterCrossProduct — --limit applied AFTER the
// cross-product, per the brief.
func TestLoadLimitTrimsAfterCrossProduct(t *testing.T) {
	b, _ := benchmarks.Get("niah")
	opts := benchmarks.LoadOpts{
		Filter: map[string]string{"lengths": "1k,2k", "depths": "0.0,0.5,1.0"},
		Limit:  2,
	}
	insts, err := b.Load(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	if len(insts) != 2 {
		t.Fatalf("Limit=2 trimmed to %d", len(insts))
	}
}

// TestParseLengthLabel — accepts the "8k" / "16K" / raw-int forms the
// CLI passes through.
func TestParseLengthLabel(t *testing.T) {
	cases := map[string]int{
		"8k":   8 * 1024,
		"16K":  16 * 1024,
		"32k":  32 * 1024,
		"4000": 4000,
		" 8k ": 8 * 1024,
	}
	for in, want := range cases {
		got, err := ParseLengthLabel(in)
		if err != nil {
			t.Errorf("ParseLengthLabel(%q): %v", in, err)
			continue
		}
		if got != want {
			t.Errorf("ParseLengthLabel(%q) = %d, want %d", in, got, want)
		}
	}
	if _, err := ParseLengthLabel("not-a-length"); err == nil {
		t.Errorf("ParseLengthLabel(garbage) should error")
	}
}

// TestScoreRetrievalHit — when the top-K results contain the needle as
// a substring, hit=true and position reports the 1-indexed rank.
func TestScoreRetrievalHit(t *testing.T) {
	needle := "The secret recipe code is 4F-9X-2B."
	res := &cognition.ResolveResult{
		Results: []cognition.Result{
			{ID: "a", Content: "irrelevant chunk", Score: 0.4},
			{ID: "b", Content: "preamble then The secret recipe code is 4F-9X-2B. then more", Score: 0.9},
		},
	}
	hit, top, pos := scoreRetrieval(res, needle)
	if !hit {
		t.Fatal("expected hit, got miss")
	}
	if top != 0.9 {
		t.Errorf("top score = %v, want 0.9", top)
	}
	if pos != "2" {
		t.Errorf("position = %q, want \"2\"", pos)
	}
}

// TestScoreRetrievalMiss — none of the results contain the needle.
func TestScoreRetrievalMiss(t *testing.T) {
	res := &cognition.ResolveResult{
		Results: []cognition.Result{
			{ID: "a", Content: "irrelevant chunk", Score: 0.4},
			{ID: "b", Content: "preamble then more lorem", Score: 0.2},
		},
	}
	hit, _, pos := scoreRetrieval(res, "secret-needle")
	if hit {
		t.Fatal("expected miss, got hit")
	}
	if pos != "missing" {
		t.Errorf("position = %q, want \"missing\"", pos)
	}
}

// TestScoreRetrievalMultipleNeedles — multiple results contain the
// needle; position reports the *earliest* rank (the one the agent
// would actually act on).
func TestScoreRetrievalMultipleNeedles(t *testing.T) {
	needle := "NEEDLE"
	res := &cognition.ResolveResult{
		Results: []cognition.Result{
			{ID: "a", Content: "no match", Score: 0.4},
			{ID: "b", Content: "first NEEDLE here", Score: 0.7},
			{ID: "c", Content: "another NEEDLE later", Score: 0.6},
		},
	}
	hit, _, pos := scoreRetrieval(res, needle)
	if !hit {
		t.Fatal("expected hit")
	}
	if pos != "2" {
		t.Errorf("position = %q, want \"2\" (earliest matching rank)", pos)
	}
}

// TestScoreRetrievalNilSafe — empty / nil results don't panic.
func TestScoreRetrievalNilSafe(t *testing.T) {
	hit, _, pos := scoreRetrieval(nil, "x")
	if hit || pos != "missing" {
		t.Errorf("nil result: hit=%v pos=%q", hit, pos)
	}
	hit, _, pos = scoreRetrieval(&cognition.ResolveResult{}, "x")
	if hit || pos != "missing" {
		t.Errorf("empty result: hit=%v pos=%q", hit, pos)
	}
}

// TestRunSmallHaystackHits — end-to-end run with a tiny haystack:
// capture chunks, ingest, search, and confirm a hit. This is the
// smoke that proves the capture→ingest→search wiring is intact;
// integration with Cortex internals is exercised, but no LLM is called
// (Reflex falls back to text search when embedder is nil, which is
// exactly the in-process search path the runner uses).
func TestRunSmallHaystackHits(t *testing.T) {
	tmp := t.TempDir()
	persisterCleanup := withTestPersister(t, tmp)
	defer persisterCleanup()

	b, _ := benchmarks.Get("niah")
	insts, err := b.Load(context.Background(), benchmarks.LoadOpts{
		Filter: map[string]string{"lengths": "1k", "depths": "0.5"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(insts) != 1 {
		t.Fatalf("want 1 instance, got %d", len(insts))
	}

	persister, err := evalv2.NewPersister()
	if err != nil {
		t.Fatalf("new persister: %v", err)
	}
	defer persister.Close()

	env := benchmarks.Env{
		Workdir:   tmp,
		Persister: persister,
	}
	cell, err := b.Run(context.Background(), insts[0], env)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if cell == nil {
		t.Fatal("nil CellResult")
	}
	if err := cell.Validate(); err != nil {
		t.Fatalf("CellResult.Validate: %v", err)
	}
	if !cell.TaskSuccess {
		t.Errorf("small-haystack run missed the needle; notes=%q", cell.Notes)
	}
	if cell.Benchmark != "niah" {
		t.Errorf("Benchmark=%q, want \"niah\"", cell.Benchmark)
	}
	if cell.ScenarioID == "" {
		t.Error("ScenarioID empty")
	}
}

// withTestPersister isolates the persister's CWD-based outputs to a
// temp dir so the test does not write to the repo's .cortex/. Returns
// a cleanup func that restores the previous CWD.
func withTestPersister(t *testing.T, dir string) func() {
	t.Helper()
	prev, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	// Ensure .cortex/db/ exists so the persister can open its files.
	if err := os.MkdirAll(filepath.Join(dir, ".cortex", "db"), 0o755); err != nil {
		t.Fatal(err)
	}
	return func() { _ = os.Chdir(prev) }
}

// TestRunRejectsBadPayload — a runner.Run with an Instance.Payload of
// the wrong type returns an error rather than panicking. Guards
// against future Loaders that forget to set Payload.
func TestRunRejectsBadPayload(t *testing.T) {
	b, _ := benchmarks.Get("niah")
	_, err := b.Run(context.Background(), benchmarks.Instance{ID: "x", Payload: "not-a-Payload"}, benchmarks.Env{Workdir: t.TempDir()})
	if err == nil {
		t.Fatal("expected error for bad payload type")
	}
}

// TestRunNeedleNoLLMUsesTextSearch — exercise the runner over an even
// smaller haystack, confirm the CellResult's Notes carries the depth +
// length + top score + position. This is what an operator reads to
// distinguish an embedder issue from a chunking issue when triaging.
func TestRunNeedleNoLLMUsesTextSearch(t *testing.T) {
	tmp := t.TempDir()
	cleanup := withTestPersister(t, tmp)
	defer cleanup()
	persister, err := evalv2.NewPersister()
	if err != nil {
		t.Fatal(err)
	}
	defer persister.Close()

	b, _ := benchmarks.Get("niah")
	insts, err := b.Load(context.Background(), benchmarks.LoadOpts{
		Filter: map[string]string{
			"lengths": "1k",
			"depths":  "0.0",
			"needle":  "BANANA-NEEDLE-XYZ-7777",
			"seed":    "11",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	cell, err := b.Run(context.Background(), insts[0], benchmarks.Env{Workdir: tmp, Persister: persister})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !strings.Contains(cell.Notes, "depth=0.00") {
		t.Errorf("Notes %q missing depth=0.00", cell.Notes)
	}
	if !strings.Contains(cell.Notes, "length=1k") {
		t.Errorf("Notes %q missing length=1k", cell.Notes)
	}

	// Sanity: cell must round-trip through JSON.
	bb, err := json.Marshal(cell)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(bb), "\"benchmark\":\"niah\"") {
		t.Errorf("JSON missing benchmark field: %s", bb)
	}
}
