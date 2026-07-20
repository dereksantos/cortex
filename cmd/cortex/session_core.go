package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/dereksantos/cortex/internal/cache"
	"github.com/dereksantos/cortex/internal/capture"
	"github.com/dereksantos/cortex/internal/lineedit"
	"github.com/dereksantos/cortex/internal/memory"
	"github.com/dereksantos/cortex/internal/shellrisk"
	"github.com/dereksantos/cortex/internal/tools"
	"github.com/dereksantos/cortex/pkg/llm"
)

type CortexArgs []string

func (a CortexArgs) Request() *AgentRequest {
	return &AgentRequest{
		Model:       defaultModel,
		Messages:    []Message{{Role: RoleSystem, Content: systemPromptContent(projectInstructions())}},
		Temperature: defaultTemperature,
		Tools:       toolSet,
		MaxTokens:   codeMaxOutputTokens,
	}
}

// systemPromptContent builds the system message content: the base
// SystemPrompt plus an optional "# Project instructions (AGENTS.md)"
// section when instructions is non-empty. Shared by CortexArgs.Request()
// (CWD-implicit, via projectInstructions()) and applyProjectByName
// (project_workspace.go, M3.5's --project, via Workspace.Instructions())
// so the two stay provably identical modulo their instructions source.
func systemPromptContent(instructions string) string {
	content := SystemPrompt
	if instructions != "" {
		content += "\n\n# Project instructions (AGENTS.md)\n\n" + instructions
	}
	return content
}

type CortexSession struct {
	Args             *CortexArgs
	Request          *AgentRequest
	LastPromptTokens int
	LastCachedTokens int
	Window           int
	Study            ModelSpec
	Fleet            Fleet
	Config           *Config
	workspace        *Workspace
	deleteRoot       string
	allowDelete      bool
	quiet            bool
	confirmRisky     func(question string) bool
	classifyShell    shellrisk.ClassifyFn
	turnIntent       string
	// onThinking, when set, is invoked with active=true on the first
	// reasoning delta of a model call and active=false once its answer
	// content starts (or the call ends without one) — the served-session SSE
	// handler's hook (serve_stream.go) so the web UI gets a "thinking" event
	// instead of dead air while a quiet session deliberates. nil (the
	// default, including every non-served session) leaves send()'s quiet
	// path on the plain blocking Send it already used.
	onThinking func(active bool)
	// approveRisky, when set, is gateShell's (tool_deps.go) approval path for
	// a quiet (non-terminal) session that nonetheless has a human present
	// out-of-band — Discord (docs/cortex-web.md Phase 7's interactive risk
	// approval), unlike confirmRisky which requires !quiet (an interactive
	// terminal). Returns approved=true to run the command; approved=false,
	// timedOut=true reproduces gateShell's exact headless-Blocked message
	// (today's behavior when no approver exists at all — the approval
	// window lapsing is not distinguishable from "no approver" by design);
	// approved=false, timedOut=false is an explicit decline. nil (the
	// default, including every REPL/serve session) leaves gateShell's
	// existing headless-Blocked fallback untouched.
	approveRisky func(ctx context.Context, reason, command string) (approved, timedOut bool)
	SessionID    string
	transcript   *os.File
	capturer     *capture.Capture
	memory       *memory.Store // project-tier notes (.cortex/memory)
	// userMemory is the cross-project tier (~/.cortex/memory, via
	// internal/userhome) — the SAME internal/memory.Store type as memory,
	// pointed at the user's home instead of the project's .cortex dir
	// (docs/cross-source-learning.md piece 1). Wired alongside memory by
	// EnableMemory; nil has the identical "memory unavailable" behavior the
	// project tier already has when no .cortex workspace exists.
	userMemory    *memory.Store
	ws            *cache.WorkingSet
	outline       []cache.OutlineEntry
	outlineFolded string // digest of previously folded outline entries (P4); rides the front of the outline zone

	// awaitingScanRootsReply is armed by MaybeGreet (M1.7) right after a
	// first-run greeting fires; the REPL read loop's next call to
	// MaybeCaptureScanRoots (scanroots.go) treats that reply as the
	// answer to "where does your code live" and persists it.
	awaitingScanRootsReply bool

	sessionStart    time.Time
	turns           int
	turnNo          int // 1-based ordinal of the in-flight turn; 0 between turns (stamped into transcript entries)
	tokensIn        int
	tokensOut       int
	reasoningTokens int // completion_tokens_details.reasoning_tokens summed across the session (0 if never reported)
	costUSD         float64
	injectedChars   int
	captures        int
	injections      int

	md      *markdownRenderer
	mdWidth int
	live    *lineedit.Anchor
}

