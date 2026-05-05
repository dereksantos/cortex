// Command library-eval drives the library-service multi-session eval end to
// end. Not meant to be a permanent CLI surface — a deliberate one-off tool
// invoked manually when the operator wants to spend Claude CLI time and
// API credits on a real comparison run.
//
// Usage:
//
//	go run ./cmd/library-eval --cond=baseline --model=claude-haiku-4-5-20251001
//	go run ./cmd/library-eval --cond=cortex   --model=claude-haiku-4-5-20251001
//	go run ./cmd/library-eval --cond=both     --model=claude-haiku-4-5-20251001
//
//	# Local-model thesis run via Aider + Ollama (Plan 05):
//	go run ./cmd/library-eval --harness=aider --model=ollama/qwen2.5-coder:1.5b --cond=both
//
// Outputs per-condition run JSON and (for --cond=both) a comparison report
// to --out (default /tmp/cortex-libsvc-runs).
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"time"

	eval "github.com/dereksantos/cortex/internal/eval/v2"
)

func main() {
	var (
		cond        = flag.String("cond", "baseline", "baseline | cortex | both")
		model       = flag.String("model", "claude-haiku-4-5-20251001", "model id (Claude model for harness=claude, e.g. ollama/qwen2.5-coder:1.5b for harness=aider)")
		harness     = flag.String("harness", "claude", "claude | aider — which CLI to drive sessions through")
		aiderBinary = flag.String("aider-binary", "", "path to aider binary (default: $AIDER_BINARY or PATH lookup); only used when --harness=aider")
		repo        = flag.String("repo", ".", "repo root (where test/evals/library-service lives)")
		outDir      = flag.String("out", "/tmp/cortex-libsvc-runs", "where to write run reports")
		keep        = flag.Bool("keep", true, "keep workdirs after the run for inspection")
	)
	flag.Parse()

	if err := os.MkdirAll(*outDir, 0o755); err != nil {
		log.Fatalf("mkdir out: %v", err)
	}

	repoAbs, err := filepath.Abs(*repo)
	if err != nil {
		log.Fatalf("repo abs: %v", err)
	}
	specDir := filepath.Join(repoAbs, "test", "evals", "library-service")
	seedDir := filepath.Join(repoAbs, "test", "evals", "projects", "library-service-seed")

	if _, err := os.Stat(specDir); err != nil {
		log.Fatalf("spec dir not found at %s: %v", specDir, err)
	}
	if _, err := os.Stat(seedDir); err != nil {
		log.Fatalf("seed dir not found at %s: %v", seedDir, err)
	}

	switch *harness {
	case "claude", "aider":
	default:
		log.Fatalf("unknown --harness=%s (want claude | aider)", *harness)
	}

	ctx := context.Background()
	e := eval.NewLibraryServiceEvaluator(specDir, seedDir)
	e.SetVerbose(true)

	runOne := func(c eval.LibraryServiceCondition) *eval.LibraryServiceRun {
		log.Printf("=== starting %s with harness=%s model=%s ===", c, *harness, *model)
		start := time.Now()
		run, err := runCondition(ctx, e, c, *harness, *aiderBinary, *model)
		if err != nil {
			log.Fatalf("%s run failed: %v", c, err)
		}
		dur := time.Since(start)
		log.Printf("=== %s done in %s ===", c, dur.Round(time.Second))
		log.Printf("workdir: %s", run.WorkDir)
		log.Printf("score:  shape=%.3f naming=%.3f smell=%.3f testParity=%.3f e2e=%.3f",
			run.Score.ShapeSimilarity, run.Score.NamingAdherence,
			run.Score.SmellDensity, run.Score.TestParity, run.Score.EndToEndPassRate)
		for _, s := range run.SessionLog {
			log.Printf("  session %-30s build=%v tests=%v files=%d  %s",
				s.SessionID, s.BuildOK, s.TestsOK, len(s.FilesChanged),
				time.Duration(s.DurationMs)*time.Millisecond)
		}

		runFile := filepath.Join(*outDir, fmt.Sprintf("%s.json", c))
		f, err := os.Create(runFile)
		if err != nil {
			log.Fatalf("create %s: %v", runFile, err)
		}
		enc := json.NewEncoder(f)
		enc.SetIndent("", "  ")
		if err := enc.Encode(run); err != nil {
			log.Fatalf("encode run: %v", err)
		}
		_ = f.Close()
		log.Printf("wrote %s", runFile)

		if !*keep {
			_ = run.Cleanup()
		}
		return run
	}

	switch *cond {
	case "baseline":
		runOne(eval.ConditionBaseline)
	case "cortex":
		runOne(eval.ConditionCortex)
	case "frontier":
		runOne(eval.ConditionFrontier)
	case "both":
		baseline := runOne(eval.ConditionBaseline)
		cortex := runOne(eval.ConditionCortex)
		report := eval.CompareRuns(baseline, cortex, nil)
		reportFile := filepath.Join(*outDir, "compare.md")
		if err := os.WriteFile(reportFile, []byte(report), 0o644); err != nil {
			log.Fatalf("write compare: %v", err)
		}
		log.Printf("wrote %s", reportFile)
		fmt.Println()
		fmt.Println(report)
	default:
		log.Fatalf("unknown --cond=%s (want baseline | cortex | frontier | both)", *cond)
	}
}

// runCondition wires the right harness for --harness, then dispatches to
// the evaluator. The Claude path stays on `e.Run` (Plan 02 back-compat,
// constructs ClaudeCLIHarness internally). The Aider path constructs
// AiderHarness here and routes through RunWithInjector so the cortex
// condition still gets its injector wired correctly.
func runCondition(ctx context.Context, e *eval.LibraryServiceEvaluator, c eval.LibraryServiceCondition, harness, aiderBinary, model string) (*eval.LibraryServiceRun, error) {
	if harness == "claude" {
		return e.Run(ctx, c, model)
	}
	// harness == "aider"
	h, err := eval.NewAiderHarness(aiderBinary, model)
	if err != nil {
		return nil, fmt.Errorf("init aider harness: %w", err)
	}
	switch c {
	case eval.ConditionBaseline, eval.ConditionFrontier:
		return e.RunWithInjector(ctx, c, model, h, eval.NoOpInjector{})
	case eval.ConditionCortex:
		return e.RunCortexWithHarness(ctx, model, h)
	default:
		return nil, fmt.Errorf("unknown condition %q", c)
	}
}
