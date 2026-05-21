package commands

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"os"
	"strconv"
	"strings"

	evalv2 "github.com/dereksantos/cortex/internal/eval/v2"
	intllm "github.com/dereksantos/cortex/internal/llm"
	"github.com/dereksantos/cortex/internal/measure"
	"github.com/dereksantos/cortex/pkg/llm"
)

func init() {
	Register(&MeasureCommand{})
}

// MeasureCommand measures prompt quality for small context windows.
type MeasureCommand struct{}

// Name returns the command name.
func (c *MeasureCommand) Name() string { return "measure" }

// Description returns the command description.
func (c *MeasureCommand) Description() string {
	return "Measure prompt quality for small context windows"
}

// DescribeFlags surfaces measure's flag set into tools.json.
func (c *MeasureCommand) DescribeFlags(fs *flag.FlagSet) {
	fs.String("provider", "", "Provider name (anthropic | ollama | openrouter)")
	fs.String("model", "", "Model id")
	fs.Int("window", 8192, "Context window in tokens")
	fs.String("output", "human", "Output format: human | json")
	fs.String("file", "", "Read prompt from file")
	fs.Bool("stdin", false, "Read prompt from stdin")
	fs.Bool("fast", false, "Fast (mechanical-only) mode")
	fs.Int("calibrate", 0, "Calibrate the prompt-quality model against a corpus of <N> tokens")
	fs.Bool("self-eval", false, "Run measure self-eval against test/evals/v2/measure/")
	fs.String("scenario", "", "Self-eval scenario path")
	fs.String("dir", "test/evals/v2/measure", "Self-eval scenario directory")
	fs.Bool("verbose", false, "Verbose output")
}