func (cs *CortexSession) markdown() *markdownRenderer {
	if cs.quiet {
		return nil
	}
	w := terminalWidth()
	if cs.live != nil {
		w = cs.live.Width()
	} else if !renderEnabled() {
		return nil
	}
	if cs.md == nil || cs.mdWidth != w {
		cs.md, cs.mdWidth = newMarkdownRenderer(w), w
	}
	return cs.md
}

// SetModel switches the coder's live model, re-resolving everything that
// binding carries — effort wire fields and the context window — rather than
// leaving them stale from the model it's replacing
// (docs/thinking-models.md known seam bug #1: /model, and the web UI's
// session override (serve_models.go's handleSetSessionModelBinding, which
// calls this same method), used to swap only the model name, so a switch to
// a hybrid reasoner could silently keep running with the old model's
// enable_thinking=false, or vice versa). When the discovered Fleet is nil or
// doesn't know the new model, effort clears to neutral (send nothing) and
// the window falls back (cs.windowSize()'s fallbackWindow) rather than
// carrying over the old binding's — the fleet has nothing to say about an
// unknown model, so nothing should be asserted on its behalf.
func (cs *CortexSession) SetModel(model string) {
	cs.Request.Model = model
	dialect := dialectFor(cs.Config.isOpenRouter())
	var effort llm.Effort
	window := 0
	if info, ok := cs.Fleet[model]; ok {
		// Re-validate the CURRENT effort intent against the new model's
		// thinking_mode, rather than resetting to some role default: /model
		// is a session-local override outside the role-binding system, so
		// there is no role policy to fall back to here.
		effort = degradeForThinkingMode(cs.Request.Effort, info.thinkingMode())
		window = info.MaxInput
	}
	applyEffort(cs.Request, dialect, effort)
	cs.Window = window
}

// windowSize resolves the code model's context window: learned (from an
// observed overflow, C2) beats configured, mirroring studyWindow()'s
// precedence. Consulting learnedWindows live only changes budget math
// (contextRatio, compact digest sizing, outline-fold thresholds, recall's
// gate) — it does not itself touch cs.ws's baked-in high/low watermarks,
// which are only rebuilt at a natural boundary (session construction,
// resume, or Compact()); the overflow handlers that write learnedWindows
// already trigger a Compact() in the same breath, so the working set
// catches up to the learned value at exactly that boundary.
func (cs *CortexSession) windowSize() int {
	if cs.Request != nil {
		if w, ok := learnedWindows[cs.Request.Model]; ok {
			return w
		}
	}
	if cs.Window > 0 {
		return cs.Window
	}
	return fallbackWindow
}

// newWorkingSet builds the demotion policy for the current window: the
// hydrated tail may grow to half the window and drains to a third by default
// (docs/context-architecture.md budgets), both configurable as validated
// fractions via context.tail_high_fraction / context.tail_drain_fraction
// (docs/configuration.md, cs.Config.tailHighWatermark/tailDrainWatermark).
// base is the message-log index where turn content starts.
func (cs *CortexSession) newWorkingSet(base int) *cache.WorkingSet {
	w := cs.windowSize()
	return cache.New(base, cs.Config.tailHighWatermark(w), cs.Config.tailDrainWatermark(w))
}

