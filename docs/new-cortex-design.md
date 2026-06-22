# A New Cortex — Re-imagined from the Innovations

> **Status.** Analysis and proposal. The starting point for a clean
> rebuild that carries forward every proven innovation from the current
> project, organized around a MECE core, with simplicity, portability,
> flexibility, and extensibility as the design constraints.
>
> **Not** a port. Not a refactor. A re-imagining that asks: if we knew
> what we know now, what would cortex look like?

---

## 1. Why a Clean Rebuild

The current project is ~90k lines of non-test Go across 16 `internal/`
and 9 `pkg/` packages, ~51k lines of tests, and 41 top-level design
docs (111 including ADRs and archives). It accreted through a research
log of eval-driven commits — each right in isolation, but the whole has
no single organizing principle. The evidence:

- **Two agent loops that never unified.** `cmd/loop/main.go` (4,279
  lines, the REPL) and `internal/harness/loop.go` (674 lines, the
  DAG-driven coding turn) are parallel implementations. The bridge is
  `decide.coding_turn` (`internal/harness/dagnode/coding_turn.go`),
  which runs the harness loop *inline within a DAG node* — the DAG
  wraps the loop instead of the loop *being* the DAG. This is
  backwards.
- **Two retrieval systems.** `internal/cognition/` (Reflex/Reflect/
  Resolve/Think/Dream — 24 methods on the `Cortex` god-object) and the
  DAG ops (`remember.vector_search`, `attend.rerank`, `decide.inject`)
  overlap heavily. The simplification audit (D10) acknowledged this and
  deferred the merge.
- **Two study engines.** The interactive `StudyLoop` (`study_loop.go`,
  192 lines: sample → infer → deepen) and the background `controller.go`
  (739 lines: sample → extract insight → journal) share the fractal
  sampling primitive but are otherwise disconnected.
- **The DAG system is the most sophisticated piece and the least
  used.** Seed-and-grow executor, per-node routing, salience contracts,
  turn state, 25 op handlers (17 registered by default) — and
  `cmd/loop` doesn't use it at all.
- **41 design docs, many stale.** They're archaeology of decisions,
  not a current architecture.

The project has proven every individual innovation. The problem is
that they were never composed into a single coherent system.

---

## 2. The Innovations Worth Keeping

### 2.1 The Agent Loop (cmd/loop)

**Proven:** The REPL is the thing people use. The turn cycle, tool
dispatch, streaming renderer, and transcript persistence are
battle-tested.

**Keep:**
- The turn cycle (Turn → Resolve → runToolCalls → repeat).
- Streaming with reasoning tails and tool-marker detection.
- Session transcripts as JSONL (simple, `cat | jq` works).
- Compaction as study-over-transcript.
- The tool surface: read, write, edit, bash, study, map, remove.
- **The context gauge** — `ctxColor` (`main.go:3355`) shifts green →
  yellow → red as the window fills. The auto-compact trigger
  (`compactThreshold = 0.8`, `main.go:119`) and the gauge color both
  read `contextRatio` (`main.go:2708`). The user *sees* when
  compaction is coming.
- **The anchored prompt with type-ahead** — `editor.Anchor`
  (`internal/lineedit/live.go`) creates a bottom-row prompt during a
  turn; stdout is redirected to a pipe whose lines feed the anchor.
  The user types the next message while the model streams. A genuine
  UX innovation.
- **No-progress detection** — the harness loop's `noProgressWindow`
  (5 turns, `loop.go:79`) distinguishes "reading 5 different files"
  (productive) from "re-issuing the same call" (stuck). Novelty-based,
  not write-based — intent-agnostic. The safety net against infinite
  tool-calling loops.

**Drop:**
- The hand-rolled HTTP transport. Use `pkg/llm` directly.
- The `CortexSession` god-object (`main.go:1882`, ~80 fields spanning
  retrieval, capture, distillation, metrics, rendering, deletion
  policy). Decompose into focused structs.
- Qwen XML tool-call parsing (`pkg/llm/xml_tool_calls.go`). Native
  tool calling only.

### 2.2 Study — Size-Adaptive Reading (internal/study)

**What it is:** Two-phase reading: mechanical fractal sampling of
byte/line regions (no LLM, deterministic) followed by agentic
inference over only the sampled regions. Cost is bounded by density,
not file size. Relevance-driven deepening: a sparse first pass
surfaces leads, the next pass targets them.

