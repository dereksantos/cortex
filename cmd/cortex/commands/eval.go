// Package commands provides CLI command implementations.
package commands

import (
	"fmt"
	"os"
	"time"

	evalv2 "github.com/dereksantos/cortex/internal/eval/v2"
	"github.com/dereksantos/cortex/pkg/config"
	"github.com/dereksantos/cortex/pkg/llm"
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
	claudeBinary := ""
	showSummary := false

	for i := 0; i < len(ctx.Args); i++ {
		arg := ctx.Args[i]
		switch {
		case arg == "-v" || arg == "--verbose":
			verbose = true
		case arg == "--dry-run":
			dryRun = true
		case arg == "--agentic":
			agenticMode = true
		case arg == "--claude-binary":
			if i+1 < len(ctx.Args) {
				claudeBinary = ctx.Args[i+1]
				i++
			}
		case arg == "-s" || arg == "--scenario":
			if i+1 < len(ctx.Args) {
				scenarioPath = ctx.Args[i+1]
				i++
			}
		case arg == "-d" || arg == "--dir":
			if i+1 < len(ctx.Args) {
				scenarioDir = ctx.Args[i+1]
				i++
			}
		case arg == "-p" || arg == "--provider":
			if i+1 < len(ctx.Args) {
				providerName = ctx.Args[i+1]
				i++
			}
		case arg == "-m" || arg == "--model":
			if i+1 < len(ctx.Args) {
				modelOverride = ctx.Args[i+1]
				i++
			}
		case arg == "-o" || arg == "--output":
			if i+1 < len(ctx.Args) {
				outputFormat = ctx.Args[i+1]
				i++
			}
		case arg == "--judge":
			useJudge = true
		case arg == "--judge-model":
			if i+1 < len(ctx.Args) {
				judgeModel = ctx.Args[i+1]
				useJudge = true // implicitly enable judge
				i++
			}
		case arg == "--summary":
			showSummary = true
		case arg == "-h" || arg == "--help":
			fmt.Println(`Usage: cortex eval [options]

Unified eval system comparing baseline vs Cortex-augmented responses.

Options:
  -s, --scenario FILE    Run single scenario
  -d, --dir DIR          Scenario directory (default: test/evals/v2)
  -p, --provider NAME    LLM provider: ollama, anthropic (default: ollama)
  -m, --model NAME       Model override
  -o, --output FORMAT    Output: human, json (default: human)
  -v, --verbose          Verbose output
  --dry-run              Use mock provider
  --judge                Enable LLM-as-judge scoring (semantic evaluation)
  --judge-model MODEL    Model for judge (default: same as eval model)
  --agentic              Use Claude CLI for agentic evals (measures tool usage)
  --claude-binary PATH   Path to claude binary (default: auto-detect)
  --summary              Show lift trend over recent runs
  -h, --help             Show this help

Examples:
  cortex eval                              # Run all scenarios
  cortex eval -s auth.yaml                 # Run single scenario
  cortex eval --judge                      # Use LLM judge for scoring
  cortex eval --judge --judge-model gemma2:2b  # Use specific judge model
  cortex eval --agentic                    # Run with Claude CLI (tool tracking)
  cortex eval --summary                    # Show lift trend
  cortex eval --summary --agentic          # Show tool call reduction trend`)
			return nil
		}
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
				return fmt.Errorf("Ollama is not running. Start with: ollama serve")
			}
			provider = ollamaClient
		case "anthropic":
			anthropicClient := llm.NewAnthropicClient(cfg)
			if !anthropicClient.IsAvailable() {
				return fmt.Errorf("ANTHROPIC_API_KEY not set")
			}
			provider = anthropicClient
		default:
			return fmt.Errorf("unknown provider: %s", providerName)
		}
	}

	// Determine actual model name
	var modelName string
	if modelOverride != "" {
		modelName = modelOverride
	} else if providerName == "anthropic" {
		modelName = cfg.AnthropicModel
	} else {
		modelName = cfg.OllamaModel
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
			if providerName == "anthropic" {
				judgeCfg.AnthropicModel = judgeModelName
				judgeProvider = llm.NewAnthropicClient(&judgeCfg)
			} else {
				judgeCfg.OllamaModel = judgeModelName
				judgeProvider = llm.NewOllamaClient(&judgeCfg)
			}
		}

		if verbose {
			fmt.Printf("Using LLM judge: %s\n", judgeModelName)
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

	// STANDARD MODE: Use LLM provider
	// Create evaluator
	evaluator := evalv2.New(provider)
	evaluator.SetVerbose(verbose)
	evaluator.SetModel(modelName)
	if judgeProvider != nil {
		evaluator.SetJudge(judgeProvider, judgeModelName)
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

	// Exit with error if ABR < threshold
	if !results.Pass {
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

	configPath := fmt.Sprintf("%s/.context/config.json", projectRoot)
	return config.Load(configPath)
}
