package main

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"runtime/debug"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/dereksantos/cortex/internal/lineedit"
	"github.com/dereksantos/cortex/internal/loopui"
	"github.com/dereksantos/cortex/internal/style"
	"github.com/dereksantos/cortex/internal/tools"
)

/*
TODO (production sequence in docs/cortex-production-harness.md):
[x] Scanner animation v1
[x] System prompt
[x] Tool calling v1 (read_file, write_file, bash allowlist)
[x] Basic editing
[x] Bash tool
[x] Tolerate native Qwen XML tool-call format (proxy fallback)
[x] Improve session status line
[x] Improve animation
[x] Timestamp in messages
[x] Study tool (map-first navigator: reads goal-relevant regions; can spawn sub-studies)
[x] Oversized bash output summarized, not truncated (spill to .cortex/shell/ + digest)
[x] Hardening: HTTP timeout, bounded retry, Ctrl-C interrupt
[x] AGENTS.md project-instructions injection
[x] Session transcripts + resume (raw JSONL in .cortex/sessions/, NOT the journal)
[x] Capture at turn end — Tier 1 (structural, mechanical: every turn + /remember)
[x] Capture Tier 2 (model-distilled insights, async on the reasoner, preemptible)
    [removed 2026-07 — memory-tools pivot; seam now carries the memory index]
[x] Eval 6a: per-session metrics (tokens/turns/captures/insights) → eval.cell_result + summary
[ ] Eval 6b: learning-loop eval runner (cold vs warm memory) — tracked as
    docs/completion-roadmap.md Track B3
[x] Compaction-as-study (red-gauge answer) + /clear + overflow recovery
[x] Retrieval injection at turn start (Fast/Reflex; ephemeral per-turn; Think later)
    [removed 2026-07 — memory-tools pivot; seam now carries the memory index]
[ ] Integrate eval suite into new harness — tracked in docs/completion-roadmap.md
[x] cortex model for cataloging and suggesting model setups based on system
    resources — docs/completion-roadmap.md Track C1
[ ] Later (after harness is stable): cortex dream / think / dag integration —
    tracked in docs/completion-roadmap.md

*/

const RoleUser = "user"
const RoleSystem = "system"
const RoleTool = "tool"
const ModelCoder = "coder"

// Tool function names — canonical identifiers, defined in the tools package.
const (
	FunctionReadFile  = tools.FunctionReadFile
	FunctionWriteFile = tools.FunctionWriteFile
	FunctionEditFile  = tools.FunctionEditFile
	FunctionStudy     = tools.FunctionStudy
	FunctionBash      = tools.FunctionBash
	FunctionRemove    = tools.FunctionRemove
	FunctionOutline   = tools.FunctionOutline
	FunctionGrep      = tools.FunctionGrep
	FunctionWebSearch = tools.FunctionWebSearch
	FunctionFetchURL  = tools.FunctionFetchURL
)

const defaultModel = ModelCoder

// maxToolIterations bounds the agentic inner loop so a confused model can't
// spin forever burning tokens. The smallest form of the "bounded" principle.
const maxToolIterations = 100

// maxToolOutput caps how much tool output we feed back into context. Mirrors
// internal/tools' zero-config default (tools.DefaultLimits().MaxToolOutput);
// used only by tests exercising that default — the live, config-resolvable
// value lives in internal/tools' active Limits (see Config.toolLimits()).
var maxToolOutput = tools.DefaultLimits().MaxToolOutput

const (
	// codeMaxOutputTokens / studyMaxOutputTokens cap each loop's per-turn OUTPUT
	// (completion) tokens. north (Cohere North-Mini-Code) supports 64K output over
	// a 256K context; one agentic turn needs far less. An explicit max_tokens is
	// the ONLY thing that bounds a runaway: unset, llama-server uses n_predict=-1,
	// and north's --context-shift means even the 256K window isn't a hard stop — a
	// request ran to 124K tokens / ~30min and pinned the slot (2026-06-28, see
	// CLAUDE.md). Override per role via config models.<role>.max_tokens.
	codeMaxOutputTokens  = 16384
	studyMaxOutputTokens = 8192
	// defaultAgentMaxTokens backstops any AgentRequest whose cap is unset, so no
	// wire request is ever unbounded even if a construction site forgets.
	defaultAgentMaxTokens = codeMaxOutputTokens
)

// requestTimeout caps one model call end-to-end. Local generation can be slow,
// so it's generous — Ctrl-C is the interactive escape hatch; this catches a
// server that accepted the request and will never answer.
const requestTimeout = 10 * time.Minute

// maxSendAttempts bounds retries of one model call. Only transient failures
// (transport errors, 429/5xx) retry; a 4xx means the request is wrong.
const maxSendAttempts = 3

// retryBackoff is the base delay between attempts (attempt × retryBackoff); a var
// so tests can shrink it.
var retryBackoff = 500 * time.Millisecond

// compactThreshold is the window-fill ratio where the gauge goes red and the
// turn-boundary auto-compact fires. One number, shared, so what the user sees
// (red) and what the harness does (compact) can't drift apart.
const compactThreshold = 0.8

