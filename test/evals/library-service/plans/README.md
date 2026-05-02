# Library Service Eval — Implementation Plans

The eval is scaffolded; the harness is stubbed. These four plans break the remaining work into independent chunks that fresh sessions can pick up without re-deriving design.

Each plan is self-contained: context, scope (in/out), files to create, ordered steps, verification, and a definition of done.

## The plans

| # | Plan | Independent? | Validates |
|---|---|---|---|
| [01](01-scorer.md) | Scorer — implement the 4 MVP rubric metrics | Yes | Eval design itself: can it tell cohesive from diverged? |
| [02](02-session-runner.md) | Session runner — drive Claude CLI through the 5 prompts | Yes (but needs 01 to be useful) | Sessions actually produce working code |
| [03](03-cortex-injection.md) | Cortex injection — the condition fork (baseline vs cortex) | No (needs 02) | The thesis: does Cortex lift cohesion? |
| [04](04-integration-test.md) | Integration test — build, start, hit 25 endpoints | Yes | Generated services actually work |

## Suggested execution order

- **Parallelize Plans 01 and 04** — both fully independent, both validatable against hand-crafted fixtures, both unblock the rest. Two fresh sessions can pick these up simultaneously.
- **Then Plan 02** — needs the scorer to know if the runner's output is good; doesn't need Plan 04 directly, but Plan 04 helps validate end-to-end.
- **Finally Plan 03** — the thesis test. Once 01–04 are done, this is the smallest piece and produces the actual eval results.

## What to do when the thesis fails

If Plan 03's first run produces a Cortex condition that doesn't beat baseline, that's a real result, not a bug. Most likely cause: existing Cortex retrieval doesn't extract handler-shape patterns well from S1's events. That motivates building tier-2 dynamic mining (see `project_direction_small_model_amplifier.md` in user memory).

The eval is doing its job either way — proving or disproving the thesis cheaply.

## What's intentionally not planned

- **Tier-2 dynamic mining** — out of scope for this eval. The eval *tests* whether the existing pipeline plus tier-2 (when built) lifts cohesion. Tier-2 itself is a separate work stream.
- **Tier-3 AST detection** — same.
- **Ollama / Aider as harnesses** — Claude CLI only for MVP. Documented as a future Harness implementation in Plan 02.
- **Refactor delta scoring** — optional in the rubric; only build if shape similarity isn't discriminative enough after the first runs.
