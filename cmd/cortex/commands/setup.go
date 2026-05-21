// Package commands provides CLI command implementations.
package commands

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	intllm "github.com/dereksantos/cortex/internal/llm"
	"github.com/dereksantos/cortex/internal/storage"
	"github.com/dereksantos/cortex/pkg/config"
	"github.com/dereksantos/cortex/pkg/llm"
	projreg "github.com/dereksantos/cortex/pkg/registry"
)

// InitCommand implements the init functionality.
type InitCommand struct{}

// InstallCommand implements the install functionality.
type InstallCommand struct{}

// UninstallCommand implements the uninstall functionality.
type UninstallCommand struct{}

// ProjectsCommand lists registered projects.
type ProjectsCommand struct{}

func init() {
	Register(&InitCommand{})
	Register(&InstallCommand{})
	Register(&UninstallCommand{})
	Register(&ProjectsCommand{})
}

// Name returns the command name.
func (c *InitCommand) Name() string { return "init" }

// Description returns the command description.
func (c *InitCommand) Description() string { return "Initialize Cortex in the current directory" }

// DescribeFlags surfaces init's flag set into tools.json.
func (c *InitCommand) DescribeFlags(fs *flag.FlagSet) {
	fs.Bool("auto", false, "Detect local LLM availability and print setup tips")
}

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
			fmt.Println("  --auto    Detect local LLM availability and print setup tips")
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

	// Register project in global registry (~/.cortex/projects.json)
	reg, err := projreg.Open()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Warning: could not open global registry: %v\n", err)
	} else {
		entry, err := reg.Register(projectRoot)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Warning: could not register project: %v\n", err)
		} else {
			cfg.ProjectID = entry.ID
			// Re-save config with project ID
			_ = cfg.Save(configPath)
			fmt.Printf("   Registered as project %q in ~/.cortex/\n", entry.ID)
		}
	}

	// Add .cortex/ to .gitignore if it exists
	ensureGitignore(projectRoot)

	// Create .cortex/.gitignore for committed/local split
	ensureCortexGitignore(cfg.ContextDir)

	fmt.Printf("Initialized Cortex at %s/.cortex/\n", projectRoot)

	if autoSetup {
		runAutoSetup(projectRoot)
	}

	return nil
}

// Name returns the command name.
func (c *InstallCommand) Name() string { return "install" }

// Description returns the command description.
func (c *InstallCommand) Description() string {
	return "Ensure .cortex/ is initialized and report local LLM availability"
}

// Execute runs the install command.
//
// Under D11 the Claude-Code hook + slash-command wiring is gone, so
// install is now a thin convenience over init: ensure the project is
// registered and surface the local LLM state. Keeping the verb so
// existing release/automation scripts don't break.
func (c *InstallCommand) Execute(ctx *Context) error {
	for _, arg := range ctx.Args {
		if arg == "-h" || arg == "--help" {
			fmt.Println("Usage: cortex install")
			fmt.Println("\nEnsures .cortex/ exists for the current project and reports")
			fmt.Println("local LLM availability. No external editor / hook wiring is done")
			fmt.Println("(cortex is its own harness; see docs/learning-harness.md).")
			return nil
		}
	}

	projectRoot, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("failed to get working directory: %w", err)
	}

	fmt.Println("Installing Cortex...")
	fmt.Println()

	contextDir := filepath.Join(projectRoot, ".cortex")
	if err := os.MkdirAll(contextDir, 0755); err != nil {
		return fmt.Errorf("failed to create .cortex directory: %w", err)
	}

	reg, err := projreg.Open()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Warning: could not open global registry: %v\n", err)
	} else {
		entry, regErr := reg.Register(projectRoot)
		if regErr != nil {
			fmt.Fprintf(os.Stderr, "Warning: could not register project: %v\n", regErr)
		} else {
			fmt.Printf("Registered project %q in ~/.cortex/\n", entry.ID)
		}
	}

	ensureGitignore(projectRoot)
	ensureCortexGitignore(contextDir)

	fmt.Println()
	fmt.Println("Checking LLM availability...")
	llmStatus := intllm.DetectLLM()

	switch {
	case llmStatus.Available && llmStatus.Provider == "ollama":
		fmt.Printf("Ollama installed at %s\n", llmStatus.OllamaPath)
		fmt.Printf("Model %s available (recommended for Cortex)\n", llmStatus.Model)
	case llmStatus.Available && llmStatus.Provider == "anthropic":
		fmt.Println("Anthropic API key configured")
		if llmStatus.OllamaInstalled {
			fmt.Printf("Ollama also installed at %s\n", llmStatus.OllamaPath)
			if len(llmStatus.OllamaModels) > 0 {
				fmt.Printf("Ollama models available: %s\n", strings.Join(llmStatus.OllamaModels, ", "))
			}
		}
	case llmStatus.OllamaInstalled:
		fmt.Printf("Ollama installed at %s\n", llmStatus.OllamaPath)
		fmt.Println("No suitable model found")
		fmt.Println()
		fmt.Println("Cortex works best with a local model for background processing.")
		fmt.Println("Install one with:")
		fmt.Println("  ollama pull qwen2.5:3b    (3GB, recommended)")
		fmt.Println("  ollama pull qwen2.5:0.5b  (500MB, lightweight)")
		fmt.Println()
		fmt.Println("Or set ANTHROPIC_API_KEY for Claude API usage.")
	default:
		fmt.Println("No local LLM found")
		fmt.Println()
		fmt.Println("For full functionality, install Ollama:")
		fmt.Println("  brew install ollama && ollama pull qwen2.5:3b")
		fmt.Println()
		fmt.Println("Or set ANTHROPIC_API_KEY for Claude API usage.")
	}

	if !llmStatus.Available {
		fmt.Println()
		fmt.Println("Without an LLM, Cortex runs in mechanical-only mode (Reflex).")
	}

	fmt.Println()
	fmt.Println("Install complete. Start the daemon with: cortex daemon")

	return nil
}

