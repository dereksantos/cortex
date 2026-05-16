# `cortex` — interactive REPL

Bare `cortex` (no subcommand, no flags) drops you into an interactive
coding REPL rooted at the current working directory. Type a request,
the harness does work, the verifier runs, you see a one-line status,
you type the next thing.

Designed for small local models: the defaults target
`qwen2.5-coder:1.5b` via Ollama, with a tight system prompt + bounded
output tokens + auto-retry on build failures + a snapshot stack for
chained `/undo`. The same surface scales up — point `--model` at any
OpenRouter model and the same UX works.

## Quick start

```bash
# in any directory (Go projects light up the verifier; others run silent)
cortex

# pin a different default model for this terminal
CORTEX_REPL_MODEL=llama3.2:3b cortex

# one-shot model override
cortex --model anthropic/claude-haiku-4.5

# verbose telemetry (tool calls, tokens, capture events)
cortex --verbose
```

`cortex repl` is an explicit alias if you want the no-args invocation
to print usage instead.

## UX shape

```
$ cortex
cortex · /Users/derek/code/gol · qwen2.5-coder:1.5b · http://localhost:11434/v1/chat/completions · /help

~ scaffold main.go with package main and an empty main()
  ✓ turn 1 · files: main.go · verify: go build ok · tokens: 412/87 · 1230ms

~ add a Grid struct wrapping [][]bool with a New(w, h int) constructor
  ✓ turn 2 · files: main.go · verify: go build ok · tokens: 510/142 · 1680ms

~ add Step() that applies the B3/S23 rule
  verify failed (go build), auto-retrying once with error context...
  ✓ turn 3 · files: main.go · verify: go build ok · tokens: 894/231 · 3140ms

~ /diff
  changes since pre-last-turn snapshot (turn 3):
    ~ main.go

~ /undo
  undone turn 3 (2 more available)

~ /quit
session saved → /Users/derek/code/gol/.cortex/sessions/20260516T210145Z/session.jsonl
```

## What each turn does

For every non-slash input, the REPL runs a 3-round verify loop:

1. **Snapshot** — copy every file ≤1 MiB under workdir (skipping
   `.git`, `.cortex`, `node_modules`, `vendor`) into
   `.cortex/sessions/<ts>/snapshots/turn-<n>/`. This is what `/undo`
   restores from and what `/diff` compares against.
2. **Attempt 1** — compose the system prompt + your request, call the
   in-process Cortex harness (same one `cortex code` uses), apply the
   resulting file edits, run the verifier.
3. **Attempt 2 (auto)** — on verify-fail, re-run the harness with the
   verifier output appended to the prompt as "PREVIOUS ATTEMPT FAILED."
   Bounded to one auto-retry; the model gets exactly one shot at
   self-correction.
4. **User gate** — if verify still fails, prompt `[r]etry / [e]dit / [s]kip / [q]uit`:
   - `r`: ask for an optional hint, run the harness again with verifier
     output + hint. Up to 5 user-driven retries (configurable in code).
   - `e`: pause for manual file edits. Edit in another terminal/editor,
     press enter to re-verify.
   - `s`: discard this turn, roll back to the snapshot.
   - `q`: discard and exit the REPL.
5. **Accept** — on verify-pass (or on `no-verify` workdirs), push the
   snapshot onto the undo stack, log a structured row to
   `.cortex/sessions/<ts>/session.jsonl`, and fire a background
   `cortex capture` event so the change becomes searchable next
   session.

## Slash commands

| Command | Effect |
|---|---|
| `/help`, `/?` | List slash commands |
| `/diff` | List files that differ between the most recent pre-turn snapshot and current state. Shows `+`/`~`/`-` for added/changed/removed. |
| `/undo` | Restore workdir to the pre-most-recent-accepted-turn snapshot. Chained — repeat `/undo` walks back through every accepted turn this session. |
| `/model [<id>]` | With no arg: show current model + API URL. With arg: swap model for subsequent turns. Slash in name → OpenRouter; no slash → Ollama. |
| `/quit`, `/exit` | Exit. `Ctrl-D` does the same. |

## Defaults

| | |
|---|---|
| Workdir | Current directory (`os.Getwd`) |
| Model | `qwen2.5-coder:1.5b` (override via `--model` or `CORTEX_REPL_MODEL`) |
| API URL | Ollama (`http://localhost:11434/v1/chat/completions`) when the model id has no `/`; OpenRouter otherwise |
| Verifier | `go build ./...` if a `go.mod` exists at workdir root; otherwise none (warned once per session) |
| Per-turn max output | 4000 tokens |
| Per-turn max agent turns | 8 |
| Per-turn snapshot ceiling | 1 MiB per file (larger files are skipped) |
| Auto-retry on verify-fail | 1 round |
| User-driven retry cap | 5 rounds per turn |
| Session directory | `.cortex/sessions/<ISO-UTC-timestamp>/` |
| System prompt | `.cortex/repl-system-prompt.md` (seeded on first run) |

## What lands in `.cortex/` after a session

```
.cortex/
├── repl-system-prompt.md            # seeded on first run; edit to tune
├── sessions/
│   └── 20260516T210145Z/
│       ├── session.jsonl            # one row per turn (see schema below)
│       └── snapshots/
│           ├── turn-001/
│           │   ├── .manifest.json   # path → sha256 for /diff
│           │   ├── main.go
│           │   └── ...
│           ├── turn-002/...
│           └── turn-003/...
└── journal/
    └── capture/                     # one capture event per accepted turn
        └── 20260516-000000-<hash>.jsonl
```

