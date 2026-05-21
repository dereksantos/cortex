# Prompt: sense.classify_intent

Routes each REPL turn into one of six intent categories *before* any heavy work runs. The classification drives two things the rest of the turn depends on:

1. **Per-intent budget** via `dag.BudgetForIntent` — cheap intents get tight budgets so a misroute can't blow the turn open.
2. **Seed shape** — trivial intents bypass the `sense.prompt → decide.next → decide.coding_turn` chain in favor of cheaper terminal nodes (today: `act.passthrough` for greetings; clarify/recall paths coming in follow-up slices).

Template: [`pkg/cognition/prompts/sense_classify_intent.tmpl`](../../pkg/cognition/prompts/sense_classify_intent.tmpl)
Op: [`pkg/cognition/dag/ops/sense_classify_intent.go`](../../pkg/cognition/dag/ops/sense_classify_intent.go)

## Categories

| Intent     | Fires when…                                                                                | Routed to                                          | Budget            |
|------------|--------------------------------------------------------------------------------------------|----------------------------------------------------|-------------------|
| `greeting` | "hi", "hello", "thanks", "ok" — short conversational acks; no action requested             | `act.passthrough` (canned reply, zero LLM)         | 2s / 300 tok / depth 3 |
| `recall`   | "what did we decide about X", "remind me", "what was the rationale"                        | (today: full chain; future: recall-only synthesizer) | 20s / 3000 tok / depth 5 |
| `clarify`  | Too vague/ambiguous; one focused question would unblock the turn                            | (today: full chain; future: `decide.clarify`)      | 3s / 500 tok / depth 3 |
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

The REPL applies `intentPassthroughThreshold = 0.7` (defined in `cmd/cortex/commands/repl.go`) to gate the trivial-intent short-circuit. Below the threshold, every intent — including `greeting` — routes through the full chain. Better to pay coding-turn cost than to give a canned "Hi" to someone who actually wanted help.

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

- **Slice 2**: dedicated seeds for `recall` (vector-search + small synthesis) and `clarify` (one-question `decide.clarify`). Today both fall back to the full chain but run under their tighter intent budgets.
- **Slice 3**: add `Intent` field to `think.session_summary` payload so journal recall can filter by intent.
- **Slice 6 / observability**: the classifier currently runs *outside* the DAG executor, so it doesn't appear in `dag_traces.jsonl`. Synthesize a trace row from the handler result so the routing decision is preserved alongside the seed.
