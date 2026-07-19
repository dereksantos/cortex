# Implementation Plan: context_adjust_watermarks

> **Tool Name**: `context_adjust_watermarks`
> **Purpose**: Dynamically adjust working set watermarks
> **Status**: **SHIPPED (2026-07)** — historical plan. The live source in
> `internal/tools/context_adjust_watermarks.go` is authoritative; the as-built
> record is [`context-window-modification-tools.md`](../context-window-modification-tools.md).

---

## 1. Overview

`context_adjust_watermarks(high_delta, low_delta)` dynamically adjusts the working set watermarks (high and low) by the given deltas. This:

- Reserves more budget for active task phases
- Tightens budget during exploratory phases
- Optimizes cache economics for current workload
- Is bounded (±W/4 to prevent abuse)

---

## 2. Tool Declaration

**File**: `internal/tools/tools.go`

```go
var ContextAdjustWatermarksTool = newTool(FunctionContextAdjustWatermarks,
	"Dynamically adjust the working set watermarks (high and low) by the given deltas. Use when you want to reserve more space for active turns or tighten the budget for exploratory phases.",
	objectSchema(map[string]any{
		"high_delta": map[string]any{
			"type":        "integer",
			"description": "Change to high watermark (positive = more space, negative = less). Range: -W/4 to +W/4.",
		},
		"low_delta": map[string]any{
			"type":        "integer",
			"description": "Change to low watermark (positive = more space, negative = less). Range: -W/4 to +W/4.",
		},
	}, "high_delta", "low_delta"))
```

**Add to `All`**:
```go
var All = []Tool{
    // ... existing tools ...
    ContextAdjustWatermarksTool,
    // ... other context tools ...
}
```

---

## 3. Working Set Extension

**File**: `internal/cache/workingset.go`

```go
// AdjustWatermarks adjusts the working set watermarks by the given deltas.
// Returns (newHigh, newLow, err).
func (ws *WorkingSet) AdjustWatermarks(highDelta, lowDelta int) (int, int, error) {
    newHigh := ws.highWM + highDelta
    newLow := ws.lowWM + lowDelta
    
    // Validate new watermarks
    if newHigh <= 0 {
        return 0, 0, fmt.Errorf("high watermark must be positive: %d", newHigh)
    }
    if newLow <= 0 {
        return 0, 0, fmt.Errorf("low watermark must be positive: %d", newLow)
    }
    if newLow > newHigh {
        return 0, 0, fmt.Errorf("low watermark (%d) cannot exceed high watermark (%d)", newLow, newHigh)
    }
    
    // Apply adjustment
    ws.highWM = newHigh
    ws.lowWM = newLow
    
    return newHigh, newLow, nil
}

// GetWatermarks returns the current watermarks.
func (ws *WorkingSet) GetWatermarks() (high, low int) {
    return ws.highWM, ws.lowWM
}
```

---

## 4. CortexSession Extension

**File**: `cmd/cortex/main.go` (CortexSession methods)

```go
// AdjustWatermarks adjusts the working set watermarks by the given deltas.
// Returns (newHigh, newLow, err).
func (cs *CortexSession) AdjustWatermarks(highDelta, lowDelta int) (int, int, error) {
    if cs.ws == nil {
        return 0, 0, errors.New("working set not initialized")
    }
    
    // Validate deltas (bounded to prevent abuse)
    maxDelta := cs.windowSize() / 4
    if highDelta < -maxDelta || highDelta > maxDelta {
        return 0, 0, fmt.Errorf(
            "high_delta out of range: %d (must be -%d to +%d)",
            highDelta, maxDelta, maxDelta,
        )
    }
    if lowDelta < -maxDelta || lowDelta > maxDelta {
        return 0, 0, fmt.Errorf(
            "low_delta out of range: %d (must be -%d to +%d)",
            lowDelta, maxDelta, maxDelta,
        )
    }
    
    // Adjust watermarks
    newHigh, newLow, err := cs.ws.AdjustWatermarks(highDelta, lowDelta)
    if err != nil {
        return 0, 0, err
    }
    
    // Log the adjustment
    cs.captureEvent("watermarks.adjust", map[string]any{
        "old_high": cs.ws.highWM - highDelta,
        "old_low":  cs.ws.lowWM - lowDelta,
        "new_high": newHigh,
        "new_low":  newLow,
        "delta_high": highDelta,
        "delta_low":  lowDelta,
    })
    
    return newHigh, newLow, nil
}

// checkConfig checks if context_adjust_watermarks is enabled via config.
// Returns (enabled, message). If disabled, returns (false, message) with explanation.
func checkConfig(tc ToolCall, deps ToolDeps) (bool, string, error) {
    // Get session config
    if session, ok := deps.(*CortexSession); ok && session.config != nil {
        if session.config.Tools.EnableContextAdjustWatermarks != nil {
            if !*session.config.Tools.EnableContextAdjustWatermarks {
                return false, "context_adjust_watermarks is disabled in .cortex/config.json", nil
            }
        }
    }
    return true, "", nil
}

// GetWindow returns the total window size.
func (cs *CortexSession) GetWindow() int {
    return cs.windowSize()
}

// checkConfig checks if context_adjust_watermarks is enabled via config.
// Returns (enabled, message). If disabled, returns (false, message) with explanation.
func checkConfig(tc ToolCall, deps ToolDeps) (bool, string, error) {
    // Get session config
    if session, ok := deps.(*CortexSession); ok && session.config != nil {
        if session.config.Tools.EnableContextAdjustWatermarks != nil {
            if !*session.config.Tools.EnableContextAdjustWatermarks {
                return false, "context_adjust_watermarks is disabled in .cortex/config.json", nil
            }
        }
    }
    return true, "", nil
}

// GetWindow returns the total window size.
func (cs *CortexSession) GetWindow() int {
    return cs.windowSize()
}
```

