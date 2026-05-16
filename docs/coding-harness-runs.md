# Cortex Coding Harness — Journal

Empirical log of runs against the `cortex code` / `cortex eval --harness cortex`
pipeline. Each entry captures what was run, the headline numbers, and what
was learned. Append-only.

Workdirs referenced live in `/var/folders/.../cortex-code-*` (cleaned up by
macOS eventually) — the SQLite + JSONL in this repo's `.cortex/db/` and the
per-workdir `.cortex/journal/coding/<runID>.jsonl` are the durable artifacts.

---

## 2026-05-15 — harness bring-up

Branch: `tmp-coding-harness` (worktree at `../cortex-harness`)
Commit: `fbd34bb` (initial)

Brought Cortex's own agent loop online: 5 tools (`read_file`, `write_file`,
`list_dir`, `run_shell`, `cortex_search`), workdir-rooted sandboxing, per-eval
`.cortex/` store, OpenRouter tool-call extension. Two entry points: `cortex
eval --harness cortex` (scenario-driven, persists CellResults) and `cortex
code` (ad-hoc interactive, live progress stream).

### Conway's Game of Life

Smoke runs against the `conways-game-of-life-single.yaml` scenario. Build,
run the binary against `blinker.in` and `glider.in`, diff against golden
4-generation `.out` files, optionally invoke a Haiku judge on a 10×10
freeform pattern.

| Model                         | Result    | Turns | Cost   | Latency | Frames |
| ----------------------------- | --------- | ----- | ------ | ------- | ------ |
| anthropic/claude-haiku-4.5    | PASS      | 14    | $0.082 | 35s     | 2/2    |
| anthropic/claude-haiku-4.5 +J | frames OK | 13    | $0.084 | 39s     | 2/2    |
| openai/gpt-oss-20b:free       | PASS      | 9     | $0.00  | 96s     | 2/2    |

(+J = judge enabled. Haiku's frame fixtures passed but the Haiku judge
mistakenly marked the freeform run as FAIL — verifying a 10×10 grid's
neighbor counts is at the edge of small-judge reliability.)

Persistence verified end-to-end: rows in `.cortex/db/evals_v2.db`,
matching JSONL appends in `.cortex/db/cell_results.jsonl`, journal entries
in `.cortex/journal/eval/0001.jsonl`. No writes to the user's
`~/.cortex/` (the store-isolation invariant held).

---

## 2026-05-15 — `cortex code` ad-hoc command

Added `cortex code "<prompt>" --workdir <dir> --model <m>` so the harness
can drive interactive coding sessions without a scenario file. Live
progress stream via a `Notify` callback on the Loop. Required `--workdir`
(no default-to-cwd footgun) with `--init` to create a fresh tempdir.

