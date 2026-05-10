# Phase 7 — Implement OpenCodeHarness + PiDevHarness

> Read this file end-to-end. Self-contained: a fresh Claude Code
> session pastes this in and continues the eval-harness work without
> any other context.

---

## Why this session exists — the anchor

Today's eval results (see `docs/eval-findings-2026-05-10.md`) are
measured through **a single harness — Aider**. Static-cortex injection
shows 0 pp lift on coding tasks and +31%/+52% lift on retrieval
tasks. But those numbers are confounded: we don't yet know how much
of "cortex lift" is actually "Aider's particular prompt-injection
shape working well" versus a real model-side effect.

The same scenario through three different harnesses is the ablation
that disambiguates this. Without it, any positive result has the
caveat "in Aider, with claude-haiku-4.5, against synthetic context."

**This session implements the two deferred harnesses** so the
cross-harness ablation becomes runnable. The remaining eval / strategy
work continues in parallel from `docs/eval-resume-prompt.md`.

### Pass criteria

A run lands as **decisive harness validation** when:

1. **Both harnesses ship and compile** — `go build ./...` green,
   `go test ./internal/eval/v2/... -count=1` green.
2. **Each harness produces non-zero `tokens_in` on a real smoke run**
   against an OpenRouter free model. That's the litmus test that the
   harness actually put the files in front of the model (the Aider
   `--file` bug from 2026-05-10 was caught exactly this way).
3. **Cross-harness smoke: 1 scenario × 3 harnesses × 1 free model ×
   baseline strategy** → 3 CellResults, all `task_success=true` or
   all populated with real `tokens_in` / `cost_usd`.
4. **Baseline pass-rate divergence across harnesses is < 20 pp** on
   the same model + scenarios. Larger divergence means one harness
   is silently failing; investigate before claiming cross-harness
   data.

A run lands as **decisive harness invalidation** when:

- A harness consistently returns 0 `tokens_in` (the model never saw
  the workdir).
- A harness's exit code is 0 but the verifier never sees any edits
  (the harness isn't applying the model's response).
- Different harnesses give pass-rates that differ by > 30 pp on the
  same model — one of them has a wiring bug.

---

## Where the work plugs in

The existing infrastructure is on `feat/measure-tooling` (PR #7).
Read these files first; they're the reference implementation:

- `internal/eval/v2/library_service.go:76` — the `Harness` interface
  every harness implements (one method: `RunSession(ctx, prompt,
  workdir) error`).
- `internal/eval/v2/harness.go` — the `ResultfulHarness` extension
  (one extra method: `RunSessionWithResult(ctx, prompt, workdir) →
  (HarnessResult, error)`). Used by the grid runner via type
  assertion.
- `internal/eval/v2/library_service_aider_harness.go` — the
  **reference implementation**. Mirror this shape:
  - Binary resolution (`$AIDER_BINARY` → PATH lookup).
  - Stdout capture + parser for tokens / cost / "Applied edit to ..."
    lines.
  - `SetModel(string)` for per-cell model swap.
  - Auto-add workdir source files via `--file` flag.
  - Re-export `OPEN_ROUTER_API_KEY` → `OPENROUTER_API_KEY` for the
    underlying SDK.
  - `RunSession` + `RunSessionWithResult` share an internal
    `runSession` for parity.
- `internal/eval/v2/library_service_aider_harness_test.go` — the
  **reference test patterns**. Mirror this shape:
  - `installFakeAider(t, dir, body)` writes a shell script that
    mimics aider's stdout shape; harness tests invoke against the
    fake.
  - Real-binary integration tests `t.Skip` when the binary isn't on
    PATH or the upstream service is unreachable.
  - Table-driven tests for the output parser.

The grid runner (`internal/eval/v2/grid.go`) already type-asserts on
`ResultfulHarness` and falls back to bare `RunSession` if a harness
doesn't implement it. Once a new harness implements both, the grid
runner picks it up automatically — no runner changes needed.

The CLI subcommand (`cmd/cortex/commands/eval_grid.go`) currently
rejects `--harnesses opencode` / `--harnesses pi_dev` with a clear
"deferred to TODOs 10/11" error. Updating the CLI's switch to allow
the new harnesses is part of this session's done criteria.

---

## Hard constraints (do not violate)

1. **`internal/eval/v2/cellresult.go` schema stays frozen.** Adding a
   new optional field with `omitempty` is allowed; renaming or
   removing fields requires user signoff (PR-level review).

2. **Existing `Harness` interface stays compatible.** `AiderHarness`
   and `ClaudeCLIHarness` must keep passing their existing tests.

3. **No mocks of the LLM in harness round-trip tests.** Use shell-
   script fake binaries (the `installFakeAider` pattern) for unit
   tests. Real-binary integration tests `t.Skip` when the dependency
   isn't present.

4. **Standard library `testing` only.** No testify, no assert
   libraries. Table-driven with `t.Run`. Setup/teardown via `defer`.

5. **Never log `OPEN_ROUTER_API_KEY`, `ANTHROPIC_API_KEY`, or any
   secret.** Redact in error messages too. Note: the project uses
   the underscore form `OPEN_ROUTER_API_KEY`; harnesses must
   re-export to their CLI's expected name (litellm uses
   `OPENROUTER_API_KEY` — opencode and pi.dev may follow the same
   convention or differ).

