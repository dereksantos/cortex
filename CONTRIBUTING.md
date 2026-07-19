# Contributing to Cortex

Thanks for looking at Cortex. This is research-grade software used daily by
its author — contributions are welcome, but keep changes small and
reviewable; see below for what the gate expects.

## What Cortex is

Cortex is a local-first coding harness built around one binary,
`cmd/cortex`: a coding agent for small and local models with working memory
built in. It manages a bounded, two-zone context window (recent turns
verbatim, older turns demoted to a cited outline, recallable on demand),
lets the model curate its own durable notes through memory tools
(`memory_write/read/search/forget`), and runs a bounded read-only `study`
subagent for locating code without blowing the context budget. See the
top of `CLAUDE.md` for the full picture and `docs/archive.md` for what
existed before the project was slimmed down to this scope.

## Development setup

Requires Go 1.26 (see `go.mod`).

```bash
git clone https://github.com/dereksantos/cortex.git
cd cortex
go build ./cmd/cortex
go test ./...
```

Optional one-time git hooks (`git config core.hooksPath .githooks`): a fast
`pre-commit` (gofmt + go vet on staged files) and a `pre-push` that runs
`golangci-lint` + `go test ./...`. Bypass only for real emergencies
(`--no-verify`); prefer fixing the underlying issue.

### The gate

`./scripts/check.sh all` runs gofmt + `go vet` + `golangci-lint` — this is
the same check CI runs and is authoritative. Individual stages:
`./scripts/check.sh {fmt,vet,lint}`. Before opening a PR:

```bash
go build ./cmd/cortex
go test ./...
./scripts/check.sh all
```

All three must be clean. `golangci-lint` is skipped locally with a warning
if not installed (`brew install golangci-lint` or
`go install github.com/golangci/golangci-lint/cmd/golangci-lint@latest`);
CI always runs it.

## Testing constraints

- Standard library `testing` only — no testify, no other assertion
  libraries. Use `t.Errorf` / `t.Fatalf` / `t.Fatal`.
- Table-driven tests with `t.Run` subtests where the cases share shape.
- Setup/teardown via `defer` (e.g. `defer os.RemoveAll(tempDir)`).
- Tests that require a live model fleet are env-gated (e.g.
  `CORTEX_LIVE_FLEET=1`) and are not part of the default `go test ./...`
  run or a PR requirement — see below.

## Code conventions

- **Error handling**: wrap with context — `fmt.Errorf("failed to X: %w", err)`.
- **Naming**: constructors are `NewXxx(cfg *config.Config)`; interfaces are
  nouns (`Provider`, `Storage`), never `IProvider`-style.
- **Package structure**: `cmd/` is entry points, `internal/` is private
  implementation, `pkg/` is public API. There is exactly one LLM layer,
  `pkg/llm` — go through its `Provider` interface (Anthropic, Ollama,
  OpenRouter, OpenAI-compatible) rather than adding a parallel client.
- Follow `gofmt`; keep functions and packages narrow rather than clever.

## Where things live

`CLAUDE.md`'s "Key files" section is the map: the turn loop
(`cmd/cortex/loop.go`), the `study` subagent (`cmd/cortex/study.go`), the
tool-call vocabulary (`internal/agent`), the tool surface
(`internal/tools`), the structural map (`internal/outline`), the
append-only journal (`internal/journal`), the shell-risk classifier
(`internal/shellrisk`), LLM providers (`pkg/llm`), and layered config
(`pkg/config`). For what to set and how to run it, see
`docs/configuration.md`. Read `CLAUDE.md` in full before making
non-trivial changes — it is the authoritative description of today's
architecture, kept current as the code moves.

## Making a change

1. Branch from `main`.
2. Keep the change small and reviewable — one concern per PR.
3. Add or update tests for the behavior you touched (table-driven, stdlib
   only, per above).
4. Update the relevant doc if the change affects documented behavior
   (`CLAUDE.md`, `docs/*.md`, `README.md`).
5. Run `go build ./cmd/cortex && go test ./... && ./scripts/check.sh all`
   and confirm it's clean.
6. Open a PR with a clear description of what changed and why. Live-fleet
   evals (anything gated on `CORTEX_LIVE_FLEET=1` or similar) are not
   required for a PR to merge — the deterministic suite is the bar.

Commit messages: short, imperative, and specific about the change; a
`type: summary` prefix (`fix:`, `feat:`, `docs:`, `refactor:`, `test:`,
`chore:`) is a reasonable convention but not enforced.

## Privacy

The journal (`.cortex/journal/`) is local-only by design — nothing in it
leaves the machine as part of normal operation. `journal.AssertLocalOnly`
exists as a code-review tripwire: if you add a code path that sends
journal data outbound, expect it to be flagged. `.cortex/` is gitignored;
don't check in session transcripts, journal segments, or config containing
keys.

## Reporting bugs and requesting features

Use the GitHub issue templates under `.github/ISSUE_TEMPLATE/`. Include
your Cortex version/commit, OS, and backend/model configuration — the
environment fields on the bug template match what `cortex --version` and
your config actually report.