func NewCortexSession() *CortexSession {
	cfg := LoadConfig()
	// instructionBytesCap must be set before args.Request() (below) reads
	// AGENTS.md via projectInstructions() — the one call site that runs
	// before the rest of this function's config-driven wiring.
	instructionBytesCap = cfg.instructionBytesCap()
	tools.Configure(cfg.toolLimits())
	fleetDiscoveryTimeout = cfg.fleetDiscoveryTimeout()
	openRouterPreflightTimeout = cfg.preflightTimeout()
	labelTickInterval = cfg.tickerInterval()

	args := CortexArgs(os.Args)
	req := args.Request()
	workspace := WorkspaceFromCWD()

	var fleet Fleet
	if !cfg.isOpenRouter() {
		fleet = discoverFleet(context.Background(), cfg.backendEndpoint())
		if fleet == nil {
			fmt.Println(withColor(fmt.Sprintf("note: model discovery unavailable at %s — set backend in .cortex/config.json or pin models", cfg.backendEndpoint()), yellow))
		}
	}
	code := cfg.resolveBinding(roleCode, fleet)
	study := cfg.resolveBinding(roleStudy, fleet)

	// E2: at startup (not the per-turn Send hot path), preflight a curated
	// OpenRouter pick against the live catalog — cheap (one bounded
	// ListModels call), and only on the openrouter+curated path. A model
	// that's been retired since the curated table was written is swapped
	// for this process only; the config file is never touched.
	code, study = preflightCuratedModels(context.Background(), cfg, code, study,
		modelSubstitutionJournalDir(workspace.ContextDir()), liveOpenRouterListModels)

	if g := sharedSwapGroup(fleet, code, study); g != "" {
		fmt.Println(withColor(fmt.Sprintf("warning: code (%s) and study (%s) share swap_group %q — they evict each other every turn; route one to different silicon", code.Model, study.Model, g), yellow))
	}

	req.Model = code.Model
	req.BaseURL = code.Endpoint
	req.APIKey = resolveKey(code)
	applyEffort(req, dialectFor(cfg.isOpenRouter()), code.Thinking)
	req.MaxTokens = code.maxOut(codeMaxOutputTokens)
	req.Temperature = code.temperature(defaultTemperature)
	// P1 timeout unification: the coder's live request stamps its transport
	// budget from the code role's config (models.code.request_timeout_sec /
	// .max_send_attempts / .retry_backoff_ms), falling back to today's
	// hardcoded defaults exactly.
	req.Timeout = code.timeout(requestTimeout)
	req.MaxAttempts = code.maxAttempts(maxSendAttempts)
	req.Backoff = code.backoff(retryBackoff)

	// network.compat_timeout_sec is the config surface for the existing
	// CORTEX_COMPAT_TIMEOUT_SEC env var's fallback default (pkg/llm's
	// DefaultCompatTimeoutSec) — env still wins over it. Set once here so
	// every OpenAICompatClient this process constructs afterward (the study
	// provider, the reasoner, any embedder) picks it up.
	if cfg != nil && cfg.Network.CompatTimeoutSec > 0 {
		llm.DefaultCompatTimeoutSec = cfg.Network.CompatTimeoutSec
	}

	if cfg.isOpenRouter() {
		req.Usage = &usageInclude{Include: true}
	}

	allowDelete := cfg.deleteEnabled()
	deleteRoot := "."
	if cfg != nil && cfg.Tools.DeleteRoot != "" {
		deleteRoot = cfg.Tools.DeleteRoot
	}
	if abs, err := filepath.Abs(deleteRoot); err == nil {
		deleteRoot = abs
	}
	if !allowDelete {
		req.Tools = toolsExcept(req.Tools, FunctionRemove)
	}

	cs := &CortexSession{
		Args:         &args,
		Request:      req,
		Config:       cfg,
		workspace:    workspace,
		Window:       code.Window,
		Study:        study,
		Fleet:        fleet,
		deleteRoot:   deleteRoot,
		allowDelete:  allowDelete,
		sessionStart: time.Now(),
	}
	cs.ws = cs.newWorkingSet(1)
	// Strip declarations for every IsToolEnabled-gated tool that config
	// disabled — scan_landscape, web_search/fetch_url, agent, context_* — so
	// the model never sees a tool that dispatch would only ever refuse
	// (docs/eval-context-pivot.md; Track B item B1). Reuses IsToolEnabled,
	// dispatch's own gate (tools.go's Execute), as the single source of
	// truth rather than a second parallel disabled-tool list; dispatch keeps
	// its own check as defense-in-depth against a hallucinated tool name.
	cs.Request.Tools = filterEnabledTools(cs.Request.Tools, cs.IsToolEnabled)
	return cs
}

