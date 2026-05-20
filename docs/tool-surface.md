# Tool Surface for the Cortex Harness

The cortex CLI is the entry point for every cortex-driven cognitive
turn. This doc names the six axes a tool call passes through and the
guardrail on each.

## The six axes

Every tool call passes through all six. A failure can be cleanly
attributed to exactly one.

| # | Axis | Cover (what exists / should exist) | Protect (failure mode → guardrail) |
|---|---|---|---|
| 1 | **Contract** — names, JSON schemas, descriptions, versions | One manifest (`tools.json`) generated from CLI flag-sets and `DescribeFlags`; description text good enough for selection | Schema drift → version field + CI diff (`manifest_test.go`) that fails when surface changes without a bump |
| 2 | **Authorization** — what the harness is allowed to invoke | Read tools (`search`, `search-vector`) vs. mutators (`capture`, `forget`, `journal rebuild`); per-project allowlist | Privilege escalation → default-deny mutators; `forget` / `rebuild` never auto-granted |
| 3 | **Dispatch + Execution** — running the work | Single `cortex <cmd>` entrypoint; deterministic exit codes; `--json` for structured stdout | Resource exhaustion → hard timeout per tool (Reflex target <20ms; full pipeline budgeted); kill on cap; no shell expansion of caller-supplied args |
| 4 | **Result** — what comes back | Stable envelope `{ok, data, error, meta:{trace_id, latency_ms}}`; machine-readable error codes | Silent truncation / leaks → explicit `truncated:true`; redact paths outside `.cortex/` + project root |
| 5 | **State / side-effects** — what changes on disk | Every mutator writes a journal entry; idempotency key for retries; `AssertLocalOnly` tripwire | Corruption / exfiltration → journal append-only; destructive ops require `--confirm`; `.gitignore` check at `init` |
| 6 | **Observability + Budget** — what we know after the call | Per-call latency, exit, tokens → SQLite **and** `.cortex/db/cell_results.jsonl`; rate-limit window per session | Invisible regressions / runaway spend → over-budget returns `error: budget_exceeded` rather than blocking; ABR eval surfaces drift |

## Exhaustiveness check

A broken call fails at exactly one of:

1. Wrong schema
2. Not allowed
3. Crashes or hangs
4. Unparseable output
5. Corrupts or leaks state
6. Invisible or over-budget

A seventh category cannot be constructed without being a composite of
the above.

## Mutual exclusivity check

Selection (which tool gets picked) lives upstream in the cortex turn
chain or the caller's prompt — it's enabled by **Contract (1)**, not a
separate axis. Telemetry of *what was chosen* is **Observability (6)**.

## Current gaps

- **(1) Contract** — `tools.json` is generated and CI-checked, but
  `DescribeFlags` is implemented on only a few commands (audit slice
  7). Until every flag-bearing command participates, the manifest
  under-describes the surface.
- **(4) Result** — Envelope isn't uniform across commands; `--json`
  coverage is partial. Search has the canonical shape; others trail.
- **(6) Observability** — `cell_results.jsonl` exists for eval cells
  and every CLI invocation now records a `TelemetryRow`, but parity
  between eval cells and ad-hoc CLI invocations isn't enforced.

## Next slice

Finishing `DescribeFlags` (audit F+7) is the smallest change that
brings (1) up to date. After that, broadening the JSON envelope to the
remaining commands closes (4) and unblocks uniform telemetry for (6).
