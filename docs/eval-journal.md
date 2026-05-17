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
