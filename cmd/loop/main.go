package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

/*
TODO:
[x] Scanner animation v1
[x] System prompt
[ ] Tool calling v1 (read_file, write_file, bash allowlist)
[ ] Basic editing
[ ] Bash tool
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
const ModelCoder = "coder"

const FunctionReadFile = "read_file"

const defaultRole = RoleUser
const defaultModel = ModelCoder

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

type PromptAnimator struct {
	prompt   string
	chars    []string
	stopChan chan struct{}
}

func NewPromptAnimator(prompt string) *PromptAnimator {
	return &PromptAnimator{
		prompt: prompt,
		chars:  chars,
	}
}

func (pa *PromptAnimator) Start() {
	pa.stopChan = make(chan struct{})
	go func() {
		for {
			select {
			case <-pa.stopChan:
				return
			default:
				for _, char := range pa.chars {
					select {
					case <-pa.stopChan:
						return
					default:
						fmt.Printf("\r%s", char)
						time.Sleep(100 * time.Millisecond)
					}
				}
			}
		}
	}()
}

func (pa *PromptAnimator) Clear() {
	fmt.Print("\r")
}

func (pa *PromptAnimator) Prompt() {
	fmt.Print(pa.prompt)
}

func (pa *PromptAnimator) Stop() {
	close(pa.stopChan)
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

var readFile = Tool{
	Type: "function",
	Function: ToolFunction{
		Name:        "read_file",
		Description: "Read contents of a file",
		Parameters: map[string]any{
			"path": "string",
		},
	},
}

var tools = []Tool{readFile}

// Sends the request to the models API endpoint
func (r *AgentRequest) Send() (*AgentResponse, error) {
	b, err := json.Marshal(r)
	if err != nil {
		return nil, fmt.Errorf("error sending agent request %w", err)
	}

	method := "POST"
	url := "http://chatterbox:4000/v1/chat/completions"
	reader := bytes.NewReader(b)
	req, err := http.NewRequest(method, url, reader)
	if err != nil {
		return nil, fmt.Errorf("error building agent request %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("error executing agent request %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("error response from agent %w", err)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("error reading agent response %w", err)
	}

	var response AgentResponse
	err = json.Unmarshal(body, &response)
	if err != nil {
		return nil, fmt.Errorf("error unmarshaling agent response %w", err)
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

	ToolCalls []ToolCall `json:"tool_calls,omitempty"`
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
	}
	return "", fmt.Errorf(`no available tools matching name "%s"`, name)
}

func (tc ToolCall) ReadFile() (string, error) {
	path, ok := tc.Function.Arguments["path"].(string)
	if ok {
		fmt.Printf("ReadFile(%s)\n", path)
		data, err := os.ReadFile(path)
		if err != nil {
			return "", fmt.Errorf("error reading file %s tool_call %s", path, tc.String())
		}
		return string(data), nil
	}
	return "", fmt.Errorf(`unable to extract "path" arg from tool call %s`, tc.String())
}

type FunctionCall struct {
	Name      string         `json:"name"`
	Arguments map[string]any `json:"arguments"`
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

func main() {
	session := NewCortexSession()

	scanner := bufio.NewScanner(os.Stdin)
	animator := NewPromptAnimator(withColor("&>", cyan))

	for {
		animator.Prompt()
		if !scanner.Scan() {
			break
		}
		animator.Start()

		prompt := strings.TrimSpace(scanner.Text())
		session.Append(Message{
			Role:    RoleUser,
			Content: prompt,
		})

		res, err := session.Request.Send()
		if err != nil {
			fmt.Errorf("agent responded with error %w", err)
		}

		if res == nil {
			fmt.Errorf("nil response from agent")
			continue
		}
		animator.Clear()

		// queue up tool calls
		// print messages
		// execute tool calls
		// loop
		for i := range res.Choices {
			choice := res.Choices[i]
			message := choice.Message
			message.Print()
			session.Append(message)

		}
		animator.Stop()
	}

	fmt.Println("exiting")
}