// Name returns the command name.
func (c *UninstallCommand) Name() string { return "uninstall" }

// Description returns the command description.
func (c *UninstallCommand) Description() string {
	return "Remove the current project's .cortex/ data (--purge required to delete)"
}

// DescribeFlags surfaces uninstall's flag set into tools.json.
func (c *UninstallCommand) DescribeFlags(fs *flag.FlagSet) {
	fs.Bool("purge", false, "Remove .cortex/ directory and all captured data")
}

// Execute runs the uninstall command.
//
// Under D11 the Claude-Code hook / slash-command teardown is gone, so
// uninstall only manages the .cortex/ data dir. Without --purge it
// reports the state and exits without touching anything.
func (c *UninstallCommand) Execute(ctx *Context) error {
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

	projectRoot, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("failed to get working directory: %w", err)
	}

	fmt.Println("Uninstalling Cortex...")
	fmt.Println()

	contextDir := filepath.Join(projectRoot, ".cortex")
	if _, err := os.Stat(contextDir); err != nil {
		fmt.Println("Nothing to uninstall (.cortex/ not present).")
		return nil
	}

	if !purge {
		fmt.Println("Kept .cortex/ data (use --purge to remove).")
		return nil
	}

	eventCount, insightCount := countContextData(contextDir)
	if err := os.RemoveAll(contextDir); err != nil {
		fmt.Printf("Warning: Could not remove .cortex/: %v\n", err)
		return nil
	}
	if eventCount > 0 || insightCount > 0 {
		fmt.Printf("Removed .cortex/ directory (%d events, %d insights deleted)\n", eventCount, insightCount)
	} else {
		fmt.Println("Removed .cortex/ directory")
	}
	fmt.Println()
	fmt.Println("Cortex has been completely removed from this project.")

	return nil
}

// Name returns the command name.
func (c *ProjectsCommand) Name() string { return "projects" }

// Description returns the command description.
func (c *ProjectsCommand) Description() string { return "List registered projects" }

// Execute runs the projects command.
func (c *ProjectsCommand) Execute(ctx *Context) error {
	for _, arg := range ctx.Args {
		if arg == "-h" || arg == "--help" {
			fmt.Println("Usage: cortex projects")
			fmt.Println("\nList all projects registered in ~/.cortex/projects.json")
			return nil
		}
	}

	reg, err := projreg.Open()
	if err != nil {
		return fmt.Errorf("failed to open registry: %w", err)
	}

	projects := reg.List()
	if len(projects) == 0 {
		fmt.Println("No projects registered.")
		fmt.Println("Run 'cortex init' in a project directory to register it.")
		return nil
	}

	fmt.Printf("Registered projects (%d):\n\n", len(projects))
	for _, p := range projects {
		status := "  "
		// Check if project has a local queue with pending events
		queueDir := filepath.Join(p.Path, ".cortex", "queue", "pending")
		if entries, err := os.ReadDir(queueDir); err == nil && len(entries) > 0 {
			status = "● "
		}
		fmt.Printf("  %s%-16s  %s\n", status, p.ID, p.Path)
		if p.GitRemote != "" {
			fmt.Printf("    %-16s  %s\n", "", p.GitRemote)
		}
	}
	fmt.Println()

	return nil
}

// --- Helper functions ---

// runAutoSetup is the `cortex init --auto` tail: probe local LLM state
// and print install tips. The Claude-Code branch was dropped under D11.
//
// allowlist:llm.NewOllamaClient — first-run probe is intentionally
// Ollama-only ("does Ollama exist on this machine?"); not a runtime
// provider selection. The provider-resolution guard test exempts this.
func runAutoSetup(_ string) {
	fmt.Println("\nChecking Ollama...")
	client := llm.NewOllamaClient(config.Default())
	if client.IsAvailable() {
		fmt.Println("   Ollama is running")

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
	fmt.Println("   1. Start the daemon: cortex daemon")
	fmt.Println("   2. Capture context: cortex capture --type=decision --content=\"...\"")
	fmt.Println("   3. Search: cortex search \"query\"")
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

// countContextData counts events and insights in the context directory.
func countContextData(contextDir string) (events int, insights int) {
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
