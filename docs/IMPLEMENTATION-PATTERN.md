# Context Window Modification Tools - Implementation Guide

This document describes how to add context window modification tools using the clean config-based enable/disable pattern.

## 1. Tool Configuration

### Add Config Keys to `cmd/loop/config.go`

```go
type ToolConfig struct {
    AllowDelete *bool  `json:"allow_delete"`
    DeleteRoot  string `json:"delete_root"`

    // Context window modification tools
    EnableContextSummarize     *bool `json:"enable_context_summarize"`
    EnableContextEvict         *bool `json:"enable_context_evict"`
    EnableContextMerge         *bool `json:"enable_context_merge"`
    EnableContextReorder       *bool `json:"enable_context_reorder"`
    EnableContextAdjustWatermarks *bool `json:"enable_context_adjust_watermarks"`
}
```

### Example `.cortex/config.json`

```json
{
  "tools": {
    "enable_context_summarize": true,
    "enable_context_evict": true,
    "enable_context_merge": true,
    "enable_context_reorder": true,
    "enable_context_adjust_watermarks": true
  }
}
```

## 2. Tool Implementation Pattern

### Create `internal/tools/context_tools.go`

```go
package tools

import (
    "context"
    "fmt"
)

const (
    FunctionContextSummarize     = "context_summarize"
    FunctionContextEvict         = "context_evict"
    FunctionContextMerge         = "context_merge"
    FunctionContextReorder       = "context_reorder"
    FunctionContextAdjustWatermarks = "context_adjust_watermarks"
)

// contextSummarize compresses demoted context.
// Config check happens at dispatcher level (Execute), not here.
func contextSummarize(tc ToolCall, deps ToolDeps) (string, error) {
    citation, err := tc.StringArg("citation")
    if err != nil {
        return "", fmt.Errorf("citation is required: %w", err)
    }
    
    // Implementation here
    return "compressed context", nil
}

// contextEvict removes an outline entry.
// Config check happens at dispatcher level.
func contextEvict(tc ToolCall, deps ToolDeps) (string, error) {
    citation, err := tc.StringArg("citation")
    if err != nil {
        return "", fmt.Errorf("citation is required: %w", err)
    }
    
    // Implementation here
    return "evicted entry", nil
}

// contextMerge merges consecutive turns.
// Config check happens at dispatcher level.
func contextMerge(tc ToolCall, deps ToolDeps) (string, error) {
    startCitation, err := tc.StringArg("range_start")
    if err != nil {
        return "", fmt.Errorf("range_start is required: %w", err)
    }
    
    endCitation, err := tc.StringArg("range_end")
    if err != nil {
        return "", fmt.Errorf("range_end is required: %w", err)
    }
    
    // Implementation here
    return "merged entries", nil
}

// contextReorder reorders hydrated tail.
// Config check happens at dispatcher level.
func contextReorder(tc ToolCall, deps ToolDeps) (string, error) {
    by, err := tc.StringArg("by")
    if err != nil {
        return "", fmt.Errorf("by is required: %w", err)
    }
    
    // Implementation here
    return "reordered tail", nil
}

// contextAdjustWatermarks adjusts working set watermarks.
// Config check happens at dispatcher level.
func contextAdjustWatermarks(tc ToolCall, deps ToolDeps) (string, error) {
    highDelta, _ := tc.IntArg("high_delta")
    lowDelta, _ := tc.IntArg("low_delta")
    
    // Implementation here
    return "adjusted watermarks", nil
}
```

---

## 4. Tool Validation

### Add Validator Interface

Tools can implement dynamic validation beyond config enable/disable:

```go
type Validator interface {
    ValidateToolCall(tc ToolCall) (bool, string)
}
```

**Example**: Watermark deltas must be within ±W/4:

```go
func (cs *CortexSession) ValidateToolCall(tc ToolCall) (bool, string) {
    switch tc.Function.Name {
    case "context_adjust_watermarks":
        w := cs.windowSize()
        bound := w / 4
        if highDelta, _ := tc.IntArg("high_delta"); highDelta != 0 {
            if highDelta < -bound || highDelta > bound {
                return false, fmt.Sprintf("high_delta %d is out of bounds (±%d)", highDelta, bound)
            }
        }
        if lowDelta, _ := tc.IntArg("low_delta"); lowDelta != 0 {
            if lowDelta < -bound || lowDelta > bound {
                return false, fmt.Sprintf("low_delta %d is out of bounds (±%d)", lowDelta, bound)
            }
        }
    }
    return true, ""
}
```

