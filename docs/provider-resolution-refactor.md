# Provider Resolution Refactor

## Context

Cortex currently has **three** opinions about how to construct an LLM provider in the same binary:

1. **REPL** uses `buildLLMProviderForREPL(cfg, model, apiURL)` (private helper in `cmd/cortex/commands/repl.go:822`). 3-step resolver:
   - Phase 4 model registry: `cfg.ResolveModelRoute(model)` → if `chatterbox/X` or any configured prefix → OpenAI-compat to that endpoint.
   - Else, Ollama-shaped `apiURL` → `llm.NewLLMClient(BackendOllama)`.
   - Else, slash-prefixed model → `llm.NewOpenRouterClientWithKey(cfg, keychainKey)`.

2. **Bootstrap** (added recently then partly reverted) uses a parallel `buildBootstrapProvider(providerName, endpoint, modelID)` with three knobs `--provider | --endpoint | --model`. Bypasses Phase 4 entirely.

3. **Everyone else** (`query`, `embed`, `ingest`, `measure`, `daemon`, `debug`, `journal_replay`, `dream_debug`, `eval_benchmark`) hardcodes `llm.NewOllamaClient(cfg)`. Ignores any user model preference. Cannot reach chatterbox/Lemonade/OpenRouter at all.

This refactor unifies them around a single helper that treats **model id as the routing key**, which is what the REPL already does correctly.

## Locked decisions

- Helper lives at **`internal/llm/build.go`** (matches the existing `intllm` import convention; private to the project).
- `--endpoint` **survives as a transient override**. The persistent answer is `model_routes` in `.cortex/config.json`; the flag is for one-off ad-hoc invocations.
- **`--provider` is dropped.** Model id is the routing key (REPL pattern). Cleaner CLI, fewer overlapping knobs.
- **Migrate every caller of `llm.NewOllamaClient`** in `cmd/cortex/commands/`. Splits into generation vs embedding paths.

## Approach

### `internal/llm/build.go` (new file)

```go
// BuildProvider returns an llm.Provider whose backend is selected by
// model id, with optional transient overrides. Resolution order:
//
//   1. opts.endpointOverride (--endpoint) — wins when set
//   2. cfg.ResolveModelRoute(modelID) — Phase 4 model_routes
//   3. opts.apiURL Ollama-shaped — legacy REPL apiURL path
//   4. slash-prefixed modelID — OpenRouter with keychain key
//   5. bare modelID — Ollama default
//   6. nil if nothing matches (callers tolerate via mechanical fallback)
func BuildProvider(cfg *config.Config, modelID string, opts ...Option) llm.Provider

// BuildEmbedder mirrors BuildProvider for embedding-only callers
// (vector_search, capture pipeline). Today: Ollama nomic-embed by
// default; future Phase 4 entry can route to a hosted embedder.
func BuildEmbedder(cfg *config.Config, opts ...Option) llm.Embedder

// Option is the functional-options carrier.
type Option func(*buildOpts)

// WithEndpointOverride forces OpenAI-compat against the given URL,
// bypassing all other resolution steps. Used by --endpoint flag.
func WithEndpointOverride(url string) Option

// WithAPIURL preserves the REPL's legacy apiURL routing where a
// non-default URL signals OpenRouter. New code should prefer
// WithEndpointOverride.
func WithAPIURL(url string) Option

// WithModelOverride lets callers force a model id different from the
// one parsed for routing. Edge case; most callers won't need it.
func WithModelOverride(id string) Option
```

**Tests** (`internal/llm/build_test.go`):

