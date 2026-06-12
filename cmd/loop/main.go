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
	"math/rand"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"runtime/debug"
	"strconv"
	"strings"
	"time"

	"github.com/dereksantos/cortex/internal/study"
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
[ ] Capture at turn end (cortex capture — distilled insights are what journals)
[x] Compaction-as-study (red-gauge answer) + /clear + overflow recovery
[ ] Retrieval injection at turn start
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

const defaultRole = RoleUser
const defaultModel = ModelCoder

// maxToolIterations bounds the agentic inner loop so a confused model can't
// spin forever burning tokens. The smallest form of the "bounded" principle.
const maxToolIterations = 100

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

// Model roles. The harness routes each kind of work to a model binding: the
// coding turn uses "code", the study tool uses "study". One mechanism — new
// nodes (think/dream/dag) just add roles. See Config.Spec.
const (
	roleCode  = "code"
	roleStudy = "study"
)

// defaultModels is the built-in role → binding map, used when config doesn't
// override a role. Window is the model's RAW context size (a starting estimate,
// config-overridable, and self-corrected at runtime from overflow errors); the
// sampling budget and density are derived from it, not hardcoded. coder for
// coding; reasoner for study (fast + concise; study is read-heavy/think-light).
//
// Both chatterbox aliases serve hybrid thinking models, and both roles are
// bounded micro-calls where built-in reasoning starves the completion budget
// (measured: the reasoner burned a full max_tokens on reasoning_content and
// returned empty content, collapsing study coverage). Thinking is therefore
// off by default for both roles; re-enable per-role via config when a role
// genuinely wants deliberation.
var defaultModels = map[string]ModelSpec{
	roleCode:  {Endpoint: "http://chatterbox:4000", Model: "coder", Window: 65536, Thinking: &thinkingOff},
	roleStudy: {Endpoint: "http://chatterbox:4000", Model: "reasoner", Window: 32768, Thinking: &thinkingOff},
}

// thinkingOff exists so defaultModels can take a *bool address.
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

func withColor(v string, c string) string {
	return fmt.Sprintf("%s%s%s", c, v, reset)
}

const spinnerWidth = 3

// fleckEvery: roughly 1 in N columns is a fleck instead of terrain.
const fleckEvery = 9

// The spinner palette. heights is the terrain ramp the random walk moves along;
// flecks are the foam (▒▓), mist (░), drifting motes (⠂⠄) and surfacing bubble
// (∘) sprinkled in. All single-width.
var (
	heights = []rune("▁▂▃▄▅▆▇█")
	flecks  = []rune("▒▓░⠂⠄∘")
)

// scroller generates an endless side-scroller as a random walk: each new column
// drifts the terrain height up/down by a step, with the odd fleck mixed in. It
// keeps the breathing-swell look of the old fixed track but never repeats. The
// rng is seeded so a given seed is fully reproducible (see tests).
type scroller struct {
	rng    *rand.Rand
	height int
	window []rune
}

func newScroller(seed int64) *scroller {
	s := &scroller{rng: rand.New(rand.NewSource(seed)), height: len(heights) / 2}
	s.window = make([]rune, spinnerWidth)
	for i := range s.window {
		s.window[i] = s.next()
	}
	return s
}

// next advances the walk one column and returns the incoming glyph.
func (s *scroller) next() rune {
	if s.rng.Intn(fleckEvery) == 0 {
		return flecks[s.rng.Intn(len(flecks))]
	}
	s.height += s.rng.Intn(3) - 1 // -1, 0, or +1
	if s.height < 0 {
		s.height = 0
	}
	if s.height >= len(heights) {
		s.height = len(heights) - 1
	}
	return heights[s.height]
}

// frame scrolls one step: shift the window left and append a fresh column.
func (s *scroller) frame() string {
	copy(s.window, s.window[1:])
	s.window[len(s.window)-1] = s.next()
	return string(s.window)
}

