# Salience Budgets

> **Purpose.** Add a third bounded-emergence axis — *output size* — to the
> DAG protocol. Each spawned node carries a budget for how many tokens it
> may deposit into turn state and a salience intent string describing
> what the upstream consumer is going to do with the output. Tight
> budgets force the compressor to extract only what matters; loose
> budgets allow verbose passthrough. The orchestrator allocates output
> budget per spawn the same way it allocates fanout.
>
> **Status.** Design. Phase 1 (Budget extension + SalienceContract on
> NodeSpec + attend.compress stub) is the minimum to unblock the
> calibration-friendly trace shape; Phases 2-3 land the application
> sites.
>
> **Owner.** `pkg/cognition/dag/` + `internal/harness/dagnode/`.

---

## Why

Three observations stack:

1. **The DAG budget already bounds two axes** — work-side (`LatencyMS`,
   `Tokens`) and shape-side (`Depth` + `MaxFanout`). It does not bound
   what a node may *leave behind* in turn state. A single `act.read_file`
   on a 64 KiB file deposits 64 KiB into the next node's prompt; the
   executor charges the read's own cost but does nothing about the
   downstream context blast.

2. **Tool-calling APIs are stateless** — every `POST /chat/completions`
   carries the full transcript. Inner agent loops that today eat 75k
   cumulative input tokens by turn 8 are paying linearly per turn for
   uncompressed prior tool outputs. The "send the whole conversation
   back" geometry is the protocol, not a bug — but it is the dominant
   driver of context exhaustion in long sessions.

3. **The small-model amplifier story has been hand-wavy on what
   "salience" actually means**. Tying compression bytes to a budgeted
   token count gives salience a concrete operational definition: the
   salience layer is the thing that fits high-information signal into
   a budgeted byte count under a stated intent. Tighter budget forces
   harder salience. Looser budget allows passthrough.

The structural fix: extend `dag.Budget` with an output-token axis, let
parents allocate it per spawn, and treat the compression step itself as
a `attend.compress` node so it is observable, traceable, and
calibrate-able like any other op.

---

## Three-axis budget

`pkg/cognition/dag/budget.go` today:

```go
type Budget struct {
    LatencyMS int
    Tokens    int
    Depth     int
}

type Cost struct {
    LatencyMS int
    Tokens    int
}
```

Extended:

```go
type Budget struct {
    LatencyMS    int
    Tokens       int
    Depth        int
    OutputTokens int // total tokens nodes may deposit into turn state this turn
}

type Cost struct {
    LatencyMS    int
    Tokens       int
    OutputTokens int // tokens this node deposited into turn state
}
```

`Budget.Consume` subtracts on all three numeric axes. `Exhausted`
returns `output_tokens` as a fourth axis name. `CanAfford` gains an
`OutputTokens` check. `DefaultTurnBudget` seeds `OutputTokens: 8000` —
roughly half a 16k context window, since the rest is system prompt +
tool defs + active assistant message.

This is the minimum extension. No node behavior changes yet; the axis is
inert until the SalienceContract on NodeSpec drives it.

---

## SalienceContract on NodeSpec

`NodeSpec` gains two fields the *parent* sets at spawn time:

```go
type SalienceContract struct {
    // MaxOutputTokens is the cap on what this node may deposit into
    // turn state. Zero means "no contract" — the node deposits raw
    // output (Phase 1 default).
    MaxOutputTokens int

    // Intent is a short string telling the compressor what the
    // upstream consumer cares about. Used as the compression prompt.
    // Empty means "preserve most salient content for general use."
    Intent string
}

type NodeSpec struct {
    // ... existing fields
    Salience *SalienceContract // nil on seed nodes; set by spawning parents
}
```

The orchestrator (`decide.next` / `decide.coding_turn` when it spawns
act.* children) populates `Salience` on each child based on:

- remaining turn `OutputTokens` budget
- number of children being spawned
- the parent's own knowledge of what it needs back

Default allocation policy (Phase 1, before calibration): split
`OutputTokens / num_children`, with a per-op floor (act.list_dir: 60,
act.read_file: 400, decide.coding_turn: 1500). Calibration replaces this
with observed-quality curves in Phase 3.

---

## attend.compress as a real op