// Execute runs the measure command.
func (c *MeasureCommand) Execute(ctx *Context) error {
	providerName := ""
	modelOverride := ""
	contextWindow := 8192
	outputFormat := "human"
	verbose := false
	fast := false
	fromFile := ""
	fromStdin := false
	calibrateTokens := 0
	selfEval := false
	selfEvalScenario := ""
	selfEvalDir := "test/evals/v2/measure"
	var promptParts []string

	for i := 0; i < len(ctx.Args); i++ {
		arg := ctx.Args[i]
		switch arg {
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
		case "-w", "--window":
			if i+1 < len(ctx.Args) {
				w, err := strconv.Atoi(ctx.Args[i+1])
				if err != nil {
					return fmt.Errorf("invalid window size: %s", ctx.Args[i+1])
				}
				contextWindow = w
				i++
			}
		case "-o", "--output":
			if i+1 < len(ctx.Args) {
				outputFormat = ctx.Args[i+1]
				i++
			}
		case "-f", "--file":
			if i+1 < len(ctx.Args) {
				fromFile = ctx.Args[i+1]
				i++
			}
		case "--stdin":
			fromStdin = true
		case "--fast":
			fast = true
		case "--calibrate":
			if i+1 < len(ctx.Args) {
				tokens, err := strconv.Atoi(ctx.Args[i+1])
				if err != nil {
					return fmt.Errorf("invalid calibrate token count: %s", ctx.Args[i+1])
				}
				calibrateTokens = tokens
				i++
			}
		case "--self-eval":
			selfEval = true
		case "-s", "--scenario":
			if i+1 < len(ctx.Args) {
				selfEvalScenario = ctx.Args[i+1]
				i++
			}
		case "-d", "--dir":
			if i+1 < len(ctx.Args) {
				selfEvalDir = ctx.Args[i+1]
				i++
			}
		case "-v", "--verbose":
			verbose = true
		case "-h", "--help":
			fmt.Println(`Usage: cortex measure [options] "prompt text"

Measure prompt quality for small context windows.
Produces a Promptability score (0-1) with grade (A-F).

Options:
  -f, --file FILE       Read prompt from file
  --stdin               Read prompt from stdin
  -p, --provider NAME   LLM provider for agentic scoring (default: mechanical only)
  -m, --model NAME      Model override
  -w, --window SIZE     Target context window in tokens (default: 8192)
  --fast                Force mechanical-only (skip agentic even if provider set)
  --calibrate TOKENS    Record actual output tokens for tuning (use with prompt)
  --self-eval           Validate the Promptability scorer: run a corpus of
                        prompts through the scorer + LLM, check that score
                        correlates with response quality. Requires -p.
  -s, --scenario PATH   Single self-eval scenario YAML (used with --self-eval)
  -d, --dir PATH        Self-eval scenario directory
                        (default: test/evals/v2/measure)
  -o, --output FORMAT   Output: human, json (default: human)
  -v, --verbose         Verbose output
  -h, --help            Show this help

Config:
  Measurement parameters are loaded from .cortex/measure.json if present.
  Use --calibrate to record prompt→output pairs. Dream mode auto-tunes from these.

  Example .cortex/measure.json:
    {
      "extra_action_verbs": ["deploy", "provision"],
      "weights": {"decomposition": 0.50, "clarity": 0.30, "inverse_scope": 0.20}
    }

Examples:
  cortex measure "Add JWT validation to the auth middleware"
  cortex measure -f prompt.txt --window 4096
  cortex measure -p ollama "Refactor the database layer"
  cortex measure --calibrate 350 "Add error handling to login"
  cortex measure --self-eval -p ollama -m qwen2.5-coder:1.5b
  echo "Fix the bug" | cortex measure --stdin`)
			return nil
		default:
			if !strings.HasPrefix(arg, "-") {
				promptParts = append(promptParts, arg)
			}
		}
	}

	// Self-eval mode: validate that Promptability scores correlate with
	// response quality. Runs scenarios from selfEvalDir / selfEvalScenario
	// through MeasureEvaluator. Requires a provider.
	if selfEval {
		if providerName == "" {
			return fmt.Errorf("--self-eval requires -p/--provider (the scorer needs an LLM to compare against)")
		}
		provider, err := createMeasureProvider(providerName, modelOverride, ctx)
		if err != nil {
			return err
		}
		return runMeasureSelfEval(provider, modelOverride, selfEvalScenario, selfEvalDir, outputFormat, verbose)
	}

	// Get prompt text
	prompt, err := resolvePrompt(promptParts, fromFile, fromStdin)
	if err != nil {
		return err
	}
	if prompt == "" {
		return fmt.Errorf("no prompt provided. Use: cortex measure \"your prompt\" or -f file.txt or --stdin")
	}

	// Load per-project config
	contextDir := resolveContextDir()
	cfg := measure.LoadConfig(contextDir)

	// Handle calibration
	if calibrateTokens > 0 {
		return runCalibration(prompt, calibrateTokens, cfg, contextDir)
	}

	// Create provider if requested
	var provider llm.Provider
	if providerName != "" && !fast {
		provider, err = createMeasureProvider(providerName, modelOverride, ctx)
		if err != nil {
			return err
		}
	}

	// Run measurement with config
	m := measure.NewWithConfig(provider, cfg)
	m.SetContextWindow(contextWindow)
	m.SetVerbose(verbose)

	result, err := m.Measure(context.TODO(), prompt)
	if err != nil {
		return fmt.Errorf("measurement failed: %w", err)
	}

	// Output
	switch outputFormat {
	case "json":
		return measure.ReportJSON(os.Stdout, result)
	default:
		measure.Report(os.Stdout, result)
		return nil
	}
}

func runCalibration(prompt string, actualTokens int, cfg *measure.Config, contextDir string) error {
	// Measure the prompt to get signals
	m := measure.NewWithConfig(nil, cfg)
	mech := m.MeasureMechanical(prompt)

	point := measure.CalibrationPoint{
		PromptTokens:       mech.InputTokens,
		ActualOutputTokens: actualTokens,
		ActionVerbs:        mech.ActionVerbCount,
		FileReferences:     mech.FileReferences,
		Concerns:           mech.ConcernCount,
	}

	cfg.Calibrations = append(cfg.Calibrations, point)

	// Auto-tune if enough samples
	tuned := cfg.Tune(5)

	if err := cfg.Save(contextDir); err != nil {
		return fmt.Errorf("save calibration: %w", err)
	}

	fmt.Printf("Calibration recorded: estimated=%d actual=%d tokens (%d samples total)\n",
		mech.EstimatedOutputTokens, actualTokens, len(cfg.Calibrations))
	if tuned {
		fmt.Println("Auto-tuned token estimation from calibration data.")
	}
	return nil
}

