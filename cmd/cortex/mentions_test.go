package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// setupMentionWorkspace creates a small workspace with a few files for the
// mention tests.
func setupMentionWorkspace(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	files := map[string]string{
		"src/alpha.go": "package main\n",
		"main.go":      "package main\n",
		"notes.txt":    "hello world\n",
	}
	for path, content := range files {
		p := filepath.Join(root, filepath.FromSlash(path))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", p, err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatalf("write %s: %v", p, err)
		}
	}
	return root
}

func TestProcessMentionsNoMentions(t *testing.T) {
	root := setupMentionWorkspace(t)
	clean, attach := processMentions(root, "just a normal line")
	if clean != "just a normal line" {
		t.Errorf("clean = %q, want unchanged", clean)
	}
	if attach != "" {
		t.Errorf("attach = %q, want empty", attach)
	}
}

func TestProcessMentionsInlinesSmallFile(t *testing.T) {
	root := setupMentionWorkspace(t)
	clean, attach := processMentions(root, "look at @notes.txt please")
	if !strings.Contains(clean, "[@notes.txt attached]") {
		t.Errorf("clean = %q, want the mention replaced by a marker", clean)
	}
	if !strings.Contains(attach, "inlined") {
		t.Errorf("attach = %q, want the inlined marker", attach)
	}
	if !strings.Contains(attach, "hello world") {
		t.Errorf("attach = %q, want the file content inlined", attach)
	}
}

func TestProcessMentionsRefusesOutOfWorkspace(t *testing.T) {
	root := setupMentionWorkspace(t)
	clean, attach := processMentions(root, "see @../../etc/passwd")
	if !strings.Contains(attach, "refused") {
		t.Errorf("attach = %q, want a refusal note", attach)
	}
	if !strings.Contains(clean, "refused") {
		t.Errorf("clean = %q, want the refused marker", clean)
	}
}

func TestProcessMentionsRefusesAbsolutePath(t *testing.T) {
	root := setupMentionWorkspace(t)
	_, attach := processMentions(root, "see @/etc/passwd")
	if !strings.Contains(attach, "absolute") {
		t.Errorf("attach = %q, want the absolute-path refusal", attach)
	}
}

func TestProcessMentionsNotFound(t *testing.T) {
	root := setupMentionWorkspace(t)
	clean, attach := processMentions(root, "see @missing.txt")
	if !strings.Contains(attach, "not found") {
		t.Errorf("attach = %q, want a not-found note", attach)
	}
	if !strings.Contains(clean, "not found") {
		t.Errorf("clean = %q, want the not-found marker", clean)
	}
}

func TestProcessMentionsDirectory(t *testing.T) {
	root := setupMentionWorkspace(t)
	_, attach := processMentions(root, "see @src")
	if !strings.Contains(attach, "directory") {
		t.Errorf("attach = %q, want a directory note", attach)
	}
}

func TestProcessMentionsMultiple(t *testing.T) {
	root := setupMentionWorkspace(t)
	clean, attach := processMentions(root, "see @notes.txt and @main.go")
	if !strings.Contains(clean, "[@notes.txt attached]") || !strings.Contains(clean, "[@main.go attached]") {
		t.Errorf("clean = %q, want both mentions replaced", clean)
	}
	if strings.Count(attach, "inlined") < 2 {
		t.Errorf("attach = %q, want both files inlined", attach)
	}
}

func TestConfineMention(t *testing.T) {
	root := setupMentionWorkspace(t)

	tests := []struct {
		name    string
		rel     string
		wantErr bool
	}{
		{"valid path", "main.go", false},
		{"nested path", "src/alpha.go", false},
		{"dot-dot escape", "../etc/passwd", true},
		{"absolute path", "/etc/passwd", true},
		{"bare filename", "notes.txt", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := confineMention(root, tt.rel)
			if (err != nil) != tt.wantErr {
				t.Errorf("confineMention(%q) err = %v, wantErr %v", tt.rel, err, tt.wantErr)
			}
		})
	}
}
