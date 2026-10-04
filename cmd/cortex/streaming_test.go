// streaming_test.go — the turn-boundary receipt printRedactions (issue
// #103): it must be a no-op at a zero count, and at a positive count write
// the notice to the sink the caller passes — the REPL passes stdout, the
// headless `cortex turn` driver passes stderr (issue #118's answer-only
// stdout contract).
package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestPrintRedactions(t *testing.T) {
	cases := []struct {
		name   string
		n      int
		want   string
		wantOK bool
	}{
		{name: "n=0 writes nothing", n: 0, want: ""},
		{name: "n=2 writes the notice", n: 2, want: "2 secret pattern(s) redacted from the transcript, journal, and memory this turn", wantOK: true},
		{name: "n=1 writes the notice", n: 1, want: "1 secret pattern(s) redacted from the transcript, journal, and memory this turn", wantOK: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			printRedactions(&buf, tc.n)
			got := buf.String()
			if tc.wantOK {
				// withColor may wrap the text in ANSI codes depending on
				// the terminal; check the core text is present and the count
				// leads the line.
				if !strings.Contains(got, tc.want) {
					t.Errorf("printRedactions(buf, %d) = %q, want it to contain %q", tc.n, got, tc.want)
				}
			} else if got != "" {
				t.Errorf("printRedactions(buf, 0) = %q, want empty (a zero count is a no-op)", got)
			}
		})
	}
}