func resolveContextDir() string {
	// Try project-level .cortex
	wd, err := os.Getwd()
	if err != nil {
		return ".cortex"
	}
	return wd + "/.cortex"
}

func resolvePrompt(parts []string, fromFile string, fromStdin bool) (string, error) {
	if fromFile != "" {
		data, err := os.ReadFile(fromFile)
		if err != nil {
			return "", fmt.Errorf("read prompt file: %w", err)
		}
		return strings.TrimSpace(string(data)), nil
	}

	if fromStdin {
		var lines []string
		scanner := bufio.NewScanner(os.Stdin)
		for scanner.Scan() {
			lines = append(lines, scanner.Text())
		}
		if err := scanner.Err(); err != nil {
			return "", fmt.Errorf("read stdin: %w", err)
		}
		return strings.TrimSpace(strings.Join(lines, "\n")), nil
	}

	if len(parts) > 0 {
		return strings.Join(parts, " "), nil
	}

	return "", nil
}

func createMeasureProvider(providerName, modelOverride string, ctx *Context) (llm.Provider, error) {
	cfg, err := loadEvalConfig()
	if err != nil {
		return nil, fmt.Errorf("failed to load config: %w", err)
	}

	if modelOverride != "" {
		if providerName == "anthropic" {
			cfg.AnthropicModel = modelOverride
		} else {
			cfg.OllamaModel = modelOverride
		}
	}

	switch providerName {
	case "ollama":
		// Route by model id — a user with Phase 4 model_routes can have
		// the "ollama" alias hit their configured local endpoint
		// (chatterbox/lemonade/lm-studio) instead of being pinned to
		// Ollama. The alias name is retained for back-compat.
		client := intllm.BuildProvider(cfg, cfg.OllamaModel)
		if client == nil || !client.IsAvailable() {
			return nil, fmt.Errorf("local LLM is not running; start with: ollama serve (or your model_routes endpoint)")
		}
		return client, nil
	case "anthropic", "openrouter", "auto":
		// All hosted-LLM aliases route through the unified surface
		// (OpenRouter primary, Anthropic fallback). "anthropic" is kept
		// for back-compat with existing scripts — it no longer pins to
		// Anthropic-direct; use NewAnthropicClient explicitly if that
		// is what you need.
		client, _, err := llm.NewLLMClient(cfg)
		if err != nil {
			return nil, fmt.Errorf("no hosted LLM available: %w", err)
		}
		return client, nil
	default:
		return nil, fmt.Errorf("unknown provider: %s (use ollama, anthropic, openrouter, or auto)", providerName)
	}
}

// runMeasureSelfEval validates the Promptability scorer by running a corpus
// of measure-scenarios through both the scorer and a real LLM, then checking
// score-vs-quality correlation. Was the --measure mode under `cortex eval`;
// it's a self-test of the measure subsystem, not a coding-harness eval, so
// it belongs next to the thing it validates.
func runMeasureSelfEval(provider llm.Provider, modelName, scenarioPath, scenarioDir, outputFormat string, verbose bool) error {
	// The judge can be the same provider — self-eval doesn't need a
	// separate frontier judge to find correlation drift.
	measureEval := evalv2.NewMeasureEvaluator(provider, provider)
	measureEval.SetVerbose(verbose)
	if modelName != "" {
		measureEval.SetModel(modelName)
	}

	if scenarioPath != "" {
		s, err := evalv2.LoadMeasureScenario(scenarioPath)
		if err != nil {
			return fmt.Errorf("load measure scenario: %w", err)
		}
		result, err := measureEval.RunScenario(s)
		if err != nil {
			return fmt.Errorf("self-eval: %w", err)
		}
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
			return nil
		}
	}

	results, err := measureEval.Run(scenarioDir)
	if err != nil {
		return fmt.Errorf("self-eval: %w", err)
	}
	switch outputFormat {
	case "json":
		return evalv2.ReportMeasureJSON(os.Stdout, results)
	default:
		evalv2.ReportMeasure(os.Stdout, results)
	}
	if !results.Pass {
		return fmt.Errorf("self-eval failed: correlation %.2f < 0.7", results.OverallCorrelation)
	}
	return nil
}
