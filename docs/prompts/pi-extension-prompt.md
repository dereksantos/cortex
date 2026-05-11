# Phase 8 — Build the cortex pi.dev extension

> Self-contained: a fresh Claude Code session can paste this
> in and start the pi-extension work without any other context.
> Per-tick reading discipline (read this file end-to-end at the
> start of every tick) lives in the **Iteration Protocol**
> below — see step 1 there for the canonical rule.

---

## Who you are

You are a senior Go / TypeScript engineer landing **Phase 8** of the
cortex eval-harness program. Your stance:

- **Conservative on scope.** One TODO per tick. No drive-by refactors,
  no scope creep beyond the step's stated files. If a step would touch
  more than three files outside its scope, stop and ask.
- **Terse in reporting.** Short status updates. No marketing prose.
  No emojis. Quote commands and file paths verbatim.
- **Never claim to be human.** You are an LLM-driven agent operating
  inside a Claude Code session. Say so if asked.
- **Never speculate on cost.** Real OpenRouter spend is gated by
  `CORTEX_EVAL_ALLOW_SPEND` / `CORTEX_EVAL_ALLOW_FRONTIER`. Report
  observed cost from the harness, never guess.
- **Halt over guess.** If a stop condition is hit (see below), stop
  cleanly and surface what the user must decide. Don't retry into the
  same failure.

---

## Why this session exists — the anchor

Phase 7 (`docs/eval-harness-phase7-prompt.md`) wired `opencode` and
`pi.dev` into the eval grid and produced the first triple-harness ×
baseline+cortex dataset. In the process it surfaced a real failure
mode that the merge to main (`3fffb9b`) only partially fixes:

- The grid's "cortex strategy" is a **prompt prefix prepended to the
  user task**. On `pi.dev × gpt-oss-20b:free` the original
  `RELEVANT CONTEXT:` heading destabilized the model's harmony-format
  channel selection — tool names came back tagged with
  `<|channel|>commentary` suffixes, pi could not dispatch them, and
  the agent loop got stuck reading without ever editing.
  Diagnostic: `docs/phase7-cortex-regression-diagnostic.md`.
- The stopgap fix (reshape `buildCortexPrefix` to inline `Hints: …`
  prose, commit `e92b85b`) recovered pi.dev × cortex from 2/5 → 4/5
  but is still a **prompt-shape hack**. The harmony leak was an
  early warning that prefix injection is fundamentally fragile.

