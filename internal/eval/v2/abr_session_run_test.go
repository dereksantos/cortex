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

	prompts := []string{
		"Search the cortex store for any prior context about authentication patterns. If you find any, summarize. If not, say so plainly.",
		"Now search the cortex store for prior notes about error handling conventions. Summarize what you find or note it's empty.",
		"Finally, search the cortex store for testing conventions and report what you find.",
	}

	criteria := `The response should:
1. Have actually called the cortex_search tool (not just answered from prior knowledge).
2. Accurately reflect what cortex_search returned — if empty, say so; if results, summarize them faithfully.
3. Be concise and not hallucinate context that wasn't retrieved.
A perfect answer calls cortex_search, accurately reports its findings, and adds no fabricated detail.`

	provider := ProviderOllama
	if cortexProviderFromModel(model) != "" {
		provider = cortexProviderFromModel(model)
	}

	opts := ABRSessionOptions{
		ScenarioID:     "abr-degenerate-empty-store-v1",
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
