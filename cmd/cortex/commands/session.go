// Package commands provides CLI command implementations.
package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/dereksantos/cortex/integrations/claude"
	"github.com/dereksantos/cortex/internal/capture"
	intcognition "github.com/dereksantos/cortex/internal/cognition"
	"github.com/dereksantos/cortex/internal/cognition/sources"
	"github.com/dereksantos/cortex/internal/storage"
	"github.com/dereksantos/cortex/pkg/cognition"
	"github.com/dereksantos/cortex/pkg/llm"
)

func init() {
	Register(&SessionStartCommand{})
	Register(&InjectContextCommand{})
	Register(&StopCommand{})
	Register(&CLICommand{})
}

// tryLogError attempts to log an error to activity.log for watch visibility.
// It fails silently if the context directory is not available.
func tryLogError(contextDir, command string, err error) {
	if contextDir == "" {
		return
	}
	logger := intcognition.NewActivityLogger(contextDir)
	_ = logger.LogError(command, err)
}

// SessionStartCommand prints session start instructions.
type SessionStartCommand struct{}

// Name returns the command name.
func (c *SessionStartCommand) Name() string { return "session-start" }

// Description returns the command description.
func (c *SessionStartCommand) Description() string {
	return "Print session start instructions for hooks"
}

// Execute runs the session-start command.
func (c *SessionStartCommand) Execute(ctx *Context) error {
	instructions := `Cortex Context Memory Available

Quick Commands:
  cortex status          # Check if daemon is running
  cortex daemon &        # Start background processor (if not running)
  cortex search "query"  # Find relevant context from past work
  cortex insights        # View extracted decisions and patterns
  cortex recent          # Show recent development events

Tip: If daemon isn't running, suggest starting it to enable automatic context capture.
Use 'cortex search' to find relevant past decisions before making new architectural choices.`

	fmt.Println(instructions)
	return nil
}

// InjectContextCommand handles context injection for prompts.
type InjectContextCommand struct{}

// Name returns the command name.
func (c *InjectContextCommand) Name() string { return "inject-context" }

// Description returns the command description.
func (c *InjectContextCommand) Description() string {
	return "Inject relevant context into prompt for hooks"
}

