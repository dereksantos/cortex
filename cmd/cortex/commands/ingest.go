// Package commands provides CLI command implementations.
package commands

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/dereksantos/cortex/integrations/claude"
	"github.com/dereksantos/cortex/integrations/cursor"
	"github.com/dereksantos/cortex/internal/capture"
	"github.com/dereksantos/cortex/internal/queue"
	"github.com/dereksantos/cortex/internal/storage"
	"github.com/dereksantos/cortex/pkg/config"
	"github.com/dereksantos/cortex/pkg/events"
	"github.com/dereksantos/cortex/pkg/llm"
)

// CaptureCommand implements event capture from stdin.
type CaptureCommand struct{}

// IngestCommand implements moving queued events to database.
type IngestCommand struct{}

// AnalyzeCommand implements LLM analysis on recent events.
type AnalyzeCommand struct{}

// ProcessCommand implements combined ingest + analyze (backward compat).
type ProcessCommand struct{}

func init() {
	Register(&CaptureCommand{})
	Register(&IngestCommand{})
	Register(&AnalyzeCommand{})
	Register(&ProcessCommand{})
}

// Name returns the command name.
func (c *CaptureCommand) Name() string { return "capture" }

// Description returns the command description.
func (c *CaptureCommand) Description() string { return "Capture event from stdin (used by AI tools)" }

