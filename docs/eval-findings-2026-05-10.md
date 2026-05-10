# Eval findings — 2026-05-10

Two experiments on identical scenarios (`test/evals/coding/`) × identical
strategies (baseline, cortex-static-bullets) × identical models, run in
the same session. The harness changed between them; that change
explains everything.

## Experiment 1 — pre-fix: huge cortex lift

Run: 50 cells, $0.035 spent.

| model | baseline | cortex | lift |
|---|---:|---:|---:|
| `openai/gpt-oss-20b:free` | 10.0% | 40.0% | **+30 pp** |
| `qwen/qwen3-coder` | 0.0% | 20.0% | **+20 pp** |
| `anthropic/claude-haiku-4.5` | 0.0% | 0.0% | 0 |
| `google/gemma-4-26b-a4b-it:free` | 0% | 0% | 0 |
| `nvidia/nemotron-nano-9b-v2:free` | 0% | 0% | 0 |

Looked like the small-model amplifier thesis with strong supporting
evidence. **It wasn't.**

## What was actually wrong

`AiderHarness` invoked `aider --message <prompt>` without any
`--file` arguments. Aider's repo map didn't reliably surface the seed
files, and Claude Haiku-4.5 returned this verbatim for every scenario:

> I'd be happy to help implement the FizzBuzz function! However, I
> don't see the files in the chat yet. Could you please add
> `fizzbuzz.go` and `fizzbuzz_test.go` to the chat so I can see what
> needs to be implemented?

Smaller models exhibited the same failure differently — some clobbered
package declarations (writing fresh files in a new package) because
they couldn't see the existing ones; some emitted invalid SR blocks
that Aider couldn't apply.

The `cortex_context` bullets sometimes referenced filenames
("Read fizzbuzz.go and ..."), which gave the small model a stronger
hint about which file to write. That's the only mechanism producing
the apparent +30% lift on `openai/gpt-oss-20b:free` — the bullets
were compensating for a harness bug, not adding architectural
context.

## Experiment 2 — post-fix: signal disappears

Fix: `AiderHarness.runSession` now walks workdir for `.go`/`.py`/`.ts`/etc.
and appends `--file <rel-path>` for each. Re-ran the same 50 cells.

Run: 50 cells, $0.048 spent. **37/50 passes** (was 4/50).

| model | baseline | cortex | lift |
|---|---:|---:|---:|
| `anthropic/claude-haiku-4.5` | 4/5 (80%) | 4/5 (80%) | 0 |
| `google/gemma-4-26b-a4b-it:free` | 3/4 (75%) | 3/4 (75%) | 0 |
| `nvidia/nemotron-nano-9b-v2:free` | 4/4 (100%) | 3/5 (60%) | **−40** (n=4, n=5; noise) |
| `openai/gpt-oss-20b:free` | 4/4 (100%) | 4/4 (100%) | 0 |
| `qwen/qwen3-coder` | 4/5 (80%) | 4/5 (80%) | 0 |

Once the harness lets every model see the files, **cortex bullets show
no measurable lift** on these scenarios. The small free model
(`gpt-oss-20b:free`) saturates at 100% baseline — there's no headroom
for cortex to lift even if it tried.

## Per-scenario pass-rate (post-fix, both strategies pooled)

| scenario | passes / cells | observation |
|---|---:|---|
| `error-wrap` | 9/10 | grep-based verifier; trivial pattern |
| `fix-off-by-one` | 10/10 | clear inclusive-bounds contract; one-character fix |
| `fizzbuzz` | 9/10 | famous task; all models trained on it |
| `rename-json-tag` | 9/10 | pure-text edit; no logic |
| `add-table-test` | 0/5* | grep-count verifier requires ≥4 cases; models add 1–2 |

(*) `add-table-test` is the only differentiating scenario in the set —
it requires the agent to *count to four* and continue past its
default brevity. Even Haiku failed it because the grep expected ≥4
entries with `name:`, and Haiku added 2.

## What this means

1. **Harness bugs swamp content.** A single missing `--file` flag
   produced 30+ percentage-point swings — far larger than the cortex
   strategy effect on the same scenarios. Any future eval that
   doesn't first nail the harness wiring is measuring noise.

2. **The static-cortex prefix doesn't add information.** Bullets like
   "match the existing test patterns" or "don't change the function
   signature" restate things the agent can already see in the seed
   files. Once `--file` is wired, the bullets are redundant.

3. **These scenarios are too easy.** Pass-rates of 75–100% leave no
   room for any strategy to improve. Real cortex value would manifest
   in scenarios where:
   - The relevant context isn't in the visible files (cross-file
     conventions, deprecated approaches, prior decisions).
   - The right answer requires *not* doing something obvious (rejecting
     a reasonable-looking approach because the team rejected it
     earlier).
   - Multi-file edits where the agent has to understand which file is
     the source of truth.

## What would falsify cortex value or confirm it

The next experiment that would actually test the small-model amplifier
thesis needs:

- 5–10 scenarios where ≥1 file is *deliberately not* in the chat, and
  the cortex bullets summarize what's in it.
- Scenarios with ≥3 files that need consistent edits, where the
  cortex bullets enumerate the files (today's auto-discovery is
  too aggressive — it surfaces everything regardless of strategy).
- A "wrong-answer trap": bullets describe a non-obvious convention
  the agent would otherwise violate.

That's where cortex's information-injection should manifest as lift.
The current scenario set can't measure it because the cortex bullets
have no information advantage over the baseline view.

## Cost receipts

| Phase | Cells | Spend | Note |
|---|---:|---:|---|
| Mini-smoke (pre-fix, gpt-oss-20b only) | 5 | $0 | discovered the verify-failure pattern |
| Experiment 1 (full, pre-fix) | 50 | $0.035 | the misleading +30% lift |
| Solo Haiku post-fix | 5 | $0.021 | confirmed the --file fix works |
| Experiment 2 (full, post-fix) | 50 | $0.048 | the honest result |
| **Total** | **110** | **$0.104** | 2.1% of the $5 budget |

Plenty of headroom for the harder-scenario follow-up.
