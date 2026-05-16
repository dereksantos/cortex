# NIAH — needle-in-a-haystack

NIAH is Cortex's permanent regression smoke for the **Reflex layer**
(capture → ingest → search). It hides a known fact at a configurable
depth inside a synthetic haystack of configurable length, drives that
text through the same `internal/capture` → `internal/processor` →
`internal/storage` path that production Cortex uses, and asks the
retriever to find the needle. A miss flags a problem in embedding,
chunking, or storage *before* it shows up as a multi-point LongMemEval
regression three days later.

The benchmark is intentionally cheap: no HuggingFace fetcher, no LLM
judge, no Docker. The generator is the source of truth — running the
same flags twice produces byte-identical haystacks.

## Two modes, two questions

NIAH ships two filler modes that ask different questions of the
retrieval substrate:

| Mode | Question | Behavior of the lorem corpus |
|---|---|---|
| `adversarial` (default) | "Can retrieval discriminate the needle from chunks that share words with the probe?" | Every filler phrase contains `secret`, `recipe`, or `code` — so the retriever sees a crowd of candidates and the scorer has to actually rank. |
| `lorem` | "Does the pipeline shape work end-to-end?" | Filler has zero overlap with probe terms — only the needle chunk matches, so a miss means the substrate (capture / ingest / chunking) is broken. |

Use **lorem first** when triaging an unfamiliar failure (does the
pipeline even work?), then **adversarial** to measure retrieval
quality. The default is adversarial because that's the regression
signal that compounds with shipping changes.

## What it measures

For each `(length × depth)` cell:

1. **Generate** a `Length * 4` char haystack from the chosen filler
   corpus, splice the needle in at `depth * fillerLen`.
2. **Capture** ~400-char overlapping chunks (overlap = needle-safe)
   into a fresh per-instance `<workdir>/.cortex/`.
3. **Ingest** the journal in-process (`processor.RunBatch`).
4. **Retrieve** via `cognition.Fast` (Reflex → Resolve) with a probe
   derived from the needle (needle minus its trailing secret token).
5. **Score**: pass iff the needle string appears as a substring in any
   of the top-K (default 10) results.

A `*evalv2.CellResult` is emitted per cell, with `Benchmark="niah"`,
`Harness=cortex`, `ContextStrategy=cortex`, and `Notes` carrying the
full diagnostic:

```
length=16k depth=0.50 filler=adversarial top_score=0.850 runner_up=0.850 gap=0.000 results=10 needle_position=missing
```

Each field is a leading indicator of a different regression class:

| Field | What it tells you |
|---|---|
| `top_score` | Best score returned. A drop in absolute value across runs = scorer regression. |
| `runner_up` | 2nd-best score. Reveals how close the contest was. |
| `gap` | `top_score − runner_up`. **The most actionable metric.** A shrinking gap means the needle is barely winning; a gap of 0.0 with many results means the scorer can't discriminate at all. |
| `results` | How many chunks Reflex actually returned. A drop from N to 1 = retrieval over-filters. |
| `needle_position` | 1-indexed rank, or `missing`. Pass requires `needle_position != missing`. |

## CLI

```bash
# Default: adversarial filler, 8K haystack, three depths (0.0, 0.5, 1.0), seed=1
./cortex eval --benchmark niah

# Cross-product sweep (4 × 3 = 12 cells)
./cortex eval --benchmark niah \
  --length 8k,16k,32k,64k --depth 0.0,0.5,1.0

# Pipeline-shape smoke (no scorer competition)
./cortex eval --benchmark niah --filler lorem \
  --length 8k,16k,32k --depth 0.0,0.5,1.0

# Single cell, custom needle and seed
./cortex eval --benchmark niah \
  --length 16k --depth 0.5 \
  --needle "the launch code is hunter2" \
  --seed 42

# --limit caps the cross-product
./cortex eval --benchmark niah --length 8k,16k --depth 0.0,0.5,1.0 --limit 4
```

Repeat-or-CSV: `--length 8k --length 16k` and `--length 8k,16k` are
equivalent. Same for `--depth`.

