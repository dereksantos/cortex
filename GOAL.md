# GOAL.md — Ralph loop: finish the Cortex near-term roadmap

This file is the immutable specification for an autonomous build loop.
Each iteration is a fresh, memoryless agent that reads this file and
STATE.md, lands ONE verified increment, and commits. Do not edit this
file (see §8 for the only exception).

## 1. North star & pillars

Cortex gets strong coding work out of small/local models by managing
their context well. This loop finishes the remaining near-term roadmap
items (ROADMAP.md, 2026-07-10 sequence) **excluding item 7 (web track —
handled in parallel elsewhere; do not touch `docs/cortex-web.md` scope)**:

- Roadmap item 6 — the general **`agent` tool** (three slices, in order).
- Roadmap item 4 remainder — the **README pass** against the current
  product surface.
- Roadmap item 8 — the **Think/Dream design-gate doc** (a document and a
  decision; NO implementation).

An outcome is *more correct* when it is a small, test-anchored change
that reuses the existing seams (`Subagent` registry, `runLoop`,
config-gated tools per `docs/IMPLEMENTATION-PATTERN.md`); it is *less
correct* when it adds parallel machinery, new abstractions, or scope the
roadmap did not ask for.

Pillars (tie-breakers, in order):
1. Green gate over progress — never commit red.
2. Smallest change that completes the increment.
3. Reuse existing seams; extend, don't fork.
4. Tests are the spec — every behavior change lands with a test that
   would fail if the change were reverted.
5. Match house style (CLAUDE.md: stdlib `testing` only, table-driven
   `t.Run` subtests, `fmt.Errorf("...: %w", err)` wrapping).

Non-goals (do not relitigate):
- No web-track work (item 7) — parallel track, out of scope here.
- No Think/Dream *implementation* — item 8 is a doc only.
- No restoring removed subsystems (`pkg/cognition`, `internal/study`
  engine, retrieval pipeline) — deleted deliberately, see `docs/archive.md`.
- No new dependencies. No testify. No tree-sitter.
- No `git push`, ever. No branch switching. No PRs.
- No edits to ROADMAP.md except ticking items landed by this loop.

## 2. Invariants & verify harness

- **Workspace:** `/Users/dereksantos/eng/projects/cortex-ralph`, branch
  `ralph/roadmap`. All work happens here. NEVER push. NEVER switch
  branches. NEVER touch the sibling checkout at `eng/projects/cortex`.
- **Language/stack:** Go, stdlib testing only. One LLM layer (`pkg/llm`).
- **Verify command (ground truth, run from repo root):**

  ```
  ./scripts/check.sh && go build ./... && go test ./...
  ```

  All three legs must exit 0. Env-gated live tests skip by default —
  that is fine; do not set their env vars. Budget: if the suite exceeds
  10 minutes something is wrong — investigate, don't wait.
- Checks are append-only: a check added by a prior milestone regressing
  blocks all forward work until green again.
- Never weaken or delete a check to get green. A genuinely wrong check
  may be corrected only with a Decisions Log entry in the same commit.

## 3. Subsystem spec — the `agent` tool (roadmap item 6)

Built ON the registered `Subagent` profile system, not beside it. The
registry (`internal/tools/tools.go` — `Register(Study)` around line 433)
was built for inheritors; Study stays untouched as the read-only profile.

### Slice 1 — per-profile seeding (milestone M1)
`runSubagent` (`internal/tools/tools.go`, ~line 569) hardwires
`StudySeed(goal, path, ol)` for every profile. Move seed construction
into the profile: add a seed-builder field to `Subagent` (e.g.
`Seed func(goal, path, outline string) string`), default it to
`StudySeed` for Study, and have `runSubagent` call the profile's field.