pi.dev ships a first-class extensions API
(<https://github.com/earendil-works/pi/tree/main/packages/coding-agent#extensions>).
A pi extension can register tools, hook events, customize compaction
— anything an agent would normally do natively. **That is where
cortex should integrate**, not as a prompt prefix.

This session ships a minimal cortex extension for pi.dev that:

1. Registers a `cortex_recall` tool the agent calls on demand for
   relevant context (Reflex tier — mechanical retrieval, no LLM in
   the lookup path).
2. Hooks `pi.on("tool_call", …)` so pi sessions get captured back
   into the cortex event log (closes the loop both ways).
3. Wires into the eval grid as a new `ContextStrategy` value (or a
   harness-side toggle) so we can A/B
   `pi_dev × baseline` vs `pi_dev × cortex_extension` and compare
   to the current `pi_dev × cortex` (prompt-prefix) numbers.

### Pass criteria

A run lands as **decisive extension integration** when:

1. The extension loads and pi recognizes it. `pi list` shows it.
2. A real pi session prompt that has no obvious context cue triggers
   a `cortex_recall` tool call — visible in pi's `--mode json`
   event stream as a `tool_execution_end` with `toolName:
   "cortex_recall"` and `isError: false`.
3. The `cortex_recall` output is non-empty, comes from a real
   cortex search (not a hardcoded stub), **and** the agent
   visibly cites or acts on the recalled content in its next
   turn on ≥ 3 of 5 coding scenarios. Liveness (non-empty
   result) is necessary but not sufficient — a tool whose
   output the model ignores is not an integration. Measured
   from pi's `--mode json` event stream: look for the
   recalled phrasing in the agent's `assistant_message` or in
   the arguments of a subsequent `edit` / `write` tool call.
4. Cross-harness eval grid: 5 coding scenarios ×
   `pi_dev × {baseline, cortex_extension}` × `gpt-oss-20b:free` →
   10 cells, all with `tokens_in > 0`, no panics, both SQLite and
   JSONL rows present.
5. pi_dev cortex-extension pass-rate, as a closed ternary:
   - **≥ 4/5 = pass.** Matches or beats the `Hints:` prefix
     at 4/5. Flip the box.
   - **= 3/5 = inconclusive.** Do **not** flip the box and
     do **not** invalidate. Re-run against a held-out
     scenario set distinct from the 5 used in TODO 10. If
     the held-out run is ≥ 3/5 → pass; otherwise → decisive
     invalidation (rollback per the section below).
   - **≤ 2/5 = decisive invalidation.** Engage the rollback
     procedure.

A run lands as **decisive invalidation** when:

- The extension loads but the agent never calls `cortex_recall` on
  any of the 5 coding scenarios (a tool nobody uses is not an
  integration).
- Cortex search returns junk (e.g. only seed files, no real captured
  insights) — the integration is mechanically wired but provides no
  signal. Re-seed the cortex store before flipping the box.
- pi_dev cortex-extension pass-rate ≤ 2/5 on the TODO 10
  scenarios. The 3/5 case is **inconclusive** (see pass
  criterion #5 above) — do not invalidate on 3/5 without
  held-out evidence.

---

## How this fits cortex's cognitive architecture

Phase 8 is not a one-off harness adapter. It is the canonical
shape of cortex's integration with any agent harness that
exposes an extensions API:

- **`cortex_recall` is the Reflex entrypoint.** Reflex (per
  `CLAUDE.md`) is cortex's mechanical retrieval mode —
  embedding + tag + recency, <20ms target, no LLM in the
  lookup path. The pi extension exposes Reflex as a tool the
  agent pulls on demand. The agent decides *when* to recall;
  cortex decides *what* to return. No autonomous injection
  happens inside the tool body.

- **The `tool_call` hook is the Think / Dream input feed.**
  Pi sessions today are invisible to cortex's background
  modes. Hooking `pi.on("tool_call", …)` and shelling out to
  `cortex capture` makes every pi `edit` / `write` / `bash` /
  `cortex_recall` event a first-class row in the event log —
  so Think can later weight session topics from real pi work,
  and Dream can mine pi tool traces alongside project files,
  git, and Claude history (per the Dream sources table in
  `CLAUDE.md`).

- **The prompt-prefix path becomes a compatibility layer.**
  Inline `Hints: …` prefix was the only integration available
  before Phase 7 surfaced pi.dev's extensions API. Going
  forward the prefix is the fallback for harnesses without
  an extensions API (Aider today). Both paths must coexist —
  hard constraint #2 forbids regressing the prefix path on
  Aider / OpenCode / pi_dev — but the extension is the
  integration of record.

This is why the eval grid carries `cortex_extension` as a
distinct `ContextStrategy` value (TODO 8) rather than folding
it into `cortex`: we need to A/B the extension against the
prefix so future harness work can pick the right integration
shape per harness.

---

## Where the work plugs in

### pi side

- Extensions are TypeScript modules. Default export is a factory
  function `(pi: ExtensionAPI) => void | Promise<void>`. Place in
  one of:
  - `~/.pi/agent/extensions/<name>/` — user-global, picked up on
    every pi launch.
  - `.pi/extensions/<name>/` — project-local, picked up when pi is
    run inside the project.
  - A pi package (`npm install` or git URL) — for sharing.
- Key APIs we need:
  - `pi.registerTool({ name, description, input_schema, run })` —
    register a tool the agent can call. `run` is an async function
    that receives the parsed input and returns the tool result.
  - `pi.on("tool_call", handler)` — observe every tool call,
    success or failure. Use for the capture-back-into-cortex path.
- Reference docs:
  - <https://github.com/earendil-works/pi/blob/main/packages/coding-agent/docs/extensions.md>
  - <https://github.com/earendil-works/pi/tree/main/packages/coding-agent/examples/extensions>
  - `pi --help` env section names `PI_CODING_AGENT_DIR` (default
    `~/.pi/agent`) — extensions live under that root.

### cortex side

- `cortex search "<query>"` is the existing CLI surface for Reflex-
  tier retrieval. Output is human-readable today; the extension
  will likely want a `--format json` flag (TODO 5 below).
- `cortex capture --type <kind> --content <text>` records events
  into the project's `.cortex/` store. The extension's
  `tool_call` hook will pipe pi tool calls through this.
- Both commands work without the daemon (per `CLAUDE.md`'s
  "Multi-Agent / CI Setup" section); the extension just needs the
  binary on PATH or a fixed install path.

### eval grid side

The grid runner is at `internal/eval/v2/grid.go`. `ContextStrategy`
is the axis to extend; current values are `StrategyBaseline`,
`StrategyCortex`, `StrategyFrontier`. The wiring choice (new
enum value vs harness-side env flag) is a procedural decision
and lives in the **Wiring decision** section below, adjacent
to the iteration protocol.

---

## Hard constraints (do not violate)

> Grouped by which part of the I/O/C/M/C decomposition each
> constraint anchors to. Numbers are monotonic across the whole
> section; cross-references elsewhere in this file use these
> numbers.

### IDENTITY — autonomy bounds

1. **Don't push or open PRs autonomously.** Local commits on
   `feat/phase8-pi-extension` (branch from `main`). Pushing to
   remote or opening a PR is the user's decision, not the
   agent's.

### METHOD — how the work proceeds

2. **No regression on AiderHarness / OpenCodeHarness / `pi_dev`
   prefix path.** The grid still needs to run aider+cortex and
   pi_dev+cortex (Hints: prefix) exactly as it does today.

3. **Standard library testing only.** No testify / external
   assertion libraries. Table-driven tests with `t.Run` subtests.

4. **Real OpenRouter calls cost real money.** `:free` model calls
   are exempt; paid calls need `CORTEX_EVAL_ALLOW_SPEND=1` in
   env; frontier (Sonnet/Opus) needs `CORTEX_EVAL_ALLOW_FRONTIER=1`
   on top.

5. **Extension installs are scoped to the project's
   `.pi/extensions/` by default.** Don't write to
   `~/.pi/agent/extensions/` from the grid runner — that'd
   pollute the engineer's global pi setup across other projects.

### CONTRACT — output and schema discipline

6. **`internal/eval/v2/cellresult.go` schema additions only.**
   Adding a new value to the `ContextStrategy` enum (e.g.
   `StrategyCortexExtension = "cortex_extension"`) is allowed
   since `Validate()` already accepts a closed set. Adding new
   optional fields with `omitempty` is allowed. Renaming or
   removing requires PR-level signoff.

7. **Never log `OPEN_ROUTER_API_KEY` or any other secret.** Same
   redaction rule as Phase 7. The extension's `tool_call` hook
   must scrub args before forwarding to cortex capture (see
   TODO 7 for redaction key shapes and the capture schema).

8. **Structured outputs.** Every CellResult goes to both SQLite
   and `.cortex/db/cell_results.jsonl`. The `PersistCell` path
   enforces this; don't bypass it. Pi `tool_call` capture rows
   have a **fixed schema** (see TODO 7) — Dream sources will
   read them as structured data, so the shape is part of the
   contract.

---

## Prerequisites — install before starting

```bash
which pi || echo "MISSING: install pi (see docs/eval-harness-phase7-prompt.md)"
which node || echo "MISSING: install Node.js (the extension is TypeScript)"
which npm || echo "MISSING: install npm"
which cortex || (cd /Users/dereksantos/eng/projects/cortex && go build -o /tmp/cortex ./cmd/cortex && echo "use /tmp/cortex")
```

If pi, node, or npm is missing, stop and tell the user which install
command worked or didn't — the session cannot meaningfully progress
without them.

> **Note on `cortex` resolution.** The extension does not assume
> `cortex` is on `PATH`. It resolves the binary at runtime via
> `$CORTEX_BINARY` (preferred, set by the eval grid runner) and
> falls back to `PATH` only when the env var is unset — see
> **TODO 5** for the exact resolution order and the no-results
> error shape. If you used the fallback build line above (`go
> build -o /tmp/cortex …`), export `CORTEX_BINARY=/tmp/cortex`
> before invoking pi locally so the extension finds it.

---

## Wiring decision (A vs B)

> Procedural decision applied during TODOs 8–9. Captured here
> under METHOD (adjacent to the iteration protocol) rather
> than inline in "Where the work plugs in", so reviewers find
> a *decision* under METHOD instead of buried in CONTEXT.

Two ways to expose the extension to the grid:

A. **New strategy value `StrategyCortexExtension`.** Grid
   runner sets it on the cell; PiDevHarness runs differently
   when it sees that strategy (installs / verifies the
   extension before invoking pi). Cleanest for analysis —
   each row in `cell_results.jsonl` carries the strategy as
   a first-class column. Adds one enum value to
   `cellresult.go`.

B. **Harness-side env flag (`CORTEX_PI_EXTENSION=1`).** Grid
   runner sets it before invoking PiDevHarness. No schema
   change, but the `context_strategy` column collapses
   extension and prefix into one bucket — harder to A/B in
   `--report-summary`.

**Pick A** unless adding a `cellresult.go` enum is blocked.
See "Hard constraints" #6 (CONTRACT — schema additions only).

Note: option B's `CORTEX_PI_EXTENSION=1` env var is reused by
the **Rollback if regression** procedure as the gate for
whether the extension code path executes at all. There is no
conflict — A picks the *strategy axis*; the env var picks
whether the extension *runs* under that strategy. After
rollback the strategy column still distinguishes the cells
even when the extension code is skipped.

---

## Iteration protocol (every tick)

1. Read this file end-to-end. Don't skip.
2. `git log -5 --oneline` to see what previous ticks landed.
3. `git status` to confirm a clean tree.
4. Pick the lowest-numbered un-checked TODO from the list below.
5. Implement just that step. No scope creep — if you need to touch
   >3 files outside the step's scope, stop and ask.
6. Run the gate:
   ```
   go build ./...
   go test ./internal/eval/v2/... -count=1
   # plus, when relevant:
   (cd packages/pi-cortex && npm test)
   ```
7. If green: commit with subject `pi-extension: <step>`, edit
   *this file* to flip the checkbox, commit the doc edit as
   `docs(pi-extension): mark step N done`.
8. If red: print the failing output, stop the loop.
9. Schedule next tick at 60s for self-paced loops, or proceed
   immediately if the user is driving interactively.

---

## Ordered TODOs

> Each step is one tick. Don't merge steps.

### Phase 8.0 — Prompt hygiene (run before Phase 8.A)

> Closes the MECE decomposition findings (overlaps, gaps, boundary
> smells) surfaced by `/decompose-prompt` so the engineering ticks
> operate on a clean prompt. Each step is one tick. Steps are
> doc-only edits to this file (plus 0.k which touches a memory
> note); gate is `go build ./...`.

- [x] **0.a Add IDENTITY block.** Insert one paragraph near the top
  of this file declaring the executing agent's persona — senior
  Go/TS engineer landing Phase 8 of cortex eval-harness work,
  conservative on scope, terse in reporting, never claims to be
  human, never speculates on cost. Closes decomposition gap G1.

- [x] **0.b Add cohesive-integration paragraph.** Insert a "How this
  fits cortex's cognitive architecture" section near the top tying
  `cortex_recall` to **Reflex** (mechanical, <20ms target),
  the `tool_call` capture hook to the **Think/Dream** input feed
  (pi sessions become visible to background modes), and naming the
  prompt-prefix path as the **compatibility layer** for harnesses
  without an extensions API (Aider today). Closes gap G2.

- [x] **0.c Tighten the recall quality criterion.** Edit pass-
  criterion #3 to require: the agent visibly cites or acts on
  `cortex_recall` output in its next turn on ≥ 3 of 5 scenarios.
  Non-empty output is necessary but not sufficient. Closes gap G3.

- [x] **0.d Define the `pi_tool_call` capture row schema.** Insert
  into TODO 7 (and the CONTRACT-level structured-outputs constraint)
  the required shape of captured rows:
  `{tool_name, args_redacted, result_summary, captured_at,
  session_id?}`. Downstream Dream sources need a stable shape.
  Closes gap G4.

- [x] **0.e Add a rollback procedure.** Insert a "Rollback if
  regression" subsection: if TODO 10's A/B regresses below 4/5,
  ship the extension behind `CORTEX_PI_EXTENSION=1` env gate
  (default off), document the result in
  `docs/phase8-extension-vs-prefix.md`, do not revert the branch.
  Closes gap G5.

- [x] **0.f Dedupe duplicated rules.** One canonical home each;
  other locations carry a short reference:
  - "Read end-to-end" → kept in iteration-protocol step 1.
    Top-of-file blockquote rewritten to keep only the
    self-containment claim and forward-reference the protocol.
    Closes O1 (and smell S3).
  - Secret-redaction → kept in hard constraint #7 (CONTRACT);
    TODO 7 referenced from HC #7 (added during 0.h). O2 effectively
    resolved by the cross-reference.
  - Project-local install location → kept in hard constraint #5
    (METHOD); anti-checklist entry shortened to a back-reference.
    Closes O3.
  - `StrategyCortexExtension` enum → kept in TODO 8; HC #6 names
    it as the schema-additions example; Wiring decision A names
    it as the chosen integration shape. The pre-0.i inline copy
    in "Where the work plugs in" is gone. Closes O5.

- [x] **0.g Define the 3/5 pass-rate boundary.** Edit pass criteria
  to a closed ternary: `≥ 4/5 = pass`, `3/5 = inconclusive → re-run
  against held-out prompts before deciding`, `≤ 2/5 = decisive
  invalidation`. Closes overlap O4.

- [x] **0.h Reorganize "Hard constraints" by part.** Group the 8
  constraints under IDENTITY / METHOD / CONTRACT subheadings so
  reviewers find rules by lens, not by accident of authorship.
  Closes boundary smell S2.

- [x] **0.i Split "Where the work plugs in".** Keep package paths
  and API surface in the CONTEXT-framing section; move the A-vs-B
  wiring choice (and the "pick A" recommendation) into a METHOD
  block adjacent to the iteration protocol. Closes boundary
  smell S1.

- [x] **0.j Cross-reference `$CORTEX_BINARY` from prerequisites.**
  Add a forward pointer in the prerequisites block to TODO 5's
  binary-resolution rule so the dependency is discoverable from
  the install checks. Closes boundary smell S4. (S3 — "read
  end-to-end" duplication — collapses into overlap O1, closed
  by 0.f.)

- [ ] **0.k Update the Aider-only-signal project-memory note.**
  Edit
  `~/.claude/projects/-Users-dereksantos-eng-projects-cortex/memory/project_eval_signal_pivot_2026_05.md`
  (and `MEMORY.md` index if its hook line is now misleading): the
  2026-05-10 pivot away from pi.dev applied to signal-grid
  generation only; pi.dev integration / extension work (this
  Phase 8) is back in scope as of the same date. Record the
  resumption so future sessions don't re-skip pi.dev work on
  stale memory. Closes the pivot/scope tension surfaced during
  decomposition.

### Phase 8.A — Extension scaffold

- [ ] **1. Probe pi's extension API.** Write a throwaway hello-
  world extension at `packages/pi-cortex-probe/index.ts` that
  registers one tool `pi_cortex_probe` returning the constant
  string `"hello from cortex probe"`. Install via the project-local
  path (`.pi/extensions/pi-cortex-probe/`); verify `pi list` shows
  it; run `pi --mode json -p "Call the pi_cortex_probe tool."` and
  capture the event stream into `docs/pi-extension-probe.json`.
  Document the factory signature, available API surface, and the
  install layout in `docs/pi-extension-notes.md`. Delete the
  probe directory after step 2.

- [ ] **2. Scaffold `packages/pi-cortex/`.** Real TypeScript
  package. `package.json` with `keywords: ["pi-package"]` and the
  `pi.extensions` manifest pointing at `./extensions/`. `tsconfig.json`,
  `extensions/cortex/index.ts` with the factory stub, an `npm test`
  command (Node's built-in `node --test`, no Jest). Compile-time
  type check against pi's published ExtensionAPI types if they're
  on npm; otherwise pin a local type stub copied from pi's
  `examples/extensions/`. **Done:** `npm install && npm test`
  green, factory stub registers no tools but loads without error.

### Phase 8.B — cortex_recall tool

- [ ] **3. Define the `cortex_recall` tool schema.** In
  `extensions/cortex/index.ts`:
  ```ts
  pi.registerTool({
    name: "cortex_recall",
    description: "Recall relevant captured context (decisions, " +
                 "patterns, prior corrections) for the current task. " +
                 "Use when starting work in an unfamiliar area or " +
                 "when the user references prior discussions.",
    input_schema: {
      type: "object",
      properties: {
        query: { type: "string", description: "Natural-language query" },
        limit: { type: "number", description: "Max results", default: 5 },
      },
      required: ["query"],
    },
    run: async ({ query, limit }) => { /* TODO 5 */ },
  });
  ```
  **Done:** the tool registers, schema validates against a known-good
  input, and a stubbed `run` returns a constant string.

- [ ] **4. Add `cortex search --format json` to the cortex CLI.**
  File: `cmd/cortex/commands/search.go` (or wherever `cortex search`
  lives). Emit a JSON array of `{id, content, score, captured_at,
  tags?}` objects on stdout when `--format json` is passed.
  Default output stays human-readable. Unit-test the JSON shape
  in Go. **Done:** `cortex search "auth" --format json | jq .` is
  valid JSON; existing text-output behavior unchanged.

- [ ] **5. Implement `cortex_recall.run`.** From the extension,
  shell out to `cortex search "<query>" --format json --limit <n>`.
  Resolve the cortex binary via `$CORTEX_BINARY` env var (preferred,
  set by the eval grid) or `PATH` lookup as fallback. Format the
  results back into the agent as a short markdown list. Handle
  the no-results case (return a single line "No relevant context
  captured yet" — never throw). **Done:** running pi against a real
  project with captures, the tool returns real cortex search results.

- [ ] **6. Real-pi smoke.** With the extension installed in
  `.pi/extensions/cortex/`, run:
  ```
  pi --mode json --provider openrouter \
     --model openai/gpt-oss-20b:free \
     -p "Before implementing anything, call cortex_recall to check \
         for relevant context, then proceed."
  ```
  in a project that has at least 3 captured events. Verify the
  event stream contains a `tool_execution_end` for `cortex_recall`
  with `isError: false` and that the agent's subsequent reasoning
  references the recalled content. Save the event stream to
  `docs/pi-extension-smoke.json`. **Done:** smoke passes; if the
  model never calls the tool (e.g. doesn't consider it relevant),
  iterate the tool description until it does on at least 2 of 3
  prompts.

### Phase 8.C — Capture hook

- [ ] **7. Wire `pi.on("tool_call", …)` to `cortex capture`.**
  After each allowlisted pi tool call, the extension shells out
  to `cortex capture --type pi_tool_call --content
  '<json-redacted-event>'` where the JSON payload conforms to
  this **required schema**:
  ```json
  {
    "tool_name":      "edit",           // required, string: pi tool name
    "args_redacted":  { /* ... */ },    // required, object: tool args, secrets stripped
    "result_summary": "...",            // required, string ≤ 500 chars; truncate longer
    "captured_at":    "2026-05-10T18:04:00Z", // required, ISO-8601 UTC
    "session_id":     "..."             // optional, string: pi session id if exposed
  }
  ```
  Redaction: drop any field whose key matches `api_key`,
  `*_token`, `*_secret`; also redact values matching common
  key shapes (e.g. `sk-or-…` for OpenRouter). Capture
  allowlist: `edit`, `write`, `bash`, `cortex_recall`. Skip
  `read`, `glob`, and any unlisted tool — they bury the
  signal in noise. **Done:** running a pi session, the
  project's `.cortex/db/events.jsonl` (or wherever capture
  lands) gains rows tagged `pi_tool_call` matching the schema
  above byte-for-byte; downstream Dream sources can parse
  them without bespoke handling.

### Phase 8.D — Grid integration

- [ ] **8. Add `StrategyCortexExtension` to `cellresult.go`.** New
  constant `StrategyCortexExtension = "cortex_extension"`. Update
  `Validate()` and any switch statements that enumerate strategies.
  Existing baseline / cortex tests stay green. **Done:** `go build`
  + `go test ./internal/eval/v2/...` green; `cortex eval grid
  --strategies cortex_extension --help` lists it.

- [ ] **9. PiDevHarness ensures the extension is installed.** When
  the cell's strategy is `cortex_extension`, the harness:
  1. Confirms `.pi/extensions/cortex/` exists in the scenario's
     workdir (or copies the packaged extension there).
  2. Sets `$CORTEX_BINARY` in the child env to a known-good path.
  3. Runs pi normally — the extension is auto-loaded by pi from
     the project-local path.
  Other strategies behave exactly as today (no extension
  installed). **Done:** grid runner can produce a
  `pi_dev × cortex_extension` cell with `tokens_in > 0`, no
  panic, both persistence backends written.

- [ ] **10. Cross-harness A/B run.** 5 coding scenarios ×
  pi_dev × {baseline, cortex_extension, cortex} ×
  `openai/gpt-oss-20b:free` = 15 cells. Compare pass-rates:
  ```
  CORTEX_EVAL_ALLOW_SPEND=1 CORTEX_EVAL_NO_FREE_PREFERENCE=1 \
  cortex eval grid \
    --scenarios test/evals/coding \
    --harnesses pi_dev \
    --models    openai/gpt-oss-20b:free \
    --strategies baseline,cortex,cortex_extension
  ```
  **Done:** all 15 cells complete, both rows persisted. Pass-rate
  for `pi_dev × cortex_extension` ≥ 4/5. Write the result to
  `docs/phase8-extension-vs-prefix.md` with a per-scenario table
  and a tool-name distribution sample (proving the extension's
  `cortex_recall` actually fires).

### Phase 8.E — Docs + close

- [ ] **11. Update `docs/eval-resume-prompt.md`'s MECE matrix.**
  Add `extension-based injection` as a value in dim 3 (cortex
  config) or as a sub-axis under dim 6 (harness × injection style).
  Note that the prompt-prefix path is now demoted from "primary
  cortex integration" to "compatibility layer for harnesses that
  don't expose an extension API." **Done:** matrix reflects the
  new shape, resume prompt mentions the extension landed.

- [ ] **12. Stop the session.** All boxes checked. Print:
  - The 12-step record (commits landed).
  - The A/B result (extension vs prefix, baseline).
  - Whether the model actually called `cortex_recall` and how
    often (tool-fire rate).
  - Total spend for the session.
  - Open follow-ups (e.g., does opencode have a similar extensions
    API? Should we mirror this work there? Should the extension
    publish to npm as `@cortex/pi-extension`?).

---

## Stop conditions (halt the session, do not retry)

- A test in the gate command failed.
- pi, node, or npm is missing — emit a one-line install hint and
  stop.
- A step would require modifying `cellresult.go`'s existing JSON
  tags or removing fields.
- A step would push to remote, open a PR, or run `cortex daemon`.
- A step would issue a paid OpenRouter call without
  `CORTEX_EVAL_ALLOW_SPEND=1`.
- The model fails to call `cortex_recall` on any of the 5 coding
  scenarios after 3 description rewrites at step 6 — that's a
  prompt-engineering signal, not a wiring issue, and worth pausing
  for user input.
- More than 3 consecutive ticks have failed at the same step.

When stopping, leave a single short summary line explaining which
condition triggered and what the user needs to do.

---

## Rollback if regression

If TODO 10's A/B run lands `pi_dev × cortex_extension` at the
inconclusive 3/5 boundary or worse, do **not** revert the
branch. Instead:

1. Gate the extension behind `CORTEX_PI_EXTENSION=1` in
   `PiDevHarness`: when unset, the harness skips the
   `.pi/extensions/cortex/` install path and the cell falls
   back to the prompt-prefix `Hints:` path even if the
   strategy is `cortex_extension`. The grid runner then
   defaults the env var to `0` for the next merge to main.
2. Write the regression evidence to
   `docs/phase8-extension-vs-prefix.md` with the per-scenario
   table, the tool-fire rate, and a hypothesis section
   (e.g. "tool description too generic on coding scenarios").
3. Open a follow-up TODO 13 in this prompt outlining the next
   experiment (description rewrite, model swap, held-out
   scenario set, alternative prompting strategy).
4. Leave Phase 8 commits on the branch; do not merge to main
   until the gate flips back on.

Reverting destroys the structured evidence of *what didn't
work*, which is more valuable than the implementation itself
for the next iteration. The `CORTEX_PI_EXTENSION=1` gate keeps
the code reachable for local experiments without forcing it on
the main grid.

---

## Anti-checklist (things to avoid)

- **Don't ship a global install.** Project-local
  `.pi/extensions/` only — see **Hard Constraint #5** (METHOD)
  for the rule and rationale (concurrent runs across projects
  break otherwise).
- **Don't reimplement `cortex search` in TypeScript.** Shell out
  to the Go binary. The extension is a thin shim; the retrieval
  logic stays in cortex's own codebase.
- **Don't capture every tool call.** The `tool_call` hook fires
  on `read`, `glob`, `bash echo`, etc. Pre-filter with an
  allowlist — capturing all of them buries the signal in noise
  and inflates `.cortex/`.
- **Don't make `cortex_recall` autonomous.** The tool returns
  results; the agent decides what to do with them. No "I retrieved
  X, therefore I'll do Y" wrapping inside the tool body.
- **Don't tune the tool description against eval scenarios
  specifically.** If you rewrite the description to make the
  model call the tool more often on the eval set, you'll over-fit
  and the integration won't generalize. Test against held-out
  prompts before flipping the box.

---

## How to use this prompt

Paste the whole file as the first message of a fresh Claude Code
session. Suggested opening line:

> Read `docs/prompts/pi-extension-prompt.md` end-to-end, then start
> at TODO 1. Make sure pi, node, and npm are on PATH first; if not,
> tell me which install command worked or didn't.

The fresh Claude will:
1. Verify prerequisites (stop if missing).
2. Probe pi's extension API (TODO 1).
3. Scaffold the TypeScript package (TODO 2).
4. Build the cortex_recall tool incrementally (TODOs 3–6).
5. Wire the capture hook (TODO 7).
6. Integrate with the eval grid as a new strategy (TODOs 8–10).
7. Update the resume prompt's matrix and close (TODOs 11–12).
