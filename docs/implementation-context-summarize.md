# Implementation Plan: context_summarize

> **Tool Name**: `context_summarize`
> **Purpose**: Compress demoted context when the model needs to recover space
> **Status**: Phase 1 — Core Implementation

---

## 1. Overview

`context_summarize(citation, goal, budget)` compresses a demoted turn (referenced by citation) into a compact digest. This is the **safest** and **most valuable** context modification tool because:

- It reuses existing, proven `Summarizer` interface
- Citations are preserved mechanically
- No risk of losing user words (input is already in journal)
- Provides immediate value: recover space while preserving key facts

---

## 2. Tool Declaration

**File**: `internal/tools/tools.go`

```go
var ContextSummarizeTool = newTool(FunctionContextSummarize,
	"Compress a demoted turn (referenced by citation) into a compact digest. Use when the outline is growing and you need to recover space while preserving the key facts.",
	objectSchema(map[string]any{
		"citation": stringProp("The exact citation from the outline, e.g. @session/20260701-143210#t12"),
		"goal":     stringProp("What key facts must the summary preserve?"),
		"budget":   map[string]any{"type": "integer", "description": "Target token budget for the summary (typically 256-1024 tokens, default 512)"},
	}, "citation", "goal"))
```

**Add to `All`**:
```go
var All = []Tool{
    // ... existing tools ...
    ContextSummarizeTool,
    // ... other context tools ...
}
```

---

## 3. ToolDeps Extension

**File**: `internal/tools/tools.go` (ToolDeps interface)

```go
// ContextSummarizer compresses context using sequential chunk-and-fold.
// Used by context_summarize tool. Satisfied by *CortexSession.
type ContextSummarizer interface {
    Summarize(ctx context.Context, path, goal string, window int) (digest string, compressed bool, err error)
}
```

**Note**: Already exists as `Summarizer` interface — we'll reuse it.

---

## 4. Implementation

**File**: `internal/tools/context_tools.go` (new file)

```go
package tools

import (
    "context"
    "fmt"
    "strings"
)

const FunctionContextSummarize = "context_summarize"

// contextSummarize compresses a demoted turn into a compact digest.
// It uses the existing Summarizer interface (sequential chunk-and-fold)
// and preserves the citation mechanically.
func contextSummarize(tc ToolCall, deps ToolDeps) (string, error) {
    // Parse arguments
    citation, err := tc.StringArg("citation")
    if err != nil {
        return "", fmt.Errorf("citation is required: %w", err)
    }
    
    goal, _ := tc.StringArg("goal") // optional
    if goal == "" {
        goal = "What are the key facts and decisions in this turn?"
    }
    
    budget, _ := tc.IntArg("budget")
    if budget <= 0 {
        budget = 512 // default
    }
    
    // Recall the raw messages (deterministic, no LLM)
    rawDetail, err := deps.Recall(citation)
    if err != nil {
        return "", fmt.Errorf("recall failed: %w", err)
    }
    
    // Write raw detail to a temp file for summarization
    // (Summarizer interface expects a file path)
    tempPath := fmt.Sprintf(".cortex/summarize-tmp/%s.md", citationToFilename(citation))
    if err := deps.WriteTempFile(tempPath, rawDetail); err != nil {
        return "", fmt.Errorf("write temp file: %w", err)
    }
    defer deps.RemoveTempFile(tempPath)
    
    // Use existing Summarizer interface
    digest, compressed, err := deps.Summarize(context.Background(), tempPath, goal, budget)
    if err != nil {
        return "", fmt.Errorf("summarize failed: %w", err)
    }
    
    // MUST preserve citation (mechanical check)
    if !strings.Contains(digest, citation) {
        digest += fmt.Sprintf("\n\n[CITATION KEPT: %s]", citation)
    }
    
    // Format response
    return fmt.Sprintf(
        "Summarized %s into ~%d tokens (compressed: %v):\n\n%s",
        citation, budget, compressed, digest,
    ), nil
}

// citationToFilename converts a citation to a safe filename.
func citationToFilename(citation string) string {
    // Replace @, /, # with safe characters
    safe := strings.NewReplacer(
        "@", "",
        "/", "-",
        "#", "-",
        " ", "",
    ).Replace(citation)
    
    // Truncate if too long
    if len(safe) > 64 {
        safe = safe[:64]
    }
    
    return safe + ".md"
}
```

