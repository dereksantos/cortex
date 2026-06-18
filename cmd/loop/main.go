package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"runtime/debug"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/dereksantos/cortex/internal/capture"
	intcog "github.com/dereksantos/cortex/internal/cognition"
	"github.com/dereksantos/cortex/internal/journal"
	"github.com/dereksantos/cortex/internal/lineedit"
	"github.com/dereksantos/cortex/internal/storage"
	"github.com/dereksantos/cortex/internal/study"
	"github.com/dereksantos/cortex/pkg/cognition"
	"github.com/dereksantos/cortex/pkg/config"
	"github.com/dereksantos/cortex/pkg/events"
	"github.com/dereksantos/cortex/pkg/llm"
)

/*
TODO (production sequence in docs/loop-production-harness.md):
[x] Scanner animation v1
[x] System prompt
[x] Tool calling v1 (read_file, write_file, bash allowlist)
[x] Basic editing
[x] Bash tool
[x] Tolerate native Qwen XML tool-call format (proxy fallback)
[x] Improve session status line
[x] Improve animation
[x] Timestamp in messages
[x] Study tool (file + dir study via internal/study; dirs default to 3 passes)
[x] Oversized bash output studied, not truncated (spill to .cortex/shell/ + digest)
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

const SystemPrompt = `Your are cortex, a coding agent focused on a continous quality improvement approach that achieves goals by working towards the simplest principled implementation that follows good system design and code design. Use your best judgement to make sound decisions that favour excellent outcomes over time. Use the provided tools to inspect files before answering.`

const RoleUser = "user"
const RoleSystem = "system"
const RoleTool = "tool"
const ModelCoder = "coder"

const FunctionReadFile = "read_file"
const FunctionWriteFile = "write_file"
const FunctionEditFile = "edit_file"
const FunctionStudy = "study"
const FunctionBash = "bash"
const FunctionRemove = "remove_path"

const defaultRole = RoleUser
const defaultModel = ModelCoder

// maxToolIterations bounds the agentic inner loop so a confused model can't
// spin forever burning tokens. The smallest form of the "bounded" principle.
const maxToolIterations = 100

// maxRepeatedToolCalls bounds how many byte-identical consecutive tool-call
// batches the inner loop tolerates before intervening. A weak model that gets a
// content-free result can otherwise re-issue the same call until
// maxToolIterations, burning the whole turn (observed: 68 identical greps in
// one turn, 2026-06-14). On the (maxRepeatedToolCalls-1)th repeat we inject a
// nudge giving the model one chance to change course; on the next we abort.
const maxRepeatedToolCalls = 3

// maxToolOutput caps how much tool output we feed back into context, so a
// `cat` of a huge file (or `find` over a big tree) can't blow the window.
const maxToolOutput = 10000

// requestTimeout caps one model call end-to-end. Local generation can be slow,
// so it's generous — Ctrl-C is the interactive escape hatch; this catches a
// server that accepted the request and will never answer.
const requestTimeout = 10 * time.Minute

// maxSendAttempts bounds retries of one model call. Only transient failures
// (transport errors, 429/5xx) retry; a 4xx means the request itself is wrong
// (e.g. context overflow) and retrying can't fix it.
const maxSendAttempts = 3

// retryBackoff is the base delay between attempts (attempt × retryBackoff).
// A var so tests can shrink it.
var retryBackoff = 500 * time.Millisecond

// compactThreshold is the window-fill ratio where the gauge goes red and the
// turn-boundary auto-compact fires. One number, shared, so what the user sees
// (red) and what the harness does (compact) can't drift apart.
const compactThreshold = 0.8

// compactPasses: deepening passes for the compaction study. The digest budget
// is a fraction of the transcript, so a second pass (covering NEW regions)
// roughly doubles conversation coverage for one extra bounded call.
const compactPasses = 2

// dirStudyPasses: default deepening passes when the study target is a
// directory. A corpus boundary is far larger than one file's, so a single
// window-budget pass sees only a sliver of the tree; the curator still ends
// the loop early (DONE / exhausted), so this is a cap, not a floor. Files
// keep the 1-pass default — their deepening loop is the agent re-calling
// study with a goal or the model passing passes explicitly.
const dirStudyPasses = 3

// compactGoal steers the compaction study toward what a continuing session
// needs — state over narrative, recent and unresolved over settled.
const compactGoal = "Summarize this coding session for continuation: the user's task and intent, " +
	"decisions made and why, files read or edited (exact paths), commands run and their key results, " +
	"the current state of the work, and anything unresolved. Prefer recent and open items over settled ones."

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

// defaultEndpoint is a NEUTRAL local fallback — the conventional LiteLLM port —
// so a fresh checkout runs against a local proxy. Real backend addresses and any
// auth never live in source: they come from gitignored .cortex/config.json
// (the backend block) or the CORTEX_BACKEND env var. The chat path appends
// /v1/chat/completions and the study client appends /v1; discovery hits
// /model/info, which is NOT under /v1 — all three derive from this one root.
const defaultEndpoint = "http://localhost:4000"

// Model roles. The harness routes each kind of work to a model binding: the
// coding turn uses "code", the study tool uses "study". One mechanism — new
// nodes (think/dream/dag) just add roles. See Config.Spec. Only code and study
// are exercised today; the rest are baked so config and future DAG nodes can
// resolve them against the discovered fleet.
const (
	roleCode     = "code"      // the coding turn (big agent)
	roleHardCode = "hard-code" // harder coding, no-thinking variant
	roleReason   = "reason"    // deliberate planning/reasoning
	roleFast     = "fast"      // quick, direct answers
	roleStudy    = "study"     // the study tool (read-heavy, think-light)
	roleEmbed    = "embed"     // embeddings
	roleRerank   = "rerank"    // reranking (direct, not via LiteLLM)
	roleTools    = "tools"     // function-calling / tool selection
)

// rolePolicy is the only source-resident routing knowledge for a logical role:
// which backend role tag it draws from, how to break ties when several models
// share that tag, and whether to default built-in thinking off. No model NAMES
// or WINDOWS live here — names are selected from discovery by capability
// (selectModel) and windows come from each model's max_input_tokens. Add a model
// to the backend and the matching role picks it up; rename one and nothing here
// changes.
//
//   - preferExperimental: among same-tag models pick the experimental one
//     (hard-code wants coder80; code wants the stable coder — the zero value).
//   - preferSwapFree: prefer a model with no swap_group, i.e. its own silicon.
//     reason/study want this because they run alongside the coder (swap_group
//     "igpu-8080"); a same-group reasoner would evict/reload coder every turn
//     (the brief's "don't alternate in a tight loop"). reasoner-npu is swap-free.
//   - thinkingOff: code/fast/study are bounded micro-calls where built-in
//     reasoning starves the completion budget (measured: a reasoner burned a
//     full max_tokens on reasoning_content and returned empty content). reason
//     leaves it on so the model deliberates. The enable_thinking kwarg only
//     reaches thinking-capable models; applyFleet drops it for the rest.
type rolePolicy struct {
	tag                string
	preferExperimental bool
	preferSwapFree     bool
	thinkingOff        bool
}

var rolePolicies = map[string]rolePolicy{
	roleCode:     {tag: "coder", thinkingOff: true},
	roleHardCode: {tag: "coder", preferExperimental: true},
	roleReason:   {tag: "reasoner", preferSwapFree: true},
	roleFast:     {tag: "fast", thinkingOff: true},
	roleStudy:    {tag: "reasoner", preferSwapFree: true, thinkingOff: true},
	roleEmbed:    {tag: "embedder"},
	roleRerank:   {tag: "reranker"},
	roleTools:    {tag: "tool"},
}

// selectModel picks the backend model for a logical role from the discovered
// fleet by capability: the model whose role tag matches, best tiebreak score
// wins (name breaks exact ties, deterministically). Returns "" when the fleet
// can't satisfy the role (or is nil) — the caller then relies on a config-pinned
// model. This is what lets the harness route without baking model names: e.g.
// study auto-falls-back from reasoner-npu to reasoner if the NPU model vanishes.
func selectModel(fleet Fleet, role string) string {
	pol, ok := rolePolicies[role]
	if !ok {
		return ""
	}
	best, bestScore := "", -1
	for name, info := range fleet {
		if info.Role != pol.tag {
			continue
		}
		score := 0
		if info.Experimental == pol.preferExperimental {
			score += 2
		}
		if pol.preferSwapFree && info.SwapGroup == "" {
			score++
		}
		if score > bestScore || (score == bestScore && (best == "" || name < best)) {
			best, bestScore = name, score
		}
	}
	return best
}

// fallbackWindow is the gauge/budget default used only when a model's real
// window is unknown — discovery down AND no config-pinned window. Cosmetic in
// practice: when the backend is reachable, discovery supplies the true window.
const fallbackWindow = 32768

// thinkingOff exists so role bindings can take a *bool address.
var thinkingOff = false

// promptGlyph is the input affordance at the end of the status line.
const promptGlyph = "❯"

const red = "\033[31m"
const cyan = "\033[36m"
const green = "\033[32m"
const black = "\033[30m"
const blue = "\033[34m"
const yellow = "\033[33m"
const gray = "\033[90m" // bright black, for dim status text
const reset = "\033[0m" // Reset to default color

// colorDisabled honors the NO_COLOR convention (https://no-color.org): any
// non-empty NO_COLOR strips ANSI from every withColor call. Read once at
// startup — the env doesn't change mid-session.
var colorDisabled = os.Getenv("NO_COLOR") != ""

func withColor(v string, c string) string {
	if colorDisabled {
		return v
	}
	return fmt.Sprintf("%s%s%s", c, v, reset)
}

// spinnerChars is the sequence of frames for the in-place spinner.
var spinnerChars = []rune("⠋⠙⠹⠸⠼⠴⠦⠧⠇⠏")

// Spinner renders a simple in-place rotating-character animation on stdout
// while we wait on the model. It uses a single mutex to serialize all stdout
// writes (spinner goroutine + main thread), so no frame can ever interleave
// with real output. Stop() blocks until the goroutine has actually exited and
// then erases the line, so no frame can bleed into output printed afterward.
type Spinner struct {
	stopChan chan struct{}
	doneChan chan struct{}
	mu       sync.Mutex // serializes all stdout writes + guards label
	label    string     // optional suffix (already colored) shown after the glyph
}

func NewSpinner() *Spinner { return &Spinner{} }

// SetLabel updates the text shown after the spinner glyph (e.g. a live
// "thinking…" reasoning tail). The string is printed verbatim, so callers apply
// their own color/truncation. Safe to call from another goroutine.
func (s *Spinner) SetLabel(label string) {
	s.mu.Lock()
	s.label = label
	s.mu.Unlock()
}

func (s *Spinner) Start() {
	s.stopChan = make(chan struct{})
	s.doneChan = make(chan struct{})
	idx := 0
	go func() {
		defer close(s.doneChan)
		ticker := time.NewTicker(90 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-s.stopChan:
				return
			case <-ticker.C:
				s.mu.Lock()
				glyph := withColor(string(spinnerChars[idx%len(spinnerChars)]), cyan)
				if s.label != "" {
					// \033[K clears any residue when the label shrinks.
					fmt.Printf("\r%s %s\033[K", glyph, s.label)
				} else {
					fmt.Printf("\r%s\033[K", glyph)
				}
				s.mu.Unlock()
				idx++
			}
		}
	}()
}

// Stop halts the spinner, waits for its goroutine to exit, then erases the
// line (\r + clear-to-end-of-line) so the cursor is clean for the next print.
func (s *Spinner) Stop() {
	close(s.stopChan)
	<-s.doneChan
	s.mu.Lock()
	fmt.Print("\r\033[K")
	s.mu.Unlock()
}

// AgentRequest captures parameters to be sent to the agent via API call.
type AgentRequest struct {
	Model string `json:"model"`
	// TODO(derek.s): Rename this to Journal once basic repl is established and integrate with journalling engine.
	Messages    []Message `json:"messages"`
	Temperature float64   `json:"temperature"`
	Tools       []Tool    `json:"tools,omitempty"`
	// ChatTemplateKwargs passes variables to the server-side chat template
	// (llama.cpp via LiteLLM honors it; unknown variables are ignored). Used to
	// disable built-in reasoning on hybrid thinking models — see
	// ModelSpec.TemplateKwargs.
	ChatTemplateKwargs map[string]any `json:"chat_template_kwargs,omitempty"`
	// BaseURL is the endpoint root (e.g. http://localhost:4000), resolved from
	// config. Not serialized — it's transport, not request body.
	BaseURL string `json:"-"`
	// APIKey is the Bearer token for endpoints that need one (e.g. OpenRouter).
	// Empty for local endpoints. Not serialized.
	APIKey string `json:"-"`
	// EphemeralSystem is per-turn context (e.g. retrieved memory) merged into
	// the system message ONLY for the wire payload — never stored in Messages,
	// so it doesn't accumulate across turns or persist. Set before a turn,
	// cleared after. The durable record of what was retrieved lives in the
	// transcript as a separate labelled entry, not here.
	EphemeralSystem string `json:"-"`

	// Stream and StreamOptions are set only on the streaming payload (SendStream);
	// omitempty keeps the blocking request byte-identical to before.
	Stream        bool           `json:"stream,omitempty"`
	StreamOptions *streamOptions `json:"stream_options,omitempty"`
	// Usage opts into OpenRouter's cost reporting (usage:{include:true}); set
	// only for OpenRouter so local backends never see an unknown field.
	Usage *usageInclude `json:"usage,omitempty"`
}

// usageInclude is OpenRouter's request-side flag to return dollar cost in the
// response usage object.
type usageInclude struct {
	Include bool `json:"include"`
}

// streamOptions toggles OpenAI's include_usage so the streamed response ends
// with a chunk carrying token counts (otherwise streaming reports no usage,
// and the context gauge would never update).
type streamOptions struct {
	IncludeUsage bool `json:"include_usage"`
}

// wireMessages returns the messages to send: Messages as-is, or — when an
// ephemeral per-turn note is set — a copy with that note folded onto the LAST
// USER message. The stored Messages are never mutated, so nothing accumulates
// and the transcript stays clean.
//
// Crucially the note rides the current user turn, NOT the system message
// (position 0). The retrieved note differs every turn; anything before the
// variable tail of the prompt that changes per turn invalidates the backend's
// prefix/KV cache from that point on. Folding it into the system message meant
// position 0 changed every turn → a full re-prefill from token 0, every turn.
// Attaching it to the last user message instead keeps [system][tools][prior
// history] byte-identical across turns, so the backend's prefix cache
// (llama-server LCP match, DeepSeek/GLM auto-cache) reuses the whole history and
// only the current turn re-prefills. Editing an existing message's content
// (rather than appending a new system/user message) keeps this portable across
// every chat template — no second system slot, no role-alternation surprise.
func (r *AgentRequest) wireMessages() []Message {
	if r.EphemeralSystem == "" || len(r.Messages) == 0 {
		return r.Messages
	}
	out := make([]Message, len(r.Messages))
	copy(out, r.Messages)
	// Fold onto the last user message — during a turn that's this turn's user
	// prompt, and it stays put as tool-call/result messages append after it, so
	// the note's position is stable across the inner tool loop too.
	for i := len(out) - 1; i >= 0; i-- {
		if out[i].Role == RoleUser {
			out[i].Content = out[i].Content + "\n\n" + r.EphemeralSystem
			return out
		}
	}
	// No user message to carry it (unexpected mid-turn): leave the prompt
	// unchanged rather than poisoning the cacheable system prefix.
	return out
}

// applyPromptCache marks Anthropic prompt-cache breakpoints on the wire messages
// so the stable prefix is billed at ~10% on a hit. It pairs with the
// tail-injection in wireMessages: that keeps the prefix byte-stable;
// llama-server/DeepSeek/GLM then auto-cache it, but Anthropic only caches at
// explicit cache_control breakpoints, so anthropic/* models need this. No-op for
// every other model (which auto-cache and would not expect cache_control).
//
// Two breakpoints (Anthropic allows up to 4): the system message (large, fully
// static, shared across every turn and session), and the message just before
// this turn's user message — i.e. the end of prior history, which only grows by
// append and so stays a cross-turn cache hit. The current turn (user note + its
// tool loop) is new and intentionally uncached. Mutates the passed slice, which
// is already an ephemeral wire copy.
func applyPromptCache(msgs []Message, model string) {
	if !strings.HasPrefix(model, "anthropic/") || len(msgs) == 0 {
		return
	}
	ephemeral := &cacheControl{Type: "ephemeral"}
	msgs[0].cache = ephemeral
	for i := len(msgs) - 1; i >= 1; i-- {
		if msgs[i].Role == RoleUser {
			if i-1 >= 1 { // skip when it would only re-mark the system message
				msgs[i-1].cache = ephemeral
			}
			break
		}
	}
}

type Tool struct {
	Type     string       `json:"type"`
	Function ToolFunction `json:"function"`
}

type ToolFunction struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Parameters  map[string]any `json:"parameters"`
}

// objectSchema builds a JSON Schema "object" with the given properties and
// required fields. Keeps the tool definitions readable instead of nesting
// map[string]any by hand.
func objectSchema(props map[string]any, required ...string) map[string]any {
	return map[string]any{
		"type":       "object",
		"properties": props,
		"required":   required,
	}
}

func stringProp(desc string) map[string]any {
	return map[string]any{"type": "string", "description": desc}
}

func boolProp(desc string) map[string]any {
	return map[string]any{"type": "boolean", "description": desc}
}

func newTool(name, desc string, params map[string]any) Tool {
	return Tool{Type: "function", Function: ToolFunction{Name: name, Description: desc, Parameters: params}}
}

var readFile = newTool(FunctionReadFile,
	"Read the whole contents of a file. Only for files that fit the context window "+
		"— large files are refused; use study for those.",
	objectSchema(map[string]any{
		"path": stringProp("Path to the file to read, relative to the working directory."),
	}, "path"))

var writeFile = newTool(FunctionWriteFile,
	"Write content to a file at the given path, creating or overwriting it.",
	objectSchema(map[string]any{
		"path":    stringProp("Path to the file to write."),
		"content": stringProp("The full contents to write to the file."),
	}, "path", "content"))

var editFile = newTool(FunctionEditFile,
	"Replace text in a file. Matching is exact first; if that finds nothing it "+
		"retries ignoring leading/trailing whitespace, so indentation needn't be "+
		"byte-perfect. old_string must still resolve to exactly one place unless "+
		"replace_all is set. Prefer this over write_file for changes to an existing "+
		"file. To make several changes at once, pass an `edits` array — they apply "+
		"in order and atomically (all succeed or the file is left untouched).",
	objectSchema(map[string]any{
		"path":        stringProp("Path to the file to edit."),
		"old_string":  stringProp("Text to find (single edit). Include enough context to be unique; indentation may differ from the file."),
		"new_string":  stringProp("Replacement text (single edit). May be empty to delete old_string."),
		"replace_all": boolProp("Replace every occurrence instead of requiring a unique match. Default false."),
		"edits": map[string]any{
			"type":        "array",
			"description": "Optional: multiple edits applied in order, atomically. When set, the top-level old_string/new_string are ignored.",
			"items": objectSchema(map[string]any{
				"old_string":  stringProp("Text to find; indentation may differ from the file."),
				"new_string":  stringProp("Replacement text; may be empty to delete."),
				"replace_all": boolProp("Replace every occurrence. Default false."),
			}, "old_string", "new_string"),
		},
	}, "path"))

var studyTool = newTool(FunctionStudy,
	"Study a file or directory and return curated context: a size-adaptive, "+
		"relevance-deepening digest with cited file:line ranges. Prefer this over "+
		"read_file for large files, for understanding whole packages/directories, or "+
		"when you want to understand something relative to a goal. Small targets are "+
		"returned whole (a directory as every file inlined under path headers).",
	objectSchema(map[string]any{
		"path":   stringProp("Path to the file or directory to study."),
		"goal":   stringProp("What you want to learn; guides which regions get deepened."),
		"passes": map[string]any{"type": "integer", "description": "Deepening passes (more = denser coverage of relevant regions, but slower). Default 1 for files, 3 for directories."},
	}, "path"))

// Dynamic study sizing — no hardcoded breakpoints.

// studyFallbackWindow is the conservative window assumed only until a model's
// real size is known (from config or learned at runtime).
const studyFallbackWindow = 8192

// The study tool runs at auto density (chunks=0 → engine-derived): the
// engine sizes chunks to the format's coherence unit and draws enough of
// them to fill the window — maximum breadth at unit granularity, derived
// from the model window and the data's shape, never hardcoded.

// learnedWindows caches context windows discovered from overflow errors at run
// time (model → tokens), so a wrong guess self-corrects after one failure.
var learnedWindows = map[string]int{}

// ctxSizeRe pulls the real context size out of a provider overflow error, e.g.
// "available context size (32768 tokens)".
var ctxSizeRe = regexp.MustCompile(`context size \((\d+) tokens\)`)

func parseCtxSize(s string) int {
	if m := ctxSizeRe.FindStringSubmatch(s); m != nil {
		n, _ := strconv.Atoi(m[1])
		return n
	}
	return 0
}

// studyWindow resolves the study model's context window: explicit env
// override > learned at runtime > configured > fallback.
//
// CORTEX_LOOP_STUDY_WINDOW is the experiment knob: a smaller window
// lowers the read-vs-study threshold so files that would pass through
// whole get studied instead — required for recursion experiments
// (studying digests of digests) where the corpus is small but a digest
// is the point.
func (cs *CortexSession) studyWindow() int {
	if v := os.Getenv("CORTEX_LOOP_STUDY_WINDOW"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	if w, ok := learnedWindows[cs.Study.Model]; ok {
		return w
	}
	if cs.Study.Window > 0 {
		return cs.Study.Window
	}
	return studyFallbackWindow
}

var bash = newTool(FunctionBash,
	"Run a shell command. Only allowlisted commands are permitted; no pipes or redirects.",
	objectSchema(map[string]any{
		"command": stringProp("The command to run, e.g. 'go test ./...' or 'ls cmd'."),
	}, "command"))

var removeTool = newTool(FunctionRemove,
	"Delete a file or directory (recursively). Confined to the workspace: paths "+
		"that escape it, the workspace root itself, and .git/.cortex are refused. "+
		"In a git repo prefer `git rm` for tracked files; use this for untracked "+
		"files or directories.",
	objectSchema(map[string]any{
		"path": stringProp("Path to delete, relative to the working directory."),
	}, "path"))

var tools = []Tool{readFile, writeFile, editFile, studyTool, bash, removeTool}

// httpClient is shared by all model calls. The timeout is the backstop guard:
// without it a server that accepts the request and never answers hangs the
// REPL forever.
var httpClient = &http.Client{Timeout: requestTimeout}

// Send runs one model call with bounded retry. Transient failures (transport
// errors, 429/5xx) retry up to maxSendAttempts with linear backoff; anything
// else — including a canceled ctx — returns immediately.
func (r *AgentRequest) Send(ctx context.Context) (*AgentResponse, error) {
	// Marshal a shallow copy with composed wire messages, so a per-turn
	// ephemeral note reaches the model without mutating stored Messages.
	payload := *r
	payload.Messages = r.wireMessages()
	applyPromptCache(payload.Messages, r.Model)
	b, err := json.Marshal(&payload)
	if err != nil {
		return nil, fmt.Errorf("error marshaling agent request: %w", err)
	}

	base := r.BaseURL
	if base == "" {
		base = defaultEndpoint
	}
	url := llm.NormalizeBaseURL(base) + "/chat/completions"

	var lastErr error
	for attempt := 1; attempt <= maxSendAttempts; attempt++ {
		if attempt > 1 {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(time.Duration(attempt-1) * retryBackoff):
			}
		}
		res, retryable, err := r.sendOnce(ctx, url, b)
		if err == nil {
			return res, nil
		}
		if !retryable || ctx.Err() != nil {
			return nil, err
		}
		lastErr = err
	}
	return nil, fmt.Errorf("model call failed after %d attempts: %w", maxSendAttempts, lastErr)
}

// sendOnce performs a single HTTP round trip. retryable reports whether the
// failure is transient (worth another attempt): transport errors and 429/5xx
// are; everything else isn't. A canceled ctx also surfaces as a transport
// error — the caller's ctx.Err() check stops the retry loop for that case.
func (r *AgentRequest) sendOnce(ctx context.Context, url string, body []byte) (res *AgentResponse, retryable bool, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, false, fmt.Errorf("error building agent request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	llm.SetAttribution(req.Header)
	if r.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+r.APIKey)
	}

	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, true, fmt.Errorf("error executing agent request: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, true, fmt.Errorf("error reading agent response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		transient := resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500
		return nil, transient, fmt.Errorf("agent returned %d: %s", resp.StatusCode, string(respBody))
	}

	var response AgentResponse
	if err := json.Unmarshal(respBody, &response); err != nil {
		return nil, false, fmt.Errorf("error unmarshaling agent response: %w", err)
	}
	return &response, false, nil
}

// SendStream runs one model call over SSE, invoking onContent for each prose
// fragment as it streams in, and assembles the result into the same
// *AgentResponse shape Send returns — so Resolve's downstream logic (tool-call
// handling, token accounting, history append) is identical either way. Retry
// mirrors Send but only applies before the first byte reaches the terminal:
// once onContent has fired we're committed, since retrying would double-print.
func (r *AgentRequest) SendStream(ctx context.Context, onContent, onReasoning func(string)) (*AgentResponse, error) {
	payload := *r
	payload.Messages = r.wireMessages()
	applyPromptCache(payload.Messages, r.Model)
	payload.Stream = true
	payload.StreamOptions = &streamOptions{IncludeUsage: true}
	b, err := json.Marshal(&payload)
	if err != nil {
		return nil, fmt.Errorf("error marshaling agent request: %w", err)
	}

	base := r.BaseURL
	if base == "" {
		base = defaultEndpoint
	}
	url := llm.NormalizeBaseURL(base) + "/chat/completions"

	// requestTimeout bounds only time-to-first-byte here, never the whole
	// stream — a long generation must not be killed by a total-request deadline.
	hc := llm.StreamHTTPClient(requestTimeout)

	var started bool
	guarded := func(s string) {
		started = true
		if onContent != nil {
			onContent(s)
		}
	}

	var lastErr error
	for attempt := 1; attempt <= maxSendAttempts; attempt++ {
		if attempt > 1 {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(time.Duration(attempt-1) * retryBackoff):
			}
		}
		res, err := llm.StreamChat(ctx, hc, url, r.APIKey, b, guarded, onReasoning)
		if err == nil {
			return assembleStreamResponse(res), nil
		}
		// Never retry once we've printed anything, when the ctx is done, or for
		// non-transient failures (4xx, overflow). Only transport blips and
		// 429/5xx before first byte are worth another attempt.
		if started || ctx.Err() != nil || !retryableStreamErr(err) {
			return nil, err
		}
		lastErr = err
	}
	return nil, fmt.Errorf("model call failed after %d attempts: %w", maxSendAttempts, lastErr)
}

// assembleStreamResponse maps the streamed aggregate into the wire AgentResponse
// shape: a single assistant Choice carrying the content, reconstructed tool
// calls, finish reason, and token usage.
func assembleStreamResponse(res llm.StreamResult) *AgentResponse {
	calls := make([]ToolCall, 0, len(res.ToolCalls))
	for _, tc := range res.ToolCalls {
		typ := tc.Type
		if typ == "" {
			typ = "function"
		}
		calls = append(calls, ToolCall{
			ID:       tc.ID,
			Type:     typ,
			Function: FunctionCall{Name: tc.Name, Arguments: tc.Arguments},
		})
	}
	return &AgentResponse{
		Choices: []Choice{{
			Index:        0,
			Message:      Message{Role: "assistant", Content: res.Content, ToolCalls: calls},
			FinishReason: res.FinishReason,
		}},
		Usage: Usage{
			PromptTokens:     res.Stats.InputTokens,
			CompletionTokens: res.Stats.OutputTokens,
			TotalTokens:      res.Stats.TotalTokens(),
			Cost:             res.Stats.CostUSD,
		},
	}
}

// retryableStreamErr classifies a streaming failure as transient — transport
// errors and 429/5xx, matching sendOnce's retry policy. Overflow and other 4xx
// are not retried (they fail identically on retry); the caller surfaces them so
// the overflow-recovery path can learn the real window.
func retryableStreamErr(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "request failed") ||
		strings.Contains(msg, "stream status 429") ||
		strings.Contains(msg, "stream status 5")
}

// AgentResponse captures the agents response from an AgentRequest
type AgentResponse struct {
	ID      string   `json:"id"`
	Object  string   `json:"object"`
	Created int64    `json:"created"`
	Model   string   `json:"model"`
	Choices []Choice `json:"choices"`
	Usage   Usage    `json:"usage"`
}

// Usage captures token counts for the agent request and response
type Usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
	// Cost is the dollar cost of the call, returned by OpenRouter when the
	// request enables usage accounting (see usageInclude). Zero otherwise.
	Cost float64 `json:"cost"`
}

// Choice represents the model response(s)
type Choice struct {
	Index        int     `json:"index"`
	Message      Message `json:"message"`
	FinishReason string  `json:"finish_reason"`
}

// Message contains a single prompt and the role (who said it)
type Message struct {
	// Who said it
	Role string `json:"role"`
	// What they said
	Content string `json:"content"`

	// ToolCalls is set on an assistant message when the model wants tools run.
	ToolCalls []ToolCall `json:"tool_calls,omitempty"`
	// ToolCallID links a role:"tool" result back to the call it answers.
	ToolCallID string `json:"tool_call_id,omitempty"`

	// cache, when set, marks this message as an Anthropic prompt-cache
	// breakpoint on the WIRE only — never stored, never persisted. Unexported so
	// encoding/json ignores it everywhere except MarshalJSON below, which is what
	// keeps the transcript byte-identical to before.
	cache *cacheControl
}

// cacheControl is an Anthropic prompt-cache breakpoint: everything up to and
// including the block it sits on is cached (read at ~10% on a hit, 5-min TTL).
// OpenRouter passes it through to anthropic/* models; other backends auto-cache
// on prefix and don't need it, so it's only emitted for those models.
type cacheControl struct {
	Type string `json:"type"` // "ephemeral"
}

// contentPart is the structured content form Anthropic requires to carry a
// cache_control breakpoint — a plain string content field can't hold one.
type contentPart struct {
	Type         string        `json:"type"` // "text"
	Text         string        `json:"text"`
	CacheControl *cacheControl `json:"cache_control,omitempty"`
}

// MarshalJSON emits the normal string-content shape (byte-identical to the
// default, so transcripts and captures are unaffected) unless a cache breakpoint
// is set — then content becomes a single text part carrying cache_control, the
// only shape Anthropic accepts a breakpoint on.
//
// Pointer receiver on purpose: sessionEntry embeds Message anonymously, and a
// VALUE-receiver MarshalJSON would promote to sessionEntry and clobber its own
// fields (ts/kind/query/…) on the transcript. A pointer method does not promote
// to the embedding value, so transcript serialization stays default. On the wire
// the messages are addressable slice elements, so encoding/json still invokes it.
func (m *Message) MarshalJSON() ([]byte, error) {
	if m.cache == nil {
		type alias Message // drops MarshalJSON (no recursion); unexported cache is ignored
		return json.Marshal(alias(*m))
	}
	return json.Marshal(struct {
		Role       string        `json:"role"`
		Content    []contentPart `json:"content"`
		ToolCalls  []ToolCall    `json:"tool_calls,omitempty"`
		ToolCallID string        `json:"tool_call_id,omitempty"`
	}{
		Role:       m.Role,
		Content:    []contentPart{{Type: "text", Text: m.Content, CacheControl: m.cache}},
		ToolCalls:  m.ToolCalls,
		ToolCallID: m.ToolCallID,
	})
}

type ToolCall struct {
	ID       string       `json:"id"`
	Type     string       `json:"type"`
	Function FunctionCall `json:"function"`
}

// Execute dispatches a tool call. ctx cancels long-running tools (bash, study)
// on interrupt; cs carries session config (model, endpoint, window) that some
// tools need — study does; the file tools ignore both.
func (tc ToolCall) Execute(ctx context.Context, cs *CortexSession) (string, error) {
	name := tc.Function.Name
	switch name {
	case FunctionReadFile:
		return tc.ReadFile(cs)
	case FunctionWriteFile:
		return tc.WriteFile()
	case FunctionEditFile:
		return tc.EditFile()
	case FunctionStudy:
		return tc.Study(ctx, cs)
	case FunctionBash:
		return tc.Bash(ctx, cs)
	case FunctionRemove:
		return tc.RemovePath(cs)
	}
	return "", fmt.Errorf(`no available tools matching name "%s"`, name)
}

// Study runs the real study engine (internal/study) over a file and returns
// curated context: a size-adaptive, relevance-deepening digest with cited line
// ranges, or the whole file when it fits the window. Inference and curation are
// backed by an OpenAI-compatible provider pointed at the session's endpoint.
func (tc ToolCall) Study(ctx context.Context, cs *CortexSession) (string, error) {
	path, err := tc.stringArg("path")
	if err != nil {
		return "", err
	}
	goal, _ := tc.stringArg("goal") // optional
	passes := 0
	if p, ok := tc.intArg("passes"); ok && p > 0 {
		passes = p
	}
	if passes == 0 {
		passes = defaultStudyPasses(path)
	}
	plural := ""
	if passes != 1 {
		plural = "es"
	}
	printToolAction(fmt.Sprintf("study(%s) via %s (%d pass%s)", path, cs.Study.Model, passes, plural))

	res, err := cs.runStudy(ctx, path, goal, passes, 0, 0, nil, 0)
	if err != nil {
		return "", err
	}
	return renderStudyResult(res), nil
}

// defaultStudyPasses picks the pass count when the model didn't ask for one:
// 1 for files, dirStudyPasses for directories (see the const's rationale).
func defaultStudyPasses(path string) int {
	if fi, err := os.Stat(path); err == nil && fi.IsDir() {
		return dirStudyPasses
	}
	return 1
}

// runStudy executes the study engine over one file and returns the structured
// result. Shared by the study tool, the study-eval runner, and compaction.
// Delegates to the STUDY model in its own context (the small-model-amplifier
// split: a cheap model reads, the coding model gets only the curated result
// back). fill is the per-chunk fraction of the window (0 → the engine default,
// 1/8); keep chunks × fill ≤ 1 so one pass's sample fits the window. numbered
// overrides per-line snippet numbering (nil → format default). window, when
// > 0, overrides the consuming-model window the budget derives from (0 → the
// study model's own window) — compaction uses this to size the digest for the
// CODE model rather than for what the study model can hold.
func (cs *CortexSession) runStudy(ctx context.Context, path, goal string, passes, chunks int, fill float64, numbered *bool, window int) (study.StudyLoopResult, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return study.StudyLoopResult{}, fmt.Errorf("resolve %s: %w", path, err)
	}
	base := strings.TrimRight(cs.Study.Endpoint, "/")
	if !strings.HasSuffix(base, "/v1") {
		base += "/v1"
	}
	provider := llm.NewOpenAICompatClient(llm.EndpointConfig{
		Name:               "study",
		BaseURL:            base,
		APIKey:             keychainKey(cs.Study.KeyService),
		ChatTemplateKwargs: cs.Study.TemplateKwargs(),
		// Study sends full-budget samples; prefill alone can exceed the
		// 300s client default on local hardware (measured: the numbered
		// NDJSON auto cell timed out at 300s and completed under 600).
		Timeout: 10 * time.Minute,
	})
	provider.SetModel(cs.Study.Model)
	provider.SetTemperature(0)
	provider.SetMaxTokens(study.CompletionTokenBudget(cs.studyWindow(), 0))

	req := study.StudyRequest{
		Path:     abs,
		RelPath:  path,
		Fill:     fill,
		Goal:     goal,
		Numbered: numbered,
		Infer:    study.ProviderInfer(provider),
	}
	// chunks > 0 pins the per-pass draw (the eval sweep does this);
	// chunks <= 0 leaves Density nil so the engine derives both chunk
	// size (the format's coherence unit) and count (window / unit) —
	// the sample fills the budget as a function of BOTH model and data.
	if chunks > 0 {
		req.Density = chunks
	}
	// Deepening: `passes` runs the study → curate → deepen loop, carrying the
	// covered set forward so each pass samples NEW regions.
	runPasses := func(window int) (study.StudyLoopResult, error) {
		req.Window = study.SampleTokenBudget(window, 0) // shared budget: conservative fraction, overhead-aware
		return study.StudyLoop(ctx, req, study.ModelCurator{Provider: provider}, passes)
	}
	win := window
	if win <= 0 {
		win = cs.studyWindow()
	}
	res, err := runPasses(win)
	// Self-calibrate: if we overflowed, the error states the model's real context
	// size — learn it and retry correctly sized, so the guess never persists.
	if err != nil {
		if real := parseCtxSize(err.Error()); real > 0 && real != win {
			learnedWindows[cs.Study.Model] = real
			res, err = runPasses(real)
		}
	}
	if err != nil {
		return study.StudyLoopResult{}, fmt.Errorf("study %s: %w", path, err)
	}
	return res, nil
}

// renderStudyResult turns the curated study-loop result into the context string
// the harness model consumes. Read mode returns the whole file verbatim;
// otherwise it's the per-pass digests plus provenance-validated citations.
func renderStudyResult(res study.StudyLoopResult) string {
	if res.Stopped == "read" && len(res.Passes) > 0 {
		return res.Passes[0].Response.ReadContent
	}
	var b strings.Builder
	fmt.Fprintf(&b, "coverage %.0f%%, stopped: %s\n", 100*res.CoveragePct, res.Stopped)
	for i, d := range res.Digests {
		if s := strings.TrimSpace(d); s != "" {
			fmt.Fprintf(&b, "\npass %d:\n%s\n", i+1, s)
		}
	}
	if len(res.Citations) > 0 {
		b.WriteString("\ncitations:\n")
		for _, c := range res.Citations {
			fmt.Fprintf(&b, "  %s:%d-%d  %s\n", c.RelPath, c.LineStart, c.LineEnd, c.Claim)
		}
	}
	return strings.TrimSpace(b.String())
}

// printToolAction prints an indented, iconned tool-action line under the
// current cortex turn, e.g. "  ▸ read_file(go.mod)". The tool name shows in
// green; its argument list is dimmed so the verb reads first.
func printToolAction(action string) {
	name, args := action, ""
	if i := strings.IndexByte(action, '('); i >= 0 {
		name, args = action[:i], action[i:]
	}
	line := withColor(iconTool+" "+name, green)
	if args != "" {
		line += withColor(args, gray)
	}
	fmt.Printf("  %s\n", line)
}

func (tc ToolCall) ReadFile(cs *CortexSession) (string, error) {
	path, err := tc.stringArg("path")
	if err != nil {
		return "", err
	}
	// Size guard: a whole-file read of something bigger than half the coding
	// model's window would blow its context. Refuse and redirect to study, which
	// the model can't otherwise be trusted to prefer on its own. (~4 bytes/token.)
	if cs != nil {
		if info, statErr := os.Stat(path); statErr == nil {
			if estTokens := int(info.Size()) / 4; estTokens > cs.windowSize()/2 {
				return "", fmt.Errorf("%s is %d bytes (~%d tokens) — too large to read whole; use study(%q, goal) instead",
					path, info.Size(), estTokens, path)
			}
		}
	}
	printToolAction(fmt.Sprintf("read_file(%s)", path))
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read %s: %w", path, err)
	}
	return string(data), nil
}

func (tc ToolCall) WriteFile() (string, error) {
	path, err := tc.stringArg("path")
	if err != nil {
		return "", err
	}
	content, err := tc.stringArg("content")
	if err != nil {
		return "", err
	}
	printToolAction(fmt.Sprintf("write_file(%s, %d bytes)", path, len(content)))
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		return "", fmt.Errorf("write %s: %w", path, err)
	}
	return fmt.Sprintf("wrote %d bytes to %s", len(content), path), nil
}

// editOp is one find/replace within an edit_file call. Several can be batched
// via the `edits` array and are applied in order to the evolving content.
type editOp struct {
	OldString  string `json:"old_string"`
	NewString  string `json:"new_string"`
	ReplaceAll bool   `json:"replace_all"`
}

// EditFile replaces text in a file. The safety property is a UNIQUE match:
// missing means the edit is wrong, ambiguous means we'd be guessing — both come
// back as observations the model can correct. Matching is exact first, then
// whitespace-tolerant (so a model that mis-indents old_string still lands the
// edit). replace_all relaxes uniqueness for renames; an `edits` array applies
// several changes atomically — if any fails the file is left untouched.
func (tc ToolCall) EditFile() (string, error) {
	var a struct {
		Path       string   `json:"path"`
		OldString  string   `json:"old_string"`
		NewString  string   `json:"new_string"`
		ReplaceAll bool     `json:"replace_all"`
		Edits      []editOp `json:"edits"`
	}
	if s := strings.TrimSpace(tc.Function.Arguments); s != "" {
		if err := json.Unmarshal([]byte(s), &a); err != nil {
			return "", fmt.Errorf("parse edit_file args: %w", err)
		}
	}
	if a.Path == "" {
		return "", fmt.Errorf("path is required")
	}

	edits := a.Edits
	multi := len(edits) > 0
	if !multi {
		edits = []editOp{{OldString: a.OldString, NewString: a.NewString, ReplaceAll: a.ReplaceAll}}
		printToolAction(fmt.Sprintf("edit_file(%s)", a.Path))
	} else {
		printToolAction(fmt.Sprintf("edit_file(%s, %s)", a.Path, countNoun(len(edits), "edit")))
	}

	info, err := os.Stat(a.Path)
	if err != nil {
		return "", fmt.Errorf("stat %s: %w", a.Path, err)
	}
	data, err := os.ReadFile(a.Path)
	if err != nil {
		return "", fmt.Errorf("read %s: %w", a.Path, err)
	}
	content := string(data)

	// Apply all edits in memory; only touch disk if every one succeeds.
	total := 0
	for i, e := range edits {
		updated, n, err := applyEdit(content, e.OldString, e.NewString, e.ReplaceAll)
		if err != nil {
			if multi {
				return "", fmt.Errorf("%s edit %d: %w", a.Path, i+1, err)
			}
			return "", fmt.Errorf("%s: %w", a.Path, err)
		}
		content = updated
		total += n
	}

	if err := os.WriteFile(a.Path, []byte(content), info.Mode()); err != nil {
		return "", fmt.Errorf("write %s: %w", a.Path, err)
	}
	if multi {
		return fmt.Sprintf("edited %s (%s, %s)", a.Path, countNoun(len(edits), "edit"), countNoun(total, "replacement")), nil
	}
	return fmt.Sprintf("edited %s (%s)", a.Path, countNoun(total, "replacement")), nil
}

// countNoun renders "1 edit" / "2 edits" — naive +s pluralization, fine for the
// nouns used here (edit, replacement).
func countNoun(n int, noun string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, noun)
	}
	return fmt.Sprintf("%d %ss", n, noun)
}

// applyEdit performs one find/replace on content, returning the result and the
// number of replacements. It tries an exact substring match first; only when
// that finds nothing does it fall back to whitespace-tolerant matching, so the
// fast path is unchanged and tolerance never overrides an exact hit.
func applyEdit(content, old, new string, replaceAll bool) (string, int, error) {
	if old == "" {
		return "", 0, fmt.Errorf("old_string must not be empty")
	}
	if old == new {
		return "", 0, fmt.Errorf("old_string and new_string are identical; nothing to change")
	}
	if n := strings.Count(content, old); n > 0 {
		if replaceAll {
			return strings.ReplaceAll(content, old, new), n, nil
		}
		if n > 1 {
			return "", 0, fmt.Errorf("old_string found %d times; add surrounding context to make it unique, or set replace_all", n)
		}
		return strings.Replace(content, old, new, 1), 1, nil
	}
	return tolerantEdit(content, old, new, replaceAll)
}

// tolerantEdit matches old against content line-by-line ignoring whitespace,
// then re-indents the replacement to the file's actual indentation. Tier 1
// ignores only trailing whitespace; tier 2 also ignores leading indentation —
// the safer tolerance is tried first. A match must still be unique unless
// replace_all is set.
func tolerantEdit(content, old, new string, replaceAll bool) (string, int, error) {
	fileLines := dropTrailingEmpty(strings.SplitAfter(content, "\n"))
	oldLines := dropTrailingEmpty(strings.SplitAfter(old, "\n"))
	k := len(oldLines)
	if k == 0 || k > len(fileLines) {
		return "", 0, fmt.Errorf("old_string not found%s", nearMissHint(fileLines, oldLines))
	}
	for tier := 1; tier <= 2; tier++ {
		var starts []int
		for i := 0; i+k <= len(fileLines); i++ {
			if windowMatches(fileLines[i:i+k], oldLines, tier) {
				starts = append(starts, i)
			}
		}
		if len(starts) == 0 {
			continue
		}
		if !replaceAll && len(starts) > 1 {
			return "", 0, fmt.Errorf("old_string matches %d places (ignoring whitespace); add context or set replace_all", len(starts))
		}
		return rebuildWithReplacements(fileLines, oldLines, new, starts), len(starts), nil
	}
	return "", 0, fmt.Errorf("old_string not found%s", nearMissHint(fileLines, oldLines))
}

// windowMatches reports whether a run of file lines equals the old block under
// the given tolerance tier.
func windowMatches(win, old []string, tier int) bool {
	for j := range old {
		if lineKey(win[j], tier) != lineKey(old[j], tier) {
			return false
		}
	}
	return true
}

// lineKey normalizes a line for tolerant comparison: tier 1 drops trailing
// whitespace, tier 2 drops leading and trailing whitespace.
func lineKey(line string, tier int) string {
	line = strings.TrimSuffix(line, "\n")
	if tier == 1 {
		return strings.TrimRight(line, " \t")
	}
	return strings.TrimSpace(line)
}

// rebuildWithReplacements substitutes new at each matched start (greedy,
// non-overlapping), re-indenting new from old's base indentation to the file
// region's, and preserving the trailing-newline state of the replaced span.
func rebuildWithReplacements(fileLines, oldLines []string, new string, starts []int) string {
	startSet := make(map[int]bool, len(starts))
	for _, s := range starts {
		startSet[s] = true
	}
	k := len(oldLines)
	oldBase := indentBase(oldLines)
	anchor := firstNonBlank(oldLines)
	var b strings.Builder
	for i := 0; i < len(fileLines); {
		if startSet[i] {
			fileBase := leadingWS(strings.TrimSuffix(fileLines[i+anchor], "\n"))
			repl := swapIndent(new, oldBase, fileBase)
			lastHadNL := strings.HasSuffix(fileLines[i+k-1], "\n")
			if lastHadNL && !strings.HasSuffix(repl, "\n") {
				repl += "\n"
			} else if !lastHadNL && strings.HasSuffix(repl, "\n") {
				repl = strings.TrimSuffix(repl, "\n")
			}
			b.WriteString(repl)
			i += k
			continue
		}
		b.WriteString(fileLines[i])
		i++
	}
	return b.String()
}

// swapIndent shifts new's base indentation: on each non-blank line that starts
// with oldBase, that prefix is swapped for fileBase (when oldBase is empty,
// fileBase is prepended). A no-op when the bases already match.
func swapIndent(s, oldBase, fileBase string) string {
	if oldBase == fileBase {
		return s
	}
	lines := strings.SplitAfter(s, "\n")
	for idx, ln := range lines {
		body := strings.TrimSuffix(ln, "\n")
		hadNL := strings.HasSuffix(ln, "\n")
		if strings.TrimSpace(body) == "" {
			continue // leave blank lines untouched
		}
		if strings.HasPrefix(body, oldBase) {
			body = fileBase + strings.TrimPrefix(body, oldBase)
		}
		if hadNL {
			body += "\n"
		}
		lines[idx] = body
	}
	return strings.Join(lines, "")
}

// dropTrailingEmpty removes the empty element SplitAfter leaves when the input
// ends with "\n", so line-window counts are exact.
func dropTrailingEmpty(lines []string) []string {
	if n := len(lines); n > 0 && lines[n-1] == "" {
		return lines[:n-1]
	}
	return lines
}

func firstNonBlank(lines []string) int {
	for i, l := range lines {
		if strings.TrimSpace(l) != "" {
			return i
		}
	}
	return 0
}

func indentBase(lines []string) string {
	return leadingWS(strings.TrimSuffix(lines[firstNonBlank(lines)], "\n"))
}

func leadingWS(s string) string {
	return s[:len(s)-len(strings.TrimLeft(s, " \t"))]
}

// nearMissHint points the model at the file line most similar (by word overlap)
// to old's first meaningful line, so a failed match is fixable in one retry
// instead of looping. Returns "" when nothing is similar enough.
func nearMissHint(fileLines, oldLines []string) string {
	target := ""
	for _, l := range oldLines {
		if t := strings.TrimSpace(strings.TrimSuffix(l, "\n")); t != "" {
			target = t
			break
		}
	}
	tset := wordSet(target)
	if len(tset) == 0 {
		return ""
	}
	bestIdx, bestScore := -1, 0.0
	for i, l := range fileLines {
		body := strings.TrimSpace(strings.TrimSuffix(l, "\n"))
		if body == "" {
			continue
		}
		if s := jaccard(tset, wordSet(body)); s > bestScore {
			bestScore, bestIdx = s, i
		}
	}
	if bestIdx < 0 || bestScore < 0.5 {
		return ""
	}
	line := strings.TrimSpace(strings.TrimSuffix(fileLines[bestIdx], "\n"))
	if len(line) > 80 {
		line = line[:80] + "…"
	}
	return fmt.Sprintf(" — closest is line %d: %q (re-read the file if it changed)", bestIdx+1, line)
}

// wordSet splits text into a set of lowercased alphanumeric tokens.
func wordSet(s string) map[string]bool {
	set := map[string]bool{}
	for _, w := range strings.FieldsFunc(strings.ToLower(s), func(r rune) bool {
		return !('a' <= r && r <= 'z' || '0' <= r && r <= '9')
	}) {
		set[w] = true
	}
	return set
}

func jaccard(a, b map[string]bool) float64 {
	if len(a) == 0 || len(b) == 0 {
		return 0
	}
	inter := 0
	for w := range a {
		if b[w] {
			inter++
		}
	}
	return float64(inter) / float64(len(a)+len(b)-inter)
}

// RemovePath deletes a file or directory, confined to the workspace. It is the
// only deletion path (raw rm is not allowlisted): the confinement — not a
// human prompt — is the safety property, so the tool stays autonomous.
func (tc ToolCall) RemovePath(cs *CortexSession) (string, error) {
	if cs == nil || !cs.allowDelete {
		return "", fmt.Errorf("remove_path is disabled")
	}
	path, err := tc.stringArg("path")
	if err != nil {
		return "", err
	}
	abs, err := confinedPath(cs.deleteRoot, path)
	if err != nil {
		return "", err
	}
	printToolAction(fmt.Sprintf("remove_path(%s)", path))
	if err := os.RemoveAll(abs); err != nil {
		return "", fmt.Errorf("remove %s: %w", path, err)
	}
	return fmt.Sprintf("removed %s", path), nil
}

// confinedPath resolves p against root and verifies it stays inside it. It
// rejects absolute/`..` escapes, the root itself, the protected .git/.cortex
// trees, and symlink escapes (a path whose real parent leaves the root). The
// returned path is absolute and safe to delete.
func confinedPath(root, p string) (string, error) {
	if strings.TrimSpace(p) == "" {
		return "", fmt.Errorf("path must not be empty")
	}
	if root == "" {
		root, _ = os.Getwd()
	}
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return "", fmt.Errorf("resolve root: %w", err)
	}
	abs := filepath.Clean(p)
	if !filepath.IsAbs(abs) {
		abs = filepath.Join(rootAbs, abs)
	}
	rel, err := filepath.Rel(rootAbs, abs)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("path %q is outside the workspace (%s)", p, rootAbs)
	}
	if rel == "." {
		return "", fmt.Errorf("refusing to delete the workspace root")
	}
	top := rel
	if i := strings.IndexRune(rel, filepath.Separator); i >= 0 {
		top = rel[:i]
	}
	if top == ".git" || top == ".cortex" {
		return "", fmt.Errorf("refusing to delete protected path %q", rel)
	}
	// Symlink-escape guard: a lexical in-root path can still point out via a
	// symlinked parent (root/link -> /etc, then "link/x"). Re-check the real
	// parent. The final component may itself be a symlink — RemoveAll deletes
	// the link, not its target, so that's safe.
	if realParent, err := filepath.EvalSymlinks(filepath.Dir(abs)); err == nil {
		rootReal, err2 := filepath.EvalSymlinks(rootAbs)
		if err2 == nil {
			if r, err := filepath.Rel(rootReal, realParent); err != nil || r == ".." || strings.HasPrefix(r, ".."+string(filepath.Separator)) {
				return "", fmt.Errorf("path %q escapes the workspace via a symlink", p)
			}
		}
	}
	return abs, nil
}

// bashAllowlist gates which binaries the bash tool may run. This is a guardrail
// against the model doing something catastrophic by accident — NOT a security
// sandbox. We exec the binary directly (no shell), so `;`, `&&`, `|`, `>` are
// inert: a command is always a single allowlisted binary plus literal args.
var bashAllowlist = map[string]bool{
	// read / search
	"ls": true, "cat": true, "head": true, "tail": true, "wc": true,
	"grep": true, "rg": true, "find": true, "tree": true, "diff": true,
	"stat": true, "which": true, "echo": true, "pwd": true,
	// filesystem management (literal paths only — no shell, no globbing).
	// Deletion is deliberately absent here: it goes through the confined
	// remove_path tool, not raw rm.
	"mkdir": true, "mv": true, "cp": true, "touch": true, "rmdir": true,
	// build / vcs
	"go": true, "git": true, "gofmt": true,
}

// tokenizeCommand splits a command line into argv the way a POSIX shell would
// for the cases we support: it honors single quotes, double quotes, and
// backslash escapes so quoted arguments reach the binary WITHOUT their quote
// characters. strings.Fields did not — `grep -n "Scroller" f` reached grep as
// the literal pattern `"Scroller"` (quotes included), which never matched, so
// the model retried the identical command 68 times before hitting the
// iteration cap (2026-06-14). We still exec the binary directly (no shell), so
// any shell metacharacter that appears *outside* quotes is something we cannot
// honor: tokenize reports the first such bare metacharacter as bareMeta so the
// caller can reject the command with a helpful message instead of silently
// passing a mangled argument. Metacharacters inside quotes are literal and
// allowed (e.g. grep -n "a|b" f searches for the literal a|b).
func tokenizeCommand(command string) (fields []string, bareMeta string, err error) {
	var cur strings.Builder
	inField := false
	flush := func() {
		if inField {
			fields = append(fields, cur.String())
			cur.Reset()
			inField = false
		}
	}
	runes := []rune(command)
	for i := 0; i < len(runes); i++ {
		switch c := runes[i]; {
		case c == '\'':
			inField = true
			for i++; i < len(runes) && runes[i] != '\''; i++ {
				cur.WriteRune(runes[i])
			}
			if i >= len(runes) {
				return nil, "", fmt.Errorf("unterminated single quote in command")
			}
		case c == '"':
			inField = true
			for i++; i < len(runes) && runes[i] != '"'; i++ {
				// Inside double quotes the shell unescapes only a few chars.
				if runes[i] == '\\' && i+1 < len(runes) {
					if n := runes[i+1]; n == '"' || n == '\\' || n == '$' || n == '`' {
						i++
					}
				}
				cur.WriteRune(runes[i])
			}
			if i >= len(runes) {
				return nil, "", fmt.Errorf("unterminated double quote in command")
			}
		case c == '\\':
			if i+1 < len(runes) {
				i++
				cur.WriteRune(runes[i])
				inField = true
			}
		case c == ' ' || c == '\t' || c == '\n':
			flush()
		case c == '|' || c == '>' || c == '<' || c == ';' || c == '&':
			if bareMeta == "" {
				bareMeta = string(c)
			}
		case c == '$' && i+1 < len(runes) && runes[i+1] == '(':
			if bareMeta == "" {
				bareMeta = "$("
			}
			i++ // consume '('
		default:
			cur.WriteRune(c)
			inField = true
		}
	}
	flush()
	return fields, bareMeta, nil
}

func (tc ToolCall) Bash(ctx context.Context, cs *CortexSession) (string, error) {
	command, err := tc.stringArg("command")
	if err != nil {
		return "", err
	}
	fields, bareMeta, err := tokenizeCommand(command)
	if err != nil {
		return "", err
	}
	if len(fields) == 0 {
		return "", fmt.Errorf("empty command")
	}
	// A bare metacharacter is shell syntax we can't honor. Reject explicitly:
	// reaching the binary as a literal arg yields confusing downstream errors
	// (`find . | head` → "find: |: unknown primary") that models retry verbatim
	// instead of adapting (observed: 3 wasted turns, 2026-06-12).
	if bareMeta != "" {
		return "", fmt.Errorf("shell syntax %q is not supported (commands run without a shell — no pipes, redirects, or chaining); run the bare command instead, e.g. %q", bareMeta, fields[0]+" ...")
	}
	if !bashAllowlist[fields[0]] {
		return "", fmt.Errorf("command %q is not in the allowlist", fields[0])
	}
	printToolAction(fmt.Sprintf("bash(%s)", command))

	out, runErr := exec.CommandContext(ctx, fields[0], fields[1:]...).CombinedOutput()
	result := string(out)
	// Oversized output is studied, not lost: the full output spills to
	// .cortex/shell/ and the model gets a cited digest plus the spill path
	// to study deeper. Truncation is only the no-study fallback.
	if len(result) > maxToolOutput {
		if studied, ok := studyShellOutput(ctx, cs, command, out); ok {
			result = studied
		} else {
			result = result[:maxToolOutput] + "\n...[output truncated]"
		}
	}
	// A non-zero exit is an observation, not a harness failure: hand the output
	// and exit error back to the model so it can react.
	if runErr != nil {
		// grep exits 1 to mean "no matches" — a normal, content-free result, not
		// an error. Reported as a bare "[exit error: exit status 1]" it gave the
		// model nothing to act on and it retried the same command in a loop
		// (2026-06-14). Name the empty-result case explicitly. Exit >=2 is a real
		// grep error and keeps its stderr (merged into result by CombinedOutput).
		var exitErr *exec.ExitError
		if errors.As(runErr, &exitErr) && exitErr.ExitCode() == 1 &&
			fields[0] == "grep" && strings.TrimSpace(result) == "" {
			return "(no matches)", nil
		}
		return result + "\n[exit error: " + runErr.Error() + "]", nil
	}
	return result, nil
}

// bashStudyWindow is the consuming-model window the shell-output study is
// sized for, in tokens. Chosen so the engine's read-vs-study threshold
// (window/2 tokens, after sample headroom) sits BELOW maxToolOutput:
// anything big enough to spill is always sampled into a bounded digest,
// never passed through whole — passthrough would re-create the context
// bloat the spill exists to avoid.
const bashStudyWindow = maxToolOutput / 2

// spillShellOutput writes oversized command output under .cortex/shell/,
// content-addressed (same output → same file) so repeated runs of an
// unchanged command don't pile up copies.
func spillShellOutput(command string, out []byte) (string, error) {
	dir := filepath.Join(".cortex", "shell")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	head := "out"
	if f := strings.Fields(command); len(f) > 0 {
		head = f[0]
	}
	sum := sha256.Sum256(out)
	path := filepath.Join(dir, fmt.Sprintf("%s-%s.txt", head, hex.EncodeToString(sum[:])[:12]))
	if err := os.WriteFile(path, out, 0o644); err != nil {
		return "", err
	}
	return path, nil
}

// studyShellOutput turns oversized bash output into curated context instead
// of a truncation: the full output spills to .cortex/shell/ and the study
// engine digests it with real line citations into the spill file, which the
// model can study again (with a goal) to dig deeper. Returns ok=false on any
// failure so the caller degrades to plain truncation — losing the study is
// acceptable, losing the turn is not.
func studyShellOutput(ctx context.Context, cs *CortexSession, command string, out []byte) (string, bool) {
	if cs == nil {
		return "", false
	}
	spill, err := spillShellOutput(command, out)
	if err != nil {
		return "", false
	}
	printToolAction(fmt.Sprintf("output %d KB → study(%s)", len(out)/1024, spill))
	goal := fmt.Sprintf("This is the output of `%s`. What does it show? Surface errors, failures, and anomalies first.", command)
	res, err := cs.runStudy(ctx, spill, goal, 1, 0, 0, nil, bashStudyWindow)
	if err != nil {
		return "", false
	}
	header := fmt.Sprintf("[%d bytes of output — studied below; full output at %s — study(path, goal) to dig deeper]\n", len(out), spill)
	return header + renderStudyResult(res), true
}

// Qwen3-Coder's native tool-call format is XML-ish:
//
//	<tool_call>
//	<function=NAME>
//	<parameter=PNAME>
//	VALUE
//	</parameter>
//	</function>
//	</tool_call>
//
// The proxy usually normalizes this into OpenAI tool_calls, but when it doesn't
// the raw XML leaks into message content with tool_calls empty. These regexes
// let us recover it. The <tool_call> wrapper is optional — we key off <function>.
var (
	fnRe    = regexp.MustCompile(`(?s)<function=([^>\s]+)>(.*?)</function>`)
	paramRe = regexp.MustCompile(`(?s)<parameter=([^>\s]+)>(.*?)</parameter>`)
)

// parseXMLToolCalls extracts Qwen-native tool calls from raw content. Returns
// nil if none are present. Each call is normalized into the same ToolCall shape
// the OpenAI path produces, so it flows through Execute unchanged.
func parseXMLToolCalls(content string) []ToolCall {
	fnMatches := fnRe.FindAllStringSubmatch(content, -1)
	if len(fnMatches) == 0 {
		return nil
	}
	var calls []ToolCall
	for i, fm := range fnMatches {
		name, body := fm[1], fm[2]
		// All current tools take string params, so a string map marshals to the
		// same JSON-string Arguments shape the wire uses. Note: TrimSpace strips
		// the framing newlines Qwen adds — fine for paths/commands, though it
		// would also trim a deliberately trailing newline in file content.
		args := map[string]string{}
		for _, pm := range paramRe.FindAllStringSubmatch(body, -1) {
			args[pm[1]] = strings.TrimSpace(pm[2])
		}
		raw, err := json.Marshal(args)
		if err != nil {
			continue
		}
		calls = append(calls, ToolCall{
			ID:       fmt.Sprintf("xml-%d", i+1),
			Type:     "function",
			Function: FunctionCall{Name: name, Arguments: string(raw)},
		})
	}
	return calls
}

// stripToolMarkup removes Qwen tool-call XML from content so we don't print the
// raw markup after converting it to tool calls. Any genuine prose preamble
// around the markup is preserved.
func stripToolMarkup(s string) string {
	s = fnRe.ReplaceAllString(s, "")
	s = strings.ReplaceAll(s, "<tool_call>", "")
	s = strings.ReplaceAll(s, "</tool_call>", "")
	return strings.TrimSpace(s)
}

type FunctionCall struct {
	Name string `json:"name"`
	// Arguments is a JSON-encoded *string* on the wire (e.g. `{"path":"go.mod"}`),
	// NOT a JSON object. Parse it with stringArg.
	Arguments string `json:"arguments"`
}

// stringArg parses Arguments (a JSON string) and pulls out one string field.
func (tc ToolCall) stringArg(name string) (string, error) {
	var m map[string]any
	if s := strings.TrimSpace(tc.Function.Arguments); s != "" {
		if err := json.Unmarshal([]byte(s), &m); err != nil {
			return "", fmt.Errorf("parse arguments %q: %w", tc.Function.Arguments, err)
		}
	}
	v, ok := m[name].(string)
	if !ok {
		return "", fmt.Errorf("missing or non-string arg %q", name)
	}
	return v, nil
}

// intArg pulls an integer field from Arguments. JSON numbers decode as float64.
// Returns (0, false) when missing or not a number.
func (tc ToolCall) intArg(name string) (int, bool) {
	var m map[string]any
	if s := strings.TrimSpace(tc.Function.Arguments); s != "" {
		if json.Unmarshal([]byte(s), &m) == nil {
			if v, ok := m[name].(float64); ok {
				return int(v), true
			}
		}
	}
	return 0, false
}

func (tc ToolCall) String() string {
	return fmt.Sprintf("wants %s %s %s %v", tc.ID, tc.Type, tc.Function.Name, tc.Function.Arguments)
}

// Message source icons for the print gutter. Single-width; swap freely.
const (
	iconCortex = "◆" // assistant / cortex
	iconTool   = "▸" // tool action
	iconUser   = "❯" // user
)

// gutter returns the icon and color identifying a message's source.
func (m Message) gutter() (icon, color string) {
	switch m.Role {
	case RoleUser:
		return iconUser, cyan
	case RoleTool:
		return iconTool, green
	default: // assistant / cortex
		return iconCortex, blue
	}
}

// gutterPrefix renders the leading "<icon> <timestamp>  ": the source icon in
// its color, the timestamp dim so it reads as metadata, not content. Shared by
// Message.render and the live stream printer so both gutters match.
func gutterPrefix(icon, color string, ts time.Time) string {
	return fmt.Sprintf("%s %s  ", withColor(icon, color), withColor(ts.Format("15:04:05"), gray))
}

// render formats the message as "<icon> HH:MM:SS  <content>" — a colored source
// icon, a dim timestamp, then the content. ts is injected so it's testable.
func (m Message) render(ts time.Time) string {
	icon, color := m.gutter()
	return gutterPrefix(icon, color, ts) + m.Content
}

// Print writes the message to stdout with a timestamped, source-colored gutter.
func (m Message) Print() {
	fmt.Println(m.render(time.Now()))
}

// CortexArgs specifies incoming cli arguments
type CortexArgs []string

// Request constructs a Request struct instance parsed from CortexArgs. The
// system message is the base prompt plus the project's AGENTS.md when one is
// found — the project speaks once, at session start, not per turn.
func (a CortexArgs) Request() *AgentRequest {
	content := SystemPrompt
	if inst := projectInstructions(); inst != "" {
		content += "\n\n# Project instructions (AGENTS.md)\n\n" + inst
	}
	systemMessage := Message{
		Role:    RoleSystem,
		Content: content,
	}
	messages := []Message{systemMessage}
	return &AgentRequest{
		Model:       defaultModel,
		Messages:    messages,
		Temperature: 0,
		Tools:       tools,
	}
}

type CortexSession struct {
	Args    *CortexArgs
	Request *AgentRequest
	// LastPromptTokens is the prompt_tokens from the most recent response.
	// Because we re-send the whole history each call, it equals how full the
	// context window currently is — the live gauge in the status line.
	LastPromptTokens int
	// Window is the code model's context window (status gauge + read_file guard).
	Window int
	// Study is the study role's binding (small long-context model in its own
	// context), resolved from config.
	Study ModelSpec
	// Fleet is the model metadata discovered from the backend at startup (nil
	// when discovery was unavailable). Windows in Request/Study/Window already
	// reflect it; kept for status display and future routing.
	Fleet Fleet
	// Config is the loaded .cortex/config.json (may be nil).
	Config *Config
	// deleteRoot confines remove_path to the workspace (default cwd at startup);
	// allowDelete gates the tool entirely.
	deleteRoot  string
	allowDelete bool
	// quiet mutes all terminal emission for the turn — no spinner, no live
	// streaming, no final-prose echo. Headless drivers (the `loop turn`
	// entrypoint or the Discord adapter) set this and read the
	// reply from TurnResult instead, keeping stdout clean for machine parsing.
	quiet bool
	// SessionID names this session's transcript file; "" when unpersisted.
	SessionID string
	// transcript is the open .cortex/sessions/<id>.jsonl file Append writes
	// through to. nil when the session is unpersisted (study CLI, tests).
	transcript *os.File
	// retriever serves Fast (mechanical) context retrieval from the project's
	// .cortex/ store; nil when retrieval is disabled (no store, or an
	// unpersisted session). store is its backing handle, closed at exit.
	retriever *intcog.Cortex
	store     *storage.Storage
	// capturer writes turn captures to the same store (so they're immediately
	// retrievable) and to the journal (durable). nil when retrieval is disabled.
	capturer *capture.Capture

	// Tier 2 distillation runs off the foreground: completed turns buffer in
	// pendingTurns, a cancelable goroutine distills them during idle, and the
	// next turn preempts it. See the Tier 2 capture section.
	distillMu     sync.Mutex
	pendingTurns  []pendingTurn
	distillCancel context.CancelFunc
	distillDone   chan struct{}

	// Session metrics (6a), cumulative across the session: shown in the
	// closing summary and emitted as one eval.cell_result at exit. tokensIn/Out
	// sum every billed model call (prompt re-sent each call, so summing prompt
	// tokens reflects real cost). insights is written by the distill goroutine,
	// so it's atomic; the rest are main-thread only.
	sessionStart  time.Time
	turns         int
	tokensIn      int
	tokensOut     int
	costUSD       float64 // cumulative dollar cost when the backend reports it
	injectedChars int     // retrieved context merged into the wire (≈ tokens × 4)
	captures      int     // Tier 1 turn captures + /remember
	retrievals    int     // turns that injected retrieved context
	insights      atomic.Int64

	// md is the cached glamour renderer for streamed assistant output, rebuilt
	// when the terminal width changes. nil when rendering is disabled (non-TTY,
	// NO_COLOR, CORTEX_LOOP_RENDER=0) — the raw token-streaming path.
	md      *markdownRenderer
	mdWidth int

	// live is the anchored bottom-row prompt during a turn (interactive +
	// streaming + render only). When set, send() routes the "thinking" status to
	// it instead of the standalone spinner, and turn output is funneled above it.
	live *lineedit.Anchor
}

// markdown returns the session's markdown renderer for the current terminal
// width, building or rebuilding it as needed. Returns nil when rendering is
// disabled or the turn is headless, so streaming falls back to raw bytes.
func (cs *CortexSession) markdown() *markdownRenderer {
	if cs.quiet {
		return nil
	}
	// An anchored turn redirects os.Stdout to a pipe, so renderEnabled() and
	// terminalWidth() (which probe os.Stdout) would misreport — width as 80 and
	// rendering as off. In that mode rendering is on by construction and the
	// width comes from the anchor's real terminal.
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

// SetModel switches the active coding model id. The code endpoint is unchanged
// (models on the same endpoint swap freely); history is preserved.
func (cs *CortexSession) SetModel(model string) { cs.Request.Model = model }

// windowSize is the code model's context window — the gauge denominator and the
// read_file size threshold. Falls back to fallbackWindow only when discovery and
// config both left it unknown.
func (cs *CortexSession) windowSize() int {
	if cs.Window > 0 {
		return cs.Window
	}
	return fallbackWindow
}

// ModelSpec is one role's binding: where to send, which model, how big its window.
// KeyService, when set, names a macOS keychain item whose secret is used as the
// Bearer token (e.g. "cortex-openrouter" for OpenRouter). The key is fetched at
// call time and never written to config or echoed.
type ModelSpec struct {
	Endpoint   string `json:"endpoint"`
	Model      string `json:"model"`
	Window     int    `json:"window"`      // context window in tokens
	KeyService string `json:"key_service"` // keychain service name for the API key, or ""
	// Thinking controls built-in reasoning on hybrid thinking models (e.g. the
	// coder alias). false → requests carry
	// chat_template_kwargs{enable_thinking:false}; nil/true → the model's
	// template default applies.
	Thinking *bool `json:"thinking"`
}

// TemplateKwargs returns the chat-template variables to send for this binding:
// enable_thinking=false when thinking is explicitly disabled, nil otherwise.
func (m ModelSpec) TemplateKwargs() map[string]any {
	if m.Thinking != nil && !*m.Thinking {
		return map[string]any{"enable_thinking": false}
	}
	return nil
}

// ModelInfo is the per-model metadata the LiteLLM backend serves at
// /model/info (LiteLLM-specific; plain /v1/models returns only IDs). We read the
// real context window and routing hints instead of guessing windows.
type ModelInfo struct {
	MaxInput     int    // max_input_tokens — the real context window
	Role         string // coder/reasoner/fast/reranker/embedder/tool
	Silicon      string // igpu/npu/cpu/macmini-metal
	Thinking     bool   // hybrid thinking model
	SwapGroup    string // models sharing a group are mutually exclusive (one slot)
	AlwaysWarm   bool
	Experimental bool
}

// Fleet maps model_name → ModelInfo, discovered once at startup. A nil Fleet
// means discovery was unavailable; callers fall back to baked windows.
type Fleet map[string]ModelInfo

// fleetDiscoveryTimeout bounds startup discovery so an unreachable backend can't
// stall the REPL — discovery is an enhancement, not a dependency.
const fleetDiscoveryTimeout = 4 * time.Second

// discoverFleet fetches /model/info from the backend root. Best-effort: any
// failure (backend down, non-200, bad JSON) returns nil and the harness falls
// to a config-pinned model + fallbackWindow, exactly like a missing config.
func discoverFleet(ctx context.Context, endpoint string) Fleet {
	ctx, cancel := context.WithTimeout(ctx, fleetDiscoveryTimeout)
	defer cancel()
	url := strings.TrimRight(endpoint, "/") + "/model/info"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil
	}
	res, err := httpClient.Do(req)
	if err != nil {
		return nil
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return nil
	}
	var body struct {
		Data []struct {
			ModelName string `json:"model_name"`
			ModelInfo struct {
				MaxInputTokens int    `json:"max_input_tokens"`
				Role           string `json:"role"`
				Silicon        string `json:"silicon"`
				Thinking       bool   `json:"thinking"`
				SwapGroup      string `json:"swap_group"`
				AlwaysWarm     bool   `json:"always_warm"`
				Experimental   bool   `json:"experimental"`
			} `json:"model_info"`
		} `json:"data"`
	}
	if err := json.NewDecoder(res.Body).Decode(&body); err != nil || len(body.Data) == 0 {
		return nil
	}
	f := make(Fleet, len(body.Data))
	for _, m := range body.Data {
		f[m.ModelName] = ModelInfo{
			MaxInput:     m.ModelInfo.MaxInputTokens,
			Role:         m.ModelInfo.Role,
			Silicon:      m.ModelInfo.Silicon,
			Thinking:     m.ModelInfo.Thinking,
			SwapGroup:    m.ModelInfo.SwapGroup,
			AlwaysWarm:   m.ModelInfo.AlwaysWarm,
			Experimental: m.ModelInfo.Experimental,
		}
	}
	return f
}

// applyFleet overlays discovered metadata onto a resolved binding: the real
// context window fills an unset window (a config-pinned window is left intact),
// and the enable_thinking kwarg is dropped for models that aren't
// thinking-capable (it's meaningless to them). A nil Fleet or an unknown model
// leaves the spec untouched.
func applyFleet(spec ModelSpec, fleet Fleet) ModelSpec {
	info, ok := fleet[spec.Model]
	if !ok {
		return spec
	}
	if info.MaxInput > 0 && spec.Window == 0 {
		spec.Window = info.MaxInput
	}
	if !info.Thinking {
		spec.Thinking = nil
	}
	return spec
}

// sharedSwapGroup reports the swap_group two bindings collide in — non-empty
// only when they resolve to DIFFERENT models in the SAME group, i.e. they'd
// evict each other on one accelerator slot. Returns "" when there's no conflict
// or the fleet is unknown.
func sharedSwapGroup(fleet Fleet, a, b ModelSpec) string {
	if fleet == nil || a.Model == b.Model {
		return ""
	}
	if g := fleet[a.Model].SwapGroup; g != "" && g == fleet[b.Model].SwapGroup {
		return g
	}
	return ""
}

// keychainKey reads a secret from the macOS keychain by service name. Returns ""
// on any error (item missing, non-macOS). The value is never logged.
func keychainKey(service string) string {
	if service == "" {
		return ""
	}
	out, err := exec.Command("security", "find-generic-password", "-s", service, "-w").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// Backend is the one place an address and its auth live — read from gitignored
// .cortex/config.json, never baked into source. Type names the profile (e.g.
// "litellm" for a local LiteLLM proxy, "openrouter") so discovery and routing can
// specialize; Endpoint is the root URL; KeyService names a keychain item for the
// bearer token (e.g. "cortex-openrouter"), fetched at call time, never stored.
type Backend struct {
	Type       string `json:"type"`
	Endpoint   string `json:"endpoint"`
	KeyService string `json:"key_service"`
}

// Config is the subset of .cortex/config.json the loop consults: the backend
// address/auth plus per-role model overrides. The old harness's other keys
// (endpoints/routing/ollama_*) are ignored — the loop routes through one
// mechanism.
type Config struct {
	Backend Backend              `json:"backend"`
	Models  map[string]ModelSpec `json:"models"`
	Tools   ToolConfig           `json:"tools"`
}

// ToolConfig tunes the agent's tool surface. All fields are optional.
type ToolConfig struct {
	// BashAllow adds extra commands to the built-in bash allowlist (merged, not
	// replaced). Also extendable via CORTEX_BASH_ALLOW (comma-separated).
	BashAllow []string `json:"bash_allow"`
	// AllowDelete enables the confined remove_path tool. Nil means the default
	// (enabled): deletion is autonomous but jailed to the workspace. Set false
	// to remove the tool entirely.
	AllowDelete *bool `json:"allow_delete"`
	// DeleteRoot overrides the deletion confinement root (default: the working
	// directory). Relative paths resolve against the cwd.
	DeleteRoot string `json:"delete_root"`
}

// isOpenRouter reports whether the backend is OpenRouter, which has no
// LiteLLM-style /model/info endpoint — so discovery is skipped and models come
// from config.
func (c *Config) isOpenRouter() bool {
	return c != nil && strings.EqualFold(c.Backend.Type, "openrouter")
}

// deleteEnabled reports whether the remove_path tool is offered (default true).
func (c *Config) deleteEnabled() bool {
	if c == nil || c.Tools.AllowDelete == nil {
		return true
	}
	return *c.Tools.AllowDelete
}

// bashAllowExtra returns extra allowlisted commands from config and the
// CORTEX_BASH_ALLOW env var (comma-separated).
func (c *Config) bashAllowExtra() []string {
	var out []string
	if c != nil {
		out = append(out, c.Tools.BashAllow...)
	}
	if v := os.Getenv("CORTEX_BASH_ALLOW"); v != "" {
		out = append(out, strings.Split(v, ",")...)
	}
	return out
}

// backendEndpoint resolves the backend root: config wins, then CORTEX_BACKEND,
// then the neutral localhost fallback. Safe on a nil Config. No address is ever
// read from source.
func (c *Config) backendEndpoint() string {
	if c != nil && c.Backend.Endpoint != "" {
		return c.Backend.Endpoint
	}
	if v := os.Getenv("CORTEX_BACKEND"); v != "" {
		return v
	}
	return defaultEndpoint
}

// resolveBinding builds the final binding for a role from three layers, in
// precedence order: an explicit config override (model/window/endpoint/key/
// thinking) wins; otherwise the model is selected from discovery by capability
// (selectModel) and its window comes from max_input_tokens; the role's thinking
// policy and the shared backend address/key fill the rest. No model name,
// window, or address is read from source. Safe on a nil Config and nil Fleet.
func (c *Config) resolveBinding(role string, fleet Fleet) ModelSpec {
	pol := rolePolicies[role]
	spec := ModelSpec{Endpoint: c.backendEndpoint()}
	if pol.thinkingOff {
		spec.Thinking = &thinkingOff
	}
	if c != nil {
		if m, ok := c.Models[role]; ok {
			spec.Model = m.Model
			if m.Endpoint != "" {
				spec.Endpoint = m.Endpoint
			}
			if m.Window > 0 {
				spec.Window = m.Window
			}
			if m.KeyService != "" {
				spec.KeyService = m.KeyService
			}
			if m.Thinking != nil {
				spec.Thinking = m.Thinking
			}
		}
		if spec.KeyService == "" {
			spec.KeyService = c.Backend.KeyService
		}
	}
	// Derive the model from discovery when config didn't pin one.
	if spec.Model == "" {
		spec.Model = selectModel(fleet, role)
	}
	// Overlay the real window + gate the thinking kwarg to thinking-capable models.
	return applyFleet(spec, fleet)
}

// findUp walks up from the cwd looking for rel (a path relative to each
// ancestor directory), like git finds .git. Returns "" if none is found.
func findUp(rel string) string {
	dir, err := os.Getwd()
	if err != nil {
		return ""
	}
	for {
		p := filepath.Join(dir, rel)
		if _, err := os.Stat(p); err == nil {
			return p
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
		}
		dir = parent
	}
}

func findConfigPath() string { return findUp(filepath.Join(".cortex", "config.json")) }

// maxInstructionBytes caps how much of AGENTS.md is injected into the system
// prompt, so a bloated instructions file can't quietly eat the window.
const maxInstructionBytes = 16384

// projectInstructions returns the nearest AGENTS.md contents (size-capped), or
// "" when there is none — instructions are an enhancement, not a dependency.
func projectInstructions() string {
	path := findUp("AGENTS.md")
	if path == "" {
		return ""
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	s := strings.TrimSpace(string(data))
	if len(s) > maxInstructionBytes {
		s = s[:maxInstructionBytes] + "\n...[AGENTS.md truncated]"
	}
	return s
}

// LoadConfig reads and parses .cortex/config.json. It returns nil on any
// problem (missing file, bad JSON) so callers transparently fall back to
// defaults — config is an enhancement, not a hard dependency.
func LoadConfig() *Config {
	path := findConfigPath()
	if path == "" {
		return nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil
	}
	return &cfg
}

func NewCortexSession() *CortexSession {
	args := CortexArgs(os.Args)
	req := args.Request()
	cfg := LoadConfig()

	// Discover the fleet, then resolve the live roles against it: model selected
	// by capability (study auto-prefers swap-free reasoner-npu, falling back to
	// reasoner if it's gone), window from max_input_tokens. A nil fleet means the
	// backend is unreachable — note it so an empty model id isn't a mystery.
	// OpenRouter (and any backend without LiteLLM's /model/info) is driven by
	// config-pinned models; skip discovery and its "unavailable" note there.
	var fleet Fleet
	if !cfg.isOpenRouter() {
		fleet = discoverFleet(context.Background(), cfg.backendEndpoint())
		if fleet == nil {
			fmt.Println(withColor(fmt.Sprintf("note: model discovery unavailable at %s — set backend in .cortex/config.json or pin models", cfg.backendEndpoint()), yellow))
		}
	}
	code := cfg.resolveBinding(roleCode, fleet)
	study := cfg.resolveBinding(roleStudy, fleet)

	// Surface the swap-group thrash the brief warns about: code and study run
	// back-to-back within a turn, so same-group bindings evict each other.
	if g := sharedSwapGroup(fleet, code, study); g != "" {
		fmt.Println(withColor(fmt.Sprintf("warning: code (%s) and study (%s) share swap_group %q — they evict each other every turn; route one to different silicon", code.Model, study.Model, g), yellow))
	}

	req.Model = code.Model
	req.BaseURL = code.Endpoint
	req.APIKey = keychainKey(code.KeyService)
	req.ChatTemplateKwargs = code.TemplateKwargs()

	// OpenRouter returns per-call dollar cost when asked; opt in so the gauge can
	// show spend. Gated to OpenRouter — local backends don't grok this field.
	if cfg.isOpenRouter() {
		req.Usage = &usageInclude{Include: true}
	}

	// Extend the bash allowlist from config + env (merged with the built-ins).
	for _, c := range cfg.bashAllowExtra() {
		if c = strings.TrimSpace(c); c != "" {
			bashAllowlist[c] = true
		}
	}

	// Deletion: confined to the workspace, autonomous by default. When disabled,
	// drop remove_path from the advertised tools so the model never sees it.
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

	return &CortexSession{
		Args:         &args,
		Request:      req,
		Config:       cfg,
		Window:       code.Window,
		Study:        study,
		Fleet:        fleet,
		deleteRoot:   deleteRoot,
		allowDelete:  allowDelete,
		sessionStart: time.Now(),
	}
}

// toolsExcept returns the tool list without the named function.
func toolsExcept(ts []Tool, name string) []Tool {
	out := make([]Tool, 0, len(ts))
	for _, t := range ts {
		if t.Function.Name != name {
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
}

// --- Session transcripts -------------------------------------------------
//
// Raw conversations persist as plain JSONL under .cortex/sessions/<id>.jsonl,
// one timestamped entry per line (pi-style). Deliberately NOT a journal
// writer-class: the journal records distilled context (capture events,
// insights); raw sessions stay out of it. `cat | jq` always works, and
// .cortex/ is gitignored so transcripts never leave the machine.
//
// The transcript records EVERYTHING that happened in a turn — not just the
// conversation, but the context that was retrieved and any compaction. `kind`
// labels each entry so the record is complete (debug "why did it do that")
// while replay stays selective: resume rebuilds the live window from `message`
// entries only (record-only policy), so retrieved context is on the record but
// not blindly re-fed. What's core vs. aux is a label, not a storage decision.

const (
	kindMessage    = "message"    // core conversation: replayed into the window on resume
	kindRetrieval  = "retrieval"  // context fed to the model this turn: recorded, not replayed
	kindCompaction = "compaction" // marker that history was compacted here
)

// sessionEntry is one transcript line. Message carries the conversation when
// kind=="message"; Query/Results carry the retrieved context when
// kind=="retrieval"; From/Coverage mark a compaction. A missing kind (older
// transcripts) is treated as a core message.
type sessionEntry struct {
	TS      time.Time `json:"ts"`
	Kind    string    `json:"kind,omitempty"`
	Message           // populated for kindMessage

	// kindRetrieval:
	Query   string            `json:"query,omitempty"`
	Results []retrievedRecord `json:"results,omitempty"`

	// kindCompaction:
	From     string  `json:"from,omitempty"`     // the prior session id this was compacted from
	Coverage float64 `json:"coverage,omitempty"` // study coverage of the compacted transcript
}

// retrievedRecord is one retrieved hit as stored in the transcript — the
// durable record of what memory the model was given on a turn.
type retrievedRecord struct {
	Category string  `json:"category,omitempty"`
	Content  string  `json:"content"`
	Score    float64 `json:"score,omitempty"`
	ID       string  `json:"id,omitempty"`
}

// contextDir resolves the project's .cortex: the nearest one up the tree, or
// cwd/.cortex when none exists yet. The journal, transcripts, and retrieval
// store all live under it.
func contextDir() string {
	root := findUp(".cortex")
	if root == "" {
		root = ".cortex"
	}
	return root
}

// sessionsDir is where raw transcripts live.
func sessionsDir() string { return filepath.Join(contextDir(), "sessions") }

// StartTranscript begins persisting this session under a fresh timestamp id.
// Best-effort: on any error the session simply runs unpersisted — the REPL
// must never fail to start because the transcript can't be written.
func (cs *CortexSession) StartTranscript() {
	dir := sessionsDir()
	if err := os.MkdirAll(dir, 0755); err != nil {
		return
	}
	// O_EXCL plus suffix probing keeps ids unique when two sessions — or a
	// same-second compact/clear — land on one timestamp.
	base := time.Now().Format("20060102-150405")
	id := base
	var f *os.File
	for i := 2; ; i++ {
		var err error
		f, err = os.OpenFile(filepath.Join(dir, id+".jsonl"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0644)
		if err == nil {
			break
		}
		if !errors.Is(err, os.ErrExist) || i > 100 {
			return
		}
		id = fmt.Sprintf("%s-%d", base, i)
	}
	cs.SessionID = id
	cs.transcript = f
	// Persist the seeded system message(s) so resume restores the exact context.
	for _, m := range cs.Request.Messages {
		cs.writeTranscript(m)
	}
}

// ResumeTranscript loads a prior session — the latest, or a specific id — into
// the request history and continues appending to the same file.
func (cs *CortexSession) ResumeTranscript(id string) error {
	dir := sessionsDir()
	if id == "" {
		var err error
		if id, err = latestSessionID(dir); err != nil {
			return err
		}
	}
	path := filepath.Join(dir, id+".jsonl")
	msgs, err := loadTranscript(path)
	if err != nil {
		return err
	}
	if len(msgs) == 0 {
		return fmt.Errorf("session %s is empty", id)
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return fmt.Errorf("reopen %s: %w", path, err)
	}
	cs.Request.Messages = msgs
	cs.SessionID = id
	cs.transcript = f
	return nil
}

// latestSessionID returns the newest transcript id in dir. Timestamp ids sort
// lexicographically, so newest = max.
func latestSessionID(dir string) (string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", fmt.Errorf("no sessions at %s: %w", dir, err)
	}
	latest := ""
	for _, e := range entries {
		if name := e.Name(); !e.IsDir() && strings.HasSuffix(name, ".jsonl") && name > latest {
			latest = name
		}
	}
	if latest == "" {
		return "", fmt.Errorf("no sessions found in %s", dir)
	}
	return strings.TrimSuffix(latest, ".jsonl"), nil
}

// sessionInfo is a one-line summary of a saved session, for /sessions.
type sessionInfo struct {
	ID       string
	ModTime  time.Time
	Messages int    // core user+assistant messages
	First    string // first user prompt, for a human-readable preview
}

// listSessions summarizes saved sessions newest-first, capped at limit (0 =
// all). A transcript that fails to parse still appears (with zero counts)
// rather than vanishing from the listing.
func listSessions(dir string, limit int) ([]sessionInfo, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("no sessions at %s: %w", dir, err)
	}
	var out []sessionInfo
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".jsonl") {
			continue
		}
		info := sessionInfo{ID: strings.TrimSuffix(name, ".jsonl")}
		if fi, ferr := e.Info(); ferr == nil {
			info.ModTime = fi.ModTime()
		}
		if msgs, lerr := loadTranscript(filepath.Join(dir, name)); lerr == nil {
			for _, m := range msgs {
				if m.Role != RoleUser && m.Role != "assistant" {
					continue
				}
				info.Messages++
				if m.Role == RoleUser && info.First == "" && strings.TrimSpace(m.Content) != "" {
					info.First = firstLine(m.Content)
				}
			}
		}
		out = append(out, info)
	}
	// ReadDir is name-sorted and ids are timestamps, so the slice is
	// chronological; reverse for newest-first.
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// firstLine returns the first non-blank line of s, trimmed.
func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = strings.TrimSpace(s[:i])
	}
	return s
}

// relTime renders a coarse "Nm ago" / "Nh ago" / "Nd ago" age.
func relTime(t time.Time) string {
	if t.IsZero() {
		return "?"
	}
	switch d := time.Since(t); {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	}
}

// invokedName is the command the user ran (argv[0]'s base), so resume hints
// quote the right binary whatever it was built/installed as.
func invokedName() string {
	if len(os.Args) > 0 {
		if b := filepath.Base(os.Args[0]); b != "" && b != "." && b != "/" {
			return b
		}
	}
	return "loop"
}

// loadTranscript reads a transcript back into messages. A malformed line is an
// error, not a skip — resuming a silently truncated history would be worse
// than telling the user the file is damaged.
func loadTranscript(path string) ([]Message, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read session: %w", err)
	}
	var msgs []Message
	for i, line := range strings.Split(string(data), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var e sessionEntry
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			return nil, fmt.Errorf("%s line %d: %w", path, i+1, err)
		}
		// Record-only replay: rebuild the live window from core conversation
		// only. Retrieval/compaction entries stay in the file (the record)
		// but are not re-fed — retrieval re-runs fresh against the new turn.
		// A missing kind (older transcripts) is a core message.
		if e.Kind == "" || e.Kind == kindMessage {
			msgs = append(msgs, e.Message)
		}
	}
	return msgs, nil
}

// writeEntry appends one labelled entry to the open transcript, if any.
// Best-effort by design: a persistence hiccup must not break the live turn.
func (cs *CortexSession) writeEntry(e sessionEntry) {
	if cs.transcript == nil {
		return
	}
	e.TS = time.Now()
	b, err := json.Marshal(e)
	if err != nil {
		return
	}
	cs.transcript.Write(append(b, '\n'))
}

// writeTranscript records one core conversation message.
func (cs *CortexSession) writeTranscript(m Message) {
	cs.writeEntry(sessionEntry{Kind: kindMessage, Message: m})
}

// recordRetrieval records what memory was fed to the model this turn. The
// record is complete (debuggability); replay is selective (loadTranscript
// skips it). No-op when nothing was retrieved.
func (cs *CortexSession) recordRetrieval(query string, results []cognition.Result) {
	if len(results) == 0 {
		return
	}
	recs := make([]retrievedRecord, 0, len(results))
	for _, r := range results {
		recs = append(recs, retrievedRecord{Category: r.Category, Content: r.Content, Score: r.Score, ID: r.ID})
	}
	cs.writeEntry(sessionEntry{Kind: kindRetrieval, Query: query, Results: recs})
}

// --- Compaction ------------------------------------------------------------
//
// Compaction IS study, pointed at the conversation: the same engine that
// curates big files curates the session transcript. No bespoke summarizer.

// compactStudy runs the study engine over a transcript for Compact. A var so
// tests can stub the model call.
var compactStudy = func(ctx context.Context, cs *CortexSession, path string, window int) (study.StudyLoopResult, error) {
	return cs.runStudy(ctx, path, compactGoal, compactPasses, 0, 0, nil, window)
}

// contextRatio is the live window-fill estimate. The gauge color and the
// auto-compact trigger both read it.
func (cs *CortexSession) contextRatio() float64 {
	return float64(cs.LastPromptTokens) / float64(cs.windowSize())
}

// Compact replaces the conversation with a study-curated digest of its own
// transcript. The raw transcript stays on disk; the digest seeds a NEW
// transcript, so resume restores the compacted state, not the overflowing one.
func (cs *CortexSession) Compact(ctx context.Context) error {
	if cs.transcript == nil || cs.SessionID == "" {
		return fmt.Errorf("no transcript to compact (unpersisted session)")
	}
	path := filepath.Join(sessionsDir(), cs.SessionID+".jsonl")

	// The digest's consumer is the CODE model, and the point of compacting is
	// to leave most of its window free — so the budget is a quarter of the
	// code window, capped by what the study model can actually hold. Passing
	// the consumer window (not the study model's) is also what forces study
	// mode: to the study model the transcript might "fit", but fitting is not
	// the goal — compression is.
	window := cs.windowSize() / 4
	if sw := cs.studyWindow(); sw < window {
		window = sw
	}
	res, err := compactStudy(ctx, cs, path, window)
	if err != nil {
		return fmt.Errorf("compact: %w", err)
	}
	// Read mode = the whole transcript fits well inside the digest budget.
	// There is nothing to compress yet; replacing history with itself would
	// only churn the session id.
	if res.Stopped == "read" {
		return fmt.Errorf("session fits within the %s-token digest budget; nothing to compact yet", humanK(window))
	}
	// Check the digests themselves, not the render — the render always carries
	// a coverage header, so it is never empty even when the study said nothing.
	if strings.TrimSpace(strings.Join(res.Digests, "")) == "" {
		return fmt.Errorf("compact: study returned an empty digest")
	}
	digest := strings.TrimSpace(renderStudyResult(res))

	// Rebuild: the original system seed plus the digest as carried-over
	// context. The digest rides a user message because many chat templates
	// allow only one system message per conversation.
	sys := cs.Request.Messages[0]
	summary := Message{
		Role:    RoleUser,
		Content: "[Compacted session — summary of the conversation so far. Continue from this state.]\n\n" + digest,
	}
	from := cs.SessionID
	cs.transcript.Close()
	cs.transcript = nil
	cs.Request.Messages = []Message{sys}
	cs.StartTranscript()
	// Mark the new transcript as a compaction of the old, so the record shows
	// where the digest came from (the raw prior transcript is still on disk).
	cs.writeEntry(sessionEntry{Kind: kindCompaction, From: from, Coverage: res.CoveragePct})
	cs.Append(summary)
	// Fill is unknown until the next send; the gauge resets rather than lies.
	cs.LastPromptTokens = 0
	return nil
}

// Clear resets the conversation to a fresh seed (system prompt + AGENTS.md,
// re-read from disk) and starts a new transcript. The old transcript stays on
// disk; the model binding — including a /model switch — is preserved.
// printSessions lists recent saved sessions for in-REPL discovery, marking the
// current one. Resuming still happens at startup (loop resume <id>); this makes
// the ids — otherwise just timestamps — discoverable and meaningful.
func (cs *CortexSession) printSessions() {
	infos, err := listSessions(sessionsDir(), 15)
	if err != nil || len(infos) == 0 {
		fmt.Println(withColor("no sessions found", gray))
		return
	}
	for _, s := range infos {
		marker := "  "
		if s.ID == cs.SessionID {
			marker = withColor("✦ ", green)
		}
		preview := s.First
		if preview == "" {
			preview = "(no prompt)"
		}
		if r := []rune(preview); len(r) > 60 {
			preview = string(r[:60]) + "…"
		}
		fmt.Printf("%s%s  %-8s  %2d msgs  %s\n", marker, s.ID, relTime(s.ModTime), s.Messages, preview)
	}
	fmt.Println(withColor(fmt.Sprintf("resume at startup: %s resume <id>", invokedName()), gray))
}

func (cs *CortexSession) Clear() {
	if cs.transcript != nil {
		cs.transcript.Close()
		cs.transcript = nil
	}
	old := cs.Request
	cs.Request = (CortexArgs{}).Request()
	cs.Request.Model = old.Model
	cs.Request.BaseURL = old.BaseURL
	cs.Request.APIKey = old.APIKey
	cs.Request.ChatTemplateKwargs = old.ChatTemplateKwargs
	cs.LastPromptTokens = 0
	cs.StartTranscript()
}

// --- Retrieval -------------------------------------------------------------
//
// Fast (mechanical) retrieval surfaces relevant prior context at turn start:
// a Reflex pass over the project's .cortex/ store, foreground-latency-bounded.
// Reranking (Reflect/Think on the reasoner, in parallel) layers on later —
// this is the latency-bounded foreground half of that split.

// retrievalLimit caps how many prior-context hits a turn injects.
const retrievalLimit = 5

// retrievedContentCap bounds each injected snippet so one long capture can't
// dominate the window.
const retrievedContentCap = 240

// EnableRetrieval wires Fast retrieval over the project's .cortex/ store.
// Best-effort: any failure leaves retrieval disabled and the REPL runs exactly
// as before. Text-only (nil embedder + nil provider) — semantic search and
// Think reranking attach later without changing this call site. Only the
// interactive REPL calls this; the study/eval subcommands never build storage.
func (cs *CortexSession) EnableRetrieval() {
	dir := contextDir()
	cfg := &config.Config{ContextDir: dir, ProjectRoot: filepath.Dir(dir)}
	store, err := storage.New(cfg)
	if err != nil {
		return
	}
	cortex, err := intcog.New(store, nil, nil, cfg)
	if err != nil {
		store.Close()
		return
	}
	cs.store = store
	cs.retriever = cortex
	cs.capturer = capture.NewWithStorage(cfg, store)
}

// --- Capture (Tier 1) ------------------------------------------------------
//
// At turn end the loop writes a structural capture to the store so the work
// becomes retrievable across sessions — the write side of the learning loop
// (retrieval is the read side). Mechanical, synchronous, best-effort: no
// model, and a failure never breaks the turn. EVERY completed turn is captured,
// read-only included — a mechanical filter can't tell a durable lesson (a
// stated preference, a correction) from noise, and read-only turns are exactly
// where those live. Tier 2 (a model, async) distills the durable unit later and
// supersedes these raw rows; until then retrieval ranking sorts the noise.

// captureExcerptCap bounds the stored answer excerpt; the full turn lives in
// the transcript (reachable via the session id), so this is just a retrieval
// surface, not the record.
const captureExcerptCap = 280

// turnArtifacts derives the mechanical outcome of a turn from its messages: the
// files edited and commands run (from tool calls), and the model's final answer
// (the last assistant message with prose and no tool calls). Pure — no I/O.
func turnArtifacts(turnMsgs []Message) (outcome, answer string) {
	var files, cmds []string
	seen := map[string]bool{}
	for _, m := range turnMsgs {
		for _, tc := range m.ToolCalls {
			switch tc.Function.Name {
			case FunctionWriteFile, FunctionEditFile:
				if p, err := tc.stringArg("path"); err == nil && !seen["f:"+p] {
					seen["f:"+p] = true
					files = append(files, p)
				}
			case FunctionBash:
				if c, err := tc.stringArg("command"); err == nil && !seen["c:"+c] {
					seen["c:"+c] = true
					cmds = append(cmds, c)
				}
			}
		}
		if m.Role != RoleUser && m.Role != RoleTool && len(m.ToolCalls) == 0 && strings.TrimSpace(m.Content) != "" {
			answer = m.Content // last one wins → the final answer
		}
	}
	var parts []string
	if len(files) > 0 {
		parts = append(parts, "edited: "+strings.Join(files, ", "))
	}
	if len(cmds) > 0 {
		parts = append(parts, "ran: "+strings.Join(cmds, "; "))
	}
	return strings.Join(parts, " | "), answer
}

// captureTurn records a completed turn to the store. The user's message is the
// retrieval key (where stated preferences/corrections live); the outcome line
// and a capped answer excerpt give a hit some substance. ID is left empty so
// capture assigns a unique one.
func (cs *CortexSession) captureTurn(userMsg string, turnMsgs []Message) {
	if cs.capturer == nil || strings.TrimSpace(userMsg) == "" {
		return
	}
	outcome, answer := turnArtifacts(turnMsgs)
	// The durable summary goes in ToolResult: Reflex surfaces that field as a
	// result's Content (what gets injected next time). The user's message is
	// the key, the outcome says what changed, a capped answer excerpt adds
	// substance.
	summary := userMsg
	if outcome != "" {
		summary += "\n[" + outcome + "]"
	}
	if answer != "" {
		if len(answer) > captureExcerptCap {
			answer = answer[:captureExcerptCap] + "…"
		}
		summary += "\n→ " + answer
	}
	if err := cs.capturer.CaptureEvent(&events.Event{
		Source:     events.SourceGeneric,
		EventType:  events.EventToolUse,
		Timestamp:  time.Now(),
		ToolName:   "loop",
		ToolInput:  map[string]any{"type": "turn", "user_prompt": userMsg},
		ToolResult: summary,
		Context:    events.EventContext{SessionID: cs.SessionID, ProjectPath: contextDir()},
	}); err == nil {
		cs.captures++
	}
}

// remember stores an explicit user memory (/remember) — the highest-precision
// capture, because the user marked it as worth keeping.
func (cs *CortexSession) remember(text string) error {
	if cs.capturer == nil {
		return fmt.Errorf("memory unavailable — no .cortex store")
	}
	// Text in ToolResult so it surfaces as the retrieved Content (Reflex maps
	// Content from ToolResult); type in ToolInput marks it for Tier 2.
	err := cs.capturer.CaptureEvent(&events.Event{
		Source:     events.SourceGeneric,
		EventType:  events.EventToolUse,
		Timestamp:  time.Now(),
		ToolName:   "loop",
		ToolInput:  map[string]any{"type": "memory"},
		ToolResult: text,
		Context:    events.EventContext{SessionID: cs.SessionID, ProjectPath: contextDir()},
	})
	if err == nil {
		cs.captures++
	}
	return err
}

// --- Capture (Tier 2: model-distilled insights) ----------------------------
//
// Tier 1 records every turn raw; Tier 2 distills the durable unit (a decision,
// correction, pattern, constraint) with the reasoner and writes it to the
// insights layer, which Reflex favors over raw event rows — so the insight
// outranks (soft-supersedes) the Tier 1 row it came from.
//
// It runs OFF the foreground: a turn buffers into pendingTurns and fires a
// cancelable goroutine that distills during the idle gap before the next
// prompt. The next turn preempts it (stopDistill) so foreground model work
// never waits behind distillation. Best-effort: a preempted or failed turn
// stays pending and retries next idle; no reasoner → nothing distilled, Tier 1
// rows remain. The extraction reuses cognition's prompt + parser (the same
// contract Dream uses); only the scheduling and turn-plumbing are local.

// distillRecentInsights bounds how many existing insights we show the reasoner
// as "already captured, don't repeat" (dedup across batches/sessions).
const distillRecentInsights = 20

// pendingTurn is a completed turn awaiting distillation.
type pendingTurn struct {
	user string
	msgs []Message
}

// distillExtract runs the reasoner over the analysis prompt. A var so tests
// stub the model call.
var distillExtract = func(ctx context.Context, p llm.Provider, prompt string) (string, error) {
	return p.GenerateWithSystem(ctx, prompt, llm.AnalysisSystemPrompt)
}

// reasoner builds the provider for background distillation — the study/reasoner
// binding, the model that thinks off the foreground path.
func (cs *CortexSession) reasoner() *llm.OpenAICompatClient {
	base := strings.TrimRight(cs.Study.Endpoint, "/")
	if !strings.HasSuffix(base, "/v1") {
		base += "/v1"
	}
	p := llm.NewOpenAICompatClient(llm.EndpointConfig{
		Name:               "distill",
		BaseURL:            base,
		APIKey:             keychainKey(cs.Study.KeyService),
		ChatTemplateKwargs: cs.Study.TemplateKwargs(),
		Timeout:            10 * time.Minute,
	})
	p.SetModel(cs.Study.Model)
	p.SetTemperature(0)
	return p
}

// noteTurn buffers a completed turn and kicks off distillation over the
// backlog. Best-effort: requires the store (where insights land).
func (cs *CortexSession) noteTurn(user string, msgs []Message) {
	if cs.store == nil || strings.TrimSpace(user) == "" {
		return
	}
	cp := make([]Message, len(msgs))
	copy(cp, msgs)
	cs.distillMu.Lock()
	cs.pendingTurns = append(cs.pendingTurns, pendingTurn{user: user, msgs: cp})
	cs.distillMu.Unlock()
	cs.startDistill()
}

// startDistill fires a cancelable background distillation if there are pending
// turns and none is already running.
func (cs *CortexSession) startDistill() {
	cs.distillMu.Lock()
	if cs.store == nil || len(cs.pendingTurns) == 0 || cs.distillCancel != nil {
		cs.distillMu.Unlock()
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	cs.distillCancel, cs.distillDone = cancel, done
	cs.distillMu.Unlock()
	go cs.distillLoop(ctx, done)
}

// stopDistill preempts any in-flight distillation and waits for it to exit, so
// the next turn's foreground model work gets the endpoint. A canceled HTTP
// call returns promptly, so the wait is short.
func (cs *CortexSession) stopDistill() {
	cs.distillMu.Lock()
	cancel, done := cs.distillCancel, cs.distillDone
	cs.distillCancel, cs.distillDone = nil, nil
	cs.distillMu.Unlock()
	if cancel != nil {
		cancel()
		<-done
	}
}

// distillLoop wraps distillPending with the cancel/done bookkeeping.
func (cs *CortexSession) distillLoop(ctx context.Context, done chan struct{}) {
	defer func() {
		cs.distillMu.Lock()
		if cs.distillDone == done { // natural finish — let startDistill run again
			cs.distillCancel, cs.distillDone = nil, nil
		}
		cs.distillMu.Unlock()
		close(done)
	}()
	cs.distillPending(ctx)
}

// distillPending drains pending turns one at a time through the reasoner,
// storing at most one insight per turn (the prompt's contract). Preemptible:
// it stops at the next turn boundary on ctx cancel, leaving the current turn
// pending. A turn that yields an insight, NO_INSIGHT, or unparseable output is
// consumed; only a transient model error leaves it to retry.
func (cs *CortexSession) distillPending(ctx context.Context) {
	for {
		cs.distillMu.Lock()
		if len(cs.pendingTurns) == 0 {
			cs.distillMu.Unlock()
			return
		}
		turn := cs.pendingTurns[0]
		cs.distillMu.Unlock()

		if ctx.Err() != nil {
			return // preempted — leave the turn pending
		}

		known := cs.recentInsightSummaries(distillRecentInsights)
		content := formatTurnForDistill(turn, known)
		prompt := fmt.Sprintf(intcog.DreamAnalysisPrompt, content, "session", cs.SessionID, "")
		resp, err := distillExtract(ctx, cs.reasoner(), prompt)
		if err != nil || ctx.Err() != nil {
			return // transient: leave pending, retry next idle
		}
		if f, perr := intcog.ParseInsight(resp); perr == nil {
			if c := strings.TrimSpace(f.Content); c != "" && !isDuplicateInsight(c, known) {
				if serr := cs.store.StoreInsightWithSession("", f.Category, c, int(f.Importance*10), f.Tags,
					"distilled from a session turn", cs.SessionID, "loop"); serr == nil {
					cs.insights.Add(1)
				}
			}
		}
		// Consume the turn (distilled / NO_INSIGHT / unparseable all consume it).
		cs.distillMu.Lock()
		if len(cs.pendingTurns) > 0 {
			cs.pendingTurns = cs.pendingTurns[1:]
		}
		cs.distillMu.Unlock()
	}
}

// recentInsightSummaries returns recent stored insight summaries, the dedup
// context fed to the reasoner ("already captured, don't repeat").
func (cs *CortexSession) recentInsightSummaries(limit int) []string {
	if cs.store == nil {
		return nil
	}
	insights, err := cs.store.GetRecentInsights(limit)
	if err != nil {
		return nil
	}
	out := make([]string, 0, len(insights))
	for _, in := range insights {
		if s := strings.TrimSpace(in.Summary); s != "" {
			out = append(out, s)
		}
	}
	return out
}

// formatTurnForDistill renders one turn for the analysis prompt: the user's
// message, what changed, the final answer, and the already-known insights so
// the reasoner doesn't re-extract them.
func formatTurnForDistill(turn pendingTurn, known []string) string {
	outcome, answer := turnArtifacts(turn.msgs)
	var b strings.Builder
	fmt.Fprintf(&b, "User: %s\n", turn.user)
	if outcome != "" {
		fmt.Fprintf(&b, "Actions: %s\n", outcome)
	}
	if answer != "" {
		fmt.Fprintf(&b, "Assistant: %s\n", answer)
	}
	if len(known) > 0 {
		b.WriteString("\nAlready captured (do not repeat these):\n")
		for _, k := range known {
			fmt.Fprintf(&b, "- %s\n", k)
		}
	}
	return b.String()
}

// isDuplicateInsight reports whether content restates something already stored.
// Mechanical backstop to the prompt-level dedup: normalized equality or
// containment either way.
func isDuplicateInsight(content string, known []string) bool {
	c := normalizeInsight(content)
	if c == "" {
		return false
	}
	for _, k := range known {
		n := normalizeInsight(k)
		if n == "" {
			continue
		}
		if n == c || strings.Contains(n, c) || strings.Contains(c, n) {
			return true
		}
	}
	return false
}

// normalizeInsight lowercases and collapses whitespace for comparison.
func normalizeInsight(s string) string {
	return strings.ToLower(strings.Join(strings.Fields(s), " "))
}

// retrieve runs one Fast retrieval for the turn and returns the hits, or nil
// when retrieval is disabled, errors, or finds nothing. It uses a background
// context, not the turn's: Reflex is mechanical (no model) so it needn't be
// Ctrl-C-cancelable, and detaching lets the async Reflect caching survive the
// turn to warm the next one. Results are quality-gated by Resolve; the caller
// injects them regardless of its inject/queue/wait decision — that gate is
// tuned for embedding-scale scores and would suppress everything on the
// text-only path local setups use.
func (cs *CortexSession) retrieve(query string) []cognition.Result {
	if cs.retriever == nil || strings.TrimSpace(query) == "" {
		return nil
	}
	res, err := cs.retriever.Retrieve(context.Background(),
		cognition.Query{Text: query, Limit: retrievalLimit}, cognition.Fast)
	if err != nil || res == nil {
		return nil
	}
	return res.Results
}

// formatRetrieved renders results as a compact, clearly-labelled context block.
// The header marks the text as retrieved memory, not a user instruction, so the
// model weighs it accordingly. Returns "" if nothing has printable content.
func formatRetrieved(results []cognition.Result) string {
	var b strings.Builder
	n := 0
	for _, r := range results {
		c := strings.Join(strings.Fields(r.Content), " ") // collapse whitespace/newlines
		if c == "" {
			continue
		}
		if len(c) > retrievedContentCap {
			c = c[:retrievedContentCap] + "…"
		}
		cat := r.Category
		if cat == "" {
			cat = "note"
		}
		fmt.Fprintf(&b, "- [%s] %s\n", cat, c)
		n++
	}
	if n == 0 {
		return ""
	}
	return "# Relevant context from memory (retrieved, not user-authored)\n" + strings.TrimRight(b.String(), "\n")
}

// Close releases the transcript and retrieval resources at REPL exit.
func (cs *CortexSession) Close() {
	cs.stopDistill() // cancel any in-flight distillation and wait for it
	if cs.transcript != nil {
		cs.transcript.Close()
		cs.transcript = nil
	}
	if cs.retriever != nil {
		cs.retriever.Shutdown(context.Background())
		cs.retriever = nil
	}
	if cs.store != nil {
		cs.store.Close()
		cs.store = nil
	}
}

// --- Session metrics (6a) --------------------------------------------------

// contextStrategy names this session's memory mode for the eval record:
// "cortex" when retrieval/capture are active, "none" when they're disabled.
func (cs *CortexSession) contextStrategy() string {
	if cs.retriever != nil {
		return "cortex"
	}
	return "none"
}

// sessionSummary is the one-line closing report.
func (cs *CortexSession) sessionSummary() string {
	dur := time.Since(cs.sessionStart).Round(time.Second)
	cost := ""
	if cs.costUSD > 0 {
		cost = " · " + humanCost(cs.costUSD)
	}
	return fmt.Sprintf("%d turns · %s in / %s out%s · %d captured · %d insights · %d retrievals · %s",
		cs.turns, humanK(cs.tokensIn), humanK(cs.tokensOut), cost,
		cs.captures, cs.insights.Load(), cs.retrievals, dur)
}

// humanCost formats a dollar cost with precision that scales to the magnitude —
// per-turn costs are often fractions of a cent.
func humanCost(c float64) string {
	switch {
	case c >= 1:
		return fmt.Sprintf("$%.2f", c)
	case c >= 0.01:
		return fmt.Sprintf("$%.3f", c)
	default:
		return fmt.Sprintf("$%.4f", c)
	}
}

// emitSessionMetrics writes one eval.cell_result for the session to the eval
// journal class — the canonical structured sink (shared with the eval suite).
// Best-effort: a metrics-write failure never affects the session.
func (cs *CortexSession) emitSessionMetrics() {
	if cs.SessionID == "" {
		return
	}
	p := journal.EvalCellResultPayload{
		SchemaVersion:         "1",
		RunID:                 cs.SessionID,
		Timestamp:             time.Now().UTC().Format(time.RFC3339),
		ScenarioID:            "repl-session",
		Harness:               "loop",
		Provider:              "openai-compat",
		Model:                 cs.Request.Model,
		Backend:               cs.Request.BaseURL,
		ContextStrategy:       cs.contextStrategy(),
		CortexVersion:         version(),
		Temperature:           cs.Request.Temperature,
		TokensIn:              cs.tokensIn,
		TokensOut:             cs.tokensOut,
		InjectedContextTokens: cs.injectedChars / 4, // ~4 bytes/token
		LatencyMs:             time.Since(cs.sessionStart).Milliseconds(),
		AgentTurnsTotal:       cs.turns,
		Notes: fmt.Sprintf("captures=%d insights=%d retrievals=%d",
			cs.captures, cs.insights.Load(), cs.retrievals),
	}
	entry, err := journal.NewEvalCellResultEntry(p)
	if err != nil {
		return
	}
	w, err := journal.NewWriter(journal.WriterOpts{
		ClassDir: filepath.Join(contextDir(), "journal", "eval"),
		Fsync:    journal.FsyncPerBatch,
	})
	if err != nil {
		return
	}
	defer w.Close()
	_, _ = w.Append(entry)
}

// humanK renders a token count compactly: 8200 -> "8.2k", 999 -> "999".
func humanK(n int) string {
	if n < 1000 {
		return fmt.Sprintf("%d", n)
	}
	return strings.TrimSuffix(fmt.Sprintf("%.1f", float64(n)/1000), ".0") + "k"
}

// ctxColor shifts the context gauge green -> yellow -> red as the window fills,
// so context pressure is ambient — you feel it before you hit the wall. Red
// begins exactly at compactThreshold: the color and the auto-compact agree.
func ctxColor(used, max int) string {
	switch r := float64(used) / float64(max); {
	case r < 0.5:
		return green
	case r < compactThreshold:
		return yellow
	default:
		return red
	}
}

// Prompt renders the inline status line printed before every scan:
//
//	cortex 0.1.0 · coder · 8.2k/64k  ❯
//
// The token fraction is live (last prompt_tokens / window) and recolors with
// fill. Status is dim; the glyph is the bright input affordance.
func (cs *CortexSession) Prompt() string {
	win := cs.windowSize()
	status := withColor(fmt.Sprintf("cortex %s · %s · ", version(), cs.Request.Model), gray)
	gauge := withColor(fmt.Sprintf("%s/%s", humanK(cs.LastPromptTokens), humanK(win)), ctxColor(cs.LastPromptTokens, win))
	cost := ""
	if cs.costUSD > 0 {
		cost = withColor(" · "+humanCost(cs.costUSD), gray)
	}
	return fmt.Sprintf("%s%s%s  %s ", status, gauge, cost, withColor(promptGlyph, cyan))
}

// streamingEnabled reports whether the REPL streams responses (the default).
// CORTEX_LOOP_STREAM=0/false/off forces the blocking spinner path — a safety
// hatch for any endpoint that doesn't speak SSE.
func streamingEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("CORTEX_LOOP_STREAM"))) {
	case "0", "false", "no", "off":
		return false
	}
	return true
}

// toolMarker is Qwen's native tool-call opener. When the proxy doesn't
// normalize tool calls they arrive as this markup inside content; it's stripped
// from the stored message, so the live echo must hold back at this point too.
const toolMarker = "<tool_call"

// streamPrinter echoes assistant prose to the terminal as it streams. It prints
// the gutter lazily on the first visible byte (stopping the spinner first), and
// suppresses output once a tool-call marker appears so raw markup never shows.
// All printing happens on the calling goroutine (StreamChat invokes onContent
// synchronously), so it never races the spinner once that's stopped.
type streamPrinter struct {
	spinner    *Spinner          // stopped on first visible byte; nil to skip (tests)
	out        io.Writer         // destination; nil means os.Stdout
	buf        strings.Builder   // all content seen so far
	reason     strings.Builder   // accumulated reasoning, for the live ticker tail
	printed    int               // bytes of buf already written
	suppress   bool              // a tool-call marker appeared; stop echoing
	began      bool              // gutter printed (and spinner stopped)
	md         *markdownRenderer // nil → raw token streaming; set → block-buffered render
	pending    string            // md path: prose not yet flushed as a complete block
	gutterOpen bool              // md path: gutter printed, first block not yet joined to it
	// onStatus drives a "thinking…" indicator when there's no standalone spinner
	// (the anchored REPL): on=true with the latest reasoning tail, on=false when
	// the answer starts. nil in the normal spinner path.
	onStatus func(on bool, tail string)
}

// reasoningTailWidth caps the live "thinking…" ticker to one line on typical
// terminals — we show the most recent runes, not the whole chain-of-thought.
const reasoningTailWidth = 80

// reasoningTail collapses whitespace and returns the last width runes, so the
// ticker stays a single, bounded line as reasoning streams.
func reasoningTail(s string, width int) string {
	s = strings.Join(strings.Fields(s), " ")
	r := []rune(s)
	if len(r) > width {
		r = r[len(r)-width:]
	}
	return string(r)
}

// onReasoning is the StreamChat reasoning callback: it feeds the spinner a dim
// live tail of the chain-of-thought. Reasoning is never printed to the
// transcript — once the answer starts, emit stops the spinner and the ticker is
// erased. No-op after the answer has begun (or with no spinner, e.g. tests).
func (p *streamPrinter) onReasoning(s string) {
	if p.began {
		return
	}
	p.reason.WriteString(s)
	tail := reasoningTail(p.reason.String(), reasoningTailWidth)
	switch {
	case p.spinner != nil:
		p.spinner.SetLabel(withColor("thinking… "+tail, gray))
	case p.onStatus != nil:
		p.onStatus(true, tail)
	}
}

// writer returns the configured sink, defaulting to stdout.
func (p *streamPrinter) writer() io.Writer {
	if p.out != nil {
		return p.out
	}
	return os.Stdout
}

// onContent is the StreamChat callback: accumulate, then print the portion that
// is safe to show — everything before a tool-call marker, holding back a
// possible partial marker straddling the chunk boundary.
func (p *streamPrinter) onContent(s string) {
	p.buf.WriteString(s)
	if p.suppress {
		return
	}
	full := p.buf.String()
	if i := strings.Index(full, toolMarker); i >= 0 {
		p.emit(full[p.printed:i])
		p.printed = len(full) // skip the markup entirely
		p.suppress = true
		return
	}
	// Hold back len(toolMarker)-1 trailing bytes: they might be the start of a
	// marker that completes in the next chunk.
	safe := len(full) - (len(toolMarker) - 1)
	if safe < p.printed {
		safe = p.printed
	}
	p.emit(full[p.printed:safe])
	p.printed = safe
}

// begin stops the spinner and prints the assistant gutter once, on the first
// visible content. The gutter is left open (no trailing newline) so the first
// fragment — raw bytes, or the first rendered block with its leading margin
// trimmed — sits on the same line as the timestamp.
func (p *streamPrinter) begin() {
	if p.began {
		return
	}
	if p.spinner != nil {
		p.spinner.Stop()
	}
	if p.onStatus != nil {
		p.onStatus(false, "") // answer started — clear the thinking status
	}
	icon, color := Message{Role: "assistant"}.gutter()
	fmt.Fprint(p.writer(), gutterPrefix(icon, color, time.Now()))
	p.gutterOpen = p.md != nil // render mode: first block joins this line
	p.began = true
}

// emit writes a fragment. In raw mode (md nil) it streams bytes straight
// through, as before. In render mode it accumulates prose and flushes each
// complete markdown block through glamour as soon as it closes.
func (p *streamPrinter) emit(s string) {
	if s == "" {
		return
	}
	if p.md == nil {
		p.begin()
		fmt.Fprint(p.writer(), s)
		return
	}
	p.pending += s
	blocks, rest := splitBlocks(p.pending)
	p.pending = rest
	for _, b := range blocks {
		p.writeBlock(b)
	}
}

// writeBlock renders one complete markdown block and prints it. Blank blocks
// are skipped so separators don't leave gaps. The first block after the gutter
// has its leading margin trimmed so it joins the timestamp line; later blocks
// flow beneath at glamour's own indent.
func (p *streamPrinter) writeBlock(b string) {
	if strings.TrimSpace(b) == "" {
		return
	}
	p.begin()
	out := p.md.render(b)
	if p.gutterOpen {
		out = trimLeadingIndent(out)
		p.gutterOpen = false
	}
	fmt.Fprintln(p.writer(), out)
}

// finish flushes any held-back tail (when no marker ever appeared) plus, in
// render mode, the final unterminated block, then closes the line. Returns
// whether the gutter was printed (i.e. anything was shown).
func (p *streamPrinter) finish() bool {
	if !p.suppress {
		if full := p.buf.String(); p.printed < len(full) {
			p.emit(full[p.printed:])
			p.printed = len(full)
		}
	}
	if p.md != nil && strings.TrimSpace(p.pending) != "" {
		p.writeBlock(p.pending)
		p.pending = ""
	}
	// Raw mode terminates the streamed line here; render mode already newline-
	// terminated every block in writeBlock.
	if p.began && p.md == nil {
		fmt.Fprintln(p.writer())
	}
	return p.began
}

// send runs one model call. In streaming mode (the default) it echoes prose
// live via a streamPrinter and returns streamed=true so Resolve doesn't
// re-print. The blocking fallback keeps the old spinner-around-the-call
// behavior and returns streamed=false (Resolve prints). Either way the spinner
// is fully stopped and the line is clean before this returns.
func (cs *CortexSession) send(ctx context.Context) (res *AgentResponse, streamed bool, err error) {
	// Headless: no spinner, no live token echo — the caller reads the reply
	// from TurnResult and owns all output.
	if cs.quiet {
		res, err = cs.Request.Send(ctx)
		return res, false, err
	}
	// Anchored REPL: no standalone spinner — the "thinking" indicator lives on
	// the pinned status row, and prose streams above it (stdout is redirected to
	// the anchor's pipe). Always streaming here (anchored mode requires it).
	if cs.live != nil {
		cs.live.SetThinking(true, "")
		p := &streamPrinter{md: cs.markdown(), onStatus: cs.live.SetThinking}
		res, err = cs.Request.SendStream(ctx, p.onContent, p.onReasoning)
		p.finish()
		cs.live.SetThinking(false, "")
		return res, true, err
	}
	s := NewSpinner()
	s.Start()
	if !streamingEnabled() {
		res, err = cs.Request.Send(ctx)
		s.Stop()
		return res, false, err
	}
	p := &streamPrinter{spinner: s, md: cs.markdown()}
	res, err = cs.Request.SendStream(ctx, p.onContent, p.onReasoning)
	p.finish()
	if !p.began {
		s.Stop()
	}
	return res, true, err
}

// runAnchoredTurn runs one turn with the prompt pinned to the bottom row and
// every byte of turn output funneled above it. os.Stdout is redirected through
// a pipe whose lines feed the anchor (so ad-hoc fmt.Print output, tool-action
// lines, and the streamed answer all land above the prompt); the anchor draws
// the input and "thinking" status straight to the real terminal. Keystrokes
// typed during the turn edit the pinned line live and are returned to seed the
// next prompt. ESC/Ctrl-C cancels via the anchor's context.
func runAnchoredTurn(session *CortexSession, editor *lineedit.Terminal, input, seed string) (string, error) {
	anchor, ctx := editor.Anchor(session.Prompt(), seed)
	r, w, err := os.Pipe()
	if err != nil {
		// Pipe setup failed (rare): fall back to the silent-capture path so the
		// turn still runs and cancels cleanly.
		anchor.Stop()
		c, stop := editor.Interruptible(context.Background())
		_, e := session.Turn(c, input)
		return stop(), e
	}
	realStdout := os.Stdout
	os.Stdout = w
	session.live = anchor

	drained := make(chan struct{})
	go func() {
		defer close(drained)
		sc := bufio.NewScanner(r)
		sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
		for sc.Scan() {
			anchor.EmitLine(sc.Text())
		}
	}()

	_, turnErr := session.Turn(ctx, input)

	// Restore stdout, then close the write end so the drain goroutine sees EOF
	// and flushes the last line before we erase the pinned block.
	os.Stdout = realStdout
	session.live = nil
	w.Close()
	<-drained
	r.Close()
	return anchor.Stop(), turnErr
}

// runToolCalls executes every requested call and appends one tool result per
// call ID — even after an interrupt. The wire invariant is that every
// assistant(tool_calls) id gets a matching tool message or the next send 400s,
// so a mid-turn cancel records "interrupted" results rather than dropping them.
func (cs *CortexSession) runToolCalls(ctx context.Context, calls []ToolCall) {
	for _, tc := range calls {
		var content string
		if ctx.Err() != nil {
			content = "Error: interrupted by user before this tool ran"
		} else {
			// Animate a status row while the tool runs — study especially makes
			// several model passes with no output until it returns.
			cs.startActivity(tc.activityLabel())
			out, err := tc.Execute(ctx, cs)
			cs.stopActivity()
			if err != nil {
				// Tool errors are observations, not crashes: feed them back so
				// the model can self-correct.
				content = "Error: " + err.Error()
			} else {
				content = out
			}
		}
		cs.Append(Message{
			Role:       RoleTool,
			ToolCallID: tc.ID,
			Content:    content,
		})
	}
}

// activityLabel is the concise "tool(arg)" shown on the spinning status row
// while a tool runs — enough to tell which call is in flight without the full
// argument dump printToolAction already recorded above.
func (tc ToolCall) activityLabel() string {
	name := tc.Function.Name
	if p, err := tc.stringArg("path"); err == nil && p != "" {
		return name + "(" + p + ")"
	}
	if c, err := tc.stringArg("command"); err == nil && c != "" {
		first := firstLine(c)
		if len(first) > 40 {
			first = first[:40] + "…"
		}
		return name + "(" + first + ")"
	}
	return name
}

// startActivity/stopActivity drive the anchored status spinner around a unit of
// work. No-op outside the anchored REPL (raw-streaming and headless modes have
// no pinned status row to animate).
func (cs *CortexSession) startActivity(label string) {
	if cs.live != nil {
		cs.live.SetActivity(label)
	}
}

func (cs *CortexSession) stopActivity() {
	if cs.live != nil {
		cs.live.SetActivity("")
	}
}

// toolCallSignature is a stable fingerprint of a tool-call batch, used by the
// inner loop to detect a model stuck re-issuing the same call. Order matters;
// call IDs (which vary every time) are deliberately excluded so two batches
// that differ only by ID compare equal.
func toolCallSignature(calls []ToolCall) string {
	var b strings.Builder
	for _, c := range calls {
		b.WriteString(c.Function.Name)
		b.WriteByte(0)
		b.WriteString(c.Function.Arguments)
		b.WriteByte('\n')
	}
	return b.String()
}

// Resolve runs the agentic inner loop for one user turn: send, run any tools
// the model asked for, feed the results back, and re-send — repeating until the
// model answers with no more tool calls (or we hit the iteration cap). The
// caller appends the user message; Resolve owns everything from there. A
// canceled ctx ends the turn at the next boundary with history left valid.
func (cs *CortexSession) Resolve(ctx context.Context) error {
	var lastSig string
	var repeats int // consecutive batches identical to lastSig, including current
	for i := 0; i < maxToolIterations; i++ {
		res, streamed, err := cs.send(ctx)
		if err != nil {
			return fmt.Errorf("model response error: %w", err)
		}
		if res == nil || len(res.Choices) == 0 {
			return fmt.Errorf("no choices in agent response")
		}
		// prompt_tokens reflects the whole re-sent history = current context fill.
		cs.LastPromptTokens = res.Usage.PromptTokens
		// Every send is a billed call (prompt re-sent each time), so summing
		// across the tool loop reflects real session cost.
		cs.tokensIn += res.Usage.PromptTokens
		cs.tokensOut += res.Usage.CompletionTokens
		cs.costUSD += res.Usage.Cost
		msg := res.Choices[0].Message

		// Fallback: if the proxy didn't normalize Qwen's native XML tool-call
		// format, recover it from the content so the call isn't silently lost
		// (empty tool_calls would otherwise be treated as a final answer).
		if len(msg.ToolCalls) == 0 {
			if calls := parseXMLToolCalls(msg.Content); len(calls) > 0 {
				msg.ToolCalls = calls
				msg.Content = stripToolMarkup(msg.Content)
			}
		}

		// (1) Append the assistant message BEFORE any tool results. The API
		// requires the sequence assistant(tool_calls) -> tool(result).
		cs.Append(msg)

		// Print any prose the model emitted (a final answer, or a preamble
		// alongside tool calls). The streaming path already echoed it live, so
		// only the blocking fallback prints here.
		if !streamed && !cs.quiet && strings.TrimSpace(msg.Content) != "" {
			msg.Print()
		}

		// (2) No tool calls => the model is done with this turn.
		if len(msg.ToolCalls) == 0 {
			return nil
		}

		// No-progress guard: a weak model handed a content-free result can
		// re-issue the identical batch forever. Track consecutive repeats and
		// break the loop before it burns the whole turn.
		if sig := toolCallSignature(msg.ToolCalls); sig == lastSig {
			repeats++
		} else {
			lastSig, repeats = sig, 1
		}

		// (3) Run the tools, then stop at the iteration boundary if the user
		// interrupted — history is valid, and the model can pick up next turn.
		cs.runToolCalls(ctx, msg.ToolCalls)
		if err := ctx.Err(); err != nil {
			return err
		}

		// Abort once the same batch has repeated past the cap: the model is not
		// going to recover on its own. The nudge below already gave it a chance.
		if repeats >= maxRepeatedToolCalls {
			fmt.Println(withColor("stopped: model repeated the same tool call with no progress", yellow))
			return nil
		}
		// One repeat short of the cap, inject a nudge so the model can change
		// course (it sees the result was identical and tries something else).
		if repeats == maxRepeatedToolCalls-1 {
			cs.Append(Message{
				Role:    RoleUser,
				Content: "Harness note: that tool call was byte-identical to the previous one and produced the same result. Repeating it will not yield new information — try a different command or approach, or stop and report what you've found.",
			})
		}
		// Loop: the next send picks up the grown history.
	}
	return fmt.Errorf("exceeded max tool iterations (%d)", maxToolIterations)
}

// TurnResult carries the outcome of one user turn for a transport-agnostic
// caller. Reply is the model's final prose (the last assistant message with no
// tool calls in this turn); in the interactive REPL it has already streamed to
// the terminal, but a headless driver — the `loop turn` entrypoint or the Discord adapter —
// reads it from here. Interrupted is true when the turn ended on a canceled ctx
// with history left valid (the model can pick up next turn).
type TurnResult struct {
	Reply       string
	Interrupted bool
}

// Turn runs one full user turn end to end: it appends the user message, attaches
// this turn's fast-retrieval context (cleared after, so it neither accumulates
// nor persists), runs the agentic Resolve loop, and — on success — captures the
// turn to the store (retrievable next time) and buffers it for background
// distillation. It is the single driveable entry point shared by the
// interactive REPL and any headless caller; the caller owns input acquisition,
// display, compaction, and the cancelable ctx.
func (cs *CortexSession) Turn(ctx context.Context, input string) (TurnResult, error) {
	// Preempt any background distillation so this turn's foreground model work
	// gets the endpoint (it resumes at turn end over the new backlog).
	cs.stopDistill()

	// Mark where this turn's messages begin, so we can capture what it did.
	turnStart := len(cs.Request.Messages)
	cs.Append(Message{Role: RoleUser, Content: input})

	// Fast retrieval for THIS turn. Hits are recorded to the transcript
	// (kindRetrieval — the durable record of what the model was given) and
	// merged into the system message for the wire only (EphemeralSystem),
	// cleared after the turn so they don't accumulate or persist.
	hits := cs.retrieve(input)
	cs.recordRetrieval(input, hits)
	note := formatRetrieved(hits)
	cs.Request.EphemeralSystem = note
	if note != "" {
		cs.retrievals++
		cs.injectedChars += len(note)
	}

	err := cs.Resolve(ctx)
	cs.Request.EphemeralSystem = ""
	cs.turns++

	if err != nil {
		return TurnResult{Interrupted: errors.Is(err, context.Canceled)}, err
	}

	// Capture the completed turn to the store (retrievable next time) BEFORE any
	// compaction the caller may run rewrites history. Every turn, read-only
	// included — see captureTurn. Tier 2: buffer and distill async (on the
	// reasoner, during the idle gap before the next prompt).
	turnMsgs := cs.Request.Messages[turnStart:]
	cs.captureTurn(input, turnMsgs)
	cs.noteTurn(input, turnMsgs)

	return TurnResult{Reply: lastAssistantText(turnMsgs)}, nil
}

// lastAssistantText returns the prose of the final assistant message in a turn's
// message slice — the answer a headless caller relays back to its transport.
// Tool-call-only assistant messages (empty content) are skipped, so the result
// is the model's actual reply, not an intermediate tool dispatch.
func lastAssistantText(turnMsgs []Message) string {
	for i := len(turnMsgs) - 1; i >= 0; i-- {
		m := turnMsgs[i]
		if m.Role == "assistant" && strings.TrimSpace(m.Content) != "" {
			return m.Content
		}
	}
	return ""
}

// compactNow runs Compact with Ctrl-C wired and prints the outcome. Shared by
// the manual /compact, the red-gauge auto-trigger, and overflow recovery.
func compactNow(session *CortexSession, reason string) {
	fmt.Println(withColor(reason+" — compacting via study…", yellow))
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	if err := session.Compact(ctx); err != nil {
		fmt.Printf("compact: %v\n", err)
		return
	}
	fmt.Println(withColor("compacted → session "+session.SessionID, gray))
}

// runStudyCLI invokes the study tool directly and prints the curated context —
// no coding model, no REPL. For inspecting what study returns in isolation:
//
//	loop study <path> [goal...] [passes]
func runStudyCLI(path, goal string, passes int) {
	session := NewCortexSession()
	args, _ := json.Marshal(map[string]any{"path": path, "goal": goal, "passes": passes})
	call := ToolCall{Function: FunctionCall{Name: FunctionStudy, Arguments: string(args)}}
	out, err := call.Study(context.Background(), session)
	if err != nil {
		fmt.Println("study error:", err)
		return
	}
	fmt.Println("\n--- curated context ---")
	fmt.Println(out)
}

// runTurnCLI implements `loop turn`: one headless agent turn over a fresh or
// resumed session, with clean machine-readable output. It is the integration
// seam for the headless + Discord architecture — an external driver invokes it
// with the persistent session's id and the user's message, and reads the reply
// (plain on stdout, or a JSON object with --json). All progress chatter and the
// resolved session id go to stderr so stdout carries only the reply.
func runTurnCLI(args []string) {
	sessionID, asJSON := "", false
	var rest []string
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--session", "-s":
			if i+1 < len(args) {
				sessionID = args[i+1]
				i++
			}
		case "--json":
			asJSON = true
		default:
			rest = append(rest, args[i])
		}
	}

	// Input from the args, or — when none are given — the whole of stdin, so a
	// driver can pipe a message body without shell-quoting it.
	input := strings.TrimSpace(strings.Join(rest, " "))
	if input == "" {
		if b, err := io.ReadAll(os.Stdin); err == nil {
			input = strings.TrimSpace(string(b))
		}
	}
	if input == "" {
		fmt.Fprintln(os.Stderr, "usage: loop turn [--session <id>] [--json] <input>")
		os.Exit(2)
	}

	session := NewCortexSession()
	session.quiet = true // headless: reply comes back via TurnResult, not stdout
	if sessionID != "" {
		if err := session.ResumeTranscript(sessionID); err != nil {
			fmt.Fprintf(os.Stderr, "resume %s: %v — starting fresh\n", sessionID, err)
			session.StartTranscript()
		}
	} else {
		session.StartTranscript()
	}
	session.EnableRetrieval()
	defer session.Close()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	res, turnErr := session.Turn(ctx, input)

	// Settle background distillation and record the session so a one-shot
	// invocation still contributes its insight + eval metrics.
	if session.turns > 0 {
		session.stopDistill()
		session.emitSessionMetrics()
	}

	if asJSON {
		out := map[string]any{"session": session.SessionID, "reply": res.Reply}
		if turnErr != nil {
			out["error"] = turnErr.Error()
			out["interrupted"] = res.Interrupted
		}
		b, _ := json.Marshal(out)
		fmt.Println(string(b))
	} else {
		if turnErr != nil {
			fmt.Fprintf(os.Stderr, "turn error: %v\n", turnErr)
		}
		if res.Reply != "" {
			fmt.Println(res.Reply)
		}
		fmt.Fprintf(os.Stderr, "session: %s\n", session.SessionID)
	}
	if turnErr != nil {
		os.Exit(1)
	}
}

func main() {
	// Study-eval mode: `loop study-eval` runs study over a fixture set and scores
	// latency / coverage / groundedness. `loop study-eval code-grid` runs the
	// 2×2 granularity × numbering isolation experiment on the code fixture.
	if len(os.Args) >= 2 && os.Args[1] == "study-eval" {
		if len(os.Args) >= 3 && os.Args[2] == "code-grid" {
			runStudyEvalCodeGrid()
			return
		}
		runStudyEval()
		return
	}

	// Direct study mode: `loop study <path> [goal...] [passes]`. A trailing bare
	// integer is taken as the deepening pass count; 0 (the default) lets the
	// study tool pick — 1 for files, dirStudyPasses for directories.
	if len(os.Args) >= 3 && os.Args[1] == "study" {
		rest := os.Args[2:]
		path, goalParts, passes := rest[0], rest[1:], 0
		if n := len(goalParts); n > 0 {
			if p, err := strconv.Atoi(goalParts[n-1]); err == nil && p > 0 {
				passes, goalParts = p, goalParts[:n-1]
			}
		}
		runStudyCLI(path, strings.Join(goalParts, " "), passes)
		return
	}

	// Headless single-turn mode: `loop turn [--session <id>] [--json] <input…>`
	// (or the input on stdin). Runs exactly one Turn over a fresh or resumed
	// session and prints the model's reply — the seam a headless driver or Discord
	// adapter shells into. Pass the persistent session's id to keep the same
	// conversation + shared .cortex/ across invocations ("one session at a
	// time"); the id is echoed on stderr so a driver can thread it forward.
	if len(os.Args) >= 2 && os.Args[1] == "turn" {
		runTurnCLI(os.Args[2:])
		return
	}

	// One-change-at-a-time git lifecycle: `loop change <start|commit|status>`.
	// A driver runs these around an agent turn so each change lands on its own
	// branch, isolated and reviewable. Local only — see change.go.
	if len(os.Args) >= 2 && os.Args[1] == "change" {
		if err := runChangeCLI(os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}

	// Discord adapter: `loop discord` connects to Discord and drives one
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

	// `loop resume [id]` continues a prior session (the latest when no id is
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
			fmt.Printf("%s\n", withColor(fmt.Sprintf("resumed %s (%d messages)", session.SessionID, len(session.Request.Messages)), gray))
		}
	} else {
		session.StartTranscript()
	}

	// Fast retrieval over .cortex/ (best-effort; disabled cleanly if the store
	// can't open). Shut down with the transcript at exit.
	session.EnableRetrieval()
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
			// Background cognition (Think/Dream) logs via the global logger to
			// stderr; in an interactive session that lands on top of the prompt.
			// Divert it to a file so the terminal stays clean (jq-free debugging
			// still available via tail -f .cortex/loop.log).
			if lf, err := os.OpenFile(filepath.Join(contextDir(), "loop.log"),
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

		// /remember <text> stores an explicit memory — the highest-precision
		// capture, since the user marked it worth keeping.
		if input == "/remember" || strings.HasPrefix(input, "/remember ") {
			text := strings.TrimSpace(strings.TrimPrefix(input, "/remember"))
			if text == "" {
				fmt.Println("usage: /remember <text to store as a memory>")
			} else if err := session.remember(text); err != nil {
				fmt.Printf("remember: %v\n", err)
			} else {
				fmt.Println(withColor("remembered", gray))
			}
			continue
		}

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

	// Settle background distillation so its insight count is final, then report
	// and record the session. emitSessionMetrics rides the eval journal class.
	if session.turns > 0 {
		session.stopDistill()
		session.emitSessionMetrics()
		fmt.Println(withColor(session.sessionSummary(), gray))
		// Pre-fill the resume command with this session's id so picking it back
		// up is copy-paste, not a hunt through .cortex/sessions/.
		if session.SessionID != "" {
			fmt.Println(withColor(fmt.Sprintf("resume: %s resume %s", invokedName(), session.SessionID), gray))
		}
	}
	fmt.Println("exiting")
}