// Execute runs the inject-context command.
func (c *InjectContextCommand) Execute(ctx *Context) error {
	// Read hook data from stdin (JSON from UserPromptSubmit hook)
	hookData, err := io.ReadAll(os.Stdin)
	if err != nil || len(hookData) == 0 {
		// No data provided, exit silently
		return nil
	}

	// Parse hook data to extract prompt and session info
	promptEvent, err := claude.ConvertPromptEvent(hookData, "")
	var prompt string
	var sessionID string
	if err != nil || promptEvent == nil {
		// Fallback: treat raw input as prompt (backwards compatibility)
		prompt = string(hookData)
	} else {
		prompt = promptEvent.Prompt
		sessionID = promptEvent.Context.SessionID
	}

	if prompt == "" {
		return nil
	}

	// Load config
	cfg := ctx.Config
	if cfg == nil {
		// Log error if we can find context dir
		tryLogError(".cortex", "inject-context", fmt.Errorf("config not loaded"))
		fmt.Println(prompt)
		return nil
	}

	// Update project path from hook data if available
	if promptEvent != nil && promptEvent.Context.WorkingDir != "" {
		cfg.ProjectRoot = promptEvent.Context.WorkingDir
	}

	// Get storage
	store := ctx.Storage
	if store == nil {
		tryLogError(cfg.ContextDir, "inject-context", fmt.Errorf("storage not available"))
		fmt.Println(prompt)
		return nil
	}

	// Capture the prompt as an event (non-blocking)
	if promptEvent != nil {
		promptEvent.Context.ProjectPath = cfg.ProjectRoot
		go func() {
			cap := capture.New(cfg)
			cap.CaptureEvent(promptEvent)
		}()
	}

	// Initialize LLM provider via the unified surface (OpenRouter then
	// Anthropic). Optional — Reflect will degrade gracefully if nil.
	var llmProvider llm.Provider
	if p, _, err := llm.NewLLMClient(cfg); err == nil {
		llmProvider = p
	}
	ollama := llm.NewOllamaClient(cfg)

	// Initialize embedder with fallback: Ollama -> Hugot
	hugotEmbedder := llm.NewHugotEmbedder()
	embedder := llm.NewFallbackEmbedder(ollama, hugotEmbedder)

	// Create Cortex cognitive pipeline
	cortex, err := intcognition.New(store, llmProvider, embedder, cfg)
	if err != nil {
		tryLogError(cfg.ContextDir, "inject-context", fmt.Errorf("cortex init: %w", err))
		fmt.Println(prompt)
		return nil
	}

	// Create state writer for status updates (shared with daemon)
	stateWriter := intcognition.NewStateWriter(cfg.ContextDir)
	cortex.SetStateWriter(stateWriter)

	// Register dream sources for background exploration. The observer
	// emits observation.* journal entries on each substrate read so
	// derivations carry full provenance.
	observer := sources.NewObserver(cfg.ContextDir)
	projSrc := sources.NewProjectSource(cfg.ProjectRoot)
	projSrc.SetObserver(observer)
	cortex.RegisterSource(projSrc)
	cortex.RegisterSource(sources.NewCortexSource(store))

	// Register Claude history source for session transcript exploration
	homeDir, _ := os.UserHomeDir()
	if homeDir != "" {
		claudeProjectsDir := filepath.Join(homeDir, ".claude", "projects")
		claudeSrc := sources.NewClaudeHistorySource(claudeProjectsDir)
		claudeSrc.SetObserver(observer)
		cortex.RegisterSource(claudeSrc)
	}

	// Register git source for commit history exploration
	gitSrc := sources.NewGitSource(cfg.ProjectRoot)
	gitSrc.SetObserver(observer)
	cortex.RegisterSource(gitSrc)

	// Determine if this is the first prompt of the session
	isFirstPrompt := isFirstPromptInSession(store, sessionID)

	// Build query
	query := cognition.Query{
		Text:      prompt,
		Limit:     5,
		Threshold: 0.3,
	}

	// Use Full mode for first prompt (sync Think), Fast mode for subsequent
	mode := cognition.Fast
	if isFirstPrompt {
		mode = cognition.Full
	}

	// Track retrieval timing
	retrieveStart := time.Now()
	result, err := cortex.Retrieve(context.Background(), query, mode)
	retrieveElapsed := time.Since(retrieveStart)

	// Write retrieval stats (best effort, don't block on errors)
	go func() {
		writeRetrievalStats(cfg.ContextDir, prompt, mode, result, retrieveElapsed)
	}()

	if err != nil || result.Decision != cognition.Inject {
		// No relevant context or decision to skip injection
		fmt.Println(prompt)
		return nil
	}

	// Output formatted context + original prompt
	fmt.Print(result.Formatted)
	fmt.Println("User Request:")
	fmt.Println(prompt)
	return nil
}

// StopCommand handles the Stop hook - captures transcript path for Dream analysis.
type StopCommand struct{}

// Name returns the command name.
func (c *StopCommand) Name() string { return "stop" }

// Description returns the command description.
func (c *StopCommand) Description() string {
	return "Handle session stop and capture transcript for hooks"
}

