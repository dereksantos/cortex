// Package commands provides CLI command implementations.
package commands

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"

	evalv2 "github.com/dereksantos/cortex/internal/eval/v2"
	"github.com/dereksantos/cortex/pkg/config"
	"github.com/dereksantos/cortex/pkg/llm"
	"github.com/dereksantos/cortex/pkg/secret"
)

func init() {
	Register(&EvalCommand{})
}

// EvalCommand runs evaluation scenarios.
type EvalCommand struct{}

// Name returns the command name.
func (c *EvalCommand) Name() string { return "eval" }

// Description returns the command description.
func (c *EvalCommand) Description() string {
	return "Run evaluation (subcommands: grid | suite | benchmark; or -s scenario -m model)"
}

// Execute runs the eval command.
//
// Dispatch order (positional subcommands take precedence, mirroring the
// `cortex journal <sub>` pattern):
//
//	cortex eval grid <args>            → executeGrid
//	cortex eval suite <name> [opts]    → runSuite
//	cortex eval benchmark <name> [opts]→ runBenchmark
//
// Any other invocation falls through to the flag-driven path below
// (scenario file + model, --summary / --abr-trend reports, etc.).
// The deprecated `--suite NAME` / `--benchmark NAME` flag forms still
// resolve, but the positional subcommand is the documented path.
func (c *EvalCommand) Execute(ctx *Context) error {
	if len(ctx.Args) > 0 {
		switch ctx.Args[0] {
		case "grid":
			return executeGrid(ctx.Args[1:])
		case "suite":
			if len(ctx.Args) < 2 {
				return fmt.Errorf("cortex eval suite: missing suite name (mechanic | legacy-cognition | journeys)")
			}
			return runSuite(ctx.Args[1], "test/evals/v2", "human", false)
		case "benchmark":
			if len(ctx.Args) < 2 {
				return fmt.Errorf("cortex eval benchmark: missing name (longmemeval | mteb | swebench | niah)")
			}
			return runBenchmark(ctx.Args[1], ctx.Args[2:], false)
		}
	}

	// Parse flags
	scenarioPath := ""
	scenarioDir := "test/evals/v2"
	modelOverride := ""
	verbose := false
	outputFormat := "human"
	useJudge := false
	judgeModel := ""
	measureMode := false
	showSummary := false
	showABRTrend := false
	benchmarkName := ""
	suiteName := ""

	for i := 0; i < len(ctx.Args); i++ {
		arg := ctx.Args[i]
		switch arg {
		case "-v", "--verbose":
			verbose = true
		case "--measure":
			measureMode = true
		case "--benchmark":
			if i+1 < len(ctx.Args) {
				benchmarkName = ctx.Args[i+1]
				i++
			}
		case "--suite":
			if i+1 < len(ctx.Args) {
				suiteName = ctx.Args[i+1]
				i++
			}
		case "-s", "--scenario":
			if i+1 < len(ctx.Args) {
				scenarioPath = ctx.Args[i+1]
				i++
			}
		case "-d", "--dir":
			if i+1 < len(ctx.Args) {
				scenarioDir = ctx.Args[i+1]
				i++
			}
		case "-m", "--model":
			if i+1 < len(ctx.Args) {
				modelOverride = ctx.Args[i+1]
				i++
			}
		case "-o", "--output":
			if i+1 < len(ctx.Args) {
				outputFormat = ctx.Args[i+1]
				i++
			}
		case "--judge":
			useJudge = true
		case "--judge-model":
			if i+1 < len(ctx.Args) {
				judgeModel = ctx.Args[i+1]
				useJudge = true // implicitly enable judge
				i++
			}
		case "--summary":
			showSummary = true
		case "--abr-trend":
			showABRTrend = true
		case "-h", "--help":
			fmt.Println(`Usage: cortex eval [options]

Unified eval system comparing baseline vs Cortex-augmented responses.

Cortex eval drives the cortex coding harness against a scenario, or wraps a
standard benchmark, or runs a special-purpose suite.

  cortex eval -s SCEN.yaml -m MODEL       Run one scenario through the cortex
                                          coding harness (the default — no
                                          --harness flag needed).
  cortex eval grid ...                    Multi-cell grid runner (see
                                          'cortex eval grid --help').
  cortex eval suite NAME                  Special-purpose suite
                                          (mechanic | legacy-cognition | journeys).
  cortex eval benchmark NAME [opts]       Wrapped benchmark
                                          (longmemeval | mteb | swebench | niah).
  cortex eval --summary | --abr-trend     Read-only reports from past runs.

Options:
  -s, --scenario FILE    Scenario YAML (required for coding-harness runs)
  -d, --dir DIR          Scenario directory (default: test/evals/v2)
  -m, --model NAME       Model id (required for coding-harness runs)
  -o, --output FORMAT    Output: human, json (default: human)
  -v, --verbose          Verbose output
  --judge                Enable LLM-as-judge scoring on the coding harness
  --judge-model MODEL    Model for judge (default: same as eval model)
  --measure              MOVED: use 'cortex measure --self-eval' instead
  --summary              Show lift trend over recent runs
  --abr-trend            Show ABR progression across runs
  --benchmark NAME       Run a dataset-driven benchmark (longmemeval, mteb, swebench, niah)
  --suite NAME           Run a special-purpose suite: mechanic | legacy-cognition | journeys
  --subset NAME          Benchmark subset (e.g. oracle | verified | NFCorpus)
  --limit N              Cap number of benchmark instances (for mteb: caps queries scored)
  --length N             NIAH only: haystack token count (8k|16k|32k|64k|4000…); repeatable
  --depth F              NIAH only: needle depth 0.0..1.0; repeatable, default 0.0,0.5,1.0
  --needle STR           NIAH only: needle text (default: "The secret recipe code is 4F-9X-2B.")
  --seed N               NIAH only: deterministic filler seed (default: 1)
  --filler MODE          NIAH only: filler corpus (adversarial|lorem; default: adversarial)
  --strategy LIST        Comma-separated strategies for benchmark cells (baseline,cortex)
  --question-type NAME   LongMemEval: ability filter (single-hop|multi-hop|temporal|knowledge-update|abstention); repeatable
  --repo SLUG            SWE-bench: (repeatable) restrict to upstream repo (e.g. django/django)
  --docker-image-prefix PFX  SWE-bench: override scoring image prefix
  --git-cache-dir DIR    SWE-bench: reuse a git mirror for repo clones
  --tasks NAME           MTEB only: task name (Phase A accepts only NFCorpus; default NFCorpus)
  --rerank               MTEB only: rerank top-K via cognition.Reflect (adds ~10-20x latency)
  --embedder ID          MTEB only: reserved for future embedder switch (default: Hugot MiniLM)
  -h, --help             Show this help

Examples:
  cortex eval -s auth.yaml -m anthropic/claude-3-5-haiku
  cortex eval -s auth.yaml -m ollama/qwen2.5-coder:1.5b --judge --judge-model gemma2:2b
  cortex eval --summary                    # Show lift trend
  cortex eval --abr-trend                  # Show ABR progression
  cortex eval benchmark longmemeval --subset oracle --limit 5 --strategy baseline,cortex --judge
  cortex eval benchmark swebench --subset verified --limit 3 --model anthropic/claude-3-5-haiku --strategy baseline,cortex
  cortex eval suite mechanic
  cortex eval grid --models openai/gpt-oss-20b:free,anthropic/claude-haiku-4-5 --strategies baseline,cortex`)
			return nil
		default:
			// Support --suite=<name> / --benchmark=<name> joined form
			// not covered by the case arms above.
			if strings.HasPrefix(arg, "--suite=") {
				suiteName = strings.TrimPrefix(arg, "--suite=")
			}
		}
	}

	// Handle --abr-trend flag
	if showABRTrend {
		persister, err := evalv2.NewPersister()
		if err != nil {
			return fmt.Errorf("failed to open database: %w", err)
		}
		defer persister.Close()

		points, err := persister.GetABRTrend(10)
		if err != nil {
			return fmt.Errorf("failed to get ABR trend: %w", err)
		}
		evalv2.ReportABRTrend(os.Stdout, points)
		return nil
	}

	// Handle --summary flag
	if showSummary {
		persister, err := evalv2.NewPersister()
		if err != nil {
			return fmt.Errorf("failed to open database: %w", err)
		}
		defer persister.Close()

		abrs, err := persister.GetTrend(10)
		if err != nil {
			return fmt.Errorf("failed to get trend: %w", err)
		}
		evalv2.ReportTrend(os.Stdout, abrs)
		return nil
	}

	// --measure mode moved to `cortex measure --self-eval`. Reject the
	// flag with a redirect so existing scripts get a clear pointer.
	if measureMode {
		return fmt.Errorf("`cortex eval --measure` moved to `cortex measure --self-eval` (audit B). Run: cortex measure --self-eval -p <provider> -m <model>")
	}

	// BENCHMARK MODE: dataset-driven eval (LongMemEval, MTEB, SWE-bench,
	// NIAH). Dispatched when --benchmark is set; the per-benchmark
	// package owns its loader, scorer, and CLI flag parsing.
	if benchmarkName != "" {
		return runBenchmark(benchmarkName, ctx.Args, verbose)
	}

	// SUITE MODE: special-purpose eval families (mechanic / legacy-cognition
	// / journeys). Dispatched when --suite is set. See eval_suite.go.
	if suiteName != "" {
		return runSuite(suiteName, scenarioDir, outputFormat, verbose)
	}

	// CORTEX CODING HARNESS (default): when -s scenario and -m model are
	// both set and no other mode dispatch matched, run the scenario through
	// the cortex coding harness. Cortex is the only harness (D1), so this
	// is the implicit eval path — no flag needed.
	if scenarioPath != "" && modelOverride != "" {
		return runCortexCodingHarness(scenarioPath, modelOverride, judgeModel, useJudge, verbose)
	}

	return fmt.Errorf("nothing to run. Use one of:\n" +
		"  cortex eval -s SCEN.yaml -m MODEL    (cortex coding harness — the default)\n" +
		"  cortex eval grid ...                 (multi-cell grid; see `cortex eval grid --help`)\n" +
		"  cortex eval suite NAME               (suites: mechanic, legacy-cognition, journeys)\n" +
		"  cortex eval benchmark NAME ...       (benchmarks: longmemeval, mteb, swebench, niah)\n" +
		"  cortex eval --summary | --abr-trend  (read-only reports from past runs)")
}

