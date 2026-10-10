package tools

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/dereksantos/cortex/internal/style"
)

func TestFoldRuns(t *testing.T) {
	call := func(action string, err error) {
		printToolAction(loud{}, action)
		finishCall(5*time.Millisecond, "a\nb\n", err)
	}
	tests := []struct {
		name string
		run  func()
		want []string // stripped lines, gutter removed
	}{
		{"a run of one prints its normal line", func() {
			call("read_file(a.go)", nil)
			FlushFold()
		}, []string{"read     a.go  2 lines, 4 B"}},
		{"counts past one", func() {
			call("read_file(a.go)", nil)
			call("grep(x, .)", nil)
			call("read_file(b.go)", nil)
			call("read_file(c.go:1-9)", nil)
			call("grep(y, .)", nil)
			FlushFold()
		}, []string{"read 3 files · grep 2 searches"}},
		{"a failure breaks the run and stays visible", func() {
			call("read_file(a.go)", nil)
			call("grep(x, .)", nil)
			call("read_file(gone.go)", errors.New("no such file"))
			call("read_file(b.go)", nil)
			FlushFold()
		}, []string{"read a.go · grep x", "read     gone.go  error: no such file", "read     b.go  2 lines, 4 B"}},
		{"a call that doesn't fold flushes the run above it", func() {
			call("outline(pkg)", nil)
			call("read_file(a.go)", nil)
			call("bash(ls)", nil)
		}, []string{"outline pkg · read a.go", "bash     ls  2 lines, 4 B"}},
		{"nested calls never fold", func() {
			printToolAction(loud{}, "study(pkg, goal)")
			flushAction()
			pushNest("study")
			beginNestedCall()
			call("read_file(a.go)", nil)
			beginNestedCall()
			call("read_file(b.go)", nil)
			popNest()
			FlushFold()
		}, []string{"study    pkg, goal", "  read     a.go  2 lines, 4 B", "  read     b.go  2 lines, 4 B"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resetNesting(t)
			defer style.ForceColor(false)()
			prev := style.TermWidth
			style.TermWidth = func() int { return 0 }
			defer func() { style.TermWidth = prev }()
			out := captureStdout(t, tt.run)
			var got []string
			for _, l := range strings.Split(strings.TrimRight(out, "\n"), "\n") {
				got = append(got, l[len(gutterPad):])
			}
			if strings.Join(got, "\n") != strings.Join(tt.want, "\n") {
				t.Errorf("got:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(tt.want, "\n"))
			}
		})
	}
}

func TestFoldOffUnderPlainRender(t *testing.T) {
	resetNesting(t)
	defer style.ForceColor(false)()
	prev := richRenderDisabled
	richRenderDisabled = true
	defer func() { richRenderDisabled = prev }()
	out := captureStdout(t, func() {
		for _, a := range []string{"read_file(a.go)", "read_file(b.go)"} {
			printToolAction(loud{}, a)
			finishCall(time.Millisecond, "x", nil)
		}
		FlushFold()
	})
	if n := strings.Count(out, "\n"); n != 2 {
		t.Errorf("CORTEX_LOOP_RENDER=0 should print each call, got %d lines:\n%s", n, out)
	}
}
