# SWE-bench Verified

`internal/eval/benchmarks/swebench/` runs Cortex's in-process coding
harness against the [SWE-bench Verified](https://www.swebench.com/)
dataset — 500 human-curated GitHub issues drawn from twelve open-source
Python repos. Each instance is a single bug whose patch is hand-graded
by the SWE-bench authors against a canonical `FAIL_TO_PASS` and
`PASS_TO_PASS` test set. The benchmark is the field's de facto standard
yardstick for coding-agent capability.

This loop wires Phase A: load instances, run the in-process harness,
extract a patch, score against the canonical Docker images. Phase B
(full 500-instance nightly, optional Python shim for the upstream
scoring harness) is deferred.

## What's measured

- `task_success` = (every `FAIL_TO_PASS` test passes) AND (every
  `PASS_TO_PASS` test still passes). Missing tests count as failed.
- `tests_passed` / `tests_failed` aggregate across both sets.
- `cost_usd`, `tokens_in`, `tokens_out`, `latency_ms` ride the
  CellResult schema directly from the harness.

Per-instance `notes` records the F2P / P2P split and the docker image
that scored the patch so failure analysis can group by image / repo /
version.

## Upstream data

| | |
|---|---|
| Dataset | [`princeton-nlp/SWE-bench_Verified`](https://huggingface.co/datasets/princeton-nlp/SWE-bench_Verified) on HuggingFace |
| Split | `test` (500 rows) |
| Scoring images | `swebench/sweb.eval.x86_64.<repo>:v<version>` on Docker Hub |
| Project page | <https://www.swebench.com/> |

**License caveat:** the dataset card does not state an explicit
license. We fetch instances at runtime from the HF datasets-server
JSON API and do NOT vendor any sample into the repo. Tests use
synthesised rows that mirror the schema but contain no upstream
content.

## CLI

```bash
cortex eval --benchmark swebench \
  --subset verified \
  --limit 3 \
  --model anthropic/claude-3-5-haiku \
  --strategy baseline,cortex
```

| Flag | Default | What |
|------|---------|------|
| `--subset` | `verified` | Only `verified` is wired; `lite`/`full` are not. |
| `--limit N` | `10` | Cap instances loaded. The full 500-row split is a CI nightly target, not interactive. |
| `--repo <slug>` | none | Repeatable. Restrict to upstream repo (e.g. `--repo django/django --repo psf/requests`). |
| `--strategy <list>` | `cortex` | Comma-separated. `baseline` runs the harness with `cortex_search` disabled and an empty `.cortex/`; `cortex` keeps the tool wired. |
| `--model <id>` | required | OpenRouter model id (e.g. `anthropic/claude-3-5-haiku`, `qwen/qwen-2.5-coder-32b-instruct`). |
| `--docker-image-prefix <pfx>` | `swebench/sweb.eval.x86_64.` | Override for registry-mirror users. |
| `--git-cache-dir <dir>` | none | If set, keeps a bare mirror at `<dir>/<repo>.git` and clones via `--reference-if-able`. Big win on django / sympy. |

## Two design choices to know about

### Shell allowlist is NOT extended for Python tooling

The in-loop shell tool (`run_shell`) only lets the agent run a fixed
set of binaries (`go`, `gofmt`, `ls`, `cat`, `head`, `tail`, `wc`,
`diff`, `grep`, `test`). SWE-bench tasks are Python; the agent does
NOT get `python`, `pip`, or `pytest` in-loop.

The agent's job is to read code, reason about the bug, and write the
fix. Verification happens out-of-loop in the canonical SWE-bench
Docker image — so the score is comparable to published agents and the
in-loop affordance stays small. Letting the agent run pytest
introduces a different bug class (the agent hands you "all tests
pass" when they didn't) that's a Phase B problem if we want it at
all.

### Patch extraction is `git diff`, not "print the patch"

`runner.go` runs `git -C repo diff --no-color` after the agent
session and treats that output as the patch. We do NOT ask the
agent to print its patch as a final message. Models forget; the
working-tree diff is authoritative. An empty diff is a valid (and
common) "agent gave up" signal.

## Docker as a soft dependency

Scoring requires `docker` on `$PATH`. If it isn't, `RunSWEBenchTests`
returns a clean install-Docker error and the run fails fast — no
silent half-success. We do NOT pull images ourselves: Docker fetches
on demand, which can add ~1 GB per repo+version on first use.

For CI / nightly use, pre-pull the images you care about:

```bash
docker pull swebench/sweb.eval.x86_64.django__django:v4.2
```

## Per-instance wall-time profile

- `git clone` (5s – 90s; django/sympy are the long tail; `--git-cache-dir`
  drops this to <5s)
- harness loop (model-dependent; 30s – 5min per attempt)
- `git diff` (instant)
- docker run + pytest (60s – 10min depending on the test set; the
  per-instance timeout is 30 min by default)

Plan for ~5–15 min per instance on a small repo on a hot cache; an
order of magnitude longer for first-time django/sympy.

## Reproduction

```bash
# Phase A smoke (cortex strategy only, three small repos)
cortex eval --benchmark swebench --limit 3 \
  --model anthropic/claude-3-5-haiku \
  --repo astroid/astroid

# Baseline vs cortex split on a single repo
cortex eval --benchmark swebench --limit 5 \
  --model anthropic/claude-3-5-haiku \
  --repo psf/requests \
  --strategy baseline,cortex

# With a git mirror cache, against a registry mirror
cortex eval --benchmark swebench --limit 20 \
  --model openai/o3-mini \
  --git-cache-dir ~/.cache/cortex/git-mirrors \
  --docker-image-prefix my-mirror.example/sweb.eval.x86_64.
```

## Deferred (Phase B)

- `--seed-from <dir>` to pre-seed `.cortex/` from a prior capture dump
  for the `cortex` strategy — currently the store is always empty at
  session start, so the strategy split mostly measures the cost of
  having `cortex_search` available with nothing to find.
- Full 500-instance nightly + parallel docker scoring.
- Optional Python shim that registers Cortex as an agent with the
  upstream `swebench` package's scoring harness, so the numbers we
  publish use the canonical scorer end-to-end rather than our own
  pytest re-runner.