Quick smoke test (Haiku, "write a Go program that prints 'hello cortex'
and run it"): 3 turns, $0.006, 5.5s. Wrote `main.go`, ran `go run`,
declared done. End-to-end clean.

---

## 2026-05-15 — LRU cache, first take

Prompt: implement a thread-safe generic LRU cache in Go with TTL and
table-driven tests covering hit/miss, eviction order, update-existing-key,
TTL expiry, Len-after-eviction.

| Model                      | Outcome       | Turns | Cost   | Latency | Tests        |
| -------------------------- | ------------- | ----- | ------ | ------- | ------------ |
| anthropic/claude-haiku-4.5 | budget-capped | 11    | $0.10  | 61s     | 9 funcs PASS |

Tests passed when I ran `go test ./...` manually, but the loop hit the
60K cumulative-token cap before the model could declare done.
Disovered two real bugs:

1. The model called `run_shell({"command": "go test", "args": ["-v"]})` —
   my allowlist rejected because `"go test"` isn't a known command.
2. The token cap (60K) was set assuming brief responses; a real coding
   task with a code file + test file blows past it in fewer turns than
   expected.

Also noted: an unsolicited `test` entry in the allowlist (for `/usr/bin/test`
string-conditionals) confused the model into thinking it could shortcut
`go test`.

### Fixes (still under `fbd34bb`'s feat scope)

- `run_shell` auto-splits whitespace-separated command strings (so the
  model can write either `command="go test", args=[…]` *or*
  `command="go", args=["test", …]`).
- Removed `test` from the allowlist.
- Renamed `Budget.MaxTokens` → `Budget.MaxCumulativeTokens`, default
  bumped to 300K.
- Added a per-turn output cap derived from the model id
  (`harness.ModelMaxOutputTokens`): Anthropic=16K, Qwen Coder=8K,
  gpt-oss=4K, Gemini=8K, fallback 4096. Override via `--max-output`.
- Surfaced provider errors via a new `coding.error` transcript event
  and `cortex code` notifier.

---

## 2026-05-15 — LRU cache, model comparison

Re-ran the same LRU prompt against six models post-cleanup. All workdirs
fresh, all `.cortex/` stores empty (so `injected_context_tokens=0` for
every run — Cortex's context layer was not exercised; this is a pure
model-quality comparison).

| Model                                | Outcome      | Turns | Tokens in/out | Cost     | Latency | Tests passing |
| ------------------------------------ | ------------ | ----- | ------------- | -------- | ------- | ------------- |
| anthropic/claude-haiku-4.5 (rerun)   | cost-cap     | 11    | 150K / 11K    | $0.2035  | 66s     | yes (9 funcs) |
| qwen/qwen3-coder (480B base)         | `model_done` | 10    | 40K / 5K      | $0.0177  | 39s     | yes (3+ funcs) + README |
| **mistralai/devstral-small (24B)**   | **`model_done`** | **7**  | **19K / 3K**  | **$0.0015** | **15.7s** | **yes (1 func, subtests)** |
| qwen/qwen3-coder-30b-a3b-instruct    | `model_done` | 9     | 41K / 4K      | $0.0039  | 37s     | yes (3 funcs) |
| qwen/qwen3-coder:free                | 429 turn 0   | 0     | —             | —        | —       | —             |
| openai/gpt-oss-20b:free              | 429 / format | 0-11  | —             | —        | —       | uncompilable  |
| mistralai/codestral-2501             | 404 (id gone)| —     | —             | —        | —       | —             |
| google/gemma-2-9b-it:free            | 404 (id gone)| —     | —             | —        | —       | —             |

### Headline

**`mistralai/devstral-small` won every axis** that matters for a small
coding task:

- 133× cheaper than Haiku ($0.0015 vs $0.20)
- 4.2× faster than Haiku (15.7s vs 66s)
- 12× cheaper than full Qwen 3 Coder, 2.6× cheaper than the Qwen 3 30B-3B MoE
- All tests pass; clean `model_done` exit (not budget-capped)

Devstral is Mistral's agent-trained 24B model, explicitly tuned for the
read–edit–run–iterate tool loop. Trade-off: fewer test functions (one
`TestLRUCache` with internal subtests vs Qwen 3 Coder's 3+ flat
functions), but all required cases are present and pass.

This is the small-model-amplifier thesis showing real signal *before*
the salience layer has done anything — `cortex_search` returned the
"empty" sentinel on every run because the store had no prior captures.
The question now is how much *additional* lift a populated store
provides.

### gpt-oss-20b notes

`openai/gpt-oss-20b:free` deserves a footnote: its chat-template tokens
leak into the tool-name field (e.g. `write_file<|channel|>commentary`).
I added `normalizeToolName` to the dispatcher to strip `<|...|>`
suffixes, which resolved the dispatch but did not save the run — the
free pool throttles requests aggressively enough that any multi-turn
session hits 429. Worth re-trying on a self-hosted vLLM endpoint
or off-peak.

### Free-tier reality check

Four of eight model ids I picked from memory either no longer exist on
OpenRouter or live in a free pool that 429s instantly. The catalog
shifts faster than my mental model; always query
`https://openrouter.ai/api/v1/models` first.

---

## Open questions / TODO

- **Real ABR experiment.** Seed `<workdir>/.cortex/` with prior decisions
  ("use `sync.RWMutex` not `sync.Mutex`"; "table-driven tests required";
  "avoid `container/list` — write a manual doubly-linked list") and
  re-run Devstral + Haiku. Compare `injected_context_tokens`, turns,
  output shape with and without the seed.
- **Harder task.** LRU is now too easy for every model we tested (modulo
  the free-tier throttling). Try a bounded-buffer channel with deadlock
  detection, or a small interpreter, where Haiku might fail and Devstral
  would need help.
- **Edit a real codebase.** Point `cortex code -w /path/to/cortex-harness`
  at an actual task on this repo (e.g. "add unit tests for
  `internal/harness/tools.go`") and see whether the harness can self-host
  its own development.
- **Commit pending fixes.** Tool-name normalization, error notifier,
  budget rename, and model-caps table are on top of `fbd34bb` but
  uncommitted as of this entry.
