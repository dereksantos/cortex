// Cortex - Context memory for AI development
package main

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/dereksantos/cortex/cmd/cortex/commands"
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
	case "capture", "ingest", "analyze", "process", "feed":
		if cmd := commands.Get(command); cmd != nil {
			ctx := &commands.Context{
				Args: os.Args[2:],
			}
			if err := cmd.Execute(ctx); err != nil {
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				os.Exit(1)
			}
		}
	case "init", "install", "uninstall":
		if cmd := commands.Get(command); cmd != nil {
			ctx := &commands.Context{
				Args: os.Args[2:],
			}
			if err := cmd.Execute(ctx); err != nil {
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				os.Exit(1)
			}
		}
	case "daemon":
		if cmd := commands.Get("daemon"); cmd != nil {
			cfg, err := loadConfig()
			if err != nil {
				fmt.Fprintf(os.Stderr, "Cortex not initialized. Run 'cortex init' first.\n")
				os.Exit(1)
			}
			store, err := storage.New(cfg)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Failed to open storage: %v\n", err)
				os.Exit(1)
			}
			defer store.Close()
			ctx := &commands.Context{
				Config:  cfg,
				Storage: store,
				Args:    os.Args[2:],
			}
			if err := cmd.Execute(ctx); err != nil {
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				os.Exit(1)
			}
		}
	case "info", "test", "stats", "status", "forget", "overview":
		if cmd := commands.Get(command); cmd != nil {
			cfg, err := loadConfig()
			if err != nil {
				// For info and status, we can proceed without config
				if command == "info" || command == "status" {
					ctx := &commands.Context{
						Args: os.Args[2:],
					}
					if err := cmd.Execute(ctx); err != nil {
						fmt.Fprintf(os.Stderr, "Error: %v\n", err)
						os.Exit(1)
					}
					return
				}
				fmt.Fprintf(os.Stderr, "Cortex not initialized. Run 'cortex init' first.\n")
				os.Exit(1)
			}
			store, err := storage.New(cfg)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Failed to open storage: %v\n", err)
				os.Exit(1)
			}
			defer store.Close()
			ctx := &commands.Context{
				Config:  cfg,
				Storage: store,
				Args:    os.Args[2:],
			}
			if err := cmd.Execute(ctx); err != nil {
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				os.Exit(1)
			}
		}
	case "eval":
		if cmd := commands.Get("eval"); cmd != nil {
			ctx := &commands.Context{
				Args: os.Args[2:],
			}
			if err := cmd.Execute(ctx); err != nil {
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				os.Exit(1)
			}
		}
	case "watch":
		if cmd := commands.Get("watch"); cmd != nil {
			cfg, err := loadConfig()
			if err != nil {
				fmt.Fprintf(os.Stderr, "Cortex not initialized. Run 'cortex init' first.\n")
				os.Exit(1)
			}
			store, err := storage.New(cfg)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Failed to open storage: %v\n", err)
				os.Exit(1)
			}
			defer store.Close()
			ctx := &commands.Context{
				Config:  cfg,
				Storage: store,
				Args:    os.Args[2:],
			}
			if err := cmd.Execute(ctx); err != nil {
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				os.Exit(1)
			}
		}
	case "mcp":
		if cmd := commands.Get("mcp"); cmd != nil {
			cfg, err := loadConfig()
			if err != nil {
				fmt.Fprintf(os.Stderr, "Cortex not initialized. Run 'cortex init' first.\n")
				os.Exit(1)
			}
			store, err := storage.New(cfg)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Failed to open storage: %v\n", err)
				os.Exit(1)
			}
			defer store.Close()
			ctx := &commands.Context{
				Config:  cfg,
				Storage: store,
				Args:    os.Args[2:],
			}
			if err := cmd.Execute(ctx); err != nil {
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				os.Exit(1)
			}
		}
	case "search", "recent", "insights", "entities", "graph", "prune", "reembed":
		if cmd := commands.Get(command); cmd != nil {
			cfg, err := loadConfig()
			if err != nil {
				fmt.Fprintf(os.Stderr, "Cortex not initialized. Run 'cortex init' first.\n")
				os.Exit(1)
			}
			// Auto-start daemon on search/insights (covers CLI-only multi-agent usage)
			if command == "search" || command == "insights" {
				maybeStartDaemon(cfg)
			}
			store, err := storage.New(cfg)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Failed to open storage: %v\n", err)
				os.Exit(1)
			}
			defer store.Close()
			ctx := &commands.Context{
				Config:  cfg,
				Storage: store,
				Args:    os.Args[2:],
			}
			if err := cmd.Execute(ctx); err != nil {
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				os.Exit(1)
			}
		}
	case "session-start", "inject-context", "stop", "cli":
		if cmd := commands.Get(command); cmd != nil {
			cfg, err := loadConfig()
			if err != nil {
				// For session commands, log error but don't block user
				if command == "inject-context" || command == "stop" {
					fmt.Fprintf(os.Stderr, "cortex %s: config error: %v\n", command, err)
					os.Exit(0)
				}
				fmt.Fprintf(os.Stderr, "Cortex not initialized. Run 'cortex init' first.\n")
				os.Exit(1)
			}
			// Auto-start daemon on session-start (beginning of every session)
			if command == "session-start" {
				maybeStartDaemon(cfg)
			}
			store, err := storage.New(cfg)
			if err != nil {
				if command == "inject-context" || command == "stop" {
					fmt.Fprintf(os.Stderr, "cortex %s: storage error: %v\n", command, err)
					os.Exit(0)
				}
				fmt.Fprintf(os.Stderr, "Failed to open storage: %v\n", err)
				os.Exit(1)
			}
			defer store.Close()
			ctx := &commands.Context{
				Config:  cfg,
				Storage: store,
				Args:    os.Args[2:],
			}
			if err := cmd.Execute(ctx); err != nil {
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				os.Exit(1)
			}
		}
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

