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
- **greeting vs clarify**: a one-word friendly opener → `greeting`; a real-but-vague ask → `clarify`.

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

## Defense-in-depth (budget × intent)

`pkg/cognition/dag/budget_test.go` enforces that `BudgetForIntent("greeting")` and `BudgetForIntent("clarify")` cannot afford `decide.coding_turn`'s cost hint (`15s / 2000 tok`). If a classifier mis-routes a code prompt as greeting, the executor's pre-spawn `CanAfford` gate refuses to spawn the coding-turn — the turn dies fast instead of ballooning.

## Cost calibration

Initial cost hint: `2s / 200 tok` (lighter than `decide.should_capture`'s `16s / 350 tok` because the prompt is shorter and the output is fixed-shape ~30 tokens). Tighten after the first real-LLM calibration pass against the same provider used elsewhere in `cmd/cortex/commands/repl.go:buildLLMProviderForREPL`.

## Open follow-ups (from the integration TODO)

- ~~**Slice 2**: dedicated seeds for `recall` (text-search + small synthesis) and `clarify` (one-question `decide.clarify`).~~ Shipped — `decide.recall_summary` and `decide.clarify` are wired in `seedForIntent`.
- **Slice 2 follow-up**: `decide.recall_summary` uses text search (`Storage.SearchEvents`) today because the REPL session doesn't wire an embedder through `newSessionCognition`. Add an embedder + switch recall to vector search when it lands.
- **Slice 3**: add `Intent` field to `think.session_summary` payload so journal recall can filter by intent.
- **Slice 4**: clarify follow-up stitching — the next user turn after `decide.clarify` is an answer to the question; carry the prior turn's intent forward so the answer doesn't get re-classified as a fresh ambiguous turn.
- **Slice 6 / observability**: the classifier currently runs *outside* the DAG executor, so it doesn't appear in `dag_traces.jsonl`. Synthesize a trace row from the handler result so the routing decision is preserved alongside the seed.
