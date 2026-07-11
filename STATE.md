# STATE.md — ralph/roadmap loop memory

## Current milestone
M5 — README pass

## Checklist
- [x] M0: GOAL.md + STATE.md committed on ralph/roadmap; verify green at baseline
- [x] M1: seed-builder field on `Subagent`; `runSubagent` calls the profile's seed func
- [x] M1: `TestProfileSeedSeam` (Study byte-identical + fake profile gets its own seed)
- [x] M2: `DepthCap` field on `Subagent` (Study explicit `DepthCap: 0`) (fcb3b39)
- [x] M2: depth threaded via context through `runSubagentStats` (the shared
      entry every subagent invocation funnels through — `RunSubagent` and
      study-eval both call it), checked before any model round-trip (fcb3b39)
- [x] M2: `TestSubagentDepthPolicy` in `cmd/cortex/study_test.go` — cap-0
      nested call refused with a clear error (direct dispatch, no HTTP
      needed since refusal is synchronous); cap-1 fixture allows one nested
      subagent via a real scripted-backend 4-round-trip recursion, refused
      at depth 2; Study's cap/toolset asserted unchanged (fcb3b39)
- [x] M3: `docs/agent-tool.md` written, 3 DECISION lines (toolset scope,
      shellrisk Risky→Blocked in subagents, excluded coder-only tools)
- [x] M4.1: `agent` `Subagent` profile registered in `internal/tools/tools.go`
      next to `Study` — `Tools: {outline,grep,read_file,write_file,edit_file,
      bash}` (decision 1), `DepthCap: 1` (decision 3), mandatory
      `Bounds.MaxTokens`, own `agentSystem` prompt (`internal/tools/study.go`),
      `AgentTool` declaration added to `tools.All` (coder sees it), registered
      via `Register(Agent)` in the shared `init()`. Config-gated via
      `tools.EnableAgent *bool` (`cmd/cortex/config.go`) threaded through
      `mergeTools` and `CortexSession.IsToolEnabled` (nil = enabled, same as
      `EnableWeb`/`EnableContext*`). Tests: `internal/tools/agent_test.go`
      (registration/DepthCap/Bounds, toolset allowlist matches decisions 1+3
      exactly, `tools.All` carries the declaration, `Execute` refuses when
      `IsToolEnabled("agent")` is false without calling `RunSubagent`);
      `cmd/cortex/context_tools_test.go` `TestIsToolEnabledAgentGate` (nil/
      false/no-config, plus `mergeTools` override) (this commit)
- [x] M4.2: wired the shellrisk Risky→Blocked-in-subagent decision (design
      doc decision 2). `gateShell` (`cmd/cortex/tool_deps.go:247`) gates the
      Risky branch's interactive-confirm path on `subagentDepth(ctx) == 0` in
      addition to the existing `cs.confirmRisky != nil && !cs.quiet` check —
      at depth ≥ 1 it now falls straight to the same "blocked (risk: ...). No
      interactive approval is available..." message `headlessDeps.GateShell`
      already returns for Risky, no new plumbing (context already carried
      `subagentDepth` end to end via `dispatcherFor` → `tools.Execute` →
      `bash` → `deps.GateShell`). Tests added to `TestBashShellSyntax`
      (`cmd/cortex/main_test.go`): "risky command blocked inside a subagent
      regardless of confirmRisky" drives `tools.Execute` with
      `withSubagentDepth(ctx, 1)` and a `confirmRisky` stub that `t.Fatal`s
      if invoked, asserting the headless-blocked message fires and the
      command never runs; "risky command at depth 0 still uses interactive
      confirm" is the control proving the coder's own top-level path is
      unchanged. Verified revert-fails: stashing just the `tool_deps.go`
      change makes the new subagent-depth subtest fail with exactly the
      `confirmRisky must not be invoked` assertion. (this commit)
