# Forever Session — Design for Continuous Context Compression

## The Question

Can a single `cmd/loop` session run indefinitely — days, weeks, months — while the model always has a bounded, high-signal context window?

**Short answer: Yes, but not with the current compaction strategy alone.** The existing `Compact` is a blunt instrument: it studies the *entire* transcript and replaces it with one digest. That works for a few hundred turns, but as the transcript grows to thousands of turns, the compaction call itself becomes expensive (large study input → slow → expensive), and the single digest loses fidelity — it becomes a "wall of text" summary that the model can't navigate.

The forever session requires a **multi-layered, incremental compression pipeline** that keeps the live context window small while preserving the ability to recover detail on demand.

---

## What "Forever" Means

A forever session has these properties:

1. **Bounded live context** — the model never sees more than ~25% of its window in core conversation
2. **Recoverable detail** — the model can "zoom in" on any past turn via study
3. **No compaction explosion** — compaction calls stay fast regardless of session age
4. **No fidelity decay** — the model doesn't gradually forget things across compactions
5. **Resumable** — the session survives restarts without losing state

The current harness already has 1, 2, and 5. It fails on 3 and 4 as sessions grow.

---

## Why Current Compaction Fails at Scale

### The single-digest problem

```
Current:  [system] + [one big digest of everything]
```

After 500 turns, the transcript is ~500 JSONL entries. The study engine studies all 500 turns in one pass. The digest is a dense wall of text covering:

- Every file ever edited (even ones no longer relevant)
- Every command ever run (even ones that succeeded on the first try)
- Every decision ever made (even ones superseded later)

The model gets this wall of text as a user message. It's hard to parse, hard to act on, and hard to remember. Each subsequent compaction studies a digest-of-digest, which compounds the loss.

### The compaction cost problem

The compaction call itself grows linearly with transcript size. A 10,000-turn session means a study call over a massive JSONL file — slow, expensive, and at risk of the study model itself overflowing.

---

## The Design: Multi-Layered Context Pipeline

### Core Insight

Don't replace the conversation with one digest. **Maintain multiple layers of context, each with a different granularity and recency bias, and compose them at turn start.**

```
┌─────────────────────────────────────────────────┐
│  LIVE CONTEXT WINDOW (model sees this)          │
│  ┌───────────────────────────────────────────┐  │
│  │ System Prompt (fixed)                     │  │
│  │ Recent Turns (last N, full detail)        │  │
│  │ ────────────────────────────────────────  │  │
│  │ Layer 1: Recent Summary (last ~50 turns)  │  │
│  │ Layer 2: Session Summary (older, compressed)│ │
│  │ ────────────────────────────────────────  │  │
│  │ Layer 3: Project Knowledge (persistent)   │  │
│  │ Layer 4: Retrieval (on-demand)            │  │
│  └───────────────────────────────────────────┘  │
└─────────────────────────────────────────────────┘
```

### Layer Definitions

#### Layer 0: System Prompt (fixed)
Already exists. Base prompt + AGENTS.md. Never changes during the session.

#### Layer 1: Recent Turns (full detail, last N turns)
The most recent N turns (configurable, default ~10-15) are kept in full message form in `cs.Request.Messages`. This is the "working memory" — the model needs full detail here because it's actively working on these items.

**Why this matters:** The current harness keeps *all* turns in Messages until compaction fires. With a 128k window and ~200 tokens/turn average, that's ~640 turns before compaction. But the model only needs full detail for the last ~15. The rest can be summarized.

#### Layer 2: Recent Summary (compressed, last ~50-100 turns)
A compact summary of the most recent batch of turns that have been compacted out of the live window. This is a **rolling summary** — each compaction produces a summary that covers a bounded window of turns, not the entire transcript.

**Key difference from current Compact:** Instead of studying the whole transcript, study only the last K turns (e.g., 50). The result is a focused summary that replaces those K turns in the live window. Older summaries are preserved as Layer 3.

