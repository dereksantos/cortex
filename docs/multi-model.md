# Multi-model: a reasoning partner beside the coder

> Status (2026-10-17; gate and dialogue added 2026-10-19): **proposed, not
> built.** Builds on the decider's live shadow wiring (docs/decide.md) and the
> effort vocabulary (docs/thinking-models.md). Nothing here changes a session
> whose config does not turn it on.

## The idea

Cortex already runs more than one model: the `code` binding drives the turn,
and the `study` binding runs Study, the summarizer, and the shell-risk
classifier. The decider adds a third, in shadow. What none of them does is
*think about the work* while it happens.

The partner is a reasoning model that sits beside the coder for the whole
session and offers advice — a well-thought-out second opinion, never a hand
on the keyboard. It reads the same record the decider reads, thinks at high
effort, and writes short free-text notes. The coder sees a note on a later
request, framed as advice it may take or leave. The partner never calls a
tool, never edits, never blocks a send, and never decides when the turn ends.

Three commitments, from the design conversation:

1. **Wherever it can.** The partner resolves to whatever reasoning capacity
   the setup has — a configured reasoner, a hosted model, or the coder's own
   model at high effort. A single-model local setup still gets a partner.
2. **Advisory throughout.** It speaks at every moment in the session where it
   has something to add, not at one gate. Its strongest voice is a block
   *suggestion* on one action (the gate, below): fixed rules, not the
   partner, decide what happens, and the rules only ever add caution.
3. **Better results is the bar.** It ships at `shadow`, and is turned up to
   `advise` by default only on a paired benchmark result (below).

## Where it lives: the binding

A new `reason` role joins `code`/`study`/`embed` in `rolePolicies`, with the
policy `{tag: "reasoner", effort: high}`. (A `reason` role was removed in the
2026-07-18 audit because nothing called it; this one ships with its caller.)

Resolution falls through until something answers:

| Order | Source | Note |
|---|---|---|
| 1 | `models.reason` | explicit — any backend, local or hosted |
| 2 | fleet discovery's `reasoner` tag | when the backend serves one |
| 3 | `models.study` at high effort | the binding already marked reasoner |
| 4 | `models.code` at high effort | same model, thinking on — always available |

The fallback at order 4 is what makes "wherever it can" literal: the coder
runs at its configured effort, and the partner runs the same weights with
thinking on. On one local GPU that serializes with the coder (one model, two
request streams); the bounds below keep it from crowding the coder out.

An endpoint other than the coder's is a new outbound path, so — exactly as
the decider's rule — a partner binding with its own endpoint is honored only
from the user config, refused under `CORTEX_LOCAL_ONLY`, and announced once on
stderr naming the destination. The decider's live probe also showed a shared
free-tier quota lets a sidecar starve the coder (docs/decide.md, live probe);
the partner therefore yields: a 429 or a coder send in flight on the same
endpoint skips the partner's moment instead of queueing it.

## When it speaks: moments

Each moment is a point where the engine already has a hook. None of them
waits on the partner.

| Moment | Hook | What the partner is given | What it is asked |
|---|---|---|---|
| `opening` | turn start (`beginTurn`) | the task, the memory index, the outline | its first take: where the problem likely is, what would confirm it, what to avoid |
| `rounds` | `Toolset.AfterBatch`, every N rounds | the decider's snapshot (last steps + ledger) | is the work on track; what it would check next and why |
| `stuck` | `AfterBatch`, when a mechanical trigger fires | the snapshot + the trigger | what is being repeated, and a different angle |
| `review` | `endTurn`, on the reply | the snapshot + the reply + the diff stat | which claims the record supports, what is left unverified |
| `between` | after the reply is out | the turn's record | what the next turn should start from |

Mechanical `stuck` triggers (no model needed to fire them): the same command
failing twice with the same last line, the same file edited three rounds
running without a test run between, or two `edit_file` not-found failures in
a row. The decider's `looping` answer can be added as a trigger once
docs/decide.md's measurement plan promotes it.

`review` and `between` land after the reply, so they reach the *next* turn —
the partner's view of the last turn is waiting at the start of the next one.
In the REPL they are also shown to the person, dim, under the footer (see
Tuning); in `cortex turn` they go to the journal and the `partner` JSON key.

## How it reaches the coder: delivery

The engine sends the next request the moment a batch ends, so a note started
at a batch can reach, at the earliest, the send after the next round
(docs/decide.md, timing). Delivery follows from that:

- **At most one note in flight; latest wins.** A moment that arrives while a
  note is still being written is journaled `busy`, as the decider does. A
  finished note that has not been delivered is replaced by a newer one.
- **Delivered at `BeforeSend`.** The hook may already mutate the request; it
  appends the ready note as a message and records it in the transcript, so
  the wire stays append-only (the prefix cache is untouched) and a resumed
  session carries the note exactly as the coder saw it.
- **Stale notes are dropped.** A note built from a snapshot more than
  `max_rounds_late` rounds old is journaled `stale` and not delivered.
- **Framed as advice.** The note carries a fixed header and footer: it is
  another reader's opinion of the record, may be wrong, and the coder should
  act on it only if the record supports it. The coder owes it no reply.

