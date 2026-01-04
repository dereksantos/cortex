# Cortex Roadmap

**Last Updated:** January 2026
**Current Status:** Eval Consolidation
**North Star:** ABR ≥ 0.9

---

## Current Eval Results

| Metric | Current | Target |
|--------|---------|--------|
| Cognition | 90% (19/21) | >90% |
| E2E | 70% | >90% |
| ABR | 0.77 | ≥0.9 |

---

## Core Metric: ABR (Agentic Benefit Ratio)

ABR measures how well background processing (Think) makes Fast mode perform like Full mode:

```
ABR = quality(Fast + Think) / quality(Full)
Goal: ABR → 1.0 as session progresses
```

| ABR | Meaning |
|-----|---------|
| ≥ 0.95 | Excellent |
| 0.85-0.95 | Good |
| 0.70-0.85 | Needs work |
| < 0.70 | Failing |

**Current: 0.77 (Needs work)**

ABR is the single metric that validates Cortex's core innovation: bounded intelligence through background processing.

---

## Roadmap: Eval Consolidation

### Phase 1: Consolidate Eval System

**Goal:** Reduce complexity, single source of truth

- [ ] Merge 23 eval files → 5 files
- [ ] Remove all mocks, use CLICortex everywhere
- [ ] SQLite-only persistence (drop JSONL)

**Files to delete:**
- `e2e_eval.go`, `e2e_journey.go`
- `cognition.go`, `cognition_eval.go`
- `mock_cortex.go`
- `tree.go`, `stats.go`
- `persist_jsonl.go`

**Target structure:**
```
internal/eval/
├── eval.go       # ~400 lines (runner + types)
├── scenario.go   # ~150 lines (YAML loading)
├── persist.go    # ~100 lines (SQLite only)
├── measure.go    # ~100 lines (ABR calculation)
└── report.go     # ~100 lines (output formatting)

Total: ~850 lines (down from ~11,000)
```

---

### Phase 2: Simplify Scenarios

**Goal:** Adding an eval = adding a YAML file only

- [ ] Single YAML format: `context` + `tests`
- [ ] Remove 7 scenario types (mode, session, benefit, pipeline, dream, conflict, idiom)
- [ ] Every scenario just establishes context and runs tests
- [ ] Unified runner for all scenarios

**New format:**
```yaml
id: auth-middleware
context:
  - event: correction
    content: "We use JWT, not sessions"
  - event: decision
    content: "Auth middleware goes in pkg/middleware"
tests:
  - query: "How do we handle authentication?"
    expect_contains: ["JWT", "middleware"]
    expect_abr: ">= 0.9"
```

---

### Phase 3: ABR Dashboard

**Goal:** Track ABR over time, make progress visible

- [ ] Single table: `eval_results(id, scenario_id, abr, latency_ms, timestamp)`
- [ ] CLI: `cortex eval --summary` shows ABR trend
- [ ] Pass criteria: ABR ≥ 0.9
- [ ] Historical comparison: "ABR improved from 0.77 to 0.85"

---

## Blocked Until Evals Pass

The following work is **blocked** until ABR ≥ 0.9:

| Feature | Reason Blocked |
|---------|----------------|
| Embeddings implementation | Can't validate without ABR baseline |
| Git Dream source | Core evals must pass first |
| Entity relationships | Polish feature, not priority |
| Cross-session learning | Need single-session working first |
| New cognitive modes | No expansion until stability |

---

## Design Documents

- `docs/prompts/cli-focus.md` - CLICortex design and rationale
- `docs/prompts/eval-abr-focus.md` - ABR-focused eval philosophy

---

## Recently Completed

- [x] Cognition evals: 44% → 90% (19/21 passing)
- [x] E2E evals: 50% → 70%
- [x] CLICortex for true E2E testing via CLI commands
- [x] Idiom extraction and evals
- [x] Nuance extraction through pipeline
- [x] Core cognitive architecture (Reflex, Reflect, Resolve, Think, Dream)
- [x] Activity-based budget models
- [x] ABR metric baseline (0.77)
- [x] Claude Code integration

---

## Success Criteria

| Phase | ABR | Cognition | E2E |
|-------|-----|-----------|-----|
| Current | 0.77 | 90% | 70% |
| Phase 1 | 0.80 | 90% | 80% |
| Phase 2 | 0.85 | 95% | 85% |
| Phase 3 | **≥0.90** | **95%** | **90%** |

**Mandate complete when:** ABR ≥ 0.9 sustained over 3 consecutive runs.

---

*This roadmap is a living document. North star: ABR ≥ 0.9.*