**Proven:** The eval suite showed P1 (accumulating findings prefix) is
a large win, P2 (curation budget) validates, P4 (cacheable prefix)
confirmed. The provenance contract — every claim attributed to a
sampled region's real line range, hallucinated lines stripped
(`ValidateCitations`, `infer.go:230`) — is the moat.

**Keep:**
- Sample → infer → deepen, with the covered set carried forward.
- The provenance contract and citation validation.
- Working memory (prior findings ride the prompt front, cache-stable).
- The curator's DONE/DENSIFY/TARGET decision (`curator.go`).
- **The cache-stable prefix** — `CacheablePrefix` (`infer.go:156`)
  produces an append-only `[system][goal][findings]` head across
  passes, so a backend's prefix cache reuses it.
  `PrefixWarmPasses`/`PrefixBreaks` (`study_loop.go:47`) measure cache
  warmth. A real cost optimization.

**Simplify:**
- One study engine, not two. The background accumulator
  (`controller.go`) and the interactive tool share the same sample →
  infer core; the accumulator just runs it in a loop over the project.
- `map` as the structural layer (see §3).

### 2.3 Map — Structural Orientation (internal/projectindex)

**What it is:** A cheap, deterministic, no-LLM structural map: file
tree with sizes, Go declaration symbols via `go/ast`, directory
grouping. Already wired as `project_index` and used by `read_file`
for the too-large-file redirect.

**Keep:** The whole thing — it's already simple and right. Extend
with fixture/vendor detection, goal-aware filtering, and session
transcript support (the session map from the study-map design doc).

### 2.4 Cognition — The Five Modes (internal/cognition)

**What it is:** Five cognitive modes:
- **Reflex** — fast mechanical retrieval (embeddings, tags, recency).
- **Reflect** — LLM-based reranking and contradiction detection.
- **Resolve** — decides injection timing (now, wait, queue) with
  thresholds: `InjectThreshold` (0.5), `QueueThreshold` (0.3),
  `WaitThreshold` (0.2), `MinInjectionQuality` (0.2), plus a
  proactive queue from Dream.
- **Think** — background processing during active work, budget decays
  with activity.
- **Dream** — deep exploration during idle, budget grows with idle
  time, samples diverse sources.

**Proven:** The Think/Dream inverse resource model is genuinely novel
— thinking exhausts, dreaming explores. The `ActivityTracker`
(`activity.go`: `RecordRetrieve`, `IsIdle`, `ActivityLevel`,
`ThinkBudget`, `DreamBudget`) is the concrete trigger mechanism.

**Keep:**
- The five-mode conceptual model.
- Reflex as the fast path (mechanical, no LLM).
- Think/Dream's inverse budget model via `ActivityTracker`.
- Dream sources as a pluggable interface (`internal/cognition/sources/`).
- **Resolve's threshold-based injection** — not just "search and
  inject" but inject/queue/wait with a quality gate and a proactive
  queue. The new `retrieve` should preserve this nuance.
- **The digest/consolidation** (`digest.go`) — after Dream, duplicate
  insights are consolidated. A maintenance function (dedup) that keeps
  the store from growing unbounded with near-duplicates.

**Simplify:**
- Collapse Reflex/Reflect/Resolve into a single `retrieve` function
  with a `full` parameter. But preserve the threshold logic — it's
  not just "search and format," it's "search, score, decide whether
  to inject now / queue for later / wait for more."
- Think and Dream become background tasks with a budget, not
  first-class modes with their own journal entry types and state
  machines.

### 2.5 The DAG — Seed-and-Grow Execution (pkg/cognition/dag)

**What it is:** A tree-walking executor where each node may spawn
children under a decaying budget. Key pieces:

- **Budget** (`budget.go`) — five axes: `LatencyMS`, `Tokens`, `Depth`,
  `OutputTokens`, `MaxContextTokens` (non-decaying, the model's
  n_ctx). Plus `Provider` (per-node) and `Intent`/`Scope` (from sense
  ops).
- **Per-node provider routing** — `Router` (`router.go`) resolves the
  provider per node: explicit override → config routing map → Requires
  chain → session default. The config routing map
  (`"decide.tool_call": "qwen3-1.7b"`) is the operator escape hatch.
- **Salience contracts** (`salience.go`) — `SalienceContract{
  MaxOutputTokens, Intent}`. Not just "compress to N tokens" but
  "compress to N tokens preserving what the upstream consumer needs."
  This is how tool outputs are bounded.
- **Turn state** (`turn_state.go`) — a per-turn KV map. After each
  node completes, its `NodeResult.Out` is deposited. Downstream
  handlers read via `PriorOut`/`PriorOutByName`/`AllPriorOutputs`.
  This is how multi-node plans compose: a synthesis node sees what
  earlier nodes produced.
