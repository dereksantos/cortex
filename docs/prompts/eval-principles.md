# Eval Principles

All of our evals as a principle and constraint MUST use the `cortex` cli tool and treat it as a black box. Otherwise, evals are useless because they will be coached by the code that wraps the eval runner. This is a major problem in cortex right now and stunts progress significantly.

These are the canonical principles. The PR-review checklist and remediation playbook lives in [`docs/benchmarks/integrity.md`](../benchmarks/integrity.md). Every run gets logged in [`docs/eval-journal.md`](../eval-journal.md).

---

## 1. Evals must treat cortex as a black box, and use its cli as the public interface

No running evals with explicit internal wrappers. They must use cortex as is, as intended through the same cli interface developers and agents would use. Otherwise, we are not truly testing the right thing.

**What this means in practice:** benchmark runners invoke `cortex <cmd>` as a subprocess. They do not import `internal/` packages. They do not instantiate `internal/storage.Storage`, `internal/capture.Capture`, `internal/cognition.Reflect`, or `evalv2.CortexHarness` directly. The harness must be the same one a human or agent would use from a terminal.

## 2. Evals must not be coached

An eval should be isolated, starting with a clean slate every time for the task, unless the benchmark explicitly is designed to include pre-existing context. Any specific wrappers around an eval that may coach the results degrade the data the eval was designed to provide.

**Coaching vs framing.** Framing declares the task ("you are answering questions about conversations"). Coaching teaches the model how to use cortex ("call `cortex_search` with the most distinctive terms from the question"). Framing is fine. Coaching is not. The test: would a CLI user receive this instruction naturally, or are we hand-feeding cortex usage to make the numbers look better?

Benchmark-specific tool-registry overrides (e.g. forcing `cortex_search` off in a "baseline" arm by toggling an internal flag) are coaching too — if the toggle is real, it must exist as a CLI flag that users could set themselves.

## 3. Evals must be graded fairly and honestly

Do not fudge the numbers or tamper the results such that the value of the eval is attenuated. Evals must be run objectively, with the goal of providing trustworthy data to the developers.

This includes **emitting versioning metadata** alongside every score: embedder ID, LLM model name, index schema version, rerank state, judge model. A regression vs. a model upgrade is indistinguishable without provenance, and "0.42 NDCG@10" six months later is meaningless if we can't reconstruct what stack produced it.

## 4. Close CLI gaps — do not work around them

When a benchmark needs functionality cortex doesn't yet expose via its CLI, the answer is **never** to reach into `internal/`. The answer is to close the gap.

**Why:** every internal-wrapper workaround relaxes principle 1 silently, and the workaround tends to become permanent. The benchmark ships, the tech debt stays, and the next benchmark cites it as precedent. The principle erodes one expedient PR at a time.

**How to apply:** when a benchmark PR discovers a missing CLI surface (e.g. `cortex embed`, `cortex search-vector`, `cortex capture --bulk`), stop coding the benchmark. File a CLI feature issue. Block the benchmark PR until the CLI feature lands in `main`. Resume the benchmark using the new CLI command.

The only acceptable exception is a documented performance escape hatch (e.g. bulk hydration where per-row subprocess overhead would dominate runtime) — and even then, the CLI feature must be filed and tracked, and the workaround removed once the CLI lands.

## 5. Evals must be reproducible

Given the same inputs and the same seed, a benchmark MUST produce the same result. This requires:

- All randomness seeds documented and pinned.
- Embedder, LLM, and index schema versions emitted as part of the result (see principle 3).
- Caches that span runs (e.g. reused embedding indexes) MUST be keyed by version. A silent cache hit across an embedder change is a fudged number.

## 6. Evals must be isolated

Each benchmark instance MUST run in its own state directory. Never read or write to the user's real `~/.cortex` or the developer's working repo's `.cortex/`. If isolation cannot be established (e.g. the harness has no way to point at a fresh workdir), the benchmark MUST fail loudly rather than silently contaminate user state.

## 7. Evals must emit structured outputs

Every `CellResult` MUST include machine-readable metadata: strategy name, retrieval mode, embedder, model, rerank state, judge model, anything that varies between runs. Analysis pipelines should be able to group, filter, and compare without parsing prose in a notes field. Assume the analysis pipeline from day one — it does not get bolted on later.

## 8. LLM-judged evals must include variance

Any benchmark where scoring depends on an LLM (judge, grader, classifier) MUST run instances multiple times with different seeds or different judges and report mean ± standard deviation, not a single point estimate. A flaky judge with ±5 variance makes a 7/10 score meaningless without error bars.

## 9. Multi-strategy evals must separate baselines

When a benchmark compares strategies (baseline vs. cortex vs. frontier), each strategy MUST emit its own `CellResult` row. Do not aggregate strategies into one score; do not compute deltas inside the runner. The analysis layer compares; the runner reports.

---

## Current compliance

The doc serves as an active TODO list — flip ✗ to ✓ as benchmarks land. See [`docs/benchmarks/integrity.md`](../benchmarks/integrity.md) for the PR #32 retrospective and per-benchmark remediation playbook.

| Benchmark | Black box (1) | No coaching (2) | Versioned (3) | Reproducible (5) | Isolated (6) | Structured (7) |
|---|---|---|---|---|---|---|
| MTEB | ✗ uses `internal/storage`, `intcognition.Reflect` | ~ `--rerank` is opt-in | ~ partial | ✓ | ✓ | ~ partial |
| NIAH | ✓ shells out via `benchmarks.RunBulkCapture/RunIngest/RunSearch` | ✓ | ~ | ✓ | ✓ | ~ |
| LongMemEval | ✗ uses `evalv2.CortexHarness` in-process | ✗ system prompt coaches `cortex_search` usage | ~ | ~ | ✓ | ~ |
| SWE-bench | ✗ uses `evalv2.CortexHarness` in-process | ✗ `SetCortexSearchEnabled(false)` toggles tool registry | ~ | ~ | ✓ | ~ |
| Library-service | ✓ shells out to `cortex search` | ✓ | ~ | ✓ | ✓ | ~ |

**CLI surfaces landed** (per principle 4): `cortex capture --bulk`, `cortex search --workdir --json`, `cortex ingest --workdir`, `cortex code --no-search`. Shared subprocess helpers live in `internal/eval/benchmarks/cortexcli.go`.

**CLI surfaces still needed**: `cortex code --json` (for SWE-bench conversion — agent telemetry), `cortex embed` + `cortex search-vector` (for MTEB conversion — direct embedding/retrieval substrate).
