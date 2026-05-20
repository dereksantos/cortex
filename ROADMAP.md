# Cortex Roadmap

**Last Updated:** 2026-05-20
**Status:** Experimental. Core pipeline working; harness reframe in progress.
**North Stars:** the three thesis claims in [`docs/eval-strategy.md`](docs/eval-strategy.md) —
multi-model leverage, learning over time, bounded emergence.

---

## Scope (locked)

Cortex is **a general-purpose coding harness that leverages multiple
models, learns over time, and has bounded emergence.** See
[`docs/eval-strategy.md`](docs/eval-strategy.md) for the full scope
definition and the three-tier eval framework that drives this roadmap.

The prior "context broker that plugs into Claude Code / Cursor" framing
is superseded. Cortex *is* the harness now, not a layer on top of one.

---

## Onboarding as the thesis surface

> "A team of agents and hardware can do a lot of work."

The first-launch onboarding flow is *not* a setup chore — it's the
product moment where the **multi-model leverage** thesis claim becomes
visible to the user. When Cortex starts for the first time it should:

1. **Detect available endpoints** in parallel — local Ollama, any
   OpenAI-compatible HTTP endpoint the user names (chatterbox /
   Lemonade / LM Studio / vLLM), cloud keys (Anthropic, OpenRouter).
2. **Catalog each endpoint's models** with capability tags (coding,
   tool-calling, embedding, reranking, reasoning) — using whatever
   labels the endpoint exposes (Lemonade does this natively).
3. **Recommend a role map** — `code`, `reason`, `fast`, `embed`,
   `rerank` → specific `endpoint/model` pairs — biased toward keeping
   work on the user's *strongest local* model unless cloud is cheaper.
4. **Show the routing decision and projected cost** before the user
   accepts. This is where the thesis becomes legible: "Coder-30B on
   chatterbox for code (free), qwen2.5:3b on local Ollama for fast
   tasks (free), Claude Haiku for ambiguous-intent clarification ($X
   per session)."
5. **Persist the map** to `~/.cortex/config.json` and re-validate at
   each REPL launch (tunnels go down, models get unloaded, machines
   power off).

Why this matters strategically: a working team of small-and-mid-sized
models orchestrated well is the visible counter-example to "use the
biggest frontier model for everything." Setup-time UX is the place
where that argument is made to the user. It also doubles as the
prerequisite substrate for the **multi-model cost/quality delta eval
(Tier 2c)** — the paired-run harness needs a routable model registry.

This belongs in [`docs/eval-strategy.md`](docs/eval-strategy.md) Tier 2c
as the product surface, and in Phase 4 as the prerequisite engineering
item. Implementation lands in Phase 4 below.

---

## What's Working

- Capture → store → retrieve → inject pipeline (used daily in
  development of Cortex itself)
- DAG executor + mechanic eval substrate (seed+grow+decay model)
- Multi-project support via global daemon and shared `~/.cortex/`
- Baseline eval suite wrapped: SWE-bench, LongMemEval, NIAH, MTEB
- v2 coding-scenario suite (40 scenarios) + library-service
  multi-session corpus
- Claude Code integration (hooks, slash commands, status line) —
  retained as one host the harness can drive

## What's Early or Aspirational

- **Learning-curve eval (Tier 2a):** scenario corpus exists
  (library-service, journeys); sequential-run driver + memory-isolation
  primitive not yet built
- **Budget–quality curve (Tier 2b):** DAG trace infrastructure landed;
  curve-plotting tooling not yet built
- **Multi-model cost/quality delta (Tier 2c):** paired-run harness not
  yet built; routing-policy ablation surface needs design
- **Regression guardrails (Tier 3):** none wrapped yet; intent-ingress,
  in-flight observability, presentation, MCP-extensibility proxies all
  on the build list
- **MCP server:** wired up but not validated against real external
  clients
- **Cursor integration:** design-only

---

## Eval strategy (single source of truth)

All eval work lives under the three-tier framework in
[`docs/eval-strategy.md`](docs/eval-strategy.md):

| Tier | Job | Metric of record |
|---|---|---|
| 1. Baseline | Prove competence on standard benchmarks | Pass-rate normalized by model size, dollars, wall-clock |
| 2. Thesis | Prove the three claims (multi-model, learning, bounded emergence) | Curves and deltas, not single numbers |
| 3. Regression | Catch silent UX-dimension degradation | Pass/fail thresholds, cheap, run weekly |

**ABR-as-ratio is retired.** The underlying question survives as the
budget–quality curve (Tier 2b). See the eval-strategy doc for the
reasoning.

Per-benchmark coverage tracking remains in
[`docs/benchmarks/coverage-matrix.md`](docs/benchmarks/coverage-matrix.md);
the matrix is now organized around which tier each dimension serves.

---

## Phases

Phase numbering carries forward from the historical roadmap; phases
1–2 are done.

### Phase 1: Consolidate Eval System DONE

Eval files merged, mocks removed, SQLite persistence.

### Phase 2: Simplify Scenarios DONE

Single YAML format: `context` + `tests`. 40 active scenarios in
`test/evals/v2/`.

### Phase 3: Eval reframe to three-tier strategy DONE

- [x] Author `docs/eval-strategy.md` (the three-tier framework)
- [x] Reframe CLAUDE.md and ROADMAP.md to locked scope
- [x] Update `docs/benchmarks/coverage-matrix.md` to map each dimension
      to a tier
- [x] Retire `internal/eval/legacy/` + `test/evals/legacy/` (superseded
      cognitive-mode abstraction)
- [x] Repurpose `internal/eval/journey/` for the learning-curve eval
      (substrate; the half-built executor is now the seed for the
      sequential-session runner in Phase 4)
