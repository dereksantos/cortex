package eval

import (
	"context"
	"crypto/rand"
	"encoding/hex"
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
	provider        llm.Provider
	model           string
	verbose         bool
	judgeProvider   llm.Provider // LLM for semantic scoring (optional)
	judgeModel      string       // Judge model name for tracking
	compareProvider llm.Provider // Frontier model for MPR comparison (optional)
	compareModel    string       // Compare model name for tracking

	// Per-cell persistence. When set, each test emits two CellResult
	// rows (baseline + cortex) through the standard fan-out (journal →
	// SQLite + cell_results.jsonl), satisfying eval-principles #7 +
	// #9. providerName is the canonical provider id (one of
	// evalv2.Provider* constants) and is required when persister is
	// non-nil.
	persister    *Persister
	providerName string
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

// SetModel sets the model name for result tracking.
func (e *Evaluator) SetModel(m string) {
	e.model = m
}

// SetJudge sets the judge provider and model for LLM-as-judge scoring.
func (e *Evaluator) SetJudge(provider llm.Provider, model string) {
	e.judgeProvider = provider
	e.judgeModel = model
}

// SetCompareProvider sets the frontier model used for Model Parity Ratio (MPR).
func (e *Evaluator) SetCompareProvider(provider llm.Provider, model string) {
	e.compareProvider = provider
	e.compareModel = model
}

// SetPersister enables per-cell CellResult persistence. After this is
// called, each test produces two CellResult rows (baseline + cortex)
// through the standard journal/SQLite/JSONL fan-out, so v2 scenarios
// satisfy eval-principles #7 (structured) and #9 (separated baselines)
// alongside the existing legacy eval_scenario_results aggregation.
//
// providerName must be one of the canonical Provider* constants
// (ProviderOpenRouter / ProviderOllama / etc.); it is recorded
// verbatim on every emitted cell.
func (e *Evaluator) SetPersister(p *Persister, providerName string) {
	e.persister = p
	e.providerName = providerName
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

	r := CalculateResults(results, e.provider.Name(), e.model)
	if e.compareProvider != nil {
		r.CompareProvider = e.compareProvider.Name()
		r.CompareModel = e.compareModel
	}
	return r, nil
}

// RunScenario executes a single scenario and returns its result.
func (e *Evaluator) RunScenario(s *Scenario) (*ScenarioResult, error) {
	if e.verbose {
		fmt.Printf("Running scenario: %s\n", s.ID)
	}

	// Route to appropriate handler
	if s.Tree != nil {
		return e.runTreeScenario(s)
	}
	return e.runFlatScenario(s)
}

// runFlatScenario executes a flat (non-tree) scenario.
func (e *Evaluator) runFlatScenario(s *Scenario) (*ScenarioResult, error) {
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

	// Store events as context (for LoCoMo-style scenarios)
	for _, event := range s.Events {
		content := event.Content
		if event.Time != "" {
			content = fmt.Sprintf("[%s] %s", event.Time, content)
		}
		if err := cortex.store("decision", content); err != nil {
			return nil, fmt.Errorf("store event: %w", err)
		}
		if e.verbose {
			fmt.Printf("  [stored] event %s: %s\n", event.ID, truncateVerbose(content, 50))
		}
	}

	// Ingest events into database
	if err := cortex.ingest(); err != nil {
		return nil, fmt.Errorf("ingest: %w", err)
	}

	// Run tests
	var testResults []TestResult
	for _, test := range s.Tests {
		result, err := e.runTest(cortex, test, 0, s.ID) // flat scenarios are depth 0
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

// runTreeScenario executes a tree-structured scenario.
// Each path through the tree accumulates context, and tests inherit all ancestor context.
func (e *Evaluator) runTreeScenario(s *Scenario) (*ScenarioResult, error) {
	var allResults []TestResult

	// Walk all paths through the tree
	err := e.walkTree(s.Tree, []Context{}, &allResults, 0, s.ID)
	if err != nil {
		return nil, err
	}

	if len(allResults) == 0 {
		return nil, fmt.Errorf("no tests completed")
	}

	return CalculateScenarioResult(s.ID, s.Name, allResults), nil
}

// walkTree recursively walks the tree, accumulating context and running tests.
func (e *Evaluator) walkTree(node *TreeNode, inherited []Context, results *[]TestResult, depth int, scenarioID string) error {
	if node == nil {
		return nil
	}

	// Accumulate context from ancestors + this node
	accumulated := append(inherited, node.Context...)

	// Create isolated Cortex instance for this path segment
	cortex, err := newCLICortex(e.verbose)
	if err != nil {
		return fmt.Errorf("create cortex: %w", err)
	}
	defer cortex.cleanup()

	// Store all accumulated context in Cortex
	for _, ctx := range accumulated {
		if err := cortex.store(ctx.Type, ctx.Content); err != nil {
			return fmt.Errorf("store context: %w", err)
		}
		if e.verbose {
			indent := strings.Repeat("  ", depth+1)
			fmt.Printf("%s[stored] %s: %s\n", indent, ctx.Type, truncateVerbose(ctx.Content, 50))
		}
	}

	// Ingest events
	if err := cortex.ingest(); err != nil {
		return fmt.Errorf("ingest: %w", err)
	}

	// Run tests at this level
	for _, test := range node.Tests {
		if e.verbose {
			indent := strings.Repeat("  ", depth+1)
			fmt.Printf("%sTest: %s (depth=%d, context=%d items)\n", indent, test.ID, depth, len(accumulated))
		}
		result, err := e.runTest(cortex, test, depth, scenarioID)
		if err != nil {
			if e.verbose {
				fmt.Printf("  [!] Test %s failed: %v\n", test.ID, err)
			}
			continue
		}
		*results = append(*results, *result)
	}

	// Recurse into children
	for _, child := range node.Children {
		if err := e.walkTree(child, accumulated, results, depth+1, scenarioID); err != nil {
			return err
		}
	}

	return nil
}

// runTest executes a single test: baseline vs cortex.
func (e *Evaluator) runTest(cortex *cliCortex, test Test, depth int, scenarioID string) (*TestResult, error) {
	if e.verbose {
		fmt.Printf("  Test: %s\n", test.ID)
	}

	ctx := context.Background()

	// 1. BASELINE: Generate response WITHOUT any context
	baselineStart := time.Now()
	baselineResponse, baselineStats, err := e.provider.GenerateWithStats(ctx, test.Query)
	baselineLatencyMs := time.Since(baselineStart).Milliseconds()
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
	cortexStart := time.Now()
	cortexResponse, cortexStats, err := e.provider.GenerateWithStats(ctx, cortexPrompt)
	cortexLatencyMs := time.Since(cortexStart).Milliseconds()
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

	result := &TestResult{
		TestID:         test.ID,
		Query:          test.Query,
		Depth:          depth,
		BaselineScore:  baselineScore,
		CortexScore:    cortexScore,
		Lift:           lift,
		Winner:         winner,
		Pass:           cortexScore >= baselineScore, // Cortex doesn't hurt
		BaselineTokens: baselineStats.TotalTokens(),
		CortexTokens:   cortexStats.TotalTokens(),
	}

	// 4b. COMPARE: Generate response with frontier model (no context) for MPR
	if e.compareProvider != nil {
		compareResponse, compareStats, err := e.compareProvider.GenerateWithStats(ctx, test.Query)
		if err != nil {
			if e.verbose {
				fmt.Printf("    [compare] error: %v\n", err)
			}
		} else {
			compareScore := Score(compareResponse, test.Expect)
			result.CompareScore = compareScore
			result.CompareTokens = compareStats.TotalTokens()
			result.HasCompare = true
			result.MPR = CalculateMPR(cortexScore, compareScore)

			if e.verbose {
				fmt.Printf("    [compare] %.2f - %s\n", compareScore, truncateVerbose(compareResponse, 80))
				fmt.Printf("    MPR: %.2f\n", result.MPR)
			}
		}
	}

	// 5. Calculate retrieval quality metrics if ranking is specified
	if len(test.Expect.Ranking) > 0 {
		// Parse Fast mode results (already retrieved above)
		fastResults := parseSearchResults(cortexContext)
		fastNDCG := ScoreRanking(test.Expect.Ranking, fastResults)

		// Run Full mode search (Reflex + Reflect)
		fullContext, err := cortex.searchWithMode(test.Query, "full", 5)
		fullNDCG := 1.0 // Default to ideal if Full mode fails
		if err == nil && fullContext != "" {
			fullResults := parseSearchResults(fullContext)
			fullNDCG = ScoreRanking(test.Expect.Ranking, fullResults)
			if fullNDCG == 0 {
				fullNDCG = 1.0 // Avoid division by zero
			}
		}

		abr := CalculateABR(fastNDCG, fullNDCG)

		result.HasRanking = true
		result.NDCG = fastNDCG // Primary retrieval quality metric
		result.FastNDCG = fastNDCG
		result.FullNDCG = fullNDCG
		result.ABR = abr

		if e.verbose {
			fmt.Printf("    [ranking] FastNDCG=%.2f, FullNDCG=%.2f, ABR=%.2f\n", fastNDCG, fullNDCG, abr)
		}
	}

	// 6. LLM Judge scoring (if enabled)
	if e.judgeProvider != nil {
		result.JudgeUsed = true

		// Score baseline response with judge
		baselineJudge, err := ScoreWithJudge(ctx, baselineResponse, test.Query, test.Expect, "", e.judgeProvider)
		if err != nil {
			if e.verbose {
				fmt.Printf("    [judge] baseline error: %v\n", err)
			}
		} else {
			result.BaselineJudgeCorrectness = baselineJudge.Correctness
			result.BaselineJudgeUnderstanding = baselineJudge.Understanding
			result.BaselineJudgeHallucination = baselineJudge.Hallucination
			result.BaselineJudgeExplanation = baselineJudge.Explanation
		}

		// Score cortex response with judge (with context)
		cortexJudge, err := ScoreWithJudge(ctx, cortexResponse, test.Query, test.Expect, cortexContext, e.judgeProvider)
		if err != nil {
			if e.verbose {
				fmt.Printf("    [judge] cortex error: %v\n", err)
			}
		} else {
			result.CortexJudgeCorrectness = cortexJudge.Correctness
			result.CortexJudgeUnderstanding = cortexJudge.Understanding
			result.CortexJudgeHallucination = cortexJudge.Hallucination
			result.CortexJudgeExplanation = cortexJudge.Explanation
		}

		if e.verbose && baselineJudge != nil && cortexJudge != nil {
			fmt.Printf("    [judge] baseline: correct=%.2f understand=%.2f hallucinate=%.2f\n",
				baselineJudge.Correctness, baselineJudge.Understanding, baselineJudge.Hallucination)
			fmt.Printf("    [judge] cortex:   correct=%.2f understand=%.2f hallucinate=%.2f\n",
				cortexJudge.Correctness, cortexJudge.Understanding, cortexJudge.Hallucination)
		}
	}

	// 7. Per-cell persistence (eval-principles #7 + #9). Emit one row
	// per strategy so analysis can treat baseline and cortex as
	// independent observations rather than a pre-aggregated delta.
	// Persistence failure is logged but does NOT fail the test —
	// missing rows are better surfaced via journal verify than by
	// aborting an otherwise-valid run.
	e.persistTestCells(ctx, scenarioID, test, result, baselineResponse, baselineStats, baselineLatencyMs,
		cortexResponse, cortexStats, cortexLatencyMs, cortexContext)

	return result, nil
}

// persistTestCells emits two CellResult rows (baseline + cortex) for a
// single test. No-op when SetPersister has not been called, so the
// existing "legacy aggregation only" callers (tests, ad-hoc scripts)
// keep working unchanged.
//
// scenarioIDIn is the YAML scenario.ID (e.g. "auth-patterns"); we
// prefix with "v2/" to namespace alongside benchmark scenario_ids
// like "longmemeval/<question>".
func (e *Evaluator) persistTestCells(ctx context.Context, scenarioIDIn string, test Test, result *TestResult,
	baselineResp string, baselineStats llm.GenerationStats, baselineLatencyMs int64,
	cortexResp string, cortexStats llm.GenerationStats, cortexLatencyMs int64,
	cortexContext string,
) {
	if e.persister == nil || e.providerName == "" {
		return
	}
	scenarioID := "v2/" + scenarioIDIn

	// Estimate injected context tokens as len(cortexContext)/4
	// (rough chars-per-token heuristic). Capped at TokensIn so the
	// CellResult.Validate invariant InjectedContextTokens <= TokensIn
	// holds even when the heuristic overshoots a small generation.
	injected := len(cortexContext) / 4
	if injected > cortexStats.TotalTokens() {
		injected = cortexStats.TotalTokens()
	}

	baseline := &CellResult{
		SchemaVersion:        CellResultSchemaVersion,
		RunID:                newScenarioCellRunID("v2", test.ID, StrategyBaseline),
		Timestamp:            time.Now().UTC().Format(time.RFC3339Nano),
		ScenarioID:           scenarioID,
		SessionID:            test.ID,
		Harness:              HarnessCortex,
		Provider:             e.providerName,
		Model:                e.model,
		ContextStrategy:      StrategyBaseline,
		Temperature:          0,
		TokensIn:             baselineStats.InputTokens,
		TokensOut:            baselineStats.OutputTokens,
		LatencyMs:            baselineLatencyMs,
		AgentTurnsTotal:      1,
		TaskSuccess:          result.BaselineScore > 0,
		TaskSuccessCriterion: CriterionScenarioAssertion,
		Notes: fmt.Sprintf("test_id=%s score=%.3f winner=%s lift=%.3f",
			test.ID, result.BaselineScore, result.Winner, result.Lift),
	}
	if err := e.persister.PersistCell(ctx, baseline); err != nil && e.verbose {
		fmt.Fprintf(os.Stderr, "    [persist baseline] %s: %v\n", test.ID, err)
	}

	cortex := &CellResult{
		SchemaVersion:         CellResultSchemaVersion,
		RunID:                 newScenarioCellRunID("v2", test.ID, StrategyCortex),
		Timestamp:             time.Now().UTC().Format(time.RFC3339Nano),
		ScenarioID:            scenarioID,
		SessionID:             test.ID,
		Harness:               HarnessCortex,
		Provider:              e.providerName,
		Model:                 e.model,
		ContextStrategy:       StrategyCortex,
		CortexVersion:         CortexVersion,
		Temperature:           0,
		TokensIn:              cortexStats.InputTokens,
		TokensOut:             cortexStats.OutputTokens,
		InjectedContextTokens: injected,
		LatencyMs:             cortexLatencyMs,
		AgentTurnsTotal:       1,
		TaskSuccess:           result.CortexScore > 0,
		TaskSuccessCriterion:  CriterionScenarioAssertion,
		Notes: fmt.Sprintf("test_id=%s score=%.3f winner=%s lift=%.3f ndcg=%.3f fast_ndcg=%.3f full_ndcg=%.3f abr=%.3f",
			test.ID, result.CortexScore, result.Winner, result.Lift,
			result.NDCG, result.FastNDCG, result.FullNDCG, result.ABR),
	}
	if err := e.persister.PersistCell(ctx, cortex); err != nil && e.verbose {
		fmt.Fprintf(os.Stderr, "    [persist cortex] %s: %v\n", test.ID, err)
	}
}

// newScenarioCellRunID builds a unique-per-cell run id for v2
// scenario tests. Format: "<benchmark>-<test_id>-<strategy>-<8 hex>".
// The hex suffix keeps retries unique without colliding on the same
// test_id within a single run. The benchmark-runner equivalent
// (newCellRunID in coding_runner.go) uses a time-prefixed form
// instead; keeping the two formats distinct makes the producer
// obvious when grepping the journal.
func newScenarioCellRunID(benchmark, testID, strategy string) string {
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	return fmt.Sprintf("%s-%s-%s-%s", benchmark, testID, strategy, hex.EncodeToString(b))
}

// parseSearchResults splits search output into individual result strings.
func parseSearchResults(output string) []string {
	if output == "" {
		return nil
	}

	var results []string
	lines := strings.Split(output, "\n")
	var current strings.Builder

	for _, line := range lines {
		// Results typically start with a number followed by period or bracket
		// e.g., "1. [54% match] content..." or "[decision] content..."
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}

		// Check if this looks like a new result (starts with number or match indicator)
		if len(trimmed) > 0 && (trimmed[0] >= '1' && trimmed[0] <= '9' ||
			strings.HasPrefix(trimmed, "[")) {
			// Save previous result if any
			if current.Len() > 0 {
				results = append(results, current.String())
				current.Reset()
			}
		}

		if current.Len() > 0 {
			current.WriteString(" ")
		}
		current.WriteString(trimmed)
	}

	// Don't forget the last result
	if current.Len() > 0 {
		results = append(results, current.String())
	}

	return results
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
	return c.searchWithMode(query, "fast", 5)
}

func (c *cliCortex) searchWithMode(query, mode string, limit int) (string, error) {
	out, err := c.run("search", fmt.Sprintf("--mode=%s", mode), fmt.Sprintf("--limit=%d", limit), query)
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
