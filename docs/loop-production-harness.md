# Loop → Production Harness

What `cmd/loop` needs to become a production-ready coding harness for local and
open-source models — broken into mutually exclusive, collectively exhaustive
parts, with an explicit "what we don't build" list. Informed by pi.dev,
opencode, and Claude Code (research notes summarized at the bottom).

> Guiding bias: pi.dev's philosophy — "primitives, not features." The loop is
> already the right shape (single bounded agent loop, small tool set, one wire
> protocol). Production readiness is mostly hardening and persistence, not
> surface area. Cortex's differentiators (study, multi-model roles, journal)
> stay; everything else stays out until an eval says otherwise.

## Where the loop stands today

~1,200 lines in `cmd/loop/main.go`, plus tests and the study-eval runner:

- Single bounded agent loop (`Resolve`): send → execute tool calls → feed
  results back → repeat, capped at 100 iterations. Tool errors are
  observations, not crashes.
- Five tools: `read_file` (size-guarded, redirects to study), `write_file`,
  `edit_file` (exact-unique-match), `study`, `bash` (binary allowlist, no
  shell interpretation).
- Role-based multi-model routing (`code` / `study`) over the OpenAI-compatible
  wire, configured in `.cortex/config.json`, keys from the macOS keychain.
- Qwen-native XML tool-call fallback parsing — small-model pragmatism the big
  harnesses don't need.
- Context-window self-calibration (learned from overflow errors) and a live
  context gauge in the status line.

That is already most of pi's core (pi ships 7 tools and the same loop shape).
The gaps are in robustness, persistence, and the context/memory economy.

## The six parts (MECE)

Each part owns one question. Nothing belongs to two parts.

### 1. Model plane — everything between the loop and a model

**Have:** OpenAI-compat wire (covers Ollama, llama.cpp, LM Studio, OpenRouter,
LiteLLM/chatterbox — one protocol is the simplicity win), role routing,
keychain keys, learned windows, XML tool-call fallback.

**Need:**
- HTTP timeout and bounded retry on `AgentRequest.Send` — today
  `http.Client{}` has **no timeout**; a hung local server hangs the REPL.
- Context-overflow handling for the *code* model (learned windows currently
  only serve study).
- Model catalog (existing TODO): a static table mapping system resources →
  suggested role bindings. A table, not a service. Prior art: models.dev.

**Don't:** a provider zoo (everything local speaks OpenAI wire); streaming
(polish, not production); native Anthropic/Gemini adapters until something
needs them.

### 2. Turn loop & action surface — what one turn may do

**Have:** the bounded loop, five tools, exact-match edit, allowlisted no-shell
bash (stronger than most harnesses' default), tool-output truncation.

**Need:**
- Interrupt: Ctrl-C cancels the in-flight model call / tool instead of killing
  the REPL (plumb `context.Context` through `Send` and `Execute`).
- Possibly a dedicated `grep` tool — opencode and Claude Code both found models
  use structured search tools more reliably than bash invocations; small
  models more so. Defer until eval evidence; `bash grep/find` covers it today.

**Don't:** subagents (DAG spawn is Cortex's own future mechanism — don't bolt
on a `task` tool now); todo tool (pi: "confusing to models," doubly so for
small ones); plan mode; MCP (pi's stance: CLI tools with READMEs).

### 3. Context economy — what enters the window and how growth is bounded

This is where the thesis lives; study is the differentiator.

**Have:** study (size-adaptive curated reading), read-size guard, tool-output
cap, live gauge with color pressure.

**Need:**
- ~~Project instructions file read at session start (`AGENTS.md`)~~ — done.
- ~~An answer for the red gauge~~ — done: **compaction IS study**, pointed at
  the session transcript. The study request carries the *consumer's* budget
  (a quarter of the code window, capped by study capacity), which is what
  forces study mode over read mode — to the study model the transcript might
  "fit", but fitting isn't the goal, compression is. Triggers: manual
  `/compact`, auto at the turn boundary when fill ≥ 80% (same threshold as
  the red gauge), and overflow recovery (a 400 naming the real window learns
  it, compacts, and asks for a re-send). The digest seeds a new transcript;
  the raw one stays on disk.
- ~~`/clear` as the manual escape hatch~~ — done (re-reads AGENTS.md,
  preserves the model binding, new transcript).

**Don't:** skills / dynamic context packages; RAG over the repo (study + grep
is the bet — measure before adding).

