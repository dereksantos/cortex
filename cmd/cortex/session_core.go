package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/dereksantos/cortex/internal/cache"
	"github.com/dereksantos/cortex/internal/capture"
	"github.com/dereksantos/cortex/internal/lineedit"
	"github.com/dereksantos/cortex/internal/memory"
	"github.com/dereksantos/cortex/internal/projectcmd"
	"github.com/dereksantos/cortex/internal/shellrisk"
	"github.com/dereksantos/cortex/internal/testguard"
	"github.com/dereksantos/cortex/internal/tools"
	"github.com/dereksantos/cortex/pkg/llm"
)

// resolveProjectCommands computes a workspace's resolved command set
// (issue #129): discovery from the root's manifests, with the config's
// project.commands declarations and the root's RESOLVED instruction
// file's `## Commands` section overriding it field-by-field (config >
// instruction file > discovery). The instruction file is resolved the
// same way the system prompt's project-instructions seed resolves it
// (#152): the first of AGENTS.md, CLAUDE.md, .github/copilot-instructions.md
// present at the root (no concatenation), read with the same size cap
// (readInstructions) — so a repo whose instructions live in CLAUDE.md
// still has its declared commands picked up. It is the ONE parsing path
// for the `## Commands` declaration: read from the workspace root
// directly, so the hook works for a CWD-implicit project with no config
// file. It never fails: an undetectable root or unreadable manifest
// degrades to an empty set, which the post-edit hook treats as "no hook".
func resolveProjectCommands(root string, cfg *Config) projectcmd.Commands {
	discovered, err := projectcmd.Discover(root)
	if err != nil {
		return projectcmd.Commands{}
	}
	declared := projectcmd.Declared{}
	if cfg != nil {
		declared = cfg.DeclaredProjectCommands()
	}
	// The `## Commands` section is read from the RESOLVED instruction file
	// (workspaceInstructions, #152's resolution); the file's name becomes
	// the Source label for any declaration that wins, so the report shows
	// where it came from. No file → no declarations (Resolve gets "").
	agentsPath, instructions := workspaceInstructions(root)
	agents := projectcmd.ParseAgentsCommands(instructions)
	return projectcmd.Resolve(discovered, declared, agents, fileLabel(root, agentsPath))
}

type CortexArgs []string

func (a CortexArgs) Request() *AgentRequest {
	path, instructions := projectInstructions()
	return &AgentRequest{
		Model:       defaultModel,
		Messages:    []Message{{Role: RoleSystem, Content: systemPromptContent(fileLabel(WorkspaceFromCWD().Root, path), instructions)}},
		Temperature: defaultTemperature,
		Tools:       toolSet,
		MaxTokens:   codeMaxOutputTokens,
	}
}

// systemPromptContent builds the system message content: the base prompt
// (built-in SystemPrompt, or prompt.file's replacement — promptBase, set by
// configurePrompt), then the attribution line when attribution is on
// (promptAttribution, set by configureAttributionPrompt), then any
// prompt.append text, then an optional "# Project instructions (<file>)"
// section when instructions is non-empty — <file> (label) names which
// instruction file was loaded (AGENTS.md, CLAUDE.md, .github/
// copilot-instructions.md, #147) so the seed shows its own provenance. The
// content is byte-stable for the life of the session — the per-turn memory
// section never rides here, it is delivered through the ephemeral wire slot
// (turn.go's memorySectionFor) — so the append-stable prefix and the prompt
// cache built on it survive every turn. Shared by CortexArgs.Request()
// (CWD-implicit, via projectInstructions()) and applyProjectByName
// (project_workspace.go, M3.5's --project, via Workspace.Instructions()) so
// the two stay provably identical modulo their instructions source.
func systemPromptContent(label, instructions string) string {
	content := promptBase
	if promptAttribution != "" {
		content += "\n\n" + promptAttribution
	}
	if promptAppend != "" {
		content += "\n\n# Additional instructions\n\n" + promptAppend
	}
	if instructions != "" {
		content += "\n\n# Project instructions (" + label + ")\n\n" + instructions
	}
	return content
}

