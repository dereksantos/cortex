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

// healDetailCap bounds the error text carried into a model.failure journal
// event.
const healDetailCap = 300

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
		if hres, hstreamed, ok := cs.tryHeal(ctx, role, req, inner, err); ok {
			return hres, hstreamed, nil
		}
		return res, streamed, err
	})
}

// tryHeal is one healing attempt for one failed call. It returns ok=false —
// leaving the caller to surface the ORIGINAL error — whenever healing does
// not apply (gate off, non-OpenRouter backend, non-healable class, user
// cancel), the live catalog is unreachable (the endpoint is the problem;
// thrashing models would not help), or every candidate also failed.
func (cs *CortexSession) tryHeal(ctx context.Context, role string, req *AgentRequest, inner Sender, cause error) (*AgentResponse, bool, bool) {
	if cs.Config == nil || !cs.Config.isOpenRouter() || !cs.Config.selfHealEnabled() {
		return nil, false, false
	}
	if ctx.Err() != nil {
		return nil, false, false
	}
	class := classifyModelError(cause)
	if !class.healable() {
		return nil, false, false
	}

	failed := req.Model
	cs.markModelDead(failed, class)

	listModels := cs.healList
	if listModels == nil {
		listModels = liveOpenRouterListModels
	}
	pctx, cancel := context.WithTimeout(ctx, openRouterPreflightTimeout)
	served, err := listModels(pctx)
	cancel()
	if err != nil {
		cs.journalModelFailure(role, failed, class, cause)
		return nil, false, false
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
			return res, streamed, true
		}
		if ctx.Err() != nil {
			return nil, false, false
		}
		cs.markModelDead(id, classifyModelError(err))
	}

	cs.journalModelFailure(role, failed, class, cause)
	return nil, false, false
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

// journalModelFailure appends one model.failure receipt — best-effort, same
// posture as the substitution write.
func (cs *CortexSession) journalModelFailure(role, model string, class modelErrClass, cause error) {
	if cs.healJournalDir() == "" {
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
		Role:   role,
		Model:  model,
		Class:  string(class),
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
