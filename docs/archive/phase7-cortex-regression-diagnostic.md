# Cortex-strategy regression — diagnostic findings

> Follow-up to `docs/phase7-crossharness-lift.md`. Goal: read what
> cortex's static bullets actually did to the agent loops in the
> regression cells, instead of guessing.

## Method

Paired reproduction of two regression cells from the 30-cell run:

| Pair | scenario × harness | bg-run baseline | bg-run cortex |
|------|---------------------|-----------------|---------------|
| A    | `fix-off-by-one × opencode` | PASS, 65 s | FAIL, 34 s |
| B    | `error-wrap × pi_dev`       | PASS, 87 s | FAIL, 215 s |

Each pair re-run in isolation (n=1), seed copied to a scratch dir,
prompt constructed with and without the `RELEVANT CONTEXT: …\n\nTASK:`
prefix that `buildCortexPrefix` produces, raw stdout captured. Outputs
in `/tmp/diag-results/`.

## Result

**Pair A (opencode):** **Did not reproduce.** Baseline passed (as
expected). Cortex passed this time. Tool-name distribution clean in
both: `glob`, `read`, `edit`. → small-model variance at n=1; the
opencode regression in the bg run may have been a one-off.

**Pair B (pi_dev):** **Reproduced.** Baseline passed; cortex failed.
The diff between final `load.go` files is decisive:

- **Baseline**: model imported `"fmt"` and replaced the bare
  `return nil, err` with `return nil, fmt.Errorf("LoadConfig(%s): %w", path, err)`.
- **Cortex**: model **never edited the file**. `load.go` is
  byte-identical to the seed.

## Why cortex didn't edit (root cause)

Tool-name distribution from the pi.dev `tool_execution_end` events:

| toolName                       | baseline | cortex |
|--------------------------------|---------:|-------:|
| `edit`                         | 1        | 0      |
| `read`                         | 1        | 14     |
| `bash`                         | 1        | 0      |
| `edit<\|channel\|>commentary`  | 1        | 5      |
| `read<\|channel\|>commentary`  | 0        | 3      |
| `bash<\|channel\|>commentary`  | 0        | 1      |

The `<|channel|>commentary` suffix is the **harmony-format channel
marker** that gpt-oss models emit when their output goes through the
`commentary` reasoning channel instead of `final`. In the cortex run
the marker leaked into the tool-name field on **9 of 23 tool calls**.
pi.dev couldn't dispatch a tool named `edit<|channel|>commentary`
(returns "Tool not found"), the model retried, and fell into a
read → try-edit-with-bad-name → fail → read loop. 0 successful
`edit` calls in the entire 215-second session.

In the baseline run the model emitted exactly one channel-tagged
edit, recovered on the next turn with a clean `edit`, and finished.

## Why this *isn't* "the bullets read as directives"

The hypothesis in `docs/phase7-crossharness-lift.md` was: cortex's
hand-authored bullets read as imperatives inside multi-turn agent
loops; the agent satisfies them literally and conflicts with the
verifier.

**That hypothesis is wrong** for pi.dev × `error-wrap` × cortex —
the model never finished a single tool call that touched a file.
It wasn't "doing the wrong thing" with the cortex bullets; it
couldn't do anything because its output format kept getting
mangled.

The actual mechanism on pi.dev is **prompt-shape destabilization of
the model's channel selection**: the `RELEVANT CONTEXT: …` preamble
seems to push gpt-oss-20b into emitting commentary-channel output
more often, and pi.dev's tool routing doesn't strip the channel
suffix before dispatch.

opencode appears to handle the same model's output cleanly — no
channel-tagged tool names in either pair-A run. Probably opencode's
tool layer strips the harmony channel marker before its dispatch
table lookup, or it uses a different completion protocol. (Not
fully traced — would need to look at opencode internals or run more
pair-A samples to confirm the opencode regression mechanism.)

## What this changes about the fix

The recommendation in `docs/phase7-crossharness-lift.md` was:
*"author harness-aware cortex bullets, or rephrase the existing ones
for multi-shape use."* That's the wrong fix for the pi.dev failure.
Rephrasing the bullets won't keep the model from emitting
`<|channel|>commentary` suffixes; the issue is in *whether the
harness can survive harmony-format tool names*, not *what's in the
bullets*.

### Three orthogonal fixes, ordered by leverage

1. **PiDevHarness pre-processing** — wrap stdout before parse:
   `s/<\|channel\|>[^"]*//g` on `toolName` and the corresponding
   `args.tool` field. Local fix, doesn't require upstream pi.dev
   changes. Should keep gpt-oss models working through pi.dev
   reliably. ~30-minute change.
2. **Avoid harmony-format models on pi.dev** for the cross-harness
   matrix. `claude-haiku-4.5`, `qwen3-coder`, etc. don't emit
   channel markers. This sidesteps the issue but loses the
   "free model" axis.
3. **Upstream report to pi.dev** — request channel-marker
   stripping in the tool dispatcher. Right fix, slowest path.

### What the original hypothesis still might explain

The opencode regressions in the bg run (3 cortex failures, 1
reproduced clean here) might still be the "bullets-as-directives"
mechanism, or might be plain n=1 variance. To know, run pair-A
3× per cell and look at the failures' tool sequences. Until that's
done, the original hypothesis remains untested for opencode
specifically.

## Suggested next move

Implement fix #1 (PiDevHarness channel-marker stripping) and re-run
the 30-cell grid. If the pi.dev cortex pass-rate jumps from 2/5
back toward 4/5, the diagnosis is confirmed AND we've fixed the
biggest piece of the cross-harness regression. Cost: ~30 min build
+ ~35 min re-run = ~1 hour, $0. The data already shows pi.dev
baseline passes 4/5 — cortex should at least match that if the
harness can dispatch the tool calls.