## When it can stop an action: the gate

A note reaches the coder a round late, so it cannot stop an action that is
about to run. For that, the partner also judges single proposed actions
before they run and says how strongly it suggests a block. It still cannot
block. A **rules decider** reads the partner's opinion beside the verdicts
the harness already has and makes the choice. This is the proposer – critic
– judge shape; the closest prior art is trusted monitoring in AI control
(Greenblatt et al., 2023), where a monitor scores each action and a fixed
protocol — not a model — decides what happens. Rules come first because they
are fast, free, and auditable; a model as the decider is a later design,
only if a measured result says the rules are too blunt.

**Which actions.** The ones that can do harm: a `bash` command that reaches
the shell-risk classifier (off the safe path, past the deny-floor) and
`remove_path`. `gate.actions` widens or narrows the set. Read-only calls and
safe-path commands are never held.

**The opinion.** Each held action gets an id; the partner answers that id
with `{"block":"none|suggest|strong","reason":"…"}`. It is a request and its
answer, not a lock: nothing is held while a model thinks except the one
action waiting for its own answer.

**The rules.** The existing verdict comes from `internal/shellrisk` (for
`bash`) or the tool's own gate.

| Existing verdict | Partner opinion | Decision |
|---|---|---|
| Blocked | any | blocked — the partner can never lower a block |
| Safe or Risky | `strong` | ask the person; headless: blocked, as Risky is today |
| Risky | `none` / `suggest` | ask the person, the partner's reason shown beside the classifier's |
| Safe | `suggest` | run; the reason is appended to the result for the coder |
| Safe | `none`, or no opinion in time | run, as today |

The table is monotone: an opinion can raise caution, never lower it — the
same rule the taint floor follows (issue #102). A `strong` opinion is not
skipped by a stored `a`/`p` session approval; the person is asked again.

**Timing.** This is the one place the coder waits on the partner: a held
action waits at most `gate.timeout_sec` (default 15). A late, failed, or
unparseable opinion is decided as if there were none — today's behavior —
and journaled `timeout` or `error`. So a slow partner costs at most the
timeout, and a broken one never makes an action less safe than today.

**Back to the coder.** A held or refused action returns the partner's reason
in its result, with the shared unknown-value tail every refusal carries. The
coder can answer the objection on its next round, or in a dialogue (below).

Every held action is journaled as `partner.gate`: the action, the existing
verdict, the opinion, the decision, latency, and what the person chose when
asked. Those rows are the gate's label set (How it is measured).

## Dialogue: going back and forth

Notes and gate opinions are one-way. Some moments need an exchange: the
reasoner raises an objection, the other side answers, the reasoner replies.
A dialogue is a bounded exchange between the reasoner and one other role.

| With | When | What it is for |
|---|---|---|
| `code` | `opening` (before the first tool call), `stuck`, and after a gate `strong` | the reasoner states its strongest objection to the plan or action; the coder answers with reasons, or changes course |
| `study` | any moment | the reasoner asks a question the record cannot answer; Study investigates, read-only and bounded, and reports — the reasoner gets evidence without tools of its own |

Any later role joins through the same seam: it needs a way to answer a
message, and bounds of its own.

- **Bounded.** `dialogue.max_exchanges` round trips (default 2), each message
  capped at `max_note_tokens`; at most `dialogue.max_per_turn` dialogues per
  turn (default 1).
- **Talk only.** The coder's side runs as a tools-withheld round (as the
  turn-end lint finalize round does), so a dialogue can change what the coder
  plans but cannot act. Study keeps its own read-only profile and bounds.
- **An outcome, not a verdict.** A dialogue ends with one line: `agreed`,
  `changed` (the coder changed course), or `disagreed`. Neither model wins a
  disagreement: the coder stays the actor, and for a held action the gate's
  rules decide.
- **Recorded.** Every message goes to the session transcript (so resume and
  `recall` see it exactly) and to the journal as `partner.dialogue`.
- **Shadow is still pure.** In `shadow`, the coder's side runs on a copy of
  the request and its answer is journaled, never added to the real
  transcript — so the coder's run is unchanged, at the price of the extra
  calls.

