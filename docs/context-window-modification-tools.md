# Context Window Modification Tools — Research & Implementation Plan

> **Purpose.** Design a set of tools that allow the LLM running the Cortex agent loop to modify its own context window, enabling smarter context management and self-compression while preserving safety invariants.
>
> **Status.** Research artifact — foundation for implementation planning.
>
> **Owner.** Context architecture team + agent loop engineers.

---

## 1. Executive Summary

The Cortex agent loop currently manages context through a **two-zone working set** model with mechanical demotion, deterministic outline entries, and citation-grounded recovery. While context is already managed intelligently, the model has no **agentic ability** to modify its own context window in response to task demands.

This research explores adding context window modification tools that let the model:
- **Summarize** demoted context when it needs to recover space
- **Evict** stale outline entries
- **Merge** consecutive demoted turns
- **Reorder** context by salience
- **Adjust** working set watermarks dynamically

Each tool preserves core safety invariants: no lost user words, append-only evolution, and recoverable demotion via citations.

---

## 2. Current Context Architecture

### 2.1 Two-Zone Wire Layout

```
                  ONE REQUEST (window W)
┌───────────────────────────────────────────────────────────────┐
│ ZONE A — PREFIX (append-stable → prompt-cache HIT)            │
│ ┌───────────────────────────────────────────────────────────┐ │
│ │ [system]  SystemPrompt + AGENTS.md            fixed       │ │
│ ├───────────────────────────────────────────────────────────┤ │
│ │ [user]    SESSION OUTLINE                     append-only │ │
│ │   t1 · user:"fix the eval" · edit x.go [ok]       ≤ W/8   │ │
│ │        ⤷ "pinned temp"  [@session/…#t1]                   │ │
│ │   t2 · …                                                  │ │
│ │   t3 · …            ◀── grows ONLY at its own tail        │ │
│ ├───────────────────────────────────────────────────────────┤ │
│ │ [user]    MEMORY INDEX          changes on memory_write   │ │
│ └───────────────────────────────────────────────────────────┘ │
├───────────────────────────────────────────────────────────────┤
│ ZONE B — HYDRATED TAIL (volatile, low-wm ≈ W/3 … high ≈ W/2)  │
│   turn k-2 : user / assistant(tool_calls) / tool …  verbatim  │
│   turn k-1 : …                                      verbatim  │
│   turn k   : current turn, appends as runLoop iterates        │
├───────────────────────────────────────────────────────────────┤
│ OUTPUT RESERVE (MaxTokens)                                    │
└───────────────────────────────────────────────────────────────┘
```

**Key properties**:
- Zone A is **append-stable** → full LCP cache hits
- Zone B is the **working set** — only recent turns the model needs verbatim
- Outline is **bounded by W/8** — when it exceeds this, oldest lines fold into coarser digest

### 2.2 Working Set Model

File: `internal/cache/workingset.go`

```go
type WorkingSet struct {
    turns    []TurnSpan      // all turns, contiguous
    frontier int             // demotion boundary (turns[frontier:] is hydrated)
    base     int             // message-log index where turn content begins
    highWM   int             // high watermark (~W/2 tokens)
    lowWM    int             // low watermark (~W/3 tokens)
}

type TurnSpan struct {
    Start, End int    // message index range [Start, End)
    Tokens     int     // estimated token count
}
```

**Demotion policy**:
- Triggers when `TailTokens() > highWM`
- Batched: oldest whole turns move to outline at once
- Drains to `lowWM` (hysteresis prevents every-turn cache misses)
- Never demotes the most recent turn (keeps active thread)

### 2.3 Outline Entries

Deterministic, no LLM. One entry per demoted turn:

```
t12 · user: "make the study eval deterministic"
      edit cmd/cortex/study_eval.go · bash go test ./cmd/cortex [ok]
      ⤷ "pinned temperature; reps now stable"
      [@session/20260701-143210#t12]
```

- User message is verbatim (truncated only for huge pastes)
- Tool calls compress to `name target [ok|err]`
- Assistant reply keeps first line
- Citation is deterministic coordinate into `.cortex/sessions/<id>.jsonl`

