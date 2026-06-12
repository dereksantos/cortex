// Package llm — fenced-JSON tool-call recovery.
//
// Qwen3.6 (and several other open coders behind llama.cpp/LiteLLM)
// sometimes emit a tool call as a fenced JSON object in the assistant
// content instead of the structured `tool_calls` array:
//
//	```json
//	{ "name": "read_file", "arguments": { "path": "src/storage.rs" } }
//	```
//
// The harness loop sees empty ToolCalls, treats the text as a final
// answer, and the session ends with zero work performed (measured:
// 17/44 codebase-eval fixtures did zero reads on the 2026-06 fleet —
// docs/eval-journal.md). ParseJSONToolCalls recovers this shape;
// RecoverToolCalls is the one-stop entry that tries every known
// text dialect.
//
// Recognition is deliberately strict — the object must have a "name"
// string and at most a "name" + "arguments" pair, nothing else — so a
// model quoting some unrelated JSON (configs, API examples with extra
// fields) doesn't get misread as a call. Looser matching risks
// executing illustrations.

package llm

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

// jsonFenceRe matches one fenced code block, optionally tagged
// (```json, ```JSON, bare ```). Non-greedy body so multiple fences in
// one response parse independently.
var jsonFenceRe = regexp.MustCompile("(?s)```[a-zA-Z]*[ \\t]*\\n?(.*?)```")

// ParseJSONToolCalls scans content for fenced (or whole-content bare)
// JSON tool-call objects and returns them as ToolCall values identical
// to what a well-formed OpenAI response would have produced. Returns
// nil when nothing matches.
func ParseJSONToolCalls(content string) []ToolCall {
	var candidates []string
	for _, m := range jsonFenceRe.FindAllStringSubmatch(content, -1) {
		candidates = append(candidates, strings.TrimSpace(m[1]))
	}
	if len(candidates) == 0 {
		// No fences: accept the whole content when it IS a JSON object.
		if s := strings.TrimSpace(content); strings.HasPrefix(s, "{") && strings.HasSuffix(s, "}") {
			candidates = append(candidates, s)
		}
	}

	var calls []ToolCall
	for _, cand := range candidates {
		if tc, ok := parseOneJSONToolCall(cand, len(calls)); ok {
			calls = append(calls, tc)
		}
	}
	return calls
}

// parseOneJSONToolCall validates one candidate snippet against the
// strict {name, arguments?} shape.
func parseOneJSONToolCall(s string, idx int) (ToolCall, bool) {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal([]byte(s), &obj); err != nil {
		return ToolCall{}, false
	}
	var name string
	if raw, ok := obj["name"]; !ok || json.Unmarshal(raw, &name) != nil || strings.TrimSpace(name) == "" {
		return ToolCall{}, false
	}
	// Exactly {name} or {name, arguments} — any extra key means this is
	// some other JSON object that merely contains a "name" field.
	if len(obj) > 2 || (len(obj) == 2 && obj["arguments"] == nil) {
		return ToolCall{}, false
	}

	args := "{}"
	if raw, ok := obj["arguments"]; ok {
		// Arguments arrive either as an object (the common emission) or
		// as a JSON-encoded string (the OpenAI wire shape echoed back).
		var asString string
		switch {
		case json.Unmarshal(raw, &asString) == nil:
			if strings.TrimSpace(asString) != "" {
				if !json.Valid([]byte(asString)) {
					return ToolCall{}, false
				}
				args = asString
			}
		case json.Valid(raw):
			args = string(raw)
		default:
			return ToolCall{}, false
		}
	}

	return ToolCall{
		ID:   fmt.Sprintf("json-call-%d", idx),
		Type: "function",
		Function: ToolCallFunction{
			Name:      strings.TrimSpace(name),
			Arguments: args,
		},
	}, true
}

// RecoverToolCalls tries every known text-shape tool-call dialect, in
// order of specificity: the XML function-block form, then fenced JSON.
// Call it when a response has empty ToolCalls but non-empty content —
// the one place models hide work the loop would otherwise drop.
func RecoverToolCalls(content string) []ToolCall {
	if calls := ParseXMLToolCalls(content); len(calls) > 0 {
		return calls
	}
	return ParseJSONToolCalls(content)
}
