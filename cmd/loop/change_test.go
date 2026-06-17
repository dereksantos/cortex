package main

import "testing"

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
		{"strips punctuation", "feat: add `loop turn`", "feat-add-loop-turn"},
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

// onChangeBranch gates automated commits: only loop/* branches are change
// branches, so a commit can't accidentally land on main or a feature branch.
func TestOnChangeBranch(t *testing.T) {
	tests := []struct {
		branch string
		want   bool
	}{
		{"loop/fix-login", true},
		{"loop/change", true},
		{"main", false},
		{"major-rework", false},
		{"feature/loop-thing", false},
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