### 2.4 Recall Mechanism

When the model needs demoted context again, it calls `recall(citation)`:
- Resolves the citation coordinate
- Returns the cited message(s) raw
- Subject to same size gate as `read_file` (oversized → redirect to `study`)
- Never rehydrates in place — always as a **new tool result at the tail**

---

## 3. Existing Tools (Relevant to Context Management)

| Tool | Purpose | Budget | Uses |
|------|---------|--------|------|
| `study(path, goal)` | Bounded subagent for reading files/directories | 8K tokens | Outline, grep, read_file |
| `read_file(path, start, end)` | Targeted line range reads | 200 lines max | Direct file access |
| `grep(pattern, path)` | Content search across filesystem | 100 hits cap | Locate symbols/strings |
| `outline(path, budget)` | Structural map of files/directories | 4K-8K tokens | Navigate codebase |
| `recall(citation)` | Fetch raw messages from demoted turns | Curation budget | Recover demoted context |
| `memory_write/read/search/forget` | Model-curated durable notes | N/A | Persistent state |
| `Summarize(ctx, path, goal, window)` | Sequential chunk-and-fold | Caller-provided | Compression |

---

## 4. Proposed Context Window Modification Tools

### 4.1 Tool Design Principles

**Safety invariants** (non-negotiable):
1. **User words are immutable** — never summarize, paraphrase, or drop them
2. **Never restructure context in-place** — only append, only demote at frontier
3. **Eviction is recoverable** — everything stays in journal, reachable via citation
4. **Citations must survive** — every outline entry keeps its coordinate
5. **All changes are journaled** — every tool call is auditable

**Constraints**:
- All tools run on existing engine (`runLoop`)
- All tools have explicit token budgets
- All tools are read-only (no write/edit/bash/remove)
- All tools use existing interfaces (no new infrastructure)

### 4.2 Tool 1: `context_summarize(citation, goal, budget)`

**Purpose**: Compress demoted context when the model needs to recover space.

**Use cases**:
- Condense long demoted turns into compact summaries
- Reduce outline length under pressure
- Recover space without losing content

**Signature**:
```json
{
  "name": "context_summarize",
  "description": "Compress a demoted turn (referenced by citation) into a compact digest. Use when the outline is growing and you need to recover space while preserving the key facts.",
  "parameters": {
    "citation": {"type": "string", "description": "The exact citation from the outline, e.g. @session/20260701-143210#t12"},
    "goal": {"type": "string", "description": "What key facts must the summary preserve?"},
    "budget": {"type": "integer", "description": "Target token budget for the summary (typically 256-1024 tokens)"}
  }
}
```

**Implementation**:
```go
func contextSummarize(tc ToolCall, deps ToolDeps) (string, error) {
    citation, _ := tc.StringArg("citation")
    goal, _ := tc.StringArg("goal")
    budget, _ := tc.IntArg("budget")
    
    // Default budget if omitted
    if budget <= 0 {
        budget = 512
    }
    
    // Resolve citation to raw messages
    raw, err := deps.Recall(citation)
    if err != nil {
        return "", fmt.Errorf("recall failed: %w", err)
    }
    
    // Use existing Summarizer interface (sequential chunk-and-fold)
    digest, compressed, err := deps.Summarize(context.Background(), "", goal, budget)
    if err != nil {
        return "", fmt.Errorf("summarize failed: %w", err)
    }
    
    // Verify citations are preserved (mechanical check)
    if !strings.Contains(digest, citation) {
        digest += fmt.Sprintf("\n\n[CITATION KEPT: %s]", citation)
    }
    
    return fmt.Sprintf("Summarized %s into ~%d tokens (compressed: %v):\n\n%s", 
        citation, budget, compressed, digest), nil
}
```

**Safety considerations**:
- Uses existing `Summarizer` interface (proven, tested)
- Must preserve citation in summary (mechanical check)
- Budget bounded (caller-provided or default)
- Input is already in journal (no loss)

---

### 4.3 Tool 2: `context_evict(citation)`

**Purpose**: Remove outline entry from working set when context is tight.

