# LongMemEval — Oracle split (Phase A)

LongMemEval (Wu et al., MIT-licensed) is a published benchmark for
long-horizon conversational memory. It probes five ability axes:

| Axis | Upstream `question_type` strings | What it tests |
|---|---|---|
| `single-hop` | `single-session-user`, `single-session-assistant`, `single-session-preference` | One-shot recall of an explicitly stated fact |
| `multi-hop` | `multi-session` | Combining facts across multiple sessions |
| `temporal` | `temporal-reasoning` | Reasoning over when a fact became true |
| `knowledge-update` | `knowledge-update` | Tracking when a fact changes |
| `abstention` | `abstention` | Refusing to invent an answer absent in context |

This package wires the **Oracle split** end-to-end through Cortex's
in-process coding harness with `cortex_search` enabled, scoring each
response via an LLM-as-judge. Oracle is the smallest split (evidence
sessions only); it produces the first cross-comparable industry number
on Cortex's memory thesis at a tolerable cost.

## Dataset

- **Source**: `xiaowu0162/longmemeval-cleaned` on HuggingFace Hub
- **License**: MIT
- **Vendored?** No — the loader fetches `longmemeval_oracle.json` at
  runtime via the benchmark cache (`~/.cortex/benchmarks/longmemeval/`)
  on first invocation and reuses it thereafter.
- **Provenance**: Wu et al., *LongMemEval: Benchmarking Chat Assistants
  on Long-Term Interactive Memory*, 2024.

S and M splits are reserved for **Phase B**. The loader rejects them
with an explicit `Phase B: subset "..." not yet wired` message so the
operator sees a clear failure mode rather than a partial result.

## How the runner works

Per (question × strategy):

1. **Workdir**: Fresh temp directory; `.cortex/` is mkdir'd inside.
2. **Hydration** (cortex strategy only): each turn of each session in
   `haystack_sessions` is captured in-process via `internal/capture`
   into the journal, then drained to storage via
   `internal/processor.RunBatch`. The same writes the `cortex` CLI
   would have made — kept in-process to avoid the per-turn shell cost
   that would dominate Oracle-split latency.
3. **Harness**: `evalv2.NewCortexHarness(model).RunSessionWithResult`
   drives the in-process agent loop with the question as the user
   message. `cortex_search` is always registered (a no-op on baseline
   since the store is empty).
4. **Scoring**: when `--judge` is set, the model's final assistant
   message is graded by the judge prompt in `judge.go` (a stricter
   port of LongMemEval's `evaluate_qa.py` rules, wrapped in a JSON
   verdict envelope).
5. **Cell emit**: one `CellResult` per (question × strategy), routed
   through the standard persister fan-out
   (journal → SQLite → `cell_results.jsonl`). `Notes` records
   `question_type=... axis=... judge_reason=...` so downstream
   rollups can group by ability axis.

## Judge model deviation

The upstream reference judge is **GPT-4o** via the OpenAI API. We
deviate to **`anthropic/claude-haiku-4.5`** by default, routed through
OpenRouter, for cost and consistency with the rest of Cortex's eval
infrastructure (Haiku 4.5 is already wired throughout the project's
budget-bounded paths).

This deviation is honest, not hidden:

- The default is documented and surfaced in the `Notes` field of
  every cell so analysis pipelines see which judge produced the
  verdict.
- The `--judge-model` flag overrides the default. For parity runs
  against the paper, pass `--judge-model openai/gpt-4o` (or the
  OpenRouter slug of your choice).
- Phase B will add a parity-judge mode (run both Haiku 4.5 and GPT-4o
  on the same instance, report agreement rate) so we can quantify
  the deviation rather than asserting it's small.

## Reproduction

The OpenRouter key must live in the macOS keychain under
`cortex-openrouter` (or in `OPEN_ROUTER_API_KEY`):

```sh
# Smoke (5 questions, baseline + cortex, judged with Haiku 4.5).
./cortex eval --benchmark longmemeval \
  --subset oracle \
  --limit 5 \
  --strategy baseline,cortex \
  --judge

# Single ability axis (e.g., abstention only).
./cortex eval --benchmark longmemeval \
  --subset oracle \
  --question-type abstention \
  --strategy baseline,cortex \
  --judge

# Parity judge run (use GPT-4o instead of Haiku 4.5).
./cortex eval --benchmark longmemeval \
  --subset oracle \
  --limit 5 \
  --strategy baseline,cortex \
  --judge-model openai/gpt-4o

# Different model under test.
./cortex eval --benchmark longmemeval \
  --subset oracle \
  --model qwen/qwen3-coder \
  --limit 10 \
  --strategy baseline,cortex \
  --judge
```

## Interpreting per-axis pass rates

Each emitted cell carries `Notes=question_type=... axis=...` so
rollups can group by axis. To produce the per-axis report from the
JSONL log:

```sh
jq -r 'select(.benchmark=="longmemeval")
       | [.context_strategy,
          (.notes | capture("axis=(?<a>[^ ]+)").a),
          .task_success]
       | @tsv' .cortex/db/cell_results.jsonl \
  | awk -F'\t' '{ tot[$1"|"$2]++; if($3=="true") pass[$1"|"$2]++ }
                END { for (k in tot) printf "%s\t%d/%d\n", k, pass[k], tot[k] }'
```

Headline metric: **lift per axis** = `pass_rate(cortex) − pass_rate(baseline)`.

- **Positive lift on `single-hop`** → cortex_search is finding the
  evidence turn cleanly.
- **Positive lift on `multi-hop`** → search composes across sessions.
- **Positive lift on `temporal`** → the timestamp metadata captured
  during hydration is being used.
- **Flat `abstention`** → expected; both arms should refuse, neither
  more nor less than the other. A regression on this axis (cortex
  lower than baseline) means the surfaced context is encouraging
  fabrication — a tuning-needed signal.

## What this does NOT measure

- The Oracle split filters down to evidence sessions only. It does
  not stress the retriever at scale — that's S/M (Phase B).
- One attempt per question; no self-correction loop. The skeleton's
  multi-attempt pattern is overkill for a QA benchmark and would
  double-count when the model retries a flatly wrong answer.
- Judge cost — a Haiku 4.5 judge on Oracle is cheap (~$0.01 for 50
  questions) but a GPT-4o parity run is meaningfully more expensive;
  the spend-ceiling check (`internal/eval/v2/spend.go`) gates it.
