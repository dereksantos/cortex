# Cortex Roadmap

**Last Updated:** December 2024
**Current Status:** ~75% Publishable Prototype
**Target:** Publication-ready v1.0

---

## Current Implementation Status

### Cognitive Modes

| Mode | Status | Notes |
|------|--------|-------|
| **Reflex** | ✅ Complete | Mechanical retrieval working; ~11ms real-world (target <20ms) ✓ |
| **Reflect** | ✅ Complete | LLM reranking, contradiction detection |
| **Resolve** | ✅ Complete | Inject/wait/queue/discard decisions |
| **Think** | ✅ Complete | Activity-inverse budget, session context learning |
| **Dream** | ⚠️ 60% | Sources partially implemented; git source missing |
| **Digest** | ✅ Complete | Post-Dream deduplication |

### Evaluation Framework

| Eval Type | Status | Scenarios |
|-----------|--------|-----------|
| **Mode** | ✅ Complete | 10 scenarios |
| **Session** | ✅ Complete | 3 scenarios |
| **Benefit (ABR)** | ✅ Complete | 3 scenarios |
| **Conflict** | ✅ Complete | 2 scenarios |
| **Pipeline** | ⚠️ Partial | Implemented, needs review |
| **Dream** | ❌ Missing | 0 scenarios |

### Integration

| Component | Status |
|-----------|--------|
| Claude Code Hooks | ✅ Complete |
| CLI Commands | ✅ Complete |
| Daemon | ✅ Complete |
| Activity Tracking | ✅ Complete |

---

## Core Innovation: The Cognition Pipeline

Cortex's key innovation is the **bounded intelligence model**: LLMs work with pre-computed data, not unbounded exploration.

> "The LLM must work with the data it is given to make resource consumption more predictable. Cortex is about background processing."

This inverts the typical LLM interaction pattern:

| Traditional | Cortex |
|-------------|--------|
| LLM decides what to fetch | Mechanical retrieval first |
| Unbounded exploration | Bounded budgets |
| Variable latency | Hard latency constraints (<20ms) |
| Resource use unpredictable | Resource use proportional to activity |

### The Three Pillars

| Pillar | What It Does | Why It Matters |
|--------|--------------|----------------|
| **Budgeting** | Activity-based resource allocation | Predictable compute costs |
| **Prompts** | Structured LLM contracts | Consistent output format |
| **Pre-computed Datasets** | Cached context from background processing | Fast retrieval without blocking |

### Pillar Roadmap

#### Budgeting System
- [x] Think budget (activity-inverse): `budget = MaxBudget × (1 - ActivityLevel)`
- [x] Dream budget (idle-growth): `budget = min(IdleTime × GrowthRate, MaxBudget)`
- [x] Activity tracking with sliding window
- [ ] Budget monitoring dashboard (`cortex watch` enhancement)
- [ ] Configurable budget profiles (aggressive, conservative, custom)
- [ ] Budget alerts/limits for runaway processing

#### Prompts
- [x] Reflect reranking prompt (JSON: ranking[], contradictions[])
- [x] Dream analysis prompt (JSON: content, category, importance, tags[])
- [x] Graceful degradation when LLM unavailable
- [ ] Prompt versioning and migration
- [ ] Prompt A/B testing framework
- [ ] Custom prompt injection points for domain-specific extraction

#### Pre-computed Datasets
- [x] SessionContext (TopicWeights, CachedReflect, ResolvedContradictions)
- [x] ProactiveQueue (Dream discoveries for opportunistic injection)
- [x] Session persistence across daemon restarts
- [ ] Embeddings layer (semantic similarity vs current term-based)
- [ ] Cross-session pattern learning
- [ ] Dataset export/import for team sharing

---

## Gap Analysis

### Critical Gaps (Blocking Publication)

| Gap | Impact | Resolution |
|-----|--------|------------|
| **Dream evals missing** | Cannot validate Dream mode claims in paper | Write 2-3 Dream scenarios |
| **Git source missing** | Dream incomplete per paper | Implement `sources/git.go` |
| **Embeddings mismatch** | Paper claims embeddings; code uses keywords | Decision needed (see below) |

### High Priority Gaps

| Gap | Impact | Resolution |
|-----|--------|------------|
| **Reflex latency ~11ms** | Within <20ms target | ✓ Target met |
| **Dream source metrics** | Can't measure Dream effectiveness | Add tracking |
| **No latency timeouts** | Reflect/Resolve could exceed budgets | Add context timeouts |

### Medium Priority Gaps

| Gap | Impact | Resolution |
|-----|--------|------------|
| **Entity relationships** | Feature incomplete | LLM-based extraction |
| **Dream insight embeddings** | Dream outputs not indexed | Embed and store |

---

## Key Decision: Embeddings Strategy

The paper describes embedding similarity, but implementation uses keyword/tag matching.

| Option | Effort | Recommendation |
|--------|--------|----------------|
| **A: Implement embeddings** | Large (2-3 weeks) | Full feature parity |
| **B: Update paper** | Small (1 day) | Honest about current approach |
| **C: Mark as future work** | Small (1 day) | Acknowledge gap, publish anyway |

