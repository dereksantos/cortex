# Prompt: sense.classify_intent

Routes each REPL turn into one of six intent categories *before* any heavy work runs. The classification drives two things the rest of the turn depends on:

1. **Per-intent budget** via `dag.BudgetForIntent` — cheap intents get tight budgets so a misroute can't blow the turn open.
2. **Seed shape** — high-confidence trivial intents bypass the `sense.prompt → decide.next → decide.coding_turn` chain in favor of dedicated terminal nodes: `act.passthrough` for greetings, `decide.clarify` for ambiguity, `decide.recall_summary` for "what did we already do" questions.

Template: [`pkg/cognition/prompts/sense_classify_intent.tmpl`](../../pkg/cognition/prompts/sense_classify_intent.tmpl)
Op: [`pkg/cognition/dag/ops/sense_classify_intent.go`](../../pkg/cognition/dag/ops/sense_classify_intent.go)

## Categories

| Intent     | Fires when…                                                                                | Routed to                                          | Budget            |
|------------|--------------------------------------------------------------------------------------------|----------------------------------------------------|-------------------|
| `greeting` | "hi", "hello", "thanks", "ok" — short conversational acks; no action requested             | `act.passthrough` (canned reply, zero LLM)         | 2s / 300 tok / depth 3 |
| `recall`   | "what did we decide about X", "remind me", "what was the rationale"                        | `decide.recall_summary` (text-search → small-LLM synthesis) | 20s / 3000 tok / depth 5 |
| `clarify`  | Too vague/ambiguous; one focused question would unblock the turn                            | `decide.clarify` (one short LLM call → one question, end turn) | 3s / 500 tok / depth 3 |
| `code`     | Write, modify, refactor, fix, add code; run build/test; edit files                          | `sense.prompt → decide.next → decide.coding_turn`  | `DefaultTurnBudget` (150s / 10k tok) |
| `review`   | Read, explain, audit, summarize existing code or docs — no writes                          | Same as code today                                 | 60s / 5000 tok / depth 8 |
| `meta`     | About Cortex itself: settings, REPL commands, model choice, journal/DAG state              | Same as code today                                 | 10s / 2000 tok / depth 4 |

## Disambiguation rules (in the prompt)

- **code vs review**: if the prompt names a change to make → `code`; if it asks how something works → `review`.
- **review vs clarify** (Slice 3.5 fix): if the prompt is a verb the agent can act on (`research`, `explore`, `audit`, `explain`, `find`, `list`) without a specific target → `review`. The agent has tools to find the target. Only pick `clarify` when there are literally two unrelated interpretations the agent can't choose between. "Research the project" is `review`, not `clarify`.
- **greeting vs anything**: a one-word friendly opener with no other content → `greeting`. "Hi, fix the auth bug" is `code`, not `greeting`.

## Output contract

```json
{"intent": "greeting|recall|clarify|code|review|meta", "confidence": 0.0-1.0, "why": "≤8 words"}
```

Strict JSON only; no markdown fence, no prose.

## Confidence threshold

The REPL applies `intentShortCircuitThreshold = 0.7` (defined in `cmd/cortex/commands/repl.go`) to gate every trivial-intent short-circuit (`greeting`, `clarify`, `recall`). Below the threshold, every intent routes through the full chain. Better to pay coding-turn cost than to give a canned "Hi", a wrong clarifying question, or an unrelated recall to someone who actually wanted real work done.

The budget profile is applied regardless of confidence — it's a *cap*, not a directional signal. A misclassified low-confidence `code` request still gets `DefaultTurnBudget` since `code` IS the safe-default.

## Failure modes

All failure paths return `intent="code"` with `confidence=0`:

- `Provider == nil` or `!Provider.IsAvailable()` → fallback `"provider unavailable or budget exhausted"`
- `!budget.CanAfford(classifyIntentCostHint)` → same fallback
- Template load error → fallback `"template load: <err>"`
- LLM error → fallback `"llm error: <err>"`
- JSON parse error → `intent=code`, `confidence=0`, `why="parse error: <err>"`
- Unknown intent label (model emitted "random-label") → normalized to `code`, confidence reset to 0

The safe-default routes downstream to today's full pipeline — misclassification can never block a turn.

## Cold-journal recall escalation (Slice 3.5)

After classification, the REPL applies `downgradeRecallIfNoContext` (in `cmd/cortex/commands/repl.go`): if `intent == "recall"` and a probe against `Storage.SearchEventsMultiTerm` returns zero hits, the intent is downgraded to `code` so the seed falls through to the full chain under `DefaultTurnBudget`. The agent investigates (list dirs, read README) instead of replying "no prior context indexed".

The probe tokenizes the prompt into content words (≥4 chars, stopwords stripped) before searching — full-string substring matching against natural-language recall questions practically never hits. With nil storage or an all-stopwords prompt, the downgrade also fires.

