// Package commands provides CLI command implementations.
package commands

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	intllm "github.com/dereksantos/cortex/internal/llm"
	"github.com/dereksantos/cortex/internal/storage"
	"github.com/dereksantos/cortex/pkg/config"
	"github.com/dereksantos/cortex/pkg/llm"
)

// InitCommand implements the init functionality.
type InitCommand struct{}

// InstallCommand implements the install functionality.
type InstallCommand struct{}

// UninstallCommand implements the uninstall functionality.
type UninstallCommand struct{}

func init() {
	Register(&InitCommand{})
	Register(&InstallCommand{})
	Register(&UninstallCommand{})
}

// Name returns the command name.
func (c *InitCommand) Name() string { return "init" }

// Description returns the command description.
func (c *InitCommand) Description() string { return "Initialize Cortex in the current directory" }

// Execute runs the init command.
func (c *InitCommand) Execute(ctx *Context) error {
	// Check for --auto flag
	autoSetup := false
	for _, arg := range ctx.Args {
		if arg == "--auto" {
			autoSetup = true
		}
		if arg == "-h" || arg == "--help" {
			fmt.Println("Usage: cortex init [flags]")
			fmt.Println("\nFlags:")
			fmt.Println("  --auto    Auto-detect and configure AI tools")
			fmt.Println("  -h, --help    Show this help message")
			return nil
		}
	}

	// Get project root
	projectRoot, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("failed to get working directory: %w", err)
	}

	// Create default config
	cfg := config.Default()
	cfg.ProjectRoot = projectRoot

	// Ensure directories
	if err := cfg.EnsureDirectories(); err != nil {
		return fmt.Errorf("failed to create directories: %w", err)
	}

	// Save config
	configPath := fmt.Sprintf("%s/.cortex/config.json", projectRoot)
	if err := cfg.Save(configPath); err != nil {
		return fmt.Errorf("failed to save config: %w", err)
	}

	// Add .cortex/ to .gitignore if it exists
	ensureGitignore(projectRoot)

	// Create .cortex/.gitignore for committed/local split
	ensureCortexGitignore(cfg.ContextDir)

	fmt.Println("Cortex initialized successfully!")
	fmt.Printf("   Config: %s\n", configPath)
	fmt.Printf("   Context directory: %s\n", cfg.ContextDir)

	if autoSetup {
		fmt.Println("\nAuto-detecting environment...")
		runAutoSetup(projectRoot)
	} else {
		fmt.Println("\nNext steps:")
		fmt.Println("   1. Configure your AI tool to use: cortex capture")
		fmt.Println("   2. Start the processor: cortex daemon")
		fmt.Println("   3. Search your context: cortex search <query>")
		fmt.Println("\nTip: Run 'cortex init --auto' for automatic setup")
	}

	return nil
}

// Name returns the command name.
func (c *InstallCommand) Name() string { return "install" }

// Description returns the command description.
func (c *InstallCommand) Description() string { return "Install Cortex hooks for Claude Code" }

