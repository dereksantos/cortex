// Package llm provides LLM client implementations
package llm

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// ClaudeCLI wraps the claude CLI for agentic evals.
// It captures tool usage metrics by parsing stream-json output.
type ClaudeCLI struct {
	binary string   // path to claude binary
	args   []string // additional args (model, allowed tools, etc.)
}

// ToolStats tracks tool usage during a Claude session.
type ToolStats struct {
	TotalCalls   int            `json:"total_calls"`
	CallsByType  map[string]int `json:"calls_by_type"`
	InputTokens  int            `json:"input_tokens"`
	OutputTokens int            `json:"output_tokens"`
}

// AgenticResult holds the result of an agentic Claude session.
type AgenticResult struct {
	Response     string    `json:"response"`
	ToolStats    ToolStats `json:"tool_stats"`
	DurationMs   int64     `json:"duration_ms"`
	NumTurns     int       `json:"num_turns"`
	TotalCostUSD float64   `json:"total_cost_usd"`
	SessionID    string    `json:"session_id"`
	Success      bool      `json:"success"`
	Error        string    `json:"error,omitempty"`
}

// NewClaudeCLI creates a new ClaudeCLI provider.
func NewClaudeCLI(binary string, args ...string) (*ClaudeCLI, error) {
	if binary == "" {
		// Try to find claude in PATH
		path, err := exec.LookPath("claude")
		if err != nil {
			return nil, fmt.Errorf("claude binary not found in PATH")
		}
		binary = path
	}

	// Verify binary exists
	if _, err := os.Stat(binary); os.IsNotExist(err) {
		return nil, fmt.Errorf("claude binary not found: %s", binary)
	}

	return &ClaudeCLI{
		binary: binary,
		args:   args,
	}, nil
}

// Run executes a prompt with the Claude CLI and captures tool usage.
// The system parameter can inject Cortex context.
func (c *ClaudeCLI) Run(ctx context.Context, prompt, system string) (*AgenticResult, error) {
	result := &AgenticResult{
		ToolStats: ToolStats{
			CallsByType: make(map[string]int),
		},
	}

	// Create isolated workspace to prevent hooks from:
	// 1. Capturing eval events into the context database
	// 2. Injecting context that would skew baseline measurements
	// 3. Adding latency from hook execution
	workDir, cleanup, err := createIsolatedWorkspace()
	if err != nil {
		return nil, fmt.Errorf("create workspace: %w", err)
	}
	defer cleanup()

	// Build command args
	args := []string{
		"-p", prompt,
		"--output-format", "stream-json",
		"--verbose",
		"--no-session-persistence",
	}

	// Inject system prompt (Cortex context)
	if system != "" {
		args = append(args, "--append-system-prompt", system)
	}

	// Add any custom args (model, allowed tools, etc.)
	args = append(args, c.args...)

	cmd := exec.CommandContext(ctx, c.binary, args...)
	cmd.Dir = workDir

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("stdout pipe: %w", err)
	}

	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, fmt.Errorf("stderr pipe: %w", err)
	}

	startTime := time.Now()

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start claude: %w", err)
	}

	// Parse stream-json output
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 1024*1024), 10*1024*1024) // 10MB buffer for large responses

	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			continue
		}

		var event streamEvent
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			continue // Skip unparseable lines
		}

		c.processEvent(&event, result)
	}

	// Capture any stderr
	stderrScanner := bufio.NewScanner(stderr)
	var stderrLines []string
	for stderrScanner.Scan() {
		stderrLines = append(stderrLines, stderrScanner.Text())
	}

	if err := cmd.Wait(); err != nil {
		result.Success = false
		result.Error = fmt.Sprintf("%v: %s", err, strings.Join(stderrLines, "\n"))
	} else {
		result.Success = true
	}

	// Fallback duration if not captured from result event
	if result.DurationMs == 0 {
		result.DurationMs = time.Since(startTime).Milliseconds()
	}

	return result, nil
}

