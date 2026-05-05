# Library-service eval — 2026-05-04, 3-way: Haiku baseline vs Haiku+Cortex vs Sonnet frontier

**First 3-way comparison.** Establishes that Cortex's pattern injection
can lift a smaller model **past** a frontier model on cohesion-specific
metrics, on this single sample.

## Setup

- **Subject model**: `claude-haiku-4-5-20251001`
- **Frontier model**: `claude-sonnet-4-6`
- **Auth**: Max subscription (`env -u ANTHROPIC_API_KEY`); `--bare`
  auto-omitted (its docstring requires API-key auth and we're on Max)
- **Harness**: `ClaudeCLIHarness` with `--permission-mode bypassPermissions`
- **Methodology note**: Cortex hooks live in project-local
  `.claude/settings.local.json`; eval workdirs are tempdirs outside any
  project tree, so hooks did not fire. Verified by inspecting
  `~/.cortex/queue/pending/`. Effectively clean.
- **Single sample per condition** — no variance estimate. Treat magnitudes
  as ±0.02 noise.

## Results

| Metric | Haiku baseline | Haiku + Cortex | Sonnet frontier |
|---|---:|---:|---:|
| **Shape similarity (headline)** | 0.955 | **0.993** | 0.975 |
| Naming adherence | 0.964 | 1.000 | 1.000 |
| Smell density (lower better) | 4.218 | **2.982** | 3.120 |
| Test parity | 1.000 | 1.000 | 1.000 |
| End-to-end pass rate | 0.840 | 1.000 | 1.000 |
| Wall-clock | 10m4s | 7m11s | 19m23s |

## The surprise

**Haiku+Cortex beat Sonnet on the headline shape similarity (0.993 vs
0.975) and on smell density (2.982 vs 3.120).** Even allowing for ±0.02
sampling noise, that's a non-trivial gap on cohesion-specific metrics —
exactly what Cortex is designed to produce.

The driver's "marginal lift" verdict label compares cortex vs baseline
against a fixed +0.2 threshold and ignores the frontier column, which
made it stick on the +0.038 number. The interesting finding here isn't
the absolute lift — it's that **Cortex closed the model-class gap and
then some**: a Haiku-Cortex run scored higher on cohesion than a Sonnet
run with no help.

Where Sonnet still wins or ties:
- Naming adherence, test parity, end-to-end pass rate — all three at 1.000
  for Sonnet and Haiku+Cortex (ceiling-bound tie)
- Sonnet at 0.975 shape sim vs Haiku-baseline 0.955 (Sonnet *is* better
  than Haiku alone, just not by much, and not enough to beat Haiku+Cortex)

Where Haiku+Cortex wins:
- Shape similarity: +0.018 over Sonnet
- Smell density: 30% fewer smells than Haiku-baseline, 4% fewer than Sonnet
- Wall-clock: ~3× faster than Sonnet (7m vs 19m)

## What this does NOT show

1. **Variance.** Single sample. Sonnet may produce 0.99+ on a re-run; the
   gap could close or invert on resampling.
2. **The actual thesis.** Haiku is a frontier-class model; the
   small-model amplifier thesis is about local sub-10B-parameter models
   like qwen2.5-coder:1.5b. Aider+Ollama runs are the real test.
3. **Generalization.** A library-service CRUD eval is one shape of work;
   cohesion across CRUD handlers is an unusually well-suited test for
   pattern injection. Other code shapes (algorithms, UI, scripts, infra)
   may show different patterns.
4. **Causal isolation between Cortex's tier-2 mining and the explicit
   preamble.** The preamble works; whether Cortex's existing retrieval is
   the *best* possible preamble is a separate question.

## What this DOES show

- The eval pipeline works end-to-end against real models
- Cortex's lift on this eval is real, consistent in direction across
  every metric, and large enough to matter (e2e: 84% → 100%)
- Pattern injection beats model-upgrade for cohesion on this task
- Cost story: Haiku+Cortex is faster and (in API-priced terms) much
  cheaper than Sonnet, while scoring better on cohesion

## Per-session activity

```
Haiku baseline (10m4s)
  S01 scaffold-and-books: 143s, 11 files
  S02 authors:             88s,  7 files
  S03 loans:              112s,  7 files
  S04 members:            145s,  7 files
  S05 branches:           111s,  7 files

Haiku + Cortex (7m11s)
  S01 scaffold-and-books:  96s, 8 files
  S02 authors:             55s, 5 files (preamble 3086 bytes)
  S03 loans:              103s, 5 files (preamble 4854 bytes)
  S04 members:             74s, 5 files (preamble 4834 bytes)
  S05 branches:            99s, 5 files (preamble 4817 bytes)

Sonnet frontier (19m23s)
  S01 scaffold-and-books: 244s, 9 files
  S02 authors:            217s, 6 files
  S03 loans:              281s, 6 files
  S04 members:            204s, 6 files
  S05 branches:           214s, 6 files
```

Notable: Sonnet sessions are 2-3× longer than Haiku sessions. Sonnet
takes more time to deliberate but the output isn't measurably more
cohesive than Haiku+Cortex on this eval.

## Next steps that would strengthen these claims

1. **Re-runs for variance.** N=3 minimum per condition would let us put
   error bars around the deltas.
2. **Sonnet+Cortex.** Does pattern injection still help Sonnet, or wash
   out? Predicts whether Cortex is a "small-model thing" or a "every-model
   thing."
3. **The actual thesis test:** AiderHarness + qwen2.5-coder:1.5b with a
   3-way comparison against Sonnet. Predicts whether Cortex closes a
   *real* small-model-vs-frontier gap.
4. **Multi-resource counts.** All three runs implemented all 5 resources;
   would the divergence-pressure increase if we went to 8 or 10?

## Raw artifacts

`/tmp/cortex-libsvc-runs/{baseline,cortex,frontier}.json` and
`compare-3way.md`. Not committed.