- **DAG trace** — per-node `TraceEntry` with cost, budget, salience,
  routing. Written to `dag_traces.jsonl`. The observability layer that
  makes the DAG calibrate-able.

**Proven:** Bounded emergence — cheap tasks → small graphs, complex
tasks → larger graphs, quality flattening at a knee. Per-node routing
lets you use a small model for classification and a big model for
generation.

**Keep:**
- Seed-and-grow with decaying budget.
- Per-node provider routing with the config escape hatch.
- The salience contract (output budget + intent).
- Turn state (how sub-tasks share results).
- DAG trace (observability for calibration).

**Simplify:**
- The op registry: 25 handlers → ~10. Most are thin wrappers around an
  LLM call with a prompt template. Collapse to mechanical ops (embed,
  search, sample) + a generic "LLM op" that takes a prompt template.
- **The DAG should be the loop's engine, not a parallel system.**
  The turn cycle IS a DAG: sense (user input) → decide (plan) → act
  (tool calls) → attend (observe results) → repeat. A simple turn is
  a degenerate one-node DAG; a complex turn grows. Same engine,
  different depth. This is the central unification.

### 2.6 The Journal (internal/journal)

**What it is:** An append-only log with typed entries, segmented by
class, with cursors, replay, and an indexer.

**Proven:** The append-only + replay design is sound. The dream
insight journal is how study's background accumulator persists
findings.

**Keep:**
- Append-only entries with typed payloads.
- Replay (the journal alone reconstructs state).
- Segmentation by class.

**Simplify:**
- The entry type explosion — 13 payload types
  (`DreamInsightPayload`, `ThinkTopicWeightPayload`,
  `ReflectRerankPayload`, `ObservationPayload`, `FeedbackPayload`,
  `ResolveRetrievalPayload`, `EvalCellResultPayload`, etc.), each with
  its own `New*Entry`/`Parse*` pair. Collapse to one `Entry` type with
  a `Kind` string and a `Payload json.RawMessage`. Typed accessors
  become helpers, not separate types.

### 2.7 Storage (internal/storage)

**What it is:** SQLite with embedding vectors, 11 public record types
(Event, Insight, Entity, Relationship, Feedback, Retrieval,
Contradiction, Observation, SessionContext, Session, EmbeddingContent)
and 73 methods on `*Storage` — a 2,517-line god-file.

**Proven:** SQLite is the right choice — embedded, zero-config,
portable. The vector search works.

**Keep:** SQLite + embeddings.

**Simplify:**
- The record type explosion. Collapse to a single `Item` table:
  `id, kind, content, embedding, tags, score, created_at`.
  Entities/relationships become items with kind="entity"/"relationship".
- The storage interface: `Put`, `Get`, `Search`, `Recent`. Everything
  else is built on top.

### 2.8 LLM Providers (pkg/llm)

**What it is:** A provider abstraction across OpenRouter, OpenAI-
compat, Anthropic, Ollama, Claude CLI. With tool calling, streaming,
embedding, context overflow detection, model recommendation, and
provider swapping.

**Proven:** Multi-model leverage — the thesis claim. The
recommendation logic (`recommend.go`: prioritize local endpoints,
larger context windows, smaller models for fast roles) works.

**Keep:**
- The provider interface.
- Model recommendation by role.
- Streaming.
- Tool calling (native JSON; drop XML).
- **Prompt caching** — `applyPromptCache` (`cmd/loop/main.go:463`)
  marks Anthropic prompt-cache breakpoints on the wire. The
  `cacheControl`/`contentPart` mechanism is Anthropic-specific (other
  backends auto-cache on prefix). A real cost optimization — the study
  loop's cache-stable prefix depends on it. The new provider layer
  should preserve cache breakpoints as a first-class concern (today
  it lives in the loop; it belongs in the provider).
- **Self-calibration** — `learnedWindows` (`main.go:589`) caches
  context windows discovered from overflow errors. `parseCtxSize`
  (`main.go:595`) extracts the real n_ctx from a provider error
  message. When a model overflows, the loop learns the real window and
  retries correctly sized. A resilience pattern the loop needs. (Today
  in the loop; the `ContextOverflowError` it depends on is already in
  `pkg/llm/context_overflow.go`.)