Compression is a node, not a side effect. It's an attend-typed op — the
cortex function set uses `attend` for any op whose job is to focus on
the salient and discard the rest (peer of `attend.rerank`). The
conceptual mode is Reflect-style (the cognitive mode in
`internal/cognition`), but the DAG function naming follows the
cortex-function taxonomy.

```
function:    attend
op:          compress
inputs:      raw (string), intent (string), max_tokens (int)
outputs:     compressed (string), original_tokens (int), kept_tokens (int)
cost hint:   { LatencyMS: 800, Tokens: 200 }
exposable:   false (used by executor, not LLM-composable)
```

The executor wires it in: after any node returns `Out`, if the node had
a `Salience` contract and the output exceeds `MaxOutputTokens`, the
executor synthesizes a `attend.compress` child with the raw output +
intent + budget. The child's compressed output replaces the parent's
`Out` in turn state. The original goes to the journal — recoverable via
`cortex journal show <offset>` but never enters the next prompt.

Three properties this gives us:

- **Observable**: every compression shows up as a trace row, with cost,
  input/output sizes, and the intent string.
- **Traceable**: bad compression manifests as `attend.compress · err`
  or as a downstream `decide.coding_turn` answer that is wrong because
  it missed a fact — both visible in the trace.
- **Calibrate-able**: same `cost_consumed` measurement loop as every
  other op. Per-intent compression-quality curves fall out of normal
  trace analysis.

The compressor itself is a small-model call. The Cortex Reflect
prompts already exist for retrieval reranking; a sister prompt for
tool-result compression is the only new piece of prompt engineering.

---

## Calibration loop

Every `attend.compress` row carries:

```
{
  intent:           "find TODOs in Go file",
  original_tokens:  860,
  budget_given:     120,
  kept_tokens:      118,
  downstream_ok:    true,   // populated after the parent synthesizes
}
```

`downstream_ok` is back-filled by the parent node when it returns: if
the synthesis answer scored well against the user prompt (or in tests,
against the ground truth), the compression was sufficient. Logging this
turns the budget-quality curve into a learnable thing:

```
For (op="act.read_file", intent~"find TODOs in Go file"):
  budget=60   → downstream_ok rate 0.30
  budget=120  → downstream_ok rate 0.75
  budget=240  → downstream_ok rate 0.78
  budget=480  → downstream_ok rate 0.79

Elbow at ~120. Future spawns of this shape get budget=120 by default.
```

This is the learning-over-time claim from
[`docs/eval-strategy.md`](eval-strategy.md), applied to the knob that
most directly controls context survival.

---

## Three application sites

The same primitive applies at three scales:

### 1. Inner agent loop (per-turn)

Today: `decide.coding_turn` opens an inner `for turn := 0; turn <
maxTurns` loop. Each `act.read_file` deposits the full file into the
transcript that the next inner-turn sends back to the model.

With salience budgets: each tool result is passed through
`attend.compress` with intent = the user's prompt and a budget
allocated from the inner loop's remaining context. The transcript
carries `"foo.go: defines X, exports Y, see L120-140"` instead of 4 KB
of source. Cumulative input across 8 inner turns drops from ~75k to
~12k. The 8-turn cap is no longer the binding constraint — context
exhaustion was.

### 2. Cross-turn context (per-session)

Today: the REPL re-injects prior user/assistant turn pairs verbatim
into each new turn (`historyTurnsOverride`). Long sessions hit the
context wall.

With salience budgets: at end of every turn, the DAG's trace +
synthesis result is summarized into a `think.session_summary` entry
under a session-wide compression budget. Next turn pulls the summary,
not raw history. Theoretically infinite session length, in practice
bounded by Chinese-whispers risk (mitigated by journal-as-recovery).

### 3. Cross-session bootstrap (per-project)

Today: the REPL starts cold every session.

With salience budgets: the prior session's final summary is itself
compressed under a project-level budget and exposed via
`cortex_search` as the seed context for the next session. The Cortex
journal already gives you the substrate; the only new piece is the
project-summary writer-class entry.

All three are the same primitive applied at three scales.

---

## Failure modes

Three real risks. Spike-1 work plans for each:

1. **Compressor floor**: if the small model can't reliably produce a
   salience-preserving summary, garbage propagates. Mitigation: log
   `downstream_ok` per compression call; when an intent class shows
   <50% downstream_ok across N samples, fall back to passthrough +
   higher budget for that intent class. Detect-and-degrade beats
   silent corruption.

