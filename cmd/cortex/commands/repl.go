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
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
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
	evalv2 "github.com/dereksantos/cortex/internal/eval/v2"
	"github.com/dereksantos/cortex/internal/harness"
	"github.com/dereksantos/cortex/pkg/config"
	"github.com/dereksantos/cortex/pkg/events"
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
	// has no provider prefix (no slash → not OpenRouter).
	defaultOllamaAPIURL = "http://localhost:11434/v1/chat/completions"

	// defaultMaxOutputTokens caps a single turn's output. Small enough
	// to force the model to take one focused step at a time, large
	// enough to write a full file in one go (Game-of-Life-sized).
	defaultMaxOutputTokens = 4000

	// defaultMaxTurns caps the inner agent loop per user message. A
	// 1.5B model spinning on tool calls is expensive without ceiling.
	defaultMaxTurns = 8

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
		case "-h", "--help":
			printREPLHelp()
			return nil
		}
	}

	workdir, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("getwd: %w", err)
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
				fmt.Println(note)
			}
		} else {
			fmt.Println("cortex: warning — Ollama unreachable at " + apiURL + " (model calls will fail until it's started)")
		}
	}

	state, err := newREPLState(workdir, model, verbose)
	if err != nil {
		return err
	}
	defer state.close()

	printREPLBanner(state)

	scanner := bufio.NewScanner(os.Stdin)
	// Default Scanner buffer is 64 KiB; bump so paste-in prompts work.
	scanner.Buffer(make([]byte, 64*1024), 1<<20)

	for {
		fmt.Print("~ ")
		if !scanner.Scan() {
			break
		}
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "/") {
			cont, err := state.dispatchSlash(line)
			if err != nil {
				fmt.Fprintf(os.Stderr, "  error: %v\n", err)
			}
			if !cont {
				break
			}
			continue
		}
		if err := state.runTurn(line); err != nil {
			fmt.Fprintf(os.Stderr, "  turn error: %v\n", err)
		}
		if state.exitRequested {
			break
		}
	}
	fmt.Printf("\nsession saved → %s\n", state.sessionPath)
	return nil
}

// replState bundles per-session mutable state: model + workdir + the
// session JSONL writer + turn counter + last snapshot for /undo.
type replState struct {
	workdir string
	model   string
	apiURL  string
	verbose bool

	// systemPrompt is loaded from .cortex/repl-system-prompt.md (created
	// with a 1.5B-tuned default on first run). Held in memory so /reload
	// can pick up edits without restarting.
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

	// verifyWarned ensures we warn at most once per session when the
	// workdir has no recognized verifier.
	verifyWarned bool

	// captureCfg + captureClient are lazily constructed on first
	// accepted turn so the capture write doesn't pay setup cost when
	// the REPL is just used for read-only exploration (no edits, no
	// captures).
	captureCfg    *config.Config
	captureClient *capture.Capture

	// sessionID is a short random identifier shared across all turn
	// rows + capture events in a single REPL invocation. Lets analysis
	// group "everything done in one session" without parsing paths.
	sessionID string

	// exitRequested is set by the /quit gate-response path so runTurn
	// can signal Execute to break the loop cleanly.
	exitRequested bool
}

// newREPLState performs auto-init: creates .cortex/ if missing, the
// session dir, the JSONL writer, and seeds the system prompt file if
// absent. Returns an error if any of these fail — we'd rather refuse
// to start than run in an inconsistent state.
func newREPLState(workdir, model string, verbose bool) (*replState, error) {
	cortexDir := filepath.Join(workdir, ".cortex")
	if err := os.MkdirAll(cortexDir, 0o755); err != nil {
		return nil, fmt.Errorf("init .cortex/: %w", err)
	}

	promptPath := filepath.Join(cortexDir, "repl-system-prompt.md")
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

	return &replState{
		workdir:      workdir,
		model:        model,
		apiURL:       resolveAPIURL(model),
		verbose:      verbose,
		systemPrompt: systemPrompt,
		sessionDir:   sessionDir,
		sessionPath:  sessionPath,
		jsonl:        f,
		sessionID:    ts,
	}, nil
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
	s.captureClient = capture.New(s.captureCfg)
	return s.captureClient, nil
}

// close flushes and closes the session JSONL.
func (s *replState) close() {
	if s.jsonl != nil {
		_ = s.jsonl.Close()
	}
}

