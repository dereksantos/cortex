package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// stateHookCmdDeps is hookCmdDeps (a workdir-anchored, shell-permitting
// deps with project commands and a trust verdict) plus the session's
// post-edit hook state, so the bash in-place-rewrite path has a session to
// announce the "hook inactive" note for.
type stateHookCmdDeps struct {
	hookCmdDeps
	state *PostEditHookState
}

func (d stateHookCmdDeps) HookState() *PostEditHookState { return d.state }

// TestPostEditHookInactiveNoteRespectsMode pins that the once-per-session
// "hook inactive" note on an UNTRUSTED workspace is only surfaced when the
// hook would otherwise run: with the effective mode off — the configured
// ceiling (tools.post_edit_hook / CORTEX_POST_EDIT_HOOK), a session /hook
// off, or the per-call `hook: "skip"` — the result is silent, nothing runs,
// and the session's one note slot is NOT consumed. Driven through the real
// write_file path (Execute).
func TestPostEditHookInactiveNoteRespectsMode(t *testing.T) {
	cases := []struct {
		name        string
		ceiling     HookMode
		sessionMode HookMode // applied with SetMode (the /hook command's setter)
		skip        bool     // the per-call `hook: "skip"`
		trusted     bool
		wantNote    bool
		wantRan     int
	}{
		{name: "untrusted, ceiling off", ceiling: HookModeOff, sessionMode: HookModeAll},
		{name: "untrusted, session /hook off", ceiling: HookModeAll, sessionMode: HookModeOff},
		{name: "untrusted, per-call skip", ceiling: HookModeAll, sessionMode: HookModeAll, skip: true},
		{name: "untrusted, format", ceiling: HookModeFormat, sessionMode: HookModeAll, wantNote: true},
		{name: "untrusted, all", ceiling: HookModeAll, sessionMode: HookModeAll, wantNote: true},
		{name: "trusted, off", ceiling: HookModeOff, sessionMode: HookModeAll, trusted: true},
		{name: "trusted, all", ceiling: HookModeAll, sessionMode: HookModeAll, trusted: true, wantRan: 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			withHookCeiling(t, tc.ceiling)
			rec := &recordingHookRunner{}
			installHookRunner(t, rec)
			root := t.TempDir()
			t.Chdir(root)

			state := &PostEditHookState{}
			state.SetMode(tc.sessionMode)
			deps := hookDeps{cmds: hookModeTestCmds(), trusted: tc.trusted, state: state}

			args := map[string]any{"path": "a.go", "content": "package main\n"}
			if tc.skip {
				args["hook"] = "skip"
			}
			out, _, err := Execute(context.Background(), callArgs(t, FunctionWriteFile, args), deps)
			if err != nil {
				t.Fatalf("write_file: %v", err)
			}
			if got := strings.Contains(out, HookInactiveNote); got != tc.wantNote {
				t.Errorf("inactive note present = %v, want %v; result %q", got, tc.wantNote, out)
			}
			if len(rec.ran) != tc.wantRan {
				t.Errorf("commands ran = %v, want %d", rec.ran, tc.wantRan)
			}
			// The slot is consumed only when the note was actually emitted.
			if due := state.inactiveNoteDue(); due == tc.wantNote {
				t.Errorf("inactiveNoteDue = %v after the call, want %v (consumed iff the note was emitted)", due, !tc.wantNote)
			}
		})
	}
}

// TestPostEditHookInactiveNoteSurvivesSkippedCall pins the reachable
// "later call has a higher effective mode" case: the per-call `hook:
// "skip"` lowers ONE call to off, so an untrusted session's skipped first
// write is silent and leaves the slot unconsumed — the next unskipped edit
// (effective mode back at the session's mode) announces the note once.
func TestPostEditHookInactiveNoteSurvivesSkippedCall(t *testing.T) {
	withHookCeiling(t, HookModeAll)
	rec := &recordingHookRunner{}
	installHookRunner(t, rec)
	root := t.TempDir()
	t.Chdir(root)
	deps := hookDeps{cmds: hookModeTestCmds(), state: &PostEditHookState{}} // untrusted

	out1, _, err := Execute(context.Background(), callArgs(t, FunctionWriteFile, map[string]any{
		"path": "a.go", "content": "package main\n", "hook": "skip",
	}), deps)
	if err != nil {
		t.Fatalf("write_file: %v", err)
	}
	if strings.Contains(out1, HookInactiveNote) {
		t.Errorf("a skipped call must not carry the inactive note, got %q", out1)
	}

	out2, _, err := Execute(context.Background(), callArgs(t, FunctionEditFile, map[string]any{
		"path": "a.go", "old_string": "package main", "new_string": "package main // edited",
	}), deps)
	if err != nil {
		t.Fatalf("edit_file: %v", err)
	}
	if !strings.Contains(out2, HookInactiveNote) {
		t.Errorf("the first unskipped edit after a skip must carry the inactive note, got %q", out2)
	}

	out3, _, err := Execute(context.Background(), callArgs(t, FunctionEditFile, map[string]any{
		"path": "a.go", "old_string": "// edited", "new_string": "// edited twice",
	}), deps)
	if err != nil {
		t.Fatalf("edit_file: %v", err)
	}
	if strings.Contains(out3, HookInactiveNote) {
		t.Errorf("the inactive note must not repeat, got %q", out3)
	}
	if len(rec.ran) != 0 {
		t.Errorf("an untrusted workspace must run nothing, ran %v", rec.ran)
	}
}

// TestBashInPlaceRewriteInactiveNoteRespectsMode pins the same rule on the
// bash in-place-rewrite path (which runs the same post-edit hook): on an
// untrusted workspace with the hook mode off, a `sed -i` on a workdir file
// carries no "hook inactive" note; with the mode on, the note appears once.
func TestBashInPlaceRewriteInactiveNoteRespectsMode(t *testing.T) {
	for _, tc := range []struct {
		name     string
		ceiling  HookMode
		wantNote bool
	}{
		{"mode off: silent", HookModeOff, false},
		{"mode all: note", HookModeAll, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			withHookCeiling(t, tc.ceiling)
			rec := &recordingHookRunner{}
			installHookRunner(t, rec)
			wd := t.TempDir()
			t.Chdir(wd)
			if err := os.WriteFile(filepath.Join(wd, "main.go"), []byte("package main\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			state := &PostEditHookState{}
			deps := stateHookCmdDeps{hookCmdDeps{wdNoteDeps{wdDeps{wd: wd}}, goFmtCmds(), false}, state}
			got, _, err := Execute(context.Background(), bashCall(t, `sed -i.bak 's/x/y/' main.go`), deps)
			if err != nil {
				t.Fatalf("bash: %v", err)
			}
			if !strings.Contains(got, "rewrites file(s) in place") {
				t.Fatalf("result should carry the rewrite steering note (the hook path was reached), got %q", got)
			}
			if has := strings.Contains(got, HookInactiveNote); has != tc.wantNote {
				t.Errorf("inactive note present = %v, want %v; result %q", has, tc.wantNote, got)
			}
			if due := state.inactiveNoteDue(); due == tc.wantNote {
				t.Errorf("inactiveNoteDue = %v, want %v", due, !tc.wantNote)
			}
			if len(rec.ran) != 0 {
				t.Errorf("an untrusted workspace must run nothing, ran %v", rec.ran)
			}
		})
	}
}
