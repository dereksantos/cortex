# `study_file` — size-adaptive, deepening-on-relevance reading

> Status: **v1 SHIPPED for files and directories** (2026-06): the
> `study_file` tool takes either, with the same threshold/sample/infer
> identity (see Boundary producers). Decided: `attend.accumulate`
> is subsumed by study coverage (removed); leads/targeting are layered
> (grep · study · AST). Supersedes the "read whole file → compress"
> path that produced the `attend.accumulate` death spiral (see Motivation).
> Aligns with [`cortex-study-plan.md`](cortex-study-plan.md) (the study
> session + coverage) and [`file-tool-roadmap.md`](file-tool-roadmap.md)
> (the file-tool surface). Reuses the existing fractal machinery — this is
> a reuse-heavy build, not a rewrite.

## TL;DR

Replace the agent's `read_file` tool with one size-adaptive tool,
`study_file`, whose identity flips on a single threshold:

```
study_file(path, density) =
    read_file(path)            if size(path) < window/2     # it fits; just read it
    study(path, density)       otherwise                    # sample → infer → (deepen)
```

`study(...)` is **two phases**: a *mechanical* fractal sample of byte/line
regions (no LLM, ~50 ms, deterministic) followed by *agentic inference*
over only the sampled regions. Its cost is bound by **density**, not file
size — a 1 GB file and a 200 KB file at the same density cost the same
inference. The hero feature is **relevance-driven deepening**: a sparse
first pass can say *"`PgStorage` looks relevant — tell me more,"* and the
next pass does a **targeted, denser study of that region** instead of
re-reading everything.

The same machinery works over **one file's byte space** or a **corpus's
file tree** — it's the same `Sampler` over a different boundary space.

## Motivation

The agent loop's `read_file` ingested whole files, then `attend.accumulate`
LLM-compressed every tool output into a tiny snapshot (`salienceCap*2` =
400–3000 tokens) using the *session* model. Two failures:

- **Cost tracked file size.** A 69 KB read on the large self-repo →
  a 257 s compaction. With ~10 tool calls per cortex cell, turns blew past
  the 15-min cap → killed → INVALID. (Small fixtures never hit it.)
- **Wrong-granularity loss.** Crushing a file to 400 tokens destroyed the
  detail the task needed, producing wrong answers even when the cell
  finished.

The fix is to make reading **density-bound and seekable**: never ingest
what doesn't fit, sample it instead, and only ever infer over the sample.

## Core model

`study_file` is the agent's sole reading primitive. The agent never
chooses read-vs-study; it always studies, and study degenerates to a plain
read when the file fits.

```
size := stat(path).Size
if est_tokens(size) < window/2:        # window = CONSUMING model's context window
    return read_file(path)             # whole content, no sampling, no compaction
else:
    return study(path, density, focus) # phase 1 sample → phase 2 infer
```

- **`window`** is the *consuming* model's context window (the coding_turn
  that will hold the result), probed via `internal/study/probe.go`. `/2`
  leaves room for the prompt + prior context + the result.
- **Threshold behavior is the whole rollout story:** every small file
  (all 33 non-cortex eval fixtures) is byte-identical to today
  (`study == read_file`). Only files over the threshold (the cortex
  self-repo cells that currently time out) take the sample path.

## The hero: relevance-driven deepening

A single study pass returns a **bounded digest of sampled regions** plus a
**coverage map** and **deepening affordances**. The agent reads the digest
and decides:

```
                 ┌──────────────────────────────────────────────┐
   study(sparse) │  digest of k regions + coverage + "leads"     │
                 └───────────────┬──────────────────────────────┘
                                 │  agent reads it
            ┌────────────────────┼─────────────────────┐
            ▼                    ▼                      ▼
        DONE              DENSIFY(region)          TARGET(symbol|lines|query)
   answer from        re-sample that area        seek to a specific lead
   what I've seen     at higher density          ("tell me more about X")
```

