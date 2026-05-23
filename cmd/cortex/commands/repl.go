//go:build !windows

// Package commands — `cortex` (no subcommand) drops into an interactive
// coding REPL rooted at the current working directory.
//
// Design intent (see PROGRESS-REPL.md + docs/repl.md):
//
//   - Bare `cortex` is the primary user-facing entry. Cwd is the workdir.
//     Default model is qwen2.5-coder:1.5b via Ollama. Type a request,
//     the harness does work, the verifier runs, you see a one-line
//     status, you type the next thing.
//
//   - The REPL reuses the in-process Cortex harness (the same
//     evalv2.CortexHarness that `cortex code` drives). It does NOT shell
//     out to `cortex code` — that would be principle-1 compliant but
//     wasteful when we already own the process. Each turn invokes
//     RunSessionWithResult once with the workdir held constant; agent
//     memory across turns flows via the filesystem + cortex_search.
//
//   - Tuned for tiny models: tight system prompt, per-turn output cap,
//     auto-detect verifier (Go-only for v1), auto-retry once on
//     verify-fail with the error in context.
//
//   - Slash commands (/help, /diff, /undo, /model) cover the steering
//     loop. /undo restores from a pre-turn file snapshot under
//     .cortex/sessions/<ts>/snapshots/turn-<n>/ — no git required.
package commands

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/dereksantos/cortex/internal/capture"
	intcognition "github.com/dereksantos/cortex/internal/cognition"
	"github.com/dereksantos/cortex/internal/eval/dagtrace"
	evalv2 "github.com/dereksantos/cortex/internal/eval/v2"
	"github.com/dereksantos/cortex/internal/harness"
	"github.com/dereksantos/cortex/internal/harness/dagnode"
	"github.com/dereksantos/cortex/internal/journal"
	intllm "github.com/dereksantos/cortex/internal/llm"
	"github.com/dereksantos/cortex/internal/processor"
	"github.com/dereksantos/cortex/internal/repltui"
	"github.com/dereksantos/cortex/internal/storage"
	"github.com/dereksantos/cortex/internal/study"
	"github.com/dereksantos/cortex/pkg/cliout"
	"github.com/dereksantos/cortex/pkg/cognition"
	"github.com/dereksantos/cortex/pkg/cognition/dag"
	"github.com/dereksantos/cortex/pkg/cognition/dag/ops"
	"github.com/dereksantos/cortex/pkg/config"
	"github.com/dereksantos/cortex/pkg/events"
	"github.com/dereksantos/cortex/pkg/llm"
	"github.com/dereksantos/cortex/pkg/secret"
)

func init() {
	Register(&REPLCommand{})
}

const (
	// defaultREPLModel is the tiny local model the REPL targets out of
	// the box. Override with --model or env CORTEX_REPL_MODEL.
	defaultREPLModel = "qwen2.5-coder:1.5b"

	// defaultOllamaAPIURL is the OpenAI-compat endpoint Ollama exposes
	// on the standard port. We auto-route to this when the model id
	// has no provider prefix (no slash → not OpenRouter). Aliased to
	// llm.DefaultOllamaURL so the URL has a single source of truth.
	defaultOllamaAPIURL = llm.DefaultOllamaURL

	// defaultMaxOutputTokens caps a single turn's output. Small enough
	// to force the model to take one focused step at a time, large
	// enough to write a full file in one go (Game-of-Life-sized).
	defaultMaxOutputTokens = 4000

	// defaultMaxTurns is the REPL's safety ceiling, NOT the binding
	// stop. Post-Phase-3 the agent loop's no-progress signal (see
	// internal/harness.noProgressWindow) raises ReasonNoProgress when
	// recent turns showed no write_file/run_shell work or read in a
	// circle. Budget caps cost. The integer cap here exists only so
	// truly pathological loops (provider drift, dispatcher bugs) stop
	// in bounded time; everyday exploration runs well under it.
	defaultMaxTurns = 50

	// defaultToolOutputSalienceCap is the FALLBACK per-tool-call
	// output-token cap when capability inference can't pick a class
	// for the running model. The active cap is normally chosen by
	// salienceCapForModel from the model id + endpoint's
	// max_context_window (Phase 3 Slice 1) — smaller for weaker
	// models, larger for stronger ones. Zero would disable
	// compression entirely; we keep this medium cap as a safe
	// floor.
	defaultToolOutputSalienceCap = 500

	// snapshotMaxFileBytes skips files above this when snapshotting
	// for /undo. Big binaries shouldn't round-trip through .cortex.
	snapshotMaxFileBytes = 1 << 20 // 1 MiB

	// envREPLModel lets users pin a default model without retyping
	// --model every invocation.
	envREPLModel = "CORTEX_REPL_MODEL"
)

// REPLCommand is the bare-`cortex` entry point.
type REPLCommand struct{}

// Name returns the registered command name. Note: main.go also routes
// the no-arg case (len(os.Args) < 2) into this command, so the user
// never has to type "cortex repl" — they just type "cortex".
func (c *REPLCommand) Name() string { return "repl" }

// Description returns the one-liner shown in `cortex help`.
func (c *REPLCommand) Description() string {
	return "Start an interactive coding REPL in the current directory (default model: qwen2.5-coder:1.5b via Ollama)"
}

// Execute parses flags, sets up session state, and runs the REPL loop
// until EOF or /quit.
//
// Headless mode (for benchmark harnesses) is opted into via --prompt:
// the REPL skips the stdin scanner, runs runTurn(--prompt) once with
// the configured verifier + retry budget, optionally emits a JSON
// summary, and exits. --auto-retry suppresses the interactive
// [r/e/s/q] gate so the loop never blocks on stdin. --verifier and
// --max-retries let the caller override the v1 hardcoded
// "go build, one auto-retry" defaults so a SWE-bench / pytest /
// arbitrary verifier can drive the same loop.
func (c *REPLCommand) Execute(ctx *Context) error {
	// Track whether the user pinned the model. Both env-var and --model
	// count as explicit — auto-upgrade only happens when the model
	// defaulted from compile-time constants.
	model := os.Getenv(envREPLModel)
	userPinned := model != ""
	if model == "" {
		model = defaultREPLModel
	}
	verbose := false

	// Headless-mode config — all default to "interactive REPL as
	// before" when unset. See doc comment above.
	oneShotPrompt := ""
	customVerifier := ""
	autoRetry := false
	maxRetries := 1
	jsonOutput := false
	workdirOverride := ""
	systemPromptOverride := ""   // --system-prompt FILE: path to a system prompt that overrides the auto-seeded one
	maxTurnsOverride := 0        // --max-turns N: override the per-attempt agent-loop cap (default 8)
	maxCostOverride := 0.0       // --max-cost-usd X: override the per-attempt USD budget (default 0.20)
	maxCumulativeOverride := 0   // --max-cumulative-tokens N: override the per-attempt token budget (default 300000)
	fullTools := false           // --full-tools: kept as a no-op alias since full surface is the iter-7 default
	minimalTools := false        // --minimal-tools: explicit opt-in to 3-tool registry for users on tiny Ollama models
	keepOnFail := false          // --keep-on-fail: do not roll back the workdir when the verifier fails (benchmark default)
	historyTurnsOverride := -1   // --history-turns N: cap on conversation-history block (-1 = use default, 0 = disabled)
	priorSummariesOverride := -1 // --prior-summaries N: cap on prior-session summaries (-1 = use default, 0 = disabled)

	args := ctx.Args
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "-m", "--model":
			if i+1 < len(args) {
				model = args[i+1]
				userPinned = true
				i++
			}
		case "-v", "--verbose":
			verbose = true
		case "--prompt":
			if i+1 < len(args) {
				oneShotPrompt = args[i+1]
				i++
			}
		case "--verifier":
			if i+1 < len(args) {
				customVerifier = args[i+1]
				i++
			}
		case "--auto-retry":
			autoRetry = true
		case "--max-retries":
			if i+1 < len(args) {
				fmt.Sscanf(args[i+1], "%d", &maxRetries)
				i++
			}
		case "--json":
			jsonOutput = true
		case "--workdir":
			if i+1 < len(args) {
				workdirOverride = args[i+1]
				i++
			}
		case "--system-prompt":
			if i+1 < len(args) {
				systemPromptOverride = args[i+1]
				i++
			}
		case "--max-turns":
			if i+1 < len(args) {
				fmt.Sscanf(args[i+1], "%d", &maxTurnsOverride)
				i++
			}
		case "--max-cost-usd":
			if i+1 < len(args) {
				fmt.Sscanf(args[i+1], "%f", &maxCostOverride)
				i++
			}
		case "--max-cumulative-tokens":
			if i+1 < len(args) {
				fmt.Sscanf(args[i+1], "%d", &maxCumulativeOverride)
				i++
			}
		case "--full-tools":
			fullTools = true
		case "--minimal-tools":
			minimalTools = true
		case "--keep-on-fail":
			keepOnFail = true
		case "--history-turns":
			if i+1 < len(args) {
				fmt.Sscanf(args[i+1], "%d", &historyTurnsOverride)
				i++
			}
		case "--prior-summaries":
			if i+1 < len(args) {
				fmt.Sscanf(args[i+1], "%d", &priorSummariesOverride)
				i++
			}
		case "-h", "--help":
			printREPLHelp()
			return nil
		}
	}

	workdir := workdirOverride
	if workdir == "" {
		var err error
		workdir, err = os.Getwd()
		if err != nil {
			return fmt.Errorf("getwd: %w", err)
		}
	} else {
		abs, err := filepath.Abs(workdir)
		if err != nil {
			return fmt.Errorf("abs workdir: %w", err)
		}
		workdir = abs
	}

	// Per-project default model: if neither CORTEX_REPL_MODEL nor
	// --model was passed and the project's .cortex/config.json
	// declares a DefaultModel, use that. Treats config-set models as
	// effectively pinned (skips the Ollama auto-pick probe below)
	// because if the operator wrote it down, they meant it. Falls
	// through to the compile-time defaultREPLModel when no config
	// override is set.
	if !userPinned {
		if cfg := loadREPLConfig(filepath.Join(workdir, ".cortex")); cfg != nil && cfg.DefaultModel != "" {
			model = cfg.DefaultModel
			userPinned = true
		}
	}

	// One Sink per session — owns every Info/Warn/Error/Banner/Event
	// and ReadLine call site downstream. The interactive REPL always
	// uses the bubbletea-backed TUI sink; the headless --prompt path
	// stays on stdout so benchmark harnesses and pipe-based callers
	// see the legacy line-by-line output they expect.
	var (
		ui     cliout.Sink
		tuiSnk *repltui.TUISink
	)
	if oneShotPrompt == "" {
		tuiSnk = repltui.NewTUISink(verbose)
		ui = tuiSnk
	} else {
		ui = cliout.New(verbose)
	}

	// Fix A: when the user hasn't pinned a model AND we're routing to
	// Ollama, probe `/api/tags` and prefer a better function-caller if
	// one is installed. Falls back silently to the default if Ollama
	// is unreachable or only weak models are available. Always prints
	// the auto-swap note when it happens so the user knows what's
	// running. Ollama-unreachable triggers a one-line warn per
	// criterion 2.
	apiURL := resolveAPIURL(model)
	if apiURL == defaultOllamaAPIURL && !userPinned {
		if chosen, ok, note := probeOllamaAndPickModel(apiURL, model); ok {
			if chosen != model {
				model = chosen
				ui.Info(note)
			}
		} else {
			ui.Warn("Ollama unreachable at " + apiURL + " (model calls will fail until it's started)")
		}
	}

	state, err := newREPLState(workdir, model, verbose, ui)
	if err != nil {
		return err
	}
	defer state.close()
	state.customVerifierCmd = customVerifier
	state.headless = autoRetry
	if maxRetries > 0 {
		state.maxRetries = maxRetries
	}
	state.maxTurns = maxTurnsOverride
	state.maxCostUSD = maxCostOverride
	state.maxCumulativeTokens = maxCumulativeOverride
	state.fullTools = fullTools
	state.minimalTools = minimalTools
	state.keepOnFail = keepOnFail
	if historyTurnsOverride >= 0 {
		state.historyLimit = historyTurnsOverride
	} else {
		state.historyLimit = defaultHistoryLimit
	}
	if priorSummariesOverride >= 0 {
		state.priorSessionsCap = priorSummariesOverride
	} else {
		state.priorSessionsCap = defaultPriorSessionsCap
	}
	// Override the auto-seeded system prompt when the caller pinned
	// one. Benchmark harnesses use this to swap the Go-flavored
	// default for a language/repo-appropriate prompt (e.g. SWE-bench
	// on a Django repo wants Python tooling guidance, not `go build`).
	if systemPromptOverride != "" {
		b, rerr := os.ReadFile(systemPromptOverride)
		if rerr != nil {
			return fmt.Errorf("read --system-prompt %s: %w", systemPromptOverride, rerr)
		}
		state.systemPrompt = string(b)
	}

	// Headless one-shot path. Skips the stdin scanner entirely:
	// runTurn runs once with --prompt, the verifier + retry budget
	// drive the loop to completion (no human gate), and we either
	// emit a JSON summary (--json) or rely on the standard turn
	// summary printed at finalize-time.
	if oneShotPrompt != "" {
		if !jsonOutput {
			printREPLBanner(state)
		}
		turnErr := state.runTurn(oneShotPrompt)
		if turnErr != nil && !jsonOutput {
			state.ui.Error(fmt.Errorf("turn: %w", turnErr))
		}
		if jsonOutput {
			emitOneShotJSON(ctx, state, turnErr)
		} else {
			state.ui.Info(fmt.Sprintf("\nsession saved → %s", state.sessionPath))
		}
		return nil
	}

	// mainLoop is the heart of the interactive REPL — read a line,
	// dispatch slash or runTurn, repeat. Factored out so both the
	// stdout path (run directly) and the TUI path (run inside
	// repltui.Run's worker goroutine) share the same body.
	mainLoop := func() error {
		printREPLBanner(state)
		maybeStartStudy(state)
		maybeStartJournalIngest(state)
		revalidateAndWarn(state.workdir, state.ui)
		for {
			line, err := state.ui.ReadLine("~ ")
			if err != nil {
				if !errors.Is(err, io.EOF) {
					state.ui.Error(err)
				}
				break
			}
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			if strings.HasPrefix(line, "/") {
				cont, err := state.dispatchSlash(line)
				if err != nil {
					state.ui.Error(err)
				}
				if !cont {
					break
				}
				continue
			}
			if err := state.runTurn(line); err != nil {
				state.ui.Error(fmt.Errorf("turn: %w", err))
			}
			if state.exitRequested {
				break
			}
		}
		state.ui.Info(fmt.Sprintf("\nsession saved → %s", state.sessionPath))
		return nil
	}

	if tuiSnk != nil {
		// TUI path. repltui.Run starts the bubbletea program on this
		// goroutine and runs mainLoop in a worker goroutine; sink
		// messages route between them. The initial status line
		// mirrors the printREPLBanner content.
		initialStatus := fmt.Sprintf("cortex · %s · %s", state.workdir, state.model)
		return repltui.Run(tuiSnk, initialStatus, mainLoop)
	}
	return mainLoop()
}

// replState bundles per-session mutable state: model + workdir + the
// session JSONL writer + turn counter + last snapshot for /undo.
type replState struct {
	workdir string
	model   string
	apiURL  string
	verbose bool

	// systemPrompt is the worker-model system prompt for coding_turn's
	// agent loop. Binary-first: defaultREPLSystemPrompt is the source
	// of truth; .cortex/repl-system-prompt.local.md (when present) is
	// an opt-in user override. Held in memory across the session.
	systemPrompt string

	// sessionDir is .cortex/sessions/<ts>/, the per-invocation root for
	// the JSONL transcript and per-turn snapshots.
	sessionDir  string
	sessionPath string // <sessionDir>/session.jsonl
	jsonl       *os.File

	// turns is the 1-indexed counter of accepted turns. Snapshots and
	// jsonl rows reference this number.
	turns int

	// snapshotStack holds the pre-turn snapshot dir for each accepted
	// turn, in chronological order. /undo pops the top entry, allowing
	// chained undo back to session start. Empty before the first turn.
	snapshotStack []string

	// captureCfg + captureClient are lazily constructed on first
	// accepted turn so the capture write doesn't pay setup cost when
	// the REPL is just used for read-only exploration (no edits, no
	// captures).
	captureCfg    *config.Config
	captureClient *capture.Capture

	// store + cortex are the shared cognition surface for this REPL
	// session. They're built once at newREPLState and reused by (a)
	// captureClient — events written by the turn loop land in the
	// same Storage that — (b) the harness's cortex_search tool
	// retrieves from. Without this sharing the tool's Storage would
	// be a separate in-memory index that never sees session captures
	// and cortex_search returns empty forever.
	//
	// Constructed eagerly because the LLM-provider arg needed by
	// Cortex (for Full mode's synchronous Reflect) requires key
	// resolution we'd rather fail fast on. nil store/cortex is the
	// signal that this session opted out of the shared path (e.g.
	// future readonly mode).
	store  *storage.Storage
	cortex *intcognition.Cortex

	// sessionID is a short random identifier shared across all turn
	// rows + capture events in a single REPL invocation. Lets analysis
	// group "everything done in one session" without parsing paths.
	sessionID string

	// exitRequested is set by the /quit gate-response path so runTurn
	// can signal Execute to break the loop cleanly.
	exitRequested bool

	// lastClarifyPrompt is the original ambiguous user prompt from the
	// most recent turn that routed to decide.clarify. The next turn's
	// stitchClarifyFollowUp combines it with the user's answer so the
	// classifier sees the disambiguated request instead of a fragment
	// like "in main.go". Cleared after consumption — only the
	// immediately-following turn benefits from the stitch.
	lastClarifyPrompt string

	// studyCancel cancels the auto-spawned study goroutine on
	// REPL exit. Nil when study was skipped (already complete, env
	// override, or detection error). Always called from close(); safe
	// to call multiple times because the underlying CancelFunc is.
	studyCancel context.CancelFunc

	// ingestCancel cancels the journal-ingest goroutine that drains
	// .cortex/journal/<class>/ segments into Storage on a ticker. The
	// daemon used to own this; now the REPL does (daemon retirement
	// Phase 2.1). Nil when ingest didn't start (no store, etc.).
	ingestCancel context.CancelFunc

	// Headless-mode config (zero values preserve interactive behavior):
	//   customVerifierCmd: shell command to run instead of `go build`.
	//   headless:          skip promptGate; treat unresolved verify-fail
	//                      as the final state for the turn.
	//   maxRetries:        total auto-retry attempts (default 1 = the
	//                      original "one auto-retry" behavior; values >1
	//                      let the loop iterate further with verifier
	//                      output fed back each round).
	customVerifierCmd string
	headless          bool
	maxRetries        int
	// Optional per-attempt budget overrides. Zero = inherit the REPL
	// defaults (defaultMaxTurns / defaults from internal/harness).
	// Benchmark harnesses bump these because SWE-bench-class repo
	// exploration blows past the interactive-mode defaults in 4-5
	// list_dir + read_file turns.
	maxTurns            int
	maxCostUSD          float64
	maxCumulativeTokens int
	// fullTools is a no-op alias kept for backward compat — the
	// default tool surface is now full (iter-7 default flip). Old
	// scripts that pass --full-tools continue to work without
	// effect.
	fullTools bool
	// minimalTools opts INTO the 3-tool registry (read_file +
	// write_file + run_shell) for users still running tiny Ollama
	// models that lose function-call discipline at ≥5 tools. Default
	// false; only set when the user passes --minimal-tools.
	minimalTools bool
	// keepOnFail suppresses runTurn's snapshot rollback when verify
	// fails. For interactive REPL use the default (rollback) is
	// right: don't keep half-broken edits. For benchmark harnesses
	// it's wrong — a real engineer iterating on a failing test
	// keeps their changes and refines, doesn't reset to scratch
	// every attempt. SWE-bench in particular needs this so the
	// agent's file writes persist across retries and the final
	// scorer sees the actual attempt rather than an empty diff.
	keepOnFail bool

	// ui is the I/O sink for everything the REPL displays or asks for.
	// All Info/Warn/Error/Banner/Event/ReadLine call sites route
	// through this so the rendering (stdout today, Bubble Tea TUI
	// later) is swappable from a single point. Always non-nil after
	// newREPLState — defaults to cliout.StdoutSink when the caller
	// doesn't inject another.
	ui cliout.Sink

	// historyLimit caps the conversation-history block sent to the
	// model on each turn. Default defaultHistoryLimit; configurable
	// via --history-turns N at startup or /history N mid-session.
	// 0 disables history injection entirely (turn 1 behavior).
	historyLimit int

	// priorSessionsCap bounds the count of think.session_summary
	// entries from PRIOR sessions injected into the harness's prior-
	// messages block. Defaults to defaultPriorSessionsCap; 0 disables
	// cross-session injection entirely (the pre-Item-4 behavior).
	// Configurable via --prior-summaries N.
	priorSessionsCap int
	// history is the per-session conversation buffer: one entry per
	// accepted turn (user prompt + assistant final text), in
	// chronological order. The tail (most recent historyLimit
	// entries) becomes the harness's PriorMessages block on the next
	// turn so the model has working memory beyond what cortex_search
	// surfaces. /undo pops the last entry alongside the snapshot.
	history []turnExchange

	// openRouterModelsCache holds the result of the most recent
	// OpenRouter /api/v1/models fetch for the /models slash command.
	// nil = never fetched (or last fetch errored — see cacheErr).
	// Cached per-session because the catalogue is large (~300+
	// entries) and changes on hour/day timescales, not request-time
	// timescales. Refreshes only on explicit /models refresh.
	openRouterModelsCache []llm.OpenRouterModel
	openRouterModelsErr   error

	// modelCatalogCache holds the formatted model-catalog string
	// injected into decide.next's prompt at call time. Computed on
	// first use, reused across turns. Invalidated by /model swap and
	// /models refresh — the next runREPLChainTurn rebuilds it.
	modelCatalogCache string

	// registry is the unified ModelRegistry over this session's
	// configured backends (Ollama + any compat endpoints in
	// .cortex/config.json + OpenRouter when a key is present). Built
	// once at newREPLState and reused by every consumer that needs
	// to know "what models exist" — modelCatalogForREPL, the Ollama
	// auto-pick probe, and (Phase B) act.read_file's size-vs-window
	// decision.
	registry llm.ModelRegistry
}

// turnExchange is one accepted turn condensed to just what the model
// needs to see on the next turn: the user's message and the assistant's
// final text. No tool-call traces — those are noise and burn tokens.
//
// TODO (history drops tool grounding): "tool-call traces are noise" is
// half-true — the raw trace is noisy, but the discoveries inside it
// ("read pkg/foo/bar.go and learned func X takes Y") are exactly the
// context that justifies not re-exploring on the next turn. With only
// {User, Assistant} the model has to rediscover the workdir each turn.
// Options: (a) append a compact "observations" line per turn — files
// read, tests run, key findings — summarized by a cheap reflect.* node
// at finalize; (b) keep the structured trace and let priorMessagesFor-
// Harness pick the salient parts per next-prompt similarity. (a) is
// the smaller slice; (b) is the learning-harness shape.
type turnExchange struct {
	User      string
	Assistant string
}

// defaultHistoryLimit is the default cap on the conversation-history
// block injected into each turn. 6 = three user/assistant pairs of
// recent context, which is enough for "now do the same for X" patterns
// without burning a large chunk of the context window.
const defaultHistoryLimit = 6

// defaultPriorSessionsCap bounds how many think.session_summary
// entries from PRIOR sessions are injected on REPL start. Phase 3
// Slice 4 stored them per-session; Item 4 (the cross-session memory
// follow-up) lifted the filter so they flow in. The cap is small
// because cross-session bleed is high-leverage context per token but
// also the easiest way to drown the prompt in stale "back in
// January we decided X" lines. Override at startup via
// --prior-summaries N (0 disables).
const defaultPriorSessionsCap = 3

