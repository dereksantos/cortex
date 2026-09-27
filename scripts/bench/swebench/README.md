# SWE-bench Verified runner

Runs the cortex coding agent on [SWE-bench Verified][verified] and scores
every patch with the **official SWE-bench evaluation harness**
(`python -m swebench.harness.run_evaluation`, pinned version). Nothing in
this runner judges a patch itself: `resolved` means the harness said so.

It is modeled on [`../polyglot`](../polyglot/README.md): a pinned per-run
config, an isolated `CORTEX_HOME`, headless `cortex turn --json`, and rows
appended and fsynced to `results.jsonl` *during* the run.

[verified]: https://www.swebench.com/

## Running it

```bash
./scripts/bench/swebench/run.sh --dry-run                    # the seeded selection; no docker, no spend
./scripts/bench/swebench/run.sh --n 5 --budget 5             # smoke: 5 instances, hard $5 cap
./scripts/bench/swebench/run.sh --instance django__django-11099
./scripts/bench/swebench/run.sh --n 50 --budget 60 --instance-cap 2 --timeout 60m   # probe
```

**Every run spends real money on OpenRouter.** `--budget` is enforced on the
OpenRouter key's own usage counter (`GET /api/v1/key`), independent of
cortex's accounting: the driver will not start an instance whose estimated
cost (the most expensive instance so far, or `--est-first`) could push the run
past the cap, and during a turn it polls every `--poll` and interrupts the
turn at `--instance-cap` or at the run cap, whichever is lower. OpenRouter's
counter can lag by a few seconds, so leave headroom.

Requirements: Docker (the instance images are `linux/amd64`; on Apple Silicon
they run under emulation — see below), Go, Python 3.11 (`PYTHON_BIN` to
override), and the OpenRouter key in the macOS keychain
(`--key-service`, default `cortex-openrouter`).

| Flag | Default | Meaning |
|---|---|---|
| `--n` / `--seed` | `5` / `106` | the `n` instances with the smallest `sha256(seed + ":" + instance_id)` — reproducible in any language |
| `--instance a,b` | — | explicit ids, in order; overrides `--n`/`--seed` |
| `--model` / `--study-model` | `qwen/qwen3-coder` / same | OpenRouter model ids; one model for both roles by default (one system) |
| `--provider` | `novita/fp8` | OpenRouter provider order (slugs or endpoint tags) |
| `--allow-fallbacks` | `false` | let OpenRouter route elsewhere when the pinned provider fails |
| `--require-parameters` | `true` | only route to providers supporting every request parameter (tools) |
| `--window` / `--temperature` | `131072` / `0` | cortex context window, sampling temperature |
| `--budget` / `--instance-cap` / `--est-first` | `5` / `1.0` / `1.0` | spend guard, USD |
| `--timeout` | `40m` | wall clock for one cortex turn |
| `--eval-timeout` | `30m` | the harness's per-instance test timeout |
| `--keep-images` | `false` | keep images this run pulled (default: `docker rmi` after scoring — they are 2–5 GB each) |
| `--no-score` | `false` | predictions only |

## What one instance does

1. **Environment.** `docker pull --platform linux/amd64` the instance's
   prebuilt image (`swebench/sweb.eval.x86_64.<id>`: the repo at `/testbed`,
   checked out at the base commit, dependencies in the `testbed` conda env).
   Start a container, copy in a static linux cortex binary and the pinned
   `.cortex/config.json`, add `.cortex/` to `.git/info/exclude`, and verify
   `HEAD == base_commit`. Any failure here is `env_error` — the agent never ran.
2. **The agent.** `cortex turn --json "<prompt>"` in `/testbed`, with the
   conda env activated, `CORTEX_HOME=/tmp/cortex-home`, temperature 0. The
   prompt is the issue text verbatim plus one paragraph of task statement
   (`BuildPrompt`). **No hints, no test patch, no test names** —
   `LoadInstances` never even decodes `hints_text`, `patch`, `test_patch`,
   `FAIL_TO_PASS` or `PASS_TO_PASS`, so they cannot leak into a prompt.
   Web tools are off (`tools.enable_web: false`) and model substitution is off
   (`network.self_heal: false`).
3. **Artifacts.** `git add -A && git diff --cached <base_commit>` is the
   prediction (`.cortex/` excluded). The session transcript is the
   trajectory (`trajs/<id>.jsonl`); the container's whole `.cortex/` and
   `CORTEX_HOME` are copied out too.