- **Sparse first.** Density starts low (cheap, broad). The first pass is a
  reconnaissance: *what is this file/corpus, roughly, and where do the
  relevant parts seem to be?*
- **"Tell me more" = targeted densification.** When the digest surfaces a
  lead (`PgStorage is referenced near line 1400`), the agent issues a
  `DENSIFY` / `TARGET` request and the next pass samples **that region**
  densely — not the whole file again. Coverage accumulates; the
  `covered` set means a re-study refines rather than repeats.
- **This is seed + grow + decay applied to one file.** Sparse seed →
  grow density where relevance is found → decay when budget exhausts or
  the answer is in hand. The bounded-emergence thesis, made literal over
  a byte space.

The deepening request is the new surface; everything under it
(`Sampler.Next` with a focus weight, `ReadRegion` seek, coverage) exists.

## Tool contract (harness-integration surface)

`study_file` must be callable from a hand-built harness, not just the DAG.
The contract is a plain request/response — no DAG coupling.

**Request**
```jsonc
{
  "path": "internal/foo/bar.go",       // file, dir, or project root
  "density": "sparse" | "normal" | "dense" | <int k>,  // default: derived from budget
  "focus": {                            // optional — drives DENSIFY/TARGET
    "lines": [1380, 1460],              //   target a line range, OR
    "symbol": "PgStorage",              //   target a symbol/string, OR
    "query": "where is the 401 returned"//   semantic lead
  },
  "session": "study-<id>",              // resumable coverage key; optional
  "window": 65536                       // consuming model window; default: probe
}
```

**Response**
```jsonc
{
  "mode": "read" | "study",             // which path ran (size-decided)
  "digest": "…",                        // bounded inference over sampled regions
  "citations": [                        // PROVENANCE — every claim attributable
    {"relpath": "internal/foo/bar.go", "line_start": 1402, "line_end": 1417,
     "byte_offset": 48213, "claim": "PgStorage.evictOldest drops the oldest key"}
  ],
  "coverage": {"eff_lines_seen": 410, "eff_lines_total": 5120, "pct": 0.08},
  "leads": [                            // candidate regions worth deepening
    {"relpath": "…", "near_line": 1432, "why": "references PgStorage, not yet sampled"}
  ],
  "deepen": {                           // how to ask for more (mirrors the request)
    "densify": {"session": "study-x", "density": "dense"},
    "target":  {"session": "study-x", "focus": {"lines": [1380,1460]}}
  },
  "exhausted": false                    // true when coverage knee / budget reached
}
```

Notes for harness authors:
- The harness loop is: call `study_file` → read `digest`+`leads` → if the
  answer isn't grounded yet, re-call with `deepen.densify`/`deepen.target`.
- `citations` are the contract that lets the harness emit real `file:line`
  references downstream (see Provenance).
- `session` makes deepening **stateful and cheap** — the second call only
  samples *new* regions.

## Phase 1 — mechanical sampling (exists)

Deterministic, no LLM. Reuses:
- **`study.BoundaryOutput` / `Chunk{ByteOffset, ByteLength, LineStart,
  LineEnd, EffLines}`** — boundary units are already byte-addressable.
- **`HierarchicalSampler.Next(out, covered, k, rng)`** — per-module
  anti-coverage + size weighting; `k` is density; `covered` is resume
  state. A `focus` weight is the net-new knob (bias the draw toward a
  target region/symbol).
- **`fractal.ReadRegion(path, offset, length)`** — seeks a byte window
  (`ReadAt`) with rune-boundary clamping. Never ingests the whole file;
  works on GB/TB unchanged.

## Phase 2 — agentic inference + the provenance contract

Reuses the controller's `ExtractFunc` (`extract_insight` / `extract_overview`,
routed by language in `extract_router.go`) — but with a hardened prompt
contract, because **the eval suite grades on `file:line` citations** and
sample-then-infer is exactly where models invent line numbers.

**Provenance contract (hard constraints on phase 2):**
1. The model sees sampled regions **labelled with their real
   `relpath:line_start-line_end`** (from the `Chunk`).