### 4. Sessions & memory — persistence across turns and sessions

The learning-over-time claim lands here. The journal infra already exists.

**Decided (2026-06-09):** raw session transcripts are NOT a journal
writer-class. They persist as plain JSONL under `.cortex/sessions/<id>.jsonl`
(pi-style) — the journal records distilled context (capture events, insights),
and raw conversations stay out of it. Capture *about* the session is the
journalling layer.

**Decided (2026-06-14): record-everything, label-for-replay.** The transcript
records EVERYTHING that happened in a turn — conversation, what was retrieved,
and compaction markers — each line tagged with a `kind`
(`message`/`retrieval`/`compaction`). Two axes that were being conflated:
*the record* (disk, full fidelity, "why did it do that") and *the live window*
(what's re-sent to the model). Persistence belongs to the first; window economy
to the second. `kind` bridges them: the record is complete, but replay is
selective. Core-vs-aux is a **label, not a storage decision**. Resume defaults
to **record-only** — it rebuilds the live window from `message` entries, so
retrieved context is on the record (inspect with `jq 'select(.kind=="retrieval")'`)
but not blindly re-fed (retrieval re-runs fresh against the new turn). Faithful
replay (re-feeding historical retrievals) is now a free policy flip if continued
sessions feel like they lost the thread.

**Need:**
- ~~Session transcript persistence~~ — done: every REPL session appends each
  message ambiently to its transcript; `loop resume [id]` continues the
  latest (or a named) session.
- Capture at turn end — the write side of the learning loop (retrieval is the
  read side; without capture the shelf is empty). Two tiers:
  - **Tier 1 (done): structural, mechanical, every turn.** At turn end the loop
    writes a capture to the *same store* retrieval reads (`capture.NewWithStorage`,
    so it's instantly findable) — the user's message as the key, plus an outcome
    line (files edited / commands run) and a capped answer excerpt, in
    `ToolResult` (the field Reflex surfaces as a hit's Content). **Every** turn,
    read-only included: a mechanical filter can't tell a stated preference or a
    correction from noise, and those live in read-only turns — dropping them
    would discard the lessons. (Reversed an earlier "mutating turns only"
    default, at the user's call: record-everything, judge-on-read, same as
    transcripts.) Plus `/remember <text>` — the highest-precision capture, since
    the user marked it. Best-effort, no model, no added foreground latency.
  - **Tier 2 (next): model-distilled insights, async on the reasoner.** Distill
    the durable unit (decision/correction/pattern) from a turn — including the
    read-only ones — assign a real type, supersede the raw Tier 1 rows. Runs
    off the foreground (parallel/idle on the reasoner) so it adds no turn
    latency; the honest caveat is that on a shared endpoint "parallel" is
    time-sharing, so it wants preemption / idle-budgeting (the Think
    integration).
- ~~Retrieval injection at turn start~~ — done: each turn runs **Fast**
  (mechanical Reflex) retrieval over `.cortex/`. The hits are **recorded** to
  the transcript (`kind:retrieval`) and **merged into the system message for
  the wire only** (`AgentRequest.EphemeralSystem`, composed at marshal time so
  stored `Messages` are never mutated), cleared after the turn so they don't
  accumulate in the live window. Reuses `internal/cognition` so reranking
  (Reflect/Think on the reasoner, in parallel) attaches later without changing
  the call site. Results are injected regardless of Resolve's inject/queue
  decision — that gate is embedding-scale-tuned and would suppress everything
  on the text-only path local setups use. Best-effort: disabled cleanly when
  the store can't open.
- Capture order note: we did retrieval *before* capture (inverting the planned
  order) at the user's call — Think integration is the natural home for the
  parallel-reasoner half, and Fast retrieval stands alone against captures from
  prior `cortex capture` use or other agents sharing `.cortex/`. Capture at
  turn end (structural always + model-distilled when a model is available,
  likely processed async on the reasoner, not inline) is the next slice.

**Don't:** session trees/branching (pi has it; we don't need it); sharing;
multi-session orchestration.

### 5. Safety & trust — what the harness may do without asking

**Have:** binary allowlist with no shell interpretation; edit-uniqueness
guard; `.cortex/` gitignore privacy invariants.

**Need:**
- A stated stance, which is pi's: **no permission popups**. The allowlist is
  the guardrail; anything riskier runs in a container. Document it.
- One cheap guard: refuse `write_file`/`edit_file` outside the working tree.

**Don't:** allow/ask/deny rule engine (opencode-style); OS sandbox; secrets
scanning. Each is real surface area with weak payoff at this scale.

### 6. Operability & quality instrumentation — knowing it works

**Have:** study-eval runner with repeat/aggregate, `cell_results.jsonl` sink
convention, status line, VCS-stamped version, table-driven tests.

**Need:**
- Per-session token/cost accounting (usage is already returned; sum it, show
  it at exit).
- Harness-level eval integration (existing TODO; the codebase-eval-suite
  handoff doc is the starting point) — the gate for every "Need" above.

**Don't:** dashboards (the daemon was retired for a reason); telemetry; logs
beyond the journal.

## The don't-build list, consolidated

MCP · subagents/task tool · plan mode · todo tool · permission rule engine ·
OS sandbox · streaming · session branching/sharing · skills/plugins · RAG
indexing · LSP integration · dashboards. Every one is something pi.dev or
opencode ships (or deliberately refuses); none is on the critical path to a
production loop, and several (subagents, skills) have better Cortex-native
answers later (DAG spawn, study).

## Suggested sequence

1. **Hardening micro-slice** (part 1+2): HTTP timeout + retry, Ctrl-C
   interrupt. Small, removes the two ways the REPL dies/hangs today.
2. **AGENTS.md injection** (part 3): one function, immediate quality lift.
3. **Session transcripts + resume** (part 4): plain JSONL under
   `.cortex/sessions/`, deliberately outside the journal. Done.
4. **Compaction-as-study** (part 3): the red-gauge answer, reusing the engine.
   Done.
5. **Retrieval injection** (part 4): the learning-over-time slice. Done
   (Fast/Reflex, ephemeral per-turn). Think reranking + capture-at-turn-end
   follow.
6. **Eval integration** (part 6): continuous, gating each step above.

Dream/Think/DAG integration (existing TODOs) deliberately come *after* this
sequence — bounded emergence needs a stable harness to emerge inside.

## Research notes (June 2026)

| Dimension | pi.dev | opencode | Claude Code |
|---|---|---|---|
| Loop | Single loop, user-steerable, no planner/subagents | Single AI-SDK loop, Plan/Build modes, `task` subagents | Gather→act→verify loop, plan mode, subagents |
| Tools | 7: read, write, edit, bash, grep, find, ls | ~11 (+MCP, +plugins) | ~12 (+MCP, +skills) |
| Context | Lossy auto-compaction; full JSONL kept; AGENTS.md | Auto-summarize at ~90% fill; LSP diagnostics injected | Tiered drop-then-summarize; CLAUDE.md + auto-memory |
| Local models | Yes (Ollama + any OpenAI/Anthropic-compat endpoint) | Yes, first-class (Ollama, llama.cpp, LM Studio) | No |
| Omits | MCP, subagents, permissions UI, plan mode, todos | Little; permissive defaults | Nothing (maximal surface) |

pi.dev sessions are JSONL trees under `~/.pi/agent/sessions/`; extensions are
the escape hatch for everything omitted. opencode's distinctive idea is LSP
diagnostics as grounded feedback after edits. Claude Code's is deterministic
hooks + skills around a closed loop.
