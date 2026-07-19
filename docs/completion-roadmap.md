# Completion roadmap — PLAN (2026-07-18)

> Status: **LIVE PLAN.** The implementation goal for bringing Cortex to
> completion, designed from the 2026-07-18 completion audit (three-way:
> authoritative docs vs code, secondary-doc sweep, repo health). Baseline at
> design time: `docs/memory-tools.md` P1–P4 fully implemented; web track
> M1–M6 merged (`9816235`); engine/study refactor and thinking-models P1–P6
> on the main line; full gate green; branch `Cortex` backed up to the
> chatterbox remote.
>
> Amended same day with findings from Derek's own cortex analysis sessions
> (`.cortex/sessions/20260717-003116` role/config launch pass,
> `20260717-004933` long-horizon eval pass) — see Tracks B and E.
>
> "Completion" here means four things, in order: the docs tell the truth,
> the core thesis is *measured* (not just built), the remaining
> checklist features land, and the harness is launch-ready for outside
> users. Explicitly **out of scope** (unchanged gates):
> Think/Dream/Reflect (blocked — no product surface named,
> `docs/think-dream-eval.md`), the Curate concept (parked), semantic
> `memory_search` (evidence-gated), fine-tuning.

## Track A — Truth reconciliation (first; ~a day; low risk)

The audit found the code moved past several docs. Fix the record before
building on it.

- [x] A1. Document `cortex serve` + the web UI: add to README's command
      reference and CLAUDE.md's command table (update
      `readme_test.go` TestReadmeSurface accordingly). Align the env-var
      lists between README and CLAUDE.md (`CORTEX_LOOP_STREAM` vs
      `CORTEX_LOOP_STUDY_WINDOW`/`CORTEX_STUDY_REPS` drift).
- [x] A2. Fix the stale checklist in `cmd/cortex/main.go:22-48`: Tier-2
      distillation and Fast/Reflex retrieval are marked `[x]` but were
      *removed* by the memory-tools pivot — annotate as removed, and point
      the checklist's remaining `[ ]` items at this roadmap.
- [x] A3. `docs/memory-tools.md` corrections: `memory_forget` is a hard
      delete (idempotent), not an "append-only reversible retraction
      marker"; tool code lives in `internal/tools/tools.go` +
      `cmd/cortex/tool_deps.go`, not `cmd/cortex/tools/`.
- [x] A4. Prune/mark the stale lower half of
      `docs/context-window-modification-tools.md` (the §5/§7/§8 plan text
      that predates the SHIPPED banner and contradicts it). Also update
      `docs/eval-context-pivot.md`'s central prior — "the tools have never
      been observed firing live" (2026-07-11) is falsified: sessions
      2026-06-11→07-17 show `recall`×1324, `context_merge`×499,
      `context_evict`×435, `context_adjust_watermarks`×462, plus
      `coding.context_rewrite`×328 in the journal.
- [x] A5. Dead code: delete `pkg/cliout` (zero importers); delete the
      obsolete `transport.go:19` rename-TODO; delete local branch
      `loop/wire-cognition-fleet` (fully merged); dedupe the doubled
      `/cortex` entry in `.gitignore`.
- [x] A6. CI: add `Cortex` to `.github/workflows/test.yml` branch triggers
      (takes effect whenever the branch reaches GitHub; harmless
      otherwise) and wire `scripts/verify-study.sh` in, per
      `docs/engine-unification.md`'s pending "wire into CI" note.

**Gate A:** `./scripts/check.sh all` + full suite green; TestReadmeSurface
covers `serve`; no remaining doc claim contradicted by code from the audit
list above.

## Track B — Proof-loop evals (the core of completion)

Everything is built; almost nothing is *measured*. This track turns the
thesis into numbers. Frame all outcomes as measurements to run — no
performance claims until receipts exist.

- [x] B1. Wire-gate fix (prereq): config-disabling a context tool refuses
      at dispatch but leaves the tool declaration on the wire
      (`IsToolEnabled` / `session_core.go`). Gates must strip declarations
      too — ARM-OFF in B4 depends on it.
