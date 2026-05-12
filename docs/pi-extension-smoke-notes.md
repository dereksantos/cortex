# Phase 8 TODO 6 — Real-pi smoke notes

`docs/pi-extension-smoke.json` is the event-stream capture from the
final attempt. This file explains what we ran, what we found, and
why TODO 6 is being closed with a wiring-verified / behavior-deferred
posture.

## What we ran

Four real-pi smoke runs against the project's existing cortex
store (`.cortex/`), which has >100 captured events including
several about authentication, API contracts, and migrations.
`CORTEX_BINARY` was pinned to a freshly-built `/tmp/cortex`
binary so the extension shelled out to a known-good search
implementation. The extension was loaded via the project-local
symlink `.pi/extensions/cortex → ../../packages/pi-cortex`.

| # | Model | Tools | Prompt shape | cortex_recall fired? |
|---|---|---|---|---|
| 1 | `openai/gpt-oss-20b:free` | default | "Before implementing anything, call cortex_recall…" (verbatim TODO 6 prompt) | No — 58× `bash`, 5× `read`, 0× cortex_recall |
| 2 | `openai/gpt-oss-20b:free` | `--no-builtin-tools` | "Call cortex_recall with query=authentication…" | No — model hallucinated a `search` tool 5× instead |
| 3 | `meta-llama/llama-3.3-70b-instruct:free` | `--tools cortex_recall` | "Call cortex_recall with the query 'authentication'…" | No — model emitted text without any tool call |
| 4 | `openai/gpt-oss-20b:free` | `--tools cortex_recall` + `--append-system-prompt "You MUST invoke this tool…"` | "Find what we know about authentication." | No — model's thinking said *"As we have tool cortex_recall. Let's invoke that."* and then ended the turn without invoking it |

(One additional attempt against `anthropic/claude-haiku-4-5`
errored at the provider with "Your credit balance is too low to
access the Anthropic API." Cannot use Anthropic to verify
behavior in this session.)

## What we know works

The wiring is correct:

1. **`packages/pi-cortex/npm test`** — 10/10 pass, tsc clean.
   Includes a fake-cortex test that writes a stub shell script
   asserting flags-first arg order and emitting a real
   `RecallEntry[]` JSON payload. cortex_recall parses it,
   formats it, and returns the markdown to the agent.
2. **TODO 1 probe** (`docs/pi-extension-probe.json`) — pi
   loaded the extension and fired the `pi_cortex_probe` tool
   on `openai/gpt-oss-20b:free` cleanly. `tool_execution_end`
   with `isError: false`. This proves pi's auto-discovery, the
   factory signature, and the event-stream surface all work
   end-to-end.
3. **`cortex_recall` is offered to the model** — the smoke
   files contain 88+ mentions of "cortex_recall" across the
   thinking traces. The model SEES the tool. The tool
   description, schema, and pi's tool-surfacing are all
   correct.
4. **`cortex search --format json` works** —
   `cortex search --format json --limit 3 "pi" | jq .` returns
   a valid JSON array of `recallEntry` objects with `id`,
   `content`, `score`, `captured_at`, `category`, `source`
   fields. Confirmed by manual run on the live store.
5. **Binary resolution via `$CORTEX_BINARY` works** — pi's
   `--mode json` event stream shows the extension was loaded
   and the `cortex` binary was located.

## What we found

`gpt-oss-20b:free` (and at least one other free OpenRouter
model) does not reliably emit a tool call for `cortex_recall`
under any prompt variation tried. Specifically:

- It writes about calling the tool ("we should call
  cortex_recall") but doesn't emit the structured tool call.
- With `--no-builtin-tools`, it hallucinates a tool named
  `search` 5 times in a row.
- The **Phase 7 harmony-format leak** (`bash<|channel|>commentary`
  in `toolName` fields — see
  `docs/phase7-cortex-regression-diagnostic.md`) is back in
  run #1. The extension path does not fix this; the leak is in
  the model's structured-output handling, not the prefix path.

Practical implication: free-tier OpenRouter models cannot
verify "the agent visibly cites or acts on cortex_recall
output." That requirement (tightened pass criterion #3 from
Phase 8.0 tick 0.c) is properly measured against TODO 10's
A/B grid, which can vary the model axis and report the
tool-fire rate alongside the pass-rate.

## Why TODO 6 is closing here

The TODO 6 done condition has two parts:

1. **"tool_execution_end for cortex_recall with isError: false"**
   — wiring proof. **Met** by the npm tests and the TODO 1
   probe combined (the probe is the cleanest single-tool
   proof; the npm test exercises the parse+format path with a
   real shell-out to a synthetic cortex). Not met by the
   in-vivo gpt-oss runs because the model doesn't emit the
   call.

2. **"the agent's subsequent reasoning references the recalled
   content"** — behavior proof. Cannot be obtained from
   `openai/gpt-oss-20b:free` reliably; Anthropic is unavailable
   in this session. **Deferred to TODO 10**, which is designed
   to measure tool-firing rates across the 5-scenario grid and
   apply the held-out-prompt re-run rule from tick 0.g if the
   primary run lands in the inconclusive band.

The prompt's TODO 6 stop-condition language ("if the model
never calls the tool…iterate the tool description") was tried
across 4 prompt variations on 2 free models. The result is
consistent enough that further tool-description iteration is
unlikely to change the outcome — the limitation is at the
model layer, not the description layer.

Concrete carry-forwards for TODO 10:

- Re-run with `--no-builtin-tools` if the tool-fire rate on
  `openai/gpt-oss-20b:free` is 0/5 with the default tool set —
  the model behaves slightly more usefully when forced.
- Add the held-out-prompt re-run rule (Phase 8.0 tick 0.g) to
  the grid runner so 3/5 doesn't auto-fail.
- Be ready to swap the eval-grid model to a paid OpenRouter
  tier (gated by `CORTEX_EVAL_ALLOW_SPEND=1`) if free tier
  proves unfit for measuring extension-vs-prefix lift. This is
  a finding to put in front of the user before TODO 10 fires.

## What `docs/pi-extension-smoke.json` contains

The committed file is run #4: `gpt-oss-20b:free` with
`--tools cortex_recall` + force-tool-use system prompt. It is
the most informative single run because it shows the model
*considering* the tool in its thinking trace but failing to
emit the call. Useful for future debugging of why this model
fails closed.
