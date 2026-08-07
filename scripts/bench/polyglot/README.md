# Polyglot benchmark runner (Go slice)

Scores the cortex coding agent on the Go slice of the [Exercism polyglot
benchmark set][repo] — the exercise corpus Aider's polyglot benchmark uses.
39 exercises, one fresh cortex session each.

**Scoring is deterministic.** The exercises ship unit tests, so an exercise
passes iff `go test ./...` exits 0 in its workdir. No LLM judges anything,
anywhere in this runner.

[repo]: https://github.com/Aider-AI/polyglot-benchmark

## Running it

```bash
./scripts/bench/polyglot/run.sh --only 3            # smoke test, minutes
./scripts/bench/polyglot/run.sh --exercise wordy,matrix
./scripts/bench/polyglot/run.sh --model coder-cuda --timeout 15m
./scripts/bench/polyglot/run.sh                     # FULL slice — hours of GPU
```

The full 39-exercise slice occupies the local GPU for a long time. Run it
deliberately, not as a side effect of iterating on the harness.

Flags (`--list` prints the corpus and exits):

| Flag | Default | Meaning |
|---|---|---|
| `--only N` | all | first N exercises in name order |
| `--exercise a,b` | — | explicit names, in the order given; overrides `--only` |
| `--model` | `qwen3-coder-q3` | model id for the `code` role |
| `--study-model` | `study` | model id for the `study` role |
| `--window` | `131072` | context window, tokens |
| `--temperature` | `0` | sampling temperature |
| `--backend` / `--endpoint` | `litellm` / `http://chatterbox:4000` | where the model is served |
| `--timeout` | `10m` | per-exercise budget for the cortex turn |
| `--test-timeout` | `2m` | per-exercise budget for `go test ./...` |
| `--run-id` | UTC timestamp | run directory name |

## What one exercise run does

1. Stage a pristine copy of the exercise into `work/<name>/` — everything the
   template ships **except `.meta/`** (which holds `example.go`, the reference
   solution) and `.docs/` (which becomes the prompt instead).
2. Create `work/<name>/.cortex/` with a pinned `config.json`. This is what
   makes the session fresh *and* isolated: cortex resolves its workspace by
   walking up for the nearest `.cortex` dir, so without this it would find the
   cortex repo's own and inherit that project's config, sessions and memory.
   `CORTEX_HOME` is likewise redirected to a run-owned directory.
3. `cortex turn --json "<instructions + task frame>"` with `cmd.Dir` set to the
   workdir, under a wall-clock timeout.
4. `go test ./...` in the workdir — the verdict.
5. Classify, then append the row to `results.jsonl` and fsync it.

## Output

Everything lands in `.cortex/bench/<run-id>/` (gitignored):

```
run.json          pinned model, temperature, cortex commit, polyglot commit, host, times
results.jsonl     one row per exercise, appended and fsynced as each finishes
transcripts/      <exercise>.jsonl (session transcript) + <exercise>.log (stdout/stderr)
work/<exercise>/  the workspace the agent actually edited, plus go-test.out
cortex-home/      the run's isolated CORTEX_HOME
```

Rows are written **during** the run, never reconstructed afterwards: a run
killed halfway still leaves a complete record of everything that finished.

One row:

```json
{"exercise":"alphametics","pass":false,"tool_calls":14,"tokens_in":312840,
 "tokens_out":4120,"wall_ms":286110,"failure_class":"wrong_code",
 "transcript_path":".cortex/bench/20260806-120000/transcripts/alphametics.jsonl",
 "session_id":"20260806-120014","mutating_calls":3,"files_changed":1,
 "agent_turns":1,"verify_ms":740,"work_dir":"..."}
```

`tokens_in` / `tokens_out` / `agent_turns` are read from the metrics row cortex
itself writes at the end of a headless turn (`emitSessionMetrics`, into the
workspace's own journal) rather than re-derived here — the benchmark reports
what the harness actually billed.

## Failure classes

Exactly one per non-passing exercise; passing rows carry `""`. Precedence runs
top to bottom — an attempt we cut short or that crashed is classified by how it
ended, not by what it managed to write first.

| Class | Meaning |
|---|---|
| `timeout` | the turn hit `--timeout` and was killed |
| `error` | the turn exited non-zero, reported an error, or the harness could not stage it |
| `chat_mode` | zero tool calls — the model answered in prose, touching nothing |
| `early_finalize` | tool calls, but zero net change to the solution files |
| `wrong_code` | the solution files changed and the tests still fail |

**`early_finalize`'s threshold is "zero solution files differ (sha256) from the
pristine stub", not "zero `write_file`/`edit_file` calls."** That is the
defensible line: an `edit_file` whose match failed, a `write_file` that rewrote
the stub byte-for-byte, and never attempting an edit at all are
indistinguishable from the exercise's point of view — in all three the agent
finalized with no work on disk. Counting attempted-but-landed-nothing calls as
work would mis-file those as `wrong_code` and hide the real failure. The
`mutating_calls` field is still recorded on every row, so the "tried and
failed" and "never tried" sub-cases stay separable in analysis.

A timed-out attempt is recorded as `pass: false` even if `go test` happens to
pass afterwards: the run was not bounded, so the result is not comparable.

## Pinning

`POLYGLOT_COMMIT` in `run.sh` pins the exercise corpus, and `run.sh` refuses to
run if the checkout is dirty. Bumping the pin changes the benchmark — results
are only comparable within one pin. The pin, the cortex commit (and whether its
tree was dirty), and the full model binding are all recorded in `run.json`.

## Layout

`run.sh` owns reproducibility (pin the corpus, build the binaries). The Go
driver owns the run: `exercise.go` (corpus, staging, prompt), `classify.go`
(evidence → one failure class), `sink.go` (`results.jsonl`, `run.json`, the
report), `main.go` (flags and the per-exercise loop). It is a standalone
`package main` — nothing here is imported by `cmd/cortex`.
