// Cortex - Context memory for AI development
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"github.com/dereksantos/cortex/integrations/claude"
	"github.com/dereksantos/cortex/internal/capture"
	"github.com/dereksantos/cortex/internal/processor"
	"github.com/dereksantos/cortex/internal/queue"
	"github.com/dereksantos/cortex/internal/storage"
	"github.com/dereksantos/cortex/pkg/config"
)

const version = "0.1.0"

func main() {
	if len(os.Args) < 2 {
		printUsage()
		os.Exit(1)
	}

	command := os.Args[1]

	switch command {
	case "capture":
		handleCapture()
	case "init":
		handleInit()
	case "process":
		handleProcess()
	case "daemon":
		handleDaemon()
	case "stats":
		handleStats()
	case "status":
		handleStatus()
	case "search":
		handleSearch()
	case "recent":
		handleRecent()
	case "version":
		fmt.Printf("cortex version %s\n", version)
	case "help", "-h", "--help":
		printUsage()
	default:
		fmt.Fprintf(os.Stderr, "Unknown command: %s\n\n", command)
		printUsage()
		os.Exit(1)
	}
}

func handleCapture() {
	// Load config
	cfg, err := loadConfig()
	if err != nil {
		// Silent failure
		os.Exit(0)
	}

	// Read stdin
	data, err := io.ReadAll(os.Stdin)
	if err != nil || len(data) == 0 {
		os.Exit(0)
	}

	// Convert Claude event to generic event
	event, err := claude.ConvertToEvent(data, cfg.ProjectRoot)
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
}

func handleInit() {
	// Get project root
	projectRoot, err := os.Getwd()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to get working directory: %v\n", err)
		os.Exit(1)
	}

	// Create default config
	cfg := config.Default()
	cfg.ProjectRoot = projectRoot

	// Ensure directories
	if err := cfg.EnsureDirectories(); err != nil {
		fmt.Fprintf(os.Stderr, "Failed to create directories: %v\n", err)
		os.Exit(1)
	}

	// Save config
	configPath := fmt.Sprintf("%s/.context/config.json", projectRoot)
	if err := cfg.Save(configPath); err != nil {
		fmt.Fprintf(os.Stderr, "Failed to save config: %v\n", err)
		os.Exit(1)
	}

	fmt.Println("✅ Cortex initialized successfully!")
	fmt.Printf("   Config: %s\n", configPath)
	fmt.Printf("   Context directory: %s\n", cfg.ContextDir)
	fmt.Println("\n📖 Next steps:")
	fmt.Println("   1. Configure your AI tool to use: cortex capture")
	fmt.Println("   2. Start the processor: cortex daemon")
	fmt.Println("   3. Search your context: cortex search <query>")
}

func handleProcess() {
	// Load config
	cfg, err := loadConfig()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to load config: %v\n", err)
		os.Exit(1)
	}

	// Open storage
	store, err := storage.New(cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to open storage: %v\n", err)
		os.Exit(1)
	}
	defer store.Close()

	// Process queue
	queueMgr := queue.New(cfg, store)
	processed, err := queueMgr.ProcessPending()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to process queue: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("✅ Processed %d events\n", processed)
}

func handleDaemon() {
	// Load config
	cfg, err := loadConfig()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to load config: %v\n", err)
		os.Exit(1)
	}

	// Open storage
	store, err := storage.New(cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to open storage: %v\n", err)
		os.Exit(1)
	}
	defer store.Close()

	// Create queue manager
	queueMgr := queue.New(cfg, store)

	// Create and start processor
	proc := processor.New(cfg, store, queueMgr)
	if err := proc.Start(); err != nil {
		fmt.Fprintf(os.Stderr, "Failed to start processor: %v\n", err)
		os.Exit(1)
	}

	fmt.Println("🤖 Cortex daemon started")
	fmt.Println("   Processing events every 5 seconds...")
	fmt.Println("   Press Ctrl+C to stop")

	// Wait for interrupt signal
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)
	<-sigChan

	fmt.Println("\n🛑 Stopping daemon...")
	proc.Stop()
	fmt.Println("✅ Daemon stopped")
}

