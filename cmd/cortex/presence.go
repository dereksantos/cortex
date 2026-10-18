package main

import (
	"context"
	"errors"

	"github.com/dereksantos/cortex/internal/presence"
)

// startPresence attaches the terminal-host presence emitter (Supacode's
// active-session badge, internal/presence) and reports the session started.
// Off a Supacode surface the emitter is nil and every report is a no-op.
// Inside a turn the session reports busy (turnWithImages), awaiting_input
// around a Risky-command prompt (gateShell), and compacting (Compact).
func (cs *CortexSession) startPresence() {
	cs.presence = presence.FromEnv()
	cs.presence.Emit(presence.SessionStart)
}

// reportTurnEnd is the host's turn-boundary report, sent once per user input
// after afterTurn (so a boundary compaction reports before idle): idle plus
// a notification carrying the reply, idle alone for an interrupted turn, or
// error for a turn that failed. The error notification is generic on
// purpose — the error text is printed in the terminal, and a notification
// leaves it.
func (cs *CortexSession) reportTurnEnd(reply string, interrupted bool, err error) {
	switch {
	case err == nil && !interrupted:
		cs.presence.Emit(presence.Idle)
		if reply != "" {
			cs.presence.Notify("", reply)
		}
	case interrupted || errors.Is(err, context.Canceled):
		cs.presence.Emit(presence.Idle)
	default:
		cs.presence.Emit(presence.Error)
		cs.presence.Notify("Agent error", "Turn stopped on an error")
	}
}

// endPresence reports the session over. The trailing idle matches the host's
// own agent integrations, which clear the surface's state after session_end.
func (cs *CortexSession) endPresence() {
	cs.presence.Emit(presence.SessionEnd)
	cs.presence.Emit(presence.Idle)
}
