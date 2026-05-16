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

## What it measures

For each `(length × depth)` cell:

1. **Generate** a `Length * 4` char haystack of deterministic lorem
   filler, splice the needle in at `depth * fillerLen`.
2. **Capture** ~400-char overlapping chunks (overlap = needle-safe)
   into a fresh per-instance `<workdir>/.cortex/`.
3. **Ingest** the journal in-process (`processor.RunBatch`).
4. **Retrieve** via `cognition.Fast` (Reflex → Resolve) with a probe
   derived from the needle (needle minus its trailing secret token).
5. **Score**: pass iff the needle string appears as a substring in any
   of the top-K results.

A `*evalv2.CellResult` is emitted per cell, with `Benchmark="niah"`,
`Harness=cortex`, `ContextStrategy=cortex`, and `Notes` containing the
length, depth, top score, and the 1-indexed position of the needle in
the result list (or `"missing"` on a miss).

## CLI

```bash
# Default: 8K haystack, three depths (0.0, 0.5, 1.0), seed=1
./cortex eval --benchmark niah

# Sweep lengths and depths (cross-product = 3 × 3 = 9 cells)
./cortex eval --benchmark niah \
  --length 8k --length 16k --length 32k \
  --depth 0.0 --depth 0.5 --depth 1.0

# Equivalent comma-form
./cortex eval --benchmark niah --length 8k,16k,32k --depth 0.0,0.5,1.0

# Single cell, custom needle and seed
./cortex eval --benchmark niah \
  --length 16k --depth 0.5 \
  --needle "the launch code is hunter2" \
  --seed 42

# --limit caps the cross-product
./cortex eval --benchmark niah --length 8k,16k --depth 0.0,0.5,1.0 --limit 4
```

The `--model` flag is **rejected** with `--benchmark niah`: NIAH
measures the retrieval substrate, not LLM quality, so a model
selection here is almost always operator error.

## How "passing" is defined

A cell passes when the needle string is a substring of **any** result
returned by `cognition.Fast`. Position 1 means it was the top result;
higher positions still count as a pass (the agent would still surface
it via top-K injection), but operators reading the rollup should treat
"pass at position 5" with more suspicion than "pass at position 1" —
it implies the retrieval substrate ranked irrelevant chunks higher.

`Notes` carries the diagnostic detail: `length=16k depth=0.50
top_score=0.732 needle_position=3`.

## How to interpret a miss

A miss at one cell with passes around it is the most actionable
signal NIAH produces. Triage by ruling out, in order:

1. **Chunking** — did the needle get split across two chunks? Check
   `generator.go`'s `chunkStride` (320) and `chunkSize` (400) against
   the needle length. The default needle is 36 chars, well inside the
   overlap. A custom `--needle` longer than ~320 chars will straddle.

2. **Probe specificity** — Reflex falls back to text search with
   stopwords stripped. The probe is the needle minus its last token,
   so a needle like `"NEEDLE"` (one word) makes the probe equal to
   the needle itself. Pick a needle with at least 3 tokens that
   includes content words not in `loremCorpus`.

3. **Storage truncation** — Reflex's `eventToResult` caps content at
   500 chars. The chunk size (400) keeps the full chunk inside the
   truncation window, so the needle is preserved. If you bump
   `chunkSize` above 500, the needle can land in the dropped tail.

4. **Depth bias** — consistent failure at `depth=0.5` while
   `depth=0.0` and `1.0` pass is a real signal about the embedder
   (the classic NIAH/RULER finding: middle context decays faster).
   The text-search fallback path is depth-invariant, so this only
   surfaces when an embedder is wired up.

5. **Embedder** — by default NIAH runs with `embedder=nil` and falls
   back to text search. Once an embedder lands in the Reflex hot
   path, NIAH becomes the canary for embedding-quality regressions.

## Reproduction

Every run is byte-deterministic for fixed `(length, depth, needle,
seed)`. To reproduce a miss reported by CI:

```bash
./cortex eval --benchmark niah \
  --length 16k --depth 0.5 \
  --needle "<exact needle from Notes>" \
  --seed <seed from Notes> \
  -v
```

The haystack will be identical bit-for-bit; the only variance
remaining is in the storage/retrieval path itself, which is what NIAH
is there to surface.

## Where results land

CellResults flow through the standard fan-out:

- **Journal**: `.cortex/journal/eval/*.jsonl` (canonical, replayable)
- **SQLite**: `.cortex/db/cortex.db` → `cell_results` table (queryable)
- **JSONL**: `.cortex/db/cell_results.jsonl` (append log for analysis)

Query the latest NIAH run from SQLite:

```sql
SELECT scenario_id, task_success, notes
FROM cell_results
WHERE benchmark = 'niah'
ORDER BY timestamp DESC
LIMIT 20;
```
