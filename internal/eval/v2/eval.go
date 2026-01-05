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

// Evaluator runs scenarios and compares Cortex vs baseline.
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

	// Create isolated Cortex instance for this scenario
	cortex, err := newCLICortex(e.verbose)
	if err != nil {
		return nil, fmt.Errorf("create cortex: %w", err)
	}
	defer cortex.cleanup()

	// Store context in Cortex
	for _, ctx := range s.Context {
		if err := cortex.store(ctx.Type, ctx.Content); err != nil {
			return nil, fmt.Errorf("store context: %w", err)
		}
		if e.verbose {
			fmt.Printf("  [stored] %s: %s\n", ctx.Type, truncateVerbose(ctx.Content, 60))
		}
	}

	// Ingest events into database
	if err := cortex.ingest(); err != nil {
		return nil, fmt.Errorf("ingest: %w", err)
	}

	// Run tests
	var testResults []TestResult
	for _, test := range s.Tests {
		result, err := e.runTest(cortex, test)
		if err != nil {
			if e.verbose {
				fmt.Printf("  [!] Test %s failed: %v\n", test.ID, err)
			}
			continue
		}
		testResults = append(testResults, *result)
	}

	if len(testResults) == 0 {
		return nil, fmt.Errorf("no tests completed")
	}

	return CalculateScenarioResult(s.ID, s.Name, testResults), nil
}

// runTest executes a single test: baseline vs cortex.
func (e *Evaluator) runTest(cortex *cliCortex, test Test) (*TestResult, error) {
	if e.verbose {
		fmt.Printf("  Test: %s\n", test.ID)
	}

	ctx := context.Background()

	// 1. BASELINE: Generate response WITHOUT any context
	baselineResponse, err := e.provider.Generate(ctx, test.Query)
	if err != nil {
		return nil, fmt.Errorf("baseline generate: %w", err)
	}
	baselineScore := Score(baselineResponse, test.Expect)

	// 2. CORTEX: Search for relevant context
	cortexContext, err := cortex.search(test.Query)
	if err != nil {
		return nil, fmt.Errorf("search: %w", err)
	}

	if e.verbose {
		fmt.Printf("    Query: %s\n", test.Query)
		if cortexContext == "" {
			fmt.Printf("    [search] No context retrieved\n")
		} else {
			fmt.Printf("    [search] Retrieved %d chars:\n", len(cortexContext))
			// Show first 200 chars of context
			preview := cortexContext
			if len(preview) > 200 {
				preview = preview[:200] + "..."
			}
			for _, line := range strings.Split(preview, "\n") {
				fmt.Printf("      | %s\n", line)
			}
		}
	}

	// 3. CORTEX: Generate response WITH context
	cortexPrompt := buildPrompt(test.Query, cortexContext)
	cortexResponse, err := e.provider.Generate(ctx, cortexPrompt)
	if err != nil {
		return nil, fmt.Errorf("cortex generate: %w", err)
	}
	cortexScore := Score(cortexResponse, test.Expect)

	// 4. Calculate lift and winner
	lift := CalculateLift(cortexScore, baselineScore)
	winner := DetermineWinner(cortexScore, baselineScore)

	if e.verbose {
		fmt.Printf("    [baseline] %.2f - %s\n", baselineScore, truncateVerbose(baselineResponse, 80))
		fmt.Printf("    [cortex]   %.2f - %s\n", cortexScore, truncateVerbose(cortexResponse, 80))
		fmt.Printf("    [expect]   includes=%v excludes=%v\n", test.Expect.Includes, test.Expect.Excludes)
		fmt.Printf("    Lift: %+.0f%%, Winner: %s\n", lift*100, winner)
	}

	return &TestResult{
		TestID:        test.ID,
		Query:         test.Query,
		BaselineScore: baselineScore,
		CortexScore:   cortexScore,
		Lift:          lift,
		Winner:        winner,
		Pass:          cortexScore >= baselineScore, // Cortex doesn't hurt
	}, nil
}

// buildPrompt creates a prompt with context for the LLM.
func buildPrompt(query, cortexContext string) string {
	if cortexContext == "" {
		return query
	}
	return fmt.Sprintf(`You have access to the following context from previous work:

%s

Based on this context, answer the following question:
%s`, cortexContext, query)
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

	// Initialize Cortex in temp directory
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
	out, err := c.run("capture", "--type="+eventType, "--content="+content)
	if c.verbose && out != "" {
		fmt.Printf("    [capture] %s\n", truncateVerbose(out, 60))
	}
	return err
}

func (c *cliCortex) ingest() error {
	out, err := c.run("ingest")
	if c.verbose && out != "" {
		fmt.Printf("  [ingest] %s\n", truncateVerbose(out, 60))
	}
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

// truncateVerbose truncates a string for verbose output, replacing newlines.
func truncateVerbose(s string, max int) string {
	s = strings.ReplaceAll(s, "\n", " ")
	s = strings.ReplaceAll(s, "\t", " ")
	if len(s) > max {
		return s[:max] + "..."
	}
	return s
}
