# The pivot eval: can the agent change course by migrating its context?

> **Status: DESIGN (2026-07-11), not built.** Eval design for the shipped
> context self-curation tools
> ([`context-window-modification-tools.md`](context-window-modification-tools.md)):
> `context_evict`, `context_merge`, `context_adjust_watermarks`, plus
> `recall(citation, budget)`. Follows the two-layer house pattern
> ([`eval-design-example.md`](eval-design-example.md)): Δ deterministic +
> ø agentic, `pass ⟺ Δ green ∧ ø green`.

## The question

**Can an agent change course mid-session by using the harness to migrate its
context?** "Can" hides three separable claims, and the eval must answer each
one on its own evidence:

1. **Capability (Δ).** Does the harness mechanically support a full
   migration — evict + merge + watermark shift composed in one session,
   through real `Turn()` calls, with the architecture's invariants still
   holding jointly afterward? Machine-decided, scripted backend, no model.
2. **Volition (ø).** Given a course change, does a live model actually reach
   for the context tools? Not "do the tools work" — "does the model drive
   them."
3. **Benefit (ø).** Does migrating *causally* improve post-pivot behavior
   versus not migrating? Without this arm the eval is vacuous: a model that
   ignores the tools and still answers every probe proves the scenario was
   too easy, not that migration works.

Existing coverage stops short of all three: `context_tools_test.go` (both
packages) proves each tool's mechanics in isolation; the context evals
(`context_eval_test.go`, `context_eval_live_test.go`) prove demotion → recall
retention with **no curation in the loop**. Nothing composes the tools, and
nothing measures a model choosing to use them.

**Empirical prior: the tools have never been observed firing live.** No model
has been seen calling a `context_*` tool in real sessions. So volition is not
a formality on the way to the benefit question — it is the open question, and
the ø layer is designed as an *instrument* that locates why the tools go
unused (a volition ladder, below), not a single pass/fail gate that would sit
red without saying anything.

> **Update (2026-07-18): this prior is falsified.** Sessions from
> 2026-06-11 through 2026-07-17 show the tools firing live: `recall`×1324,
> `context_merge`×499, `context_evict`×435, `context_adjust_watermarks`×462,
> plus `coding.context_rewrite`×328 in the journal. The ø volition question
> is effectively answered — models do call these tools unprompted. The open
> question this doc's design must now serve is the *benefit arm*: does that
> curation causally improve retention over not curating? See
> `docs/completion-roadmap.md` Track B2.

## Why the tools may never fire — hypotheses the eval must separate

A wiring audit (2026-07-11) rules the boring explanation out and sharpens the
rest. The declarations do reach the wire — `toolSet = tools.All`
(`cmd/cortex/tool_deps.go:48`) includes all three, and the config gates
default to enabled. What remains:

| # | Hypothesis | Evidence today | Discriminated by |
|---|---|---|---|
| H1 | **No affordance where the model looks.** `outlineHeader` (`cmd/cortex/demote.go:105`) explicitly advertises `recall` ("call recall with a turn's @session/… citation") but never says the outline is curatable — and recall is exactly the context-shaped tool that DOES fire live (`context_eval_live_test.go`). The one advertised tool is the one used. | strong | L1 vs L2 rungs; if naming the tools in a user turn converts, a one-clause header mention is the fix |
| H2 | **No pressure signal.** Nothing on the wire says the outline is near its cap or that clutter costs anything; the descriptions' "use when clearly irrelevant" has no trigger moment tied to anything observable. | strong | pivot-with-housekeeping-ask (L1) vs bare pivot (L0); both failing while L2 passes implicates missing salience, not missing comprehension |
| H3 | **System prompt silence.** `SystemPrompt` never mentions context curation as part of the job. | confirmed by grep | same lever as H1; principle-shaped line, per the system-prompt feedback |
| H4 | **Schema/dispatch friction.** Small models may emit the call malformed (wrong name, prose "I'll evict t3", citation format wrong) and give up. | unknown | L3 direct-command rung + near-miss capture |
| H5 | **Meta-work aversion.** Small local models use tools that map to user-task verbs (read, edit, grep); `context_evict` maps to no user verb, so it never wins the next-token race even when understood. | plausible | L2 passing but L0/L1 never converting after H1/H3 fixes land |

One side finding from the audit, independent of the eval: config-disabling a
tool refuses it **at dispatch** (`IsToolEnabled`, `session_core.go`) but the
declaration stays on the wire — a disabled tool is still offered to the
model. Harmless today; it matters for ARM-OFF below, and is worth fixing so
gates strip declarations too.

## The scenario (shared by both layers)

One session, two tasks, one pivot. Facts are codeword-style needles planted
past the outline's 500-rune verbatim cap, so after demotion the outline holds
only a hint and the needle is reachable solely through `recall` — the same
trick as the live context eval.

