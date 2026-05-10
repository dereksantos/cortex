# Phase 7 — Cross-harness × cross-strategy data on coding scenarios

> First triple-harness × baseline+cortex run on the v2 coding eval
> shape, model = `openrouter/openai/gpt-oss-20b:free`, 2026-05-10.
> 30 cells (5 scenarios × 3 harnesses × 2 strategies), single seed.

## Headline

| Harness  | Baseline pass | Cortex pass | Cortex lift |
|----------|---------------|-------------|-------------|
| aider    | 4/5 (80 %)    | 4/5 (80 %)  | **0 pp**    |
| opencode | 4/5 (80 %)    | 2/5 (40 %)  | **−40 pp**  |
| pi_dev   | 4/5 (80 %)    | 2/5 (40 %)  | **−40 pp**  |

**Static-cortex injection (the hand-authored YAML bullets in each
scenario) is a 0 pp lift on aider — matching the 2026-05-10 single-
harness finding — and a ~40 pp REGRESSION on both opencode and pi.dev
at the same model + scenarios.**

This is a real cross-harness signal, surfaced exactly as the Phase 7
ablation was designed to.

## Per-scenario × harness × strategy

```
                    aider              opencode           pi_dev
SCENARIO            base | cortex      base | cortex      base | cortex
─────────────────────────────────────────────────────────────────────────
add-table-test       ✗   |   ✗          ✗   |   ✗          ✗   |   ✗
error-wrap           ✓   |   ✓          ✓   |   ✓          ✓   |   ✗
fix-off-by-one       ✓   |   ✓          ✓   |   ✗          ✓   |   ✗
fizzbuzz             ✓   |   ✓          ✓   |   ✗          ✓   |   ✓
rename-json-tag      ✓   |   ✓          ✓   |   ✓          ✓   |   ✓
─────────────────────────────────────────────────────────────────────────
pass-rate           4/5  |  4/5        4/5  |  2/5        4/5  |  2/5
```

Where cortex hurts (✓ → ✗ flips):

- opencode × fix-off-by-one
- opencode × fizzbuzz
- pi_dev   × error-wrap
- pi_dev   × fix-off-by-one

Four regression points, two harnesses, three of the five scenarios.

## Token + latency cost

| Harness × Strategy | Σ tokens_in | avg latency_s |
|--------------------|-------------|---------------|
| aider × baseline   | 4,912       | 17            |
| aider × cortex     | 5,280       | 17            |
| opencode × baseline| 78,491      | 71            |
| opencode × cortex  | 46,498*     | 129           |
| pi_dev × baseline  | 14,282      | 88            |
| pi_dev × cortex    | 15,101      | 132           |

\* opencode's cortex tokens are *lower* than baseline, but this is an
artifact: when cortex steered the model wrong, it exited the agent
loop after one or two failed tool calls instead of the longer
recovery loop baseline cells went through. Lower tokens here mean
"gave up faster," not "more efficient."

## Why cortex hurts non-aider harnesses (hypothesis)

The scenario YAML's `cortex_context:` bullets were authored
implicitly for aider's `--message`-mode interaction shape:

- aider gets one shot at the prompt and replies (one model turn).
  Static hints are inlined into that single context window — they
  bias the response directly.
- opencode and pi.dev are multi-turn agent loops. The same hints
  reach the model as a *system-ish* prefix that gets re-emitted on
  every turn of the loop. Hints that read as "here's a hint" to a
  one-shot model read as "this is what to do" to a tool-using agent
  — and the agent then tries to satisfy the hint literally, often
  in ways that conflict with the scenario's actual completion
  criterion.

Two concrete examples from the run:

- `pi_dev × error-wrap × cortex` (FAIL, 215 s, 4,612 tokens): the
  cortex bullets steered the model toward a structural rewrite the
  verifier didn't accept. Baseline pi_dev passed the same scenario
  in 87 s, 1,059 tokens.
- `opencode × fix-off-by-one × cortex` (FAIL, 34 s, 9,341 tokens):
  the agent loop terminated early after the cortex bullet
  conflicted with what the seed code actually required.

This is the kind of harness-shape sensitivity the original Phase 7
prompt warned against ("each harness has different event shapes;
copying the parser would silently miss data"). Here the parser is
fine; the *prompt* shape needs to be harness-aware to keep
cortex-injection useful across all three.

## What the data does and does not say

**Does say:**

- Both new harnesses are wired through evals (grid CLI) AND through
  cortex injection (`--strategies cortex` flows through them
  end-to-end and is reflected in `cell_results.context_strategy`).
- We have data from all three harnesses, both strategies, on the
  same 5 coding scenarios + 1 model.
- Static-cortex bullets help one harness (aider, neutrally) and
  hurt two others on this model.

**Does NOT say:**

- Whether the regression holds at a stronger model. `gpt-oss-20b:free`
  hallucinates paths and gives up easily — that's a confounder. The
  same scenarios on `claude-haiku-4.5` or `qwen3-coder` might tell
  a different story.
- Whether non-static-cortex configs (Reflex-mined, Reflect-reranked)
  share the regression. The injector is the same path; the *content*
  changes.
- Anything about cross-tier amplification or cost-per-passing-cell
  rollups — that needs ≥2 model tiers and saturated scenario sets.

## Suggested next experiments

In rough order of leverage:

1. **Re-run on `claude-haiku-4.5`** (paid; needs
   `CORTEX_EVAL_ALLOW_SPEND=1`). Headline question: does the −40 pp
   cortex regression on opencode/pi.dev hold on a model that doesn't
   hallucinate? Estimate $0.20–0.50 for 30 cells.
2. **Author harness-aware cortex bullets.** Add a `cortex_context_opencode:`
   / `cortex_context_pi_dev:` field on the scenario YAML for cases
   where the static bullet phrasing breaks the multi-turn agent
   loops. Or rework the existing bullets to read well in both
   shapes.
3. **Add more samples per cell.** With n=1 per cell, 1-pass variance
   on small models is high. Run each cell 3× and report median.
   Compute a real confidence interval before publishing the −40 pp
   number externally.
4. **Cortex strategy ≠ static bullets only**: wire up `Reflex` and
   `Reflect` cortex configs in the grid runner so the strategy axis
   gains real depth. The current `--strategies cortex` only varies
   the prefix; mature cortex should vary what's retrieved.

## Raw data

30 rows live in `.cortex/db/cell_results.jsonl` (tail -30) and the
SQLite `cell_results` table on the `feat/phase7-harnesses` branch.
Run command:

```
CORTEX_EVAL_ALLOW_SPEND=1 CORTEX_EVAL_NO_FREE_PREFERENCE=1 \
cortex eval grid \
  --scenarios test/evals/coding \
  --harnesses aider,opencode,pi_dev \
  --models    openai/gpt-oss-20b:free \
  --strategies baseline,cortex
```

Total spend: $0 (free model).
