# File-tool surface — scaling roadmap

> **Status:** roadmap, written 2026-05-25 while Piece 1 + 2 + the
> Piece 3 spec ship in `derek.s/chunker-and-handoff`. Captures what's
> still missing once the immediate re-read loop is closed, in priority
> order.

## Where we are

| Piece | Status | What it gave |
|---|---|---|
| 1 — chunker token budget | shipped | Read emits up to N tokens instead of 8 chunks |
| 2 — `read_file` line range | shipped | Truncation marker's "re-fetch" advice is reachable |
| 3 — `patch_file` line range | spec'd (`docs/patch-file-tool.md`) | Symmetric write surface; ends the full-file-replace failure |

Pieces 1-3 are necessary but not sufficient. Below are the scaling
gaps that will surface as Cortex moves beyond Go monorepos with
hand-written source.

## Scaling gaps

### A. Line numbers drift under edits

Turn 1: `read_file(repl.go, start_line=920, end_line=970)`. Turn 2:
`patch_file(repl.go, ..., content=…)` inserts 5 lines. Turn 3: the
model wants to "re-read the function I just edited" — its memory of
the line number is wrong by 5, and Cortex has no rebase. The patch
appears to succeed at the wrong location.

The Piece 3 spec already proposes `verify_match: string` (the model
echoes the content it expects to be replacing) — that's the minimum
viable guard. The fuller version tracks per-session edit history and
auto-rebases line numbers in the accumulator snapshot.

### B. Lines aren't the semantic unit edits care about

"Rename Foo to Bar" / "extract this function" / "delete the dead
import" — none of these are line-range edits in the user's head; they
are AST edits. Coder models translate them in their heads from
chunked reads, and small coders are bad at it.

`project_direction_small_model_amplifier.md` already names AST
detection as the Tier-3 leverage point. The line-range surface is a
stopgap. The endgame surface is:

- `find_function(name)` → returns `path, start_line, end_line`
- `find_references(symbol)` → list of `(path, line)`
- `rename_symbol(symbol, new_name)` → atomic across all references

Line ranges sit underneath these but stop being what the model
directly addresses.

### C. The accumulator doesn't track *coverage*

Today's snapshot compresses content. A model that paginates `repl.go`
across four turns has the *content* in the snapshot but no "I have
read lines 1-4986 of repl.go" coverage header. On turn 10 it can
re-issue `read_file(repl.go)` and the trailing-K detector can't
distinguish "re-fetching for valid reason" from "lost track of what I
already saw."

Handoff doc flags this as Piece 3 (their numbering, not this doc's)
— "background, lower priority." I'd raise it. The accumulator already
sees every `act.read_file` and every `attend.chunk` row in turn
state; adding `files_seen: {path: [(start,end), ...]}` to the
snapshot is a small, mechanical change that pays back compounding
returns.

Candidate site: `pkg/cognition/dag/ops/attend_accumulate.go`.

### D. Multi-file edits fall off the cliff

The current surface is single-file. "Rename `FooClient` to `BarClient`
across the repo" via `read_file + patch_file` requires the model to
manage 30+ paths in its working memory. That's exactly the working-
memory failure Cortex was supposed to solve.

This is the natural home for the AST tools in (B). Defer until then;
don't bolt multi-file extensions onto `patch_file`.

### E. Format-specific files break line semantics

- `.ipynb` (Jupyter notebooks): JSON with embedded cell content; "line
  N" addresses JSON structure, not user-visible source.
- Machine-generated code (large TS schema files, protobuf-generated
  Go): can be 50k lines of one-rule-per-line; per-chunk caps tuned
  for hand-written source don't fit.
- SQL migrations / config files: natural granularity is per-statement
  or per-section, not per-line.

The current 500-token per-chunk cap was tuned against Go source on
one fleet. A per-format chunking adaptor — picked from the path
extension — keeps the surface narrow while letting each format use
its own boundary detector. Out of scope until the AST work
materializes; capture as a known scaling cliff.

### F. Byte caps are arbitrary anchors

`maxReadFileBytes = 64 KiB`, `maxWriteFileBytes = 128 KiB`, chunker
emission = `30% of n_ctx`. These were tuned for chatterbox/coder
(65K n_ctx). On a 4K-ctx local model, 64 KiB reads are absurd; on
Haiku 4.5 with 200K n_ctx, they're stingy.

The fix is to derive all three from `Budget.MaxContextTokens` (which
already flows everywhere). Mechanical change; one PR. Worth doing
before the AST work because the AST tools will inherit the same
hardcoded constants if we don't fix them first.

## Sequenced plan

```
Now (open PR)         Piece 1 + Piece 2 + Piece 3 spec
                      └─ closes the re-read loop on chatterbox

Next (small, fast)    Piece 3 impl   — patch_file w/ verify_match
                      Gap (C) impl   — accumulator coverage header
                      Gap (F) impl   — byte caps from Budget.MaxContextTokens

Later (structural)    Gap (B) impl   — find_function / find_references /
                                       rename_symbol (Tier 3 AST work)
                      Gap (A) impl   — auto-rebase line numbers via
                                       per-session edit log
                      Gap (E) impl   — per-format chunking adaptors

Out of scope          Gap (D) — multi-file edits live downstream of (B)
```

## Decisions held open

1. **Diff vs line-range patches.** `patch_file` ships line-range
   (Piece 3 spec). If empirical use shows the LLM mis-emits more often
   than helpful, `apply_patch(unified_diff)` joins the surface — they
   are not mutually exclusive.
2. **AST tool scope per language.** Tier-3 starts with Go (the project's
   own language; trivial to dogfood). Tree-sitter for everything else
   when the Go path is proven.
3. **Whether to expose `lines_read_so_far` to the model.** Coverage
   tracking (gap C) might surface inside the accumulator snapshot
   (model sees a "files seen" header) or stay purely on the
   `no_progress` side (the harness uses it but the model doesn't).
   Default: surface it; the model can use it to decide whether to
   re-read.

## Anti-goals

- **Don't reinvent LSP.** AST tools should be narrowly-scoped helpers
  for *editing*, not a full language server. "Go to definition" inside
  a single repository is enough; "find usages in all packages on
  GOPATH" is not.
- **Don't add format-specific surfaces to `read_file` / `patch_file`.**
  Notebook editing belongs in a separate tool (`read_notebook`,
  `patch_notebook_cell`) so the line-range surface stays mechanical.
- **Don't tier the tool catalog by model size.** The handoff's
  routing layer already picks the model; the tool surface stays
  uniform. A weak model that can't drive `rename_symbol` falls back
  to `read_file + patch_file` on its own; the surface doesn't hide
  capabilities from it.

## What this doc isn't

A design for any of the gaps above — only the order they should land
in, with the rationale for why. Each one earns its own design doc
when it's the next thing in flight.
