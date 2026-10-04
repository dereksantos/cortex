package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dereksantos/cortex/internal/lineedit"
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

// TestProcessMentionsProseLeavesInputUnchanged covers the prose cases:
// only an @ starting a whitespace-delimited word is a mention, and a
// mention that does not resolve to a readable file is left in the input
// EXACTLY as the user typed it — the user's prose is what goes into history
// and to the model.
func TestProcessMentionsProseLeavesInputUnchanged(t *testing.T) {
	root := setupMentionWorkspace(t)
	tests := []struct {
		name  string
		input string
	}{
		{"email address", "mail user@example.com"},
		{"scoped npm name mid-identifier context", "bump @types/node"},
		{"Java-style annotation", "annotate @Override on the method"},
		{"trailing punctuation after a real mention resolves", "look at @main.go, please"},
		{"missing file stays as typed", "see @missing.txt"},
		{"out-of-workspace path stays as typed", "see @../../etc/passwd"},
		{"absolute path stays as typed", "see @/etc/passwd"},
		{"a directory stays as typed", "see @src"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			clean, attach := processMentions(root, tt.input)
			// For every case above, the input must come back unchanged:
			// email/scoped/annotation are prose (not mentions at all);
			// missing/escape/absolute/directory are mentions that did not
			// resolve — left as typed, with NO attachment note.
			// Exception: the punctuation case resolves (main.go exists), so
			// it IS rewritten — handled below with its own expectations.
			if tt.name == "trailing punctuation after a real mention resolves" {
				// The mention resolves (main.go exists): the comma is stripped
				// before resolution and the marker names the resolved path,
				// while the user's trailing punctuation stays in the line.
				if clean != "look at [@main.go attached], please" {
					t.Errorf("clean = %q, want \"look at [@main.go attached], please\" (comma stripped, path resolved, punctuation kept)", clean)
				}
				if !strings.Contains(attach, "inlined") || !strings.Contains(attach, "package main") {
					t.Errorf("attach = %q, want main.go inlined", attach)
				}
				return
			}
			if clean != tt.input {
				t.Errorf("clean = %q, want the input unchanged (%q)", clean, tt.input)
			}
			if attach != "" {
				t.Errorf("attach = %q, want empty (unresolved mention: no note)", attach)
			}
		})
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

// TestProcessMentionsLargeFileOutlines verifies the large-file branch uses
// the same size rule as read_file (CurationBudgetTokens, ~16k tokens) and
// hands back a structural outline with a pointer to study, instead of a
// 64k-byte inline.
func TestProcessMentionsLargeFileOutlines(t *testing.T) {
	root := t.TempDir()
	// ~70k bytes: above 16k tokens (the curation budget) by a wide margin.
	big := strings.Repeat("lorem ipsum dolor sit amet\n", 5000)
	if err := os.WriteFile(filepath.Join(root, "big.txt"), []byte(big), 0o644); err != nil {
		t.Fatalf("write big: %v", err)
	}
	clean, attach := processMentions(root, "review @big.txt")
	if !strings.Contains(clean, "[@big.txt attached]") {
		t.Errorf("clean = %q, want the mention marked", clean)
	}
	if strings.Contains(attach, big) {
		t.Errorf("attach inlined the whole 70k file — it should be reduced to an outline")
	}
	if !strings.Contains(attach, "too large to inline") {
		t.Errorf("attach = %q, want the too-large-to-inline note", attach)
	}
	if !strings.Contains(attach, "study") {
		t.Errorf("attach = %q, want a pointer to study", attach)
	}
}

// TestMentionCompleterSingleModelSource drives the REAL wired completers
// (mentionCompleter's map shape, as cmd/cortex/main.go installs them) for a
// /model continuation: every model id must appear exactly once in the
// candidate list — the old double wiring (SlashCompleter.Sub AND a separate
// "model" entry) listed each id twice and never filled the prefix on the
// first Tab. The ids are word-level (bare model names), so the first Tab
// fills the common model-id prefix in place of the argument.
func TestMentionCompleterSingleModelSource(t *testing.T) {
	names := []string{"qwen/qwen3-coder:free", "tencent/hy3:free"}
	commands := []string{
		"/clear", "/compact", "/context", "/help", "/hook",
		"/model", "/plan", "/quit", "/sessions",
	}
	// The map exactly as mentionCompleter builds it (minus the session's
	// live model list, which the Sub hook's Names func stands in for):
	// ONE source for /model, the slash completer's Sub hook — no separate
	// "model" entry.
	completers := map[string]lineedit.Completer{
		"slash": lineedit.SlashCompleter{
			Commands: commands,
			Sub: func(line string, cursor int) []string {
				if !strings.HasPrefix(line, "/model ") {
					return nil
				}
				return lineedit.ModelCompleter{Names: func() []string { return names }}.Candidates(line, cursor)
			},
		},
		"path": lineedit.PathCompleter{Root: t.TempDir(), MaxCandidates: 50},
	}

	// Merged exactly as Terminal.completionCandidates merges them —
	// including the "model" slot (absent here), so a regression re-adding
	// it would double every id in this merged list.
	var cands []string
	for _, k := range []string{"slash", "model", "path"} {
		if c, ok := completers[k]; ok {
			cands = append(cands, c.Candidates("/model q", 8)...)
		}
	}
	if len(cands) != 1 {
		t.Fatalf("candidates = %v, want exactly one (the single matching id, no duplicates)", cands)
	}
	if cands[0] != "qwen/qwen3-coder:free" {
		t.Errorf("candidates[0] = %q, want the bare id (word-level: it replaces the argument word)", cands[0])
	}

	// The first Tab fills the common model-id prefix into the argument.
	cm := lineedit.NewCompletions()
	line, pos, _ := cm.Tab("/model q", 8, func(string, int) []string { return cands })
	if line != "/model qwen/qwen3-coder:free" || pos != 28 {
		t.Errorf("first Tab = (%q, %d), want the id filled in place of 'q'", line, pos)
	}
}