The `--model` flag is **rejected** with `--benchmark niah`: NIAH
measures the retrieval substrate, not LLM quality, so a model
selection here is almost always operator error.

## Current findings (text-search baseline, no embedder)

Baseline established in PR #32 against `cognition.Fast` with
`embedder=nil` (the default Reflex falls back to text-search). Two
runs over the canonical grid:

| Mode | Pass rate | Pattern |
|---|---|---|
| `lorem` | 12/12 | Pipeline shape is sound at every (length, depth). |
| `adversarial` | 4/12 | All depth=0.0 and 0.5 cells **miss**. All depth=1.0 cells **pass**. |

The adversarial finding is a real, reproducible signal about the
current Reflex scorer:

- The scorer ties every full-coverage chunk at exactly `top_score = 0.850`
  (text 1.0 × 0.40 + tag 0.5 × 0.20 + category 0.15 + recency ~1.0 ×
  0.15 + importance 0.5 × 0.10). With 0 gap, ordering falls back to
  insertion order.
- `SearchEventsMultiTerm` iterates `eventsByTime` (recency DESC) and
  caps at the query limit (10). For an N-chunk haystack with N > 10,
  the 10 most recently captured chunks come back first.
- The needle at depth 0.0 or 0.5 lands in a chunk captured early
  (chunk index ≪ N), so it is not in the recency-DESC top 10 → miss.
- At depth 1.0 the needle is in the LAST chunk captured → it tops
  the recency-DESC list → pass.

NIAH is doing its job: surfacing that the text-search path has a
strong recency bias that defeats depth-invariance. This is the kind
of finding that would otherwise hide for weeks until LongMemEval
shows an unexplained drop.

## How to interpret a miss

A miss at one cell with passes around it is the most actionable
signal NIAH produces. Triage by ruling out, in order:

1. **Did `--filler lorem` also miss?** If yes, the substrate is
   broken (capture / ingest / chunking). If no, the scorer or ranker
   is the suspect.

2. **Chunking** — did the needle get split across two chunks? Check
   `runner.go`'s `chunkStride` (320) and `chunkSize` (400) against
   the needle length. The default needle is 36 chars, well inside the
   overlap. A custom `--needle` longer than 320 chars will straddle
   (the `TestRunNeedleSplitAcrossChunks` test demonstrates this).

3. **Probe specificity** — Reflex falls back to text search with
   stopwords stripped. The probe is the needle minus its last token,
   so a needle like `"NEEDLE"` (one word) makes the probe equal to
   the needle itself. Pick a needle with at least 3 tokens that
   includes content words distinct from the filler corpus.

4. **Storage truncation** — Reflex's `eventToResult` caps content at
   500 chars. The chunk size (400) keeps the full chunk inside the
   truncation window, so the needle is preserved. If you bump
   `chunkSize` above 500, the needle can land in the dropped tail.

5. **Depth bias** — consistent failure at `depth=0.5` while
   `depth=0.0` and `1.0` pass is a real signal about the embedder
   (the classic NIAH/RULER finding: middle context decays faster).
   The text-search fallback path is *recency*-biased, not depth-biased,
   so this only cleanly surfaces once an embedder is wired into Reflex.

## Reproduction

Every run is byte-deterministic for fixed `(length, depth, needle,
seed, filler)`. To reproduce a miss reported by CI:

```bash
./cortex eval --benchmark niah \
  --length 16k --depth 0.5 \
  --needle "<exact needle from Notes>" \
  --seed <seed from Notes> \
  --filler <filler from Notes> \
  -v
```

The haystack will be identical bit-for-bit; the only variance
remaining is in the storage/retrieval path itself, which is what NIAH
is there to surface.

## Where results land

CellResults flow through the standard fan-out:

- **Journal**: `.cortex/journal/eval/*.jsonl` (canonical, replayable)
- **SQLite**: `.cortex/db/evals_v2.db` → `cell_results` table (queryable)
- **JSONL**: `.cortex/db/cell_results.jsonl` (append log for analysis)

Query the latest NIAH run from SQLite:

```sql
SELECT scenario_id, task_success, notes
FROM cell_results
WHERE benchmark = 'niah'
ORDER BY timestamp DESC
LIMIT 20;
```