```
 PHASE A — old course                PIVOT                PHASE B — new course
 ────────────────────           ───────────────           ────────────────────
 plant A-dead needle            user: "indexer work       plant B needle
 plant A-keep needle             is abandoned; new        filler turns until
 filler turns until A            task is <B> — we'll      B demotes → its
 demotes → outline fills         still need the deploy    outline hint is live
 with A entries                  codeword. Curate your    ────────────────────
                                 working context for      PROBES
                                 the new task first."     B needle      graded
                                                          A-keep needle graded
                                                          A-dead needle floor
```

- **A-dead** — a needle the pivot declares obsolete (e.g. an indexer shard
  count). Only the floor applies at probe time: correct or honest miss
  passes, confabulation fails.
- **A-keep** — a needle the pivot explicitly says the new task still needs
  (e.g. the deploy codeword). Graded hard. This is what makes
  slash-and-burn curation fail: evict everything and the A-keep citation
  leaves the wire, the model can no longer recall it, and a graded probe
  misses.
- **B** — the new task's needle, planted after the pivot. Graded hard **in
  the migrated arm** when its outline hint is live at probe time.

The three probes triangulate the three failure directions: no migration
(B's hint folds away → blind miss), indiscriminate migration (A-keep gone →
graded miss), and destructive/confabulating recovery (A-dead floor).

## The sizing rule (what makes migration causally necessary)

The discriminating lever is the outline cap (W/8). Size the window and
filler volume so that:

- **A entries + B entries > outline cap** — without curation, the fold fires
  before the B probe and B's hint leaves the live outline (probe degrades to
  blind → floor at best);
- **A-keep entry + B entries ≤ outline cap** — with A-dead entries evicted
  (or A merged to one spanning entry), B's hint survives to probe time and
  the graded probe is answerable.

So the *only* way to hold all three probes simultaneously is a selective
migration: shed the dead weight, keep the declared-relevant citation, leave
room for the new task. That is the behavior the question asks about, and the
sizing makes it load-bearing rather than decorative. Watermark adjustment is
**observed and reported, not gated** — there is no honest way to make a ±W/4
shift causally necessary in a scripted scenario, and a gate that can be
passed without it shouldn't pretend otherwise.

Sizing is pinned by a deterministic pre-flight in the test itself (same
pattern as `hintVisible`/`leak` in the live context eval): before probing,
assert the scenario actually put the model in the dilemma — A demoted, B
demoted, and (in the no-migration arm) the fold actually fired. A scenario
that never tightened proves nothing and must fail loudly as a setup error,
not pass quietly.

## Δ — deterministic layer (the harness CAN migrate)

`cmd/cortex/context_pivot_eval_test.go`, riding the `contextEvalBackend`
pattern extended to return **scripted tool calls** (the canned assistant
plays the ideal migrator at the pivot turn). Unlike the refactor eval's Δ,
this layer is expected **green from the first run** — the tools are shipped;
any red is a real composition bug. Asserts, on every turn of the scripted
pivot session:

| # | Invariant after a composed migration |
|---|---|
| Δ1 | **Evict is wire-only loss.** Post-evict, the A-dead entry is off the wire, but `cs.Recall(citation)` still resolves it — the transcript is intact; eviction touches only the working set. |
| Δ2 | **Merge is lossless.** The merged spanning citation `#m<first>-<last>` resolves every original message of the range (turn spans partition the message log). |
| Δ3 | **Completeness modulo evictions.** The existing `assertWireComplete` invariant holds for every non-evicted marker; evicted markers are the *only* ones allowed off the wire-reachable set. |
| Δ4 | **Bounded.** `assertWireBounded` holds through and after the migration turn, including after a watermark shift (budget recomputed against the shifted watermarks). |
| Δ5 | **Cache re-stabilizes.** The migration turn breaks the prefix once (re-prefill bounded by zone budgets, like a demotion turn); every subsequent unchanged-zone-A turn is pure append again. A migration that permanently poisons the LCP cache fails. |
| Δ6 | **Session-local.** Resume of the post-migration transcript replays policy over the transcript: evictions/merges/watermarks revert, and the resumed session still satisfies Δ3 with an empty eviction set. |

Δ is the layer that can be built first and entirely offline; it reuses
`assertWireComplete` / `assertWireBounded` / `rePrefillChars` from
`context_eval_test.go` verbatim.

## ø — agentic layer (the model DOES migrate, and it HELPS)

`cmd/cortex/pivot_eval_live_test.go`, gated `CORTEX_LIVE_FLEET=1`, same
env-knob shape as the live context eval
(`CORTEX_PIVOT_EVAL_{ENDPOINT,MODEL,WINDOW,RUNG}`). Reuses `fillerInput`,
`floorGrade`, the `codewordish` confabulation regex, and the per-turn stats
table (prompt/cached/demoted/elapsed) — extract those into a shared live-eval
helper rather than copying.

