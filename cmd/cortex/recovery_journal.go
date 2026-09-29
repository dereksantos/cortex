// recovery_journal.go — the issue #149 reasoning-fallback receipt write path.
// appendReasoningFallback appends one recovery.reasoning_fallback entry to the
// project-scope class dir (.cortex/journal/recovery/) when a fallback
// triggers — best-effort, mirroring heal.go's journalModelFailure /
// reportHeal posture (best-effort: a failed receipt write is not surfaced as
// an engine error, it just means the recovery is not visible in run history
// this one time, which is the same posture model.substitution / model.failure
// receipts already take). The class dir is resolved from the session's
// workspace .cortex dir (cs.ContextDir(), which falls back to the CWD-implicit
// contextDir() for hand-constructed sessions with no resolved workspace) —
// the project-scope convention study_eval.go / study.go use for
// .cortex/journal/study/, .cortex/journal/eval/, .cortex/journal/capture/.
//
// The receipt is the issue #149 telemetry the plan calls for: which models
// keep needing the fallback, per project, separated from the prompt-based
// salvage receipts (salvageEmptyFinalize / salvageObservationFinalize, which
// today write no journal receipt of their own) so a reader can compare
// "how often does the on/off retry recover an empty finish" against "how
// often does the prompt-based salvage recover" on the same model.
package main

import (
	"path/filepath"

	"github.com/dereksantos/cortex/internal/journal"
)

// recoveryClassDir resolves the project-scope recovery journal class dir
// (.cortex/journal/recovery/), or "" when the session has no workspace
// (bare test constructions) — journal writes are skipped, the recovery
// itself still runs. Mirrors heal.go's healJournalDir shape: nil
// workspace → "" → appendReasoningFallback skips the write (best-effort).
func (cs *CortexSession) recoveryClassDir() string {
	if cs.workspace == nil {
		return ""
	}
	return filepath.Join(cs.workspace.ContextDir(), "journal", "recovery")
}

// appendReasoningFallback appends one recovery.reasoning_fallback receipt to
// the project-scope class dir. Best-effort: a failed write (workspace missing,
// disk error) is swallowed — the recovery already ran, the receipt is a
// post-hoc record, and a write failure is not an engine error the caller
// should surface. The caller (runLoop's natural-finish branch, via
// Toolset.OnReasoningFallback) passes the fields the recovery attributed at
// the time of the recovery (model, role, path, stop reason, clamp state) —
// the receipt is a snapshot of that attribution, not a re-derivation from the
// turn's transcript. role is the role binding that was running ("code" or
// "study"), the same vocabulary model.substitution / model.failure receipts
// use.
func (cs *CortexSession) appendReasoningFallback(role, path, model, stopReason, outcome string, maxTokensClamped, salvagedUnclamped bool) {
	if role == "" {
		return // no role to attribute — skip the receipt
	}
	dir := cs.recoveryClassDir()
	if dir == "" {
		return
	}
	entry, err := journal.NewReasoningFallbackEntry(journal.ReasoningFallbackPayload{
		Model:             model,
		Role:              role,
		Path:              path,
		Outcome:           outcome,
		StopReason:        stopReason,
		MaxTokensClamped:  maxTokensClamped,
		SalvagedUnclamped: salvagedUnclamped,
	})
	if err != nil {
		return
	}
	w, err := journal.NewWriter(journal.WriterOpts{ClassDir: dir, Fsync: journal.FsyncPerBatch})
	if err != nil {
		return
	}
	defer w.Close()
	_, _ = w.Append(entry)
}

// transcriptNote writes a one-line record to the session transcript ONLY —
// never to req.Messages. Unlike cs.Append, it must not touch the wire
// conversation: the note is a human-readable marker of a harness-side event,
// and appending a system message mid-conversation would put a non-leading
// system message into every later request and the resumable session log. It
// is written under a distinct kindNote so loadSession (cortex resume) skips
// it — it is never part of the model-visible history, only of the
// human-readable transcript. No-op when the transcript is not started (bare
// test constructions), the same best-effort posture as writeTranscript.
func (cs *CortexSession) transcriptNote(content string) {
	if cs.transcript == nil {
		return
	}
	cs.writeEntry(sessionEntry{Kind: kindNote, Turn: cs.turnNo, Message: Message{Role: RoleSystem, Content: content}})
}