// newREPLState performs auto-init: creates .cortex/ if missing, the
// session dir, the JSONL writer, and seeds the system prompt file if
// absent. Returns an error if any of these fail — we'd rather refuse
// to start than run in an inconsistent state.
//
// ui may be nil; the state falls back to cliout.New(verbose) which
// is the stdout-bound implementation. Callers wanting a TUI or test
// double inject their own here.
func newREPLState(workdir, model string, verbose bool, ui cliout.Sink) (*replState, error) {
	cortexDir := filepath.Join(workdir, ".cortex")
	if err := os.MkdirAll(cortexDir, 0o755); err != nil {
		return nil, fmt.Errorf("init .cortex/: %w", err)
	}

	// Binary-first: the in-binary defaultREPLSystemPrompt is the source
	// of truth. .local.md is an opt-in user override. Earlier versions
	// seeded .cortex/repl-system-prompt.md on first run; those legacy
	// files are silently ignored.
	// TODO: prefer AGENTS.md (the emerging cross-tool standard) over a
	// Cortex-private .local.md override. Look for ./AGENTS.md at the
	// project root first, fall back to .cortex/repl-system-prompt.local.md
	// for back-compat.
	promptPath := filepath.Join(cortexDir, "repl-system-prompt.local.md")
	systemPrompt, err := loadOrSeedSystemPrompt(promptPath)
	if err != nil {
		return nil, err
	}

	ts := time.Now().UTC().Format("20060102T150405Z")
	sessionDir := filepath.Join(cortexDir, "sessions", ts)
	if err := os.MkdirAll(sessionDir, 0o755); err != nil {
		return nil, fmt.Errorf("init session dir: %w", err)
	}
	sessionPath := filepath.Join(sessionDir, "session.jsonl")
	f, err := os.OpenFile(sessionPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return nil, fmt.Errorf("open session jsonl: %w", err)
	}

	apiURL := resolveAPIURL(model)

	// Shared cognition surface. Captures from the turn loop and reads
	// from the cortex_search tool both target this Storage/Cortex —
	// that's the wire that makes intra-session learning measurable at
	// all (without it, a capture from turn 1 isn't visible to a
	// retrieval on turn 2 because each consumer would have opened its
	// own in-memory Storage).
	//
	// Failures here are NOT fatal: a session with no cognition surface
	// still runs (we just lose the auto-capture pipeline). REPL
	// readonly use cases or environments without an LLM key still get
	// a working REPL — the captureClient simply runs without storage
	// attached and falls back to journal-only persistence.
	store, cortex, cogErr := newSessionCognition(cortexDir, model, apiURL)
	if cogErr != nil && verbose {
		ui.Warn(fmt.Sprintf("cortex auto-capture disabled (%v)", cogErr))
	}

	// Phase 3 Slice 5: pull the calibrated per-class salience cap
	// override if a snapshot exists. Missing file is fine — the
	// static SalienceCapForClass defaults stay in force.
	applySalienceCalibration(cortexDir, verbose, ui)

	if ui == nil {
		ui = cliout.New(verbose)
	}
	registry := buildREPLRegistry(cortexDir, apiURL, ui, verbose)
	return &replState{
		workdir:      workdir,
		model:        model,
		apiURL:       apiURL,
		verbose:      verbose,
		ui:           ui,
		systemPrompt: systemPrompt,
		sessionDir:   sessionDir,
		sessionPath:  sessionPath,
		jsonl:        f,
		sessionID:    ts,
		store:        store,
		cortex:       cortex,
		registry:     registry,
	}, nil
}

// buildREPLRegistry composes the per-session ModelRegistry from the
// configured backends: Ollama at apiURL (when it looks Ollama-shaped),
// each OpenAI-compat endpoint declared in .cortex/config.json, and
// OpenRouter (when the keychain has a key). Errors from individual
// probes don't fail the build — they just shrink the catalogue and
// surface via ui.Warn when verbose.
//
// 5-minute TTL is generous enough for the per-turn catalog injection
// to never re-probe under typical use, and short enough that a fresh
// `ollama pull` surfaces within the same session without manual
// refresh.
func buildREPLRegistry(cortexDir, apiURL string, ui cliout.Sink, verbose bool) llm.ModelRegistry {
	cfg := loadREPLConfig(cortexDir)
	probes := []llm.Probe{}

	// Ollama probe — always include for the local-default path. The
	// apiURL may point at a non-default host (CORTEX_OLLAMA_URL or
	// /endpoint override), so derive the base from there.
	probes = append(probes, llm.NewOllamaProbe(llm.OllamaProbeConfig{
		BaseURL: trimOllamaChatSuffix(apiURL),
	}))

	// OpenAI-compat endpoints — one probe per configured entry.
	if cfg != nil {
		for _, ep := range cfg.Endpoints {
			probes = append(probes, llm.NewOpenAICompatProbe(llm.OpenAICompatProbeConfig{
				Endpoint: llm.EndpointConfig{
					Name:    ep.Name,
					BaseURL: ep.BaseURL,
					APIKey:  ep.APIKey,
				},
				IsLocal: true, // configured endpoints are assumed local/LAN today
			}))
		}
	}

	// OpenRouter — only when a key is resolvable.
	if key, _, err := secret.MustOpenRouterKey(); err == nil && key != "" {
		probes = append(probes, llm.NewOpenRouterProbe(llm.OpenRouterProbeConfig{
			APIKey: key,
			Cfg:    cfg,
		}))
	}

	onError := llm.ErrorReporter(nil)
	if verbose {
		onError = func(name string, err error) {
			ui.Warn(fmt.Sprintf("registry probe %s: %v", name, err))
		}
	}
	return llm.NewCompositeRegistry(llm.RegistryConfig{
		Probes:       probes,
		TTL:          5 * time.Minute,
		ProbeTimeout: 5 * time.Second,
		OnError:      onError,
	})
}

// trimOllamaChatSuffix returns the Ollama root URL given either a root
// or a /v1/chat/completions URL. Used to derive the /api/tags base for
// the OllamaProbe from the REPL's apiURL.
func trimOllamaChatSuffix(u string) string {
	const suffix = "/v1/chat/completions"
	if n := len(u) - len(suffix); n >= 0 && u[n:] == suffix {
		return u[:n]
	}
	return u
}

// loadREPLConfig loads the user's config from <cortexDir>/config.json
// when present, falling back to a minimal in-memory config bound to
// the project paths. Tolerant: a missing or unreadable file is not an
// error — the REPL keeps working without endpoint registry features.
//
// Phase 4: this is the seam where Endpoints + Models reach the REPL.
// When the file isn't there, ResolveModelRoute returns no matches and
// routing falls back to the legacy slash heuristic — i.e. existing
// users see no behavior change until they author a config.json.
func loadREPLConfig(cortexDir string) *config.Config {
	configPath := filepath.Join(cortexDir, "config.json")
	if cfg, err := config.Load(configPath); err == nil && cfg != nil {
		// Load may return a partial config — make sure the paths are
		// populated even if the file omitted them.
		if cfg.ContextDir == "" {
			cfg.ContextDir = cortexDir
		}
		if cfg.ProjectRoot == "" {
			cfg.ProjectRoot = filepath.Dir(cortexDir)
		}
		return cfg
	}
	return &config.Config{ContextDir: cortexDir, ProjectRoot: filepath.Dir(cortexDir)}
}

// newSessionCognition builds the shared Storage + Cortex pair for one
// REPL session. The Cortex carries an LLM provider so cortex_search's
// Full mode (synchronous Reflect) can actually call out. For Ollama
// routes the provider is an OpenRouter client pointed at the local
// chat endpoint (it speaks the OpenAI-compat protocol Ollama exposes);
// for OpenRouter routes the keychain key is required.
//
// Returns (nil, nil, err) on any setup failure — caller decides
// whether to abort the session or continue without auto-capture.
func newSessionCognition(cortexDir, model, apiURL string) (*storage.Storage, *intcognition.Cortex, error) {
	cfg := loadREPLConfig(cortexDir)
	store, err := storage.New(cfg)
	if err != nil {
		return nil, nil, fmt.Errorf("storage init: %w", err)
	}

	provider := buildLLMProviderForREPL(cfg, model, apiURL)
	// provider may still be nil — Cortex tolerates that (Full mode
	// degrades to Fast with a flagged response).

	cortex, err := intcognition.New(store, provider, nil, cfg)
	if err != nil {
		_ = store.Close()
		return nil, nil, fmt.Errorf("cognition init: %w", err)
	}
	return store, cortex, nil
}

// modelCatalogForREPL returns the formatted model-catalog string for
// decide.next's prompt. Lazy + cached per-session — first call probes
// Ollama (fast, local) and OpenRouter (slow, remote, cached on the
// llm side too); subsequent calls reuse the string. Invalidate via
// resetModelCatalog when /model swaps or /models refresh.
//
// Format: local-first list, then a small curated OpenRouter sample
// (the full 300+ catalogue would dominate the prompt). The LLM
// picks per-node models from this list via the attrs.model field;
// names absent from the list still work because per-node dispatch
// just sets the harness model and api URL — the catalog is a hint,
// not an allowlist.
func (s *replState) modelCatalogForREPL() string {
	if s.modelCatalogCache != "" {
		return s.modelCatalogCache
	}
	models := s.registry.Filter(context.Background(), func(m llm.ModelInfo) bool {
		// Drop embeddings/rerankers — they fail with "does not support
		// chat" when routed to a chat call. Capability tags are sourced
		// from endpoint labels (Lemonade) or id-pattern inference
		// (Ollama), so this single filter replaces the old per-list
		// embed-marker heuristic.
		if m.HasCapability(llm.CapEmbedding) || m.HasCapability(llm.CapReranking) {
			return false
		}
		return true
	})
	// Group by (Local, Endpoint) for display: local backends first so
	// the LLM sees "what's on this machine" before scrolling to cloud
	// options. Within a group, sort by id for stability.
	sort.SliceStable(models, func(i, j int) bool {
		if models[i].IsLocal != models[j].IsLocal {
			return models[i].IsLocal // locals first
		}
		if models[i].Endpoint != models[j].Endpoint {
			return models[i].Endpoint < models[j].Endpoint
		}
		return models[i].ID < models[j].ID
	})
	var b strings.Builder
	currentGroup := ""
	for _, m := range models {
		group := m.Endpoint
		if !m.IsLocal {
			group += " (cloud)"
		}
		if group != currentGroup {
			if currentGroup != "" {
				b.WriteString("\n")
			}
			fmt.Fprintf(&b, "%s:\n", group)
			currentGroup = group
		}
		// Surface context window when known so the LLM can pick a
		// model with enough room for the task.
		if m.EffectiveContextWindow > 0 {
			fmt.Fprintf(&b, "  %s (ctx=%d)\n", m.ID, m.EffectiveContextWindow)
		} else {
			fmt.Fprintf(&b, "  %s\n", m.ID)
		}
	}
	if b.Len() == 0 {
		b.WriteString("(no models discoverable — Ollama down and no compat endpoints configured)\n")
	}
	s.modelCatalogCache = b.String()
	return s.modelCatalogCache
}

// resetModelCatalog invalidates the cached catalog. Called when the
// REPL swaps models (so the LLM sees the current default reflected
// in the catalog) or when /models refresh is invoked.
func (s *replState) resetModelCatalog() {
	s.modelCatalogCache = ""
}

// buildProviderFactoryForREPL constructs an llm.ProviderFactory the
// REPL chain hands to decide.next. The factory resolves per-call
// model IDs the LLM emits via attrs.model — bare names route to
// Ollama, slash-prefixed to OpenRouter (matching the rest of the
// REPL's routing convention).
//
// The session default (used when the LLM doesn't specify) is the
// REPL's currently-pinned model + endpoint, so a /model swap shifts
// the default for subsequent turns. Per-call IDs the LLM picks
// override on a node-by-node basis.
//
// An empty/missing OpenRouter key keeps Ollama routing working —
// slash-prefixed IDs will error, but bare-name lookups succeed.
func buildProviderFactoryForREPL(cfg *config.Config, model, apiURL string) llm.ProviderFactory {
	key, _, _ := secret.MustOpenRouterKey() // empty on failure is fine
	ollamaURL := ""
	if apiURL == defaultOllamaAPIURL {
		ollamaURL = apiURL
	} else {
		// Default-Ollama endpoint even when the session is currently
		// routed to OpenRouter — so the LLM can still emit a bare
		// local model id and have it resolve. Removes a footgun where
		// the factory silently can't route to Ollama just because the
		// session happens to be on cloud.
		ollamaURL = defaultOllamaAPIURL
	}
	return llm.NewProviderFactory(llm.FactoryConfig{
		Cfg:           cfg,
		OpenRouterKey: key,
		OllamaAPIURL:  ollamaURL,
		DefaultModel:  model,
		DefaultAPIURL: apiURL,
	})
}

// buildLLMProviderForREPL constructs an llm.Provider matching the
// model + apiURL the REPL is currently routed to.
//
// Resolution order:
//
//  1. cfg.ResolveModelRoute — if the user has configured an endpoint
//     (Phase 4 model registry) and the model id matches by prefix or
//     role-map, construct an OpenAICompatClient bound to that endpoint.
//     This is what lets "chatterbox/Qwen3-Coder-30B-A3B-Instruct-GGUF"
//     route to a local Lemonade server instead of falling through to
//     the slash heuristic.
//  2. Ollama-shaped apiURL → llm.NewLLMClient(BackendOllama).
//  3. Everything else → OpenRouter with the keychain-resolved key.
//
// Returns nil when no provider can be configured (Cortex + decide.next
// both tolerate nil providers by degrading to mechanical / rule-based
// paths).
//
// Shared between newSessionCognition (the eager build) and the REPL
// chain's decide.next handler (which wants the same provider to
// classify the next action without re-doing the construction).
//
// Now delegates to internal/llm.BuildProvider — the unified resolver
// every cmd/cortex/commands/ caller shares. WithAPIURL preserves the
// REPL's legacy "non-default apiURL routes to OpenRouter" behavior so
// existing /model + /endpoint flows don't change shape. See
// docs/provider-resolution-refactor.md.
func buildLLMProviderForREPL(cfg *config.Config, model, apiURL string) llm.Provider {
	return intllm.BuildProvider(cfg, model, intllm.WithAPIURL(apiURL))
}

// rebindCognitionForModel rebuilds s.store + s.cortex so the shared
// cognition surface uses a provider bound to the new model. Called by
// the /model slash command — without this the provider stays bound to
// the model captured at newREPLState and cortex_search keeps calling
// the original model for the whole session even after a swap.
//
// On success the old store is closed and the new pair is assigned in
// place. On failure the old pair is preserved (the session keeps
// working with the prior provider) and the error is returned.
func (s *replState) rebindCognitionForModel() error {
	cortexDir := filepath.Join(s.workdir, ".cortex")
	newStore, newCortex, err := newSessionCognition(cortexDir, s.model, s.apiURL)
	if err != nil {
		return err
	}
	oldStore := s.store
	s.store = newStore
	s.cortex = newCortex
	if oldStore != nil {
		_ = oldStore.Close()
	}
	return nil
}

// ensureCaptureClient lazily builds the workdir-rooted Capture once,
// shared across all turns of a REPL session. We intentionally do NOT
// use the global ~/.cortex/ store — captures from a REPL session live
// next to the project that produced them, so they ride along with the
// codebase (and the user can ingest them later or have the daemon do
// it). Returns nil + error only on filesystem failures; ordinary use
// always succeeds.
func (s *replState) ensureCaptureClient() (*capture.Capture, error) {
	if s.captureClient != nil {
		return s.captureClient, nil
	}
	s.captureCfg = &config.Config{
		ContextDir:  filepath.Join(s.workdir, ".cortex"),
		ProjectRoot: s.workdir,
	}
	// Attach the shared Storage when available so captures populate
	// the same in-memory indexes that cortex_search reads. nil store
	// is the fallback path where captures still land in the journal
	// for later replay but aren't searchable in-session.
	s.captureClient = capture.NewWithStorage(s.captureCfg, s.store)
	return s.captureClient, nil
}

// studyBoundariesLine returns a single dash-prefixed line for
// SystemBoundaries describing the project's study coverage. With
// auto-accumulation, "complete" no longer means "done forever" — the
// number reported is the last persisted snapshot. If the controller
// is mid-run, it'll refresh on next save.
//
// Returns "" when no state has been written yet. Best-effort: a stale
// or missing file degrades to "no boundary info" silently.
func studyBoundariesLine(workdir string) string {
	cortexDir := filepath.Join(workdir, ".cortex")
	sum := study.LoadDeficitSummary(cortexDir)
	if !sum.HasState {
		return ""
	}
	status := "in progress"
	if sum.LastCompletedAt != nil {
		status = "at target"
	}
	return fmt.Sprintf(
		"- Project study: %s (%d insights, %.0f%% effective-LOC, %.0f%% file-coverage; query via remember.vector_search).\n",
		status, sum.InsightsEmitted, 100*sum.LastCoveredEff, 100*sum.LastCoveredFile,
	)
}

// maybeStartStudy is the REPL's per-session study hook. With
// auto-accumulation, this fires on every REPL startup (unless
// CORTEX_SKIP_STUDY=1): the controller does a cheap boundary scan,
// detects drift since the last study, and either extracts insights for
// the changed/new files or halts immediately with reason="no_drift".
//
// The REPL keeps accepting input immediately; the controller surfaces
// progress (or "no drift, halting") as banners between user prompts.
// Cancel-on-exit is wired via state.studyCancel, which state.close()
// invokes during defer.
//
// CORTEX_SKIP_BOOTSTRAP is accepted as a deprecated alias for the same
// disable signal so users who set the old name don't suddenly start
// auto-studying after upgrading.
func maybeStartStudy(state *replState) {
	if os.Getenv("CORTEX_SKIP_STUDY") == "1" || os.Getenv("CORTEX_SKIP_BOOTSTRAP") == "1" {
		return
	}
	cortexDir := filepath.Join(state.workdir, ".cortex")
	run, reason := study.ShouldRun(cortexDir)
	if !run {
		// Today this branch is unreachable — ShouldRun always returns
		// run=true in the drift-aware model. Kept defensively so a
		// future "explicit-disable" reason can short-circuit here.
		return
	}
	state.ui.Info(fmt.Sprintf("cortex: study checking project drift (%s) — progress will surface as banners (CORTEX_SKIP_STUDY=1 to disable)", reason))

	ctx, cancel := context.WithCancel(context.Background())
	state.studyCancel = cancel

	// Build the provider the same way newSessionCognition did, so
	// study inherits the REPL's currently-pinned model/endpoint.
	cfg := &config.Config{ContextDir: cortexDir, ProjectRoot: state.workdir}
	provider := buildLLMProviderForREPL(cfg, state.model, state.apiURL)

	cc := study.ControllerConfig{
		Config: study.Config{
			ProjectRoot:    state.workdir,
			ContextDir:     cortexDir,
			Provider:       provider,
			TargetCoverage: 0.80,
			BudgetMax:      200,
			BatchSize:      4,
			WindowLines:    study.DefaultWindowLines,
			WindowOverlap:  study.DefaultWindowOverlap,
			ExtractOp:      study.ExtractOpAuto,
			Banner: func(line string) {
				// Structured event so the TUI can pin progress to the
				// ambient row (between divider and status) and drop it
				// when study finishes. StdoutSink's renderer prints
				// the legacy "[study] foo" inline form. We can't
				// detect "done" from inside the controller's banner
				// callback today; state.close()'s studyCancel runs on
				// session exit, which is a fine clear-trigger but we'd
				// want the controller to emit one final done=true line
				// at coverage-met. Follow-up — for now the row clears
				// on session end via the cancel hook.
				state.ui.Event("study.progress", map[string]any{
					"line": line,
					"done": false,
				})
			},
		},
		ExtractInsightFn:  wrapInsightFn(provider),
		ExtractOverviewFn: wrapOverviewFn(provider),
	}
	go study.RunInBackground(ctx, cc)
}

// journalIngestInterval is the ticker cadence for the REPL's
// journal-ingest goroutine. The retired daemon used a 5s tick at the
// processor level; the REPL pays for fewer wake-ups because capture
// latency is already absorbed at write time and search can fall back
// to the journal directly if the index lags. var (not const) so the
// 2.5 smoke test can shorten it.
var journalIngestInterval = 30 * time.Second

// maybeStartJournalIngest spawns a goroutine that drains the project's
// capture/observation/dream/etc. journals into Storage and fires the
// background cognitive modes (Think + Dream) on a ticker. Replaces
// the auto-started daemon's processor + cognitive loops
// (daemon-retirement Phase 2.1, 2.2, 2.3).
//
// MaybeThink/MaybeDream each internally enforce their budget contracts
// (Think's budget decays with activity; Dream's budget grows with
// idle time and is capped at MaxBudget). Firing them every tick is
// the same shape the daemon's 10s cognitive ticker used — the inner
// budget gates whether anything actually happens.
//
// No-ops when state.store is nil — sessions that opted out of the
// shared cognition surface have nothing to ingest into.
func maybeStartJournalIngest(state *replState) {
	if state == nil || state.store == nil {
		return
	}
	cortexDir := filepath.Join(state.workdir, ".cortex")
	cfg := &config.Config{ContextDir: cortexDir, ProjectRoot: state.workdir}
	proc := processor.New(cfg, state.store)

	ctx, cancel := context.WithCancel(context.Background())
	state.ingestCancel = cancel

	go func() {
		ticker := time.NewTicker(journalIngestInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if _, err := proc.RunBatch(); err != nil && state.verbose {
					state.ui.Warn(fmt.Sprintf("(journal ingest: %v)", err))
				}
				if state.cortex != nil && state.cortex.IsModeEnabled("think") {
					res, err := state.cortex.MaybeThink(ctx)
					switch {
					case err != nil && state.verbose:
						state.ui.Warn(fmt.Sprintf("(think: %v)", err))
					case res != nil && res.Status == cognition.ThinkRan:
						state.ui.Info(fmt.Sprintf("[think] processed %d operations (%dms)",
							res.Operations, res.Duration.Milliseconds()))
					}
				}
				if state.cortex != nil && state.cortex.IsModeEnabled("dream") {
					res, err := state.cortex.MaybeDream(ctx)
					switch {
					case err != nil && state.verbose:
						state.ui.Warn(fmt.Sprintf("(dream: %v)", err))
					case res != nil && res.Status == cognition.DreamRan:
						state.ui.Info(fmt.Sprintf("[dream] explored %d items, %d insights (%dms)",
							res.Operations, res.Insights, res.Duration.Milliseconds()))
					}
				}
			}
		}
	}()
}

// close flushes and closes the session JSONL plus any shared cognition
// state. The Storage close flushes its append-mode JSONL handles so
// the events.jsonl on disk matches the in-memory indexes at the
// moment of session end.
func (s *replState) close() {
	if s.studyCancel != nil {
		s.studyCancel()
		s.studyCancel = nil
	}
	if s.ingestCancel != nil {
		s.ingestCancel()
		s.ingestCancel = nil
	}
	if s.jsonl != nil {
		_ = s.jsonl.Close()
	}
	if s.store != nil {
		_ = s.store.Close()
	}
}

// applySalienceCalibration reads .cortex/calibration/salience.json
// (Phase 3 Slice 5) and pushes the snapshot's GlobalCap into
// llm.SetSalienceCapOverride so SalienceCapForClass returns the
// calibrated value across every class. Missing file or missing
// GlobalCap is silent — the static class defaults stay in force.
//
// Per-class differentiation isn't possible from the trace shape
// today (rows don't carry the running session's ContextClass), so a
// single class-agnostic cap is the most we can say with the data.
// When the trace schema grows a class column, this function gains a
// per-class breakdown.
func applySalienceCalibration(cortexDir string, verbose bool, ui cliout.Sink) {
	path := filepath.Join(cortexDir, "calibration", "salience.json")
	snap, err := dag.LoadSalienceCalibration(path)
	if err != nil {
		if verbose && ui != nil {
			ui.Warn(fmt.Sprintf("salience calibration load failed (%v)", err))
		}
		return
	}
	if snap == nil || snap.GlobalCap <= 0 {
		return
	}
	llm.SetSalienceCapOverride(map[llm.ContextClass]int{
		llm.ContextSmall:  snap.GlobalCap,
		llm.ContextMedium: snap.GlobalCap,
		llm.ContextLarge:  snap.GlobalCap,
	})
	if verbose && ui != nil {
		ui.Info(fmt.Sprintf("salience: calibrated cap=%d (samples=%d)", snap.GlobalCap, snap.Samples))
	}
}

