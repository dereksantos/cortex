# Prompt: Implement LLM-as-Judge + LoCoMo Integration

## Context

We have an eval framework for Cortex (context memory for AI assistants) with:
- 37 scenarios testing 5 LongMemEval abilities
- Substring-based scoring (`includes`/`excludes` keyword matching)
- ABR (Agentic Benefit Ratio) for retrieval quality
- Adversarial scenarios that test if models follow context over training priors

Current scoring limitation: Models can pass by parroting keywords without understanding.

## Goal

Implement two improvements:

### 1. LLM-as-Judge Scoring Option

Add an alternative scoring mode that uses an LLM to evaluate semantic correctness.

**Requirements:**
- Keep substring scoring as default (fast, cheap, deterministic)
- Add `--judge` flag to use LLM-as-judge
- Judge model should be configurable (default: same as eval model, or use a stronger model)
- Judge prompt should evaluate:
  - Semantic correctness (not just keyword presence)
  - Whether response demonstrates understanding vs parroting
  - Confidence score 0.0-1.0

**Proposed judge prompt:**
```
You are evaluating whether an AI response correctly answers a question given specific project context.

Question: {query}
Expected behavior: Should include concepts: {includes}, Should NOT include: {excludes}
Context provided: {context_summary}

Response to evaluate:
{response}

Evaluate on these criteria:
1. CORRECTNESS: Does the response align with the provided context? (0.0-1.0)
2. UNDERSTANDING: Does it demonstrate understanding vs just keyword matching? (0.0-1.0)
3. HALLUCINATION: Does it make up information not in context? (0.0-1.0, higher = more hallucination)

Return JSON: {"correctness": 0.X, "understanding": 0.X, "hallucination": 0.X, "explanation": "..."}
```

**Implementation location:**
- Add `ScoreWithJudge()` in `internal/eval/v2/measure.go`
- Add judge option to `Evaluator` struct in `internal/eval/v2/eval.go`
- Add `--judge` and `--judge-model` flags in `cmd/cortex/main.go`

### 2. LoCoMo-Inspired Eval Categories

LoCoMo tests 5 QA reasoning types. Map to our scenarios:

| LoCoMo Category | Our Equivalent | Status |
|-----------------|----------------|--------|
| Single-hop | extraction-* scenarios | ✅ Have |
| Multi-hop | reasoning-* scenarios | ✅ Have |
| Temporal | temporal-* scenarios | ✅ Have |
| Adversarial | adversarial-* scenarios | ✅ Have |
| Commonsense/World Knowledge | **NEW: locomo-commonsense** | ❌ Need |

**Create new scenario type: Commonsense Integration**

Tests whether model correctly combines project context with world knowledge:

```yaml
id: locomo-commonsense
name: "LoCoMo: Commonsense integration"

context:
  - type: decision
    content: "We deploy to AWS us-east-1 region."

tests:
  - id: combine-knowledge
    query: "What timezone should our cron jobs use for end-of-business processing?"
    expect:
      includes: ["EST", "Eastern", "US/Eastern", "America/New_York"]
      excludes: ["UTC", "PST"]
    # Model must know us-east-1 is Eastern timezone (world knowledge)
    # and apply it to project context (we deploy there)
```

### 3. LoCoMo Event Graph Concept

LoCoMo uses "event graphs" showing temporal/causal relationships. We can adapt this:

**Add `events` field to scenarios:**
```yaml
id: locomo-event-graph
name: "LoCoMo: Event causality"

events:
  - id: e1
    time: "2024-01"
    content: "Chose MongoDB for flexibility"
  - id: e2
    time: "2024-06"
    content: "Hit MongoDB scaling issues at 1M users"
    caused_by: [e1]
  - id: e3
    time: "2024-09"
    content: "Migrated to PostgreSQL"
    caused_by: [e2]

tests:
  - id: causal-chain
    query: "Why did we migrate to PostgreSQL?"
    expect:
      includes: ["scaling", "MongoDB", "issues"]
    causal_chain: [e1, e2, e3]  # Must reference this chain
```

## Files to Modify

1. `internal/eval/v2/measure.go` - Add `ScoreWithJudge()` function
2. `internal/eval/v2/eval.go` - Add judge option to Evaluator
3. `internal/eval/v2/scenario.go` - Add `events` field for causal graphs
4. `cmd/cortex/main.go` - Add `--judge` and `--judge-model` flags
5. `test/evals/v2/locomo-*.yaml` - Create LoCoMo-inspired scenarios

## Acceptance Criteria

1. `./cortex eval --judge` uses LLM to score instead of substring matching
2. `./cortex eval --judge --judge-model claude-3-haiku` uses specific judge model
3. Judge scores appear in output: `[judge] correctness=0.85 understanding=0.90`
4. At least 3 new LoCoMo-inspired scenarios created
5. Database schema updated to store judge scores

## Test Commands

```bash
# Compare substring vs judge scoring
./cortex eval -p ollama -m qwen2.5:0.5b --scenario test/evals/v2/adversarial-defaults.yaml
./cortex eval -p ollama -m qwen2.5:0.5b --scenario test/evals/v2/adversarial-defaults.yaml --judge

# Use stronger judge
./cortex eval -p ollama -m smollm:360m --scenario test/evals/v2/adversarial-defaults.yaml --judge --judge-model gemma2:2b
```

## References

- LongMemEval: https://github.com/xiaowu0162/LongMemEval (5 memory abilities)
- LoCoMo: https://snap-research.github.io/locomo/ (event graphs, multi-hop, commonsense)
- Current evals: `test/evals/v2/*.yaml` (37 scenarios)
- Eval framework: `internal/eval/v2/` (measure.go, eval.go, scenario.go)
