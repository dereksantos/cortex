package eval

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	intcognition "github.com/dereksantos/cortex/internal/cognition"
	"github.com/dereksantos/cortex/pkg/cognition"
	"github.com/dereksantos/cortex/pkg/llm"
)

// CLICortex uses the cortex CLI for real end-to-end testing.
// This avoids mock/real discrepancies by testing the actual system.
type CLICortex struct {
	// workDir is the isolated directory for this eval run
	workDir string

	// cortexBin is the path to the cortex binary
	cortexBin string

	// verbose enables debug output
	verbose bool

	// eventCount tracks stored events for debugging
	eventCount int

	// llm is an optional LLM provider for nuance extraction.
	// When set, patterns are automatically analyzed for nuances.
	llm llm.Provider

	// nuances holds extracted nuances (from LLM or pre-populated).
	nuances map[string][]cognition.Nuance

	// patternCount tracks patterns for nuance ID generation
	patternCount int
}

// NewCLICortex creates a CLI-based Cortex for E2E testing.
// It creates an isolated temp directory and initializes Cortex there.
func NewCLICortex(verbose bool) (*CLICortex, error) {
	// Create temp directory for isolation
	workDir, err := os.MkdirTemp("", "cortex-e2e-*")
	if err != nil {
		return nil, fmt.Errorf("failed to create temp directory: %w", err)
	}

	// Find cortex binary (assume it's in current directory or PATH)
	cortexBin := "./cortex"
	if _, err := os.Stat(cortexBin); os.IsNotExist(err) {
		// Try to find in PATH
		cortexBin, err = exec.LookPath("cortex")
		if err != nil {
			os.RemoveAll(workDir)
			return nil, fmt.Errorf("cortex binary not found: %w", err)
		}
	}

	// Make path absolute
	if !filepath.IsAbs(cortexBin) {
		abs, err := filepath.Abs(cortexBin)
		if err != nil {
			os.RemoveAll(workDir)
			return nil, fmt.Errorf("failed to get absolute path for cortex binary: %w", err)
		}
		cortexBin = abs
	}

	c := &CLICortex{
		workDir:   workDir,
		cortexBin: cortexBin,
		verbose:   verbose,
	}

	// Initialize Cortex in the temp directory
	if err := c.init(); err != nil {
		c.Cleanup()
		return nil, fmt.Errorf("failed to initialize Cortex: %w", err)
	}

	return c, nil
}

// Cleanup removes the temp directory
func (c *CLICortex) Cleanup() {
	if c.workDir != "" {
		os.RemoveAll(c.workDir)
	}
}

// WorkDir returns the working directory for this instance
func (c *CLICortex) WorkDir() string {
	return c.workDir
}

// init runs cortex init in the work directory
func (c *CLICortex) init() error {
	_, err := c.runCommand("init")
	return err
}

// runCommand executes a cortex command in the work directory
func (c *CLICortex) runCommand(args ...string) (string, error) {
	cmd := exec.Command(c.cortexBin, args...)
	cmd.Dir = c.workDir

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if c.verbose {
		fmt.Printf("[CLI] Running: %s %s (in %s)\n", c.cortexBin, strings.Join(args, " "), c.workDir)
	}

	err := cmd.Run()
	if err != nil {
		if c.verbose {
			fmt.Printf("[CLI] Error: %v\nStderr: %s\n", err, stderr.String())
		}
		return "", fmt.Errorf("command failed: %w\nstderr: %s", err, stderr.String())
	}

	output := stdout.String()
	if c.verbose && output != "" {
		fmt.Printf("[CLI] Output: %s\n", output)
	}

	return output, nil
}

// SetLLM sets the LLM provider for automatic nuance extraction.
// When set, patterns are automatically analyzed as they are stored.
func (c *CLICortex) SetLLM(provider llm.Provider) {
	c.llm = provider
}

// StoreEvent captures an event using the CLI
func (c *CLICortex) StoreEvent(eventType, content string, tags []string) error {
	// Use capture command with --type and --content
	args := []string{
		"capture",
		"--type=" + eventType,
		"--content=" + content,
	}

	_, err := c.runCommand(args...)
	if err != nil {
		return fmt.Errorf("failed to capture event: %w", err)
	}

	c.eventCount++

	// Extract nuances for patterns if LLM is available
	if eventType == "pattern" && c.llm != nil && c.llm.IsAvailable() {
		c.extractNuancesForPattern(content)
	}

	return nil
}

