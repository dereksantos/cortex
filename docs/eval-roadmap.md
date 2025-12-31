# E2E Generative Eval Roadmap

## Current Status

| Item | Status | Notes |
|------|--------|-------|
| Journey Framework | Done | 30 sessions, treatment vs baseline |
| Event Storage Integration | Done | P0.1 completed |
| Metrics & Reporting | Done | Lift calculations working |
| CLI Integration | Done | `./cortex eval -t e2e` |
| Task Completion | Blocked | Tasks too complex, needs simpler evals + LLM-as-judge |

---

## P0: Get One Journey Passing

### P0.2 - Trivial Journey: "Hello World"
**Goal**: Prove the eval works end-to-end with simplest possible task

```yaml
# test/evals/journeys/trivial-hello-world.yaml
id: trivial-hello-world
type: e2e
name: "Trivial - Hello World"

project:
  name: "hello-service"
  scaffold: "test/evals/projects/hello-service"

sessions:
  - id: session-01
    phase: foundation
    events:
      - type: decision
        id: greeting-format
        content: "Greetings should be formatted as 'Hello, {name}!' with exclamation"
        tags: [greeting, format]
        importance: 8

  - id: session-02
    phase: feature
    task:
      description: "Add a Greet function that returns a greeting for a name"
      files_to_modify:
        - "greeter.go"
      max_turns: 5
      acceptance:
        tests_pass:
          - "TestGreet"
        patterns_required:
          - "Hello,"
          - "!"
```

**Scaffold**: Simple Go file with stub function
**Acceptance**: Function returns "Hello, {name}!" format
**Why it proves ROI**: Treatment should recall "use exclamation", baseline might use "Hello, name." or similar

---

### P0.3 - Small Journey: "Update Function Signature"
**Goal**: Test context helps with refactoring tasks

```yaml
id: small-refactor
type: e2e
name: "Small - Function Signature Update"

sessions:
  - id: session-01
    phase: foundation
    events:
      - type: decision
        id: error-handling
        content: "All service methods must return (result, error) tuple, never panic"
        tags: [errors, patterns]
        importance: 9

      - type: pattern
        id: context-first
        content: "All public functions must accept context.Context as first parameter"
        tags: [context, patterns]
        importance: 8

  - id: session-02
    phase: feature
    task:
      description: "Update GetUser to accept context and return error"
      files_to_modify:
        - "user.go"
      acceptance:
        patterns_required:
          - "context.Context"
          - "error"
        patterns_forbidden:
          - "panic("
```

**Why it proves ROI**: Treatment recalls "context first, return error", baseline might miss one or both

---

### P0.4 - Medium Journey: "Add Logging to Service"
**Goal**: Test multi-file awareness and pattern consistency

```yaml
id: medium-logging
type: e2e
name: "Medium - Add Structured Logging"

sessions:
  - id: session-01
    phase: foundation
    events:
      - type: decision
        id: logging-lib
        content: "Use log/slog for all logging, NOT fmt.Println or log.Printf"
        tags: [logging, infrastructure]
        importance: 9

      - type: pattern
        id: log-format
        content: "Log entries must include: operation name, duration, error (if any)"
        tags: [logging, observability]
        importance: 7

  - id: session-02
    phase: feature
    events:
      - type: pattern
        id: log-levels
        content: "Use slog.Info for success, slog.Error for failures, slog.Debug for verbose"
        tags: [logging]
        importance: 6

  - id: session-03
    phase: feature
    task:
      description: "Add structured logging to the ProcessOrder function"
      files_to_modify:
        - "order/processor.go"
      acceptance:
        patterns_required:
          - "slog."
          - "Info("
          - "Error("
        patterns_forbidden:
          - "fmt.Println"
          - "log.Printf"
```

**Why it proves ROI**: Treatment recalls specific slog patterns, baseline likely uses fmt.Println or wrong log levels

---

## P1: LLM-as-Judge Acceptance

### P1.1 - Implement CodeReview Acceptance
**Goal**: Use LLM to evaluate response quality instead of brittle parsing

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

### P2.1 - Complex Journey: "API Service Evolution" (Current)
- Keep as stretch goal / benchmark
- Will pass once LLM-as-judge implemented

### P2.2 - CI Integration
```yaml
# .github/workflows/eval.yml
- name: Run E2E Evals
  run: |
    ./cortex eval -t e2e -p anthropic --journey trivial
    ./cortex eval -t e2e -p anthropic --journey small
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

## Implementation Order

```
Week 1: P0.2 (Trivial) + P0.3 (Small) scaffolds
        ↓
Week 2: P0.4 (Medium) + verify at least one passes
        ↓
Week 3: P1.1 (LLM-as-Judge) implementation
        ↓
Week 4: P1.2 (Judge provider) + P2.2 (CI)
        ↓
Future: P2.3-P2.4 (Dashboard, more journeys)
```

---

## Success Criteria

| Milestone | Metric | Target |
|-----------|--------|--------|
| MVP | At least 1 journey passes | Trivial @ 100% |
| Proof of Value | Treatment beats baseline | >20% lift on any metric |
| Production Ready | 3+ journeys passing | >50% completion rate |
| Full Suite | Complex journey passes | API Evolution @ >50% |