| Case | Inputs | Expected backend |
|------|--------|------------------|
| Phase 4 prefix | `chatterbox/Qwen3-Coder-30B-...`, configured route | OpenAI-compat to chatterbox |
| Phase 4 role-map | `Qwen3-Coder-30B-A3B-Instruct-GGUF`, role map → chatterbox | OpenAI-compat to chatterbox |
| Endpoint override | model=anything, `WithEndpointOverride("http://x/v1")` | OpenAI-compat to `http://x/v1` |
| Endpoint over Phase 4 | model has Phase 4 route, `WithEndpointOverride` set | OpenAI-compat to override (Phase 4 ignored) |
| Slash-prefix OpenRouter | `anthropic/claude-haiku-4.5`, no route | OpenRouter (keychain) |
| Bare name Ollama | `qwen2.5-coder:7b`, no route | Ollama default URL |
| Empty model | `""`, no opts | Ollama default with cfg.OllamaModel |
| Nil cfg | nil cfg, bare model | Ollama, no panic |

### REPL migration

`buildLLMProviderForREPL` becomes a one-liner that delegates:

```go
// repl.go (replacement at line 822)
func buildLLMProviderForREPL(cfg *config.Config, model, apiURL string) llm.Provider {
    return intllm.BuildProvider(cfg, model, intllm.WithAPIURL(apiURL))
}
```

Kept as a thin wrapper rather than deleted outright so the 6 existing call sites in `repl.go` don't all churn at once. Once migration settles, inline + remove.

### Bootstrap migration

Restore the previously-added `--endpoint` flag via the helper. Drop `--provider`.

```go
// bootstrap.go Execute
provider := intllm.BuildProvider(
    ctx.Config,
    modelID,
    intllm.WithEndpointOverride(endpoint),
)
```

Delete `buildBootstrapProvider` entirely. Help text shrinks by one line.

### Generation-path migrations

These commands today call `llm.NewOllamaClient(cfg)` for actual LLM generation (analysis, classification, summarization). They should route through `BuildProvider`:

| File:line | Current behavior | Action |
|-----------|------------------|--------|
| `dream_debug.go:50` | `llm.NewOllamaClient(cfg)` for Dream's analyzeItem path | `BuildProvider(cfg, cfg.OllamaModel)` |
| `daemon.go:134` | Ollama for daemon's Think/Dream/Reflect | `BuildProvider(cfg, cfg.OllamaModel)` |
| `debug.go:263` | Ollama for analysis | `BuildProvider(cfg, cfg.OllamaModel)` |
| `debug.go:1037` | Ollama for analysis | same |
| `debug.go:1448` | Ollama (constructed config) | same |
| `journal_replay.go:231` | Ollama for replay LLM | `BuildProvider(cfg, modelOpt)` |
| `measure.go:316` | Ollama for self-eval | `BuildProvider(cfg, cfg.OllamaModel)` |
| `eval.go:364` | OpenRouter direct construct | `BuildProvider(cfg, modelID)` (model id picks OR) |
| `eval_benchmark.go:253` | OpenRouter w/ key | same |
| `setup.go:336` | Ollama for first-run probe | leave alone (intentionally Ollama-only — `cortex install` probes local availability) OR keep but tag as exception |

### Embedding-path migrations

These commands call `llm.NewOllamaClient(cfg)` to get an **Embedder** for vector search, not a generation provider. They should route through `BuildEmbedder`:

| File:line | Current behavior | Action |
|-----------|------------------|--------|
| `embed.go:241` | `llm.NewOllamaClient(cfg)` + `.IsEmbeddingAvailable()` | `BuildEmbedder(cfg)` |
| `reembed.go:71` | Ollama embedder passed to reembed routine | `BuildEmbedder(cfg)` |
| `query.go:116`, `:125` | Ollama for query embedding | `BuildEmbedder(cfg)` |
| `ingest.go:281`, `:397`, `:503` | Ollama for ingest's embedding pipeline | `BuildEmbedder(cfg)` |
| `repl.go:2809` | `llm.NewOpenRouterClient(nil)` — out-of-place | keep (used for status check); orthogonal to embedding |

The split is important: some Ollama callers want embedding (`nomic-embed-text`), some want generation (`qwen2.5-coder`). Conflating them into `BuildProvider` would degrade embedding quality.

### Behavior changes from the migration