// IsToolEnabled reports whether a context window tool is enabled via config.
func (cs *CortexSession) IsToolEnabled(toolName string) bool {
	if cs.Config == nil {
		return true // default: all tools enabled
	}
	// nil pointers mean defaults: enabled.
	t := &cs.Config.Tools
	switch toolName {
	case tools.FunctionWebSearch, tools.FunctionFetchURL:
		return t.EnableWeb == nil || *t.EnableWeb
	case tools.FunctionAgent:
		return t.EnableAgent == nil || *t.EnableAgent
	case tools.FunctionScanLandscape:
		return t.EnableScan == nil || *t.EnableScan
	case tools.FunctionContextEvict:
		return t.EnableContextEvict == nil || *t.EnableContextEvict
	case tools.FunctionContextMerge:
		return t.EnableContextMerge == nil || *t.EnableContextMerge
	case tools.FunctionContextAdjustWatermarks:
		return t.EnableContextAdjustWatermarks == nil || *t.EnableContextAdjustWatermarks
	}
	return true // unknown tools enabled by default
}

// ValidateToolCall provides dynamic validation for tool calls beyond config.
// Returns (true, "") if valid, (false, message) if invalid.
func (cs *CortexSession) ValidateToolCall(tc ToolCall) (bool, string) {
	switch tc.Function.Name {
	case "context_adjust_watermarks":
		// Validate watermarks are within bounds (±highWM/2 — mirrors
		// internal/cache.WorkingSet.AdjustWatermarks' own clamp exactly, so
		// this pre-check can't diverge from what dispatch will actually
		// enforce. Derived from the LIVE high watermark rather than
		// windowSize()/4: with context.tail_high_fraction now configurable
		// (docs/configuration.md), highWM is no longer guaranteed to equal
		// W/2, so a windowSize()-only bound could reject (or wrongly accept)
		// deltas AdjustWatermarks itself would judge differently.
		if cs != nil && cs.ws != nil {
			high, _ := cs.ws.GetWatermarks()
			bound := high / 2
			if highDelta, _ := tc.IntArg("high_delta"); highDelta != 0 {
				if highDelta < -bound || highDelta > bound {
					return false, fmt.Sprintf("high_delta %d is out of bounds (±%d for current high watermark %d)", highDelta, bound, high)
				}
			}
			if lowDelta, _ := tc.IntArg("low_delta"); lowDelta != 0 {
				if lowDelta < -bound || lowDelta > bound {
					return false, fmt.Sprintf("low_delta %d is out of bounds (±%d for current high watermark %d)", lowDelta, bound, high)
				}
			}
		}
	}
	return true, ""
}

// RemoveOutlineEntry removes an outline entry by citation.
// Returns true if the entry was found and removed.
// This is idempotent (safe to call multiple times).
func (cs *CortexSession) RemoveOutlineEntry(citation string) bool {
	for i := 0; i < len(cs.outline); i++ {
		if cs.outline[i].Citation == citation {
			cs.outline = append(cs.outline[:i], cs.outline[i+1:]...)
			return true
		}
	}
	return false
}

// MergeOutlineEntries replaces the contiguous outline entries from
// startCitation through endCitation with a single merged entry. The merged
// entry carries ONE spanning citation — @session/<id>#m<firstStart>-<lastEnd>
// — so recall still resolves every original message (turn spans partition the
// message log, so the span between two outline citations is contiguous even
// if an entry in between was evicted). Returns the spanning citation.
func (cs *CortexSession) MergeOutlineEntries(startCitation, endCitation string) (string, error) {
	sm := citationRe.FindStringSubmatch(startCitation)
	em := citationRe.FindStringSubmatch(endCitation)
	if sm == nil || em == nil {
		return "", fmt.Errorf("citations must be @session/<id>#m<start>-<end> coordinates, as shown in the outline")
	}
	if sm[1] != em[1] {
		return "", fmt.Errorf("citations reference different sessions (%s vs %s)", sm[1], em[1])
	}

	startIdx, endIdx := -1, -1
	for i, e := range cs.outline {
		if e.Citation == startCitation {
			startIdx = i
		}
		if e.Citation == endCitation {
			endIdx = i
		}
	}
	if startIdx == -1 {
		return "", fmt.Errorf("start citation %s not found in the outline", startCitation)
	}
	if endIdx == -1 {
		return "", fmt.Errorf("end citation %s not found in the outline", endCitation)
	}
	if endIdx <= startIdx {
		return "", fmt.Errorf("range_end must come after range_start in the outline")
	}

	spanning := fmt.Sprintf("@session/%s#m%s-%s", sm[1], sm[2], em[3])
	merged := mergeOutlineEntries(cs.outline[startIdx:endIdx+1], spanning)
	cs.outline = append(cs.outline[:startIdx], append([]cache.OutlineEntry{merged}, cs.outline[endIdx+1:]...)...)
	return spanning, nil
}

