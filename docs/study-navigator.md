# Study as navigator: map-first, tiny batches

> **Purpose.** Replace study's blind statistical *sampling* engine with a bounded
> *navigator*: lead with a cheap, no-LLM structural **map** of the target, then let
> a small model read only the few regions that matter, one tiny range at a time.
> The map turns "sample bytes blind and reconcile" into "see the seams and read
> what's relevant" — which is both cheaper and far higher-signal for the small and
> local models this harness targets.
>
> **Status.** Proposed. Supersedes the sampling-era machinery (incl. the P1–P4
> findings-prefix work in [`working-memory-study.md`](working-memory-study.md) for
> the *path-study* case — see "What this supersedes").
>
> **Owner.** `internal/study/`, `internal/projectindex/` (→ `map`), and the study
> tool wiring in `cmd/loop/tools/tools.go` + `cmd/loop/main.go`.
>
> **Builds on.** `internal/projectindex/` (already the right orientation layer),
> the per-language boundary regexes in `internal/study/boundary.go`, the bounded
> agentic loop in `cmd/loop/main.go` (`Resolve` + `runToolCalls` + no-progress
> guard), and citation validation in `internal/study/infer.go`.

---

## Diagnosis — why study is slow and resource-heavy

Today's `study` is a **statistical sampling engine**, not a navigator. The cost is
structural, not incidental:

- **The "half load" is literal.** `SampleTokenBudget = window·0.3 − overhead`
  (`study.go:59`). Every inference call deliberately fills ~30% of the window with
  a *blindly sampled* byte region, reserves a large output budget
  (`CompletionTokenBudget` halves the remainder), and on later passes prepends a
  growing findings prefix (up to 0.30·window). One study of one file pushes a
  large, mostly-blind payload through the model every pass.
- **It samples blind, then needs heavy machinery to stay coherent.** Because it
  reads byte-grid regions chosen by weighted RNG (`sampler_hierarchical.go`), it
  needs boundary-snapping, `RefineChunk`, density resolution, a focus sampler,
  directed sampling, a `Director` (pre-pass LLM call), a `Curator` (per-pass LLM
  call), and the whole P1–P4 findings-prefix/curation stack to stitch disjoint
  samples into something readable.
- **Call count balloons.** `StudyLoop` at defaults = 1 director + up to 4
  inference + 3 curator ≈ **8 LLM calls per invocation**, each carrying a 2–4k
  token prompt. On a small local model that is the latency you feel.

Root premise: *"I can't see the file, so I'll sample it statistically and
reconcile."* That premise forces both the big per-call payloads and the
reconciliation machinery.

## The reframe: sampling → navigation

Invert the premise. Get a **free structural map first**, then read only the
regions that matter, in tiny batches.

`project_index` is already the orientation layer — **free (no LLM)**, AST-based,
giving a dir tree + per-file symbols with line numbers, or a single-file
declaration skeleton. Fold it into a `map` tool and make study lead with it.

```
study(path, goal):
  1. m := map(path)                      # free, structural — orientation, no LLM
  2. run a bounded navigator sub-loop seeded with:
       - the navigator subsystem prompt
       - m  (the map)
       - the goal
       - a NARROW toolset: map (drill deeper), read_file (exact line range), study (recurse)
  3. the navigator works in tiny batches:
       pick a symbol/region from the map → read that exact range →
       record a finding with its file:line → pick the next → … until the goal is
       covered or the budget hits
  4. return the accumulated digest with cited file:line ranges
```

This is study-as-a-small-sub-agent: the *model* navigates using the map, instead
of an RNG sampler guessing. For small/local models this is far higher-signal —
they see the seams and read what's relevant, never a blind 30% slab. The seed
`[system][goal][map]` is a naturally cacheable prefix (the map is stable for the
whole sub-loop), recovering the prompt-cache win P4 chased — for free, because the
prefix is structural rather than a curated findings digest.

## `map` tool (fold in `project_index`)

Rename `project_index` → `map`; keep behavior, add one thing and generalize the
outline (next section):

- **Add symbol end-lines (spans).** Symbols today carry only a start line
  (`Symbol.Line`). Add `EndLine` (trivial from `go/ast`:
  `fset.Position(decl.End())`). The navigator then knows each declaration's *size*
  and can issue a precise `read_file(path, 40, 92)` instead of guessing. This is
  the single change that makes "tiny batches" land — without spans the navigator
  can't target ranges cleanly.
