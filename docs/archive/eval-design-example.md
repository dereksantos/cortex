# The refactor eval: two layers, one gate

> Verification design for the engine-unification + study-subagent refactor
> ([`engine-unification.md`](../engine-unification.md),
> [`study-subagent.md`](../study-subagent.md)). The eval is built **first** and is
> **expected to fail** on every intermediate commit — it is a contract, not a
> smoke test. Parts come online as phases land (partial green); the autonomous
> run earns its pause only when **both** layers are fully green.

## The two layers

- **Δ — deterministic** (the *shape* is correct). Machine-decided, no model,
  same answer every run: `scripts/verify-study.sh` + `go test ./...` + an
  existence check for the behavior-preservation characterization test. It is the
  bulk of the contract and the only layer that can be fully built **before** the
  code.
- **ø — agentic** (the *behavior* is correct). Live-model-decided: `loop
  study-eval` drives the real `Study` subagent over frozen probes on the live
  fleet and scores goal-hit (must-mention facts present), `completed`, and
  `bounded`. A pinned threshold `T` makes it a hard gate, not a report.

```
                          AUTONOMOUS RUN (one session, no stops until complete)
                          ────────────────────────────────────────────────────
   ┌──────────────────────────────────────────────────────────────────────┐
   │  for each phase (engine 0..3, then study 1..6):                        │
   │     write the phase  ──►  commit  ──►  run the GATE                    │
   │                                          │                            │
   │                                          ▼                            │
   │            ┌───────────────── THE GATE ──────────────────┐           │
   │            │                                              │           │
   │   Δ  deterministic            ø  agentic (LIVE model)     │           │
   │   ──────────────────          ───────────────────────    │           │
   │   verify-study.sh             cortex study-eval            │           │
   │     §1 deletions→0 refs         real Study subagent      │           │
   │     §2 engine+seams exist       over frozen probes       │           │
   │     §3 study primitives         on the live fleet        │           │
   │     §4 tool surface exact       ─────────────────────    │           │
   │     §5 import graph             goal-hit ≥ T  (facts      │           │
   │     §6 build+vet+TEST             present in digest)      │           │
   │     §7 LOC band (vs base)       completed & bounded      │           │
   │   + characterization test                                │           │
   │   ──────────────────          ───────────────────────    │           │
   │   machine-decided             model-decided              │           │
   │   no model, deterministic     real inference, scored     │           │
   │            │                              │              │           │
   │            └──────────────┬───────────────┘              │           │
   │                           ▼                              │           │
   │              PAUSE  ⟺  Δ green  AND  ø green             │           │
   │              else: keep going to the next phase          │           │
   └──────────────────────────────────────────────────────────────────────┘
```

## Progressive green — RED until done, by design

The gate is **expected to fail on every intermediate commit.** Parts come
online as phases land; the run only earns its pause at the end, when both
layers are fully green.

```
                       Δ structural        ø behavioral
   phase              (verify-study.sh)   (live study-eval)
   ─────────────────  ─────────────────   ─────────────────
   start (today)      ▓░░░░░  7/45        ▒  baseline 5/6 (old nav over-reads probe 6)
   engine 0..2        ▓▓▓░░░  seams up    ▒  baseline held (navigator untouched)
   engine 3           ▓▓▓▓░░  agent pkg   ▒  baseline held (navigator untouched)
   study 1..3         ▓▓▓▓▓░  primitives  ▒  baseline held (navigator untouched)
   study 4  ◄──────── ▓▓▓▓▓▓  deletions   ░  ABSENT (nav + old driver deleted)
   study 5            ▓▓▓▓▓▓  green        ▓  driver rebuilt → drive to 6/6 exit 0
   study 6 (docs)     ██████  GREEN        █  GREEN (6/6 exit 0)  ──►  PAUSE ✓
                          Δ ✓                  ø ✓
```

Key facts: **green-at-the-END is the bar, not green-at-the-start.** Δ grows
monotonically from 7/45 toward all-green. ø is **not a hard gate until phase 5**:
today the old navigator scores a **5/6 baseline** on the 6-probe set — it
over-reads the `tools.go` smoke-floor probe to a ~102s, empty-digest fail, the
exact brute-read failure the new grep-based `Study` is built to fix. Through
engine phases 0–3 and study phases 1–3 the navigator is untouched, so ø is used
only as a **regression detector: the 5/6 baseline must not drop** (a drop means
the engine work leaked behavior). ø goes **absent at study phase 4** (navigator +
driver deleted), then at **phase 5 becomes the real hard gate** — the new `Study`
must pass **all 6 probes at n=1, exit 0** (reachable because grep removes the
over-read that fails probe 6 today). The pause is earned only when both layers
are green at the end.

