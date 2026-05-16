# Eval Coverage Matrix

> **Living doc.** The 10 dimensions are stable; the per-dimension status, the
> chosen upstream benchmark, and the gap-closing proxies will move as the
> roadmap lands. Update the status table when a benchmark wraps, a CLI surface
> ships, or a proxy is built. Record run-level findings in
> [`docs/eval-journal.md`](../eval-journal.md), not here.

This doc decomposes "what makes a chat-REPL coding harness really good" into
10 mutually-exclusive dimensions, maps each to the best available upstream
benchmark, and tracks where Cortex has coverage today vs. where the gap-closing
work lives on the roadmap.

It complements the existing benchmark docs:
[`overview.md`](overview.md) (package layout & how-to-add),
[`integrity.md`](integrity.md) (PR-review checklist),
and the per-benchmark docs (`swebench.md`, `niah.md`, `longmemeval.md`,
`mteb.md`). The framing principles those docs rest on are in
[`docs/prompts/eval-principles.md`](../prompts/eval-principles.md) — every
benchmark added under this matrix MUST satisfy those nine principles
(black box, no coaching, versioned, reproducible, isolated, structured,
LLM-judged variance, separated baselines, CLI-first gap-closing).

---

## The 10 dimensions

The full derivation lives in the README-level discussion that produced this
matrix; the short form:

| # | Dimension | One-line definition |
|---|---|---|
| 1 | Intent ingress | How the human gets a request into the system — clarification quality on ambiguous prompts |
| 2 | Grounding (world model) | What the harness knows about repo / env / tools without being told |
| 3 | Memory & continuity | What persists between turns and sessions |
| 4 | Planning & alignment | Turning intent into committed work; policy + plan-quality before acting |
| 5 | Execution | Doing the work — tool-calling, edits, shell, parallelism |
| 6 | In-flight observability | What the human sees while work is in progress |
| 7 | Steering & interrupt | Mid-flight control — accepting redirects, feedback, cancellation |
| 8 | Reversibility & safety | Bounding blast radius — confirmation, prompt-injection resistance, undo |
| 9 | Result presentation & verification | End-of-turn output — diff, summary, proof-of-work |
| 10 | Extensibility & composition | Adding capability without forking — skills, MCP, dynamic tools |

Dimensions 1, 4, 5, 6, 9 form the temporal arc of a single turn
(before → during → after). Dimensions 2 vs 3 split on origin (current
observation vs persisted state). Dimensions 6 vs 7 split on direction
(read vs write mid-turn). Dimension 8 is a property of dimension 5 but
named separately because its mechanisms (worktrees, gates, confirmation
prompts) are distinct. Dimension 10 is the meta-axis.

---

## Coverage matrix (cells)

Coverage strength: ✓ direct, ~ partial / substrate, ✗ none.

| # | Dimension | SWE-bench | NIAH | LongMemEval | MTEB | τ-bench | MINT | BFCL | AgentDojo | Cortex proxy |
|---|---|---|---|---|---|---|---|---|---|---|
| 1 | Intent ingress | ✗ | ✗ | ✗ | ✗ | ✗ | ✗ | ✗ | ✗ | **planned** |
| 2 | Grounding | ✓ | ~ | ✗ | ~ | ✗ | ✗ | ✗ | ✗ | covered |
| 3 | Memory & continuity | ✗ | ~ | ✓ | ~ | ✗ | ✗ | ✗ | ✗ | covered |
| 4 | Planning & alignment | ✗ | ✗ | ✗ | ✗ | ✓ | ✗ | ✗ | ✗ | candidate |
| 5 | Execution | ✓ | ✗ | ✗ | ✗ | ~ | ~ | ✓ | ~ | covered |
| 6 | In-flight observability | ✗ | ✗ | ✗ | ✗ | ✗ | ✗ | ✗ | ✗ | **planned** |
| 7 | Steering & interrupt | ✗ | ✗ | ✗ | ✗ | ~ | ✓ | ~ | ✗ | candidate |
| 8 | Reversibility & safety | ✗ | ✗ | ✗ | ✗ | ✗ | ✗ | ✗ | ✓ | candidate |
| 9 | Result presentation & verification | ~ verif. only | ✗ | ✗ | ✗ | ✗ | ✗ | ✗ | ✗ | **planned** presentation judge |
| 10 | Extensibility & composition | ✗ | ✗ | ✗ | ✗ | ✗ | ✗ | ~ live | ✗ | candidate |

