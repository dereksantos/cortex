package main

import (
	"strings"
)

// untrustedTaint is the per-turn untrusted-content taint (issue #102): the
// record that attacker-controllable web content entered the conversation in
// this turn, so a later Risky shell command is judged under the raised bar —
// the intent judge's verdict no longer waves it through, only a human
// decision does (see CortexSession.taint in session_core.go and gateShell in
// tool_deps.go).
//
// It follows the same-action ledger's lifecycle (issue #169): owned by the
// session, scoped to the in-flight turn (turnNo), inert outside a turn
// (turnNo == 0 — records drop, the gate never consults a taint that isn't
// the current turn's), and explicitly cleared at the START of every turn
// (turn.go) so a taint from a turn that errored or was interrupted before
// its end cannot leak into the next one. Nil between turns.
type untrustedTaint struct {
	// turnNo is the 1-based ordinal of the turn this taint belongs to,
	// stamped from cs.turnNo at record time (mirrors sameActionBlocked's
	// keying by cs.turnNo).
	turnNo int
	// sources names what brought untrusted content into the turn — the tool
	// names that produced a framed result ("fetch_url", "web_search"), in
	// first-seen order. Surfaced in the prompt's reason and the blocked
	// message so the model and the human see WHY the bar is raised.
	sources []string
	// gatedLogged marks that the security.taint follow-up receipt
	// (RiskyGated=true) has already been written for this turn: the first
	// Risky gate under the taint journals once, repeats stay quiet (same
	// once-per-event discipline as the per-source arrival receipts).
	gatedLogged bool
}

// recordUntrustedContent records that a tool result from toolName carried
// the untrusted-content framing marker (tools.UntrustedMarker) this turn,
// tainting it (issue #102). Called from coderDispatcher (loop.go) when
// tools.ObservationIsUntrustedContent matches an observation; repeated calls
// (several fetches in one turn) merge into the same taint, naming each
// source once. Outside a turn (cs.turnNo == 0) the record is dropped exactly
// as recordSameActionBlock drops its ledger entry (tool_deps.go): there is
// no current turn to attach it to, and a stale record must not leak into the
// next one. The taint event is journalled best-effort alongside the record
// (security.taint, see untrusted_journal.go); a failed write never changes
// the gate.
func (cs *CortexSession) recordUntrustedContent(toolName string) {
	if cs == nil || cs.turnNo == 0 {
		return
	}
	if cs.taint == nil || cs.taint.turnNo != cs.turnNo {
		cs.taint = &untrustedTaint{turnNo: cs.turnNo}
		cs.taint.sources = append(cs.taint.sources, toolName)
		// One receipt per turn per source (issue #102): the first arrival of
		// a source is the event worth journalling; repeats only add nothing
		// new to the count a reader would want.
		cs.journalSecurityTaint(toolName, false)
		return
	}
	for _, s := range cs.taint.sources {
		if s == toolName {
			return
		}
	}
	cs.taint.sources = append(cs.taint.sources, toolName)
	cs.journalSecurityTaint(toolName, false)
}

// untrustedContentActive reports whether the CURRENT turn is tainted
// (issue #102). Like sameActionBlockedInTurn it is inert outside a turn
// (turnNo == 0): the gate still runs, but nothing is judged under the
// raised bar. A taint stamped to a different turnNo (a stale one from a
// turn that ended abnormally before turn.go's start-of-turn clear) does not
// count either.
func (cs *CortexSession) untrustedContentActive() bool {
	if cs == nil || cs.turnNo == 0 || cs.taint == nil {
		return false
	}
	return cs.taint.turnNo == cs.turnNo
}

// ConfineWrites implements tools.WriteConferrer (issue #102): on a tainted
// turn, write_file / edit_file / remove_path confine their paths to the
// session's workspace ROOT (cs.WorkspaceRoot() — every workspace, explicit
// or CWD-derived, unlike Workdir()'s explicit-only anchor) — a page that
// steered the request must not get an escape-the-workspace write or delete
// waved through as ordinary work. Untainted turns (and turns outside any
// session) answer false: the tools keep their normal behavior.
func (cs *CortexSession) ConfineWrites() bool { return cs.untrustedContentActive() }

// TaintedWriteRoot is the second WriteConferrer method (issue #102): the
// root a confined call must stay within — the session's workspace root (""
// when there is none, which tools.ConfineWrites resolves to the CWD, the
// root a CWD-derived session lives in). Kept separate from Workdir() on
// purpose: Workdir anchors relative paths only for explicit (--project)
// workspaces, while the taint rule confines to whatever root the session
// actually has.
func (cs *CortexSession) TaintedWriteRoot() string {
	if cs == nil || cs.workspace == nil {
		return ""
	}
	return cs.workspace.Root
}

// recordRiskyGateUnderTaint journals the security.taint follow-up receipt
// exactly once per turn when a Risky command was gated while the turn was
// tainted (issue #102) — the "did the raised bar actually engage" half of
// the telemetry. Called from gateShell's Risky branch. No-op when the turn
// isn't tainted or the receipt already went out this turn.
func (cs *CortexSession) recordRiskyGateUnderTaint() {
	if !cs.untrustedContentActive() || cs.taint.gatedLogged {
		return
	}
	cs.taint.gatedLogged = true
	// The receipt names the taint's primary source: the first source to
	// enter the turn is what raised the bar a reader is measuring against.
	cs.journalSecurityTaint(cs.taint.sources[0], true)
}

// taintSources returns the taint's source tool names in first-seen order
// (nil when the turn is not tainted) — the raw list the blocked message and
// the journal event name, unsorted so it records what actually fired first.
func (cs *CortexSession) taintSources() []string {
	if !cs.untrustedContentActive() {
		return nil
	}
	return cs.taint.sources
}

// taintSourceNote renders the taint's sources for a human-facing reason
// line: `untrusted web content entered this turn (fetch_url, web_search)`.
// Empty when the turn is not tainted. Sources keep first-seen order — the
// prompt reads as the story of the turn, and the taint's wording has one
// ordering rule everywhere (prompt, blocked message, journal).
func (cs *CortexSession) taintSourceNote() string {
	if !cs.untrustedContentActive() {
		return ""
	}
	return "untrusted web content entered this turn (" + strings.Join(cs.taint.sources, ", ") + ")"
}
