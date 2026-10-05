// recover_hermes_test.go — issue #132's text-form tool-call recovery, at the
// seams the engine exposes: the runLoop dispatch (a reply whose only tool call
// is Hermes <function_calls> markup gets dispatched, not returned as prose)
// and reportWithheldToolCalls (the forced-wrap-up receipt: a tools-withheld
// finalize reply carrying markup reports the intended action instead of
// dropping it). The parser itself (ParseFunctionCallsTags) and the display
// strip (StripToolMarkup) are covered in internal/tools
// (function_calls_test.go).
package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/dereksantos/cortex/internal/tools"
)

// TestRunLoopDispatchesRecoveredHermesToolCalls drives runLoop with a sender
// that returns a reply whose content is Hermes-style markup and NO structured
// tool_calls. The recovery (recoverTextToolCalls → ToolCallsFromContent) must
// convert it to real tool calls and dispatch them — the dispatcher records
// the name and path it received; the turn then ends through the forced
// finalize (MaxIter 2), so the test proves the tool ROUND happened, not just
// that the loop ran.
func TestRunLoopDispatchesRecoveredHermesToolCalls(t *testing.T) {
	req := &AgentRequest{Model: "m", Messages: []Message{{Role: RoleSystem, Content: "s"}}}
	appendMsg := func(m Message) { req.Messages = append(req.Messages, m) }

	var dispatchedName, dispatchedPath string
	send := SenderFunc(func(_ context.Context, r *AgentRequest) (*AgentResponse, bool, error) {
		if r.Tools == nil { // the forced finalize
			return fakeResp("forced", nil, 1, 1), false, nil
		}
		// No structured tool_calls — the call lives in the text.
		const content = `Let me look. <function_calls>{"name":"read_file","arguments":{"path":"f"}}</function_calls>`
		return fakeResp(content, nil, 1, 1), false, nil
	})
	disp := DispatchFunc(func(_ context.Context, call ToolCall) string {
		dispatchedName = call.Function.Name
		var args map[string]string
		// Arguments is the JSON-encoded-string shape the wire uses: a
		// stringified JSON object, so decode the string first.
		var s string
		if err := json.Unmarshal([]byte(call.Function.Arguments), &s); err == nil && s != "" {
			_ = json.Unmarshal([]byte(s), &args)
		} else {
			_ = json.Unmarshal([]byte(call.Function.Arguments), &args)
		}
		dispatchedPath = args["path"]
		return "file content"
	})

	_, stats, err := runLoop(context.Background(), send, req,
		Toolset{Tools: []Tool{tools.ReadFile}, Dispatch: disp},
		Bounds{MaxTokens: 100, MaxIter: 2}, nil, appendMsg, nil)
	if err != nil {
		t.Fatalf("runLoop: %v", err)
	}
	if dispatchedName != "read_file" {
		t.Fatalf("dispatcher saw %q, want read_file — the recovered Hermes call must be dispatched", dispatchedName)
	}
	if dispatchedPath != "f" {
		t.Errorf("dispatcher saw path %q, want f", dispatchedPath)
	}
	if stats.Iterations != 2 || stats.StopReason != "max-iter" {
		t.Errorf("stop = %q iters = %d, want max-iter after two rounds (recovered tool round + cap)", stats.StopReason, stats.Iterations)
	}
}

// TestReportWithheldToolCalls covers the forced-wrap-up receipt: when the
// tools-withheld finalize reply carries tool-call markup, the receipt names
// the intended action; prose is kept (markup stripped), and prose-only
// replies pass through untouched.
func TestReportWithheldToolCalls(t *testing.T) {
	const markup = `Let me fix that.
<function_calls>{"name":"bash","arguments":{"command":"go test ./..."}}</function_calls>`

	t.Run("markup plus prose: receipt leads, prose kept", func(t *testing.T) {
		got := reportWithheldToolCalls(markup)
		if !strings.Contains(got, "Intended action not executed") {
			t.Errorf("receipt missing the intended-action line: %q", got)
		}
		if !strings.Contains(got, "bash") {
			t.Errorf("receipt should name the withheld tool: %q", got)
		}
		if !strings.Contains(got, "go test ./...") {
			t.Errorf("receipt should name the withheld command: %q", got)
		}
		if !strings.Contains(got, "Let me fix that.") {
			t.Errorf("the model's prose must be kept: %q", got)
		}
		if strings.Contains(got, "<function_calls>") {
			t.Errorf("raw markup must not leak into the receipt: %q", got)
		}
	})

	t.Run("markup only: receipt without prose", func(t *testing.T) {
		const only = `<function_calls>{"name":"bash","arguments":{"command":"ls"}}</function_calls>`
		got := reportWithheldToolCalls(only)
		if !strings.Contains(got, "Intended action not executed") {
			t.Errorf("receipt missing: %q", got)
		}
		if strings.Contains(got, "<function_calls>") {
			t.Errorf("raw markup must not leak: %q", got)
		}
	})

	t.Run("prose only: untouched", func(t *testing.T) {
		const prose = "All done, tests pass."
		if got := reportWithheldToolCalls(prose); got != prose {
			t.Errorf("prose-only reply must pass through untouched, got %q", got)
		}
	})
}