// salienceCapForSession returns the per-tool-call output-token cap to
// use for this REPL turn, derived from the model id + the endpoint's
// effective context window. Two regimes:
//
//   - When ctxWindow > 0 (the registry knows the runtime window), the
//     cap is sized as a fraction of the window — windowFractionForCap
//     (currently 1/8). Reads/tool outputs smaller than the cap pass
//     through with no LLM compression; bigger outputs still compress
//     but to a cap that scales with the model so a 64K-window model
//     compresses to ~8K instead of the static 1500-token class default.
//     This is the size-aware-tool-results path.
//
//   - When ctxWindow == 0 (unknown — pre-registry behavior), falls back
//     to the static SalienceCapForClass bucket (200/500/1500 per
//     small/medium/large). The pre-registry call sites keep working
//     unchanged.
//
// Callers should plumb the registry-sourced EffectiveContextWindow
// through via replState.effectiveContextWindow() — a llama-server
// booted at --ctx-size 65536 reports 65536 here, not the 262144 the
// model's theoretical max would suggest.
func salienceCapForSession(model string, ctxWindow int) (int, llm.ContextClass) {
	class := llm.InferContextClass(model, ctxWindow)
	if ctxWindow > 0 {
		// Scale the cap with the runtime window: a generous fraction
		// big enough that reads which comfortably fit pass through
		// without LLM-mediated salience compression. 1/8th leaves room
		// for ~8 accumulator entries of this size plus the prompt.
		cap := ctxWindow / windowFractionForCap
		if cap < defaultToolOutputSalienceCap {
			cap = defaultToolOutputSalienceCap
		}
		return cap, class
	}
	cap := llm.SalienceCapForClass(class)
	if cap <= 0 {
		cap = defaultToolOutputSalienceCap
	}
	return cap, class
}

// windowFractionForCap is the divisor applied to the calling model's
// effective context window when deriving the per-tool-call salience
// cap. 1/8th = ~12.5% of the window per deposit, leaving room for
// ~8 such deposits in the accumulator plus the prompt. Empirically
// tuned for the small-model amplifier path: smaller fractions waste
// the model's window on too-aggressive compression; larger fractions
// risk one big read crowding out everything else in the accumulator.
const windowFractionForCap = 8

// effectiveContextWindow returns the calling model's runtime context
// window from the registry. Zero when the model isn't in the registry
// (e.g. the user pinned a model that hasn't been probed, or the
// registry is empty because every backend is down) — callers should
// treat zero as "unknown, fall back to id-pattern inference."
//
// Used by every site that needs the size-vs-window decision today
// (Salience cap inference) and by Phase B's deterministic-chunking
// path tomorrow.
func (s *replState) effectiveContextWindow() int {
	if s.registry == nil {
		return 0
	}
	info, ok := s.registry.Get(context.Background(), s.model)
	if !ok {
		return 0
	}
	return info.EffectiveContextWindow
}

// resolveAPIURL routes to Ollama when the model id looks local (no
// provider prefix), to OpenRouter otherwise. We treat a slash as the
// "this is provider/model" signal — matches the convention `cortex code`
// uses (anthropic/foo, qwen/foo, openai/foo).
//
// TODO (two-backend world is too narrow): "ollama or openrouter" is
// the entire universe today. Real users with their own inference
// servers — vLLM, llama.cpp, LM Studio, sglang — have no path,
// neither do direct Anthropic / OpenAI keys. For the small-model
// amplifier story local inference variety is the point. Generalize
// to a backend registry (model id pattern → endpoint + auth) with
// the current ollama/openrouter pair as two preconfigured entries.
func resolveAPIURL(model string) string {
	if !strings.Contains(model, "/") {
		return defaultOllamaAPIURL
	}
	return "" // empty → harness uses OpenRouter default
}

// TODO (layer, don't replace): the override file is full-replacement
// today — if a user writes .local.md they lose the iter-7 calibrated
// guardrails baked into defaultREPLSystemPrompt ("don't hallucinate
// the codebase", "no absolute paths"). Combined with the AGENTS.md
// TODO at newREPLState: the right shape is `default + (project
// addendum from AGENTS.md or .local.md) + (per-model variant if
// any)`. Keep the calibrated rules always; let projects ADD
// conventions without losing them.
//
// loadOrSeedSystemPrompt resolves the worker-model system prompt for
// this REPL session. Binary-first: the const is the source of truth,
// .cortex/repl-system-prompt.local.md is an opt-in override the user
// can write to customize per-workdir.
//
// Earlier behavior wrote a seed file (.cortex/repl-system-prompt.md)
// on first run and then read it back every session. That made every
// binary update silently stale until the user `rm`'d the file. Now:
//
//  1. Always start with defaultREPLSystemPrompt (in-binary const).
//  2. If <workdir>/.cortex/repl-system-prompt.local.md exists, return
//     its content instead — this is the explicit user override.
//  3. Never write a seed file. Legacy .cortex/repl-system-prompt.md
//     files are silently ignored.
//
// `path` is kept as a parameter for call-site clarity but is now
// interpreted as the *override* file path (the .local.md variant).
func loadOrSeedSystemPrompt(path string) (string, error) {
	if b, err := os.ReadFile(path); err == nil {
		return string(b), nil
	}
	return defaultREPLSystemPrompt, nil
}

// TODO (rules are policy, not enforcement): the "Don't claim you read
// a file you didn't read" and "Never write under .git or .cortex"
// rules are model-side asks with nothing enforcing them. A reflect.*
// node at finalize that cross-checks final_text claims ("I read X",
// "After reviewing Y") against the turn's act.read_file trace would
// turn policy into a signal — fans into the learning loop as either
// a feedback.correction event or a captured "model hallucinated again"
// metric. This is exactly the salience-layer-for-small-models pattern
// applied to prompt rules.
//
// defaultREPLSystemPrompt is the worker-model system prompt for the
// agent loop inside decide.coding_turn. Assumed floor: 7B+ model with
// reliable function-calling. The user override lives at
// <workdir>/.cortex/repl-system-prompt.local.md.
//
// Tuning lessons baked in (history; floor has moved up):
//   - Iter-3: long meta-instructions about tool-call format HURT small
//     models. Keep it short.
//   - Iter-4/5: at the 1.5B floor, register only 3 tools and match the
//     prompt to the registered surface exactly. No longer the floor.
//   - Iter-6: "You are a Go programmer" + "use go build to verify"
//     was over-pinned. Generalize.
//   - Iter-7 (this version): the "questions → no tools" rule was the
//     leanjs failure root — model never read README, hallucinated Go
//     commands from training priors. New framing: when the user asks
//     about THIS workdir, ground yourself in the actual files first.
//     The agent loop's tool-call discipline does the rest.
const defaultREPLSystemPrompt = `You are a capable assistant working in a workdir you fully own. Code, conversation, and analysis are all in scope.

CRITICAL: when the user asks about THIS workdir or its code, you MUST call tools to read files before answering. Do not describe the codebase from priors — you have not seen it. Never write "I have read X" or "After reviewing X" unless you actually called read_file(X) in this turn. If you find yourself about to describe a project shape without having called any tools, STOP and call list_dir(".") and read_file("README.md") first.

When to use tools:
  - User asks about the workdir / its code → call list_dir + read_file FIRST, then answer from what you actually read. Do not infer from the workdir name.
  - User asks for a code change → read what you need, write the change, then verify with the appropriate build/test command.
  - User asks a general question with no workdir grounding needed → answer in prose. No tool calls.

Discipline:
  - Make ONE focused change per user message. Edit one file unless the request explicitly spans files.
  - Read narrowly. Before reading a file you don't already know is small, check its size with run_shell ("wc -l <file>"). If it's large, use run_shell with grep to locate the relevant span, then read only that range. Whole-file reads of large files burn budget and slow every downstream step.
  - Don't narrate what you're about to do; just do it.
  - When making changes, run the project's build/test command (via run_shell) before declaring done. Read errors, fix, retry.
  - When the step is done, respond with a short summary and NO further tool calls.

Rules:
  - Paths are relative to the workdir; no absolute paths, no "..".
  - Never write under .git or .cortex.
  - Don't claim you read a file you didn't read.
`

// emitOneShotJSON prints a single-line JSON envelope summary of the
// headless one-shot run to stdout. Data is intentionally minimal — the
// full turn row already lands in <sessionDir>/session.jsonl for callers
// that want the long-form transcript; this is just the at-a-glance
// "did it pass, what did it cost" view.
func emitOneShotJSON(ctx *Context, s *replState, turnErr error) {
	data := map[string]any{
		"session_id":   s.sessionID,
		"session_path": s.sessionPath,
		"workdir":      s.workdir,
		"model":        s.model,
		"accepted":     s.turns > 0, // finalize() only bumps turns on accept
	}
	emitter := EmitterFor(ctx, s.workdir)
	if turnErr != nil {
		// Failure surfaces both as ok=false + structured error code AND in
		// the data payload's legacy "error" field so the existing benchmark
		// parser keeps working.
		data["error"] = turnErr.Error()
		_ = emitter.Fail(os.Stdout, cliout.ErrCodeInternal, turnErr.Error(), data)
		return
	}
	_ = emitter.Ok(os.Stdout, data)
}

// revalidateAndWarn runs the Phase 4 Slice E role-map revalidation
// against <workdir>/.cortex/config.json and prints a one-line warning
// per stale assignment. Stale doesn't block the REPL — the routing
// layer will surface the actual error if a user invokes a broken
// role — but surfacing it up front lets the user fix it before
// hitting it mid-session.
func revalidateAndWarn(workdir string, ui cliout.Sink) {
	cfg := loadREPLConfig(filepath.Join(workdir, ".cortex"))
	if cfg == nil || cfg.Models == nil {
		return
	}
	stale := intllm.RevalidateRoleMap(cfg)
	for _, s := range stale {
		ui.Warn(fmt.Sprintf("role %s pinned to %s/%s — %s", s.Role, s.Endpoint, s.Model, s.Reason))
	}
}

// printREPLBanner prints the welcome line. One line, no ASCII art.
// The backend label honors endpoint resolution: a model id matching a
// configured endpoint shows that endpoint's name (e.g. "chatterbox"),
// otherwise falls back to apiURL or "openrouter (default)".
func printREPLBanner(s *replState) {
	api := s.apiURL
	if cfg := loadREPLConfig(filepath.Join(s.workdir, ".cortex")); cfg != nil {
		if ep, _, ok := cfg.ResolveModelRoute(s.model); ok {
			api = fmt.Sprintf("%s (%s)", ep.Name, ep.BaseURL)
		}
	}
	if api == "" {
		api = "openrouter (default)"
	}
	s.ui.Banner(fmt.Sprintf("cortex · %s · %s · %s · /help", s.workdir, s.model, api))
}

// printREPLHelp dumps the bare `cortex --help` text. Mirrors slash /help.
func printREPLHelp() {
	fmt.Println(`Usage: cortex [flags]

Bare cortex with no subcommand enters an interactive coding REPL in the
current directory. Default model is qwen2.5-coder:1.5b via Ollama.

Flags:
  -m, --model NAME     Model id. Slash in name = OpenRouter (e.g.
                       anthropic/claude-haiku-4.5); no slash = Ollama
                       (e.g. qwen2.5-coder:1.5b, llama3.2:3b).
                       Default: qwen2.5-coder:1.5b. Override via
                       CORTEX_REPL_MODEL env var.
  -v, --verbose        Print agent-loop telemetry (tool calls + tokens).
      --workdir DIR    Use DIR instead of cwd as the workdir.
  -h, --help           Show this help.

Headless flags (skip stdin scanner, used by benchmark harnesses):
      --prompt TEXT    Run one turn with TEXT as the user message, then exit.
      --verifier CMD   Shell command used to verify each attempt instead of
                       the auto-detected 'go build'. Exit 0 = pass.
      --auto-retry     Skip the interactive [r/e/s/q] gate; treat unresolved
                       verify-fail as the final state for the turn.
      --max-retries N  Cap on auto-retry attempts (default 1).
      --json           Emit a one-line JSON summary on stdout instead of
                       the human-readable banner + session-saved tail.
      --system-prompt FILE  Read the system prompt from FILE instead of
                       the auto-seeded Go-flavored default. Useful for
                       benchmark harnesses that need a different
                       language / repo-shape guidance.
      --max-turns N    Per-attempt agent-loop cap (default 8).
      --max-cost-usd X Per-attempt USD budget (default 0.20).
      --max-cumulative-tokens N  Per-attempt token budget (default 300000).
      --full-tools     No-op alias kept for backward compat. The full
                       5-tool surface (read/write/list_dir/run_shell/
                       cortex_search) is now the default — see
                       --minimal-tools to opt back into the iter-4
                       3-tool registry.
      --minimal-tools  Drop list_dir + cortex_search from the registry,
                       leaving read_file + write_file + run_shell.
                       Opt-in for users still running tiny (<7B)
                       Ollama models that lose tool-call discipline
                       at 5 tools.
      --keep-on-fail   Don't roll back the workdir when the verifier
                       fails. Benchmark default — iterations build
                       on prior work instead of restarting from
                       scratch each attempt.
      --history-turns N  Cap on the conversation-history block sent to
                       the model on each turn. Default 6 (last 3
                       user/assistant pairs). 0 disables history.
                       Mid-session, /history N changes the cap.
      --prior-summaries N  Cap on cross-session think.session_summary
                       entries injected at REPL start. Default 3.
                       0 disables cross-session memory (the pre-
                       Item-4 behavior). Mid-session,
                       /prior-summaries N changes the cap.
In the REPL:
  /help                Show slash-command help.
  /diff                Show files changed since session start.
  /undo                Restore workdir to pre-last-turn snapshot.
  /model <id>          Swap model for subsequent turns.
  /quit or Ctrl-D      Exit; session saved to .cortex/sessions/<ts>/.

Examples:
  cortex
  cortex --model anthropic/claude-haiku-4.5
  CORTEX_REPL_MODEL=llama3.2:3b cortex`)
}

// dispatchSlash handles slash-prefixed input. Returns (continue, error):
// continue=false means the loop should exit (e.g. /quit).
func (s *replState) dispatchSlash(line string) (bool, error) {
	parts := strings.Fields(line)
	cmd := parts[0]
	rest := parts[1:]
	switch cmd {
	case "/help", "/?":
		s.printSlashHelp()
		return true, nil
	case "/quit", "/exit":
		return false, nil
	case "/model":
		if len(rest) == 0 {
			s.ui.Info(fmt.Sprintf("  current model: %s (api: %s)", s.model, displayAPI(s.apiURL)))
			return true, nil
		}
		prevModel, prevAPI := s.model, s.apiURL
		s.model = rest[0]
		s.apiURL = resolveAPIURL(s.model)
		if err := s.rebindCognitionForModel(); err != nil {
			s.model, s.apiURL = prevModel, prevAPI
			return true, fmt.Errorf("model swap failed (provider rebind): %w", err)
		}
		s.resetModelCatalog()
		s.ui.Info(fmt.Sprintf("  model → %s (api: %s)", s.model, displayAPI(s.apiURL)))
		return true, nil
	case "/models":
		refresh := len(rest) > 0 && rest[0] == "refresh"
		if refresh {
			s.resetModelCatalog()
		}
		s.printModels(refresh)
		return true, nil
	case "/shell-policy":
		s.printShellPolicy()
		return true, nil
	case "/history":
		if len(rest) == 0 {
			s.ui.Info(fmt.Sprintf("  history: cap=%d turns, buffered=%d turns", s.historyLimit, len(s.history)))
			return true, nil
		}
		var n int
		if _, err := fmt.Sscanf(rest[0], "%d", &n); err != nil || n < 0 {
			return true, fmt.Errorf("/history N: N must be a non-negative integer")
		}
		s.historyLimit = n
		s.ui.Info(fmt.Sprintf("  history cap → %d turns (buffered=%d)", s.historyLimit, len(s.history)))
		return true, nil
	case "/prior-summaries":
		if len(rest) == 0 {
			s.ui.Info(fmt.Sprintf("  prior-session summaries: cap=%d", s.priorSessionsCap))
			return true, nil
		}
		var n int
		if _, err := fmt.Sscanf(rest[0], "%d", &n); err != nil || n < 0 {
			return true, fmt.Errorf("/prior-summaries N: N must be a non-negative integer")
		}
		s.priorSessionsCap = n
		s.ui.Info(fmt.Sprintf("  prior-session summary cap → %d", s.priorSessionsCap))
		return true, nil
	case "/verbose":
		// Mid-session toggle of the verbose flag. With no argument
		// it flips; "on" / "off" pin the value explicitly. Surfaces
		// via the sink so both StdoutSink and TUISink update their
		// Event rendering gate on the next coding.turn /
		// coding.tool_result.
		if len(rest) == 0 {
			s.verbose = !s.verbose
		} else {
			switch strings.ToLower(rest[0]) {
			case "on", "true", "1", "yes":
				s.verbose = true
			case "off", "false", "0", "no":
				s.verbose = false
			default:
				return true, fmt.Errorf("/verbose [on|off]: argument must be on|off (got %q)", rest[0])
			}
		}
		s.ui.SetVerbose(s.verbose)
		state := "off"
		if s.verbose {
			state = "on"
		}
		s.ui.Info(fmt.Sprintf("  verbose → %s", state))
		return true, nil
	// TODO: remove /diff and /undo — see snapshotWorkdir TODO. Modern
	// coding harnesses (Claude Code, Cursor) punt to git/IDE for both;
	// keeping them here just to back a parallel snapshot system isn't
	// worth the maintenance.
	case "/diff":
		s.printDiff()
		return true, nil
	case "/undo":
		if err := s.undoLastTurn(); err != nil {
			return true, err
		}
		return true, nil
	default:
		return true, fmt.Errorf("unknown slash command %q — try /help", cmd)
	}
}

func (s *replState) printSlashHelp() {
	s.ui.Info(`  /help              this message
  /diff              changed files since session start
  /undo              restore workdir to pre-last-turn snapshot
  /model [<id>]      show or swap model (slash in name = OpenRouter, no slash = Ollama)
  /models [refresh]  list installed Ollama models + OpenRouter catalogue (cached per session)
  /shell-policy      show the user-configured shell allow/deny policy (if any)
  /history [N]       show buffered turn count, or set the conversation-history cap to N
  /prior-summaries [N]  show or set the cap on cross-session summary injection
  /verbose [on|off]  toggle (or set) verbose event rendering mid-session
  /quit              exit (also Ctrl-D)`)
}

func displayAPI(api string) string {
	if api == "" {
		return "openrouter"
	}
	return api
}

// runTurn is the single-prompt path. The flow is:
//
//	snapshot pre-turn state
//	attempt 1: harness(userPrompt)             → verify
//	if fail   → attempt 2: harness(+errorCtx)  → verify
//	if fail   → user gate [r/e/s/q]
//	          r: ask hint, harness(+errorCtx+hint) → verify → re-gate
//	          e: pause for manual workdir edits → verify → re-gate
//	          s: rollback, return
//	          q: rollback, signal exit
//	on accept → push snapshot, write jsonl, fire background capture
//
// The structured row in session.jsonl carries enough to reconstruct
// what happened, including the auto-retry round and any user-driven
// retry hints.
//
// TODO: move verification from this hardcoded outer-loop gate into the
// DAG as an emergent micro-node (e.g. verify.* op family spawned when
// the coding turn produced edits). The small LLM at that node decides
// what to run from project context (test file present? Makefile? CI
// config?) and its output feeds back via a normal DAG edge — same
// shape as any other decide.* node. Benefits: verification quality
// becomes a measurable DAG node captured in cell_results.jsonl,
// evaluable + swappable, and language-agnostic without hardcoded
// detection. Gate on an emergence eval (docs/emergence-evals.md) for
// "post-edit → verify-node-spawned" recall before deleting --verifier;
// keep --verifier as a benchmark-only forcing function while the
// emergent path matures.
func (s *replState) runTurn(userPrompt string) error {
	turnNum := s.turns + 1

	// Slice 4: auto-emit feedback if the user's prompt opens with a
	// correction/confirmation cue (e.g. "no, do X instead" / "perfect,
	// thanks"). The graded target is the immediately-prior turn —
	// skipped silently on turn 1 since there's nothing to grade.
	// Best-effort: any journal write failure is logged + ignored.
	if cue := detectFeedbackCue(userPrompt); cue != "" {
		if ferr := s.emitFeedbackEntry(cue, userPrompt); ferr != nil {
			s.ui.Info(fmt.Sprintf("  · feedback emit failed (%s): %v", cue, ferr))
		} else if s.verbose {
			s.ui.Info(fmt.Sprintf("  · feedback emitted: %s → prior turn", cue))
		}
	}

	// Slice 4: clarify follow-up stitching. When the prior turn routed
	// to decide.clarify and stashed its original ambiguous prompt, we
	// combine it with the user's answer so the classifier sees the
	// disambiguated request rather than a bare fragment. No-op when
	// the prior turn wasn't a clarify (lastClarifyPrompt empty).
	turnPrompt := s.stitchClarifyFollowUp(userPrompt)

	snapDir, err := s.snapshotWorkdir(turnNum)
	if err != nil {
		return fmt.Errorf("snapshot: %w", err)
	}

	row := turnRow{
		Turn:         turnNum,
		SessionID:    s.sessionID,
		Timestamp:    time.Now().UTC().Format(time.RFC3339),
		UserMessage:  userPrompt, // original, not the stitched version — preserves what the user actually typed
		Model:        s.model,
		APIURL:       s.apiURL,
		SystemPrompt: s.systemPrompt,
		SnapshotDir:  snapDir,
	}

	// Attempt 1: fresh model call. The intent classification from this
	// first call defines the turn's intent for journaling purposes;
	// retries reuse it (the user's ask hasn't changed, only the
	// verifier feedback has).
	hres, lres, intent, intentConf, runErr := s.runHarness(turnPrompt, "")
	row.HarnessError = errString(runErr)
	row.AgentTurns = hres.AgentTurnsTotal
	row.TokensIn = hres.TokensIn
	row.TokensOut = hres.TokensOut
	row.InjectedContextTokens = lres.InjectedContextTokens
	row.CostUSD = hres.CostUSD
	row.LatencyMs = hres.LatencyMs
	row.FilesChanged = hres.FilesChanged
	row.FinalText = lres.Final
	row.Intent = intent
	row.IntentConfidence = intentConf

	verifyRes := s.runVerifier()
	row.VerifyKind = verifyRes.Kind
	row.VerifyOK = verifyRes.OK
	row.VerifyOutput = verifyRes.OutputTail

	// Auto-retry loop. Default is one extra attempt (the historical
	// "one auto-retry" behavior) so interactive REPL UX is unchanged.
	// --max-retries N raises the cap; verifier output from the most
	// recent fail is fed back into each subsequent attempt as the
	// retry context. The first auto-retry's telemetry stays on the
	// dedicated Row.Retry* fields for back-compat with existing JSONL
	// consumers; later attempts merge their files-changed into the
	// row but are not individually broken out (they would land as
	// extra session.jsonl fields in a follow-up schema bump).
	//
	// TODO (retry is monotone): each retry re-runs the SAME model with
	// the SAME prompt + SAME tool surface, just with verifier output
	// appended. For small-model amplification the escalation should
	// have diversity: bump temperature, route attempt 2 through a
	// stronger decide.next model, decompose into a plan.* node, or
	// flip --minimal-tools off/on. A retry-policy node ("given attempt
	// N failed with signal S, pick the diversification move") fits the
	// micro-decision DAG model and is the place small-model amplifier
	// behavior actually shows up. Today "try again with the error" is
	// the only move; that's where the harness leaves capability on the
	// table.
	retryBudget := s.maxRetries
	if retryBudget < 1 {
		retryBudget = 1
	}
	autoAttempt := 0
	for autoAttempt < retryBudget && !verifyRes.OK && verifyRes.Kind != verifierNone {
		autoAttempt++
		if autoAttempt == 1 {
			s.ui.Info(fmt.Sprintf("  verify failed (%s), auto-retrying with error context (1/%d)...", verifyRes.Kind, retryBudget))
		} else {
			s.ui.Info(fmt.Sprintf("  verify still failing, auto-retry %d/%d...", autoAttempt, retryBudget))
		}
		hres2, lres2, _, _, runErr2 := s.runHarness(turnPrompt, verifyRes.OutputTail)
		if autoAttempt == 1 {
			row.RetryAgentTurns = hres2.AgentTurnsTotal
			row.RetryTokensIn = hres2.TokensIn
			row.RetryTokensOut = hres2.TokensOut
			row.RetryHarnessError = errString(runErr2)
			row.RetryFinalText = lres2.Final
		}
		row.FilesChanged = mergeFiles(row.FilesChanged, hres2.FilesChanged)

		verifyRes = s.runVerifier()
		if autoAttempt == 1 {
			row.RetryVerifyOK = verifyRes.OK
			row.RetryVerifyOutput = verifyRes.OutputTail
		}
	}

	// User-driven retry/edit loop. Loops until verify passes, user
	// skips, or user quits. Bounded by userGateMaxAttempts so a stuck
	// model can't trap the REPL. SKIPPED in headless mode (--auto-retry)
	// — the auto-retry budget is the only escalation a benchmark
	// harness ever wants.
	userHints := []string{}
	attempts := 0
	for !s.headless && !verifyRes.OK && verifyRes.Kind != verifierNone {
		attempts++
		if attempts > userGateMaxAttempts {
			s.ui.Info(fmt.Sprintf("  reached %d user-retry attempts; rolling back", userGateMaxAttempts))
			break
		}
		decision := s.promptGate(verifyRes)
		switch decision.kind {
		case gateAccept:
			// Possible only via "s" with verifier KindNone, which
			// we've already filtered above; here for completeness.
			verifyRes.OK = true
		case gateSkip:
			row.UserGate = "skip"
			s.fillRowGateLoop(&row, userHints)
			return s.finalize(row, snapDir, false)
		case gateQuit:
			row.UserGate = "quit"
			s.exitRequested = true
			s.fillRowGateLoop(&row, userHints)
			return s.finalize(row, snapDir, false)
		case gateRetry:
			row.UserGate = "retry"
			userHints = append(userHints, decision.hint)
			combined := verifyRes.OutputTail + "\n\nUSER HINT: " + decision.hint
			hres3, lres3, _, _, runErr3 := s.runHarness(turnPrompt, combined)
			row.UserRetryAttempts++
			row.UserRetryHarnessError = errString(runErr3)
			row.UserRetryFinalText = lres3.Final
			row.FilesChanged = mergeFiles(row.FilesChanged, hres3.FilesChanged)
			verifyRes = s.runVerifier()
			row.UserRetryVerifyOK = verifyRes.OK
			row.UserRetryVerifyOutput = verifyRes.OutputTail
		case gateEdit:
			row.UserGate = "edit"
			row.UserRetryAttempts++
			_, _ = s.ui.ReadLine("  edit any files in the workdir, then press enter to re-verify\n")
			verifyRes = s.runVerifier()
			row.UserRetryVerifyOK = verifyRes.OK
			row.UserRetryVerifyOutput = verifyRes.OutputTail
		}
	}

	s.fillRowGateLoop(&row, userHints)
	accepted := verifyRes.OK || verifyRes.Kind == verifierNone
	return s.finalize(row, snapDir, accepted)
}

