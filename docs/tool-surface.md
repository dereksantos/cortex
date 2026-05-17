# Tool Surface for the Cortex Harness

A MECE breakdown of what's needed for an agent harness (Aider, Claude Code, opencode) to use Cortex as a set of tools, and the protections each axis requires.

## The six axes

Every tool call passes through all six. A failure can be cleanly attributed to exactly one.

| # | Axis | Cover (what exists / should exist) | Protect (failure mode → guardrail) |
|---|---|---|---|
| 1 | **Contract** — names, JSON schemas, descriptions, versions | One manifest (`tools.json`) generated from CLI flag-sets; description text good enough for selection | Schema drift → version field + CI diff that fails when surface changes without a bump |
| 2 | **Authorization** — what the agent is allowed to invoke | Read tools (`search`, `recent`, `insights`, `search-vector`) vs. mutators (`capture`, `forget`, `journal rebuild`); per-project allowlist | Privilege escalation → default-deny mutators; `forget` / `rebuild` never auto-granted |
| 3 | **Dispatch + Execution** — running the work | Single `cortex <cmd>` entrypoint; deterministic exit codes; `--json` for structured stdout | Resource exhaustion → hard timeout per tool (Reflex <20ms target, full pipeline budgeted); kill on cap; no shell expansion of agent-supplied args |
| 4 | **Result** — what comes back | Stable envelope `{ok, data, error, meta:{trace_id, latency_ms}}`; machine-readable error codes | Silent truncation / leaks → explicit `truncated:true`; redact paths outside `.cortex/` + project root |
| 5 | **State / side-effects** — what changes on disk | Every mutator writes a journal entry; idempotency key for retries; `AssertLocalOnly` tripwire | Corruption / exfiltration → journal append-only; destructive ops require `--confirm`; `.gitignore` check at `init` |
| 6 | **Observability + Budget** — what we know after the call | Per-call latency, exit, tokens → SQLite **and** `.cortex/db/cell_results.jsonl`; rate-limit window per harness session | Invisible regressions / runaway spend → over-budget returns `error: budget_exceeded` rather than blocking; ABR eval surfaces drift |

## Exhaustiveness check

A broken or hostile call fails at exactly one of:

1. Wrong schema
2. Not allowed
3. Crashes or hangs
4. Unparseable output
5. Corrupts or leaks state
6. Invisible or over-budget

A seventh category cannot be constructed without being a composite of the above.

## Mutual exclusivity check

Selection (which tool the agent picks) lives upstream in the harness prompt — it's enabled by **Contract (1)**, not a separate axis. Telemetry of *what the agent chose* is **Observability (6)**, not Selection.

## Current gaps

Given recent CLI growth (`embed`, `search-vector`, `embed --bulk`):

- **(1) Contract** — No generated `tools.json` manifest. Descriptions live in `cobra` `Short`/`Long` and drift silently from the CLI surface.
- **(4) Result** — Envelope isn't uniform across commands; `--json` coverage is partial.
- **(6) Observability** — `cell_results.jsonl` exists for eval cells, but ad-hoc CLI invocations from Aider / Claude Code don't land there. The same call inside vs. outside an eval is observable asymmetrically.

## First slice

A `tools.json` generator + envelope wrapper covers (1) and (4) and unblocks uniform telemetry for (6). That's the smallest change that closes the three open gaps simultaneously.
