package accumulator

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dereksantos/cortex/internal/journal"
	"github.com/dereksantos/cortex/pkg/llm"
)

// TestAccumulatorVsNaive_Shape is the always-runnable shape check.
// No real LLM — runs both paths against a hard-coded "echo the
// observation" provider so the scaffolding is exercised in CI even
// without a model. The naive path's prompt should clearly exceed a
// modest n_ctx budget; the accumulator path's final prompt should
// fit. Whether the model actually answers correctly is the env-
// gated test below.
func TestAccumulatorVsNaive_Shape(t *testing.T) {
	scn := DefaultScenario()
	p := &echoProvider{}

	naive, err := RunNaive(context.Background(), p, scn, 4096)
	if err != nil {
		t.Fatalf("RunNaive: %v", err)
	}
	if !naive.OverflowedNCtx {
		t.Errorf("naive prompt should overflow 4096-token budget; got %d tokens", naive.PromptTokens)
	}

	acc, err := RunAccumulator(context.Background(), p, scn, 600, 4096, "")
	if err != nil {
		t.Fatalf("RunAccumulator: %v", err)
	}
	if acc.OverflowedNCtx {
		t.Errorf("accumulator final prompt should fit 4096-token budget; got %d tokens", acc.PromptTokens)
	}
	if len(acc.AccumulatorTrajectoy) != len(scn.Observations) {
		t.Errorf("trajectory len=%d, want %d", len(acc.AccumulatorTrajectoy), len(scn.Observations))
	}
	for i, tok := range acc.AccumulatorTrajectoy {
		if tok > 600 {
			t.Errorf("snapshot at step %d = %d tokens > 600 budget", i+1, tok)
		}
	}
	t.Logf("naive: %d prompt tokens, overflowed=%v", naive.PromptTokens, naive.OverflowedNCtx)
	t.Logf("accum: %d prompt tokens at final synthesis, overflowed=%v, trajectory=%v",
		acc.PromptTokens, acc.OverflowedNCtx, acc.AccumulatorTrajectoy)
}

// TestAccumulator_JournalWiring confirms the eval driver writes a
// journal entry per accumulator step. Hermetic — uses the echo
// provider so no LLM is needed; the journal hook contract is what
// matters here, not the snapshot quality.
func TestAccumulator_JournalWiring(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "think")
	scn := DefaultScenario()
	_, err := RunAccumulator(context.Background(), echoProvider{}, scn, 600, 4096, dir)
	if err != nil {
		t.Fatalf("RunAccumulator: %v", err)
	}
	got := countJournalEntries(t, dir)
	if got != len(scn.Observations) {
		t.Errorf("journal entries = %d, want %d (one per accumulator step)", got, len(scn.Observations))
	}
}

