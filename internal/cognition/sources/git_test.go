package sources

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestGitSource_Name(t *testing.T) {
	source := NewGitSource("/tmp")
	if source.Name() != "git" {
		t.Errorf("expected name 'git', got %q", source.Name())
	}
}

func TestGitSource_Sample_InGitRepo(t *testing.T) {
	// Find project root (where .git exists)
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}

	// Walk up to find .git directory
	projectRoot := cwd
	for {
		gitDir := filepath.Join(projectRoot, ".git")
		if _, err := os.Stat(gitDir); err == nil {
			break
		}
		parent := filepath.Dir(projectRoot)
		if parent == projectRoot {
			t.Skip("not in a git repository")
		}
		projectRoot = parent
	}

	source := NewGitSource(projectRoot)

	// Sample 5 items
	items, err := source.Sample(context.Background(), 5)
	if err != nil {
		t.Fatalf("Sample failed: %v", err)
	}

	// Should get some items from a git repo
	if len(items) == 0 {
		t.Error("expected some items from git history")
	}

	// Verify item structure
	for _, item := range items {
		if item.Source != "git" {
			t.Errorf("expected source 'git', got %q", item.Source)
		}
		if item.ID == "" {
			t.Error("item ID should not be empty")
		}
		if item.Content == "" {
			t.Error("item content should not be empty")
		}

		// Check metadata
		itemType, ok := item.Metadata["type"].(string)
		if !ok {
			t.Error("item should have 'type' metadata")
		}
		if itemType != "commit" && itemType != "diff" {
			t.Errorf("unexpected item type: %q", itemType)
		}
	}

	t.Logf("Got %d items from git history", len(items))
}

func TestGitSource_Sample_NotGitRepo(t *testing.T) {
	// Create temp dir that's not a git repo
	tmpDir, err := os.MkdirTemp("", "not-git-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	source := NewGitSource(tmpDir)

	// Sample should return empty, not error
	items, err := source.Sample(context.Background(), 5)
	if err != nil {
		t.Fatalf("Sample failed: %v", err)
	}

	if len(items) != 0 {
		t.Errorf("expected 0 items from non-git directory, got %d", len(items))
	}
}

func TestClassifyCommit(t *testing.T) {
	tests := []struct {
		subject  string
		expected string
	}{
		{"feat: add new feature", "feature"},
		{"fix: resolve bug", "bugfix"},
		{"feat(auth): implement login", "feature"},
		{"docs: update README", "documentation"},
		{"refactor: clean up code", "refactor"},
		{"test: add unit tests", "test"},
		{"chore: update deps", "chore"},
		{"Add user authentication", "feature"},
		{"Fix login bug", "bugfix"},
		{"Refactor handler code", "refactor"},
		{"Update documentation", "documentation"},
		{"Random commit message", "other"},
	}

	for _, tc := range tests {
		t.Run(tc.subject, func(t *testing.T) {
			result := classifyCommit(tc.subject)
			if result != tc.expected {
				t.Errorf("classifyCommit(%q) = %q, want %q", tc.subject, result, tc.expected)
			}
		})
	}
}

func TestIsHex(t *testing.T) {
	tests := []struct {
		input    string
		expected bool
	}{
		{"abc123", true},
		{"ABCDEF", true},
		{"0123456789abcdef", true},
		{"xyz", false},
		{"abc-123", false},
		{"", true}, // empty string is valid hex (vacuously true)
	}

	for _, tc := range tests {
		t.Run(tc.input, func(t *testing.T) {
			result := isHex(tc.input)
			if result != tc.expected {
				t.Errorf("isHex(%q) = %v, want %v", tc.input, result, tc.expected)
			}
		})
	}
}
