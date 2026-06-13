# Handoff — 2026-06-12: fleet bring-up, study maturation, DAG-harness fault tree

Three evenings of work (June 9–12) on branch `derek.s/self-improvement-loop`
(~20 commits, `7e794db..41b5184`). All measured results are in
`docs/eval-journal.md` (entries dated 2026-06-10 through 2026-06-11); this doc
is the state map and the work queue, not the data.

## What landed (done, tested, committed)

**Fleet config (new chatterbox fleet: Qwen3.6-35B coder / coder80 / Gemma 4 reasoner)**
- `chat_template_kwargs` per model (pkg/llm + pkg/config + cmd/loop): thinking
  suppressed for coder + reasoner everywhere. Thinking-on starves bounded calls
  (reasoner burned full max_tokens on reasoning_content, returned empty).
- `model_context_overrides` per model (probe + config); per-endpoint HTTP
  `Timeout` (study uses 10m — full-budget prefill exceeds the 300s default).
- NOTE: `.cortex/config.json` is gitignored — the fleet config (aliases,
  capabilities, kwargs, windows) lives only on this machine.

**Study (the week's centerpiece — usable and trusted in cmd/loop)**
- Tier 1.5 boundary layer (`internal/study/boundary.go`): per-format coherence
  units (code 3072B measured / prose 1024B / data 2048B), boundary-snap of
  fragment leading edges, zero-knob auto density (k = budget/unit).
- Citation pipeline: union-of-ranges validation (+2-line gap tolerance),
  completion-cap floor (`studyCompletionCap`, kills the recurring 1024-token
  JSON truncation), numbered snippets for code+data (prose stays bare),
  verbatim-relay validation (`citationRelayed`) for digest-of-digests chains.
- Measured grounding at the operating point: code 100% (n=10), prose 100%,
  record data 100%. The n=10 2×2 grid REVISED an earlier claim: coordinates
  (line numbers) dominate granularity for citation accuracy; granularity buys
  latency + breadth.
- Hierarchy receipt: 24 files (~2.5MB) → 50.7KB L0 digests → L1 study at 11%
  sample → correct 4-subsystem map with 4 relay citations chained to source.
  Scripts: /tmp/cortex-recursion-exp.sh; knob: CORTEX_LOOP_STUDY_WINDOW.
- The other session landed `studyDir` (corpus studies) + per-region numbering;
  live-validated in a real loop session (transcript
  `.cortex/sessions/20260612-003737.jsonl` — study + go test/vet/gofmt
  composed into a grounded code-quality review).
- Loop bash tool now rejects shell metacharacters instructively (was: `|`
  reached find as a literal arg; model retried verbatim 3 turns).

**DAG harness (`cortex code` path) repairs**
- Fenced-JSON tool-call salvage (`pkg/llm/json_tool_calls.go`,
  `RecoverToolCalls` in the loop): Qwen3.6 emits tool calls as fenced JSON
  text; this caused 17/44 fixtures to do ZERO file reads. Probe-verified.
- CORTEX_STUDY_FILE gate repaired: planner op surface follows the gate,
  `act.study_file` counts as a read in codebase-eval metrics, and a
  dispatch-only registry alias maps habitual `read_file` calls onto study.

## Macro verdict (read-vs-study A/B, 2026-06-11)

Wash on the headline (A 15/39 valid, B 15/35 valid), run stamped COMPROMISED
(9 invalid cells). Study is macro-NEUTRAL on this suite: where it samples it
neither lifts nor sinks; the binding failures are in the coding model's
synthesis, not in how files are read. Two structural notes for future runs:
- The suite's fixture files are mostly too small to exercise study (pass-through).
- Study latency blows the 600s fixture cap on large-file fixtures; the 30m
  retry rescued only one cell — the rest fail on the items below.

## The fault tree for the remaining failures (NEXT WORK, in order)

1. **~~`/v1` URL bug~~ → FIXED 2026-06-12, diagnosis revised.** The missing
   `/v1` was a red herring (LiteLLM serves both paths — probed 200/200).
   Real cause: `pkg/llm/provider_factory.go` built endpoint clients without
   `ChatTemplateKwargs`, so the Router-resolved reasoner ran with thinking
   ON and blew the 30s classifier deadline (37.8s vs 1.6s, curl-measured).
   Factory now threads the kwargs; `NewOpenAICompatClient` also normalizes
   bare-root base_urls to `/v1`. Probe-verified: decide.next 13.5s, real
   2-node plan, no fallback. Full entry in eval-journal 2026-06-12.
2. **~~No-progress guard~~ → FIXED 2026-06-12, but it was NOT q2's blocker.**
   Rewrote the guard (`internal/harness/loop.go`) to intent-agnostic
   novelty: progress = write/shell OR a new `(tool, args)` signature;
   fires only on genuine repetition or the hard caps. Dropped the
   `Loop.Intent` plumbing (and `CortexHarness.SetIntent`) — no longer
   depends on the classifier. 12 tracker unit tests rewritten.
   BUT: with item 1's classifier fixed, q2-cross-file-cortex still emits
   empty synthesis from a DIFFERENT cause (the guard never engages — the
   model stops after 2 reads with a NEED_MORE). Real cause = scope
   under-budget (20K tokens for a question needing repl.go @ 35 chunks)
   → coding_turn blows budget (`budget_after_tokens=-6410`) → hop-2
   NEED_MORE can't schedule → synth-mode stripped the NEED_MORE line →
   empty Final. The handoff's "no_progress, 93K burned" fit the OLD
   broken-classifier runs. See eval-journal 2026-06-12 + new items below.
3. **Model selection — the coder80 probe.** The 35B was picked on inference
   benchmarks; harness fitness was never measured. coder80 is Coder-family
   (likely native tool_calls). Probe slice:
   `cortex eval codebase --only r3-symbol-in-large-file-rust-weather --only q1-pinpoint-cortex --only b2-termination-cortex -m coder80 ...`
   Compare vs the same slice on coder before any full run (probe-first).
4. **May-31 baseline diff** (`--compare`, baselines in
   `.cortex/db/eval_baselines/`) to split judge-change effects from
   coder-change effects in the 64%→38% drop. Old fleet's models are gone, so
   this is forensic, not re-runnable.

## New fault-tree items (found 2026-06-12 while closing item 2)

5. **~~Empty Final when a hop can't schedule~~ → FIXED 2026-06-12 (unit-level).**
   `finalizeSynthFinal` (coding_turn.go) substitutes an honest non-empty
   fallback when synth-mode stripping of a marker-only Final yields empty;
   cap-hit path fixed too. Unit-verified (`TestFinalizeSynthFinal`). NOT
   proven live: q2 went 3/3 PASS afterward but all via direct-answer
   (`need_more=0`) — local-backend variance at the answer↔NEED_MORE
   boundary, not the fallback path (which item 5 can't influence). Item 5
   bounds the worst case to a valid FAIL; item 6 is what makes q2
   *reliably* pass. See eval-journal 2026-06-12.
6. **~~estimate_scope under-budgets large-file Qs~~ → token axis FIXED 2026-06-13.**
   `referencedFileFloor` (repl.go) floors the turn's token budget at
   `8000 + Σ(named-file bytes/4)`, clamped to 200K. Verified: q2 budget
   20K → ~60K, `budget_after_tokens` −6410 → +33765. Deterministic, no
   LLM dep, pinpoint questions unaffected. Did NOT add a latency floor
   (would trade a valid fallback-FAIL for a slow timeout-INVALID). See
   eval-journal 2026-06-13.

7. **Synthesizer latency on large injected context (NEW, now q2's blocker).**
   With tokens fixed, the NEED_MORE→hop-2 path dead-ends on LATENCY:
   the coder synth took 194,684 ms for ONE pass over ~24K injected
   tokens (repl.go chunk-1 dumped whole via formatPriorOutputs) vs a
   120,000 ms budget → `budget_after_latency_ms=-91963` → hop-2 refused
   → item-5 fallback shown (correct, non-empty). Fix direction:
   NARROW the synth's injected context (salience/study the read output,
   or a smaller synth-mode chunk) — cuts latency AND raises the odds the
   synth answers in one pass. NOT a bigger latency cap. coder80 may also
   synth faster (ties into item 3).

## Open follow-ups (smaller, queued in eval-journal entries)

- Per-format chars-per-token (JSON ≈ 2.7, engine assumes 4): data-study
  prompts run ~40% over token budget → slow cells + overflow risk.
- Corpus-study productionization: port the citation contract into the
  project-study controller (`internal/study/controller.go`); the L0 loop
  exists, insights currently uncited.
- Numbered corpus lines for L1 studies should raise relay yield above 4/11.
- `--probe` flag for `eval codebase` (canonical 3-fixture mechanical-health
  slice, nonzero exit) — make probe-first a command, not a habit.
- Wall-clock: RTX 3090 as second backend (cleanest: add to chatterbox's
  LiteLLM as a remote backend; new alias + window entry in config only),
  enables parallel A/B passes via endpoint-prefixed `--model`. Nightly cron
  for full suites.

## Working agreements (from this arc; memory has details)

- **Probe before long runs** — minutes-scale sanity slice before hours-scale
  runs, mandatory on new configurations. (Learned the hard way, twice.)
- **Fine-tuning is deferred** until the harness matures — don't propose
  training spend; trajectories + eval verdicts accrue free in
  `dag_traces.jsonl` / `cell_results.jsonl`.
- Eval claims get revised when n says so (the n=3 → n=10 granularity story);
  every measured claim goes in `docs/eval-journal.md` with its command.
- Branch plan: items 1–4 continue on `derek.s/self-improvement-loop`
  (the DAG-harness repairs are part of the same arc; merge to main when
  the fault tree is closed out).