- [x] Archive `docs/eval-prep-epic.md` (all 6 phases complete per its
      own closing note) — moved to `docs/archive/`
- [x] Triage `docs/eval.md`, `docs/integration-roadmap.md` — added
      "superseded by eval-strategy.md" headers
- [x] Triage `docs/archive/`, `docs/eval-journal.md`,
      `docs/simplification-audit.md` (archive README added,
      eval-journal rolloff policy added)

### Phase 4: Build Tier 2 (thesis evals) NEXT

- [ ] **Model registry + multi-endpoint detection (PREREQUISITE).**
      Gates the multi-model paired-run harness below. Detailed sub-items:
  - [ ] Extend `internal/llm/detect.go` to probe a configurable list of
        OpenAI-compatible endpoints in parallel (chatterbox / LM Studio /
        vLLM / Lemonade), alongside the existing Ollama + Anthropic
        probes.
  - [ ] Add a generic `pkg/llm/openai_compat.go` provider (clone of
        `openrouter.go` parameterized by `base_url` + optional API
        key). Used by all OpenAI-compatible local endpoints.
  - [ ] Tag each detected model with capabilities (coding,
        tool-calling, embedding, reranking, reasoning) from the
        endpoint's metadata when available; fall back to a static
        capability table in `pkg/models/` for endpoints that don't
        expose labels.
  - [ ] Add a `Models` role map to `pkg/config/config.go` —
        `code / reason / fast / embed / rerank → endpoint/model_id`.
        Persisted to `~/.cortex/config.json`.
  - [ ] Onboarding flow in `cortex install` / REPL first-launch:
        detect → recommend role map → show projected per-role costs →
        prompt to accept/edit → save. This is the **thesis surface**
        described above; it's UX work, not just plumbing.
  - [ ] Re-validate the saved map at each REPL launch (tunnel up,
        model resident, key still valid) and surface stale entries
        with a one-line "refresh?" prompt.
  - [ ] Swap-aware routing helper: given the role-map and the
        currently-loaded model on each endpoint, prefer routing that
        avoids a model swap when quality is comparable. Materializes
        the "batch by model" guidance from chatterbox doc §4.
- [ ] **Learning-curve runner** — sequential session execution with
      per-run memory isolation. Substrate: existing library-service +
      v2 corpora. Output: pass-rate-vs-session-index curve.
- [ ] **Budget–quality curve plotter** — runs the same task at varying
      DAG budgets, plots quality vs budget. Substrate: mechanic +
      dagtrace.
- [ ] **Multi-model paired-run harness** — `small alone` vs
      `small + Cortex` vs `frontier alone` vs `multi-model + Cortex`.
      Reports cost-quality Pareto frontier. Depends on the model
      registry above.

### Phase 5: Build Tier 3 (regression guardrails)

- [ ] Intent-ingress proxy (ambiguous-prompt corpus + LLM-judge)
- [ ] In-flight observability proxy (event-stream cadence/density/coverage) —
      depends on CLI `--events` surface landing first
- [ ] Presentation-judge proxy (end-of-turn summary vs actual diff)
- [ ] Coding-specific destructive-op corpus (Tier-3 safety)
- [ ] MCP-extensibility proxy (custom MCP server with un-memorized
      tools)

### Phase 6: Tier 1 expansion

- [ ] BFCL wrapper (execution breadth, tool-calling)
- [ ] τ-bench wrapper (planning, policy adherence)
- [ ] MINT wrapper (steering & interrupt — shares plumbing with τ-bench)
- [ ] AgentDojo wrapper (safety / prompt-injection)

CLI prerequisites (multi-turn session driver, MCP-server registration,
confirmation-gate flag) tracked in
[`docs/benchmarks/coverage-matrix.md`](docs/benchmarks/coverage-matrix.md)
Stage 1.

### Phase 7: End-to-end scenario

Single multi-session scenario touching all 10 dimensions, per-step
scored. Cannot start until Phases 4–6 land the substrate.

---

## Recently Completed

- [x] **Library-service multi-session eval** — scaffold, session
      runner, scorer, end-to-end probe (Plans 01–05)
- [x] **DAG protocol substrate** — mechanic runner + dagtrace
- [x] **Multi-project support** via single global daemon and shared
      `~/.cortex/`
- [x] **Composable status line** with compact format
- [x] **Per-mode cognitive tuning** via config
- [x] **Dream improvements**: fractal region sampling, novelty cache,
      follow-up queue
- [x] **Semantic search with embeddings** — nomic-embed-text
- [x] **Eval consolidation** — 23 files → 5 files
- [x] **Unified scenario format** — single YAML pattern
- [x] **CLICortex** for true E2E testing via CLI commands
- [x] **Claude Code integration** (hooks, slash commands, status line)

---

## Historical metric notes (for archival reasoning)

The prior ABR ≥ 0.9 north star produced a 0.586 baseline (43-scenario
v2 sweep, 2026-05-17) against a 0.77 figure measured 2025-12-30 under
the since-deleted `--cognition` runner (commit `1628173`). The 0.586
figure had known run-to-run variance (a same-day re-run scored 0.492).
That unreproducibility — plus the structural concerns documented in
[`docs/eval-strategy.md`](docs/eval-strategy.md) — is what motivated
the move from ABR-as-ratio to budget-quality-as-curve.

The canonical pre-DAG baseline snapshot in
[`docs/eval-baseline.md`](docs/eval-baseline.md) (pinned to SHA
`387468f`) remains valid as a historical reference, but isn't a target
under the new strategy.

---

*Living document. North stars: the three thesis claims in
[`docs/eval-strategy.md`](docs/eval-strategy.md).*
