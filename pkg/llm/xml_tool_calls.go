// Package llm — permissive XML-style tool-call parser.
//
// OpenAI's structured `tool_calls` array is the standard but plenty of
// open coders emit a hybrid XML-ish form in the assistant content
// instead — Qwen3-Coder, DeepSeek-Coder, CodeLlama in tool-use mode,
// and the older Anthropic function-calling API. The harness loop
// receives `Content="<function=...>...</function>"` with empty
// `ToolCalls`, treats it as "model done", and the session ends with
// zero work performed.
//
// ParseXMLToolCalls recovers tool calls from that content shape and
// returns them as []ToolCall identical to what a well-formed OpenAI
// response would have produced. The caller decides what to do with
// them (typically: inject into the ChatResult and let the agent loop
// dispatch as normal).
//
// Format recognized:
//
//	<function=NAME>
//	<parameter=KEY>VALUE</parameter>
//	...
//	</function>
//
// Tolerant of surrounding wrappers (`<tool_call>...</tool_call>`,
// markdown fences, prose between blocks). Strict on the function +
// parameter tag shape — looser matching risks false-positives on
// code blocks containing literal `<function=...>` strings.

package llm

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

// xmlFunctionBlockRe matches a single `<function=NAME>...</function>`
// block. Non-greedy on the body so multiple blocks in one response
// parse independently. Dotall (?s) so multi-line parameter values
// are captured.
var xmlFunctionBlockRe = regexp.MustCompile(`(?s)<function=([^>\s]+)>(.*?)</function>`)

// xmlParameterRe matches one `<parameter=KEY>VALUE</parameter>` line
// inside a function block. Non-greedy on the value.
var xmlParameterRe = regexp.MustCompile(`(?s)<parameter=([^>\s]+)>(.*?)</parameter>`)

// ParseXMLToolCalls scans content for XML-style function blocks and
// returns them as ToolCall values. Each parameter becomes an entry in
// the JSON-encoded Arguments string. Returns an empty slice when no
// blocks match — caller treats that as "this was a final answer, not
// a tool-call attempt."
//
// Parameter values are returned verbatim (whitespace-trimmed) as
// strings — the format doesn't carry type info, and the receivers
// (act.read_file path, act.run_shell cmd, etc.) all accept strings.
// Tools that need typed args (numbers, nested objects) won't work
// well via this path; pin those handlers to a model that emits
// structured tool_calls instead.
func ParseXMLToolCalls(content string) []ToolCall {
	matches := xmlFunctionBlockRe.FindAllStringSubmatch(content, -1)
	if len(matches) == 0 {
		return nil
	}
	calls := make([]ToolCall, 0, len(matches))
	for i, m := range matches {
		name := strings.TrimSpace(m[1])
		if name == "" {
			continue
		}
		body := m[2]
		args := map[string]string{}
		for _, p := range xmlParameterRe.FindAllStringSubmatch(body, -1) {
			key := strings.TrimSpace(p[1])
			if key == "" {
				continue
			}
			args[key] = strings.TrimSpace(p[2])
		}
		// Empty args map serializes to "{}", which is what an OpenAI
		// response with a no-arg tool call would have. Don't drop the
		// call just because the model omitted args.
		argsJSON, err := json.Marshal(args)
		if err != nil {
			// Marshalling a map[string]string can't fail in practice,
			// but if it did we'd rather drop this one call than blow
			// up the whole turn.
			continue
		}
		calls = append(calls, ToolCall{
			ID:   fmt.Sprintf("xml-call-%d", i),
			Type: "function",
			Function: ToolCallFunction{
				Name:      name,
				Arguments: string(argsJSON),
			},
		})
	}
	return calls
}