// userGateMaxAttempts caps the user-driven retry loop. Set high enough
// to be useful (give the model real coaching room) but low enough that
// a stuck loop doesn't trap the REPL with no recourse.
const userGateMaxAttempts = 5

// fillRowGateLoop sets the joined user-hints field on the row so the
// session JSONL captures the full sequence of nudges the user gave.
func (s *replState) fillRowGateLoop(row *turnRow, hints []string) {
	if len(hints) == 0 {
		return
	}
	row.UserRetryHints = strings.Join(hints, " | ")
}

// finalize completes a turn: writes the JSONL row, pushes the snapshot
// to the undo stack and fires background capture on accept, or rolls
// back from snapshot on reject (unless --keep-on-fail suppresses
// the rollback for benchmark callers that want iteration to build on
// prior attempts instead of resetting to scratch). Always writes the
// row.
//
// TODO (learning loop is open): captureTurn records user prompts as
// events, but the OUTCOME signals on `row` — VerifyOK, UserGate (skip/
// retry/quit), UserRetryHints, RetryVerifyOK, the diff between attempt
// 1 and attempt 2's FilesChanged — never become durable wisdom anyone
// retrieves. session.jsonl is structured but read by no one. To make
// this an actual learning harness, fan outcome rows into the journal
// (capture/observation/feedback class as appropriate) so cortex_search
// surfaces "last time you tried X the verifier said Y; the fix was Z"
// on the next session. Today the inbound wire (shared Storage →
// cortex_search) exists; the outbound wire doesn't.
func (s *replState) finalize(row turnRow, snapDir string, accepted bool) error {
	row.Accepted = accepted
	if accepted {
		s.turns = row.Turn
		s.snapshotStack = append(s.snapshotStack, snapDir)
		// Append to the conversation buffer so subsequent turns see this
		// exchange via PriorMessages. Only the user prompt + assistant
		// final text — no tool-call traces.
		s.history = append(s.history, turnExchange{
			User:      row.UserMessage,
			Assistant: row.FinalText,
		})
		// Phase 3 Slice 4: emit a compressed per-turn summary so the
		// next turn can pull the rolling-summary view from the journal
		// instead of replaying full transcripts. Best-effort; failures
		// don't block the turn (logged in verbose mode).
		if err := s.emitSessionSummary(row); err != nil && s.verbose {
			s.ui.Warn(fmt.Sprintf("(session summary failed: %v)", err))
		}
		// Slice 5: when the per-turn summary count crosses the
		// digest threshold (15 summaries since the last digest), fold
		// the head of the summary history into a single dream.session_digest
		// entry. Synchronous because the REPL doesn't have a background
		// scheduler — pays ~6s of latency at threshold crossings only,
		// then 0 until the next threshold. Best-effort.
		if err := s.maybeWriteSessionDigest(); err != nil && s.verbose {
			s.ui.Warn(fmt.Sprintf("(session digest failed: %v)", err))
		}
		s.printTurnSummary(row)
		if err := s.captureTurn(row); err != nil && s.verbose {
			s.ui.Warn(fmt.Sprintf("(capture failed: %v)", err))
		} else if s.verbose {
			s.ui.Info("  (captured)")
		}
	} else if s.keepOnFail {
		// Benchmark mode: preserve the agent's attempt so the next
		// retry's verifier sees what it actually did, and so an
		// out-of-budget mid-attempt termination doesn't lose all
		// the work the agent did get done. Snapshot still lives on
		// disk; `/undo` could surface it if needed.
		if s.verbose {
			s.ui.Info("  (--keep-on-fail: rollback suppressed; preserving agent edits)")
		}
	} else {
		if err := s.restoreFromSnapshot(snapDir); err != nil {
			s.ui.Warn(fmt.Sprintf("rollback failed: %v", err))
		}
	}
	if err := s.writeJSONL(row); err != nil {
		s.ui.Warn(fmt.Sprintf("session log write failed: %v", err))
	}
	return nil
}

// emitSessionSummary writes a compressed per-turn summary to the
// journal's think class so subsequent turns can pull the rolling-
// summary view instead of replaying full prior transcripts
// (docs/salience-budgets.md "Cross-turn context (per-session)").
//
// Path:
//
//  1. Compose a draft string from the turn's signals — user prompt,
//     final response, files changed, verifier result.
//  2. Run it through attend.compress under intent="session-summary"
//     with the same capability-aware salience cap the dispatcher
//     used for this turn. Stronger model → more verbose summary;
//     weaker model → tighter one.
//  3. Write a ThinkSessionSummaryPayload entry to
//     <workdir>/.cortex/journal/think/.
//
// Best-effort: any failure is returned but the caller logs+continues.
// Compressor unavailable (no provider) → passthrough on the draft.
func (s *replState) emitSessionSummary(row turnRow) error {
	if s.workdir == "" {
		return nil
	}
	draft := composeSessionSummaryDraft(row)
	if strings.TrimSpace(draft) == "" {
		return nil
	}

	cap, _ := salienceCapForSession(s.model, s.effectiveContextWindow())
	intent := "session-summary: what the user asked, what was done, files changed, key takeaways. Preserve facts, drop pleasantries."

	cfg := loadREPLConfig(filepath.Join(s.workdir, ".cortex"))
	provider := buildLLMProviderForREPL(cfg, s.model, s.apiURL)

	// Tight timeout: summary writes are best-effort; we don't want a
	// hung LLM call to block the REPL prompt from returning. 15s is
	// generous for a small-model salience-extraction pass; misses
	// fall through to passthrough/truncate-stub.
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	res, err := ops.CompressSpec(ops.CompressConfig{Provider: provider}).Handler(ctx,
		map[string]any{"raw": draft, "max_tokens": cap, "intent": intent},
		dag.Budget{LatencyMS: 15000, Tokens: 2000, Depth: 1})
	if err != nil {
		return fmt.Errorf("attend.compress: %w", err)
	}

	compressed, _ := res.Out["compressed"].(string)
	if strings.TrimSpace(compressed) == "" {
		compressed = draft
	}
	origTok, _ := res.Out["original_tokens"].(int)
	keptTok, _ := res.Out["kept_tokens"].(int)
	fallback, _ := res.Out["fallback"].(bool)
	compressOp := "attend.compress"
	if fallback {
		compressOp = "fallback"
	}

	payload := journal.ThinkSessionSummaryPayload{
		SessionID:        s.sessionID,
		Turn:             row.Turn,
		UserPrompt:       row.UserMessage,
		Summary:          compressed,
		FilesChanged:     row.FilesChanged,
		VerifyKind:       row.VerifyKind,
		VerifyOK:         row.VerifyOK,
		OrigTokens:       origTok,
		KeptTokens:       keptTok,
		CompressOp:       compressOp,
		Intent:           row.Intent,
		IntentConfidence: row.IntentConfidence,
	}
	entry, err := journal.NewThinkSessionSummaryEntry(payload)
	if err != nil {
		return fmt.Errorf("build entry: %w", err)
	}
	classDir := filepath.Join(s.workdir, ".cortex", "journal", "think")
	w, err := journal.NewWriter(journal.WriterOpts{
		ClassDir: classDir,
		Fsync:    journal.FsyncPerBatch,
	})
	if err != nil {
		return fmt.Errorf("open writer: %w", err)
	}
	defer w.Close()
	if _, err := w.Append(entry); err != nil {
		return fmt.Errorf("append: %w", err)
	}
	return nil
}

// composeSessionSummaryDraft builds the raw text the salience
// compressor will work on. Pulls the highest-signal fields from a
// finalized turnRow into a compact prose block. The compressor is
// what makes this fit a budget; the draft just gathers everything
// worth considering.
func composeSessionSummaryDraft(row turnRow) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Turn %d.\n", row.Turn)
	// Intent line preserves the classifier's routing decision in the
	// prose the salience compressor sees; the compressed summary is
	// more useful to future turns when it carries this dimension.
	// Omitted entirely when intent wasn't captured (legacy / failure).
	if row.Intent != "" {
		fmt.Fprintf(&b, "Intent: %s (confidence %.2f)\n", row.Intent, row.IntentConfidence)
	}
	fmt.Fprintf(&b, "\nUser prompt:\n%s\n\n", row.UserMessage)
	if row.FinalText != "" {
		fmt.Fprintf(&b, "Assistant final response:\n%s\n\n", row.FinalText)
	}
	if len(row.FilesChanged) > 0 {
		fmt.Fprintf(&b, "Files changed: %s\n", strings.Join(row.FilesChanged, ", "))
	}
	if row.VerifyKind != "" && row.VerifyKind != "none" {
		fmt.Fprintf(&b, "Verifier (%s): ok=%v\n", row.VerifyKind, row.VerifyOK)
		if row.VerifyOutput != "" {
			fmt.Fprintf(&b, "Verifier output:\n%s\n", row.VerifyOutput)
		}
	}
	if row.RetryFinalText != "" {
		fmt.Fprintf(&b, "Retry attempt final:\n%s\n", row.RetryFinalText)
	}
	if row.UserGate != "" {
		fmt.Fprintf(&b, "User gate: %s\n", row.UserGate)
	}
	return b.String()
}

// priorMessagesForHarness assembles the conversation-history block for
// the next harness call.
//
// Phase 3 Slice 4: this is the cross-turn rolling-summary read path.
// Instead of replaying the raw (user, assistant) text of the last K
// turns — which scales linearly in transcript length and quadratically
// in cumulative tokens — we pull the most recent K
// think.session_summary entries from the journal and inject them as a
// single condensed system message. Each summary is the compressed
// artifact emitted at finalize() under the same capability-aware
// salience cap the per-turn flow uses. Cumulative prior-context size
// flattens; long sessions stay viable.
//
// Item 4 (cross-session memory): the SessionID filter is now off —
// prior-session summaries flow in on REPL start so the model resumes
// with context from earlier work. priorSessionsCap bounds the
// cross-session count separately from historyLimit (current session).
//
// Behavior:
//   - historyLimit ≤ 0 AND priorSessionsCap ≤ 0 → no prior messages.
//   - Journal read failure or zero matching summaries → fall back to
//     the verbatim s.history slice (pre-Slice-4 behavior).
func (s *replState) priorMessagesForHarness() []llm.ChatMessage {
	if s.historyLimit <= 0 && s.priorSessionsCap <= 0 {
		return nil
	}
	// Slice 5: when a dream.session_digest exists, prefer it for the
	// older history — readRecentSessionSummaries skips the summaries
	// the digest already folded in, and readLatestSessionDigest returns
	// the digest narrative to prepend as a single labeled block. The
	// effective prior-context block is then [digest] + [recent
	// post-digest summaries], not [all summaries up to cap].
	digest := s.readLatestSessionDigest()
	summaries := s.readRecentSessionSummaries(s.historyLimit, s.priorSessionsCap)
	if digest != nil || len(summaries) > 0 {
		return digestAndSummariesAsChatMessages(digest, summaries)
	}
	// Fallback: verbatim history (pre-Slice-4 behavior). Triggers when
	// the journal has no summary entries for this session yet (turn 1
	// after a fresh start before finalize has run) or a journal read
	// errored. Keeps the REPL functional regardless of journal state.
	if len(s.history) == 0 {
		return nil
	}
	start := len(s.history) - s.historyLimit
	if start < 0 {
		start = 0
	}
	tail := s.history[start:]
	out := make([]llm.ChatMessage, 0, len(tail)*2)
	for _, ex := range tail {
		if ex.User != "" {
			out = append(out, llm.ChatMessage{Role: "user", Content: ex.User})
		}
		if ex.Assistant != "" {
			out = append(out, llm.ChatMessage{Role: "assistant", Content: ex.Assistant})
		}
	}
	return out
}

// readRecentSessionSummaries scans the workdir's think journal for
// recent session-summary entries and returns them in chronological
// order (oldest first). currentCap bounds the count of entries from
// the current session; priorCap bounds the count of entries from
// every other session combined. Prior-session entries come first in
// the returned slice so the most-recent (current-session) summaries
// remain the last context the model sees.
//
// Returns an empty slice on any error or when no entries exist (the
// caller decides whether to fall back).
func (s *replState) readRecentSessionSummaries(currentCap, priorCap int) []journal.ThinkSessionSummaryPayload {
	if (currentCap <= 0 && priorCap <= 0) || s.workdir == "" || s.sessionID == "" {
		return nil
	}
	classDir := filepath.Join(s.workdir, ".cortex", "journal", "think")
	r, err := journal.NewReader(classDir)
	if err != nil {
		return nil
	}
	defer r.Close()
	// Slice 5: skip summaries already folded into the latest digest.
	// Digests cover the chronological prefix, so the first
	// SummaryCountIn entries we encounter (in iteration order) are
	// the ones to skip. Zero when no digest exists yet — the loop is
	// the same as pre-Slice-5 behavior.
	skip := s.digestCoveredCount()
	skipped := 0
	var current, prior []journal.ThinkSessionSummaryPayload
	for {
		e, err := r.Next()
		if err != nil {
			break // io.EOF or read error — return what we got
		}
		if e.Type != journal.TypeThinkSessionSummary {
			continue
		}
		p, perr := journal.ParseThinkSessionSummary(e)
		if perr != nil {
			continue
		}
		if skipped < skip {
			skipped++
			continue
		}
		if p.SessionID == s.sessionID {
			current = append(current, *p)
		} else {
			prior = append(prior, *p)
		}
	}
	current = tailN(current, currentCap)
	prior = tailN(prior, priorCap)
	out := make([]journal.ThinkSessionSummaryPayload, 0, len(prior)+len(current))
	out = append(out, prior...)
	out = append(out, current...)
	return out
}

// readLatestSessionDigest returns the most recent
// dream.session_digest payload from the workdir's journal, or nil
// when no digest exists yet. Iteration order is chronological, so
// the LAST entry observed is the latest one. Read failures yield
// nil — the caller falls back to the raw-summary path.
func (s *replState) readLatestSessionDigest() *journal.DreamSessionDigestPayload {
	if s.workdir == "" {
		return nil
	}
	r, err := journal.NewReader(filepath.Join(s.workdir, ".cortex", "journal", "dream"))
	if err != nil {
		return nil
	}
	defer r.Close()
	var latest *journal.DreamSessionDigestPayload
	for {
		e, rerr := r.Next()
		if rerr != nil {
			break
		}
		if e.Type != journal.TypeDreamSessionDigest {
			continue
		}
		p, perr := journal.ParseDreamSessionDigest(e)
		if perr != nil {
			continue
		}
		latest = p
	}
	return latest
}

// digestCoveredCount returns the SummaryCountIn from the most recent
// dream.session_digest, or 0 if no digest exists. Used by
// readRecentSessionSummaries to skip the summaries already folded
// in — avoids double-injection of older history.
func (s *replState) digestCoveredCount() int {
	if d := s.readLatestSessionDigest(); d != nil {
		return d.SummaryCountIn
	}
	return 0
}

// digestAndSummariesAsChatMessages renders the combined prior-context
// block: digest narrative first (when present) as a single labeled
// chunk, then the recent post-digest summaries via the existing
// summariesAsChatMessages path. When no digest exists, behaves
// identically to a direct summariesAsChatMessages call.
func digestAndSummariesAsChatMessages(digest *journal.DreamSessionDigestPayload, summaries []journal.ThinkSessionSummaryPayload) []llm.ChatMessage {
	if digest == nil {
		return summariesAsChatMessages(summaries)
	}
	if len(summaries) == 0 && digest == nil {
		return nil
	}
	var b strings.Builder
	b.WriteString("CONVERSATION SO FAR (summary of prior turns; not user input):\n\n")
	b.WriteString("[digest covering ")
	fmt.Fprintf(&b, "%d earlier turn(s)] ", digest.SummaryCountIn)
	b.WriteString(strings.TrimSpace(digest.Narrative))
	b.WriteByte('\n')

	currentID := ""
	if len(summaries) > 0 {
		currentID = summaries[len(summaries)-1].SessionID
	}
	for _, p := range summaries {
		tag := ""
		if p.SessionID != currentID {
			tag = "[prior session] "
		}
		intentTag := ""
		if p.Intent != "" {
			intentTag = "[intent=" + p.Intent + "] "
		}
		fmt.Fprintf(&b, "\n%s%s[turn %d] %s\n", tag, intentTag, p.Turn, strings.TrimSpace(p.Summary))
	}
	return []llm.ChatMessage{
		{Role: "user", Content: b.String()},
		{Role: "assistant", Content: "Understood — I'll treat that as context, not as new user input."},
	}
}

// tailN returns the last n elements of s. n ≤ 0 returns nil; n ≥
// len(s) returns s verbatim. Used to cap the current/prior summary
// slices independently before they're merged.
func tailN(s []journal.ThinkSessionSummaryPayload, n int) []journal.ThinkSessionSummaryPayload {
	if n <= 0 || len(s) == 0 {
		return nil
	}
	if len(s) <= n {
		return s
	}
	return s[len(s)-n:]
}

// summariesAsChatMessages converts a slice of session summaries into
// the harness's prior-messages slot. We emit a single user-role
// message containing all summaries — the model treats it as
// "background on what's already happened" without confusing it for an
// actual turn the user took. One message keeps the transcript shape
// flat and predictable for the inner loop.
//
// Item 4: prior-session entries are surfaced with a "[prior session]"
// tag so the model can distinguish older context from the current
// session's turn-by-turn history. Both share a single block; the
// boundary is visual, not structural.
func summariesAsChatMessages(summaries []journal.ThinkSessionSummaryPayload) []llm.ChatMessage {
	if len(summaries) == 0 {
		return nil
	}
	// currentSessionID is implicit in the slice — the readRecent
	// function appends prior entries first, then current. We carry
	// it forward by looking at the last entry's session id when
	// available; ties to a single id when only one session is in
	// play.
	var currentID string
	if len(summaries) > 0 {
		currentID = summaries[len(summaries)-1].SessionID
	}
	var b strings.Builder
	b.WriteString("CONVERSATION SO FAR (summary of prior turns; not user input):\n")
	for _, p := range summaries {
		tag := ""
		if p.SessionID != currentID {
			tag = "[prior session] "
		}
		// Slice 3: surface the per-turn intent in the prior-message
		// block so the model can passively weight relevance to the
		// current turn. The tag is informational — readers (the LLM)
		// decide whether to lean on a [intent=recall] turn for a
		// recall question without the harness having to filter ahead
		// of time. Omitted when the legacy entry had no intent.
		intentTag := ""
		if p.Intent != "" {
			intentTag = "[intent=" + p.Intent + "] "
		}
		fmt.Fprintf(&b, "\n%s%s[turn %d] %s\n", tag, intentTag, p.Turn, strings.TrimSpace(p.Summary))
	}
	return []llm.ChatMessage{
		{Role: "user", Content: b.String()},
		{Role: "assistant", Content: "Understood — I'll treat that as context, not as new user input."},
	}
}

// runHarness wraps evalv2.CortexHarness.RunSessionWithResult. Returns
// zero values when the underlying construction fails (e.g. Ollama not
// reachable) — caller surfaces via HarnessError.
//
// retryContext, when non-empty, is appended to the user prompt as a
// "previous attempt failed with this build/test error" tail so the
// model has the failure in context on a retry.
// Returns the classified intent + confidence alongside the harness
// result so callers can persist it on turnRow (first attempt only;
// retries reuse the original classification rather than re-running it).
func (s *replState) runHarness(userPrompt, retryContext string) (evalv2.HarnessResult, harness.LoopResult, string, float64, error) {
	// Phase 4: endpoint resolution. When the model id resolves to a
	// configured OpenAI-compatible endpoint (e.g. "chatterbox/..."),
	// bind the harness to that endpoint and strip the prefix before
	// the call to the LLM.
	cfg := loadREPLConfig(filepath.Join(s.workdir, ".cortex"))
	ep, bareModel, useEndpoint := cfg.ResolveModelRoute(s.model)

	h, err := evalv2.NewCortexHarness(s.model)
	if err != nil {
		return evalv2.HarnessResult{}, harness.LoopResult{}, "", 0, fmt.Errorf("init harness: %w", err)
	}
	if useEndpoint {
		h.SetEndpoint(&llm.EndpointConfig{
			Name:    ep.Name,
			BaseURL: ep.BaseURL,
			APIKey:  ep.ResolveAPIKey(),
		})
		h.SetModel(bareModel)
	}

	// Act-op dispatch is wired by the chain's decide.coding_turn
	// handler via CodingTurnConfig.ActRegistry. The chain path gives
	// parent_node_id real lineage (the executor's coding_turn node
	// ID).

	// Share the REPL's Cortex with the cortex_search tool so captures
	// from earlier turns in this session are findable. Nil cortex is
	// the legacy path: the tool builds its own Cortex on first call,
	// captures from this session aren't visible to it.
	if s.cortex != nil {
		h.SetSharedCortex(s.cortex)
	}
	if s.systemPrompt != "" {
		h.SetSystemPrompt(s.systemPrompt)
	}
	if prior := s.priorMessagesForHarness(); len(prior) > 0 {
		h.SetPriorMessages(prior)
	}
	if s.apiURL != "" {
		h.SetAPIURL(s.apiURL)
	}
	// Iter-4 auto-trigger ("Ollama → drop to 3 tools") was tuned for
	// qwen2.5-coder:1.5b which lost function-call discipline at ≥5
	// tools. The current floor is 7B+ (mistral 7b and qwen-coder 7b
	// both handle the 5-tool surface fine), and the iter-7 leanjs run
	// surfaced the real cost: dropping list_dir means the model can't
	// orient in an unfamiliar workdir even when its system prompt
	// tells it to. Default is now full 5 tools.
	//
	// --minimal-tools is the explicit opt-out for users still on
	// tiny models. --full-tools stays as a no-op alias so existing
	// scripts/benchmark harnesses don't break.
	if s.minimalTools {
		h.SetMinimalTools(true)
	}
	// Stream the agent loop into the REPL: one line per tool call so
	// the human sees what's happening during long turns (gpt-oss-20b
	// takes 20-30s per turn; without this it's a silent stare). Token
	// + per-turn telemetry is gated behind --verbose.
	h.SetNotify(makeREPLNotifier(s.ui, s.verbose))
	turns := defaultMaxTurns
	if s.maxTurns > 0 {
		turns = s.maxTurns
	}
	h.SetMaxTurns(turns)
	h.SetMaxOutputTokens(defaultMaxOutputTokens)
	// Per-attempt budget overrides for benchmark harnesses. Leaving
	// either field at 0 lets internal/harness fall back to its own
	// defaults (300k cumulative tokens, $0.20 cost).
	if s.maxCostUSD > 0 || s.maxCumulativeTokens > 0 {
		h.SetBudget(harness.Budget{
			MaxCostUSD:          s.maxCostUSD,
			MaxCumulativeTokens: s.maxCumulativeTokens,
		})
	}

	prompt := userPrompt
	if retryContext != "" {
		prompt = userPrompt + "\n\nPREVIOUS ATTEMPT FAILED. Verifier output:\n" + retryContext + "\n\nFix this, then stop."
	}

	// Every REPL turn is a dag.Executor.Run over the dynamic-DAG seed.
	// The preconfigured harness flows in via CodingTurnConfig.HarnessFactory
	// so the chain's decide.coding_turn node drives this same instance
	// (with all REPL state already set above). The full HarnessResult
	// + LoopResult come back via ResultCallback.
	hr, lr, intent, intentConf, runErr := runREPLChainTurn(s, h, prompt)

	// Fix B: if the model emitted a tool call as fenced JSON in the
	// response text instead of via the OpenAI tool_calls field, salvage
	// it. Currently only handles write_file (the dominant case) — see
	// salvageTextToolCall for the rationale and scope.
	if salvaged, _ := s.salvageTextToolCall(hr, lr); len(salvaged) > 0 {
		hr.FilesChanged = mergeFiles(hr.FilesChanged, salvaged)
	}
	return hr, lr, intent, intentConf, runErr
}

