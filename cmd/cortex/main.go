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
	"github.com/dereksantos/cortex/pkg/llm"
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
	case "insights":
		handleInsights()
	case "entities":
		handleEntities()
	case "graph":
		handleGraph()
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
	// Check for --auto flag
	autoSetup := false
	if len(os.Args) >= 3 && os.Args[2] == "--auto" {
		autoSetup = true
	}

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

	if autoSetup {
		fmt.Println("\n🔍 Auto-detecting environment...")
		runAutoSetup(projectRoot)
	} else {
		fmt.Println("\n📖 Next steps:")
		fmt.Println("   1. Configure your AI tool to use: cortex capture")
		fmt.Println("   2. Start the processor: cortex daemon")
		fmt.Println("   3. Search your context: cortex search <query>")
		fmt.Println("\n💡 Tip: Run 'cortex init --auto' for automatic setup")
	}
}

func runAutoSetup(projectRoot string) {
	// Get absolute path to cortex binary
	cortexPath, err := os.Executable()
	if err != nil {
		cortexPath = fmt.Sprintf("%s/cortex", projectRoot)
	}

	// Detect Claude Code
	claudeDir := fmt.Sprintf("%s/.claude", projectRoot)
	if _, err := os.Stat(claudeDir); err == nil {
		fmt.Println("\n✅ Detected Claude Code")
		if err := setupClaudeCode(claudeDir, cortexPath); err != nil {
			fmt.Printf("   ⚠️  Failed to configure Claude Code: %v\n", err)
		} else {
			fmt.Println("   ✅ Configured hooks in .claude/settings.local.json")
		}
	} else {
		fmt.Println("\n❌ Claude Code not detected (.claude directory not found)")
	}

	// Detect Ollama
	fmt.Println("\n🧠 Checking Ollama...")
	client := llm.NewOllamaClient(config.Default())
	if client.IsAvailable() {
		fmt.Println("   ✅ Ollama is running")

		// Check for model
		if client.IsModelAvailable() {
			fmt.Printf("   ✅ Model '%s' is available\n", config.Default().OllamaModel)
		} else {
			fmt.Printf("   ⚠️  Model '%s' not found\n", config.Default().OllamaModel)
			fmt.Println("   💡 Run: ollama pull mistral:7b")
		}
	} else {
		fmt.Println("   ❌ Ollama is not running")
		fmt.Println("   💡 Install from: https://ollama.ai")
	}

	fmt.Println("\n🎉 Auto-setup complete!")
	fmt.Println("\n📖 Next steps:")
	fmt.Println("   1. Start the processor: cortex daemon")
	fmt.Println("   2. Use Claude Code normally - events will be captured automatically")
	fmt.Println("   3. View insights: cortex insights")
}

func setupClaudeCode(claudeDir, cortexPath string) error {
	settingsPath := fmt.Sprintf("%s/settings.local.json", claudeDir)

	// Read existing settings or create new
	var settings map[string]interface{}
	data, err := os.ReadFile(settingsPath)
	if err != nil {
		// File doesn't exist, create new settings
		settings = make(map[string]interface{})
	} else {
		// Parse existing settings
		if err := json.Unmarshal(data, &settings); err != nil {
			return fmt.Errorf("failed to parse existing settings: %w", err)
		}
	}

	// Configure hooks (preserve existing ones if needed)
	hooks := map[string]interface{}{
		"PostToolUse": []interface{}{
			map[string]interface{}{
				"hooks": []interface{}{
					map[string]interface{}{
						"type":    "command",
						"command": fmt.Sprintf("%s capture", cortexPath),
					},
				},
			},
		},
	}

	// Configure status line
	statusLine := map[string]interface{}{
		"type":    "command",
		"command": fmt.Sprintf("%s status", cortexPath),
	}

	settings["hooks"] = hooks
	settings["statusLine"] = statusLine
	// Note: Preserves existing permissions and other settings

	// Write settings
	newData, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal settings: %w", err)
	}

	if err := os.WriteFile(settingsPath, newData, 0644); err != nil {
		return fmt.Errorf("failed to write settings: %w", err)
	}

	return nil
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