- **Context overflow handling** — `ContextOverflowError` with
  `AvailableTokens` and `RequestedTokens`
  (`pkg/llm/context_overflow.go`). The harness loop has a
  catch-and-retry path (`loop.go:395`) that trims the prompt and
  retries. Essential for long sessions.
- **The model swap tracker** — `SwapTracker` (`pkg/llm/swap.go`)
  records which model is loaded on each endpoint. Single-residency
  endpoints (local Ollama with one loaded model) have a 7-15s swap
  cost. The router should prefer no-swap routing when quality is
  comparable. This matters for local model usage.
- **Per-model output token caps** — `ModelMaxOutputTokens`
  (`internal/harness/model_caps.go`) knows that Claude supports 16K
  output, Qwen Coder 8K, gpt-oss 4K, etc. A practical detail that
  prevents truncated generations. (Today in `internal/harness`; belongs
  in `pkg/models`.)
- **Temperature control via env var** — `CORTEX_TEMPERATURE`
  (`pkg/llm/temperature.go`) survives the subprocess boundary (evals
  shell out to a fresh cortex per cell). Matters for eval
  reproducibility.

**Simplify:**
- The provider-specific files can be fewer. Most OpenAI-compat
  providers share one implementation (`openai_compat.go`). Anthropic
  is the one genuine variant (cache control, content parts).
- Drop the probe files (`probe_ollama.go`, `probe_openai_compat.go`,
  `probe_openrouter.go`) — model discovery should be a single
  `Models()` method, not per-provider probe logic.

### 2.9 Shell Risk Classification (internal/shellrisk)

A classifier that labels shell commands as Safe, Risky, or Blocked,
replacing a static allowlist. Small and right. Keep as-is.

### 2.10 Line Editing (internal/lineedit)

A terminal line editor with anchored prompts, type-ahead, and live
display during turns. Keep as-is. The anchored prompt mechanism
(redirect stdout to a pipe, feed lines to the anchor, user types while
the model streams) is the UX innovation.

### 2.11 The Capture Pipeline (internal/capture + pkg/events)

**What it is:** The write side of the learning loop:
`Capture → Filter → Store → Retrieve → Inject`. The `Event` struct
(`Source`, `EventType`, `ToolName`, `ToolInput`, `ToolResult`,
`Prompt`, `Context`) is the generic format. Capture is <10ms target.
The filter uses `SkipPatterns` to skip routine bash commands.

**Proven:** Every completed turn is captured — even read-only turns,
because a mechanical filter can't tell a durable lesson (a stated
preference, a correction) from noise. Tier 2 (a model, async) distills
the durable unit later.

**Keep:**
- The event format (generic, tool-agnostic).
- The <10ms capture target.
- The skip-pattern filter.
- **`turnArtifacts`** (`main.go:2869`) — the pure function that
  mechanically extracts files edited, commands run, and the final
  answer from a turn's messages. This is the seed of *both* the
  capture pipeline (the write side of learning) *and* the session map
  (the structural layer for compaction). The new design should
  recognize this unity — one extraction function feeds both.

**Simplify:**
- The event type is over-specified for what's actually used. Most
  events are `tool_use` with a tool name and input. Collapse the
  reserved-but-unused types.

### 2.12 The Working Memory Design (docs/working-memory.md)

**What it is:** A design (not fully implemented) for treating the
context window as a *managed working set*, not an append-only log.
The key reframe: **"the window is a cache, the journal is memory."**
Eviction is demotion, not deletion — the item stays in the journal
and is recallable via retrieval.

**The curation loop:**
1. **Score** — rank items by relevance to the current focus.
2. **Triage** — given a token budget, bucket each item: keep
   verbatim / compress / evict.
3. **Compress** — only the compress bucket goes through compression.
4. **Evict** — drop from the working set; remains in the journal.

**Item-level context model:** each message carries `kind` (system /
user / assistant-prose / tool-call-pair / digest), `turn` (when it
entered), `pinned` (never evict), and `atomic group` (an
assistant+tool-call pair moves together or not at all).

**Keep:**
- The "window is a cache, journal is memory" principle.
- The keep/compress/evict triage.
- Item-level metadata (kind, turn, pinned, atomic group).
- **The connection to study** — study's working memory (prior findings
  carried across passes) and the session's working memory
  (keep/compress/evict across turns) are the *same pattern at
  different scales*. Both are "bounded working set + eviction to a
  durable store." The new design should recognize this unity.

### 2.13 Change Isolation (cmd/loop/change.go)

One change at a time. The persistent loop works on a single branch
dedicated to the active change, so edits stay isolated and reviewable.
Git primitives for branch isolation — local only by design (nothing
pushes or opens a PR). A workflow innovation that keeps the loop's
work reviewable. Keep.

