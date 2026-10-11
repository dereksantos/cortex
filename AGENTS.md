# Working on the Cortex codebase

Guidance for any agent (Cortex itself included) changing this repository.
Cortex loads this file into its system prompt when it runs here.

Cortex is a general coding agent for any language, so its built-in prompt
(`cmd/cortex/prompt.go`) holds only short, language-neutral principles.
Everything specific to working on *this* Go codebase lives here.

For the architecture (the turn loop, the two-zone context window, memory
tools, subagents, configuration), see `CLAUDE.md` and the `docs/` it links.
Both are long: outline or grep them for the section you need, then read only
that span.

## Build and check

- `go build ./...` builds everything; `go build ./cmd/cortex` builds the binary.
- `go test ./...` runs the full suite.
- `./scripts/check.sh` runs gofmt, `go vet`, and golangci-lint, the same gate
  CI runs. A change is ready when all three pass.

## Go conventions

- Wrap errors with context: `fmt.Errorf("failed to X: %w", err)`.
- Constructors are `NewXxx(...)`; interfaces are nouns (`Provider`), not `IProvider`.
- `cmd/` holds entry points, `internal/` private code, `pkg/` public API.
  `pkg/` must not import `internal/`.
- LLM calls go through the `pkg/llm` provider interface; there is no second
  LLM layer.

## Tests

- Standard library `testing` only: `t.Errorf` / `t.Fatalf` / `t.Fatal`, no
  testify. Table-driven tests with `t.Run` subtests. Clean up with `defer` or
  `t.Cleanup`.
- Tests that run shell commands must use forms that work on both GNU and BSD
  (macOS) tools, e.g. `sed -i.bak` rather than bare `sed -i`
  (`internal/tools/shell_portability_test.go` guards this).
- **Tests are evidence.** An existing test's expected value records what
  someone decided correct behavior is; when it disagrees with your change, the
  burden of proof is on your change. Rewriting an expectation to match output
  you just produced turns a bug into the specification.
- **Test the path the requirement names.** A criterion is verified only
  through the surface it describes (the command, the API, the entry point the
  user invokes), not through a lower-level helper that surface routes through.
- **A test written from an issue's acceptance criteria is a spec.** Making it
  green by editing the test, its setup, or its expectation, instead of fixing
  the behavior, is a deviation to report, not a fix. Fix the defect in
  production code, never by patching around it in the test. If the criterion
  itself is wrong, say so with the evidence.
- If you change or remove an existing test, say what and why in your summary.

## Locating code

- Outline or grep to find where something lives, then `read_file` only the
  spans you need. Don't re-read content already in context.
- Read and write files with the dedicated tools, not shell: no `cat`/`sed`/`head`
  to read, no heredocs, `tee`, or `/tmp` scratch files to create.
- Before writing code that names a helper, constant, type, or path you haven't
  seen this session, grep for it and use what exists. Before adding a new
  package-level name (test helper, const, fixture), grep the whole package,
  including its other `_test.go` files, so it neither goes undefined nor
  collides. After a `.go` write, the write-sanity note lists duplicate and
  undefined names across the package; fix the whole batch at once.

## Debugging

- Check every error in test and fixture setup with `t.Fatal`, so a missing
  fixture can't masquerade as a code bug; confirm the fixture exists before
  suspecting the code under test.
- Debug with a focused test and `t.Logf` in the real package. Don't copy
  production code into scratch modules, and don't leave `DEBUG` prints in
  shipped code.
- A check that was blocked, refused, or declined leaves its result unknown.
  Don't guess it, and a check of something else doesn't stand in for it. Find
  another safe way to observe the same thing, or mark the claim unverified
  wherever you state it.

## Comments and claims

A comment says what the code does, not what you intended. After a behavioral
change, re-read the comments next to the span you edited (a reordered call,
a new overwrite, or a removed guard stales them) and fix or delete any that
no longer hold. Summaries, commit messages, and docs follow the same rule:
claiming behavior the code doesn't ship is a deviation to report.

## Review feedback

- Give every review finding an explicit disposition: addressed, deferred with
  a reason, or disputed. A finding with none of these was dropped, and it will
  come back next round.
- When a reviewer offers alternatives, pick one and say which; don't apply
  them all.
- When a reviewer gives example instances, fix the underlying class, not only
  the listed instances.
- Put things where the reviewer asked for them, not in a nearby file.

## Changing the harness prompt and tool text

The built-in system prompt, plan-mode prompts, subagent prompts, and tool
descriptions are sent to every model in every repository. Keep them general:

- `TestSystemPromptSizeBudget` caps the default composed system prompt at
  4,000 characters. It is paid on every call, and small local models follow
  long prompts poorly.
- `TestHarnessTextIsLanguageNeutral` rejects language- and toolchain-specific
  terms in any of those surfaces.
- When an observed failure suggests a new rule, prefer fixing the tool or the
  harness mechanism. If it is guidance, put it here (for this repo) or in an
  Agent Skill, not in the built-in prompt.
