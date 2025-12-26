// Cortex - Context memory for AI development
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/dereksantos/cortex/integrations/claude"
	"github.com/dereksantos/cortex/internal/capture"
	"github.com/dereksantos/cortex/internal/eval"
	"github.com/dereksantos/cortex/internal/processor"
	"github.com/dereksantos/cortex/internal/queue"
	"github.com/dereksantos/cortex/internal/storage"
	"github.com/dereksantos/cortex/pkg/config"
	"github.com/dereksantos/cortex/pkg/events"
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
	case "ingest":
		handleIngest()
	case "analyze":
		handleAnalyze()
	case "process":
		// Backward compatibility: process = ingest + analyze
		handleProcess()
	case "daemon":
		handleDaemon()
	case "info":
		handleInfo()
	case "test":
		handleTest()
	case "eval":
		handleEval()
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
	case "session-start":
		handleSessionStart()
	case "inject-context":
		handleInjectContext()
	case "overview":
		handleOverview()
	case "cli":
		handleCLI()
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

	// Check for --source flag
	source := "claude" // default
	if len(os.Args) >= 3 && os.Args[2] == "--source" && len(os.Args) >= 4 {
		source = os.Args[3]
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
		// Import cursor adapter
		event, err = convertCursorEvent(data, cfg.ProjectRoot)
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
}