// extractNuancesForPattern extracts nuances from a pattern using the LLM.
// This simulates what Think would do during active work.
func (c *CLICortex) extractNuancesForPattern(content string) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	nuances, err := intcognition.ExtractNuances(ctx, c.llm, content)
	if err != nil {
		if c.verbose {
			fmt.Printf("[CLI] Failed to extract nuances: %v\n", err)
		}
		return
	}

	if len(nuances) == 0 {
		return
	}

	// Convert to cognition.Nuance and store
	c.patternCount++
	patternID := fmt.Sprintf("pattern:%d", c.patternCount)

	if c.nuances == nil {
		c.nuances = make(map[string][]cognition.Nuance)
	}

	cogNuances := make([]cognition.Nuance, len(nuances))
	for i, n := range nuances {
		cogNuances[i] = cognition.Nuance{
			Detail: n.Detail,
			Why:    n.Why,
		}
	}
	c.nuances[patternID] = cogNuances

	if c.verbose {
		fmt.Printf("[CLI] Extracted %d nuances for %s\n", len(nuances), patternID)
	}
}

// Ingest processes the event queue and runs analysis
func (c *CLICortex) Ingest() error {
	// First, ingest events from queue to database
	_, err := c.runCommand("ingest")
	if err != nil {
		return fmt.Errorf("ingest failed: %w", err)
	}
	return nil
}

// Search queries context using the CLI
func (c *CLICortex) Search(query string, limit int) ([]cognition.Result, error) {
	// Use shared ExtractTerms to get meaningful search terms
	terms := intcognition.ExtractTerms(query)
	if len(terms) == 0 {
		return []cognition.Result{}, nil
	}

	// CLI search uses LIKE '%query%' which requires exact substring match.
	// Search for each term individually and combine unique results.
	seenIDs := make(map[string]bool)
	var allResults []cognition.Result

	// Limit to first 5 terms
	if len(terms) > 5 {
		terms = terms[:5]
	}

	for _, term := range terms {
		args := []string{"search", term}
		if limit > 0 {
			args = append(args, fmt.Sprintf("--limit=%d", limit))
		}

		output, err := c.runCommand(args...)
		if err != nil {
			// No results for this term is fine, try next
			if strings.Contains(err.Error(), "No results found") || strings.Contains(err.Error(), "No events found") {
				continue
			}
			return nil, fmt.Errorf("search failed: %w", err)
		}

		results, err := c.parseSearchResults(output)
		if err != nil {
			continue
		}

		// Add unique results
		for _, r := range results {
			if !seenIDs[r.ID] {
				seenIDs[r.ID] = true
				allResults = append(allResults, r)
			}
		}

		// Stop if we have enough results
		if len(allResults) >= limit {
			break
		}
	}

	// Limit total results
	if limit > 0 && len(allResults) > limit {
		allResults = allResults[:limit]
	}

	return allResults, nil
}

// parseSearchResults converts CLI output to cognition.Result slice
func (c *CLICortex) parseSearchResults(output string) ([]cognition.Result, error) {
	// Try JSON format first
	var jsonResults []struct {
		ID        string   `json:"id"`
		Content   string   `json:"content"`
		Category  string   `json:"category"`
		Score     float64  `json:"score"`
		Tags      []string `json:"tags"`
		Timestamp string   `json:"timestamp"`
	}

	if err := json.Unmarshal([]byte(output), &jsonResults); err == nil {
		results := make([]cognition.Result, len(jsonResults))
		for i, jr := range jsonResults {
			ts, _ := time.Parse(time.RFC3339, jr.Timestamp)
			results[i] = cognition.Result{
				ID:        jr.ID,
				Content:   jr.Content,
				Category:  jr.Category,
				Score:     jr.Score,
				Tags:      jr.Tags,
				Timestamp: ts,
			}
		}
		return results, nil
	}

	// Fallback: parse text output
	// Format: "1. [source] ToolName - timestamp\n   content..."
	return c.parseTextSearchResults(output), nil
}

// parseTextSearchResults parses the human-readable search output
func (c *CLICortex) parseTextSearchResults(output string) []cognition.Result {
	var results []cognition.Result
	lines := strings.Split(output, "\n")

	for i := 0; i < len(lines); i++ {
		line := strings.TrimSpace(lines[i])

		// Skip empty lines and header
		if line == "" || strings.HasPrefix(line, "Found") {
			continue
		}

		// Look for numbered results: "1. [source] ..."
		if len(line) > 0 && line[0] >= '1' && line[0] <= '9' && strings.Contains(line, ".") {
			// Extract content from following lines
			content := ""
			for j := i + 1; j < len(lines) && !strings.HasPrefix(strings.TrimSpace(lines[j]), fmt.Sprintf("%d.", len(results)+2)); j++ {
				if strings.TrimSpace(lines[j]) != "" {
					if content != "" {
						content += " "
					}
					content += strings.TrimSpace(lines[j])
				}
				i = j
			}

			if content != "" {
				results = append(results, cognition.Result{
					ID:      fmt.Sprintf("result-%d", len(results)+1),
					Content: content,
					Score:   1.0 - float64(len(results))*0.1, // Decreasing scores
				})
			}
		}
	}

	return results
}

