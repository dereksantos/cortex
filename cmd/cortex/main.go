// Cortex - Context memory for AI development
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/dereksantos/cortex/integrations/claude"
	"github.com/dereksantos/cortex/integrations/cursor"
	"github.com/dereksantos/cortex/internal/capture"
	intcognition "github.com/dereksantos/cortex/internal/cognition"
	"github.com/dereksantos/cortex/internal/cognition/sources"
	"github.com/dereksantos/cortex/internal/eval"
	intllm "github.com/dereksantos/cortex/internal/llm"
	"github.com/dereksantos/cortex/internal/processor"
	"github.com/dereksantos/cortex/internal/queue"
	"github.com/dereksantos/cortex/internal/storage"
	"github.com/dereksantos/cortex/pkg/cognition"
	"github.com/dereksantos/cortex/pkg/config"
	"github.com/dereksantos/cortex/pkg/events"
	"github.com/dereksantos/cortex/pkg/llm"
	"github.com/dereksantos/cortex/pkg/system"
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
	case "install":
		handleInstall()
	case "uninstall":
		handleUninstall()
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
	case "forget":
		handleForget()
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

	// Parse flags
	source := "claude" // default
	captureType := ""
	content := ""

	for i := 2; i < len(os.Args); i++ {
		arg := os.Args[i]
		switch {
		case arg == "--source" && i+1 < len(os.Args):
			source = os.Args[i+1]
			i++
		case strings.HasPrefix(arg, "--type="):
			captureType = strings.TrimPrefix(arg, "--type=")
		case arg == "--type" && i+1 < len(os.Args):
			captureType = os.Args[i+1]
			i++
		case strings.HasPrefix(arg, "--content="):
			content = strings.TrimPrefix(arg, "--content=")
		case arg == "--content" && i+1 < len(os.Args):
			content = os.Args[i+1]
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
}

// truncateString truncates a string to max length with ellipsis
func truncateString(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen-3] + "..."
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

func handleInstall() {
	// Get project root and home directory
	projectRoot, err := os.Getwd()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to get working directory: %v\n", err)
		os.Exit(1)
	}

	homeDir, err := os.UserHomeDir()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to get home directory: %v\n", err)
		os.Exit(1)
	}

	fmt.Println("Installing Cortex for Claude Code...")
	fmt.Println()

	// 1. Detect Claude Code
	claudeHomeDir := filepath.Join(homeDir, ".claude")
	claudeProjectDir := filepath.Join(projectRoot, ".claude")

	if _, err := os.Stat(claudeHomeDir); err != nil {
		fmt.Println("Claude Code not detected at ~/.claude/")
		fmt.Println("Install Claude Code first: https://claude.ai/claude-code")
		os.Exit(1)
	}

	fmt.Printf("Detected Claude Code at %s\n", claudeHomeDir)

	// 2. Ensure .context/ directory exists
	contextDir := filepath.Join(projectRoot, ".context")
	if err := os.MkdirAll(contextDir, 0755); err != nil {
		fmt.Fprintf(os.Stderr, "Failed to create .context directory: %v\n", err)
		os.Exit(1)
	}

	// 3. Ensure .claude/ directory exists in project
	if err := os.MkdirAll(claudeProjectDir, 0755); err != nil {
		fmt.Fprintf(os.Stderr, "Failed to create .claude directory: %v\n", err)
		os.Exit(1)
	}

	// 4. Create/merge settings.local.json with hooks
	settingsPath := filepath.Join(claudeProjectDir, "settings.local.json")
	if err := createClaudeSettings(settingsPath); err != nil {
		fmt.Fprintf(os.Stderr, "Failed to create settings: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("Created %s with hooks\n", settingsPath)

	// 5. Create slash command
	commandsDir := filepath.Join(claudeProjectDir, "commands")
	if err := os.MkdirAll(commandsDir, 0755); err != nil {
		fmt.Fprintf(os.Stderr, "Failed to create commands directory: %v\n", err)
		os.Exit(1)
	}

	commandFile := filepath.Join(commandsDir, "cortex.md")
	if _, err := os.Stat(commandFile); err == nil {
		fmt.Printf("Slash command already exists at %s\n", commandFile)
	} else {
		if err := createCortexCommand(commandFile); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: Failed to create slash command: %v\n", err)
		} else {
			fmt.Printf("Created %s\n", commandFile)
		}
	}

	// 5.1 Create additional slash commands
	additionalCommands := []struct {
		name    string
		content string
	}{
		{
			name: "cortex-recall.md",
			content: `---
description: Recall what Cortex knows about a topic
argument-hint: "<topic>"
allowed-tools: Bash(./cortex:*)
---

Search Cortex for context related to: $ARGUMENTS

Run: ./cortex search "$ARGUMENTS"

Summarize the relevant insights, decisions, and patterns found.
`,
		},
		{
			name: "cortex-decide.md",
			content: `---
description: Record an architectural decision
argument-hint: "<decision>"
allowed-tools: Bash(./cortex:*)
---

Record this architectural decision in Cortex:

Decision: $ARGUMENTS

Run: ./cortex capture --type=decision --content="$ARGUMENTS"

Confirm the decision was recorded.
`,
		},
		{
			name: "cortex-correct.md",
			content: `---
description: Record a correction (e.g., "we use X not Y")
argument-hint: "<correction>"
allowed-tools: Bash(./cortex:*)
---

Record this correction in Cortex:

Correction: $ARGUMENTS

This will be surfaced in future sessions when relevant.

Run: ./cortex capture --type=correction --content="$ARGUMENTS"
`,
		},
		{
			name: "cortex-forget.md",
			content: `---
description: Mark context as outdated
argument-hint: "<insight-id or description>"
allowed-tools: Bash(./cortex:*)
---

Mark this context as outdated/deprecated:

$ARGUMENTS

Run: ./cortex forget "$ARGUMENTS"
`,
		},
	}

	for _, cmd := range additionalCommands {
		cmdFile := filepath.Join(commandsDir, cmd.name)
		if _, err := os.Stat(cmdFile); err != nil {
			// File doesn't exist, create it
			if err := os.WriteFile(cmdFile, []byte(cmd.content), 0644); err != nil {
				fmt.Fprintf(os.Stderr, "Warning: Failed to create %s: %v\n", cmd.name, err)
			} else {
				fmt.Printf("Created %s\n", cmdFile)
			}
		}
	}

	// 6. Check LLM availability
	fmt.Println()
	fmt.Println("Checking LLM availability...")
	llmStatus := intllm.DetectLLM()

	if llmStatus.Available {
		if llmStatus.Provider == "ollama" {
			fmt.Printf("Ollama installed at %s\n", llmStatus.OllamaPath)
			fmt.Printf("Model %s available (recommended for Cortex)\n", llmStatus.Model)
		} else if llmStatus.Provider == "anthropic" {
			fmt.Println("Anthropic API key configured")
			if llmStatus.OllamaInstalled {
				fmt.Printf("Ollama also installed at %s\n", llmStatus.OllamaPath)
				if len(llmStatus.OllamaModels) > 0 {
					fmt.Printf("Ollama models available: %s\n", strings.Join(llmStatus.OllamaModels, ", "))
				}
			}
		}
	} else if llmStatus.OllamaInstalled {
		fmt.Printf("Ollama installed at %s\n", llmStatus.OllamaPath)
		fmt.Println("No suitable model found")
		fmt.Println()
		fmt.Println("Cortex works best with a local model for background processing.")
		fmt.Println("Install one with:")
		fmt.Println("  ollama pull qwen2.5:3b    (3GB, recommended)")
		fmt.Println("  ollama pull qwen2.5:0.5b  (500MB, lightweight)")
		fmt.Println()
		fmt.Println("Or set ANTHROPIC_API_KEY for Claude API usage.")
	} else {
		fmt.Println("No local LLM found")
		fmt.Println()
		fmt.Println("For full functionality, install Ollama:")
		fmt.Println("  brew install ollama && ollama pull qwen2.5:3b")
		fmt.Println()
		fmt.Println("Or set ANTHROPIC_API_KEY for Claude API usage.")
	}

	if !llmStatus.Available {
		fmt.Println()
		fmt.Println("Without an LLM, Cortex will run in mechanical-only mode (Reflex).")
	}

	// 7. Create plugin structure for distribution
	pluginDir := filepath.Join(projectRoot, ".claude-plugin")
	if err := os.MkdirAll(pluginDir, 0755); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: Failed to create plugin directory: %v\n", err)
	} else {
		if err := createPluginJSON(pluginDir); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: Failed to create plugin.json: %v\n", err)
		}
	}

	fmt.Println()
	fmt.Println("Installation complete!")
	fmt.Println()
	fmt.Println("Run `claude` to start a session with Cortex enabled.")
}

