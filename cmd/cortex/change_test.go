package main

import (
	"testing"
)

// appendTrailerToMessage tests the trailer append logic.
func TestAppendTrailerToMessage(t *testing.T) {
	tests := []struct {
		name      string
		message   string
		trailer   string
		want      string
		wantAdded bool
	}{
		{"no trailer", "fix login", "", "fix login", false},
		{"trailer added", "fix login", "Co-Authored-By: Cortex (qwen3-coder)", "fix login\n\nCo-Authored-By: Cortex (qwen3-coder)", true},
		{"trailer already exists", "fix login\n\nCo-Authored-By: Cortex (qwen3-coder)", "Co-Authored-By: Cortex (qwen3-coder)", "fix login\n\nCo-Authored-By: Cortex (qwen3-coder)", false},
		{"trailer with spaces", "fix login", "Co-Authored-By: Cortex", "fix login\n\nCo-Authored-By: Cortex", true},
		{"empty message", "", "Co-Authored-By: Cortex", "\n\nCo-Authored-By: Cortex", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, added := appendTrailerToMessage(tt.message, tt.trailer)
			if got != tt.want {
				t.Errorf("appendTrailerToMessage(%q, %q) = %q, want %q", tt.message, tt.trailer, got, tt.want)
			}
			if added != tt.wantAdded {
				t.Errorf("appendTrailerToMessage(%q, %q) added=%v, want %v", tt.message, tt.trailer, added, tt.wantAdded)
			}
		})
	}
}

// slugifyChange feeds branch names, so it must stay within safe ref characters
// and never produce a leading/trailing/doubled dash or an empty suffix.
func TestSlugifyChange(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"simple", "fix login", "fix-login"},
		{"already slug", "fix-login", "fix-login"},
		{"mixed case", "Fix Login Bug", "fix-login-bug"},
		{"collapses runs", "fix   the:: login!!", "fix-the-login"},
		{"trims edges", "  --Fix Login--  ", "fix-login"},
		{"keeps digits", "issue 42 retry", "issue-42-retry"},
		{"strips punctuation", "feat: add `cortex turn`", "feat-add-cortex-turn"},
		{"empty falls back", "", "change"},
		{"punctuation only falls back", "!!!", "change"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := slugifyChange(tt.in); got != tt.want {
				t.Errorf("slugifyChange(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

// onChangeBranch gates automated commits: only cortex/* branches are change
// branches, so a commit can't accidentally land on main or a feature branch.
func TestOnChangeBranch(t *testing.T) {
	tests := []struct {
		branch string
		want   bool
	}{
		{"cortex/fix-login", true},
		{"cortex/change", true},
		{"main", false},
		{"major-rework", false},
		{"feature/cortex-thing", false},
		{"HEAD", false},
		{"", false},
	}
	for _, tt := range tests {
		t.Run(tt.branch, func(t *testing.T) {
			if got := onChangeBranch(tt.branch); got != tt.want {
				t.Errorf("onChangeBranch(%q) = %v, want %v", tt.branch, got, tt.want)
			}
		})
	}
}
