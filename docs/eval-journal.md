# Eval Journal

A human-readable log of eval runs — what we ran, why, what we noticed. The structured record lives in `.cortex/journal/eval/` (`eval.cell_result` JSONL) and is the canonical source for analysis. This file is the lab notebook around those numbers.

Principles: [`docs/prompts/eval-principles.md`](prompts/eval-principles.md). Operational checklist: [`docs/benchmarks/integrity.md`](benchmarks/integrity.md).

## How to use this journal

- **Every eval run gets an entry.** Even failed runs, even runs we discarded — write down why.
- **Newest at the top.** Reverse chronological. Past entries are immutable; corrections go in a new entry that references the old one.
- **Quote the actual command.** Per principle 1, the CLI invocation IS the eval. Paste it verbatim; never paraphrase.
- **Capture versions.** Per principle 3, scores without provenance are meaningless six months later.
- **Note hypothesis vs. surprise.** What did you expect? What actually happened? Surprises are the high-signal moments worth coming back to.

## Entry template

```markdown
### YYYY-MM-DD — <benchmark> / <variant>

**Cortex**: `<git SHA or branch>`
**Command**:
\`\`\`
./cortex eval ...   # or actual subprocess invocation
\`\`\`
**Versions**: embedder=`<provider/model>`, llm=`<model>`, judge=`<model>`, rerank=`<true|false>`
**Result**: `<primary metric>` (full results in `.cortex/journal/eval/<segment>`)

**Why this run**: one sentence — what changed, what hypothesis.

**Observations**: what stood out. Bullet points fine.

**Follow-ups**: issues filed, next runs queued, principles flagged.
```

## Entries

<!-- Newest at the top. -->

### 2026-05-17 — SWE-bench (verified, limit=5) / baseline vs cortex

**Cortex**: `533ca06` (branch `derek.s/dag-build`); `cortex_version=0.1.0`
**Command**:
```
CORTEX_BINARY=$PWD/bin/cortex \
CORTEX_EVAL_RUN_USD_CEILING=25 \
CORTEX_EVAL_DAILY_USD_CEILING=25 \
CORTEX_EVAL_LIFETIME_USD_CEILING=25 \
./bin/cortex eval --benchmark swebench --subset verified --limit 5 \
  --strategy baseline,cortex --model anthropic/claude-haiku-4.5
```
**Versions**: provider=`openrouter`, llm=`anthropic/claude-haiku-4.5`, judge=n/a (SWE-bench uses `tests_pass_all`, i.e. Docker test-suite execution, not LLM judging), scoring images = `swebench/sweb.eval.x86_64.<repo>:v<n>`, `cortex_version=0.1.0`.
**Result**:

| Strategy | n | Pass | Pass rate | Total cost | Avg latency | Avg tokens (in/out) | Avg turns | Avg injected ctx |
|---|---|---|---|---|---|---|---|---|
| baseline | 6 | 0 | 0.0% | $1.294 | 28.4 s | 205200 / 2086 | 13.5 | 0 |
| cortex   | 5 | 0 | 0.0% | $1.059 | 26.4 s | 202601 / 1849 | 14.2 | 0 |

(baseline n=6 includes one extra cell from the probe run; cortex n=5 is the clean limit-5 sweep.)

Per-instance F2P/P2P (all instances from `astropy/astropy`, the alphabetically-first repo in the verified subset):

| Instance | strat=baseline | strat=cortex |
|---|---|---|
| astropy-12907 | F2P=0/2, P2P=0/13 | F2P=0/2, P2P=0/13 |
| astropy-13033 | F2P=0/1, P2P=0/20 | F2P=0/1, P2P=0/20 |
| astropy-13236 | F2P=0/2, P2P=0/644 | F2P=0/2, P2P=0/644 |
| astropy-13398 | F2P=0/4, P2P=0/68  | F2P=0/4, P2P=0/68 |
| astropy-13453 | F2P=0/1, P2P=0/9   | F2P=0/1, P2P=0/9 |

Per-cell records in `.cortex/db/cell_results.jsonl` (11 swebench rows total).

**Why this run**: Phase A Step 3 — establish a pre-integration SWE-bench Verified baseline.

