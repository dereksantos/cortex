// Cortex - Context memory for AI development
package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/dereksantos/cortex/cmd/cortex/commands"
	"github.com/dereksantos/cortex/internal/storage"
	"github.com/dereksantos/cortex/pkg/config"
	"github.com/dereksantos/cortex/pkg/registry"
)

const version = "0.1.0"

func main() {
	if len(os.Args) < 2 {
		// Bare `cortex` (no subcommand) drops into the interactive REPL
		// rooted at cwd with the default tiny model. See repl.go.
		runREPL(nil)
		return
	}

	// `cortex --flag ...` (no subcommand, only flags) also enters the
	// REPL — flags are forwarded. This matches the bare-cortex UX
	// promise: `cortex --model phi3:mini` should "just work" without
	// requiring `cortex repl --model phi3:mini`.
	if strings.HasPrefix(os.Args[1], "-") {
		runREPL(os.Args[1:])
		return
	}

	command := os.Args[1]

	switch command {
	case "repl":
		runREPL(os.Args[2:])
		return
	case "capture", "ingest", "analyze", "process", "feed", "journal":
		if cmd := commands.Get(command); cmd != nil {
			ctx := &commands.Context{
				Args: os.Args[2:],
			}
			if err := cmd.Execute(ctx); err != nil {
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				os.Exit(1)
			}
		}
	case "init", "install", "uninstall", "projects":
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
				// Daemon can run without a per-project config
				cfg = config.Default()
			}
			ctx := &commands.Context{
				Config: cfg,
				Args:   os.Args[2:],
			}
			if err := cmd.Execute(ctx); err != nil {
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				os.Exit(1)
			}
		}
	case "info", "test", "stats", "status", "forget", "overview", "dream-debug":
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
	case "code":
		// `cortex code` is the ad-hoc coding harness: no config or
		// storage needed (the harness opens its own per-workdir store).
		if cmd := commands.Get("code"); cmd != nil {
			ctx := &commands.Context{
				Args: os.Args[2:],
			}
			if err := cmd.Execute(ctx); err != nil {
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				os.Exit(1)
			}
		}
	case "measure":
		if cmd := commands.Get("measure"); cmd != nil {
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
	case "search", "recent", "insights", "entities", "graph", "prune", "reembed", "embed", "search-vector":
		if cmd := commands.Get(command); cmd != nil {
			// --workdir signals an isolated invocation (benchmarks,
			// tests, multi-tenant tools). Skip the global init dance:
			// no daemon, no global cfg/storage. The command opens its
			// own workdir-rooted state via openWorkdirContext.
			if hasWorkdirFlag(os.Args[2:]) {
				ctx := &commands.Context{Args: os.Args[2:]}
				if err := cmd.Execute(ctx); err != nil {
					fmt.Fprintf(os.Stderr, "Error: %v\n", err)
					os.Exit(1)
				}
				return
			}
			cfg, err := loadConfig()
			if err != nil {
				fmt.Fprintf(os.Stderr, "Cortex not initialized. Run 'cortex init' first.\n")
				os.Exit(1)
			}
			// Auto-start daemon on search/insights (covers CLI-only multi-agent usage).
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

// runREPL invokes the bare-`cortex` REPL command. Bare invocation and
// explicit `cortex repl` both land here. No config / storage init
// happens here — the REPL state code in commands/repl.go does its own
// per-workdir .cortex/ bootstrap, since the user may be in a fresh
// directory that's never been touched by cortex before.
func runREPL(args []string) {
	cmd := commands.Get("repl")
	if cmd == nil {
		fmt.Fprintln(os.Stderr, "cortex: repl command not registered (build error?)")
		os.Exit(1)
	}
	ctx := &commands.Context{Args: args}
	if err := cmd.Execute(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}

func loadConfig() (*config.Config, error) {
	projectRoot, err := os.Getwd()
	if err != nil {
		return nil, err
	}

	configPath := fmt.Sprintf("%s/.cortex/config.json", projectRoot)
	cfg, err := config.Load(configPath)
	if err != nil {
		return nil, err
	}

	// Point storage at global data directory (~/.cortex/)
	globalDir := registry.GlobalDir()
	cfg.GlobalDir = globalDir
	cfg.ContextDir = globalDir

	// Try to find project ID from registry
	if cfg.ProjectID == "" {
		if reg, regErr := registry.Open(); regErr == nil {
			if entry := reg.FindByPath(projectRoot); entry != nil {
				cfg.ProjectID = entry.ID
			}
		}
	}

	return cfg, nil
}

// maybeStartDaemon auto-starts the global daemon if it's not running.
// Fire-and-forget: never blocks the caller, never fails the caller.
// Writes to stderr only so it doesn't pollute hook stdout.
// hasWorkdirFlag returns true if args carries --workdir or --workdir=...
// — used to suppress global side effects (auto-daemon, ~/.cortex
// touches) on isolated benchmark invocations.
func hasWorkdirFlag(args []string) bool {
	for _, a := range args {
		if a == "--workdir" || strings.HasPrefix(a, "--workdir=") {
			return true
		}
	}
	return false
}

func maybeStartDaemon(_ *config.Config) {
	globalDir := registry.GlobalDir()
	if commands.IsDaemonRunning(globalDir) {
		return
	}
	pid, err := commands.StartDaemonBackground(globalDir)
	if err != nil {
		// Already running or can't start — either way, not our problem
		return
	}
	fmt.Fprintf(os.Stderr, "cortex: auto-started daemon (pid %d)\n", pid)
}

func printUsage() {
	fmt.Printf(`Cortex %s - Context memory for AI development

Usage:
  cortex <command> [options]

Commands:
  init           Initialize Cortex in current directory
  install        Install Cortex hooks for Claude Code
  uninstall      Remove Cortex hooks (--purge to also delete .cortex/)
  projects       List registered projects
  info           Show system info and model recommendations
  test           Test LLM analysis [decision|pattern|insight]

  capture        Capture event from stdin (used by AI tools)
  ingest         Move queued events to database
  analyze        Run LLM analysis on recent events [limit]
  process        Process queue + analyze (backward compat)
  feed           Seed knowledge from files or directories
  journal        Journal operations (rebuild/replay/verify/show/tail/migrate/ingest)
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
  measure        Measure prompt quality for small context windows
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
