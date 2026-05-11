# Phase 8 — extension vs prefix A/B

The cross-harness grid run TODO 10 specified: 5 coding scenarios
× pi_dev × {baseline, cortex, cortex_extension} ×
`openai/gpt-oss-20b:free` = **15 cells**.

Source data: `.cortex/db/cell_results.jsonl` (filter by
`git_commit_sha ∈ {148f482, c113cc8, f957782}` and
`model = openai/gpt-oss-20b:free`).

## Conditions

| dimension | value |
|---|---|
| scenarios | `test/evals/coding` (`add-table-test`, `error-wrap`, `fix-off-by-one`, `fizzbuzz`, `rename-json-tag`) |
| harness | `pi_dev` (pi-coding-agent v0.74.0) |
| provider | `openrouter` |
| model | `openai/gpt-oss-20b:free` |
| strategies | `baseline`, `cortex` (prefix `Hints:` shape — Phase 7), `cortex_extension` (Phase 8 packaged extension) |
| `$CORTEX_BINARY` | `/tmp/cortex` (built from `feat/phase8-pi-extension`) |
| `$CORTEX_PI_EXTENSION_SOURCE` | `$(pwd)/packages/pi-cortex` (absolute) |
| spend | $0 (`:free` model) |

## Results — per scenario pass-rate

| scenario | baseline | cortex (prefix) | cortex_extension |
|---|---|---|---|
| add-table-test | TBD | TBD | TBD |
| error-wrap | TBD | TBD | TBD |
| fix-off-by-one | TBD | TBD | TBD |
| fizzbuzz | TBD | TBD | TBD |
| rename-json-tag | TBD | TBD | TBD |
| **pass-rate** | **TBD / 5** | **TBD / 5** | **TBD / 5** |

## Tool-fire rate

The tightened pass criterion #3 (Phase 8.0 tick 0.c) requires the
agent to **visibly cite or act on `cortex_recall` output in its
next turn on ≥ 3 of 5 coding scenarios**. Liveness (the tool
firing at all) is necessary but not sufficient.

| metric | cortex_extension |
|---|---|
| cells where `cortex_recall` fired at all | TBD / 5 |
| cells where the agent's next turn references the recalled content | TBD / 5 |
| cells meeting tightened pass criterion #3 | TBD / 5 |

Source: pi `--mode json` event streams reparsed from `.cortex/`
event captures for each cell's run_id.

## Verdict per Phase 8.0 tick 0.g

The 3/5 boundary is a closed ternary:

- **≥ 4/5 on cortex_extension** → pass; the extension shape is the
  primary integration.
- **= 3/5** → inconclusive; re-run against a held-out scenario set
  before deciding.
- **≤ 2/5** → decisive invalidation; engage the rollback procedure
  (gate behind `CORTEX_PI_EXTENSION=1`, document evidence, do not
  revert the branch).

**Verdict:** TBD (fill in after grid completes).

## Carry-forwards

Inherited from `docs/pi-extension-smoke-notes.md` after TODO 6's
real-pi smoke runs:

- `openai/gpt-oss-20b:free` does not reliably emit a
  `cortex_recall` tool call even when the tool is the only one
  available and the system prompt explicitly demands it. The model
  writes about calling the tool in its thinking trace, then ends
  the turn without emitting the call.
- Phase 7 harmony-format leak (`bash<|channel|>commentary` in
  tool names) is back, separate from the extension path.

If the cortex_extension cells land at 0–2/5 with no
`cortex_recall` calls in the event stream, the rollback procedure
applies — but the *finding* is at the model layer (gpt-oss-20b
tool-calling reliability), not the extension wiring. Future
extension validation should swap models (paid OpenRouter or
Anthropic Haiku with a funded credit balance).

## Notes

- Grid runner kicked off at `Bash` foreground from
  `feat/phase8-pi-extension` HEAD, env: `CORTEX_BINARY=/tmp/cortex
  CORTEX_PI_EXTENSION_SOURCE=$(pwd)/packages/pi-cortex`.
- Persistence verified: each cell writes to both
  `.cortex/db/evals_v2.db` (SQLite) and
  `.cortex/db/cell_results.jsonl` per the `PersistCell` funnel.
- This file is a TODO 10 artifact; final close report and full
  carry-forward list live in `docs/phase8-close-report.md`
  (TODO 12).
