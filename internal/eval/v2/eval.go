package eval

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/dereksantos/cortex/pkg/llm"
)

// Evaluator runs scenarios and measures ABR.
type Evaluator struct {
	provider llm.Provider
	verbose  bool
}

// New creates a new Evaluator.
func New(provider llm.Provider) *Evaluator {
	return &Evaluator{
		provider: provider,
	}
}

// SetVerbose enables verbose output.
func (e *Evaluator) SetVerbose(v bool) {
	e.verbose = v
}

// Run executes all scenarios in a directory and returns results.
func (e *Evaluator) Run(dir string) (*Results, error) {
	scenarios, err := LoadAll(dir)
	if err != nil {
		return nil, fmt.Errorf("load scenarios: %w", err)
	}

	if len(scenarios) == 0 {
		return nil, fmt.Errorf("no scenarios found in %s", dir)
	}

	var results []ScenarioResult
	for _, s := range scenarios {
		result, err := e.RunScenario(s)
		if err != nil {
			if e.verbose {
				fmt.Printf("  [!] %s: %v\n", s.ID, err)
			}
			continue
		}
		results = append(results, *result)
	}

	return CalculateResults(results, e.provider.Name(), ""), nil
}

// RunScenario executes a single scenario and returns its result.
func (e *Evaluator) RunScenario(s *Scenario) (*ScenarioResult, error) {
	if e.verbose {
		fmt.Printf("Running scenario: %s\n", s.ID)
	}

	// Create isolated Cortex instance
	cortex, err := newCLICortex(e.verbose)
	if err != nil {
		return nil, fmt.Errorf("create cortex: %w", err)
	}
	defer cortex.cleanup()

	// Store context
	for _, ctx := range s.Context {
		if err := cortex.store(ctx.Type, ctx.Content); err != nil {
			return nil, fmt.Errorf("store context: %w", err)
		}
	}

	// Ingest events
	if err := cortex.ingest(); err != nil {
		return nil, fmt.Errorf("ingest: %w", err)
	}

	// Run tests
	var testResults []TestResult
	var totalABR float64
	passCount := 0

	for _, test := range s.Tests {
		result, err := e.runTest(cortex, test)
		if err != nil {
			if e.verbose {
				fmt.Printf("  [!] Test %s failed: %v\n", test.ID, err)
			}
			continue
		}
		testResults = append(testResults, *result)
		totalABR += result.ABR
		if result.Pass {
			passCount++
		}
	}

	if len(testResults) == 0 {
		return nil, fmt.Errorf("no tests passed")
	}

	avgABR := totalABR / float64(len(testResults))
	passRate := float64(passCount) / float64(len(testResults))

	return &ScenarioResult{
		ScenarioID: s.ID,
		Name:       s.Name,
		Tests:      testResults,
		ABR:        avgABR,
		PassRate:   passRate,
		Pass:       avgABR >= ABRThreshold,
	}, nil
}

// runTest executes a single test within a scenario.
func (e *Evaluator) runTest(cortex *cliCortex, test Test) (*TestResult, error) {
	if e.verbose {
		fmt.Printf("  Test: %s\n", test.ID)
	}

	// Get context with Fast mode (mechanical retrieval only)
	fastContext, err := cortex.search(test.Query)
	if err != nil {
		return nil, fmt.Errorf("fast search: %w", err)
	}

	// Get context with Full mode (with agentic reranking)
	// For now, Full mode uses the same search since we don't have mode flags yet
	// TODO: Add --mode=full flag to cortex search
	fullContext := fastContext

	// Generate responses with LLM
	ctx := context.Background()

	fastPrompt := buildPrompt(test.Query, fastContext)
	fastResponse, err := e.provider.Generate(ctx, fastPrompt)
	if err != nil {
		return nil, fmt.Errorf("fast generate: %w", err)
	}

	fullPrompt := buildPrompt(test.Query, fullContext)
	fullResponse, err := e.provider.Generate(ctx, fullPrompt)
	if err != nil {
		return nil, fmt.Errorf("full generate: %w", err)
	}

	// Score responses
	fastScore := Score(fastResponse, test.Expect)
	fullScore := Score(fullResponse, test.Expect)
	abr := CalculateABR(fastScore, fullScore)

	if e.verbose {
		fmt.Printf("    Fast: %.2f, Full: %.2f, ABR: %.2f\n", fastScore, fullScore, abr)
	}

	return &TestResult{
		TestID:    test.ID,
		Query:     test.Query,
		FastScore: fastScore,
		FullScore: fullScore,
		ABR:       abr,
		Pass:      abr >= ABRThreshold,
	}, nil
}

// buildPrompt creates a prompt with context for the LLM.
func buildPrompt(query, context string) string {
	if context == "" {
		return query
	}
	return fmt.Sprintf("Context:\n%s\n\nQuestion: %s", context, query)
}

// cliCortex is a minimal CLI wrapper for eval purposes.
type cliCortex struct {
	workDir   string
	cortexBin string
	verbose   bool
}

func newCLICortex(verbose bool) (*cliCortex, error) {
	workDir, err := os.MkdirTemp("", "cortex-eval-*")
	if err != nil {
		return nil, err
	}

	cortexBin := "./cortex"
	if _, err := os.Stat(cortexBin); os.IsNotExist(err) {
		cortexBin, err = exec.LookPath("cortex")
		if err != nil {
			os.RemoveAll(workDir)
			return nil, fmt.Errorf("cortex binary not found")
		}
	}

	if !filepath.IsAbs(cortexBin) {
		cortexBin, _ = filepath.Abs(cortexBin)
	}

	c := &cliCortex{
		workDir:   workDir,
		cortexBin: cortexBin,
		verbose:   verbose,
	}

	// Initialize
	if _, err := c.run("init"); err != nil {
		c.cleanup()
		return nil, err
	}

	return c, nil
}

func (c *cliCortex) cleanup() {
	if c.workDir != "" {
		os.RemoveAll(c.workDir)
	}
}

func (c *cliCortex) run(args ...string) (string, error) {
	cmd := exec.Command(c.cortexBin, args...)
	cmd.Dir = c.workDir

	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("%s: %s", err, string(out))
	}
	return string(out), nil
}

func (c *cliCortex) store(eventType, content string) error {
	_, err := c.run("capture", "--type="+eventType, "--content="+content)
	return err
}

func (c *cliCortex) ingest() error {
	_, err := c.run("ingest")
	return err
}

func (c *cliCortex) search(query string) (string, error) {
	out, err := c.run("search", query, "--limit=5")
	if err != nil {
		if strings.Contains(err.Error(), "No results") || strings.Contains(err.Error(), "No events") {
			return "", nil
		}
		return "", err
	}
	return out, nil
}

// Timestamp returns a formatted timestamp for results.
func Timestamp() string {
	return time.Now().Format("2006-01-02T15:04:05Z")
}