2. **Intent infidelity**: orchestrator says "preserve TODO-relevance,"
   compressor preserves something tangentially related. Hard to
   detect without re-reading the original. Mitigation: the journal
   keeps raw output; a `cortex journal verify` extension can spot-check
   compressed outputs against originals on a sample.

3. **Recursion / Chinese whispers**: cross-turn summary is itself
   compressed-of-compressions. After 20 turns, working memory is a
   summary of summaries. Mitigation: every compression keeps a journal
   offset pointer back to the raw artifact. When `downstream_ok`
   degrades on a long session, an explicit "rehydrate" op re-reads
   the original artifacts via journal offsets.

---

## Eval to falsify

**Hypothesis**: Cortex Fast mode + salience budgets matches Full mode
quality on a fixed workload while staying inside a 16k context window
for 50+ sequential turns.

**Setup**:

- Workload: a 50-turn synthetic session with progressive context
  accumulation (each turn references prior content).
- Baseline (Full, no budgets): plain inner loop, full transcript,
  measure point of context exhaustion.
- Treatment (Fast + salience): budgets active, attend.compress
  in line.
- Metric: `(turns_completed_before_context_exhaustion,
  answer_quality_at_turn_50)` pair.

**Pass criteria**: Treatment completes all 50 turns *and* answer
quality at turn 50 within 5% of baseline-at-its-final-turn (baseline
is expected to exhaust before turn 20 with this workload). Fail
either condition → falsifies the thesis, design needs rework.

---

## Phased delivery

### Phase 1 — Budget extension + SalienceContract type

- Add `OutputTokens` to `Budget` and `Cost` (with `Consume`, `Exhausted`,
  `CanAfford`, `DefaultTurnBudget` updates).
- Add `SalienceContract` and `NodeSpec.Salience`.
- Register `attend.compress` as a passthrough stub (returns input
  verbatim if under budget; truncates with `…` marker if over). No
  LLM call yet — proves the wiring, not the salience quality.
- Update the existing dag_traces.jsonl writer to include
  `salience_intent` and `output_tokens` columns.

Outcome: the trace gains the columns the calibration loop will need,
but behavior is unchanged. Risk-free pin for the protocol surface.

### Phase 2 — Real compressor + inner-loop application

- Replace the passthrough stub with a Cortex Reflect-style small-model
  compression call. Prompt mirrors the existing rerank prompt
  (concise, deterministic, returns structured output).
- Wire the executor's post-handler hook so any node with
  `Salience.MaxOutputTokens > 0` and `output_tokens > MaxOutputTokens`
  triggers a `attend.compress` child.
- Apply to the inner agent loop: when `decide.coding_turn` spawns act.*
  via the dispatcher, attach a SalienceContract derived from the
  remaining inner-loop output budget.

Outcome: the 8-turn agent loop no longer exhausts context on the same
workloads it does today. Eval: re-run the "tell me about this codebase"
trace under chatterbox 4k ctx — turn limit hit goes away, answer lands.

### Phase 3 — Cross-turn summary + calibration

- Add `think.session_summary` writer-class entry. Each REPL turn
  finalizes by spawning a `think.session_summary` node with the turn's
  DAG trace as input.
- Replace the REPL's `priorMessages` array with a "summary of last K
  turns" string assembled from the writer-class projection.
- Stand up the calibration loop: per-intent budget-quality curves get
  fitted nightly from `dag_traces.jsonl`; default budget allocations
  for each (op, intent_class) pair update accordingly.

Outcome: long sessions become viable. Cross-session bootstrap is a
copy-paste of Phase 3 with `project_summary` writer-class instead of
`session_summary`.

---

## Open questions

- **Intent class discovery**: hand-curated taxonomy ("find TODOs",
  "read manifest", "summarize file") vs. embedding-clustered? Phase 3
  decision; Phase 2 uses a flat string passthrough.
- **Compressor model selection**: does the same small model serve
  every intent, or does a coding intent prefer a coder model? Phase 2
  default: shared small model; calibration data answers the question.
- **Budget allocation policy**: split evenly vs. learned per-op
  priors. Phase 1 uses fixed per-op floors; Phase 3 learns.
- **Failure recovery UX**: when a compression flags itself as
  potentially lossy, does the orchestrator re-spawn with a higher
  budget or rehydrate from the journal? Phase 2 always rehydrates;
  Phase 3 may choose.
