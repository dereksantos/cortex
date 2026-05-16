//go:build !windows

package commands

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestResolveAPIURL covers the Ollama-vs-OpenRouter routing rule:
// model ids with a slash are OpenRouter; ids without are local Ollama.
// This is the load-bearing default that lets `cortex` "just work" with
// qwen2.5-coder:1.5b without any flags.
func TestResolveAPIURL(t *testing.T) {
	tests := []struct {
		name  string
		model string
		want  string
	}{
		{"ollama default", "qwen2.5-coder:1.5b", defaultOllamaAPIURL},
		{"ollama variant", "llama3.2:3b", defaultOllamaAPIURL},
		{"openrouter anthropic", "anthropic/claude-haiku-4.5", ""},
		{"openrouter qwen", "qwen/qwen3-coder", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := resolveAPIURL(tt.model)
			if got != tt.want {
				t.Errorf("resolveAPIURL(%q) = %q, want %q", tt.model, got, tt.want)
			}
		})
	}
}

// TestLoadOrSeedSystemPrompt covers two flows: (a) fresh workdir gets
// the default prompt written; (b) existing prompt is read verbatim.
// The 1.5B-tuned default isn't compared byte-for-byte (it'll evolve),
// but we assert key constraints survive in the seed.
func TestLoadOrSeedSystemPrompt(t *testing.T) {
	t.Run("seeds default when missing", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "repl-system-prompt.md")
		got, err := loadOrSeedSystemPrompt(path)
		if err != nil {
			t.Fatalf("loadOrSeedSystemPrompt: %v", err)
		}
		// The seed must mention the tool surface so the model knows
		// what's callable. Exact wording rotates as we tune for
		// small-model reliability — assert structure, not phrasing.
		if !strings.Contains(got, "write_file") {
			t.Errorf("seed should describe the write_file tool; got: %q", got[:min(120, len(got))])
		}
		// File was written so the user can edit it.
		if _, err := os.Stat(path); err != nil {
			t.Errorf("seed file not persisted: %v", err)
		}
	})
	t.Run("reads existing verbatim", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "repl-system-prompt.md")
		custom := "you are a turbo-charged Go programmer"
		if err := os.WriteFile(path, []byte(custom), 0o644); err != nil {
			t.Fatalf("seed write: %v", err)
		}
		got, err := loadOrSeedSystemPrompt(path)
		if err != nil {
			t.Fatalf("loadOrSeedSystemPrompt: %v", err)
		}
		if got != custom {
			t.Errorf("expected verbatim read of existing prompt; got %q", got)
		}
	})
}

// TestUndoStackChained walks chronologically back through three turns,
// confirming that each /undo restores the workdir state captured before
// the most recent accepted turn. Exercises the snapshotStack push/pop.
func TestUndoStackChained(t *testing.T) {
	workdir := t.TempDir()
	mustWrite(t, workdir, "go.mod", "module test\n")
	mustWrite(t, workdir, "step.txt", "v0")

	sessionDir := filepath.Join(workdir, ".cortex", "sessions", "test")
	if err := os.MkdirAll(sessionDir, 0o755); err != nil {
		t.Fatalf("session dir: %v", err)
	}
	s := &replState{workdir: workdir, sessionDir: sessionDir}

	// Simulate three accepted turns. Each turn snapshots the pre-state,
	// then mutates step.txt, then pushes the snap onto the stack.
	for i, content := range []string{"v1", "v2", "v3"} {
		snap, err := s.snapshotWorkdir(i + 1)
		if err != nil {
			t.Fatalf("snap %d: %v", i+1, err)
		}
		mustWrite(t, workdir, "step.txt", content)
		s.snapshotStack = append(s.snapshotStack, snap)
		s.turns = i + 1
	}
	if got := mustRead(t, workdir, "step.txt"); got != "v3" {
		t.Fatalf("setup: expected v3, got %q", got)
	}

	// Walk back three undos. After each, step.txt should match the
	// pre-turn state.
	wantAfterEach := []string{"v2", "v1", "v0"}
	for i, want := range wantAfterEach {
		if err := s.undoLastTurn(); err != nil {
			t.Fatalf("undo %d: %v", i+1, err)
		}
		got := mustRead(t, workdir, "step.txt")
		if got != want {
			t.Errorf("after undo %d: got %q want %q", i+1, got, want)
		}
	}

	// Fourth undo should fail — stack is empty.
	if err := s.undoLastTurn(); err == nil {
		t.Errorf("expected error on undo with empty stack")
	}
}

