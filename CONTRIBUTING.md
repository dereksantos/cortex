# Contributing to Cortex

Thank you for your interest in Cortex. Contributions are welcome. Keep each
change small, so that a reviewer can read it in one pass.

Cortex is one binary, `cmd/cortex`: a coding agent for small and local
models, with working memory built in. `CLAUDE.md` describes the
architecture and is kept current with the code. `AGENTS.md` has the rules
for working on this repository. Read both before a large change.

## Build and test

Cortex needs Go 1.26 or later. The `toolchain` line in `go.mod` selects Go
1.27.2, which is the version CI uses.

```bash
git clone https://github.com/dereksantos/cortex.git
cd cortex
go build ./cmd/cortex     # build the binary
go test ./...             # run all tests
./scripts/check.sh        # gofmt, go vet, and golangci-lint
```

All three commands must pass before you open a pull request (PR).
`./scripts/check.sh fmt`, `vet`, and `lint` run one check each. If
golangci-lint is not installed, the script skips it with a warning. CI
always runs it.

Optional: run `git config core.hooksPath .githooks` to use the repository
hooks. The pre-commit hook runs gofmt and go vet on staged files. The
pre-push hook runs golangci-lint and `go test ./...`.

## Conventions

- Use the standard library `testing` package only. Do not use testify or
  other assertion libraries. Use `t.Errorf`, `t.Fatalf`, and `t.Fatal`.
- Write table-driven tests with `t.Run` subtests.
- Tests that need a live model are gated by an environment variable (for
  example `CORTEX_LIVE_FLEET=1`). They are not part of `go test ./...` and
  are not necessary for a PR.
- Wrap errors with context: `fmt.Errorf("failed to X: %w", err)`.
- Put entry points in `cmd/`, private code in `internal/`, and public API in
  `pkg/`. Send all LLM calls through the `pkg/llm` provider interface.
- When a change affects documented behavior, update the document in the
  same PR (`CLAUDE.md`, `README.md`, or `docs/*.md`). Some tests compare
  the README and `CLAUDE.md` with the code.

## Find an issue

1. Look at the issues with the label
   [`good first issue`](https://github.com/dereksantos/cortex/labels/good%20first%20issue)
   or [`help wanted`](https://github.com/dereksantos/cortex/labels/help%20wanted).
2. Optional: comment on the issue to say that you work on it. A comment
   holds the issue for 3 days.
3. Open a PR that fixes the issue. A working PR is the claim. You do not
   need permission to start.

If two PRs fix the same issue, the maintainer reviews the first PR that
meets the acceptance criteria and passes CI. A PR that reserves an issue
but does not fix it is closed.

## The self-development loop

Cortex fixes issues in its own repository (see "Cortex builds Cortex" in
the README). You can see its work in the repository:

- The loop works only on open issues that the maintainer labels `loop-ok`.
  It does not take an issue without this label, so your work on such an
  issue does not collide with the loop.
- Cortex opens a PR with the label `cortex-loop`. A bot account commits
  through [githug](https://githug.ai).
- Claude reviews the loop's PRs. Claude can read the code but cannot change
  it. Cortex fixes each finding, and the review starts again.
- Issues with the label `loop-finding` come from these reviews. The loop
  takes one only after the maintainer adds `loop-ok`.

PRs from people are welcome at the same time. The maintainer reviews them.

## Open a pull request

1. Make a branch from `main`. Keep one concern in each PR.
2. Add or update tests for the behavior that you change.
3. Fill in `.github/pull_request_template.md`: a summary, and a test plan
   that says what you ran. If you change terminal output, also do the
   terminal render checklist in the template.
4. Use a short, specific commit message. A `type: summary` prefix (`fix:`,
   `feat:`, `docs:`) is common but not mandatory.

These CI checks must pass before a merge: **Test on ubuntu-latest**,
**Test on macos-latest** (go vet, `go test -race`, build, and the study
contract), and **Commit identity**. CI also runs golangci-lint,
govulncheck, and a cross-platform build. A maintainer must approve the PR.

### PRs written by an agent

Every merged change must be connected to a person. For a PR from a fork,
that person is the GitHub account that opened it. If an agent wrote your
PR, say so in the description. The terms are the same: a real fix, a test,
and a description of what changed and why.

Optional: commit through githug so that your agent's commits have their own
identity and an `Accountable:` trailer with your name. Run
`hug init --repo <you>/cortex` and `hug setup --agent <name>` in the clone
of your fork. The **Commit identity** check accepts fork PRs in both cases.
It adds a notice to commits that did not come through githug. Agent work on
branches in this repository itself must use githug.

## Privacy

The journal (`.cortex/journal/`) stays on your computer.
`journal.AssertLocalOnly` marks this rule in the code. A change that sends
journal data out of the computer gets extra review. Do not commit session
transcripts, journal files, or a config that contains keys. In a git
workspace, Cortex writes `.cortex/.gitignore` to keep `.cortex/` out of git.

## Report a bug or request a feature

Use the issue templates in `.github/ISSUE_TEMPLATE/`. Include the output of
`cortex --version`, your operating system, and your backend and model
configuration.

## Releases (maintainer)

A `v*` tag starts `.github/workflows/release.yml`. GoReleaser
(`.goreleaser.yaml`) builds darwin and linux binaries for amd64 and arm64
(`CGO_ENABLED=0`) and attaches checksummed archives to a GitHub Release.
The release text points to `CHANGELOG.md`. GoReleaser does not generate
notes from commits.

1. In a PR, move the `[Unreleased]` entries of `CHANGELOG.md` under a new
   `## [X.Y.Z] - YYYY-MM-DD` heading. Keep an empty `[Unreleased]` heading.
2. Merge the PR.
3. Tag the merge commit: `git tag -a vX.Y.Z -m "vX.Y.Z"`.
4. Push the tag: `git push origin vX.Y.Z`.

For a local dry run before the tag, use
`goreleaser release --snapshot --clean`.

Versions follow pre-1.0 semver: a minor version can contain breaking
changes, and a patch version contains only fixes. `scripts/install.sh`
downloads the latest release. Its archive name must agree with
`name_template` in `.goreleaser.yaml`.