**Score today (wrapped + working):** 3 of 10 dimensions with direct coverage
(2, 3, 5), 1 partial (9 verification half).

**Score after upstream-integration roadmap:** 7-8 of 10 (adds 4, 7, 8, 10).

**Score after gap-closing proxies:** 10 of 10. Two dimensions (1, 6) have no
upstream benchmark we can reuse — these require a thin Cortex-built proxy.

---

## Per-dimension status & candidate

### 1. Intent ingress — **GAP, no upstream**

What it measures: given an ambiguous user request, does the harness ask a
useful clarifying question (or correctly proceed when the ambiguity doesn't
matter)?

**Why no upstream.** Clarification-question benchmarks exist in NLP
(ClarifyDelphi, ConvAI3) but are dialogue-style, not coding-agent shaped.
No agent benchmark scores this directly.

**Proposed proxy.** Synthetic corpus of ambiguous coding prompts (e.g.
"refactor the user code" — which user code? which kind of refactor?) +
LLM-judge rubric: (a) did the agent ask a question, (b) was the question
load-bearing, (c) did the answer the agent eventually produced match the
clarification it received. Held-out hand-curated split for the judge-vs-human
spot-check.

**CLI surfaces needed.** `cortex code` already supports multi-turn. May need
a `--interactive-stub` mode or test-driver harness that pipes simulated-user
responses to the agent's clarifying questions.

**Status.** Roadmap stage 3.

---

### 2. Grounding (world model) — **COVERED**

What it measures: can the harness explore an unfamiliar codebase and produce
a fix grounded in real repo state.

**Primary:** SWE-bench Verified (wrapped, [`swebench.md`](swebench.md)).
**Substrate:** MTEB embedding quality (wrapped, [`mteb.md`](mteb.md)) —
better embeddings → better candidate grounding for retrieval-augmented work.
**Adjacent:** NIAH long-context recall ([`niah.md`](niah.md)) — measures
"can the model use grounding it was handed," not "can it go find grounding."

**Gap-narrowing add (optional, Phase B):** retrieval-trace eval —
instrument `cortex search` and score whether the right files appeared in
the top-K candidates for known-good SWE-bench instances. Would isolate
retrieval quality from generation quality.

**Status.** Done. SWE-bench is the primary signal; MTEB the substrate.

---

### 3. Memory & continuity — **COVERED**

What it measures: across multiple sessions, does the harness remember facts,
preferences, decisions, and corrections; does it update memory when
information changes.

**Primary:** LongMemEval (wrapped, [`longmemeval.md`](longmemeval.md)) —
multi-session, includes temporal-reasoning and knowledge-update splits.
**Adjacent:** NIAH ([`niah.md`](niah.md)) — within-session memory floor.

**Gap-narrowing add (Phase B):** Cortex-specific "did the captured insight
actually surface at the right moment" eval — uses the existing library-service
multi-session scenarios. The existing scenarios were authored for this
purpose; they just need to be re-run under the post-CLI-conversion harness.

**Status.** Done. LongMemEval is primary.

---

### 4. Planning & alignment — **GAP, upstream candidate available**

What it measures: before committing to work, does the agent (a) form a plan
that matches the user's stated constraints (b) adhere to declared policies
("refunds require manager approval," "don't `rm -rf` without confirmation").

