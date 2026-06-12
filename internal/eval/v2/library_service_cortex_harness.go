//go:build !windows

package eval

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	intcognition "github.com/dereksantos/cortex/internal/cognition"
	"github.com/dereksantos/cortex/internal/harness"
	"github.com/dereksantos/cortex/internal/study"
	"github.com/dereksantos/cortex/pkg/llm"
	"github.com/dereksantos/cortex/pkg/secret"
)

// CortexHarness is Cortex's own coding harness, implementing the
// ResultfulHarness contract so the eval grid + coding_runner can use
// it interchangeably with Aider/OpenCode/pi.dev/Claude CLI.
//
// Unlike the other harnesses, this one does NOT shell out to a third-
// party CLI. It hosts the agent loop in-process (internal/harness),
// drives an OpenRouter model directly, and gives the model access to
// Cortex's own search via the `cortex_search` tool.
//
// Construction is cheap (no network calls). Provisioning the per-loop
// OpenRouter client and tool registry happens on every RunSession so
// the harness is reusable across cells without stateful contamination.
type CortexHarness struct {
	model  string
	apiKey string
	keySrc string

	// system is the agent's standing instructions. Empty -> default.
	// Override-able so a scenario-specific system prompt can be wired
	// in by the runner later (iteration 2).
	system string

	// maxTurns and budget cap the loop. 0 -> defaults defined in
	// internal/harness.
	maxTurns int
	budget   harness.Budget

	// lastLoop holds the raw LoopResult from the most recent
	// RunSession* call. The coding runner reads it back via
	// LastLoopResult() to populate CellResult fields the generic
	// HarnessResult doesn't carry (InjectedContextTokens,
	// CorrectionTurns, terminal reason). Single-session-at-a-time
	// usage makes the unguarded field safe.
	lastLoop harness.LoopResult

	// notify is forwarded to the Loop's Notify hook. Used by
	// interactive callers (`cortex code`) to stream progress.
	notify func(kind string, payload any)

	// maxOutputTokens is the per-turn output cap passed to the
	// OpenRouter client. 0 -> derive from the model id via
	// harness.ModelMaxOutputTokens.
	maxOutputTokens int

	// apiURL overrides the chat-completions endpoint. Empty means
	// the OpenRouter default. Used to point at a local Ollama
	// instance (`http://localhost:11434/v1/chat/completions`) or
	// any other OpenAI-compatible server.
	apiURL string

	// endpoint, when non-nil, takes precedence over apiURL/apiKey:
	// the harness constructs an OpenAICompatClient bound to this
	// endpoint instead of an OpenRouter client. This is the Phase 4
	// model-registry hook that lets the REPL route "chatterbox/..."
	// model ids to a local Lemonade / LM Studio / vLLM server
	// without going through OpenRouter's cloud gateway.
	endpoint *llm.EndpointConfig

	// disableCortexSearch omits the cortex_search tool from the
	// per-session registry. Used by benchmarks that need a baseline
	// run with no Cortex augmentation (SWE-bench --strategy baseline).
	disableCortexSearch bool

	// minimalTools restricts the per-session registry to write_file +
	// read_file + run_shell only. Used by the `cortex` REPL when
	// driving small local models (qwen2.5-coder:1.5b et al.) whose
	// tool-call discipline degrades with 5 tools registered. Setting
	// this drops list_dir and cortex_search from the registry so the
	// model sees a 3-tool surface area it can reliably function-call
	// against. See PROGRESS-REPL.md iter 3/4 for the probe matrix.
	minimalTools bool

	// noTools — when true, RunSessionWithResult builds an empty tool
	// registry. The LLM sees zero callable tools, so it can only
	// produce a textual response. Used by decide.coding_turn's
	// synthesize mode to force the model into "answer-or-NEED_MORE"
	// shape — without this, the synthesizer can quietly agent-loop
	// instead of emitting NEED_MORE for an emergent DAG hop, which
	// defeats the seed+grow architecture (see memory:
	// project-multi-hop-via-spawn).
	noTools bool

	// sharedCortex is plumbed through to the cortex_search tool via
	// NewCortexSearchToolFromCortex when non-nil. This is what lets the
	// REPL's auto-capture path become searchable in-session: the
	// captureClient writes to the same Storage that the cortex_search
	// tool's Cortex reads from, with both pointing at the same
	// in-memory indexes. Nil here keeps the legacy
	// self-construct-per-call behavior for benchmark cells that don't
	// capture anything.
	sharedCortex *intcognition.Cortex

	// dispatcher, when non-nil, is forwarded to the constructed
	// harness.Loop's Dispatcher field. Replaces inline tool dispatch
	// per call. Set by Stage 3 callers (coding_turn) to route tool
	// calls through the DAG executor as act.* nodes. nil → V0
	// inline dispatch via the ToolRegistry.
	dispatcher harness.ToolDispatcher

	// priorMessages is forwarded to the constructed harness.Loop's
	// PriorMessages field. Used by the REPL to inject conversation
	// history from earlier accepted turns so the model has working
	// memory beyond what cortex_search surfaces.
	priorMessages []llm.ChatMessage

	// accumulatorSnapshot, when non-nil, is forwarded to the
	// constructed harness.Loop's AccumulatorSnapshot field. Wired by
	// decide.coding_turn (when the dispatcher folds tool outputs
	// through attend.accumulate) so the inner agent loop's per-turn
	// input is bounded by a snapshot rather than the linear sum of
	// tool outputs. See harness.Loop.AccumulatorSnapshot doc.
	accumulatorSnapshot func(context.Context) string

	// keepRecentTurns is forwarded to Loop.KeepRecentTurns. 0 lets
	// the loop apply its own default (1).
	keepRecentTurns int

	// intent is the classified session intent (code / review / recall /
	// etc.) forwarded to Loop.Intent. Gates the loop's no_progress
	// heuristic: read-only windows are normal for explanatory
	// intents and shouldn't trigger ReasonNoProgress, but they remain
	// the dominant pathology signal for code-intended sessions.
	intent string
}

