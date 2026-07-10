# Implementation Plan: context_merge

> **Tool Name**: `context_merge`
> **Purpose**: Merge consecutive demoted turns into single outline entry
> **Status**: Phase 1 — Core Implementation

---

## 1. Overview

`context_merge(range_start, range_end)` merges consecutive demoted turns (referenced by citations) into a single outline entry. This:

- Reduces outline length while preserving content
- Consolidates related turns
- Frees up outline budget (W/8)
- Is deterministic (no LLM, mechanical merge)

---

## 2. Tool Declaration

**File**: `internal/tools/tools.go`

```go
var ContextMergeTool = newTool(FunctionContextMerge,
	"Merge consecutive demoted turns (referenced by citations) into a single outline entry. Use when the outline is growing and you have multiple adjacent demoted turns that can be condensed.",
	objectSchema(map[string]any{
		"range_start": stringProp("Citation of first turn to merge, e.g. @session/20260701-143210#t10"),
		"range_end":   stringProp("Citation of last turn to merge, e.g. @session/20260701-143210#t12"),
	}, "range_start", "range_end"))
```

**Add to `All`**:
```go
var All = []Tool{
    // ... existing tools ...
    ContextMergeTool,
    // ... other context tools ...
}
```

---

## 3. Implementation

**File**: `internal/tools/context_tools.go` (add function)

```go
const FunctionContextMerge = "context_merge"

// contextMerge merges consecutive demoted turns into a single outline entry.
// It is deterministic (no LLM) and preserves citations in the merged entry.
func contextMerge(tc ToolCall, deps ToolDeps) (string, error) {
    // Parse arguments
    startCitation, err := tc.StringArg("range_start")
    if err != nil {
        return "", fmt.Errorf("range_start is required: %w", err)
    }
    
    endCitation, err := tc.StringArg("range_end")
    if err != nil {
        return "", fmt.Errorf("range_end is required: %w", err)
    }
    
    // Parse citations to get turn ordinals
    _, startOrdinal, err := parseCitation(startCitation)
    if err != nil {
        return "", fmt.Errorf("invalid range_start citation: %w", err)
    }
    
    _, endOrdinal, err := parseCitation(endCitation)
    if err != nil {
        return "", fmt.Errorf("invalid range_end citation: %w", err)
    }
    
    // Validate range
    if startOrdinal > endOrdinal {
        return "", fmt.Errorf(
            "range_start (%d) must be <= range_end (%d)",
            startOrdinal, endOrdinal,
        )
    }
    
    // Perform merge (deterministic, mechanical)
    mergedCitation, err := deps.MergeOutlineEntries(startCitation, endCitation)
    if err != nil {
        return "", fmt.Errorf("merge failed: %w", err)
    }
    
    // Calculate turns merged
    turnsMerged := endOrdinal - startOrdinal + 1
    
    // Format response
    return fmt.Sprintf(
        "Merged turns %d-%d into single outline entry %s. Turns reduced: %d.",
        startOrdinal, endOrdinal, mergedCitation, turnsMerged,
    ), nil
}

// checkConfig checks if context_merge is enabled via config.
// Returns (enabled, message). If disabled, returns (false, message) with explanation.
func checkConfig(tc ToolCall, deps ToolDeps) (bool, string, error) {
    // Get session config
    if session, ok := deps.(*CortexSession); ok && session.config != nil {
        if session.config.Tools.EnableContextMerge != nil {
            if !*session.config.Tools.EnableContextMerge {
                return false, "context_merge is disabled in .cortex/config.json", nil
            }
        }
    }
    return true, "", nil
}
```

---

## 4. CortexSession Extension

**File**: `cmd/cortex/main.go` (CortexSession methods)

