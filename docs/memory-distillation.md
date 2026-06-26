# Memory distillation — transcript → meaningful memory

## The problem

Retrieval currently serves **Tier‑1 raw captures**: one `events.jsonl` row per
turn, holding the user's prompt + a one‑line summary. That's effectively the
transcript. `/forget --all` made it obvious — the "memories" were 49 raw turn
prompts (`hi`, `Tell me about this project`, `Audit the entire docs/ …`). The
**Tier‑2 distillation** that was meant to produce *meaningful* memory exists
(`distillPending` → `insights.jsonl`) but is vestigial: it never supersedes the
raw layer, it's provider‑gated, and raw rows vastly outnumber insights, so
retrieval is swamped by transcript‑shaped noise.

## The redesign

Three moves:

1. **Retrieval pulls only distilled memory.** Raw turn captures stop being a
   retrieval surface. They remain in the journal as the durable *record*, not as
   working memory.
2. **`study(journal)` for depth on demand.** When a distilled note isn't enough,
   the model navigates the raw transcript/journal with the existing map‑first
   navigator — pull‑based, not preloaded.
3. **Free‑form, model‑named memory files.** Distillation writes durable knowledge
   into topic‑named files the model chooses (kebab‑case, conflict‑free,
   update‑or‑create), instead of a rigid `category|importance|tags` schema. A
   small index lists them. This is the agentic‑memory pattern (and exactly how
   Claude Code's own `MEMORY.md` + `memory/*.md` works).

## Flow

```
   your turn
      │
      ▼
 .cortex/sessions/<id>.jsonl        ← raw transcript (the record; reachable via study)
      │
      ├─ TIER 1  captureTurn()      [mechanical · every turn]
      │     └─►  journal (capture class)        ← record only, NOT retrieved
      │
      └─ TIER 2  distill (reasoner · async)
            │  "is there durable, transferable knowledge here?"
            ▼
      .cortex/memory/<model-named>.md           ← free-form, topic-named
      .cortex/memory/INDEX.md                   ← one line per memory (the recall index)
                 │
                 ▼
           ┌───────────────┐
           │   Retrieval   │   ← serves ONLY distilled memory files
           └───────────────┘
                 │ not enough?
                 ▼
           study(journal)        ← navigate the raw transcript on demand
```

The shift: **retrieval = distilled only; raw = pull‑based via study.** Tier‑1
becomes the audit record; Tier‑2 becomes the working memory.

## Storage model — free‑form named files

- `.cortex/memory/<name>.md` — one durable note per file, named by topic
  (`auth-decisions.md`, `eval-harness.md`). Light frontmatter optional; the body
  is durable prose with a pointer back to where it came from (a file, a journal
  turn) so `study(journal)` can expand it.
- `.cortex/memory/INDEX.md` — one line per file (`- name — hook`). Loaded as the
  recall surface; retrieval ranks against it, then reads the matched file(s).
- **Conflict rule:** the model is shown the existing file list and must either
  *update* the file the knowledge belongs in or *create* a new uniquely‑named
  one. Never duplicate; never overwrite an unrelated file.

## The distillation system prompt (the crux)

Small models default to *summarizing the turn* unless told not to. The prompt
must push for transferable, non‑obvious knowledge with the "why," forbid
transcript‑summary, and make "nothing" the common, acceptable answer.

```
You keep a durable memory for this project — notes that you, on a fresh session
next month, or a teammate, would actually need. A turn just finished. Decide what,
if anything, is worth remembering.

Save ONLY durable, transferable knowledge:
- Decisions and the reason behind them
- Constraints, gotchas, and "don't do X" corrections
- The user's stated preferences and how they want you to work
- Non-obvious architecture facts and where things live

Do NOT save: a summary of this turn, the restated question, transient status, or
anything already obvious from the code or git history. If you'd never need it
again, skip it. Most turns produce nothing — answer NOTHING and that is correct.

You will be shown the existing memory files (name — summary). If this knowledge
belongs in one, UPDATE it; otherwise create a new file with a short kebab-case
topic name. Never duplicate an existing file.

Write the note as durable prose with a brief pointer to where it came from (a
file path, a journal turn) so it can be expanded later.

Respond as:
  FILE: <kebab-name>.md        (existing → update | new)
  NOTE: <durable knowledge, 1–5 sentences, with the why and a pointer>
or:
  NOTHING
```

The key levers vs. today's `DreamAnalysisPrompt`: (1) "would I need this on a
fresh session?" as the test; (2) an explicit *don't summarize / don't restate*
ban; (3) "most turns produce nothing" so it stops manufacturing insights from
greetings; (4) free‑form naming with update‑or‑create instead of a fixed enum.

## Phased plan

- **P1 — retrieval distilled‑only:** stop embedding Tier‑1 captures; point Reflex
  at the memory files + INDEX. Raw events stay in the journal.
- **P2 — `study(journal)`:** route the navigator at `.cortex/sessions` / journal
  so the model can recover detail a note points to.
- **P3 — free‑form memory writer:** replace the `insights.jsonl` schema with the
  named‑file writer + INDEX, driven by the prompt above (update‑or‑create,
  conflict‑safe).
- **P4 — prune/merge:** Tier‑2 consolidates and supersedes (contradiction →
  retraction already exists for the event layer; extend to memory files).

## Open decisions

- Index format: flat `INDEX.md` vs. embedding each note for semantic recall (or
  both — index for cheap recall, embeddings for fuzzy match).
- Does `/remember` write a memory file directly (highest‑precision, user‑named)?
- Migration: existing `insights.jsonl` → memory files, or start fresh.