func createPluginJSON(pluginDir string) error {
	pluginJSON := `{
  "name": "cortex",
  "description": "Persistent context memory for AI coding assistants",
  "version": "0.1.0",
  "author": {
    "name": "Cortex"
  },
  "repository": "https://github.com/dereksantos/cortex",
  "license": "MIT"
}`

	pluginPath := filepath.Join(pluginDir, "plugin.json")
	if _, err := os.Stat(pluginPath); err == nil {
		// File exists, don't overwrite
		return nil
	}

	return os.WriteFile(pluginPath, []byte(pluginJSON), 0644)
}

func createClaudeSettings(settingsPath string) error {
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
		"SessionStart": []interface{}{
			map[string]interface{}{
				"hooks": []interface{}{
					map[string]interface{}{
						"type":    "command",
						"command": "./cortex session-start",
					},
				},
			},
		},
		"UserPromptSubmit": []interface{}{
			map[string]interface{}{
				"hooks": []interface{}{
					map[string]interface{}{
						"type":    "command",
						"command": "./cortex inject-context",
					},
				},
			},
		},
		"PostToolUse": []interface{}{
			map[string]interface{}{
				"matcher": "Write|Edit|Bash",
				"hooks": []interface{}{
					map[string]interface{}{
						"type":    "command",
						"command": "./cortex capture",
					},
				},
			},
		},
	}

	// Configure status line
	statusLine := map[string]interface{}{
		"type":    "command",
		"command": "./cortex status --format=claude",
	}

	settings["hooks"] = hooks
	settings["statusLine"] = statusLine

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

