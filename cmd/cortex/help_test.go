package main

import (
	"strings"
	"testing"
)

// TestHelpListsEveryCommand covers PART 2 of the 2026-07-19 de-glyph +
// discoverability work: /help must mention every slash command the REPL
// actually dispatches (main.go's read loop), so the list can't silently
// drift out of sync with what's really wired up.
func TestHelpListsEveryCommand(t *testing.T) {
	body := strings.Join(helpLines, "\n")
	for _, cmd := range []string{"/help", "/context", "/compact", "/clear", "/undo", "/sessions", "/model", "/quit", "/plan"} {
		if !strings.Contains(body, cmd) {
			t.Errorf("helpLines missing %q; got:\n%s", cmd, body)
		}
	}
}

// TestHelpLinesArePlainASCII covers the de-glyph decision: the help body
// carries no icon set, only plain text and (at print time) ANSI color.
func TestHelpLinesArePlainASCII(t *testing.T) {
	for _, line := range helpLines {
		for _, r := range line {
			if r > 127 {
				t.Errorf("helpLines has a non-ASCII rune %q in line %q", r, line)
			}
		}
	}
}

func TestHelpCommands(t *testing.T) {
	names, help := helpCommands()
	for _, want := range []string{"/help", "/undo", "/model", "/hook", "/quit"} {
		if _, ok := help[want]; !ok {
			t.Errorf("helpCommands missing %s (names %v)", want, names)
		}
	}
	tests := map[string]string{
		"/compact": "distill the session via study, freeing context",
		"/model":   "show the code/study model bindings, or switch the coding model",
		"/hook":    "turn the post-edit hook down or off for this session (never raises it)",
	}
	for name, want := range tests {
		if got := help[name]; got != want {
			t.Errorf("help[%s] = %q, want %q", name, got, want)
		}
	}
	if len(names) != len(helpLines) {
		t.Errorf("got %d names for %d help lines", len(names), len(helpLines))
	}
}
