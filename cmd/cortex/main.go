// Cortex - Context memory for AI development
package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/dereksantos/cortex/cmd/cortex/commands"
	"github.com/dereksantos/cortex/internal/storage"
	"github.com/dereksantos/cortex/pkg/cliout"
	"github.com/dereksantos/cortex/pkg/config"
	"github.com/dereksantos/cortex/pkg/registry"
)

const version = "0.1.0"

func init() {
	// Stamp the manifest generator with the binary's version so
	// regenerating tools.json picks up the build that's running.
	commands.BinaryVersion = version
}

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

	/**
		TODO(derek.s): Break down to less commands

		Non agentic commands

		cortex -> opens REPL
		cortex version or --version --> shows version
		cortex help or --help --> shows cli manual
		cortex install --> optional. creates default settings for cortex
		cortex daemon --> runs the daemon or returns its status

		Agentic commands

		cortex <command> "optional prompt"
			if no prompt shows basic stats

		cortex status ["prompt"]
			with no prompt shows one line stats

		cortex journal "prompt"
			with no prompt shows one line stats

			e.g.1 cortex journal "learn about the project for the first time"
				-> emegent dag builds context about the project

			e.g.2 cortex journal "log this session prompt"

			e.g.3 cortex journal "forget about separate commands for "

			e.g.4 cortex journal "calibrate memory and learn from recent dag logs"

		cortex eval "prompt"
			with no prompt shows online
			if empty runs all of

		cortex run "prompt"
			-> Run a general purpose emergent dag

	**/

	switch command {
	case "repl":
		runREPL(os.Args[2:])
		return
	case "capture", "ingest", "analyze", "feed", "journal":
		if cmd := commands.Get(command); cmd != nil {
			runCommand(command, cmd, &commands.Context{Args: os.Args[2:]})
		}
	case "init", "install", "uninstall", "projects":
		if cmd := commands.Get(command); cmd != nil {
			runCommand(command, cmd, &commands.Context{Args: os.Args[2:]})
		}
	case "daemon":
		if cmd := commands.Get("daemon"); cmd != nil {
			cfg, err := loadConfig()
			if err != nil {
				// Daemon can run without a per-project config
				cfg = config.Default()
			}
			runCommand(command, cmd, &commands.Context{Config: cfg, Args: os.Args[2:]})
		}
	case "test", "status", "forget", "dream-debug":
		if cmd := commands.Get(command); cmd != nil {
			cfg, err := loadConfig()
			if err != nil {
				// status works without config — it absorbs the old `info`
				// view (system + Ollama + model recs) which is meaningful
				// before `cortex init`.
				if command == "status" {
					runCommand(command, cmd, &commands.Context{Args: os.Args[2:]})
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
			runCommand(command, cmd, &commands.Context{Config: cfg, Storage: store, Args: os.Args[2:]})
		}
	case "eval":
		if cmd := commands.Get("eval"); cmd != nil {
			runCommand(command, cmd, &commands.Context{Args: os.Args[2:]})
		}
	case "code":
		// `cortex code` is the ad-hoc coding harness: no config or
		// storage needed (the harness opens its own per-workdir store).
		if cmd := commands.Get("code"); cmd != nil {
			runCommand(command, cmd, &commands.Context{Args: os.Args[2:]})
		}
	case "models":
		// `cortex models` probes configured endpoints + Ollama and
		// prints a recommended role map. No config/storage needed —
		// the command opens its own workdir-rooted .cortex/config.json
		// for endpoint definitions.
		if cmd := commands.Get("models"); cmd != nil {
			runCommand(command, cmd, &commands.Context{Args: os.Args[2:]})
		}
	case "run":
		// `cortex run --type=<dag-type>` invokes the DAG executor
		// per docs/dag-build-plan.md Stage 1 v0. No config or storage
		// needed for v0; later stages will wire to the unified Phase
		// 1 telemetry sink.
		if cmd := commands.Get("run"); cmd != nil {
			runCommand(command, cmd, &commands.Context{Args: os.Args[2:]})
		}
	case "calibrate":
		// `cortex calibrate` recomputes per-op p50 cost hints from the
		// project's dag_traces.jsonl rolling window and persists them
		// to .cortex/db/op_cost_hints.json. Stage 4-C deliverable.
		if cmd := commands.Get("calibrate"); cmd != nil {
			runCommand(command, cmd, &commands.Context{Args: os.Args[2:]})
		}
	case "tools":
		if cmd := commands.Get("tools"); cmd != nil {
			runCommand(command, cmd, &commands.Context{Args: os.Args[2:]})
		}
	case "measure":
		if cmd := commands.Get("measure"); cmd != nil {
			runCommand(command, cmd, &commands.Context{Args: os.Args[2:]})
		}
	case "search", "prune", "reembed", "embed", "search-vector":
		if cmd := commands.Get(command); cmd != nil {
			// --workdir signals an isolated invocation (benchmarks,
			// tests, multi-tenant tools). Skip the global init dance:
			// no daemon, no global cfg/storage. The command opens its
			// own workdir-rooted state via openWorkdirContext.
			if hasWorkdirFlag(os.Args[2:]) {
				runCommand(command, cmd, &commands.Context{Args: os.Args[2:]})
				return
			}
			cfg, err := loadConfig()
			if err != nil {
				fmt.Fprintf(os.Stderr, "Cortex not initialized. Run 'cortex init' first.\n")
				os.Exit(1)
			}
			// Auto-start daemon on search (covers CLI-only multi-agent usage).
			if command == "search" {
				maybeStartDaemon(cfg)
			}
			store, err := storage.New(cfg)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Failed to open storage: %v\n", err)
				os.Exit(1)
			}
			defer store.Close()
			runCommand(command, cmd, &commands.Context{Config: cfg, Storage: store, Args: os.Args[2:]})
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

// runCommand executes a registered command with unified telemetry +
// error handling. Behavior:
//   - --no-telemetry (or CORTEX_NO_TELEMETRY env) suppresses the row
//     write but the flag is still stripped from ctx.Args so the
//     downstream parser doesn't reject it.
//   - One TelemetryRow is appended per invocation. Errors during the
//     write are swallowed — telemetry must never crash a successful
//     command.
//   - On Execute error, prints to stderr and exits 1 (matches the
//     prior per-case behavior). The telemetry row is written FIRST so
//     failed invocations still leave a trace.
func runCommand(command string, cmd commands.Command, ctx *commands.Context) {
	noTelemetry := cliout.HasNoTelemetryFlag(ctx.Args)
	ctx.Args = cliout.StripNoTelemetry(ctx.Args)
	workdir := cliout.WorkdirFromArgs(ctx.Args)
	inv := cliout.NewInvocation(command, cliout.CortexFunctionFor(command), workdir)
	ctx.Invocation = inv

	err := cmd.Execute(ctx)

	if !noTelemetry {
		var row cliout.TelemetryRow
		if err != nil {
			row = inv.FinishErr("")
		} else {
			row = inv.FinishOk()
		}
		_ = inv.WriteRow(row)
	}

	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
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
  test           Test LLM analysis [decision|pattern|insight]

  capture        Capture event from stdin (used by AI tools)
  ingest         Move queued events to database
  analyze        Run LLM analysis on recent events [limit]
  feed           Seed knowledge from files or directories
  journal        Journal operations (rebuild/replay/verify/show/tail/migrate/ingest)
  daemon         Start background processor (dashboard at :9090)

  search         Search captured context; --type=recent|insights|entities|graph for views
  status         Show status (default: one line; --system|--memory|--json|--expand)
  prune          Manage context size relative to project
  reembed        Re-generate embeddings with current model
  measure        Measure prompt quality for small context windows

  tools          Generate / verify tools.json manifest

  run            Run a DAG by type (turn|eval; think|dream|capture pending)
  calibrate      Recompute per-op p50 cost hints from dag_traces.jsonl
  eval           Run an eval scenario or suite (mechanic|coding|v2)
  code           One-shot coding agent against a workdir (DAG-driven)

  version        Show version
  help           Show this help

Global flags (accepted on every command):
  --no-telemetry    Suppress the per-invocation row write to
                    .cortex/db/cell_results.jsonl. The CORTEX_NO_TELEMETRY
                    environment variable (set to any non-empty value) has
                    the same effect.

Examples:
  # Inspect current state
  cortex status                  # one-line status (status-line use)
  cortex status --system         # system resources + Ollama + model recs
  cortex status --memory         # context memory dashboard
  cortex status --expand         # LLM-generated multi-line summary (small local model)
  cortex status --json           # raw stats as JSON

  # Test LLM analysis quality
  cortex test decision
  cortex test

  # Initialize in project
  cortex init

  # Process workflow (manual)
  cortex ingest              # Queue → Database
  cortex analyze 5           # Analyze last 5 events

  # Capture from AI tool (in hook)
  echo '{"tool_name":"Edit",...}' | cortex capture

  # Search context
  cortex search "authentication decisions"

  # View captured state via --type (replaces standalone recent/insights/entities/graph)
  cortex search --type=recent              # last 10 events
  cortex search --type=insights decision   # insights filtered by category
  cortex search --type=entities pattern    # entities of one type
  cortex search --type=graph decision "JWT authentication"

For more information: https://github.com/dereksantos/cortex
`, version)
}