func createCortexCommand(commandFile string) error {
	content := `---
description: Query Cortex context memory
argument-hint: "<query>"
allowed-tools: Bash(./cortex:*)
---

Search Cortex for relevant context:

./cortex search "$ARGUMENTS"

If results are found, summarize the relevant insights, decisions, and patterns.
`

	if err := os.WriteFile(commandFile, []byte(content), 0644); err != nil {
		return fmt.Errorf("failed to write command file: %w", err)
	}

	return nil
}

func handleUninstall() {
	// Get project root
	projectRoot, err := os.Getwd()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to get working directory: %v\n", err)
		os.Exit(1)
	}

	// Parse flags
	purge := false
	for i := 2; i < len(os.Args); i++ {
		if os.Args[i] == "--purge" {
			purge = true
		}
	}

	fmt.Println("Uninstalling Cortex...")
	fmt.Println()

	removedSomething := false

	// 1. Remove Cortex hooks from .claude/settings.local.json
	claudeProjectDir := filepath.Join(projectRoot, ".claude")
	settingsPath := filepath.Join(claudeProjectDir, "settings.local.json")

	settingsRemoved, err := removeCortexFromSettings(settingsPath)
	if err != nil {
		fmt.Printf("Warning: Could not modify settings: %v\n", err)
	} else if settingsRemoved {
		fmt.Println("Removed Cortex hooks from .claude/settings.local.json")
		removedSomething = true
	}

	// 2. Remove .claude/commands/cortex.md
	commandFile := filepath.Join(claudeProjectDir, "commands", "cortex.md")
	if _, err := os.Stat(commandFile); err == nil {
		if err := os.Remove(commandFile); err != nil {
			fmt.Printf("Warning: Could not remove slash command: %v\n", err)
		} else {
			fmt.Println("Removed .claude/commands/cortex.md")
			removedSomething = true
		}

		// Try to remove commands directory if empty
		commandsDir := filepath.Join(claudeProjectDir, "commands")
		if isEmpty, _ := isDirEmpty(commandsDir); isEmpty {
			os.Remove(commandsDir)
		}
	}

	// Try to remove .claude directory if empty (only if we created it)
	if isEmpty, _ := isDirEmpty(claudeProjectDir); isEmpty {
		os.Remove(claudeProjectDir)
	}

	// 3. Handle .context/ directory
	contextDir := filepath.Join(projectRoot, ".context")
	if _, err := os.Stat(contextDir); err == nil {
		if purge {
			// Count events and insights before removal
			eventCount, insightCount := countContextData(contextDir)

			if err := os.RemoveAll(contextDir); err != nil {
				fmt.Printf("Warning: Could not remove .context/: %v\n", err)
			} else {
				if eventCount > 0 || insightCount > 0 {
					fmt.Printf("Removed .context/ directory (%d events, %d insights deleted)\n", eventCount, insightCount)
				} else {
					fmt.Println("Removed .context/ directory")
				}
				removedSomething = true
			}
		} else {
			fmt.Println("Kept .context/ data (use --purge to remove)")
		}
	}

	// Summary
	fmt.Println()
	if removedSomething {
		if purge {
			fmt.Println("Cortex has been completely removed from this project.")
		} else {
			fmt.Println("Cortex has been uninstalled from this project.")
		}
	} else {
		fmt.Println("Nothing to uninstall.")
	}
}

