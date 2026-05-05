# Fresh-session prompt — Plan 03 (Cortex injection / condition fork)

Copy the block below verbatim as the opening message of a fresh Claude Code session.

---

You are implementing Plan 03 (Cortex injection — the condition fork) for the
library-service multi-session eval. This is **the thesis test for Cortex**.
After this plan lands, the eval can produce real A-vs-B comparison data
showing whether Cortex actually lifts cohesion across sessions or not.

A negative result (Cortex condition does not beat baseline) is **valid data,
not a bug**. It motivates building tier-2 dynamic mining. Your job is to make
the comparison work, not to make Cortex "win." Be honest with the numbers.

## Read these first, in order

1. `test/evals/library-service/plans/03-cortex-injection.md`        — the plan
2. `test/evals/library-service/SPEC.md`                              — eval design
3. `internal/eval/v2/library_service.go`                             — `Run`,
                                                                       `RunWithHarness`, `Harness` interface, `LibraryServiceRun.Cleanup`
4. `internal/eval/v2/library_service_runner.go`                      — what's
                                                                       already wired (sessions loop, workdir, git, baseline)
5. `cmd/cortex/commands/ingest.go`                                   — `cortex capture` (line ~50) and `cortex feed` (line ~392)
6. `cmd/cortex/commands/query.go`                                    — `cortex search` (line ~32)
7. `cmd/cortex/commands/session.go`                                  — `cortex inject-context` (line ~73)
8. `pkg/registry/registry.go`                                        — how Cortex
                                                                       resolves the global directory (the `~/.cortex/` it reads/writes)
9. **Direction memory** in user memory: `project_direction_small_model_amplifier.md`
                                                                     — context on the thesis you're testing.

Run `cortex --help` to see the full CLI surface. The relevant subcommands are
`capture`, `feed`, `search`, `inject-context`. None of them have to be perfect
— you're using them as-is.

## What's already done — DO NOT redo

- Plan 01 (scorer) shipped at `1a55ca1`. All five MVP metrics implemented.
- Plan 02 (runner) shipped at `aef4bcc`. `RunWithHarness` is the seam you'll
  build on. `Harness` interface is `RunSession(ctx, prompt, workdir) error`
  and stays unchanged.
- Plan 04 (e2e) shipped at `2eb6e90`. `EndToEndPassRate` populated by
  `Score()`.
- The runner copies `system-spec.md` into the workdir on setup and commits it
  before any session runs. Per-session diffs already exclude it.

## What you are building

The condition fork: when `cond == ConditionCortex`, the runner consults a
Cortex-backed `Injector` to (a) prepend a "previously-established conventions"
preamble to each session prompt after S1, and (b) record the session's output
into Cortex's pipeline so the next session has something to retrieve.

When `cond == ConditionBaseline`, the existing path is unchanged. When
`cond == ConditionFrontier`, also baseline (no injection) — frontier is just
"baseline with a bigger model."

## Critical design constraints

1. **`Injector` interface** lives in a new file. Defined as:

   ```go
   type Injector interface {
       // Preamble returns markdown to prepend to the next session's prompt.
       // For S1 (sessionIdx == 0), returns "" — there's nothing to mine yet.
       Preamble(ctx context.Context, sessionIdx int, workdir string) (string, error)

       // Record captures what the model did in the just-completed session.
       // No-op for baseline; feeds Cortex's capture pipeline for the cortex
       // condition.
       Record(ctx context.Context, sessionIdx int, workdir string, result SessionResult) error
   }
   ```

   Two implementations:
   - `noopInjector{}` — both methods return zero values immediately (used by
     baseline and frontier conditions)
   - `cortexInjector` — invokes `cortex` CLI subcommands

2. **Inject at the runner level, not the harness level.** The `Harness`
   interface stays as-is. Add a `RunWithInjector(ctx, harness, injector)`
   method (or extend `RunWithHarness` with an optional injector parameter
   defaulting to `noopInjector{}`). The runner is what wraps prompts and
   records results; the harness stays Cortex-ignorant.

3. **Per-condition state isolation is mandatory.** Baseline and cortex runs
   must never share Cortex state, or comparison results are corrupted.
   - Each `cortexInjector` gets its own Cortex global dir at
     `${workdir}/.cortex-state/` (or a sibling tempdir).
   - Set the appropriate environment variable when invoking `cortex` CLI
     subcommands so they read/write that dir, not `~/.cortex/`. **Look at
     `pkg/registry/registry.go` and `pkg/config/config.go` to find what
     variable controls this.** Likely `CORTEX_GLOBAL_DIR` or similar — do
     not guess; read the code.
   - Verify isolation in a test: spin up two `cortexInjector`s with
     different state dirs, capture different events into each, confirm
     `Preamble` from injector A doesn't see events from injector B.

4. **Verbose logging is mandatory.** When `LibraryServiceEvaluator.verbose`
   is true:
   - Log the preamble that was prepended to each session prompt
   - Log what was captured (event types, file count, byte count)
   - Log the search/inject-context query and the raw result before
     wrapping it in markdown

   This is not optional. The first question when results don't move is
   "what did Cortex actually inject?" Make that visible.

