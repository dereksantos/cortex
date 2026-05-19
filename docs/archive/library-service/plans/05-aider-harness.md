# Plan 05 — AiderHarness

## Context

Plan 02 left a deliberate seam: `Harness` interface with one current
implementation (`ClaudeCLIHarness`). The thesis test for Cortex — making
small local models produce well-architected code — requires running
sessions through a model harness that supports local models. Claude CLI
is hardwired to Anthropic models (verified via `claude-code-guide`,
2026-05-04); Aider is the natural fit.

This plan adds an `AiderHarness` implementation that drives a session via
the `aider` CLI, configured to use an Ollama-served model. Once this
lands, the same eval pipeline that runs against Haiku via `ClaudeCLIHarness`
can run against `qwen2.5-coder:1.5b` (or any Ollama model) via
`AiderHarness`, and the comparison data finally tests the *actual*
small-model amplifier thesis.

## Scope

**In (MVP):**
- `AiderHarness` implementing the existing `Harness` interface
  (`RunSession(ctx, prompt, workdir) error`)
- Aider binary discovery via `AIDER_BINARY` env override + `PATH` lookup
  (mirroring the `CORTEX_BINARY` pattern)
- Non-interactive invocation: one prompt per session, no REPL loop
- Disable Aider's automatic git commits (the runner manages git itself)
- Driver flag: `cmd/library-eval/main.go` gains `--harness=claude|aider`
  (default `claude` for back-compat)
- Documented Ollama prerequisite

**Defer:**
- Other model backends (OpenAI-compat, Gemini via Aider) — Aider supports
  many; only Ollama matters for the thesis
- Auto-installing Aider or Ollama — the harness assumes both are present
- A "frontier ceiling via Aider" condition — Aider can drive Sonnet too,
  but we already have Sonnet via `ClaudeCLIHarness`. No need to dual-path.
- HOME isolation for Aider — its config is small and unlikely to interfere;
  revisit if test runs show contamination

## Approach

Mirror `ClaudeCLIHarness` structurally:

```go
type AiderHarness struct {
    binary string  // path to aider executable
    model  string  // e.g., "ollama/qwen2.5-coder:1.5b"
}

func NewAiderHarness(binary, model string) (*AiderHarness, error) { ... }
func (h *AiderHarness) RunSession(ctx context.Context, prompt, workdir string) error { ... }
```

Invocation shape (verify exact flags by running `aider --help`):

```
aider \
  --model <model> \
  --message <prompt> \
  --yes-always \
  --no-auto-commits \
  --no-stream \
  --no-show-model-warnings \
  --no-pretty
```

Cwd = workdir. Aider should pick up workdir files automatically (its
default behavior is to scan for relevant files); explicit file listing is
not needed for MVP.

## Files to create / modify

| Path | Purpose |
|---|---|
| `internal/eval/v2/library_service_aider_harness.go` | `AiderHarness`, `resolveAiderBinary`, invocation helpers |
| `internal/eval/v2/library_service_aider_harness_test.go` | Fake-aider-via-shell-script tests |
| `cmd/library-eval/main.go` | New `--harness=claude|aider` flag; harness construction switches on it |
| (optional) `test/evals/library-service/plans/05-aider-harness.md` | This plan, may be polished |

## Implementation steps

1. **Verify Aider's actual CLI surface.** Run `aider --help` to confirm
   the flags suggested above. Look specifically for: `--message`,
   `--yes-always` (or `--yes`), `--no-auto-commits`, `--no-stream`,
   `--no-show-model-warnings`, exit-after-message behavior. **Adjust the
   invocation to match what Aider actually accepts in the installed
   version.** Don't trust the suggested flag names blindly.

2. **Implement `resolveAiderBinary`** following the `resolveCortexBinary`
   pattern (env-var override → PATH lookup → error). Use `AIDER_BINARY`.

3. **Implement `NewAiderHarness`** with model validation: if the model
   string doesn't start with `ollama/` (Aider's convention for Ollama
   models), accept it but log a warning. The thesis target is Ollama
   models; other backends work but aren't the point.

4. **Implement `RunSession`**:
   - Build the command with the agreed flag set
   - `cmd.Dir = workdir`, `cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}`
   - Capture stdout (for diagnostics) and stderr separately
   - Honor `ctx`: on cancel, SIGTERM the process group with 2s grace then
     SIGKILL — same pattern as `ClaudeCLIHarness` and the e2e helper
   - On non-zero exit, return an error wrapping the stderr output

5. **Aider's git interaction.** Aider auto-commits by default. The runner
   already calls `git commit -m "session NN"` after each session. If Aider
   commits too, we end up with extra commits in the per-session diff.
   `--no-auto-commits` should disable this. Verify in a test run that
   `git log --oneline` after a session shows only the runner's commit.