// compactGoal steers the compaction study toward what a continuing session
// needs — state over narrative, recent and unresolved over settled ones.
// Carries the working-style intent (checkpoints, tidy-vs-feature split) so
// a compaction preserves the batch discipline, not just a content summary.
const compactGoal = "Summarize this coding session for continuation: the user's task and intent, " +
	"decisions made and why, files read or edited (exact paths), commands run and their key results, " +
	"the current state of the work, what checkpoint you just delivered, what is tidy vs. what is the " +
	"feature, and anything unresolved. Prefer recent and open items over settled ones."

// Version is the semantic base shown in the status line. It's a var (not const)
// so a release build can override it: go build -ldflags "-X main.Version=1.2.3".
var Version = "0.3.0"

// version returns the display version: the semantic base plus the short git
// revision (and a -dirty marker) when the binary was built from a VCS checkout.
// `go build` stamps this automatically via debug.ReadBuildInfo — no flags needed.
func version() string {
	v := Version
	bi, ok := debug.ReadBuildInfo()
	if !ok {
		return v
	}
	var rev string
	var dirty bool
	for _, s := range bi.Settings {
		switch s.Key {
		case "vcs.revision":
			rev = s.Value
		case "vcs.modified":
			dirty = s.Value == "true"
		}
	}
	if len(rev) >= 7 {
		v += "+" + rev[:7]
		if dirty {
			v += "-dirty"
		}
	}
	return v
}

// curationBudgetTokens is the read_file → study redirect threshold: a whole-file
// read estimated above this many tokens is refused in favor of study, so the
// coder receives a curated digest instead of a raw dump. Fixed (not a fraction
// of the coder window) so curation stays the default even on a large-window
// model — the whole point is to spend the coder's context on distilled signal,
// not raw bytes it could technically hold. ~16k tok ≈ 64 KB ≈ 1600 lines:
// ordinary source files still read whole; large files curate. Mirrors
// internal/tools' zero-config default; see maxToolOutput's doc comment above
// for why this is a var pinned to the default rather than the live value.
var curationBudgetTokens = tools.DefaultLimits().CurationBudgetTokens

// promptGlyph is the input affordance at the end of the status line.
const promptGlyph = tools.PromptGlyph

type Spinner = loopui.Spinner

func NewSpinner() *Spinner { return loopui.NewSpinner() }

// Tool and ToolFunction types, the schema helpers, and the tool declarations
// live in the tools package. Aliased here so the rest of main.go reads
// unchanged.
type Tool = tools.Tool
type ToolFunction = tools.ToolFunction

// The seam, asserted: *CortexSession is the composition root that structurally
// satisfies each narrow tool role-interface. A missing method fails HERE with a
// clear location instead of at a distant dispatch call.
var (
	_ tools.ToolDeps        = (*CortexSession)(nil)
	_ tools.MemoryStore     = (*CortexSession)(nil)
	_ tools.Summarizer      = (*CortexSession)(nil)
	_ tools.Outliner        = (*CortexSession)(nil)
	_ tools.SubAgentRunner  = (*CortexSession)(nil)
	_ tools.ShellGate       = (*CortexSession)(nil)
	_ tools.DeleteGate      = (*CortexSession)(nil)
	_ tools.ConfigProvider  = (*CortexSession)(nil)
	_ tools.Validator       = (*CortexSession)(nil)
	_ tools.OutlineModifier = (*CortexSession)(nil)
)

// --- Memory ----------------------------------------------------------------
//
// Memory is model-driven (docs/memory-tools.md): the agent curates free-form
// named notes through the memory_* tools, and the note index is injected at
// turn start (memoryIndexNote). The only durable substrate kept alongside is
// the append-only journal — the per-turn record study(.cortex/journal) reads.
// The old mechanical retrieve/rerank/distill pipeline was removed.

// EnableMemory wires the model-driven memory store and the journal capturer over
// the project's .cortex/ directory. Best-effort: a failure just leaves memory
// unavailable (the tools say so) and capture off; the REPL runs regardless. Only
// the interactive REPL and headless turn/Discord drivers call this; the
// study/eval subcommands never build it.
// runToolCalls executes every requested call and appends one tool result per
// call ID — even after an interrupt. The wire invariant is that every
// assistant(tool_calls) id gets a matching tool message or the next send 400s,
// so a mid-turn cancel records "interrupted" results rather than dropping them.
// startActivity/stopActivity drive the anchored status spinner around a unit of
// work. No-op outside the anchored REPL (raw-streaming and headless modes have
// no pinned status row to animate).

// printAvailableTools prints the list of available tools to stdout.
func printAvailableTools() {
	fmt.Println(style.Paint("Available tools:", style.Accent))
	for _, tool := range tools.All {
		fmt.Printf("  - %s\n", tool.Function.Name)
	}
	fmt.Println()
}

// helpLines is the /help body: every slash command, one line each, plain
// text — kept as a var (not inlined) so TestHelp can assert every command
// name appears without hardcoding the exact prose.
var helpLines = []string{
	"/help              show this list",
	"/context           open the current session's context-window map (q to close)",
	"/compact           distill the session via study, freeing context",
	"/plan <task>       plan-then-execute: one planning turn, then each step as its own turn",
	"/clear             reset the conversation and start a fresh session",
	"/undo [N]          revert the Nth-most-recent turn's file changes (default 1)",
	"/sessions          pick a saved session to resume (plain list when not a TTY)",
	"/last              every tool call of the last turn, unabridged (Ctrl-O)",
	"/memory            browse saved memory notes, read-only (the agent writes them)",
	"/model [name]      show the code/study model bindings, or switch the coding model",
	"/hook off|format|all  turn the post-edit hook down or off for this session (never raises it)",
	"/quit              exit (Ctrl-D and /exit also work)",
}