## The brief

- **Δ — deterministic (the shape is correct).** `verify-study.sh` + `go test
  ./...` + an existence check for the Part-1 characterization test. Pure machine
  verdict: symbols that must be gone are gone, symbols that must exist exist, the
  import graph holds, the tree builds and all tests pass, the net-LOC stays in
  band. Same answer every run. The only layer that can be fully built **before**
  the code.

- **ø — agentic (the behavior is correct).** `cortex study-eval` on the **live
  fleet**: the real `Study` subagent reads frozen probes and answers;
  `countGoalHits` scores whether the must-mention facts are in the digest, and
  the engine's own accounting confirms each run `completed & bounded`. Pinned
  threshold so it self-decides: **every probe passes at n=1 on the configured
  study model, all completed & bounded** — enforced by exiting non-zero
  otherwise, so the autonomous run reads pass/fail from the exit code.

- **The gate rule.** `pause ⟺ Δ green ∧ ø green`. Either red → not done →
  continue. No partial credit, no "deferred to follow-up."

- **Build-first.** Two of the three pieces precede the code and are built in the
  very first commits: the Δ contract (`verify-study.sh`, already red) and the
  **characterization test** that locks coder-loop behavior before the
  `Resolve`→`Turn` fold. The ø *driver* is co-built in study phase 5 (it tests
  something that does not exist yet), but its **scorer already exists and is
  unit-tested**, so it is wiring, not new logic.

## What the eval ran on (flight check — 2026-06-27)

Recorded so it is clear what ø was measured against. **No endpoint host is
named here** — it lives only in the untracked user config (`~/.cortex/config.json`).

- **Backend.** A local, OpenAI-compatible LiteLLM gateway (`backend.type:
  litellm`). Endpoint is in the untracked user config, not the repo.
- **Role bindings at flight-check time.** `code → north`, `study → north`.
- **Models available on the gateway.** `north`, `glm-4.7-flash`, `qwen3-4b`,
  `reasoner`, `gpt-oss-20b`, `gpt-oss-120b`, `devstral`, `coder`, `study`,
  `xlam-1b-fc-r`, `embedder`, `reranker` (and variants).
- **`north` is a reasoning model** (returns `reasoning_content`); a bare
  round-trip answered in ~0.4s.
- **ø flight result.** `cortex study-eval` on `north` with study deliberating
  (thinking ON), over the current pre-refactor navigator path: **pass 3/3,
  median ~33s, 0 errors → exit 0.** With thinking forced OFF it was 2/3 — the
  main.go probe returned a thin ~156-char digest; enabling reasoning lifted it
  to a full pass (4.8k-char digest, goal-hit 2/2) at ~2× latency. This is the
  empirical refutation of the old "thinking-off for study" claim, and confirms
  ø-green is reachable on a working study impl. The T gate still holds (2/3 ⇒
  exit 1, 3/3 ⇒ exit 0). Note this exercises the OLD navigator; study phase 4
  deletes it and phase 5 rebuilds the driver on `RunSubagent`, so ø goes absent
  then must return to green there. **Baseline update (2026-06-28):** the probe
  set was then hardened from these 3 weak cases to **6 discriminating probes**
  (see the study-subagent Decisions log). On that harder set the OLD navigator
  scores **5/6** — it over-reads the `tools.go` smoke-floor probe to a ~102s,
  empty-digest fail. That 5/6 is the regression-detector baseline through Part 1;
  the new grep-based `Study` is what drives it to 6/6 exit 0 at phase 5. ø is
  therefore **not exit-0 at the start, and is not required to be** —
  green-at-the-end is the bar.
- **Study runs on the reasoner, thinking ON.** `study` draws from the `reasoner`
  tag (flight check: bound to `north`). The flight check confirms a reasoner
  uses tools — `north` called `read_file` across all three probes — so study
  deliberates with reasoning enabled rather than forcing it off.

## Running it

```bash
scripts/verify-study.sh                 # Δ: structural contract (current tree)
scripts/verify-study.sh --diff-base REF # Δ: + net-LOC band + scoped-diff vs REF
cortex study-eval                         # ø: live-model goal-hit gate (exit code = verdict)
```