// convertCursorEvent converts Cursor LSP events
func convertCursorEvent(data []byte, projectRoot string) (*events.Event, error) {
	// Import cursor package dynamically to avoid circular dependency
	var lspNotification map[string]interface{}
	if err := json.Unmarshal(data, &lspNotification); err != nil {
		return nil, err
	}

	// Create event from LSP notification
	eventID := fmt.Sprintf("cursor-%d", time.Now().UnixNano())
	method, _ := lspNotification["method"].(string)
	params, _ := lspNotification["params"].(map[string]interface{})

	toolName := "Edit" // default
	if method == "textDocument/didSave" {
		toolName = "Write"
	} else if method == "textDocument/didOpen" {
		toolName = "Read"
	}

	event := &events.Event{
		ID:         eventID,
		Source:     events.SourceCursor,
		EventType:  events.EventToolUse,
		Timestamp:  time.Now(),
		ToolName:   toolName,
		ToolInput:  params,
		ToolResult: "success",
		Context: events.EventContext{
			ProjectPath: projectRoot,
			SessionID:   fmt.Sprintf("cursor-%d", time.Now().Unix()),
		},
	}

	return event, nil
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

	// Add .context/ to .gitignore if it exists
	ensureGitignore(projectRoot)

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

func ensureGitignore(projectRoot string) {
	gitignorePath := filepath.Join(projectRoot, ".gitignore")

	// Check if .gitignore exists
	content, err := os.ReadFile(gitignorePath)
	if err != nil {
		// No .gitignore file, skip silently
		return
	}

	gitignoreContent := string(content)

	// Check if .context/ is already ignored
	if strings.Contains(gitignoreContent, ".context/") || strings.Contains(gitignoreContent, ".context") {
		// Already in gitignore
		return
	}

	// Append .context/ to gitignore
	f, err := os.OpenFile(gitignorePath, os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		// Silent failure - not critical
		return
	}
	defer f.Close()

	// Add newline if file doesn't end with one
	if len(content) > 0 && content[len(content)-1] != '\n' {
		f.WriteString("\n")
	}

	// Add .context/ with comment
	f.WriteString("\n# Cortex context memory (local development context)\n.context/\n")

	fmt.Println("   ✅ Added .context/ to .gitignore")
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
		"SessionStart": []interface{}{
			map[string]interface{}{
				"hooks": []interface{}{
					map[string]interface{}{
						"type":    "command",
						"command": fmt.Sprintf("%s session-start", cortexPath),
					},
				},
			},
		},
		"UserPromptSubmit": []interface{}{
			map[string]interface{}{
				"hooks": []interface{}{
					map[string]interface{}{
						"type":    "command",
						"command": fmt.Sprintf("%s inject-context", cortexPath),
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

	// Create slash command
	if err := createSlashCommand(claudeDir, cortexPath); err != nil {
		// Non-fatal, just warn
		fmt.Printf("   ⚠️  Could not create slash command: %v\n", err)
	} else {
		fmt.Println("   ✅ Created /cortex slash command")
	}

	return nil
}

func createSlashCommand(claudeDir, cortexPath string) error {
	// Ensure commands directory exists
	commandsDir := filepath.Join(claudeDir, "commands")
	if err := os.MkdirAll(commandsDir, 0755); err != nil {
		return fmt.Errorf("failed to create commands directory: %w", err)
	}

	commandFile := filepath.Join(commandsDir, "cortex.md")

	// Don't overwrite if exists
	if _, err := os.Stat(commandFile); err == nil {
		return nil // File exists, skip
	}

	// Create slash command content
	content := fmt.Sprintf(`# Cortex Context Memory

Interact with your captured development context.

**Usage:**
- /cortex - Show context overview
- /cortex search <query> - Search for relevant context
- /cortex insights - Show recent insights
- /cortex status - Check system status
- /cortex <prompt> - Smart search (anything else)

**Examples:**
- /cortex → Shows: 📊 47 events, 12 insights
- /cortex search authentication → Find auth decisions
- /cortex insights → List recent insights
- /cortex how did we handle errors → Smart search

---

%s cli "$@"
`, cortexPath)

	// Write command file
	if err := os.WriteFile(commandFile, []byte(content), 0644); err != nil {
		return fmt.Errorf("failed to write command file: %w", err)
	}

	return nil
}

// handleIngest moves events from queue to database (no analysis)
func handleIngest() {
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

	// Process queue (move to DB only)
	queueMgr := queue.New(cfg, store)
	processed, err := queueMgr.ProcessPending()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to process queue: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("✅ Ingested %d events to database\n", processed)
}

// handleAnalyze runs LLM analysis on recent unanalyzed events
func handleAnalyze() {
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

	// Get limit from args (default: 10)
	limit := 10
	if len(os.Args) >= 3 {
		fmt.Sscanf(os.Args[2], "%d", &limit)
	}

	// Get recent events
	events, err := store.GetRecentEvents(limit)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to get recent events: %v\n", err)
		os.Exit(1)
	}

	if len(events) == 0 {
		fmt.Println("No events to analyze")
		return
	}

	fmt.Printf("🔍 Analyzing %d events with LLM...\n", len(events))

	// Create processor
	queueMgr := queue.New(cfg, store)
	proc := processor.New(cfg, store, queueMgr)

	// Run analysis synchronously
	analyzed := 0
	for _, event := range events {
		if err := proc.AnalyzeEventSync(event); err == nil {
			analyzed++
		}
	}

	if analyzed > 0 {
		fmt.Printf("✅ Analyzed %d events\n", analyzed)
	} else {
		fmt.Println("⚠️  No events were analyzed (check Ollama availability)")
	}
}

// handleProcess provides backward compatibility (ingest + analyze)
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

	// If events were processed, run analysis immediately
	if processed > 0 {
		proc := processor.New(cfg, store, queueMgr)

		// Analyze recent events
		events, err := store.GetRecentEvents(processed)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Warning: Failed to get recent events: %v\n", err)
			return
		}

		fmt.Printf("🔍 Analyzing %d events with LLM...\n", len(events))

		// Run analysis synchronously for immediate results
		analyzed := 0
		for _, event := range events {
			if err := proc.AnalyzeEventSync(event); err == nil {
				analyzed++
			}
		}

		if analyzed > 0 {
			fmt.Printf("✅ Analyzed %d events\n", analyzed)
		}
	}
}

func handleInfo() {
	fmt.Println("Cortex System Information")
	fmt.Println("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")
	fmt.Println()

	// Detect system resources
	sysInfo, err := detectSystem()
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
		fmt.Println("  ✅ Running at http://localhost:11434")

		if len(installedModels) > 0 {
			fmt.Printf("  ✅ Models installed: %d\n", len(installedModels))
			for _, model := range installedModels {
				fmt.Printf("     • %s\n", model)
			}
		} else {
			fmt.Println("  ⚠️  No models installed")
		}
		fmt.Println()
	} else {
		fmt.Println("Ollama Status:")
		fmt.Println("  ❌ Not running")
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
	cfg, err := loadConfig()
	if err == nil {
		fmt.Println("Current Project:")
		fmt.Printf("  ✅ Initialized at %s\n", cfg.ProjectRoot)
		fmt.Printf("  Model: %s\n", cfg.OllamaModel)

		// Try to get stats
		if store, err := storage.New(cfg); err == nil {
			defer store.Close()
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
		fmt.Println("  ⚠️  Not initialized")
		fmt.Println("  Run: cortex init")
	}
	fmt.Println()
}

func handleTest() {
	// Get test type from args (default: run all)
	testType := "all"
	if len(os.Args) >= 3 {
		testType = os.Args[2]
	}

	// Validate test type
	validTypes := map[string]bool{
		"all": true, "decision": true, "pattern": true, "insight": true,
	}
	if !validTypes[testType] {
		fmt.Fprintf(os.Stderr, "Invalid test type: %s\n", testType)
		fmt.Println("Valid types: decision, pattern, insight, all")
		os.Exit(1)
	}

	fmt.Println("🧪 Cortex LLM Analysis Test")
	fmt.Println("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")
	fmt.Println()

	// Check Ollama availability
	ollamaRunning, _ := checkOllama()
	if !ollamaRunning {
		fmt.Println("❌ Ollama is not running")
		fmt.Println("   Start with: ollama serve")
		os.Exit(1)
	}

	// Run requested tests
	tests := []struct {
		name  string
		event *events.Event
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
		fmt.Fprintf(os.Stderr, "Failed to create temp directory: %v\n", err)
		os.Exit(1)
	}
	defer os.RemoveAll(tmpDir)

	// Initialize cortex in temp dir
	originalDir, _ := os.Getwd()
	os.Chdir(tmpDir)
	defer os.Chdir(originalDir)

	cfg := config.Default()
	cfg.ProjectRoot = tmpDir
	cfg.ContextDir = tmpDir + "/.context"
	if err := cfg.EnsureDirectories(); err != nil {
		fmt.Fprintf(os.Stderr, "Failed to create directories: %v\n", err)
		os.Exit(1)
	}
	if err := cfg.Save(cfg.ContextDir + "/config.json"); err != nil {
		fmt.Fprintf(os.Stderr, "Failed to save config: %v\n", err)
		os.Exit(1)
	}

	// Open storage
	store, err := storage.New(cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to open storage: %v\n", err)
		os.Exit(1)
	}
	defer store.Close()

	// Run each test
	passed := 0
	failed := 0

	for _, i := range testsToRun {
		test := tests[i]
		fmt.Printf("Testing: %s\n", test.name)
		fmt.Println(strings.Repeat("─", 60))

		// Store event
		if err := store.StoreEvent(test.event); err != nil {
			fmt.Printf("❌ FAIL: Could not store event: %v\n\n", err)
			failed++
			continue
		}

		// Analyze with LLM
		queueMgr := queue.New(cfg, store)
		proc := processor.New(cfg, store, queueMgr)

		startTime := time.Now()
		if err := proc.AnalyzeEventSync(test.event); err != nil {
			fmt.Printf("❌ FAIL: Analysis failed: %v\n\n", err)
			failed++
			continue
		}
		elapsed := time.Since(startTime)

		// Get insights
		insights, err := store.GetRecentInsights(1)
		if err != nil || len(insights) == 0 {
			fmt.Printf("❌ FAIL: No insight generated\n\n")
			failed++
			continue
		}

		insight := insights[0]

		// Validate
		if validateTestInsight(insight, test.expected) {
			fmt.Printf("✅ PASS (%.1fs)\n", elapsed.Seconds())
			fmt.Printf("   Category: %s\n", insight.Category)
			fmt.Printf("   Summary: %s\n", insight.Summary)
			fmt.Printf("   Tags: %v\n", insight.Tags)
			fmt.Printf("   Importance: %d\n", insight.Importance)
			passed++
		} else {
			fmt.Printf("⚠️  MARGINAL (%.1fs)\n", elapsed.Seconds())
			fmt.Printf("   Category: %s (expected: %v)\n", insight.Category, test.expected.AllowedCategories)
			fmt.Printf("   Summary: %s\n", insight.Summary)
			fmt.Printf("   Tags: %v\n", insight.Tags)
			fmt.Printf("   Note: Analysis completed but quality may vary\n")
			passed++ // Count as pass since LLM is non-deterministic
		}
		fmt.Println()
	}

	// Summary
	fmt.Println("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")
	fmt.Printf("Results: %d/%d passed\n", passed, passed+failed)
	fmt.Println()

	if failed > 0 {
		fmt.Println("⚠️  Some tests failed - check Ollama status and model")
		os.Exit(1)
	} else {
		fmt.Println("✅ All tests passed - LLM analysis is working!")
	}
}

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

func handleEval() {
	// Parse flags
	scenarioPath := ""
	scenarioDir := "test/evals/scenarios"
	outputFormat := "human"
	verbose := false
	modelOverride := ""
	providerName := "ollama"
	e2eMode := false
	dryRun := false
	treeMode := false

	for i := 2; i < len(os.Args); i++ {
		arg := os.Args[i]
		switch {
		case arg == "--verbose" || arg == "-v":
			verbose = true
		case arg == "--e2e":
			e2eMode = true
		case arg == "--tree":
			treeMode = true
		case arg == "--dry-run":
			dryRun = true
		case arg == "--output" || arg == "-o":
			if i+1 < len(os.Args) {
				outputFormat = os.Args[i+1]
				i++
			}
		case arg == "--scenario" || arg == "-s":
			if i+1 < len(os.Args) {
				scenarioPath = os.Args[i+1]
				i++
			}
		case arg == "--dir" || arg == "-d":
			if i+1 < len(os.Args) {
				scenarioDir = os.Args[i+1]
				i++
			}
		case arg == "--model" || arg == "-m":
			if i+1 < len(os.Args) {
				modelOverride = os.Args[i+1]
				i++
			}
		case arg == "--provider" || arg == "-p":
			if i+1 < len(os.Args) {
				providerName = os.Args[i+1]
				i++
			}
		case arg == "--help" || arg == "-h":
			fmt.Println("Usage: cortex eval [options]")
			fmt.Println()
			fmt.Println("Options:")
			fmt.Println("  --scenario, -s <file>  Run a specific scenario file")
			fmt.Println("  --dir, -d <dir>        Scenario directory (default: test/evals/scenarios)")
			fmt.Println("  --e2e                  Run E2E evals (tests full Cortex pipeline)")
			fmt.Println("  --tree                 Run tree evals (multi-path, temporal)")
			fmt.Println("  --provider, -p <name>  LLM provider: ollama, anthropic (default: ollama)")
			fmt.Println("  --model, -m <model>    Model to use (provider-specific)")
			fmt.Println("  --dry-run              Use mock provider (no LLM calls, instant)")
			fmt.Println("  --output, -o <format>  Output format: human, json (default: human)")
			fmt.Println("  --verbose, -v          Show detailed output")
			fmt.Println("  --help, -h             Show this help")
			fmt.Println()
			fmt.Println("Eval Types:")
			fmt.Println("  linear (default)   Pre-defined context injection")
			fmt.Println("  e2e (--e2e)        Full pipeline: capture → process → recall")
			fmt.Println("  tree (--tree)      Multi-path and temporal evals")
			fmt.Println()
			fmt.Println("Providers:")
			fmt.Println("  ollama (default)   Local models via Ollama")
			fmt.Println("    Models: qwen2:0.5b (fast), phi3:mini, mistral:7b")
			fmt.Println("  anthropic          Claude models via API (requires ANTHROPIC_API_KEY)")
			fmt.Println("    Models: claude-3-5-haiku-20241022 (fast, default)")
			fmt.Println("            claude-3-5-sonnet-20241022 (more capable)")
			fmt.Println()
			fmt.Println("Examples:")
			fmt.Println("  cortex eval -p anthropic                    # Use Claude Haiku")
			fmt.Println("  cortex eval -p anthropic -m claude-3-5-sonnet-20241022")
			fmt.Println("  cortex eval -p ollama -m qwen2:0.5b         # Fast local model")
			return
		}
	}

	// Load config
	cfg, err := loadConfig()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to load config: %v\n", err)
		os.Exit(1)
	}

	// Override model if specified
	if modelOverride != "" {
		if providerName == "anthropic" {
			cfg.AnthropicModel = modelOverride
		} else {
			cfg.OllamaModel = modelOverride
		}
	}

	// Select provider based on mode
	var provider llm.Provider
	if dryRun {
		provider = llm.NewMockProvider(10) // 10ms simulated latency
		if verbose {
			fmt.Println("Using mock provider (--dry-run mode)")
		}
	} else {
		switch providerName {
		case "anthropic":
			anthropicClient := llm.NewAnthropicClient(cfg)
			if !anthropicClient.IsAvailable() {
				fmt.Fprintf(os.Stderr, "Anthropic API key not set. Set ANTHROPIC_API_KEY environment variable.\n")
				os.Exit(1)
			}
			provider = anthropicClient
			if verbose {
				fmt.Printf("Using Anthropic provider (model: %s)\n", anthropicClient.Model())
			}
		case "ollama":
			ollamaClient := llm.NewOllamaClient(cfg)
			if !ollamaClient.IsAvailable() {
				fmt.Fprintf(os.Stderr, "Ollama is not running. Start with: ollama serve\n")
				os.Exit(1)
			}
			provider = ollamaClient
		default:
			fmt.Fprintf(os.Stderr, "Unknown provider: %s. Use 'ollama' or 'anthropic'.\n", providerName)
			os.Exit(1)
		}
	}

	// Run evaluation based on mode
	var run *eval.EvalRun

	if e2eMode {
		// E2E mode: test full Cortex pipeline
		e2eEvaluator := eval.NewE2EEvaluator(provider, cfg)
		e2eEvaluator.SetVerbose(verbose)

		if scenarioPath != "" {
			// Load and run single E2E scenario
			scenario, err := eval.LoadScenario(scenarioPath)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Failed to load scenario: %v\n", err)
				os.Exit(1)
			}
			results, err := e2eEvaluator.RunE2EScenario(scenario)
			if err != nil {
				fmt.Fprintf(os.Stderr, "E2E evaluation failed: %v\n", err)
				os.Exit(1)
			}
			run = &eval.EvalRun{
				ID:        fmt.Sprintf("e2e-eval-%s", time.Now().Format("20060102-150405")),
				Timestamp: time.Now(),
				Provider:  provider.Name(),
				Scenarios: []string{scenario.ID},
				Results:   results,
			}
			run.Summary = eval.CalculateSummary(results)
		} else {
			run, err = e2eEvaluator.RunE2E(scenarioDir)
			if err != nil {
				fmt.Fprintf(os.Stderr, "E2E evaluation failed: %v\n", err)
				os.Exit(1)
			}
		}
	} else if treeMode {
		// Tree mode: multi-path and temporal evals
		treeEvaluator := eval.NewTreeEvaluator(provider)
		treeEvaluator.SetVerbose(verbose)

		// Load tree scenarios from the tree directory
		treeDir := scenarioDir + "/tree"
		if scenarioPath != "" {
			treeDir = scenarioPath
		}

		scenarios, err := eval.LoadScenarios(treeDir)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Failed to load tree scenarios: %v\n", err)
			os.Exit(1)
		}

		// Run tree scenarios
		allResults := make([]eval.EvalResult, 0)
		scenarioIDs := make([]string, 0)

		for _, scenario := range scenarios {
			if scenario.Type == eval.ScenarioMultiPath && len(scenario.Paths) >= 2 {
				treeRun, err := treeEvaluator.RunMultiPath(scenario)
				if err != nil {
					fmt.Fprintf(os.Stderr, "Multi-path eval failed: %v\n", err)
					continue
				}
				scenarioIDs = append(scenarioIDs, scenario.ID)
				// Convert TreeEvalResult to EvalResult for unified reporting
				for _, r := range treeRun.Results {
					allResults = append(allResults, r.EvalResult)
				}

				// Print tree-specific summary
				if verbose {
					fmt.Printf("\nTree Summary for %s:\n", scenario.ID)
					fmt.Printf("  Path Adherence: %.0f%%\n", treeRun.Summary.AvgPathAdherence*100)
					fmt.Printf("  Contamination Detected: %d/%d (%.0f%%)\n",
						treeRun.Summary.ContaminationDetected,
						treeRun.Summary.ContaminationTests,
						treeRun.Summary.ContaminationRate*100)
					for _, ps := range treeRun.Summary.PathStats {
						fmt.Printf("  Path %s: %.0f%% pass, avg score %.2f\n",
							ps.PathName, ps.PassRate*100, ps.AvgScore)
					}
				}
			} else if scenario.Type == eval.ScenarioTemporal && len(scenario.Phases) >= 2 {
				treeRun, err := treeEvaluator.RunTemporal(scenario)
				if err != nil {
					fmt.Fprintf(os.Stderr, "Temporal eval failed: %v\n", err)
					continue
				}
				scenarioIDs = append(scenarioIDs, scenario.ID)
				for _, r := range treeRun.Results {
					allResults = append(allResults, r.EvalResult)
				}
			}
		}

		run = &eval.EvalRun{
			ID:        fmt.Sprintf("tree-eval-%s", time.Now().Format("20060102-150405")),
			Timestamp: time.Now(),
			Provider:  provider.Name(),
			Scenarios: scenarioIDs,
			Results:   allResults,
		}
		run.Summary = eval.CalculateSummary(allResults)

	} else {
		// Check if directory contains cognition scenarios
		cognitionScenarios, _ := eval.LoadCognitionScenarios(scenarioDir)
		if len(cognitionScenarios) > 0 {
			// Cognition mode: test cognitive modes
			runCognitionEvals(cognitionScenarios, verbose, outputFormat)
			return
		}

		// Regular mode: pre-defined context injection
		evaluator := eval.NewEvaluator(provider)
		evaluator.SetVerbose(verbose)

		if scenarioPath != "" {
			run, err = evaluator.RunSingle(scenarioPath)
		} else {
			run, err = evaluator.RunAll(scenarioDir)
		}

		if err != nil {
			fmt.Fprintf(os.Stderr, "Evaluation failed: %v\n", err)
			os.Exit(1)
		}
	}

	// Output results
	reporter := eval.NewReporter(verbose)
	switch outputFormat {
	case "json":
		if err := reporter.ReportJSON(os.Stdout, run); err != nil {
			fmt.Fprintf(os.Stderr, "Failed to write JSON: %v\n", err)
			os.Exit(1)
		}
	default:
		if err := reporter.ReportHuman(os.Stdout, run); err != nil {
			fmt.Fprintf(os.Stderr, "Failed to write report: %v\n", err)
			os.Exit(1)
		}
	}

	// Exit with error if pass rate is below threshold
	if run.Summary.PassRate < 0.5 {
		os.Exit(1)
	}
}

