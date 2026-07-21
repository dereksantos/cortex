// preflight.go — the E2 unavailability fallback: a cheap, bounded check at
// session startup for whether a curated OpenRouter :free pick is still
// served, substituting a live one when it isn't
// (docs/completion-roadmap.md Track E2). Deterministic first (walk the
// curated table), adaptive on failure (discover :free models and pick by a
// documented heuristic). Runs once per session construction — NOT on the
// per-turn Send hot path — and never blocks startup: a preflight failure
// (network down, slow) leaves the configured model unchanged.
package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/dereksantos/cortex/internal/journal"
	"github.com/dereksantos/cortex/pkg/llm"
)

// defaultOpenRouterPreflightTimeout is the immutable historical default —
// see fleetDiscoveryTimeout's doc comment (config.go) for why the resolver
// (Config.preflightTimeout) falls back to this const, never the mutable
// openRouterPreflightTimeout var below.
const defaultOpenRouterPreflightTimeout = 4 * time.Second

// openRouterPreflightTimeout bounds the one ListModels call the whole
// substitution check makes — small enough that REPL startup latency stays
// imperceptible even when OpenRouter is slow to answer, and this path only
// runs at all when the backend is openrouter and a role bound a curated id.
// A var: NewCortexSession sets it once from network.preflight_timeout_sec;
// unset, it stays defaultOpenRouterPreflightTimeout.
var openRouterPreflightTimeout = defaultOpenRouterPreflightTimeout

// listModelsFn matches llm.OpenRouterClient.ListModels's shape. Production
// wires it to a real client; tests fake it, so the preflight never touches
// the network in the test suite.
type listModelsFn func(context.Context) ([]llm.OpenRouterModel, error)

// preflightCuratedModels checks code and study's bound models against
// OpenRouter's live catalog ONE time (a single listModels call shared by
// both roles) and substitutes any bound model that's no longer served.
// Under network.self_heal (the default, docs/model-self-healing.md §3) that
// covers EVERY OpenRouter binding — a user's own pin included, since a
// retired pin would otherwise fail the first turn; with the gate off, the
// preflight keeps its strict historical posture and only second-guesses
// picks it (or a prior bootstrap run) made from the curated table.
// Substitution never rewrites the user's config file: it mutates the
// in-memory ModelSpec for this process only, prints one stderr line per
// substitution, and journals the event to journalDir. A network failure (or
// a backend/model combination the preflight doesn't apply to) returns code
// and study unchanged.
func preflightCuratedModels(ctx context.Context, cfg *Config, code, study ModelSpec, journalDir string, listModels listModelsFn) (ModelSpec, ModelSpec) {
	if !cfg.isOpenRouter() {
		return code, study
	}
	checkAll := cfg.selfHealEnabled()
	if !checkAll && !isCuratedModel(code.Model) && !isCuratedModel(study.Model) {
		return code, study
	}

	pctx, cancel := context.WithTimeout(ctx, openRouterPreflightTimeout)
	defer cancel()
	served, err := listModels(pctx)
	if err != nil {
		// Network down or slow: proceed with the configured models
		// unchanged — never block startup on the preflight.
		return code, study
	}

	servedSet := make(map[string]bool, len(served))
	servedFree := make([]llm.OpenRouterModel, 0, len(served))
	for _, m := range served {
		servedSet[m.ID] = true
		if strings.HasSuffix(m.ID, ":free") {
			servedFree = append(servedFree, m)
		}
	}

	if checkAll || isCuratedModel(code.Model) {
		code = substituteIfMissing(code, roleCode, servedSet, servedFree, journalDir)
	}
	if checkAll || isCuratedModel(study.Model) {
		study = substituteIfMissing(study, roleStudy, servedSet, servedFree, journalDir)
	}
	return code, study
}

// substituteIfMissing is the per-role decision: if spec.Model is still
// served, it's returned untouched — no stderr line, no journal event.
// Otherwise it walks curatedFreeModels in preference order for the first
// still-served entry (deterministic); failing that, it falls back to
// discoverFreeModel's heuristic pick over the live :free catalog (adaptive).
// If literally nothing survives (no curated entry served, no :free models at
// all), spec is returned unchanged — a stale pick beats no model.
func substituteIfMissing(spec ModelSpec, role string, servedSet map[string]bool, servedFree []llm.OpenRouterModel, journalDir string) ModelSpec {
	if servedSet[spec.Model] {
		return spec
	}

	old := spec.Model
	newID, newWindow, reason, ok := nextCuratedPick(old, servedSet)
	if !ok {
		newID, newWindow, reason, ok = discoverFreeModel(servedFree)
	}
	if !ok {
		return spec
	}

	spec.Model = newID
	if newWindow > 0 {
		spec.Window = newWindow
	}
	reportSubstitution(role, old, newID, reason, journalDir)
	return spec
}

