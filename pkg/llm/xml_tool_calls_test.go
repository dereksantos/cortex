package llm

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestParseXMLToolCalls_SingleCall(t *testing.T) {
	// The exact shape Qwen3-Coder emitted in the live trace that
	// motivated this parser. Surrounding prose + trailing </tool_call>
	// wrapper must not block extraction.
	content := `I'll list the files in cmd/cortex/ and describe what each one does.

<function=list_dir>
<parameter=path>
cmd/cortex
</parameter>
</function>
</tool_call>`

	calls := ParseXMLToolCalls(content)
	if len(calls) != 1 {
		t.Fatalf("expected 1 call, got %d", len(calls))
	}
	if calls[0].Function.Name != "list_dir" {
		t.Errorf("name: got %q want list_dir", calls[0].Function.Name)
	}
	if calls[0].Type != "function" {
		t.Errorf("type: got %q want function", calls[0].Type)
	}
	var args map[string]string
	if err := json.Unmarshal([]byte(calls[0].Function.Arguments), &args); err != nil {
		t.Fatalf("args unmarshal: %v", err)
	}
	if args["path"] != "cmd/cortex" {
		t.Errorf("path: got %q want cmd/cortex", args["path"])
	}
}

func TestParseXMLToolCalls_MultipleCalls(t *testing.T) {
	// Some models emit multiple function blocks in one response;
	// each block is its own ToolCall. Each gets a distinct id.
	content := `<function=list_dir><parameter=path>cmd</parameter></function>
<function=read_file><parameter=path>main.go</parameter></function>`

	calls := ParseXMLToolCalls(content)
	if len(calls) != 2 {
		t.Fatalf("expected 2 calls, got %d", len(calls))
	}
	if calls[0].Function.Name != "list_dir" || calls[1].Function.Name != "read_file" {
		t.Errorf("names: got %q %q", calls[0].Function.Name, calls[1].Function.Name)
	}
	if calls[0].ID == calls[1].ID {
		t.Errorf("IDs collided: %q", calls[0].ID)
	}
}

func TestParseXMLToolCalls_MultipleParameters(t *testing.T) {
	content := `<function=run_shell>
<parameter=cmd>ls -la</parameter>
<parameter=cwd>/tmp</parameter>
</function>`
	calls := ParseXMLToolCalls(content)
	if len(calls) != 1 {
		t.Fatalf("expected 1 call, got %d", len(calls))
	}
	var args map[string]string
	_ = json.Unmarshal([]byte(calls[0].Function.Arguments), &args)
	if args["cmd"] != "ls -la" || args["cwd"] != "/tmp" {
		t.Errorf("args: got %+v", args)
	}
}

func TestParseXMLToolCalls_NoMatch_PlainAnswer(t *testing.T) {
	// Pure-prose response → no calls extracted. Caller treats this
	// as "model done" and ends the loop.
	content := "Here are the files: foo.go, bar.go, baz.go."
	calls := ParseXMLToolCalls(content)
	if len(calls) != 0 {
		t.Errorf("expected 0 calls for plain prose, got %d", len(calls))
	}
}

func TestParseXMLToolCalls_EmptyContent(t *testing.T) {
	if calls := ParseXMLToolCalls(""); calls != nil {
		t.Errorf("empty content should return nil, got %v", calls)
	}
}

func TestParseXMLToolCalls_NoArgs(t *testing.T) {
	// A no-arg call (e.g. act.list_dir with no path → workdir root)
	// should still produce a ToolCall with empty args.
	content := `<function=ping></function>`
	calls := ParseXMLToolCalls(content)
	if len(calls) != 1 {
		t.Fatalf("expected 1 call, got %d", len(calls))
	}
	if calls[0].Function.Arguments != "{}" {
		t.Errorf("expected empty-args {}, got %q", calls[0].Function.Arguments)
	}
}

func TestParseXMLToolCalls_MultiLineValue(t *testing.T) {
	// Multi-line parameter values (e.g. a code patch) must be
	// captured intact; (?s) on the regex makes . match newlines.
	content := `<function=write_file>
<parameter=path>foo.go</parameter>
<parameter=content>package main

func main() {
	println("hi")
}</parameter>
</function>`
	calls := ParseXMLToolCalls(content)
	if len(calls) != 1 {
		t.Fatalf("expected 1 call, got %d", len(calls))
	}
	var args map[string]string
	_ = json.Unmarshal([]byte(calls[0].Function.Arguments), &args)
	if !strings.Contains(args["content"], `println("hi")`) {
		t.Errorf("multi-line value lost; got %q", args["content"])
	}
}

func TestParseXMLToolCalls_IgnoresMarkdownFence(t *testing.T) {
	// Some models wrap their tool calls in markdown fences. The
	// regex matches the function block regardless of surrounding
	// fence characters.
	content := "```\n<function=list_dir><parameter=path>.</parameter></function>\n```"
	calls := ParseXMLToolCalls(content)
	if len(calls) != 1 {
		t.Errorf("expected 1 call inside fences, got %d", len(calls))
	}
}

func TestParseXMLToolCalls_LiteralStringNotMistaken(t *testing.T) {
	// A code block describing the format shouldn't itself be parsed
	// as a call. The regex requires both opening AND closing tags,
	// so partial text won't match.
	content := "To use the read_file function, emit <function=read_file> with a path parameter."
	calls := ParseXMLToolCalls(content)
	if len(calls) != 0 {
		t.Errorf("incomplete pattern should not match; got %v", calls)
	}
}