// mergeOutlineEntries folds a run of outline entries into one: user heads join
// on newlines (bounded by outlineUserCap), actions concatenate in order, and
// the reply head keeps the last non-empty one — the state the run ended in.
func mergeOutlineEntries(entries []cache.OutlineEntry, citation string) cache.OutlineEntry {
	users := make([]string, 0, len(entries))
	var actions []string
	replyHead := ""
	for _, e := range entries {
		if e.User != "" {
			users = append(users, e.User)
		}
		actions = append(actions, e.Actions...)
		if e.ReplyHead != "" {
			replyHead = e.ReplyHead
		}
	}
	user := strings.Join(users, "\n")
	if r := []rune(user); len(r) > outlineUserCap {
		user = string(r[:outlineUserCap]) + "… (truncated; recall the citation below for the rest)"
	}
	return cache.OutlineEntry{
		Turn:      entries[0].Turn,
		User:      user,
		Actions:   actions,
		ReplyHead: replyHead,
		Citation:  citation,
	}
}

// OutlineLen returns the number of outline entries.
func (cs *CortexSession) OutlineLen() int {
	return len(cs.outline)
}

// AdjustWatermarks adjusts the working set watermarks by the given deltas.
// Bounded (±W/4) to prevent abuse. Returns (oldHigh, oldLow, newHigh, newLow, error).
func (cs *CortexSession) AdjustWatermarks(highDelta, lowDelta int) (int, int, int, int, error) {
	if cs.ws == nil {
		return 0, 0, 0, 0, fmt.Errorf("working set not available")
	}
	oldHigh, oldLow := cs.ws.GetWatermarks()
	newHigh, newLow, err := cs.ws.AdjustWatermarks(highDelta, lowDelta)
	return oldHigh, oldLow, newHigh, newLow, err
}

func toolsExcept(ts []Tool, name string) []Tool {
	out := make([]Tool, 0, len(ts))
	for _, t := range ts {
		if t.Function.Name != name {
			out = append(out, t)
		}
	}
	return out
}

// filterEnabledTools drops any declaration whose tool name the predicate
// (IsToolEnabled in production) reports disabled. It is the wire-side half
// of the config gates: dispatch's Execute already refuses a disabled tool
// call, but until this filter ran, the declaration stayed on the wire
// anyway — a model could see and call a tool that would only ever be
// refused (docs/eval-context-pivot.md; Track B item B1).
func filterEnabledTools(ts []Tool, enabled func(name string) bool) []Tool {
	out := make([]Tool, 0, len(ts))
	for _, t := range ts {
		if enabled(t.Function.Name) {
			out = append(out, t)
		}
	}
	return out
}

func (cs *CortexSession) PrintArgs() {
	fmt.Printf("Cortex Model: %s Temp:%f\n", cs.Request.Model, cs.Request.Temperature)
}

func (cs *CortexSession) Append(message Message) {
	cs.Request.Messages = append(cs.Request.Messages, message)
	cs.writeTranscript(message)
	// Update LastPromptTokens to reflect current context size
	// This ensures the display gauge updates as tool results are appended
	cs.LastPromptTokens = cs.currentContextSize()
}

// currentContextSize estimates the current context size from all messages.
// It sums len(Content) for each message plus len(Function.Name)+len(Function.Arguments)
// for each ToolCall, then converts to tokens using cache.TokensOf.
func (cs *CortexSession) currentContextSize() int {
	sum := 0
	for _, msg := range cs.Request.Messages {
		sum += len(msg.Content)
		for _, call := range msg.ToolCalls {
			sum += len(call.Function.Name) + len(call.Function.Arguments)
		}
	}
	return cache.TokensOf(sum)
}