// Spinner renders an in-place animation on stdout while we wait on the model.
// It is meant to wrap a single network call: Stop() blocks until the goroutine
// has actually exited and then erases the line, so no frame can bleed into
// output printed afterward. That guarantee is the whole point — the old version
// kept spinning during tool execution and interleaved glyphs with real output.
type Spinner struct {
	stopChan chan struct{}
	doneChan chan struct{}
}

func NewSpinner() *Spinner { return &Spinner{} }

func (s *Spinner) Start() {
	s.stopChan = make(chan struct{})
	s.doneChan = make(chan struct{})
	sc := newScroller(time.Now().UnixNano())
	go func() {
		defer close(s.doneChan)
		for {
			select {
			case <-s.stopChan:
				return
			default:
				fmt.Printf("\r%s", withColor(sc.frame(), cyan))
				time.Sleep(90 * time.Millisecond)
			}
		}
	}()
}

// Stop halts the spinner, waits for its goroutine to exit, then erases the
// line (\r + clear-to-end-of-line) so the cursor is clean for the next print.
func (s *Spinner) Stop() {
	close(s.stopChan)
	<-s.doneChan
	fmt.Print("\r\033[K")
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
	// BaseURL is the endpoint root (e.g. http://chatterbox:4000), resolved from
	// config. Not serialized — it's transport, not request body.
	BaseURL string `json:"-"`
	// APIKey is the Bearer token for endpoints that need one (e.g. OpenRouter).
	// Empty for local endpoints. Not serialized.
	APIKey string `json:"-"`
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
	"Replace an exact substring in a file. old_string must appear EXACTLY ONCE; "+
		"include enough surrounding context to make it unique. Prefer this over "+
		"write_file for small changes to an existing file.",
	objectSchema(map[string]any{
		"path":       stringProp("Path to the file to edit."),
		"old_string": stringProp("The exact text to find. Must match exactly once, including whitespace."),
		"new_string": stringProp("The text to replace it with. May be empty to delete old_string."),
	}, "path", "old_string", "new_string"))

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

// sampleBudget is the token budget for one study pass: the window minus headroom
// for the inference template and the model's completion. Derived from the
// window, never a magic number.
func sampleBudget(window int) int {
	headroom := window / 4
	if headroom < 2048 {
		headroom = 2048
	}
	return window - headroom
}

// studyCompletionCap is the max_tokens granted to one study inference
// response. The response carries the digest plus one citation per claim
// (each repeating the file path), which routinely exceeds the client's
// 1024-token default — truncating the JSON mid-array, which silently
// degrades to a digest with zero citations. Half the window headroom
// usually suffices, but at small (forced) windows that collapses back
// to the truncation point, so it floors at 2048. The floor can nominally
// overshoot a genuinely tiny window; the overflow self-correction
// (learnedWindows) handles that case, while a silently citation-less
// study would not self-correct at all.
func studyCompletionCap(window int) int {
	c := (window - sampleBudget(window)) / 2
	if c < 2048 {
		c = 2048
	}
	return c
}

var bash = newTool(FunctionBash,
	"Run a shell command. Only allowlisted commands are permitted; no pipes or redirects.",
	objectSchema(map[string]any{
		"command": stringProp("The command to run, e.g. 'go test ./...' or 'ls cmd'."),
	}, "command"))

var tools = []Tool{readFile, writeFile, editFile, studyTool, bash}

// httpClient is shared by all model calls. The timeout is the backstop guard:
// without it a server that accepts the request and never answers hangs the
// REPL forever.
var httpClient = &http.Client{Timeout: requestTimeout}

