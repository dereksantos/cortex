package main

import (
	"path/filepath"

	"github.com/dereksantos/cortex/internal/journal"
)

// taintReceiptReason is the Reason every security.taint receipt carries:
// the event's cause is always the framing the source tool's result carried
// (the dispatcher detects nothing else). A constant, not a parameter — the
// write path has exactly one cause to name.
const taintReceiptReason = "untrusted-content marker in observation"

// securityClassDir resolves the project-scope security journal class dir
// (.cortex/journal/security/), or "" when the session has no workspace
// (bare test constructions) — the write is skipped, the taint itself still
// applies. Mirrors recovery_journal.go's recoveryClassDir shape: nil
// workspace → "" → journalSecurityTaint skips the receipt (best-effort,
// the same posture reasoning-fallback / model-failure receipts take).
func (cs *CortexSession) securityClassDir() string {
	if cs.workspace == nil {
		return ""
	}
	return filepath.Join(cs.workspace.ContextDir(), "journal", "security")
}

// journalSecurityTaint appends one security.taint receipt to the
// project-scope class dir (issue #102): once per source when the framed
// content arrives (riskyGated=false), and once when a Risky command is
// first gated under the turn's taint (riskyGated=true) — so a reader can
// count both "how often web content enters a turn" and "how often that
// taint actually engaged the raised approval bar". Best-effort: a failed
// write (workspace missing, disk error) never changes the gate's behavior
// and is never surfaced as an engine error — it only means this event is
// missing from run-history telemetry, which is why the failure is
// swallowed here (the measurement having holes is acceptable; the security
// behavior failing with it is not).
func (cs *CortexSession) journalSecurityTaint(source string, riskyGated bool) {
	dir := cs.securityClassDir()
	if dir == "" || cs.turnNo <= 0 {
		return
	}
	entry, err := journal.NewSecurityTaintEntry(journal.SecurityTaintPayload{
		Source:     source,
		Reason:     taintReceiptReason,
		TurnNo:     cs.turnNo,
		RiskyGated: riskyGated,
	})
	if err != nil {
		return
	}
	w, err := journal.NewWriter(journal.WriterOpts{
		ClassDir: dir,
		Fsync:    journal.FsyncPerBatch,
	})
	if err != nil {
		return
	}
	defer w.Close()
	_, _ = w.Append(entry)
}
