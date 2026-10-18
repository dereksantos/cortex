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
  the first task runs, so a killed run still shows up (as `start` with no
  `finish`). A run that ends early on an error still records its partial
  results (`finish` with a note) and syncs them.
- **Reproducible.** The commit under test is recorded, and a dirty tree's
  tracked changes and untracked files are saved as `source.patch` (a failure
  to capture it is recorded in `source.patch.err` and the run's index entry,
  never dropped).
- **Every runner uses it.** `TestEveryRunnerUsesTheStore` fails CI for any
  `scripts/bench/*/main.go` that doesn't open its run with `benchstore.Begin`
  and sync it, and any shell runner that picks its own output dir without
  `benchstore begin` / `finish`.

## Running

| Runner | What | Notes |
|---|---|---|
| `scripts/bench/swebench/run.sh` | SWE-bench Verified, scored by the official harness | `--backend litellm --endpoint <proxy>` for the local fleet; [README](../scripts/bench/swebench/README.md) |
| `scripts/bench/swebench/run_mini.sh` | mini-SWE-agent on the same instances (the reference harness) | give it a sibling run id to the cortex side |
| `scripts/bench/polyglot/run.sh` | Aider polyglot, Go slice | [README](../scripts/bench/polyglot/README.md) |
| `go run ./scripts/bench/classifier --model <id>` | The `bash` gate's gray-zone classifier on 33 hand-labeled commands (bench `shell-classifier`) | an unparseable reply keeps its raw text in the row; `--reps`, `--temp` |

Tag arms with `--label key=value` (repeatable) — `--label arm=decider`,
`--label prompt=2026-10-07` — so trend views can group runs without parsing
`run.json`.

## Result format v1

Every run directory carries two normalized files beside its runner's native
`run.json`/`results.jsonl`. Runners write them through `Run.Finish`;
`benchstore reindex` (re)writes them for runs copied in or rescored. The
native files keep everything a runner records; **the v1 files are the one
shape every summary, comparison, and trend view reads.** Fields beyond v1
ride in `extra`, verbatim. Schema: `internal/benchstore/schema.go`.

`run.v1.json` (`"schema": "cortex.bench.run/v1"`): `bench`, `run_id`,
`harness` (the agent under test — `cortex`, `mini-swe-agent`, …),
`harness_version`, `started_at`/`ended_at`, `models` (role → id), `backend`,
`endpoint`, `window`, `temperature`, `cortex{commit,dirty}`,
`dataset{name,revision,split}` (results compare only within one pin),
`tasks`, `labels`, `host{name,os,arch}`, `extra`.

`results.v1.jsonl`, one row per task (`"schema": "cortex.bench.result/v1"`):
`task_id`, `status`, `failure_class`, `failure_detail` (the runner's own
class), `tokens_in`, `tokens_out`, `cost_usd`, `wall_ms`, `tool_calls`,
`turns`, `extra`.

**Status:** `pass` · `fail` — anything the agent caused, including leaving
no change at all · `error` — the harness or environment failed, not the
agent. Pass rate = pass / (pass + fail); errors are counted and shown, never
silently dropped.

**Failure classes** (shared across benchmarks):