type CortexSession struct {
	Args             *CortexArgs
	Request          *AgentRequest
	LastPromptTokens int
	LastCachedTokens int
	// LastOutputTokens is the last response's billed completion tokens — the
	// status row's "out" figure (issue #109), mirroring LastPromptTokens for
	// the "in" side. Updated per request in turn()'s onStatusUpdate and
	// settled to the turn's final request in turn() after the run.
	LastOutputTokens int
	Window           int
	Study            ModelSpec
	Fleet            Fleet
	// deadModels marks models the healing ladder (heal.go) found failing
	// this session — skipped by later candidate walks. Session-local by
	// design; nothing persists except the journal receipts.
	deadModels map[string]modelErrClass
	// healList is the healing ladder's catalog fetch, injectable for tests;
	// nil means liveOpenRouterListModels (the production default).
	healList listModelsFn
	// catalog is the OpenRouter listing this session holds, if any: the
	// startup fetch the preflight made, kept so a /model switch can read a
	// model's declared input modalities for the vision verdict (#216)
	// without refetching. nil (a non-OpenRouter backend, or a failed fetch)
	// means the switch falls back to the config flag / name heuristic.
	catalog   []llm.OpenRouterModel
	Config    *Config
	workspace *Workspace
	// projectCommands is the resolved project command set (issue #129) —
	// discovery from the workspace's manifests, overridden by the config's
	// project.commands declarations and the resolved instruction file's
	// `## Commands` section. Computed once at session construction /
	// project targeting; nil-safety means an empty value (no manifest, no
	// declarations) is a no-op hook.
	projectCommands projectcmd.Commands
	// hookState is the session-scoped state of the post-edit hook
	// (issue #129): the "workspace not trusted" note fires once per
	// session, and the session is the unit that owns that flag AND the
	// current mode (tools.PostEditHookState; a REPL /hook command lowers
	// it in place via SetMode — it never raises above the process
	// ceiling installed below from the resolved config).
	hookState     *tools.PostEditHookState
	deleteRoot    string
	allowDelete   bool
	quiet         bool
	confirmRisky  func(question string) lineedit.ConfirmChoice
	classifyShell shellrisk.ClassifyFn
	turnIntent    string
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

	// shellApprovals is the session-scoped bash approval list (issue #107):
	// the records the user chose "always this session" for at the
	// risky-command prompt — an exact command (chosen "a") or a derived
	// prefix (chosen "p", the program plus one subcommand + "*", e.g.
	// "make test*" for "make test ./pkg/..."). The kind rides with each
	// record on purpose: an exact approval of a command that ends in "*"
	// (e.g. "rm -f build/*") must keep comparing byte for byte — it must
	// never be re-read as a prefix by the pattern string alone. It is
	// memory-only by design: a fresh session starts with an empty list, and
	// persisting it to the config (a `tools.shell_allow` list) is a
	// separate decision. gateShell checks it for Risky verdicts only —
	// after classification, never before — so an approval can never
	// override a Blocked command.
	shellApprovals []shellApproval

	// pendingImage is the image attachment read_file just recorded on
	// THIS session via the ImageSink seam (#217): a per-session field, so
	// concurrent sessions (serve, discord) can never take each other's
	// image. spliceImageResult consumes it onto the tool-result message.
	// pendingImageSet distinguishes "an image is parked" from the zero
	// value. pendingSideCar is the same part parked one step further —
	// from the splice until the engine's append-time side-car write
	// (writeImageSideCarAt) consumes it. Tool dispatch within a session
	// is sequential (one tool batch at a time), so no lock is needed.
	pendingImage      tools.ImagePart
	pendingImageSet   bool
	pendingSideCar    tools.ImagePart
	pendingSideCarSet bool
	// pendingTurnSideCars are the image parts a HUMAN attached to the turn
	// (#218), parked by attachTurnImages for writeTurnImageSideCars to persist
	// at the transcript index the turn's user message lands at. A slice
	// because one turn can carry several @mentioned images, and unlike the
	// read_file slot above it is never keyed to a tool observation — the user
	// message is appended once per turn, so this is set and consumed within
	// the same turn (cleared unconditionally, so an early return cannot leak
	// an image onto a later message).
	pendingTurnSideCars []tools.ImagePart
	// pendingTurnSideCarRefs are the references (an @mention path, a URL) the
	// parked images above came from, index-aligned with them, so the manifest
	// written beside the bytes can name what the human attached.
	pendingTurnSideCarRefs []string

	SessionID  string
	transcript *os.File
	capturer   *capture.Capture
	memory     *memory.Store // project-tier notes (.cortex/memory)
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
	// testwatch is this turn's before-snapshot of the files its
	// write_file/edit_file/remove_path calls will touch (testwatch.go, issue
	// #141). Armed by touchFile before each mutating call (coderDispatcher);
	// drained into the "tests changed:" receipt (testwatchReceipt) at the
	// clean-finalize point and at turn end (captureTurn), and cleared at the
	// START of every turn (turn.go's testwatchDrop) so a turn that errors or
	// is interrupted before captureTurn can't leak a stale before-side into
	// the next one. Nil outside a turn.
	testwatch map[string]*testwatchSnapshot

	// testwatchBash / testwatchBashArmed are the BASH arm of the turn's
	// testwatch receipt (issue #141): a separate budget of compact
	// per-file baselines (testguard.Baseline) for the workspace's
	// test-named files, armed ONCE per turn by the first bash call
	// (armTestwatch). A separate store so the bash arm can't starve the
	// named-tool arm's 32-file / 1 MiB content budget — a turn that runs
	// `go test` before a write_file on a test file must still get a
	// receipt for the write_file. Nil / false outside a turn.
	testwatchBash      map[string]testguard.Baseline
	testwatchBashArmed bool

	// inTurnOriginals records, per absolute message index, the ORIGINAL
	// content of every tool result applyInTurnDemotion stubbed on the wire
	// copy (indemote.go). Turn-end consumers — the turn-end
	// outline entry (turnOutlineEntry) and the journal capture (captureTurn)
	// — read through turnOriginalSpan instead of the mutated wire copy, so
	// they see the original result (an [err] label, the true content for
	// web_search/fetch_url artifacts), never the one-line stub (issue #171
	// item 5). Entries live until their OWNING turn is demoted — turn.go
	// deletes a span's entries when it builds that span's outline entry,
	// because DemoteBatch usually drains a turn several turns after it ran,
	// not at the next turn's start — and the map is cleared wholesale
	// wherever the message log is rewritten (Compact, /clear, ResumeTranscript),
	// because the absolute indices shift there. The transcript already holds
	// every original losslessly; this is the in-session in-memory half.
	// Pure in-memory cache: never written to session state, so nothing
	// persists it.
	inTurnOriginals map[int]string
	// senderOverride, when non-nil, replaces the coder's round-trip sender
	// (the network-backed coderSender) inside the healing ladder — a TEST-ONLY
	// seam (no production code sets it) that lets a test drive the REAL turn
	// path with a scripted model and zero network (the same pattern healList
	// is injectable for tests).
	senderOverride Sender
	// coderDispatcherOverride, when non-nil, replaces coderDispatcher() (loop.go)
	// — the tool-call dispatcher the coder's Toolset is built with (turn.go).
	// TEST-ONLY seam (no production code sets it) for the same class of test
	// as senderOverride: drive the REAL turn path with scripted tool results
	// instead of real file access. Both are nil in every production session.
	coderDispatcherOverride func() AgentDispatcher
	// subagentSenderOverride, when non-nil, replaces the subagent's model
	// round-trip sender (the blockingSender a subagent's runLoop uses) for the
	// named subagent role — a TEST-ONLY seam (no production code sets it) for
	// the same class of test as senderOverride: drive the REAL subagent run
	// (runSubagentStats → runLoop over the real dispatcher) with a scripted
	// model and zero network. A subagent's model REPLIES (its tool-call and
	// final-answer rounds) are produced by the sender, not the request, so
	// scripting them goes through this seam, not through the request. nil in
	// every production session.
	subagentSenderOverride map[string]Sender

	// testwatchScratchBefore is the leftover-debug arm's (issue #154)
	// PRE-bash baseline of scratch-named paths: the set of workdir-relative
	// scratch-named files that EXISTED before this turn's first bash call.
	// Armed by the same armTestwatch walk as the test-file baselines (before
	// the command runs); sweepScratchFiles then records only scratch files
	// NEW since this baseline — a pre-existing scratch file (testdata/foo.bak,
	// scripts/tmp_setup.sh, ...) is already in the baseline, so it is not
	// snapshotted and never gets a false "scratch file left behind" receipt
	// on every turn that runs bash. Dropped with the snapshot (testwatchDrop).
	testwatchScratchBefore map[string]bool

	// turnLinter is the turn-end lint pass's (issue #129 piece 3) per-turn
	// state: the DISTINCT files this turn's write_file/edit_file calls
	// touched (workdir-resolved paths, first-touch order — the same keys
	// touchFile uses, normalized), and the turn's total lint budget
	// (project.turn_lint_budget_sec, 0 = the default 60s) as a deadline.
	// Armed by lintTouchedPath (the coder dispatcher, before each mutating
	// call), read by turnLintAtFinalize at the clean-finalize point (the
	// turn's FinalizeHook, alongside the #141 test-loss receipt), and
	// dropped with the snapshot (testwatchDrop). Lint moved off the per-
	// edit hook because clippy/eslint are slow and noisy: format stays
	// per-edit, lint runs once per turn over the distinct touched files.
	// Nil outside a turn; budgetSec 0 means "not armed by NewCortexSession"
	// (hand-built test sessions use the default budget).
	turnLinter struct {
		touched   []string
		budgetSec int
	}
	// lintReceipt is this turn's "lint: …" receipt (issue #129 piece 3):
	// computed at the clean-finalize point by turnLintAtFinalize (stored
	// there because the pass must run BEFORE the model's final answer, so
	// the model can fix findings) and read by turn.go into
	// TurnResult.LintReceipt (the REPL / `cortex turn` print it) and by
	// captureTurn (the journal record shows it). "" between turns (dropped
	// with the snapshot).
	lintReceipt string

	// awaitingScanRootsReply is armed by MaybeGreet (M1.7) right after a
	// first-run greeting fires; the REPL read loop's next call to
	// MaybeCaptureScanRoots (scanroots.go) treats that reply as the
	// answer to "where does your code live" and persists it.
	awaitingScanRootsReply bool

	// sameActionBlocked is the per-turn same-action ledger (issue #169):
	// the set of effect classes (shellrisk.EffectClass) that were Blocked
	// in the current turn, keyed by cs.turnNo so it resets automatically
	// when the turn advances. A later command in the same class is
	// refused before it is even classified — a mechanical block against
	// routing around an earlier block with a same-effect variant. Nil
	// outside a turn.
	sameActionBlocked map[string]bool

	// checkpoints is the per-turn undo stack (issue #111): the snapshot
	// (tree hash + untracked baseline) of each turn that changed the working
	// tree, in turn order. A snapshot is recorded at the START of every turn
	// (recordCheckpoint) but committed to this stack only at the turn's END,
	// and only if the turn actually changed the working tree — commitCheckpoint
	// re-snapshots the tracked state and the untracked set and compares them
	// with the pending entry, keeping the checkpoint iff the tree hash differs
	// or the untracked set differs (so a read-only turn's recorded ref is
	// dropped instead) — and the stack's depth N always maps to the
	// Nth-most-recent turn that changed files (the issue's spec). Scoped to
	// cs.root(), keyed by session id, pruned to the newest maxCheckpointRefs on
	// append, cleared on /clear and session end (clearCheckpoints). Nil until
	// the first mutating turn. The turn never fails because of a checkpoint —
	// recordCheckpoint and commitCheckpoint swallow git errors.
	checkpoints *checkpointStack

	// pending is the snapshot recordCheckpoint took at the start of the
	// in-flight turn, not yet on the undo stack: commitCheckpoint at the
	// turn's end re-snapshots the working tree and commits it (and keeps its
	// ref) iff the turn changed the tree, else drops the ref. Zero between
	// turns.
	pending checkpointEntry

	// taint is the per-turn untrusted-content taint (issue #102): set when
	// attacker-controllable web content (a fetch_url / web_search result,
	// detected by its framing marker in coderDispatcher, loop.go) enters the
	// conversation. While the turn is tainted, gateShell raises the bar for
	// Risky shell commands: an interactive approver is asked with the taint
	// reason appended (the intent judge's Safe verdict no longer waves a
	// Risky command through — see tool_deps.go), and with no approver
	// reachable (headless, subagent, timeout) the command is blocked with
	// shellrisk.TaintBlockedMessage. Same lifecycle as sameActionBlocked
	// (issue #169): inert outside a turn (turnNo == 0 — record drops, the
	// gate never consults it), explicitly cleared at the START of every
	// turn in turn.go so a turn that errored or was interrupted before its
	// end cannot leak the taint into the next one. Nil between turns.
	taint *untrustedTaint

	sessionStart    time.Time
	turnStart       time.Time // in-flight turn's start (issue #109: the status row's elapsed clock); zero between turns
	turns           int
	turnNo          int // 1-based ordinal of the in-flight turn; 0 between turns (stamped into transcript entries)
	tokensIn        int
	tokensOut       int
	reasoningTokens int // completion_tokens_details.reasoning_tokens summed across the session (0 if never reported)
	costUSD         float64
	injectedChars   int
	captures        int
	injections      int
	// redactions is the running count of secret patterns masked for the
	// CURRENT turn as they were persisted (issue #103): writeTranscript
	// redacts each outgoing message (content + tool-call args + tool results)
	// on the way to the on-disk transcript, so a secret the agent read is never
	// stored. The live in-memory Request.Messages stays verbatim (the model
	// still uses the value this turn); only what hits disk is masked. The
	// counter is reset at the START of every turn (turn.go) so it always
	// counts exactly the in-flight turn, and carried on TurnResult so a
	// caller can see it.
	redactions int
	// redactionsTotal is the CUMULATIVE session count of secret patterns
	// masked while the session's messages were persisted (issue #103): every
	// turn's per-turn redactions (cs.redactions, reset at turn start) is
	// folded in here once the turn ends, so the session summary and the eval
	// journal can report the session-wide total — distinct from the per-turn
	// cs.redactions that rides TurnResult. It is a session-lifetime metric in
	// the same sense as cs.captures / cs.tokensIn (the summary reports the
	// session's whole lifetime, not just the current post-/clear conversation).
	redactionsTotal int

	md      *markdownRenderer
	mdWidth int
	live    *lineedit.Anchor

	phase turnPhase // one-char state light at the far left of Prompt(); see display.go
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
	// #216: a /model switch re-derives the vision verdict with the SAME
	// precedence the role binding used (visionForModel): the code role's
	// explicit config flag when the new model is that role's configured
	// model, then the catalog's declared input modalities for an
	// OpenRouter model this session's listing knows, then the id's
	// capability tags, then false — unknown means the gate refuses images,
	// never drops them silently.
	cs.Request.Vision = visionForModel(cs.Config, cs.catalog, model)
	cs.Window = window
}

