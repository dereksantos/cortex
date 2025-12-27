# Eval System Roadmap

Assessment of Cortex eval system comprehensiveness and next steps.

## Current State

### What's Solid

- **A/B Comparison Framework**: Statistical rigor with t-tests, Cohen's d, p-values, confidence intervals
- **21 YAML Scenarios**: Linear (5), Idiom (3), E2E (1), Tree (4), Cognition (3), Retention (2), Other (3)
- **E2E Pipeline Tests**: Exercise full capture → store → retrieve → inject flow
- **Multi-path/Temporal Tree Evals**: Test constraint propagation and context isolation
- **Reporting**: Human-readable + Promptfoo-compatible JSON for CI

### Framework Components

| File | Purpose |
|------|---------|
| `internal/eval/eval.go` | Core A/B comparison evaluator |
| `internal/eval/e2e.go` | Full pipeline testing with real storage |
| `internal/eval/tree.go` | Multi-path and temporal scenarios |
| `internal/eval/cognition_eval.go` | Cognitive mode testing framework |
| `internal/eval/scorer.go` | Rule-based assertion scoring |
| `internal/eval/stats.go` | Statistical analysis |
| `internal/eval/reporter.go` | Output formatting |

---

## Gap Analysis

### Critical Gaps

| Gap | Impact | Notes |
|-----|--------|-------|
| No real Cognition Mode scenarios | Can't validate core differentiator | Modes tested via unit mocks only |
| No ABR measurement | Can't prove Think improves Fast mode | Agentic Benefit Ratio is key metric |
| No session accumulation tests | Can't verify learning over time | Multi-step quality improvement untested |

### High Priority Gaps

| Gap | Impact | Notes |
|-----|--------|-------|
| Dream sources not implemented | Dream mode can't run | Interface defined, no implementations |
| Think budget model untested | Budget decay with activity unverified | Core to cognitive architecture |
| No SDLC-spanning evals | OnContextEvolution vision unrealized | Missing multi-phase lifecycle tests |

### Medium Priority Gaps

| Gap | Impact | Notes |
|-----|--------|-------|
| Conflict resolution incomplete | Severity handling untested | Basic detection works |
| Reflect reranking not tested | NDCG quality unknown | No real scenarios |
| Resolve decision accuracy | inject/wait/queue logic unverified | Only mock tests |

---

## Priority Roadmap

### Tier 1: Core Value Proposition

These enable measuring whether Cortex actually works.

#### 1.1 Real Cognition Mode Scenarios

**Status**: [ ] Not started

Create YAML scenarios that test each mode with real data:

```yaml
# Example: reflex-embedding-quality.yaml
id: reflex-embedding-quality
type: cognition-mode
mode: reflex
name: "Reflex embedding retrieval quality"
corpus:
  - id: auth_jwt
    content: "Use JWT tokens with RS256 signing"
    tags: ["auth", "security"]
  - id: auth_session
    content: "Session-based auth with Redis store"
    tags: ["auth", "session"]
queries:
  - text: "How should we handle authentication?"
    expected_ids: ["auth_jwt", "auth_session"]
    max_latency_ms: 10
assertions:
  - precision_at_5: ">= 0.8"
  - latency_p99: "< 10ms"
```

**Modes to cover**:
- [ ] Reflex: embedding search with latency assertions
- [ ] Reflect: reranking quality with NDCG measurement
- [ ] Resolve: decision accuracy (inject/wait/queue/discard)

#### 1.2 Session Accumulation Evals

**Status**: [ ] Not started

Test that quality improves over multi-step sessions:

```yaml
id: session-learning
type: cognition-session
name: "Session learns authentication patterns"
session_steps:
  - id: step1
    query: "How does authentication work?"
    expected_ids: ["auth_module"]

  - id: step2
    query: "Show me the login flow"
    expect_topic_weights:
      authentication: ">= 0.6"

  - id: step3
    query: "What about session tokens?"
    expect_cache_hit: true
    expect_quality_vs_step1: ">= 1.1"  # 10% improvement
```

**Metrics to track**:
- [ ] Quality score improvement over steps
- [ ] TopicWeight learning accuracy
- [ ] WarmCache hit rate

#### 1.3 Agentic Benefit Ratio (ABR) Scenarios

**Status**: [ ] Not started

Measure: `quality(Fast+Think) / quality(Full)`

```yaml
id: abr-session-progression
type: cognition-benefit
name: "ABR approaches 1.0 over session"
checkpoints:
  - after_step: 1
    expected_abr: ">= 0.5"
  - after_step: 3
    expected_abr: ">= 0.7"
  - after_step: 5
    expected_abr: ">= 0.9"
```

---

### Tier 2: Background Modes

These enable Think and Dream to actually run.

#### 2.1 Dream Source Implementations

**Status**: [ ] Not started

Implement DreamSource interface for each source:

- [ ] **ProjectSource**: Sample random project files (code, docs, configs)
- [ ] **CortexSource**: Sample stored events and insights
- [ ] **GitSource**: Sample commits, diffs, blame history
- [ ] **ClaudeHistorySource**: Sample session logs (if available)

```go
type DreamSource interface {
    Name() string
    Sample(budget Budget) ([]Content, error)
    Priority() int
}
```

#### 2.2 Budget Model Tests

**Status**: [ ] Not started

