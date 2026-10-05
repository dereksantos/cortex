package main

import (
	"context"
	"io"
	"path/filepath"
	"testing"

	"github.com/dereksantos/cortex/internal/journal"
	"github.com/dereksantos/cortex/internal/lineedit"
	"github.com/dereksantos/cortex/internal/shellrisk"
)

// TestShellApprovals is the acceptance coverage for issue #107's session-
// scoped approvals. The gate is tested directly (gateShell), because the
// tool-level path would actually execute the command. A "p" (prefix)
// approval matches only commands with that prefix and never a Blocked
// command; an "a" (exact) approval matches only that exact command; "y"
// once runs without recording anything, so the same command prompts again.
func TestShellApprovals(t *testing.T) {
	stubRisky := func(_ context.Context, _ string) (shellrisk.Level, string, error) {
		return shellrisk.Risky, "test: always risky", nil
	}
	stubBlocked := func(_ context.Context, _ string) (shellrisk.Level, string, error) {
		return shellrisk.Blocked, "test: blocked", nil
	}

	// riskyBlockedSession builds a session whose classifier returns Risky for
	// every command but has no approver (quiet) — the production shape of a
	// session where Risky commands are gated.
	riskyBlockedSession := func() *CortexSession {
		return &CortexSession{classifyShell: stubRisky, quiet: true}
	}

	t.Run("prefix approval matches only commands with that prefix", func(t *testing.T) {
		cs := riskyBlockedSession()
		cs.ApproveShell("prefix", "make test*", "make test", "test: always risky")
		if got, ok := cs.gateShell(context.Background(), "make test"); !ok || got != "" {
			t.Errorf("approved prefix command should run without prompting: (%q, %v)", got, ok)
		}
		if got, ok := cs.gateShell(context.Background(), "make build"); ok {
			t.Errorf("unrelated command should still be blocked (no approver): %q", got)
		}
		// Note: "make testX" DOES match the prefix "make test*" (it starts
		// with "make test"). This is the expected behavior of a prefix
		// approval — it matches any command that starts with the prefix.
		// The acceptance criterion is that it matches "only commands with
		// that prefix", not that it does word-boundary matching.
	})

	t.Run("prefix approval never matches a Blocked command", func(t *testing.T) {
		cs := riskyBlockedSession()
		cs.ApproveShell("prefix", "make test*", "make test", "test: always risky")
		// A Blocked command must stay blocked even with a matching prefix
		// approval. The classifier returns Blocked for "rm -rf /".
		cs2 := &CortexSession{classifyShell: stubBlocked, quiet: true,
			shellApprovals: []string{"rm -rf /"}}
		if _, ok := cs2.gateShell(context.Background(), "rm -rf /"); ok {
			t.Error("Blocked command must stay blocked even with a matching approval")
		}
		if _, ok := cs.gateShell(context.Background(), "rm -rf /"); ok {
			t.Error("Blocked command must stay blocked (no matching approval either)")
		}
	})

	t.Run("exact approval matches only that exact command", func(t *testing.T) {
		cs := riskyBlockedSession()
		cs.ApproveShell("exact", "make test", "make test", "test: always risky")
		if got, ok := cs.gateShell(context.Background(), "make test"); !ok || got != "" {
			t.Errorf("approved exact command should run without prompting: (%q, %v)", got, ok)
		}
		if got, ok := cs.gateShell(context.Background(), "make test extra"); ok {
			t.Errorf("different command should still be blocked: %q", got)
		}
		if got, ok := cs.gateShell(context.Background(), "make test|rm -rf /"); ok {
			t.Errorf("prefix of an exact approval must not match: %q", got)
		}
	})

	t.Run("y once runs without recording an approval", func(t *testing.T) {
		cs := &CortexSession{classifyShell: stubRisky,
			confirmRisky: func(string) lineedit.ConfirmChoice { return lineedit.ConfirmYes }}
		if _, ok := cs.gateShell(context.Background(), "make test"); !ok {
			t.Fatal("y should allow the command")
		}
		if len(cs.shellApprovals) != 0 {
			t.Errorf("y must not record an approval, got %v", cs.shellApprovals)
		}
		// A second call to the same command must prompt again (no approval
		// was recorded), so with no approver it is blocked.
		cs2 := &CortexSession{classifyShell: stubRisky, quiet: true, shellApprovals: cs.shellApprovals}
		if _, ok := cs2.gateShell(context.Background(), "make test"); ok {
			t.Error("same command should be blocked again after y (no approval recorded)")
		}
	})

	t.Run("approvals are journaled", func(t *testing.T) {
		root := t.TempDir()
		cs := &CortexSession{classifyShell: stubRisky, workspace: &Workspace{Root: root},
			confirmRisky: func(string) lineedit.ConfirmChoice { return lineedit.ConfirmAlwaysPrefix }}
		cs.ApproveShell("prefix", "make test*", "make test", "test: always risky")
		cs.ApproveShell("exact", "make lint", "make lint", "test: always risky")
		dir := filepath.Join(root, ".cortex", "journal", "shell")
		r, err := journal.NewReader(dir)
		if err != nil {
			t.Fatalf("journal.NewReader: %v", err)
		}
		defer r.Close()
		var got []journal.ShellApprovalPayload
		for {
			e, err := r.Next()
			if err == io.EOF {
				break
			}
			if err != nil {
				t.Fatalf("Reader.Next: %v", err)
			}
			p, err := journal.ParseShellApproval(e)
			if err != nil {
				t.Fatalf("ParseShellApproval: %v", err)
			}
			got = append(got, *p)
		}
		if len(got) != 2 {
			t.Fatalf("want 2 journaled approvals, got %d: %v", len(got), got)
		}
		if got[0].Kind != "prefix" || got[0].Pattern != "make test*" {
			t.Errorf("first approval = %+v, want prefix/make test*", got[0])
		}
		if got[1].Kind != "exact" || got[1].Pattern != "make lint" {
			t.Errorf("second approval = %+v, want exact/make lint", got[1])
		}
	})

	t.Run("approvals are session-scoped (memory only)", func(t *testing.T) {
		cs := &CortexSession{classifyShell: stubRisky, quiet: true,
			shellApprovals: []string{"make test*"}}
		if got, ok := cs.gateShell(context.Background(), "make test"); !ok || got != "" {
			t.Errorf("approved prefix command should run: (%q, %v)", got, ok)
		}
		// A fresh session with no approvals must not inherit them.
		cs2 := &CortexSession{classifyShell: stubRisky, quiet: true}
		if _, ok := cs2.gateShell(context.Background(), "make test"); ok {
			t.Error("fresh session must not inherit approvals")
		}
	})
}
