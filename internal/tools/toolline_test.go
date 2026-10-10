package tools

import (
	"errors"
	"testing"
	"time"
)

func TestSplitAction(t *testing.T) {
	tests := []struct {
		action, verb, target string
	}{
		{"read_file(go.mod)", "read", "go.mod"},
		{`grep("a(b)", dir)`, "grep", `"a(b)", dir`},
		{"read_file(big.py) → skeleton (~18k tokens, too large)", "read", "big.py → skeleton (~18k tokens, too large)"},
		{"scan_landscape()", "scan_landscape", ""},
		{"output 120 KB → summarize(spill.txt)", "output 120 KB → summarize(spill.txt)", ""},
		{"memory_search(deploy, scope=user)", "memory_search", "deploy, scope=user"},
		{"bash(echo (unclosed", "bash", "echo (unclosed"},
	}
	for _, tt := range tests {
		t.Run(tt.action, func(t *testing.T) {
			verb, target := splitAction(tt.action)
			if verb != tt.verb || target != tt.target {
				t.Errorf("splitAction(%q) = (%q, %q), want (%q, %q)", tt.action, verb, target, tt.verb, tt.target)
			}
		})
	}
}

func TestCallResult(t *testing.T) {
	tests := []struct {
		name string
		p    pendingAction
		d    time.Duration
		out  string
		err  error
		want string
	}{
		{"fast call: summary only", pendingAction{}, 3 * time.Millisecond, "a\nb\n", nil, "2 lines, 4 B"},
		{"slow call: elapsed leads", pendingAction{}, 6800 * time.Millisecond, "ok", nil, "6.8s  ok"},
		{"diff counts replace the summary", pendingAction{result: "+9 -2", extra: []string{"@@"}}, time.Millisecond, "edited x", nil, "+9 -2"},
		{"diff without counts: no echo", pendingAction{extra: []string{"no change"}}, time.Millisecond, "edited x", nil, ""},
		{"error wins over counts", pendingAction{result: "+1"}, time.Millisecond, "", errors.New("boom"), "error: boom"},
		{"whitespace collapsed", pendingAction{}, time.Millisecond, "ok  \tpkg\t0.5s", nil, "ok pkg 0.5s"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := callResult(&tt.p, tt.d, tt.out, tt.err); got != tt.want {
				t.Errorf("callResult = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestShortAction(t *testing.T) {
	tests := []struct{ in, want string }{
		{"read_file(greet.py)", "read greet.py"},
		{"bash(python3 greet.py)", "bash python3 greet.py"},
		{"grep", "grep"},
	}
	for _, tt := range tests {
		if got := ShortAction(tt.in); got != tt.want {
			t.Errorf("ShortAction(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}