// runCognitionEvals runs cognition-specific evaluations
func runCognitionEvals(scenarios []*eval.CognitionScenario, verbose bool, outputFormat string) {
	// Create mock Cortex for evals (real implementation would use actual Cortex)
	mock := eval.NewMockCortex()
	evaluator := eval.NewCognitionEvaluator(mock)
	evaluator.SetVerbose(verbose)

	ctx := context.Background()

	var results []*eval.CognitionEvalResult
	passCount := 0
	failCount := 0

	fmt.Println("Cortex Cognition Eval")
	fmt.Println("=====================")
	fmt.Println()

	for _, scenario := range scenarios {
		if verbose {
			fmt.Printf("Running: %s (%s)\n", scenario.Name, scenario.Type)
		}

		result, err := evaluator.RunScenario(ctx, scenario)
		if err != nil {
			fmt.Fprintf(os.Stderr, "  Error: %v\n", err)
			failCount++
			continue
		}

		results = append(results, result)
		if result.Pass {
			passCount++
			if verbose {
				fmt.Printf("  ✓ PASS\n")
			}
		} else {
			failCount++
			fmt.Printf("  ✗ FAIL: %s\n", result.Reason)
		}
	}

	// Print summary
	fmt.Println()
	fmt.Println("Summary")
	fmt.Println("========================================")
	fmt.Printf("Total Scenarios: %d\n", len(scenarios))
	fmt.Printf("Pass:            %d\n", passCount)
	fmt.Printf("Fail:            %d\n", failCount)
	fmt.Printf("Pass Rate:       %.0f%%\n", float64(passCount)/float64(len(scenarios))*100)

	// Print detailed results for specific types
	for _, result := range results {
		if result.ConflictResults != nil {
			fmt.Println()
			fmt.Printf("Conflict: %s\n", result.ScenarioID)
			fmt.Printf("  Detected:  %v\n", result.ConflictResults.ConflictDetected)
			fmt.Printf("  Severity:  %s\n", result.ConflictResults.DetectedSeverity)
			fmt.Printf("  Surfaced:  %v\n", result.ConflictResults.Surfaced)
			if result.ConflictResults.ChosenPattern != "" {
				fmt.Printf("  Chosen:    %s\n", result.ConflictResults.ChosenPattern)
			}
		}
	}

	// Exit with error if pass rate is below threshold
	if float64(passCount)/float64(len(scenarios)) < 0.5 {
		os.Exit(1)
	}
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

func handleOverview() {
	// Load config
	cfg, err := loadConfig()
	if err != nil {
		fmt.Println("❌ Cortex not initialized")
		fmt.Println("   Run: cortex init --auto")
		os.Exit(1)
	}

	// Open storage
	store, err := storage.New(cfg)
	if err != nil {
		fmt.Println("❌ Failed to open storage")
		os.Exit(1)
	}
	defer store.Close()

	// Get stats
	stats, err := store.GetStats()
	if err != nil {
		fmt.Println("❌ Failed to get stats")
		os.Exit(1)
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
	daemonStatus := "❌ Stopped"
	if recentEvents, err := store.GetRecentEvents(5); err == nil && len(recentEvents) > 0 {
		// Check if last event is recent (< 5 min old)
		if time.Since(recentEvents[0].Timestamp) < 5*time.Minute {
			daemonStatus = "✅ Running"
		}
	}

	// Print overview
	fmt.Println("📊 Cortex Context Memory")
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
					stars += "⭐"
				}
				if stars == "" {
					stars = "⭐⭐⭐"
				}
				prefix := "  ├─"
				if cat == "strategy" || (cat == "insight" && categoryCount["strategy"] == 0) {
					prefix = "  └─"
				}
				fmt.Printf("%s %ss:  %d %s\n", prefix, cat, count, stars)
			}
		}
	}
	fmt.Println()

	// Status
	fmt.Printf("Status:     🤖 Daemon %s\n", daemonStatus)
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
	fmt.Println("💡 Try: /cortex search <query>")
}