---

## 5. Implementation

**File**: `internal/tools/context_tools.go` (add function)

```go
const FunctionContextAdjustWatermarks = "context_adjust_watermarks"

// contextAdjustWatermarks dynamically adjusts the working set watermarks.
// This is bounded (±W/4 to prevent abuse) and maintains the invariant lowWM <= highWM.
func contextAdjustWatermarks(tc ToolCall, deps ToolDeps) (string, error) {
    // Check if tool is enabled via config
    if enabled, msg, err := checkConfig(tc, deps); !enabled {
        if err != nil {
            return "", err
        }
        return msg, nil
    }
    
    // Parse arguments
    highDelta, _ := tc.IntArg("high_delta")
    lowDelta, _ := tc.IntArg("low_delta")
    
    // Default to 0 if omitted
    if tc.Function.Arguments != "" {
        // Arguments were provided, so we need to parse them
        // (The ToolCall.StringArg/IntArg return (value, ok) pattern)
        // For now, we'll use default values if ok is false
    }
    
    // Validate deltas (bounded to prevent abuse)
    maxDelta := deps.GetWindow() / 4
    if highDelta < -maxDelta || highDelta > maxDelta {
        return "", fmt.Errorf(
            "high_delta out of range: %d (must be -%d to +%d)",
            highDelta, maxDelta, maxDelta,
        )
    }
    if lowDelta < -maxDelta || lowDelta > maxDelta {
        return "", fmt.Errorf(
            "low_delta out of range: %d (must be -%d to +%d)",
            lowDelta, maxDelta, maxDelta,
        )
    }
    
    // Adjust watermarks
    newHigh, newLow, err := deps.AdjustWatermarks(highDelta, lowDelta)
    if err != nil {
        return "", fmt.Errorf("adjust watermarks failed: %w", err)
    }
    
    // Format response
    return fmt.Sprintf(
        "Adjusted watermarks: high=%d→%d, low=%d→%d. Hysteresis preserved: %d tokens.",
        newHigh-highDelta, newHigh, newLow-lowDelta, newLow, newHigh-newLow,
    ), nil
}
```

---

## 6. Unit Tests

**File**: `internal/tools/context_tools_test.go` (add tests)