A dialogue costs coder time (its replies are coder requests), which is why
it fires only at the moments above. On a local fleet the reasoner resolves to
a separate model (order 2 of the binding), so its side runs beside the coder
rather than in its queue; the 2026-10-18 classifier probe saw a shared model
slow from about 1 s to 10 s a call under load, so the yield rule (a coder
send in flight on the same endpoint skips the partner's moment) applies to
dialogues too.

## The prompt

A micro-prompt, held to the same rules as the decider's
(docs/decide.md): language-neutral and under
`TestHarnessTextIsLanguageNeutral`; principles, not recipes; versioned
(`PartnerPromptVersion`), and rows from different versions never pooled.
Its principles: judge only from the record; a blocked or failed step did no
work; say what you would check and why, not what to type; if the work is on
track, say so in one line; never claim something was done that the record
does not show. Each moment adds one sentence naming what it is asked.

Notes are capped (`max_note_tokens`, default 300). A partner that runs out of
budget while reasoning produces no note — never a salvaged guess.

## Tuning

One top-level `partner` block (docs/configuration.md gets the full entry):

```json
{
  "partner": {
    "mode": "shadow",
    "effort": "high",
    "moments": {
      "opening": true,
      "rounds":  { "every": 3 },
      "stuck":   true,
      "review":  true,
      "between": true
    },
    "max_notes_per_turn": 4,
    "max_rounds_late": 2,
    "max_note_tokens": 300,
    "timeout_sec": 90,
    "show": "dim",
    "gate": {
      "mode": "off",
      "actions": ["bash", "remove_path"],
      "timeout_sec": 15
    },
    "dialogue": {
      "mode": "off",
      "with": ["code", "study"],
      "max_exchanges": 2,
      "max_per_turn": 1
    }
  }
}
```

- `mode`: `off` (nothing runs), `shadow` (runs and journals, nothing
  delivered), `advise` (delivered). Default `off`.
- `moments.*`: each moment on/off; `rounds.every` sets the cadence.
- `show`: how the REPL shows a delivered note to the person —
  `off`, `dim` (one-line summary in the tool-line stream), `full`.
- `/partner off|shadow|advise` adjusts the mode for the session, like `/hook`.
- `gate.mode` and `dialogue.mode` take the same three values, separately, so
  notes can be on while the gate stays in `shadow`. A gate in `shadow` asks
  for opinions and journals the decision the rules *would* have made, while
  today's decision stands.

Every note is journaled as `partner.note` in `.cortex/journal/partner/`:
moment, prompt version, model, effort, status (`ok`, `busy`, `stale`,
`timeout`, `rate_limited`, `budget`, `error`), latency, rounds late,
delivered or not, the note, and what the coder did next — the same
`actual` record the decider writes.

## How it is measured

Advice that does not change outcomes is cost. Following docs/benchmarks.md,
every run goes to the bench store, tagged `--label partner=<mode>`.

1. **Plumbing (shadow).** A short polyglot probe with `--partner-model`:
   notes are produced at each moment, latency and rounds-late are recorded,
   and the coder's own run is unchanged (same pass/fail, no rate-limit
   errors). Shadow is a pure shadow, or it is not shadow.
2. **Prerequisite.** The SWE-bench baseline's labels measure "cortex with a
   broken classifier" (docs/decide.md, measurement plan step 1). That is
   fixed before any partner A/B is read on SWE-bench; polyglot is unaffected.
3. **Paired A/B.** Same instances, `partner=off` vs `partner=advise`,
   discordant pairs scored by McNemar. Primary: pass/resolve rate.
   Secondary: wall time, tokens, rounds, and the unsupported-claim rate of
   final replies. Per-moment arms (`opening` only, `stuck` only, all) follow
   once the full arm shows an effect, to learn which moments carry it.
4. **Topology arms.** The same A/B with the partner resolved at order 1
   (separate reasoner) and order 4 (coder at high effort), so the result
   says whether the gain needs a second model.

`advise` becomes a default only on a step-3 result whose interval excludes
zero. Until then the partner is an opt-in.

The gate and dialogue are measured on their own:

5. **Gate, offline.** The `scripts/bench/classifier` cases (hand-labeled
   commands) plus the gray-zone commands in the journal, each given to the
   partner. Scored: how often a `strong` lands on a truly risky action
   (precision), how many risky actions get one (recall), and how much more
   often the person would be asked. A rule changes only on these numbers.
6. **Gate, shadow.** `partner.gate` rows from real sessions: the rules'
   would-be decision against what the person actually chose when asked.
7. **Dialogue A/B.** Paired, as step 3, `dialogue=off` vs `dialogue=advise`,
   with a separate arm per partner role (`code`, `study`).

## Build order

1. `reason` role + the resolution chain + tests (no caller yet; the chain is
   testable on its own).
2. `internal/partner`: the snapshot reuse from `internal/decide`, the
   prompt, the moment triggers, the one-in-flight scheduler; `shadow` mode
   journaling via the hooks `decide_shadow.go` already uses.
3. `scripts/bench/polyglot --partner-model` and the step-1 probe.
4. `advise` delivery at `BeforeSend` + transcript recording + resume.
5. REPL display + `/partner`, `cortex turn` JSON key.
6. Step 3's A/B.
7. Gate opinions in `shadow`: action ids, the opinion call, the rules table
   as a pure function with table tests, `partner.gate` journaling; step 5's
   offline score.
8. Gate `advise`: the rules decide live, under `gate.timeout_sec`.
9. Dialogue with `study`, then with `code`; step 7's A/B.

## What this does not do

No new engine, no per-request model router, no open-ended debate, no
partner tools. The coder stays the only actor; the partner reads, speaks,
and — at the gate — suggests. Dialogues are bounded and end with an outcome
line; no model decides a held action — the rules do. The partner gets
evidence by asking Study, never by running tools itself. If a later result
says the partner should act (bounce a turn, rewrite a plan), or that the
decider should be a model, that is a separate design with its own
measurement.