5. **The query is a starting hypothesis, not a fixed contract.** The plan
   suggests something like *"what conventions are in use in this Go HTTP
   service for handlers, error handling, response shape, validation,
   tests"* but you can iterate. Document whatever you choose in a comment
   so the next person knows it's tunable. Don't hide it inside a string
   constant with no rationale.

6. **What to record per session.** After session N completes:
   - `git diff --name-only HEAD~1 HEAD` (the runner already does this)
   - For each changed `.go` file, capture its content as a Cortex event.
     Use `cortex capture --type=code --content=<...>` or whatever the
     existing `cortex capture` interface accepts. Read `ingest.go` to
     find the right shape.
   - Optional: also capture a per-session summary event (`type=session`)
     describing what the session was supposed to do. Skip if it
     complicates things; signal-over-noise is fine for MVP.

## Specific gotchas

1. **Cortex CLI binary path.** The `cortex` binary needs to be findable.
   Either build it as a test prerequisite (`go build -o /tmp/cortex
   ./cmd/cortex` in test setup), or accept a `cortex` binary path on the
   `cortexInjector`. Don't rely on `$PATH` having the right cortex —
   developer machines may have stale installs.

2. **Cortex daemon dependency.** Some Cortex commands assume the daemon is
   running. `cortex search` likely works without it (reads JSONL/SQLite
   directly), but `cortex capture` queues for the daemon to ingest. You
   may need to run `cortex ingest` synchronously after capture, or run a
   short-lived daemon per condition. Read the existing flow in
   `cmd/cortex/commands/ingest.go` and `daemon.go` to decide.

3. **Cortex output format may not match what an LLM wants.** `cortex
   search` returns formatted text suitable for terminal display. Wrap it
   sensibly in your preamble — strip ANSI, prefix with context ("These
   patterns are in use in prior sessions of this codebase. Match them in
   your implementation."), maybe truncate if it's huge.

4. **Don't try to build tier-2 mining or tier-3 AST detection.** That is
   explicitly out of scope. The MVP uses what Cortex already does today.
   If retrieval results are weak, that's the data motivating tier-2 work
   — don't preempt the verdict by building tier-2 inline.

5. **A real comparison run is expensive.** 5 sessions × 5-15 min × 2
   conditions = 50-150 min of Claude CLI time per comparison. **Do not
   run a real comparison as part of `go test ./...`.** Gate any
   real-Cortex integration test behind `//go:build integration`.

6. **Frontier condition.** Same as baseline — no injector. Don't try to
   make `ConditionFrontier` use Cortex. The condition exists to measure
   the upper bound for the model dimension, not the Cortex dimension.

## Definition of done

  - [ ] `Injector` interface defined; `noopInjector` and `cortexInjector`
        both implemented
  - [ ] Runner integration: `RunWithInjector` (or equivalent) so the
        runner consults the injector before/after each session
  - [ ] `Run` selects the right injector based on `cond`
        (baseline+frontier → noop; cortex → cortexInjector with isolated
        state dir)
  - [ ] Per-condition state isolation works — verified by a unit test
        that confirms two `cortexInjector`s with different state dirs
        don't see each other's events
  - [ ] Verbose mode logs the preamble, the capture summary, and the
        search query+result
  - [ ] `CompareRuns(baseline, cortex, frontier)` produces a markdown
        report per `SPEC.md` "Pass criteria" — at minimum a side-by-side
        table of the five score fields with deltas. The "lift" semantics
        are: cortex.ShapeSimilarity − baseline.ShapeSimilarity.
  - [ ] Unit tests with a fake `cortex` binary (a small shell script in
        a tempdir) covering: preamble retrieval, capture invocation,
        state isolation, error propagation
  - [ ] Documentation block on `cortexInjector` explains the query
        choice and what's tunable
  - [ ] `go test -race ./internal/eval/v2/...` green
  - [ ] No real Cortex CLI invocations in the default test suite

## What I do NOT want

- Tier-2 dynamic mining of any kind (separate work stream)
- Tier-3 AST detection (separate work stream)
- MCP server integration (deferred — only matters when the model can call
  Cortex on demand, not for prompt injection)
- A "smart" injector that does its own embedding or reranking (use
  Cortex as-is)
- Modifications to existing Cortex commands or pipeline (the eval is the
  consumer, not the producer)
- Real comparison runs in CI

## Scope discipline

- Don't modify `rubric.md`, `SPEC.md`, session prompts, fixtures, or any
  scorer file
- Don't modify the existing `Harness` interface (add a new layer above it)
- Don't modify Plan 02's runner internals beyond adding the injector seam
- Don't add new top-level dependencies — `os/exec` for invoking the
  cortex binary is enough
- Standard library testing only

## Suggested first commands

    cd $REPO_ROOT
    cat test/evals/library-service/plans/03-cortex-injection.md
    cortex --help                                  # see the CLI surface
    grep -n "GLOBAL_DIR\|ContextDir\|GlobalDir" pkg/registry/registry.go pkg/config/config.go
    cat cmd/cortex/commands/ingest.go | grep -A 30 "CaptureCommand"
    cat cmd/cortex/commands/query.go | grep -A 30 "SearchCommand"
    go test ./internal/eval/v2/...                 # baseline: should be green

When done:

    go test -race ./internal/eval/v2/...

Then propose a commit message and stop. The user will review before
committing. After this lands, **do not** auto-trigger a real comparison run
— that's a separate decision the user makes deliberately given the cost.