// keyHints is the one-row key reference "?" shows under an empty prompt.
var keyHints = style.Paint("tab complete · alt-enter newline · ctrl-o last turn · ctrl-r search · esc stop", style.Dim)

// helpCommands parses helpLines into the sorted command names ("/model") and
// each one's description — the completion source's vocabulary.
func helpCommands() ([]string, map[string]string) {
	var names []string
	help := map[string]string{}
	for _, l := range helpLines {
		fields := strings.Fields(l)
		if len(fields) == 0 {
			continue
		}
		name := fields[0]
		// The description is what follows the run of two or more spaces after
		// the command and its argument hint.
		desc := ""
		if i := strings.Index(l, "  "); i >= 0 {
			desc = strings.TrimSpace(l[i:])
		}
		names = append(names, name)
		help[name] = desc
	}
	sort.Strings(names)
	return names, help
}

// printHelp lists the slash commands, plain text, one per line — matching
// the REPL's plain style (no icons; ANSI color only).
func printHelp() {
	fmt.Println(style.Paint("Commands:", style.Accent))
	for _, line := range helpLines {
		fmt.Println("  " + line)
	}
}

// usageLines is the `cortex --help` body: every subcommand main() dispatches
// on, one line each. Kept as a var next to helpLines (the REPL's /help) for
// the same reason — TestUsageFlag asserts every subcommand name appears
// without pinning the prose.
var usageLines = []string{
	"cortex                                    interactive REPL (default)",
	"cortex resume [id]                        resume a prior session (no id: pick one on a TTY, else latest)",
	"cortex turn [--session id] [--plan] [--json] ...   headless turn; --plan runs plan-then-execute; session id to stderr",
	"cortex study <path> [goal...]             one-off study; prints the digest",
	"cortex learn [--project <name>]           background learning pass over the journal",
	"cortex change <start|commit|status>       git change lifecycle (local git only)",
	"cortex serve [--port <n>]                 local HTTP/SSE adapter for the web UI",
	"cortex scan [--json] [--root <path>]      scan configured roots for projects",
	"cortex project <add|list|remove>          manage the project registry",
	"cortex project trust <add|remove|list>    manage the per-workspace trust list (the post-edit hook's only gate)",
	"cortex discord                            Discord adapter (DISCORD_BOT_TOKEN)",
	"cortex model [--json]                     show model role bindings and what's served",
	"cortex study-eval                         study acceptance test",
	"cortex version                            print the version and exit",
}

// printUsage is the CLI-level counterpart to printHelp: `cortex --help`
// lists the subcommands and global flags, then points at /help for the
// slash commands that only exist once the REPL is running. Without it
// `cortex --help` fell through into the REPL, which reads as a hang.
func printUsage() {
	fmt.Println("cortex " + version())
	fmt.Println()
	fmt.Println(style.Paint("Usage:", style.Accent))
	for _, line := range usageLines {
		fmt.Println("  " + line)
	}
	fmt.Println()
	fmt.Println(style.Paint("Flags:", style.Accent))
	fmt.Println("  --help, -h        show this list")
	fmt.Println("  --version, -v     print the version and exit")
	fmt.Println("  --tools           list the agent's registered tools at startup")
	fmt.Println()
	fmt.Println("Inside the REPL, type /help for the slash commands.")
}