#### Layer 3: Session Summary (persistent, growing)
A single "session state" message that captures the high-level state of the session: what's being built, what decisions have been made, what files are active, what's unresolved. This is updated incrementally — each compaction of Layer 2 merges its result into the Session Summary rather than replacing it.

**This is the key innovation:** The Session Summary is a single message that grows slowly (via merge, not replace). It's the "long-term memory" of the session. It never explodes because it's bounded by design — each compaction merges new info into it, dropping stale items.

#### Layer 4: Project Knowledge (persistent, cross-session)
Already partially exists via the retrieval/capture system. Tier 1 captures + Tier 2 distilled insights live in `.cortex/` and are retrieved on-demand. This layer is **not** in the context window — it's fetched at turn start via `cs.retrieve()`.

**Enhancement:** Add a "session manifest" — a small JSON file that records the session's high-level state, active files, and unresolved items. This is loaded at session start and injected as Layer 3, so the model immediately knows what it was working on before the restart.

---

## Architecture

### New Types

```go
// Layer2Summary is a compressed summary of a bounded window of turns.
type Layer2Summary struct {
    TurnRange  [2]int   // [startTurn, endTurn]
    Summary    string   // the compressed summary text
    TurnCount  int      // how many turns this covers
    Timestamp  time.Time
}

// SessionState is the persistent session summary (Layer 3).
type SessionState struct {
    Version     int       // schema version for forward compatibility
    Task        string    // the user's overarching task/intent
    Decisions   []string  // key decisions made (with rationale)
    ActiveFiles []string  // files currently being worked on
    Unresolved  []string  // open items, questions, blockers
    LastTurn    int       // turn number when this was last updated
    UpdatedAt   time.Time
}
```

### New Methods on CortexSession

```go
// CompactRecent compacts the last K turns into a summary and merges it
// into the session state. The compacted turns are removed from the live
// window; the summary replaces them.
func (cs *CortexSession) CompactRecent(ctx context.Context, k int) error

// MergeIntoSessionState merges a Layer2Summary into the SessionState,
// updating task, decisions, active files, and unresolved items.
// Stale items are pruned based on recency and relevance signals.
func (cs *CortexSession) MergeIntoSessionState(summary *Layer2Summary) error

// BuildLiveContext assembles the messages to send for the next turn:
// system prompt + recent turns + session state + ephemeral retrieval.
func (cs *CortexSession) BuildLiveContext() []Message

// SaveSessionState persists the SessionState to .cortex/session_state.json
// so it survives restarts.
func (cs *CortexSession) SaveSessionState() error

// LoadSessionState loads the SessionState from disk at startup.
func (cs *CortexSession) LoadSessionState() (*SessionState, error)
```

### The Compaction Flow

```
Before compaction:
  Messages = [system, user1, assistant1, tool1, user2, assistant2, ..., userN, assistantN]
  where N is large (e.g., 200)

CompactRecent(k=50):
  1. Take the last 50 turns (user151..assistant200)
  2. Write them to a temp transcript file
  3. Study that file with compactGoal focused on "recent work"
  4. Parse the study result into a Layer2Summary
  5. Merge the Layer2Summary into SessionState (MergeIntoSessionState)
  6. Replace those 50 turns in Messages with the SessionState summary
  7. Save SessionState to disk

After compaction:
  Messages = [system, user1..assistant150, sessionStateSummary]
  where sessionStateSummary is a single message containing the merged state
```

### The Merge Strategy (Preventing Fidelity Decay)

This is the hardest part. How do you merge a new summary into an existing SessionState without losing information?

**Principles:**

1. **Task evolution:** The task field tracks the *current* task. If the user changes direction, the new task replaces the old. If the user is still working on the same thing, the task description is updated with new details.

2. **Decision deduplication:** Decisions are deduplicated by content similarity (already partially implemented via `isDuplicateInsight`). New decisions that restate old ones are dropped.

3. **Active file tracking:** Files are marked "active" when edited in the recent window. Files not touched in the last M turns are demoted to "historical." This prevents the active files list from growing unbounded.

