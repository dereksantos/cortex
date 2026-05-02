## Scorer fixtures

Hand-crafted reference repos used to validate the library-service scorer (see
`internal/eval/v2/score_*.go`). Three points on the cohesion axis:

| Fixture         | Purpose                                                       |
|-----------------|---------------------------------------------------------------|
| `cohesive/`     | Upper anchor — every resource follows the same template       |
| `near_cohesive/`| Calibration probe — one realistic drift per non-S1 resource   |
| `diverged/`     | Lower anchor — every resource picks a different idiom         |

The scorer should produce `cohesive ≥ near_cohesive ≥ diverged` on shape,
naming, and test parity, and `cohesive ≤ near_cohesive ≤ diverged` on smell
density. Hard thresholds for the upper/lower anchors live in
`internal/eval/v2/library_service_score_test.go`; soft envelopes for
`near_cohesive/` live in `library_service_calibration_test.go`.

Files carry `//go:build ignore` so the Go toolchain skips them — they exist
only to be parsed via `go/parser` by the scorer's tests. Edit them deliberately:
loosening the cohesive set or hardening the diverged set will move the
validation thresholds.

### cohesive/

Five resources (books, authors, loans, members, branches), every handler file
written from the same template. Same function names (modulo resource), same
signatures, same `fmt.Errorf` error wrapping, same `json.NewEncoder(...).Encode`
response, same status code set, same `if id == ""` validation idiom, same
table-driven test layout using `t.Errorf`.

### near_cohesive/

Same five resources, books identical to S1. Each of the other four carries
exactly one realistic drift the kind a small model commonly produces:

| Resource | Drift                                                            |
|----------|------------------------------------------------------------------|
| authors  | drops `fmt.Errorf("...: %w", err)` for plain string concatenation |
| loans    | uses `json.Marshal` + `w.Write(body)` instead of `json.NewEncoder(w).Encode` |
| members  | validates with `len(id) == 0` instead of `id == ""`              |
| branches | tests are sequential instead of S1's table-driven layout         |

Used to verify the scorer has resolution in the band between perfect cohesion
and outright divergence — the band Cortex is supposed to operate in. See the
calibration test for the exact envelopes.

### diverged/

Same five resources, but each file deliberately picks a different idiom on
every axis: error wrapping (`fmt.Errorf`, `errors.Wrap`, custom error type,
panic), response writing (`json.NewEncoder`, `json.Marshal+w.Write`,
`http.Error`, custom helper), signature shape (free function vs method on a
struct receiver), and validation. Tests vary in setup helper presence,
table-driven vs sequential, and `t.Errorf` vs `t.Fatalf`.

The diverged handlers also carry intentional code smells (deep nesting, long
function bodies, magic numbers) so the smell-density metric has signal to
pick up.