// streamEvent represents a single event from stream-json output.
type streamEvent struct {
	Type    string `json:"type"`
	Subtype string `json:"subtype,omitempty"`

	// For type=assistant events
	Message *assistantMessage `json:"message,omitempty"`

	// For type=result events
	DurationMs   int64   `json:"duration_ms,omitempty"`
	NumTurns     int     `json:"num_turns,omitempty"`
	TotalCostUSD float64 `json:"total_cost_usd,omitempty"`
	SessionID    string  `json:"session_id,omitempty"`
	Result       string  `json:"result,omitempty"`
	IsError      bool    `json:"is_error,omitempty"`

	// For usage tracking
	Usage *usageInfo `json:"usage,omitempty"`
}

type assistantMessage struct {
	Content []contentBlock `json:"content,omitempty"`
	Usage   *usageInfo     `json:"usage,omitempty"`
}

type contentBlock struct {
	Type  string `json:"type"`
	Name  string `json:"name,omitempty"` // tool name for tool_use blocks
	Text  string `json:"text,omitempty"`
	Input any    `json:"input,omitempty"`
}

type usageInfo struct {
	InputTokens  int `json:"input_tokens,omitempty"`
	OutputTokens int `json:"output_tokens,omitempty"`
}

// processEvent extracts metrics from a stream event.
func (c *ClaudeCLI) processEvent(event *streamEvent, result *AgenticResult) {
	switch event.Type {
	case "assistant":
		if event.Message != nil {
			// Track tool usage
			for _, block := range event.Message.Content {
				if block.Type == "tool_use" && block.Name != "" {
					result.ToolStats.TotalCalls++
					result.ToolStats.CallsByType[block.Name]++
				}
			}

			// Track token usage
			if event.Message.Usage != nil {
				result.ToolStats.InputTokens += event.Message.Usage.InputTokens
				result.ToolStats.OutputTokens += event.Message.Usage.OutputTokens
			}
		}

	case "result":
		result.Response = event.Result
		result.DurationMs = event.DurationMs
		result.NumTurns = event.NumTurns
		result.TotalCostUSD = event.TotalCostUSD
		result.SessionID = event.SessionID
		if event.IsError {
			result.Success = false
		}
	}
}

// RunBaseline runs the prompt without any injected context.
func (c *ClaudeCLI) RunBaseline(ctx context.Context, prompt string) (*AgenticResult, error) {
	return c.Run(ctx, prompt, "")
}

// RunWithContext runs the prompt with Cortex context injected.
func (c *ClaudeCLI) RunWithContext(ctx context.Context, prompt, cortexContext string) (*AgenticResult, error) {
	system := fmt.Sprintf(`You have access to the following context from previous work sessions:

%s

Use this context to inform your response. Prefer using the provided context over exploring the codebase when the information is already available.`, cortexContext)

	return c.Run(ctx, prompt, system)
}

// Name returns the provider identifier.
func (c *ClaudeCLI) Name() string {
	return "claude-cli"
}

// createIsolatedWorkspace creates a temp directory with settings that disable hooks.
// Returns the workspace path and a cleanup function.
func createIsolatedWorkspace() (string, func(), error) {
	// Create temp directory
	workDir, err := os.MkdirTemp("", "cortex-agentic-eval-*")
	if err != nil {
		return "", nil, err
	}

	cleanup := func() {
		os.RemoveAll(workDir)
	}

	// Create .claude directory with empty hooks settings
	claudeDir := filepath.Join(workDir, ".claude")
	if err := os.MkdirAll(claudeDir, 0755); err != nil {
		cleanup()
		return "", nil, err
	}

	// Write settings.json that disables all hooks
	settings := `{
  "hooks": {}
}`
	settingsPath := filepath.Join(claudeDir, "settings.json")
	if err := os.WriteFile(settingsPath, []byte(settings), 0644); err != nil {
		cleanup()
		return "", nil, err
	}

	return workDir, cleanup, nil
}
