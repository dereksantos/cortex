# Real AI Integration Test

## What This Test Actually Does

This test **actually uses Ollama** to analyze events and demonstrates real LLM behavior, including its non-deterministic nature.

### Test Flow:

1. **Capture real event** with actual code/decision
2. **Start Cortex daemon** (or manually process)
3. **Wait for Ollama analysis** to complete
4. **Query insights** and validate they exist
5. **Check for variability** across multiple runs

### What Makes This "Real":

✅ **Actual LLM calls**: Ollama analyzes the event
✅ **Non-deterministic output**: LLM may produce different summaries
✅ **Timeout handling**: Test fails if Ollama is unavailable
✅ **Quality checks**: Validates analysis makes sense (not just exists)

### What This Still Doesn't Prove:

❌ **Developer productivity**: Would need user study
❌ **Time savings**: No baseline measurement
❌ **AI behavior improvement**: Would need before/after comparison with real AI assistant

## Prerequisites

- ✅ Ollama running (`ollama serve`)
- ✅ Model installed (`ollama pull mistral:7b`)
- ✅ Cortex built and in PATH

## Running the Test

```bash
# Check prerequisites
./check-prerequisites.sh

# Run test (may take 10-30 seconds for LLM)
./test-with-llm.sh

# Expected: Analysis completes with reasonable output
```

## What "Pass" Means

**Pass criteria:**
1. Event captured successfully
2. Ollama processes the event (no errors)
3. Insight created in database
4. Insight contains relevant keywords from event
5. Category assigned (decision/pattern/insight/strategy)

**Pass does NOT mean:**
- Perfect analysis quality
- Identical output across runs
- Specific wording or importance score

## Handling Non-Determinism

Since LLMs are non-deterministic:

```bash
# Run 3 times, check variations
for i in {1..3}; do
  ./test-with-llm.sh
  echo "Run $i complete"
  sleep 2
done

# Expected: Similar themes, different wording
# Valid: Category might vary slightly
# Invalid: Complete nonsense or no analysis
```

## Example Output (Actual)

```
📝 Captured event: Database choice decision

⏳ Waiting for Ollama analysis...
   (This may take 10-30 seconds)

✅ Analysis complete!

Insight:
{
  "summary": "Selected SQLite for embedded database needs",
  "category": "decision",
  "importance": 7,
  "tags": ["database", "sqlite", "embedded"],
  "reasoning": "Choice driven by single-binary deployment requirement"
}

✅ PASS: LLM analysis produced reasonable insight
⚠️  Note: Output may vary across runs (LLM is non-deterministic)
```

## Failure Cases

Test fails if:
- Ollama not running → `❌ FAIL: Ollama unavailable`
- Model not installed → `❌ FAIL: Model not found`
- Analysis timeout (>60s) → `❌ FAIL: Analysis timeout`
- Empty/invalid insight → `❌ FAIL: Invalid analysis`

## Honest Assessment

**What this proves:**
- Ollama integration works
- Events get analyzed
- Insights are stored

**What this doesn't prove:**
- Analysis quality is "good" (subjective)
- Developers will use this
- It saves time in practice

**To prove value, you'd need:**
- A/B testing with real developers
- Measured task completion times
- Surveys on usefulness
- Long-term usage data
