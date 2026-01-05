# Cortex Roadmap

**Last Updated:** January 2026
**Current Status:** Semantic Search Working, ABR Optimization
**North Star:** ABR ≥ 0.9

---

## Current Eval Results

| Metric | Current | Target |
|--------|---------|--------|
| Semantic Lift | +35% | >0% |
| Win Rate | 44% (8/18) | >50% |
| ABR | 0.77 | ≥0.9 |

Latest run (qwen2:0.5b): Cortex wins 8/18, baseline wins 2/18, ties 8/18.

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

## Roadmap

### Phase 1: Consolidate Eval System ✅ DONE

**Goal:** Reduce complexity, single source of truth

- [x] Merge 23 eval files → 5 files (commit befe426)
- [x] Remove all mocks, use CLICortex everywhere
- [x] SQLite-only persistence (drop JSONL)

**Achieved structure:**
```
internal/eval/v2/
├── eval.go       # 286 lines (runner + types)
├── scenario.go   # 104 lines (YAML loading)
├── persist.go    # 155 lines (SQLite only)
├── measure.go    # 218 lines (scoring)
└── report.go     # 163 lines (output formatting)

Total: 926 lines (down from ~11,000)
```

---

### Phase 2: Simplify Scenarios ✅ DONE

**Goal:** Adding an eval = adding a YAML file only

- [x] Single YAML format: `context` + `tests`
- [x] Remove legacy scenario types
- [x] Every scenario just establishes context and runs tests
- [x] Unified runner for all scenarios

**Active scenarios:** 7 in `test/evals/v2/`
- auth-patterns, db-patterns, error-handling
- go-logging, go-naming, go-testing, testing-patterns

**Format:**
```yaml
id: auth-middleware
context:
  - type: decision
    content: "We use JWT, not sessions"
tests:
  - id: auth-approach
    query: "How do we handle authentication?"
    expect:
      includes: ["JWT"]
      excludes: ["session"]
```

---

### Phase 3: ABR Dashboard 🔄 IN PROGRESS

**Goal:** Track ABR over time, make progress visible

- [x] SQLite persistence for eval results (`evals_v2.db`)
- [ ] CLI: `cortex eval --summary` shows ABR trend
- [ ] Historical comparison: "ABR improved from 0.77 to 0.85"
- [ ] Pass criteria enforcement: ABR ≥ 0.9

---

### Phase 4: ABR Optimization 📋 NEXT

**Goal:** Improve ABR from 0.77 → 0.9

Potential improvements:
- [ ] Better embedding model selection
- [ ] Retrieval tuning (top-k, similarity threshold)
- [ ] Context formatting for LLM consumption
- [ ] Larger/better LLM for eval (Claude Haiku vs qwen2:0.5b)

---

## Blocked Until ABR ≥ 0.9

| Feature | Reason Blocked |
|---------|----------------|
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

- [x] **Semantic search with embeddings** - nomic-embed-text, +35% lift
- [x] **Eval consolidation** - 23 files → 5 files (926 lines)
- [x] **Unified scenario format** - single YAML pattern
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

| Phase | Status | Key Metric |
|-------|--------|------------|
| Phase 1: Consolidate | ✅ Done | 926 lines (was 11k) |
| Phase 2: Scenarios | ✅ Done | 7 active scenarios |
| Phase 3: Dashboard | 🔄 In Progress | `--summary` flag |
| Phase 4: Optimize | 📋 Next | ABR ≥ 0.9 |

**Mandate complete when:** ABR ≥ 0.9 sustained over 3 consecutive runs.

---

*This roadmap is a living document. North star: ABR ≥ 0.9.*
