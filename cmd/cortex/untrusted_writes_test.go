package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dereksantos/cortex/internal/tools"
)

// TestTaintedWriteConfinement is the issue #102 write-side rule (the
// issue's "consider" item extended to the mutating tools): once untrusted
// web content entered the turn, write_file / edit_file / remove_path
// confine their paths to the workspace root — the same escape ordinary
// work is allowed to make (an absolute path outside the root, a `..` that
// leaves it) becomes a rejection in ConfinePath's error shape carrying the
// taint reason. Untainted turns keep today's behavior byte-for-byte: the
// escape lands. In-tree writes are unaffected in both states, and the
// post-edit hook path is unchanged (these sessions are untrusted, so the
// hook stays inert — what the tests pin is that the confined rejection
// happens BEFORE any filesystem touch and before the hook plumbing).
func TestTaintedWriteConfinement(t *testing.T) {
	root := t.TempDir()
	ws := mustWorkspace(t, root)
	// A target outside the workspace, sibling to root: untainted writes
	// land here, tainted ones must not create or touch anything. EvalSymlinks
	// because macOS hands t.TempDir() under a /var → /private/var symlink —
	// ConfinePath's real containment check (which the taint rule reuses
	// verbatim) resolves it, and the test's expectations must speak the
	// resolved paths too.
	realRoot := root
	if r, err := filepath.EvalSymlinks(root); err == nil {
		realRoot = r
	}
	outside := filepath.Join(filepath.Dir(realRoot), "outside-"+filepath.Base(realRoot))
	if err := os.MkdirAll(outside, 0o755); err != nil {
		t.Fatalf("mkdir outside: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(outside) })
	// An existing outside file for the edit_file / remove_path untainted
	// legs.
	existing := filepath.Join(outside, "existing.txt")
	if err := os.WriteFile(existing, []byte("original"), 0o644); err != nil {
		t.Fatalf("seed outside file: %v", err)
	}

	writeCall := func(path string) ToolCall {
		args, _ := json.Marshal(map[string]string{"path": path, "content": "payload"})
		return tc(tools.FunctionWriteFile, string(args))
	}
	editCall := func(path string) ToolCall {
		args, _ := json.Marshal(map[string]any{"path": path, "old_string": "original", "new_string": "tampered"})
		return tc(tools.FunctionEditFile, string(args))
	}
	removeCall := func(path string) ToolCall {
		args, _ := json.Marshal(map[string]string{"path": path})
		return tc(tools.FunctionRemove, string(args))
	}

	// Each case is run twice: tainted (fetch first) and untainted.
	type caseShape struct {
		name string
		call func(t *testing.T) ToolCall
		// wantBlocked asserts (tainted run) the observation is a rejection
		// with the confined shape + taint reason.
		wantBlocked bool
		// wantUntouched asserts neither run may create/modify/delete the
		// path (only used with wantBlocked).
		targetFile string // "" = nothing to stat
	}
	cases := []caseShape{
		{
			name:        "write_file absolute outside root",
			call:        func(*testing.T) ToolCall { return writeCall(filepath.Join(outside, "new.txt")) },
			wantBlocked: true,
			targetFile:  filepath.Join(outside, "new.txt"),
		},
		{
			name:        "write_file relative escape",
			call:        func(*testing.T) ToolCall { return writeCall("../" + filepath.Base(outside) + "/escape.txt") },
			wantBlocked: true,
			targetFile:  filepath.Join(outside, "escape.txt"),
		},
		{
			name:        "edit_file absolute outside root",
			call:        func(*testing.T) ToolCall { return editCall(existing) },
			wantBlocked: true,
			targetFile:  existing,
		},
		// remove_path's confinedPath already refused outside-root deletes
		// before the taint existed; the taint-time rejection simply fires
		// first, in ConfinePath's shape + the taint reason.
		{
			name:        "remove_path absolute outside root",
			call:        func(*testing.T) ToolCall { return removeCall(existing) },
			wantBlocked: true,
			targetFile:  existing,
		},
	}

	// runCase drives one tool call through Execute against a session that
	// is optionally tainted (a recorded fetch_url observation).
	runCase := func(t *testing.T, c caseShape, tainted bool) (string, error) {
		t.Helper()
		cs := &CortexSession{quiet: true, workspace: ws, turnNo: 1, allowDelete: true, deleteRoot: outside}
		if tainted {
			cs.recordUntrustedContent("fetch_url")
		}
		// Pin the CWD at the workspace root so relative escapes resolve the
		// same way they do in a REPL session.
		t.Chdir(root)
		out, _, err := tools.Execute(context.Background(), c.call(t), cs)
		return out, err
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			// Tainted: rejected in ConfinePath's shape with the taint reason,
			// and the target untouched.
			obs, err := runCase(t, c, true)
			got := obs
			if err != nil {
				got = err.Error()
			}
			if !c.wantBlocked {
				return
			}
			if !strings.Contains(got, "escapes the workspace") && !strings.Contains(got, "must be relative") {
				t.Errorf("tainted run: want ConfinePath's error shape, got %q", got)
			}
			if err == nil {
				t.Errorf("tainted run: a confined write reports its rejection as Execute's error, got only the observation %q", obs)
			}
			if !strings.Contains(got, "untrusted web content entered this turn") {
				t.Errorf("tainted run: rejection must carry the taint reason, got %q", got)
			}
			if c.targetFile != "" {
				info, statErr := os.Stat(c.targetFile)
				if c.targetFile == existing {
					// edit/remove targets: must still exist, unmodified.
					if statErr != nil {
						t.Fatalf("tainted run touched the target: %v", statErr)
					}
					body, _ := os.ReadFile(c.targetFile)
					if string(body) != "original" {
						t.Errorf("tainted run modified the target: %q", body)
					}
				} else if statErr == nil && !info.IsDir() {
					t.Errorf("tainted run created %s (rejected writes must touch nothing)", c.targetFile)
				}
			}
		})
	}

	// The untainted half: every escape above behaves exactly as before —
	// the write lands, the edit applies, the delete deletes.
	t.Run("untainted escape still lands (unchanged behavior)", func(t *testing.T) {
		// deleteRoot is configured wide here precisely to pin that the
		// UNTAINTED escape behavior is unchanged: remove_path's own
		// confinedPath would refuse this delete for any root narrower than
		// the target, so a wide deleteRoot isolates the taint rule.
		cs := &CortexSession{quiet: true, workspace: ws, turnNo: 1, allowDelete: true, deleteRoot: outside}
		t.Chdir(root)

		obs, _, err := tools.Execute(context.Background(), writeCall(filepath.Join(outside, "new.txt")), cs)
		if err != nil || !strings.Contains(obs, "wrote") {
			t.Fatalf("untainted absolute write outside the root must still land, got obs=%q err=%v", obs, err)
		}
		if body, rerr := os.ReadFile(filepath.Join(outside, "new.txt")); rerr != nil || string(body) != "payload" {
			t.Errorf("untainted write did not land: %q, %v", body, rerr)
		}
		obs, _, err = tools.Execute(context.Background(), editCall(existing), cs)
		if err != nil || !strings.Contains(obs, "edited") {
			t.Fatalf("untainted edit outside the root must still apply, got obs=%q err=%v", obs, err)
		}
		obs, _, err = tools.Execute(context.Background(), removeCall(existing), cs)
		if err != nil || !strings.Contains(obs, "removed") {
			t.Fatalf("untainted remove outside the root must still delete, got obs=%q err=%v", obs, err)
		}
		if _, statErr := os.Stat(existing); !os.IsNotExist(statErr) {
			t.Errorf("untainted remove left the file in place")
		}
	})

	t.Run("in-tree writes are unaffected in both states", func(t *testing.T) {
		for _, tainted := range []bool{false, true} {
			cs := &CortexSession{quiet: true, workspace: ws, turnNo: 2, allowDelete: true, deleteRoot: outside}
			if tainted {
				cs.recordUntrustedContent("web_search")
			}
			t.Chdir(root)
			// A relative in-tree path, mkdir'ed first: write_file creates
			// files, not directories.
			if err := os.MkdirAll(filepath.Join(root, "intree"), 0o755); err != nil {
				t.Fatal(err)
			}
			inTree := "intree/f.txt"
			obs, _, err := tools.Execute(context.Background(), writeCall(inTree), cs)
			if err != nil || !strings.Contains(obs, "wrote") {
				t.Errorf("tainted=%v: in-tree write must land, got obs=%q err=%v", tainted, obs, err)
			}
		}
	})

	t.Run("confinement resets with the turn", func(t *testing.T) {
		// Turn 1 tainted: the escape is REJECTED by the tool's own in-tool
		// check, which reports it as Execute's error (the confinement happens
		// before any filesystem touch, not as a validator observation). After
		// the turn-start reset (a new turnNo + cs.taint nil, as turn.go does)
		// the same escape lands again.
		sink := filepath.Join(outside, "reset.txt")
		cs := &CortexSession{quiet: true, workspace: ws, turnNo: 1, allowDelete: true, deleteRoot: outside}
		cs.recordUntrustedContent("fetch_url")
		t.Chdir(root)
		obs, _, err := tools.Execute(context.Background(), writeCall(sink), cs)
		if !strings.Contains(err.Error(), "untrusted web content entered this turn") {
			t.Fatalf("tainted turn must reject the escape, got obs=%q err=%v", obs, err)
		}
		cs.turnNo = 2
		cs.taint = nil // turn.go's turn-start reset
		obs, _, err = tools.Execute(context.Background(), writeCall(sink), cs)
		if err != nil || !strings.Contains(obs, "wrote") {
			t.Errorf("after the turn reset the escape must behave as ordinary work again: obs=%q err=%v", obs, err)
		}
	})

	t.Run("the in-tool check covers a call driven through the session dispatcher", func(t *testing.T) {
		// The confinement lives in the tools, not in ValidateToolCall (the
		// validator-level copy was removed as a duplicate). This drives a write
		// the way a SUBAGENT's call lands — through the dispatcher with the
		// tainted session as the child's ToolDeps, which is exactly what
		// RunSubagent does — and asserts the in-tool check alone refuses it.
		// The subagent-depth context value must not change the outcome.
		sink := filepath.Join(outside, "subagent.txt")
		cs := &CortexSession{quiet: true, workspace: ws, turnNo: 1, allowDelete: true, deleteRoot: outside}
		cs.recordUntrustedContent("fetch_url")
		t.Chdir(root)
		childCtx := withSubagentDepth(context.Background(), 1)
		obs, _, err := tools.Execute(childCtx, writeCall(sink), cs)
		if !strings.Contains(err.Error(), "untrusted web content entered this turn") {
			t.Errorf("a subagent-driven confined write must be refused by the in-tool check, got obs=%q err=%v", obs, err)
		}
		if _, statErr := os.Stat(sink); statErr == nil {
			t.Errorf("the refused subagent write created %s", sink)
		}
	})
}