- Keep dir-view (tree + symbols) and single-file skeleton as-is.
- The agent keeps `map` as a top-level tool too — the existing "call it first"
  guidance becomes literally what study does internally.

## The outline tiers (and what "unstructured" means)

Map-first only works when there's a map to produce. "Unstructured" is a **spectrum,
not a wall**, and the floor is still an *outline*, not blind sampling — even
structureless text has *position*, and a positional index the model picks ranges
from beats today's RNG byte-grid on both cost and signal.

So `map` becomes a tiered **"cheapest available outline"** function —
`Outline(path) → []Section{label, startLine, endLine}`:

| Tier | Input | Cheap index (no LLM) | Source |
|---|---|---|---|
| 1 | Go | AST skeleton + spans | `go/ast` (have it) |
| 2 | Other code (py/js/ts/rust…) | regex declaration outline (`^(def\|class\|func\|export…)` → line nums) | **`boundary.go` already has `boundaryRe[lang]`** |
| 3 | Markdown / prose | heading outline (`^#{1,6}`), or blank-line section offsets when headingless | trivial scan |
| 4 | Data (JSON/CSV/log) | top-level keys + array lengths / header + row count / level-marker grep (`ERROR\|WARN\|FAIL`) | shallow parse |
| 5 | Genuine blob | positional outline: a line-range table of contents | last resort |

Tiers 1–4 are real structure for cheap; tier 2 is nearly free given `boundary.go`.
Tier 5 is the honest floor: hand the model `lines 1–4000 of N` with offsets and let
it `read_file(range)` — still navigation, still tiny batches, just coarser. Most of
"unstructured" lands in 2–4.

### Build the outline in, or let the model grep?

Two ways to handle tiers 2–5: **(a)** build outline-generators into `map`
(markdown headings, regex decls, JSON shape) — deterministic, no LLM; or **(b)**
give the navigator `bash grep -n`/`sed` and let it outline on the fly.

**Decision: (a) for tiers 2–4, (b) as the safety net.** A 4B model reliably reads
an outline you hand it; it is much less reliable at *inventing* the right `grep` to
build one. Doing the cheap structural work for the model is the whole
small-model-amplifier thesis — and tier 2 is mostly already written.

### The honest floor: keep one minimal fallback

Below tier 5 — a 50MB minified JSON, a structureless log with no markers —
navigation has nothing to grip, and we should not pretend otherwise. Keep **both**:

1. A **minimal chunk-and-fold summarizer** (sequential, *not* RNG-sampled) — the
   only thing worth salvaging from the current engine, scoped to this one case.
2. **Refuse/redirect** above some size: "this is 50MB of unstructured X — tell me
   what to grep for." For an interactive small-model harness, redirecting often
   beats burning 20 calls folding a blob.

The floor is therefore "navigator + a small explicit fallback, and we are honest
about which inputs hit it" — never silent blob-folding dressed up as coverage.

## Study splits into two: navigator + summarizer

Two consumers share the `study` name today but want different things:

- **Navigator** — path + map (the redesign above). For files and directories.
- **Summarizer** — free text with no path/map: **compaction** (working memory) and
  **bash-output study** (`studyShellOutput`, `tools.go`).

These must be split, but note one is *not* actually unstructured:

- **Conversation / compaction is structured** — it has turns, roles, tool calls,
  and `captureTurn()` already records files-edited / commands-run / final-answer
  per turn. Its map is the **turn/journal index**: the navigator reads the turn
  list and drills into salient turns. That is more principled than blob-summarizing
  the transcript, reuses machinery you already have, and is squarely on the
  forever-session direction in [`working-memory.md`](working-memory.md). Working
  memory becomes "navigate the journal."
- **Shell output is the genuinely-unstructured case** — but tier-4 marker-grep
  (surface `ERROR`/`FAIL`/`panic` lines + offsets) still yields an index. Only the
  entropic tail (an opaque dump) hits the floor fallback.

## New study loop spec