### 2.14 Config (pkg/config)

`.cortex/config.json` with endpoints, models, routing (per-node-type
model overrides), default model, mode tuning. The `findUp`/
`findConfigPath` discovery walks up from the workdir.

**Keep:** The config file format, the routing map (operator escape
hatch for per-node model pinning), the endpoint discovery (multiple
OpenAI-compat servers).

**Simplify:**
- Collapse the mode tuning — the five-mode config becomes four
  function configs (retrieve, distill, dream, compact).
- Drop the web port, the graph/vector feature flags, the daemon
  settings.

### 2.15 Project Scan (internal/projectscan)

The ignore system that both `map` and `study` use. Handles
`.gitignore`, vendor/build dirs, secrets. Keep as-is — it's the
shared file-discovery layer.

### 2.16 The Measure System (internal/measure)

Measures "promptability" — how good a prompt is for a given model.
Mechanical metrics (action verbs, conditionals, constraints, vague
patterns) + agentic metrics (via LLM). Produces a `Promptability`
score and a `Grade`. Keep the concept — it's an eval tool that could
verify the new project's prompts are well-shaped. Defer the
implementation until the core is built.

---

## 3. The MECE Core — A Single Organizing Principle

The rebuild organizes around **one loop, four subsystems, one store**:

```
                    ┌─────────────────────────────────────┐
                    │            The Loop                 │
                    │  (turn cycle IS a DAG:              │
                    │   sense → decide → act → attend     │
                    │   → repeat, growing from a seed     │
                    │   under a decaying budget)          │
                    └────────┬───────────────┬────────────┘
                             │               │
              ┌──────────────▼──┐    ┌───────▼────────────┐
              │     Tools       │    │     Cognition      │
              │  (map, study,   │    │  (retrieve, distill│
              │   read, write,  │    │   dream, compact)  │
              │   edit, bash,   │    └───────┬────────────┘
              │   remove)       │            │
              └─────────────────┘    ┌───────▼────────────┐
                                     │      Store         │
                                     │  (SQLite + journal │
                                     │   + embeddings)    │
                                     └────────────────────┘
                             │
              ┌──────────────▼──────────────┐
              │         Providers           │
              │  (LLM access, routing,      │
              │   caching, calibration,     │
              │   overflow recovery)        │
              └─────────────────────────────┘
```

### The Loop

One loop, DAG-driven. The turn cycle is the seed; the DAG's seed-
and-grow is how a turn expands into sub-tasks when needed. A simple
turn (user → model → tools → done) is a degenerate DAG with one node.
A complex turn (user → plan → sub-task 1 → sub-task 2 → synthesize)
is a DAG that grows. Same engine, different depth.

**The working set is a cache, the journal is memory.** The live
message list is the hot cache; cold history is one retrieval away.
Compaction is not "summarize everything" — it's keep/compress/evict
per item, with eviction being demotion to the journal (recallable),
not deletion.

**Safety nets:**
- No-progress detection (novelty-based, not write-based).
- Context overflow recovery (catch → learn real n_ctx → trim →
  retry).
- Auto-compact at 80% window fill (the gauge goes red).

### Tools

Seven tools, each with a clear cell:

| Tool | What | Cost | When |
|------|------|------|------|
| `map` | Structure (tree, symbols, session skeleton) | Free | First — orient |
| `read_file` | Exact content of a small file | Free | Know what you want, it fits |
| `study` | Analysis of a large target | LLM | Don't know where to look |
| `write_file` | Create/overwrite a file | Free | Writing |
| `edit_file` | Find/replace in a file | Free | Modifying |
| `bash` | Run a shell command | Free | Need to execute |
| `remove_path` | Delete a file/dir | Free | Cleaning up |

`map` and `study` are the producer/consumer pair. `read_file` and
`bash` are the precision tools. The file mutators are self-
explanatory.

**Tool outputs are bounded by salience contracts.** A tool that
returns a large output (bash, read_file on a big file) deposits into
turn state with a `MaxOutputTokens` cap and an `Intent` string. The
compression step preserves what the consumer needs, not just "first N
tokens." This is the `salience-budgets.md` principle.

### Cognition

Four functions, not five modes:

- **`retrieve(query, full?) → results`** — fast mechanical search
  (Reflex), with optional LLM reranking (Reflect) when `full`. But
  preserve Resolve's threshold logic: results above `InjectThreshold`
  are injected now, above `QueueThreshold` are queued, above
  `WaitThreshold` are held. A quality gate filters clearly bad content.
  A proactive queue (from Dream) can inject ahead of query.