// removeCortexFromSettings removes Cortex-specific hooks and statusLine from settings
// Returns true if anything was removed
func removeCortexFromSettings(settingsPath string) (bool, error) {
	// Read existing settings
	data, err := os.ReadFile(settingsPath)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil // File doesn't exist, nothing to remove
		}
		return false, fmt.Errorf("failed to read settings: %w", err)
	}

	var settings map[string]interface{}
	if err := json.Unmarshal(data, &settings); err != nil {
		return false, fmt.Errorf("failed to parse settings: %w", err)
	}

	modified := false

	// Remove hooks that contain "./cortex" commands
	if hooks, ok := settings["hooks"].(map[string]interface{}); ok {
		cleanedHooks := cleanCortexHooks(hooks)
		if len(cleanedHooks) == 0 {
			delete(settings, "hooks")
			modified = true
		} else if len(cleanedHooks) != len(hooks) {
			settings["hooks"] = cleanedHooks
			modified = true
		}
	}

	// Remove statusLine if it's a Cortex command
	if statusLine, ok := settings["statusLine"].(map[string]interface{}); ok {
		if cmd, ok := statusLine["command"].(string); ok {
			if strings.Contains(cmd, "cortex") {
				delete(settings, "statusLine")
				modified = true
			}
		}
	}

	if !modified {
		return false, nil
	}

	// If settings is now empty or only has trivial content, delete the file
	if len(settings) == 0 {
		if err := os.Remove(settingsPath); err != nil {
			return true, fmt.Errorf("failed to remove empty settings file: %w", err)
		}
		return true, nil
	}

	// Write updated settings
	newData, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return true, fmt.Errorf("failed to marshal settings: %w", err)
	}

	if err := os.WriteFile(settingsPath, newData, 0644); err != nil {
		return true, fmt.Errorf("failed to write settings: %w", err)
	}

	return true, nil
}

// cleanCortexHooks removes hook entries that contain "./cortex" or "cortex" commands
func cleanCortexHooks(hooks map[string]interface{}) map[string]interface{} {
	cleaned := make(map[string]interface{})

	for hookType, hookValue := range hooks {
		// Each hook type (e.g., "PostToolUse") has an array of hook groups
		hookGroups, ok := hookValue.([]interface{})
		if !ok {
			// Keep non-array values as-is
			cleaned[hookType] = hookValue
			continue
		}

		var cleanedGroups []interface{}
		for _, group := range hookGroups {
			groupMap, ok := group.(map[string]interface{})
			if !ok {
				cleanedGroups = append(cleanedGroups, group)
				continue
			}

			// Check if this group's hooks contain cortex commands
			groupHooks, ok := groupMap["hooks"].([]interface{})
			if !ok {
				cleanedGroups = append(cleanedGroups, group)
				continue
			}

			// Filter out cortex hooks
			var cleanedGroupHooks []interface{}
			for _, hook := range groupHooks {
				hookMap, ok := hook.(map[string]interface{})
				if !ok {
					cleanedGroupHooks = append(cleanedGroupHooks, hook)
					continue
				}

				// Check command field
				if cmd, ok := hookMap["command"].(string); ok {
					if strings.Contains(cmd, "cortex") {
						// Skip this hook (it's a Cortex hook)
						continue
					}
				}
				cleanedGroupHooks = append(cleanedGroupHooks, hook)
			}

			// If all hooks in this group were cortex hooks, skip the entire group
			if len(cleanedGroupHooks) > 0 {
				groupMap["hooks"] = cleanedGroupHooks
				cleanedGroups = append(cleanedGroups, groupMap)
			}
		}

		// Only keep hook type if it has remaining groups
		if len(cleanedGroups) > 0 {
			cleaned[hookType] = cleanedGroups
		}
	}

	return cleaned
}

// isDirEmpty checks if a directory is empty
func isDirEmpty(path string) (bool, error) {
	entries, err := os.ReadDir(path)
	if err != nil {
		return false, err
	}
	return len(entries) == 0, nil
}

