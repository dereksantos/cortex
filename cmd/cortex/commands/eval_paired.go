// Package commands — `cortex eval paired` subcommand.
//
// Runs the same coding scenario across three conditions
// (small_alone, small + Cortex, frontier_alone) and writes a
// cost-quality JSONL row per condition. This is the Tier 2c
// substrate from docs/eval-strategy.md.
package commands

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/dereksantos/cortex/internal/eval/paired"
	"github.com/dereksantos/cortex/pkg/llm"
)

// executePaired parses the `cortex eval paired` flag set and runs the
// three canonical conditions against the supplied scenario.
//
// Three conditions are hard-coded: small_alone, small_with_cortex,
// frontier_alone. The flag surface controls which models fill those
// slots and (optionally) which OpenAI-compatible endpoint to bind for
// the small-model slot.
func executePaired(args []string) error {
	fs := flag.NewFlagSet("paired", flag.ContinueOnError)
	fs.SetOutput(io.Discard) // suppress flag's own error spam; we surface a clear usage block instead

	scenario := fs.String("scenario", "", "Coding scenario YAML (required)")
	smallModel := fs.String("small-model", "", "Small-model id (required)")
	frontierModel := fs.String("frontier-model", "", "Frontier-model id (required)")
	smallEndpointURL := fs.String("small-endpoint", "", "OpenAI-compat endpoint root for the small model (optional; uses OpenRouter when empty)")
	smallEndpointName := fs.String("small-endpoint-name", "", "Short name for the small-model endpoint (defaults to host of --small-endpoint)")
	smallEndpointKey := fs.String("small-endpoint-key", "", "API key for the small endpoint (optional; many local endpoints accept none)")
	out := fs.String("out", ".cortex/db/paired_results.jsonl", "Output JSONL path (created if missing)")
	verbose := fs.Bool("verbose", false, "Verbose progress output")

	if err := fs.Parse(args); err != nil {
		printPairedUsage()
		return err
	}

	if *scenario == "" || *smallModel == "" || *frontierModel == "" {
		printPairedUsage()
		return fmt.Errorf("cortex eval paired: --scenario, --small-model, --frontier-model are required")
	}

	var smallEndpoint *llm.EndpointConfig
	if *smallEndpointURL != "" {
		name := *smallEndpointName
		if name == "" {
			name = endpointShortName(*smallEndpointURL)
		}
		smallEndpoint = &llm.EndpointConfig{
			Name:    name,
			BaseURL: *smallEndpointURL,
			APIKey:  *smallEndpointKey,
		}
	}

	conditions := []paired.Condition{
		{Name: "small_alone", Model: *smallModel, Endpoint: smallEndpoint, UseCortex: false},
		{Name: "small_with_cortex", Model: *smallModel, Endpoint: smallEndpoint, UseCortex: true},
		{Name: "frontier_alone", Model: *frontierModel, Endpoint: nil, UseCortex: false},
	}

	if *verbose {
		fmt.Printf("[paired] scenario=%s out=%s\n", *scenario, *out)
		for _, c := range conditions {
			ep := "openrouter"
			if c.Endpoint != nil {
				ep = c.Endpoint.Name
			}
			fmt.Printf("[paired]   %s: model=%s endpoint=%s cortex=%v\n", c.Name, c.Model, ep, c.UseCortex)
		}
	}

	h := paired.NewDefaultHarness()
	h.Verbose = *verbose
	results, err := paired.Run(context.Background(), *scenario, conditions, h, *out)
	if err != nil {
		return fmt.Errorf("paired run: %w", err)
	}

	reportPaired(os.Stdout, results, *out)
	return nil
}

// reportPaired prints the three condition rows in cost-ascending
// order — the natural shape for inspecting the cost-quality Pareto
// frontier. Numbers come from the runner's Results; nothing is
// recomputed here.
func reportPaired(w io.Writer, rows []paired.Result, outPath string) {
	fmt.Fprintf(w, "\n# Paired run results\n")
	fmt.Fprintf(w, "JSONL: %s\n\n", outPath)
	sorted := paired.SortByCost(rows)
	for _, r := range sorted {
		fmt.Fprintf(w, "  %-20s  model=%-32s  cost=$%.4f  frames=%d/%d  pass=%v  cortex=%v\n",
			r.Condition, r.Model, r.CostUSD, r.FramesPassed, r.FramesPassed+r.FramesFailed, r.Pass, r.UseCortex)
		if r.Err != "" {
			fmt.Fprintf(w, "    err: %s\n", r.Err)
		}
	}
}

func endpointShortName(rawURL string) string {
	s := rawURL
	for _, pfx := range []string{"http://", "https://"} {
		s = strings.TrimPrefix(s, pfx)
	}
	if idx := strings.IndexAny(s, "/?:"); idx >= 0 {
		s = s[:idx]
	}
	if s == "" {
		return "endpoint"
	}
	return s
}

func printPairedUsage() {
	fmt.Println(`Usage: cortex eval paired --scenario PATH --small-model M --frontier-model M [flags]

Runs a coding scenario across three conditions and writes one JSONL row each:
  small_alone         — small model, no cortex_search
  small_with_cortex   — small model, cortex_search + Cortex DAG
  frontier_alone      — frontier model, no cortex_search

Required:
  --scenario PATH         Coding scenario YAML (e.g. test/evals/v2/coding/conways-game-of-life-single.yaml)
  --small-model NAME      Small-model id (OpenRouter form, or bare id if --small-endpoint is set)
  --frontier-model NAME   Frontier-model id (OpenRouter)

Optional:
  --small-endpoint URL          OpenAI-compatible endpoint root for the small model (e.g. http://localhost:11434/v1)
  --small-endpoint-name STR     Short name for telemetry (defaults to URL host)
  --small-endpoint-key STR      API key (often empty for local endpoints)
  --out PATH                    Output JSONL path (default: .cortex/db/paired_results.jsonl)
  --verbose                     Per-condition progress

Examples:
  cortex eval paired \
    --scenario test/evals/v2/coding/conways-game-of-life-single.yaml \
    --small-model qwen/qwen-2.5-coder-7b-instruct \
    --frontier-model anthropic/claude-3-5-sonnet

  cortex eval paired \
    --scenario test/evals/v2/coding/conways-game-of-life-single.yaml \
    --small-model Qwen3-Coder-30B-A3B-Instruct-GGUF \
    --small-endpoint http://localhost:13305/v1 \
    --frontier-model anthropic/claude-3-5-sonnet`)
}
