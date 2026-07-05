# Implementation Plan: context_evict

> **Tool Name**: `context_evict`
> **Purpose**: Remove outline entry from working set when context is tight
> **Status**: Phase 1 — Core Implementation

---

## 1. Overview

`context_evict(citation)` removes an outline entry from the working set. This is the **safest** evictive action because:

- The entry is already demoted (in the journal)
- Only affects outline, not hydrated tail
- Entry remains recoverable via `recall(citation)`
- Immediate space recovery in outline (W/8 budget)

---

## 2. Tool Declaration

**File**: `internal/tools/tools.go`

```go
var ContextEvictTool = newTool(FunctionContextEvict,
	"Remove an outline entry from the working set. The entry is already demoted (in the journal), so this only affects the hydrated tail and outline. Use when the outline is at its budget cap and you need to make room for new turns.",
	objectSchema(map[string]any{
		"citation": stringProp("The exact citation from the outline to evict, e.g. @session/20260701-143210#t12"),
	}, "citation"))
```

**Add to `All`**:
```go
var All = []Tool{
    // ... existing tools ...
    ContextEvictTool,
    // ... other context tools ...
}
```

---

## 3. Working Set Extension

**File**: `internal/cache/workingset.go`

```go
// EvictOutlineEntry removes an outline entry by citation.
// Returns true if the entry was found and evicted.
// This is idempotent (safe to call multiple times).
func (ws *WorkingSet) EvictOutlineEntry(citation string) bool {
    // Implementation: remove the entry from cs.outline
    // This is handled by CortexSession, not WorkingSet directly
    // But we can add a method to mark an entry as evicted
    return false // placeholder
}
```

**Note**: Actually, `context_evict` removes from `cs.outline` in `CortexSession`, not from `WorkingSet`. The working set tracks turns, not outline entries.

---

## 4. CortexSession Extension

**File**: `cmd/loop/main.go` (CortexSession methods)

```go
// RemoveOutlineEntry removes an outline entry by citation.
// Returns true if the entry was found and removed.
// This is idempotent (safe to call multiple times).
func (cs *CortexSession) RemoveOutlineEntry(citation string) bool {
    for i, entry := range cs.outline {
        if entry.Citation == citation {
            // Remove the entry
            cs.outline = append(cs.outline[:i], cs.outline[i+1:]...)
            
            // Log the change
            cs.captureEvent("outline.evict", map[string]any{
                "citation": citation,
                "outline_remaining": len(cs.outline),
            })
            
            return true
        }
    }
    
    // Entry not found (already evicted or invalid citation)
    return false
}

// OutlineEvictionCount returns the number of times entries have been evicted.
// Used for telemetry.
func (cs *CortexSession) OutlineEvictionCount() int {
    return cs.outlineEvictionCount
}

// checkConfig checks if context_evict is enabled via config.
// Returns (enabled, message). If disabled, returns (false, message) with explanation.
func checkConfig(tc ToolCall, deps ToolDeps) (bool, string, error) {
    // Get session config
    if session, ok := deps.(*CortexSession); ok && session.config != nil {
        if session.config.Tools.EnableContextEvict != nil {
            if !*session.config.Tools.EnableContextEvict {
                return false, "context_evict is disabled in .cortex/config.json", nil
            }
        }
    }
    return true, "", nil
}
```

**CortexSession struct extension**:
```go
type CortexSession struct {
    // ... existing fields ...
    
    outline            []TurnOutlineEntry
    outlineEvictionCount int  // telemetry
    
    // ... existing fields ...
}
```

---

## 5. Implementation

**File**: `internal/tools/context_tools.go` (add function)

