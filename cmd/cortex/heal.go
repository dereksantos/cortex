// heal.go — the mid-session model-healing ladder (docs/model-self-healing.md
// §2). A Sender decorator wraps the coder's and every subagent's round-trip:
// when a call fails with a healable class (model-missing; rate-limited or
// server after the transport's own retries), the failing model is marked
// dead for this session, the existing curated ladder (preflight.go's
// nextCuratedPick → discoverFreeModel selection) picks a live replacement,
// the session bindings rebind the way SetModel does, and the SAME pending
// request re-issues on the new model — runLoop never notices, the turn
// continues. The config file is never rewritten; the receipt is one stderr
// line plus a model.substitution journal event, exactly like the startup
// preflight. Non-healable classes (auth, timeout, unreachable) pass through
// untouched: swapping models cannot fix a revoked key or a dead endpoint.
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/dereksantos/cortex/internal/journal"
	"github.com/dereksantos/cortex/pkg/llm"
)

// healMaxCandidates caps replacement sends per failing call — the real
// pending request is the smoke test for each candidate, so this bounds the
// worst case at a handful of extra round-trips, never an unbounded walk.
const healMaxCandidates = 3

// healDetailCap bounds the error text carried into a model failure journal
// event.
const healDetailCap = 300

// pendingFailure is the failure receipt the healing ladder settles for one
// failed SEND (heal.go): the role binding whose call failed, the model whose
// call failed (the one the ladder marked dead), and the classified class.
// It rides the returned error (healJournaledError) rather than any
// session-scoped flag, so the receipt is scoped to the exact send that
// failed — a later subagent's healed-then-failed send can neither consume
// nor clobber the coder's own receipt (issue #117 review round 3).
type pendingFailure struct {
	role  string
	model string
	class modelErrClass
}

// healJournaledError wraps the original provider error of one failed send
// that the healing ladder walked and could not recover, tagging it with the
// receipt to settle for that send. runLoop passes the error it got through
// the Sender seam untouched into stats.LastError (the error-recovered break)
// and returns it to the caller (the unrecovered "error" stop), so a
// single marker at the seam scopes the receipt to the send.
type healJournaledError struct {
	pendingFailure
	cause error
}

func (e *healJournaledError) Error() string { return e.cause.Error() }
func (e *healJournaledError) Unwrap() error { return e.cause }

// pendingFailureOf extracts the receipt a failed send carried (healJournaledError),
// or nil when the send failed without the ladder walking it (a non-healable
// class, the gate off, a streamed partial, …).
func pendingFailureOf(err error) *pendingFailure {
	var hje *healJournaledError
	if errors.As(err, &hje) {
		return &hje.pendingFailure
	}
	return nil
}

// healingSender decorates inner with the self-healing ladder. role labels
// the binding for notices and journal events ("code", "study", a subagent
// profile name). The decorator heals only complete-call failures: when the
// coder path already streamed part of an answer, the error passes through
// (a re-send would double-echo) and the NEXT send heals instead.
func (cs *CortexSession) healingSender(role string, inner Sender) Sender {
	return SenderFunc(func(ctx context.Context, req *AgentRequest) (*AgentResponse, bool, error) {
		res, streamed, err := inner.Send(ctx, req)
		if err == nil || streamed {
			return res, streamed, err
		}
		hres, hstreamed, ok, pf := cs.tryHeal(ctx, role, req, inner, err)
		if ok {
			return hres, hstreamed, nil
		}
		if pf != nil {
			return res, streamed, &healJournaledError{pendingFailure: *pf, cause: err}
		}
		return res, streamed, err
	})
}

// tryHeal is one healing attempt for one failed call. It returns ok=false —
// leaving the caller to surface the ORIGINAL error — whenever healing does
// not apply (gate off, non-OpenRouter backend, non-healable class, user
// cancel), the live catalog is unreachable (the endpoint is the problem;
// thrashing models would not help), or every candidate also failed.
//
// ok=false with a non-nil pendingFailure (pf) means the ladder walked but
// could not recover: the caller (healingSender) stashes that receipt on the
// returned error, and the turn layer settles it for the send — see
// pendingFailure's doc. ok=false with pf nil means healing does not apply at
// all; the error passes through with no receipt attached.
func (cs *CortexSession) tryHeal(ctx context.Context, role string, req *AgentRequest, inner Sender, cause error) (*AgentResponse, bool, bool, *pendingFailure) {
	if cs.Config == nil || !cs.Config.isOpenRouter() || !cs.Config.selfHealEnabled() {
		return nil, false, false, nil
	}
	if ctx.Err() != nil {
		return nil, false, false, nil
	}
	class := classifyModelError(cause)
	if !class.healable() {
		return nil, false, false, nil
	}

	failed := req.Model
	cs.markModelDead(failed, class)

	pf := &pendingFailure{role: role, model: failed, class: class}
	listModels := cs.healList
	if listModels == nil {
		listModels = liveOpenRouterListModels
	}
	pctx, cancel := context.WithTimeout(ctx, openRouterPreflightTimeout)
	served, err := listModels(pctx)
	cancel()
	if err != nil {
		// The catalog itself is down: the original error surfaces to the
		// loop, which recovers (error-recovered) when the turn has made
		// progress. The receipt for this send rides the returned error
		// (healJournaledError) — the turn layer settles it as
		// model.recovered_error on a recovered turn, as model.failure only
		// when nothing recovers — so one failed send yields exactly one
		// journal record, scoped to THIS send (a subagent's own ladder walk
		// carries its own marker and can't suppress the coder's receipt).
		// The stderr notice still tells the user what failed.
		return nil, false, false, pf
	}

	servedSet := make(map[string]bool, len(served))
	servedFree := make([]llm.OpenRouterModel, 0, len(served))
	for _, m := range served {
		servedSet[m.ID] = true
		if strings.HasSuffix(m.ID, ":free") {
			servedFree = append(servedFree, m)
		}
	}

	for attempt := 0; attempt < healMaxCandidates; attempt++ {
		id, window, why, ok := cs.nextHealCandidate(servedSet, servedFree)
		if !ok {
			break
		}
		old := req.Model
		cs.rebindAfterHeal(req, old, id, window)
		reportHeal(role, old, id, class, why, cs.healJournalDir())

		res, streamed, err := inner.Send(ctx, req)
		if err == nil {
			return res, streamed, true, nil
		}
		if ctx.Err() != nil {
			return nil, false, false, nil
		}
		cs.markModelDead(id, classifyModelError(err))
	}

	// Every candidate failed. The receipt for THIS send rides back on the
	// pendingFailure (the caller wraps it into the returned error); the turn
	// layer or the subagent path settles it — model.recovered_error when the
	// run recovers, model.failure when nothing does — so one failed send
	// yields exactly one journal record and a recovered turn is never
	// reported as "FAILED unrecovered".
	return nil, false, false, pf
}