**Observations**:
- **0% pass on both strategies.** The agent runs 13–14 turns per instance, emits a patch, and the patch fails all F2P tests in the scoring container every time. Same set of 5 astropy instances passes/fails identically across strategies, modulo a small token/turn delta.
- **Cortex strategy still injects 0 context.** Same finding as LongMemEval: store is empty pre-integration, so "cortex" cell is "baseline + search-tax". The 2-second latency reduction and slightly fewer output tokens on the cortex side are noise at n=5.
- **Repo coverage is narrow.** `--limit 5` against the alphabetical-by-instance-id verified subset picked only astropy. A representative SWE-bench Verified baseline needs `--repo` rotation across the 12 repos in the subset. Not done here to respect both the cost ceiling and per-loop scope.
- Scoring container worked first try (Docker is up; `swebench/sweb.eval.x86_64.astropy__astropy:{v4.3,v5.0}` images pulled and executed).
- One legitimate principle-1 confirmation: the harness only sees `cortex` as a black box (`internal/eval/benchmarks/cortexcli.go`); F2P numbers come from running the test suite in the Docker image, not from the agent self-reporting.

**Follow-ups**:
- Cross-repo sweep (one limit-1 per repo × 12 repos × 2 strategies ≈ 24 cells, est. $5) for a representative Verified baseline. Defer until cost estimator is recalibrated.
- 0% baseline is consistent with claude-haiku-4.5's reported SWE-bench Verified pass rate — to expose any DAG-protocol delta we'll want a stronger model (e.g. `anthropic/claude-sonnet-4.5`) in Phase 6 comparison, not just haiku.
- Same `cortex-fast / cortex-full` taxonomy gap as LongMemEval — see that entry's follow-ups.

---

### 2026-05-17 — LongMemEval (oracle, limit=25) / baseline vs cortex

