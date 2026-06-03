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

ENVIRONMENT EXPORTS (every session, set once)
- CORTEX_BINARY=/tmp/cortex-eval
- CORTEX_SKIP_STUDY=1
- CORTEX_EVAL_RUN_USD_CEILING=10000
- CORTEX_EVAL_DAILY_USD_CEILING=10000
- CORTEX_EVAL_LIFETIME_USD_CEILING=10000

PRE-FLIGHT (run ONCE before the first iteration of this session)

The loop's safety net (regression-check canaries) only works if the
floor it compares against is current. The pre-flight establishes that
floor on the current HEAD and verifies every regression-check
fixture passes BEFORE any iteration changes anything. If the pre-flight
surfaces a problem, do not enter the loop — surface to the user.

1. Rebuild binary from HEAD:
   `go build -o /tmp/cortex-eval ./cmd/cortex`
   Fast (~5s); idempotent.

2. Read the most recent baseline at
   `.cortex/db/eval_baselines/<commit>/<latest>.jsonl`. Confirm it's
   from the current commit (`git rev-parse HEAD | head -c 12` matches
   the commit dir). If stale (older commit), the pre-flight MUST
   refresh it via step 3. If absent entirely, step 3 generates the
   first one.

3. Run a fresh full baseline IF (a) no baseline exists for HEAD, or
   (b) more than one harness/prompt commit has landed since the last
   baseline:
     `$CORTEX_BINARY eval codebase --baseline --binary $CORTEX_BINARY --timeout 900 --judge-model reasoner`
   Wall time ~45 min. Skip if a fresh baseline already exists.

   BINARY DISCIPLINE (non-negotiable): the baseline MUST run on the same
   HEAD binary the iterations use — both the OUTER `$CORTEX_BINARY eval`
   invocation AND the per-cell subprocess (`--binary $CORTEX_BINARY`).
   A bare `cortex` resolves to whatever is on PATH (often a stale
   `bin/cortex`), and a stale binary that doesn't emit
   sense.estimate_scope silently zeroes budget_tokens for every cell —
   comparing HEAD iterations against a stale-binary floor. `eval
   codebase` now also falls back to $CORTEX_BINARY for the subprocess
   when `--binary` is omitted, but pass it explicitly anyway.

4. Regression-check canaries — these MUST pass before the loop starts.
   Any fail = STOP, surface to user.
   - Codebase canaries:
       `$CORTEX_BINARY eval codebase --only R1 --only B2 --only B3 --binary $CORTEX_BINARY --timeout 300`
     Expected: 12/12 passing (R1×4 + B2×4 + B3×4 = 12 universal-pass).
   - DAG invariants:
       `cortex eval suite mechanic`
     Expected: 5/5.
   - Retrieval signal:
       `cortex eval benchmark mteb --tasks NFCorpus --limit 50`
     Expected: 1/1, NDCG signal landed in cell_results.
   - LongMemEval (small subset, with judge):
       `cortex eval benchmark longmemeval --subset oracle --limit 3
        --strategy cortex --model coder --judge --judge-model reasoner`
     Expected: per-cell tokens > 0 (i.e. real chatterbox calls, not
     74ms silent failures). Pass count is informational; the signal
     we lock is "the pipeline runs end-to-end."

5. Record pre-flight result to the conversation as ONE line:
   `pre-flight: baseline N/44, canaries N/12, mechanic 5/5, mteb ok,
    longmemeval ok — entering loop` (or `STOP — <what broke>`).

PER-ITERATION LOOP

Pre-iteration check (cheap, every iteration):
- Confirm `/tmp/cortex-eval` matches HEAD: rebuild if `git rev-parse
  HEAD` differs from the binary's embedded commit. Use the same env
  exports from the top of this prompt.

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
7. Update the working baseline:
   `$CORTEX_BINARY eval codebase --baseline --binary $CORTEX_BINARY`
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

### 2026-06-01 — the "6/44 catastrophe" was a stale-binary artifact

A first diagnostic baseline read **6/44 with budget_tokens=0 on every
cell** — apparently a synth-turn collapse. It was not. The baseline
command omitted `--binary`, so each cell's subprocess shelled out to the
PATH `cortex` = a 10-day-stale `bin/cortex` (May 21) that doesn't emit
`sense.estimate_scope` at all → `budget_tokens=0`. Re-running the SAME
HEAD on the freshly built binary (`--binary $CORTEX_BINARY`) scored
**24/44** with healthy budgets (5000–120000) — in the same ballpark as
the prior 28/44, i.e. **no catastrophic regression**.

Root cause was a measurement-honesty hole in this very prompt: the
baseline + canary steps ran bare `cortex` while iterations ran
`--binary $CORTEX_BINARY`, comparing HEAD iterations against a
stale-binary floor. Fixes landed:

- `eval codebase` now falls back to `$CORTEX_BINARY` for the cell
  subprocess when `--binary` is omitted, and prints the resolved binary.
- This prompt's baseline/canary/update-baseline steps now invoke the
  HEAD binary explicitly (outer `$CORTEX_BINARY eval` + `--binary
  $CORTEX_BINARY`).
- Hardened row→cell association in the codebase runner: byte-offset tail
  → turn_id delta (snapshot prior turn_ids, keep rows whose turn_id is
  new). Robust to flush ordering and shared per-project trace files;
  not the cause of the 0s here, but removes a fragile assumption.

Caveat for the Path-B story: the earlier bisect used `--binary` on both
sides, so its canary delta (B2 4→0, R1 4→1) is method-valid — but the
corrected full baseline shows B at 9/12, so much of that gap is likely
run-to-run variance. Determinism (temp=0, local-only routing) matters
more than first weighted; it's the next measurement-honesty lever.

### 2026-05-31 — pre-flight gate added; Path B landed

The loop prompt grew a PRE-FLIGHT section that runs ONCE before the
first iteration of each session, ensuring:

1. The binary at `/tmp/cortex-eval` is built from current HEAD.
2. A fresh baseline exists for HEAD (refresh if stale or absent —
   ~45min, but skip if a current one is already on disk).
3. Every regression-check fixture (R1/B2/B3 canaries + mechanic +
   mteb-NFCorpus-50 + longmemeval-oracle-3) passes BEFORE any
   iteration. Failure = STOP, surface to user.

Rationale: without the pre-flight, the loop's `--compare` regression
check could be diffing against a stale baseline, and a silent
upstream regression (in mechanic, longmemeval, mteb) wouldn't surface
until much later. The pre-flight makes "no regression elsewhere" a
verified property at session start, not just an iteration-level
hope.

Path B (per-node model swap on review-class synth) landed in commits
0c87340 (operator capability map), 0eb9a61 (catalog summary +
prompt nudge), and bb3ec1f (deterministic SynthDefaultModel closure).
Q1 cortex newly passes with judge_pass=true; Q3 cortex synth now
runs on the chatterbox/reasoner (95s latency vs 45s for coder).
Operator config sample documented at docs/config-sample.md.

The current baseline number (28/44 from before Path B) is now stale.
The pre-flight will generate a fresh post-Path-B baseline on first
session start — expect 30-32/44 since Q1 cortex newly passes and a
few other Q-class cells likely benefit from the routing.



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
