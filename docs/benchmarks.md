# Benchmarks and evals — the durable store

> **Rule: every benchmark, eval, or probe result is written to the durable
> store, and nowhere else.** Not a gitignored dir in the repo, not an agent
> worktree, not a scratchpad, not `/tmp`. Months of results were lost that
> way — runs that lived in `.cortex/bench/` of a worktree that was later
> removed, an eval database in a deleted framework, receipts that only
> existed in a session's scratchpad. This doc is the authority; CLAUDE.md
> and AGENTS.md point here.

## Where results live

Root: `$CORTEX_BENCH_HOME`, default `~/.cortex/benchmarks` — outside every
git working tree, so no `git clean`, worktree removal, or fresh clone can
touch it.

```
~/.cortex/benchmarks/
  index.jsonl                  append-only start/finish events, one line each —
                               the time series every trend view reads
  runs/<bench>/<run-id>/       one directory per run (the source of truth):
    run.json                   what ran: model, endpoint, window, cortex commit,
                               dirty flag, dataset/corpus pin, host, timeouts
    results.jsonl              one row per task, appended + fsynced as each finishes
    source.patch               `git diff HEAD` when the tree under test was dirty
    …                          the runner's evidence (transcripts, logs, trajs, eval/)
  archive/                     older formats kept raw (not indexed): the
                               2026-05 evals_v2.db, eval-loop logs, eval docs
                               recovered from git history
```

`index.jsonl` is a projection of `runs/` (the journal's CQRS shape):
`benchstore reindex` re-derives entries for any run on disk the index has
not seen finish, so losing the index loses nothing — losing `runs/` does.

## Guarantees (enforced in `internal/benchstore`)

- **Never overwritten.** A run directory is created exclusively; reusing a
  run id is an error, not a truncation.
- **Never inside a repo.** `Begin` refuses a run directory under any git
  working tree (`.git` dir or a linked worktree's `.git` file). `--ephemeral`
  opts out, for throwaway smoke checks whose results don't matter.
- **On record from the first second.** A `start` event is appended before
  the first task runs, so a crashed or killed run still shows up (as
  `start` with no `finish`).
- **Reproducible.** The commit under test is recorded, and a dirty tree's
  diff is saved as `source.patch`.
- **Every runner uses it.** `TestEveryRunnerUsesTheStore` fails CI for any
  `scripts/bench/*/main.go` that doesn't open its run with `benchstore.Begin`.

## Running

| Runner | What | Notes |
|---|---|---|
| `scripts/bench/swebench/run.sh` | SWE-bench Verified, scored by the official harness | `--backend litellm --endpoint <proxy>` for the local fleet; [README](../scripts/bench/swebench/README.md) |
| `scripts/bench/swebench/run_mini.sh` | mini-SWE-agent on the same instances (the reference harness) | give it a sibling run id to the cortex side |
| `scripts/bench/polyglot/run.sh` | Aider polyglot, Go slice | [README](../scripts/bench/polyglot/README.md) |

Tag arms with `--label key=value` (repeatable) — `--label arm=decider`,
`--label prompt=2026-10-07` — so trend views can group runs without parsing
`run.json`.

## Reading

```bash
go run ./scripts/bench/benchstore list [--bench swebench] [--json]   # one line per run, oldest first
go run ./scripts/bench/benchstore csv  [--bench swebench] > runs.csv # for graphing
go run ./scripts/bench/benchstore reindex                            # pick up runs copied in by hand
```

## Ad-hoc probes and shell runners

A one-off probe is still a result. Open it in the store, write there, record
the finish:

```bash
dir=$(go run ./scripts/bench/benchstore begin --bench probe --run-id decider-shadow-01 --label arm=shadow)
# … write run.json + results.jsonl ({"pass":true|false, …} or {"resolved":…}) into "$dir" …
go run ./scripts/bench/benchstore finish --bench probe --run-id decider-shadow-01
```

`results.jsonl` rows are summarized benchmark-neutrally: a row passes on
`"pass": true` or `"resolved": true`, is skipped when `eval_status` starts
with `skipped` or `error`, and `tokens_in`/`tokens_out`/`cost_usd`/`wall_ms`/
`failure_class` are rolled up when present.

## Not yet

- **Off-machine copy.** The store is one disk. It is plain files (JSONL +
  evidence dirs) so it can be a git repo with a private remote, or synced;
  that choice is open.
- **Trend view.** `benchstore csv` is the data feed; a dashboard over
  `index.jsonl` is the next step.