**Cortex**: `f815d06` (branch `derek.s/dag-build`); `cortex_version=0.1.0`
**Command**:
```
CORTEX_BINARY=$PWD/bin/cortex \
CORTEX_EVAL_RUN_USD_CEILING=25 \
CORTEX_EVAL_DAILY_USD_CEILING=25 \
CORTEX_EVAL_LIFETIME_USD_CEILING=25 \
./bin/cortex eval --benchmark longmemeval --subset oracle --limit 25 \
  --strategy baseline,cortex --judge -m anthropic/claude-haiku-4.5
```
**Versions**: provider=`openrouter`, llm=`anthropic/claude-haiku-4.5` (also used as judge per `internal/eval/benchmarks/longmemeval/judge.go:13`), rerank=n/a (LongMemEval doesn't use rerank), `cortex_version=0.1.0`. Dataset: `~/.cortex/benchmarks/longmemeval/longmemeval_oracle.json` (HuggingFace `xiaowu0162/longmemeval-cleaned`, 500 questions, sorted by QuestionID; `--limit 25` takes the first 25).
**Result** (aggregated across this run + the prior limit=5 probe, 62 cells total):

| Strategy | n | Pass | Pass rate | Total cost | Avg latency | Avg tokens (in/out) | Avg injected ctx |
|---|---|---|---|---|---|---|---|
| baseline | 32 | 5 | 15.6% | $0.0550 | 1808 ms | 1214 / 101 | 0 |
| cortex   | 30 | 4 | 13.3% | $0.0559 | 2049 ms | 1327 / 107 | 0 |

Per-axis (latest 50 cells; n is per-strategy):

| Axis | baseline | cortex |
|---|---|---|
| single-hop        | 1/8 | 1/8 |
| multi-hop         | 1/7 | 1/7 |
| knowledge-update  | 1/8 | 1/8 |
| temporal          | 2/2 | 1/2 (n too small) |

Per-cell records in `.cortex/db/cell_results.jsonl` (62 rows) and `.cortex/journal/eval/0001.jsonl`.

**Why this run**: Phase A Step 2 — establish a pre-integration LongMemEval baseline so Phase 6 has a real "before" picture.

**Observations**:
- **Cortex strategy injects zero context** (`injected_context_tokens=0` on every cortex cell). The persistent store is empty pre-integration, so the "cortex" cell pays a search-overhead tax (+241 ms avg, +9% tokens_in) without retrieving anything. Pass-rate parity is the *only* honest reading; the small baseline-better delta is within noise.
- Judge wiring works (`task_success_criterion=judge_llm`; representative `notes`: "The candidate refuses to answer and claims lack of access to information, while the gold answer provides specific concrete facts (4 engineers initially, 5 now)…"). Failures are real model abstentions, not tool errors.
- **Cost estimator is ~250× pessimistic.** `spend.EstimateCost` projects $0.45/cell for `anthropic/claude-haiku-4.5`; actual cell cost ≈ $0.0017–$0.0018. All three default ceilings ($5 run / $5 daily / $5 lifetime) had to be raised to $25 to permit 50 instances, even though real spend totalled $0.11. Worth recalibrating before larger Phase A sweeps.
- **Strategy taxonomy gap (principle 8).** `internal/eval/v2/cellresult.go:44` only allows `StrategyBaseline / StrategyCortex / StrategyFrontier`. There is no `cortex-fast` vs `cortex-full` split in the cell schema, so the loop's principle 8 "no-context / Cortex-Fast / Cortex-Full as 3 distinct rows" is **structurally unsupported** today — for *all* benchmarks, not just v2.
- Provider routing surprise repeats here: `-p anthropic` → `pkg/llm/client.go:137` resolves OpenRouter first (keychain `cortex-openrouter`), so OpenRouter-style model id is mandatory even with `-p anthropic`. Recorded in the v2 entry below.

**Follow-ups**:
- Phase 1 / DAG protocol: add a `cortex-fast` vs `cortex-full` axis to `evalv2.CellResult.ContextStrategy` so principle 8 becomes expressible.
- Calibrate `spend.EstimateCost` for `anthropic/claude-haiku-4.5` (current 250× over-estimate forces ceiling overrides for routine sweeps).
- Decide before Phase 6: do we pre-ingest each LongMemEval haystack into the cortex store before scoring the "cortex" cell? Without that the "cortex" strategy is just baseline-with-search-tax and the post-DAG delta will be uninterpretable.
- Larger N sweep (e.g. 100 per axis) is cheap (~$0.50 total) and worth running once the ceiling estimator is fixed.

---

### 2026-05-17 — v2 suite / BLOCKED: needs Phase 1 unified telemetry

**Cortex**: `55d7427` (branch `derek.s/dag-build`)
**Command**:
```
./bin/cortex eval -s test/evals/v2/auth-patterns.yaml -p anthropic -m anthropic/claude-haiku-4.5 -v
```
**Versions**: provider=`openrouter` (keychain `cortex-openrouter`), llm=`anthropic/claude-haiku-4.5`, judge=none, rerank=false
**Result**: BLOCKED — see Observations.

**Why this run**: Phase A Step 1 — establish a v2 baseline. Probed with one scenario before committing to all 40, per the loop's "verify telemetry first" gate.

**Observations**:
- Single-scenario probe succeeded as a *legacy* eval: `auth-patterns` reported avg lift +33%, ABR 0.67, 3/3 tests ran, baseline 1464 → cortex 1107 tokens (-24%).
- Persistence landed in `.cortex/db/evals_v2.db` table `eval_scenario_results` (legacy schema) only — **not** in `.cortex/db/cell_results.jsonl`, **not** in the `cell_results` SQLite table, **not** in `.cortex/journal/eval/*.jsonl` (segment `0001.jsonl` is 0 bytes after the run).
- Call site confirms the gap: `cmd/cortex/commands/eval_benchmark.go:141` is the only caller of `evalv2.Persister.PersistCell` (the writer that fans out to journal + SQLite `cell_results` + JSONL). The v2 scenario runner in `internal/eval/v2/` does not invoke it; it writes the older per-scenario summary row instead.
- Independent principle-8 gap: v2 runner produces only `baseline` vs `cortex` cells; it does not emit separated `no-context / Cortex-Fast / Cortex-Full` rows the loop requires.
- Provider routing surprise (not a blocker, worth recording): `-p anthropic` routes through OpenRouter when the keychain key is present (resolution order at `pkg/llm/client.go:137`), so the OpenRouter-style model id (`anthropic/claude-haiku-4.5`) is required even with `-p anthropic`. The direct Anthropic model id (`claude-haiku-4-5-20251001`) returned `openrouter (400): … is not a valid model ID`.

**Follow-ups**:
- Phase 1 of `docs/integration-roadmap.md` (unified `cell_results.jsonl` for ad-hoc CLI invocations) is the prerequisite. Until it lands, all 40 v2 scenarios will fail principle 6 (Structured) the same way; skipping the full sweep for now.
- Independent Phase-A item to file: extend the v2 runner to emit Fast vs Full as distinct rows so principle 8 (Separated baselines) can be honored even after the telemetry sink lands.
- The legacy run is retained in `eval_scenario_results` for reference; do **not** treat it as the Phase A v2 baseline.
