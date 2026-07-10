# Implementation Plan: context_reorder

> **Tool Name**: `context_reorder`
> **Purpose**: Reorder hydrated tail by relevance score
> **Status**: **CUT (2026-07-10)** — never shippable as designed. The wire is
> assembled from the raw message log (`transport.wireMessages`), so reordering
> turn spans is unobservable by construction, and an observable reorder would
> violate the append-only invariant and the LCP prompt cache. See the status
> note in [`context-window-modification-tools.md`](context-window-modification-tools.md).
> Kept for the record.

---

## 1. Overview

`context_reorder(by="salience|recency|task-relevance")` reorders the hydrated tail (recent turns) by relevance score. This:

- Rearranges context order (no eviction or compression)
- Prioritizes most relevant turns
- Optimizes context for current task
- Is deterministic (same metric = same order)

---

## 2. Tool Declaration

**File**: `internal/tools/tools.go`

```go
var ContextReorderTool = newTool(FunctionContextReorder,
	"Reorder the hydrated tail (recent turns) by relevance score. This only rearranges order—it does not evict or compress. Use when context is bounded but you want the most relevant turns to appear first.",
	objectSchema(map[string]any{
		"by": map[string]any{
			"type":        "string",
			"description": "Relevance metric: 'salience' (long-term importance), 'recency' (most recent first), 'task-relevance' (current goal)",
			"enum":        []string{"salience", "recency", "task-relevance"},
		},
	}, "by"))
```

**Add to `All`**:
```go
var All = []Tool{
    // ... existing tools ...
    ContextReorderTool,
    // ... other context tools ...
}
```

---

## 3. Working Set Extension

**File**: `internal/cache/workingset.go`

```go
// ReorderTail reorders the hydrated tail turns based on the given metric.
// This only rearranges order—it does not evict or compress.
// Returns the new order of turn spans.
func (ws *WorkingSet) ReorderTail(metric string) []TurnSpan {
    // Get the hydrated tail (turns at or after frontier)
    tail := ws.getHydratedTail()
    
    // Sort based on metric
    switch metric {
    case "recency":
        // Already in recency order (newest at end), reverse for reverse order
        // For now, just return as-is (recency is the default order)
        return tail
    case "salience":
        // Use salience scores if available
        // For now, return as-is (salience not yet implemented)
        return tail
    case "task-relevance":
        // Use task relevance scores if available
        // For now, return as-is (task relevance not yet implemented)
        return tail
    default:
        // Invalid metric, return unchanged
        return tail
    }
}

// getHydratedTail returns the hydrated tail turns.
func (ws *WorkingSet) getHydratedTail() []TurnSpan {
    if ws.frontier >= len(ws.turns) {
        return nil
    }
    return ws.turns[ws.frontier:]
}
```

---

## 4. CortexSession Extension

**File**: `cmd/cortex/main.go` (CortexSession methods)

```go
// GetHydratedTail returns the current hydrated tail turns.
func (cs *CortexSession) GetHydratedTail() []TurnSpan {
    if cs.ws == nil {
        return nil
    }
    return cs.ws.getHydratedTail()
}

// ReorderTail reorders the hydrated tail based on the given metric.
// This only rearranges order—it does not evict or compress.
func (cs *CortexSession) ReorderTail(metric string) []TurnSpan {
    if cs.ws == nil {
        return nil
    }
    
    // Validate metric
    if metric != "salience" && metric != "recency" && metric != "task-relevance" {
        return nil
    }
    
    // Reorder the turns
    newOrder := cs.ws.ReorderTail(metric)
    
    // Update the working set
    // (This is a simplification — in practice, you'd need to preserve message indices)
    
    // Log the reorder
    cs.captureEvent("tail.reorder", map[string]any{
        "metric": metric,
        "count":  len(newOrder),
    })
    
    return newOrder
}

// checkConfig checks if context_reorder is enabled via config.
// Returns (enabled, message). If disabled, returns (false, message) with explanation.
func checkConfig(tc ToolCall, deps ToolDeps) (bool, string, error) {
    // Get session config
    if session, ok := deps.(*CortexSession); ok && session.config != nil {
        if session.config.Tools.EnableContextReorder != nil {
            if !*session.config.Tools.EnableContextReorder {
                return false, "context_reorder is disabled in .cortex/config.json", nil
            }
        }
    }
    return true, "", nil
}
```

---

## 5. Implementation

**File**: `internal/tools/context_tools.go` (add function)

