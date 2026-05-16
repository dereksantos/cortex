//go:build !windows

package eval

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	"github.com/dereksantos/cortex/internal/harness"
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
}

// NewCortexHarness resolves the OpenRouter API key (keychain first, env
// fallback via pkg/secret) and returns a configured harness. A missing
// key is a hard error — the harness cannot run without it.
//
// model is required and is passed verbatim to OpenRouter (e.g.
// "anthropic/claude-3-5-haiku", "qwen/qwen-2.5-coder-32b-instruct").
func NewCortexHarness(model string) (*CortexHarness, error) {
	if strings.TrimSpace(model) == "" {
		return nil, fmt.Errorf("cortex harness: model is required")
	}
	key, src, err := secret.MustOpenRouterKey()
	if err != nil {
		return nil, fmt.Errorf("cortex harness: %w", err)
	}
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

// defaultSystemPrompt is the default agent contract. Deliberately
// concise — small models tend to ignore long system prompts, and the
// tool descriptions already document mechanics.
const defaultSystemPrompt = `You are a Go programmer working inside a workdir you fully own.

You have these tools:
  - list_dir(path): see what files exist
  - read_file(path): read a file
  - write_file(path, content): create or replace a file
  - run_shell(command, args): run go build, go test, go run, ls, cat, head, tail, wc, diff, grep, test
  - cortex_search(query): search prior captures from earlier attempts (returns "empty" on a fresh run)

Workflow: explore with list_dir/read_file, then write_file your implementation, then run_shell to build and test. Iterate on errors. When the task is complete and tests pass, respond with a short summary and NO tool calls.

Rules:
  - Paths are relative to the workdir; no absolute paths, no "..".
  - Never write under .git or .cortex.
  - Use "go run" or "go test" to verify your work — don't claim success without running it.
  - When a build or test fails, read the error, fix the code, and try again.`

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

	// Fresh OpenRouter client per session: avoids leaking
	// LastCostUSD / LastProvider from a prior cell.
	client := llm.NewOpenRouterClientWithKey(nil, h.apiKey)
	client.SetModel(h.model)
	if h.apiURL != "" {
		client.SetAPIURL(h.apiURL)
	}

	// Per-turn output cap: derive from the model id unless the
	// caller overrode it via SetMaxOutputTokens. The cap bounds a
	// single response, not cumulative spend (that's Budget).
	maxOut := h.maxOutputTokens
	if maxOut <= 0 {
		maxOut = harness.ModelMaxOutputTokens(h.model)
	}
	client.SetMaxTokens(maxOut)

	registry := harness.NewToolRegistry()
	registry.Register(harness.NewReadFileTool(workdir))
	registry.Register(harness.NewWriteFileTool(workdir, registry))
	registry.Register(harness.NewListDirTool(workdir))
	registry.Register(harness.NewRunShellTool(workdir, registry))
	cortexSearch, err := harness.NewCortexSearchTool(workdir)
	if err != nil {
		return HarnessResult{}, fmt.Errorf("cortex_search tool: %w", err)
	}
	registry.Register(cortexSearch)

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

	loop := &harness.Loop{
		Provider:   client,
		Registry:   registry,
		System:     sys,
		MaxTurns:   h.maxTurns,
		Budget:     h.budget,
		Transcript: transcript,
		Notify:     h.notify,
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
