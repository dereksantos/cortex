# Plan 03 — Cortex Injection (Condition Fork)

## Context

This plan implements the actual *with-Cortex* condition. Plan 02's runner runs sessions in a workdir; this plan adds the difference between baseline (model alone) and Cortex (model + Cortex's captured patterns from prior sessions injected into the next session's prompt).

This is the eval's actual *test*: does Cortex make S2–S5 follow S1's conventions better than baseline? If this plan's implementation doesn't produce a measurable lift in shape similarity, **the small-model amplifier thesis is wrong** as currently specified, and that's the most valuable result the eval can return.

**Depends on:** Plan 02 (runner) and Plan 01 (scorer).

**Note on tier-2/3:** The committed direction (`project_direction_small_model_amplifier.md`) calls for tier-2 dynamic mining and tier-3 AST detection. Neither exists yet. This plan uses **what Cortex already does today** (capture session events, embed, search, inject context) and accepts that results may be modest. If they are, that motivates building tier-2/3.

## Scope

**In (MVP):**
- Two condition implementations: `baseline` (no injection) and `cortex` (injection via existing pipeline)
- Use existing `cortex capture` to record session events; existing `cortex search`/`cortex inject-context` to retrieve before the next session
- Session-isolation via per-condition Cortex global directory (so baseline and cortex runs don't share state)
- Prepend the retrieved context to the next session's prompt as a "previously-established conventions" preamble

**Defer:**
- Tier-2 dynamic pattern mining (when built, swap the retrieval call here)
- Tier-3 AST detection on writes (when built, plug in via a PreToolUse hook in the harness, not here)
- MCP-server-as-injection-channel → only matters when a tool-using model can query Cortex on demand

## Approach

The condition fork is implemented by changing **what the harness does between sessions**, not by changing the harness or the prompts.

For both conditions, after each session completes, the harness captures the session's outputs as Cortex events. Before each session (except S1), the harness retrieves Cortex's context for "what conventions are in use in this codebase" and prepends it to the prompt.

The difference is a single boolean: in baseline, capture and retrieve are no-ops. In cortex, they actually run.

To prevent cross-condition contamination, each run gets its own Cortex global directory: `${TMPDIR}/cortex-libsvc-${condition}-${timestamp}/.cortex-state/`. Set `CORTEX_GLOBAL_DIR` (or equivalent) when invoking `cortex capture` and `cortex search`.

## Files to create / modify

| Path | Purpose |
|---|---|
| `internal/eval/v2/library_service_inject.go` | `Injector` interface + `NoOpInjector` (baseline) + `CortexInjector` (with cortex) |
| `internal/eval/v2/library_service_inject_test.go` | Unit tests for both injectors |
| `internal/eval/v2/library_service_runner.go` | Modify session loop to call injector before/after each session |
| `internal/eval/v2/library_service.go` | Wire condition → injector selection |

## Implementation steps

1. **Define `Injector` interface**:

   ```go
   type Injector interface {
       // Preamble returns content to prepend to the next session's prompt.
       // For S1 or baseline, return "".
       Preamble(ctx context.Context, sessionIdx int, workdir string) (string, error)

       // Record captures what the model did in the session that just finished.
       // Used to feed Cortex's pipeline. No-op for baseline.
       Record(ctx context.Context, sessionIdx int, workdir string, sessionResult SessionResult) error
   }
   ```

2. **Implement `NoOpInjector`** — both methods return immediately.

3. **Implement `CortexInjector`**:
   - Holds a path to a per-run Cortex global dir
   - `Record`: invoke `cortex capture` for each file changed in the session, with type=`code` and content=file diff. Or use `cortex feed` if it accepts a path. Reuse whatever `cmd/cortex/commands/capture.go` exposes.
   - `Preamble`: invoke `cortex search` (or `cortex inject-context`) with a fixed query like "what conventions are in use in this Go HTTP service for handlers, error handling, response shape, validation, tests" — capture the output and format as a markdown preamble:

     ```markdown
     ## Conventions established in prior sessions

     The following patterns are in use. Match them in your implementation:

     <retrieved context>
     ```

4. **Modify `(*LibraryServiceEvaluator).Run`** to construct the right `Injector` based on `cond`:
   ```go
   var injector Injector
   switch cond {
   case ConditionBaseline:
       injector = NoOpInjector{}
   case ConditionCortex:
       injector = NewCortexInjector(filepath.Join(workdir, ".cortex-state"))
   case ConditionFrontier:
       injector = NoOpInjector{} // frontier-with-cortex is a separate condition
   }
   ```

5. **In the session loop**, wrap the prompt with the injector preamble:
   ```go
   preamble, _ := injector.Preamble(ctx, idx, workdir)
   fullPrompt := preamble + "\n\n" + sessionPrompt
   harness.RunSession(ctx, fullPrompt, workdir)
   // ... build/test, capture SessionResult ...
   injector.Record(ctx, idx, workdir, sessionResult)
   ```

6. **Logging**: when verbose, print the preamble that was prepended for each session. This is critical for debugging — if results don't move, the first question is "what did Cortex inject?"

7. **Sanity checks**:
   - Verify the per-run Cortex global dir is empty before S1
   - Verify it has events after S1
   - Verify `cortex search` returns non-empty results before S2

## Verification

- Unit tests for `NoOpInjector`: returns empty preamble; `Record` is no-op.
- Unit tests for `CortexInjector`: with a mocked Cortex CLI, `Record` invokes `cortex capture` with expected args; `Preamble` includes the search output.
- Integration test: run baseline + cortex conditions back-to-back on the same model, score both. Cortex condition's preamble for S2 should be non-empty and reference patterns from S1.
- The full 4-condition matrix (A/B/C/D in `SPEC.md`) should run end-to-end without errors. Compare scores; if Cortex (B) doesn't beat baseline (A) on shape similarity, that's a thesis failure to surface immediately.

## Definition of done

- [ ] `Injector` interface defined; both implementations work
- [ ] `Run` selects injector by condition
- [ ] Session loop wraps prompts with preamble and records after
- [ ] Per-condition state isolation works (no cross-contamination)
- [ ] At least one full A vs B comparison run produces a `CompareRuns` report

## Suggested next plan

After this plan and Plans 01, 02, 04: run the eval for real and report the headline shape-similarity delta. That's the moment of truth for the thesis.
