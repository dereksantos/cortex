package main

import (
	"context"
	"io"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dereksantos/cortex/internal/journal"
	"github.com/dereksantos/cortex/internal/lineedit"
	"github.com/dereksantos/cortex/internal/shellrisk"
)

// TestShellApprovals is the acceptance coverage for issue #107's session-
// scoped approvals. The gate is tested directly (gateShell), because the
// tool-level path would actually execute the command. A "p" (prefix)
// approval matches only commands with that prefix and never a Blocked
// command; an "a" (exact) approval matches only that exact command — even
// when it ends in "*"; "y" once runs without recording anything, so the
// same command prompts again.
func TestShellApprovals(t *testing.T) {
	stubRisky := func(_ context.Context, _, _ string) (shellrisk.Level, string, error) {
		return shellrisk.Risky, "test: always risky", nil
	}
	stubBlocked := func(_ context.Context, _, _ string) (shellrisk.Level, string, error) {
		return shellrisk.Blocked, "test: blocked", nil
	}

	// riskyBlockedSession builds a session whose classifier returns Risky for
	// every command but has no approver (quiet) — the production shape of a
	// session where Risky commands are gated. The workspace points at a temp
	// dir so ApproveShell journals into the test's own tree, never the repo's
	// working directory (journalShellApproval skips journaling without a
	// workspace).
	riskyBlockedSession := func() *CortexSession {
		return &CortexSession{classifyShell: stubRisky, quiet: true,
			workspace: &Workspace{Root: t.TempDir()}}
	}

	t.Run("prefix approval matches only commands with that prefix", func(t *testing.T) {
		cs := riskyBlockedSession()
		cs.ApproveShell("prefix", "make test*", "make test", "test: always risky")
		if got, ok := cs.gateShell(context.Background(), "make test"); !ok || got != "" {
			t.Errorf("approved prefix command should run without prompting: (%q, %v)", got, ok)
		}
		if got, ok := cs.gateShell(context.Background(), "make test -v"); !ok || got != "" {
			t.Errorf("prefix + whitespace + further args must match the approval: (%q, %v)", got, ok)
		}
		if got, ok := cs.gateShell(context.Background(), "make build"); ok {
			t.Errorf("unrelated command should still be blocked (no approver): %q", got)
		}
		// The prefix is a WORD boundary, not a byte boundary: a longer word
		// glued onto the prefix ("make testX") is a different command, and
		// nothing may chain, pipe, redirect, or substitute after it — the
		// whole point of issue #107's approval is that the user approved
		// "make test", not "make test" + arbitrary shell.
		for _, cmd := range []string{"make testX", "make test; rm -rf x", "make test && echo hi", "make test|cat"} {
			if _, ok := cs.gateShell(context.Background(), cmd); ok {
				t.Errorf("prefix approval must not match %q (no approver): it ran without a prompt", cmd)
			}
		}
	})

	t.Run("prefix approval never matches a Blocked command", func(t *testing.T) {
		cs := riskyBlockedSession()
		cs.ApproveShell("prefix", "make test*", "make test", "test: always risky")
		// A Blocked command must stay blocked even with a matching prefix
		// approval. The classifier returns Blocked for "rm -rf /".
		cs2 := &CortexSession{classifyShell: stubBlocked, quiet: true,
			shellApprovals: []shellApproval{{Kind: "exact", Pattern: "rm -rf /"}}}
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

	t.Run("exact approval of a star-terminated command stays exact", func(t *testing.T) {
		// The kind rides with the pattern: an EXACT approval of a command
		// that ends in "*" (the shell glob, expanded by the shell before
		// cortex ever sees it) must compare byte for byte, not be re-read as
		// a prefix. Without the kind, "rm -f build/*" would be stored as the
		// word prefix "rm -f build/" and silently auto-allow
		// "rm -f build/ ../other".
		cs := riskyBlockedSession()
		cs.ApproveShell("exact", "rm -f build/*", "rm -f build/*", "test: always risky")
		if got, ok := cs.gateShell(context.Background(), "rm -f build/*"); !ok || got != "" {
			t.Errorf("exact approval of a * command must match that command: (%q, %v)", got, ok)
		}
		if _, ok := cs.gateShell(context.Background(), "rm -f build/ x"); ok {
			t.Error("exact * approval must not act as a word prefix (ran without a prompt)")
		}
	})

	t.Run("a stored approval does not run on a tainted turn", func(t *testing.T) {
		// Issue #102: once untrusted content entered the turn, every Risky
		// command needs an explicit answer for the rest of it — a stored
		// "always" (#107) must not carry injected text past that bar.
		cs := riskyBlockedSession()
		cs.turnNo = 1
		cs.ApproveShell("prefix", "make test*", "make test", "test: always risky")
		cs.recordUntrustedContent("fetch_url")
		if got, ok := cs.gateShell(context.Background(), "make test"); ok {
			t.Errorf("tainted turn: a stored approval must not auto-run (no approver), got (%q, %v)", got, ok)
		}
		asked := false
		cs.quiet = false
		cs.confirmRisky = func(string) lineedit.ConfirmChoice { asked = true; return lineedit.ConfirmYes }
		if _, ok := cs.gateShell(context.Background(), "make test"); !ok || !asked {
			t.Errorf("tainted turn: the human must be asked and a yes must run (asked=%v ran=%v)", asked, ok)
		}
	})

	t.Run("y once runs without recording an approval", func(t *testing.T) {
		cs := &CortexSession{classifyShell: stubRisky,
			workspace:    &Workspace{Root: t.TempDir()},
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

	t.Run("p stores exactly the prefix shown in the prompt", func(t *testing.T) {
		cs := &CortexSession{classifyShell: stubRisky,
			workspace: &Workspace{Root: t.TempDir()},
			confirmRisky: func(q string) lineedit.ConfirmChoice {
				want := commandPrefixPattern("make test ./pkg/...")
				if !strings.Contains(q, `p always "`+want+`"`) {
					t.Errorf("prompt must show the derived prefix %q: %q", want, q)
				}
				return lineedit.ConfirmAlwaysPrefix
			}}
		if _, ok := cs.gateShell(context.Background(), "make test ./pkg/..."); !ok {
			t.Fatal("p should allow the command")
		}
		if len(cs.shellApprovals) != 1 ||
			cs.shellApprovals[0].Kind != "prefix" ||
			cs.shellApprovals[0].Pattern != commandPrefixPattern("make test ./pkg/...") {
			t.Fatalf("p must store the shown prefix as kind=prefix, got %+v", cs.shellApprovals)
		}
	})

	t.Run("p for a single-token command covers the command itself", func(t *testing.T) {
		cs := &CortexSession{classifyShell: stubRisky, quiet: true}
		cs.ApproveShell("prefix", commandPrefixPattern("git"), "git", "test: always risky")
		if got, ok := cs.gateShell(context.Background(), "git"); !ok || got != "" {
			t.Errorf("a p approval of bare git must cover git itself: (%q, %v)", got, ok)
		}
		if got, ok := cs.gateShell(context.Background(), "git status"); !ok || got != "" {
			t.Errorf("a p approval of bare git must cover git with further args: (%q, %v)", got, ok)
		}
		if _, ok := cs.gateShell(context.Background(), "github"); ok {
			t.Error("git* must not match github (word boundary)")
		}
	})

	t.Run("approvals are journaled", func(t *testing.T) {
		root := t.TempDir()
		cs := &CortexSession{classifyShell: stubRisky, workspace: &Workspace{Root: root},
			confirmRisky: func(q string) lineedit.ConfirmChoice {
				if !strings.Contains(q, `p always "git*"`) {
					t.Errorf("prompt must show the derived prefix git*: %q", q)
				}
				return lineedit.ConfirmAlwaysPrefix
			}}
		if _, ok := cs.gateShell(context.Background(), "git"); !ok {
			t.Fatal("p should allow the command")
		}
		if len(cs.shellApprovals) != 1 ||
			cs.shellApprovals[0].Kind != "prefix" ||
			cs.shellApprovals[0].Pattern != "git*" {
			t.Fatalf("p must record the derived prefix, got %+v", cs.shellApprovals)
		}
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
		if got[0].Kind != "prefix" || got[0].Pattern != "git*" {
			t.Errorf("first approval = %+v, want prefix/git*", got[0])
		}
		if got[1].Kind != "exact" || got[1].Pattern != "make lint" {
			t.Errorf("second approval = %+v, want exact/make lint", got[1])
		}
	})

	t.Run("approvals are session-scoped (memory only)", func(t *testing.T) {
		cs := &CortexSession{classifyShell: stubRisky, quiet: true,
			shellApprovals: []shellApproval{{Kind: "prefix", Pattern: "make test*"}}}
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

// TestCommandPrefixPattern pins the stored prefix for a "p" (prefix)
// approval (issue #107): the program plus one subcommand + "*" — the issue's
// "make test" example yields "make test*" — while a single-token command
// yields "prog*" (matching the command itself and its arguments, but never
// a longer glued word), and an empty command yields "*" (a bare "*" prefix
// matches nothing, so it is harmless).
func TestCommandPrefixPattern(t *testing.T) {
	for _, tc := range []struct {
		cmd  string
		want string
	}{
		{"make test ./pkg/...", "make test*"},
		{"make test", "make test*"},
		{"git", "git*"},
		{"", "*"},
	} {
		t.Run(tc.cmd, func(t *testing.T) {
			if got := commandPrefixPattern(tc.cmd); got != tc.want {
				t.Errorf("commandPrefixPattern(%q) = %q, want %q", tc.cmd, got, tc.want)
			}
		})
	}
}