// markModelDead records a model as unusable for the remainder of this
// session so later heals and turns skip it. Session-local by design: a model
// that was rate-limited today is retried fresh in the next session.
func (cs *CortexSession) markModelDead(model string, class modelErrClass) {
	if cs.deadModels == nil {
		cs.deadModels = make(map[string]modelErrClass)
	}
	cs.deadModels[model] = class
}

// nextHealCandidate picks the next replacement: the first curated entry that
// is served and not session-dead (deterministic), else discoverFreeModel's
// heuristic over the served :free catalog with session-dead models filtered
// out (adaptive) — the same two-tier ladder the startup preflight walks.
func (cs *CortexSession) nextHealCandidate(servedSet map[string]bool, servedFree []llm.OpenRouterModel) (id string, window int, why string, ok bool) {
	for _, m := range curatedFreeModels {
		if _, dead := cs.deadModels[m.ID]; dead {
			continue
		}
		if servedSet[m.ID] {
			return m.ID, m.Window, "next curated pick still served", true
		}
	}
	alive := make([]llm.OpenRouterModel, 0, len(servedFree))
	for _, m := range servedFree {
		if _, dead := cs.deadModels[m.ID]; !dead {
			alive = append(alive, m)
		}
	}
	return discoverFreeModel(alive)
}

// rebindAfterHeal updates every session binding that carried the dead model
// (code and study often share one) so later requests — including fresh
// requestFor builds for subagents — start from the healed pick, then stamps
// the in-flight request itself. The code-role path goes through SetModel so
// effort wire fields re-validate instead of carrying over stale
// (docs/thinking-models.md seam bug #1).
func (cs *CortexSession) rebindAfterHeal(req *AgentRequest, oldModel, newID string, window int) {
	if cs.Request != nil && cs.Request.Model == oldModel {
		cs.SetModel(newID)
		if window > 0 {
			cs.Window = window
		}
	}
	if cs.Study.Model == oldModel {
		cs.Study.Model = newID
		if window > 0 {
			cs.Study.Window = window
		}
	}
	if req.Model == oldModel {
		req.Model = newID
	}
}

// reportHeal prints the one mid-session notice line and journals the
// substitution — the mid-session sibling of preflight.go's
// reportSubstitution, reusing its journal payload with the failure class in
// the reason so the receipt says WHY the switch happened, not just what it
// switched to.
func reportHeal(role, old, newModel string, class modelErrClass, why, journalDir string) {
	fmt.Fprintf(os.Stderr, "cortex: %s model %q failed (%s) — switching to %q (%s)\n",
		role, old, class, newModel, why)
	if journalDir == "" {
		return
	}
	_ = appendModelSubstitution(journalDir, journal.ModelSubstitutionPayload{
		Role:   role,
		Old:    old,
		New:    newModel,
		Reason: fmt.Sprintf("mid-session heal after %s: %s", class, why),
	})
}

// healJournalDir resolves the model journal class-dir, or "" when the
// session has no workspace (bare test constructions) — journal writes are
// skipped, healing itself still runs.
func (cs *CortexSession) healJournalDir() string {
	if cs.workspace == nil {
		return ""
	}
	return modelSubstitutionJournalDir(cs.workspace.ContextDir())
}

// journalModelFailure appends one model.failure receipt for a send nothing
// recovered (the unrecovered case the healing ladder walked) — best-effort,
// same posture as the substitution write. The recovered case's receipt is
// model.recovered_error (turn.go's reportRecoverableError): the distinct
// types keep a recovered turn from being reported as "FAILED unrecovered".
func (cs *CortexSession) journalModelFailure(pf *pendingFailure, cause error) {
	if pf == nil || cs.healJournalDir() == "" {
		return
	}
	detail := ""
	if cause != nil {
		detail = cause.Error()
		if len(detail) > healDetailCap {
			detail = detail[:healDetailCap]
		}
	}
	entry, err := journal.NewModelFailureEntry(journal.ModelFailurePayload{
		Role:   pf.role,
		Model:  pf.model,
		Class:  string(pf.class),
		Status: errStatus(cause),
		Detail: detail,
	})
	if err != nil {
		return
	}
	w, err := journal.NewWriter(journal.WriterOpts{
		ClassDir: modelSubstitutionJournalDir(cs.workspace.ContextDir()),
		Fsync:    journal.FsyncPerBatch,
	})
	if err != nil {
		return
	}
	defer w.Close()
	_, _ = w.Append(entry)
}
