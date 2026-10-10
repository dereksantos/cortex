package main

import (
	"strings"
	"testing"
	"time"

	"github.com/dereksantos/cortex/internal/style"
	"github.com/dereksantos/cortex/internal/tools"
)

func TestLastTurnView(t *testing.T) {
	defer style.ForceColor(false)()
	at := time.Date(2026, 10, 9, 14, 2, 11, 0, time.UTC)
	v := lastTurnView{calls: []tools.CallRecord{
		{At: at, Action: "read_file(a.go)", Result: "2 lines, 9 B", Output: []string{"package a", "\tfunc A() {}"}},
		{At: at, Action: "bash(seq 50)", Result: "50 lines", Output: []string{"1", "2"}, More: 48},
	}}
	if v.Title() != "last turn — 2 tool calls" {
		t.Errorf("title = %q", v.Title())
	}
	got := strings.Join(v.Lines(80), "\n")
	want := "14:02:11  read     a.go" + strings.Repeat(" ", 45) + "2 lines, 9 B\n" +
		"            package a\n" +
		"                func A() {}\n" +
		"\n" +
		"14:02:11  bash     seq 50" + strings.Repeat(" ", 47) + "50 lines\n" +
		"            1\n" +
		"            2\n" +
		"            … 48 more lines"
	if got != want {
		t.Errorf("lines =\n%s\nwant\n%s", got, want)
	}
	if empty := (lastTurnView{}).Lines(80); len(empty) != 1 || !strings.Contains(empty[0], "no tool calls yet") {
		t.Errorf("empty view = %q", empty)
	}
}