```go
const FunctionContextEvict = "context_evict"

// contextEvict removes an outline entry from the working set.
// The entry is already demoted (in the journal), so this only affects
// the hydrated tail and outline. It is idempotent.
func contextEvict(tc ToolCall, deps ToolDeps) (string, error) {
    // Check if tool is enabled via config
    if enabled, msg, err := checkConfig(tc, deps); !enabled {
        if err != nil {
            return "", err
        }
        return msg, nil
    }
    
    // Parse citation
    citation, err := tc.StringArg("citation")
    if err != nil {
        return "", fmt.Errorf("citation is required: %w", err)
    }
    
    // Parse to verify format (but not require turn ordinal)
    _, err = parseCitation(citation)
    if err != nil {
        return "", fmt.Errorf("invalid citation format: %w", err)
    }
    
    // Attempt to remove the outline entry
    removed := deps.RemoveOutlineEntry(citation)
    if !removed {
        // Entry not found or already evicted
        return fmt.Sprintf(
            "Outline entry %s was not found or already evicted. No action taken.",
            citation,
        ), nil
    }
    
    // Success
    return fmt.Sprintf(
        "Evicted outline entry %s. Outline reduced by 1 entry. Space recovered: ~1-2 lines of outline.",
        citation,
    ), nil
}

// parseCitation parses a citation string into its components.
// Returns (sessionID, turnOrdinal, err).
func parseCitation(citation string) (sessionID string, turnOrdinal int, err error) {
    // Expected format: @session/<session-id>#t<ordinal>
    // Example: @session/20260701-143210#t12
    
    if !strings.HasPrefix(citation, "@session/") {
        return "", 0, fmt.Errorf("citation must start with '@session/'")
    }
    
    rest := citation[len("@session/"):]
    
    // Split by #t
    parts := strings.Split(rest, "#t")
    if len(parts) != 2 {
        return "", 0, fmt.Errorf("citation must contain '#t<ordinal>'")
    }
    
    sessionID = parts[0]
    
    // Parse ordinal
    ordinalStr := parts[1]
    if !strings.HasPrefix(ordinalStr, "t") {
        return "", 0, fmt.Errorf("ordinal must start with 't'")
    }
    
    ordinal, err := strconv.Atoi(ordinalStr[1:])
    if err != nil {
        return "", 0, fmt.Errorf("invalid ordinal: %w", err)
    }
    
    return sessionID, ordinal, nil
}
```

---

## 6. Unit Tests

**File**: `internal/tools/context_tools_test.go` (add tests)

```go
func TestContextEvict(t *testing.T) {
    // Setup: create a mock session with an outline entry
    cs := &mockSession{
        outline: []TurnOutlineEntry{
            {Citation: "@session/test#t1"},
            {Citation: "@session/test#t2"},
            {Citation: "@session/test#t3"},
        },
    }
    
    // Evict middle entry
    tc := ToolCall{
        Function: FunctionCall{
            Name: FunctionContextEvict,
            Arguments: `{"citation":"@session/test#t2"}`,
        },
    }
    
    result, err := contextEvict(tc, cs)
    if err != nil {
        t.Fatalf("contextEvict failed: %v", err)
    }
    
    // Verify result
    if !strings.Contains(result, "Evicted") {
        t.Errorf("unexpected result: %s", result)
    }
    
    // Verify entry was removed
    if len(cs.outline) != 2 {
        t.Errorf("expected 2 outline entries after evict, got %d", len(cs.outline))
    }
    
    // Verify correct entry was removed
    for _, entry := range cs.outline {
        if entry.Citation == "@session/test#t2" {
            t.Errorf("entry @session/test#t2 was not evicted")
        }
    }
}

func TestContextEvict_Idempotent(t *testing.T) {
    // Test: evict same entry twice
    cs := &mockSession{
        outline: []TurnOutlineEntry{
            {Citation: "@session/test#t1"},
        },
    }
    
    tc := ToolCall{
        Function: FunctionCall{
            Name: FunctionContextEvict,
            Arguments: `{"citation":"@session/test#t1"}`,
        },
    }
    
    // First eviction
    _, err := contextEvict(tc, cs)
    if err != nil {
        t.Fatalf("first contextEvict failed: %v", err)
    }
    
    // Second eviction (should be no-op)
    result, err := contextEvict(tc, cs)
    if err != nil {
        t.Fatalf("second contextEvict failed: %v", err)
    }
    
    // Verify second call reports entry not found
    if !strings.Contains(result, "was not found or already evicted") {
        t.Errorf("expected 'already evicted' message, got: %s", result)
    }
}

func TestContextEvict_InvalidCitation(t *testing.T) {
    tc := ToolCall{
        Function: FunctionCall{
            Name: FunctionContextEvict,
            Arguments: `{"citation":"invalid-citation"}`,
        },
    }
    
    _, err := contextEvict(tc, nil)
    if err == nil {
        t.Errorf("expected error for invalid citation")
    }
}

func TestContextEvict_ParseCitation(t *testing.T) {
    tests := []struct {
        citation     string
        expectError  bool
        expectID     string
        expectOrdinal int
    }{
        {"@session/20260701-143210#t12", false, "20260701-143210", 12},
        {"@session/test#t1", false, "test", 1},
        {"invalid", true, "", 0},
        {"@session/test#x12", true, "", 0},
        {"@session/test", true, "", 0},
    }
    
    for _, tt := range tests {
        sessionID, ordinal, err := parseCitation(tt.citation)
        if tt.expectError {
            if err == nil {
                t.Errorf("parseCitation(%q) expected error, got nil", tt.citation)
            }
        } else {
            if err != nil {
                t.Errorf("parseCitation(%q) unexpected error: %v", tt.citation, err)
            }
            if sessionID != tt.expectID {
                t.Errorf("parseCitation(%q) sessionID = %q, want %q", tt.citation, sessionID, tt.expectID)
            }
            if ordinal != tt.expectOrdinal {
                t.Errorf("parseCitation(%q) ordinal = %d, want %d", tt.citation, ordinal, tt.expectOrdinal)
            }
        }
    }
}
```