// SetDispatcher overrides the per-tool dispatcher for subsequent
// RunSession* calls on this harness. Pass nil to restore the V0
// inline-dispatch behavior. Used by Stage 3 coding_turn handler to
// route every tool call through the DAG executor (act.* ops + per-tool
// trace rows).
func (h *CortexHarness) SetDispatcher(d harness.ToolDispatcher) { h.dispatcher = d }

// SetNoTools toggles the synthesizer-mode zero-tool path. When true,
// RunSessionWithResult constructs an empty tool registry — the LLM
// sees no callable tools and must answer in prose (or, with the
// synthesizer directive in place, emit NEED_MORE: for the next hop).
// Used by decide.coding_turn when attrs.synthesize=true to enforce
// the "synthesize from prior context, don't intra-loop tool-call"
// contract at the protocol level rather than relying on prompt-only
// instruction. Pass false (default) to restore the full tool surface.
func (h *CortexHarness) SetNoTools(b bool) { h.noTools = b }

// NewCortexHarness returns a configured harness. The OpenRouter API
// key is resolved opportunistically (keychain first, env fallback) —
// a missing key is NOT an error here. Auth is a property of the
// backend chosen at RunSession time: configured endpoints (SetEndpoint)
// use their own auth (often none); the OpenRouter cloud path errors
// at the actual call if no key is available. This lets users run
// against a local Lemonade / LM Studio / Ollama server without ever
// having an OpenRouter key configured.
//
// model is required and is sent verbatim to whichever backend the
// session routes to. Callers using SetEndpoint typically pass the
// post-prefix bare model id ("Qwen3-Coder-30B-A3B-Instruct-GGUF");
// callers using OpenRouter pass the provider-prefixed id
// ("anthropic/claude-3-5-haiku").
func NewCortexHarness(model string) (*CortexHarness, error) {
	if strings.TrimSpace(model) == "" {
		return nil, fmt.Errorf("cortex harness: model is required")
	}
	// Best-effort key resolution. Empty key is fine — the OpenRouter
	// branch in RunSessionWithResult will surface a clear error if
	// the call actually goes out without one.
	key, src, _ := secret.MustOpenRouterKey()
	return &CortexHarness{
		model:  model,
		apiKey: key,
		keySrc: src,
	}, nil
}

// SetModel mirrors the AiderHarness contract: the grid runner can
// re-point one harness across model cells without re-resolving the
// API key.
func (h *CortexHarness) SetModel(model string) { h.model = model }

// SetSystemPrompt overrides the default agent system prompt. Useful
// for scenario-specific framing (e.g. "you are implementing a CLI").
func (h *CortexHarness) SetSystemPrompt(s string) { h.system = s }

