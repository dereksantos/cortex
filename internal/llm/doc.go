// Package llm hosts internal LLM utilities for the cortex CLI:
//
//   - BuildProvider / BuildEmbedder (build.go): the unified
//     provider+embedder construction surface. Every cmd/cortex/commands
//     caller goes through these so model id remains the single source of
//     truth for routing.
//   - DetectLLM (detect.go): first-run probe that reports which local /
//     hosted LLMs are reachable.
//   - RevalidateRoleMap (revalidate.go): keeps Phase 4 model_routes
//     coherent when endpoints come and go.
//
// # Routing convention
//
// BuildProvider treats the model id as the routing key. Resolution
// order, highest priority first:
//
//  1. WithEndpointOverride — explicit --endpoint flag wins over
//     everything; constructs an OpenAI-compat client pointed at the
//     given URL.
//  2. cfg.ResolveModelRoute(modelID) — Phase 4 model_routes from
//     .cortex/config.json. Lets "chatterbox/Qwen3-Coder-…" or any
//     prefix-registered endpoint route to a local OpenAI-compat server
//     (Lemonade, LM Studio, vLLM).
//  3. WithAPIURL — legacy REPL apiURL behavior. Ollama-shaped URLs
//     route to Ollama; everything else routes to OpenRouter with the
//     apiURL applied. Preserved for the REPL's /model swap UX; new
//     callers should prefer WithEndpointOverride.
//  4. Slash-prefixed model id (e.g. "anthropic/claude-haiku-4.5")
//     with no Phase 4 match — OpenRouter via the keychain key.
//  5. Bare model id — Ollama default URL.
//
// BuildEmbedder is intentionally simpler: today it always returns an
// Ollama nomic-embed-text client. A future Phase 4 hook can route
// embedding through cfg.Models.Embed without changing call sites.
//
// # Guard
//
// cmd/cortex/commands/provider_resolution_guard_test.go fails CI if a
// non-allowlisted file in that package reintroduces a direct
// llm.NewOllamaClient or llm.NewOpenRouterClient call. Allowlist
// markers are line comments of the form
//
//	allowlist:llm.NewOllamaClient
//
// within ~10 lines preceding the construction. The three current
// exceptions are: first-run install probe (setup.go), checkOllama
// status probe (debug.go), and OpenRouter catalog discovery (repl.go).
//
// See docs/provider-resolution-refactor.md for the migration history
// and the rationale behind each locked decision.
package llm
