# Plan 04 — Integration Test (End-to-End Pass Rate)

## Context

After all 5 sessions complete, the workdir should contain a runnable HTTP server implementing all 25 endpoints across 5 resources. This plan implements the end-to-end check: build the binary, start it, hit every endpoint, verify the responses make sense.

This populates `LibraryServiceScore.EndToEndPassRate` (currently left at 0 by Plan 01).

**Depends on:** nothing structural. Can be built and validated against any working library-service implementation (e.g., a hand-written cohesive fixture) before any sessions have ever run.

## Scope

**In (MVP):**
- Build `cmd/server` from the workdir
- Start the binary on a random free port
- Exercise all 25 endpoints with realistic payloads
- Score: percentage of endpoints returning the expected status class
- Clean lifecycle: start, test, kill, cleanup

**Defer:**
- Performance/load testing
- Concurrent-request safety
- Schema migration validation
- Body-shape validation beyond "is parseable JSON" (the eval is about cohesion, not field-by-field correctness)

## Approach

Standard subprocess pattern: spawn the binary as a child, wait for the port to accept connections, run the HTTP probes, then SIGTERM the child. Use a unique tempdir per run for the SQLite DB so concurrent runs don't conflict.

Endpoint coverage is implemented as a table-driven test plan, not 25 hand-written tests. Each row says: method, path template, body template, expected status class, dependency (e.g., loans depends on books + members existing).

Order matters: create-then-list-then-get-then-update-then-delete, with cross-resource dependencies (loans needs an existing book + member) handled by an explicit ordering of resources.

## Files to create / modify

| Path | Purpose |
|---|---|
| `internal/eval/v2/library_service_e2e.go` | Integration test runner: build, start, probe, kill |
| `internal/eval/v2/library_service_e2e_test.go` | Unit tests against a hand-written fixture |
| `internal/eval/v2/library_service.go` | Wire `endToEndPassRate()` call in `Score()` |
| `test/evals/library-service/fixtures/cohesive/` | Reuse the cohesive fixture from Plan 01 — must include a working `cmd/server` |

## Implementation steps

1. **Define endpoint plan** as a slice of structs:

   ```go
   type endpointProbe struct {
       resource     string
       method       string
       pathTemplate string // e.g., "/books", "/books/{id}"
       bodyTemplate string // JSON template; {{.id}} substitutions allowed
       expectClass  int    // 200 / 201 / 204 / 4xx — class, not exact code
       captureID    bool   // for POST: capture returned id for later use
   }
   ```

   Build the plan in resource order: authors → members → branches → books (which need authors) → loans (which need books + members). Within each resource: POST → GET list → GET one → PUT → DELETE.

2. **Implement `endToEndPassRate(workdir string) (float64, error)`**:
   - `go build -o ${workdir}/bin/server ./cmd/server` from workdir
   - Pick a random free port (`net.Listen("tcp", ":0")`, get port, close)
   - Spawn binary with `PORT=<port>`. Capture stdout/stderr to a buffer for diagnostics.
   - Poll `GET http://localhost:<port>/books` (or any list) for up to 5 seconds with 100ms backoff until it responds.
   - Run the endpoint plan in order; for each probe:
     - Render path with captured ids
     - Render body with captured ids
     - Send request, check status class matches `expectClass`
     - If POST and `captureID`, parse response body, store the `id`
   - Send SIGTERM, wait up to 2 seconds, then SIGKILL if still alive
   - Return `passed / total`.

3. **Failure modes to handle**:
   - Build fails → return 0.0 with error wrapped as "build failed: ..."
   - Server doesn't start → 0.0 with stderr included in error
   - Server starts but specific endpoints 404/500 → score reflects partial pass; not a hard error
   - Schema mismatch (e.g., missing column) → manifests as 500s from create endpoints; score reflects it

4. **Test against fixtures**:
   - Cohesive fixture (from Plan 01): should score 1.0 (all 25 endpoints pass)
   - Empty seed: should score 0.0 (build succeeds but no routes registered)
   - Partially-implemented (e.g., only books done): should score 5/25 = 0.2

5. **Wire into `Score()`** — call from `(*LibraryServiceEvaluator).Score`, populate `EndToEndPassRate`. Error from this call should NOT fail the score; record as 0 and continue (other metrics remain valid).

6. **Concurrency**: the eval may run multiple conditions. Each condition's workdir has its own SQLite DB; binding to port 0 picks fresh ports. No global state should be touched.

## Verification

- Unit test: spin up the cohesive fixture's server, run the probe plan, verify 1.0.
- Unit test: spin up a fixture with only books implemented, verify the score is in the right range (5/25 = 0.2 if the plan covers all 5 books endpoints first).
- Unit test: run with a workdir that doesn't compile, verify the error message is informative.
- Run with `-race` to catch any goroutine-leak issues with the spawned subprocess.

## Definition of done

- [ ] `endToEndPassRate()` implemented with the documented contract
- [ ] Cohesive fixture scores 1.0
- [ ] Empty seed scores 0.0 cleanly (no panics)
- [ ] Subprocess is reliably reaped on test exit (no orphans)
- [ ] Wired into `Score()`; populates `LibraryServiceScore.EndToEndPassRate`
- [ ] Tests pass with `-race`

## Suggested next plan

None — this is independent. After this plan plus Plan 01 are done, the scorer is fully functional and Plans 02 + 03 can drive real comparison runs.
