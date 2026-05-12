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

## Re-evaluation criteria

To remove the gate (or default it ON), the next eval run must show:

- `cortex_extension` pass-rate ≥ baseline pass-rate on the
  `test/evals/coding` set (5 scenarios) AND on a held-out set of ≥ 3
  additional coding scenarios.
- `cortex_recall` calls per cell average < baseline turn count (the
  agent should pull context surgically, not speculatively).

If both hold, remove the env gate in `library_service_opencode_harness.go`
and update this doc to mark the rollback closed.
