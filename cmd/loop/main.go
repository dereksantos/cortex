package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime/debug"
	"strings"
	"time"
)

/*
TODO:
[x] Scanner animation v1
[x] System prompt
[x] Tool calling v1 (read_file, write_file, bash allowlist)
[x] Basic editing
[x] Bash tool
[x] Tolerate native Qwen XML tool-call format (proxy fallback)
[x] Improve session status line
[x] Improve animation
[ ] Timestamp in messages
[ ] Study tool
[ ] Journal tool
[ ] Integrate cortex dream
[ ] Integrate cortex think
[ ] Integreate cortex dag
[ ] Add hooks and review settings
[ ] Verify cortex v1 loop
[ ] cortex model for cataloging and suggesting model setups based on system resources
[ ] Integrate eval suite into new harness

*/

const SystemPrompt = `Your are cortex, a coding agent focused on a continous quality improvement approach that achieves goals by working towards the simplest principled implementation that follows good system design and code design. Use your best judgement to make sound decisions that favour excellent outcomes over time. Use the provided tools to inspect files before answering.`

const RoleUser = "user"
const RoleSystem = "system"
const RoleTool = "tool"
const ModelCoder = "coder"

const FunctionReadFile = "read_file"
const FunctionWriteFile = "write_file"
const FunctionEditFile = "edit_file"
const FunctionBash = "bash"

const defaultRole = RoleUser
const defaultModel = ModelCoder

// maxToolIterations bounds the agentic inner loop so a confused model can't
// spin forever burning tokens. The smallest form of the "bounded" principle.
const maxToolIterations = 10

// maxToolOutput caps how much tool output we feed back into context, so a
// `cat` of a huge file (or `find` over a big tree) can't blow the window.
const maxToolOutput = 10000

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

// defaultMaxContext is the fallback context window (tokens) used only when the
// config has no max_context_override for the active model. The real value is
// config-driven — see Config / NewCortexSession.
const defaultMaxContext = 65536

// defaultBaseURL is the fallback endpoint used only when the config names no
// endpoint for the active model.
const defaultBaseURL = "http://chatterbox:4000"

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
	// BaseURL is the endpoint root (e.g. http://chatterbox:4000), resolved from
	// config. Not serialized — it's transport, not request body.
	BaseURL string `json:"-"`
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
	"Read the contents of a file at the given path.",
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

var bash = newTool(FunctionBash,
	"Run a shell command. Only allowlisted commands are permitted; no pipes or redirects.",
	objectSchema(map[string]any{
		"command": stringProp("The command to run, e.g. 'go test ./...' or 'ls cmd'."),
	}, "command"))

var tools = []Tool{readFile, writeFile, editFile, bash}