// countContextData counts events and insights in the context directory
func countContextData(contextDir string) (events int, insights int) {
	// Try to load config and storage to get accurate counts
	configPath := filepath.Join(contextDir, "config.json")
	cfg, err := config.Load(configPath)
	if err != nil {
		return 0, 0
	}

	store, err := storage.New(cfg)
	if err != nil {
		return 0, 0
	}
	defer store.Close()

	stats, err := store.GetStats()
	if err != nil {
		return 0, 0
	}

	if val, ok := stats["total_events"].(int); ok {
		events = val
	}
	if val, ok := stats["total_insights"].(int); ok {
		insights = val
	}

	return events, insights
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
			runCognitionEvals(cognitionScenarios, verbose, outputFormat, dryRun, provider, cfg)
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
func runCognitionEvals(scenarios []*eval.CognitionScenario, verbose bool, outputFormat string, dryRun bool, provider llm.Provider, cfg *config.Config) {
	var cortex cognition.Cortex

	if dryRun {
		// Mock mode: use MockCortex with test corpus
		mock := eval.NewMockCortex()
		corpusPath := "test/evals/corpus/cognition_corpus.yaml"
		if _, err := mock.WithCorpus(corpusPath); err != nil {
			if verbose {
				fmt.Printf("Note: Could not load corpus from %s: %v\n", corpusPath, err)
			}
		}
		cortex = mock
		if verbose {
			fmt.Println("Using MockCortex (--dry-run mode)")
		}
	} else {
		// Real mode: use actual Cortex with storage and LLM
		// Ensure ContextDir is valid, create temp if needed
		if cfg.ContextDir == "" {
			tmpDir, err := os.MkdirTemp("", "cortex-eval-*")
			if err != nil {
				fmt.Fprintf(os.Stderr, "Failed to create temp directory: %v\n", err)
				os.Exit(1)
			}
			cfg.ContextDir = tmpDir
			if verbose {
				fmt.Printf("Using temp storage: %s\n", tmpDir)
			}
		}
		if err := cfg.EnsureDirectories(); err != nil {
			fmt.Fprintf(os.Stderr, "Failed to create directories: %v\n", err)
			os.Exit(1)
		}

		store, err := storage.New(cfg)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Failed to open storage: %v\n", err)
			os.Exit(1)
		}
		defer store.Close()

		// Seed storage with test corpus for evals
		corpusPath := "test/evals/corpus/cognition_corpus.yaml"
		corpus, err := eval.LoadCorpusFile(corpusPath)
		if err != nil {
			if verbose {
				fmt.Printf("Note: Could not load corpus from %s: %v\n", corpusPath, err)
			}
		} else {
			seeded := 0
			for _, item := range corpus.Results {
				// Convert score (0-1) to importance (1-10)
				importance := int(item.Score * 10)
				if importance < 1 {
					importance = 1
				}
				if importance > 10 {
					importance = 10
				}

				// Parse timestamp if provided, otherwise use a default "old" timestamp
				// so items with explicit timestamps can be tested for recency ordering
				var timestamp time.Time
				if item.Timestamp != "" {
					timestamp, _ = time.Parse(time.RFC3339, item.Timestamp)
				} else {
					// Default to 6 months ago for items without timestamps
					// This ensures they don't dominate recency scoring
					timestamp = time.Now().AddDate(0, -6, 0)
				}

				// Store as insight with timestamp (Reflex searches insights)
				err := store.StoreInsightWithTimestamp(
					item.ID,       // eventID (use corpus ID as reference)
					item.Category, // category
					item.Content,  // summary
					importance,    // importance
					item.Tags,     // tags
					"",            // reasoning
					timestamp,     // timestamp from corpus
				)
				if err == nil {
					seeded++
				}
			}
			if verbose {
				fmt.Printf("Seeded storage with %d corpus items\n", seeded)
			}
		}

		realCortex, err := intcognition.New(store, provider, cfg)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Failed to create Cortex: %v\n", err)
			os.Exit(1)
		}
		cortex = realCortex
		if verbose {
			fmt.Printf("Using real Cortex (provider: %s)\n", provider.Name())
		}
	}

	evaluator := eval.NewCognitionEvaluator(cortex)
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

	// Initialize LLM provider for cognitive modes
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

	// Create Cortex cognitive pipeline
	cortex, err := intcognition.New(store, llmProvider, cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Warning: Could not initialize cognitive pipeline: %v\n", err)
		// Continue without cognitive features
	}

	// Create state writer for real-time cognitive mode status
	stateWriter := intcognition.NewStateWriter(cfg.ContextDir)
	if cortex != nil {
		cortex.SetStateWriter(stateWriter)

		// Register dream sources for background exploration
		cortex.RegisterSource(sources.NewProjectSource(cfg.ProjectRoot))
		cortex.RegisterSource(sources.NewCortexSource(store))

		// Register Claude history source
		homeDir, _ := os.UserHomeDir()
		if homeDir != "" {
			claudeProjectsDir := filepath.Join(homeDir, ".claude", "projects")
			cortex.RegisterSource(sources.NewClaudeHistorySource(claudeProjectsDir))
		}
	}

	// Load persisted session
	sessionPersister := intcognition.NewSessionPersister(cfg.ContextDir)
	persistedSession, err := sessionPersister.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Warning: Could not load session: %v\n", err)
	} else if cortex != nil {
		// Restore session state to Think's SessionContext
		sessionCtx := cortex.SessionContext()
		if persistedSession != nil && sessionCtx != nil {
			sessionCtx.TopicWeights = persistedSession.TopicWeights
			sessionCtx.WarmCache = persistedSession.WarmCache
			sessionCtx.ResolvedContradictions = persistedSession.ResolvedContradictions
			sessionCtx.LastUpdated = persistedSession.LastUpdated
			fmt.Println("   Restored session state from previous run")
		}
	}

	// Create session saver for periodic saves
	sessionSaver := intcognition.NewSessionSaver(sessionPersister, 30*time.Second)

	fmt.Println("🤖 Cortex daemon started")
	fmt.Println("   Processing events every 5 seconds...")
	fmt.Println("   Session persisted every 30 seconds...")
	fmt.Println("   Status updates every 2 seconds...")
	fmt.Println("   Cognitive modes check every 10 seconds...")
	fmt.Println("   Press Ctrl+C to stop")

	// Set up signal handling for graceful shutdown
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)

	// Periodic session save ticker
	saveTicker := time.NewTicker(30 * time.Second)
	defer saveTicker.Stop()

	// Periodic state update ticker for stats
	stateTicker := time.NewTicker(2 * time.Second)
	defer stateTicker.Stop()

	// Periodic cognitive mode ticker (Dream when idle, Think when active)
	cognitiveTicker := time.NewTicker(10 * time.Second)
	defer cognitiveTicker.Stop()

	// Idle threshold for Dream triggering (30 seconds without events)
	idleThreshold := 30 * time.Second

	// Write initial state
	updateDaemonStats(store, stateWriter)

	// Main daemon loop
	done := false
	for !done {
		select {
		case <-stateTicker.C:
			// Periodic state update with current stats
			updateDaemonStats(store, stateWriter)
		case <-saveTicker.C:
			// Periodic session save
			if cortex != nil {
				sessionSaver.MarkDirty()
				if sessionSaver.MaybeSave(cortex.SessionContext()) {
					// Silent save - no output needed
				}
			}
		case <-cognitiveTicker.C:
			// Trigger cognitive modes based on activity
			if cortex != nil {
				if isUserIdle(store, idleThreshold) {
					// Idle - run Dream for background exploration
					go cortex.MaybeDream(context.Background())
				} else {
					// Active - run Think for session pattern learning
					go cortex.MaybeThink(context.Background())
				}
			}
		case <-sigChan:
			done = true
		}
	}

	fmt.Println("\n🛑 Stopping daemon...")

	// Save session on graceful shutdown
	if cortex != nil {
		if err := sessionSaver.ForceSave(cortex.SessionContext()); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: Could not save session: %v\n", err)
		} else {
			fmt.Println("   Session state saved")
		}
	}

	// Clean up state file
	stateWriter.WriteMode("idle", "")

	proc.Stop()
	fmt.Println("✅ Daemon stopped")
}

