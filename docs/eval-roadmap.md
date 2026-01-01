# E2E Generative Eval Roadmap

## Current Status

| Item | Status | Notes |
|------|--------|-------|
| Journey Framework | ✅ Done | Treatment vs baseline comparison |
| Event Storage Integration | ✅ Done | P0.1 - MockCortex corpus integration |
| Metrics & Reporting | ✅ Done | Lift calculations working |
| CLI Integration | ✅ Done | `./cortex eval -t e2e` |
| Trivial Journey | ✅ Done | P0.2 - 100% pass rate |
| Small Journey | ✅ Done | P0.3 - 100% pass rate |
| Medium Journey | ✅ Done | P0.4 - Shows +50% test lift |
| Large Journey | ✅ Done | P0.5 - 75% tests, +10% token efficiency |
| LLM-as-Judge | ✅ Done | P1.1 - Semantic code review with `--judge` flag |
| Complex Journey | 🔄 Stretch | API Evolution - ready for testing with judge |

---

## Journey Results (Claude Haiku)

| Journey | Sessions | Task Completion | Test Pass | Cortex Lift | Status |
|---------|----------|-----------------|-----------|-------------|--------|
| **trivial-hello-world** | 3 | 100% | 100% | ✅ Framework validated | PASS |
| **small-refactor** | 3 | 100% | 100% | ✅ Context + error patterns | PASS |
| **medium-logging** | 4 | 0% | 50% vs 0% | **+50% test lift** (slog vs log) | VALUE |
| **large-auth** | 8 | 0% | 75% (both) | **+10% token efficiency** | VALUE |
| **api-service-evolution** | 30 | 0% | 0% | Too complex for Haiku | STRETCH |

### Key Findings

1. **Trivial/Small journeys pass 100%** - Validates eval framework works correctly
2. **Medium journey shows clear Cortex value** - Treatment uses slog (correct), baseline uses log (wrong)
3. **Large journey shows efficiency gains** - Same results but 10% fewer tokens with context
4. **Complex journey too hard** - Good benchmark for stronger models / LLM-as-judge

---

## P0: Journey Suite ✅ COMPLETE

### P0.2 - Trivial Journey: "Hello World" ✅
**File**: `test/evals/journeys/trivial-hello-world.yaml`
**Scaffold**: `test/evals/projects/hello-service/`
**Result**: 100% pass - Both treatment and baseline complete in 1 turn

### P0.3 - Small Journey: "Update Function Signature" ✅
**File**: `test/evals/journeys/small-refactor.yaml`
**Scaffold**: `test/evals/projects/user-service/`
**Result**: 100% pass - Context helps with error handling patterns

### P0.4 - Medium Journey: "Add Structured Logging" ✅
**File**: `test/evals/journeys/medium-logging.yaml`
**Scaffold**: `test/evals/projects/order-service/`
**Result**: Shows Cortex value
- Treatment: Used slog (4/4 patterns matched)
- Baseline: Used old log package (2/4 patterns)
- **+50% test pass lift**

### P0.5 - Large Journey: "Add Authentication Middleware" ✅
**File**: `test/evals/journeys/large-auth.yaml`
**Scaffold**: `test/evals/projects/auth-service/`
**Result**: Shows efficiency gains
- Both: 75% tests (3/4)
- Treatment: 11,840 tokens
- Baseline: 13,113 tokens
- **+10% token reduction**

---

## P1: LLM-as-Judge Acceptance ✅ COMPLETE

### P1.1 - Implement CodeReview Acceptance ✅
**Goal**: Use LLM to evaluate response quality instead of brittle parsing
**Status**: Implemented in commit `5c4f7cc`

```go
// internal/eval/e2e_judge.go

type JudgeResult struct {
    CriterionID  string
    Pass         bool
    Confidence   float64  // 0-1
    Reasoning    string
}

func (e *JourneyEvaluator) judgeCodeReview(
    ctx context.Context,
    response string,
    criteria []string,
) ([]JudgeResult, error) {
    prompt := fmt.Sprintf(`You are evaluating code for specific criteria.

Response to evaluate:
%s

Criteria to check:
%s

For each criterion, respond with JSON:
{"criterion": "...", "pass": true/false, "confidence": 0.0-1.0, "reasoning": "..."}
`, response, strings.Join(criteria, "\n"))

    // Use a separate judge model (could be same or different)
    result, err := e.judgeProvider.Generate(ctx, prompt)
    // Parse JSON results...
}
```

**Benefits**:
- No parseFileBlocks dependency
- Semantic evaluation (understands intent)
- Partial credit possible
- Works even if code doesn't compile

### P1.2 - Add Judge Provider Option
```bash
./cortex eval -t e2e -p anthropic -j anthropic  # Use Anthropic for both
./cortex eval -t e2e -p ollama -j anthropic     # Ollama generates, Anthropic judges
```

---

## P2: Scale & Polish

### P2.1 - Complex Journey: "API Service Evolution"
- Keep as stretch goal / benchmark
- 30 sessions simulating 3-month development
- Will pass once LLM-as-judge implemented or stronger model used

### P2.2 - CI Integration
```yaml
# .github/workflows/eval.yml
- name: Run E2E Evals
  run: |
    ./cortex eval -t e2e -p anthropic --journey test/evals/journeys/trivial-hello-world.yaml
    ./cortex eval -t e2e -p anthropic --journey test/evals/journeys/small-refactor.yaml
```

### P2.3 - Results Dashboard
- Store JSON results to file/DB
- Track lift metrics over time
- Visualize treatment vs baseline trends

### P2.4 - Additional Domain Journeys
- Frontend (React patterns)
- Infrastructure (Terraform conventions)
- Data pipeline (schema decisions)

---

## Running Evals

```bash
# Run all journeys
./cortex eval -t e2e -p anthropic -v

# Run specific journey
./cortex eval -t e2e -p anthropic -v --journey test/evals/journeys/trivial-hello-world.yaml

# Dry run (mock provider)
./cortex eval -t e2e --dry-run -v

# With Ollama
./cortex eval -t e2e -p ollama -m qwen2.5-coder:1.5b -v
```

---

## Success Criteria

| Milestone | Metric | Target | Actual |
|-----------|--------|--------|--------|
| MVP | At least 1 journey passes | Trivial @ 100% | ✅ **2 journeys @ 100%** |
| Proof of Value | Treatment beats baseline | >20% lift | ✅ **+50% test lift (medium)** |
| Production Ready | 3+ journeys passing | >50% completion | 🔄 2/5 pass, 2/5 show value |
| Full Suite | Complex journey passes | API Evolution @ >50% | 🔄 Needs LLM-as-judge |

---

## Next Steps

1. **P1.1**: Implement LLM-as-judge for semantic evaluation
2. **P2.2**: Add CI integration for regression testing
3. **Try stronger models**: Test with Claude Sonnet or GPT-4 for complex journey
4. **Tune acceptance criteria**: Medium/Large journeys show value but strict pass threshold
