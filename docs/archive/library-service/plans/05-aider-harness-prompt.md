# Fresh-session prompt — Plan 05 (AiderHarness)

Copy the block below verbatim as the opening message of a fresh Claude Code session.

---

You are implementing Plan 05 (AiderHarness) for the library-service
multi-session eval. This unblocks the actual small-model amplifier
thesis test by adding a harness that drives sessions through Aider with
an Ollama-served local model (e.g., qwen2.5-coder:1.5b), instead of
through Claude CLI which is hardwired to Anthropic models.

## Read these first, in order

1. `test/evals/library-service/plans/05-aider-harness.md`             — the plan
2. `internal/eval/v2/library_service_runner.go`                       — the
                                                                        existing `Harness` interface and `ClaudeCLIHarness` you're mirroring
3. `internal/eval/v2/library_service.go`                              — `RunWithHarness`
                                                                        is what the driver will call for the aider path
4. `internal/eval/v2/library_service_inject.go`                       — pattern
                                                                        for `resolveCortexBinary` + `CORTEX_BINARY` env override; mirror it
5. `cmd/library-eval/main.go`                                         — driver
                                                                        you'll add `--harness=claude|aider` to
6. **Run `aider --help`** — confirm the flags actually accepted by the
   installed Aider version. Don't trust the suggested flag names in the
   plan blindly. Adjust to reality.

## What's already done — DO NOT redo

- Plans 01, 02, 03, 04 all shipped (commits 1a55ca1, aef4bcc, 2eb6e90,
  9ea8c6d). Eval pipeline runs end-to-end against `ClaudeCLIHarness`.
- `Harness` interface is `RunSession(ctx, prompt, workdir) error` and
  stays unchanged. You add a second implementation alongside the
  existing `ClaudeCLIHarness`, you don't modify the interface.
- `RunWithHarness` accepts any `Harness` and is the seam.
- The driver at `cmd/library-eval/main.go` currently constructs
  `ClaudeCLIHarness` internally; you'll either factor that out or add a
  branch on a new `--harness` flag (judgment).

## Critical constraints

1. **Don't modify the `Harness` interface.** A second implementation is
   the whole point of the existing seam. Touching the interface would
   break Plan 02's tests and ClaudeCLIHarness without need.

2. **Don't auto-install Aider or Ollama.** Both are environment
   prerequisites. The harness assumes they exist. Document the
   requirement in the package comment; fail fast with a clear error if
   the binary is missing.

3. **Don't modify Plan 02–04 code beyond minimal driver changes.**
   AiderHarness is purely additive: a new file in `internal/eval/v2/`,
   a new test file, and a small driver tweak.

4. **No real Aider invocations in the default `go test ./...` run.**
   Same discipline as the previous plans. Use a fake-aider-via-shell-
   script pattern (see `internal/eval/v2/library_service_inject_test.go`
   for the cortex-fake pattern — copy it).

5. **Subprocess lifecycle must mirror `ClaudeCLIHarness`.** Setpgid,
   SIGTERM with 2s grace then SIGKILL on ctx cancel, stderr captured
   and surfaced in errors. Aider sessions can be 15-45 min long; do not
   set per-call timeouts, rely on ctx.

## Specific gotchas

1. **Aider auto-commits to git by default.** The runner already commits
   per-session. If Aider also commits, you get duplicate commits in the
   per-session diff. The flag is probably `--no-auto-commits` — verify.
   After implementation, manually run a session and `git log --oneline`
   should show *only* the runner's commits.

2. **Aider has a confirmation prompt for shell tool calls.** Use
   `--yes-always` (or whatever the current flag is — verify) to skip
   them. Without this, Aider will block waiting for input and the
   session will hang until ctx cancel.

3. **Ollama must be running and the model must be pulled.** Document the
   prerequisites:
   ```
   ollama pull qwen2.5-coder:1.5b
   ollama serve   # if not already running
   ```
   The harness should produce a clear error if Ollama isn't reachable
   (Aider itself will surface this; just propagate the stderr).

4. **Aider config files** (`.aider.conf.yml`) get picked up from cwd,
   home, and parent dirs. The user's personal config might inject
   unwanted flags. For MVP, accept this risk; document it. HOME-isolation
   (same pattern as `CortexInjector`) can be a follow-up if it bites.

5. **Aider may have telemetry/analytics on by default.** Check
   `--analytics-disable` or similar. Eval runs should not phone home
   with code samples.

6. **Latency.** Plan estimates 15-45 min per session for qwen2.5-coder
   on M-series Mac. A full 5-session run is 2-4 hours. Build accordingly
   — don't kick off speculatively in CI.

## Definition of done

  - [ ] `AiderHarness` in new file `internal/eval/v2/library_service_aider_harness.go`
  - [ ] Implements `Harness` interface; structurally mirrors `ClaudeCLIHarness`
  - [ ] `resolveAiderBinary` with `AIDER_BINARY` env override + PATH
        lookup
  - [ ] Invocation flags verified by running `aider --help` and noted
        in source
  - [ ] `--no-auto-commits` (or equivalent) confirmed to suppress
        Aider's git activity (manual smoke test required)
  - [ ] Driver gains `--harness=claude|aider` flag (default `claude`)
  - [ ] Unit tests with fake aider cover: happy path, missing binary,
        non-zero exit with stderr surfaced, ctx cancel
  - [ ] Package comment documents the Ollama prerequisites
  - [ ] `go test -race ./internal/eval/v2/...` green
  - [ ] Manual smoke test against real Aider + Ollama produces a
        populated workdir for at least S01 (gated, not in default
        `go test`)

## Scope discipline

- Don't modify rubric, SPEC, session prompts, fixtures, or scorer
- Don't modify `Harness` interface
- Don't modify `ClaudeCLIHarness` or its tests
- Don't modify `CortexInjector` or its tests
- Don't add new top-level deps — `os/exec` is enough
- Standard library testing only

## Suggested first commands

    cd $REPO_ROOT
    cat test/evals/library-service/plans/05-aider-harness.md
    aider --help                                    # verify the actual flag set
    which aider                                     # confirm installed
    ollama list 2>/dev/null || echo "ollama not running"
    grep -A 30 "ClaudeCLIHarness\b" internal/eval/v2/library_service_runner.go
    go test ./internal/eval/v2/...                  # baseline: should be green

When done:

    go test -race ./internal/eval/v2/...

Then propose a commit message and stop. The user will review before
committing. After this lands, **do not** auto-trigger a real Aider+Ollama
run — that's a 2-4 hour, locally-burned-cycles operation the user
schedules deliberately.