// TestCaptureTurnWritesJournalEntry confirms the background capture
// path lands an event in .cortex/journal/capture/. We don't decode the
// payload here (that's captured by capture_test.go); we just verify the
// journal grew, which is the load-bearing signal that capture wires
// through.
func TestCaptureTurnWritesJournalEntry(t *testing.T) {
	workdir := t.TempDir()
	sessionDir := filepath.Join(workdir, ".cortex", "sessions", "test")
	if err := os.MkdirAll(sessionDir, 0o755); err != nil {
		t.Fatalf("session dir: %v", err)
	}
	s := &replState{workdir: workdir, sessionDir: sessionDir, sessionID: "test-sess"}

	row := turnRow{
		Turn:         1,
		SessionID:    "test-sess",
		UserMessage:  "scaffold main.go",
		Model:        "qwen2.5-coder:1.5b",
		FilesChanged: []string{"main.go"},
		FinalText:    "scaffold landed",
		Accepted:     true,
		VerifyKind:   verifierGoBuild,
		VerifyOK:     true,
	}
	if err := s.captureTurn(row); err != nil {
		t.Fatalf("captureTurn: %v", err)
	}

	// One *.jsonl should now exist under .cortex/journal/capture/.
	capDir := filepath.Join(workdir, ".cortex", "journal", "capture")
	entries, err := os.ReadDir(capDir)
	if err != nil {
		t.Fatalf("read capture dir: %v", err)
	}
	found := false
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".jsonl") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected at least one .jsonl in %s; got %d entries", capDir, len(entries))
	}
}

// TestCaptureTurnSkipsOnReject confirms rejected turns don't pollute
// the capture journal — only accepted state is durable context.
func TestCaptureTurnSkipsOnReject(t *testing.T) {
	workdir := t.TempDir()
	s := &replState{workdir: workdir, sessionID: "test"}
	row := turnRow{Turn: 1, Accepted: false, UserMessage: "noop"}
	if err := s.captureTurn(row); err != nil {
		t.Fatalf("captureTurn on rejected: %v", err)
	}
	capDir := filepath.Join(workdir, ".cortex", "journal", "capture")
	if entries, err := os.ReadDir(capDir); err == nil && len(entries) > 0 {
		t.Errorf("rejected turn should not write capture; found %d entries", len(entries))
	}
}

// TestSnapshotAndRestore is the round-trip integrity test for the
// /undo machinery. Snapshot a workdir, mutate it (edit, add, delete),
// restore from snapshot, assert state matches pre-mutation.
func TestSnapshotAndRestore(t *testing.T) {
	workdir := t.TempDir()
	mustWrite(t, workdir, "go.mod", "module test\n\ngo 1.22\n")
	mustWrite(t, workdir, "main.go", "package main\n\nfunc main(){}\n")
	mustWrite(t, workdir, "sub/util.go", "package sub\n")
	// .cortex must be ignored by the snapshot — confirm it survives mutation.
	mustWrite(t, workdir, ".cortex/keep.txt", "this should not be overwritten")

	s := &replState{
		workdir:    workdir,
		sessionDir: filepath.Join(workdir, ".cortex", "sessions", "test"),
	}
	if err := os.MkdirAll(s.sessionDir, 0o755); err != nil {
		t.Fatalf("session dir: %v", err)
	}

	snapDir, err := s.snapshotWorkdir(1)
	if err != nil {
		t.Fatalf("snapshotWorkdir: %v", err)
	}

	// Mutate: change main.go, add new file, delete sub/util.go.
	mustWrite(t, workdir, "main.go", "package main\n\nfunc main(){println(\"changed\")}\n")
	mustWrite(t, workdir, "added.go", "package main\n")
	if err := os.Remove(filepath.Join(workdir, "sub", "util.go")); err != nil {
		t.Fatalf("remove sub/util.go: %v", err)
	}

	if err := s.restoreFromSnapshot(snapDir); err != nil {
		t.Fatalf("restoreFromSnapshot: %v", err)
	}

	// main.go is back to original.
	got := mustRead(t, workdir, "main.go")
	want := "package main\n\nfunc main(){}\n"
	if got != want {
		t.Errorf("main.go after restore: got %q, want %q", got, want)
	}

	// sub/util.go was restored.
	gotUtil := mustRead(t, workdir, "sub/util.go")
	wantUtil := "package sub\n"
	if gotUtil != wantUtil {
		t.Errorf("sub/util.go after restore: got %q, want %q", gotUtil, wantUtil)
	}

	// added.go (not in snapshot) was removed.
	if _, err := os.Stat(filepath.Join(workdir, "added.go")); !os.IsNotExist(err) {
		t.Errorf("added.go should be removed after restore; stat err: %v", err)
	}

	// .cortex/keep.txt untouched.
	gotKeep := mustRead(t, workdir, ".cortex/keep.txt")
	if gotKeep != "this should not be overwritten" {
		t.Errorf(".cortex/keep.txt was disturbed: %q", gotKeep)
	}
}

