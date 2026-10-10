package tools

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/dereksantos/cortex/internal/style"
)

func TestTurnLogKeepsEveryCall(t *testing.T) {
	resetNesting(t)
	defer style.ForceColor(false)()
	BeginTurn(true)
	captureStdout(t, func() {
		for _, a := range []string{"read_file(a.go)", "grep(x, .)"} { // these two fold on screen
			printToolAction(loud{}, a)
			finishCall(time.Millisecond, "hit 1\nhit 2", nil)
		}
		printToolAction(loud{}, "read_file(gone.go)")
		finishCall(time.Millisecond, "", errors.New("no such file"))
		printToolAction(loud{}, "bash(seq 100)")
		finishCall(time.Millisecond, strings.Repeat("n\n", 100), nil)
		FlushFold()
	})
	calls := EndTurn()
	if len(calls) != 4 {
		t.Fatalf("want 4 records (folded ones included), got %d", len(calls))
	}
	if calls[0].Action != "read_file(a.go)" || len(calls[0].Output) != 2 {
		t.Errorf("first record = %+v", calls[0])
	}
	if !calls[2].Failed || calls[2].Output[0] != "error: no such file" {
		t.Errorf("failed call should say why: %+v", calls[2])
	}
	if len(calls[3].Output) != recordOutputLines || calls[3].More != 100-recordOutputLines {
		t.Errorf("output head not bounded: %d lines, %d more", len(calls[3].Output), calls[3].More)
	}

	BeginTurn(true)
	if n := len(EndTurn()); n != 0 {
		t.Errorf("BeginTurn should clear, %d left", n)
	}
	printToolAction(loud{}, "read_file(after.go)")
	captureStdout(t, func() { finishCall(time.Millisecond, "x", nil) })
	BeginTurn(true)
	if n := len(EndTurn()); n != 0 {
		t.Errorf("calls outside a turn (cortex study, study-eval) must not be recorded, got %d", n)
	}
}

func TestFormatCallLineMatchesTheScrollbackLine(t *testing.T) {
	defer style.ForceColor(false)()
	at := time.Date(2026, 10, 9, 14, 2, 19, 0, time.UTC)
	got := FormatCallLine(CallRecord{At: at, Depth: 1, Action: "edit_file(a.go)", Result: "+2 -1"}, 60)
	want := "14:02:19    edit     a.go" + strings.Repeat(" ", 30) + "+2 -1"
	if style.Width(got) != 60 {
		t.Errorf("the line should end at the given width 60, is %d", style.Width(got))
	}
	if got != want {
		t.Errorf("FormatCallLine =\n%q\nwant\n%q", got, want)
	}
}
