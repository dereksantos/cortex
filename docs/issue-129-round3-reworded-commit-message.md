selfdev: address review round 3 (#129)

Checks passed: ./scripts/check.sh (gofmt, go vet, golangci-lint), go build ./..., go test ./...

Cortex's summary (unverified):
All five review findings are fixed, and all gates pass (`./scripts/check.sh`, `go build ./...`, `go test ./...`).

**Major — docs** (`docs/configuration.md`, `CLAUDE.md`)
- Rewrote the post-edit hook section: trust is the only gate, read only from the user config's `project.trusted` (the repo's own config is dropped by the merge — it can never mark itself trusted); untrusted runs nothing with a one-line note on the session's first edit; trusted runs any applicable per-file/per-package command as a shell-free argv, with shell-syntax templates skipped by note; kept the 10s budget text and documented the 2000-byte output cap.
- Documented the `project.trusted` field (added to the `project` JSON example) and the `cortex project trust add|remove|list` subcommand.
- Updated the CLAUDE.md bullet to match and added the `cortex project trust <add|remove|list>` row to the CLAUDE.md commands table. Also added it to the `--help` subcommand list (`main.go`, so the help surface and tests stay in sync) and the README's command table — same documentation class of fix, one line each.

**Major — test clobbering real user config** (`cmd/cortex/project_trust_test.go`)
- `TestWorkspaceTrustedMatching` now starts with `t.Setenv("CORTEX_HOME", t.TempDir())` before `userConfigPath()`, and the manual `os.Remove` cleanup is dropped (t.TempDir cleanup suffices). A developer/CI with `CORTEX_HOME` set is no longer at risk.

**Major — misleading hook test** (`internal/tools/project_command_hook_test.go`)
- Renamed `TestPostEditHookTrustedIsUserConfigOnly` → `TestPostEditHookUntrustedNoteOnWrite` and dropped its unused fixtures (it never asserted the repo-claim behavior it claimed to). The real repo-claim coverage is `TestWorkspaceTrustedEndToEndFromConfigs` in `cmd/cortex`.

**Removed test functions (per `git show bfdec6e -- '*_test.go'`)**

From `internal/shellrisk/project_command_test.go` (deleted file):
- `TestAllowlistProjectCommand` — tested the deleted `AllowlistProjectCommand` allowlist (recognized single-invocation tools).
- `TestAllowlistProjectCommand_MaliciousScriptDenied` — tested the deleted allowlist refusing malicious script invocations.
- `TestAllowlistProjectCommand_PathInWorkspaceRefused` — tested the deleted allowlist refusing repo-local binary paths.
- `TestAllowlistProjectCommand_NoModelCall` — tested the deleted allowlist's no-classifier-needed decision.
- `TestAllowlistProjectCommand_Tiers` — tested the deleted `ProjectCommandTier` (Inert/Code) two-tier split.
- `TestAppendNPXNoInstall` — tested the deleted `--no-install` argument handling for `npx` project commands.
- `TestSubstitutedArgs` — tested the deleted post-allowlist `{file}`/`{dir}` substitution path.

From `internal/tools/project_command_hook_test.go`:
- `TestPostEditHookRefusesNonAllowlistedFormat` — tested the deleted allowlist (a non-allowlisted format command being refused).
- `TestAllowlistProjectCommandRefusesShellControl` — tested the deleted allowlist refusing shell control operators.
- `TestPostEditHookTrustGateEndToEnd` — tested the trust gate end-to-end; replacement coverage: `TestPostEditHookUntrustedRunsNothing`, `TestPostEditHookTrustedRunsArbitraryCommand`, and `cmd/cortex`'s `TestWorkspaceTrustedEndToEndFromConfigs`.
- `TestSplitAndCheckArgsRefusesShellShapedPaths` — tested the deleted `splitAndCheckArgs` refusing shell-shaped paths.
- `TestSplitAndCheckArgsPinsNPXBeforeTheTool` — tested the deleted `splitAndCheckArgs` npx pinning.

Generated-By: Cortex (qwen3.8-27b, selfdev loop)
Accountable: Derek Santos