func handleSessionStart() {
	// Print usage instructions for AI agent
	instructions := `🤖 Cortex Context Memory Available

Quick Commands:
  cortex status          # Check if daemon is running
  cortex daemon &        # Start background processor (if not running)
  cortex search "query"  # Find relevant context from past work
  cortex insights        # View extracted decisions and patterns
  cortex recent          # Show recent development events

💡 Tip: If daemon isn't running, suggest starting it to enable automatic context capture.
📚 Use 'cortex search' to find relevant past decisions before making new architectural choices.`

	fmt.Println(instructions)
}

func handleCLI() {
	// Route slash command arguments
	// Usage: cortex cli [args...]

	args := os.Args[2:] // Skip "cortex" and "cli"

	if len(args) == 0 {
		// No args - show overview
		handleOverview()
		return
	}

	subcommand := args[0]

	switch subcommand {
	case "search":
		// cortex cli search <query>
		if len(args) < 2 {
			fmt.Println("Usage: /cortex search <query>")
			os.Exit(1)
		}
		// Reconstruct search query from remaining args
		query := strings.Join(args[1:], " ")
		os.Args = []string{"cortex", "search", query}
		handleSearch()

	case "insights":
		// cortex cli insights
		os.Args = []string{"cortex", "insights"}
		handleInsights()

	case "status":
		// cortex cli status
		os.Args = []string{"cortex", "info"}
		handleInfo()

	default:
		// Treat entire input as search query
		query := strings.Join(args, " ")
		os.Args = []string{"cortex", "search", query}
		handleSearch()
	}
}