**Two arms, same script, same seeds:**

- **ARM-ON** — context tools enabled (default config).
- **ARM-OFF** — the control, measuring what the scenario does to a model
  that cannot migrate; it is the anti-vacuity check — if ARM-OFF passes the
  B graded probe anyway, the sizing failed and the run is invalid (setup
  error, not a pass). **Implementation note:** the config gates refuse only
  at dispatch and leave the declarations on the wire, which would give the
  control arm a different failure shape (call → refusal) than "no tools."
  ARM-OFF must strip the three declarations from `Request.Tools`.

**The volition ladder** (the instrument; per-rung pivot-turn wording, run
top-down until one converts):

| Rung | Pivot turn says | A pass here means |
|---|---|---|
| L0 | bare pivot — "indexer work is abandoned; new task is <B>, we'll still need the deploy codeword" | spontaneous migration (the end goal; expected red today) |
| L1 | + "curate your working context for the new task before we start" | the principle lands; the model connects "curate context" to the tools unaided |
| L2 | + names the capability: "you have context tools for evicting or merging outline entries" | comprehension is fine; the gap is salience/affordance (H1/H2/H3) — fix the header/system prompt, not the model |
| L3 | direct command: "evict the outline entries about the indexer work" | plumbing works end-to-end; anything above L3 failing is a prompting/salience problem, not schema |
| — | L3 fails too | schema/dispatch friction (H4) or model floor — inspect the near-miss capture |

The rung wording stays principle-shaped through L2 (never a tool recipe);
L3 is deliberately a recipe because its *job* is to isolate the mechanical
layer. **The primary output of the ø layer is the conversion rung** — the
lowest L at which migration fires — reported per model. Harness fixes (an
outline-header affordance clause, a system-prompt line, a pressure signal)
then aim to lower that rung run over run; the ladder is how the eval "sees
if it can," and keeps seeing as the harness improves.

**Near-miss capture** (the diagnostic for H4, recorded at every rung): raw
assistant messages around the pivot are scanned for tool-shaped failures —
calls to names not in the registry, `context_*` mentioned in prose without a
call, evict/merge attempted with a malformed citation, or `recall` fired
where curation was asked for. These are reported verbatim; a red rung with
an empty near-miss log means the model never even oriented toward the tools.

**Gates (ARM-ON, pinned threshold — every gate at n=1 per run, n≥3 runs
before trusting the number).** G1 gates the *plumbing* rung; the conversion
rung itself is a reported measurement, so the eval stays green-able while
the volition frontier moves:

| # | Gate | Decided by |
|---|---|---|
| G1 | **Migration reachable.** ≥1 of `context_evict`/`context_merge`/`context_adjust_watermarks` fires at *some* rung ≤ L3 — the plumbing works and the model can be induced. The conversion rung is reported alongside. | machine (tool-call scan, like `recallCalledSince`) |
| G2 | **New-course retention.** At the conversion rung: B graded probe contains the B needle, hard when B's outline hint is live at probe time (pre-flight asserts it should be, given G1-quality migration). | machine (string match) |
| G3 | **Selective preservation.** A-keep graded probe contains the A-keep needle — recall bridged through whatever the migration left on the wire (original entry, merged span, or a memory note). | machine |
| G4 | **No destructive loss.** A-dead floor: honest miss or correct passes, `codewordish` confabulation fails. Plus the mechanical half: `cs.Recall(A-dead citation)` still resolves from the transcript after all migration activity. | machine |
| G5 | **Bounded.** Prompt tokens stay under envelope + W/2 + W/8 + overshoot on every turn, including the migration turn. | machine |

**Reported, not gated:** the conversion rung (the headline number); which
tools fired and in what order; the near-miss log; watermark usage; outline
size before/after the pivot; hint-liveness delta vs ARM-OFF; cached-token
ratios. The ARM-ON vs ARM-OFF comparison is
promoted to a gate (G6: ARM-ON holds B's hint live strictly longer than
ARM-OFF) only after n≥3 shows the separation is stable — a comparative gate
on one noisy run per arm is a coin flip, per the probe-before-long-runs rule.

## How the eval answers the question

