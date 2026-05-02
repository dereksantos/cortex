# Plan 02 — Session Runner

## Context

The eval needs to actually drive a model through each of the five session prompts. The model must be able to read existing files in the workdir, write/edit code, and the runner must capture: which files changed, did `go build` pass, did `go test` pass, how long did the session take.

This fills `(*LibraryServiceEvaluator).Run` in `internal/eval/v2/library_service.go`.

**Depends on:** nothing structural, but Plan 01 (scorer) is what consumes the runner's output. Plan 03 (Cortex injection) plugs *into* this runner.

## Scope

**In (MVP):**
- Claude CLI as the model harness (already used by `internal/eval/v2/agentic.go`)
- Single-condition runs: just baseline, no Cortex injection (Plan 03 layers that on)
- Session orchestration: copy seed → workdir, run sessions in order, capture results
- Result capture: per-session file changes, build/test exit codes, duration

**Defer:**
- Ollama / Aider as alternative harnesses → these need a tool-use loop wrapper. Document the path, leave a stub interface.
- Parallel session runs → sessions must be sequential by design (each reads the previous one's output).
- Resuming partial runs → not needed for MVP; on failure, restart from S1.

## Approach

Reuse the `pkg/llm.ClaudeCLI` pattern from `agentic.go`. Each session is one Claude CLI invocation:

```
claude code -p "<session prompt content>" --cwd <workdir>
```

Claude CLI handles the Edit/Write tool loop internally, so we don't need to build an agent loop ourselves. We just give it a prompt and a working directory, then check what changed.

Per-session result capture is via `git` inside the workdir: init a git repo when copying the seed, commit after each session, diff between commits to know what each session changed.

## Files to create / modify

| Path | Purpose |
|---|---|
| `internal/eval/v2/library_service_runner.go` | Runner implementation: workdir setup, session loop, result capture |
| `internal/eval/v2/library_service_runner_test.go` | Unit tests; uses a fake `Harness` interface |
| `internal/eval/v2/library_service.go` | Replace `Run()` stub with runner call; add `Harness` interface |

## Implementation steps

1. **Define the `Harness` interface** in `library_service.go`:

   ```go
   type Harness interface {
       // RunSession invokes the model with the prompt against workdir.
       // The model is expected to edit files in workdir directly.
       RunSession(ctx context.Context, prompt string, workdir string) error
   }
   ```

   Two impls eventually: `ClaudeCLIHarness` (now), `OllamaAgentHarness` (later).

2. **Implement `ClaudeCLIHarness`** wrapping `pkg/llm.ClaudeCLI`. Construct with the binary path. The wrapper just sets cwd and passes the prompt.

3. **Implement workdir setup**:
   - Copy `test/evals/projects/library-service-seed/` → `${TMPDIR}/cortex-libsvc-${condition}-${timestamp}/`
   - `git init` in workdir, `git add . && git commit -m "seed"` so we have a baseline to diff against
   - Return workdir path

4. **Implement session loop** in `(*LibraryServiceEvaluator).Run`:
   ```
   for each session in 01..05:
     read prompt from sessions/NN-*.md
     start := now()
     err := harness.RunSession(ctx, prompt, workdir)
     duration := now() - start
     filesChanged := git diff --name-only HEAD
     run go build ./...; capture exit code
     run go test ./...; capture exit code
     git add . && git commit -m "session NN"
     append SessionResult to run.SessionLog
     if buildOK and testsOK are both false, optionally bail early (config flag)
   ```

5. **After all sessions**: call `Score()` to populate `run.Score`, return the run.

6. **Error handling**: any session failure (build broken AND tests broken) is recorded but does NOT stop the eval — the diverged baseline is allowed to leave broken code, and that's data. Hard errors (model unreachable, disk full) do stop the eval.

7. **Logging**: print per-session output if `verbose` is set; otherwise just a one-line summary per session.

## Verification

- Unit test with a fake `Harness` that writes a fixed file per session: verify the session log captures all 5, files-changed lists are correct, build/test results recorded.
- Smoke test against real Claude CLI on a single session (S1 only): verify a workdir gets populated and `go build` passes. Mark this test with a build tag `//go:build integration` so it doesn't run in normal CI.
- Run a full 5-session baseline against Claude CLI manually; confirm the workdir at the end has 5 resources implemented.

## Definition of done

- [ ] `Harness` interface defined; `ClaudeCLIHarness` implemented
- [ ] `Run()` returns a populated `LibraryServiceRun` with all 5 `SessionResult`s
- [ ] Workdir setup uses git for per-session diffing
- [ ] Unit tests pass with fake harness
- [ ] Manual integration smoke test produces a working library-service after S5
- [ ] Build and test results captured per session

## Suggested next plan

Plan 03 (Cortex injection) — wires the with-Cortex condition on top of this runner.