- [x] B2. Context-pivot **live benefit eval first** (priority reordered by
      the 2026-07-17 session evidence): the ø volition question is
      effectively answered — the tools fire live — so the open question is
      the *benefit arm*: does curation causally improve retention? Build
      `cmd/cortex/context_pivot_eval_live_test.go` (gated
      `CORTEX_LIVE_FLEET=1`) reusing `context_eval_live_test.go`'s helpers
      (`fillerInput`, `factInput`, `recallCalledSince`, `codewordish`,
      `cacheOutlineText`): three needles (A-dead, A-keep, B), pivot turn,
      ARM-ON vs ARM-OFF where the control arm strips the three context-tool
      declarations from `Request.Tools` directly (honest control even
      before B1 lands); report the L0–L3 conversion rung + retention grades
      per model. The offline Δ layer
      (`context_pivot_eval_test.go`) follows as the deterministic guard.
- [x] B3. Eval 6b — the cold-vs-warm learning-loop runner (the unmeasured
      raison d'être: does accumulated memory make the agent better?).
      Design first per the harness doc, then build: two-arm run of the
      same task set — cold = fresh `.cortex/`, warm = seeded memory notes
      + journal from prior sessions; emit `eval.cell_result` rows with
      `ContextStrategy=cold|warm`; reuse the `memory_e2e_live_test.go`
      harness patterns; gate live runs behind `CORTEX_LIVE_FLEET=1`.
      Probe-before-long-run applies (minutes-scale sanity rep first).
- [x] B4. ø context-pivot gate run at n≥3 on the live fleet once B2's
      scenario is stable, recording receipts in the journal.
- [x] B5. Thinking-models fleet probe: sanity-check the level→budget tier
      strawman (low=1k/med=4k/high=16k); verify `fast`-role sub-calls pin
      effort off; only after the probe, decide on escalation defaults
      (`docs/thinking-models.md` open decisions).
- [ ] B7. (Found during B2, 2026-07-18) Resume-revert contradiction:
      `context_evict/merge/adjust_watermarks` mutations do NOT revert on
      session resume — `writeSessionState`/`restoreSessionState`
      (`cmd/cortex/session.go`) snapshots the outline + watermarks per
      turn and replays the snapshot verbatim — contradicting
      `docs/context-window-modification-tools.md` ("session-local …
      revert on resume"), the `internal/tools/context.go` docstrings, and
      CLAUDE.md. Decision needed: persist-by-design (fix the three docs)
      or revert-by-policy (replay demotion policy instead of the
      snapshot). Evidence: a resume-revert probe test failed exactly this
      way during B2 (removed from the deliverable as out of scope).
- [ ] B6. (Stretch) Retention-QA eval from `docs/context-architecture.md`
      — the eval that gates the outline zone becoming default. The
      100-turn prefill slope stays a manual run.

**Gate B:** Δ suites green offline in CI; live gates recorded with
receipts (cell_results + a short eval-journal entry per run). Known
external risks, not blockers to building: LiteLLM peg-500 on tool-call
topics, fleet slot contention (`docs/refactor-status.md`).

## Track C — Completion features (checklist debt)

- [x] C1. `cortex model` — catalog + suggest model/role setups from system
      resources (`cmd/cortex/main.go:45`). Decision first: `pkg/llm`'s
      `Recommend()` engine is currently dead code (nothing calls it, and it
      defines a second parallel `Role` type — part of the role sprawl, per
      the 2026-07-17 session) — either wire it as this command's engine or
      delete it and build lean; don't leave it dormant.
- [x] C2. Code-model learned-window calibration: extend `learnedWindows`
      beyond the study model (`cmd/cortex/tool_deps.go:39`) so the code
      role self-calibrates on overflow; compaction remains the fallback.
- [x] C3. Study-eval telemetry to the journal sink: emit `study.result`
      rows sharing the `EvalCellResultPayload` vocabulary alongside the
      current stdout JSONL (the deferred wiring in
      `docs/study-subagent.md` §5).

**Gate C:** README/CLAUDE.md updated for `cortex model`; suite green;
study-eval rows visible via jq over the journal.

## Track D — Structural cleanup (largest; last; parallelizable after B)

- [x] D1. Config consolidation: retire `pkg/config`'s dormant trees
      (`Modes`, `Routing`, `EnableGraph`, `EnableVector`, `WebPort`,
      `DatabaseURL`); migrate the four remaining importers
      (`session_runtime.go`, `internal/capture`, `internal/storage`,
      `pkg/llm`) onto the live layered config or a trimmed core, ending
      the two-config-systems split.
- [x] D2. **Audited 2026-07-18: NO MOVE — verdict recorded, move
      re-scoped.** A compiler-driven dependency audit (scratch-package
      compile of all 49 serve*/webui* files) showed the extraction is not
      one clean slice: the handlers reach six shared package-main
      subsystems (config, scan, workspace, session-listing, change
      status, loop firing) that are themselves not importable, so a move
      today means either relocating those first or duplicating ~5 DTOs +
      ~20 wrappers (the half-broken outcome). Prerequisite ladder for a
      future pass, in order: (1) Session/SessionFactory interfaces in
      internal/serve satisfied by *CortexSession; (2) relocate config.go,
      scan.go, workspace.go, configwrite.go + the data half of session.go
      into internal packages (independently low-risk, no CortexSession
      dependency); (3) RunLoopFiring/changeStatusFor follow; (4) then the
      serve/webui move is mechanical. Steps 1–4 are OPTIONAL post-
      completion architecture work, not launch-blocking.
- [ ] D3. Web Phase 7 — Discord parity (`docs/cortex-web.md`, decision
      D14's gate has now effectively fired): rebase `discord.go` onto the
      Phase-4 SessionManager, native application commands, interactive
      risk approval (Risky prompts instead of headless-Blocked), progress
      as message edits + interrupt. (The D2 audit confirmed this does not
      depend on the extraction — all in package main.)

**Gate D:** full gate green; a browser-verified serve session and a live
Discord round-trip after D3.

## Track E — Launch readiness (Derek's stated end-goal, 2026-07-17 session)

The 00:31 session's goal: "get this project live and publicly ready as a
coding harness that can be used reliably." Its analysis (grounded in
code): `cmd/cortex/config.go` defines **8 roles** (`code`, `hard-code`,
`reason`, `fast`, `study`, `embed`, `rerank`, `tools`) but only
`code`+`study` are on live paths; `discoverFleet` reads a LiteLLM
`/model/info` endpoint OpenRouter doesn't serve, so raw-OpenRouter users
must pin models by hand. The session's recommendation (two forks left
unconfirmed there; adopted here):

- [x] E1. Role collapse: the configurable surface becomes `code`+`study`
      defined as *agent roles* under an `agent`-shaped config (same model
      by default); the other six roles are removed or demoted to
      internal/reserved. Lands together with D1 (they touch the same
      config seams).
- [ ] E2. Curated OpenRouter free-model default fleet (fork A — curated
      list shipped in config/docs): works without a LiteLLM proxy,
      zero-cost first run. **Decision (2026-07-18):** curated is primary;
      auto-discovery is a *fallback layer on top*, not the default — when
      a curated model is unavailable (404/410/rate-limited at first use),
      fall back to discovering `:free` models via OpenRouter's
      `/api/v1/models` (the existing `pkg/llm/openrouter.go ListModels`
      path) and pick a substitute; surface the substitution to the user
      and journal it. Deterministic first, adaptive on failure.
- [ ] E3. Invariant pruning + a configuration doc: one page that tells an
      outside user exactly what to set (backend, key, two roles) and what
      every `tools.*` gate does.
- [ ] E4. Release surface: versioned build, install instructions
      (`go install` at minimum), and a first-run smoke path (`cortex` →
      bootstrap → one green turn on the free fleet).

**Gate E:** a fresh machine + an OpenRouter key reaches a green first
turn with no hand-edited config beyond the key; README quickstart matches
reality.

## Sequencing

A → B are strictly ordered (truth base, then proof). C, D, and E can
proceed in parallel once B1–B3 are underway; E1 lands with D1; D3 lands
last. Big mechanical tracks (D1/D2) are candidates for the loop harness /
a ralph loop; B and E1/E2 need hands-on design.

## Definition of done

All five gates green; no doc contradicts the code; the cold-vs-warm and
context-pivot numbers exist in the journal with receipts; the remaining
`main.go` checklist items are either done or explicitly retired here; a
stranger with an OpenRouter key can install, bootstrap, and complete a
green turn.