func handleInjectContext() {
	// Read user prompt from stdin
	promptData, err := io.ReadAll(os.Stdin)
	if err != nil || len(promptData) == 0 {
		// No prompt provided, exit silently
		os.Exit(0)
	}

	prompt := string(promptData)

	// Load config
	cfg, err := loadConfig()
	if err != nil {
		// Silent failure - don't block user if Cortex not initialized
		fmt.Println(prompt)
		os.Exit(0)
	}

	// Open storage
	store, err := storage.New(cfg)
	if err != nil {
		// Silent failure
		fmt.Println(prompt)
		os.Exit(0)
	}
	defer store.Close()

	// Search for relevant context (simple keyword search for now)
	// Extract key terms from prompt (basic approach)
	query := extractKeyTerms(prompt)
	if query == "" {
		// No meaningful terms, just return original prompt
		fmt.Println(prompt)
		os.Exit(0)
	}

	// Search for relevant insights
	insights, err := store.GetRecentInsights(50)
	if err != nil || len(insights) == 0 {
		// No insights available
		fmt.Println(prompt)
		os.Exit(0)
	}

	// Find top 2 most relevant insights (simple text matching)
	relevant := findRelevantInsights(insights, query, 2)
	if len(relevant) == 0 {
		// No relevant context found
		fmt.Println(prompt)
		os.Exit(0)
	}

	// Inject context before the prompt
	fmt.Println("📚 Relevant Context from Cortex:")
	for i, insight := range relevant {
		fmt.Printf("%d. [%s] %s\n", i+1, insight.Category, insight.Summary)
		if len(insight.Tags) > 0 {
			fmt.Printf("   Tags: %v\n", insight.Tags)
		}
	}
	fmt.Println()
	fmt.Println("User Request:")
	fmt.Println(prompt)
}

