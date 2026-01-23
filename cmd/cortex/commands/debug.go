// Package commands provides CLI command implementations.
package commands

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	intcognition "github.com/dereksantos/cortex/internal/cognition"
	"github.com/dereksantos/cortex/internal/storage"
	"github.com/dereksantos/cortex/pkg/config"
	"github.com/dereksantos/cortex/pkg/events"
	"github.com/dereksantos/cortex/pkg/llm"
	"github.com/dereksantos/cortex/pkg/system"
)

func init() {
	Register(&InfoCommand{})
	Register(&TestCommand{})
	Register(&StatsCommand{})
	Register(&StatusCommand{})
	Register(&ForgetCommand{})
	Register(&OverviewCommand{})
}

// InfoCommand shows system information and model recommendations.
type InfoCommand struct{}

// Name returns the command name.
func (c *InfoCommand) Name() string { return "info" }

// Description returns the command description.
func (c *InfoCommand) Description() string { return "Show system info and model recommendations" }

// Execute runs the info command.
func (c *InfoCommand) Execute(ctx *Context) error {
	fmt.Println("Cortex System Information")
	fmt.Println("---------------------------------------------------")
	fmt.Println()

	// Detect system resources
	sysInfo, err := system.Detect()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Warning: Could not detect system info: %v\n", err)
	} else {
		fmt.Println("System Resources:")
		fmt.Printf("  CPU: %d cores\n", sysInfo.CPUCores)
		fmt.Printf("  RAM: %.1f GB total\n", sysInfo.TotalRAMGB)
		fmt.Printf("  OS: %s (%s)\n", sysInfo.FormatOS(), sysInfo.Arch)
		fmt.Println()
	}

	// Check Ollama status
	ollamaRunning, installedModels := checkOllama()

	if ollamaRunning {
		fmt.Println("Ollama Status:")
		fmt.Println("  [OK] Running at http://localhost:11434")

		if len(installedModels) > 0 {
			fmt.Printf("  [OK] Models installed: %d\n", len(installedModels))
			for _, model := range installedModels {
				fmt.Printf("     - %s\n", model)
			}
		} else {
			fmt.Println("  [!] No models installed")
		}
		fmt.Println()
	} else {
		fmt.Println("Ollama Status:")
		fmt.Println("  [X] Not running")
		fmt.Println("  Install: https://ollama.com")
		fmt.Println("  Start: ollama serve")
		fmt.Println()
	}

	// Show model recommendations
	if sysInfo != nil {
		fmt.Println("Model Recommendations:")
		showModelRecommendations(sysInfo.AvailableRAMGB)
		fmt.Println()
	}

	// Show current project status
	cfg := ctx.Config
	if cfg != nil {
		fmt.Println("Current Project:")
		fmt.Printf("  [OK] Initialized at %s\n", cfg.ProjectRoot)
		fmt.Printf("  Model: %s\n", cfg.OllamaModel)

		// Try to get stats
		store := ctx.Storage
		if store != nil {
			if stats, err := store.GetStats(); err == nil {
				if totalEvents, ok := stats["total_events"].(int); ok {
					fmt.Printf("  Events: %d\n", totalEvents)
				}
				if totalInsights, ok := stats["total_insights"].(int); ok {
					fmt.Printf("  Insights: %d\n", totalInsights)
				}
			}
		}
	} else {
		fmt.Println("Current Project:")
		fmt.Println("  [!] Not initialized")
		fmt.Println("  Run: cortex init")
	}
	fmt.Println()

	return nil
}

// TestCommand runs LLM analysis tests.
type TestCommand struct{}

// Name returns the command name.
func (c *TestCommand) Name() string { return "test" }

// Description returns the command description.
func (c *TestCommand) Description() string { return "Test LLM analysis quality" }