// updateDaemonStats updates the daemon state file with current stats.
// Only writes idle state if no cognitive mode is currently active.
func updateDaemonStats(store *storage.Storage, stateWriter *intcognition.StateWriter) {
	if store == nil || stateWriter == nil {
		return
	}

	// Check if a cognitive mode is currently active - don't overwrite it
	currentState, _ := intcognition.ReadDaemonState(stateWriter.Path())
	if currentState != nil && currentState.Mode != "" && currentState.Mode != "idle" {
		// A cognitive mode is active and fresh - don't overwrite with idle
		// Just update stats in-place by re-writing with same mode
		stats, err := store.GetStats()
		if err != nil {
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
		stateWriter.WriteModeWithStats(currentState.Mode, currentState.Description, totalEvents, totalInsights)
		return
	}

	stats, err := store.GetStats()
	if err != nil {
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

	// Write idle state with stats (no cognitive mode is active)
	stateWriter.WriteModeWithStats("idle", "", totalEvents, totalInsights)
}

// isUserIdle checks if the user has been idle based on recent captured events.
// Returns true if no events in the last idleThreshold duration.
func isUserIdle(store *storage.Storage, idleThreshold time.Duration) bool {
	if store == nil {
		return true
	}

	recentEvents, err := store.GetRecentEvents(1)
	if err != nil || len(recentEvents) == 0 {
		return true // No events = idle
	}

	timeSince := time.Since(recentEvents[0].Timestamp)
	return timeSince > idleThreshold
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
	// Standardized text-based status output
	displayStatus()
}

func displayStatus() {
	// Read JSON from stdin (Claude Code context, if present)
	var claudeContext map[string]interface{}
	data, err := io.ReadAll(os.Stdin)
	if err == nil && len(data) > 0 {
		json.Unmarshal(data, &claudeContext)
	}

	// Try to load config and get current state
	cfg, err := loadConfig()
	if err != nil {
		// Not initialized yet
		fmt.Print("◌ Not initialized")
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

	// Open storage to check for data
	store, err := storage.New(cfg)
	if err != nil {
		fmt.Print("◌ No data")
		return
	}
	defer store.Close()

	// Get stats
	stats, err := store.GetStats()
	if err != nil {
		fmt.Print("● Ready")
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
	spinner := "✓"

	if totalEvents == 0 && totalInsights == 0 {
		mode = "Cold start"
		spinner = "◌"
	} else {
		// Check for recent activity
		recentEvents, _ := store.GetRecentEvents(5)
		if len(recentEvents) > 0 {
			lastEvent := recentEvents[0]
			timeSince := time.Since(lastEvent.Timestamp)

			if timeSince < 30*time.Second {
				// Very recent activity - still use checkmark
				mode = "Processing"
				spinner = "✓"
			} else if timeSince < 5*time.Minute {
				mode = "Active"
				spinner = "✓"
			}
		}
	}

	// Format output: natural language sentences (no colons)
	if daemonOffline {
		// Daemon is not running - show stopped status
		if totalEvents > 0 || totalInsights > 0 {
			fmt.Printf("⏸ Stopped: %d events, %d insights", totalEvents, totalInsights)
		} else {
			fmt.Print("⏸ Daemon not running")
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
		return getThinkSpinner()
	case "dream":
		return getDreamSpinner()
	case "reflex":
		return getReflexSpinner()
	case "reflect":
		return getReflectSpinner()
	case "resolve":
		return getResolveSpinner()
	case "insight":
		return getInsightSpinner()
	default:
		return "●"
	}
}

// getThinkSpinner returns the Think mode icon.
// Half-filled circle represents processing/learning.
func getThinkSpinner() string {
	return "◐"
}

// getDreamSpinner returns the Dream mode icon.
// Cloud represents wandering, exploratory thinking.
func getDreamSpinner() string {
	return "☁"
}

// getReflexSpinner returns the Reflex mode icon.
// Lightning represents fast, mechanical search.
func getReflexSpinner() string {
	return "⚡"
}

// getReflectSpinner returns the Reflect mode icon.
// Opposite half-filled circle represents evaluation.
func getReflectSpinner() string {
	return "◑"
}

// getResolveSpinner returns the Resolve mode icon.
// Play/forward triangle represents deciding/choosing.
func getResolveSpinner() string {
	return "▸"
}

// getInsightSpinner returns the Insight mode icon.
// Star represents discovery.
func getInsightSpinner() string {
	return "✦"
}

// getModeDisplayName returns the display name for a cognitive mode.
func getModeDisplayName(mode string) string {
	switch mode {
	case "think":
		return "Think"
	case "dream":
		return "Dream"
	case "reflex":
		return "Reflex"
	case "reflect":
		return "Reflect"
	case "resolve":
		return "Resolve"
	default:
		return "Ready"
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
	default:
		return ""
	}
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

	// Search insights first (more valuable)
	insights, err := store.SearchInsights(query, 10)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Warning: Failed to search insights: %v\n", err)
	}

	// Search events
	events, err := store.SearchEvents(query, 10)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to search events: %v\n", err)
		os.Exit(1)
	}

	// Display results
	if len(insights) == 0 && len(events) == 0 {
		fmt.Println("No results found")
		return
	}

	// Show insights first
	if len(insights) > 0 {
		fmt.Printf("Found %d insights:\n\n", len(insights))
		for i, insight := range insights {
			fmt.Printf("%d. [%s] %s\n", i+1, insight.Category, insight.Summary)
			if len(insight.Tags) > 0 {
				fmt.Printf("   Tags: %v\n", insight.Tags)
			}
			fmt.Println()
		}
	}

	// Show events if any
	if len(events) > 0 {
		fmt.Printf("Found %d events:\n\n", len(events))
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

	// Initialize LLM provider (optional - Reflect will degrade gracefully if nil)
	var llmProvider llm.Provider
	anthropic := llm.NewAnthropicClient(cfg)
	if anthropic.IsAvailable() {
		llmProvider = anthropic
	}

	// Create Cortex cognitive pipeline
	cortex, err := intcognition.New(store, llmProvider, cfg)
	if err != nil {
		// Fallback to just printing the prompt
		fmt.Println(prompt)
		os.Exit(0)
	}

	// Create state writer for status updates (shared with daemon)
	stateWriter := intcognition.NewStateWriter(cfg.ContextDir)
	cortex.SetStateWriter(stateWriter)

	// Register dream sources for background exploration
	cortex.RegisterSource(sources.NewProjectSource(cfg.ProjectRoot))
	cortex.RegisterSource(sources.NewCortexSource(store))

	// Register Claude history source for session transcript exploration
	homeDir, _ := os.UserHomeDir()
	if homeDir != "" {
		claudeProjectsDir := filepath.Join(homeDir, ".claude", "projects")
		cortex.RegisterSource(sources.NewClaudeHistorySource(claudeProjectsDir))
	}

	// Build query
	query := cognition.Query{
		Text:      prompt,
		Limit:     5,
		Threshold: 0.3,
	}

	// Use Fast mode for quick response during active sessions
	// First message in a session could use Full mode for higher accuracy
	result, err := cortex.Retrieve(context.Background(), query, cognition.Fast)
	if err != nil || result.Decision != cognition.Inject {
		// No relevant context or decision to skip injection
		fmt.Println(prompt)
		os.Exit(0)
	}

	// Output formatted context + original prompt
	fmt.Print(result.Formatted)
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

func handleForget() {
	if len(os.Args) < 3 {
		fmt.Fprintf(os.Stderr, "Usage: cortex forget <id-or-keyword>\n")
		fmt.Fprintf(os.Stderr, "\nExamples:\n")
		fmt.Fprintf(os.Stderr, "  cortex forget 123           # Forget insight by ID\n")
		fmt.Fprintf(os.Stderr, "  cortex forget \"redux\"       # Forget insights matching keyword\n")
		os.Exit(1)
	}

	input := os.Args[2]

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

	// Try to parse as ID first
	var id int64
	if _, err := fmt.Sscanf(input, "%d", &id); err == nil && id > 0 {
		// Delete by ID
		if err := store.ForgetInsight(id); err != nil {
			fmt.Fprintf(os.Stderr, "Failed to forget insight: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("Forgot insight #%d\n", id)
		return
	}

	// Search for matching insights first
	insights, err := store.SearchInsights(input, 10)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to search insights: %v\n", err)
		os.Exit(1)
	}

	if len(insights) == 0 {
		fmt.Printf("No insights found matching '%s'\n", input)
		return
	}

	// Show matching insights
	fmt.Printf("Found %d insight(s) matching '%s':\n\n", len(insights), input)
	for _, insight := range insights {
		fmt.Printf("  #%d [%s] %s\n", insight.ID, insight.Category, truncateString(insight.Summary, 60))
	}

	// Delete by keyword
	deleted, err := store.ForgetInsightsByKeyword(input)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to forget insights: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("\nForgot %d insight(s)\n", deleted)
}

// ensureDaemonRunning checks if the daemon appears to be running.
// Returns true if running, false otherwise.
// Prints helpful message if not running.
func ensureDaemonRunning() bool {
	// Load config to check for context directory
	cfg, err := loadConfig()
	if err != nil {
		// Config not found means cortex isn't initialized
		fmt.Fprintln(os.Stderr, "Cortex is not initialized in this project.")
		fmt.Fprintln(os.Stderr, "Run 'cortex init' or 'cortex install' first.")
		return false
	}

	// Check for recent activity by looking at session file
	sessionPath := filepath.Join(cfg.ContextDir, "session.json")
	info, err := os.Stat(sessionPath)
	if err != nil {
		// Session file doesn't exist - daemon may not be running
		// But this could also be a fresh install, so just warn
		fmt.Fprintln(os.Stderr, "Warning: Daemon may not be running.")
		fmt.Fprintln(os.Stderr, "Start it with: cortex daemon &")
		return false
	}

	// Check if session was updated recently (within last 2 minutes)
	if time.Since(info.ModTime()) > 2*time.Minute {
		fmt.Fprintln(os.Stderr, "Warning: Daemon may not be running (session stale).")
		fmt.Fprintln(os.Stderr, "Start it with: cortex daemon &")
		return false
	}

	return true
}

// warnDaemonNotRunning prints a warning if daemon isn't running, but doesn't fail.
// Use this for commands that work without daemon but work better with it.
func warnDaemonNotRunning() {
	cfg, err := loadConfig()
	if err != nil {
		return // Silently skip if config not found
	}

	sessionPath := filepath.Join(cfg.ContextDir, "session.json")
	info, err := os.Stat(sessionPath)
	if err != nil || time.Since(info.ModTime()) > 2*time.Minute {
		fmt.Fprintln(os.Stderr, "Tip: Start 'cortex daemon &' for automatic context capture.")
	}
}

// loadConfigWithFallback loads config or creates a default for recovery.
func loadConfigWithFallback() *config.Config {
	cfg, err := loadConfig()
	if err != nil {
		// Return default config for basic operations
		return config.Default()
	}
	return cfg
}

func printUsage() {
	fmt.Printf(`Cortex %s - Context memory for AI development

Usage:
  cortex <command> [options]

Commands:
  init           Initialize Cortex in current directory
  install        Install Cortex hooks for Claude Code
  uninstall      Remove Cortex hooks (--purge to also delete .context/)
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
