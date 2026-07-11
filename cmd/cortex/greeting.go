// greeting.go — the first-run greeting turn (Phase 1 / M1.5, M1.7).
//
// On first run (IsFirstRun, M1.4), MaybeGreet runs one synthetic
// session.Turn with greetingPrompt and writes the greeting marker so later
// launches don't re-fire. Per GOAL.md §3 P1 greeting, the prompt states
// principles — never a task recipe — and consent to scan the user's AI
// landscape is the user's conversational reply each time, not this text or
// any flag. The scan itself is Phase 2; Phase 1 only ships the invitation.
//
// M1.7 (D3's ask-and-persist half): the same greeting also asks where the
// user's code lives, and arms cs.awaitingScanRootsReply so the REPL's next
// real input — the human's actual answer — is captured by
// MaybeCaptureScanRoots (scanroots.go) and persisted to user config. Only
// the asking lives here; parsing/persisting the answer is deliberately
// plain text processing, not another model round-trip (see scanroots.go's
// doc comment).
package main

import (
	"context"
	"os"
	"path/filepath"
)

// greetingPrompt is the synthetic user turn MaybeGreet plays on first run.
// Golden-pinned (TestGreetingPromptGolden): any change to this text is a
// visible diff, per GOAL.md §3 P1's "golden-pinned" requirement.
const greetingPrompt = `This is the user's first time running you. Introduce yourself in a ` +
	`few short sentences: who you are (a coding agent with working memory ` +
	`that persists across turns) and how you like to work. Mention, as an ` +
	`offer only, that you can look over their local AI landscape ` +
	`(installed harnesses, runtimes, other projects on this machine) if ` +
	`they'd like — make clear it's read-only and only happens if they say ` +
	`yes. Also ask where their code generally lives on this machine (for ` +
	`example ~/code or ~/projects), so you'll know where to look later if ` +
	`they ever want that survey — this is just information gathering, not ` +
	`the survey itself. Keep it brief and warm; don't recite a menu of ` +
	`commands or tools.`

// MaybeGreet fires the greeting turn exactly once. Non-first runs (per
// IsFirstRun) are a no-op. The marker is written only after Turn returns
// without error, so a failed greeting can retry on the next launch.
func MaybeGreet(ctx context.Context, cs *CortexSession) error {
	if !IsFirstRun() {
		return nil
	}
	if _, err := cs.Turn(ctx, greetingPrompt); err != nil {
		return err
	}
	if err := writeGreetingMarker(); err != nil {
		return err
	}
	// M1.7: the greeting just asked where the user's code lives; arm the
	// flag so the REPL's next real input (the human's actual reply, not
	// another model call) is captured and persisted as scan roots.
	cs.awaitingScanRootsReply = true
	return nil
}

// writeGreetingMarker records that the greeting has fired by creating the
// marker file (see greetingMarkerPath, M1.4). A resolution failure (no
// $CORTEX_HOME, no home directory) degrades to a no-op, matching
// greetingMarkerPath's own degrade-to-empty convention — nothing to mark.
func writeGreetingMarker() error {
	path := greetingMarkerPath()
	if path == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte("1\n"), 0644)
}
