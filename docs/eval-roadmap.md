# E2E Generative Eval Roadmap

**Mandate:** All evals >90% before new features

## Current Status: 50% (5/10)

| Journey | Result | Cortex Lift | Issue |
|---------|--------|-------------|-------|
| trivial-hello-world | ✅ PASS | 0% | - |
| small-refactor | ✅ PASS | 0% | - |
| medium-logging | ✅ PASS | **+57%** | - |
| cortex-config-quirk | ✅ PASS | +10% | - |
| cortex-error-handling | ✅ PASS | **+61%** | - |
| large-auth | ❌ FAIL | 0% completion | Build failures |
| api-service-evolution | ❌ FAIL | 0% completion | Too complex for Haiku |
| cortex-internal-api | ❌ FAIL | 0% completion | Build failures |
| cortex-deprecated-pattern | ❌ FAIL | **-30% regression** | Context hurts |
| cortex-id-naming | ❌ FAIL | **-20% regression** | Context hurts |

### Key Findings

1. **Cortex helps on knowledge-dependent tasks** - +57% and +61% lift where project-specific patterns matter
2. **Cortex hurts on simple tasks** - Regressions where baseline already succeeds
3. **Build failures dominate** - LLM generates placeholder imports, code doesn't compile
4. **Complex journeys exceed Haiku** - Need Sonnet for API Evolution

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

| Milestone | Target | Actual | Status |
|-----------|--------|--------|--------|
| MVP | 1+ journey passes | 5/10 pass | ✅ |
| Proof of Value | >20% lift somewhere | +61% (error handling) | ✅ |
| No Regressions | 0 regressions | 2 regressions | ❌ |
| **>90% Pass Rate** | 9/10 journeys | 5/10 journeys | ❌ |

---

## Path to >90%

### Fix Regressions (5/10 → 7/10)
- [ ] `cortex-deprecated-pattern`: Improve context relevance threshold
- [ ] `cortex-id-naming`: Improve context relevance threshold

### Fix Build Failures (7/10 → 9/10)
- [ ] `large-auth`: Investigate why builds fail
- [ ] `cortex-internal-api`: Investigate why builds fail
- [ ] Consider: scaffold files with real import paths

### Model Alignment (9/10 → 10/10)
- [ ] `api-service-evolution`: Tag as Sonnet-required, skip on Haiku runs

---

## Running Evals

```bash
# Run all journeys
./cortex eval -t e2e -p anthropic -v

# Run specific journey
./cortex eval -t e2e -p anthropic -v --journey test/evals/journeys/trivial-hello-world.yaml

# With stronger model
./cortex eval -t e2e -p anthropic -m claude-3-5-sonnet-20241022 -v
```
