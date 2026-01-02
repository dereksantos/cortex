# Cortex Roadmap

**Last Updated:** January 2026
**Current Status:** Eval Stabilization
**Mandate:** All evals >90% before new features

---

## Current Eval Results

### Cognition Evals: 44% (16/36)

| Category | Pass/Total | Status | Issues |
|----------|------------|--------|--------|
| Reflect | 9/9 | ✅ | - |
| Reflex | 5/6 | ⚠️ | jwt-specific precision failing |
| Think (session) | 3/3 | ✅ | - |
| ABR (benefit) | 3/3 | ✅ | - |
| Dream | 2/3 | ⚠️ | Source coverage failing |
| Conflict | 2/2 | ✅ | - |
| **Unimplemented** | 0/18 | ❌ | linear, e2e, idiom, temporal, multi-path |

### E2E Journey Evals: 50% (5/10)

| Journey | Result | Cortex Lift |
|---------|--------|-------------|
| Trivial - Hello World | ✅ PASS | 0% (both succeed) |
| Small - Function Signature | ✅ PASS | 0% (both succeed) |
| Medium - Structured Logging | ✅ PASS | **+57% lift** |
| Cortex Proof - Config Quirk | ✅ PASS | +10% lift |
| Cortex Proof - Error Handling | ✅ PASS | **+61% lift** |
| Large - Auth Middleware | ❌ FAIL | 0% completion |
| API Service Evolution | ❌ FAIL | 0% completion |
| Cortex Proof - Internal API | ❌ FAIL | 0% completion |
| Cortex Proof - Deprecated Pattern | ❌ FAIL | **-30% regression** |
| Cortex Proof - ID Naming | ❌ FAIL | **-20% regression** |

### Summary

| Eval Suite | Current | Target |
|------------|---------|--------|
| Cognition | 44% | >90% |
| E2E Journey | 50% | >90% |
| **Overall** | **47%** | **>90%** |

---

## Mandate: Evals First

No new features until all evals consistently pass at >90%. This ensures:
1. Existing functionality is validated before expansion
2. Regressions are caught immediately
3. Claims in documentation match reality

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

## Gap Analysis: Path to >90%

### Cognition Evals: 44% → 90%

| Gap | Impact | Fix |
|-----|--------|-----|
| **18 unimplemented scenario types** | 50% of failures | Remove or implement |
| **Reflex jwt-specific precision** | 1 test failing | Tune scoring or fix test |
| **Dream source coverage** | 1 test failing | Fix source sampling |

**Decision needed:** The 18 "unimplemented" scenarios (linear, e2e, idiom, temporal, multi-path) were never meant to run in cognition eval. Either:
- **Option A:** Remove from cognition eval (they belong in other eval types)
- **Option B:** Implement handlers for each type

### E2E Journey Evals: 50% → 90%

| Gap | Impact | Fix |
|-----|--------|-----|
| **5 journeys at 0% completion** | 50% failures | Investigate why code doesn't build |
| **2 regressions** | Context hurting | Improve context relevance filtering |

**Root causes identified:**
1. LLM generates placeholder imports (`"path/to/pkg/..."`)
2. Complex journeys exceed model capability (Haiku)
3. Injected context adds noise on simple tasks

---

## Phased Roadmap: Eval Stabilization

### Phase E1: Cleanup (Target: 70%)

**Goal:** Remove false failures, fix obvious bugs

- [ ] **Cognition eval cleanup**
  - [ ] Remove/relocate 18 unimplemented scenario types
  - [ ] Fix jwt-specific precision test (tune threshold or corpus)
  - [ ] Fix Dream source coverage (ensure all sources sampled)

- [ ] **E2E journey cleanup**
  - [ ] Investigate 0% completion journeys - why builds fail
  - [ ] Fix or remove the 2 regression journeys

**Expected result:** Cognition 80%+, E2E 70%+

---

### Phase E2: Quality (Target: 85%)

**Goal:** Improve context relevance, reduce noise

- [ ] **Context injection quality**
  - [ ] Add relevance threshold - don't inject low-confidence results
  - [ ] Limit context to top-K most relevant items
  - [ ] Test: regressions should become ties or wins

- [ ] **Journey acceptance tuning**
  - [ ] Review failing journeys - are expectations realistic?
  - [ ] Adjust acceptance criteria where appropriate

**Expected result:** Cognition 85%+, E2E 80%+

---

### Phase E3: Robustness (Target: 90%+)

**Goal:** Consistent >90% across multiple runs

- [ ] **Flakiness reduction**
  - [ ] Identify non-deterministic failures
  - [ ] Add retries or fix root causes
  - [ ] Run eval suite 3x, all should pass

- [ ] **Model capability alignment**
  - [ ] Tag journeys by required model capability
  - [ ] Complex journeys require Sonnet, not Haiku
  - [ ] CI runs appropriate subset per model

**Expected result:** Consistent 90%+ on both suites

---

## Blocked Until Evals Pass

The following work is **blocked** until >90% evals:

| Feature | Reason Blocked |
|---------|----------------|
| Embeddings implementation | Can't validate without passing evals |
| Git Dream source | Core evals must pass first |
| Entity relationships | Polish feature, not priority |
| Literature-grounded evals | Need base evals working first |
| New cognitive modes | No expansion until stability |

---

## Success Criteria

| Milestone | Cognition | E2E Journey | Overall |
|-----------|-----------|-------------|---------|
| Current | 44% | 50% | 47% |
| Phase E1 | 80% | 70% | 75% |
| Phase E2 | 85% | 80% | 82% |
| Phase E3 | 90%+ | 90%+ | **90%+** |

**Mandate complete when:** 3 consecutive runs of full eval suite all pass at >90%.

---

## Open Questions

1. **Unimplemented scenarios:** Remove from cognition eval or implement handlers?
2. **Regression journeys:** Fix context injection or remove journeys?
3. **Model requirements:** Which journeys are Haiku-appropriate vs Sonnet-required?

---

## Recently Completed

- [x] Core cognitive architecture (Reflex, Reflect, Resolve, Think, Dream, Digest)
- [x] Event routing through cognition pipeline
- [x] Activity-based budget models
- [x] ABR metric and benefit evals
- [x] Session accumulation evals
- [x] Conflict detection evals
- [x] Claude Code integration
- [x] CLI-based Cortex for E2E testing
- [x] LLM-as-judge for code review acceptance
- [x] 5 Cortex-proof E2E journeys
- [x] Established eval baseline (47% overall)

---

*This roadmap is a living document. Mandate: >90% evals before new features.*