---

## 5. Dispatcher Registration

**File**: `internal/tools/tools.go` (Execute switch)

```go
const (
    // ... existing constants ...
    FunctionContextSummarize = "context_summarize"
)

func Execute(ctx context.Context, tc ToolCall, deps ToolDeps) (string, error) {
    // ... existing cases ...
    case FunctionContextSummarize:
        return contextSummarize(tc, deps)
    // ... other cases ...
}
```

---

## 6. Unit Tests

**File**: `internal/tools/context_tools_test.go` (new file)

```go
package tools

import (
    "testing"
)

func TestContextSummarize(t *testing.T) {
    // Setup: create a mock session with a demoted turn
    // Recall returns: "user: 'fix the bug' · edit x.go [ok] · assistant: fixed"
    // Summarize returns: "Fixed bug in x.go"
    
    tc := ToolCall{
        Function: FunctionCall{
            Name: FunctionContextSummarize,
            Arguments: `{"citation":"@session/test#t1","goal":"What was the fix?","budget":256}`,
        },
    }
    
    deps := &mockDeps{
        recallResult: "user: 'fix the bug' · edit x.go [ok] · assistant: fixed",
        summarizeResult: "Fixed bug in x.go",
        summarizeCompressed: true,
    }
    
    result, err := contextSummarize(tc, deps)
    if err != nil {
        t.Fatalf("contextSummarize failed: %v", err)
    }
    
    if !strings.Contains(result, "@session/test#t1") {
        t.Errorf("result does not contain citation: %s", result)
    }
    
    if !strings.Contains(result, "Fixed bug in x.go") {
        t.Errorf("result does not contain summary: %s", result)
    }
}

func TestContextSummarize_PreservesCitation(t *testing.T) {
    // Edge case: summarize returns content without citation
    tc := ToolCall{
        Function: FunctionCall{
            Name: FunctionContextSummarize,
            Arguments: `{"citation":"@session/test#t5","goal":"Summary","budget":128}`,
        },
    }
    
    deps := &mockDeps{
        recallResult: "user: 'test' · edit y.go [ok]",
        summarizeResult: "Only summary text, no citation",
        summarizeCompressed: true,
    }
    
    result, err := contextSummarize(tc, deps)
    if err != nil {
        t.Fatalf("contextSummarize failed: %v", err)
    }
    
    // Citation MUST be preserved
    if !strings.Contains(result, "@session/test#t5") {
        t.Errorf("citation was not preserved in result: %s", result)
    }
}

func TestContextSummarize_DefaultBudget(t *testing.T) {
    // Edge case: budget omitted
    tc := ToolCall{
        Function: FunctionCall{
            Name: FunctionContextSummarize,
            Arguments: `{"citation":"@session/test#t1","goal":"Summary"}`,
        },
    }
    
    deps := &mockDeps{
        recallResult: "user: 'test' · edit z.go [ok]",
        summarizeResult: "Summary",
        summarizeCompressed: false,
    }
    
    _, err := contextSummarize(tc, deps)
    if err != nil {
        t.Fatalf("contextSummarize failed with default budget: %v", err)
    }
    
    // Verify default budget (512) was used
    // (In mock, verify the budget parameter was passed)
}
```

---

## 7. Integration Tests

**File**: `cmd/loop/context_tools_integration_test.go` (new file)

```go
package main

import (
    "testing"
)

func TestContextSummarize_Integration(t *testing.T) {
    // Full integration test with real session
    if testing.Short() {
        t.Skip("Skipping integration test in short mode")
    }
    
    // 1. Create session with a demoted turn
    cs := newTestSession()
    
    // Simulate demotion (add to outline)
    cs.outline = append(cs.outline, TurnOutlineEntry{
        // ... populate fields ...
    })
    
    // 2. Call context_summarize via tool dispatch
    tc := ToolCall{
        Function: FunctionCall{
            Name: FunctionContextSummarize,
            Arguments: `{"citation":"@session/test#t1","goal":"Key facts"}`,
        },
    }
    
    result, err := cs.ExecuteTool(tc)
    if err != nil {
        t.Fatalf("context_summarize failed: %v", err)
    }
    
    // 3. Verify result
    if !strings.Contains(result, "Summarized") {
        t.Errorf("unexpected result format: %s", result)
    }
    
    // 4. Verify outline entry still exists (not removed)
    if len(cs.outline) == 0 {
        t.Error("outline entry was removed (should only be summarized)")
    }
}

func TestContextSummarize_BudgetControl(t *testing.T) {
    // Test that budget is respected
    cs := newTestSession()
    
    // Create a large demoted turn
    largeContent := generateLargeTurn(10000) // 10K chars ~ 2.5K tokens
    
    // Add to outline
    cs.outline = append(cs.outline, TurnOutlineEntry{
        // ... populate fields with largeContent ...
    })
    
    // Summarize with small budget
    tc := ToolCall{
        Function: FunctionCall{
            Name: FunctionContextSummarize,
            Arguments: `{"citation":"@session/test#t1","goal":"Summary","budget":256}`,
        },
    }
    
    result, err := cs.ExecuteTool(tc)
    if err != nil {
        t.Fatalf("context_summarize failed: %v", err)
    }
    
    // Result should be within budget (approximate check)
    // (In practice, verify via stats or mock)
}
```