```go
func TestContextAdjustWatermarks(t *testing.T) {
    // Setup: create a working set with default watermarks
    windowSize := 8192 // 8k tokens
    ws := cache.New(1, windowSize/2, windowSize/3) // highWM=W/2, lowWM=W/3
    
    cs := &mockSession{
        ws:     ws,
        window: windowSize,
    }
    
    // Adjust watermarks
    tc := ToolCall{
        Function: FunctionCall{
            Name: FunctionContextAdjustWatermarks,
            Arguments: `{"high_delta":256,"low_delta":128}`,
        },
    }
    
    result, err := contextAdjustWatermarks(tc, cs)
    if err != nil {
        t.Fatalf("contextAdjustWatermarks failed: %v", err)
    }
    
    // Verify result
    if !strings.Contains(result, "Adjusted") {
        t.Errorf("unexpected result: %s", result)
    }
    
    // Verify watermarks were adjusted
    high, low := cs.ws.GetWatermarks()
    expectedHigh := windowSize/2 + 256
    expectedLow := windowSize/3 + 128
    
    if high != expectedHigh {
        t.Errorf("high watermark = %d, want %d", high, expectedHigh)
    }
    if low != expectedLow {
        t.Errorf("low watermark = %d, want %d", low, expectedLow)
    }
}

func TestContextAdjustWatermarks_InvalidDelta(t *testing.T) {
    windowSize := 8192
    ws := cache.New(1, windowSize/2, windowSize/3)
    cs := &mockSession{ws: ws, window: windowSize}
    
    tc := ToolCall{
        Function: FunctionCall{
            Name: FunctionContextAdjustWatermarks,
            Arguments: `{"high_delta":10000,"low_delta":0}`, // Exceeds maxDelta (W/4 = 2048)
        },
    }
    
    _, err := contextAdjustWatermarks(tc, cs)
    if err == nil {
        t.Errorf("expected error for delta exceeding max")
    }
}

func TestContextAdjustWatermarks_InvariantViolation(t *testing.T) {
    windowSize := 8192
    ws := cache.New(1, windowSize/2, windowSize/3)
    cs := &mockSession{ws: ws, window: windowSize}
    
    // Try to make low > high
    tc := ToolCall{
        Function: FunctionCall{
            Name: FunctionContextAdjustWatermarks,
            Arguments: `{"high_delta":-1000,"low_delta":1000}`,
        },
    }
    
    _, err := contextAdjustWatermarks(tc, cs)
    if err == nil {
        t.Errorf("expected error for invariant violation (low > high)")
    }
}

func TestContextAdjustWatermarks_DefaultValues(t *testing.T) {
    windowSize := 8192
    ws := cache.New(1, windowSize/2, windowSize/3)
    cs := &mockSession{ws: ws, window: windowSize}
    
    // Call with no arguments (should use defaults)
    tc := ToolCall{
        Function: FunctionCall{
            Name: FunctionContextAdjustWatermarks,
            Arguments: `{}`,
        },
    }
    
    result, err := contextAdjustWatermarks(tc, cs)
    if err != nil {
        t.Fatalf("contextAdjustWatermarks with defaults failed: %v", err)
    }
    
    // Verify watermarks unchanged (deltas = 0)
    high, low := cs.ws.GetWatermarks()
    expectedHigh := windowSize / 2
    expectedLow := windowSize / 3
    
    if high != expectedHigh {
        t.Errorf("high watermark = %d, want %d", high, expectedHigh)
    }
    if low != expectedLow {
        t.Errorf("low watermark = %d, want %d", low, expectedLow)
    }
}
```

---

## 7. Integration Tests

**File**: `cmd/cortex/context_tools_integration_test.go` (add tests)

```go
func TestContextAdjustWatermarks_Integration(t *testing.T) {
    // Full integration test with real session
    if testing.Short() {
        t.Skip("Skipping integration test in short mode")
    }
    
    // 1. Create session with working set
    cs := newTestSession()
    
    originalHigh, originalLow := cs.ws.GetWatermarks()
    windowSize := cs.GetWindow()
    
    // 2. Call context_adjust_watermarks via tool dispatch
    tc := ToolCall{
        Function: FunctionCall{
            Name: FunctionContextAdjustWatermarks,
            Arguments: `{"high_delta":512,"low_delta":256}`,
        },
    }
    
    result, err := cs.ExecuteTool(tc)
    if err != nil {
        t.Fatalf("context_adjust_watermarks failed: %v", err)
    }
    
    // 3. Verify result
    if !strings.Contains(result, "Adjusted") {
        t.Errorf("unexpected result format: %s", result)
    }
    
    // 4. Verify watermarks were adjusted
    newHigh, newLow := cs.ws.GetWatermarks()
    
    if newHigh != originalHigh+512 {
        t.Errorf("high watermark = %d, want %d", newHigh, originalHigh+512)
    }
    if newLow != originalLow+256 {
        t.Errorf("low watermark = %d, want %d", newLow, originalLow+256)
    }
}

func TestContextAdjustWatermarks_MultipleAdjustments(t *testing.T) {
    // Test: perform multiple adjustments in sequence
    cs := newTestSession()
    
    // Adjust up
    tc1 := ToolCall{
        Function: FunctionCall{
            Name: FunctionContextAdjustWatermarks,
            Arguments: `{"high_delta":256,"low_delta":128}`,
        },
    }
    _, err := cs.ExecuteTool(tc1)
    if err != nil {
        t.Fatalf("first adjustment failed: %v", err)
    }
    
    // Adjust down
    tc2 := ToolCall{
        Function: FunctionCall{
            Name: FunctionContextAdjustWatermarks,
            Arguments: `{"high_delta":-256,"low_delta":-128}`,
        },
    }
    _, err = cs.ExecuteTool(tc2)
    if err != nil {
        t.Fatalf("second adjustment failed: %v", err)
    }
    
    // Verify watermarks returned to original
    originalHigh, originalLow := cs.ws.GetWatermarks()
    expectedHigh := cs.GetWindow() / 2
    expectedLow := cs.GetWindow() / 3
    
    if originalHigh != expectedHigh {
        t.Errorf("high watermark = %d, want %d", originalHigh, expectedHigh)
    }
    if originalLow != expectedLow {
        t.Errorf("low watermark = %d, want %d", originalLow, expectedLow)
    }
}
```

