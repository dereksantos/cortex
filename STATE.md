# STATE.md — ralph/roadmap loop memory

## Current milestone
M1 — Agent slice 1: per-profile seeding

## Checklist
- [x] M0: GOAL.md + STATE.md committed on ralph/roadmap; verify green at baseline
- [x] M1: seed-builder field on `Subagent`; `runSubagent` calls the profile's seed func
- [x] M1: `TestProfileSeedSeam` (Study byte-identical + fake profile gets its own seed)

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

## Known Issues
(none)

## Next Up
M1 is done (all DoD items checked); verify is green. Start M2 (GOAL §3
slice 2): replace the blanket no-recursion rule with a per-profile depth
cap threaded through `RunSubagent` (`cmd/cortex/study.go`, ~line 42).
Study keeps cap 0. Land `TestSubagentDepthPolicy` (cap-0 subagent-tool
call refused; a fake cap-1 profile can dispatch one nested subagent whose
own subagent call is refused at depth 2) with a scripted `Sender`, per
existing loop test patterns.