// loadEvalConfig loads the config for eval command.
func loadEvalConfig() (*config.Config, error) {
	projectRoot, err := os.Getwd()
	if err != nil {
		return nil, err
	}

	configPath := fmt.Sprintf("%s/.cortex/config.json", projectRoot)
	return config.Load(configPath)
}

// runCortexCodingHarness is the dispatch path for `cortex eval
// --harness cortex -s <scenario> -m <model>`. The coding-harness eval
// goes through internal/harness (agent loop) and internal/eval/v2's
// coding_runner. It does NOT load the standard scenario YAML format
// — the coding scenarios have their own schema (CodingScenario).
func runCortexCodingHarness(scenarioPath, model, judgeModel string, useJudge, verbose bool) error {
	if scenarioPath == "" {
		return fmt.Errorf("--harness cortex requires -s <scenario.yaml>")
	}
	if model == "" {
		return fmt.Errorf("--harness cortex requires -m <model> (e.g. anthropic/claude-3-5-haiku, qwen/qwen-2.5-coder-32b-instruct)")
	}

	scenario, err := evalv2.LoadCodingScenario(scenarioPath)
	if err != nil {
		return fmt.Errorf("load coding scenario: %w", err)
	}
	if verbose {
		fmt.Printf("[cortex-harness] scenario=%s mode=%s tries=%d model=%s\n",
			scenario.ID, scenario.Mode, scenario.MaxTries, model)
	}

	harness, err := evalv2.NewCortexHarness(model)
	if err != nil {
		return fmt.Errorf("init harness: %w", err)
	}

	var judgeProvider llm.Provider
	if useJudge {
		// The judge is always OpenRouter (same auth path). Use the
		// supplied judge-model, falling back to the eval model itself.
		jm := judgeModel
		if jm == "" {
			jm = model
		}
		jp, err := newOpenRouterJudgeForCoding(jm)
		if err != nil {
			return fmt.Errorf("init judge: %w", err)
		}
		judgeProvider = jp
		if verbose {
			fmt.Printf("[cortex-harness] judge model: %s\n", jm)
		}
	}

	persister, err := evalv2.NewPersister()
	if err != nil {
		return fmt.Errorf("open eval persister: %w", err)
	}
	defer persister.Close()

	ctx := context.Background()
	res, err := evalv2.RunCodingScenario(ctx, scenario, harness, judgeProvider, persister, verbose)
	if err != nil {
		return fmt.Errorf("run scenario: %w", err)
	}

	reportCodingRun(os.Stdout, res)
	if !res.Passed {
		return fmt.Errorf("coding eval failed: %d attempts, no TaskSuccess", len(res.Attempts))
	}
	return nil
}