// Send runs one model call with bounded retry. Transient failures (transport
// errors, 429/5xx) retry up to maxSendAttempts with linear backoff; anything
// else — including a canceled ctx — returns immediately.
func (r *AgentRequest) Send(ctx context.Context) (*AgentResponse, error) {
	b, err := json.Marshal(r)
	if err != nil {
		return nil, fmt.Errorf("error marshaling agent request: %w", err)
	}

	base := r.BaseURL
	if base == "" {
		base = defaultModels[roleCode].Endpoint
	}
	url := strings.TrimRight(base, "/") + "/v1/chat/completions"

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
	provider.SetMaxTokens(studyCompletionCap(cs.studyWindow()))

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
		req.Window = sampleBudget(window) // window minus headroom for template + completion
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
// current cortex turn, e.g. "  ▸ read_file(go.mod)".
func printToolAction(action string) {
	fmt.Printf("  %s\n", withColor(iconTool+" "+action, green))
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

// EditFile replaces an exact, unique substring in a file. Requiring old_string
// to match exactly once is the safety property: if it's missing the edit is
// wrong, and if it's ambiguous we refuse rather than guess which occurrence the
// model meant. Both cases return an error that goes back as an observation, so
// the model can add context and retry.
func (tc ToolCall) EditFile() (string, error) {
	path, err := tc.stringArg("path")
	if err != nil {
		return "", err
	}
	oldStr, err := tc.stringArg("old_string")
	if err != nil {
		return "", err
	}
	newStr, err := tc.stringArg("new_string")
	if err != nil {
		return "", err
	}
	if oldStr == "" {
		return "", fmt.Errorf("old_string must not be empty")
	}
	if oldStr == newStr {
		return "", fmt.Errorf("old_string and new_string are identical; nothing to change")
	}

	printToolAction(fmt.Sprintf("edit_file(%s)", path))

	info, err := os.Stat(path)
	if err != nil {
		return "", fmt.Errorf("stat %s: %w", path, err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read %s: %w", path, err)
	}
	content := string(data)

	switch n := strings.Count(content, oldStr); n {
	case 0:
		return "", fmt.Errorf("old_string not found in %s", path)
	case 1:
		// exactly one match — the only safe case
	default:
		return "", fmt.Errorf("old_string found %d times in %s; add surrounding context so it matches exactly once", n, path)
	}

	updated := strings.Replace(content, oldStr, newStr, 1)
	if err := os.WriteFile(path, []byte(updated), info.Mode()); err != nil {
		return "", fmt.Errorf("write %s: %w", path, err)
	}
	return fmt.Sprintf("edited %s (replaced %d bytes with %d)", path, len(oldStr), len(newStr)), nil
}

// bashAllowlist gates which binaries the bash tool may run. This is a guardrail
// against the model doing something catastrophic by accident — NOT a security
// sandbox. We exec the binary directly (no shell), so `;`, `&&`, `|`, `>` are
// inert: a command is always a single allowlisted binary plus literal args.
var bashAllowlist = map[string]bool{
	"ls": true, "cat": true, "head": true, "tail": true, "wc": true,
	"grep": true, "find": true, "echo": true, "pwd": true, "tree": true,
	"go": true, "git": true, "gofmt": true,
}

// bashShellSyntax matches shell metacharacters the bash tool cannot honor
// (we exec the binary directly, no shell). Without an explicit rejection
// the operator reaches the underlying binary as a literal argument and the
// model gets a confusing downstream error — `find . | head` yields
// "find: |: unknown primary", which models retry verbatim instead of
// adapting (observed: 3 wasted turns in one session, 2026-06-12).
var bashShellSyntax = regexp.MustCompile(`[|><;&]|\$\(`)

func (tc ToolCall) Bash(ctx context.Context, cs *CortexSession) (string, error) {
	command, err := tc.stringArg("command")
	if err != nil {
		return "", err
	}
	fields := strings.Fields(command)
	if len(fields) == 0 {
		return "", fmt.Errorf("empty command")
	}
	if m := bashShellSyntax.FindString(command); m != "" {
		return "", fmt.Errorf("shell syntax %q is not supported (commands run without a shell — no pipes, redirects, or chaining); run the bare command instead, e.g. %q", m, fields[0]+" ...")
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

// render formats the message as "<icon> HH:MM:SS  <content>", the gutter colored
// by source. ts is injected so the formatting is testable.
func (m Message) render(ts time.Time) string {
	icon, color := m.gutter()
	gutter := withColor(fmt.Sprintf("%s %s", icon, ts.Format("15:04:05")), color)
	return fmt.Sprintf("%s  %s", gutter, m.Content)
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
	// Config is the loaded .cortex/config.json (may be nil).
	Config *Config
	// SessionID names this session's transcript file; "" when unpersisted.
	SessionID string
	// transcript is the open .cortex/sessions/<id>.jsonl file Append writes
	// through to. nil when the session is unpersisted (study CLI, tests).
	transcript *os.File
}

// SetModel switches the active coding model id. The code endpoint is unchanged
// (models on the same endpoint swap freely); history is preserved.
func (cs *CortexSession) SetModel(model string) { cs.Request.Model = model }

// windowSize is the code model's context window — the gauge denominator and the
// read_file size threshold. Falls back to the built-in default.
func (cs CortexSession) windowSize() int {
	if cs.Window > 0 {
		return cs.Window
	}
	return defaultModels[roleCode].Window
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
	// chatterbox coder alias). false → requests carry
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

// Config is the subset of .cortex/config.json the loop consults: a role → model
// binding map. The old harness's other keys (endpoints/routing/ollama_*) are
// ignored — the loop routes through one mechanism.
type Config struct {
	Models map[string]ModelSpec `json:"models"`
}

// Spec resolves a role to its binding: a per-field config override layered on the
// built-in default, so a config can set just the model and inherit the rest.
func (c *Config) Spec(role string) ModelSpec {
	spec := defaultModels[role]
	if c != nil {
		if m, ok := c.Models[role]; ok {
			if m.Endpoint != "" {
				spec.Endpoint = m.Endpoint
			}
			if m.Model != "" {
				spec.Model = m.Model
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
	}
	return spec
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

	// Resolve the code role: the model + endpoint + window for the coding turn.
	code := cfg.Spec(roleCode)
	req.Model = code.Model
	req.BaseURL = code.Endpoint
	req.APIKey = keychainKey(code.KeyService)
	req.ChatTemplateKwargs = code.TemplateKwargs()

	return &CortexSession{
		Args:    &args,
		Request: req,
		Config:  cfg,
		Window:  code.Window,
		Study:   cfg.Spec(roleStudy), // study role: small long-context model
	}
}

func (cs CortexSession) PrintArgs() {
	fmt.Printf("Cortex Model: %s Temp:%f\n", cs.Request.Model, cs.Request.Temperature)
}

func (cs CortexSession) Append(message Message) {
	cs.Request.Messages = append(cs.Request.Messages, message)
	cs.writeTranscript(message)
}

// --- Session transcripts -------------------------------------------------
//
// Raw conversations persist as plain JSONL under .cortex/sessions/<id>.jsonl,
// one timestamped message per line (pi-style). Deliberately NOT a journal
// writer-class: the journal records distilled context (capture events,
// insights); raw sessions stay out of it. `cat | jq` always works, and
// .cortex/ is gitignored so transcripts never leave the machine.

// sessionEntry is one transcript line: the message plus when it was appended.
type sessionEntry struct {
	TS time.Time `json:"ts"`
	Message
}

// sessionsDir resolves where transcripts live: alongside the nearest .cortex
// up the tree, or cwd/.cortex when none exists yet.
func sessionsDir() string {
	root := findUp(".cortex")
	if root == "" {
		root = ".cortex"
	}
	return filepath.Join(root, "sessions")
}

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
		msgs = append(msgs, e.Message)
	}
	return msgs, nil
}

// writeTranscript appends one message to the open transcript, if any.
// Best-effort by design: a persistence hiccup must not break the live turn.
func (cs CortexSession) writeTranscript(m Message) {
	if cs.transcript == nil {
		return
	}
	b, err := json.Marshal(sessionEntry{TS: time.Now(), Message: m})
	if err != nil {
		return
	}
	cs.transcript.Write(append(b, '\n'))
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
func (cs CortexSession) contextRatio() float64 {
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
	cs.transcript.Close()
	cs.transcript = nil
	cs.Request.Messages = []Message{sys}
	cs.StartTranscript()
	cs.Append(summary)
	// Fill is unknown until the next send; the gauge resets rather than lies.
	cs.LastPromptTokens = 0
	return nil
}

// Clear resets the conversation to a fresh seed (system prompt + AGENTS.md,
// re-read from disk) and starts a new transcript. The old transcript stays on
// disk; the model binding — including a /model switch — is preserved.
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
func (cs CortexSession) Prompt() string {
	win := cs.windowSize()
	status := withColor(fmt.Sprintf("cortex %s · %s · ", version(), cs.Request.Model), gray)
	gauge := withColor(fmt.Sprintf("%s/%s", humanK(cs.LastPromptTokens), humanK(win)), ctxColor(cs.LastPromptTokens, win))
	return fmt.Sprintf("%s%s  %s ", status, gauge, withColor(promptGlyph, cyan))
}

// send runs one model call with a spinner during the network wait. The spinner
// is fully stopped and the line erased before this returns, so the caller can
// print immediately without interleaving.
func (cs CortexSession) send(ctx context.Context) (*AgentResponse, error) {
	s := NewSpinner()
	s.Start()
	res, err := cs.Request.Send(ctx)
	s.Stop()
	return res, err
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
		} else if out, err := tc.Execute(ctx, cs); err != nil {
			// Tool errors are observations, not crashes: feed them back so
			// the model can self-correct.
			content = "Error: " + err.Error()
		} else {
			content = out
		}
		cs.Append(Message{
			Role:       RoleTool,
			ToolCallID: tc.ID,
			Content:    content,
		})
	}
}

// Resolve runs the agentic inner loop for one user turn: send, run any tools
// the model asked for, feed the results back, and re-send — repeating until the
// model answers with no more tool calls (or we hit the iteration cap). The
// caller appends the user message; Resolve owns everything from there. A
// canceled ctx ends the turn at the next boundary with history left valid.
func (cs *CortexSession) Resolve(ctx context.Context) error {
	for i := 0; i < maxToolIterations; i++ {
		res, err := cs.send(ctx)
		if err != nil {
			return fmt.Errorf("model response error: %w", err)
		}
		if res == nil || len(res.Choices) == 0 {
			return fmt.Errorf("no choices in agent response")
		}
		// prompt_tokens reflects the whole re-sent history = current context fill.
		cs.LastPromptTokens = res.Usage.PromptTokens
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
		// alongside tool calls).
		if strings.TrimSpace(msg.Content) != "" {
			msg.Print()
		}

		// (2) No tool calls => the model is done with this turn.
		if len(msg.ToolCalls) == 0 {
			return nil
		}

		// (3) Run the tools, then stop at the iteration boundary if the user
		// interrupted — history is valid, and the model can pick up next turn.
		cs.runToolCalls(ctx, msg.ToolCalls)
		if err := ctx.Err(); err != nil {
			return err
		}
		// Loop: the next send picks up the grown history.
	}
	return fmt.Errorf("exceeded max tool iterations (%d)", maxToolIterations)
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

	scanner := bufio.NewScanner(os.Stdin)

	for {
		fmt.Print(session.Prompt())
		if !scanner.Scan() {
			if err := scanner.Err(); err != nil {
				fmt.Printf("scanner error: %v\n", err)
			}
			break
		}
		input := strings.TrimSpace(scanner.Text())
		if input == "" {
			continue
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

		session.Append(Message{Role: RoleUser, Content: input})
		// Ctrl-C cancels the in-flight turn (model call or tool) instead of
		// killing the REPL. stop() restores default signal handling at the
		// prompt, so Ctrl-C while idle still exits the process.
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
		err := session.Resolve(ctx)
		stop()
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

	fmt.Println("exiting")
}