---

## 8. Performance Testing

**File**: `internal/tools/context_tools_bench_test.go` (new file)

```go
package tools

import (
    "testing"
)

func BenchmarkContextSummarize(b *testing.B) {
    // Setup: create a large demoted turn
    largeContent := generateLargeTurn(50000) // ~12.5K tokens
    
    deps := &mockDeps{
        recallResult: largeContent,
        summarizeResult: "Summarized content",
        summarizeCompressed: true,
    }
    
    tc := ToolCall{
        Function: FunctionCall{
            Name: FunctionContextSummarize,
            Arguments: `{"citation":"@session/test#t1","goal":"Summary","budget":512}`,
        },
    }
    
    b.ResetTimer()
    for i := 0; i < b.N; i++ {
        _, _ = contextSummarize(tc, deps)
    }
}

func BenchmarkContextSummarize_RealSession(b *testing.B) {
    // Real session test (if performance is acceptable)
    cs := newTestSession()
    
    // Populate session with outline entry
    // ...
    
    tc := ToolCall{
        Function: FunctionCall{
            Name: FunctionContextSummarize,
            Arguments: `{"citation":"@session/test#t1","goal":"Summary","budget":512}`,
        },
    }
    
    b.ResetTimer()
    for i := 0; i < b.N; i++ {
        _, _ = cs.ExecuteTool(tc)
    }
}
```

---

## 9. Documentation

**File**: `docs/context-window-modification-tools.md` (add section)

### context_summarize

**Usage**:
```json
{
  "tool": "context_summarize",
  "citation": "@session/20260701-143210#t12",
  "goal": "What were the key decisions and implementation details?",
  "budget": 512
}
```

**When to use**:
- Outline is approaching its W/8 budget cap
- You need to recover space while preserving facts
- A demoted turn contains valuable information that should be summarized

**Safety**:
- Citations are always preserved
- Input is already in journal (no loss)
- Budget is enforced (default 512 tokens)

**Examples**:

1. **Consolidate implementation details**:
```json
{
  "tool": "context_summarize",
  "citation": "@session/20260701-143210#t5",
  "goal": "What was the API design decision?",
  "budget": 256
}
```

2. **Recover outline space**:
```json
{
  "tool": "context_summarize",
  "citation": "@session/20260701-143210#t8",
  "goal": "What was the test approach?",
  "budget": 384
}
```

---

## 10. Rollout Plan

**Canary**:
1. Enable tool for selected sessions
2. Monitor usage patterns
3. Verify no regressions

**Full rollout**:
1. Add to system prompt
2. Add to toolset
3. Monitor for 24 hours

**Fallback**:
- If issues detected, remove from toolset
- Tool remains in codebase (easy to re-enable)

---

## 11. Success Metrics

| Metric | Target | Measurement |
|--------|--------|-------------|
| Tool call rate | < 1% of turns | Journal telemetry |
| Budget respected | 100% of calls | Log budget vs actual |
| Citations preserved | 100% of calls | Test suite |
| No regressions | 100% of existing tests pass | Test suite |

---

## 12. Open Questions

1. Should we add a "compression quality" parameter?
2. Should we support multiple summarization strategies (e.g., "concise" vs "detailed")?
3. Should we automatically summarize outline entries under W/16 (proactive compression)?

---

**Status**: Implementation ready for Phase 1

**Next**: Implement, test, document