---

## 7. Integration Tests

**File**: `cmd/loop/context_tools_integration_test.go` (add tests)

```go
func TestContextEvict_Integration(t *testing.T) {
    // Full integration test with real session
    if testing.Short() {
        t.Skip("Skipping integration test in short mode")
    }
    
    // 1. Create session with outline entries
    cs := newTestSession()
    
    // Simulate some turns and demotion
    // (add outline entries manually for test)
    cs.outline = append(cs.outline, TurnOutlineEntry{
        Citation: "@session/test#t1",
        // ... other fields ...
    })
    cs.outline = append(cs.outline, TurnOutlineEntry{
        Citation: "@session/test#t2",
        // ... other fields ...
    })
    
    originalCount := len(cs.outline)
    
    // 2. Call context_evict via tool dispatch
    tc := ToolCall{
        Function: FunctionCall{
            Name: FunctionContextEvict,
            Arguments: `{"citation":"@session/test#t1"}`,
        },
    }
    
    result, err := cs.ExecuteTool(tc)
    if err != nil {
        t.Fatalf("context_evict failed: %v", err)
    }
    
    // 3. Verify result
    if !strings.Contains(result, "Evicted") {
        t.Errorf("unexpected result format: %s", result)
    }
    
    // 4. Verify outline was reduced
    if len(cs.outline) != originalCount-1 {
        t.Errorf("expected outline count %d, got %d", originalCount-1, len(cs.outline))
    }
}

func TestContextEvict_OutlineBudget(t *testing.T) {
    // Test: evict reduces outline count
    cs := newTestSession()
    
    // Add many outline entries
    for i := 1; i <= 10; i++ {
        cs.outline = append(cs.outline, TurnOutlineEntry{
            Citation: fmt.Sprintf("@session/test#t%d", i),
        })
    }
    
    // Evict half
    for i := 1; i <= 5; i++ {
        tc := ToolCall{
            Function: FunctionCall{
                Name: FunctionContextEvict,
                Arguments: fmt.Sprintf(`{"citation":"@session/test#t%d"}`, i),
            },
        }
        _, err := cs.ExecuteTool(tc)
        if err != nil {
            t.Fatalf("context_evict %d failed: %v", i, err)
        }
    }
    
    // Verify count
    if len(cs.outline) != 5 {
        t.Errorf("expected 5 outline entries, got %d", len(cs.outline))
    }
}
```

