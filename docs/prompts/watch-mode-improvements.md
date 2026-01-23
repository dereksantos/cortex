# Watch Mode Improvements: Real-Time Observability

## Goal
Make `cortex watch` a useful dashboard for monitoring Cortex during development, showing:
- Active sessions and their activity
- Background cognitive processing (Think, Dream, Reflect)
- Retrieval performance and quality metrics
- Event flow in real-time

## Current State
- Interactive watch mode exists (arrow keys, expand sessions)
- TUI package created in `internal/tui/` but not fully utilized
- Session tracking in place but display is basic
- Cognitive mode status shown but minimal detail

## Proposed Improvements

### 1. Enhanced Session Panel
- Show **active session's recent queries** with retrieval stats
- Display **topic weights** learned by Think mode
- Show **cache hit rate** (how often Fast mode matches Full quality)
- Expandable session details with event timeline

### 2. Cognitive Mode Activity Feed
- **Real-time activity log** showing mode transitions
- **Think**: "Learning topic: authentication (weight: 0.7)"
- **Dream**: "Exploring: git history → found 3 insights"
- **Reflect**: "Reranking 12 candidates for query X"
- Color-coded by mode with animated spinners

### 3. Retrieval Metrics Panel
- **ABR (Agentic Benefit Ratio)** - How well Fast approaches Full
- **Latency histogram** - Track <20ms target
- **Hit/miss rates** for different query types
- **Recent queries** with their scores

### 4. Background Processing Status
- Dream queue depth and processing rate
- Think budget remaining
- Insights generated this session
- Embeddings indexed

## Files to Modify
- `cmd/cortex/commands/watch.go` - Main watch logic
- `internal/tui/` - May need new layout components (tables, grids)
- `internal/cognition/` - May need to expose more metrics
- `internal/storage/` - Query recent activity efficiently

## Suggested Approach
1. Start with a mockup of the ideal layout
2. Identify what data is available vs needs to be exposed
3. Build incrementally - one panel at a time
4. Test with real Cortex usage in parallel

## Target Layout

```
┌─────────────────────────────────────────────────────────┐
│ ● THINKING  Analyzing session patterns...               │
├─────────────────────────────────────────────────────────┤
│ Sessions (2 active)                                     │
│ ▸ 14:23  "implement auth"  [12 queries, 45 events]     │
│   15:01  "fix tests"       [3 queries, 8 events]       │
├─────────────────────────────────────────────────────────┤
│ Retrieval                    │ Background               │
│ Queries: 15   ABR: 0.92     │ Dream: idle (5 queued)   │
│ Avg latency: 23ms           │ Think: active (budget: 3) │
│ Cache hits: 73%             │ Insights: +2 this session │
├─────────────────────────────────────────────────────────┤
│ Activity Feed                                           │
│ 15:03:42 [reflex] Query: "how does auth work?" → 5 hits│
│ 15:03:41 [think]  Topic weight: auth → 0.8             │
│ 15:03:38 [dream]  Processed git commit: "add login"    │
└─────────────────────────────────────────────────────────┘
```

## Key Metrics to Surface

| Metric | Source | Why It Matters |
|--------|--------|----------------|
| ABR | Retrieval stats | Shows if Think is helping Fast mode |
| Query latency | Reflex timing | Track <20ms target |
| Cache hit rate | Think cache | Efficiency of pre-computation |
| Topic weights | SessionContext | What Think has learned |
| Dream queue | Dream state | Background processing backlog |
| Insights/session | Storage | Value being captured |

## Data Availability Checklist

- [ ] Session list with metadata - `storage.GetRecentSessions()`
- [ ] Query history per session - needs new query
- [ ] Topic weights - `Think.SessionContext().TopicWeights`
- [ ] ABR metrics - `RetrievalStats` in context dir
- [ ] Dream queue depth - `Dream` state (may need to expose)
- [ ] Activity feed - needs event subscription or polling

## Notes
- Consider adding `--follow` mode that tails new events
- May want `--json` streaming output for integration with other tools
- Balance information density vs readability