// runREPLChainTurn executes one REPL turn as a dynamic dag.Executor.Run
// over a minimal seed (sense.prompt → decide.next). decide.next
// inspects the prompt and grows the tree based on the user's intent:
// conversational prompts produce a 3-node tree, code prompts produce
// a coding_turn branch, search-augmented prompts produce a longer
// chain. Same seed, different shape per prompt — this is the
// dynamic-DAG slice that replaces the prior fixed 8-node chain.
//
// The preconfigured harness flows in via CodingTurnConfig.HarnessFactory
// so when decide.next spawns decide.coding_turn the chain drives THIS
// instance (with all REPL state already set: notifier, system prompt,
// shared cortex, prior messages, budget, minimal tools). The full
// HarnessResult + LoopResult are captured back via ResultCallback.
//
// Executor runs in sequential mode so search → decide.next is
// guaranteed FIFO (parallel mode would race them).
//
// TODO (DAG state is per-turn, not per-session): registry + executor
// are rebuilt fresh every turn and the calibration snapshot is loaded
// cold each time. Observed costs/latencies/success rates from this
// turn — exactly the signal that would let CanAfford decisions get
// smarter — are discarded at function return. decide.next's composition
// shape ("for prompts like this, last time spawning [vector_search,
// coding_turn] worked, parallel act.read_file did not") is also lost.
// Hoist the registry onto replState (or a session-scoped Registry
// pool), persist per-op observed cost back into the calibration store
// at finalize, and let decide.next see "shapes that worked recently"
// as part of NextConfig. This is the cross-turn DAG learning the
// inverse "DAG learns what to spawn" pitch depends on.
func runREPLChainTurn(s *replState, h *evalv2.CortexHarness, prompt string) (evalv2.HarnessResult, harness.LoopResult, string, float64, error) {
	turnID := fmt.Sprintf("repl-%d", time.Now().UnixNano())

	tw, _ := dagtrace.NewWriter("")
	var traceCB dag.TraceCallback
	if tw != nil {
		traceCB = tw.Callback(turnID)
	}
	// Always wrap with the stdout streamer so the user sees the DAG
	// shape emerge live. The wrapper invokes the underlying writer
	// callback (when present) after each print so dag_traces.jsonl
	// still captures every entry.
	traceCB = makeREPLDAGTracer(s.ui, traceCB)

	actReg := dag.NewRegistry()
	// Build a shared provider for the salience compressor so per-tool
	// compression runs through a real Reflect-style small-model pass
	// rather than the truncate-stub fallback. Same provider the
	// REPL's dynamic registry uses for the other LLM-backed ops.
	compressProvider := buildLLMProviderForREPL(loadREPLConfig(filepath.Join(s.workdir, ".cortex")), s.model, s.apiURL)
	if _, err := dagnode.RegisterDefaultActOpMetadataWithCompressor(actReg, compressProvider); err != nil {
		return evalv2.HarnessResult{}, harness.LoopResult{}, "", 0, fmt.Errorf("act-op metadata: %w", err)
	}

	// Phase 3 Slice 1: capability-aware salience cap. Weaker models
	// get a tighter cap (favoring fan-out into many small reads);
	// stronger models get a looser cap (so a synthesis turn sees rich
	// uncompressed evidence). ctxWindow=0 means "use the model id
	// alone"; future iteration will probe the endpoint's /v1/models
	// for max_context_window.
	salienceCap, ctxClass := salienceCapForSession(s.model, s.effectiveContextWindow())
	if s.verbose {
		s.ui.Info(fmt.Sprintf("  · salience cap=%d (model class=%s)", salienceCap, ctxClass))
	}

	var (
		capturedHR  evalv2.HarnessResult
		capturedLR  harness.LoopResult
		capturedErr error
	)
	// Accumulator budget: 2x the per-tool salience cap leaves room
	// for several folded observations before attend.compact rolls
	// the older ones up. salienceCap is already calibrated per
	// model-context-class (200 small / 500 medium / 1500 large) so
	// the accumulator scales with model capability automatically.
	accumulatorMaxTokens := salienceCap * 2
	if accumulatorMaxTokens <= 0 {
		accumulatorMaxTokens = 1000
	}
	codingCfg := dagnode.CodingTurnConfig{
		Model:                 s.model,
		Workdir:               s.workdir,
		ActRegistry:           actReg,
		TraceCB:               traceCB,
		ToolOutputSalienceCap: salienceCap,
		// Bounded working memory: each act.* output gets folded
		// through attend.accumulate so the synthesis decide.coding_turn
		// (and any decide.next that re-decides mid-plan) sees the
		// distilled snapshot via dag.LatestAccumulatorSnapshot instead
		// of re-fetching from raw tool transcripts. Shares the same
		// LLM provider as the salience compressor — one small model
		// driving both compression paths.
		AccumulatorProvider:  compressProvider,
		AccumulatorMaxTokens: accumulatorMaxTokens,
		// AccumulatorIntent stays empty here: intent isn't known until
		// after sense.classify_intent runs (below), which is after this
		// cfg is captured by the dynamic registry. The accumulator
		// prompt tolerates empty intent (renders as "Intent: " — the
		// model treats the user's surrounding instructions as the
		// effective intent). Threading per-turn intent into the
		// dispatcher is a follow-up if the empty path drifts.
		HarnessFactory: func() (*evalv2.CortexHarness, error) {
			return h, nil
		},
		ResultCallback: func(_ *evalv2.CortexHarness, hr evalv2.HarnessResult, lr harness.LoopResult, runErr error) {
			capturedHR = hr
			capturedLR = lr
			capturedErr = runErr
		},
	}

	// Shared OnResponse for every intent-aware terminal node
	// (act.passthrough, decide.clarify, decide.recall_summary). Lands
	// the reply in the same LoopResult.Final the REPL prints + journals
	// so trivial-intent paths are indistinguishable from a coding_turn
	// response from the renderer's point of view.
	intentTerminalOnResponse := func(reply string) {
		capturedLR.Final = reply
		capturedHR.OutputText = reply
	}

	reg, err := buildREPLDynamicRegistry(s, prompt, codingCfg, intentTerminalOnResponse, traceCB)
	if err != nil {
		return evalv2.HarnessResult{}, harness.LoopResult{}, "", 0, err
	}

	// Warm the registry from the calibration snapshot (Stage 4-C)
	// so pre-spawn CanAfford checks reflect observed reality. Missing
	// snapshot is a cold start; not an error.
	_, _ = dag.LoadCalibrationSnapshot(reg, "")

	ex := dag.NewExecutor(reg, traceCB)
	// Sequential mode: when decide.next spawns [vector_search,
	// decide.next] for the search arm, FIFO ordering puts vector_search
	// before the follow-up decide.next. Parallel mode would race them
	// and the follow-up wouldn't see search results.
	ex.SetSequential(true)

	// Intent ingestion: classify the prompt before seeding the DAG.
	// Drives per-intent budget (dag.BudgetForIntent) AND seed shape —
	// trivial intents bypass the sense.prompt → decide.next → coding_turn
	// chain in favor of cheaper terminal nodes (act.passthrough for
	// greetings). Failure modes return intent=code with confidence=0 so
	// the seed falls back to today's full chain.
	intent, intentConf := classifyIntentForTurn(reg, prompt, traceCB)
	// Cold-journal escalation: decide.recall_summary has nothing useful
	// to say when storage holds no events matching the prompt. Better
	// to let the agent actually investigate (read README, list dirs)
	// than reply "no prior context indexed". The downgrade returns
	// intent="code" with the original confidence so the seed falls
	// through to the full chain under DefaultTurnBudget.
	intent = downgradeRecallIfNoContext(intent, prompt, s.store)
	turnBudget := dag.BudgetForIntent(intent)
	// Layer the model's n_ctx onto the seed budget so handlers
	// (attend.accumulate, decide.next, attend.compact) can size
	// their own prompts via budget.PromptBudget(). Probe-cache miss
	// → MaxContextTokens stays 0 (unknown); handlers fall back to
	// calibrated defaults and the harness loop's catch-and-retry
	// path still learns n_ctx from any overflow.
	if cfg := loadREPLConfig(filepath.Join(s.workdir, ".cortex")); cfg != nil {
		if ep, bareModel, useEp := cfg.ResolveModelRoute(s.model); useEp {
			if p, ok := study.LookupCached(filepath.Join(s.workdir, ".cortex"), bareModel, ep.Name); ok && p.CtxWindowTokens > 0 {
				turnBudget = turnBudget.WithMaxContextTokens(p.CtxWindowTokens)
			}
		}
	}
	seed := seedForIntent(intent, intentConf, prompt)
	// Slice 4: stash the original prompt when we're about to clarify
	// so the NEXT turn's stitchClarifyFollowUp can combine it with
	// the user's answer before re-classifying. Cleared on consumption
	// (one-shot) or replaced when this turn re-clarifies. Non-clarify
	// seeds clear the stash so a stale clarify doesn't follow the
	// user across an unrelated turn.
	if seed[0].QualifiedName() == "decide.clarify" {
		s.lastClarifyPrompt = prompt
	} else {
		s.lastClarifyPrompt = ""
	}
	if s.verbose {
		s.ui.Info(fmt.Sprintf("  · intent=%s (conf=%.2f), budget=%s, seed=%s",
			intent, intentConf, turnBudget, seed[0].QualifiedName()))
	}
	if _, err := ex.Run(context.Background(), turnID, seed, turnBudget); err != nil {
		return capturedHR, capturedLR, intent, intentConf, fmt.Errorf("repl chain executor: %w", err)
	}
	return capturedHR, capturedLR, intent, intentConf, capturedErr
}

// classifyIntentForTurn runs the sense.classify_intent handler
// directly (outside the DAG executor) so the result is known at
// seed-time and can drive both budget selection and seed shape.
// Returns ("code", 0) on any failure — safe default routes to the
// existing full pipeline so misclassifications never block a turn.
//
// Slice 6: synthesizes a dag.TraceEntry for the classifier call and
// invokes traceCB with it before returning. Lets the routing
// decision appear in dag_traces.jsonl + the stdout streamer
// alongside the seed it produced, even though the executor never
// touched it. traceCB may be nil — tests pass nil to skip the
// emission.
func classifyIntentForTurn(reg *dag.Registry, prompt string, traceCB dag.TraceCallback) (string, float64) {
	started := time.Now()
	const (
		classifyNodeID = "n0-intent"
		classifySeedID = "n1"
	)
	spec, err := reg.Get("sense.classify_intent")
	if err != nil {
		emitClassifyTraceEntry(traceCB, classifyNodeID, classifySeedID, ops.IntentCode, 0, "spec_not_registered", err.Error(), nil, started)
		return ops.IntentCode, 0
	}
	res, herr := spec.Handler(context.Background(), map[string]any{"prompt": prompt}, dag.DefaultTurnBudget())
	if herr != nil {
		emitClassifyTraceEntry(traceCB, classifyNodeID, classifySeedID, ops.IntentCode, 0, "handler_error", herr.Error(), nil, started)
		return ops.IntentCode, 0
	}
	intent, _ := res.Out["intent"].(string)
	if intent == "" {
		intent = ops.IntentCode
	}
	confidence, _ := res.Out["confidence"].(float64)
	emitClassifyTraceEntry(traceCB, classifyNodeID, classifySeedID, intent, confidence, "", "", &res, started)
	return intent, confidence
}

// emitClassifyTraceEntry synthesizes a dag.TraceEntry for the
// out-of-executor classifier call and invokes traceCB with it. Nil
// traceCB → no-op so tests and headless paths work without
// instrumentation. Includes the cost the handler reported (when
// available) so downstream analyzers see the classifier's cost
// alongside the rest of the turn.
func emitClassifyTraceEntry(traceCB dag.TraceCallback, nodeID, seedID, intent string, confidence float64, errCode, errMsg string, res *dag.NodeResult, started time.Time) {
	if traceCB == nil {
		return
	}
	end := time.Now()
	entry := dag.TraceEntry{
		NodeID:          nodeID,
		ParentID:        "", // entry-point — runs before the seed
		QualifiedName:   "sense.classify_intent",
		OK:              errCode == "",
		ErrorCode:       errCode,
		ErrorMessage:    errMsg,
		SpawnedChildren: []string{seedID},
		WallStart:       started,
		WallEnd:         end,
		Out: map[string]any{
			"intent":     intent,
			"confidence": confidence,
		},
	}
	if res != nil {
		entry.CostConsumed = res.CostConsumed
		// Surface fallback + why too so the trace tells the full story
		// (LLM result vs safe-default route).
		if fb, ok := res.Out["fallback"].(bool); ok {
			entry.Out["fallback"] = fb
		}
		if why, ok := res.Out["why"].(string); ok && why != "" {
			entry.Out["why"] = why
		}
	} else {
		// No result (registry miss / handler error). Bill a tiny wall-
		// time so the row's duration isn't 0ms which would skew p50s.
		entry.CostConsumed = dag.Cost{LatencyMS: int(end.Sub(started).Milliseconds())}
	}
	traceCB(entry)
}

// sessionDigestThreshold is the number of think.session_summary
// entries that must accumulate before maybeWriteSessionDigest will
// fire. Sized so first-time users don't pay the digest cost in their
// first short session; long-running users pay it once per ~15 turns
// of accumulated history.
const sessionDigestThreshold = 15

// sessionDigestMaxTokens is the target output budget for the digest
// narrative. Sized to comfortably fit alongside the active prior-
// message block (6 recent summaries × ~200 tokens) plus the system
// prompt without dominating the context window. Below the
// attend.compact template's 800-token cap; the cap wins if they
// diverge.
const sessionDigestMaxTokens = 800

// sessionDigestIntent is the salience-preservation hint passed to
// attend.compact when re-summarizing K session summaries into the
// durable narrative. Tells the compactor which facts matter when it
// has to choose between competing content.
const sessionDigestIntent = "session-digest: preserve DURABLE decisions, constraints, cross-cutting work, and open threads. Group thematically, not by turn number. Drop pleasantries, routine edits, and verifier passes that didn't fail."

// maybeWriteSessionDigest checks whether the journal has accumulated
// enough new think.session_summary entries since the most recent
// dream.session_digest to warrant a fresh digest. When the threshold
// is crossed, it folds those summaries into a single consolidated
// narrative via attend.compact and writes the result as a
// dream.session_digest entry. Best-effort: any failure is returned
// but the caller logs+continues — a missing digest doesn't break
// the REPL, it just leaves more raw summaries in the prior-context
// block until the next opportunity.
//
// No-op when the threshold isn't crossed, when there's no provider
// available, or when s.workdir is empty.
func (s *replState) maybeWriteSessionDigest() error {
	if s.workdir == "" {
		return nil
	}
	summaries, lastDigestCovers, err := s.readSummariesForDigest()
	if err != nil {
		return fmt.Errorf("read summaries: %w", err)
	}
	newSinceLast := len(summaries) - lastDigestCovers
	if newSinceLast < sessionDigestThreshold {
		return nil
	}

	cfg := loadREPLConfig(filepath.Join(s.workdir, ".cortex"))
	provider := buildLLMProviderForREPL(cfg, s.model, s.apiURL)
	if provider == nil || !provider.IsAvailable() {
		return nil // can't compose; skip silently — try again next turn
	}

	// Build the snapshots input — one string per summary, chronological.
	snapshots := make([]string, len(summaries))
	for i, p := range summaries {
		intentTag := ""
		if p.Intent != "" {
			intentTag = "[intent=" + p.Intent + "] "
		}
		snapshots[i] = fmt.Sprintf("[session=%s turn=%d] %s%s", p.SessionID, p.Turn, intentTag, strings.TrimSpace(p.Summary))
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	res, herr := ops.CompactSpec(ops.CompactConfig{Provider: provider}).Handler(ctx,
		map[string]any{
			"snapshots":  snapshots,
			"max_tokens": sessionDigestMaxTokens,
			"intent":     sessionDigestIntent,
		},
		dag.Budget{LatencyMS: 30000, Tokens: 6000, Depth: 1, OutputTokens: sessionDigestMaxTokens * 2})
	if herr != nil {
		return fmt.Errorf("attend.compact: %w", herr)
	}
	narrative, _ := res.Out["snapshot"].(string)
	if strings.TrimSpace(narrative) == "" {
		return fmt.Errorf("attend.compact returned empty narrative")
	}
	snapTokens, _ := res.Out["snapshot_tokens"].(int)
	fallback, _ := res.Out["fallback"].(bool)
	compressOp := "attend.compact"
	if fallback {
		compressOp = "fallback"
	}

	// Build the journal payload. CoversSessionIDs lets a future
	// invalidation pass scope retraction effects to digests that
	// covered the retracted session.
	sessionSet := map[string]bool{}
	for _, p := range summaries {
		sessionSet[p.SessionID] = true
	}
	covers := make([]string, 0, len(sessionSet))
	for id := range sessionSet {
		covers = append(covers, id)
	}
	sort.Strings(covers)

	origTotal := 0
	for _, sn := range snapshots {
		origTotal += len(sn) / 4
	}

	entry, err := journal.NewDreamSessionDigestEntry(journal.DreamSessionDigestPayload{
		Narrative:        strings.TrimSpace(narrative),
		SummaryCountIn:   len(summaries),
		CoversSessionIDs: covers,
		OrigTokens:       origTotal,
		KeptTokens:       snapTokens,
		CompressOp:       compressOp,
	})
	if err != nil {
		return fmt.Errorf("build entry: %w", err)
	}
	classDir := filepath.Join(s.workdir, ".cortex", "journal", "dream")
	w, err := journal.NewWriter(journal.WriterOpts{
		ClassDir: classDir,
		Fsync:    journal.FsyncPerEntry,
	})
	if err != nil {
		return fmt.Errorf("open writer: %w", err)
	}
	defer w.Close()
	if _, err := w.Append(entry); err != nil {
		return fmt.Errorf("append entry: %w", err)
	}
	return nil
}

// readSummariesForDigest scans the workdir journal and returns the
// full chronological list of think.session_summary payloads plus the
// SummaryCountIn from the most recent dream.session_digest (0 when
// no digest exists yet). The diff between len(summaries) and the
// covers count is the "new since last digest" pressure that triggers
// the next digest.
func (s *replState) readSummariesForDigest() ([]journal.ThinkSessionSummaryPayload, int, error) {
	classDir := filepath.Join(s.workdir, ".cortex", "journal", "think")
	r, err := journal.NewReader(classDir)
	if err != nil {
		return nil, 0, nil // no think dir → nothing to digest, no error
	}
	defer r.Close()
	var summaries []journal.ThinkSessionSummaryPayload
	for {
		e, rerr := r.Next()
		if rerr != nil {
			break
		}
		if e.Type != journal.TypeThinkSessionSummary {
			continue
		}
		p, perr := journal.ParseThinkSessionSummary(e)
		if perr != nil {
			continue
		}
		summaries = append(summaries, *p)
	}

	// Latest digest's SummaryCountIn — captures how many summaries
	// from the head of `summaries` are already folded into the most
	// recent digest. Reads the dream class; absence is fine (0).
	covers := 0
	dr, derr := journal.NewReader(filepath.Join(s.workdir, ".cortex", "journal", "dream"))
	if derr == nil {
		defer dr.Close()
		for {
			e, rerr := dr.Next()
			if rerr != nil {
				break
			}
			if e.Type != journal.TypeDreamSessionDigest {
				continue
			}
			d, perr := journal.ParseDreamSessionDigest(e)
			if perr != nil {
				continue
			}
			// Take the LAST one (highest covers) since iteration is
			// chronological and digests only ever grow their cover set.
			covers = d.SummaryCountIn
		}
	}
	return summaries, covers, nil
}

// IntentRollup is one aggregated row in a per-intent breakdown of a
// session's turnRows. Slice 6 ships the aggregator standalone; future
// surfaces (cortex repl stats / dashboards / eval summaries) can
// import it without re-implementing the math.
type IntentRollup struct {
	Intent         string
	Count          int
	P50LatencyMs   int64
	P95LatencyMs   int64
	TotalTokensIn  int
	TotalTokensOut int
	TotalCostUSD   float64
}

// aggregateByIntent groups turnRows by their Intent field and returns
// one IntentRollup per distinct intent, sorted by Count descending
// (most-frequent intents first). Empty intent rows are folded into
// the "(none)" bucket so legacy turnRows from before Slice 3 still
// show up rather than being silently dropped.
//
// Empty input → empty output, no error. Suitable for stats commands
// that just want to print "0 turns" rather than special-case.
func aggregateByIntent(rows []turnRow) []IntentRollup {
	if len(rows) == 0 {
		return nil
	}
	buckets := map[string][]turnRow{}
	for _, r := range rows {
		key := r.Intent
		if key == "" {
			key = "(none)"
		}
		buckets[key] = append(buckets[key], r)
	}
	out := make([]IntentRollup, 0, len(buckets))
	for intent, rs := range buckets {
		out = append(out, intentRollupFor(intent, rs))
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Count != out[j].Count {
			return out[i].Count > out[j].Count
		}
		return out[i].Intent < out[j].Intent // stable secondary sort
	})
	return out
}

// intentRollupFor builds one IntentRollup from a per-intent slice of
// rows. P50 / P95 are computed in-place over a copy of the latency
// values; tokens / cost are summed directly. Single-row buckets
// yield P50=P95=that row's latency.
func intentRollupFor(intent string, rows []turnRow) IntentRollup {
	r := IntentRollup{Intent: intent, Count: len(rows)}
	latencies := make([]int64, len(rows))
	for i, row := range rows {
		latencies[i] = row.LatencyMs
		r.TotalTokensIn += row.TokensIn
		r.TotalTokensOut += row.TokensOut
		r.TotalCostUSD += row.CostUSD
	}
	sort.Slice(latencies, func(i, j int) bool { return latencies[i] < latencies[j] })
	r.P50LatencyMs = percentile(latencies, 0.50)
	r.P95LatencyMs = percentile(latencies, 0.95)
	return r
}