// TestAccumulatorVsNaive_Live runs the scenario against a real
// OpenAI-compatible endpoint. Gated on env so unit suites stay
// hermetic. Required env:
//
//	CORTEX_EVAL_ENDPOINT — base URL (e.g. http://localhost:13305/v1)
//	CORTEX_EVAL_MODEL    — model ID (e.g. Qwen3-Coder-30B-A3B-Instruct-GGUF)
//
// Optional:
//
//	CORTEX_EVAL_NCTX     — int n_ctx for overflow scoring (default 4096)
//	CORTEX_EVAL_BUDGET   — int snapshot max_tokens (default 600)
//
// Compares naive vs accumulator path on fact-presence; logs the
// answers + trajectory so a human can eyeball where compression went
// wrong (or right).
func TestAccumulatorVsNaive_Live(t *testing.T) {
	baseURL := os.Getenv("CORTEX_EVAL_ENDPOINT")
	modelID := os.Getenv("CORTEX_EVAL_MODEL")
	if baseURL == "" || modelID == "" {
		t.Skip("set CORTEX_EVAL_ENDPOINT and CORTEX_EVAL_MODEL to run the live accumulator-vs-naive eval")
	}
	nctx := getenvInt("CORTEX_EVAL_NCTX", 4096)
	snapMax := getenvInt("CORTEX_EVAL_BUDGET", 600)

	p := llm.NewOpenAICompatClient(llm.EndpointConfig{Name: "eval", BaseURL: baseURL})
	p.SetModel(modelID)
	scn := DefaultScenario()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	t.Logf("scenario=%q intent=%q observations=%d nctx=%d snapshot_max_tokens=%d",
		scn.Name, scn.Intent, len(scn.Observations), nctx, snapMax)

	// Run the accumulator first. If the naive baseline crashes on
	// overflow we still have a partial demo from the accumulator
	// path. Writes journal entries to a per-test temp dir so the
	// trajectory is inspectable but doesn't pollute the project
	// journal.
	journalDir := filepath.Join(t.TempDir(), "think")
	acc, accErr := RunAccumulator(ctx, p, scn, snapMax, nctx, journalDir)
	logResult(t, acc, accErr)
	if accErr == nil {
		count := countJournalEntries(t, journalDir)
		t.Logf("  journal entries written: %d", count)
	}

	naive, naiveErr := RunNaive(ctx, p, scn, nctx)
	logResult(t, naive, naiveErr)

	// We don't fail the test on the accumulator missing facts — the
	// demo's value is the comparison + trajectory. But we DO want a
	// loud signal when neither path works at all, since that means
	// the eval scaffolding is broken.
	if accErr != nil && naiveErr != nil {
		t.Fatalf("both paths failed; eval scaffolding probably broken. acc=%v naive=%v", accErr, naiveErr)
	}
}

func logResult(t *testing.T, r Result, err error) {
	t.Helper()
	t.Logf("=== %s ===", strings.ToUpper(r.Path))
	if err != nil {
		t.Logf("  ERROR: %v", err)
		return
	}
	t.Logf("  prompt_tokens=%d overflowed_nctx=%v", r.PromptTokens, r.OverflowedNCtx)
	t.Logf("  llm_calls=%d latency_ms=%d", r.TotalLLMCalls, r.TotalLatencyMS)
	t.Logf("  facts_found=%v", r.FactsFound)
	t.Logf("  facts_missing=%v", r.FactsMissing)
	if len(r.AccumulatorTrajectoy) > 0 {
		t.Logf("  snapshot_tokens_per_step=%v", r.AccumulatorTrajectoy)
	}
	if len(r.FinalAnswer) > 0 {
		preview := r.FinalAnswer
		if len(preview) > 500 {
			preview = preview[:500] + "…"
		}
		t.Logf("  answer: %s", preview)
	}
}

func getenvInt(key string, def int) int {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	n := 0
	for _, c := range v {
		if c < '0' || c > '9' {
			return def
		}
		n = n*10 + int(c-'0')
	}
	if n <= 0 {
		return def
	}
	return n
}

// countJournalEntries reads journal files in dir and reports how
// many think.accumulator_update entries they contain. Used by the
// live test to confirm the journaling hook fired for every step.
func countJournalEntries(t *testing.T, dir string) int {
	t.Helper()
	r, err := journal.NewReader(dir)
	if err != nil {
		t.Logf("  open journal reader: %v", err)
		return 0
	}
	defer r.Close()
	n := 0
	for {
		e, err := r.Next()
		if err != nil || e == nil {
			break
		}
		if e.Type == journal.TypeThinkAccumulatorUpdate {
			n++
		}
	}
	return n
}

// echoProvider returns a fixed string. Used by the shape test —
// the test only cares about prompt size + trajectory, not what the
// model "answers".
type echoProvider struct{}

func (echoProvider) Name() string      { return "echo" }
func (echoProvider) IsAvailable() bool { return true }
func (echoProvider) Generate(_ context.Context, _ string) (string, error) {
	return "ok", nil
}
func (echoProvider) GenerateWithSystem(_ context.Context, _, _ string) (string, error) {
	return "ok", nil
}
func (echoProvider) GenerateWithStats(_ context.Context, _ string) (string, llm.GenerationStats, error) {
	return "ok", llm.GenerationStats{InputTokens: 10, OutputTokens: 1}, nil
}