// SetMaxTurns overrides the loop's hard turn cap. 0 reverts to the
// internal/harness default.
func (h *CortexHarness) SetMaxTurns(n int) { h.maxTurns = n }

// SetBudget overrides the loop's token + cost cap.
func (h *CortexHarness) SetBudget(b harness.Budget) { h.budget = b }

// SetNotify wires a live progress callback into every subsequent
// RunSession* call. Pass nil to clear.
func (h *CortexHarness) SetNotify(f func(kind string, payload any)) { h.notify = f }

// SetMaxOutputTokens overrides the per-turn output cap. 0 reverts
// to the harness.ModelMaxOutputTokens default for the current model.
func (h *CortexHarness) SetMaxOutputTokens(n int) { h.maxOutputTokens = n }

// SetAPIURL overrides the chat-completions endpoint. Useful for
// pointing at a local Ollama instance
// (`http://localhost:11434/v1/chat/completions`) or any other
// OpenAI-compatible server. Empty -> default OpenRouter endpoint.
func (h *CortexHarness) SetAPIURL(url string) { h.apiURL = url }

// SetEndpoint binds the harness to a specific OpenAI-compatible
// endpoint, bypassing OpenRouter. When set, subsequent RunSession*
// calls construct an OpenAICompatClient (with this endpoint's BaseURL
// + optional APIKey) instead of an OpenRouter client. The model id
// passed to SetModel is sent verbatim — no provider-prefix mangling.
//
// Pass nil to clear and revert to OpenRouter-backed behavior. This
// is the Phase 4 model-registry hook used by the REPL when the
// configured endpoint resolves a model id (e.g. "chatterbox/...").
func (h *CortexHarness) SetEndpoint(ep *llm.EndpointConfig) { h.endpoint = ep }

// SetIntent records the per-turn classified intent ("code", "review",
// "recall", "meta", etc.). Forwarded to Loop.Intent which gates the
// no_progress heuristic — read-only windows are expected for
// explanation intents and shouldn't trigger ReasonNoProgress, but
// they remain the dominant pathology signal for code intent. Empty
// preserves pre-intent loop behavior (no_progress fires regardless).
func (h *CortexHarness) SetIntent(intent string) { h.intent = intent }

// SetCortexSearchEnabled toggles registration of the cortex_search
// tool in subsequent RunSession* calls. Defaults to enabled. Pass
// false to run a baseline-style cell with no Cortex augmentation.
func (h *CortexHarness) SetCortexSearchEnabled(enabled bool) { h.disableCortexSearch = !enabled }

// SetMinimalTools restricts the per-session registry to read_file +
// write_file + run_shell when enabled. Used by the `cortex` REPL when
// driving small local models whose function-call discipline degrades
// at the default 5-tool surface area. When true, this overrides
// SetCortexSearchEnabled (cortex_search is dropped regardless).
func (h *CortexHarness) SetMinimalTools(enabled bool) { h.minimalTools = enabled }

// SetSharedCortex wires a caller-built Cortex into the harness so the
// cortex_search tool registered for the next RunSession uses it. See
// the sharedCortex field for the reasoning. nil clears the wiring.
func (h *CortexHarness) SetSharedCortex(cx *intcognition.Cortex) { h.sharedCortex = cx }

// SetPriorMessages sets the conversation-history block injected
// between the system prompt and the current user message. Pass nil
// or empty to clear. The REPL uses this to give multi-turn sessions
// working memory of prior accepted turns (user prompt + assistant
// final text only — no tool-call traces).
func (h *CortexHarness) SetPriorMessages(m []llm.ChatMessage) { h.priorMessages = m }

// SetAccumulatorSnapshot wires a working-memory provider into the
// inner agent loop. Subsequent RunSession* calls forward the
// callback to Loop.AccumulatorSnapshot; the loop invokes it before
// each provider call after turn 0 and rewrites msgs so per-turn
// input plateaus at (system + user + snapshot + last K turns)
// instead of growing with tool-call count.
//
// keepRecentTurns < 1 lets the loop apply its default (1). Pass nil
// to clear the wiring (reverts to history-grows-linearly behavior).
func (h *CortexHarness) SetAccumulatorSnapshot(fn func(context.Context) string, keepRecentTurns int) {
	h.accumulatorSnapshot = fn
	h.keepRecentTurns = keepRecentTurns
}