// percentile returns the p-th percentile (0.0 <= p <= 1.0) from a
// pre-sorted slice using the nearest-rank method (ceil(N*p) - 1).
// This places p=0 at the smallest value, p=1 at the largest, and
// p=0.95 on N=3 at the third value rather than the second — which
// matches what humans expect when reading "p95 latency" on small
// samples. Empty slice → 0; single element → that element.
//
// Not a stats-grade implementation. Fine for REPL session sizes
// (tens to hundreds of turns).
func percentile(sorted []int64, p float64) int64 {
	if len(sorted) == 0 {
		return 0
	}
	if len(sorted) == 1 {
		return sorted[0]
	}
	// ceil(N*p) - 1: math.Ceil avoided by adding (N - 1) before
	// integer division.
	n := len(sorted)
	idx := (int(float64(n)*p*1000) + 999) / 1000
	if idx > 0 {
		idx--
	}
	if idx < 0 {
		idx = 0
	}
	if idx >= n {
		idx = n - 1
	}
	return sorted[idx]
}

// detectFeedbackCue mechanically classifies a user prompt as
// "correction", "confirmation", or "" (none). The detector is a
// small keyword pass — not an LLM call — so the per-turn cost is
// negligible. False positives cost a misclassified feedback entry
// the user can /cortex-forget; false negatives just mean we miss
// the signal on a particular turn. Both are recoverable.
//
// Correction trumps confirmation when both fire (the user changing
// course is the load-bearing signal). Word boundaries matter — "no
// problem" must NOT trigger correction, "no, that's wrong" must.
func detectFeedbackCue(prompt string) string {
	p := " " + strings.ToLower(strings.TrimSpace(prompt)) + " "
	for _, marker := range correctionMarkers {
		if strings.Contains(p, marker) {
			return "correction"
		}
	}
	for _, marker := range confirmationMarkers {
		if strings.Contains(p, marker) {
			return "confirmation"
		}
	}
	return ""
}

// correctionMarkers are space-padded so they only match on word
// boundaries. "no problem" deliberately doesn't appear here so it
// can't masquerade as "no" — the pad guarantees we look for "no"
// followed by punctuation/whitespace + a contrastive continuation,
// expressed below as the specific patterns we actually want to
// catch.
var correctionMarkers = []string{
	"no, ", "no. ", "nope, ", "nope. ",
	" wrong ", " incorrect ", " incorrectly ",
	" actually ", " instead ", " not what ",
	" that's not ", " that isn't ", " that is not ",
	" don't ", " dont ", " stop doing ",
}

// confirmationMarkers — similarly space-padded. Generous on
// "thanks" / "perfect" / "exactly" since those are unambiguous
// positive cues that rarely appear inside corrections.
var confirmationMarkers = []string{
	" perfect ", " perfect.", " perfect!",
	" thanks ", " thanks.", " thanks!", " thank you ",
	" exactly ", " exactly.", " exactly!",
	" got it ", " got it.", " got it!", " got it,",
	" yes that ", " yes, that ", " yep, that ",
	" that worked ", " that works ",
	" nice ", " nice.", " nice!",
}

// emitFeedbackEntry writes a feedback.{correction,confirmation}
// journal entry linked to the prior turn via GradedID. Best-effort:
// any failure is returned but the caller logs+continues — the user's
// turn must not block on a feedback emit.
//
// The GradedID format mirrors what the eventual capture entry for
// the prior turn produces (repl-<sessionID>-turn-<N>) so a future
// projector can join feedback to its target row by string key. When
// the cue fires on turn 1 (s.turns == 0), there's no prior turn to
// grade — skip silently.
func (s *replState) emitFeedbackEntry(cue, prompt string) error {
	if s.workdir == "" || s.turns == 0 {
		return nil
	}
	var typ string
	switch cue {
	case "correction":
		typ = journal.TypeFeedbackCorrection
	case "confirmation":
		typ = journal.TypeFeedbackConfirmation
	default:
		return nil
	}
	gradedID := fmt.Sprintf("repl-%s-turn-%d", s.sessionID, s.turns)
	payload := journal.FeedbackPayload{
		GradedID:  gradedID,
		Note:      strings.TrimSpace(prompt),
		SessionID: s.sessionID,
	}
	entry, err := journal.NewFeedbackEntry(typ, payload)
	if err != nil {
		return fmt.Errorf("build feedback entry: %w", err)
	}
	classDir := filepath.Join(s.workdir, ".cortex", "journal", "feedback")
	w, err := journal.NewWriter(journal.WriterOpts{
		ClassDir: classDir,
		Fsync:    journal.FsyncPerEntry,
	})
	if err != nil {
		return fmt.Errorf("open feedback writer: %w", err)
	}
	defer w.Close()
	if _, err := w.Append(entry); err != nil {
		return fmt.Errorf("append feedback: %w", err)
	}
	return nil
}

// stitchClarifyFollowUp returns the combined prompt when the prior
// turn routed to decide.clarify and stashed its original ambiguous
// prompt. Resets the stash so only the immediately-following turn
// benefits. Returns the input prompt unchanged otherwise.
//
// Format: the classifier sees both the prior ambiguous prompt and
// the current answer in one block — "delete it" + "the migrations
// table" → "delete the migrations table" semantics — so it can
// pick the disambiguated intent (likely code) instead of
// re-classifying the bare answer.
func (s *replState) stitchClarifyFollowUp(prompt string) string {
	if s.lastClarifyPrompt == "" {
		return prompt
	}
	prior := s.lastClarifyPrompt
	s.lastClarifyPrompt = ""
	return fmt.Sprintf("Original (ambiguous) request: %s\nUser's clarification: %s", prior, prompt)
}

// downgradeRecallIfNoContext returns intent unchanged unless it's
// "recall" AND the storage probe finds nothing relevant to the
// prompt. In that case it downgrades to "code" so the seed becomes
// the full sense.prompt chain under DefaultTurnBudget — the agent
// can investigate (list dirs, read files) instead of replying "no
// prior context indexed" on a cold journal.
//
// Nil storage → downgrade. A recall question we can't answer from
// storage is better served by the agent than by an apology.
//
// The probe tokenizes the prompt into content words and asks storage
// for any event matching ANY word. Full-string substring matching
// (what SearchEvents does) practically never hits on natural-language
// recall questions — "what did we decide about postgres?" won't be a
// substring of any stored event. Per-word OR matching catches the
// realistic case: at least one content word from the question
// (postgres, auth, pgx, …) appears in some prior tool-result or
// tool-input.
func downgradeRecallIfNoContext(intent, prompt string, store *storage.Storage) string {
	if intent != "recall" {
		return intent
	}
	if store == nil {
		return ops.IntentCode
	}
	terms := recallProbeTerms(prompt)
	if len(terms) == 0 {
		return ops.IntentCode
	}
	matches, err := store.SearchEventsMultiTerm(terms, 1)
	if err != nil || len(matches) == 0 {
		return ops.IntentCode
	}
	return intent
}

// recallProbeTerms extracts content words from a recall prompt: lower
// case, ≥4 characters, not in the small stopword set. The threshold
// is deliberately generous — false positives (downgrade=false when
// downgrade=true would have been kinder) cost the user a recall_summary
// that says "no context"; false negatives cost a full coding-turn
// when a recall would have been cheaper. Both are recoverable; the
// false-positive case is worse so we err toward keeping recall when
// any plausible word might match.
func recallProbeTerms(prompt string) []string {
	stop := map[string]bool{
		"what": true, "when": true, "where": true, "which": true, "who": true,
		"why": true, "how": true, "that": true, "this": true, "with": true,
		"about": true, "from": true, "into": true, "have": true, "were": true,
		"been": true, "they": true, "them": true, "their": true, "there": true,
		"will": true, "would": true, "could": true, "should": true,
		"again": true, "just": true, "your": true, "ours": true, "decide": true,
		"decided": true, "settled": true, "remind": true,
	}
	var out []string
	seen := map[string]bool{}
	// Split on non-letter runs so "postgres?" → "postgres" without a
	// regex dependency. ASCII-only; non-ASCII content words won't be
	// indexed for the probe but storage's substring match handles
	// them downstream if SearchEventsMultiTerm gets routed differently
	// later.
	cur := make([]byte, 0, 16)
	flush := func() {
		if len(cur) < 4 {
			cur = cur[:0]
			return
		}
		w := string(cur)
		cur = cur[:0]
		if stop[w] || seen[w] {
			return
		}
		seen[w] = true
		out = append(out, w)
	}
	for i := 0; i < len(prompt); i++ {
		c := prompt[i]
		switch {
		case c >= 'A' && c <= 'Z':
			cur = append(cur, c+32)
		case c >= 'a' && c <= 'z':
			cur = append(cur, c)
		default:
			flush()
		}
	}
	flush()
	return out
}

// intentShortCircuitThreshold gates intent-aware short-circuits.
// Below this confidence every intent routes through the full chain —
// better to pay the coding_turn cost than to give a canned greeting,
// a wrong clarifying question, or an unrelated recall to someone who
// actually wanted real work done.
const intentShortCircuitThreshold = 0.7

// seedForIntent picks the per-turn seed spec list based on the
// classified intent. High-confidence trivial intents bypass the
// sense.prompt → decide.next → coding_turn chain in favor of a
// dedicated terminal node:
//
//	greeting → act.passthrough        (mechanical, zero LLM)
//	clarify  → decide.clarify         (one short LLM call, one question, end turn)
//	recall   → decide.recall_summary  (search storage, synthesize prose)
//
// Everything else (code / review / meta, or any low-confidence
// classification) falls through to the existing sense.prompt seed.
// Per-intent budgets from dag.BudgetForIntent give defense-in-depth:
// even when the seed falls through, a misclassified trivial intent
// runs under a budget too tight for decide.coding_turn to spawn.
func seedForIntent(intent string, confidence float64, prompt string) []dag.NodeSpec {
	if confidence >= intentShortCircuitThreshold {
		switch intent {
		case "greeting":
			return []dag.NodeSpec{{
				Function: dag.FuncAct,
				Op:       "passthrough",
				ID:       "n1",
				Attrs:    map[string]any{"prompt": prompt},
			}}
		case "clarify":
			return []dag.NodeSpec{{
				Function: dag.FuncDecide,
				Op:       "clarify",
				ID:       "n1",
				Attrs:    map[string]any{"prompt": prompt},
			}}
		case "recall":
			return []dag.NodeSpec{{
				Function: dag.FuncDecide,
				Op:       "recall_summary",
				ID:       "n1",
				Attrs:    map[string]any{"prompt": prompt},
			}}
		}
	}
	return []dag.NodeSpec{{
		Function: dag.FuncSense,
		Op:       "prompt",
		ID:       "n1",
		Attrs:    map[string]any{"prompt": prompt},
	}}
}

// buildREPLDynamicRegistry builds the registry for the dynamic-DAG
// REPL path. decide.next is the steering op — it sees the live op
// catalog + live model catalog via NextConfig and emits the nodes to
// spawn. Workflow ops the LLM can compose: decide.coding_turn (with
// optional attrs.model for per-node routing), decide.next (recurse),
// remember.vector_search.
//
// The registry is captured by reference inside decide.next's handler
// closure, so subsequent registrations (decide.coding_turn,
// sense.prompt) are visible when the handler runs.
func buildREPLDynamicRegistry(s *replState, prompt string, codingCfg dagnode.CodingTurnConfig, intentTerminalOnResponse func(string), traceCB dag.TraceCallback) (*dag.Registry, error) {
	reg := dag.NewRegistry()
	if _, err := ops.RegisterDefaults(reg, ops.DefaultsConfig{}); err != nil {
		return nil, fmt.Errorf("ops defaults: %w", err)
	}

	// sense.classify_intent — routes each turn to a per-intent budget
	// + seed shape (see classifyIntentForTurn + seedForIntent in
	// runREPLChainTurn). Registered Exposable so decide.next can
	// re-invoke it if its plan needs to re-classify mid-chain.
	classifyProvider := buildLLMProviderForREPL(loadREPLConfig(filepath.Join(s.workdir, ".cortex")), s.model, s.apiURL)
	if err := reg.Register(ops.ClassifyIntentSpec(ops.ClassifyIntentConfig{
		Provider: classifyProvider,
	})); err != nil {
		return nil, fmt.Errorf("register sense.classify_intent: %w", err)
	}

	// act.passthrough — mechanical zero-LLM terminal for trivial
	// conversational turns. OnResponse threads the chosen reply back
	// into the captured LoopResult so the standard render + journal
	// path is unchanged.
	if err := reg.Register(ops.PassthroughSpec(ops.PassthroughConfig{
		OnResponse: intentTerminalOnResponse,
	})); err != nil {
		return nil, fmt.Errorf("register act.passthrough: %w", err)
	}

	// decide.clarify — small-LLM terminal that asks ONE focused
	// question for ambiguous prompts and ends the turn. Reuses
	// classifyProvider (same model used for the upstream classifier;
	// the work is similarly narrow JSON-output).
	if err := reg.Register(ops.ClarifySpec(ops.ClarifyConfig{
		Provider:   classifyProvider,
		OnResponse: intentTerminalOnResponse,
	})); err != nil {
		return nil, fmt.Errorf("register decide.clarify: %w", err)
	}

	// decide.recall_summary — terminal node for the `recall` intent.
	// Searches s.store via text search (no embedder required; the
	// REPL session today doesn't wire one through newSessionCognition),
	// then asks the small model to synthesize a prose answer grounded
	// in the matches. s.store may be nil on cold start — the op
	// degrades to a `grounded=false` synthesis from the prompt alone.
	if err := reg.Register(ops.RecallSummarySpec(ops.RecallSummaryConfig{
		Provider:   classifyProvider,
		Storage:    s.store,
		OnResponse: intentTerminalOnResponse,
	})); err != nil {
		return nil, fmt.Errorf("register decide.recall_summary: %w", err)
	}

	// decide.next — the steering op. Provider + Registry + ModelCatalog
	// give it everything it needs to compose multi-op DAGs at call
	// time. ProviderFactory enables per-call routing: when the LLM
	// emits a decide.next spawn with `model: "<id>"`, that recursive
	// classification call uses factory.Get(id) instead of the session
	// default. Lets the steering layer compose multiple small
	// specialists (e.g., 3B classifier + 14B re-decider).
	//
	// Provider nil falls back to a single coding_turn spawn so the
	// chain always walks.
	cfg := loadREPLConfig(filepath.Join(s.workdir, ".cortex"))
	nextProvider := buildLLMProviderForREPL(cfg, s.model, s.apiURL)
	nextFactory := buildProviderFactoryForREPL(cfg, s.model, s.apiURL)
	modelCatalog := s.modelCatalogForREPL()
	// Phase 3 Slice 2: surface this turn's capability class + salience
	// cap to decide.next so the planner sees its physical-system
	// boundaries when choosing fanout shape (small/tight → many
	// narrow nodes; large/loose → fewer bigger nodes). Empty-friendly
	// fallback for zero codingCfg is handled inside formatBoundariesBlock.
	cap := codingCfg.ToolOutputSalienceCap
	ctxWindow := s.effectiveContextWindow()
	class := llm.InferContextClass(s.model, ctxWindow)
	systemBoundaries := fmt.Sprintf(
		"- Model: %s (capability class: %s, ctx=%d)\n- Per-tool-call salience cap: %d tokens (oversized outputs auto-compress)\n",
		s.model, class, ctxWindow, cap,
	)
	// Phase 3 Slice 3 dovetail: when the project-study DAG has
	// completed, surface a one-liner so decide.next plans against
	// stored insights rather than fanning out to re-read README.
	if line := studyBoundariesLine(s.workdir); line != "" {
		systemBoundaries += line
	}
	if err := reg.Register(ops.NextSpec(ops.NextConfig{
		Provider:         nextProvider,
		ProviderFactory:  nextFactory,
		Registry:         reg,
		ModelCatalog:     modelCatalog,
		SystemBoundaries: systemBoundaries,
	})); err != nil {
		return nil, fmt.Errorf("register decide.next: %w", err)
	}

	// sense.prompt — REPL-flavored: spawns decide.next (instead of
	// the static chain's represent.embed).
	if err := reg.Register(dag.NodeSpec{
		Function:    dag.FuncSense,
		Op:          "prompt",
		Description: "ingress: user prompt arrives; spawns decide.next to steer the turn",
		Cost:        dag.Cost{LatencyMS: 5, Tokens: 0},
		Handler: func(ctx context.Context, in map[string]any, b dag.Budget) (dag.NodeResult, error) {
			p, _ := in["prompt"].(string)
			if p == "" {
				p = prompt
			}
			return dag.NodeResult{
				Out: map[string]any{"prompt": p},
				Spawn: []dag.NodeSpec{{
					Function: dag.FuncDecide, Op: "next",
					Attrs: map[string]any{"prompt": p},
				}},
				CostConsumed: dag.Cost{LatencyMS: 5},
			}, nil
		},
	}); err != nil {
		return nil, fmt.Errorf("register sense.prompt: %w", err)
	}

	// decide.coding_turn — runs the harness's agent loop. Exposable
	// to decide.next's LLM so it can compose coding_turn calls (with
	// optional attrs.model per-node) into multi-step workflows.
	// HarnessFactory + ResultCallback flow through the existing
	// REPL state; reattempt wrapper handles fetch-op retries.
	rawCT := dagnode.NewCodingTurnHandler(codingCfg)
	ctHandler := wrapCodingTurnWithReattempt(rawCT, codingCfg, traceCB)
	if err := reg.Register(dag.NodeSpec{
		Function:    dag.FuncDecide,
		Op:          "coding_turn",
		Description: "run one LLM agent-loop turn against the workdir; emits a response and may write/run files; supports attrs.model to route this call to a different model",
		Inputs: []dag.ParamSpec{
			{Name: "prompt", Type: "string", Required: true},
			{Name: "model", Type: "string"},
		},
		Outputs: []dag.ParamSpec{
			{Name: "response", Type: "string"},
		},
		Cost:      dag.Cost{LatencyMS: 15000, Tokens: 2000},
		Exposable: true,
		Handler:   ctHandler,
	}); err != nil {
		return nil, fmt.Errorf("register decide.coding_turn: %w", err)
	}

	// Real act.* handlers registered on the main registry so the
	// executor can spawn them directly. decide.tool_call's emitted
	// `act.<tool>` NodeSpecs land here. Coding_turn still has its
	// own dispatcher path (via codingCfg.ActRegistry → actReg) — this
	// surface is purely for DAG-level spawns where the specialist
	// tool-caller has already produced structured args.
	actToolReg := harness.NewToolRegistry()
	readTool := harness.NewReadFileTool(s.workdir)
	writeTool := harness.NewWriteFileTool(s.workdir, actToolReg)
	listTool := harness.NewListDirTool(s.workdir)
	shellTool := harness.NewRunShellTool(s.workdir, actToolReg)
	actToolReg.Register(readTool)
	actToolReg.Register(writeTool)
	actToolReg.Register(listTool)
	actToolReg.Register(shellTool)

	contracts := dagnode.DefaultActOpContracts()
	costs := dagnode.DefaultActOpCosts()
	for _, t := range []harness.ToolHandler{readTool, listTool, writeTool, shellTool} {
		name := t.Name()
		spec := dagnode.AdaptToolAsAct(dagnode.ActOpConfig{
			Handler:  t,
			Contract: contracts[name],
			Cost:     costs[name],
		})
		spec.Exposable = true
		if err := reg.Register(spec); err != nil {
			return nil, fmt.Errorf("register act.%s: %w", name, err)
		}
	}

	// decide.tool_call — specialist function-calling node. Routes a
	// natural-language intent through a small purpose-built model
	// (default = session model; can be routed per-call via attrs.model
	// e.g. xlam-1.5b). Spawns the resolved act.<tool>.
	if err := reg.Register(ops.ToolCallSpec(ops.ToolCallConfig{
		Provider:              nextProvider,
		ProviderFactory:       nextFactory,
		Registry:              reg,
		ToolOutputSalienceCap: codingCfg.ToolOutputSalienceCap,
	})); err != nil {
		return nil, fmt.Errorf("register decide.tool_call: %w", err)
	}

	return reg, nil
}

// verifier kinds — small enum, easy to extend.
const (
	verifierNone    = "none"
	verifierGoBuild = "go build"
	verifierCustom  = "custom"
)

type verifyResult struct {
	Kind       string // "go build", "none", ...
	OK         bool
	OutputTail string
}

// runVerifier returns the gate result for a turn. The REPL no longer
// auto-picks a verifier by detecting project files (go.mod, package.json,
// etc.) — that was hardcoded language detection living outside the DAG.
// The agent loop uses run_shell to build/test itself when appropriate;
// double-checking from outside the loop is redundant.
//
// Two paths remain:
//  1. --verifier <cmd>: explicit user/benchmark-supplied shell command.
//     Treated as success on exit 0. SWE-bench-style headless runs use
//     this to gate the auto-retry loop.
//  2. fallback: no verifier; accept the turn. Trust the agent loop.
func (s *replState) runVerifier() verifyResult {
	if s.customVerifierCmd != "" {
		return s.runCustomVerifier()
	}
	return verifyResult{Kind: verifierNone, OK: true}
}

// runCustomVerifier runs the --verifier shell command in the workdir
// and reports OK iff the command exits 0. 5-minute hard cap so a
// hung test suite doesn't trap a benchmark run. Output is tailed to
// 8 KiB — enough to fit a few stack traces while keeping the
// retry-context prompt under the model's input cap.
func (s *replState) runCustomVerifier() verifyResult {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "sh", "-c", s.customVerifierCmd)
	cmd.Dir = s.workdir
	out, err := cmd.CombinedOutput()
	return verifyResult{
		Kind:       verifierCustom,
		OK:         err == nil,
		OutputTail: tailString(string(out), 8192),
	}
}

// gateDecision is what the user chose at the [r/e/s/q] prompt.
type gateDecision struct {
	kind gateKind
	hint string // populated only for gateRetry
}

type gateKind int

const (
	gateAccept gateKind = iota // verify passed (no prompt was shown)
	gateRetry                  // user wants the model to try again, optionally with hint
	gateEdit                   // user will fix files manually
	gateSkip                   // discard this turn
	gateQuit                   // discard and exit the REPL
)

// promptGate shows the [r/e/s/q] prompt after verify keeps failing.
// Reads one line; "r" then asks for an optional hint on the next line.
// Defaults (empty input) to skip — safest choice when the user is
// piping or got distracted.
func (s *replState) promptGate(v verifyResult) gateDecision {
	s.ui.Info(fmt.Sprintf("  verify still failing (%s).", v.Kind))
	resp, err := s.ui.ReadLine("  [r]etry / [e]dit / [s]kip / [q]uit: ")
	if err != nil {
		return gateDecision{kind: gateSkip}
	}
	switch strings.TrimSpace(strings.ToLower(resp)) {
	case "r", "retry":
		hint, _ := s.ui.ReadLine("  hint for the model (enter for none): ")
		return gateDecision{kind: gateRetry, hint: strings.TrimSpace(hint)}
	case "e", "edit":
		return gateDecision{kind: gateEdit}
	case "q", "quit":
		return gateDecision{kind: gateQuit}
	default: // "s", "skip", "" all map here
		return gateDecision{kind: gateSkip}
	}
}

// captureTurn fires a single capture event recording this REPL turn.
// Writes to the workdir-local .cortex/journal/capture/ so the daemon
// (or a later `cortex ingest`) projects it into the project's store.
// Best-effort: errors are returned to runTurn's finalize() but never
// fail the turn.
func (s *replState) captureTurn(row turnRow) error {
	if !row.Accepted {
		return nil
	}
	cap, err := s.ensureCaptureClient()
	if err != nil {
		return err
	}
	content := row.UserMessage
	if row.FinalText != "" {
		content = row.UserMessage + "\n\n" + row.FinalText
	}
	event := &events.Event{
		Source:    events.SourceGeneric,
		EventType: events.EventToolUse,
		Timestamp: time.Now(),
		ToolName:  "cortex-repl",
		ToolInput: map[string]interface{}{
			"type":          "repl_turn",
			"content":       content,
			"user_prompt":   row.UserMessage,
			"files_changed": row.FilesChanged,
			"model":         row.Model,
		},
		ToolResult: row.FinalText,
		Context: events.EventContext{
			ProjectPath: s.workdir,
			SessionID:   row.SessionID,
			WorkingDir:  s.workdir,
		},
		Metadata: map[string]interface{}{
			"capture_type": "repl_turn",
			"source":       "cortex-repl",
			"session_id":   row.SessionID,
			"turn":         row.Turn,
			"verify_kind":  row.VerifyKind,
			"verify_ok":    row.VerifyOK || row.RetryVerifyOK || row.UserRetryVerifyOK,
			"agent_turns":  row.AgentTurns,
			"tokens_in":    row.TokensIn,
			"tokens_out":   row.TokensOut,
		},
	}
	return cap.CaptureEvent(event)
}