| Verdict | Evidence |
|---|---|
| **"Yes, unaided, and it helps"** | Δ green ∧ G1–G5 green ∧ conversion rung ≤ L1 ∧ ARM-OFF degraded (B blind/floor) |
| **"Yes when told, not otherwise"** | conversion rung = L2 — comprehension fine, salience missing; the fix is the outline-header affordance clause / system-prompt line (H1/H3), then re-run and watch the rung drop |
| **"Only under direct command"** | conversion rung = L3 — the model never maps intent to these tools; tool descriptions are the lever, or the model is below the meta-work floor (H5) |
| **"The harness can, the model can't"** | Δ green ∧ G1 red (no rung converts) — schema/dispatch friction (H4); read the near-miss log |
| **"The model tries, migration is destructive"** | G1 green ∧ (G3 or G4 red) — curation judgment failure; the interesting failure mode |
| **"Vacuous run"** | ARM-OFF passes B graded — sizing failed, rerun with a tighter window; **not** a pass |

## Out of scope (MECE with existing evals)

- **Memory distillation at pivot** (`memory_write` of task-A conclusions) —
  observed in the tool-call report but not gated; the memory e2e evals own
  that surface. If the model answers G3 via a memory note instead of recall,
  that *passes* — the question is course-change competence, not which lane.
- **Baseline demote → recall retention** — owned by
  `context_eval_live_test.go`; this eval assumes it and builds on it.
- **Study, compaction safety net** — untouched.

## Build order

1. **Δ composed test** — offline, buildable today, expected green; any red
   is a shipped-tool composition bug worth finding before the live layer
   exists. Needs one new piece of scaffolding: a scripted backend that
   returns canned `tool_calls`.
2. **Shared live helpers** — extract `fillerInput`/`floorGrade`/stats from
   `context_eval_live_test.go`.
3. **L3 dispatch probe first** — a minutes-scale flight check (probe before
   long runs) that skips the full scenario: seed a session with a few
   demoted turns, issue the L3 direct command, and watch whether a
   well-formed `context_evict` comes back. This answers H4 for a model in
   one cheap turn, before any fleet time goes into the ladder.
4. **ø driver, ARM-ON, ladder top-down (L3→L0)** — find the conversion rung.
5. **ARM-OFF + full gate run, n≥3 at the conversion rung** — then decide G6
   promotion.
6. **Harness fixes, re-run the ladder** — if the rung lands at L2 (the H1/H3
   prediction), add the one-clause curability mention to `outlineHeader`
   (the recall precedent says header advertisement works) and a
   principle-shaped system-prompt line, and watch whether the rung drops.
   The eval is the instrument for that loop, not a one-shot verdict.
7. **Promotion** — if this becomes a standing gate, lift the driver into a
   `cortex pivot-eval` subcommand with an exit-code verdict, the
   `study-eval` pattern.

## Running it (as designed)

```bash
go test ./cmd/cortex -run ContextPivotEval            # Δ: composed migration invariants
CORTEX_LIVE_FLEET=1 go test ./cmd/cortex -run PivotEval_Live -v -timeout 1800s   # ø
```

## References

- [`context-window-modification-tools.md`](context-window-modification-tools.md) — the tools under test, as built
- [`context-architecture.md`](context-architecture.md) — the two-zone working set the migration operates on
- [`eval-design-example.md`](eval-design-example.md) — the Δ/ø house pattern
- `cmd/cortex/context_eval_test.go`, `context_eval_live_test.go` — the invariant helpers and needle machinery this reuses
- `cmd/cortex/context_tools_test.go`, `internal/tools/context_tools_test.go` — per-tool mechanics already covered (not re-tested here)

## First gate runs — receipts (2026-07-18)

Built as `cmd/cortex/context_pivot_eval_test.go` (Δ, in-suite, green) and
`context_pivot_eval_live_test.go` (ø). Three ø runs on the chatterbox
fleet, `qwen3-coder-q3`, window 6000, rung **L3**, 8 filler turns/phase.
Probe measurements, not benchmarks:

| run | ARM-ON migrated@pivot | ARM-ON B | ARM-OFF B | A-keep (both arms) | verdict |
|---|---|---|---|---|---|
| 1 | yes (4 evicts) | hit | hit | honest miss | PASS |
| 2 | yes | hit | hit | honest miss | PASS |
| 3 | yes | hit | **miss** | ON **confabulated** ("GLACIER-88") | FAIL |

Read: **volition at L3 is solved** — migration fired 3/3, and ARM-OFF
(declarations stripped) never migrated, so the instrument is honest.
B-needle retention favored ARM-ON (3/3 vs 2/3), and ARM-ON answered
probes from the intact prefix cache (~22 evaluated tokens, sub-second)
where ARM-OFF paid a rebuild (~3.4k tokens, ~33s). The open problem the
eval exposed is **selective keeping**: A-keep was lost in all six arms —
ARM-ON over-evicts spans the pivot explicitly marked keep (once
confabulating the answer instead of missing honestly). Next lever per
step 6 of the build order: wording/system-prompt iteration on what
"keep" means at migration time, then re-run; L0–L2 rungs unprobed.