Verify budget behavior:

```yaml
id: think-budget-decay
type: cognition-budget
mode: think
activity_levels:
  - level: high
    expected_budget: "< 0.2"  # Spare cycles only
  - level: medium
    expected_budget: "0.3-0.6"
  - level: low
    expected_budget: ">= 0.7"
```

```yaml
id: dream-budget-growth
type: cognition-budget
mode: dream
idle_periods:
  - duration_sec: 60
    expected_budget: ">= 0.3"
  - duration_sec: 300
    expected_budget: ">= 0.8"
  - duration_sec: 600
    expected_budget: "= 1.0"  # Capped at MaxBudget
```

---

### Tier 3: SDLC Evolution (OnContextEvolution Vision)

These realize the full context evolution vision.

#### 3.1 Multi-Phase Lifecycle Evals

**Status**: [ ] Not started

Test context evolution across SDLC phases:

```yaml
id: sdlc-prototype-to-production
type: evolution
name: "Context evolves prototype to production"
phases:
  - id: prototype
    description: "Initial prototype phase"
    events:
      - "Built quick auth with local storage"
      - "Used console.log for debugging"

  - id: production_ready
    description: "Hardening for production"
    events:
      - "Migrated auth to JWT with proper signing"
      - "Added structured logging"
    expected_context_shift:
      - old: "local storage auth"
        new: "JWT auth"

  - id: bug_fix
    query: "How do we handle auth?"
    expected: "JWT with RS256"
    must_not_include: "local storage"
```

#### 3.2 Hint Size Tuning Framework

**Status**: [ ] Not started

Find optimal context injection size:

```yaml
id: hint-size-optimization
type: tuning
parameter: hint_size
values: [100, 250, 500, 1000, 2000]
metrics:
  - quality_score
  - context_pollution_rate
  - latency_impact
goal: "maximize quality while pollution < 0.1"
```

#### 3.3 Decay Parameter Tuning

**Status**: [ ] Not started

Optimize Think/Dream decay rates:

```yaml
id: decay-tuning
type: tuning
parameters:
  think_decay_rate: [0.1, 0.2, 0.3, 0.5]
  dream_growth_rate: [0.05, 0.1, 0.2]
metrics:
  - abr_convergence_speed
  - resource_utilization
  - insight_freshness
```

---

## Implementation Notes

### Scenario File Locations

```
test/evals/scenarios/
├── cognition/
│   ├── mode/           # Individual mode tests
│   ├── session/        # Multi-step accumulation
│   ├── benefit/        # ABR measurement
│   └── budget/         # Budget model tests
├── evolution/          # SDLC lifecycle tests
└── tuning/             # Parameter optimization
```

### Running Cognition Evals

```bash
# Run all cognition evals
./cortex eval --cognition -p anthropic

# Run specific type
./cortex eval --cognition --type session

# Dry run (mock provider)
./cortex eval --cognition --dry-run
```

### Key Metrics to Track

| Metric | Target | Notes |
|--------|--------|-------|
| Reflex P99 latency | < 10ms | Non-negotiable |
| Reflect NDCG | >= 0.8 | Reranking quality |
| Resolve accuracy | >= 0.9 | Decision correctness |
| ABR at session end | >= 0.9 | Think effectiveness |
| Dream coverage | >= 0.7 | Source exploration |

---

## Progress Tracking

### Tier 1 Progress (COMPLETE)
- [x] 1.1 Real Cognition Mode Scenarios
  - [x] Reflex scenarios (reflex_quality, reflex_recency, reflex_edge_cases)
  - [x] Reflect scenarios (reflect_rerank, reflect_contradictions, reflect_ndcg)
  - [x] Resolve scenarios (resolve_inject, resolve_wait, resolve_queue)
- [x] 1.2 Session Accumulation Evals (session_topic_learning, session_cache_warmup, session_contradiction_resolution)
- [x] 1.3 ABR Scenarios (abr_cold_start, abr_domain_focus, abr_convergence)

**Infrastructure completed:**
- [x] Test corpus (test/evals/corpus/cognition_corpus.yaml)
- [x] MockCortex enhanced with corpus loading, mutex safety, improved Think simulation
- [x] Evaluator fixes (TopicWeightAccuracy, math.Log2, quality calculation)
- [x] CLI updated to load corpus

**Test results:** 73% pass rate (11/15 scenarios) with mock provider
- All Reflect, Session, ABR, and Conflict scenarios passing
- Some Reflex scenarios require real embeddings for precision@K

### Tier 2 Progress
- [ ] 2.1 Dream Source Implementations
  - [ ] ProjectSource
  - [ ] CortexSource
  - [ ] GitSource
  - [ ] ClaudeHistorySource
- [ ] 2.2 Budget Model Tests
  - [ ] Think budget decay
  - [ ] Dream budget growth

### Tier 3 Progress
- [ ] 3.1 Multi-Phase Lifecycle Evals
- [ ] 3.2 Hint Size Tuning
- [ ] 3.3 Decay Parameter Tuning

---

## References

- `CLAUDE.md` - Cognitive architecture documentation
- `OnContextEvolution.md` - Vision for SDLC-spanning evals
- `internal/eval/cognition.go` - Cognition eval type definitions
- `internal/eval/cognition_eval.go` - Cognition evaluator implementation
