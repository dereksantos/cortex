package main

import (
	"testing"
	"time"

	"github.com/dereksantos/cortex/internal/style"
)

func TestSummarizeTurn(t *testing.T) {
	call := func(name, args string) ToolCall {
		tc := ToolCall{}
		tc.Function.Name = name
		tc.Function.Arguments = args
		return tc
	}
	msgs := []Message{
		{Role: "assistant", ToolCalls: []ToolCall{
			call(FunctionReadFile, `{"path":"a.go"}`),
			call(FunctionEditFile, `{"path":"a.go"}`),
		}},
		{Role: RoleTool, Content: "ok"},
		{Role: "assistant", ToolCalls: []ToolCall{
			call(FunctionEditFile, `{"path":"a.go"}`), // same file again: counted once
			call(FunctionWriteFile, `{"path":"b.go"}`),
			call(FunctionBash, `{"command":"go test ./..."}`),
		}},
		{Role: "assistant", Content: "done"},
	}
	got := summarizeTurn(msgs, 20*time.Second, 0.004)
	want := turnSummary{Elapsed: 20 * time.Second, Tools: 5, FilesChanged: 2, Cost: 0.004}
	if got != want {
		t.Errorf("summarizeTurn = %+v, want %+v", got, want)
	}
}

func TestRenderFooterGolden(t *testing.T) {
	tests := []struct {
		name  string
		s     turnSummary
		gauge string
		want  string
	}{
		{"full", turnSummary{Elapsed: 20 * time.Second, Tools: 7, FilesChanged: 1, Cost: 0.004}, "24k|131k",
			"          20s · 7 tools · 1 file changed · 24k|131k · $0.0040"},
		{"answer only", turnSummary{Elapsed: 800 * time.Millisecond}, "3k|131k",
			"          0.8s · 3k|131k"},
		{"long turn, local model", turnSummary{Elapsed: 185 * time.Second, Tools: 31, FilesChanged: 4}, "",
			"          3m05s · 31 tools · 4 files changed"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			defer style.ForceColor(false)()
			if got := renderFooter(tt.s, tt.gauge); got != tt.want {
				t.Errorf("footer changed.\n got: %q\nwant: %q", got, tt.want)
			}
		})
	}
}