func main() {
	// `cortex --version` / `cortex version` prints the same string the REPL
	// banner shows (display.go) and exits 0 — the standard "how do I check
	// what I have installed" surface (docs/completion-roadmap.md Track E4).
	// Checked first, ahead of every other subcommand/flag.
	if len(os.Args) >= 2 && (os.Args[1] == "--version" || os.Args[1] == "-v" || os.Args[1] == "version") {
		fmt.Println("cortex " + version())
		os.Exit(0)
	}

	// `cortex --help` / `-h` / `help` prints the subcommand usage and exits 0.
	// Checked alongside --version, ahead of every subcommand: without it the
	// flag fell through to the REPL and looked like a hang.
	if len(os.Args) >= 2 && (os.Args[1] == "--help" || os.Args[1] == "-h" || os.Args[1] == "help") {
		printUsage()
		os.Exit(0)
	}

	// The tool list is opt-in: strip --tools / --show-tools (if present) and
	// remember it so printAvailableTools runs only when explicitly requested.
	showTools := false
	var args []string
	for _, a := range os.Args {
		switch a {
		case "--tools", "--show-tools":
			showTools = true
		default:
			args = append(args, a)
		}
	}
	os.Args = args

	// Study-eval mode: `cortex study-eval` runs study over a fixture set and scores
	// latency / coverage / groundedness. `cortex study-eval code-grid` runs the
	// 2×2 granularity × numbering isolation experiment on the code fixture.
	if len(os.Args) >= 2 && os.Args[1] == "study-eval" {
		runStudyEvalNav()
		return
	}

	// Direct study mode: `cortex study [--project <name>] <path> [goal...]`.
	// The navigator subagent reads what the goal needs; there are no
	// deepening passes to configure. --project (M3.5) resolves via the
	// registry and runs against that project's root instead of the CWD.
	if len(os.Args) >= 3 && os.Args[1] == "study" {
		project, rest := parseProjectFlag(os.Args[2:])
		if len(rest) == 0 {
			fmt.Fprintln(os.Stderr, "usage: cortex study [--project <name>] <path> [goal...]")
			os.Exit(2)
		}
		runStudyCLI(project, rest[0], strings.Join(rest[1:], " "))
		return
	}

	// Headless single-turn mode: `cortex turn [--session <id>] [--json] <input…>`
	// (or the input on stdin). Runs exactly one Turn over a fresh or resumed
	// session and prints the model's reply — the seam a headless driver or Discord
	// adapter shells into. Pass the persistent session's id to keep the same
	// conversation + shared .cortex/ across invocations ("one session at a
	// time"); the id is echoed on stderr so a driver can thread it forward.
	if len(os.Args) >= 2 && os.Args[1] == "turn" {
		runTurnCLI(os.Args[2:])
		return
	}

	// One-shot headless learning pass: `cortex learn [--project <name>]`
	// (docs/learning-loop.md). Runs the background Learn subagent over the
	// current (or named) project's journal since the last learn cursor and
	// prints a short plain report — always exits 0, since "nothing worth
	// saving" is a normal outcome, not a failure. See learn.go.
	if len(os.Args) >= 2 && os.Args[1] == "learn" {
		runLearnCLI(os.Args[2:])
		return
	}

	// Landscape scan: `cortex scan [--json] [--root <path>]` (Phase 2 /
	// M2.5). Uses persisted scan.roots or --root; refuses (never a blind
	// $HOME sweep) when neither is available — see scan.go.
	if len(os.Args) >= 2 && os.Args[1] == "scan" {
		runScanCLI(os.Args[2:])
		return
	}

	// Project registry: `cortex project add/list/remove` (Phase 3 / M3.4).
	// See project.go.
	if len(os.Args) >= 2 && os.Args[1] == "project" {
		runProjectCLI(os.Args[2:])
		return
	}

	// Model catalog + suggestion: `cortex model [--json]`
	// (docs/completion-roadmap.md Track C1). See model.go.
	if len(os.Args) >= 2 && os.Args[1] == "model" {
		runModelCLI(os.Args[2:])
		return
	}

	// One-change-at-a-time git lifecycle: `cortex change <start|commit|status>`.
	// A driver runs these around an agent turn so each change lands on its own
	// branch, isolated and reviewable. Local only — see change.go.
	if len(os.Args) >= 2 && os.Args[1] == "change" {
		if err := runChangeCLI(os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}

	// Local HTTP/SSE adapter: `cortex serve [--port <n>]` (Phase 4 / M4.1).
	// Foreground, loopback-only, gated by a Host/Origin allowlist (no bearer
	// token — 2026-07-19, SECURITY.md) — see serve.go.
	if len(os.Args) >= 2 && os.Args[1] == "serve" {
		runServeCLI(os.Args[2:])
		return
	}

	// Discord adapter: `cortex discord` connects to Discord and drives one
	// persistent session in-process. The only Discord-aware entry point — see
	// discord.go. Token + scope come from the environment.
	if len(os.Args) >= 2 && os.Args[1] == "discord" {
		if err := runDiscordCLI(); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}

	// Interactive-REPL-only backend bootstrap (docs/completion-roadmap.md
	// Gate E): every headless subcommand above (`turn`, `learn`, `study`,
	// `scan`, `project`, `model`, `change`, `serve`, `discord`, `study-eval`)
	// already returned, so this only ever runs for the bare `cortex` /
	// `cortex resume` REPL entry points. On a true first run (no user
	// config, no project config, no $CORTEX_BACKEND) with stdin attached
	// to a real terminal, walks the user through picking/pasting an
	// OpenRouter key and persists the result before NewCortexSession()
	// reads it — a fresh machine with just a key reaches a green first
	// turn without hand-editing a config file. A non-interactive first run
	// (piped stdin, CI) prints one hint instead and falls through to
	// today's behavior (target localhost:4000) unchanged. An existing
	// config or env var bypasses this entirely.
	maybeRunGuidedBootstrap()

	session := NewCortexSession()

	// Print available tools on launch only when --tools was passed; the REPL
	// otherwise starts clean.
	if showTools {
		printAvailableTools()
	}

	// `cortex resume [--project <name>] [id]` continues a prior session (the
	// latest when no id is given); otherwise every REPL session persists
	// under a fresh transcript. --project (M3.5) resolves via the registry
	// and re-targets the session at that project's root, so the resumed (or
	// fresh) session's ContextDir/SessionsDir/instructions/confinement all
	// follow the project instead of the CWD.
	resumed := false
	if len(os.Args) >= 2 && os.Args[1] == "resume" {
		project, rest := parseProjectFlag(os.Args[2:])
		if project != "" {
			if err := applyProjectFlag(session, project); err != nil {
				fmt.Fprintf(os.Stderr, "project %s: %v\n", project, err)
			}
		}
		id := ""
		if len(rest) >= 1 {
			id = rest[0]
		}
		// `cortex resume` with no id picks interactively (issue #110) rather than
		// silently taking the latest: the user sees what they are resuming. An
		// explicit id is never second-guessed. The gate is the same one /sessions
		// uses (interactive stdin, rich-render stdout), and first-run bootstrap is
		// excluded because a user who has not configured a backend yet has no
		// sessions worth picking — that path must reach the setup flow. Leaving the
		// picker without choosing (ESC, or a harness failure) falls through to
		// today's latest-session resume; "cancel" is not "quit".
		if id == "" && resumePickerUsable() {
			if picked, ok := pickSessionAtStartup(session); ok {
				id = picked
			}
		}
		if err := session.ResumeTranscript(id); err != nil {
			fmt.Printf("resume: %v - starting fresh\n", err)
			session.StartTranscript()
		} else {
			resumed = true
		}
	} else {
		session.StartTranscript()
	}

	// Fast retrieval over .cortex/ (best-effort; disabled cleanly if the store
	// can't open). Shut down with the transcript at exit.
	session.EnableMemory()
	defer session.Close()

	// The opening header (docs/tui-polish.md, track 1): version · project ·
	// model, plus what a resume carried over. After EnableMemory so the note
	// count is real.
	for _, line := range renderHeader(session.startupFacts(resumed)) {
		fmt.Println(line)
	}

	// First-run greeting (Phase 1 / M1.5): fires exactly once, before the
	// read loop, so the very first thing a fresh machine sees from `cortex`
	// is the introduction rather than a bare prompt. A failed greeting
	// (e.g. no backend reachable yet) is reported but does not block the
	// REPL — the marker is only written on success, so it retries next launch.
	greetCtx, stopGreet := signal.NotifyContext(context.Background(), os.Interrupt)
	if err := MaybeGreet(greetCtx, session); err != nil {
		fmt.Fprintf(os.Stderr, "greeting: %v\n", err)
	}
	stopGreet()

	// One static hint after the greeting, every run (not just first-run) — the
	// discoverability surface for /help now that the REPL carries no icon set
	// to hint at itself visually.
	fmt.Println(style.Paint("type /help for commands", style.Dim))

	// Interactive terminals get the raw-mode line editor (arrows, editing,
	// bracketed paste, ESC-to-interrupt). Piped/redirected input — tests, CI,
	// `printf … | loop` — falls back to the plain scanner unchanged.
	var editor *lineedit.Terminal
	var scanner *bufio.Scanner
	if lineedit.IsInteractive(os.Stdin) {
		if t, err := lineedit.Open(os.Stdin, os.Stdout); err == nil {
			editor = t
			editor.SetHistory(lineedit.LoadHistory(filepath.Join(session.ContextDir(), "history")))
			editor.SetAcceptedLine(session.acceptedLine)
			// Ctrl-O at the prompt opens the last turn's calls, unabridged.
			editor.SetDetail(func() { openLastTurn(editor, session) })
			editor.SetKeyHints(keyHints)
			editor.SetCompletion(mentionCompleter(session)) // issue #108
			defer editor.Close()
			// Risky-command confirmation reads the answer through the anchor's
			// key loop (issue #107: y once / n / a always this command / p
			// always this command prefix). Tool calls run synchronously on
			// this goroutine between ReadLine calls, so there's no
			// concurrent reader to fight. Headless/piped sessions leave this
			// nil and the gate blocks risky commands instead.
			session.confirmRisky = func(question string) lineedit.ConfirmChoice {
				// During an anchored turn the Anchor's key loop owns the terminal;
				// a second reader (editor.ReadLine) fights it for the keystroke and
				// the answer never lands — the user can't approve and resorts to Ctrl-C.
				// Route the confirm through the anchor, which serves it from the same
				// loop. Fall back to a direct read only when no turn is pinned.
				if a := session.live; a != nil {
					return a.Confirm(question)
				}
				ans, err := editor.ReadLine(question)
				if err != nil {
					return lineedit.ConfirmNo
				}
				switch strings.ToLower(strings.TrimSpace(ans)) {
				case "y", "yes":
					return lineedit.ConfirmYes
				case "a", "always":
					return lineedit.ConfirmAlwaysExact
				case "p", "prefix":
					return lineedit.ConfirmAlwaysPrefix
				default:
					return lineedit.ConfirmNo
				}
			}
			// Stray stdlib-logger output (any subsystem that calls log.Printf)
			// goes to stderr, which in an interactive session lands on top of
			// the prompt. Divert it to a file so the terminal stays clean
			// (tail -f .cortex/cortex.log for debugging).
			if lf, err := os.OpenFile(filepath.Join(session.ContextDir(), "cortex.log"),
				os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644); err == nil {
				log.SetOutput(lf)
				defer lf.Close()
			}
		}
	}
	if editor == nil {
		scanner = bufio.NewScanner(os.Stdin)
	}

	// typeAhead carries keystrokes the user typed while the previous turn was
	// still streaming (captured by Interruptible) into the next prompt's draft.
	var typeAhead string

	for {
		// A blank line before each prompt separates turns when scrolling back.
		// Printed here — once per turn — rather than baked into Prompt(), which
		// is redrawn on every keystroke and cannot carry a newline (see Prompt).
		if !session.quiet {
			fmt.Println()
		}
		var input string
		if editor != nil {
			line, err := editor.ReadLineEcho(session.Prompt(), typeAhead)
			typeAhead = ""
			if err == io.EOF {
				break
			}
			if err == lineedit.ErrInterrupted {
				continue // Ctrl-C abandons the line, keeps the REPL
			}
			if err != nil {
				fmt.Printf("input error: %v\n", err)
				break
			}
			input = strings.TrimSpace(line)
		} else {
			fmt.Print(session.Prompt())
			if !scanner.Scan() {
				if err := scanner.Err(); err != nil {
					fmt.Printf("scanner error: %v\n", err)
				}
				break
			}
			input = strings.TrimSpace(scanner.Text())
		}
		if input == "" {
			continue
		}

		// Record for ↑/↓ and Ctrl-R recall — but not the session-enders, so a
		// fresh prompt's first ↑ lands on real work, not "/quit". The history
		// gets the line EXACTLY as typed: mention processing (below) only
		// rewrites the copy handed to the model, never what the user recalls.
		if editor != nil && input != "/quit" && input != "/exit" {
			editor.AddHistory(input)
		}

		// /quit and /exit leave the REPL; EOF (Ctrl-D) breaks above. All
		// three paths fall through to the single "exiting" print below.
		if input == "/quit" || input == "/exit" {
			break
		}

		// /clear resets the conversation; /compact distills it via study.
		if input == "/clear" {
			session.Clear()
			fmt.Println(style.Paint("cleared -> session "+session.SessionID, style.Dim))
			continue
		}
		if input == "/compact" {
			compactNow(session, "manual compact")
			continue
		}

		// /undo [N] reverts the Nth-most-recent turn's file changes to the
		// working tree (issue #111): restore the snapshot, report the files
		// changed, and record the transcript note so the model learns its
		// edits were reverted. Not in a git repo or nothing to undo prints a
		// one-line message.
		if input == "/undo" || strings.HasPrefix(input, "/undo ") {
			arg := strings.TrimSpace(strings.TrimPrefix(input, "/undo"))
			n := 1
			if arg != "" {
				if v, err := strconv.Atoi(arg); err == nil && v > 0 {
					n = v
				} else {
					fmt.Println(style.Paint("usage: /undo [N]  (N is a positive integer, default 1)", style.Dim))
					continue
				}
			}
			session.undo(n)
			continue
		}

		// Issue #108: @path mentions are attached to the turn through the same
		// size rules as read_file (small files inline, large files as an
		// outline with a pointer to study). The attachment is prepended to
		// what the model sees; the mentions in the input are replaced by short
		// markers. This runs only on the two paths that hand the line to the
		// model (a normal turn, and /plan's task) — slash commands and prose
		// the user never sends to the model are left untouched.
		//
		// Issue #218: an @mention naming an IMAGE (a workspace path or an
		// http(s) address) is not inlined text — it comes back in
		// mentionImages as a content part for the turn itself. Parsing happens
		// here, at the one place that knows whether this line goes to the
		// model at all; the bytes reach the model in step with the turn that
		// carries them.
		var mentionAttachment string
		var mentionImages []TurnImage
		var mentionImageRefusals []MentionRefusal
		process := func() {
			// context.Background(): mention resolution runs while the REPL is
			// between turns, where the turn's own context does not exist yet —
			// and a URL mention's download is bounded by the fetch client's
			// own timeout, so there is nothing here left unbounded.
			input, mentionAttachment, mentionImages, mentionImageRefusals = processMentions(context.Background(), session.root(), input, session)
		}

		// /plan <task> runs the plan-then-execute path (#150): one planning
		// turn, then each step as its own turn with the project's checks in
		// between. The task is the rest of the line; a bare /plan with no
		// task prints the usage hint. The per-step report is printed to the
		// REPL exactly as the headless `cortex turn --plan` prints it.
		//
		// The run goes through the SAME context choice and post-turn safety
		// net as a normal turn (runUnderAnchor / Interruptible for the cancel
		// and type-ahead, afterTurn for the compaction / diagnose / learnWindow
		// handling) — a plan is up to planStepCap turns in a row, the place
		// context grows most, so it must reach the compaction safety net too.
		if input == "/plan" || strings.HasPrefix(input, "/plan ") {
			task := strings.TrimSpace(strings.TrimPrefix(input, "/plan"))
			if task == "" {
				fmt.Println(style.Paint("usage: /plan <task>  (plan-then-execute: one planning turn, then each step as its own turn)", style.Dim))
				continue
			}
			process()
			// Issue #218: /plan has no image seam — TurnWithPlan builds each
			// step's turn itself — so an @mentioned image cannot ride a plan
			// run. Saying so beats the alternative: process() already replaced
			// the mention with "[@x.png attached]", and leaving that unpaired
			// would tell the human a screenshot went to the model when it did
			// not. A plan over an image is a normal turn with the mention, then.
			if len(mentionImages) > 0 || len(mentionImageRefusals) > 0 {
				printMentionImages(nil, nil, []string{
					"/plan does not carry image attachments — run the task as a normal turn (or ask about the image there) so the model can see it",
				})
			}
			var plan PlanRunResult
			var planErr error
			switch {
			case editor != nil && anchoredInput():
				typeAhead, planErr = runUnderAnchor(session, editor, typeAhead, func(ctx context.Context) error {
					var runErr error
					plan, runErr = session.TurnWithPlan(ctx, task, mentionAttachment)
					return runErr
				})
			case editor != nil:
				ctx, stop := editor.Interruptible(context.Background())
				plan, planErr = session.TurnWithPlan(ctx, task, mentionAttachment)
				typeAhead = stop()
			default:
				ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
				plan, planErr = session.TurnWithPlan(ctx, task, mentionAttachment)
				cancel()
			}
			if plan.Reply != "" {
				fmt.Println(plan.Reply)
			}
			// Issue #141: each step is its own turn with its own "tests
			// changed" receipt; the plan run carries them (TurnWithPlan) so
			// a /plan run reports test loss exactly like a single turn.
			printTestReceipt(plan.TestReceipt)
			printLintReceipt(plan.LintReceipt)
			printTurnReceipt(plan.Receipt)
			afterTurn(session, planErr)
			continue
		}

		// /sessions lists saved sessions so their ids are discoverable from
		// inside the REPL. On an interactive TTY it opens the picker instead
		// (issue #110): typing filters, Enter resumes, ESC leaves the listing
		// alone. Piped stdout, NO_COLOR, or CORTEX_LOOP_RENDER=0 keep the plain
		// list, and so does a harness failure — see session_picker.go. Resuming
		// is deferred until after the picker closes, so a failed resume cannot
		// strand the user inside a full-screen view; a resume that fails reopens
		// the session the user was on rather than copying it into a new one.
		if input == "/sessions" {
			if sessionsInspectable(editor) {
				picker := NewSessionPicker(listSessionsOrEmpty(session.SessionsDir()))
				if err := inspectSession(editor, picker); err == nil && picker.Accepted() {
					if id := picker.SelectedID(); id != "" {
						resumeFromSessionPicker(session, id)
					}
				}
				continue
			}
			session.printSessions()
			continue
		}

		// /help lists the slash commands; /context renders the two-zone
		// context-window map for the current session (a teaching surface as
		// well as a debugging one — see contextReport's doc comment).
		if input == "/help" {
			printHelp()
			continue
		}
		// /memory browses the saved notes, read-only — writing and forgetting
		// stay model-driven (docs/memory-tools.md). On a TTY: a picker over
		// both tiers; Enter opens a note, leaving it returns to the list.
		if input == "/memory" {
			if len(newMemoryPicker(session).items) == 0 {
				fmt.Println(style.Paint("no memory notes yet — ask the agent to remember something", style.Dim))
				continue
			}
			if !sessionsInspectable(editor) {
				for _, l := range newMemoryPicker(session).Texts() {
					fmt.Println(l)
				}
				continue
			}
			filter, last := "", ""
			for {
				picker := newMemoryPicker(session)
				picker.SetFilter(filter)
				picker.SelectID(last) // back on the note just read, under the same filter
				if err := inspectSession(editor, picker); err != nil || !picker.Accepted() || picker.SelectedID() == "" {
					break
				}
				filter, last = picker.Filter(), picker.SelectedID()
				id := last
				body, err := readMemoryNote(session, id)
				if err != nil {
					body = "could not read " + id + ": " + err.Error()
				}
				_ = inspectSession(editor, noteView{id: id, body: body})
			}
			continue
		}
		// /last opens the last turn's tool calls, unabridged — what the
		// scrollback folded or capped (docs/tui-polish.md, track 4). Ctrl-O at
		// the prompt opens the same view. Piped sessions get a plain listing.
		if input == "/last" {
			if sessionsInspectable(editor) {
				openLastTurn(editor, session)
			} else {
				for _, l := range (lastTurnView{calls: session.lastTurnCalls}).Lines(0) {
					fmt.Println(l)
				}
			}
			continue
		}
		if input == "/context" {
			// On an interactive TTY the map opens as a full-screen inspector
			// (alt screen; scrollback restored byte-for-byte on exit). Piped
			// stdout, NO_COLOR, or CORTEX_LOOP_RENDER=0 keep the plain
			// scrolling report — see context_view.go. A harness failure falls
			// back to that same report rather than swallowing the command.
			if contextInspectable(editor) {
				if err := editor.Inspect(contextView{cs: session}); err == nil {
					continue
				}
			}
			fmt.Println(session.contextReport())
			continue
		}

		// Memory is now model-driven: ask in natural language ("remember that …",
		// "forget the … note") and the agent calls memory_write / memory_forget.
		// The old /remember and /forget slash commands were removed with the
		// mechanical capture/retract pipeline. See docs/memory-tools.md.

		// /model [name] shows the role bindings, or switches the coding model.
		if input == "/model" || strings.HasPrefix(input, "/model ") {
			name := strings.TrimSpace(strings.TrimPrefix(input, "/model"))
			// A bare /model on an interactive terminal opens a picker over the
			// models this session knows (docs/tui-polish.md, track 4); Enter
			// switches the coder. Piped/plain sessions keep the printout.
			if name == "" && sessionsInspectable(editor) {
				picker := newModelPicker(session)
				if err := inspectSession(editor, picker); err == nil && picker.Accepted() {
					if id := picker.SelectedID(); id != "" && id != session.Request.Model {
						session.SetModel(id)
						fmt.Println(style.Paint("code model -> "+id, style.Dim))
					}
				}
				continue
			}
			if name == "" {
				fmt.Printf("code:  %s @ %s\nstudy: %s @ %s\n",
					session.Request.Model, session.Request.BaseURL,
					session.Study.Model, session.Study.Endpoint)
			} else {
				session.SetModel(name)
				fmt.Printf("code model -> %s\n", name)
			}
			continue
		}

		// /hook off|format|all turns the post-edit hook down or off for the
		// current session (issue #129 piece 2). A bare /hook prints the
		// current (effective) mode; a valid value lowers the session's mode
		// in place (SetMode is monotone-down, and the process ceiling is
		// folded in at read time, so nothing here can raise it above the
		// configured mode, and trust is never affected). An UNRECOGNIZED
		// value prints usage and the current mode and lowers nothing:
		// ParseHookMode maps unknown values to off, and a typo must never
		// silently disable the operator's hook.
		if input == "/hook" || strings.HasPrefix(input, "/hook ") {
			val := strings.TrimSpace(strings.TrimPrefix(input, "/hook"))
			switch val {
			case "":
				fmt.Println("post-edit hook: " + session.hookModeName())
			case "off", "format", "all":
				session.SetHookMode(tools.ParseHookMode(val))
				fmt.Println("post-edit hook -> " + session.hookModeName())
			default:
				fmt.Printf("usage: /hook off|format|all (post-edit hook: %s)\n", session.hookModeName())
			}
			continue
		}

		// M1.7: if a first-run greeting just asked where the user's code
		// lives, this is that answer — capture and persist it before running
		// the turn normally (the reply still gets an ordinary response too).
		if _, err := session.MaybeCaptureScanRoots(input); err != nil {
			fmt.Fprintf(os.Stderr, "scan roots: %v\n", err)
		}

		// Run the turn. The whole per-turn pipeline lives in Turn now — the same
		// entry point a headless driver calls; the REPL owns only the cancelable
		// ctx, display, and compaction. Three input modes:
		//   - anchored: the prompt is pinned to the bottom row and type-ahead
		//     echoes live above the streaming output (interactive + render);
		//   - capture: ESC/Ctrl-C cancel and mid-turn keystrokes are captured
		//     silently to seed the next prompt (interactive, raw streaming);
		//   - signal: piped input falls back to SIGINT for cancel.
		// The reply itself is printed by the coder sender (printCoderProse /
		// the live stream), not here — the REPL owns only the turn-boundary
		// receipt, display, and compaction.
		process()
		// Issue #108: prepend the mention attachment (if any) to the turn's
		// input so the model sees the file content (or outline) in context.
		turnInput := input
		if mentionAttachment != "" {
			turnInput = mentionAttachment + "\n" + input
		}
		// The turn opens with one blank line under the input it answers
		// (docs/tui-polish.md, track 2: a turn reads as one block).
		fmt.Println()
		var (
			err error
			res TurnResult
		)
		// Issue #218: the @mentioned images resolved above ride THIS turn, as
		// content parts on its user message (attachTurnImages owns what that
		// means per vision verdict). Passing them at the call rather than
		// prepending anything to turnInput is the point: an image is never text.
		switch {
		case editor != nil && anchoredInput():
			// runAnchoredTurn runs its own Turn and returns its result, so the
			// turn-boundary receipt below surfaces in this mode too.
			typeAhead, res, err = runAnchoredTurn(session, editor, turnInput, typeAhead, mentionImages...)
		case editor != nil:
			ctx, stop := editor.Interruptible(context.Background())
			res, err = session.TurnWithAttachments(ctx, turnInput, mentionImages...)
			typeAhead = stop()
		default:
			ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
			res, err = session.TurnWithAttachments(ctx, turnInput, mentionImages...)
			cancel()
		}
		// Issue #218: one line per image the human attached, printed after
		// submitting — the attachments that reached the model, and the ones
		// that did not with the reason. An image that silently vanished would
		// be indistinguishable from a typo'd mention, so silence is not an
		// option for either case.
		printMentionImages(mentionImages, mentionImageRefusals, res.ImageNotes)
		// Issue #141: surface the "tests changed" receipt to the user before
		// the shared post-turn safety net (afterTurn). The model has already
		// been told (via the finalize hook) and the journal carries it; the
		// human gets it here, in the terminal, on the turn that produced it —
		// the loss is reported where a reviewer would look.
		printTestReceipt(res.TestReceipt)
		// Issue #129 piece 3: the turn-end lint receipt rides the same
		// turn-boundary surface — the findings the model saw in the finalize
		// round reach the human in the terminal too.
		printLintReceipt(res.LintReceipt)
		// Issue #219: the measurement-only turn receipt — what the turn
		// actually left in the workspace (the git diff --stat block, the
		// project's own test/build exit codes, the format hook's knowledge) —
		// rides the same turn-boundary surface, measured, not claimed.
		printTurnReceipt(res.Receipt)
		// Issue #117: a turn that recovered from a mid-turn provider failure
		// SUCCEEDED (err is nil), so the backend's status/body was otherwise
		// lost behind the reply — one dim line, secrets redacted. afterTurn is
		// the safety net for the UNRECOVERED case (err != nil), already
		// handled there; this is the recovered case, its sibling.
		printBackendError(res.LastError)
		// Issue #103: a turn whose persisted messages carried a secret reports
		// how much was masked (dim provenance, not an alarm) — the same
		// per-turn count the journal capture's metadata and TurnResult carry.
		printRedactions(os.Stdout, res.Redactions)
		if err == nil {
			session.printTurnFooter()
		}
		afterTurn(session, err)
	}

	// Report and record the session. emitSessionMetrics rides the eval journal class.
	if session.turns > 0 {
		session.emitSessionMetrics()
		fmt.Println(style.Paint(session.sessionSummary(), style.Dim))
		// Pre-fill the resume command with this session's id so picking it back
		// up is copy-paste, not a hunt through .cortex/sessions/.
		if session.SessionID != "" {
			fmt.Println(style.Paint(fmt.Sprintf("resume: %s resume %s", invokedName(), session.SessionID), style.Dim))
		}
	}
	fmt.Println(style.Paint("exiting", style.Dim))
}