// Execute runs the install command.
func (c *InstallCommand) Execute(ctx *Context) error {
	for _, arg := range ctx.Args {
		if arg == "-h" || arg == "--help" {
			fmt.Println("Usage: cortex install")
			fmt.Println("\nInstalls Cortex hooks and slash commands for Claude Code.")
			fmt.Println("This sets up automatic context capture during Claude Code sessions.")
			return nil
		}
	}

	// Get project root and home directory
	projectRoot, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("failed to get working directory: %w", err)
	}

	homeDir, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("failed to get home directory: %w", err)
	}

	fmt.Println("Installing Cortex for Claude Code...")
	fmt.Println()

	// 1. Detect Claude Code
	claudeHomeDir := filepath.Join(homeDir, ".claude")
	claudeProjectDir := filepath.Join(projectRoot, ".claude")

	if _, err := os.Stat(claudeHomeDir); err != nil {
		fmt.Println("Claude Code not detected at ~/.claude/")
		fmt.Println("Install Claude Code first: https://claude.ai/claude-code")
		return fmt.Errorf("claude code not installed")
	}

	fmt.Printf("Detected Claude Code at %s\n", claudeHomeDir)

	// 2. Ensure .cortex/ directory exists
	contextDir := filepath.Join(projectRoot, ".cortex")
	if err := os.MkdirAll(contextDir, 0755); err != nil {
		return fmt.Errorf("failed to create .cortex directory: %w", err)
	}

	// 3. Ensure .claude/ directory exists in project
	if err := os.MkdirAll(claudeProjectDir, 0755); err != nil {
		return fmt.Errorf("failed to create .claude directory: %w", err)
	}

	// 4. Create/merge settings.local.json with hooks
	settingsPath := filepath.Join(claudeProjectDir, "settings.local.json")
	if err := createClaudeSettings(settingsPath); err != nil {
		return fmt.Errorf("failed to create settings: %w", err)
	}
	fmt.Printf("Created %s with hooks\n", settingsPath)

	// 4b. Ensure .cortex/ is in .gitignore
	ensureGitignore(projectRoot)

	// 4c. Create .cortex/.gitignore for committed/local split
	ensureCortexGitignore(contextDir)

	// 5. Create slash command
	commandsDir := filepath.Join(claudeProjectDir, "commands")
	if err := os.MkdirAll(commandsDir, 0755); err != nil {
		return fmt.Errorf("failed to create commands directory: %w", err)
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
allowed-tools: Bash(cortex:*)
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
allowed-tools: Bash(cortex:*)
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
allowed-tools: Bash(cortex:*)
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
allowed-tools: Bash(cortex:*)
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
		switch llmStatus.Provider {
		case "ollama":
			fmt.Printf("Ollama installed at %s\n", llmStatus.OllamaPath)
			fmt.Printf("Model %s available (recommended for Cortex)\n", llmStatus.Model)
		case "anthropic":
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

	return nil
}

// Name returns the command name.
func (c *UninstallCommand) Name() string { return "uninstall" }

// Description returns the command description.
func (c *UninstallCommand) Description() string { return "Remove Cortex hooks from Claude Code" }

// Execute runs the uninstall command.
func (c *UninstallCommand) Execute(ctx *Context) error {
	// Parse flags
	purge := false
	for _, arg := range ctx.Args {
		if arg == "--purge" {
			purge = true
		}
		if arg == "-h" || arg == "--help" {
			fmt.Println("Usage: cortex uninstall [flags]")
			fmt.Println("\nFlags:")
			fmt.Println("  --purge       Remove .cortex/ directory and all captured data")
			fmt.Println("  -h, --help    Show this help message")
			return nil
		}
	}

	// Get project root
	projectRoot, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("failed to get working directory: %w", err)
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

	// 2. Remove .claude/commands/cortex*.md files
	commandsDir := filepath.Join(claudeProjectDir, "commands")
	cortexCommands := []string{
		"cortex.md",
		"cortex-recall.md",
		"cortex-decide.md",
		"cortex-correct.md",
		"cortex-forget.md",
	}

	for _, cmdFile := range cortexCommands {
		cmdPath := filepath.Join(commandsDir, cmdFile)
		if _, err := os.Stat(cmdPath); err == nil {
			if err := os.Remove(cmdPath); err != nil {
				fmt.Printf("Warning: Could not remove %s: %v\n", cmdFile, err)
			} else {
				fmt.Printf("Removed .claude/commands/%s\n", cmdFile)
				removedSomething = true
			}
		}
	}

	// Try to remove commands directory if empty
	if isEmpty, _ := isDirEmpty(commandsDir); isEmpty {
		os.Remove(commandsDir)
	}

	// Try to remove .claude directory if empty (only if we created it)
	if isEmpty, _ := isDirEmpty(claudeProjectDir); isEmpty {
		os.Remove(claudeProjectDir)
	}

	// 3. Handle .cortex/ directory
	contextDir := filepath.Join(projectRoot, ".cortex")
	if _, err := os.Stat(contextDir); err == nil {
		if purge {
			// Count events and insights before removal
			eventCount, insightCount := countContextData(contextDir)

			if err := os.RemoveAll(contextDir); err != nil {
				fmt.Printf("Warning: Could not remove .cortex/: %v\n", err)
			} else {
				if eventCount > 0 || insightCount > 0 {
					fmt.Printf("Removed .cortex/ directory (%d events, %d insights deleted)\n", eventCount, insightCount)
				} else {
					fmt.Println("Removed .cortex/ directory")
				}
				removedSomething = true
			}
		} else {
			fmt.Println("Kept .cortex/ data (use --purge to remove)")
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

	return nil
}

// --- Helper functions ---

func runAutoSetup(projectRoot string) {
	// Get absolute path to cortex binary
	cortexPath, err := os.Executable()
	if err != nil {
		cortexPath = fmt.Sprintf("%s/cortex", projectRoot)
	}

	// Detect Claude Code
	claudeDir := fmt.Sprintf("%s/.claude", projectRoot)
	if _, err := os.Stat(claudeDir); err == nil {
		fmt.Println("\nDetected Claude Code")
		if err := setupClaudeCode(claudeDir, cortexPath); err != nil {
			fmt.Printf("   Warning: Failed to configure Claude Code: %v\n", err)
		} else {
			fmt.Println("   Configured hooks in .claude/settings.local.json")
		}
	} else {
		fmt.Println("\nClaude Code not detected (.claude directory not found)")
	}

	// Detect Ollama
	fmt.Println("\nChecking Ollama...")
	client := llm.NewOllamaClient(config.Default())
	if client.IsAvailable() {
		fmt.Println("   Ollama is running")

		// Check for model
		if client.IsModelAvailable() {
			fmt.Printf("   Model '%s' is available\n", config.Default().OllamaModel)
		} else {
			fmt.Printf("   Warning: Model '%s' not found\n", config.Default().OllamaModel)
			fmt.Println("   Tip: Run: ollama pull mistral:7b")
		}
	} else {
		fmt.Println("   Ollama is not running")
		fmt.Println("   Tip: Install from: https://ollama.ai")
	}

	fmt.Println("\nAuto-setup complete!")
	fmt.Println("\nNext steps:")
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

	// Check if .cortex/ is already ignored
	if strings.Contains(gitignoreContent, ".cortex/") || strings.Contains(gitignoreContent, ".cortex") {
		// Already in gitignore
		return
	}

	// Append .cortex/ to gitignore
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

	// Add .cortex/ with comment
	f.WriteString("\n# Cortex context memory (local development context)\n.cortex/\n")

	fmt.Println("   Added .cortex/ to .gitignore")
}

func ensureCortexGitignore(contextDir string) {
	gitignorePath := filepath.Join(contextDir, ".gitignore")

	// Don't overwrite if it already exists
	if _, err := os.Stat(gitignorePath); err == nil {
		return
	}

	content := `# Ignore everything except team-shared knowledge
*
!.gitignore
!knowledge/
!knowledge/**
`
	if err := os.WriteFile(gitignorePath, []byte(content), 0644); err != nil {
		// Silent failure - not critical
		return
	}

	fmt.Println("   Created .cortex/.gitignore (knowledge/ will be committed, everything else ignored)")
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
		"PostToolUse": []interface{}{
			map[string]interface{}{
				"matcher": "Write|Edit|Bash|Read|Grep|Glob",
				"hooks": []interface{}{
					map[string]interface{}{
						"type":    "command",
						"command": fmt.Sprintf("%s capture", cortexPath),
					},
				},
			},
		},
		"PreToolUse": []interface{}{
			map[string]interface{}{
				"matcher": "Write|Edit",
				"hooks": []interface{}{
					map[string]interface{}{
						"type":    "command",
						"command": fmt.Sprintf("%s inject-context --pre-tool", cortexPath),
					},
				},
			},
		},
		"Stop": []interface{}{
			map[string]interface{}{
				"hooks": []interface{}{
					map[string]interface{}{
						"type":    "command",
						"command": fmt.Sprintf("%s stop", cortexPath),
					},
				},
			},
		},
		"Notification": []interface{}{
			map[string]interface{}{
				"hooks": []interface{}{
					map[string]interface{}{
						"type":    "command",
						"command": fmt.Sprintf("%s capture --notification", cortexPath),
					},
				},
			},
		},
		"SubagentComplete": []interface{}{
			map[string]interface{}{
				"hooks": []interface{}{
					map[string]interface{}{
						"type":    "command",
						"command": fmt.Sprintf("%s capture --subagent", cortexPath),
					},
				},
			},
		},
	}

	// Configure status line, composing with any existing non-cortex statusLine
	statusLine := map[string]interface{}{
		"type":    "command",
		"command": fmt.Sprintf("%s status --format=claude", cortexPath),
	}

	if existing, ok := settings["statusLine"].(map[string]interface{}); ok {
		if cmd, ok := existing["command"].(string); ok {
			if !strings.Contains(cmd, "cortex") {
				composed := fmt.Sprintf(
					`bash -c 'input=$(cat); existing=$(echo "$input" | %s); cortex=$(%s status --format=claude < /dev/null); echo "${existing} | ${cortex}"'`,
					cmd, cortexPath,
				)
				statusLine["command"] = composed
			}
		}
	}

	settings["hooks"] = hooks
	settings["statusLine"] = statusLine

	// Configure MCP server
	settings["mcpServers"] = map[string]interface{}{
		"cortex": map[string]interface{}{
			"command": cortexPath,
			"args":    []string{"mcp"},
		},
	}

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
		fmt.Printf("   Warning: Could not create slash command: %v\n", err)
	} else {
		fmt.Println("   Created /cortex slash command")
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
- /cortex -> Shows: 47 events, 12 insights
- /cortex search authentication -> Find auth decisions
- /cortex insights -> List recent insights
- /cortex how did we handle errors -> Smart search

---

%s cli "$@"
`, cortexPath)

	// Write command file
	if err := os.WriteFile(commandFile, []byte(content), 0644); err != nil {
		return fmt.Errorf("failed to write command file: %w", err)
	}

	return nil
}

func createPluginJSON(pluginDir string) error {
	pluginJSON := `{
  "name": "cortex",
  "description": "Reactive context engine with budget-bounded cognitive modes for AI coding assistants",
  "version": "0.2.0",
  "author": {
    "name": "Cortex"
  },
  "repository": "https://github.com/dereksantos/cortex",
  "license": "MIT",
  "hooks": {
    "SessionStart": [{"hooks": [{"type": "command", "command": "cortex session-start"}]}],
    "PreToolUse": [{"matcher": "Write|Edit", "hooks": [{"type": "command", "command": "cortex inject-context --pre-tool"}]}],
    "UserPromptSubmit": [{"hooks": [{"type": "command", "command": "cortex inject-context"}]}],
    "PostToolUse": [{"matcher": "Write|Edit|Bash|Read|Grep|Glob", "hooks": [{"type": "command", "command": "cortex capture"}]}],
    "Stop": [{"hooks": [{"type": "command", "command": "cortex stop"}]}],
    "Notification": [{"hooks": [{"type": "command", "command": "cortex capture --notification"}]}],
  },
  "commands": [
    {"name": "cortex", "description": "Query Cortex context memory"},
    {"name": "cortex-recall", "description": "Recall what Cortex knows about a topic"},
    {"name": "cortex-decide", "description": "Record an architectural decision"},
    {"name": "cortex-correct", "description": "Record a correction"},
    {"name": "cortex-forget", "description": "Mark context as outdated"}
  ],
  "mcpServers": {
    "cortex": {
      "command": "cortex",
      "args": ["mcp"]
    }
  }
}`

	pluginPath := filepath.Join(pluginDir, "plugin.json")
	if _, err := os.Stat(pluginPath); err == nil {
		// File exists, don't overwrite
		return nil
	}

	return os.WriteFile(pluginPath, []byte(pluginJSON), 0644)
}

// cortexBinPath returns the absolute path to the current cortex binary.
func cortexBinPath() string {
	exe, err := os.Executable()
	if err == nil {
		exe, err = filepath.EvalSymlinks(exe)
		if err == nil {
			return exe
		}
	}
	return "cortex"
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

	// Resolve cortex binary path for hooks
	cortexPath := cortexBinPath()

	// Configure hooks (preserve existing ones if needed)
	hooks := map[string]interface{}{
		"SessionStart": []interface{}{
			map[string]interface{}{
				"hooks": []interface{}{
					map[string]interface{}{
						"type":    "command",
						"command": cortexPath + " session-start",
					},
				},
			},
		},
		"UserPromptSubmit": []interface{}{
			map[string]interface{}{
				"hooks": []interface{}{
					map[string]interface{}{
						"type":    "command",
						"command": cortexPath + " inject-context",
					},
				},
			},
		},
		"PostToolUse": []interface{}{
			map[string]interface{}{
				"matcher": "Write|Edit|Bash|Read|Grep|Glob",
				"hooks": []interface{}{
					map[string]interface{}{
						"type":    "command",
						"command": cortexPath + " capture",
					},
				},
			},
		},
		"PreToolUse": []interface{}{
			map[string]interface{}{
				"matcher": "Write|Edit",
				"hooks": []interface{}{
					map[string]interface{}{
						"type":    "command",
						"command": cortexPath + " inject-context --pre-tool",
					},
				},
			},
		},
		"Stop": []interface{}{
			map[string]interface{}{
				"hooks": []interface{}{
					map[string]interface{}{
						"type":    "command",
						"command": cortexPath + " stop",
					},
				},
			},
		},
		"Notification": []interface{}{
			map[string]interface{}{
				"hooks": []interface{}{
					map[string]interface{}{
						"type":    "command",
						"command": cortexPath + " capture --notification",
					},
				},
			},
		},
	}

	// Configure status line, composing with any existing non-cortex statusLine
	statusLine := map[string]interface{}{
		"type":    "command",
		"command": cortexPath + " status --format=claude",
	}

	if existing, ok := settings["statusLine"].(map[string]interface{}); ok {
		if cmd, ok := existing["command"].(string); ok {
			if !strings.Contains(cmd, "cortex") {
				composed := fmt.Sprintf(
					`bash -c 'input=$(cat); existing=$(echo "$input" | %s); cortex=$(%s status --format=claude < /dev/null); echo "${existing} | ${cortex}"'`,
					cmd, cortexPath,
				)
				statusLine["command"] = composed
			}
		}
	}

	settings["hooks"] = hooks
	settings["statusLine"] = statusLine

	// Configure MCP server
	settings["mcpServers"] = map[string]interface{}{
		"cortex": map[string]interface{}{
			"command": cortexPath,
			"args":    []string{"mcp"},
		},
	}

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
allowed-tools: Bash(cortex:*)
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

	// Remove hooks that contain "cortex" commands
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

	// Remove statusLine if it's a Cortex command, or unwrap if composed
	if statusLine, ok := settings["statusLine"].(map[string]interface{}); ok {
		if cmd, ok := statusLine["command"].(string); ok {
			if strings.Contains(cmd, "cortex") {
				// Check if this is a composed wrapper with an original command
				if strings.HasPrefix(cmd, "bash -c") && strings.Contains(cmd, `echo "$input" |`) {
					// Extract original command: between 'echo "$input" | ' and '); cortex=$('
					start := strings.Index(cmd, `echo "$input" | `)
					end := strings.Index(cmd, `); cortex=$(`)
					if start != -1 && end != -1 {
						start += len(`echo "$input" | `)
						original := cmd[start:end]
						statusLine["command"] = original
						settings["statusLine"] = statusLine
						modified = true
					} else {
						delete(settings, "statusLine")
						modified = true
					}
				} else {
					delete(settings, "statusLine")
					modified = true
				}
			}
		}
	}

	// Remove mcpServers.cortex
	if mcpServers, ok := settings["mcpServers"].(map[string]interface{}); ok {
		if _, ok := mcpServers["cortex"]; ok {
			delete(mcpServers, "cortex")
			modified = true
			if len(mcpServers) == 0 {
				delete(settings, "mcpServers")
			} else {
				settings["mcpServers"] = mcpServers
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

// cleanCortexHooks removes hook entries that contain "cortex" or "cortex" commands
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
