# opencode-extension implementation loop

Self-pacing loop prompt for implementing the opencode integration. Mirrors
the Phase 8 pi.dev extension work in `packages/pi-cortex/`.

## Usage

```sh
# dynamic self-pacing (recommended — implementation steps have uneven duration)
/loop <paste contents below>

# or fixed cadence if you want to babysit
/loop 10m <paste contents below>
```

Cancel signals are baked into the prompt: regression in TODO 6, repeated
error across iterations without progress, or exit criteria met.

---

## Prompt

Implement the opencode integration for cortex, mirroring the existing pi.dev integration. Working dir: /Users/dereksantos/eng/projects/cortex. Branch: create feat/phase8-opencode-extension off main if you're not on it.

## Strict rules
- TDD: every TODO below writes the failing test FIRST, runs it to confirm it fails for the expected reason, THEN implements until green. No implementation before a failing test.
- Mirror packages/pi-cortex/ where the pattern translates; diverge only where opencode's plugin API differs:
  - Zod 4 (tool.schema is re-exported z) vs pi's typebox
  - `Plugin = (input) => Promise<Hooks>` async-export vs pi's `default function(pi): void`
  - Tool returns `string | {output: string, metadata?}` vs pi's `{content: [...], details}`
  - Hook is `"tool.execute.after": (input, output) => ...` with `(input.tool, input.args, output.output)` vs pi's `pi.on("tool_result", e => ...)` with `(e.toolName, e.input, e.content)`
  - Use `input.directory` (NOT process.cwd()) when shelling out
  - Discovery: flat file at `.opencode/plugins/cortex.ts` (NOT a subdir like pi's `.pi/extensions/cortex/`)
- Do NOT touch internal/eval/v2/grid.go or cmd/cortex/commands/eval_grid.go. The grid runner already type-asserts on `interface{ SetCortexExtensionEnabled(bool) }` at grid.go:303 — adding the method to OpenCodeHarness wires the toggle for free.
- Commit after each TODO completes (test + impl in one commit). Match the project's commit style: `git log --oneline -10`.
- NEVER push --force. NEVER --no-verify. If a hook fails, fix the underlying issue.

## Resume guidance
At iteration start: `git log --oneline main..HEAD` to see which TODOs are committed. Continue from the next uncommitted TODO. If the working tree is dirty, finish that TODO first before starting the next.

## TODOs (in order)

TODO 1 — Scaffolding
- Create packages/opencode-cortex/{package.json,tsconfig.json} mirroring packages/pi-cortex/.
- Deps: `@opencode-ai/plugin@^1.14.48`, devDeps: typescript, @types/node. Scripts: typecheck, test (node --test --experimental-strip-types on test/**/*.test.ts).
- Acceptance: `npm --prefix packages/opencode-cortex install && npm --prefix packages/opencode-cortex run typecheck` exits 0.

TODO 2 — Pure helpers
- Create packages/opencode-cortex/plugins/_helpers.ts exporting (copy verbatim from packages/pi-cortex/extensions/cortex/index.ts): resolveCortexBinary, formatRecallResults, redactSecrets, summarizeResultContent, CAPTURE_ALLOWLIST, SECRET_KEY_PATTERN, SECRET_VALUE_PATTERN, REDACTED, RESULT_SUMMARY_MAX, NO_RESULTS_TEXT.
- Tests in test/helpers.test.ts mirroring the pure-helper tests in packages/pi-cortex/test/extension.test.ts.
- Acceptance: `npm --prefix packages/opencode-cortex test` passes.

TODO 3 — cortex_recall tool only
- Create packages/opencode-cortex/plugins/cortex.ts exporting `export const CortexPlugin: Plugin = async ({directory, $}) => ({...})`.
- Implements cortex_recall ONLY (skip capture hook this commit).
- Tool returns `{output: formatRecallResults(entries), metadata: {count, binary}}` on success; benign output sentence on error (never throws — agent loop must not break).
- Shell out: execFile cortex binary with `cwd: process.env.CORTEX_PROJECT_ROOT?.trim() || directory`. Flags BEFORE positional query (Go flag.Parse stops at first non-flag).
- Tests: invoke the plugin function with a fake PluginInput; assert returned object has tool.cortex_recall with the right description; mock execFile via dependency injection or env-pointed binary stub. Cover happy path + error path.
- Acceptance: typecheck + test both pass.

TODO 4 — Go-side toggle on OpenCodeHarness
- Edit internal/eval/v2/library_service_opencode_harness.go mirroring library_service_pidev_harness.go:36-128:
  - Add field `cortexExtensionEnabled bool` to OpenCodeHarness
  - Add SetCortexExtensionEnabled(bool) and CortexExtensionEnabled() bool methods
  - Add const `EnvOpencodeCortexPluginSource = "CORTEX_OPENCODE_PLUGIN_SOURCE"`
  - Add func ensureOpencodeCortexPluginInstalled(workdir string) error — symlinks the source FILE (not dir) to <workdir>/.opencode/plugins/cortex.ts. Validate source is absolute path to a regular file (use os.Stat, not info.IsDir()).
  - In runSession: gate install on h.cortexExtensionEnabled; assert $CORTEX_BINARY is set; default $CORTEX_PROJECT_ROOT to cwd. Mirror pi.dev harness lines 181-201.
- Tests in library_service_opencode_harness_test.go mirroring library_service_pidev_harness_test.go:462-601: SetCortexExtensionEnabled round-trip, install creates symlink when enabled, install does NOT touch .opencode/ when disabled, install errors on unset/relative/missing $CORTEX_OPENCODE_PLUGIN_SOURCE.
- Acceptance: `go test ./internal/eval/v2/...` passes.

TODO 5 — Capture hook
- Add `"tool.execute.after"` hook to plugins/cortex.ts. Use CAPTURE_ALLOWLIST from helpers. Event type: `opencode_tool_call` (parallel to pi.dev's pi_tool_call).
- Use spawn + unref (fire-and-forget) so the agent loop never blocks. `cwd: process.env.CORTEX_PROJECT_ROOT || directory`. Errors silently swallowed.
- Tests: trigger the returned hook with a fake event; assert spawn was called with expected args. Allowlisted tool fires capture; non-allowlisted does not. Secrets redacted in args_redacted.
- Acceptance: typecheck + test pass; no regression in TODO 4 Go tests.

TODO 6 — End-to-end eval
- Build: `go build -o bin/cortex ./cmd/cortex`
- Env: `export CORTEX_BINARY=$PWD/bin/cortex CORTEX_OPENCODE_PLUGIN_SOURCE=$PWD/packages/opencode-cortex/plugins/cortex.ts CORTEX_PROJECT_ROOT=$PWD`
- Run: `./bin/cortex eval grid --harnesses opencode --strategies baseline,cortex_extension --models openai/gpt-oss-20b:free --scenarios test/evals/v2`
- Read result: `./bin/cortex eval grid --report-summary`
- Acceptance: cortex_extension pass-rate ≥ baseline pass-rate. Save the lift-table output verbatim into a scratch file (e.g., /tmp/opencode-lift.txt) for the PR body.
- If regression: STOP and report the regression with the lift table — do not paper over. Do not adjust the strategy to avoid the regression; surface it.

TODO 7 — PR
- Push: `git push -u origin feat/phase8-opencode-extension`
- Open PR with `gh pr create --base main` — body must include: (1) scope summary, (2) lift-table excerpt from TODO 6, (3) test pass summary, (4) "files mirrored from pi-cortex / where they diverge" section listing the API differences.
- Watch checks: `gh pr checks <PR#> --watch`. If any fail: read logs with `gh run view --log-failed`, fix root cause, push a new commit (no amend, no force).
- Loop until all checks green.

## Exit criteria (ALL must hold before reporting done)

1. All TODOs 1-7 committed on feat/phase8-opencode-extension.
2. `go test ./internal/eval/v2/...` passes locally.
3. `npm --prefix packages/opencode-cortex test` passes locally.
4. PR open against main, all GitHub Actions checks green.
5. Lift table from TODO 6 shows cortex_extension pass-rate ≥ baseline pass-rate.

When all 5 hold, report ONE message containing: PR URL, lift-table excerpt, "checks passing: X/X", one-sentence confidence assessment ("production-ready" or specific reservations). Then STOP — do not call ScheduleWakeup.

## Self-pacing

- After each TODO completes successfully, schedule next wake in 120-270s (stay in cache window; no point waiting longer with momentum).
- Long process (CI run, eval grid run): arm a Monitor for the gh-checks output or grid-completion log line, with 1200-1800s fallback heartbeat.
- If the same error recurs across two iterations without measurable progress, STOP and report the blocker. Do not loop on stuck state.
