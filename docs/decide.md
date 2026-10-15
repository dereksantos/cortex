# The decider — a parallel "think" stream

> Status (2026-10-15): **shadow only.** `internal/decide`, the offline runner
> (`scripts/bench/decide`), and the live shadow wiring are built. A live
> session runs a decider only when the user config enables `decide`, and no
> decision ever reaches the coder. This doc is the authority for the design
> and for how its value is measured.

## The idea

A small decision model runs beside the coding model — the little voice in the
agent's head. It never acts. It watches the record of the turn and answers a
few closed-choice questions about it:

| Question | Choices | Asked |
|---|---|---|
| `next_move` | read / search / edit / test / run / recall / finalize | after each tool batch |
| `looping` | the ids of recent steps that repeat without new information, or `none` | after each tool batch |
| `claim_check` | supported / unsupported / no_claims | at the final reply |
| `recall_target` | the session-outline citations, or `none` | reserved — see below |

Closed choices keep the voice cheap, checkable, and swappable: the same `Ask`
fits a chat model asked for JSON today and a scoring backend (log-probs, a
hosted decision model) later.

## Shape

```go
type Decider interface {
    Decide(ctx context.Context, s Snapshot, asks []Ask) Decision // never errors
    Name() string
}
```

- **Snapshot** — a bounded, language-neutral digest: the task, the last
  `RecentSteps` (12) tool calls with clipped args and head+tail-clipped results
  (tool output states its verdict last), a whole-turn **ledger** (tool counts,
  files changed, every command with its last output line or a did-not-run
  marker), and — at finalize — the reply.
- **Decision** — the answers plus `Status` (`ok`, `timeout`, `rate_limited`,
  `parse_error`, `budget`, `error`, `disabled`), latency, model, and
  `PromptVersion`. Anything but `ok` carries no answers: a caller skips it.
- **Backends** — `Null` (default, decides nothing), `Chat` (any
  OpenAI-compatible endpoint asked for strict JSON — OpenRouter free models,
  SubQ, a local server), `Logprob` (stub: interface only).
- **Validation** — every pick must come from its ask's choices; an off-menu or
  unparseable answer is a `parse_error`, never kept.

The prompt (`internal/decide/prompt.go`) is a micro-prompt: three principles
(judge only from the record; a blocked or failed step did no work; answer only
from the choices) plus one definition per question. It is part of
`TestHarnessTextIsLanguageNeutral`. Any change to it changes `PromptVersion`,
and rows from different versions are never pooled.

## The timing constraint

The engine sends the next request the moment a tool batch ends (`runLoop`:
dispatch → `BeforeSend` with no model call between). A decision started when
a batch ends therefore **can never reach the very next send**. The earliest
send it can reach is the one after the agent's next round — so the live
question is whether a decision fits inside one agent round. The replay
measures this offline from transcript timestamps (`decide.Point.Window`).

## How it is measured

**Step 1 — does it carry signal? (offline, shadow).** A decider is a pure
function of a Snapshot, so stored runs can be replayed through it without
re-running the agent:

```bash
go run ./scripts/bench/decide eval   --label arm=decider-eval            # the prompt's own eval
go run ./scripts/bench/decide replay --source <swebench batch dir> \
     --label arm=decider-shadow --label source=<batch id>
go run ./scripts/bench/decide analyze ~/.cortex/benchmarks/runs/decide-replay/<run-id>
```

- `eval` (bench `decide-eval`) scores the prompt + model on hand-built cases
  (`decide.EvalCases`) — a floor check: format, choices, unambiguous cases.
- `replay` (bench `decide-replay`) cuts each stored cortex transcript into
  decision points (≤ `--points` per task, evenly spaced, plus finalize) and
  writes `decisions.jsonl` (one row per question, with the agent's actual
  next move and the point's time window) and `results.jsonl` (one row per
  task whose status is the **source task's outcome** — the label).
- `analyze` scores each per-task signal by AUROC against "task failed", next to
  the mechanical signals the decider must beat (rounds, failed steps, tool
  calls), plus next_move agreement by label and readiness. It adds
  `analysis.json` to the run dir.

The bar for going further: a decider signal that separates failing from
resolved tasks **better than the mechanical signals**, at a latency that fits
an agent round. Below that bar, acting on it cannot help.

**Step 2 — does acting on it help? (paired A/B, not built).** Same instances,
hint-off vs hint-on arms, discordant pairs scored by McNemar. Primary: resolve
rate; secondary: tokens, wall time, unsupported-claim rate in final replies.
At a ~50% pass rate a +10-point effect needs on the order of 150–200
instances; 50 resolves only a large effect.

`recall_target` is not asked on SWE-bench: those runs are single long turns
that rarely demote context, so there is nothing to recall. It needs a
multi-turn benchmark (the pivot eval design, or SWE-bench tasks split across
turns) before it can be measured.

## Live wiring (shadow)

`cmd/cortex/decide_shadow.go`, wired in `turn.go`:

- `Toolset.AfterBatch` (nil for every caller but the coder) hands each tool
  round's calls and observations to the shadow decider, which builds a
  snapshot from memory and starts the decision on its own goroutine.
  `BeforeSend` only counts the send. `endTurn` resolves the last point
  (`finalize`, or `forced:<stop reason>`) and starts the finalize
  `claim_check` on the reply. None of these waits, does I/O, or calls a model.
- At most one decision is in flight; a round that ends while one is running
  is journaled as `busy`. Journal writes go through one background writer.
- Each answer is journaled as `decide.shadow` in `.cortex/journal/decide/`:
  question, choices, picks, status, latency, model, prompt version,
  `rounds_late` (sends that went out between the decision's start and its
  completion — 1 is the best possible), and `actual` (what the agent did
  next: its moves and tools, `finalize`, or `forced:<reason>`).
- Session close waits up to `decide.drain_sec` for decisions still in flight
  (after the answer is out — process exit only).
- Config: the top-level `decide` block (docs/configuration.md), off by
  default, **honored only from the user config** — the decider sends turn
  excerpts to its own endpoint, a new outbound path a repository must not be
  able to switch on or redirect — refused under `CORTEX_LOCAL_ONLY`, and
  announced once on stderr naming the destination.
- Live probe: `scripts/bench/polyglot` takes `--decide-model` (plus
  `--decide-endpoint`, `--decide-key-service`) and writes the block into the
  run's own CORTEX_HOME; each exercise's rows land in its workspace journal
  inside the run dir.