## Defense-in-depth (budget × intent)

`pkg/cognition/dag/budget_test.go` enforces that `BudgetForIntent("greeting")` and `BudgetForIntent("clarify")` cannot afford `decide.coding_turn`'s cost hint (`15s / 2000 tok`). If a classifier mis-routes a code prompt as greeting, the executor's pre-spawn `CanAfford` gate refuses to spawn the coding-turn — the turn dies fast instead of ballooning.

## Cost calibration

Initial cost hint: `2s / 200 tok` (lighter than `decide.should_capture`'s `16s / 350 tok` because the prompt is shorter and the output is fixed-shape ~30 tokens). Tighten after the first real-LLM calibration pass against the same provider used elsewhere in `cmd/cortex/commands/repl.go:buildLLMProviderForREPL`.

## Clarify follow-up stitching (Slice 4)

When the prior turn routed to `decide.clarify`, `runREPLChainTurn` stashes the original ambiguous prompt on `replState.lastClarifyPrompt`. The next turn's `stitchClarifyFollowUp` combines the prior prompt with the user's answer before re-classification — `"delete it"` + `"the migrations table"` becomes a single combined prompt the classifier picks up as `code` rather than another fragment. Stash is one-shot: cleared after consumption, replaced when the new turn also clarifies, wiped on any non-clarify seed so a stale clarify doesn't follow the user across an unrelated turn.

## Feedback auto-emit (Slice 4)

`detectFeedbackCue` runs at the top of `runTurn` against the raw user prompt. When it fires (`correction` from `"no, "`, `"wrong"`, `"actually"`, `"that's not"`, `"don't"`, `"instead"`, … / `confirmation` from `"perfect"`, `"thanks"`, `"got it"`, `"yes that"`, `"exactly"`, `"that works"`, `"nice"`, …), `emitFeedbackEntry` writes a `feedback.correction` or `feedback.confirmation` entry to `.cortex/journal/feedback/` linked to the prior turn via `GradedID="repl-<sessionID>-turn-<N>"`. Word boundaries keep false positives in check — `"no problem"` doesn't trigger correction. Best-effort: failures log + continue; turn 1 emits are skipped (no prior turn to grade).

## Forever-session digest (Slice 5)

`maybeWriteSessionDigest` runs at the end of every accepted turn. When the journal accumulates more than `sessionDigestThreshold = 15` `think.session_summary` entries since the most recent `dream.session_digest` (or since session start), it folds the head of the summary history into a single consolidated narrative via `attend.compact` with a session-digest-specific intent string, then writes a `dream.session_digest` entry.

Hydration (`priorMessagesForHarness`) now prefers the digest over raw history:

- `readLatestSessionDigest` returns the most recent digest payload (nil when none).
- `readRecentSessionSummaries` skips the first `digest.SummaryCountIn` summaries (the ones already folded in) and applies the existing `historyLimit` / `priorSessionsCap` to the remainder.
- `digestAndSummariesAsChatMessages` renders the combined block — digest narrative as a labeled `[digest covering N earlier turn(s)] …` chunk, then post-digest summaries with their `[intent=…]` tags. When no digest exists, behaves identically to the pre-Slice-5 path.

Net effect: a 100-turn session injects `[digest of first 90 turns] + [last 10 summaries]` instead of `[10 most recent of 100 summaries, older 90 forgotten]`. Long sessions get logarithmic-ish memory growth rather than linear forgetting.

## Open follow-ups (from the integration TODO)

- ~~**Slice 2**: dedicated seeds for `recall` (text-search + small synthesis) and `clarify` (one-question `decide.clarify`).~~ Shipped.
- ~~**Slice 3**: add `Intent` field to `think.session_summary` payload so journal recall can filter by intent.~~ Shipped.
- ~~**Slice 4**: clarify follow-up stitching + feedback writer-class auto-emit.~~ Shipped.
- ~~**Slice 5**: `dream.session_digest` via `attend.compact`; hydration prefers digest + post-digest summaries.~~ Shipped.
- **Slice 2 follow-up**: `decide.recall_summary` uses text search (`Storage.SearchEventsMultiTerm`) today because the REPL session doesn't wire an embedder through `newSessionCognition`. Add an embedder + switch recall to vector search when it lands.
- **Slice 5 follow-up — digest invalidation**: a `feedback.retraction` inside the digest's covered window should mark the digest stale so the next finalize re-runs `maybeWriteSessionDigest`. Today the digest stays in place until naturally superseded by the threshold crossing.
- **Slice 6 / observability**: the classifier currently runs *outside* the DAG executor, so it doesn't appear in `dag_traces.jsonl`. Synthesize a trace row from the handler result so the routing decision is preserved alongside the seed.
