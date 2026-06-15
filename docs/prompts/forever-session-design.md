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

### The Study Engine Is the Compaction Mechanism

The study engine (`StudyLoop` + `StudyFile`) is not a tool that compaction calls — **it is the compaction mechanism.** The forever session leverages the study engine's existing capabilities:

- **Bounded sampling** — files over window/2 are sampled, not read whole (commit `65a9c72`)
- **Provenance-constrained inference** — citations must fall within sampled regions; off-sample needs become leads (commit `65a9c72`)
- **Curator-driven deepening** — the curator decides DONE/DENSIFY/TARGET, so compaction stops when the summary is sufficient (commit `5faf0a2`)
- **Coherence-unit chunk sizing** — fragments snap to format-aware boundaries (JSONL records are whole turns), preventing mid-turn splits (commit `2109fa2`)
- **Auto density mode** — derives k from window-budget / unit-size, adapting to transcript size without tuning (commit `2109fa2`)
- **Citation validation** — prevents hallucinated citations; the merge is self-correcting (commit `d7d8a94`)

### Compaction: StudyLoop, Not Custom Logic

```go
// CompactRecent compacts the last K turns into a summary and merges it
// into the session state. The compacted turns are removed from the live
// window; the summary replaces them.
func (cs *CortexSession) CompactRecent(ctx context.Context) error {
    // 1. Take the last K turns from Messages
    // 2. Write them to a temp JSONL transcript file
    // 3. Call StudyLoop with goal="summarize recent work, decisions, active files"
    //    - Uses auto density (Chunks=0, Fill=0) — adapts to transcript size
    //    - Curator stops when summary is sufficient (not a fixed k)
    //    - Citations validate the summary is grounded in actual turns
    // 4. The digest becomes the Layer2Summary
    // 5. Merge into SessionState via study-based merge (see below)
    // 6. Replace compacted turns in Messages with the merged session state
    // 7. Update session manifest (fast-load JSON)
}
```

No custom compaction logic needed. The study engine handles the deepening loop, the curator decides when the summary is complete, and the citation validation ensures the summary doesn't hallucinate.

### The Merge: Study-Based, Not Mechanical

The plan originally described a mechanical merge of typed structs. Now it's a **study call on a text document**:

1. Write current SessionState (text) + new Layer2Summary (text) to a temp file
2. Call `StudyFile` with goal="merge these two summaries, resolve conflicts, prune stale items"
3. The study engine samples the combined text, infers a merged result, and validates citations
4. The merged result becomes the new SessionState

**Why this works:** The study engine's citation validation prevents the merge from introducing contradictions. If the model says "decision X was made" but can't cite where in the input that came from, the citation drops. The merge is self-correcting.

### Session State: Text Document + Fast-Load Manifest

The session state is two artifacts:

**Session state (text file, `.cortex/session_state.txt`):**
- The full persistent state, studied at merge time
- Updated by merging new summaries into it via study
- Grows slowly via merge; bounded by the study engine's sampling

**Session manifest (JSON file, `.cortex/session_manifest.json`):**
- Fast-load summary for resume (2KB, loaded instantly)
- Updated after each merge
- Contains: version, task, decisions, activeFiles, unresolved, lastTurn, updatedAt

This is simpler and more flexible than a typed struct. The study engine handles the parsing/merging. The manifest is just a fast-load shortcut.

### New Methods on CortexSession

```go
// CompactRecent compacts the last K turns using StudyLoop (auto density).
// The curator decides when the summary is sufficient. Citations validate
// the summary is grounded in actual turns.
func (cs *CortexSession) CompactRecent(ctx context.Context) error

// MergeIntoSessionState merges a Layer2Summary into the SessionState
// via a study call on the combined text. Citations prevent contradictions.
func (cs *CortexSession) MergeIntoSessionState(summary string) error

// BuildLiveContext assembles the messages to send for the next turn:
// system prompt + recent turns + session state + ephemeral retrieval.
func (cs *CortexSession) BuildLiveContext() []Message

// SaveSessionManifest persists the fast-load JSON manifest.
func (cs *CortexSession) SaveSessionManifest() error

// LoadSessionManifest loads the fast-load JSON manifest at startup.
func (cs *CortexSession) LoadSessionManifest() (*SessionManifest, error)
```

### The Compaction Flow