- **`distill(turn) → insights`** — background extraction of durable
  context from a completed turn. This is Think, simplified: a function
  that takes a turn and produces insights for the store. Uses
  `turnArtifacts` for the mechanical extraction.
- **`dream() → insights`** — background exploration during idle.
  Samples from sources (project files, store, git), produces insights.
  This is Dream, simplified. Uses the fractal sampling primitive
  (shared with study).
- **`compact(session) → digest`** — study over the session transcript,
  producing a compressed digest. But with the working-memory triage:
  keep verbatim / compress / evict per item, not uniform
  summarization. The session map (from `map`) is the structural input.

**Background tasks** (Think/Dream) are:

```go
type BackgroundTask struct {
    Name     string
    Trigger  func(activity ActivityLevel) bool  // active → think, idle → dream
    Budget   func(idle time.Duration) int        // decays or grows
    Run      func(ctx context.Context, budget int) error
    Interval time.Duration
}
```

The `ActivityTracker` (`RecordRetrieve`, `IsIdle`, `ActivityLevel`,
`ThinkBudget`, `DreamBudget`) is the concrete trigger — more proven
than the abstraction.

### Store

One store, three layers:

- **SQLite** — items (content + embedding + metadata), the durable
  searchable record.
- **Journal** — append-only log. Replay reconstructs the store.
  **The journal is memory; the live window is a cache.**
- **Transcripts** — JSONL session files. Simple, portable, `cat | jq`.

```go
type Store interface {
    Put(ctx context.Context, item Item) error
    Get(ctx context.Context, id string) (Item, error)
    Search(ctx context.Context, query string, limit int) ([]Item, error)
    Recent(ctx context.Context, kind string, limit int) ([]Item, error)
}
```

Everything else (insights, events, entities, relationships,
contradictions, observations) is an `Item` with a `Kind` field.

### Providers

One provider interface, one router:

```go
type Provider interface {
    Generate(ctx context.Context, req Request) (Response, error)
    Stream(ctx context.Context, req Request, on ChunkFunc) (Response, error)
    Embed(ctx context.Context, text string) ([]float32, error)
    Models(ctx context.Context) ([]Model, error)
}
```

The router picks the provider per request (or per node in the DAG),
using:
- The recommendation logic (local first, larger context for reasoning,
  smaller for fast roles).
- The config routing map (operator escape hatch for per-node pinning).
- The swap tracker (prefer no-swap routing on single-residency
  endpoints).
- Per-model output token caps (prevent truncated generations).

**Prompt caching** is a first-class concern: the provider marks cache
breakpoints on the wire (Anthropic) or relies on prefix auto-caching
(others). The study loop's cache-stable prefix and the loop's
system-prompt-stability both depend on this. (Today this lives in the
loop — `applyPromptCache` in `cmd/loop/main.go:463` — it belongs in
the provider layer.)

