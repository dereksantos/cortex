package main

import (
	"context"
	"strings"
	"testing"

	"github.com/dereksantos/cortex/internal/tools"
)

// TestSessionHookOffSilencesInactiveNote pins the session path (the REPL's
// /hook command → CortexSession.SetHookMode, then a write_file through the
// session's own ToolDeps and its FormatHook wrapper): on an UNTRUSTED
// workspace the one-time "hook inactive" note appears on the first write
// only when the hook mode is on — after /hook off the result is silent,
// because the note would tell the operator how to enable a hook they just
// turned off.
func TestSessionHookOffSilencesInactiveNote(t *testing.T) {
	for _, tc := range []struct {
		name     string
		hookOff  bool
		wantNote bool
	}{
		{name: "hook on: note on first write", hookOff: false, wantNote: true},
		{name: "/hook off: no note", hookOff: true, wantNote: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			t.Chdir(dir)
			cs := receiptSession(t, dir, false, false) // untrusted
			if tc.hookOff {
				cs.SetHookMode(tools.HookModeOff)
			}
			out, _, err := tools.Execute(context.Background(), writeFileCall("w1", "main.go", "package main\n"), cs)
			if err != nil {
				t.Fatalf("write_file: %v", err)
			}
			if got := strings.Contains(out, tools.HookInactiveNote); got != tc.wantNote {
				t.Errorf("inactive note present = %v, want %v; result %q", got, tc.wantNote, out)
			}
		})
	}
}