// defaultSystemPrompt is the fallback agent contract used only when a
// caller doesn't override via SetSystemPrompt (the REPL always
// overrides; benchmark callers may not). Deliberately concise —
// small models ignore long system prompts and the tool descriptions
// already document mechanics. Language-agnostic since the harness is
// general-purpose.
const defaultSystemPrompt = `You are a capable assistant working in a workdir you fully own. Code, conversation, and analysis are all in scope.

You have these tools:
  - list_dir(path): see what files exist
  - read_file(path): read a file
  - write_file(path, content): create or replace a file
  - run_shell(command, args): run a build/test/inspect command (allowlisted)
  - cortex_search(query): search prior captures (returns "empty" on a fresh run)

When the user asks about THIS workdir, ground yourself by reading actual files before answering. Don't infer project shape from the workdir name. Don't claim you read a file you didn't read.

Workflow: explore with list_dir/read_file when needed, write_file changes, run_shell to verify (the right build/test command for whatever this project uses). Iterate on errors. When the requested step is done, respond with a short summary and NO further tool calls.

Rules:
  - Paths are relative to the workdir; no absolute paths, no "..".
  - Never write under .git or .cortex.`

// RunSession is the Harness-interface entry point. Discards the
// result; callers wanting telemetry use RunSessionWithResult.
func (h *CortexHarness) RunSession(ctx context.Context, prompt, workdir string) error {
	_, err := h.RunSessionWithResult(ctx, prompt, workdir)
	return err
}