---

## 8. Performance Testing

**File**: `internal/tools/context_tools_bench_test.go` (add benchmark)

```go
func BenchmarkContextAdjustWatermarks(b *testing.B) {
    windowSize := 8192
    ws := cache.New(1, windowSize/2, windowSize/3)
    cs := &mockSession{ws: ws, window: windowSize}
    
    tc := ToolCall{
        Function: FunctionCall{
            Name: FunctionContextAdjustWatermarks,
            Arguments: `{"high_delta":256,"low_delta":128}`,
        },
    }
    
    b.ResetTimer()
    for i := 0; i < b.N; i++ {
        _, _ = contextAdjustWatermarks(tc, cs)
    }
}

func BenchmarkContextAdjustWatermarks_LargeDelta(b *testing.B) {
    // Test performance with large delta (close to max)
    windowSize := 8192
    ws := cache.New(1, windowSize/2, windowSize/3)
    cs := &mockSession{ws: ws, window: windowSize}
    
    tc := ToolCall{
        Function: FunctionCall{
            Name: FunctionContextAdjustWatermarks,
            Arguments: `{"high_delta":2000,"low_delta":1000}`, // Close to W/4 = 2048
        },
    }
    
    b.ResetTimer()
    for i := 0; i < b.N; i++ {
        _, _ = contextAdjustWatermarks(tc, cs)
    }
}
```

---

## 9. Documentation

**File**: `docs/context-window-modification-tools.md` (add section)

### context_adjust_watermarks

**Usage**:
```json
{
  "tool": "context_adjust_watermarks",
  "high_delta": 256,
  "low_delta": 128
}
```

**When to use**:
- You want to reserve more space for active turns
- You need to tighten the budget during exploratory phases
- Cache economics don't match current workload

**Safety**:
- Deltas bounded (±W/4 to prevent abuse)
- Invariant maintained (lowWM <= highWM)
- All adjustments journaled
- No immediate demotion (adjustment is passive)

**Examples**:

1. **Reserve more space**:
```json
{
  "tool": "context_adjust_watermarks",
  "high_delta": 512,
  "low_delta": 256
}
```

2. **Tighten budget**:
```json
{
  "tool": "context_adjust_watermarks",
  "high_delta": -256,
  "low_delta": -128
}
```

3. **Optimize for current task**:
```json
{
  "tool": "context_adjust_watermarks",
  "high_delta": 128,
  "low_delta": 64
}
```

---

## 10. Configuration via .cortex/config.json

The tool is enabled by default when the config section exists. To disable:

```json
{
  "tools": {
    "enable_context_adjust_watermarks": false
  }
}
```

**Configuration key**: `tools.enable_context_adjust_watermarks`

**Default**: enabled (when key omitted or `true`)

---

## 11. Success Metrics

| Metric | Target | Measurement |
|--------|--------|-------------|
| Tool call rate | < 0.1% of turns | Journal telemetry |
| Budget respected | 100% of calls | Log deltas |
| Invariant maintained | 100% of calls | Test suite |
| No regressions | 100% of existing tests pass | Test suite |

---

## 12. Open Questions

1. Should we add automatic watermark adjustment based on usage patterns?
2. Should we add a "target_budget" parameter instead of deltas?
3. Should we add watermarks to the journal for replay?

---

**Status**: Implementation ready for Phase 1

**Next**: Implement, test, document