// newOpenRouterJudgeForCoding builds an OpenRouter client wired with
// the keychain-resolved API key, with model pinned. Used as the LLM
// judge for the coding harness's qualitative-correctness scoring.
//
// Lives here (not in pkg/llm) because resolving the keychain key
// would invert layering — pkg/secret is layered above pkg/llm only
// here at the command boundary.
func newOpenRouterJudgeForCoding(model string) (llm.Provider, error) {
	key, _, err := secret.MustOpenRouterKey()
	if err != nil {
		return nil, err
	}
	c := llm.NewOpenRouterClientWithKey(nil, key)
	c.SetModel(model)
	return c, nil
}

// reportCodingRun prints a human-readable summary of a CodingRunResult.
func reportCodingRun(w io.Writer, r *evalv2.CodingRunResult) {
	fmt.Fprintf(w, "\n=== Cortex Coding Harness — %s ===\n", r.Scenario.ID)
	fmt.Fprintf(w, "Mode:        %s (%d attempts)\n", r.Scenario.Mode, len(r.Attempts))
	fmt.Fprintf(w, "Loop root:   %s\n", r.LoopRoot)
	if r.Passed {
		fmt.Fprintf(w, "Result:      PASS (winning run: %s)\n", r.WinningRunID)
	} else {
		fmt.Fprintf(w, "Result:      FAIL\n")
	}
	fmt.Fprintln(w)
	for _, a := range r.Attempts {
		status := "FAIL"
		if a.TaskSuccess {
			status = "PASS"
		}
		fmt.Fprintf(w, "Attempt %d (%s): %s | turns=%d tokens=%d/%d cost=$%.4f latency=%dms\n",
			a.Attempt, a.SessionID, status,
			a.HarnessResult.AgentTurnsTotal,
			a.HarnessResult.TokensIn, a.HarnessResult.TokensOut,
			a.HarnessResult.CostUSD,
			a.HarnessResult.LatencyMs)
		fmt.Fprintf(w, "  build=%v frames=%d/%d judge=%v\n",
			a.Frames.BuildOK, a.Frames.Passed, a.Frames.Passed+a.Frames.Failed, a.Judge.Pass)
		if a.Judge.Verdict != "" && !a.Judge.Pass {
			fmt.Fprintf(w, "  judge verdict: %s\n", a.Judge.Verdict)
		}
		if a.Err != nil {
			fmt.Fprintf(w, "  error: %v\n", a.Err)
		}
	}
}