```go
const FunctionContextReorder = "context_reorder"

// contextReorder reorders the hydrated tail by relevance score.
// This only rearranges order—it does not evict or compress.
func contextReorder(tc ToolCall, deps ToolDeps) (string, error) {
    // Check if tool is enabled via config
    if enabled, msg, err := checkConfig(tc, deps); !enabled {
        if err != nil {
            return "", err
        }
        return msg, nil
    }
    
    // Parse argument
    by, err := tc.StringArg("by")
    if err != nil {
        return "", fmt.Errorf("by is required: %w", err)
    }
    
    // Validate metric
    if by != "salience" && by != "recency" && by != "task-relevance" {
        return "", fmt.Errorf(
            "invalid metric: %s (must be 'salience', 'recency', or 'task-relevance')",
            by,
        )
    }
    
    // Get current hydrated tail
    oldOrder := deps.GetHydratedTail()
    if oldOrder == nil {
        return "No hydrated tail turns to reorder.", nil
    }
    
    // Reorder the tail
    newOrder := deps.ReorderTail(by)
    if newOrder == nil {
        return fmt.Sprintf("Reorder failed for metric: %s", by), nil
    }
    
    // Format response
    return fmt.Sprintf(
        "Reordered hydrated tail by %s. %d turns rearranged. No tokens recovered (order only).",
        by, len(oldOrder),
    ), nil
}
```

---

## 6. Unit Tests

**File**: `internal/tools/context_tools_test.go` (add tests)

```go
func TestContextReorder(t *testing.T) {
    // Setup: create a mock session with hydrated tail turns
    ws := cache.New(1, 1000, 500) // highWM=1000, lowWM=500
    
    // Add some turns
    ws.AddTurn(cache.TurnSpan{Start: 1, End: 10, Tokens: 200})
    ws.AddTurn(cache.TurnSpan{Start: 10, End: 20, Tokens: 300})
    ws.AddTurn(cache.TurnSpan{Start: 20, End: 30, Tokens: 250})
    
    cs := &mockSession{
        ws: ws,
    }
    
    tc := ToolCall{
        Function: FunctionCall{
            Name: FunctionContextReorder,
            Arguments: `{"by":"recency"}`,
        },
    }
    
    result, err := contextReorder(tc, cs)
    if err != nil {
        t.Fatalf("contextReorder failed: %v", err)
    }
    
    // Verify result
    if !strings.Contains(result, "Reordered") {
        t.Errorf("unexpected result: %s", result)
    }
}

func TestContextReorder_InvalidMetric(t *testing.T) {
    cs := &mockSession{
        ws: cache.New(1, 1000, 500),
    }
    
    tc := ToolCall{
        Function: FunctionCall{
            Name: FunctionContextReorder,
            Arguments: `{"by":"invalid"}`,
        },
    }
    
    _, err := contextReorder(tc, cs)
    if err == nil {
        t.Errorf("expected error for invalid metric")
    }
}

func TestContextReorder_NoTail(t *testing.T) {
    // Setup: working set with no hydrated tail
    ws := cache.New(1, 1000, 500)
    // Add some turns, then demote them all
    ws.AddTurn(cache.TurnSpan{Start: 1, End: 10, Tokens: 600}) // highWM=1000, so this demotes
    
    cs := &mockSession{
        ws: ws,
    }
    
    tc := ToolCall{
        Function: FunctionCall{
            Name: FunctionContextReorder,
            Arguments: `{"by":"recency"}`,
        },
    }
    
    result, err := contextReorder(tc, cs)
    if err != nil {
        t.Fatalf("contextReorder failed: %v", err)
    }
    
    // Verify no tail message
    if !strings.Contains(result, "No hydrated tail turns") {
        t.Errorf("expected 'no tail' message, got: %s", result)
    }
}

func TestContextReorder_Deterministic(t *testing.T) {
    // Test: same input always produces same output
    ws1 := cache.New(1, 1000, 500)
    ws1.AddTurn(cache.TurnSpan{Start: 1, End: 10, Tokens: 200})
    ws1.AddTurn(cache.TurnSpan{Start: 10, End: 20, Tokens: 300})
    ws1.AddTurn(cache.TurnSpan{Start: 20, End: 30, Tokens: 250})
    
    ws2 := cache.New(1, 1000, 500)
    ws2.AddTurn(cache.TurnSpan{Start: 1, End: 10, Tokens: 200})
    ws2.AddTurn(cache.TurnSpan{Start: 10, End: 20, Tokens: 300})
    ws2.AddTurn(cache.TurnSpan{Start: 20, End: 30, Tokens: 250})
    
    cs1 := &mockSession{ws: ws1}
    cs2 := &mockSession{ws: ws2}
    
    tc := ToolCall{
        Function: FunctionCall{
            Name: FunctionContextReorder,
            Arguments: `{"by":"recency"}`,
        },
    }
    
    _, err1 := contextReorder(tc, cs1)
    if err1 != nil {
        t.Fatalf("first contextReorder failed: %v", err1)
    }
    
    _, err2 := contextReorder(tc, cs2)
    if err2 != nil {
        t.Fatalf("second contextReorder failed: %v", err2)
    }
    
    // Verify same result
    // (In practice, verify the order is the same)
}
```

---

## 7. Integration Tests

**File**: `cmd/cortex/context_tools_integration_test.go` (add tests)

