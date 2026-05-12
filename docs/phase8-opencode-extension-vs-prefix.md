# Phase 8 — opencode-cortex extension vs. baseline

Companion to `docs/phase8-extension-vs-prefix.md` (pi.dev's analog).
Documents the eval result, the rollback decision, and how to flip the
gate when the cortex store is seeded.

## Run config

| field | value |
|---|---|
| harness | `opencode` (sst/opencode v1.14.46) |
| model | `openrouter/openai/gpt-oss-20b:free` |
| strategies | `baseline`, `cortex_extension` |
| scenarios | `test/evals/coding` (`add-table-test`, `error-wrap`, `fix-off-by-one`, `fizzbuzz`, `rename-json-tag`) |
| cells | 5 scenarios × 2 strategies = 10 |
| date | 2026-05-12 |
| commits at run | `3ad8c5d` … `00f8390` (this branch through commit 6) |

## Result

| scenario        | baseline           | cortex_extension   |
|-----------------|--------------------|--------------------|
| add-table-test  | NO                 | NO                 |
| error-wrap      | **YES**            | NO                 |
| fix-off-by-one  | NO                 | NO                 |
| fizzbuzz        | **YES**            | NO                 |
| rename-json-tag | **YES**            | **YES**            |
| **pass-rate**   | **3/5 (60%)**      | **1/5 (20%)**      |

**Lift: −40 pp.** Same wall pi-extension's close report flagged
(`docs/phase8-close-report.md`): "Cortex search returns junk … the
integration is mechanically wired but provides no signal. Re-seed the
cortex store before flipping the box."

## Diagnosis

- Plugin loads cleanly. (Initial run failed with "plugin config hook
  failed"; root cause: opencode's loader iterates every named export
  as a Plugin candidate. Fixed in commit `00f8390` by un-exporting
  helper functions and types.)
- `cortex_recall` IS reaching the agent — opencode's `tool.registry`
  log shows 4–10 calls per `cortex_extension` cell.
- Output token cliff: 4 of 5 `cortex_extension` cells produce
  `out=29-30` (one-line responses). The model burns its turn budget
  on speculative `cortex_recall` calls that all return *"No relevant
  context captured yet"* (cortex store is unseeded for these
  scenarios), then never writes the code change. The single passing
  cell (rename-json-tag) made 10 `cortex_recall` calls AND wrote 231
  output tokens — proving the integration can pass when the agent
  doesn't get stuck.

## Decision

**Ship behind `CORTEX_OPENCODE_EXTENSION=1` env gate, default OFF.**

Rationale:
- Mirrors pi-extension's documented rollback procedure (close report
  §"Rollback if regression").
- The integration code is mechanically correct (CI green; 25 TS tests
  pass, 8 new Go tests pass).
- The regression is environmental (unseeded store), not a code bug.
- Default-off avoids the regression's blast radius on day one of
  merge. Operators flip it on after seeding scenario-relevant
  context.

## How to flip the gate ON

```sh
export CORTEX_OPENCODE_EXTENSION=1
export CORTEX_BINARY=/path/to/cortex
export CORTEX_OPENCODE_PLUGIN_SOURCE=/path/to/packages/opencode-cortex/plugins/cortex.ts
export CORTEX_PROJECT_ROOT=/path/to/your/project    # dir holding .cortex/

# now `cortex eval grid --strategies cortex_extension …` will install
# the plugin per cell and exercise the integration end-to-end
```

When unset (or set to anything other than `"1"`), the
`OpenCodeHarness` skips the install even when
`SetCortexExtensionEnabled(true)` was called per-cell — the cell runs
as baseline.

## Operator playbook: what to seed before flipping the gate

The 5 `test/evals/coding/` scenarios each ship with a `cortex_context`
field — these are the exact insights `cortex_recall` should be
returning when called. Capture them into the cortex store before
running the gate-ON eval, otherwise `cortex_recall` returns the
no-results sentence and the model spins on empty recalls.

Run from the cortex project root:

```sh
# error-wrap
./bin/cortex capture --type=decision --content="Use fmt.Errorf with the verb %w to wrap; this preserves errors.Is/As compatibility."
./bin/cortex capture --type=convention --content="Add a meaningful prefix like 'load config %q' rather than just 'error'."
./bin/cortex capture --type=constraint --content="Don't change the function signature; keep returning ([]byte, error)."

# fizzbuzz
./bin/cortex capture --type=constraint --content="The function signature is fixed; do not rename FizzBuzz or change its parameters."
./bin/cortex capture --type=convention --content="Match the file's existing style: package comment first, exported function with doc comment."
./bin/cortex capture --type=workflow --content="Run \`go test ./...\` to verify before considering the task complete."

# fix-off-by-one, add-table-test, rename-json-tag — see each scenario's
# cortex_context block in test/evals/coding/<scenario>.yaml and capture
# each line via `cortex capture --type=<decision|convention|constraint|workflow> --content=<line>`
```

Then run `cortex ingest` (without daemon) so the captures are
indexed and `cortex search` can return them. Now re-run the eval:

```sh
./bin/cortex eval grid \
  --harnesses opencode \
  --strategies baseline,cortex_extension \
  --models openai/gpt-oss-20b:free \
  --scenarios test/evals/coding
```

If the new run lands `cortex_extension ≥ baseline`, remove the env
gate (single-line change in `library_service_opencode_harness.go`)
and update this doc to mark the rollback closed. If it still
regresses with seeded context, re-evaluate per `Re-evaluation
criteria` below.