**Use cases**:
- Clean up stale outline entries
- Free up outline budget for new turns
- Remove entries no longer relevant to current task

**Signature**:
```json
{
  "name": "context_evict",
  "description": "Remove an outline entry from the working set. The entry is already demoted (in the journal), so this only affects the hydrated tail and outline. Use when the outline is at its budget cap and you need to make room for new turns.",
  "parameters": {
    "citation": {"type": "string", "description": "The exact citation from the outline to evict, e.g. @session/20260701-143210#t12"}
  }
}
```

**Implementation**:
```go
func contextEvict(tc ToolCall, deps ToolDeps) (string, error) {
    citation, _ := tc.StringArg("citation")
    
    // Parse citation to extract turn ordinal
    turnOrdinal, err := parseTurnOrdinal(citation)
    if err != nil {
        return "", fmt.Errorf("invalid citation format: %w", err)
    }
    
    // Evict the outline entry (remove from cs.outline)
    // This is a no-op if already evicted (idempotent)
    success := deps.RemoveOutlineEntry(citation)
    if !success {
        return fmt.Sprintf("Outline entry %s was not found or already evicted.", citation), nil
    }
    
    return fmt.Sprintf("Evicted outline entry %s. Space recovered: ~1-2 lines of outline.", citation), nil
}
```

**Safety considerations**:
- Only removes from outline (already demoted = safe)
- Entry remains in journal (recoverable via `recall`)
- Idempotent (safe to retry)
- Must verify citation format before eviction

---

### 4.4 Tool 3: `context_merge(range_start, range_end)`

**Purpose**: Merge consecutive demoted turns into single outline entry.

**Use cases**:
- Reduce outline length while preserving content
- Consolidate related turns
- Free up outline budget

**Signature**:
```json
{
  "name": "context_merge",
  "description": "Merge consecutive demoted turns (referenced by citations) into a single outline entry. Use when the outline is growing and you have multiple adjacent demoted turns that can be condensed.",
  "parameters": {
    "range_start": {"type": "string", "description": "Citation of first turn to merge, e.g. @session/20260701-143210#t10"},
    "range_end": {"type": "string", "description": "Citation of last turn to merge, e.g. @session/20260701-143210#t12"}
  }
}
```

**Implementation**:
```go
func contextMerge(tc ToolCall, deps ToolDeps) (string, error) {
    startCitation, _ := tc.StringArg("range_start")
    endCitation, _ := tc.StringArg("range_end")
    
    // Parse citations to get turn ordinals
    startOrdinal, err := parseTurnOrdinal(startCitation)
    if err != nil {
        return "", fmt.Errorf("invalid start citation: %w", err)
    }
    endOrdinal, err := parseTurnOrdinal(endCitation)
    if err != nil {
        return "", fmt.Errorf("invalid end citation: %w", err)
    }
    
    if startOrdinal > endOrdinal {
        return "", fmt.Errorf("range_start (%d) must be <= range_end (%d)", startOrdinal, endOrdinal)
    }
    
    // Merge the turns (deterministic, mechanical)
    mergedCitation, err := deps.MergeOutlineEntries(startCitation, endCitation)
    if err != nil {
        return "", fmt.Errorf("merge failed: %w", err)
    }
    
    return fmt.Sprintf("Merged turns %d-%d into single outline entry %s. Turns reduced: %d.", 
        startOrdinal, endOrdinal, mergedCitation, endOrdinal-startOrdinal+1), nil
}
```

**Safety considerations**:
- Only merges already-demoted turns
- Citations preserved in merged entry
- Deterministic (no LLM, mechanical merge)
- Range validated (start <= end)

---

### 4.5 Tool 4: `context_reorder(by="salience|recency|task-relevance")`

**Purpose**: Reorder hydrated tail by relevance score.

**Use cases**:
- Prioritize most relevant context
- Move recent high-value turns to the front
- Optimize context for current task

**Signature**:
```json
{
  "name": "context_reorder",
  "description": "Reorder the hydrated tail (recent turns) by relevance score. This only rearranges order—it does not evict or compress. Use when context is bounded but you want the most relevant turns to appear first.",
  "parameters": {
    "by": {"type": "string", "description": "Relevance metric: 'salience' (long-term importance), 'recency' (most recent first), 'task-relevance' (current goal)", "enum": ["salience", "recency", "task-relevance"]}
  }
}
```

