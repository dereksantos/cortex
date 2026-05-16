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

<!-- Newest at the top. Below is a placeholder showing the format — replace with real entries. -->

### 2026-MM-DD — example / placeholder

**Cortex**: `<sha>`
**Command**:
```
./cortex eval --benchmark <name> --strategy <variant>
```
**Versions**: embedder=`<embedder>`, llm=`<llm>`, judge=`<judge>`, rerank=`<bool>`
**Result**: `<score>`

**Why this run**: <one sentence>.

**Observations**:
- <what stood out>

**Follow-ups**:
- <issue / next run / principle flagged>