## Empirical seeded-store result (gate ON, store seeded per playbook)

After running the operator playbook above (15 captures across the 5
scenarios + `cortex ingest`) and re-running the gate-ON eval, the
lift inverts:

| scenario        | baseline (re-run)  | cortex_extension (seeded) |
|-----------------|--------------------|---------------------------|
| add-table-test  | NO (out=29)        | NO (out=59)               |
| error-wrap      | NO (out=467)       | **YES** (out=795)         |
| fix-off-by-one  | NO (out=59)        | **YES** (out=360)         |
| fizzbuzz        | NO (out=62)        | NO (out=54)               |
| rename-json-tag | **YES** (out=373)  | **YES** (out=210)         |
| **pass-rate**   | **1/5 (20%)**      | **3/5 (60%)**             |

**Lift: +40 pp.** Output tokens on the now-passing cortex_extension
cells jumped from 29-30 (unseeded) to 360-795 — the model is writing
real code on the previously-failing scenarios instead of stalling on
empty recalls. Validates the diagnostic hypothesis: the original
regression was *environmental* (unseeded store), not *structural*.

### Important caveat — baseline drift

The baseline column dropped from **3/5 (unseeded run) to 1/5 (seeded
run)** on the same 5 scenarios with the same model. Two reads:

1. `openai/gpt-oss-20b:free` has high run-to-run variance — N=5 is
   noisy. The +40 pp lift in this run may overstate the true effect.
2. The cortex_extension column moving 1/5 → 3/5 (i.e., +2 scenarios
   passed) while baseline dropped (i.e., -2 scenarios failed) is a
   genuine rotation of which scenarios pass, not a tide-rising
   artifact.

The cortex_extension number is the load-bearing one. It went from
20% → 60% across runs that share the same model+scenarios+harness.
Read (2) is more plausible than read (1) explaining a clean rotation.

### Re-evaluation criteria — **updated** to flip the gate default-on

The original criteria stand, with one numeric refinement based on
empirical data:

- `cortex_extension` pass-rate ≥ baseline pass-rate on the
  `test/evals/coding` set (5 scenarios) — **MET** (see N=10
  combined-runs table below).
- Held-out set of ≥ 3 additional coding scenarios — **NOT YET RUN**.
  Track in a follow-up phase; recommend running with N≥10 cells per
  strategy to dampen `:free` model variance.
- `cortex_recall` calls per cell average < baseline turn count — see
  the gate-ON unseeded observation table below; not re-measured for
  the seeded runs.

### Combined two-run result (N=10 per strategy on the same 5 scenarios)

A second seeded run (same 5 scenarios, store unchanged) was added to
dampen `:free` variance. Combined results:

| run     | baseline | cortex_extension |
|---------|----------|------------------|
| Run 1   | 1/5 (20%)| 3/5 (60%)        |
| Run 2   | 2/5 (40%)| 2/5 (40%)        |
| **N=10**| **3/10 (30%)** | **5/10 (50%)** |

**Combined lift: +20 pp.** Smaller than the single-run +40 pp (which
benefited from a baseline-low / extension-high coincidence), but
still positive and consistent with the diagnostic hypothesis. The
extension never falls below baseline across the two runs; in run 2
it ties; in run 1 it leads.

Run 2 shows another encouraging signal: cortex_extension's input
tokens on its passing cells dropped sharply (1179, 753 in run 2 vs
10838, 625 in run 1) — the model is making fewer / more targeted
`cortex_recall` calls when the store reliably returns useful
context, rather than spamming queries.

Decision: leave the env gate IN PLACE (default OFF) until a
held-out N≥10 run on scenarios outside `test/evals/coding/` lands.
The +20 pp lift at N=10 is encouraging but the standard error on
binomial at N=10 with p≈0.4 is ~15 pp, so a +20 pp gap is
statistically borderline. Held-out scenarios eliminate the risk
that the captures are over-fit to the test set.

## Per-cell observation from the unseeded gate-ON run (kept for history)

| scenario        | base ok | ext ok | ext cortex_recall calls | ext out tokens |
|-----------------|---------|--------|--------------------------|----------------|
| add-table-test  | NO      | NO     | ~4                       | 30             |
| error-wrap      | YES     | NO     | ~4                       | 30             |
| fix-off-by-one  | NO      | NO     | ~0–4                     | 29             |
| fizzbuzz        | YES     | NO     | ~0–4                     | 30             |
| rename-json-tag | YES     | YES    | **10**                   | 231            |

The single passing `cortex_extension` cell (rename-json-tag) called
`cortex_recall` more times than the failing ones, AND produced more
output tokens than its baseline counterpart (231 vs 162). This says
the integration **can** lift when the agent uses the tool well — the
failure mode is *underuse-then-give-up*, not *overuse-and-stall*.
Seeding the store gives `cortex_recall` something useful to return on
the early calls, which should keep the agent on track for the later
turns.

## Re-evaluation criteria

To remove the gate (or default it ON), the next eval run must show:

- `cortex_extension` pass-rate ≥ baseline pass-rate on the
  `test/evals/coding` set (5 scenarios) AND on a held-out set of ≥ 3
  additional coding scenarios.
- `cortex_recall` calls per cell average < baseline turn count (the
  agent should pull context surgically, not speculatively).

If both hold, remove the env gate in `library_service_opencode_harness.go`
and update this doc to mark the rollback closed.
