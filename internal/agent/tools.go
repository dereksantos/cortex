// Package agent holds the shared agent data vocabulary — the tool-call types the
// engine and the tools package both speak. It is a leaf: it imports only the
// standard library (and pkg/llm where the wire protocol lives), never the tool
// implementations or the session, so the dependency arrow points
// internal/tools → internal/agent (not the reverse). See
// docs/engine-unification.md.
package agent

import (
	"encoding/json"
	"fmt"
	"strings"
)

// Bounds are the engine's independent ceilings for one agent run; whichever
// trips first forces finalize. THREE mandatory caps — no request is EVER
// unbounded:
//   - MaxTokens: a finite per-request completion cap, the primary runaway
//     backstop — the one thing that holds when cancellation fails (see the
//     2026-06-28 north runaway in docs/engine-unification.md).
//   - MaxIter: caps the rounds.
//   - ReadBudgetBytes: caps accumulated tool output (0 = unbounded, the coder).
type Bounds struct {
	MaxTokens       int
	MaxIter         int
	ReadBudgetBytes int
}

// Tool is the OpenAI-format tool declaration passed in the tools array.
type Tool struct {
	Type     string       `json:"type"`
	Function ToolFunction `json:"function"`
}

// ToolFunction carries the name, description, and JSON-Schema parameters.
type ToolFunction struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Parameters  map[string]any `json:"parameters"`
}

// FunctionCall is the function part of a tool call.
type FunctionCall struct {
	Name string `json:"name"`
	// Arguments is a JSON-encoded *string* on the wire (e.g. `{"path":"go.mod"}`),
	// NOT a JSON object. Parse it with StringArg.
	Arguments string `json:"arguments"`
}

// ToolCall is one tool invocation from the model.
type ToolCall struct {
	ID       string       `json:"id"`
	Type     string       `json:"type"`
	Function FunctionCall `json:"function"`
}

// StringArg parses Arguments (a JSON string) and pulls out one string field.
func (tc ToolCall) StringArg(name string) (string, error) {
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

// IntArg pulls an integer field from Arguments. JSON numbers decode as float64.
// Returns (0, false) when missing or not a number.
func (tc ToolCall) IntArg(name string) (int, bool) {
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

// ActivityLabel is the concise "tool(arg)" shown on the spinning status row while
// a tool runs — enough to tell which call is in flight without the full argument
// dump.
func (tc ToolCall) ActivityLabel() string {
	name := tc.Function.Name
	if p, err := tc.StringArg("path"); err == nil && p != "" {
		return name + "(" + p + ")"
	}
	if c, err := tc.StringArg("command"); err == nil && c != "" {
		first := firstLine(c)
		if len(first) > 40 {
			first = first[:40] + "…"
		}
		return name + "(" + first + ")"
	}
	return name
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}
