# Thinking models: effort as a first-class harness control

## Why

Cortex's entire thinking-model vocabulary today is one boolean, expressed only
as an off-switch, in one provider dialect: `ModelSpec.Thinking *bool` →
`{"enable_thinking": false}` in `chat_template_kwargs`. There is no way to
force thinking on, no levels/effort/budget control, no other provider dialect,
and the response side treats reasoning as display-only ephemera — with no
`<think>`-tag fallback parsing at all.

The harness already carries scar tissue proving this matters:

- **`reFinalizePrompt`** (`cmd/cortex/loop.go:161`) — "you spent the whole
  budget thinking and never answered" — exists because `MaxTokens` is a single
  number with no thinking/answer split. A deliberating model can burn the
  whole completion cap on reasoning and emit nothing; the salvage prompt is a
  coping mechanism for a missing control.
- **The 37.8s incident** (`pkg/llm/provider_factory.go:163-167`) — factory-routed
  calls without per-model kwargs ran a hybrid model with thinking ON and the
  budget classifier burned its whole 30s deadline on reasoning_content
  (37.8s vs 1.6s measured, 2026-06-12).
- **The study reversal** — "thinking off for study" was recommended, shipped,
  and then falsified (2026-06-28): study now runs the reasoner tag with
  thinking ON. Effort policy is a live, per-role tuning surface; today it can
  only be tuned between two positions, one of which ("on") isn't even
  expressible — it's just the absence of "off".

And it matters *more* for this project than for a frontier harness: the
target is small local models, where hybrid thinking (Qwen3, GLM) is the norm,
serving stacks differ in how they surface reasoning, and a wrong default costs
a 30-second turn on a 2-second task.

## Today's state (survey)

**Request side.** `rolePolicies` (`cmd/cortex/config.go:40`) hard-codes
`thinkingOff` for `code`/`fast`; `reason`/`study` get model-default thinking.
`TemplateKwargs()` (`config.go:106`) emits `{"enable_thinking": false}` when
`Thinking` is explicitly false, nothing otherwise. Fleet discovery reads a
per-model `thinking: bool` from LiteLLM `/model/info` and `applyFleet` clears
the flag for non-thinking models — but `resolveBinding` re-stamps the user's
config value afterwards (`config.go:324-329`), and OpenRouter backends skip
discovery entirely, so the kwargs go out uncontrolled there.

**Response side.** Streaming parses exactly one field, `reasoning_content`
(`pkg/llm/stream.go:34`), accumulates it display-only (live ticker tail +
140-rune breadcrumb, `cmd/cortex/streaming.go`), and drops it — never stored,
never persisted, never re-sent. The blocking path (`Send`, used by headless
`cortex turn`) doesn't parse it at all: `Message` has no reasoning field.
OpenRouter's `reasoning` response field is not parsed. Inline
`<think>…</think>` fences are not handled anywhere — a server that doesn't
split reasoning out (raw llama.cpp without `--reasoning-format`, Ollama with
R1-style models) leaks chain-of-thought into `Content`, which then pollutes
the transcript, `captureTurn`, and the demotion outline forever.

**Known seam bugs.**

