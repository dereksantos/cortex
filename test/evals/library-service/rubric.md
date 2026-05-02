# Library Service Eval — Scoring Rubric

All measurements are computed against the final state of the repo after all sessions complete.

## 1. Shape similarity (headline)

For each pair of handler files (books, authors, loans, members, branches), extract a feature vector via `go/ast`:

- Handler function names (normalized: drop the resource name)
- Function signature shapes (params + returns)
- Error-handling call sites (e.g., `fmt.Errorf` vs `errors.Wrap` vs custom)
- Response writer pattern (`json.NewEncoder` vs `w.Write` vs custom helper)
- Validation pattern (inline vs helper vs library)
- Logging pattern (which logger, which call style)

Score each pair as cosine similarity over the feature vector. Aggregate as the mean over all 10 pairs.

**Range:** 0.0 (every handler different) to 1.0 (every handler structurally identical modulo resource names).

## 2. Naming convention adherence

Extract from S1's handler:
- Handler function naming pattern (e.g., `HandleListBooks`, `ListBooks`, `BooksList`)
- Error variable naming (e.g., `ErrNotFound`, `errNotFound`, `NotFoundErr`)
- Test naming (e.g., `TestListBooks`, `Test_ListBooks_Returns200`)
- Table/file naming

For S2–S5: % of identifiers matching S1's pattern. Aggregate as the mean.

**Range:** 0% to 100%.

## 3. Smell density

Per file via `go/ast`:
- Cyclomatic complexity per function (target < 10)
- Function length (target < 50 lines)
- Nesting depth (target < 4)
- Magic-number count
- Duplicate-block count (best-effort)

Score: weighted sum of normalized smells per 100 LOC. Lower is better.

## 4. Test parity

For each resource, a test file exists with:
- Same setup pattern (e.g., shared `setupTest` helper or repeated init)
- Same assertion style (testify? stdlib? table-driven?)
- Same coverage of CRUD operations

Score: % of resources passing parity check vs S1.

## 5. End-to-end works

- `go build ./...` exit 0
- `go test ./...` exit 0
- Integration test: start the server, hit all 25 endpoints, verify status codes and basic body shape.

Score: pass / fail / partial (X of 25 endpoints).

## 6. Refactor delta (optional)

Use a frontier model to rewrite the codebase for maximum cohesion (single pass, full repo as input). Diff against actual output.

Score: lines changed / total lines. Lower = less drift.

## Aggregation

The headline pass criterion is **shape similarity**. The other metrics are diagnostic — they explain *why* shape similarity moved or didn't.

For a Cortex run to be considered a "win" vs baseline:
- Shape similarity lifted by ≥ 0.2 (e.g., 0.55 → 0.85)
- AND naming adherence lifted by ≥ 20pp
- AND end-to-end pass rate not regressed

If end-to-end regresses, Cortex is hurting more than helping (probably injecting bad context).