Replace the `StudyLoop`/`Controller` sampling pipeline with a bounded mini-`Resolve`.
The loop primitive already exists (`Resolve` + `runToolCalls` + no-progress guard,
`main.go:3086`); the navigator is the same pattern with a tighter budget and a
restricted tool list:

```
navigatorSystemPrompt =
  "You are a code navigator. You are given a structural MAP of a path and a GOAL.
   Read ONLY the regions relevant to the goal, one small range at a time, using
   read_file(path, start, end). Use map to drill into sub-paths. After each read,
   record a one-line finding with its file:line range. Stop when the goal is
   answered or you've read the few regions that matter. Cite every claim with a
   range. Do not read whole files; do not read regions the map shows are irrelevant."
```

- **Seed:** `[system][goal][map(path)]` — the map is the cacheable prefix.
- **Tiny batches:** one declaration/region per read, not a window-sized chunk.
- **Bound it:** a per-study iteration cap (≈6–10) and/or token budget, reusing the
  existing no-progress guard. Recursion (`study` → `study` on a subdir) gets a
  depth cap (1–2) so it can't fan out.
- **Model:** the study-role model (small/cheap), reusing the provider plumbing in
  `runStudy` (`main.go:848`).
- **Keep the fast path:** a small file/dir under budget skips the loop and is
  inlined whole (`study_file.go`'s existing whole-target behavior).

## What this supersedes / what dies

A large simplification, consistent with the recent "center on `cmd/loop`, cut
stale artifacts" arc:

- **Gone:** `analyzer_bytegrid.go`, `sampler_hierarchical.go`, `sampler_focus.go`,
  `director.go`, `curator.go`, `curate.go`, `directed.go`, `density.go`,
  `refine.go`, `boundary.go`'s snapping (keep its language regexes for tier 2),
  most of `study.go`'s budget math (`MakePlan`/`SampleTokenBudget`/coverage
  targets), the findings-prefix/working-memory P1–P4 stack, and `Controller`'s
  project-coverage loop.
- **Kept & repurposed:** `projectindex` (→ `map`), AST skeletoning, the provider
  wiring, citation validation (`infer.go`'s `ValidateCitations`), and the
  small-target whole-inline fast path.

This **supersedes the P1–P4 findings-prefix investment** documented in
[`working-memory-study.md`](working-memory-study.md) *for the path-study case*.
That work existed to make *blind sampling* coherent across passes; navigation
doesn't sample blind, so it doesn't need it. The findings/triage ideas still apply
to the *conversation* working-memory case (navigate the journal), so the direction
in [`working-memory.md`](working-memory.md) survives — it's the sampling
*implementation* that's retired.

## Risks / open decisions

- **Determinism.** Current study is seeded-RNG reproducible; an agentic navigator
  is not. For interactive use this is acceptable — recommend accepting it.
- **read_file recursion trap.** `read_file` currently refuses >16k-token files and
  *redirects to study* (`tools.go`). The navigator must read by **range**, so give
  it `read_file(path, start, end)` (or a dedicated `read_range`) that never
  bounces — otherwise study → read_file → "use study" → loop.
- **Evals.** `study-eval`/wm metrics (coverage/grounding/synthesis) were built for
  the sampling engine. The new design needs a new eval: *did the navigator surface
  the goal-relevant code, with grounded citations, in few calls?* Build it before
  deleting the old metrics.
- **Budget bound shape.** Iteration cap vs token budget vs both — recommend a small
  iteration cap + the existing no-progress guard. Keep it dead simple.

## Suggested phasing

1. **`map` = `project_index` + symbol spans (`EndLine`)** + ranged
   `read_file(path, start, end)`. Cheap, independently useful, no behavior change
   to study yet.
2. **Outline tiers 2–4** in `map` (regex decls from `boundary.go`, markdown
   headings, JSON/CSV shape) + the tier-5 positional fallback.
3. **Navigator sub-loop behind a flag** (`CORTEX_STUDY_NAV` or config), alongside
   the old engine. Seed `[system][goal][map]`, narrow toolset, bounded.
4. **New eval** comparing navigator vs old engine on the existing study corpora
   (latency + did-it-find-the-goal + citation groundedness).
5. **Split the summarizer path** — compaction (→ journal/turn index) and shell
   spill (→ marker-grep + floor fallback) stop depending on the sampling engine.
6. **Delete the sampling stack** once the eval clears.