// Sends the request to the models API endpoint
func (r *AgentRequest) Send() (*AgentResponse, error) {
	b, err := json.Marshal(r)
	if err != nil {
		return nil, fmt.Errorf("error marshaling agent request: %w", err)
	}

	method := "POST"
	base := r.BaseURL
	if base == "" {
		base = defaultBaseURL
	}
	url := strings.TrimRight(base, "/") + "/v1/chat/completions"
	reader := bytes.NewReader(b)
	req, err := http.NewRequest(method, url, reader)
	if err != nil {
		return nil, fmt.Errorf("error building agent request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("error executing agent request: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("error reading agent response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("agent returned %d: %s", resp.StatusCode, string(body))
	}

	var response AgentResponse
	err = json.Unmarshal(body, &response)
	if err != nil {
		return nil, fmt.Errorf("error unmarshaling agent response: %w", err)
	}

	return &response, nil
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

func (tc ToolCall) Execute() (string, error) {
	name := tc.Function.Name
	switch name {
	case FunctionReadFile:
		return tc.ReadFile()
	case FunctionWriteFile:
		return tc.WriteFile()
	case FunctionEditFile:
		return tc.EditFile()
	case FunctionBash:
		return tc.Bash()
	}
	return "", fmt.Errorf(`no available tools matching name "%s"`, name)
}

func (tc ToolCall) ReadFile() (string, error) {
	path, err := tc.stringArg("path")
	if err != nil {
		return "", err
	}
	fmt.Printf("  %s\n", withColor(fmt.Sprintf("read_file(%s)", path), green))
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
	fmt.Printf("  %s\n", withColor(fmt.Sprintf("write_file(%s, %d bytes)", path, len(content)), green))
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

	fmt.Printf("  %s\n", withColor(fmt.Sprintf("edit_file(%s)", path), green))

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

func (tc ToolCall) Bash() (string, error) {
	command, err := tc.stringArg("command")
	if err != nil {
		return "", err
	}
	fields := strings.Fields(command)
	if len(fields) == 0 {
		return "", fmt.Errorf("empty command")
	}
	if !bashAllowlist[fields[0]] {
		return "", fmt.Errorf("command %q is not in the allowlist", fields[0])
	}
	fmt.Printf("  %s\n", withColor(fmt.Sprintf("bash(%s)", command), green))

	out, runErr := exec.Command(fields[0], fields[1:]...).CombinedOutput()
	result := string(out)
	if len(result) > maxToolOutput {
		result = result[:maxToolOutput] + "\n...[output truncated]"
	}
	// A non-zero exit is an observation, not a harness failure: hand the output
	// and exit error back to the model so it can react.
	if runErr != nil {
		return result + "\n[exit error: " + runErr.Error() + "]", nil
	}
	return result, nil
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

func (tc ToolCall) String() string {
	return fmt.Sprintf("wants %s %s %s %v", tc.ID, tc.Type, tc.Function.Name, tc.Function.Arguments)
}

// Print writes the message to stdout
func (m Message) Print() {
	var formatted string

	output := m.Content

	switch m.Role {
	case RoleUser:
		formatted = withColor(output, black)
	default:
		formatted = withColor(output, blue)
	}

	fmt.Printf("%s\n", formatted)
}

// CortexArgs specifies incoming cli arguments
type CortexArgs []string

// Request constructs a Request struct instance parsed from CortexArgs.
func (a CortexArgs) Request() *AgentRequest {
	systemMessage := Message{
		Role:    RoleSystem,
		Content: SystemPrompt,
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
	// MaxContext is the active model's context window, resolved from config.
	// Zero means "unknown" → windowSize falls back to defaultMaxContext.
	MaxContext int
	// Config is the loaded .cortex/config.json (may be nil), kept so /model can
	// re-resolve the endpoint when switching models mid-session.
	Config *Config
}

// SetModel switches the active model and re-resolves its endpoint (base URL +
// context window) from config. It errors if the model isn't in any configured
// endpoint, since without that we don't know where to send or how big the
// window is. Conversation history is preserved across the switch.
func (cs *CortexSession) SetModel(model string) error {
	ep := cs.Config.EndpointFor(model)
	if ep == nil {
		return fmt.Errorf("unknown model %q (not in any configured endpoint)", model)
	}
	cs.Request.Model = model
	if ep.BaseURL != "" {
		cs.Request.BaseURL = ep.BaseURL
	}
	cs.MaxContext = ep.MaxContextOverride
	return nil
}

// AvailableModels lists every model across all configured endpoints.
func (cs *CortexSession) AvailableModels() []string {
	var out []string
	if cs.Config != nil {
		for _, ep := range cs.Config.Endpoints {
			out = append(out, ep.Models...)
		}
	}
	return out
}

// windowSize is the context window to gauge against: the config-resolved value,
// or the default if config didn't provide one.
func (cs CortexSession) windowSize() int {
	if cs.MaxContext > 0 {
		return cs.MaxContext
	}
	return defaultMaxContext
}

// Config is the subset of .cortex/config.json the loop consults. The file is
// built during cortex setup; the loop is a reader, never a writer. Unknown
// fields are ignored on unmarshal.
type Config struct {
	Endpoints    []Endpoint `json:"endpoints"`
	DefaultModel string     `json:"default_model"`
}

type Endpoint struct {
	Name               string   `json:"name"`
	BaseURL            string   `json:"base_url"`
	MaxContextOverride int      `json:"max_context_override"`
	Models             []string `json:"models"`
}

// EndpointFor returns the endpoint that serves the given model, or nil.
func (c *Config) EndpointFor(model string) *Endpoint {
	if c == nil {
		return nil
	}
	for i := range c.Endpoints {
		for _, m := range c.Endpoints[i].Models {
			if m == model {
				return &c.Endpoints[i]
			}
		}
	}
	return nil
}

// findConfigPath walks up from the cwd looking for .cortex/config.json, like
// git finds .git. Returns "" if none is found.
func findConfigPath() string {
	dir, err := os.Getwd()
	if err != nil {
		return ""
	}
	for {
		p := filepath.Join(dir, ".cortex", "config.json")
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
	session := &CortexSession{Args: &args, Request: req, Config: cfg}

	// Resolve the active model's endpoint from config: its base URL and context
	// window. Missing config or model => keep the built-in defaults.
	if ep := cfg.EndpointFor(req.Model); ep != nil {
		if ep.BaseURL != "" {
			req.BaseURL = ep.BaseURL
		}
		if ep.MaxContextOverride > 0 {
			session.MaxContext = ep.MaxContextOverride
		}
	}
	return session
}

func (cs CortexSession) PrintArgs() {
	fmt.Printf("Cortex Model: %s Temp:%f\n", cs.Request.Model, cs.Request.Temperature)
}

func (cs CortexSession) Append(message Message) {
	cs.Request.Messages = append(cs.Request.Messages, message)
}

// humanK renders a token count compactly: 8200 -> "8.2k", 999 -> "999".
func humanK(n int) string {
	if n < 1000 {
		return fmt.Sprintf("%d", n)
	}
	return strings.TrimSuffix(fmt.Sprintf("%.1f", float64(n)/1000), ".0") + "k"
}

// ctxColor shifts the context gauge green -> yellow -> red as the window fills,
// so context pressure is ambient — you feel it before you hit the wall.
func ctxColor(used, max int) string {
	switch r := float64(used) / float64(max); {
	case r < 0.5:
		return green
	case r < 0.8:
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
func (cs CortexSession) send() (*AgentResponse, error) {
	s := NewSpinner()
	s.Start()
	res, err := cs.Request.Send()
	s.Stop()
	return res, err
}

// Resolve runs the agentic inner loop for one user turn: send, run any tools
// the model asked for, feed the results back, and re-send — repeating until the
// model answers with no more tool calls (or we hit the iteration cap). The
// caller appends the user message; Resolve owns everything from there.
func (cs *CortexSession) Resolve() error {
	for i := 0; i < maxToolIterations; i++ {
		res, err := cs.send()
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

		// (3) Run every requested tool and append one result per call. Each
		// id MUST get a matching tool message or the next send 400s.
		for _, tc := range msg.ToolCalls {
			content, err := tc.Execute()
			if err != nil {
				// Tool errors are observations, not crashes: feed them back so
				// the model can self-correct.
				content = "Error: " + err.Error()
			}
			cs.Append(Message{
				Role:       RoleTool,
				ToolCallID: tc.ID,
				Content:    content,
			})
		}
		// Loop: the next send picks up the grown history.
	}
	return fmt.Errorf("exceeded max tool iterations (%d)", maxToolIterations)
}

func main() {
	session := NewCortexSession()
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

		// /model [name] shows or switches the active model. With no name it
		// lists what's available; switching re-resolves the endpoint from config
		// and keeps the conversation history.
		if input == "/model" || strings.HasPrefix(input, "/model ") {
			name := strings.TrimSpace(strings.TrimPrefix(input, "/model"))
			if name == "" {
				fmt.Printf("current: %s\navailable: %s\n", session.Request.Model, strings.Join(session.AvailableModels(), ", "))
			} else if err := session.SetModel(name); err != nil {
				fmt.Printf("%v\n", err)
			} else {
				fmt.Printf("switched to %s\n", name)
			}
			continue
		}

		session.Append(Message{Role: RoleUser, Content: input})
		if err := session.Resolve(); err != nil {
			fmt.Printf("turn error: %v\n", err)
		}
	}

	fmt.Println("exiting")
}
