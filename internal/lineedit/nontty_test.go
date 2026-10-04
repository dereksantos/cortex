package lineedit

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

// readLineWithNoTTY drives the real ReadLinePrefilled driver (readLineWith —
// the same loop the REPL runs) against an in-memory byte source: no TTY is
// needed (the fd is only consulted for width and signal restore, neither of
// which matters here), so the key decoding and buffer loop execute exactly as
// they would in the REPL.
func readLineWithNoTTY(t *testing.T, data string, prefill string, completers map[string]Completer) (string, error) {
	t.Helper()
	var out bytes.Buffer
	term := &Terminal{out: &out}
	if completers != nil {
		term.SetCompletion(completers)
	}
	return term.readLineWith("> ", prefill, &sliceSource{data: []byte(data)})
}

// TestTabIsInertWithoutCompletion wires the non-TTY invariant into the driver
// itself: a Terminal that never had SetCompletion called (completers == nil —
// the state of every non-TTY session, which reads with bufio.Scanner and never
// opens the editor) has no completion state at all, so a typed Tab is
// inserted as a literal character and the line passes through as typed —
// nothing is completed or cycled, and the tab is not dropped.
func TestTabIsInertWithoutCompletion(t *testing.T) {
	line, err := readLineWithNoTTY(t, "/he\tlp\r", "", nil)
	if err != nil {
		t.Fatalf("readLineWith: %v", err)
	}
	if line != "/he\tlp" {
		t.Errorf("line = %q, want %q (no completer wired: Tab passes through as a literal character)", line, "/he\tlp")
	}
}

// TestTabCompletesWhenWired is the same path with a completer set: the first
// Tab fills the common prefix in place, keeping the typed prefix — the
// behavior the inert test above says is absent without completion.
func TestTabCompletesWhenWired(t *testing.T) {
	line, err := readLineWithNoTTY(t, "/he\t\r", "", map[string]Completer{
		"slash": SlashCompleter{Commands: []string{"/help", "/hook"}},
	})
	if err != nil {
		t.Fatalf("readLineWith: %v", err)
	}
	if line != "/help" {
		t.Errorf("line = %q, want \"/help\" (common-prefix fill on first Tab)", line)
	}
}

// TestTabFillsSinglePathCandidateEndToEnd pins the @path completion end to
// end: typing "@src/al" and pressing a single Tab fills the single matching
// candidate IN PLACE, keeping the "@" marker — the buffer holds a real
// mention the user can submit. Without the marker on candidates the first
// Tab never fills (the candidate is not an extension of the typed word) and
// the cycle Tabs drop the "@", silently un-mentioning the line.
func TestTabFillsSinglePathCandidateEndToEnd(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "src"), 0o755); err != nil {
		t.Fatalf("mkdir src: %v", err)
	}
	for _, f := range []string{"src/alpha.go", "src/internal.go", "main.go"} {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(root, f)), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", f, err)
		}
		if err := os.WriteFile(filepath.Join(root, f), []byte("package main\n"), 0o644); err != nil {
			t.Fatalf("write %s: %v", f, err)
		}
	}
	line, err := readLineWithNoTTY(t, "@src/al\t\r", "", map[string]Completer{
		"path": PathCompleter{Root: root, MaxCandidates: 50},
	})
	if err != nil {
		t.Fatalf("readLineWith: %v", err)
	}
	if line != "@src/alpha.go" {
		t.Errorf("line = %q, want \"@src/alpha.go\" (single-match fill keeps the @ marker)", line)
	}
}
