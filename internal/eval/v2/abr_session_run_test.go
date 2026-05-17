package eval

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dereksantos/cortex/pkg/config"
	"github.com/dereksantos/cortex/pkg/llm"
)

// cortexProviderFromModel returns the CellResult provider enum that
// matches the REPL's model-id → provider routing. Empty when the model
// id doesn't carry a recognized prefix.
func cortexProviderFromModel(model string) string {
	if !strings.Contains(model, "/") {
		return ProviderOllama
	}
	// Slash-bearing model ids go through NewLLMClient which prefers
	// OpenRouter (keychain) → OpenRouter (env) → Anthropic. We can't
	// distinguish at the cell level without inspecting which resolved,
	// so default to the most common path and let the caller override.
	return ProviderOpenRouter
}

// TestABRSession_Real spawns the REPL twice (Fast pass + Full pass)
// over a short prompt sequence and prints the per-turn quality scores
// and ABR ratio. This is a real LLM-spending test, gated by
// RUN_ABR_SESSION=1 so CI never trips it.
//
// Usage:
//   RUN_ABR_SESSION=1 go test ./internal/eval/v2 \
//       -run TestABRSession_Real -v -timeout 30m
//
// Expected outcome on an EMPTY per-eval store (the default for this
// test): both passes get `{"empty":true}` from cortex_search, the
// agent falls back to its prior on both, the judge scores them
// similarly, and ABR ≈ 1.0 (degenerate case). The plumbing being
// proven correct on the degenerate case is the prerequisite for
// running on a seeded store where the Fast/Full delta is real.
func TestABRSession_Real(t *testing.T) {
	if os.Getenv("RUN_ABR_SESSION") == "" {
		t.Skip("set RUN_ABR_SESSION=1 to run (spawns the REPL twice and pays for judge calls)")
	}

	binary := os.Getenv("CORTEX_BINARY")
	if binary == "" {
		// Resolve relative to repo root; the test runs from internal/eval/v2.
		abs, err := filepath.Abs("../../../bin/cortex")
		if err != nil {
			t.Fatalf("resolve binary path: %v", err)
		}
		binary = abs
	}
	if _, err := os.Stat(binary); err != nil {
		t.Fatalf("cortex binary not found at %s — run `go build -o bin/cortex ./cmd/cortex` first", binary)
	}

	// REPL agent model. Defaults to local Ollama so this test doesn't
	// burn cloud credits — the OpenRouter keychain key is out of funds
	// as of 2026-05-17 per the prior session-close journal entry. Slash
	// in name → OpenRouter / Anthropic; no slash → Ollama (per
	// repl.go:resolveAPIURL).
	model := os.Getenv("CORTEX_ABR_MODEL")
	if model == "" {
		model = "qwen2.5-coder:1.5b"
	}

	// Judge selection bypasses the keychain-first OpenRouter resolution
	// in NewLLMClient (which would otherwise return the credit-exhausted
	// OpenRouter client). Direct constructors per backend instead.
	//
	// CORTEX_ABR_JUDGE: empty → try Anthropic direct, fall back to local
	// gemma2:2b. Slash → NewLLMClient (OpenRouter / Anthropic). No slash
	// → NewOllamaClient with that model id.
	cfg := config.Default()
	judgeModel := os.Getenv("CORTEX_ABR_JUDGE")
	var judge llm.Provider
	var source string

	pick := func(name string) (llm.Provider, string, string, bool) {
		if strings.Contains(name, "/") {
			j, src, err := llm.NewLLMClient(cfg, llm.WithModel(name))
			if err != nil {
				return nil, name, "", false
			}
			return j, name, string(src), j.IsAvailable()
		}
		oc := llm.NewOllamaClient(cfg)
		if ms, ok := any(oc).(interface{ SetModel(string) }); ok {
			ms.SetModel(name)
		}
		return oc, name, "ollama-direct", oc.IsAvailable()
	}

	if judgeModel == "" {
		ac := llm.NewAnthropicClient(cfg)
		if ac.IsAvailable() {
			judge = ac
			source = "anthropic-direct"
			judgeModel = "claude-haiku-4-5-20251001"
		} else {
			judgeModel = "gemma2:2b" // local fallback when no cloud judge is reachable
		}
	}
	if judge == nil {
		j, m, src, ok := pick(judgeModel)
		if !ok || j == nil {
			t.Fatalf("judge %q unavailable", judgeModel)
		}
		judge = j
		judgeModel = m
		source = src
	}
	t.Logf("judge: %s via %s", judgeModel, source)

	tmp := t.TempDir()
	t.Logf("workdir: %s", tmp)

	// Option-3 intra-session learning scenario: turn 1 establishes a
	// JWT-auth decision, turns 2-4 ask about authentication. With
	// auto-capture live, turn 1's content lands in the per-eval store;
	// turns 2-4's cortex_search should retrieve it. Without auto-capture
	// the store stays empty across all turns and every response says
	// "no prior context."
	//
	// The recurring "JWT" / "authentication" / "session" terms across
	// turns mean Reflex's text-match has something to find.
	prompts := []string{
		"Record this decision: we use JWT tokens for authentication, NOT session-based cookies, because our API needs to be stateless for horizontal scaling. After recording it, confirm in one sentence.",
		"Search the cortex store and tell me: what's our authentication approach? Cite what you find verbatim if possible.",
		"Search the cortex store again. Why did we choose JWT over session-based auth? Cite from any prior captures.",
		"One more search: do we use session-based cookies anywhere in this project?",
	}

	criteria := `Evaluate whether the response uses information actually retrieved from the cortex store (vs. fabricating or answering from prior knowledge).

For turn 1 (decision-recording): full credit for confirming the decision was captured.

For turns 2-4 (retrieval): the project's actual authentication approach is JWT (decided in turn 1 because the API needs to be stateless for horizontal scaling). A correct answer is grounded in that captured decision and ideally cites or paraphrases it. A wrong answer either:
- says the store is empty / no prior captures (it should NOT be empty by turn 2+),
- answers from generic prior knowledge without retrieving,
- contradicts the captured decision (e.g. recommending sessions when we said JWT),
- fabricates details not in the capture.

Score correctness high (0.8-1.0) only when the response clearly grounds in retrieved cortex content. Score low (0-0.3) when the response says the store is empty or doesn't engage with the captured decision.`

	provider := ProviderOllama
	if cortexProviderFromModel(model) != "" {
		provider = cortexProviderFromModel(model)
	}

	opts := ABRSessionOptions{
		ScenarioID:     "abr-intra-session-jwt-v1",
		REPLBinary:     binary,
		Model:          model,
		Workdir:        tmp,
		Prompts:        prompts,
		JudgeCriteria:  criteria,
		Judge:          judge,
		Provider:       provider,
		CortexVersion:  "0.1.0",
		PerPassTimeout: 15 * time.Minute,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()

	t.Log("starting ABR session — this spawns the REPL twice")
	start := time.Now()
	res, err := RunABRSession(ctx, opts)
	if err != nil {
		// Even on partial failure we want the summary printed so we can
		// see how far we got.
		t.Logf("partial result: %+v", res)
		t.Fatalf("RunABRSession: %v", err)
	}
	t.Logf("session completed in %s", time.Since(start).Round(time.Second))

	// Pretty-print the result to the test log so the user sees the
	// numbers without grepping. Also write a JSON blob to /tmp so the
	// adapter author can inspect it.
	pretty, _ := json.MarshalIndent(res, "", "  ")
	t.Logf("ABR session result:\n%s", pretty)
	outPath := filepath.Join(os.TempDir(), fmt.Sprintf("abr-session-%s.json", res.SessionID))
	if err := os.WriteFile(outPath, pretty, 0o644); err == nil {
		t.Logf("wrote summary to %s", outPath)
	}

	if res.TurnsScored == 0 {
		t.Fatal("no turns were scored — REPL likely failed both passes; check the stderr above")
	}
	t.Logf("scored %d/%d turns; mean ABR (over turns where Full>0) = %.3f (denominator: %d)",
		res.TurnsScored, len(prompts), res.MeanABR, res.TurnsFullNonzero)
	t.Logf("mean fast quality = %.3f, mean full quality = %.3f", res.MeanFastScore, res.MeanFullScore)
}
