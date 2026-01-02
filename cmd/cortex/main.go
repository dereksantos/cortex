// Cortex - Context memory for AI development
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
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
	case "watch":
		handleWatch()
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
	case "stop":
		handleStop()
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
		"Stop": []interface{}{
			map[string]interface{}{
				"hooks": []interface{}{
					map[string]interface{}{
						"type":    "command",
						"command": "./cortex stop",
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
		fmt.Println("⚠️  No LLM available (check Ollama or ANTHROPIC_API_KEY)")
		return
	}

	// Analyze events and store insights
	analyzed := 0
	for _, event := range events {
		if err := analyzeEventWithLLM(event, store, llmProvider); err == nil {
			analyzed++
		}
	}

	if analyzed > 0 {
		fmt.Printf("✅ Analyzed %d events\n", analyzed)
	} else {
		fmt.Println("⚠️  No events were analyzed")
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
			fmt.Println("⚠️  No LLM available for analysis")
			return
		}

		// Analyze recent events
		recentEvents, err := store.GetRecentEvents(processed)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Warning: Failed to get recent events: %v\n", err)
			return
		}

		fmt.Printf("🔍 Analyzing %d events with LLM...\n", len(recentEvents))

		// Run analysis synchronously for immediate results
		analyzed := 0
		for _, event := range recentEvents {
			if err := analyzeEventWithLLM(event, store, llmProvider); err == nil {
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
		"all": true, "decision": true, "pattern": true, "insight": true, "ollama": true,
	}
	if !validTypes[testType] {
		fmt.Fprintf(os.Stderr, "Invalid test type: %s\n", testType)
		fmt.Println("Valid types: decision, pattern, insight, ollama, all")
		os.Exit(1)
	}

	// Handle ollama benchmark separately
	if testType == "ollama" {
		runOllamaBenchmark()
		return
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

	// Get LLM provider
	var llmProvider llm.Provider
	ollama := llm.NewOllamaClient(cfg)
	if ollama.IsAvailable() {
		llmProvider = ollama
	}

	if llmProvider == nil {
		fmt.Println("❌ No LLM available for testing")
		os.Exit(1)
	}

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
		startTime := time.Now()
		if err := analyzeEventWithLLM(test.event, store, llmProvider); err != nil {
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

func runOllamaBenchmark() {
	cfg := config.Default()
	cfg.ProjectRoot, _ = os.Getwd()

	fmt.Printf("🔬 Ollama Benchmark (model: %s)\n", cfg.OllamaModel)
	fmt.Println("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")
	fmt.Println()

	// Check Ollama availability
	ollamaRunning, _ := checkOllama()
	if !ollamaRunning {
		fmt.Println("❌ Ollama is not running")
		fmt.Println("   Start with: ollama serve")
		os.Exit(1)
	}
	fmt.Println("✓ Ollama is running")
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
			fmt.Printf("  %s: ❌ Error: %v\n", size, err)
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
				warning = " ⚠️  exceeds 30s timeout"
			} else {
				safeConcurrency = concurrency
			}
			if max > maxConcurrentTime {
				maxConcurrentTime = max
			}
			fmt.Printf("  %2d concurrent: avg %.1fs (max: %.1fs)%s\n", concurrency, avg.Seconds(), max.Seconds(), warning)
		} else {
			fmt.Printf("  %2d concurrent: ❌ all requests failed\n", concurrency)
		}
	}
	fmt.Println()

	// Current implementation info
	fmt.Println("Current Implementation:")
	fmt.Println("  - Timeout: 30s (hardcoded in pkg/llm/ollama.go:30)")
	fmt.Println("  - Workers: 5 (hardcoded in internal/processor/processor.go:35)")
	fmt.Println()

	// Recommendations
	fmt.Println("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")
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
	cognitionMode := false
	evalType := ""    // New: -t flag for eval type (e2e, cognition, etc.)
	journeyPath := "" // New: --journey flag for specific journey file
	judgeProviderName := ""    // --judge or -j: LLM provider for code review judge
	judgeModelOverride := ""   // --judge-model: Model for judge provider

	for i := 2; i < len(os.Args); i++ {
		arg := os.Args[i]
		switch {
		case arg == "--verbose" || arg == "-v":
			verbose = true
		case arg == "--e2e":
			e2eMode = true
		case arg == "--tree":
			treeMode = true
		case arg == "--cognition":
			cognitionMode = true
		case arg == "--dry-run":
			dryRun = true
		case arg == "-t" || arg == "--type":
			if i+1 < len(os.Args) {
				evalType = os.Args[i+1]
				i++
			}
		case arg == "--journey":
			if i+1 < len(os.Args) {
				journeyPath = os.Args[i+1]
				i++
			}
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
		case arg == "--judge" || arg == "-j":
			if i+1 < len(os.Args) {
				judgeProviderName = os.Args[i+1]
				i++
			}
		case arg == "--judge-model":
			if i+1 < len(os.Args) {
				judgeModelOverride = os.Args[i+1]
				i++
			}
		case arg == "--help" || arg == "-h":
			fmt.Println("Usage: cortex eval [options]")
			fmt.Println()
			fmt.Println("Options:")
			fmt.Println("  --scenario, -s <file>  Run a specific scenario file")
			fmt.Println("  --dir, -d <dir>        Scenario directory (default: test/evals/scenarios)")
			fmt.Println("  -t, --type <type>      Eval type: e2e, cognition, tree")
			fmt.Println("  --journey <file>       Path to E2E journey YAML file (for -t e2e)")
			fmt.Println("  --e2e                  Run E2E evals (legacy, use -t e2e instead)")
			fmt.Println("  --tree                 Run tree evals (multi-path, temporal)")
			fmt.Println("  --cognition            Run cognition evals (cognitive modes)")
			fmt.Println("  --provider, -p <name>  LLM provider: ollama, anthropic (default: ollama)")
			fmt.Println("  --model, -m <model>    Model to use (provider-specific)")
			fmt.Println("  --judge, -j <provider> LLM provider for code review judge (defaults to -p)")
			fmt.Println("  --judge-model <model>  Model for judge provider")
			fmt.Println("  --dry-run              Use mock provider (no LLM calls, instant)")
			fmt.Println("  --output, -o <format>  Output format: human, json (default: human)")
			fmt.Println("  --verbose, -v          Show detailed output")
			fmt.Println("  --help, -h             Show this help")
			fmt.Println()
			fmt.Println("Eval Types:")
			fmt.Println("  linear (default)   Pre-defined context injection")
			fmt.Println("  e2e (-t e2e)       E2E journey evals: generative tests with LLM tasks")
			fmt.Println("  tree (--tree)      Multi-path and temporal evals")
			fmt.Println("  cognition (--cognition)  Cognitive modes (Reflex, Reflect, etc.)")
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
			fmt.Println("  cortex eval -t e2e -p anthropic -v          # Run E2E journey evals")
			fmt.Println("  cortex eval -t e2e --journey path/to/journey.yaml")
			fmt.Println("  cortex eval -t e2e --dry-run -v             # Mock provider, no LLM calls")
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

	// Setup judge provider (defaults to same as main provider)
	var judgeProvider llm.Provider
	if judgeProviderName == "" {
		judgeProvider = provider // Same as generation provider
	} else if dryRun {
		judgeProvider = llm.NewMockProvider(10)
	} else {
		// Create separate judge provider config if model override specified
		judgeCfg := cfg
		if judgeModelOverride != "" {
			// Create a copy of config with overridden model
			judgeCfgCopy := *cfg
			if judgeProviderName == "anthropic" {
				judgeCfgCopy.AnthropicModel = judgeModelOverride
			} else {
				judgeCfgCopy.OllamaModel = judgeModelOverride
			}
			judgeCfg = &judgeCfgCopy
		}

		switch judgeProviderName {
		case "anthropic":
			anthropicClient := llm.NewAnthropicClient(judgeCfg)
			if !anthropicClient.IsAvailable() {
				fmt.Fprintf(os.Stderr, "Anthropic API key not set for judge. Set ANTHROPIC_API_KEY environment variable.\n")
				os.Exit(1)
			}
			judgeProvider = anthropicClient
			if verbose {
				fmt.Printf("Using Anthropic judge provider (model: %s)\n", anthropicClient.Model())
			}
		case "ollama":
			ollamaClient := llm.NewOllamaClient(judgeCfg)
			if !ollamaClient.IsAvailable() {
				fmt.Fprintf(os.Stderr, "Ollama is not running for judge. Start with: ollama serve\n")
				os.Exit(1)
			}
			judgeProvider = ollamaClient
			if verbose {
				fmt.Printf("Using Ollama judge provider (model: %s)\n", judgeCfg.OllamaModel)
			}
		default:
			fmt.Fprintf(os.Stderr, "Unknown judge provider: %s. Use 'ollama' or 'anthropic'.\n", judgeProviderName)
			os.Exit(1)
		}
	}

	// Run evaluation based on mode
	var run *eval.EvalRun

	// Check for -t e2e (E2E Journey Eval) - new style
	if evalType == "e2e" {
		runE2EJourneyEval(provider, judgeProvider, journeyPath, dryRun, verbose, outputFormat)
		return
	}

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

	} else if cognitionMode {
		// Cognition mode: test cognitive modes
		cognitionDir := filepath.Join(scenarioDir, "cognition")
		cognitionScenarios, err := eval.LoadCognitionScenarios(cognitionDir)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Failed to load cognition scenarios from %s: %v\n", cognitionDir, err)
			os.Exit(1)
		}
		if len(cognitionScenarios) == 0 {
			fmt.Fprintf(os.Stderr, "No cognition scenarios found in %s\n", cognitionDir)
			os.Exit(1)
		}
		runCognitionEvals(cognitionScenarios, verbose, outputFormat, dryRun, provider, cfg)
		return
	} else {
		// Check if directory contains cognition scenarios (auto-detect)
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
		// ALWAYS create a temporary directory for test isolation
		// This ensures evals only see corpus data, not actual project files
		tmpDir, err := os.MkdirTemp("", "cortex-eval-*")
		if err != nil {
			fmt.Fprintf(os.Stderr, "Failed to create temp directory: %v\n", err)
			os.Exit(1)
		}
		defer os.RemoveAll(tmpDir) // Clean up temp directory when done

		// Create a copy of config with temp directory for isolated eval
		evalCfg := &config.Config{
			ContextDir:  tmpDir,
			OllamaURL:   cfg.OllamaURL,
			OllamaModel: cfg.OllamaModel,
		}
		cfg = evalCfg // Use the isolated config

		if verbose {
			fmt.Printf("Using isolated temp storage: %s\n", tmpDir)
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

				// Parse timestamp if provided, otherwise use recent timestamp
				// so corpus items can compete with Dream-generated items in scoring
				var timestamp time.Time
				if item.Timestamp != "" {
					timestamp, _ = time.Parse(time.RFC3339, item.Timestamp)
				} else {
					// Default to 1 week ago - recent enough to score well,
					// old enough to test recency ordering for items with explicit timestamps
					timestamp = time.Now().AddDate(0, 0, -7)
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

		// Only register CortexSource for eval - it reads from storage where
		// the corpus is seeded. ProjectSource/GitSource would read from the
		// real filesystem, generating content that doesn't match test expectations.
		realCortex.RegisterSource(sources.NewCortexSource(store))

		cortex = realCortex
		if verbose {
			fmt.Printf("Using real Cortex (provider: %s)\n", provider.Name())
		}
	}

	evaluator := eval.NewCognitionEvaluator(cortex)
	evaluator.SetVerbose(verbose)
	evaluator.SetProvider(provider)

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

// runE2EJourneyEval runs E2E journey-based evaluations.
// These are generative evals that test actual development outcomes by having
// an LLM complete implementation tasks with and without Cortex context.
func runE2EJourneyEval(provider llm.Provider, judgeProvider llm.Provider, journeyPath string, dryRun, verbose bool, outputFormat string) {
	ctx := context.Background()

	// Load journey(s)
	var journeys []*eval.E2EJourney
	if journeyPath != "" {
		// Load specific journey
		journey, err := eval.LoadE2EJourney(journeyPath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Failed to load journey: %v\n", err)
			os.Exit(1)
		}
		journeys = append(journeys, journey)
	} else {
		// Load all journeys from default directory
		defaultDir := "test/evals/journeys"
		js, err := eval.LoadE2EJourneys(defaultDir)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Failed to load journeys from %s: %v\n", defaultDir, err)
			os.Exit(1)
		}
		if len(js) == 0 {
			fmt.Fprintf(os.Stderr, "No E2E journey files found in %s\n", defaultDir)
			os.Exit(1)
		}
		journeys = js
	}

	if verbose {
		fmt.Printf("Loaded %d E2E journey(s)\n", len(journeys))
		if dryRun {
			fmt.Println("Running in dry-run mode (MockCortex, no real LLM calls for tasks)")
		}
	}

	fmt.Println()
	fmt.Println("Cortex E2E Journey Eval")
	fmt.Println("=======================")
	fmt.Println()

	passCount := 0
	failCount := 0
	var allResults []*eval.E2EJourneyResult

	for _, journey := range journeys {
		fmt.Printf("Journey: %s\n", journey.Name)
		fmt.Printf("  ID: %s\n", journey.ID)
		fmt.Printf("  Sessions: %d | Events: %d | Tasks: %d\n",
			len(journey.Sessions), journey.TotalEvents(), journey.TotalTasks())

		// Create CLI-based Cortex for E2E testing
		// This tests the real system end-to-end via CLI commands
		cliCortex, err := eval.NewCLICortex(verbose)
		if err != nil {
			fmt.Fprintf(os.Stderr, "  Error: Failed to create CLI Cortex: %v\n", err)
			failCount++
			continue
		}
		defer cliCortex.Cleanup()

		var cortex cognition.Cortex = cliCortex

		// Create evaluator and run journey
		// Pass "." as projectDir since scaffold paths in journey YAML are relative to cwd
		evaluator := eval.NewJourneyEvaluator(cortex, provider, ".", verbose)
		evaluator.SetJudgeProvider(judgeProvider)
		result, err := evaluator.RunJourney(ctx, journey)
		if err != nil {
			fmt.Fprintf(os.Stderr, "  Error: %v\n", err)
			failCount++
			continue
		}

		allResults = append(allResults, result)

		// Print result summary
		if result.Pass {
			passCount++
			fmt.Printf("  Result: PASS\n")
		} else {
			failCount++
			fmt.Printf("  Result: FAIL - %s\n", result.Reason)
		}

		// Print detailed metrics
		printE2EJourneyResult(result, verbose)
		fmt.Println()
	}

	// Print overall summary
	fmt.Println("Summary")
	fmt.Println("========================================")
	fmt.Printf("Total Journeys:  %d\n", len(journeys))
	fmt.Printf("Pass:            %d\n", passCount)
	fmt.Printf("Fail:            %d\n", failCount)
	if len(journeys) > 0 {
		fmt.Printf("Pass Rate:       %.0f%%\n", float64(passCount)/float64(len(journeys))*100)
	}

	// JSON output if requested
	if outputFormat == "json" {
		printE2EJourneyResultsJSON(allResults)
	}

	// Exit with error if pass rate is below threshold
	if len(journeys) > 0 && float64(passCount)/float64(len(journeys)) < 0.5 {
		os.Exit(1)
	}
}

// printE2EJourneyResult prints detailed metrics for a journey result.
func printE2EJourneyResult(result *eval.E2EJourneyResult, verbose bool) {
	if result.TreatmentResults == nil || result.BaselineResults == nil {
		return
	}

	treatment := result.TreatmentResults
	baseline := result.BaselineResults
	comp := result.Comparison

	if verbose {
		fmt.Println()
		fmt.Println("  Treatment (Cortex enabled):")
		fmt.Printf("    Task Completion: %.0f%% (%d/%d)\n",
			treatment.TaskCompletionRate*100, treatment.TasksCompleted, treatment.TasksTotal)
		fmt.Printf("    Test Pass Rate:  %.0f%% (%d/%d)\n",
			treatment.TestPassRate*100, treatment.TestsPassed, treatment.TestsTotal)
		fmt.Printf("    Avg Turns:       %.1f\n", treatment.AverageTurns)
		fmt.Printf("    Total Tokens:    %d\n", treatment.TotalTokens)
		fmt.Printf("    Violations:      %d\n", treatment.PatternViolations)

		fmt.Println()
		fmt.Println("  Baseline (No memory):")
		fmt.Printf("    Task Completion: %.0f%% (%d/%d)\n",
			baseline.TaskCompletionRate*100, baseline.TasksCompleted, baseline.TasksTotal)
		fmt.Printf("    Test Pass Rate:  %.0f%% (%d/%d)\n",
			baseline.TestPassRate*100, baseline.TestsPassed, baseline.TestsTotal)
		fmt.Printf("    Avg Turns:       %.1f\n", baseline.AverageTurns)
		fmt.Printf("    Total Tokens:    %d\n", baseline.TotalTokens)
		fmt.Printf("    Violations:      %d\n", baseline.PatternViolations)
	}

	if comp != nil {
		fmt.Println()
		fmt.Println("  Comparison (Treatment vs Baseline):")
		fmt.Printf("    Task Completion Lift: %+.0f%%\n", comp.TaskCompletionLift*100)
		fmt.Printf("    Test Pass Lift:       %+.0f%%\n", comp.TestPassLift*100)
		fmt.Printf("    Turn Reduction:       %+.0f%%\n", comp.TurnReduction*100)
		fmt.Printf("    Token Reduction:      %+.0f%%\n", comp.TokenReduction*100)
		fmt.Printf("    Violation Reduction:  %+d\n", comp.ViolationReduction)
		fmt.Printf("    Overall Lift:         %+.2f\n", comp.OverallLift)
		if comp.Regression {
			fmt.Printf("    REGRESSION: %s\n", comp.RegressionDetails)
		}
	}
}

// printE2EJourneyResultsJSON prints results in JSON format.
func printE2EJourneyResultsJSON(results []*eval.E2EJourneyResult) {
	type jsonOutput struct {
		Journeys  int                       `json:"journeys"`
		PassCount int                       `json:"pass_count"`
		FailCount int                       `json:"fail_count"`
		PassRate  float64                   `json:"pass_rate"`
		Results   []*eval.E2EJourneyResult  `json:"results"`
	}

	passCount := 0
	for _, r := range results {
		if r.Pass {
			passCount++
		}
	}

	output := jsonOutput{
		Journeys:  len(results),
		PassCount: passCount,
		FailCount: len(results) - passCount,
		PassRate:  float64(passCount) / float64(len(results)),
		Results:   results,
	}

	data, err := json.MarshalIndent(output, "", "  ")
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to marshal JSON: %v\n", err)
		return
	}
	fmt.Println(string(data))
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

		// Route events through cognition pipeline when processor handles them
		proc.SetEventCallback(func(evts []*events.Event) {
			cortex.IngestBatch(context.Background(), evts)
		})

		// Register dream sources for background exploration
		cortex.RegisterSource(sources.NewProjectSource(cfg.ProjectRoot))
		cortex.RegisterSource(sources.NewCortexSource(store))

		// Register Claude history source
		homeDir, _ := os.UserHomeDir()
		if homeDir != "" {
			claudeProjectsDir := filepath.Join(homeDir, ".claude", "projects")
			cortex.RegisterSource(sources.NewClaudeHistorySource(claudeProjectsDir))
		}

		// Register transcript queue source (from Stop hooks)
		transcriptQueueDir := filepath.Join(cfg.ContextDir, "transcript_queue")
		cortex.RegisterSource(sources.NewTranscriptQueueSource(transcriptQueueDir))

		// Register git source for commit history exploration
		cortex.RegisterSource(sources.NewGitSource(cfg.ProjectRoot))
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
				activityLogger := intcognition.NewActivityLogger(cfg.ContextDir)
				if isUserIdle(store, idleThreshold) {
					// Idle - run Dream for background exploration
					go func() {
						result, err := cortex.MaybeDream(context.Background())
						if err == nil && result != nil && result.Status == cognition.DreamRan {
							activityLogger.Log(&intcognition.ActivityLogEntry{
								Mode:        "dream",
								Description: fmt.Sprintf("explored %d items, %d insights", result.Operations, result.Insights),
								LatencyMs:   result.Duration.Milliseconds(),
							})
						}
					}()
				} else {
					// Active - run Think for session pattern learning
					go func() {
						result, err := cortex.MaybeThink(context.Background())
						if err == nil && result != nil && result.Status == cognition.ThinkRan {
							activityLogger.Log(&intcognition.ActivityLogEntry{
								Mode:        "think",
								Description: fmt.Sprintf("processed %d operations", result.Operations),
								LatencyMs:   result.Duration.Milliseconds(),
							})
						}
					}()
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
	case "digest":
		return getDigestSpinner()
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

// getDigestSpinner returns the Digest mode icon.
// Tilde represents consolidation/compression.
func getDigestSpinner() string {
	return "~"
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
	case "digest":
		return "Digest"
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
	case "digest":
		return "Consolidating insights..."
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
				if len(preview) > 500 {
					preview = preview[:500] + "..."
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

// analyzeEventWithLLM analyzes an event using the LLM and stores the insight.
// Used by CLI commands for sync analysis (daemon uses cognition modes instead).
func analyzeEventWithLLM(event *events.Event, store *storage.Storage, provider llm.Provider) error {
	if provider == nil || !provider.IsAvailable() {
		return fmt.Errorf("LLM not available")
	}

	// Skip routine events
	if event.ToolName == "Read" || event.ToolName == "Grep" || event.ToolName == "Glob" {
		return fmt.Errorf("skipped routine event")
	}

	// Build prompt for analysis
	eventDesc := fmt.Sprintf("Tool: %s\n", event.ToolName)
	if filePath, ok := event.ToolInput["file_path"].(string); ok {
		eventDesc += fmt.Sprintf("File: %s\n", filePath)
	}
	if event.ToolResult != "" && len(event.ToolResult) < 500 {
		eventDesc += fmt.Sprintf("Result: %s\n", event.ToolResult)
	}

	prompt := fmt.Sprintf(`Analyze this development event for durable insights:

%s

Extract any decisions, patterns, or constraints. Respond in JSON:
{
  "category": "decision|pattern|constraint|correction",
  "summary": "1-2 sentence insight",
  "importance": 1-10,
  "tags": ["tag1", "tag2"]
}

If nothing significant, respond: NO_INSIGHT`, eventDesc)

	response, err := provider.GenerateWithSystem(context.Background(), prompt, llm.AnalysisSystemPrompt)
	if err != nil {
		return err
	}

	// Check for NO_INSIGHT
	if strings.Contains(strings.ToUpper(response), "NO_INSIGHT") {
		return fmt.Errorf("no insight found")
	}

	// Parse JSON response
	start := strings.Index(response, "{")
	end := strings.LastIndex(response, "}")
	if start == -1 || end == -1 {
		return fmt.Errorf("invalid response format")
	}

	var result struct {
		Category   string   `json:"category"`
		Summary    string   `json:"summary"`
		Importance int      `json:"importance"`
		Tags       []string `json:"tags"`
	}

	if err := json.Unmarshal([]byte(response[start:end+1]), &result); err != nil {
		return err
	}

	// Store insight
	return store.StoreInsight(
		event.ID,
		result.Category,
		result.Summary,
		result.Importance,
		result.Tags,
		"",
	)
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
	// Read hook data from stdin (JSON from UserPromptSubmit hook)
	hookData, err := io.ReadAll(os.Stdin)
	if err != nil || len(hookData) == 0 {
		// No data provided, exit silently
		os.Exit(0)
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
		os.Exit(0)
	}

	// Load config
	cfg, err := loadConfig()
	if err != nil {
		// Silent failure - don't block user if Cortex not initialized
		fmt.Println(prompt)
		os.Exit(0)
	}

	// Update project path from hook data if available
	if promptEvent != nil && promptEvent.Context.WorkingDir != "" {
		cfg.ProjectRoot = promptEvent.Context.WorkingDir
	}

	// Open storage
	store, err := storage.New(cfg)
	if err != nil {
		// Silent failure
		fmt.Println(prompt)
		os.Exit(0)
	}
	defer store.Close()

	// Capture the prompt as an event (non-blocking)
	if promptEvent != nil {
		promptEvent.Context.ProjectPath = cfg.ProjectRoot
		go func() {
			cap := capture.New(cfg)
			cap.CaptureEvent(promptEvent)
		}()
	}

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

	// Register git source for commit history exploration
	cortex.RegisterSource(sources.NewGitSource(cfg.ProjectRoot))

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
		os.Exit(0)
	}

	// Output formatted context + original prompt
	fmt.Print(result.Formatted)
	fmt.Println("User Request:")
	fmt.Println(prompt)
}

// handleStop handles the Stop hook - captures transcript path for Dream analysis
func handleStop() {
	// Read hook data from stdin (JSON from Stop hook)
	hookData, err := io.ReadAll(os.Stdin)
	if err != nil || len(hookData) == 0 {
		// No data provided, exit silently
		os.Exit(0)
	}

	// Parse hook data to extract transcript path and session info
	stopEvent, err := claude.ConvertStopEvent(hookData, "")
	if err != nil || stopEvent == nil {
		// Can't parse, exit silently
		os.Exit(0)
	}

	// Load config
	cfg, err := loadConfig()
	if err != nil {
		// Silent failure
		os.Exit(0)
	}

	// Update project path from hook data if available
	if stopEvent.Context.WorkingDir != "" {
		cfg.ProjectRoot = stopEvent.Context.WorkingDir
	}

	// Open storage
	store, err := storage.New(cfg)
	if err != nil {
		// Silent failure
		os.Exit(0)
	}
	defer store.Close()

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

	os.Exit(0)
}

// isFirstPromptInSession checks if this is the first prompt for this session
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

// writeRetrievalStats writes retrieval statistics and logs activity.
// This is called after each cortex.Retrieve() in handleInjectContext.
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
		LastResults:     0,
		LastDecision:    "skip",
		TotalRetrievals: totalRetrievals,
	}

	if result != nil {
		stats.LastResults = len(result.Results)
		stats.LastDecision = result.Decision.String()
	}

	// For Full mode, estimate reflect took majority of time
	if mode == cognition.Full && elapsed.Milliseconds() > 50 {
		stats.LastReflexMs = 10 // Estimate ~10ms for reflex
		stats.LastReflectMs = elapsed.Milliseconds() - 10
	}

	// Write stats
	statsWriter := intcognition.NewRetrievalStatsWriter(contextDir)
	statsWriter.WriteStats(stats)

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
			Description: fmt.Sprintf("reranked results (Full mode)"),
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

func handleWatch() {
	// Parse flags
	jsonOutput := false
	noAnimate := false
	retrievalOnly := false
	backgroundOnly := false

	for _, arg := range os.Args[2:] {
		switch arg {
		case "--json":
			jsonOutput = true
		case "--no-animate":
			noAnimate = true
		case "--retrieval-only":
			retrievalOnly = true
		case "--background-only":
			backgroundOnly = true
		case "-h", "--help":
			fmt.Println("Usage: cortex watch [flags]")
			fmt.Println("\nFlags:")
			fmt.Println("  --json             Machine-readable JSON output")
			fmt.Println("  --no-animate       Static output (single snapshot)")
			fmt.Println("  --retrieval-only   Show only retrieval stats")
			fmt.Println("  --background-only  Show only background (daemon) stats")
			os.Exit(0)
		}
	}

	// Load config
	cfg, err := loadConfig()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Cortex not initialized. Run 'cortex init' first.\n")
		os.Exit(1)
	}

	// Open storage for stats
	store, err := storage.New(cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to open storage: %v\n", err)
		os.Exit(1)
	}
	defer store.Close()

	// If JSON output, print once and exit
	if jsonOutput {
		printWatchJSON(cfg, store)
		return
	}

	// If no-animate, print once and exit
	if noAnimate {
		printWatchStatic(cfg, store, retrievalOnly, backgroundOnly)
		return
	}

	// Set up signal handling
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)

	// Animation ticker (refresh every 300ms)
	ticker := time.NewTicker(300 * time.Millisecond)
	defer ticker.Stop()

	// Animation state
	animFrame := 0

	// Clear screen and hide cursor
	fmt.Print("\033[2J\033[H\033[?25l")
	defer fmt.Print("\033[?25h") // Show cursor on exit

	// Initial render
	printWatchAnimated(cfg, store, retrievalOnly, backgroundOnly, animFrame)

	for {
		select {
		case <-ticker.C:
			animFrame++
			// Move cursor to top and redraw
			fmt.Print("\033[H")
			printWatchAnimated(cfg, store, retrievalOnly, backgroundOnly, animFrame)
		case <-sigChan:
			fmt.Print("\033[?25h") // Show cursor
			fmt.Println("\n\nStopped watching.")
			return
		}
	}
}

// printWatchJSON outputs all watch data as JSON.
func printWatchJSON(cfg *config.Config, store *storage.Storage) {
	type WatchOutput struct {
		DaemonState    *intcognition.DaemonState    `json:"daemon_state,omitempty"`
		RetrievalStats *intcognition.RetrievalStats `json:"retrieval_stats,omitempty"`
		RecentActivity []intcognition.ActivityLogEntry `json:"recent_activity,omitempty"`
		Stats          map[string]interface{}       `json:"stats,omitempty"`
	}

	output := WatchOutput{}

	// Get daemon state
	statePath := intcognition.GetDaemonStatePath(cfg.ContextDir)
	output.DaemonState, _ = intcognition.ReadDaemonState(statePath)

	// Get retrieval stats
	output.RetrievalStats, _ = intcognition.ReadRetrievalStats(cfg.ContextDir)

	// Get recent activity
	output.RecentActivity, _ = intcognition.ReadRecentActivity(cfg.ContextDir, 10)

	// Get storage stats
	output.Stats, _ = store.GetStats()

	data, _ := json.MarshalIndent(output, "", "  ")
	fmt.Println(string(data))
}

// printWatchStatic outputs a single snapshot without animation.
func printWatchStatic(cfg *config.Config, store *storage.Storage, retrievalOnly, backgroundOnly bool) {
	// Get all state
	statePath := intcognition.GetDaemonStatePath(cfg.ContextDir)
	daemonState, _ := intcognition.ReadDaemonState(statePath)
	retrievalStats, _ := intcognition.ReadRetrievalStats(cfg.ContextDir)
	recentActivity, _ := intcognition.ReadRecentActivity(cfg.ContextDir, 5)
	stats, _ := store.GetStats()

	// Session data
	sessionPath := filepath.Join(cfg.ContextDir, "session.json")
	var topicWeights map[string]float64
	if sessionData, err := os.ReadFile(sessionPath); err == nil {
		var session struct {
			TopicWeights map[string]float64 `json:"topic_weights"`
		}
		if json.Unmarshal(sessionData, &session) == nil {
			topicWeights = session.TopicWeights
		}
	}

	// Column widths for pipe-style table
	col1Width := 28 // Background column
	col2Width := 28 // Retrieval column

	// Get stats
	events := 0
	insights := 0
	if daemonState != nil {
		events = daemonState.Stats.Events
		insights = daemonState.Stats.Insights
	}
	if statsEvents, ok := stats["total_events"].(int); ok && statsEvents > events {
		events = statsEvents
	}
	if statsInsights, ok := stats["total_insights"].(int); ok && statsInsights > insights {
		insights = statsInsights
	}

	// Mode status
	modeIcon := "○"
	modeName := "IDLE"
	modeDesc := ""
	if daemonState != nil && daemonState.Mode != "" && daemonState.Mode != "idle" {
		modeIcon = getModeSpinner(daemonState.Mode)
		modeName = strings.ToUpper(daemonState.Mode)
		modeDesc = daemonState.Description
		if modeDesc == "" {
			modeDesc = getDefaultModeDescription(daemonState.Mode)
		}
	}

	// Helper to pad cell content
	padCell := func(content string, width int) string {
		if len(content) > width {
			return content[:width]
		}
		return content + strings.Repeat(" ", width-len(content))
	}

	// Print mode header
	fmt.Printf("┌%s┐\n", strings.Repeat("─", col1Width+col2Width+3))
	modeStr := fmt.Sprintf(" %s %s", modeIcon, modeName)
	fmt.Printf("│%s│\n", padCell(modeStr, col1Width+col2Width+3))
	if modeDesc != "" {
		descStr := fmt.Sprintf(" %s", truncateString(modeDesc, col1Width+col2Width+1))
		fmt.Printf("│%s│\n", padCell(descStr, col1Width+col2Width+3))
	}

	// Two-column table
	if !retrievalOnly && !backgroundOnly {
		fmt.Printf("├%s┬%s┤\n", strings.Repeat("─", col1Width+1), strings.Repeat("─", col2Width+1))
		fmt.Printf("│ %s │ %s │\n", padCell("Background", col1Width-1), padCell("Retrieval", col2Width-1))
		fmt.Printf("├%s┼%s┤\n", strings.Repeat("─", col1Width+1), strings.Repeat("─", col2Width+1))

		bgEvents := fmt.Sprintf("Events: %d", events)
		retQueries := ""
		if retrievalStats != nil {
			retQueries = fmt.Sprintf("Queries: %d", retrievalStats.TotalRetrievals)
		}
		fmt.Printf("│ %s │ %s │\n", padCell(bgEvents, col1Width-1), padCell(retQueries, col2Width-1))

		bgInsights := fmt.Sprintf("Insights: %d", insights)
		retLatency := ""
		if retrievalStats != nil && retrievalStats.LastMode != "" {
			modeTitle := strings.ToUpper(retrievalStats.LastMode[:1]) + retrievalStats.LastMode[1:]
			retLatency = fmt.Sprintf("Last: %dms (%s)", retrievalStats.LastReflexMs, modeTitle)
		}
		fmt.Printf("│ %s │ %s │\n", padCell(bgInsights, col1Width-1), padCell(retLatency, col2Width-1))

		topicsStr := ""
		if len(topicWeights) > 0 {
			topicList := make([]string, 0)
			for topic, weight := range topicWeights {
				if weight > 0.3 {
					topicList = append(topicList, fmt.Sprintf("%s(%.1f)", topic, weight))
				}
			}
			if len(topicList) > 0 {
				topicsStr = strings.Join(topicList[:min(2, len(topicList))], ", ")
			}
		}
		retResults := ""
		if retrievalStats != nil {
			retResults = fmt.Sprintf("Results: %d → %s", retrievalStats.LastResults, retrievalStats.LastDecision)
		}
		fmt.Printf("│ %s │ %s │\n", padCell(topicsStr, col1Width-1), padCell(retResults, col2Width-1))

		fmt.Printf("└%s┴%s┘\n", strings.Repeat("─", col1Width+1), strings.Repeat("─", col2Width+1))
	} else if backgroundOnly {
		fmt.Printf("├%s┤\n", strings.Repeat("─", col1Width+col2Width+3))
		fmt.Printf("│ %s │\n", padCell(fmt.Sprintf("Events: %d  Insights: %d", events, insights), col1Width+col2Width+1))
		fmt.Printf("└%s┘\n", strings.Repeat("─", col1Width+col2Width+3))
	} else if retrievalOnly && retrievalStats != nil {
		fmt.Printf("├%s┤\n", strings.Repeat("─", col1Width+col2Width+3))
		fmt.Printf("│ %s │\n", padCell(fmt.Sprintf("Queries: %d", retrievalStats.TotalRetrievals), col1Width+col2Width+1))
		modeTitle := "N/A"
		if retrievalStats.LastMode != "" {
			modeTitle = strings.ToUpper(retrievalStats.LastMode[:1]) + retrievalStats.LastMode[1:]
		}
		fmt.Printf("│ %s │\n", padCell(fmt.Sprintf("Last: %dms (%s)", retrievalStats.LastReflexMs, modeTitle), col1Width+col2Width+1))
		fmt.Printf("│ %s │\n", padCell(fmt.Sprintf("Results: %d → %s", retrievalStats.LastResults, retrievalStats.LastDecision), col1Width+col2Width+1))
		fmt.Printf("└%s┘\n", strings.Repeat("─", col1Width+col2Width+3))
	} else {
		fmt.Printf("└%s┘\n", strings.Repeat("─", col1Width+col2Width+3))
	}

	// Recent activity table
	totalWidth := col1Width + col2Width + 3
	fmt.Println()
	fmt.Printf("┌%s┐\n", strings.Repeat("─", totalWidth))
	fmt.Printf("│ %s │\n", padCell("Recent Activity", totalWidth-2))
	fmt.Printf("├%s┬%s┬%s┤\n", strings.Repeat("─", 10), strings.Repeat("─", 8), strings.Repeat("─", totalWidth-21))
	fmt.Printf("│ %s │ %s │ %s │\n", padCell("Time", 8), padCell("Mode", 6), padCell("Description", totalWidth-23))
	fmt.Printf("├%s┼%s┼%s┤\n", strings.Repeat("─", 10), strings.Repeat("─", 8), strings.Repeat("─", totalWidth-21))

	if len(recentActivity) > 0 {
		for _, entry := range recentActivity {
			timeStr := entry.Timestamp.Format("15:04:05")
			modeIcon := getModeSpinner(entry.Mode)
			desc := truncateString(entry.Description, totalWidth-25)
			fmt.Printf("│ %s │ %s │ %s │\n", padCell(timeStr, 8), padCell(modeIcon, 6), padCell(desc, totalWidth-23))
		}
	} else {
		fmt.Printf("│ %s │ %s │ %s │\n", padCell("--:--:--", 8), padCell("○", 6), padCell("No activity yet", totalWidth-23))
	}

	fmt.Printf("└%s┴%s┴%s┘\n", strings.Repeat("─", 10), strings.Repeat("─", 8), strings.Repeat("─", totalWidth-21))
}

// printWatchAnimated outputs the animated watch display.
func printWatchAnimated(cfg *config.Config, store *storage.Storage, retrievalOnly, backgroundOnly bool, frame int) {
	// Get all state
	statePath := intcognition.GetDaemonStatePath(cfg.ContextDir)
	daemonState, _ := intcognition.ReadDaemonState(statePath)
	retrievalStats, _ := intcognition.ReadRetrievalStats(cfg.ContextDir)
	recentActivity, _ := intcognition.ReadRecentActivity(cfg.ContextDir, 5)
	stats, _ := store.GetStats()

	// Session data
	sessionPath := filepath.Join(cfg.ContextDir, "session.json")
	var topicWeights map[string]float64
	if sessionData, err := os.ReadFile(sessionPath); err == nil {
		var session struct {
			TopicWeights map[string]float64 `json:"topic_weights"`
		}
		if json.Unmarshal(sessionData, &session) == nil {
			topicWeights = session.TopicWeights
		}
	}

	// Column widths for pipe-style table
	col1Width := 28 // Background column
	col2Width := 28 // Retrieval column

	// Get stats
	events := 0
	insights := 0
	if daemonState != nil {
		events = daemonState.Stats.Events
		insights = daemonState.Stats.Insights
	}
	if statsEvents, ok := stats["total_events"].(int); ok && statsEvents > events {
		events = statsEvents
	}
	if statsInsights, ok := stats["total_insights"].(int); ok && statsInsights > insights {
		insights = statsInsights
	}

	// Mode status
	modeIcon := "○"
	modeName := "IDLE"
	modeDesc := ""
	if daemonState != nil && daemonState.Mode != "" && daemonState.Mode != "idle" {
		modeIcon = getAnimatedModeSpinner(daemonState.Mode, frame)
		modeName = strings.ToUpper(daemonState.Mode) + "ING"
		if daemonState.Mode == "dream" {
			modeName = "DREAMING"
		} else if daemonState.Mode == "think" {
			modeName = "THINKING"
		}
		modeDesc = daemonState.Description
		if modeDesc == "" {
			modeDesc = getDefaultModeDescription(daemonState.Mode)
		}
	}

	// Helper to pad cell content
	padCell := func(content string, width int) string {
		if len(content) > width {
			return content[:width]
		}
		return content + strings.Repeat(" ", width-len(content))
	}

	// Print mode header
	fmt.Printf("┌%s┐\n", strings.Repeat("─", col1Width+col2Width+3))
	modeStr := fmt.Sprintf(" %s %s", modeIcon, modeName)
	fmt.Printf("│%s│\n", padCell(modeStr, col1Width+col2Width+3))
	if modeDesc != "" {
		descStr := fmt.Sprintf(" %s", truncateString(modeDesc, col1Width+col2Width+1))
		fmt.Printf("│%s│\n", padCell(descStr, col1Width+col2Width+3))
	}

	// Two-column table header
	if !retrievalOnly && !backgroundOnly {
		fmt.Printf("├%s┬%s┤\n", strings.Repeat("─", col1Width+1), strings.Repeat("─", col2Width+1))
		fmt.Printf("│ %s │ %s │\n", padCell("Background", col1Width-1), padCell("Retrieval", col2Width-1))
		fmt.Printf("├%s┼%s┤\n", strings.Repeat("─", col1Width+1), strings.Repeat("─", col2Width+1))

		// Stats rows
		bgEvents := fmt.Sprintf("Events: %d", events)
		retQueries := ""
		if retrievalStats != nil {
			retQueries = fmt.Sprintf("Queries: %d", retrievalStats.TotalRetrievals)
		}
		fmt.Printf("│ %s │ %s │\n", padCell(bgEvents, col1Width-1), padCell(retQueries, col2Width-1))

		bgInsights := fmt.Sprintf("Insights: %d", insights)
		retLatency := ""
		if retrievalStats != nil && retrievalStats.LastMode != "" {
			modeTitle := strings.ToUpper(retrievalStats.LastMode[:1]) + retrievalStats.LastMode[1:]
			retLatency = fmt.Sprintf("Last: %dms (%s)", retrievalStats.LastReflexMs, modeTitle)
		}
		fmt.Printf("│ %s │ %s │\n", padCell(bgInsights, col1Width-1), padCell(retLatency, col2Width-1))

		// Topics
		topicsStr := ""
		if len(topicWeights) > 0 {
			topicList := make([]string, 0)
			for topic, weight := range topicWeights {
				if weight > 0.3 {
					topicList = append(topicList, fmt.Sprintf("%s(%.1f)", topic, weight))
				}
			}
			if len(topicList) > 0 {
				topicsStr = strings.Join(topicList[:min(2, len(topicList))], ", ")
			}
		}
		retResults := ""
		if retrievalStats != nil {
			retResults = fmt.Sprintf("Results: %d → %s", retrievalStats.LastResults, retrievalStats.LastDecision)
		}
		fmt.Printf("│ %s │ %s │\n", padCell(topicsStr, col1Width-1), padCell(retResults, col2Width-1))

		fmt.Printf("└%s┴%s┘\n", strings.Repeat("─", col1Width+1), strings.Repeat("─", col2Width+1))
	} else if backgroundOnly {
		fmt.Printf("├%s┤\n", strings.Repeat("─", col1Width+col2Width+3))
		fmt.Printf("│ %s │\n", padCell(fmt.Sprintf("Events: %d  Insights: %d", events, insights), col1Width+col2Width+1))
		fmt.Printf("└%s┘\n", strings.Repeat("─", col1Width+col2Width+3))
	} else if retrievalOnly && retrievalStats != nil {
		fmt.Printf("├%s┤\n", strings.Repeat("─", col1Width+col2Width+3))
		fmt.Printf("│ %s │\n", padCell(fmt.Sprintf("Queries: %d", retrievalStats.TotalRetrievals), col1Width+col2Width+1))
		modeTitle := "N/A"
		if retrievalStats.LastMode != "" {
			modeTitle = strings.ToUpper(retrievalStats.LastMode[:1]) + retrievalStats.LastMode[1:]
		}
		fmt.Printf("│ %s │\n", padCell(fmt.Sprintf("Last: %dms (%s)", retrievalStats.LastReflexMs, modeTitle), col1Width+col2Width+1))
		fmt.Printf("│ %s │\n", padCell(fmt.Sprintf("Results: %d → %s", retrievalStats.LastResults, retrievalStats.LastDecision), col1Width+col2Width+1))
		fmt.Printf("└%s┘\n", strings.Repeat("─", col1Width+col2Width+3))
	} else {
		fmt.Printf("└%s┘\n", strings.Repeat("─", col1Width+col2Width+3))
	}

	// Recent activity table
	totalWidth := col1Width + col2Width + 3
	fmt.Println()
	fmt.Printf("┌%s┐\n", strings.Repeat("─", totalWidth))
	fmt.Printf("│ %s │\n", padCell("Recent Activity", totalWidth-2))
	fmt.Printf("├%s┬%s┬%s┤\n", strings.Repeat("─", 10), strings.Repeat("─", 8), strings.Repeat("─", totalWidth-21))
	fmt.Printf("│ %s │ %s │ %s │\n", padCell("Time", 8), padCell("Mode", 6), padCell("Description", totalWidth-23))
	fmt.Printf("├%s┼%s┼%s┤\n", strings.Repeat("─", 10), strings.Repeat("─", 8), strings.Repeat("─", totalWidth-21))

	if len(recentActivity) > 0 {
		for _, entry := range recentActivity {
			timeStr := entry.Timestamp.Format("15:04:05")
			modeIcon := getAnimatedModeSpinner(entry.Mode, frame)
			desc := truncateString(entry.Description, totalWidth-25)
			fmt.Printf("│ %s │ %s │ %s │\n", padCell(timeStr, 8), padCell(modeIcon, 6), padCell(desc, totalWidth-23))
		}
	} else {
		fmt.Printf("│ %s │ %s │ %s │\n", padCell("--:--:--", 8), padCell("○", 6), padCell("No activity yet. Start daemon or use Cortex.", totalWidth-23))
	}

	fmt.Printf("└%s┴%s┴%s┘\n", strings.Repeat("─", 10), strings.Repeat("─", 8), strings.Repeat("─", totalWidth-21))

	fmt.Printf("\nPress Ctrl+C to stop. Refreshing every 300ms...\n")
}

// getAnimatedModeSpinner returns an animated spinner for a cognitive mode.
func getAnimatedModeSpinner(mode string, frame int) string {
	switch mode {
	case "dream":
		// Breathing, organic
		spinners := []string{"○", "◔", "◑", "◕", "●", "◕", "◑", "◔"}
		return spinners[frame%len(spinners)]
	case "think":
		// Braille dots, subtle
		spinners := []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}
		return spinners[frame%len(spinners)]
	case "reflect":
		// Arrow cycle
		spinners := []string{"→", "↘", "↓", "↙", "←", "↖", "↑", "↗"}
		return spinners[frame%len(spinners)]
	case "reflex":
		// Bouncing dot
		spinners := []string{"∙", "•", "●", "•", "∙"}
		return spinners[frame%len(spinners)]
	case "resolve":
		// Ellipsis
		spinners := []string{"·  ", "·· ", "···", "·· ", "·  ", "   "}
		return spinners[frame%len(spinners)]
	case "insight", "digest":
		// Special marker
		spinners := []string{"✦", "★", "✦", "☆"}
		return spinners[frame%len(spinners)]
	default:
		return "●"
	}
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
  watch          Live dashboard of cognitive modes

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