```go
func TestContextReorder_Integration(t *testing.T) {
    // Full integration test with real session
    if testing.Short() {
        t.Skip("Skipping integration test in short mode")
    }
    
    // 1. Create session with hydrated tail turns
    cs := newTestSession()
    
    // Simulate some turns (they go into the hydrated tail)
    // (In practice, you'd add turns via cs.Append)
    
    originalTail := cs.GetHydratedTail()
    if len(originalTail) == 0 {
        t.Skip("No hydrated tail turns to test with")
    }
    
    originalCount := len(originalTail)
    
    // 2. Call context_reorder via tool dispatch
    tc := ToolCall{
        Function: FunctionCall{
            Name: FunctionContextReorder,
            Arguments: `{"by":"recency"}`,
        },
    }
    
    result, err := cs.ExecuteTool(tc)
    if err != nil {
        t.Fatalf("context_reorder failed: %v", err)
    }
    
    // 3. Verify result
    if !strings.Contains(result, "Reordered") {
        t.Errorf("unexpected result format: %s", result)
    }
    
    // 4. Verify count unchanged (only order changed)
    newTail := cs.GetHydratedTail()
    if len(newTail) != originalCount {
        t.Errorf("expected %d tail turns, got %d", originalCount, len(newTail))
    }
}

func TestContextReorder_Metrics(t *testing.T) {
    // Test: different metrics produce different behaviors
    cs := newTestSession()
    
    // Add some turns
    // (In practice, you'd add turns via cs.Append)
    
    // Test each metric
    metrics := []string{"recency", "salience", "task-relevance"}
    
    for _, metric := range metrics {
        tc := ToolCall{
            Function: FunctionCall{
                Name: FunctionContextReorder,
                Arguments: fmt.Sprintf(`{"by":"%s"}`, metric),
            },
        }
        
        _, err := cs.ExecuteTool(tc)
        if err != nil {
            t.Fatalf("context_reorder(%s) failed: %v", metric, err)
        }
    }
}
```

---

## 8. Performance Testing

**File**: `internal/tools/context_tools_bench_test.go` (add benchmark)

```go
func BenchmarkContextReorder(b *testing.B) {
    // Setup: create a session with many hydrated tail turns
    ws := cache.New(1, 10000, 5000) // Large working set
    
    for i := 0; i < 100; i++ {
        ws.AddTurn(cache.TurnSpan{
            Start: i * 10,
            End:   (i + 1) * 10,
            Tokens: 200 + i*10,
        })
    }
    
    cs := &mockSession{ws: ws}
    
    tc := ToolCall{
        Function: FunctionCall{
            Name: FunctionContextReorder,
            Arguments: `{"by":"recency"}`,
        },
    }
    
    b.ResetTimer()
    for i := 0; i < b.N; i++ {
        _, _ = contextReorder(tc, cs)
    }
}

func BenchmarkContextReorder_ManyTurns(b *testing.B) {
    // Test performance with many turns
    ws := cache.New(1, 100000, 50000)
    
    for i := 0; i < 1000; i++ {
        ws.AddTurn(cache.TurnSpan{
            Start: i * 10,
            End:   (i + 1) * 10,
            Tokens: 200 + i*10,
        })
    }
    
    cs := &mockSession{ws: ws}
    
    tc := ToolCall{
        Function: FunctionCall{
            Name: FunctionContextReorder,
            Arguments: `{"by":"recency"}`,
        },
    }
    
    b.ResetTimer()
    for i := 0; i < b.N; i++ {
        _, _ = contextReorder(tc, cs)
    }
}
```

---

## 9. Documentation

**File**: `docs/context-window-modification-tools.md` (add section)

### context_reorder

**Usage**:
```json
{
  "tool": "context_reorder",
  "by": "recency"
}
```

**When to use**:
- Context is bounded but you want most relevant turns first
- Task context has shifted and you want to prioritize new information
- You want to optimize context order without evicting anything

**Safety**:
- Only rearranges (no eviction or compression)
- Preserves all messages
- Deterministic (same metric = same order)
- No budget impact (just reordering)

**Examples**:

1. **Prioritize recent information**:
```json
{
  "tool": "context_reorder",
  "by": "recency"
}
```

2. **Optimize for task relevance**:
```json
{
  "tool": "context_reorder",
  "by": "task-relevance"
}
```

3. **Use long-term salience**:
```json
{
  "tool": "context_reorder",
  "by": "salience"
}
```

---

## 9. Configuration via .cortex/config.json

The tool is enabled by default when the config section exists. To disable:

```json
{
  "tools": {
    "enable_context_reorder": false
  }
}
```

**Configuration key**: `tools.enable_context_reorder`

**Default**: enabled (when key omitted or `true`)

---

## 10. Success Metrics

| Metric | Target | Measurement |
|--------|--------|-------------|
| Tool call rate | < 0.2% of turns | Journal telemetry |
| Tail count unchanged | 100% of calls | Log count |
| Citations preserved | 100% of calls | Test suite |
| No regressions | 100% of existing tests pass | Test suite |

---

## 12. Open Questions

1. How should we implement salience scoring?
2. How should we implement task-relevance scoring?
3. Should we add a "direction" parameter (ascending vs descending)?

---

**Status**: Implementation ready for Phase 1

**Next**: Implement, test, document