- [x] M4.3: `TestAgentToolEndToEnd` (`cmd/cortex/agent_test.go`) — scripted-
      `Sender` loop test (pattern: `study_test.go`'s cap-1 recursion case)
      driving a real `cs.Turn()` that calls `agent`; the `agent` subagent's
      own loop dispatches a `bash` call before finalizing; the digest lands
      back on the coder's own tool result and its finalize reply carries it.
      Four scripted round-trips (coder ask, subagent ask, subagent finalize,
      coder finalize), asserted by count and by wire content at each step.
      The dispatched tool is a Risky `bash` command via a `classifyShell`
      stub, exercised along the real path (`coderDispatcher` →
      `tools.Execute` → `runSubagent` → `runSubagentStats` → `runLoop` →
      `dispatcherFor` → `bash` → `gateShell`), not a hand-built ctx — so a
      regression in `Agent.Tools` (e.g. `bash` dropped) fails this test too
      (verified by hand: temporarily removing `Bash` from `tools.Agent.Tools`
      fails the test's blocked-message assertion with the dispatcher's
      "not available here" refusal instead). `confirmRisky` is wired to
      `t.Fatal` if invoked, as a defense-in-depth check — session is `quiet`,
      which alone keeps Risky treated as Blocked regardless of depth (the
      pure depth-vs-quiet distinction for M4.2 is `TestBashShellSyntax`'s job
      in `main_test.go`; this test is integration coverage of the composed
      path, not a second unit test of the depth branch). ROADMAP.md item 6
      marked `*(landed 2026-07-11: ...)*` in this commit — M4 is complete.
      (this commit)

## How to run / verify (quickref — keep short)
./scripts/check.sh && go build ./... && go test ./...

## Decisions Log
- 2026-07-11: GOAL.md v1 finalized. Scope = roadmap items 6, 4-remainder
  (README), 8 (design doc only); web track excluded (parallel).
- 2026-07-11: `Subagent.Seed` is nil-safe in `runSubagent` — a profile that
  leaves `Seed` unset falls back to `StudySeed`, so pre-existing tests that
  register bare `Subagent{}` fixtures (no `Seed` field) keep working
  unchanged. Only `Study` sets `Seed: StudySeed` explicitly; a future
  profile with a distinct seed shape sets its own.
- 2026-07-11: Depth cap enforcement lives in `CortexSession.runSubagentStats`
  (cmd/cortex/study.go), not in `internal/tools`'s `runSubagent`, because the
  check needs the recursion depth threaded via `context.Context` across
  successive `deps.RunSubagent` calls — and `runSubagentStats` is the one
  function both `RunSubagent` (live callers) and the study-eval driver share,
  so every invocation (top-level or nested) is covered by one check. A call
  at `depth > sa.DepthCap` is refused before any model round-trip (no wasted
  network call). `Subagent.DepthCap` is per-profile (0 for Study, matching
  its prior de-facto behavior); the acceptance test proves the cap-0 refusal
  via direct dispatch (no HTTP needed — refusal is synchronous) and proves
  cap-1's one-level-then-refuse shape via a real scripted-HTTP-backend
  4-round-trip recursion (root ask, child ask, child finalize after its own
  nested attempt is refused, root finalize).
- 2026-07-11: `docs/agent-tool.md` (M3) settled: (1) `agent`'s toolset is
  `outline`/`grep`/`read_file`/`write_file`/`edit_file`/`bash` — read/search
  plus write/edit/bash, not read-only-plus-write, since the profile exists
  to do bounded implementation work (not just report on it) and nesting is
  already bounded by DepthCap (M2), not by withholding bash; (2) `shellrisk`
  Risky inside any subagent is Blocked (no interactive operator on that
  seam — same policy headless `cortex turn` already applies) — confirms the
  ROADMAP.md default, doc doesn't argue otherwise; (3) `agent` excludes
  `recall` + all four memory tools + all three context tools (session-scoped
  state a fresh-seeded subagent never has access to, same as Study today),
  plus nesting past DepthCap 1 is bounded by the cap mechanism, not by
  toolset omission alone. M4 (agent profile registration) must implement
  these three exactly; if M4 finds a decision wrong, the fix is a doc edit
  with a dated Decisions Log entry (GOAL.md §2), not a silent deviation.
- 2026-07-11: M4 (§3 slice 3b) split into three increments in the checklist
  (M4.1/M4.2/M4.3) rather than landed as one commit — GOAL.md §7 step 4's
  "an increment too big for one iteration gets split IN the checklist" call.
  M4.1 (this commit) lands the registered profile + config gate + its own
  unit tests, matching decisions 1 and 3 verbatim (toolset, exclusions,
  DepthCap 1). It deliberately does NOT touch `cmd/cortex/tool_deps.go`'s
  `gateShell` — decision 2 (Risky→Blocked inside a subagent) is not wired yet;
  today a Risky `bash` call from inside a running `agent` instance still hits
  the coder's own interactive-confirm path unchanged, because
  `dispatcherFor` hands the same `*CortexSession` through as `ToolDeps` with
  no depth-aware branch in `gateShell`. `TestAgentToolEndToEnd` is deferred to
  M4.3 for the same reason: an end-to-end test that exercises `bash` inside
  `agent` before M4.2 lands would either dodge the Risky path entirely (weak
  coverage) or need to assert the WRONG (not-yet-fixed) interactive-confirm
  behavior — better to land it once M4.2 makes the correct behavior real,
  then write one test that proves both the plumbing and the end-to-end shape
  at once. Bounds picked for `Agent` (`MaxTokens: 8_192, MaxIter: 20,
  ReadBudgetBytes: 128_000`) are task-fit like Study's, sized up from Study's
  `MaxIter: 12` since edit+verify needs more rounds than read-only research;
  not benchmarked, revisit if `TestAgentToolEndToEnd` (M4.3) or a later live
  probe shows it's wrong in either direction.
- 2026-07-11: M4.2 lands decision 2 as a one-line gate: `gateShell`'s existing
  Risky fallback branch already had the exact right text (byte-identical to
  `headlessDeps.GateShell`'s Risky message, both built from the same
  `shellrisk.Level`/`v.Reason` shape) — so the fix is adding
  `subagentDepth(ctx) == 0 &&` to the interactive-confirm branch's condition,
  not writing new blocked-message code. No new plumbing:
  `subagentDepth`/`withSubagentDepth` (`study.go`) and the ctx threading
  through `dispatcherFor` → `tools.Execute` → `bash` → `deps.GateShell`
  already existed from M2's depth-cap work.
- 2026-07-11: M4.3 closes M4 — the whole `agent` tool subsystem (GOAL.md §3,
  ROADMAP.md item 6) is landed. `TestAgentToolEndToEnd` deliberately does NOT
  re-prove the M4.2 depth-vs-quiet distinction in isolation (a `quiet`
  session already forces Risky→Blocked regardless of depth, so that specific
  branch's uniqueness is `TestBashShellSyntax`'s job) — its value is proving
  the full composed path (`agent` dispatch → subagent `runLoop` → `bash` →
  `gateShell`) actually wires together end to end, which it caught failing
  (dispatcher refusal, not the risk gate) when `bash` was hand-removed from
  `Agent.Tools` as a manual revert check.

## Known Issues
(none)

## Next Up
Start M5 (README pass, GOAL.md §4) — first enumerate its acceptance bullets
as unchecked checklist increments (GOAL.md §7 step 4: "at milestone start,
first enumerate its DoD items as unchecked increments"), then pick the first
one. GOAL.md §4's acceptance is all grep-on-README.md: (a) documents every
command — `cortex`, `resume`, `turn`, `study`, `change`, `discord`,
`study-eval`; (b) mentions the memory tools (`memory_write` or "memory
tools"), `study`, `recall`, the context tools (`context_evict` or "context
tools"), and `web_search`/`fetch_url`; (c) mentions the `agent` tool (M4
landed, so this applies now); (d) zero references to removed surfaces —
`project_index`, `/remember`, `/forget`, `cortex daemon`, `pkg/cognition`.
Read README.md fully against CLAUDE.md (the reference for current product
surface) before editing — likely several of (a)/(b) already hold and only a
subset needs adding, plus (c) the `agent` tool mention and scrubbing any (d)
leftovers. Since acceptance is pure grep with no build/test surface, "extend
the harness so reverting would fail a check" for M5 likely means adding a
lightweight `TestReadmeSurface` (or similar) in `cmd/cortex` that runs those
same greps against README.md — GOAL.md doesn't mandate a test file
specifically for M5 (unlike M1/M2/M4's explicit `Test...` names), but §2's
"never weaken or delete a check" and the pillar "tests are the spec" argue
for making the grep acceptance an actual `go test` check rather than a
one-time manual pass that could regress silently on a later README edit.
