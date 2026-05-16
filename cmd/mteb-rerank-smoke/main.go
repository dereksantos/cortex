// One-shot smoke for mteb --rerank comparisons.
//
// Reuses a stable workdir under /tmp so the corpus is indexed once and
// follow-up runs (different rerank models/providers) only pay the
// retrieval+rerank cost.
//
// Env knobs:
//
//	CORTEX_MTEB_RERANK_PROVIDER  = "" (none) | "openrouter" | "ollama" | "anthropic"
//	CORTEX_MTEB_RERANK_MODEL     = provider-specific model ID
//
// When PROVIDER is "openrouter", OPEN_ROUTER_API_KEY must be set in the
// environment of the invoking shell (DO NOT print it; fetch from
// keychain via `security find-generic-password -s cortex-openrouter -w`
// inline at invocation time and pass via env).
//
// Run:
//
//	go run ./cmd/mteb-rerank-smoke               # baseline only
//	CORTEX_MTEB_RUNOPTS=rerank go run ./cmd/mteb-rerank-smoke
//	CORTEX_MTEB_RUNOPTS=rerank \
//	  CORTEX_MTEB_RERANK_PROVIDER=openrouter \
//	  CORTEX_MTEB_RERANK_MODEL=anthropic/claude-haiku-4-5 \
//	  go run ./cmd/mteb-rerank-smoke
package main

import (
	"context"
	"fmt"
	"os"

	"github.com/dereksantos/cortex/internal/eval/benchmarks"
	_ "github.com/dereksantos/cortex/internal/eval/benchmarks/mteb"
	"github.com/dereksantos/cortex/pkg/config"
	"github.com/dereksantos/cortex/pkg/llm"
)

func main() {
	workdir := "/tmp/mteb-rerank-shared"
	if err := os.MkdirAll(workdir, 0o755); err != nil {
		fmt.Fprintln(os.Stderr, "mkdir:", err)
		os.Exit(1)
	}

	b, err := benchmarks.Get("mteb")
	if err != nil {
		fmt.Fprintln(os.Stderr, "get:", err)
		os.Exit(1)
	}

	ctx := context.Background()
	insts, err := b.Load(ctx, benchmarks.LoadOpts{Subset: "NFCorpus", Limit: 20})
	if err != nil {
		fmt.Fprintln(os.Stderr, "load:", err)
		os.Exit(1)
	}

	provider := buildProvider()
	env := benchmarks.Env{Workdir: workdir, Verbose: true, Provider: provider}

	cell, err := b.Run(ctx, insts[0], env)
	if err != nil {
		fmt.Fprintln(os.Stderr, "run:", err)
		os.Exit(1)
	}
	mode := "baseline"
	if os.Getenv("CORTEX_MTEB_RUNOPTS") == "rerank" {
		mode = "rerank"
	}
	fmt.Printf("\nMODE=%s PROVIDER=%s MODEL=%s\n%s\n",
		mode, os.Getenv("CORTEX_MTEB_RERANK_PROVIDER"),
		os.Getenv("CORTEX_MTEB_RERANK_MODEL"), cell.Notes)
}

// buildProvider honors CORTEX_MTEB_RERANK_PROVIDER. Empty value defers
// to the runner's defaultLocalProvider (Ollama → Anthropic).
func buildProvider() llm.Provider {
	switch os.Getenv("CORTEX_MTEB_RERANK_PROVIDER") {
	case "openrouter":
		cfg := config.Default()
		c := llm.NewOpenRouterClient(cfg)
		if m := os.Getenv("CORTEX_MTEB_RERANK_MODEL"); m != "" {
			c.SetModel(m)
		}
		if !c.IsAvailable() {
			fmt.Fprintln(os.Stderr, "openrouter not available (OPEN_ROUTER_API_KEY not set?); falling back to defaultLocalProvider")
			return nil
		}
		return c
	case "anthropic":
		cfg := config.Default()
		if m := os.Getenv("CORTEX_MTEB_RERANK_MODEL"); m != "" {
			cfg.AnthropicModel = m
		}
		return llm.NewAnthropicClient(cfg)
	case "ollama":
		cfg := config.Default()
		if m := os.Getenv("CORTEX_MTEB_RERANK_MODEL"); m != "" {
			cfg.OllamaModel = m
		}
		return llm.NewOllamaClient(cfg)
	}
	return nil
}