// printTurnSummary prints the model's final response followed by a
// one-line metadata footer for an accepted turn. The response was
// previously captured to JSONL but never surfaced to the user —
// stats-only output is fine for benchmark mode but unusable when
// you're asking actual questions and want to see the answer.
//
// Derives the verify summary from the row's terminal verify status
// (covers all three rounds: initial, auto-retry, user-driven).
//
// TODO (collapses the retry path): the verify summary reduces three
// rounds of telemetry to "ok" / "FAIL". For an interactive learning
// harness the path itself is signal worth surfacing: "ok (retry 2
// +user hint)" tells the user the model needed coaching, "ok
// (initial)" tells them it landed first try. Both are free wins from
// data already on the row — no new capture, just a richer formatter
// that reads RetryVerifyOK/UserRetryVerifyOK/UserRetryHints and emits
// the path tag.
func (s *replState) printTurnSummary(row turnRow) {
	if final := strings.TrimSpace(row.FinalText); final != "" {
		s.ui.Info("")
		s.ui.Markdown(final)
		s.ui.Info("")
	}
	files := "0"
	if len(row.FilesChanged) > 0 {
		files = strings.Join(row.FilesChanged, ", ")
	}
	verify := "no-verify"
	if row.VerifyKind != verifierNone {
		ok := row.VerifyOK || row.RetryVerifyOK || row.UserRetryVerifyOK
		if ok {
			verify = row.VerifyKind + " ok"
		} else {
			verify = row.VerifyKind + " FAIL"
		}
	}
	s.ui.Info(fmt.Sprintf("  ✓ turn %d · files: %s · verify: %s · tokens: %d/%d · %dms",
		row.Turn, files, verify, row.TokensIn, row.TokensOut, row.LatencyMs))
}

// turnRow is the structured JSONL row written per turn. Fields are
// deliberately permissive — missing data stays zero/empty rather than
// erroring. Field names are snake_case for grep/jq friendliness.
//
// Three rounds of verify can happen on a single turn: the initial
// attempt, the automatic retry with verifier output, and zero or more
// user-driven [r]/[e] rounds. Each round gets its own VerifyOK +
// VerifyOutput pair so downstream analysis can distinguish "model
// got it second try" from "user hint saved the turn."
//
// TODO (schema caps at one auto-retry): the dedicated Retry* and
// UserRetry* fields only represent round 1 of each kind. With
// --max-retries N>1, attempts 2..N silently merge their files-changed
// into the row and lose per-attempt telemetry (tokens, latency, verify
// output) — see the comment in runTurn's auto-retry loop. Once the
// retry-policy diversification TODO lands (different model/temp/tool
// surface per round), per-attempt telemetry IS the signal worth
// keeping. Replace with `attempts: [{round, kind, model, tokens_in,
// tokens_out, verify_ok, verify_output, ...}]` and write a downgrade
// shim so existing jq consumers continue to work.
type turnRow struct {
	Turn         int    `json:"turn"`
	SessionID    string `json:"session_id"`
	Timestamp    string `json:"timestamp"`
	UserMessage  string `json:"user_message"`
	Model        string `json:"model"`
	APIURL       string `json:"api_url,omitempty"`
	SystemPrompt string `json:"system_prompt"`
	SnapshotDir  string `json:"snapshot_dir"`

	// Intent ingestion result (sense.classify_intent) from the FIRST
	// runHarness call. Retries reuse the same intent — the user's
	// underlying ask hasn't changed, only verifier feedback has.
	// Empty / zero on entries written before the field existed; readers
	// tolerate the absence.
	Intent           string  `json:"intent,omitempty"`
	IntentConfidence float64 `json:"intent_confidence,omitempty"`

	// Initial attempt (no error context).
	HarnessError          string   `json:"harness_error,omitempty"`
	AgentTurns            int      `json:"agent_turns"`
	TokensIn              int      `json:"tokens_in"`
	TokensOut             int      `json:"tokens_out"`
	InjectedContextTokens int      `json:"injected_context_tokens,omitempty"` // bytes the cortex_search tool returned across this turn / 4 (proxy)
	CostUSD               float64  `json:"cost_usd"`
	LatencyMs             int64    `json:"latency_ms"`
	FilesChanged          []string `json:"files_changed,omitempty"`
	FinalText             string   `json:"final_text,omitempty"`
	VerifyKind            string   `json:"verify_kind"`
	VerifyOK              bool     `json:"verify_ok"`
	VerifyOutput          string   `json:"verify_output,omitempty"`

	// Automatic single retry (model gets the verifier output).
	RetryAgentTurns   int    `json:"retry_agent_turns,omitempty"`
	RetryTokensIn     int    `json:"retry_tokens_in,omitempty"`
	RetryTokensOut    int    `json:"retry_tokens_out,omitempty"`
	RetryHarnessError string `json:"retry_harness_error,omitempty"`
	RetryFinalText    string `json:"retry_final_text,omitempty"`
	RetryVerifyOK     bool   `json:"retry_verify_ok,omitempty"`
	RetryVerifyOutput string `json:"retry_verify_output,omitempty"`

	// User-driven retry/edit loop (post-auto-retry). UserGate carries
	// the terminal decision ("retry", "edit", "skip", "quit"). Hints
	// is the pipe-joined sequence of user nudges across multiple [r]
	// rounds. The Verify pair reflects the latest user-round result.
	UserGate              string `json:"user_gate,omitempty"`
	UserRetryAttempts     int    `json:"user_retry_attempts,omitempty"`
	UserRetryHints        string `json:"user_retry_hints,omitempty"`
	UserRetryHarnessError string `json:"user_retry_harness_error,omitempty"`
	UserRetryFinalText    string `json:"user_retry_final_text,omitempty"`
	UserRetryVerifyOK     bool   `json:"user_retry_verify_ok,omitempty"`
	UserRetryVerifyOutput string `json:"user_retry_verify_output,omitempty"`

	Accepted bool `json:"accepted"`
}

func (s *replState) writeJSONL(row turnRow) error {
	b, err := json.Marshal(row)
	if err != nil {
		return err
	}
	if _, err := s.jsonl.Write(append(b, '\n')); err != nil {
		return err
	}
	return s.jsonl.Sync()
}

// snapshotWorkdir copies every small text-like file under workdir into
// <sessionDir>/snapshots/turn-<n>/. Skips .git, .cortex, files larger
// than snapshotMaxFileBytes. Returns the snapshot dir path.
//
// For GoL-sized workdirs this is microseconds. For large repos we
// should switch to git-based snapshots — flagged in PROGRESS-REPL.md.
//
// TODO (drop the snapshot system entirely, require git): the every-
// file copy per turn doesn't scale (Django-sized = tens of thousands
// of files even after the skip-list) AND the parallel snapshot
// machinery exists mostly to back /diff + /undo, which modern coding
// harnesses don't provide — Claude Code and Cursor punt to git/IDE;
// Aider has /undo + /diff but they're git-backed. Direction: require
// a git repo at session start (fail clearly + offer `git init` if
// missing, mirroring /ultrareview), delete snapshotWorkdir +
// restoreFromSnapshot + the snapshotStack field, and delete /diff +
// /undo from dispatchSlash. runTurn's rollback-on-fail becomes a
// no-op (keep-on-fail default) — users have `git checkout .` and
// `git stash` natively.
func (s *replState) snapshotWorkdir(turn int) (string, error) {
	snapDir := filepath.Join(s.sessionDir, "snapshots", fmt.Sprintf("turn-%03d", turn))
	if err := os.MkdirAll(snapDir, 0o755); err != nil {
		return "", err
	}

	manifest := map[string]string{}
	err := filepath.WalkDir(s.workdir, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(s.workdir, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		// Skip vendor + tooling state.
		top := strings.SplitN(rel, string(os.PathSeparator), 2)[0]
		if top == ".git" || top == ".cortex" || top == "node_modules" || top == "vendor" {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		if info.Size() > snapshotMaxFileBytes {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		dst := filepath.Join(snapDir, rel)
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(dst, data, info.Mode().Perm()); err != nil {
			return err
		}
		sum := sha256.Sum256(data)
		manifest[rel] = hex.EncodeToString(sum[:])
		return nil
	})
	if err != nil {
		return snapDir, err
	}
	mb, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return snapDir, err
	}
	if err := os.WriteFile(filepath.Join(snapDir, ".manifest.json"), mb, 0o644); err != nil {
		return snapDir, err
	}
	return snapDir, nil
}

// restoreFromSnapshot reverts the workdir to the contents of snapDir.
// Files present in snapshot are overwritten with snapshot content;
// files absent from snapshot (= created by the rejected turn) are
// removed. Skip-rules match snapshotWorkdir (.git, .cortex untouched).
func (s *replState) restoreFromSnapshot(snapDir string) error {
	if snapDir == "" {
		return fmt.Errorf("no snapshot to restore from")
	}
	manifestPath := filepath.Join(snapDir, ".manifest.json")
	mb, err := os.ReadFile(manifestPath)
	if err != nil {
		return fmt.Errorf("read manifest: %w", err)
	}
	var manifest map[string]string
	if err := json.Unmarshal(mb, &manifest); err != nil {
		return fmt.Errorf("parse manifest: %w", err)
	}
	for rel := range manifest {
		src := filepath.Join(snapDir, rel)
		dst := filepath.Join(s.workdir, rel)
		data, err := os.ReadFile(src)
		if err != nil {
			return fmt.Errorf("read snap %s: %w", rel, err)
		}
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return fmt.Errorf("mkdir for %s: %w", rel, err)
		}
		if err := os.WriteFile(dst, data, 0o644); err != nil {
			return fmt.Errorf("write back %s: %w", rel, err)
		}
	}
	// Delete files in the workdir that are NOT in the snapshot (those
	// were created by the rejected turn). Same skip rules as snapshot.
	return filepath.WalkDir(s.workdir, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(s.workdir, path)
		if err != nil || rel == "." {
			return err
		}
		top := strings.SplitN(rel, string(os.PathSeparator), 2)[0]
		if top == ".git" || top == ".cortex" || top == "node_modules" || top == "vendor" {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			return nil
		}
		if _, ok := manifest[rel]; !ok {
			_ = os.Remove(path)
		}
		return nil
	})
}

// undoLastTurn restores the workdir to the snapshot taken before the
// most recent accepted turn and pops the snapshot stack. Repeated
// /undo walks chronologically back to session start.
func (s *replState) undoLastTurn() error {
	n := len(s.snapshotStack)
	if n == 0 {
		return fmt.Errorf("nothing to undo")
	}
	top := s.snapshotStack[n-1]
	if err := s.restoreFromSnapshot(top); err != nil {
		return err
	}
	s.ui.Info(fmt.Sprintf("  undone turn %d (%d more available)", s.turns, n-1))
	s.turns--
	s.snapshotStack = s.snapshotStack[:n-1]
	// Pop the corresponding conversation-history entry so the model
	// doesn't see "I made that change" on the next turn for a change
	// that's been rolled back.
	if h := len(s.history); h > 0 {
		s.history = s.history[:h-1]
	}
	return nil
}

// printDiff lists files that differ between the most recent pre-turn
// snapshot and the current workdir. Best-effort — for v1 we list paths
// only, not unified diffs.
func (s *replState) printDiff() {
	n := len(s.snapshotStack)
	if n == 0 {
		s.ui.Info("  no accepted turns yet")
		return
	}
	top := s.snapshotStack[n-1]
	mb, err := os.ReadFile(filepath.Join(top, ".manifest.json"))
	if err != nil {
		s.ui.Info(fmt.Sprintf("  diff unavailable (manifest read: %v)", err))
		return
	}
	var manifest map[string]string
	if err := json.Unmarshal(mb, &manifest); err != nil {
		s.ui.Info(fmt.Sprintf("  diff unavailable (manifest parse: %v)", err))
		return
	}
	var changed, added, removed []string
	current := map[string]string{}
	filepath.WalkDir(s.workdir, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil || d.IsDir() {
			return nil
		}
		rel, _ := filepath.Rel(s.workdir, path)
		top := strings.SplitN(rel, string(os.PathSeparator), 2)[0]
		if top == ".git" || top == ".cortex" || top == "node_modules" || top == "vendor" {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		sum := sha256.Sum256(data)
		current[rel] = hex.EncodeToString(sum[:])
		return nil
	})
	for rel, sum := range current {
		if prev, ok := manifest[rel]; !ok {
			added = append(added, rel)
		} else if prev != sum {
			changed = append(changed, rel)
		}
	}
	for rel := range manifest {
		if _, ok := current[rel]; !ok {
			removed = append(removed, rel)
		}
	}
	sort.Strings(changed)
	sort.Strings(added)
	sort.Strings(removed)
	if len(changed)+len(added)+len(removed) == 0 {
		s.ui.Info("  no changes since pre-last-turn snapshot")
		return
	}
	s.ui.Info(fmt.Sprintf("  changes since pre-last-turn snapshot (turn %d):", s.turns))
	for _, p := range added {
		s.ui.Info(fmt.Sprintf("    + %s", p))
	}
	for _, p := range changed {
		s.ui.Info(fmt.Sprintf("    ~ %s", p))
	}
	for _, p := range removed {
		s.ui.Info(fmt.Sprintf("    - %s", p))
	}
}

// tailString returns the last n bytes of s, prefixed with "..." if it
// was truncated. Used to keep verifier output bounded in jsonl rows.
func tailString(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return "..." + s[len(s)-n:]
}

// mergeFiles dedupes two file-changed lists. Order: first-list-first.
func mergeFiles(a, b []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(a)+len(b))
	for _, x := range append(append([]string{}, a...), b...) {
		if !seen[x] {
			seen[x] = true
			out = append(out, x)
		}
	}
	return out
}

func errString(e error) string {
	if e == nil {
		return ""
	}
	return e.Error()
}

// ============================================================================
// Fix A — Ollama model probe + auto-pick
// ============================================================================
//
// qwen2.5-coder:1.5b can technically function-call but loses discipline as
// the registered-tool count grows (5 tools in our harness → emits the call
// as fenced JSON in `content` instead of `tool_calls`). The auto-pick path
// upgrades the default to a more capable installed model when the user
// hasn't pinned one. See PROGRESS-REPL.md iteration 3 for the probe matrix.

// ollamaTagsURL converts the OpenAI-compat endpoint we use for chat
// completions into the native `/api/tags` URL Ollama uses for model
// inventory. We don't validate further — if the host isn't actually
// Ollama, the probe times out and we fall back.
func ollamaTagsURL(chatAPI string) string {
	u, err := url.Parse(chatAPI)
	if err != nil {
		return ""
	}
	return u.Scheme + "://" + u.Host + "/api/tags"
}

// ollamaTagsResponse is the subset of `/api/tags` we care about.
type ollamaTagsResponse struct {
	Models []struct {
		Name string `json:"name"`
	} `json:"models"`
}

// printShellPolicy shows the active run_shell allow/deny policy.
// Resolution mirrors harness.LoadShellPolicy: per-workdir first, then
// global, then empty. We re-load here rather than cache because the
// user may have edited the JSON file mid-session.
func (s *replState) printShellPolicy() {
	policy := harness.LoadShellPolicy(s.workdir)
	if policy.IsEmpty() {
		s.ui.Info("  shell policy: none active (run_shell permits all commands)")
		s.ui.Info(fmt.Sprintf("  to configure, create %s with {\"allow\":[...],\"deny\":[...]}",
			filepath.Join(s.workdir, ".cortex", "shell-policy.json")))
		return
	}
	if len(policy.Deny) > 0 {
		s.ui.Info("  deny patterns (any match rejects):")
		for _, p := range policy.Deny {
			s.ui.Info(fmt.Sprintf("    %s", p))
		}
	}
	if len(policy.Allow) > 0 {
		s.ui.Info("  allow patterns (one match required):")
		for _, p := range policy.Allow {
			s.ui.Info(fmt.Sprintf("    %s", p))
		}
	}
}

// printModels handles the /models slash command: fetches Ollama
// (fresh each time, local + fast) and OpenRouter (cached per session
// unless refresh=true, since the catalogue is ~300+ entries) and
// renders both as two sections, capped at modelListLimit entries each.
//
// Ollama uses the current REPL's apiURL if it's Ollama-shaped, else
// the default Ollama URL — this lets the user list local models even
// when currently routed to OpenRouter.
func (s *replState) printModels(refresh bool) {
	ollamaProbeURL := s.apiURL
	if ollamaProbeURL == "" {
		ollamaProbeURL = defaultOllamaAPIURL
	}
	ollamaModels, ollamaAvailable, ollamaErr := listOllamaModels(ollamaProbeURL)

	s.ui.Info("  Ollama (local):")
	switch {
	case !ollamaAvailable:
		s.ui.Info("    (unreachable — run `ollama serve` to enable)")
	case ollamaErr != nil:
		s.ui.Info(fmt.Sprintf("    (error: %v)", ollamaErr))
	case len(ollamaModels) == 0:
		s.ui.Info("    (none installed — try `ollama pull qwen2.5-coder:1.5b`)")
	default:
		printModelListOllama(s.ui, ollamaModels)
	}

	s.ui.Info("  OpenRouter:")
	if refresh || s.openRouterModelsCache == nil && s.openRouterModelsErr == nil {
		s.openRouterModelsCache, s.openRouterModelsErr = fetchOpenRouterModels()
	}
	switch {
	case s.openRouterModelsErr != nil:
		s.ui.Info(fmt.Sprintf("    (error: %v)", s.openRouterModelsErr))
	case len(s.openRouterModelsCache) == 0:
		s.ui.Info("    (empty catalogue)")
	default:
		printModelListOpenRouter(s.ui, s.openRouterModelsCache)
	}
}

// modelListLimit caps how many models per section /models prints
// before collapsing the tail into a "+N more" footer. Keeps the
// REPL surface usable when OpenRouter returns 300+ models.
const modelListLimit = 20

// fetchOpenRouterModels calls OpenRouterClient.ListModels using a
// throwaway client constructed for the catalog call only (no
// session-bound state). Results are sorted by ID for deterministic
// output. The endpoint is unauthenticated; a missing key doesn't
// block discovery.
//
// allowlist:llm.NewOpenRouterClient — catalog discovery, not a runtime
// provider for generation/embedding. The provider-resolution guard
// test exempts this site.
func fetchOpenRouterModels() ([]llm.OpenRouterModel, error) {
	client := llm.NewOpenRouterClient(nil)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	models, err := client.ListModels(ctx)
	if err != nil {
		return nil, err
	}
	sort.Slice(models, func(i, j int) bool { return models[i].ID < models[j].ID })
	return models, nil
}

func printModelListOllama(ui cliout.Sink, names []string) {
	sort.Strings(names)
	shown := names
	if len(shown) > modelListLimit {
		shown = shown[:modelListLimit]
	}
	for _, n := range shown {
		ui.Info(fmt.Sprintf("    %s", n))
	}
	if extra := len(names) - len(shown); extra > 0 {
		ui.Info(fmt.Sprintf("    +%d more", extra))
	}
}

func printModelListOpenRouter(ui cliout.Sink, models []llm.OpenRouterModel) {
	shown := models
	if len(shown) > modelListLimit {
		shown = shown[:modelListLimit]
	}
	for _, m := range shown {
		ctx := ""
		if m.ContextLength > 0 {
			ctx = fmt.Sprintf(" %s ctx", humanCtx(m.ContextLength))
		}
		// Prices in API are USD/token; print per 1M to be human-readable.
		price := ""
		if m.PricePromptPerTok > 0 || m.PriceComplPerTok > 0 {
			price = fmt.Sprintf(" $%.2f/$%.2f per 1M (in/out)",
				m.PricePromptPerTok*1_000_000, m.PriceComplPerTok*1_000_000)
		}
		ui.Info(fmt.Sprintf("    %s%s%s", m.ID, ctx, price))
	}
	if extra := len(models) - len(shown); extra > 0 {
		ui.Info(fmt.Sprintf("    +%d more — /model <id> to pin a specific one", extra))
	}
}

// humanCtx formats a token count as k or M for compactness. 8192 → 8k.
func humanCtx(n int) string {
	switch {
	case n >= 1_000_000:
		return fmt.Sprintf("%.1fM", float64(n)/1_000_000)
	case n >= 1_000:
		return fmt.Sprintf("%dk", n/1_000)
	default:
		return fmt.Sprintf("%d", n)
	}
}

// listOllamaModels returns the names of all models installed on the
// Ollama server reachable at chatAPI's host. Returns (names, available,
// err): available is false iff Ollama itself is unreachable (so the
// caller can distinguish "Ollama is down" from "Ollama is up but
// returned an unexpected body").
//
// Extracted from probeOllamaAndPickModel so /models can list without
// re-implementing the /api/tags fetch.
func listOllamaModels(chatAPI string) ([]string, bool, error) {
	tagsURL := ollamaTagsURL(chatAPI)
	if tagsURL == "" {
		return nil, true, fmt.Errorf("ollama: cannot derive /api/tags URL from %q", chatAPI)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, tagsURL, nil)
	if err != nil {
		return nil, true, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, false, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, false, fmt.Errorf("ollama /api/tags: status %d", resp.StatusCode)
	}
	var tags ollamaTagsResponse
	if err := json.NewDecoder(resp.Body).Decode(&tags); err != nil {
		return nil, true, err
	}
	out := make([]string, 0, len(tags.Models))
	for _, m := range tags.Models {
		out = append(out, m.Name)
	}
	return out, true, nil
}

// TODO (Ollama-only auto-probe is not portable): this whole path
// assumes Ollama. There's no equivalent for vLLM / llama.cpp / LM
// Studio / OpenRouter ("you have key X, here are the catalog entries
// matching your project's language" etc.). Pair with the resolveAPIURL
// backend-registry TODO: each registered backend should expose a
// `ListAvailable() ([]ModelInfo, error)` method so probe is backend-
// agnostic and the REPL works the same against any local-or-remote
// inference server.
//
// TODO (probe is one-shot at startup): runs once in Execute and never
// re-evaluates. If the user runs `ollama pull qwen2.5-coder:14b`
// mid-session, the new model is invisible until restart. /models
// refresh busts the OpenRouter catalog cache but doesn't re-run the
// Ollama probe. Either re-probe on /model and /models, or watch
// `~/.ollama/models` for changes.
//
// probeOllamaAndPickModel asks Ollama what's installed, scores each
// model for tool-calling fitness, and returns the best choice. The
// fallback is what we return if (a) Ollama is reachable but our
// fallback is itself the best option, or (b) the scoring decides no
// installed model beats the fallback meaningfully.
//
// Returns (chosen, available, note):
//   - chosen: the model id to use
//   - available: false iff Ollama itself is unreachable (we surface a
//     warning in that case so the user knows it'll fail until they
//     start Ollama)
//   - note: human-readable explanation when chosen != fallback;
//     empty when no swap happened
func probeOllamaAndPickModel(chatAPI, fallback string) (string, bool, string) {
	installed, available, err := listOllamaModels(chatAPI)
	if !available {
		return fallback, false, ""
	}
	if err != nil {
		return fallback, true, ""
	}
	chosen := pickBestOllamaModel(installed, fallback)
	if chosen == fallback {
		return fallback, true, ""
	}
	note := fmt.Sprintf("cortex: auto-selected %s (more reliable tool-calling than %s default; pin with --model or CORTEX_REPL_MODEL to override)", chosen, fallback)
	return chosen, true, note
}

