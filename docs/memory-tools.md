# Memory as tools, not a pipeline

## The pivot

Memory should be **tools the model drives**, not a mechanical retrieval/distill
pipeline that reasons on the model's behalf. The model is better at deciding
what's relevant, what's worth saving, and whether a note is stale than any
recency/contradiction heuristic we can hand-code. This supersedes the mechanical
memory line (Reflex/Reflect/Resolve, recency weighting, contradiction→retraction,
freshness injection, auto-distill) — see [`memory-distillation.md`](memory-distillation.md)
for the design that mechanical approach was heading toward.

It's the agentic-memory pattern, and it's how Claude Code's own memory works: a
few tools + free-form named files + a tiny index, the model curates it.

## The tool surface (4 + 1)

| Tool | Does |
|---|---|
| `memory_write(name, content)` | Create or update a named note. Model picks the name (kebab-case); update if it exists, else create. |
| `memory_read(name)` | Read one note in full. |
| `memory_search(query)` | Find relevant notes — returns names + snippets (keyword over the small note corpus; embeddings optional later). |
| `memory_forget(name)` | Remove a note (append-only retraction marker; reversible). |
| `study(.cortex/journal …)` | Reuse the navigator on the raw journal/transcript for detail no note captured. |

That's it. No pre-turn retrieval, no rerank, no auto-distill.

## Storage

- `.cortex/memory/<name>.md` — one durable note per file. Light frontmatter:
  `created` / `updated` timestamps so the model can reason about staleness
  ("saved 3 weeks ago → maybe verify"). Body is free-form prose with a pointer
  to source (a file, a journal turn).
- `.cortex/memory/INDEX.md` — one line per note (`- name — hook (updated <date>)`),
  regenerated on every write. This is the recall surface.

## The one mechanical seam: index injection

The only mechanical thing left: **inject `INDEX.md` at turn start** (size-capped).
That's what reminds the model *what it can recall* so it chooses to read the
relevant notes — without it, a tool-only model is blind to its own memory. Cheap,
always-on, no model call.

## System-prompt guidance (principles, not recipes)

```
You have a memory: named notes you've written, listed in the index below.
- Read the notes relevant to the task before answering.
- When you learn something worth having next session — a decision and its why,
  a constraint, a user preference, a non-obvious fact — write a note
  (memory_write). Update the existing note if one fits; don't duplicate.
- Notes are timestamped. If one looks stale for what you're doing, verify it
  rather than trusting it.
- For detail a note only points at, study the journal.
```

## What's removed vs kept

**Removed (the mechanical mess):** `retrieve()` pre-turn injection,
`formatRetrieved` + `relAge` freshness injection, `weightByProvenance` recency
ranking, `applyRetractions` contradiction→retraction, auto-distill
(`distillPending`/`noteTurn`), and the Reflex/Reflect/Resolve wiring in
`EnableRetrieval`. `internal/cognition` (and the `pkg/cognition` DAG it was built
on) was first dropped from the hot path here, then deleted outright on 2026-06-27
once nothing live consumed it — see [`archive.md`](archive.md).

**Kept as substrate:** the **journal** (the raw record `study(journal)` reads),
the **embedder** (can back `memory_search` later), the **navigator/study** (backs
`study(journal)`), and per-turn journal capture (the record — no longer embedded
or mechanically retrieved).

So the recent investments survive as substrate; the heuristic layer on top is
what goes.

## The bet (and the hedge)

"Just tools" bets the local model is agentic enough to *remember to save* and
*remember to search*. A capable model nails it; a small one may forget to look.
The hedge is the index injection — the model is always reminded what exists. If
that's not enough for the smallest models, a later nudge (a cheap "you have N
notes on this topic — read them?" prompt) can be added, but only if evals show
the need. Start without it.

## Phased plan — DONE

- **P1 — tools + storage + index. ✅** `memory_write/read/search/forget` in
  `cmd/cortex/tools/tools.go` (dispatch → `CortexSession` via `ToolDeps`), the
  `internal/memory` note store (`.cortex/memory/<name>.md` + `INDEX.md`), index
  injection at turn start (`memoryIndexNote`, folded onto the user turn via
  `EphemeralSystem`), and the memory-principles block in the seed system prompt.
- **P2 — `study(journal)`. ✅** The navigator reads `.cortex/journal` /
  `.cortex/sessions` on demand (the system prompt points at them). The ignore set
  keys off the scan root, so an explicit `.cortex/journal` root is not
  self-excluded — guarded by `TestStudyJournalPathNavigable`.
- **P3 — remove the pipeline. ✅** Ripped out pre-turn `retrieve()`, recency
  weighting, contradiction→retraction, freshness injection, and auto-distill from
  the hot path; `EnableRetrieval` → `EnableMemory` (note store + journal-only
  capturer). Kept the journal + navigator as substrate. (The `embed`/`fast`
  bindings and `resolveEmbedder` survive, reserved for a future semantic
  `memory_search` — see decision below.)
- **P4 — evals (tool-native, no mocks). ✅** A fast model-free end-to-end check
  (`memory_e2e_test.go`: write in session 1 → recall in a fresh session 2, stale
  update, forget) and a gated live behavioral eval against the fleet
  (`memory_e2e_live_test.go`, `CORTEX_LIVE_FLEET=1`): (a) cross-session recall,
  (b) stale-note update, (c) `study(journal)` for an unnoted detail. All three
  live scenarios pass on the local-gw fleet (glm-4.7-flash).

## Decisions (resolved)

- **`memory_search` is keyword-only.** The corpus is the model's own notes and
  the full index is injected anyway. Reversible: swap to embeddings later only if
  evals show recall misses. (The embedder resolution path is kept dormant.)
- **`/remember` / `/forget` folded into the model tools.** The slash commands
  were removed; the user asks in natural language and the agent calls
  `memory_write` / `memory_forget`.
- **Frontmatter = timestamps only** (`created` / `updated`). Tags/source can be
  added if search precision proves insufficient.
