# docs/archive

Historical documents kept for institutional memory but no longer current.
Items here are *not* authoritative — when an archived doc and an active
doc conflict, the active doc wins.

Active strategy lives in [`../eval-strategy.md`](../eval-strategy.md);
phase sequencing in [`../../ROADMAP.md`](../../ROADMAP.md).

## What's in here

| File / dir | Why archived |
|---|---|
| `architecture-review-2025-01.md` | Pre-DAG architecture review; superseded by `dag-protocol.md` + `eval-strategy.md`. |
| `eval-findings-2026-05-10.md` | Findings snapshot from the May 10 Aider-signal pivot. |
| `eval-harness-loop.md` | Original eval-loop design; rolled into the v2 runner. |
| `eval-harness-phase7-prompt.md` | Phase-7 prompt scaffolding; complete. |
| `eval-prep-epic.md` | All 6 phases complete per its own closing note. Moved 2026-05-20. |
| `eval-resume-prompt.md` | Long resume-prompt artifact; preserved for reference. |
| `library-service/` | Earlier library-service eval iterations; current corpus lives at `test/evals/library-service/`. |
| `opencode-probe.json`, `opencode-tiers.md` | opencode harness probes from the cross-harness lift period. |
| `paper-references-todo.md` | Research-reference triage list; superseded by `MEMORY.md` references. |
| `phase7-*.md` | Phase-7 cross-harness lift work; complete. |
| `pidev-events.md`, `pidev-probe.json` | pi.dev integration probes. |
| `pre-launch-checklist.md` | Pre-launch checklist; complete. |

## When to add something here

Move a doc to this directory when:

- It explicitly declares itself complete (e.g., "All N phases done").
- Its framing is superseded by a current strategy doc and rewriting in
  place would be more confusing than archiving.
- It's a one-shot artifact (prompt, probe, snapshot) that doesn't need
  ongoing maintenance.

Don't move docs that still inform active decisions — those get a
"superseded by" header pointing at the authoritative doc instead.