// Execute runs the stop command.
func (c *StopCommand) Execute(ctx *Context) error {
	// Read hook data from stdin (JSON from Stop hook)
	hookData, err := io.ReadAll(os.Stdin)
	if err != nil || len(hookData) == 0 {
		// No data provided, exit silently
		return nil
	}

	// Parse hook data to extract transcript path and session info
	stopEvent, err := claude.ConvertStopEvent(hookData, "")
	if err != nil || stopEvent == nil {
		// Can't parse, exit silently
		return nil
	}

	// Get config and storage
	cfg := ctx.Config
	if cfg == nil {
		tryLogError(".cortex", "stop", fmt.Errorf("config not loaded"))
		return nil
	}

	// Update project path from hook data if available
	if stopEvent.Context.WorkingDir != "" {
		cfg.ProjectRoot = stopEvent.Context.WorkingDir
	}

	store := ctx.Storage
	if store == nil {
		tryLogError(cfg.ContextDir, "stop", fmt.Errorf("storage not available"))
		return nil
	}

	// Capture the stop event
	cap := capture.New(cfg)
	if err := cap.CaptureEvent(stopEvent); err != nil {
		// Log error but continue
		fmt.Fprintf(os.Stderr, "Warning: failed to capture stop event: %v\n", err)
	}

	// Queue transcript for Dream analysis if path is available
	if stopEvent.TranscriptPath != "" {
		// Write transcript path to a queue file for daemon to pick up
		queueDir := filepath.Join(cfg.ContextDir, "transcript_queue")
		if err := os.MkdirAll(queueDir, 0755); err == nil {
			queueFile := filepath.Join(queueDir, fmt.Sprintf("%d.json", time.Now().UnixNano()))
			queueData := map[string]string{
				"transcript_path": stopEvent.TranscriptPath,
				"session_id":      stopEvent.Context.SessionID,
			}
			if data, err := json.Marshal(queueData); err == nil {
				os.WriteFile(queueFile, data, 0644)
			}
		}
	}

	return nil
}

// CLICommand routes slash command arguments.
type CLICommand struct{}

// Name returns the command name.
func (c *CLICommand) Name() string { return "cli" }

// Description returns the command description.
func (c *CLICommand) Description() string { return "Route slash command arguments for /cortex" }

// Execute runs the cli command.
func (c *CLICommand) Execute(ctx *Context) error {
	args := ctx.Args

	if len(args) == 0 {
		// No args - show overview (delegate to overview command)
		return executeOverview(ctx)
	}

	subcommand := args[0]

	switch subcommand {
	case "search":
		// cortex cli search <query>
		if len(args) < 2 {
			fmt.Println("Usage: /cortex search <query>")
			return fmt.Errorf("missing search query")
		}
		// Reconstruct search query from remaining args
		query := strings.Join(args[1:], " ")
		searchCmd := &SearchCommand{}
		return searchCmd.Execute(&Context{
			Config:  ctx.Config,
			Storage: ctx.Storage,
			Args:    []string{query},
		})

	case "insights":
		// cortex cli insights
		insightsCmd := &InsightsCommand{}
		return insightsCmd.Execute(&Context{
			Config:  ctx.Config,
			Storage: ctx.Storage,
			Args:    []string{},
		})

	case "status":
		// cortex cli status - show info
		return executeInfo(ctx)

	default:
		// Treat entire input as search query
		query := strings.Join(args, " ")
		searchCmd := &SearchCommand{}
		return searchCmd.Execute(&Context{
			Config:  ctx.Config,
			Storage: ctx.Storage,
			Args:    []string{query},
		})
	}
}

// isFirstPromptInSession checks if this is the first prompt for this session.
func isFirstPromptInSession(store *storage.Storage, sessionID string) bool {
	if sessionID == "" {
		return true // Assume first if no session ID
	}

	// Check if we've seen this session before
	count, err := store.CountEventsBySession(sessionID)
	if err != nil {
		return true // Assume first on error
	}
	return count == 0
}