Acceptance (all automatable):
- All pre-existing tests stay green (verify exits 0).
- New test `TestProfileSeedSeam` in `internal/tools`: (a) the Study
  profile's seed output is byte-identical to `StudySeed(goal, path, ol)`
  for a fixed input triple; (b) a registered fake profile with a custom
  seed func receives its own seed, not StudySeed's.

### Slice 2 — explicit depth policy (milestone M2)
Replace the blanket no-recursion rule with a per-profile depth cap
threaded through `RunSubagent` (`cmd/cortex/study.go`, ~line 42). Study
stays cap 0 (cannot spawn subagents). The cap must be enforced in the
shared path so a future cap-1 profile can spawn depth-1 children whose
own subagent tools are refused at depth 2.

Acceptance:
- New test `TestSubagentDepthPolicy`: (a) at Study's cap 0, a subagent
  tool call from inside the subagent loop is refused with a clear error;
  (b) a fake cap-1 profile can dispatch one nested subagent, and that
  child's own subagent call is refused. Drive with a scripted `Sender`
  (see existing loop tests for the pattern).
- Study behavior unchanged: existing study tests green.

### Slice 3a — design note (milestone M3)
Write `docs/agent-tool.md` (≤150 lines) settling exactly three
decisions, each with a one-paragraph rationale and a DECISION line:
1. **Toolset scope** — which tools the `agent` profile gets (read/search
   set plus `write_file`/`edit_file`? `bash`?).
2. **`shellrisk` Risky inside a subagent** — no human mid-loop; the
   default answer per ROADMAP.md is treat-as-headless (Risky ⇒ Blocked)
   unless the doc argues otherwise.
3. **Excluded coder-only tools** — `recall`, memory writes, context
   tools stay out; confirm and enumerate.

Acceptance: file exists; `grep -c '^DECISION:' docs/agent-tool.md` ≥ 3.

### Slice 3b — the `agent` profile (milestone M4)
Register a general `agent` profile implementing M3's decisions: own
system prompt, tool allowlist, mandatory `Bounds`, depth cap 1,
config-gated `tools.enable_agent` following
`docs/IMPLEMENTATION-PATTERN.md` (same shape as `tools.enable_context_*`).

Acceptance:
- Profile-registry + seed-seam unit tests in `internal/tools` cover the
  new profile (registration, gating on/off, allowlist excludes the M3
  exclusions).
- `TestAgentToolEndToEnd`: a scripted-`Sender` loop test driving a full
  `agent` tool call end to end (coder calls `agent` → subagent runs ≥1
  tool → digest returns to the coder turn).
- Verify green; ROADMAP.md item 6 marked *(landed)* in the same commit.

## 4. Subsystem spec — README pass (milestone M5)

Bring README.md in line with the current product surface (CLAUDE.md is
the reference for what exists). Acceptance, all via grep on README.md:
- Documents every command: `cortex` (REPL), `resume`, `turn`, `study`,
  `change`, `discord`, `study-eval` — each appears at least once.
- Mentions the memory tools (`memory_write` or "memory tools"), `study`,
  `recall`, the context tools (`context_evict` or "context tools"), and
  `web_search`/`fetch_url`.
- Mentions the `agent` tool iff M4 has landed (do M5 after M4).
- Zero references to removed surfaces: `project_index`, `/remember`,
  `/forget`, `cortex daemon`, `pkg/cognition`.

## 5. Subsystem spec — Think/Dream design doc (milestone M6)

Write `docs/think-dream-eval.md` answering the roadmap's design gate:
would a simplified background-curation (Think/Dream) layer improve
long-horizon curation beyond what the memory tools + context tools
already deliver — and what eval would prove it? Document, not code.

Required section headings (exact, `##` level): `## Decision question`,
`## Prior art in git history`, `## The eval`, `## Go / no-go criteria`.
The eval section must define measurable outcomes (pass/fail assertions,
not judgments), consistent with the style of existing eval docs
(`docs/eval-context-pivot.md` is the worked example).

