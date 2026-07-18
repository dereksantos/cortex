# Completion roadmap — PLAN (2026-07-18)

> Status: **LIVE PLAN.** The implementation goal for bringing Cortex to
> completion, designed from the 2026-07-18 completion audit (three-way:
> authoritative docs vs code, secondary-doc sweep, repo health). Baseline at
> design time: `docs/memory-tools.md` P1–P4 fully implemented; web track
> M1–M6 merged (`9816235`); engine/study refactor and thinking-models P1–P6
> on the main line; full gate green; branch `Cortex` backed up to the
> chatterbox remote.
>
> "Completion" here means three things, in order: the docs tell the truth,
> the core thesis is *measured* (not just built), and the remaining
> checklist features land. Explicitly **out of scope** (unchanged gates):
> Think/Dream/Reflect (blocked — no product surface named,
> `docs/think-dream-eval.md`), the Curate concept (parked), semantic
> `memory_search` (evidence-gated), fine-tuning.

## Track A — Truth reconciliation (first; ~a day; low risk)

The audit found the code moved past several docs. Fix the record before
building on it.

- [ ] A1. Document `cortex serve` + the web UI: add to README's command
      reference and CLAUDE.md's command table (update
      `readme_test.go` TestReadmeSurface accordingly). Align the env-var
      lists between README and CLAUDE.md (`CORTEX_LOOP_STREAM` vs
      `CORTEX_LOOP_STUDY_WINDOW`/`CORTEX_STUDY_REPS` drift).
- [ ] A2. Fix the stale checklist in `cmd/cortex/main.go:22-48`: Tier-2
      distillation and Fast/Reflex retrieval are marked `[x]` but were
      *removed* by the memory-tools pivot — annotate as removed, and point
      the checklist's remaining `[ ]` items at this roadmap.
- [ ] A3. `docs/memory-tools.md` corrections: `memory_forget` is a hard
      delete (idempotent), not an "append-only reversible retraction
      marker"; tool code lives in `internal/tools/tools.go` +
      `cmd/cortex/tool_deps.go`, not `cmd/cortex/tools/`.
- [ ] A4. Prune/mark the stale lower half of
      `docs/context-window-modification-tools.md` (the §5/§7/§8 plan text
      that predates the SHIPPED banner and contradicts it).
- [ ] A5. Dead code: delete `pkg/cliout` (zero importers); delete the
      obsolete `transport.go:19` rename-TODO; delete local branch
      `loop/wire-cognition-fleet` (fully merged); dedupe the doubled
      `/cortex` entry in `.gitignore`.
- [ ] A6. CI: add `Cortex` to `.github/workflows/test.yml` branch triggers
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

- [ ] B1. Wire-gate fix (prereq): config-disabling a context tool refuses
      at dispatch but leaves the tool declaration on the wire
      (`IsToolEnabled` / `session_core.go`). Gates must strip declarations
      too — ARM-OFF in B4 depends on it.
- [ ] B2. Context-pivot Δ eval: build
      `cmd/cortex/context_pivot_eval_test.go` per
      `docs/eval-context-pivot.md` (composed-migration invariants;
      offline, deterministic, expected green).
- [ ] B3. Eval 6b — the cold-vs-warm learning-loop runner (the unmeasured
      raison d'être: does accumulated memory make the agent better?).
      Design first per the harness doc, then build: two-arm run of the
      same task set — cold = fresh `.cortex/`, warm = seeded memory notes
      + journal from prior sessions; emit `eval.cell_result` rows with
      `ContextStrategy=cold|warm`; reuse the `memory_e2e_live_test.go`
      harness patterns; gate live runs behind `CORTEX_LIVE_FLEET=1`.
      Probe-before-long-run applies (minutes-scale sanity rep first).
- [ ] B4. ø context-pivot live eval: the L0–L3 volition ladder + ARM-OFF
      two-arm gate run (n≥3), per the doc's build order — only after B2 is
      green. This also answers the open empirical question: the shipped
      `context_*` tools have never been observed firing live.
- [ ] B5. Thinking-models fleet probe: sanity-check the level→budget tier
      strawman (low=1k/med=4k/high=16k); verify `fast`-role sub-calls pin
      effort off; only after the probe, decide on escalation defaults
      (`docs/thinking-models.md` open decisions).
- [ ] B6. (Stretch) Retention-QA eval from `docs/context-architecture.md`
      — the eval that gates the outline zone becoming default. The
      100-turn prefill slope stays a manual run.

**Gate B:** Δ suites green offline in CI; live gates recorded with
receipts (cell_results + a short eval-journal entry per run). Known
external risks, not blockers to building: LiteLLM peg-500 on tool-call
topics, fleet slot contention (`docs/refactor-status.md`).

## Track C — Completion features (checklist debt)

- [ ] C1. `cortex model` — catalog + suggest model/role setups from system
      resources (`cmd/cortex/main.go:45`), building on the existing
      `pkg/llm/recommend.go` recommender.
- [ ] C2. Code-model learned-window calibration: extend `learnedWindows`
      beyond the study model (`cmd/cortex/tool_deps.go:39`) so the code
      role self-calibrates on overflow; compaction remains the fallback.
- [ ] C3. Study-eval telemetry to the journal sink: emit `study.result`
      rows sharing the `EvalCellResultPayload` vocabulary alongside the
      current stdout JSONL (the deferred wiring in
      `docs/study-subagent.md` §5).

**Gate C:** README/CLAUDE.md updated for `cortex model`; suite green;
study-eval rows visible via jq over the journal.

## Track D — Structural cleanup (largest; last; parallelizable after B)

- [ ] D1. Config consolidation: retire `pkg/config`'s dormant trees
      (`Modes`, `Routing`, `EnableGraph`, `EnableVector`, `WebPort`,
      `DatabaseURL`); migrate the four remaining importers
      (`session_runtime.go`, `internal/capture`, `internal/storage`,
      `pkg/llm`) onto the live layered config or a trimmed core, ending
      the two-config-systems split.
- [ ] D2. Extract the serve/webui surface (~50 files of
      `cmd/cortex`'s 123) into an internal package; `cmd/cortex` stays the
      composition root. Behavior-preserving; REPL byte-identical.
- [ ] D3. Web Phase 7 — Discord parity (`docs/cortex-web.md`, decision
      D14's gate has now effectively fired): rebase `discord.go` onto the
      Phase-4 SessionManager, native application commands, interactive
      risk approval (Risky prompts instead of headless-Blocked), progress
      as message edits + interrupt. Do this after D2 so it rebases onto
      the extracted package once, not twice.

**Gate D:** full gate green; a browser-verified serve session and a live
Discord round-trip after D3.

## Sequencing

A → B are strictly ordered (truth base, then proof). C and D can proceed
in parallel once B1–B3 are underway; D3 lands last. Big mechanical tracks
(D1/D2) are candidates for the loop harness / a ralph loop; B needs
hands-on design.

## Definition of done

All four gates green; no doc contradicts the code; the cold-vs-warm and
context-pivot numbers exist in the journal with receipts; the remaining
`main.go` checklist items are either done or explicitly retired here.