// RunSessionWithResult drives one session: builds a fresh OpenRouter
// client + tool registry + transcript, runs the loop, maps the result
// onto a HarnessResult.
//
// workdir must be absolute. The cortex_search tool opens its store at
// <workdir>/.cortex; the runner is responsible for whatever state
// lives there before this call (empty for Mode A; carried across
// attempts for Mode B).
//
// The transcript lands at <workdir>/.cortex/journal/coding/<runID>.jsonl.
// Errors from the transcript layer are swallowed inside the loop; a
// transcript-write failure doesn't fail the session.
func (h *CortexHarness) RunSessionWithResult(ctx context.Context, prompt, workdir string) (HarnessResult, error) {
	if strings.TrimSpace(prompt) == "" {
		return HarnessResult{}, fmt.Errorf("cortex harness: prompt is required")
	}

	// Per-turn output cap: derive from the model id unless the
	// caller overrode it via SetMaxOutputTokens. The cap bounds a
	// single response, not cumulative spend (that's Budget).
	maxOut := h.maxOutputTokens
	if maxOut <= 0 {
		maxOut = harness.ModelMaxOutputTokens(h.model)
	}

	// Provider construction: Phase 4 endpoint-aware routing. When
	// SetEndpoint pinned a specific OpenAI-compatible server (e.g.
	// chatterbox / LM Studio), use the generic OpenAICompatClient;
	// otherwise fall back to the OpenRouter cloud path. The harness
	// loop needs LoopProvider (GenerateWithTools); the cortex_search
	// tool needs llm.Provider (Generate/GenerateWithSystem). Both
	// client types satisfy both interfaces, so we hold one variable
	// per role.
	var (
		loopProvider harness.LoopProvider
		chatProvider llm.Provider
	)
	if h.endpoint != nil {
		compat := llm.NewOpenAICompatClient(*h.endpoint)
		compat.SetModel(h.model)
		compat.SetMaxTokens(maxOut)
		loopProvider = compat
		chatProvider = compat
	} else {
		// Fresh OpenRouter client per session: avoids leaking
		// LastCostUSD / LastProvider from a prior cell.
		client := llm.NewOpenRouterClientWithKey(nil, h.apiKey)
		client.SetModel(h.model)
		if h.apiURL != "" {
			client.SetAPIURL(h.apiURL)
		}
		client.SetMaxTokens(maxOut)
		loopProvider = client
		chatProvider = client
	}

	registry := harness.NewToolRegistry()
	// noTools short-circuits all registration. The loop still requires
	// a non-nil registry (it tracks per-call accounting through it
	// even when no tools fire), but with zero specs registered the
	// LLM sees no callable tools and is forced into a prose-only
	// response — which is exactly what synthesize-mode wants.
	if !h.noTools {
		// study_file subsumes read_file: a file that fits the model's
		// window reads byte-identically; a file over the threshold is
		// sampled (density-bound) instead of ingested whole — the fix for
		// the large-repo cells that timed out under read+accumulate. Gated
		// behind CORTEX_STUDY_FILE while it proves out so the committed
		// baseline (which keys read_count on act.read_file) is untouched.
		if os.Getenv("CORTEX_STUDY_FILE") == "1" {
			studyOpts := harness.StudyFileToolOpts{
				Provider:   chatProvider,
				ContextDir: filepath.Join(workdir, ".cortex"),
				ModelID:    h.model,
			}
			if h.endpoint != nil {
				studyOpts.Endpoint = h.endpoint.Name
			}
			registry.Register(harness.NewStudyFileTool(workdir, studyOpts))
			// Models call read_file from habit regardless of the
			// advertised specs (measured: every recovered call in the
			// gated probe named read_file). Dispatch-only alias so the
			// habit lands on study instead of "unknown tool".
			registry.RegisterAlias("read_file", "study_file")
		} else {
			registry.Register(harness.NewReadFileTool(workdir))
		}
		registry.Register(harness.NewWriteFileTool(workdir, registry))
		if !h.minimalTools {
			registry.Register(harness.NewListDirTool(workdir))
		}
		registry.Register(harness.NewRunShellTool(workdir, registry))
		if !h.minimalTools && !h.disableCortexSearch {
			var (
				cortexSearch harness.ToolHandler
				err          error
			)
			if h.sharedCortex != nil {
				cortexSearch, err = harness.NewCortexSearchToolFromCortex(workdir, h.sharedCortex, chatProvider)
			} else {
				cortexSearch, err = harness.NewCortexSearchTool(workdir, chatProvider)
			}
			if err != nil {
				return HarnessResult{}, fmt.Errorf("cortex_search tool: %w", err)
			}
			registry.Register(cortexSearch)
		}
	}

	runID := newCodingRunID()
	transcript, err := harness.NewTranscript(workdir, runID)
	if err != nil {
		return HarnessResult{}, fmt.Errorf("transcript: %w", err)
	}
	defer transcript.Close()

	sys := h.system
	if sys == "" {
		sys = defaultSystemPrompt
	}

	// n_ctx from the probe cache (if any). Cheap read; the harness
	// has its own catch-and-retry safety net for cache misses, so
	// this is best-effort. Endpoint key matches what
	// internal/study.Probe uses on write.
	var ctxWindow int
	if h.endpoint != nil {
		if p, ok := study.LookupCached(filepath.Join(workdir, ".cortex"), h.model, h.endpoint.Name); ok {
			ctxWindow = p.CtxWindowTokens
		}
	}

	loop := &harness.Loop{
		Provider:            loopProvider,
		Registry:            registry,
		System:              sys,
		MaxTurns:            h.maxTurns,
		Budget:              h.budget,
		Transcript:          transcript,
		Notify:              h.notify,
		Dispatcher:          h.dispatcher,
		PriorMessages:       h.priorMessages,
		ContextWindowTokens: ctxWindow,
		AccumulatorSnapshot: h.accumulatorSnapshot,
		KeepRecentTurns:     h.keepRecentTurns,
		Intent:              h.intent,
	}

	start := time.Now()
	res, runErr := loop.Run(ctx, prompt)
	elapsed := time.Since(start).Milliseconds()

	h.lastLoop = res

	hr := HarnessResult{
		TokensIn:        res.TokensIn,
		TokensOut:       res.TokensOut,
		CostUSD:         res.CostUSD,
		AgentTurnsTotal: res.Turns,
		FilesChanged:    res.FilesWritten,
		LatencyMs:       elapsed,
		ProviderEcho:    "openrouter",
		ModelEcho:       h.model,
	}
	return hr, runErr
}

// LastLoopResult returns the raw LoopResult from the most recent
// RunSession* call. Used by the coding runner to populate CellResult
// fields HarnessResult doesn't carry: InjectedContextTokens,
// CorrectionTurns (ShellNonZeroExits), and the terminal reason.
//
// Returns the zero value before any session has run.
func (h *CortexHarness) LastLoopResult() harness.LoopResult { return h.lastLoop }

// newCodingRunID returns a short, sortable, uniquely-suffixed run id
// suitable for naming transcript files. Format:
// <YYYYMMDDTHHMMSSZ>-<8hex>. Time-prefixed so listing the directory
// sorts chronologically.
func newCodingRunID() string {
	ts := time.Now().UTC().Format("20060102T150405Z")
	var b [4]byte
	_, _ = rand.Read(b[:])
	return ts + "-" + hex.EncodeToString(b[:])
}