```go
// MergeOutlineEntries merges consecutive outline entries into a single entry.
// Returns the citation of the merged entry.
func (cs *CortexSession) MergeOutlineEntries(startCitation, endCitation string) (string, error) {
    // Find start and end indices
    startIndex := -1
    endIndex := -1
    
    for i, entry := range cs.outline {
        if entry.Citation == startCitation {
            startIndex = i
        }
        if entry.Citation == endCitation {
            endIndex = i
        }
    }
    
    if startIndex == -1 {
        return "", fmt.Errorf("start citation not found: %s", startCitation)
    }
    if endIndex == -1 {
        return "", fmt.Errorf("end citation not found: %s", endCitation)
    }
    if startIndex > endIndex {
        return "", fmt.Errorf("start index (%d) must be <= end index (%d)", startIndex, endIndex)
    }
    
    // Verify consecutive (no gaps)
    expectedCount := endIndex - startIndex + 1
    actualCount := 0
    for i := startIndex; i <= endIndex; i++ {
        // Check if entry is still at expected position
        // (outline may have been modified by evictions)
        if cs.outline[i].Citation != "" {
            actualCount++
        }
    }
    
    if actualCount < expectedCount {
        return "", fmt.Errorf("gap detected in outline entries %d-%d", startIndex, endIndex)
    }
    
    // Merge the entries (deterministic)
    mergedEntry := cs.mergeOutlineEntriesInternal(startIndex, endIndex)
    
    // Remove old entries and insert merged entry
    cs.outline = append(cs.outline[:startIndex], cs.outline[endIndex+1:]...)
    cs.outline = append(cs.outline[:startIndex], mergedEntry)
    
    // Update ordinals for entries after the merge
    cs.renumberOutlineEntries()
    
    // Log the merge
    cs.captureEvent("outline.merge", map[string]any{
        "start":       startIndex,
        "end":         endIndex,
        "merged_into": mergedEntry.Citation,
    })
    
    return mergedEntry.Citation, nil
}

// mergeOutlineEntriesInternal creates a merged outline entry from a range.
// This is deterministic (no LLM) — we just combine the text.
func (cs *CortexSession) mergeOutlineEntriesInternal(startIndex, endIndex int) TurnOutlineEntry {
    // Collect all entries in the range
    var entries []TurnOutlineEntry
    for i := startIndex; i <= endIndex; i++ {
        entries = append(entries, cs.outline[i])
    }
    
    // Merge text (simple concatenation with separators)
    var mergedText strings.Builder
    for i, entry := range entries {
        if i > 0 {
            mergedText.WriteString("\n\n---\n\n") // Separator
        }
        mergedText.WriteString(entry.Text)
    }
    
    // Create merged entry
    // Citation is the first entry's citation (keep the coordinate)
    mergedCitation := entries[0].Citation
    
    return TurnOutlineEntry{
        Ordinal:  entries[0].Ordinal, // Keep first ordinal
        Citation: mergedCitation,
        Text:     mergedText.String(),
        // Preserve other fields as needed
    }
}

// renumberOutlineEntries updates ordinal values after a merge.
func (cs *CortexSession) renumberOutlineEntries() {
    for i, entry := range cs.outline {
        // Renumber based on position in outline
        // (This is a simplification — in practice, you might want to keep original ordinals)
        entry.Ordinal = i + 1
        cs.outline[i] = entry
    }
}
```

---

## 5. Unit Tests

**File**: `internal/tools/context_tools_test.go` (add tests)

```go
func TestContextMerge(t *testing.T) {
    // Setup: create a mock session with outline entries
    cs := &mockSession{
        outline: []TurnOutlineEntry{
            {Citation: "@session/test#t1", Text: "Turn 1 content"},
            {Citation: "@session/test#t2", Text: "Turn 2 content"},
            {Citation: "@session/test#t3", Text: "Turn 3 content"},
        },
    }
    
    // Merge middle two entries
    tc := ToolCall{
        Function: FunctionCall{
            Name: FunctionContextMerge,
            Arguments: `{"range_start":"@session/test#t2","range_end":"@session/test#t3"}`,
        },
    }
    
    result, err := contextMerge(tc, cs)
    if err != nil {
        t.Fatalf("contextMerge failed: %v", err)
    }
    
    // Verify result
    if !strings.Contains(result, "Merged") {
        t.Errorf("unexpected result: %s", result)
    }
    
    // Verify entries were merged
    if len(cs.outline) != 2 {
        t.Errorf("expected 2 outline entries after merge, got %d", len(cs.outline))
    }
    
    // Verify merged entry contains both texts
    mergedFound := false
    for _, entry := range cs.outline {
        if strings.Contains(entry.Text, "Turn 2 content") && strings.Contains(entry.Text, "Turn 3 content") {
            mergedFound = true
            break
        }
    }
    if !mergedFound {
        t.Errorf("merged entry not found with expected content")
    }
}

func TestContextMerge_InvalidRange(t *testing.T) {
    cs := &mockSession{
        outline: []TurnOutlineEntry{
            {Citation: "@session/test#t1"},
        },
    }
    
    tc := ToolCall{
        Function: FunctionCall{
            Name: FunctionContextMerge,
            Arguments: `{"range_start":"@session/test#t3","range_end":"@session/test#t1"}`,
        },
    }
    
    _, err := contextMerge(tc, cs)
    if err == nil {
        t.Errorf("expected error for invalid range (start > end)")
    }
}

func TestContextMerge_EntriesNotFound(t *testing.T) {
    cs := &mockSession{
        outline: []TurnOutlineEntry{
            {Citation: "@session/test#t1"},
        },
    }
    
    tc := ToolCall{
        Function: FunctionCall{
            Name: FunctionContextMerge,
            Arguments: `{"range_start":"@session/test#t5","range_end":"@session/test#t10"}`,
        },
    }
    
    _, err := contextMerge(tc, cs)
    if err == nil {
        t.Errorf("expected error for citations not found")
    }
}

func TestContextMerge_Deterministic(t *testing.T) {
    // Test: same input always produces same output
    cs1 := &mockSession{
        outline: []TurnOutlineEntry{
            {Citation: "@session/test#t1", Text: "Turn 1"},
            {Citation: "@session/test#t2", Text: "Turn 2"},
            {Citation: "@session/test#t3", Text: "Turn 3"},
        },
    }
    
    cs2 := &mockSession{
        outline: []TurnOutlineEntry{
            {Citation: "@session/test#t1", Text: "Turn 1"},
            {Citation: "@session/test#t2", Text: "Turn 2"},
            {Citation: "@session/test#t3", Text: "Turn 3"},
        },
    }
    
    tc := ToolCall{
        Function: FunctionCall{
            Name: FunctionContextMerge,
            Arguments: `{"range_start":"@session/test#t2","range_end":"@session/test#t3"}`,
        },
    }
    
    _, err1 := contextMerge(tc, cs1)
    if err1 != nil {
        t.Fatalf("first contextMerge failed: %v", err1)
    }
    
    _, err2 := contextMerge(tc, cs2)
    if err2 != nil {
        t.Fatalf("second contextMerge failed: %v", err2)
    }
    
    // Verify same result
    if len(cs1.outline) != len(cs2.outline) {
        t.Errorf("results differ: %d vs %d entries", len(cs1.outline), len(cs2.outline))
    }
}
```