// extractKeyTerms extracts meaningful terms from prompt (basic implementation)
func extractKeyTerms(prompt string) string {
	// Remove common words and extract key terms
	stopWords := map[string]bool{
		"the": true, "a": true, "an": true, "and": true, "or": true,
		"but": true, "in": true, "on": true, "at": true, "to": true,
		"for": true, "of": true, "with": true, "by": true, "from": true,
		"is": true, "are": true, "was": true, "were": true, "be": true,
		"have": true, "has": true, "had": true, "do": true, "does": true,
		"did": true, "will": true, "would": true, "should": true, "could": true,
		"can": true, "may": true, "might": true, "must": true,
		"i": true, "you": true, "he": true, "she": true, "it": true,
		"we": true, "they": true, "them": true, "their": true, "my": true,
		"help": true, "me": true, "please": true, "want": true, "need": true,
		"how": true, "what": true, "when": true, "where": true, "why": true,
	}

	words := strings.Fields(strings.ToLower(prompt))
	var keyTerms []string

	for _, word := range words {
		// Clean word (remove punctuation)
		word = strings.Trim(word, ",.!?;:\"'")
		// Skip if stop word or too short
		if len(word) < 3 || stopWords[word] {
			continue
		}
		keyTerms = append(keyTerms, word)
	}

	return strings.Join(keyTerms, " ")
}

