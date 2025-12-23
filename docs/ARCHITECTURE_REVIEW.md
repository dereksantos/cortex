# Cortex Architecture Review

**Date**: 2025-01-15
**Version**: 0.1.0
**Status**: Critical Analysis & Redesign Planning

---

## Table of Contents

1. [Current Architecture Overview](#current-architecture-overview)
2. [Critical Issues & Questions](#critical-issues--questions)
3. [Fundamental Design Questions](#fundamental-design-questions)
4. [Technology Analysis: Turso vs SQLite](#technology-analysis-turso-vs-sqlite)
5. [Assessment Summary](#assessment-summary)
6. [Next Steps](#next-steps)

---

## Current Architecture Overview

### High-Level Data Flow

```
AI Tool → Hook → cortex capture (<10ms) → File Queue (pending/)
                                              ↓
User Queries ← SQLite (FTS5/Graph) ← LLM Analysis ← Daemon (polls every 5s)
```

### Core Components

#### 1. Capture Layer (`internal/capture/`)
- **Purpose**: Fast event capture from AI tool hooks
- **Performance Target**: <10ms
- **Mechanism**: Atomic file writes (temp + rename pattern)
- **Output**: JSON events written to file queue
- **Design**: Silent failure (doesn't interrupt AI tools)

#### 2. Queue System (`internal/queue/`)
- **Type**: File-based queue
- **Structure**:
  - `pending/` - New events to process
  - `processing/` - Currently being processed
  - `processed/` - Completed events
- **Processing**: Daemon polls every 5 seconds

#### 3. Storage Layer (`internal/storage/`)
- **Database**: SQLite with event sourcing
- **Schema**:
  - `events` - Immutable append-only event log
  - `insights` - LLM-extracted insights with categories
  - `entities` - Knowledge graph nodes (decisions, patterns)
  - `relationships` - Knowledge graph edges
  - `events_fts` - Full-text search index (FTS5)
- **Pattern**: Event sourcing (immutable, auditable)

#### 4. Processor (`internal/processor/`)
- **Type**: Background daemon
- **Polling**: Every 5 seconds
- **Workers**: 5 parallel LLM analysis workers
- **Filtering**: Skips routine operations, binary files, lock files
- **Deduplication**: 30-second window per file

#### 5. LLM Analysis (`pkg/llm/`)
- **Provider**: Ollama (local LLM)
- **Default Model**: mistral:7b
- **Timeout**: 30 seconds per analysis
- **Output**: Categories, importance scores, tags, entities, relationships
- **Fallback**: Heuristic-based extraction if JSON parsing fails

#### 6. Query Layer (in `cmd/cortex/main.go`)
- **Search**: Full-text with LIKE queries (FTS5 table exists but unused)
- **Insights**: Filter by category (decision, pattern, insight, strategy)
- **Entities**: Browse knowledge graph nodes
- **Graph**: Traverse relationships
- **Context Injection**: Naive keyword matching for AI prompt injection

### Event Structure

```go
type Event struct {
    ID         string
    Source     Source      // claude, cursor, copilot, etc.
    EventType  EventType   // tool_use, edit, search, etc.
    Timestamp  time.Time
    ToolName   string
    ToolInput  map[string]interface{}
    ToolResult string
    Context    EventContext
    Metadata   map[string]interface{}
}
```

### Configuration

```go
type Config struct {
    ContextDir    string      // .context/
    ProjectRoot   string
    SkipPatterns  []string    // Filtering patterns
    OllamaURL     string      // http://localhost:11434
    OllamaModel   string      // mistral:7b
    EnableGraph   bool        // true
    EnableVector  bool        // false (not implemented)
}
```

---

## Critical Issues & Questions

### 1. Event Granularity Problem

**Current Behavior**: Every tool use generates an event.

**What's Being Captured**:
- Every Edit operation
- Every Read operation
- Every Write operation
- Every Bash command (except ls/pwd/echo/cd/which/date)
- Every Grep search
- Every file operation

**The Problem**:
Even with filtering, you're capturing extremely fine-grained events. A single "implement user authentication" task might generate 50+ events (edits, reads, writes, tests).

**Questions**:
- Is tool-use the right granularity?
- Should the unit be "task" (coherent piece of work)?
- Should it be "session" (entire AI conversation)?
- Should it be "decision" (architectural choices)?
- How do you aggregate low-level events into meaningful memory?

**Example Scenario**:
```
Task: "Add JWT authentication"

Current Capture (50+ events):
- Read auth.go
- Edit auth.go (add JWT import)
- Edit auth.go (add GenerateToken func)
- Edit auth.go (add ValidateToken func)
- Write auth_test.go
- Bash: go test
- Read middleware.go
- Edit middleware.go (add auth middleware)
- Read config.go
- Edit config.go (add JWT_SECRET)
... 40 more events

What You Actually Want to Remember:
- Decision: Chose JWT over sessions because stateless + microservices
- Pattern: Token in Authorization header
- Implementation: Using github.com/golang-jwt/jwt library
- Security: Tokens expire after 24h, refresh token pattern
```

**Impact**: Database fills with noise. Hard to find signal. Context injection becomes ineffective.

---

### 2. Queue Processing Model

**Current Implementation**:
- File-based queue (pending/, processing/, processed/)
- Daemon polls every 5 seconds
- Processes all pending events in batch

**Issues**:

#### A. Polling Overhead
- Checks filesystem every 5 seconds even when idle
- Not responsive to bursts (up to 5s delay)
- File I/O overhead for every event

#### B. Queue Growth
- If daemon isn't running, queue grows unbounded
- No alerts or backpressure
- Could fill disk in long sessions

#### C. Processing Semantics
- What if daemon crashes during processing?
- Are events in `processing/` cleaned up?
- No transaction guarantees

#### D. Alternative Approaches

**Option 1: Reactive Processing (fsnotify/inotify)**
```go
watcher.Add(queueDir)
for {
    select {
    case event := <-watcher.Events:
        processImmediately(event)
    }
}
```
- ✅ Real-time processing (no 5s delay)
- ✅ No polling overhead
- ❌ More complex (watch semantics)

**Option 2: In-Memory Queue + Persistence**
```go
type Queue struct {
    events chan Event
    wal    *WriteAheadLog
}
```
- ✅ Fast (no file I/O per event)
- ✅ Durability (WAL for crash recovery)
- ❌ Requires long-running daemon

**Option 3: Direct to Database**
```
Capture → SQLite (events table) → Async processing
```
- ✅ Simple (no separate queue)
- ✅ Transaction guarantees
- ❌ SQLite write locks could slow capture

**Questions**:
- Why file-based queue vs alternatives?
- Is 5s polling interval optimal?
- How do you handle daemon not running?

---

### 3. LLM Processing Bottleneck

**Current Behavior**: Every captured event goes through LLM analysis.

**The Bottleneck**:
```
Capture 100 events in session
→ 100 Ollama API calls (even with 5 workers)
→ ~30s timeout each = up to 10 minutes total processing
→ Queue grows faster than processing speed
```

**Issues**:

#### A. No Batching
Each event processed individually. Could batch similar events:
```
Batch: 10 consecutive edits to auth.go
→ Single LLM call: "Summarize these changes to auth.go"
```

#### B. Everything Gets LLM Treatment
Even after filtering, routine operations get analyzed:
- `go test` (fails) → LLM analyzes
- `git status` → LLM analyzes
- Reading documentation → LLM analyzes

**Should these need LLM?**

#### C. No Prioritization
All events treated equally. No concept of:
- High priority (Edit, Write) vs low priority (Read)
- Important files (core logic) vs config files
- Complex operations vs simple ones

#### D. Synchronous Processing Model
Daemon must process everything before moving on. No lazy evaluation.

**Alternative Approaches**:

**Option 1: Lazy LLM Analysis**
```
Capture → Store raw event → Process on-demand during query
```
- ✅ No processing bottleneck
- ✅ Only analyze what's queried
- ❌ Query latency

**Option 2: Tiered Processing**
```
Tier 1: Rule-based categorization (fast, no LLM)
Tier 2: LLM analysis only for "important" events
Tier 3: Deep analysis on-demand
```

**Option 3: Batched Processing**
```
Accumulate events for session/task
→ Single LLM call: "Summarize this work session"
```

**Questions**:
- Do you need LLM for every event?
- Should analysis be lazy (on-demand)?
- Should events be batched by session/task?
- Is Ollama the bottleneck or the processing model?

---

### 4. Knowledge Graph Schema

**Current Implementation**: Schema-less entity extraction by LLM.

**Example Extraction**:
```json
{
  "entities": [
    {"type": "decision", "name": "Use JWT for auth"},
    {"type": "pattern", "name": "middleware pattern"}
  ],
  "relationships": [
    {"from": "auth decision", "to": "middleware pattern", "type": "implements"}
  ]
}
```

**Issues**:

#### A. No Entity Resolution
LLM might extract:
- "authentication" (lowercase)
- "Authentication" (capitalized)
- "auth" (abbreviated)
- "user authentication" (qualified)
- "JWT authentication" (specific)

All stored as separate entities. No deduplication or normalization.

#### B. No Relationship Schema
Relationships are free-form strings:
- "implements"
- "uses"
- "depends on"
- "related to"
- "part of"

How do you query this meaningfully? No type hierarchy or semantics.

#### C. No Entity Types Schema
Types are LLM-generated strings:
- "decision" vs "architectural decision" vs "design decision"
- "pattern" vs "design pattern" vs "code pattern"

No consistency enforcement.

#### D. No Versioning
Entities change over time:
```
Day 1: Decision: "Use REST API"
Day 5: Decision: "Migrate to GraphQL"
```

No way to track entity evolution or superseded decisions.

**What's Missing**:

1. **Entity Resolution**: Deduplicate and normalize entity names
2. **Schema Enforcement**: Fixed entity types and relationship types
3. **Versioning**: Track how entities evolve
4. **Confidence Scores**: LLM might hallucinate entities
5. **Entity Linking**: Connect to source events (which event created this entity?)

**Questions**:
- Should entities have a fixed schema?
- How do you handle entity deduplication?
- Should there be a fixed ontology of relationships?
- How do you version entities over time?

---

### 5. Context Injection Limitations

**Current Implementation** (`cmd/cortex/main.go:1414-1477`):

```go
func extractKeyTerms(userPrompt string) []string {
    // Split on spaces/punctuation
    words := strings.FieldsFunc(userPrompt, splitFunc)

    // Basic stop word removal
    stopWords := []string{"the", "a", "an", "and", "or", ...}

    // Return non-stop words
    return terms
}

func findRelevantInsights(terms []string) []Insight {
    for _, term := range terms {
        // SQL: WHERE content LIKE '%term%'
        insights = append(insights, matchingInsights...)
    }
    return insights
}
```

**The Problem**: This is naive keyword matching with no:
- Semantic similarity
- Relevance ranking
- Context understanding
- Query expansion

**Example Failure**:
```
User Prompt: "How should I handle concurrent writes?"

Extracted Terms: ["handle", "concurrent", "writes"]

LIKE '%concurrent%' matches:
- ❌ "Use concurrent map for cache"
- ❌ "Concurrent HTTP requests"
- ✅ "Database write lock strategy"
- ❌ "Concurrent test execution"

What you actually want:
- ✅ "Database write lock strategy" (relevant!)
- ✅ "Race condition in file writes" (semantic match)
- ✅ "Mutex vs channel decision" (related concept)
```

**What's Missing**:

#### A. Vector Embeddings
Convert insights to vectors, compute cosine similarity:
```
user_prompt_vector = embed("How should I handle concurrent writes?")
insight_vectors = embed(all_insights)
similarities = cosine_similarity(user_prompt_vector, insight_vectors)
top_5 = sort(similarities)[:5]
```

#### B. Relevance Ranking
- TF-IDF scoring
- BM25 ranking
- Recency bias (recent insights more relevant)
- Importance weighting (high importance → higher rank)

#### C. Query Expansion
```
"concurrent writes" → ["concurrency", "race conditions", "locks", "mutex", "synchronization"]
```

#### D. Context Window Management
- How many insights to inject?
- How long should injected context be?
- Do you summarize long insights?

**Current State**: `EnableVector: false` in config, not implemented.

**Impact**: Context injection is essentially random. Won't scale past 100 insights.

---

### 6. Session Semantics Unclear

**Current Implementation**: Events have `SessionID` field.

**Questions**:

#### A. What Defines a Session?
- A Claude Code conversation?
- A single day of work?
- A feature branch?
- A specific task/ticket?
- Manually defined?

**Not documented or enforced anywhere.**

#### B. How Are Sessions Created?
Looking at the code:
- `handleSessionStart()` exists (line 1173 in main.go)
- Writes to session file
- But how is SessionID generated?
- When does a session end?

#### C. How Do You Query by Session?
- No "list sessions" command
- No "show session X" command
- No session-based search
- `cortex search` doesn't filter by session

#### D. Session vs Task vs Conversation
```
Example:
- Task: "Implement user authentication"
- Claude Conversation: 2 hours, 50 messages
- Git Branch: feature/auth
- Calendar Day: Wednesday

Which is the "session"?
```

**What's Missing**:
- Clear session boundaries
- Session lifecycle (start, end, pause, resume)
- Session metadata (title, description, tags)
- Session-based queries
- Session summaries

**Impact**: SessionID exists but doesn't add value. Just metadata.

---

### 7. FTS5 Table Created But Unused

**The Issue**:

`internal/storage/storage.go:133` creates FTS5 table:
```sql
CREATE VIRTUAL TABLE IF NOT EXISTS events_fts USING fts5(
    event_id,
    content,
    category,
    tags
);
```

But `internal/storage/storage.go:247` uses LIKE queries:
```go
func (s *Storage) Search(query string) ([]*Insight, error) {
    sql := `
        SELECT * FROM insights
        WHERE content LIKE ? OR title LIKE ? OR tags LIKE ?
    `
    // Simple LIKE search for now (we'll enhance with FTS later)
}
```

**Why This Matters**:

LIKE queries:
- ❌ Slow on large datasets (full table scan)
- ❌ No relevance ranking
- ❌ No stemming (search "running" won't match "run")
- ❌ No phrase matching
- ❌ Poor for multi-term queries

FTS5 provides:
- ✅ Fast full-text search (inverted index)
- ✅ Relevance ranking (BM25)
- ✅ Stemming and tokenization
- ✅ Phrase queries ("exact match")
- ✅ Boolean operators (AND, OR, NOT)

**Questions**:
- Why create FTS5 table if not using it?
- Was it deprioritized?
- Is it legacy from Python version?

**Impact**: Search performance will degrade as database grows.

---

### 8. Multi-Project Problem

**Current Limitation**: Single `ProjectRoot` in config.

**Issues**:

#### A. One Project Per Cortex Instance
```
~/project-a/.context/  (separate database)
~/project-b/.context/  (separate database)
```

No unified view across projects.

#### B. Monorepo Challenges
```
~/monorepo/
  ├── service-a/
  ├── service-b/
  └── shared-lib/

Where does .context/ go?
- At root? (captures everything)
- Per service? (can't track cross-service decisions)
```

#### C. Cross-Project Patterns
"We solved authentication in project-a with JWT. Let's use same pattern in project-b."

**Current**: Can't query across projects.

#### D. Context Switching
Working on multiple projects in parallel:
- Switch git branches
- Switch directories
- **But all events go to single .context/**

No per-branch or per-workspace isolation.

**Alternative Approaches**:

**Option 1: Global Cortex Database**
```
~/.cortex/
  └── db/
      └── cortex.db  (all projects)

Events have ProjectRoot field → filter by project
```

**Option 2: Workspace Support**
```
.context/
  ├── workspaces/
  │   ├── feature-auth/
  │   ├── feature-payments/
  │   └── bugfix-123/
```

**Option 3: Project Linking**
```
project-a/.context/
project-b/.context/
shared-knowledge/.context/  (linked from both)
```

**Questions**:
- How should multi-project work?
- Should there be a global Cortex database?
- How do you handle monorepos?
- Should workspaces/branches have separate contexts?

---

### 9. Dead Code: Knowledge Directories

**The Issue**:

`pkg/config/config.go:90-93` creates directories:
```go
filepath.Join(c.ContextDir, "knowledge", "decisions"),
filepath.Join(c.ContextDir, "knowledge", "patterns"),
filepath.Join(c.ContextDir, "knowledge", "insights"),
filepath.Join(c.ContextDir, "knowledge", "strategies"),
```

**But nothing uses them.** Everything goes to SQLite.

**Origin**: Legacy from Python version that organized insights as markdown files:
```
knowledge/
  ├── decisions/
  │   └── 2025-01-15_jwt-authentication.md
  ├── patterns/
  │   └── 2025-01-14_middleware-pattern.md
  ...
```

**Go version**: Uses SQLite exclusively.

**Impact**:
- Confusing to users (empty directories)
- Maintenance burden (code that does nothing)
- Suggests feature that doesn't exist

**Resolution**: Remove directory creation or implement markdown export.

---

### 10. No Backpressure Mechanism

**The Problem**: If LLM processing is slow, queue grows unbounded.

**Scenario**:
```
Fast capture: 10 events/second
Slow processing: 1 event/5 seconds

After 1 minute:
- Captured: 600 events
- Processed: 12 events
- Queue size: 588 events

After 10 minutes:
- Queue size: 5,988 events
```

**No Protection Against**:
- Disk space exhaustion
- Memory exhaustion (if in-memory queue)
- Unbounded processing backlog

**No Alerts For**:
- Queue depth exceeding threshold
- Processing lag exceeding time threshold
- Daemon not running

**What's Missing**:

#### A. Queue Depth Monitoring
```go
if queueDepth > 1000 {
    log.Warn("Queue backlog: %d events", queueDepth)
}
```

#### B. Backpressure
```go
if queueDepth > 5000 {
    // Stop capturing new events
    return ErrBackpressure
}
```

#### C. Adaptive Processing
```go
if queueDepth > 1000 {
    // Skip LLM analysis, store raw events
    fastPath = true
}
```

#### D. Health Checks
```go
// cortex status
Queue depth: 5,234 events (WARNING: high)
Last processed: 2 minutes ago (WARNING: slow)
Daemon status: running
```

**Questions**:
- What happens when queue grows too large?
- Should capture be throttled?
- Should processing be simplified under load?
- How do you monitor system health?

---

## Fundamental Design Questions

### Question 1: What is the Actual "Memory" Being Captured?

**Current**: Individual tool uses (edits, reads, writes, commands)

**Alternatives**:

#### A. Tool Uses (Current)
```
✅ Pros: Complete audit trail, high fidelity
❌ Cons: Too granular, lots of noise, hard to find signal
```

#### B. Tasks
```
Example: "Implement JWT authentication"
- Coherent unit of work
- Has beginning, middle, end
- Has a goal/outcome

✅ Pros: Right level of abstraction, meaningful memory
❌ Cons: Hard to detect task boundaries automatically
```

#### C. Decisions
```
Example: "Use JWT instead of sessions because microservices"
- Architectural choices
- Technical trade-offs
- Design rationale

✅ Pros: High-value information, long-lived relevance
❌ Cons: Requires understanding context, infrequent
```

#### D. Sessions
```
Example: Entire Claude Code conversation
- Natural boundary
- Captures context and flow
- Has topic/theme

✅ Pros: Captures narrative, easy to define
❌ Cons: Might be too coarse, loses detail
```

#### E. Hybrid Approach
```
Level 1: Raw events (complete audit)
Level 2: Task summaries (synthesized)
Level 3: Decision records (curated)

Query at appropriate level:
- "What did I do?" → Task summaries
- "Why did we choose X?" → Decision records
- "Exact command I ran?" → Raw events
```

**Question**: What granularity provides the most value?

---

### Question 2: When Should LLM Analysis Happen?

**Current**: Asynchronously via daemon after capture

**Alternatives**:

#### A. Synchronous During Capture
```
Capture → LLM Analysis → Store
```
- ✅ Immediate results
- ❌ Slows capture (<10ms requirement violated)
- ❌ Blocks AI tool

#### B. Asynchronous Queue (Current)
```
Capture → Queue → Daemon → LLM → Store
```
- ✅ Fast capture
- ✅ Decoupled
- ❌ Processing lag
- ❌ Queue growth

#### C. Lazy On-Demand
```
Capture → Store Raw → [User Queries] → LLM Analysis → Cache
```
- ✅ No processing bottleneck
- ✅ Only analyze what's queried
- ❌ Query latency
- ❌ Cold start for first query

#### D. Batched Periodic
```
Capture → Store → [End of Session] → Batch LLM Analysis
```
- ✅ Efficient (batch processing)
- ✅ Can summarize entire session
- ❌ Delayed results
- ❌ Requires session boundaries

#### E. Tiered Processing
```
Tier 1: Rule-based (instant, no LLM)
  - Categorize by tool name
  - Extract file paths
  - Basic metadata

Tier 2: Lightweight LLM (fast model, simple prompt)
  - Importance scoring
  - Quick categorization

Tier 3: Deep analysis (on-demand)
  - Entity extraction
  - Relationship mapping
  - Semantic embedding
```
- ✅ Balanced trade-off
- ✅ Adaptive to load
- ❌ More complex

**Question**: What processing model optimizes for value vs performance?

---

### Question 3: What's the Query Model?

**Current**: Multiple query modes (search, insights, entities, graph)

**Query Patterns**:

#### A. Full-Text Search
```
cortex search "authentication decision"
→ Returns insights matching keywords
```
- Current: LIKE queries
- Ideal: FTS5 or vector search

#### B. Semantic Search
```
cortex search "How do we handle concurrent access?"
→ Returns insights about locks, mutexes, race conditions
```
- Needs: Vector embeddings
- Current: Not implemented (`EnableVector: false`)

#### C. Temporal Queries
```
cortex timeline --since="yesterday"
cortex timeline --during="feature/auth branch"
```
- Needs: Time-based indexing
- Current: Not implemented

#### D. Session-Based
```
cortex session show <session-id>
cortex session list --recent
```
- Needs: Clear session semantics
- Current: SessionID exists but underutilized

#### E. Graph Traversal
```
cortex graph "JWT decision" --depth=2
→ Show all decisions/patterns related to JWT
```
- Current: Basic implementation
- Needs: Better relationship types

#### F. Natural Language
```
cortex ask "Why did we choose PostgreSQL?"
cortex ask "What patterns do we use for error handling?"
```
- Needs: LLM-powered query understanding
- Could leverage context + LLM

**Question**: Which query modes provide the most value? Should there be a unified query interface?

---

### Question 4: Is Local-Only the Right Model?

**Current**: 100% local (Ollama, SQLite, no network)

**Trade-offs**:

#### A. Local-Only (Current)
```
✅ Privacy: Code never leaves machine
✅ Fast: No network latency
✅ Free: No API costs
✅ Offline: Works without internet

❌ Ollama Required: High barrier to entry
❌ Limited Models: Can't use GPT-4, Claude, etc.
❌ No Vector Search: Ollama doesn't provide embeddings API
❌ No Multi-Device: Can't sync across machines
❌ Local Compute: Slower on weak machines
```

#### B. Hybrid (Local + Optional Cloud)
```
Default: Local processing (Ollama)
Optional: Cloud LLM (OpenAI, Anthropic) for better analysis
Optional: Cloud sync for multi-device

✅ Best of both worlds
✅ User choice (privacy vs features)
✅ Better models available
✅ Vector embeddings available

❌ More complex
❌ Privacy concerns if cloud enabled
❌ API costs if cloud enabled
```

#### C. Cloud-First
```
All processing in cloud
Local DB is just a cache

✅ Best features (GPT-4, embeddings, etc.)
✅ Multi-device sync built-in
✅ No local compute needed

❌ Privacy violation (code leaves machine)
❌ Requires internet
❌ API costs
❌ Not aligned with "privacy-first" principle
```

**The Ollama Barrier**:
- Many developers don't have Ollama installed
- Requires downloading multi-GB models
- Slower than cloud APIs
- Setup friction

**The Vector Search Problem**:
- Semantic search needs embeddings
- Ollama doesn't provide embeddings API
- Would need separate embedding service (local or cloud)
- Or switch to Turso with native vector search

**Question**: Should Cortex support optional cloud processing? Or stay pure local?

---

### Question 5: What's the Intended UX?

**Current**: Multiple usage patterns, not clearly prioritized

**Possible UX Models**:

#### A. Passive Memory (Current Default)
```
User: [Works with AI assistant]
Cortex: [Silently captures everything]
User: [Sometime later] cortex search "auth decision"
Cortex: [Shows relevant insights]
```
- Low friction
- Delayed value (only useful later)
- Requires user to remember to query

#### B. Active Injection
```
User: [Asks AI assistant] "How should I handle auth?"
Cortex: [Auto-injects context] "You previously decided to use JWT because..."
AI: [Responds with context awareness]
```
- Immediate value
- Requires good context injection (vector search)
- More intrusive (injected context visible)

#### C. Exploration Tool
```
User: cortex overview
Cortex: [Shows visual graph of decisions/patterns]
User: cortex graph "authentication"
Cortex: [Shows all related entities and relationships]
```
- Discovery-oriented
- Good for reviewing past work
- Requires good visualization

#### D. Documentation Generator
```
User: cortex docs generate
Cortex: [Creates markdown docs from decisions/patterns]
```
- Output-oriented
- Transforms memory into artifacts
- Good for team sharing

#### E. Pair Programming Assistant
```
User: [In Claude Code] "Remind me how we handle errors"
Cortex: [Injected] "Pattern: Wrap errors with context using fmt.Errorf"
```
- Conversational
- Requires natural language interface
- High value if works well

**Question**: Which UX model should be primary? Should Cortex support multiple modes?

---

## Technology Analysis: Turso vs SQLite

### Turso Overview

**What is Turso?**
- SQLite-compatible database built in Rust
- "Built for the agentic future"
- Native vector search support
- Distributed/sync capabilities
- Async-first architecture

**Key Features for Cortex**:

#### 1. Native Vector Embeddings ✅
```sql
-- Turso native vector search
CREATE TABLE insights_vectors (
    id INTEGER PRIMARY KEY,
    embedding VECTOR(768)  -- Native vector type
);

-- Semantic similarity search
SELECT * FROM insights_vectors
ORDER BY embedding <-> query_vector
LIMIT 10;
```

**Impact**:
- Enables semantic search without external service
- Critical for context injection
- No need for separate vector DB (Pinecone, Weaviate, etc.)

#### 2. Distributed Sync
```
Local: User's machine (embedded Turso)
Cloud: Optional Turso Cloud
Sync: Bidirectional, conflict-free

Use Case: Multi-device sync (optional, privacy-preserved)
```

**Impact**:
- Could enable multi-device Cortex
- Optional feature (local-first still default)
- Better than building custom sync

#### 3. Embedded Mode (Local-First)
```go
import "github.com/tursodatabase/go-libsql"

db, err := sql.Open("libsql", "file:./local.db")
// Pure local, no cloud required
```

**Impact**:
- Can use Turso purely locally (no cloud)
- Drop-in SQLite replacement
- Privacy preserved

#### 4. Async Architecture
- Uses modern async primitives (io_uring)
- Better concurrency than SQLite

**Impact**:
- Could handle concurrent writes better
- Less lock contention

### Turso vs SQLite Comparison

| Feature | SQLite (Current) | Turso |
|---------|------------------|-------|
| **Vector Search** | ❌ Not supported | ✅ Native support |
| **Embeddings** | ❌ Need external service | ✅ Built-in |
| **Semantic Search** | ❌ FTS5 only (keyword) | ✅ Vector similarity |
| **Concurrent Writes** | ⚠️ Lock contention | ✅ Better concurrency |
| **Sync** | ❌ Not supported | ✅ Built-in |
| **Local-First** | ✅ Always local | ✅ Optional local |
| **Maturity** | ✅ 20+ years | ⚠️ Newer |
| **Go Support** | ✅ database/sql | ✅ go-libsql (CGO) |
| **Size** | ✅ Tiny (~1MB) | ⚠️ Larger (Rust runtime) |
| **License** | ✅ Public domain | ⚠️ MIT (check Turso Cloud terms) |

### Turso for Cortex Use Cases

#### ✅ Enables Semantic Search
**Current Problem**: Context injection uses naive keyword matching.

**With Turso**:
```sql
-- Store insight embeddings
INSERT INTO insights_vectors (id, content, embedding)
VALUES (1, 'Use JWT for stateless auth', embed(content));

-- Semantic search
SELECT content FROM insights_vectors
ORDER BY embedding <-> embed('How should I handle authentication?')
LIMIT 5;
```

**Impact**: Solves Issue #5 (Context Injection Limitations)

#### ✅ Enables Multi-Device Sync (Optional)
**Current Problem**: Each machine has separate .context/

**With Turso**:
```
Work Mac: .context/ (Turso embedded)
Personal Mac: .context/ (Turso embedded)
Turso Cloud: Sync layer (optional)

Sync: Bidirectional, automatic
```

**Impact**: Solves Issue #8 (Multi-Project Problem) partially

#### ⚠️ Doesn't Solve Core Design Issues
Turso won't fix:
- Event granularity problem (#1)
- Queue processing model (#2)
- LLM processing bottleneck (#3)
- Knowledge graph schema (#4)
- Session semantics (#6)

These are architectural/design issues, not database issues.

### Migration Considerations

#### Pros
- ✅ Enables vector search (critical feature)
- ✅ Better concurrency
- ✅ Optional sync (future feature)
- ✅ SQLite-compatible (easier migration)

#### Cons
- ⚠️ Requires CGO (go-libsql uses C bindings)
- ⚠️ Larger binary size
- ⚠️ Newer, less mature
- ⚠️ Embedding generation still needs LLM

#### Embedding Generation
**Important**: Turso stores vectors but doesn't generate them.

Still need:
- Ollama (no embeddings API)
- OpenAI embeddings API (cloud, costs $)
- Sentence transformers (local, CPU)
- Other embedding service

**Options**:
1. OpenAI embeddings (best quality, requires API key)
2. Local transformer (privacy-first, slower)
3. Ollama + custom embeddings endpoint

### Recommendation: Incremental Adoption

#### Phase 1: Keep SQLite, Add Vector Table
```sql
CREATE TABLE insight_embeddings (
    insight_id INTEGER PRIMARY KEY,
    embedding BLOB  -- Store as binary blob
);

-- Use external library for similarity
-- e.g., github.com/chewxy/math32
```

Test vector search with SQLite first.

#### Phase 2: Migrate to Turso
Once vector search proven valuable:
- Switch to Turso embedded
- Use native vector type
- Keep local-first
- Don't enable sync yet

#### Phase 3: Optional Cloud Features
If users want multi-device:
- Enable Turso Cloud sync
- Make it opt-in
- Document privacy implications

---

## Assessment Summary

### What Works Well ✅

1. **Fast Capture Design**: <10ms target, atomic writes, silent failure
2. **Event Sourcing**: Immutable, auditable, time-travel capable
3. **Privacy-First**: Local processing, no telemetry
4. **Graceful Degradation**: Works without Ollama (stores raw events)
5. **Integration Model**: Clean hook-based capture from AI tools

### Critical Issues ⚠️

1. **Event Granularity Too Fine**: Tool use vs task/decision
2. **LLM Processing Bottleneck**: Every event analyzed, no batching
3. **Context Injection Naive**: Keyword matching, needs vector search
4. **Knowledge Graph Schema-less**: No entity resolution
5. **Session Semantics Unclear**: Concept exists but underutilized

### Missing Features for Real Value ❌

1. **Vector Embeddings**: Needed for semantic search
2. **Entity Resolution**: Needed for knowledge graph consistency
3. **Relevance Ranking**: Needed for context injection
4. **Task/Session Boundaries**: Needed for coherent memory units
5. **FTS5 Search**: Created but not used

### Technology Gaps

1. **Ollama Limitations**: No embeddings API, setup friction
2. **SQLite Limitations**: No native vector search
3. **Queue Processing**: Polling vs reactive

### Architectural Questions Needing Answers

1. What is the right memory granularity? (event vs task vs decision vs session)
2. When should LLM analysis happen? (sync vs async vs lazy vs batched)
3. What query model provides most value? (keyword vs semantic vs graph vs temporal)
4. Should Cortex stay local-only? (pure local vs hybrid vs cloud)
5. What's the primary UX? (passive memory vs active injection vs exploration vs docs)

---

## Next Steps

### Immediate Actions

1. **Answer Fundamental Design Questions** (Section 3)
   - Define memory granularity
   - Define query model priority
   - Define UX model priority

2. **Write Design Document**
   - Based on answers to fundamental questions
   - Propose concrete solutions to 10 critical issues
   - Prioritize improvements

3. **Prototype Vector Search**
   - Test with SQLite + binary blobs
   - Or test with Turso embedded
   - Validate semantic search value

4. **Implement FTS5 Search**
   - Already have table, just use it
   - Quick win for search improvement

### Medium-Term Improvements

5. **Refactor Event Granularity**
   - Define task boundaries
   - Implement task summarization
   - Aggregate events into tasks

6. **Improve LLM Processing**
   - Add batching
   - Add prioritization
   - Add lazy evaluation option

7. **Entity Resolution**
   - Define entity schema
   - Implement deduplication
   - Add entity versioning

8. **Session Management**
   - Define session lifecycle
   - Add session queries
   - Add session summaries

### Long-Term Features

9. **Multi-Project Support**
   - Design workspace model
   - Implement cross-project queries
   - Add project linking

10. **Advanced Context Injection**
    - Vector-based relevance
    - Context window management
    - Adaptive injection

---

**Document Status**: Living document - update as architecture evolves
**Last Updated**: 2025-01-15
**Next Review**: After fundamental design questions answered