// pickBestOllamaModel ranks installed Ollama models by tool-calling
// fitness for the cortex harness's 5-tool registry. Higher is better.
//
// Scoring rubric (tuned via live REPL runs — last revision 2026-05-19
// after mistral:7b was caught emitting text-shape fake tool calls on
// leanjs while qwen2.5-coder:7b emits proper structured tool_calls):
//
//	+30 if model family is in the "known good function-callers" list
//	     (qwen2.5-coder ≥3b, llama3.1/3.2, mistral-nemo, granite-code,
//	      command-r)
//	+10 if the name contains "coder" or "instruct"
//	+size-bucket-bonus by parameter count parsed from the name suffix
//	−50 if the model family is known-broken for our tool registry
//	     (gemma2, qwen2.5:0.5b, qwen2:0.5b, tinyllama, smollm,
//	      mistral:7b/8b — see iter-7 note: these emit prose
//	      describing tool use rather than actual tool_calls)
//
// Ties broken by name (alphabetical, deterministic). We only return a
// non-fallback if the best score beats the fallback's score by at
// least 10 — we don't want to swap a 1.5b for a 2b on a tiny margin.
func pickBestOllamaModel(installed []string, fallback string) string {
	type ranked struct {
		name  string
		score int
	}
	rs := make([]ranked, 0, len(installed))
	rs = append(rs, ranked{name: fallback, score: scoreOllamaModel(fallback)})
	seen := map[string]bool{fallback: true}
	for _, m := range installed {
		if seen[m] {
			continue
		}
		seen[m] = true
		rs = append(rs, ranked{name: m, score: scoreOllamaModel(m)})
	}
	sort.SliceStable(rs, func(i, j int) bool {
		if rs[i].score != rs[j].score {
			return rs[i].score > rs[j].score
		}
		return rs[i].name < rs[j].name
	})
	if rs[0].name == fallback {
		return fallback
	}
	fallbackScore := scoreOllamaModel(fallback)
	if rs[0].score-fallbackScore < 10 {
		return fallback
	}
	return rs[0].name
}

// modelSizeRegex matches the parameter-count suffix Ollama uses:
//
//	qwen2.5-coder:1.5b → 1.5b → 1500_000_000
//	mistral:7b         → 7b   → 7000_000_000
//	smollm:360m        → 360m → 360_000_000
//	llama3.2:latest    → (no match → 0)
var modelSizeRegex = regexp.MustCompile(`(?i):(\d+(?:\.\d+)?)([mb])`)

// TODO (replace hardcoded rubric with learned fitness): every entry
// in knownGood/knownBad is "iter-X taught us model Y does/doesn't
// emit structured tool_calls". That knowledge is exactly what the
// salvage-telemetry TODO measures — observed tool-call success rate
// per model — so this should be a lookup against the learning store,
// not a Go literal. First-turn calibration probe + observed-rate
// score gives a clean, dynamic, project-agnostic alternative: no
// list to maintain, the REPL adapts as new models arrive, and the
// scoring is introspectable (surface it in /models so users see WHY
// a model was picked). Drop the lists once observed-rate has enough
// signal; keep this as a cold-start prior only.
//
// TODO (project-agnostic ≠ project-blind): the rubric weights size
// + "coder/instruct" suffix uniformly. A Python project on a workdir
// where the user's last 10 turns were pytest-shaped wants a different
// preference than a Go project. Once the learning store carries
// per-project per-model success rates, score should consult them.
// Generic prior + project-tuned posterior is the shape.
//
// scoreOllamaModel applies the rubric in pickBestOllamaModel.
//
// Capability-based scoring (CapToolCalling, CapCoding) uses
// llm.InferCapabilities — the same id-pattern inference the
// ModelRegistry's Ollama probe runs. So a model that's "good" by this
// scorer is also tagged with the right capabilities in the registry's
// ModelInfo, and vice versa: there's one place these patterns live.
//
// knownBad survives as a cold-start backstop until observed-rate
// (per the salvage-telemetry TODO) replaces it — these are concrete
// iter-7 failures (mistral:7 emits prose pretending to call tools,
// phi3:mini doesn't support tools in Ollama at all) that capability
// inference can't see from the id alone.
func scoreOllamaModel(name string) int {
	lower := strings.ToLower(name)
	score := 0

	// Capability-driven positive scoring — same source the registry
	// uses for ModelInfo.Capabilities. Tool-calling is the strongest
	// signal because the REPL agent loop requires it; coding is a
	// secondary preference.
	caps := llm.InferCapabilities(name)
	if hasCap(caps, llm.CapToolCalling) {
		score += 25
	}
	if hasCap(caps, llm.CapCoding) {
		score += 15
	}

	knownBad := []string{
		"gemma2:", "gemma:",
		"qwen2.5:0.5", "qwen2:0.5", "qwen:0.5",
		"tinyllama", "smollm",
		"nomic-embed",
		"phi3:mini",  // doesn't support tools in Ollama at all
		"mistral:7",  // iter-7: emits prose pretending to call tools instead of structured tool_calls
		"mistral:8",  // same family idiosyncrasy as mistral:7
		"mistral:la", // matches mistral:latest, which is mistral:7b by default
	}
	for _, b := range knownBad {
		if strings.Contains(lower, b) {
			score -= 50
			break
		}
	}

	// Parameter-size bucket bonus.
	if m := modelSizeRegex.FindStringSubmatch(lower); m != nil {
		var bn float64
		fmt.Sscanf(m[1], "%f", &bn)
		unit := strings.ToLower(m[2])
		if unit == "m" {
			bn = bn / 1000.0
		}
		switch {
		case bn >= 13:
			score += 25
		case bn >= 7:
			score += 20
		case bn >= 3:
			score += 12
		case bn >= 1.5:
			score += 5
		case bn >= 0.5:
			score += 2
		}
	}

	return score
}

// hasCap returns true when c is present in caps. Small local helper
// since the registry's HasCapability is a method on ModelInfo, and the
// scorer works in []string directly.
func hasCap(caps []string, c string) bool {
	for _, x := range caps {
		if x == c {
			return true
		}
	}
	return false
}

// ============================================================================
// Fix B — Text-tool-call extractor
// ============================================================================
//
// When a small model emits a tool call as fenced JSON in the response
// content instead of via the OpenAI `tool_calls` field, the harness
// returns FilesChanged=[] and the verifier passes trivially against the
// pre-turn state. salvageTextToolCall scans the response for the shape
// the harness expected and executes the tool out-of-band so the turn
// produces real work. Conservative: only supports write_file (the
// dominant case) and only when no tool call was registered.
//
// TODO (migrate to decide.tool_call, then delete this stack): this
// whole salvage path is the small-model-amplifier story implemented as
// defensive regex parsing — a maintenance tax that grows linearly with
// model variety. The DAG-native answer is the decide.tool_call
// specialist node already registered in buildREPLDynamicRegistry: it
// takes natural-language intent + tool catalog and emits structured
// args via a purpose-built model (xLAM-style). Once that path is the
// default for models known to fail OpenAI tool_calls (route on model
// id or on observed salvage rate per TODO below), the entire
// extractToolCallFromText / extractToolCallByFieldRegex /
// repairBacktickStrings stack can be deleted.
//
// TODO (salvage bypasses the DAG): when salvageTextToolCall fires it
// runs the tool out-of-band — no act.* node spawn, no dag_traces.jsonl
// row, no cost or latency recorded. So the cross-turn DAG learning TODO
// on runREPLChainTurn never sees salvaged work; a session that survives
// only because of salvage looks like a session that worked first try.
// Route salvaged calls through the act.* registry instead so the trace
// is honest and the calibration store learns from them.
//
// TODO (add salvage telemetry): no metric tracks how often salvage
// fires per model. That's the signal worth keeping — a model with
// >X% salvage rate should auto-route through decide.tool_call (or get
// surfaced to the user as "this model needs the specialist router").
// Drop a counter into row + emit a per-session summary at finalize.

// toolCallTextRegex matches a JSON tool-call shape in arbitrary text:
//
//	{"name": "<tool>", "arguments": {...}}
//
// Tolerates surrounding markdown fences and prose. Captures the JSON
// object as a single group. Non-greedy on the trailing brace so prose
// after the JSON doesn't extend the match.
//
// Also matches the array-wrapped shape `[{"name":...,"arguments":...}]`
// which mistral:7b and several other models emit instead of the bare
// object shape. The leading `[` and trailing `]` are optional in the
// pattern — Go regex `?` is the right tool for this. The captures are
// the same regardless of wrapper.
var toolCallTextRegex = regexp.MustCompile(`(?s)\[?\s*\{\s*"name"\s*:\s*"([^"]+)"\s*,\s*"arguments"\s*:\s*(\{.*?\})\s*\}\s*\]?`)

// extractedToolCall is the parsed shape of a salvaged tool call.
type extractedToolCall struct {
	Name string
	Args map[string]any
}

// extractToolCallFromText finds the first JSON tool-call object in a
// text blob. Returns nil if none found. The blob is typically the
// `Final` text from a LoopResult.
//
// Tolerates four common small-model deviations from strict JSON, in
// fall-through order — cheapest first:
//   - strict JSON (the happy path)
//   - markdown code fences around the JSON (handled by the regex)
//   - array-wrapped shape `[{...}]` (mistral-style; handled by regex)
//   - backtick-delimited string values (qwen2.5-coder:1.5b habit;
//     handled by repairBacktickStrings)
//   - literal unescaped newlines in string values (mistral-7b habit;
//     handled by extractToolCallByFieldRegex as a last resort)
func extractToolCallFromText(text string) *extractedToolCall {
	m := toolCallTextRegex.FindStringSubmatch(text)
	if m == nil {
		return nil
	}
	raw := m[2]
	name := m[1]
	var args map[string]any
	if err := json.Unmarshal([]byte(raw), &args); err == nil {
		return &extractedToolCall{Name: name, Args: args}
	}
	// Fallback 1: repair backtick-delimited strings.
	repaired := repairBacktickStrings(raw)
	if err := json.Unmarshal([]byte(repaired), &args); err == nil {
		return &extractedToolCall{Name: name, Args: args}
	}
	// Fallback 2: when strings contain literal unescaped newlines, the
	// JSON unmarshal will never succeed. Extract path + content with
	// per-field regexes. Lossier (no other args) but covers the most
	// common write_file usage.
	if name == "write_file" {
		if call := extractToolCallByFieldRegex(text); call != nil {
			return call
		}
	}
	return nil
}

// fieldRegexPath captures a path field with any of the common
// small-model quoting styles: double quote, backtick, or single quote.
var fieldRegexPath = regexp.MustCompile("(?s)\"path\"\\s*:\\s*(?:\"([^\"]+)\"|`([^`]+)`|'([^']+)')")

// fieldRegexContent captures a content field, tolerating multi-line
// values bounded by any of double quote / backtick / single quote.
// Non-greedy on the closing quote so we don't swallow the surrounding
// JSON structure.
var fieldRegexContent = regexp.MustCompile("(?s)\"content\"\\s*:\\s*(?:\"((?:[^\"\\\\]|\\\\.)*?)\"|`(.*?)`|'((?:[^'\\\\]|\\\\.)*?)')")

// extractToolCallByFieldRegex is the last-resort write_file extractor:
// scan the whole text blob for `path` and `content` fields regardless
// of surrounding JSON validity. Used when proper Unmarshal fails
// because the content has literal newlines or other JSON-invalid bytes
// (a common mistral:7b emission pattern).
//
// When the matched content was inside double-quoted JSON it may carry
// escape sequences (\n, \t, \"); we run a single-pass decoder to turn
// them into the real characters so the file lands the same as if the
// model had properly tool-called. Backtick and single-quoted captures
// are used verbatim.
func extractToolCallByFieldRegex(text string) *extractedToolCall {
	pm := fieldRegexPath.FindStringSubmatch(text)
	cm := fieldRegexContent.FindStringSubmatch(text)
	if pm == nil || cm == nil {
		return nil
	}
	path := firstNonEmpty(pm[1], pm[2], pm[3])
	content := firstNonEmpty(cm[1], cm[2], cm[3])
	if path == "" {
		return nil
	}
	// Decode escape sequences only when the match came from the
	// double-quoted JSON arm (capture group 1). Backtick / single-
	// quoted forms use the bytes verbatim.
	if cm[1] != "" {
		content = decodeJSONStringEscapes(content)
	}
	return &extractedToolCall{
		Name: "write_file",
		Args: map[string]any{"path": path, "content": content},
	}
}

// decodeJSONStringEscapes turns JSON string escape sequences (\n, \t,
// \\, \") into their literal characters. We don't use json.Unmarshal
// because the source isn't valid JSON in context (it broke for the
// reason we're in this fallback).
func decodeJSONStringEscapes(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c != '\\' || i+1 >= len(s) {
			b.WriteByte(c)
			continue
		}
		next := s[i+1]
		switch next {
		case 'n':
			b.WriteByte('\n')
		case 't':
			b.WriteByte('\t')
		case 'r':
			b.WriteByte('\r')
		case '\\':
			b.WriteByte('\\')
		case '"':
			b.WriteByte('"')
		case '\'':
			b.WriteByte('\'')
		default:
			b.WriteByte(c)
			b.WriteByte(next)
		}
		i++
	}
	return b.String()
}

// firstNonEmpty returns the first non-empty string in vs, or "" if all
// are empty. Used by the per-field regex extractor which captures the
// same logical value across multiple alternation groups.
func firstNonEmpty(vs ...string) string {
	for _, v := range vs {
		if v != "" {
			return v
		}
	}
	return ""
}

// backtickStringRegex matches a JSON-shaped key/value where the value
// is wrapped in backticks instead of double quotes:
//
//	"content": `...arbitrary string...`
//
// The body capture is non-greedy and DOTALL so multi-line backtick
// content (the dominant case — code blocks) matches.
var backtickStringRegex = regexp.MustCompile("(?s)\"([^\"]+)\"\\s*:\\s*`(.*?)`")

// repairBacktickStrings rewrites backtick-delimited string values in a
// JSON-shaped blob to proper double-quoted JSON. Inside the backticks
// we escape `\` and `"` so the result round-trips through Unmarshal,
// but we leave existing `\n` / `\t` escape sequences alone — the model
// almost always meant them as escape sequences (it used backticks
// precisely to AVOID having to escape inner double quotes). This is a
// heuristic, but covers >95% of qwen-style emissions in practice.
//
// Limitation: backslashes the model emitted as raw backslashes (rare —
// you'd need Windows paths inside backticks) come out double-escaped.
// Acceptable for v1.
func repairBacktickStrings(s string) string {
	return backtickStringRegex.ReplaceAllStringFunc(s, func(match string) string {
		sub := backtickStringRegex.FindStringSubmatch(match)
		key := sub[1]
		body := sub[2]
		// Escape `"` so the wrapping double-quotes round-trip. Don't
		// touch backslashes — preserve the model's escape sequences.
		body = strings.ReplaceAll(body, `"`, `\"`)
		return fmt.Sprintf(`"%s": "%s"`, key, body)
	})
}

// salvageWriteFile applies a text-extracted write_file call to the
// workdir. Returns the relative path written (for FilesChanged) and
// any error. Refuses absolute paths, paths with "..", and paths that
// escape the workdir.
func salvageWriteFile(workdir string, args map[string]any) (string, error) {
	pathRaw, _ := args["path"].(string)
	content, _ := args["content"].(string)
	if pathRaw == "" {
		return "", fmt.Errorf("write_file: empty path")
	}
	if filepath.IsAbs(pathRaw) {
		return "", fmt.Errorf("write_file: absolute path %q forbidden", pathRaw)
	}
	clean := filepath.Clean(pathRaw)
	if strings.HasPrefix(clean, "..") || strings.Contains(clean, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("write_file: path %q escapes workdir", pathRaw)
	}
	dst := filepath.Join(workdir, clean)
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return "", fmt.Errorf("mkdir: %w", err)
	}
	if err := os.WriteFile(dst, []byte(content), 0o644); err != nil {
		return "", fmt.Errorf("write: %w", err)
	}
	return clean, nil
}

// salvageTextToolCall is the hook called from runHarness after a
// harness round. When the harness reported zero files changed AND the
// final text contains a tool-call shape we can execute, we apply it
// and return the synthesized FilesChanged list + a note for the JSONL
// row. Currently only supports write_file; other tools fall through
// to a no-op (and a verbose-mode warning).
//
// Returns (changed, note):
//   - changed: paths salvaged (to merge into hres.FilesChanged)
//   - note: human-readable description ("salvaged write_file:main.go from text content");
//     empty when nothing was salvaged
func (s *replState) salvageTextToolCall(hres evalv2.HarnessResult, lres harness.LoopResult) ([]string, string) {
	if len(hres.FilesChanged) > 0 {
		return nil, ""
	}
	call := extractToolCallFromText(lres.Final)
	if call == nil {
		return nil, ""
	}
	switch call.Name {
	case "write_file":
		path, err := salvageWriteFile(s.workdir, call.Args)
		if err != nil {
			if s.verbose {
				s.ui.Warn(fmt.Sprintf("(salvage write_file failed: %v)", err))
			}
			return nil, ""
		}
		note := fmt.Sprintf("salvaged write_file:%s from text content", path)
		if s.verbose {
			s.ui.Info("  " + note)
		}
		return []string{path}, note
	default:
		if s.verbose {
			s.ui.Warn(fmt.Sprintf("(text contained %s tool call; only write_file is salvaged in v1)", call.Name))
		}
		return nil, ""
	}
}

// ============================================================================
// In-flight observability — stream the agent loop into the REPL
// ============================================================================
//
// `cortex code` has had a live progress notifier since iteration 1 of the
// coding harness (see makeCodeNotifier in code.go); the REPL just didn't
// wire it up in the initial cuts. Without streaming, long turns
// (gpt-oss-20b: 20–30s; qwen-1.5b on a complex prompt: 30–60s) feel like
// a silent stare. With streaming the user sees a per-tool-call line as
// the agent works.
//
// Default mode (no --verbose) shows only tool calls with smart per-tool
// arg summaries. Verbose adds inner-turn telemetry + tool-result sizes
// + session-start banner + error stack.
//
// TODO (one source of truth, not two): the notifier (stdout side
// effect) and the DAG trace (.cortex/db/dag_traces.jsonl) emit the
// same events through parallel paths — every tool call is both a
// coding.tool_call notifier event AND a dag.TraceEntry. Pick one
// canonical source (the DAG trace, since it's structured + persisted)
// and make stdout streaming a subscriber that formats trace rows as
// they're written. Removes the duplication AND ensures stdout
// streaming and post-hoc analysis can never diverge.

// makeREPLNotifier returns the callback wired to the harness's Notify
// hook. The shape mirrors makeCodeNotifier in code.go but the visible
// surface is narrower — the REPL prints its own per-user-turn summary,
// so we don't echo the agent loop's internal turn/final events at
// default verbosity.
//
// Routes everything through the supplied Sink rather than fmt.Printf.
// That lets a TUI sink format the same events differently (color by
// function, group under the user's prompt line) without the REPL
// needing to know which presentation is in play.
func makeREPLNotifier(ui cliout.Sink, verbose bool) func(string, any) {
	return func(kind string, payload any) {
		switch kind {
		case "coding.tool_call":
			p := mapOf(payload)
			name, _ := p["name"].(string)
			argsStr, _ := p["args"].(string)
			summary := summarizeToolArgs(name, argsStr, verbose)
			ui.Info(fmt.Sprintf("  → %s%s", name, summary))
		case "coding.tool_result":
			if !verbose {
				return
			}
			p := mapOf(payload)
			ui.Info(fmt.Sprintf("    (result: %v chars)", p["output_chars"]))
		case "coding.turn":
			if !verbose {
				return
			}
			p := mapOf(payload)
			ui.Info(fmt.Sprintf("  · agent turn %v · finish=%v · tokens=%v/%v · calls=%v",
				p["turn"], p["finish_reason"],
				p["tokens_in"], p["tokens_out"],
				p["tool_calls"]))
		case "coding.session_start":
			if !verbose {
				return
			}
			p := mapOf(payload)
			ui.Info(fmt.Sprintf("  · session_start · max_turns=%v · num_tools=%v",
				p["max_turns"], p["num_tools"]))
		case "coding.turn_limit":
			ui.Info("  ⚠ agent turn limit hit")
		case "coding.budget_exceeded":
			p := mapOf(payload)
			ui.Info(fmt.Sprintf("  ⚠ budget exceeded · cumulative_tokens=%v/%v · cost=$%.4f",
				p["cumulative_tokens"], p["cap_tokens"], asFloat(p["cost_usd"])))
		case "coding.error":
			p := mapOf(payload)
			msg := fmt.Sprintf("%v", p["error"])
			ui.Info(fmt.Sprintf("  ⚠ provider error: %s", msg))
			// llama-server (chatterbox / LM Studio / vLLM running llama.cpp)
			// returns this exact phrasing when the prompt + tools blow past
			// n_ctx. The fix is server-side — bump n_ctx on the model launch
			// — so surface it explicitly instead of leaving the user to
			// decode the error.
			if strings.Contains(msg, "exceeds the available context size") {
				ui.Info("    hint: the local model was loaded with a small n_ctx; restart it with a larger context (e.g. --ctx-size 16384) on the server side.")
			}
		}
	}
}

// summarizeToolArgs produces a one-line arg snippet sized for the REPL.
// Smart per-tool summaries make the dominant tools (write_file,
// read_file, run_shell) self-explanatory at a glance; everything else
// falls through to a generic truncated dump. Verbose mode lifts the
// truncation cap so the human can see exactly what was called.
//
// Arg formats by tool:
//
//	write_file → "(main.go, 168 bytes)"
//	read_file  → "(main.go)"
//	run_shell  → "(go build ./...)"
//	default    → "(first-80-chars…)"
//
// The argsStr coming in is the harness's JSON-stringified arguments
// — same shape the tool dispatcher saw. We do a forgiving parse: if
// the JSON doesn't decode (rare), fall through to the generic path.
func summarizeToolArgs(name, argsStr string, verbose bool) string {
	max := 80
	if verbose {
		max = 240
	}
	var args map[string]any
	if err := json.Unmarshal([]byte(argsStr), &args); err == nil {
		switch name {
		case "write_file":
			path, _ := args["path"].(string)
			content, _ := args["content"].(string)
			if path != "" {
				return fmt.Sprintf("(%s, %d bytes)", path, len(content))
			}
		case "read_file", "list_dir":
			if path, ok := args["path"].(string); ok && path != "" {
				return fmt.Sprintf("(%s)", path)
			}
		case "run_shell":
			cmd, _ := args["command"].(string)
			if rest, ok := args["args"].([]any); ok && len(rest) > 0 {
				parts := make([]string, 0, len(rest))
				for _, r := range rest {
					if s, ok := r.(string); ok {
						parts = append(parts, s)
					}
				}
				full := cmd + " " + strings.Join(parts, " ")
				return "(" + truncateHead(full, max) + ")"
			}
			if cmd != "" {
				return "(" + truncateHead(cmd, max) + ")"
			}
		case "cortex_search":
			if q, ok := args["query"].(string); ok && q != "" {
				return fmt.Sprintf("(%q)", truncateHead(q, max))
			}
		}
	}
	return "(" + truncateHead(argsStr, max) + ")"
}

// truncate caps s at max chars and appends an ellipsis when truncated.
// Distinct from tailString (which keeps the tail) because for argument
// previews we care about the head.
func truncateHead(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "…"
}

// makeREPLDAGTracer returns a dag.TraceCallback that prints one line per
// completed node so the user sees the DAG emerge live in the REPL.
//
// Format per line: `  ▪ <function.op> [<NodeID>] · ok|err · <latency>`,
// followed by an arrow-prefixed list of spawned child IDs when this node
// grew the tree, or a `· cause: …` tail when it failed.
//
// The executor runs in sequential DFS-prepend mode (see runREPLChainTurn),
// so completion order IS the natural left-to-right tree walk — child
// lines appear immediately under their parent. No explicit indentation
// is needed; the structure is preserved by ordering alone.
//
// next, when non-nil, is invoked after the print so the existing
// dag_traces.jsonl writer still gets every entry.
func makeREPLDAGTracer(ui cliout.Sink, next dag.TraceCallback) dag.TraceCallback {
	return func(e dag.TraceEntry) {
		name := e.QualifiedName
		if name == "" {
			name = "?"
		}
		latency := e.WallEnd.Sub(e.WallStart)
		cause := ""
		if !e.OK {
			cause = e.ErrorMessage
			if cause == "" {
				cause = e.ErrorCode
			}
		}
		// Route as a structured Event so the TUI sink can color
		// the line by cortex function (sense/attend/decide/act/…)
		// instead of treating it as plain Info. StdoutSink's Event
		// renderer prints the same shape.
		ui.Event("dag.trace", map[string]any{
			"qualified_name": name,
			"node_id":        e.NodeID,
			"ok":             e.OK,
			"latency_ms":     int(latency / time.Millisecond),
			"spawned":        e.SpawnedChildren,
			"err_cause":      cause,
		})
		if next != nil {
			next(e)
		}
	}
}

// Compile-time guard: ensure REPLCommand satisfies the Command interface.
var _ Command = (*REPLCommand)(nil)