```
Before compaction:
  Messages = [system, user1, assistant1, tool1, user2, assistant2, ..., userN, assistantN]
  where N is large (e.g., 200)

CompactRecent():
  1. Take the last K turns from Messages
  2. Write them to a temp JSONL transcript file
  3. StudyLoop(goal="summarize recent work, decisions, active files")
     - Auto density (Chunks=0, Fill=0) — adapts to transcript size
     - Curator stops when summary is sufficient
     - Citations validate groundedness
  4. Merge digest into SessionState via StudyFile on combined text
  5. Replace compacted turns in Messages with merged session state
  6. Update session manifest (fast-load JSON)

After compaction:
  Messages = [system, user1..assistant150, sessionStateSummary]
  where sessionStateSummary is a single message containing the merged state
```

### The Lead Mechanism: Proactive Detail Recovery

The study engine's `Lead` type (off-sample needs → leads, never hallucinated citations) is a perfect fit for the forever session's "recoverable detail" requirement:

- During compaction, leads become **retrieval targets** — the model can study those specific turns later
- The session state includes a list of unresolved leads from the last compaction
- At turn start, the model sees: "Previous compaction left these unresolved items: [leads]. You can study any of them."

This is better than "study any past turn" because it's **proactive** — the study engine identifies what the model might need, not just what the model asks for.

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
    
    // Add unresolved leads from last compaction
    if cs.UnresolvedLeads != nil && len(cs.UnresolvedLeads) > 0 {
        msgs = append(msgs, Message{
            Role:    RoleUser,
            Content: "[Unresolved from last compaction — study any of these for detail:]\n" + cs.UnresolvedLeads.Format(),
        })
    }
    
    return msgs
}
```

At turn start, the ephemeral retrieval (Layer 4) is still fetched and folded into the system message as `EphemeralSystem`. The session state (Layer 3) is a persistent user message in the history.

---

## Compaction Triggers: Coverage-Driven

The current harness compacts at 80% window fill. The forever session replaces manual triggers with **coverage-driven triggers** — the study engine decides when enough:

1. **Window fill trigger (existing):** When `contextRatio() >= compactThreshold`, run `StudyLoop` on the recent turns. The study engine stops when coverage is sufficient (curator says DONE).

2. **Session state size trigger (new):** When the SessionState message grows beyond a token budget (e.g., 2000 tokens), trigger a merge+prune cycle via `StudyFile` on the combined text.

3. **Manual trigger (existing):** `/compact` still works, but now uses `StudyLoop` with auto density on the recent window rather than studying the entire transcript.

No recency trigger needed — the coverage signal is sufficient. If the context is getting large enough that the study engine needs to compress it, it will.

---

## Resume / Restart

When the session is resumed:

1. Load the session manifest from `.cortex/session_manifest.json` (fast, 2KB)
2. Load the recent turns from the latest transcript
3. If the transcript was compacted, the recent turns are shorter (the compacted turns are replaced by the session state summary)
4. The session state is injected as a user message in the history
5. If the manifest is missing, start fresh (graceful degradation)

The session manifest (`.cortex/session_manifest.json`) contains:
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

### Forever approach (bounded recent compaction via StudyLoop)

| Session Age | Recent Window | Compaction Cost |
|-------------|--------------|-----------------|
| 100 turns   | 50 turns     | ~1 StudyLoop call (auto density, curator stops when sufficient) |
| 500 turns   | 50 turns     | ~1 StudyLoop call |
| 2000 turns  | 50 turns     | ~1 StudyLoop call |
| 10000 turns | 50 turns     | ~1 StudyLoop call |

**The compaction cost is constant regardless of session age.** The merge is also a single bounded `StudyFile` call. Auto density adapts to transcript size — no tuning needed.

### Token cost per turn

| Component | Tokens (approx) |
|-----------|----------------|
| System prompt | 500 |
| Recent turns (15) | 3000 |
| Session state | 1000 |
| Unresolved leads | 200 |
| Retrieval (ephemeral) | 500 |
| **Total** | **~5200** |

This is well within even a 32k window, leaving 84% for the model's response and tool calls.

---

## What This Enables

1. **True forever sessions** — the session can run indefinitely without degradation
2. **Faster compaction** — compaction is always a bounded StudyLoop call, never grows with session age
3. **Better model comprehension** — the model sees a structured session state, not a wall of text
4. **Cross-session continuity** — the session state persists across restarts
5. **Retrieval synergy** — the session state tells the model what to look for; retrieval finds the details
6. **Proactive detail recovery** — leads from compaction identify what the model might need, not just what it asks for

---

## What This Does NOT Do

1. **It does not replace retrieval.** Retrieval (Layer 4) still handles on-demand detail lookup. The session state is a summary; retrieval is the drill-down.

2. **It does not eliminate the need for the transcript.** The full transcript is still persisted (for debugging, resume, and study). The session state is a compressed view.

3. **It does not solve the "model forgets" problem entirely.** The model can still lose track of subtle details across compactions. But the structured session state + retrieval + leads gives it much better recovery than a single digest.

4. **It does not change the inner tool loop.** The Resolve method, tool calls, and all existing mechanics remain unchanged. This is purely a context management layer.

---

## Implementation Phases

### Phase 1: Session Manifest (minimal viable)
- Add `SessionManifest` struct with Task, Decisions, ActiveFiles, Unresolved
- Add `SaveSessionManifest` / `LoadSessionManifest` (JSON file I/O)
- Add `/compact` that produces a session manifest update (not a full transcript replacement)
- Inject manifest as a user message at turn start

### Phase 2: StudyLoop Compaction
- Replace `Compact` with `CompactRecent` that calls `StudyLoop` (auto density)
- Curator decides when summary is sufficient (not fixed k)
- Citations validate groundedness
- Replace compacted turns with the study digest

### Phase 3: Study-Based Merge
- Implement merge via `StudyFile` on combined text (session state + new summary)
- Citations prevent contradictions in the merge
- Add session state size trigger (prune when growing too large)
- Extract leads from compaction into unresolved items

### Phase 4: Resume Integration
- Load session manifest at startup
- Inject into history on resume
- Handle manifest migration across schema versions
- Add unresolved leads to turn start context

---

## Risks and Mitigations

| Risk | Mitigation |
|------|-----------|
| Model produces poor summaries | The study engine already handles this well (bounded, cited). The merge prompt can include examples. |
| Session state grows unbounded | Bounded by design — each merge prunes stale items. Hard cap on message size. |
| Compaction loses critical detail | The full transcript is preserved. The model can study any past turn via the study tool. Leads identify what might be needed. |
| Merge introduces contradictions | Study-based merge with citation validation — contradictions drop uncited claims. |
| Resume with mismatched session state | Version field + graceful degradation (if manifest is missing, start fresh). |
| Auto density produces inconsistent summaries | Auto density is deterministic for the same input (same session salt). Consistency is guaranteed by the study engine's coverage tracking. |

---

## Comparison to Existing Approaches

| Approach | Bounded Context | Recoverable Detail | Constant Compaction Cost | Resumable |
|----------|----------------|-------------------|------------------------|-----------|
| Current (single digest) | ✅ | ❌ (digest is opaque) | ❌ (grows with age) | ✅ |
| Forever (this design) | ✅ | ✅ (study any turn + leads) | ✅ (bounded window) | ✅ |
| Naive (no compaction) | ❌ | ✅ | N/A | ✅ |

---

## Compaction Eval: Measuring Quality

The `study_eval.go` harness tests study on code, prose, and data files. For the forever session, we need a **compaction eval**:

- Generate synthetic transcripts (100, 500, 2000 turns)
- Run `StudyLoop` with goal="summarize recent work"
- Score: (1) groundedness of the summary (does it cite actual turns?), (2) coverage (what % of turns are represented?), (3) fidelity (does the summary preserve key decisions and active files?)

This would validate that the study engine's compaction doesn't lose information across sessions.

---

## Conclusion

A forever session is possible with this design. The key insight is that **compaction should be incremental and bounded, not wholesale and unbounded.** By maintaining multiple layers of context — recent turns (full detail), recent summary (compressed), session state (persistent), and retrieval (on-demand) — the model always has a small, structured, high-signal context window while retaining access to full detail on demand.

The existing harness already has most of the building blocks: the study engine (`StudyLoop` + `StudyFile` with auto density, citation validation, and curator-driven deepening), the transcript system, the retrieval/capture pipeline, and the compaction mechanism. The forever session design reorganizes these into a multi-layered pipeline rather than a single compaction event.

**The study engine is not a tool that compaction calls — it is the compaction mechanism.** This is the critical realization: the same primitives that study files (bounded sampling, provenance-constrained inference, curator-driven deepening) study transcripts. The forever session is just a different application of the same engine.
