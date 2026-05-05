# Fresh-session prompt — Plan 04 (integration test)

Copy the block below verbatim as the opening message of a fresh Claude Code session.

---

You are implementing Plan 04 (integration test / end-to-end pass rate) for the
library-service multi-session eval in this repo.

## Read these first, in order

1. `test/evals/library-service/plans/04-integration-test.md`   — the plan
2. `test/evals/library-service/SPEC.md`                         — eval design
3. `test/evals/library-service/rubric.md`                       — scoring rubric
4. `test/evals/library-service/system-spec.md`                  — what the
                                                                  library service is
5. `internal/eval/v2/library_service.go`                        — `Score()` is
                                                                  wired; you'll plug
                                                                  `EndToEndPassRate` in

## What's already done — DO NOT redo

- Plan 01 (scorer) shipped at commit `1a55ca1`. `ShapeSimilarity`,
  `NamingAdherence`, `SmellDensity`, `TestParity` all implemented and calibrated
  against three fixtures in
  `test/evals/library-service/fixtures/{cohesive,near_cohesive,diverged}/`.
- `LibraryServiceScore.EndToEndPassRate` is the field you populate.
  `RefactorDeltaPct` stays at -1 (optional rubric metric, deferred).
- Plans 02 (runner) and 03 (cortex injection) are NOT done; do not touch them.

## Critical gotcha — fixture currently isn't runnable

`test/evals/library-service/fixtures/cohesive/` contains 10 `.go` files all
marked `//go:build ignore` so the main module's `go test ./...` doesn't
parse-error on them. They are NOT a Go package and there is no `cmd/server`
or `go.mod`.

Plan 04 needs this fixture to build into a working server so you can run
endpoint probes against it. Recommended fix:

  1. Add `fixtures/cohesive/go.mod` declaring a separate module
     (e.g. `module github.com/example/library-service-cohesive`).
  2. Drop the `//go:build ignore` tags from the runtime files (keep them
     on test files if needed; or just leave the test files as-is —
     they're for the scorer, not the server).
  3. Add `fixtures/cohesive/cmd/server/main.go` that wires the existing
     handlers and starts an HTTP server reading `PORT` from env.

Verify the scorer still works after — it parses files directly via
`go/parser` and doesn't care about modules or build tags. Run:

    go test ./internal/eval/v2/... -run Calibration

If those tests still pass with the same numbers (shape 1.000 / 0.907 / 0.054),
you didn't break Plan 01.

## Definition of done

Plan 04 spells these out, but the headline:

  - [ ] `endToEndPassRate(workdir string) (float64, error)` implemented
  - [ ] Cohesive fixture scores 1.0 (all 25 endpoints return expected status class)
  - [ ] Empty seed (`test/evals/projects/library-service-seed/`) scores 0.0 cleanly
        — no panics, just zero
  - [ ] Subprocess is reliably reaped (no orphans after tests exit)
  - [ ] Wired into `(*LibraryServiceEvaluator).Score` so `EndToEndPassRate`
        gets populated alongside the other metrics
  - [ ] Tests pass with `go test -race ./internal/eval/v2/...`

## Scope discipline

- Don't modify `rubric.md` without flagging it back to the user first.
- Don't touch the existing scorer files (`score_*.go`) except to add a
  call into your new `endToEndPassRate` from `library_service.go`'s `Score()`.
- Don't add any new top-level dependencies — `net/http` and `os/exec` from
  stdlib should be enough. `modernc.org/sqlite` is already used for the
  fixture's persistence; that's fine.
- Standard library testing only. No testify or external assertion libs.
  This is a hard project constraint (see `CLAUDE.md`).

## Suggested first commands

    cd /Users/dereksantos/eng/projects/cortex
    cat test/evals/library-service/plans/04-integration-test.md
    go test ./internal/eval/v2/...   # baseline: should be green
    ls test/evals/library-service/fixtures/cohesive/

When done, run the full eval test suite:

    go test -race ./internal/eval/v2/...

Then propose a commit message and stop. The user will review before committing.
