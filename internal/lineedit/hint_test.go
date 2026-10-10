package lineedit

import (
	"bytes"
	"strings"
	"testing"
)

// driveEditor runs the real line-reading loop over data and returns the line
// and every byte the editor wrote.
func driveEditor(t *testing.T, data string) (string, string) {
	t.Helper()
	var out bytes.Buffer
	term := &Terminal{out: &out}
	term.SetCompletion(map[string]Completer{"slash": SlashCompleter{
		Commands: []string{"/compact", "/context", "/help"},
		Help:     map[string]string{"/compact": "distill the session", "/context": "open the context map", "/help": "show this list"},
	}})
	line, err := term.readLineWith("> ", "", &sliceSource{data: []byte(data)}, nil)
	if err != nil {
		t.Fatalf("readLineWith: %v", err)
	}
	return line, out.String()
}

func TestGhostHintPreviewsTheOnlyMatch(t *testing.T) {
	tests := []struct {
		name, typed string
		wantGhost   string // "" = no preview on the final redraw
	}{
		{"two matches: no guess", "/co", ""},
		{"one match: remainder and what it does", "/comp", "act   distill the session"},
		{"complete command: description only", "/help", "   show this list"},
		{"prose: never", "hello", ""},
		{"argument started: never", "/model x", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, out := driveEditor(t, tt.typed+"\r")
			// The last redraw is the state after the final typed character (Enter
			// itself doesn't redraw: no accepted-line rewrite is wired here).
			frames := strings.Split(out, "\r\033[K")
			before := stripANSI(frames[len(frames)-1])
			if tt.wantGhost == "" {
				if got := strings.TrimRight(before, " \r\n"); got != "> "+tt.typed {
					t.Errorf("unexpected preview: frame %q, want just %q", got, "> "+tt.typed)
				}
				return
			}
			if !strings.Contains(before, "> "+tt.typed+tt.wantGhost) {
				t.Errorf("preview missing: got frame %q, want %q", before, "> "+tt.typed+tt.wantGhost)
			}
		})
	}
}

func TestTabRowDescribesTheFocusedCommand(t *testing.T) {
	line, out := driveEditor(t, "/comp\t\r")
	if line != "/compact" {
		t.Fatalf("line = %q, want /compact", line)
	}
	if !strings.Contains(stripANSI(out), "/compact  distill the session") {
		t.Errorf("candidate row should describe the single match: %q", stripANSI(out))
	}
}

func TestCandidateRowIsClearedOnTheNextKey(t *testing.T) {
	_, out := driveEditor(t, "/co\tn\r")
	i := strings.Index(out, "\0337") // the row was drawn (cursor saved around it)
	if i < 0 {
		t.Fatalf("no candidate row drawn: %q", out)
	}
	if !strings.Contains(out[i:], "\r\033[J") {
		t.Errorf("the next redraw must clear the row below the input: %q", out[i:])
	}
}