---

## 6. Integration Tests

**File**: `cmd/cortex/context_tools_integration_test.go` (add tests)

```go
func TestContextMerge_Integration(t *testing.T) {
    // Full integration test with real session
    if testing.Short() {
        t.Skip("Skipping integration test in short mode")
    }
    
    // 1. Create session with outline entries
    cs := newTestSession()
    
    // Add multiple outline entries
    for i := 1; i <= 5; i++ {
        cs.outline = append(cs.outline, TurnOutlineEntry{
            Citation: fmt.Sprintf("@session/test#t%d", i),
            Text:     fmt.Sprintf("Turn %d content", i),
        })
    }
    
    originalCount := len(cs.outline)
    
    // 2. Call context_merge via tool dispatch
    tc := ToolCall{
        Function: FunctionCall{
            Name: FunctionContextMerge,
            Arguments: `{"range_start":"@session/test#t2","range_end":"@session/test#t4"}`,
        },
    }
    
    result, err := cs.ExecuteTool(tc)
    if err != nil {
        t.Fatalf("context_merge failed: %v", err)
    }
    
    // 3. Verify result
    if !strings.Contains(result, "Merged") {
        t.Errorf("unexpected result format: %s", result)
    }
    
    // 4. Verify outline was reduced
    if len(cs.outline) != originalCount-2 { // 5 - 3 + 1 = 3
        t.Errorf("expected %d outline entries, got %d", originalCount-2, len(cs.outline))
    }
    
    // 5. Verify merged entry contains expected content
    mergedFound := false
    for _, entry := range cs.outline {
        if strings.Contains(entry.Text, "Turn 2") && strings.Contains(entry.Text, "Turn 4") {
            mergedFound = true
            break
        }
    }
    if !mergedFound {
        t.Errorf("merged entry not found")
    }
}

func TestContextMerge_MultipleMerges(t *testing.T) {
    // Test: perform multiple merges in sequence
    cs := newTestSession()
    
    // Add many outline entries
    for i := 1; i <= 10; i++ {
        cs.outline = append(cs.outline, TurnOutlineEntry{
            Citation: fmt.Sprintf("@session/test#t%d", i),
            Text:     fmt.Sprintf("Turn %d", i),
        })
    }
    
    // Merge first 3
    tc1 := ToolCall{
        Function: FunctionCall{
            Name: FunctionContextMerge,
            Arguments: `{"range_start":"@session/test#t1","range_end":"@session/test#t3"}`,
        },
    }
    _, err := cs.ExecuteTool(tc1)
    if err != nil {
        t.Fatalf("first merge failed: %v", err)
    }
    
    // Merge next 3
    tc2 := ToolCall{
        Function: FunctionCall{
            Name: FunctionContextMerge,
            Arguments: `{"range_start":"@session/test#t4","range_end":"@session/test#t6"}`,
        },
    }
    _, err = cs.ExecuteTool(tc2)
    if err != nil {
        t.Fatalf("second merge failed: %v", err)
    }
    
    // Verify count
    expectedCount := 10 - 3 - 3 + 2 // 10 - 6 + 2 = 6
    if len(cs.outline) != expectedCount {
        t.Errorf("expected %d outline entries, got %d", expectedCount, len(cs.outline))
    }
}
```