4. **Unresolved item lifecycle:** Unresolved items are checked against recent turns. If a previously unresolved item was addressed (the model's response indicates completion), it's removed. New unresolved items are added.

5. **Recency decay:** Older information is kept but with lower priority. The SessionState message itself is bounded — if it grows beyond a threshold, the oldest entries are pruned (with a "see transcript for details" note).

**Implementation:** The merge is done by the study model itself. Instead of a mechanical merge, we send the current SessionState + the new Layer2Summary to the study model with a prompt like:

> "Merge these two summaries into one. Keep all unique information. Resolve conflicts (if the task changed, use the newer version). Prune stale items (files no longer active, decisions superseded). Output a single consolidated summary."

This is a single bounded study call regardless of session age.

---

## Turn Start: Building the Context Window

```go
func (cs *CortexSession) BuildLiveContext() []Message {
    msgs := []Message{cs.Request.Messages[0]} // system prompt
    
    // Add recent turns (full detail)
    recent := cs.Request.Messages[1:]
    if len(recent) > cs.recentTurnLimit {
        recent = recent[len(recent)-cs.recentTurnLimit:]
    }
    msgs = append(msgs, recent...)
    
    // Add session state if we have compacted turns
    if cs.SessionState != nil && cs.SessionState.Version > 0 {
        msgs = append(msgs, Message{
            Role:    RoleUser,
            Content: "[Session state — summary of prior work. Continue from this state.]\n\n" + cs.SessionState.Format(),
        })
    }
    
    return msgs
}
```

At turn start, the ephemeral retrieval (Layer 4) is still fetched and folded into the system message as `EphemeralSystem`. The session state (Layer 3) is a persistent user message in the history.

---

## Compaction Triggers

The current harness compacts at 80% window fill. The forever session needs **multiple triggers**:

1. **Window fill trigger (existing):** When `contextRatio() >= compactThreshold`, compact the most recent K turns. This is the safety net.

2. **Recency trigger (new):** After every N turns (configurable, default 20), run a lightweight compaction of the most recent batch. This keeps the rolling summary fresh and prevents the session state from becoming stale.

3. **Session state size trigger (new):** When the SessionState message grows beyond a token budget (e.g., 2000 tokens), trigger a merge+prune cycle.

4. **Manual trigger (existing):** `/compact` still works, but now compacts the recent window rather than the entire transcript.

---

## Resume / Restart

When the session is resumed:

1. Load the SessionState from `.cortex/session_state.json`
2. Load the recent turns from the latest transcript
3. If the transcript was compacted, the recent turns are shorter (the compacted turns are replaced by the session state summary)
4. The session state is injected as a user message in the history

The session manifest (`.cortex/session_state.json`) contains:
```json
{
    "version": 1,
    "task": "Implementing authentication with JWT...",
    "decisions": ["Use pgx, not database/sql", "JWT tokens expire in 24h"],
    "activeFiles": ["auth/jwt.go", "auth/middleware.go"],
    "unresolved": ["Need to add token refresh endpoint"],
    "lastTurn": 847,
    "updatedAt": "2026-01-15T10:30:00Z"
}
```

This is small, fast to load, and gives the model immediate context about what it was doing.

---

## Cost Analysis

### Current approach (single digest of entire transcript)

| Session Age | Transcript Size | Compaction Cost |
|-------------|----------------|-----------------|
| 100 turns   | ~50KB          | ~1 study call   |
| 500 turns   | ~250KB         | ~1 study call (slow) |
| 2000 turns  | ~1MB           | ~1 study call (very slow, may overflow study model) |

### Forever approach (bounded recent compaction)

| Session Age | Recent Window | Compaction Cost |
|-------------|--------------|-----------------|
| 100 turns   | 50 turns     | ~1 study call   |
| 500 turns   | 50 turns     | ~1 study call   |
| 2000 turns  | 50 turns     | ~1 study call   |
| 10000 turns | 50 turns     | ~1 study call   |

**The compaction cost is constant regardless of session age.** The SessionState merge is also a single bounded study call.

### Token cost per turn

| Component | Tokens (approx) |
|-----------|----------------|
| System prompt | 500 |
| Recent turns (15) | 3000 |
| Session state | 1000 |
| Retrieval (ephemeral) | 500 |
| **Total** | **~5000** |

This is well within even a 32k window, leaving 85% for the model's response and tool calls.

---

## What This Enables

1. **True forever sessions** — the session can run indefinitely without degradation
2. **Faster compaction** — compaction is always a bounded study call, never grows with session age
3. **Better model comprehension** — the model sees a structured session state, not a wall of text
4. **Cross-session continuity** — the session state persists across restarts
5. **Retrieval synergy** — the session state tells the model what to look for; retrieval finds the details

---

## What This Does NOT Do

1. **It does not replace retrieval.** Retrieval (Layer 4) still handles on-demand detail lookup. The session state is a summary; retrieval is the drill-down.

2. **It does not eliminate the need for the transcript.** The full transcript is still persisted (for debugging, resume, and study). The session state is a compressed view.

3. **It does not solve the "model forgets" problem entirely.** The model can still lose track of subtle details across compactions. But the structured session state + retrieval gives it much better recovery than a single digest.

4. **It does not change the inner tool loop.** The Resolve method, tool calls, and all existing mechanics remain unchanged. This is purely a context management layer.

---

## Implementation Phases

### Phase 1: Session State (minimal viable)
- Add `SessionState` struct with Task, Decisions, ActiveFiles, Unresolved
- Add `SaveSessionState` / `LoadSessionState` (JSON file I/O)
- Add `/compact` that produces a session state update (not a full transcript replacement)
- Inject session state as a user message at turn start

### Phase 2: Rolling Compaction
- Add `CompactRecent(k int)` that studies only the last K turns
- Replace compacted turns with a summary message
- Merge summary into session state
- Add recency trigger (compact every N turns)

### Phase 3: Smart Merge
- Implement the study-model-based merge of summaries into session state
- Add staleness detection (prune old decisions, inactive files)
- Add session state size trigger

### Phase 4: Resume Integration
- Load session state at startup
- Inject into history on resume
- Handle session state migration across schema versions

---

## Risks and Mitigations

| Risk | Mitigation |
|------|-----------|
| Model produces poor summaries | The study engine already handles this well (bounded, cited). The merge prompt can include examples. |
| Session state grows unbounded | Bounded by design — each merge prunes stale items. Hard cap on message size. |
| Compaction loses critical detail | The full transcript is preserved. The model can study any past turn via the study tool. |
| Merge introduces contradictions | The merge prompt explicitly asks the model to resolve conflicts. The session state version field tracks schema changes. |
| Resume with mismatched session state | Version field + graceful degradation (if session state is missing, start fresh). |

---

## Comparison to Existing Approaches

| Approach | Bounded Context | Recoverable Detail | Constant Compaction Cost | Resumable |
|----------|----------------|-------------------|------------------------|-----------|
| Current (single digest) | ✅ | ❌ (digest is opaque) | ❌ (grows with age) | ✅ |
| Forever (this design) | ✅ | ✅ (study any turn) | ✅ (bounded window) | ✅ |
| Naive (no compaction) | ❌ | ✅ | N/A | ✅ |

---

## Conclusion

A forever session is possible with this design. The key insight is that **compaction should be incremental and bounded, not wholesale and unbounded.** By maintaining multiple layers of context — recent turns (full detail), recent summary (compressed), session state (persistent), and retrieval (on-demand) — the model always has a small, structured, high-signal context window while retaining access to full detail on demand.

The existing harness already has most of the building blocks: the study engine, the transcript system, the retrieval/capture pipeline, and the compaction mechanism. The forever session design reorganizes these into a multi-layered pipeline rather than a single compaction event.