| class | meaning | polyglot | swebench |
|---|---|---|---|
| `chat_mode` | answered in prose: no tool calls, no change | `chat_mode` | `chat_mode` |
| `no_change` | worked with tools but left no change on disk | `early_finalize` | `empty_patch` |
| `wrong_change` | changed the code; the tests still fail (or the diff doesn't apply) | `wrong_code` | `wrong_patch`, `patch_apply_failed` |
| `timeout` | hit the wall-clock limit | `timeout` | `timeout` |
| `budget_stop` | hit the spend limit | — | `budget_stop` |
| `agent_error` | the agent process errored | `error` | `error` |
| `infra_error` | harness/environment failure (status `error`) | — | `env_error`, `eval_error`, unscored prediction |

A new runner maps its classes in `nativeClass`; an unmapped class is kept
as-is rather than dropped.

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

Native rows are normalized to v1 on finish: a row passes on `"pass": true`
or `"resolved": true`; `failure_class` maps through the shared taxonomy
above; `tokens_in`/`tokens_out`/`cost_usd`/`wall_ms`/`tool_calls`/
`agent_turns` carry over when present.

## Off-machine copy (`benchstore sync`)

Every runner ends with a sync (`SyncBestEffort`), and
`go run ./scripts/bench/benchstore sync` runs one by hand:

1. **Local history.** The store is its own git repo (created on first sync).
   Its `.gitignore` tracks only the records — `index.jsonl`, `run.json`,
   `results.jsonl`, `*.v1.json(l)`, `source.patch`, `predictions.jsonl`,
   `usage.jsonl`, the upload ledger, and `archive/` — and each sync commits
   them before uploading anything, so a failed upload never leaves the local
   history behind. It has no remote; nothing is pushed. This git repo is the
   one `.git` the store's own durability check accepts.
2. **R2 bundles.** Each unit — every top-level entry of `runs/<bench>/` and
   of `archive/` — is packed as a `.tar.gz` named by its content hash
   (`runs/swebench/pair-20261008/<sha256[:16]>.tar.gz`) and uploaded with
   `wrangler r2 object put --remote`. A changed unit gets a new key, so an
   upload never overwrites an earlier one. `.sync/r2.jsonl` is the ledger of
   what is already off-machine; `index.jsonl` is also kept at the bucket root.
   One failed unit is reported and the rest still upload. Syncs take a lock,
   so two runners finishing together don't collide.
3. **No secrets off-machine.** A unit holding something shaped like a
   provider API key (`sk-or-v1-`, `sk-ant-api`, `sk-proj-`, `ghp_`,
   `github_pat_`) is not uploaded; the sync names the file. Scrub it, then
   sync again.

**Known limits.** A unit that changes after upload (a rescore, a run still
writing) is uploaded whole again under a new key, and nothing prunes
superseded bundles, so the bucket only grows. `wrangler r2 object put` caps
one object at about 315 MB; a larger unit fails to upload (named in the sync
error) until bundles are split or uploaded multipart.

The bucket is local configuration, not repo content: `.sync/config.json` in
the store (`{"r2_bucket": "<name>"}`), or `CORTEX_BENCH_R2_BUCKET` (`off`
disables the upload; the git commit still happens). A sync failure never
fails a run — the local record already exists — but it prints a warning with
the retry command.

Restore a run: `wrangler r2 object get <bucket>/<key> --file run.tgz --remote
&& tar -xzf run.tgz -C "$CORTEX_BENCH_HOME"`.

## Trend dashboard

`go run ./scripts/bench/benchstore export --out <dir>` writes the three
tables the trend dashboard reads, from the v1 files: `runs.csv` (one row per
logical run — benchmark, harness, arm, model, commit, kind, counts, pass
rate, labels), `failures.csv` (run × failure class), and `tasks.csv` (one
row per task attempt, with its run's arm and model). A logical run is one
**arm** of a **batch** (the run id's first segment): a runner that stores one
run per instance (`<batch>/cortex-<instance>`, `<batch>/mini-<instance>/mini`)
is folded back into its arm, and the arm is the rest of the run id
(`ab-x/patch` → `patch`) or the harness when there is none. `kind` separates
measured runs from probes/smoke runs and aborted ones by run name. Paths and
host names are left out of the export.

The dashboard (https://claude.ai/artifact/ARmnggXtMTif4zDFS3TPC1) derives
everything from those three tables — comparisons are every batch with two or
more arms, runs labeled `invalid=…` are left out — so new batches, models and
A/B arms appear without touching the page. To refresh it:

1. `./scripts/bench/dashboard-export.sh [--pull <host>]…` — copies runs other
   hosts recorded (e.g. `--pull chatterbox`; rsync `--update`, never
   deletes), reindexes, syncs, and exports to `$CORTEX_BENCH_HOME/exports/`.
2. Upload the three CSVs to the dashboard and point its `runs`, `tasks` and
   `failures` datasets at the new files (Claude does this: three asset
   uploads and one store write; the page itself is not republished).
