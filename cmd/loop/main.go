package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
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
[ ] Study tool
[ ] Journal tool
[ ] Spawn
[ ] Integrate Emergent DAG
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

const red = "\033[31m"
const cyan = "\033[36m"
const green = "\033[32m"
const black = "\033[30m"
const blue = "\033[34m"
const reset = "\033[0m" // Reset to default color

func withColor(v string, c string) string {
	return fmt.Sprintf("%s%s%s", c, v, reset)
}

// Define the cycling characters
var chars = []string{
	"█",
	"▓",
	"▒",
	"░",
	"░",
	"▒",
	"▓",
	"█",
	"─",
	"━",
	"│",
	"┃",
	"┄",
	"┅",
	"┆",
	"┇",
	"┈",
	"┉",
	"┊",
	"┋",
	"┌",
	"┍",
	"┎",
	"┏",
	"└",
	"┕",
	"┖",
	"┗",
	"┘",
	"┙",
	"┚",
	"┛",
	"┞",
	"┟",
	"┠",
	"┡",
	"┢",
	"┣",
	"┤",
	"┥",
	"┦",
	"┧",
	"┨",
	"┩",
	"┪",
	"┫",
	"┬",
	"┭",
	"┮",
	"┯",
	"┰",
	"┱",
	"┲",
	"┳",
	"┴",
	"┵",
	"┶",
	"┷",
	"┸",
	"┹",
	"┺",
	"┻",
	"┼",
	"┽",
	"┾",
	"┿",
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
	go func() {
		defer close(s.doneChan)
		for i := 0; ; i++ {
			select {
			case <-s.stopChan:
				return
			default:
				fmt.Printf("\r%s", withColor(chars[i%len(chars)], cyan))
				time.Sleep(80 * time.Millisecond)
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
	url := "http://chatterbox:4000/v1/chat/completions"
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
}

func NewCortexSession() *CortexSession {
	args := CortexArgs(os.Args)
	req := args.Request()
	return &CortexSession{
		Args:    &args,
		Request: req,
	}
}

func (cs CortexSession) PrintArgs() {
	fmt.Printf("Cortex Model: %s Temp:%f\n", cs.Request.Model, cs.Request.Temperature)
}

func (cs CortexSession) Append(message Message) {
	cs.Request.Messages = append(cs.Request.Messages, message)
}

// Resolve runs the agentic inner loop for one user turn: it appends the
// assistant message, runs any tools it asked for, feeds the results back, and
// re-sends — repeating until the model answers with no more tool calls (or we
// hit the iteration cap). `res` is the model's first response to the new user
// message; Resolve owns everything from there to the final answer.
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
func (cs CortexSession) Resolve() error {
	for i := 0; i < maxToolIterations; i++ {
		res, err := cs.send()
		if err != nil {
			return fmt.Errorf("model response error: %w", err)
		}
		if res == nil || len(res.Choices) == 0 {
			return fmt.Errorf("no choices in agent response")
		}
		msg := res.Choices[0].Message

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
	prompt := withColor("&> ", cyan)

	for {
		fmt.Print(prompt)
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

		session.Append(Message{Role: RoleUser, Content: input})
		if err := session.Resolve(); err != nil {
			fmt.Printf("turn error: %v\n", err)
		}
	}

	fmt.Println("exiting")
}