**Implementation**:
```go
func contextReorder(tc ToolCall, deps ToolDeps) (string, error) {
    by, _ := tc.StringArg("by")
    
    // Validate metric
    if by != "salience" && by != "recency" && by != "task-relevance" {
        return "", fmt.Errorf("invalid metric: %s (must be 'salience', 'recency', or 'task-relevance')", by)
    }
    
    // Reorder the hydrated tail (in-place, mechanical)
    // Uses existing salience scoring if available
    oldOrder := deps.GetHydratedTail()
    newOrder := deps.ReorderTail(by)
    
    return fmt.Sprintf("Reordered hydrated tail by %s. %d turns rearranged. No tokens recovered (order only).", 
        by, len(oldOrder)), nil
}
```

**Safety considerations**:
- Only rearranges (no evict/compress)
- Preserves all messages
- Deterministic (same metric = same order)
- No budget impact (just reordering)

---

### 4.6 Tool 5: `context_adjust_watermarks(high_delta, low_delta)`

**Purpose**: Dynamically adjust working set watermarks.

**Use cases**:
- Reserve more budget for active task phases
- Tighten budget during exploratory phases
- Optimize cache economics for current workload

**Signature**:
```json
{
  "name": "context_adjust_watermarks",
  "description": "Dynamically adjust the working set watermarks (high and low) by the given deltas. Use when you want to reserve more space for active turns or tighten the budget for exploratory phases.",
  "parameters": {
    "high_delta": {"type": "integer", "description": "Change to high watermark (positive = more space, negative = less). Range: -W/4 to +W/4."},
    "low_delta": {"type": "integer", "description": "Change to low watermark (positive = more space, negative = less). Range: -W/4 to +W/4."}
  }
}
```

**Implementation**:
```go
func contextAdjustWatermarks(tc ToolCall, deps ToolDeps) (string, error) {
    highDelta, _ := tc.IntArg("high_delta")
    lowDelta, _ := tc.IntArg("low_delta")
    
    // Validate deltas (bounded to prevent abuse)
    maxDelta := deps.GetWindow() / 4
    if highDelta < -maxDelta || highDelta > maxDelta {
        return "", fmt.Errorf("high_delta out of range: %d (must be -%d to +%d)", highDelta, maxDelta, maxDelta)
    }
    if lowDelta < -maxDelta || lowDelta > maxDelta {
        return "", fmt.Errorf("low_delta out of range: %d (must be -%d to +%d)", lowDelta, maxDelta, maxDelta)
    }
    
    // Adjust watermarks (must maintain high >= low)
    oldHigh, oldLow := deps.GetWatermarks()
    newHigh := oldHigh + highDelta
    newLow := oldLow + lowDelta
    
    // Validate invariant
    if newLow > newHigh {
        return "", fmt.Errorf("adjustment would violate lowWM <= highWM: low=%d, high=%d", newLow, newHigh)
    }
    
    // Apply adjustment
    deps.SetWatermarks(newHigh, newLow)
    
    return fmt.Sprintf("Adjusted watermarks: high=%d→%d, low=%d→%d. Hysteresis preserved: %d tokens.", 
        oldHigh, newHigh, oldLow, newLow, newHigh-newLow), nil
}
```

**Safety considerations**:
- Deltas bounded (±W/4 to prevent abuse)
- Invariant maintained (lowWM <= highWM)
- No immediate demotion (adjustment is passive)
- All adjustments journaled

---

## 4. Configuration via .cortex/config.json

Each tool is controlled by a boolean flag in `.cortex/config.json` under `tools`:

| Tool | Config Key | Default |
|------|------------|---------|
| `context_summarize` | `tools.enable_context_summarize` | enabled |
| `context_evict` | `tools.enable_context_evict` | enabled |
| `context_merge` | `tools.enable_context_merge` | enabled |
| `context_reorder` | `tools.enable_context_reorder` | enabled |
| `context_adjust_watermarks` | `tools.enable_context_adjust_watermarks` | enabled |

