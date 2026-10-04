package lineedit

import (
	"bytes"
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
// opens the editor) has Tab decoded as a key and then dropped by the driver's
// `if completion == nil { continue }` guard. The line comes back UNCHANGED:
// whatever bytes were typed — including a tab — pass through as typed;
// nothing is completed, cycled, or inserted.
func TestTabIsInertWithoutCompletion(t *testing.T) {
	line, err := readLineWithNoTTY(t, "/he\tlp\r", "", nil)
	if err != nil {
		t.Fatalf("readLineWith: %v", err)
	}
	if line != "/he\tlp" {
		t.Errorf("line = %q, want %q (no completer wired: Tab inserts nothing)", line, "/he\tlp")
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
