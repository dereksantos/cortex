# Think/Dream evaluation — design gate

> **Status: DESIGN (2026-07-11), decision pending owner review.** Answers
> ROADMAP.md item 8: before any Think/Dream implementation, decide whether a
> simplified background-curation layer would improve long-horizon curation
> beyond what the memory tools (`docs/memory-tools.md`) and context tools
> (`docs/context-window-modification-tools.md`) already deliver, and define
> the eval that would prove it. **Document only — no code lands with this
> milestone** (GOAL.md §1 non-goals).

## Decision question

Cortex today curates context two ways, both **foreground and model-driven**:
the coder calls `memory_write/read/search/forget` when it judges a note
worth saving, and `context_evict/merge/adjust_watermarks` when it judges the
working set needs reshaping. Both only fire while the coder is already
mid-turn, thinking about the task at hand — curation is a side effect of
work, not a dedicated activity.

The historical Think/Dream design (`docs/archive.md` §"The five cognitive
modes") proposed a *background* pass: an agentic step off the coder's
critical path, budget-bounded, that scans recent activity and does curation
work the foreground turn had no reason to do — Think during active periods
at reduced budget, Dream during idle periods at a budget that grows with
idle time, both capped.

**The question this doc must answer:** does adding that background pass back
— in a simplified form, on top of what's shipped today — measurably improve
curation over a long session, or does the foreground-only, model-driven
regime (the memory-tools pivot's bet) already capture the value at lower
complexity? If simplified Think/Dream would win, what does the eval that
proves it look like? If the memory-tools bet already wins, what evidence
would show that instead, so the decision isn't argued from priors?

This is deliberately framed as a decision between two regimes, not "should
we add background compute" in the abstract — the foreground regime is
already live and already embodies a considered bet (`memory-tools.md`: "the
model is better at deciding what's relevant... than any recency/
contradiction heuristic we can hand-code"). Think/Dream has to clear that
bar, not just clear "does nothing."

## Prior art in git history

Three generations of this idea exist in the history, each a data point for
what worked and what got cut:

1. **The original five-mode design** (`archive.md`, pre-2026-06-27). Reflex
   (mechanical, <20ms target, every retrieval), Reflect (agentic sync/async,
   LLM rerank + contradiction detection), Resolve (agentic, inject-now vs.
   wait vs. queue), Think (background, budget *decays* with activity), Dream
   (background, budget *grows* with idle time, capped). Two commitments:
   *inverse activity gradient* (Think throttles while busy, Dream grows
   while idle) and *mechanical foreground with a latency target* (Reflex
   stays fast; agentic modes feed it via cached artifacts rather than
   sitting on the critical path). Backed by a `Capture → Filter → Store →
   Retrieve → Inject` pipeline and Dream *sources* that sampled project
   files, stored events, Claude history, and git commits/diffs to produce
   embeddings, insights, entity relationships, and a proactive injection
   queue.

2. **Wired to a live fleet, then deleted eight days later**
   (`7471bf1` → `b63a83d`). `7471bf1` ("wire the cognition stack to the
   fleet") lit up real embed/rerank/Think/Dream against live models — not a
   stub. `d709a2b`/`0b2c97d` (daemon retirement) moved `MaybeThink`/
   `MaybeDream` dispatch into the REPL's idle hook, i.e. Think/Dream were
   live in the interactive product, not just in eval code. `b63a83d`
   ("delete the cognition DAG + retrieve/study stack") removed all of it
   nine days after it was wired: by then it had exactly **one** live
   consumer end to end — Discord's `route_message` "continue this session
   vs. start a new one?" classifier — and that one consumer was
   reimplemented in ~70 lines as a standalone LLM call
   (`classifyRoute`/`parseRouteDecision`), a net −18,350 src / −11,029 test
   LOC deletion. The lesson isn't "background compute doesn't work" — the
   wiring worked — it's that **18k lines of general-purpose retrieve/
   rerank/Think/Dream machinery had no product surface asking for it**
   beyond one narrow classification call. Any Think/Dream redesign has to
   name its consumer up front or repeat this exact arc.

3. **The memory-as-tools pivot that replaced it** (`memory-tools.md`,
   shipped, P1–P4 done). Explicitly supersedes "the mechanical memory line
   (Reflex/Reflect/Resolve, recency weighting, contradiction→retraction,
   freshness injection, auto-distill)". The stated bet: a capable model
   curates better than a hand-coded heuristic, and the only mechanical
   surface kept is index injection (`INDEX.md` at turn start) — because a
   tool-only model with no reminder of what it already knows is blind to
   its own memory. The doc names its own hedge explicitly: *"If that's not
   enough for the smallest models, a later nudge... can be added, but only
   if evals show the need. Start without it."* This design doc is that
   evidence-gathering step for the hedge, not a rebuttal of the pivot.

Adjacent but out of scope for this decision: `working-memory-study.md`'s
findings-prefix curation (P1–P4, landed on an unmerged branch) is a
*foreground* multi-pass accumulator inside a single `study` invocation, not
a background pass across turns — it's evidence that curation quality is
model-dependent (helps ~qwen3-4b/coder80, hurts a stronger reasoner) but
doesn't bear directly on background-vs-foreground.

## The eval

Two-layer house pattern (`docs/eval-design-example.md`): **Δ deterministic**
(the background mechanism, once built, behaves — budget bounds hold, no
model needed) and **ø agentic** (a live model's background pass produces
curation a foreground-only session would have missed, and that curation
*causally* helps a later probe — same shape as `docs/eval-context-pivot.md`'s
three-claim split of capability/volition/benefit, adapted to background vs.
foreground instead of tool-use vs. no-tool-use).

This section specifies what the eval would measure **if built**; per
ROADMAP.md item 8 and GOAL.md §1, no implementation lands with this
milestone.

### The scenario

One long session, seeded so a save-worthy fact is stated once, mid-session,
in a way the foreground coder has no task-shaped reason to write down (the
turn's own job doesn't touch `memory_write` — e.g. an aside like "by the way,
we standardized on X for this kind of thing" embedded inside an unrelated
debugging turn). Filler turns continue past it. A later turn — in the same
session, or a fresh session after resume — asks a question only that aside
answers.

- **NEEDLE-A (foreground-missed)** — the aside above: plausible, real, but
  off the current turn's task, so a foreground-only coder has no local
  reason to call `memory_write`. This is the fact the background pass exists
  to catch.
- **NEEDLE-B (foreground-caught, control)** — a fact stated where the
  foreground coder's existing incentives already cover it (directly asked
  "remember that..." or squarely on-task) — both regimes should catch this
  one; if the background-only arm fails it too, the scenario is broken, not
  informative.
- **NOISE turns** — filler that produces nothing save-worthy, to measure
  whether background curation adds spurious notes (the precision floor) at
  a realistic base rate rather than a saturated one.

### Δ — deterministic layer (the mechanism, once built, behaves)

Machine-decided, scripted backend, no live model:

| # | Invariant |
|---|---|
| Δ1 | **Budget bounds hold.** Think's per-tick budget strictly decreases while `turnIntent` shows activity; Dream's strictly increases with idle duration, capped at `MaxBudget`. A tick that exceeds its budget is a bug, not a slow run. |
| Δ2 | **Off the coder's critical path.** A scripted turn's wall-clock latency is unaffected (within noise) by whether a background tick is scheduled concurrently — this is a regression check on "mechanical foreground with a latency target", the original design's second commitment. |
| Δ3 | **Idempotent / non-destructive.** Re-running a tick over the same journal window does not duplicate or corrupt existing `memory_write` notes; a tick that finds nothing new writes nothing. |
| Δ4 | **Local-only.** `journal.AssertLocalOnly` holds for whatever new journal writer-class a Think/Dream tick introduces — no outbound path is a hidden regression here (CLAUDE.md invariant, still enforced). |

### ø — agentic layer (does it help, and is it worth it)

Live model, gated (`CORTEX_LIVE_FLEET=1`), same env-knob shape as the
existing live evals. **Two arms, same scripted session:**

- **ARM-FOREGROUND** — today's shipped behavior: memory tools + context
  tools only, no background pass. This is not a stub control — it's the
  regime already in production, so the comparison is against the real
  incumbent, not a strawman.
- **ARM-BACKGROUND** — ARM-FOREGROUND plus the candidate background pass
  running on schedule (Think while turns are active, Dream once the
  scripted session goes idle).

| # | Gate | Decided by |
|---|---|---|
| G1 | **NEEDLE-B parity.** Both arms answer the NEEDLE-B probe correctly — proves the scenario didn't accidentally make foreground curation impossible (anti-vacuity, same role as ARM-OFF in `eval-context-pivot.md`). | machine (string/grade match) |
| G2 | **NEEDLE-A lift.** ARM-BACKGROUND answers the NEEDLE-A probe correctly (or with a `memory_search` hit) strictly more often than ARM-FOREGROUND across n≥3 runs — the causal claim. A tie or a loss here is a real "no-go" signal, not noise to explain away. | machine, n≥3 |
| G3 | **Precision floor.** ARM-BACKGROUND's note count over the NOISE turns stays within a small multiple of ARM-FOREGROUND's (e.g. ≤2×) — a background pass that "helps" by writing a note per turn is not a win, it's `memory_write` spam that degrades the index-injection surface the whole memory-tools design depends on staying small. | machine (note count) |
| G4 | **Bounded cost.** Total background wall-clock + token spend across the session stays under a pinned budget (the Δ1 bound observed live, not just in the deterministic layer) — a background pass that wins G2 by burning an unbounded budget fails the "simplified" premise in ROADMAP.md item 8's framing. | machine (budget accounting) |

**Reported, not gated:** which specific ticks (Think vs. Dream) produced the
winning note; latency distribution of ticks; false-positive note content
(for qualitative review); model used for the background pass (the
`study`-role binding already reserved in config, per CLAUDE.md's "Roles"
section, is the natural fit — no new role needed).

## Go / no-go criteria

| Verdict | Evidence required |
|---|---|
| **Go — build simplified Think/Dream** | Δ green (mechanism is sound) ∧ G1–G4 all green at n≥3 — background curation produces a measurable, bounded-cost, low-noise lift the foreground-only regime structurally cannot reach (it never sees the aside because no turn's task touches it). |
| **No-go — foreground regime already sufficient** | G2 is a tie or a loss (ARM-BACKGROUND does not outperform ARM-FOREGROUND on NEEDLE-A) at n≥3 — the memory-tools pivot's bet holds without the hedge; do not build. |
| **No-go — cost exceeds benefit** | G2 green but G3 or G4 red — a background pass that helps only by being noisy or unbounded fails ROADMAP.md item 8's "simplified" framing; a *more* simplified design (narrower trigger, smaller model, capped ticks) would need to re-clear the gate, not a straight yes. |
| **Inconclusive — scenario invalid** | G1 red — the scenario failed to isolate a genuinely foreground-missed fact; fix the scenario (the aside was actually task-adjacent enough that a real coder would have saved it) and rerun before drawing any conclusion. |
| **Blocked — no product surface named** | Before any of the above is run: if no concrete consumer of the background pass's output can be named (a specific probe, a specific downstream tool that reads its notes), stop — this is exactly the failure mode `b63a83d` already lived through once (18k LOC wired to the fleet, one narrow consumer, deleted nine days later). Name the consumer first, or don't build. |

The default reading absent a run: **no-go by omission** — ROADMAP.md item 8
already states nothing is implementable until this doc exists, and this doc
recommends the eval above be run (Δ first, cheap and offline; ø only if Δ is
green and a consumer is named per the "Blocked" row) before committing to
implementation. This doc does not itself run the eval or render the verdict
— that is explicitly owner review, per ROADMAP.md item 8 and this file's
status line.