// Execute runs the capture command.
func (c *CaptureCommand) Execute(ctx *Context) error {
	// Load config if not provided
	cfg := ctx.Config
	if cfg == nil {
		var err error
		cfg, err = loadCaptureConfig()
		if err != nil {
			// Silent failure for capture
			os.Exit(0)
		}
	}

	// Parse flags
	source := "claude" // default
	captureType := ""
	content := ""

	for i := 0; i < len(ctx.Args); i++ {
		arg := ctx.Args[i]
		switch {
		case arg == "--source" && i+1 < len(ctx.Args):
			source = ctx.Args[i+1]
			i++
		case strings.HasPrefix(arg, "--type="):
			captureType = strings.TrimPrefix(arg, "--type=")
		case arg == "--type" && i+1 < len(ctx.Args):
			captureType = ctx.Args[i+1]
			i++
		case strings.HasPrefix(arg, "--content="):
			content = strings.TrimPrefix(arg, "--content=")
		case arg == "--content" && i+1 < len(ctx.Args):
			content = ctx.Args[i+1]
			i++
		}
	}

	// If --type and --content are provided, create event directly from CLI
	if captureType != "" && content != "" {
		event := &events.Event{
			Source:    events.SourceClaude,
			EventType: events.EventToolUse,
			Timestamp: time.Now(),
			ToolName:  "Capture",
			ToolInput: map[string]interface{}{
				"type":    captureType,
				"content": content,
			},
			ToolResult: content,
			Context: events.EventContext{
				ProjectPath: cfg.ProjectRoot,
			},
			Metadata: map[string]interface{}{
				"capture_type": captureType,
				"source":       "cli",
			},
		}

		cap := capture.New(cfg)
		if err := cap.CaptureEvent(event); err != nil {
			fmt.Fprintf(os.Stderr, "Failed to capture: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("Captured %s: %s\n", captureType, truncateString(content, 60))
		os.Exit(0)
	}

	// Read stdin
	data, err := io.ReadAll(os.Stdin)
	if err != nil || len(data) == 0 {
		os.Exit(0)
	}

	var event *events.Event

	// Convert based on source
	switch source {
	case "claude":
		event, err = claude.ConvertToEvent(data, cfg.ProjectRoot)
	case "cursor":
		event, err = cursor.ConvertToEvent(data, cfg.ProjectRoot)
	default:
		// Try Claude format as fallback
		event, err = claude.ConvertToEvent(data, cfg.ProjectRoot)
	}

	if err != nil {
		// Try direct capture as fallback
		cap := capture.New(cfg)
		_ = cap.CaptureFromStdin()
		os.Exit(0)
	}

	// Capture the converted event
	cap := capture.New(cfg)
	if err := cap.CaptureEvent(event); err != nil {
		// Silent failure
	}

	os.Exit(0)
	return nil
}

// Name returns the command name.
func (c *IngestCommand) Name() string { return "ingest" }

// Description returns the command description.
func (c *IngestCommand) Description() string { return "Move queued events to database" }

// Execute runs the ingest command.
func (c *IngestCommand) Execute(ctx *Context) error {
	cfg := ctx.Config
	store := ctx.Storage

	// Load config and storage if not provided
	if cfg == nil || store == nil {
		var err error
		cfg, err = loadCaptureConfig()
		if err != nil {
			return fmt.Errorf("failed to load config: %w", err)
		}

		store, err = storage.New(cfg)
		if err != nil {
			return fmt.Errorf("failed to open storage: %w", err)
		}
		defer store.Close()
	}

	// Process queue (move to DB only)
	queueMgr := queue.New(cfg, store)
	processed, err := queueMgr.ProcessPending()
	if err != nil {
		return fmt.Errorf("failed to process queue: %w", err)
	}

	fmt.Printf("Ingested %d events to database\n", processed)

	// Generate embeddings if vector search is enabled
	if cfg.EnableVector && processed > 0 {
		ollamaClient := llm.NewOllamaClient(cfg)
		hugotEmbedder := llm.NewHugotEmbedder()
		embedder := llm.NewFallbackEmbedder(ollamaClient, hugotEmbedder)
		if embedder.IsEmbeddingAvailable() {
			bgCtx := context.Background()
			events, _ := store.GetRecentEvents(processed)
			embedded := 0
			for _, event := range events {
				if event.ToolResult != "" {
					vec, err := embedder.Embed(bgCtx, event.ToolResult)
					if err == nil {
						store.StoreEmbedding(event.ID, "event", vec)
						embedded++
					}
				}
			}
			if embedded > 0 {
				fmt.Printf("Generated %d embeddings\n", embedded)
			}
		}
	}

	return nil
}

// Name returns the command name.
func (c *AnalyzeCommand) Name() string { return "analyze" }

// Description returns the command description.
func (c *AnalyzeCommand) Description() string { return "Run LLM analysis on recent events" }

// Execute runs the analyze command.
func (c *AnalyzeCommand) Execute(ctx *Context) error {
	cfg := ctx.Config
	store := ctx.Storage

	// Load config and storage if not provided
	if cfg == nil || store == nil {
		var err error
		cfg, err = loadCaptureConfig()
		if err != nil {
			return fmt.Errorf("failed to load config: %w", err)
		}

		store, err = storage.New(cfg)
		if err != nil {
			return fmt.Errorf("failed to open storage: %w", err)
		}
		defer store.Close()
	}

	// Get limit from args (default: 10)
	limit := 10
	if len(ctx.Args) >= 1 {
		fmt.Sscanf(ctx.Args[0], "%d", &limit)
	}

	// Get recent events
	recentEvents, err := store.GetRecentEvents(limit)
	if err != nil {
		return fmt.Errorf("failed to get recent events: %w", err)
	}

	if len(recentEvents) == 0 {
		fmt.Println("No events to analyze")
		return nil
	}

	fmt.Printf("Analyzing %d events with LLM...\n", len(recentEvents))

	// Use LLM directly for analysis (cognition modes handle this normally)
	var llmProvider llm.Provider
	anthropic := llm.NewAnthropicClient(cfg)
	if anthropic.IsAvailable() {
		llmProvider = anthropic
	} else {
		ollama := llm.NewOllamaClient(cfg)
		if ollama.IsAvailable() {
			llmProvider = ollama
		}
	}

	if llmProvider == nil {
		fmt.Println("No LLM available (check Ollama or ANTHROPIC_API_KEY)")
		return nil
	}

	// Analyze events and store insights
	analyzed := 0
	for _, event := range recentEvents {
		if err := AnalyzeEventWithLLM(event, store, llmProvider); err == nil {
			analyzed++
		}
	}

	if analyzed > 0 {
		fmt.Printf("Analyzed %d events\n", analyzed)
	} else {
		fmt.Println("No events were analyzed")
	}

	return nil
}

// Name returns the command name.
func (c *ProcessCommand) Name() string { return "process" }

// Description returns the command description.
func (c *ProcessCommand) Description() string { return "Process queue + analyze (backward compat)" }

// Execute runs the process command (ingest + analyze).
func (c *ProcessCommand) Execute(ctx *Context) error {
	cfg := ctx.Config
	store := ctx.Storage

	// Load config and storage if not provided
	if cfg == nil || store == nil {
		var err error
		cfg, err = loadCaptureConfig()
		if err != nil {
			return fmt.Errorf("failed to load config: %w", err)
		}

		store, err = storage.New(cfg)
		if err != nil {
			return fmt.Errorf("failed to open storage: %w", err)
		}
		defer store.Close()
	}

	// Process queue
	queueMgr := queue.New(cfg, store)
	processed, err := queueMgr.ProcessPending()
	if err != nil {
		return fmt.Errorf("failed to process queue: %w", err)
	}

	fmt.Printf("Processed %d events\n", processed)

	// If events were processed, run analysis immediately
	if processed > 0 {
		// Get LLM provider
		var llmProvider llm.Provider
		anthropic := llm.NewAnthropicClient(cfg)
		if anthropic.IsAvailable() {
			llmProvider = anthropic
		} else {
			ollama := llm.NewOllamaClient(cfg)
			if ollama.IsAvailable() {
				llmProvider = ollama
			}
		}

		if llmProvider == nil {
			fmt.Println("No LLM available for analysis")
			return nil
		}

		// Analyze recent events
		recentEvents, err := store.GetRecentEvents(processed)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Warning: Failed to get recent events: %v\n", err)
			return nil
		}

		fmt.Printf("Analyzing %d events with LLM...\n", len(recentEvents))

		// Run analysis synchronously for immediate results
		analyzed := 0
		for _, event := range recentEvents {
			if err := AnalyzeEventWithLLM(event, store, llmProvider); err == nil {
				analyzed++
			}
		}

		if analyzed > 0 {
			fmt.Printf("Analyzed %d events\n", analyzed)
		}
	}

	return nil
}

// --- Helper functions ---

// loadCaptureConfig loads config from the current working directory.
func loadCaptureConfig() (*config.Config, error) {
	projectRoot, err := os.Getwd()
	if err != nil {
		return nil, err
	}

	configPath := fmt.Sprintf("%s/.context/config.json", projectRoot)
	return config.Load(configPath)
}