// nextCuratedPick walks curatedFreeModels in preference order for the first
// entry — other than the one that just failed preflight — that servedSet
// reports as still served.
func nextCuratedPick(failed string, servedSet map[string]bool) (id string, window int, reason string, ok bool) {
	for _, m := range curatedFreeModels {
		if m.ID == failed {
			continue
		}
		if servedSet[m.ID] {
			return m.ID, m.Window, "next curated pick still served", true
		}
	}
	return "", 0, "", false
}

// coderNameHints are substrings (case-insensitive) that mark a model id as
// coder-oriented for discoverFreeModel's heuristic.
var coderNameHints = []string{"coder", "code", "coding"}

// discoverFreeModel is the adaptive fallback once every curated entry has
// gone missing: pick a :free model by a simple, documented heuristic —
// prefer a coder-ish name match (docs/completion-roadmap.md E2: "coder-ish
// name match, then largest context"), then break ties (or fall through when
// no name matches at all) by largest context length. free must already be
// filtered to ":free"-suffixed ids.
func discoverFreeModel(free []llm.OpenRouterModel) (id string, window int, reason string, ok bool) {
	if len(free) == 0 {
		return "", 0, "", false
	}
	var coderish []llm.OpenRouterModel
	for _, m := range free {
		lower := strings.ToLower(m.ID)
		for _, hint := range coderNameHints {
			if strings.Contains(lower, hint) {
				coderish = append(coderish, m)
				break
			}
		}
	}
	pool, reasonText := free, "largest-context :free model (no coder-named model was available)"
	if len(coderish) > 0 {
		pool, reasonText = coderish, "coder-named :free model with the largest context"
	}
	sort.Slice(pool, func(i, j int) bool { return pool[i].ContextLength > pool[j].ContextLength })
	best := pool[0]
	return best.ID, best.ContextLength, reasonText, true
}

// reportSubstitution prints the one required stderr line and journals the
// event. The journal write is best-effort — a failure never blocks startup
// or undoes the in-memory substitution that already happened.
func reportSubstitution(role, old, newModel, reason, journalDir string) {
	fmt.Fprintf(os.Stderr, "cortex: %s model %q is no longer served by OpenRouter — substituting %q (%s)\n",
		role, old, newModel, reason)
	_ = appendModelSubstitution(journalDir, journal.ModelSubstitutionPayload{
		Role:   role,
		Old:    old,
		New:    newModel,
		Reason: reason,
	})
}

// appendModelSubstitution appends one model.substitution event to
// journalDir. Mirrors emitStudyResult's write path (study_eval.go):
// FsyncPerBatch, since a substitution event is regeneratable in spirit by
// re-running the preflight against the live catalog.
func appendModelSubstitution(journalDir string, p journal.ModelSubstitutionPayload) error {
	entry, err := journal.NewModelSubstitutionEntry(p)
	if err != nil {
		return fmt.Errorf("preflight: build model.substitution entry: %w", err)
	}
	w, err := journal.NewWriter(journal.WriterOpts{ClassDir: journalDir, Fsync: journal.FsyncPerBatch})
	if err != nil {
		return fmt.Errorf("preflight: open model.substitution writer: %w", err)
	}
	defer w.Close()
	if _, err := w.Append(entry); err != nil {
		return fmt.Errorf("preflight: append model.substitution: %w", err)
	}
	return nil
}

// modelSubstitutionJournalDir resolves the project journal class-dir for
// model.substitution events, matching emitSessionMetrics's
// filepath.Join(ContextDir(), "journal", "eval") convention.
func modelSubstitutionJournalDir(contextDir string) string {
	return filepath.Join(contextDir, "journal", "model")
}

// liveOpenRouterListModels is the production listModelsFn: an
// unauthenticated ListModels call against the real OpenRouter catalog
// (pkg/llm/openrouter.go). Split out so NewCortexSession's wiring reads as
// one line and tests never construct a real client.
func liveOpenRouterListModels(ctx context.Context) ([]llm.OpenRouterModel, error) {
	return llm.NewOpenRouterClient(nil).ListModels(ctx)
}
