# Loop — drive codebase eval suite from 28/44 toward 44/44

Goal: take the codebase-reading eval suite
(`docs/eval-suite-codebase-reading.md`) from its current local-only
baseline of **28/44** (commit `35dedfa`, model = chatterbox `coder`
qwen3-30b-moe + reasoner judge) toward **44/44** without regressing any
other eval family (mechanic / journeys / mteb / niah / longmemeval).

This file is the living plan. Each major decision lands as an edit
here; the loop prompt at the bottom is the runnable artifact.

---

## Current baseline (2026-05-31)

```
HARNESS BASELINE — git 35dedfa — local-only — $0 — chatterbox fleet

  mechanic              5/5     (100%)
  journeys              10/24   (42%)
  codebase              28/44   (64%)   ← target this number
  mteb-nfcorpus         50q     NDCG
  niah-retrieval        2/6     (33%)
  longmemeval-oracle    1/10    (10%)
```

Codebase per-class:

| Class | Pass | Difficulty of remaining |
|---|---|---|
| R1 single-chunk | 4/4 | — |
| R2 paged | 2/4 | medium (synth summary too vague) |
| R3 symbol-in-large | 2/4 | medium (enclosing_symbol nav, model invents names) |
| Q1 pinpoint | 3/4 | easy (synth-prompt nudge for one fixture) |
| Q2 cross-file | 2/4 | medium (cortex_search ranking) |
| Q3 audit | **1/4** | hard (synth runs out of budget on whole-project) |
| Q4 refactor | **1/4** | hard (can't enumerate symbols at scale) |
| Q5 locate | 1/4 | medium (model dumps grep instead of file:line) |
| B1 scope-estimator | 4/4 | — |
| B2 termination | 4/4 | — |
| B3 honest-unknown | 4/4 | — |

---

## Honest ceiling and why 44/44 is not just a tuning problem

Realistic ceiling on the local `coder` model with pure
fixture/prompt/extractor tuning: **~38-40/44**. The hard cluster
(Q3 audit × 3, Q4 refactor × 3 = 6 cells) encodes failure modes that
are *the point of the eval*:

- **Q3 (audit)**: synth needs to hold 5+ disparate claims with
  citations in working memory while reasoning about doc-vs-code
  divergence. The small-model synthesizer runs out of output budget
  or skips claims silently.
- **Q4 (refactor)**: model must enumerate symbols across thousands of
  lines and group them coherently. The chunker emits a fraction of
  the file per read; the synth never sees the whole symbol set.

Pushing these from 1/4 to 4/4 by tuning fixture thresholds would
**launder the score** — exactly the failure mode `eval-principles.md`
warns about. Real progress here requires structural work in one of
three directions, described below.

---

## Three paths beyond the tuning ceiling

Each path has a "**Built?**" audit — what's already in the repo as of
commit `5004f30`, separate from what's net-new. The original draft of
this doc over-claimed the cost of A and B; the audit below corrects it.

### Path A — DAG decomposition (structural; cortex's thesis play)

Current shape for Q3 / Q4: one big `decide.coding_turn` synth turn
trying to hold the whole project in working memory. Fails predictably
on context window + reasoning depth.

Proposed shape: a new seed plan that **decomposes audit/refactor
prompts into per-file sub-questions, runs each as its own
`decide.coding_turn`, then synthesizes the answers**. This is exactly
the cortex thesis — "small model + harness matches bigger model
alone" — applied to a task class that today defeats single-pass synth.

Concrete sketch:

```
sense.classify_intent  →  intent=audit | refactor
   ↓
attend.decompose (NEW)  →  {targets: ["pkg/foo/bar.go", "docs/X.md"], …}
   ↓
[fan-out: act.read_file × N targets, each followed by attend.distill]
   ↓
[fan-in: attend.accumulate (existing) collects per-file observations]
   ↓
decide.coding_turn (synthesize=true)  →  final answer
```

**Built? ~75%.** The attend.* op family already exists in
`pkg/cognition/dag/ops/`:
`attend.accumulate` (fan-in), `attend.compact`, `attend.compress`,
`attend.distill` (claim extraction), `attend.rerank`,
`attend.fractal_sample`. `decide.next` already spawns child specs and
the executor handles fan-out. The NEED_MORE → decide.next →
coding_turn loop is primitive decomposition that already works.

**What's net-new:**
- `attend.decompose` op (~50 lines, mirrors `sense.estimate_scope`'s
  shape) — small-LLM call that takes prompt + intent + project root
  listing and emits 3-7 targets.
- Intent-aware seed plan: when `intent ∈ {audit, refactor}`,
  `decide.next` (or a new `decide.plan` extension) emits the
  decomposition shape above instead of the single-turn shape.

**Revised cost:** 2-3 days, not 2 weeks. Touches
`pkg/cognition/dag/ops/`, `internal/harness/dagnode/coding_turn.go`,
and the seed registry in `cmd/cortex/commands/repl.go`.

**Risk:** regresses single-pass prompts if intent classifier
mis-routes — gate strictly on intent + a confidence threshold.

**Expected lift:** Q3 1/4 → 3/4, Q4 1/4 → 3/4. Possibly Q2 also.
Net codebase: +5 to +7.

### Path B — Per-node model swap (capability play)

Use a stronger model **only for the audit/refactor synth turn**, not
for the whole pipeline. The per-node routing shipped in `eda2bbe`
already does this generally — Path B is just configuring it for the
hard nodes.

**Built? ~90%.** Audited live:
- `pkg/llm/capabilities.go` already defines: `CapCoding`,
  `CapCodingSpecialist`, `CapReasoning`, **`CapReasoningSpecialist`**
  (the "deep reasoning" tag), `CapToolCalling`,
  `CapToolCallingSpecialist`, `CapEmbedding`, `CapReranking`,
  `CapVision`.
- `dag.NodeSpec.Requires []string` field — ordered capability
  preference chain consumed by the router.
- Working examples in the tree: `sense.estimate_scope` and
  `decide.next` both declare `Requires: []string{llm.CapReasoning,
  llm.CapToolCalling}` and route correctly today.

**What's net-new:**
- `decide.coding_turn`'s spec doesn't currently declare `Requires` —
  it falls through to the harness default. Add intent-aware `Requires`:
  when `intent ∈ {audit, refactor, synthesize}`, declare
  `Requires: []string{llm.CapReasoningSpecialist, llm.CapReasoning}`.
- Tag a stronger model with `CapReasoningSpecialist`. Two options:
  - **(a)** OpenRouter route to gpt-5.4 or claude-haiku-4.5 — costs
    $0.01-0.10 per audit cell, acceptable for an eval push.
  - **(b)** Add a `deepseek-coder-v3` or `qwen3-72b` to the chatterbox
    fleet and tag it. Local; requires hardware budget.

**Revised cost:** 1 day, not 3.

**Risk:** introduces a non-local dependency if route (a). The cortex
thesis ("small model + harness matches bigger model alone") doesn't
forbid this — the claim is *quality normalized by model size or
dollars spent*. A frontier model on 1 of ~6 synth nodes still beats
running everything on the frontier.

**Expected lift:** Q3 1/4 → 3-4/4, Q4 1/4 → 3-4/4. Net: +4 to +7.

### Path C — cortex_search ranking improvements (foundational)

Q2 cross-file failures and some Q3/Q4 audit failures share a root
cause: when the synth asks "what's the relationship between X and
Y", `cortex_search` doesn't return the right files in the top-K. The
synth then answers from priors or NEED_MOREs fruitlessly.

**Built? ~50%.** Audited live:
- Fast mode (Reflex) and Full mode (Reflex → Reflect → Resolve) both
  shipped; tool param `mode: full` opts in per call.
- `Reflect` IS the rerank pass; takes any `llm.Provider`. Currently
  uses whatever provider the harness is configured with (the `coder`
  30B), not a dedicated reranker model.
- `CapReranking` capability tag + chatterbox `reranker` alias both
  exist; just not wired into Reflect's provider selection.
- `attend.rerank` DAG op exists as a separate primitive.

**What's net-new:**
- **Quick win (~half day):** Route Reflect through a
  `CapReranking`-tagged model from the registry instead of the
  harness's general provider. Mirrors how `sense.estimate_scope`
  declares its Requires. Faster + likely higher quality than reranking
  with the 30B coder.
- **Code-tuned embedder swap (~2 days):** Currently `nomic-embed-text`
  by default. A code-tuned embedder (jina-code-embeddings,
  voyage-code-3, or similar) would lift symbol-name queries
  significantly. Requires re-embedding the workdir on swap — small
  one-time cost.
- **Hybrid search (~3-4 days):** Confirmed NOT present
  (`internal/storage/` has no BM25). Blend BM25 (exact-name match) +
  dense (semantic) for the top-K. Particularly valuable for symbol
  lookups where the question has the exact symbol name.
- **Retrieval-only benchmark (~1 day):** Score "did Full mode surface
  the right top-3 files for question Q?" in isolation. Currently
  retrieval quality is only observable through downstream Q-class
  pass rate — too noisy.

**Revised cost:** ~1 week for the full path; **half day for the
quick win**.

**Risk:** changes ranking for *every* benchmark — could regress mteb
or longmemeval. Must compare-mode-test across all suites, not just
codebase.

**Expected lift:** Q2 2/4 → 3-4/4, Q3 +1, Q4 +1. Net codebase: +3 to
+5. Also modest lift on longmemeval.

### Recommended sequence (revised)

The original draft put C first because it's foundational. The audit
reveals B-quick is **one day** and C-quick is **half day**, both
likely 2-5 cell lifts each. Front-load both quick wins, then loop,
then bigger work.

| step | actual cost | expected lift | running total |
|---|---|---|---|
| **B-quick**: intent-aware `Requires` on `decide.coding_turn` + tag a frontier or stronger local | 1 day | +2 to +5 | 28 → 30-33 |
| **C-quick**: route Reflect through a `CapReranking`-tagged model | half day | +1 to +3 | 30-33 → 31-36 |
| **Loop tier 1+2** (this prompt) | 1-2 days | +4 to +6 | 31-36 → 35-42 |
| **Path A**: intent=audit seed plan + `attend.decompose` op | 2-3 days | +3 to +5 | 35-42 → 38-44 |
| **C-full**: code-tuned embedder + hybrid BM25/dense + retrieval eval | 1 week | +1 to +3 | 38-44 → 39-44+ |

Combined ceiling on this stack: **42-44/44**, likely cap at 43-44
since one or two cells encode genuine small-model limits that no
amount of decomposition or reranking fixes. That's the kind of cell
worth keeping as a regression target rather than tuning past.

---

## Failure cluster breakdown (16 failing fixtures)

For each, the cheapest-first targeted fix.

### Tier 1: fixture/prompt-level (~3-5 wins, low risk)

- **R3 python-todo**: model still invented `add_todo` after the
  prompt rephrase. Try a harder rephrase: "Inside the route handler
  that creates a new todo (the POST /todos endpoint), what storage
  method is called?" The function name itself is no longer in the
  question.
- **Q1 leanjs**: model returned `NEED_MORE:` instead of converging.
  Probably the hop-2 budget was too tight. Bump hop_count_max=3 OR
  add an explicit "lean.js is small enough to read fully" hint.
- **R2 cortex / python-todo**: synth summary was too vague to pass
  citation_rate. Tighten the prompt to require an explicit line range
  citation: "Cite a 5-line range" instead of "cite the lines you read".

### Tier 2: harness/synth-prompt (~5-10 wins, medium risk)

- **Q5 (3 fail)**: model emits a grep-dump instead of a file:line
  range. Add a Q5-specific synth directive: when `intent=locate`,
  the synthesizer system prompt includes "answer with ONE file:line
  range, not a list of matches; emit NEED_MORE: if you have multiple
  candidates and need to narrow down." Wire via the existing
  intent-aware path in `internal/harness/dagnode/coding_turn.go`.
- **R3 leanjs**: model used grep but not read_file. Strengthen the
  `enclosing_symbol` nav hint in `act.read_file`'s output so the
  model is more inclined to use it.
- **Q2 cortex / leanjs**: cortex_search isn't surfacing the right
  files (overlaps with Path C above). Short-term, the synth directive
  for `intent=lookup` can say "always cite TWO files: producer and
  consumer."

### Tier 3: structural (Path A/B/C above)

- **Q3 (3 fail)**: needs Path A or B.
- **Q4 (3 fail)**: needs Path A or B.

Tier 1 is the loop's bread and butter. Tier 2 is the loop's stretch
work — but each Tier 2 change MUST run the harness regression suite
(mechanic + longmemeval limit=3) before it can stick.

---

## Regression bounds

Anything the loop changes must hold these as hard floors:

- `mechanic`: 5/5 (any drop = REVERT)
- `codebase R1/B2/B3` canaries: 4/4 each (any drop = REVERT)
- `longmemeval --limit 3`: ≥ 0/3 (currently 0/3 on this subset; baseline
  is 1/10 on the full subset — don't let the loop touch anything that
  drops the full-subset rate, but limit=3 is the in-loop check)
- `mteb` (NDCG p50 on NFCorpus@50): within 5% of baseline

If the loop wants to change a system prompt, the synth directive, the
DAG seed, or `cortex_search` config, it MUST run mechanic +
longmemeval-limit-3 + mteb-50 in addition to the codebase regression
check. Fixture-only changes can skip the heavier regression checks.

---

## Working environment

- **Branch:** `derek.s/self-improvement-loop`
- **Binary:** `/tmp/cortex-eval`, rebuilt from HEAD before each
  iteration: `go build -o /tmp/cortex-eval ./cmd/cortex`
- **Env vars** (all sessions):
  - `CORTEX_BINARY=/tmp/cortex-eval`
  - `CORTEX_SKIP_STUDY=1`
  - `CORTEX_EVAL_RUN_USD_CEILING=10000`
  - `CORTEX_EVAL_DAILY_USD_CEILING=10000`
  - `CORTEX_EVAL_LIFETIME_USD_CEILING=10000`
- **Fleet:** chatterbox:4000 — `coder` (qwen3-30b-moe, the
  implementor), `reasoner` (the judge), `xlam-1b-fc-r` (tool-call
  dispatch), `embedder`, `reranker`.
- **Baseline source of truth**: `.cortex/db/eval_baselines/<commit>/<ts>.jsonl`,
  newest entry. Read via `cortex status --eval` for the aggregate or
  parse the JSONL directly for per-fixture detail.

---

## The runnable loop prompt

```
/loop 30m

You are continuing work on the cortex coding harness eval suite. Your
goal: drive `cortex eval codebase` from its current baseline toward
44/44 WITHOUT regressing any other eval family. Each iteration makes
one focused change and proves it helps.

GROUND TRUTH BEFORE STARTING THIS ITERATION
1. Read docs/prompts/loop-codebase-44.md — the full plan + failure
   cluster breakdown + regression bounds. This is the source of truth
   for what to work on and what to avoid.
2. Read .cortex/db/eval_baselines/<newest>/<latest>.jsonl to find the
   current pass set + per-fixture metrics. Skip if you already know
   it from the prior iteration this session.
3. Confirm /tmp/cortex-eval is built from HEAD:
   `go build -o /tmp/cortex-eval ./cmd/cortex` (idempotent; ~5s rebuild).
4. Export: CORTEX_BINARY=/tmp/cortex-eval, CORTEX_SKIP_STUDY=1,
   CORTEX_EVAL_RUN_USD_CEILING=10000, CORTEX_EVAL_DAILY_USD_CEILING=10000.

PER-ITERATION LOOP
1. Pick the SINGLE cheapest failing fixture you have a clear
   hypothesis for. Use the Tier 1 / Tier 2 lists in
   docs/prompts/loop-codebase-44.md to guide priority. Never pick
   a Tier 3 (Q3 audit, Q4 refactor) fixture — those need structural
   work, not tuning.
2. Read the failing fixture's most recent answer in
   <workdir>/.cortex/sessions/<latest>/session.jsonl and the
   dag_traces.jsonl rows for the turn. Form ONE concrete hypothesis
   about why it failed.
3. Apply the MINIMAL change implied: fixture tweak (prompt rephrase,
   bound widening, judge_rubric clarification), harness tweak (synth
   directive in internal/harness/dagnode/coding_turn.go, decide.next
   prompt in pkg/cognition/dag/ops), or extractor tweak (metrics.go).
   Never change more than two files per iteration.
4. Targeted re-run:
   `/tmp/cortex-eval eval codebase --only <fixture-id> --binary $CORTEX_BINARY --timeout 600`
   If still failing, REVERT the change and pick a different
   hypothesis. Do not iterate the same hypothesis twice in a row.
5. Regression check (REQUIRED before keeping the change):
   - Always: `cortex eval codebase --only R1 --only B2 --only B3
     --binary $CORTEX_BINARY --timeout 300`. Any fail = REVERT.
   - For harness changes (internal/harness/, pkg/cognition/, or
     prompts): also run `cortex eval suite mechanic` AND
     `cortex eval benchmark longmemeval --subset oracle --limit 3
     --strategy cortex --model coder --judge --judge-model reasoner`.
     Any pass-rate drop vs baseline = REVERT.
   - Fixture-only changes: skip the heavier checks.
6. Commit on success:
   `git commit -m "fix(evals): <fixture-id> — <one-line why>"`
   with `Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>` trailer.
   Use `git status` before staging; do NOT use `git add -A`.
7. Update the working baseline: `cortex eval codebase --baseline`
   so future --compare runs reflect the new floor.

STOP CONDITIONS (any of these = exit loop)
- 44/44 reached. Celebrate; update docs/prompts/loop-codebase-44.md
  with the final per-class breakdown and how we got there.
- ~36-38/44 reached AND only Tier 3 (Q3/Q4) remains. Surface to the
  user: "Tier 1+2 exhausted; Path A/B/C (see loop-codebase-44.md) is
  the next move and requires explicit signoff."
- 5 iterations in a row without improvement. Stop and surface what's
  left.
- A regression was introduced and could not be safely reverted. Tell
  the user what happened.

CONSTRAINTS
- Never disable a bound to make a fixture pass. The bounds encode
  what we're trying to measure.
- Never edit cortex_search's relevance threshold or the reasoner
  judge's grounding prompt without explicit user signoff.
- Never commit `.cortex/`, `docs/handoff-*.md`, or `/tmp/` artifacts.
- Never use `git add -A` or `git add .` — stage by path.
- Per-iteration wall-time cap: 25 minutes. If targeted re-run +
  regression checks blow past this, kill the run and pick a smaller
  change next iteration.

REPORT
End each iteration with one line to the conversation:
`iter N: <fixture-id> <hypothesis> → +1 / no-change / REVERTED. baseline now M/44.`
```

---

## Discussion log

> Section to capture decisions as we iterate on this plan. Newest entries at the top.

### 2026-05-31 — Path audit corrected the cost estimates

Quick audit of the repo against my own plan revealed I had
significantly over-claimed the cost of Paths A and B:

- **Path B was 90% built, not 70%.** `pkg/llm/capabilities.go`
  already defines `CapReasoningSpecialist`, `dag.NodeSpec.Requires`
  already routes by capability chain, and `sense.estimate_scope` +
  `decide.next` are live working examples. The only net-new work is
  intent-aware `Requires` on `decide.coding_turn` and tagging a
  stronger model. **1 day, not 3.**
- **Path A was 75% built, not 60%.** The entire attend.* op family
  exists (`accumulate`, `compact`, `compress`, `distill`, `rerank`,
  `fractal_sample`). What's missing is an `attend.decompose` op
  (~50 lines) and an intent-aware seed plan. **2-3 days, not 2 weeks.**
- **Path C was 50% built as claimed**, but the audit surfaced a
  half-day quick win: route Reflect's rerank call through a
  `CapReranking`-tagged model instead of the harness's general
  provider. Faster + likely higher-quality reranking, no infra change.

Recommended sequence reordered: B-quick (1 day) + C-quick (half day)
front-loaded before the loop, since both are cheap and likely to lift
several cells each. Then loop tier 1+2, then Path A, then C-full.

Combined ceiling estimate unchanged at 42-44/44; what changed is the
schedule — what looked like ~2-3 weeks of structural work is now
roughly a week's worth.

### 2026-05-31 — initial plan landed

Plan written by Derek + Claude. Loop targets the 16 failing fixtures
in Tier 1+2 first; Path A/B/C strategic work explicitly out of the
loop's scope (requires structural changes + user signoff). Honest
ceiling on this loop: ~36-38/44. The remaining ~6-8 cells are the
hard cluster that justifies a discrete structural follow-up rather
than more tuning.

Open questions for the next round of discussion:
- Path A vs B vs C ordering: doc currently recommends C → B → A, but
  the priority depends on whether we care more about local-only
  ceiling (A) or absolute number (B+frontier model).
- Should the loop be allowed to author NEW fixtures (e.g., a Q6 class
  for compositional Q&A) once 44/44 is hit? Not in the current
  prompt; would extend the suite rather than improve the harness.
- The `inspect_count` schema may need a third axis — "approach"
  (read_file preferred vs shell-then-read vs shell-only) — once we
  start scoring tool-choice quality, not just tool-use count.