// Retrieve implements a subset of cognition.Cortex for compatibility
func (c *CLICortex) Retrieve(ctx context.Context, q cognition.Query, mode cognition.RetrieveMode) (*cognition.ResolveResult, error) {
	results, err := c.Search(q.Text, q.Limit)
	if err != nil {
		return nil, err
	}

	return &cognition.ResolveResult{
		Results:  results,
		Decision: cognition.Inject,
	}, nil
}

// Reflex implements cognition.Reflexer (delegates to Search)
func (c *CLICortex) Reflex(ctx context.Context, q cognition.Query) ([]cognition.Result, error) {
	return c.Search(q.Text, q.Limit)
}

// Reflect implements cognition.Reflector (pass-through for CLI mode)
func (c *CLICortex) Reflect(ctx context.Context, q cognition.Query, candidates []cognition.Result) ([]cognition.Result, error) {
	return candidates, nil
}

// Resolve implements cognition.Resolver
func (c *CLICortex) Resolve(ctx context.Context, q cognition.Query, candidates []cognition.Result) (*cognition.ResolveResult, error) {
	return &cognition.ResolveResult{
		Results:  candidates,
		Decision: cognition.Inject,
	}, nil
}

// MaybeThink implements cognition.Thinker (no-op for CLI mode)
func (c *CLICortex) MaybeThink(ctx context.Context) (*cognition.ThinkResult, error) {
	return &cognition.ThinkResult{
		Status: cognition.ThinkSkippedIdle,
	}, nil
}

// MaybeDream implements cognition.Dreamer (no-op for CLI mode)
func (c *CLICortex) MaybeDream(ctx context.Context) (*cognition.DreamResult, error) {
	return &cognition.DreamResult{
		Status: cognition.DreamSkippedActive,
	}, nil
}

// ProactiveQueue implements cognition.Dreamer (returns empty for CLI mode)
func (c *CLICortex) ProactiveQueue() []cognition.Result {
	return []cognition.Result{}
}

// Insights implements cognition.Dreamer (returns closed channel for CLI mode)
func (c *CLICortex) Insights() <-chan cognition.Result {
	ch := make(chan cognition.Result)
	close(ch) // Return a closed channel
	return ch
}

// RegisterSource implements cognition.Dreamer (no-op for CLI mode)
func (c *CLICortex) RegisterSource(source cognition.DreamSource) {}

// ForceIdle implements cognition.Dreamer (no-op for CLI mode)
func (c *CLICortex) ForceIdle() {}

// ResetForTesting implements cognition.Dreamer (no-op for CLI mode)
func (c *CLICortex) ResetForTesting() {}

// SessionContext implements cognition.Thinker
func (c *CLICortex) SessionContext() *cognition.SessionContext {
	ctx := &cognition.SessionContext{
		TopicWeights:     make(map[string]float64),
		RecentQueries:    make([]cognition.Query, 0),
		ExtractedNuances: make(map[string][]cognition.Nuance),
	}

	// Include pre-populated nuances if any
	if c.nuances != nil {
		for k, v := range c.nuances {
			ctx.ExtractedNuances[k] = v
		}
	}

	return ctx
}

// SetNuances pre-populates nuances for E2E testing.
// This simulates what Think would have extracted from patterns.
func (c *CLICortex) SetNuances(nuances map[string][]cognition.Nuance) {
	c.nuances = nuances
}

// AddNuance adds a single nuance for testing.
func (c *CLICortex) AddNuance(patternID string, detail, why string) {
	if c.nuances == nil {
		c.nuances = make(map[string][]cognition.Nuance)
	}
	c.nuances[patternID] = append(c.nuances[patternID], cognition.Nuance{
		Detail: detail,
		Why:    why,
	})
}

// MaybeDigest implements cognition.Digester (no-op for CLI mode)
func (c *CLICortex) MaybeDigest(ctx context.Context) (*cognition.DigestResult, error) {
	return &cognition.DigestResult{
		Status: cognition.DigestSkippedNoDream,
	}, nil
}

// NotifyDreamCompleted implements cognition.Digester (no-op for CLI mode)
func (c *CLICortex) NotifyDreamCompleted() {}

// DigestInsights implements cognition.Digester (no-op for CLI mode)
func (c *CLICortex) DigestInsights(ctx context.Context, insights []cognition.Result) ([]cognition.DigestedInsight, error) {
	// Convert each insight to a DigestedInsight with no duplicates
	result := make([]cognition.DigestedInsight, len(insights))
	for i, insight := range insights {
		result[i] = cognition.DigestedInsight{
			Representative: insight,
			Duplicates:     nil,
			Similarity:     1.0,
		}
	}
	return result, nil
}

// GetDigestedInsights implements cognition.Digester (no-op for CLI mode)
func (c *CLICortex) GetDigestedInsights(ctx context.Context, limit int) ([]cognition.DigestedInsight, error) {
	return []cognition.DigestedInsight{}, nil
}

// EventCount returns the number of events stored
func (c *CLICortex) EventCount() int {
	return c.eventCount
}