6. **Real OpenRouter calls cost real money.** `:free` model calls
   are exempt (TODO 1 in the build loop measured the limits). Paid
   model calls in this session need `CORTEX_EVAL_ALLOW_SPEND=1` in
   env. Frontier (Sonnet/Opus) needs `CORTEX_EVAL_ALLOW_FRONTIER=1`
   on top.

7. **Don't push, don't open PRs, don't run `cortex daemon`.** Local
   commits on the `feat/phase7-harnesses` branch (create it from
   `main` once PR #7 merges, otherwise from `feat/measure-tooling`).

8. **Structured outputs.** Every CellResult goes to both SQLite
   `cell_results` AND `.cortex/db/cell_results.jsonl`. The
   `PersistCell` path already enforces this — don't bypass it.

---

## Prerequisites — install before starting

Both CLIs are gated behind PATH lookup. Install before the first
TODO:

```bash
# opencode (open-source AI coding agent)
curl -fsSL https://opencode.ai/install | bash

# pi (Pi coding agent — see https://pi.dev for the current install)
# Probably one of:
#   npm install -g @earendil/pi
#   curl -fsSL https://pi.dev/install | bash
# Confirm via `which pi` after install.
```

Verify both are on PATH before proceeding:

```bash
which opencode || echo "MISSING: install opencode first"
which pi || echo "MISSING: install pi.dev first"
```

If either is missing, **stop and ask the user to install before
continuing**. The session can't make meaningful progress without them.

---

## Iteration protocol (every tick)

1. **Read this file end-to-end.** Don't skip.
2. **Read `git log -5 --oneline`** to see what previous ticks landed.
3. **Read `git status`** to confirm a clean tree.
4. **Pick the lowest-numbered un-checked TODO** from the list below.
5. **Implement just that step.** No scope creep — if you need to
   touch >3 files outside the step's scope, stop and ask.
6. **Run the gate:**
   ```
   go build ./...
   go test ./internal/eval/v2/... -count=1
   ```
7. **If green:** commit with subject `eval-harness-phase7: <step>`,
   edit *this file* to flip the checkbox, commit the doc edit as
   `docs(eval-harness-phase7): mark step N done`.
8. **If red:** print the failing output, stop the loop.
9. **Schedule next tick at 60s** for self-paced loops, or proceed
   immediately if the user is driving interactively.

---

## Ordered TODOs

> Each step is one tick. Don't merge steps.

### Phase 7.A — OpenCodeHarness

- [x] **1. Probe opencode CLI output shape.**
  - Write a throwaway `cmd/cortex-opencode-probe/main.go` (delete
    after step 2) that:
    1. Creates a tiny scratch dir with a seed file (a `hello.go` with
       a stub function).
    2. Invokes `opencode run --model openrouter/openai/gpt-oss-20b:free
       --dir <scratch> --format json "implement the stub"`.
    3. Captures stdout + stderr; writes the full event stream to
       `docs/opencode-probe.json`.
  - Inspect the event stream. Document in `docs/opencode-tiers.md`:
    - The event types emitted (probably `message`, `tool_call`,
      `file_edit`, `usage`, etc.).
    - The exact JSON keys for token counts, cost, file edits, exit
      reason.
    - How file additions work (does `--dir` auto-add, or do we need
      explicit `--add <path>` like Aider's `--file`?).
  - **Done:** probe runs, both files written, event schema
    summarized.

- [x] **2. Implement `OpenCodeHarness` in
  `internal/eval/v2/library_service_opencode_harness.go`.**
  - Mirror `AiderHarness` structure:
    - `OpenCodeHarness struct { binary, model string }`
    - `NewOpenCodeHarness(binary, model string) (*OpenCodeHarness, error)`
      with `$OPENCODE_BINARY` env override + PATH lookup.
    - `SetModel(string)`, `Model() string`.
    - `RunSession(ctx, prompt, workdir) error` — thin wrapper.
    - `RunSessionWithResult(ctx, prompt, workdir) (HarnessResult, error)`
      — captures stdout, parses event stream, returns populated
      result.
  - Build the CLI invocation:
    ```
    opencode run --model <provider/model> --dir <workdir>
                 --format json "<prompt>"
    ```
    Auto-add source files via the equivalent of Aider's `--file` if
    opencode requires it (discovered in step 1).
  - Re-export the OpenRouter API key under whatever name opencode
    expects (likely `OPENROUTER_API_KEY` — confirm in step 1).
  - Parse the event stream for: tokens_in, tokens_out, cost_usd,
    agent_turns_total, files_changed.
  - **Done:** harness compiles, implements both interfaces (compile-
    time `var _ Harness = ...` / `var _ ResultfulHarness = ...`).
  - Delete `cmd/cortex-opencode-probe/`.

- [x] **3. Test `OpenCodeHarness` against a fake binary.**
  - Mirror `library_service_aider_harness_test.go`'s `installFakeOpencode`
    pattern: a shell script that writes the documented event-stream
    shape to stdout.
  - Cover:
    - Binary missing → clear error.
    - `$OPENCODE_BINARY` env precedence over PATH.
    - Happy path: prompt forwarded, exit 0, parser populates result.
    - Non-zero exit wraps stderr.
    - Context cancel kills subprocess group within the SIGTERM
      grace window.
  - **Done:** new test file with the above cases, all passing.

- [x] **4. Real-binary smoke for OpenCodeHarness against
  OpenRouter.**
  - Test that t.Skips when `opencode` not on PATH OR when
    `OPEN_ROUTER_API_KEY` not set.
  - Otherwise runs against `openrouter/openai/gpt-oss-20b:free` on a
    trivial seed dir (one file with a stub).
  - Asserts: `TokensIn > 0`, `LatencyMs > 0`, `ProviderEcho` and
    `ModelEcho` populated.
  - **Done:** test green when prerequisites present, t.Skip
    otherwise.

### Phase 7.B — PiDevHarness

- [x] **5. Probe pi.dev CLI output shape.**
  - Throwaway `cmd/cortex-pidev-probe/main.go`, same shape as TODO 1.
  - Invocation:
    ```
    pi --mode json --provider openrouter --model
       openai/gpt-oss-20b:free -p "implement the stub"
    ```
    Working dir = the scratch dir (via `cmd.Dir`).
  - Inspect newline-delimited JSON events. Document in
    `docs/pidev-events.md`.
  - Confirm the OpenRouter provider is configured. If pi.dev needs
    `~/.pi/agent/models.json`, write that as part of the probe and
    document the required schema.
  - **Done:** probe runs, event shape documented, models.json (if
    needed) committed at a known path.

- [x] **6. Implement `PiDevHarness` in
  `internal/eval/v2/library_service_pidev_harness.go`.**
  - Mirror `OpenCodeHarness` from TODO 2, swapping the CLI
    invocation and the event-stream parser.
  - Binary lookup: `$PI_BINARY` env → PATH `pi`.
  - **Done:** compiles, implements both interfaces, compile-time
    interface guards present.

- [x] **7. Test `PiDevHarness` against a fake binary.**
  - Parallel to TODO 3.
  - **Done:** new test file with the same case coverage.

- [ ] **8. Real-binary smoke for PiDevHarness against OpenRouter.**
  - Parallel to TODO 4.
  - **Done:** test green when prerequisites present, t.Skip
    otherwise.

### Phase 7.C — wire into the grid runner + CLI

- [ ] **9. Allow `opencode` and `pi_dev` in the grid CLI's
  `buildGridHarnesses`.**
  - File: `cmd/cortex/commands/eval_grid.go`.
  - Current state: rejects both with "deferred to TODOs 10/11"
    error. Replace with:
    ```go
    case evalv2.HarnessOpenCode:
        h, err := evalv2.NewOpenCodeHarness("", "")
        ...
    case evalv2.HarnessPiDev:
        h, err := evalv2.NewPiDevHarness("", "")
        ...
    ```
  - Update `printGridHelp()` to drop the "deferred" callout.
  - **Done:** `cortex eval grid --harnesses aider,opencode,pi_dev
    --help` works without errors.

- [ ] **10. Cross-harness smoke (gated).**
  - Requires `CORTEX_EVAL_ALLOW_SPEND=1` in env (even on free models,
    per the build-loop convention).
  - Run:
    ```
    CORTEX_EVAL_ALLOW_SPEND=1 CORTEX_EVAL_NO_FREE_PREFERENCE=1 \
    cortex eval grid \
      --scenarios test/evals/smoke \
      --harnesses aider,opencode,pi_dev \
      --models openai/gpt-oss-20b:free \
      --strategies baseline
    ```
  - **Done:** 3 cells complete, each with `TokensIn > 0`,
    `LatencyMs > 0`, no panics. Both SQLite and JSONL contain rows.
    `cortex eval grid --report` shows all 3.

- [ ] **11. Cross-harness divergence check on the coding scenarios.**
  - Same command shape as TODO 10 but with `--scenarios
    test/evals/coding` and `--strategies baseline`.
    15 cells total (5 scenarios × 3 harnesses).
  - **Done:** report shows per-harness pass rate; max divergence
    across harnesses on baseline is < 20 pp. If larger, investigate
    before flipping the box.

### Phase 7.D — MECE matrix update + doc

- [ ] **12. Update `docs/eval-resume-prompt.md`'s MECE matrix.**
  - The matrix's "harness" axis is currently implicit — Aider is
    the only harness, so it doesn't show as a separate dim. Phase 7
    adds a real harness dimension. Update the matrix to include it
    as a 6th dim, or as a sub-axis under shape (since some shapes
    use one harness, others use another).
  - Note in the resume prompt that Phase 7 landed and cross-harness
    ablation is now possible.
  - **Done:** matrix reflects the new dimension; resume prompt's
    "current state" table mentions cross-harness validation as
    available.

- [ ] **13. Stop the session.** All boxes checked. Print:
  - The 13-step record (commits landed).
  - Cross-harness smoke result (which harness x scenario combos
    passed).
  - Open follow-ups (e.g., if any harness had > 20 pp divergence,
    the cause; whether the user wants to re-run the $5 experiment
    with all 3 harnesses).
  - Total spend for the session.

---

## Stop conditions (halt the session, do not retry)

- A test in the gate command failed.
- `opencode` or `pi` is missing and a step needs it — emit a one-line
  install hint and stop.
- A step would require modifying `cellresult.go`'s JSON tags.
- A step would push to remote, open a PR, or run `cortex daemon`.
- A step would issue a paid OpenRouter call without
  `CORTEX_EVAL_ALLOW_SPEND=1`.
- A cross-harness divergence > 30 pp on baseline (TODO 11) — that's
  a real bug, not a wiring issue worth pressing through.
- More than 3 consecutive ticks have failed at the same step.

When stopping, leave a single short summary line explaining which
condition triggered and what the user needs to do.

---

## Anti-checklist (things to avoid)

- **Don't merge with `feat/measure-tooling`** until PR #7 lands.
  If you must start before #7 merges, branch from `feat/measure-tooling`
  and rebase onto main after #7 merges.
- **Don't copy AiderHarness verbatim.** opencode and pi.dev have
  different event shapes; copying the Aider parser will produce
  zero-token CellResults silently.
- **Don't skip the file-discovery step.** Aider's `--file` bug
  produced 30+ pp swings on first measurement; the same trap awaits
  in opencode/pi.dev if their `--dir` doesn't auto-add. Verify with
  a real probe before writing the parser.
- **Don't claim "harness divergence is fine"** when baseline pass
  rates differ by > 20 pp across harnesses on the same model. That's
  a wiring bug, not natural variation.
- **Don't add a third harness** in this session if either opencode
  or pi.dev runs into deep wiring issues. Cut to one + Aider for
  comparison and call it Phase 7.5.

---

## How to use this prompt

Paste the whole file as the first message of a fresh Claude Code
session. Suggested opening line:

> Read `docs/eval-harness-phase7-prompt.md` and start at TODO 1.
> Make sure opencode and pi.dev are installed first; if not, ask me
> which install command worked for me.

The fresh Claude will:
1. Verify both CLIs are on PATH (stop if missing).
2. Pick up the existing Harness / ResultfulHarness contracts.
3. Probe → implement → test → smoke for each harness.
4. Wire into the grid CLI.
5. Update the resume prompt's MECE matrix.
6. Print the session summary and stop.