Commands that were Ollama-only now follow model id routing. **This is intentional** — the unified resolver is the whole point — but it changes the answer when a user has configured a non-Ollama default. Specifically:

- A user with `chatterbox` registered in `model_routes` who sets the default model to a chatterbox-routed id will start having `dream`, `journal_replay`, `measure`, `daemon`'s background work hit chatterbox instead of Ollama. That's the desired behavior but should be called out.
- Migration audit: each migrated command needs a test or manual verification that the new resolution path doesn't silently swap providers for users who never wanted that.

### Defaults / config

`config.Config` already carries `OllamaModel` (`.cortex/config.json::ollama_model`) and `AnthropicModel` (`anthropic_model`). The helper uses these as the modelID when a caller passes empty:

```go
if modelID == "" {
    modelID = cfg.OllamaModel // existing field
}
```

Add a helper `cfg.DefaultGenerationModel()` that returns the right model id based on what's configured (Ollama if registered, else Anthropic via OpenRouter, else "").

## Critical files

**Touch sites (new + modified):**
- `internal/llm/build.go` *(new)* — BuildProvider, BuildEmbedder, Option
- `internal/llm/build_test.go` *(new)* — resolution-order table tests
- `internal/llm/doc.go` *(modify)* — package doc on the convention
- `pkg/config/config.go` *(modify if needed)* — add `DefaultGenerationModel()` helper
- `cmd/cortex/commands/repl.go` *(modify)* — `buildLLMProviderForREPL` becomes a one-liner delegate
- `cmd/cortex/commands/bootstrap.go` *(modify)* — drop `--provider`, restore `--endpoint`, delegate to `BuildProvider`
- `cmd/cortex/commands/dream_debug.go` *(modify)* — `BuildProvider`
- `cmd/cortex/commands/daemon.go` *(modify)* — `BuildProvider`
- `cmd/cortex/commands/debug.go` *(modify)* — `BuildProvider` (3 sites)
- `cmd/cortex/commands/journal_replay.go` *(modify)* — `BuildProvider`
- `cmd/cortex/commands/measure.go` *(modify)* — `BuildProvider`
- `cmd/cortex/commands/eval.go` *(modify)* — `BuildProvider`
- `cmd/cortex/commands/eval_benchmark.go` *(modify)* — `BuildProvider`
- `cmd/cortex/commands/embed.go` *(modify)* — `BuildEmbedder`
- `cmd/cortex/commands/reembed.go` *(modify)* — `BuildEmbedder`
- `cmd/cortex/commands/query.go` *(modify)* — `BuildEmbedder` (2 sites)
- `cmd/cortex/commands/ingest.go` *(modify)* — `BuildEmbedder` (3 sites)
- `tools.json` *(regenerate)* — `--provider` removed from bootstrap manifest

