# Library Service Multi-Session Eval

Tests Cortex's ability to keep a small local model **cohesive across sessions**: does S1's chosen "house style" propagate to S2–S5 when the model has no chat memory between sessions?

## Thesis being tested

> A small model writing CRUD handlers across 5 fresh sessions will diverge in shape (error wrapping, response envelope, naming, test layout) without help. Cortex's tier-2 mining (extract S1's patterns) + tier-3 detection (smell-detect during writes) closes the divergence gap measurably.

## System under construction

A small Go HTTP service for a fictional library. Five resources, each with full CRUD:

| Resource | Endpoints |
|---|---|
| books    | GET /books, GET /books/{id}, POST /books, PUT /books/{id}, DELETE /books/{id} |
| authors  | (same shape) |
| loans    | (same shape) — touches books + members |
| members  | (same shape) |
| branches | (same shape) |

Persistence: SQLite via `database/sql` + `modernc.org/sqlite` (no CGO).

Total target: ~25 endpoints, ~1500–2500 LOC, including tests.

The system spec — what the service does — is in `system-spec.md` and is shared verbatim across all sessions.

## Session structure

Each session is a **fresh model invocation** with **no chat history** of prior sessions. The repo on disk *is* the only inheritance.

| Session | Prompt | Adds |
|---|---|---|
| S1 | `sessions/01-scaffold-and-books.md` | Project scaffold + Books resource. Establishes conventions. |
| S2 | `sessions/02-authors.md` | Authors resource |
| S3 | `sessions/03-loans.md` | Loans (cross-resource — references books + members) |
| S4 | `sessions/04-members.md` | Members |
| S5 | `sessions/05-branches.md` | Branches |

Sessions run in order. Each session can read everything in the repo from prior sessions.

## Conditions

| Condition | Description |
|---|---|
| **A. Baseline** | Small model (default: `qwen2.5-coder:1.5b`), no Cortex. Repo is the only context. |
| **B. Cortex** | Same model + Cortex full pipeline: tier-1 principles seed, tier-2 mining of S1+, tier-3 AST detection on `Edit`/`Write`. |
| **C. Frontier ceiling** *(optional)* | Claude Sonnet, no Cortex. Upper bound. |
| **D. Frontier + Cortex** *(optional)* | Sanity check: does Cortex still help frontier or wash out? |

## Measurements

All scored programmatically against the final state of the repo after S5.

1. **Shape similarity (headline metric)** — pairwise cosine similarity of AST features across the 5 handler files. Features include: function signatures, error-wrap call patterns, response struct shape, validation idiom, logging idiom. Higher = more cohesive.

2. **Naming convention adherence** — % of identifiers in S2–S5 (functions, error vars, table names, test names) matching S1's conventions. Extracted via `go/ast`.

3. **Smell density** — `go/ast` driven: cyclomatic complexity per function, function length, nesting depth, magic-number count. Lower = better. Measured per-file and aggregated.

4. **Test parity** — every handler has tests; same setup/teardown shape; same assertion idiom. Binary-ish: percentage of handlers passing parity check.

5. **End-to-end works** — `go build ./...` clean, `go test ./...` green, integration test against the running server hits all 25 endpoints with 200/4xx as appropriate.

6. **Refactor delta (optional, judge-driven)** — diff size between actual output and a "cohesive ideal" (frontier-LLM-rewritten version). Smaller = less drift. Costs more to run; reserve for headline runs.

## Pass criteria (initial targets)

| Metric | Baseline expected | Cortex target | Frontier ceiling |
|---|---|---|---|
| Shape similarity | < 0.6 | ≥ 0.85 | ≥ 0.9 |
| Naming adherence | < 60% | ≥ 85% | ≥ 90% |
| Smell density (per 100 LOC) | high | < 0.5× baseline | ≤ Cortex |
| Test parity | < 60% | ≥ 90% | ≥ 90% |
| End-to-end | passes ~70% | passes 100% | passes 100% |

These are starting hypotheses; calibrate after first runs.

## Why these targets

The eval is "good" if **Cortex moves the headline metric (shape similarity) most of the way from baseline toward the frontier ceiling**. If Cortex doesn't move it, the thesis is wrong, and that's the result we want to know cheaply.

## Files in this eval

```
test/evals/library-service/
  SPEC.md                          # this file
  system-spec.md                   # service description, shared across sessions
  rubric.md                        # detailed scoring rubric
  sessions/
    01-scaffold-and-books.md
    02-authors.md
    03-loans.md
    04-members.md
    05-branches.md

test/evals/projects/library-service-seed/
  go.mod                           # blank canvas: module + nothing
  README.md                        # points to system-spec
  cmd/server/main.go               # minimal, just compiles

internal/eval/v2/library_service.go
                                   # harness: drives sessions, scores output
```

## Status

Scaffolded. Harness implementation is stub-only — runner, scorer, and reporter are all TODO.