// writeRetrievalStats writes retrieval statistics and logs activity.
// This is called after each cortex.Retrieve() in InjectContextCommand.
func writeRetrievalStats(contextDir string, query string, mode cognition.RetrieveMode, result *cognition.ResolveResult, elapsed time.Duration) {
	// Read existing stats to update total count
	existingStats, _ := intcognition.ReadRetrievalStats(contextDir)
	totalRetrievals := 1
	if existingStats != nil {
		totalRetrievals = existingStats.TotalRetrievals + 1
	}

	// Determine mode string
	modeStr := "fast"
	if mode == cognition.Full {
		modeStr = "full"
	}

	// Build stats
	stats := &intcognition.RetrievalStats{
		LastQuery:       truncateString(query, 100),
		LastMode:        modeStr,
		LastReflexMs:    elapsed.Milliseconds(), // Total time as estimate
		LastReflectMs:   0,
		LastResolveMs:   0,
		LastResults:     0,
		LastDecision:    "skip",
		TotalRetrievals: totalRetrievals,
	}

	if result != nil {
		stats.LastResults = len(result.Results)
		stats.LastDecision = result.Decision.String()
		stats.LastResolveMs = result.ResolveMs
	}

	// For Full mode, estimate reflect took majority of time
	// Subtract resolve time from elapsed before estimating reflex/reflect split
	elapsedMinusResolve := elapsed.Milliseconds() - stats.LastResolveMs
	if mode == cognition.Full && elapsedMinusResolve > 50 {
		stats.LastReflexMs = 10 // Estimate ~10ms for reflex
		stats.LastReflectMs = elapsedMinusResolve - 10
	} else {
		// Fast mode or short elapsed: most time is reflex
		stats.LastReflexMs = elapsedMinusResolve
	}

	// Note: retrieval_stats.json is no longer written from this path
	// (Z1 unification). The watch UI now reads from the journal-projected
	// storage.Retrievals via retrievalStatsFromStorage(). The
	// retrieval_stats_history.jsonl latency-trend log is preserved
	// because its per-step latency breakdown isn't yet captured in
	// resolve.retrieval entries — see docs/journal-design-log.md item 5.
	historyWriter := intcognition.NewRetrievalStatsHistoryWriter(contextDir)
	historyWriter.AppendFromStats(stats)

	// Log the activity
	logger := intcognition.NewActivityLogger(contextDir)

	// Log reflex
	reflexEntry := &intcognition.ActivityLogEntry{
		Timestamp:   time.Now(),
		Mode:        "reflex",
		Description: fmt.Sprintf("%d results for \"%s\"", stats.LastResults, truncateString(query, 30)),
		Query:       query,
		Results:     stats.LastResults,
		LatencyMs:   stats.LastReflexMs,
	}
	logger.Log(reflexEntry)

	// If Full mode, also log reflect
	if mode == cognition.Full {
		reflectEntry := &intcognition.ActivityLogEntry{
			Timestamp:   time.Now(),
			Mode:        "reflect",
			Description: "reranked results (Full mode)",
			LatencyMs:   stats.LastReflectMs,
		}
		logger.Log(reflectEntry)
	}

	// Log resolve decision
	resolveEntry := &intcognition.ActivityLogEntry{
		Timestamp:   time.Now(),
		Mode:        "resolve",
		Description: fmt.Sprintf("%s decision, %d results", stats.LastDecision, stats.LastResults),
		Results:     stats.LastResults,
	}
	logger.Log(resolveEntry)
}

// executeOverview delegates to the overview handler.
// This is a stub that will be replaced when overview is extracted.
func executeOverview(ctx *Context) error {
	// For now, show a basic overview using available data
	store := ctx.Storage
	if store == nil {
		fmt.Println("Cortex not initialized")
		return nil
	}

	// Get stats
	stats, err := store.GetStats()
	if err != nil {
		fmt.Println("Failed to get stats")
		return nil
	}

	// Get insights for breakdown
	insights, _ := store.GetRecentInsights(100)

	// Count by category
	categoryCount := make(map[string]int)
	for _, insight := range insights {
		categoryCount[insight.Category]++
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
				fmt.Printf("  - %ss: %d\n", cat, count)
			}
		}
	}
	fmt.Println()
	fmt.Println("Try: /cortex search <query>")

	return nil
}

// executeInfo shows system info.
// This is a stub that displays basic status.
func executeInfo(ctx *Context) error {
	fmt.Println("Cortex System Status")
	fmt.Println()

	cfg := ctx.Config
	if cfg != nil {
		fmt.Printf("Project: %s\n", cfg.ProjectRoot)
	}

	store := ctx.Storage
	if store != nil {
		stats, err := store.GetStats()
		if err == nil {
			if totalEvents, ok := stats["total_events"].(int); ok {
				fmt.Printf("Events: %d\n", totalEvents)
			}
			if totalInsights, ok := stats["total_insights"].(int); ok {
				fmt.Printf("Insights: %d\n", totalInsights)
			}
		}
	}

	return nil
}