### Update Execute Dispatcher

```go
func Execute(ctx context.Context, tc ToolCall, deps ToolDeps) (string, error) {
    // ... existing code ...
    
    // Check if tool is enabled via config
    if !deps.IsToolEnabled(tc.Function.Name) {
        return fmt.Sprintf("%s is disabled in .cortex/config.json", tc.Function.Name), nil
    }
    
    // Validate tool call (dynamic checks)
    if deps != nil {
        if ok, msg := deps.ValidateToolCall(tc); !ok {
            return msg, nil
        }
    }
    
    // ... switch on tool name ...
}
```

---

## 5. Unit Tests

```go
var ContextSummarizeTool = newTool(FunctionContextSummarize,
    "Compresses a demoted turn into a compact digest using the existing summarizer interface.",
    objectSchema(map[string]any{
        "citation": stringProp("The citation to summarize, e.g., @session/20260701-143210#t1"),
        "goal":     stringProp("Optional: What should the summary focus on?"),
        "budget":   map[string]any{"type": "integer", "description": "Token budget for the summary (default: half window)"},
    }, "citation"))

var ContextEvictTool = newTool(FunctionContextEvict,
    "Removes an outline entry from the working set. The entry is already demoted (in the journal), so this only affects the hydrated tail and outline.",
    objectSchema(map[string]any{
        "citation": stringProp("The citation to evict, e.g., @session/20260701-143210#t1"),
    }, "citation"))

var ContextMergeTool = newTool(FunctionContextMerge,
    "Merges consecutive demoted turns into a single outline entry. Deterministic (no LLM) and preserves citations.",
    objectSchema(map[string]any{
        "range_start": stringProp("Start citation of range, e.g., @session/20260701-143210#t1"),
        "range_end":   stringProp("End citation of range, e.g., @session/20260701-143210#t5"),
    }, "range_start", "range_end"))

var ContextReorderTool = newTool(FunctionContextReorder,
    "Reorders the hydrated tail based on the given metric. Only rearranges order—it does not evict or compress.",
    objectSchema(map[string]any{
        "by": stringProp("Metric to sort by: 'salience', 'recency', or 'task-relevance'"),
    }, "by"))

var ContextAdjustWatermarksTool = newTool(FunctionContextAdjustWatermarks,
    "Dynamically adjusts the working set watermarks by the given deltas. Bounded (±W/4) to prevent abuse.",
    objectSchema(map[string]any{
        "high_delta": map[string]any{"type": "integer", "description": "Change to high watermark (default: 0)"},
        "low_delta":  map[string]any{"type": "integer", "description": "Change to low watermark (default: 0)"},
    }, "high_delta", "low_delta"))
```

### Add Tools to the All List

```go
var All = []Tool{
    ReadFile, WriteFile, EditFile, StudyTool, OutlineTool, GrepTool, Bash, RemoveTool,
    MemoryWriteTool, MemoryReadTool, MemorySearchTool, MemoryForgetTool, RecallTool,
    ContextSummarizeTool, ContextEvictTool, ContextMergeTool, ContextReorderTool, ContextAdjustWatermarksTool,
}
```

## 3. Dispatcher Update

### Update `internal/tools/tools.go:Execute()`

```go
func Execute(ctx context.Context, tc ToolCall, deps ToolDeps) (string, error) {
    if deps == nil {
        deps = headlessDeps{}
    }

    // Check if tool is enabled via config (ONE PLACE FOR ALL TOOLS)
    if !deps.IsToolEnabled(tc.Function.Name) {
        return fmt.Sprintf("%s is disabled in .cortex/config.json", tc.Function.Name), nil
    }

    name := tc.Function.Name
    switch name {
    case FunctionReadFile:
        return readFile(tc, deps)
    case FunctionWriteFile:
        return writeFile(tc)
    case FunctionEditFile:
        return editFile(tc)
    case FunctionStudy:
        return study(ctx, tc, deps)
    case FunctionOutline:
        return outlineTool(tc)
    case FunctionGrep:
        return grep(ctx, tc)
    case FunctionBash:
        return bash(ctx, tc, deps)
    case FunctionRemove:
        return removePath(tc, deps)
    case FunctionMemoryWrite:
        return memoryWrite(tc, deps)
    case FunctionMemoryRead:
        return memoryRead(tc, deps)
    case FunctionMemorySearch:
        return memorySearch(tc, deps)
    case FunctionMemoryForget:
        return memoryForget(tc, deps)
    case FunctionRecall:
        return recall(tc, deps)
    // NEW: Context window tools
    case FunctionContextSummarize:
        return contextSummarize(tc, deps)
    case FunctionContextEvict:
        return contextEvict(tc, deps)
    case FunctionContextMerge:
        return contextMerge(tc, deps)
    case FunctionContextReorder:
        return contextReorder(tc, deps)
    case FunctionContextAdjustWatermarks:
        return contextAdjustWatermarks(tc, deps)
    }
    return "", fmt.Errorf(`no available tools matching name "%s"`, name)
}
```

