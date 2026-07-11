# STATE.md — ralph/roadmap loop memory

## Current milestone
M3 — Agent slice 3a: design note

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
      shellrisk Risky→Blocked in subagents, excluded coder-only tools) (this commit)

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

## Known Issues
(none)

## Next Up
M3 is done (file exists, 3 DECISION lines, verify green — no code changed
so no new red/green cycle was needed). Start M4 (GOAL §3 slice 3b): register
an `agent` Subagent profile in `internal/tools/tools.go` next to `Study`,
implementing the three M3 decisions verbatim — toolset
`{outline,grep,read_file,write_file,edit_file,bash}`, `DepthCap: 1`,
mandatory `Bounds`, own system prompt, `Declaration` tool, registered via
`Register(Agent)` in the same `init()` as Study. Gate it behind
`tools.enable_agent` in `ToolConfig` (cmd/cortex/config.go) following
`docs/IMPLEMENTATION-PATTERN.md`'s `IsToolEnabled` pattern (default true —
missing config key must not disable a shipped tool, matching the existing
`EnableContext*` precedent). Wire the shellrisk Risky→Blocked-in-subagent
decision through the `ShellGate` path `bash` already uses (check whether
`cmd/cortex/study.go`'s `RunSubagent`/`dispatcherFor` already treats
subagent `bash` calls as headless for shellrisk purposes before adding new
plumbing — M2's depth-cap context threading may be the natural place to
also carry a headless/subagent flag). This is a multi-part increment; if it
doesn't fit one iteration, split it further in the checklist rather than
landing it half-wired. Acceptance per GOAL.md §3 slice 3b: profile-registry
+ seed-seam unit tests cover the new profile (registration, gating on/off,
allowlist excludes the M3 exclusions); `TestAgentToolEndToEnd` scripted-
Sender loop test drives a full `agent` call end to end; verify green;
ROADMAP.md item 6 marked *(landed)* in the same commit as the last M4 piece.
