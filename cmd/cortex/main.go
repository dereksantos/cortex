package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"runtime/debug"
	"strings"
	"time"

	"github.com/dereksantos/cortex/internal/lineedit"
	"github.com/dereksantos/cortex/internal/loopui"
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
[x] Eval 6a: per-session metrics (tokens/turns/captures/insights) → eval.cell_result + summary
[ ] Eval 6b: learning-loop eval runner (cold vs warm memory) — design next
[x] Compaction-as-study (red-gauge answer) + /clear + overflow recovery
[x] Retrieval injection at turn start (Fast/Reflex; ephemeral per-turn; Think later)
[ ] Integrate eval suite into new harness
[ ] cortex model for cataloging and suggesting model setups based on system resources
[ ] Later (after harness is stable): cortex dream / think / dag integration

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
)

const defaultModel = ModelCoder

// maxToolIterations bounds the agentic inner loop so a confused model can't
// spin forever burning tokens. The smallest form of the "bounded" principle.
const maxToolIterations = 100

// maxToolOutput caps how much tool output we feed back into context. Defined
// in the tools package; aliased here for the references in main.go.
const maxToolOutput = tools.MaxToolOutput

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
var Version = "0.1.0"

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
// ordinary source files still read whole; large files curate.
const curationBudgetTokens = tools.CurationBudgetTokens

// promptGlyph is the input affordance at the end of the status line.
const promptGlyph = tools.PromptGlyph

// Color palette and the NO_COLOR-aware wrapper live in the ui package;
// aliased here so the many call sites in main.go read unchanged.
const (
	red    = tools.Red
	cyan   = tools.Cyan
	green  = tools.Green
	blue   = tools.Blue
	yellow = tools.Yellow
	gray   = tools.Gray
	reset  = tools.Reset
)

func withColor(v string, c string) string { return tools.Color(v, c) }

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
	fmt.Println(withColor("Available tools:", cyan))
	for _, tool := range tools.All {
		fmt.Printf("  • %s\n", tool.Function.Name)
	}
	fmt.Println()
}

