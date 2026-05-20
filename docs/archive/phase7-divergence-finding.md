# Phase 7 TODO 11 — Cross-harness divergence > 30 pp on baseline

> **Status: stop-condition triggered.** TODO 11's pass criterion is
> < 20 pp baseline divergence across harnesses. Observed: 40 pp.
> The Phase 7 prompt's stop-condition list explicitly halts the loop
> on > 30 pp divergence pending investigation. This doc is that
> investigation.

## What was run

```
CORTEX_EVAL_ALLOW_SPEND=1 CORTEX_EVAL_NO_FREE_PREFERENCE=1 \
cortex eval grid \
  --scenarios test/evals/coding \
  --harnesses aider,opencode,pi_dev \
  --models    openai/gpt-oss-20b:free \
  --strategies baseline
```

15 cells (5 scenarios × 3 harnesses × 1 model × 1 strategy).
All cells completed (no panics, no harness failures).

## Per-harness pass rate (baseline)

| Harness  | Passes | Pass rate |
|----------|--------|-----------|
| aider    | 4/5    | 80 %      |
| opencode | 2/5    | 40 %      |
| pi_dev   | 4/5    | 80 %      |

**Divergence**: 80 − 40 = **40 pp** (max − min across harnesses).

## Per-scenario results

| Scenario          | aider | opencode | pi_dev |
|-------------------|-------|----------|--------|
| add-table-test    | ✗     | ✗        | ✗      |
| error-wrap        | ✓     | ✓        | ✓      |
| fix-off-by-one    | ✓     | ✓        | ✓      |
| fizzbuzz          | ✓     | ✗        | ✓      |
| rename-json-tag   | ✓     | ✗        | ✓      |

opencode loses two scenarios (fizzbuzz, rename-json-tag) that the
other two harnesses pass.

## Root cause

Re-ran rename-json-tag through opencode in isolation. The stdout
contains a non-JSON line:

```
! permission requested: external_directory
  (/private/var/folders/l7/gxhw1xgs6mx0cdgg8nd6ml1w77526e0/*); auto-rejecting
```

The workdir created by the grid runner was
`/var/folders/l7/gxhw1xgs6mx0cdgg8nd6ml1m0000gn/T/tmp.98U8e5O4Ao` —
but the model's tool call referenced a different path
(`...ml1w77526e0/*`). The model **hallucinated** a path that doesn't
match the real workdir. opencode's permission system correctly
flagged it as external to `--dir` and auto-rejected it. The model
made no follow-up attempt and exited with a "done" reply that didn't
actually edit anything.

This is a model-behavior difference, surfaced by the harness:

- **aider** has no per-tool path-permission gate. When the model
  passes a bad path, aider returns a "file not found" error and the
  model tries again, eventually landing on the right path.
- **pi_dev** lets the model's `bash` / `read` / `edit` tools resolve
  paths relative to `cmd.Dir`. Bad paths produce empty results, and
  the model recovers similarly.
- **opencode** treats any path outside `--dir` as an external
  directory request requiring explicit permission. Auto-reject in
  non-interactive mode kills the recovery loop.

This is `gpt-oss-20b:free` behaving worse through opencode's tool
contract than through aider's / pi's — same underlying model, same
prompt, different "shape" of the harness surface produces different
recovery behavior.

## Is this a harness wiring bug?

**No, this is the cross-harness ablation working as designed.** The
whole point of Phase 7 is to disambiguate "Cortex helps the model"
from "this particular CLI surface shape gets lucky with this model".
The 40 pp gap is a real, reproducible model-on-harness sensitivity
that the eval harness is supposed to measure.

It does, however, exceed the prompt's 30 pp halting threshold, so
the iteration protocol stops here per the documented contract.

## Mitigation options (for user decision)

1. **Accept and proceed**: file the 40 pp as the headline cross-
   harness finding. The Phase 7 deliverables are sound; the wide
   gap is the first interesting cross-harness data point and should
   be reported as such in `docs/eval-resume-prompt.md`'s MECE matrix
   update (TODO 12).
2. **Re-run with a stronger model**: `claude-haiku-4.5` or
   `qwen/qwen3-coder` are less likely to hallucinate paths. If the
   gap narrows to < 20 pp with a stronger model, that supports
   reporting Phase 7 as "wired + measured" and the 40 pp on
   gpt-oss-20b:free as a small-model fragility note. Requires
   `CORTEX_EVAL_ALLOW_SPEND=1` and (for Sonnet/Opus)
   `CORTEX_EVAL_ALLOW_FRONTIER=1`.
3. **Set opencode's permission policy to allow `--dir`-resolved
   paths broadly**: would close the gap on this specific model but
   masks a real model-fragility signal that the eval ought to surface.
   Probably the wrong choice.

## Files referenced

- `docs/eval-harness-phase7-prompt.md` — Phase 7 spec / pass criteria
- `internal/eval/v2/library_service_opencode_harness.go` — harness
  under test (no changes needed; auto-reject line ships in stdout
  as the parser's expected "non-JSON line, skip silently" path)
- `internal/eval/v2/grid.go` — grid runner (creates workdir under
  `os.MkdirTemp("", "cortex-grid-cell-")`; on macOS this lands under
  `/var/folders/...`, which gets symlink-resolved to `/private/var/folders/...`
  inside subprocesses — this is normal macOS, not a runner bug)