**Read-only references:**
- `pkg/config/config.go:111-130` — existing `ResolveModelRoute`
- `pkg/config/endpoints_test.go` — existing route-resolution tests (don't rewrite; mirror their conventions)
- `pkg/llm/openai_compat.go:75` — `NewOpenAICompatClient(EndpointConfig)`
- `pkg/llm/client.go` — `NewLLMClient`, backend constants
- `pkg/llm/openrouter.go` — `NewOpenRouterClientWithKey`
- `pkg/secret/secret.go` — `MustOpenRouterKey` (keychain access)

## Implementation sequence

Each phase independently reviewable and ships green.

1. **Land `internal/llm/build.go` + tests.** No callers yet. Resolution-order table covers all 8 cases. Build green.
2. **Migrate REPL.** `buildLLMProviderForREPL` becomes a one-liner. Existing REPL tests verify no behavior change. Manual smoke: bare `cortex` against chatterbox-routed model still works.
3. **Migrate bootstrap.** Restore `--endpoint`, drop `--provider`. Regenerate `tools.json`. Manual smoke: re-run the dogfood from this morning, confirm same behavior.
4. **Migrate generation-path callers** (one PR per file ideally, grouped here for plan size): `dream_debug`, `daemon`, `debug`, `journal_replay`, `measure`, `eval`, `eval_benchmark`. Each comes with a manual smoke test against the user's current default.
5. **Migrate embedding-path callers**: `embed`, `reembed`, `query`, `ingest`. Verify vector-search results don't regress (the embedding model is the same; only the construction path changes).
6. **Final sweep.** `grep -rn "llm.NewOllamaClient\|llm.NewOpenRouterClient" cmd/` should return only test files and the intentional `setup.go:336` first-run probe. Land a guard test that fails if a non-allowlisted file reintroduces direct construction.

## Verification

**Unit tests:**
- `internal/llm/build_test.go` — the 8-case resolution table.
- Per-command tests already exist for most migrated commands; rerun them.

**Manual smoke tests** (after each phase):
- REPL on chatterbox-routed model: `./cortex --model chatterbox/Qwen3-Coder-30B-A3B-Instruct-GGUF` — should hit Lemonade.
- Bootstrap on Cortex: `./cortex bootstrap --endpoint http://localhost:13305/v1 --model Qwen3-Coder-30B-A3B-Instruct-GGUF --target-coverage 0.05 --budget 10` — should match the dogfood run.
- Dream debug: `./cortex dream-debug` — should still produce insights.
- Embedding: `./cortex embed "test query"` — should return a vector via nomic-embed.
- Search: `./cortex search "auth"` — should embed and return results.

**Guard test:**
- A test in `cmd/cortex/commands/` that walks the package and fails if any non-allowlisted file imports `llm.NewOllamaClient` directly. Mirrors the existing manifest-up-to-date test pattern.

## Risks / gotchas

1. **Silent provider swap.** A user who had a `model_routes` entry but never set a default model could see their `dream` / `measure` work shift from Ollama to a hosted endpoint after migration. **Mitigation:** the helper falls back to `cfg.OllamaModel` when no model is specified, preserving Ollama as the implicit default. Verify with a config that has both Phase 4 routes + an Ollama default.

2. **Embedding vs generation confusion.** Some `NewOllamaClient` callers use both `.Generate*` and `.Embed`. Need to inspect each carefully — if a single client serves both, migrating to `BuildEmbedder` could break the generation path or vice versa. **Mitigation:** per-file audit in phase 4/5 with a paired manual smoke.

3. **`setup.go:336`'s first-run probe is genuinely Ollama-only.** It's checking local LLM availability for the install flow, not picking a runtime provider. **Action:** mark as an exception in the guard test allowlist with a comment explaining why.

4. **`OpenRouter`-direct callers** (`eval.go:364`, `eval_benchmark.go:253`, `repl.go:2809`) construct OpenRouter clients with a keychain key, not via model id. If the model id heuristic doesn't always route slash-prefixed names to OpenRouter, these could break. **Mitigation:** the helper's slash-prefix → OpenRouter step is exactly equivalent to what these do today; the migration is a no-op for them in steady state.

5. **`tools.json` drift.** Dropping `--provider` from bootstrap changes the manifest. CI runs `TestToolsJSONUpToDate` (already burned us once). **Action:** regenerate `tools.json` in the same PR as the bootstrap migration.

## Out of scope

- Adding new providers (e.g., direct Anthropic) — orthogonal.
- The Phase 4 model registry itself — already exists in `pkg/config/config.go`. We're just teaching more callers to use it.
- A `model_routes` config UI / `cortex routes add` subcommand — would be nice; not blocking.

## Open follow-ups (after this refactor lands)

- Audit whether any `internal/cognition/*` packages also construct providers directly. If so, lift them into `BuildProvider` too.
- Consider whether `BuildProvider` should cache by `(modelID, options)` to avoid re-keychain-lookups per call (cheap, but if measure shows it matters, add).
- Document `model_routes` config more prominently in `docs/repl.md` and a new `docs/providers.md`.
