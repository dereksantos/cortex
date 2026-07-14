package main

import "testing"

// Workdir anchors tool execution ONLY for explicit-root workspaces (the
// --project / serve / loop-firing population): a CWD-derived workspace or a
// hand-constructed session must return "" so REPL tool behavior is
// byte-identical to the pre-workdir code.
func TestWorkdirOnlyForExplicitWorkspace(t *testing.T) {
	t.Run("explicit workspace anchors", func(t *testing.T) {
		root := t.TempDir()
		ws, err := NewWorkspace(root)
		if err != nil {
			t.Fatalf("NewWorkspace: %v", err)
		}
		cs := &CortexSession{workspace: ws}
		if got := cs.Workdir(); got != ws.Root {
			t.Errorf("explicit workspace should anchor to its root, got %q want %q", got, ws.Root)
		}
	})

	t.Run("cwd-derived workspace does not anchor", func(t *testing.T) {
		t.Chdir(t.TempDir())
		cs := &CortexSession{workspace: WorkspaceFromCWD()}
		if got := cs.Workdir(); got != "" {
			t.Errorf("CWD-derived workspace must not anchor (REPL stays CWD-relative), got %q", got)
		}
	})

	t.Run("nil workspace does not anchor", func(t *testing.T) {
		cs := &CortexSession{}
		if got := cs.Workdir(); got != "" {
			t.Errorf("nil workspace must not anchor, got %q", got)
		}
	})
}