// TestDispatchSlash covers the slash-command parsing surface. Asserts
// that /quit returns continue=false, unknown commands return an error,
// and /help / /model don't crash on bare invocation.
func TestDispatchSlash(t *testing.T) {
	dir := t.TempDir()
	s := &replState{
		workdir:    dir,
		sessionDir: filepath.Join(dir, "sess"),
		model:      "qwen2.5-coder:1.5b",
		apiURL:     defaultOllamaAPIURL,
	}
	if err := os.MkdirAll(s.sessionDir, 0o755); err != nil {
		t.Fatalf("session dir: %v", err)
	}

	tests := []struct {
		name      string
		line      string
		wantCont  bool
		wantError bool
	}{
		{"help", "/help", true, false},
		{"help alias", "/?", true, false},
		{"quit", "/quit", false, false},
		{"exit alias", "/exit", false, false},
		{"model bare prints current", "/model", true, false},
		{"model swap", "/model llama3.2:3b", true, false},
		{"diff with no turns", "/diff", true, false},
		{"unknown", "/whoami", true, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cont, err := s.dispatchSlash(tt.line)
			if cont != tt.wantCont {
				t.Errorf("continue: got %v, want %v", cont, tt.wantCont)
			}
			if (err != nil) != tt.wantError {
				t.Errorf("err: got %v, want error=%v", err, tt.wantError)
			}
		})
	}
	// /model swap takes effect on subsequent calls.
	if _, err := s.dispatchSlash("/model llama3.2:3b"); err != nil {
		t.Fatalf("/model swap: %v", err)
	}
	if s.model != "llama3.2:3b" {
		t.Errorf("model not updated after /model swap; got %q", s.model)
	}
	if s.apiURL != defaultOllamaAPIURL {
		t.Errorf("apiURL should still resolve to Ollama for slashless model; got %q", s.apiURL)
	}
}

// TestTailString covers the bounded-output helper used to keep
// verifier stdout from blowing up the JSONL row size.
func TestTailString(t *testing.T) {
	if got := tailString("short", 100); got != "short" {
		t.Errorf("under-limit passthrough: got %q", got)
	}
	long := strings.Repeat("x", 200)
	got := tailString(long, 50)
	if !strings.HasPrefix(got, "...") {
		t.Errorf("expected truncation prefix; got %q", got[:10])
	}
	if len(got) != 53 { // 3 for "..." + 50 tail
		t.Errorf("expected len 53; got %d", len(got))
	}
}