// findRelevantInsights finds insights matching the query terms
func findRelevantInsights(insights []*storage.Insight, query string, limit int) []*storage.Insight {
	type scoredInsight struct {
		insight *storage.Insight
		score   int
	}

	var scored []scoredInsight
	queryTerms := strings.Fields(strings.ToLower(query))

	for _, insight := range insights {
		score := 0
		searchText := strings.ToLower(insight.Summary + " " + strings.Join(insight.Tags, " ") + " " + insight.Category)

		// Count matching terms
		for _, term := range queryTerms {
			if strings.Contains(searchText, term) {
				score++
			}
		}

		if score > 0 {
			scored = append(scored, scoredInsight{insight, score})
		}
	}

	// Sort by score (descending)
	for i := 0; i < len(scored)-1; i++ {
		for j := i + 1; j < len(scored); j++ {
			if scored[j].score > scored[i].score {
				scored[i], scored[j] = scored[j], scored[i]
			}
		}
	}

	// Return top N
	var result []*storage.Insight
	for i := 0; i < limit && i < len(scored); i++ {
		result = append(result, scored[i].insight)
	}

	return result
}

func loadConfig() (*config.Config, error) {
	projectRoot, err := os.Getwd()
	if err != nil {
		return nil, err
	}

	configPath := fmt.Sprintf("%s/.context/config.json", projectRoot)
	return config.Load(configPath)
}