1. `SetModel` (`cmd/cortex/session_core.go:106`) swaps only the model name;
   `ChatTemplateKwargs` and `MaxTokens` stay from the old binding. `/model`
   (and the web UI's session override) can silently run a hybrid reasoner
   with the coder's `enable_thinking: false`, or a thinking model with no
   suppression and a 16k cap.
2. `subagentRequest` (`cmd/cortex/study.go:46-48`): a per-call `model` arg on
   the `agent` tool pins the model name but inherits the spawning model's
   kwargs.
3. Anthropic extended thinking with tool use cannot work at all: Anthropic
   requires thinking blocks round-tripped across tool-use turns; `Message`
   can't represent them.

## The design

### 1. A levels vocabulary on ModelSpec

Replace the boolean with a small closed vocabulary, JSON-compatible with the
existing bool (accept both; bool maps to `"off"`/`"on"`):

```json
{
  "models": {
    "code":  { "model": "...", "thinking": "off" },
    "study": { "model": "...", "thinking": "on" },
    "hard-code": { "model": "...", "thinking": "high" },
    "reason": { "model": "...", "thinking": { "budget": 8192 } }
  }
}
```

- `"off"` — suppress thinking.
- `"on"` — affirmatively enable at the model's default depth
  (`enable_thinking: true` / `reasoning: {enabled: true}`). Not an omission:
  hosted defaults often resolve to no reasoning (measured on tencent/hy3 via
  OpenRouter, 2026-07-17), so on-as-omission was indistinguishable from off.
  Unset is the send-nothing/model-default state. As of 2026-07-17 the `code`
  role defaults to `on` — deliberation by default; effort-off is opt-in via
  config (`fast` stays off; the summarizer/shell-risk call sites stay pinned
  off).
- `"low" | "medium" | "high"` — effort levels, for dialects that have them.
- `{ "budget": N }` — token budget, for dialects that have that.

Levels degrade gracefully: a dialect with no levels treats `low/medium/high`
as `on`; a dialect with budgets maps levels to fixed budget tiers. The spec
records *intent*; the transport seam translates.

### 2. Dialect translation at the transport seam

One translation function, keyed by backend/endpoint type, called where
`TemplateKwargs()` is called today (`requestFor`, session init, tool deps):

| Dialect | off | levels | budget | reasoning in response |
|---|---|---|---|---|
| llama.cpp / LiteLLM (`chat_template_kwargs`) | `enable_thinking: false` | — (treat as on) | — | `reasoning_content` or inline `<think>` (depends on `--reasoning-format`) |
| OpenAI-compat (`reasoning_effort`) | `"minimal"`/omit | `low/medium/high` | — | usually withheld; vLLM emits `reasoning_content` |
| OpenRouter (`reasoning: {...}`) | `{enabled: false}` | `{effort: "..."}` | `{max_tokens: N}` | `reasoning` field (+ `reasoning_details`) |
| Anthropic (`thinking: {...}`) | omit | map to budget tiers | `{type: "enabled", budget_tokens: N}` | `thinking` content blocks; MUST round-trip during tool use |
| Ollama native (`think`) | `false` | `"low"/"medium"/"high"` (gpt-oss) | — | `message.thinking` |

The fleet `/model/info` descriptor grows from `thinking: bool` to an optional
`thinking_mode: "none" | "hybrid" | "levels" | "always"` (bool stays accepted;
`true` → `hybrid`). `applyFleet` uses it to refuse impossible asks (levels on
a hybrid on/off model → `on`) instead of just clearing the flag.

### 3. Response normalization (the correctness fix)

One normalization step where responses are assembled (`StreamChat` +
`sendOnce`), before anything is appended, captured, or displayed:

- Parse `reasoning_content` **and** `reasoning` (OpenRouter) into the same
  accumulated trace. The blocking path gains the same field so headless turns
  see the same shape.
- **Strip inline `<think>…</think>` fences from `Content`** and fold them
  into the reasoning trace. Fence-tolerant (unclosed fence at
  end-of-completion = the max-tokens-clamp signature; treat everything after
  the open fence as reasoning). This is the single highest-severity fix in
  this doc: today it is silent context poisoning on common local stacks.
- Reasoning stays display-only: ticker + breadcrumb, never in `Messages`,
  never in the transcript. (The one exception is the deferred Anthropic
  block round-trip — see don't-build.)

### 4. Budget split

`Bounds`/`requestFor` learn a thinking allowance distinct from the answer
cap. Where the dialect has a real budget (Anthropic, OpenRouter
`max_tokens`), pass it through. Where it doesn't (llama.cpp), approximate:
when a turn's completion was clamped **and** the reasoning trace consumed
>~80% of it, the salvage path (`reFinalizePrompt`) re-asks with thinking
**off** instead of just pleading for brevity — turning the existing prompt
hack into an actual control. `rewriteClampedPrompt` gets the same treatment.

### 5. Effort as a harness escalation primitive

The engine has natural points where effort should change, all currently
inexpressible:

- **Finalize** (tools withheld) is a formatting ask → thinking `off`.
- **`hard-code`** role → `high` by default.
- **Stuck** (the no-progress guard) → today jitters temperature; optionally
  escalate effort one tier for the post-redirect re-sample, same one-shot
  revert pattern as `stuckJitterTemp`.

These are per-request overrides layered on the role's spec — the request
carries a resolved effort, not a pointer to the spec.

### 6. Seam fixes

- `SetModel` re-resolves the binding (kwargs, max_tokens, window) for the new
  model — via `applyFleet` when a fleet is known, else clears to neutral.
- `subagentRequest` with a per-call `model` clears inherited kwargs unless
  the fleet says the new model is hybrid.

## Coverage map — every surface thinking crosses

Model thinking is a vertical: it enters at config, travels the wire, burns
budget in the engine, and surfaces (or fails to) at every place a human or a
log looks. The design sections above cover the wire and the engine; this map
enumerates *all* the surfaces so nothing is covered by accident. Categories
are by surface, mutually exclusive; each row names the smallest change with
the biggest impact there.

| # | Surface | What thinking must do there | Today | Small thing, big impact |
|---|---|---|---|---|
| 1 | **Wire — request** (`transport.go`, `openai_compat.go`, factory) | Carry resolved effort in the right dialect per backend | One dialect, off-only | §1–2 above (P2) |
| 2 | **Wire — response** (`stream.go`, `sendOnce`) | Split reasoning from answer on every path, every server style | `reasoning_content` streaming-only; `<think>` fences leak; blocking path blind | Fence stripping + `reasoning` alias + blocking parity (P1). Also: parse `completion_tokens_details.reasoning_tokens` from usage — one struct field, and the budget heuristic, telemetry, and evals all stop guessing |
| 3 | **Engine & budgets** (`loop.go`, `Bounds`) | Deliberation must not eat the answer budget; effort should move at engine decision points | Single `MaxTokens`; salvage prompts plead after the fact | Finalize/salvage re-ask with thinking **off** (P4); stuck-guard escalates effort not just temperature (P5) |
| 4 | **Interactive stdout** (`streaming.go`, `lineedit/live.go`) | Long deliberation must read as progress, not a hang; cost of thought visible at turn end | Ticker tail + breadcrumb exist; **no elapsed time anywhere**, no turn-end trace of thought spent | Elapsed counter on the status row ("thinking… 12s"), and a turn-end stat line ("thought 14s · 1.1k tok"). Two tiny changes; the entire perceived-latency story |
| 5 | **Headless & adapters** (`turn.go`, `serve_session.go`, `discord.go`) | Same normalization as interactive; some visible liveness signal per medium | `cs.quiet` → blocking `Send` → reasoning unparsed entirely; web UI session looks dead during deliberation; Discord has only the typing refresh | P1 parity fixes correctness everywhere at once. Web: one `thinking` SSE event (on/off) on the session stream so the UI can show a state, not silence. Discord: typing indicator suffices — explicitly no work |
| 6 | **Loops & unattended runs** (`loop_run.go`, serve loops) | Nobody is watching: latency tolerance up, but cost and slot-pinning (swap groups) matter per firing | Loop firings inherit role policy blindly | Loop `Spec` gets an optional `thinking` override (rides P2's vocabulary); default stays role policy |
| 7 | **Persistence & context** (transcript, `captureTurn`, demotion/outline, resume) | Traces stay OUT — of the transcript, the journal capture, the outline, and resume replay | Correct by design *if* fences never leak into `Content` | P1 is the whole fix; add a resume-replay test asserting no fence bytes survive in any stored message |
| 8 | **Accounting & telemetry** (`Usage`, `loopStats`, `study.result`, `EvalCellResultPayload`) | Deliberation cost must be attributable — per turn, per role, per eval row | Reasoning tokens hide inside `CompletionTokens`; **eval payload records `temperature` but not thinking config** — rows can't be grouped by the very knob under test | Add `thinking` (resolved effort) + `reasoning_tokens` to `EvalCellResultPayload` and `study.result`. Without this every A/B on thinking policy is unlabeled data |
| 9 | **Config & model management** (`config.go`, fleet, `/model`, web UI `PUT /api/models/{role}`) | One vocabulary, resolved per binding, surviving model switches | Bool + the `SetModel` stale-kwargs bug; web UI write path knows nothing | P2 vocabulary + P3 `SetModel` re-resolve; web UI models panel shows the fleet's `thinking_mode` per model (read-only is enough) |
| 10 | **Evals & gates** (`study-eval`, live evals, probe-before-run) | Thinking on/off/effort is an eval *dimension*, not ambient state | The study thinking-off reversal happened precisely because this wasn't a first-class dimension | A `CORTEX_*_THINK`-style knob on the live evals + the row-labeling from #8; per standing practice, a minutes-scale probe before any effort-policy change ships |

Terminology hazard: the journal's dormant **Think/Dream** cognition modes
(`internal/journal/think.go`, `cliout/telemetry.go` mode strings) predate this
doc and have nothing to do with model thinking. Don't reuse the word in new
identifiers — use *reasoning* (traces, tokens) and *effort* (the control) in
code; "thinking" survives only in user-facing strings and the config key.

## What we do NOT build

- **Reasoning persistence in the transcript.** Traces are ephemeral. The
  journal may keep the one-line breadcrumb it already gets; nothing more.
- **Anthropic thinking-block round-trip.** Required for extended thinking +
  tool use, but Anthropic is not in the local fleet path this project
  centers on. Deferred until an Anthropic backend is actually driven
  agentically; until then the translation layer simply refuses (`thinking:
  off` for Anthropic + tools) rather than half-working.
- **Automatic per-task effort inference.** No classifier deciding "this turn
  deserves high". The model can be given a self-serve control later (a
  `set_effort` context tool, in the spirit of the context-curation tools) if
  evals show the need. Start with static role policy + the three engine
  escalation points.
- **Reasoning-trace scoring/judging.** We don't parse, grade, or act on the
  content of the trace beyond the byte count used by the budget heuristic.
- **Qwen3 `/think` `/no_think` soft switches.** Prompt-side toggles are
  fragile (they persist oddly across turns in some templates); the
  template-kwarg path covers the same models.

## Phased plan

- **P1 — response normalization.** `reasoning` alias + `<think>` fence
  stripping + blocking-path parity, plus parsing
  `completion_tokens_details.reasoning_tokens` from usage where the server
  reports it. Pure correctness, no config change, no behavior change for
  well-behaved servers. Tests: fence variants (split across SSE chunks,
  unclosed at clamp, fence-only completion), OpenRouter fixture,
  blocking-path fixture, and a resume-replay test asserting no fence bytes
  survive in any stored message. This ships first and alone.
- **P2 — the vocabulary + translation.** `ThinkingSpec` on `ModelSpec`
  (bool-compatible JSON), the dialect table, fleet `thinking_mode`
  descriptor. Existing configs keep working byte-for-byte.
- **P3 — seam fixes.** `SetModel` re-resolve, subagent model-pin kwargs.
  Table tests over the switch matrix (thinking→non, non→hybrid, fleet
  known/unknown).
- **P4 — budget split + salvage demotion.** Thinking-aware clamp detection;
  `reFinalizePrompt`/finalize re-ask with thinking off. Measured by the
  existing study-eval ø gate (clean-finalize rate) — the expectation is the
  salvage path fires less, not more.
- **P5 — escalation points.** Finalize-off, hard-code-high, stuck-escalate.
  Opt-in, eval-gated (live fleet probe before any long run, per standing
  practice).
- **P6 — presentation + attribution smalls.** The coverage-map quick wins,
  each independent and tiny: elapsed counter on the status row
  ("thinking… 12s"), turn-end thought stat ("thought 14s · 1.1k tok"),
  a `thinking` on/off event on the serve session stream, and `thinking` +
  `reasoning_tokens` fields on `EvalCellResultPayload` / `study.result`.
  Only the last two depend on anything (P1's token parse); the display
  items can land any time, including before P2.

Each phase is independently shippable; P1 is worth shipping even if nothing
else ever lands.

## Open decisions

- **Where translation lives.** `pkg/llm` (next to the providers, visible to
  the factory path that already hit the 37.8s bug) vs `cmd/cortex`
  (`ModelSpec` lives there today). Leaning `pkg/llm`: the factory path needs
  it too, and `ModelSpec.TemplateKwargs` already leaks a llama.cpp-ism into
  the composition root.
- **Level→budget tiers for budget dialects.** Strawman: low=1k, medium=4k,
  high=16k thinking tokens. Needs one fleet probe to sanity-check against
  the local models' actual deliberation lengths.
- **Whether `fast`-role sub-calls (summarizer, shell-risk judge) should pin
  `off` at the call site** rather than relying on role policy — the 37.8s
  incident says yes; do it in P2 while the call sites are open.