func loadConfig() (*config.Config, error) {
	projectRoot, err := os.Getwd()
	if err != nil {
		return nil, err
	}

	configPath := fmt.Sprintf("%s/.cortex/config.json", projectRoot)
	return config.Load(configPath)
}

// maybeStartDaemon auto-starts the daemon if it's not running.
// Fire-and-forget: never blocks the caller, never fails the caller.
// Writes to stderr only so it doesn't pollute hook stdout.
func maybeStartDaemon(cfg *config.Config) {
	if cfg == nil {
		return
	}
	if commands.IsDaemonRunning(cfg.ContextDir) {
		return
	}
	pid, err := commands.StartDaemonBackground(cfg.ContextDir)
	if err != nil {
		// Already running or can't start — either way, not our problem
		return
	}
	fmt.Fprintf(os.Stderr, "cortex: auto-started daemon (pid %d)\n", pid)
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

// truncateString truncates a string to max length with ellipsis.
func truncateString(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen-3] + "..."
}

func printUsage() {
	fmt.Printf(`Cortex %s - Context memory for AI development

Usage:
  cortex <command> [options]

Commands:
  init           Initialize Cortex in current directory
  install        Install Cortex hooks for Claude Code
  uninstall      Remove Cortex hooks (--purge to also delete .cortex/)
  info           Show system info and model recommendations
  test           Test LLM analysis [decision|pattern|insight]

  capture        Capture event from stdin (used by AI tools)
  ingest         Move queued events to database
  analyze        Run LLM analysis on recent events [limit]
  process        Process queue + analyze (backward compat)
  feed           Seed knowledge from files or directories
  daemon         Start background processor (dashboard at :9090)

  search         Search captured context
  recent         Show recent events
  insights       Show insights [category] [limit]
  entities       Show entities [type]
  graph          Show knowledge graph for entity
  stats          Show statistics
  status         Show status (for status line)
  watch          Live dashboard of cognitive modes
  prune          Manage context size relative to project
  reembed        Re-generate embeddings with current model
  mcp            Start MCP server (for cross-tool access)

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
