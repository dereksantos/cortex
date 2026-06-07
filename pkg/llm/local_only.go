package llm

import "os"

// LocalOnlyEnv, when truthy, forces a session to use only local models —
// no OpenRouter / remote endpoints in the registry, and no routing pins
// that target a remote model. Set by `eval ... --local-only`; the env
// var is the transport that survives the eval's per-cell subprocess
// boundary (same pattern as CORTEX_TEMPERATURE).
//
// Motivation: with a remote pin (e.g. decide.next → openai/gpt-5.4) and
// the keychain OpenRouter key in the registry, capability routing sends
// nodes like sense.estimate_scope to a remote frontier model. When that
// path degrades, the whole suite collapses (budget_tokens=0, killed
// cells) — a local-only eval must never be hostage to remote availability.
const LocalOnlyEnv = "CORTEX_LOCAL_ONLY"

// LocalOnly reports whether CORTEX_LOCAL_ONLY is set to a truthy value.
func LocalOnly() bool {
	switch os.Getenv(LocalOnlyEnv) {
	case "1", "true", "TRUE", "yes", "on":
		return true
	}
	return false
}