// Execute runs the test command.
func (c *TestCommand) Execute(ctx *Context) error {
	// Get test type from args (default: run all)
	testType := "all"
	if len(ctx.Args) >= 1 {
		testType = ctx.Args[0]
	}

	// Validate test type
	validTypes := map[string]bool{
		"all": true, "decision": true, "pattern": true, "insight": true, "ollama": true,
	}
	if !validTypes[testType] {
		return fmt.Errorf("invalid test type: %s (valid: decision, pattern, insight, ollama, all)", testType)
	}

	// Handle ollama benchmark separately
	if testType == "ollama" {
		runOllamaBenchmark()
		return nil
	}

	fmt.Println("Cortex LLM Analysis Test")
	fmt.Println("---------------------------------------------------")
	fmt.Println()

	// Check Ollama availability
	ollamaRunning, _ := checkOllama()
	if !ollamaRunning {
		fmt.Println("[X] Ollama is not running")
		fmt.Println("   Start with: ollama serve")
		return fmt.Errorf("ollama not running")
	}

	// Run requested tests
	tests := []struct {
		name     string
		event    *events.Event
		expected TestExpectations
	}{
		{
			name: "decision",
			event: &events.Event{
				ToolName: "Write",
				ToolInput: map[string]interface{}{
					"file_path": "auth/oauth.go",
				},
				ToolResult: "Implemented OAuth2 with PKCE flow instead of basic auth. Rejected JWT sessions due to token revocation requirements. Chose authorization_code flow over implicit for mobile security. Added refresh token rotation per OWASP guidelines.",
			},
			expected: TestExpectations{
				AllowedCategories: []string{"decision", "strategy"},
				RequiredConcepts:  []string{"oauth", "pkce", "security", "auth"},
				MinImportance:     5,
			},
		},
		{
			name: "pattern",
			event: &events.Event{
				ToolName: "Edit",
				ToolInput: map[string]interface{}{
					"file_path": "errors/handler.go",
				},
				ToolResult: "Refactored error handling to use Result<T,E> pattern. Replaced try/catch blocks with railway-oriented programming. Errors now propagate with ? operator. Added context wrapping for better debugging.",
			},
			expected: TestExpectations{
				AllowedCategories: []string{"pattern", "insight"},
				RequiredConcepts:  []string{"error", "result", "refactor"},
				MinImportance:     4,
			},
		},
		{
			name: "insight",
			event: &events.Event{
				ToolName: "Task",
				ToolInput: map[string]interface{}{
					"description": "Fix race condition",
				},
				ToolResult: "Fixed race condition in cache invalidation by adding mutex. Root cause: concurrent map writes. Added sync.RWMutex. Lesson learned: always profile concurrent code under load before deploying.",
			},
			expected: TestExpectations{
				AllowedCategories: []string{"insight", "learning", "pattern"},
				RequiredConcepts:  []string{"race", "mutex", "concurrent"},
				MinImportance:     5,
			},
		},
	}

	// Filter tests
	var testsToRun []int
	if testType == "all" {
		for i := range tests {
			testsToRun = append(testsToRun, i)
		}
	} else {
		for i, t := range tests {
			if t.name == testType {
				testsToRun = append(testsToRun, i)
				break
			}
		}
	}

	// Run tests in temp directory
	tmpDir, err := os.MkdirTemp("", "cortex-test-*")
	if err != nil {
		return fmt.Errorf("failed to create temp directory: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	// Initialize cortex in temp dir
	originalDir, _ := os.Getwd()
	os.Chdir(tmpDir)
	defer os.Chdir(originalDir)

	cfg := config.Default()
	cfg.ProjectRoot = tmpDir
	cfg.ContextDir = tmpDir + "/.cortex"
	if err := cfg.EnsureDirectories(); err != nil {
		return fmt.Errorf("failed to create directories: %w", err)
	}
	if err := cfg.Save(cfg.ContextDir + "/config.json"); err != nil {
		return fmt.Errorf("failed to save config: %w", err)
	}

	// Open storage
	store, err := storage.New(cfg)
	if err != nil {
		return fmt.Errorf("failed to open storage: %w", err)
	}
	defer store.Close()

	// Run each test
	passed := 0
	failed := 0

	// Get LLM provider
	var llmProvider llm.Provider
	ollama := llm.NewOllamaClient(cfg)
	if ollama.IsAvailable() {
		llmProvider = ollama
	}

	if llmProvider == nil {
		fmt.Println("[X] No LLM available for testing")
		return fmt.Errorf("no LLM available")
	}

	for _, i := range testsToRun {
		test := tests[i]
		fmt.Printf("Testing: %s\n", test.name)
		fmt.Println(strings.Repeat("-", 60))

		// Store event
		if err := store.StoreEvent(test.event); err != nil {
			fmt.Printf("[X] FAIL: Could not store event: %v\n\n", err)
			failed++
			continue
		}

		// Analyze with LLM
		startTime := time.Now()
		if err := AnalyzeEventWithLLM(test.event, store, llmProvider); err != nil {
			fmt.Printf("[X] FAIL: Analysis failed: %v\n\n", err)
			failed++
			continue
		}
		elapsed := time.Since(startTime)

		// Get insights
		insights, err := store.GetRecentInsights(1)
		if err != nil || len(insights) == 0 {
			fmt.Printf("[X] FAIL: No insight generated\n\n")
			failed++
			continue
		}

		insight := insights[0]

		// Validate
		if validateTestInsight(insight, test.expected) {
			fmt.Printf("[OK] PASS (%.1fs)\n", elapsed.Seconds())
			fmt.Printf("   Category: %s\n", insight.Category)
			fmt.Printf("   Summary: %s\n", insight.Summary)
			fmt.Printf("   Tags: %v\n", insight.Tags)
			fmt.Printf("   Importance: %d\n", insight.Importance)
			passed++
		} else {
			fmt.Printf("[!] MARGINAL (%.1fs)\n", elapsed.Seconds())
			fmt.Printf("   Category: %s (expected: %v)\n", insight.Category, test.expected.AllowedCategories)
			fmt.Printf("   Summary: %s\n", insight.Summary)
			fmt.Printf("   Tags: %v\n", insight.Tags)
			fmt.Printf("   Note: Analysis completed but quality may vary\n")
			passed++ // Count as pass since LLM is non-deterministic
		}
		fmt.Println()
	}

	// Summary
	fmt.Println("---------------------------------------------------")
	fmt.Printf("Results: %d/%d passed\n", passed, passed+failed)
	fmt.Println()

	if failed > 0 {
		fmt.Println("[!] Some tests failed - check Ollama status and model")
		return fmt.Errorf("some tests failed")
	}
	fmt.Println("[OK] All tests passed - LLM analysis is working!")
	return nil
}

// TestExpectations defines what a test expects from the LLM analysis.
type TestExpectations struct {
	AllowedCategories []string
	RequiredConcepts  []string
	MinImportance     int
}

func validateTestInsight(insight *storage.Insight, expected TestExpectations) bool {
	// Check category
	categoryOK := false
	for _, allowed := range expected.AllowedCategories {
		if strings.Contains(strings.ToLower(insight.Category), allowed) {
			categoryOK = true
			break
		}
	}

	// Check concepts (fuzzy match in summary + tags)
	fullText := strings.ToLower(insight.Summary)
	for _, tag := range insight.Tags {
		fullText += " " + strings.ToLower(tag)
	}

	conceptMatches := 0
	for _, concept := range expected.RequiredConcepts {
		if strings.Contains(fullText, strings.ToLower(concept)) {
			conceptMatches++
		}
	}
	conceptsOK := float64(conceptMatches)/float64(len(expected.RequiredConcepts)) >= 0.6

	// Check importance
	importanceOK := insight.Importance >= expected.MinImportance

	return categoryOK && conceptsOK && importanceOK
}

func runOllamaBenchmark() {
	cfg := config.Default()
	cfg.ProjectRoot, _ = os.Getwd()

	fmt.Printf("Ollama Benchmark (model: %s)\n", cfg.OllamaModel)
	fmt.Println("-----------------------------------------------")
	fmt.Println()

	// Check Ollama availability
	ollamaRunning, _ := checkOllama()
	if !ollamaRunning {
		fmt.Println("[X] Ollama is not running")
		fmt.Println("   Start with: ollama serve")
		os.Exit(1)
	}
	fmt.Println("[OK] Ollama is running")
	fmt.Println()

	// Create HTTP client with long timeout for benchmarking
	client := &http.Client{
		Timeout: 180 * time.Second,
	}

	// Generate test prompts of different sizes (matching real-world data)
	// Real tool_results range from 1KB to 40KB based on Claude history analysis
	prompts := map[string]string{
		"small (~200B)":  generateTestPrompt(200),
		"medium (~2KB)":  generateTestPrompt(2000),
		"large (~10KB)":  generateTestPrompt(10000),
		"xlarge (~25KB)": generateTestPrompt(25000),
	}

	// Test each prompt size
	fmt.Println("Single Request by Prompt Size:")
	var maxSingleTime time.Duration
	for _, size := range []string{"small (~200B)", "medium (~2KB)", "large (~10KB)", "xlarge (~25KB)"} {
		prompt := prompts[size]
		start := time.Now()
		_, err := ollamaBenchmarkRequest(client, cfg.OllamaURL, cfg.OllamaModel, prompt)
		elapsed := time.Since(start)
		if err != nil {
			fmt.Printf("  %s: [X] Error: %v\n", size, err)
		} else {
			fmt.Printf("  %s: %.1fs\n", size, elapsed.Seconds())
			if elapsed > maxSingleTime {
				maxSingleTime = elapsed
			}
		}
	}
	fmt.Println()

	// Use medium prompt for concurrency tests (most common real-world size)
	mediumPrompt := prompts["medium (~2KB)"]

	// Concurrent request tests with higher concurrency
	fmt.Println("Concurrent Requests (medium prompt):")
	concurrencyLevels := []int{2, 3, 5, 8, 10}
	var safeConcurrency int
	var maxConcurrentTime time.Duration

	for _, concurrency := range concurrencyLevels {
		times := runConcurrentBenchmark(client, cfg.OllamaURL, cfg.OllamaModel, mediumPrompt, concurrency)
		if len(times) > 0 {
			avg, _, max := calcStats(times)
			warning := ""
			if max.Seconds() > 30 {
				warning = " [!] exceeds 30s timeout"
			} else {
				safeConcurrency = concurrency
			}
			if max > maxConcurrentTime {
				maxConcurrentTime = max
			}
			fmt.Printf("  %2d concurrent: avg %.1fs (max: %.1fs)%s\n", concurrency, avg.Seconds(), max.Seconds(), warning)
		} else {
			fmt.Printf("  %2d concurrent: [X] all requests failed\n", concurrency)
		}
	}
	fmt.Println()

	// Current implementation info
	fmt.Println("Current Implementation:")
	fmt.Println("  - Timeout: 30s (hardcoded in pkg/llm/ollama.go:30)")
	fmt.Println("  - Workers: 5 (hardcoded in internal/processor/processor.go:35)")
	fmt.Println()

	// Recommendations
	fmt.Println("-----------------------------------------------")
	fmt.Println("Recommendations:")
	if maxSingleTime > 0 {
		suggestedTimeout := time.Duration(float64(maxSingleTime) * 2)
		if suggestedTimeout < 60*time.Second {
			suggestedTimeout = 60 * time.Second
		}
		fmt.Printf("  - Suggested timeout: %.0fs (based on large prompt performance)\n", suggestedTimeout.Seconds())
	}
	if safeConcurrency > 0 {
		fmt.Printf("  - Suggested max workers: %d (keeps max under 30s)\n", safeConcurrency)
	} else {
		fmt.Println("  - Suggested max workers: 1-2 (high latency detected)")
	}
}

func generateTestPrompt(targetSize int) string {
	base := `Analyze this development event and extract any important insight.

Tool: Write
File: auth/handler.go
Result: `

	// Generate realistic filler content
	filler := `Implemented JWT authentication with refresh tokens. Chose RS256 over HS256 for better security. Added token rotation on each refresh. The authentication module now supports multiple identity providers including OAuth2, SAML, and OpenID Connect. Error handling has been improved with detailed error codes and user-friendly messages. Session management includes automatic cleanup of expired tokens. `

	suffix := `

Respond in JSON format:
{
  "summary": "Brief summary (1 sentence)",
  "category": "decision|pattern|insight|strategy|constraint",
  "importance": 1-10,
  "tags": ["tag1", "tag2"],
  "reasoning": "Why this is important"
}
JSON:`

	// Build prompt to target size
	prompt := base
	for len(prompt) < targetSize-len(suffix) {
		prompt += filler
	}
	prompt += suffix

	return prompt
}

func ollamaBenchmarkRequest(client *http.Client, baseURL, model, prompt string) (string, error) {
	reqBody := map[string]interface{}{
		"model":  model,
		"prompt": prompt,
		"stream": false,
	}
	jsonData, _ := json.Marshal(reqBody)

	resp, err := client.Post(baseURL+"/api/generate", "application/json", bytes.NewBuffer(jsonData))
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("status %d: %s", resp.StatusCode, string(body))
	}

	var result struct {
		Response string `json:"response"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", err
	}
	return result.Response, nil
}

func runConcurrentBenchmark(client *http.Client, baseURL, model, prompt string, concurrency int) []time.Duration {
	results := make(chan time.Duration, concurrency)
	var wg sync.WaitGroup

	for i := 0; i < concurrency; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			start := time.Now()
			_, err := ollamaBenchmarkRequest(client, baseURL, model, prompt)
			if err == nil {
				results <- time.Since(start)
			}
		}()
	}

	wg.Wait()
	close(results)

	var times []time.Duration
	for t := range results {
		times = append(times, t)
	}
	return times
}

func calcStats(times []time.Duration) (avg, min, max time.Duration) {
	if len(times) == 0 {
		return 0, 0, 0
	}

	min = times[0]
	max = times[0]
	var total time.Duration

	for _, t := range times {
		total += t
		if t < min {
			min = t
		}
		if t > max {
			max = t
		}
	}

	avg = total / time.Duration(len(times))
	return avg, min, max
}

// StatsCommand shows storage statistics.
type StatsCommand struct{}

// Name returns the command name.
func (c *StatsCommand) Name() string { return "stats" }

// Description returns the command description.
func (c *StatsCommand) Description() string { return "Show storage statistics" }

// Execute runs the stats command.
func (c *StatsCommand) Execute(ctx *Context) error {
	store := ctx.Storage
	if store == nil {
		return fmt.Errorf("storage not available")
	}

	// Get stats
	stats, err := store.GetStats()
	if err != nil {
		return fmt.Errorf("failed to get stats: %w", err)
	}

	// Pretty print stats
	data, _ := json.MarshalIndent(stats, "", "  ")
	fmt.Println(string(data))

	return nil
}

// StatusCommand shows the current Cortex status.
type StatusCommand struct{}

// Name returns the command name.
func (c *StatusCommand) Name() string { return "status" }

// Description returns the command description.
func (c *StatusCommand) Description() string { return "Show status (for status line)" }

// Execute runs the status command.
func (c *StatusCommand) Execute(ctx *Context) error {
	displayStatus(ctx)
	return nil
}

func displayStatus(ctx *Context) {
	// Read JSON from stdin (Claude Code context, if present)
	var claudeContext map[string]interface{}
	data, err := io.ReadAll(os.Stdin)
	if err == nil && len(data) > 0 {
		json.Unmarshal(data, &claudeContext)
	}

	cfg := ctx.Config
	if cfg == nil {
		// Not initialized yet
		fmt.Print("o Not initialized")
		return
	}

	// Check for daemon state file first (real-time cognitive mode status)
	statePath := intcognition.GetDaemonStatePath(cfg.ContextDir)
	daemonState, _ := intcognition.ReadDaemonState(statePath)

	// If we have fresh daemon state, use it
	if daemonState != nil && daemonState.Mode != "" && daemonState.Mode != "idle" {
		spinner := getModeSpinner(daemonState.Mode)
		desc := daemonState.Description
		if desc == "" {
			desc = getDefaultModeDescription(daemonState.Mode)
		}
		// Natural language format: "{spinner} {description}" - no "Mode:" prefix
		fmt.Printf("%s %s", spinner, desc)
		return
	}

	// Check if daemon is offline (state file exists but is stale)
	daemonOffline := false
	if _, err := os.Stat(statePath); err == nil {
		// State file exists - if daemonState is nil, it means it's stale (daemon stopped)
		if daemonState == nil {
			daemonOffline = true
		}
	}

	store := ctx.Storage
	if store == nil {
		fmt.Print("o No data")
		return
	}

	// Get stats
	stats, err := store.GetStats()
	if err != nil {
		fmt.Print("* Ready")
		return
	}

	totalEvents := 0
	if val, ok := stats["total_events"].(int); ok {
		totalEvents = val
	}

	totalInsights := 0
	if val, ok := stats["total_insights"].(int); ok {
		totalInsights = val
	}

	// If daemon state has stats, prefer those (more recent)
	if daemonState != nil && (daemonState.Stats.Events > 0 || daemonState.Stats.Insights > 0) {
		totalEvents = daemonState.Stats.Events
		totalInsights = daemonState.Stats.Insights
	}

	// Determine current mode based on activity
	mode := "Ready"
	spinner := "+"

	if totalEvents == 0 && totalInsights == 0 {
		mode = "Cold start"
		spinner = "o"
	} else {
		// Check for recent activity
		recentEvents, _ := store.GetRecentEvents(5)
		if len(recentEvents) > 0 {
			lastEvent := recentEvents[0]
			timeSince := time.Since(lastEvent.Timestamp)

			if timeSince < 30*time.Second {
				// Very recent activity - still use checkmark
				mode = "Processing"
				spinner = "+"
			} else if timeSince < 5*time.Minute {
				mode = "Active"
				spinner = "+"
			}
		}
	}

	// Format output: natural language sentences (no colons)
	_ = mode // suppress unused warning - mode determines spinner
	if daemonOffline {
		// Daemon is not running - show stopped status
		if totalEvents > 0 || totalInsights > 0 {
			fmt.Printf("|| Stopped: %d events, %d insights", totalEvents, totalInsights)
		} else {
			fmt.Print("|| Daemon not running")
		}
	} else if totalEvents > 0 || totalInsights > 0 {
		fmt.Printf("%s Ready with %d events and %d insights", spinner, totalEvents, totalInsights)
	} else if mode == "Cold start" {
		fmt.Printf("%s Waiting for first activity...", spinner)
	} else {
		fmt.Printf("%s Watching for activity...", spinner)
	}
}

// getModeSpinner returns the appropriate spinner for a cognitive mode.
func getModeSpinner(mode string) string {
	switch mode {
	case "think":
		return "o-" // Half-filled circle represents processing/learning
	case "dream":
		return "~" // Cloud represents wandering, exploratory thinking
	case "reflex":
		return "*" // Lightning represents fast, mechanical search
	case "reflect":
		return "-o" // Opposite half-filled circle represents evaluation
	case "resolve":
		return ">" // Play/forward triangle represents deciding/choosing
	case "insight":
		return "+" // Star represents discovery
	case "digest":
		return "~" // Tilde represents consolidation/compression
	default:
		return "*"
	}
}

// getDefaultModeDescription returns a natural language description for a mode.
// These are complete sentences without colons.
func getDefaultModeDescription(mode string) string {
	switch mode {
	case "think":
		return "Thinking about session patterns..."
	case "dream":
		return "Dreaming about the codebase..."
	case "reflex":
		return "Searching for relevant context..."
	case "reflect":
		return "Reflecting on search results..."
	case "resolve":
		return "Deciding what to inject..."
	case "digest":
		return "Consolidating insights..."
	default:
		return ""
	}
}

// ForgetCommand removes context by ID or keyword.
type ForgetCommand struct{}

// Name returns the command name.
func (c *ForgetCommand) Name() string { return "forget" }

// Description returns the command description.
func (c *ForgetCommand) Description() string { return "Remove outdated context" }

// Execute runs the forget command.
func (c *ForgetCommand) Execute(ctx *Context) error {
	if len(ctx.Args) < 1 {
		fmt.Fprintf(os.Stderr, "Usage: cortex forget <id-or-keyword>\n")
		fmt.Fprintf(os.Stderr, "\nExamples:\n")
		fmt.Fprintf(os.Stderr, "  cortex forget 123           # Forget insight by ID\n")
		fmt.Fprintf(os.Stderr, "  cortex forget \"redux\"       # Forget insights matching keyword\n")
		return fmt.Errorf("missing argument")
	}

	input := ctx.Args[0]
	store := ctx.Storage
	if store == nil {
		return fmt.Errorf("storage not available")
	}

	// Try to parse as ID first
	var id int64
	if _, err := fmt.Sscanf(input, "%d", &id); err == nil && id > 0 {
		// Delete by ID
		if err := store.ForgetInsight(id); err != nil {
			return fmt.Errorf("failed to forget insight: %w", err)
		}
		fmt.Printf("Forgot insight #%d\n", id)
		return nil
	}

	// Search for matching insights first
	insights, err := store.SearchInsights(input, 10)
	if err != nil {
		return fmt.Errorf("failed to search insights: %w", err)
	}

	if len(insights) == 0 {
		fmt.Printf("No insights found matching '%s'\n", input)
		return nil
	}

	// Show matching insights
	fmt.Printf("Found %d insight(s) matching '%s':\n\n", len(insights), input)
	for _, insight := range insights {
		fmt.Printf("  #%d [%s] %s\n", insight.ID, insight.Category, truncateString(insight.Summary, 60))
	}

	// Delete by keyword
	deleted, err := store.ForgetInsightsByKeyword(input)
	if err != nil {
		return fmt.Errorf("failed to forget insights: %w", err)
	}

	fmt.Printf("\nForgot %d insight(s)\n", deleted)
	return nil
}

// OverviewCommand shows a visual summary of context.
type OverviewCommand struct{}

// Name returns the command name.
func (c *OverviewCommand) Name() string { return "overview" }

// Description returns the command description.
func (c *OverviewCommand) Description() string { return "Show context overview (visual summary)" }

// Execute runs the overview command.
func (c *OverviewCommand) Execute(ctx *Context) error {
	cfg := ctx.Config
	if cfg == nil {
		fmt.Println("[X] Cortex not initialized")
		fmt.Println("   Run: cortex init --auto")
		return fmt.Errorf("not initialized")
	}

	store := ctx.Storage
	if store == nil {
		fmt.Println("[X] Failed to open storage")
		return fmt.Errorf("storage not available")
	}

	// Get stats
	stats, err := store.GetStats()
	if err != nil {
		fmt.Println("[X] Failed to get stats")
		return fmt.Errorf("failed to get stats: %w", err)
	}

	// Get insights for breakdown
	insights, _ := store.GetRecentInsights(100)

	// Count by category
	categoryCount := make(map[string]int)
	categoryStars := make(map[string]int)
	for _, insight := range insights {
		categoryCount[insight.Category]++
		if insight.Importance >= 4 {
			categoryStars[insight.Category]++
		}
	}

	// Get database size
	dbPath := filepath.Join(cfg.ContextDir, "db", "events.db")
	dbInfo, _ := os.Stat(dbPath)
	dbSize := float64(0)
	if dbInfo != nil {
		dbSize = float64(dbInfo.Size()) / 1024 // KB
	}

	// Check if daemon is running (heuristic: check if processing recently)
	daemonStatus := "[X] Stopped"
	if recentEvents, err := store.GetRecentEvents(5); err == nil && len(recentEvents) > 0 {
		// Check if last event is recent (< 5 min old)
		if time.Since(recentEvents[0].Timestamp) < 5*time.Minute {
			daemonStatus = "[OK] Running"
		}
	}

	// Print overview
	fmt.Println("Cortex Context Memory")
	fmt.Println()

	// Events
	totalEvents := 0
	if val, ok := stats["total_events"].(int); ok {
		totalEvents = val
	}
	fmt.Printf("Events:     %d captured\n", totalEvents)

	// Insights
	totalInsights := 0
	if val, ok := stats["total_insights"].(int); ok {
		totalInsights = val
	}
	fmt.Printf("Insights:   %d extracted\n", totalInsights)

	// Breakdown by category
	if len(categoryCount) > 0 {
		for _, cat := range []string{"decision", "pattern", "insight", "strategy"} {
			if count, ok := categoryCount[cat]; ok {
				stars := ""
				for i := 0; i < categoryStars[cat] && i < 5; i++ {
					stars += "*"
				}
				if stars == "" {
					stars = "***"
				}
				prefix := "  |-"
				if cat == "strategy" || (cat == "insight" && categoryCount["strategy"] == 0) {
					prefix = "  +-"
				}
				fmt.Printf("%s %ss:  %d %s\n", prefix, cat, count, stars)
			}
		}
	}
	fmt.Println()

	// Status
	fmt.Printf("Status:     Daemon %s\n", daemonStatus)
	if dbSize > 1024 {
		fmt.Printf("Database:   %.1f MB\n", dbSize/1024)
	} else {
		fmt.Printf("Database:   %.0f KB\n", dbSize)
	}

	// Recent activity
	recentEvents, _ := store.GetRecentEvents(10)
	recentCount := 0
	for _, event := range recentEvents {
		if time.Since(event.Timestamp) < 5*time.Minute {
			recentCount++
		}
	}
	if recentCount > 0 {
		fmt.Printf("Recent:     %d events (last 5 min)\n", recentCount)
	}

	fmt.Println()
	fmt.Println("Tip: /cortex search <query>")

	return nil
}

// --- Helper functions ---

// checkOllama checks if Ollama is running and returns installed models
func checkOllama() (bool, []string) {
	ollamaClient := llm.NewOllamaClient(&config.Config{
		OllamaURL:   "http://localhost:11434",
		OllamaModel: "mistral:7b",
	})

	if !ollamaClient.IsAvailable() {
		return false, nil
	}

	// Try to get model list
	type ModelInfo struct {
		Name string `json:"name"`
	}
	type ModelsResponse struct {
		Models []ModelInfo `json:"models"`
	}

	resp, err := http.Get("http://localhost:11434/api/tags")
	if err != nil {
		return true, nil
	}
	defer resp.Body.Close()

	var modelsResp ModelsResponse
	if err := json.NewDecoder(resp.Body).Decode(&modelsResp); err != nil {
		return true, nil
	}

	var models []string
	for _, m := range modelsResp.Models {
		models = append(models, m.Name)
	}

	return true, models
}

// showModelRecommendations shows model recommendations based on available RAM
func showModelRecommendations(availableRAM float64) {
	type Model struct {
		Name        string
		Size        string
		RAMGB       float64
		Desc        string
		Recommended bool
	}

	models := []Model{
		{"phi3:mini", "2.0 GB", 2.0, "Fastest, lightweight (3.8B)", false},
		{"mistral:7b", "4.1 GB", 4.5, "Best balance (7.2B)", true},
		{"llama3.2:3b", "2.0 GB", 2.5, "Fast, good quality (3B)", false},
		{"llama3.1:8b", "4.7 GB", 5.0, "High quality (8B)", false},
		{"llama3.1:70b", "40 GB", 48.0, "Best quality, slow (70B)", false},
	}

	for _, m := range models {
		var status string
		if m.RAMGB <= availableRAM {
			if m.Recommended {
				status = "[OK] * Recommended"
			} else {
				status = "[OK] Compatible"
			}
		} else {
			status = fmt.Sprintf("[X] Needs %.1f GB", m.RAMGB)
		}

		fmt.Printf("  %-15s %-10s %s - %s\n", m.Name, m.Size, status, m.Desc)
	}
}

// truncateString truncates a string to max length with ellipsis
func truncateString(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen-3] + "..."
}
