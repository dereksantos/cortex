package main

import (
	"testing"
	"time"

	"github.com/dereksantos/cortex/internal/style"
	"github.com/dereksantos/cortex/internal/tools"
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
		name string
		s    turnSummary
		want string
	}{
		{"full", turnSummary{Elapsed: 72 * time.Second, Thought: "thought 38s", Tools: 7, FilesChanged: 1, Cost: 0.004},
			"          1m12s · thought 38s · 7 tools · 1 file changed · $0.0040"},
		{"answer only", turnSummary{Elapsed: 800 * time.Millisecond}, "          0.8s"},
		{"long turn, local model", turnSummary{Elapsed: 185 * time.Second, Tools: 31, FilesChanged: 4},
			"          3m05s · 31 tools · 4 files changed"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			defer style.ForceColor(false)()
			if got := renderFooter(tt.s); got != tt.want {
				t.Errorf("footer changed.\n got: %q\nwant: %q", got, tt.want)
			}
		})
	}
}

func TestRiskyQuestionLayout(t *testing.T) {
	defer style.ForceColor(false)()
	prev := tools.Now
	tools.Now = func() time.Time { return time.Date(2026, 10, 9, 14, 5, 2, 0, time.UTC) }
	defer func() { tools.Now = prev }()
	got := riskyQuestion("git reset --hard HEAD~1", "discards uncommitted changes and the last commit", "git reset*")
	want := "\n14:05:02  bash     git reset --hard HEAD~1\n" +
		"          risky: discards uncommitted changes and the last commit\n" +
		`run it?  y once · a this command · p always "git reset*" · n `
	if got != want {
		t.Errorf("riskyQuestion =\n%q\nwant\n%q", got, want)
	}
}
