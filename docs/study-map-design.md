# Map & Study — Tool Redesign Working Document

> **Status.** Working document. Two tools with a clean separation:
> `map()` gives you **structure** (what's here, how it's organized),
> `study()` gives you **analysis** of the structure and some substance
> (what it means for your goal). Map is always cheap and deterministic;
> study is expensive, citation-grounded, and *informed by* the map.
>
> **Builds on.** [`study-file.md`](study-file.md) (the tool contract),
> [`working-memory.md`](working-memory.md) (session curation),
> [`working-memory-study.md`](working-memory-study.md) (cross-pass
> findings), [`cortex-study-plan.md`](cortex-study-plan.md) (the
> background accumulator).
>
> **Scope.** The *interactive* `study` tool (`study_file` + `StudyLoop`
> + `infer.go`), the existing `project_index` tool (which becomes
> `map`), and the compaction caller (`Compact`). The background
> dream-insight accumulator (`controller.go`) is out of scope — it's
> infrastructure, not the agent-facing tool.

---

## 1. Problem Statement

Study today produces a **thematic digest of sampled regions**, not a
**useful map**. Two failure modes observed in practice:

1. **Directory study lands in the wrong subtree.** Studying the repo
   root sampled into `test/evals/projects/library-service-seed/server`
   — an eval fixture containing a *different* Go project — and reported
   it as the real codebase. The sampler's weight formula
   (`pow(uncovered_eff_lines, 0.5) * (1 + 3.0 * uncovered_fraction)`)
   is purely structural: it has no goal signal and no notion of
   "fixtures are not real source."

2. **No terrain before flora.** Study skips straight to LLM inference
   over sampled byte regions. The caller never sees the file tree,
   module structure, or declaration skeleton *first* — the cheap,
   deterministic, always-useful orienting layer is absent. The
   `BoundaryOutput` already computes module structure, file hashes,
   and effective line counts, but it feeds the sampler, not the output.

The root cause is the same in both cases: **study has no layered
input**. It does the expensive, probabilistic thing (sample → infer)
without first establishing the cheap, deterministic territory (what's
here, what's relevant, what's a fixture).

### What already exists

The codebase already has most of `map()` — it's called `project_index`
(`internal/projectindex/`). It walks a directory tree (respecting
`projectscan` ignore rules), counts lines, and extracts Go declaration
symbols via `go/ast`. Two render modes:

- **Directory:** file tree grouped by directory, each Go file followed
  by its `func`/`type` symbols inline as `name:line · …`.
- **Single file:** full declaration skeleton — every top-level
  `func`/`type`/`const`/`var` in file order with line numbers.

It's already wired as a tool (`FunctionProjectIndex`) and already used
by `read_file` for the too-large-Go-file redirect (`goFileSkeleton`).
What it **doesn't** do:

