# Phase 1 — Tool-Surface Foundation

Three deliverables landed end-to-end: (1) generated `tools.json` from the
cobra command tree with version field + CI diff check; (2) uniform
`{ok, data, error, meta:{trace_id, latency_ms}}` envelope on every
`--json` CLI output; (3) unified `cell_results.jsonl` sink — every CLI
invocation writes a row, not just eval cells.

This phase is the verification floor for every subsequent eval phase
and every build stage. Phase A baseline ran into BLOCKED entries
because this sink doesn't exist; B/D/E/F + the entire DAG protocol
build depend on it.

See [`docs/integration-roadmap.md`](../integration-roadmap.md) Phase 1,
[`docs/tool-surface.md`](../tool-surface.md) for the 6-axis contract,
and [`docs/prompts/eval-principles.md`](eval-principles.md) for the
principles every result row must satisfy.

## Working environment

- **Worktree:** `/Users/dereksantos/eng/projects/cortex-dag-build/`
- **Branch:** `derek.s/dag-build`
- **Build:** `go build -o bin/cortex ./cmd/cortex` after each change
- **Tests:** `go test ./...` after each change (TDD where possible)

All paths in the loop instructions below are **relative to the
worktree root**. If `pwd` is not the worktree root, `cd` there first.

## Loop

Each iteration:

0. **Verify environment.** Run `pwd && git rev-parse --abbrev-ref HEAD`.
   If `pwd` is not `/Users/dereksantos/eng/projects/cortex-dag-build`
   or the branch is not `derek.s/dag-build`, STOP and report — do not
   proceed.

1. **Read state.** Open `docs/integration-roadmap.md` Phase 1 and
   check which of the 3 deliverables are checked off. Open
   `docs/tool-surface.md` to confirm the axis contract for the
   in-flight deliverable.

2. **Pick the next deliverable** in this priority order (skip any
   already checked off):
   - **Deliverable 1: `tools.json` generator** — most independent
   - **Deliverable 2: uniform envelope wrapper** — used by deliverable 3
   - **Deliverable 3: unified `cell_results.jsonl` sink** — depends on
     envelope shape

3. **Implement the deliverable.**

   For **(1) `tools.json` generator:**
   - Walk the cobra command tree (probably under
     `cmd/cortex/commands/`).
   - Emit JSON manifest per command: `name`, `description` (from
     cobra `Short`/`Long`), `args` schema, `flags` schema, `version`
     field.
   - Write to `tools.json` at repo root.
   - Add a `go generate` directive or build-step + a CI check that
     fails when the generated file diverges from committed.

   For **(2) uniform envelope:**
   - Define `pkg/cliout/envelope.go` (or similar) with the canonical
     shape: `{ok bool, data any, error {code, message}, meta:
     {trace_id, latency_ms, truncated bool}}`.
   - Add a wrapper helper every command that emits `--json` uses
     instead of raw `json.Marshal`.
   - Path redaction outside `.cortex/` and the project root applies in
     the wrapper.
   - Migrate existing `--json` consumers (search, capture, recent,
     insights, eval) one at a time, with tests.

   For **(3) unified `cell_results.jsonl` sink:**
   - Per-CLI-invocation `cell_results.jsonl` row writes under
     `.cortex/db/cell_results.jsonl` regardless of whether invocation
     is inside an eval cell or ad-hoc.
   - Row schema includes: `timestamp`, `cortex_function` (tag),
     `tool` (op name), `command` (CLI verb), `latency_ms`, `tokens`
     (where applicable), `cost_usd` (where applicable), `ok`,
     `error_code`, `trace_id` (matches envelope meta), `bytes_in`,
     `bytes_out`.
   - Eval cell rows continue to land in the same file — schema must
     superset the existing eval-cell row format so no current
     consumer breaks.
   - Add a `--no-telemetry` flag for users who want to opt out.

4. **Test.** `go build ./...` then `go test ./...`. Land tests
   alongside the change, not after.

5. **Commit** with conventional-commits style. One commit per
   deliverable. Examples:
   - `feat(cli): generate tools.json manifest from cobra command tree`
   - `feat(cliout): uniform envelope on --json output`
   - `feat(telemetry): unified cell_results.jsonl sink for all CLI invocations`
   Do NOT push.

6. **Update `docs/integration-roadmap.md`** Phase 1 to check off the
   completed deliverable.

7. **Check stopping condition** (next section). If all 3 done + tests
   green + Phase A re-run succeeds, STOP.

## Constraints

- **Do not modify the v2 eval framework** (`internal/eval/v2/*`) or
  the cognitive mode implementations (`internal/cognition/*`). Those
  are Phase 3 / Phase 5 territory.
- **Do not change the cobra command surface** (Use / Short / Long
  strings). The generator reads those; it does not rewrite them.
- **Do not push to remote.** Local commits only.
- **Do not break existing automated consumers.** Check `integrations/`
  for anything parsing `--json` output and migrate carefully.
- **Per eval-principles 4 (Reproducible):** the envelope's `meta`
  must capture enough state for a row to be replayable.
- **Per eval-principles 6 (Structured):** the envelope schema is the
  same across every command. No per-command variant.
- **Per `tool-surface.md` axis 5:** mutator commands must continue to
  write journal entries; the envelope is for *output*, not a
  replacement for journal capture.

## Verification

Per deliverable:

**(1) `tools.json` generator:**
- ☐ `tools.json` exists at repo root, committed.
- ☐ Re-running the generator produces an identical file (deterministic).
- ☐ A CI check (or pre-commit hook) fails when the cobra surface
  changes without a regenerated manifest.
- ☐ Each entry has a `version` field.

**(2) uniform envelope:**
- ☐ Every existing `--json`-supporting command's output validates
  against the schema.
- ☐ `truncated: true` set on outputs that hit a size cap.
- ☐ Paths outside `.cortex/` and the project root are redacted in
  `data` fields.
- ☐ `meta.trace_id` is unique per invocation; `meta.latency_ms` is
  populated.

**(3) unified `cell_results.jsonl`:**
- ☐ Running `cortex search "X"` outside an eval produces a row in
  `.cortex/db/cell_results.jsonl`.
- ☐ Running the same query inside an eval produces a row with the
  same schema (different `cell_id` context but identical structure).
- ☐ Existing eval-cell rows continue to land unchanged (no
  back-compat break).
- ☐ `--no-telemetry` suppresses the row write.

Loop-wide stopping condition:
- ☐ All 3 deliverables checked off in `docs/integration-roadmap.md`.
- ☐ `go test ./...` green.
- ☐ A re-run of Phase A's eval suites (v2 + ABR) now produces telemetry
  rows in `.cortex/db/cell_results.jsonl` for the ad-hoc CLI
  invocations that previously landed as BLOCKED in `eval-journal.md`.
- ☐ Write a short summary entry in `eval-journal.md`: "Phase 1
  complete; Phase A re-run viable."

## When to ask the user

- If the cobra structure doesn't surface JSON schemas naturally and
  authoring them by hand for 20+ commands would dominate the work.
- If the envelope migration would break an automated consumer in
  `integrations/` and the right answer isn't obvious.
- If unified `cell_results.jsonl` would meaningfully grow disk usage
  and we should discuss a retention policy.
- If the Phase A re-run still produces BLOCKED entries after the sink
  lands — that means something else is wrong; stop and surface it.