// resolveAPIURL routes to Ollama when the model id looks local (no
// provider prefix), to OpenRouter otherwise. We treat a slash as the
// "this is provider/model" signal — matches the convention `cortex code`
// uses (anthropic/foo, qwen/foo, openai/foo).
func resolveAPIURL(model string) string {
	if !strings.Contains(model, "/") {
		return defaultOllamaAPIURL
	}
	return "" // empty → harness uses OpenRouter default
}

// loadOrSeedSystemPrompt reads the per-workdir REPL system prompt or
// writes a default one. The default is deliberately short and biased
// toward single-step changes — small models do better when explicitly
// told not to attempt too much per turn.
func loadOrSeedSystemPrompt(path string) (string, error) {
	if b, err := os.ReadFile(path); err == nil {
		return string(b), nil
	}
	seed := defaultREPLSystemPrompt
	if err := os.WriteFile(path, []byte(seed), 0o644); err != nil {
		return "", fmt.Errorf("seed system prompt at %s: %w", path, err)
	}
	return seed, nil
}

// defaultREPLSystemPrompt is the seed content for
// .cortex/repl-system-prompt.md on a fresh workdir. Tuned for the
// REPL's 3-tool minimal registry (read_file + write_file + run_shell)
// — see CortexHarness.SetMinimalTools and the iter-5 coherence fix.
//
// Tuning lessons baked in:
//   - Iter-3: verbose meta-instructions about tool-call format
//     actively HURT small models — they parrot the prose instead of
//     acting. Keep it short.
//   - Iter-4: small models lose function-call discipline with ≥5
//     tools registered; the REPL drops to 3 tools when Ollama-routed.
//   - Iter-5 (this fix): the seeded system prompt previously listed
//     5 tools, but the harness only registers 3 in REPL mode. Telling
//     the model about tools that don't exist is the kind of small
//     coherence bug that pushes a borderline-competent 1.5B model
//     over the edge into text-only responses. Prompt now matches
//     the registered surface area exactly.
const defaultREPLSystemPrompt = `You are a Go programmer working inside a workdir you fully own.

You have these tools:
  - read_file(path): read a file
  - write_file(path, content): create or replace a file
  - run_shell(command, args): run go build, go test, go run, cat, head, tail, wc, diff, grep, test

Workflow: read_file the relevant file (if any), then write_file your implementation, then run_shell to build. Iterate on errors. When the requested step is done, respond with a short summary and NO further tool calls.

Make ONE focused change per user message. Edit one file unless the request explicitly spans files. Do not refactor adjacent code that wasn't asked for.

Rules:
  - Paths are relative to the workdir; no absolute paths, no "..".
  - Never write under .git or .cortex.
  - Use "go build" to verify your work — don't claim success without running it.
  - When a build fails, read the error, fix the code, and try again.
`