- No fixture/vendor/test detection beyond `projectscan`'s ignore rules.
- No goal-aware filtering (it's a static map, same output regardless of
  why you're asking).
- No session transcript support (it's file-tree-only).
- Study doesn't consume it — `internal/study/` has no import of
  `projectindex`. The map and the analysis are disconnected.

So the redesign is: **rename `project_index` → `map`, extend it with
goal-filtering and session support, and make `study` consume `map`'s
output as its Layer 1.**

---

## 2. The MECE Cell — Two Tools, Not One

Two axes: **target size** (fits the window vs. doesn't) and **goal
specificity** (know exactly what/where vs. orienting).

|                  | Precise goal                          | Exploratory goal                |
|------------------|---------------------------------------|---------------------------------|
| **Small target** | `read_file` — exact, lossless         | `map` — cheap structure         |
| **Large target** | `bash grep` + `read_file` — precision | **`map` → `study`**             |

`map()` owns the **structure** column — it's useful for both small and
large targets when the goal is exploratory. `study()` owns the
**analysis** cell — large target + exploratory goal — and it consumes
`map`'s output to know *where* to sample.

The key shift: `map` and `study` are **separate tools with a producer/
consumer relationship**, not one tool with layered output. The agent
calls `map` first (cheap, always useful), reads the structure, then
calls `study` with a goal informed by what the map showed. Study
internally also calls map to get its Layer 1 — so the agent and the
tool share the same structural foundation.

---

## 3. Target Architecture — Two Tools

### `map(path, goal?)` — Structure

The rename and extension of `project_index`. Always cheap, always
deterministic, no LLM.

**Directory target:**
- File tree with sizes, languages, effective line counts (today).
- Module boundaries (today, via directory grouping).
- Go declaration symbols per file (today).
- **NEW:** Fixture/vendor/test detection — paths under `test/evals/`,
  `vendor/`, `testdata/` are labelled, not excluded.
- **NEW:** Goal-aware filtering — when a `goal` is provided, demote
  fixtures and promote goal-relevant subtrees. Without a goal, the map
  is neutral (today's behavior).

**File target:**
- Full declaration skeleton for Go files (today).
- Size, language, line count (today).
- **NEW:** Non-Go skeletons — a coarse structural outline for other
  languages (e.g., Markdown headers, YAML top-level keys). Defer until
  the Go path is proven.

**Session transcript target (NEW):**
- Turn-by-turn skeleton (see §5). This is the big new capability —
  `map` on a `.jsonl` transcript produces the session map, which
  `study` (via `Compact`) consumes for compaction.

**Cost.** Sub-50ms, no LLM. Pure mechanical extraction. This is what
`ls` + `wc -l` + `grep` gives me today, but richer, unified, and
available as a tool the agent (and other tools) can call.

### `study(path, goal, passes?)` — Analysis

The existing sample → infer → deepen pipeline, now **informed by
`map`**. Study calls `map` internally to get its Layer 1 terrain, uses
it to filter where it samples, and includes the map in its output so
the agent sees both structure and analysis.

- **Layer 1 (from map):** terrain map of the target, rendered first.
- **Layer 2 (goal filter):** study's sampler weights regions by goal
  relevance derived from the map (fixtures demoted, goal-keywords
  promoted). Today the sampler is purely structural.
- **Layer 3 (sampled digest):** the existing citation-grounded digest,
  unchanged in mechanism but guided by Layers 1–2.
- **Coverage map as first-class output:** sampled vs. unsampled regions,
  not just a coverage percentage. The blind spots are the most important
  thing to communicate when study stops at partial coverage.
- **Provenance contract preserved.** Every claim attributed to a
  sampled region's real line range; hallucinated lines stripped. This is
  the moat — don't change it.

---

## 4. What I'd Change — Concrete Edits

Tracked as buildable slices, cheapest-first. The big shift from the
prior version of this document: **`map` already exists as
`project_index`**, so the work is extension and wiring, not greenfield.

### 4.0 Rename `project_index` → `map` (mechanical)

**Files:** `cmd/loop/main.go` (tool registration + dispatch),
`internal/projectindex/` (package rename optional — keep the import
path, rename the tool function constant + description).

- `FunctionProjectIndex` → `FunctionMap`.
- Update the tool description to emphasize the producer role: "Map a
  project, file, or session transcript structurally. Returns the file
  tree with per-file symbols (Go), or a declaration skeleton for a
  single file. Cheap, deterministic — call it first to orient, then
  study for analysis."
- No behavior change. Pure rename.

**Risk.** None. Mechanical. Unblocks the naming throughout the rest of
the slices.

### 4.1 Fixture/vendor detection in `map`

**File:** `internal/projectindex/index.go`

`projectscan` handles `.gitignore` but doesn't label fixtures. Add a
`Role` field to `File` (`source` | `test` | `fixture` | `vendor` |
`doc` | `config`), derived from path heuristics:

- `test/evals/` → `fixture`
- `vendor/`, `third_party/` → `vendor`
- `*_test.go`, `test/` → `test`
- `*.md`, `docs/` → `doc`
- everything else → `source`

Render the role as a tag in the directory view so the agent (and study's
sampler) can see "this subtree is fixtures" without reading content.

**Risk.** Low. Additive metadata; no change to what's included, just
how it's labelled.

### 4.2 Goal-aware filtering in `map`

**File:** `internal/projectindex/index.go` (new `relevance.go`)

When `map` is called with a `goal` parameter, rank files by goal
relevance:

- Goal-keyword match against file paths and symbol names (promotion).
- Fixture/vendor demotion (from 4.1's roles).
- Render a "relevance" column or order files by relevance within each
  directory.

Without a goal, the map is neutral (today's behavior). With a goal, the
map becomes a *filtered view* — still the full tree, but annotated so
the agent knows where to focus.

**Risk.** Low. Additive output; the goal parameter is optional.

### 4.3 Wire `study` to consume `map`

**File:** `internal/study/study_file.go`, `study_dir.go`

Today `internal/study/` has no import of `projectindex`. Study computes
its own `BoundaryOutput` (module tree, file hashes, chunks) and discards
it from the output. Wire study to:

1. Call `projectindex.Build` (or the renamed `map.Build`) to get the
   terrain map.
2. Use the map's file roles (from 4.1) to bias the sampler — fixtures
   get lower weight. This is the fix for the eval-fixture misfire.
3. Include the terrain map in the study response, rendered before the
   digest, so the agent sees structure + analysis together.

**Risk.** Medium. Changes study's output and sampling behavior. Guard
with a flag until eval'd against `cmd/loop/study_eval.go`.

### 4.4 Coverage map as structural output

**File:** `internal/study/study_file.go`, `study_loop.go`

Today `StudyLoopResult.CoveragePct` is a single float. Extend to a
per-file structural map: sampled line ranges vs. total (e.g.,
`cmd/loop/main.go: 1-1800, 2400-2700 sampled of 4221`). The `seen` map
in `StudyLoop` already tracks sampled regions by `relpath:byteoffset` —
extend it to track line ranges and render them.

**Risk.** Low. Additive output; the data is already tracked.

### 4.5 Session map in `map`

**File:** `internal/projectindex/index.go` (new `session.go`), or
`cmd/loop/main.go` (if it stays session-specific)

When `map` is called on a `.jsonl` transcript, produce the turn
skeleton (see §5). This extends `map` from file-tree-only to also
handle session transcripts — the structural primitive for compaction.

**Risk.** Low for the pure function (additive). Medium for wiring into
`Compact` (changes compaction input).

---

## 5. The Session Map — Study for Compaction

Compaction is study pointed at the conversation transcript. Today
(`cmd/loop/main.go:Compact`):

```
Compact → compactStudy → runStudy → StudyLoop(transcript.jsonl)
```

The transcript is a JSONL file of `sessionEntry` records (kinds:
`message`, `retrieval`, `compaction`). `StudyLoop` treats it as a flat
text file and samples byte regions — which is exactly the "random
sampling" problem. A session is **not a file**; it's a **temporal
sequence of structured turns**, and the structure is already
mechanically extractable.

### 5.1 What a session map looks like

`turnArtifacts` (`cmd/loop/main.go:2811`) already extracts per-turn
structure: files edited, commands run, final answer. The session map
extends this to a full skeleton:

```
Session <id> — 42 turns, 18.4k tokens, 3 files touched, 12 commands run
  Turn  1  [user]      "refactor the loop main.go"
                      edited: cmd/loop/tools_parse.go
                      answer: "Extracted parseXMLToolCalls…"
  Turn  2  [user]      "now the config layer"
                      edited: cmd/loop/config.go
                      ran: go build ./...
  Turn  3  [user]      "run the tests"
                      ran: go test ./cmd/loop/...
                      answer: "all passing"
  ...
  Turn 38  [user]      "what's left?"
                      answer: "session.go is the last slice…"
  [retrieval entries: 4 — not conversational]
  [compaction entries: 1 — prior compact at turn 22]
```

Each turn is one line: index, role, intent (first line of user
message), mechanical outcome (files/commands from tool calls), answer
gist (first line of final assistant message). Retrieval and
compaction entries are marked as non-conversational.

### 5.2 Why this is better than byte-sampling the transcript

Today's compaction samples byte regions of the JSONL — it might grab
the middle of a tool result and miss the user's intent, or sample two
adjacent turns and miss the turn between them. The session map makes
**turns the unit of coverage**, not bytes:

- **Keep verbatim** — recent turns, turns with unresolved decisions,
  turns that established key constraints. The working-memory doc
  (`working-memory.md`) already proposes this triage
  (keep/compress/evict); the session map is the input it needs.
- **Compress** — older turns whose outcome is captured by later turns
  ("ran tests" → "all passing" can collapse to "tests passed").
- **Evict** — turns that were pure exploration (read-only study, grep)
  with no durable outcome. Their substance lives in the transcript on
  disk and in the capture store; evicting them from the compacted
  window loses nothing retrievable.

This is the bridge between study (the file tool) and working memory
(the session curation design). The session map is Layer 1 for the
transcript; the keep/compress/evict triage is Layer 2; the compacted
digest is Layer 3.

### 5.3 Concrete build slice

**File:** `cmd/loop/main.go` (session map), `internal/study/` (plumb
through)

1. **`sessionMap(transcript []sessionEntry) string`** — a pure
   function that renders the turn skeleton. Reuses `turnArtifacts`
   logic, extended to all turns (not just the current turn). No LLM.
2. **Feed it to `Compact`** — before calling `compactStudy`, emit the
   session map as the first part of the compaction prompt. The study
   model sees the skeleton *and* the sampled regions, so its digest
   is grounded in turn structure, not just byte content.
3. **Turn-aware sampling** — longer path: teach `StudyLoop` that a
   transcript has turn boundaries (parse the JSONL kinds) and sample
   *by turn*, not by byte region. A turn is the atomic unit; either
   it's in the sample or it isn't. This replaces byte-grid chunking
   for transcript targets with turn-aligned chunking.

**Risk.** Medium for step 3 (changes sampling for transcripts). Low
for steps 1–2 (additive context to the existing compaction call).

---

## 6. Build Order

| Slice | What | Risk | Unblocks |
|-------|------|------|----------|
| 4.0 | Rename `project_index` → `map` | None | Naming throughout |
| 4.1 | Fixture/vendor detection in `map` | Low | Roles for filtering |
| 4.2 | Goal-aware filtering in `map` | Low | Filtered view |
| 4.5 | Session map in `map` | Low | Layer 1 for transcripts |
| 4.4 | Coverage map as structural output | Low | Trust signal for partial studies |
| 4.3 | Wire `study` to consume `map` | Medium | Fixes the fixture problem in study |
| 5.3.2 | Feed session map to `Compact` | Low | Better compaction |
| 5.3.3 | Turn-aware transcript sampling | Medium | Turns as the compaction unit |

Slices 4.0–4.2, 4.4–4.5, 5.3.1–5.3.2 are all **additive output or
mechanical rename** — they surface data that's already computed or
cheaply computable, without changing sampling or inference. They can
ship independently and each makes the tool surface immediately more
useful. Slices 4.3 and 5.3.3 change sampling/compaction behavior and
need eval backing.

---

## 7. Open Questions

1. **RESOLVED: `map` is a separate tool.** The agent calls `map`
   first (cheap structure), then `study` (analysis informed by the
   map). Study also calls `map` internally for its Layer 1. The
   existing `project_index` tool is the foundation — rename to `map`,
   extend with goal-filtering and session support.

2. **Fixture detection heuristics.** Path-based (`test/evals/`,
   `vendor/`, `testdata/`) is cheap but coarse. A `.cortex/relevance`
   config could let projects declare their own fixture roots. Defer
   until the path heuristic proves insufficient.

3. **Session map token budget.** A 200-turn session's skeleton at one
   line per turn is ~2–4k tokens — non-trivial if the compaction
   budget is a quarter of the code window. May need compression
   (collapse consecutive read-only turns, truncate answer gists)
   before feeding to the study model.

4. **Does the background accumulator (`controller.go`) want the same
   layered treatment?** Out of scope here, but the same principle
   applies: it currently samples chunks and extracts insights without
   a terrain layer. The dream journal would benefit from a structural
   overview entry per run. Defer.

5. **Should `map` on a session transcript be a `map` tool call, or
   only internal to `Compact`?** The agent doesn't compact sessions
   directly — `Compact` is called by the loop. But exposing
   `map(session.jsonl)` would let the agent inspect session history
   structurally, which could be useful for `/sessions` or debugging.
   Lean: expose it — same tool, different target type.