func handleInsights() {
	category := ""
	limit := 10

	if len(os.Args) >= 3 {
		category = os.Args[2]
	}
	if len(os.Args) >= 4 {
		fmt.Sscanf(os.Args[3], "%d", &limit)
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

	// Get insights
	var insights []*storage.Insight
	if category != "" {
		insights, err = store.GetInsightsByCategory(category, limit)
	} else {
		insights, err = store.GetRecentInsights(limit)
	}

	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to get insights: %v\n", err)
		os.Exit(1)
	}

	// Display results
	if len(insights) == 0 {
		fmt.Println("No insights found")
		return
	}

	if category != "" {
		fmt.Printf("📊 %s Insights:\n\n", category)
	} else {
		fmt.Printf("💡 Recent Insights:\n\n")
	}

	for i, insight := range insights {
		importance := ""
		for j := 0; j < insight.Importance && j < 5; j++ {
			importance += "⭐"
		}

		fmt.Printf("%d. [%s] %s %s\n", i+1, insight.Category, insight.Summary, importance)
		if len(insight.Tags) > 0 {
			fmt.Printf("   Tags: %v\n", insight.Tags)
		}
		fmt.Printf("   %s\n", insight.CreatedAt.Format("2006-01-02 15:04"))
		fmt.Println()
	}
}

func handleEntities() {
	entityType := ""
	if len(os.Args) >= 3 {
		entityType = os.Args[2]
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

	// Get entities
	var entities []*storage.Entity
	if entityType != "" {
		entities, err = store.GetEntitiesByType(entityType)
	} else {
		// Get all entity types
		types := []string{"decision", "pattern", "insight", "strategy"}
		for _, t := range types {
			typeEntities, _ := store.GetEntitiesByType(t)
			entities = append(entities, typeEntities...)
		}
	}

	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to get entities: %v\n", err)
		os.Exit(1)
	}

	// Display results
	if len(entities) == 0 {
		fmt.Println("No entities found")
		return
	}

	fmt.Printf("🔍 Entities:\n\n")
	for i, entity := range entities {
		fmt.Printf("%d. [%s] %s\n", i+1, entity.Type, entity.Name)
		fmt.Printf("   First seen: %s, Last seen: %s\n",
			entity.FirstSeen.Format("2006-01-02"),
			entity.LastSeen.Format("2006-01-02"))
		fmt.Println()
	}
}

func handleGraph() {
	if len(os.Args) < 3 {
		fmt.Fprintf(os.Stderr, "Usage: cortex graph <entity_type> <entity_name>\n")
		os.Exit(1)
	}

	entityType := os.Args[2]
	entityName := ""
	if len(os.Args) >= 4 {
		entityName = os.Args[3]
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

	// Get entity
	entity, err := store.GetEntity(entityType, entityName)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Entity not found: %v\n", err)
		os.Exit(1)
	}

	// Get relationships
	relationships, err := store.GetRelationships(entity.ID)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to get relationships: %v\n", err)
		os.Exit(1)
	}

	// Display entity and relationships
	fmt.Printf("🌐 Knowledge Graph for: %s (%s)\n\n", entity.Name, entity.Type)
	fmt.Printf("First seen: %s\n", entity.FirstSeen.Format("2006-01-02"))
	fmt.Printf("Last seen: %s\n\n", entity.LastSeen.Format("2006-01-02"))

	if len(relationships) == 0 {
		fmt.Println("No relationships found")
		return
	}

	fmt.Printf("Relationships (%d):\n\n", len(relationships))
	for i, rel := range relationships {
		if rel.FromEntity != nil && rel.ToEntity != nil {
			fmt.Printf("%d. %s -[%s]-> %s\n",
				i+1,
				rel.FromEntity.Name,
				rel.RelationType,
				rel.ToEntity.Name)
		}
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
  process     Process queue manually (one-time)

  search      Search captured context
  recent      Show recent events
  insights    Show insights [category] [limit]
  entities    Show entities [type]
  graph       Show knowledge graph for entity
  stats       Show statistics
  status      Show status (for status line)

  version     Show version
  help        Show this help

Examples:
  # Initialize in project
  cortex init

  # Capture from AI tool (in hook)
  echo '{"tool_name":"Edit",...}' | cortex capture

  # Search context
  cortex search "authentication decisions"

  # View insights
  cortex insights decision
  cortex insights

  # Browse entities
  cortex entities pattern
  cortex graph decision "JWT authentication"

For more information: https://github.com/dereksantos/cortex
`, version)
}