**Candidate:** [τ-bench](https://github.com/sierra-research/tau-bench) (MIT).
Simulated-user × agent in retail / airline domains with written policies.
Scoring: did the final database state match expected + did the agent stay
policy-compliant. Uses `pass^k` metric (consistency across k trials) — the
right metric for "is this agent reliable, not just lucky once."

**CLI surfaces needed.**
- A simulated-user driver. τ-bench upstream provides one in Python; we either
  port the loop into the Cortex benchmark runner or shell out to the upstream
  Python harness for the user-simulation half.
- Multi-turn session-mode for `cortex code` — already exists via the
  in-process harness, but the CLI surface for "advance one turn with a new
  user message" may need explicit support so the τ-bench runner can drive it
  as a black box. Verify before building.

**Adjacent upstream (deferred):** PlanBench, Plan-Verifier. Smaller scope
than τ-bench but worth evaluating once τ-bench is wrapped.

**Status.** Roadmap stage 2.

---

### 5. Execution — **COVERED (primary), candidate for breadth**

What it measures: actually doing work — calling functions correctly, editing
files, running tools, handling parallel and long-running tasks.

**Primary:** SWE-bench Verified — end-to-end execution (must edit, run, and
pass tests).
**Candidate for breadth:** [Berkeley Function-Calling Leaderboard
(BFCL)](https://gorilla.cs.berkeley.edu/leaderboard.html) (Apache 2.0).
Categories ladder up: *simple* → *parallel* → *multi-turn* → *live* (continuously
collected real-world prompts, contamination-resistant) → *agentic* (longer
chains). The *live* split is the load-bearing one — fights leaked test data.

**CLI surfaces needed.**
- `cortex code` already exposes tool-calling. BFCL primarily evaluates raw
  function-call AST/execution match; the existing `--json` output of
  `cortex code` may be sufficient. Verify the surface returns the tool-call
  sequence at the granularity BFCL needs.
- If BFCL's live split requires posting results to the leaderboard, that's an
  offline analysis step, not a CLI requirement.

**Status.** Primary done. BFCL integration on stage 2.

---

### 6. In-flight observability — **GAP, no upstream**

What it measures: while a long task is running, does the harness emit
progress signals at a useful cadence (not silent, not noisy), with enough
information that a human can decide to wait, redirect, or cancel.

**Why no upstream.** This is fundamentally a UX property, not a model
capability. No corpus benchmark scores it.

**Proposed proxy (mechanical, not LLM-judged).** Run a long-running task
(SWE-bench instance or library-service multi-file refactor) through the
Cortex CLI in event-streaming mode. Measure: (a) max silence interval — gap
between events, (b) event-information density — fraction of events that name
a tool / file / decision vs. content-free heartbeats, (c) coverage —
fraction of tool calls that emit a pre-event the human could read before
the tool acted.

This is mechanical and runs fast. No judge needed. Pass/fail thresholds
set against a "good baseline" trace from a hand-graded run.

**CLI surfaces needed.**
- `cortex code --events <ndjson>` or `--stream` — emit a structured event
  stream (tool-call-start, tool-call-end, text-delta, decision) to stderr
  or a file. This may not exist yet; verify before building.
- Without this surface, the proxy is impossible — it's the primary CLI gap
  for this dimension.

**Status.** Roadmap stage 3. Depends on CLI event-stream surface landing.

---

### 7. Steering & interrupt — **GAP, upstream candidate available**

What it measures: mid-flight, can the user redirect the agent, provide
corrective feedback, or change scope without the agent losing context or
ignoring the change.

**Candidate:** [MINT](https://xingyaoww.github.io/mint-bench/) (MIT). Agent
attempts a task; receives both tool results and natural-language feedback
from a GPT-4 user-simulator; scored on convergence over k turns. The
delta-over-turns metric is the interesting axis — does the agent actually
incorporate feedback, or ignore it.

**Adjacent partial:** τ-bench (covers user-message-mid-session implicitly
via the simulated customer).

**CLI surfaces needed.**
- Same multi-turn driver requirement as τ-bench. If τ-bench wraps cleanly,
  MINT will too — both want "advance a turn with a new user message."
- A simulated-user side: MINT upstream provides one; same shell-out vs.
  port question as τ-bench.

**Status.** Roadmap stage 2. Couple this to τ-bench — same plumbing.

---

### 8. Reversibility & safety — **GAP, upstream candidate available**

What it measures: bounded blast radius — does the agent refuse or gate
destructive ops, resist prompt-injection attacks, surface dangerous actions
for confirmation.

**Candidate:** [AgentDojo](https://agentdojo.spylab.ai/) (Apache 2.0).
Legitimate user tasks (email, banking, travel, slack) with injected
attacker instructions in tool outputs. Two scores: utility (legit task
succeeded) and targeted attack success (agent fell for the injection).
The frontier is high utility + low attack success simultaneously — a
benchmark that rewards "refuse everything" would be useless.

**Cortex-specific extension proxy.** AgentDojo is general-agent; for
coding-specific safety, add a thin proxy: corpus of "destructive op
without confirmation" tasks (`rm -rf`, `git reset --hard`, force-push,
`DROP TABLE`). Score: did the agent execute without asking. Pairs with
AgentDojo but is closer to coding-harness reality.

**CLI surfaces needed.**
- AgentDojo wraps the agent in a tool environment it controls. Either we
  drive `cortex code` from inside the AgentDojo harness (shell-out) or we
  re-implement the AgentDojo task scenarios as Cortex benchmark instances.
  Shell-out is cheaper but couples us to AgentDojo's Python harness;
  re-implementing is slower but keeps everything in the Go benchmark runner.
  Decide at integration time.

**Status.** Roadmap stage 2.

---

### 9. Result presentation & verification — **PARTIAL → augment**

What it measures: at end of turn, did the agent (a) verify its own work
(tests, typecheck, lint) and (b) present results in a form the human can
quickly evaluate (diff, summary, proof, "what changed and what's next").

**Verification half — primary:** SWE-bench (canonical test suite scores
the patch).
**Verification half — adjacent:** any benchmark with a hard grader.

**Presentation half — GAP, no upstream.** No benchmark scores
"did the agent's end-of-turn summary accurately reflect what changed."

**Proposed proxy.** Run a known-fix scenario; capture the agent's
end-of-turn message + the actual diff; LLM-judge rubric: (a) did the
summary mention every changed file, (b) did it accurately describe the
behavior change, (c) did it call out anything intentionally not-done, (d)
did it surface relevant follow-ups. Variance per principle 8.

**CLI surfaces needed.** None new — `cortex code --json` already returns
the final assistant message alongside the patch.

**Status.** Verification half done. Presentation proxy on roadmap stage 3.

---

### 10. Extensibility & composition — **GAP, partial upstream**

What it measures: can the agent use a new tool that wasn't in its initial
context — registered via skill, MCP server, or runtime hook.

**Partial candidate:** BFCL's *agentic* + *live* splits include scenarios
with tool selection from an inventory the model hadn't seen during
training. Not a perfect fit but the closest non-bespoke option.

**Emerging upstream (watch, don't commit yet):**
[MCP-Bench](https://github.com/Accenture/mcp-bench) and the
multi-server tool-use community work — still early; revisit Q3 2026.

**Cortex-specific proxy.** Register an MCP server with a known tool the
model couldn't have memorized; ask a task that requires it. Score: did
the agent discover and call the tool correctly.

**CLI surfaces needed.**
- MCP-server registration via CLI flag — verify whether `cortex code`
  supports `--mcp-server` or equivalent. If not, gap to close before the
  proxy is buildable.

**Status.** BFCL covers part of this on stage 2. Custom MCP proxy on
stage 3.

---

## End-to-end scenario (target shape)

A single multi-session library-service-style scenario that touches all 10:

1. **Ingress.** User opens with an ambiguous request ("clean up the user
   code"). Agent must ask which one.
2. **Plan.** Given a constraint ("don't change the public API"), agent must
   produce a plan that respects it.
3. **Ground.** Agent reads the repo, identifies the relevant files.
4. **Execute.** Multi-file edit.
5. **Observe.** Streamed event log emitted at expected cadence.
6. **Steer.** User injects a mid-task redirect ("also rename `Foo` to
   `Bar`"). Agent must incorporate without losing prior context.
7. **Safety.** A subtask requires `git reset --hard`. Agent must surface for
   confirmation, not execute silently.
8. **Continuity.** A later session asks a follow-up; agent must recall
   decisions from the first session.
9. **Present.** End-of-session diff + summary + verification (tests).
10. **Extend.** Mid-session, register a new MCP tool; agent must use it.

Each step scored individually (so partial failures are visible). Holistic
pass = all 10 individual passes. This is the "one e2e that touches all 10"
the matrix is in service of.

**Status.** Stage 4. Cannot be built until stages 1–3 land — depends on the
CLI surfaces and proxy implementations they produce.

---

## Roadmap (living)

Stages are ordered by dependency, not necessarily by calendar. A stage is
"done" when every dimension it covers has a wrapped or built benchmark
producing structured `CellResult` rows.

### Stage 0 — Coverage matrix doc *(this file)*

- [x] Author this matrix.
- [ ] Add a one-line entry to [`overview.md`](overview.md) cross-linking to
      this doc.
- [ ] Surface this doc from the eval-principles compliance table so the
      "what's missing" picture stays visible at PR-review time.

### Stage 1 — Verify CLI surfaces for the integration wave

Before integrating any new upstream, confirm the CLI exposes what each
benchmark needs. Each item below is a discovery task — file an issue or
mark "already satisfied":

- [ ] Multi-turn session-driver mode for `cortex code` (user-message advance
      a turn, returns assistant + tool-calls).
- [ ] Event-stream output (`cortex code --events` or `--stream` NDJSON).
      Blocks dimension 6.
- [ ] MCP-server registration via CLI flag. Blocks dimension 10.
- [ ] Confirmation-gate flag for destructive operations (so the agent can
      be tested both with and without the gate).

Output: a short addendum to this doc listing which gaps need PRs vs. which
are already satisfied.

### Stage 2 — Integrate upstream benchmarks for dimensions 4, 7, 8, 10

Each follows the existing pattern: thin Go wrapper under
`internal/eval/benchmarks/<name>/`, CLI flag under `cortex eval --benchmark`,
doc under `docs/benchmarks/<name>.md`. Sequence by dependency: τ-bench and
MINT share the simulated-user driver, so build them together.

- [ ] τ-bench wrapper → dimension 4. License: MIT. Source:
      `sierra-research/tau-bench`.
- [ ] MINT wrapper → dimension 7. License: MIT. Source:
      `xingyaoww/mint-bench`. Shares plumbing with τ-bench.
- [ ] AgentDojo wrapper → dimension 8. License: Apache 2.0. Source:
      `ethz-spylab/agentdojo`.
- [ ] BFCL wrapper → dimensions 5 (breadth) + 10 (partial). Sources:
      `ShishirPatil/gorilla` (Apache 2.0). Start with `live` split for
      contamination resistance.

Each integration MUST satisfy all nine principles in
[`eval-principles.md`](../prompts/eval-principles.md). Add a row to that
doc's compliance table per benchmark.

### Stage 3 — Build proxies for the truly novel gaps (1, 6, 9-presentation)

- [ ] Intent-ingress proxy → dimension 1. Synthetic ambiguous-prompt corpus
      + LLM-judge rubric + simulated-user response.
- [ ] In-flight observability proxy → dimension 6. Mechanical event-stream
      analysis (cadence, density, coverage). Depends on stage-1 CLI surface.
- [ ] Presentation judge → dimension 9 (presentation half). LLM-judge over
      end-of-turn summary vs. actual diff.

All three need: dataset curation plan, judge variance reporting (per
principle 8), held-out spot-check split.

### Stage 4 — End-to-end scenario

- [ ] Author the e2e scenario above as a single benchmark instance with
      per-step scoring.
- [ ] Produce one `CellResult` row per step (10 rows per run) so analysis
      can see partial passes, plus one holistic row.

### Stage 5 — Maintenance

- [ ] Move CLI-version pinning and dataset-snapshot pinning into the
      reproducibility addendum (per principle 5).
- [ ] Quarterly contamination check on `live` splits.

---

## Open questions (resolve as we go)

- **Shell out vs. port simulated-user.** τ-bench, MINT, and AgentDojo all
  ship Python harnesses with simulated-user logic. Shell-out is faster to
  integrate but adds a Python runtime dep and a process-boundary bug
  class. Porting keeps everything Go but takes longer per benchmark.
  Default: shell out for the first integration of each, port if the
  process boundary becomes painful.
- **Cost ceiling per run.** Stage 2 integrations multiply OpenRouter spend.
  Set a per-benchmark `--max-cost-usd` flag and a default cap (e.g. $5) so
  runs fail closed rather than burning a tank.
- **Local-model fallback for proxies.** Stages 3 proxies that use an
  LLM-judge should support a small local model (qwen2.5-coder-7b or
  similar) as the default judge for development loops, with a frontier
  model gate before scoring lands in `eval-journal.md`.
- **What counts as "covered."** A dimension is ✓ when at least one wrapped
  benchmark produces structured `CellResult` rows that an analysis script
  can chart over time. A benchmark that's wrapped but never run in CI
  doesn't count. Codify in stage 5.

---

## How to update this doc

- When a stage item completes, flip its checkbox and link the PR.
- When a benchmark wraps, update its row in the coverage matrix from a
  candidate to a wrapped status; add its detail doc under
  `docs/benchmarks/<name>.md`.
- When a CLI gap closes, remove the "CLI surfaces needed" bullet for the
  affected dimensions.
- When a new upstream emerges that's a better fit for a dimension, file an
  open question above, not a silent swap.
- Run-level findings (numbers, regressions, model deltas) go in
  [`eval-journal.md`](../eval-journal.md), not here. This doc tracks
  *coverage*, not *results*.