// printREPLBanner prints the welcome line. One line, no ASCII art.
func printREPLBanner(s *replState) {
	api := s.apiURL
	if api == "" {
		api = "openrouter (default)"
	}
	fmt.Printf("cortex · %s · %s · %s · /help\n", s.workdir, s.model, api)
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
  -h, --help           Show this help.

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
		printSlashHelp()
		return true, nil
	case "/quit", "/exit":
		return false, nil
	case "/model":
		if len(rest) == 0 {
			fmt.Printf("  current model: %s (api: %s)\n", s.model, displayAPI(s.apiURL))
			return true, nil
		}
		s.model = rest[0]
		s.apiURL = resolveAPIURL(s.model)
		fmt.Printf("  model → %s (api: %s)\n", s.model, displayAPI(s.apiURL))
		return true, nil
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

func printSlashHelp() {
	fmt.Println(`  /help              this message
  /diff              changed files since session start
  /undo              restore workdir to pre-last-turn snapshot
  /model [<id>]      show or swap model (slash in name = OpenRouter, no slash = Ollama)
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
func (s *replState) runTurn(userPrompt string) error {
	turnNum := s.turns + 1

	snapDir, err := s.snapshotWorkdir(turnNum)
	if err != nil {
		return fmt.Errorf("snapshot: %w", err)
	}

	row := turnRow{
		Turn:         turnNum,
		SessionID:    s.sessionID,
		Timestamp:    time.Now().UTC().Format(time.RFC3339),
		UserMessage:  userPrompt,
		Model:        s.model,
		APIURL:       s.apiURL,
		SystemPrompt: s.systemPrompt,
		SnapshotDir:  snapDir,
	}

	// Attempt 1: fresh model call with just the user prompt.
	hres, lres, runErr := s.runHarness(userPrompt, "")
	row.HarnessError = errString(runErr)
	row.AgentTurns = hres.AgentTurnsTotal
	row.TokensIn = hres.TokensIn
	row.TokensOut = hres.TokensOut
	row.CostUSD = hres.CostUSD
	row.LatencyMs = hres.LatencyMs
	row.FilesChanged = hres.FilesChanged
	row.FinalText = lres.Final

	verifyRes := s.runVerifier()
	row.VerifyKind = verifyRes.Kind
	row.VerifyOK = verifyRes.OK
	row.VerifyOutput = verifyRes.OutputTail

	// Attempt 2: automatic single retry on verify-fail with the
	// verifier output fed back into the prompt.
	if !verifyRes.OK && verifyRes.Kind != verifierNone {
		fmt.Printf("  verify failed (%s), auto-retrying once with error context...\n", verifyRes.Kind)
		hres2, lres2, runErr2 := s.runHarness(userPrompt, verifyRes.OutputTail)
		row.RetryAgentTurns = hres2.AgentTurnsTotal
		row.RetryTokensIn = hres2.TokensIn
		row.RetryTokensOut = hres2.TokensOut
		row.RetryHarnessError = errString(runErr2)
		row.RetryFinalText = lres2.Final
		row.FilesChanged = mergeFiles(row.FilesChanged, hres2.FilesChanged)

		verifyRes = s.runVerifier()
		row.RetryVerifyOK = verifyRes.OK
		row.RetryVerifyOutput = verifyRes.OutputTail
	}

	// User-driven retry/edit loop. Loops until verify passes, user
	// skips, or user quits. Bounded by userGateMaxAttempts so a stuck
	// model can't trap the REPL.
	userHints := []string{}
	attempts := 0
	for !verifyRes.OK && verifyRes.Kind != verifierNone {
		attempts++
		if attempts > userGateMaxAttempts {
			fmt.Printf("  reached %d user-retry attempts; rolling back\n", userGateMaxAttempts)
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
			hres3, lres3, runErr3 := s.runHarness(userPrompt, combined)
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
			fmt.Println("  edit any files in the workdir, then press enter to re-verify")
			_, _ = bufio.NewReader(os.Stdin).ReadString('\n')
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
// back from snapshot on reject. Always writes the row.
func (s *replState) finalize(row turnRow, snapDir string, accepted bool) error {
	row.Accepted = accepted
	if accepted {
		s.turns = row.Turn
		s.snapshotStack = append(s.snapshotStack, snapDir)
		printTurnSummary(row)
		if err := s.captureTurn(row); err != nil && s.verbose {
			fmt.Fprintf(os.Stderr, "  (capture failed: %v)\n", err)
		} else if s.verbose {
			fmt.Println("  (captured)")
		}
	} else {
		if err := s.restoreFromSnapshot(snapDir); err != nil {
			fmt.Fprintf(os.Stderr, "  WARN: rollback failed: %v\n", err)
		}
	}
	if err := s.writeJSONL(row); err != nil {
		fmt.Fprintf(os.Stderr, "  WARN: session log write failed: %v\n", err)
	}
	return nil
}

// runHarness wraps evalv2.CortexHarness.RunSessionWithResult. Returns
// zero values when the underlying construction fails (e.g. Ollama not
// reachable) — caller surfaces via HarnessError.
//
// retryContext, when non-empty, is appended to the user prompt as a
// "previous attempt failed with this build/test error" tail so the
// model has the failure in context on a retry.
func (s *replState) runHarness(userPrompt, retryContext string) (evalv2.HarnessResult, harness.LoopResult, error) {
	if err := ensureStubOpenRouterKey(s.apiURL); err != nil {
		return evalv2.HarnessResult{}, harness.LoopResult{}, err
	}
	h, err := evalv2.NewCortexHarness(s.model)
	if err != nil {
		return evalv2.HarnessResult{}, harness.LoopResult{}, fmt.Errorf("init harness: %w", err)
	}
	if s.systemPrompt != "" {
		h.SetSystemPrompt(s.systemPrompt)
	}
	if s.apiURL != "" {
		h.SetAPIURL(s.apiURL)
	}
	// Iter-4 fix: when routed to local Ollama, small models lose
	// function-call discipline with the default 5-tool registry. Drop
	// list_dir + cortex_search so the model sees a 3-tool surface
	// (read_file + write_file + run_shell). The probe matrix in
	// PROGRESS-REPL.md iter 3 showed qwen-1.5b function-calls cleanly
	// with ≤3 tools but emits text shapes at ≥5.
	if s.apiURL == defaultOllamaAPIURL {
		h.SetMinimalTools(true)
	}
	// Stream the agent loop into the REPL: one line per tool call so
	// the human sees what's happening during long turns (gpt-oss-20b
	// takes 20-30s per turn; without this it's a silent stare). Token
	// + per-turn telemetry is gated behind --verbose.
	h.SetNotify(makeREPLNotifier(s.verbose))
	h.SetMaxTurns(defaultMaxTurns)
	h.SetMaxOutputTokens(defaultMaxOutputTokens)

	prompt := userPrompt
	if retryContext != "" {
		prompt = userPrompt + "\n\nPREVIOUS ATTEMPT FAILED. Verifier output:\n" + retryContext + "\n\nFix this, then stop."
	}

	hr, runErr := h.RunSessionWithResult(context.Background(), prompt, s.workdir)
	lr := h.LastLoopResult()

	// Fix B: if the model emitted a tool call as fenced JSON in the
	// response text instead of via the OpenAI tool_calls field, salvage
	// it. Currently only handles write_file (the dominant case) — see
	// salvageTextToolCall for the rationale and scope.
	if salvaged, _ := s.salvageTextToolCall(hr, lr); len(salvaged) > 0 {
		hr.FilesChanged = mergeFiles(hr.FilesChanged, salvaged)
	}
	return hr, lr, runErr
}

// ensureStubOpenRouterKey lets a local-Ollama REPL run even when no
// OpenRouter key is configured. The harness's NewCortexHarness
// constructor mandates a key via pkg/secret; for local-only use the
// key is never sent (Ollama ignores Authorization). Stub-only injected
// when we're definitely pointed at a local URL.
func ensureStubOpenRouterKey(apiURL string) error {
	if apiURL != defaultOllamaAPIURL {
		return nil
	}
	if os.Getenv("OPEN_ROUTER_API_KEY") != "" {
		return nil
	}
	if err := os.Setenv("OPEN_ROUTER_API_KEY", "ollama-local-stub"); err != nil {
		return fmt.Errorf("stub key set: %w", err)
	}
	return nil
}

// verifier kinds — small enum, easy to extend.
const (
	verifierNone    = "none"
	verifierGoBuild = "go build"
)

type verifyResult struct {
	Kind       string // "go build", "none", ...
	OK         bool
	OutputTail string
}

// runVerifier picks a verifier based on workdir contents. v1 supports
// go.mod → `go build ./...`. Anything else returns a no-op result with
// a one-time warning.
func (s *replState) runVerifier() verifyResult {
	if _, err := os.Stat(filepath.Join(s.workdir, "go.mod")); err == nil {
		return s.runGoBuild()
	}
	if !s.verifyWarned {
		fmt.Println("  (no verifier: workdir has no go.mod; v1 only auto-verifies Go projects)")
		s.verifyWarned = true
	}
	return verifyResult{Kind: verifierNone, OK: true}
}

// runGoBuild shells out to `go build ./...`. We cap wall time at 60s
// to avoid wedging the REPL if a build hangs.
//
// Subtle bug guarded against: `go build ./...` exits 0 with a warning
// when there are no Go files to build (e.g. workdir has only a go.mod).
// Treating that as "ok" hides the failure mode where the model didn't
// write any code at all. We catch the "matched no packages" pattern
// and downgrade to no-verify so the turn doesn't accept silently.
func (s *replState) runGoBuild() verifyResult {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "go", "build", "./...")
	cmd.Dir = s.workdir
	out, err := cmd.CombinedOutput()
	output := string(out)
	if strings.Contains(output, "matched no packages") {
		// No Go files yet — treat as no-verify so a turn that wrote
		// nothing doesn't get a false accept. The verifier kind stays
		// "go build" so the JSONL row reflects what we tried.
		return verifyResult{Kind: verifierGoBuild, OK: false, OutputTail: tailString(output, 4096)}
	}
	return verifyResult{Kind: verifierGoBuild, OK: err == nil, OutputTail: tailString(output, 4096)}
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
	fmt.Printf("  verify still failing (%s).\n  [r]etry / [e]dit / [s]kip / [q]uit: ", v.Kind)
	reader := bufio.NewReader(os.Stdin)
	resp, err := reader.ReadString('\n')
	if err != nil {
		return gateDecision{kind: gateSkip}
	}
	switch strings.TrimSpace(strings.ToLower(resp)) {
	case "r", "retry":
		fmt.Print("  hint for the model (enter for none): ")
		hint, _ := reader.ReadString('\n')
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

// printTurnSummary emits one human-readable line per accepted turn.
// Derives the verify summary from the row's terminal verify status
// (covers all three rounds: initial, auto-retry, user-driven).
func printTurnSummary(row turnRow) {
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
	fmt.Printf("  ✓ turn %d · files: %s · verify: %s · tokens: %d/%d · %dms\n",
		row.Turn, files, verify, row.TokensIn, row.TokensOut, row.LatencyMs)
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
type turnRow struct {
	Turn         int    `json:"turn"`
	SessionID    string `json:"session_id"`
	Timestamp    string `json:"timestamp"`
	UserMessage  string `json:"user_message"`
	Model        string `json:"model"`
	APIURL       string `json:"api_url,omitempty"`
	SystemPrompt string `json:"system_prompt"`
	SnapshotDir  string `json:"snapshot_dir"`

	// Initial attempt (no error context).
	HarnessError string   `json:"harness_error,omitempty"`
	AgentTurns   int      `json:"agent_turns"`
	TokensIn     int      `json:"tokens_in"`
	TokensOut    int      `json:"tokens_out"`
	CostUSD      float64  `json:"cost_usd"`
	LatencyMs    int64    `json:"latency_ms"`
	FilesChanged []string `json:"files_changed,omitempty"`
	FinalText    string   `json:"final_text,omitempty"`
	VerifyKind   string   `json:"verify_kind"`
	VerifyOK     bool     `json:"verify_ok"`
	VerifyOutput string   `json:"verify_output,omitempty"`

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
	fmt.Printf("  undone turn %d (%d more available)\n", s.turns, n-1)
	s.turns--
	s.snapshotStack = s.snapshotStack[:n-1]
	return nil
}

// printDiff lists files that differ between the most recent pre-turn
// snapshot and the current workdir. Best-effort — for v1 we list paths
// only, not unified diffs.
func (s *replState) printDiff() {
	n := len(s.snapshotStack)
	if n == 0 {
		fmt.Println("  no accepted turns yet")
		return
	}
	top := s.snapshotStack[n-1]
	mb, err := os.ReadFile(filepath.Join(top, ".manifest.json"))
	if err != nil {
		fmt.Printf("  diff unavailable (manifest read: %v)\n", err)
		return
	}
	var manifest map[string]string
	if err := json.Unmarshal(mb, &manifest); err != nil {
		fmt.Printf("  diff unavailable (manifest parse: %v)\n", err)
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
		fmt.Println("  no changes since pre-last-turn snapshot")
		return
	}
	fmt.Printf("  changes since pre-last-turn snapshot (turn %d):\n", s.turns)
	for _, p := range added {
		fmt.Printf("    + %s\n", p)
	}
	for _, p := range changed {
		fmt.Printf("    ~ %s\n", p)
	}
	for _, p := range removed {
		fmt.Printf("    - %s\n", p)
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
	tagsURL := ollamaTagsURL(chatAPI)
	if tagsURL == "" {
		return fallback, true, ""
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, tagsURL, nil)
	if err != nil {
		return fallback, true, ""
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fallback, false, ""
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fallback, false, ""
	}
	var tags ollamaTagsResponse
	if err := json.NewDecoder(resp.Body).Decode(&tags); err != nil {
		return fallback, true, ""
	}
	installed := make([]string, 0, len(tags.Models))
	for _, m := range tags.Models {
		installed = append(installed, m.Name)
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
// Scoring rubric (tuned against the iter-2 probe matrix in
// PROGRESS-REPL.md — qwen-1.5b loses discipline with 5 tools, mistral:7b
// handles it cleanly, gemma2 and qwen:0.5b can't tool-call at all):
//
//	+30 if model family is in the "known good function-callers" list
//	     (qwen2.5-coder ≥3b, llama3.1/3.2, mistral-nemo, granite-code,
//	      command-r, mistral 7b+)
//	+10 if the name contains "coder" or "instruct"
//	+size-bucket-bonus by parameter count parsed from the name suffix
//	−50 if the model family is known-broken for our tool registry
//	     (gemma2, qwen2.5:0.5b, qwen2:0.5b, tinyllama, smollm)
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

// scoreOllamaModel applies the rubric in pickBestOllamaModel.
func scoreOllamaModel(name string) int {
	lower := strings.ToLower(name)
	score := 0

	knownGood := []string{
		"qwen2.5-coder:3", "qwen2.5-coder:7", "qwen2.5-coder:14", "qwen2.5-coder:32",
		"qwen3-coder",
		"llama3.1", "llama3.2", "llama3.3",
		"mistral-nemo", "mistral:7", "mistral:8",
		"granite-code", "granite3",
		"command-r",
	}
	for _, g := range knownGood {
		if strings.Contains(lower, g) {
			score += 30
			break
		}
	}

	knownBad := []string{
		"gemma2:", "gemma:",
		"qwen2.5:0.5", "qwen2:0.5", "qwen:0.5",
		"tinyllama", "smollm",
		"nomic-embed",
		"phi3:mini", // doesn't support tools in Ollama at all
	}
	for _, b := range knownBad {
		if strings.Contains(lower, b) {
			score -= 50
			break
		}
	}

	if strings.Contains(lower, "coder") || strings.Contains(lower, "instruct") {
		score += 10
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
				fmt.Fprintf(os.Stderr, "  (salvage write_file failed: %v)\n", err)
			}
			return nil, ""
		}
		note := fmt.Sprintf("salvaged write_file:%s from text content", path)
		if s.verbose {
			fmt.Println("  " + note)
		}
		return []string{path}, note
	default:
		if s.verbose {
			fmt.Fprintf(os.Stderr, "  (text contained %s tool call; only write_file is salvaged in v1)\n", call.Name)
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

// makeREPLNotifier returns the callback wired to the harness's Notify
// hook. The shape mirrors makeCodeNotifier in code.go but the visible
// surface is narrower — the REPL prints its own per-user-turn summary,
// so we don't echo the agent loop's internal turn/final events at
// default verbosity.
func makeREPLNotifier(verbose bool) func(string, any) {
	return func(kind string, payload any) {
		switch kind {
		case "coding.tool_call":
			p := mapOf(payload)
			name, _ := p["name"].(string)
			argsStr, _ := p["args"].(string)
			summary := summarizeToolArgs(name, argsStr, verbose)
			fmt.Printf("  → %s%s\n", name, summary)
		case "coding.tool_result":
			if !verbose {
				return
			}
			p := mapOf(payload)
			fmt.Printf("    (result: %v chars)\n", p["output_chars"])
		case "coding.turn":
			if !verbose {
				return
			}
			p := mapOf(payload)
			fmt.Printf("  · agent turn %v · finish=%v · tokens=%v/%v · calls=%v\n",
				p["turn"], p["finish_reason"],
				p["tokens_in"], p["tokens_out"],
				p["tool_calls"])
		case "coding.session_start":
			if !verbose {
				return
			}
			p := mapOf(payload)
			fmt.Printf("  · session_start · max_turns=%v · num_tools=%v\n",
				p["max_turns"], p["num_tools"])
		case "coding.turn_limit":
			fmt.Printf("  ⚠ agent turn limit hit\n")
		case "coding.budget_exceeded":
			p := mapOf(payload)
			fmt.Printf("  ⚠ budget exceeded · cumulative_tokens=%v/%v · cost=$%.4f\n",
				p["cumulative_tokens"], p["cap_tokens"], asFloat(p["cost_usd"]))
		case "coding.error":
			p := mapOf(payload)
			fmt.Printf("  ⚠ provider error: %v\n", p["error"])
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

// Compile-time guard: ensure REPLCommand satisfies the Command interface.
var _ Command = (*REPLCommand)(nil)