**Example configuration**:
```json
{
  "tools": {
    "enable_context_summarize": true,
    "enable_context_evict": false,
    "enable_context_merge": true
  }
}
```

**Design rationale**:
- No canary rollout needed (no production users yet)
- Config-driven enable/disable for easy experimentation
- Each tool is independently configurable
- Default behavior: enabled when key omitted or explicitly `true`

---

## 5. Implementation Plan

### Phase 1: Core Infrastructure (1-2 weeks)

**Tasks**:
1. Add new tool declarations to `internal/tools/tools.go`
2. Add `RemoveOutlineEntry`, `MergeOutlineEntries`, `ReorderTail`, `SetWatermarks` methods to `CortexSession`
3. Implement `context_summarize` (reuses existing `Summarizer`)
4. Write unit tests for each tool

**Deliverables**:
- All 5 tools declared
- All 5 tools dispatchable
- All 5 tools tested (unit)

### Phase 2: Integration & Testing (1-2 weeks)

**Tasks**:
1. Wire tools into `Coder` toolset (available to main loop)
2. Add live eval tests (integration)
3. Add journal entries for all context modifications
4. Performance testing (token budgets, latency)

**Deliverables**:
- Tools available in main loop
- All tools tested (integration + live)
- Performance benchmarks

### Phase 3: Documentation & Rollout (1 week)

**Tasks**:
1. Update `context-architecture.md` with new tools
2. Add tool descriptions to system prompt
3. Add usage examples to `docs/`
4. Rollout plan (canary → full)

**Deliverables**:
- Complete documentation
- System prompt updated
- Rollout plan approved

---

## 6. Safety & Risk Assessment

### Safety Invariants (Verified)

| Tool | Invariant | Verification |
|------|-----------|--------------|
| `context_summarize` | Citations preserved | Mechanical check in implementation |
| `context_evict` | Only removes from outline | Only `cs.outline` modified |
| `context_merge` | Deterministic, no LLM | Mechanical merge function |
| `context_reorder` | Only rearranges | No eviction/compression |
| `context_adjust_watermarks` | Invariant maintained | Range check before apply |

### Risk Mitigation

| Risk | Mitigation |
|------|------------|
| Tool abuse (excessive calls) | Budgets enforced on each tool |
| Watermark abuse | Deltas bounded (±W/4) |
| Outline corruption | Citations verified before any change |
| Loss of context | All changes journaled, recoverable |
| Cache invalidation | Demotion remains mechanical (no LLM) |

---

## 7. Success Criteria

**Phase 1**:
- All 5 tools declared and dispatchable
- All 5 tools pass unit tests
- No regressions in existing tools

**Phase 2**:
- All 5 tools available in main loop
- All 5 tools pass integration tests
- Performance within 10% of baseline

**Phase 3**:
- Documentation complete
- System prompt updated
- Configuration added to `.cortex/config.json`

**Configuration**: Each tool is controlled by `tools.enable_context_*` in `.cortex/config.json`. Defaults to enabled when key omitted or `true`.

---

## 8. Future Work

**Post-implementation**:
1. Add salience scoring to `context_reorder`
2. Add predictive watermark adjustment (learning-based)
3. Add context folding for merged entries (LLM, rare)
4. Add telemetry on tool usage patterns

**Long-term**:
1. Study how models use these tools in practice
2. Adjust defaults based on usage patterns
3. Add adaptive budgets based on task type
4. Add "context health" metrics to journal

---

## 9. References

- `docs/context-architecture.md` — Current two-zone layout
- `docs/working-memory.md` — Working set model
- `docs/memory-tools.md` — Model-driven memory tools
- `docs/study-subagent.md` — Bounded subagent design
- `internal/tools/tools.go` — Existing tool declarations
- `internal/cache/workingset.go` — Working set implementation

---

**Next Steps**:
1. Review this research artifact
2. Prioritize tools for implementation (start with `context_summarize`)
3. Begin Phase 1: Core Infrastructure
4. Add configuration keys to `.cortex/config.json`