func main() {
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

	// Direct study mode: `cortex study <path> [goal...]`. The navigator subagent
	// reads what the goal needs; there are no deepening passes to configure.
	if len(os.Args) >= 3 && os.Args[1] == "study" {
		rest := os.Args[2:]
		runStudyCLI(rest[0], strings.Join(rest[1:], " "))
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

	session := NewCortexSession()

	// Print available tools on launch only when --tools was passed; the REPL
	// otherwise starts clean.
	if showTools {
		printAvailableTools()
	}

	// `cortex resume [id]` continues a prior session (the latest when no id is
	// given); otherwise every REPL session persists under a fresh transcript.
	if len(os.Args) >= 2 && os.Args[1] == "resume" {
		id := ""
		if len(os.Args) >= 3 {
			id = os.Args[2]
		}
		if err := session.ResumeTranscript(id); err != nil {
			fmt.Printf("resume: %v — starting fresh\n", err)
			session.StartTranscript()
		} else {
			session.showLoadedContext(session.SessionID)
		}
	} else {
		session.StartTranscript()
	}

	// Fast retrieval over .cortex/ (best-effort; disabled cleanly if the store
	// can't open). Shut down with the transcript at exit.
	session.EnableMemory()
	defer session.Close()

	// Interactive terminals get the raw-mode line editor (arrows, editing,
	// bracketed paste, ESC-to-interrupt). Piped/redirected input — tests, CI,
	// `printf … | loop` — falls back to the plain scanner unchanged.
	var editor *lineedit.Terminal
	var scanner *bufio.Scanner
	if lineedit.IsInteractive(os.Stdin) {
		if t, err := lineedit.Open(os.Stdin, os.Stdout); err == nil {
			editor = t
			editor.SetHistory(lineedit.LoadHistory(filepath.Join(contextDir(), "history")))
			defer editor.Close()
			// Risky-command confirmation reads a y/N line from the editor. Tool
			// calls run synchronously on this goroutine between ReadLine calls,
			// so there's no concurrent reader to fight. Headless/piped sessions
			// leave this nil and the gate blocks risky commands instead.
			session.confirmRisky = func(question string) bool {
				// During an anchored turn the Anchor's key loop owns the terminal;
				// a second reader (editor.ReadLine) fights it for the keystroke and
				// the y/N never lands — the user can't approve and resorts to Ctrl-C.
				// Route the confirm through the anchor, which serves it from the same
				// loop. Fall back to a direct read only when no turn is pinned.
				if a := session.live; a != nil {
					return a.Confirm(question)
				}
				ans, err := editor.ReadLine(question)
				if err != nil {
					return false
				}
				switch strings.ToLower(strings.TrimSpace(ans)) {
				case "y", "yes":
					return true
				default:
					return false
				}
			}
			// Stray stdlib-logger output (any subsystem that calls log.Printf)
			// goes to stderr, which in an interactive session lands on top of
			// the prompt. Divert it to a file so the terminal stays clean
			// (tail -f .cortex/cortex.log for debugging).
			if lf, err := os.OpenFile(filepath.Join(contextDir(), "cortex.log"),
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
			line, err := editor.ReadLinePrefilled(session.Prompt(), typeAhead)
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
		// fresh prompt's first ↑ lands on real work, not "/quit".
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
			fmt.Println(withColor("cleared → session "+session.SessionID, gray))
			continue
		}
		if input == "/compact" {
			compactNow(session, "manual compact")
			continue
		}

		// /sessions lists saved sessions so their ids are discoverable from
		// inside the REPL (resuming still happens at startup).
		if input == "/sessions" {
			session.printSessions()
			continue
		}

		// Memory is now model-driven: ask in natural language ("remember that …",
		// "forget the … note") and the agent calls memory_write / memory_forget.
		// The old /remember and /forget slash commands were removed with the
		// mechanical capture/retract pipeline. See docs/memory-tools.md.

		// /model [name] shows the role bindings, or switches the coding model.
		if input == "/model" || strings.HasPrefix(input, "/model ") {
			name := strings.TrimSpace(strings.TrimPrefix(input, "/model"))
			if name == "" {
				fmt.Printf("code:  %s @ %s\nstudy: %s @ %s\n",
					session.Request.Model, session.Request.BaseURL,
					session.Study.Model, session.Study.Endpoint)
			} else {
				session.SetModel(name)
				fmt.Printf("code model → %s\n", name)
			}
			continue
		}

		// Run the turn. The whole per-turn pipeline lives in Turn now — the same
		// entry point a headless driver calls; the REPL owns only the cancelable
		// ctx, display, and compaction. Three input modes:
		//   - anchored: the prompt is pinned to the bottom row and type-ahead
		//     echoes live above the streaming output (interactive + render);
		//   - capture: ESC/Ctrl-C cancel and mid-turn keystrokes are captured
		//     silently to seed the next prompt (interactive, raw streaming);
		//   - signal: piped input falls back to SIGINT for cancel.
		var err error
		switch {
		case editor != nil && anchoredInput():
			typeAhead, err = runAnchoredTurn(session, editor, input, typeAhead)
		case editor != nil:
			ctx, stop := editor.Interruptible(context.Background())
			_, err = session.Turn(ctx, input)
			typeAhead = stop()
		default:
			ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
			_, err = session.Turn(ctx, input)
			cancel()
		}
		switch {
		case err == nil:
			// Red gauge: compact at the turn boundary, before the window
			// actually overflows. The boundary is the only safe point —
			// mid-turn compaction would orphan tool_call sequences.
			if session.contextRatio() >= compactThreshold {
				compactNow(session, fmt.Sprintf("context at %.0f%%", 100*session.contextRatio()))
			}
		case errors.Is(err, context.Canceled):
			fmt.Println(withColor("interrupted", yellow))
		default:
			fmt.Printf("turn error: %v\n", err)
			// An overflow error names the code model's real window: learn it
			// (the gauge and read_file guard self-correct) and compact so the
			// next request fits. The failed request is in the digest; the
			// user re-asks.
			if real := parseCtxSize(err.Error()); real > 0 {
				session.Window = real
				compactNow(session, "context overflowed")
				fmt.Println("please re-send your request")
			}
		}
	}

	// Report and record the session. emitSessionMetrics rides the eval journal class.
	if session.turns > 0 {
		session.emitSessionMetrics()
		fmt.Println(withColor(session.sessionSummary(), gray))
		// Pre-fill the resume command with this session's id so picking it back
		// up is copy-paste, not a hunt through .cortex/sessions/.
		if session.SessionID != "" {
			fmt.Println(withColor(fmt.Sprintf("resume: %s resume %s", invokedName(), session.SessionID), gray))
		}
	}
	fmt.Println(withColor("exiting", gray))
}