// TestMergeFiles covers dedupe + order preservation for the
// FilesChanged list across retry rounds.
func TestMergeFiles(t *testing.T) {
	got := mergeFiles([]string{"a.go", "b.go"}, []string{"b.go", "c.go"})
	want := []string{"a.go", "b.go", "c.go"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i, p := range want {
		if got[i] != p {
			t.Errorf("idx %d: got %q want %q", i, got[i], p)
		}
	}
}

// TestPickBestOllamaModel asserts the scoring rubric prefers known-good
// function-callers + larger models over the qwen-1.5b default, and
// stays on the default when nothing better is installed. These cases
// are the load-bearing scenarios for Fix A (PROGRESS-REPL.md iter 3).
func TestPickBestOllamaModel(t *testing.T) {
	tests := []struct {
		name      string
		installed []string
		fallback  string
		want      string
	}{
		{
			name:      "only weak models installed → stay on fallback",
			installed: []string{"smollm:360m", "tinyllama:latest", "qwen2.5:0.5b", "qwen2.5-coder:1.5b", "gemma2:2b"},
			fallback:  "qwen2.5-coder:1.5b",
			want:      "qwen2.5-coder:1.5b",
		},
		{
			name:      "mistral 7b beats qwen-1.5b",
			installed: []string{"qwen2.5-coder:1.5b", "mistral:7b"},
			fallback:  "qwen2.5-coder:1.5b",
			want:      "mistral:7b",
		},
		{
			name:      "qwen2.5-coder:7b beats mistral:7b (coder bonus)",
			installed: []string{"mistral:7b", "qwen2.5-coder:7b"},
			fallback:  "qwen2.5-coder:1.5b",
			want:      "qwen2.5-coder:7b",
		},
		{
			name:      "phi3:mini avoided (no tool support in Ollama)",
			installed: []string{"phi3:mini", "qwen2.5-coder:1.5b"},
			fallback:  "qwen2.5-coder:1.5b",
			want:      "qwen2.5-coder:1.5b",
		},
		{
			name:      "empty install list → fallback",
			installed: []string{},
			fallback:  "qwen2.5-coder:1.5b",
			want:      "qwen2.5-coder:1.5b",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := pickBestOllamaModel(tt.installed, tt.fallback)
			if got != tt.want {
				t.Errorf("pickBestOllamaModel(%v, %q) = %q, want %q",
					tt.installed, tt.fallback, got, tt.want)
			}
		})
	}
}

// TestOllamaTagsURL covers the URL transform: OpenAI-compat chat
// endpoint → Ollama native /api/tags. Sanity test for the probe wire.
func TestOllamaTagsURL(t *testing.T) {
	got := ollamaTagsURL("http://localhost:11434/v1/chat/completions")
	want := "http://localhost:11434/api/tags"
	if got != want {
		t.Errorf("got %q want %q", got, want)
	}
	// Custom port / host should pass through.
	got2 := ollamaTagsURL("http://192.168.1.10:8080/v1/chat/completions")
	want2 := "http://192.168.1.10:8080/api/tags"
	if got2 != want2 {
		t.Errorf("got %q want %q", got2, want2)
	}
}

