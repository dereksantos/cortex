# Fresh-session prompt — Plan 02 (session runner)

Copy the block below verbatim as the opening message of a fresh Claude Code session.

---

You are implementing Plan 02 (session runner) for the library-service
multi-session eval in this repo.

## Read these first, in order

1. `test/evals/library-service/plans/02-session-runner.md`        — the plan
2. `test/evals/library-service/SPEC.md`                            — eval design,
                                                                     conditions, pass criteria
3. `test/evals/library-service/sessions/01-scaffold-and-books.md`  — and skim
   `02-authors.md` through `05-branches.md`. These are what your runner feeds
   to the model. Do NOT modify them.
4. `internal/eval/v2/library_service.go`                           — `Run()` is
   the function you fill in; types `LibraryServiceRun` and `SessionResult` are
   already defined.
5. `internal/eval/v2/agentic.go`                                   — the existing
   eval pattern that uses `pkg/llm.ClaudeCLI`. **This is your reference
   implementation for invoking the model.**
6. `pkg/llm/` (browse)                                             — find the
   `ClaudeCLI` type. Confirm how it discovers the `claude` binary on PATH and
   how it accepts a prompt + working directory.

## What's already done — DO NOT redo

- Plan 01 (scorer) shipped at `1a55ca1`. All four MVP metrics implemented and
  calibrated.
- Plan 04 (e2e) shipped at `2eb6e90`. `EndToEndPassRate` populated by
  `Score()`. `LibraryServiceScore` is now fully populated end-to-end.
- After your runner produces a finished workdir, callers do:
  `score, err := evaluator.Score(ctx, run.WorkDir)`. Your `Run` populates
  `run.Score` by calling this internally after S5 completes.

## What you are NOT doing — Plan 03 territory

- **Do not inject any "previously-established conventions" preamble into
  session prompts.** The prompts already tell the model to "look at existing
  code and match conventions." That's the baseline. Plan 03 (Cortex
  injection) is what later prepends Cortex's retrieved patterns. Your runner
  should not know anything about Cortex.
- **Do not call `cortex capture`, `cortex search`, `cortex inject-context`,
  or set `CORTEX_GLOBAL_DIR`.** Same reason — that's Plan 03's surface area.
  Your `Harness` interface is what Plan 03 will hook into.
- **Do not build an Ollama / Aider harness.** Plan 02 ships Claude-CLI-only.
  Document the future-Harness-implementation slot via the `Harness` interface;
  that's enough.

## Critical design constraints (from the plan)

1. **`Harness` interface**: define it in `library_service.go` so Plan 03 can
   add a `CortexInjectingHarness` later without touching the runner.

   ```go
   type Harness interface {
       RunSession(ctx context.Context, prompt string, workdir string) error
   }
   ```

   Implementations: `ClaudeCLIHarness` (now), `OllamaAgentHarness` (later,
   stub only or omit entirely).

2. **Workdir lifecycle**:
   - Copy `test/evals/projects/library-service-seed/` → `${TMPDIR}/cortex-libsvc-${cond}-${ts}/`
   - `git init`, `git add .`, `git commit -m "seed"` so per-session diffs are
     trivial via `git diff --name-only HEAD~1 HEAD`
   - **Do NOT delete the workdir on success.** `Score()` is called against it
     after the run. Leave cleanup to the caller (or a `Cleanup()` method on
     `LibraryServiceRun`).
   - Path to workdir goes in `run.WorkDir`.

3. **Session loop**:
   - For each session 01..05 in order:
     a. Read the prompt from `sessions/NN-*.md`
     b. `start := time.Now()`
     c. `harness.RunSession(ctx, prompt, workdir)` — Claude CLI handles the
        Edit/Write tool loop natively; you just give it prompt + cwd
     d. `duration := time.Since(start)`
     e. `git diff --name-only HEAD` → `FilesChanged`
     f. `go build ./...` in workdir → `BuildOK`
     g. `go test ./...` in workdir → `TestsOK`
     h. `git add . && git commit -m "session NN"` (allow empty if model
        produced nothing — `git commit --allow-empty`)
     i. Append `SessionResult` to `run.SessionLog`
   - After S5: call `Score()` against the workdir, populate `run.Score`,
     return.

