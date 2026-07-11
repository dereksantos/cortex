# STATE.md — ralph/roadmap loop memory

## Current milestone
M4 — Agent slice 3b: the `agent` profile

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
- [ ] M4.3: `TestAgentToolEndToEnd` — scripted-`Sender` loop test (pattern:
      `cmd/cortex/study_test.go`'s cap-1 case) driving a full coder turn that
      calls `agent`, whose own loop dispatches ≥1 tool (e.g. `edit_file` or
      `bash`) before finalizing, and the digest lands back in the coder's
      turn. Mark ROADMAP.md item 6 *(landed)* in this commit (last M4 piece).

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

## Known Issues
(none)

## Next Up
Start M4.3: `TestAgentToolEndToEnd`, a scripted-`Sender` loop test (pattern:
`cmd/cortex/study_test.go`'s cap-1 recursion case in `TestSubagentDepthPolicy`)
driving a full coder turn that calls the `agent` tool, whose own loop
dispatches at least one tool (e.g. `edit_file` or `bash` — now that M4.2 is
in, a Risky `bash` call inside this test's `agent` instance should assert the
headless-blocked shape, not an interactive prompt, giving the test double
duty) before finalizing, with the digest landing back in the coder's turn.
Mark ROADMAP.md item 6 *(landed)* in this same commit — GOAL.md §3 slice 3b's
acceptance bullets are one unit, so M4 is not complete (and M5's README pass,
which depends on M4 having landed per GOAL.md §4, cannot start) until this
lands. Look for the roadmap file (`ROADMAP.md` at repo root, referenced
throughout GOAL.md) to find item 6's exact current wording before editing it.
