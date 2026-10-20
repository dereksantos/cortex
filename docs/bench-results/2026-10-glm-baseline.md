# First paired baseline: cortex vs mini-SWE-agent on glm-4.7-flash (October 2026)

**Result.** On 50 SWE-bench Verified tasks, same model, same tasks, one task at
a time: **cortex 24/50 (48%), mini-SWE-agent 29/50 (58%)**. On the tasks only
one of them solved, mini took 10 and cortex 5: a two-sided sign test gives
**p = 0.30**, so this is not a measured difference. One of cortex's 26 failures
was a scoring artifact (below); scored correctly the run is likely 25 vs 29.

**What would change it.** Two cortex changes — restoring the prompt's explicit
red/green step, and raising the tool-call cap from 100 to 250 — did not beat an
unchanged re-run on the tasks cortex lost (§4). Cortex finds the right code and
runs the tests about as often as mini does; most of its losses are a fix that
misreads what the issue means (§5). That makes intent checking, not more
verification, the next lever.

Every number here comes from the durable store (`~/.cortex/benchmarks`, synced
to R2; `docs/benchmarks.md`) and is reproduced by the scripts in
[`scripts/bench/analysis/`](../../scripts/bench/analysis/) — §8 lists the
commands.

## 1. Setup

| | |
|---|---|
| Benchmark | SWE-bench Verified, dataset revision `78f471bf655a`, scored by the official harness (`swebench` 5.0.2) |
| Tasks | `--seed 106 --n 50` — the runner's sha256-ranked selection; the frozen list is `ids.txt` in the batch dir. 24 django, 6 sympy, 4 matplotlib, 3 each sphinx / pytest / astropy, 2 each scikit-learn / xarray / requests, 1 pylint |
| Model | glm-4.7-flash, `GLM-4.7-Flash-UD-Q4_K_XL.gguf`, llama-server (one slot, 202,752-token context) on chatterbox, temperature 1.0, chat template's default reasoning |
| cortex | `617cbb6` (main after #289, #290, #292), headless `cortex turn`, window 131,072, 30-minute turn timeout, tool-call cap 100, `tools.edit_format` auto (= `replace` for GLM) |
| mini-SWE-agent | 2.4.6, its official `swebench.yaml` (step limit 250), 30-minute wall limit, response cap 16,384 tokens (§3) |
| Pairing | each task ran both arms back to back, order alternating per task; before every arm the driver checked that the server served GLM and its slot was idle |
| Store | batch `baseline-sv50-s106-20261013` (labels `baseline=sv50-s106 model=glm-4.7-flash served=GLM-4.7-Flash-UD-Q4_K_XL.gguf`), 2026-10-14 00:19Z – 16:33Z |

Why glm-4.7-flash. A short model probe on six tasks (`ab-editfmt-20261013`,
`probe-glm-20261013b`) ruled out gpt-oss-20b, which solved 0–1 of 6 and could
not edit reliably through `edit_file` (it kept omitting `new_string`; #292 added
`apply_patch` for its model family). GLM solved 3/6 in cortex and 1/6 in mini
there, a pass rate with room to see a difference.

## 2. The result

| | cortex | mini-SWE-agent |
|---|---|---|
| Solved | **24 / 50 (48%)** | **29 / 50 (58%)** |
| Solved only by this agent | 5 | 10 |
| Failures: wrong fix | 25 | 16 |
| Failures: timeout | 1 | 3 |
| Failures: no change | 0 | 2 |
| Median minutes per task | 6.6 | 5.2 |

- Only cortex: astropy-7166, django-11951, requests-5414, scikit-learn-25931, sympy-22456.
- Only mini: astropy-8872, django-13023, django-14140, django-15161, django-15375, django-15572, django-16877, django-16938, pytest-5787, pytest-7324.

Cortex never gives up empty-handed: 25 of its 26 failures are a change the
official tests reject. At this sample size a 10-vs-5 split arises by chance
about 30% of the time; telling a ten-point gap from noise needs roughly
150–200 paired tasks.

For reference, the earlier qwen3.8-27b pairs on 11 of these tasks, scored the
same way on the tasks both agents were scored on, were one task apart:
`pair-20261008` (cortex `a60f56a`, whose prompt still had the red/green line)
cortex 8/11 vs mini 9/11, and `pair-cb` (cortex `f14ab43`) 7/11 vs 8/11.

## 3. What went wrong during the run, and the fixes

Three infrastructure problems surfaced; each is fixed (#295), and the affected
attempts are kept in the store labeled `invalid=<reason>` and re-run as `.r2`.
The export leaves invalid runs out of every table and folds a `.rN` re-run into
its arm, so each task has exactly one valid result per agent.

1. **Both agents ran at once (instance 2, matplotlib-23476).** mini hit its
   30-minute wall limit; `run_mini.sh`'s kill path ran `pkill -P` first, which
   matched no child and, under `set -e`, ended the script before mini was
   killed. The driver started cortex while mini kept running on the one-slot
   server. Fix: `|| true` on every kill (`run_mini.sh`). Label
   `invalid=concurrent-arms`.
2. **A runaway response held the server (instances 3–4).** mini's official
   config sends no `max_tokens`; a hosted API caps responses, a local
   llama-server does not. A looping GLM response passed 50,000 tokens, and
   llama-server kept decoding it after mini gave up, holding its only slot for
   over an hour — cortex's next arms got 0 tool calls before timing out. Fix:
   the litellm overlay sends `max_tokens: 16384`, the cap cortex already sends
   (`codeMaxOutputTokens`); the driver also waits for an idle slot before every
   arm. Label `invalid=runaway-blocked`. Recommended, not done: start
   llama-server with `-n 16384` so no client can hold the slot this way.
3. **mini's timeouts vanished from the comparison.** A mini run killed at its
   wall limit writes no prediction, and the store built mini's rows from
   predictions only, so the task had no mini row and dropped out of the paired
   count while cortex's timeouts counted as failures. Fix: an instance in
   `usage.jsonl` with no prediction is a failed attempt (`timeout`, or
   `agent_error`).

Before the run, a probe of the same driver showed that chatterbox's LiteLLM
routes several model names (glm-4.7-flash, gpt-oss-20b, …) to one llama-server
whose loaded model is swapped by hand; a "glm" probe was really gpt-oss
(`probe-glm-20261013`, `invalid=route-served-gpt-oss-20b`). The drivers now read
the served file from llama-server's `/props` before every arm and record it as
the `served` label. A runner-level version of that check is not built yet.

## 4. Follow-up A/B: red/green prompt and tool-call cap

The 2026-10-09 prompt rewrite (`ded553b`) replaced *"For a behavior change,
write the test before the change — see it fail, then make it pass"* with
*"Reproduce a reported problem before fixing it, ideally as a failing test"*;
and cortex's turn stops at 100 tool calls where mini's limit is 250 steps.
Either could explain losses, so batch `ab-redgreen-cap-20261014` re-ran cortex
on the 10 tasks only mini solved, four arms, order rotating per task:

| Arm | Change | Solved |
|---|---|---|
| control | none: `prompt.file` = today's built-in prompt, verbatim | **3 / 10** |
| redgreen | one sentence: *"For a behavior change, write the test first: see it fail, then make it pass."* | 2 / 10 |
| cap250 | `limits.max_tool_iterations` 250 | 4 / 10 |
| both | redgreen + cap250 | 3 / 10 |

No arm beats control. The control arm matters: these tasks were chosen for
failing once at temperature 1, so a plain re-run already recovers some of them
— django-15161 and django-15572 pass in all four arms, which makes the
baseline losses on them chance. Five tasks fail in every arm: django-14140,
django-15375, django-16877, pytest-5787, pytest-7324.

The red/green sentence barely changed behavior: cortex already writes or edits
a test file in nearly every run (control 10/10), and its first edit was a test
in 5 control runs, 3 redgreen, 8 both. Verification is not what is missing.

## 5. Why cortex loses

Across the five always-fail tasks and the whole baseline:

- **It finds the right code.** In every attempt cortex edited the same file the
  official fix and mini changed.
- **It runs the tests about as often as mini.** By a text match on its shell
  commands (approximate), cortex ran the project's own test suite in 39 of 50
  tasks; mini in 38 of the 45 it was scored on.
- **Its 26 failures:** 1 scoring artifact, 4 that broke tests which used to
  pass (astropy-8872, django-14787, matplotlib-21568, pytest-5787), and about
  21 fixes the official tests reject without breaking anything else.

The always-fail tasks show the kinds of wrong fix:

- **A scoring artifact — django-16877.** Cortex's fix is the official one
  (`escapeseq` returning `[conditional_escape(obj) for obj in value]`), but
  cortex also wrote `tests/template_tests/filter_tests/test_escapeseq.py` —
  the path the official test patch creates. Scoring's `git apply` failed
  ("already exists in working directory"), the official tests never ran, and
  cortex's own test file was scored instead. The runner submits every new file
  as part of the prediction; mini's config tells the model to leave test and
  reproduction files out. It is the only task that collided: all five of its
attempts, out of 90 cortex runs scanned.
- **The issue's other option — django-14140.** The issue offers two fixes:
  remove a special case (the linked PR), or *"a more backward-compatible
  approach … keep the special case and explicitly check that the child is a
  length-2 tuple"*. Cortex chose the second in all five attempts; the official
  tests expect the first. Plausibly reinforced by the prompt's *"Existing
  tests … record decisions someone made; when they disagree with your change,
  suspect your change first"*, since the existing test encodes the old
  behavior. One task — not a reason to weaken that principle, which came from
  real test-weakening incidents.
- **Misread semantics — pytest-7324.** Cortex turned `True`/`False`/`None` into
  Python constants; in pytest's mark expressions they are identifiers to match
  (the official fix prefixes them). Its own test checked only that compiling no
  longer crashed, so its verification could not see the wrong meaning.
- **Regressions — pytest-5787, django-15375.** The target tests pass (5787) or
  the fix is in the wrong layer (15375), and tests that passed before break. In
  the attempts examined, cortex verified with scratch scripts instead of the
  module's existing suite — the exception rather than the rule, per the 39/50
  above.

## 6. Conclusions

1. On glm-4.7-flash the two harnesses are not measurably different at n = 50;
   mini is ahead by 10 points, within chance.
2. Scored correctly (§5, django-16877) the gap narrows to about 25 vs 29.
3. The red/green sentence and the higher tool-call cap do not recover lost
   tasks.
4. Cortex's main failure is understanding the intended behavior, not finding
   code or running tests — the kind of check a decision model beside the agent
   could make ("does this fix match what the issue asks for?").

## 7. Next

- **Runner: keep the agent's own tests and scratch files out of the
  prediction**, the mechanical counterpart of mini's instruction, then re-score
  the existing predictions (no agent re-run). Expected to flip django-16877.
- **Server: llama-server `-n 16384`** on chatterbox so no client can monopolize
  the slot.
- **Measure variance before ranking**: 2–3 repeats of both agents on the 15
  tasks where they disagreed, or grow the set toward 150–200 tasks.
- **Decider (shadow first)**: an intent check at finalize, evaluated against
  these per-task outcomes before it is allowed to change anything.

## 8. Reproducing the numbers

```bash
./scripts/bench/dashboard-export.sh                     # store → exports/{runs,tasks,failures}.csv
python3 scripts/bench/analysis/paired.py baseline-sv50-s106-20261013           # §2
python3 scripts/bench/analysis/paired.py ab-redgreen-cap-20261014 --table      # §4
python3 scripts/bench/analysis/scoring.py \
  ~/.cortex/benchmarks/runs/swebench/baseline-sv50-s106-20261013 \
  ~/.cortex/benchmarks/runs/swebench/ab-redgreen-cap-20261014 --mini           # §5
```

The batch dirs also hold the drivers that produced them (`driver.sh`,
`driver2.sh`, `fixup-instance2.sh`), the frozen `ids.txt`, and for the A/B the
two prompt files. The trend dashboard
(https://claude.ai/artifact/ARmnggXtMTif4zDFS3TPC1) shows the same tables.