4. **Failure modes**:
   - Claude CLI binary missing or model unreachable → **hard error**, abort
     the run, return wrapped error.
   - Build fails or tests fail after a session → record `BuildOK`/`TestsOK`
     as `false`, continue to next session. The diverged baseline is allowed
     to leave broken code; that's data.
   - Context cancellation → respect it; clean up running subprocesses.

5. **Conditions in this plan**: only `ConditionBaseline`. `ConditionCortex`
   and `ConditionFrontier` should return a clear "not implemented yet" error
   (or the same as baseline if you want — Plan 03 will fork properly).
   Don't try to make them work; that's Plan 03.

## Specific gotchas

1. **Claude CLI argument shape**: check `agentic.go` and `pkg/llm/` for the
   exact invocation. The existing pattern likely uses `claude code -p
   "<prompt>"` or `claude -p "<prompt>"` with `--cwd` or by setting
   `cmd.Dir`. Match what's already there — don't invent a new style.

2. **Long-running sessions**: a single session may take 5-15 minutes for the
   model to read the existing code, plan, and produce edits. **Do NOT set
   short timeouts** on the harness call. Use `ctx` for cancellation, not
   per-call timeouts.

3. **Subprocess cleanup**: if your harness shells out (it will, via Claude
   CLI), follow the `library_service_e2e.go` pattern for clean reaping
   (`Setpgid`, two-tier SIGTERM/SIGKILL).

4. **Verbose mode**: `LibraryServiceEvaluator` has a `verbose bool` field.
   When true, log per-session start/end with one line each ("S03 loans:
   started"; "S03 loans: 4m12s, 8 files changed, build=ok tests=ok"). When
   false, run silent except for hard errors.

5. **Git operations**: use `os/exec` to call `git` directly. Don't pull in a
   git library. Each call gets `cmd.Dir = workdir`.

6. **`run.Model`**: pass `model` through to the harness if applicable. For
   Claude CLI, this might be a flag like `--model sonnet`. Check existing
   usage in `agentic.go`.

## Definition of done

  - [ ] `Harness` interface defined; `ClaudeCLIHarness` implemented
  - [ ] `(*LibraryServiceEvaluator).Run` returns a populated
        `LibraryServiceRun` with all 5 `SessionResult`s and a populated
        `Score` for the baseline condition
  - [ ] Workdir setup uses git for per-session diffing
  - [ ] Workdir is left intact on success (caller can Score it, then clean)
  - [ ] Verbose mode logs per-session progress; non-verbose is silent
  - [ ] Hard errors (binary missing, model unreachable) abort cleanly with
        wrapped error
  - [ ] Per-session failures (build/test broken) get recorded but the run
        continues
  - [ ] Unit tests with a fake `Harness` covering: happy path (5 sessions
        succeed), per-session build failure recorded, hard error from
        harness aborts, context cancellation handled
  - [ ] Documentation block at the top of `library_service.go`'s `Run`
        updated to reflect what's implemented vs Plan 03's responsibilities
  - [ ] `go test -race ./internal/eval/v2/...` green

## What I do NOT want

- An integration test that actually invokes Claude CLI as part of the normal
  `go test ./...` run. That's slow, requires the binary, and costs API
  credits. Add a `//go:build integration` tagged smoke test the user can run
  manually if you want, but the default test suite must stay deterministic
  and offline.

## Scope discipline

- Don't modify `rubric.md`, `SPEC.md`, or any session prompt file
- Don't touch `score_*.go` files
- Don't touch `library_service_e2e.go` or its test
- Don't add new top-level dependencies
- Standard library testing only (no testify) — hard project constraint per
  `CLAUDE.md`

## Suggested first commands

    cd /Users/dereksantos/eng/projects/cortex
    cat test/evals/library-service/plans/02-session-runner.md
    cat internal/eval/v2/agentic.go | head -100   # see existing ClaudeCLI usage
    grep -rn "ClaudeCLI" pkg/llm/ internal/        # find the type and existing call sites
    go test ./internal/eval/v2/...                  # baseline: should be green

When done, run:

    go test -race ./internal/eval/v2/...

Then propose a commit message and stop. The user will review before
committing.
