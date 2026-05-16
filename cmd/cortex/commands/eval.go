// Package commands provides CLI command implementations.
package commands

import (
	"context"
	"fmt"
	"io"
	"os"
	"time"

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
func (c *EvalCommand) Description() string { return "Run evaluation scenarios" }

// Execute runs the eval command.
func (c *EvalCommand) Execute(ctx *Context) error {
	// `cortex eval grid <flags>` dispatches to the cross-harness grid
	// runner (see eval_grid.go). All other usages fall through to the
	// existing v2 scenario evaluator below.
	if len(ctx.Args) > 0 && ctx.Args[0] == "grid" {
		return executeGrid(ctx.Args[1:])
	}

	// Parse flags
	scenarioPath := ""
	scenarioDir := "test/evals/v2"
	providerName := "ollama"
	modelOverride := ""
	verbose := false
	outputFormat := "human"
	dryRun := false
	useJudge := false
	judgeModel := ""
	agenticMode := false
	measureMode := false
	claudeBinary := ""
	showSummary := false
	showABRTrend := false
	compareProviderName := ""
	compareModelOverride := ""
	harnessName := ""
	benchmarkName := ""

	for i := 0; i < len(ctx.Args); i++ {
		arg := ctx.Args[i]
		switch arg {
		case "-v", "--verbose":
			verbose = true
		case "--dry-run":
			dryRun = true
		case "--agentic":
			agenticMode = true
		case "--measure":
			measureMode = true
		case "--harness":
			if i+1 < len(ctx.Args) {
				harnessName = ctx.Args[i+1]
				i++
			}
		case "--benchmark":
			if i+1 < len(ctx.Args) {
				benchmarkName = ctx.Args[i+1]
				i++
			}
		case "--claude-binary":
			if i+1 < len(ctx.Args) {
				claudeBinary = ctx.Args[i+1]
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
		case "-p", "--provider":
			if i+1 < len(ctx.Args) {
				providerName = ctx.Args[i+1]
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
		case "--compare-provider":
			if i+1 < len(ctx.Args) {
				compareProviderName = ctx.Args[i+1]
				i++
			}
		case "--compare-model":
			if i+1 < len(ctx.Args) {
				compareModelOverride = ctx.Args[i+1]
				i++
			}
		case "-h", "--help":
			fmt.Println(`Usage: cortex eval [options]

Unified eval system comparing baseline vs Cortex-augmented responses.

Options:
  -s, --scenario FILE    Run single scenario
  -d, --dir DIR          Scenario directory (default: test/evals/v2)
  -p, --provider NAME    LLM provider: ollama, anthropic, openrouter (default: ollama)
  -m, --model NAME       Model override
  -o, --output FORMAT    Output: human, json (default: human)
  -v, --verbose          Verbose output
  --dry-run              Use mock provider
  --judge                Enable LLM-as-judge scoring (semantic evaluation)
  --judge-model MODEL    Model for judge (default: same as eval model)
  --agentic              Use Claude CLI for agentic evals (measures tool usage)
  --measure              Run Promptability vs quality correlation evals
  --claude-binary PATH   Path to claude binary (default: auto-detect)
  --compare-provider NAME  Frontier provider for MPR comparison (e.g., anthropic)
  --compare-model MODEL    Frontier model override (default: provider default)
  --summary              Show lift trend over recent runs
  --abr-trend            Show ABR progression across runs
  --benchmark NAME       Run a dataset-driven benchmark (longmemeval, mteb, swebench, niah)
  --subset NAME          Benchmark subset (e.g. oracle | verified | NFCorpus)
  --limit N              Cap number of benchmark instances
  --length N             NIAH only: haystack token count (8k|16k|32k|64k|4000…); repeatable
  --depth F              NIAH only: needle depth 0.0..1.0; repeatable, default 0.0,0.5,1.0
  --needle STR           NIAH only: needle text (default: "The secret recipe code is 4F-9X-2B.")
  --seed N               NIAH only: deterministic filler seed (default: 1)
  -h, --help             Show this help

Examples:
  cortex eval                              # Run all scenarios
  cortex eval -s auth.yaml                 # Run single scenario
  cortex eval --judge                      # Use LLM judge for scoring
  cortex eval --judge --judge-model gemma2:2b  # Use specific judge model
  cortex eval --agentic                    # Run with Claude CLI (tool tracking)
  cortex eval --compare-provider anthropic --compare-model claude-haiku-4-5-20251001
  cortex eval --summary                    # Show lift trend
  cortex eval --summary --agentic          # Show tool call reduction trend
  cortex eval --abr-trend                  # Show ABR progression`)
			return nil
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

		if agenticMode {
			// Show agentic tool call reduction trend
			points, err := persister.GetAgenticTrend(10)
			if err != nil {
				return fmt.Errorf("failed to get agentic trend: %w", err)
			}
			evalv2.ReportAgenticTrend(os.Stdout, points)
		} else {
			// Show standard lift trend
			abrs, err := persister.GetTrend(10)
			if err != nil {
				return fmt.Errorf("failed to get trend: %w", err)
			}
			evalv2.ReportTrend(os.Stdout, abrs)
		}
		return nil
	}

	// CORTEX HARNESS MODE: agent loop hosted in-process (internal/harness).
	// Dispatched when `--harness cortex` is set. Requires a single
	// scenario via -s and a model name via -m. Goes through coding_runner,
	// which writes CellResults to the standard SQLite + JSONL + journal
	// fan-out.
	if harnessName == evalv2.HarnessCortex {
		return runCortexCodingHarness(scenarioPath, modelOverride, judgeModel, useJudge, verbose)
	}

	// BENCHMARK MODE: dataset-driven eval (LongMemEval, MTEB, SWE-bench,
	// NIAH). Dispatched when --benchmark is set; the per-benchmark
	// package owns its loader, scorer, and CLI flag parsing.
	// CellResults flow through the standard persister fan-out so analysis
	// pipelines see them alongside scenario-driven results.
	if benchmarkName != "" {
		return runBenchmark(benchmarkName, ctx.Args, verbose)
	}

	// Create provider
	cfg, err := loadEvalConfig()
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}

	// Apply model override
	if modelOverride != "" {
		if providerName == "anthropic" {
			cfg.AnthropicModel = modelOverride
		} else {
			cfg.OllamaModel = modelOverride
		}
	}

	var provider llm.Provider
	if dryRun {
		provider = llm.NewMockProvider(10) // 10ms delay
		if verbose {
			fmt.Println("Using mock provider (dry-run mode)")
		}
	} else {
		switch providerName {
		case "ollama":
			ollamaClient := llm.NewOllamaClient(cfg)
			if !ollamaClient.IsAvailable() {
				return fmt.Errorf("ollama is not running, start with: ollama serve")
			}
			provider = ollamaClient
		case "anthropic":
			anthropicClient := llm.NewAnthropicClient(cfg)
			if !anthropicClient.IsAvailable() {
				return fmt.Errorf("ANTHROPIC_API_KEY not set")
			}
			provider = anthropicClient
		case "openrouter":
			orClient := llm.NewOpenRouterClient(cfg)
			if !orClient.IsAvailable() {
				return fmt.Errorf("OPEN_ROUTER_API_KEY not set (note the underscore — this project uses the underscore form)")
			}
			if modelOverride != "" {
				orClient.SetModel(modelOverride)
			}
			provider = orClient
		default:
			return fmt.Errorf("unknown provider: %s (valid: ollama, anthropic, openrouter)", providerName)
		}
	}

	// Determine actual model name (used for display + judge model
	// fallback). For openrouter, the OpenRouterClient holds the model
	// internally — no cfg field — so we ask it directly.
	var modelName string
	if modelOverride != "" {
		modelName = modelOverride
	} else {
		switch providerName {
		case "anthropic":
			modelName = cfg.AnthropicModel
		case "openrouter":
			if orClient, ok := provider.(*llm.OpenRouterClient); ok {
				modelName = orClient.Model()
			}
		default:
			modelName = cfg.OllamaModel
		}
	}

	// Create judge provider if enabled
	var judgeProvider llm.Provider
	var judgeModelName string
	if useJudge {
		// Determine judge model
		judgeModelName = judgeModel
		if judgeModelName == "" {
			judgeModelName = modelName // Use same model as eval
		}

		if dryRun {
			judgeProvider = llm.NewMockProvider(10)
		} else {
			// Create judge provider (can be different model, same provider type)
			judgeCfg := *cfg
			switch providerName {
			case "anthropic":
				judgeCfg.AnthropicModel = judgeModelName
				judgeProvider = llm.NewAnthropicClient(&judgeCfg)
			case "openrouter":
				orJudge := llm.NewOpenRouterClient(&judgeCfg)
				orJudge.SetModel(judgeModelName)
				judgeProvider = orJudge
			default:
				judgeCfg.OllamaModel = judgeModelName
				judgeProvider = llm.NewOllamaClient(&judgeCfg)
			}
		}

		if verbose {
			fmt.Printf("Using LLM judge: %s\n", judgeModelName)
		}
	}

	// Create compare provider for MPR if requested
	var compareProvider llm.Provider
	var compareModelName string
	if compareProviderName != "" {
		compareModelName = compareModelOverride

		if dryRun {
			compareProvider = llm.NewMockProvider(10)
		} else {
			compareCfg := *cfg
			switch compareProviderName {
			case "anthropic":
				if compareModelName != "" {
					compareCfg.AnthropicModel = compareModelName
				}
				if compareModelName == "" {
					compareModelName = compareCfg.AnthropicModel
				}
				compareProvider = llm.NewAnthropicClient(&compareCfg)
			case "ollama":
				if compareModelName != "" {
					compareCfg.OllamaModel = compareModelName
				}
				if compareModelName == "" {
					compareModelName = compareCfg.OllamaModel
				}
				compareProvider = llm.NewOllamaClient(&compareCfg)
			default:
				return fmt.Errorf("unknown compare provider: %s", compareProviderName)
			}
		}

		if verbose {
			fmt.Printf("Using compare provider: %s/%s (for MPR)\n", compareProviderName, compareModelName)
		}
	}

	// Track start time for duration measurement
	startTime := time.Now()

	// AGENTIC MODE: Use Claude CLI for tool usage measurement
	if agenticMode {
		if dryRun {
			return fmt.Errorf("--agentic mode does not support --dry-run")
		}

		// Build claude CLI args
		var cliArgs []string
		if modelOverride != "" {
			cliArgs = append(cliArgs, "--model", modelOverride)
		}

		agenticEval, err := evalv2.NewAgenticEvaluator(claudeBinary, cliArgs...)
		if err != nil {
			return fmt.Errorf("failed to create agentic evaluator: %w", err)
		}
		agenticEval.SetVerbose(verbose)

		if verbose {
			fmt.Println("Running in agentic mode (Claude CLI with tool tracking)")
		}

		// Run agentic eval
		var agenticResults *evalv2.AgenticResults
		if scenarioPath != "" {
			scenario, err := evalv2.Load(scenarioPath)
			if err != nil {
				return fmt.Errorf("failed to load scenario: %w", err)
			}
			scenarioResult, err := agenticEval.RunScenario(scenario)
			if err != nil {
				return fmt.Errorf("failed to run scenario: %w", err)
			}
			agenticResults = evalv2.CalculateAgenticResults([]evalv2.AgenticScenarioResult{*scenarioResult})
		} else {
			agenticResults, err = agenticEval.Run(scenarioDir)
			if err != nil {
				return fmt.Errorf("failed to run agentic evals: %w", err)
			}
		}

		// Calculate duration
		agenticDurationMs := time.Since(startTime).Milliseconds()

		// Report agentic results
		switch outputFormat {
		case "json":
			evalv2.ReportAgenticJSON(os.Stdout, agenticResults)
		default:
			evalv2.ReportAgentic(os.Stdout, agenticResults)
		}

		// Persist agentic results
		persister, err := evalv2.NewPersister()
		if err != nil {
			if verbose {
				fmt.Fprintf(os.Stderr, "Warning: Failed to persist agentic results: %v\n", err)
			}
		} else {
			defer persister.Close()
			if err := persister.PersistAgentic(agenticResults, agenticDurationMs); err != nil {
				if verbose {
					fmt.Fprintf(os.Stderr, "Warning: Failed to persist agentic results: %v\n", err)
				}
			}
		}

		if !agenticResults.Pass {
			return fmt.Errorf("agentic eval failed")
		}
		return nil
	}

	// MEASURE MODE: Test Promptability vs response quality correlation
	if measureMode {
		// Measure evals require a judge
		measureJudge := judgeProvider
		if measureJudge == nil {
			measureJudge = provider // Use same provider as judge if not specified
		}

		measureEval := evalv2.NewMeasureEvaluator(provider, measureJudge)
		measureEval.SetVerbose(verbose)
		measureEval.SetModel(modelName)

		measureDir := scenarioDir + "/measure"
		if scenarioPath != "" {
			// Single scenario
			s, err := evalv2.LoadMeasureScenario(scenarioPath)
			if err != nil {
				return fmt.Errorf("failed to load measure scenario: %w", err)
			}
			result, err := measureEval.RunScenario(s)
			if err != nil {
				return fmt.Errorf("measure eval failed: %w", err)
			}
			// Wrap in aggregate results for reporting
			aggregate := &evalv2.MeasureResults{
				Provider:           provider.Name(),
				Model:              modelName,
				Scenarios:          []evalv2.MeasureScenarioResult{*result},
				OverallCorrelation: result.Correlation,
				Pass:               result.Correlation >= 0.7,
			}
			switch outputFormat {
			case "json":
				return evalv2.ReportMeasureJSON(os.Stdout, aggregate)
			default:
				evalv2.ReportMeasure(os.Stdout, aggregate)
			}
			return nil
		}

		// Run all measure scenarios
		results, err := measureEval.Run(measureDir)
		if err != nil {
			return fmt.Errorf("measure eval failed: %w", err)
		}

		switch outputFormat {
		case "json":
			return evalv2.ReportMeasureJSON(os.Stdout, results)
		default:
			evalv2.ReportMeasure(os.Stdout, results)
		}

		if !results.Pass {
			return fmt.Errorf("measure eval failed: correlation %.2f < 0.7", results.OverallCorrelation)
		}
		return nil
	}

	// STANDARD MODE: Use LLM provider
	// Create evaluator
	evaluator := evalv2.New(provider)
	evaluator.SetVerbose(verbose)
	evaluator.SetModel(modelName)
	if judgeProvider != nil {
		evaluator.SetJudge(judgeProvider, judgeModelName)
	}
	if compareProvider != nil {
		evaluator.SetCompareProvider(compareProvider, compareModelName)
	}

	// Run eval
	var results *evalv2.Results
	if scenarioPath != "" {
		scenario, err := evalv2.Load(scenarioPath)
		if err != nil {
			return fmt.Errorf("failed to load scenario: %w", err)
		}
		scenarioResult, err := evaluator.RunScenario(scenario)
		if err != nil {
			return fmt.Errorf("failed to run scenario: %w", err)
		}
		results = evalv2.CalculateResults([]evalv2.ScenarioResult{*scenarioResult}, provider.Name(), modelName)
	} else {
		results, err = evaluator.Run(scenarioDir)
		if err != nil {
			return fmt.Errorf("failed to run evals: %w", err)
		}
	}

	// Calculate duration
	durationMs := time.Since(startTime).Milliseconds()

	// Report results
	switch outputFormat {
	case "json":
		evalv2.ReportJSON(os.Stdout, results)
	default:
		evalv2.Report(os.Stdout, results)
	}

	// Persist results
	persister, err := evalv2.NewPersister()
	if err != nil {
		if verbose {
			fmt.Fprintf(os.Stderr, "Warning: Failed to persist results: %v\n", err)
		}
	} else {
		defer persister.Close()
		if err := persister.Persist(results, durationMs); err != nil {
			if verbose {
				fmt.Fprintf(os.Stderr, "Warning: Failed to persist results: %v\n", err)
			}
		}
	}

	// Exit with error if ABR < threshold. Skip in dry-run mode: the mock
	// provider returns canned responses, so its ABR has no relationship to
	// real-world Cortex quality. Dry-run is a pipeline-shape smoke test, not
	// a quality gate.
	if !results.Pass && !dryRun {
		return fmt.Errorf("eval failed: ABR below threshold")
	}
	return nil
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