**Self-calibration** is built in: when a request overflows, the
provider error carries the real n_ctx; the loop learns it, caches it,
and retries correctly sized. The `ContextOverflowError` is caught and
handled, not fatal. (Today `learnedWindows`/`parseCtxSize` live in
the loop — they belong with the provider's calibration logic.)

### The Deeper Unifications

The MECE core achieves several unifications the current project misses:

- **The loop IS the DAG.** Not two systems, one wrapping the other.
  A simple turn is a one-node DAG; a complex turn grows. The turn
  state is how sub-tasks share results.
- **The window is a cache, the journal is memory.** Eviction is
  demotion, not deletion. Compaction is keep/compress/evict per item,
  not uniform summarization. This is the working-memory principle,
  applied at both the study scale (prior findings across passes) and
  the session scale (messages across turns).
- **Study's working memory and the session's working memory are the
  same pattern.** Bounded working set + eviction to a durable store.
  One mechanism, two scales.
- **`turnArtifacts` is the shared extraction primitive.** It feeds
  capture (the write side of learning) and the session map (the
  structural layer for compaction). One function, two consumers.
- **The fractal sampling primitive is shared by study and dream.**
  Study samples byte regions of a file; Dream samples regions of
  project files via sources. One sampler, two uses.
- **Prompt caching is a first-class concern.** The study loop's
  cache-stable prefix, the loop's system-prompt stability, and the
  provider's cache breakpoints are all the same optimization: keep
  the prefix stable so the cache hits.

---

## 4. Package Layout

```
cortex/
├── cmd/cortex/main.go          # CLI entry, flag parsing, dispatch
├── internal/
│   ├── loop/                   # The turn cycle + DAG engine
│   │   ├── loop.go             # Turn = DAG: sense → decide → act → attend → repeat
│   │   ├── dag.go              # Seed-and-grow executor (the loop's engine)
│   │   ├── turnstate.go        # Per-turn KV map (how sub-tasks share results)
│   │   ├── session.go          # Transcript lifecycle, compaction, working memory
│   │   ├── stream.go           # Streaming renderer, reasoning tails, context gauge
│   │   ├── tools.go            # Tool dispatch (map/read/study/write/edit/bash/remove)
│   │   └── safety.go           # No-progress detection, overflow recovery, auto-compact
│   ├── study/                  # Size-adaptive reading
│   │   ├── study.go            # Sample → infer → deepen
│   │   ├── sampler.go          # Fractal sampling (byte regions, turn-aligned)
│   │   ├── curator.go          # DONE/DENSIFY/TARGET decision
│   │   ├── infer.go            # Provenance-constrained digest
│   │   └── workingmem.go       # Prior findings (cache-stable prefix)
│   ├── map/                    # Structural orientation
│   │   ├── map.go              # File tree, symbols, session skeleton
│   │   └── relevance.go        # Goal-aware filtering, fixture detection
│   ├── cognition/              # Background cognition
│   │   ├── retrieve.go         # Fast search + optional rerank + threshold injection
│   │   ├── distill.go          # Turn → insights (Think, simplified)
│   │   ├── dream.go            # Idle exploration (Dream, simplified)
│   │   ├── digest.go           # Dedup/consolidation after dream
│   │   └── background.go       # BackgroundTask runner + ActivityTracker
│   ├── store/                  # Persistence
│   │   ├── store.go            # SQLite + embeddings (Put/Get/Search/Recent)
│   │   ├── journal.go          # Append-only log, replay
│   │   └── transcript.go       # JSONL session files
│   ├── llm/                    # Provider abstraction
│   │   ├── provider.go         # The interface + Request/Response
│   │   ├── router.go           # Per-request/per-node model selection
│   │   ├── cache.go            # Prompt-cache breakpoints (Anthropic + auto)
│   │   ├── calibrate.go        # Self-calibration, overflow recovery
│   │   ├── openai.go           # OpenAI-compat (covers most providers)
│   │   ├── anthropic.go        # Anthropic (cache control, content parts)
│   │   └── ollama.go           # Local models
│   ├── shellrisk/              # Shell command classification
│   ├── lineedit/               # Terminal line editor (anchored prompt, type-ahead)
│   ├── capture/                # Event capture (the write side of learning)
│   │   └── capture.go          # Event format, filter, turnArtifacts extraction
│   └── projectscan/            # File discovery + ignore (.gitignore, vendor, secrets)
├── pkg/
│   ├── config/                 # Configuration loading (.cortex/config.json)
│   └── models/                 # Model metadata, capabilities, output caps
└── eval/                       # Verification evals (minimal)
    ├── study_test.go           # Study produces grounded citations
    ├── loop_test.go            # Loop completes a coding turn
    ├── compact_test.go         # Compaction preserves key context
    └── retrieve_test.go        # Retrieval finds relevant prior context
```

**Design constraints:**
- **Simplicity.** Each package has one responsibility. No god-objects.
  `CortexSession` (80 fields) and the `Storage` god-file (2,517 lines,
  73 methods) are gone; the loop holds state in focused structs.
- **Portability.** SQLite (embedded), JSONL transcripts (plain text),
  no external services. `cat | jq` always works.
- **Flexibility.** Providers, tools, dream sources, and background
  tasks are all interfaces. New ones drop in without touching the
  loop.
- **Extensibility.** The DAG is the loop's engine, so extending the
  turn cycle (new op, new tool, new cognition mode) means registering
  a handler, not modifying the loop.

---

## 5. What Gets Dropped

- **The DAG op zoo (25 handlers, 17 registered).** Replaced by ~10:
  mechanical ops (embed, search, sample) + a generic LLM op with
  prompt templates.
- **The five-mode cognition pipeline as separate types.** Replaced by
  four functions.
- **The dual agent loops.** One loop, DAG-driven.
- **The dual study engines.** One study engine, used both
  interactively and by the background accumulator.
- **The entry type explosion in the journal (13 payload types).** One
  `Entry` type, `Kind` string, `json.RawMessage` payload.
- **The record type explosion in storage (11 types, 73 methods).** One
  `Item` table.
- **Qwen XML tool-call parsing.** Native tool calling only.
- **The probe files.** One `Models()` method per provider.
- **The `cmd/cortex` CLI with 33 subcommands across 35 files.** The
  CLI is `cortex` (REPL), `cortex study`, `cortex eval`. That's it.
- **The daemon, the web dashboard (`internal/web/`), the TUI
  (`internal/repltui/`).** The REPL is the interface.
- **The benchmark integrations** (SWE-bench, NIAH, LongMemEval, MTEB).
  The eval system keeps the unified `cell_results.jsonl` output format
  but drops the external benchmark integrations. Evals are
  verification tools, not the product.
- **Most of the 41 design docs.** They're archaeology. The new project
  starts with this document and the study-map design doc.

---

## 6. Evals — Verification, Not Ocean-Boiling

Four evals, each verifying one subsystem works:

1. **`study_test.go`** — Given a large file, study produces a digest
   with citations that validate against the sampled regions. Verifies
   the provenance contract.
2. **`loop_test.go`** — Given a simple coding task in a workdir, the
   loop completes it (edit a file, run a test). Verifies the turn
   cycle + tool dispatch + no-progress detection.
3. **`compact_test.go`** — Given a session transcript, compaction
   produces a digest that preserves the key context (user intent,
   files touched, decisions made). Verifies session-as-study with
   keep/compress/evict.
4. **`retrieve_test.go`** — Given a store with prior captures,
   retrieval finds relevant context for a query. Verifies the
   cognition pipeline + threshold injection.

Each eval is a Go test with a mock provider (scripted responses) and a
temp workdir. No external services, no fixtures from other projects,
no LLM judging. They verify the machinery works; quality evaluation
comes later.

**The unified output format is preserved:** `cell_results.jsonl` —
one row per eval cell with model, tokens, cost, latency, pass/fail.
This is the eval contract from the current project, and it's right.

---

## 7. Build Order

The rebuild in phases, each independently testable:

| Phase | What | Verifies |
|-------|------|----------|
| 1 | `internal/llm/` — provider interface + OpenAI-compat + cache + calibrate | Can call a model, cache, recover from overflow |
| 2 | `internal/store/` — SQLite + journal + transcript | Can persist and search |
| 3 | `internal/map/` + `internal/projectscan/` — file tree + symbols + session skeleton | Can orient |
| 4 | `internal/study/` — sample → infer → deepen + working memory | `study_test.go` |
| 5 | `internal/loop/` — turn cycle + tool dispatch + streaming + safety nets | `loop_test.go` |
| 6 | `internal/cognition/` — retrieve + distill + dream + background + digest | `retrieve_test.go` |
| 7 | `internal/loop/session.go` — compaction with keep/compress/evict | `compact_test.go` |
| 8 | `internal/capture/` — event capture + turnArtifacts | Captures feed the store |
| 9 | `cmd/cortex/main.go` — CLI entry, REPL, anchored prompt | End-to-end |
| 10 | `internal/loop/dag.go` — seed-and-grow for complex turns | DAG eval |

Phases 1–4 are the foundation (provider, store, map, study). Phase 5
is the loop — the thing at the center. Phases 6–8 add cognition,
compaction, and capture. Phase 9 wires the CLI. Phase 10 adds the DAG
for complex turns — it's last because a simple turn (one node) should
work before a complex turn (growing DAG) does.

**Can this be done in one session?** Phases 1–5 (the foundation +
loop) are the core, and the current codebase proves each piece works.
Phases 6–10 are extensions. The key is starting with the MECE layout
and not carrying forward the accreted complexity.

---

## 8. What This Preserves from the Original Vision

The CLAUDE.md states three thesis claims:

1. **Multi-model leverage.** Preserved — the provider router picks the
   right model per request/node, with the config escape hatch, the
   swap tracker, and per-model output caps.
2. **Learning over time.** Preserved — capture → store → retrieve →
   inject, with distillation and dreaming as background tasks. The
   `turnArtifacts` extraction feeds both capture and the session map.
3. **Bounded emergence.** Preserved — the DAG's seed-and-grow with
   decaying budget produces task-appropriate complexity. The salience
   contract bounds output. The turn state lets sub-tasks compose.

What changes is the **composition**: one loop (DAG-driven), one store
(cache + memory), one provider interface (with caching and
calibration), one study engine (with working memory), one map (the
structural layer) — organized MECE, built simple, designed to extend.