Acceptance: file exists; all four headings present (grep); ROADMAP.md
item 8 marked as *(design doc written — decision pending owner review)*.

## 6. Milestone ladder (fixed, strictly sequential)

| # | Milestone | DoD (all must pass) |
|---|---|---|
| M0 | Bootstrap | GOAL.md + STATE.md committed on `ralph/roadmap`; verify exits 0 at baseline. *(done by the loop owner at setup)* |
| M1 | Agent slice 1: per-profile seeding | §3 slice 1 acceptance; verify green |
| M2 | Agent slice 2: depth policy | §3 slice 2 acceptance; verify green |
| M3 | Agent slice 3a: design note | §3 slice 3a acceptance |
| M4 | Agent slice 3b: `agent` profile | §3 slice 3b acceptance; verify green |
| M5 | README pass | §4 acceptance |
| M6 | Think/Dream design doc | §5 acceptance |

When M6's DoD is checked, write `LOOP-COMPLETE` as the first line of
STATE.md's Next Up section. That token is the loop's stop signal.

## 7. Iteration protocol & STATE.md

Each iteration, in order:
1. Read GOAL.md fully. Read STATE.md.
2. If the working tree is dirty (a prior iteration died mid-flight):
   run verify; if green, commit what exists with message
   `chore(ralph): recover uncommitted work`; if red, `git reset --hard
   HEAD` and append a dated Known Issues line saying so.
3. Run the verify command. It is ground truth — if it disagrees with
   STATE.md's checklist, fix STATE.md first (that may be the iteration's
   increment).
4. Pick the SINGLE next unchecked increment of the current milestone.
   At milestone start, first enumerate its DoD items as unchecked
   increments in STATE.md. An increment too big for one iteration gets
   split IN the checklist — that split is the iteration's work.
5. Implement the smallest change that completes it. Extend the harness
   so reverting the increment would fail a check. Red exists only in
   the working tree, never in a commit.
6. Run verify until it exits 0. Never commit red.
7. Commit (conventional message, reference the milestone, e.g.
   `feat(tools): per-profile seed seam (M1)`). Update STATE.md in the
   same commit: tick items with the commit hash, set Next Up, append
   Decisions/Known Issues as needed. One commit minimum per iteration —
   an iteration that commits nothing never happened.
8. Do NOT push. Do not create branches or PRs.

**Three-strike rule:** every failed attempt at an increment MUST append
a dated line to Known Issues: `attempt N at <increment>: tried X,
failed because Y` — then commit that STATE.md change (this satisfies
the one-commit minimum). At attempt 3, stop retrying: mark the
increment `BLOCKED` in the checklist, write what a human should decide
in Next Up, and move to the next non-dependent increment if one exists;
otherwise write `LOOP-BLOCKED` as the first line of Next Up.

**STATE.md recovery:** if STATE.md is missing or malformed, recover the
last committed version from git history first (`git log -- STATE.md`);
carry Decisions Log + Known Issues forward verbatim; rebuild the
checklist from §6's DoD lists, ticking an item iff its check passes now.

**Conflict precedence:** §6 wins for WHEN; the most specific section
wins for HOW; record the chosen reading in the Decisions Log so every
iteration resolves it identically.

**STATE.md template:**

```markdown
# STATE.md — ralph/roadmap loop memory

## Current milestone
M<n> — <name>

## Checklist
- [x] <increment> (<commit hash>)
- [ ] <increment>

## How to run / verify (quickref — keep short)
./scripts/check.sh && go build ./... && go test ./...

## Decisions Log
- 2026-07-11: GOAL.md v1 finalized.

## Known Issues
(attempt lines and environment notes go here)

## Next Up
<one specific, immediately startable sentence>
```

## 8. Amendments (project owner only)

Only Derek (the loop owner, outside the loop) may amend this file, e.g.
to relocate a check the loop's environment cannot run. Iterations never
edit GOAL.md.