func handleStats() {
	// Load config
	cfg, err := loadConfig()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to load config: %v\n", err)
		os.Exit(1)
	}

	// Open storage
	store, err := storage.New(cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to open storage: %v\n", err)
		os.Exit(1)
	}
	defer store.Close()

	// Get stats
	stats, err := store.GetStats()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to get stats: %v\n", err)
		os.Exit(1)
	}

	// Pretty print stats
	data, _ := json.MarshalIndent(stats, "", "  ")
	fmt.Println(string(data))
}

func handleStatus() {
	// Simple status line for now
	// Future: check daemon running, queue size, etc.
	fmt.Print("🤖🧠💡")
}

func handleSearch() {
	if len(os.Args) < 3 {
		fmt.Fprintf(os.Stderr, "Usage: cortex search <query>\n")
		os.Exit(1)
	}

	query := os.Args[2]

	// Load config
	cfg, err := loadConfig()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to load config: %v\n", err)
		os.Exit(1)
	}

	// Open storage
	store, err := storage.New(cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to open storage: %v\n", err)
		os.Exit(1)
	}
	defer store.Close()

	// Search events
	events, err := store.SearchEvents(query, 10)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to search: %v\n", err)
		os.Exit(1)
	}

	// Display results
	if len(events) == 0 {
		fmt.Println("No results found")
		return
	}

	fmt.Printf("Found %d results:\n\n", len(events))
	for i, event := range events {
		fmt.Printf("%d. [%s] %s - %s\n", i+1, event.Source, event.ToolName, event.Timestamp.Format("2006-01-02 15:04"))
		if event.ToolResult != "" {
			preview := event.ToolResult
			if len(preview) > 100 {
				preview = preview[:100] + "..."
			}
			fmt.Printf("   %s\n", preview)
		}
		fmt.Println()
	}
}

func handleRecent() {
	limit := 10
	if len(os.Args) >= 3 {
		fmt.Sscanf(os.Args[2], "%d", &limit)
	}

	// Load config
	cfg, err := loadConfig()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to load config: %v\n", err)
		os.Exit(1)
	}

	// Open storage
	store, err := storage.New(cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to open storage: %v\n", err)
		os.Exit(1)
	}
	defer store.Close()

	// Get recent events
	events, err := store.GetRecentEvents(limit)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to get recent events: %v\n", err)
		os.Exit(1)
	}

	// Display results
	if len(events) == 0 {
		fmt.Println("No events found")
		return
	}

	fmt.Printf("Recent %d events:\n\n", len(events))
	for i, event := range events {
		fmt.Printf("%d. [%s] %s - %s\n", i+1, event.Source, event.ToolName, event.Timestamp.Format("2006-01-02 15:04"))
		if filePath, ok := event.ToolInput["file_path"].(string); ok {
			fmt.Printf("   File: %s\n", filePath)
		}
		fmt.Println()
	}
}

func loadConfig() (*config.Config, error) {
	projectRoot, err := os.Getwd()
	if err != nil {
		return nil, err
	}

	configPath := fmt.Sprintf("%s/.context/config.json", projectRoot)
	return config.Load(configPath)
}

func printUsage() {
	fmt.Printf(`Cortex %s - Context memory for AI development

Usage:
  cortex <command> [options]

Commands:
  capture     Capture event from stdin (used by AI tools)
  init        Initialize Cortex in current directory
  daemon      Start background processor
  search      Search captured context
  ask         Ask questions about your context
  stats       Show statistics
  version     Show version
  help        Show this help

Examples:
  # Initialize in project
  cortex init

  # Capture from AI tool (in hook)
  echo '{"tool_name":"Edit",...}' | cortex capture

  # Search context
  cortex search "authentication decisions"

  # Ask questions
  cortex ask "why did we choose JWT?"

For more information: https://github.com/dereksantos/cortex
`, version)
}
