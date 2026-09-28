package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestCommitChangeWithAttribution exercises commitChangeWithAttribution in a
// temp git repo: it must produce exactly one trailer, be idempotent when the
// message already carries it, keep an unrelated trailer, and leave the
// commit plain when attribution is disabled (the real Enabled=false path,
// not a nil config).
func TestCommitChangeWithAttribution(t *testing.T) {
	disabled := false
	tests := []struct {
		name             string
		message          string
		cfg              *Config
		wantAttributed   bool
		wantTrailerCount int
	}{
		{
			name:             "no trailer - adds one",
			message:          "fix: login bug",
			cfg:              &Config{Models: map[string]ModelSpec{"code": {Model: "qwen3-coder"}}},
			wantAttributed:   true,
			wantTrailerCount: 1,
		},
		{
			name:             "same trailer already present - no duplicate",
			message:          "fix: login bug\n\nCo-Authored-By: Cortex (qwen3-coder)",
			cfg:              &Config{Models: map[string]ModelSpec{"code": {Model: "qwen3-coder"}}},
			wantAttributed:   true,
			wantTrailerCount: 1,
		},
		{
			name:             "different trailer - both kept",
			message:          "fix: login bug\n\nCo-Authored-By: Alice <a@example.com>",
			cfg:              &Config{Models: map[string]ModelSpec{"code": {Model: "qwen3-coder"}}},
			wantAttributed:   true,
			wantTrailerCount: 2,
		},
		{
			name:             "attribution disabled (Enabled=false) - no trailer",
			message:          "fix: login bug",
			cfg:              &Config{Attribution: AttributionConfig{Enabled: &disabled}, Models: map[string]ModelSpec{"code": {Model: "qwen3-coder"}}},
			wantAttributed:   false,
			wantTrailerCount: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmpDir := t.TempDir()
			// Initialize git repo
			gitCmd(t, tmpDir, "init")
			gitCmd(t, tmpDir, "config", "user.name", "Test User")
			gitCmd(t, tmpDir, "config", "user.email", "test@example.com")

			// Create initial commit on main
			filePath := filepath.Join(tmpDir, "test.txt")
			if err := os.WriteFile(filePath, []byte("initial"), 0o644); err != nil {
				t.Fatal(err)
			}
			gitCmd(t, tmpDir, "add", "test.txt")
			gitCmd(t, tmpDir, "commit", "-m", "initial")

			// Create and checkout change branch
			gitCmd(t, tmpDir, "checkout", "-b", "cortex/test")

			// Modify file to make it dirty
			if err := os.WriteFile(filePath, []byte("modified"), 0o644); err != nil {
				t.Fatal(err)
			}

			// Commit with attribution
			_, attributed, err := commitChangeWithAttribution(tmpDir, tt.message, tt.cfg)
			if err != nil {
				t.Fatalf("commitChangeWithAttribution failed: %v", err)
			}
			if attributed != tt.wantAttributed {
				t.Errorf("attributed = %v, want %v", attributed, tt.wantAttributed)
			}

			// Count trailers in the last commit
			output, err := gitCmdOutput(t, tmpDir, "log", "-1", "--format=%B")
			if err != nil {
				t.Fatalf("git log failed: %v", err)
			}

			trailerCount := countTrailers(output, "Co-Authored-By")
			if trailerCount != tt.wantTrailerCount {
				t.Errorf("trailer count = %d, want %d. Output:\n%s", trailerCount, tt.wantTrailerCount, output)
			}
		})
	}
}

func countTrailers(message, key string) int {
	count := 0
	lines := strings.Split(message, "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, key+":") {
			count++
		}
	}
	return count
}

func gitCmd(t *testing.T, dir string, args ...string) {
	t.Helper()
	_, err := gitCmdOutput(t, dir, args...)
	if err != nil {
		t.Fatalf("git %s failed in %s: %v", strings.Join(args, " "), dir, err)
	}
}

func gitCmdOutput(t *testing.T, dir string, args ...string) (string, error) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	output, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(output)), err
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