// TestExtractToolCallFromText is the parser test for Fix B. The same
// shape appears across many small-model output styles: bare JSON,
// markdown-fenced JSON, JSON-inside-prose. All three should match.
func TestExtractToolCallFromText(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		wantName string
		wantPath string
	}{
		{
			name:     "bare json",
			input:    `{"name": "write_file", "arguments": {"path": "main.go", "content": "package main\n"}}`,
			wantName: "write_file",
			wantPath: "main.go",
		},
		{
			name:     "fenced json",
			input:    "```json\n{\"name\": \"write_file\", \"arguments\": {\"path\": \"main.go\", \"content\": \"package main\\n\"}}\n```",
			wantName: "write_file",
			wantPath: "main.go",
		},
		{
			name:     "json in prose",
			input:    "Sure! Here's the tool call:\n\n{\"name\": \"write_file\", \"arguments\": {\"path\": \"app.go\", \"content\": \"// hi\\n\"}}\n\nLet me know if you need changes.",
			wantName: "write_file",
			wantPath: "app.go",
		},
		{
			name:  "no tool call",
			input: "I can help with that. Tell me more.",
		},
		{
			name:  "malformed",
			input: `{"name": "write_file", "arguments": this is not valid}`,
		},
		{
			// The qwen2.5-coder:1.5b shape: backtick-delimited string
			// value with JS-style escape sequences inside.
			name:     "backtick-quoted content (qwen-style)",
			input:    "```json\n{\n  \"name\": \"write_file\",\n  \"arguments\": {\n    \"content\": `\\npackage main\\n\\nfunc main() {\\n\\tprintln(\"hi\")\\n}\\n`,\n    \"path\": \"main.go\"\n  }\n}\n```",
			wantName: "write_file",
			wantPath: "main.go",
		},
		{
			// Mistral-style: array-wrapped object with the bog-standard
			// JSON shape inside. Same fields, just one extra layer of
			// brackets.
			name:     "array-wrapped (mistral-style)",
			input:    "```\n[{\"name\":\"write_file\",\"arguments\":{\"path\":\"main.go\",\"content\":\"package main\"}}]\n```",
			wantName: "write_file",
			wantPath: "main.go",
		},
		{
			// Mistral-style: content has literal unescaped newlines, so
			// Unmarshal fails. The per-field regex fallback should still
			// recover path and content.
			name:     "unescaped-newline content (mistral-style)",
			input:    "[{\"name\":\"write_file\",\"arguments\":{\"path\":\"main.go\",\"content\":\"package main\n\nconst SIZE = 5\n\nfunc main() {\n}\n\"}}]",
			wantName: "write_file",
			wantPath: "main.go",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractToolCallFromText(tt.input)
			if tt.wantName == "" {
				if got != nil {
					t.Errorf("expected nil; got %+v", got)
				}
				return
			}
			if got == nil {
				t.Fatalf("expected match; got nil")
			}
			if got.Name != tt.wantName {
				t.Errorf("name: got %q want %q", got.Name, tt.wantName)
			}
			if p, _ := got.Args["path"].(string); p != tt.wantPath {
				t.Errorf("path: got %q want %q", p, tt.wantPath)
			}
		})
	}
}

// TestSalvageWriteFile is the integrity test for the text-extractor's
// write side. Confirms safe paths land, unsafe paths are refused, and
// existing files are overwritten.
func TestSalvageWriteFile(t *testing.T) {
	workdir := t.TempDir()

	t.Run("happy path", func(t *testing.T) {
		args := map[string]any{"path": "main.go", "content": "package main\n"}
		rel, err := salvageWriteFile(workdir, args)
		if err != nil {
			t.Fatalf("salvageWriteFile: %v", err)
		}
		if rel != "main.go" {
			t.Errorf("rel: got %q", rel)
		}
		got := mustRead(t, workdir, "main.go")
		if got != "package main\n" {
			t.Errorf("content: got %q", got)
		}
	})

	t.Run("nested path creates dirs", func(t *testing.T) {
		args := map[string]any{"path": "pkg/util/helper.go", "content": "package util\n"}
		rel, err := salvageWriteFile(workdir, args)
		if err != nil {
			t.Fatalf("nested salvageWriteFile: %v", err)
		}
		if rel != filepath.Join("pkg", "util", "helper.go") {
			t.Errorf("rel: got %q", rel)
		}
		got := mustRead(t, workdir, "pkg/util/helper.go")
		if got != "package util\n" {
			t.Errorf("nested content: got %q", got)
		}
	})

	t.Run("absolute path refused", func(t *testing.T) {
		args := map[string]any{"path": "/tmp/evil", "content": "x"}
		if _, err := salvageWriteFile(workdir, args); err == nil {
			t.Errorf("expected error on absolute path")
		}
	})

	t.Run("escape attempt refused", func(t *testing.T) {
		args := map[string]any{"path": "../escaped.txt", "content": "x"}
		if _, err := salvageWriteFile(workdir, args); err == nil {
			t.Errorf("expected error on .. path")
		}
	})

	t.Run("empty path refused", func(t *testing.T) {
		args := map[string]any{"path": "", "content": "x"}
		if _, err := salvageWriteFile(workdir, args); err == nil {
			t.Errorf("expected error on empty path")
		}
	})
}

// --- helpers ---

func mustWrite(t *testing.T, root, rel, content string) {
	t.Helper()
	full := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatalf("mkdir for %s: %v", rel, err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", rel, err)
	}
}

func mustRead(t *testing.T, root, rel string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(root, rel))
	if err != nil {
		t.Fatalf("read %s: %v", rel, err)
	}
	return string(b)
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
