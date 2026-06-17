package main

import (
	"strings"
	"testing"
)

// chunkMessage feeds Discord's 2000-char limit, so every chunk must stay within
// max, break on the nicest available boundary, and lose no content except the
// single separator a break lands on.
func TestChunkMessage(t *testing.T) {
	tests := []struct {
		name string
		in   string
		max  int
		want []string
	}{
		{"empty", "", 10, nil},
		{"under limit", "hello", 10, []string{"hello"}},
		{"exactly limit", "helloworld", 10, []string{"helloworld"}},
		{"breaks on newline", "line one\nline two", 10, []string{"line one", "line two"}},
		{"breaks on last space within window", "alpha beta gamma", 10, []string{"alpha", "beta gamma"}},
		{"hard cut unbroken run", "aaaaaaaaaaaaa", 5, []string{"aaaaa", "aaaaa", "aaa"}},
		{"breaks at last newline within window", "a\nb\nc\nd", 5, []string{"a\nb", "c\nd"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := chunkMessage(tt.in, tt.max)
			if len(got) != len(tt.want) {
				t.Fatalf("chunkMessage(%q,%d) = %v (%d chunks), want %v (%d)", tt.in, tt.max, got, len(got), tt.want, len(tt.want))
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("chunk %d = %q, want %q", i, got[i], tt.want[i])
				}
				if len(got[i]) > tt.max {
					t.Errorf("chunk %d length %d exceeds max %d", i, len(got[i]), tt.max)
				}
			}
		})
	}
}

// No chunk may ever exceed the limit, whatever the input — the property that
// actually keeps Discord from rejecting a send.
func TestChunkMessageNeverExceedsMax(t *testing.T) {
	inputs := []string{
		strings.Repeat("x", 5000),
		strings.Repeat("word ", 1000),
		strings.Repeat("line\n", 800),
	}
	const max = 1990
	for _, in := range inputs {
		for i, c := range chunkMessage(in, max) {
			if len(c) > max {
				t.Errorf("input len %d: chunk %d length %d exceeds max %d", len(in), i, len(c), max)
			}
		}
	}
}

// parseBotCommand distinguishes manual overrides from ordinary messages by the
// first token, and carries the change name for !new.
func TestParseBotCommand(t *testing.T) {
	tests := []struct {
		in       string
		wantKind string
		wantArg  string
	}{
		{"!status", "status", ""},
		{"!continue", "continue", ""},
		{"!new add-metrics", "new", "add-metrics"},
		{"!new   the auth bug  ", "new", "the auth bug"},
		{"!new", "new", ""},
		{"fix the login bug", "", "fix the login bug"},
		{"", "", ""},
		{"!newish thing", "", "!newish thing"}, // not !new — must not match as a command
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			kind, arg := parseBotCommand(tt.in)
			if kind != tt.wantKind || arg != tt.wantArg {
				t.Errorf("parseBotCommand(%q) = (%q, %q), want (%q, %q)", tt.in, kind, arg, tt.wantKind, tt.wantArg)
			}
		})
	}
}

// stripMention removes the bot's mention in both plain and nickname forms so the
// model sees the request, not the plumbing.
func TestStripMention(t *testing.T) {
	const botID = "12345"
	tests := []struct {
		in   string
		want string
	}{
		{"<@12345> fix the bug", " fix the bug"},
		{"<@!12345> deploy", " deploy"},
		{"no mention here", "no mention here"},
		{"hey <@12345> and <@!12345> done", "hey  and  done"},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			if got := stripMention(tt.in, botID); got != tt.want {
				t.Errorf("stripMention(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}
