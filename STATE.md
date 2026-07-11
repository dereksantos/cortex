# STATE.md — ralph/roadmap loop memory

## Current milestone
M1 — Agent slice 1: per-profile seeding

## Checklist
- [x] M0: GOAL.md + STATE.md committed on ralph/roadmap; verify green at baseline
- [ ] M1: seed-builder field on `Subagent`; `runSubagent` calls the profile's seed func
- [ ] M1: `TestProfileSeedSeam` (Study byte-identical + fake profile gets its own seed)

## How to run / verify (quickref — keep short)
./scripts/check.sh && go build ./... && go test ./...

## Decisions Log
- 2026-07-11: GOAL.md v1 finalized. Scope = roadmap items 6, 4-remainder
  (README), 8 (design doc only); web track excluded (parallel).

## Known Issues
(none)

## Next Up
Add a `Seed func(goal, path, outline string) string` field to `Subagent`
in internal/tools, default Study's to StudySeed, call it from
runSubagent (~tools.go:569), and land TestProfileSeedSeam (GOAL §3
slice 1).