---

## 8. Performance Testing

**File**: `internal/tools/context_tools_bench_test.go` (add benchmark)

```go
func BenchmarkContextEvict(b *testing.B) {
    // Setup: create a session with many outline entries
    cs := &mockSession{
        outline: make([]TurnOutlineEntry, 100),
    }
    for i := range cs.outline {
        cs.outline[i] = TurnOutlineEntry{
            Citation: fmt.Sprintf("@session/test#t%d", i+1),
        }
    }
    
    tc := ToolCall{
        Function: FunctionCall{
            Name: FunctionContextEvict,
            Arguments: `{"citation":"@session/test#t50"}`,
        },
    }
    
    b.ResetTimer()
    for i := 0; i < b.N; i++ {
        // Reset outline for each iteration
        cs.outline = cs.outline[:100]
        for j := range cs.outline {
            cs.outline[j] = TurnOutlineEntry{
                Citation: fmt.Sprintf("@session/test#t%d", j+1),
            }
        }
        
        _, _ = contextEvict(tc, cs)
    }
}

func BenchmarkContextEvict_ManyEntries(b *testing.B) {
    // Test performance with many entries (worst case: evict last)
    cs := &mockSession{
        outline: make([]TurnOutlineEntry, 1000),
    }
    for i := range cs.outline {
        cs.outline[i] = TurnOutlineEntry{
            Citation: fmt.Sprintf("@session/test#t%d", i+1),
        }
    }
    
    tc := ToolCall{
        Function: FunctionCall{
            Name: FunctionContextEvict,
            Arguments: `{"citation":"@session/test#t1000"}`,
        },
    }
    
    b.ResetTimer()
    for i := 0; i < b.N; i++ {
        // Reset outline
        cs.outline = cs.outline[:1000]
        for j := range cs.outline {
            cs.outline[j] = TurnOutlineEntry{
                Citation: fmt.Sprintf("@session/test#t%d", j+1),
            }
        }
        
        _, _ = contextEvict(tc, cs)
    }
}
```

---

## 9. Documentation

**File**: `docs/context-window-modification-tools.md` (add section)

### context_evict

**Usage**:
```json
{
  "tool": "context_evict",
  "citation": "@session/20260701-143210#t12"
}
```

**When to use**:
- Outline is approaching its W/8 budget cap
- You need immediate space recovery
- The demoted turn is no longer relevant to the current task

**Safety**:
- Entry is already in journal (no loss)
- Recoverable via `recall(citation)`
- Idempotent (safe to retry)
- Only removes from outline (not hydrated tail)

**Examples**:

1. **Clean up stale outline**:
```json
{
  "tool": "context_evict",
  "citation": "@session/20260701-143210#t5"
}
```

2. **Free up outline budget**:
```json
{
  "tool": "context_evict",
  "citation": "@session/20260701-143210#t8"
}
```

3. **After summarizing**:
```json
{
  "tool": "context_evict",
  "citation": "@session/20260701-143210#t10"
}
```

---

## 10. Configuration via .cortex/config.json

The tool is enabled by default when the config section exists. To disable:

```json
{
  "tools": {
    "enable_context_evict": false
  }
}
```

**Configuration key**: `tools.enable_context_evict`

**Default**: enabled (when key omitted or `true`)

---

## 11. Success Metrics

| Metric | Target | Measurement |
|--------|--------|-------------|
| Tool call rate | < 0.5% of turns | Journal telemetry |
| Outline reduction | 1 entry per call | Log citation |
| Citations preserved | 100% of calls | Test suite |
| No regressions | 100% of existing tests pass | Test suite |

---

## 12. Open Questions

1. Should we add a "reason" parameter to log why the entry was evicted?
2. Should we automatically evict entries under W/32 (proactive cleanup)?
3. Should we add a "batch evict" tool for multiple entries?

---

**Status**: Implementation ready for Phase 1

**Next**: Implement, test, document