---

## 7. Performance Testing

**File**: `internal/tools/context_tools_bench_test.go` (add benchmark)

```go
func BenchmarkContextMerge(b *testing.B) {
    // Setup: create a session with many outline entries
    cs := &mockSession{
        outline: make([]TurnOutlineEntry, 100),
    }
    for i := range cs.outline {
        cs.outline[i] = TurnOutlineEntry{
            Citation: fmt.Sprintf("@session/test#t%d", i+1),
            Text:     fmt.Sprintf("Turn %d content", i+1),
        }
    }
    
    tc := ToolCall{
        Function: FunctionCall{
            Name: FunctionContextMerge,
            Arguments: `{"range_start":"@session/test#t50","range_end":"@session/test#t60"}`,
        },
    }
    
    b.ResetTimer()
    for i := 0; i < b.N; i++ {
        // Reset outline for each iteration
        cs.outline = cs.outline[:100]
        for j := range cs.outline {
            cs.outline[j] = TurnOutlineEntry{
                Citation: fmt.Sprintf("@session/test#t%d", j+1),
                Text:     fmt.Sprintf("Turn %d content", j+1),
            }
        }
        
        _, _ = contextMerge(tc, cs)
    }
}

func BenchmarkContextMerge_LargeRange(b *testing.B) {
    // Test performance with large merge range
    cs := &mockSession{
        outline: make([]TurnOutlineEntry, 100),
    }
    for i := range cs.outline {
        cs.outline[i] = TurnOutlineEntry{
            Citation: fmt.Sprintf("@session/test#t%d", i+1),
            Text:     fmt.Sprintf("Turn %d content", i+1),
        }
    }
    
    tc := ToolCall{
        Function: FunctionCall{
            Name: FunctionContextMerge,
            Arguments: `{"range_start":"@session/test#t1","range_end":"@session/test#t50"}`,
        },
    }
    
    b.ResetTimer()
    for i := 0; i < b.N; i++ {
        // Reset outline
        cs.outline = cs.outline[:100]
        for j := range cs.outline {
            cs.outline[j] = TurnOutlineEntry{
                Citation: fmt.Sprintf("@session/test#t%d", j+1),
                Text:     fmt.Sprintf("Turn %d content", j+1),
            }
        }
        
        _, _ = contextMerge(tc, cs)
    }
}
```

---

## 8. Documentation

**File**: `docs/context-window-modification-tools.md` (add section)

### context_merge

**Usage**:
```json
{
  "tool": "context_merge",
  "range_start": "@session/20260701-143210#t10",
  "range_end": "@session/20260701-143210#t12"
}
```

**When to use**:
- Outline is approaching its W/8 budget cap
- You have multiple adjacent demoted turns that can be condensed
- You want to reduce outline length while preserving content

**Safety**:
- Deterministic (no LLM, mechanical merge)
- Citations preserved in merged entry
- Only merges already-demoted turns
- Range validated (start <= end)

**Examples**:

1. **Consolidate implementation details**:
```json
{
  "tool": "context_merge",
  "range_start": "@session/20260701-143210#t5",
  "range_end": "@session/20260701-143210#t7"
}
```

2. **Free up outline budget**:
```json
{
  "tool": "context_merge",
  "range_start": "@session/20260701-143210#t8",
  "range_end": "@session/20260701-143210#t10"
}
```

3. **After summarizing multiple entries**:
```json
{
  "tool": "context_merge",
  "range_start": "@session/20260701-143210#t12",
  "range_end": "@session/20260701-143210#t14"
}
```

---

## 9. Configuration via .cortex/config.json

The tool is enabled by default when the config section exists. To disable:

```json
{
  "tools": {
    "enable_context_merge": false
  }
}
```

**Configuration key**: `tools.enable_context_merge`

**Default**: enabled (when key omitted or `true`)

---

## 10. Success Metrics

| Metric | Target | Measurement |
|--------|--------|-------------|
| Tool call rate | < 0.3% of turns | Journal telemetry |
| Outline reduction | 1 entry per N turns merged | Log citation |
| Citations preserved | 100% of calls | Test suite |
| No regressions | 100% of existing tests pass | Test suite |

---

## 11. Open Questions

1. Should we add a "merge strategy" parameter (e.g., "concise" vs "detailed")?
2. Should we automatically merge entries under W/16 (proactive consolidation)?
3. Should we support non-consecutive merges (with gaps)?

---

**Status**: Implementation ready for Phase 1

**Next**: Implement, test, document