`session.jsonl` is the cross-session-debuggable transcript. Each row
is a single JSON object with the structure:

```json
{
  "turn": 2,
  "session_id": "20260516T210145Z",
  "timestamp": "2026-05-16T21:03:11Z",
  "user_message": "add a Grid struct...",
  "model": "qwen2.5-coder:1.5b",
  "api_url": "http://localhost:11434/v1/chat/completions",
  "system_prompt": "You are pair-programming...",
  "snapshot_dir": "/path/.cortex/sessions/.../snapshots/turn-002",
  "agent_turns": 3,
  "tokens_in": 510,
  "tokens_out": 142,
  "cost_usd": 0,
  "latency_ms": 1680,
  "files_changed": ["main.go"],
  "final_text": "added Grid type with constructor",
  "verify_kind": "go build",
  "verify_ok": true,
  "accepted": true
}
```

Three rounds of verify can stamp the same row: initial (`verify_*`),
auto-retry (`retry_verify_*`), user-driven (`user_retry_verify_*`).
The `accepted` field reflects the final disposition.

## Tuning the system prompt

`.cortex/repl-system-prompt.md` is the file the harness sends as the
agent's standing instructions every turn. The default is tuned for
1.5B models:

- explicit single-step framing ("ONE focused change per request")
- prohibition on adjacent refactoring
- enumeration of available tools
- explicit "stop when done" guidance (small models tend to keep working
  past the ask)
- explicit "ask one clarifying question and stop" escape hatch
- explicit "fix and re-run on build/test failure" rule

Edit it freely — changes take effect on the next turn. If you delete
it, the default is re-seeded on next session start.

When tuning for a larger model, you can usually shorten the prompt
substantially: bigger models infer single-step etiquette from context.
When tuning for an even smaller model (Phi-2, qwen2.5-coder:0.5b),
make the prompt MORE explicit — name the file to edit, name the
function to define, give an example.

## Tradeoffs and design notes

### Per-turn snapshots (instead of git)

The REPL doesn't require a git repository. Every accepted turn
snapshots files into `.cortex/sessions/<ts>/snapshots/turn-<n>/`.
Pros: works in any directory; `/undo` is unambiguous; no impact on
your existing git workflow. Cons: O(workdir-size) per turn; large
repos (>1 GiB) will groan. For projects above ~100 MiB consider
switching to a git-stash-based mode (not yet implemented).

### Background capture, not auto-commit

Each accepted turn fires a `capture.event` to
`.cortex/journal/capture/`. These are durable, searchable context that
later REPL sessions can pull up via the agent's `cortex_search` tool —
the "Cortex makes the next session smarter" loop in microcosm.

The REPL deliberately does **not** `git add` or `git commit` —
your git history is yours to curate.

### Workdir is cwd, no `--workdir` flag

`cortex code` requires `--workdir` for safety: a typo could overwrite
the wrong project. The REPL relaxes this: the whole UX is "you're
interactively typing prompts about *this directory*," so cwd is the
intended target. If you want a fresh tempdir, `mkdir` first.

### Single auto-retry, not bounded

The model gets exactly one automatic retry on verify-fail. Two reasons:
(1) small models that miss twice usually need user nudging, not more
context, (2) every retry doubles cost. The user-driven `[r]` path is
the right tool for "the model just needs one more hint."

### Ollama vs. OpenRouter routing

Model id with `/` → OpenRouter (e.g. `anthropic/claude-haiku-4.5`).
Model id without `/` → Ollama at `http://localhost:11434/v1/chat/completions`.
The check is purely lexical — no probe, no network call at startup.
If Ollama isn't running, the first harness call will time out and
the turn surfaces the error.

## Known v1 limitations

| What | Why | Workaround |
|---|---|---|
| Auto-retry retry-count is 1, not configurable | Defaults are conservative for 1.5B economics | Edit `defaultMaxTurns` / retry constant in `cmd/cortex/commands/repl.go` |
| Snapshot strategy is full-walk per turn | Simple, correct, no git dep | OK for projects ≤100 MiB |
| Verifier is Go-only | Most common case for now | Manual `[e]` gate + `go build` |
| `OPEN_ROUTER_API_KEY` is auto-stubbed when Ollama-routed | Harness constructor mandates a key | Will fix with `NewCortexHarnessLocal()` constructor |
| Windows is unsupported (`//go:build !windows`) | Inherited from the harness file | Same as `cortex code` |

## Eval-principles compliance

The REPL is part of cortex's user-facing CLI, not an internal-only
surface. It satisfies:

- **Principle 1 (black box):** the REPL invokes the same harness
  surface (`evalv2.CortexHarness.RunSessionWithResult`) that
  `cortex code` exposes; benchmarks would shell out to `cortex` and
  drive it through stdin (v2 work — not yet wrapped).
- **Principle 2 (no coaching):** the seeded system prompt is *framing*
  (declares the task), not *coaching* (it doesn't hand-feed
  `cortex_search` queries). Edits are visible in
  `.cortex/repl-system-prompt.md` — what the user sees is what gets
  sent.
- **Principle 6 (isolation):** the REPL's session state lives entirely
  under `<workdir>/.cortex/`. Cross-project contamination is
  impossible — there's no implicit fall-through to `~/.cortex/`.
- **Principle 7 (structured outputs):** `session.jsonl` is the
  structured record; every turn carries model id, API URL, token
  counts, verifier kind + result, gate decisions. Analysis pipelines
  can chart this without parsing prose.

Principles 3 (versioned metadata), 5 (reproducibility seeds), and 8
(LLM-judge variance) apply when the REPL is wrapped as a benchmark
instance — v2 work, deferred.