// applyVisionCatalog adopts a freshly fetched OpenRouter listing as this
// session's vision source (#216) and re-derives the in-flight request's
// verdict from it. Explicit `models.code.vision` is never overridden —
// visionForModel consults it first. Called from the healing ladder, whose
// substitution changes the model under a running session; startup does its
// equivalent inside preflightCuratedModels, where the study binding is
// still a local ModelSpec rather than session state.
func (cs *CortexSession) applyVisionCatalog(catalog []llm.OpenRouterModel) {
	if len(catalog) == 0 || cs.Config == nil || !cs.Config.isOpenRouter() {
		return
	}
	cs.catalog = catalog
	if cs.Request != nil {
		cs.Request.Vision = visionForModel(cs.Config, catalog, cs.Request.Model)
	}
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

// printStartupWarning writes a colored startup preflight diagnostic (model
// discovery unavailable, shared swap_group, …) to w. These are
// operator-facing status, not turn output, so callers always pass
// os.Stderr — stdout must stay machine-clean for headless `turn --json`
// consumers (the session id is already stderr-only for the same reason).
func printStartupWarning(w io.Writer, msg string) {
	fmt.Fprintln(w, withColor(msg, yellow))
}

func NewCortexSession() *CortexSession {
	cfg := LoadConfig()
	// instructionBytesCap must be set before args.Request() (below) reads
	// AGENTS.md via projectInstructions() — the one call site that runs
	// before the rest of this function's config-driven wiring.
	// configurePrompt has the same ordering constraint (args.Request() builds
	// the system message) and additionally must run AFTER the cap is set,
	// since prompt.file truncates at instructionBytesCap.
	instructionBytesCap = cfg.instructionBytesCap()
	configurePrompt(cfg)
	tools.Configure(cfg.toolLimits())
	tools.SetHookCeiling(cfg.postEditHookMode())
	fleetDiscoveryTimeout = cfg.fleetDiscoveryTimeout()
	openRouterPreflightTimeout = cfg.preflightTimeout()
	labelTickInterval = cfg.tickerInterval()

	args := CortexArgs(os.Args)
	workspace := WorkspaceFromCWD()

	var fleet Fleet
	if !cfg.isOpenRouter() {
		fleet = discoverFleet(context.Background(), cfg.backendEndpoint())
		if fleet == nil {
			printStartupWarning(os.Stderr, fmt.Sprintf("note: model discovery unavailable at %s — set backend in .cortex/config.json or pin models", cfg.backendEndpoint()))
		}
	}
	code := cfg.resolveBinding(roleCode, fleet)
	study := cfg.resolveBinding(roleStudy, fleet)

	// E2: at startup (not the per-turn Send hot path), preflight a curated
	// OpenRouter pick against the live catalog — cheap (one bounded
	// ListModels call), and only on the openrouter+curated path. A model
	// that's been retired since the curated table was written is swapped
	// for this process only; the config file is never touched. The listing
	// itself is returned and kept on the session (#216): it is the primary
	// source of the vision verdict, for these bindings and for any later
	// /model switch.
	code, study, startupCatalog := preflightCuratedModels(context.Background(), cfg, code, study,
		modelSubstitutionJournalDir(workspace.ContextDir()), liveOpenRouterListModels)
	// #216: the preflight settled verdicts from the live catalog where it
	// had one; anything it left nil (no fetch, an unlisted id) falls to the
	// name heuristic here — LAST, so the heuristic can never mask a verdict
	// the catalog stated for these bindings.
	applyHeuristicVision(cfg, &code, &study)

	if g := sharedSwapGroup(fleet, code, study); g != "" {
		printStartupWarning(os.Stderr, fmt.Sprintf("warning: code (%s) and study (%s) share swap_group %q — they evict each other every turn; route one to different silicon", code.Model, study.Model, g))
	}

	// The attribution line names the resolved code model in its trailer, so
	// it is configured only now — after resolution, before args.Request()
	// builds the system message.
	configureAttributionPrompt(cfg, code.Model)
	req := args.Request()
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
	req.Vision = code.VisionEnabled()

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
		Args:            &args,
		Request:         req,
		Config:          cfg,
		workspace:       workspace,
		Window:          code.Window,
		Study:           study,
		Fleet:           fleet,
		catalog:         startupCatalog,
		deleteRoot:      deleteRoot,
		allowDelete:     allowDelete,
		projectCommands: resolveProjectCommands(workspace.Root, cfg),
		hookState:       &tools.PostEditHookState{},
		sessionStart:    time.Now(),
	}
	// Issue #119: the workspace is now resolved and every command that goes
	// through this constructor (REPL, turn, study, learn, serve, discord,
	// loop, study-eval) will write under its .cortex/ — journal, memory, the
	// learn cursor — often without ever opening a transcript (study/learn
	// never call StartTranscript). Self-ignore the dir now, before the first
	// write could leak it; a later re-target (--project / serve / loop
	// firings, via applyProjectByName) runs the same guard for its (possibly
	// different) root. See gitignore_self.go.
	cs.SetWorkspace(workspace)
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

// SetHookMode lowers the session's post-edit hook mode in place (issue #129
// piece 2): a REPL /hook command is its sole caller. It is monotone-down;
// the process ceiling (SetHookCeiling, installed at session construction
// from the resolved config) is folded in at read time by EffectiveHookMode,
// so a session can never operate in a mode more permissive than the one
// the operator configured; the agent has no setter at all.
func (cs *CortexSession) SetHookMode(m tools.HookMode) {
	if cs.hookState != nil {
		cs.hookState.SetMode(m)
	}
}

// hookModeName renders the session's current EFFECTIVE hook mode (the
// /hook command's current-value display; the more-restrictive of the
// session mode and the process ceiling, where larger is more restrictive —
// off > format > all).
func (cs *CortexSession) hookModeName() string {
	switch tools.EffectiveHookMode(cs.hookState) {
	case tools.HookModeOff:
		return "off"
	case tools.HookModeFormat:
		return "format"
	default:
		return "all"
	}
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

// AttributionProvider implementation for *CortexSession. The session resolves
// the model itself — cs.Request.Model is the code role's binding
// NewCortexSession resolved for the Turn — so the trailer always names the
// model that actually authored the commit (never a literal "<model>"). A nil
// Config still yields the default-enabled trailer (Config.attributionCommit
// is nil-safe).
func (cs *CortexSession) AttributionCommit() string {
	model := ""
	if cs != nil && cs.Request != nil {
		model = cs.Request.Model
	}
	return cs.Config.attributionCommit(model)
}

// AttributionJournaler implementation for *CortexSession (issue #146): the
// coordinate pair an attribution.commit receipt carries. The bash tool cannot
// reach a session's turn ordinal or workspace root, and cmd/cortex cannot own
// the write path (internal/tools classifies the commit), so the session
// supplies the coordinates and internal/tools appends the event — the same
// split Workdirer uses. turn is cs.turnNo, the 1-based in-flight turn (0
// between turns), matching the "turn" field captureTurn records.
func (cs *CortexSession) AttributionSession() (string, int) {
	if cs == nil {
		return "", 0
	}
	return cs.SessionID, cs.turnNo
}

// AttributionProject returns the receipt's workspace root ("" when the session
// has none — a bare test construction, where the event still records the
// command it observed).
func (cs *CortexSession) AttributionProject() string {
	if cs == nil || cs.workspace == nil {
		return ""
	}
	return cs.workspace.Root
}

// ValidateToolCall provides dynamic validation for tool calls beyond config.
// Returns (true, "") if valid, (false, message) if invalid.
func (cs *CortexSession) ValidateToolCall(tc ToolCall) (bool, string) {
	// Issue #102's taint rules live at the tools themselves, not here:
	// write_file / edit_file / remove_path each confine their path with
	// tools.ConfineWrites before touching the filesystem, and RunSubagent
	// hands this session to the child as its ToolDeps — so the in-tool check
	// covers the subagent leg too, and bash pushes are gateShell's floor.
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