## 4. Session Extension

### Add `IsToolEnabled()` to `CortexSession` in `cmd/loop/session_core.go`

```go
// IsToolEnabled reports whether a context window tool is enabled via config.
func (cs *CortexSession) IsToolEnabled(toolName string) bool {
    if cs.Config == nil {
        return true // default: all tools enabled
    }
    t := &cs.Config.Tools
    if t.EnableContextSummarize == nil &&
        t.EnableContextEvict == nil &&
        t.EnableContextMerge == nil &&
        t.EnableContextReorder == nil &&
        t.EnableContextAdjustWatermarks == nil &&
        t.AllowDelete == nil &&
        t.DeleteRoot == "" {
        return true // default: all tools enabled
    }
    switch toolName {
    case "context_summarize":
        return t.EnableContextSummarize == nil || *t.EnableContextSummarize
    case "context_evict":
        return t.EnableContextEvict == nil || *t.EnableContextEvict
    case "context_merge":
        return t.EnableContextMerge == nil || *t.EnableContextMerge
    case "context_reorder":
        return t.EnableContextReorder == nil || *t.EnableContextReorder
    case "context_adjust_watermarks":
        return t.EnableContextAdjustWatermarks == nil || *t.EnableContextAdjustWatermarks
    }
    return true // unknown tools enabled by default
}
```

## 5. Interface Assertion

### Add ConfigProvider assertion in `cmd/loop/main.go`

```go
var (
    _ tools.ToolDeps       = (*CortexSession)(nil)
    _ tools.MemoryStore    = (*CortexSession)(nil)
    _ tools.Summarizer     = (*CortexSession)(nil)
    _ tools.Outliner       = (*CortexSession)(nil)
    _ tools.SubAgentRunner = (*CortexSession)(nil)
    _ tools.ShellGate      = (*CortexSession)(nil)
    _ tools.DeleteGate     = (*CortexSession)(nil)
    _ tools.ConfigProvider = (*CortexSession)(nil) // NEW
)
```

## 6. Key Design Principles

### ✅ Clean Pattern

1. **Config check happens ONCE** at dispatcher level in `Execute()`
2. **No duplicate checks** in individual tool implementations
3. **ToolDeps interface** extended with `ConfigProvider` method
4. **Session implements `IsToolEnabled()`** - no config imports in internal/tools
5. **Backward compatible** - missing config keys default to enabled

### ✅ Why This Pattern Works

| Benefit | Explanation |
|---------|-------------|
| Single source of truth | Config check in `Execute()` |
| Clean separation | `internal/tools` doesn't import `cmd/loop` |
| Easy to extend | New tools follow same pattern |
| Testable | Easy to stub `ConfigProvider` in tests |
| Safe | Headless mode defaults to enabled |

## 7. Testing

```go
func TestContextSummarize_Disabled(t *testing.T) {
    tc := ToolCall{
        Function: FunctionCall{
            Name: FunctionContextSummarize,
            Arguments: `{"citation":"@session/test#t1"}`,
        },
    }
    
    // Mock deps that reports disabled
    deps := &mockDeps{
        isToolEnabled: false,
    }
    
    result, err := contextSummarize(tc, deps)
    if err != nil {
        t.Fatalf("contextSummarize failed: %v", err)
    }
    
    if !strings.Contains(result, "disabled in .cortex/config.json") {
        t.Errorf("expected disabled message, got: %s", result)
    }
}
```

## 8. Migration from Old Pattern

If you have old implementation plans with `checkConfig()` in each tool:

1. Remove `checkConfig()` from each tool
2. Rely on `Execute()`'s config check
3. Update dispatcher to add case statements
4. Add `IsToolEnabled()` to `CortexSession`
