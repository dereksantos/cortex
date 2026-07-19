// curated.go — the source-of-truth table of curated OpenRouter :free models
// (docs/completion-roadmap.md Track E2). Curated is the PRIMARY default:
// bootstrap's first-run config generation (bootstrap.go, bootstrap_persist.go)
// seeds both live roles (code, study — same model by default, per E1's role
// collapse) from curatedFreeModels[0]. Auto-discovery via ListModels is a
// FALLBACK layer, walked only when a curated pick turns out to be
// unavailable at session startup (preflight.go) — never the default path.
//
// Verified live against OpenRouter's /api/v1/models (unauthenticated) during
// this track's implementation: all five entries below were present,
// :free-suffixed, and reported the context length listed.
package main

// CuratedModel is one curated OpenRouter :free model: an id OpenRouter
// accepts verbatim, its served context window, and a one-line rationale for
// why it's on the list.
type CuratedModel struct {
	ID     string
	Window int
	Why    string
}

// curatedFreeModels is ordered by preference — index 0 is the top pick used
// for both live roles (code, study) by bootstrap's first-run config
// generation. The unavailability fallback (preflight.go) walks this same
// order looking for the first still-served entry before it ever falls
// through to open-ended discovery.
var curatedFreeModels = []CuratedModel{
	{
		ID:     "qwen/qwen3-coder:free",
		Window: 1048576,
		Why:    "purpose-built coder model with the largest free context of any candidate — headroom to keep the outline+recall working set whole across a long session.",
	},
	{
		ID:     "cohere/north-mini-code:free",
		Window: 256000,
		Why:    "coder-branded North model already validated in this codebase's own output-token sizing notes (main.go's codeMaxOutputTokens comment).",
	},
	{
		ID:     "tencent/hy3:free",
		Window: 262144,
		Why:    "the prior bootstrap default; general-purpose but a proven, capable tool-caller — kept as a fallback rather than dropped outright.",
	},
	{
		ID:     "qwen/qwen3-next-80b-a3b-instruct:free",
		Window: 262144,
		Why:    "large-context general instruct model; a solid fourth choice if the top three churn together.",
	},
	{
		ID:     "nvidia/nemotron-3-nano-omni-30b-a3b-reasoning:free",
		Window: 256000,
		Why:    "reasoning-branded, well matched to the Study subagent's bounded outline/grep/read loop.",
	},
}

// curatedTopPick returns the highest-preference curated model — bootstrap's
// first-run default for both code and study (E1: same model by default).
func curatedTopPick() CuratedModel { return curatedFreeModels[0] }

// isCuratedModel reports whether id names an entry in curatedFreeModels —
// the minimal "did this binding come from the curated table" marker the
// unavailability preflight (preflight.go) uses instead of a separate config
// flag: a bound model is "curated" exactly when it matches a table entry,
// whether that binding came from bootstrap's own write or a user pinning the
// same id by hand.
func isCuratedModel(id string) bool {
	for _, m := range curatedFreeModels {
		if m.ID == id {
			return true
		}
	}
	return false
}
