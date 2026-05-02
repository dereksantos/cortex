# Plan 01 — Scorer

## Context

The library-service eval needs to programmatically score a completed library-service repo against the rubric in `../rubric.md`. This is the most independent piece of the eval — it can be built and validated *before* any session runner exists, by scoring hand-crafted reference repos.

The scorer fills in `(*LibraryServiceEvaluator).Score` in `internal/eval/v2/library_service.go`, returning a `LibraryServiceScore`.

This is the highest-priority plan because:
- Builds without dependency on the runner or model harness
- Validates the eval design itself — if the scorer can't tell a cohesive repo from a diverged one, the eval is broken regardless of Cortex
- Once it works, the rest of the eval has a target

## Scope

**In (MVP):**
- Shape similarity (headline metric)
- Naming convention adherence
- Smell density
- Test parity

**Defer:**
- End-to-end pass rate → covered by Plan 04 (integration test). The score field will exist; just leave it 0 with a TODO to wire Plan 04's result in.
- Refactor delta → optional in rubric; requires a frontier model + diff. Implement only if early runs show shape similarity isn't discriminative enough.

## Approach

One file per metric, all in `internal/eval/v2/`. Keeps each metric small enough to test in isolation.

For each metric: define a stable, language-aware feature extraction (via `go/ast` and `go/parser`), then compute the metric over the extracted features. No regex over source; always go through the AST.

For shape similarity specifically: extract a feature vector per handler file, compute pairwise cosine, average. Feature vector design is the most consequential decision — keep it documented inline.

## Files to create / modify

| Path | Purpose |
|---|---|
| `internal/eval/v2/library_service.go` | Wire `Score()` to call the new helpers; remove the `not implemented` |
| `internal/eval/v2/score_shape.go` | `shapeSimilarity(handlerFiles []string) (float64, error)` |
| `internal/eval/v2/score_shape_test.go` | Table-driven tests using small literal Go files |
| `internal/eval/v2/score_naming.go` | `namingAdherence(s1File string, otherFiles []string) (float64, error)` |
| `internal/eval/v2/score_naming_test.go` | |
| `internal/eval/v2/score_smell.go` | `smellDensity(files []string) (float64, error)` |
| `internal/eval/v2/score_smell_test.go` | |
| `internal/eval/v2/score_test_parity.go` | `testParity(s1TestFile string, otherTestFiles []string) (float64, error)` |
| `internal/eval/v2/score_test_parity_test.go` | |
| `test/evals/library-service/fixtures/cohesive/` | Hand-written cohesive 5-resource library-service for validation |
| `test/evals/library-service/fixtures/diverged/` | Same 5 resources but deliberately divergent (different naming, error wrapping, response shape per file) |

## Implementation steps

1. **Define the handler-feature vector contract.** Decide what's in it. Suggested starting set:
   - Set of function names, normalized by stripping the resource token (e.g., `ListBooks` → `List_X`)
   - Multiset of function-signature shapes (param types + return types)
   - Multiset of "error-handling call-sites" (qualified call name, e.g., `fmt.Errorf`, `errors.Wrap`)
   - Multiset of "response-write call-sites" (e.g., `json.NewEncoder`, `w.Write`, helper-fn call)
   - Set of HTTP status code constants used (`http.StatusOK`, etc.)
   - Validation idiom marker (presence of `if X == ""`, presence of validator package import, etc.)

   Encode as a sparse `map[string]int`. Cosine over the sparse vector = sum of products / product of norms.

2. **Write `shapeSimilarity()`** — parse files via `go/parser`, walk AST building the feature map per file, compute cosine for all `C(5,2) = 10` pairs, return mean.

3. **Build the cohesive and diverged fixture repos.** Hand-write both with all 5 resources. The cohesive version uses identical patterns; the diverged version uses 5 different error-wrapping styles, 5 different response shapes, etc. ~500 LOC each.

4. **Validate**: cohesive should score > 0.85; diverged should score < 0.5. If both score similarly, the feature vector is wrong — iterate before continuing.

5. **Implement `namingAdherence()`** — extract identifier patterns from S1's file (function-name shape, error-var-name shape, test-name shape) using `go/ast`; for each later file, check the percentage of its identifiers that match.

6. **Implement `smellDensity()`** — walk all `.go` files, compute per-function: cyclomatic complexity (1 + count of `if`/`for`/`case`/`&&`/`||`), function length in lines, max nesting depth, count of integer/string literals not in `const` blocks. Aggregate as weighted sum normalized per 100 LOC.

7. **Implement `testParity()`** — for each resource's test file, extract: presence of shared setup helper, assertion idiom (does it use `t.Errorf` vs an assert lib import), table-driven vs sequential structure. Compute % matching S1's test file shape.

8. **Wire `(*LibraryServiceEvaluator).Score`** to call all four, populate `LibraryServiceScore`. Leave `EndToEndPassRate` and `RefactorDeltaPct` at 0 / -1 with TODO referencing Plan 04.

## Verification

- Each metric has a table-driven unit test using small literal Go files (the test fixtures don't need to compile; just be parseable by `go/parser`).
- The cohesive fixture scores meaningfully higher than the diverged fixture on shape similarity, naming, and test parity.
- Smell density is non-zero on the diverged fixture (which should have intentional smells: 70-line functions, deep nesting) and lower on the cohesive one.
- `go test ./internal/eval/v2/...` green.

## Definition of done

- [ ] All four MVP metrics implemented with unit tests
- [ ] Cohesive fixture scores ≥ 0.85 shape similarity, ≥ 85% naming adherence
- [ ] Diverged fixture scores ≤ 0.5 shape similarity, ≤ 60% naming adherence
- [ ] `Score()` returns a populated `LibraryServiceScore`, no `not implemented` error
- [ ] Documentation in `library_service.go` updated to reflect what's implemented vs deferred

## Suggested next plan

Plan 04 (integration test) — also independent, also validatable against the cohesive fixture. Can be done in parallel with this plan by a different session.