6. **Modify the driver** (`cmd/library-eval/main.go`):
   - New `--harness=claude|aider` flag (default `claude`)
   - New `--aider-model` flag (default empty; if `--harness=aider` and
     this is empty, fall back to `--model` value)
   - When `--harness=aider`, wire `AiderHarness` instead of `ClaudeCLIHarness`
   - The driver's existing `--model` flag stays the canonical one;
     `--aider-model` is just a convenience for the common Ollama case

   Note: this requires the eval's `Run()` to accept a custom Harness, not
   construct one internally. Check what Plan 02 wired — `RunWithHarness`
   exists for exactly this. The driver can call `RunWithHarness` directly
   for the harness=aider path. (Or, cleaner, add a constructor on the
   evaluator that takes a Harness factory function — implementer's
   judgment.)

7. **Tests** (fake-aider-via-shell-script, mirroring the pattern in
   `library_service_inject_test.go`):
   - Happy path: fake aider writes a file via shell, returns 0
   - Missing binary: `NewAiderHarness("/no/such/path", ...)` returns error
   - Non-zero exit: fake aider exits 1 with stderr output; harness wraps
     and surfaces it
   - Context cancellation: fake aider sleeps; ctx cancel terminates it
     within reasonable time

   No test should invoke real Aider in the default `go test` run. Same
   discipline as Plans 02–04.

## Specific gotchas

1. **Ollama must be running.** `aider --model ollama/...` will fail
   immediately if Ollama isn't accepting connections at `127.0.0.1:11434`.
   Document this in the harness's package comment. Don't try to start
   Ollama from the harness — that's an environment concern.

2. **Model must be pulled first.** `ollama pull qwen2.5-coder:1.5b` is a
   prerequisite. Document; don't auto-pull.

3. **Aider config discovery.** Aider reads `.aider.conf.yml` from cwd,
   home, and parent dirs. The user's personal config might inject flags
   we don't want (verbose mode, alternative model, dark theme, etc.).
   For MVP, accept this; for thesis runs, consider running with HOME
   isolation (same pattern as `CortexInjector`) to get a clean Aider
   environment.

4. **Aider's tool-use is search-replace, not function-call.** Small
   models can drive search-replace edits much better than they can drive
   function-call tool use, which is *the entire reason* Aider exists for
   this use case. This is a feature, not a bug — but if you ever try to
   compare Aider+model X to Claude+model X, the comparison includes the
   tool-use protocol delta, not just the model.

5. **Latency with local Ollama is significant.** Plan 02 estimated 5–15
   min per session for Claude CLI. Ollama with qwen2.5-coder:1.5b on a
   M-series Mac is probably 15–45 min per session. A full 5-session run
   could easily be 2-4 hours. **Plan accordingly** — don't kick off
   speculatively.

6. **Aider may want to upload diffs to its telemetry endpoint** unless
   disabled. Check `--analytics-disable` (or whatever the current flag
   is). Eval runs should not phone home with code samples.

## Verification

- Unit tests pass: `go test -race ./internal/eval/v2/...` green
- Smoke test (manual, integration-tagged): with Ollama running and
  `qwen2.5-coder:1.5b` pulled, invoke `library-eval --harness=aider
  --model=ollama/qwen2.5-coder:1.5b --cond=baseline` against a single
  session (or the full 5). Confirm the workdir gets populated and
  `git log` shows only the runner's commits.
- The driver's existing `--harness=claude` path still works — Plan 02's
  `ClaudeCLIHarness` behavior unchanged.

## Definition of done

  - [ ] `AiderHarness` implements `Harness` interface
  - [ ] `resolveAiderBinary` follows the `resolveCortexBinary` pattern
        with `AIDER_BINARY` env override
  - [ ] Invocation flags verified against the installed Aider version
        and documented in the harness's source
  - [ ] `--no-auto-commits` (or equivalent) confirmed to suppress
        Aider's git activity
  - [ ] Driver `--harness=claude|aider` flag works; default is `claude`
  - [ ] Unit tests with fake aider cover: happy path, missing binary,
        non-zero exit with stderr surfaced, ctx cancel
  - [ ] No real Aider in default `go test`
  - [ ] Package comment documents Ollama prerequisite + model pull
        prerequisite
  - [ ] Manual smoke test against real Aider + Ollama produces a
        populated workdir for at least S01

## Suggested next plan

After AiderHarness lands and passes a manual smoke test, the actual
thesis run becomes possible:

```
env -u ANTHROPIC_API_KEY \
  CORTEX_BINARY=/tmp/cortex \
  /tmp/library-eval \
    --harness=aider \
    --model=ollama/qwen2.5-coder:1.5b \
    --cond=both \
    --repo=$REPO_ROOT
```

Expected wall-clock: 4-8 hours. Cost: free (local model). Result: actual
thesis data on whether Cortex amplifies a 1.5B-param local model.