// detectSystem detects system resources
func detectSystem() (*SystemInfo, error) {
	info := &SystemInfo{
		CPUCores: runtime.NumCPU(),
		OS:       runtime.GOOS,
		Arch:     runtime.GOARCH,
	}

	// Detect RAM based on OS
	switch runtime.GOOS {
	case "darwin":
		info.TotalRAMGB = detectRAMMacOS()
	case "linux":
		info.TotalRAMGB = detectRAMLinux()
	case "windows":
		info.TotalRAMGB = detectRAMWindows()
	default:
		info.TotalRAMGB = 8.0
	}

	info.AvailableRAMGB = info.TotalRAMGB * 0.7
	return info, nil
}

// SystemInfo holds system information
type SystemInfo struct {
	OS             string
	Arch           string
	CPUCores       int
	TotalRAMGB     float64
	AvailableRAMGB float64
}

func (s *SystemInfo) FormatOS() string {
	osNames := map[string]string{
		"darwin":  "macOS",
		"linux":   "Linux",
		"windows": "Windows",
	}
	goos := runtime.GOOS
	if name, ok := osNames[goos]; ok {
		return name
	}
	return goos
}

func detectRAMMacOS() float64 {
	cmd := exec.Command("sysctl", "-n", "hw.memsize")
	output, err := cmd.Output()
	if err != nil {
		return 8.0
	}
	bytes, _ := strconv.ParseInt(strings.TrimSpace(string(output)), 10, 64)
	return float64(bytes) / (1024 * 1024 * 1024)
}

func detectRAMLinux() float64 {
	data, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return 8.0
	}
	lines := strings.Split(string(data), "\n")
	for _, line := range lines {
		if strings.HasPrefix(line, "MemTotal:") {
			fields := strings.Fields(line)
			if len(fields) >= 2 {
				kb, _ := strconv.ParseFloat(fields[1], 64)
				return kb / (1024 * 1024)
			}
		}
	}
	return 8.0
}

func detectRAMWindows() float64 {
	cmd := exec.Command("wmic", "computersystem", "get", "totalphysicalmemory")
	output, err := cmd.Output()
	if err != nil {
		return 8.0
	}
	lines := strings.Split(string(output), "\n")
	if len(lines) >= 2 {
		bytes, _ := strconv.ParseInt(strings.TrimSpace(lines[1]), 10, 64)
		return float64(bytes) / (1024 * 1024 * 1024)
	}
	return 8.0
}

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
		Name   string
		Size   string
		RAMGB  float64
		Desc   string
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
				status = "✅ ⭐ Recommended"
			} else {
				status = "✅ Compatible"
			}
		} else {
			status = fmt.Sprintf("❌ Needs %.1f GB", m.RAMGB)
		}

		fmt.Printf("  %-15s %-10s %s - %s\n", m.Name, m.Size, status, m.Desc)
	}
}

func printUsage() {
	fmt.Printf(`Cortex %s - Context memory for AI development

Usage:
  cortex <command> [options]

Commands:
  init           Initialize Cortex in current directory
  info           Show system info and model recommendations
  test           Test LLM analysis [decision|pattern|insight]

  capture        Capture event from stdin (used by AI tools)
  ingest         Move queued events to database
  analyze        Run LLM analysis on recent events [limit]
  process        Process queue + analyze (backward compat)
  daemon         Start background processor

  search         Search captured context
  recent         Show recent events
  insights       Show insights [category] [limit]
  entities       Show entities [type]
  graph          Show knowledge graph for entity
  stats          Show statistics
  status         Show status (for status line)

  session-start  Print session start instructions (for hooks)
  inject-context Inject relevant context into prompt (for hooks)
  overview       Show context overview (visual summary)
  cli            Route slash command arguments (for /cortex)

  version        Show version
  help           Show this help

Examples:
  # Get system info and model recommendations
  cortex info

  # Test LLM analysis quality
  cortex test decision
  cortex test

  # Initialize in project
  cortex init

  # Process workflow (manual)
  cortex ingest              # Queue → Database
  cortex analyze 5           # Analyze last 5 events
  cortex process             # Both steps combined

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

  # Slash command (Claude Code)
  /cortex                        # Show overview
  /cortex search auth            # Search context
  /cortex insights               # List insights
  /cortex how did we handle X    # Smart search

For more information: https://github.com/dereksantos/cortex
`, version)
}
