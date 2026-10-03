package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dereksantos/cortex/internal/journal"
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
			// The attribution receipt this commit writes is a machine-level
			// journal event; keep it out of the real ~/.cortex.
			t.Setenv("CORTEX_HOME", t.TempDir())
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

			// And the receipt the commit wrote says the same thing the repo
			// says (issue #146): one event, verified, naming the trailer or
			// recording that attribution was off.
			events := readAttributionEvents(t)
			if len(events) != 1 {
				t.Fatalf("attribution events = %d, want exactly 1: %+v", len(events), events)
			}
			got := events[0]
			wantOutcome := journal.AttributionOutcomeAdded
			if !tt.wantAttributed {
				wantOutcome = journal.AttributionOutcomeDisabled
			}
			if got.Outcome != wantOutcome {
				t.Errorf("outcome = %q, want %q", got.Outcome, wantOutcome)
			}
			if !got.Verified {
				t.Error("receipt not marked verified, want the post-commit read-back")
			}
			if got.SHA == "" {
				t.Error("receipt has no SHA")
			}
			full, err := gitCmdOutput(t, tmpDir, "rev-parse", "HEAD")
			if err != nil {
				t.Fatalf("git rev-parse: %v", err)
			}
			if got.SHA != full {
				t.Errorf("receipt SHA = %q, want HEAD %q", got.SHA, full)
			}
			if got.TrailerPresent != tt.wantAttributed {
				t.Errorf("TrailerPresent = %v, want %v", got.TrailerPresent, tt.wantAttributed)
			}
			if got.Project != tmpDir {
				t.Errorf("Project = %q, want %q", got.Project, tmpDir)
			}
			if !strings.HasPrefix(got.Command, "cortex change commit ") {
				t.Errorf("Command = %q, want it to name the CLI path", got.Command)
			}
		})
	}
}

// TestChangeCommitRefusalsJournalNothing pins the other half of the contract:
// a commit that never happened writes no receipt. A refusal reason already
// reaches the caller, and an entry with a SHA the repository doesn't have
// would be worse than no entry at all.
func TestChangeCommitRefusalsJournalNothing(t *testing.T) {
	tests := []struct {
		name    string
		branch  string // "" leaves HEAD where init put it (not a change branch)
		message string
	}{
		{
			name:    "not on a change branch",
			message: "fix: something",
		},
		{
			name:    "nothing to commit",
			branch:  "cortex/test",
			message: "fix: something",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("CORTEX_HOME", t.TempDir())
			tmpDir := t.TempDir()
			gitCmd(t, tmpDir, "init")
			gitCmd(t, tmpDir, "config", "user.name", "Test User")
			gitCmd(t, tmpDir, "config", "user.email", "test@example.com")
			filePath := filepath.Join(tmpDir, "test.txt")
			if err := os.WriteFile(filePath, []byte("initial"), 0o644); err != nil {
				t.Fatal(err)
			}
			gitCmd(t, tmpDir, "add", "test.txt")
			gitCmd(t, tmpDir, "commit", "-m", "initial")
			if tt.branch != "" {
				gitCmd(t, tmpDir, "checkout", "-b", tt.branch)
			}

			if _, _, err := commitChangeWithAttribution(tmpDir, tt.message, nil); err == nil {
				t.Fatal("commitChangeWithAttribution succeeded, want a refusal")
			}
			if events := readAttributionEvents(t); len(events) != 0 {
				t.Errorf("attribution events = %+v, want none for a commit that did not happen", events)
			}
		})
	}
}

// readAttributionEvents reads the machine-level attribution journal the
// isolated CORTEX_HOME holds, oldest first.
func readAttributionEvents(t *testing.T) []journal.AttributionCommitPayload {
	t.Helper()
	got, _, err := journal.LatestAttributionCommits()
	if err != nil {
		t.Fatalf("LatestAttributionCommits: %v", err)
	}
	return got
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