**Current recommendation:** Option C — publish with "Future Work" note, add embeddings in v2.

---

## Phased Roadmap

### Phase 1: Critical Fixes (Week 1)

**Goal:** Unblock publishable prototype

- [ ] **Add Dream eval scenarios**
  - [ ] `dream_source_coverage.yaml` — Test all sources sampled
  - [ ] `dream_insight_quality.yaml` — Test insight extraction rate
  - Effort: 2 days

- [ ] **Implement Git source**
  - [ ] Create `internal/cognition/sources/git.go`
  - [ ] Sample recent commits, diffs, blame
  - [ ] Extract decision/pattern insights
  - Effort: 3 days

- [ ] **Add Dream source coverage metrics**
  - [ ] Track which sources sampled per Dream cycle
  - [ ] Report in eval results
  - Effort: 1 day

**Phase 1 Total:** ~6 days

---

### Phase 2: Polish (Week 2)

**Goal:** Meet latency guarantees, improve reliability

- [ ] **Optimize Reflex latency**
  - [ ] Profile current path
  - [ ] Optimize query order (FTS vs category lookup)
  - [x] Target: <20ms (currently ~11ms) ✓
  - Effort: 2-3 days

- [ ] **Add latency timeouts**
  - [ ] Reflect: 250ms timeout with fallback
  - [ ] Resolve: 150ms timeout with fallback
  - [ ] Graceful degradation to Fast path
  - Effort: 2 days

**Phase 2 Total:** ~5 days

---

### Phase 3: Feature Completeness (Weeks 3-4)

**Goal:** Full paper-implementation alignment

- [ ] **Embeddings decision**
  - [ ] If implementing: integrate sqlite-vec, add embedding model
  - [ ] If not: update paper to reflect keyword-based approach
  - Effort: 10-15 days (if implementing) or 1 day (if updating paper)

- [ ] **Entity relationship discovery**
  - [ ] Simple: concept co-occurrence graph
  - [ ] Or: LLM-based relationship extraction
  - Effort: 5 days

- [ ] **Dream insight embeddings**
  - [ ] Embed Dream outputs
  - [ ] Store in vector index
  - [ ] Enable semantic search over insights
  - Effort: 3 days

**Phase 3 Total:** 8-23 days (depending on embeddings decision)

---

### Phase 4: Literature-Grounded Evals (Weeks 5-6)

**Goal:** Eval scenarios from established software engineering sources

- [ ] **Clean Code evals**
  - [ ] Naming conventions
  - [ ] Function size/complexity
  - [ ] Formatting rules

- [ ] **Refactoring evals**
  - [ ] Code smell detection
  - [ ] Transformation suggestions

- [ ] **DDD evals**
  - [ ] Bounded context enforcement
  - [ ] Ubiquitous language consistency

- [ ] **Design Patterns evals**
  - [ ] Pattern recognition
  - [ ] Appropriate pattern suggestion

**Phase 4 Total:** ~10 days

---

## Timeline Summary

| Phase | Duration | Milestone |
|-------|----------|-----------|
| Phase 1: Critical | 1 week | 85% publishable |
| Phase 2: Polish | 1 week | 95% publishable |
| Phase 3: Complete | 2-3 weeks | 100% paper-aligned |
| Phase 4: Literature | 1-2 weeks | Quality-grounded evals |

**Minimum viable publication:** 2 weeks (Phases 1-2)
**Full feature parity:** 5-6 weeks (Phases 1-4)

---

## Success Metrics

### For Publication

- [ ] All cognitive modes implemented and tested
- [ ] Dream evals pass at >80%
- [x] Reflex latency <20ms (P95) — currently ~11ms ✓
- [ ] ABR average >0.75
- [ ] Overall eval pass rate >90%

### For v1.0 Release

- [ ] Embeddings implemented OR paper updated
- [ ] Entity relationships working
- [ ] Literature-grounded evals for 3+ books
- [ ] Real-project validation (not just synthetic corpus)

---

## Open Questions

1. **Embeddings:** Implement true vector similarity or stay with keyword matching?
2. **Entity relationships:** Simple co-occurrence or LLM-based extraction?
3. **Literature evals:** Which books to prioritize first?
4. **Real-world testing:** Which project to use for validation beyond Cortex itself?

---

## Recently Completed

- [x] Core cognitive architecture (Reflex, Reflect, Resolve, Think, Dream, Digest)
- [x] Event routing through cognition pipeline
- [x] Activity-based budget models
- [x] ABR metric and benefit evals
- [x] Session accumulation evals
- [x] Conflict detection evals
- [x] Claude Code integration
- [x] `--cognition` flag for eval command
- [x] Test isolation for evals (temp DB with corpus)
- [x] ABSTRACT.md with initial results (87% pass, ABR 0.77)
- [x] Related Work section with benchmark positioning

---

*This roadmap is a living document. Update as gaps are closed and priorities shift.*