4. **Scoring.** The prediction goes to the official harness, one instance at
   a time, against the **same pinned dataset file** the prompt came from. Its
   `report.json` decides `resolved`.
5. **Record.** Append to `predictions.jsonl` and `results.jsonl`, fsynced.

## The API key

The key never touches disk or argv. The driver reads it from the keychain
into memory and passes it to the in-container cortex only through the docker
CLI's process environment (`docker exec -e OPENROUTER_API_KEY` with no value).
The in-container config references it by name (`key_env`), and cortex strips
every `key_env` variable from its `bash` tool's environment
(`internal/tools/shellenv.go`), so a model running `env` sees nothing. As a
backstop, every artifact is scanned for the key before the row is written;
`secret_redacted: true` on a row means it was found and replaced — a bug to
investigate, never expected.

## Output

Everything lands in `.cortex/bench/swebench/<run-id>/` (gitignored):

```
run.json            model, provider routing, full workspace config, cortex commit,
                    swebench version, dataset + revision, seed, selection, caps, host/docker arch
results.jsonl       one row per instance, appended + fsynced as each finishes
predictions.jsonl   official format: {"instance_id","model_name_or_path","model_patch"}
trajs/<id>.jsonl    the cortex session transcript (the trajectory)
logs/<id>/          cortex.log (turn stdout/stderr), setup.log, patch.diff
instances/<id>/     the container's .cortex/ (sessions, journal, memory) + cortex-home/
eval/               the harness's working dir: logs/run_evaluation/<run>/<model>/<id>/
                    {report.json, test_output.txt, run_instance.log, patch.diff}
```

One row (abridged):

```json
{"instance_id":"sympy__sympy-13372","resolved":false,"failure_class":"wrong_patch",
 "turn_end":"completed","eval_status":"unresolved","tokens_in":412000,"tokens_out":6100,
 "cost_usd":0.17,"account_cost_usd":0.18,"wall_ms":903000,"tool_calls":41,
 "patch_bytes":812,"patch_files":1,"secret_redacted":false}
```

`cost_usd` is cortex's own sum of OpenRouter's per-call `usage.cost`
(the headless metrics row); `account_cost_usd` is the key's usage delta across
the instance (includes the harness-side spend of anything else using the key
at the same time — keep the key otherwise idle during a run).

## Failure classes

Exactly one per unresolved instance; resolved rows carry `""` (a resolved
patch counts however the turn ended — the diff on disk is the system's
output). Precedence top to bottom:

| Class | Meaning |
|---|---|
| `env_error` | image pull / container / checkout failed; the agent never ran |
| `budget_stop` | the spend guard interrupted the turn |
| `timeout` | the turn hit `--timeout` |
| `error` | the turn exited non-zero or reported an error |
| `chat_mode` | zero tool calls and no diff |
| `empty_patch` | tool calls, but no diff against the base commit |
| `patch_apply_failed` | the harness could not apply the diff |
| `eval_error` | the harness produced no report |
| `wrong_patch` | applied cleanly; the tests did not pass |

`turn_end` (`completed`/`timeout`/`budget_stop`/`error`) and `eval_status`
(`resolved`/`unresolved`/`patch_apply_failed`/`eval_error`/`skipped_empty_patch`)
are recorded separately on every row.

## Apple Silicon / emulation

The published instance images are `linux/amd64` only. The driver pulls them
with `--platform linux/amd64` and Docker Desktop runs them under emulation;
the official harness then finds the image locally and uses it (it would
otherwise try an `arm64` pull and fail with "no matching manifest"). Emulation
makes test runs several times slower, and a few environments misbehave under
it — treat an `env_error` or `eval_error` on arm64 as unverified until re-run
on an x86_64 host. For the 50-instance probe and the full 500, run on a native
x86_64 Linux host (see the run's report for the plan).

## Submission status

SWE-bench's experiments repo states (2025-11-18) that **Verified** now only
accepts submissions with an arXiv preprint/technical report and at least one
author affiliated with an academic institution or established research lab.
This runner produces everything a submission needs (predictions, per-instance
trajectories, harness logs) and follows the checklist (pass@1, no hints, no
test knowledge, no web), but whether an entry is *eligible* is a policy
question, not a runner one.