2. Every claim in `digest` MUST attribute to one sampled chunk's range;
   claims that can't be attributed are dropped.
3. The model MUST NOT cite any line outside the sampled set. If it needs a
   line it didn't see, it emits a **lead** (→ deepening), not a citation.
4. `citations[]` is validated against the sampled chunk ranges before
   return; unvalidated citations are stripped and logged.

This makes "I didn't sample that" a first-class, *safe* outcome (a lead),
instead of a hallucinated `file:line`.

## Tooling layers: bash/grep, study, and system-specific tools (AST)

`study_file` is not the only tool — the agent also has **bash**, so
**grep stays bread-and-butter**. The three layers compose by cost and
richness:

| layer | cost | what it's for |
|---|---|---|
| **bash / grep** | cheap, mechanical | *location* — "where is `PgStorage`?", "which files mention X". Yields offsets/line numbers directly. Bread-and-butter; the agent reaches for it freely. |
| **`study_file`** | density-bound LLM | *richer inference* — "what does this region/file/corpus mean", bounded digests with provenance, deepening on relevance. |
| **system-specific tools (AST, LSP, …)** | tool-dependent | *precise structure* — `study` equipped with an AST gives exact symbol→byte-range targeting, structural chunk boundaries, and richer extraction for code. |

This resolves two things cleanly:

- **Where leads come from (was open #4): both, layered.** Cheap leads come
  from **grep via bash** (location is a `grep -n` away) and feed
  `TARGET{lines}` directly. Richer leads come from phase-2 **inference**
  ("this region references X, off-sample"). They're complementary, not a
  choice — grep for *where*, study for *what*.
- **Targeting precision (was open #5): AST for code, grep otherwise.** A
  `TARGET{symbol}` resolves to a byte region via **AST** when the language
  has one wired (precise: the symbol's exact span), falling back to a
  **streaming grep** that yields offsets to feed `ReadRegion`. Same for the
  boundary producer's lazy refinement — AST snaps chunk edges to real
  declarations for code; byte/line heuristics handle everything else.

So `study` is the *richer-inference* layer, made sharper as it's equipped
with more capable system-specific tools — while grep remains the fast path
the agent uses constantly for plain location.

## Boundary producers

| target | boundary space | producer | status |
|---|---|---|---|
| project | tree of files | `UniversalAnalyzer.Analyze(root)` | exists |
| dir | subtree | `studyDir` → scoped `UniversalAnalyzer` | ✅ shipped (2026-06) |
| single huge file | byte ranges within it | byte-space grid | ✅ shipped (2026-06) |

Directory studies (`internal/study/study_dir.go`) keep the same
size-adaptive identity: a dir whose total size fits window/2 is returned
WHOLE — every file inlined under a `----- relpath -----` header, the
"study this small package" comprehension win — and a larger dir routes
through the scoped universal analyzer into the same sampler + inference
+ citation pipeline. Chunk relpaths are prefixed with the dir's own
caller-relative path so citations stay real for the consuming agent.
`Focus.Path` (file or subtree) is the corpus targeting knob — line
numbers alone are ambiguous across files — and the curator emits it
from a lead's relpath (`focus_path` in the model-curator contract).
Known gap: files over the analyzer's 1 MiB cap are skipped by the walk,
so a huge file inside a studied dir is invisible; the fix is per-file
byte grids merged into the corpus boundary (TODO in `studyDir`).

The net-new producer must **not read the file to chunk it** (that defeats
the purpose):
- `stat` → size. Lay a **hierarchical byte grid from size alone** (level 0
  = whole file; recursively subdivide to a target chunk size, e.g.
  ~window/8).
- Emit `Chunk{ByteOffset, ByteLength}` with provisional line bounds.
- **Refine lazily on first visit**: once `ReadRegion` reads a window,
  snap its edges to the nearest line/section boundary and fill real
  `LineStart/LineEnd/EffLines`. Coverage and citations use the refined
  bounds.

## Density & the budget lever

`density` maps to `(k chunks, grid depth)`. Source of density:
- **Explicit** for `cortex study <target> --density …` and for harness
  callers.
- **Budget-derived** inside the agent loop: `sense.estimate_scope` already
  emits a per-turn budget — map it to density. Cheap intent → sparse;
  audit/refactor → dense. This closes the loop with the estimator we've
  been debugging.

## Coverage & resumability (the study session)

- Coverage is keyed **per source by content hash** (`BoundaryOutput.FileHashes`
  is the existing drift key). Re-studying an unchanged file resumes; a
  changed file invalidates its coverage.
- `cortex study [file|dir|project] [--density] [--session]` becomes the
  user-facing entry; the agent-loop `study_file` tool shares the same
  controller + coverage so foreground reads and background study compound.
- Target-coverage gating (`TargetCoverage`, default 0.80) already exists;
  deepening is "raise effective coverage of *this region* on demand."

## What it replaces

- **`read_file`** → the sub-threshold branch of `study_file` (unchanged
  behavior for files that fit).
- **`attend.accumulate` → SUBSUMED by study coverage.** Decided: not
  fold-only, gone. Each `study_file` result is bounded by construction, so
  there is nothing to recompress per tool call; the role accumulate played
  (bounded working memory) is now the **study session's coverage +
  digests**, which is bounded, resumable, per-source, and mechanical to
  maintain. One compaction path, not two. The node and its per-tool
  invocation in the agent loop are removed; any genuine "fold N digests"
  need is served by re-studying at the session level, not a per-call LLM
  compaction.

## Reuse map

| Piece | Status |
|---|---|
| Seek primitive (`fractal.ReadRegion`) | ✅ exists |
| Byte-addressable chunks (`Chunk.ByteOffset/Length`) | ✅ exists |
| Fractal sampler (`HierarchicalSampler`, pluggable Lévy/RWR) | ✅ exists |
| Coverage / resume (`covered`, `FileHashes`, `TargetCoverage`) | ✅ exists |
| Study session loop + extract ops (`Controller`, `ExtractFunc`) | ✅ exists |
| Project/dir boundary producer (`UniversalAnalyzer`) | ✅ exists |
| Byte-space boundary producer (single huge file) | ✅ shipped |
| Size threshold + `study_file` delegation seam (files + dirs) | ✅ shipped |
| `focus` weight in the sampler (TARGET/DENSIFY, lines + path) | ✅ shipped |
| Provenance-constrained inference prompt + citation validation | ✅ shipped |
| `study_file` request/response tool contract | ✅ shipped |

## Rollout & eval impact

- **Zero risk to the 33 sub-threshold cells** — they stay `read_file`.
- **Targets exactly the broken cells** — the 11 large-repo cortex cells
  that time out get the sample path; success = they become *scoreable*
  (not INVALID) **and** keep correct citations (provenance contract).
- Watch for the failure mode where sampling misses the needed region →
  silent wrong answer. The deepening loop + leads are the mitigation; the
  eval is the test.

## Decided

- **`attend.accumulate` → subsumed by study coverage** (see *What it
  replaces*). One compaction path.
- **Leads & targeting are layered, not either/or** — grep (via bash) for
  cheap *location*, study inference for richer leads, AST for precise
  code targeting (see *Tooling layers*).

## Open decisions

1. **Density taxonomy** — fixed names (`sparse/normal/dense`) vs raw `k`
   vs purely budget-derived. Harness wants explicit; loop wants derived.
   (Likely: support both — names map to `k`, budget overrides when unset.)
2. **Inference model** — session model vs a dedicated small + large-context
   "sole compressor." The window threshold makes the latter attractive
   (bigger window → fewer studies trigger).
3. **AST coverage** — which languages get an AST-equipped study first
   (`go/ast` is in-tree and cheapest), and the fallback ladder
   (AST → tree-sitter → byte/line grid + grep) for everything else.
