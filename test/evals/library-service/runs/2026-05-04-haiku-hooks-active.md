# Library-service eval — 2026-05-04, Haiku 4.5, hooks active

**First end-to-end run of the library-service eval.** Snapshot of "the
pipeline works" rather than a definitive thesis result.

> **Update 2026-05-04 (post-investigation):** The "hooks-active" caveat
> below was overstated. Cortex's hooks live in
> `.claude/settings.local.json` (project-local), and Claude CLI loads
> them via cwd-based discovery. The eval's `claude` subprocesses run
> with `cwd=tempdir`, which is outside any project tree — so this
> project's Cortex hooks did NOT actually fire during the run. Verified
> by inspecting `~/.cortex/queue/pending/` for events from the 22:08-
> 22:26 window: every event came from the operator's interactive
> Claude Code session, none from the eval's subprocesses. Treat this
> run as effectively clean (modulo any global hooks the operator may
> have installed — none in this case).
>
> The `--bare` patch (commit 850e649) and its conditional successor
> (commit 3cba829) remain valuable as defensive hardening for setups
> with global hooks, plugins, or different cwd structures.

## Setup

- **Model**: `claude-haiku-4-5-20251001` (Anthropic-hosted via Claude CLI)
- **Auth**: Max subscription (`env -u ANTHROPIC_API_KEY`)
- **Harness**: `ClaudeCLIHarness` **without** `--bare` — user's installed
  Cortex hooks fired on every session in both conditions
- **Conditions**: baseline + cortex (no frontier reference yet)
- **Single sample** — no variance estimate
- **Wall-clock**: baseline 10m4s, cortex 7m11s
- **Cost**: $0 (Max subscription, no API credits used)

## Results

| Metric | Baseline | Cortex | Δ (cortex − baseline) |
|---|---:|---:|---:|
| Shape similarity (headline) | 0.955 | 0.993 | +0.038 |
| Naming adherence | 0.964 | 1.000 | +0.036 |
| Smell density (lower better) | 4.218 | 2.982 | +1.236 |
| Test parity | 1.000 | 1.000 | +0.000 |
| End-to-end pass rate | 0.840 | 1.000 | +0.160 |

**Headline shape-similarity lift:** +0.038 (marginal lift in the
report's verdict).

## Per-session activity

```
Baseline (10m4s total)
  S01 scaffold-and-books: 143s, 11 files
  S02 authors:             88s,  7 files
  S03 loans:              112s,  7 files
  S04 members:            145s,  7 files
  S05 branches:           111s,  7 files

Cortex (7m11s total)
  S01 scaffold-and-books:  96s, 8 files
  S02 authors:             55s, 5 files (preamble 3086 bytes)
  S03 loans:              103s, 5 files (preamble 4854 bytes)
  S04 members:             74s, 5 files (preamble 4834 bytes)
  S05 branches:            99s, 5 files (preamble 4817 bytes)
```

## Interpretation

1. **Both conditions scored very high.** Baseline shape similarity 0.955
   was already near the cohesive-fixture ceiling (1.000). Haiku is a
   frontier-class model; even without help, it reads existing code and
   matches conventions well.

2. **Cortex captured 84% of the available headroom.** The +0.038 lift
   on shape similarity is 84% of the 0.045 of room between baseline
   and ceiling. The pre-stated +0.2 lift target was structurally
   impossible because baseline left only +0.045 to take.

3. **All metrics moved in the right direction.** Cortex condition wins
   on every metric, not just shape similarity. Smell density 30% lower
   and end-to-end pass rate going from 84% → 100% (Cortex got every
   one of 25 endpoints right; baseline missed 4) are the most striking
   numbers — measurable functional improvement, not just stylistic.

4. **Cortex was faster** (7m vs 10m). With patterns pre-loaded, the
   model spent less time exploring and rediscovering. Token-efficiency
   story has signal.

## Caveats

- **Hooks-active methodology contaminates "baseline"**. The user's
  installed Cortex hooks fired in both conditions — passive Cortex
  capture was happening even in baseline. This run measured "explicit
  preamble + hooks" vs "hooks alone", not "Cortex" vs "no Cortex".
  Future runs use `--bare` (added in commit 850e649) to strip hooks
  for a clean comparison.
- **Single run**, no statistical variance. Could shift ±0.02 on a
  re-run.
- **Haiku is not the thesis model.** The small-model amplifier
  thesis is about local sub-10B-parameter models. Haiku is hosted
  frontier-class. Real thesis test requires AiderHarness +
  qwen2.5-coder via Ollama.

## What this snapshot is good for

- Proof that the eval pipeline works end-to-end against a real model.
- Lower-bound estimate of Cortex's lift even on a model that barely
  needs help.
- Reference point for comparing future methodology changes (--bare,
  multi-sample averages, etc.).

## What it is NOT

- Not a thesis-validating result — the model class is wrong.
- Not statistically meaningful — single sample.
- Not directly comparable to future `--bare`-based runs.

## Raw artifacts

The run JSONs (`baseline.json`, `cortex.json`, `compare.md`, `run2.log`)
were generated at `/tmp/cortex-libsvc-runs/` during the run. Not
checked in (large, ephemeral); reproduce with the driver if needed,
though the result will differ slightly post-`--bare`.
